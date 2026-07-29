package coordinator

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestGateOutcomeValidateChecksTargetPhaseAndStep(t *testing.T) {
	snapshot := RuntimeSnapshot{
		Control: RuntimeControlAwaitingModelStep,
		PhaseRuntime: &PhaseRuntimeState{
			Active: PhaseRef{Index: 2, Name: "guided_diagnosis"},
			Phases: []PhaseSpec{
				{Ref: PhaseRef{Index: 1, Name: "lightweight_lookup"}},
				{Ref: PhaseRef{Index: 2, Name: "guided_diagnosis"}},
			},
		},
		ActiveSteps: []StepRuntimeState{
			{Ref: StepRef{
				Phase: PhaseRef{Index: 2, Name: "guided_diagnosis"},
				Kind:  StepResourceGuideDiagnostic,
				Index: 1,
			}},
		},
	}
	valid := GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		ExpectedControl: RuntimeControlAwaitingModelStep,
		TargetPhase:     &PhaseRef{Name: "guided_diagnosis"},
		TargetStep:      &StepRef{Kind: StepResourceGuideDiagnostic, Index: 1},
	}
	if err := valid.Validate(snapshot); err != nil {
		t.Fatalf("valid outcome rejected: %v", err)
	}
	missingPhase := valid
	missingPhase.TargetPhase = &PhaseRef{Name: "missing"}
	if err := missingPhase.Validate(snapshot); err == nil {
		t.Fatal("expected missing target phase to be rejected")
	}
	missingStep := valid
	missingStep.TargetStep = &StepRef{Kind: StepMutationEvidenceRequirement, ID: "missing"}
	if err := missingStep.Validate(snapshot); err == nil {
		t.Fatal("expected missing target step to be rejected")
	}
}

func TestGateOutcomeExpectedControlIsPostApplyAssertion(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
		output: make(chan *api.Message, 1),
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "expected_control_mismatch",
		ExpectedControl: RuntimeControlAwaitingFinalReport,
		ModelCorrection: "retry",
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchStayCurrent,
	})
	if !handled {
		t.Fatal("expected outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
		t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput for post-apply expected control mismatch", loop.loopLifecycle())
	}
	select {
	case message := <-loop.output:
		if message.Type != api.MessageTypeError {
			t.Fatalf("message type = %v, want error", message.Type)
		}
	default:
		t.Fatal("expected-control mismatch did not emit a user-visible error")
	}
}

func TestGateOutcomeValidateRejectsIncompleteSkipStepTargets(t *testing.T) {
	snapshot := RuntimeSnapshot{
		ActiveSteps: []StepRuntimeState{
			{Ref: StepRef{Kind: StepResourceGuideDiagnostic, Index: 1}},
			{Ref: StepRef{Kind: StepMutationEvidenceRequirement, ID: "direct"}},
			{Ref: StepRef{Kind: StepGeneralAction, Index: 1}},
		},
	}
	cases := []StepRef{
		{Kind: StepResourceGuideDiagnostic},
		{Kind: StepMutationEvidenceRequirement},
		{Kind: StepGeneralAction, Index: 1},
	}
	for _, ref := range cases {
		outcome := GateOutcome{
			Kind:         GateOutcomeModelOutputCorrection,
			TargetStep:   &ref,
			BranchPolicy: BranchSkipStep,
		}
		if err := outcome.Validate(snapshot); err == nil {
			t.Fatalf("expected invalid skip target %s to be rejected", ref.String())
		}
	}
}

