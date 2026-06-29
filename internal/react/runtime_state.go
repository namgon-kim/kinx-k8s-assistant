package react

import "strings"

type ControlState string

const (
	ControlIdle                                 ControlState = "idle"
	ControlAwaitingUserQuery                    ControlState = "awaiting_user_query"
	ControlAwaitingRequirementAnalysis          ControlState = "awaiting_requirement_analysis"
	ControlAwaitingPhasePlan                    ControlState = "awaiting_phase_plan"
	ControlAwaitingModelStep                    ControlState = "awaiting_model_step"
	ControlAwaitingResourceGuideLookup          ControlState = "awaiting_resource_guide_lookup"
	ControlAwaitingGuidedDiagnosisStep          ControlState = "awaiting_guided_diagnosis_step"
	ControlAwaitingGuidedPhaseProgress          ControlState = "awaiting_guided_phase_progress"
	ControlAwaitingFinalReport                  ControlState = "awaiting_final_report"
	ControlAwaitingNextDirections               ControlState = "awaiting_next_directions"
	ControlAwaitingApproval                     ControlState = "awaiting_approval"
	ControlExecutingTool                        ControlState = "executing_tool"
	ControlAwaitingMutationVerificationEvidence ControlState = "awaiting_mutation_verification_evidence"
	ControlAwaitingMutationVerificationResult   ControlState = "awaiting_mutation_verification_result"
	ControlAwaitingMutationContinuation         ControlState = "awaiting_mutation_continuation"
	ControlAwaitingContinuationChoice           ControlState = "awaiting_continuation_choice"
	ControlAwaitingContinuationText             ControlState = "awaiting_continuation_text"
	ControlComplete                             ControlState = "complete"
	ControlExited                               ControlState = "exited"
)

type RuntimeSnapshot struct {
	LoopState State
	Control   ControlState

	OriginalQuery string

	Requirement            *requirementAnalysis
	Request                *requestContext
	ResourceClassification *resourceClassification

	Phase *phaseStepState
	Guide *guideStepState

	PendingCalls                   []PendingCall
	PendingMutationVerification    *pendingMutationVerification
	MutationContinuationRequired   bool
	PendingFinalReport             *finalReport
	PendingNextDirections          *nextDirections
	PendingDirectionPrompt         *directionPromptState
	PendingDirective               string
	ResourceGuideInjected          bool
	GuidedPhaseProgressRequested   bool
	FinalReportRequested           bool
	RequiresResourceGuideLookupNow bool
}

func (l *Loop) RuntimeSnapshot() RuntimeSnapshot {
	if l == nil {
		return RuntimeSnapshot{Control: ControlIdle}
	}
	snapshot := RuntimeSnapshot{
		LoopState:                      l.state,
		OriginalQuery:                  strings.TrimSpace(l.originalQuery),
		Requirement:                    l.requirementAnalysis,
		Request:                        l.requestContext,
		ResourceClassification:         l.resourceClassification,
		Phase:                          l.phaseStepState,
		Guide:                          l.guideStepState,
		PendingCalls:                   append([]PendingCall(nil), l.pendingCalls...),
		PendingMutationVerification:    l.pendingMutationVerification,
		MutationContinuationRequired:   l.mutationContinuationRequired,
		PendingFinalReport:             l.pendingFinalReport,
		PendingNextDirections:          l.pendingNextDirections,
		PendingDirectionPrompt:         l.pendingDirectionPrompt,
		PendingDirective:               strings.TrimSpace(l.pendingResponseDirective),
		ResourceGuideInjected:          l.resourceGuideInjected,
		GuidedPhaseProgressRequested:   l.guidedPhaseProgressRequested,
		FinalReportRequested:           l.finalReportRequested,
		RequiresResourceGuideLookupNow: l.phaseStepRequiresResourceGuideLookup(),
	}
	snapshot.Control = snapshot.deriveControl()
	return snapshot
}

func (s RuntimeSnapshot) deriveControl() ControlState {
	switch {
	case s.LoopState == StateExited:
		return ControlExited
	case s.LoopState == StateWaitingApproval:
		return ControlAwaitingApproval
	case s.LoopState == StateWaitingDirectionChoice:
		return ControlAwaitingContinuationChoice
	case s.LoopState == StateWaitingDirectionText:
		return ControlAwaitingContinuationText
	case s.LoopState == StateIdle || s.LoopState == StateDone:
		return ControlAwaitingUserQuery
	case s.PendingMutationVerification != nil && s.PendingMutationVerification.AwaitingResult:
		return ControlAwaitingMutationVerificationResult
	case s.PendingMutationVerification != nil:
		return ControlAwaitingMutationVerificationEvidence
	case s.MutationContinuationRequired:
		return ControlAwaitingMutationContinuation
	case s.GuidedPhaseProgressRequested:
		return ControlAwaitingGuidedPhaseProgress
	case s.FinalReportRequested:
		return ControlAwaitingFinalReport
	case s.PendingFinalReport != nil && s.PendingNextDirections == nil && s.PendingDirectionPrompt == nil:
		return ControlAwaitingNextDirections
	case s.Requirement == nil:
		return ControlAwaitingRequirementAnalysis
	case s.Phase == nil:
		return ControlAwaitingPhasePlan
	case s.RequiresResourceGuideLookupNow:
		return ControlAwaitingResourceGuideLookup
	case s.Guide != nil && len(s.Guide.remainingSteps()) > 0:
		return ControlAwaitingGuidedDiagnosisStep
	default:
		return ControlAwaitingModelStep
	}
}

