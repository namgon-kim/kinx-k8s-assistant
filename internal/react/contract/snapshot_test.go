package contract

import "testing"

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
