package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	reactcontract "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/request"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/kube"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/language"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/prompt"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
	"k8s.io/klog/v2"
)

type Loop struct {
	cfg      *config.Config
	llm      gollm.Client
	chat     gollm.Chat
	lang     *language.Translator
	registry *toolconnector.Registry
	executor sandbox.Executor
	workDir  string

	input  chan any
	output chan *api.Message

	session *session.State
	// control is retained temporarily for package-local test fixtures. Runtime
	// transitions use session, the canonical mutable state owner.
	control            RuntimeControlState
	currIteration      int
	currChatContent    []any
	contextBlockHashes map[string]struct{}
	pendingCalls       []PendingCall
	skipPermissions    bool

	systemPrompt            string
	promptOptions           promptOptions
	toolProfile             ToolProfile
	requestIntent           request.Intent
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
	pendingResponseDirective     string
	pendingFinalReport           *finalReport
	pendingNextDirections        *nextDirections
	pendingDirectionPrompt       *directionPromptState
	pendingMutationVerification  *pendingMutationVerification
	mutationContinuationAttempts int

	cancel context.CancelFunc
	once   sync.Once

	inputOwner      atomic.Int32
	runtimeSnapshot atomic.Value
}

func New(cfg *config.Config) (*Loop, error) {
	klog.V(0).InfoS("react loop creating", "provider", cfg.LLMProvider, "model", cfg.Model, "shim", cfg.EnableToolUseShim, "read_only", cfg.ReadOnly, "mcp", cfg.MCPClient)
	llmClient, err := newModelClient(cfg)
	if err != nil {
		klog.ErrorS(err, "LLM client creation failed", "provider", cfg.LLMProvider)
		return nil, err
	}
	klog.V(0).InfoS("react loop created", "provider", cfg.LLMProvider)
	return &Loop{
		cfg:     cfg,
		llm:     llmClient,
		input:   make(chan any, 1),
		output:  make(chan *api.Message, 32),
		session: session.New(),
		control: session.InitialControl(),
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
		klog.V(0).InfoS("react loop closing", "lifecycle", logStateName(l.loopLifecycle()), "work_dir", l.workDir)
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
	l.executor = newExecutor()
	klog.V(1).InfoS("react work directory created", "path", workDir)

	registry, err := newToolRegistry(ctx, l.executor, l.cfg)
	if err != nil {
		return fmt.Errorf("tool registry 초기화 실패: %w", err)
	}
	l.registry = registry
	klog.V(0).InfoS("react registry initialized", "tools", len(registry.Tools.Names()))

	l.lang = language.New(l.cfg)
	klog.V(0).InfoS("language translator configured", "language", l.cfg.Lang.Language, "enabled", l.lang != nil && l.lang.Enabled())

	l.requestIntent = request.General
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
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
	}

	for {
		select {
		case <-ctx.Done():
			l.transitionControl(RuntimeControlExited)
			return
		default:
		}

		if handled := l.auditRuntimeState(); handled {
			continue
		}

		switch l.loopLifecycle() {
		case LoopLifecycleAwaitingUserInput:
			l.refreshInputOwner()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, ">>>")
			if !l.waitForInput(ctx) {
				return
			}
		case LoopLifecycleWaitingApproval:
			l.refreshInputOwner()
			if !l.waitForApproval(ctx) {
				return
			}
		case LoopLifecycleWaitingContinuationChoice:
			l.refreshInputOwner()
			if !l.waitForDirectionChoice(ctx) {
				return
			}
		case LoopLifecycleWaitingContinuationText:
			l.refreshInputOwner()
			if !l.waitForDirectionText(ctx) {
				return
			}
		case LoopLifecycleModelTurn:
			l.refreshInputOwner()
			if err := l.runIteration(ctx); err != nil {
				l.pendingCalls = nil
				l.currChatContent = nil
				l.currIteration = 0
				l.transitionControl(RuntimeControlAwaitingUserQuery)
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			}
		case LoopLifecycleExited:
			return
		}
	}
}

func (l *Loop) startQuery(query string) error {
	intent := request.Classify(query)
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
	l.currChatContent = append(l.currChatContent, prompt.RequirementAnalysis())
	l.currChatContent = append(l.currChatContent, prompt.RequirementAnalysisDefinitions())
	l.currChatContent = append(l.currChatContent, query)
	l.contextBlockHashes = nil
	l.pendingCalls = nil
	l.originalQuery = query
	l.requirementAnalysis = nil
	l.requestContext = nil
	l.phaseStepState = nil
	l.mutableSession().Context.ResetRequest()
	l.session.Context.OriginalQuery = query
	l.session.Phase.Reset()
	l.session.Verification.Reset()
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
	l.pendingResponseDirective = ""
	l.pendingFinalReport = nil
	l.pendingNextDirections = nil
	l.pendingDirectionPrompt = nil
	l.pendingMutationVerification = nil
	l.mutationContinuationAttempts = 0
	l.transitionControl(RuntimeControlAwaitingRequirementAnalysis)
	klog.V(0).InfoS("query lifecycle initialized", "intent", intent, "lifecycle", logStateName(l.loopLifecycle()))
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

func (l *Loop) newPromptOptions(intent request.Intent, includeGuidance bool, includeClusterAPI bool) promptOptions {
	toolProfile := l.toolProfile
	if len(toolProfile.ToolNames) == 0 && l.registry != nil {
		toolProfile = selectToolProfile(l.registry.Tools, intent, l.originalQuery)
	}
	return promptOptions{
		EnableToolUseShim:          l.cfg.EnableToolUseShim,
		ReadOnly:                   l.cfg.ReadOnly,
		UserLanguage:               l.cfg.Lang.Language,
		TranslateOutput:            l.lang != nil && l.lang.Enabled(),
		IncludeGuidanceProtocol:    includeGuidance,
		IncludeManifestGuidelines:  intent == request.Manifest,
		IncludeClusterAPIGuardrail: includeClusterAPI,
		ToolProfile:                toolProfile,
	}
}

func (l *Loop) handleMetaQuery(ctx context.Context, query string) bool {
	switch query {
	case "clear", "reset":
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.requestIntent = request.General
		l.toolProfile = selectToolProfile(l.registry.Tools, l.requestIntent, "")
		l.promptOptions = l.newPromptOptions(l.requestIntent, false, false)
		if err := l.resetChatSession(); err != nil {
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			return true
		}
		l.clearConversationState()
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "대화 컨텍스트를 초기화했습니다.")
		return true
	case "exit", "quit":
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.applyRuntimeCleanup(cleanupExitPolicy())
		l.transitionControl(RuntimeControlExited)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
		return true
	case "model":
		klog.V(1).InfoS("react meta query handled", "query", query, "model", l.cfg.Model)
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Current model is `"+l.cfg.Model+"`")
		return true
	case "models":
		klog.V(0).InfoS("react meta query handled", "query", query)
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		models, err := l.llm.ListModels(ctx)
		if err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, err.Error())
		} else {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "Available models:\n\n  - "+strings.Join(models, "\n  - "))
		}
		return true
	case "tools":
		klog.V(0).InfoS("react meta query handled", "query", query, "tools", len(l.registry.Tools.Names()))
		l.transitionControl(RuntimeControlAwaitingUserQuery)
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
	l.mutableSession().Context.ResetRequest()
	l.session.Phase.Reset()
	l.session.Verification.Reset()
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
	l.pendingResponseDirective = ""
	l.pendingFinalReport = nil
	l.pendingNextDirections = nil
	l.pendingDirectionPrompt = nil
	l.pendingMutationVerification = nil
	l.mutationContinuationAttempts = 0
}

