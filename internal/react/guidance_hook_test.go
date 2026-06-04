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
	query := "resource family: cluster-api\nproblem focus: nodegroup reconciliation"
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
		ProblemFocus:   "nodegroup reconciliation",
		Reason:         "live evidence requires a more specific guide than the initial resource-family guide",
		Evidence:       "controller reported nodegroup reconciliation is blocked",
	})
	for _, want := range []string{
		"resource family: cluster-api",
		"problem focus: nodegroup reconciliation",
		"observed evidence: controller reported nodegroup reconciliation is blocked",
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

func TestCommandMentionsResourceTreatsKubectlLogsAsPodTarget(t *testing.T) {
	if !commandMentionsResource("kubectl logs clst-pz02-shs1006-04-85d5694687-5h2tz -n tenant-a -c konnectivity-server", "pod") {
		t.Fatal("expected kubectl logs <pod> to satisfy pod action target")
	}
}

func TestRejectUnrelatedFirstDiagnosticForExplicitClusterTarget(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-pz02-shs1006-04"},
			Scope:         requestScope{Namespace: "tenant-a"},
			ResourceClass: "custom_resource",
		},
	}
	if !loop.rejectUnrelatedFirstDiagnostic([]gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"target": map[string]any{
				"resource":  "pods",
				"namespace": "tenant-a",
			},
			"command": "kubectl get pods -n tenant-a",
		},
	}}) {
		t.Fatal("expected unrelated pod listing to be rejected before first cluster diagnostic")
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
				"resource":  "Namespace",
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

func TestInconsistentActionTargetMessageRejectsUnknownActionTargetResource(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl describe unknown clst-pz02-shs1006-04 -n tenant-a",
			"target": map[string]any{
				"resource":  "unknown",
				"namespace": "tenant-a",
				"name":      "clst-pz02-shs1006-04",
			},
		},
	}
	got, invalid := inconsistentActionTargetMessage(call)
	if !invalid || !strings.Contains(got, "`unknown` is not a Kubernetes resource kind") {
		t.Fatalf("expected unknown action target to be rejected, got invalid=%v message=%q", invalid, got)
	}
}

func TestKubectlCommandUsesUnknownResourceRejectsGetDescribeOnly(t *testing.T) {
	for _, command := range []string{
		"kubectl describe unknown clst-pz02-shs1006-04 -n tenant-a",
		"kubectl get cluster,unknown clst-pz02-shs1006-04 -n tenant-a",
	} {
		if !kubectlCommandUsesUnknownResource(command) {
			t.Fatalf("expected unknown resource to be detected in %q", command)
		}
	}
	if kubectlCommandUsesUnknownResource("kubectl logs unknown -n tenant-a") {
		t.Fatal("kubectl logs unknown should be treated as a pod name, not an unknown resource kind")
	}
}

func TestRequestContextRejectsNamespaceAsPrimaryTargetWithNamespaceScope(t *testing.T) {
	_, ok := requestContextFromFunctionCall(gollm.FunctionCall{
		Name: internalRequestContextCall,
		Arguments: map[string]any{
			"primary_target": map[string]any{
				"resource": "Namespace",
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

func TestRequestContextRejectsUnknownPrimaryTargetResource(t *testing.T) {
	_, ok := requestContextFromFunctionCall(gollm.FunctionCall{
		Name: internalRequestContextCall,
		Arguments: map[string]any{
			"primary_target": map[string]any{
				"resource": "unknown",
				"name":     "clst-pz02-shs1006-04",
			},
			"scope": map[string]any{
				"namespace": "tenant-a",
			},
			"resource_class": "unknown",
		},
	})
	if ok {
		t.Fatal("expected unknown primary target resource to be rejected")
	}
}

func TestRequestContextNormalizesPrimaryTargetResource(t *testing.T) {
	got, ok := requestContextFromFunctionCall(gollm.FunctionCall{
		Name: internalRequestContextCall,
		Arguments: map[string]any{
			"primary_target": map[string]any{
				"resource": "Cluster",
				"name":     "cluster-a",
			},
			"scope": map[string]any{
				"namespace": "tenant-a",
			},
			"resource_class": "custom_resource",
		},
	})
	if !ok {
		t.Fatal("expected request context to be accepted")
	}
	if got.PrimaryTarget.Resource != "cluster" {
		t.Fatalf("expected normalized primary target resource, got %q", got.PrimaryTarget.Resource)
	}
}

func TestRequirementAnalysisDerivesRequestContextOnlyForResourceCandidates(t *testing.T) {
	if _, ok := requestContextFromRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "cluster"},
	}); ok {
		t.Fatal("non-resource target analysis must not become a Kubernetes resource context")
	}

	got, ok := requestContextFromRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "resource"},
		Resources: []requirementResource{{
			Kind: "cluster",
			Name: "cluster-a",
			Role: "primary",
		}},
		Scope: requirementScope{Type: "namespaced", Namespace: "tenant-a"},
	})
	if !ok {
		t.Fatal("primary resource candidate should derive request context")
	}
	if got.PrimaryTarget.Resource != "cluster" || got.PrimaryTarget.Name != "cluster-a" || got.Scope.Namespace != "tenant-a" {
		t.Fatalf("unexpected derived request context: %#v", got)
	}
}

