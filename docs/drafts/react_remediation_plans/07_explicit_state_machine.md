# Plan 07: Explicit State Machine

## 목적

이 문서는 `internal/react`의 implicit state machine을 명시적 state machine으로 바꾸기 전에 반드시 정의하고 점검해야 할 내용을 고정한다.

목표는 단순히 enum을 늘리는 것이 아니다. 목표는 ReAct loop가 매 순간 다음 중 무엇을 기다리는지 하나의 상태 계약으로 설명되게 만드는 것이다.

- model structured output
- tool execution
- user approval
- user continuation choice
- user free text
- mutation verification evidence
- mutation verification result
- resource guide lookup/result
- final report

## 왜 07이 필요한가

현재 `react.Loop`에는 `State` enum이 있지만 실제 제어 의미는 여러 필드 조합에서 나온다.

예:

- `StateRunning + requirementAnalysis == nil`
- `StateRunning + phaseStepState == nil`
- `StateRunning + guideStepState != nil`
- `StateRunning + finalReportRequested == true`
- `StateRunning + guidedPhaseProgressRequested == true`
- `StateRunning + pendingMutationVerification != nil`
- `StateRunning + pendingMutationVerification.AwaitingResult == true`
- `StateRunning + mutationContinuationRequired == true`
- `StateDone + pendingFinalReport != nil`
- `StateWaitingDirectionChoice + pendingDirectionPrompt != nil`
- `StateWaitingDirectionText + inputOwner == react_text`

이 구조는 "현재 무엇을 기다리는가"가 하나의 값으로 드러나지 않는다. 그래서 작은 수정이 다른 플래그와 충돌하면 다음 문제가 생긴다.

- final report를 받아야 할 때 action을 다시 실행한다.
- guide step 완료 후 phase_progress 또는 final_report 유도가 누락된다.
- mutation 후 verification evidence가 부족한데 종료한다.
- ReAct가 사용자 입력을 기다리는데 orchestrator side-flow가 끼어든다.
- RAG/runbook이 현재 phase와 무관하게 제안된다.
- LLM에게는 correction을 보냈지만 runtime 상태는 여전히 다른 것을 기다린다.

## 현재 코드 기준 상태 인벤토리

### Coarse State

`internal/react/loop.go`:

```go
type State int

const (
    StateIdle State = iota
    StateRunning
    StateWaitingApproval
    StateWaitingDirectionChoice
    StateWaitingDirectionText
    StateDone
    StateExited
)
```

이 값은 외부 loop lifecycle에는 유용하지만, `StateRunning` 내부의 세부 상태를 표현하지 못한다.

### Request/Phase 상태

관련 필드:

- `originalQuery`
- `requirementAnalysis`
- `requestContext`
- `resourceClassification`
- `phaseStepState`

현재 의미:

- `requirementAnalysis == nil`: 다음 model response는 `requirement_analysis`여야 한다.
- `requirementAnalysis != nil && phaseStepState == nil`: 다음 model response는 `phase_plan`이어야 한다.
- `phaseStepState != nil`: 현재 phase의 completion condition에 따라 action 또는 `phase_progress`가 가능하다.

점검 필요:

- requirement analysis 수용 후 phase plan으로만 넘어가는가?
- phase plan 수용 전 action/final_report/answer가 deterministic하게 차단되는가?
- phase plan의 `allowed_next`가 실제 runtime 전이와 일치하는가?
- mutation/remediation phase가 있으면 verification phase가 plan에 포함되는가?
- built-in resource는 resource guide phase를 포함하지 않는가?

### Guide/RAG 상태

관련 필드:

- `resourceGuideInjected`
- `resourceGuideEvidence`
- `resourceGuideQueries`
- `guideStepState`
- `finalReportRequested`
- `guidedPhaseProgressRequested`
- `pendingResponseDirective`

현재 의미:

- `guidance_lookup` phase에서 CRD 확인 후 `resource_guide_lookup`만 허용한다.
- guide 검색 성공 시 `guideStepState`를 만들고 `guided_diagnosis` phase로 진입한다.
- guide step이 모두 완료되면 runtime이 `phase_progress` 또는 `final_report`를 유도한다.
- guide 검색 실패는 unavailable observation으로 기록하고 일반 phase 진행으로 돌아간다.

점검 필요:

- guide lookup은 CRD primary target에만 가능한가?
- `guidance_lookup` phase에서 action이 차단되는가?
- guide step 완료와 top-level phase completion이 섞이지 않는가?
- guide 완료 후 `guidedPhaseProgressRequested`와 `finalReportRequested`가 동시에 true가 될 수 없는가?
- runbook/incident guidance가 ReAct phase와 독립적으로 끼어들지 않는가?

### Mutation 상태

관련 필드:

- `pendingCalls`
- `skipPermissions`
- `pendingMutationVerification`
- `mutationContinuationRequired`
- `pendingResponseDirective`

현재 의미:

- mutating call은 approval 대상이다.
- mutation 성공 후 `pendingMutationVerification`이 생기면 verification read-only action만 허용한다.
- evidence requirement가 모두 만족되면 `mutation_verification_result`만 허용한다.
- result가 `progressing` 또는 `unresolved`이면 `mutationContinuationRequired`가 켜지고 추가 ReAct action을 요구한다.

점검 필요:

- approval이 verification을 우회하지 않는가?
- `yes_and_dont_ask_me_again`도 verification은 우회하지 않는가?
- target 없는 successful `kubectl apply -f ...` 정책이 state machine에서 별도 예외로 명시되는가?
- 여러 mutation이 하나의 목표를 이룰 때 verification requirement가 누적되는가?
- verification evidence가 unresolved인데 conclusive final report로 닫히지 않는가?

### User Input 상태

관련 필드:

- `inputOwner`
- `pendingFinalReport`
- `pendingNextDirections`
- `pendingDirectionPrompt`
- `StateWaitingApproval`
- `StateWaitingDirectionChoice`
- `StateWaitingDirectionText`

현재 의미:

- approval prompt는 ReAct approval이 소유한다.
- direction choice는 ReAct continuation choice가 소유한다.
- free-text continuation은 ReAct text input이 소유한다.
- orchestrator meta command는 orchestrator가 처리하되 ReAct에 빈 입력을 보내지 않아야 한다.
- incident runbook 검색은 continuation choice의 명시적 option일 때만 실행한다.

점검 필요:

- ReAct-owned input 중 incident prompt가 끼어들 수 없는가?
- `/clear`, `/help`, `/readonly` 같은 meta command 처리 후 같은 prompt가 유지되는가?
- free text 선택 후 입력이 `waitForDirectionText`로 전달되는가?
- runbook option은 choice list에만 추가되고 free-text prompt에는 추가되지 않는가?

### Runtime Summary 상태

최근 추가된 `internal/react/runtime_state_anchor.go`는 LLM에게 다음 값을 보여준다.

- `loop_state`
- `current_phase`
- `active_nested_state`
- `active_gate`
- `required_next_output`
- `forbidden_next_outputs`
- `pending_runtime_directive`

이것은 07 구현의 전 단계로 유용하지만, 아직 state machine 자체는 아니다.

중요한 구분:

- `runtimeStateAnchor`는 LLM attention aid다.
- 07의 목표는 runtime transition source of truth를 만드는 것이다.
- 따라서 anchor는 새 state snapshot에서 파생되어야 하고, 별도 추론 로직으로 남으면 안 된다.

## 사전에 확정해야 할 용어

### Control State

`ControlState`는 loop가 현재 누구의 어떤 출력을 기다리는지 표현한다.

예:

```go
type ControlState string
```

이 값은 `StateRunning` 내부의 hidden mode를 대체한다.

### Gate

`Gate`는 현재 상태에서 허용되는 next output/action을 제한하는 deterministic rule이다.

예:

- requirement analysis required
- phase plan required
- resource guide lookup required
- mutation verification evidence required
- mutation verification result required
- final report required

Gate는 correction message를 만들 수 있지만, correction 자체가 gate가 되어서는 안 된다.

### Runtime Snapshot

`RuntimeSnapshot`은 현재 상태를 출력/디버깅/anchor 생성에 사용하는 read-only view다.

요구사항:

- 하나의 snapshot만 보면 현재 loop가 무엇을 기다리는지 알 수 있어야 한다.
- orchestrator input ownership도 snapshot에서 유도 가능해야 한다.
- LLM anchor는 snapshot에서 파생되어야 한다.

### Transition Event

`RuntimeEvent`는 상태 변화를 일으키는 명시적 사건이다.

예:

- `UserQueryAccepted`
- `RequirementAnalysisAccepted`
- `PhasePlanAccepted`
- `ToolCallsProposed`
- `ApprovalRequested`
- `ApprovalGranted`
- `ToolExecutionCompleted`
- `MutationVerificationEvidenceSatisfied`
- `MutationVerificationResultAccepted`
- `GuideLookupRequested`
- `GuideLookupUnavailable`
- `GuideStepsCompleted`
- `PhaseProgressAccepted`
- `FinalReportAccepted`
- `ContinuationChoiceRequested`
- `ContinuationTextReceived`

## 제안 ControlState

초기 구현은 너무 많은 state를 만들지 말고, 실제 충돌이 발생한 축만 명시한다.

