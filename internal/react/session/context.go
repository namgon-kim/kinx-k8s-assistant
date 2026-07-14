package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type ContextState struct {
	OriginalQuery       string
	Requirement         *contract.RequirementAnalysis
	Request             *contract.RequestContext
	ApproxTokens        int
	LastCompactedAction int
	BlockHashes         map[string]struct{}
}

func (s *ContextState) ResetRequest() {
	*s = ContextState{BlockHashes: make(map[string]struct{})}
}
