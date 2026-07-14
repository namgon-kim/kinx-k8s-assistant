# Orchestrator and ReAct Architecture

이 문서는 현재 코드 기준으로 CLI 입력부터 ReAct model turn, Kubernetes tool 실행,
사용자 출력까지의 구조를 설명한다. 안정된 외부 경계와 내부 package 분리를 다루며,
아직 이전 중인 compatibility state도 구분해서 기록한다.

## Scope

주요 실행 경계는 다음과 같다.

| 위치 | 책임 |
| --- | --- |
| `cmd/k8s-assistant` | config/flag/env를 읽고 interactive CLI를 시작한다. |
| `internal/orchestrator` | readline, meta command, active agent 교체, formatter, incident-guidance side-flow를 소유한다. |
| `internal/react/react.go` | 외부 facade다. `New`, `Loop`, runtime snapshot/input 관련 공개 alias를 제공한다. |
| `internal/react/coordinator` | model/input/tool/output I/O와 한 iteration의 실행 순서를 조정한다. |
| `internal/react/session` | control, phase, verification, compact context mutable state의 목표 소유자다. |
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
│   ├── output.go
│   └── dependencies.go
├── session/
│   ├── state.go
│   ├── control.go
│   ├── phase.go
│   ├── verification.go
│   ├── context.go
│   ├── snapshot.go
│   └── cleanup.go
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
| `PhaseStatus` | model-declared top-level phase의 진행 상태 | pending, active, completed, skipped |
| `StepStatus` | phase 아래 실제 실행/guide/verification step 상태 | pending, active, completed, retrying |
| `InputOwner` | 현재 사용자 입력을 소비할 계층 | orchestrator, ReAct choice/text, approval |

`RuntimeControlState`는 phase 이름이 아니다. 예를 들어
`awaiting_mutation_verification_result`는 현재 phase가 무엇이든 runtime이 먼저 해결해야 할
검증 obligation이다. 반대로 `guided_diagnosis`는 model plan의 phase이며 그 내부의 다음
guide action은 `awaiting_guided_diagnosis_step` control로 나타날 수 있다.

현재 control enum과 lifecycle projection은 `contract/enums.go`와 `session/control.go`에 있다.
`session.State`는 다음 mutable 영역을 묶는다.

```text
State
├── Control
├── Phase
├── Verification
└── Context
```

`session.State.Snapshot()`은 외부에서 읽을 immutable `contract.RuntimeSnapshot`을 만든다.
orchestrator는 snapshot의 `Control`과 `InputOwner`를 사용해 입력을 agent에 보낼지, meta
command로 처리할지 결정한다.

## Migration Status

package split은 완료됐지만 state ownership 이전은 아직 완전히 끝나지 않았다.

| 항목 | 상태 |
| --- | --- |
| facade와 implementation 분리 | 완료 |
| enum/structured payload/snapshot의 `contract` 분리 | 완료 |
| `session.State`와 control/phase/verification/context container 도입 | 완료 |
| request/phase/guidance/verification/report/direction/gate 규칙 package 도입 | 완료 |
| phase validation/progress, guidance lookup/progress, verification matching/continuation, report/direction normalization의 production 경로 연결 | 완료 |
| gate outcome 계약/target validation과 correction message 선택의 production 경로 연결 | 완료 |
| protocol, kube, prompt, provider, language 분리 | 완료 |
| `coordinator.Loop`의 기존 mutable compatibility 필드 제거 | 미완료 |
| 모든 transition이 `session.State`만 변경하도록 단일화 | 미완료 |
| gate consume/enforce 순서와 decision/apply 전체의 reducer 위임 | 미완료 |

따라서 현재 `session.State`는 목표 source of truth지만, 코드 전체가 이미 그 상태만 사용한다고
간주하면 안 된다. `coordinator.Loop`에는 request/phase/guide/verification 관련 기존 필드와
package-local compatibility control이 남아 있다. 이 중복은 [`TODO.md`](./TODO.md)의
state/session ownership cleanup에서 추적한다.

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

