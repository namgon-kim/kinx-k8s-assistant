package coordinator

import (
	"fmt"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
)

func TestConsumePhasePlanRevisionPreservesHistoryAndLineageBudget(t *testing.T) {
	loop := &Loop{runtimeState: &runtimeState{control: RuntimeControlAwaitingModelStep}}
	loop.mutableRuntime()
	loop.beginGoalExecution()
	loop.mutableRuntime().phaseStepState = newPhaseStepState(phasePlan{
		RequestGoal:       "diagnose workload",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{{
			Index:               1,
			Name:                "workload_check",
			Goal:                "inspect workload state",
			CompletionCondition: "workload state explained",
		}},
	})
	loop.acceptGoalExecutionPlan(phasePlan{
		RequestGoal:       "diagnose workload",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{{
			Index:               1,
			Name:                "workload_check",
			Goal:                "inspect workload state",
			CompletionCondition: "workload state explained",
		}},
	})

	execution := loop.mutableRuntime().execution
	oldPhase := execution.Phases[0]
	oldStep := oldPhase.Steps[0]
	execution.StepStatus[oldStep.ID] = contract.StepBlocked
	execution.ActiveStepID = ""
	observationID := loop.recordInternalObservation("kubectl", "the workload is blocked by node readiness")
	execution.BlockedObligations = []string{"mutation_verification:attempt-old:verification-old"}
	for i := 0; i < oldStep.MaxAttempts; i++ {
		if err := execution.AppendAttempt(contract.AttemptRecord{
			ID:            fmt.Sprintf("attempt-%d", i+1),
			StepID:        oldStep.ID,
			GoalLineageID: oldStep.GoalLineageID,
		}); err != nil {
			t.Fatal(err)
		}
	}

	replacementPhaseID := oldPhase.ID + ".revision-2"
	replacementStepID := oldStep.ID + ".revision-2"
	revision := phasePlanRevision{
		BaseRevision:       execution.PlanRevision,
		Reason:             "the latest observation identifies node readiness as the useful diagnostic direction",
		EvidenceRefs:       []string{observationID},
		SupersededPhaseIDs: []string{oldPhase.ID},
		SupersededStepIDs:  []string{oldStep.ID},
		RemainingPhases: []contract.PhaseContract{{
			ID:              replacementPhaseID,
			PhaseLineageID:  oldPhase.PhaseLineageID,
			ReplacesPhaseID: oldPhase.ID,
			Name:            "node_check",
			Goal:            "inspect the observed node blocker",
			CompletionCriteria: []contract.CriterionContract{{
				ID:                   replacementPhaseID + ".criterion-1",
				Description:          "node blocker explained",
				RequiredEvidenceKind: contract.EvidenceObservation,
			}},
			Steps: []contract.StepContract{{
				ID:            replacementStepID,
				GoalLineageID: oldStep.GoalLineageID,
				Kind:          oldStep.Kind,
				Goal:          "inspect node readiness",
				CompletionCriteria: []contract.CriterionContract{{
					ID:                   replacementStepID + ".criterion-1",
					Description:          "node readiness observed",
					RequiredEvidenceKind: contract.EvidenceObservation,
				}},
				RequiredEvidence: []contract.EvidenceKind{contract.EvidenceObservation},
			}},
		}},
		StepLineageMappings: []contract.StepLineageMapping{{
			PreviousStepID:    oldStep.ID,
			ReplacementStepID: replacementStepID,
			GoalLineageID:     oldStep.GoalLineageID,
		}},
		ActivePhaseID: replacementPhaseID,
		ActiveStepID:  replacementStepID,
	}
	arguments, err := toMap(revision)
	if err != nil {
		t.Fatalf("revision arguments: %v", err)
	}
	remaining, handled := loop.consumePhasePlanRevision([]gollm.FunctionCall{{
		Name:      protocol.PhasePlanRevisionCall,
		Arguments: arguments,
	}})
	if !handled || len(remaining) != 0 {
		t.Fatalf("revision result handled=%v remaining=%#v", handled, remaining)
	}
	if execution.PlanRevision != 2 || len(execution.PlanRevisions) != 1 {
		t.Fatalf("revision history = revision %d, records %d", execution.PlanRevision, len(execution.PlanRevisions))
	}
	if execution.PhaseStatus[oldPhase.ID] != contract.PhaseSuperseded ||
		execution.StepStatus[oldStep.ID] != contract.StepSuperseded {
		t.Fatalf("superseded history was not retained: phase=%s step=%s", execution.PhaseStatus[oldPhase.ID], execution.StepStatus[oldStep.ID])
	}
	if len(execution.BlockedObligations) != 1 ||
		execution.BlockedObligations[0] != "mutation_verification:attempt-old:verification-old" {
		t.Fatalf("unresolved obligation was not preserved: %#v", execution.BlockedObligations)
	}
	newStep := loop.activeExecutionStep()
	if newStep == nil || newStep.GoalLineageID != oldStep.GoalLineageID {
		t.Fatalf("replacement step lineage = %#v", newStep)
	}
	allowed, reason := loop.pendingAttemptAllowed(PendingCall{StepRef: &StepRef{
		Phase:         PhaseRef{ID: replacementPhaseID, LineageID: oldPhase.PhaseLineageID},
		Kind:          newStep.Kind,
		ID:            newStep.ID,
		GoalLineageID: newStep.GoalLineageID,
	}})
	if allowed || reason != "step attempt budget is exhausted" {
		t.Fatalf("lineage budget allowed=%v reason=%q", allowed, reason)
	}
}
