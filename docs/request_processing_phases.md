# Natural-Language Request Processing Phases

This document defines the default processing phases for natural-language requests in the ReAct loop. The purpose is to make the model declare an explicit processing plan before actions, keep RAG/resource-guide lookup as an optional later phase after enough observation, and prevent the runtime from inventing planning decisions from keyword checks.

Related contracts:

- [`requirement_analysis.md`](./requirement_analysis.md): request classification, target/scope extraction, operational focus.
- [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md): behavior after a resource guide has already been injected.

Current code ownership:

- `internal/react/contract/structured.go`, `execution.go`: immutable phase payloads and stable goal/phase/step/criterion contracts;
- `internal/react/flow/phase`: plan start, progress, graph validation, and plan-revision rules;
- `internal/react/session/execution.go`: goal execution, attempt, observation, and correction ledger cloned under the revisioned runtime root;
- `internal/react/coordinator/state.go`: the single revisioned runtime root containing accepted phase and request state;
- `internal/react/flow/guidance` and `internal/react/flow/verification`: nested guide and mutation-verification decisions;
- `internal/react/coordinator/iteration.go`, `execution_state.go`, `revision.go`: protocol consumption, execution-contract projection, plan revision, and model-turn integration.

## Core Principle

RAG is not the primary request-processing phase. The model should first determine what the user is asking, declare the ordered processing phases, define each phase's goal and completion condition, and then advance one phase at a time.

The runtime must not decide the phase sequence from natural-language keywords. Runtime responsibilities are limited to:

- store the accepted phase plan and current phase state;
- re-inject the active phase goal and completion condition on each iteration;
- validate safety, schema shape, tool-call consistency, and read-only policy;
- record observations and pass them back to the model;
- accept the model's phase-completion report when it is structurally valid and not contradicted by blocked/declined/internal-error observations;
- after observation phases, run runtime discovery to confirm whether the relevant resource is CRD-backed before allowing guide lookup.

The model owns:

- selecting the phases needed for the request;
- ordering those phases;
- choosing the current phase's next action;
- reporting when the current phase is complete;
- choosing whether the next phase should be direct response, further observation, or guidance consideration.

In particular:

- Do not run resource-guide/RAG lookup immediately after `requirement_analysis`.
- Do not run resource-guide/RAG lookup only because discovery says the primary resource is a CRD.
- Do not treat the first `kubectl` action as automatically sufficient evidence.
- Do not block useful multi-resource observation just because it is not a single `kubectl -n <namespace> get <kind> <name> -o yaml` command.
- Do not treat logs as ordinary Kubernetes resource observation. Resource observation is API-object evidence: status, conditions, spec, metadata, events, owner/dependent references, and related Kubernetes objects.
- Use logs only for explicit log/log-analysis requests, or after user input, live evidence, or guide context identifies a concrete log-bearing Pod, container, or controller.
- Use RAG only when the accepted phase plan reaches a guidance phase, observed evidence exists, and runtime discovery confirms the relevant guide-supported resource family.

## Current Runtime Strategy

The current implementation uses the phase-owned guide entry path:

1. `requirement_analysis` classifies intent, target, scope, and operational focus.
2. The model emits a `phase_plan` made of ordered `phase_step` entries.
3. Runtime stores the accepted phase plan, projects it to stable goal/phase/step/criterion IDs, fixes the request budget profile, and injects only the active execution anchor on each iteration.
4. The model emits one action or one response for the active step. Runtime binds actions to the single active step. Command tools must declare `risk.risky`; only `risky=true` requires exact human approval. Accepted actions become immutable dispatch intents before execution and their attempts/observations remain under that lineage.
5. The model reports `step_result` with existing observation IDs and criterion results when a non-lightweight step reaches `achieved`, `blocked`, or `replan_required`.
6. Runtime accepts `phase_progress` only after the active phase's execution steps are achieved, then advances to a declared next phase.
7. Runtime discovery classifies the accepted primary resource as built-in, CRD-backed, or unknown and exposes that eligibility to the model.
8. The model decides whether to enter a guidance phase. Runtime does not automatically inject RAG only because a CRD was observed.
9. If guidance is selected and injected, guide diagnostic steps become nested `guidance_step` entries inside the active `guided_diagnosis` phase.
10. If new evidence invalidates the active or remaining graph, the model may emit one standalone `phase_plan_revision`; runtime validates its evidence reference, revision budget, lineage, history, and mandatory obligations before replacement.
11. `final_report` closes the request after either ordinary phase completion or guided diagnosis completion.

