# Plan 08: Turn Output Contract and Goal-Driven Execution

> 상태: Stage 0A와 Stage 1~6 코드 구현 완료.
>
> 관련 이슈: [#13 ReAct loop: consume/enforce가 섞인 gate pipeline에 output lock 도입](https://github.com/namgon-kim/kinx-k8s-assistant/issues/13)
>
> 이 계획은 기존 구조를 부분 보수하는 것을 전제로 하지 않는다. `coordinator.Loop`의 workflow
> field를 `coordinator.runtimeState` 하나로 묶고 `session.Aggregate[runtimeState]`가 revision과
> atomic replacement를 소유하는 단일 mutable source of truth로 전환한다. 단, 현재 지원하는
> native function calling, shim mode, phase/guidance/mutation lifecycle,
> approval, read-only, translation, streaming, continuation 기능은 손실 없이 보존해야 한다.

## 0. 구현 감사

2026-07-28 코드 대조 기준:

- Stage 0A와 Stage 1~6의 contract/session/flow/coordinator/protocol 연결은 구현돼 있다.
- Model output closed registry는 sentinel 포함 control 21개, 유효 control 20개, model-turn 14개,
  output kind 13개와 일치한다.
- Tool 실행은 immutable dispatch intent와 `dispatch_pending` attempt를 state commit한 뒤 시작하며,
  결과 commit이 불확실하면 같은 dispatch를 자동 재실행하지 않고 unknown observation과 mandatory
  verification으로 복구한다.
- 실행 이력의 source of truth는 indexed attempt/observation ledger 하나다. 구형
  `completedActions` mirror, rewind trim, duplicate projection은 제거됐다.
- Mutation verification은 single/ordered-chain/await-state 절차를 사용하고, RBAC/Forbidden은
  satisfied evidence가 아니라 access blocker로 취급한다.
- Request clear/reset은 execution ledger, dispatch intent, continuation을 함께 정리한다. Segment
  continuation은 request, lineage, ledger, budget을 유지한다.
- Plan 09 durable persistence는 이 계획과 분리된 미구현 범위다.

이 감사는 source 및 문서 정합성 확인이며 별도 build/test 실행 결과를 의미하지 않는다.

## 1. 목적

이 계획의 목적은 다음 네 가지를 하나의 실행 모델로 통합하는 것이다.

1. 모델 응답 전체를 상태 변경 전에 runtime 절차로 검증한다.
2. phase 이름이 아니라 목표와 완료 조건을 기준으로 step을 진행한다.
3. 목표를 달성하지 못한 시도와 관찰을 세션 이력으로 유지하고 다음 판단에 사용한다.
4. 새로운 정보가 기존 계획을 무효화하면 정해진 절차로 계획을 수정한다.

Runtime은 Kubernetes 관찰 결과의 의미나 모델 결론의 사실 여부를 재판정하지 않는다.
Runtime이 보장할 범위는 다음과 같다.

- 실제 action과 observation이 존재한다.
- model이 참조한 goal, step, criterion, evidence가 현재 세션에 존재한다.
- 현재 control state에서 허용된 output과 transition만 사용한다.
- step attempt budget과 correction budget을 구분해 적용한다.
- approval, read-only, mutation verification 같은 mandatory obligation을 우회하지 않는다.
- 상태 변경과 지속 기록이 정해진 순서로 수행된다.

LLM은 다음 판단을 담당한다.

- observation이 goal과 completion criterion을 만족하는지 해석한다.
- 현재 strategy가 충분하지 않을 때 다른 strategy를 선택한다.
- 새로운 evidence로 기존 remaining plan을 수정할 필요가 있는지 판단한다.
- 충분한 근거가 없을 때 inconclusive 결과나 사용자 선택지를 제안한다.

## 2. 핵심 설계 결정

### 2.1 Output lock은 LLM 검증 요청이 아니다

Output lock은 별도 LLM 호출이 아니다. 모델 호출 직전에 runtime state로 정책을 만들고,
응답을 받은 뒤 Go 코드로 output 종류와 조합을 비교한다.

```text
turn-entry snapshot
-> deterministic output policy
-> model response
-> protocol normalization
-> output envelope classification
-> deterministic policy validation
-> domain validation
-> state effects commit
-> external effects execution
```

### 2.2 일반 model step은 열린 정책을 사용한다

일반 진단 상태에서 자연어 progress text, 서로 독립적인 read-only observation, 새로운 strategy를
불필요하게 제한하지 않는다. Exclusive policy는 runtime이 특정 protocol event를 기다리는 상태에만
적용한다.

### 2.3 상태 전이는 명시적인 event로 요청한다

Model은 자유롭게 다음 상태를 선택할 수 있지만, 정해진 event를 사용해야 한다.

- 현재 step 계속: external action
- step 목표 달성 또는 중단: `step_result`
- top-level phase 완료: `phase_progress`
- remaining plan 변경: `phase_plan_revision`
- mutation 판정: `mutation_verification_result`
- 진단 종료: `final_report`
- 사용자 선택 요청: `next_directions`

### 2.4 반복 방지의 주 수단은 금지 목록이 아니라 실행 이력이다

Runtime은 phase 이름의 재사용이나 비슷한 command를 광범위하게 금지하지 않는다. 현재 goal,
completion criteria, 이전 attempts, observations, plan revisions를 compact projection으로 모델에
제공한다. Runtime의 강제 제동은 step attempt budget과 명백한 무진전 retry에 한정한다.

### 2.5 계획 변경은 safety obligation을 변경하지 못한다

Approval, mutation verification, read-only restriction, user-input ownership은 model-owned plan이
아니다. Plan revision으로 해당 obligation을 삭제하거나 완료 처리할 수 없다.

### 2.6 실행 이력은 session state에서 bounded projection으로 제공한다

LLM은 전체 대화나 모든 attempt를 매 turn 다시 받지 않는다. Runtime은 in-memory session state에서
현재 goal, active step, completion criteria, 최근 attempts, mandatory obligations를 선택해 bounded
projection으로 제공한다. Process 종료 이후의 durable 저장과 복구는
[`09_durable_session_persistence.md`](./09_durable_session_persistence.md)의 독립 범위다.

## 3. 현재 구조의 문제

### 3.1 consume과 enforce의 순서 의존성

현재 `coordinator.executeIteration`에는 다음 종류의 로직이 한 pass에 섞여 있다.

- prerequisite enforcement
- requested structured output enforcement
- structured payload parsing
- phase/guide/verification state mutation
- action target/read-only validation
- tool dispatch

일부 consumer는 output combination을 직접 검사하고, 일부는 앞쪽 enforcer에 의존한다.
Guide completion처럼 consumer가 새로운 control obligation을 만들면 같은 enforcer를 다시 호출해야
한다. 이 구조는 새로운 structured output이나 state를 추가할 때 gate 위치 누락을 만들기 쉽다.

### 3.2 mutable state의 중복 소유

구현 전에는 `session.State`가 존재했지만 실제 판단에는 다음 `coordinator.Loop` 필드가 사용됐다.

- `requirementAnalysis`
- `requestContext`
- `phaseStepState`
- `guideStepState`
- `pendingMutationVerification`
- `pendingFinalReport`
- `pendingNextDirections`
- `completedActions`
- context compaction 관련 필드

새 goal, step, attempt, revision state를 양쪽에 추가하면 snapshot과 실제 dispatch 기준이 달라질 수
있었다. Plan 08은 이 이중 소유를 제거하고 하나의 revisioned aggregate root로 통합한다.

### 3.3 phase plan을 수정하는 protocol 부재

최초 `phase_plan`이 수락된 뒤 model이 remaining plan을 공식적으로 수정할 방법이 없다. 새로운
evidence로 diagnosis focus가 변경되어도 기존 phase를 계속하거나 imperative rewind를 사용해야 한다.

### 3.4 action history가 goal lifecycle을 표현하지 못함

구현 전 `completedActions`는 tool, phase, command, target, result hash, compact result를 유지했지만
다음을 직접 표현하지 않았다.

- 어떤 step goal을 위해 수행했는가
- 어떤 completion criterion을 확인하려 했는가
- goal 달성에 충분했는가
- 부족했다면 다음 strategy가 무엇인가
- 어느 plan revision에서 수행했는가
- retry가 정당한 상태 변경 이후인지

### 3.5 compaction과 실행 이력의 소유권이 분리되지 않음

구현 전 compact anchor는 대화 context를 줄이는 데 유용했지만 goal/step/attempt의 source of truth가
아니었다. 실행 이력이 compacted text에만 남으면 context rewrite 시 goal과 이전 시도 사이의 연결이
약해진다.
Plan 08은 구조화된 in-memory session state를 source of truth로 두고 compact anchor를 projection으로
낮춘다. 파일 기반 복구는 Plan 09에서 별도로 다룬다.

## 4. 보존해야 하는 기존 기능

리팩터링은 다음 기능을 삭제하거나 축소하면 안 된다.

### 4.1 Provider와 protocol

- Native function calling
- Tool-use shim의 단일 JSON object protocol
- Invalid action 및 mixed structured answer repair
- Streaming text 수집과 user-visible progress 출력
- OpenAI-compatible provider 설정

### 4.2 Request lifecycle

- 최초 `requirement_analysis` 강제
- request context 파생 및 target/scope anchor
- follow-up 요청에서 prior context 재사용
- conversation/clarification request의 직접 답변
- broad cluster request를 임의의 Kubernetes Cluster object로 변경하지 않는 정책

### 4.3 Phase lifecycle

- 최초 phase plan 검증
- declared phase reference 검증
- forward-only `allowed_next`
- mutation request의 verification phase 요구
- CRD가 아닌 target에서 resource-guide phase 차단
- active phase completion condition anchor

### 4.4 Guidance lifecycle

- CRD classification 이후에만 resource guide lookup
- guidance lookup과 guided diagnosis 분리
- guide step progress
- guide 완료 후 top-level phase progress
- guide unavailable 이후 일반 진단 계속
- incident guidance와 resource guidance의 ownership 분리

### 4.5 Mutation safety

- `risk.risky=true` command의 exact payload approval
- 영구 approval skip state 없이 read-only/target/verification 독립 강제
- mutation action의 single/ordered-chain direct verification 계약
- namespace/action target verification
- evidence 충족 후 `mutation_verification_result`
- 같은 ID의 await-state waiting 재확인과 최대 5회 budget
- failed/budget exhaustion 후 mutation 재실행 금지와 다른 safe strategy

### 4.6 Input, output, language

- approval, continuation choice, continuation text의 input ownership
- orchestrator meta command 처리
- Korean translation boundary
- tool call, command, resource name, raw output 비번역
- translation 실패 시 Korean error 처리
- final report와 next directions UX

### 4.7 Runtime safety and maintenance

- read-only kubectl classification과 safe pipeline
- correction escalation과 context compaction
- target/resource/namespace/read-only/tool execution gates
- cancellation과 request cleanup
- runtime snapshot publish

## 5. 구현 패키지 구조

```text
internal/react/
  contract/
    control.go            # control state closed set and execution classification
    model_output.go       # provider-neutral output/event kinds
    execution.go          # goal/phase/step/attempt/budget immutable contracts
    dispatch.go           # immutable tool dispatch intent and status
    verification.go       # verification shape/mode/check contracts
    events.go             # accepted input events
    effects.go            # post-commit external effects
    snapshot.go           # immutable runtime references
    clone.go              # provider data deep clone

  session/
    aggregate.go          # generic revisioned root and atomic candidate commit
    control.go            # control/lifecycle aliases and classification
    execution.go          # goal execution state clone and lookup facade
    ledger.go             # indexed attempt/observation storage and audit

  flow/
    gate/
      output_policy.go    # turn-entry output policy
      outcome.go
      correction.go
    phase/
      movement.go
      plan.go
      progress.go
      revision.go
      validation.go
    verification/
      evidence.go
      matching.go
      requirements.go
    guidance/
    request/
    report/
    direction/

  protocol/
    calls.go              # native call name normalization/classification
    schema.go             # native function schemas
    shim.go               # shim parse/repair

  coordinator/
    loop.go               # lifecycle and turn orchestration only
    iteration.go          # model turn -> event/effect sequence
    execution.go          # external tool effects
    dispatch.go           # pre-dispatch intent commit and reconciliation recovery
    state.go              # runtimeState root, candidate transaction and audit
    execution_state.go    # goal/phase/step/attempt runtime projection
    turn_output.go        # envelope policy and domain validation pipeline
    turn_effects.go       # post-commit effect execution
    verification_runtime.go
    termination.go        # single mutation-verification terminal finalizer
    continuation.go       # bounded segment handoff and resume
    revision.go           # accepted plan revision application
    input.go
    output.go
    dependencies.go
```

`flow`는 `coordinator`나 `session`을 import하지 않는다. `flow`는 immutable contract와 snapshot을
입력으로 받아 event validation result와 effects를 반환한다. `coordinator`만 model과 tool I/O를
실행한다. Durable file I/O는 Plan 09의 optional integration이다.

### 5.1 Session aggregate boundary

`coordinator.runtimeState`는 모든 runtime substate를 연결하는 package-private root이고,
`session.Aggregate[runtimeState]`는 다음 저장 책임만 가진다.

- current immutable state 보관
- monotonic state revision 관리
- expected revision 비교
- validated candidate state의 atomic commit
- immutable deep snapshot 제공
- revision, single-owner, active-reference 같은 structural invariant 확인

Goal 달성 판단, phase revision validation, output policy, retry/branch 결정은 `flow` 규칙을 사용한다.
Coordinator는 committed root를 직접 부분 변경하지 않고 active transaction의 detached candidate에만
validated transition을 적용한다.

God object로 변질되는 것을 막기 위한 구조 규칙:

- 신규 mutable workflow field를 `coordinator.Loop`에 추가하지 않는다.
- published snapshot으로 mutable substate pointer를 외부로 반환하지 않는다.
- goal execution ledger는 `session/execution.go`, workflow root와 clone/audit는
  `coordinator/state.go`에 분리한다.
- cross-domain workflow invariant는 candidate commit 전 `coordinator.auditRuntimeCandidate`에서
  검사한다.
- session commit은 revision/ownership/reference 같은 structural invariant만 재확인한다.
- session method 안에 provider, Kubernetes, prompt, semantic goal 판단을 넣지 않는다.
- package dependency test로 `flow -> session/coordinator`와 `session -> coordinator` import를 차단한다.

## 6. Domain Contract

### 6.1 Goal contract

```go
type GoalContract struct {
    ID                   string
    Statement            string
    CompletionCriteria   []CriterionContract
    MaxPlanRevisions     int
}

type CriterionContract struct {
    ID                   string
    Description          string
    RequiredEvidenceKind EvidenceKind
}
```

Criterion은 free-text를 다시 비교하지 않는다. Runtime이 stable ID를 부여하고 model은 ID를 참조한다.
`MaxPlanRevisions`는 model이 선택하는 값이 아니라 request 시작 시 runtime `BudgetPolicy`가 확정해
적용한 값을 표시한다. Model payload에 값이 있더라도 runtime limit을 늘릴 수 없다.

### 6.2 Phase and step contract

```go
type PhaseContract struct {
    ID                   string
    PhaseLineageID       string
    ReplacesPhaseID      string
    Name                 string
    Goal                 string
    CompletionCriteria   []CriterionContract
    Steps                []StepContract
    AllowedNext          []string
}

type StepContract struct {
    ID                   string
    GoalLineageID        string
    Goal                 string
    CompletionCriteria   []CriterionContract
    RequiredEvidence     []EvidenceRequirement
    MaxAttempts          int
}
```

`PhaseLineageID`는 phase 전체를 새 이름과 새 ID로 교체해 revision/attempt budget을 초기화하는 것을
막는다. 기존 phase를 대체하면 phase lineage를 상속하고 `ReplacesPhaseID`로 직접 관계를 선언해야 한다.

`GoalLineageID`는 동일한 step goal을 새 이름과 새 step ID로 다시 만들면서 attempt budget을
초기화하는 것을 막는다. Runtime은 대체 phase 안의 기존 goal마다 replacement step mapping과 lineage
상속을 요구한다. 완전히 새로운 target/goal은 새로운 lineage를 가질 수 있지만, 이를 도입한 새로운
observation reference와 request-level revision budget을 함께 사용한다.

`MaxAttempts`도 model-owned plan parameter가 아니다. Runtime이 step kind와 request risk에 따라
확정하고 contract에는 실제 적용값만 기록한다. Model은 더 큰 값을 요청해 budget을 우회할 수 없다.

### 6.3 Step runtime state

```go
type StepStatus string

const (
    StepPending    StepStatus = "pending"
    StepActive     StepStatus = "active"
    StepAchieved   StepStatus = "achieved"
    StepBlocked    StepStatus = "blocked"
    StepSuperseded StepStatus = "superseded"
)
```

정상 전이는 다음과 같다.

```text
pending -> active
active  -> achieved
active  -> blocked
active  -> superseded
```

Plan revision은 completed history를 삭제하지 않는다. Superseded phase와 step도 session event history에
남는다.

### 6.4 Attempt contract

```go
type AttemptRecord struct {
    ID                  string
    SessionID           string
    RequestID           string
    PlanRevision        int
    PhaseID             string
    StepID              string
    GoalLineageID       string
    Strategy            string
    Action              Action
    ObservationRefs     []string
    Status              AttemptStatus
    RetryOf             string
    RetryReason         string
    ChangedSince        []string
}
```

Attempt record와 provisional reservation은 accepted dispatch intent commit 시 만들어져 external effect
전에 budget 초과 batch를 막는다. 실제 invocation 또는 unknown execution으로 이어진 attempt만 lineage
budget을 계속 소비한다. 같은 batch의 앞선 실패 때문에 invocation 전에 cancelled된 뒤쪽 intent는 audit
history에는 남지만 attempt count와 repetition matching에서 제외한다. Parse error, output-policy
violation, target rejection은 공통 correction state에 포함하고 step attempt에는 포함하지 않는다.

### 6.5 Step result

```go
type StepResult struct {
    StepID              string
    Status              StepResultStatus
    Criteria            []CriterionResult
    EvidenceRefs        []string
    RemainingGap        string
    SuggestedNext       string
}
```

허용 status:

- `achieved`: completion criteria를 충족했다고 model이 판단함
- `blocked`: 현재 허용된 strategy와 budget으로 완료할 수 없음
- `replan_required`: current/remaining plan이 evidence와 맞지 않음

목표가 아직 달성되지 않았지만 다른 strategy를 바로 시도할 수 있으면 별도 `step_result`를 요구하지
않는다. 다음 action의 `reason`과 active step ID로 continuation을 표현한다. Step을 닫거나 replan으로
넘길 때만 structured `step_result`가 필요하다.

### 6.6 Plan revision

```go
type PhasePlanRevision struct {
    BaseRevision        int
    Reason              string
    EvidenceRefs        []string
    SupersededPhaseIDs  []string
    SupersededStepIDs   []string
    RemainingPhases     []PhaseContract
    StepLineageMappings []StepLineageMapping
    ActivePhaseID       string
    ActiveStepID        string
}
```

Runtime validation 범위:

- `BaseRevision`이 현재 revision과 같다.
- evidence reference가 현재 session에 존재한다.
- reference가 허용된 observation, user input, mutation, external-state event다.
- completed history와 mandatory obligation을 삭제하지 않는다.
- replacement phase는 기존 phase lineage와 직접 replacement 관계를 상속한다.
- replacement phase 안에서 계속 수행할 기존 goal은 step lineage mapping을 제출한다. 관찰 결과
  폐기된 old goal은 superseded history로 남기고 mapping하지 않으며, genuinely new goal은 새 lineage를
  사용한다.
- replacement step은 기존 goal lineage와 누적 attempt budget을 상속한다.
- 새로운 target/goal lineage는 기존 observation에서 출발했음을 참조한다.
- revised graph와 active references가 유효하다.

Runtime은 evidence의 의미가 revision 결론을 실제로 뒷받침하는지 판정하지 않는다.

### 6.7 Mandatory obligation

```go
type ObligationKind string

const (
    ObligationApproval             ObligationKind = "approval"
    ObligationMutationEvidence     ObligationKind = "mutation_evidence"
    ObligationMutationResult       ObligationKind = "mutation_result"
    ObligationResourceGuideLookup  ObligationKind = "resource_guide_lookup"
    ObligationUserInput            ObligationKind = "user_input"
    ObligationForcedFinalReport    ObligationKind = "forced_final_report"
)
```

Control state는 active obligation과 model/user/tool owner로부터 결정한다. Plan revision reducer는
obligation을 수정하지 않는다.

### 6.8 Correction state

Plan 08은 Plan 06과 별도의 correction counter를 만들지 않는다. 모든 deterministic rejection은 기존
`GateOutcome`으로 수렴하고 `session.Corrections`의 동일한 counter를 사용한다.

```go
type CorrectionKey struct {
    Code         string
    RetryScope   RetryScope
    PhaseID      string
    StepID       string
    ObligationID string
}

type CorrectionState struct {
    Count          int
    LastRevision   uint64
    LastOutcome    GateOutcomeKind
}
```

운영 규칙:

- 한 model response에서 같은 key는 한 번만 증가한다.
- command/error 문자열이 달라도 같은 gate code와 scope면 같은 counter다.
- 정상적인 step/phase/obligation 전진 시 해당 scope counter를 reset한다.
- threshold 도달 시 `GateOutcome.BranchPolicy`가 retry, blocked, replan, request block을 결정한다.
- correction, step attempt, verification evidence, mutation continuation, plan revision budget은 서로
  독립적이다.
- output-policy violation도 새 counter가 아니라 동일한 GateOutcome correction 경로를 사용한다.

따라서 namespace gate, output policy, structured payload correction이 서로 다른 구현 파일에 있어도
최종 retry/stop 판단은 session의 하나의 correction state에서 이루어진다.

### 6.9 Runtime budget policy

Budget은 model output이 아니라 runtime configuration과 capability profile이 소유한다.

```go
type BudgetPolicy struct {
    StepAttempts                 map[StepKind]int
    PlanRevisions                int
    CorrectionRetries            map[CorrectionClass]int
    VerificationEvidenceAttempts int
    MutationContinuationAttempts int
    MaxIterations                int
    ClosureReserve               int
    Profile                      string
}
```

독립 budget 축:

| Budget | 증가 조건 | reset/종료 조건 |
| --- | --- | --- |
| Correction | 실행 전 거부된 model output | scope 정상 전진 또는 GateOutcome branch |
| Step attempt | execution-approved dispatch intent commit에서 provisional reserve, invocation 시 확정 | invocation 전 cancelled면 제외; step achieved/blocked/superseded면 종료 |
| Verification evidence | active verification check를 위한 observation action | check satisfied/failed 또는 evidence budget 소진 |
| Await-state recheck | 같은 verification ID가 waiting으로 판정됨 | satisfied/failed 또는 최대 5회 소진 |
| Mutation continuation | verification이 failed인 뒤 다른 안전한 전략/revision을 요청 | 새 mutation verification 시작 또는 continuation exhaustion |
| Plan revision | 유효한 revision이 accepted됨 | request 종료 |
| Max iterations | execution segment의 model turn 진행 | validated continuation handoff 또는 request 종료 |
| Closure reserve | segment 상한 전 handoff 정리 turn 예약 | 새 segment 시작 |

Verification evidence budget은 mutation continuation budget과 합치지 않는다. Evidence budget은
active check의 최초 observation과 같은 ID의 await-state recheck에 적용하고, continuation budget은
verification이 `failed`로 닫힌 뒤 다른 strategy를 선택하는 lifecycle에 적용한다. Evidence budget은
`ObligationID + VerificationID`를 key로 관리한다. 기본 evidence action 상한은 최초 확인 1회와
await-state 재확인 5회를 합친 6회다.

Budget profile은 다음 요소를 조합한다.

- protocol capability: native function calling, shim
- request risk: read-only, mutation
- step kind: lightweight, diagnostic, guidance, verification
- correction class: protocol/schema, domain, safety

Provider/model 차이는 주로 protocol/schema correction budget에만 반영한다. Safety/domain budget과
mutation lifecycle을 provider마다 느슨하게 바꾸지 않는다. Request가 시작되면 선택한 profile과 적용
값을 session snapshot에 고정하며, 실행 중 config 변경으로 budget이 바뀌지 않는다.

`CorrectionClass`는 최소 `protocol_schema`, `domain`, `safety`로 나눈다. Gate code는 하나의 class에
정적으로 등록되며 unknown code는 더 느슨한 protocol budget으로 fallback하지 않는다.

현재 runtime 기본값:

| Budget | 초기 후보값 |
| --- | ---: |
| Lightweight action attempts | 2 |
| 일반 read-only step attempts | 4 |
| Guided step attempts | 3 |
| Plan revisions per request | 2 |
| Native protocol/schema correction | 2 |
| Shim protocol/schema correction | 3 |
| Safety/domain correction | 2 |
| Verification evidence actions per verification | 6 (initial 1 + recheck 5) |
| Mutation continuation attempts | 기존 3 유지 |
| Model iterations per segment | 20 |
| Closure reserve | 2 |

Config override는 허용하되 runtime hard minimum/maximum으로 clamp한다. 기본값이나 clamp 범위를
변경할 때는 근거를 문서와 release note에 남기며, LLM이 plan payload로 limit을 변경할 수는 없다.

초기 hard-bound 후보:

| Budget | Min | Max |
| --- | ---: | ---: |
| Lightweight action attempts | 1 | 3 |
| 일반 read-only step attempts | 2 | 8 |
| Guided step attempts | 2 | 6 |
| Plan revisions | 1 | 4 |
| Correction retries | 1 | 5 |
| Verification evidence attempts | 1 | 6 |
| Mutation continuation attempts | 1 | 5 |

Mutation command의 반복 실행은 일반 step max가 허용해도 자동 허용되지 않는다. Successful 또는 outcome
unknown mutation 이후에는 verification obligation이 우선하며, 새 mutation은 별도 action validation을
다시 통과한다. Command가 `risk.risky=true`이면 exact payload approval도 다시 받아야 한다.

초기값 산정은 step kind와 correction class별 successful scenario의 p95에 한 번의 여유를 더한 값을
후보로 삼고 hard maximum으로 clamp한다. Solvable regression corpus를 모두 통과하지 못하면 숫자만
늘리기 전에 goal granularity, schema, prompt, provider normalization 결함을 먼저 조사한다. Production
실행 중 자동으로 budget을 늘리는 adaptive tuning은 사용하지 않는다. Model/provider version이 바뀌면
고정 corpus를 다시 실행하고 versioned profile을 새로 발행한다.

## 7. Turn Output Policy

### 7.1 Model output envelope

```go
type ModelOutputEnvelope struct {
    ProgressText       string
    PlainAnswer        string
    InternalEvents     []ModelOutputEvent
    ExternalActions    []ActionProposal
    InvalidOutputs     []InvalidOutput
}

type ActionProposal struct {
    Action       Action
    StepRef      *StepRef // optional assertion, not required bookkeeping
    Verification *VerificationSpec
}
```

Native function calls와 shim JSON은 protocol layer에서 동일한 envelope로 변환한다. Unknown internal
name은 internal event로 인정하지 않는다. 등록되지 않은 external tool은 이후 tool-registry gate에서
차단한다.

Action의 step binding은 기본적으로 runtime이 현재 snapshot에서 결정한다.

- active step이 하나면 해당 step에 자동 bind한다.
- lightweight bundle은 candidate plan에서 생성한 provisional step에 bind한다.
- mutation verification action은 runtime-selected active verification ID에 bind한다.
- guide action은 active guide step에 bind한다.
- model이 optional `StepRef`를 제공하면 runtime-derived ref와 일치해야 한다.
- 둘 이상의 일반 active step이 존재해 binding이 모호하면 action correction이 아니라 session invariant
  violation으로 처리한다.

따라서 model이 매 action마다 stable ID를 반복해 적을 필요는 없다. `StepRef`는 복잡한 provider
bookkeeping 요구가 아니라 model이 의도를 명시하고 싶을 때 사용하는 assertion이다.

### 7.2 Policy modes

```go
type MixPolicy string

const (
    MixOpen              MixPolicy = "open"
    MixGuarded           MixPolicy = "guarded"
    MixRequiredExclusive MixPolicy = "required_exclusive"
)

type OutputPolicy struct {
    Mix                 MixPolicy
    StateEventExclusive bool
}
```

- `MixOpen`: 일반 diagnostic action과 progress text 허용
- `MixGuarded`: action은 허용하지만 terminal/obligation-bypassing event 금지
- `MixRequiredExclusive`: 지정된 protocol event만 허용
- `StateEventExclusive`: mix mode와 독립적으로 state-changing event와 external action 혼합을 금지

### 7.3 Control state policy matrix

| Turn-entry control | 허용 output | Mix policy |
| --- | --- | --- |
| AwaitingRequirementAnalysis | requirement_analysis | RequiredExclusive |
| AwaitingPhasePlan | phase_plan | RequiredExclusive, 기존 lightweight 예외는 별도 bundle |
| AwaitingModelStep | action, step_result, phase_progress, phase_plan_revision, final_report | Open + state event exclusive |
| AwaitingResourceGuideLookup | resource_guide_lookup | RequiredExclusive |
| AwaitingGuidedDiagnosisStep | guide-related action, guide_progress, step_result, phase_plan_revision | Guarded + state event exclusive |
| AwaitingGuidedPhaseProgress | phase_progress | RequiredExclusive |
| AwaitingMutationVerificationEvidence | matching observation action | Guarded |
| AwaitingMutationVerificationResult | mutation_verification_result | RequiredExclusive |
| AwaitingMutationVerificationChainEvidence | matching observation action for the active chain check | Guarded |
| AwaitingMutationVerificationChainResult | mutation_verification_result | RequiredExclusive |
| AwaitingMutationContinuation | materially different action 또는 evidence-grounded phase_plan_revision | Guarded + state event exclusive |
| AwaitingFinalReport | final_report | RequiredExclusive |
| AwaitingNextDirections | next_directions | RequiredExclusive |
| AwaitingContinuationHandoff | continuation_handoff | RequiredExclusive |

Progress text는 function call과 함께 표시할 수 있다. Plain answer는 response/clarification이 허용된 phase
또는 user-query completion 상태에서만 허용한다.

### 7.4 Lightweight lookup fast path

기존 single `lightweight_lookup`의 `phase_plan + one action`은 단순 조회의 불필요한 model turn을
늘리지 않기 위한 정식 fast path로 유지한다. 이 경로는 full step assessment나 plan revision을 위한
경로가 아니라, 하나의 read-only 조회를 실행하고 observation 수신 여부만 확인한 뒤 최종 응답으로
넘기는 경로다.

Eligibility:

- accepted request가 mutation, remediation, approval, guidance, verification을 요구하지 않는다.
- candidate plan에 `lightweight_lookup` phase가 정확히 하나 있다.
- phase에는 runtime이 생성한 implicit step이 정확히 하나 있다.
- completion criterion은 `successful observation received`다.
- bundle에는 external action이 정확히 하나 있고 다른 internal event가 없다.
- action은 read-only이며 accepted target/scope와 일치한다.

Runtime은 model에게 아직 존재하지 않는 step ID를 미리 생성하도록 요구하지 않는다. Candidate plan을
검증하면서 provisional phase/step을 만들고 같은 response의 action을 그 유일한 step에 bind한다.

```text
parse phase_plan and action
-> validate candidate lightweight plan and eligibility
-> if plan invalid: reject the entire bundle without state change
-> create provisional phase/step binding
-> evaluate action against the candidate snapshot without committing
-> commit plan accepted and step activated
-> if action rejected: record correction and keep the active lightweight step
-> if action allowed: execute one read-only action
-> record attempt and observation
-> successful observation automatically completes the lightweight step/phase
-> request the existing final response path grounded in that observation
```

이 fast path에서는 별도 `step_result`를 요구하지 않는다. Runtime은 조회 결과의 의미가 질문에
충분한지 판정하지 않고 tool success와 observation 수신만 확인한다. Model은 다음 final response에서
observation을 해석한다.

Plan validation과 action pre-validation은 독립적인 decision이다. Plan validation이 성공하면 action
decision이 reject여도 plan/step effect를 commit한다. Action reject는 external effect만 차단하고 유효한
plan과 active lightweight step은 유지한 채 corrected action을 요구한다. Tool execution이 실패하면
step은 active 상태로 남고 attempt budget 안에서 다른 read-only strategy를 선택한다. Action이 실행되지
않은 pre-validation failure는 attempt count를 증가시키지 않는다.

## 8. Event, Validation, Effect, Commit

### 8.1 Turn processing phases

```text
1. Capture immutable session snapshot and state revision.
2. Derive output policy.
3. Send model request.
4. Normalize native/shim response.
5. Classify full output envelope.
6. Validate output policy without mutation.
7. Parse and domain-validate internal events without mutation.
8. Pre-validate action target/tool/read-only constraints.
9. Pure reducers produce one candidate next state and external effects.
10. Compare captured state revision with current revision.
11. Audit the full candidate state and atomically commit it.
12. Execute external effects.
13. Apply observation and follow-up state effects.
```

Model response를 기다리는 동안 cancellation, meta command, user input이 state revision을 변경했다면
stale response를 적용하지 않는다.

### 8.2 Atomic state transition

한 model event가 step completion, phase progress, next-step activation, obligation creation, control transition을
동시에 만들 수 있다. 이 state effects는 부분 적용하지 않고 하나의 transition으로 commit한다.

```go
type runtimeTransaction struct {
    expected  uint64
    previous  *runtimeState
    candidate *runtimeState
    messages  []deferredMessage
    effects   []contract.Effect
}
```

Atomic commit 절차:

```text
immutable current snapshot
-> pure reducer builds a deep candidate state
-> flow/runtime audits all cross-domain invariants
-> compare ExpectedRevision
-> enter the single session owner/lock
-> session rechecks structural invariants
-> swap the state root once
-> increment state revision exactly once
-> release owner/lock
-> execute external effects
```

Audit, revision comparison, candidate construction 중 하나라도 실패하면 current state는 변경하지 않는다.
Go STM은 사용하지 않는다. Deep candidate state와 단일 owner 또는 짧은 critical section의 root pointer
swap으로 all-or-nothing을 구현한다.

External effect 실패는 이미 commit된 transition을 rollback하지 않는다. Tool failure나 unknown outcome은
새 observation event로 다음 atomic transition을 만든다. Lightweight bundle에서 action이 reject된 경우도
candidate state에 accepted plan, active step, correction을 함께 넣고 한 번에 commit한 뒤 external action
effect만 생성하지 않는다.

### 8.3 State effect와 external effect 분리

State effect 예:

- requirement accepted
- plan accepted/revised
- step achieved/blocked
- control transition
- obligation created/satisfied
- correction recorded

External effect 예:

- model call
- kubectl/tool invocation
- approval request
- user message emit

Flow reducer는 I/O를 수행하지 않는다. Coordinator는 effect ordering과 external failure mapping만
담당한다.

### 8.4 실패 semantics

- Output policy failure: state transition 없음, correction만 기록
- Structured payload failure: domain state transition 없음, correction만 기록
- Action pre-validation failure: action 미실행, active step 유지, correction count 증가
- Tool execution failure: attempt와 observation failure 기록, retry policy 적용
- Mutation execution outcome unknown: 자동 재실행 금지, verification obligation 생성

Durable write ordering과 process crash recovery는 Plan 09에서 이 in-memory effect ordering에 별도로
연결한다.

## 9. Goal-Driven Execution Flow

### 9.1 New request

```text
user query
-> requirement_analysis
-> request context and resource classification
-> phase plan with phase/step goals and criteria
-> runtime validation
-> plan revision 1 accepted
-> first active step
```

### 9.2 Step attempt

```text
active step anchor
-> model chooses strategy and action
-> action pre-validation
-> attempt_started session event
-> tool execution
-> observation_recorded session event
-> attempt ledger update
-> next model turn with compact step projection
```

### 9.3 Goal not achieved

Model이 다른 action을 반환하면 current step은 active 상태를 유지한다. Runtime prompt에는 이전 attempt와
observation summary, 남은 completion criteria, attempt budget을 포함한다.

```text
attempt 1: workload output did not expose Pod Ready condition
attempt 2: direct Pod query selected
```

### 9.4 Step achieved

Model은 `step_result(status=achieved)`로 criterion별 evidence reference를 반환한다. Runtime은 references와
절차를 검증한 뒤 step을 achieved로 닫고 다음 declared step 또는 phase transition을 요구한다.

### 9.5 Step blocked or budget exhausted

Budget을 넘으면 새 action을 실행하지 않는다. 다음 중 하나를 요구한다.

- `step_result(status=blocked)`
- evidence 기반 `phase_plan_revision`
- inconclusive `final_report`
- `next_directions`를 통한 사용자 선택

Runtime이 임의로 의미상의 다음 plan을 작성하지 않는다.

### 9.6 Plan revision

Plan revision은 관찰 command나 strategy가 바뀔 때마다 사용하지 않는다.

| 변경 | 처리 |
| --- | --- |
| 같은 step goal에서 다른 command 선택 | 바로 action |
| 같은 phase goal에서 다른 resource 관계 관찰 | 바로 action |
| current step strategy 변경 | 바로 action |
| current step goal 자체가 evidence와 맞지 않음 | plan revision |
| remaining phase graph가 evidence와 맞지 않음 | plan revision |
| accepted primary request anchor가 바뀜 | requirement/request revision 별도 절차 |

초기 phase/step goal을 특정 원인에 과도하게 결합하면 정상적인 탐색도 revision을 요구하게 된다.
Runtime prompt와 phase-plan validation은 초기 진단 goal이 결과가 아니라 탐색 목적을 표현하도록 유도한다.

```text
과도하게 좁은 goal: Deployment replica 문제 확인
탐색 가능한 goal: workload failure source 식별
```

후자에서는 Deployment observation 이후 Node를 확인해도 같은 goal 안의 strategy 변경이므로 revision이
아니다. Revision은 extra model turn과 ID/evidence validation을 요구하는 의도적인 safety boundary지만,
일반 read-only 탐색의 기본 경로가 되어서는 안 된다.

```text
new observation
-> model concludes current/remaining plan is insufficient
-> step_result(replan_required) or plan_revision proposal
-> revision procedure validation
-> old active step becomes superseded when necessary
-> revision history append
-> active phase/step update
-> next model turn under revised plan
```

Revision과 external action은 같은 response에서 실행하지 않는다.

Revision payload는 전체 plan을 다시 출력하기보다 base revision에 대한 delta를 표현한다.

```text
base_revision
evidence_refs
supersede phase/step refs
insert or replace remaining phase/step contracts
new active refs
```

Revision 빈도와 correction rate가 높으면 model 성능만의 문제가 아니라 initial goal granularity 또는
schema friction의 문제로 간주한다.

### 9.7 Mutation and verification

Mutation은 mandatory direct-verification lifecycle을 가지지만 mutation action과 verification은
하나의 step attempt다.

```text
mutation action proposed
-> model declares single or ordered-chain verification
-> risk.risky=true이면 exact approval obligation
-> attempt_started session event
-> mutation executed
-> observation session event
-> one active mutation verification
-> read-only evidence action attached to the same AttemptID
-> mutation_verification_result
-> satisfied, waiting on the same ID, or failed
```

Plan revision은 pending verification을 제거할 수 없다. Mutation 이후 같은 target을 다시 조회하는 것은
과거 phase rewind나 새 attempt가 아니라 현재 mutation attempt의 verification observation이다.

Verification shape와 timing은 서로 다른 축이다.

- `shape=single`: 하나의 expected state를 확인한다.
- `shape=chain`, `policy=ordered`: 서로 다른 direct check를 한 번에 하나씩 순서대로 확인한다.
- `mode=immediate`: observation 한 번으로 판정한다.
- `mode=await_state`: Kubernetes state 수렴을 기다리며 같은 VerificationID로 최대 5회 재확인한다.

시간차 재확인은 chain이 아니다. Chain은 서로 다른 조건에만 사용한다. 한 step attempt에는 mutating
primary action 하나만 허용하며 chain check와 temporal recheck는 step attempt를 추가하지 않는다.
Mutation 대상과 원래 user-visible target이 다르면 direct verification과 outcome verification을 같은
pending obligation에 병합하지 않는다. Outcome은 다음 declared plan step에서 확인한다.

Verification evidence action이 실제 실행되어 observation을 만들면 해당
`ObligationID + VerificationID` budget을 증가시킨다. Parse/correction과 action pre-validation failure는
evidence budget을 소비하지 않는다. `satisfied` 또는 `failed`가 되면 해당 counter를 닫는다.

Evidence budget이 소진됐는데 expected state가 확인되지 않으면 verification을 satisfied로 처리하지
않고 mutation attempt를 `unknown`으로 닫아 unresolved obligation과 inconclusive-report 제약을 남긴다.
반대로 state-bearing evidence가 expected state를 명시적으로 반증한 `failed`는 terminal attempt
history로 남지만 영구 unresolved obligation을 만들지 않는다. Mutation을 자동 재실행하지 않으며, 남은
step/revision budget이 있으면 materially different read-only strategy 또는 evidence-based revision을
허용한다. 진행 중인 verification은 revision을 차단하지만, unknown으로 닫힌 obligation은 replaceable
plan graph 밖의 request ledger에서 그대로 보존되므로 remaining graph revision으로 삭제되지 않는다.
이 obligation이 남은 동안의 final report는 항상 `conclusive=false`여야 한다.

Forbidden/Unauthorized observation은 `access_blocker`이며 `satisfied`/`waiting`을 지지하지 않는다.
일반 진단에서 RBAC가 발생해도 같은 forbidden command를 반복하거나 request를 즉시 terminal block하지
않는다. Current phase에서 permitted evidence를 우선 선택하고, 목표 달성에 RBAC 변경이 실제로 필요하면
별도의 `risk.risky=true` mutating action, exact user approval, direct verification을 사용한다.

## 10. Attempt Ledger and Repetition Handling

### 10.1 Ledger record levels

Session level:

- original goal
- requirement/request anchors
- accepted plan revisions
- mandatory obligations

Phase level:

- phase goal and criteria
- achieved/blocked/superseded step summaries
- phase result

Step level:

- active goal and criteria
- strategy attempts
- observation references
- remaining gaps
- attempt budget

### 10.2 Prompt projection

모든 attempt를 매 turn 반복하지 않는다.

- Active step: 최근 attempts와 남은 criteria를 항상 compact하게 포함
- Active phase: completed step 결과를 한 줄씩 포함
- Previous phases: phase outcome만 포함
- Full in-process history: session state에 유지하고 prompt에는 필요한 projection만 포함
- Safety obligations: compaction 대상에서 제외

### 10.3 Retry contract

정당한 retry는 다음 metadata를 가질 수 있다.

```text
retry_of
retry_reason
changed_since
```

`changed_since`는 임의 문자열이 아니라 현재 session event history에 존재하는 mutation, user input,
external wait, resource version change, timeout/partial-failure event를 참조한다.

### 10.4 Runtime braking

Runtime은 semantic similarity로 광범위한 금지를 하지 않는다. 다음 경우에만 제동한다.

- 동일 active step에서 budget 초과
- 동일 normalized action/target을 한 batch에 중복하거나, 중간의 다른 action으로 숨기거나,
  state change/retry 근거 없이 반복
- 동일 base revision과 동일 remaining plan을 새로운 evidence 없이 재제출
- correction response가 canonical correction state의 gate/scope threshold를 초과

Model이 phase나 step 이름만 바꾸더라도 phase lineage, step goal lineage, replacement mapping,
request-level revision budget으로 무제한 초기화를 방지한다. 의미적으로 다른 goal인지 runtime이
완전히 판정할 수 없다는 한계는 명시적으로 수용한다.

## 11. Durable Persistence Boundary

### 11.1 Bounded in-process continuation

`MaxIterations`는 request 전체를 폐기하는 상한이 아니라 한 model execution segment의 상한이다.
Request-fixed `ClosureReserve`에 진입하면 pending mutation verification을 먼저 끝내고, 안전하게
중단 가능한 model-turn control에서 `AwaitingContinuationHandoff`로 이동한다. Model은
`conclusive=false`인 `continuation_handoff` 하나만 반환한다.

Runtime은 request/goal ID, terminal completed step ID, observation ID, mandatory obligation,
recommended next step을 검증한다. `unresolved_steps`는 현재 execution의 active/pending step을
하나도 빠뜨릴 수 없다. Accepted handoff의 현재 evidence 기반 판단과 권장 다음 step을 번역 경계로
사용자에게 먼저 보여준 뒤 계속/종료를 선택하게 한다. 사용자가 계속을 선택하면 continuation segment와 segment iteration만
전진/초기화하고 request ID, goal/phase/step lineage, plan revision, ledger, correction, step,
verification evidence와 mutation continuation budget counter는 유지한다.

Accepted execution contract가 만들어지기 전에 hard iteration limit에 도달한 경우에는 유효한
handoff ID를 만들 수 없으므로 continuation을 위조하지 않는다. Runtime은 해당 segment를 닫고
사용자에게 더 구체적인 요청을 요구한다.

### 11.2 Process persistence boundary

Plan 08은 in-memory session state, accepted event history, attempt ledger, bounded prompt projection까지
완결한다. Checkpoint, append-only journal, process crash recovery, mutation write-ahead record, file security와
retention은 파생 계획인 [`09_durable_session_persistence.md`](./09_durable_session_persistence.md)에서
구현한다.

Plan 09가 미구현이거나 storage 결정으로 blocked되어도 Plan 08의 output contract와 goal/step runtime은
독립적으로 배포할 수 있어야 한다. 반대로 Plan 09는 Plan 08의 immutable snapshot과 accepted events를
저장할 뿐 output policy나 workflow transition을 다시 판단하지 않는다.

## 12. Prompt and Schema Changes

### 12.1 Default prompt

`prompts/default.tmpl`에 다음 규칙을 추가한다.

- action은 active step goal과 criterion을 진전시켜야 한다.
- 이전 attempts와 ruled-out approach를 참고한다.
- goal이 미달성되면 다른 strategy를 선택한다.
- step을 닫을 때 criterion별 evidence reference를 반환한다.
- remaining plan이 맞지 않으면 `phase_plan_revision`을 단독 반환한다.
- active mandatory obligation을 plan revision으로 우회하지 않는다. Unknown으로 닫힌 obligation은
  remaining plan을 수정해도 request ledger와 inconclusive-report 제약에 남는다.
- retry는 이전 attempt와 changed event를 참조한다.
- action `StepRef`는 optional이며 runtime이 유일한 active step에 기본 bind한다.
- 같은 goal 안의 strategy/focus 변경에는 plan revision을 사용하지 않는다.
- current goal 또는 remaining graph가 바뀔 때만 delta `phase_plan_revision`을 사용한다.

Prompt는 runtime enforcement를 대체하지 않는다. Function schema와 shim schema도 동일한 contract를
표현해야 한다.

### 12.2 Runtime anchors

Anchor order:

```text
runtime control and output policy
mandatory obligations
original goal and request anchor
current plan revision
active phase and step contract
applied and remaining budgets
attempt ledger projection
guide/verification nested state
latest observations
```

### 12.3 Injection boundary

Observation projection은 다음 원칙을 따른다.

- raw observation을 system instruction과 같은 text block으로 합치지 않는다.
- JSON/string encoding으로 data boundary를 유지한다.
- model interpretation과 observed value를 다른 필드로 표시한다.
- Kubernetes object text가 instruction이 아님을 prompt contract에 명시한다.

경계 설정은 prompt injection 위험을 줄이지만 model-level 위험을 완전히 제거하지 못한다. 이는 residual
risk로 유지한다.

## 13. Migration Plan

구조를 직접 교체할 수 있지만 기능 손실을 막기 위해 commit과 검증 단위는 분리한다.

### Stage 0A: Deterministic behavior inventory

구현 상태: 완료. Declared control/output registry, policy-derived matrix, native/shim parity와 deterministic
loop harness가 코드에 연결되어 있다. 아래 assertion 수는 현재 closed registry에서 산출한 source
baseline이다.

Stage 0A는 대표 사례 몇 개를 추가한 상태가 아니라, 현재 runtime이 가진 상태와 출력을 먼저 명시적인
closed set으로 만들고 그 집합에서 파생되는 policy space를 모두 분류한 상태에서만 완료한다. 다음
substage는 순서대로 수행한다.

#### Stage 0A.1: Control state classification

현재 `RuntimeControlState`는 string typed constant 21개를 선언한다. `RuntimeControlUnset`은 초기화 오류를
검출하기 위한 sentinel이고, 이를 제외하면 유효 runtime state는 20개다. 기존 코드에는 이 20개 중
어느 상태에서 model request를 보내는지 나타내는 authoritative classification이 없다.

현재 model-turn state는 다음 14개다.

- `AwaitingRequirementAnalysis`
- `AwaitingPhasePlan`
- `AwaitingModelStep`
- `AwaitingResourceGuideLookup`
- `AwaitingGuidedDiagnosisStep`
- `AwaitingGuidedPhaseProgress`
- `AwaitingFinalReport`
- `AwaitingNextDirections`
- `AwaitingMutationVerificationEvidence`
- `AwaitingMutationVerificationResult`
- `AwaitingMutationVerificationChainEvidence`
- `AwaitingMutationVerificationChainResult`
- `AwaitingMutationContinuation`
- `AwaitingContinuationHandoff`

나머지 6개 유효 상태는 user input 대기, approval 대기, committed tool-result 대기, continuation input 대기 또는 terminal
상태로 분류하고 `Unset`은 invalid로 분류한다. Single verification과 ordered-chain verification은
동일한 output 종류를 사용하지만 서로 다른 control row를 가져 shape/control 불일치를 audit한다.

`session.LifecycleFor`는 closed registry의 execution class를 사용한다. `AwaitingToolResult`와 새로
추가된 미분류 상태가 default-open model turn이 되지 않으며 unknown/unclassified state는 fail-closed로
처리한다.

분류 결과는 `AllRuntimeControlStates()`와 state execution class registry의 단일 contract로 만든다.
Model-send entry point는 registry가 model-turn으로 분류한 상태만 허용하고, non-model-turn 또는 `Unset`은
요청 전송 전에 stable error로 거부한다.

#### Stage 0A.2: Typed semantic output classification

현재 `protocol/calls.go`의 상수는 type 없는 transport call-name string이며 policy가 다루는 semantic
output kind가 아니다. Native function name, shim JSON key, external action, plain answer는 표현이 서로
다르므로 call-name 상수에 type만 붙여 policy enum으로 재사용하지 않는다.

Stage 0A에서 `contract.ModelOutputKind string`과 `AllModelOutputKinds()`를 먼저 도입하고, §7.3의 13개
semantic output class를 typed constant로 정의한다. Protocol adapter는 기존 native/shim 표현을 이 enum으로
분류하되 session state를 변경하지 않는다. Stage 0A 시점에는 `step_result`와
`phase_plan_revision`도 policy target column에만 두고 unsupported로 분류했다. `step_result`는 Stage 3
구현으로 schema/native/shim mapping과 supported-kind parity row가 추가됐고, `phase_plan_revision`은
Stage 5가 같은 방식으로 registry와 parity test를 확장한다.

이 작업은 provider-neutral output envelope나 turn pipeline을 미리 구현하는 것이 아니다. Stage 0A는
typed kind, transport-to-kind classifier, closed-set coverage만 추가하고, Stage 2가 이 enum을 담는 envelope와
atomic event/effect pipeline을 구현한다.

#### Stage 0A.3: Deterministic loop harness

Stage 0A 착수 당시 public `New`/`Start` 경로는 실제 model client, local executor, tool registry를
내부에서 생성하고 `Start`가 private run loop를 goroutine으로 실행했다. Package-local unit test가
`Loop`를 직접 조립하거나 일부 executor fake를 사용하는 경우는 있었지만, model response부터 tool
observation과 다음 control state까지 반복 실행하는 공통 deterministic harness는 없었다.

Stage 0A에 production default를 유지하는 dependency injection seam과 다음 test fixture를 추가한다.

- scripted fake model/chat: native function-call response 또는 shim text response를 순서대로 반환하고 받은
  prompt, tool schema, call count를 기록한다.
- fake tool dispatcher/executor: 예상 action과 target에 대해 bounded observation, failure, unknown outcome을
  순서대로 반환하고 실제 invocation 여부와 횟수를 기록한다.
- bounded turn driver: wall-clock sleep/backoff 없이 입력, model response, approval, tool result를 결정적
  순서로 진행하고 각 turn의 snapshot, output classification, gate outcome, external effect를 수집한다.
  Explicit max turn/effect count와 context cancellation로 fixture 오류가 test hang이 되지 않게 한다.
- dependency bundle: production constructor는 기존 client/registry/executor/guidance factory를 기본값으로
  사용하고 test constructor만 fake를 주입한다. Test-only condition을 workflow 코드에 넣지 않는다.

Pure policy matrix test는 harness 없이 실행한다. Harness는 기존 native/shim normalization, exclusive
output enforcement, lightweight bundle, approval, guidance, mutation verification처럼 orchestration 순서가
중요한 characterization에 사용한다.

#### Stage 0A.4: Generated policy inventory

현재 계획 기준 baseline:

- declared control constants: sentinel 1개와 유효 runtime state 20개
- model-turn control states: 14개
- output classes: 13개
  - requirement_analysis, phase_plan, action, step_result, phase_progress
  - phase_plan_revision, resource_guide_lookup, guide_progress
  - mutation_verification_result, continuation_handoff, final_report, next_directions, plain_answer
- single-output matrix: `14 x 13 = 182` assertions
- internal-event/action pair matrix: `14 x 11 = 154` assertions
  - 11은 13개 output class에서 external action과 plain answer를 제외한 internal event class 수다.
- RequiredExclusive states: 9개, required event와 나머지 12개 output pair `9 x 12 = 108` assertions
- intentional bundle characterization: 현재 lightweight plan/action success와, action pre-validation 또는
  tool failure 뒤 accepted plan 유지
- 현재 supported structured output은 supported-kind registry에서 생성한 native/shim normalization parity
- `step_result`, revision, continuation handoff parity row를 포함하며 registry가 늘어나면 row도 자동 증가

테스트는 수백 개의 수동 case를 작성하지 않고 enum과 policy table에서 생성한다. 새 control state나 output
kind를 추가하면 unclassified row/column 때문에 자동으로 실패해야 한다. RequiredExclusive pair와 일반
internal/action pair 사이의 중복 assertion은 허용하며, coverage 누락보다 중복을 우선한다.

이 자동 실패는 수동 `map` 완성도에 의존하지 않고 다음 장치로 강제한다.

- `AllRuntimeControlStates()`와 `AllModelOutputKinds()`를 policy test의 canonical registry로 둔다.
- AST 기반 enum coverage test가 `internal/react/contract`의 typed `const` 선언을 수집하고, 선언 집합과
  canonical registry 집합이 정확히 같은지 검사한다. 따라서 새 string 상수를 선언하고 registry 갱신을
  빼먹어도 실패한다.
- 모든 declared control state를 invalid, model-turn 또는 non-model-turn으로 정확히 한 번 분류한다.
  Policy table의 key 집합은 registry에서 파생한 model-turn subset과 정확히 같아야 하며, 누락과 extra
  key를 모두 실패시킨다.
- Output kind도 declared constants, canonical registry, policy matrix column의 세 집합이 정확히 같아야
  한다. 중복 underlying string value도 실패시킨다.
- Runtime policy 조회는 `policy, ok := table[state]` 형태로 membership을 확인한다. Missing state/output을
  zero-value policy로 처리하지 않고 stable internal error code로 fail-closed 처리한다.

AST test 대신 enum source generation을 도입할 수는 있지만, constants와 registry가 하나의 source에서
함께 생성되고 CI가 stale generated output을 실패시키는 경우에만 동등한 강제로 인정한다. 수동
`All...()` 목록만 추가하는 것은 완료 조건을 충족하지 않는다.

Stage 0A 완료 조건:

1. Sentinel을 포함한 declared control state 21개가 invalid, model-turn, non-model-turn 중 정확히 하나로
   분류되고 실제 model-send entry point와 일치한다.
2. 기존 transport call/string 응답이 `ModelOutputKind`로 side effect 없이 분류되며, 아직 지원하지 않는
   target kind는 implicit unknown이 아니라 stable unsupported classification을 가진다.
3. Scripted fake model과 fake tool executor로 native/shim의 model turn, tool observation, 다음 state를
   실제 network나 cluster 없이 반복 실행할 수 있다.
4. Typed const 선언, canonical registry, control/output 분류, policy table의 집합 동등성 검사가 CI에서
   실행되며 새 enum 또는 중복 값 추가 시 실패한다.
5. 모든 model-turn control state가 policy table에 정확히 한 번 존재한다.
6. 모든 single-output 조합이 allow 또는 stable rejection code를 가진다.
7. 모든 state-changing internal event와 action 혼합이 명시적으로 분류된다.
8. 모든 RequiredExclusive state에서 required event 외의 혼합이 거부됨을 검증한다.
9. plain answer와 structured output의 혼합 규칙을 검증한다.
10. 기존 lightweight plan/action bundle 수락과 action pre-validation/tool failure 뒤 accepted plan 유지
    동작을 native/shim에서 검증한다. Provisional binding과 execution-confirmed completion은 Stage 2/3의
    구현 완료 조건으로 검증한다.
11. 현재 지원되는 native/shim output이 같은 provider-neutral output kind와 invalid-output classification을
    생성한다.
12. User/approval/continuation 대기, tool 실행, terminal, invalid control state는 model-send 불가를
    검증한다.
13. read-only, approval, mutation verification, guidance, translation의 기존 behavior characterization을
    유지한다.

### Stage 1: Session source-of-truth

구현 상태: 완료. `session.Aggregate[runtimeState]`가 revisioned root를 소유하고 model turn은 deep
candidate, turn-entry revision 비교, invariant audit, atomic root replacement를 사용한다. Published
snapshot은 detached copy다. Stage 6에서 package-local test fixture도 동일한 `runtimeState` root를
사용하도록 바꾸고 `Loop`의 private seed mirror와 hydrate 경로를 제거했다. Query 초기화와
`clear`/`reset`/`exit` meta cleanup을 포함한 input/meta control transition도 audit 없는 revision
touch 대신 detached candidate audit/commit을 사용한다. `clear`/`reset`은 request-scoped execution
ledger와 dispatch intent도 함께 제거한다. 초기
migration용 `session.State`와 phase/verification/context/snapshot/cleanup 사본은 제거했다.

- Loop compatibility state를 session model로 이전
- immutable deep snapshot과 monotonic state revision 추가
- session을 state storage/atomic commit/invariant audit만 담당하는 aggregate root로 제한
- deep candidate state audit와 root pointer swap 기반 all-or-nothing transition 추가
- runtime anchors와 input ownership을 snapshot에서만 파생
- Loop field와 session field의 dual write 제거

### Stage 2: Event/effect turn pipeline

구현 상태: 완료. Native/shim 응답은 `contract.ModelOutputEnvelope`로 정규화되고 `flow/gate` output
policy가 기존 domain consumer보다 먼저 실행된다. Internal state effect는 candidate에 적용되고 tool 및
resource-guide lookup, accepted-requirement chat/discovery 준비는 candidate commit 뒤 effect runner가
실행한다. 번역과 user message emit도 commit 뒤로 지연된다. Observation과 effect failure 후속 상태는
별도 candidate로 commit한다. Stage 6에서 turn-entry policy와 중복되던 required/mixed-output 보조
enforcer를 제거했고 domain 및 safety validation만 consumer 뒤에 유지한다.

- Stage 0A의 `ModelOutputKind`를 사용하는 provider-neutral output envelope 추가
- runtime-derived action/step binding과 optional StepRef assertion 추가
- turn-entry output policy 추가
- consumer의 parse/domain state 적용을 detached candidate로 제한하고 외부 I/O effect를 commit 뒤로 분리
- 한 model event의 state effects를 하나의 atomic transition으로 commit
- state event와 external action ordering 고정
- lightweight plan/action을 candidate snapshot에 provisional bind하고, plan validation 성공 뒤 action
  pre-validation 결과와 독립적으로 plan/active step을 atomic commit
- turn-entry policy를 authoritative enforcement로 연결하고 기존 `onlyFunctionCall`/trailing-call
  duplicate gate 제거

### Stage 3: Goal/phase/step contracts

구현 상태: 완료. Accepted plan은 request-local stable ID와 phase/step lineage를 가진 execution
contract로 변환되고, model action은 runtime이 계산한 active step에 bind된다. `step_result`는
native/shim 양쪽에서 같은 call로 정규화되며 runtime은 active ID, criterion ID, observation reference,
budget, 전이 순서만 검증한다. Evidence의 의미 판단은 model 책임으로 유지한다. Phase/step replacement를
실제로 적용하는 revision reducer와 lineage 상속 검증은 Stage 5 범위다.

- stable goal/phase/step/criterion IDs 도입
- phase lineage, replacement relation, descendant step lineage mapping 도입
- action을 active step과 연결
- step result schema와 native/shim mapping, supported-kind parity row, attempt budget 도입
- runtime BudgetPolicy와 request-fixed applied profile 도입
- verification evidence budget을 mutation continuation budget과 분리
- phase progress를 achieved step/criteria와 연결
- lightweight read-only action의 successful observation이 별도 `step_result` 없이 implicit step/phase를
  execution-confirmed completion으로 닫도록 구현
- guide step과 active mutation verification을 control-aware step projection에 연결
- Plan 06 GateOutcome counter를 session correction state로 이전

### Stage 4: Attempt ledger and bounded anchors

구현 상태: 완료. 실행 대상으로 accepted된 action은 외부 invocation 전에 `dispatch_pending` attempt로
ledger에 기록되고, reconciliation에서 terminal status와 observation이 같은 attempt에 연결된다.
이 기록은 `session.GoalExecutionState`의 request 전체 ledger에 보존된다. Prompt/compaction에는 active step 최근 attempt 3개, 연결된
observation, active phase criterion 결과, 최근 prior phase outcome 4개만 DATA 경계 안에 투영한다.
Projection 제한은 in-memory ledger를 자르지 않는다.
Reconciled/rejected/cancelled dispatch intent는 실행 ledger 자체가 아니므로 terminal result와 status를
먼저 commit한 뒤 다음 dispatch batch 또는 request 종료에서 active dispatch map으로부터 정리한다.
Invocation 전에 rejected/cancelled된 dispatch attempt는 기록상 보존하되
lineage attempt budget과 same-action repetition 기준에서는 제외한다. 각 committed assistant tool call은
동일 call ID의 terminal result를 정확히 하나 가지며, batch의 미실행 후속 call은
`cancelled/not_invoked` synthetic result로 provider protocol을 닫는다.

- existing completed action history를 attempt ledger로 migration
- observation reference와 retry metadata 추가
- phase/step/session projection size policy 구현
- context compaction이 in-memory goal/step/attempt state를 덮어쓰지 않게 변경

### Stage 5: Plan revision

구현 상태: 완료. `phase_plan_revision`은 native function call과 shim JSON에서 동일한 semantic output으로
정규화된다. `flow/phase.ValidateRevision`이 현재 base revision, observation reference, request-fixed
revision budget, active/remaining graph 전체 replacement, phase/step lineage mapping, completed history와
mandatory obligation 보존을 검증한다. Active verification은 replacement를 차단하고, 이미 unknown으로
닫힌 obligation은 replaceable graph 밖의 request ledger에 보존한 채 remaining strategy revision을
허용한다. 통과한 revision만 model-turn candidate에 적용되며 accepted revision history와 superseded
contract는 session ledger에 남는다. Replacement step의 attempt는
`GoalLineageID` 기준으로 누적되어 이름/ID 변경으로 budget을 초기화할 수 없다.

- protocol/schema/prompt에 plan revision을 추가하고 native/shim mapping과 supported-kind parity row 확장
- revision reducer와 phase/step lineage/budget validation 추가
- obligation-preserving revision tests 추가
- evidence-based focus change와 blocked-step escalation 연결

### Stage 6: Compatibility cleanup

구현 상태: 완료. `Loop`의 package-local state mirror와 최초 hydrate 경로를 제거하고 production/test가
같은 `runtimeState` aggregate root를 사용하도록 통일했다. Turn-entry output policy와 중복되던
required-output, mixed-output, plain-answer, guidance lookup 보조 enforcer를 제거했으며 namespace,
read-only, target, mutation evidence matching 같은 domain/safety gate는 유지했다. 모든 flow 하위
package의 production import를 확인했고 public compatibility alias는 `internal/react` facade에 남겼다.
Mutation continuation budget을 잘못 소비하던 producer 없는 `BranchRecheckStep`, 미사용 lineage ID 조회,
문자열 `(y/n)` 감지 choice path와 nil-phase guide-progress fail-open 분기도 제거했다. Indexed
attempt/observation ledger와 같은 데이터를 이중 보관하던 `completedActions` mirror 및 그 rewind
trimming/projection 경로도 제거했다.

- obsolete Loop fields와 duplicate enforcers 제거
- compatibility aliases가 필요한 public facade만 유지
- architecture and runtime docs 갱신
- dead flow copies가 없는지 import graph 확인

## 14. Conflict Handling Rules

구현 중 다음 충돌이 발견되면 임의로 behavior를 선택하지 않고 사용자에게 먼저 확인한다.

1. 새 atomic event ordering이 기존 native/shim 출력 조합을 제거해야 하는 경우
2. 기존 lightweight one-response plan/action과 execution-confirmed completion을 유지할 수 없는 경우
3. plan revision이 forward-only phase invariant와 충돌하는 경우
4. mandatory mutation verification과 사용자 요청 종료가 충돌하는 경우
5. 기존 provider/tool schema가 step metadata를 전달하지 못해 behavior 변경이 필요한 경우
6. session source-of-truth 이전 중 현재 테스트가 의도된 동작과 문서 계약 중 어느 쪽을 따라야 할지
   불명확한 경우
7. context budget 때문에 goal/criterion/safety obligation 중 하나를 생략해야 하는 경우

질문 없이 변경할 수 있는 것은 내부 파일 배치, private helper 이름, 성능상 동등한 자료구조처럼
user-visible behavior와 safety invariant를 바꾸지 않는 구현 세부사항이다.

## 15. Verification Matrix

### 15.1 Output policy

- requirement analysis와 action 혼합은 state mutation 없이 거부
- phase progress와 action 혼합은 phase 변경 없이 거부
- guide progress 완료와 trailing action 혼합은 거부
- final report/next directions/mutation result exclusive enforcement
- progress text와 허용된 diagnostic action은 유지
- native/shim 결과가 같은 envelope와 gate outcome을 생성

### 15.2 Goal and step

- 같은 goal에서 다른 strategy를 선택할 수 있음
- accepted dispatch intent는 provisional attempt를 예약하고, invocation 전 cancelled intent는 최종
  attempt count에서 제외
- criterion ID/evidence reference validation
- achieved/blocked/replan step transition
- budget exhaustion 후 action 차단과 escalation
- phase 전체 교체와 step 교체 모두 revision 후 lineage budget 유지
- lightweight action success 후 별도 step_result 없이 execution-confirmed completion
- 유일한 active step에서 StepRef 없는 action 자동 binding
- 같은 goal의 strategy 변경은 revision 없이 action으로 진행
- goal/remaining graph 변경만 delta plan revision 사용

### 15.3 Existing safety

- read-only mutation block
- safe read-only pipeline 허용
- command risk exact approval과 permanent-skip 부재
- approval 누락/canonical hash mismatch/invocation parse failure가 실행 성공 경로가 아니라
  `rejected/not_invoked`로 종결
- batch 앞 call 실패 시 미실행 후속 call마다 동일 call ID의 `cancelled/not_invoked` result 생성
- pending dispatch recovery commit 실패가 busy loop 없이 pending intent를 보존하고 종료
- mutation evidence/result/continuation lifecycle
- conclusive=false budget final report
- namespace and target invariants
- CRD-only resource guidance
- guide completion과 phase completion 분리

### 15.4 Correction and lineage

- Plan 06/08 gate가 동일한 session correction state를 사용
- 같은 gate code/scope가 한 rejected turn에서 한 번만 증가
- 정상적인 step/phase/obligation 전진 시 scope counter reset
- threshold 도달 시 GateOutcome branch policy 적용
- correction, step attempt, verification evidence, mutation continuation, plan revision budget 상호 독립
- phase replacement가 descendant step attempt budget을 초기화하지 않음

### 15.5 Atomic transition and budgets

- 복수 state effect가 하나의 candidate state로 all-or-nothing commit
- invariant/revision failure에서 기존 root와 revision 불변
- successful commit당 state revision 정확히 1 증가
- external effect failure가 rollback 대신 follow-up transition을 생성
- provider별 차등은 protocol/schema correction profile에만 적용
- request 시작 시 적용된 budget profile이 고정되고 runtime hard bound를 벗어나지 않음

### 15.6 Context and UX

- long attempt history가 bounded projection으로 렌더링
- compaction 후 original goal/current criteria/obligation 보존
- Korean translation boundary 유지
- tool output 비번역과 data boundary 유지
- continuation input ownership 유지

## 16. Completion Criteria

Plan 08은 다음 조건을 모두 만족할 때 완료된다. 현재 1~13의 코드 구조는 구현됐다.

1. `session.Aggregate[runtimeState]`가 request, control, phase, step, attempt, obligation,
   continuation state를 포함한 하나의 revisioned mutable root를 소유한다.
2. 모든 model response는 consumer/state mutation 전에 provider-neutral envelope와 turn-entry policy를
   통과한다.
3. State-changing consumer는 직접 Loop field를 수정하지 않고 전체 candidate state를 원자적으로
   commit하며 successful transition당 revision을 한 번만 증가시킨다.
4. Model은 active step goal과 criterion, 이전 attempts를 bounded anchor로 받는다.
5. Dispatch admission 전에 거부된 output은 attempt가 아니다. 실행 대상으로 accepted되어 provisional
   attempt가 예약된 뒤 invocation 전에 rejected/cancelled되면 audit record는 보존하되 lineage budget과
   repetition matching에서 제외하고, 실제 invocation 또는 unknown execution만 attempt budget을 소비한다.
6. Plan revision은 evidence references와 base revision을 가지며 phase/step lineage budget을 상속하고
   mandatory obligation을 변경하지 못한다.
7. Plan 06과 Plan 08의 모든 correction은 하나의 `session.Corrections`에서 count/reset/escalation된다.
8. Step, verification evidence, mutation continuation, plan revision, correction budget이 독립적으로
   적용되고 request 시작 시 고정된 runtime-owned profile과 hard bound를 가진다.
9. Action은 기본적으로 runtime이 유일한 active/provisional step에 bind하고 optional StepRef는 assertion으로
   검증한다.
10. Lightweight lookup은 one-response plan/action을 유지하고 성공 observation으로 step/phase를 자동
   완료한 뒤 기존 final response path로 전환한다.
11. Native function calling과 shim mode가 동일한 lifecycle contract를 갖는다.
12. Existing request, guidance, mutation, approval, read-only, translation, continuation 기능이 regression
   matrix를 통과한다.
13. 기존 분산 enforcement와 compatibility mutable state가 제거되고 문서가 실제 import/state ownership과
    일치한다.

## 17. Known Residual Risks

다음은 구현 후에도 완전히 제거할 수 없는 의도된 한계다.

- LLM이 observation을 잘못 해석하거나 criterion을 잘못 만족했다고 판단할 수 있다.
- 새로운 goal lineage가 실제로 기존 goal의 표현 변경인지 runtime이 완전히 판정할 수 없다.
- Bounded prompt projection이 일부 과거 detail을 active attention에서 제거할 수 있다.
- Tool output data boundary가 model-level prompt injection을 완전히 제거하지 못한다.
- Attempt/revision budget이 매우 복잡하지만 유효한 진단을 조기에 중단할 수 있다.
- Plan revision이 필요한 실제 goal/graph 변경은 최소 한 번의 추가 model turn과 schema correction 위험을
  가진다.

이 한계는 runtime이 의미상의 사실 판정자가 아니라 procedure, safety, state ownership의 집행자라는
경계를 유지하기 위해 수용한다. 사용자가 명시적으로 다른 판단 주체나 보장 수준을 요구하면 구현 전에
contract를 다시 확정한다.
