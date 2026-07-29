package phase

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestValidateMovementRejectsMandatoryObligationRegardlessOfPhaseName(t *testing.T) {
	owner := contract.StepRef{
		ID:            "request-1.step-2",
		GoalLineageID: "request-1.goal-lineage-2",
		Phase: contract.PhaseRef{
			ID:        "request-1.phase-2",
			LineageID: "request-1.phase-lineage-2",
			Index:     2,
			Name:      "ordinary-work",
		},
	}
	err := ValidateMovement(MovementInput{
		Kind:           MovementRewind,
		Current:        owner.Phase,
		Target:         contract.PhaseRef{ID: "request-1.phase-1", Index: 1, Name: "initial-work"},
		MandatoryOwner: &owner,
	})
	if err == nil {
		t.Fatal("phase rewind bypassed a mandatory obligation")
	}
}
