package phase

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestValidateRevisionPreservesLineageAndAttemptBudget(t *testing.T) {
	proposed, context := validRevisionFixture()

	result := ValidateRevision(proposed, context)
	if !result.Valid() {
		t.Fatalf("revision rejected: %s: %s", result.Code, result.Message)
	}
	phase := result.Revision.RemainingPhases[0]
	if phase.Index != 2 {
		t.Fatalf("normalized phase index = %d, want 2", phase.Index)
	}
	if got := phase.Steps[0].MaxAttempts; got != 4 {
		t.Fatalf("inherited max attempts = %d, want 4", got)
	}
	if phase.PhaseLineageID != "phase-lineage-1" || phase.Steps[0].GoalLineageID != "step-lineage-1" {
		t.Fatalf("lineage was not preserved: %#v", phase)
	}
}

func TestValidateRevisionAllowsEvidenceGroundedNewGoalWithoutLineageMapping(t *testing.T) {
	proposed, context := validRevisionFixture()
	proposed.StepLineageMappings = nil
	proposed.RemainingPhases[0].Steps[0].GoalLineageID = "new-node-goal-lineage"
	context.AppliedBudget.StepAttempts[contract.StepGeneralAction] = 3

	result := ValidateRevision(proposed, context)
	if !result.Valid() {
		t.Fatalf("new goal revision rejected: %s: %s", result.Code, result.Message)
	}
	if got := result.Revision.RemainingPhases[0].Steps[0].MaxAttempts; got != 3 {
		t.Fatalf("new goal max attempts = %d, want applied default 3", got)
	}
}

func TestValidateRevisionNormalizesAcceptedReferences(t *testing.T) {
	proposed, context := validRevisionFixture()
	proposed.EvidenceRefs[0] = " observation-1 "
	proposed.SupersededPhaseIDs[0] = " phase-1 "
	proposed.SupersededStepIDs[0] = " step-1 "
	proposed.RemainingPhases[0].ID = " phase-2 "
	proposed.RemainingPhases[0].Steps[0].ID = " step-2 "
	proposed.StepLineageMappings[0].ReplacementStepID = " step-2 "
	proposed.ActivePhaseID = " phase-2 "
	proposed.ActiveStepID = " step-2 "

	result := ValidateRevision(proposed, context)
	if !result.Valid() {
		t.Fatalf("normalized revision rejected: %s: %s", result.Code, result.Message)
	}
	if result.Revision.EvidenceRefs[0] != "observation-1" ||
		result.Revision.ActivePhaseID != "phase-2" ||
		result.Revision.ActiveStepID != "step-2" {
		t.Fatalf("revision references were not normalized: %#v", result.Revision)
	}
}

func TestValidateRevisionRejectsUnsafeGraphChanges(t *testing.T) {
	tests := []struct {
		name string
		edit func(*contract.PhasePlanRevision, *RevisionContext)
		code string
	}{
		{
			name: "mandatory obligation",
			edit: func(_ *contract.PhasePlanRevision, context *RevisionContext) {
				context.HasActiveMandatoryObligation = true
			},
			code: "plan_revision_mandatory_obligation",
		},
		{
			name: "stale base",
			edit: func(revision *contract.PhasePlanRevision, _ *RevisionContext) {
				revision.BaseRevision = 2
			},
			code: "plan_revision_stale_base",
		},
		{
			name: "unknown evidence",
			edit: func(revision *contract.PhasePlanRevision, _ *RevisionContext) {
				revision.EvidenceRefs = []string{"missing"}
			},
			code: "plan_revision_unknown_evidence",
		},
		{
			name: "lineage reset",
			edit: func(revision *contract.PhasePlanRevision, _ *RevisionContext) {
				revision.RemainingPhases[0].Steps[0].GoalLineageID = "reset-lineage"
			},
			code: "plan_revision_step_lineage_mismatch",
		},
		{
			name: "unmapped lineage reuse",
			edit: func(revision *contract.PhasePlanRevision, _ *RevisionContext) {
				revision.StepLineageMappings = nil
			},
			code: "plan_revision_reused_new_step_lineage",
		},
		{
			name: "unknown mapped replacement",
			edit: func(revision *contract.PhasePlanRevision, _ *RevisionContext) {
				revision.StepLineageMappings[0].ReplacementStepID = "missing-step"
				revision.RemainingPhases[0].Steps[0].GoalLineageID = "new-node-goal-lineage"
			},
			code: "plan_revision_unknown_replacement_step",
		},
		{
			name: "terminal history",
			edit: func(_ *contract.PhasePlanRevision, context *RevisionContext) {
				context.PhaseStatus["phase-1"] = contract.PhaseCompleted
			},
			code: "plan_revision_terminal_history",
		},
		{
			name: "superseded history",
			edit: func(_ *contract.PhasePlanRevision, context *RevisionContext) {
				context.PhaseStatus["phase-1"] = contract.PhaseSuperseded
			},
			code: "plan_revision_terminal_history",
		},
		{
			name: "revision budget exhausted",
			edit: func(_ *contract.PhasePlanRevision, context *RevisionContext) {
				context.CurrentRevision = 3
				context.MaxPlanRevisions = 2
			},
			code: "plan_revision_budget_exhausted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision, context := validRevisionFixture()
			tt.edit(&revision, &context)
			if tt.name == "revision budget exhausted" {
				revision.BaseRevision = context.CurrentRevision
			}
			result := ValidateRevision(revision, context)
			if result.Code != tt.code {
				t.Fatalf("code = %q, want %q (%s)", result.Code, tt.code, result.Message)
			}
		})
	}
}

