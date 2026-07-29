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
	guidanceflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/request"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/language"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/prompt"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
	"k8s.io/klog/v2"
)

type Loop struct {
	runtimeState      *runtimeState
	stateStore        *session.Aggregate[runtimeState]
	activeTransaction *runtimeTransaction

	cfg      *config.Config
	deps     loopDependencies
	llm      gollm.Client
	chat     gollm.Chat
	lang     *language.Translator
	registry *toolconnector.Registry
	executor sandbox.Executor
	workDir  string

	input  chan any
	output chan *api.Message

	cancel context.CancelFunc
	once   sync.Once

	inputOwner      atomic.Int32
	runtimeSnapshot atomic.Value
}

func New(cfg *config.Config) (*Loop, error) {
	return newLoopWithDependencies(cfg, defaultLoopDependencies())
}

func newLoopWithDependencies(cfg *config.Config, dependencies loopDependencies) (*Loop, error) {
	dependencies = dependencies.withDefaults()
	klog.V(0).InfoS("react loop creating", "provider", cfg.LLMProvider, "model", cfg.Model, "shim", cfg.EnableToolUseShim, "read_only", cfg.ReadOnly, "mcp", cfg.MCPClient)
	llmClient, err := dependencies.modelClient(cfg)
	if err != nil {
		klog.ErrorS(err, "LLM client creation failed", "provider", cfg.LLMProvider)
		return nil, err
	}
	klog.V(0).InfoS("react loop created", "provider", cfg.LLMProvider)
	initial := newRuntimeState()
	stateStore := session.NewAggregate(initial)
	return &Loop{
		runtimeState: stateStore.Root(),
		stateStore:   stateStore,
		cfg:          cfg,
		deps:         dependencies,
		llm:          llmClient,
		input:        make(chan any, 1),
		output:       make(chan *api.Message, 32),
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
	l.deps = l.deps.withDefaults()
	l.executor = l.deps.executor()
	klog.V(1).InfoS("react work directory created", "path", workDir)

	registry, err := l.deps.toolRegistry(ctx, l.executor, l.cfg)
	if err != nil {
		return fmt.Errorf("tool registry 초기화 실패: %w", err)
	}
	l.registry = registry
	klog.V(0).InfoS("react registry initialized", "tools", len(registry.Tools.Names()))

	l.lang = language.New(l.cfg)
	klog.V(0).InfoS("language translator configured", "language", l.cfg.Lang.Language, "enabled", l.lang != nil && l.lang.Enabled())

	l.mutableRuntime().requestIntent = request.General
	l.mutableRuntime().toolProfile = selectToolProfile(registry.Tools, l.mutableRuntime().requestIntent, "")
	l.mutableRuntime().promptOptions = l.newPromptOptions(l.mutableRuntime().requestIntent, false, false)

	return l.resetChatSession()
}

func (l *Loop) resetChatSession() error {
	systemPrompt, toolProfile, chat, err := l.newChatSession()
	if err != nil {
		return err
	}
	l.mutableRuntime().systemPrompt = systemPrompt
	l.mutableRuntime().toolProfile = toolProfile
	l.mutableRuntime().contextApproxTokens = estimateContextTokens(systemPrompt)
	l.chat = chat
	klog.V(1).InfoS("chat session reset", "prompt_tokens_estimate", l.mutableRuntime().contextApproxTokens, "tool_profile", l.mutableRuntime().toolProfile.Name, "tools", len(l.mutableRuntime().toolProfile.ToolNames), "shim", l.cfg.EnableToolUseShim)
	klog.V(2).InfoS("chat session tool profile", "tool_profile", l.mutableRuntime().toolProfile.Name, "tools", l.mutableRuntime().toolProfile.ToolNames)
	return nil
}

func (l *Loop) newChatSession() (string, ToolProfile, gollm.Chat, error) {
	systemPrompt, err := buildSystemPromptWithOptions(l.cfg.PromptTemplateFile, l.registry.Tools, l.mutableRuntime().promptOptions)
	if err != nil {
		klog.ErrorS(err, "system prompt build failed", "template", l.cfg.PromptTemplateFile)
		return "", ToolProfile{}, nil, err
	}
	chat := gollm.NewRetryChat(
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
		defs := collectFunctionDefinitionsForProfile(l.registry.Tools, l.mutableRuntime().toolProfile, true)
		klog.V(1).InfoS("setting function definitions", "count", len(defs), "tool_profile", l.mutableRuntime().toolProfile.Name)
		if err := chat.SetFunctionDefinitions(defs); err != nil {
			klog.ErrorS(err, "function definition injection failed")
			return "", ToolProfile{}, nil, fmt.Errorf("tool function definition 주입 실패: %w", err)
		}
	}
	return systemPrompt, l.mutableRuntime().promptOptions.ToolProfile, chat, nil
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
		handled, auditErr := l.auditRuntimeState()
		if auditErr != nil {
			klog.ErrorS(auditErr, "runtime audit recovery failed; stopping with committed state intact")
			l.emitMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime 상태 복구에 실패해 미해결 작업을 보존한 채 실행을 중단합니다.\n"+auditErr.Error())
			return
		}
		if handled {
			continue
		}

		select {
		case <-ctx.Done():
			if err := l.mutateRuntimeAtomically(func() error {
				l.finalizePendingMutationVerification("runtime context was cancelled", reactcontract.AttemptUnknown)
				l.applyRuntimeCleanup(cleanupExitPolicy())
				l.transitionControl(RuntimeControlExited)
				return nil
			}); err != nil {
				klog.ErrorS(err, "failed to commit context cancellation cleanup")
			}
			return
		default:
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
				if recoveryErr := l.mutateRuntimeAtomically(func() error {
					if warning := l.recoverUnreconciledDispatches(err.Error()); warning != "" {
						l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
						if l.mutableRuntime().pendingMutationVerification != nil {
							return nil
						}
					}
					if warning := l.finalizePendingMutationVerification("model turn failed: "+err.Error(), reactcontract.AttemptUnknown); warning != "" {
						l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
					}
					l.mutableRuntime().pendingCalls = nil
					l.mutableRuntime().currChatContent = nil
					l.mutableRuntime().currIteration = 0
					l.transitionControl(RuntimeControlAwaitingUserQuery)
					l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
					return nil
				}); recoveryErr != nil {
					klog.ErrorS(recoveryErr, "failed to commit model-turn error recovery")
				}
			}
		case LoopLifecycleExited:
			return
		}
	}
}

func (l *Loop) startQuery(query string) error {
	return l.mutateRuntimeAtomically(func() error {
		return l.initializeQuery(query)
	})
}

