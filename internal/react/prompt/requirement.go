package prompt

func RequirementAnalysis() string {
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
        "role": "Use primary for the user's Kubernetes object subject; otherwise use scope, related, evidence, owner, or dependent.",
        "source": "Use user_request, previous_context, live_evidence, guide_context, or model_inference"
      }
    ],
    "operational_focus": {
      "summary": "Optional operational problem focus for this request; omit when the request has no separate operational focus",
      "relationship_to_primary": "Use same_primary, related_to_primary, new_primary, or unclear",
      "changed_from_previous": false,
      "reason": "Why this focus is relevant to the current request",
      "related_resource_hints": [
        {
          "kind": "Related Kubernetes kind suggested by the user, prior context, live evidence, or guide context",
          "name": "Related object name when known",
          "namespace": "Related object namespace when known",
          "role": "Use suspected_related, suspected_blocker, evidence_source, owner, dependent, or related",
          "source": "Use user_request, previous_context, live_evidence, guide_context, or model_inference",
          "evidence": "Short grounding for this hint"
        }
      ],
      "evidence_needs": ["Facts needed to confirm this operational focus"]
    },
    "evidence_needs": ["Facts or live evidence needed before deciding the next action"],
    "constraints": ["Constraints from the user or runtime context"],
    "ambiguities": ["Ambiguities that matter for the next action"]
  }
}

