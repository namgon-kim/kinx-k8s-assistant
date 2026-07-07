package react

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/google/uuid"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
	"k8s.io/klog/v2"
)

type State int

const (
	StateIdle State = iota
	StateRunning
	StateWaitingApproval
	StateWaitingDirectionChoice
	StateWaitingDirectionText
	StateDone
	StateExited
)

type InputOwner string

const (
	InputOwnerOrchestrator InputOwner = "orchestrator"
	InputOwnerReactChoice  InputOwner = "react_choice"
	InputOwnerReactText    InputOwner = "react_text"
	InputOwnerApproval     InputOwner = "approval"
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
const internalRequirementAnalysisCall = "__requirement_analysis__"
const internalPhasePlanCall = "__phase_plan__"
const internalPhaseProgressCall = "__phase_progress__"
const internalGuideProgressCall = "__guide_progress__"
const internalFinalReportCall = "__final_report__"
const internalNextDirectionsCall = "__next_directions__"
const internalMutationVerificationResultCall = "__mutation_verification_result__"
const internalInvalidActionCall = "__invalid_action__"
const internalInvalidStructuredOutputCall = "__invalid_structured_output__"

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

	systemPrompt            string
	promptOptions           promptOptions
	toolProfile             ToolProfile
	requestIntent           RequestIntent
	originalQuery           string
	requirementAnalysis     *requirementAnalysis
	requestContext          *requestContext
	phaseStepState          *phaseStepState
	resourceClassification  *resourceClassification
	lastOriginalQuery       string
	lastRequirementAnalysis *requirementAnalysis
	lastRequestContext      *requestContext
	lastDiagnosisSummary    string
	resourceDiscoveryCache  map[string]resourceClassification
	lastContextError        *contextError
	injectedGuides          map[string]guideRef
	completedActions        []actionRecord
	actionSeq               int
	lastCompactedActionSeq  int
	contextApproxTokens     int
	lastAssistantText       string
	lastProgressText        string
	resourceGuideInjected   bool
	resourceGuideEvidence   []string
	resourceGuideQueries    map[string]struct{}

	guideStepState               *guideStepState
	finalReportRequested         bool
	guidedPhaseProgressRequested bool
	pendingResponseDirective     string
	pendingFinalReport           *finalReport
	pendingNextDirections        *nextDirections
	pendingDirectionPrompt       *directionPromptState
	pendingMutationVerification  *pendingMutationVerification
	mutationContinuationRequired bool
	mutationContinuationAttempts int
	toolDispatchInProgress       bool

	cancel context.CancelFunc
	once   sync.Once

	inputOwner      atomic.Int32
	runtimeSnapshot atomic.Value
}

type guideStepState struct {
	GuideID      string
	Title        string
	TotalSteps   int
	StepFilePath string
	StepHash     string
	StepDetails  []guideStepDetail
	Completed    map[int]bool
	Skipped      map[int]bool
}

type guideStepDetail struct {
	Index           int      `json:"index"`
	Description     string   `json:"description,omitempty"`
	CommandTemplate string   `json:"command_template,omitempty"`
	RenderedCommand string   `json:"rendered_command,omitempty"`
	ExpectedOutcome string   `json:"expected_outcome,omitempty"`
	Preconditions   []string `json:"preconditions,omitempty"`
}

func (g *guideStepState) allCompleted() bool {
	if g == nil || g.TotalSteps == 0 {
		return false
	}
	for i := 1; i <= g.TotalSteps; i++ {
		if !g.Completed[i] && !g.Skipped[i] {
			return false
		}
	}
	return true
}

func (g *guideStepState) remainingSteps() []int {
	if g == nil {
		return nil
	}
	var remaining []int
	for i := 1; i <= g.TotalSteps; i++ {
		if !g.Completed[i] && !g.Skipped[i] {
			remaining = append(remaining, i)
		}
	}
	return remaining
}

func (g *guideStepState) skippedSteps() []int {
	if g == nil {
		return nil
	}
	var skipped []int
	for i := 1; i <= g.TotalSteps; i++ {
		if g.Skipped[i] {
			skipped = append(skipped, i)
		}
	}
	return skipped
}

func (g *guideStepState) stepRuntimeStates(phase PhaseRef) []StepRuntimeState {
	if g == nil {
		return nil
	}
	remaining := g.remainingSteps()
	steps := make([]StepRuntimeState, 0, len(g.StepDetails))
	for _, detail := range g.StepDetails {
		status := StepPending
		if g.Completed != nil && g.Completed[detail.Index] {
			status = StepCompleted
		} else if g.Skipped != nil && g.Skipped[detail.Index] {
			status = StepSkipped
		} else if detail.Index > 0 && len(remaining) > 0 && remaining[0] == detail.Index {
			status = StepActive
		}
		steps = append(steps, StepRuntimeState{
			Ref: StepRef{
				Phase: phase,
				Kind:  StepResourceGuideDiagnostic,
				Index: detail.Index,
			},
			Status:          status,
			Description:     strings.TrimSpace(detail.Description),
			Command:         strings.TrimSpace(detail.RenderedCommand),
			ExpectedOutcome: strings.TrimSpace(detail.ExpectedOutcome),
		})
	}
	return steps
}

// directionPromptState records the LLM-proposed next_directions options that
// were rendered to the user, so the user's choice index can be mapped back to
// a concrete continuation action.
type directionPromptState struct {
	Options      []nextDirectionOption
	HasFreeInput bool
	FreeInputIdx int
	FinalizeIdx  int
}

func New(cfg *config.Config) (*Loop, error) {
	setupProviderEnv(cfg)
	klog.V(0).InfoS("react loop creating", "provider", cfg.LLMProvider, "model", cfg.Model, "shim", cfg.EnableToolUseShim, "read_only", cfg.ReadOnly, "mcp", cfg.MCPClient)
	llmClient, err := gollm.NewClient(context.Background(), cfg.LLMProvider)
	if err != nil {
		klog.ErrorS(err, "LLM client creation failed", "provider", cfg.LLMProvider)
		return nil, fmt.Errorf("LLM 클라이언트 생성 실패 (%s): %w", cfg.LLMProvider, err)
	}
	klog.V(0).InfoS("react loop created", "provider", cfg.LLMProvider)
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
	klog.V(0).InfoS("react loop starting", "initial_query", strings.TrimSpace(initialQuery) != "", "query_len", len(strings.TrimSpace(initialQuery)))

	if err := l.init(loopCtx); err != nil {
		cancel()
		klog.ErrorS(err, "react loop init failed")
		return err
	}

	go l.run(loopCtx, strings.TrimSpace(initialQuery))
	return nil
}

func (l *Loop) Output() <-chan *api.Message {
	return l.output
}

func (l *Loop) SendInput(input any) {
	klog.V(2).InfoS("react input enqueue requested", "type", fmt.Sprintf("%T", input))
	select {
	case l.input <- input:
		klog.V(2).InfoS("react input enqueued", "type", fmt.Sprintf("%T", input))
	default:
		klog.Warningf("react loop input 채널이 가득 찼습니다. 입력 버림: %v", input)
	}
}

