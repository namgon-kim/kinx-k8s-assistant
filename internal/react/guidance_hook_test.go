package react

import (
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
)

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

func TestFilterResourceGuidesDropsDeletionGuideForGeneralDiagnosis(t *testing.T) {
	loop := &Loop{originalQuery: "namespace tenant-a에서 clst-a cluster가 왜 문제야?"}
	got := loop.filterResourceGuidesForRequest(&guidance.GuideSearchResult{
		Cases: []guidance.GuideCase{
			{ID: "iksv2-renew-cluster-deletion", Title: "IKS v2 Cluster Deletion and Cleanup", Tags: []string{"delete"}},
			{ID: "iksv2-renew-cluster-creation-top-level", Title: "IKS v2 Cluster Creation Top-Level Resources"},
		},
	}, "primary target resource: cluster")
	if len(got.Cases) != 1 || got.Cases[0].ID != "iksv2-renew-cluster-creation-top-level" {
		t.Fatalf("expected deletion guide to be filtered, got %#v", got.Cases)
	}
}

func TestFilterResourceGuidesKeepsDeletionGuideForDeletionDiagnosis(t *testing.T) {
	loop := &Loop{originalQuery: "cluster deletion이 왜 안 끝나?"}
	got := loop.filterResourceGuidesForRequest(&guidance.GuideSearchResult{
		Cases: []guidance.GuideCase{
			{ID: "iksv2-renew-cluster-deletion", Title: "IKS v2 Cluster Deletion and Cleanup", Tags: []string{"delete"}},
		},
	}, "primary target resource: cluster")
	if len(got.Cases) != 1 {
		t.Fatalf("expected deletion guide to be kept, got %#v", got.Cases)
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

func TestCommandMentionsResourceAllowsKubectlResourceNameShorthand(t *testing.T) {
	command := "kubectl -n tenant-a get cluster/clst-a openstackcluster/clst-a kamajicontrolplane/clst-a -o yaml"
	if !commandMentionsResource(command, "cluster") {
		t.Fatalf("expected resource/name shorthand to mention cluster resource: %s", command)
	}
	if !commandMentionsToken(command, "clst-a") {
		t.Fatalf("expected resource/name shorthand to mention object name: %s", command)
	}
}

func TestCommandMentionsResourceAllowsMultipleResourceNameShorthandArgs(t *testing.T) {
	command := "kubectl -n tenant-a get ingress/clst-a secret/clst-a-cloud-conf secret/clst-a-admin-kubeconfig -o yaml"
	if !commandMentionsResource(command, "ingress, secret") {
		t.Fatalf("expected multiple resource/name shorthand args to mention ingress and secret: %s", command)
	}
	if !commandMentionsToken(command, "clst-a, clst-a-cloud-conf, clst-a-admin-kubeconfig") {
		t.Fatalf("expected comma-separated target names to match command: %s", command)
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

func TestResourceGuideObservationPreservesGuideMetadataAndAnchorCarriesCommands(t *testing.T) {
	result := &guidance.GuideSearchResult{Cases: []guidance.GuideCase{{
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
	}}}
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
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected guide observation to contain %q, got %q", want, got)
		}
	}

	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
		},
	}
	loop.guideStepState = loop.buildGuideStepState(result)
	anchor := loop.guideStepAnchor()
	for _, want := range []string{
		"next_step_command_template: kubectl -n {{namespace}} get machinedeployment -l cluster.x-k8s.io/cluster-name={{name}},metadata.example.com/primary=true -o yaml",
		"next_step_rendered_command: kubectl -n tenant-a get machinedeployment -l cluster.x-k8s.io/cluster-name=clst-a,metadata.example.com/primary=true -o yaml",
		"next_step_expected_outcome: Check guide-provided annotations and labels",
	} {
		if !strings.Contains(anchor, want) {
			t.Fatalf("expected guide anchor to contain %q, got %q", want, anchor)
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

func TestInconsistentActionTargetMessageAllowsResourceNameShorthand(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n tenant-a get cluster/clst-a openstackcluster/clst-a kamajicontrolplane/clst-a -o yaml",
			"target": map[string]any{
				"resource":  "cluster",
				"namespace": "tenant-a",
				"name":      "clst-a",
			},
		},
	}
	if got, invalid := inconsistentActionTargetMessage(call); invalid {
		t.Fatalf("expected resource/name shorthand command to pass, got %q", got)
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

func TestInconsistentActionTargetMessageAllowsAllNamespacesScope(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get machinedeployment -A -o yaml",
			"target": map[string]any{
				"resource":  "machinedeployment",
				"namespace": "all",
			},
		},
	}
	if got, invalid := inconsistentActionTargetMessage(call); invalid {
		t.Fatalf("expected all-namespaces command to pass, got %q", got)
	}

	call.Arguments["command"] = "kubectl get machinedeployment -o yaml"
	got, invalid := inconsistentActionTargetMessage(call)
	if !invalid {
		t.Fatal("expected missing all-namespaces flag to be rejected")
	}
	if strings.Contains(got, "-n all") || strings.Contains(got, "--namespace=all") {
		t.Fatalf("correction must not ask for namespace all, got %q", got)
	}
}

func TestInconsistentActionTargetMessageIgnoresUnknownNamePlaceholder(t *testing.T) {
	call := gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get node -n tenant-a",
			"target": map[string]any{
				"resource":  "node",
				"namespace": "tenant-a",
				"name":      "unknown",
			},
		},
	}
	if got, invalid := inconsistentActionTargetMessage(call); invalid {
		t.Fatalf("unknown target name placeholder should not be enforced, got %q", got)
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

func TestRequestNamespaceInvariantRejectsMutatingCommandWithoutNamespace(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "configmap", Name: "app-config"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	got, invalid := loop.requestNamespaceInvariantMessage(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl create configmap app-config --from-literal=key=value",
			"target": map[string]any{
				"resource": "configmap",
				"name":     "app-config",
			},
		},
	})
	if !invalid || !strings.Contains(got, `Request namespace is "web"`) || !strings.Contains(got, "omits namespace") {
		t.Fatalf("expected missing namespace to be rejected, got invalid=%v message=%q", invalid, got)
	}
}

