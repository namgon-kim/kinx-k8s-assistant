# Orchestrator and ReAct Architecture

이 문서는 현재 코드 기준으로 CLI 입력부터 ReAct model turn, Kubernetes tool 실행,
사용자 출력까지의 구조를 설명한다. 안정된 외부 경계와 내부 package 분리를 다루며,
현재 session source-of-truth와 compatibility가 필요한 외부 facade를 구분해서 기록한다.

## Scope

주요 실행 경계는 다음과 같다.

| 위치 | 책임 |
| --- | --- |
| `cmd/k8s-assistant` | config/flag/env를 읽고 interactive CLI를 시작한다. |
| `internal/orchestrator` | readline, meta command, active agent 교체, formatter, incident-guidance side-flow를 소유한다. |
| `internal/react/react.go` | 외부 facade다. `New`, `Loop`, runtime snapshot/input 관련 공개 alias를 제공한다. |
| `internal/react/coordinator` | model/input/tool/output I/O와 한 iteration의 실행 순서를 조정한다. |
| `internal/react/session` | generic revisioned aggregate, lifecycle projection, goal/phase/step execution ledger를 제공한다. Package-private workflow root는 `coordinator.runtimeState`다. |
| `internal/react/flow` | I/O 없는 request/phase/guidance/verification/report/direction/gate 규칙을 둔다. |
| `internal/react/contract` | enum, event/effect, structured payload, snapshot처럼 공유되는 immutable 계약을 둔다. |
| `internal/react/protocol` | runtime internal call 이름, schema, native/shim normalization을 담당한다. |
| `internal/react/kube` | kubectl command/resource/target 파싱과 read-only 판정을 담당한다. |
| `internal/react/prompt` | prompt template rendering과 requirement-analysis prompt 조립을 담당한다. |
| `internal/react/provider` | main LLM provider setup을 담당한다. |
| `internal/react/language` | 선택형 user-facing translation client를 담당한다. |
| `internal/toolconnector` | kubectl-ai tool registry와 선택형 MCP tool을 연결한다. |
| `internal/guidance` | resource/incident guide 검색과 planning을 담당하며 Kubernetes 명령을 직접 실행하지 않는다. |

## Current Package Layout

```text
internal/react/
├── react.go
├── coordinator/
│   ├── loop.go
│   ├── iteration.go
│   ├── input.go
│   ├── execution.go
│   ├── execution_state.go
│   ├── output.go
│   ├── dependencies.go
│   ├── state.go
│   ├── turn_output.go
│   ├── turn_effects.go
│   └── revision.go
├── session/
│   ├── aggregate.go
│   ├── control.go
│   └── execution.go
├── flow/
│   ├── request/
│   ├── phase/
│   ├── guidance/
│   ├── verification/
│   ├── report/
│   ├── direction/
│   └── gate/
├── contract/
│   ├── enums.go
│   ├── events.go
│   ├── effects.go
│   ├── control.go
│   ├── model_output.go
│   ├── execution.go
│   ├── clone.go
│   ├── structured.go
│   ├── action.go
│   └── snapshot.go
├── protocol/
├── kube/
├── prompt/
├── provider/
└── language/
```

외부 package는 `internal/react/coordinator`를 직접 의존하지 않고 `internal/react` facade를
사용한다. 이 규칙은 내부 디렉터리를 다시 정리해도 orchestrator의 호출 계약이 흔들리지
않게 한다.

## Dependency Direction

의도한 의존 방향은 다음과 같다.

```text
cmd/k8s-assistant
        |
        v
internal/orchestrator ---> internal/react (facade)
                                |
                                v
                        react/coordinator
                         /      |       \
                        v       v        v
                    session    flow   protocol/kube
                       \        |       /
                        v       v      v
                         react/contract
```

- `contract`는 다른 ReAct package의 구체 구현을 의존하지 않는다.
- `flow`는 Kubernetes I/O나 model client를 호출하지 않는 규칙 계층이다.
- `session`은 mutable 값을 보관하지만 model/tool I/O를 수행하지 않는다.
- `coordinator`만 이 규칙과 상태를 실제 provider/tool/input/output에 연결한다.
- `guidance`는 진단 정보를 반환할 뿐 변경 명령을 직접 실행하지 않는다.

