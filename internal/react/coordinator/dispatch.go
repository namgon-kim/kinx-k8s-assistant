package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type canonicalDispatchPayload struct {
	Tool             string                     `json:"tool"`
	Arguments        map[string]any             `json:"arguments"`
	Target           *contract.ActionTarget     `json:"target,omitempty"`
	ModifiesResource string                     `json:"modifies_resource"`
	Verification     *contract.VerificationSpec `json:"verification,omitempty"`
	Risk             *contract.CommandRisk      `json:"risk,omitempty"`
}

type dispatchAdmissionCode string

const (
	dispatchAdmissionApprovalMissing   dispatchAdmissionCode = "approval_missing"
	dispatchAdmissionIntegrityMismatch dispatchAdmissionCode = "integrity_mismatch"
	dispatchAdmissionInvocationInvalid dispatchAdmissionCode = "invocation_parse_failed"
)

type dispatchAdmissionError struct {
	Code       dispatchAdmissionCode
	DispatchID string
	Err        error
}

func (e *dispatchAdmissionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("dispatch %q rejected before invocation: %s", e.DispatchID, e.Code)
	}
	return fmt.Sprintf("dispatch %q rejected before invocation: %s: %v", e.DispatchID, e.Code, e.Err)
}

func (e *dispatchAdmissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (l *Loop) preparePendingToolDispatch(approvalGranted bool) error {
	if l == nil || l.activeTransaction == nil {
		return fmt.Errorf("tool dispatch preparation requires an active runtime transaction")
	}
	pending := l.mutableRuntime().pendingCalls
	if len(pending) == 0 {
		return fmt.Errorf("tool dispatch preparation has no pending calls")
	}
	l.pruneTerminalDispatches()
	if l.mutableRuntime().dispatchIntents == nil {
		l.mutableRuntime().dispatchIntents = map[string]contract.ToolDispatchIntent{}
	}
	dispatchIDs := make([]string, 0, len(pending))
	for i := range pending {
		call := pending[i]
		attemptID, attemptReserved := l.prepareDispatchAttempt(&call)
		if attemptID == "" {
			return fmt.Errorf("tool dispatch attempt could not be allocated")
		}
		if execution := l.mutableRuntime().execution; execution != nil {
			execution.EventSequence++
			call.DispatchID = fmt.Sprintf("%s.dispatch-%06d", execution.RequestID, execution.EventSequence)
		} else {
			l.mutableRuntime().actionSeq++
			call.DispatchID = fmt.Sprintf("dispatch-%06d", l.mutableRuntime().actionSeq)
		}
		call.AttemptID = attemptID
		intent, err := l.toolDispatchIntent(call, approvalGranted, attemptReserved)
		if err != nil {
			return err
		}
		l.mutableRuntime().dispatchIntents[intent.ID] = intent
		l.mutableRuntime().dispatchOrder = append(l.mutableRuntime().dispatchOrder, intent.ID)
		dispatchIDs = append(dispatchIDs, intent.ID)
	}
	l.mutableRuntime().pendingCalls = nil
	l.transitionControl(RuntimeControlAwaitingToolResult)
	l.refreshInputOwner()
	return l.queueTurnEffect(contract.Effect{
		Kind:    contract.EffectInvokeTool,
		Payload: contract.InvokeToolEffect{DispatchIDs: dispatchIDs},
	})
}

func (l *Loop) prepareDispatchAttempt(call *PendingCall) (string, bool) {
	if call == nil {
		return "", false
	}
	if call.StepRef != nil && call.StepRef.Kind == contract.StepMutationEvidenceRequirement {
		if verification := l.mutableRuntime().pendingMutationVerification; verification != nil {
			return verification.AttemptID, false
		}
	}
	execution := l.mutableRuntime().execution
	if execution == nil || call.StepRef == nil {
		l.mutableRuntime().actionSeq++
		return fmt.Sprintf("unscoped-attempt-%06d", l.mutableRuntime().actionSeq), false
	}
	execution.EventSequence++
	attemptID := fmt.Sprintf("%s.attempt-%06d", execution.RequestID, execution.EventSequence)
	if err := execution.AppendAttempt(attemptRecordFromCall(execution, *call, attemptID, contract.AttemptDispatchPending)); err != nil {
		return "", false
	}
	return attemptID, true
}

func (l *Loop) toolDispatchIntent(call PendingCall, approvalGranted, attemptReserved bool) (contract.ToolDispatchIntent, error) {
	arguments := contract.CloneDataMap(call.FunctionCall.Arguments)
	var target *contract.ActionTarget
	if value, ok := actionTargetFromFunctionCall(call.FunctionCall); ok {
		copy := contract.ActionTarget(value)
		target = &copy
	}
	tool := l.registry.Tools.Lookup(call.FunctionCall.Name)
	if !toolDefinesArgument(tool, "target") {
		delete(arguments, "target")
	}
	delete(arguments, "runtime_target")
	payload := canonicalPayloadFromCall(call.FunctionCall.Name, arguments, target, call.ModifiesResource, call.Verification, call.Risk)
	hash, err := hashCanonicalDispatch(payload)
	if err != nil {
		return contract.ToolDispatchIntent{}, err
	}
	approvalRequired := callRequiresApproval(call)
	return contract.ToolDispatchIntent{
		ID:              call.DispatchID,
		AttemptID:       call.AttemptID,
		AttemptReserved: attemptReserved,
		Call: contract.FunctionCall{
			ID:        call.FunctionCall.ID,
			Name:      call.FunctionCall.Name,
			Arguments: arguments,
		},
		StepRef:              clonePointer(call.StepRef),
		Target:               target,
		ModifiesResource:     call.ModifiesResource,
		Verification:         cloneVerificationSpec(call.Verification),
		Risk:                 clonePointer(call.Risk),
		ApprovalRequired:     approvalRequired,
		ApprovalGranted:      !approvalRequired || approvalGranted,
		CanonicalPayloadHash: hash,
		Status:               contract.ToolDispatchPending,
	}, nil
}

func hashCanonicalDispatch(payload canonicalDispatchPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("canonical dispatch payload marshal failed: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalPayloadFromCall(
	tool string,
	arguments map[string]any,
	target *contract.ActionTarget,
	modifiesResource string,
	verification *contract.VerificationSpec,
	risk *contract.CommandRisk,
) canonicalDispatchPayload {
	return canonicalDispatchPayload{
		Tool:             tool,
		Arguments:        contract.CloneDataMap(arguments),
		Target:           clonePointer(target),
		ModifiesResource: modifiesResource,
		Verification:     cloneVerificationSpec(verification),
		Risk:             clonePointer(risk),
	}
}

func canonicalPayloadFromIntent(intent contract.ToolDispatchIntent) canonicalDispatchPayload {
	return canonicalPayloadFromCall(
		intent.Call.Name,
		intent.Call.Arguments,
		intent.Target,
		intent.ModifiesResource,
		intent.Verification,
		intent.Risk,
	)
}

func callRequiresApproval(call PendingCall) bool {
	return call.Risk != nil && call.Risk.Risky
}

func (l *Loop) pendingCallsRequireApproval() bool {
	for _, call := range l.mutableRuntime().pendingCalls {
		if callRequiresApproval(call) {
			return true
		}
	}
	return false
}

func (l *Loop) pendingCallFromDispatch(ctx context.Context, intent contract.ToolDispatchIntent) (PendingCall, error) {
	if intent.Status != contract.ToolDispatchPending {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionInvocationInvalid,
			DispatchID: intent.ID,
			Err:        fmt.Errorf("dispatch status is %q", intent.Status),
		}
	}
	if intent.ApprovalRequired && !intent.ApprovalGranted {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionApprovalMissing,
			DispatchID: intent.ID,
			Err:        fmt.Errorf("required human approval is missing"),
		}
	}
	payload := canonicalPayloadFromIntent(intent)
	hash, err := hashCanonicalDispatch(payload)
	if err != nil {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionIntegrityMismatch,
			DispatchID: intent.ID,
			Err:        err,
		}
	}
	if hash != intent.CanonicalPayloadHash {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionIntegrityMismatch,
			DispatchID: intent.ID,
			Err:        fmt.Errorf("canonical payload changed after commit"),
		}
	}
	if l == nil || l.registry == nil {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionInvocationInvalid,
			DispatchID: intent.ID,
			Err:        fmt.Errorf("tool registry is unavailable"),
		}
	}
	parsed, err := l.registry.Tools.ParseToolInvocation(ctx, intent.Call.Name, contract.CloneDataMap(intent.Call.Arguments))
	if err != nil {
		return PendingCall{}, &dispatchAdmissionError{
			Code:       dispatchAdmissionInvocationInvalid,
			DispatchID: intent.ID,
			Err:        err,
		}
	}
	call := pendingCallFromIntent(intent, l.registry.Tools.Lookup(intent.Call.Name))
	call.ParsedToolCall = parsed
	return call, nil
}

