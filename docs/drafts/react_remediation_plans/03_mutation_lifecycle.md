# Plan 03: Mutation Lifecycle

> 상태: Resolved. 일반 kubectl failure 분류 정밀화와 manifest 기반 target/namespace 검증은
> Plan 06 및 TODO의 독립 후속 범위다.
>
> 현재 코드는 성공한 mutating command 이후 `pendingMutationVerification`을 만들고,
> read-only verification evidence와 `mutation_verification_result`를 요구한다.
> 아래 Problem은 이 계획을 작성할 당시의 원래 결함 설명이며, 현재 구현 상태는
> 하단의 Implementation Status를 기준으로 본다.
> 현재 package 경계는 `flow/verification`, `session/execution.go`,
> `coordinator/state.go`, `coordinator/execution.go`, `coordinator/iteration.go`다. 아래 옛 루트 파일 경로와
> compatibility field 이름은 구현 이력이다.

## Original Problem

당시 mutating command는 approval을 받으면 실행됐다. 하지만 실행 후 해당 변경이 실제로 원하는 상태를 만들었는지 검증하는 runtime state가 없었다.

Approval은 "실행해도 되는가"만 답한다. "해결됐는가"는 별도의 verification이 필요하다.

## Current Code Evidence

- `internal/react/coordinator/execution.go`, `internal/react/coordinator/iteration.go`
  - `hasModifyingCalls`가 mutating call을 감지한다.
  - `requestApproval`이 사용자 승인을 받는다.
  - `dispatchToolCalls`는 결과를 observation으로 붙인 뒤 `trackMutationVerification`으로 mutation verification lifecycle을 시작하거나 갱신한다.
- `internal/react/flow/verification`, `internal/react/session/execution.go`, `internal/react/coordinator/state.go`, `internal/react/coordinator/iteration.go`
  - `pendingMutationVerification`이 mutation attempt와 ordered verification check를 연결한다.
  - pending verification 중에는 현재 active check를 만족하는 read-only observation 하나만 허용한다.
  - dispatch 후 취소되어 mutation 결과가 uncertain이면 자동 재실행하지 않고 같은 verification obligation을 생성한다.
  - active check의 evidence가 수집되면 `mutation_verification_result`만 허용한다.
  - `waiting`은 같은 verification ID를 유지한 채 최대 5회 재확인한다. State-bearing evidence가
    expected state를 반증한 `failed`는 다른 안전한 전략 또는 plan revision으로 진행하고,
    execution/verification 결과가 불확실한 `unknown`만 unresolved obligation으로 남는다.
- `internal/react/session/aggregate.go`, `internal/react/coordinator/state.go`, `execution_state.go`
  - verification obligation, evidence attempt, observation과 control transition은 같은 detached
    candidate에서 audit된 뒤 한 번에 commit된다.
  - verification evidence와 mutation continuation은 서로 다른 request-fixed budget 축을 사용한다.

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
    MutationStep    int
    MutationCommand string
    AttemptID       string
    Shape           VerificationShape // single | chain
    Checks          []verificationRuntimeCheck
    ActiveIndex     int
    AwaitingResult  bool
}

