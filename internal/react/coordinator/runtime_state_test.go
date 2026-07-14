package coordinator

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

func TestInputDispatchDecisionTable(t *testing.T) {
	tests := []struct {
		name     string
		control  RuntimeControlState
		input    string
		accepted bool
		handler  InputHandlerKind
	}{
		{
			name:     "continuation choice accepts number",
			control:  RuntimeControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
			handler:  InputHandlerReactChoice,
		},
		{
			name:     "continuation choice rejects approval token",
			control:  RuntimeControlAwaitingContinuationChoice,
			input:    "y",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "choice rejects slash meta",
			control:  RuntimeControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "text accepts slash meta",
			control:  RuntimeControlAwaitingContinuationText,
			input:    "/help",
			accepted: true,
			handler:  InputHandlerOrchestratorMeta,
		},
		{
			name:     "text accepts free text",
			control:  RuntimeControlAwaitingContinuationText,
			input:    "네임스페이스가 달라",
			accepted: true,
			handler:  InputHandlerReactText,
		},
		{
			name:     "approval rejects slash meta",
			control:  RuntimeControlAwaitingApproval,
			input:    "/readonly status",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "user query accepts slash meta",
			control:  RuntimeControlAwaitingUserQuery,
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
		control: RuntimeControlAwaitingNextDirections,
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
		Lifecycle:                   LoopLifecycleModelTurn,
		Control:                     RuntimeControlAwaitingMutationVerificationEvidence,
		PendingMutationVerification: &pendingMutationVerification{},
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because mutation verification control takes precedence", got)
	}
	if got := snapshot.Control; got != RuntimeControlAwaitingMutationVerificationEvidence {
		t.Fatalf("control = %s, want %s", got, RuntimeControlAwaitingMutationVerificationEvidence)
	}
}

func TestRuntimeStateControlLetsGuidedPhaseProgressPrecedeFinalReport(t *testing.T) {
	snapshot := RuntimeSnapshot{
		Lifecycle: LoopLifecycleModelTurn,
		Control:   RuntimeControlAwaitingGuidedPhaseProgress,
		Phase: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps:        []phaseStep{{Index: 1, Name: "guided_diagnosis"}},
			Completed:         map[int]bool{},
		},
		Guide: &guideStepState{
			TotalSteps: 1,
			Completed:  map[int]bool{1: true},
		},
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because requested output precedence handles it", got)
	}
	if got := snapshot.Control; got != RuntimeControlAwaitingGuidedPhaseProgress {
		t.Fatalf("control = %s, want %s", got, RuntimeControlAwaitingGuidedPhaseProgress)
	}
}

func TestRuntimeStateAuditRejectsFinalReportWithPendingVerification(t *testing.T) {
	loop := &Loop{
		control: RuntimeControlAwaitingFinalReport,
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{{ID: "direct"}},
		},
	}

	if got := loop.RuntimeSnapshot().AuditError(); got == "" {
		t.Fatal("final report and pending mutation verification must not coexist")
	}
}

func TestTransitionControlDerivesLifecycle(t *testing.T) {
	loop := &Loop{}
	loop.transitionControl(RuntimeControlAwaitingApproval)
	if loop.loopLifecycle() != LoopLifecycleWaitingApproval {
		t.Fatalf("lifecycle = %v, want approval wait", loop.loopLifecycle())
	}

	loop.transitionControl(RuntimeControlAwaitingMutationVerificationEvidence)
	if loop.loopLifecycle() != LoopLifecycleModelTurn {
		t.Fatalf("lifecycle = %v, want model turn", loop.loopLifecycle())
	}
}

func TestTransitionAfterToolFailureLeavesNoExecutingControl(t *testing.T) {
	loop := &Loop{}
	loop.transitionControl(RuntimeControlExecutingTool)
	loop.transitionAfterToolFailure()
	if loop.control != RuntimeControlAwaitingModelStep {
		t.Fatalf("control = %s, want model step after tool failure", loop.control)
	}

	loop.transitionControl(RuntimeControlAwaitingMutationVerificationEvidence)
	loop.transitionAfterToolFailure()
	if loop.control != RuntimeControlAwaitingMutationVerificationEvidence {
		t.Fatalf("tool failure recovery must preserve a more specific control, got %s", loop.control)
	}
}

func TestRefreshInputOwnerPublishesExecutingToolSnapshot(t *testing.T) {
	loop := &Loop{
		control: RuntimeControlExecutingTool,
	}
	loop.refreshInputOwner()
	snapshot, ok := loop.PublishedRuntimeSnapshot()
	if !ok {
		t.Fatal("expected published snapshot")
	}
	if snapshot.Control != RuntimeControlExecutingTool {
		t.Fatalf("control = %s, want %s", snapshot.Control, RuntimeControlExecutingTool)
	}
}

