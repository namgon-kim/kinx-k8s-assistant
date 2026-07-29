# ReAct Remediation Plans Overview

이 문서는 현재 `internal/react`와 `internal/orchestrator` 구조가 사용자의 기대와 다르게 개발된 지점을 고정하고, 항목별 수정 계획을 연결한다.

핵심 결론은 다음과 같다.

- ReAct loop는 단순 tool loop가 아니라 운영 agent의 단일 제어기여야 한다.
- Orchestrator는 표시, 입력 수집, meta command, lifecycle 관리에 집중하고, 진단 흐름의 다음 행동을 임의로 가로채면 안 된다.
- Model이 phase plan을 제안할 수는 있지만, 운영 정책과 안전 lifecycle은 runtime이 결정해야 한다.
- RAG는 보조 근거다. 검색 실패, 낮은 confidence, target mismatch는 "없음"으로 끝내야 하며 다른 자료를 억지로 가져오면 안 된다.
- Kubernetes mutation은 approval만으로 충분하지 않다. 실행 후 검증까지 runtime contract가 되어야 한다.

## Plan Set

| Plan | Scope | Priority |
| --- | --- | --- |
| [`01_user_input_ownership.md`](./01_user_input_ownership.md) | ReAct 입력 소유권과 orchestrator side-flow 차단 | High |
| [`02_phase_plan_runtime_contract.md`](./02_phase_plan_runtime_contract.md) | model-owned phase plan을 runtime contract로 제한 | High |
| [`03_mutation_lifecycle.md`](./03_mutation_lifecycle.md) | mutation의 plan -> approve -> execute -> verify -> report 강제 | Critical |
| [`04_namespace_scope_invariant.md`](./04_namespace_scope_invariant.md) | request namespace/scope와 action command 일치 강제 | Critical |
| [`05_rag_boundary.md`](./05_rag_boundary.md) | resource guide와 incident runbook 경계 정리 | High |
| [`06_deterministic_gates_vs_correction.md`](./06_deterministic_gates_vs_correction.md) | LLM correction 의존을 deterministic gate로 전환 | High |
| [`07_explicit_state_machine.md`](./07_explicit_state_machine.md) | implicit flags를 명시적 state machine으로 정리 | High |
| [`08_turn_output_contract_and_goal_execution.md`](./08_turn_output_contract_and_goal_execution.md) | turn output contract, goal/step execution, attempt ledger, plan revision | High |
| [`09_durable_session_persistence.md`](./09_durable_session_persistence.md) | checkpoint/journal, crash consistency, durable session recovery | High |

## Current Evidence

현재 코드에서 확인된 구현 상태:

- `orchestrator.handleMessage`가 agent text/tool result를 보고 `IncidentGuidanceFlow`를 갱신한다.
- `orchestrator.handleAgentInputRequest`는 ReAct-owned input을 incident guidance prompt로 선점하지 않고, `RuntimeControlState`와 input kind로 dispatch한다.
- `internal/react/react.go`는 facade, `coordinator`는 I/O orchestration, `contract`는 enum/payload, `session`은 mutable state, `flow`는 업무 규칙으로 분리됐다.
- `session.Aggregate[runtimeState]`의 control과 detached `RuntimeSnapshot.Control`이 requirement/phase/guide/final/next/mutation/user-input obligation을 표현한다.
- `coordinator.Loop`의 mutable state는 `session.Aggregate[runtimeState]` 한 root로 단일화됐고,
  package-local test seed mirror도 제거됐다.
- `phase_plan`은 model이 제안하지만 runtime이 schema, forward-only transition, mutation verification phase, CRD guidance eligibility를 수용 전에 검증한다.
- `phase_plan_revision`은 기존 observation을 근거로 active/remaining graph를 바꿀 수 있지만,
  runtime이 revision budget과 phase/step lineage 상속, completed history와 mandatory obligation 보존을
  검증한 뒤 원자적으로 적용한다.
