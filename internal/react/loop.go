package react

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/google/uuid"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
	"k8s.io/klog/v2"
)

type State int

const (
	StateIdle State = iota
	StateRunning
	StateWaitingApproval
	StateDone
	StateExited
)

type PendingCall struct {
	FunctionCall     gollm.FunctionCall
	ParsedToolCall   *tools.ToolCall
	IsInteractive    bool
	InteractiveError error
	ModifiesResource string
}

const internalResourceGuideLookupCall = "__resource_guide_lookup__"
const internalRequestContextCall = "__request_context__"

type Loop struct {
	cfg      *config.Config
	llm      gollm.Client
	chat     gollm.Chat
	lang     *langTranslator
	registry *toolconnector.Registry
	executor sandbox.Executor
	workDir  string

	input  chan any
	output chan *api.Message

	state              State
	currIteration      int
	currChatContent    []any
	contextBlockHashes map[string]struct{}
	pendingCalls       []PendingCall
	skipPermissions    bool

	systemPrompt           string
	promptOptions          promptOptions
	toolProfile            ToolProfile
	requestIntent          RequestIntent
	originalQuery          string
	requestContext         *requestContext
	resourceClassification *resourceClassification
	resourceDiscoveryCache map[string]resourceClassification
	lastContextError       *contextError
	injectedGuides         map[string]guideRef
	completedActions       []actionRecord
	actionSeq              int
	lastCompactedActionSeq int
	contextApproxTokens    int
	lastAssistantText      string
	lastProgressText       string
	initialGuideAttempted  bool
	resourceGuideInjected  bool
	resourceGuideEvidence  []string
	resourceGuideQueries   map[string]struct{}

	cancel context.CancelFunc
	once   sync.Once
}

func New(cfg *config.Config) (*Loop, error) {
	setupProviderEnv(cfg)
	llmClient, err := gollm.NewClient(context.Background(), cfg.LLMProvider)
	if err != nil {
		return nil, fmt.Errorf("LLM 클라이언트 생성 실패 (%s): %w", cfg.LLMProvider, err)
	}
	return &Loop{
		cfg:    cfg,
		llm:    llmClient,
		input:  make(chan any, 1),
		output: make(chan *api.Message, 32),
		state:  StateIdle,
	}, nil
}

func (l *Loop) Start(ctx context.Context, initialQuery string) error {
	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	if err := l.init(loopCtx); err != nil {
		cancel()
		return err
	}

	go l.run(loopCtx, strings.TrimSpace(initialQuery))
	return nil
}

func (l *Loop) Output() <-chan *api.Message {
	return l.output
}

func (l *Loop) SendInput(input any) {
	select {
	case l.input <- input:
	default:
		klog.Warningf("react loop input 채널이 가득 찼습니다. 입력 버림: %v", input)
	}
}

func (l *Loop) Close() {
	l.once.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		select {
		case l.input <- io.EOF:
		default:
		}
		if l.registry != nil {
			_ = l.registry.Close()
		}
		if l.executor != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = l.executor.Close(ctx)
			cancel()
		}
		if l.llm != nil {
			_ = l.llm.Close()
		}
		if l.workDir != "" {
			_ = os.RemoveAll(l.workDir)
		}
	})
}

func (l *Loop) init(ctx context.Context) error {
	workDir, err := os.MkdirTemp("", "k8s-assistant-*")
	if err != nil {
		return fmt.Errorf("작업 디렉터리 생성 실패: %w", err)
	}
	l.workDir = workDir
	l.executor = sandbox.NewLocalExecutor()

	registry, err := toolconnector.NewRegistry(ctx, l.executor, l.cfg.MCPClient)
	if err != nil {
		return fmt.Errorf("tool registry 초기화 실패: %w", err)
	}
	l.registry = registry

	l.lang = newLangTranslator(l.cfg)

	l.requestIntent = RequestIntentGeneral
	l.toolProfile = selectToolProfile(registry.Tools, l.requestIntent, "")
	l.promptOptions = l.newPromptOptions(l.requestIntent, false, false)

	return l.resetChatSession()
}

func (l *Loop) resetChatSession() error {
	systemPrompt, err := buildSystemPromptWithOptions(l.cfg.PromptTemplateFile, l.registry.Tools, l.promptOptions)
	if err != nil {
		return err
	}
	l.systemPrompt = systemPrompt
	l.toolProfile = l.promptOptions.ToolProfile
	l.contextApproxTokens = estimateContextTokens(systemPrompt)
	l.chat = gollm.NewRetryChat(
		l.llm.StartChat(l.systemPrompt, l.cfg.Model),
		gollm.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 10 * time.Second,
			MaxBackoff:     60 * time.Second,
			BackoffFactor:  2,
			Jitter:         true,
		},
	)
	if !l.cfg.EnableToolUseShim {
		defs := collectFunctionDefinitionsForProfile(l.registry.Tools, l.toolProfile)
		if err := l.chat.SetFunctionDefinitions(defs); err != nil {
			return fmt.Errorf("tool function definition 주입 실패: %w", err)
		}
	}
	return nil
}

func (l *Loop) run(ctx context.Context, initialQuery string) {
	defer close(l.output)
	defer l.Close()

	if initialQuery != "" {
		if err := l.startQuery(initialQuery); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.state = StateDone
		}
	}

	for {
		select {
		case <-ctx.Done():
			l.state = StateExited
			return
		default:
		}

		switch l.state {
		case StateIdle, StateDone:
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, ">>>")
			if !l.waitForInput(ctx) {
				return
			}
		case StateWaitingApproval:
			if !l.waitForApproval(ctx) {
				return
			}
		case StateRunning:
			if err := l.runIteration(ctx); err != nil {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
				l.pendingCalls = nil
				l.currChatContent = nil
				l.currIteration = 0
				l.state = StateDone
			}
		case StateExited:
			return
		}
	}
}

func (l *Loop) waitForInput(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
			l.state = StateExited
			return false
		}
		input, ok := raw.(*api.UserInputResponse)
		if !ok {
			return true
		}
		query := strings.TrimSpace(input.Query)
		if query == "" {
			l.state = StateDone
			return true
		}
		if handled := l.handleMetaQuery(ctx, query); handled {
			return true
		}
		if err := l.startQuery(query); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.state = StateDone
			return true
		}
		return true
	}
}

func (l *Loop) waitForApproval(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.state = StateExited
			return false
		}
		choice, ok := raw.(*api.UserChoiceResponse)
		if !ok {
			return true
		}
		if err := l.handleApproval(ctx, choice.Choice); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.state = StateDone
		}
		return true
	}
}

func (l *Loop) startQuery(query string) error {
	intent := classifyRequestIntent(query)
	l.requestIntent = intent
	priorState := l.priorConversationStateMessage()
	l.toolProfile = selectToolProfile(l.registry.Tools, intent, query)
	l.promptOptions = l.newPromptOptions(intent, false, false)
	if err := l.resetChatSession(); err != nil {
		return err
	}
	l.addMessage(api.MessageSourceUser, api.MessageTypeText, query)
	l.currIteration = 0
	l.currChatContent = nil
	if priorState != "" {
		l.currChatContent = append(l.currChatContent, priorState)
	}
	l.currChatContent = append(l.currChatContent, query)
	l.contextBlockHashes = nil
	l.pendingCalls = nil
	l.originalQuery = query
	l.requestContext = nil
	l.resourceClassification = nil
	l.lastContextError = nil
	l.injectedGuides = nil
	l.completedActions = nil
	l.actionSeq = 0
	l.lastCompactedActionSeq = 0
	l.lastAssistantText = ""
	l.lastProgressText = ""
	l.initialGuideAttempted = false
	l.resourceGuideInjected = false
	l.resourceGuideEvidence = nil
	l.resourceGuideQueries = nil
	l.state = StateRunning
	return nil
}