func TestRuntimeSnapshotProjectsPhaseAndSteps(t *testing.T) {
	loop := &Loop{
		control: RuntimeControlAwaitingModelStep,
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
		control: RuntimeControlAwaitingApproval,
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
		control:                      RuntimeControlAwaitingFinalReport,
		pendingCalls:                 []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
		pendingResponseDirective:     "final_report",
		pendingFinalReport:           &finalReport{},
		pendingNextDirections:        &nextDirections{},
		pendingDirectionPrompt:       &directionPromptState{},
		mutationContinuationAttempts: 2,
		pendingMutationVerification:  &pendingMutationVerification{},
	}

	loop.applyRuntimeCleanup(cleanupExitPolicy())

	if len(loop.pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.pendingCalls)
	}
	if loop.pendingResponseDirective != "" {
		t.Fatalf("response directive still set: %q", loop.pendingResponseDirective)
	}
	if loop.pendingFinalReport != nil || loop.pendingNextDirections != nil || loop.pendingDirectionPrompt != nil {
		t.Fatalf("direction lifecycle still set")
	}
	if loop.mutationContinuationAttempts != 0 {
		t.Fatalf("mutation continuation attempts = %d, want 0", loop.mutationContinuationAttempts)
	}
	if loop.pendingMutationVerification == nil {
		t.Fatal("exit cleanup should not discard mutation verification evidence obligation")
	}
}

func TestApprovalDeclinedCleanupPreservesResponseDirectives(t *testing.T) {
	loop := &Loop{
		control:                  RuntimeControlAwaitingFinalReport,
		pendingCalls:             []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
		pendingResponseDirective: "return final_report",
	}

	loop.applyRuntimeCleanup(cleanupApprovalDeclinedPolicy())

	if len(loop.pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.pendingCalls)
	}
	if loop.control != RuntimeControlAwaitingFinalReport || loop.pendingResponseDirective != "return final_report" {
		t.Fatalf("approval cleanup must preserve control and directive: control=%s directive=%q", loop.control, loop.pendingResponseDirective)
	}
}

func TestDirectionCleanupPreservesPendingCalls(t *testing.T) {
	loop := &Loop{
		control:                     RuntimeControlAwaitingMutationContinuation,
		pendingCalls:                []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
		pendingResponseDirective:    "next_directions",
		pendingFinalReport:          &finalReport{},
		pendingNextDirections:       &nextDirections{},
		pendingDirectionPrompt:      &directionPromptState{},
		pendingMutationVerification: &pendingMutationVerification{},
	}

	loop.applyRuntimeCleanup(cleanupDirectionPromptPolicy())

	if len(loop.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %#v, want preserved", loop.pendingCalls)
	}
	if loop.pendingFinalReport != nil || loop.pendingNextDirections != nil || loop.pendingDirectionPrompt != nil {
		t.Fatalf("direction lifecycle still set")
	}
	if loop.pendingMutationVerification == nil || loop.control != RuntimeControlAwaitingMutationContinuation {
		t.Fatalf("verification lifecycle should be preserved")
	}
	if loop.pendingResponseDirective != "" {
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
			loop: &Loop{control: RuntimeControlAwaitingApproval},
		},
		{
			name: "direction choice without prompt",
			loop: &Loop{control: RuntimeControlAwaitingContinuationChoice},
		},
		{
			name: "direction text with stale choice prompt",
			loop: &Loop{
				control:                RuntimeControlAwaitingContinuationText,
				pendingDirectionPrompt: &directionPromptState{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.loop.output = make(chan *api.Message, 1)
			if !tt.loop.auditRuntimeState() {
				t.Fatal("expected audit to handle invalid waiting lifecycle")
			}
			if tt.loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
				t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput", tt.loop.loopLifecycle())
			}
		})
	}
}

func TestNextDirectionsRequiredGateBlocksPlainAnswer(t *testing.T) {
	loop := &Loop{
		control: RuntimeControlAwaitingNextDirections,
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
	if loop.loopLifecycle() != LoopLifecycleModelTurn {
		t.Fatalf("lifecycle = %v, want LoopLifecycleModelTurn", loop.loopLifecycle())
	}
	if !strings.Contains(loop.pendingResponseDirective, "next_directions") {
		t.Fatalf("expected next_directions directive, got %q", loop.pendingResponseDirective)
	}
}

func TestNextDirectionsRequiredGateStopsAfterRepeatedPlainAnswer(t *testing.T) {
	loop := &Loop{
		control: RuntimeControlAwaitingNextDirections,
		output:  make(chan *api.Message, 1),
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
	if loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
		t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput", loop.loopLifecycle())
	}
}