func (l *Loop) executeIteration(ctx context.Context) error {
	snapshot := l.RuntimeSnapshot()
	klog.V(1).InfoS("react iteration starting",
		"iteration", l.currIteration+1,
		"max_iterations", l.cfg.MaxIterations,
		"lifecycle", logStateName(l.loopLifecycle()),
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
		l.transitionControl(RuntimeControlAwaitingUserQuery)
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
	klog.V(1).InfoS("model response received", "duration", time.Since(sendStart), "text_len", len(streamedText), "function_calls", len(functionCalls), "call_names", logFunctionCallNames(functionCalls))
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
		l.transitionControl(RuntimeControlAwaitingUserQuery)
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

	// Locks for runtime-requested structured output must run before any
	// consumer can advance phase or clear the active control obligation.
	if handled := l.enforceRequestedStructuredDirective(functionCalls); handled {
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

	// Guide completion can create a new phase-progress obligation while
	// consuming this response. Re-apply the lock before any trailing call can
	// reach the action dispatcher.
	if handled := l.enforceRequestedStructuredDirective(functionCalls); handled {
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
	klog.V(1).InfoS("tool calls analyzed", "pending", len(pending), "summaries", logPendingCallSummaries(pending))

	if handled := l.rejectInteractiveToolCalls(); handled {
		klog.V(0).InfoS("interactive tool calls rejected")
		return nil
	}

	if l.cfg.ReadOnly && l.hasModifyingCalls() {
		klog.V(0).InfoS("read-only mode blocking modifying tool calls", "pending", len(l.pendingCalls))
		klog.V(1).InfoS("read-only blocked call summaries", "pending", logPendingCallSummaries(l.pendingCalls))
		l.rejectReadOnlyModifyingCalls()
		return nil
	}
	if !l.skipPermissions && l.hasModifyingCalls() {
		klog.V(0).InfoS("approval required for modifying tool calls", "pending", len(l.pendingCalls))
		klog.V(1).InfoS("approval required call summaries", "pending", logPendingCallSummaries(l.pendingCalls))
		l.emitAcceptedProgressText(ctx, deferredProgressText)
		l.requestApproval()
		return nil
	}

	l.emitAcceptedProgressText(ctx, deferredProgressText)
	klog.V(1).InfoS("dispatching tool calls without approval", "pending", len(l.pendingCalls))
	return l.dispatchToolCalls(ctx)
}

func (l *Loop) rejectPlainAnswerDuringNextDirections(text string) bool {
	if l.RuntimeSnapshot().Control != RuntimeControlAwaitingNextDirections || strings.TrimSpace(text) == "" {
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
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.refreshInputOwner()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime lifecycle invariant violation: "+message)
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
	case snapshot.Control == RuntimeControlAwaitingNextDirections && !onlyFunctionCall(calls, internalNextDirectionsCall):
		message := "The previous final_report was inconclusive and the runtime requested `next_directions`. Return only a next_directions object with concrete continuation options; do not emit action, final_report, phase_progress, or a plain answer yet."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("next_directions_required", "next_directions 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	case snapshot.Control == RuntimeControlAwaitingGuidedPhaseProgress && !onlyFunctionCall(calls, internalPhaseProgressCall):
		message := "The runtime already requested `phase_progress` for the completed guided_diagnosis phase. Do not emit another action. Return only a phase_progress object completing guided_diagnosis."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("guided_phase_progress_required", "guided_diagnosis phase_progress 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	case snapshot.Control == RuntimeControlAwaitingFinalReport && !onlyFunctionCall(calls, internalFinalReportCall):
		message := "The runtime already requested `final_report` after completing the resource-guide diagnostic steps. Do not emit another action. Return only a final_report object."
		l.queueResponseDirective(message)
		return l.applyModelOutputCorrectionGate("final_report_required", "final_report 요청이 반복적으로 무시되어 진단을 중단합니다.", message)
	}
	return false
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
	if extracted, ok := kube.ExtractShellScript(script); ok {
		script = extracted
	}
	commands := kube.SplitShellCommandList(script)
	if len(commands) == 0 {
		commands = []string{script}
	}
	for _, candidate := range commands {
		fields := kube.ShellWords(candidate)
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

type LoopLifecycleState = reactcontract.LoopLifecycleState

const (
	LoopLifecycleAwaitingUserInput         = reactcontract.LoopLifecycleAwaitingUserInput
	LoopLifecycleModelTurn                 = reactcontract.LoopLifecycleModelTurn
	LoopLifecycleWaitingApproval           = reactcontract.LoopLifecycleWaitingApproval
	LoopLifecycleWaitingContinuationChoice = reactcontract.LoopLifecycleWaitingContinuationChoice
	LoopLifecycleWaitingContinuationText   = reactcontract.LoopLifecycleWaitingContinuationText
	LoopLifecycleExited                    = reactcontract.LoopLifecycleExited
)

type InputOwner = reactcontract.InputOwner

const (
	InputOwnerOrchestrator = reactcontract.InputOwnerOrchestrator
	InputOwnerReactChoice  = reactcontract.InputOwnerReactChoice
	InputOwnerReactText    = reactcontract.InputOwnerReactText
	InputOwnerApproval     = reactcontract.InputOwnerApproval
)

type RuntimeControlState = reactcontract.RuntimeControlState

const (
	RuntimeControlUnset                                = reactcontract.RuntimeControlUnset
	RuntimeControlAwaitingUserQuery                    = reactcontract.RuntimeControlAwaitingUserQuery
	RuntimeControlAwaitingRequirementAnalysis          = reactcontract.RuntimeControlAwaitingRequirementAnalysis
	RuntimeControlAwaitingPhasePlan                    = reactcontract.RuntimeControlAwaitingPhasePlan
	RuntimeControlAwaitingModelStep                    = reactcontract.RuntimeControlAwaitingModelStep
	RuntimeControlAwaitingResourceGuideLookup          = reactcontract.RuntimeControlAwaitingResourceGuideLookup
	RuntimeControlAwaitingGuidedDiagnosisStep          = reactcontract.RuntimeControlAwaitingGuidedDiagnosisStep
	RuntimeControlAwaitingGuidedPhaseProgress          = reactcontract.RuntimeControlAwaitingGuidedPhaseProgress
	RuntimeControlAwaitingFinalReport                  = reactcontract.RuntimeControlAwaitingFinalReport
	RuntimeControlAwaitingNextDirections               = reactcontract.RuntimeControlAwaitingNextDirections
	RuntimeControlAwaitingApproval                     = reactcontract.RuntimeControlAwaitingApproval
	RuntimeControlExecutingTool                        = reactcontract.RuntimeControlExecutingTool
	RuntimeControlAwaitingMutationVerificationEvidence = reactcontract.RuntimeControlAwaitingMutationVerificationEvidence
	RuntimeControlAwaitingMutationVerificationResult   = reactcontract.RuntimeControlAwaitingMutationVerificationResult
	RuntimeControlAwaitingMutationContinuation         = reactcontract.RuntimeControlAwaitingMutationContinuation
	RuntimeControlAwaitingContinuationChoice           = reactcontract.RuntimeControlAwaitingContinuationChoice
	RuntimeControlAwaitingContinuationText             = reactcontract.RuntimeControlAwaitingContinuationText
	RuntimeControlExited                               = reactcontract.RuntimeControlExited
)

type PhaseStatus = reactcontract.PhaseStatus

const (
	PhasePending   = reactcontract.PhasePending
	PhaseActive    = reactcontract.PhaseActive
	PhaseCompleted = reactcontract.PhaseCompleted
	PhaseSkipped   = reactcontract.PhaseSkipped
)

type StepKind = reactcontract.StepKind

const (
	StepGeneralAction               = reactcontract.StepGeneralAction
	StepExplicitPhase               = reactcontract.StepExplicitPhase
	StepResourceGuideDiagnostic     = reactcontract.StepResourceGuideDiagnostic
	StepMutationEvidenceRequirement = reactcontract.StepMutationEvidenceRequirement
)

type StepStatus = reactcontract.StepStatus

const (
	StepPending   = reactcontract.StepPending
	StepActive    = reactcontract.StepActive
	StepCompleted = reactcontract.StepCompleted
	StepSkipped   = reactcontract.StepSkipped
	StepRetrying  = reactcontract.StepRetrying
)

type UserInputKind = reactcontract.UserInputKind

const (
	InputChoiceNumber = reactcontract.InputChoiceNumber
	InputApproval     = reactcontract.InputApproval
	InputSlashMeta    = reactcontract.InputSlashMeta
	InputFreeText     = reactcontract.InputFreeText
	InputEmpty        = reactcontract.InputEmpty
)

type InputHandlerKind = reactcontract.InputHandlerKind

const (
	InputHandlerNone             = reactcontract.InputHandlerNone
	InputHandlerOrchestratorMeta = reactcontract.InputHandlerOrchestratorMeta
	InputHandlerReactChoice      = reactcontract.InputHandlerReactChoice
	InputHandlerReactText        = reactcontract.InputHandlerReactText
	InputHandlerReactApproval    = reactcontract.InputHandlerReactApproval
	InputHandlerUserQuery        = reactcontract.InputHandlerUserQuery
)

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

// transitionControl is the only production mutation path for the runtime's
// top-level control state. Lifecycle is derived on demand from control, so it
// cannot independently change accepted output.
func (l *Loop) transitionControl(next RuntimeControlState) {
	if l == nil {
		return
	}
	l.mutableSession().Transition(next)
	l.control = next
}

func (l *Loop) mutableSession() *session.State {
	if l.session == nil {
		l.session = session.New()
		// Package-local tests may construct Loop with the compatibility field.
		if l.control != RuntimeControlUnset {
			l.session.Control = l.control
		}
	}
	return l.session
}

func (l *Loop) controlState() RuntimeControlState {
	if l == nil {
		return RuntimeControlUnset
	}
	if l.session != nil {
		return l.session.Control
	}
	return l.control
}

func (l *Loop) loopLifecycle() LoopLifecycleState {
	if l == nil {
		return LoopLifecycleAwaitingUserInput
	}
	return session.LifecycleFor(l.controlState())
}

func (l *Loop) transitionAfterToolFailure() {
	if l != nil && l.controlState() == RuntimeControlExecutingTool {
		l.transitionControl(RuntimeControlAwaitingModelStep)
	}
}

func reactPhaseRef(ref PhaseRef) reactcontract.PhaseRef {
	return ref
}

type PhaseRef = reactcontract.PhaseRef
type PhaseRuntimeState = reactcontract.PhaseRuntime
type PhaseSpec = reactcontract.PhaseRuntimeSpec
type StepRef = reactcontract.StepRef
type StepRuntimeState = reactcontract.StepRuntime

type InputDispatchDecision struct {
	Kind     UserInputKind
	Accepted bool
	Handler  InputHandlerKind
	Reason   string
}

func ClassifyUserInput(input string) UserInputKind {
	trimmed := strings.TrimSpace(input)
	switch {
	case trimmed == "":
		return InputEmpty
	case strings.HasPrefix(trimmed, "/"):
		return InputSlashMeta
	case isChoiceNumber(trimmed):
		return InputChoiceNumber
	case isApprovalToken(trimmed):
		return InputApproval
	default:
		return InputFreeText
	}
}

func DecideInputDispatch(control RuntimeControlState, kind UserInputKind) InputDispatchDecision {
	decision := InputDispatchDecision{Kind: kind, Handler: InputHandlerNone}
	switch control {
	case RuntimeControlAwaitingContinuationChoice:
		if kind == InputChoiceNumber {
			decision.Accepted = true
			decision.Handler = InputHandlerReactChoice
			return decision
		}
		decision.Reason = "choice prompt accepts a number only"
		return decision
	case RuntimeControlAwaitingApproval:
		if kind == InputChoiceNumber || kind == InputApproval {
			decision.Accepted = true
			decision.Handler = InputHandlerReactApproval
			return decision
		}
		decision.Reason = "approval prompt accepts approval choices only"
		return decision
	case RuntimeControlAwaitingContinuationText:
		if kind == InputSlashMeta {
			decision.Accepted = true
			decision.Handler = InputHandlerOrchestratorMeta
			return decision
		}
		if kind == InputFreeText || kind == InputChoiceNumber || kind == InputApproval || kind == InputEmpty {
			decision.Accepted = true
			decision.Handler = InputHandlerReactText
			return decision
		}
	case RuntimeControlAwaitingUserQuery:
		if kind == InputSlashMeta {
			decision.Accepted = true
			decision.Handler = InputHandlerOrchestratorMeta
			return decision
		}
		if kind == InputFreeText || kind == InputChoiceNumber || kind == InputApproval || kind == InputEmpty {
			decision.Accepted = true
			decision.Handler = InputHandlerUserQuery
			return decision
		}
	}
	decision.Reason = "current lifecycle does not accept user input of this type"
	return decision
}

func isChoiceNumber(input string) bool {
	for _, r := range input {
		if r < '0' || r > '9' {
			return false
		}
	}
	return input != ""
}

func isApprovalToken(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes", "n", "no", "예", "아니오":
		return true
	default:
		return false
	}
}

// RuntimeSnapshot is a shallow, same-goroutine projection of Loop control
// lifecycle. It centralizes lifecycle interpretation for prompts and diagnostics; it
// is not an immutable deep copy for cross-goroutine use.
type RuntimeSnapshot struct {
	Lifecycle  LoopLifecycleState
	Control    RuntimeControlState
	InputOwner InputOwner

	OriginalQuery string

	Requirement            *requirementAnalysis
	Request                *requestContext
	ResourceClassification *resourceClassification

	Phase        *phaseStepState
	Guide        *guideStepState
	PhaseRuntime *PhaseRuntimeState
	ActiveSteps  []StepRuntimeState

	PendingCalls                   []PendingCall
	PendingMutationVerification    *pendingMutationVerification
	MutationContinuationAttempts   int
	PendingFinalReport             *finalReport
	PendingNextDirections          *nextDirections
	PendingDirectionPrompt         *directionPromptState
	PendingDirective               string
	ResourceGuideInjected          bool
	RequiresResourceGuideLookupNow bool
}

func (l *Loop) RuntimeSnapshot() RuntimeSnapshot {
	if l == nil {
		return RuntimeSnapshot{Control: RuntimeControlUnset}
	}
	snapshot := RuntimeSnapshot{
		Lifecycle:                      l.loopLifecycle(),
		Control:                        l.controlState(),
		OriginalQuery:                  strings.TrimSpace(l.originalQuery),
		Requirement:                    l.requirementAnalysis,
		Request:                        l.requestContext,
		ResourceClassification:         l.resourceClassification,
		Phase:                          l.phaseStepState,
		Guide:                          l.guideStepState,
		PhaseRuntime:                   l.phaseStepState.runtimeState(),
		PendingCalls:                   append([]PendingCall(nil), l.pendingCalls...),
		PendingMutationVerification:    l.pendingMutationVerification,
		MutationContinuationAttempts:   l.mutationContinuationAttempts,
		PendingFinalReport:             l.pendingFinalReport,
		PendingNextDirections:          l.pendingNextDirections,
		PendingDirectionPrompt:         l.pendingDirectionPrompt,
		PendingDirective:               strings.TrimSpace(l.pendingResponseDirective),
		ResourceGuideInjected:          l.resourceGuideInjected,
		RequiresResourceGuideLookupNow: l.phaseStepRequiresResourceGuideLookup(),
	}
	snapshot.ActiveSteps = l.activeStepRuntimeStates(snapshot.PhaseRuntime)
	snapshot.InputOwner = snapshot.DerivedInputOwner()
	return snapshot
}

func (l *Loop) activeStepRuntimeStates(phase *PhaseRuntimeState) []StepRuntimeState {
	if l == nil {
		return nil
	}
	activePhase := PhaseRef{}
	if phase != nil {
		activePhase = phase.Active
	}
	var steps []StepRuntimeState
	for _, action := range l.completedActions {
		if action.Phase == nil || (action.Phase.Index == 0 && strings.TrimSpace(action.Phase.Name) == "") {
			continue
		}
		steps = append(steps, action.stepRuntimeState())
	}
	if l.guideStepState != nil {
		steps = append(steps, l.guideStepState.stepRuntimeStates(activePhase)...)
	}
	if l.pendingMutationVerification != nil {
		steps = append(steps, l.pendingMutationVerification.stepRuntimeStates(activePhase)...)
	}
	for i, call := range l.pendingCalls {
		if strings.HasPrefix(strings.TrimSpace(call.FunctionCall.Name), "__") {
			continue
		}
		steps = append(steps, pendingCallStepRuntimeState(activePhase, i+1, call))
	}
	return steps
}

func pendingCallStepRuntimeState(phase PhaseRef, index int, call PendingCall) StepRuntimeState {
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	if command == "" {
		command = strings.TrimSpace(stringFromAny(call.FunctionCall.Arguments["command"]))
	}
	return StepRuntimeState{
		Ref: StepRef{
			Phase: phase,
			Kind:  StepGeneralAction,
			Index: index,
		},
		Status:      StepActive,
		Description: strings.TrimSpace(call.FunctionCall.Name),
		Command:     command,
	}
}

func (a actionRecord) stepRuntimeState() StepRuntimeState {
	phase := PhaseRef{}
	if a.Phase != nil {
		phase = *a.Phase
	}
	return StepRuntimeState{
		Ref: StepRef{
			Phase: phase,
			Kind:  StepGeneralAction,
			Index: a.Step,
		},
		Status:      StepCompleted,
		Description: strings.TrimSpace(a.Tool),
		Command:     strings.TrimSpace(a.Command),
	}
}

func (l *Loop) PublishedRuntimeSnapshot() (RuntimeSnapshot, bool) {
	if l == nil {
		return RuntimeSnapshot{Control: RuntimeControlUnset, InputOwner: InputOwnerOrchestrator}, false
	}
	raw := l.runtimeSnapshot.Load()
	if raw == nil {
		return RuntimeSnapshot{}, false
	}
	snapshot, ok := raw.(RuntimeSnapshot)
	return snapshot, ok
}

func (l *Loop) publishRuntimeSnapshot() RuntimeSnapshot {
	snapshot := l.RuntimeSnapshot()
	l.runtimeSnapshot.Store(snapshot)
	return snapshot
}

func (s RuntimeSnapshot) DerivedInputOwner() InputOwner {
	switch s.Control {
	case RuntimeControlAwaitingApproval:
		return InputOwnerApproval
	case RuntimeControlAwaitingContinuationChoice:
		return InputOwnerReactChoice
	case RuntimeControlAwaitingContinuationText:
		return InputOwnerReactText
	default:
		return InputOwnerOrchestrator
	}
}

func (s RuntimeSnapshot) ShouldEmitAnchor() bool {
	return s.Requirement != nil ||
		s.Phase != nil ||
		s.PendingMutationVerification != nil ||
		s.Guide != nil ||
		s.PendingDirective != "" ||
		s.PendingFinalReport != nil ||
		s.Control == RuntimeControlAwaitingRequirementAnalysis ||
		s.Control == RuntimeControlAwaitingPhasePlan
}

func (s RuntimeSnapshot) ActiveGate() string {
	switch s.Control {
	case RuntimeControlAwaitingMutationVerificationResult:
		return "mutation_verification_result_required"
	case RuntimeControlAwaitingMutationVerificationEvidence:
		return "mutation_verification_evidence_required"
	case RuntimeControlAwaitingMutationContinuation:
		return "mutation_continuation_required"
	case RuntimeControlAwaitingGuidedPhaseProgress:
		return "guided_diagnosis_phase_progress_required"
	case RuntimeControlAwaitingFinalReport:
		return "final_report_required"
	case RuntimeControlAwaitingNextDirections:
		return "next_directions_required"
	case RuntimeControlAwaitingRequirementAnalysis:
		return "requirement_analysis_required"
	case RuntimeControlAwaitingPhasePlan:
		return "phase_plan_required"
	case RuntimeControlAwaitingResourceGuideLookup:
		return "resource_guide_lookup_required"
	default:
		return "none"
	}
}

func (s RuntimeSnapshot) RequiredNextOutput() string {
	switch s.Control {
	case RuntimeControlAwaitingMutationVerificationResult:
		return "mutation_verification_result"
	case RuntimeControlAwaitingMutationVerificationEvidence:
		return "one read-only action satisfying a remaining mutation evidence requirement"
	case RuntimeControlAwaitingMutationContinuation:
		return "next best action based on verification evidence"
	case RuntimeControlAwaitingGuidedPhaseProgress:
		return "phase_progress"
	case RuntimeControlAwaitingFinalReport:
		return "final_report"
	case RuntimeControlAwaitingNextDirections:
		return "next_directions"
	case RuntimeControlAwaitingRequirementAnalysis:
		return "requirement_analysis"
	case RuntimeControlAwaitingPhasePlan:
		return "phase_plan"
	case RuntimeControlAwaitingResourceGuideLookup:
		return "resource_guide_lookup"
	case RuntimeControlAwaitingGuidedDiagnosisStep:
		return "action for the next guide step, or guide_progress after useful evidence is already observed"
	default:
		if s.Phase != nil {
			return "action or phase_progress according to the current phase completion condition"
		}
		return "valid structured output for the current request"
	}
}

func (s RuntimeSnapshot) ForbiddenNextOutputs() []string {
	switch s.Control {
	case RuntimeControlAwaitingMutationVerificationResult:
		return []string{"action", "final_report", "phase_progress", "next_directions", "answer"}
	case RuntimeControlAwaitingMutationVerificationEvidence:
		return []string{"mutating action", "final_report", "phase_progress", "next_directions", "answer", "mutation_verification_result"}
	case RuntimeControlAwaitingMutationContinuation:
		return []string{"final_report", "phase_progress", "next_directions", "answer"}
	case RuntimeControlAwaitingGuidedPhaseProgress:
		return []string{"action", "final_report", "next_directions", "answer"}
	case RuntimeControlAwaitingFinalReport:
		return []string{"action", "phase_progress", "next_directions", "answer"}
	case RuntimeControlAwaitingNextDirections:
		return []string{"action", "phase_progress", "final_report", "answer"}
	case RuntimeControlAwaitingRequirementAnalysis:
		return []string{"action", "phase_plan", "phase_progress", "final_report", "next_directions", "answer"}
	case RuntimeControlAwaitingPhasePlan:
		return []string{"action", "phase_progress", "final_report", "next_directions", "answer"}
	case RuntimeControlAwaitingResourceGuideLookup:
		return []string{"action", "phase_progress", "guide_progress", "final_report", "next_directions", "answer"}
	default:
		return nil
	}
}

func (s RuntimeSnapshot) NestedStateName() string {
	if s.PendingMutationVerification != nil {
		if s.PendingMutationVerification.AwaitingResult {
			return "mutation_verification_result"
		}
		return "mutation_verification_evidence"
	}
	if s.Guide != nil {
		if len(s.Guide.remainingSteps()) > 0 {
			return "resource_guide_steps"
		}
		return "resource_guide_steps_complete"
	}
	return "none"
}

func (s RuntimeSnapshot) AuditError() string {
	switch {
	case s.Control == RuntimeControlUnset:
		return "runtime control state is unset"
	case s.Control == RuntimeControlAwaitingContinuationText && s.PendingDirectionPrompt != nil:
		return "direction free-text lifecycle still has a pending choice prompt"
	case s.Control == RuntimeControlAwaitingContinuationChoice && s.PendingDirectionPrompt == nil:
		return "direction choice lifecycle has no pending direction prompt"
	case s.Control == RuntimeControlAwaitingApproval && len(s.PendingCalls) == 0:
		return "approval lifecycle has no pending calls"
	case s.Control == RuntimeControlAwaitingRequirementAnalysis && s.Requirement != nil:
		return "requirement analysis control has already accepted a requirement"
	case s.Control == RuntimeControlAwaitingPhasePlan && (s.Requirement == nil || s.Phase != nil):
		return "phase plan control does not match requirement/phase payload"
	case s.Control == RuntimeControlAwaitingResourceGuideLookup && !s.RequiresResourceGuideLookupNow:
		return "resource guide lookup control has no eligible guidance lookup phase"
	case s.Control == RuntimeControlAwaitingGuidedDiagnosisStep && (s.Guide == nil || len(s.Guide.remainingSteps()) == 0):
		return "guided diagnosis control has no remaining guide step"
	case s.Control == RuntimeControlAwaitingMutationVerificationEvidence && (s.PendingMutationVerification == nil || s.PendingMutationVerification.AwaitingResult):
		return "mutation evidence control does not match verification payload"
	case s.Control == RuntimeControlAwaitingMutationVerificationResult && (s.PendingMutationVerification == nil || !s.PendingMutationVerification.AwaitingResult):
		return "mutation verification result control does not match verification payload"
	case s.Control == RuntimeControlAwaitingGuidedPhaseProgress && (s.PendingMutationVerification != nil || s.Phase == nil || !strings.EqualFold(strings.TrimSpace(s.Phase.currentStep().Name), "guided_diagnosis") || s.Guide == nil || !s.Guide.allCompleted()):
		return "guided phase progress control does not match a completed guided_diagnosis phase"
	case s.Control == RuntimeControlAwaitingFinalReport && s.PendingMutationVerification != nil:
		return "final report control cannot coexist with pending mutation verification"
	case s.Control == RuntimeControlAwaitingNextDirections && (s.PendingFinalReport == nil || s.PendingFinalReport.Conclusive):
		return "next directions control has no inconclusive final report"
	default:
		return ""
	}
}

func (l *Loop) runtimeStateAnchor() string {
	snapshot := l.RuntimeSnapshot()
	if !snapshot.ShouldEmitAnchor() {
		return ""
	}

	var b strings.Builder
	b.WriteString("Runtime lifecycle summary. Treat this as the concise decision contract for the next response; detailed anchors below remain authoritative for their domain.\n")
	fmt.Fprintf(&b, "loop_lifecycle: %s\n", reactStateName(snapshot.Lifecycle))
	fmt.Fprintf(&b, "control_state: %s\n", snapshot.Control)
	if snapshot.OriginalQuery != "" {
		fmt.Fprintf(&b, "original_query: %s\n", snapshot.OriginalQuery)
	}
	if snapshot.Requirement != nil {
		fmt.Fprintf(&b, "request_type: %s\n", snapshot.Requirement.RequestType)
		fmt.Fprintf(&b, "requested_action: %s\n", snapshot.Requirement.Action)
		if snapshot.Requirement.Target.Category != "" {
			fmt.Fprintf(&b, "target_category: %s\n", snapshot.Requirement.Target.Category)
		}
		if snapshot.Requirement.Target.Name != "" {
			fmt.Fprintf(&b, "target_name: %s\n", snapshot.Requirement.Target.Name)
		}
	}
	if snapshot.Request != nil {
		if snapshot.Request.PrimaryTarget.Resource != "" {
			fmt.Fprintf(&b, "primary_target_resource: %s\n", snapshot.Request.PrimaryTarget.Resource)
		}
		if snapshot.Request.PrimaryTarget.Name != "" {
			fmt.Fprintf(&b, "primary_target_name: %s\n", snapshot.Request.PrimaryTarget.Name)
		}
		if snapshot.Request.Scope.Namespace != "" {
			fmt.Fprintf(&b, "scope_namespace: %s\n", snapshot.Request.Scope.Namespace)
		}
		if snapshot.Request.ResourceClass != "" {
			fmt.Fprintf(&b, "resource_class: %s\n", snapshot.Request.ResourceClass)
		}
	}
	if snapshot.ResourceClassification != nil {
		fmt.Fprintf(&b, "resource_classification: %s\n", snapshot.ResourceClassification.Kind)
	}

	snapshot.writeRuntimePhaseSummary(&b)
	snapshot.writeRuntimeNestedStateSummary(&b)

	fmt.Fprintf(&b, "active_gate: %s\n", snapshot.ActiveGate())
	fmt.Fprintf(&b, "required_next_output: %s\n", snapshot.RequiredNextOutput())
	if forbidden := snapshot.ForbiddenNextOutputs(); len(forbidden) > 0 {
		fmt.Fprintf(&b, "forbidden_next_outputs: %s\n", strings.Join(forbidden, ","))
	}
	if snapshot.PendingDirective != "" {
		fmt.Fprintf(&b, "pending_runtime_directive: %s\n", compactSingleLine(snapshot.PendingDirective))
	}
	return b.String()
}

func (s RuntimeSnapshot) writeRuntimePhaseSummary(b *strings.Builder) {
	if s.Phase == nil {
		if s.Requirement != nil {
			b.WriteString("current_phase: phase_plan_required\n")
		}
		return
	}
	current := s.Phase.currentStep()
	if current.Index == 0 {
		b.WriteString("current_phase: unknown\n")
		return
	}
	fmt.Fprintf(b, "current_phase_index: %d\n", current.Index)
	fmt.Fprintf(b, "current_phase_name: %s\n", current.Name)
	if current.Goal != "" {
		fmt.Fprintf(b, "current_phase_goal: %s\n", current.Goal)
	}
	if current.CompletionCondition != "" {
		fmt.Fprintf(b, "current_phase_completion_condition: %s\n", current.CompletionCondition)
	}
	if len(current.Steps) > 0 {
		b.WriteString("current_phase_declared_steps:\n")
		for _, step := range current.Steps {
			ref := step.ID
			if ref == "" && step.Index > 0 {
				ref = fmt.Sprintf("%d", step.Index)
			}
			var details []string
			if step.Kind != "" {
				details = append(details, "kind="+step.Kind)
			}
			if step.Description != "" {
				details = append(details, "description="+step.Description)
			}
			if step.Command != "" {
				details = append(details, "command="+step.Command)
			}
			if step.ExpectedOutcome != "" {
				details = append(details, "expected_outcome="+step.ExpectedOutcome)
			}
			fmt.Fprintf(b, "- %s: %s\n", ref, strings.Join(details, "; "))
		}
	}
	if len(current.AllowedNext) > 0 {
		fmt.Fprintf(b, "allowed_next_phases: %s\n", strings.Join(current.AllowedNext, ","))
	}
	if completed := s.Phase.completedPhaseIndices(); len(completed) > 0 {
		fmt.Fprintf(b, "completed_phase_indices: %s\n", formatStepIndices(completed))
	}
}

func (s RuntimeSnapshot) writeRuntimeNestedStateSummary(b *strings.Builder) {
	fmt.Fprintf(b, "active_nested_state: %s\n", s.NestedStateName())
	if s.PendingMutationVerification != nil && !s.PendingMutationVerification.AwaitingResult {
		if remaining := s.PendingMutationVerification.remainingRequirements(); len(remaining) > 0 {
			var ids []string
			for _, req := range remaining {
				ids = append(ids, req.ID)
			}
			fmt.Fprintf(b, "remaining_mutation_evidence_ids: %s\n", strings.Join(ids, ","))
		}
		return
	}
	if s.Guide != nil {
		if skipped := s.Guide.skippedSteps(); len(skipped) > 0 {
			fmt.Fprintf(b, "skipped_guide_step_indices: %s\n", formatStepIndices(skipped))
		}
		if remaining := s.Guide.remainingSteps(); len(remaining) > 0 {
			fmt.Fprintf(b, "remaining_guide_step_indices: %s\n", formatStepIndices(remaining))
			fmt.Fprintf(b, "next_guide_step_index: %d\n", remaining[0])
		}
	}
}

func (l *Loop) phaseStepRequiresResourceGuideLookup() bool {
	if l.phaseStepState == nil || l.resourceGuideInjected {
		return false
	}
	current := l.phaseStepState.currentStep()
	return strings.EqualFold(strings.TrimSpace(current.Name), "guidance_lookup") &&
		l.resourceClassification != nil &&
		l.resourceClassification.Kind == resourceClassificationCRD
}

func (s *phaseStepState) completedPhaseIndices() []int {
	if s == nil {
		return nil
	}
	var out []int
	for _, step := range s.PhaseSteps {
		if s.Completed[step.Index] {
			out = append(out, step.Index)
		}
	}
	return out
}

func compactSingleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func reactStateName(lifecycle LoopLifecycleState) string {
	switch lifecycle {
	case LoopLifecycleAwaitingUserInput:
		return "AwaitingUserInput"
	case LoopLifecycleModelTurn:
		return "Running"
	case LoopLifecycleWaitingApproval:
		return "WaitingApproval"
	case LoopLifecycleWaitingContinuationChoice:
		return "WaitingDirectionChoice"
	case LoopLifecycleWaitingContinuationText:
		return "WaitingDirectionText"
	case LoopLifecycleExited:
		return "Exited"
	default:
		return fmt.Sprintf("LoopLifecycleState(%d)", lifecycle)
	}
}

type runtimeCleanupPolicy struct {
	ClearPendingCalls         bool
	ClearResponseDirectives   bool
	ClearDirectionState       bool
	ClearMutationContinuation bool
	ClearMutationVerification bool
}

func (l *Loop) applyRuntimeCleanup(policy runtimeCleanupPolicy) {
	if l == nil {
		return
	}
	if policy.ClearPendingCalls {
		l.pendingCalls = nil
	}
	if policy.ClearResponseDirectives {
		l.pendingResponseDirective = ""
	}
	if policy.ClearDirectionState {
		l.pendingFinalReport = nil
		l.pendingNextDirections = nil
		l.pendingDirectionPrompt = nil
	}
	if policy.ClearMutationContinuation {
		l.mutationContinuationAttempts = 0
	}
	if policy.ClearMutationVerification {
		l.pendingMutationVerification = nil
	}
}

func cleanupApprovalDeclinedPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearPendingCalls: true,
	}
}

func cleanupDirectionPromptPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearDirectionState:     true,
		ClearResponseDirectives: true,
	}
}

func cleanupExitPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearPendingCalls:         true,
		ClearDirectionState:       true,
		ClearResponseDirectives:   true,
		ClearMutationContinuation: true,
	}
}

