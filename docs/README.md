# Documentation Index

이 디렉터리는 구현 계약, 설계 검토, 초안, RAG 입력 자료를 분리해서 보관한다.

## 분류 기준

| 위치 | 의미 | 관리 원칙 |
| --- | --- | --- |
| `docs/*.md` | 현재 코드와 맞춰 유지하는 안정 문서 | 코드 동작이 바뀌면 함께 갱신한다. |
| `docs/reviews/` | 특정 설계 검토, 감사, 미결 논점 | 현재 구현과 다른 과거 가정은 명시적으로 표시한다. |
| `docs/drafts/` | 구현 전/진행 중 설계 초안 | 구현된 항목은 상태를 갱신하고, 안정 계약이 되면 `docs/`로 승격한다. |
| `docs/drafts/react_remediation_plans/` | ReAct runtime 개선 계획 묶음 | 각 plan의 `Implementation Status`를 기준으로 적용 여부를 판단한다. |
| `docs/rag/` | guidance RAG 업로드/검색용 정제 문서 | 코드 계약 문서가 아니라 운영 지식 입력이다. |
| `docs/rag_raws/` | RAG 문서 작성 전 원천/메모 | 런타임 계약이나 사용자 문서로 간주하지 않는다. |

## 문서별 감사 결과

### Stable Docs

| 문서 | 적용 상태 | 주요 코드 위치 |
| --- | --- | --- |
| [`architecture_orchestrator_react.md`](./architecture_orchestrator_react.md) | 코드 기준 갱신됨. runtime pipeline, pre-dispatch gates, tool failure classification, incident guidance summary 경계 반영 | `cmd/k8s-assistant`, `internal/orchestrator`, `internal/react`, `internal/toolconnector`, `internal/guidance` |
| [`requirement_analysis.md`](./requirement_analysis.md) | 코드 기준 갱신됨. compact previous context, follow-up defaulting, clarification gate 구현 반영 | `internal/react/requirement_prompt.go`, `internal/react/request_context.go`, `internal/react/context_state.go`, `prompts/default.tmpl` |
| [`request_processing_phases.md`](./request_processing_phases.md) | 코드 기준 갱신됨. `__phase_plan__`, phase-owned `resource_guide_lookup`, mutation verification 반영 | `internal/react/phase_plan.go`, `internal/react/resource_guidance.go`, `internal/react/mutation_lifecycle.go` |
| [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md) | 코드 기준 갱신됨. anchor 순서, guide-step completion matching, directive/runtime gate, 내부 오류 격리 반영 | `internal/react/request_context.go`, `internal/react/phase_plan.go`, `internal/react/final_report.go`, `internal/react/next_directions.go`, `internal/react/tool_failure.go` |
| [`TODO.md`](./TODO.md) | 남은 TODO와 설계 변경 drop 사유 요약 | hardening, cleanup, 중단된 과거 설계 추적 |
| [`README.md`](./README.md) | 이 인덱스 문서. 적용/미적용 상태를 추적함 | docs 분류 기준 |

### Drafts

