# Plan 05: RAG Boundary

> 상태: 구현됨.
>
> Resource guide는 `internal/react`의 `guidance_lookup` phase와 CRD discovery gate를
> 통해서만 진입한다. Incident guidance는 orchestrator continuation choice에서
> 명시적으로 선택될 때만 `internal/guidance` client로 실행된다.

## Problem

이 프로젝트에는 두 종류의 RAG/guidance가 있다.

- resource guide: ReAct loop 내부 phase로 관리된다.
- incident guidance/runbook: orchestrator side-flow로 관리된다.

resource guide는 비교적 phase contract 안에 들어와 있지만, incident guidance는 agent text/tool result를 관찰하다가 ReAct 상태와 별도로 개입할 수 있다. 또한 runbook 검색이 실패하거나 confidence가 낮을 때 다른 자료를 억지로 끌고 오는 흐름은 운영 agent에 위험하다.

## Current Code Evidence

### Resource Guide

- `internal/react/resource_guidance.go`
  - `resource_guide_lookup` internal call로 진입한다.
  - `guidance_lookup` phase에서만 허용한다.
  - CRD 확인 전에는 차단한다.

### Incident Guidance

- `internal/orchestrator/incident_guidance_flow.go`
  - agent text와 tool result를 관찰한다.
  - keyword 기반으로 offer pending을 만든다.
  - ReAct continuation choice에 `[runbook 검색]` option으로만 노출된다.
  - 선택된 runbook/plan이 usable validation을 통과할 때만 summary로 출력한다.
  - summary command는 confirmation-required step, 미완성 template placeholder, 불완전한 namespace/value가 있으면 표시하지 않는다.
- `internal/guidance/client.go`
  - `Analyze`는 runbook match 후 plan을 만들 때 `AllowMutation=true`, `RequireDryRun=true`, `RequireConfirmation=true` 제약을 사용한다.
  - 이 plan은 orchestrator summary의 입력일 뿐이며 ReAct tool dispatch로 자동 주입되지 않는다.

## Desired Contract

RAG는 action owner가 아니라 evidence provider다.

### Resource Guide

- CRD-backed primary target에서만 사용한다.
- runtime discovery 전에는 사용하지 않는다.
- guide step은 `guided_diagnosis` 내부 nested progress다.
- 검색 실패는 "guide unavailable"로 기록하고 일반 진단으로 복귀한다.

### Incident Runbook

- 자동 prompt가 아니라 명시적 user choice로만 실행한다.
- ReAct-owned input을 가로채지 않는다.
- 검색 결과 없음, detection unknown, validation invalid, target mismatch면 "검색 결과 없음"으로 종료한다.
- 다른 runbook이나 generic knowledge를 억지 fallback으로 사용하지 않는다.

## Implemented Changes

1. Incident guidance를 `UserChoiceRequest` option으로만 노출한다.

2. incident runbook result validation을 강화한다.

```go
func incidentGuidanceResultUsable(result *guidance.ClientResult) bool
```

필수 조건:

- runbook case exists
- detection type is not only Unknown
- validation valid
- selected runbook `match_types` intersects with the signal detection types
- target kind is compatible with selected runbook `related_objects` when known
- plan has steps or verification

3. no match result는 active ReAct loop에 빈 입력을 보내지 않는다.

4. summary formatter는 hard-coded OOM/Node 문구를 쓰지 않는다.

5. out-of-band runbook remediation prompt는 제거됐다. runbook summary는 evidence/provider output일 뿐이고 Kubernetes 변경 실행은 ReAct/tool loop 안에서만 이뤄진다.

6. summary formatter는 unsafe/incomplete command를 숨긴다.

숨김 조건:

- `RequiresConfirmation=true`
- rendered command에 `{{...}}` placeholder가 남아 있음
- placeholder 대체값이 target/step variables에서 확인되지 않음
- `-n`, `--namespace`, `--namespace=` 값이 비어 있음
- rendered command에 incomplete marker인 ` / `가 남아 있음

## Example

web-app deployment 문제인데 runbook 검색 결과가 `Node NotReady`이고 detection이 `Unknown`이면:

```text
검색된 runbook이 없어 incident guidance를 종료합니다.
```

이어야 한다.

다음은 금지된다.

```text
참고 runbook: Node NotReady
권장 단계: 문제 노드 cordon
```

## Acceptance Criteria

- ReAct free-text continuation 중 incident prompt가 나오지 않는다.
- runbook 검색은 choice option을 선택했을 때만 실행된다.
- Unknown detection은 no usable runbook으로 처리한다.
- validation invalid면 plan steps를 권장 단계로 출력하지 않는다.
- selected runbook `match_types`가 signal detection과 겹치지 않으면 no usable runbook으로 처리한다.
- target kind가 selected runbook `related_objects`와 맞지 않으면 no usable runbook으로 처리한다.
- summary는 result plan/runbook content에서만 생성된다.

## Remaining Limits

- target compatibility는 resource kind 중심이다. namespace/name mismatch는 현재 incident client validation이 제공하는 `ValidationResult`를 신뢰한다.
- runbook `related_objects`가 비어 있으면 target kind로 폐기하지 않는다. runbook metadata가 부족한 상태에서 정상 케이스를 과도하게 차단하지 않기 위한 fail-open 지점이다.
- runbook summary는 remediation을 실행하지 않고 active ReAct loop에 remediation prompt도 주입하지 않는다. 사용자가 실제 변경을 원하면 별도 ReAct 요청으로 들어가고, 그 이후는 approval과 mutation verification lifecycle을 따른다.

## Regression Scenarios

1. `Pending` text만 있고 target이 deployment인 경우 Node runbook match
   - Expected: no usable runbook unless validation confirms target compatibility.

2. detection unknown
   - Expected: no runbook summary.

3. runbook not found
   - Expected: no fallback, 종료.

4. valid CrashLoopBackOff pod runbook
   - Expected: summary displayed, mutation steps still require approval.

## Risks

- 자동 runbook 추천 빈도가 줄어든다.
- 대신 잘못된 remediation 노출 위험이 줄어든다.