This removes the old shortcut:

```text
observation -> runtime detects CRD -> runtime automatically injects RAG
```

and replaces it with:

```text
observation phase complete -> runtime confirms CRD eligibility -> model enters guidance_decision/guidance_lookup phase if useful
```

## Phase Plan Contract

The model should return a phase plan near the start of the request, after `requirement_analysis` and before ordinary kubectl actions. The runtime transport is a top-level internal structured call named `__phase_plan__`; shim mode accepts the equivalent top-level `phase_plan` object and normalizes it to that internal call.

```json
{
  "phase_plan": {
    "request_goal": "what the user ultimately wants answered or done",
    "current_phase_index": 1,
    "phase_steps": [
      {
        "index": 1,
        "name": "observation_planning",
        "goal": "decide the minimum live evidence needed before diagnosis",
        "completion_condition": "next observation action is selected and grounded in request_context",
        "allowed_next": ["observation_execution", "clarification"]
      },
      {
        "index": 2,
        "name": "observation_execution",
        "goal": "collect live state for the active target or related operational focus",
        "completion_condition": "a useful observation is returned or a blocking condition is reported",
        "allowed_next": ["observation_completion"]
      }
    ]
  }
}
```

`phases` is allowed as a backward-compatible field name while the schema is being introduced, but the stable contract should use `phase_steps`. A `phase_step` is the top-level unit of request processing.

`allowed_next` is a closed graph over declared `phase_step` names. Every name in `allowed_next` must appear as a `phase_steps[].name` in the same `phase_plan`. There are no implicit or virtual phase steps: if `guidance_lookup`, `guided_diagnosis`, `response_synthesis`, or `final_report` can be used as a next phase, it must be declared as an explicit `phase_step` first. Any non-terminal `phase_step` with a later step in the plan must declare at least one `allowed_next`; only terminal steps may leave it empty.

The runtime stores this plan compactly. Each iteration should include only:

- request goal;
- current `phase_step` index/name;
- current `phase_step` goal;
- current `phase_step` completion condition;
- completed `phase_step` indices;
- valid next `phase_step` names.

The accepted plan is also projected to a request-local execution contract:

- stable goal, phase, step, criterion, and lineage IDs are assigned by runtime;
- exactly one execution step is active for ordinary actions;
- the request-fixed budget profile supplies step, correction, verification-evidence, mutation-continuation, plan-revision, max-iteration, and closure-reserve limits;
- every execution-approved action is first committed as an immutable dispatch intent and attempt; every reconciled result creates an observation record;
- prompt anchors include only bounded recent attempts, linked observations, criterion outcomes, and recent prior-phase outcomes while the in-memory ledger remains complete.

The model does not invent bookkeeping IDs for actions. Runtime derives the active step and may
treat a model-supplied step reference only as an assertion. A conflicting assertion is rejected.
Before closing a non-lightweight step, the model returns a standalone `step_result` using IDs from
the execution anchor and evidence references that already exist in the observation ledger.

## Plan Revision Contract

Plan revision is for evidence-driven changes to the active and remaining workflow, such as changing
from a workload hypothesis to a node-level diagnosis after live observations invalidate the
original path. It is not an unrestricted jump or an action bundle.

A `phase_plan_revision` must:

- be the only semantic output in that model turn;
- reference the current base revision and existing observation IDs;
- replace the complete active and remaining nonterminal graph;
- preserve completed/skipped/superseded history and mandatory guide/mutation-verification obligations;
- preserve phase and step lineage for replacement work so renaming IDs cannot reset attempt budgets;
- stay within the request-fixed plan revision budget.

Runtime validates structure and procedure, not whether the model's diagnosis is factually correct.
The model decides whether evidence justifies a new hypothesis and states the revised goals and
completion criteria. The runtime checks that the cited evidence exists and that the transition is
legal, then applies the accepted revision atomically.

The model must report phase completion explicitly, for example:

```json
{
  "phase_progress": {
    "phase_completed": 2,
    "evidence_useful": true,
    "completion_reason": "Cluster status and conditions were observed from the live API",
    "next_phase": "observation_completion"
  }
}
```

