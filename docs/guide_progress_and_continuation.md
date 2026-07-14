# Guide Progress and Continuation Flow

This document describes how the ReAct loop keeps the model aligned with the original request and the active resource guide across many iterations, and how it transitions to a `final_report` and a user-chosen next direction when the guide is exhausted.

Related contracts: [`requirement_analysis`](./requirement_analysis.md) (initial request classification), [`request_processing_phases`](./request_processing_phases.md) (default observation and guidance-decision phases before any guide is injected).

## Motivation

Two patterns frequently regressed during long diagnoses:

1. **Drift from the determined request.** The model's attention shifts to the most recent tool observation and stops serving the original `requirement_analysis`.
2. **Drift from the RAG guide.** The injected resource-guide body becomes "old" chat history. The model collects evidence but never converges on the answer or sometimes invents new diagnostic targets disconnected from the guide.

The runtime addresses both without relying only on prompt memory. Compact anchors are re-emitted on every iteration, and directive gates define the exhaustion path that hands control back to the user.

## Iteration anchors

`Loop.buildIterationSendContent` prepends compact anchor messages before whatever the current iteration is sending. Because each anchor is prepended, the model sees them in this effective order: `runtime_state`, `requirement_analysis`, `phase_step`, `guide_step`, `mutation_verification`, then the latest observations. `runtime_state` comes first so required/forbidden next outputs are visible before the model reads the older diagnostic context.

### requirement_analysis anchor

Re-emits the accepted `requirement_analysis` JSON (and the derived `request_context` when present) so the model keeps `target.category`, `resource_candidates`, and `request_type` stable across iterations.

The anchor explicitly tells the model:

- Do not silently switch `target.category` or `resource_candidates`.
- If live evidence implies a different operational focus on the same target family, use `resource_guide_lookup` instead of pivoting the diagnosis target.
- Before emitting `action`, verify it advances this analysis.

### phase_step anchor (L1)

Re-emits the active top-level request-processing step from the model-declared `phase_plan`. This is the parent workflow step that owns ordinary observation, response synthesis, guidance decision, and final report transitions.

The `phase_step` anchor should include:

- request goal;
- current phase index/name;
- current phase goal;
- current phase completion condition;
- completed phase indices;
- allowed next phase names;
- compact CRD/resource-family eligibility context when runtime discovery has confirmed it after observation.

The model completes a phase with `phase_progress`. Runtime must not use `guide_progress` to complete a top-level phase.

### guide_step anchor (L2, nested)

Re-emits a compact progress representation of the active resource guide. This anchor exists only when the active `phase_step` is `guided_diagnosis`. The full guide body is still injected only once via `appendGuideObservation`. The diagnostic step list is stored in `guideStepState.StepDetails`; each iteration carries only the progress counters and the next step detail needed for the current action.

Format (rendered each iteration):

```text
Active resource-guide progress. Continue following this guide unless final_report has already been emitted.
guide_id: <id>
guide_title: <title>
steps_completed: <done> / <total>
remaining_step_indices: 3,4,5
next_step_index: 3
next_step_description: Inspect Cluster conditions and synchronization annotations
next_step_command_template: kubectl -n <ns> get cluster <name> -o yaml
next_step_expected_outcome: Conditions identify the reconciliation blocker
Rules:
- After useful live evidence advances a guide step, emit `guide_progress.step_completed` with the 1-based step index and `guide_progress.evidence_useful=true`.
- Follow next_step unless live evidence makes it redundant; if skipping, explain why and mark only the step that was actually advanced.
- Do not invent step indices outside remaining_step_indices.
- When every step is completed (or further steps are clearly redundant for the live evidence), complete the parent `guided_diagnosis` phase with `phase_progress`; the runtime may then request `final_report`.
```

`guideStepState` is built from `GuideCase.DiagnosticSteps` at the moment the guide is injected. Only the top case is tracked because the runtime injects one case at a time. The runtime stores the diagnostic step payload in `guideStepState.StepDetails` and exposes current progress through the runtime snapshot/anchor; the model does not need the full list in every iteration.

`guideStepState` is scoped to the current `guided_diagnosis` `phase_step`. When every nested guide step is completed, the model should complete the parent `guided_diagnosis` phase with `phase_progress` and move to `final_report`.

## Output schema additions

The JSON ReAct shim accepts these additional top-level objects in the model response (each in its own iteration, never combined):

### `phase_progress`

Self-reported progress for the active top-level `phase_step`. This is separate from guide progress and is valid before, during, and after guided diagnosis.

