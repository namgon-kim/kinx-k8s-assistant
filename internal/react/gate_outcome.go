package react

import (
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"k8s.io/klog/v2"
)

type GateOutcomeKind string

const (
	GateOutcomeModelOutputCorrection GateOutcomeKind = "model_output_correction"
	GateOutcomeAgentCommandRetry     GateOutcomeKind = "agent_command_retry"
	GateOutcomeUserRequestBlocked    GateOutcomeKind = "user_request_blocked"
	GateOutcomePolicyBlock           GateOutcomeKind = "policy_block"
	GateOutcomeToolExecutionFailure  GateOutcomeKind = "tool_execution_failure"
	GateOutcomeRetrievalResultGate   GateOutcomeKind = "retrieval_result_gate"
	GateOutcomeApprovalRequired      GateOutcomeKind = "approval_required"
	GateOutcomeHumanInputRequired    GateOutcomeKind = "human_input_required"
	GateOutcomeExternalStateWait     GateOutcomeKind = "external_state_wait"
	GateOutcomeHardInvariant         GateOutcomeKind = "hard_invariant"
)

type RetryScope string

const (
	RetryScopeNone          RetryScope = ""
	RetryScopeCurrentStep   RetryScope = "current_step"
	RetryScopeCurrentPhase  RetryScope = "current_phase"
	RetryScopeAgentCommand  RetryScope = "agent_command"
	RetryScopeUserRequest   RetryScope = "user_request"
	RetryScopeExternalState RetryScope = "external_state"
)

type CorrectionMode string

const (
	CorrectionModeNone            CorrectionMode = "none"
	CorrectionModeAppendCompacted CorrectionMode = "append_compacted"
	CorrectionModeAppendPlain     CorrectionMode = "append_plain"
	CorrectionModeUserMessageOnly CorrectionMode = "user_message_only"
)

type BranchPolicy string

const (
	BranchStayCurrent      BranchPolicy = "stay_current"
	BranchRetryStep        BranchPolicy = "retry_step"
	BranchRecheckStep      BranchPolicy = "recheck_step"
	BranchSkipStep         BranchPolicy = "skip_step"
	BranchMovePhase        BranchPolicy = "move_phase"
	BranchRewindPhase      BranchPolicy = "rewind_phase"
	BranchBlockUserRequest BranchPolicy = "block_user_request"
)

type GateOutcome struct {
	Allow bool
	Kind  GateOutcomeKind
	Code  string

	ExpectedControl ControlState
	TargetPhase     *PhaseRef
	TargetStep      *StepRef

	Retryable  bool
	RetryScope RetryScope

	UserVisible     bool
	UserMessage     string
	ModelCorrection string

	CorrectionMode CorrectionMode
	BranchPolicy   BranchPolicy
}

func (o GateOutcome) Validate(snapshot RuntimeSnapshot) error {
	if o.Allow {
		return nil
	}
	if o.TargetPhase != nil && !snapshot.hasPhaseRef(*o.TargetPhase) {
		return fmt.Errorf("target phase does not exist: %s", o.TargetPhase.String())
	}
	if o.TargetStep != nil && !snapshot.hasStepRef(*o.TargetStep) {
		return fmt.Errorf("target step does not exist: %s", o.TargetStep.String())
	}
	if o.BranchPolicy == BranchSkipStep {
		if o.TargetStep == nil {
			return fmt.Errorf("branch policy %s requires target step", o.BranchPolicy)
		}
		if err := validateSkippableStepRef(*o.TargetStep); err != nil {
			return err
		}
	}
	return nil
}

func validateSkippableStepRef(ref StepRef) error {
	switch ref.Kind {
	case StepResourceGuideDiagnostic:
		if ref.Index <= 0 {
			return fmt.Errorf("branch policy %s requires a positive guide step index", BranchSkipStep)
		}
		return nil
	case StepMutationEvidenceRequirement:
		if strings.TrimSpace(ref.ID) == "" {
			return fmt.Errorf("branch policy %s requires a mutation evidence requirement id", BranchSkipStep)
		}
		return nil
	case StepGeneralAction:
		return fmt.Errorf("branch policy %s does not support general action steps", BranchSkipStep)
	default:
		return fmt.Errorf("branch policy %s does not support step kind %q", BranchSkipStep, ref.Kind)
	}
}