func (l *Loop) Close() {
	l.once.Do(func() {
		klog.V(0).InfoS("react loop closing", "state", logStateName(l.state), "work_dir", l.workDir)
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

func (l *Loop) InputOwner() InputOwner {
	if l == nil {
		return InputOwnerOrchestrator
	}
	if snapshot, ok := l.PublishedRuntimeSnapshot(); ok {
		return snapshot.InputOwner
	}
	switch l.inputOwner.Load() {
	case 1:
		return InputOwnerReactChoice
	case 2:
		return InputOwnerReactText
	case 3:
		return InputOwnerApproval
	default:
		return InputOwnerOrchestrator
	}
}

func (l *Loop) refreshInputOwner() {
	if l == nil {
		return
	}
	snapshot := l.publishRuntimeSnapshot()
	owner := snapshot.InputOwner
	var value int32
	switch owner {
	case InputOwnerReactChoice:
		value = 1
	case InputOwnerReactText:
		value = 2
	case InputOwnerApproval:
		value = 3
	default:
		value = 0
	}
	l.inputOwner.Store(value)
}

func (l *Loop) init(ctx context.Context) error {
	workDir, err := os.MkdirTemp("", "k8s-assistant-*")
	if err != nil {
		return fmt.Errorf("작업 디렉터리 생성 실패: %w", err)
	}
	l.workDir = workDir
	l.executor = sandbox.NewLocalExecutor()
	klog.V(1).InfoS("react work directory created", "path", workDir)

	registry, err := toolconnector.NewRegistry(ctx, l.executor, l.cfg.MCPClient)
	if err != nil {
		return fmt.Errorf("tool registry 초기화 실패: %w", err)
	}
	l.registry = registry
	klog.V(0).InfoS("react registry initialized", "tools", len(registry.Tools.Names()))

	l.lang = newLangTranslator(l.cfg)
	klog.V(0).InfoS("language translator configured", "language", l.cfg.Lang.Language, "enabled", l.lang != nil && l.lang.enabled())

	l.requestIntent = RequestIntentGeneral
	l.toolProfile = selectToolProfile(registry.Tools, l.requestIntent, "")
	l.promptOptions = l.newPromptOptions(l.requestIntent, false, false)

	return l.resetChatSession()
}

func (l *Loop) resetChatSession() error {
	systemPrompt, err := buildSystemPromptWithOptions(l.cfg.PromptTemplateFile, l.registry.Tools, l.promptOptions)
	if err != nil {
		klog.ErrorS(err, "system prompt build failed", "template", l.cfg.PromptTemplateFile)
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
		defs := collectFunctionDefinitionsForProfile(l.registry.Tools, l.toolProfile, true)
		klog.V(1).InfoS("setting function definitions", "count", len(defs), "tool_profile", l.toolProfile.Name)
		if err := l.chat.SetFunctionDefinitions(defs); err != nil {
			klog.ErrorS(err, "function definition injection failed")
			return fmt.Errorf("tool function definition 주입 실패: %w", err)
		}
	}
	klog.V(1).InfoS("chat session reset", "prompt_tokens_estimate", l.contextApproxTokens, "tool_profile", l.toolProfile.Name, "tools", len(l.toolProfile.ToolNames), "shim", l.cfg.EnableToolUseShim)
	klog.V(2).InfoS("chat session tool profile", "tool_profile", l.toolProfile.Name, "tools", l.toolProfile.ToolNames)
	return nil
}

func (l *Loop) run(ctx context.Context, initialQuery string) {
	defer close(l.output)
	defer l.Close()
	defer klog.V(0).InfoS("react run loop stopped")
	klog.V(0).InfoS("react run loop entered", "initial_query", initialQuery != "")

	if initialQuery != "" {
		if err := l.startQuery(initialQuery); err != nil {
			l.state = StateDone
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
	}

	for {
		select {
		case <-ctx.Done():
			l.state = StateExited
			return
		default:
		}

		if handled := l.auditRuntimeState(); handled {
			continue
		}

		switch l.state {
		case StateIdle, StateDone:
			l.refreshInputOwner()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, ">>>")
			if !l.waitForInput(ctx) {
				return
			}
		case StateWaitingApproval:
			l.refreshInputOwner()
			if !l.waitForApproval(ctx) {
				return
			}
		case StateWaitingDirectionChoice:
			l.refreshInputOwner()
			if !l.waitForDirectionChoice(ctx) {
				return
			}
		case StateWaitingDirectionText:
			l.refreshInputOwner()
			if !l.waitForDirectionText(ctx) {
				return
			}
		case StateRunning:
			l.refreshInputOwner()
			if err := l.runIteration(ctx); err != nil {
				l.pendingCalls = nil
				l.currChatContent = nil
				l.currIteration = 0
				l.state = StateDone
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
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
			klog.V(0).InfoS("react waitForInput received EOF")
			l.applyRuntimeCleanup(cleanupExitPolicy())
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
			klog.V(1).InfoS("react waitForInput received empty query")
			l.state = StateDone
			return true
		}
		klog.V(0).InfoS("react waitForInput received query", "query_len", len(query))
		if handled := l.handleMetaQuery(ctx, query); handled {
			return true
		}
		if err := l.startQuery(query); err != nil {
			l.state = StateDone
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
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
			klog.V(0).InfoS("react waitForApproval received EOF")
			l.applyRuntimeCleanup(cleanupExitPolicy())
			l.state = StateExited
			return false
		}
		choice, ok := raw.(*api.UserChoiceResponse)
		if !ok {
			return true
		}
		klog.V(0).InfoS("react approval choice received", "choice", choice.Choice)
		if err := l.handleApproval(ctx, choice.Choice); err != nil {
			l.state = StateDone
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
		return true
	}
}

func (l *Loop) startQuery(query string) error {
	intent := classifyRequestIntent(query)
	klog.V(0).InfoS("query starting", "query_len", len(query), "intent", intent)
	l.requestIntent = intent
	l.captureConversationMemory()
	priorState := l.priorConversationStateMessage()
	l.toolProfile = selectToolProfile(l.registry.Tools, intent, query)
	l.promptOptions = l.newPromptOptions(intent, false, false)
	klog.V(1).InfoS("query prompt options selected", "intent", intent, "tool_profile", l.toolProfile.Name, "tools", len(l.toolProfile.ToolNames), "read_only", l.cfg.ReadOnly, "translate_output", l.promptOptions.TranslateOutput)
	if err := l.resetChatSession(); err != nil {
		return err
	}
	l.addMessage(api.MessageSourceUser, api.MessageTypeText, query)
	l.currIteration = 0
	l.currChatContent = nil
	if priorState != "" {
		l.currChatContent = append(l.currChatContent, priorState)
	}
	l.currChatContent = append(l.currChatContent, requirementAnalysisPrompt())
	l.currChatContent = append(l.currChatContent, requirementAnalysisDefinitionPrompt())
	l.currChatContent = append(l.currChatContent, query)
	l.contextBlockHashes = nil
	l.pendingCalls = nil
	l.originalQuery = query
	l.requirementAnalysis = nil
	l.requestContext = nil
	l.phaseStepState = nil
	l.resourceClassification = nil
	l.lastContextError = nil
	l.injectedGuides = nil
	l.completedActions = nil
	l.actionSeq = 0
	l.lastCompactedActionSeq = 0
	l.lastAssistantText = ""
	l.lastProgressText = ""
	l.resourceGuideInjected = false
	l.resourceGuideEvidence = nil
	l.resourceGuideQueries = nil
	l.guideStepState = nil
	l.finalReportRequested = false
	l.guidedPhaseProgressRequested = false
	l.pendingResponseDirective = ""
	l.pendingFinalReport = nil
	l.pendingNextDirections = nil
	l.pendingDirectionPrompt = nil
	l.pendingMutationVerification = nil
	l.mutationContinuationRequired = false
	l.mutationContinuationAttempts = 0
	l.state = StateRunning
	klog.V(0).InfoS("query state initialized", "intent", intent, "state", logStateName(l.state))
	return nil
}

func (l *Loop) captureConversationMemory() {
	if !l.hasConversationState() {
		return
	}
	l.lastOriginalQuery = l.originalQuery
	l.lastRequirementAnalysis = cloneRequirementAnalysis(l.requirementAnalysis)
	l.lastRequestContext = cloneRequestContext(l.requestContext)
	l.lastDiagnosisSummary = l.compactDiagnosisSummary()
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
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.requestIntent = RequestIntentGeneral
		l.toolProfile = selectToolProfile(l.registry.Tools, l.requestIntent, "")
		l.promptOptions = l.newPromptOptions(l.requestIntent, false, false)
		if err := l.resetChatSession(); err != nil {
			l.state = StateDone
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			return true
		}
		l.clearConversationState()
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "대화 컨텍스트를 초기화했습니다.")
		return true
	case "exit", "quit":
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.applyRuntimeCleanup(cleanupExitPolicy())
		l.state = StateExited
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
		return true
	case "model":
		klog.V(1).InfoS("react meta query handled", "query", query, "model", l.cfg.Model)
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Current model is `"+l.cfg.Model+"`")
		return true
	case "models":
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.state = StateDone
		models, err := l.llm.ListModels(ctx)
		if err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, err.Error())
		} else {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Available models:\n\n  - "+strings.Join(models, "\n  - "))
		}
		return true
	case "tools":
		klog.V(0).InfoS("react meta query handled", "query", query, "tools", len(l.registry.Tools.Names()))
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Available tools:\n\n  - "+strings.Join(l.registry.Tools.Names(), "\n  - "))
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
	l.requirementAnalysis = nil
	l.requestContext = nil
	l.phaseStepState = nil
	l.resourceClassification = nil
	l.lastOriginalQuery = ""
	l.lastRequirementAnalysis = nil
	l.lastRequestContext = nil
	l.lastDiagnosisSummary = ""
	l.lastContextError = nil
	l.injectedGuides = nil
	l.completedActions = nil
	l.actionSeq = 0
	l.lastCompactedActionSeq = 0
	l.contextApproxTokens = estimateContextTokens(l.systemPrompt)
	l.lastAssistantText = ""
	l.lastProgressText = ""
	l.resourceGuideInjected = false
	l.resourceGuideEvidence = nil
	l.resourceGuideQueries = nil
	l.guideStepState = nil
	l.finalReportRequested = false
	l.guidedPhaseProgressRequested = false
	l.pendingResponseDirective = ""
	l.pendingFinalReport = nil
	l.pendingNextDirections = nil
	l.pendingDirectionPrompt = nil
	l.pendingMutationVerification = nil
	l.mutationContinuationRequired = false
	l.mutationContinuationAttempts = 0
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
	model := ""
	if l.cfg != nil {
		model = strings.ToLower(strings.TrimSpace(l.cfg.Model))
	}
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
	snapshot := l.RuntimeSnapshot()
	klog.V(1).InfoS("react iteration starting",
		"iteration", l.currIteration+1,
		"max_iterations", l.cfg.MaxIterations,
		"state", logStateName(l.state),
		"control", snapshot.Control,
		"context_tokens_estimate", l.contextApproxTokens,
		"chat_content_items", len(l.currChatContent),
	)
	if l.currIteration >= l.cfg.MaxIterations {
		klog.V(0).InfoS("react max iterations reached", "max_iterations", l.cfg.MaxIterations)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Maximum number of iterations reached.")
		l.currIteration = 0
		l.currChatContent = nil
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}

	if l.shouldCompactBeforeNextSend() {
		klog.V(1).InfoS("context compaction triggered before send", "context_tokens_estimate", l.contextApproxTokens, "limit", l.contextLimitTokens())
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}

	sentContent := l.buildIterationSendContent()
	klog.V(2).InfoS("sending model request", "content_items", len(sentContent), "content_tokens_estimate", estimateContextTokens(sentContent...))
	sendStart := time.Now()
	streamedText, functionCalls, err := l.sendAndCollectStreaming(ctx, sentContent)
	if err != nil {
		if !isContextLengthError(err) {
			klog.ErrorS(err, "model streaming request failed", "duration", time.Since(sendStart))
			return err
		}
		klog.V(0).InfoS("model streaming hit context length", "error", err.Error(), "duration", time.Since(sendStart))
		if ok := l.compactAfterContextLengthError(err); !ok {
			return err
		}
		sentContent = l.buildIterationSendContent()
		klog.V(1).InfoS("retrying model request after context compaction", "content_items", len(sentContent), "content_tokens_estimate", estimateContextTokens(sentContent...))
		sendStart = time.Now()
		streamedText, functionCalls, err = l.sendAndCollectStreaming(ctx, sentContent)
		if err != nil {
			klog.ErrorS(err, "model streaming retry failed", "duration", time.Since(sendStart))
			return err
		}
	}
	klog.V(0).InfoS("model response received", "duration", time.Since(sendStart), "text_len", len(streamedText), "function_calls", len(functionCalls), "call_names", logFunctionCallNames(functionCalls))
	klog.V(2).InfoS("model response call summaries", "calls", logFunctionCallSummaries(functionCalls))
	l.noteContextContent(sentContent...)
	l.currChatContent = nil
	l.pendingResponseDirective = ""

	if len(functionCalls) == 0 {
		if strings.TrimSpace(streamedText) != "" {
			if parsed, err := parseReActResponse(streamedText); err == nil {
				functionCalls = functionCallsFromParsedReActResponse(parsed)
			}
		}
		if len(functionCalls) == 0 && l.requirementAnalysis == nil {
			if handled := l.requireRequirementAnalysisBeforeAction(nil); handled {
				return nil
			}
		}
		if len(functionCalls) == 0 && l.requirementAnalysis != nil && l.phaseStepState == nil {
			if handled := l.requirePhasePlanBeforeAction(nil); handled {
				return nil
			}
		}
	}

	if len(functionCalls) == 0 {
		klog.V(1).InfoS("model response had no function calls", "text_len", len(streamedText))
		if handled := l.rejectMissingMutationVerificationOnNoCalls(); handled {
			return nil
		}
		if handled := l.rejectMutationContinuationOnNoCalls(); handled {
			return nil
		}
		if handled := l.rejectPlainAnswerDuringNextDirections(streamedText); handled {
			return nil
		}
		if l.phaseStepState != nil && strings.TrimSpace(streamedText) != "" && !l.phaseAllowsPlainAnswer() {
			if handled := l.rejectPlainAnswerOutsideResponsePhase(); handled {
				return nil
			}
		}
		if strings.TrimSpace(streamedText) != "" {
			rawModelText := streamedText
			l.contextApproxTokens += estimateContextTokens(rawModelText)
			displayText := l.translateModelText(ctx, rawModelText)
			l.addMessage(api.MessageSourceModel, api.MessageTypeText, displayText)
			l.lastAssistantText = rawModelText
			klog.V(0).InfoS("plain model answer emitted", "raw_len", len(rawModelText), "display_len", len(displayText))
		}
		l.currIteration = 0
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}
	deferredProgressText := strings.TrimSpace(streamedText)
	functionCalls = normalizeAssistantStructuredFunctionCalls(functionCalls)
	klog.V(1).InfoS("normalized model function calls", "function_calls", len(functionCalls), "call_names", logFunctionCallNames(functionCalls))

	if handled := l.rejectInvalidShimStructuredCalls(functionCalls); handled {
		return nil
	}

	if handled := l.requireRequirementAnalysisBeforeAction(functionCalls); handled {
		return nil
	}

	var requestContextHandled bool
	functionCalls, requestContextHandled = l.consumeRequestContext(ctx, functionCalls)
	if requestContextHandled {
		return nil
	}

	if handled := l.requirePhasePlanBeforeAction(functionCalls); handled {
		return nil
	}

	var phasePlanHandled bool
	functionCalls, phasePlanHandled = l.consumePhasePlan(functionCalls)
	if phasePlanHandled {
		return nil
	}

	if handled := l.enforcePendingMutationVerification(functionCalls); handled {
		return nil
	}

	if handled := l.enforceMutationContinuation(functionCalls); handled {
		return nil
	}

	var mutationVerificationResultHandled bool
	functionCalls, mutationVerificationResultHandled = l.consumeMutationVerificationResult(functionCalls)
	if mutationVerificationResultHandled {
		return nil
	}

	var guideProgressHandled bool
	functionCalls, guideProgressHandled = l.consumeGuideProgress(functionCalls)
	if guideProgressHandled {
		return nil
	}

	var phaseProgressHandled bool
	functionCalls, phaseProgressHandled = l.consumePhaseProgress(functionCalls)
	if phaseProgressHandled {
		return nil
	}

	if handled := l.handleRequestedResourceGuideLookup(ctx, functionCalls); handled {
		return nil
	}

	if handled := l.rejectActionDuringGuidanceLookupWithoutGuide(functionCalls); handled {
		return nil
	}

	if handled := l.rejectConversationalToolCalls(functionCalls); handled {
		return nil
	}

	if handled := l.enforceRequestedStructuredDirective(functionCalls); handled {
		return nil
	}

	var finalReportHandled bool
	functionCalls, finalReportHandled = l.consumeFinalReport(ctx, functionCalls)
	if finalReportHandled {
		return nil
	}

	var nextDirectionsHandled bool
	functionCalls, nextDirectionsHandled = l.consumeNextDirections(functionCalls)
	if nextDirectionsHandled {
		return nil
	}

	if handled := l.rejectInconsistentActionTargets(functionCalls); handled {
		return nil
	}

	if handled := l.rejectInvalidKubectlResources(functionCalls); handled {
		return nil
	}

	if handled := l.rejectUnrelatedFirstDiagnostic(functionCalls); handled {
		return nil
	}

	functionCalls = l.rejectAssistantManagedToolCalls(functionCalls)
	if len(functionCalls) == 0 {
		klog.V(1).InfoS("all function calls were handled internally or rejected")
		l.currIteration++
		return nil
	}

	if handled := l.rejectNonObservationShellToolCalls(functionCalls); handled {
		return nil
	}

	pending, err := l.analyzeToolCalls(ctx, functionCalls)
	if err != nil {
		klog.ErrorS(err, "tool call analysis failed", "call_names", logFunctionCallNames(functionCalls))
		return err
	}
	l.pendingCalls = pending
	klog.V(0).InfoS("tool calls analyzed", "pending", len(pending), "summaries", logPendingCallSummaries(pending))

	if handled := l.rejectInteractiveToolCalls(); handled {
		klog.V(0).InfoS("interactive tool calls rejected")
		return nil
	}

	if l.cfg.ReadOnly && l.hasModifyingCalls() {
		klog.V(0).InfoS("read-only mode blocking modifying tool calls", "pending", logPendingCallSummaries(l.pendingCalls))
		l.rejectReadOnlyModifyingCalls()
		return nil
	}
	if !l.skipPermissions && l.hasModifyingCalls() {
		klog.V(0).InfoS("approval required for modifying tool calls", "pending", logPendingCallSummaries(l.pendingCalls))
		l.emitAcceptedProgressText(ctx, deferredProgressText)
		l.requestApproval()
		return nil
	}

	l.emitAcceptedProgressText(ctx, deferredProgressText)
	klog.V(1).InfoS("dispatching tool calls without approval", "pending", len(l.pendingCalls))
	return l.dispatchToolCalls(ctx)
}

func (l *Loop) rejectPlainAnswerDuringNextDirections(text string) bool {
	if l.RuntimeSnapshot().Control != ControlAwaitingNextDirections || strings.TrimSpace(text) == "" {
		return false
	}
	message := "The previous final_report was inconclusive and the runtime requested `next_directions`. Return only a next_directions object with concrete continuation options; do not emit a plain answer yet."
	l.queueResponseDirective(message)
	return l.applyModelOutputCorrectionGate("next_directions_required_plain_answer", "next_directions 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
}

func (l *Loop) auditRuntimeState() bool {
	snapshot := l.RuntimeSnapshot()
	if message := snapshot.AuditError(); message != "" {
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		l.refreshInputOwner()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime state invariant violation: "+message)
		return true
	}
	return false
}

// buildIterationSendContent assembles the message list that will be sent to
// the LLM for the current iteration. It prepends compact anchors so the model
// keeps the active request, phase, nested guide/mutation state, and required
// next output in active attention across many iterations of tool observations.
func (l *Loop) buildIterationSendContent() []any {
	sentContent := append([]any(nil), l.currChatContent...)
	if anchor := l.mutationVerificationAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.guideStepAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.phaseStepAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.requirementAnalysisAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.runtimeStateAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	return sentContent
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

func (l *Loop) enforceRequestedStructuredDirective(calls []gollm.FunctionCall) bool {
	if len(calls) == 0 {
		return false
	}
	snapshot := l.RuntimeSnapshot()
	switch {
	case snapshot.Control == ControlAwaitingNextDirections && !onlyFunctionCall(calls, internalNextDirectionsCall):
		message := "The previous final_report was inconclusive and the runtime requested `next_directions`. Return only a next_directions object with concrete continuation options; do not emit action, final_report, phase_progress, or a plain answer yet."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("next_directions_required", "next_directions 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	case snapshot.Control == ControlAwaitingGuidedPhaseProgress && !onlyFunctionCall(calls, internalPhaseProgressCall):
		message := "The runtime already requested `phase_progress` for the completed guided_diagnosis phase. Do not emit another action. Return only a phase_progress object completing guided_diagnosis."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("guided_phase_progress_required", "guided_diagnosis phase_progress 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	case snapshot.Control == ControlAwaitingFinalReport && !onlyFunctionCall(calls, internalFinalReportCall):
		message := "The runtime already requested `final_report` after completing the resource-guide diagnostic steps. Do not emit another action. Return only a final_report object."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("final_report_required", "final_report 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	}
	return false
}

func onlyFunctionCall(calls []gollm.FunctionCall, name string) bool {
	return len(calls) == 1 && calls[0].Name == name
}

func (l *Loop) rejectConversationalToolCalls(calls []gollm.FunctionCall) bool {
	if len(calls) == 0 || l.requirementAnalysis == nil {
		return false
	}
	if !l.requirementAnalysisNeedsDirectConversation() {
		return false
	}
	for _, call := range calls {
		if isRuntimeInternalCall(call.Name) {
			continue
		}
		message := "The accepted requirement_analysis is a conversation/clarification request. Do not call shell, bash, kubectl, echo, or any other tool to ask the user a question. Return a plain assistant answer/question directly, or complete the clarification phase with phase_progress when the user's intent is already clear."
		return l.applyModelOutputCorrectionGate("conversation_tool_call", "대화/확인 요청에서 tool call이 반복되어 진단을 중단합니다.", message)
	}
	return false
}

func (l *Loop) rejectNonObservationShellToolCalls(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		command, ok := selfTalkShellCommand(call)
		if !ok {
			continue
		}
		message := fmt.Sprintf("The previous action was rejected before execution because it does not observe cluster state. Command %q only prints or waits locally, so it is assistant self-talk, not a diagnostic command. Retry with one of these valid next outputs: emit phase_progress if the current planning phase is complete, or emit one real read-only kubectl action if more evidence is needed. Do not return final_report yet unless the active phase allows it and enough evidence is already available.", command)
		return l.applyGateOutcome(GateOutcome{
			Kind:            GateOutcomeAgentCommandRetry,
			Code:            "non_observation_shell_action",
			Retryable:       true,
			RetryScope:      RetryScopeAgentCommand,
			UserVisible:     true,
			UserMessage:     "관찰이 없는 shell action을 실행하지 않고 다음 응답을 재요청합니다:\n* " + command,
			ModelCorrection: message,
			CorrectionMode:  CorrectionModeAppendCompacted,
			BranchPolicy:    BranchRetryStep,
		})
	}
	return false
}

func (l *Loop) rejectInteractiveToolCalls() bool {
	var descriptions []string
	for _, call := range l.pendingCalls {
		if !call.IsInteractive {
			continue
		}
		errText := "interactive command cannot run in this non-interactive session"
		if call.InteractiveError != nil {
			errText = call.InteractiveError.Error()
		}
		descriptions = append(descriptions, call.FunctionCall.Name+": "+errText)
		l.appendToolObservation(call, map[string]any{
			"error":              errText,
			"status":             "blocked",
			"policy":             "interactive_command_blocked",
			"retryable":          true,
			"retry_scope":        "agent_correct_command",
			"suggested_response": "Retry with a non-interactive command that observes the same evidence or performs the same safe operation without prompting for stdin/TTY input.",
		})
	}
	if len(descriptions) == 0 {
		return false
	}
	message := "The previous tool call requires interactive input and cannot run in this non-interactive ReAct loop. Retry with one non-interactive command that observes the same evidence or performs the same safe operation without stdin/TTY prompts. Do not ask the user to run the blocked command unless no safe non-interactive alternative exists."
	return l.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomePolicyBlock,
		Code:            "interactive_command_blocked",
		Retryable:       true,
		RetryScope:      RetryScopeAgentCommand,
		UserVisible:     true,
		UserMessage:     "interactive command를 실행하지 않고 non-interactive 대안을 재요청합니다:\n* " + strings.Join(descriptions, "\n* "),
		ModelCorrection: message,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRetryStep,
	})
}

func selfTalkShellCommand(call gollm.FunctionCall) (string, bool) {
	command, ok := commandString(call.Arguments["command"])
	if !ok {
		return "", false
	}
	script := strings.TrimSpace(command)
	if extracted, ok := extractShellScript(script); ok {
		script = extracted
	}
	commands := splitShellCommandList(script)
	if len(commands) == 0 {
		commands = []string{script}
	}
	for _, candidate := range commands {
		fields := shellWords(candidate)
		if len(fields) == 0 {
			continue
		}
		if !isSelfTalkShellProgram(fields[0]) {
			return "", false
		}
	}
	return command, true
}

func isSelfTalkShellProgram(program string) bool {
	switch strings.Trim(strings.ToLower(program), "'\"") {
	case "echo", "printf", "true", "false", "sleep", "read":
		return true
	default:
		return false
	}
}

func (l *Loop) requirementAnalysisNeedsDirectConversation() bool {
	analysis := l.requirementAnalysis
	if analysis == nil {
		return false
	}
	category := strings.ToLower(strings.TrimSpace(analysis.Target.Category))
	action := strings.ToLower(strings.TrimSpace(analysis.Action))
	requestType := strings.ToLower(strings.TrimSpace(analysis.RequestType))
	return category == "conversation" ||
		requestType == "explanation" && strings.Contains(action, "clarify") ||
		strings.Contains(action, "clarify_request")
}

func isRuntimeInternalCall(name string) bool {
	return internalStructuredCallName(name) != ""
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

func normalizeAssistantStructuredFunctionCalls(calls []gollm.FunctionCall) []gollm.FunctionCall {
	if len(calls) == 0 {
		return calls
	}
	normalized := make([]gollm.FunctionCall, 0, len(calls))
	for _, call := range calls {
		if internalName := internalStructuredCallName(call.Name); internalName != "" {
			call.Name = internalName
		}
		normalized = append(normalized, call)
	}
	return normalized
}

func internalStructuredCallName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case bareInternalCallName(internalRequirementAnalysisCall), internalRequirementAnalysisCall:
		return internalRequirementAnalysisCall
	case bareInternalCallName(internalRequestContextCall), internalRequestContextCall:
		return internalRequestContextCall
	case bareInternalCallName(internalPhasePlanCall), internalPhasePlanCall:
		return internalPhasePlanCall
	case bareInternalCallName(internalPhaseProgressCall), internalPhaseProgressCall:
		return internalPhaseProgressCall
	case bareInternalCallName(internalGuideProgressCall), internalGuideProgressCall:
		return internalGuideProgressCall
	case bareInternalCallName(internalResourceGuideLookupCall), internalResourceGuideLookupCall:
		return internalResourceGuideLookupCall
	case bareInternalCallName(internalFinalReportCall), internalFinalReportCall:
		return internalFinalReportCall
	case bareInternalCallName(internalNextDirectionsCall), internalNextDirectionsCall:
		return internalNextDirectionsCall
	case bareInternalCallName(internalMutationVerificationResultCall), internalMutationVerificationResultCall:
		return internalMutationVerificationResultCall
	default:
		return ""
	}
}

func bareInternalCallName(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(name), "__"), "__")
}

func (l *Loop) analyzeToolCalls(ctx context.Context, calls []gollm.FunctionCall) ([]PendingCall, error) {
	pending := make([]PendingCall, len(calls))
	for i, call := range calls {
		klog.V(2).InfoS("parsing tool invocation", "name", call.Name, "argument_keys", logMapKeys(call.Arguments))
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
		klog.V(1).InfoS("tool invocation parsed", "name", call.Name, "modifies_resource", pending[i].ModifiesResource, "interactive", isInteractive)
	}
	return pending, nil
}

func (l *Loop) modifiesResource(parsed *tools.ToolCall, call gollm.FunctionCall) string {
	if isObservationToolName(call.Name) {
		return "no"
	}
	toolDecision := parsed.GetTool().CheckModifiesResource(call.Arguments)
	if toolDecision == "yes" || hasKnownMutatingKubectlInvocation(call) {
		return "yes"
	}
	if hasBlockedReadOnlyFastPathFeature(call) {
		return "unknown"
	}
	if isNonMutatingKubectlInvocation(call) {
		return "no"
	}
	return toolDecision
}

func hasKnownMutatingKubectlInvocation(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := splitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if containsMutatingKubectlCommand(command) {
			return true
		}
	}
	return false
}

func hasBlockedReadOnlyFastPathFeature(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := splitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if containsShellEvaluation(command) || containsDisallowedReadOnlyKubectlSubcommand(command) {
			return true
		}
	}
	return false
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

func containsDisallowedReadOnlyKubectlSubcommand(command string) bool {
	segments := splitShellPipeline(command)
	for _, segment := range segments {
		fields := strings.Fields(segment)
		if len(fields) == 0 || fields[0] != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, 0)
		if !ok || strings.ToLower(verb) != "auth" {
			continue
		}
		if !isReadOnlyKubectlSubcommand(fields, verbIndex) {
			return true
		}
	}
	return false
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
		if field == "-c" || isShellShortFlagWithC(field) {
			script := strings.TrimSpace(fields[i+1])
			return script, script != ""
		}
	}
	return "", false
}

