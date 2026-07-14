// Package session owns all mutable workflow state for one ReAct session.
package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type State struct {
	Control      contract.RuntimeControlState
	Phase        PhaseState
	Verification VerificationState
	Context      ContextState
}

func New() *State {
	return &State{Control: InitialControl()}
}
