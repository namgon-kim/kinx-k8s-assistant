package coordinator

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/request"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
)

// runtimeState is the single mutable root for one coordinator session.
// session.Aggregate owns revisioning and atomic root replacement.
type runtimeState struct {
	control                   RuntimeControlState
	currIteration             int
	currChatContent           []any
	contextBlockHashes        map[string]struct{}
	pendingCalls              []PendingCall
	dispatchIntents           map[string]contract.ToolDispatchIntent
	dispatchOrder             []string
	sessionID                 string
	requestSequence           int
	execution                 *session.GoalExecutionState
	continuation              *contract.ContinuationState
	continuationResumePending bool

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
	actionSeq               int
	lastCompactedActionSeq  int
	contextApproxTokens     int
	pendingCompaction       *compactionState
	contextLengthRetryUsed  bool
	lastAssistantText       string
	lastProgressText        string
	resourceGuideInjected   bool
	resourceGuideEvidence   []string
	resourceGuideQueries    map[string]struct{}

	guideStepState                *guideStepState
	pendingResponseDirective      string
	pendingFinalReport            *finalReport
	pendingNextDirections         *nextDirections
	pendingDirectionPrompt        *directionPromptState
	pendingMutationVerification   *pendingMutationVerification
	mutationContinuationAttempts  int
	finalReportMustBeInconclusive bool
}

type runtimeTransaction struct {
	expected          uint64
	previous          *runtimeState
	candidate         *runtimeState
	messages          []deferredMessage
	effects           []contract.Effect
	refreshInputOwner bool
}

func (l *Loop) queueTurnEffect(effect contract.Effect) error {
	if l == nil || l.activeTransaction == nil {
		return fmt.Errorf("turn effect %q queued without active transaction", effect.Kind)
	}
	l.activeTransaction.effects = append(l.activeTransaction.effects, effect)
	return nil
}

type deferredMessage struct {
	source      api.MessageSource
	messageType api.MessageType
	payload     any
	translate   bool
	context     context.Context
}

func newRuntimeState() runtimeState {
	return runtimeState{control: RuntimeControlAwaitingUserQuery}
}

func (l *Loop) mutableRuntime() *runtimeState {
	if l == nil {
		return nil
	}
	if l.runtimeState != nil {
		if l.stateStore == nil {
			l.stateStore = session.NewAggregate(cloneRuntimeState(*l.runtimeState))
			l.runtimeState = l.stateStore.Root()
		}
		return l.runtimeState
	}
	initial := newRuntimeState()
	l.stateStore = session.NewAggregate(initial)
	l.runtimeState = l.stateStore.Root()
	return l.runtimeState
}

func (l *Loop) beginRuntimeTransaction(requiredRevision ...uint64) (*runtimeTransaction, error) {
	if l == nil {
		return nil, fmt.Errorf("session aggregate is not initialized")
	}
	l.mutableRuntime()
	if l.stateStore == nil {
		return nil, fmt.Errorf("session aggregate is not initialized")
	}
	if l.activeTransaction != nil {
		return nil, fmt.Errorf("nested runtime transaction is not allowed")
	}
	expected, candidate := l.stateStore.Candidate(cloneRuntimeState)
	if len(requiredRevision) > 0 && expected != requiredRevision[0] {
		return nil, fmt.Errorf("stale model response: turn started at revision %d, current revision is %d", requiredRevision[0], expected)
	}
	if candidate == nil {
		return nil, fmt.Errorf("session candidate is nil")
	}
	tx := &runtimeTransaction{expected: expected, previous: l.runtimeState, candidate: candidate}
	l.runtimeState = candidate
	l.activeTransaction = tx
	return tx, nil
}

func (l *Loop) mutateRuntimeAtomically(update func() error) (returnErr error) {
	if l == nil || update == nil {
		return fmt.Errorf("runtime mutation is not configured")
	}
	if l.activeTransaction != nil {
		return update()
	}
	tx, err := l.beginRuntimeTransaction()
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		returnErr = l.commitRuntimeTransaction(tx)
	}()
	return update()
}

func (l *Loop) commitRuntimeTransaction(tx *runtimeTransaction) error {
	if l == nil || tx == nil || l.activeTransaction != tx {
		return fmt.Errorf("runtime transaction is not active")
	}
	_, err := l.stateStore.Commit(tx.expected, tx.candidate, l.auditRuntimeCandidate)
	if err != nil {
		l.runtimeState = tx.previous
		l.activeTransaction = nil
		return err
	}
	l.runtimeState = l.stateStore.Root()
	l.activeTransaction = nil
	if tx.refreshInputOwner {
		l.refreshInputOwner()
	}
	for _, message := range tx.messages {
		payload := message.payload
		if message.translate {
			if raw, ok := payload.(string); ok {
				payload = l.translateModelText(message.context, raw)
			}
		}
		l.emitMessage(message.source, message.messageType, payload)
	}
	return nil
}