## State Boundaries

상태 이름은 서로 다른 축을 섞지 않는다.

| State axis | 의미 | 예 |
| --- | --- | --- |
| `LoopLifecycleState` | goroutine/UI 실행 형태 | model turn, approval 대기, continuation 입력 대기, 종료 |
| `RuntimeControlState` | runtime이 다음에 받아야 하는 obligation | requirement analysis, phase plan, tool approval, verification result |
| `PhaseStatus` | model-declared top-level phase의 진행 상태 | pending, active, completed, skipped, superseded |
| `StepStatus` | phase 아래 실제 실행/guide/verification step 상태 | pending, active, achieved, blocked, skipped, superseded |
| `InputOwner` | 현재 사용자 입력을 소비할 계층 | orchestrator, ReAct choice/text, approval |

`RuntimeControlState`는 phase 이름이 아니다. 예를 들어
`awaiting_mutation_verification_result`는 현재 phase가 무엇이든 runtime이 먼저 해결해야 할
검증 obligation이다. 반대로 `guided_diagnosis`는 model plan의 phase이며 그 내부의 다음
guide action은 `awaiting_guided_diagnosis_step` control로 나타날 수 있다.

현재 control enum과 lifecycle projection은 `contract/enums.go`와 `session/control.go`에 있다.
실행 중 mutable 값은 `session.Aggregate[runtimeState]`가 소유하는 하나의 revisioned root에 묶인다.

```text
runtimeState root
├── Control
├── Phase
├── Verification
├── Context
├── immutable dispatch intents
├── continuation segment state
└── GoalExecutionState
    ├── request-fixed BudgetPolicy
    ├── goal/phase/step/criterion contracts
    ├── accepted plan revision history
    ├── attempt and observation ledger
    └── correction and verification-evidence counters
```

coordinator의 `RuntimeSnapshot()`은 aggregate root를 deep copy해 외부에서 읽을 detached snapshot을 만든다.
Snapshot projection은 clone된 `runtimeState`와 revision만 받는 pure helper이며, dependency가 비어 있는
임시 `Loop`를 만들지 않는다. Runtime data clone은 map/slice/pointer/array뿐 아니라 exported struct
field 안의 참조 값도 재귀적으로 분리하므로 candidate와 published snapshot이 committed root의 tool
result를 공유하지 않는다.
Model turn은 turn-entry revision을 기록하고 deep candidate를 만든 뒤 invariant audit와 revision 비교를
통과한 경우에만 root를 한 번 교체한다. 성공한 candidate commit은 revision을 정확히 한 번 증가시킨다.
orchestrator는 snapshot의 `Control`과 `InputOwner`를 사용해 입력을 agent에 보낼지, meta
command로 처리할지 결정한다.

## Migration Status

package split과 model-turn state ownership 이전은 완료됐다.

| 항목 | 상태 |
| --- | --- |
| facade와 implementation 분리 | 완료 |
| enum/structured payload/snapshot의 `contract` 분리 | 완료 |
| revisioned session aggregate와 deep candidate/snapshot 도입 | 완료 |
| request/phase/guidance/verification/report/direction/gate 규칙 package 도입 | 완료 |
| phase validation/progress, guidance lookup/progress, verification matching/continuation, report/direction normalization의 production 경로 연결 | 완료 |
| gate outcome 계약/target validation과 correction message 선택의 production 경로 연결 | 완료 |
| protocol, kube, prompt, provider, language 분리 | 완료 |
| model-turn state를 session aggregate root 하나로 단일화 | 완료 |
| native/shim provider-neutral output envelope와 turn-entry policy 연결 | 완료 |
| state candidate commit과 request/tool/guidance/translation/message external effect 분리 | 완료 |
| stable goal/phase/step/criterion contract와 active-step action binding | 완료 |
| native/shim `step_result`, request-fixed budget, phase completion gate | 완료 |
| full attempt/observation ledger와 bounded prompt/compaction projection | 완료 |
| command risk 계약과 immutable two-phase tool dispatch | 완료 |
| evidence qualification, verification 단일 종료, max-iteration handoff | 완료 |
| committed compaction effect와 shim invalid-output 단일화 | 완료 |
| session-scoped GateOutcome correction counter 통합 | 완료 |
| evidence-based `phase_plan_revision`, lineage 상속, revision budget | 완료 |
| package-local test fixture seed 필드와 turn-entry duplicate enforcer 제거 | 완료 |
| public compatibility alias를 `internal/react` facade로 제한 | 완료 |
| flow package production import graph 연결 | 완료 |

