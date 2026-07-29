package session

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestExecutionLedgerIndexesLineageAndObservations(t *testing.T) {
	var ledger ExecutionLedger
	if err := ledger.AppendAttempt(contract.AttemptRecord{
		ID:            "attempt-1",
		GoalLineageID: "goal-lineage-1",
		Status:        contract.AttemptDispatchPending,
	}); err != nil {
		t.Fatalf("append first attempt: %v", err)
	}
	if err := ledger.AppendAttempt(contract.AttemptRecord{
		ID:            "attempt-2",
		GoalLineageID: "goal-lineage-1",
		Status:        contract.AttemptFailed,
	}); err != nil {
		t.Fatalf("append second attempt: %v", err)
	}
	if err := ledger.AppendObservation(contract.ObservationRecord{
		ID:        "observation-1",
		AttemptID: "attempt-2",
		Result:    map[string]any{"items": []any{"pod-a"}},
	}); err != nil {
		t.Fatalf("append observation: %v", err)
	}
	if !ledger.LinkObservationToAttempt("attempt-2", "observation-1") {
		t.Fatal("link observation to attempt failed")
	}

	if got := ledger.AttemptCountForLineage("goal-lineage-1"); got != 2 {
		t.Fatalf("lineage attempt count = %d, want 2", got)
	}
	if err := ledger.AppendAttempt(contract.AttemptRecord{
		ID:            "attempt-3",
		GoalLineageID: "goal-lineage-1",
		Status:        contract.AttemptCancelled,
	}); err != nil {
		t.Fatalf("append cancelled attempt: %v", err)
	}
	if got := ledger.AttemptCountForLineage("goal-lineage-1"); got != 2 {
		t.Fatalf("cancelled dispatch consumed lineage budget: got %d, want 2", got)
	}
	latest, ok := ledger.LatestAttemptForLineage("goal-lineage-1")
	if !ok || latest.ID != "attempt-2" || len(latest.ObservationRefs) != 1 {
		t.Fatalf("latest lineage attempt = %#v, ok=%v", latest, ok)
	}
	if position, ok := ledger.ObservationPosition("observation-1"); !ok || position != 0 {
		t.Fatalf("observation position = %d, ok=%v", position, ok)
	}
	if err := ledger.AuditError(); err != "" {
		t.Fatalf("ledger audit failed: %s", err)
	}
}

func TestExecutionLedgerCloneDoesNotShareNestedRecords(t *testing.T) {
	var ledger ExecutionLedger
	if err := ledger.AppendAttempt(contract.AttemptRecord{
		ID:            "attempt-1",
		GoalLineageID: "goal-lineage-1",
		Action: contract.FunctionCall{
			Arguments: map[string]any{"target": map[string]any{"resource": "pods"}},
		},
	}); err != nil {
		t.Fatalf("append attempt: %v", err)
	}
	clone := ledger.Clone()
	clone.UpdateAttempt("attempt-1", func(attempt *contract.AttemptRecord) {
		attempt.Action.Arguments["target"].(map[string]any)["resource"] = "deployments"
	})

	original, _ := ledger.AttemptByID("attempt-1")
	target := original.Action.Arguments["target"].(map[string]any)
	if got := target["resource"]; got != "pods" {
		t.Fatalf("clone mutation leaked into original ledger: %v", got)
	}
}