type verificationRuntimeCheck struct {
    ID                     string
    Mode                   VerificationMode // immediate | await_state
    Target                 actionTarget
    ExpectedState          string
    SuggestedCommand       string
    Status                 VerificationCheckStatus
    EvidenceRefs           []string
    RechecksUsed           int
    MaxRechecks            int
    InitialDelaySeconds    int
    RecheckIntervalSeconds int
}
```

단일 verification은 check 하나다. 서로 다른 조건을 같은 mutation step에서 순서대로 확인해야 할 때만 `shape=chain`, `policy=ordered`를 사용한다. 동일한 상태가 수렴하기를 기다리며 반복 조회하는 것은 chain이 아니라 같은 ID를 가진 `mode=await_state` 단일 verification이다.

단순 direct condition을 안전한 read-only command list 하나로 확인할 수 있으면 single check의 action 하나에
묶는다. Ordered chain은 앞 check 결과를 해석해야 다음 check로 진행할 수 있는 서로 다른 조건에 사용한다.

Runtime control도 shape를 구분한다.

- single: `awaiting_mutation_verification_evidence` -> `awaiting_mutation_verification_result`
- ordered chain: `awaiting_mutation_verification_chain_evidence` -> `awaiting_mutation_verification_chain_result`

Chain control은 `ActiveIndex`의 check 하나만 활성화한다. 현재 result가 `satisfied`일 때만 다음 check로
진행하며, 각 check마다 evidence/result control을 반복한다.

## Proposed Flow

1. model은 mutating action에 `verification`을 선언한다. Runtime은 single/chain shape, target, timing, read-only 후속 절차를 구조적으로 검증한다.
2. 한 step attempt에는 mutating primary action 하나만 허용한다.
3. mutation 실행 후 runtime은 같은 AttemptID에 direct verification을 연결한다.
4. 다음 model response에는 현재 active verification check를 만족하는 read-only action 하나만 허용한다.
5. verification observation은 새 step attempt를 만들지 않고 mutation attempt에 추가된다.
6. evidence 수집 후 model은 정확한 verification ID와 가장 최근 observation을 포함한 evidence refs를 사용해 `satisfied | waiting | failed`를 판정한다.
7. `satisfied`이면 ordered chain의 다음 check를 활성화하거나 mutation attempt를 완료한다.
8. `waiting`이면 같은 ID/target/expected state를 유지하고 runtime이 대기한 뒤 최대 5회 재확인한다.
9. `failed` 또는 recheck budget 소진 시 mutation을 자동 재실행하지 않고 다른 read-only 전략이나 evidence 기반 plan revision을 요구한다.
10. mutation 대상과 원래 사용자 목표가 다르면 outcome 확인은 다음 plan step으로 분리한다.

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
```

이 명령은 mutation step의 direct verification이다. ConfigMap 생성 자체가 성공했더라도 `web-app`이 정상화됐는지는 다음 outcome-verification step에서 `kubectl -n web get deployment web-app -o yaml`로 확인한다. Deployment rollout처럼 수렴 시간이 필요하면 그 다음 step의 단일 verification을 `mode=await_state`로 선언하고 같은 ID로 재확인한다.

Runtime must reject:

```text
"생성했습니다."
```

until verification evidence exists.

## Acceptance Criteria

- Mutating command success does not allow immediate final answer.
- Verification command must be read-only.
- Verification command must include exact namespace when known.
- Command approval is requested only for `risk.risky=true`, applies to the exact payload, and has no permanent skip option.
- Read-only enforcement and mutation verification remain mandatory regardless of the risk flag.
- Read-only mode still blocks mutation before this lifecycle starts.

## Implementation Status

- `internal/react/flow/verification`, `coordinator/state.go`, `coordinator/iteration.go`에 mutation
  verification obligation의 immutable rule, runtime ownership, orchestration을 분리했다.
- mutating tool의 execution state가 `uncertain`이면 성공 여부와 무관하게 verification obligation을
  만들고, read-only evidence가 수집되기 전 mutation 재실행을 허용하지 않는다.
- 성공한 mutating tool observation 이후 `pendingMutationVerification`을 설정하고, 다음 모델 응답에는 read-only verification action만 허용한다.
- pending verification 상태에서는 plain answer, `final_report`, `phase_progress`, `next_directions`, 추가 mutation, unrelated action을 correction으로 되돌린다.
- verification은 single 또는 ordered chain으로 관리하며 active check는 항상 하나다.
- mutation action과 direct verification은 하나의 AttemptID를 공유한다. verification observation과 await-state recheck는 step attempt를 추가하지 않는다.
- verification command는 active check의 resource/name/namespace를 만족해야 하며, namespace가 known이면 command에도 동일 namespace가 있어야 한다.
- active check evidence가 수집되면 pending verification은 `AwaitingResult`가 되며, 다음 응답은 `mutation_verification_result`만 허용한다.
- `mutation_verification_result.status=satisfied`는 다음 chain check 또는 plan step으로 진행한다. `waiting`은 같은 ID로 최대 5회 재확인하고 `failed`는 다른 전략을 요구한다.
- `failed`는 terminal failed attempt history이며 그 자체로 영구 inconclusive obligation을 만들지
  않는다. `unknown`/cancelled verification만 blocked obligation을 보존하고 conclusive report를
  차단한다.
- `RuntimeControlAwaitingMutationContinuation`은 materially different action 또는
  evidence-grounded `phase_plan_revision`만 허용하고 둘의 혼합은 거부한다.