func (l *Loop) applyGateOutcome(outcome GateOutcome) bool {
	return l.applyGateOutcomeWithRepeatedCorrection(outcome, nil)
}

func (l *Loop) applyGateOutcomeWithRepeatedCorrection(outcome GateOutcome, repeated func(message string) bool) bool {
	if outcome.Allow {
		klog.V(2).InfoS("runtime gate outcome allowed", "code", outcome.Code, "kind", outcome.Kind)
		return false
	}
	klog.V(0).InfoS("runtime gate outcome blocking",
		"code", outcome.Code,
		"kind", outcome.Kind,
		"retryable", outcome.Retryable,
		"retry_scope", outcome.RetryScope,
		"branch_policy", outcome.BranchPolicy,
		"correction_mode", outcome.CorrectionMode,
	)
	if err := outcome.Validate(l.RuntimeSnapshot()); err != nil {
		klog.ErrorS(err, "runtime gate outcome validation failed", "code", outcome.Code, "kind", outcome.Kind)
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate outcome이 현재 runtime snapshot과 맞지 않아 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	message := strings.TrimSpace(outcome.ModelCorrection)
	if message == "" {
		message = strings.TrimSpace(outcome.UserMessage)
	}
	if message == "" {
		message = "Runtime gate blocked the previous model output. Choose a safe corrected next step."
	}
	code := strings.TrimSpace(outcome.Code)
	if code == "" {
		code = "runtime_gate_blocked"
	}
	mode := outcome.CorrectionMode
	if mode == "" {
		mode = CorrectionModeAppendCompacted
	}
	if mode != CorrectionModeUserMessageOnly && mode != CorrectionModeNone {
		appended := false
		if mode == CorrectionModeAppendPlain {
			appended = l.appendCorrection(code, message)
		} else {
			appended = l.appendCorrectionWithCompaction(code, message)
		}
		if !appended {
			klog.V(0).InfoS("runtime gate correction repeated", "code", code)
			if repeated != nil {
				return repeated(message)
			}
			userMessage := strings.TrimSpace(outcome.UserMessage)
			if userMessage == "" {
				userMessage = "runtime gate correction이 반복되어 루프를 중단했습니다."
			}
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, userMessage+"\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.state = StateDone
			return true
		}
	}
	if outcome.UserVisible && strings.TrimSpace(outcome.UserMessage) != "" {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, strings.TrimSpace(outcome.UserMessage))
	}
	if err := l.applyGateBranch(outcome); err != nil {
		klog.ErrorS(err, "runtime gate branch failed", "code", code, "branch_policy", outcome.BranchPolicy)
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate branch 적용 실패로 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
	// This is a post-condition assertion only. Branch side effects are not
	// rolled back on failure, so production gates should avoid ExpectedControl
	// unless the branch outcome is already safe to keep if the assertion fails.
	if err := outcome.AssertExpectedControl(l.RuntimeSnapshot()); err != nil {
		klog.ErrorS(err, "runtime gate expected control assertion failed", "code", code, "expected", outcome.ExpectedControl)
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate outcome 적용 후 control assertion이 맞지 않아 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	klog.V(1).InfoS("runtime gate outcome applied", "code", code, "next_iteration", l.currIteration+1)
	return true
}

func (o GateOutcome) AssertExpectedControl(snapshot RuntimeSnapshot) error {
	if o.ExpectedControl == "" || snapshot.Control == o.ExpectedControl {
		return nil
	}
	return fmt.Errorf("expected control %s, got %s", o.ExpectedControl, snapshot.Control)
}

func (l *Loop) applyGateBranch(outcome GateOutcome) error {
	switch outcome.BranchPolicy {
	case "", BranchStayCurrent, BranchRetryStep, BranchBlockUserRequest:
		return nil
	case BranchRecheckStep:
		return l.applyRecheckStepBranch(outcome)
	case BranchMovePhase:
		target, err := l.validateMovePhaseBranch(outcome)
		if err != nil {
			return err
		}
		return l.phaseStepState.moveToPhase(PhaseRef{Index: target.Index, Name: strings.TrimSpace(target.Name)})
	case BranchSkipStep:
		if outcome.TargetStep == nil {
			return fmt.Errorf("branch policy %s requires target step", outcome.BranchPolicy)
		}
		if !l.SkipStep(*outcome.TargetStep) {
			return fmt.Errorf("branch policy %s cannot skip target step %s", outcome.BranchPolicy, outcome.TargetStep.String())
		}
		if outcome.TargetStep.Kind == StepResourceGuideDiagnostic && l.guideStepState != nil && l.guideStepState.allCompleted() {
			l.requestPostGuideCompletionDirective()
		}
		return nil
	case BranchRewindPhase:
		target, err := l.validateRewindPhaseBranch(outcome)
		if err != nil {
			return err
		}
		targetRef, err := l.phaseStepState.rewindToPhase(PhaseRef{Index: target.Index, Name: strings.TrimSpace(target.Name)})
		if err != nil {
			return err
		}
		l.resetPhaseScopedState(targetRef, l.defaultPhaseScopedResetPolicy(targetRef))
		return nil
	default:
		return fmt.Errorf("unsupported branch policy %q", outcome.BranchPolicy)
	}
}

func (l *Loop) validateMovePhaseBranch(outcome GateOutcome) (phaseStep, error) {
	if outcome.TargetPhase == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires target phase", outcome.BranchPolicy)
	}
	if l == nil || l.phaseStepState == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires active phase state", outcome.BranchPolicy)
	}
	target, ok := l.phaseStepState.phaseStepForRef(*outcome.TargetPhase)
	if !ok {
		return phaseStep{}, fmt.Errorf("target phase does not exist: %s", outcome.TargetPhase.String())
	}
	if l.phaseStepState.Completed != nil && l.phaseStepState.Completed[target.Index] {
		return phaseStep{}, fmt.Errorf("target phase %s is already completed", PhaseRef{Index: target.Index, Name: target.Name}.String())
	}
	current := l.phaseStepState.currentStep()
	if current.Index == 0 {
		return phaseStep{}, fmt.Errorf("branch policy %s requires current phase", outcome.BranchPolicy)
	}
	if target.Index == current.Index {
		return target, nil
	}
	if target.Index < current.Index {
		return phaseStep{}, fmt.Errorf("branch policy %s cannot move backward from %s to %s; use %s with cleanup instead", outcome.BranchPolicy, PhaseRef{Index: current.Index, Name: current.Name}.String(), PhaseRef{Index: target.Index, Name: target.Name}.String(), BranchRewindPhase)
	}
	if l.phaseStepState.allowedNextIndex(current, target.Name) == target.Index {
		return target, nil
	}
	if l.runtimeAllowsPhaseMove(outcome, current, target) {
		return target, nil
	}
	return phaseStep{}, fmt.Errorf("target phase %s is not in allowed_next for current phase %s and no runtime override applies", PhaseRef{Index: target.Index, Name: target.Name}.String(), PhaseRef{Index: current.Index, Name: current.Name}.String())
}