func isShellShortFlagWithC(field string) bool {
	field = strings.TrimSpace(field)
	return strings.HasPrefix(field, "-") &&
		!strings.HasPrefix(field, "--") &&
		len(field) > 2 &&
		strings.Contains(field[1:], "c")
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
		if containsShellEvaluation(segment) || containsShellRedirection(segment) || containsMutatingKubectlVerb(segment) {
			return false
		}
	}

	firstFields := strings.Fields(segments[0])
	verb, verbIndex, ok := kubectlVerbAndIndexFromFields(firstFields, 0)
	if len(firstFields) < 2 || firstFields[0] != "kubectl" || !ok || !isKubectlReadOnlyVerb(verb) {
		return false
	}
	if !isReadOnlyKubectlSubcommand(firstFields, verbIndex) {
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

func containsShellEvaluation(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
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
		if inSingle {
			continue
		}
		if ch == '`' {
			return true
		}
		if ch == '$' && i+1 < len(command) && command[i+1] == '(' {
			return true
		}
		if (ch == '<' || ch == '>') && i+1 < len(command) && command[i+1] == '(' {
			return true
		}
	}
	return containsHeredocOperator(command)
}

func containsHeredocOperator(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command)-1; i++ {
		ch := command[i]
		if escaped {
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
		if !inSingle && !inDouble && ch == '<' && command[i+1] == '<' {
			return true
		}
	}
	return false
}

