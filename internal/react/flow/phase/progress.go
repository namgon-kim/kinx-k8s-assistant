package phase

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

func Advance(plan contract.PhasePlan, current contract.PhaseRef, progress contract.PhaseProgress) (contract.PhaseRef, bool) {
	if progress.PhaseCompleted != current.Index {
		return current, false
	}
	for _, candidate := range plan.PhaseSteps {
		if candidate.Name == progress.NextPhase {
			return contract.PhaseRef{Index: candidate.Index, Name: candidate.Name}, true
		}
	}
	return current, false
}