If the active phase has `allowed_next`, `phase_progress.next_phase` must be one of those declared phase names. If `next_phase` is omitted, runtime advances to the first declared allowed next phase. Runtime must not fall through to an undeclared later phase. If the active phase has no `allowed_next`, it is terminal and `next_phase` should be omitted.

Runtime should not infer a phase as complete just because one command ran. It may reject or ignore completion for blocked, declined, malformed, or internal-error observations.

Ordinary phase activation treats `blocked` as a terminal step status and selects the next pending
step instead of silently reviving blocked work. Only an explicit rewind transition may reset a
blocked step to `pending`; that transition also clears the rewound criteria before activation.
Phase move/rewind is rejected while a stable owner ID still has a mandatory mutation-verification
obligation. Display names such as `mutation` or `verification` are not used to infer cleanup scope.

## `phase_step` and `guidance_step` Hierarchy

`phase_step` is the top-level request-processing step. `guidance_step` is a lower-level step that exists only after a guide has been injected and only while the active phase is `guided_diagnosis`.

```text
request
└─ phase_plan
   ├─ phase_step 1: context_resolution
   ├─ phase_step 2: observation_planning
   ├─ phase_step 3: observation_execution
   ├─ phase_step 4: observation_completion
   ├─ phase_step 5: guidance_decision
   ├─ phase_step 6: guidance_lookup
   ├─ phase_step 7: guided_diagnosis
   │  ├─ guidance_step 1: inspect top-level CRD resources
   │  ├─ guidance_step 2: inspect related secrets/config
   │  └─ guidance_step 3: inspect worker-group resources
   └─ phase_step 8: final_report
```

Rules:

- A `guidance_step` must not exist before a guide is injected.
- A `guidance_step` is not a `phase_step` and must not be listed in `phase_plan` or `allowed_next`.
- `guided_diagnosis` is the parent `phase_step` that owns injected `guidance_step` entries.
- `guide_progress` must not be used to complete ordinary `phase_step` entries.
- `phase_progress` completes top-level `phase_step` entries.
- `guide_progress` completes nested `guidance_step` entries within `guided_diagnosis`.
- When all `guidance_step` entries are complete, the model should complete the parent `guided_diagnosis` phase with `phase_progress` and move to `final_report`.
- Runtime stores parent phase state separately from nested guide progress; nested guide state is scoped to the current `guided_diagnosis` `phase_step`.

Example after guide injection:

```json
{
  "phase_progress": {
    "phase_completed": 6,
    "evidence_useful": true,
    "completion_reason": "A matching resource guide was injected for the CRD-backed cluster problem.",
    "next_phase": "guided_diagnosis"
  }
}
```

Then guided actions report nested guide progress:

```json
{
  "action": {
    "name": "kubectl",
    "command": "kubectl -n tenant-a get cluster/name openstackcluster/name kamajicontrolplane/name -o yaml",
    "guide_progress": {
      "step_completed": 1,
      "evidence_useful": true
    }
  }
}
```

After nested guide steps are done:

```json
{
  "phase_progress": {
    "phase_completed": 7,
    "evidence_useful": true,
    "completion_reason": "The active guide's diagnostic steps were completed or made redundant by live evidence.",
    "next_phase": "final_report"
  }
}
```

## Guidance Step Classification Under Phase Steps

Guidance-related work must be classified under the top-level phase workflow.

| Top-Level `phase_step` | Guidance-Related Meaning | Allowed Nested `guidance_step`? | Notes |
|---|---|---|---|
| `requirement_analysis` | Classify request only. | No | Must not trigger RAG or guide search. |
| `context_resolution` | Resolve previous target/scope/focus. | No | May prepare evidence needs for later phases. |
| `observation_planning` | Plan the next observation needed before guide eligibility. | No | May mention that guide may be considered later, but no guide lookup yet. |
| `observation_execution` | Execute live observation. | No | Raw observation is stored as phase evidence. |
| `observation_completion` | Model reports whether observation goals are complete. | No | Runtime may run CRD discovery after this phase if relevant. |
| `guidance_decision` | Model decides whether guide lookup is useful. Runtime only confirms eligibility. | No | The output may be direct response, further observation, or `guidance_lookup`. |
| `guidance_lookup` | Perform resource/incident/remediation guide search and inject context. | No | For CRD-backed primary targets, this phase must emit `resource_guide_lookup` before kubectl actions or `phase_progress`. It produces guide context or records lookup unavailability, but has no diagnostic guide steps yet. |
| `guided_diagnosis` | Execute injected guide diagnostic procedure. | Yes | Each guide diagnostic instruction is a nested `guidance_step`; progress is tracked with `guide_progress`. |
| `response_synthesis` | Answer directly from gathered evidence. | No | Used when guide is unnecessary or skipped. |
| `final_report` | Close the request. | No | May summarize completed phase steps and nested guide steps. |

