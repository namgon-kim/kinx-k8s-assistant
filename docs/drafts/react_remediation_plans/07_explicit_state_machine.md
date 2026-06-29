# Plan 07: Explicit State Machine

## Problem

현재 `react.Loop`는 `State` enum을 갖고 있지만 실제 의미는 여러 플래그 조합에 의존한다.

예:

- `StateRunning + guideStepState != nil`
- `StateRunning + finalReportRequested == true`
- `StateRunning + guidedPhaseProgressRequested == true`
- `StateDone + pendingFinalReport != nil`
- `StateWaitingDirectionText + acceptRawSlashInput == true`

이런 implicit state machine은 작은 수정에도 예기치 않은 전이를 만들 수 있다.

## Current Code Evidence

- `internal/react/loop.go`
  - `State` enum은 coarse-grained 상태다.
  - `Loop` struct에 phase, guide, final report, direction prompt, resource guide 관련 flags가 분산되어 있다.
- `internal/react/phase_plan.go`
  - phase state 전이는 `phaseStepState.acceptProgress`에 있다.
- `internal/react/resource_guidance.go`
  - resource guide injection이 phase와 promptOptions, guideStepState를 함께 바꾼다.
- `internal/react/final_report.go`
  - final report가 next direction과 problematic resource prompt로 이어진다.

## Desired Contract

상태는 "현재 무엇을 기다리는가"를 명확히 표현해야 한다.

Proposed states:

```go
type ControlState string

const (
    AwaitingUserQuery ControlState = "awaiting_user_query"
    AwaitingRequirementAnalysis ControlState = "awaiting_requirement_analysis"
    AwaitingPhasePlan ControlState = "awaiting_phase_plan"
    AwaitingAction ControlState = "awaiting_action"
    AwaitingApproval ControlState = "awaiting_approval"
    ExecutingTool ControlState = "executing_tool"
    AwaitingMutationVerification ControlState = "awaiting_mutation_verification"
    AwaitingPhaseProgress ControlState = "awaiting_phase_progress"
    AwaitingGuideProgress ControlState = "awaiting_guide_progress"
    AwaitingFinalReport ControlState = "awaiting_final_report"
    AwaitingContinuationChoice ControlState = "awaiting_continuation_choice"
    AwaitingContinuationText ControlState = "awaiting_continuation_text"
    Complete ControlState = "complete"
    Exited ControlState = "exited"
)
```

## Proposed State Context

```go
type RuntimeState struct {
    Control ControlState
    Request *requestContext
    Phase *phaseStepState
    Guide *guideStepState
    PendingApproval []PendingCall
    PendingMutationVerification *pendingMutationVerification
    PendingFinalReport *finalReport
    PendingDirectionPrompt *directionPromptState
}
```

## Transition Table

| From | Event | To |
| --- | --- | --- |
| AwaitingUserQuery | user query | AwaitingRequirementAnalysis |
| AwaitingRequirementAnalysis | valid requirement_analysis | AwaitingPhasePlan |
| AwaitingPhasePlan | valid phase_plan | AwaitingAction |
| AwaitingAction | mutating action | AwaitingApproval |
| AwaitingApproval | approved | ExecutingTool |
| ExecutingTool | mutation success | AwaitingMutationVerification |
| AwaitingMutationVerification | verification observed | AwaitingPhaseProgress or AwaitingFinalReport |
| AwaitingFinalReport | inconclusive report | AwaitingContinuationChoice |
| AwaitingContinuationChoice | free input selected | AwaitingContinuationText |
| AwaitingContinuationText | text received | AwaitingPhasePlan or AwaitingAction |

## Proposed Changes

1. Keep existing `State` temporarily as compatibility layer.
2. Add `RuntimeState.Control`.
3. Move `finalReportRequested`, `guidedPhaseProgressRequested`, `pendingDirectionPrompt`, `pendingMutationVerification` under typed state context.
4. Add a transition function.

```go
func (l *Loop) transition(event RuntimeEvent) error
```

5. Gate functions check `RuntimeState.Control`, not scattered flags.

## Acceptance Criteria

- It is possible to print/debug one state value that explains what the loop is waiting for.
- User input ownership is derived from state, not separate atomic booleans.
- Mutation verification is a first-class state.
- Guide completion cannot request final report and phase progress simultaneously.
- Direction choice/free-text cannot be preempted by orchestrator side flow.

## Migration Strategy

1. Add `ControlState` without deleting existing `State`.
2. Set `ControlState` alongside existing state transitions.
3. Convert user input ownership to `ControlState`.
4. Convert mutation verification to `ControlState`.
5. Convert guide/final report requested flags.
6. Remove redundant flags once tests cover transitions.

## Risks

- Broad refactor touches many files.
- Do not do this before namespace and mutation lifecycle gates are fixed.
- Start by adding observability and tests for current transitions.