func TestPhasePlanValidationUsesGateOutcomeCorrectionPath(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	handled := loop.applyGateOutcome(phasePlanValidationResult{
		Code:    "phase_plan_missing",
		Message: "return phase_plan",
	}.gateOutcome())
	if !handled {
		t.Fatal("expected phase plan validation outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleModelTurn {
		t.Fatalf("lifecycle = %v, want LoopLifecycleModelTurn", loop.loopLifecycle())
	}
	if loop.mutableRuntime().currIteration != 1 {
		t.Fatalf("currIteration = %d, want 1", loop.mutableRuntime().currIteration)
	}
	found := false
	for _, item := range loop.mutableRuntime().currChatContent {
		text, ok := item.(string)
		if ok && strings.Contains(text, "return phase_plan") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected model correction in currChatContent, got %#v", loop.mutableRuntime().currChatContent)
	}
}

func TestApplyGateOutcomeCanMoveToTargetPhase(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation", AllowedNext: []string{"verification"}},
					{Index: 2, Name: "verification"},
				},
				Completed: map[int]bool{},
			},
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "move_to_verification",
		ModelCorrection: "move to verification",
		TargetPhase:     &PhaseRef{Name: "verification"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchMovePhase,
	})
	if !handled {
		t.Fatal("expected outcome to be handled")
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 2 {
		t.Fatalf("current phase index = %d, want 2", got)
	}
}

func TestApplyGateOutcomeRejectsMoveOutsideAllowedNext(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation", AllowedNext: []string{"verification"}},
					{Index: 2, Name: "verification"},
					{Index: 3, Name: "remediation"},
				},
				Completed: map[int]bool{},
			},
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "skip_to_remediation",
		ModelCorrection: "move to remediation",
		TargetPhase:     &PhaseRef{Name: "remediation"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchMovePhase,
	})
	if !handled {
		t.Fatal("expected invalid branch outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
		t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput for invalid branch", loop.loopLifecycle())
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase index = %d, want unchanged phase 1", got)
	}
}

func TestApplyGateOutcomeAllowsRuntimeOverrideToMutationLifecyclePhase(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "remediation", AllowedNext: []string{"reporting"}},
					{Index: 2, Name: "reporting"},
					{Index: 3, Name: "mutation_verification"},
				},
				Completed: map[int]bool{},
			},
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeExternalStateWait,
		Code:            "mutation_requires_verification",
		RetryScope:      RetryScopeExternalState,
		ModelCorrection: "verify mutation outcome",
		TargetPhase:     &PhaseRef{Name: "mutation_verification"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchMovePhase,
	})
	if !handled {
		t.Fatal("expected override branch outcome to be handled")
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 3 {
		t.Fatalf("current phase index = %d, want mutation verification phase", got)
	}
}

func TestApplyGateOutcomeRejectsBackwardMoveWithoutRewind(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 2,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation"},
					{Index: 2, Name: "verification"},
				},
				Completed: map[int]bool{},
			},
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeExternalStateWait,
		Code:            "backward_move_without_cleanup",
		RetryScope:      RetryScopeExternalState,
		ModelCorrection: "move backward",
		TargetPhase:     &PhaseRef{Name: "observation"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchMovePhase,
	})
	if !handled {
		t.Fatal("expected invalid backward move outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
		t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput for backward move", loop.loopLifecycle())
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 2 {
		t.Fatalf("current phase index = %d, want unchanged phase 2", got)
	}
}

func TestApplyGateOutcomeRejectsForwardRewind(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation"},
					{Index: 2, Name: "verification"},
				},
				Completed: map[int]bool{},
			},
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "rewind_forward",
		ModelCorrection: "rewind",
		TargetPhase:     &PhaseRef{Name: "verification"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRewindPhase,
	})
	if !handled {
		t.Fatal("expected invalid rewind outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleAwaitingUserInput {
		t.Fatalf("lifecycle = %v, want LoopLifecycleAwaitingUserInput for invalid rewind", loop.loopLifecycle())
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase index = %d, want unchanged phase 1", got)
	}
}