Example classification:

```text
phase_step 1: requirement_analysis
phase_step 2: observation_planning
phase_step 3: observation_execution
phase_step 4: observation_completion
phase_step 5: guidance_decision
phase_step 6: guidance_lookup
phase_step 7: guided_diagnosis
  guidance_step 1: inspect top-level Cluster/CAPI resources
  guidance_step 2: inspect worker group resources
  guidance_step 3: inspect events or related API objects tied to the blocker
phase_step 8: final_report
```

The important distinction is that `guidance_step` is procedural content from a retrieved guide. It does not replace the assistant's own request-processing phases.

## Implementation Status

The phase-owned guidance flow is implemented in this order:

1. `phase_plan` and `phase_progress` are accepted by shim parsing and function-call conversion.
2. Runtime stores the accepted `phase_plan`, completed phase indices, active phase, and stable goal execution contract.
3. Runtime injects bounded phase/step/attempt/observation anchors on every iteration after requirement analysis is accepted.
4. Native and shim outputs use the same typed output policy before domain consumers run.
5. Prompts require `phase_plan` before ordinary actions, `step_result` for explicit step closure, and `phase_progress` only after the phase's execution steps are achieved.
6. Evidence-backed `phase_plan_revision` can atomically replace the active and remaining graph while preserving lineage and request-fixed budgets.
7. CRD discovery is exposed as eligibility context for the next model turn, not as automatic guide injection.
8. Runtime-driven initial guide injection paths are disabled.
9. Top-level `resource_guide_lookup` is allowed only as the model-selected action inside the `guidance_lookup` phase.
10. Nested guide progress is scoped under `guided_diagnosis`, and nested guide completion requires parent `phase_progress`.
11. Final-report prompting distinguishes completed `phase_step` entries and nested `guidance_step` entries conceptually.
12. Tool invocation uses a committed dispatch intent and canonical payload hash; reconciliation failure cannot recreate the same mutation as an unapproved pending action.
13. RBAC observations remain in history as access blockers and cannot satisfy or keep waiting a mutation verification. A general RBAC failure returns to the current phase for permitted alternative evidence; when an RBAC change is genuinely required, the model must propose a separate risky mutating action that receives exact user approval and direct verification.
14. Max-iteration closure produces a validated `continuation_handoff`; resuming retains request lineage, ledger, and safety counters.
15. A state-bearing `failed` verification closes that attempt and permits a materially different action or evidence-backed plan revision. Only unknown execution/verification outcomes remain mandatory unresolved obligations that force an inconclusive report. An active verification blocks plan replacement; an already-closed unknown obligation stays outside the replaceable plan graph, so a revision may change the remaining strategy but cannot remove that obligation or make the final report conclusive.

Remaining hardening should focus on stricter semantic validation of phase completion, not on adding another guide-entry path.

## Phase Overview

