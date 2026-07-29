package coordinator

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	verificationflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/verification"
)

func TestNormalizeVerificationSpecSeparatesAwaitStateFromChain(t *testing.T) {
	spec := contract.VerificationSpec{
		Shape:                  contract.VerificationSingle,
		Mode:                   contract.VerificationAwaitState,
		ExpectedState:          "deployment is available",
		RecheckIntervalSeconds: 5,
	}
	if err := normalizeVerificationSpec(&spec); err != nil {
		t.Fatalf("normalize single await-state verification: %v", err)
	}
	if len(spec.Checks) != 0 {
		t.Fatalf("temporal recheck became a chain: %#v", spec.Checks)
	}
}

func TestVerificationChainActivatesOneDistinctCheckAtATime(t *testing.T) {
	spec := contract.VerificationSpec{
		Shape:  contract.VerificationChain,
		Policy: contract.VerificationOrdered,
		Checks: []contract.VerificationCheckSpec{
			{
				Mode:          contract.VerificationImmediate,
				ExpectedState: "node is uncordoned",
				Target:        &contract.ActionTarget{Resource: "node", Name: "node-1"},
			},
			{
				Mode:          contract.VerificationAwaitState,
				ExpectedState: "node is ready",
				Target:        &contract.ActionTarget{Resource: "node", Name: "node-1"},
			},
		},
	}
	if err := normalizeVerificationSpec(&spec); err != nil {
		t.Fatalf("normalize ordered chain: %v", err)
	}
	checks := verificationChecksFromSpec(&spec, "verification-1", actionTarget{})
	checks[1].Status = contract.VerificationCheckPending
	verification := pendingMutationVerification{Shape: contract.VerificationChain, Checks: checks}
	if got := verification.activeCheck(); got == nil || got.ID != "verification-1.check-1" {
		t.Fatalf("active check = %#v", got)
	}
	verification.Checks[0].Status = contract.VerificationCheckSatisfied
	if !verification.advanceCheck() {
		t.Fatal("expected second ordered check to activate")
	}
	if verification.ActiveIndex != 1 || verification.Checks[1].Status != contract.VerificationCheckActive {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestVerificationResultMustReferenceLatestEvidence(t *testing.T) {
	check := verificationRuntimeCheck{
		ID:           "verification-1",
		EvidenceRefs: []string{"observation-1", "observation-2"},
	}
	stale := mutationVerificationResult{
		VerificationID: "verification-1",
		EvidenceRefs:   []string{"observation-1"},
	}
	if verificationResultReferencesActiveEvidence(stale, check) {
		t.Fatal("stale evidence satisfied a temporal recheck")
	}
	current := stale
	current.EvidenceRefs = []string{"observation-1", "observation-2"}
	if !verificationResultReferencesActiveEvidence(current, check) {
		t.Fatal("latest verification evidence was rejected")
	}
}

func TestNormalizeVerificationSpecRejectsDuplicateChainChecks(t *testing.T) {
	spec := contract.VerificationSpec{
		Shape:  contract.VerificationChain,
		Policy: contract.VerificationOrdered,
		Checks: []contract.VerificationCheckSpec{
			{
				ExpectedState: "node is schedulable",
				Target:        &contract.ActionTarget{Resource: "node", Name: "node-1"},
			},
			{
				ExpectedState: "node is schedulable",
				Target:        &contract.ActionTarget{Resource: "nodes", Name: "node-1"},
			},
		},
	}
	if err := normalizeVerificationSpec(&spec); err == nil {
		t.Fatal("duplicate ordered verification checks were accepted")
	}
}

func TestValidateMutationVerificationSpecRejectsGenericSingleTarget(t *testing.T) {
	call := PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl apply -f deployment.yaml",
			},
		},
		Verification: &contract.VerificationSpec{
			Shape:         contract.VerificationSingle,
			Mode:          contract.VerificationImmediate,
			ExpectedState: "manifest resources exist",
		},
	}
	if err := validateMutationVerificationSpec(call); err == nil {
		t.Fatal("single verification without a concrete mutation target was accepted")
	}
}

func TestActionTargetFromFunctionCallPrefersRuntimeTarget(t *testing.T) {
	target, ok := actionTargetFromFunctionCall(gollm.FunctionCall{
		Name: "custom",
		Arguments: map[string]any{
			"target": "tool-owned-destination",
			"runtime_target": map[string]any{
				"resource":  "deployment",
				"namespace": "web",
				"name":      "app",
			},
		},
	})
	if !ok || target.Resource != "deployment" || target.Namespace != "web" || target.Name != "app" {
		t.Fatalf("runtime target = %#v, ok=%t", target, ok)
	}
}

