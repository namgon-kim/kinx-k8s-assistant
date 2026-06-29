# Plan 06: Deterministic Gates vs LLM Correction

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
- destructive command approval and verification

Correction은 model에게 다음 출력을 안내하는 보조 수단으로만 사용한다.

## Proposed Gate Types

```go
type GateDecision struct {
    Allow bool
    Code string
    UserMessage string
    ModelCorrection string
    NextState State
}
```

각 gate는 다음 중 하나를 반환한다.

- allow
- block and ask model for corrected action
- block and ask user
- block and finish
- require verification

## Proposed Changes

1. gate 함수를 pure decision과 side effect로 분리한다.

현재:

```go
if !l.appendCorrectionWithCompaction(...) {
    l.state = StateDone
}
```

목표:

```go
decision := l.namespaceGate.Decide(context, calls)
l.applyGateDecision(decision)
```

2. safety-critical gate는 correction 실패와 무관하게 실행 차단을 보장한다.

3. model correction 반복 한도는 gate별로 관리한다.

4. correction이 반복되면 "실행"이 아니라 "중단" 또는 "사용자 확인"으로 간다.

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