func (l *Loop) auditRuntimeCandidate(candidate runtimeState, candidateRevision uint64) error {
	message := projectRuntimeSnapshot(&candidate, candidateRevision).AuditError()
	if message != "" {
		return fmt.Errorf("runtime lifecycle invariant violation: %s", message)
	}
	return nil
}

func (l *Loop) rollbackRuntimeTransaction(tx *runtimeTransaction) {
	if l == nil || tx == nil || l.activeTransaction != tx {
		return
	}
	l.runtimeState = tx.previous
	l.activeTransaction = nil
}

func cloneRuntimeState(source runtimeState) runtimeState {
	clone := source
	clone.currChatContent = cloneChatContent(source.currChatContent)
	clone.contextBlockHashes = cloneStringSet(source.contextBlockHashes)
	clone.pendingCalls = clonePendingCalls(source.pendingCalls)
	clone.dispatchIntents = cloneDispatchIntents(source.dispatchIntents)
	clone.dispatchOrder = append([]string(nil), source.dispatchOrder...)
	clone.promptOptions.ToolProfile.ToolNames = append([]string(nil), source.promptOptions.ToolProfile.ToolNames...)
	clone.toolProfile.ToolNames = append([]string(nil), source.toolProfile.ToolNames...)
	clone.requirementAnalysis = cloneRequirementAnalysis(source.requirementAnalysis)
	clone.requestContext = clonePointer(source.requestContext)
	clone.phaseStepState = clonePhaseStepState(source.phaseStepState)
	clone.resourceClassification = clonePointer(source.resourceClassification)
	clone.lastRequirementAnalysis = cloneRequirementAnalysis(source.lastRequirementAnalysis)
	clone.lastRequestContext = clonePointer(source.lastRequestContext)
	clone.resourceDiscoveryCache = cloneResourceDiscoveryCache(source.resourceDiscoveryCache)
	clone.lastContextError = clonePointer(source.lastContextError)
	clone.injectedGuides = cloneGuideRefs(source.injectedGuides)
	clone.pendingCompaction = cloneCompactionState(source.pendingCompaction)
	clone.resourceGuideEvidence = append([]string(nil), source.resourceGuideEvidence...)
	clone.resourceGuideQueries = cloneStringSet(source.resourceGuideQueries)
	clone.guideStepState = cloneGuideStepState(source.guideStepState)
	clone.pendingFinalReport = cloneFinalReport(source.pendingFinalReport)
	clone.pendingNextDirections = cloneNextDirections(source.pendingNextDirections)
	clone.pendingDirectionPrompt = cloneDirectionPrompt(source.pendingDirectionPrompt)
	clone.pendingMutationVerification = cloneMutationVerification(source.pendingMutationVerification)
	clone.execution = session.CloneGoalExecutionState(source.execution)
	clone.continuation = cloneContinuationState(source.continuation)
	return clone
}

func cloneContinuationState(source *contract.ContinuationState) *contract.ContinuationState {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Handoff.CompletedSteps = append([]string(nil), source.Handoff.CompletedSteps...)
	clone.Handoff.EvidenceRefs = append([]string(nil), source.Handoff.EvidenceRefs...)
	clone.Handoff.UnresolvedSteps = append([]string(nil), source.Handoff.UnresolvedSteps...)
	clone.Handoff.MandatoryObligations = append([]string(nil), source.Handoff.MandatoryObligations...)
	return &clone
}

func cloneCompactionState(source *compactionState) *compactionState {
	if source == nil {
		return nil
	}
	clone := *source
	clone.OriginalContent = cloneChatContent(source.OriginalContent)
	clone.OriginalHashes = cloneStringSet(source.OriginalHashes)
	return &clone
}

func cloneDispatchIntents(source map[string]contract.ToolDispatchIntent) map[string]contract.ToolDispatchIntent {
	if source == nil {
		return nil
	}
	clone := make(map[string]contract.ToolDispatchIntent, len(source))
	for id, intent := range source {
		clone[id] = cloneDispatchIntent(intent)
	}
	return clone
}

func cloneDispatchIntent(intent contract.ToolDispatchIntent) contract.ToolDispatchIntent {
	intent.Call.Arguments = contract.CloneDataMap(intent.Call.Arguments)
	intent.StepRef = clonePointer(intent.StepRef)
	intent.Target = clonePointer(intent.Target)
	intent.Verification = cloneVerificationSpec(intent.Verification)
	intent.Risk = clonePointer(intent.Risk)
	return intent
}

func cloneChatContent(source []any) []any {
	if source == nil {
		return nil
	}
	clone := make([]any, len(source))
	for i, item := range source {
		switch value := item.(type) {
		case gollm.FunctionCallResult:
			value.Result = contract.CloneDataMap(value.Result)
			clone[i] = value
		case *gollm.FunctionCallResult:
			if value == nil {
				clone[i] = (*gollm.FunctionCallResult)(nil)
				continue
			}
			itemClone := *value
			itemClone.Result = contract.CloneDataMap(value.Result)
			clone[i] = &itemClone
		default:
			clone[i] = cloneRuntimeData(item)
		}
	}
	return clone
}

