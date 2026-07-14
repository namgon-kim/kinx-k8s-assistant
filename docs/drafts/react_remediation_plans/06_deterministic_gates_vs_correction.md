# Plan 06: Deterministic Gates vs LLM Correction

> 상태: 대부분 구현됨.
>
> `GateOutcome`, `RetryScope`, `CorrectionMode`, `BranchPolicy`가 공통 gate 모델로
> 적용되어 있다. 일부 gate의 pure decision/apply 분리와 code별 correction counter는
> 후속 cleanup으로 남아 있다.
> 현재 공통 모델은 `internal/react/flow/gate`, snapshot/refs는 `contract`와 `session`,
> 적용 pipeline은 `coordinator/iteration.go`에 있다. 아래 옛 루트 파일 경로는 구현 이력이다.

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
  - self-talk shell action, read-only unknown command, read-only known mutation, interactive command, target/resource validation, requested structured output enforcement가 `GateOutcome` correction/apply 경로를 사용한다.
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
- mutation approval requirement
- post-mutation verification requirement
- CRD-only resource guide eligibility
- incident runbook no-match handling
- interactive command block
- conversation/clarification tool-call block
- non-observation shell action block
- tool execution failure classification
- destructive command approval and verification

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
| Requested structured output ignored | `next_directions_required`, `guided_phase_progress_required`, `final_report_required` | conflicting calls rejected, directive re-queued |
| Conversation request used tool | `conversation_tool_call` | tool call rejected, plain answer/question or clarification phase completion requested |
| Self-talk shell action | `non_observation_shell_action` | command not dispatched, current step retried |
| Interactive command | `interactive_command_blocked` | command not dispatched, non-interactive alternative requested |
| Read-only known mutation | read-only policy block | no dispatch, user request blocked |
| Read-only unknown command shape | read-only unknown retry | no dispatch, agent command correction |
| Tool execution failure | `tool_execution_<failure_class>` | observation annotated and branch/retry policy applied |

## Remaining Work

- gate별 correction 반복 한도는 아직 공통 dedup/compaction 기반이다. 필요하면 `GateOutcome.Code`별 counter로 분리한다.
- 일부 gate는 아직 pure decision 함수로 완전히 분리되어 있지 않다. 다만 apply/correction 의미는 `GateOutcome`으로 수렴한다.
- `BranchRecheckStep`, `BranchMovePhase`, `BranchRewindPhase`는 primitive가 있지만 모든 production gate가 target phase/step을 지정하는 것은 아니다.
- `ExpectedControl`은 apply 대상이 아니라 post-apply assertion이므로, 새 gate 추가 시 control을 직접 저장하거나 덮어쓰면 안 된다.

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
