# Log Analyzer

`internal/loganalyzer` is the read-only observation layer for logs and metrics.
It collects evidence from file logs, Loki, Prometheus, Grafana, and OpenSearch,
stores large raw responses as artifacts, and returns compact summaries/samples to the ReAct loop.

It does not perform RAG, runbook lookup, remediation planning, or Kubernetes
mutations. Those belong to `internal/troubleshooting` and the main ReAct
approval flow.

The previous `internal/loganalyzer/rag` implementation and log-analyzer
runbook YAMLs were removed. Keep RAG-related code under
`internal/troubleshooting` only.

## How It Is Used

The default path is internal tool registration through `internal/toolconnector`.
When `log_analyzer.enabled` is true in `~/.k8s-assistant/config.yaml`,
k8s-assistant exposes `log_analyzer_*` tools directly to the model. Detailed
source settings are loaded from `~/.k8s-assistant/log-analyzer.yaml`. You do
not need to run `log-analyzer-server` for normal use.

The optional `cmd/log-analyzer-server` still exists for MCP compatibility, but
it is not the primary integration path.

## Standalone MCP Server

`cmd/log-analyzer-server` can run log-analyzer as a separate MCP server. This is
for MCP compatibility and external MCP clients; normal k8s-assistant usage does
not require it.

Start the server in a separate terminal:

```bash
log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --loki-url http://localhost:3100 \
  --prometheus-url http://localhost:9090 \
  --grafana-url http://localhost:3000 \
  --opensearch-url https://localhost:9200 \
  --opensearch-index 'logs-*'
```

The MCP endpoint is:

```text
http://localhost:9090/mcp
```

Register it in `~/.k8s-assistant/mcp.yaml`:

```yaml
servers:
  - name: log-analyzer
    url: http://localhost:9090/mcp
    use_streaming: true
    timeout: 60
```

Then run k8s-assistant with MCP client mode:

```bash
k8s-assistant --mcp-client
```

Standalone server source settings are passed through flags and
`K8S_ASSISTANT_*` environment variables. The main `~/.k8s-assistant/config.yaml`
still only controls the internal adapter's `log_analyzer.enabled` toggle.

Typical flow:

1. Check source configuration with `log_analyzer_check_sources`.
2. Query file/Loki/Prometheus/Grafana/OpenSearch tools.
3. Use returned `artifact_id` for follow-up analysis.
4. Extract patterns or key evidence.
5. Pass summarized problem signals to troubleshooting when appropriate.

## Configuration

Use `~/.k8s-assistant/config.yaml` only to enable or disable the feature:

```yaml
log_analyzer:
  enabled: true
```

Put source/client settings in `~/.k8s-assistant/log-analyzer.yaml`:

```yaml
artifact_dir: ""            # Default: ~/.k8s-assistant/artifacts/loganalyzer
artifact_ttl: 86400
max_artifact_bytes: 52428800
file:
  enabled: true
  root_dir: /var/log/filebeat
  max_lines: 1000
loki:
  enabled: true
  url: "http://localhost:3100"
  token: ""
  username: ""
  password: ""
  org_id: ""                # Sent as X-Scope-OrgID
  headers: {}
  tls_skip_verify: false
  ca_file: ""
  timeout: 30
  query_timeout: 30
  default_limit: 1000
  max_entries: 5000
prometheus:
  enabled: true
  url: "http://localhost:9090"
  token: ""
  username: ""
  password: ""
  headers: {}
  tls_skip_verify: false
  ca_file: ""
  timeout: 30
  query_timeout: 30
grafana:
  enabled: true
  url: "http://localhost:3000"
  token: ""
  username: ""
  password: ""
  org_id: ""                # Sent as X-Grafana-Org-Id
  headers: {}
  tls_skip_verify: false
  ca_file: ""
  timeout: 30
  query_timeout: 30
opensearch:
  enabled: true
  url: "https://localhost:9200"
  token: ""
  username: ""
  password: ""
  headers: {}
  tls_skip_verify: false
  ca_file: ""
  timeout: 30
  query_timeout: 30
  default_limit: 100
  max_entries: 1000
  default_index: "logs-*"
```

Environment overrides:

```bash
export K8S_ASSISTANT_LOKI_URL=http://localhost:3100
export K8S_ASSISTANT_LOKI_TOKEN=...
export K8S_ASSISTANT_LOKI_USERNAME=...
export K8S_ASSISTANT_LOKI_PASSWORD=...
export K8S_ASSISTANT_LOKI_ORG_ID=tenant-a

export K8S_ASSISTANT_PROMETHEUS_URL=http://localhost:9090
export K8S_ASSISTANT_PROMETHEUS_TOKEN=...
export K8S_ASSISTANT_PROMETHEUS_USERNAME=...
export K8S_ASSISTANT_PROMETHEUS_PASSWORD=...

export K8S_ASSISTANT_GRAFANA_URL=http://localhost:3000
export K8S_ASSISTANT_GRAFANA_TOKEN=...
export K8S_ASSISTANT_GRAFANA_USERNAME=...
export K8S_ASSISTANT_GRAFANA_PASSWORD=...
export K8S_ASSISTANT_GRAFANA_ORG_ID=1

export K8S_ASSISTANT_OPENSEARCH_URL=https://localhost:9200
export K8S_ASSISTANT_OPENSEARCH_TOKEN=...
export K8S_ASSISTANT_OPENSEARCH_USERNAME=...
export K8S_ASSISTANT_OPENSEARCH_PASSWORD=...
export K8S_ASSISTANT_OPENSEARCH_DEFAULT_INDEX=logs-*
```

## Tools

All tools are read-only and report `modifies_resource=no`.

| Tool | Purpose |
|---|---|
| `log_analyzer_check_sources` | Show enabled/configured/reachable status for file, Loki, Prometheus, Grafana, OpenSearch |
| `log_analyzer_fetch_logs` | Fetch file logs or dispatch to Loki by namespace/pod/container |
| `log_analyzer_query_loki` | Run Loki `query_range` |
| `log_analyzer_query_loki_instant` | Run Loki instant `query` |
| `log_analyzer_query_loki_labels` | List Loki label names or values |
| `log_analyzer_query_loki_series` | List Loki series for matchers/time range |
| `log_analyzer_query_prometheus_instant` | Run instant PromQL |
| `log_analyzer_query_prometheus_range` | Run range PromQL |
| `log_analyzer_list_prometheus_alerts` | List Prometheus alerts |
| `log_analyzer_list_prometheus_rules` | List Prometheus rule groups |
| `log_analyzer_list_prometheus_targets` | List Prometheus targets |
| `log_analyzer_list_grafana_datasources` | List Grafana datasources |
| `log_analyzer_query_grafana_datasource` | Query a Grafana datasource proxy by UID |
| `log_analyzer_search_grafana_dashboards` | Search Grafana dashboards |
| `log_analyzer_get_grafana_dashboard` | Get a Grafana dashboard by UID |
| `log_analyzer_extract_grafana_panel_queries` | Extract panel datasource queries |
| `log_analyzer_list_grafana_alert_rules` | List Grafana unified alert rules |
| `log_analyzer_list_opensearch_indices` | List OpenSearch indices |
| `log_analyzer_get_opensearch_mapping` | Get OpenSearch index mapping |
| `log_analyzer_query_opensearch` | Query OpenSearch logs |
| `log_analyzer_analyze_pattern` | Detect log patterns from logs or a log artifact |
| `log_analyzer_analyze_metric_pattern` | Detect metric patterns from a metric artifact |
| `log_analyzer_key_evidence` | Extract important log/metric evidence from an artifact |
| `log_analyzer_summarize_evidence` | Compress evidence into short problem signals |
| `log_analyzer_get_artifact_sample` | Read bounded lines from an artifact |
| `log_analyzer_clean_artifacts` | Cleanup artifacts by TTL/size |

## File Logs

`log_analyzer_fetch_logs` uses `file.root_dir` from `log-analyzer.yaml` as the search root.
It matches namespace, pod, and container names against path segments and file
names, prefers stronger matches, and reads newest files first.

Examples:

```json
{
  "source": "file",
  "namespace": "payments",
  "pod_name": "api",
  "container_name": "app",
  "max_lines": 500,
  "level": "ERROR"
}
```

To read one file directly:

```json
{
  "source": "file",
  "file_path": "payments/api/app.log",
  "max_lines": 200
}
```

