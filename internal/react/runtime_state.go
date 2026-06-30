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

type UserInputKind string

const (
	InputChoiceNumber UserInputKind = "choice_number"
	InputApproval     UserInputKind = "approval"
	InputSlashMeta    UserInputKind = "slash_meta"
	InputFreeText     UserInputKind = "free_text"
	InputEmpty        UserInputKind = "empty"
)

type InputHandlerKind string

const (
	InputHandlerNone             InputHandlerKind = "none"
	InputHandlerOrchestratorMeta InputHandlerKind = "orchestrator_meta"
	InputHandlerReactChoice      InputHandlerKind = "react_choice"
	InputHandlerReactText        InputHandlerKind = "react_text"
	InputHandlerReactApproval    InputHandlerKind = "react_approval"
	InputHandlerUserQuery        InputHandlerKind = "user_query"
)

type InputDispatchDecision struct {
	Kind     UserInputKind
	Accepted bool
	Handler  InputHandlerKind
	Reason   string
}

func ClassifyUserInput(input string) UserInputKind {
	trimmed := strings.TrimSpace(input)
	switch {
	case trimmed == "":
		return InputEmpty
	case strings.HasPrefix(trimmed, "/"):
		return InputSlashMeta
	case isChoiceNumber(trimmed):
		return InputChoiceNumber
	case isApprovalToken(trimmed):
		return InputApproval
	default:
		return InputFreeText
	}
}

func DecideInputDispatch(control ControlState, kind UserInputKind) InputDispatchDecision {
	decision := InputDispatchDecision{Kind: kind, Handler: InputHandlerNone}
	switch control {
	case ControlAwaitingContinuationChoice:
		if kind == InputChoiceNumber || kind == InputApproval {
			decision.Accepted = true
			decision.Handler = InputHandlerReactChoice
			return decision
		}
		decision.Reason = "choice prompt accepts a number only"
		return decision
	case ControlAwaitingApproval:
		if kind == InputChoiceNumber || kind == InputApproval {
			decision.Accepted = true
			decision.Handler = InputHandlerReactApproval
			return decision
		}
		decision.Reason = "approval prompt accepts approval choices only"
		return decision
	case ControlAwaitingContinuationText:
		if kind == InputSlashMeta {
			decision.Accepted = true
			decision.Handler = InputHandlerOrchestratorMeta
			return decision
		}
		if kind == InputFreeText || kind == InputChoiceNumber || kind == InputApproval || kind == InputEmpty {
			decision.Accepted = true
			decision.Handler = InputHandlerReactText
			return decision
		}
	case ControlAwaitingUserQuery:
		if kind == InputSlashMeta {
			decision.Accepted = true
			decision.Handler = InputHandlerOrchestratorMeta
			return decision
		}
		if kind == InputFreeText || kind == InputChoiceNumber || kind == InputApproval || kind == InputEmpty {
			decision.Accepted = true
			decision.Handler = InputHandlerUserQuery
			return decision
		}
	}
	decision.Reason = "current state does not accept user input of this type"
	return decision
}

func isChoiceNumber(input string) bool {
	for _, r := range input {
		if r < '0' || r > '9' {
			return false
		}
	}
	return input != ""
}

func isApprovalToken(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes", "n", "no", "예", "아니오":
		return true
	default:
		return false
	}
}

// RuntimeSnapshot is a shallow, same-goroutine projection of Loop control
// state. It centralizes state interpretation for prompts and diagnostics; it
// is not an immutable deep copy for cross-goroutine use.
type RuntimeSnapshot struct {
	LoopState  State
	Control    ControlState
	InputOwner InputOwner

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
	ToolDispatchInProgress         bool
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
		ToolDispatchInProgress:         l.toolDispatchInProgress,
	}
	snapshot.Control = snapshot.deriveControl()
	snapshot.InputOwner = snapshot.DerivedInputOwner()
	return snapshot
}

func (l *Loop) PublishedRuntimeSnapshot() (RuntimeSnapshot, bool) {
	if l == nil {
		return RuntimeSnapshot{Control: ControlIdle, InputOwner: InputOwnerOrchestrator}, false
	}
	raw := l.runtimeSnapshot.Load()
	if raw == nil {
		return RuntimeSnapshot{}, false
	}
	snapshot, ok := raw.(RuntimeSnapshot)
	return snapshot, ok
}

func (l *Loop) publishRuntimeSnapshot() RuntimeSnapshot {
	snapshot := l.RuntimeSnapshot()
	l.runtimeSnapshot.Store(snapshot)
	return snapshot
}

func (s RuntimeSnapshot) deriveControl() ControlState {
	switch {
	case s.LoopState == StateExited:
		return ControlExited
	case s.ToolDispatchInProgress:
		return ControlExecutingTool
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

func (s RuntimeSnapshot) AuditError() string {
	switch {
	case s.PendingMutationVerification != nil && (s.FinalReportRequested || s.GuidedPhaseProgressRequested):
		return "mutation verification is pending while final_report/phase_progress is also requested"
	case s.FinalReportRequested && s.GuidedPhaseProgressRequested:
		return "final_report and guided phase_progress are both requested"
	case s.LoopState == StateWaitingDirectionText && s.PendingDirectionPrompt != nil:
		return "direction free-text state still has a pending choice prompt"
	case s.LoopState == StateWaitingDirectionChoice && s.PendingDirectionPrompt == nil:
		return "direction choice state has no pending direction prompt"
	case s.LoopState == StateWaitingApproval && len(s.PendingCalls) == 0:
		return "approval state has no pending calls"
	default:
		return ""
	}
}