```go
type ControlState string

const (
    ControlIdle ControlState = "idle"
    ControlAwaitingUserQuery ControlState = "awaiting_user_query"
    ControlAwaitingRequirementAnalysis ControlState = "awaiting_requirement_analysis"
    ControlAwaitingPhasePlan ControlState = "awaiting_phase_plan"
    ControlAwaitingModelStep ControlState = "awaiting_model_step"
    ControlAwaitingResourceGuideLookup ControlState = "awaiting_resource_guide_lookup"
    ControlAwaitingGuidedDiagnosisStep ControlState = "awaiting_guided_diagnosis_step"
    ControlAwaitingGuidedPhaseProgress ControlState = "awaiting_guided_phase_progress"
    ControlAwaitingFinalReport ControlState = "awaiting_final_report"
    ControlAwaitingNextDirections ControlState = "awaiting_next_directions"
    ControlAwaitingApproval ControlState = "awaiting_approval"
    ControlExecutingTool ControlState = "executing_tool"
    ControlAwaitingMutationVerificationEvidence ControlState = "awaiting_mutation_verification_evidence"
    ControlAwaitingMutationVerificationResult ControlState = "awaiting_mutation_verification_result"
    ControlAwaitingMutationContinuation ControlState = "awaiting_mutation_continuation"
    ControlAwaitingContinuationChoice ControlState = "awaiting_continuation_choice"
    ControlAwaitingContinuationText ControlState = "awaiting_continuation_text"
    ControlComplete ControlState = "complete"
    ControlExited ControlState = "exited"
)
```

주의:

- `ControlAwaitingUserQuery`는 현재 코드의 `StateIdle`/`StateDone`과 대응한다. 현재 loop는 완료 상태에서 멈춰 있지 않고 즉시 새 사용자 query prompt를 연다.
- `ControlAwaitingGuidedDiagnosisStep`는 top-level phase가 아니다. `guided_diagnosis` phase 내부 nested state다. 이 상태는 `guide_progress` object만 기다린다는 뜻이 아니라, 다음 guide step을 진행하는 `action` 또는 관찰 후 `guide_progress`를 기다린다는 뜻이다.
- `ControlAwaitingMutationVerificationEvidence`도 top-level phase가 아니다. mutation lifecycle gate다.
- `ControlAwaitingMutationContinuation`은 `progressing`/`unresolved` result 이후 계속 진단해야 하는 상태다.
- `ControlExecutingTool`은 `dispatchToolCalls` 동기 실행 구간에서 published snapshot에 표시된다.
- `ControlComplete`는 "요청이 막 끝났다"는 전이 이벤트에 가깝다. 현재 코드의 안정 상태로는 `StateDone`이 곧 `ControlAwaitingUserQuery`로 해석된다.

## 제안 RuntimeState

초기 구조는 기존 필드를 한 번에 모두 옮기지 않는다. 먼저 snapshot/context 타입을 추가하고, 이후 source of truth를 옮긴다.

```go
type RuntimeState struct {
    Control ControlState

    Request *requestContext
    Requirement *requirementAnalysis
    ResourceClassification *resourceClassification

    Phase *phaseStepState
    Guide *guideStepState

    PendingCalls []PendingCall
    PendingApproval []PendingCall

    PendingMutationVerification *pendingMutationVerification
    MutationContinuationRequired bool

    PendingFinalReport *finalReport
    PendingNextDirections *nextDirections
    PendingDirectionPrompt *directionPromptState

    PendingDirective string
}
```

초기에는 이 타입이 기존 필드의 projection이어도 된다. 단, 다음 조건을 만족해야 한다.

- projection 함수는 단일 위치에 있어야 한다.
- `runtimeStateAnchor`, `InputOwner`, 디버그 출력은 projection 결과만 사용해야 한다.
- 같은 상태를 여러 함수에서 각자 추론하면 안 된다.

## 상태 우선순위 정의

여러 플래그가 동시에 존재할 수 있으므로 우선순위를 명시해야 한다. 이 순서가 없으면 "final_report_requested와 mutation verification이 동시에 있으면 무엇이 우선인가" 같은 버그가 다시 생긴다.

제안 우선순위:

1. `StateExited` -> `ControlExited`
2. `toolDispatchInProgress` -> `ControlExecutingTool`
3. `StateWaitingApproval` -> `ControlAwaitingApproval`
4. `StateWaitingDirectionChoice` -> `ControlAwaitingContinuationChoice`
5. `StateWaitingDirectionText` -> `ControlAwaitingContinuationText`
6. `StateIdle || StateDone` -> `ControlAwaitingUserQuery`
7. `pendingMutationVerification != nil && AwaitingResult` -> `ControlAwaitingMutationVerificationResult`
8. `pendingMutationVerification != nil` -> `ControlAwaitingMutationVerificationEvidence`
9. `mutationContinuationRequired` -> `ControlAwaitingMutationContinuation`
10. `guidedPhaseProgressRequested` -> `ControlAwaitingGuidedPhaseProgress`
11. `finalReportRequested` -> `ControlAwaitingFinalReport`
12. `pendingFinalReport != nil && pendingNextDirections == nil && pendingDirectionPrompt == nil` -> `ControlAwaitingNextDirections`
13. `requirementAnalysis == nil` -> `ControlAwaitingRequirementAnalysis`
14. `phaseStepState == nil` -> `ControlAwaitingPhasePlan`
15. `guidance_lookup phase && resourceGuideInjected == false && CRD` -> `ControlAwaitingResourceGuideLookup`
16. `guideStepState != nil && remaining guide steps exist` -> `ControlAwaitingGuidedDiagnosisStep`
17. default -> `ControlAwaitingModelStep`