func TestRequestNamespaceInvariantRejectsDifferentMutationNamespace(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "configmap", Name: "app-config"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	got, invalid := loop.requestNamespaceInvariantMessage(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n default create configmap app-config --from-literal=key=value",
			"target": map[string]any{
				"resource":  "configmap",
				"namespace": "default",
				"name":      "app-config",
			},
		},
	})
	if !invalid || !strings.Contains(got, `action target namespace is "default"`) {
		t.Fatalf("expected different namespace to be rejected, got invalid=%v message=%q", invalid, got)
	}
}

func TestRequestNamespaceInvariantAllowsMatchingMutationNamespace(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "configmap", Name: "app-config"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	got, invalid := loop.requestNamespaceInvariantMessage(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n web create configmap app-config --from-literal=key=value",
			"target": map[string]any{
				"resource": "configmap",
				"name":     "app-config",
			},
		},
	})
	if invalid {
		t.Fatalf("expected matching namespace to pass, got %q", got)
	}
}

func TestRequestNamespaceInvariantAllowsClusterScopedMutation(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "node", Name: "node-a"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	got, invalid := loop.requestNamespaceInvariantMessage(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl label node node-a maintenance=true",
			"target": map[string]any{
				"resource": "node",
				"name":     "node-a",
			},
		},
	})
	if invalid {
		t.Fatalf("expected cluster-scoped mutation to pass namespace invariant, got %q", got)
	}
}

func TestMutationLifecycleCreatesPendingVerificationAfterMutation(t *testing.T) {
	loop := &Loop{
		cfg: &config.Config{},
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "configmap", Name: "app-config"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
		actionSeq: 3,
	}
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web create configmap app-config --from-literal=key=value",
				"target": map[string]any{
					"resource":  "configmap",
					"namespace": "web",
					"name":      "app-config",
				},
				"expected_observation": "configmap exists",
			},
		},
		ModifiesResource: "yes",
	}, map[string]any{"status": "ok"})

	if loop.pendingMutationVerification == nil {
		t.Fatal("expected pending mutation verification")
	}
	got := loop.pendingMutationVerification
	if len(got.Requirements) != 1 {
		t.Fatalf("expected one direct evidence requirement, got %#v", got.Requirements)
	}
	direct := got.Requirements[0]
	if direct.Kind != "direct_effect" || direct.Target.Resource != "configmap" || direct.Target.Namespace != "web" || direct.Target.Name != "app-config" {
		t.Fatalf("unexpected direct evidence target: %#v", direct)
	}
	if !strings.Contains(direct.SuggestedCommand, "kubectl get configmap app-config -n web -o yaml") {
		t.Fatalf("unexpected verification command hint: %q", direct.SuggestedCommand)
	}
}

func TestMutationLifecycleDoesNotStartInReadOnlyMode(t *testing.T) {
	loop := &Loop{cfg: &config.Config{ReadOnly: true}}
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web create configmap app-config --from-literal=key=value",
			},
		},
		ModifiesResource: "yes",
	}, map[string]any{"status": "ok"})

	if loop.pendingMutationVerification != nil {
		t.Fatalf("read-only mode must not start mutation verification, got %#v", loop.pendingMutationVerification)
	}
}

