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

## Current Evidence

현재 코드에서 확인된 구현 상태:

- `orchestrator.handleMessage`가 agent text/tool result를 보고 `IncidentGuidanceFlow`를 갱신한다.
- `orchestrator.handleAgentInputRequest`는 ReAct-owned input을 incident guidance prompt로 선점하지 않고, `ControlState`와 input kind로 dispatch한다.
- `react.Loop`는 `State` enum과 함께 `RuntimeSnapshot.Control`을 파생해 requirement/phase/guide/final/next/mutation/user-input 대기 상태를 구분한다.
- `phase_plan`은 model이 제안하지만 runtime이 schema, forward-only transition, mutation verification phase, CRD guidance eligibility를 수용 전에 검증한다.

최근 구현에서 반영된 부분:

- 성공한 mutating command는 `pendingMutationVerification`을 만들고, goal-level read-only verification evidence를 요구한다.
- 여러 mutating command가 하나의 목표를 완성하는 경우 verification requirement를 누적한다.
- verification evidence가 모두 충족되면 model은 `mutation_verification_result`로 `resolved`, `progressing`, `unresolved`를 판정해야 한다.
- `progressing`/`unresolved`는 바로 `final_report`나 `phase_progress`로 닫히지 않고 추가 ReAct action을 요구한다.
- target을 추출하지 못한 successful `kubectl apply -f ...`는 apply output 자체를 evidence로 보고 generic verification을 만들지 않는 정책을 둔다.

## Non-Goals

- 이번 문서 세트는 구현 패치가 아니다.
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

이 순서는 사용자가 실제로 본 오류를 먼저 닫기 위한 것이다. namespace/mutation 문제는 cluster 변경을 잘못 수행할 수 있으므로 최우선이다.
