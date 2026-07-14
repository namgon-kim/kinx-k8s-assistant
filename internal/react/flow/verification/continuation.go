package verification

type Continuation string

const (
	ContinueEvidence Continuation = "evidence"
	ContinueResult   Continuation = "result"
	ContinueDone     Continuation = "done"
)

func Next(total, satisfied int, awaitingResult bool) Continuation {
	if satisfied < total {
		return ContinueEvidence
	}
	if awaitingResult {
		return ContinueResult
	}
	return ContinueDone
}
