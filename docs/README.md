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

문서 상태는 다음 의미로 사용한다.

| 상태 | 의미 |
| --- | --- |
| `Resolved` | 해당 계획의 acceptance contract가 현재 코드와 안정 문서에 반영됐다. 관련 영역의 독립 hardening TODO까지 모두 끝났다는 뜻은 아니다. |
| `Active` | 현재 구조에 유효하며 아직 구현 또는 정리가 남아 있다. |
| `Planned` | 기존 구현과 분리된 후속 프로젝트이며 아직 착수하지 않았다. |
| `Dropped` | 현재 구조에서는 실행하지 않는다. 역사적 근거만 보존할 수 있다. |
| `Reference` | 코드 구현 상태를 추적하는 계획이 아니라 domain/RAG 입력 자료다. |

## 문서별 감사 결과

### Root Docs

| 문서 | 적용 상태 |
| --- | --- |
| [`../README.md`](../README.md) | 현재 CLI 개요와 새 `internal/react` package tree를 반영함. |
| [`../GUIDE.md`](../GUIDE.md) | 현재 설정/read-only/meta command와 observation 이후 phase-owned resource-guide 시나리오를 반영함. |
| [`../bug.md`](../bug.md) | package split 이후 현재 코드 `Area`로 갱신함. 개별 증상은 재현 검증 전까지 backlog로 유지함. |
| [`../AGENTS.md`](../AGENTS.md) | facade/coordinator/session/flow/contract 경계와 현재 검증 지침을 반영함. |

### Stable Docs

| 문서 | 적용 상태 | 주요 코드 위치 |
| --- | --- | --- |
| [`react_agent_architecture_blueprint.md`](./react_agent_architecture_blueprint.md) | 현재 Agent를 system context, runtime state, atomic turn, output contract, dispatch, verification, guidance, continuation 관점의 상세 설계도로 설명 | `internal/orchestrator`, `internal/react/*`, `internal/toolconnector`, `internal/guidance` |
| [`architecture_orchestrator_react.md`](./architecture_orchestrator_react.md) | 새 package layout 기준 갱신됨. revisioned session aggregate, immutable dispatch, evidence qualification, bounded continuation과 plan revision 경계를 반영 | `cmd/k8s-assistant`, `internal/orchestrator`, `internal/react/*`, `internal/toolconnector`, `internal/guidance` |
| [`requirement_analysis.md`](./requirement_analysis.md) | 코드 기준 갱신됨. follow-up 계약과 현재 keyword/all-namespaces gap(`BUG-23`, `BUG-24`)을 구분 | `internal/react/contract/structured.go`, `internal/react/prompt/requirement.go`, `internal/react/flow/request`, `internal/react/coordinator/{state,iteration}.go` |
| [`request_processing_phases.md`](./request_processing_phases.md) | 코드 기준 갱신됨. stable goal/phase/step contract, step result, evidence-based plan revision과 phase validation gap(`BUG-7`, `BUG-8`, `BUG-10`, `BUG-21`) 반영 | `internal/react/flow/phase`, `internal/react/session/execution.go`, `internal/react/flow/guidance`, `internal/react/flow/verification`, `internal/react/coordinator/{state,iteration,execution_state,revision}.go` |
| [`guide_progress_and_continuation.md`](./guide_progress_and_continuation.md) | 코드 기준 갱신됨. aggregate state, turn-entry output policy와 guide/verification/continuation known gaps를 별도 표기 | `internal/react/flow/{gate,guidance,verification,report,direction}`, `internal/react/session`, `internal/react/protocol`, `internal/react/coordinator` |
| [`TODO.md`](./TODO.md) | 남은 TODO와 설계 변경 drop 사유 요약 | hardening, cleanup, 중단된 과거 설계 추적 |
| [`README.md`](./README.md) | 이 인덱스 문서. 적용/미적용 상태를 추적함 | docs 분류 기준 |

### Drafts

