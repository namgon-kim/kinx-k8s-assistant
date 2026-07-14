package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type PhaseState struct {
	Plan        *contract.PhasePlan
	Current     contract.PhaseRef
	Completed   map[int]bool
	ActiveSteps []contract.StepRuntime
}

func (s *PhaseState) Reset() {
	*s = PhaseState{}
}
