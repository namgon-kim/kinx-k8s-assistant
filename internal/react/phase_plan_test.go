package react

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

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

func TestValidatePhasePlanForRequestRequiresMutationVerification(t *testing.T) {
	loop := &Loop{requirementAnalysis: &requirementAnalysis{
		RequestType: "mutation",
		Action:      "create_configmap",
	}}
	plan := phasePlan{
		RequestGoal:       "create configmap",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "mutation_execution", Goal: "create", CompletionCondition: "created", AllowedNext: []string{"response_synthesis"}},
			{Index: 2, Name: "response_synthesis", Goal: "report", CompletionCondition: "reported"},
		},
	}
	result := loop.validatePhasePlanForRequest(plan)
	if result.Valid || result.Code != "phase_plan_missing_mutation_verification" {
		t.Fatalf("expected missing verification rejection, got %#v", result)
	}

	plan.PhaseSteps[0].AllowedNext = []string{"mutation_verification"}
	plan.PhaseSteps[1] = phaseStep{Index: 2, Name: "mutation_verification", Goal: "verify", CompletionCondition: "verified", AllowedNext: []string{"response_synthesis"}}
	plan.PhaseSteps = append(plan.PhaseSteps, phaseStep{Index: 3, Name: "response_synthesis", Goal: "report", CompletionCondition: "reported"})
	if result := loop.validatePhasePlanForRequest(plan); !result.Valid {
		t.Fatalf("expected mutation verification phase to satisfy contract, got %#v", result)
	}
}

func TestValidatePhasePlanForReadOnlyRequestDoesNotRequireMutationVerification(t *testing.T) {
	loop := &Loop{
		cfg: &config.Config{ReadOnly: true},
		requirementAnalysis: &requirementAnalysis{
			RequestType: "mutation",
			Action:      "create_configmap",
		},
	}
	plan := phasePlan{
		RequestGoal:       "explain read-only limitation",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "response_synthesis", Goal: "explain blocked mutation", CompletionCondition: "reported"},
		},
	}
	if result := loop.validatePhasePlanForRequest(plan); !result.Valid {
		t.Fatalf("read-only mutation request should not require mutation verification phase, got %#v", result)
	}
}

func TestValidatePhasePlanForRequestRejectsGuidanceWithoutCRD(t *testing.T) {
	loop := &Loop{resourceClassification: &resourceClassification{Kind: resourceClassificationBuiltin}}
	plan := phasePlan{
		RequestGoal:       "diagnose pod",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "observation_planning", Goal: "observe", CompletionCondition: "observed", AllowedNext: []string{"guidance_lookup"}},
			{Index: 2, Name: "guidance_lookup", Goal: "lookup guide", CompletionCondition: "guide found", AllowedNext: []string{"guided_diagnosis"}},
			{Index: 3, Name: "guided_diagnosis", Goal: "follow guide", CompletionCondition: "diagnosed"},
		},
	}
	result := loop.validatePhasePlanForRequest(plan)
	if result.Valid || result.Code != "phase_plan_guidance_without_crd" {
		t.Fatalf("expected guidance rejection for built-in resource, got %#v", result)
	}

	loop.resourceClassification = &resourceClassification{Kind: resourceClassificationCRD}
	if result := loop.validatePhasePlanForRequest(plan); !result.Valid {
		t.Fatalf("expected guidance phases after CRD classification, got %#v", result)
	}
}

func TestValidatePhasePlanForRequestAllowsLightweightLookup(t *testing.T) {
	loop := &Loop{requirementAnalysis: &requirementAnalysis{
		RequestType: "lookup",
		Action:      "count_pods",
	}}
	plan := phasePlan{
		RequestGoal:       "count pods",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{Index: 1, Name: "lightweight_lookup", Goal: "aggregate", CompletionCondition: "answer produced"},
		},
	}
	if result := loop.validatePhasePlanForRequest(plan); !result.Valid {
		t.Fatalf("expected lightweight lookup to remain valid, got %#v", result)
	}
}
