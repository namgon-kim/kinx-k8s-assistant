# Requirement Analysis Contract

`requirement_analysis` is the first response contract for each user request. It classifies the natural-language request before the ReAct loop chooses a tool, asks for resource guidance, or returns a final answer.

After acceptance, the analysis is re-emitted on every subsequent iteration as the `requirement_analysis` anchor so the model keeps serving the originally determined request. See [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md) for the iteration anchor, guide-step tracking, and `final_report` / `next_directions` continuation flow.

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
        "role": "primary | scope | related | evidence | owner | dependent"
      }
    ],
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

## resource_candidates.role

| Value | Meaning |
|---|---|
| `primary` | Kubernetes object the user is asking about. |
| `scope` | Kubernetes object used only as scope. |
| `related` | Related object that may help diagnose the primary target. |
| `evidence` | Object or kind needed only to gather evidence. |
| `owner` | Owner/controller of the primary target. |
| `dependent` | Dependent/child object of the primary target. |

## Runtime Rules

- Broad phrases such as "this cluster" classify as `target.category=cluster_environment` and leave `resource_candidates` empty unless the user names a concrete Kubernetes resource kind/object.
- `resource_candidates` is the only source for Kubernetes resource context derivation.
- If `resource_candidates` is empty, runtime does not create Kubernetes resource context and does not trigger CRD resource-guide/RAG lookup.
- Runtime uses Kubernetes discovery, not model wording, to decide whether a primary resource candidate is built-in or CRD-backed.
- Built-in resources skip CRD resource-guide/RAG lookup.
- CRD resource-guide/RAG lookup runs only after discovery confirms the primary resource candidate is a CRD.
- If resource-guide/RAG is unavailable or empty, the assistant continues with ordinary kubectl evidence gathering and model reasoning.
- `unknown` is not a Kubernetes resource kind. Do not use it in `resource_candidates.kind`, `action.target.resource`, or a kubectl resource position.