func TestMutationLifecycleSkipsGenericVerificationForUnmappedSuccessfulMutation(t *testing.T) {
	loop := &Loop{cfg: &config.Config{}}
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "bash",
			Arguments: map[string]any{
				"command": "kubectl apply -f /tmp/rendered.yaml",
			},
		},
		ModifiesResource: "unknown",
	}, map[string]any{"status": "ok"})

	if loop.pendingMutationVerification != nil {
		t.Fatalf("successful unmapped mutation must not create generic verification, got %#v", loop.pendingMutationVerification)
	}
}

func TestMutationLifecycleRejectsFinalReportBeforeVerification(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{{
				ID:     "direct_effect",
				Kind:   "direct_effect",
				Target: actionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
			}},
		},
	}
	if !loop.enforcePendingMutationVerification([]gollm.FunctionCall{{
		Name: internalFinalReportCall,
		Arguments: map[string]any{
			"conclusive":        true,
			"attempted":         []any{"created configmap"},
			"evidence_known":    []any{"create returned ok"},
			"most_likely_cause": "missing configmap",
			"conclusion":        "fixed",
		},
	}}) {
		t.Fatal("expected final_report to be rejected while verification is pending")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
}

func TestMutationLifecycleRequiresExactReadOnlyVerification(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{{
				ID:     "direct_effect",
				Kind:   "direct_effect",
				Target: actionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
			}},
		},
	}
	if _, ok := loop.mutationVerificationCallMatchID(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get configmap app-config -n default -o yaml",
		},
	}); ok {
		t.Fatal("verification with different namespace must not match")
	}
	if _, ok := loop.mutationVerificationCallMatchID(gollm.FunctionCall{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get configmap app-config -n web -o yaml",
		},
	}); !ok {
		t.Fatal("expected exact read-only verification to match")
	}
}

func TestMutationLifecycleAwaitsResultAfterSuccessfulVerification(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{{
				ID:     "direct_effect",
				Kind:   "direct_effect",
				Target: actionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
			}},
			Satisfied: map[string]bool{},
		},
	}
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get configmap app-config -n web -o yaml",
			},
		},
		ModifiesResource: "no",
	}, map[string]any{"status": "ok"})
	if loop.pendingMutationVerification == nil || !loop.pendingMutationVerification.AwaitingResult {
		t.Fatalf("expected verification to await interpretation result, got %#v", loop.pendingMutationVerification)
	}
}

func TestMutationLifecycleKeepsPendingUntilOutcomeEvidenceSatisfied(t *testing.T) {
	loop := &Loop{
		cfg: &config.Config{},
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "deployment", Name: "web-app"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web create configmap app-config --from-literal=key=value",
				"target": map[string]any{
					"resource":  "configmap",
					"namespace": "web",
					"name":      "app-config",
				},
			},
		},
		ModifiesResource: "yes",
	}, map[string]any{"status": "ok"})
	if loop.pendingMutationVerification == nil || len(loop.pendingMutationVerification.Requirements) != 2 {
		t.Fatalf("expected direct and outcome evidence requirements, got %#v", loop.pendingMutationVerification)
	}

	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get configmap app-config -n web -o yaml",
			},
		},
		ModifiesResource: "no",
	}, map[string]any{"status": "ok"})
	if loop.pendingMutationVerification == nil {
		t.Fatal("direct evidence alone must not clear pending verification while outcome evidence remains")
	}
	directID := loop.pendingMutationVerification.Requirements[0].ID
	if !loop.pendingMutationVerification.Satisfied[directID] {
		t.Fatalf("expected direct evidence to be marked satisfied, got %#v", loop.pendingMutationVerification.Satisfied)
	}

	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get deployment web-app -n web -o yaml",
			},
		},
		ModifiesResource: "no",
	}, map[string]any{"status": "ok"})
	if loop.pendingMutationVerification == nil || !loop.pendingMutationVerification.AwaitingResult {
		t.Fatalf("expected pending verification to await interpretation after outcome evidence, got %#v", loop.pendingMutationVerification)
	}
}

func TestMutationLifecycleConsumesResolvedVerificationResult(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			AwaitingResult: true,
			Requirements: []mutationEvidenceRequirement{{
				ID:     "mutation_1_direct_effect",
				Kind:   "direct_effect",
				Target: actionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
			}},
			Satisfied: map[string]bool{"mutation_1_direct_effect": true},
		},
	}
	_, handled := loop.consumeMutationVerificationResult([]gollm.FunctionCall{{
		Name: internalMutationVerificationResultCall,
		Arguments: map[string]any{
			"status":           "resolved",
			"evidence_summary": []any{"configmap exists and deployment is available"},
			"reason":           "verification evidence shows the requested state is healthy",
		},
	}})
	if !handled {
		t.Fatal("expected mutation_verification_result to be handled")
	}
	if loop.pendingMutationVerification != nil {
		t.Fatalf("expected pending verification to clear after resolved interpretation, got %#v", loop.pendingMutationVerification)
	}
}