이 우선순위는 현재 runtime enforcement 순서와 맞아야 한다.

특히 mutation verification은 final report나 phase progress보다 우선해야 한다. Kubernetes 변경 후 검증 obligation이 남아 있으면 어떤 보고도 먼저 나가면 안 된다.

현재 코드와 맞추기 위한 중요한 보정:

- `StateDone`은 stable complete state가 아니라 다음 사용자 query를 기다리는 상태다.
- inconclusive `final_report` 뒤에는 바로 continuation choice가 아니라, 먼저 model의 `next_directions` structured output을 기다린다.
- guided diagnosis는 다음 guide step을 수행하는 action을 허용한다. `guide_progress`는 step 완료 기록이지 유일한 next output이 아니다.

## 전이 테이블

### Query 시작

| From | Event | To | Notes |
| --- | --- | --- | --- |
| `ControlAwaitingUserQuery` | user query | `ControlAwaitingRequirementAnalysis` | `startQuery`가 query context를 초기화한다. |
| `ControlIdle` | first query exists | `ControlAwaitingRequirementAnalysis` | CLI initial query도 같은 초기화 경로를 탄다. |
| `ControlComplete` | loop tick | `ControlAwaitingUserQuery` | 현재 코드에서는 별도 tick 없이 `StateDone`이 곧 user query prompt로 처리된다. |

### Requirement / Phase

| From | Event | To | Notes |
| --- | --- | --- | --- |
| `ControlAwaitingRequirementAnalysis` | valid `requirement_analysis` | `ControlAwaitingPhasePlan` | request context와 resource classification을 만든다. |
| `ControlAwaitingRequirementAnalysis` | invalid/plain/action | same | deterministic correction. |
| `ControlAwaitingPhasePlan` | valid `phase_plan` | `ControlAwaitingModelStep` | plan gate 통과 후 action/phase_progress 가능. |
| `ControlAwaitingPhasePlan` | invalid plan | same | missing verification phase, bad allowed_next, invalid guidance phase 차단. |

### Resource Guide

| From | Event | To | Notes |
| --- | --- | --- | --- |
| `ControlAwaitingModelStep` | enter `guidance_lookup` phase for CRD | `ControlAwaitingResourceGuideLookup` | action 금지. |
| `ControlAwaitingResourceGuideLookup` | valid `resource_guide_lookup` | `ControlAwaitingGuidedDiagnosisStep` or `ControlAwaitingModelStep` | guide found면 guided diagnosis step 진행, unavailable이면 일반 진행. |
| `ControlAwaitingGuidedDiagnosisStep` | action observes useful guide evidence, remaining exists | same | 다음 guide step 진행. |
| `ControlAwaitingGuidedDiagnosisStep` | all guide steps complete | `ControlAwaitingGuidedPhaseProgress` | top-level `phase_progress` 요구. |
| `ControlAwaitingGuidedPhaseProgress` | valid `phase_progress` | `ControlAwaitingModelStep` or `ControlAwaitingFinalReport` | allowed_next에 따라 이동. |

### Mutation

| From | Event | To | Notes |
| --- | --- | --- | --- |
| `ControlAwaitingModelStep` | mutating action proposed | `ControlAwaitingApproval` | read-only mode면 mutation lifecycle 시작 전 차단. |
| `ControlAwaitingApproval` | approved | `ControlExecutingTool` | 승인된 command만 실행. |
| `ControlExecutingTool` | successful mutation with targets | `ControlAwaitingMutationVerificationEvidence` | requirement 생성/merge. |
| `ControlExecutingTool` | successful targetless `kubectl apply -f ...` with all apply results successful | `ControlAwaitingModelStep` | 정책상 apply output 자체를 evidence로 보고 추가 generic verification 없음. |
| `ControlAwaitingMutationVerificationEvidence` | read-only evidence collected, remaining exists | same | 다음 evidence 요구. |
| `ControlAwaitingMutationVerificationEvidence` | all requirements satisfied | `ControlAwaitingMutationVerificationResult` | 바로 final report 금지. |
| `ControlAwaitingMutationVerificationResult` | `resolved` | `ControlAwaitingModelStep` | phase_progress/final_report 가능. |
| `ControlAwaitingMutationVerificationResult` | `progressing` | `ControlAwaitingMutationContinuation` | wait/recheck 또는 다음 observation. |
| `ControlAwaitingMutationVerificationResult` | `unresolved` | `ControlAwaitingMutationContinuation` | 다른 진단/수정 접근. |
| `ControlAwaitingMutationContinuation` | useful action result | `ControlAwaitingModelStep` or `ControlAwaitingMutationVerificationEvidence` | 추가 mutation이면 verification 재진입. |