```json
{
  "phase_progress": {
    "phase_completed": 4,
    "evidence_useful": true,
    "completion_reason": "The primary Cluster status and related control-plane resources were observed.",
    "next_phase": "guidance_decision"
  }
}
```

| Field | Meaning |
|---|---|
| `phase_completed` | 1-based index of the top-level `phase_step` completed by the latest response/observation. |
| `evidence_useful` | True when the latest observation or response advanced the request goal. |
| `completion_reason` | Short grounding for why the phase completion condition is satisfied. |
| `next_phase` | Name of the next top-level phase from the accepted phase plan, or a model-proposed amendment when the observation changed the route. |

`phase_progress` owns the parent workflow. `guide_progress` owns nested guide diagnostic steps only.

### `guide_progress`

Self-reported nested guide progress. This field is valid only while the active top-level phase is `guided_diagnosis`.

In native function-calling mode, guide progress is a separate runtime-internal function call named `__guide_progress__`; it is not part of the real `kubectl` or `bash` tool schema. In shim mode, the parser accepts top-level `guide_progress` and also preserves backward compatibility for `action.guide_progress` by folding it into the same internal guide-progress call. Duplicate guide progress from the same shim response is not emitted twice.

```json
{
  "guide_progress": {
    "step_completed": 1,
    "evidence_useful": true
  }
}
```

| Field | Meaning |
|---|---|
| `step_completed` | 1-based index of the guide step advanced by live evidence. Omit when no guide is active. |
| `evidence_useful` | True when the previous observation moved diagnosis forward. Omit when no guide is active. |

When this field is present and the referenced evidence is useful, `consumeGuideProgress` calls `markGuideStepCompleted(step)`. If the model omits explicit guide progress, `recordAction` may infer completion only for the current next guide step when the executed command exactly matches the rendered guide command after whitespace normalization. Observations with explicit errors or statuses such as `blocked`, `declined`, `failed`, or `error` do not complete guide steps.

When `guideStepState.allCompleted()` becomes true, the model should emit `phase_progress` for the parent `guided_diagnosis` phase and then proceed to `final_report`. Runtime may request a final report as a safety fallback, but the conceptual transition is phase-level, not guide-step-level.

### `final_report`

The runtime instructs the model to emit this when all diagnostic_steps are completed (or further steps are clearly redundant). The model may also emit it earlier if it judges the evidence sufficient.

```json
{
  "thought": "Briefly explain why the guide is exhausted and whether the gathered evidence is conclusive.",
  "final_report": {
    "conclusive": true,
    "conclusion": "Grounded answer when conclusive=true. Omit when false.",
    "attempted": ["short bullets summarizing the diagnostic steps actually run"],
    "evidence_known": ["facts directly observed from tool output; required when conclusive=true"],
    "evidence_missing": ["facts that would have helped but were not obtainable; use with blockers when conclusive=false and evidence_known is empty"],
    "most_likely_cause": "best-guess cause given partial evidence, or the literal string \"inconclusive\"",
    "problematic_resources": [
      {
        "kind": "Kubernetes resource kind for the suspected blocker/root-cause investigation target",
        "name": "resource name",
        "namespace": "resource namespace when namespaced",
        "reason": "observed evidence showing why this resource is the next suspected blocker/root-cause target to investigate"
      }
    ],
    "recommended_user_actions": ["concrete next steps the user can run outside this session (optional)"],
    "blockers": ["hard constraints that prevented full diagnosis (optional)"]
  }
}
```

| `conclusive` | Loop behavior |
|---|---|
| `true` without `problematic_resources` | Render the conclusion and return control to `RuntimeControlAwaitingUserQuery`. |
| `true` with `problematic_resources` | Render the conclusion, show a user choice asking whether to investigate the named suspected blocker/root-cause resource(s). If the user chooses yes/resource option, start a new query from requirement_analysis with that resource kind/name/namespace as the target. |
| `false` | Render the report, call `requestNextDirectionsFromModel(report)`, continue the loop so the next model response is a `next_directions` object. |

`problematic_resources` is not a list of resources that merely report symptoms. If the original primary resource reports a condition caused by another resource family, do not list the primary resource unless evidence shows the primary object's own spec, metadata, or configuration is malformed. If the likely related blocker kind is known but its object name is not, leave `problematic_resources` empty and put the missing related-resource lookup in `evidence_missing` or `recommended_user_actions`.

### `next_directions`

Emitted only after an inconclusive `final_report`. The model proposes 1–3 distinct continuation options. The runtime adds a "직접 입력" and a "여기서 진단 종료" choice on the user side; the model must not include a "finalize" option itself.