type phaseScopedResetPolicy struct {
	ResetGuide                 bool
	ResetResourceGuideLookup   bool
	ResetMutationVerification  bool
	ClearResponseDirectives    bool
	ClearPendingFinalDirection bool
	TrimCompletedActions       bool
}

func (l *Loop) resetPhaseScopedState(from PhaseRef, policy phaseScopedResetPolicy) {
	if l == nil {
		return
	}
	if policy.ResetGuide {
		l.guideStepState = nil
	}
	if policy.ResetResourceGuideLookup {
		l.resourceGuideInjected = false
		l.resourceGuideQueries = nil
	}
	if policy.ResetMutationVerification {
		l.pendingMutationVerification = nil
		l.mutationContinuationAttempts = 0
	}
	if policy.ClearResponseDirectives {
		l.pendingResponseDirective = ""
	}
	if policy.ClearPendingFinalDirection {
		l.pendingFinalReport = nil
		l.pendingNextDirections = nil
		l.pendingDirectionPrompt = nil
	}
	if policy.TrimCompletedActions {
		l.trimCompletedActionsFromPhase(from)
	}
}

func (l *Loop) defaultPhaseScopedResetPolicy(from PhaseRef) phaseScopedResetPolicy {
	policy := phaseScopedResetPolicy{
		ClearResponseDirectives:    true,
		ClearPendingFinalDirection: true,
		TrimCompletedActions:       true,
	}
	if l == nil || l.phaseStepState == nil {
		return policy
	}
	for _, ref := range l.phaseStepState.phasesAtOrAfter(from) {
		name := strings.ToLower(strings.TrimSpace(ref.Name))
		switch {
		case strings.Contains(name, "guidance"), strings.Contains(name, "guided"):
			policy.ResetGuide = true
			policy.ResetResourceGuideLookup = true
		case strings.Contains(name, "mutation"), strings.Contains(name, "remediation"), strings.Contains(name, "verification"):
			policy.ResetMutationVerification = true
		}
	}
	return policy
}

