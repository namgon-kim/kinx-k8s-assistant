package phase

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

func Start(plan contract.PhasePlan) contract.PhaseRef {
	if len(plan.PhaseSteps) == 0 {
		return contract.PhaseRef{}
	}
	index := plan.CurrentPhaseIndex
	if index == 0 {
		index = plan.PhaseSteps[0].Index
	}
	for _, step := range plan.PhaseSteps {
		if step.Index == index {
			return contract.PhaseRef{Index: step.Index, Name: step.Name}
		}
	}
	return contract.PhaseRef{}
}
