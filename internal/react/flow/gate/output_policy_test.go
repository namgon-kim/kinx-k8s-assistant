package gate

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestOutputPolicyCoversEveryModelTurnState(t *testing.T) {
	policyStates := AllOutputPolicyStates()
	if len(policyStates) != 14 {
		t.Fatalf("policy states = %d, want 14", len(policyStates))
	}
	seen := map[contract.RuntimeControlState]bool{}
	for _, state := range policyStates {
		if seen[state] {
			t.Fatalf("duplicate policy state %q", state)
		}
		seen[state] = true
	}
	for _, state := range contract.AllRuntimeControlStates() {
		class, ok := contract.ClassifyRuntimeControlState(state)
		if !ok {
			t.Fatalf("unclassified state %q", state)
		}
		_, hasPolicy := OutputPolicyFor(state)
		if class == contract.ControlExecutionModelTurn && !hasPolicy {
			t.Errorf("model-turn state %q has no output policy", state)
		}
		if class != contract.ControlExecutionModelTurn && hasPolicy {
			t.Errorf("non-model state %q has an output policy", state)
		}
		if hasPolicy {
			policy, _ := OutputPolicyFor(state)
			if policy.Control != state {
				t.Errorf("policy key %q declares control %q", state, policy.Control)
			}
		}
	}
}

func TestSingleOutputMatrixIsFullyClassified(t *testing.T) {
	assertions := 0
	for _, state := range AllOutputPolicyStates() {
		for _, kind := range contract.AllModelOutputKinds() {
			decision := ValidateOutputKinds(state, []contract.ModelOutputKind{kind}, OutputPolicyContext{})
			if !decision.Allow && decision.Code == "" {
				t.Errorf("state=%s kind=%s has no rejection code", state, kind)
			}
			assertions++
		}
	}
	if assertions != 182 {
		t.Fatalf("single-output assertions = %d, want 182", assertions)
	}
}

func TestInternalEventActionPairMatrixIsFullyClassified(t *testing.T) {
	assertions := 0
	for _, state := range AllOutputPolicyStates() {
		for _, kind := range contract.AllModelOutputKinds() {
			if kind == contract.OutputAction || kind == contract.OutputPlainAnswer {
				continue
			}
			decision := ValidateOutputKinds(
				state,
				[]contract.ModelOutputKind{kind, contract.OutputAction},
				OutputPolicyContext{},
			)
			if !decision.Allow && decision.Code == "" {
				t.Errorf("state=%s kind=%s action pair has no rejection code", state, kind)
			}
			assertions++
		}
	}
	if assertions != 154 {
		t.Fatalf("internal/action assertions = %d, want 154", assertions)
	}
}

func TestRequiredExclusivePolicies(t *testing.T) {
	required := 0
	pairs := 0
	for _, state := range AllOutputPolicyStates() {
		policy, _ := OutputPolicyFor(state)
		if policy.Mix != MixRequiredExclusive {
			continue
		}
		required++
		if decision := ValidateOutputKinds(state, []contract.ModelOutputKind{policy.Required}, OutputPolicyContext{}); !decision.Allow {
			t.Errorf("state=%s required output rejected: %s", state, decision.Code)
		}
		for _, other := range contract.AllModelOutputKinds() {
			if other == policy.Required {
				continue
			}
			pairs++
			decision := ValidateOutputKinds(
				state,
				[]contract.ModelOutputKind{policy.Required, other},
				OutputPolicyContext{},
			)
			expectedCode := "required_output_exclusive"
			if state == contract.RuntimeControlAwaitingPhasePlan && other == contract.OutputAction {
				expectedCode = "lightweight_plan_action_not_eligible"
			}
			if decision.Allow || decision.Code != expectedCode {
				t.Errorf("state=%s required=%s other=%s decision=%#v", state, policy.Required, other, decision)
			}
		}
	}
	if required != 9 {
		t.Fatalf("required-exclusive states = %d, want 9", required)
	}
	if pairs != 108 {
		t.Fatalf("required-exclusive pairs = %d, want 108", pairs)
	}
}

