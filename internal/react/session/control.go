package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type Control = contract.RuntimeControlState
type Lifecycle = contract.LoopLifecycleState

func InitialControl() Control {
	return contract.RuntimeControlAwaitingUserQuery
}

func Transition(current *Control, next Control) {
	if current == nil {
		return
	}
	*current = next
}

func (s *State) Transition(next Control) {
	if s == nil {
		return
	}
	Transition(&s.Control, next)
}

func LifecycleFor(control Control) Lifecycle {
	switch control {
	case contract.RuntimeControlExited:
		return contract.LoopLifecycleExited
	case contract.RuntimeControlAwaitingUserQuery, contract.RuntimeControlUnset:
		return contract.LoopLifecycleAwaitingUserInput
	case contract.RuntimeControlAwaitingApproval:
		return contract.LoopLifecycleWaitingApproval
	case contract.RuntimeControlAwaitingContinuationChoice:
		return contract.LoopLifecycleWaitingContinuationChoice
	case contract.RuntimeControlAwaitingContinuationText:
		return contract.LoopLifecycleWaitingContinuationText
	default:
		return contract.LoopLifecycleModelTurn
	}
}
