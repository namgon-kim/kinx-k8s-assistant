# Plan 03: Mutation Lifecycle

> 상태: 대부분 구현됨.
>
> 현재 코드는 성공한 mutating command 이후 `pendingMutationVerification`을 만들고,
> read-only verification evidence와 `mutation_verification_result`를 요구한다.
> 아래 Problem은 이 계획을 작성할 당시의 원래 결함 설명이며, 현재 구현 상태는
> 하단의 Implementation Status를 기준으로 본다.
> 현재 package 경계는 `flow/verification`, `session/verification.go`,
> `coordinator/execution.go`, `coordinator/iteration.go`다. 아래 옛 루트 파일 경로와
> compatibility field 이름은 구현 이력이다.

## Original Problem

당시 mutating command는 approval을 받으면 실행됐다. 하지만 실행 후 해당 변경이 실제로 원하는 상태를 만들었는지 검증하는 runtime state가 없었다.

Approval은 "실행해도 되는가"만 답한다. "해결됐는가"는 별도의 verification이 필요하다.

## Current Code Evidence

- `internal/react/coordinator/execution.go`, `internal/react/coordinator/iteration.go`
  - `hasModifyingCalls`가 mutating call을 감지한다.
  - `requestApproval`이 사용자 승인을 받는다.
  - `dispatchToolCalls`는 결과를 observation으로 붙인 뒤 `trackMutationVerification`으로 mutation verification lifecycle을 시작하거나 갱신한다.
- `internal/react/flow/verification`, `internal/react/session/verification.go`, `internal/react/coordinator/iteration.go`
  - `pendingMutationVerification`이 direct/outcome evidence requirement와 satisfied 상태를 보관한다.
  - pending verification 중에는 verification requirement를 만족하는 read-only observation만 허용한다.
  - 모든 requirement가 충족되면 `mutation_verification_result`만 허용한다.
  - `progressing`/`unresolved` 결과는 추가 ReAct action을 요구하고, recheck budget을 초과하면 inconclusive `final_report`를 요구한다.

## Desired Contract

Mutation lifecycle은 반드시 다음 순서를 따른다.

```text
plan -> approve -> execute -> verify -> report
```

각 단계의 책임:

| Step | Owner | Description |
| --- | --- | --- |
| plan | model + runtime validation | 변경 대상, namespace, command, expected outcome 확정 |
| approve | user | concrete command 승인 |
| execute | runtime | 승인된 command 실행 |
| verify | runtime-enforced ReAct step | read-only command로 변경 결과 확인 |
| report | model | verification evidence 기반으로 결과 보고 |

## Proposed Data Model

```go
type pendingMutationVerification struct {
    MutationStep int
    MutationCommand string
    Requirements []mutationEvidenceRequirement
    Satisfied map[string]bool
    AwaitingResult bool
}

type mutationEvidenceRequirement struct {
    ID string
    Kind string // direct_effect | outcome_evidence
    Target actionTarget
    Purpose string
    SuggestedCommand string
}
```

현재 구현의 기본 강제 단위는 “필수 evidence를 확보했는가”다. Runtime checker는 아직 data model에 포함하지 않는다. 나중에 추가하더라도 모든 mutation에 강제하는 기본 단위가 아니라, deterministic하게 판정할 수 있는 일부 표준 리소스/조건에만 선택적으로 붙인다.

## Proposed Flow

1. 목표 단위 mutation verification obligation을 계산한다. 개별 line/command 하나를 독립된 lifecycle로 보지 않는다.
2. 같은 goal 안에서 순차적으로 여러 mutation이 실행되면 각 mutation의 direct evidence requirement를 누적한다.
3. command 실행 성공 후 `pendingMutationVerification`을 set하거나 기존 pending verification에 merge한다.
4. 다음 model response에서 허용되는 것은 verification read-only action뿐이다.
5. direct effect evidence는 변경한 리소스/namespace/name을 확인해야 한다.
6. outcome evidence는 원래 사용자 문제와 연결된 상태를 확인해야 한다.
7. outcome이 아직 progressing/rolling out/pending이면 기다렸다가 재확인하거나, 다음 단서가 되는 read-only observation을 선택한다.
8. 모든 required evidence가 수집되면 바로 final_report로 가지 않고 `mutation_verification_result`로 evidence를 해석한다.
9. `mutation_verification_result.status=resolved`일 때만 conclusive final_report가 가능하다.
10. `progressing`이면 wait/recheck 또는 다음 read-only observation으로 계속한다.
11. `unresolved`이면 다른 진단/수정 접근으로 계속한다.
12. Runtime checker가 있는 경우에만 deterministic condition을 평가한다.

## Example

사용자:

```text
web 네임스페이스에 web-app이 참조하는 app-config configmap이 없으니 만들어줘
```

Mutation command:

```bash
kubectl -n web create configmap app-config --from-literal=...
```

Required verification evidence:

```bash
kubectl -n web get configmap app-config -o yaml
kubectl -n web get deployment web-app -o yaml
```

