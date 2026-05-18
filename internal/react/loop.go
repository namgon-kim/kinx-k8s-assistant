package react

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
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

	state           State
	currIteration   int
	currChatContent []any
	pendingCalls    []PendingCall
	skipPermissions bool

	originalQuery         string
	requestContext        *requestContext
	initialGuideAttempted bool
	resourceGuideInjected bool
	resourceGuideEvidence []string
	resourceGuideQueries  map[string]struct{}

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

	systemPrompt, err := buildSystemPrompt(l.cfg.PromptTemplateFile, registry.Tools, l.cfg.EnableToolUseShim, l.cfg.ReadOnly, l.cfg.Lang.Language, l.lang.enabled())
	if err != nil {
		return err
	}

	l.chat = gollm.NewRetryChat(
		l.llm.StartChat(systemPrompt, l.cfg.Model),
		gollm.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 10 * time.Second,
			MaxBackoff:     60 * time.Second,
			BackoffFactor:  2,
			Jitter:         true,
		},
	)

	if !l.cfg.EnableToolUseShim {
		defs := collectFunctionDefinitions(registry.Tools)
		if err := l.chat.SetFunctionDefinitions(defs); err != nil {
			return fmt.Errorf("tool function definition 주입 실패: %w", err)
		}
	}
	return nil
}

func collectFunctionDefinitions(registry tools.Tools) []*gollm.FunctionDefinition {
	defs := make([]*gollm.FunctionDefinition, 0)
	for _, tool := range registry.AllTools() {
		defs = append(defs, tool.FunctionDefinition())
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name < defs[j].Name
	})
	return defs
}