func (l *Loop) initializeQuery(query string) error {
	intent := request.Classify(query)
	klog.V(0).InfoS("query starting", "query_len", len(query), "intent", intent)
	l.mutableRuntime().requestIntent = intent
	l.captureConversationMemory()
	priorState := l.priorConversationStateMessage()
	l.mutableRuntime().toolProfile = selectToolProfile(l.registry.Tools, intent, query)
	l.mutableRuntime().promptOptions = l.newPromptOptions(intent, false, false)
	klog.V(1).InfoS("query prompt options selected", "intent", intent, "tool_profile", l.mutableRuntime().toolProfile.Name, "tools", len(l.mutableRuntime().toolProfile.ToolNames), "read_only", l.cfg.ReadOnly, "translate_output", l.mutableRuntime().promptOptions.TranslateOutput)
	if err := l.resetChatSession(); err != nil {
		return err
	}
	l.addMessage(api.MessageSourceUser, api.MessageTypeText, query)
	l.mutableRuntime().currIteration = 0
	l.mutableRuntime().currChatContent = nil
	if priorState != "" {
		l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, priorState)
	}
	l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, prompt.RequirementAnalysis())
	l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, prompt.RequirementAnalysisDefinitions())
	l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, query)
	l.mutableRuntime().contextBlockHashes = nil
	l.mutableRuntime().pendingCalls = nil
	l.mutableRuntime().dispatchIntents = nil
	l.mutableRuntime().dispatchOrder = nil
	l.mutableRuntime().originalQuery = query
	l.mutableRuntime().requirementAnalysis = nil
	l.mutableRuntime().requestContext = nil
	l.mutableRuntime().phaseStepState = nil
	l.mutableRuntime().resourceClassification = nil
	l.mutableRuntime().lastContextError = nil
	l.mutableRuntime().injectedGuides = nil
	l.mutableRuntime().actionSeq = 0
	l.mutableRuntime().lastCompactedActionSeq = 0
	l.mutableRuntime().lastAssistantText = ""
	l.mutableRuntime().lastProgressText = ""
	l.mutableRuntime().resourceGuideInjected = false
	l.mutableRuntime().resourceGuideEvidence = nil
	l.mutableRuntime().resourceGuideQueries = nil
	l.mutableRuntime().guideStepState = nil
	l.mutableRuntime().pendingResponseDirective = ""
	l.mutableRuntime().pendingFinalReport = nil
	l.mutableRuntime().pendingNextDirections = nil
	l.mutableRuntime().pendingDirectionPrompt = nil
	l.mutableRuntime().pendingMutationVerification = nil
	l.mutableRuntime().mutationContinuationAttempts = 0
	l.mutableRuntime().finalReportMustBeInconclusive = false
	l.mutableRuntime().continuation = nil
	l.mutableRuntime().continuationResumePending = false
	l.beginGoalExecution()
	l.recordInternalObservation("user_input", query)
	l.transitionControl(RuntimeControlAwaitingRequirementAnalysis)
	klog.V(0).InfoS("query lifecycle initialized", "intent", intent, "lifecycle", logStateName(l.loopLifecycle()))
	return nil
}

func (l *Loop) captureConversationMemory() {
	if !l.hasConversationState() {
		return
	}
	l.mutableRuntime().lastOriginalQuery = l.mutableRuntime().originalQuery
	l.mutableRuntime().lastRequirementAnalysis = cloneRequirementAnalysis(l.mutableRuntime().requirementAnalysis)
	l.mutableRuntime().lastRequestContext = clonePointer(l.mutableRuntime().requestContext)
	l.mutableRuntime().lastDiagnosisSummary = l.compactDiagnosisSummary()
}

func (l *Loop) newPromptOptions(intent request.Intent, includeGuidance bool, includeClusterAPI bool) promptOptions {
	toolProfile := l.mutableRuntime().toolProfile
	if len(toolProfile.ToolNames) == 0 && l.registry != nil {
		toolProfile = selectToolProfile(l.registry.Tools, intent, l.mutableRuntime().originalQuery)
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
		if err := l.mutateRuntimeAtomically(func() error {
			l.finalizePendingMutationVerification("user reset the active request", reactcontract.AttemptUnknown)
			l.mutableRuntime().requestIntent = request.General
			l.mutableRuntime().toolProfile = selectToolProfile(l.registry.Tools, l.mutableRuntime().requestIntent, "")
			l.mutableRuntime().promptOptions = l.newPromptOptions(l.mutableRuntime().requestIntent, false, false)
			if err := l.resetChatSession(); err != nil {
				return err
			}
			l.clearConversationState()
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "대화 컨텍스트를 초기화했습니다.")
			return nil
		}); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
		return true
	case "exit", "quit":
		klog.V(0).InfoS("react meta query handled", "query", query)
		if err := l.mutateRuntimeAtomically(func() error {
			if warning := l.finalizePendingMutationVerification("user exited the session", reactcontract.AttemptUnknown); warning != "" {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
			}
			l.applyRuntimeCleanup(cleanupExitPolicy())
			l.transitionControl(RuntimeControlExited)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
			return nil
		}); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
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
	l.mutableRuntime().currIteration = 0
	l.mutableRuntime().currChatContent = nil
	l.mutableRuntime().contextBlockHashes = nil
	l.mutableRuntime().pendingCalls = nil
	l.mutableRuntime().dispatchIntents = nil
	l.mutableRuntime().dispatchOrder = nil
	l.mutableRuntime().execution = nil
	l.mutableRuntime().originalQuery = ""
	l.mutableRuntime().requirementAnalysis = nil
	l.mutableRuntime().requestContext = nil
	l.mutableRuntime().phaseStepState = nil
	l.mutableRuntime().resourceClassification = nil
	l.mutableRuntime().lastOriginalQuery = ""
	l.mutableRuntime().lastRequirementAnalysis = nil
	l.mutableRuntime().lastRequestContext = nil
	l.mutableRuntime().lastDiagnosisSummary = ""
	l.mutableRuntime().lastContextError = nil
	l.mutableRuntime().injectedGuides = nil
	l.mutableRuntime().actionSeq = 0
	l.mutableRuntime().lastCompactedActionSeq = 0
	l.mutableRuntime().contextApproxTokens = estimateContextTokens(l.mutableRuntime().systemPrompt)
	l.mutableRuntime().lastAssistantText = ""
	l.mutableRuntime().lastProgressText = ""
	l.mutableRuntime().resourceGuideInjected = false
	l.mutableRuntime().resourceGuideEvidence = nil
	l.mutableRuntime().resourceGuideQueries = nil
	l.mutableRuntime().guideStepState = nil
	l.mutableRuntime().pendingResponseDirective = ""
	l.mutableRuntime().pendingFinalReport = nil
	l.mutableRuntime().pendingNextDirections = nil
	l.mutableRuntime().pendingDirectionPrompt = nil
	l.mutableRuntime().pendingMutationVerification = nil
	l.mutableRuntime().mutationContinuationAttempts = 0
	l.mutableRuntime().finalReportMustBeInconclusive = false
	l.mutableRuntime().continuation = nil
	l.mutableRuntime().continuationResumePending = false
}