func (l *Loop) trimCompletedActionsFromPhase(from PhaseRef) {
	if l == nil || len(l.completedActions) == 0 || l.phaseStepState == nil {
		return
	}
	var kept []actionRecord
	for _, action := range l.completedActions {
		if action.Phase == nil || action.Phase.Index == 0 {
			kept = append(kept, action)
			continue
		}
		if action.Phase.Index < from.Index {
			kept = append(kept, action)
		}
	}
	l.completedActions = kept
}

type contextError struct {
	Code      string
	Message   string
	Retryable bool
}

type guideRef struct {
	GuideID string `json:"guide_id"`
	Hash    string `json:"hash"`
	Content string `json:"content,omitempty"`
}

type actionRecord struct {
	Step       int            `json:"step"`
	Tool       string         `json:"tool"`
	Phase      *PhaseRef      `json:"phase,omitempty"`
	Command    string         `json:"command,omitempty"`
	Target     *actionTarget  `json:"target,omitempty"`
	ResultHash string         `json:"result_hash"`
	Result     map[string]any `json:"result,omitempty"`
	Clues      []string       `json:"clues,omitempty"`
}

func contextHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:8])
}

func (l *Loop) appendContextBlock(kind, content string, preserve bool) bool {
	if preserve {
		l.currChatContent = append(l.currChatContent, content)
		return true
	}
	if l.contextBlockHashes == nil {
		l.contextBlockHashes = make(map[string]struct{})
	}
	key := kind + ":" + contextHash(content)
	if _, ok := l.contextBlockHashes[key]; ok {
		return false
	}
	l.contextBlockHashes[key] = struct{}{}
	l.currChatContent = append(l.currChatContent, content)
	return true
}

