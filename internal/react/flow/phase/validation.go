package phase

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func Validate(plan contract.PhasePlan) bool {
	if strings.TrimSpace(plan.RequestGoal) == "" || len(plan.PhaseSteps) == 0 {
		return false
	}
	foundCurrent := false
	seenIndex := make(map[int]struct{}, len(plan.PhaseSteps))
	seenName := make(map[string]struct{}, len(plan.PhaseSteps))
	indexByName := make(map[string]int, len(plan.PhaseSteps))
	for _, step := range plan.PhaseSteps {
		if step.Index == 0 || strings.TrimSpace(step.Name) == "" || !ExecutionStepsValid(step.Steps) {
			return false
		}
		if _, exists := seenIndex[step.Index]; exists {
			return false
		}
		seenIndex[step.Index] = struct{}{}
		name := normalizeName(step.Name)
		if _, exists := seenName[name]; exists {
			return false
		}
		seenName[name] = struct{}{}
		indexByName[name] = step.Index
		if step.Index == plan.CurrentPhaseIndex {
			foundCurrent = true
		}
	}
	for _, step := range plan.PhaseSteps {
		if hasLaterStep(plan.PhaseSteps, step.Index) && len(nonEmpty(step.AllowedNext)) == 0 {
			return false
		}
		for _, next := range step.AllowedNext {
			nextName := normalizeName(next)
			if nextName == "" {
				continue
			}
			if _, exists := seenName[nextName]; !exists || indexByName[nextName] <= step.Index {
				return false
			}
		}
	}
	return foundCurrent
}

func ExecutionStepValid(step contract.PhaseExecutionStep) bool {
	if strings.TrimSpace(step.ID) == "" && step.Index <= 0 {
		return false
	}
	return strings.TrimSpace(step.Kind) != "" || strings.TrimSpace(step.Description) != "" ||
		strings.TrimSpace(step.Command) != "" || strings.TrimSpace(step.ExpectedOutcome) != ""
}

func ExecutionStepsValid(steps []contract.PhaseExecutionStep) bool {
	seenID := map[string]struct{}{}
	seenIndex := map[int]struct{}{}
	for _, step := range steps {
		if !ExecutionStepValid(step) {
			return false
		}
		if id := normalizeName(step.ID); id != "" {
			if _, exists := seenID[id]; exists {
				return false
			}
			seenID[id] = struct{}{}
		}
		if step.Index > 0 {
			if _, exists := seenIndex[step.Index]; exists {
				return false
			}
			seenIndex[step.Index] = struct{}{}
		}
	}
	return true
}

func hasLaterStep(steps []contract.PhaseStep, index int) bool {
	for _, step := range steps {
		if step.Index > index {
			return true
		}
	}
	return false
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}
