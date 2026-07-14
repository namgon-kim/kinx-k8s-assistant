package verification

type Continuation string

const (
	ContinueEvidence Continuation = "evidence"
	ContinueResult   Continuation = "result"
	ContinueDone     Continuation = "done"
)

func Next(hasRemaining, awaitingResult bool) Continuation {
	if hasRemaining {
		return ContinueEvidence
	}
	if awaitingResult {
		return ContinueResult
	}
	return ContinueDone
}
