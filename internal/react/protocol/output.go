package protocol

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type OutputSupport string

const (
	OutputSupported   OutputSupport = "supported"
	OutputUnsupported OutputSupport = "unsupported"
	OutputUnknown     OutputSupport = "unknown"
)

type OutputClassification struct {
	Kind    contract.ModelOutputKind
	Support OutputSupport
	Name    string
}

var supportedStructuredOutputKinds = []contract.ModelOutputKind{
	contract.OutputRequirementAnalysis,
	contract.OutputPhasePlan,
	contract.OutputPhasePlanRevision,
	contract.OutputStepResult,
	contract.OutputPhaseProgress,
	contract.OutputResourceGuideLookup,
	contract.OutputGuideProgress,
	contract.OutputMutationVerificationResult,
	contract.OutputFinalReport,
	contract.OutputNextDirections,
	contract.OutputContinuationHandoff,
}

func SupportedStructuredOutputKinds() []contract.ModelOutputKind {
	return append([]contract.ModelOutputKind(nil), supportedStructuredOutputKinds...)
}

// ClassifyNativeCallName converts a native function name to a semantic output
// kind. Native non-internal names are external tool actions.
func ClassifyNativeCallName(name string) OutputClassification {
	return classifyStructuredName(name, true)
}

// ClassifyShimKey converts a top-level shim JSON key to a semantic output
// kind. Only the explicit action key represents an external tool action;
// unknown shim keys fail closed.
func ClassifyShimKey(name string) OutputClassification {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "action":
		return OutputClassification{
			Kind:    contract.OutputAction,
			Support: OutputSupported,
			Name:    "action",
		}
	case "answer":
		return OutputClassification{
			Kind:    contract.OutputPlainAnswer,
			Support: OutputSupported,
			Name:    "answer",
		}
	}
	return classifyStructuredName(name, false)
}

func classifyStructuredName(name string, externalActionFallback bool) OutputClassification {
	normalized := strings.ToLower(strings.TrimSpace(name))
	bare := BareInternalCallName(normalized)
	classification := OutputClassification{Name: normalized}

	switch bare {
	case BareInternalCallName(RequirementAnalysisCall), BareInternalCallName(RequestContextCall):
		classification.Kind = contract.OutputRequirementAnalysis
		classification.Support = OutputSupported
	case BareInternalCallName(PhasePlanCall):
		classification.Kind = contract.OutputPhasePlan
		classification.Support = OutputSupported
	case BareInternalCallName(StepResultCall):
		classification.Kind = contract.OutputStepResult
		classification.Support = OutputSupported
	case BareInternalCallName(PhaseProgressCall):
		classification.Kind = contract.OutputPhaseProgress
		classification.Support = OutputSupported
	case BareInternalCallName(PhasePlanRevisionCall):
		classification.Kind = contract.OutputPhasePlanRevision
		classification.Support = OutputSupported
	case BareInternalCallName(ResourceGuideLookupCall):
		classification.Kind = contract.OutputResourceGuideLookup
		classification.Support = OutputSupported
	case BareInternalCallName(GuideProgressCall):
		classification.Kind = contract.OutputGuideProgress
		classification.Support = OutputSupported
	case BareInternalCallName(MutationVerificationResultCall):
		classification.Kind = contract.OutputMutationVerificationResult
		classification.Support = OutputSupported
	case BareInternalCallName(FinalReportCall):
		classification.Kind = contract.OutputFinalReport
		classification.Support = OutputSupported
	case BareInternalCallName(NextDirectionsCall):
		classification.Kind = contract.OutputNextDirections
		classification.Support = OutputSupported
	case BareInternalCallName(ContinuationHandoffCall):
		classification.Kind = contract.OutputContinuationHandoff
		classification.Support = OutputSupported
	case "":
		classification.Support = OutputUnknown
	default:
		if kind, declared := declaredStructuredOutputKind(bare); declared {
			classification.Kind = kind
			classification.Support = OutputUnsupported
			break
		}
		if !externalActionFallback || strings.HasPrefix(normalized, "__") && strings.HasSuffix(normalized, "__") {
			classification.Support = OutputUnknown
			break
		}
		classification.Kind = contract.OutputAction
		classification.Support = OutputSupported
	}
	return classification
}

func declaredStructuredOutputKind(name string) (contract.ModelOutputKind, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, kind := range contract.AllModelOutputKinds() {
		if kind == contract.OutputAction || kind == contract.OutputPlainAnswer {
			continue
		}
		if name == strings.ToLower(kind.String()) {
			return kind, true
		}
	}
	return "", false
}

func ClassifyPlainAnswer(text string) OutputClassification {
	classification := OutputClassification{
		Kind:    contract.OutputPlainAnswer,
		Support: OutputSupported,
		Name:    contract.OutputPlainAnswer.String(),
	}
	if strings.TrimSpace(text) == "" {
		classification.Support = OutputUnknown
	}
	return classification
}
