package react

func requirementAnalysisPrompt() string {
	return `Requirement-analysis phase for this user request.

Return exactly one JSON code block containing only "requirement_analysis". Do not choose a tool, do not request resource guidance, and do not provide a final answer in this response.

Classify the natural-language request first. The target may be a Kubernetes resource, but it may also be a cluster environment, namespace scope, logs, events, metrics, network, storage, configuration, policy, local file, external system, conversation, or another non-resource subject.

Use this schema:
{
  "requirement_analysis": {
    "request_type": "Use the definition prompt. Prefer a stable value such as diagnosis, remediation, inspection, lookup, summary, explanation, generation, configuration, mutation, comparison, operation, or other.",
    "action": "Concrete requested action such as diagnose_problem, remediate_problem, summarize_events, inspect_config, create_manifest",
    "target": {
      "category": "Use the definition prompt. Prefer a stable category such as cluster_environment, namespace_scope, kubernetes_resource, workload, node, control_plane, network, storage, security_policy, access_control, scheduling, logs, events, metrics, configuration, manifest, local_file, external_system, conversation, unknown, or other.",
      "name": "Target name only when the request names the target itself",
      "description": "Concise description of what the user is asking about"
    },
    "scope": {
      "type": "Use the definition prompt. Prefer namespaced, cluster_scoped, all_namespaces, cross_namespace, external, local, unknown, or other.",
      "namespace": "Namespace only when provided or clearly implied"
    },
    "resource_candidates": [
      {
        "kind": "Concrete Kubernetes resource kind only when the request names or clearly implies one",
        "name": "Object name when provided",
        "namespace": "Object namespace when provided",
        "role": "Use primary for the user's Kubernetes object subject; otherwise use scope, related, evidence, owner, or dependent."
      }
    ],
    "evidence_needs": ["Facts or live evidence needed before deciding the next action"],
    "constraints": ["Constraints from the user or runtime context"],
    "ambiguities": ["Ambiguities that matter for the next action"]
  }
}

Rules:
- Use only target.category and resource_candidates for target classification.
- Treat broad phrases such as "this cluster", "current cluster", or "solve this cluster's problem" as target.category="cluster_environment" with no resource_candidates unless the user names a concrete Kubernetes resource kind/object.
- Do not create a resource candidate just because the word "cluster" appears. Create kind="cluster" only when the user identifies a Kubernetes Cluster object by kind/name or the request clearly targets that Kubernetes object.
- Namespace phrases are normally scope, not the target resource, unless the user explicitly asks about a Namespace object.
- If no concrete Kubernetes resource kind is identifiable, leave resource_candidates empty and capture the ambiguity instead of inventing "unknown" as a resource kind.`
}

func requirementAnalysisDefinitionPrompt() string {
	return `Requirement-analysis value definitions.

These definitions are guidance for consistent classification, not a reason to force a Kubernetes resource target. Prefer the listed stable values. If none fits, use "other" and explain the nuance in target.description, evidence_needs, or ambiguities.

request_type:
- diagnosis: find cause or health/problem state.
- remediation: solve, repair, recover, or recommend a fix.
- inspection: inspect current live state or configuration.
- lookup: find a specific object, field, setting, or fact.
- summary: summarize observed output, events, logs, or prior steps.
- explanation: explain meaning, concept, command, or result.
- generation: generate a manifest, command, script, or document.
- configuration: configure assistant, kubeconfig, context, language, readonly, or runtime settings.
- mutation: create, update, patch, delete, scale, restart, apply, or otherwise change resources.
- comparison: compare objects, states, versions, or options.
- operation: perform a concrete operational workflow that is not mainly diagnosis/remediation.
- other: use when no stable type fits.

target.category:
- cluster_environment: the connected Kubernetes cluster as an environment, not a Kubernetes Cluster object.
- namespace_scope: a namespace as a scope or operating boundary.
- kubernetes_resource: a concrete Kubernetes object kind/name is the subject.
- workload: Pod, Deployment, StatefulSet, DaemonSet, Job, CronJob, or workload behavior in general.
- node: node capacity, readiness, pressure, labels, taints, or scheduling surface.
- control_plane: API server, scheduler, controller-manager, etcd, admission, or cluster control plane.
- network: Service, Ingress, DNS, CNI, NetworkPolicy, connectivity, routing, or ports.
- storage: PVC, PV, StorageClass, CSI, volume attachment, mount, or disk behavior.
- security_policy: RBAC, PodSecurity, policy, admission, secret, certificate, or compliance.
- access_control: kubeconfig, context, auth, impersonation, permissions, or identity.
- scheduling: placement, affinity, tolerations, taints, quotas, resources, or scheduler decisions.
- logs: logs as the primary target.
- events: Kubernetes events as the primary target.
- metrics: metrics, usage, top, resource consumption, or time-series evidence.
- configuration: assistant/runtime/application/Kubernetes configuration.
- manifest: YAML/JSON manifest content or generation.
- local_file: local files, documents, scripts, or repo artifacts.
- external_system: cloud provider, registry, load balancer, DNS provider, or non-Kubernetes system.
- conversation: meta question about the assistant's previous answer or behavior.
- unknown: target is unclear and clarification may be needed.
- other: target is clear but no listed category fits.

scope.type:
- namespaced: one namespace scopes the target.
- cluster_scoped: cluster-wide or cluster-scoped object.
- all_namespaces: explicitly all namespaces.
- cross_namespace: multiple specific namespaces.
- external: outside Kubernetes.
- local: local file/repository/workspace.
- unknown: scope is unclear.
- other: scope is clear but no listed type fits.

resource_candidates.role:
- primary: the Kubernetes object the user is asking about.
- scope: Kubernetes object used only as scope, such as Namespace when it is truly the scope.
- related: related object that may help diagnose the primary target.
- evidence: object or kind needed only to gather evidence.
- owner: owner/controller of the primary target.
- dependent: dependent/child object of the primary target.`
}
