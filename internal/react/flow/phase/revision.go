package phase

import (
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

type RevisionContext struct {
	CurrentRevision              int
	MaxPlanRevisions             int
	Goal                         contract.GoalContract
	ActivePhaseID                string
	Phases                       []contract.PhaseContract
	PhaseStatus                  map[string]contract.PhaseStatus
	StepStatus                   map[string]contract.StepStatus
	EvidenceRecordIDs            map[string]struct{}
	AppliedBudget                contract.BudgetPolicy
	HasActiveMandatoryObligation bool
}

type RevisionResult struct {
	Revision contract.PhasePlanRevision
	Code     string
	Message  string
}

func (r RevisionResult) Valid() bool {
	return r.Code == ""
}

func ValidateRevision(proposed contract.PhasePlanRevision, ctx RevisionContext) RevisionResult {
	reject := func(code, message string) RevisionResult {
		return RevisionResult{Code: code, Message: message}
	}
	proposed = normalizeRevision(proposed)
	if ctx.HasActiveMandatoryObligation {
		return reject("plan_revision_mandatory_obligation", "phase_plan_revision cannot replace or bypass an active mandatory obligation")
	}
	if proposed.BaseRevision != ctx.CurrentRevision || proposed.BaseRevision <= 0 {
		return reject("plan_revision_stale_base", "base_revision must equal the active plan revision")
	}
	if ctx.MaxPlanRevisions <= 0 || ctx.CurrentRevision-1 >= ctx.MaxPlanRevisions {
		return reject("plan_revision_budget_exhausted", "the request plan revision budget is exhausted")
	}
	if strings.TrimSpace(proposed.Reason) == "" {
		return reject("plan_revision_missing_reason", "reason is required")
	}
	if len(proposed.EvidenceRefs) == 0 {
		return reject("plan_revision_missing_evidence", "at least one existing evidence reference is required")
	}
	for _, ref := range proposed.EvidenceRefs {
		if _, ok := ctx.EvidenceRecordIDs[strings.TrimSpace(ref)]; !ok {
			return reject("plan_revision_unknown_evidence", fmt.Sprintf("evidence record %q does not exist", ref))
		}
	}
	if refs, duplicate := stringSet(proposed.EvidenceRefs); duplicate || len(refs) != len(proposed.EvidenceRefs) {
		return reject("plan_revision_duplicate_evidence", "evidence_refs must contain unique existing evidence record IDs")
	}
	if len(proposed.RemainingPhases) == 0 {
		return reject("plan_revision_empty_graph", "remaining_phases must contain the revised active and remaining graph")
	}

	oldPhases := make(map[string]contract.PhaseContract, len(ctx.Phases))
	oldSteps := map[string]contract.StepContract{}
	oldStepPhase := map[string]string{}
	oldPhaseLineages := map[string]struct{}{}
	oldStepLineages := map[string]struct{}{}
	oldCriterionIDs := map[string]struct{}{}
	maxPhaseIndex := 0
	for _, criterion := range ctx.Goal.CompletionCriteria {
		oldCriterionIDs[criterion.ID] = struct{}{}
	}
	for _, phase := range ctx.Phases {
		oldPhases[phase.ID] = phase
		oldPhaseLineages[phase.PhaseLineageID] = struct{}{}
		if phase.Index > maxPhaseIndex {
			maxPhaseIndex = phase.Index
		}
		for _, criterion := range phase.CompletionCriteria {
			oldCriterionIDs[criterion.ID] = struct{}{}
		}
		for _, step := range phase.Steps {
			oldSteps[step.ID] = step
			oldStepPhase[step.ID] = phase.ID
			oldStepLineages[step.GoalLineageID] = struct{}{}
			for _, criterion := range step.CompletionCriteria {
				oldCriterionIDs[criterion.ID] = struct{}{}
			}
		}
	}

	supersededPhases, duplicate := stringSet(proposed.SupersededPhaseIDs)
	if duplicate || len(supersededPhases) == 0 {
		return reject("plan_revision_invalid_superseded_phases", "superseded_phase_ids must contain unique existing nonterminal phase IDs")
	}
	if _, ok := supersededPhases[ctx.ActivePhaseID]; !ok {
		return reject("plan_revision_active_phase_not_superseded", "the active phase must be explicitly superseded")
	}
	for id := range supersededPhases {
		phase, ok := oldPhases[id]
		if !ok {
			return reject("plan_revision_unknown_phase", fmt.Sprintf("superseded phase %q does not exist", id))
		}
		switch ctx.PhaseStatus[phase.ID] {
		case contract.PhaseCompleted, contract.PhaseSkipped, contract.PhaseSuperseded:
			return reject("plan_revision_terminal_history", fmt.Sprintf("terminal phase %q cannot be superseded again", id))
		}
	}
	for _, phase := range ctx.Phases {
		switch ctx.PhaseStatus[phase.ID] {
		case contract.PhaseCompleted, contract.PhaseSkipped, contract.PhaseSuperseded:
			continue
		}
		if _, ok := supersededPhases[phase.ID]; !ok {
			return reject("plan_revision_incomplete_graph_replacement", fmt.Sprintf("nonterminal phase %q must be superseded when replacing the active and remaining graph", phase.ID))
		}
	}

	supersededSteps, duplicate := stringSet(proposed.SupersededStepIDs)
	if duplicate {
		return reject("plan_revision_invalid_superseded_steps", "superseded_step_ids must be unique")
	}
	for id := range supersededSteps {
		if _, ok := oldSteps[id]; !ok {
			return reject("plan_revision_unknown_step", fmt.Sprintf("superseded step %q does not exist", id))
		}
		if _, ok := supersededPhases[oldStepPhase[id]]; !ok {
			return reject("plan_revision_step_outside_phase", fmt.Sprintf("superseded step %q is not inside a superseded phase", id))
		}
		switch ctx.StepStatus[id] {
		case contract.StepAchieved, contract.StepCompleted, contract.StepSkipped, contract.StepSuperseded:
			return reject("plan_revision_terminal_history", fmt.Sprintf("terminal step %q cannot be superseded again", id))
		}
	}
	for phaseID := range supersededPhases {
		for _, step := range oldPhases[phaseID].Steps {
			switch ctx.StepStatus[step.ID] {
			case contract.StepAchieved, contract.StepCompleted, contract.StepSkipped:
				continue
			}
			if _, ok := supersededSteps[step.ID]; !ok {
				return reject("plan_revision_missing_descendant_step", fmt.Sprintf("nonterminal step %q must be superseded with its phase", step.ID))
			}
		}
	}

	mappingByOld := map[string]contract.StepLineageMapping{}
	mappingByNew := map[string]contract.StepLineageMapping{}
	for _, mapping := range proposed.StepLineageMappings {
		if mapping.PreviousStepID == "" || mapping.ReplacementStepID == "" || mapping.GoalLineageID == "" {
			return reject("plan_revision_invalid_lineage_mapping", "every step lineage mapping requires previous_step_id, replacement_step_id, and goal_lineage_id")
		}
		if _, exists := mappingByOld[mapping.PreviousStepID]; exists {
			return reject("plan_revision_duplicate_lineage_mapping", fmt.Sprintf("step %q has more than one replacement", mapping.PreviousStepID))
		}
		if _, exists := mappingByNew[mapping.ReplacementStepID]; exists {
			return reject("plan_revision_duplicate_lineage_mapping", fmt.Sprintf("replacement step %q is mapped more than once", mapping.ReplacementStepID))
		}
		old, ok := oldSteps[mapping.PreviousStepID]
		if !ok {
			return reject("plan_revision_unknown_step", fmt.Sprintf("mapped step %q does not exist", mapping.PreviousStepID))
		}
		if _, ok := supersededSteps[mapping.PreviousStepID]; !ok {
			return reject("plan_revision_unlisted_lineage_mapping", fmt.Sprintf("mapped step %q is not superseded", mapping.PreviousStepID))
		}
		if mapping.GoalLineageID != old.GoalLineageID {
			return reject("plan_revision_lineage_mismatch", fmt.Sprintf("mapping for %q does not preserve goal lineage", mapping.PreviousStepID))
		}
		mappingByOld[mapping.PreviousStepID] = mapping
		mappingByNew[mapping.ReplacementStepID] = mapping
	}
	normalized := proposed
	normalized.RemainingPhases = make([]contract.PhaseContract, len(proposed.RemainingPhases))
	newPhaseIDs := map[string]struct{}{}
	newPhaseNames := map[string]struct{}{}
	newPhaseLineages := map[string]struct{}{}
	newStepIDs := map[string]struct{}{}
	newStepLineages := map[string]struct{}{}
	newCriterionIDs := map[string]struct{}{}
	replacementByOldPhase := map[string]int{}
	activePhaseFound := false
	activeStepPhase := ""
	for phaseIndex, sourcePhase := range proposed.RemainingPhases {
		phase := clonePhaseContract(sourcePhase)
		phase.Name = strings.TrimSpace(phase.Name)
		phase.Goal = strings.TrimSpace(phase.Goal)
		if phase.ID == "" || phase.PhaseLineageID == "" || phase.Name == "" || phase.Goal == "" {
			return reject("plan_revision_invalid_phase", "every remaining phase requires id, phase_lineage_id, name, and goal")
		}
		if _, exists := oldPhases[phase.ID]; exists {
			return reject("plan_revision_reused_phase_id", fmt.Sprintf("remaining phase ID %q already exists", phase.ID))
		}
		if _, duplicate := newPhaseIDs[phase.ID]; duplicate {
			return reject("plan_revision_duplicate_phase_id", fmt.Sprintf("remaining phase ID %q is duplicated", phase.ID))
		}
		newPhaseIDs[phase.ID] = struct{}{}
		normalizedName := strings.ToLower(phase.Name)
		if _, duplicate := newPhaseNames[normalizedName]; duplicate {
			return reject("plan_revision_duplicate_phase_name", fmt.Sprintf("remaining phase name %q is duplicated", phase.Name))
		}
		newPhaseNames[normalizedName] = struct{}{}
		if _, duplicate := newPhaseLineages[phase.PhaseLineageID]; duplicate {
			return reject("plan_revision_duplicate_phase_lineage", fmt.Sprintf("remaining phase lineage %q is duplicated", phase.PhaseLineageID))
		}
		newPhaseLineages[phase.PhaseLineageID] = struct{}{}
		if phase.ReplacesPhaseID != "" {
			old, ok := oldPhases[phase.ReplacesPhaseID]
			if !ok {
				return reject("plan_revision_unknown_replaced_phase", fmt.Sprintf("replacement phase %q references unknown phase %q", phase.ID, phase.ReplacesPhaseID))
			}
			if _, ok := supersededPhases[old.ID]; !ok {
				return reject("plan_revision_unlisted_phase_replacement", fmt.Sprintf("replacement phase %q targets a phase that is not superseded", phase.ID))
			}
			if phase.PhaseLineageID != old.PhaseLineageID {
				return reject("plan_revision_phase_lineage_mismatch", fmt.Sprintf("replacement phase %q must preserve lineage %q", phase.ID, old.PhaseLineageID))
			}
			replacementByOldPhase[old.ID]++
		} else if _, reused := oldPhaseLineages[phase.PhaseLineageID]; reused {
			return reject("plan_revision_reused_new_phase_lineage", fmt.Sprintf("new phase %q reuses an existing lineage without replaces_phase_id", phase.ID))
		}
		if len(phase.CompletionCriteria) == 0 || len(phase.Steps) == 0 {
			return reject("plan_revision_incomplete_phase", fmt.Sprintf("remaining phase %q requires completion criteria and steps", phase.ID))
		}
		phase.Index = maxPhaseIndex + phaseIndex + 1
		for _, criterion := range phase.CompletionCriteria {
			if err := validateNewCriterion(criterion, oldCriterionIDs, newCriterionIDs); err != nil {
				return reject("plan_revision_invalid_criterion", err.Error())
			}
			newCriterionIDs[criterion.ID] = struct{}{}
		}
		for stepIndex := range phase.Steps {
			step := &phase.Steps[stepIndex]
			step.Goal = strings.TrimSpace(step.Goal)
			if step.ID == "" || step.GoalLineageID == "" || step.Goal == "" || !revisionStepKindAllowed(step.Kind) {
				return reject("plan_revision_invalid_step", fmt.Sprintf("remaining step %q has an invalid identity, lineage, goal, or kind", step.ID))
			}
			if _, exists := oldSteps[step.ID]; exists {
				return reject("plan_revision_reused_step_id", fmt.Sprintf("remaining step ID %q already exists", step.ID))
			}
			if _, duplicate := newStepIDs[step.ID]; duplicate {
				return reject("plan_revision_duplicate_step_id", fmt.Sprintf("remaining step ID %q is duplicated", step.ID))
			}
			newStepIDs[step.ID] = struct{}{}
			if _, duplicate := newStepLineages[step.GoalLineageID]; duplicate {
				return reject("plan_revision_duplicate_step_lineage", fmt.Sprintf("remaining step lineage %q is duplicated", step.GoalLineageID))
			}
			newStepLineages[step.GoalLineageID] = struct{}{}
			step.Index = stepIndex + 1
			if len(step.CompletionCriteria) == 0 {
				return reject("plan_revision_incomplete_step", fmt.Sprintf("remaining step %q requires completion criteria", step.ID))
			}
			for _, criterion := range step.CompletionCriteria {
				if err := validateNewCriterion(criterion, oldCriterionIDs, newCriterionIDs); err != nil {
					return reject("plan_revision_invalid_criterion", err.Error())
				}
				newCriterionIDs[criterion.ID] = struct{}{}
			}
			if mapping, mapped := mappingByNew[step.ID]; mapped {
				old := oldSteps[mapping.PreviousStepID]
				if phase.ReplacesPhaseID != oldStepPhase[mapping.PreviousStepID] {
					return reject("plan_revision_cross_phase_step_mapping", fmt.Sprintf("replacement step %q must remain in the replacement of phase %q", step.ID, oldStepPhase[mapping.PreviousStepID]))
				}
				if step.GoalLineageID != old.GoalLineageID {
					return reject("plan_revision_step_lineage_mismatch", fmt.Sprintf("replacement step %q must preserve lineage %q", step.ID, old.GoalLineageID))
				}
				step.MaxAttempts = old.MaxAttempts
			} else {
				if _, reused := oldStepLineages[step.GoalLineageID]; reused {
					return reject("plan_revision_reused_new_step_lineage", fmt.Sprintf("new step %q reuses an existing lineage without replacement mapping", step.ID))
				}
				step.MaxAttempts = ctx.AppliedBudget.StepAttempts[step.Kind]
				if step.MaxAttempts <= 0 {
					step.MaxAttempts = ctx.AppliedBudget.StepAttempts[contract.StepGeneralAction]
				}
			}
			if len(step.RequiredEvidence) == 0 {
				step.RequiredEvidence = []contract.EvidenceKind{contract.EvidenceObservation}
			}
			for _, kind := range step.RequiredEvidence {
				if !evidenceKindAllowed(kind) {
					return reject("plan_revision_invalid_step_evidence", fmt.Sprintf("remaining step %q has invalid required evidence kind %q", step.ID, kind))
				}
			}
			if step.ID == proposed.ActiveStepID {
				activeStepPhase = phase.ID
			}
		}
		if phase.ID == proposed.ActivePhaseID {
			activePhaseFound = true
		}
		normalized.RemainingPhases[phaseIndex] = phase
	}
	for oldID := range supersededPhases {
		if replacementByOldPhase[oldID] != 1 {
			return reject("plan_revision_missing_phase_replacement", fmt.Sprintf("superseded phase %q requires exactly one direct replacement", oldID))
		}
	}
	for replacementID := range mappingByNew {
		if _, ok := newStepIDs[replacementID]; !ok {
			return reject("plan_revision_unknown_replacement_step", fmt.Sprintf("lineage mapping references unknown replacement step %q", replacementID))
		}
	}
	phaseOrder := make(map[string]int, len(normalized.RemainingPhases))
	for index, phase := range normalized.RemainingPhases {
		phaseOrder[strings.ToLower(strings.TrimSpace(phase.Name))] = index
	}
	for index, phase := range normalized.RemainingPhases {
		if index < len(normalized.RemainingPhases)-1 && len(phase.AllowedNext) == 0 {
			return reject("plan_revision_missing_allowed_next", fmt.Sprintf("nonterminal phase %q requires at least one forward allowed_next", phase.ID))
		}
		for _, next := range phase.AllowedNext {
			nextOrder, ok := phaseOrder[strings.ToLower(strings.TrimSpace(next))]
			if !ok {
				return reject("plan_revision_invalid_allowed_next", fmt.Sprintf("phase %q references undeclared next phase %q", phase.ID, next))
			}
			if nextOrder <= index {
				return reject("plan_revision_non_forward_edge", fmt.Sprintf("phase %q allowed_next %q is not forward-only", phase.ID, next))
			}
		}
	}
	if !activePhaseFound || activeStepPhase == "" || activeStepPhase != proposed.ActivePhaseID {
		return reject("plan_revision_invalid_active_refs", "active_phase_id and active_step_id must identify one step in remaining_phases")
	}
	if normalized.RemainingPhases[0].ID != proposed.ActivePhaseID ||
		normalized.RemainingPhases[0].ReplacesPhaseID != ctx.ActivePhaseID {
		return reject("plan_revision_invalid_active_replacement", "the first remaining phase must be active and directly replace the previously active phase")
	}
	return RevisionResult{Revision: normalized}
}

func stringSet(values []string) (map[string]struct{}, bool) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return result, true
		}
		if _, exists := result[value]; exists {
			return result, true
		}
		result[value] = struct{}{}
	}
	return result, false
}