func TestApplyGateOutcomeRewindsPhaseWithGuideScopedCleanup(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 3,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation"},
					{Index: 2, Name: "guided_diagnosis"},
					{Index: 3, Name: "mutation_verification"},
				},
				Completed: map[int]bool{1: true, 2: true},
			},
			guideStepState:           &guideStepState{TotalSteps: 1, Completed: map[int]bool{1: true}},
			resourceGuideInjected:    true,
			resourceGuideQueries:     map[string]struct{}{"query": {}},
			pendingResponseDirective: "final_report",
		},
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "rewind_guided_diagnosis",
		ModelCorrection: "rewind",
		TargetPhase:     &PhaseRef{Name: "guided_diagnosis"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRewindPhase,
	})
	if !handled {
		t.Fatal("expected outcome to be handled")
	}
	if loop.loopLifecycle() != LoopLifecycleModelTurn {
		t.Fatalf("lifecycle = %v, want LoopLifecycleModelTurn", loop.loopLifecycle())
	}
	if loop.mutableRuntime().phaseStepState.CurrentPhaseIndex != 2 {
		t.Fatalf("current phase = %d, want 2", loop.mutableRuntime().phaseStepState.CurrentPhaseIndex)
	}
	if loop.mutableRuntime().phaseStepState.Completed[2] {
		t.Fatalf("guided_diagnosis should no longer be completed: %#v", loop.mutableRuntime().phaseStepState.Completed)
	}
	if loop.mutableRuntime().guideStepState != nil || loop.mutableRuntime().resourceGuideInjected || loop.mutableRuntime().resourceGuideQueries != nil {
		t.Fatalf("expected guide state cleanup, guide=%#v injected=%v queries=%#v", loop.mutableRuntime().guideStepState, loop.mutableRuntime().resourceGuideInjected, loop.mutableRuntime().resourceGuideQueries)
	}
	if loop.mutableRuntime().pendingResponseDirective != "" {
		t.Fatalf("expected response directives to clear")
	}
}

func TestApplyGateOutcomeRejectsRewindWithMandatoryVerification(t *testing.T) {
	owner := StepRef{
		Phase:         PhaseRef{Index: 3, Name: "mutation_verification"},
		Kind:          StepMutationEvidenceRequirement,
		ID:            "direct",
		GoalLineageID: "mutation-direct",
	}
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingMutationVerificationEvidence,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 3,
				PhaseSteps: []phaseStep{
					{Index: 1, Name: "observation"},
					{Index: 2, Name: "guided_diagnosis"},
					{Index: 3, Name: "mutation_verification"},
				},
				Completed: map[int]bool{1: true, 2: true},
			},
			pendingMutationVerification: &pendingMutationVerification{
				Owner: owner,
				Checks: []verificationRuntimeCheck{{
					ID:     "direct",
					Status: contract.VerificationCheckActive,
				}},
			},
		},
	}

	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "rewind_guided_diagnosis",
		ModelCorrection: "rewind",
		TargetPhase:     &PhaseRef{Name: "guided_diagnosis"},
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRewindPhase,
	})
	if !handled {
		t.Fatal("expected rewind outcome to be handled")
	}
	if got := loop.mutableRuntime().phaseStepState.CurrentPhaseIndex; got != 3 {
		t.Fatalf("current phase = %d, want unchanged phase 3", got)
	}
	if loop.mutableRuntime().pendingMutationVerification == nil {
		t.Fatal("mandatory mutation verification was cleared by phase rewind")
	}
	if got := loop.controlState(); got != RuntimeControlAwaitingMutationVerificationEvidence {
		t.Fatalf("control = %s, want mandatory verification control", got)
	}
}