func containsShellRedirection(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
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
		if !inSingle && !inDouble && (ch == '>' || ch == '<') {
			return true
		}
	}
	return false
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
	fields := shellWords(strings.ToLower(segment))
	kubectlIndex, ok := kubectlExecutableIndexFromFields(fields)
	if !ok {
		return false
	}
	verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, kubectlIndex)
	if !ok {
		return false
	}
	if isKubectlMutatingVerb(verb) {
		return true
	}
	if subcommand, ok := kubectlSubcommandFromFields(fields, verbIndex); ok && isKubectlMutatingSubcommand(verb, subcommand) {
		return true
	}
	return false
}

func containsMutatingKubectlCommand(command string) bool {
	for _, segment := range splitShellPipeline(command) {
		if containsMutatingKubectlVerb(segment) {
			return true
		}
	}
	return false
}

func kubectlExecutableIndexFromFields(fields []string) (int, bool) {
	for i, field := range fields {
		field = strings.Trim(field, "'\"")
		if field == "" {
			continue
		}
		if i == 0 || isShellAssignment(fields[i-1]) {
			if isShellAssignment(field) {
				continue
			}
		}
		if field == "kubectl" {
			return i, true
		}
		return -1, false
	}
	return -1, false
}

func isShellAssignment(field string) bool {
	field = strings.Trim(field, "'\"")
	if field == "" || strings.HasPrefix(field, "-") {
		return false
	}
	index := strings.IndexByte(field, '=')
	if index <= 0 {
		return false
	}
	name := field[:index]
	for i, ch := range name {
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
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
				if kubectlGlobalFlagName(strings.SplitN(field, "=", 2)[0]) {
					continue
				}
				return "", -1, false
			}
			if kubectlGlobalFlagRequiresValue(field) {
				if i+1 >= len(fields) {
					return "", -1, false
				}
				i++
				continue
			}
			if kubectlGlobalFlagName(field) {
				continue
			}
			return "", -1, false
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortGlobalFlagRequiresValue(field) && len(field) == 2 {
				if i+1 >= len(fields) {
					return "", -1, false
				}
				i++
				continue
			}
			if kubectlShortGlobalFlagName(field) {
				continue
			}
			return "", -1, false
		}
		return strings.ToLower(field), i, true
	}
	return "", -1, false
}

