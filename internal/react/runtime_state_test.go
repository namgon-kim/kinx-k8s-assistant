package react

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

func TestInputDispatchDecisionTable(t *testing.T) {
	tests := []struct {
		name     string
		control  ControlState
		input    string
		accepted bool
		handler  InputHandlerKind
	}{
		{
			name:     "continuation choice accepts number",
			control:  ControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
			handler:  InputHandlerReactChoice,
		},
		{
			name:     "continuation choice rejects approval token",
			control:  ControlAwaitingContinuationChoice,
			input:    "y",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "choice rejects slash meta",
			control:  ControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "text accepts slash meta",
			control:  ControlAwaitingContinuationText,
			input:    "/help",
			accepted: true,
			handler:  InputHandlerOrchestratorMeta,
		},
		{
			name:     "text accepts free text",
			control:  ControlAwaitingContinuationText,
			input:    "네임스페이스가 달라",
			accepted: true,
			handler:  InputHandlerReactText,
		},
		{
			name:     "approval rejects slash meta",
			control:  ControlAwaitingApproval,
			input:    "/readonly status",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "user query accepts slash meta",
			control:  ControlAwaitingUserQuery,
			input:    "/readonly status",
			accepted: true,
			handler:  InputHandlerOrchestratorMeta,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := DecideInputDispatch(tt.control, ClassifyUserInput(tt.input))
			if decision.Accepted != tt.accepted || decision.Handler != tt.handler {
				t.Fatalf("decision = %#v, want accepted=%v handler=%s", decision, tt.accepted, tt.handler)
			}
		})
	}
}

