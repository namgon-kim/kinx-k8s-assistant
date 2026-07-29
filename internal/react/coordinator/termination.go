package coordinator

import (
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func (l *Loop) finalizePendingMutationVerification(cause string, status contract.AttemptStatus) string {
	if l == nil {
		return ""
	}
	verification := l.mutableRuntime().pendingMutationVerification
	if verification == nil {
		return ""
	}
	if status == "" {
		status = contract.AttemptUnknown
	}
	cause = strings.TrimSpace(cause)
	if cause == "" {
		cause = "mutation verification ended without a conclusive result"
	}

	checkID := ""
	if check := verification.activeCheck(); check != nil {
		checkID = check.ID
		if status == contract.AttemptFailed {
			check.Status = contract.VerificationCheckFailed
		}
	}
	l.setAttemptStatus(verification.AttemptID, status)
	unresolved := status == contract.AttemptUnknown || status == contract.AttemptCancelled
	if unresolved {
		l.appendBlockedObligation(verification)
		l.mutableRuntime().finalReportMustBeInconclusive = true
	}
	l.mutableRuntime().pendingMutationVerification = nil
	l.mutableRuntime().mutationContinuationAttempts = 0
	l.mutableRuntime().pendingResponseDirective = ""
	l.discardQueuedVerificationWaits()

	target := verification.Owner.ID
	if target == "" {
		target = checkID
	}
	return fmt.Sprintf(
		"Mutation verification for %q ended as %s: %s. The mutation must not be repeated automatically.",
		target,
		status,
		cause,
	)
}

func (l *Loop) appendBlockedObligation(verification *pendingMutationVerification) {
	execution := l.mutableRuntime().execution
	if execution == nil || verification == nil {
		return
	}
	ownerID := strings.TrimSpace(verification.Owner.ID)
	if check := verification.activeCheck(); check != nil && strings.TrimSpace(check.ID) != "" {
		ownerID = strings.TrimSpace(check.ID)
	}
	key := fmt.Sprintf(
		"mutation_verification:%s:%s",
		strings.TrimSpace(verification.AttemptID),
		ownerID,
	)
	for _, existing := range execution.BlockedObligations {
		if existing == key {
			return
		}
	}
	execution.BlockedObligations = append(execution.BlockedObligations, key)
}

func (l *Loop) discardQueuedVerificationWaits() {
	if l.activeTransaction == nil || len(l.activeTransaction.effects) == 0 {
		return
	}
	effects := make([]contract.Effect, 0, len(l.activeTransaction.effects))
	for _, effect := range l.activeTransaction.effects {
		if effect.Kind != contract.EffectWait {
			effects = append(effects, effect)
		}
	}
	l.activeTransaction.effects = effects
}