func kubectlSubcommandFromFields(fields []string, verbIndex int) (string, bool) {
	if verbIndex < 0 || verbIndex >= len(fields) {
		return "", false
	}
	for i := verbIndex + 1; i < len(fields); i++ {
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
		return strings.ToLower(field), true
	}
	return "", false
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

func kubectlGlobalFlagName(flag string) bool {
	if kubectlGlobalFlagRequiresValue(flag) {
		return true
	}
	switch flag {
	case "--alsologtostderr", "--insecure-skip-tls-verify", "--match-server-version",
		"--skip-headers", "--skip-log-headers", "--stderrthreshold", "--use-openapi-print-columns",
		"--warnings-as-errors":
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

func kubectlShortGlobalFlagName(flag string) bool {
	return kubectlShortGlobalFlagRequiresValue(flag)
}

func isKubectlReadOnlyVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "get", "describe", "logs", "top", "api-resources", "api-versions", "version", "config", "auth":
		return true
	default:
		return false
	}
}

func isReadOnlyKubectlSubcommand(fields []string, verbIndex int) bool {
	if verbIndex < 0 || verbIndex >= len(fields) {
		return false
	}
	if strings.ToLower(strings.Trim(fields[verbIndex], "'\"")) != "auth" {
		return true
	}
	for i := verbIndex + 1; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlAuthFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlAuthShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		switch strings.ToLower(field) {
		case "can-i", "whoami":
			return true
		default:
			return false
		}
	}
	return false
}