`Loop`는 `runtimeState` root를 한 번만 참조한다. Package-local 테스트도 별도 mirror field 대신
`runtimeState` fixture를 만들고, `mutableRuntime()`이 이를 `session.Aggregate` root로 채택한다.
Runtime state의 이중 hydrate 또는 dual write 경로는 없다.

## Runtime Flow

### 1. Query start

1. CLI가 일반 입력과 meta command를 구분한다.
2. 일반 query는 `react.New`로 만든 facade `Loop`에 전달된다.
3. coordinator가 provider, tool registry, sandbox executor, prompt, guidance client를 준비한다.
4. request context를 초기화하고 control을 `awaiting_requirement_analysis`로 전환한다.

예: 사용자가 `tests 네임스페이스의 실패한 pod를 확인해줘`라고 입력하면 orchestrator는
이를 `/config` 같은 meta command로 처리하지 않고 active ReAct loop에 전달한다.

### 2. Requirement and phase setup

1. model은 먼저 structured `requirement_analysis`를 반환한다.
2. `contract.RequirementAnalysis`로 정규화하고 `flow/request` 규칙으로 intent/context를 만든다.
3. accepted request context는 session context에 보관된다.
4. 다음 model turn은 `phase_plan`을 요구한다.
5. `flow/phase.Validate`가 plan graph를 검증하고 session phase state가 current/completed 상태를 보관한다.
6. Runtime이 accepted plan을 stable goal/phase/step/criterion ID와 고정 budget profile을 가진
   `GoalExecutionState`로 투영한다.
7. 이후 observation이 active/remaining graph를 무효화하면 model은 단독 `phase_plan_revision`을
   제출한다. `flow/phase`는 base revision, observation reference, revision budget, phase/step
   replacement와 lineage 보존을 검증하고 통과한 graph만 candidate state에 적용한다.

예: 직전 query가 `tenant-a`의 Deployment를 대상으로 했다면 `그럼 rollout은?` 같은
follow-up은 이전 target/scope를 기본값으로 사용할 수 있다. 새 query가 명시적으로 다른
namespace나 all-namespaces를 지정하면 새 값이 우선해야 한다.

### 3. Model step and gate

coordinator는 model 응답을 native function calls 또는 shim JSON에서
`contract.ModelOutputEnvelope`로 정규화한다. Envelope는 progress text, plain answer, internal event,
external action, invalid output을 분리한다. `flow/gate`의 turn-entry policy를 domain consumer보다 먼저
적용한 뒤 runtime obligation, phase, safety, correction 규칙을 적용한다.

- runtime internal call: requirement, phase plan/revision, step result, phase progress, guide progress,
  verification result, continuation handoff, final report, next directions
- real action: kubectl, bash, configured MCP/tool call
- user-facing text: answer, progress, correction

State-changing internal event와 action 혼합, required-exclusive output 위반, unknown shim key는 state
candidate를 적용하기 전에 차단된다. 허용된 response는 detached candidate에만 적용되고 audit 성공 후
atomic commit된다. Accepted-request 준비, tool과 guidance lookup은 commit 뒤 coordinator effect runner가
실행하며 translation/message emit도 commit 이후로 지연된다. Observation은 새 candidate transition으로
기록한다. 기존 세부 domain consumer를 각각 pure reducer로 옮기는 작업은
남아 있지만 이 consumer들은 더 이상 committed root를 부분 변경하지 않는다.

