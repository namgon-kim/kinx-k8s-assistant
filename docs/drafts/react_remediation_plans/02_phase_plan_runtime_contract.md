# Plan 02: Phase Plan Runtime Contract

## Problem

현재 phase plan은 model이 생성하고 runtime은 schema와 일부 전이 규칙만 검증한다. 이 구조에서는 model이 운영상 부적절한 phase를 만들거나, 해결 요청인데 검증 phase 없이 종료 가능한 plan을 만들 수 있다.

Model은 plan proposer일 수 있지만, runtime policy owner가 되어서는 안 된다.

## Current Code Evidence

- `internal/react/phase_plan.go`
  - `consumePhasePlan`은 model의 `phase_plan`을 `newPhaseStepState(plan)`으로 수용한다.
  - `phasePlanValid`는 index/name/allowed_next forward edge 중심으로 검증한다.
- `internal/react/loop.go`
  - `runIteration`은 phase plan 이후 action/final/answer를 gate한다.
  - 그러나 "mutation 요청에는 verification phase 필수" 같은 domain lifecycle은 phase plan validation에 없다.

## Desired Contract

Phase plan은 다음 두 계층으로 나눈다.

1. Model-proposed plan
   - 사용자의 의도와 필요한 관찰 단계를 제안한다.

2. Runtime-constrained plan
   - runtime이 request type, read-only, mutation, namespace, guidance eligibility를 기준으로 필수 phase를 삽입하거나 거부한다.

## Required Runtime Rules

### Mutation Request

`requirement_analysis.request_type` 또는 action이 mutation이면 다음 lifecycle이 필요하다.

```text
context_resolution? -> observation_before_change -> mutation_planning -> approval -> mutation_execution -> mutation_verification -> response_synthesis
```

`mutation_verification` 없는 plan은 invalid다.

### Lookup/Summary Request

single observation 후 answer가 가능하다.

```text
lightweight_lookup
```

단, aggregation 질문은 deterministic aggregation command가 필요하다.

### Diagnosis Request

최소한 observation과 synthesis가 필요하다.

```text
context_resolution? -> observation_planning -> observation_execution -> response_synthesis
```

### CRD Guidance

`guidance_lookup`과 `guided_diagnosis`는 runtime discovery가 CRD를 확인한 경우에만 허용한다.

## Proposed Changes

1. `phasePlanValid`를 request-aware validation으로 분리한다.

```go
func (l *Loop) validatePhasePlanForRequest(plan phasePlan) phasePlanValidationResult
```

2. validation result는 단순 bool이 아니라 reason과 required correction을 포함한다.

```go
type phasePlanValidationResult struct {
    Valid bool
    Code string
    Message string
}
```

3. mutation request에서 verification phase가 없으면 correction한다.

4. guidance phase가 CRD 미확정 또는 built-in resource에 포함되면 correction한다.

5. final/report phase는 evidence completion 이후에만 허용한다.

## Acceptance Criteria

- configmap 생성 요청에서 `mutation_verification` 없는 phase plan은 accepted되지 않는다.
- built-in resource diagnosis에서 `guidance_lookup` phase를 포함한 plan은 accepted되지 않는다.
- CRD 확인 후에만 `guidance_lookup`을 포함한 plan이 accepted된다.
- lightweight lookup은 여전히 single phase로 동작한다.

## Regression Scenarios

1. "configmap 생성해줘"
   - Expected: verification phase 필수.

2. "pods 많은 namespace 알려줘"
   - Expected: lightweight lookup 가능.

3. "deployment web-app 문제 해결해줘"
   - Expected: observation -> fix planning -> mutation/verification if needed.

4. "cluster CRD 문제 진단해줘"
   - Expected: CRD discovery 후 guidance phase 허용.

## Risks

- model이 만든 plan을 더 많이 reject하게 된다.
- 대신 phase contract가 명확해지고 운영 사고 위험이 줄어든다.