| 문서 | 상태 | 정리 방향 |
| --- | --- | --- |
| [`drafts/react_gate_outcome_design.md`](./drafts/react_gate_outcome_design.md) | Resolved | `GateOutcome`과 branch/correction 계약은 적용됨. package split 이전 N차 실행 순서는 완료된 migration 이력으로 보존한다. |
| [`drafts/react_remediation_plans/00_overview.md`](./drafts/react_remediation_plans/00_overview.md) | Active index | remediation plan 묶음의 authoritative disposition 표로 유지한다. |
| [`drafts/react_remediation_plans/01_user_input_ownership.md`](./drafts/react_remediation_plans/01_user_input_ownership.md) | Resolved | 현재 구현 기준으로 유지하거나 안정 문서에 병합 가능. |
| [`drafts/react_remediation_plans/02_phase_plan_runtime_contract.md`](./drafts/react_remediation_plans/02_phase_plan_runtime_contract.md) | Resolved | 현재 계약은 `request_processing_phases.md`에 반영됐고 draft는 상세 이력으로 유지한다. |
| [`drafts/react_remediation_plans/03_mutation_lifecycle.md`](./drafts/react_remediation_plans/03_mutation_lifecycle.md) | Resolved | mutation verification 계약은 안정 문서에 반영됨. kubectl failure/manifest parsing은 독립 TODO다. |
| [`drafts/react_remediation_plans/04_namespace_scope_invariant.md`](./drafts/react_remediation_plans/04_namespace_scope_invariant.md) | Active | file/manifest 기반 namespace 검증은 [`TODO.md`](./TODO.md)에서 추적한다. |
| [`drafts/react_remediation_plans/05_rag_boundary.md`](./drafts/react_remediation_plans/05_rag_boundary.md) | Resolved | incident plan은 summary 입력일 뿐 자동 실행/주입 경로가 아니다. |
| [`drafts/react_remediation_plans/06_deterministic_gates_vs_correction.md`](./drafts/react_remediation_plans/06_deterministic_gates_vs_correction.md) | Resolved | runtime gate와 session correction state는 적용됨. kubectl verbose 분류와 semantic hardening은 독립 TODO다. |
| [`drafts/react_remediation_plans/07_explicit_state_machine.md`](./drafts/react_remediation_plans/07_explicit_state_machine.md) | Resolved | revisioned aggregate 단일 source of truth를 반영했으며 Step 1~7은 완료된 migration 이력이다. |
| [`drafts/react_remediation_plans/08_turn_output_contract_and_goal_execution.md`](./drafts/react_remediation_plans/08_turn_output_contract_and_goal_execution.md) | Resolved | Stage 0A와 1~6이 구현됐으며 현재 turn/runtime 계약의 상세 구현 기록이다. |
| [`drafts/react_remediation_plans/09_durable_session_persistence.md`](./drafts/react_remediation_plans/09_durable_session_persistence.md) | Planned | checkpoint/journal과 crash recovery는 turn contract와 분리된 미착수 후속 프로젝트다. |
| [`drafts/draft_troubleshooting_v1.md`](./drafts/draft_troubleshooting_v1.md) | Legacy | 역할 분리와 incident signal 논의는 이력으로 남기고, standalone troubleshooting 서버/package/upload/Agent 재주입 전제만 Dropped로 본다. |
| [`drafts/draft_for_cluster-api.md`](./drafts/draft_for_cluster-api.md) | 미적용 도메인 초안 | Cluster API 확장 논의는 유지하고, 초반의 별도 MCP/troubleshooting package 전제만 Dropped로 본다. |
| [`drafts/draft_runbook_iksv2.md`](./drafts/draft_runbook_iksv2.md) | Reference | 코드 구현 상태 대상이 아니다. 정제본은 `docs/rag/`와 `internal/guidance/runbooks/`를 기준으로 본다. |

### Reviews

| 문서 | 상태 |
| --- | --- |
| [`reviews/kubectl_ai_react_loop_review.md`](./reviews/kubectl_ai_react_loop_review.md) | Historical review. kubectl-ai ReAct loop 분리 근거로 보관한다. 현재 구현은 `internal/react/react.go` facade와 `coordinator` loop를 사용하며, 본문의 단일-package 제안 경로는 설계 이력이다. |
| [`reviews/react_loop_structure_review.md`](./reviews/react_loop_structure_review.md) | 리팩터링 전 구조 리뷰. 새 package 대응표를 추가했으며, package split만으로 개별 리스크가 해결됐다고 간주하지 않는다. |
| [`reviews/revise_troubleshooting.md`](./reviews/revise_troubleshooting.md) | phase-owned resource guide와 guidance/log-analyzer 경계, 구현된 language/empty-result 처리, 남은 incident guidance/delete-recreate 정책을 구분한다. |

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
| `guide_progress_and_continuation.md` anchor 순서 | 코드상 effective order는 `runtime_state` → `requirement_analysis` → `execution` → `phase_step` → `guide_step` → `mutation_verification` | 문서 수정 완료. |
| `guide_progress_and_continuation.md` guide step matching | 코드상 인식 가능한 command tool/shell wrapper에서 추출한 실행문이 rendered command와 whitespace-normalized exact match일 때만 자동 완료로 인정함 | 문서 수정 완료. |
| runtime gate 설명 부족 | 코드상 conversation tool-call, interactive command, read-only effect classification, assistant-managed guidance tool, tool failure classification gate가 있음 | `architecture_orchestrator_react.md`, `guide_progress_and_continuation.md`, plan 06에 반영 완료. |
| incident guidance 실행 경계 | `Analyze`는 plan을 만들지만 orchestrator가 summary만 출력하고 unsafe/incomplete command는 숨기며 ReAct에 remediation을 주입하지 않음 | `architecture_orchestrator_react.md`, plan 05에 반영 완료. |

## 코드 레이아웃 기준

| 위치 | 책임 |
| --- | --- |
| `cmd/k8s-assistant` | CLI entrypoint, config/env bootstrap, orchestrator 실행 |
| `cmd/log-analyzer-server` | optional log-analyzer service entrypoint |
| `cmd/guidance-upload` | guidance RAG/runbook Qdrant 업로드 helper |
| `cmd/test-banner` | banner 출력 확인용 개발 helper |
| `internal/react/react.go` | 외부 facade와 공개 compatibility alias |
| `internal/react/coordinator` | loop lifecycle, model turn, input, execution, output, dependency wiring |
| `internal/react/session` | revisioned aggregate, lifecycle projection, goal execution ledger |
| `internal/react/flow` | request/phase/guidance/verification/report/direction/gate reducer와 validation |
| `internal/react/contract` | immutable enum, event/effect, structured payload, shared runtime references |
| `internal/react/protocol` | internal structured call, schema, native/shim normalization |
| `internal/react/kube` | kubectl command parsing, resource/target normalization, read-only policy |
| `internal/react/{prompt,provider,language}` | prompt rendering, LLM setup, user-facing translation |
| `internal/orchestrator` | interactive CLI, meta command, formatter, incident guidance side-flow |
| `internal/guidance` | resource guide, incident guide, RAG client, runbook loading/upload logic |
| `internal/loganalyzer` | logs/events/metrics 관찰 및 패턴 분석 domain |
| `internal/toolconnector` | kubectl-ai tool registry integration, optional MCP config sync |
| `internal/config` | config file/env/default handling |
| `internal/k8s` | kubeconfig helper |
| `internal/masking` | masking utility |
| `internal/diagnostic` | diagnostic shared type definitions |