### Final / Continuation

| From | Event | To | Notes |
| --- | --- | --- | --- |
| `ControlAwaitingModelStep` | valid conclusive `final_report` without investigation choices | `ControlComplete` -> `ControlAwaitingUserQuery` | evidence grounding 필요. 현재 코드에서는 `StateDone`으로 가고 다음 loop tick에서 prompt를 연다. |
| `ControlAwaitingModelStep` | valid conclusive `final_report` with problematic related resources | `ControlAwaitingContinuationChoice` | 추가 조사 choice prompt를 연다. |
| `ControlAwaitingFinalReport` | valid conclusive `final_report` without investigation choices | `ControlComplete` -> `ControlAwaitingUserQuery` | requested final report만 허용. |
| `ControlAwaitingFinalReport` | valid conclusive `final_report` with problematic related resources | `ControlAwaitingContinuationChoice` | `promptProblematicResourceInvestigation` 경로. |
| `ControlAwaitingFinalReport` | inconclusive `final_report` | `ControlAwaitingNextDirections` | model에게 `next_directions` structured output을 요구한다. |
| `ControlAwaitingNextDirections` | valid `next_directions` | `ControlAwaitingContinuationChoice` | 사용자 choice prompt를 연다. |
| `ControlAwaitingContinuationChoice` | user picks option | `ControlAwaitingModelStep` or `ControlAwaitingContinuationText` | free text면 text state로 이동. |
| `ControlAwaitingContinuationText` | user text | `ControlAwaitingPhasePlan` or `ControlAwaitingModelStep` | 새 방향을 반영해 계속. |

## 구현 전 필수 점검 체크리스트

### 1. 상태 조합 inventory

현재 `Loop` 필드 조합을 모두 표로 만든다.

필수 항목:

- `State`
- `requirementAnalysis`
- `phaseStepState`
- `guideStepState`
- `resourceGuideInjected`
- `finalReportRequested`
- `guidedPhaseProgressRequested`
- `pendingResponseDirective`
- `pendingFinalReport`
- `pendingNextDirections`
- `pendingDirectionPrompt`
- `pendingMutationVerification`
- `mutationContinuationRequired`
- `pendingCalls`
- `inputOwner`

각 조합마다 다음을 기록한다.

- 현재 의미
- 다음에 허용되는 model output
- 금지되는 model output
- user input owner
- deterministic gate 위치
- 상태 전이 함수 후보

### 2. Enforcement 순서 점검

`runIteration`의 처리 순서와 `ControlState` 우선순위가 일치해야 한다.

현재 순서에서 반드시 확인할 것:

- requirement analysis 강제는 가장 앞에 있는가?
- phase plan 강제는 action보다 앞에 있는가?
- mutation verification gate는 final report/phase progress보다 앞에 있는가?
- guide progress consume과 final report consume 순서가 trailing call을 잃지 않는가?
- resource guide lookup gate는 action dispatch보다 앞에 있는가?
- requested directive gate는 final report/next directions consume 전에 동작하는가?

### 3. 상태 전이 owner 점검

상태 변경은 흩어진 field assignment가 아니라 transition 함수로 수렴해야 한다.

초기에는 모든 assignment를 바로 제거하지 않는다. 대신 다음 지점부터 transition wrapper를 도입한다.

- `startQuery`
- `consumeRequestContext`
- `consumePhasePlan`
- `consumePhaseProgress`
- `handleRequestedResourceGuideLookup`
- `injectResourceGuideAttempt`
- `injectResourceGuideUnavailable`
- `consumeGuideProgress`
- `requestPostGuideCompletionDirective`
- `requestFinalReportFromModel`
- `consumeFinalReport`
- `requestNextDirectionsFromModel`
- `handleDirectionChoice`
- `waitForDirectionText`
- `requestApproval`
- `handleApproval`
- `dispatchToolCalls`
- `trackMutationVerification`
- `consumeMutationVerificationResult`

각 지점에서 변경 전후 snapshot을 비교할 수 있어야 한다.

### 4. Input ownership 점검

`InputOwner()`는 별도 atomic flag 추론이 아니라 `ControlState`에서 파생되어야 한다.

매핑:

| ControlState | InputOwner |
| --- | --- |
| `ControlAwaitingApproval` | `approval` |
| `ControlAwaitingContinuationChoice` | `react_choice` |
| `ControlAwaitingContinuationText` | `react_text` |
| `ControlAwaitingUserQuery` | `orchestrator` |
| others | `orchestrator` |

점검:

- orchestrator가 `react_choice`/`react_text` 상태에서 incident prompt를 띄우지 않는가?
- slash meta command 처리 후 loop에 빈 입력을 보내지 않는가?
- `/clear`/`/reset`이 active loop cleanup을 명확히 수행하는가?

### 5. LLM anchor 점검

