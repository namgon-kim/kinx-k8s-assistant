package session

import (
	"fmt"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type ExecutionLedger struct {
	attemptOrder     []string
	attempts         map[string]contract.AttemptRecord
	observationOrder []string
	observations     map[string]contract.ObservationRecord
	observationIndex map[string]int
	lineageAttempts  map[string][]string
}

func (l *ExecutionLedger) AppendAttempt(attempt contract.AttemptRecord) error {
	if attempt.ID == "" {
		return fmt.Errorf("attempt ID is required")
	}
	l.ensure()
	if _, exists := l.attempts[attempt.ID]; exists {
		return fmt.Errorf("attempt %q already exists", attempt.ID)
	}
	attempt = cloneAttempt(attempt)
	l.attempts[attempt.ID] = attempt
	l.attemptOrder = append(l.attemptOrder, attempt.ID)
	if attempt.GoalLineageID != "" {
		l.lineageAttempts[attempt.GoalLineageID] = append(l.lineageAttempts[attempt.GoalLineageID], attempt.ID)
	}
	return nil
}

func (l *ExecutionLedger) UpdateAttempt(id string, update func(*contract.AttemptRecord)) bool {
	if l == nil || update == nil {
		return false
	}
	attempt, ok := l.attempts[id]
	if !ok {
		return false
	}
	beforeLineage := attempt.GoalLineageID
	update(&attempt)
	attempt = cloneAttempt(attempt)
	l.attempts[id] = attempt
	if attempt.GoalLineageID != beforeLineage {
		l.rebuildLineageIndex()
	}
	return true
}

func (l *ExecutionLedger) AttemptByID(id string) (contract.AttemptRecord, bool) {
	if l == nil {
		return contract.AttemptRecord{}, false
	}
	attempt, ok := l.attempts[id]
	return cloneAttempt(attempt), ok
}

func (l *ExecutionLedger) OrderedAttempts() []contract.AttemptRecord {
	if l == nil {
		return nil
	}
	out := make([]contract.AttemptRecord, 0, len(l.attemptOrder))
	for _, id := range l.attemptOrder {
		if attempt, ok := l.attempts[id]; ok {
			out = append(out, cloneAttempt(attempt))
		}
	}
	return out
}

func (l *ExecutionLedger) AttemptCountForLineage(lineageID string) int {
	if l == nil {
		return 0
	}
	count := 0
	for _, id := range l.lineageAttempts[lineageID] {
		if attempt, ok := l.attempts[id]; ok && attempt.Status != contract.AttemptCancelled {
			count++
		}
	}
	return count
}

func (l *ExecutionLedger) LatestAttemptForLineage(lineageID string) (contract.AttemptRecord, bool) {
	if l == nil {
		return contract.AttemptRecord{}, false
	}
	ids := l.lineageAttempts[lineageID]
	for i := len(ids) - 1; i >= 0; i-- {
		attempt, ok := l.AttemptByID(ids[i])
		if ok && attempt.Status != contract.AttemptCancelled {
			return attempt, true
		}
	}
	return contract.AttemptRecord{}, false
}

func (l *ExecutionLedger) LinkObservationToAttempt(attemptID, observationID string) bool {
	if l == nil {
		return false
	}
	if _, ok := l.observations[observationID]; !ok {
		return false
	}
	return l.UpdateAttempt(attemptID, func(attempt *contract.AttemptRecord) {
		for _, existing := range attempt.ObservationRefs {
			if existing == observationID {
				return
			}
		}
		attempt.ObservationRefs = append(attempt.ObservationRefs, observationID)
	})
}

func (l *ExecutionLedger) AppendObservation(observation contract.ObservationRecord) error {
	if observation.ID == "" {
		return fmt.Errorf("observation ID is required")
	}
	l.ensure()
	if _, exists := l.observations[observation.ID]; exists {
		return fmt.Errorf("observation %q already exists", observation.ID)
	}
	observation = cloneObservation(observation)
	l.observations[observation.ID] = observation
	l.observationIndex[observation.ID] = len(l.observationOrder)
	l.observationOrder = append(l.observationOrder, observation.ID)
	return nil
}

func (l *ExecutionLedger) ObservationByID(id string) (contract.ObservationRecord, bool) {
	if l == nil {
		return contract.ObservationRecord{}, false
	}
	observation, ok := l.observations[id]
	return cloneObservation(observation), ok
}

func (l *ExecutionLedger) OrderedObservations() []contract.ObservationRecord {
	if l == nil {
		return nil
	}
	out := make([]contract.ObservationRecord, 0, len(l.observationOrder))
	for _, id := range l.observationOrder {
		if observation, ok := l.observations[id]; ok {
			out = append(out, cloneObservation(observation))
		}
	}
	return out
}

func (l *ExecutionLedger) ObservationPosition(id string) (int, bool) {
	if l == nil {
		return 0, false
	}
	position, ok := l.observationIndex[id]
	return position, ok
}

func (l *ExecutionLedger) Clone() ExecutionLedger {
	if l == nil {
		return ExecutionLedger{}
	}
	clone := ExecutionLedger{
		attemptOrder:     append([]string(nil), l.attemptOrder...),
		attempts:         make(map[string]contract.AttemptRecord, len(l.attempts)),
		observationOrder: append([]string(nil), l.observationOrder...),
		observations:     make(map[string]contract.ObservationRecord, len(l.observations)),
	}
	for id, attempt := range l.attempts {
		clone.attempts[id] = cloneAttempt(attempt)
	}
	for id, observation := range l.observations {
		clone.observations[id] = cloneObservation(observation)
	}
	clone.rebuildObservationIndex()
	clone.rebuildLineageIndex()
	return clone
}

func (l *ExecutionLedger) AuditError() string {
	if l == nil {
		return ""
	}
	if len(l.attemptOrder) != len(l.attempts) {
		return "attempt ledger order and record counts differ"
	}
	seen := map[string]struct{}{}
	for _, id := range l.attemptOrder {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Sprintf("attempt ledger order contains duplicate %q", id)
		}
		seen[id] = struct{}{}
		if _, ok := l.attempts[id]; !ok {
			return fmt.Sprintf("attempt ledger order references missing record %q", id)
		}
	}
	if len(l.observationOrder) != len(l.observations) {
		return "observation ledger order and record counts differ"
	}
	seen = map[string]struct{}{}
	for _, id := range l.observationOrder {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Sprintf("observation ledger order contains duplicate %q", id)
		}
		seen[id] = struct{}{}
		if _, ok := l.observations[id]; !ok {
			return fmt.Sprintf("observation ledger order references missing record %q", id)
		}
		if position, ok := l.observationIndex[id]; !ok || position != len(seen)-1 {
			return fmt.Sprintf("observation ledger index is inconsistent for %q", id)
		}
	}
	for lineageID, ids := range l.lineageAttempts {
		for _, id := range ids {
			attempt, ok := l.attempts[id]
			if !ok || attempt.GoalLineageID != lineageID {
				return fmt.Sprintf("attempt ledger lineage index is inconsistent for %q", id)
			}
		}
	}
	for id, attempt := range l.attempts {
		if attempt.GoalLineageID == "" {
			continue
		}
		found := false
		for _, indexedID := range l.lineageAttempts[attempt.GoalLineageID] {
			if indexedID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("attempt ledger lineage index omits %q", id)
		}
	}
	return ""
}

