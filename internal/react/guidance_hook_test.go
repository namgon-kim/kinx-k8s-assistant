package react

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

func TestCustomResourceCandidateFromKubectl(t *testing.T) {
	tests := []struct {
		command string
		want    string
		ok      bool
	}{
		{command: "kubectl get cluster clst-a -n ns", want: "cluster", ok: true},
		{command: "kubectl describe machinedeployment md-a -n ns", want: "machinedeployment", ok: true},
		{command: "kubectl get machines -n ns", want: "machines", ok: true},
		{command: "kubectl get pods -n ns", ok: false},
		{command: "kubectl get nodes", ok: false},
		{command: "kubectl get hpa -n ns", ok: false},
		{command: "kubectl get networkpolicies -n ns", ok: false},
		{command: "kubectl get certificatesigningrequests", ok: false},
		{command: "kubectl get volumeattachments", ok: false},
		{command: "kubectl logs pod-a -n ns", ok: false},
	}
	for _, tt := range tests {
		got, ok := customResourceCandidateFromKubectl(tt.command)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("customResourceCandidateFromKubectl(%q) = (%q, %v), want (%q, %v)", tt.command, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCommandStringAcceptsOnlyKubectlCommands(t *testing.T) {
	if _, ok := commandString("kubectl get cluster c1 -n ns"); !ok {
		t.Fatal("expected kubectl command to be accepted")
	}
	if _, ok := commandString("echo hello"); ok {
		t.Fatal("expected non-kubectl command to be rejected")
	}
}

func TestResourceGuideQueryDeduplicatesExactRefinement(t *testing.T) {
	loop := &Loop{}
	query := "resource family: cluster-api\nproblem focus: worker scale"
	if loop.resourceGuideQueryAlreadyUsed(query) {
		t.Fatal("query must not be marked before use")
	}
	loop.markResourceGuideQuery(query)
	if !loop.resourceGuideQueryAlreadyUsed(query) {
		t.Fatal("query must be marked after use")
	}
}

func TestResourceGuideRefinementQueryUsesProblemFocus(t *testing.T) {
	loop := &Loop{originalQuery: "cluster status"}
	got := loop.resourceGuideRefinementQuery(resourceGuideLookup{
		ResourceFamily: "cluster-api",
		ProblemFocus:   "worker scale / replica availability",
		Reason:         "worker replicas are below desired count",
		Evidence:       "spec.replicas=3, availableReplicas=1",
	})
	for _, want := range []string{
		"resource family: cluster-api",
		"problem focus: worker scale / replica availability",
		"observed evidence: spec.replicas=3, availableReplicas=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected refinement query to contain %q, got %q", want, got)
		}
	}
}

func TestFormatResourceGuideObservationWithoutResultsStillInjectsGuardrail(t *testing.T) {
	got := formatResourceGuideObservation("cluster", nil)
	if !strings.Contains(got, "No matching resource guide was found") {
		t.Fatalf("expected empty-result guardrail, got %q", got)
	}
}

func TestFormatResourceGuideUnavailableObservationDoesNotClaimNoMatch(t *testing.T) {
	got := formatResourceGuideUnavailableObservation("cluster", "provider=local")
	if strings.Contains(got, "No matching resource guide was found") {
		t.Fatalf("unavailable lookup must not claim no match: %q", got)
	}
	if !strings.Contains(got, "lookup was not executed") {
		t.Fatalf("expected unavailable lookup explanation, got %q", got)
	}
}

func TestInconsistentActionTargetMessageRequiresNamespaceAndName(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get cluster -o yaml",
			"target": map[string]any{
				"resource":  "cluster",
				"namespace": "tenant-a",
				"name":      "cluster-a",
			},
		},
	}
	got, invalid := inconsistentActionTargetMessage(call)
	if !invalid || !strings.Contains(got, `name "cluster-a"`) {
		t.Fatalf("expected missing name to be rejected, got invalid=%v message=%q", invalid, got)
	}

	call.Arguments["command"] = "kubectl get cluster cluster-a -o yaml"
	got, invalid = inconsistentActionTargetMessage(call)
	if !invalid || !strings.Contains(got, `namespace "tenant-a"`) {
		t.Fatalf("expected missing namespace to be rejected, got invalid=%v message=%q", invalid, got)
	}

	call.Arguments["command"] = "kubectl get cluster cluster-a -n tenant-a -o yaml"
	if got, invalid = inconsistentActionTargetMessage(call); invalid {
		t.Fatalf("expected matching command to pass, got %q", got)
	}
}

func TestInconsistentActionTargetMessageRejectsNamespacedNamespaceTarget(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get namespace tenant-a -n tenant-a -o yaml",
			"target": map[string]any{
				"resource":  "namespace",
				"namespace": "tenant-a",
				"name":      "tenant-a",
			},
		},
	}
	got, invalid := inconsistentActionTargetMessage(call)
	if !invalid || !strings.Contains(got, "Namespace objects are cluster-scoped") {
		t.Fatalf("expected namespaced namespace target to be rejected, got invalid=%v message=%q", invalid, got)
	}
}

func TestRequestContextRejectsNamespaceAsPrimaryTargetWithNamespaceScope(t *testing.T) {
	_, ok := requestContextFromFunctionCall(gollm.FunctionCall{
		Name: internalRequestContextCall,
		Arguments: map[string]any{
			"primary_target": map[string]any{
				"resource": "namespace",
				"name":     "tenant-a",
			},
			"scope": map[string]any{
				"namespace": "tenant-a",
			},
			"resource_class": "built_in",
		},
	})
	if ok {
		t.Fatal("expected namespace primary target with namespace scope to be rejected")
	}
}

func TestInitialResourceGuideLookupUsesRuntimeBuiltInExclusion(t *testing.T) {
	loop := &Loop{}
	if !loop.shouldRunInitialResourceGuideLookup(requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "cluster-a"},
		Scope:         requestScope{Namespace: "tenant-a"},
		ResourceClass: "built_in",
	}) {
		t.Fatal("non-built-in resources should still trigger initial guide lookup even when model class hint is wrong")
	}
	if loop.shouldRunInitialResourceGuideLookup(requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "pod", Name: "pod-a"},
		Scope:         requestScope{Namespace: "tenant-a"},
		ResourceClass: "custom_resource",
	}) {
		t.Fatal("built-in resources must not trigger initial guide lookup even when model class hint is wrong")
	}
}