func cloneRequirementAnalysis(source *requirementAnalysis) *requirementAnalysis {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Resources = append([]requirementResource(nil), source.Resources...)
	clone.Evidence = append([]string(nil), source.Evidence...)
	clone.Constraints = append([]string(nil), source.Constraints...)
	clone.Ambiguities = append([]string(nil), source.Ambiguities...)
	clone.OperationalFocus = clonePointer(source.OperationalFocus)
	if clone.OperationalFocus != nil {
		clone.OperationalFocus.RelatedResourceHints = append([]requirementRelatedResource(nil), source.OperationalFocus.RelatedResourceHints...)
		clone.OperationalFocus.EvidenceNeeds = append([]string(nil), source.OperationalFocus.EvidenceNeeds...)
	}
	return clone
}

func clonePendingCalls(source []PendingCall) []PendingCall {
	clone := append([]PendingCall(nil), source...)
	for i := range clone {
		clone[i].FunctionCall.Arguments = contract.CloneDataMap(source[i].FunctionCall.Arguments)
		clone[i].StepRef = clonePointer(source[i].StepRef)
		clone[i].ChangedSince = append([]string(nil), source[i].ChangedSince...)
		clone[i].Verification = cloneVerificationSpec(source[i].Verification)
		clone[i].Risk = clonePointer(source[i].Risk)
	}
	return clone
}

func cloneVerificationSpec(source *contract.VerificationSpec) *contract.VerificationSpec {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Checks = make([]contract.VerificationCheckSpec, len(source.Checks))
	for i, check := range source.Checks {
		check.Target = clonePointer(check.Target)
		clone.Checks[i] = check
	}
	return clone
}

func clonePointer[T any](source *T) *T {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	if source == nil {
		return nil
	}
	clone := make(map[string]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func clonePhaseStepState(source *phaseStepState) *phaseStepState {
	if source == nil {
		return nil
	}
	clone := *source
	clone.PhaseSteps = append([]phaseStep(nil), source.PhaseSteps...)
	for i := range clone.PhaseSteps {
		clone.PhaseSteps[i].AllowedNext = append([]string(nil), source.PhaseSteps[i].AllowedNext...)
		clone.PhaseSteps[i].Steps = append([]phaseExecutionStep(nil), source.PhaseSteps[i].Steps...)
	}
	clone.Completed = make(map[int]bool, len(source.Completed))
	for key, value := range source.Completed {
		clone.Completed[key] = value
	}
	return &clone
}

func cloneResourceDiscoveryCache(source map[string]resourceClassification) map[string]resourceClassification {
	if source == nil {
		return nil
	}
	clone := make(map[string]resourceClassification, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneGuideRefs(source map[string]guideRef) map[string]guideRef {
	if source == nil {
		return nil
	}
	clone := make(map[string]guideRef, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneRuntimeData(source any) any {
	if value, ok := source.(map[string]any); ok {
		return contract.CloneDataMap(value)
	}
	return source
}

func cloneGuideStepState(source *guideStepState) *guideStepState {
	if source == nil {
		return nil
	}
	clone := *source
	clone.StepDetails = append([]guideStepDetail(nil), source.StepDetails...)
	for i := range clone.StepDetails {
		clone.StepDetails[i].Preconditions = append([]string(nil), source.StepDetails[i].Preconditions...)
	}
	clone.Completed = cloneIntBoolMap(source.Completed)
	clone.Skipped = cloneIntBoolMap(source.Skipped)
	return &clone
}

func cloneIntBoolMap(source map[int]bool) map[int]bool {
	if source == nil {
		return nil
	}
	clone := make(map[int]bool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneFinalReport(source *finalReport) *finalReport {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Attempted = append([]string(nil), source.Attempted...)
	clone.EvidenceKnown = append([]string(nil), source.EvidenceKnown...)
	clone.EvidenceMissing = append([]string(nil), source.EvidenceMissing...)
	clone.RecommendedUserActions = append([]string(nil), source.RecommendedUserActions...)
	clone.ProblematicResources = append([]problematicResource(nil), source.ProblematicResources...)
	clone.Blockers = append([]string(nil), source.Blockers...)
	return clone
}

func cloneNextDirections(source *nextDirections) *nextDirections {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Options = append([]nextDirectionOption(nil), source.Options...)
	return clone
}

func cloneDirectionPrompt(source *directionPromptState) *directionPromptState {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Options = append([]nextDirectionOption(nil), source.Options...)
	return clone
}

func cloneMutationVerification(source *pendingMutationVerification) *pendingMutationVerification {
	clone := clonePointer(source)
	if clone == nil {
		return nil
	}
	clone.Checks = make([]verificationRuntimeCheck, len(source.Checks))
	for i, check := range source.Checks {
		check.EvidenceRefs = append([]string(nil), check.EvidenceRefs...)
		clone.Checks[i] = check
	}
	return clone
}
