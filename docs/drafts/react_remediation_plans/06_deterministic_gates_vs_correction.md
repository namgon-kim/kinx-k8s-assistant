# Plan 06: Deterministic Gates vs LLM Correction

> 상태: 구현됨.
>
> `GateOutcome`, `RetryScope`, `CorrectionMode`, `BranchPolicy`가 공통 gate 모델로
> 적용되어 있다. Turn-entry output policy와 code/scope별 correction counter도
> session goal execution state에 연결되어 있다.
> 현재 공통 모델은 `internal/react/flow/gate`, snapshot/refs는 `contract`와 `session`,
> 적용 pipeline은 `coordinator/iteration.go`에 있다. 아래 옛 루트 파일 경로는 구현 이력이다.
> Code/scope별 correction state의 최종 소유권과 reset/escalation 계약은
> [`08_turn_output_contract_and_goal_execution.md`](./08_turn_output_contract_and_goal_execution.md)의
> `session.Corrections` 설계를 따른다. Plan 08은 별도 correction counter를 만들지 않는다.

## Problem

현재 많은 오류 처리와 정책 강제가 correction message를 통해 model에게 다시 시키는 방식이다. ReAct에서는 흔하지만, 운영 자동화에서는 안전 정책과 lifecycle을 LLM 재시도에 맡기면 안 된다.

## Current Code Evidence

다음 로직들은 correction 중심이다.

- invalid requirement analysis
- missing phase plan
- invalid phase progress
- wrong resource guide phase
- requested final report ignored
- action target mismatch

현재 반영된 deterministic gate:

- `internal/react/flow/gate`
  - `GateOutcome`, `RetryScope`, `CorrectionMode`, `BranchPolicy`를 공통 gate outcome 모델로 사용한다.
  - gate는 먼저 allow/block/retry/wait/rebranch 의미를 결정하고, correction은 block 이후 모델을 재유도하는 보조 수단으로만 사용한다.
- `internal/react/coordinator/iteration.go`, `internal/react/flow/phase`
  - `validatePhasePlanForRequest`가 phase plan을 수용하기 전에 mutation verification/guidance eligibility를 결정한다.
  - gate에 막힌 phase plan은 `phaseStepState`로 수용되지 않으므로 이후 action dispatch로 내려가지 않는다.
- `internal/react/coordinator/loop.go`, `internal/react/coordinator/execution.go`, `internal/react/coordinator/iteration.go`
  - read-only unknown command, read-only known mutation, interactive command,
    target/resource validation이 `GateOutcome` correction/apply 경로를 사용한다.
- `internal/react/protocol`, `internal/react/flow/gate/output_policy.go`
  - native/shim 응답을 typed output kind로 정규화하고 control별 required/exclusive/mix policy를
    domain consumer보다 먼저 적용한다.
- `internal/react/coordinator/execution_state.go`
  - correction은 `Code + RetryScope + PhaseID/StepID/ObligationID` key와 request-fixed
    protocol/domain/safety threshold로 누적하고 scope가 정상 전진할 때 reset한다.
- `internal/react/coordinator/output.go`, `internal/react/coordinator/iteration.go`
  - tool execution failure를 `command_syntax`, `rbac_forbidden`, `resource_not_found`, `timeout_or_api_unavailable`, `partial_success`, `unknown`으로 분류한다.
  - 각 failure class는 `retryable`, `retry_scope`, `suggested_response`를 observation에 붙이고 `GateOutcomeToolExecutionFailure`로 이어진다.
- `internal/orchestrator/incident_guidance_flow.go`
  - incident runbook은 continuation choice에서만 실행되고, usable validation과 command rendering guard를 통과한 summary만 출력된다.

Correction 자체는 필요하지만, 안전 정책의 최종 보증 수단이 되어서는 안 된다.

## Desired Contract

다음은 deterministic gate여야 한다.

- read-only mutation block
- namespace/scope mismatch block
- `risk.risky=true` command exact approval requirement
- post-mutation verification requirement
- CRD-only resource guide eligibility
- incident runbook no-match handling
- interactive command block
- conversation/clarification tool-call block
- tool execution failure classification
- risky command approval and mutation verification