func (l *Loop) newPromptOptions(intent RequestIntent, includeGuidance bool, includeClusterAPI bool) promptOptions {
	toolProfile := l.toolProfile
	if len(toolProfile.ToolNames) == 0 && l.registry != nil {
		toolProfile = selectToolProfile(l.registry.Tools, intent, l.originalQuery)
	}
	return promptOptions{
		EnableToolUseShim:          l.cfg.EnableToolUseShim,
		ReadOnly:                   l.cfg.ReadOnly,
		UserLanguage:               l.cfg.Lang.Language,
		TranslateOutput:            l.lang != nil && l.lang.enabled(),
		IncludeGuidanceProtocol:    includeGuidance,
		IncludeManifestGuidelines:  intent == RequestIntentManifest,
		IncludeClusterAPIGuardrail: includeClusterAPI,
		ToolProfile:                toolProfile,
	}
}

func (l *Loop) handleMetaQuery(ctx context.Context, query string) bool {
	switch query {
	case "clear", "reset":
		l.requestIntent = RequestIntentGeneral
		l.toolProfile = selectToolProfile(l.registry.Tools, l.requestIntent, "")
		l.promptOptions = l.newPromptOptions(l.requestIntent, false, false)
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.state = StateDone
			return true
		}
		l.clearConversationState()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "대화 컨텍스트를 초기화했습니다.")
		l.state = StateDone
		return true
	case "exit", "quit":
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
		l.state = StateExited
		return true
	case "model":
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Current model is `"+l.cfg.Model+"`")
		l.state = StateDone
		return true
	case "models":
		models, err := l.llm.ListModels(ctx)
		if err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, err.Error())
		} else {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Available models:\n\n  - "+strings.Join(models, "\n  - "))
		}
		l.state = StateDone
		return true
	case "tools":
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Available tools:\n\n  - "+strings.Join(l.registry.Tools.Names(), "\n  - "))
		l.state = StateDone
		return true
	default:
		return false
	}
}

func (l *Loop) clearConversationState() {
	l.currIteration = 0
	l.currChatContent = nil
	l.contextBlockHashes = nil
	l.pendingCalls = nil
	l.originalQuery = ""
	l.requestContext = nil
	l.resourceClassification = nil
	l.lastContextError = nil
	l.injectedGuides = nil
	l.completedActions = nil
	l.actionSeq = 0
	l.lastCompactedActionSeq = 0
	l.contextApproxTokens = estimateContextTokens(l.systemPrompt)
	l.lastAssistantText = ""
	l.lastProgressText = ""
	l.initialGuideAttempted = false
	l.resourceGuideInjected = false
	l.resourceGuideEvidence = nil
	l.resourceGuideQueries = nil
}

func (l *Loop) noteContextContent(contents ...any) {
	l.contextApproxTokens += estimateContextTokens(contents...)
}

func (l *Loop) contextCompactThresholdTokens() int {
	return l.contextLimitTokens() * 80 / 100
}

func (l *Loop) contextLimitTokens() int {
	if value := strings.TrimSpace(os.Getenv("K8S_ASSISTANT_CONTEXT_LIMIT_TOKENS")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	model := strings.ToLower(strings.TrimSpace(l.cfg.Model))
	switch {
	case strings.Contains(model, "llama-3.3"):
		return 32768
	case strings.Contains(model, "gpt-4o"), strings.Contains(model, "gpt-4.1"), strings.Contains(model, "gpt-5"):
		return 128000
	case strings.Contains(model, "gemini-1.5"), strings.Contains(model, "gemini-2.0"), strings.Contains(model, "gemini-2.5"):
		return 1000000
	default:
		return 32768
	}
}

func estimateContextTokens(values ...any) int {
	total := 0
	for _, value := range values {
		total += estimateTextTokens(fmt.Sprintf("%v", value))
	}
	return total
}

func estimateTextTokens(text string) int {
	asciiChars := 0
	nonASCII := 0
	for _, r := range text {
		if r < 128 {
			asciiChars++
		} else {
			nonASCII++
		}
	}
	return (asciiChars+3)/4 + nonASCII
}

func (l *Loop) runIteration(ctx context.Context) error {
	if l.currIteration >= l.cfg.MaxIterations {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Maximum number of iterations reached.")
		l.currIteration = 0
		l.currChatContent = nil
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}

	if l.shouldCompactBeforeNextSend() {
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}

	sentContent := append([]any(nil), l.currChatContent...)
	streamedText, functionCalls, err := l.sendAndCollectStreaming(ctx, sentContent)
	if err != nil {
		if !isContextLengthError(err) {
			return err
		}
		if ok := l.compactAfterContextLengthError(err); !ok {
			return err
		}
		sentContent = append([]any(nil), l.currChatContent...)
		streamedText, functionCalls, err = l.sendAndCollectStreaming(ctx, sentContent)
		if err != nil {
			return err
		}
	}
	l.noteContextContent(sentContent...)
	l.currChatContent = nil

	if len(functionCalls) == 0 {
		if strings.TrimSpace(streamedText) != "" {
			rawModelText := streamedText
			l.contextApproxTokens += estimateContextTokens(rawModelText)
			displayText := l.translateModelText(ctx, rawModelText)
			l.addMessage(api.MessageSourceModel, api.MessageTypeText, displayText)
			l.lastAssistantText = rawModelText
		}
		l.currIteration = 0
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}
	deferredProgressText := strings.TrimSpace(streamedText)

	var requestContextHandled bool
	functionCalls, requestContextHandled = l.consumeRequestContext(ctx, functionCalls)
	if requestContextHandled {
		return nil
	}

	if handled := l.handleRequestedResourceGuideLookup(ctx, functionCalls); handled {
		return nil
	}

	if handled := l.rejectInconsistentActionTargets(functionCalls); handled {
		return nil
	}

	if handled := l.interceptCustomResourceFunctionCalls(ctx, functionCalls); handled {
		return nil
	}

	functionCalls = l.rejectAssistantManagedToolCalls(functionCalls)
	if len(functionCalls) == 0 {
		l.currIteration++
		return nil
	}

	pending, err := l.analyzeToolCalls(ctx, functionCalls)
	if err != nil {
		return err
	}
	l.pendingCalls = pending

	for _, call := range pending {
		if call.IsInteractive {
			errText := call.InteractiveError.Error()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, errText)
			l.appendToolObservation(call, map[string]any{"error": errText})
			l.pendingCalls = nil
			l.currIteration++
			return nil
		}
	}

	if l.cfg.ReadOnly && l.hasModifyingCalls() {
		l.rejectReadOnlyModifyingCalls()
		return nil
	}
	if !l.skipPermissions && l.hasModifyingCalls() {
		l.emitAcceptedProgressText(ctx, deferredProgressText)
		l.requestApproval()
		l.state = StateWaitingApproval
		return nil
	}

	l.emitAcceptedProgressText(ctx, deferredProgressText)
	return l.dispatchToolCalls(ctx)
}