func validateNewCriterion(criterion contract.CriterionContract, old, current map[string]struct{}) error {
	if strings.TrimSpace(criterion.ID) == "" || strings.TrimSpace(criterion.Description) == "" {
		return fmt.Errorf("criterion ID and description are required")
	}
	if !evidenceKindAllowed(criterion.RequiredEvidenceKind) {
		return fmt.Errorf("criterion %q has invalid required evidence kind %q", criterion.ID, criterion.RequiredEvidenceKind)
	}
	if _, exists := old[criterion.ID]; exists {
		return fmt.Errorf("criterion ID %q already exists in plan history", criterion.ID)
	}
	if _, duplicate := current[criterion.ID]; duplicate {
		return fmt.Errorf("criterion ID %q is duplicated", criterion.ID)
	}
	return nil
}

func evidenceKindAllowed(kind contract.EvidenceKind) bool {
	switch kind {
	case contract.EvidenceObservation, contract.EvidenceMutation, contract.EvidenceUserInput, contract.EvidenceExternal:
		return true
	default:
		return false
	}
}

func revisionStepKindAllowed(kind contract.StepKind) bool {
	switch kind {
	case contract.StepGeneralAction, contract.StepLightweightLookup, contract.StepExplicitPhase:
		return true
	default:
		return false
	}
}