func TestMutationLifecycleProgressingDirectiveKeepsNextAction(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			AwaitingResult: true,
			Requirements: []mutationEvidenceRequirement{{
				ID:     "mutation_1_outcome_primary_target",
				Kind:   "outcome_evidence",
				Target: actionTarget{Resource: "deployment", Namespace: "web", Name: "web-app"},
			}},
			Satisfied: map[string]bool{"mutation_1_outcome_primary_target": true},
		},
	}
	_, handled := loop.consumeMutationVerificationResult([]gollm.FunctionCall{{
		Name: internalMutationVerificationResultCall,
		Arguments: map[string]any{
			"status":           "progressing",
			"evidence_summary": []any{"deployment rollout is still progressing"},
			"reason":           "new replicaset is not fully available yet",
			"next_action":      "wait briefly, then run kubectl -n web rollout status deployment/web-app",
		},
	}})
	if !handled {
		t.Fatal("expected progressing mutation_verification_result to be handled")
	}
	if !loop.mutationContinuationRequired {
		t.Fatal("expected progressing result to require mutation continuation")
	}
	if !strings.Contains(loop.pendingResponseDirective, "rollout status deployment/web-app") {
		t.Fatalf("expected next_action to be preserved in directive, got %q", loop.pendingResponseDirective)
	}
}

func TestMutationVerificationResultRequiresNextActionForUnresolved(t *testing.T) {
	_, ok := mutationVerificationResultFromFunctionCall(gollm.FunctionCall{
		Name: internalMutationVerificationResultCall,
		Arguments: map[string]any{
			"status":           "unresolved",
			"evidence_summary": []any{"deployment remains unavailable"},
			"reason":           "config is still missing",
		},
	})
	if ok {
		t.Fatal("unresolved mutation_verification_result without next_action must be invalid")
	}
}

func TestMutationContinuationBlocksReportAndClearsAfterUsefulObservation(t *testing.T) {
	loop := &Loop{mutationContinuationRequired: true}
	if !loop.enforceMutationContinuation([]gollm.FunctionCall{{Name: internalFinalReportCall}}) {
		t.Fatal("final_report must be blocked while mutation continuation is required")
	}

	loop.mutationContinuationRequired = true
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web rollout status deployment/web-app",
			},
		},
		ModifiesResource: "no",
	}, map[string]any{"status": "ok"})
	if loop.mutationContinuationRequired {
		t.Fatal("successful observation should clear mutation continuation requirement")
	}
}

func TestMutationLifecycleAllowsMultipleVerificationActions(t *testing.T) {
	loop := &Loop{
		pendingMutationVerification: &pendingMutationVerification{
			Requirements: []mutationEvidenceRequirement{
				{
					ID:     "mutation_1_direct_effect",
					Kind:   "direct_effect",
					Target: actionTarget{Resource: "configmap", Namespace: "web", Name: "app-config"},
				},
				{
					ID:     "mutation_1_outcome_primary_target",
					Kind:   "outcome_evidence",
					Target: actionTarget{Resource: "deployment", Namespace: "web", Name: "web-app"},
				},
			},
			Satisfied: map[string]bool{},
		},
	}
	if !loop.mutationVerificationCallsMatch([]gollm.FunctionCall{
		{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get configmap app-config -n web -o yaml",
			},
		},
		{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl get deployment web-app -n web -o yaml",
			},
		},
	}) {
		t.Fatal("expected multiple read-only verification actions to be allowed")
	}
}

