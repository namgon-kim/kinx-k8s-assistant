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
	ExternalStateWait     OutcomeKind = "external_state_wait"
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
	Allow bool
	Kind  OutcomeKind
	Code  string

	ExpectedControl contract.RuntimeControlState
	TargetPhase     *contract.PhaseRef
	TargetStep      *contract.StepRef

	Retryable  bool
	RetryScope RetryScope

	UserVisible     bool
	UserMessage     string
	ModelCorrection string

	CorrectionMode CorrectionMode
	BranchPolicy   BranchPolicy
}

type ValidationContext struct {
	HasTargetPhase bool
	HasTargetStep  bool
}

func Validate(outcome Outcome, context ValidationContext) error {
	if outcome.Allow {
		return nil
	}
	if outcome.TargetPhase != nil && !context.HasTargetPhase {
		return fmt.Errorf("target phase does not exist: %s", outcome.TargetPhase.String())
	}
	if outcome.TargetStep != nil && !context.HasTargetStep {
		return fmt.Errorf("target step does not exist: %s", outcome.TargetStep.String())
	}
	if outcome.BranchPolicy != SkipStep {
		return nil
	}
	if outcome.TargetStep == nil {
		return fmt.Errorf("branch policy %s requires target step", outcome.BranchPolicy)
	}
	return validateSkippableStep(*outcome.TargetStep)
}

func validateSkippableStep(ref contract.StepRef) error {
	switch ref.Kind {
	case contract.StepResourceGuideDiagnostic:
		if ref.Index <= 0 {
			return fmt.Errorf("branch policy %s requires a positive guide step index", SkipStep)
		}
		return nil
	case contract.StepMutationEvidenceRequirement:
		if strings.TrimSpace(ref.ID) == "" {
			return fmt.Errorf("branch policy %s requires a mutation evidence requirement id", SkipStep)
		}
		return nil
	case contract.StepGeneralAction:
		return fmt.Errorf("branch policy %s does not support general action steps", SkipStep)
	default:
		return fmt.Errorf("branch policy %s does not support step kind %q", SkipStep, ref.Kind)
	}
}

func AssertExpectedControl(expected, actual contract.RuntimeControlState) error {
	if expected == "" || expected == actual {
		return nil
	}
	return fmt.Errorf("expected control %s, got %s", expected, actual)
}
