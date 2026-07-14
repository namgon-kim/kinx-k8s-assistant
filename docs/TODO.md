# Documentation TODO

현재 코드 기준으로 남은 작업과, 설계 변경으로 중단한 항목만 짧게 기록한다.

## Active TODO

| Area | Remaining work |
| --- | --- |
| Requirement analysis | aggregate previous-context budget/structured truncation shape, runtime guarantee table, clarification boundary table |
| Request phases | phase completion semantic validation, `-A` discovery context promotion, observation convergence rules |
| Guide/continuation | final-report evidence levels, bounded fallback/directive/error catalog |
| Flow/gate extraction | production gate target mapping, consume/enforce ordering의 pure decision/apply split, correction counter review |
| Namespace/mutation policy | `kubectl apply -f ...` manifest namespace validation, wrapped kubectl mutation classifier |
| State/session ownership cleanup | remove coordinator compatibility state, make `session.State.Control` and nested session state the only mutable source, derive directives/snapshots from it |

## Dropped

| Dropped item | Reason |
| --- | --- |
| Standalone `trouble_shooting` MCP/server and `internal/troubleshooting` package | Current design moved guidance to `internal/guidance`; only configured MCP servers are loaded. |
| `troubleshooting-upload` helper | Replaced in current layout by `cmd/guidance-upload`. |
| Feeding remediation plans back into the kubectl-ai Agent | Current code owns execution in `internal/react`; Kubernetes changes stay in the k8s-assistant ReAct/tool loop and approval flow. |
| `cluster-api-server` MCP | Dropped by design review because Kubernetes data collection belongs to kubectl/ReAct, not a separate MCP client. |
| log-analyzer RAG/remediation as the default path | Current boundary keeps log-analyzer on logs/events/metrics pattern analysis; guidance owns runbook/RAG planning. |