- tool, mutation, external-state, resource-guide, user continuation evidence는 typed stable ID로
  기록된다. Prompt는 최근 evidence ID를 bounded runtime data로 노출하고, runtime은 revision이
  session ledger에 실제로 존재하는 evidence만 참조하는지 검증한다.

최근 구현에서 반영된 부분:

- mutating command 하나와 single 또는 ordered-chain direct verification은 같은 step attempt에 속한다.
- single verification과 ordered chain은 별도 evidence/result control pair를 사용하며, chain은 현재 check 하나만 활성화한다.
- active verification evidence가 수집되면 model은 exact verification ID와 최신 observation을 포함한 evidence refs를 사용해 `satisfied`, `waiting`, `failed`를 판정해야 한다.
- `waiting`은 action이 미리 선언한 `await_state` mode에서만 같은 ID로 최대 5회 재확인하며 mutation을 반복하지 않는다.
- target을 추출하지 못한 `kubectl apply -f ...` 같은 mutation은 generic verification을 만들지 않는다. 모든 check에 concrete target을 선언한 ordered chain이 있어야 실행 전 admission을 통과한다.
- command action은 `risk.risky`를 명시하고, 승인된 action은 immutable dispatch intent와
  `dispatch_pending` attempt로 먼저 commit된 뒤 실행된다. Reconciliation이 실패해도 mutating
  dispatch를 자동 재실행하지 않는다.
- 사용자 승인은 `risk.risky=true`인 exact command payload에만 적용하며 영구 승인 생략 상태는 두지
  않는다. Read-only, target, mutation verification은 risk/approval과 독립적으로 강제한다.
- 실행 전 approval/integrity/parse 거부와 batch 미실행 후속 call은 각각
  `rejected/not_invoked`, `cancelled/not_invoked` terminal result로 provider protocol을 닫고
  observation이나 mutation evidence를 만들지 않는다.
- Forbidden/Unauthorized는 access blocker이며 verification의 `satisfied`/`waiting` 근거가 될 수
  없다. 명시적으로 반증된 `failed`와 결과를 모르는 `unknown`을 구분하고, unresolved obligation과
  inconclusive-report 강제는 `unknown`에만 남긴다.
- 일반 observation의 RBAC 실패는 같은 forbidden command 반복이 아니라 current-phase 대체 evidence
  선택으로 이어진다. RBAC 변경이 실제 목표 달성에 필요하면 별도의 `risk.risky=true` mutation,
  exact user approval, direct verification 계약을 사용한다.
- verification failure 뒤에는 materially different action과 evidence-grounded
  `phase_plan_revision` 중 하나를 선택할 수 있다.
- Active verification은 plan replacement를 차단한다. Unknown으로 닫힌 verification obligation은
  request ledger와 inconclusive-report 제약에 남지만, 이를 삭제하지 않는 remaining-strategy
  revision은 허용한다.
- `MaxIterations`는 한 execution segment를 닫고 validated `continuation_handoff`를 요구한다.
  계속하기를 선택해도 request, lineage, ledger와 safety counter를 초기화하지 않는다.

## Non-Goals

- 문서만으로 runtime enforcement를 대체하지 않는다.
- prompt wording만 바꾸는 해결책은 최종 목표가 아니다.
- 특정 resource 이름이나 특정 runbook title을 필터링하는 방식은 원칙적으로 피한다.
- guidance를 Kubernetes 변경 실행기로 만들지 않는다.

## Execution Order

1. `04_namespace_scope_invariant.md`
2. `03_mutation_lifecycle.md`
3. `01_user_input_ownership.md`
4. `05_rag_boundary.md`
5. `02_phase_plan_runtime_contract.md`
6. `06_deterministic_gates_vs_correction.md`
7. `07_explicit_state_machine.md`
8. `08_turn_output_contract_and_goal_execution.md`
9. `09_durable_session_persistence.md`

이 순서는 사용자가 실제로 본 오류를 먼저 닫기 위한 것이다. namespace/mutation 문제는 cluster 변경을 잘못 수행할 수 있으므로 최우선이다.