func pendingCallFromIntent(intent contract.ToolDispatchIntent, tool tools.Tool) PendingCall {
	return PendingCall{
		FunctionCall:     runtimeCallFromDispatch(intent, tool),
		StepRef:          clonePointer(intent.StepRef),
		ModifiesResource: intent.ModifiesResource,
		Verification:     cloneVerificationSpec(intent.Verification),
		Risk:             clonePointer(intent.Risk),
		DispatchID:       intent.ID,
		AttemptID:        intent.AttemptID,
	}
}

func runtimeCallFromDispatch(intent contract.ToolDispatchIntent, tool tools.Tool) gollm.FunctionCall {
	call := gollm.FunctionCall{
		ID:        intent.Call.ID,
		Name:      intent.Call.Name,
		Arguments: contract.CloneDataMap(intent.Call.Arguments),
	}
	if intent.Target != nil {
		target := actionTargetData(*intent.Target)
		if toolDefinesArgument(tool, "target") {
			call.Arguments["runtime_target"] = target
		} else {
			call.Arguments["target"] = target
		}
	}
	return call
}

func actionTargetData(target contract.ActionTarget) map[string]any {
	return map[string]any{
		"resource":  target.Resource,
		"namespace": target.Namespace,
		"name":      target.Name,
	}
}

