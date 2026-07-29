package coordinator

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
)

func TestRBACObservationCannotSatisfyMutationVerification(t *testing.T) {
	result := map[string]any{
		"stderr": "Error from server (Forbidden): pods is forbidden: User cannot get resource \"pods\"",
	}
	if got := verificationEvidenceQualification(result); got != contract.EvidenceAccessBlocker {
		t.Fatalf("qualification = %q, want %q", got, contract.EvidenceAccessBlocker)
	}
	if toolResultSucceeded(result) {
		t.Fatal("stderr-only RBAC response was classified as a successful tool result")
	}

	execution := &session.GoalExecutionState{}
	if err := execution.AppendObservation(contract.ObservationRecord{
		ID:            "observation-rbac",
		Qualification: contract.EvidenceAccessBlocker,
	}); err != nil {
		t.Fatalf("append observation: %v", err)
	}
	loop := &Loop{runtimeState: &runtimeState{execution: execution}}
	satisfied := mutationVerificationResult{
		Status:       contract.VerificationSatisfied,
		EvidenceRefs: []string{"observation-rbac"},
	}
	if loop.verificationEvidenceSupportsStatus(satisfied) {
		t.Fatal("RBAC-only evidence satisfied mutation verification")
	}
	failed := satisfied
	failed.Status = contract.VerificationFailed
	if !loop.verificationEvidenceSupportsStatus(failed) {
		t.Fatal("RBAC access blocker was not accepted for failed verification")
	}
}

func TestUnknownAndCancelledResultsAreNotSuccessfulEvidence(t *testing.T) {
	for _, status := range []string{"unknown", "uncertain", "cancelled", "canceled"} {
		result := map[string]any{"status": status}
		if toolResultSucceeded(result) {
			t.Fatalf("status %q was classified as successful", status)
		}
	}
	if got := verificationEvidenceQualification(map[string]any{"status": "unknown"}); got != contract.EvidenceUncertain {
		t.Fatalf("unknown evidence qualification = %q, want %q", got, contract.EvidenceUncertain)
	}
	if got := verificationEvidenceQualification(map[string]any{"status": "cancelled"}); got != contract.EvidenceUnusable {
		t.Fatalf("cancelled evidence qualification = %q, want %q", got, contract.EvidenceUnusable)
	}
}

func TestBlockedObligationForcesInconclusiveReportAcrossControlTransitions(t *testing.T) {
	loop := &Loop{runtimeState: &runtimeState{
		execution: &session.GoalExecutionState{
			SessionID:          "session-1",
			RequestID:          "request-1",
			BlockedObligations: []string{"mutation_verification:attempt-1:verification-1"},
		},
	}}
	loop.transitionControl(RuntimeControlAwaitingMutationContinuation)
	loop.transitionControl(RuntimeControlAwaitingFinalReport)
	if !loop.finalReportMustBeInconclusive() {
		t.Fatal("blocked obligation did not constrain the final report")
	}
	if !loop.RuntimeSnapshot().FinalReportMustBeInconclusive {
		t.Fatal("snapshot omitted the blocked-obligation report constraint")
	}
}

func TestClearConversationRemovesRequestExecutionAndDispatchState(t *testing.T) {
	loop := &Loop{runtimeState: &runtimeState{
		execution:       &session.GoalExecutionState{RequestID: "request-1"},
		dispatchIntents: map[string]contract.ToolDispatchIntent{"dispatch-1": {ID: "dispatch-1"}},
		dispatchOrder:   []string{"dispatch-1"},
	}}
	loop.clearConversationState()
	if loop.mutableRuntime().execution != nil {
		t.Fatal("clear retained the previous goal execution")
	}
	if len(loop.mutableRuntime().dispatchIntents) != 0 || len(loop.mutableRuntime().dispatchOrder) != 0 {
		t.Fatal("clear retained request-scoped dispatch state")
	}
}

func TestNonZeroExitCodeIsFailureWithoutGuessingFailureClass(t *testing.T) {
	result := map[string]any{"exit_code": 1, "stderr": "opaque provider failure"}
	if toolResultSucceeded(result) {
		t.Fatal("non-zero process exit was classified as success")
	}
	if got := verificationEvidenceQualification(result); got != contract.EvidenceUnusable {
		t.Fatalf("opaque non-zero exit qualification = %q, want %q", got, contract.EvidenceUnusable)
	}
}

func TestExplicitVerificationFailureDoesNotCreateUnresolvedObligation(t *testing.T) {
	execution := &session.GoalExecutionState{RequestID: "request-1"}
	if err := execution.AppendAttempt(contract.AttemptRecord{
		ID:     "attempt-1",
		StepID: "step-1",
		Status: contract.AttemptVerifying,
	}); err != nil {
		t.Fatalf("append attempt: %v", err)
	}
	loop := &Loop{runtimeState: &runtimeState{
		execution: execution,
		pendingMutationVerification: &pendingMutationVerification{
			AttemptID: "attempt-1",
			Owner:     contract.StepRef{ID: "step-1"},
			Checks: []verificationRuntimeCheck{{
				ID:     "verification-1",
				Status: contract.VerificationCheckActive,
			}},
		},
	}}
	loop.finalizePendingMutationVerification("expected state was disproved", contract.AttemptFailed)
	if len(loop.mutableRuntime().execution.BlockedObligations) != 0 {
		t.Fatalf("explicit failure created unresolved obligations: %#v", loop.mutableRuntime().execution.BlockedObligations)
	}
	if loop.mutableRuntime().finalReportMustBeInconclusive {
		t.Fatal("explicitly disproved state permanently forced an inconclusive report")
	}
}

