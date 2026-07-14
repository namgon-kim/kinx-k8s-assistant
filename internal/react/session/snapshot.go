package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

func (s *State) Snapshot() contract.RuntimeSnapshot {
	if s == nil {
		return contract.RuntimeSnapshot{Control: contract.RuntimeControlUnset}
	}
	snapshot := contract.RuntimeSnapshot{
		Lifecycle:     LifecycleFor(s.Control),
		Control:       s.Control,
		OriginalQuery: s.Context.OriginalQuery,
		ActiveSteps:   append([]contract.StepRuntime(nil), s.Phase.ActiveSteps...),
	}
	if s.Phase.Plan != nil {
		phases := make([]contract.PhaseRuntimeSpec, 0, len(s.Phase.Plan.PhaseSteps))
		for _, phase := range s.Phase.Plan.PhaseSteps {
			phases = append(phases, contract.PhaseRuntimeSpec{
				Ref:                 contract.PhaseRef{Index: phase.Index, Name: phase.Name},
				Goal:                phase.Goal,
				CompletionCondition: phase.CompletionCondition,
				AllowedNext:         append([]string(nil), phase.AllowedNext...),
			})
		}
		snapshot.Phase = &contract.PhaseRuntime{
			RequestGoal: s.Phase.Plan.RequestGoal,
			Active:      s.Phase.Current,
			Phases:      phases,
			Completed:   cloneCompleted(s.Phase.Completed),
		}
	}
	return snapshot
}

func cloneCompleted(source map[int]bool) map[int]bool {
	if source == nil {
		return nil
	}
	result := make(map[int]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