func TestApplyGateOutcomeSkipsGuideStep(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps:        []phaseStep{{Index: 1, Name: "guided_diagnosis"}},
				Completed:         map[int]bool{},
			},
			guideStepState: &guideStepState{
				TotalSteps: 2,
				StepDetails: []guideStepDetail{
					{Index: 1, Description: "already covered by live evidence"},
					{Index: 2, Description: "check events"},
				},
				Completed: map[int]bool{},
				Skipped:   map[int]bool{},
			},
		},
	}
	step := StepRef{
		Phase: PhaseRef{Index: 1, Name: "guided_diagnosis"},
		Kind:  StepResourceGuideDiagnostic,
		Index: 1,
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "skip_redundant_guide_step",
		ModelCorrection: "continue from the next remaining guide step",
		TargetStep:      &step,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchSkipStep,
	})
	if !handled {
		t.Fatal("expected skip outcome to be handled")
	}
	if !loop.mutableRuntime().guideStepState.Skipped[1] {
		t.Fatalf("guide skipped map = %#v", loop.mutableRuntime().guideStepState.Skipped)
	}
	if remaining := loop.mutableRuntime().guideStepState.remainingSteps(); len(remaining) != 1 || remaining[0] != 2 {
		t.Fatalf("remaining = %#v, want [2]", remaining)
	}
	snapshot := loop.RuntimeSnapshot()
	if snapshot.ActiveSteps[0].Status != StepSkipped {
		t.Fatalf("guide step 1 status = %s, want %s", snapshot.ActiveSteps[0].Status, StepSkipped)
	}
	if snapshot.ActiveSteps[1].Status != StepActive {
		t.Fatalf("guide step 2 status = %s, want %s", snapshot.ActiveSteps[1].Status, StepActive)
	}
}

func TestApplyGateOutcomeSkipsMutationEvidenceRequirement(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps:        []phaseStep{{Index: 1, Name: "mutation_verification"}},
				Completed:         map[int]bool{},
			},
			pendingMutationVerification: &pendingMutationVerification{
				Checks: []verificationRuntimeCheck{{
					ID:           "generic",
					Status:       contract.VerificationCheckActive,
					EvidenceRefs: []string{"observation-1"},
				}},
			},
		},
	}
	step := StepRef{
		Phase: PhaseRef{Index: 1, Name: "mutation_verification"},
		Kind:  StepMutationEvidenceRequirement,
		ID:    "generic",
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "skip_redundant_mutation_evidence",
		ModelCorrection: "emit mutation_verification_result using the collected evidence",
		TargetStep:      &step,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchSkipStep,
	})
	if !handled {
		t.Fatal("expected skip outcome to be handled")
	}
	if got := loop.mutableRuntime().pendingMutationVerification.Checks[0].Status; got != contract.VerificationCheckSkipped {
		t.Fatalf("check status = %s, want skipped", got)
	}
	if !loop.mutableRuntime().pendingMutationVerification.AwaitingResult {
		t.Fatal("expected skipped final evidence to await mutation_verification_result")
	}
	snapshot := loop.RuntimeSnapshot()
	if snapshot.ActiveSteps[0].Status != StepSkipped {
		t.Fatalf("mutation step status = %s, want %s", snapshot.ActiveSteps[0].Status, StepSkipped)
	}
}

func TestMutationContinuationBudgetRequestsFinalReport(t *testing.T) {
	loop := &Loop{}
	result := mutationVerificationResult{
		Status:          contract.VerificationFailed,
		EvidenceSummary: []string{"rollout still progressing"},
		NextAction:      "recheck rollout",
	}
	for i := 0; i < maxMutationContinuationAttempts; i++ {
		loop.requestMutationContinuationOrBudgetReport(result)
		if loop.mutableRuntime().control != RuntimeControlAwaitingMutationContinuation {
			t.Fatalf("attempt %d control = %s, want mutation continuation", i+1, loop.mutableRuntime().control)
		}
	}
	loop.requestMutationContinuationOrBudgetReport(result)
	if loop.mutableRuntime().control != RuntimeControlAwaitingFinalReport {
		t.Fatalf("control = %s, want final_report after budget exhaustion", loop.mutableRuntime().control)
	}
	if !strings.Contains(loop.mutableRuntime().pendingResponseDirective, "conclusive=false") {
		t.Fatalf("directive = %q, want inconclusive final report instruction", loop.mutableRuntime().pendingResponseDirective)
	}
}