func TestMutationLifecycleAccumulatesSequentialMutationsForSameGoal(t *testing.T) {
	loop := &Loop{
		cfg: &config.Config{},
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "deployment", Name: "web-app"},
			Scope:         requestScope{Namespace: "web"},
			ResourceClass: "built_in",
		},
	}
	loop.actionSeq = 1
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web create configmap app-config --from-literal=key=value",
				"target": map[string]any{
					"resource":  "configmap",
					"namespace": "web",
					"name":      "app-config",
				},
			},
		},
		ModifiesResource: "yes",
	}, map[string]any{"status": "ok"})
	loop.actionSeq = 2
	loop.trackMutationVerification(PendingCall{
		FunctionCall: gollm.FunctionCall{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": "kubectl -n web rollout restart deployment/web-app",
				"target": map[string]any{
					"resource":  "deployment",
					"namespace": "web",
					"name":      "web-app",
				},
			},
		},
		ModifiesResource: "yes",
	}, map[string]any{"status": "ok"})

	if loop.pendingMutationVerification == nil {
		t.Fatal("expected pending verification")
	}
	if len(loop.pendingMutationVerification.Requirements) != 3 {
		t.Fatalf("expected direct configmap, outcome deployment, and direct deployment requirements, got %#v", loop.pendingMutationVerification.Requirements)
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

	report, ok = finalReportFromFunctionCall(gollm.FunctionCall{
		Name: internalFinalReportCall,
		Arguments: map[string]any{
			"conclusive":        false,
			"attempted":         []any{"checked cluster"},
			"evidence_missing":  []any{"workload kubeconfig was not available"},
			"blockers":          []any{"RBAC denied node inspection"},
			"most_likely_cause": "inconclusive",
		},
	})
	if !ok || report.Conclusive {
		t.Fatalf("expected valid inconclusive final report, got ok=%v report=%#v", ok, report)
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

func TestGuideProgressObservationUsefulRejectsBlockedResults(t *testing.T) {
	blocked := map[string]any{"status": "blocked", "error": "read-only mode is enabled"}
	if guideProgressObservationUseful(blocked) {
		t.Fatal("blocked observation must not complete guide progress")
	}
	declined := map[string]any{"status": "declined"}
	if guideProgressObservationUseful(declined) {
		t.Fatal("declined observation must not complete guide progress")
	}
	success := map[string]any{"status": "ok", "items": []any{}}
	if !guideProgressObservationUseful(success) {
		t.Fatal("successful observation should be allowed to complete guide progress")
	}
}

func TestGuideStepCompletedRejectsEvidenceNotUseful(t *testing.T) {
	if _, ok := guideStepCompletedFromFunctionCall(gollm.FunctionCall{
		Arguments: map[string]any{
			"guide_progress": map[string]any{
				"step_completed":  1,
				"evidence_useful": false,
			},
		},
	}); ok {
		t.Fatal("guide_progress with evidence_useful=false must not complete a step")
	}
}

func TestConsumeGuideProgressCompletesNestedGuideStep(t *testing.T) {
	loop := &Loop{
		guideStepState: &guideStepState{
			TotalSteps: 2,
			Completed:  map[int]bool{},
			StepDetails: []guideStepDetail{
				{Index: 1, Description: "inspect first signal"},
				{Index: 2, Description: "inspect second signal"},
			},
		},
		phaseStepState: &phaseStepState{
			PhaseSteps: []phaseStep{{
				Index: 1,
				Name:  "guided_diagnosis",
			}},
			CurrentPhaseIndex: 1,
			Completed:         map[int]bool{},
		},
	}

	remaining, handled := loop.consumeGuideProgress([]gollm.FunctionCall{{
		Name: internalGuideProgressCall,
		Arguments: map[string]any{
			"step_completed":  1,
			"evidence_useful": true,
		},
	}})
	if !handled {
		t.Fatal("expected guide_progress to be handled")
	}
	if len(remaining) != 0 {
		t.Fatalf("unexpected remaining calls: %#v", remaining)
	}
	if !loop.guideStepState.Completed[1] {
		t.Fatal("expected guide step 1 to be completed")
	}
}

func TestConsumeGuideProgressLastStepRequestsGuidedPhaseProgress(t *testing.T) {
	loop := &Loop{
		guideStepState: &guideStepState{
			TotalSteps: 1,
			Completed:  map[int]bool{},
			StepDetails: []guideStepDetail{
				{Index: 1, Description: "inspect final signal"},
			},
		},
		phaseStepState: &phaseStepState{
			PhaseSteps: []phaseStep{{
				Index: 1,
				Name:  "guided_diagnosis",
			}},
			CurrentPhaseIndex: 1,
			Completed:         map[int]bool{},
		},
	}

	remaining, handled := loop.consumeGuideProgress([]gollm.FunctionCall{{
		Name: internalGuideProgressCall,
		Arguments: map[string]any{
			"step_completed":  1,
			"evidence_useful": true,
		},
	}})
	if !handled {
		t.Fatal("expected guide_progress to be handled when no trailing calls remain")
	}
	if len(remaining) != 0 {
		t.Fatalf("unexpected remaining calls: %#v", remaining)
	}
	if !loop.guidedPhaseProgressRequested {
		t.Fatal("expected completed guided_diagnosis to request phase_progress")
	}
	if !strings.Contains(loop.pendingResponseDirective, "phase_progress") {
		t.Fatalf("expected phase_progress directive, got %q", loop.pendingResponseDirective)
	}
}

func TestConsumeGuideProgressPreservesTrailingCalls(t *testing.T) {
	loop := &Loop{
		guideStepState: &guideStepState{
			TotalSteps: 2,
			Completed:  map[int]bool{},
		},
		phaseStepState: &phaseStepState{
			PhaseSteps: []phaseStep{{
				Index: 1,
				Name:  "guided_diagnosis",
			}},
			CurrentPhaseIndex: 1,
			Completed:         map[int]bool{},
		},
	}
	trailing := gollm.FunctionCall{
		Name: internalPhaseProgressCall,
		Arguments: map[string]any{
			"phase_completed": 1,
			"next_phase":      "final_report",
		},
	}

	remaining, handled := loop.consumeGuideProgress([]gollm.FunctionCall{
		{
			Name: internalGuideProgressCall,
			Arguments: map[string]any{
				"step_completed":  1,
				"evidence_useful": true,
			},
		},
		trailing,
	})
	if handled {
		t.Fatal("guide_progress with trailing calls should flow through to the remaining pipeline")
	}
	if len(remaining) != 1 || remaining[0].Name != internalPhaseProgressCall {
		t.Fatalf("expected trailing phase_progress to be preserved, got %#v", remaining)
	}
	if !loop.guideStepState.Completed[1] {
		t.Fatal("expected guide step to be recorded before trailing calls continue")
	}
}

func TestEnforceRequestedDirectiveAllowsOnlyRequestedCall(t *testing.T) {
	phaseOnly := &Loop{guidedPhaseProgressRequested: true}
	if phaseOnly.enforceRequestedStructuredDirective([]gollm.FunctionCall{{
		Name: internalPhaseProgressCall,
	}}) {
		t.Fatal("sole requested phase_progress should pass through")
	}

	phaseWithAction := &Loop{guidedPhaseProgressRequested: true}
	if !phaseWithAction.enforceRequestedStructuredDirective([]gollm.FunctionCall{
		{Name: internalPhaseProgressCall},
		{Name: "kubectl"},
	}) {
		t.Fatal("phase_progress mixed with action should be corrected")
	}

	finalOnly := &Loop{finalReportRequested: true}
	if finalOnly.enforceRequestedStructuredDirective([]gollm.FunctionCall{{
		Name: internalFinalReportCall,
	}}) {
		t.Fatal("sole requested final_report should pass through")
	}

	finalWithAction := &Loop{finalReportRequested: true}
	if !finalWithAction.enforceRequestedStructuredDirective([]gollm.FunctionCall{
		{Name: internalFinalReportCall},
		{Name: "kubectl"},
	}) {
		t.Fatal("final_report mixed with action should be corrected")
	}
}

func TestFallbackNextDirectionsUsesPendingFinalReportGaps(t *testing.T) {
	loop := &Loop{
		pendingFinalReport: &finalReport{
			EvidenceMissing: []string{"workload kubeconfig was not available"},
			Blockers:        []string{"node providerID could not be checked"},
		},
	}
	got := loop.fallbackNextDirections()
	if len(got.Options) != 1 {
		t.Fatalf("expected one generic continuation option, got %#v", got.Options)
	}
	if got.Options[0].Kind != "different_approach" || !strings.Contains(got.Options[0].Instruction, "workload kubeconfig") {
		t.Fatalf("unexpected fallback option: %#v", got.Options[0])
	}
}

func TestFallbackNextDirectionsAllowsOnlyRuntimeChoicesWithoutReportGaps(t *testing.T) {
	got := (&Loop{}).fallbackNextDirections()
	if len(got.Options) != 0 {
		t.Fatalf("expected no model-derived options when report has no gaps, got %#v", got.Options)
	}
}

func TestPriorConversationStateUsesExplicitFollowUpMemory(t *testing.T) {
	loop := &Loop{
		originalQuery: "namespace tenant-a에서 clst-a cluster 문제 찾아줘",
		requirementAnalysis: &requirementAnalysis{
			RequestType: "diagnosis",
			Action:      "diagnose_problem",
			Target:      requirementAnalysisTarget{Category: "kubernetes_resource", Description: "Cluster clst-a"},
			Scope:       requirementScope{Type: "namespaced", Namespace: "tenant-a"},
			Resources: []requirementResource{{
				Kind:      "cluster",
				Name:      "clst-a",
				Namespace: "tenant-a",
				Role:      "primary",
			}},
		},
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
			ResourceClass: "custom_resource",
		},
		lastAssistantText: "MachineDeployment has zero available replicas.",
	}
	loop.captureConversationMemory()
	loop.originalQuery = ""
	loop.requirementAnalysis = nil
	loop.requestContext = nil
	loop.lastAssistantText = ""

	got := loop.priorConversationStateMessage()
	for _, want := range []string{
		"previous_requirement_analysis:",
		"previous_request_context:",
		`"namespace":"tenant-a"`,
		`"resource":"cluster"`,
		"Follow-up handling:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected prior memory to contain %q, got %q", want, got)
		}
	}
}

