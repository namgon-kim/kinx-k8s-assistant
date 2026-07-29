package contract

import (
	"strings"
	"testing"
)

func TestStepRefMatchesRejectsDifferentPhaseIDs(t *testing.T) {
	left := StepRef{
		Kind:  StepExplicitPhase,
		ID:    "step-1",
		Phase: PhaseRef{ID: "phase-1"},
	}
	right := StepRef{
		Kind:  StepExplicitPhase,
		ID:    "step-1",
		Phase: PhaseRef{ID: "phase-2"},
	}

	if left.Matches(right) {
		t.Fatal("step references from different phase IDs matched")
	}
}

func TestStepRefMatchesRejectsPhaseOnlyAssertion(t *testing.T) {
	asserted := StepRef{Phase: PhaseRef{ID: "phase-1"}}
	bound := StepRef{
		Kind:          StepExplicitPhase,
		ID:            "step-1",
		GoalLineageID: "lineage-1",
		Index:         1,
		Phase:         PhaseRef{ID: "phase-1", LineageID: "phase-lineage-1", Index: 2, Name: "diagnosis"},
	}

	if asserted.Matches(bound) {
		t.Fatal("phase-only assertion matched a concrete step")
	}
}

func TestStepRefMatchesAcceptsStepIDWithCompatiblePhase(t *testing.T) {
	asserted := StepRef{ID: "step-1", Phase: PhaseRef{ID: "phase-1"}}
	bound := StepRef{
		Kind:          StepExplicitPhase,
		ID:            "step-1",
		GoalLineageID: "lineage-1",
		Index:         1,
		Phase:         PhaseRef{ID: "phase-1", LineageID: "phase-lineage-1", Index: 2, Name: "diagnosis"},
	}

	if !asserted.Matches(bound) {
		t.Fatal("step ID assertion with a compatible phase did not match")
	}
}

func TestStepRefStringIncludesPhaseStableIdentity(t *testing.T) {
	withID := StepRef{
		Kind:  StepGeneralAction,
		ID:    "step-9",
		Phase: PhaseRef{ID: "phase-1"},
	}
	if got := withID.String(); !strings.Contains(got, "phase=phase-1") {
		t.Fatalf("step ref string omitted phase ID: %q", got)
	}

	withLineage := StepRef{
		Kind:  StepGeneralAction,
		ID:    "step-10",
		Phase: PhaseRef{LineageID: "phase-lineage-1"},
	}
	if got := withLineage.String(); !strings.Contains(got, "phase=lineage=phase-lineage-1") {
		t.Fatalf("step ref string omitted phase lineage: %q", got)
	}
}