func (l *Loop) appendCorrection(code, message string) bool {
	if l.lastContextError != nil && l.lastContextError.Code == code && l.lastContextError.Message == message {
		return false
	}
	l.lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	return l.appendContextBlock("correction:"+code, message, false)
}

func (l *Loop) appendCorrectionWithCompaction(code, message string) bool {
	if l.lastContextError != nil && l.lastContextError.Code == code && l.lastContextError.Message == message {
		return false
	}
	l.lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	if !l.shouldCompactForStateRewrite() {
		return l.appendContextBlock("correction:"+code, message, false)
	}
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: correction state %q triggered compaction; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", code, before, limit))
	if err := l.resetChatSession(); err != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+err.Error())
		return l.appendContextBlock("correction:"+code, message, false)
	}
	l.currChatContent = []any{l.compactedStateMessage("Return one corrected next response. Do not repeat the invalid response.")}
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: correction state %q preserved. estimated context %d/%d tokens.", code, after, limit))
	return true
}

func (l *Loop) compactedStateMessage(nextInstruction string) string {
	var b strings.Builder
	b.WriteString("Continue the same user request from compacted state.\n")
	l.writeConversationState(&b, true)
	if nextInstruction != "" {
		b.WriteString(nextInstruction)
	}
	return b.String()
}