func (l *Loop) executeIteration(ctx context.Context) (returnErr error) {
	var tx *runtimeTransaction
	defer func() {
		if tx == nil {
			return
		}
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		effects := append([]reactcontract.Effect(nil), tx.effects...)
		returnErr = l.commitRuntimeTransaction(tx)
		if returnErr == nil && len(effects) > 0 {
			returnErr = l.executeTurnEffects(ctx, effects)
		}
	}()

	snapshot := l.RuntimeSnapshot()
	if !reactcontract.IsModelTurnControl(snapshot.Control) {
		return fmt.Errorf("model turn is not allowed from runtime control state %q", snapshot.Control)
	}
	klog.V(1).InfoS("react iteration starting",
		"iteration", l.mutableRuntime().currIteration+1,
		"max_iterations", l.maxIterationLimit(),
		"lifecycle", logStateName(l.loopLifecycle()),
		"control", snapshot.Control,
		"context_tokens_estimate", l.mutableRuntime().contextApproxTokens,
		"chat_content_items", len(l.mutableRuntime().currChatContent),
	)
	if l.mutableRuntime().currIteration >= l.maxIterationLimit() {
		klog.V(0).InfoS("react max iterations reached", "max_iterations", l.maxIterationLimit())
		return l.mutateRuntimeAtomically(func() error {
			if warning := l.finalizePendingMutationVerification("model iteration limit was reached", reactcontract.AttemptUnknown); warning != "" {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
			}
			l.mutableRuntime().pendingCalls = nil
			execution := l.mutableRuntime().execution
			if execution == nil || strings.TrimSpace(execution.RequestID) == "" || strings.TrimSpace(execution.Goal.ID) == "" {
				l.transitionControl(RuntimeControlAwaitingUserQuery)
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "실행 계획이 확정되기 전에 model iteration 한도에 도달했습니다. 요청을 더 구체화해 다시 입력해 주세요.")
				return nil
			}
			l.acceptContinuationHandoff(ctx, l.deterministicContinuationHandoff("The execution segment reached its model iteration limit before the request was conclusive."))
			return nil
		})
	}

	if l.closureThresholdReached() && l.closureCanInterruptCurrentControl() {
		tx, returnErr = l.beginRuntimeTransaction(snapshot.Revision)
		if returnErr != nil {
			return returnErr
		}
		l.requestContinuationHandoff()
		return nil
	}

	if l.shouldCompactBeforeNextSend() {
		klog.V(1).InfoS("context compaction triggered before send", "context_tokens_estimate", l.mutableRuntime().contextApproxTokens, "limit", l.contextLimitTokens())
		tx, returnErr = l.beginRuntimeTransaction(snapshot.Revision)
		if returnErr != nil {
			return returnErr
		}
		returnErr = l.prepareContextCompaction(
			"pre_send",
			"token_threshold",
			"Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.",
		)
		return returnErr
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
		if l.mutableRuntime().contextLengthRetryUsed {
			return err
		}
		tx, returnErr = l.beginRuntimeTransaction(snapshot.Revision)
		if returnErr != nil {
			return returnErr
		}
		l.mutableRuntime().contextLengthRetryUsed = true
		returnErr = l.prepareContextCompaction(
			"context_length",
			"context_length_exceeded",
			"The previous LLM request exceeded the provider context limit. Continue from this compacted state and follow the pending runtime directive. Do not repeat completed commands unless new evidence requires it.",
		)
		return returnErr
	}
	klog.V(1).InfoS("model response received", "duration", time.Since(sendStart), "text_len", len(streamedText), "function_calls", len(functionCalls), "call_names", logFunctionCallNames(functionCalls))
	klog.V(2).InfoS("model response call summaries", "calls", logFunctionCallSummaries(functionCalls))
	tx, returnErr = l.beginRuntimeTransaction(snapshot.Revision)
	if returnErr != nil {
		return returnErr
	}
	l.noteContextContent(sentContent...)
	l.mutableRuntime().currChatContent = nil
	l.mutableRuntime().contextLengthRetryUsed = false
	l.mutableRuntime().pendingResponseDirective = ""

	if len(functionCalls) == 0 {
		if strings.TrimSpace(streamedText) != "" {
			if parsed, err := parseReActResponse(streamedText); err == nil {
				functionCalls = functionCallsFromParsedReActResponse(parsed)
			}
		}
	}
	functionCalls = protocol.NormalizeAssistantStructuredFunctionCalls(functionCalls)
	klog.V(1).InfoS("normalized model function calls", "function_calls", len(functionCalls), "call_names", logFunctionCallNames(functionCalls))
	envelope := normalizeModelOutputEnvelope(streamedText, functionCalls)
	if handled := l.validateModelOutputEnvelope(&envelope, functionCalls); handled {
		return nil
	}

	if len(functionCalls) == 0 {
		klog.V(1).InfoS("model response had no function calls", "text_len", len(streamedText))
		if strings.TrimSpace(streamedText) != "" {
			rawModelText := streamedText
			l.mutableRuntime().contextApproxTokens += estimateContextTokens(rawModelText)
			l.addTranslatedModelMessage(ctx, rawModelText)
			l.mutableRuntime().lastAssistantText = rawModelText
			l.recordPlainAnswerExecution(rawModelText)
			klog.V(0).InfoS("plain model answer accepted", "raw_len", len(rawModelText))
		}
		l.mutableRuntime().currIteration = 0
		l.mutableRuntime().pendingCalls = nil
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		return nil
	}
	deferredProgressText := strings.TrimSpace(streamedText)

	var requestContextHandled bool
	functionCalls, requestContextHandled = l.consumeRequestContext(ctx, functionCalls)
	if requestContextHandled {
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

	var continuationHandoffHandled bool
	functionCalls, continuationHandoffHandled = l.consumeContinuationHandoff(ctx, functionCalls)
	if continuationHandoffHandled {
		return nil
	}

	var mutationVerificationResultHandled bool
	functionCalls, mutationVerificationResultHandled = l.consumeMutationVerificationResult(functionCalls)
	if mutationVerificationResultHandled {
		return nil
	}

	var stepResultHandled bool
	functionCalls, stepResultHandled = l.consumeStepResult(functionCalls)
	if stepResultHandled {
		return nil
	}

	var phasePlanRevisionHandled bool
	functionCalls, phasePlanRevisionHandled = l.consumePhasePlanRevision(functionCalls)
	if phasePlanRevisionHandled {
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

	if handled := l.handleRequestedResourceGuideLookup(functionCalls); handled {
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
		l.mutableRuntime().currIteration++
		return nil
	}

	if err := l.validateCommandRiskMetadata(functionCalls); err != nil {
		l.applyModelOutputCorrectionGate(
			"command_risk_metadata_required",
			"command risk 계약 오류가 반복되어 요청을 중단했습니다.",
			"The proposed command action has invalid risk metadata: "+err.Error()+". Return one corrected action with an explicit risk.risky boolean and risk.reason when risky=true.",
		)
		return nil
	}
	pending, err := l.analyzeToolCalls(ctx, functionCalls)
	if err != nil {
		klog.ErrorS(err, "tool call analysis failed", "call_names", logFunctionCallNames(functionCalls))
		l.applyModelOutputCorrectionGate(
			"tool_call_parse_error",
			"tool call 형식 오류가 반복되어 요청을 중단했습니다.",
			"The proposed action could not be parsed by the registered tool: "+err.Error()+" Return one corrected action using the registered tool schema; keep the accepted plan and active step unchanged.",
		)
		return nil
	}
	l.mutableRuntime().pendingCalls = pending
	klog.V(1).InfoS("tool calls analyzed", "pending", len(pending), "summaries", logPendingCallSummaries(pending))
	if l.cfg.ReadOnly && l.hasModifyingCalls() {
		clearPendingVerificationInput(pending)
		klog.V(0).InfoS("read-only mode blocking modifying tool calls", "pending", len(l.mutableRuntime().pendingCalls))
		klog.V(1).InfoS("read-only blocked call summaries", "pending", logPendingCallSummaries(l.mutableRuntime().pendingCalls))
		l.rejectReadOnlyModifyingCalls()
		return nil
	}
	if err := normalizePendingVerification(pending); err != nil {
		l.mutableRuntime().pendingCalls = nil
		l.applyModelOutputCorrectionGate(
			"mutation_verification_spec_required",
			"mutation verification 계약 형식이 반복적으로 잘못되어 요청을 중단했습니다.",
			"The proposed verification metadata was invalid: "+err.Error()+". Return one corrected mutating action with a single or ordered-chain verification contract.",
		)
		return nil
	}
	modifyingCalls := 0
	for _, call := range pending {
		if strings.EqualFold(strings.TrimSpace(call.ModifiesResource), "yes") ||
			strings.EqualFold(strings.TrimSpace(call.ModifiesResource), "unknown") {
			modifyingCalls++
			if err := validateMutationVerificationSpec(call); err != nil {
				l.mutableRuntime().pendingCalls = nil
				l.applyModelOutputCorrectionGate(
					"mutation_verification_spec_required",
					"mutation verification 계약이 누락되어 요청을 중단했습니다.",
					"The mutating action was rejected before execution: "+err.Error()+". Return one corrected mutating action with a single or ordered-chain verification contract.",
				)
				return nil
			}
		}
		if l.verificationEvidenceBudgetExhausted(call) {
			l.blockMutationEvidenceBudget(call)
			return nil
		}
	}
	if modifyingCalls > 1 {
		l.mutableRuntime().pendingCalls = nil
		l.applyModelOutputCorrectionGate(
			"multiple_mutations_per_attempt",
			"한 attempt에 여러 mutation을 제안하여 요청을 중단했습니다.",
			"Return exactly one mutating primary action for the active step. Its direct verification belongs to the same attempt; do not batch independent mutations.",
		)
		return nil
	}
	if allowed, reason := l.pendingAttemptBatchAllowed(pending); !allowed {
		l.mutableRuntime().pendingCalls = nil
		l.applyModelOutputCorrectionGate(
			"action_attempt_contract",
			"action이 active step의 attempt contract와 반복적으로 충돌하여 요청을 중단했습니다.",
			"The proposed action was rejected before execution: "+reason+". Choose a materially different strategy within the active step budget, or close the step with step_result.",
		)
		return nil
	}

	if handled := l.rejectInteractiveToolCalls(); handled {
		klog.V(0).InfoS("interactive tool calls rejected")
		return nil
	}

	if l.pendingCallsRequireApproval() {
		klog.V(0).InfoS("approval required for tool calls", "pending", len(l.mutableRuntime().pendingCalls))
		klog.V(1).InfoS("approval required call summaries", "pending", logPendingCallSummaries(l.mutableRuntime().pendingCalls))
		l.emitAcceptedProgressText(ctx, deferredProgressText)
		l.requestApproval()
		return nil
	}

	l.emitAcceptedProgressText(ctx, deferredProgressText)
	klog.V(1).InfoS("dispatching tool calls without approval", "pending", len(l.mutableRuntime().pendingCalls))
	if err := l.preparePendingToolDispatch(false); err != nil {
		return err
	}
	return nil
}

func (l *Loop) auditRuntimeState() (bool, error) {
	snapshot := l.RuntimeSnapshot()
	if len(snapshot.PendingDispatches) > 0 {
		if err := l.mutateRuntimeAtomically(func() error {
			if warning := l.recoverUnreconciledDispatches("runtime resumed with an unreconciled committed dispatch"); warning != "" {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
			}
			return nil
		}); err != nil {
			return false, fmt.Errorf("recover committed tool dispatch: %w", err)
		}
		return true, nil
	}
	if message := snapshot.AuditError(); message != "" {
		err := l.mutateRuntimeAtomically(func() error {
			if warning := l.finalizePendingMutationVerification("runtime audit failed: "+message, reactcontract.AttemptUnknown); warning != "" {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
			}
			l.mutableRuntime().pendingCalls = nil
			l.mutableRuntime().currIteration = 0
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.refreshInputOwner()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime lifecycle invariant violation: "+message)
			return nil
		})
		if err != nil {
			return false, fmt.Errorf("commit runtime audit recovery for %q: %w", message, err)
		}
		return true, nil
	}
	return false, nil
}

// buildIterationSendContent assembles the message list that will be sent to
// the LLM for the current iteration. It prepends compact anchors so the model
// keeps the active request, phase, nested guide/mutation state, and required
// next output in active attention across many iterations of tool observations.
func (l *Loop) buildIterationSendContent() []any {
	sentContent := append([]any(nil), l.mutableRuntime().currChatContent...)
	if anchor := l.mutationVerificationAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.guideStepAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.phaseStepAnchor(); anchor != "" {
		sentContent = append([]any{anchor}, sentContent...)
	}
	if anchor := l.executionAnchor(); anchor != "" {
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
			if shimErr, ok := err.(*shimOutputError); ok {
				return shimErr.Raw, []gollm.FunctionCall{{Name: protocol.InvalidStructuredOutputCall}}, nil
			}
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
	if rawText == strings.TrimSpace(l.mutableRuntime().lastProgressText) {
		return
	}
	l.addTranslatedModelMessage(ctx, rawText)
	l.mutableRuntime().lastProgressText = rawText
}

func (l *Loop) emitAcceptedProgressText(ctx context.Context, progressText string) {
	progressText = strings.TrimSpace(progressText)
	if progressText == "" {
		return
	}
	l.mutableRuntime().contextApproxTokens += estimateContextTokens(progressText)
	l.mutableRuntime().lastAssistantText = progressText
	l.emitProgressText(ctx, progressText)
}

func (l *Loop) rejectConversationalToolCalls(calls []gollm.FunctionCall) bool {
	if len(calls) == 0 || l.mutableRuntime().requirementAnalysis == nil {
		return false
	}
	if !l.requirementAnalysisNeedsDirectConversation() {
		return false
	}
	for _, call := range calls {
		if protocol.IsRuntimeInternalCall(call.Name) {
			continue
		}
		message := "The accepted requirement_analysis is a conversation/clarification request. Do not call shell, bash, kubectl, echo, or any other tool to ask the user a question. Return a plain assistant answer/question directly, or complete the clarification phase with phase_progress when the user's intent is already clear."
		return l.applyModelOutputCorrectionGate("conversation_tool_call", "대화/확인 요청에서 tool call이 반복되어 진단을 중단합니다.", message)
	}
	return false
}

func (l *Loop) rejectInteractiveToolCalls() bool {
	var descriptions []string
	for _, call := range l.mutableRuntime().pendingCalls {
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

func (l *Loop) requirementAnalysisNeedsDirectConversation() bool {
	analysis := l.mutableRuntime().requirementAnalysis
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
			l.appendFunctionCallResult(call, result)
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
	RuntimeControlUnset                                     = reactcontract.RuntimeControlUnset
	RuntimeControlAwaitingUserQuery                         = reactcontract.RuntimeControlAwaitingUserQuery
	RuntimeControlAwaitingRequirementAnalysis               = reactcontract.RuntimeControlAwaitingRequirementAnalysis
	RuntimeControlAwaitingPhasePlan                         = reactcontract.RuntimeControlAwaitingPhasePlan
	RuntimeControlAwaitingModelStep                         = reactcontract.RuntimeControlAwaitingModelStep
	RuntimeControlAwaitingResourceGuideLookup               = reactcontract.RuntimeControlAwaitingResourceGuideLookup
	RuntimeControlAwaitingGuidedDiagnosisStep               = reactcontract.RuntimeControlAwaitingGuidedDiagnosisStep
	RuntimeControlAwaitingGuidedPhaseProgress               = reactcontract.RuntimeControlAwaitingGuidedPhaseProgress
	RuntimeControlAwaitingFinalReport                       = reactcontract.RuntimeControlAwaitingFinalReport
	RuntimeControlAwaitingNextDirections                    = reactcontract.RuntimeControlAwaitingNextDirections
	RuntimeControlAwaitingApproval                          = reactcontract.RuntimeControlAwaitingApproval
	RuntimeControlAwaitingToolResult                        = reactcontract.RuntimeControlAwaitingToolResult
	RuntimeControlAwaitingMutationVerificationEvidence      = reactcontract.RuntimeControlAwaitingMutationVerificationEvidence
	RuntimeControlAwaitingMutationVerificationResult        = reactcontract.RuntimeControlAwaitingMutationVerificationResult
	RuntimeControlAwaitingMutationVerificationChainEvidence = reactcontract.RuntimeControlAwaitingMutationVerificationChainEvidence
	RuntimeControlAwaitingMutationVerificationChainResult   = reactcontract.RuntimeControlAwaitingMutationVerificationChainResult
	RuntimeControlAwaitingMutationContinuation              = reactcontract.RuntimeControlAwaitingMutationContinuation
	RuntimeControlAwaitingContinuationHandoff               = reactcontract.RuntimeControlAwaitingContinuationHandoff
	RuntimeControlAwaitingContinuationChoice                = reactcontract.RuntimeControlAwaitingContinuationChoice
	RuntimeControlAwaitingContinuationText                  = reactcontract.RuntimeControlAwaitingContinuationText
	RuntimeControlExited                                    = reactcontract.RuntimeControlExited
)

type PhaseStatus = reactcontract.PhaseStatus

const (
	PhasePending    = reactcontract.PhasePending
	PhaseActive     = reactcontract.PhaseActive
	PhaseCompleted  = reactcontract.PhaseCompleted
	PhaseSkipped    = reactcontract.PhaseSkipped
	PhaseSuperseded = reactcontract.PhaseSuperseded
)

type StepKind = reactcontract.StepKind

const (
	StepGeneralAction               = reactcontract.StepGeneralAction
	StepLightweightLookup           = reactcontract.StepLightweightLookup
	StepExplicitPhase               = reactcontract.StepExplicitPhase
	StepResourceGuideDiagnostic     = reactcontract.StepResourceGuideDiagnostic
	StepMutationEvidenceRequirement = reactcontract.StepMutationEvidenceRequirement
)

type StepStatus = reactcontract.StepStatus

const (
	StepPending    = reactcontract.StepPending
	StepActive     = reactcontract.StepActive
	StepCompleted  = reactcontract.StepCompleted
	StepAchieved   = reactcontract.StepAchieved
	StepBlocked    = reactcontract.StepBlocked
	StepSkipped    = reactcontract.StepSkipped
	StepRetrying   = reactcontract.StepRetrying
	StepSuperseded = reactcontract.StepSuperseded
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
	if l.activeTransaction != nil {
		l.activeTransaction.refreshInputOwner = true
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
	if l.activeTransaction == nil {
		l.mutableRuntime()
		expected, candidate := l.stateStore.Candidate(cloneRuntimeState)
		if candidate == nil {
			klog.ErrorS(fmt.Errorf("runtime control transition candidate is nil"), "runtime control transition rejected", "next", next)
			return
		}
		candidate.control = next
		if _, err := l.stateStore.Commit(expected, candidate, l.auditRuntimeCandidate); err != nil {
			klog.ErrorS(err, "runtime control transition rejected", "next", next)
			l.runtimeState = l.stateStore.Root()
			return
		}
		l.runtimeState = l.stateStore.Root()
		return
	}
	l.mutableRuntime().control = next
}

func (l *Loop) controlState() RuntimeControlState {
	if l == nil {
		return RuntimeControlUnset
	}
	return l.mutableRuntime().control
}

func (l *Loop) loopLifecycle() LoopLifecycleState {
	if l == nil {
		return LoopLifecycleAwaitingUserInput
	}
	return session.LifecycleFor(l.controlState())
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

// RuntimeSnapshot is a detached projection of the revisioned session root.
// Maps, slices, and workflow pointers are copied before publication.
type RuntimeSnapshot struct {
	Revision   uint64
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
	PendingDispatches              []reactcontract.ToolDispatchIntent
	PendingMutationVerification    *pendingMutationVerification
	MutationContinuationAttempts   int
	FinalReportMustBeInconclusive  bool
	PendingFinalReport             *finalReport
	PendingNextDirections          *nextDirections
	PendingDirectionPrompt         *directionPromptState
	PendingDirective               string
	Continuation                   *reactcontract.ContinuationState
	ContinuationResumePending      bool
	ResourceGuideInjected          bool
	RequiresResourceGuideLookupNow bool
	Execution                      *session.GoalExecutionState
}

func (l *Loop) RuntimeSnapshot() RuntimeSnapshot {
	if l == nil {
		return RuntimeSnapshot{Control: RuntimeControlUnset}
	}
	revision := uint64(0)
	if l.stateStore != nil {
		revision = l.stateStore.Revision()
	}
	return projectRuntimeSnapshot(l.mutableRuntime(), revision)
}

func projectRuntimeSnapshot(source *runtimeState, revision uint64) RuntimeSnapshot {
	if source == nil {
		return RuntimeSnapshot{Control: RuntimeControlUnset}
	}
	state := cloneRuntimeState(*source)
	phaseRuntime := state.phaseStepState.runtimeState()
	enrichPhaseRuntimeContracts(phaseRuntime, state.execution)
	snapshot := RuntimeSnapshot{
		Revision:                     revision,
		Lifecycle:                    session.LifecycleFor(state.control),
		Control:                      state.control,
		OriginalQuery:                strings.TrimSpace(state.originalQuery),
		Requirement:                  state.requirementAnalysis,
		Request:                      state.requestContext,
		ResourceClassification:       state.resourceClassification,
		Phase:                        state.phaseStepState,
		Guide:                        state.guideStepState,
		PhaseRuntime:                 phaseRuntime,
		PendingCalls:                 clonePendingCalls(state.pendingCalls),
		PendingDispatches:            pendingDispatchIntents(&state),
		PendingMutationVerification:  state.pendingMutationVerification,
		MutationContinuationAttempts: state.mutationContinuationAttempts,
		FinalReportMustBeInconclusive: state.finalReportMustBeInconclusive ||
			(state.execution != nil && len(state.execution.BlockedObligations) > 0),
		PendingFinalReport:             state.pendingFinalReport,
		PendingNextDirections:          state.pendingNextDirections,
		PendingDirectionPrompt:         state.pendingDirectionPrompt,
		PendingDirective:               strings.TrimSpace(state.pendingResponseDirective),
		Continuation:                   cloneContinuationState(state.continuation),
		ContinuationResumePending:      state.continuationResumePending,
		ResourceGuideInjected:          state.resourceGuideInjected,
		RequiresResourceGuideLookupNow: projectRequiresResourceGuideLookup(&state),
		Execution:                      session.CloneGoalExecutionState(state.execution),
	}
	snapshot.ActiveSteps = projectActiveStepRuntimeStates(&state, snapshot.PhaseRuntime)
	snapshot.InputOwner = snapshot.DerivedInputOwner()
	return snapshot
}

func pendingDispatchIntents(state *runtimeState) []reactcontract.ToolDispatchIntent {
	if state == nil {
		return nil
	}
	var intents []reactcontract.ToolDispatchIntent
	for _, id := range state.dispatchOrder {
		intent, ok := state.dispatchIntents[id]
		if !ok || intent.Status != reactcontract.ToolDispatchPending {
			continue
		}
		intents = append(intents, cloneDispatchIntent(intent))
	}
	return intents
}

func projectActiveStepRuntimeStates(state *runtimeState, phase *PhaseRuntimeState) []StepRuntimeState {
	if state == nil {
		return nil
	}
	activePhase := PhaseRef{}
	if phase != nil {
		activePhase = phase.Active
	}
	if verification := state.pendingMutationVerification; verification != nil &&
		state.control != RuntimeControlAwaitingMutationContinuation {
		return verification.stepRuntimeStates(activePhase)
	}
	var steps []StepRuntimeState
	hasRuntimeProjection := false
	if guide := state.guideStepState; guide != nil {
		steps = append(steps, guide.stepRuntimeStates(activePhase)...)
		hasRuntimeProjection = true
	}
	if hasRuntimeProjection {
		return steps
	}
	if execution := state.execution; execution != nil && len(execution.Phases) > 0 {
		for _, phaseContract := range execution.Phases {
			for _, step := range phaseContract.Steps {
				status := execution.StepStatus[step.ID]
				if status == "" {
					status = StepPending
				}
				steps = append(steps, StepRuntimeState{
					Ref: StepRef{
						Phase: PhaseRef{
							ID:        phaseContract.ID,
							LineageID: phaseContract.PhaseLineageID,
							Index:     phaseContract.Index,
							Name:      phaseContract.Name,
						},
						Kind:          step.Kind,
						ID:            step.ID,
						GoalLineageID: step.GoalLineageID,
						Index:         step.Index,
					},
					Status:      status,
					Description: step.Goal,
				})
			}
		}
		return steps
	}
	for i, call := range state.pendingCalls {
		if strings.HasPrefix(strings.TrimSpace(call.FunctionCall.Name), "__") {
			continue
		}
		steps = append(steps, pendingCallStepRuntimeState(activePhase, i+1, call))
	}
	return steps
}

func pendingCallStepRuntimeState(phase PhaseRef, index int, call PendingCall) StepRuntimeState {
	command, _ := rawCommandString(call.FunctionCall.Arguments["command"])
	ref := StepRef{
		Phase: phase,
		Kind:  StepGeneralAction,
		Index: index,
	}
	if call.StepRef != nil {
		ref = *call.StepRef
	}
	return StepRuntimeState{
		Ref:         ref,
		Status:      StepActive,
		Description: strings.TrimSpace(call.FunctionCall.Name),
		Command:     command,
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

func (l *Loop) publishCommittedRuntimeSnapshot() RuntimeSnapshot {
	if l == nil || l.activeTransaction == nil || l.activeTransaction.previous == nil {
		return l.publishRuntimeSnapshot()
	}
	snapshot := projectRuntimeSnapshot(l.activeTransaction.previous, l.activeTransaction.expected)
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
	case RuntimeControlAwaitingMutationVerificationResult,
		RuntimeControlAwaitingMutationVerificationChainResult:
		return "mutation_verification_result_required"
	case RuntimeControlAwaitingMutationVerificationEvidence,
		RuntimeControlAwaitingMutationVerificationChainEvidence:
		return "mutation_verification_evidence_required"
	case RuntimeControlAwaitingMutationContinuation:
		return "mutation_continuation_required"
	case RuntimeControlAwaitingContinuationHandoff:
		return "continuation_handoff_required"
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
		if s.RequiresResourceGuideLookupNow {
			return "resource_guide_lookup_required"
		}
		if s.requiresGuidedDiagnosisStepNow() {
			return "guided_diagnosis_step_required"
		}
		return "none"
	}
}

func (s RuntimeSnapshot) RequiredNextOutput() string {
	switch s.Control {
	case RuntimeControlAwaitingMutationVerificationResult,
		RuntimeControlAwaitingMutationVerificationChainResult:
		return "mutation_verification_result"
	case RuntimeControlAwaitingMutationVerificationEvidence,
		RuntimeControlAwaitingMutationVerificationChainEvidence:
		return "one read-only action satisfying a remaining mutation evidence requirement"
	case RuntimeControlAwaitingMutationContinuation:
		return "one materially different action or evidence-grounded phase_plan_revision"
	case RuntimeControlAwaitingContinuationHandoff:
		return "continuation_handoff"
	case RuntimeControlAwaitingGuidedPhaseProgress:
		return "phase_progress"
	case RuntimeControlAwaitingFinalReport:
		if s.FinalReportMustBeInconclusive {
			return "final_report with conclusive=false"
		}
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
		if s.RequiresResourceGuideLookupNow {
			return "resource_guide_lookup"
		}
		if s.requiresGuidedDiagnosisStepNow() {
			return "action for the next guide step, or guide_progress after useful evidence is already observed"
		}
		if s.Phase != nil {
			return "action or phase_progress according to the current phase completion condition"
		}
		return "valid structured output for the current request"
	}
}

func (s RuntimeSnapshot) ForbiddenNextOutputs() []string {
	switch s.Control {
	case RuntimeControlAwaitingMutationVerificationResult,
		RuntimeControlAwaitingMutationVerificationChainResult:
		return []string{"action", "final_report", "phase_progress", "next_directions", "answer"}
	case RuntimeControlAwaitingMutationVerificationEvidence,
		RuntimeControlAwaitingMutationVerificationChainEvidence:
		return []string{"mutating action", "final_report", "phase_progress", "next_directions", "answer", "mutation_verification_result"}
	case RuntimeControlAwaitingMutationContinuation:
		return []string{"final_report", "phase_progress", "next_directions", "answer"}
	case RuntimeControlAwaitingContinuationHandoff:
		return []string{"action", "phase_plan_revision", "phase_progress", "final_report", "next_directions", "answer"}
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
		if s.RequiresResourceGuideLookupNow {
			return []string{"action", "phase_progress", "guide_progress", "final_report", "next_directions", "answer"}
		}
		if s.requiresGuidedDiagnosisStepNow() {
			return []string{"final_report", "next_directions", "answer"}
		}
		return nil
	}
}

func (s RuntimeSnapshot) requiresGuidedDiagnosisStepNow() bool {
	if s.Phase == nil || s.Guide == nil || len(s.Guide.remainingSteps()) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s.Phase.currentStep().Name), "guided_diagnosis")
}

func (s RuntimeSnapshot) NestedStateName() string {
	if s.PendingMutationVerification != nil {
		if s.PendingMutationVerification.AwaitingResult {
			if s.PendingMutationVerification.isChain() {
				return "mutation_verification_chain_result"
			}
			return "mutation_verification_result"
		}
		if s.PendingMutationVerification.isChain() {
			return "mutation_verification_chain_evidence"
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
	if message := auditGoalExecution(s.Execution); message != "" {
		return message
	}
	class, knownControl := reactcontract.ClassifyRuntimeControlState(s.Control)
	switch {
	case !knownControl:
		return fmt.Sprintf("runtime control state is unknown: %q", s.Control)
	case class == reactcontract.ControlExecutionInvalid:
		return "runtime control state is unset"
	case len(s.PendingDispatches) > 0 && s.Control != RuntimeControlAwaitingToolResult:
		return "committed pending dispatch exists outside tool-result control"
	case s.Control == RuntimeControlAwaitingToolResult && len(s.PendingDispatches) == 0:
		return "tool result control has no committed pending dispatch"
	case (s.Control == RuntimeControlAwaitingUserQuery || s.Control == RuntimeControlExited) &&
		s.PendingMutationVerification != nil:
		return "terminal/user-query control cannot retain pending mutation verification"
	case s.PendingMutationVerification != nil && s.Execution != nil &&
		!snapshotHasAttempt(s.Execution, s.PendingMutationVerification.AttemptID):
		return "pending mutation verification has no owning attempt"
	case snapshotHasOrphanVerifyingAttempt(s.Execution, s.PendingMutationVerification):
		return "verifying mutation attempt has no active verification owner"
	case s.Control == RuntimeControlAwaitingContinuationHandoff &&
		(s.Execution == nil || strings.TrimSpace(s.Execution.RequestID) == "" || strings.TrimSpace(s.Execution.Goal.ID) == ""):
		return "continuation handoff control has no active request and goal"
	case s.ContinuationResumePending &&
		(s.Continuation == nil || s.Control != RuntimeControlAwaitingContinuationChoice):
		return "continuation resume flag has no matching continuation choice"
	case s.ContinuationResumePending &&
		(s.Execution == nil ||
			s.Continuation.Handoff.RequestID != s.Execution.RequestID ||
			s.Continuation.Handoff.GoalID != s.Execution.Goal.ID):
		return "continuation handoff identity does not match active execution"
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
	case s.Control == RuntimeControlAwaitingMutationVerificationEvidence &&
		(s.PendingMutationVerification == nil || !s.PendingMutationVerification.matchesEvidenceControl(s.Control)):
		return "mutation evidence control does not match verification payload"
	case s.Control == RuntimeControlAwaitingMutationVerificationResult &&
		(s.PendingMutationVerification == nil || !s.PendingMutationVerification.matchesResultControl(s.Control)):
		return "mutation verification result control does not match verification payload"
	case s.Control == RuntimeControlAwaitingMutationVerificationChainEvidence &&
		(s.PendingMutationVerification == nil || !s.PendingMutationVerification.matchesEvidenceControl(s.Control)):
		return "mutation verification chain evidence control does not match verification payload"
	case s.Control == RuntimeControlAwaitingMutationVerificationChainResult &&
		(s.PendingMutationVerification == nil || !s.PendingMutationVerification.matchesResultControl(s.Control)):
		return "mutation verification chain result control does not match verification payload"
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

func snapshotHasAttempt(execution *session.GoalExecutionState, attemptID string) bool {
	if execution == nil || strings.TrimSpace(attemptID) == "" {
		return false
	}
	_, ok := execution.AttemptByID(attemptID)
	return ok
}

func snapshotHasOrphanVerifyingAttempt(execution *session.GoalExecutionState, verification *pendingMutationVerification) bool {
	if execution == nil {
		return false
	}
	ownerID := ""
	if verification != nil {
		ownerID = verification.AttemptID
	}
	for _, attempt := range execution.OrderedAttempts() {
		if attempt.Status == reactcontract.AttemptVerifying && attempt.ID != ownerID {
			return true
		}
	}
	return false
}

func (l *Loop) runtimeStateAnchor() string {
	snapshot := l.RuntimeSnapshot()
	if !snapshot.ShouldEmitAnchor() {
		return ""
	}

	var b strings.Builder
	b.WriteString("Runtime lifecycle summary. Treat this as the concise decision contract for the next response; detailed anchors below remain authoritative for their domain.\n")
	fmt.Fprintf(&b, "state_revision: %d\n", snapshot.Revision)
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
		if check := s.PendingMutationVerification.activeCheck(); check != nil {
			fmt.Fprintf(b, "active_mutation_verification_id: %s\n", check.ID)
			fmt.Fprintf(b, "active_mutation_verification_mode: %s\n", check.Mode)
			if len(s.PendingMutationVerification.Checks) > 1 {
				fmt.Fprintf(b, "mutation_verification_chain_position: %d/%d\n", s.PendingMutationVerification.ActiveIndex+1, len(s.PendingMutationVerification.Checks))
			}
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
	if l == nil {
		return false
	}
	return projectRequiresResourceGuideLookup(l.mutableRuntime())
}

func projectRequiresResourceGuideLookup(state *runtimeState) bool {
	if state == nil || state.phaseStepState == nil || state.resourceClassification == nil {
		return false
	}
	current := state.phaseStepState.currentStep()
	return guidanceflow.LookupRequired(
		string(state.resourceClassification.Kind),
		state.resourceGuideInjected,
		current.Name,
	)
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
}

func (l *Loop) applyRuntimeCleanup(policy runtimeCleanupPolicy) {
	if l == nil {
		return
	}
	if policy.ClearPendingCalls {
		l.mutableRuntime().pendingCalls = nil
	}
	if policy.ClearResponseDirectives {
		l.mutableRuntime().pendingResponseDirective = ""
	}
	if policy.ClearDirectionState {
		l.mutableRuntime().pendingFinalReport = nil
		l.mutableRuntime().pendingNextDirections = nil
		l.mutableRuntime().pendingDirectionPrompt = nil
	}
	if policy.ClearMutationContinuation {
		l.mutableRuntime().mutationContinuationAttempts = 0
		l.mutableRuntime().finalReportMustBeInconclusive = false
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
	ClearResponseDirectives    bool
	ClearPendingFinalDirection bool
}

func (l *Loop) resetPhaseScopedState(policy phaseScopedResetPolicy) {
	if l == nil {
		return
	}
	if policy.ResetGuide {
		l.mutableRuntime().guideStepState = nil
	}
	if policy.ResetResourceGuideLookup {
		l.mutableRuntime().resourceGuideInjected = false
		l.mutableRuntime().resourceGuideQueries = nil
	}
	if policy.ClearResponseDirectives {
		l.mutableRuntime().pendingResponseDirective = ""
	}
	if policy.ClearPendingFinalDirection {
		l.mutableRuntime().pendingFinalReport = nil
		l.mutableRuntime().pendingNextDirections = nil
		l.mutableRuntime().pendingDirectionPrompt = nil
	}
}

func (l *Loop) defaultPhaseScopedResetPolicy(from PhaseRef) phaseScopedResetPolicy {
	policy := phaseScopedResetPolicy{
		ClearResponseDirectives:    true,
		ClearPendingFinalDirection: true,
	}
	if l == nil || l.mutableRuntime().phaseStepState == nil {
		return policy
	}
	for _, ref := range l.mutableRuntime().phaseStepState.phasesAtOrAfter(from) {
		name := strings.ToLower(strings.TrimSpace(ref.Name))
		switch {
		case strings.Contains(name, "guidance"), strings.Contains(name, "guided"):
			policy.ResetGuide = true
			policy.ResetResourceGuideLookup = true
		}
	}
	return policy
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

func contextHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:8])
}

func (l *Loop) appendContextBlock(kind, content string, preserve bool) bool {
	if preserve {
		l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, content)
		return true
	}
	if l.mutableRuntime().contextBlockHashes == nil {
		l.mutableRuntime().contextBlockHashes = make(map[string]struct{})
	}
	key := kind + ":" + contextHash(content)
	if _, ok := l.mutableRuntime().contextBlockHashes[key]; ok {
		return false
	}
	l.mutableRuntime().contextBlockHashes[key] = struct{}{}
	l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, content)
	return true
}

func (l *Loop) appendCorrection(code, message string) bool {
	l.mutableRuntime().lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	return l.appendContextBlock("correction:"+code, message, false)
}

func (l *Loop) appendCorrectionWithCompaction(code, message string) bool {
	l.mutableRuntime().lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	if !l.shouldCompactForStateRewrite() {
		return l.appendContextBlock("correction:"+code, message, false)
	}
	if l.activeTransaction != nil {
		if err := l.prepareContextCompaction(
			"correction",
			code,
			"Return one corrected next response. Do not repeat the invalid response.",
		); err != nil {
			return l.appendContextBlock("correction:"+code, message, false)
		}
		return true
	}
	return l.appendContextBlock("correction:"+code, message, false)
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
	return l.mutableRuntime().lastOriginalQuery != "" ||
		l.mutableRuntime().lastRequirementAnalysis != nil ||
		l.mutableRuntime().lastRequestContext != nil ||
		strings.TrimSpace(l.mutableRuntime().lastDiagnosisSummary) != ""
}

func (l *Loop) writePriorConversationMemory(b *strings.Builder) {
	if l.mutableRuntime().lastOriginalQuery != "" {
		b.WriteString("previous_original_query: ")
		b.WriteString(compactPriorString(l.mutableRuntime().lastOriginalQuery, 1000))
		b.WriteString("\n")
	}
	if l.mutableRuntime().lastRequirementAnalysis != nil {
		if raw, err := json.Marshal(compactPriorRequirementAnalysis(l.mutableRuntime().lastRequirementAnalysis)); err == nil {
			b.WriteString("previous_requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.mutableRuntime().lastRequestContext != nil {
		if raw, err := json.Marshal(compactPriorRequestContext(l.mutableRuntime().lastRequestContext)); err == nil {
			b.WriteString("previous_request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.mutableRuntime().lastDiagnosisSummary) != "" {
		if raw, err := json.Marshal(l.mutableRuntime().lastDiagnosisSummary); err == nil {
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
	return l.mutableRuntime().originalQuery != "" ||
		l.mutableRuntime().requirementAnalysis != nil ||
		l.mutableRuntime().requestContext != nil ||
		l.mutableRuntime().resourceClassification != nil ||
		l.mutableRuntime().lastContextError != nil ||
		len(l.mutableRuntime().injectedGuides) > 0 ||
		strings.TrimSpace(l.mutableRuntime().lastAssistantText) != ""
}

func (l *Loop) compactDiagnosisSummary() string {
	var b strings.Builder
	if anchor := l.executionAnchor(); anchor != "" {
		b.WriteString("goal_execution_projection: ")
		b.WriteString(anchor)
		b.WriteString("\n")
	}
	if strings.TrimSpace(l.mutableRuntime().lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.mutableRuntime().lastAssistantText)); err == nil {
			b.WriteString("last_assistant_text: ")
			b.Write(raw)
		}
	}
	return strings.TrimSpace(b.String())
}

func (l *Loop) writeConversationState(b *strings.Builder, includeGuideContent bool) {
	if l.mutableRuntime().originalQuery != "" {
		b.WriteString("original_query: ")
		b.WriteString(l.mutableRuntime().originalQuery)
		b.WriteString("\n")
	}
	if l.mutableRuntime().requirementAnalysis != nil {
		if raw, err := json.Marshal(l.mutableRuntime().requirementAnalysis); err == nil {
			b.WriteString("requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.mutableRuntime().requestContext != nil {
		if raw, err := json.Marshal(l.mutableRuntime().requestContext); err == nil {
			b.WriteString("request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.mutableRuntime().resourceClassification != nil {
		if raw, err := json.Marshal(l.mutableRuntime().resourceClassification); err == nil {
			b.WriteString("resource_classification: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.mutableRuntime().lastContextError != nil {
		if raw, err := json.Marshal(l.mutableRuntime().lastContextError); err == nil {
			b.WriteString("last_error: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if len(l.mutableRuntime().injectedGuides) > 0 {
		keys := make([]string, 0, len(l.mutableRuntime().injectedGuides))
		for key := range l.mutableRuntime().injectedGuides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		refs := make([]guideRef, 0, len(keys))
		for _, key := range keys {
			ref := l.mutableRuntime().injectedGuides[key]
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
	if anchor := l.executionAnchor(); anchor != "" {
		b.WriteString("goal_execution_projection: ")
		b.WriteString(anchor)
		b.WriteString("\n")
	}
	if strings.TrimSpace(l.mutableRuntime().lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.mutableRuntime().lastAssistantText)); err == nil {
			b.WriteString("last_assistant_answer: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
}

func (l *Loop) shouldCompactBeforeNextSend() bool {
	if l.mutableRuntime().actionSeq == l.mutableRuntime().lastCompactedActionSeq {
		return false
	}
	estimated := l.mutableRuntime().contextApproxTokens + estimateContextTokens(l.mutableRuntime().currChatContent...)
	return estimated >= l.contextCompactThresholdTokens()
}

func (l *Loop) shouldCompactForStateRewrite() bool {
	return l.mutableRuntime().contextApproxTokens+estimateContextTokens(l.mutableRuntime().currChatContent...) >= l.contextCompactThresholdTokens()
}

func (l *Loop) prepareContextCompaction(reason, code, nextInstruction string) error {
	if l == nil || l.activeTransaction == nil {
		return fmt.Errorf("context compaction requires an active runtime transaction")
	}
	if l.mutableRuntime().pendingCompaction != nil {
		return fmt.Errorf("context compaction is already pending")
	}
	before := l.mutableRuntime().contextApproxTokens + estimateContextTokens(l.mutableRuntime().currChatContent...)
	limit := l.contextLimitTokens()
	if l.mutableRuntime().pendingResponseDirective != "" {
		nextInstruction = "Continue from compacted state and follow the pending runtime directive below."
	}
	l.mutableRuntime().pendingCompaction = &compactionState{
		Reason:          reason,
		Code:            code,
		Limit:           limit,
		OriginalContent: cloneChatContent(l.mutableRuntime().currChatContent),
		OriginalHashes:  cloneStringSet(l.mutableRuntime().contextBlockHashes),
	}
	l.mutableRuntime().currChatContent = []any{l.compactedStateMessage(nextInstruction)}
	l.appendPendingResponseDirectiveAfterCompaction()
	l.mutableRuntime().contextBlockHashes = nil
	l.mutableRuntime().lastCompactedActionSeq = l.mutableRuntime().actionSeq
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: estimated context %d/%d tokens. Preserving request, procedure order, evidence, and pending directive.", before, limit))
	return l.queueTurnEffect(reactcontract.Effect{
		Kind: reactcontract.EffectResetChat,
		Payload: chatResetEffect{
			Reason: reason,
			Code:   code,
			Limit:  limit,
		},
	})
}

func (l *Loop) appendPendingResponseDirectiveAfterCompaction() {
	if strings.TrimSpace(l.mutableRuntime().pendingResponseDirective) == "" {
		return
	}
	l.mutableRuntime().currChatContent = append(l.mutableRuntime().currChatContent, "Pending runtime directive for the next model response:\n"+l.mutableRuntime().pendingResponseDirective)
}

func (l *Loop) appendGuideObservation(ref guideRef, content string) {
	if l.mutableRuntime().injectedGuides == nil {
		l.mutableRuntime().injectedGuides = make(map[string]guideRef)
	}
	key := ref.GuideID
	if key == "" {
		key = ref.Hash
	}
	if previous, ok := l.mutableRuntime().injectedGuides[key]; ok && previous.Hash == ref.Hash {
		l.appendContextBlock("guide-ref", fmt.Sprintf("Guide context already injected; use guide_ref %s (%s) without repeating the guide body.", key, ref.Hash), false)
		return
	}
	ref.Content = content
	l.mutableRuntime().injectedGuides[key] = ref
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
	l.mutableRuntime().contextApproxTokens += estimateContextTokens(contents...)
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
	FunctionCall      gollm.FunctionCall
	StepRef           *StepRef
	ParsedToolCall    *tools.ToolCall
	IsInteractive     bool
	InteractiveError  error
	ModifiesResource  string
	RetryOf           string
	RetryReason       string
	ChangedSince      []string
	Verification      *reactcontract.VerificationSpec
	verificationInput any
	Risk              *reactcontract.CommandRisk
	DispatchID        string
	AttemptID         string
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
			Ref:             guideRuntimeStepRef(phase, detail.Index),
			Status:          status,
			Description:     strings.TrimSpace(detail.Description),
			Command:         strings.TrimSpace(detail.RenderedCommand),
			ExpectedOutcome: strings.TrimSpace(detail.ExpectedOutcome),
		})
	}
	return steps
}

func guideRuntimeStepRef(phase PhaseRef, stepIndex int) StepRef {
	stepID := fmt.Sprintf("guide-step-%d", stepIndex)
	if phase.ID != "" {
		stepID = fmt.Sprintf("%s.guide-step-%d", phase.ID, stepIndex)
	}
	return StepRef{
		Phase:         phase,
		Kind:          StepResourceGuideDiagnostic,
		ID:            stepID,
		GoalLineageID: stepID + ".lineage",
		Index:         stepIndex,
	}
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
	if l == nil || l.mutableRuntime().guideStepState == nil || ref.Index <= 0 || ref.Index > l.mutableRuntime().guideStepState.TotalSteps {
		return false
	}
	if l.mutableRuntime().guideStepState.Skipped == nil {
		l.mutableRuntime().guideStepState.Skipped = map[int]bool{}
	}
	if l.mutableRuntime().guideStepState.Completed[ref.Index] || l.mutableRuntime().guideStepState.Skipped[ref.Index] {
		return false
	}
	l.mutableRuntime().guideStepState.Skipped[ref.Index] = true
	return true
}

func (l *Loop) skipMutationEvidenceRuntimeStep(ref StepRef) bool {
	if l == nil || l.mutableRuntime().pendingMutationVerification == nil || ref.ID == "" {
		return false
	}
	verification := l.mutableRuntime().pendingMutationVerification
	check := verification.activeCheck()
	if check == nil || check.ID != ref.ID {
		return false
	}
	if len(check.EvidenceRefs) == 0 {
		return false
	}
	if check.Status == reactcontract.VerificationCheckSatisfied ||
		check.Status == reactcontract.VerificationCheckSkipped {
		return false
	}
	check.Status = reactcontract.VerificationCheckSkipped
	if !verification.advanceCheck() {
		verification.AwaitingResult = true
	}
	return true
}
