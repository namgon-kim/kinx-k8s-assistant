package react

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

func TestGateOutcomeValidateChecksTargetPhaseAndStep(t *testing.T) {
	snapshot := RuntimeSnapshot{
		Control: ControlAwaitingModelStep,
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
		ExpectedControl: ControlAwaitingModelStep,
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
	loop := &Loop{state: StateRunning}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            "expected_control_mismatch",
		ExpectedControl: ControlAwaitingFinalReport,
		ModelCorrection: "retry",
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchStayCurrent,
	})
	if !handled {
		t.Fatal("expected outcome to be handled")
	}
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone for post-apply expected control mismatch", loop.state)
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
	loop := &Loop{state: StateRunning}
	handled := loop.applyGateOutcome(phasePlanValidationResult{
		Code:    "phase_plan_missing",
		Message: "return phase_plan",
	}.gateOutcome())
	if !handled {
		t.Fatal("expected phase plan validation outcome to be handled")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if loop.currIteration != 1 {
		t.Fatalf("currIteration = %d, want 1", loop.currIteration)
	}
	found := false
	for _, item := range loop.currChatContent {
		text, ok := item.(string)
		if ok && strings.Contains(text, "return phase_plan") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected model correction in currChatContent, got %#v", loop.currChatContent)
	}
}

func TestApplyGateOutcomeCanMoveToTargetPhase(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation", AllowedNext: []string{"verification"}},
				{Index: 2, Name: "verification"},
			},
			Completed: map[int]bool{},
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
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 2 {
		t.Fatalf("current phase index = %d, want 2", got)
	}
}

func TestApplyGateOutcomeRejectsMoveOutsideAllowedNext(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation", AllowedNext: []string{"verification"}},
				{Index: 2, Name: "verification"},
				{Index: 3, Name: "remediation"},
			},
			Completed: map[int]bool{},
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
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone for invalid branch", loop.state)
	}
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase index = %d, want unchanged phase 1", got)
	}
}

func TestApplyGateOutcomeAllowsRuntimeOverrideToMutationLifecyclePhase(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "remediation", AllowedNext: []string{"reporting"}},
				{Index: 2, Name: "reporting"},
				{Index: 3, Name: "mutation_verification"},
			},
			Completed: map[int]bool{},
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
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 3 {
		t.Fatalf("current phase index = %d, want mutation verification phase", got)
	}
}

func TestApplyGateOutcomeRejectsBackwardMoveWithoutRewind(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 2,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation"},
				{Index: 2, Name: "verification"},
			},
			Completed: map[int]bool{},
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
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone for backward move", loop.state)
	}
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 2 {
		t.Fatalf("current phase index = %d, want unchanged phase 2", got)
	}
}

func TestApplyGateOutcomeRejectsForwardRewind(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation"},
				{Index: 2, Name: "verification"},
			},
			Completed: map[int]bool{},
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
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone for invalid rewind", loop.state)
	}
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase index = %d, want unchanged phase 1", got)
	}
}

func TestApplyGateOutcomeRewindsPhaseWithScopedCleanup(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 3,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation"},
				{Index: 2, Name: "guided_diagnosis"},
				{Index: 3, Name: "mutation_verification"},
			},
			Completed: map[int]bool{1: true, 2: true},
		},
		guideStepState:               &guideStepState{TotalSteps: 1, Completed: map[int]bool{1: true}},
		resourceGuideInjected:        true,
		resourceGuideQueries:         map[string]struct{}{"query": {}},
		pendingMutationVerification:  &pendingMutationVerification{Requirements: []mutationEvidenceRequirement{{ID: "direct"}}},
		mutationContinuationRequired: true,
		finalReportRequested:         true,
		pendingResponseDirective:     "final_report",
		completedActions: []actionRecord{
			{Step: 1, Phase: &PhaseRef{Index: 1, Name: "observation"}},
			{Step: 2, Phase: &PhaseRef{Index: 2, Name: "guided_diagnosis"}},
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
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if loop.phaseStepState.CurrentPhaseIndex != 2 {
		t.Fatalf("current phase = %d, want 2", loop.phaseStepState.CurrentPhaseIndex)
	}
	if loop.phaseStepState.Completed[2] {
		t.Fatalf("guided_diagnosis should no longer be completed: %#v", loop.phaseStepState.Completed)
	}
	if loop.guideStepState != nil || loop.resourceGuideInjected || loop.resourceGuideQueries != nil {
		t.Fatalf("expected guide state cleanup, guide=%#v injected=%v queries=%#v", loop.guideStepState, loop.resourceGuideInjected, loop.resourceGuideQueries)
	}
	if loop.pendingMutationVerification != nil || loop.mutationContinuationRequired {
		t.Fatalf("expected mutation verification cleanup, pending=%#v continuation=%v", loop.pendingMutationVerification, loop.mutationContinuationRequired)
	}
	if loop.finalReportRequested || loop.pendingResponseDirective != "" {
		t.Fatalf("expected response directives to clear")
	}
	if len(loop.completedActions) != 1 || loop.completedActions[0].Step != 1 {
		t.Fatalf("completed actions = %#v, want only pre-rewind actions", loop.completedActions)
	}
}