func TestVerificationChecksNormalizeDeclaredTargets(t *testing.T) {
	spec := contract.VerificationSpec{
		Shape:  contract.VerificationChain,
		Policy: contract.VerificationOrdered,
		Checks: []contract.VerificationCheckSpec{
			{
				ExpectedState: "pod exists",
				Target:        &contract.ActionTarget{Resource: " Pods ", Namespace: " web ", Name: " pod-1 "},
			},
			{
				ExpectedState: "pod is ready",
				Target:        &contract.ActionTarget{Resource: "POD", Namespace: "web", Name: "pod-1"},
			},
		},
	}
	if err := normalizeVerificationSpec(&spec); err != nil {
		t.Fatalf("normalize verification spec: %v", err)
	}
	checks := verificationChecksFromSpec(&spec, "verification-1", actionTarget{})
	if got := checks[0].Target; got.Resource != "pod" || got.Namespace != "web" || got.Name != "pod-1" {
		t.Fatalf("normalized target = %#v", got)
	}
}

func TestValidateMutationVerificationSpecRejectsChainWithoutConcreteActionTarget(t *testing.T) {
	call := PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl apply -f resources.yaml",
			},
		},
		Verification: &contract.VerificationSpec{
			Shape:  contract.VerificationChain,
			Policy: contract.VerificationOrdered,
			Checks: []contract.VerificationCheckSpec{
				{
					ExpectedState: "first resource exists",
					Target:        &contract.ActionTarget{Resource: "configmap", Name: "app-config"},
				},
				{
					ExpectedState: "second resource exists",
					Target:        &contract.ActionTarget{Resource: "deployment", Name: "web"},
				},
			},
		},
	}
	if err := validateMutationVerificationSpec(call); err == nil {
		t.Fatal("ordered verification without a concrete action target was accepted")
	}
}

func TestVerificationChainChecksInheritConcreteActionTarget(t *testing.T) {
	call := PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl create configmap app-config -n web --from-literal=key=value",
				"target": map[string]any{
					"resource":  "configmap",
					"namespace": "web",
					"name":      "app-config",
				},
			},
		},
		Verification: &contract.VerificationSpec{
			Shape:  contract.VerificationChain,
			Policy: contract.VerificationOrdered,
			Checks: []contract.VerificationCheckSpec{
				{ExpectedState: "configmap exists"},
				{ExpectedState: "configmap contains the requested key"},
			},
		},
	}
	if err := validateMutationVerificationSpec(call); err != nil {
		t.Fatalf("validate chain with inherited action target: %v", err)
	}
	checks := verificationChecksFromSpec(call.Verification, "verification-1", actionTarget{
		Resource:  "configmap",
		Namespace: "web",
		Name:      "app-config",
	})
	for i, check := range checks {
		if check.Target.Resource != "configmap" || check.Target.Namespace != "web" || check.Target.Name != "app-config" {
			t.Fatalf("check %d target = %#v, want inherited action target", i+1, check.Target)
		}
	}
}

func TestValidateMutationVerificationSpecRejectsPlaceholderLaterChainTarget(t *testing.T) {
	call := PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl create configmap app-config -n web --from-literal=key=value",
				"target": map[string]any{
					"resource":  "configmap",
					"namespace": "web",
					"name":      "app-config",
				},
			},
		},
		Verification: &contract.VerificationSpec{
			Shape:  contract.VerificationChain,
			Policy: contract.VerificationOrdered,
			Checks: []contract.VerificationCheckSpec{
				{
					ExpectedState: "configmap exists",
					Target:        &contract.ActionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
				},
				{
					ExpectedState: "configmap contains the requested key",
					Target:        &contract.ActionTarget{Resource: "unknown", Namespace: "web", Name: "unknown"},
				},
			},
		},
	}
	if err := validateMutationVerificationSpec(call); err == nil {
		t.Fatal("placeholder target in a later ordered verification check was accepted")
	}
}

func TestMutationVerificationAcceptsKubernetesAbsenceAsEvidence(t *testing.T) {
	result := map[string]any{
		"status": "failed",
		"error":  "Error from server (NotFound): pods \"web\" not found",
	}
	if !verificationflow.CanRequestResult(verificationEvidenceQualification(result)) {
		t.Fatal("Kubernetes NotFound did not reach verification result interpretation")
	}
	result["execution_state"] = "uncertain"
	if verificationflow.CanRequestResult(verificationEvidenceQualification(result)) {
		t.Fatal("uncertain execution was accepted as verification evidence")
	}
}

func TestVerificationNotFoundDoesNotEnterToolFailureCorrection(t *testing.T) {
	call := PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get pod web -n prod",
			},
		},
		ModifiesResource: "no",
	}
	loop := &Loop{runtimeState: &runtimeState{
		control: RuntimeControlAwaitingMutationVerificationEvidence,
		pendingMutationVerification: &pendingMutationVerification{
			Shape: contract.VerificationSingle,
			Checks: []verificationRuntimeCheck{{
				ID:     "verification-1",
				Target: actionTarget{Resource: "pod", Namespace: "prod", Name: "web"},
				Status: contract.VerificationCheckActive,
			}},
		},
	}}
	result := map[string]any{
		"status": "failed",
		"error":  "Error from server (NotFound): pods \"web\" not found",
	}
	if outcome, failed := loop.annotateToolFailureResult(call, result); failed {
		t.Fatalf("verification absence entered tool failure correction: %#v", outcome)
	}
}
