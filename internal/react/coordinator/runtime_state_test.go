package coordinator

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
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

func TestRuntimeStateAuditAllowsMutationVerificationToPrecedeRequestedReport(t *testing.T) {
	snapshot := RuntimeSnapshot{
		Lifecycle: LoopLifecycleModelTurn,
		Control:   RuntimeControlAwaitingMutationVerificationEvidence,
		PendingMutationVerification: &pendingMutationVerification{
			Checks: []verificationRuntimeCheck{{
				ID:     "verification-1",
				Status: contract.VerificationCheckActive,
			}},
		},
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because mutation verification control takes precedence", got)
	}
	if got := snapshot.Control; got != RuntimeControlAwaitingMutationVerificationEvidence {
		t.Fatalf("control = %s, want %s", got, RuntimeControlAwaitingMutationVerificationEvidence)
	}
}

func TestProjectRuntimeSnapshotUsesDetachedStateOnly(t *testing.T) {
	state := runtimeState{
		control:       RuntimeControlAwaitingModelStep,
		originalQuery: "inspect pods",
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{{
				Index: 1,
				Name:  "observation_execution",
			}},
			Completed: map[int]bool{},
		},
	}

	snapshot := projectRuntimeSnapshot(&state, 7)

	if snapshot.Revision != 7 || snapshot.Control != RuntimeControlAwaitingModelStep {
		t.Fatalf("snapshot revision/control = %d/%s", snapshot.Revision, snapshot.Control)
	}
	snapshot.Phase.Completed[1] = true
	if state.phaseStepState.Completed[1] {
		t.Fatal("projected phase state shares storage with runtime root")
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
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingFinalReport,
			pendingMutationVerification: &pendingMutationVerification{
				Checks: []verificationRuntimeCheck{{ID: "direct"}},
			},
		},
	}

	if got := loop.RuntimeSnapshot().AuditError(); got == "" {
		t.Fatal("final report and pending mutation verification must not coexist")
	}
}

func TestRuntimeStateAuditRejectsVerificationShapeControlMismatch(t *testing.T) {
	loop := &Loop{runtimeState: &runtimeState{
		control: RuntimeControlAwaitingMutationVerificationChainEvidence,
		pendingMutationVerification: &pendingMutationVerification{
			Shape: contract.VerificationSingle,
			Checks: []verificationRuntimeCheck{{
				ID:     "verification-1",
				Status: contract.VerificationCheckActive,
			}},
		},
	}}
	if got := loop.RuntimeSnapshot().AuditError(); got == "" {
		t.Fatal("single verification was accepted under chain evidence control")
	}
}

func TestTransitionControlDerivesLifecycle(t *testing.T) {
	loop := &Loop{runtimeState: &runtimeState{
		control:      RuntimeControlAwaitingModelStep,
		pendingCalls: []PendingCall{{}},
	}}
	loop.transitionControl(RuntimeControlAwaitingApproval)
	if loop.loopLifecycle() != LoopLifecycleWaitingApproval {
		t.Fatalf("lifecycle = %v, want approval wait", loop.loopLifecycle())
	}

	loop.mutableRuntime().pendingMutationVerification = &pendingMutationVerification{
		Checks: []verificationRuntimeCheck{{ID: "direct", Status: contract.VerificationCheckActive}},
	}
	loop.transitionControl(RuntimeControlAwaitingMutationVerificationEvidence)
	if loop.loopLifecycle() != LoopLifecycleModelTurn {
		t.Fatalf("lifecycle = %v, want model turn", loop.loopLifecycle())
	}
}

func TestRuntimeSnapshotProjectsPhaseAndSteps(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingMutationVerificationChainEvidence,
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
				Shape:       contract.VerificationChain,
				ActiveIndex: 1,
				Checks: []verificationRuntimeCheck{
					{ID: "mutation_1_direct", ExpectedState: "check configmap", SuggestedCommand: "kubectl get configmap web -n app", Status: contract.VerificationCheckSatisfied},
					{ID: "mutation_1_chain_2", ExpectedState: "check rollout", SuggestedCommand: "kubectl rollout status deployment/web -n app", Status: contract.VerificationCheckActive},
				},
			},
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
	if len(snapshot.ActiveSteps) != 2 {
		t.Fatalf("active steps len = %d, want 2", len(snapshot.ActiveSteps))
	}
	if got := snapshot.ActiveSteps[0].Ref.Kind; got != StepMutationEvidenceRequirement {
		t.Fatalf("first step kind = %s, want %s", got, StepMutationEvidenceRequirement)
	}
	if got := snapshot.ActiveSteps[0].Status; got != StepCompleted {
		t.Fatalf("verification check 1 status = %s, want %s", got, StepCompleted)
	}
	if got := snapshot.ActiveSteps[1].Status; got != StepActive {
		t.Fatalf("verification check 2 status = %s, want %s", got, StepActive)
	}
	if projected := loop.activeProjectedStepRef(); projected == nil ||
		projected.Kind != StepMutationEvidenceRequirement ||
		projected.ID != "mutation_1_chain_2" {
		t.Fatalf("projected active step = %#v, want active mutation verification check", projected)
	}
}

