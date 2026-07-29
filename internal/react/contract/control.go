package contract

// ControlExecutionClass determines who owns the next runtime transition.
type ControlExecutionClass string

const (
	ControlExecutionInvalid      ControlExecutionClass = "invalid"
	ControlExecutionModelTurn    ControlExecutionClass = "model_turn"
	ControlExecutionNonModelTurn ControlExecutionClass = "non_model_turn"
)

var runtimeControlStates = []RuntimeControlState{
	RuntimeControlUnset,
	RuntimeControlAwaitingUserQuery,
	RuntimeControlAwaitingRequirementAnalysis,
	RuntimeControlAwaitingPhasePlan,
	RuntimeControlAwaitingModelStep,
	RuntimeControlAwaitingResourceGuideLookup,
	RuntimeControlAwaitingGuidedDiagnosisStep,
	RuntimeControlAwaitingGuidedPhaseProgress,
	RuntimeControlAwaitingFinalReport,
	RuntimeControlAwaitingNextDirections,
	RuntimeControlAwaitingApproval,
	RuntimeControlAwaitingToolResult,
	RuntimeControlAwaitingMutationVerificationEvidence,
	RuntimeControlAwaitingMutationVerificationResult,
	RuntimeControlAwaitingMutationVerificationChainEvidence,
	RuntimeControlAwaitingMutationVerificationChainResult,
	RuntimeControlAwaitingMutationContinuation,
	RuntimeControlAwaitingContinuationHandoff,
	RuntimeControlAwaitingContinuationChoice,
	RuntimeControlAwaitingContinuationText,
	RuntimeControlExited,
}

// AllRuntimeControlStates returns the complete declared control-state set.
func AllRuntimeControlStates() []RuntimeControlState {
	return append([]RuntimeControlState(nil), runtimeControlStates...)
}

// ClassifyRuntimeControlState returns the owner class for a known control
// state. Unknown values are rejected instead of falling through to a model
// turn.
func ClassifyRuntimeControlState(state RuntimeControlState) (ControlExecutionClass, bool) {
	switch state {
	case RuntimeControlUnset:
		return ControlExecutionInvalid, true
	case RuntimeControlAwaitingRequirementAnalysis,
		RuntimeControlAwaitingPhasePlan,
		RuntimeControlAwaitingModelStep,
		RuntimeControlAwaitingResourceGuideLookup,
		RuntimeControlAwaitingGuidedDiagnosisStep,
		RuntimeControlAwaitingGuidedPhaseProgress,
		RuntimeControlAwaitingFinalReport,
		RuntimeControlAwaitingNextDirections,
		RuntimeControlAwaitingMutationVerificationEvidence,
		RuntimeControlAwaitingMutationVerificationResult,
		RuntimeControlAwaitingMutationVerificationChainEvidence,
		RuntimeControlAwaitingMutationVerificationChainResult,
		RuntimeControlAwaitingMutationContinuation,
		RuntimeControlAwaitingContinuationHandoff:
		return ControlExecutionModelTurn, true
	case RuntimeControlAwaitingUserQuery,
		RuntimeControlAwaitingApproval,
		RuntimeControlAwaitingToolResult,
		RuntimeControlAwaitingContinuationChoice,
		RuntimeControlAwaitingContinuationText,
		RuntimeControlExited:
		return ControlExecutionNonModelTurn, true
	default:
		return "", false
	}
}

func IsModelTurnControl(state RuntimeControlState) bool {
	class, ok := ClassifyRuntimeControlState(state)
	return ok && class == ControlExecutionModelTurn
}
