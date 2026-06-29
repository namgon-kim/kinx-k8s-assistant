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

	cancel context.CancelFunc
	once   sync.Once

	inputOwner atomic.Int32
}

type guideStepState struct {
	GuideID      string
	Title        string
	TotalSteps   int
	StepFilePath string
	StepHash     string
	StepDetails  []guideStepDetail
	Completed    map[int]bool
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
		if !g.Completed[i] {
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
		if !g.Completed[i] {
			remaining = append(remaining, i)
		}
	}
	return remaining
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

func (l *Loop) InputOwner() InputOwner {
	if l == nil {
		return InputOwnerOrchestrator
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

func (l *Loop) setInputOwner(owner InputOwner) {
	if l == nil {
		return
	}
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
		defs := collectFunctionDefinitionsForProfile(l.registry.Tools, l.toolProfile, true)
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
			l.setInputOwner(InputOwnerOrchestrator)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, ">>>")
			if !l.waitForInput(ctx) {
				return
			}
		case StateWaitingApproval:
			l.setInputOwner(InputOwnerApproval)
			if !l.waitForApproval(ctx) {
				return
			}
		case StateWaitingDirectionChoice:
			l.setInputOwner(InputOwnerReactChoice)
			if !l.waitForDirectionChoice(ctx) {
				return
			}
		case StateWaitingDirectionText:
			l.setInputOwner(InputOwnerReactText)
			if !l.waitForDirectionText(ctx) {
				return
			}
		case StateRunning:
			l.setInputOwner(InputOwnerOrchestrator)
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
	l.captureConversationMemory()
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
	l.state = StateRunning
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

	sentContent := l.buildIterationSendContent()
	streamedText, functionCalls, err := l.sendAndCollectStreaming(ctx, sentContent)
	if err != nil {
		if !isContextLengthError(err) {
			return err
		}
		if ok := l.compactAfterContextLengthError(err); !ok {
			return err
		}
		sentContent = l.buildIterationSendContent()
		streamedText, functionCalls, err = l.sendAndCollectStreaming(ctx, sentContent)
		if err != nil {
			return err
		}
	}
	l.noteContextContent(sentContent...)
	l.currChatContent = nil
	l.pendingResponseDirective = ""

	if len(functionCalls) == 0 {
		if strings.TrimSpace(streamedText) != "" {
			// Native text fallback intentionally recovers only the minimum
			// planning gates. Full structured recovery remains owned by native
			// function calling or shim parsing so runtime-internal calls do not
			// silently diverge between execution modes.
			if parsed, err := parseReActResponse(streamedText); err == nil && l.requirementAnalysis == nil && parsed.RequirementAnalysis != nil {
				functionCalls = []gollm.FunctionCall{{
					Name:      internalRequirementAnalysisCall,
					Arguments: requirementAnalysisToArguments(parsed.RequirementAnalysis),
				}}
			} else if err == nil && l.requirementAnalysis != nil && l.phaseStepState == nil && parsed.PhasePlan != nil {
				if args, mapErr := toMap(parsed.PhasePlan); mapErr == nil {
					functionCalls = []gollm.FunctionCall{{
						Name:      internalPhasePlanCall,
						Arguments: args,
					}}
				}
			} else if err == nil && l.phaseStepState != nil && parsed.PhaseProgress != nil {
				if args, mapErr := toMap(parsed.PhaseProgress); mapErr == nil {
					functionCalls = []gollm.FunctionCall{{
						Name:      internalPhaseProgressCall,
						Arguments: args,
					}}
				}
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
		if handled := l.rejectPlainAnswerDuringMutationVerification(streamedText); handled {
			return nil
		}
		if handled := l.rejectPlainAnswerDuringMutationContinuation(streamedText); handled {
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
		}
		l.currIteration = 0
		l.pendingCalls = nil
		l.state = StateDone
		return nil
	}
	deferredProgressText := strings.TrimSpace(streamedText)
	functionCalls = normalizeAssistantStructuredFunctionCalls(functionCalls)

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
	functionCalls, nextDirectionsHandled = l.consumeNextDirections(ctx, functionCalls)
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
		return nil
	}

	l.emitAcceptedProgressText(ctx, deferredProgressText)
	return l.dispatchToolCalls(ctx)
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
	if l.guidedPhaseProgressRequested && !onlyFunctionCall(calls, internalPhaseProgressCall) {
		message := "The runtime already requested `phase_progress` for the completed guided_diagnosis phase. Do not emit another action. Return only a phase_progress object completing guided_diagnosis."
		l.queueResponseDirective(message)
		if !l.appendCorrectionWithCompaction("guided_phase_progress_required", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "guided_diagnosis phase_progress 요청이 반복적으로 무시되어 진단을 중단합니다.")
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
	if l.finalReportRequested && !onlyFunctionCall(calls, internalFinalReportCall) {
		message := "The runtime already requested `final_report` after completing the resource-guide diagnostic steps. Do not emit another action. Return only a final_report object."
		l.queueResponseDirective(message)
		if !l.appendCorrectionWithCompaction("final_report_required", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "final_report 요청이 반복적으로 무시되어 진단을 중단합니다.")
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
		if !l.appendCorrectionWithCompaction("conversation_tool_call", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "대화/확인 요청에서 tool call이 반복되어 진단을 중단합니다.")
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
	if hasBlockedReadOnlyFastPathFeature(call) {
		return "unknown"
	}
	if isNonMutatingKubectlInvocation(call) {
		return "no"
	}
	return parsed.GetTool().CheckModifiesResource(call.Arguments)
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
	hasUnknown := false
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		if call.ModifiesResource == "unknown" {
			hasUnknown = true
		}
		descriptions = append(descriptions, call.ParsedToolCall.Description())
		errorMessage := "read-only mode is enabled; commands that modify Kubernetes resources are blocked."
		if call.ModifiesResource == "unknown" {
			errorMessage = "read-only mode is enabled; command safety could not be verified, so the command was blocked."
		}
		l.appendToolObservation(call, map[string]any{
			"error":              errorMessage,
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
		descriptions = append(descriptions, "안전성이 확인되지 않은 명령")
	}
	headline := "read-only 모드가 활성화되어 리소스 변경 명령을 차단했습니다"
	if hasUnknown {
		headline = "read-only 모드가 활성화되어 안전성이 확인되지 않은 명령을 차단했습니다"
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeError, headline+":\n* "+strings.Join(descriptions, "\n* "))
	if !l.appendCorrection("readonly_mutation_blocked", "Read-only mode blocked the proposed mutation. Do not repeat the same action or restate the same recommendation in another loop. Your next response must be a single final answer with the cause, evidence, and one manual recommendation.") {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "read-only 차단 correction이 반복되어 진단을 중단합니다.")
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		return
	}
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
	l.state = StateWaitingApproval
	l.setInputOwner(InputOwnerApproval)
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
		for _, call := range l.pendingCalls {
			l.appendToolObservation(call, map[string]any{
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
	l.requestPostGuideCompletionDirective()
	if l.shouldCompactBeforeNextSend() {
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}
	l.state = StateRunning
	return nil
}

func (l *Loop) appendToolObservation(call PendingCall, result map[string]any) {
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
			if !l.appendCorrectionWithCompaction("guide_progress_wrong_phase", "guide_progress is only valid while a resource guide is active inside guided_diagnosis. Continue the current phase with a valid structured output or action.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "잘못된 phase의 guide_progress가 반복되어 진단을 중단합니다.")
				l.currIteration = 0
				l.state = StateDone
				return remaining, true
			}
			l.currIteration++
			l.state = StateRunning
			return remaining, true
		}
		step, ok := guideStepCompletedFromArguments(call.Arguments)
		if !ok {
			if !l.appendCorrectionWithCompaction("invalid_guide_progress", "guide_progress payload was invalid. Return guide_progress with step_completed as a positive 1-based guide step index and evidence_useful=true only when live evidence advanced that step.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "guide_progress 형식 오류가 반복되어 진단을 중단합니다.")
				l.currIteration = 0
				l.state = StateDone
				return remaining, true
			}
			l.currIteration++
			l.state = StateRunning
			return remaining, true
		}
		completedAll := l.markGuideStepCompleted(step)
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
	l.output <- &api.Message{
		ID:        uuid.NewString(),
		Source:    source,
		Type:      messageType,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}