### 4. Approval and execution

1. `kube` helper가 kubectl command, target, mutation/read-only 특성을 판정한다.
2. read-only 위반은 실행 전에 차단한다.
3. command 실행형 tool은 `risk.risky`를 반드시 선언한다. `risky=true`인 call만 exact command와
   이유를 한 번의 승인 요청에 묶으며, 영구 승인 생략 상태는 두지 않는다. Read-only, target,
   mutation verification은 approval과 독립적으로 검사한다.
4. Admission을 통과한 call은 stable attempt ID, immutable `ToolDispatchIntent`, canonical payload hash를
   revisioned root에 먼저 commit한다. 이 commit이 실패하면 tool을 실행하지 않는다.
5. commit된 dispatch ID를 가진 effect만 registry invocation을 재구성해 transaction 밖에서 실행한다.
6. 결과는 별도 reconciliation transaction으로 observation, verification obligation, dispatch
   terminal status와 함께 commit한다. Approval 누락, canonical hash mismatch, invocation parse
   failure는 tool 결과가 아니라 `rejected/not_invoked` pre-invocation terminal result로 기록한다.
7. raw observation과 실제 tool argument의 raw command는 model history와 runtime progress에 기록한다.
   Raw command 기록, kubectl-only 정책 판정, shell wrapper script 추출은 서로 다른 helper를 사용한다.
8. 실제 실행 대상으로 commit된 action만 active step의 `AttemptRecord`가 되며 tool result는 stable observation ID를
   가진 `ObservationRecord`로 연결된다. 실행 전 target/schema/safety 거부는 step attempt를 증가시키지
   않는다.

Committed assistant tool call은 실행 여부와 무관하게 동일 call ID의 terminal result를 정확히 하나
가진다. 같은 batch의 앞선 실패로 실행되지 않은 뒤쪽 call은 `cancelled/not_invoked` synthetic result로
provider protocol을 닫으며 observation이나 mutation evidence로 승격하지 않는다.

Tool dispatch가 시작된 뒤 context cancellation, executor error, reconciliation commit 실패가
발생해도 pre-dispatch intent는 남는다. Runtime은 해당 mutation을 자동 재실행하지 않고
`status=unknown`, `execution_state=uncertain` observation과 read-only verification obligation으로
복구한다. 같은 batch에서 아직 invocation하지 않은 뒤쪽 dispatch는 cancelled로 종결한다.
Run loop는 context cancellation cleanup보다 committed pending dispatch recovery를 먼저 수행한다.
Recovery transaction이 실패하면 같은 candidate를 무한 재시도하지 않고 pending intent를 보존한 채
실행을 중단한다.
Dispatch intent와 순서는 active request 범위의 reconciliation 상태이므로 terminal status를 먼저
commit한 뒤 다음 dispatch batch 또는 새 request/reset에서 정리하고, request 전체 실행 이력은 indexed
attempt/observation ledger에 보존한다.
Invocation 전에 cancelled된 뒤쪽 intent는 audit history에는 남지만 lineage attempt budget과
same-action repetition 기준에서는 제외한다.

CRD 목록과 API resource 목록 조회는 model action이 아니라 typed discovery operation이다. Coordinator는
두 allowlisted operation만 고정된 read-only kubectl command로 변환하고 실행 전 read-only classifier를
통과시킨다. Discovery API는 임의 command 문자열을 받지 않으며 mutation으로 확장되지 않는다.
`/clear`와 `/reset`은 active request의 execution ledger와 dispatch reconciliation state도 제거해
이전 goal/evidence가 detached snapshot에 남지 않게 한다.

동일한 normalized action을 다시 사용하려면 중간에 다른 action이 있었더라도 가장 최근의 동일
attempt를 가리키는 `retry_of`와 반복을 정당화하는 `changed_since` observation reference가
필요하다. 같은 batch 안의 identical action 중복은 observation 사이의 상태 변화가 존재할 수
없으므로 dispatch 전에 거부한다. Step을 닫을 때 model은 `step_result`에 active
step/criterion/observation ID를 제출한다. Runtime은 reference와 절차를 검증하고 evidence 의미 자체는
재판단하지 않는다. `lightweight_lookup`은 성공한 단일 read-only observation 수신만 확인해 implicit
step과 phase를 자동 완료한다.