func (l *Loop) sendAndCollectStreaming(ctx context.Context, contents []any) (string, []gollm.FunctionCall, error) {
	stream, err := l.chat.SendStreaming(ctx, contents...)
	if err != nil {
		return "", nil, err
	}
	if l.cfg.EnableToolUseShim {
		stream, err = candidateToShimCandidate(stream)
		if err != nil {
			return "", nil, err
		}
	}

	var streamedText string
	var functionCalls []gollm.FunctionCall
	for response, err := range stream {
		if err != nil {
			return "", nil, err
		}
		if response == nil {
			break
		}
		if len(response.Candidates()) == 0 {
			return "", nil, fmt.Errorf("LLM 응답 후보가 없습니다")
		}
		for _, part := range response.Candidates()[0].Parts() {
			if text, ok := part.AsText(); ok {
				streamedText += text
			}
			if calls, ok := part.AsFunctionCalls(); ok && len(calls) > 0 {
				functionCalls = append(functionCalls, calls...)
			}
		}
	}
	return streamedText, functionCalls, nil
}

func (l *Loop) emitProgressText(ctx context.Context, rawText string) {
	rawText = strings.TrimSpace(rawText)
	if rawText == "" {
		return
	}
	if rawText == strings.TrimSpace(l.lastProgressText) {
		return
	}
	displayText := l.translateModelText(ctx, rawText)
	l.addMessage(api.MessageSourceModel, api.MessageTypeText, displayText)
	l.lastProgressText = rawText
}

func (l *Loop) emitAcceptedProgressText(ctx context.Context, progressText string) {
	progressText = strings.TrimSpace(progressText)
	if progressText == "" {
		return
	}
	l.contextApproxTokens += estimateContextTokens(progressText)
	l.lastAssistantText = progressText
	l.emitProgressText(ctx, progressText)
}

func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context length",
		"context_length_exceeded",
		"maximum context",
		"max context",
		"max_num_tokens",
		"prompt length",
		"too many tokens",
		"token limit",
		"exceed max",
		"exceeds max",
		"should not exceed",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (l *Loop) consumeRequestContext(ctx context.Context, calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalRequestContextCall {
			remaining = append(remaining, call)
			continue
		}
		if l.requestContext != nil {
			continue
		}
		request, ok := requestContextFromFunctionCall(call)
		if !ok {
			l.appendCorrectionWithCompaction("invalid_request_context", "Request context was invalid. Return one corrected response with primary_target, scope, and resource_class before choosing the next action. Namespace is a scope field unless the user explicitly asks about a Namespace object; do not set primary_target.resource=namespace while also setting scope.namespace.")
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		l.requestContext = &request
		classification := l.classifyResourceByDiscovery(ctx, request.PrimaryTarget.Resource)
		l.resourceClassification = &classification
		if l.shouldRunInitialResourceGuideLookup(request, classification) {
			l.initialGuideAttempted = true
			resource := normalizeKubectlResource(request.PrimaryTarget.Resource)
			l.searchAndInjectResourceGuide(ctx, resource, l.initialResourceGuideQuery(request))
			return nil, true
		}
	}
	return remaining, false
}

func requestContextFromFunctionCall(call gollm.FunctionCall) (requestContext, bool) {
	var request requestContext
	primaryRaw, ok := call.Arguments["primary_target"].(map[string]any)
	if !ok {
		return request, false
	}
	resource, _ := primaryRaw["resource"].(string)
	name, _ := primaryRaw["name"].(string)
	resourceClass, _ := call.Arguments["resource_class"].(string)
	request.PrimaryTarget = requestPrimaryTarget{
		Resource: strings.TrimSpace(resource),
		Name:     strings.TrimSpace(name),
	}
	if scopeRaw, ok := call.Arguments["scope"].(map[string]any); ok {
		namespace, _ := scopeRaw["namespace"].(string)
		request.Scope.Namespace = strings.TrimSpace(namespace)
	}
	switch strings.ToLower(strings.TrimSpace(resourceClass)) {
	case "built_in", "custom_resource", "unknown":
		request.ResourceClass = strings.ToLower(strings.TrimSpace(resourceClass))
	default:
		return requestContext{}, false
	}
	if request.PrimaryTarget.Resource == "" {
		return requestContext{}, false
	}
	if request.PrimaryTarget.Resource == "namespace" && request.Scope.Namespace != "" {
		return requestContext{}, false
	}
	return request, true
}

func (l *Loop) shouldRunInitialResourceGuideLookup(request requestContext, classification resourceClassification) bool {
	if l.initialGuideAttempted {
		return false
	}
	resource := normalizeKubectlResource(strings.ToLower(request.PrimaryTarget.Resource))
	if resource == "" {
		return false
	}
	return classification.Kind == resourceClassificationCRD
}

func (l *Loop) initialResourceGuideQuery(request requestContext) string {
	lines := []string{
		l.originalQuery,
		"primary target resource: " + request.PrimaryTarget.Resource,
	}
	if l.resourceClassification != nil {
		lines = append(lines,
			"resource classification: "+l.resourceClassification.Kind,
			"classification source: "+l.resourceClassification.Source,
		)
		if l.resourceClassification.APIGroup != "" {
			lines = append(lines, "api group: "+l.resourceClassification.APIGroup)
		}
	}
	if request.PrimaryTarget.Name != "" {
		lines = append(lines, "primary target name: "+request.PrimaryTarget.Name)
	}
	if request.Scope.Namespace != "" {
		lines = append(lines, "scope namespace: "+request.Scope.Namespace)
	}
	return strings.Join(lines, "\n")
}

func (l *Loop) rejectInconsistentActionTargets(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		message, invalid := inconsistentActionTargetMessage(call)
		if !invalid {
			continue
		}
		if !l.appendCorrectionWithCompaction("inconsistent_action_target", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 action target 불일치로 루프를 중단했습니다:\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.state = StateDone
			return true
		}
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
		return true
	}
	return false
}

func inconsistentActionTargetMessage(call gollm.FunctionCall) (string, bool) {
	command, ok := kubectlCommandFromFunctionCall(call)
	if !ok {
		return "", false
	}
	target, ok := actionTargetFromFunctionCall(call)
	if !ok {
		return "", false
	}
	if target.Resource == "namespace" && target.Namespace != "" {
		return fmt.Sprintf("Action target declared resource %q with namespace scope %q, but Namespace objects are cluster-scoped. Use resource=namespace only when diagnosing a Namespace object itself; otherwise keep namespace as scope for the real target resource.", target.Resource, target.Namespace), true
	}
	if target.Resource != "" && !commandMentionsResource(command, target.Resource) {
		return fmt.Sprintf("Action target declared resource %q, but command %q does not include that resource. Preserve the declared target and return one corrected next action.", target.Resource, command), true
	}
	if target.Name != "" && !commandMentionsToken(command, target.Name) && !commandUsesSelectorForName(command, target.Name) {
		return fmt.Sprintf("Action target declared name %q, but command %q does not include that name. Preserve the declared target and return one corrected next action.", target.Name, command), true
	}
	if target.Namespace != "" && !commandUsesNamespace(command, target.Namespace) {
		return fmt.Sprintf("Action target declared namespace %q, but command %q omits that namespace. Preserve the declared target and return one corrected next action with `-n %s` or `--namespace=%s`.", target.Namespace, command, target.Namespace, target.Namespace), true
	}
	return "", false
}

