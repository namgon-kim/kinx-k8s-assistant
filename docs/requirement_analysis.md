# Requirement Analysis Contract

`requirement_analysis` is the first response contract for each user request. It classifies the natural-language request before the ReAct loop chooses a tool, asks for resource guidance, or returns a final answer.

After acceptance, the analysis is re-emitted on every subsequent iteration as the `requirement_analysis` anchor so the model keeps serving the originally determined request. See [`request_processing_phases.md`](./request_processing_phases.md) for the default natural-language request pipeline before guide lookup, and [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md) for the iteration anchor, guide-step tracking, and `final_report` / `next_directions` continuation flow.

The value tables below define preferred stable values. Natural-language requests can be broader than the table, so `target.category` and `scope.type` are not hard-blocked by runtime enum validation. When no preferred value fits, use `other` and explain the nuance in `target.description`, `evidence_needs`, or `ambiguities`.

## JSON Shape

```json
{
  "requirement_analysis": {
    "request_type": "diagnosis | remediation | inspection | lookup | summary | explanation | generation | configuration | mutation | comparison | operation | other",
    "action": "snake_case verb phrase for the concrete requested action",
    "target": {
      "category": "preferred target category or other",
      "name": "target name only when the request names the target itself",
      "description": "concise natural-language target description"
    },
    "scope": {
      "type": "preferred scope type or other",
      "namespace": "namespace only when provided or clearly implied"
    },
    "resource_candidates": [
      {
        "kind": "concrete Kubernetes resource kind only when named or clearly implied",
        "name": "object name when provided",
        "namespace": "object namespace when provided",
        "role": "primary | scope | related | evidence | owner | dependent",
        "source": "user_request | previous_context | live_evidence | guide_context | model_inference"
      }
    ],
    "operational_focus": {
      "summary": "optional operational problem focus for the current request",
      "relationship_to_primary": "same_primary | related_to_primary | new_primary | unclear",
      "changed_from_previous": false,
      "reason": "short grounding for this focus",
      "related_resource_hints": [
        {
          "kind": "related Kubernetes kind when known",
          "name": "related object name when known",
          "namespace": "related object namespace when known",
          "role": "suspected_related | suspected_blocker | evidence_source | owner | dependent | related",
          "source": "user_request | previous_context | live_evidence | guide_context | model_inference",
          "evidence": "short grounding for this hint"
        }
      ],
      "evidence_needs": ["facts needed to confirm or reject this focus"]
    },
    "evidence_needs": ["facts or live evidence needed before deciding the next action"],
    "constraints": ["constraints from user request or runtime context"],
    "ambiguities": ["ambiguities that matter for the next action"]
  }
}
```

## request_type

| Value | Meaning |
|---|---|
| `diagnosis` | Find cause, health, failure, or problem state. |
| `remediation` | Solve, repair, recover, or recommend a fix. |
| `inspection` | Inspect current live state or configuration. |
| `lookup` | Find a specific object, field, setting, or fact. |
| `summary` | Summarize observed output, events, logs, or prior steps. |
| `explanation` | Explain a concept, command, result, or previous answer. |
| `generation` | Generate manifest, command, script, document, or example. |
| `configuration` | Configure assistant, kubeconfig, context, language, readonly, or runtime settings. |
| `mutation` | Create, update, patch, delete, scale, restart, apply, or otherwise change resources. |
| `comparison` | Compare objects, states, versions, options, or tradeoffs. |
| `operation` | Perform a concrete operational workflow not mainly diagnosis/remediation. |
| `other` | No stable type fits; explain in `target.description` or `ambiguities`. |

## target.category

| Value | Meaning |
|---|---|
| `cluster_environment` | Connected Kubernetes cluster as an environment, not a Kubernetes `Cluster` object. |
| `namespace_scope` | Namespace as a scope or operating boundary. |
| `kubernetes_resource` | Concrete Kubernetes object kind/name is the subject. |
| `workload` | Pod, Deployment, StatefulSet, DaemonSet, Job, CronJob, or workload behavior. |
| `node` | Node capacity, readiness, pressure, labels, taints, or scheduling surface. |
| `control_plane` | API server, scheduler, controller-manager, etcd, admission, or control-plane behavior. |
| `network` | Service, Ingress, DNS, CNI, NetworkPolicy, connectivity, routing, or ports. |
| `storage` | PVC, PV, StorageClass, CSI, volume attachment, mount, or disk behavior. |
| `security_policy` | RBAC, PodSecurity, policy, admission, secret, certificate, or compliance. |
| `access_control` | Kubeconfig, context, auth, impersonation, permissions, or identity. |
| `scheduling` | Placement, affinity, tolerations, taints, quotas, resources, or scheduler decisions. |
| `logs` | Logs are the primary target. |
| `events` | Kubernetes events are the primary target. |
| `metrics` | Metrics, usage, top, resource consumption, or time-series evidence. |
| `configuration` | Assistant/runtime/application/Kubernetes configuration. |
| `manifest` | YAML/JSON manifest content or generation. |
| `local_file` | Local files, documents, scripts, or repo artifacts. |
| `external_system` | Cloud provider, registry, load balancer, DNS provider, or non-Kubernetes system. |
| `conversation` | Meta question about the assistant's previous answer or behavior. |
| `unknown` | Target is unclear and clarification may be needed. |
| `other` | Target is clear but no listed category fits. |

