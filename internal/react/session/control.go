package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type Control = contract.RuntimeControlState
type Lifecycle = contract.LoopLifecycleState

func LifecycleFor(control Control) Lifecycle {
	class, ok := contract.ClassifyRuntimeControlState(control)
	if !ok || class == contract.ControlExecutionInvalid {
		return contract.LoopLifecycleAwaitingUserInput
	}
	if class == contract.ControlExecutionModelTurn {
		return contract.LoopLifecycleModelTurn
	}
	switch control {
	case contract.RuntimeControlExited:
		return contract.LoopLifecycleExited
	case contract.RuntimeControlAwaitingApproval:
		return contract.LoopLifecycleWaitingApproval
	case contract.RuntimeControlAwaitingContinuationChoice:
		return contract.LoopLifecycleWaitingContinuationChoice
	case contract.RuntimeControlAwaitingContinuationText:
		return contract.LoopLifecycleWaitingContinuationText
	case contract.RuntimeControlAwaitingUserQuery, contract.RuntimeControlAwaitingToolResult:
		return contract.LoopLifecycleAwaitingUserInput
	default:
		return contract.LoopLifecycleAwaitingUserInput
	}
}