| 문서 | 구현 상태 | 정리 방향 |
| --- | --- | --- |
| [`drafts/react_gate_outcome_design.md`](./drafts/react_gate_outcome_design.md) | 구현 완료, TODO 분리 | `GateOutcome`, `RuntimeSnapshot`, branch primitive 내용은 안정 문서 승격 후보. |
| [`drafts/react_remediation_plans/00_overview.md`](./drafts/react_remediation_plans/00_overview.md) | 코드 기준 갱신됨 | remediation plan 묶음의 현황 인덱스로 유지. |
| [`drafts/react_remediation_plans/01_user_input_ownership.md`](./drafts/react_remediation_plans/01_user_input_ownership.md) | 구현됨 | 현재 구현 기준으로 유지하거나 안정 문서에 병합 가능. |
| [`drafts/react_remediation_plans/02_phase_plan_runtime_contract.md`](./drafts/react_remediation_plans/02_phase_plan_runtime_contract.md) | 구현됨 | `request_processing_phases.md`와 중복되는 부분은 장기적으로 통합. |
| [`drafts/react_remediation_plans/03_mutation_lifecycle.md`](./drafts/react_remediation_plans/03_mutation_lifecycle.md) | 대부분 구현됨 | mutation verification 계약은 안정 문서 승격 후보. |
| [`drafts/react_remediation_plans/04_namespace_scope_invariant.md`](./drafts/react_remediation_plans/04_namespace_scope_invariant.md) | 부분 구현됨, TODO 분리 | file/manifest 기반 namespace 검증은 [`TODO.md`](./TODO.md)에서 추적. |
| [`drafts/react_remediation_plans/05_rag_boundary.md`](./drafts/react_remediation_plans/05_rag_boundary.md) | 구현됨, 코드 기준 갱신됨 | incident plan은 summary 입력일 뿐 자동 실행/주입 경로가 아님. |
| [`drafts/react_remediation_plans/06_deterministic_gates_vs_correction.md`](./drafts/react_remediation_plans/06_deterministic_gates_vs_correction.md) | 대부분 구현됨, 코드 기준 갱신됨 | tool failure 분류와 runtime gate 구현 반영. 남은 gate pure decision 함수화는 [`TODO.md`](./TODO.md)에서 추적. |
| [`drafts/react_remediation_plans/07_explicit_state_machine.md`](./drafts/react_remediation_plans/07_explicit_state_machine.md) | 부분 구현됨 | `RuntimeSnapshot.Control`은 구현됨. source-of-truth flag cleanup은 [`TODO.md`](./TODO.md)에서 추적. |
| [`drafts/draft_troubleshooting_v1.md`](./drafts/draft_troubleshooting_v1.md) | Legacy 초안, 현재 구조와 다수 불일치 | `trouble_shooting` MCP/server, `internal/troubleshooting`, `troubleshooting-upload`, kubectl-ai Agent 재주입 흐름은 현재 구현 기준이 아니다. 현재 기준은 `internal/guidance` 내장 client와 `log-analyzer` 분리다. |
| [`drafts/draft_for_cluster-api.md`](./drafts/draft_for_cluster-api.md) | 미구현 설계 초안, 후반부 최종 권장안만 현재 원칙과 가까움 | 초반의 `cluster-api-server` MCP, `trouble-shooting` MCP, `internal/troubleshooting/runbooks` 경로는 현재 구현 기준이 아니다. Cluster API 도메인 확장 논의로 보관. |
| [`drafts/draft_runbook_iksv2.md`](./drafts/draft_runbook_iksv2.md) | RAG/runbook 원천 초안, 코드 구현 상태 대상 아님 | 정제본은 `docs/rag/`와 `internal/guidance/runbooks/`를 기준으로 본다. |

### Reviews

| 문서 | 상태 |
| --- | --- |
| [`reviews/kubectl_ai_react_loop_review.md`](./reviews/kubectl_ai_react_loop_review.md) | Historical review. kubectl-ai ReAct loop 분리 근거로 보관한다. 현재 구현은 자체 `internal/react` loop를 사용하며, 문서 내 `troubleshooting_flow`, `internal/agent/setup.go`, `internal/react/approval.go` 같은 일부 제안 경로는 현재 파일 구조와 다르다. |
| [`reviews/react_loop_structure_review.md`](./reviews/react_loop_structure_review.md) | Current structure review. 분산 플래그 상태, gate pipeline, phase/guidance 재진입, mutation verification lifecycle, liveness, protocol channel mixing 리스크를 정리한다. |
| [`reviews/revise_troubleshooting.md`](./reviews/revise_troubleshooting.md) | guidance/log-analyzer 경계와 남은 revise 논점. 과거 `trouble-shooting` MCP 서버 가정은 현재 구현 기준으로 정리됐다. |

### RAG 자료

| 문서 | 상태 |
| --- | --- |
| [`rag/iksv2_issue_tracing_guide.md`](./rag/iksv2_issue_tracing_guide.md) | guidance RAG용 정제 문서. 장애 추적 절차와 retrieval query 기준. 런타임 코드 계약은 아니다. |
| [`rag/iksv2_v1.md`](./rag/iksv2_v1.md) | guidance RAG용 IKS v2 상태 판정 문서 v1. 최신 계약 문서가 아니다. |
| [`rag/iksv2_v2.md`](./rag/iksv2_v2.md) | guidance RAG용 IKS v2 트러블슈팅 문서 v2. 최신 계약 문서가 아니다. |
| [`rag_raws/AM.md`](./rag_raws/AM.md) | api-manager 원천 분석. 정제 전 RAG source다. |
| [`rag_raws/EP.md`](./rag_raws/EP.md) | event-processor 원천 분석. 정제 전 RAG source다. |
| [`rag_raws/EW.md`](./rag_raws/EW.md) | event-watcher 원천 분석. 정제 전 RAG source다. |
| [`rag_raws/common.md`](./rag_raws/common.md) | controller 구성/동작 분석 원천. 정제 전 RAG source다. |
| [`rag_raws/iks_db.md`](./rag_raws/iks_db.md) | DB 상태까지 포함한 IKS v2 상태 판정 원천. 현재 runtime guide는 Kubernetes evidence 중심이므로 그대로 계약 문서로 쓰지 않는다. |
| [`rag_raws/iksv2.md`](./rag_raws/iksv2.md) | `draft_runbook_iksv2.md`와 같은 계열의 원천 운영 가이드. 정제본 기준은 `docs/rag/`와 embedded runbook이다. |
| `internal/guidance/runbooks/*.yaml` | embedded guidance runbook. 런타임 기본 검색 자료다. |
| `internal/loganalyzer/rag/runbooks/*.yaml` | log-analyzer legacy/default runbook 자료. guidance runbook과 같은 도메인으로 섞지 않는다. |