Rules:
- Use only target.category and resource_candidates for target classification.
- Use operational_focus to capture the user's operational problem focus without changing the primary target. It is not a RAG request and must not contain resource_guide_lookup instructions.
- If a prior conversation state is present and the new user request is a follow-up without an explicit resource, name, or namespace, use the prior accepted requirement_analysis/request_context as defaults and set resource_candidates.primary.source="previous_context". Explicit target or scope in the new request always wins and should use source="user_request".
- Treat "this cluster" / "이 클러스터" as an anaphoric reference, not as a keyword. If prior accepted context contains a named Kubernetes Cluster object and the new request is a follow-up, bind the phrase to that previous Cluster object with resource_candidates.primary.source="previous_context". If there is no prior named Cluster object and the request asks about the connected Kubernetes environment, classify target.category="cluster_environment" with no resource_candidates. If both are plausible, record the ambiguity instead of inventing a resource.
- Treat "current cluster" / "현재 클러스터" / "connected cluster" as the connected Kubernetes environment when the wording refers to the active kubeconfig/context or overall environment health. If the wording is a follow-up to a named Cluster object, prefer the previous accepted context unless the user explicitly switches to the connected environment.
- Do not create a resource candidate just because the word "cluster" appears. Create kind="cluster" only when the user identifies a Kubernetes Cluster object by kind/name, a prior accepted Cluster object is being referenced by follow-up wording, or the request clearly targets that Kubernetes object.
- Namespace phrases are normally scope, not the target resource, unless the user explicitly asks about a Namespace object.
- When a value is explicitly introduced by namespace wording such as "namespace", "네임스페이스", or "네임스페이스의", consider that value as a namespace scope candidate before treating it as an object ID, target ID, or object name. If the same request also contains a separate quoted or name-like object and a resource kind, consider that separate object/kind as the primary resource candidate unless the user explicitly says the namespace-marked value is the object ID. If the wording still leaves multiple plausible readings, record the ambiguity rather than forcing one.
- Put only a real namespace value in scope.namespace and resource_candidates[].namespace. If the namespace is unknown or unresolved, leave namespace empty, set scope.type="unknown" when appropriate, and record the ambiguity. Do not write placeholders such as "undefined", "namespace of the object", or "can be inferred from context" into namespace fields.
- If a namespaced object kind and object name are known but the namespace is unknown, plan a context_resolution step that locates the object across namespaces with kubectl get <kind> -A --field-selector metadata.name=<name> before the exact namespaced observation. Do not use kubectl get <kind> <name> -A.
- For an explicitly named Kubernetes resource diagnosis, the initial evidence need is the primary object's API state: metadata, spec, status, and conditions. Do not add node status, related resources, events, or logs as initial evidence needs before the primary object has been observed.
- After the primary object is observed, runtime discovery can classify whether the resource is CRD-backed. Use the later phase/discovery context to decide whether related resources, events, or guidance lookup are needed.
- For Kubernetes resource observation, do not include logs as a default evidence need. Use logs only when the user explicitly asks for logs/log analysis, or when prior live evidence/guide context identifies a concrete log-bearing Pod, container, or controller as the diagnostic target.
- "node group" / "노드 그룹" is a natural operations term that can mean a worker group. In a Cluster API context it may relate to MachineDeployment, MachineSet, Machine, or worker-group behavior. Do not hard-map the phrase to one kind; use prior context and live evidence to decide whether it is a related problem focus or a concrete resource target.
- If a node-group follow-up has prior Cluster/CAPI context, keep the previous target/scope as resource_candidates.primary.source="previous_context" and set operational_focus.relationship_to_primary="related_to_primary". Put MachineDeployment/MachineSet/Machine as operational_focus.related_resource_hints with source="model_inference" unless the user explicitly names one as the target. If there is no useful prior context and no namespace, cluster, resource, or name is identifiable, return target.category="unknown" with ambiguities instead of inventing a resource kind.
- If no concrete Kubernetes resource kind is identifiable, leave resource_candidates empty and capture the ambiguity instead of inventing "unknown" as a resource kind.`
}

func RequirementAnalysisDefinitions() string {
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

natural-language operations terms:
- node group / 노드 그룹: a worker group concept. In Cluster API contexts it can involve MachineDeployment, MachineSet, Machine, or worker lifecycle evidence. Treat it as a semantic clue, not a fixed runtime alias.
- worker group: same operational family as node group; use prior context, named resources, labels, and live evidence to decide the concrete Kubernetes resources.

operational_focus:
- summary: concise operational problem focus, such as worker group availability, node provisioning, scheduling pressure, network reachability, or certificate expiry.
- relationship_to_primary: same_primary when the focus is the primary target itself; related_to_primary when the focus narrows diagnosis to related/dependent behavior while keeping the primary target; new_primary when the user explicitly names a new target; unclear when the relationship cannot be determined.
- changed_from_previous: true when a follow-up shifts the focus from the previous accepted request context.
- reason: short grounding for why this focus matches the user request and prior context.
- related_resource_hints: related objects or kinds that may help diagnosis. Hints do not replace resource_candidates.primary.
- evidence_needs: live facts needed to confirm or reject this focus.

operational_focus.related_resource_hints.role:
- suspected_related: likely related to the operational focus but not established as a blocker.
- suspected_blocker: likely blocking the primary target's health or availability.
- evidence_source: useful source of evidence for the operational focus.
- owner: owner/controller of the focused object or behavior.
- dependent: child/dependent object of the primary target.
- related: generally related when a more specific role is not clear.

operational_focus.related_resource_hints.source:
- user_request: stated directly by the user.
- previous_context: carried from previous accepted requirement_analysis, request_context, or diagnosis summary.
- live_evidence: observed from tool output.
- guide_context: suggested by injected resource-guide context.
- model_inference: inferred by the model from Kubernetes/CAPI domain knowledge.

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
- dependent: dependent/child object of the primary target.

resource_candidates.source:
- user_request: the user explicitly named this resource kind/object in the current request.
- previous_context: carried from the previous accepted requirement_analysis or request_context.
- live_evidence: observed in current or previous tool output.
- guide_context: suggested by injected resource-guide context.
- model_inference: inferred by the model from Kubernetes/CAPI domain knowledge. Do not use model_inference as primary when relationship_to_primary is related_to_primary; put it in operational_focus.related_resource_hints instead.`
}