func kubectlAuthFlagRequiresValue(flag string) bool {
	return kubectlFlagRequiresValue(flag)
}

func kubectlAuthShortFlagRequiresValue(flag string) bool {
	return kubectlShortFlagRequiresValue(flag)
}

func isKubectlMutatingVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "apply", "delete", "patch", "replace", "edit", "scale", "autoscale", "set", "create",
		"annotate", "label", "cordon", "uncordon", "drain", "taint", "expose", "run", "exec",
		"debug", "attach", "cp":
		return true
	default:
		return false
	}
}

func isKubectlMutatingSubcommand(verb, subcommand string) bool {
	switch strings.ToLower(verb) {
	case "rollout":
		switch strings.ToLower(subcommand) {
		case "pause", "restart", "resume", "undo":
			return true
		}
	case "auth":
		return strings.EqualFold(subcommand, "reconcile")
	case "certificate":
		switch strings.ToLower(subcommand) {
		case "approve", "deny":
			return true
		}
	}
	return false
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
	hasUnknown := false
	hasKnownMutation := false
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		if call.ModifiesResource == "unknown" {
			hasUnknown = true
		} else {
			hasKnownMutation = true
		}
		descriptions = append(descriptions, call.ParsedToolCall.Description())
	}
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		errorMessage := "read-only mode is enabled; commands that modify Kubernetes resources are blocked."
		if call.ModifiesResource == "unknown" {
			errorMessage = "read-only mode is enabled; command safety could not be verified, so the command was blocked."
		}
		l.appendToolObservation(call, map[string]any{
			"error":              errorMessage,
			"status":             "blocked",
			"retryable":          readOnlyBlockRetryable(call.ModifiesResource, hasKnownMutation),
			"retry_scope":        readOnlyBlockRetryScope(call.ModifiesResource, hasKnownMutation),
			"modifies_resource":  call.ModifiesResource,
			"read_only_mode":     true,
			"allowed_operation":  "read-only diagnostics such as get, describe, logs, top, events",
			"blocked_operation":  call.ParsedToolCall.Description(),
			"suggested_response": readOnlyBlockedSuggestedResponse(call.ModifiesResource, hasKnownMutation),
		})
	}
	klog.V(0).InfoS("read-only modifying calls rejected", "has_unknown", hasUnknown, "has_known_mutation", hasKnownMutation, "descriptions", descriptions)
	if len(descriptions) == 0 {
		descriptions = append(descriptions, "안전성이 확인되지 않은 명령")
	}
	headline := "read-only 모드가 활성화되어 리소스 변경 명령을 차단했습니다"
	code := "readonly_mutation_blocked"
	correction := "Read-only mode blocked a command that can modify Kubernetes resources. This is a user/request blocker, not an agent retry. Do not repeat the same mutation. If the user asked for a change, explain that read-only mode blocks it and provide one manual recommendation."
	if hasUnknown {
		headline = "안전한 read-only 진단 명령으로 확인되지 않아 실행하지 않았습니다"
	}
	if hasUnknown && hasKnownMutation {
		headline = "read-only 모드에서 변경 명령과 안전성이 확인되지 않은 명령을 차단했습니다"
	}
	if hasUnknown && !hasKnownMutation {
		code = "readonly_unknown_command_blocked"
		correction = "The runtime did not execute the previous command because it could not verify it as a safe Kubernetes diagnostic. This is an agent retry, not a user-request blocker. Retry with one real read-only kubectl command such as get, describe, logs, top, api-resources, api-versions, version, config, or auth can-i/whoami. If the active phase is complete, emit phase_progress. Do not return final_report only because this command was blocked."
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeError, headline+":\n* "+strings.Join(descriptions, "\n* "))
	kind := GateOutcomeUserRequestBlocked
	retryable := false
	retryScope := RetryScopeUserRequest
	branch := BranchBlockUserRequest
	if hasUnknown && !hasKnownMutation {
		kind = GateOutcomeAgentCommandRetry
		retryable = true
		retryScope = RetryScopeAgentCommand
		branch = BranchRetryStep
	}
	l.applyGateOutcome(GateOutcome{
		Kind:            kind,
		Code:            code,
		Retryable:       retryable,
		RetryScope:      retryScope,
		UserMessage:     "read-only 차단 correction이 반복되어 진단을 중단합니다.",
		ModelCorrection: correction,
		CorrectionMode:  CorrectionModeAppendPlain,
		BranchPolicy:    branch,
	})
}

