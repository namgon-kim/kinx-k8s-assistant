package gate

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type MixPolicy string

const (
	MixOpen              MixPolicy = "open"
	MixGuarded           MixPolicy = "guarded"
	MixRequiredExclusive MixPolicy = "required_exclusive"
)

type OutputCondition string

const (
	ConditionResponsePhase OutputCondition = "response_phase"
)

type OutputPolicy struct {
	Control             contract.RuntimeControlState
	Allowed             []contract.ModelOutputKind
	Conditional         map[contract.ModelOutputKind]OutputCondition
	Mix                 MixPolicy
	Required            contract.ModelOutputKind
	StateEventExclusive bool
}

type OutputPolicyContext struct {
	AllowPlainAnswer           bool
	AllowLightweightPlanAction bool
	ExternalActionCount        int
}

type OutputDecision struct {
	Allow bool
	Code  string
}

var outputPolicies = map[contract.RuntimeControlState]OutputPolicy{
	contract.RuntimeControlAwaitingRequirementAnalysis: requiredPolicy(
		contract.RuntimeControlAwaitingRequirementAnalysis,
		contract.OutputRequirementAnalysis,
	),
	contract.RuntimeControlAwaitingPhasePlan: requiredPolicy(
		contract.RuntimeControlAwaitingPhasePlan,
		contract.OutputPhasePlan,
	),
	contract.RuntimeControlAwaitingModelStep: {
		Control: contract.RuntimeControlAwaitingModelStep,
		Allowed: []contract.ModelOutputKind{
			contract.OutputAction,
			contract.OutputStepResult,
			contract.OutputPhaseProgress,
			contract.OutputPhasePlanRevision,
			contract.OutputFinalReport,
		},
		Conditional: map[contract.ModelOutputKind]OutputCondition{
			contract.OutputPlainAnswer: ConditionResponsePhase,
		},
		Mix:                 MixOpen,
		StateEventExclusive: true,
	},
	contract.RuntimeControlAwaitingResourceGuideLookup: requiredPolicy(
		contract.RuntimeControlAwaitingResourceGuideLookup,
		contract.OutputResourceGuideLookup,
	),
	contract.RuntimeControlAwaitingGuidedDiagnosisStep: {
		Control: contract.RuntimeControlAwaitingGuidedDiagnosisStep,
		Allowed: []contract.ModelOutputKind{
			contract.OutputAction,
			contract.OutputGuideProgress,
			contract.OutputStepResult,
			contract.OutputPhasePlanRevision,
		},
		Mix:                 MixGuarded,
		StateEventExclusive: true,
	},
	contract.RuntimeControlAwaitingGuidedPhaseProgress: requiredPolicy(
		contract.RuntimeControlAwaitingGuidedPhaseProgress,
		contract.OutputPhaseProgress,
	),
	contract.RuntimeControlAwaitingMutationVerificationEvidence: {
		Control: contract.RuntimeControlAwaitingMutationVerificationEvidence,
		Allowed: []contract.ModelOutputKind{contract.OutputAction},
		Mix:     MixGuarded,
	},
	contract.RuntimeControlAwaitingMutationVerificationResult: requiredPolicy(
		contract.RuntimeControlAwaitingMutationVerificationResult,
		contract.OutputMutationVerificationResult,
	),
	contract.RuntimeControlAwaitingMutationVerificationChainEvidence: {
		Control: contract.RuntimeControlAwaitingMutationVerificationChainEvidence,
		Allowed: []contract.ModelOutputKind{contract.OutputAction},
		Mix:     MixGuarded,
	},
	contract.RuntimeControlAwaitingMutationVerificationChainResult: requiredPolicy(
		contract.RuntimeControlAwaitingMutationVerificationChainResult,
		contract.OutputMutationVerificationResult,
	),
	contract.RuntimeControlAwaitingMutationContinuation: {
		Control: contract.RuntimeControlAwaitingMutationContinuation,
		Allowed: []contract.ModelOutputKind{
			contract.OutputAction,
			contract.OutputPhasePlanRevision,
		},
		Mix:                 MixGuarded,
		StateEventExclusive: true,
	},
	contract.RuntimeControlAwaitingFinalReport: requiredPolicy(
		contract.RuntimeControlAwaitingFinalReport,
		contract.OutputFinalReport,
	),
	contract.RuntimeControlAwaitingNextDirections: requiredPolicy(
		contract.RuntimeControlAwaitingNextDirections,
		contract.OutputNextDirections,
	),
	contract.RuntimeControlAwaitingContinuationHandoff: requiredPolicy(
		contract.RuntimeControlAwaitingContinuationHandoff,
		contract.OutputContinuationHandoff,
	),
}

