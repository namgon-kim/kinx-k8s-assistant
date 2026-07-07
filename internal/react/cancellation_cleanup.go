package react

type runtimeCleanupPolicy struct {
	ClearPendingCalls           bool
	ClearResponseDirectives     bool
	ClearDirectionState         bool
	ClearMutationContinuation   bool
	ClearMutationVerification   bool
	ClearToolDispatchInProgress bool
}

func (l *Loop) applyRuntimeCleanup(policy runtimeCleanupPolicy) {
	if l == nil {
		return
	}
	if policy.ClearPendingCalls {
		l.pendingCalls = nil
	}
	if policy.ClearResponseDirectives {
		l.finalReportRequested = false
		l.guidedPhaseProgressRequested = false
		l.pendingResponseDirective = ""
	}
	if policy.ClearDirectionState {
		l.pendingFinalReport = nil
		l.pendingNextDirections = nil
		l.pendingDirectionPrompt = nil
	}
	if policy.ClearMutationContinuation {
		l.mutationContinuationRequired = false
		l.mutationContinuationAttempts = 0
	}
	if policy.ClearMutationVerification {
		l.pendingMutationVerification = nil
	}
	if policy.ClearToolDispatchInProgress {
		l.toolDispatchInProgress = false
	}
}

func cleanupApprovalDeclinedPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearPendingCalls: true,
	}
}

func cleanupDirectionPromptPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearDirectionState:     true,
		ClearResponseDirectives: true,
	}
}

func cleanupExitPolicy() runtimeCleanupPolicy {
	return runtimeCleanupPolicy{
		ClearPendingCalls:           true,
		ClearDirectionState:         true,
		ClearResponseDirectives:     true,
		ClearMutationContinuation:   true,
		ClearToolDispatchInProgress: true,
	}
}