func actionTargetFromFunctionCall(call gollm.FunctionCall) (actionTarget, bool) {
	raw, ok := call.Arguments["target"].(map[string]any)
	if !ok {
		return actionTarget{}, false
	}
	resource, _ := raw["resource"].(string)
	namespace, _ := raw["namespace"].(string)
	name, _ := raw["name"].(string)
	target := actionTarget{
		Resource:  strings.TrimSpace(resource),
		Namespace: strings.TrimSpace(namespace),
		Name:      strings.TrimSpace(name),
	}
	if target.Resource == "" && target.Namespace == "" && target.Name == "" {
		return actionTarget{}, false
	}
	return target, true
}

func commandMentionsToken(command, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return true
	}
	for _, field := range strings.Fields(command) {
		if strings.ToLower(strings.Trim(field, "'\"")) == token {
			return true
		}
	}
	return false
}

func commandMentionsResource(command, resource string) bool {
	wants := normalizedResourceList(resource)
	if len(wants) == 0 {
		return true
	}
	var mentioned []string
	for _, field := range strings.Fields(strings.ToLower(command)) {
		for _, part := range strings.Split(strings.Trim(field, "'\","), ",") {
			part = normalizeKubectlResource(strings.TrimSpace(part))
			if part != "" {
				mentioned = append(mentioned, part)
			}
		}
	}
	for _, want := range wants {
		if !resourceListContains(mentioned, want) {
			return false
		}
	}
	return true
}

func normalizedResourceList(resource string) []string {
	var resources []string
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(resource)), ",") {
		part = normalizeKubectlResource(strings.TrimSpace(part))
		if part != "" {
			resources = append(resources, part)
		}
	}
	return resources
}

func resourceListContains(resources []string, want string) bool {
	for _, resource := range resources {
		if resourceNamesEquivalent(resource, want) {
			return true
		}
	}
	return false
}

func resourceNamesEquivalent(a, b string) bool {
	a = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(a)))
	b = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(b)))
	if a == "" || b == "" {
		return false
	}
	a = strings.Split(a, ".")[0]
	b = strings.Split(b, ".")[0]
	if a == b {
		return true
	}
	return singularizeResourceName(a) == b || singularizeResourceName(b) == a
}

func singularizeResourceName(resource string) string {
	switch {
	case strings.HasSuffix(resource, "ies") && len(resource) > 3:
		return strings.TrimSuffix(resource, "ies") + "y"
	case strings.HasSuffix(resource, "s") && len(resource) > 1:
		return strings.TrimSuffix(resource, "s")
	default:
		return resource
	}
}

func commandUsesSelectorForName(command, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	lower := strings.ToLower(command)
	return strings.Contains(lower, "cluster-name="+name) ||
		strings.Contains(lower, "cluster-name: "+name) ||
		strings.Contains(lower, "cluster.x-k8s.io/cluster-name="+name)
}

func commandUsesNamespace(command, namespace string) bool {
	fields := strings.Fields(command)
	for i, field := range fields {
		trimmed := strings.Trim(field, "'\"")
		if (trimmed == "-n" || trimmed == "--namespace") && i+1 < len(fields) && strings.Trim(fields[i+1], "'\"") == namespace {
			return true
		}
		if strings.HasPrefix(trimmed, "--namespace=") && strings.TrimPrefix(trimmed, "--namespace=") == namespace {
			return true
		}
	}
	return false
}

func (l *Loop) handleRequestedResourceGuideLookup(ctx context.Context, calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		if call.Name != internalResourceGuideLookupCall {
			continue
		}
		request, ok := resourceGuideLookupFromFunctionCall(call)
		if !ok {
			l.appendCorrectionWithCompaction("invalid_resource_guide_lookup", "Resource guide lookup request was invalid. Continue with the next safest kubectl diagnostic.")
			l.currIteration++
			l.state = StateRunning
			return true
		}
		if l.resourceClassification == nil || l.resourceClassification.Kind != resourceClassificationCRD {
			l.appendCorrectionWithCompaction("resource_guide_without_confirmed_crd", "Resource guide lookup is only available after runtime discovery confirms the primary target is a CRD. Continue with the next safest kubectl diagnostic and do not infer a CRD or Cluster API family from the name alone.")
			l.currIteration++
			l.state = StateRunning
			return true
		}
		query := l.resourceGuideRefinementQuery(request)
		if l.resourceGuideQueryAlreadyUsed(query) {
			l.appendCorrectionWithCompaction("duplicate_resource_guide_lookup", "That refined resource-guide lookup was already performed for the same problem focus and evidence. Do not repeat it; choose the next kubectl diagnostic or answer from the evidence.")
			l.currIteration++
			l.state = StateRunning
			return true
		}
		l.searchAndInjectResourceGuide(ctx, request.ResourceFamily, query)
		return true
	}
	return false
}

func (l *Loop) rejectAssistantManagedToolCalls(calls []gollm.FunctionCall) []gollm.FunctionCall {
	var allowed []gollm.FunctionCall
	for _, call := range calls {
		if isAssistantManagedToolName(call.Name) {
			result := map[string]any{
				"error": "guidance is handled by k8s-assistant outside the model tool loop. Continue with kubectl only.",
			}
			if l.cfg.EnableToolUseShim {
				l.currChatContent = append(l.currChatContent, fmt.Sprintf("Result of running %q:\n%v", call.Name, result))
			} else {
				l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
					ID:     call.ID,
					Name:   call.Name,
					Result: result,
				})
			}
			continue
		}
		allowed = append(allowed, call)
	}
	return allowed
}

func (l *Loop) analyzeToolCalls(ctx context.Context, calls []gollm.FunctionCall) ([]PendingCall, error) {
	pending := make([]PendingCall, len(calls))
	for i, call := range calls {
		parsed, err := l.registry.Tools.ParseToolInvocation(ctx, call.Name, call.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call 파싱 실패: %w", err)
		}
		isInteractive, interactiveErr := parsed.GetTool().IsInteractive(call.Arguments)
		pending[i] = PendingCall{
			FunctionCall:     call,
			ParsedToolCall:   parsed,
			IsInteractive:    isInteractive,
			InteractiveError: interactiveErr,
			ModifiesResource: l.modifiesResource(parsed, call),
		}
	}
	return pending, nil
}

func (l *Loop) modifiesResource(parsed *tools.ToolCall, call gollm.FunctionCall) string {
	if isObservationToolName(call.Name) {
		return "no"
	}
	if isNonMutatingKubectlInvocation(call) {
		return "no"
	}
	return parsed.GetTool().CheckModifiesResource(call.Arguments)
}

func isNonMutatingKubectlInvocation(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := splitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if !isReadOnlyKubectlPipeline(command) {
			return false
		}
	}
	return true
}

func readonlyShellScriptFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	raw, ok := call.Arguments["command"].(string)
	if !ok {
		return "", false
	}
	command := strings.TrimSpace(raw)
	if command == "" {
		return "", false
	}
	if script, ok := extractShellScript(command); ok {
		return script, true
	}

	switch strings.ToLower(call.Name) {
	case "kubectl":
		if strings.HasPrefix(command, "kubectl ") {
			return command, true
		}
		return "", false
	case "bash", "sh", "shell":
		if strings.HasPrefix(command, "kubectl ") {
			return command, true
		}
		return "", false
	default:
		return "", false
	}
}