func (l *ExecutionLedger) ensure() {
	if l.attempts == nil {
		l.attempts = map[string]contract.AttemptRecord{}
	}
	if l.observations == nil {
		l.observations = map[string]contract.ObservationRecord{}
	}
	if l.observationIndex == nil {
		l.rebuildObservationIndex()
	}
	if l.lineageAttempts == nil {
		l.lineageAttempts = map[string][]string{}
	}
}

func (l *ExecutionLedger) rebuildObservationIndex() {
	l.observationIndex = map[string]int{}
	for position, id := range l.observationOrder {
		l.observationIndex[id] = position
	}
}

func (l *ExecutionLedger) rebuildLineageIndex() {
	l.lineageAttempts = map[string][]string{}
	for _, id := range l.attemptOrder {
		attempt, ok := l.attempts[id]
		if ok && attempt.GoalLineageID != "" {
			l.lineageAttempts[attempt.GoalLineageID] = append(l.lineageAttempts[attempt.GoalLineageID], id)
		}
	}
}

func cloneAttempt(attempt contract.AttemptRecord) contract.AttemptRecord {
	attempt.Action.Arguments = contract.CloneDataMap(attempt.Action.Arguments)
	attempt.ObservationRefs = append([]string(nil), attempt.ObservationRefs...)
	attempt.ChangedSince = append([]string(nil), attempt.ChangedSince...)
	return attempt
}

func cloneObservation(observation contract.ObservationRecord) contract.ObservationRecord {
	observation.Result = contract.CloneDataMap(observation.Result)
	observation.Clues = append([]string(nil), observation.Clues...)
	return observation
}