첫 번째 명령은 direct effect evidence다. 두 번째 명령은 outcome evidence다. ConfigMap 생성 자체가 성공했더라도 `web-app`이 정상화됐는지는 별도 evidence가 필요하다.

Runtime must reject:

```text
"생성했습니다."
```

until verification evidence exists.

## Acceptance Criteria

- Mutating command success does not allow immediate final answer.
- Verification command must be read-only.
- Verification command must include exact namespace when known.
- `yes_and_dont_ask_me_again` bypasses future approval prompts but does not bypass verification.
- Read-only mode still blocks mutation before this lifecycle starts.

## Implementation Status

- `internal/react/flow/verification`, `session/verification.go`, `coordinator/iteration.go`에 mutation verification obligation을 분리했다.
- 성공한 mutating tool observation 이후 `pendingMutationVerification`을 설정하고, 다음 모델 응답에는 read-only verification action만 허용한다.
- pending verification 상태에서는 plain answer, `final_report`, `phase_progress`, `next_directions`, 추가 mutation, unrelated action을 correction으로 되돌린다.
- verification obligation은 `direct_effect`와 `outcome_evidence` requirement 목록으로 관리한다.
- 같은 approved dispatch 또는 같은 goal 안에서 순차 mutation이 여러 개 실행되면 verification requirement를 누적한다.
- verification command는 남은 requirement 중 하나의 resource/name/namespace를 만족해야 하며, namespace가 known이면 command에도 동일 namespace가 있어야 한다.
- verification observation이 성공하면 해당 requirement만 satisfied로 표시한다.
- 모든 requirement가 satisfied가 되면 pending verification은 `AwaitingResult` 상태가 되며, 다음 응답은 `mutation_verification_result`만 허용한다.
- `mutation_verification_result`가 `resolved`일 때만 final_report 또는 phase completion으로 넘어갈 수 있다. `progressing`/`unresolved`이면 ReAct loop를 계속한다.
- 여러 read-only verification action이 각각 남은 requirement를 만족하면 한 번에 허용한다.
- generic direct requirement와 구체적인 outcome requirement가 동시에 매칭될 수 있으므로, command matching은 더 구체적인 requirement를 우선한다.
- target을 확인할 수 없는 successful mutation에는 generic verification을 만들지 않는다. 성공 시 generic evidence는 너무 약해서 아무 read-only observation으로 통과될 수 있기 때문이다.
- target을 확인할 수 없는 successful `kubectl apply -f ...`는 apply 출력 결과가 모두 성공이면 추가 verification을 하지 않는다. 실패/부분 실패는 tool error/evidence로 다음 ReAct 접근을 선택한다.
- Deployment 같은 상위 리소스 문제는 먼저 Deployment 자체의 상태 evidence를 보고, 그 결과의 단서에 따라 Pod/Event/ReplicaSet 등 하위 리소스를 선택한다. 처음부터 하위 리소스를 전부 조회하지 않는다.
- required evidence가 모두 수집되어도 evidence가 unresolved/progressing/degraded를 가리키면 conclusive final report가 아니라 다음 observation 또는 remediation approach를 선택해야 한다.
- `yes_and_dont_ask_me_again`은 approval만 생략하며, mutation 이후 verification obligation은 계속 적용된다.
- `buildIterationSendContent`에 active mutation verification anchor를 추가해 모델이 다음 단계 의무를 계속 볼 수 있게 했다.
- read-only mode에서는 mutation이 실행되지 않아야 하므로 verification lifecycle도 시작하지 않는다.
- non-read-only session에서 성공한 mutation 이후에만 verification obligation을 만들고, 이때 verification action 자체는 read-only kubectl observation이어야 한다.

현재 runtime은 verification evidence 수행 여부와 target 일치 여부를 강제한다. verification output이 실제 desired state를 만족하는지에 대한 의미 판정은 아직 final report 단계의 evidence grounding에 맡긴다.

다음 단계는 checker를 전면 강제하는 것이 아니다. Mutation lifecycle과 required evidence obligation을 먼저 명확히 하고, checker는 deterministic하게 판정 가능한 일부 direct effect에만 선택적으로 붙인다.

## Regression Scenarios

1. `kubectl create configmap` succeeds, model answers immediately.
   - Expected: rejected, verification required.

2. verification command omits namespace.
   - Expected: rejected.

3. verification command uses different resource/name.
   - Expected: rejected.

4. verification confirms object exists and deployment status evidence is collected.
   - Expected: final report/answer allowed.

5. verification confirms only the changed object, but does not inspect the affected workload.
   - Expected: conclusive final report rejected when outcome evidence is required.

## Risks

- Some mutation commands do not map cleanly to one Kubernetes object.
- The resource changed by a command is not always the resource that proves the user-visible problem was resolved.
- Direct effect verification can pass while outcome verification still fails.
- Overly strict checkers can create false negatives for CRDs, custom controllers, and operator-managed workflows.
- For ambiguous mutations, runtime should require explicit read-only evidence requirements rather than inventing a checker.
