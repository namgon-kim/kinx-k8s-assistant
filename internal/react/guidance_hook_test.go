package react

import (
	"strings"
	"testing"
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