## scope.type

| Value | Meaning |
|---|---|
| `namespaced` | One namespace scopes the target. |
| `cluster_scoped` | Cluster-wide or cluster-scoped object. |
| `all_namespaces` | User explicitly asks for all namespaces. |
| `cross_namespace` | Multiple specific namespaces. |
| `external` | Outside Kubernetes. |
| `local` | Local file/repository/workspace. |
| `unknown` | Scope is unclear. |
| `other` | Scope is clear but no listed type fits. |

Namespace phrasing rules:

- When a value is explicitly introduced by namespace wording such as `namespace`, `네임스페이스`, or `네임스페이스의`, consider it as namespace scope before treating it as an object ID or target name.
- If the same request also contains a separate quoted or name-like object and a resource kind, consider that separate object/kind as the primary resource candidate unless the user explicitly says the namespace-marked value is the object ID.
- A UUID-shaped namespace candidate is not automatically an object ID. Use the user's wording and record ambiguity when multiple readings remain plausible.

## resource_candidates.role

| Value | Meaning |
|---|---|
| `primary` | Kubernetes object the user is asking about. |
| `scope` | Kubernetes object used only as scope. |
| `related` | Related object that may help diagnose the primary target. |
| `evidence` | Object or kind needed only to gather evidence. |
| `owner` | Owner/controller of the primary target. |
| `dependent` | Dependent/child object of the primary target. |

## resource_candidates.source

`resource_candidates.source` explains why a Kubernetes resource candidate is present. It is especially important for `role=primary`, because runtime must distinguish a user-declared target from a model-inferred related resource.

| Value | Meaning | Runtime Behavior |
|---|---|---|
| `user_request` | The user explicitly named this resource kind/object in the current request. | Treated as the current request's primary target when `role=primary`. Previous context does not override it. |
| `previous_context` | Carried from the previous accepted `requirement_analysis` or `request_context`. | Runtime may fill missing name/namespace from previous `request_context`. |
| `live_evidence` | Observed in current or previous tool output. | Preserved as evidence. It does not override an explicit `user_request` primary. |
| `guide_context` | Suggested by injected resource-guide context. | Preserved as guide-supported context, not as a user-declared target. |
| `model_inference` | Inferred by the model from Kubernetes/CAPI domain knowledge. | Must not become `primary` when `operational_focus.relationship_to_primary=related_to_primary`; runtime keeps the previous primary and treats this as a related hint. |

## operational_focus

`operational_focus` captures the current operational problem angle without replacing `resource_candidates.primary`. It is optional and can appear on any request, including the first request and follow-up requests.

The field is not a RAG execution request. Guide lookup is selected by the model's later processing phase, not directly by `operational_focus`. Runtime may confirm CRD/resource-family eligibility after observation before allowing that guide phase to execute.

| Field | Model Guidance | Runtime Behavior |
|---|---|---|
| `summary` | Concise operational issue such as worker group availability, node provisioning, DNS reachability, scheduling pressure, or certificate expiry. | Preserved in the accepted analysis anchor. Runtime does not validate or map the natural-language value. |
| `relationship_to_primary` | Use `same_primary` when the focus is the primary target itself; `related_to_primary` when the focus narrows diagnosis to related/dependent behavior; `new_primary` when the user explicitly names a new target; `unclear` when the relationship cannot be determined. | `same_primary` or `related_to_primary` can allow previous `request_context` defaults when no new primary resource is declared. `new_primary` prevents prior-target defaulting. |
| `changed_from_previous` | `true` when a follow-up shifts the diagnostic angle from prior conversation context. | Preserved as focus metadata. Runtime target inheritance is controlled by `relationship_to_primary` plus `resource_candidates.primary.source`, not by this field alone. |
| `reason` | Short grounding for why this focus matches the current request and prior context. | Preserved for the next ReAct prompt. Runtime does not parse it for keywords. |
| `related_resource_hints` | Related kinds or objects suggested by the user, previous context, live evidence, guide context, or domain inference. | Preserved as hints only. Hints do not replace `resource_candidates.primary` and do not directly become kubectl targets. |
| `evidence_needs` | Live facts needed to confirm or reject this focus. | Preserved in the accepted analysis anchor so the next ReAct step can choose a lookup, action, or final answer. |