func requiredPolicy(control contract.RuntimeControlState, required contract.ModelOutputKind) OutputPolicy {
	return OutputPolicy{
		Control:  control,
		Allowed:  []contract.ModelOutputKind{required},
		Mix:      MixRequiredExclusive,
		Required: required,
	}
}

func AllOutputPolicyStates() []contract.RuntimeControlState {
	states := make([]contract.RuntimeControlState, 0, len(outputPolicies))
	for _, state := range contract.AllRuntimeControlStates() {
		if _, ok := outputPolicies[state]; ok {
			states = append(states, state)
		}
	}
	return states
}

func OutputPolicyFor(control contract.RuntimeControlState) (OutputPolicy, bool) {
	policy, ok := outputPolicies[control]
	if !ok {
		return OutputPolicy{}, false
	}
	policy.Allowed = append([]contract.ModelOutputKind(nil), policy.Allowed...)
	if policy.Conditional != nil {
		policy.Conditional = cloneConditions(policy.Conditional)
	}
	return policy, true
}

func ValidateOutputKinds(control contract.RuntimeControlState, kinds []contract.ModelOutputKind, context OutputPolicyContext) OutputDecision {
	policy, ok := outputPolicies[control]
	if !ok {
		return OutputDecision{Code: "unclassified_control_state"}
	}
	if len(kinds) == 0 {
		return OutputDecision{Code: "model_output_required"}
	}
	if hasDuplicateKinds(kinds) {
		return OutputDecision{Code: "duplicate_output_kind"}
	}
	if isLightweightPlanActionBundle(control, kinds) {
		if context.AllowLightweightPlanAction {
			return OutputDecision{Allow: true}
		}
		return OutputDecision{Code: "lightweight_plan_action_not_eligible"}
	}
	if policy.Mix == MixRequiredExclusive && (len(kinds) != 1 || kinds[0] != policy.Required) {
		return OutputDecision{Code: "required_output_exclusive"}
	}
	if policy.Mix == MixGuarded && containsKind(kinds, contract.OutputAction) && context.ExternalActionCount > 1 {
		return OutputDecision{Code: "multiple_guarded_actions"}
	}
	for _, kind := range kinds {
		if !knownOutputKind(kind) {
			return OutputDecision{Code: "unknown_output_kind"}
		}
		if containsKind(policy.Allowed, kind) {
			continue
		}
		condition, conditional := policy.Conditional[kind]
		if conditional && conditionSatisfied(condition, context) {
			continue
		}
		if conditional {
			return OutputDecision{Code: "output_condition_not_satisfied"}
		}
		return OutputDecision{Code: "output_not_allowed"}
	}
	if containsKind(kinds, contract.OutputPlainAnswer) && len(kinds) > 1 {
		return OutputDecision{Code: "plain_answer_mixed_with_structured_output"}
	}
	if policy.StateEventExclusive && containsKind(kinds, contract.OutputAction) && hasStateChangingOutput(kinds) {
		return OutputDecision{Code: "state_event_action_mixed"}
	}
	if countStateChangingOutputs(kinds) > 1 {
		return OutputDecision{Code: "multiple_state_events"}
	}
	return OutputDecision{Allow: true}
}

func cloneConditions(source map[contract.ModelOutputKind]OutputCondition) map[contract.ModelOutputKind]OutputCondition {
	result := make(map[contract.ModelOutputKind]OutputCondition, len(source))
	for kind, condition := range source {
		result[kind] = condition
	}
	return result
}

func knownOutputKind(kind contract.ModelOutputKind) bool {
	return containsKind(contract.AllModelOutputKinds(), kind)
}

func containsKind(kinds []contract.ModelOutputKind, target contract.ModelOutputKind) bool {
	for _, kind := range kinds {
		if kind == target {
			return true
		}
	}
	return false
}

func hasDuplicateKinds(kinds []contract.ModelOutputKind) bool {
	seen := make(map[contract.ModelOutputKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if _, ok := seen[kind]; ok {
			return true
		}
		seen[kind] = struct{}{}
	}
	return false
}

func hasStateChangingOutput(kinds []contract.ModelOutputKind) bool {
	return countStateChangingOutputs(kinds) != 0
}

func countStateChangingOutputs(kinds []contract.ModelOutputKind) int {
	count := 0
	for _, kind := range kinds {
		if contract.IsStateChangingOutput(kind) {
			count++
		}
	}
	return count
}

func conditionSatisfied(condition OutputCondition, context OutputPolicyContext) bool {
	switch condition {
	case ConditionResponsePhase:
		return context.AllowPlainAnswer
	default:
		return false
	}
}

func isLightweightPlanActionBundle(control contract.RuntimeControlState, kinds []contract.ModelOutputKind) bool {
	return control == contract.RuntimeControlAwaitingPhasePlan &&
		len(kinds) == 2 &&
		containsKind(kinds, contract.OutputPhasePlan) &&
		containsKind(kinds, contract.OutputAction)
}