func (l *Loop) run(ctx context.Context, initialQuery string) {
	defer close(l.output)
	defer l.Close()

	if initialQuery != "" {
		l.startQuery(initialQuery)
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
		l.startQuery(query)
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

func (l *Loop) startQuery(query string) {
	l.addMessage(api.MessageSourceUser, api.MessageTypeText, query)
	l.currIteration = 0
	l.currChatContent = []any{query}
	l.pendingCalls = nil
	l.originalQuery = query
	l.requestContext = nil
	l.initialGuideAttempted = false
	l.resourceGuideInjected = false
	l.resourceGuideEvidence = nil
	l.resourceGuideQueries = nil
	l.state = StateRunning
}

func (l *Loop) handleMetaQuery(ctx context.Context, query string) bool {
	switch query {
	case "clear", "reset":
		if l.chat != nil {
			_ = l.chat.Initialize(nil)
		}
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

func (l *Loop) runIteration(ctx context.Context) error {
	if l.currIteration >= l.cfg.MaxIterations {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Maximum number of iterations reached.")
		l.currIteration = 0
		l.currChatContent = nil
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}

	stream, err := l.chat.SendStreaming(ctx, l.currChatContent...)
	if err != nil {
		return err
	}
	l.currChatContent = nil
	if l.cfg.EnableToolUseShim {
		stream, err = candidateToShimCandidate(stream)
		if err != nil {
			return err
		}
	}

	var streamedText string
	var functionCalls []gollm.FunctionCall
	for response, err := range stream {
		if err != nil {
			return err
		}
		if response == nil {
			break
		}
		if len(response.Candidates()) == 0 {
			return fmt.Errorf("LLM 응답 후보가 없습니다")
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

	if strings.TrimSpace(streamedText) != "" {
		streamedText = l.translateModelText(ctx, streamedText)
		l.addMessage(api.MessageSourceModel, api.MessageTypeText, streamedText)
	}

	if len(functionCalls) == 0 {
		l.currIteration = 0
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}

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
		l.requestApproval()
		l.state = StateWaitingApproval
		return nil
	}

	return l.dispatchToolCalls(ctx)
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
			l.currChatContent = append(l.currChatContent, "Request context was invalid. Return one corrected response with primary_target, scope, and resource_class before choosing the next action. Namespace is a scope field unless the user explicitly asks about a Namespace object; do not set primary_target.resource=namespace while also setting scope.namespace.")
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		l.requestContext = &request
		if l.shouldRunInitialResourceGuideLookup(request) {
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

func (l *Loop) shouldRunInitialResourceGuideLookup(request requestContext) bool {
	if l.initialGuideAttempted {
		return false
	}
	resource := normalizeKubectlResource(strings.ToLower(request.PrimaryTarget.Resource))
	if resource == "" || isBuiltinKubernetesResource(resource) {
		return false
	}
	return true
}

func (l *Loop) initialResourceGuideQuery(request requestContext) string {
	lines := []string{
		l.originalQuery,
		"primary target resource: " + request.PrimaryTarget.Resource,
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
		l.currChatContent = append(l.currChatContent, message)
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
	if target.Resource != "" && !commandMentionsToken(command, target.Resource) {
		return fmt.Sprintf("Action target declared resource %q, but command %q does not include that resource. Preserve the declared target and return one corrected next action.", target.Resource, command), true
	}
	if target.Name != "" && !commandMentionsToken(command, target.Name) {
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
	for _, field := range strings.Fields(command) {
		if strings.Trim(field, "'\"") == token {
			return true
		}
	}
	return false
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
			l.currChatContent = append(l.currChatContent, "Resource guide lookup request was invalid. Continue with the next safest kubectl diagnostic.")
			l.currIteration++
			l.state = StateRunning
			return true
		}
		query := l.resourceGuideRefinementQuery(request)
		if l.resourceGuideQueryAlreadyUsed(query) {
			l.currChatContent = append(l.currChatContent, "That refined resource-guide lookup was already performed for the same problem focus and evidence. Do not repeat it; choose the next kubectl diagnostic or answer from the evidence.")
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
	if strings.ToLower(call.Name) != "kubectl" {
		return false
	}
	raw, ok := call.Arguments["command"].(string)
	if !ok {
		return false
	}
	command := strings.TrimSpace(raw)
	if command == "" {
		return false
	}
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
	if len(firstFields) < 2 || firstFields[0] != "kubectl" || !isKubectlReadOnlyVerb(firstFields[1]) {
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

func splitShellPipeline(command string) []string {
	var segments []string
	for _, segment := range strings.Split(command, "|") {
		segment = strings.TrimSpace(segment)
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

func containsMutatingKubectlVerb(segment string) bool {
	fields := strings.Fields(strings.ToLower(segment))
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "kubectl" && isKubectlMutatingVerb(fields[i+1]) {
			return true
		}
	}
	return false
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
	l.currChatContent = append(l.currChatContent, "Read-only mode blocked the proposed mutation. Do not repeat the same action or restate the same recommendation in another loop. Your next response must be a single final answer with the cause, evidence, and one manual recommendation.")
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
	l.currChatContent = append(l.currChatContent, formatResourceGuideObservation(resource, found))
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) injectResourceGuideUnavailable(resource, reason string) {
	l.resourceGuideInjected = true
	l.currChatContent = append(l.currChatContent, formatResourceGuideUnavailableObservation(resource, reason))
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) resourceGuideQuery(resource string) string {
	return strings.Join(append([]string{l.originalQuery, "observed custom resource: " + resource}, l.resourceGuideEvidence...), "\n")
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

func customResourceCandidateFromPendingCall(call PendingCall) (string, bool) {
	command, ok := commandString(call.FunctionCall.Arguments["command"])
	if !ok {
		return "", false
	}
	return customResourceCandidateFromKubectl(command)
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
	switch fields[1] {
	case "get", "describe":
	default:
		return "", false
	}
	resource := normalizeKubectlResource(strings.Trim(fields[2], ","))
	if resource == "" || isBuiltinKubernetesResource(resource) {
		return "", false
	}
	return resource, true
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
	b.WriteString("Resource guide lookup was triggered because the ReAct loop accessed a custom-resource candidate: ")
	b.WriteString(resource)
	if found == nil || len(found.Cases) == 0 {
		b.WriteString(".\nNo matching resource guide was found. Continue only with custom-resource-aware diagnostics; do not treat this resource as a built-in Kubernetes workload object.")
		return b.String()
	}
	b.WriteString(".\nUse these guides before continuing diagnosis:\n")
	for i, c := range found.Cases {
		if i >= 3 {
			break
		}
		fmt.Fprintf(&b, "- %s\n", c.Title)
		if len(c.EvidenceKeywords) > 0 {
			fmt.Fprintf(&b, "  evidence keywords: %s\n", strings.Join(c.EvidenceKeywords, ", "))
		}
		if len(c.DecisionHints) > 0 {
			fmt.Fprintf(&b, "  decision hints: %s\n", strings.Join(c.DecisionHints, " | "))
		}
		if len(c.DiagnosticSteps) > 0 {
			var steps []string
			for j, step := range c.DiagnosticSteps {
				if j >= 3 {
					break
				}
				steps = append(steps, step.Description)
			}
			fmt.Fprintf(&b, "  next diagnostics: %s\n", strings.Join(steps, " | "))
		}
		if summary := firstNonEmptyGuideText(c.Resolution, c.Cause); summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", summary)
		}
	}
	b.WriteString("Choose the next action by comparing live evidence against the guide keywords and decision hints. If the current evidence does not match a guide, collect the missing evidence instead of assuming the guide is correct.")
	return b.String()
}

func formatResourceGuideUnavailableObservation(resource, reason string) string {
	return fmt.Sprintf(
		"Resource guide lookup is required before diagnosing custom-resource candidate %s, but lookup was not executed (%s). Do not claim that no guide matched. Continue only with custom-resource-aware diagnostics and state that resource guidance was unavailable.",
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