`runtimeStateAnchor`는 현재 별도 추론을 갖고 있다. 07 구현 후에는 다음처럼 바뀌어야 한다.

```go
snapshot := l.RuntimeSnapshot()
anchor := snapshot.AnchorText()
```

점검:

- anchor의 `active_gate`가 `ControlState`와 불일치하지 않는가?
- `required_next_output`이 실제 gate와 불일치하지 않는가?
- `forbidden_next_outputs`가 과하게 넓어 정상 전이를 막지 않는가?
- pending directive와 control state가 서로 다른 요구를 하지 않는가?

### 6. Mutation lifecycle 점검

03의 정책을 state machine에 반영해야 한다.

필수 invariant:

- mutation 성공 후 verification obligation이 있으면 final report 금지.
- verification evidence가 모두 수집되기 전 `mutation_verification_result` 금지.
- evidence가 모두 수집된 뒤에는 `mutation_verification_result`만 허용.
- `resolved`만 conclusive final report로 이어질 수 있다.
- `progressing`/`unresolved`는 추가 ReAct action으로 이어져야 한다.
- targetless successful `kubectl apply -f ...` 예외는 명시적 state/event로 표현한다.

### 7. RAG boundary 점검

05의 정책을 state machine에 반영해야 한다.

필수 invariant:

- resource guide는 `ControlAwaitingResourceGuideLookup`에서만 실행된다.
- CRD discovery 전 resource guide lookup 금지.
- built-in/unknown resource에서 guide phase 금지.
- incident runbook은 ReAct input state를 선점하지 않는다.
- runbook 검색 실패/invalid/unknown은 no result로 종료하고 fallback하지 않는다.

### 8. Phase plan contract 점검

02의 정책을 state machine에 반영해야 한다.

필수 invariant:

- phase plan은 flat graph다. 상위/하위 phase 구조를 만들지 않는다.
- guide step과 mutation verification은 phase graph 내부 하위 phase가 아니라 runtime nested state다.
- mutation/remediation execution phase가 있으면 verification phase가 필요하다.
- allowed_next는 forward-only이며 completed phase로 역전이하지 않는다.
- runtime이 임의로 phase를 삽입하기보다 invalid plan을 reject하고 corrected plan을 받는다.

### 9. Deterministic gate 점검

06의 정책을 state machine에 반영해야 한다.

필수 invariant:

- 안전/권한/namespace/mutation verification은 prompt correction만으로 처리하지 않는다.
- gate 결과는 `allow`, `block`, `correction`, `terminal` 중 하나로 표현한다.
- 같은 gate가 반복 correction 실패 시 terminal state로 이동한다.
- gate가 block한 상태와 `ControlState`가 서로 모순되지 않는다.

## 구현 순서

## Implementation Status

현재 반영된 범위:

- `internal/react/runtime_state.go`에 `ControlState`와 `RuntimeSnapshot` projection을 추가했다.
- `RuntimeSnapshot()`은 현재 `Loop` 필드 조합을 읽어 문서의 우선순위에 맞는 `ControlState`를 계산한다.
- `runtimeStateAnchor`는 더 이상 자체적으로 active gate를 추론하지 않고 `RuntimeSnapshot`의 `ActiveGate`, `RequiredNextOutput`, `ForbiddenNextOutputs`, `NestedStateName`을 사용한다.
- `ControlAwaitingUserQuery`, `ControlAwaitingNextDirections`, `ControlAwaitingGuidedDiagnosisStep`를 현재 코드 전이에 맞게 projection에 반영했다.
- `DerivedInputOwner()`를 snapshot에 추가했고, `InputOwner()`는 published `RuntimeSnapshot`을 우선 읽는다.
- `setInputOwner(owner)`는 인자를 받지 않는 `refreshInputOwner()`로 정리했다. owner는 항상 `RuntimeSnapshot.Control`에서 파생한다.
- `addMessage()` 직전에 snapshot을 publish해 orchestrator goroutine이 `Loop` 내부 state를 직접 읽지 않게 했다.
- raw input을 `choice_number`, `approval`, `slash_meta`, `free_text`, `empty`로 분류하고, `ControlState + input kind`를 `DecideInputDispatch`로 판단한다.
- continuation choice는 slash meta를 받지 않고, continuation free-text에서만 slash meta를 orchestrator가 처리한다.
- `next_directions_required`는 structured output과 plain answer 양쪽에서 deterministic gate로 강제한다.
- impossible state audit hook을 run loop 공통 경로에 추가해 mutation verification과 final/phase requested flags 충돌, direction prompt 불일치, approval pending call 누락을 내부 invariant 위반으로 드러낸다.
- `ControlExecutingTool`은 `dispatchToolCalls` 동기 실행 구간에서 snapshot에 표시되며, 진입/종료 시 명시적으로 snapshot을 publish한다.
- requested structured output gate는 raw requested flag가 아니라 `RuntimeSnapshot.Control`을 기준으로 판단한다.
- orchestrator input dispatch, waiting-state audit, `next_directions` plain-answer gate 회귀 테스트를 추가했다.