func TestApplyGateOutcomeBranchRecheckStepConsumesMutationBudget(t *testing.T) {
	loop := &Loop{
		state:                        StateRunning,
		mutationContinuationRequired: true,
		mutationContinuationAttempts: maxMutationContinuationAttempts,
	}
	handled := loop.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeExternalStateWait,
		Code:            "rollout_still_progressing",
		ModelCorrection: "recheck rollout",
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRecheckStep,
	})
	if !handled {
		t.Fatal("expected recheck branch outcome to be handled")
	}
	if loop.mutationContinuationRequired {
		t.Fatal("expected mutation continuation to stop after recheck budget exhaustion")
	}
	if !loop.finalReportRequested {
		t.Fatal("expected final report after recheck budget exhaustion")
	}
	if !strings.Contains(loop.pendingResponseDirective, "conclusive=false") {
		t.Fatalf("directive = %q, want inconclusive final report instruction", loop.pendingResponseDirective)
	}
	if !strings.Contains(loop.pendingResponseDirective, "after 3 recheck attempts") {
		t.Fatalf("directive = %q, want exhausted attempt count", loop.pendingResponseDirective)
	}
}

func TestApplyGateOutcomeSkipsGuideStep(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
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
	if !loop.guideStepState.Skipped[1] {
		t.Fatalf("guide skipped map = %#v", loop.guideStepState.Skipped)
	}
	if remaining := loop.guideStepState.remainingSteps(); len(remaining) != 1 || remaining[0] != 2 {
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
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps:        []phaseStep{{Index: 1, Name: "mutation_verification"}},
			Completed:         map[int]bool{},
		},
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{{ID: "generic", Kind: "generic"}},
			Satisfied:    map[string]bool{},
			Skipped:      map[string]bool{},
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
	if !loop.pendingMutationVerification.Skipped["generic"] {
		t.Fatalf("skipped map = %#v", loop.pendingMutationVerification.Skipped)
	}
	if remaining := loop.pendingMutationVerification.remainingRequirements(); len(remaining) != 0 {
		t.Fatalf("remaining = %#v, want none", remaining)
	}
	if !loop.pendingMutationVerification.AwaitingResult {
		t.Fatal("expected skipped final evidence to await mutation_verification_result")
	}
	snapshot := loop.RuntimeSnapshot()
	if snapshot.ActiveSteps[0].Status != StepSkipped {
		t.Fatalf("mutation step status = %s, want %s", snapshot.ActiveSteps[0].Status, StepSkipped)
	}
}

func TestRuntimeSnapshotProjectsCompletedGeneralActions(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 2,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "observation"},
				{Index: 2, Name: "verification"},
			},
			Completed: map[int]bool{1: true},
		},
		completedActions: []actionRecord{{
			Step:    1,
			Tool:    "kubectl",
			Phase:   &PhaseRef{Index: 1, Name: "observation"},
			Command: "kubectl get pods",
		}},
	}
	snapshot := loop.RuntimeSnapshot()
	if len(snapshot.ActiveSteps) != 1 {
		t.Fatalf("active steps len = %d, want completed action projection", len(snapshot.ActiveSteps))
	}
	step := snapshot.ActiveSteps[0]
	if step.Ref.Kind != StepGeneralAction || step.Status != StepCompleted {
		t.Fatalf("step = %#v, want completed general action", step)
	}
	if step.Ref.Phase.Name != "observation" || step.Command != "kubectl get pods" {
		t.Fatalf("step = %#v", step)
	}
}

func TestMutationContinuationBudgetRequestsFinalReport(t *testing.T) {
	loop := &Loop{}
	result := mutationVerificationResult{
		Status:          "progressing",
		EvidenceSummary: []string{"rollout still progressing"},
		NextAction:      "recheck rollout",
	}
	for i := 0; i < maxMutationContinuationAttempts; i++ {
		loop.requestMutationContinuationOrBudgetReport(result)
		if !loop.mutationContinuationRequired {
			t.Fatalf("attempt %d should still require continuation", i+1)
		}
	}
	loop.requestMutationContinuationOrBudgetReport(result)
	if loop.mutationContinuationRequired {
		t.Fatal("expected continuation to stop after budget exhaustion")
	}
	if !loop.finalReportRequested {
		t.Fatal("expected final_report to be requested after budget exhaustion")
	}
	if !strings.Contains(loop.pendingResponseDirective, "conclusive=false") {
		t.Fatalf("directive = %q, want inconclusive final report instruction", loop.pendingResponseDirective)
	}
}

func TestMutationContinuationBudgetResetsWhenVerificationLifecycleExpands(t *testing.T) {
	loop := &Loop{
		mutationContinuationAttempts: 2,
	}
	loop.mergeMutationVerification(pendingMutationVerification{
		Requirements: []mutationEvidenceRequirement{{ID: "direct"}},
	})
	if loop.mutationContinuationAttempts != 0 {
		t.Fatalf("attempts = %d, want reset for new verification", loop.mutationContinuationAttempts)
	}
	loop.mutationContinuationAttempts = 2
	loop.mergeMutationVerification(pendingMutationVerification{
		Requirements: []mutationEvidenceRequirement{{ID: "outcome"}},
	})
	if loop.mutationContinuationAttempts != 0 {
		t.Fatalf("attempts = %d, want reset for expanded verification", loop.mutationContinuationAttempts)
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
	if outcome.Kind != GateOutcomeToolExecutionFailure || outcome.Retryable || outcome.BranchPolicy != BranchBlockUserRequest {
		t.Fatalf("outcome = %#v, want non-retryable tool execution failure", outcome)
	}
	if forbidden["failure_class"] != string(toolFailureRBAC) || forbidden["retry_scope"] != string(RetryScopeUserRequest) {
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