- 같은 response의 여러 mutating primary action과 여러 chain check의 동시 실행은 허용하지 않는다.
- original outcome은 mutation direct verification에 자동 병합하지 않고 plan의 다음 verification step에서 확인한다.
- target을 확인할 수 없는 mutation에는 generic verification을 만들지 않는다. `kubectl apply -f ...`처럼 여러 target이 가능한 mutation은 concrete target을 가진 ordered chain을 action에 선언해야 한다.
- Deployment 같은 상위 리소스 문제는 먼저 Deployment 자체의 상태 evidence를 보고, 그 결과의 단서에 따라 Pod/Event/ReplicaSet 등 하위 리소스를 선택한다. 처음부터 하위 리소스를 전부 조회하지 않는다.
- evidence가 waiting/failed/degraded를 가리키면 즉시 conclusive final report로 가지 않고 같은-ID 재확인 또는 다른 안전한 strategy를 선택해야 한다.
- Command approval은 `risk.risky=true`인 exact payload에만 적용하며 영구 승인 생략 선택은 두지 않는다.
- Risk flag와 무관하게 read-only 및 mutation verification obligation은 계속 적용된다.
- `buildIterationSendContent`에 active mutation verification anchor를 추가해 모델이 다음 단계 의무를 계속 볼 수 있게 했다.
- read-only mode에서는 mutation이 실행되지 않아야 하므로 verification lifecycle도 시작하지 않는다.
- non-read-only session에서 성공한 mutation 이후에만 verification obligation을 만들고, 이때 verification action 자체는 read-only kubectl observation이어야 한다.
- verification evidence action budget과 mutation continuation budget은 correction/일반 step attempt와
  분리되어 request 시작 시 적용값이 고정된다.
- mutation verification state와 goal execution ledger는 하나의 model-turn candidate에서 함께
  commit되므로 evidence만 기록되거나 control만 전진하는 부분 상태를 남기지 않는다.

현재 runtime은 verification ID, evidence ref, target, read-only 여부, 순서, timing mode와 budget을 강제한다. evidence가 실제 expected state를 충족하는지에 대한 의미 판정은 LLM의 typed `mutation_verification_result`가 담당한다.

모든 mutating action에는 verification contract를 강제하지만, runtime checker의 책임은 deterministic한
절차 검증으로 제한한다. Expected state가 실제로 충족됐는지의 의미 판정은 model result가 담당한다.

## Regression Scenarios

1. `kubectl create configmap` succeeds, model answers immediately.
   - Expected: rejected, verification required.

2. verification command omits namespace.
   - Expected: rejected.

3. verification command uses different resource/name.
   - Expected: rejected.

4. verification confirms the mutated object.
   - Expected: mutation attempt completes; runtime advances to the separately declared outcome step.

5. `await_state` verification returns waiting repeatedly.
   - Expected: same verification ID and attempt are retained; after five rechecks the mutation is not repeated and a different safe strategy is required.

6. ordered chain has two distinct checks.
   - Expected: only the first check is active; the second becomes active after a satisfied result for the first.

7. delete verification observes Kubernetes NotFound.
   - Expected: NotFound is recorded as the latest verification evidence rather than retried as a tool failure; the model decides whether it satisfies the declared absence state.

8. verification receives only a Forbidden response.
   - Expected: evidence is classified as an access blocker. It may support `failed`, but never `satisfied` or `waiting`.

9. state-bearing evidence disproves the expected state.
   - Expected: the attempt closes as failed and the model may choose a different action or evidence-backed plan revision; no permanent unresolved obligation is added.

10. dispatch or verification outcome remains unknown.
   - Expected: the owning attempt closes as unknown, a blocked obligation remains, and a conclusive final report is rejected.

11. a general diagnostic observation receives Forbidden and the task genuinely requires an RBAC change.
   - Expected: the forbidden observation is retained as an access blocker and is not repeated unchanged. The model may first use permitted evidence or propose a separate risky RBAC mutation; that mutation requires exact user approval and its own direct verification.

## Risks

- Some mutation commands do not map cleanly to one Kubernetes object.
- The resource changed by a command is not always the resource that proves the user-visible problem was resolved.
- Direct effect verification can pass while outcome verification still fails.
- Overly strict checkers can create false negatives for CRDs, custom controllers, and operator-managed workflows.
- For ambiguous mutations, runtime should require explicit read-only evidence requirements rather than inventing a checker.
