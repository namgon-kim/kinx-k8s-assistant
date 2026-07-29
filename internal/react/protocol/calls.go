package protocol

import (
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

const (
	ResourceGuideLookupCall        = "__resource_guide_lookup__"
	RequestContextCall             = "__request_context__"
	RequirementAnalysisCall        = "__requirement_analysis__"
	PhasePlanCall                  = "__phase_plan__"
	PhasePlanRevisionCall          = "__phase_plan_revision__"
	StepResultCall                 = "__step_result__"
	PhaseProgressCall              = "__phase_progress__"
	GuideProgressCall              = "__guide_progress__"
	FinalReportCall                = "__final_report__"
	NextDirectionsCall             = "__next_directions__"
	MutationVerificationResultCall = "__mutation_verification_result__"
	ContinuationHandoffCall        = "__continuation_handoff__"
	InvalidActionCall              = "__invalid_action__"
	InvalidStructuredOutputCall    = "__invalid_structured_output__"
)

func IsRuntimeInternalCall(name string) bool {
	return InternalStructuredCallName(name) != ""
}

func NormalizeAssistantStructuredFunctionCalls(calls []gollm.FunctionCall) []gollm.FunctionCall {
	if len(calls) == 0 {
		return calls
	}
	normalized := make([]gollm.FunctionCall, 0, len(calls))
	for _, call := range calls {
		if internalName := InternalStructuredCallName(call.Name); internalName != "" {
			call.Name = internalName
		}
		normalized = append(normalized, call)
	}
	return normalized
}

func InternalStructuredCallName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case BareInternalCallName(RequirementAnalysisCall), RequirementAnalysisCall:
		return RequirementAnalysisCall
	case BareInternalCallName(RequestContextCall), RequestContextCall:
		return RequestContextCall
	case BareInternalCallName(PhasePlanCall), PhasePlanCall:
		return PhasePlanCall
	case BareInternalCallName(PhasePlanRevisionCall), PhasePlanRevisionCall:
		return PhasePlanRevisionCall
	case BareInternalCallName(StepResultCall), StepResultCall:
		return StepResultCall
	case BareInternalCallName(PhaseProgressCall), PhaseProgressCall:
		return PhaseProgressCall
	case BareInternalCallName(GuideProgressCall), GuideProgressCall:
		return GuideProgressCall
	case BareInternalCallName(ResourceGuideLookupCall), ResourceGuideLookupCall:
		return ResourceGuideLookupCall
	case BareInternalCallName(FinalReportCall), FinalReportCall:
		return FinalReportCall
	case BareInternalCallName(NextDirectionsCall), NextDirectionsCall:
		return NextDirectionsCall
	case BareInternalCallName(MutationVerificationResultCall), MutationVerificationResultCall:
		return MutationVerificationResultCall
	case BareInternalCallName(ContinuationHandoffCall), ContinuationHandoffCall:
		return ContinuationHandoffCall
	default:
		return ""
	}
}

func BareInternalCallName(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(name), "__"), "__")
}
