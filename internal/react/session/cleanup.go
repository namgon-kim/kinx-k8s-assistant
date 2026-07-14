package session

type CleanupScope int

const (
	CleanupRequest CleanupScope = iota
	CleanupPhase
	CleanupVerification
	CleanupAll
)

func (s *State) Cleanup(scope CleanupScope) {
	if s == nil {
		return
	}
	switch scope {
	case CleanupRequest:
		s.Context.ResetRequest()
	case CleanupPhase:
		s.Phase.Reset()
	case CleanupVerification:
		s.Verification.Reset()
	case CleanupAll:
		*s = *New()
	}
}