func extractShellScript(command string) (string, bool) {
	fields := shellWords(command)
	if len(fields) < 3 || !isShellBinary(fields[0]) {
		return "", false
	}
	for i := 1; i < len(fields)-1; i++ {
		field := strings.TrimSpace(fields[i])
		if field == "-c" || (strings.HasPrefix(field, "-") && strings.Contains(field, "c")) {
			script := strings.TrimSpace(fields[i+1])
			return script, script != ""
		}
	}
	return "", false
}

func isShellBinary(value string) bool {
	value = strings.Trim(strings.ToLower(value), "'\"")
	return value == "bash" || value == "sh" || strings.HasSuffix(value, "/bash") || strings.HasSuffix(value, "/sh")
}

func isReadOnlyKubectlPipeline(command string) bool {
	segments := splitShellPipeline(command)
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if containsMutatingKubectlVerb(segment) {
			return false
		}
	}

	firstFields := strings.Fields(segments[0])
	verb, ok := kubectlVerbFromFields(firstFields, 0)
	if len(firstFields) < 2 || firstFields[0] != "kubectl" || !ok || !isKubectlReadOnlyVerb(verb) {
		return false
	}
	// Later pipeline segments are only allowed when they are local text
	// processors. This makes `kubectl get ... | tail -20` read-only while
	// keeping `kubectl get ... | kubectl apply -f -` blocked.
	for _, segment := range segments[1:] {
		fields := strings.Fields(segment)
		if len(fields) == 0 || !isSafeLocalPipelineCommand(fields[0]) {
			return false
		}
	}
	return true
}

func splitShellCommandList(command string) []string {
	return splitShellBy(command, func(s string, i int) (int, bool) {
		switch s[i] {
		case ';':
			return 1, true
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				return 2, true
			}
		}
		return 0, false
	})
}

func splitShellPipeline(command string) []string {
	return splitShellBy(command, func(s string, i int) (int, bool) {
		if s[i] == '|' && !(i+1 < len(s) && s[i+1] == '|') {
			return 1, true
		}
		return 0, false
	})
}

func splitShellBy(command string, isSeparator func(string, int) (int, bool)) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			current.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			current.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			current.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if width, ok := isSeparator(command, i); ok {
				if segment := strings.TrimSpace(current.String()); segment != "" {
					segments = append(segments, segment)
				}
				current.Reset()
				i += width - 1
				continue
			}
		}
		current.WriteByte(ch)
	}
	if segment := strings.TrimSpace(current.String()); segment != "" {
		segments = append(segments, segment)
	}
	return segments
}

func shellWords(command string) []string {
	var words []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, current.String())
		current.Reset()
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') {
			flush()
			continue
		}
		current.WriteByte(ch)
	}
	flush()
	return words
}

func containsMutatingKubectlVerb(segment string) bool {
	fields := strings.Fields(strings.ToLower(segment))
	for i := 0; i < len(fields); i++ {
		if fields[i] != "kubectl" {
			continue
		}
		if verb, ok := kubectlVerbFromFields(fields, i); ok && isKubectlMutatingVerb(verb) {
			return true
		}
	}
	return false
}

func kubectlVerbFromFields(fields []string, kubectlIndex int) (string, bool) {
	verb, _, ok := kubectlVerbAndIndexFromFields(fields, kubectlIndex)
	return verb, ok
}

func kubectlVerbAndIndexFromFields(fields []string, kubectlIndex int) (string, int, bool) {
	if kubectlIndex < 0 || kubectlIndex >= len(fields) || strings.Trim(fields[kubectlIndex], "'\"") != "kubectl" {
		return "", -1, false
	}
	for i := kubectlIndex + 1; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if field == "--" {
			if i+1 < len(fields) {
				return strings.ToLower(strings.Trim(fields[i+1], "'\"")), i + 1, true
			}
			return "", -1, false
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlGlobalFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortGlobalFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		return strings.ToLower(field), i, true
	}
	return "", -1, false
}

func kubectlGlobalFlagRequiresValue(flag string) bool {
	switch flag {
	case "--as", "--as-group", "--cache-dir", "--certificate-authority", "--client-certificate",
		"--client-key", "--cluster", "--context", "--kubeconfig", "--log-flush-frequency",
		"--namespace", "--profile", "--profile-output", "--request-timeout", "--server",
		"--tls-server-name", "--token", "--user", "--v", "--vmodule":
		return true
	default:
		return false
	}
}

func kubectlShortGlobalFlagRequiresValue(flag string) bool {
	switch flag {
	case "-n", "-s", "-v":
		return true
	default:
		return false
	}
}

func isKubectlReadOnlyVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "get", "describe", "logs", "top", "api-resources", "api-versions", "version", "config", "auth":
		return true
	default:
		return false
	}
}

func isKubectlMutatingVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "apply", "delete", "patch", "replace", "edit", "scale", "set", "create", "annotate", "label", "cordon", "uncordon", "drain", "taint":
		return true
	default:
		return false
	}
}

func isSafeLocalPipelineCommand(command string) bool {
	switch strings.ToLower(command) {
	case "tail", "head", "grep", "egrep", "fgrep", "awk", "sed", "sort", "uniq", "wc", "cut", "jq", "yq", "column":
		return true
	default:
		return false
	}
}

func isAssistantManagedToolName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "guidance_")
}

func isObservationToolName(name string) bool {
	name = strings.ToLower(name)
	return isAssistantManagedToolName(name) ||
		strings.HasPrefix(name, "log-analyzer_") ||
		strings.HasPrefix(name, "log_analyzer_")
}

func (l *Loop) hasModifyingCalls() bool {
	for _, call := range l.pendingCalls {
		if call.ModifiesResource != "no" {
			return true
		}
	}
	return false
}

func (l *Loop) rejectReadOnlyModifyingCalls() {
	var descriptions []string
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		descriptions = append(descriptions, call.ParsedToolCall.Description())
		l.appendToolObservation(call, map[string]any{
			"error":              "read-only mode is enabled; commands that modify Kubernetes resources are blocked.",
			"status":             "blocked",
			"retryable":          false,
			"modifies_resource":  call.ModifiesResource,
			"read_only_mode":     true,
			"allowed_operation":  "read-only diagnostics such as get, describe, logs, top, events",
			"blocked_operation":  call.ParsedToolCall.Description(),
			"suggested_response": "Return one final answer now. Explain that read-only mode blocks this change, state the observed cause and evidence once, and provide the manual recommendation once. Do not issue the same mutating action again.",
		})
	}
	if len(descriptions) == 0 {
		descriptions = append(descriptions, "리소스 변경 명령")
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "read-only 모드가 활성화되어 리소스 변경 명령을 차단했습니다:\n* "+strings.Join(descriptions, "\n* "))
	l.appendCorrection("readonly_mutation_blocked", "Read-only mode blocked the proposed mutation. Do not repeat the same action or restate the same recommendation in another loop. Your next response must be a single final answer with the cause, evidence, and one manual recommendation.")
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) requestApproval() {
	descriptions := make([]string, 0, len(l.pendingCalls))
	for _, call := range l.pendingCalls {
		descriptions = append(descriptions, call.ParsedToolCall.Description())
	}
	prompt := "다음 명령은 실행 전 승인이 필요합니다:\n* " + strings.Join(descriptions, "\n* ")
	prompt += "\n\n진행할까요?"
	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt: prompt,
		Options: []api.UserChoiceOption{
			{Value: "yes", Label: "예"},
			{Value: "yes_and_dont_ask_me_again", Label: "예, 이후 묻지 않기"},
			{Value: "no", Label: "아니오"},
		},
	})
}