func TestToolFailureOutcomeClassifiesFailureKinds(t *testing.T) {
	loop := &Loop{}
	call := PendingCall{FunctionCall: gollm.FunctionCall{
		Name:      "kubectl",
		Arguments: map[string]any{"command": "kubectl get pods -n app"},
	}}

	forbidden := map[string]any{
		"status": "error",
		"stderr": "Error from server (Forbidden): pods is forbidden",
	}
	outcome, failed := loop.annotateToolFailureResult(call, forbidden)
	if !failed {
		t.Fatal("expected forbidden result to be classified as tool failure")
	}
	if outcome.Kind != GateOutcomeToolExecutionFailure || !outcome.Retryable || outcome.BranchPolicy != BranchRetryStep {
		t.Fatalf("outcome = %#v, want retryable current-phase tool execution failure", outcome)
	}
	if forbidden["failure_class"] != string(toolFailureRBAC) || forbidden["retry_scope"] != string(RetryScopeCurrentPhase) {
		t.Fatalf("forbidden annotations = %#v", forbidden)
	}

	syntax := map[string]any{
		"status": "failed",
		"stderr": "unknown flag: --bad",
	}
	outcome, failed = loop.annotateToolFailureResult(call, syntax)
	if !failed {
		t.Fatal("expected syntax result to be classified as tool failure")
	}
	if outcome.Kind != GateOutcomeToolExecutionFailure || !outcome.Retryable || outcome.RetryScope != RetryScopeAgentCommand || outcome.BranchPolicy != BranchRetryStep {
		t.Fatalf("outcome = %#v, want retryable agent command failure", outcome)
	}
	if syntax["failure_class"] != string(toolFailureCommandSyntax) || syntax["retry_scope"] != string(RetryScopeAgentCommand) {
		t.Fatalf("syntax annotations = %#v", syntax)
	}
}

func TestToolFailureResultFromErrorsFeedsOutcomeClassifier(t *testing.T) {
	loop := &Loop{}
	call := PendingCall{FunctionCall: gollm.FunctionCall{
		Name:      "kubectl",
		Arguments: map[string]any{"command": "kubectl get pods"},
	}}

	result := toolFailureResultFromError(assertErr("deadline exceeded while calling API server"))
	outcome, failed := loop.annotateToolFailureResult(call, result)
	if !failed {
		t.Fatal("expected InvokeTool error result to be classified")
	}
	if result["failure_class"] != string(toolFailureTimeout) || outcome.RetryScope != RetryScopeExternalState {
		t.Fatalf("result=%#v outcome=%#v, want timeout external-state failure", result, outcome)
	}

	result = toolFailureResultFromMapError(assertErr("missing result payload"))
	outcome, failed = loop.annotateToolFailureResult(call, result)
	if !failed {
		t.Fatal("expected result conversion error to be classified")
	}
	if result["failure_class"] != string(toolFailureUnknown) || outcome.RetryScope != RetryScopeAgentCommand {
		t.Fatalf("result=%#v outcome=%#v, want unknown agent-command failure", result, outcome)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestToolFailureOutcomeClassifiesPartialSuccess(t *testing.T) {
	loop := &Loop{}
	call := PendingCall{FunctionCall: gollm.FunctionCall{
		Name:      "kubectl",
		Arguments: map[string]any{"command": "kubectl get pods -A"},
	}}
	result := map[string]any{
		"status": "partial_success",
		"items":  []any{map[string]any{"metadata": map[string]any{"name": "ok"}}},
		"errors": []any{"namespace restricted: forbidden"},
	}
	outcome, failed := loop.annotateToolFailureResult(call, result)
	if !failed {
		t.Fatal("expected partial success to be classified as tool failure")
	}
	if outcome.Kind != GateOutcomeToolExecutionFailure || !outcome.Retryable || outcome.RetryScope != RetryScopeCurrentStep {
		t.Fatalf("outcome = %#v, want retryable current-step partial success", outcome)
	}
	if result["failure_class"] != string(toolFailurePartial) || result["retry_scope"] != string(RetryScopeCurrentStep) {
		t.Fatalf("partial annotations = %#v", result)
	}
	if toolResultSucceeded(result) {
		t.Fatal("partial success must not satisfy mutation verification")
	}
}
