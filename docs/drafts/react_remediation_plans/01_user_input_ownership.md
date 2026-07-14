# Plan 01: User Input Ownership

> 상태: 구현됨.
>
> `RuntimeSnapshot.Control` 기반 input dispatch와 orchestrator의 incident guidance
> choice-gating이 반영되어 있다. ReAct-owned free text/approval/choice 입력은
> incident side-flow가 선점하지 않는다.
> 현재 구현 위치는 `internal/react/contract/enums.go`, `session/control.go`,
> `coordinator/input.go`, `coordinator/loop.go`다. 아래 옛 루트 파일 경로는 구현 이력이다.

## Problem

ReAct loop가 사용자 입력을 기다리는 동안 orchestrator side-flow가 같은 입력 지점을 가로챌 수 있다. 실제 재현에서는 `next_directions`에서 "직접 다른 방향 입력"을 선택한 뒤, free-text 입력을 받아야 하는 순간에 incident guidance prompt가 먼저 나왔다.

이는 single controller 원칙 위반이다. 특정 시점의 사용자 입력은 하나의 controller만 소유해야 한다.

## Current Code Evidence

- `internal/react/contract/enums.go`, `internal/react/session/control.go`, `internal/react/coordinator/loop.go`, `internal/react/coordinator/iteration.go`
  - `RuntimeControlState`와 `InputOwner`가 approval, continuation choice, continuation text를 구분한다.
  - `Loop.InputOwner()`와 published snapshot이 현재 입력 소유자를 노출한다.
  - continuation choice에서 free-input을 고르면 `RuntimeControlAwaitingContinuationText`로 전환한다.
  - 이후 `MessageTypeUserInputRequest`가 발생하고 사용자의 직접 입력을 기다린다.
- `internal/orchestrator/orchestrator.go`
  - `handleMessage`는 `MessageTypeUserInputRequest`를 받으면 `handleAgentInputRequest`로 진입한다.
  - `handleAgentInputRequest`는 incident guidance side-flow를 실행하지 않고 pending offer를 폐기한 뒤 입력을 ReAct loop로 전달한다.
  - `handleAgentChoiceRequest`는 continuation-choice control이고 선택지가 조건을 만족할 때만 runbook 검색 option을 추가한다.
- `internal/orchestrator/incident_guidance_flow.go`
  - agent text/tool result를 관찰해 `incidentGuidanceOfferPending`을 만들 수 있다.
  - out-of-band remediation approval prompt 경로는 제거됐다.

## Desired Contract

ReAct가 explicit user response를 기다리는 동안 입력 소유자는 ReAct다.

다음 상태에서는 orchestrator side-flow가 사용자 입력을 선점하면 안 된다.

- approval choice
- direction choice
- direction free text
- clarification prompt
- mutation confirmation
- future verification prompt

Orchestrator는 이 상태에서 다음만 수행한다.

- prompt 출력
- 입력 문자열 수집
- slash meta command 처리. meta command는 ReAct loop로 전달하지 않는다.
- ReAct loop로 response 전달

## Implemented Changes

1. `react.Loop`에 input ownership snapshot을 제공한다.

예시:

```go
type InputOwner string

const (
    InputOwnerOrchestrator InputOwner = "orchestrator"
    InputOwnerReactChoice  InputOwner = "react_choice"
    InputOwnerReactText    InputOwner = "react_text"
    InputOwnerApproval     InputOwner = "approval"
)

func (l *Loop) InputOwner() InputOwner
```

2. `orchestrator.handleAgentInputRequest`는 incident guidance prompt를 실행하지 않는다. pending offer가 있으면 해당 prompt의 입력을 선점하지 않도록 폐기한다.

3. `orchestrator.handleAgentInputRequest`는 ReAct-owned free-text prompt 중에도 slash meta command를 orchestrator에서 처리한다. 출력형 meta command는 같은 input prompt를 다시 표시하고, `/clear`/`/reset`처럼 active agent를 제거하는 command는 대기 중인 loop를 정리한다. meta 처리 결과를 빈 `UserInputResponse`로 보내지 않는다.

4. `orchestrator.handleAgentChoiceRequest`는 ReAct continuation choice에서만 runbook 검색 option을 추가한다. approval choice나 free-text input에는 추가하지 않는다.

5. Runbook 검색은 별도 y/n prompt가 아니라 ReAct continuation choice의 명시적 option으로만 노출한다.

6. Side-flow decline 또는 no-result가 빈 `UserInputResponse{Query: ""}`를 ReAct-owned prompt에 보내는 경로를 제거했다.

## Remaining Limits

- 일반 prompt에서 pending incident offer는 자동 표시되지 않고 폐기된다. 의도적인 선택이다. runbook 검색은 continuation choice에 명시적으로 표시될 때만 실행한다.
- `InputOwner`는 runtime-visible state snapshot이지 전체 state machine 재설계는 아니다. `07_explicit_state_machine.md`에서 implicit flag 축소를 별도로 다룬다.

## Acceptance Criteria

- `next_directions`에서 "직접 다른 방향 입력" 선택 후 사용자가 입력한 free text가 반드시 `waitForDirectionText`로 전달된다.
- ReAct-owned input 중 slash meta command는 orchestrator가 처리하고 loop에 diagnostic directive로 전달하지 않는다.
- incident guidance pending 상태여도 ReAct free-text prompt가 먼저 처리된다.
- `감지된 문제에 대해 해결 방법을 찾아볼까요? (y/n):` 같은 out-of-band prompt가 ReAct-owned input 중간에 나오지 않는다.
- runbook 검색은 continuation choice list에 명시적으로 표시될 때만 실행된다.

## Regression Scenarios

1. `next_directions` -> `직접 다른 방향 입력` -> `네임스페이스가 달라`
   - Expected: free text continuation으로 들어간다.

2. `next_directions` list에 runbook option이 있음 -> 사용자가 다른 접근 선택
   - Expected: runbook 검색은 실행되지 않고 pending offer는 폐기된다.

3. approval prompt 중 incident evidence pending
   - Expected: approval choice만 처리된다.

## Risks

- 기존 incident guidance 자동 제안 UX가 줄어든다.
- 대신 의도하지 않은 진단 선점이 사라지고, 사용자 선택 기반으로 바뀐다.
