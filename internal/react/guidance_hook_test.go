package react

import (
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
)

func TestCustomResourceCandidateFromKubectl(t *testing.T) {
	tests := []struct {
		command string
		want    string
		ok      bool
	}{
		{command: "kubectl get cluster clst-a -n ns", want: "cluster", ok: true},
		{command: "kubectl -n ns get cluster clst-a", want: "cluster", ok: true},
		{command: "kubectl -n ns get cluster,openstackcluster clst-a", want: "cluster", ok: true},
		{command: "kubectl get -n ns -o yaml machinedeployment md-a", want: "machinedeployment", ok: true},
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

func TestCommandMentionsResourceAllowsCommaSeparatedTargetResource(t *testing.T) {
	command := "kubectl -n 43e3c8fe-8674-4ccf-88e9-7084805034bb get machinedeployment,tenantcontrolplane -l cluster.x-k8s.io/cluster-name=clst-pz02-shs1006-04 -o yaml"
	if !commandMentionsResource(command, "machinedeployment,tenantcontrolplane") {
		t.Fatalf("expected comma-separated target resources to match command: %s", command)
	}
}

func TestCommandMentionsResourceAllowsCRDPluralSingularMismatch(t *testing.T) {
	if !commandMentionsResource("kubectl get machines -n tenant-a", "machine") {
		t.Fatal("expected plural CRD resource in command to match singular action target")
	}
	if !commandMentionsResource("kubectl get machinedeployments -n tenant-a", "machinedeployment") {
		t.Fatal("expected plural MachineDeployment resource in command to match singular action target")
	}
}

func TestFormatResourceGuideObservationWithoutResultsStillInjectsGuardrail(t *testing.T) {
	got := formatResourceGuideObservation("cluster", nil)
	if !strings.Contains(got, "No matching resource guide was found") {
		t.Fatalf("expected empty-result guardrail, got %q", got)
	}
}

func TestFormatResourceGuideObservationAddsClusterAPIGuardrailsOnlyFromGuide(t *testing.T) {
	got := formatResourceGuideObservation("cluster", &guidance.GuideSearchResult{Cases: []guidance.GuideCase{{
		ID:    "cluster-api-guide",
		Title: "Cluster API guide",
		DiagnosticSteps: []guidance.PlanStep{{
			CommandTemplate: "kubectl get machinedeployment -l cluster.x-k8s.io/cluster-name=test -o yaml",
		}},
	}}})
	if !strings.Contains(got, "Cluster API guardrails are enabled") {
		t.Fatalf("expected Cluster API guardrails when guide context implies CAPI, got %q", got)
	}
	if !strings.Contains(got, "management-cluster `kubectl get node` result is not workload-cluster evidence") {
		t.Fatalf("expected nuanced management-cluster node guidance, got %q", got)
	}

	got = formatResourceGuideObservation("widgets", &guidance.GuideSearchResult{Cases: []guidance.GuideCase{{
		ID:    "generic-widget",
		Title: "Generic widget guide",
	}}})
	if strings.Contains(got, "Cluster API guardrails are enabled") {
		t.Fatalf("did not expect Cluster API guardrails for generic guide, got %q", got)
	}
}

func TestFormatResourceGuideObservationPreservesGuideMetadataAndCommands(t *testing.T) {
	got := formatResourceGuideObservation("cluster", &guidance.GuideSearchResult{Cases: []guidance.GuideCase{{
		ID:               "cluster-api-metadata-guide",
		Title:            "Cluster API Metadata Guide",
		EvidenceKeywords: []string{"metadata.example.com/state-ready", "metadata.example.com/state-paused"},
		DecisionHints:    []string{"The selected object is identified by metadata.example.com/primary=true."},
		RelatedObjects:   []string{"Cluster", "MachineDeployment"},
		Tags:             []string{"annotations", "nodegroup"},
		DiagnosticSteps: []guidance.PlanStep{{
			Description:     "Inspect the initial node group",
			CommandTemplate: "kubectl -n {{namespace}} get machinedeployment -l cluster.x-k8s.io/cluster-name={{name}},metadata.example.com/primary=true -o yaml",
			ExpectedOutcome: "Check guide-provided annotations and labels",
		}},
	}}})
	for _, want := range []string{
		"related objects: Cluster, MachineDeployment",
		"tags: annotations, nodegroup",
		"metadata cues to inspect:",
		"metadata.example.com/primary=true",
		"command templates exactly",
		"kubectl -n {{namespace}} get machinedeployment -l cluster.x-k8s.io/cluster-name={{name}},metadata.example.com/primary=true -o yaml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected guide observation to contain %q, got %q", want, got)
		}
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

func TestInconsistentActionTargetMessageAllowsCaseAndMultiResourceCommands(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n tenant-a get machinedeployment,machineset,machine -l cluster.x-k8s.io/cluster-name=cluster-a -o yaml",
			"target": map[string]any{
				"resource":  "MachineDeployment",
				"namespace": "tenant-a",
				"name":      "cluster-a",
			},
		},
	}
	if got, invalid := inconsistentActionTargetMessage(call); invalid {
		t.Fatalf("expected multi-resource selector command to pass, got %q", got)
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

func TestInitialResourceGuideLookupUsesRuntimeCRDDiscovery(t *testing.T) {
	loop := &Loop{executor: fakeDiscoveryExecutor{}}
	clusterClass := loop.classifyResourceByDiscovery(context.Background(), "cluster")
	if clusterClass.Kind != resourceClassificationCRD {
		t.Fatalf("expected cluster to be classified as CRD, got %#v", clusterClass)
	}
	if !loop.shouldRunInitialResourceGuideLookup(requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "cluster-a"},
		Scope:         requestScope{Namespace: "tenant-a"},
		ResourceClass: "built_in",
	}, clusterClass) {
		t.Fatal("CRDs should trigger initial guide lookup even when model class hint is wrong")
	}

	podClass := loop.classifyResourceByDiscovery(context.Background(), "pod")
	if podClass.Kind != resourceClassificationBuiltin {
		t.Fatalf("expected pod to be classified as built-in, got %#v", podClass)
	}
	if loop.shouldRunInitialResourceGuideLookup(requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "pod", Name: "pod-a"},
		Scope:         requestScope{Namespace: "tenant-a"},
		ResourceClass: "custom_resource",
	}, podClass) {
		t.Fatal("built-in resources must not trigger initial guide lookup even when model class hint is wrong")
	}
}

type fakeDiscoveryExecutor struct{}

func (fakeDiscoveryExecutor) Execute(ctx context.Context, command string, env []string, workDir string) (*sandbox.ExecResult, error) {
	switch command {
	case "kubectl get customresourcedefinitions.apiextensions.k8s.io -o json":
		return &sandbox.ExecResult{Command: command, Stdout: `{
  "items": [
    {
      "spec": {
        "group": "cluster.x-k8s.io",
        "names": {
          "plural": "clusters",
          "singular": "cluster",
          "kind": "Cluster",
          "shortNames": ["cl"]
        }
      }
    }
  ]
}`}, nil
	case "kubectl api-resources -o name":
		return &sandbox.ExecResult{Command: command, Stdout: "pods\nservices\n"}, nil
	default:
		return &sandbox.ExecResult{Command: command, ExitCode: 1, Stderr: "unexpected command"}, nil
	}
}

func (fakeDiscoveryExecutor) Close(ctx context.Context) error { return nil }