func (l *Loop) validateRewindPhaseBranch(outcome GateOutcome) (phaseStep, error) {
	if outcome.TargetPhase == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires target phase", outcome.BranchPolicy)
	}
	if strings.TrimSpace(outcome.Code) == "" {
		return phaseStep{}, fmt.Errorf("branch policy %s requires source gate code for cleanup audit", outcome.BranchPolicy)
	}
	if l == nil || l.phaseStepState == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires active phase state", outcome.BranchPolicy)
	}
	target, ok := l.phaseStepState.phaseStepForRef(*outcome.TargetPhase)
	if !ok {
		return phaseStep{}, fmt.Errorf("target phase does not exist: %s", outcome.TargetPhase.String())
	}
	current := l.phaseStepState.currentStep()
	if current.Index == 0 {
		return phaseStep{}, fmt.Errorf("branch policy %s requires current phase", outcome.BranchPolicy)
	}
	if target.Index > current.Index {
		return phaseStep{}, fmt.Errorf("branch policy %s cannot rewind forward from %s to %s", outcome.BranchPolicy, PhaseRef{Index: current.Index, Name: current.Name}.String(), PhaseRef{Index: target.Index, Name: target.Name}.String())
	}
	return target, nil
}

func (l *Loop) runtimeAllowsPhaseMove(outcome GateOutcome, current, target phaseStep) bool {
	if strings.TrimSpace(outcome.Code) == "" {
		return false
	}
	switch outcome.Kind {
	case GateOutcomeExternalStateWait:
		return outcome.RetryScope == RetryScopeExternalState
	case GateOutcomeRetrievalResultGate:
		return outcome.RetryScope == RetryScopeCurrentPhase || outcome.RetryScope == RetryScopeCurrentStep
	case GateOutcomePolicyBlock, GateOutcomeAgentCommandRetry, GateOutcomeModelOutputCorrection:
		return target.Index == current.Index
	default:
		return false
	}
}