func readOnlyBlockedSuggestedResponse(modifiesResource string, hasKnownMutation bool) string {
	if modifiesResource == "unknown" && !hasKnownMutation {
		return "Agent retry: replace the blocked command with a concrete read-only kubectl diagnostic, or emit phase_progress if the active phase is complete. Do not finalize solely because this command was blocked."
	}
	return "User/request blocker: do not repeat this mutation while read-only mode is enabled. Explain the read-only blocker only when a final response is otherwise appropriate."
}

func readOnlyBlockRetryable(modifiesResource string, hasKnownMutation bool) bool {
	return modifiesResource == "unknown" && !hasKnownMutation
}

func readOnlyBlockRetryScope(modifiesResource string, hasKnownMutation bool) string {
	if modifiesResource == "unknown" && !hasKnownMutation {
		return "agent_correct_command"
	}
	return "user_request_blocked_by_read_only"
}

func (l *Loop) requestApproval() {
	descriptions := make([]string, 0, len(l.pendingCalls))
	for _, call := range l.pendingCalls {
		descriptions = append(descriptions, call.ParsedToolCall.Description())
	}
	prompt := "다음 명령은 실행 전 승인이 필요합니다:\n* " + strings.Join(descriptions, "\n* ")
	prompt += "\n\n진행할까요?"
	klog.V(0).InfoS("approval requested", "calls", len(l.pendingCalls), "descriptions", descriptions)
	l.state = StateWaitingApproval
	l.refreshInputOwner()
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
	klog.V(0).InfoS("handling approval choice", "choice", choice, "pending", len(l.pendingCalls))
	switch choice {
	case 1:
		if err := l.dispatchToolCalls(ctx); err != nil {
			return err
		}
	case 2:
		l.skipPermissions = true
		klog.V(0).InfoS("approval granted with skip future permissions")
		if err := l.dispatchToolCalls(ctx); err != nil {
			return err
		}
	case 3:
		klog.V(0).InfoS("approval declined", "pending", len(l.pendingCalls))
		for _, call := range l.pendingCalls {
			l.appendToolObservation(call, map[string]any{
				"error":     "User declined to run this operation.",
				"status":    "declined",
				"retryable": false,
			})
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "사용자가 작업 실행을 거부했습니다.")
		l.applyRuntimeCleanup(cleanupApprovalDeclinedPolicy())
		l.currIteration++
		l.state = StateRunning
	default:
		return fmt.Errorf("잘못된 승인 선택: %d", choice)
	}
	return nil
}