func TestRequirementAnalysisDefaultsFollowUpToPriorRequestContext(t *testing.T) {
	loop := &Loop{
		lastRequestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
			ResourceClass: "custom_resource",
		},
	}
	got := loop.applyPriorContextToFollowUpRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "unknown", Description: "node group is not working"},
		Scope:       requirementScope{Type: "unknown"},
		OperationalFocus: &requirementOperationalFocus{
			Summary:               "worker group availability",
			RelationshipToPrimary: "related_to_primary",
			ChangedFromPrevious:   true,
			Reason:                "follow-up narrows the previous Cluster diagnosis to worker group behavior",
			EvidenceNeeds:         []string{"MachineDeployment status related to the previous target"},
		},
	})
	resource := primaryRequirementResource(got.Resources)
	if resource.Kind != "cluster" || resource.Name != "clst-a" || resource.Namespace != "tenant-a" {
		t.Fatalf("expected prior cluster context as default, got %#v", resource)
	}
	if resource.Source != "previous_context" {
		t.Fatalf("expected previous context source, got %#v", resource)
	}
	if got.Scope.Namespace != "tenant-a" {
		t.Fatalf("expected prior namespace, got %#v", got.Scope)
	}
	if got.OperationalFocus == nil || !strings.Contains(got.OperationalFocus.Summary, "worker group") {
		t.Fatalf("expected operational focus to be preserved, got %#v", got.OperationalFocus)
	}
}