Plan revision은 기존 attempt를 지우지 않는다. Replacement step은 이전 `GoalLineageID`와
`MaxAttempts`를 상속하므로 step/phase 이름이나 ID를 바꿔도 같은 goal의 누적 attempt budget이
초기화되지 않는다. Completed/skipped/superseded history와 accepted revision은 session에 유지하고 prompt에는
현재 remaining graph와 최근 revision projection만 bounded runtime data로 넣는다. Tool observation뿐
아니라 resource-guide/external-state 및 사용자가 선택한 continuation도 typed evidence record로 남기며,
action과 직접 연결되지 않은 최근 evidence까지 bounded projection에 포함해 revision이 실제 stable ID를
참조할 수 있게 한다. Tool/user content는 항상 `BEGIN_RUNTIME_DATA`/`END_RUNTIME_DATA` 경계 안의
데이터로 주입한다.

예: `kubectl scale deployment api --replicas=3 -n prod`는 mutation이므로 approval 없이는
실행하지 않는다. `--read-only`가 켜져 있으면 이전에 사용자가 권한 확인을 생략하도록
선택했더라도 실행을 차단해야 한다.

### 5. Mutation verification

model은 mutating action에 single 또는 ordered-chain verification을 선언한다. coordinator는 첫
direct check가 mutation target과 일치하는지와 각 evidence action의 namespace/resource/name 및
read-only 여부를 검사한다. Pending verification과 goal execution ledger는 같은 aggregate root에서
commit되며, verification observation은 새 step attempt가 아니라 mutation AttemptID에 연결된다.

이름을 label/field selector에서 확인할 때는 selector flag를 파싱한 뒤 key/value를 완전
일치로 비교한다. 예를 들어 target `web`은 `metadata.name=web-prod`와 일치하지 않는다.

예: Deployment replicas를 변경한 뒤에는 변경 command의 성공 출력만으로 종료하지 않고
read-only rollout/status 관찰과 `mutation_verification_result`를 거쳐
`satisfied/waiting/failed`를 판단한다. `await_state`의 waiting은 같은 VerificationID와 AttemptID로
최대 5회 재확인한다. 서로 다른 조건은 ordered chain으로 한 번에 하나만 활성화한다. Mutation target과
원래 user-visible target이 다르면 최초 plan에 mutation 대상의 direct verification과 원래 대상의
outcome verification을 연속된 독립 step으로 선언한다. 진행 중 evidence가 기존 경로를 무효화하면
새 action 전에 `phase_plan_revision`으로 active/remaining graph를 교체한다.
Verification이 실패하거나 budget을 소진해도 mutation을 자동 재실행하지 않는다.

Single verification은 `awaiting_mutation_verification_evidence/result`, ordered chain은
`awaiting_mutation_verification_chain_evidence/result` control pair를 사용한다. Result는 현재 check의
가장 최근 observation을 반드시 참조해야 하며, chain은 current check가 satisfied된 뒤에만 다음 check를
활성화한다.

Observation은 저장 시 evidence qualification을 함께 가진다. 성공한 read-only 결과와 Kubernetes
NotFound는 `state_bearing`, Forbidden/Unauthorized는 `access_blocker`다. RBAC 거부는 확인을 수행하지
못했다는 증거이므로 `failed`/inconclusive 판단에는 사용할 수 있지만 `satisfied` 또는 `waiting`을
성립시키지 못한다. Error detail의 NotFound/RBAC/timeout 분류는 일반 성공 판정보다 먼저 적용하므로
`stderr`만 있는 Forbidden도 성공 evidence로 승격되지 않는다.

