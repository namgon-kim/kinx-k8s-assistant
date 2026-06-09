package react

import "testing"

func TestPhasePlanValidRejectsBackEdge(t *testing.T) {
	plan := phasePlan{
		RequestGoal:       "diagnose",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "observe", Goal: "observe", CompletionCondition: "observed", AllowedNext: []string{"diagnose"}},
			{Index: 2, Name: "diagnose", Goal: "diagnose", CompletionCondition: "diagnosed", AllowedNext: []string{"observe"}},
		},
	}
	if phasePlanValid(plan) {
		t.Fatal("phase plan with backward allowed_next should be invalid")
	}
}

func TestPhasePlanValidAllowsForwardEdges(t *testing.T) {
	plan := phasePlan{
		RequestGoal:       "diagnose",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "observe", Goal: "observe", CompletionCondition: "observed", AllowedNext: []string{"diagnose"}},
			{Index: 2, Name: "diagnose", Goal: "diagnose", CompletionCondition: "diagnosed"},
		},
	}
	if !phasePlanValid(plan) {
		t.Fatal("forward-only phase plan should be valid")
	}
}