func (l *Loop) priorConversationStateMessage() string {
	if !l.hasPriorConversationMemory() && !l.hasConversationState() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Previous conversation context for requirement analysis. Use it only when the new user request is a follow-up; explicit resource, name, namespace, or all-namespaces scope in the new request wins.\n")
	if l.hasPriorConversationMemory() {
		l.writePriorConversationMemory(&b)
	} else {
		l.writeConversationState(&b, false)
	}
	b.WriteString("Follow-up handling: if the new request is a follow-up without naming a new target/scope, default to the previous request_context target and scope and express the new diagnostic angle in requirement_analysis.operational_focus. Do not invent a new Kubernetes resource kind from follow-up wording alone.\n")
	b.WriteString("Do not repeat previous raw assistant JSON, guide bodies, corrections, or diagnostics unless the user asks for them.")
	return b.String()
}

func (l *Loop) hasPriorConversationMemory() bool {
	return l.lastOriginalQuery != "" ||
		l.lastRequirementAnalysis != nil ||
		l.lastRequestContext != nil ||
		strings.TrimSpace(l.lastDiagnosisSummary) != ""
}

func (l *Loop) writePriorConversationMemory(b *strings.Builder) {
	if l.lastOriginalQuery != "" {
		b.WriteString("previous_original_query: ")
		b.WriteString(compactPriorString(l.lastOriginalQuery, 1000))
		b.WriteString("\n")
	}
	if l.lastRequirementAnalysis != nil {
		if raw, err := json.Marshal(compactPriorRequirementAnalysis(l.lastRequirementAnalysis)); err == nil {
			b.WriteString("previous_requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.lastRequestContext != nil {
		if raw, err := json.Marshal(compactPriorRequestContext(l.lastRequestContext)); err == nil {
			b.WriteString("previous_request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastDiagnosisSummary) != "" {
		if raw, err := json.Marshal(l.lastDiagnosisSummary); err == nil {
			b.WriteString("previous_diagnosis_summary: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
}

func compactPriorRequirementAnalysis(analysis *requirementAnalysis) map[string]any {
	if analysis == nil {
		return nil
	}
	out := map[string]any{
		"request_type": analysis.RequestType,
		"action":       analysis.Action,
		"target": map[string]any{
			"category":    analysis.Target.Category,
			"name":        analysis.Target.Name,
			"description": compactPriorString(analysis.Target.Description, 500),
		},
		"scope": analysis.Scope,
	}
	if len(analysis.Resources) > 0 {
		limit := len(analysis.Resources)
		if limit > 3 {
			limit = 3
		}
		out["resource_candidates"] = append([]requirementResource(nil), analysis.Resources[:limit]...)
	}
	if analysis.OperationalFocus != nil {
		focus := map[string]any{
			"summary":                 compactPriorString(analysis.OperationalFocus.Summary, 500),
			"relationship_to_primary": analysis.OperationalFocus.RelationshipToPrimary,
			"changed_from_previous":   analysis.OperationalFocus.ChangedFromPrevious,
			"reason":                  compactPriorString(analysis.OperationalFocus.Reason, 500),
			"evidence_needs":          compactPriorStringSlice(analysis.OperationalFocus.EvidenceNeeds, 3, 300),
		}
		if len(analysis.OperationalFocus.RelatedResourceHints) > 0 {
			limit := len(analysis.OperationalFocus.RelatedResourceHints)
			if limit > 3 {
				limit = 3
			}
			hints := append([]requirementRelatedResource(nil), analysis.OperationalFocus.RelatedResourceHints[:limit]...)
			for i := range hints {
				hints[i].Evidence = compactPriorString(hints[i].Evidence, 300)
			}
			focus["related_resource_hints"] = hints
		}
		out["operational_focus"] = focus
	}
	if len(analysis.Evidence) > 0 {
		out["evidence_needs"] = compactPriorStringSlice(analysis.Evidence, 3, 300)
	}
	return out
}

func compactPriorRequestContext(ctx *requestContext) map[string]any {
	if ctx == nil {
		return nil
	}
	return map[string]any{
		"primary_target": ctx.PrimaryTarget,
		"scope":          ctx.Scope,
		"resource_class": ctx.ResourceClass,
	}
}

func compactPriorStringSlice(values []string, limit int, maxBytes int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) < limit {
		limit = len(values)
	}
	out := make([]string, 0, limit)
	for _, value := range values[:limit] {
		if text := compactPriorString(value, maxBytes); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func compactPriorString(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return safeStringHead(value, maxBytes) + " ...[truncated " + contextHash(value) + "]"
}

func (l *Loop) hasConversationState() bool {
	return l.originalQuery != "" ||
		l.requirementAnalysis != nil ||
		l.requestContext != nil ||
		l.resourceClassification != nil ||
		l.lastContextError != nil ||
		len(l.injectedGuides) > 0 ||
		len(l.completedActions) > 0 ||
		strings.TrimSpace(l.lastAssistantText) != ""
}

func (l *Loop) compactDiagnosisSummary() string {
	var b strings.Builder
	if len(l.completedActions) > 0 {
		if raw, err := json.Marshal(l.compactedActionSummaries()); err == nil {
			b.WriteString("completed_procedure_and_clues: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.lastAssistantText)); err == nil {
			b.WriteString("last_assistant_text: ")
			b.Write(raw)
		}
	}
	return strings.TrimSpace(b.String())
}

func cloneRequirementAnalysis(value *requirementAnalysis) *requirementAnalysis {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Resources = append([]requirementResource(nil), value.Resources...)
	if value.OperationalFocus != nil {
		focus := *value.OperationalFocus
		focus.RelatedResourceHints = append([]requirementRelatedResource(nil), value.OperationalFocus.RelatedResourceHints...)
		focus.EvidenceNeeds = append([]string(nil), value.OperationalFocus.EvidenceNeeds...)
		cloned.OperationalFocus = &focus
	}
	cloned.Evidence = append([]string(nil), value.Evidence...)
	cloned.Constraints = append([]string(nil), value.Constraints...)
	cloned.Ambiguities = append([]string(nil), value.Ambiguities...)
	return &cloned
}

func cloneRequestContext(value *requestContext) *requestContext {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (l *Loop) writeConversationState(b *strings.Builder, includeGuideContent bool) {
	if l.originalQuery != "" {
		b.WriteString("original_query: ")
		b.WriteString(l.originalQuery)
		b.WriteString("\n")
	}
	if l.requirementAnalysis != nil {
		if raw, err := json.Marshal(l.requirementAnalysis); err == nil {
			b.WriteString("requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.requestContext != nil {
		if raw, err := json.Marshal(l.requestContext); err == nil {
			b.WriteString("request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.resourceClassification != nil {
		if raw, err := json.Marshal(l.resourceClassification); err == nil {
			b.WriteString("resource_classification: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.lastContextError != nil {
		if raw, err := json.Marshal(l.lastContextError); err == nil {
			b.WriteString("last_error: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if len(l.injectedGuides) > 0 {
		keys := make([]string, 0, len(l.injectedGuides))
		for key := range l.injectedGuides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		refs := make([]guideRef, 0, len(keys))
		for _, key := range keys {
			ref := l.injectedGuides[key]
			if !includeGuideContent {
				ref.Content = ""
			}
			refs = append(refs, ref)
		}
		if raw, err := json.Marshal(refs); err == nil {
			b.WriteString("guide_contexts: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if len(l.completedActions) > 0 {
		if raw, err := json.Marshal(l.compactedActionSummaries()); err == nil {
			b.WriteString("completed_procedure_and_clues: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.lastAssistantText)); err == nil {
			b.WriteString("last_assistant_answer: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
}

func (l *Loop) compactedActionSummaries() []map[string]any {
	out := make([]map[string]any, 0, len(l.completedActions))
	for _, action := range l.completedActions {
		item := map[string]any{
			"step":        action.Step,
			"tool":        action.Tool,
			"result_hash": action.ResultHash,
		}
		if action.Command != "" {
			item["command"] = action.Command
		}
		if action.Target != nil {
			item["target"] = action.Target
		}
		if len(action.Clues) > 0 {
			item["clues"] = action.Clues
		}
		out = append(out, item)
	}
	return out
}

func (l *Loop) shouldCompactBeforeNextSend() bool {
	if l.actionSeq == l.lastCompactedActionSeq {
		return false
	}
	estimated := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	return estimated >= l.contextCompactThresholdTokens()
}

func (l *Loop) shouldCompactForStateRewrite() bool {
	return l.contextApproxTokens+estimateContextTokens(l.currChatContent...) >= l.contextCompactThresholdTokens()
}

func (l *Loop) compactBeforeNextIteration(nextInstruction string) {
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: estimated context %d/%d tokens (>=80%%). Preserving question, procedure order, clues, and next action.", before, limit))
	if err := l.resetChatSession(); err != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+err.Error())
		return
	}
	if l.pendingResponseDirective != "" {
		nextInstruction = "Continue from compacted state and follow the pending runtime directive below."
	}
	l.currChatContent = []any{l.compactedStateMessage(nextInstruction)}
	l.appendPendingResponseDirectiveAfterCompaction()
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: estimated context %d/%d tokens; %d completed diagnostic steps preserved.", after, limit, len(l.completedActions)))
}

func (l *Loop) compactAfterContextLengthError(err error) bool {
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.lastContextError = &contextError{
		Code:      "context_length_exceeded",
		Message:   err.Error(),
		Retryable: true,
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting after provider context-length error: estimated context %d/%d tokens. Retrying once with compacted procedure/clues.", before, limit))
	if resetErr := l.resetChatSession(); resetErr != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+resetErr.Error())
		return false
	}
	nextInstruction := "The previous LLM request exceeded the provider context limit. Continue from this compacted state. Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it."
	if l.pendingResponseDirective != "" {
		nextInstruction = "The previous LLM request exceeded the provider context limit. Continue from this compacted state and follow the pending runtime directive below."
	}
	l.currChatContent = []any{l.compactedStateMessage(nextInstruction)}
	l.appendPendingResponseDirectiveAfterCompaction()
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted after context-length error: estimated context %d/%d tokens; retrying now.", after, limit))
	return true
}

func (l *Loop) appendPendingResponseDirectiveAfterCompaction() {
	if strings.TrimSpace(l.pendingResponseDirective) == "" {
		return
	}
	l.currChatContent = append(l.currChatContent, "Pending runtime directive for the next model response:\n"+l.pendingResponseDirective)
}

func (l *Loop) appendGuideObservation(ref guideRef, content string) {
	if l.injectedGuides == nil {
		l.injectedGuides = make(map[string]guideRef)
	}
	key := ref.GuideID
	if key == "" {
		key = ref.Hash
	}
	if previous, ok := l.injectedGuides[key]; ok && previous.Hash == ref.Hash {
		l.appendContextBlock("guide-ref", fmt.Sprintf("Guide context already injected; use guide_ref %s (%s) without repeating the guide body.", key, ref.Hash), false)
		return
	}
	ref.Content = content
	l.injectedGuides[key] = ref
	l.appendContextBlock("guide", content, false)
}

func compactObservationResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	out := make(map[string]any, len(result))
	for key, value := range result {
		out[key] = compactObservationValue(value)
	}
	return out
}

func compactObservationValue(value any) any {
	switch v := value.(type) {
	case string:
		return compactObservationString(v)
	case map[string]any:
		return compactObservationResult(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, compactObservationValue(item))
		}
		return out
	default:
		return value
	}
}

func compactObservationString(value string) any {
	const maxObservationChars = 16000
	if len(value) <= maxObservationChars {
		return value
	}
	const headChars = 10000
	const tailChars = 4000
	return map[string]any{
		"content_head": safeStringHead(value, headChars),
		"content_tail": safeStringTail(value, tailChars),
		"content_hash": contextHash(value),
		"original_len": len(value),
		"truncated":    true,
	}
}

func compactStateText(value string) any {
	const maxStateChars = 8000
	if len(value) <= maxStateChars {
		return value
	}
	const headChars = 5000
	const tailChars = 2000
	return map[string]any{
		"content_head": safeStringHead(value, headChars),
		"content_tail": safeStringTail(value, tailChars),
		"content_hash": contextHash(value),
		"original_len": len(value),
		"truncated":    true,
	}
}

func safeStringHead(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	if end == 0 {
		return ""
	}
	return value[:end]
}

func safeStringTail(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	if start >= len(value) {
		return ""
	}
	return value[start:]
}

func extractObservationClues(result map[string]any) []string {
	if result == nil {
		return nil
	}
	var clues []string
	seen := map[string]struct{}{}
	for _, text := range observationStrings(result) {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !isClueLine(line) {
				continue
			}
			if len(line) > 300 {
				line = line[:300] + "..."
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			clues = append(clues, line)
			if len(clues) >= 16 {
				return clues
			}
		}
	}
	if len(clues) > 0 {
		return clues
	}
	hash := contextHash(fmt.Sprintf("%v", result))
	return []string{"no concise clue extracted; result_hash=" + hash}
}

func observationStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case map[string]any:
		var out []string
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, observationStrings(v[key])...)
		}
		return out
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, observationStrings(item)...)
		}
		return out
	default:
		return nil
	}
}

func isClueLine(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"condition", "status:", "phase:", "reason:", "message:", "ready", "available",
		"replicas", "providerid", "annotation", "annotations:", "label", "labels:",
		"paused", "failed", "error", "warning", "waiting", "notavailable", "false",
		"true", "unhealthy", "unknown", "deletiontimestamp", "finalizers:",
		"ownerreferences:", "name:", "namespace:", ".io/", ".com/", ".net/", "/",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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

type PendingCall struct {
	FunctionCall     gollm.FunctionCall
	ParsedToolCall   *tools.ToolCall
	IsInteractive    bool
	InteractiveError error
	ModifiesResource string
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

// directionPromptState maps rendered next-direction choices to their runtime
// continuation options.
type directionPromptState struct {
	Options      []nextDirectionOption
	HasFreeInput bool
	FreeInputIdx int
	FinalizeIdx  int
}

// SkipStep marks a stored guide or mutation evidence step as terminal without
// requiring successful evidence. General action steps cannot be skipped.
func (l *Loop) SkipStep(ref StepRef) bool {
	switch ref.Kind {
	case StepResourceGuideDiagnostic:
		return l.skipGuideRuntimeStep(ref)
	case StepMutationEvidenceRequirement:
		return l.skipMutationEvidenceRuntimeStep(ref)
	default:
		return false
	}
}

func (l *Loop) skipGuideRuntimeStep(ref StepRef) bool {
	if l == nil || l.guideStepState == nil || ref.Index <= 0 || ref.Index > l.guideStepState.TotalSteps {
		return false
	}
	if l.guideStepState.Skipped == nil {
		l.guideStepState.Skipped = map[int]bool{}
	}
	if l.guideStepState.Completed[ref.Index] || l.guideStepState.Skipped[ref.Index] {
		return false
	}
	l.guideStepState.Skipped[ref.Index] = true
	return true
}

func (l *Loop) skipMutationEvidenceRuntimeStep(ref StepRef) bool {
	if l == nil || l.pendingMutationVerification == nil || ref.ID == "" {
		return false
	}
	if !l.pendingMutationVerification.hasRequirement(ref.ID) {
		return false
	}
	if l.pendingMutationVerification.Skipped == nil {
		l.pendingMutationVerification.Skipped = map[string]bool{}
	}
	if l.pendingMutationVerification.Satisfied[ref.ID] || l.pendingMutationVerification.Skipped[ref.ID] {
		return false
	}
	l.pendingMutationVerification.Skipped[ref.ID] = true
	if l.pendingMutationVerification.allSatisfied() {
		l.pendingMutationVerification.AwaitingResult = true
	}
	return true
}

func (v pendingMutationVerification) hasRequirement(id string) bool {
	for _, req := range v.Requirements {
		if req.ID == id {
			return true
		}
	}
	return false
}