일반 observation이 RBAC로 거부된 경우에도 같은 forbidden command를 그대로 반복하지 않는다.
Runtime은 이를 current-phase retry로 돌려 대체 가능한 permitted evidence를 먼저 선택하게 한다.
요청 달성에 권한 변경이 실제로 필요하다고 model이 판단하면, RBAC command를 별도의
`risk.risky=true` mutating action과 direct verification으로 제안해야 하며 runtime은 exact payload를
사용자에게 승인받는다. Read-only mode는 이 경로에서도 mutation을 차단한다.

State-bearing evidence가 expected state를 명시적으로 반증한 `failed`는 owning attempt를 failed로
닫고 다른 action 또는 evidence-grounded `phase_plan_revision`을 허용한다. 실행 결과나 verification
결과가 `unknown`인 경우에만 unresolved blocked obligation과
`finalReportMustBeInconclusive`를 남긴다. Model error, 사용자 종료, iteration limit 또는 evidence
budget 소진으로 verification을 중단할 때는 단일 종료 경로가 owning attempt를 terminal 상태로
만들고 pointer와 wait effect를 정리한다.

진행 중인 mutation verification은 plan revision으로 대체할 수 없다. Unknown으로 닫힌 obligation은
replaceable phase graph 밖의 request-scoped ledger에 남으므로, model은 남은 전략을 revision할 수
있지만 해당 obligation을 삭제하거나 이후 final report를 conclusive로 바꿀 수 없다.

공통 tool success 판정은 known failure detail과 non-zero process exit를 성공보다 먼저 확인한다.
`status=unknown/uncertain/cancelled`도 성공이나 state-bearing evidence로 취급하지 않는다.
Exit code는 failure 여부의 참고값일 뿐 NotFound/RBAC/timeout 원인 class를 단독으로 결정하지 않는다.
정밀한 Kubernetes 원인 분류는 Plan 06의 bounded `kubectl -v` 후속 범위다.

### 6. Guidance and report

resource guide는 `flow/guidance`가 accepted phase, runtime resource classification, 기존 guide
주입 여부를 확인해 lookup이 필요하다고 판단한 경우에만 사용한다. guide step은 top-level
phase가 아니라 `guided_diagnosis` 아래 nested step이며 완료/skip 진행도 역시
`flow/guidance` 규칙을 거친다. 결과는 `flow/report`와 `flow/direction` 규칙을 거쳐 final
report 또는 사용자 continuation choice로 연결된다.

`guidance_lookup` 결과가 관찰되기 전에는 `resource_guide_lookup`만 허용한다. 또한
`guided_diagnosis`에 남은 guide step이 있으면 `final_report`와 `next_directions`를 수락하지
않는다. 이 검사는 control 값뿐 아니라 현재 phase와 nested guide 진행 상태를 함께 사용한다.

incident guidance는 orchestrator side-flow다. 구체적 실패 신호가 있을 때만 제안하며,
검색 결과의 summary를 보여줄 수 있지만 ReAct loop를 건너뛰어 Kubernetes 변경을 실행하지
않는다.

예: CRD Cluster의 status와 관련 객체를 관찰한 뒤 guide lookup phase에 들어가면 resource
guide를 사용할 수 있다. 단순히 `이벤트를 요약해줘`라고 요청했고 장애 신호가 없다면 incident
runbook 검색을 자동 제안하지 않는다.

### 7. Bounded segment closure and compaction

`MaxIterations`는 request history를 지우는 제한이 아니라 한 model-run segment의 상한이다. Request
시작 시 `MaxIterations=20`, `ClosureReserve=2`가 applied budget에 고정된다. Reserve에 진입하면 새
탐색보다 pending mandatory verification을 먼저 완료하고, 이후 model에 exclusive
`continuation_handoff`를 요구한다. Handoff의 request/goal/step/evidence/obligation ID와
`conclusive=false`, 모든 nonterminal step의 `unresolved_steps` 포함 여부를 runtime이 검증한다.
검증된 current judgement와 recommended next step은 번역 경계를 거쳐 사용자에게 먼저 표시한다.
사용자가 계속을 선택하면 같은 request, lineage, ledger와
safety budget counter를 유지하고 segment iteration만 초기화한다.