func dispatchTerminalResult(status, executionState, reason, causedBy string) map[string]any {
	result := map[string]any{
		"status":          status,
		"execution_state": executionState,
		"reason":          strings.TrimSpace(reason),
	}
	if causedBy = strings.TrimSpace(causedBy); causedBy != "" {
		result["caused_by_dispatch_id"] = causedBy
	}
	return result
}

func (l *Loop) cancelUninvokedDispatches(dispatchIDs []string, reason, causedBy string) {
	cancelled := 0
	for _, dispatchID := range dispatchIDs {
		intent, ok := l.mutableRuntime().dispatchIntents[dispatchID]
		if !ok || intent.Status != contract.ToolDispatchPending {
			continue
		}
		call := pendingCallFromIntent(intent, l.lookupTool(intent.Call.Name))
		l.appendFunctionCallResult(call.FunctionCall, dispatchTerminalResult(
			"cancelled",
			"not_invoked",
			reason,
			causedBy,
		))
		intent.Status = contract.ToolDispatchCancelled
		l.mutableRuntime().dispatchIntents[dispatchID] = intent
		if intent.AttemptReserved {
			l.setAttemptStatus(intent.AttemptID, contract.AttemptCancelled)
		}
		cancelled++
	}
	if cancelled > 0 {
		l.recordInternalObservation("tool_dispatch_cancelled", reason)
	}
}

func (l *Loop) lookupTool(name string) tools.Tool {
	if l == nil || l.registry == nil {
		return nil
	}
	return l.registry.Tools.Lookup(name)
}

func (l *Loop) pruneTerminalDispatches() {
	state := l.mutableRuntime()
	if state == nil || len(state.dispatchIntents) == 0 {
		return
	}
	pendingOrder := make([]string, 0, len(state.dispatchOrder))
	for _, dispatchID := range state.dispatchOrder {
		intent, ok := state.dispatchIntents[dispatchID]
		if !ok {
			continue
		}
		if intent.Status == contract.ToolDispatchPending {
			pendingOrder = append(pendingOrder, dispatchID)
			continue
		}
		delete(state.dispatchIntents, dispatchID)
	}
	if len(state.dispatchIntents) == 0 {
		state.dispatchIntents = nil
		state.dispatchOrder = nil
		return
	}
	state.dispatchOrder = pendingOrder
}

func (l *Loop) recoverUnreconciledDispatches(cause string) string {
	var pending []string
	for _, dispatchID := range l.mutableRuntime().dispatchOrder {
		intent, ok := l.mutableRuntime().dispatchIntents[dispatchID]
		if ok && intent.Status == contract.ToolDispatchPending {
			pending = append(pending, dispatchID)
		}
	}
	if len(pending) == 0 {
		return ""
	}

	intent := l.mutableRuntime().dispatchIntents[pending[0]]
	verification := cloneVerificationSpec(intent.Verification)
	clearVerificationInitialDelay(verification)
	call := pendingCallFromIntent(intent, l.lookupTool(intent.Call.Name))
	call.Verification = verification
	result := dispatchTerminalResult("unknown", "uncertain", cause, "")
	result["error"] = "tool dispatch result was not committed"
	if strings.EqualFold(strings.TrimSpace(intent.ModifiesResource), "no") {
		result["status"] = "failed"
		result["execution_state"] = "not_confirmed"
		intent.Status = contract.ToolDispatchCancelled
	} else {
		intent.Status = contract.ToolDispatchUncertain
	}
	l.mutableRuntime().dispatchIntents[intent.ID] = intent
	if err := l.appendToolObservation(call, result); err != nil {
		l.setAttemptStatus(intent.AttemptID, contract.AttemptUnknown)
	}
	l.cancelUninvokedDispatches(
		pending[1:],
		"dispatches after an unreconciled invocation were never started",
		intent.ID,
	)
	if l.mutableRuntime().pendingMutationVerification != nil {
		l.transitionMutationVerification()
		l.queueResponseDirective(l.mutableRuntime().pendingMutationVerification.requiredMessage())
		return "A mutating command may have executed, but its result could not be committed. The runtime will not repeat it automatically and requires read-only verification."
	}
	if l.controlState() == RuntimeControlAwaitingToolResult {
		l.transitionControl(RuntimeControlAwaitingModelStep)
	}
	return "A tool dispatch result could not be committed. The unresolved dispatch was closed without automatic re-execution."
}

func clearVerificationInitialDelay(spec *contract.VerificationSpec) {
	if spec == nil {
		return
	}
	spec.InitialDelaySeconds = 0
	for i := range spec.Checks {
		spec.Checks[i].InitialDelaySeconds = 0
	}
}

func stepRefValue(ref *contract.StepRef) contract.StepRef {
	if ref == nil {
		return contract.StepRef{}
	}
	return *ref
}