func TestRequirementAnalysisRetriesPreviousAnswerInsteadOfClarification(t *testing.T) {
	loop := &Loop{
		originalQuery: "아닌 것 같아. 다시 정확하게.",
		lastRequirementAnalysis: &requirementAnalysis{
			RequestType: "inspection",
			Action:      "summarize_pods",
			Target:      requirementAnalysisTarget{Category: "kubernetes_resource", Description: "Pods in the cluster"},
			Scope:       requirementScope{Type: "cluster_scoped"},
			Resources: []requirementResource{{
				Kind:   "pod",
				Role:   "primary",
				Source: "user_request",
			}},
			Evidence: []string{"Pod distribution across nodes and namespaces"},
		},
		lastRequestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "pod"},
			ResourceClass: "built_in",
		},
	}

	got := loop.applyPriorContextToFollowUpRequirementAnalysis(requirementAnalysis{
		RequestType: "explanation",
		Action:      "clarify_request",
		Target:      requirementAnalysisTarget{Category: "conversation", Description: "User request clarification"},
		Scope:       requirementScope{Type: "unknown"},
		OperationalFocus: &requirementOperationalFocus{
			Summary:               "Request clarification",
			RelationshipToPrimary: "unclear",
			ChangedFromPrevious:   true,
		},
	})
	resource := primaryRequirementResource(got.Resources)
	if got.RequestType != "inspection" || got.Action != "summarize_pods" {
		t.Fatalf("expected previous inspection request to be reused, got %#v", got)
	}
	if resource.Kind != "pod" {
		t.Fatalf("expected previous pod target, got %#v", resource)
	}
	if got.OperationalFocus == nil || got.OperationalFocus.RelationshipToPrimary != "same_primary" || got.OperationalFocus.ChangedFromPrevious {
		t.Fatalf("expected retry focus on same primary, got %#v", got.OperationalFocus)
	}
}

func TestRequirementAnalysisDemotesModelInferredPrimaryToOperationalFocusHint(t *testing.T) {
	loop := &Loop{
		lastRequestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
			ResourceClass: "custom_resource",
		},
	}
	got := loop.applyPriorContextToFollowUpRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "kubernetes_resource", Description: "worker group is not working"},
		Scope:       requirementScope{Type: "namespaced", Namespace: "tenant-a"},
		Resources: []requirementResource{{
			Kind:   "machinedeployment",
			Role:   "primary",
			Source: "model_inference",
		}},
		OperationalFocus: &requirementOperationalFocus{
			Summary:               "worker group availability",
			RelationshipToPrimary: "related_to_primary",
			ChangedFromPrevious:   true,
		},
	})
	resource := primaryRequirementResource(got.Resources)
	if resource.Kind != "cluster" || resource.Name != "clst-a" || resource.Source != "previous_context" {
		t.Fatalf("expected previous cluster primary, got %#v", resource)
	}
	if got.OperationalFocus == nil || len(got.OperationalFocus.RelatedResourceHints) == 0 {
		t.Fatalf("expected inferred primary to become focus hint, got %#v", got.OperationalFocus)
	}
	hint := got.OperationalFocus.RelatedResourceHints[0]
	if hint.Kind != "machinedeployment" || hint.Source != "model_inference" {
		t.Fatalf("unexpected focus hint: %#v", hint)
	}
}