func TestConditionalOutputBundles(t *testing.T) {
	plain := ValidateOutputKinds(
		contract.RuntimeControlAwaitingModelStep,
		[]contract.ModelOutputKind{contract.OutputPlainAnswer},
		OutputPolicyContext{AllowPlainAnswer: true},
	)
	if !plain.Allow {
		t.Fatalf("response-phase plain answer rejected: %s", plain.Code)
	}
	for _, kind := range contract.AllModelOutputKinds() {
		if kind == contract.OutputPlainAnswer {
			continue
		}
		decision := ValidateOutputKinds(
			contract.RuntimeControlAwaitingModelStep,
			[]contract.ModelOutputKind{contract.OutputPlainAnswer, kind},
			OutputPolicyContext{AllowPlainAnswer: true},
		)
		if decision.Allow {
			t.Errorf("plain answer was mixed with %s", kind)
		}
	}
	bundle := []contract.ModelOutputKind{contract.OutputPhasePlan, contract.OutputAction}
	if decision := ValidateOutputKinds(contract.RuntimeControlAwaitingPhasePlan, bundle, OutputPolicyContext{}); decision.Allow {
		t.Fatal("ineligible lightweight bundle was allowed")
	}
	if decision := ValidateOutputKinds(contract.RuntimeControlAwaitingPhasePlan, bundle, OutputPolicyContext{AllowLightweightPlanAction: true}); !decision.Allow {
		t.Fatalf("eligible lightweight bundle rejected: %s", decision.Code)
	}
}

func TestPhasePlanRevisionIsExclusiveAndLimitedToActiveDiagnosticStates(t *testing.T) {
	for _, state := range []contract.RuntimeControlState{
		contract.RuntimeControlAwaitingModelStep,
		contract.RuntimeControlAwaitingGuidedDiagnosisStep,
		contract.RuntimeControlAwaitingMutationContinuation,
	} {
		decision := ValidateOutputKinds(state, []contract.ModelOutputKind{contract.OutputPhasePlanRevision}, OutputPolicyContext{})
		if !decision.Allow {
			t.Errorf("state=%s rejected phase plan revision: %s", state, decision.Code)
		}
		mixed := ValidateOutputKinds(
			state,
			[]contract.ModelOutputKind{contract.OutputPhasePlanRevision, contract.OutputAction},
			OutputPolicyContext{},
		)
		if mixed.Allow || mixed.Code != "state_event_action_mixed" {
			t.Errorf("state=%s mixed revision/action decision=%#v", state, mixed)
		}
	}
	if decision := ValidateOutputKinds(
		contract.RuntimeControlAwaitingMutationVerificationEvidence,
		[]contract.ModelOutputKind{contract.OutputPhasePlanRevision},
		OutputPolicyContext{},
	); decision.Allow {
		t.Fatal("phase plan revision bypassed mutation evidence obligation")
	}
	if decision := ValidateOutputKinds(
		contract.RuntimeControlAwaitingMutationVerificationChainEvidence,
		[]contract.ModelOutputKind{contract.OutputPhasePlanRevision},
		OutputPolicyContext{},
	); decision.Allow {
		t.Fatal("phase plan revision bypassed mutation verification chain")
	}
}

func TestGuardedMixPolicyOwnsExternalActionCardinality(t *testing.T) {
	for _, state := range []contract.RuntimeControlState{
		contract.RuntimeControlAwaitingGuidedDiagnosisStep,
		contract.RuntimeControlAwaitingMutationVerificationEvidence,
		contract.RuntimeControlAwaitingMutationVerificationChainEvidence,
		contract.RuntimeControlAwaitingMutationContinuation,
	} {
		decision := ValidateOutputKinds(
			state,
			[]contract.ModelOutputKind{contract.OutputAction},
			OutputPolicyContext{ExternalActionCount: 2},
		)
		if decision.Allow || decision.Code != "multiple_guarded_actions" {
			t.Fatalf("state %q decision = %#v", state, decision)
		}
	}

	decision := ValidateOutputKinds(
		contract.RuntimeControlAwaitingModelStep,
		[]contract.ModelOutputKind{contract.OutputAction},
		OutputPolicyContext{ExternalActionCount: 2},
	)
	if !decision.Allow {
		t.Fatalf("ordinary model step unexpectedly rejected multiple actions: %#v", decision)
	}
}