func TestRequirementAnalysisRejectsLegacyTargetKind(t *testing.T) {
	legacyCurrentCluster := "current" + "_cluster"
	_, ok := requirementAnalysisFromFunctionCall(gollm.FunctionCall{
		Name: internalRequirementAnalysisCall,
		Arguments: map[string]any{
			"request_type": "diagnosis",
			"action":       "diagnose_problem",
			"target": map[string]any{
				"kind":        legacyCurrentCluster,
				"description": "connected Kubernetes cluster",
			},
			"scope": map[string]any{
				"type": "cluster_scoped",
			},
		},
	})
	if ok {
		t.Fatal("legacy target field analysis must be rejected")
	}
}

func TestRequirementAnalysisDoesNotDeriveUnknownResourceContext(t *testing.T) {
	if _, ok := requestContextFromRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "resource"},
		Resources: []requirementResource{{
			Kind: "unknown",
			Name: "cluster-a",
			Role: "primary",
		}},
		Scope: requirementScope{Type: "namespaced", Namespace: "tenant-a"},
	}); ok {
		t.Fatal("unknown resource candidate must not become request context")
	}
}

func TestRequirementAnalysisAllowsOpenTargetCategory(t *testing.T) {
	got, ok := requirementAnalysisFromFunctionCall(gollm.FunctionCall{
		Name: internalRequirementAnalysisCall,
		Arguments: map[string]any{
			"request_type": "inspection",
			"action":       "inspect_certificate_rotation",
			"target": map[string]any{
				"category":    "certificate_lifecycle",
				"description": "certificate rotation state",
			},
			"scope": map[string]any{
				"type": "custom_scope",
			},
		},
	})
	if !ok {
		t.Fatal("open natural-language target categories should be accepted")
	}
	if got.Target.Category != "certificate_lifecycle" || got.Scope.Type != "custom_scope" {
		t.Fatalf("unexpected open classification: %#v", got)
	}
}

func TestRequirementAnalysisNormalizesResourceRoleSynonym(t *testing.T) {
	got, ok := requestContextFromRequirementAnalysis(requirementAnalysis{
		RequestType: "inspection",
		Action:      "inspect_object",
		Target:      requirementAnalysisTarget{Category: "kubernetes_resource"},
		Resources: []requirementResource{{
			Kind: "cluster",
			Name: "cluster-a",
			Role: normalizeRequirementResourceRole("target"),
		}},
		Scope: requirementScope{Type: "namespaced", Namespace: "tenant-a"},
	})
	if !ok {
		t.Fatal("target role synonym should become primary request context")
	}
	if got.PrimaryTarget.Resource != "cluster" || got.PrimaryTarget.Name != "cluster-a" {
		t.Fatalf("unexpected request context: %#v", got)
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
	if !loop.shouldRunInitialResourceGuideLookup(requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "cluster"},
		ResourceClass: "unknown",
	}, clusterClass) {
		t.Fatal("CRD resource candidates should trigger initial guide lookup even without an object name")
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

func TestFinalReportRequiresDocumentedFields(t *testing.T) {
	if _, ok := finalReportFromFunctionCall(gollm.FunctionCall{
		Name: internalFinalReportCall,
		Arguments: map[string]any{
			"conclusive": true,
		},
	}); ok {
		t.Fatal("final_report without documented required fields must be rejected")
	}

	report, ok := finalReportFromFunctionCall(gollm.FunctionCall{
		Name: internalFinalReportCall,
		Arguments: map[string]any{
			"conclusive":        true,
			"conclusion":        "cluster is healthy",
			"attempted":         []any{"checked cluster"},
			"evidence_known":    []any{"status is healthy"},
			"most_likely_cause": "none",
		},
	})
	if !ok || !report.Conclusive {
		t.Fatalf("expected valid conclusive final report, got ok=%v report=%#v", ok, report)
	}
}

func TestNextDirectionsKeepsAtMostThreeValidOptions(t *testing.T) {
	got, ok := nextDirectionsFromFunctionCall(gollm.FunctionCall{
		Name: internalNextDirectionsCall,
		Arguments: map[string]any{
			"options": []any{
				map[string]any{"kind": "different_approach", "summary": "A", "instruction": "try A"},
				map[string]any{"kind": "different_approach", "summary": "B", "instruction": "try B"},
				map[string]any{"kind": "another_guide", "summary": "C", "resource_family": "cluster", "problem_focus": "sync issue"},
				map[string]any{"kind": "different_approach", "summary": "D", "instruction": "try D"},
			},
		},
	})
	if !ok {
		t.Fatal("expected next_directions to be valid")
	}
	if len(got.Options) != 3 {
		t.Fatalf("expected only three options, got %#v", got.Options)
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