func TestContinuationSnapshotIncludesResumeStateAndAuditsOwner(t *testing.T) {
	continuation := &contract.ContinuationState{
		Handoff: contract.ContinuationHandoff{
			RequestID:           "request-1",
			GoalID:              "goal-1",
			CurrentJudgement:    "segment limit reached",
			RecommendedNextStep: "continue active step",
		},
		Segment: 1,
	}
	snapshot := projectRuntimeSnapshot(&runtimeState{
		control:                   RuntimeControlAwaitingContinuationChoice,
		continuation:              continuation,
		continuationResumePending: true,
		pendingDirectionPrompt:    &directionPromptState{},
		execution: &session.GoalExecutionState{
			SessionID:    "session-1",
			RequestID:    "request-1",
			Goal:         contract.GoalContract{ID: "goal-1"},
			PlanRevision: 1,
		},
	}, 3)
	if snapshot.Continuation == nil || !snapshot.ContinuationResumePending {
		t.Fatalf("continuation state missing from snapshot: %#v", snapshot)
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("valid continuation snapshot audit = %q", got)
	}

	snapshot.Continuation = nil
	if got := snapshot.AuditError(); got == "" {
		t.Fatal("orphan continuation resume flag passed audit")
	}
}

func TestContinuationHandoffCannotOmitNonterminalStep(t *testing.T) {
	execution := &session.GoalExecutionState{
		RequestID: "request-1",
		Goal:      contract.GoalContract{ID: "goal-1"},
		Phases: []contract.PhaseContract{{
			ID: "phase-1",
			Steps: []contract.StepContract{{
				ID: "step-1",
			}},
		}},
		StepStatus: map[string]contract.StepStatus{"step-1": contract.StepActive},
	}
	loop := &Loop{runtimeState: &runtimeState{execution: execution}}
	handoff := contract.ContinuationHandoff{
		RequestID:           "request-1",
		GoalID:              "goal-1",
		CurrentJudgement:    "diagnosis is incomplete",
		RecommendedNextStep: "continue step-1",
	}
	if err := loop.validateContinuationHandoff(handoff); err == nil {
		t.Fatal("handoff omitted the active step")
	}
	handoff.UnresolvedSteps = []string{"step-1"}
	if err := loop.validateContinuationHandoff(handoff); err != nil {
		t.Fatalf("complete handoff rejected: %v", err)
	}
}

func TestNotFoundRemainsStateBearingVerificationEvidence(t *testing.T) {
	result := map[string]any{
		"status": "failed",
		"stderr": "Error from server (NotFound): pods \"app\" not found",
	}
	if got := verificationEvidenceQualification(result); got != contract.EvidenceStateBearing {
		t.Fatalf("qualification = %q, want %q", got, contract.EvidenceStateBearing)
	}
}

func TestCommandRiskRequiresExplicitBooleanAndReason(t *testing.T) {
	if _, err := commandRiskFromAny(nil, true); err == nil {
		t.Fatal("missing command risk was accepted")
	}
	if _, err := commandRiskFromAny(map[string]any{"reason": "dangerous"}, true); err == nil {
		t.Fatal("risk object without risky boolean was accepted")
	}
	if _, err := commandRiskFromAny(map[string]any{"risky": true}, true); err == nil {
		t.Fatal("risky command without reason was accepted")
	}
	risk, err := commandRiskFromAny(map[string]any{"risky": false}, true)
	if err != nil || risk == nil || risk.Risky {
		t.Fatalf("safe command risk = %#v, err=%v", risk, err)
	}
}

func TestFinalizePendingMutationVerificationClosesOwningAttempt(t *testing.T) {
	execution := &session.GoalExecutionState{
		SessionID:          "session-1",
		RequestID:          "request-1",
		BlockedObligations: []string{},
	}
	if err := execution.AppendAttempt(contract.AttemptRecord{
		ID:     "attempt-1",
		StepID: "step-1",
		Status: contract.AttemptVerifying,
	}); err != nil {
		t.Fatalf("append attempt: %v", err)
	}
	loop := &Loop{runtimeState: &runtimeState{
		execution: execution,
		pendingMutationVerification: &pendingMutationVerification{
			AttemptID: "attempt-1",
			Owner:     contract.StepRef{ID: "step-1"},
			Checks: []verificationRuntimeCheck{{
				ID:     "verification-1",
				Status: contract.VerificationCheckActive,
			}},
		},
	}}
	warning := loop.finalizePendingMutationVerification("user ended the request", contract.AttemptUnknown)
	if warning == "" || loop.mutableRuntime().pendingMutationVerification != nil {
		t.Fatalf("verification was not finalized: warning=%q state=%#v", warning, loop.mutableRuntime().pendingMutationVerification)
	}
	attempt, ok := loop.mutableRuntime().execution.AttemptByID("attempt-1")
	if !ok || attempt.Status != contract.AttemptUnknown {
		t.Fatalf("owning attempt = %#v, ok=%v", attempt, ok)
	}
	if len(loop.mutableRuntime().execution.BlockedObligations) != 1 {
		t.Fatalf("blocked obligations = %#v", loop.mutableRuntime().execution.BlockedObligations)
	}
	if got := loop.mutableRuntime().execution.BlockedObligations[0]; got != "mutation_verification:attempt-1:verification-1" {
		t.Fatalf("blocked obligation ID = %q", got)
	}
	loop.transitionControl(RuntimeControlAwaitingMutationContinuation)
	if !loop.mutableRuntime().finalReportMustBeInconclusive {
		t.Fatal("control transition cleared the unresolved verification report constraint")
	}
}