func (s RuntimeSnapshot) DerivedInputOwner() InputOwner {
	switch s.Control {
	case ControlAwaitingApproval:
		return InputOwnerApproval
	case ControlAwaitingContinuationChoice:
		return InputOwnerReactChoice
	case ControlAwaitingContinuationText:
		return InputOwnerReactText
	default:
		return InputOwnerOrchestrator
	}
}

func (s RuntimeSnapshot) ShouldEmitAnchor() bool {
	return s.Requirement != nil ||
		s.Phase != nil ||
		s.PendingMutationVerification != nil ||
		s.Guide != nil ||
		s.PendingDirective != "" ||
		s.MutationContinuationRequired ||
		s.PendingFinalReport != nil ||
		s.Control == ControlAwaitingRequirementAnalysis ||
		s.Control == ControlAwaitingPhasePlan
}

func (s RuntimeSnapshot) ActiveGate() string {
	switch s.Control {
	case ControlAwaitingMutationVerificationResult:
		return "mutation_verification_result_required"
	case ControlAwaitingMutationVerificationEvidence:
		return "mutation_verification_evidence_required"
	case ControlAwaitingMutationContinuation:
		return "mutation_continuation_required"
	case ControlAwaitingGuidedPhaseProgress:
		return "guided_diagnosis_phase_progress_required"
	case ControlAwaitingFinalReport:
		return "final_report_required"
	case ControlAwaitingNextDirections:
		return "next_directions_required"
	case ControlAwaitingRequirementAnalysis:
		return "requirement_analysis_required"
	case ControlAwaitingPhasePlan:
		return "phase_plan_required"
	case ControlAwaitingResourceGuideLookup:
		return "resource_guide_lookup_required"
	default:
		return "none"
	}
}

func (s RuntimeSnapshot) RequiredNextOutput() string {
	switch s.Control {
	case ControlAwaitingMutationVerificationResult:
		return "mutation_verification_result"
	case ControlAwaitingMutationVerificationEvidence:
		return "one read-only action satisfying a remaining mutation evidence requirement"
	case ControlAwaitingMutationContinuation:
		return "next best action based on verification evidence"
	case ControlAwaitingGuidedPhaseProgress:
		return "phase_progress"
	case ControlAwaitingFinalReport:
		return "final_report"
	case ControlAwaitingNextDirections:
		return "next_directions"
	case ControlAwaitingRequirementAnalysis:
		return "requirement_analysis"
	case ControlAwaitingPhasePlan:
		return "phase_plan"
	case ControlAwaitingResourceGuideLookup:
		return "resource_guide_lookup"
	case ControlAwaitingGuidedDiagnosisStep:
		return "action for the next guide step, or guide_progress after useful evidence is already observed"
	default:
		if s.Phase != nil {
			return "action or phase_progress according to the current phase completion condition"
		}
		return "valid structured output for the current request"
	}
}

func (s RuntimeSnapshot) ForbiddenNextOutputs() []string {
	switch s.Control {
	case ControlAwaitingMutationVerificationResult:
		return []string{"action", "final_report", "phase_progress", "next_directions", "answer"}
	case ControlAwaitingMutationVerificationEvidence:
		return []string{"mutating action", "final_report", "phase_progress", "next_directions", "answer", "mutation_verification_result"}
	case ControlAwaitingMutationContinuation:
		return []string{"final_report", "phase_progress", "next_directions", "answer"}
	case ControlAwaitingGuidedPhaseProgress:
		return []string{"action", "final_report", "next_directions", "answer"}
	case ControlAwaitingFinalReport:
		return []string{"action", "phase_progress", "next_directions", "answer"}
	case ControlAwaitingNextDirections:
		return []string{"action", "phase_progress", "final_report", "answer"}
	case ControlAwaitingRequirementAnalysis:
		return []string{"action", "phase_plan", "phase_progress", "final_report", "next_directions", "answer"}
	case ControlAwaitingPhasePlan:
		return []string{"action", "phase_progress", "final_report", "next_directions", "answer"}
	case ControlAwaitingResourceGuideLookup:
		return []string{"action", "phase_progress", "guide_progress", "final_report", "next_directions", "answer"}
	default:
		return nil
	}
}

func (s RuntimeSnapshot) NestedStateName() string {
	if s.PendingMutationVerification != nil {
		if s.PendingMutationVerification.AwaitingResult {
			return "mutation_verification_result"
		}
		return "mutation_verification_evidence"
	}
	if s.Guide != nil {
		if len(s.Guide.remainingSteps()) > 0 {
			return "resource_guide_steps"
		}
		return "resource_guide_steps_complete"
	}
	return "none"
}