아직 의도적으로 남긴 범위:

- `inputOwner` atomic field는 fallback compatibility로 남아 있다. source of truth는 published snapshot으로 이동했지만, 완전 제거는 후속 cleanup이다.
- `finalReportRequested`, `guidedPhaseProgressRequested`, `pendingMutationVerification`, `mutationContinuationRequired`, `pendingResponseDirective`는 아직 source field로 남아 있다.
- `pendingResponseDirective`는 중복 append를 막지만, 아직 완전히 `ControlState -> directive` 파생 구조로 바뀐 것은 아니다.

### Step 1. Snapshot만 추가

목표:

- 기존 동작을 바꾸지 않고 `RuntimeSnapshot()`을 추가한다.
- `runtimeStateAnchor`가 snapshot을 사용하게 한다.
- `InputOwner()`도 snapshot 기반으로 바꾸기 위한 준비를 한다.

완료 조건:

- snapshot 하나로 current control state를 설명할 수 있다.
- 기존 flags와 snapshot control state의 mismatch를 로그/test에서 볼 수 있다.

### Step 2. ControlState projection 고정

목표:

- 위의 상태 우선순위 함수 하나를 만든다.
- 흩어진 `runtimeActiveGate()`류 추론을 projection에 모은다.

완료 조건:

- active gate, required next output, forbidden outputs가 projection에서 나온다.
- 기존 anchor와 input owner가 같은 projection을 참조한다.

### Step 3. Transition audit hooks 추가

목표:

- 상태 전이가 일어나는 주요 지점에 `before/after` audit helper를 넣는다.
- 아직 모든 assignment를 제거하지 않아도 된다.

완료 조건:

- 불가능한 조합을 감지할 수 있다.
- 예: `pendingMutationVerification != nil && finalReportRequested == true` 같은 조합을 발견하면 correction보다 먼저 내부 error로 드러난다.

### Step 4. Input owner를 ControlState에서 파생

목표:

- `inputOwner` atomic flag를 compatibility layer로 낮춘다.
- 최종적으로는 `ControlState`에서 input owner를 계산한다.

완료 조건:

- approval/direction choice/free text에서 orchestrator side-flow가 끼어들 수 없다.
- slash meta command 처리 후 ReAct prompt가 유지된다.

### Step 5. Mutation verification state를 ControlState로 승격

목표:

- `pendingMutationVerification`의 존재 여부가 아니라 `ControlState`가 verification evidence/result 상태를 대표하게 한다.

완료 조건:

- evidence required 상태에서 action whitelist가 명확하다.
- result required 상태에서 `mutation_verification_result` 외 출력이 차단된다.
- progressing/unresolved 후 continuation 상태가 명확하다.

### Step 6. Guide/final report request flags 정리

목표:

- `guidedPhaseProgressRequested`와 `finalReportRequested`를 ControlState로 대체하거나 최소한 derived flag로 만든다.

완료 조건:

- guide 완료 후 다음 요구가 하나만 존재한다.
- phase progress와 final report 요구가 동시에 켜지지 않는다.
- requested directive는 state의 설명이지 별도 source of truth가 아니다.

### Step 7. Redundant flag 제거

목표:

- compatibility가 필요 없어진 flag를 제거한다.

삭제 후보:

- `finalReportRequested`
- `guidedPhaseProgressRequested`
- 일부 `pendingResponseDirective` source-of-truth 사용
- `inputOwner` 직접 관리

삭제 전 조건:

- 관련 transition test가 충분해야 한다.
- architecture 문서가 새 state model을 설명해야 한다.

## Regression Scenario Matrix

### Requirement/phase

1. 첫 응답이 action
   - Expected: `ControlAwaitingRequirementAnalysis`, action reject.

2. requirement accepted 후 phase_plan 없이 final_report
   - Expected: `ControlAwaitingPhasePlan`, final_report reject.

3. mutation execution phase가 있지만 verification phase 없음
   - Expected: phase plan reject.

### Mutation

4. configmap create 성공 후 즉시 "완료" 답변
   - Expected: `ControlAwaitingMutationVerificationEvidence`, plain answer reject.

5. configmap create 성공 후 다른 namespace에서 get
   - Expected: verification action reject.

6. verification evidence 모두 수집 후 action 추가
   - Expected: `ControlAwaitingMutationVerificationResult`, action reject.

7. verification result `progressing`
   - Expected: `ControlAwaitingMutationContinuation`, final_report reject.

8. targetless `kubectl apply -f file.yaml` 성공
   - Expected: apply output success policy로 generic verification을 만들지 않음.

### Guide/RAG

9. built-in pod 문제에서 guidance_lookup phase 포함
   - Expected: phase plan reject.

10. CRD guidance_lookup phase에서 kubectl action 먼저 출력
    - Expected: `ControlAwaitingResourceGuideLookup`, action reject.

