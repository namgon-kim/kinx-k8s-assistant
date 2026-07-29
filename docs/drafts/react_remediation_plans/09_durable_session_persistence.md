# Plan 09: Durable Session Persistence and Recovery

> 상태: Plan 08에서 파생된 상세 구현 계획. 아직 구현되지 않음.
>
> 선행 계획: [`08_turn_output_contract_and_goal_execution.md`](./08_turn_output_contract_and_goal_execution.md)
>
> Plan 09는 checkpoint, append-only journal, process crash recovery, retention, file security만 다룬다.
> Plan 08의 turn output contract와 goal/step runtime은 durable persistence 없이도 독립적으로 구현하고
> 배포할 수 있어야 한다. Plan 09의 파일 경로나 recovery 정책이 결정되지 않았다는 이유로 Plan 08의
> 배포를 막지 않는다.

## 1. 목적

Plan 08은 한 process 안에서 다음 상태를 일관되게 관리한다.

- original request와 accepted requirement
- control state와 state revision
- phase/step goal과 completion criteria
- attempts와 observation references
- plan revisions
- mandatory obligations
- correction state
- request-fixed budget profile과 verification evidence counters

Plan 09는 이 in-memory session state를 process 종료, crash, context 손실 이후에도 복구할 수 있게 한다.
특히 Kubernetes mutation이 실행됐지만 process가 결과를 기록하지 못한 경우 같은 mutation을 자동으로
재실행하지 않도록 하는 것이 가장 중요한 safety goal이다.

## 2. Plan 08과의 경계

Plan 08이 보장하는 것:

- model output의 deterministic validation
- event/effect ordering
- session 단일 mutable source of truth
- goal/step/attempt lifecycle
- plan revision과 mandatory obligation
- in-memory attempt ledger와 bounded prompt projection

Plan 09가 추가로 보장하는 것:

- session checkpoint 저장과 복원
- append-only execution event 기록
- observation payload의 bounded durable storage
- mutation execution 전 write-ahead record
- incomplete execution의 fail-closed recovery
- schema migration, retention, permission, redaction

Plan 08의 contract 타입은 persistence를 고려해 stable ID와 serializable field를 사용하지만,
`flow`, `session`, `coordinator`가 파일 저장 성공을 turn output validation의 전제조건으로 삼지 않는다.
Persistence integration은 coordinator가 실행하는 별도 effect boundary다.

Plan 09는 max-iteration closure 진입 조건, mandatory obligation 우선순위,
`continuation_handoff` 검증 또는 continuation segment transition을 정의하지 않는다.
Runtime이 이미 commit한 `ContinuationState`를 persistence input으로 받아 동일한
state와 counters를 저장·복원할 뿐, closure 시점이나 다음 workflow action을 새로
판단하지 않는다.

## 3. Non-Goals

- LLM이 session directory를 직접 탐색하도록 하지 않는다.
- 모든 raw Kubernetes output을 무제한 보관하지 않는다.
- 여러 host 사이에서 distributed consensus를 제공하지 않는다.
- process crash 이전에 kernel이나 storage가 실제로 durable write를 완료했다고 절대적으로 보장하지 않는다.
- Plan 08의 output policy, phase validation, correction semantics를 다시 정의하지 않는다.
- 기존 `/save` config 저장과 CLI input history의 의미를 변경하지 않는다.

## 4. Proposed Package Boundary

```text
internal/react/
  persistence/
    store.go              # session store interface
    file_store.go         # file-backed implementation
    checkpoint.go         # checkpoint codec and atomic replace
    journal.go            # append-only event codec
    observation.go        # bounded observation payload storage
    recovery.go           # replay and incomplete execution handling
    migration.go          # schema version migration

  coordinator/
    persistence.go        # persistence effects and user-visible failures

  contract/
    persistence.go        # immutable persisted envelope metadata
```

`session`은 파일 I/O를 수행하지 않는다. Coordinator가 immutable session snapshot과 accepted runtime
events를 persistence effect로 전달한다. Persistence package는 workflow 결정을 하지 않고 저장, 읽기,
형식 검증, replay input 생성만 담당한다.

## 5. Storage Layout

기본 제안:

```text
~/.k8s-assistant/sessions/<session-id>/
  checkpoint.json
  events.jsonl
  observations/
    <observation-id>.json
```