```json
{
  "thought": "Briefly state what remained unresolved and why these options would unblock progress.",
  "next_directions": {
    "note": "Optional one-line context for the user.",
    "options": [
      {
        "kind": "another_guide",
        "summary": "User-facing one-liner.",
        "why": "Why it might unblock progress.",
        "resource_family": "CRD family for the refined lookup",
        "problem_focus": "Operational problem to search for; never raw status values"
      },
      {
        "kind": "different_approach",
        "summary": "User-facing one-liner.",
        "why": "Why this angle helps.",
        "instruction": "Short directive for the alternative diagnostic angle"
      }
    ]
  }
}
```

| `kind` | Required fields | Runtime behavior on user pick |
|---|---|---|
| `another_guide` | `resource_family`, `problem_focus` | Reset guide step state, rewind to the pre-guidance phase, and let the model reach `guidance_lookup` before emitting a new `resource_guide_lookup`. Runtime does not inject guide steps outside a declared `guided_diagnosis` phase. |
| `different_approach` | `instruction` | Inject the directive as a user message and resume the ReAct loop. |
| `investigate_resource` | `resource_kind`, `resource_name`, optional `namespace` | Runtime-created option after a conclusive report with `problematic_resources`; starts a new query from requirement_analysis for that resource. |

Invalid options (missing required fields, unknown `kind`) are filtered before the user is prompted. If no valid option remains, the runtime emits one correction and asks the model to re-emit. If the correction also fails, the runtime does not show an internal schema error to the user; it falls back to a user choice prompt containing "직접 다른 방향 입력" and "여기서 진단 종료", plus one generic `different_approach` option when `blockers` or `evidence_missing` from the inconclusive `final_report` can be turned into a safe continuation directive.

## Control flow

The model-declared `phase_step` workflow and runtime control are separate axes. A phase says
which diagnostic goal is active; `RuntimeControlState` says which output or external event is
accepted next.

```text
RuntimeControlAwaitingUserQuery
  -> RuntimeControlAwaitingRequirementAnalysis
  -> RuntimeControlAwaitingPhasePlan
  -> RuntimeControlAwaitingModelStep
       -> RuntimeControlAwaitingResourceGuideLookup
       -> RuntimeControlAwaitingGuidedDiagnosisStep
       -> RuntimeControlAwaitingGuidedPhaseProgress
       -> RuntimeControlAwaitingApproval -> RuntimeControlExecutingTool
       -> RuntimeControlAwaitingMutationVerificationEvidence
       -> RuntimeControlAwaitingMutationVerificationResult
       -> RuntimeControlAwaitingFinalReport
       -> RuntimeControlAwaitingNextDirections
       -> RuntimeControlAwaitingContinuationChoice
            -> RuntimeControlAwaitingContinuationText
            -> RuntimeControlAwaitingModelStep
  -> RuntimeControlAwaitingUserQuery
```

Not every request visits every state. A read-only lookup can finish without approval or mutation
verification. An inconclusive diagnosis goes through next directions and continuation choice;
selecting direct input additionally uses continuation text.

The enum is defined in `contract/enums.go`; mutable control and lifecycle projection live in
`session/control.go`. `LoopLifecycleState` is only the goroutine/UI execution view and must not be
used as a substitute for the runtime obligation.

## Key files

| File | Role |
|---|---|
| `internal/react/contract/structured.go`, `enums.go` | Phase/guide/report/direction/verification payloads and control/phase/step enums. |
| `internal/react/session/phase.go`, `verification.go`, `context.go` | Mutable phase, verification, and compact context state. |
| `internal/react/flow/guidance` | Guide lookup eligibility and nested guide-step completion rules. |
| `internal/react/flow/verification` | Mutation evidence requirements, command matching, and continuation decision. |
| `internal/react/flow/report`, `internal/react/flow/direction` | Final-report normalization and continuation options. |
| `internal/react/protocol/calls.go`, `shim.go`, `schema.go` | Internal call names, native/shim normalization, and shim JSON repair. |
| `internal/react/coordinator/iteration.go` | Anchor wiring, structured-call consumption, guide/report/direction lifecycle integration, and compatibility adapters. |
| `internal/react/coordinator/input.go`, `output.go`, `execution.go` | Continuation input, user-facing output, approval/tool execution. |
| `prompts/default.tmpl`, `prompts/system_ko.tmpl` | Output schemas and model rules for phase planning, phase progress, nested guide progress, and final reporting. |