11. guide step 모두 완료 후 action 출력
    - Expected: `ControlAwaitingGuidedPhaseProgress`, action reject.

12. guided diagnosis 중 remaining guide step이 있음
    - Expected: `ControlAwaitingGuidedDiagnosisStep`, next guide step을 진행하는 action 또는 관찰 후 guide_progress 허용.

13. runbook 검색 결과 Unknown detection
    - Expected: no usable runbook, ReAct loop에 임의 directive 주입 없음.

### User input

14. next_directions에서 직접 입력 선택 후 free text 입력
    - Expected: `ControlAwaitingContinuationText`, text가 ReAct로 전달.

15. free text prompt 중 `/help`
    - Expected: orchestrator meta 처리, 빈 ReAct input 전송 없음, prompt 유지.

16. approval prompt 중 incident offer pending
    - Expected: approval input만 처리.

## Acceptance Criteria

- 하나의 `RuntimeSnapshot` 또는 `ControlState` 값만 보고 현재 loop가 무엇을 기다리는지 설명할 수 있다.
- `runtimeStateAnchor`, `InputOwner`, deterministic gate 설명이 같은 snapshot에서 나온다.
- mutation verification, guide completion, final report request, continuation choice가 서로 다른 flag 조합이 아니라 명시적 state로 표현된다.
- 불가능한 flag 조합은 조용히 넘어가지 않고 audit에서 드러난다.
- ReAct-owned user input을 orchestrator side-flow가 선점하지 않는다.
- resource guide/runbook boundary가 state machine 안에서 분리된다.
- 기존 정상 동작을 바꾸지 않기 위해 snapshot -> projection -> transition 함수 -> flag 제거 순서로 진행한다.

## Non-Goals

- 이번 07 수정에서 모든 flag를 즉시 제거하지 않는다.
- phase plan을 hierarchical phase graph로 바꾸지 않는다.
- guide step을 top-level phase로 승격하지 않는다.
- mutation verification checker를 모든 리소스에 강제하지 않는다.
- incident guidance를 ReAct execution owner로 만들지 않는다.
- prompt wording만으로 state 문제를 해결했다고 보지 않는다.

## Risks

- 상태를 한 번에 바꾸면 기존 correction/gate 흐름을 깨뜨릴 수 있다.
- `StateRunning` 내부의 hidden mode가 많아서 projection 우선순위를 잘못 잡으면 정상 action을 차단할 수 있다.
- mutation verification과 guide/final report 요청이 동시에 걸린 경우 우선순위를 틀리면 안전 gate가 약해진다.
- input ownership을 잘못 옮기면 slash meta command 또는 approval UX가 깨질 수 있다.
- `pendingResponseDirective`를 너무 빨리 제거하면 context compaction 이후 모델 유도력이 약해질 수 있다.

## 구현 전 최종 질문

수정 전에 다음 결정을 코드로 고정해야 한다.

1. `ControlState`는 기존 `State`를 대체하지 않고 당분간 projection으로 둘 것인가?
   - 권장: 예. 먼저 projection/snapshot으로 도입한다.

2. `pendingResponseDirective`는 source of truth인가, state에서 파생되는 설명인가?
   - 권장: source of truth가 아니라 state에서 파생되는 설명으로 낮춘다. 단, compaction 대응을 위해 transition 기간에는 유지한다.

3. `guidedPhaseProgressRequested`와 `finalReportRequested`가 동시에 true가 되면 어떤 것이 우선인가?
   - 권장: mutation verification이 최우선이고, guide 완료 후에는 phase plan의 allowed_next에 따라 하나만 요구하도록 audit에서 금지한다.

4. `inputOwner` atomic은 제거할 것인가?
   - 권장: 바로 제거하지 않는다. 먼저 `ControlState` 기반 계산과 비교한 뒤 compatibility layer로 유지한다.

5. transition 함수는 모든 field assignment를 즉시 감쌀 것인가?
   - 권장: 아니다. 상태 위험도가 큰 지점부터 감싼다. mutation verification, guide completion, continuation input, approval부터 시작한다.

## 문서 업데이트 대상

07 구현이 진행되면 다음 문서도 같이 갱신해야 한다.

- `docs/architecture_orchestrator_react.md`
  - ReAct runtime state section
  - mutation verification state
  - resource guide state
  - user input ownership
- `docs/drafts/react_remediation_plans/00_overview.md`
  - 07 implementation status
- `docs/drafts/react_remediation_plans/01_user_input_ownership.md`
  - `InputOwner`가 `ControlState` projection으로 바뀌는 부분
- `docs/drafts/react_remediation_plans/03_mutation_lifecycle.md`
  - verification state naming
- `docs/drafts/react_remediation_plans/05_rag_boundary.md`
  - guide/runbook state boundary
- `docs/drafts/react_remediation_plans/06_deterministic_gates_vs_correction.md`
  - gate decision과 control state 연결
