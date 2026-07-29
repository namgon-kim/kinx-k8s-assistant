package coordinator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	phaseflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/phase"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
)

func phasePlanRevisionFromFunctionCall(call gollm.FunctionCall) (phasePlanRevision, bool) {
	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		return phasePlanRevision{}, false
	}
	var revision phasePlanRevision
	if err := json.Unmarshal(raw, &revision); err != nil {
		return phasePlanRevision{}, false
	}
	return revision, true
}

func (l *Loop) consumePhasePlanRevision(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != protocol.PhasePlanRevisionCall {
			remaining = append(remaining, call)
			continue
		}
		revision, ok := phasePlanRevisionFromFunctionCall(call)
		if !ok {
			return nil, l.rejectPhasePlanRevision(
				"plan_revision_invalid_payload",
				"phase_plan_revision must match the declared schema and reference the active plan revision.",
			)
		}
		result := phaseflow.ValidateRevision(revision, l.phasePlanRevisionContext())
		if !result.Valid() {
			return nil, l.rejectPhasePlanRevision(result.Code, result.Message)
		}
		l.applyPhasePlanRevision(result.Revision)
		l.appendFunctionCallResult(call, map[string]any{
			"status":          "accepted",
			"plan_revision":   l.mutableRuntime().execution.PlanRevision,
			"active_phase_id": result.Revision.ActivePhaseID,
			"active_step_id":  result.Revision.ActiveStepID,
		})
		l.mutableRuntime().currIteration++
		l.transitionAfterPhaseAdvance()
		return nil, true
	}
	return remaining, false
}

func (l *Loop) rejectPhasePlanRevision(code, reason string) bool {
	return l.applyModelOutputCorrectionGate(
		code,
		"phase plan revision이 현재 실행 계약과 맞지 않아 적용하지 않았습니다.",
		"The phase_plan_revision was rejected before state mutation: "+strings.TrimSpace(reason)+
			". Keep the current accepted plan unchanged and return one corrected phase_plan_revision without an action.",
	)
}

func (l *Loop) phasePlanRevisionContext() phaseflow.RevisionContext {
	execution := l.mutableRuntime().execution
	if execution == nil {
		return phaseflow.RevisionContext{}
	}
	observations := execution.OrderedObservations()
	evidenceRecordIDs := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		evidenceRecordIDs[observation.ID] = struct{}{}
	}
	return phaseflow.RevisionContext{
		CurrentRevision:              execution.PlanRevision,
		MaxPlanRevisions:             execution.Goal.MaxPlanRevisions,
		Goal:                         execution.Goal,
		ActivePhaseID:                execution.ActivePhaseID,
		Phases:                       execution.Phases,
		PhaseStatus:                  execution.PhaseStatus,
		StepStatus:                   execution.StepStatus,
		EvidenceRecordIDs:            evidenceRecordIDs,
		AppliedBudget:                execution.AppliedBudget,
		HasActiveMandatoryObligation: l.mutableRuntime().pendingMutationVerification != nil,
	}
}

func (l *Loop) applyPhasePlanRevision(revision phasePlanRevision) {
	execution := l.mutableRuntime().execution
	if execution == nil {
		return
	}
	for _, phaseID := range revision.SupersededPhaseIDs {
		l.resetCorrectionScope(string(RetryScopeCurrentPhase), phaseID, "")
		execution.PhaseStatus[phaseID] = contract.PhaseSuperseded
	}
	for _, stepID := range revision.SupersededStepIDs {
		l.resetCorrectionScope(string(RetryScopeCurrentStep), "", stepID)
		execution.StepStatus[stepID] = contract.StepSuperseded
	}
	for _, phase := range revision.RemainingPhases {
		execution.Phases = append(execution.Phases, phase)
		execution.PhaseStatus[phase.ID] = contract.PhasePending
		for _, step := range phase.Steps {
			execution.StepStatus[step.ID] = contract.StepPending
		}
	}
	execution.PlanRevision++
	execution.PlanRevisions = append(execution.PlanRevisions, revision)
	execution.ActivePhaseID = revision.ActivePhaseID
	execution.ActiveStepID = revision.ActiveStepID
	execution.PhaseStatus[revision.ActivePhaseID] = contract.PhaseActive
	execution.StepStatus[revision.ActiveStepID] = contract.StepActive

	l.mutableRuntime().phaseStepState = phaseStateFromRevision(execution.Goal.Statement, revision)
	activePhaseName := strings.ToLower(strings.TrimSpace(l.mutableRuntime().phaseStepState.currentStep().Name))
	if activePhaseName != "guided_diagnosis" {
		l.mutableRuntime().guideStepState = nil
		l.mutableRuntime().resourceGuideInjected = false
	}
	l.queueResponseDirective(fmt.Sprintf(
		"Plan revision %d was accepted. Continue only with active_phase_id=%s and active_step_id=%s. The superseded history remains authoritative and its attempt budgets continue by goal_lineage_id.",
		execution.PlanRevision,
		revision.ActivePhaseID,
		revision.ActiveStepID,
	))
}

func phaseStateFromRevision(requestGoal string, revision phasePlanRevision) *phaseStepState {
	state := &phaseStepState{
		RequestGoal: strings.TrimSpace(requestGoal),
		Completed:   map[int]bool{},
	}
	for _, phase := range revision.RemainingPhases {
		step := phaseStep{
			Index:               phase.Index,
			Name:                phase.Name,
			Goal:                phase.Goal,
			CompletionCondition: firstCriterionDescription(phase.CompletionCriteria),
			AllowedNext:         append([]string(nil), phase.AllowedNext...),
		}
		for _, contractStep := range phase.Steps {
			step.Steps = append(step.Steps, phaseExecutionStep{
				ID:              contractStep.ID,
				Index:           contractStep.Index,
				Kind:            string(contractStep.Kind),
				Description:     contractStep.Goal,
				ExpectedOutcome: firstCriterionDescription(contractStep.CompletionCriteria),
			})
		}
		state.PhaseSteps = append(state.PhaseSteps, step)
		if phase.ID == revision.ActivePhaseID {
			state.CurrentPhaseIndex = phase.Index
		}
	}
	return state
}

func firstCriterionDescription(criteria []contract.CriterionContract) string {
	if len(criteria) == 0 {
		return ""
	}
	return strings.TrimSpace(criteria[0].Description)
}
