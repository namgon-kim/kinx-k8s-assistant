package contract

// ModelOutputKind is the provider-neutral semantic class of one model output.
// Transport call names and shim JSON keys are normalized to this type by the
// protocol package.
type ModelOutputKind string

func (k ModelOutputKind) String() string {
	return string(k)
}

const (
	OutputRequirementAnalysis        ModelOutputKind = "requirement_analysis"
	OutputPhasePlan                  ModelOutputKind = "phase_plan"
	OutputAction                     ModelOutputKind = "action"
	OutputStepResult                 ModelOutputKind = "step_result"
	OutputPhaseProgress              ModelOutputKind = "phase_progress"
	OutputPhasePlanRevision          ModelOutputKind = "phase_plan_revision"
	OutputResourceGuideLookup        ModelOutputKind = "resource_guide_lookup"
	OutputGuideProgress              ModelOutputKind = "guide_progress"
	OutputMutationVerificationResult ModelOutputKind = "mutation_verification_result"
	OutputFinalReport                ModelOutputKind = "final_report"
	OutputNextDirections             ModelOutputKind = "next_directions"
	OutputContinuationHandoff        ModelOutputKind = "continuation_handoff"
	OutputPlainAnswer                ModelOutputKind = "plain_answer"
)

var modelOutputKinds = []ModelOutputKind{
	OutputRequirementAnalysis,
	OutputPhasePlan,
	OutputAction,
	OutputStepResult,
	OutputPhaseProgress,
	OutputPhasePlanRevision,
	OutputResourceGuideLookup,
	OutputGuideProgress,
	OutputMutationVerificationResult,
	OutputFinalReport,
	OutputNextDirections,
	OutputContinuationHandoff,
	OutputPlainAnswer,
}

func AllModelOutputKinds() []ModelOutputKind {
	return append([]ModelOutputKind(nil), modelOutputKinds...)
}

func IsStateChangingOutput(kind ModelOutputKind) bool {
	switch kind {
	case OutputRequirementAnalysis,
		OutputPhasePlan,
		OutputStepResult,
		OutputPhaseProgress,
		OutputPhasePlanRevision,
		OutputResourceGuideLookup,
		OutputGuideProgress,
		OutputMutationVerificationResult,
		OutputFinalReport,
		OutputNextDirections,
		OutputContinuationHandoff:
		return true
	default:
		return false
	}
}

// ModelOutputEnvelope is the provider-neutral representation of one complete
// model response. Protocol adapters populate it without mutating session state.
type ModelOutputEnvelope struct {
	ProgressText    string
	PlainAnswer     string
	InternalEvents  []ModelOutputEvent
	ExternalActions []ActionProposal
	InvalidOutputs  []InvalidOutput
}

type ModelOutputEvent struct {
	Kind ModelOutputKind
	Call FunctionCall
}

type ActionProposal struct {
	Call         FunctionCall
	StepRef      *StepRef
	BoundStepRef *StepRef
}

type InvalidOutput struct {
	Name   string
	Reason string
}

func (e ModelOutputEnvelope) Kinds() []ModelOutputKind {
	kinds := make([]ModelOutputKind, 0, len(e.InternalEvents)+len(e.ExternalActions)+1)
	for _, event := range e.InternalEvents {
		kinds = append(kinds, event.Kind)
	}
	if len(e.ExternalActions) > 0 {
		kinds = append(kinds, OutputAction)
	}
	if e.PlainAnswer != "" {
		kinds = append(kinds, OutputPlainAnswer)
	}
	return kinds
}