func (l *Loop) applyRecheckStepBranch(outcome GateOutcome) error {
	if l == nil || !l.mutationContinuationRequired {
		return fmt.Errorf("branch policy %s requires active external-state mutation continuation", outcome.BranchPolicy)
	}
	attempt, exhausted := l.consumeMutationContinuationAttempt()
	if exhausted {
		l.mutationContinuationRequired = false
		l.guidedPhaseProgressRequested = false
		l.finalReportRequested = true
		l.queueResponseDirective(fmt.Sprintf("External state recheck budget is exhausted after %d recheck attempts for source_gate=%s. Return final_report with conclusive=false, summarize the recheck attempts, and explain that the external state did not reach a resolved condition within the runtime budget.", attempt, strings.TrimSpace(outcome.Code)))
		return nil
	}
	l.queueResponseDirective(fmt.Sprintf("External state still needs verification for source_gate=%s. Continue with one read-only recheck action or a valid mutation_verification_result after the observation. recheck_attempt: %d/%d.", strings.TrimSpace(outcome.Code), attempt, maxMutationContinuationAttempts))
	return nil
}

func (l *Loop) applyModelOutputCorrectionGate(code, userMessage, correction string) bool {
	return l.applyModelOutputCorrectionGateWithMode(code, userMessage, correction, CorrectionModeAppendCompacted)
}

func (l *Loop) applyPlainModelOutputCorrectionGate(code, userMessage, correction string) bool {
	return l.applyModelOutputCorrectionGateWithMode(code, userMessage, correction, CorrectionModeAppendPlain)
}

func (l *Loop) applyModelOutputCorrectionGateWithMode(code, userMessage, correction string, mode CorrectionMode) bool {
	return l.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            code,
		Retryable:       true,
		RetryScope:      RetryScopeCurrentPhase,
		UserMessage:     userMessage,
		ModelCorrection: correction,
		CorrectionMode:  mode,
		BranchPolicy:    BranchStayCurrent,
	})
}

func (s RuntimeSnapshot) hasPhaseRef(ref PhaseRef) bool {
	if ref.Index == 0 && strings.TrimSpace(ref.Name) == "" {
		return false
	}
	if s.PhaseRuntime == nil {
		return false
	}
	for _, phase := range s.PhaseRuntime.Phases {
		if phase.Ref.matches(ref) {
			return true
		}
	}
	return false
}

func (s RuntimeSnapshot) hasStepRef(ref StepRef) bool {
	for _, step := range s.ActiveSteps {
		if step.Ref.matches(ref) {
			return true
		}
	}
	return false
}

func (r PhaseRef) matches(other PhaseRef) bool {
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	}
	if strings.TrimSpace(r.Name) != "" && strings.TrimSpace(other.Name) != "" && !strings.EqualFold(r.Name, other.Name) {
		return false
	}
	return (r.Index != 0 || strings.TrimSpace(r.Name) != "") &&
		(other.Index != 0 || strings.TrimSpace(other.Name) != "")
}

func (r StepRef) matches(other StepRef) bool {
	if r.Kind != "" && other.Kind != "" && r.Kind != other.Kind {
		return false
	}
	if r.ID != "" && other.ID != "" && r.ID != other.ID {
		return false
	}
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	}
	if (r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != "") &&
		(other.Phase.Index != 0 || strings.TrimSpace(other.Phase.Name) != "") &&
		!r.Phase.matches(other.Phase) {
		return false
	}
	return r.Kind != "" || r.ID != "" || r.Index != 0
}

func (r PhaseRef) String() string {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Sprintf("#%d", r.Index)
	}
	if r.Index == 0 {
		return strings.TrimSpace(r.Name)
	}
	return fmt.Sprintf("%s#%d", strings.TrimSpace(r.Name), r.Index)
}

func (r StepRef) String() string {
	parts := []string{string(r.Kind)}
	if r.ID != "" {
		parts = append(parts, "id="+r.ID)
	}
	if r.Index != 0 {
		parts = append(parts, fmt.Sprintf("index=%d", r.Index))
	}
	if r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != "" {
		parts = append(parts, "phase="+r.Phase.String())
	}
	return strings.Join(parts, " ")
}