Correction은 model에게 다음 출력을 안내하는 보조 수단으로만 사용한다.

## Current Gate Type

```go
type GateOutcome struct {
    Allow bool
    Kind  GateOutcomeKind
    Code  string

    ExpectedControl ControlState
    TargetPhase     *PhaseRef
    TargetStep      *StepRef

    Retryable  bool
    RetryScope RetryScope

    UserVisible     bool
    UserMessage     string
    ModelCorrection string

    CorrectionMode CorrectionMode
    BranchPolicy   BranchPolicy
}
```

각 gate는 다음 중 하나를 반환한다.

- allow
- block and ask model for corrected action
- block and ask user
- block and finish
- require verification / external-state wait / phase or step branch

## Implemented First Step

1. gate 결과를 `GateOutcome`과 side effect apply 경로로 분리하는 구조를 도입했다.

현재:

```go
if !l.appendCorrectionWithCompaction(...) {
    l.state = StateDone
}
```

목표:

```go
outcome := l.namespaceGate.Decide(context, calls)
l.applyGateOutcome(outcome)
```

이번 구현:

```go
result := l.validatePhasePlanForRequest(plan)
if !result.Valid {
    l.applyGateOutcome(result.gateOutcome())
}
```

2. phase-plan safety-critical gate는 correction 실패와 무관하게 실행 차단을 보장한다.

3. model correction 반복 한도는 기존 correction dedup/compaction 경로를 재사용한다.

4. correction이 반복되면 plan을 수용하거나 실행하지 않고 `StateDone`으로 중단한다.

## Current Deterministic Phase Plan Gates

- mutation request 또는 mutation execution phase가 있는데 verification phase가 없으면 block.
- `guidance_lookup`/`guided_diagnosis`가 있는데 runtime discovery가 CRD를 확인하지 않았으면 block.
- `guided_diagnosis`가 `guidance_lookup` 없이 등장하면 block.
- `lightweight_lookup` single phase는 기존대로 allow.

## Current Tool/Runtime Gates

| Gate | Code / class | Deterministic result |
|---|---|---|
| Turn output contract 위반 | `turn_output_policy_<code>` | state 변경 전 reject, 현재 control directive 재요구 |
| Conversation request used tool | `conversation_tool_call` | tool call rejected, plain answer/question or clarification phase completion requested |
| Interactive command | `interactive_command_blocked` | command not dispatched, non-interactive alternative requested |
| Read-only known mutation | read-only policy block | no dispatch, user request blocked |
| Read-only unknown command shape | read-only unknown retry | no dispatch, agent command correction |
| Tool execution failure | `tool_execution_<failure_class>` | observation annotated and branch/retry policy applied |
| Tool invocation cancellation after dispatch | `tool_execution_unknown` | uncertain observation committed, prior action history preserved |

## Current Boundary

- 같은 rejected model turn의 동일 correction key는 한 번만 증가하고 정상적인 step/phase 전진 시 reset한다.
- threshold 도달 시 해당 `GateOutcome.BranchPolicy`가 retry, blocked, replan 또는 request block을 결정한다.
- Step attempt, correction, verification evidence, mutation continuation, plan revision budget은 서로 합치지 않는다.
- 일부 domain gate는 coordinator helper에서 decision input을 조립하지만 allow/block 의미와 counter 적용은
  공통 contract를 따른다.
- `BranchMovePhase`, `BranchRewindPhase`, `BranchSkipStep` primitive를 새 gate에 연결할 때는
  target phase/step reference와 mandatory owner를 함께 검증해야 한다.
- 이전 `BranchRecheckStep`은 production producer 없이 mutation continuation budget을 공유하던
  중복 경로라 제거했다. Temporal verification retry는 active verification ID의
  `mode=await_state`, `RechecksUsed`, verification evidence budget만 사용한다.
