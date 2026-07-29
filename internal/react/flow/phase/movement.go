package phase

import (
	"fmt"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type MovementKind string

const (
	MovementForward MovementKind = "forward"
	MovementRewind  MovementKind = "rewind"
)

type MovementInput struct {
	Kind             MovementKind
	Current          contract.PhaseRef
	Target           contract.PhaseRef
	MandatoryOwner   *contract.StepRef
	TargetIsComplete bool
}

func ValidateMovement(input MovementInput) error {
	if input.MandatoryOwner != nil {
		return fmt.Errorf(
			"phase %s is blocked by mandatory obligation owned by step %s",
			input.Kind,
			input.MandatoryOwner.String(),
		)
	}
	if input.Current.Index == 0 || input.Target.Index == 0 {
		return fmt.Errorf("phase %s requires stable current and target references", input.Kind)
	}
	if input.TargetIsComplete && input.Kind == MovementForward {
		return fmt.Errorf("target phase %s is already complete", input.Target.String())
	}
	switch input.Kind {
	case MovementForward:
		if input.Target.Index < input.Current.Index {
			return fmt.Errorf("forward phase movement cannot target an earlier phase")
		}
	case MovementRewind:
		if input.Target.Index > input.Current.Index {
			return fmt.Errorf("phase rewind cannot target a later phase")
		}
	default:
		return fmt.Errorf("unsupported phase movement kind %q", input.Kind)
	}
	return nil
}