func (l *Loop) handleApproval(ctx context.Context, choice int) error {
	switch choice {
	case 1:
		if err := l.dispatchToolCalls(ctx); err != nil {
			return err
		}
	case 2:
		l.skipPermissions = true
		if err := l.dispatchToolCalls(ctx); err != nil {
			return err
		}
	case 3:
		if len(l.pendingCalls) > 0 {
			l.appendToolObservation(l.pendingCalls[0], map[string]any{
				"error":     "User declined to run this operation.",
				"status":    "declined",
				"retryable": false,
			})
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "사용자가 작업 실행을 거부했습니다.")
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
	default:
		return fmt.Errorf("잘못된 승인 선택: %d", choice)
	}
	return nil
}

func (l *Loop) dispatchToolCalls(ctx context.Context) error {
	for _, call := range l.pendingCalls {
		description := call.ParsedToolCall.Description()
		l.addMessage(api.MessageSourceModel, api.MessageTypeToolCallRequest, description)

		output, err := call.ParsedToolCall.InvokeTool(ctx, tools.InvokeToolOptions{
			Kubeconfig: l.cfg.Kubeconfig,
			WorkDir:    l.workDir,
			Executor:   l.executor,
		})
		if err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, err.Error())
			return err
		}

		result, err := tools.ToolResultToMap(output)
		if err != nil {
			return err
		}
		l.appendToolObservation(call, result)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
	}

	l.pendingCalls = nil
	l.currIteration++
	if l.shouldCompactBeforeNextSend() {
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}
	l.state = StateRunning
	return nil
}

func (l *Loop) interceptCustomResourceFunctionCalls(ctx context.Context, calls []gollm.FunctionCall) bool {
	if l.resourceGuideInjected || l.initialGuideAttempted {
		return false
	}
	for _, call := range calls {
		command, ok := kubectlCommandFromFunctionCall(call)
		if !ok {
			continue
		}
		resource, ok := customResourceCandidateFromKubectl(command)
		if !ok {
			continue
		}
		classification := l.classifyResourceByDiscovery(ctx, resource)
		l.resourceClassification = &classification
		if classification.Kind != resourceClassificationCRD {
			continue
		}
		l.resourceGuideEvidence = append(l.resourceGuideEvidence, command)
		l.initialGuideAttempted = true
		l.searchAndInjectResourceGuide(ctx, resource, l.resourceGuideQuery(resource))
		return true
	}
	return false
}

func (l *Loop) searchAndInjectResourceGuide(ctx context.Context, resource, query string) {
	l.markResourceGuideQuery(query)
	client, err := guidance.NewResourceGuideClient(l.cfg)
	if err != nil {
		klog.Warningf("resource guidance client init failed: %v", err)
		l.injectResourceGuideUnavailable(resource, "client_init_failed")
		return
	}
	if client.KnowledgeProvider() != guidance.KnowledgeProviderQdrant {
		l.injectResourceGuideUnavailable(resource, "provider="+string(client.KnowledgeProvider()))
		return
	}
	searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	found, err := client.SearchGuides(searchCtx, query)
	cancel()
	if err != nil {
		klog.Warningf("resource guidance search failed: %v", err)
		l.injectResourceGuideUnavailable(resource, "search_failed")
		return
	}
	l.injectResourceGuideAttempt(resource, found)
}

func (l *Loop) resourceGuideQueryAlreadyUsed(query string) bool {
	if l.resourceGuideQueries == nil {
		return false
	}
	_, ok := l.resourceGuideQueries[query]
	return ok
}

func (l *Loop) markResourceGuideQuery(query string) {
	if l.resourceGuideQueries == nil {
		l.resourceGuideQueries = make(map[string]struct{})
	}
	l.resourceGuideQueries[query] = struct{}{}
}

func (l *Loop) injectResourceGuideAttempt(resource string, found *guidance.GuideSearchResult) {
	l.resourceGuideInjected = true
	includeClusterAPI := guideResultImpliesClusterAPI(found)
	l.promptOptions = l.newPromptOptions(l.requestIntent, true, includeClusterAPI)
	if l.shouldCompactForStateRewrite() {
		before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		limit := l.contextLimitTokens()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: injecting CRD guide context for %s; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", resource, before, limit))
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.currChatContent = []any{l.compactedStateMessage("Use the following guide context as decision support, then choose the next safest step.")}
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: CRD guide context injected for %s. estimated context %d/%d tokens.", resource, after, limit))
	} else {
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("ℹ resource guide injected for CRD %s without context compact.", resource))
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) injectResourceGuideUnavailable(resource, reason string) {
	l.resourceGuideInjected = true
	l.promptOptions = l.newPromptOptions(l.requestIntent, true, false)
	content := formatResourceGuideUnavailableObservation(resource, reason)
	if l.shouldCompactForStateRewrite() {
		before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		limit := l.contextLimitTokens()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: recording unavailable CRD guide context for %s; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", resource, before, limit))
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.currChatContent = []any{l.compactedStateMessage("Resource guide lookup was unavailable; continue with custom-resource-aware diagnostics.")}
		l.appendGuideObservation(guideRef{GuideID: "unavailable:" + resource + ":" + reason, Hash: contextHash(content)}, content)
		after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: unavailable CRD guide context recorded for %s. estimated context %d/%d tokens.", resource, after, limit))
	} else {
		l.appendGuideObservation(guideRef{GuideID: "unavailable:" + resource + ":" + reason, Hash: contextHash(content)}, content)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("ℹ resource guide unavailable for CRD %s (%s); continuing without context compact.", resource, reason))
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) resourceGuideQuery(resource string) string {
	lines := []string{l.originalQuery, "observed custom resource: " + resource}
	if l.resourceClassification != nil {
		lines = append(lines,
			"resource classification: "+l.resourceClassification.Kind,
			"classification source: "+l.resourceClassification.Source,
		)
		if l.resourceClassification.APIGroup != "" {
			lines = append(lines, "api group: "+l.resourceClassification.APIGroup)
		}
	}
	return strings.Join(append(lines, l.resourceGuideEvidence...), "\n")
}

func (l *Loop) resourceGuideRefinementQuery(request resourceGuideLookup) string {
	return strings.Join([]string{
		l.originalQuery,
		"resource family: " + request.ResourceFamily,
		"problem focus: " + request.ProblemFocus,
		"reason for refinement: " + request.Reason,
		"observed evidence: " + request.Evidence,
	}, "\n")
}

func resourceGuideLookupFromFunctionCall(call gollm.FunctionCall) (resourceGuideLookup, bool) {
	var request resourceGuideLookup
	resourceFamily, familyOK := call.Arguments["resource_family"].(string)
	problemFocus, focusOK := call.Arguments["problem_focus"].(string)
	reason, reasonOK := call.Arguments["reason"].(string)
	evidence, evidenceOK := call.Arguments["evidence"].(string)
	if !familyOK || !focusOK || !reasonOK || !evidenceOK {
		return request, false
	}
	request = resourceGuideLookup{
		ResourceFamily: strings.TrimSpace(resourceFamily),
		ProblemFocus:   strings.TrimSpace(problemFocus),
		Reason:         strings.TrimSpace(reason),
		Evidence:       strings.TrimSpace(evidence),
	}
	if request.ResourceFamily == "" || request.ProblemFocus == "" || request.Reason == "" || request.Evidence == "" {
		return resourceGuideLookup{}, false
	}
	return request, true
}

func kubectlCommandFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	return commandString(call.Arguments["command"])
}

func commandString(value any) (string, bool) {
	command, ok := value.(string)
	if !ok {
		return "", false
	}
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(strings.ToLower(command), "kubectl ") {
		return "", false
	}
	return command, true
}

func customResourceCandidateFromKubectl(command string) (string, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	if len(fields) < 3 || fields[0] != "kubectl" {
		return "", false
	}
	verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, 0)
	if !ok {
		return "", false
	}
	switch verb {
	case "get", "describe":
	default:
		return "", false
	}
	resource, ok := firstKubectlResourceArg(fields, verbIndex+1)
	if !ok {
		return "", false
	}
	resource = normalizeKubectlResource(resource)
	if resource == "" || isBuiltinKubernetesResource(resource) {
		return "", false
	}
	return resource, true
}

func firstKubectlResourceArg(fields []string, start int) (string, bool) {
	for i := start; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		resource := strings.Trim(field, ",")
		if strings.Contains(resource, ",") {
			resource = strings.Split(resource, ",")[0]
		}
		return resource, resource != ""
	}
	return "", false
}

func kubectlFlagRequiresValue(flag string) bool {
	return kubectlGlobalFlagRequiresValue(flag) || kubectlCommandFlagRequiresValue(flag)
}

func kubectlShortFlagRequiresValue(flag string) bool {
	return kubectlShortGlobalFlagRequiresValue(flag) || kubectlShortCommandFlagRequiresValue(flag)
}

func kubectlCommandFlagRequiresValue(flag string) bool {
	switch flag {
	case "--filename", "--field-selector", "--label-columns", "--output", "--output-watch-events",
		"--raw", "--selector", "--server-print", "--sort-by", "--template", "--watch-only":
		return true
	default:
		return false
	}
}

func kubectlShortCommandFlagRequiresValue(flag string) bool {
	switch flag {
	case "-f", "-l", "-o", "-R", "-w":
		return true
	default:
		return false
	}
}

var builtinKubernetesResources = map[string]string{
	"bindings": "binding", "componentstatuses": "componentstatus", "pods": "pod", "nodes": "node",
	"services": "service", "endpoints": "endpoint", "limitranges": "limitrange", "deployments": "deployment",
	"replicasets": "replicaset", "statefulsets": "statefulset", "daemonsets": "daemonset", "jobs": "job",
	"cronjobs": "cronjob", "configmaps": "configmap", "secrets": "secret", "namespaces": "namespace",
	"events": "event", "podtemplates": "podtemplate", "replicationcontrollers": "replicationcontroller",
	"resourcequotas": "resourcequota", "ingresses": "ingress", "persistentvolumes": "persistentvolume",
	"persistentvolumeclaims": "persistentvolumeclaim", "serviceaccounts": "serviceaccount", "roles": "role",
	"rolebindings": "rolebinding", "clusterroles": "clusterrole", "clusterrolebindings": "clusterrolebinding",
	"mutatingwebhookconfigurations": "mutatingwebhookconfiguration", "validatingwebhookconfigurations": "validatingwebhookconfiguration",
	"customresourcedefinitions": "customresourcedefinition", "apiservices": "apiservice",
	"controllerrevisions": "controllerrevision", "tokenreviews": "tokenreview",
	"localsubjectaccessreviews": "localsubjectaccessreview", "selfsubjectaccessreviews": "selfsubjectaccessreview",
	"selfsubjectrulesreviews": "selfsubjectrulesreview", "subjectaccessreviews": "subjectaccessreview",
	"horizontalpodautoscalers": "horizontalpodautoscaler", "certificatesigningrequests": "certificatesigningrequest",
	"leases": "lease", "flowschemas": "flowschema", "prioritylevelconfigurations": "prioritylevelconfiguration",
	"ingressclasses": "ingressclass", "networkpolicies": "networkpolicy", "runtimeclasses": "runtimeclass",
	"poddisruptionbudgets": "poddisruptionbudget", "podsecuritypolicies": "podsecuritypolicy",
	"priorityclasses": "priorityclass", "csidrivers": "csidriver", "csinodes": "csinode",
	"csistoragecapacities": "csistoragecapacity", "storageclasses": "storageclass",
	"endpointslices": "endpointslice", "volumeattachments": "volumeattachment",
	"cs": "componentstatus", "cm": "configmap", "ep": "endpoint", "ev": "event", "limits": "limitrange",
	"ns": "namespace", "no": "node", "pvc": "persistentvolumeclaim", "pv": "persistentvolume",
	"po": "pod", "rc": "replicationcontroller", "quota": "resourcequota", "sa": "serviceaccount",
	"svc": "service", "crd": "customresourcedefinition", "crds": "customresourcedefinition",
	"ds": "daemonset", "deploy": "deployment", "rs": "replicaset", "sts": "statefulset",
	"hpa": "horizontalpodautoscaler", "cj": "cronjob", "csr": "certificatesigningrequest",
	"ing": "ingress", "netpol": "networkpolicy", "pdb": "poddisruptionbudget", "psp": "podsecuritypolicy",
	"pc": "priorityclass", "sc": "storageclass",
}

func normalizeKubectlResource(resource string) string {
	if normalized, ok := builtinKubernetesResources[resource]; ok {
		return normalized
	}
	return resource
}

func isBuiltinKubernetesResource(resource string) bool {
	_, ok := builtinKubernetesResources[resource]
	if ok {
		return true
	}
	for _, normalized := range builtinKubernetesResources {
		if resource == normalized {
			return true
		}
	}
	return false
}