## Design constraints honored

- **Directive gates for critical transitions.** When runtime has requested guide completion, final report, or mutation verification result, conflicting structured outputs are rejected instead of being silently executed or dropped. Anchors still provide context, but lifecycle safety no longer depends only on the model following a soft instruction.
- **Conversation and observation gates.** Conversation/clarification requests cannot escape into `kubectl`/shell tool calls, and shell commands that only print or wait locally are rejected before dispatch because they do not produce cluster evidence.
- **Single-case tracking.** Only the top guide case's `DiagnosticSteps` populates active nested guide progress. Multi-case progress is not tracked because the runtime only injects one case at a time.
- **No MaxIteration coupling.** `MaxIterations` still ends the loop with the existing "Maximum number of iterations reached" message. Guide-exhaustion via `final_report` is independent and can fire well before `MaxIterations`.
- **Phase owns guide.** Nested guide progress is subordinate to the active `guided_diagnosis` `phase_step`; guide completion should lead to parent `phase_progress`, not directly replace the phase workflow.
- **Mutation needs verification.** A successful mutating command creates goal-level read-only evidence requirements unless the only available evidence is a successful target-unmapped `kubectl apply -f ...` output. Final report and phase completion are blocked until the model returns `mutation_verification_result` after the required observations.

## Contract invariants

- `session.State.Control` represents the next runtime obligation; phase and step status do not replace it.
- An accepted phase plan populates `session.PhaseState`, and its current phase remains separate from nested guide or verification steps.
- Active guide progress is valid only under the `guided_diagnosis` phase and is cleared when that parent phase or request ends.
- `another_guide` must re-enter guidance through a declared phase path; it must not inject guide steps directly into an unrelated phase.
- A successful mutation that needs direct-effect evidence populates verification requirements. Once all required evidence is collected, control must require exactly one `mutation_verification_result` before final reporting.
- `progressing` or `unresolved` verification must keep a continuation/recheck obligation until resolved or the bounded retry policy closes inconclusively.
- Continuation choice and free-text input are distinct controls with `react_choice` and `react_text` input owners.
- `phase_progress.phase_completed` is the model's self-report for the top-level phase. It is ignored when the corresponding observation is blocked, declined, failed, errored, or structurally unrelated to the active phase goal.
- `guide_progress.step_completed` is the model's self-report, not enforced by command matching. It is ignored when the corresponding observation is blocked, declined, failed, or errored. Misreported indices on successful observations are accepted (the worst case is premature parent phase-progress/final-report instruction or a step staying open).
- Internal schema/correction errors, including invalid `next_directions`, are runtime errors rather than Kubernetes incident evidence. They must not trigger incident guidance offers.

The coordinator still contains package-local compatibility fields for several of these concepts.
They are migration state, not a second public contract; removing them in favor of `session.State`
is tracked in [`TODO.md`](./TODO.md).

## Known implementation gaps

The invariants above are the required contract. The following gaps remain in the current code and
are tracked in [`../bug.md`](../bug.md):

- `phase_progress` does not yet reject `evidence_useful=false` or every failed/blocked latest observation (`BUG-8`).
- standalone `__guide_progress__` does not verify that the latest observation succeeded (`BUG-12`).
- progressing/unresolved mutation verification does not reliably re-arm the same verification cycle (`BUG-9`, `BUG-14`).
- `another_guide` rewind and `different_approach` continuation still have re-entry/branching risks (`BUG-1`, `BUG-13`).
- shim structured acknowledgements for guide and mutation results still use native `FunctionCallResult` history (`BUG-5`).

## Remaining Contract Hardening

The current guide progress and continuation runtime is implemented, but a few semantic thresholds still need tighter documentation before adding more continuation features.

### Define `final_report.conclusive` Evidence Levels

The current text says a report is conclusive when the evidence is sufficient, but it does not define sufficient. Add an evidence-level table that separates symptoms, supported causes, and root causes.

Recommended evidence levels:

| Level | Meaning | Allowed `final_report` behavior |
|---|---|---|
| Symptom observed | A condition/status/message says something is unhealthy, e.g. `WorkersAvailable=False`. | May report the symptom, but must not claim root cause. |
| Immediate blocker identified | Related resource evidence identifies where progress is blocked, e.g. MachineDeployment has 0 available replicas and a specific Machine is not ready. | May state the blocker and likely affected component. |
| Cause supported | Logs/events/status from the responsible object or controller support a specific cause. | May state most likely cause with evidence. |
| Root cause confirmed | Direct evidence proves the underlying cause and rules out major alternatives. | May mark `conclusive=true` for cause diagnosis. |

