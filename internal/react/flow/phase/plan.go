package phase

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func HasPhase(plan contract.PhasePlan, name string) bool {
	want := normalizeName(name)
	for _, step := range plan.PhaseSteps {
		if normalizeName(step.Name) == want {
			return true
		}
	}
	return false
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