Relative `file_path` values are resolved under `root_dir`. Absolute paths are
treated as explicit user input. `max_lines` is applied as tail-style latest-N
after `level` and `since_seconds` filtering.

## Loki

Use Loki when logs are already aggregated. Raw Loki responses are stored as
artifacts; the tool output includes counts and bounded samples.

Range query:

```json
{
  "query": "{namespace=\"payments\", pod=~\"api-.*\"} |= \"error\"",
  "start": "2026-05-14T00:00:00Z",
  "end": "2026-05-14T01:00:00Z",
  "limit": 1000,
  "direction": "backward"
}
```

Labels or values:

```json
{
  "name": "pod",
  "query": "{namespace=\"payments\"}",
  "start": "2026-05-14T00:00:00Z",
  "end": "2026-05-14T01:00:00Z"
}
```

Series:

```json
{
  "matcher": "{namespace=\"payments\"},{namespace=\"platform\"}",
  "start": "2026-05-14T00:00:00Z",
  "end": "2026-05-14T01:00:00Z"
}
```

For multi-tenant Loki, set `loki.org_id`; it is sent as `X-Scope-OrgID`.

## Prometheus

Prometheus tools return parsed series plus API `warnings` and `infos` when the
server sends them. Raw responses are stored as artifacts when appropriate.

Instant query:

```json
{
  "query": "sum(rate(http_requests_total{status=~\"5..\"}[5m])) by (service)"
}
```

Range query:

```json
{
  "query": "rate(container_cpu_usage_seconds_total{namespace=\"payments\"}[5m])",
  "start": "2026-05-14T00:00:00Z",
  "end": "2026-05-14T01:00:00Z",
  "step": "60s"
}
```

Operational inventory:

```json
{}
```

Use the empty input with:

- `log_analyzer_list_prometheus_alerts`
- `log_analyzer_list_prometheus_rules`
- `log_analyzer_list_prometheus_targets`

## Grafana

Grafana is used as a read-only UI/API source. It can list datasources, query a
datasource through Grafana proxy, search/load dashboards, extract panel queries,
and list unified alert rules. Raw responses are stored as artifacts.

Datasource proxy query:

```json
{
  "datasource_uid": "prometheus",
  "path": "/api/v1/query",
  "query": "up",
  "time": "2026-05-14T01:00:00Z"
}
```

Dashboard query extraction:

```json
{
  "uid": "cluster-overview"
}
```

For multi-org Grafana, set `grafana.org_id`; it is sent as
`X-Grafana-Org-Id`.

## OpenSearch

OpenSearch is used as a read-only log search backend. It can list indices, read
index mappings, and run bounded log searches. Raw search responses are stored as
artifacts; tool output returns hit counts and a bounded `LogEntry` sample.

List indices:

```json
{}
```

Query logs:

```json
{
  "index": "logs-*",
  "query_string": "error OR exception",
  "namespace": "payments",
  "pod_name": "api",
  "start": "2026-05-14T00:00:00Z",
  "end": "2026-05-14T01:00:00Z",
  "limit": 100
}
```

If `opensearch.default_index` is set, `index` may be omitted. Search results are
converted to log entries by looking for common `_source` fields such as
`@timestamp`, `timestamp`, `level`, `log.level`, `message`, `msg`, `log`, and
`body`. The original `_source` remains in the artifact for follow-up sampling or
pattern analysis.

## Artifacts And Follow-Up

Large raw results are not placed directly into the model context. Tools return
an `artifact_id` and a small sample. Use the artifact ID for follow-up tools:

```json
{
  "artifact_id": "loki-logs-...",
  "pod_name": "api",
  "namespace": "payments"
}
```

Useful follow-ups:

- `log_analyzer_analyze_pattern`
- `log_analyzer_analyze_metric_pattern`
- `log_analyzer_key_evidence`
- `log_analyzer_get_artifact_sample`

Artifacts are stored under `artifact_dir` and can be cleaned with
`log_analyzer_clean_artifacts`.

## Boundaries

Log Analyzer may:

- collect logs/metrics/events from configured read-only sources
- classify deterministic log/metric patterns
- summarize evidence and artifact samples

Log Analyzer must not:

- run Kubernetes mutations
- choose remediation steps
- perform runbook/RAG search
- execute troubleshooting actions directly