Add rules:

- A raw condition or status value is evidence, not automatically a cause.
- `conclusive=true` requires evidence that answers the user's actual question, not merely completion of guide steps.
- If only symptoms are observed, use `conclusive=false` or clearly label the answer as symptom-level.
- `most_likely_cause` must distinguish between `symptom`, `blocker`, and `cause`.

Acceptance criteria:

- A Cluster condition such as `WorkersAvailable=False` alone cannot produce a root-cause final report.
- The final report can still be useful by saying what is known, what is missing, and what should be inspected next.

### Implemented: Guide Step Completion Matching

Runtime supports two guide-step completion paths:

- Explicit `guide_progress.step_completed` is accepted only when `evidence_useful` is not false and the current tool observation succeeded.
- Automatic inference is limited to the current next guide step and requires exact equality between the executed command and `GuideCase.DiagnosticSteps[].RenderedCommand` after whitespace normalization.

Partial command matches, selector-equivalent commands, extra-resource commands, failed observations, blocked read-only attempts, and declined approvals do not complete guide steps automatically.

### Catalog Runtime Fallbacks and Directive Gates

The current design uses prompt-level guidance for ordinary reasoning, but runtime gates for schema, safety, and lifecycle transitions. Keep the boundary explicit so future changes do not reintroduce soft-only control for critical transitions.

Define three categories:

- Model output guidance: prompt-level instructions only.
- Runtime correction: invalid schema or unsafe output is corrected by asking the model to re-emit.
- Runtime UX fallback: after repeated schema failure, the runtime may bypass the model output and show safe user choices.
- Runtime directive gate: when runtime has requested a specific internal structured output, conflicting calls are rejected until the requested transition is satisfied or explicitly cleared.

Explicitly include:

- invalid `next_directions`
- invalid `final_report`
- invalid/missing `requirement_analysis`
- requested `phase_progress`/`final_report` after guide completion
- requested `mutation_verification_result` after mutation verification evidence is collected
- `mutationContinuationRequired` after `progressing` or `unresolved`
- conversation/clarification requests attempting tool calls
- self-talk shell actions such as `echo`, `printf`, `sleep`, or `read`
- assistant-managed guidance tool names emitted as model tool calls
- interactive command blocks
- classified tool failures: `command_syntax`, `rbac_forbidden`, `resource_not_found`, `timeout_or_api_unavailable`, `partial_success`, `unknown`
- action target mismatch
- read-only mutation block
- approval decline

Acceptance criteria:

- Directive gates are limited to schema, safety, and lifecycle-critical transitions.
- Runtime fallback behavior is predictable and documented for every internal structured output.

### Bound `next_directions` Fallback Directives

The runtime already falls back to a generic `different_approach` built from `blockers` and `evidence_missing` when repeated `next_directions` correction fails. The remaining contract work is bounding that generated directive.

Rules to define:

- maximum number of blockers/evidence items used
- maximum directive length
- whether raw command output may be included
- how to handle duplicate or low-value missing evidence
- when no generic option should be generated
- how the runtime labels fallback-generated options vs model-generated options

Acceptance criteria:

- Fallback choices remain short enough for CLI UX.
- Fallback directives cannot inject raw YAML/log blobs back into the model.
- The user can still choose "직접 다른 방향 입력" or "여기서 진단 종료" when no safe generic option exists.

### Document Internal Error Isolation

The runtime already filters internal runtime/schema/correction text out of incident guidance offers. The table below is the current classification boundary to preserve when new gates are added.

| Error class | Examples | User-visible? | Incident guidance candidate? |
|---|---|---|---|
| Kubernetes observation error | resource not found, API forbidden, timeout, partial result | Yes | Maybe, only if related to user diagnosis and the continuation choice UI is already shown |
| Agent command error | command syntax, `kubectl` binary missing, self-talk shell action, interactive command | Yes as retry/correction context | No by itself |
| Runtime schema error | invalid `next_directions`, invalid `final_report`, shim parse issue | No or compact message | No |
| Provider error | LLM HTTP 500, context length, streaming error | Yes as assistant/runtime error | No |
| User approval/policy outcome | declined, read-only blocked, RBAC blocker | Yes | No by itself |

Acceptance criteria:

- Internal runtime failures do not create incident runbook continuation options.
- Kubernetes evidence can create an incident runbook continuation option only when the user asked for diagnosis/remediation and the ReAct continuation choice UI is already being shown.