func TestRequirementAnalysisKeepsUserRequestPrimary(t *testing.T) {
	loop := &Loop{
		lastRequestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
			ResourceClass: "custom_resource",
		},
	}
	got := loop.applyPriorContextToFollowUpRequirementAnalysis(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target:      requirementAnalysisTarget{Category: "kubernetes_resource", Description: "MachineDeployment md-a"},
		Scope:       requirementScope{Type: "namespaced", Namespace: "tenant-a"},
		Resources: []requirementResource{{
			Kind:   "machinedeployment",
			Name:   "md-a",
			Role:   "primary",
			Source: "user_request",
		}},
		OperationalFocus: &requirementOperationalFocus{
			Summary:               "MachineDeployment health",
			RelationshipToPrimary: "new_primary",
			ChangedFromPrevious:   true,
		},
	})
	resource := primaryRequirementResource(got.Resources)
	if resource.Kind != "machinedeployment" || resource.Name != "md-a" || resource.Source != "user_request" {
		t.Fatalf("expected explicit user primary to be preserved, got %#v", resource)
	}
}

func TestRejectConversationalToolCallsBeforeDispatch(t *testing.T) {
	loop := &Loop{
		requirementAnalysis: &requirementAnalysis{
			RequestType: "explanation",
			Action:      "clarify_request",
			Target:      requirementAnalysisTarget{Category: "conversation"},
		},
	}

	if !loop.rejectConversationalToolCalls([]gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": "echo 'Could you please rephrase?'",
		},
	}}) {
		t.Fatal("expected conversational tool call to be rejected before dispatch")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
}

func TestRequirementAnalysisDoesNotHardcodeNodeGroupClarification(t *testing.T) {
	if message, ok := requirementAnalysisClarificationMessage(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target: requirementAnalysisTarget{
			Category:    "kubernetes_resource",
			Description: "node group is not working normally",
		},
		Resources: []requirementResource{{
			Kind: "nodegroup",
			Role: "primary",
		}},
	}); ok {
		t.Fatalf("node group must not be runtime-hardcoded into a clarification path, got %q", message)
	}
}

func TestRequirementAnalysisDoesNotClarifyBroadClusterEnvironmentDiagnosis(t *testing.T) {
	if message, ok := requirementAnalysisClarificationMessage(requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target: requirementAnalysisTarget{
			Category:    "cluster_environment",
			Description: "current cluster health",
		},
	}); ok {
		t.Fatalf("did not expect broad cluster environment clarification, got %q", message)
	}
}

func TestInferGuideStepCompletedFromMatchingNextStepCommand(t *testing.T) {
	loop := &Loop{
		requestContext: &requestContext{
			PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "clst-a"},
			Scope:         requestScope{Namespace: "tenant-a"},
		},
		guideStepState: &guideStepState{
			TotalSteps: 2,
			Completed:  map[int]bool{},
			StepDetails: []guideStepDetail{
				{
					Index:           1,
					Description:     "Inspect top-level cluster resources",
					CommandTemplate: "kubectl -n {{namespace}} get cluster/{{cluster_name}} openstackcluster/{{cluster_name}} kamajicontrolplane/{{cluster_name}} -o yaml",
					RenderedCommand: "kubectl -n tenant-a get cluster/clst-a openstackcluster/clst-a kamajicontrolplane/clst-a -o yaml",
				},
				{
					Index:           2,
					Description:     "Inspect cloud config Secrets",
					CommandTemplate: "kubectl -n {{namespace}} get secret/{{cluster_name}}-cloud-conf secret/{{cluster_name}}-ccm-cloud-config -o yaml",
					RenderedCommand: "kubectl -n tenant-a get secret/clst-a-cloud-conf secret/clst-a-ccm-cloud-config -o yaml",
				},
			},
		},
	}
	step, ok := loop.inferGuideStepCompletedFromFunctionCall(gollm.FunctionCall{
		Arguments: map[string]any{
			"command": "kubectl -n tenant-a get cluster/clst-a openstackcluster/clst-a kamajicontrolplane/clst-a -o yaml",
		},
	})
	if !ok || step != 1 {
		t.Fatalf("expected step 1 to be inferred, got step=%d ok=%v", step, ok)
	}

	if step, ok = loop.inferGuideStepCompletedFromFunctionCall(gollm.FunctionCall{
		Arguments: map[string]any{
			"command": "kubectl -n tenant-a get cluster/clst-a -o yaml",
		},
	}); ok {
		t.Fatalf("did not expect partial same-resource command to infer guide progress, got step=%d", step)
	}

	if step, ok = loop.inferGuideStepCompletedFromFunctionCall(gollm.FunctionCall{
		Arguments: map[string]any{
			"command": "kubectl -n tenant-a get events --field-selector=involvedObject.name=clst-a",
		},
	}); ok {
		t.Fatalf("did not expect unrelated command to infer guide progress, got step=%d", step)
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