- `ExpectedControl`은 post-apply assertion이며 새 control 값을 저장하는 명령이 아니다.
- RBAC/Forbidden observation은 대상 상태의 성공 evidence가 아니지만 곧바로 request를 terminal block하지
  않는다. Current-phase retry에서 permitted alternative를 선택하고, 권한 변경이 실제로 필요하면 model이
  별도의 risky mutation과 direct verification을 제안해 exact user approval을 받는다.

## Future kubectl verbose failure classification

> 구현되지 않은 후속 작업이다. 현재 classifier는 normalized tool result와
> stderr/error text를 사용하며 bounded `kubectl -v` metadata 수집은 수행하지 않는다. 현재
> 공통 success 판정은 non-zero exit code를 실패로 처리하지만, 그 값만으로 Kubernetes failure
> class를 정하지 않는다.

kubectl process exit code는 성공, 일반 실패, 실행 불가, signal interruption을 구분하는
참고 자료로 사용한다. 하나의 non-zero exit code가 NotFound, Forbidden, Conflict,
Invalid, TooManyRequests, API server failure 등 여러 Kubernetes 오류에 사용될 수 있으므로
exit code만으로 failure class를 결정하지 않는다.

향후 classifier는 다음 우선순위를 사용한다.

1. tool result가 제공하는 typed Kubernetes `Status.reason`과 HTTP status code
2. kubectl 실행에서 수집한 bounded verbose HTTP metadata
3. normalized stderr/error/status field
4. bounded kubectl error pattern
5. process exit code

Bounded verbosity는 command의 read-only/mutation 성격과 무관하게 kubectl 실행 결과의
실패 여부와 구체적인 사유를 확인하기 위한 진단 metadata다. Runtime은 선택된 verbosity
정책을 실제 kubectl command 실행 시 적용하고, stdout/stderr와 분리된 normalized failure
metadata로 수집한다.

초기 수집 수준은 HTTP method, request URL과 response status를 확인할 수 있는 낮은
verbosity부터 시작한다. 예를 들어 `kubectl --v=6 ...` 수준을 후보로 검증하되, header와
body가 과도하게 노출되는 높은 verbosity를 자동으로 사용하지 않는다. 실제 level과
수집 정책은 kubectl 버전별 출력, 성능과 보안 영향 검증 후 결정한다.

Runtime은 verbose output을 model이나 history에 넣기 전에 다음을 강제한다.

- Authorization header, bearer token, client certificate와 credential path 제거
- Secret data, request/response body와 민감 query value 마스킹
- 출력 크기와 line 수 제한
- raw verbose output의 prompt 및 durable checkpoint 영구 저장 금지
- verbose metadata와 실제 command result의 invocation ID 일치
- namespace/target invariant와 기존 command execution contract 유지

최소 회귀 사례는 HTTP `401`, `403`, `404`, `409`, `422`, `429`, `5xx`, timeout,
TLS failure, command-not-found와 signal interruption이다. Verbose metadata는 command
실패 원인을 분류하기 위한 자료이며 그 자체를 resource expected-state evidence로
승격하지 않는다.

## Example

잘못된 mutation:

```bash
kubectl create configmap app-config
```

request namespace:

```text
web
```

Gate result:

```json
{
  "allow": false,
  "code": "namespace_required_for_mutation",
  "next_state": "StateRunning",
  "model_correction": "The mutation target is namespaced and the accepted request namespace is web. Return one corrected action using -n web.",
  "user_message": "namespace가 필요한 변경 명령이 namespace 없이 제안되어 차단했습니다."
}
```

중요한 점: correction이 실패해도 command는 실행되지 않는다.

## Acceptance Criteria

- safety-critical violations never reach `dispatchToolCalls`.
- gate decision can be unit tested without LLM.
- correction text changes do not change safety behavior.
- repeated invalid correction ends in deterministic stop/user prompt.

## Regression Scenarios

1. read-only mutation
   - Expected: block, no dispatch.

2. namespace mismatch
   - Expected: block, no dispatch.

3. wrong guide phase
   - Expected: correction, no dispatch.

4. final report requested but action emitted
   - Expected: no action dispatch.

## Risks

- Initial refactor may duplicate some existing correction logic.
- Mitigate by first wrapping existing gates with decision structs, then extracting pure logic.
