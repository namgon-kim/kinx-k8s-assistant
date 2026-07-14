package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type VerificationRequirement struct {
	ID        string
	Resource  contract.ActionTarget
	Satisfied bool
	Skipped   bool
}

type VerificationState struct {
	Requirements   []VerificationRequirement
	AwaitingResult bool
	Attempts       int
}

func (s *VerificationState) Reset() {
	*s = VerificationState{}
}
