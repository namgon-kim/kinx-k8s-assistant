# Guide Progress and Continuation Flow

This document describes how the ReAct loop keeps the model aligned with the original request and the active resource guide across many iterations, and how it transitions to a `final_report` and a user-chosen next direction when the guide is exhausted.

Related contracts: [`requirement_analysis`](./requirement_analysis.md) (initial request classification), [`request_processing_phases`](./request_processing_phases.md) (default observation and guidance-decision phases before any guide is injected).

## Motivation

Two patterns frequently regressed during long diagnoses:

1. **Drift from the determined request.** The model's attention shifts to the most recent tool observation and stops serving the original `requirement_analysis`.
2. **Drift from the RAG guide.** The injected resource-guide body becomes "old" chat history. The model collects evidence but never converges on the answer or sometimes invents new diagnostic targets disconnected from the guide.

The runtime addresses both without forcibly blocking output. Two anchors are re-emitted on every iteration, and the loop has a defined exhaustion path that hands control back to the user.

## Iteration anchors

`Loop.buildIterationSendContent` prepends compact anchor messages before whatever the current iteration is sending. Order matters: `requirement_analysis` first (the user-level goal), then `phase_step` when a phase plan is active, then `guide_step` only while the active phase is `guided_diagnosis`, then the latest observations.

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

Re-emits a compact progress representation of the active resource guide. This anchor exists only when the active `phase_step` is `guided_diagnosis`. The full guide body is still injected only once via `appendGuideObservation`. The complete diagnostic step list is persisted in a workspace-local temporary step store; each iteration carries only the progress counters and the next step detail needed for the current action.

Format (rendered each iteration):

```text
Active resource-guide progress. Continue following this guide unless final_report has already been emitted.
guide_id: <id>
guide_title: <title>
steps_completed: <done> / <total>
step_store: <workdir>/guides/guide-steps-<hash>.json
step_store_hash: sha256:<hash>
remaining_step_indices: 3,4,5
next_step_index: 3
next_step_description: Inspect Cluster conditions and synchronization annotations
next_step_command_template: kubectl -n <ns> get cluster <name> -o yaml
next_step_expected_outcome: Conditions identify the reconciliation blocker
Rules:
- For each action, set action.guide_progress.step_completed to the 1-based step index this action advances, and action.guide_progress.evidence_useful to whether the observation moved diagnosis forward.
- Follow next_step unless live evidence makes it redundant; if skipping, explain why and mark only the step that was actually advanced.
- Do not invent step indices outside remaining_step_indices.
- When every step is completed (or further steps are clearly redundant for the live evidence), emit final_report instead of another action.
```

`guideStepState` is built from `GuideCase.DiagnosticSteps` at the moment the guide is injected. Only the top case is tracked because the runtime injects one case at a time. The runtime stores the full diagnostic step payload in `step_store` for bookkeeping, but the model does not need the full list in every iteration.

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

### `action.guide_progress`

Self-reported nested guide progress for the action that the model is emitting. This field is valid only while the active top-level phase is `guided_diagnosis`.

```json
{
  "action": {
    "name": "kubectl",
    "command": "kubectl -n <ns> get cluster <name> -o yaml",
    "modifies_resource": "no",
    "guide_progress": {
      "step_completed": 1,
      "evidence_useful": true
    }
    /* ...other fields... */
  }
}
```

| Field | Meaning |
|---|---|
| `step_completed` | 1-based index of the guide step this action advances. Omit when no guide is active. |
| `evidence_useful` | True when the previous observation moved diagnosis forward. Omit when no guide is active. |

When this field is present and the tool observation is useful, `recordAction` calls `markGuideStepCompleted(step)`. If the model omits `action.guide_progress`, the runtime may infer completion only for the current next guide step when the executed command matches that step's command template. Observations with explicit errors or statuses such as `blocked`, `declined`, `failed`, or `error` do not complete guide steps.

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
| `true` without `problematic_resources` | Render the conclusion, transition to `StateDone`. |
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

## State machine

This is the outer runtime state machine. The model-declared `phase_step` workflow is nested inside `StateRunning`; it should not require a new user-visible runtime state for every phase.