func (l *Loop) dispatchToolCalls(ctx context.Context) error {
	klog.V(0).InfoS("tool dispatch starting", "pending", len(l.pendingCalls), "summaries", logPendingCallSummaries(l.pendingCalls))
	l.toolDispatchInProgress = true
	l.refreshInputOwner()
	defer func() {
		l.toolDispatchInProgress = false
		l.refreshInputOwner()
	}()
	var failureOutcome *GateOutcome
	for _, call := range l.pendingCalls {
		description := call.ParsedToolCall.Description()
		toolStart := time.Now()
		klog.V(0).InfoS("tool invocation starting", "tool", call.FunctionCall.Name, "description", description, "modifies_resource", call.ModifiesResource)
		l.addMessage(api.MessageSourceModel, api.MessageTypeToolCallRequest, description)

		output, err := call.ParsedToolCall.InvokeTool(ctx, tools.InvokeToolOptions{
			Kubeconfig: l.cfg.Kubeconfig,
			WorkDir:    l.workDir,
			Executor:   l.executor,
		})
		if err != nil {
			if ctx.Err() != nil {
				klog.ErrorS(err, "tool invocation cancelled", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart))
				return err
			}
			result := toolFailureResultFromError(err)
			status, errText, keys := logResultSummary(result)
			klog.V(0).InfoS("tool invocation failed", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart), "status", status, "error", errText)
			klog.V(2).InfoS("tool failure result summary", "tool", call.FunctionCall.Name, "keys", keys)
			if outcome, failed := l.annotateToolFailureResult(call, result); failed && failureOutcome == nil {
				failureOutcome = &outcome
			}
			l.appendToolObservation(call, result)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
			break
		}

		result, err := tools.ToolResultToMap(output)
		if err != nil {
			result = toolFailureResultFromMapError(err)
			status, errText, keys := logResultSummary(result)
			klog.V(0).InfoS("tool result conversion failed", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart), "status", status, "error", errText)
			klog.V(2).InfoS("tool conversion failure result summary", "tool", call.FunctionCall.Name, "keys", keys)
			if outcome, failed := l.annotateToolFailureResult(call, result); failed && failureOutcome == nil {
				failureOutcome = &outcome
			}
			l.appendToolObservation(call, result)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
			break
		}
		if outcome, failed := l.annotateToolFailureResult(call, result); failed && failureOutcome == nil {
			failureOutcome = &outcome
		}
		status, errText, keys := logResultSummary(result)
		klog.V(0).InfoS("tool invocation completed", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart), "status", status, "error", errText)
		klog.V(2).InfoS("tool result summary", "tool", call.FunctionCall.Name, "keys", keys)
		l.appendToolObservation(call, result)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
	}

	if failureOutcome != nil {
		klog.V(0).InfoS("tool dispatch produced failure outcome", "code", failureOutcome.Code, "kind", failureOutcome.Kind, "retryable", failureOutcome.Retryable)
		l.applyGateOutcome(*failureOutcome)
		return nil
	}

	l.pendingCalls = nil
	l.currIteration++
	l.requestPostGuideCompletionDirective()
	if l.shouldCompactBeforeNextSend() {
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}
	l.state = StateRunning
	klog.V(0).InfoS("tool dispatch completed", "next_iteration", l.currIteration+1)
	return nil
}

func (l *Loop) appendToolObservation(call PendingCall, result map[string]any) {
	status, errText, keys := logResultSummary(result)
	klog.V(2).InfoS("appending tool observation", "tool", call.FunctionCall.Name, "status", status, "error", errText, "keys", keys, "shim", l.cfg.EnableToolUseShim)
	l.recordAction(call, result)
	l.trackMutationVerification(call, result)
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
	phase := l.currentPhaseRef()
	record := actionRecord{
		Step:       l.actionSeq,
		Tool:       call.FunctionCall.Name,
		Command:    command,
		ResultHash: contextHash(fmt.Sprintf("%v", result)),
		Result:     compactObservationResult(result),
		Clues:      extractObservationClues(result),
	}
	if phase.Index != 0 || strings.TrimSpace(phase.Name) != "" {
		record.Phase = &phase
	}
	if ok {
		record.Target = &target
	}
	l.completedActions = append(l.completedActions, record)
	klog.V(1).InfoS("action recorded", "step", record.Step, "tool", record.Tool, "command", trimForLog(record.Command, 180), "result_hash", record.ResultHash)
	if len(l.completedActions) > 12 {
		l.completedActions = l.completedActions[len(l.completedActions)-12:]
	}
	if guideProgressObservationUseful(result) && l.guideProgressAllowedForCurrentPhase() {
		if step, ok := guideStepCompletedFromFunctionCall(call.FunctionCall); ok {
			l.markGuideStepCompleted(step)
		} else if step, ok := l.inferGuideStepCompletedFromFunctionCall(call.FunctionCall); ok {
			l.markGuideStepCompleted(step)
		}
	}
}

func (l *Loop) consumeGuideProgress(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	handled := false
	for _, call := range calls {
		if call.Name != internalGuideProgressCall {
			remaining = append(remaining, call)
			continue
		}
		handled = true
		if !l.guideProgressAllowedForCurrentPhase() {
			return remaining, l.applyModelOutputCorrectionGate("guide_progress_wrong_phase", "잘못된 phase의 guide_progress가 반복되어 진단을 중단합니다.", "guide_progress is only valid while a resource guide is active inside guided_diagnosis. Continue the current phase with a valid structured output or action.")
		}
		step, ok := guideStepCompletedFromArguments(call.Arguments)
		if !ok {
			return remaining, l.applyModelOutputCorrectionGate("invalid_guide_progress", "guide_progress 형식 오류가 반복되어 진단을 중단합니다.", "guide_progress payload was invalid. Return guide_progress with step_completed as a positive 1-based guide step index and evidence_useful=true only when live evidence advanced that step.")
		}
		completedAll := l.markGuideStepCompleted(step)
		klog.V(0).InfoS("guide progress consumed", "step_completed", step, "completed_all", completedAll)
		l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
			ID:   call.ID,
			Name: call.Name,
			Result: map[string]any{
				"status":         "recorded",
				"step_completed": step,
			},
		})
		if completedAll {
			l.requestPostGuideCompletionDirective()
		}
	}
	if handled && len(remaining) == 0 {
		l.currIteration++
		l.state = StateRunning
		return nil, true
	}
	return remaining, false
}

func (l *Loop) guideProgressAllowedForCurrentPhase() bool {
	if l.guideStepState == nil {
		return false
	}
	if l.phaseStepState == nil {
		return true
	}
	return strings.EqualFold(l.phaseStepState.currentStep().Name, "guided_diagnosis")
}

func guideProgressObservationUseful(result map[string]any) bool {
	return toolResultSucceeded(result)
}

func guideStepCompletedFromFunctionCall(call gollm.FunctionCall) (int, bool) {
	raw, ok := call.Arguments["guide_progress"].(map[string]any)
	if !ok {
		return 0, false
	}
	return guideStepCompletedFromArguments(raw)
}

func guideStepCompletedFromArguments(raw map[string]any) (int, bool) {
	if useful, ok := raw["evidence_useful"]; ok && !boolFromAny(useful) {
		return 0, false
	}
	value, ok := raw["step_completed"]
	if !ok {
		return 0, false
	}
	step := intFromAny(value)
	return step, step > 0
}

func (l *Loop) inferGuideStepCompletedFromFunctionCall(call gollm.FunctionCall) (int, bool) {
	state := l.guideStepState
	if state == nil {
		return 0, false
	}
	command, ok := commandString(call.Arguments["command"])
	if !ok {
		return 0, false
	}
	remaining := state.remainingSteps()
	if len(remaining) == 0 {
		return 0, false
	}
	nextStep := remaining[0]
	if guideStepCommandMatches(state.stepDetail(nextStep), command) {
		return nextStep, true
	}
	return 0, false
}

func guideStepCommandMatches(step guideStepDetail, command string) bool {
	rendered := strings.TrimSpace(step.RenderedCommand)
	if rendered == "" || strings.Contains(rendered, "{{") || strings.TrimSpace(command) == "" {
		return false
	}
	return normalizeGuideCommand(rendered) == normalizeGuideCommand(command)
}

func normalizeGuideCommand(command string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
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
	l.publishRuntimeSnapshot()
	klog.V(2).InfoS("react output message queued", "source", source, "type", messageType, "payload_type", fmt.Sprintf("%T", payload))
	l.output <- &api.Message{
		ID:        uuid.NewString(),
		Source:    source,
		Type:      messageType,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}