구현 전에 다음을 확인한다.

- 기존 config, input history, save-session convention과 경로 충돌이 없는가
- 사용자별 session retention 기본값이 필요한가
- cluster identity, kube context, namespace를 어느 수준까지 파일에 기록할 것인가
- 로그와 observation에 secret이 포함될 가능성을 어떤 정책으로 처리할 것인가

경로나 retention 선택이 기존 동작과 충돌하면 구현 전에 사용자에게 확인한다. 해당 결정이 지연돼도
Plan 08 구현과 배포는 계속할 수 있다.

## 6. Persisted Model

### 6.1 Checkpoint envelope

```go
type Checkpoint struct {
    SchemaVersion int
    SessionID     string
    StateRevision uint64
    WrittenAt     time.Time
    State         contract.SessionSnapshot
    LastEventID   string
}
```

Checkpoint에는 현재 실행과 복구에 필요한 normalized state만 저장한다.

- original goal and accepted request context
- current control and input owner
- current plan revision
- phase/step contracts and status
- attempt counters and compact outcomes
- normalized attempt/observation ledger records and their order/index data
- immutable tool dispatch intents
  - dispatch ID, attempt ID, canonical payload hash, normalized payload and status
- applied budget profile and independent budget counters
- active mandatory obligations
  - mutation verification shape, active chain index, per-check status, evidence refs, timing, and recheck counters
- correction counters
- continuation state
  - closure reason and bounded handoff summary
  - resumable request/goal/phase/step lineage IDs
  - total iteration count, continuation segment number and segment iteration count
  - request-fixed max iteration and closure reserve
- typed evidence references for tool observations, mutation results, external state and user continuation input

Model prompt 원문, 전체 streaming text, 전체 raw output은 checkpoint에 저장하지 않는다.

현재 in-memory `session.ExecutionLedger`는 map/index를 package-private field로 소유한다. 이를
`encoding/json`으로 직접 marshal하면 ledger content가 아니라 빈 object가 되므로 checkpoint는
`GoalExecutionState` 또는 `ExecutionLedger`를 그대로 직렬화하지 않는다. Persistence adapter가
`OrderedAttempts()`와 `OrderedObservations()`에서 versioned DTO를 만들고, load 시 append API로
index를 재구성한 뒤 ledger audit을 통과시켜야 한다. `lineageAttempts`와 observation position map은
중복 persisted source of truth가 아니라 재생성 가능한 index다.

`applied budget profile`은 checkpoint envelope의 중복 필드가 아니라
`contract.SessionSnapshot` 안의 필수 request substate로 저장한다. 최소 persisted shape은 다음 의미를
보존해야 한다.

```go
type AppliedBudgetState struct {
    ProfileID      string
    PolicyVersion  string
    ProtocolMode   string
    Provider       string
    Model          string
    AppliedLimits  contract.BudgetLimits
    Counters       contract.BudgetCounters
}

type SessionSnapshot struct {
    // Other normalized request/session state is omitted here.
    AppliedBudget AppliedBudgetState
}
```

`AppliedLimits`는 Plan 08 §6.9의 request 시작 시 clamp까지 끝난 실제 적용값이다. `Counters`는
correction, step attempt, verification evidence, mutation continuation, plan revision, max iteration의
독립 축과 각 scope key를 모두 보존한다. 복구 시 profile 이름만 저장한 뒤 현재 config에서 limit을 다시
계산해서는 안 된다. 동일한 profile 이름의 설정값도 실행 중 변경될 수 있기 때문이다.

### 6.2 Journal event envelope

```go
type PersistedEvent struct {
    SchemaVersion int
    EventID       string
    SessionID     string
    StateRevision uint64
    OccurredAt    time.Time
    Kind          string
    Payload       json.RawMessage
}
```

예상 event:

- `session_started`
- `requirement_accepted`
- `plan_accepted`
- `plan_revised`
- `step_activated`
- `attempt_started`
- `dispatch_intent_committed`
- `dispatch_reconciled`
- `dispatch_outcome_unknown`
- `dispatch_rejected_before_invocation`
- `dispatch_cancelled_before_invocation`
- `action_rejected`
- `observation_recorded`
- `user_input_evidence_recorded`
- `step_achieved`
- `step_blocked`
- `obligation_created`
- `obligation_satisfied`
- `correction_recorded`
- `budget_profile_applied`
- `budget_consumed`
- `budget_scope_closed`
- `final_report_accepted`
- `continuation_handoff_committed`
- `continuation_checkpoint_written`
- `session_resume_requested`
- `session_resumed`
- `session_completed`