func validRevisionFixture() (contract.PhasePlanRevision, RevisionContext) {
	oldStep := contract.StepContract{
		ID:            "step-1",
		GoalLineageID: "step-lineage-1",
		Kind:          contract.StepGeneralAction,
		Index:         1,
		Goal:          "inspect workload state",
		CompletionCriteria: []contract.CriterionContract{{
			ID:                   "step-criterion-1",
			Description:          "workload state observed",
			RequiredEvidenceKind: contract.EvidenceObservation,
		}},
		RequiredEvidence: []contract.EvidenceKind{contract.EvidenceObservation},
		MaxAttempts:      4,
	}
	oldPhase := contract.PhaseContract{
		ID:             "phase-1",
		PhaseLineageID: "phase-lineage-1",
		Index:          1,
		Name:           "workload_check",
		Goal:           "diagnose workload",
		CompletionCriteria: []contract.CriterionContract{{
			ID:                   "phase-criterion-1",
			Description:          "workload diagnosis complete",
			RequiredEvidenceKind: contract.EvidenceObservation,
		}},
		Steps: []contract.StepContract{oldStep},
	}
	replacementStep := oldStep
	replacementStep.ID = "step-2"
	replacementStep.Goal = "inspect node state"
	replacementStep.MaxAttempts = 99
	replacementStep.CompletionCriteria = []contract.CriterionContract{{
		ID:                   "step-criterion-2",
		Description:          "node state observed",
		RequiredEvidenceKind: contract.EvidenceObservation,
	}}
	replacementPhase := contract.PhaseContract{
		ID:              "phase-2",
		PhaseLineageID:  oldPhase.PhaseLineageID,
		ReplacesPhaseID: oldPhase.ID,
		Name:            "node_check",
		Goal:            "follow the observed node blocker",
		CompletionCriteria: []contract.CriterionContract{{
			ID:                   "phase-criterion-2",
			Description:          "node diagnosis complete",
			RequiredEvidenceKind: contract.EvidenceObservation,
		}},
		Steps: []contract.StepContract{replacementStep},
	}
	revision := contract.PhasePlanRevision{
		BaseRevision:       1,
		Reason:             "the workload observation identifies a node blocker",
		EvidenceRefs:       []string{"observation-1"},
		SupersededPhaseIDs: []string{oldPhase.ID},
		SupersededStepIDs:  []string{oldStep.ID},
		RemainingPhases:    []contract.PhaseContract{replacementPhase},
		StepLineageMappings: []contract.StepLineageMapping{{
			PreviousStepID:    oldStep.ID,
			ReplacementStepID: replacementStep.ID,
			GoalLineageID:     oldStep.GoalLineageID,
		}},
		ActivePhaseID: replacementPhase.ID,
		ActiveStepID:  replacementStep.ID,
	}
	context := RevisionContext{
		CurrentRevision:  1,
		MaxPlanRevisions: 2,
		Goal: contract.GoalContract{
			ID: "goal-1",
			CompletionCriteria: []contract.CriterionContract{{
				ID:                   "goal-criterion-1",
				Description:          "request answered",
				RequiredEvidenceKind: contract.EvidenceObservation,
			}},
		},
		ActivePhaseID:     oldPhase.ID,
		Phases:            []contract.PhaseContract{oldPhase},
		PhaseStatus:       map[string]contract.PhaseStatus{oldPhase.ID: contract.PhaseActive},
		StepStatus:        map[string]contract.StepStatus{oldStep.ID: contract.StepBlocked},
		EvidenceRecordIDs: map[string]struct{}{"observation-1": {}},
		AppliedBudget: contract.BudgetPolicy{StepAttempts: map[contract.StepKind]int{
			contract.StepGeneralAction: 4,
		}},
	}
	return revision, context
}