func clonePhaseContract(source contract.PhaseContract) contract.PhaseContract {
	clone := source
	clone.ID = strings.TrimSpace(source.ID)
	clone.PhaseLineageID = strings.TrimSpace(source.PhaseLineageID)
	clone.ReplacesPhaseID = strings.TrimSpace(source.ReplacesPhaseID)
	clone.Name = strings.TrimSpace(source.Name)
	clone.Goal = strings.TrimSpace(source.Goal)
	clone.AllowedNext = trimStrings(source.AllowedNext)
	clone.CompletionCriteria = append([]contract.CriterionContract(nil), source.CompletionCriteria...)
	for i := range clone.CompletionCriteria {
		clone.CompletionCriteria[i].ID = strings.TrimSpace(clone.CompletionCriteria[i].ID)
		clone.CompletionCriteria[i].Description = strings.TrimSpace(clone.CompletionCriteria[i].Description)
	}
	clone.Steps = make([]contract.StepContract, len(source.Steps))
	for i, step := range source.Steps {
		step.ID = strings.TrimSpace(step.ID)
		step.GoalLineageID = strings.TrimSpace(step.GoalLineageID)
		step.Goal = strings.TrimSpace(step.Goal)
		step.CompletionCriteria = append([]contract.CriterionContract(nil), step.CompletionCriteria...)
		for criterionIndex := range step.CompletionCriteria {
			step.CompletionCriteria[criterionIndex].ID = strings.TrimSpace(step.CompletionCriteria[criterionIndex].ID)
			step.CompletionCriteria[criterionIndex].Description = strings.TrimSpace(step.CompletionCriteria[criterionIndex].Description)
		}
		step.RequiredEvidence = append([]contract.EvidenceKind(nil), step.RequiredEvidence...)
		clone.Steps[i] = step
	}
	return clone
}

