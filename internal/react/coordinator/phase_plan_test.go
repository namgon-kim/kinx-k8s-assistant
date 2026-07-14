package coordinator

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

func TestConsumePhaseProgressRejectsMixedOutputBeforePhaseAdvance(t *testing.T) {
	loop := &Loop{
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "guided_diagnosis", AllowedNext: []string{"response_synthesis"}},
				{Index: 2, Name: "response_synthesis"},
			},
			Completed: map[int]bool{},
		},
	}
	loop.transitionControl(RuntimeControlAwaitingGuidedPhaseProgress)

	_, handled := loop.consumePhaseProgress([]gollm.FunctionCall{
		{Name: internalPhaseProgressCall, Arguments: map[string]any{"phase_completed": 1, "next_phase": "response_synthesis"}},
		{Name: "kubectl", Arguments: map[string]any{"command": "kubectl get pods"}},
	})
	if !handled {
		t.Fatal("mixed phase_progress output should be rejected")
	}
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase = %d, want unchanged phase 1", got)
	}
	if loop.control != RuntimeControlAwaitingGuidedPhaseProgress {
		t.Fatalf("control = %s, want guided phase progress", loop.control)
	}
}

func TestConsumePhaseProgressRejectsIncompleteGuidedDiagnosis(t *testing.T) {
	loop := &Loop{
		phaseStepState: &phaseStepState{
			CurrentPhaseIndex: 1,
			PhaseSteps: []phaseStep{
				{Index: 1, Name: "guided_diagnosis", AllowedNext: []string{"response_synthesis"}},
				{Index: 2, Name: "response_synthesis"},
			},
			Completed: map[int]bool{},
		},
		guideStepState: &guideStepState{TotalSteps: 2, Completed: map[int]bool{1: true}},
	}
	loop.transitionControl(RuntimeControlAwaitingGuidedDiagnosisStep)

	_, handled := loop.consumePhaseProgress([]gollm.FunctionCall{{
		Name: internalPhaseProgressCall,
		Arguments: map[string]any{
			"phase_completed": 1,
			"next_phase":      "response_synthesis",
		},
	}})
	if !handled {
		t.Fatal("incomplete guided_diagnosis must be rejected")
	}
	if got := loop.phaseStepState.CurrentPhaseIndex; got != 1 {
		t.Fatalf("current phase = %d, want unchanged guided_diagnosis", got)
	}
}

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

func TestPhasePlanValidAllowsOptionalExplicitSteps(t *testing.T) {
	plan := phasePlan{
		RequestGoal:       "diagnose",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{
				Index:               1,
				Name:                "observation_execution",
				Goal:                "observe",
				CompletionCondition: "observed",
				Steps: []phaseExecutionStep{
					{
						ID:              "list_pods",
						Kind:            "observation",
						Description:     "List pods in the requested namespace",
						Command:         "kubectl get pods -n app",
						ExpectedOutcome: "pod status is visible",
					},
				},
			},
		},
	}
	if !phasePlanValid(plan) {
		t.Fatal("phase plan with optional explicit steps should be valid")
	}
}

func TestPhasePlanValidRejectsMalformedOptionalExplicitSteps(t *testing.T) {
	plan := phasePlan{
		RequestGoal:       "diagnose",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{
				Index:               1,
				Name:                "observation_execution",
				Goal:                "observe",
				CompletionCondition: "observed",
				Steps: []phaseExecutionStep{
					{Description: "missing stable id or index"},
				},
			},
		},
	}
	if phasePlanValid(plan) {
		t.Fatal("phase plan with malformed optional explicit steps should be invalid")
	}
}

func TestPhasePlanValidRejectsDuplicateOptionalExplicitStepRefs(t *testing.T) {
	plan := phasePlan{
		RequestGoal:       "diagnose",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{
			{
				Index:               1,
				Name:                "observation_execution",
				Goal:                "observe",
				CompletionCondition: "observed",
				Steps: []phaseExecutionStep{
					{ID: "same", Description: "first observation"},
					{ID: "same", Description: "second observation"},
				},
			},
		},
	}
	if phasePlanValid(plan) {
		t.Fatal("phase plan with duplicate explicit step ids should be invalid")
	}

	plan.PhaseSteps[0].Steps = []phaseExecutionStep{
		{Index: 1, Description: "first observation"},
		{Index: 1, Description: "second observation"},
	}
	if phasePlanValid(plan) {
		t.Fatal("phase plan with duplicate explicit step indices should be invalid")
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