| Phase | Purpose | Typical Output |
|---|---|---|
| `requirement_analysis` | Classify the user request before choosing tools or guidance. | Structured intent, target candidates, scope, operational focus, ambiguities. |
| `context_resolution` | Apply previous conversation context and resolve follow-up references. | Updated target/scope defaults or clarification need. |
| `clarification` | Ask the user when target/scope/action cannot be determined safely. | User-facing question, no kubectl action. |
| `safety_policy` | Enforce read-only mode, risk-flagged command approval, and unsafe command rejection. | Allowed action, approval request, or blocked action. |
| `observation_planning` | Model declares what evidence must be collected before answering or guide lookup. | One next kubectl/tool diagnostic action. |
| `observation_execution` | Execute the approved observation action. | Raw tool observation. |
| `observation_completion` | Model reports whether enough observation exists for the request's next step. | Continue observation, synthesize answer, or consider guidance. |
| `response_synthesis` | Answer directly from observation or prior context when guidance is unnecessary. | User-facing summary, explanation, lookup result, or final report. |
| `guidance_decision` | Model decides whether resource/incident/remediation guide lookup is warranted after observation. Runtime confirms guide eligibility. | Skip guidance, continue observation, answer directly, or advance to `guidance_lookup` with `phase_progress`. |
| `guidance_lookup` | Search and inject guide context. | Resource-guide observation or unavailable-guide observation. |
| `guided_diagnosis` | Follow injected guide steps as nested `guidance_step` entries under this `phase_step`. | Further observations, `guide_progress`, then parent `phase_progress`. |
| `final_report` | Close the request with conclusion, evidence, gaps, and next actions. | Final answer or user continuation options. |

The exact path depends on request type. Not every request needs every phase.

## Request-Type Routes

| Request Type | Default Route |
|---|---|
| Simple lookup | `requirement_analysis -> observation_planning -> observation_execution -> response_synthesis` |
| Status inspection | `requirement_analysis -> observation_planning -> observation_execution -> observation_completion -> response_synthesis` |
| Summary of supplied or observed data | `requirement_analysis -> observation_completion -> response_synthesis` |
| Diagnosis | `requirement_analysis -> context_resolution -> observation_planning -> observation_execution -> observation_completion -> guidance_decision(optional) -> final_report` |
| Follow-up diagnosis | `requirement_analysis -> context_resolution -> observation_planning -> observation_execution -> observation_completion -> guidance_decision(optional) -> final_report` |
| Explanation | `requirement_analysis -> response_synthesis`, unless live state is required. |
| Mutation/remediation | `requirement_analysis -> safety_policy -> approval(if risk.risky=true) -> execution -> verification_observation -> mutation_verification_result -> phase_progress/final_report` |
| Configuration/meta request | Runtime-specific handler when possible; otherwise `requirement_analysis -> response_synthesis`. |

## Observation Roles

Observation is a first-class phase before RAG. The model declares the intended observation role in the phase/action context. Runtime can validate obvious contradictions, but should not replace the model's role classification with hard-coded keyword rules.

| Observation Role | Meaning | Examples | Completes Diagnosis Observation? |
|---|---|---|---|
| `target_resolution` | Finds namespace/name/kind candidates when the user omitted them or used a follow-up phrase. | `kubectl get cluster -A`, `kubectl get machinedeployment -A` | No, unless the user only asked to find the object. |
| `primary_status` | Reads current state of the accepted primary target. | `kubectl -n ns get cluster name -o yaml`, `kubectl describe pod pod-a -n ns` | Usually yes. |
| `related_resource_status` | Reads related resources needed to understand the primary target or operational focus. | `kubectl -n ns get cluster/name openstackcluster/name kamajicontrolplane/name -o yaml`, `kubectl -n ns get machinedeployment,machineset,machine -l cluster.x-k8s.io/cluster-name=name -o yaml` | Yes when related to the active target/focus. |
| `event_observation` | Reads Kubernetes events as supporting resource evidence. | `kubectl -n ns get events --field-selector=...` | Yes when it adds concrete failure evidence. |
| `log_observation` | Reads logs only when logs are the explicit user target or a concrete log-bearing Pod/container/controller has already been identified. | `kubectl logs <pod> -n ns -c <container>` | No for ordinary resource observation; yes only for explicit log analysis or identified log-bearing targets. |
| `broad_scan` | Scans a wide scope for clues or inventory. | `kubectl get pods -A`, `kubectl get events -A` | Usually no by itself; can become useful evidence if concrete signals are extracted. |
| `verification_observation` | Checks whether a suspected condition still holds after prior evidence or remediation. | `kubectl -n ns get machine md-... -o yaml`, `kubectl rollout status ...` | Yes for verification-focused requests. |

## Mutation Verification Contract

Mutation/remediation requests have an additional runtime contract after successful execution. Approval only authorizes the change; it does not prove the user's goal was achieved.

Every mutating action declares one concrete action target and a direct verification contract. Runtime does not infer a missing mutation resource or name from the command. It binds the verification contract to the mutation attempt and requires read-only evidence before the mutation step can close. An ordered-chain check inherits the action target when its own target is omitted and uses a concrete check target only as an explicit override.

