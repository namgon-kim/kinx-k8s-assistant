package gate

import (
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type OutcomeKind string

const (
	ModelOutputCorrection OutcomeKind = "model_output_correction"
	AgentCommandRetry     OutcomeKind = "agent_command_retry"
	UserRequestBlocked    OutcomeKind = "user_request_blocked"
	PolicyBlock           OutcomeKind = "policy_block"
	ToolExecutionFailure  OutcomeKind = "tool_execution_failure"
	RetrievalResultGate   OutcomeKind = "retrieval_result_gate"
	ApprovalRequired      OutcomeKind = "approval_required"
	HumanInputRequired    OutcomeKind = "human_input_required"
	ExternalStateWait     OutcomeKind = "external_state_wait"
	HardInvariant         OutcomeKind = "hard_invariant"
)

type RetryScope string

const (
	RetryNone          RetryScope = ""
	RetryCurrentStep   RetryScope = "current_step"
	RetryCurrentPhase  RetryScope = "current_phase"
	RetryAgentCommand  RetryScope = "agent_command"
	RetryUserRequest   RetryScope = "user_request"
	RetryExternalState RetryScope = "external_state"
)

type BranchPolicy string

const (
	StayCurrent      BranchPolicy = "stay_current"
	RetryStep        BranchPolicy = "retry_step"
	RecheckStep      BranchPolicy = "recheck_step"
	SkipStep         BranchPolicy = "skip_step"
	MovePhase        BranchPolicy = "move_phase"
	RewindPhase      BranchPolicy = "rewind_phase"
	BlockUserRequest BranchPolicy = "block_user_request"
)

type Outcome struct {
	Allow           bool
	Kind            OutcomeKind
	Code            string
	ExpectedControl contract.RuntimeControlState
	TargetPhase     *contract.PhaseRef
	TargetStep      *contract.StepRef
	Retryable       bool
	RetryScope      RetryScope
	UserVisible     bool
	UserMessage     string
	ModelCorrection string
	CorrectionMode  CorrectionMode
	BranchPolicy    BranchPolicy
}

func (o Outcome) Validate(snapshot contract.RuntimeSnapshot) error {
	if o.Allow {
		return nil
	}
	if o.TargetPhase != nil && !hasPhase(snapshot, *o.TargetPhase) {
		return fmt.Errorf("target phase does not exist")
	}
	if o.TargetStep != nil && !hasStep(snapshot, *o.TargetStep) {
		return fmt.Errorf("target step does not exist")
	}
	if o.BranchPolicy == SkipStep && o.TargetStep == nil {
		return fmt.Errorf("skip_step requires target step")
	}
	return nil
}

func hasPhase(snapshot contract.RuntimeSnapshot, target contract.PhaseRef) bool {
	if snapshot.Phase == nil {
		return false
	}
	for _, phase := range snapshot.Phase.Phases {
		if phase.Ref.Index == target.Index || strings.EqualFold(phase.Ref.Name, target.Name) {
			return true
		}
	}
	return false
}

func hasStep(snapshot contract.RuntimeSnapshot, target contract.StepRef) bool {
	for _, step := range snapshot.ActiveSteps {
		if step.Ref.Kind == target.Kind && (target.ID == "" || step.Ref.ID == target.ID) && (target.Index == 0 || step.Ref.Index == target.Index) {
			return true
		}
	}
	return false
}
