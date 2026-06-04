# Guide Progress and Continuation Flow

This document describes how the ReAct loop keeps the model aligned with the original request and the active resource guide across many iterations, and how it transitions to a `final_report` and a user-chosen next direction when the guide is exhausted.

Related contracts: [`requirement_analysis`](./requirement_analysis.md) (initial request classification).

## Motivation

Two patterns frequently regressed during long diagnoses:

1. **Drift from the determined request.** The model's attention shifts to the most recent tool observation and stops serving the original `requirement_analysis`.
2. **Drift from the RAG guide.** The injected resource-guide body becomes "old" chat history. The model collects evidence but never converges on the answer or sometimes invents new diagnostic targets disconnected from the guide.

The runtime addresses both without forcibly blocking output. Two anchors are re-emitted on every iteration, and the loop has a defined exhaustion path that hands control back to the user.

## Iteration anchors

`Loop.buildIterationSendContent` prepends two short anchor messages before whatever the current iteration is sending. Order matters: `requirement_analysis` first (the user-level goal), then `guide_step` (the current procedural step), then the latest observations.

### requirement_analysis anchor

Re-emits the accepted `requirement_analysis` JSON (and the derived `request_context` when present) so the model keeps `target.category`, `resource_candidates`, and `request_type` stable across iterations.

The anchor explicitly tells the model:

- Do not silently switch `target.category` or `resource_candidates`.
- If live evidence implies a different operational focus on the same target family, use `resource_guide_lookup` instead of pivoting the diagnosis target.
- Before emitting `action`, verify it advances this analysis.

### guide_step anchor (L2)

Re-emits a compact checklist representation of the active resource guide. The full guide body is still injected only once via `appendGuideObservation`; the anchor is the lightweight per-iteration reminder.

Format (rendered each iteration):

```text
Active resource-guide progress. Continue following this guide unless final_report has already been emitted.
guide_id: <id>
guide_title: <title>
steps_completed: <done> / <total>
steps:
  [x] 1. Inspect top-level cluster resources
  [x] 2. Inspect controller-created child resources
  [ ] 3. Inspect Cluster conditions and synchronization annotations
Rules:
- For each action, set guide_progress.step_completed to the 1-based step index this action advances, and guide_progress.evidence_useful to whether the observation moved diagnosis forward.
- Do not skip ahead by collapsing several steps into one command unless the guide template combined them.
- When every step is completed (or further steps are clearly redundant for the live evidence), emit final_report instead of another action.
```

`guideStepState` is built from `GuideCase.DiagnosticSteps` at the moment the guide is injected. Only the top case is tracked because the runtime injects one case at a time.

## Output schema additions

The JSON ReAct shim accepts these additional top-level objects in the model response (each in its own iteration, never combined):

### `action.guide_progress`

Self-reported guide progress for the action that the model is emitting.

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

When this field is present, `recordAction` calls `markGuideStepCompleted(step)`. When `guideStepState.allCompleted()` becomes true, `dispatchToolCalls` calls `requestFinalReportFromModel()` exactly once.

### `final_report`

The runtime instructs the model to emit this when all diagnostic_steps are completed (or further steps are clearly redundant). The model may also emit it earlier if it judges the evidence sufficient.

```json
{
  "thought": "Briefly explain why the guide is exhausted and whether the gathered evidence is conclusive.",
  "final_report": {
    "conclusive": true,
    "conclusion": "Grounded answer when conclusive=true. Omit when false.",
    "attempted": ["short bullets summarizing the diagnostic steps actually run"],
    "evidence_known": ["facts directly observed from tool output"],
    "evidence_missing": ["facts that would have helped but were not obtainable; only when conclusive=false"],
    "most_likely_cause": "best-guess cause given partial evidence, or the literal string \"inconclusive\"",
    "recommended_user_actions": ["concrete next steps the user can run outside this session (optional)"],
    "blockers": ["hard constraints that prevented full diagnosis (optional)"]
  }
}
```

| `conclusive` | Loop behavior |
|---|---|
| `true` | Render the conclusion, transition to `StateDone`. |
| `false` | Render the report, call `requestNextDirectionsFromModel(report)`, continue the loop so the next model response is a `next_directions` object. |

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
| `another_guide` | `resource_family`, `problem_focus` | Reset guide step state, run `searchAndInjectResourceGuide(family, query)` and resume. |
| `different_approach` | `instruction` | Inject the directive as a user message and resume the ReAct loop. |

Invalid options (missing required fields, unknown `kind`) are filtered before the user is prompted. If no valid option remains, the runtime emits a correction and asks the model to re-emit.

## State machine

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
| `internal/react/loop.go` | State enum, `guideStepState`, anchor wiring (`buildIterationSendContent`), guide step completion bookkeeping. |
| `internal/react/request_context.go` | `requirementAnalysisAnchor()`, `guideStepAnchor()`. |
| `internal/react/resource_guidance.go` | `buildGuideStepState` builds the checklist from `GuideCase.DiagnosticSteps` when the guide is injected. |
| `internal/react/final_report.go` | `requestFinalReportFromModel`, `consumeFinalReport`, `renderFinalReport`, `requestNextDirectionsFromModel`. |
| `internal/react/next_directions.go` | `consumeNextDirections`, `promptDirectionChoice`, `waitForDirectionChoice`, `waitForDirectionText`, `applyDirectionOption`. |
| `internal/react/shim.go` | New top-level shim fields: `action.guide_progress`, `final_report`, `next_directions`. |
| `prompts/default.tmpl`, `prompts/system_ko.tmpl` | Output schemas and rules for the model. |

## Design constraints honored

- **No forced output filtering.** The model can ignore the final_report instruction and emit `action` again; the anchor will re-emit on the next iteration. Drift is mitigated by per-iteration anchors and clear positive rules, not by post-hoc string filtering.
- **Single-case tracking.** Only the top guide case's `DiagnosticSteps` populates `guideStepState`. Multi-case progress is not tracked because the runtime only injects one case at a time.
- **No MaxIteration coupling.** `MaxIterations` still ends the loop with the existing "Maximum number of iterations reached" message. Guide-exhaustion via `final_report` is independent and can fire well before `MaxIterations`.

## Invariants

- `guideStepState` is non-nil only between `injectResourceGuideAttempt` (or `applyDirectionOption(another_guide)`) and a successful `final_report` or `startQuery`/`clearConversationState`.
- `finalReportRequested` is set when `requestFinalReportFromModel` queues its instruction. It prevents duplicate instruction text from being re-appended on every dispatch. It is cleared on `applyDirectionOption`, `startQuery`, `clearConversationState`, and at every `injectResourceGuide*` entry.
- `pendingDirectionPrompt` is non-nil only while in `StateWaitingDirectionChoice`. The user's `Choice` index is mapped back to a concrete `nextDirectionOption` (or the synthetic finalize / free-input rows) using `FreeInputIdx` and `FinalizeIdx`.
- `action.guide_progress.step_completed` is the model's self-report, not enforced by command matching. Misreported indices are accepted (the worst case is premature `final_report` instruction or a step staying open).