The contract is:

1. The mutating action runs through normal read-only and approval gates.
2. Runtime opens one verification ID under the same step attempt. A single verification has one check; an ordered chain has two or more distinct checks with only one active at a time.
3. The model issues one read-only observation for the active verification check.
4. Runtime asks for exactly one `mutation_verification_result` with the active verification ID and evidence references including the latest observation for that check.
5. `status=satisfied` completes the check and activates the next chain check or closes the direct mutation verification.
6. `status=waiting` is valid for state convergence and rechecks the same verification ID up to five times without consuming another step attempt.
7. `status=failed` requires a materially different safe strategy or an evidence-grounded plan revision; runtime does not repeat the mutation automatically.

One step attempt contains at most one mutating primary action. Mutation, direct verification, ordered chain checks, and temporal rechecks belong to that attempt. The initial phase plan declares the complete expected procedure. When the resource changed by the mutation differs from the original user-visible target, direct mutation verification and original-outcome verification are consecutive independent plan steps rather than checks in one mutation-verification chain. If live evidence requires an undeclared outcome step or a different route, the model emits a standalone evidence-backed `phase_plan_revision` before acting on that route.

Single checks use the `awaiting_mutation_verification_evidence/result` control pair. Ordered chains use
`awaiting_mutation_verification_chain_evidence/result`; only the active chain index is exposed as an executable
obligation, and a satisfied result advances to the next index.

Generic multi-resource mutations without one concrete `action.target.resource` and `action.target.name` are rejected before execution. The model must declare one honest primary mutation target; when a command cannot be represented by one such target, it must be split or replaced with a more specific action. Ordered checks may override their inherited target only for another concrete direct condition.

## Observation Completion

`observation_completion` is model-reported. The model decides whether the loop has enough evidence to answer, continue observing, or consider guide lookup, and must ground that decision in the latest observation.

The phase completion report should distinguish at least these booleans:

| Field | Meaning |
|---|---|
| `target_resolved` | The concrete target/scope is known well enough for the request. |
| `primary_observed` | The accepted primary target was observed directly, when a primary target exists. |
| `related_observed` | Related resources relevant to `operational_focus` were observed. |
| `diagnostic_evidence_collected` | The observation contains status, conditions, events, errors, references, or absence signals that advance the user's question. |
| `answer_ready` | The request can be answered without additional guide lookup. |
| `guidance_candidate` | The request may benefit from guide lookup after observation and classification. |

Important cases:

- `kubectl get cluster -A` for a named cluster without namespace is usually `target_resolution`, not completed diagnosis.
- `kubectl -n <namespace> get cluster <name> -o yaml` is usually `primary_status` and can complete the minimum observation because it captures the primary object's metadata, spec, status, and conditions.
- For an explicitly named resource diagnosis, do not inspect nodes, related resources, events, or logs before `primary_status` has observed the primary object. Complete the observation phase after primary status when it is sufficient so runtime can expose CRD discovery/classification before guidance or related-resource diagnosis.
- A multi-resource command can complete observation when the resources are related to the accepted target or operational focus.
- An observation with runtime/internal errors, approval decline, blocked execution, or malformed schema is not diagnostic evidence.
- Kubernetes `NotFound` can be state-bearing evidence when absence directly answers the request.
  `Forbidden`/`Unauthorized` is an access blocker: it proves that inspection was denied, not the
  resource state, and therefore cannot satisfy or keep waiting a mutation verification.
- A forbidden observation is not an automatic permission grant. Do not repeat it unchanged. Prefer
  permitted evidence; if the request requires an RBAC change, propose that change as a separate
  `risk.risky=true` mutation subject to read-only policy, exact approval, and direct verification.

## Guidance Decision

`guidance_decision` runs after model-reported observation completion, not before observation.

Guide lookup is allowed when all of these are true:

- The request intent benefits from procedural guidance, such as diagnosis, remediation planning, or non-trivial operational workflow.
- Runtime discovery, performed after observation when needed, confirms the relevant resource family is CRD-backed or otherwise guide-supported.
- Observed evidence exists and is relevant to the active target/focus.
- The query can include the original request, accepted request context, operational focus, and compact observed evidence.