예: 직전 query가 `tenant-a`의 Deployment를 대상으로 했다면 `그럼 rollout은?` 같은
follow-up은 이전 target/scope를 기본값으로 사용할 수 있다. 새 query가 명시적으로 다른
namespace나 all-namespaces를 지정하면 새 값이 우선해야 한다.

### 3. Model step and gate

coordinator는 model 응답을 native function calls 또는 shim JSON에서 공통 call 형태로
정규화한다. 이후 runtime obligation, phase, safety, correction 규칙을 적용한다.

- runtime internal call: requirement, phase plan/progress, guide progress, verification result,
  final report, next directions
- real action: kubectl, bash, configured MCP/tool call
- user-facing text: answer, progress, correction

한 응답이 internal structured call과 real action을 동시에 포함할 수 있으므로 최종 허용/차단은
coordinator의 iteration pipeline이 결정한다. `flow/gate`는 outcome/correction 값을 제공하지만,
consume/enforce 순서 전체가 순수 reducer로 이전된 상태는 아니다.

### 4. Approval and execution

1. `kube` helper가 kubectl command, target, mutation/read-only 특성을 판정한다.
2. read-only 위반은 실행 전에 차단한다.
3. mutation은 필요한 경우 user approval을 요청한다.
4. 승인된 call만 `toolconnector.Registry`와 executor로 실행한다.
5. raw observation은 model history와 runtime progress에 기록한다.

예: `kubectl scale deployment api --replicas=3 -n prod`는 mutation이므로 approval 없이는
실행하지 않는다. `--read-only`가 켜져 있으면 이전에 사용자가 권한 확인을 생략하도록
선택했더라도 실행을 차단해야 한다.

### 5. Mutation verification

mutation이 성공하면 coordinator가 action target과 command에서 evidence requirement 및
namespace/resource/name match evidence를 만든다. `flow/verification`은 이 구조화된 evidence의
matching과 evidence/result continuation을 판단한다. 현재 mutable requirement/attempt의 실제
판단 경로에는 coordinator의 compatibility 필드가 남아 있다. 이를
`session.VerificationState`만 사용하도록 단일화하는 작업은 후속 범위다.

이름을 label/field selector에서 확인할 때는 selector flag를 파싱한 뒤 key/value를 완전
일치로 비교한다. 예를 들어 target `web`은 `metadata.name=web-prod`와 일치하지 않는다.

예: Deployment replicas를 변경한 뒤에는 변경 command의 성공 출력만으로 종료하지 않고
read-only rollout/status 관찰과 `mutation_verification_result`를 거쳐 resolved/progressing/
unresolved를 판단한다. progressing/unresolved recheck budget이 소진되면 runtime은 다음
`final_report`를 `conclusive=false`로 제한하며, 모델 지시가 아니라 payload validation으로
이를 강제한다. 같은 pending verification에 여러 mutation이 포함되면 아직 충족되지 않은
동일 target의 `outcome_evidence`는 한 번만 요구한다.

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

## Native and Shim Protocol

- native mode는 provider의 function call을 사용한다.
- shim mode는 하나의 `json` code block 안 JSON object를 받는다.
- `protocol/shim.go`가 JSON 문자열을 추출/복구한다.
- `protocol/calls.go`가 internal call 이름을 정규화한다.
- `protocol/schema.go`가 runtime structured call 목록을 제공한다.
- coordinator가 normalized call을 실제 lifecycle에 적용한다.

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
- 새 shared enum/payload는 `contract`에 두되 provider/tool 구현 타입을 끌어오지 않는다.
- model/tool/input/output I/O가 없는 판단은 해당 `flow` package로 이동한다.
- Kubernetes command policy는 `kube`, transport/shim 규칙은 `protocol`에 둔다.
- package 이동과 behavior fix를 구분한다. 이동만 한 경우 bug/review 항목을 완료 처리하지 않는다.