Journal event는 이미 Plan 08 runtime에서 수락된 event를 기록한다. Journal replay가 새로운 workflow
판단을 만들면 안 된다. Budget event에는 budget 축, scope key, 변경 전후 값, 적용 limit을 기록해
checkpoint 이후 tail replay에서도 counter를 동일하게 재구성한다.

### 6.3 Observation storage

Observation file은 다음 metadata를 가진다.

```text
observation ID
attempt ID
evidence kind
tool name
target reference
exit/success/failure class
content hash
bounded structured result
redaction metadata
```

대용량 YAML, logs, events는 bounded projection과 hash를 저장한다. Full payload 보관이 필요한 경우 별도
config와 retention 정책을 요구하며 기본 동작으로 두지 않는다.

`user_input` evidence는 tool observation과 같은 ID/reference lifecycle을 사용하지만 raw text 보존과
redaction 정책은 별도로 둔다. Checkpoint에는 plan revision을 재검증할 수 있는 bounded normalized
record를 저장하고, 전체 CLI input history를 durable evidence storage로 복제하지 않는다.

## 7. Write Ordering

### 7.1 Read-only action

```text
in-memory attempt proposed
-> execute read-only tool
-> create observation
-> append observation_recorded
-> update checkpoint
```

Read-only observation 저장에 실패하면 현재 process에서는 결과를 사용할 수 있지만, recovery 시 해당
결과가 없음을 user-visible warning과 session status로 남긴다.

### 7.2 Mutation action

Mutation은 write-ahead 순서를 지킨다.

```text
receive committed dispatch persistence request
-> append and flush attempt_started + dispatch_intent_committed
-> checkpoint the same pending dispatch IDs and canonical payload hashes
-> return durable acknowledgement for the dispatch IDs
... external mutation executes outside the persistence package ...
-> receive observation_recorded or execution_outcome_unknown persistence request
-> append observation_recorded or execution_outcome_unknown
-> persist the committed mutation verification obligation
-> update checkpoint
```

`attempt_started`와 `dispatch_intent_committed`를 durable하게 기록하지 못하면 mutation을
실행할 수 있는 durable acknowledgement를 반환하지 않는다. Persistence request,
journal과 checkpoint는 같은 committed dispatch ID와 canonical payload hash를 사용해야
하며 서로 다르면 fail-closed 처리한다.

Checkpoint가 pending dispatch를 기록한 뒤 process가 invocation 전 또는 invocation 도중
종료되면 실제 실행 여부를 추측하지 않는다. Recovery는 mutating dispatch를 unknown으로
전환해 verification을 요구하며 자동 재실행하지 않는다.

Context cancellation과 정상 shutdown도 pending dispatch를 지우는 일반 cleanup보다 먼저 같은
reconciliation 절차를 수행한다.

```text
shutdown requested
-> inspect committed pending dispatches
-> if invocation-not-started is proven: persist cancelled + synthetic tool result
-> otherwise: persist execution_outcome_unknown + mandatory verification
-> flush journal and replace checkpoint
-> only then commit exited control
```

`AwaitingToolResult`에서 곧바로 `Exited`로 전환하거나 pending dispatch map을 비우면 안 된다.
Cancellation 중 reconciliation/checkpoint가 실패하면 마지막 durable pending intent를 유지하고
recovery-required 상태로 종료한다. 실패한 cleanup commit을 로그만 남긴 채 정상 종료로 간주하지
않는다.

### 7.3 Checkpoint replacement

```text
write checkpoint.tmp
-> flush file
-> validate serialized envelope
-> atomic rename to checkpoint.json
-> flush parent directory where supported
```

기존 valid checkpoint는 새 checkpoint 교체가 완료되기 전까지 유지한다.

## 8. Recovery State Machine

### 8.1 Normal recovery

```text
read checkpoint
-> validate schema/session/revision
-> replay journal after LastEventID
-> verify monotonic revisions
-> rebuild coordinator.runtimeState and session.Aggregate revision including request-fixed applied budget profile, limits, and counters
-> audit mandatory obligations
-> publish recovered snapshot
```