func TestNextDirectionsRequiredGateBlocksOtherStructuredOutput(t *testing.T) {
	loop := &Loop{
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.enforceRequestedStructuredDirective([]gollm.FunctionCall{{Name: "kubectl"}}) {
		t.Fatal("next_directions_required gate should block action")
	}
	if !strings.Contains(loop.pendingResponseDirective, "next_directions") {
		t.Fatalf("expected next_directions directive, got %q", loop.pendingResponseDirective)
	}
}

func TestRuntimeStateAuditAllowsMutationVerificationToPrecedeRequestedReport(t *testing.T) {
	snapshot := RuntimeSnapshot{
		LoopState:                    StateRunning,
		PendingMutationVerification:  &pendingMutationVerification{},
		FinalReportRequested:         true,
		GuidedPhaseProgressRequested: false,
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because mutation verification control takes precedence", got)
	}
	if got := snapshot.deriveControl(); got != ControlAwaitingMutationVerificationEvidence {
		t.Fatalf("control = %s, want %s", got, ControlAwaitingMutationVerificationEvidence)
	}
}

func TestRuntimeStateControlLetsGuidedPhaseProgressPrecedeFinalReport(t *testing.T) {
	snapshot := RuntimeSnapshot{
		LoopState:                    StateRunning,
		FinalReportRequested:         true,
		GuidedPhaseProgressRequested: true,
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because requested output precedence handles it", got)
	}
	if got := snapshot.deriveControl(); got != ControlAwaitingGuidedPhaseProgress {
		t.Fatalf("control = %s, want %s", got, ControlAwaitingGuidedPhaseProgress)
	}
}

func TestRefreshInputOwnerPublishesExecutingToolSnapshot(t *testing.T) {
	loop := &Loop{
		state:                  StateRunning,
		toolDispatchInProgress: true,
	}
	loop.refreshInputOwner()
	snapshot, ok := loop.PublishedRuntimeSnapshot()
	if !ok {
		t.Fatal("expected published snapshot")
	}
	if snapshot.Control != ControlExecutingTool {
		t.Fatalf("control = %s, want %s", snapshot.Control, ControlExecutingTool)
	}
}

func TestRuntimeSnapshotProjectsPhaseAndSteps(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			RequestGoal:       "fix web app",
			CurrentPhaseIndex: 2,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "lightweight_lookup", Goal: "inspect", CompletionCondition: "evidence collected"},
				{
					Index:               2,
					Name:                "guided_diagnosis",
					Goal:                "diagnose",
					CompletionCondition: "guide completed",
					Steps: []phaseExecutionStep{
						{
							ID:              "inspect_pods",
							Kind:            "observation",
							Description:     "Inspect pod state before following guide details",
							Command:         "kubectl get pods -n app",
							ExpectedOutcome: "pod state is visible",
						},
					},
				},
			},
			Completed: map[int]bool{1: true},
		},
		guideStepState: &guideStepState{
			TotalSteps: 2,
			StepDetails: []guideStepDetail{
				{Index: 1, Description: "check pods", RenderedCommand: "kubectl get pods", ExpectedOutcome: "pods listed"},
				{Index: 2, Description: "check events", RenderedCommand: "kubectl get events", ExpectedOutcome: "events listed"},
			},
			Completed: map[int]bool{1: true},
		},
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{
				{ID: "mutation_1_direct", Kind: "direct_effect", Purpose: "check configmap", SuggestedCommand: "kubectl get configmap web -n app"},
				{ID: "mutation_1_outcome", Kind: "outcome", Purpose: "check rollout", SuggestedCommand: "kubectl rollout status deployment/web -n app"},
			},
			Satisfied: map[string]bool{"mutation_1_direct": true},
		},
	}
	snapshot := loop.RuntimeSnapshot()
	if snapshot.PhaseRuntime == nil {
		t.Fatal("expected phase runtime projection")
	}
	if snapshot.PhaseRuntime.Active != (PhaseRef{Index: 2, Name: "guided_diagnosis"}) {
		t.Fatalf("active phase = %#v, want guided_diagnosis", snapshot.PhaseRuntime.Active)
	}
	if got := snapshot.PhaseRuntime.Phases[0].Status; got != PhaseCompleted {
		t.Fatalf("phase 1 status = %s, want %s", got, PhaseCompleted)
	}
	if got := snapshot.PhaseRuntime.Phases[1].Status; got != PhaseActive {
		t.Fatalf("phase 2 status = %s, want %s", got, PhaseActive)
	}
	if len(snapshot.PhaseRuntime.Phases[1].Steps) != 1 {
		t.Fatalf("declared phase steps len = %d, want 1", len(snapshot.PhaseRuntime.Phases[1].Steps))
	}
	if got := snapshot.PhaseRuntime.Phases[1].Steps[0].Ref.Kind; got != StepExplicitPhase {
		t.Fatalf("declared phase step kind = %s, want %s", got, StepExplicitPhase)
	}
	if got := snapshot.PhaseRuntime.Phases[1].Steps[0].Status; got != StepPending {
		t.Fatalf("declared phase step status = %s, want %s", got, StepPending)
	}
	if len(snapshot.ActiveSteps) != 4 {
		t.Fatalf("active steps len = %d, want 4", len(snapshot.ActiveSteps))
	}
	if got := snapshot.ActiveSteps[0].Ref.Kind; got != StepResourceGuideDiagnostic {
		t.Fatalf("first step kind = %s, want %s", got, StepResourceGuideDiagnostic)
	}
	if got := snapshot.ActiveSteps[0].Status; got != StepCompleted {
		t.Fatalf("guide step 1 status = %s, want %s", got, StepCompleted)
	}
	if got := snapshot.ActiveSteps[1].Status; got != StepActive {
		t.Fatalf("guide step 2 status = %s, want %s", got, StepActive)
	}
	if got := snapshot.ActiveSteps[2].Ref.ID; got != "mutation_1_direct" {
		t.Fatalf("mutation step id = %q, want mutation_1_direct", got)
	}
	if got := snapshot.ActiveSteps[2].Status; got != StepCompleted {
		t.Fatalf("mutation direct status = %s, want %s", got, StepCompleted)
	}
	if got := snapshot.ActiveSteps[3].Status; got != StepActive {
		t.Fatalf("mutation outcome status = %s, want %s", got, StepActive)
	}
}

func TestRuntimeSnapshotProjectsPendingCallAsEphemeralGeneralAction(t *testing.T) {
	loop := &Loop{
		state: StateWaitingApproval,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps:        []phaseStep{{Index: 1, Name: "remediation_execution"}},
			Completed:         map[int]bool{},
		},
		pendingCalls: []PendingCall{
			{FunctionCall: gollm.FunctionCall{
				Name:      "kubectl",
				Arguments: map[string]any{"command": "kubectl rollout restart deployment/web -n app"},
			}},
		},
	}
	snapshot := loop.RuntimeSnapshot()
	if len(snapshot.ActiveSteps) != 1 {
		t.Fatalf("active steps len = %d, want 1", len(snapshot.ActiveSteps))
	}
	step := snapshot.ActiveSteps[0]
	if step.Ref.Kind != StepGeneralAction {
		t.Fatalf("step kind = %s, want %s", step.Ref.Kind, StepGeneralAction)
	}
	if step.Ref.Phase.Name != "remediation_execution" {
		t.Fatalf("step phase = %#v, want remediation_execution", step.Ref.Phase)
	}
	if step.Command != "kubectl rollout restart deployment/web -n app" {
		t.Fatalf("step command = %q", step.Command)
	}
}