func TestRuntimeSnapshotProjectsPendingCallAsEphemeralGeneralAction(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
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
		runtimeState: &runtimeState{
			control:                      RuntimeControlAwaitingFinalReport,
			pendingCalls:                 []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
			pendingResponseDirective:     "final_report",
			pendingFinalReport:           &finalReport{},
			pendingNextDirections:        &nextDirections{},
			pendingDirectionPrompt:       &directionPromptState{},
			mutationContinuationAttempts: 2,
			pendingMutationVerification:  &pendingMutationVerification{},
		},
	}

	loop.applyRuntimeCleanup(cleanupExitPolicy())

	if len(loop.mutableRuntime().pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.mutableRuntime().pendingCalls)
	}
	if loop.mutableRuntime().pendingResponseDirective != "" {
		t.Fatalf("response directive still set: %q", loop.mutableRuntime().pendingResponseDirective)
	}
	if loop.mutableRuntime().pendingFinalReport != nil || loop.mutableRuntime().pendingNextDirections != nil || loop.mutableRuntime().pendingDirectionPrompt != nil {
		t.Fatalf("direction lifecycle still set")
	}
	if loop.mutableRuntime().mutationContinuationAttempts != 0 {
		t.Fatalf("mutation continuation attempts = %d, want 0", loop.mutableRuntime().mutationContinuationAttempts)
	}
	if loop.mutableRuntime().pendingMutationVerification == nil {
		t.Fatal("exit cleanup should not discard mutation verification evidence obligation")
	}
}

func TestApprovalDeclinedCleanupPreservesResponseDirectives(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control:                  RuntimeControlAwaitingFinalReport,
			pendingCalls:             []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
			pendingResponseDirective: "return final_report",
		},
	}

	loop.applyRuntimeCleanup(cleanupApprovalDeclinedPolicy())

	if len(loop.mutableRuntime().pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.mutableRuntime().pendingCalls)
	}
	if loop.mutableRuntime().control != RuntimeControlAwaitingFinalReport || loop.mutableRuntime().pendingResponseDirective != "return final_report" {
		t.Fatalf("approval cleanup must preserve control and directive: control=%s directive=%q", loop.mutableRuntime().control, loop.mutableRuntime().pendingResponseDirective)
	}
}

func TestDirectionCleanupPreservesPendingCalls(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control:                     RuntimeControlAwaitingMutationContinuation,
			pendingCalls:                []PendingCall{{FunctionCall: gollm.FunctionCall{Name: "kubectl"}}},
			pendingResponseDirective:    "next_directions",
			pendingFinalReport:          &finalReport{},
			pendingNextDirections:       &nextDirections{},
			pendingDirectionPrompt:      &directionPromptState{},
			pendingMutationVerification: &pendingMutationVerification{},
		},
	}

	loop.applyRuntimeCleanup(cleanupDirectionPromptPolicy())

	if len(loop.mutableRuntime().pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %#v, want preserved", loop.mutableRuntime().pendingCalls)
	}
	if loop.mutableRuntime().pendingFinalReport != nil || loop.mutableRuntime().pendingNextDirections != nil || loop.mutableRuntime().pendingDirectionPrompt != nil {
		t.Fatalf("direction lifecycle still set")
	}
	if loop.mutableRuntime().pendingMutationVerification == nil || loop.mutableRuntime().control != RuntimeControlAwaitingMutationContinuation {
		t.Fatalf("verification lifecycle should be preserved")
	}
	if loop.mutableRuntime().pendingResponseDirective != "" {
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
			loop: &Loop{
				runtimeState: &runtimeState{
					control: RuntimeControlAwaitingApproval,
				},
			},
		},
		{
			name: "direction choice without prompt",
			loop: &Loop{
				runtimeState: &runtimeState{
					control: RuntimeControlAwaitingContinuationChoice,
				},
			},
		},
		{
			name: "direction text with stale choice prompt",
			loop: &Loop{
				runtimeState: &runtimeState{
					control:                RuntimeControlAwaitingContinuationText,
					pendingDirectionPrompt: &directionPromptState{},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.loop.output = make(chan *api.Message, 1)
			handled, err := tt.loop.auditRuntimeState()
			if err != nil {
				t.Fatalf("auditRuntimeState() error = %v", err)
			}
			if !handled {
				t.Fatal("expected audit to handle invalid waiting lifecycle")
			}
			if tt.loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
				t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput", tt.loop.loopLifecycle())
			}
		})
	}
}