When the active phase is `guidance_lookup` for a CRD-backed primary target:

- The next model output must be top-level `resource_guide_lookup`.
- Runtime rejects `phase_progress` from `guidance_lookup` until a guide lookup result has been injected or lookup unavailability has been recorded.
- Runtime rejects kubectl actions from `guidance_lookup` before `resource_guide_lookup`.
- `guided_diagnosis` is valid only after guide context exists; otherwise the model should continue to `response_synthesis` or earlier observation phases instead of pretending guided diagnosis started.

Guide lookup should be skipped when:

- The user asked for a simple lookup, summary, explanation, or count and the answer is already available.
- Only target resolution has happened.
- The only signal is an internal runtime/correction/schema error.
- The guide family is only a model guess and has not been supported by discovery or live evidence.
- The matched guide lifecycle does not match the request/evidence, such as deletion/cleanup guides for a general health diagnosis.

## Multi-Resource Requests

The pipeline must support users asking about multiple resources or asking a question that naturally requires multiple resources.

Examples:

| User Intent | Valid Observation |
|---|---|
| "Check why this cluster is unhealthy." | Primary `Cluster` status, then related `OpenStackCluster`, `KamajiControlPlane`, `MachineDeployment`, `Machine`, or events when evidence points there. |
| "Check cluster and control plane status." | Multi-resource `get cluster/name openstackcluster/name kamajicontrolplane/name -o yaml`. |
| "Why is the node group not working?" after a Cluster diagnosis | Keep previous Cluster as primary context and observe worker-group related resources as `related_resource_status`. |
| "Show all clusters in this namespace." | List observation and direct response; no guide lookup needed. |
| "Find which namespace has cluster X." | `target_resolution` observation and direct response or next primary detail observation, depending on the user's wording. |

Do not force all diagnostic observations into a single-resource shape. The key question is whether the observation advances the active request context.

## RAG vs Direct Response

RAG is useful when the assistant needs procedural operating knowledge beyond the immediate observation. Direct response is preferred when the observation already answers the question.

| Situation | Preferred Next Phase |
|---|---|
| User asks "what is the status?" and status is visible. | `response_synthesis` |
| User asks "why is it broken?" and status shows a CRD-specific failure pattern. | `guidance_decision` then possibly `guidance_lookup` |
| User asks "summarize these events." | `response_synthesis` |
| User asks "fix it." | `safety_policy`, then observation/remediation planning; guide lookup optional after evidence. |
| Target/scope is missing but discoverable. | `target_resolution` observation |
| Target/scope is missing and not discoverable safely. | `clarification` |

## Implementation Notes

- `requirement_analysis` should not emit or imply a RAG action. It may be followed by a model-declared `phase_plan`.
- `operational_focus` is a diagnostic angle, not an execution target and not a guide lookup command.
- Runtime validation should steer correction, not act as the main planning engine.
- Runtime disables automatic initial guide injection paths; guide lookup is entered through model-selected phase flow.
- Runtime should expose CRD discovery results as evidence or eligibility context for the model's `guidance_decision`, not as an automatic transition.
- Runtime may maintain compact observation state, but should avoid storing full YAML/logs in persistent conversation memory.
- Guide progress begins only after a guide is injected and only inside the `guided_diagnosis` `phase_step`. Before that, observations belong to the default request-processing pipeline and are completed with `phase_progress`.
- `resource guide injected...` style messages are internal logs, not user-facing output.

## Remaining Contract Details

The phase-owned guide path is implemented. The remaining work is contract hardening, not another guide-entry path:

- Reject `phase_progress` when `evidence_useful=false` or the latest relevant observation is blocked/failed (`BUG-8`).
- Validate that mutation verification occurs after execution and before response/final phases, rather than accepting any phase whose name merely contains `verify` (`BUG-10`).
- Determine terminal phases from graph sinks instead of list order so valid multi-sink plans are accepted (`BUG-7`).
- Require a valid initial phase instead of accepting any declared `current_phase_index` (`BUG-21`).
- Refine phase-progress validation beyond structural checks, especially for broad scans and ambiguous evidence.
- Define when a broad scan becomes diagnostic evidence instead of target resolution.
- Define how namespace/name discovered from `-A` output should update `request_context`.
- Define how many observations can be collected before forcing either response synthesis or guidance decision.