복구된 active request에는 checkpoint와 journal이 가진 적용값을 그대로 사용한다. 복구 시점의 provider,
model, protocol mode 또는 config가 달라졌더라도 현재 request의 limit을 다시 선택하거나 clamp하지 않는다.
현재 config는 recovery 이후 시작하는 새 request에만 적용한다.

### 8.2 Mutation outcome unknown

`attempt_started`가 있지만 observation이나 explicit failure가 없으면 다음 상태로 복구한다.

```text
control: awaiting_mutation_verification_evidence
         or awaiting_mutation_verification_chain_evidence
execution status: unknown
automatic mutation retry: forbidden
required next output: read-only reconciliation action
```

Runtime은 같은 AttemptID 아래 single 또는 ordered verification을 복원한다. Cluster observation 이후
model은 exact VerificationID와 evidence refs로 satisfied, waiting, failed를 판정한다. `waiting`이면
복구 전 recheck count와 request-fixed budget을 유지하고 mutation을 재실행하지 않는다. Ordered chain은
checkpoint의 active index와 check status를 복원해 완료한 check를 다시 실행하거나 순서를 건너뛰지 않는다.

### 8.3 Corrupt or partial journal

- 마지막 줄만 partial이면 마지막 valid event까지만 replay하고 recovery warning을 남긴다.
- 중간 event가 손상됐으면 이후 event를 신뢰하지 않고 fail-closed 상태로 전환한다.
- state revision이 감소하거나 중복 conflict가 있으면 자동 merge하지 않는다.
- active mutation intent가 포함된 구간이 불명확하면 reconciliation을 요구한다.

### 8.4 Stale checkpoint

Journal이 checkpoint보다 앞서 있으면 replay한다. Checkpoint가 journal보다 앞서거나 event chain이 맞지
않으면 user confirmation 없이 임의로 history를 삭제하거나 되감지 않는다.

### 8.5 Missing or incompatible budget profile

Active request checkpoint에 applied budget profile, 실제 limit 또는 필수 counter가 없으면 현재 config로
조용히 보충하지 않는다. Schema migration이 당시 적용값을 손실 없이 결정할 수 있을 때만 변환하고,
그렇지 않으면 incompatible checkpoint로 fail-closed load한다. Unknown `PolicyVersion`, budget 축 또는
scope encoding도 같은 규칙을 따른다. 원본 파일은 유지하고 새 request로 시작할지는 사용자 확인을
받는다.

### 8.6 Persistence of an accepted continuation handoff

Runtime이 closure와 mandatory obligation 처리를 마치고 `ContinuationState`를
commit한 뒤에만 continuation checkpoint를 기록한다. Plan 09는 closure threshold를
다시 계산하거나 model에게 handoff를 직접 요청하지 않는다.

Checkpoint는 최소한 다음을 보존한다.

- original request와 accepted goal
- request ID와 goal/phase/step lineage IDs
- current plan revision과 superseded history
- completed, blocked, active step status
- attempt ledger와 observation references
- pending mutation verification과 다른 mandatory obligations
- request 시작 시 고정된 applied budget limits
- 사용한 step attempt, verification evidence, mutation continuation, correction과 plan
  revision counter
- total iteration count
- 당시 적용한 `MaxIterations`, `ClosureReserve`와 closure 진입 iteration
- bounded handoff summary와 다음에 수행할 active step

Runtime의 accepted handoff commit과 file checkpoint 성공은 서로 다른 결과다.
Persistence boundary는 다음 순서를 지킨다.

```text
receive committed ContinuationState snapshot
-> append and flush continuation_handoff_committed
-> atomically replace checkpoint
-> verify checkpoint session ID, state revision and content hash
-> append continuation_checkpoint_written audit marker
-> return verified resumable checkpoint metadata
```

Checkpoint write 또는 검증이 실패하면 이전 valid checkpoint를 유지하고 현재 in-memory
`ContinuationState`도 되돌리지 않는다. 다만 file resume이 가능하다고 사용자에게
표시할 수 있는 success result를 반환하지 않는다. Coordinator가 persistence failure와
현재 process 안에서만 continuation 가능한 상태를 사용자에게 알린다. 검증된 checkpoint
이후 audit marker append만 실패한 경우에는 checkpoint
자체의 resumability를 무효화하지 않고 journal telemetry degraded warning을 남긴다.
`continuation_checkpoint_written`은 workflow state를 변경하지 않는 audit event이므로
replay가 handoff를 다시 commit해서는 안 된다.