func TestRuntimeCleanupPoliciesClearControlBoundaryState(t *testing.T) {
	loop := &Loop{
		pendingCalls:                 []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
		finalReportRequested:         true,
		guidedPhaseProgressRequested: true,
		pendingResponseDirective:     "final_report",
		pendingFinalReport:           &finalReport{},
		pendingNextDirections:        &nextDirections{},
		pendingDirectionPrompt:       &directionPromptState{},
		mutationContinuationRequired: true,
		mutationContinuationAttempts: 2,
		pendingMutationVerification:  &pendingMutationVerification{},
		toolDispatchInProgress:       true,
	}

	loop.applyRuntimeCleanup(cleanupExitPolicy())

	if len(loop.pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.pendingCalls)
	}
	if loop.finalReportRequested || loop.guidedPhaseProgressRequested || loop.pendingResponseDirective != "" {
		t.Fatalf("response directives still set: final=%v guided=%v directive=%q", loop.finalReportRequested, loop.guidedPhaseProgressRequested, loop.pendingResponseDirective)
	}
	if loop.pendingFinalReport != nil || loop.pendingNextDirections != nil || loop.pendingDirectionPrompt != nil {
		t.Fatalf("direction state still set")
	}
	if loop.mutationContinuationRequired || loop.mutationContinuationAttempts != 0 {
		t.Fatalf("mutation continuation still set: required=%v attempts=%d", loop.mutationContinuationRequired, loop.mutationContinuationAttempts)
	}
	if loop.pendingMutationVerification == nil {
		t.Fatal("exit cleanup should not discard mutation verification evidence obligation")
	}
	if loop.toolDispatchInProgress {
		t.Fatal("tool dispatch flag should be cleared")
	}
}

func TestDirectionCleanupPreservesPendingCalls(t *testing.T) {
	loop := &Loop{
		pendingCalls:                 []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
		finalReportRequested:         true,
		pendingResponseDirective:     "next_directions",
		pendingFinalReport:           &finalReport{},
		pendingNextDirections:        &nextDirections{},
		pendingDirectionPrompt:       &directionPromptState{},
		pendingMutationVerification:  &pendingMutationVerification{},
		mutationContinuationRequired: true,
	}

	loop.applyRuntimeCleanup(cleanupDirectionPromptPolicy())

	if len(loop.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %#v, want preserved", loop.pendingCalls)
	}
	if loop.pendingFinalReport != nil || loop.pendingNextDirections != nil || loop.pendingDirectionPrompt != nil {
		t.Fatalf("direction state still set")
	}
	if loop.pendingMutationVerification == nil || !loop.mutationContinuationRequired {
		t.Fatalf("verification state should be preserved")
	}
	if loop.finalReportRequested || loop.pendingResponseDirective != "" {
		t.Fatalf("response directives still set")
	}
}

func TestAuditRuntimeStateHandlesWaitingStateInvariants(t *testing.T) {
	tests := []struct {
		name string
		loop *Loop
	}{
		{
			name: "approval without pending calls",
			loop: &Loop{state: StateWaitingApproval},
		},
		{
			name: "direction choice without prompt",
			loop: &Loop{state: StateWaitingDirectionChoice},
		},
		{
			name: "direction text with stale choice prompt",
			loop: &Loop{
				state:                  StateWaitingDirectionText,
				pendingDirectionPrompt: &directionPromptState{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.loop.output = make(chan *api.Message, 1)
			if !tt.loop.auditRuntimeState() {
				t.Fatal("expected audit to handle invalid waiting state")
			}
			if tt.loop.state != StateDone {
				t.Fatalf("state = %v, want StateDone", tt.loop.state)
			}
		})
	}
}

func TestNextDirectionsRequiredGateBlocksPlainAnswer(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("next_directions plain-answer gate should block text")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if !strings.Contains(loop.pendingResponseDirective, "next_directions") {
		t.Fatalf("expected next_directions directive, got %q", loop.pendingResponseDirective)
	}
}

func TestNextDirectionsRequiredGateStopsAfterRepeatedPlainAnswer(t *testing.T) {
	loop := &Loop{
		state:  StateRunning,
		output: make(chan *api.Message, 1),
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("first plain answer should be handled")
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("repeated plain answer should be handled")
	}
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone", loop.state)
	}
}