func normalizeRevision(source contract.PhasePlanRevision) contract.PhasePlanRevision {
	normalized := source
	normalized.Reason = strings.TrimSpace(source.Reason)
	normalized.EvidenceRefs = trimStrings(source.EvidenceRefs)
	normalized.SupersededPhaseIDs = trimStrings(source.SupersededPhaseIDs)
	normalized.SupersededStepIDs = trimStrings(source.SupersededStepIDs)
	normalized.ActivePhaseID = strings.TrimSpace(source.ActivePhaseID)
	normalized.ActiveStepID = strings.TrimSpace(source.ActiveStepID)
	normalized.RemainingPhases = make([]contract.PhaseContract, len(source.RemainingPhases))
	for i := range source.RemainingPhases {
		normalized.RemainingPhases[i] = clonePhaseContract(source.RemainingPhases[i])
	}
	normalized.StepLineageMappings = append([]contract.StepLineageMapping(nil), source.StepLineageMappings...)
	for i := range normalized.StepLineageMappings {
		mapping := &normalized.StepLineageMappings[i]
		mapping.PreviousStepID = strings.TrimSpace(mapping.PreviousStepID)
		mapping.ReplacementStepID = strings.TrimSpace(mapping.ReplacementStepID)
		mapping.GoalLineageID = strings.TrimSpace(mapping.GoalLineageID)
	}
	return normalized
}

func trimStrings(source []string) []string {
	trimmed := make([]string, len(source))
	for i := range source {
		trimmed[i] = strings.TrimSpace(source[i])
	}
	return trimmed
}
