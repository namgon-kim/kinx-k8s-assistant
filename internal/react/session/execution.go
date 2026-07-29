package session

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

// GoalExecutionState is the mutable execution record for one accepted user
// request. Workflow packages decide transitions; session only owns storage.
type GoalExecutionState struct {
	SessionID                    string
	RequestID                    string
	Goal                         contract.GoalContract
	PlanRevision                 int
	PlanRevisions                []contract.PhasePlanRevision
	Phases                       []contract.PhaseContract
	ActivePhaseID                string
	ActiveStepID                 string
	PhaseStatus                  map[string]contract.PhaseStatus
	StepStatus                   map[string]contract.StepStatus
	CriterionResults             map[string]contract.CriterionResult
	Ledger                       ExecutionLedger
	AppliedBudget                contract.BudgetPolicy
	VerificationEvidenceAttempts map[string]int
	BlockedObligations           []string
	Corrections                  map[contract.CorrectionKey]contract.CorrectionState
	EventSequence                int
}

func (s *GoalExecutionState) AppendAttempt(attempt contract.AttemptRecord) error {
	return s.Ledger.AppendAttempt(attempt)
}

func (s *GoalExecutionState) UpdateAttempt(id string, update func(*contract.AttemptRecord)) bool {
	return s.Ledger.UpdateAttempt(id, update)
}

func (s *GoalExecutionState) AttemptByID(id string) (contract.AttemptRecord, bool) {
	return s.Ledger.AttemptByID(id)
}

func (s *GoalExecutionState) OrderedAttempts() []contract.AttemptRecord {
	return s.Ledger.OrderedAttempts()
}

func (s *GoalExecutionState) AttemptCountForLineage(lineageID string) int {
	return s.Ledger.AttemptCountForLineage(lineageID)
}

func (s *GoalExecutionState) LatestAttemptForLineage(lineageID string) (contract.AttemptRecord, bool) {
	return s.Ledger.LatestAttemptForLineage(lineageID)
}

func (s *GoalExecutionState) AppendObservation(observation contract.ObservationRecord) error {
	return s.Ledger.AppendObservation(observation)
}

func (s *GoalExecutionState) LinkObservationToAttempt(attemptID, observationID string) bool {
	return s.Ledger.LinkObservationToAttempt(attemptID, observationID)
}

func (s *GoalExecutionState) ObservationByID(id string) (contract.ObservationRecord, bool) {
	return s.Ledger.ObservationByID(id)
}

func (s *GoalExecutionState) OrderedObservations() []contract.ObservationRecord {
	return s.Ledger.OrderedObservations()
}

func (s *GoalExecutionState) ObservationPosition(id string) (int, bool) {
	return s.Ledger.ObservationPosition(id)
}

func CloneGoalExecutionState(source *GoalExecutionState) *GoalExecutionState {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Goal = cloneGoal(source.Goal)
	clone.PlanRevisions = make([]contract.PhasePlanRevision, len(source.PlanRevisions))
	for i := range source.PlanRevisions {
		clone.PlanRevisions[i] = clonePlanRevision(source.PlanRevisions[i])
	}
	clone.Phases = make([]contract.PhaseContract, len(source.Phases))
	for i := range source.Phases {
		clone.Phases[i] = clonePhase(source.Phases[i])
	}
	clone.PhaseStatus = cloneMap(source.PhaseStatus)
	clone.StepStatus = cloneMap(source.StepStatus)
	clone.CriterionResults = make(map[string]contract.CriterionResult, len(source.CriterionResults))
	for key, result := range source.CriterionResults {
		result.EvidenceRefs = append([]string(nil), result.EvidenceRefs...)
		clone.CriterionResults[key] = result
	}
	clone.Ledger = source.Ledger.Clone()
	clone.AppliedBudget = cloneBudget(source.AppliedBudget)
	clone.VerificationEvidenceAttempts = cloneMap(source.VerificationEvidenceAttempts)
	clone.BlockedObligations = append([]string(nil), source.BlockedObligations...)
	clone.Corrections = cloneMap(source.Corrections)
	return &clone
}

func clonePlanRevision(source contract.PhasePlanRevision) contract.PhasePlanRevision {
	clone := source
	clone.EvidenceRefs = append([]string(nil), source.EvidenceRefs...)
	clone.SupersededPhaseIDs = append([]string(nil), source.SupersededPhaseIDs...)
	clone.SupersededStepIDs = append([]string(nil), source.SupersededStepIDs...)
	clone.StepLineageMappings = append([]contract.StepLineageMapping(nil), source.StepLineageMappings...)
	clone.RemainingPhases = make([]contract.PhaseContract, len(source.RemainingPhases))
	for i := range source.RemainingPhases {
		clone.RemainingPhases[i] = clonePhase(source.RemainingPhases[i])
	}
	return clone
}

func cloneGoal(source contract.GoalContract) contract.GoalContract {
	clone := source
	clone.CompletionCriteria = append([]contract.CriterionContract(nil), source.CompletionCriteria...)
	return clone
}

func clonePhase(source contract.PhaseContract) contract.PhaseContract {
	clone := source
	clone.CompletionCriteria = append([]contract.CriterionContract(nil), source.CompletionCriteria...)
	clone.AllowedNext = append([]string(nil), source.AllowedNext...)
	clone.Steps = make([]contract.StepContract, len(source.Steps))
	for i, step := range source.Steps {
		step.CompletionCriteria = append([]contract.CriterionContract(nil), step.CompletionCriteria...)
		step.RequiredEvidence = append([]contract.EvidenceKind(nil), step.RequiredEvidence...)
		clone.Steps[i] = step
	}
	return clone
}

func cloneBudget(source contract.BudgetPolicy) contract.BudgetPolicy {
	clone := source
	clone.StepAttempts = cloneMap(source.StepAttempts)
	clone.CorrectionRetries = cloneMap(source.CorrectionRetries)
	return clone
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	clone := make(map[K]V, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