사용자가 이후 계속 수행하겠다는 요청을 입력하면 coordinator는 명시적으로 선택된 file
checkpoint를 읽어 동일 session을 resume할 수 있다. 임의의 새 요청에 가장 최근
checkpoint를 자동 결합하지 않는다.

Resume 절차:

1. resumable session ID와 checkpoint schema를 검증한다.
2. checkpoint의 cluster identity, kube context와 namespace scope가 현재 실행 환경과
   호환되는지 확인한다.
3. original goal, 마지막 수행 내용과 unresolved step을 사용자에게 보여준다.
4. 사용자가 continuation을 선택하면 checkpoint와 journal tail을 replay한다.
5. 기존 request ID, goal/phase/step lineage와 plan revision history를 유지한다.
6. 기존 attempt, verification evidence, mutation continuation, correction과 plan
   revision counter를 유지한다.
7. unresolved mutation 또는 in-flight dispatch reference를 runtime recovery input에
   손실 없이 제공한다. 우선순위와 다음 action은 persistence가 결정하지 않는다.

File recovery는 checkpoint에 저장된 `TotalIterations`, `ContinuationSegment`,
`SegmentIterations`, step attempt, verification evidence, mutation continuation,
correction과 plan revision counter를 값 그대로 복원한다. 어느 값을 증가시키거나
초기화할지는 persistence가 아니라 recovery 이후 runtime transition의 책임이다.

## 9. Failure Semantics

| Failure | Runtime behavior |
| --- | --- |
| Session directory 생성 실패 | persistence 비활성/실패를 알리고 Plan 08 in-memory session 유지 여부를 policy에 따라 결정 |
| Read-only observation 저장 실패 | 현재 turn은 계속 가능, recovery gap 경고 |
| Mutation intent 저장 실패 | mutation 실행 금지 |
| Mutation 결과 저장 실패 | execution outcome unknown, verification 강제 |
| Shutdown 중 pending dispatch reconciliation 실패 | durable pending intent 유지, recovery-required 종료, 자동 재실행 금지 |
| Checkpoint atomic replace 실패 | 이전 valid checkpoint 유지 |
| Schema migration 실패 | fail-closed load, 원본 파일 유지 |
| Retention cleanup 실패 | 현재 session 실행과 분리, warning 기록 |

Persistence failure가 output policy violation이나 model correction count로 기록되면 안 된다. 이는
external-state/persistence failure이며 별도의 `GateOutcome` code와 retry scope를 사용한다.

## 10. File Safety and Data Boundary

- owner-only directory/file permission
- symlink/path traversal 방지
- atomic checkpoint replacement
- bounded file and observation size
- token, credential, Secret data redaction
- control character sanitization
- observation을 instruction이 아닌 quoted structured data로 projection
- unknown field preservation 또는 versioned rejection 정책 명시
- session ID와 kube context mismatch audit

Tool output data boundary는 prompt injection 위험을 낮추지만 model-level injection을 완전히 제거하지
못한다. Persistent file에서 prompt projection을 만들 때도 Plan 08과 동일한 boundary를 적용한다.

## 11. Retention and Cleanup

Retention은 구현 전에 다음 정책을 확정한다.

- completed session 기본 보관 기간
- active/incomplete session 보관 기간
- observation payload 최대 총량
- explicit delete/reset 동작
- `/clear`, `/reset`, `/exit`과 durable session의 관계
- user가 session 복구를 원하지 않을 때의 opt-out

Cleanup은 active session checkpoint를 삭제하지 않는다. Retention 삭제는 workflow event와 분리하고,
실패해도 현재 ReAct turn을 중단하지 않는다.

## 12. Migration Stages

### Stage 0: Storage contract

- 저장 경로와 retention 충돌 확인
- checkpoint/event schema version 정의
- store interface와 failure classes 정의
- read-only와 mutation durability 수준 분리

### Stage 1: Checkpoint and replay

- in-memory store reference implementation
- file checkpoint codec
- atomic replacement
- snapshot round-trip and replay audit

### Stage 2: Append-only journal

- event append and monotonic revision
- partial write handling
- checkpoint plus tail replay
- correction/obligation event persistence