func formatResourceGuideObservation(resource string, found *guidance.GuideSearchResult) string {
	var b strings.Builder
	b.WriteString("Resource guide lookup was triggered after runtime discovery confirmed this resource as a CRD: ")
	b.WriteString(resource)
	if found == nil || len(found.Cases) == 0 {
		b.WriteString(".\nNo matching resource guide was found. Continue only with custom-resource-aware diagnostics; do not treat this resource as a built-in Kubernetes workload object.")
		return b.String()
	}
	b.WriteString(".\nUse this top guide before continuing diagnosis:\n")
	for i, c := range found.Cases {
		if i >= 1 {
			break
		}
		fmt.Fprintf(&b, "- %s", c.Title)
		if c.ID != "" {
			fmt.Fprintf(&b, " (guide_id: %s)", c.ID)
		}
		b.WriteString("\n")
		if len(c.EvidenceKeywords) > 0 {
			fmt.Fprintf(&b, "  evidence keywords: %s\n", strings.Join(c.EvidenceKeywords, ", "))
		}
		if len(c.DecisionHints) > 0 {
			fmt.Fprintf(&b, "  decision hints: %s\n", strings.Join(c.DecisionHints, " | "))
		}
		if len(c.RelatedObjects) > 0 {
			fmt.Fprintf(&b, "  related objects: %s\n", strings.Join(c.RelatedObjects, ", "))
		}
		if len(c.Tags) > 0 {
			fmt.Fprintf(&b, "  tags: %s\n", strings.Join(c.Tags, ", "))
		}
		if cues := guideMetadataCues(c); len(cues) > 0 {
			fmt.Fprintf(&b, "  metadata cues to inspect: %s\n", strings.Join(cues, ", "))
		}
		if len(c.DiagnosticSteps) > 0 {
			b.WriteString("  diagnostic steps; preserve label selectors, annotations, and command templates exactly when applicable:\n")
			for j, step := range c.DiagnosticSteps {
				if j >= 5 {
					break
				}
				fmt.Fprintf(&b, "    %d. %s", j+1, step.Description)
				if step.CommandTemplate != "" {
					fmt.Fprintf(&b, " -> %s", step.CommandTemplate)
				}
				if step.ExpectedOutcome != "" {
					fmt.Fprintf(&b, " ; expected: %s", step.ExpectedOutcome)
				}
				if len(step.Preconditions) > 0 {
					fmt.Fprintf(&b, " ; preconditions: %s", strings.Join(step.Preconditions, " | "))
				}
				b.WriteString("\n")
			}
		}
		if summary := firstNonEmptyGuideText(c.Resolution, c.Cause); summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", summary)
		}
	}
	if guideResultImpliesClusterAPI(found) {
		b.WriteString("Cluster API guardrails are enabled because the injected guide context indicates a Cluster API CRD family:\n")
		b.WriteString("- Treat an operator-managed `Cluster` custom resource as a management-cluster control-plane object that describes a workload cluster, not as the workload cluster itself.\n")
		b.WriteString("- A management-cluster `kubectl get node` result is not workload-cluster evidence; do not use it to conclude workload-cluster node registration, node health, or providerID presence.\n")
		b.WriteString("- Do not run `kubectl get node` for workload-cluster diagnosis until the current kubeconfig/context is confirmed to be that workload cluster's kubeconfig. If it is not confirmed, continue with guide-supported CRDs, labels, annotations, infrastructure/bootstrap resources, and providerID evidence instead.\n")
		b.WriteString("- Inspect workload-cluster nodes only after obtaining that workload cluster's kubeconfig by the project-approved method, unless the user explicitly asks about management-cluster nodes.\n")
	}
	b.WriteString("Choose the next action by comparing live evidence against the guide keywords and decision hints. If the current evidence does not match a guide, collect the missing evidence instead of assuming the guide is correct.")
	return b.String()
}

func guideMetadataCues(c guidance.GuideCase) []string {
	seen := map[string]struct{}{}
	var cues []string
	addCue := func(value string) {
		value = strings.Trim(value, " ,.;:()[]{}\"'`")
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		cues = append(cues, value)
	}
	fields := []string{
		c.Cause,
		c.Resolution,
		strings.Join(c.Symptoms, " "),
		strings.Join(c.EvidenceKeywords, " "),
		strings.Join(c.DecisionHints, " "),
		strings.Join(c.Tags, " "),
	}
	for _, step := range c.DiagnosticSteps {
		fields = append(fields, step.Description, step.CommandTemplate, step.ExpectedOutcome, strings.Join(step.Preconditions, " "))
	}
	for _, text := range fields {
		for _, token := range strings.FieldsFunc(text, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == ';'
		}) {
			lower := strings.ToLower(token)
			if strings.Contains(lower, "annotation") ||
				strings.Contains(lower, "label") ||
				strings.Contains(lower, "/") ||
				strings.Contains(lower, "=") {
				addCue(token)
			}
		}
	}
	if len(cues) > 12 {
		return cues[:12]
	}
	return cues
}

func guideResultImpliesClusterAPI(found *guidance.GuideSearchResult) bool {
	if found == nil || len(found.Cases) == 0 {
		return false
	}
	return guideCaseImpliesClusterAPI(found.Cases[0])
}

func guideRefFromResult(resource string, found *guidance.GuideSearchResult) guideRef {
	if found == nil || len(found.Cases) == 0 {
		id := "none:" + resource
		return guideRef{GuideID: id, Hash: contextHash(id)}
	}
	c := found.Cases[0]
	id := c.ID
	if id == "" {
		id = c.Title
	}
	content := strings.Join([]string{
		c.ID,
		c.Title,
		strings.Join(c.EvidenceKeywords, ","),
		strings.Join(c.DecisionHints, "|"),
		firstNonEmptyGuideText(c.Resolution, c.Cause),
	}, "\n")
	return guideRef{GuideID: id, Hash: contextHash(content)}
}

func guideCaseImpliesClusterAPI(c guidance.GuideCase) bool {
	var parts []string
	parts = append(parts, c.ID, c.Title, c.Cause, c.Resolution, c.Source)
	parts = append(parts, c.Symptoms...)
	parts = append(parts, c.EvidenceKeywords...)
	parts = append(parts, c.DecisionHints...)
	parts = append(parts, c.RelatedObjects...)
	parts = append(parts, c.Tags...)
	for _, step := range c.DiagnosticSteps {
		parts = append(parts, step.Description, step.CommandTemplate, step.RenderedCommand)
	}
	text := strings.ToLower(strings.Join(parts, "\n"))
	for _, marker := range []string{
		"cluster-api",
		"cluster api",
		"cluster.x-k8s.io",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func formatResourceGuideUnavailableObservation(resource, reason string) string {
	return fmt.Sprintf(
		"Resource guide lookup is required before diagnosing CRD resource %s, but lookup was not executed (%s). Do not claim that no guide matched. Continue only with custom-resource-aware diagnostics and state that resource guidance was unavailable.",
		resource,
		reason,
	)
}

func firstNonEmptyGuideText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (l *Loop) appendToolObservation(call PendingCall, result map[string]any) {
	l.recordAction(call, result)
	if l.cfg.EnableToolUseShim {
		l.currChatContent = append(l.currChatContent, fmt.Sprintf("Result of running %q:\n%v", call.FunctionCall.Name, result))
		return
	}
	l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
		ID:     call.FunctionCall.ID,
		Name:   call.FunctionCall.Name,
		Result: result,
	})
}

func (l *Loop) recordAction(call PendingCall, result map[string]any) {
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	target, ok := actionTargetFromFunctionCall(call.FunctionCall)
	l.actionSeq++
	record := actionRecord{
		Step:       l.actionSeq,
		Tool:       call.FunctionCall.Name,
		Command:    command,
		ResultHash: contextHash(fmt.Sprintf("%v", result)),
		Result:     compactObservationResult(result),
		Clues:      extractObservationClues(result),
	}
	if ok {
		record.Target = &target
	}
	l.completedActions = append(l.completedActions, record)
	if len(l.completedActions) > 12 {
		l.completedActions = l.completedActions[len(l.completedActions)-12:]
	}
}

func (l *Loop) translateModelText(ctx context.Context, text string) string {
	if l.lang == nil || !l.lang.enabled() || strings.TrimSpace(text) == "" {
		return text
	}
	if strings.EqualFold(strings.TrimSpace(l.cfg.Lang.Language), "English") {
		return text
	}
	translated, err := l.lang.translate(ctx, text)
	if err != nil {
		klog.Warningf("lang translation failed: %v", err)
		return "번역 모델 호출에 실패했습니다. /lang status의 model과 endpoint 설정을 확인하세요."
	}
	if strings.TrimSpace(translated) == "" {
		return "번역 모델이 빈 응답을 반환했습니다. /lang status의 model과 endpoint 설정을 확인하세요."
	}
	return translated
}

func (l *Loop) addMessage(source api.MessageSource, messageType api.MessageType, payload any) {
	l.output <- &api.Message{
		ID:        uuid.NewString(),
		Source:    source,
		Type:      messageType,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}