Pre-send 또는 provider context-length compaction은 compacted candidate와 `EffectResetChat`을 먼저
commit한다. Chat reset I/O는 commit 이후 실행하며 성공 상태 commit 뒤에만 성공 메시지를 보낸다.
Reset이 실패하면 pre-compaction content를 복원한다. Shim의 mixed answer/unknown-key 오류는 protocol
normalization에서 synthetic invalid output 하나로 합쳐진다.

## Native and Shim Protocol

- native mode는 provider의 function call을 사용한다.
- shim mode는 하나의 `json` code block 안 JSON object를 받는다.
- `protocol/shim.go`가 JSON 문자열을 추출/복구한다.
- `protocol/calls.go`가 internal call 이름을 정규화한다.
- `protocol/envelope.go`가 native/shim call을 provider-neutral output kind로 분류한다.
- `protocol/schema.go`가 runtime structured call 목록을 제공한다.
- coordinator가 full envelope policy를 통과한 event/action만 candidate lifecycle에 적용한다.
- Native external-tool schema 복사본에는 runtime-only action context인 `target`, `step_ref`, `retry_of`,
  `retry_reason`, `changed_since`, `verification`, command tool의 required `risk`를 추가한다. 원래 tool이 자체 `target` 인자를 정의하면
  그 schema와 invocation argument를 보존하고 coordinator target은 `runtime_target`으로 노출한다.
  Coordinator는 나머지 runtime metadata를 registry parse 전에 분리하므로 실제 tool 인자 계약은
  바뀌지 않는다.

shim/native 차이는 transport에만 있어야 한다. phase, approval, read-only, verification,
guidance 규칙은 두 모드에서 동일해야 한다.

## Read-Only Boundary

`internal/react/kube/readonly.go`는 현재 local classifier를 보유한다. 허용되는 pipeline은 첫
segment가 read-only kubectl이고 이후 segment가 `grep`, `jq`, `head` 같은 안전한 local text
processor인 경우다. mutating kubectl, shell evaluation, unsafe redirection은 실행 전에
거부해야 한다.

read-only classifier의 알려진 리스크와 `kubectl-readonly` 대체 대상은 최상위
[`bug.md`](../bug.md)를 기준으로 추적한다. package 이동만으로 해당 리스크가 해결됐다고
간주하지 않는다.

## Documentation Map

| 주제 | 문서 |
| --- | --- |
| requirement classification/context | [`requirement_analysis.md`](./requirement_analysis.md) |
| model phase plan과 guidance 진입 | [`request_processing_phases.md`](./request_processing_phases.md) |
| guide progress, report, continuation | [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md) |
| 명시적 state machine 설계 이력 | [`drafts/react_remediation_plans/07_explicit_state_machine.md`](./drafts/react_remediation_plans/07_explicit_state_machine.md) |
| 구조적 리스크 | [`reviews/react_loop_structure_review.md`](./reviews/react_loop_structure_review.md) |
| 재현 가능한 bug backlog | [`../bug.md`](../bug.md) |

## Maintenance Rules

- 외부 호출 계약은 `internal/react/react.go`에서 유지한다.
- 새 mutable workflow state는 `session`에 두고 coordinator에 별도 source of truth를 추가하지 않는다.
- Full attempt/observation history는 session에 보존하고 prompt에는 bounded projection만 넣는다.
- Tool output을 prompt에 재주입할 때는 명시적인 runtime DATA 경계를 사용한다.
- 새 shared enum/payload는 `contract`에 두되 provider/tool 구현 타입을 끌어오지 않는다.
- model/tool/input/output I/O가 없는 판단은 해당 `flow` package로 이동한다.
- Kubernetes command policy는 `kube`, transport/shim 규칙은 `protocol`에 둔다. Raw command 보존과
  kubectl-only 판정은 같은 extractor로 합치지 않고, shell wrapper 해제도 별도 경계로 유지한다.
- package 이동과 behavior fix를 구분한다. 이동만 한 경우 bug/review 항목을 완료 처리하지 않는다.