### Stage 3: Mutation write-ahead recovery

- attempt intent persistence
- outcome unknown classification
- verification/reconciliation transition
- duplicate mutation prevention

### Stage 4: Observation storage and retention

- bounded observation files
- redaction and data boundary
- retention cleanup
- config and user controls

### Stage 5: Compatibility and operations

- existing config/history/save behavior regression
- schema migration
- recovery diagnostics
- max-iteration continuation checkpoint와 explicit resume
- documentation and operational support

## 13. Conflict Handling Rules

다음 상황은 임의로 결정하지 않고 사용자에게 확인한다.

1. session 경로가 기존 config/history/save-session convention과 충돌함
2. mutation durability를 위해 현재보다 강한 disk write 실패 정책이 필요함
3. raw observation retention 요구가 secret/redaction 정책과 충돌함
4. `/clear`, `/reset`, `/exit`이 durable session을 삭제할지 보존할지 기존 UX로 결정할 수 없음
5. schema migration이 기존 session을 손실 없이 변환할 수 없음
6. host/storage 특성 때문에 문서의 atomicity 수준을 제공할 수 없음

## 14. Verification Matrix

### 14.1 Checkpoint and replay

- empty/new session round trip
- active goal/phase/step/attempt round trip
- mandatory obligation and correction state round trip
- request-fixed applied budget profile, limits, and all independent counters round trip
- checkpoint 이후 budget journal event replay
- recovery 시 config/provider/model 값이 변경돼도 active request의 applied limits 유지
- checkpoint plus journal tail replay
- monotonic state revision enforcement
- max-iteration handoff checkpoint round trip
- explicit resume 후 request/goal/phase/step lineage 유지
- resume 후 기존 attempt, verification, correction과 revision counter 유지
- 새 continuation segment에서 segment iteration만 초기화되고 total iteration은 누적

### 14.2 Crash points

- before read-only invocation
- after read-only invocation, before observation append
- before mutation intent flush
- after mutation invocation, before result append
- after result append, before checkpoint replace
- during checkpoint temporary write and rename

### 14.3 Corruption and migration

- partial final journal line
- corrupt middle journal event
- stale checkpoint
- incompatible schema version
- applied budget profile 또는 필수 counter가 없는 active request checkpoint
- unknown budget policy version/scope encoding
- failed migration preserving original files

### 14.4 Security and retention

- file permissions
- symlink/path traversal rejection
- secret redaction
- bounded observation size
- active session retention exclusion

## 15. Completion Criteria

Plan 09는 다음 조건을 모두 만족할 때 완료된다.

1. Plan 08은 persistence failure 없이 독립적으로 동작하고 Plan 09를 optional integration으로 사용한다.
2. Checkpoint와 journal replay가 동일한 `coordinator.runtimeState` snapshot과
   `session.Aggregate` revision을 생성한다.
3. Mutation 실행 전 intent persistence 실패 시 command가 실행되지 않는다.
4. Mutation 결과가 불명확한 recovery에서 command를 자동 재실행하지 않는다.
5. Corrupt/stale state를 자동으로 덮어쓰거나 유효한 원본을 삭제하지 않는다.
6. Existing config, history, `/save`, `/clear`, `/reset`, `/exit` behavior와의 관계가 문서화된다.
7. File permission, redaction, bounded retention 정책이 적용된다.
8. Persistence-specific failure가 model correction 또는 step attempt budget과 섞이지 않는다.
9. Active request의 applied budget profile, 실제 limit, 독립 counter가 정확히 복원되며 현재 config로
   재계산되지 않는다.
10. Max-iteration continuation이 file checkpoint에서 동일 request/goal lineage와 기존
    safety budget counter를 유지한 채 resume된다.

## 16. Known Residual Risks

- LLM이 persistent observation을 잘못 해석할 수 있다.
- Filesystem과 host 장애에 대해 완전한 durability를 보장할 수 없다.
- Redaction이 모든 민감 정보를 의미적으로 식별하지 못할 수 있다.
- 장기 session의 journal과 observation 저장량이 증가할 수 있다.
- Schema migration이 모든 과거 개발 버전의 임시 형식을 지원하지 못할 수 있다.

이 위험은 Plan 08의 turn validation과 분리해 관리한다. Persistence failure로 Plan 08의 이미 검증된
output contract 개선 배포를 되돌리지 않는다.