## 주요 불일치와 정리 방향

| 항목 | 확인 내용 | 정리 방향 |
| --- | --- | --- |
| `trouble_shooting` / `trouble-shooting` 서버 | `draft_troubleshooting_v1.md`, `draft_for_cluster-api.md`, `kubectl_ai_react_loop_review.md`에 과거 MCP/server 설계가 남아 있음 | 과거 초안 또는 historical review로만 취급. 현재 구현 설명은 `internal/guidance` 내장 client 기준으로 작성한다. |
| `internal/troubleshooting` 경로 | 현재 repository에는 없음 | 새 문서에는 쓰지 않는다. runbook/guidance 구현은 `internal/guidance`와 `internal/guidance/runbooks` 기준. |
| `cluster-api-server` MCP | 미구현이며 후반 재검토에서 제거 권장으로 정리됨 | `draft_for_cluster-api.md`는 미적용 도메인 확장 초안으로 유지. |
| stable docs와 draft 중복 | phase/guidance/mutation 계약이 stable docs와 remediation draft에 동시에 있음 | 현재는 `docs/*.md`를 계약 기준으로 보고, draft는 이력/세부 계획으로 둔다. 안정화되면 중복 draft를 통합한다. |
| RAG 문서 | 운영 지식 문서이며 코드 레이아웃 설명이 아님 | `docs/rag`와 `docs/rag_raws`는 구현 계약/README와 분리한다. |
| `request_processing_phases.md` legacy guide-trigger 문구 | 코드상 자동 initial guide injection은 비활성화됐고 `guidance_lookup` phase에서만 `resource_guide_lookup`을 허용함 | 현재 runtime strategy로 수정 완료. |
| `guide_progress_and_continuation.md` anchor 순서 | 코드상 effective order는 `runtime_state` → `requirement_analysis` → `phase_step` → `guide_step` → `mutation_verification` | 문서 수정 완료. |
| `guide_progress_and_continuation.md` guide step matching | 코드상 rendered command의 whitespace-normalized exact match만 자동 완료로 인정함 | 문서 수정 완료. |
| runtime gate 설명 부족 | 코드상 conversation tool-call, self-talk shell, interactive command, assistant-managed guidance tool, tool failure classification gate가 있음 | `architecture_orchestrator_react.md`, `guide_progress_and_continuation.md`, plan 06에 반영 완료. |
| incident guidance 실행 경계 | `Analyze`는 plan을 만들지만 orchestrator가 summary만 출력하고 unsafe/incomplete command는 숨기며 ReAct에 remediation을 주입하지 않음 | `architecture_orchestrator_react.md`, plan 05에 반영 완료. |

## 코드 레이아웃 기준

| 위치 | 책임 |
| --- | --- |
| `cmd/k8s-assistant` | CLI entrypoint, config/env bootstrap, orchestrator 실행 |
| `cmd/log-analyzer-server` | optional log-analyzer service entrypoint |
| `cmd/guidance-upload` | guidance RAG/runbook Qdrant 업로드 helper |
| `cmd/test-banner` | banner 출력 확인용 개발 helper |
| `internal/react` | ReAct loop, prompt rendering, shim/native structured output, read-only/mutation/guidance gates |
| `internal/orchestrator` | interactive CLI, meta command, formatter, incident guidance side-flow |
| `internal/guidance` | resource guide, incident guide, RAG client, runbook loading/upload logic |
| `internal/loganalyzer` | logs/events/metrics 관찰 및 패턴 분석 domain |
| `internal/toolconnector` | kubectl-ai tool registry integration, optional MCP config sync |
| `internal/config` | config file/env/default handling |
| `internal/k8s` | kubeconfig helper |
| `internal/masking` | masking utility |
| `internal/diagnostic` | diagnostic shared type definitions |
