package react

import "strings"

type phaseScopedResetPolicy struct {
	ResetGuide                 bool
	ResetResourceGuideLookup   bool
	ResetMutationVerification  bool
	ClearResponseDirectives    bool
	ClearPendingFinalDirection bool
	TrimCompletedActions       bool
}

func (l *Loop) resetPhaseScopedState(from PhaseRef, policy phaseScopedResetPolicy) {
	if l == nil {
		return
	}
	if policy.ResetGuide {
		l.guideStepState = nil
	}
	if policy.ResetResourceGuideLookup {
		l.resourceGuideInjected = false
		l.resourceGuideQueries = nil
	}
	if policy.ResetMutationVerification {
		l.pendingMutationVerification = nil
		l.mutationContinuationRequired = false
		l.mutationContinuationAttempts = 0
	}
	if policy.ClearResponseDirectives {
		l.finalReportRequested = false
		l.guidedPhaseProgressRequested = false
		l.pendingResponseDirective = ""
	}
	if policy.ClearPendingFinalDirection {
		l.pendingFinalReport = nil
		l.pendingNextDirections = nil
		l.pendingDirectionPrompt = nil
	}
	if policy.TrimCompletedActions {
		l.trimCompletedActionsFromPhase(from)
	}
}

func (l *Loop) defaultPhaseScopedResetPolicy(from PhaseRef) phaseScopedResetPolicy {
	policy := phaseScopedResetPolicy{
		ClearResponseDirectives:    true,
		ClearPendingFinalDirection: true,
		TrimCompletedActions:       true,
	}
	if l == nil || l.phaseStepState == nil {
		return policy
	}
	for _, ref := range l.phaseStepState.phasesAtOrAfter(from) {
		name := strings.ToLower(strings.TrimSpace(ref.Name))
		switch {
		case strings.Contains(name, "guidance"), strings.Contains(name, "guided"):
			policy.ResetGuide = true
			policy.ResetResourceGuideLookup = true
		case strings.Contains(name, "mutation"), strings.Contains(name, "remediation"), strings.Contains(name, "verification"):
			policy.ResetMutationVerification = true
		}
	}
	return policy
}

func (l *Loop) trimCompletedActionsFromPhase(from PhaseRef) {
	if l == nil || len(l.completedActions) == 0 || l.phaseStepState == nil {
		return
	}
	var kept []actionRecord
	for _, action := range l.completedActions {
		if action.Phase == nil || action.Phase.Index == 0 {
			kept = append(kept, action)
			continue
		}
		if action.Phase.Index < from.Index {
			kept = append(kept, action)
		}
	}
	l.completedActions = kept
}
