package phase

import (
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func Validate(plan contract.PhasePlan) error {
	if strings.TrimSpace(plan.RequestGoal) == "" || len(plan.PhaseSteps) == 0 {
		return fmt.Errorf("phase plan requires a request goal and at least one phase")
	}
	seenIndex := make(map[int]struct{}, len(plan.PhaseSteps))
	seenName := make(map[string]struct{}, len(plan.PhaseSteps))
	for _, phase := range plan.PhaseSteps {
		name := strings.ToLower(strings.TrimSpace(phase.Name))
		if phase.Index <= 0 || name == "" || strings.TrimSpace(phase.Goal) == "" || strings.TrimSpace(phase.CompletionCondition) == "" {
			return fmt.Errorf("invalid phase at index %d", phase.Index)
		}
		if _, exists := seenIndex[phase.Index]; exists {
			return fmt.Errorf("duplicate phase index %d", phase.Index)
		}
		if _, exists := seenName[name]; exists {
			return fmt.Errorf("duplicate phase name %q", phase.Name)
		}
		seenIndex[phase.Index] = struct{}{}
		seenName[name] = struct{}{}
	}
	return nil
}