### related_resource_hints.role

| Value | Meaning |
|---|---|
| `suspected_related` | Likely related to the operational focus, but not established as a blocker. |
| `suspected_blocker` | Likely blocking primary target health or availability. |
| `evidence_source` | Useful source of evidence for the focus. |
| `owner` | Owner/controller of the focused object or behavior. |
| `dependent` | Child/dependent object of the primary target. |
| `related` | Related when a more specific role is not clear. |

### related_resource_hints.source

| Value | Meaning |
|---|---|
| `user_request` | Stated directly by the user. |
| `previous_context` | Carried from previous accepted `requirement_analysis`, `request_context`, or diagnosis summary. |
| `live_evidence` | Observed from tool output. |
| `guide_context` | Suggested by injected resource-guide context. |
| `model_inference` | Inferred by the model from Kubernetes/CAPI domain knowledge. |

## Runtime Rules

- Before each new query, the runtime snapshots the previous accepted `requirement_analysis`, derived `request_context`, original query, and compact diagnosis summary into explicit conversation memory fields such as `previous_requirement_analysis` and `previous_request_context`.
- For follow-up requests, the runtime injects that previous conversation context before the requirement-analysis prompt. If the new request does not explicitly name a resource, object, or namespace, the model should use the previous accepted `requirement_analysis` / `request_context` as defaults.
- Explicit resource, object name, namespace, or all-namespaces scope in the new request always overrides prior conversation state.
- Normal follow-up target defaulting is based on `operational_focus.relationship_to_primary` and `resource_candidates.primary.source`. A compatibility retry branch still uses query keywords; this is a known gap rather than part of the contract.
- `resource_candidates.primary.source=user_request` means the current request explicitly changed or named the primary target.
- `resource_candidates.primary.source=previous_context` means runtime may fill missing target name or namespace from the previous request context.
- `resource_candidates.primary.source=model_inference` with `operational_focus.relationship_to_primary=related_to_primary` is not accepted as a target switch; runtime keeps the previous primary and moves the inferred resource into `operational_focus.related_resource_hints`.
- Namespace fields must contain only real namespace values. If a namespace is unknown, leave the namespace field empty, use `scope.type=unknown` when appropriate, and record the ambiguity. Do not put placeholders such as `undefined`, `namespace of the object`, or `can be inferred from context` in namespace fields.
- If a namespaced kind and object name are known but the namespace is unknown, the later phase plan should include context resolution with `kubectl get <kind> -A --field-selector metadata.name=<name>` to locate the object before exact namespaced observation. Do not use positional object-name lookup with `-A`.
- Phrases such as "this cluster" / "이 클러스터" are anaphoric references. If prior accepted context contains a named Kubernetes `Cluster` object and the new request is a follow-up, keep that previous object as `resource_candidates.primary` with `source=previous_context`. If no prior named object exists and the wording refers to the active kubeconfig/context or overall environment health, classify as `target.category=cluster_environment` with empty `resource_candidates`. If both meanings are plausible, record the ambiguity instead of inventing a target.
- For an explicitly named Kubernetes resource diagnosis, initial `evidence_needs` should be limited to the primary object's API state: metadata, spec, status, and conditions.
- Do not add node status, related resources, events, or logs as initial evidence needs before the primary object has been observed. After the primary object observation, runtime discovery can classify CRD eligibility and the later phase can decide whether related resources, events, or guide lookup are needed.
- Kubernetes resource observation does not include logs by default.
- Use logs only when the user explicitly asks for logs/log analysis, or when prior live evidence or guide context identifies a concrete log-bearing Pod, container, or controller as the diagnostic target.
- `resource_candidates` is the only source for Kubernetes resource context derivation.
- If `resource_candidates` is empty, runtime does not create Kubernetes resource context and does not trigger CRD resource-guide/RAG lookup.
- Runtime uses Kubernetes discovery, not model wording, to confirm whether a relevant resource candidate is built-in or CRD-backed.
- Built-in resources skip CRD resource-guide/RAG lookup.
- CRD resource-guide/RAG lookup runs only when the model-declared phase plan reaches guidance consideration, discovery confirms the relevant resource candidate is a CRD, and the observation phase has collected useful evidence. See [`request_processing_phases.md`](./request_processing_phases.md).
- `operational_focus` does not trigger CRD resource-guide/RAG lookup by itself. It is passed to the next ReAct step as context for the model-declared phase plan and later guidance decision.
- If resource-guide/RAG is unavailable or empty, the assistant continues with ordinary kubectl evidence gathering and model reasoning.
- `unknown` is not a Kubernetes resource kind. Do not use it in `resource_candidates.kind`, `action.target.resource`, or a kubectl resource position.
- `scope.type=all_namespaces` represents all namespaces. Do not store this as `scope.namespace="all"`; `all` is not a real namespace in this contract.

