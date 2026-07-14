package verification

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type Requirement struct {
	ID       string
	Target   contract.ActionTarget
	Evidence string
}

func ForMutation(action contract.Action) []Requirement {
	if action.Target == nil || action.ModifiesResource == "" {
		return nil
	}
	return []Requirement{{ID: "direct", Target: *action.Target, Evidence: action.ExpectedObservation}}
}