```
                  ┌──────────────────────────┐
                  │ StateIdle / StateDone    │
                  │  (waits for user input)  │
                  └────────────┬─────────────┘
                               │ user types query
                               ▼
                  ┌──────────────────────────┐
   ┌─────────────►│ StateRunning             │
   │              │  runIteration            │
   │              └───┬──────────┬─────────┬─┘
   │                  │          │         │
   │   mutation       │          │         │   final_report (conclusive=true)
   │   approval?      │          │         │   or plain answer
   │   ┌──────────────▼──┐       │         │   ───────────────► StateDone
   │   │ StateWaiting    │       │         │
   │   │   Approval      │       │         │
   │   └──────────┬──────┘       │         │   final_report (conclusive=false)
   │              │              │         │   → next_directions request queued
   │              ▼              │         │
   └──────────────────────────── │ ────────┘
                                 │
                          next_directions emitted by model
                                 │
                                 ▼
                  ┌──────────────────────────┐
                  │ StateWaitingDirection    │
                  │   Choice                 │
                  │  (UserChoiceRequest)     │
                  └─┬──────────┬──────────┬──┘
                    │          │          │
       finalize     │          │ another_ │ different_
       ─────────────┘          │  guide   │  approach
       StateDone               │          │
                               ▼          ▼
                  ┌──────────────────────────┐
                  │ searchAndInjectResource  │  or  inject directive
                  │  Guide                   │  into currChatContent
                  │  → StateRunning          │  → StateRunning
                  └──────────────────────────┘
                    user picked "직접 입력"
                               │
                               ▼
                  ┌──────────────────────────┐
                  │ StateWaitingDirection    │
                  │   Text                   │
                  │  (UserInputRequest)      │
                  └────────────┬─────────────┘
                               │ user types text
                               ▼
                       inject as different_approach
                          → StateRunning
```

New states added in this flow: `StateWaitingDirectionChoice`, `StateWaitingDirectionText`. Both reuse the orchestrator's existing `UserChoiceRequest` and `UserInputRequest` handlers.

## Key files

| File | Role |
|---|---|
| `internal/react/loop.go` | State enum, `phaseStepState`, `guideStepState`, anchor wiring (`buildIterationSendContent`), phase and guide step completion bookkeeping. |
| `internal/react/request_context.go`, `internal/react/phase_plan.go` | `requirementAnalysisAnchor()`, `phaseStepAnchor()`, `guideStepAnchor()`. |
| `internal/react/resource_guidance.go` | `buildGuideStepState` builds nested guide progress from `GuideCase.DiagnosticSteps` and persists the full step details to `step_store` when the guide is injected under `guided_diagnosis`. |
| `internal/react/final_report.go` | `requestFinalReportFromModel`, `consumeFinalReport`, `renderFinalReport`, `requestNextDirectionsFromModel`. |
| `internal/react/next_directions.go` | `consumeNextDirections`, `promptDirectionChoice`, `waitForDirectionChoice`, `waitForDirectionText`, `applyDirectionOption`. |
| `internal/react/shim.go` | Target top-level shim fields: `phase_plan`, `phase_progress`, `action.guide_progress`, `final_report`, `next_directions`. |
| `prompts/default.tmpl`, `prompts/system_ko.tmpl` | Output schemas and model rules for phase planning, phase progress, nested guide progress, and final reporting. |

## Design constraints honored

- **No forced output filtering.** The model can ignore the final_report instruction and emit `action` again; the anchor will re-emit on the next iteration. Drift is mitigated by per-iteration anchors and clear positive rules, not by post-hoc string filtering.
- **Single-case tracking.** Only the top guide case's `DiagnosticSteps` populates `guideStepState`. Multi-case progress is not tracked because the runtime only injects one case at a time.
- **No MaxIteration coupling.** `MaxIterations` still ends the loop with the existing "Maximum number of iterations reached" message. Guide-exhaustion via `final_report` is independent and can fire well before `MaxIterations`.
- **Phase owns guide.** `guideStepState` is subordinate to the active `guided_diagnosis` `phase_step`; guide completion should lead to parent `phase_progress`, not directly replace the phase workflow.

## Invariants