## Natural Language Semantics

Some user terms are operational concepts rather than Kubernetes resource kinds.

| Term | Handling |
|---|---|
| `node group`, `노드 그룹`, `worker group` | Treat as a worker-group concept. In Cluster API context it can involve MachineDeployment, MachineSet, Machine, or worker lifecycle evidence. Do not hard-map it to one runtime kind. Use `operational_focus` to express it as a related problem focus unless the user explicitly names a concrete primary resource. Inferred MachineDeployment/MachineSet/Machine candidates should use `operational_focus.related_resource_hints.source=model_inference`, not `resource_candidates.primary.source=model_inference`. |
| `this cluster`, `이 클러스터` | Resolve as a follow-up reference first. If the previous accepted primary target was a named Kubernetes `Cluster` object, preserve that object with `source=previous_context`. If there is no prior named object and the request refers to active kubeconfig/context or global environment health, classify as `cluster_environment`. If both are plausible, ask for clarification. |
| `current cluster`, `현재 클러스터`, `connected cluster` | Usually means the connected Kubernetes environment when it refers to the active kubeconfig/context. In a clear follow-up to a named `Cluster` object, previous context still wins unless the user explicitly switches to the connected environment. |
| follow-up references such as "그럼", "왜", "그건" | Use prior accepted context only when the new request omits explicit target/scope, and express the new diagnostic angle in `operational_focus`. |
| "왜 정상동작 하지 않는 걸까", "노드 그룹은?", "그럼 이건?" | Treat as follow-up wording when previous context exists. Do not invent a new resource kind from these phrases; default to the previous target/scope through `operational_focus.relationship_to_primary`. |

If a node-group question has no prior useful context and no namespace, cluster, resource, or object name is identifiable, classify the target as `unknown` and record the missing details in `ambiguities`.

## Current Implementation Notes

The contract is defined in `internal/react/contract/structured.go`. Prompt text lives in
`internal/react/prompt/requirement.go`; request classification, context derivation, and
follow-up defaulting live in `internal/react/flow/request`; mutable request memory lives in
`internal/react/session/context.go`. `internal/react/coordinator/iteration.go` integrates
these rules with model turns and compatibility behavior.

Example: for a follow-up such as "이번에는 모든 namespace에서 확인해줘", the intended
result is to preserve the accepted target while overriding the prior namespace with
`scope.type=all_namespaces`. The coordinator parses the structured requirement and
`session.ContextState` retains the accepted context for later turns. The namespace-value-only
encoding gap described below can still incorrectly restore the previous namespace.

Implemented behavior:

- The first model response for each query must be `requirement_analysis`; actions, answers, phase plans, and guide lookups before it are corrected.
- Previous conversation memory is injected only as compact fields: `previous_original_query`, `previous_requirement_analysis`, `previous_request_context`, and `previous_diagnosis_summary`.
- Previous memory is bounded by field-specific truncation helpers and hashes. Full raw YAML, logs, event lists, and guide bodies are not carried as follow-up memory.
- Follow-up defaulting uses prior `request_context` only when the new analysis is diagnostic/remediation, `operational_focus.relationship_to_primary` is `same_primary` or `related_to_primary`, and no explicit current primary target overrides it.
- `model_inference` primary candidates for related follow-ups are moved into `operational_focus.related_resource_hints` instead of replacing the previous primary target.
- Placeholder namespace/name values such as unknown or explanatory phrases are normalized away; `unknown` is rejected as a Kubernetes resource kind.
- Clarification is emitted for diagnostic/remediation requests when `target.category=unknown`, or when no primary resource is available and the target category is not a broad environment/log/event/metric category.

Known implementation gaps:

- `shouldRetryPreviousRequest` still uses common Korean words such as `다시`, `정확`, and `아닌`; a new conversation-style question can be mistaken for a retry of the previous Kubernetes request (`BUG-23`).
- If all-namespaces intent is encoded only in `scope.namespace` without `scope.type=all_namespaces`, normalization can clear the value and prior-context merging can restore the old namespace (`BUG-24`).

These are tracked in [`../bug.md`](../bug.md). The runtime rules above remain the required
contract, not a claim that both edge cases are already enforced.

Remaining contract details:

- The exact truncation shape is currently `head + truncated hash`, not a structured `{content_head, content_tail, original_len}` object. Adopt a richer shape only if consumers need it.
- The runtime accepts preferred enum-like strings but does not hard-block every unlisted `request_type` or `target.category`; future docs should keep model guidance and runtime rejection rules separate.
- Clarification boundaries are implemented for the high-risk cases above, but the documentation can still add a decision table for edge cases such as namespace-only diagnostic requests or all-namespaces discovery.