- `phaseStepState` is non-nil after the model-declared `phase_plan` is accepted and remains active until `final_report`, `startQuery`, or `clearConversationState`.
- `guideStepState` is non-nil only while the active `phase_step` is `guided_diagnosis`; it is cleared when that parent phase completes, when a different guide is selected, or on `startQuery`/`clearConversationState`.
- `another_guide` never bypasses `phase_step`: it clears prior guide progress and resumes the top-level phase workflow so that a new guide can be requested only through `guidance_lookup` and consumed only under `guided_diagnosis`.
- `finalReportRequested` is set when `requestFinalReportFromModel` queues its instruction. It prevents duplicate instruction text from being re-appended on every dispatch. It is cleared on `applyDirectionOption`, `startQuery`, `clearConversationState`, and at every `injectResourceGuide*` entry.
- `pendingDirectionPrompt` is non-nil only while in `StateWaitingDirectionChoice`. The user's `Choice` index is mapped back to a concrete `nextDirectionOption` (or the synthetic finalize / free-input rows) using `FreeInputIdx` and `FinalizeIdx`.
- `phase_progress.phase_completed` is the model's self-report for the top-level phase. It is ignored when the corresponding observation is blocked, declined, failed, errored, or structurally unrelated to the active phase goal.
- `action.guide_progress.step_completed` is the model's self-report, not enforced by command matching. It is ignored when the corresponding observation is blocked, declined, failed, or errored. Misreported indices on successful observations are accepted (the worst case is premature `final_report` instruction or a step staying open).
- Internal schema/correction errors, including invalid `next_directions`, are runtime errors rather than Kubernetes incident evidence. They must not trigger incident guidance offers.

## TODO: Contract Hardening

The current guide progress and continuation contract still has ambiguous areas that can cause premature conclusions or inconsistent fallback behavior. Tighten these before adding more continuation features.

### TODO: Define `final_report.conclusive` Evidence Levels

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

### TODO: Specify Guide Step Completion Matching

The contract says the runtime may infer guide step completion when a command matches a step template, but the match semantics are not defined.

Define:

- whether matching is exact normalized command equality or structured command matching
- whether namespace, name, selectors, labels, and resource/name shorthand must match
- whether a command that queries only part of a multi-resource template can complete the step
- whether a command with extra resources can complete the step
- whether a command with the same verb/resource but different selector can complete the step
- whether a failed command can ever complete the step

Suggested default:

- Exact normalized rendered command match is the only automatic completion path.
- Explicit `action.guide_progress.step_completed` is accepted only when the observation is useful and not blocked/declined/failed/error.
- Partial command matches do not complete a step unless explicitly documented for that guide.

Acceptance criteria:

- An implementer does not have to guess whether `kubectl get cluster <name>` completes a step whose template queried `cluster/openstackcluster/kamajicontrolplane`.
- Premature `final_report` requests cannot be caused by loose verb/resource matching.

### TODO: Separate Runtime Fallbacks From Output Filtering

The design says "No forced output filtering", while later sections describe schema recovery and fallback choices. Clarify the exception boundary.

Define three categories:

- Model output guidance: prompt-level instructions only.
- Runtime correction: invalid schema or unsafe output is corrected by asking the model to re-emit.
- Runtime UX fallback: after repeated schema failure, the runtime may bypass the model output and show safe user choices.

Explicitly include:

- invalid `next_directions`
- invalid `final_report`
- invalid/missing `requirement_analysis`
- action target mismatch
- read-only mutation block
- approval decline

Acceptance criteria:

- "No forced output filtering" does not prevent schema/safety recovery.
- Runtime fallback behavior is predictable and documented for every internal structured output.

### TODO: Bound `next_directions` Fallback Directives

The fallback option built from `blockers` and `evidence_missing` can become vague or too long. Define how to create a safe generic `different_approach` directive.

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

### TODO: Document Internal Error Isolation

The contract now states internal schema/correction errors are not incident evidence, but it does not define the full classification.

Add a table of error classes:

| Error class | Examples | User-visible? | Incident guidance candidate? |
|---|---|---|---|
| Kubernetes observation error | `kubectl` not found, resource not found, API forbidden | Yes | Maybe, only if related to user diagnosis |
| Runtime schema error | invalid `next_directions`, invalid `final_report`, shim parse issue | No or compact message | No |
| Provider error | LLM HTTP 500, context length, streaming error | Yes as assistant/runtime error | No |
| User approval outcome | declined, read-only blocked | Yes | No |

Acceptance criteria:

- Internal runtime failures do not trigger "감지된 문제에 대해 해결 방법을 찾아볼까요?".
- Kubernetes evidence still can trigger incident guidance when the user asked for diagnosis/remediation.
