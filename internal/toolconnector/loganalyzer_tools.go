package toolconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type logAnalyzerTool struct {
	name        string
	description string
	required    []string
	properties  map[string]*gollm.Schema
	run         func(context.Context, map[string]any) (any, error)
}

var _ tools.Tool = (*logAnalyzerTool)(nil)

func RegisterLogAnalyzerTools(registry *tools.Tools, analyzer loganalyzer.Analyzer) {
	for _, tool := range logAnalyzerTools(analyzer) {
		if registry.Lookup(tool.Name()) == nil {
			registry.RegisterTool(tool)
		}
	}
}

func logAnalyzerTools(analyzer loganalyzer.Analyzer) []*logAnalyzerTool {
	return []*logAnalyzerTool{
		{
			name:        "log_analyzer_fetch_logs",
			description: "Fetch file, Loki, or OpenSearch logs and store raw results as an artifact. Read-only.",
			required:    []string{},
			properties: props(
				strProp("source", "Log source: file, loki, or opensearch. Defaults to file."),
				strProp("index", "OpenSearch index or index pattern when source=opensearch."),
				strProp("file_path", "Optional file path. Relative paths are resolved under log-analyzer.yaml file.root_dir; absolute paths are read as explicitly requested."),
				strProp("namespace", "Kubernetes namespace."),
				strProp("pod_name", "Pod name or substring."),
				strProp("container_name", "Container name."),
				numProp("since_seconds", "Lookback window in seconds."),
				numProp("max_lines", "Maximum log lines."),
				strProp("level", "Optional level filter for file logs."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.FetchLogs(ctx, loganalyzer.FetchLogsRequest{
					Source:        stringArg(args, "source"),
					Index:         stringArg(args, "index"),
					FilePath:      stringArg(args, "file_path"),
					Namespace:     stringArg(args, "namespace"),
					PodName:       stringArg(args, "pod_name"),
					ContainerName: stringArg(args, "container_name"),
					SinceSeconds:  int64Arg(args, "since_seconds"),
					MaxLines:      intArg(args, "max_lines"),
					Level:         stringArg(args, "level"),
				})
			},
		},
		{
			name:        "log_analyzer_query_loki",
			description: "Run Loki query_range and store raw log results as an artifact. Read-only.",
			required:    []string{"query"},
			properties: props(
				strProp("query", "LogQL query."),
				strProp("start", "Optional RFC3339 start time."),
				strProp("end", "Optional RFC3339 end time."),
				strProp("step", "Optional query step."),
				numProp("limit", "Maximum entries."),
				strProp("direction", "forward or backward."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryLoki(ctx, loganalyzer.LokiQueryRequest{
					Query:     stringArg(args, "query"),
					Start:     timeArg(args, "start"),
					End:       timeArg(args, "end"),
					Step:      stringArg(args, "step"),
					Limit:     intArg(args, "limit"),
					Direction: stringArg(args, "direction"),
				})
			},
		},
		{
			name:        "log_analyzer_query_loki_instant",
			description: "Run Loki instant query and store raw log results as an artifact. Read-only.",
			required:    []string{"query"},
			properties: props(
				strProp("query", "LogQL query."),
				strProp("time", "Optional RFC3339 query time."),
				numProp("limit", "Maximum entries."),
				strProp("direction", "forward or backward."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryLokiInstant(ctx, loganalyzer.LokiQueryRequest{
					Query:     stringArg(args, "query"),
					End:       timeArg(args, "time"),
					Limit:     intArg(args, "limit"),
					Direction: stringArg(args, "direction"),
				})
			},
		},
		{
			name:        "log_analyzer_query_loki_labels",
			description: "List Loki labels or values for a specific label. Read-only.",
			properties: props(
				strProp("name", "Label name. Omit to list label names."),
				strProp("matcher", "Optional comma-separated LogQL stream matchers."),
				strProp("query", "Optional LogQL stream matcher."),
				strProp("start", "Optional RFC3339 start time."),
				strProp("end", "Optional RFC3339 end time."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryLokiLabels(ctx, loganalyzer.LokiLabelsRequest{
					Name:    stringArg(args, "name"),
					Matcher: splitCSV(stringArg(args, "matcher")),
					Query:   stringArg(args, "query"),
					Start:   timeArg(args, "start"),
					End:     timeArg(args, "end"),
				})
			},
		},
		{
			name:        "log_analyzer_query_loki_series",
			description: "List Loki series for optional matchers and time range. Read-only.",
			properties: props(
				strProp("matcher", "Optional comma-separated LogQL stream matchers."),
				strProp("query", "Optional LogQL stream matcher."),
				strProp("start", "Optional RFC3339 start time."),
				strProp("end", "Optional RFC3339 end time."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryLokiSeries(ctx, loganalyzer.LokiSeriesRequest{
					Matcher: splitCSV(stringArg(args, "matcher")),
					Query:   stringArg(args, "query"),
					Start:   timeArg(args, "start"),
					End:     timeArg(args, "end"),
				})
			},
		},
		{
			name:        "log_analyzer_query_prometheus_instant",
			description: "Run instant PromQL and store metric evidence as an artifact. Read-only.",
			required:    []string{"query"},
			properties: props(
				strProp("query", "PromQL query."),
				strProp("time", "Optional RFC3339 query time."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryPrometheusInstant(ctx, loganalyzer.PrometheusInstantRequest{
					Query: stringArg(args, "query"),
					Time:  timeArg(args, "time"),
				})
			},
		},
		{
			name:        "log_analyzer_query_prometheus_range",
			description: "Run range PromQL and store metric evidence as an artifact. Read-only.",
			required:    []string{"query", "start", "end"},
			properties: props(
				strProp("query", "PromQL query."),
				strProp("start", "RFC3339 start time."),
				strProp("end", "RFC3339 end time."),
				strProp("step", "Query step, e.g. 60s."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryPrometheusRange(ctx, loganalyzer.PrometheusRangeRequest{
					Query: stringArg(args, "query"),
					Start: timeArg(args, "start"),
					End:   timeArg(args, "end"),
					Step:  stringArg(args, "step"),
				})
			},
		},
		{
			name:        "log_analyzer_list_prometheus_alerts",
			description: "List Prometheus alerts and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListPrometheusAlerts(ctx)
			},
		},
		{
			name:        "log_analyzer_list_prometheus_rules",
			description: "List Prometheus rules and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListPrometheusRules(ctx)
			},
		},
		{
			name:        "log_analyzer_list_prometheus_targets",
			description: "List Prometheus targets and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListPrometheusTargets(ctx)
			},
		},
		{
			name:        "log_analyzer_list_grafana_datasources",
			description: "List Grafana datasources and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListGrafanaDatasources(ctx)
			},
		},
		{
			name:        "log_analyzer_query_grafana_datasource",
			description: "Run a read-only Grafana datasource proxy GET query by datasource UID. Read-only.",
			required:    []string{"datasource_uid"},
			properties: props(
				strProp("datasource_uid", "Grafana datasource UID."),
				strProp("path", "Datasource proxy path, e.g. /api/v1/query. Defaults to /api/v1/query."),
				strProp("params", "Optional query params as JSON object or comma-separated key=value pairs."),
				strProp("query", "Optional query expression."),
				strProp("start", "Optional RFC3339 start time."),
				strProp("end", "Optional RFC3339 end time."),
				strProp("time", "Optional RFC3339 query time."),
				strProp("step", "Optional query step."),
				numProp("limit", "Optional result limit."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryGrafanaDatasource(ctx, loganalyzer.GrafanaDatasourceQueryRequest{
					DatasourceUID: stringArg(args, "datasource_uid"),
					Path:          stringArg(args, "path"),
					Params:        stringMapArg(args, "params"),
					Query:         stringArg(args, "query"),
					Start:         timeArg(args, "start"),
					End:           timeArg(args, "end"),
					Time:          timeArg(args, "time"),
					Step:          stringArg(args, "step"),
					Limit:         intArg(args, "limit"),
				})
			},
		},
		{
			name:        "log_analyzer_search_grafana_dashboards",
			description: "Search Grafana dashboards and store raw response as an artifact. Read-only.",
			properties: props(
				strProp("query", "Dashboard search query."),
				strProp("type", "Optional Grafana search type, e.g. dash-db."),
				numProp("limit", "Optional result limit."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.SearchGrafanaDashboards(ctx, loganalyzer.GrafanaDashboardSearchRequest{
					Query: stringArg(args, "query"),
					Type:  stringArg(args, "type"),
					Limit: intArg(args, "limit"),
				})
			},
		},
		{
			name:        "log_analyzer_get_grafana_dashboard",
			description: "Get a Grafana dashboard by UID and store raw response as an artifact. Read-only.",
			required:    []string{"uid"},
			properties: props(
				strProp("uid", "Grafana dashboard UID."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.GetGrafanaDashboard(ctx, loganalyzer.GrafanaDashboardRequest{UID: stringArg(args, "uid")})
			},
		},
		{
			name:        "log_analyzer_extract_grafana_panel_queries",
			description: "Extract datasource queries from a Grafana dashboard UID or dashboard artifact. Read-only.",
			properties: props(
				strProp("uid", "Grafana dashboard UID."),
				strProp("artifact_id", "Dashboard artifact id from log_analyzer_get_grafana_dashboard."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ExtractGrafanaPanelQueries(ctx, loganalyzer.GrafanaPanelQueryRequest{
					UID:        stringArg(args, "uid"),
					ArtifactID: stringArg(args, "artifact_id"),
				})
			},
		},
		{
			name:        "log_analyzer_list_grafana_alert_rules",
			description: "List Grafana unified alert rules and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListGrafanaAlertRules(ctx)
			},
		},
		{
			name:        "log_analyzer_list_opensearch_indices",
			description: "List OpenSearch indices and store raw response as an artifact. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.ListOpenSearchIndices(ctx)
			},
		},
		{
			name:        "log_analyzer_get_opensearch_mapping",
			description: "Get OpenSearch index mapping and store raw response as an artifact. Read-only.",
			properties: props(
				strProp("index", "OpenSearch index or index pattern. Defaults to opensearch.default_index."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.GetOpenSearchMapping(ctx, loganalyzer.OpenSearchMappingRequest{Index: stringArg(args, "index")})
			},
		},
		{
			name:        "log_analyzer_query_opensearch",
			description: "Query OpenSearch logs and store raw search response as an artifact. Read-only.",
			properties: props(
				strProp("index", "OpenSearch index or index pattern. Defaults to opensearch.default_index."),
				strProp("query_string", "OpenSearch query_string query."),
				strProp("namespace", "Kubernetes namespace."),
				strProp("pod_name", "Pod name."),
				strProp("container_name", "Container name."),
				strProp("level", "Log level."),
				strProp("message", "Message text filter."),
				strProp("start", "Optional RFC3339 start time."),
				strProp("end", "Optional RFC3339 end time."),
				numProp("limit", "Maximum hits."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.QueryOpenSearch(ctx, loganalyzer.OpenSearchQueryRequest{
					Index:         stringArg(args, "index"),
					QueryString:   stringArg(args, "query_string"),
					Namespace:     stringArg(args, "namespace"),
					PodName:       stringArg(args, "pod_name"),
					ContainerName: stringArg(args, "container_name"),
					Level:         stringArg(args, "level"),
					Message:       stringArg(args, "message"),
					Start:         timeArg(args, "start"),
					End:           timeArg(args, "end"),
					Limit:         intArg(args, "limit"),
				})
			},
		},
		{
			name:        "log_analyzer_check_sources",
			description: "Check configured log-analyzer sources without changing resources. Read-only.",
			properties:  props(),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.CheckSources(ctx)
			},
		},
		{
			name:        "log_analyzer_analyze_pattern",
			description: "Analyze log entries or a log artifact for deterministic patterns. Read-only.",
			properties: props(
				strProp("artifact_id", "Log artifact id."),
				strProp("pod_name", "Pod name for summary context."),
				strProp("namespace", "Namespace for summary context."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.AnalyzePattern(ctx, loganalyzer.AnalyzePatternRequest{
					ArtifactID: stringArg(args, "artifact_id"),
					PodName:    stringArg(args, "pod_name"),
					Namespace:  stringArg(args, "namespace"),
				})
			},
		},
		{
			name:        "log_analyzer_analyze_metric_pattern",
			description: "Analyze a metric artifact for deterministic metric patterns. Read-only.",
			properties: props(
				strProp("artifact_id", "Metric artifact id."),
				strProp("query", "Original PromQL query for pattern hints."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.AnalyzeMetricPattern(ctx, loganalyzer.AnalyzeMetricPatternRequest{
					ArtifactID: stringArg(args, "artifact_id"),
					Query:      stringArg(args, "query"),
				})
			},
		},
		{
			name:        "log_analyzer_key_evidence",
			description: "Extract key evidence from a log artifact and detected patterns. Read-only.",
			properties: props(
				strProp("artifact_id", "Artifact id."),
				numProp("limit", "Maximum evidence items."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.KeyEvidence(ctx, loganalyzer.KeyEvidenceRequest{
					ArtifactID: stringArg(args, "artifact_id"),
					Limit:      intArg(args, "limit"),
				})
			},
		},
		{
			name:        "log_analyzer_summarize_evidence",
			description: "Summarize evidence items into concise problem signals. Read-only.",
			properties: props(
				strProp("artifact_id", "Related artifact id."),
				strProp("patterns", "Comma-separated pattern names or signals."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.SummarizeEvidence(ctx, loganalyzer.SummarizeEvidenceRequest{
					ArtifactID: stringArg(args, "artifact_id"),
					Patterns:   splitCSV(stringArg(args, "patterns")),
				})
			},
		},
		{
			name:        "log_analyzer_get_artifact_sample",
			description: "Read a bounded sample from a log-analyzer artifact. Read-only.",
			required:    []string{"artifact_id"},
			properties: props(
				strProp("artifact_id", "Artifact id."),
				numProp("max_lines", "Maximum lines to return."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.GetArtifactSample(ctx, loganalyzer.ArtifactSampleRequest{
					ArtifactID: stringArg(args, "artifact_id"),
					MaxLines:   intArg(args, "max_lines"),
				})
			},
		},
		{
			name:        "log_analyzer_clean_artifacts",
			description: "Clean log-analyzer artifacts using TTL and max-bytes limits. Read-only.",
			properties: props(
				numProp("ttl_seconds", "Artifact TTL in seconds."),
				numProp("max_bytes", "Maximum artifact store bytes."),
			),
			run: func(ctx context.Context, args map[string]any) (any, error) {
				return analyzer.CleanArtifacts(ctx, loganalyzer.CleanArtifactsRequest{
					TTLSeconds: int64Arg(args, "ttl_seconds"),
					MaxBytes:   int64Arg(args, "max_bytes"),
				})
			},
		},
	}
}

func (t *logAnalyzerTool) Name() string {
	return t.name
}

func (t *logAnalyzerTool) Description() string {
	return t.description
}

func (t *logAnalyzerTool) FunctionDefinition() *gollm.FunctionDefinition {
	return &gollm.FunctionDefinition{
		Name:        t.name,
		Description: t.description,
		Parameters: &gollm.Schema{
			Type:       gollm.TypeObject,
			Properties: t.properties,
			Required:   t.required,
		},
	}
}

func (t *logAnalyzerTool) Run(ctx context.Context, args map[string]any) (any, error) {
	result, err := t.run(ctx, args)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (t *logAnalyzerTool) IsInteractive(args map[string]any) (bool, error) {
	return false, nil
}

func (t *logAnalyzerTool) CheckModifiesResource(args map[string]any) string {
	return "no"
}

func props(fields ...map[string]*gollm.Schema) map[string]*gollm.Schema {
	out := map[string]*gollm.Schema{}
	for _, field := range fields {
		for key, value := range field {
			out[key] = value
		}
	}
	return out
}

func strProp(name, description string) map[string]*gollm.Schema {
	return map[string]*gollm.Schema{name: &gollm.Schema{Type: gollm.TypeString, Description: description}}
}

func numProp(name, description string) map[string]*gollm.Schema {
	return map[string]*gollm.Schema{name: &gollm.Schema{Type: gollm.TypeNumber, Description: description}}
}

func stringArg(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func intArg(args map[string]any, key string) int {
	return int(int64Arg(args, key))
}

func int64Arg(args map[string]any, key string) int64 {
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		i, _ := v.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return i
	default:
		return 0
	}
}

func timeArg(args map[string]any, key string) time.Time {
	raw := stringArg(args, key)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func stringMapArg(args map[string]any, key string) map[string]string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch v := value.(type) {
	case map[string]string:
		return v
	case map[string]any:
		out := map[string]string{}
		for key, value := range v {
			out[key] = fmt.Sprint(value)
		}
		return out
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return nil
		}
		var parsed map[string]string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return parsed
		}
		out := map[string]string{}
		for _, part := range strings.Split(raw, ",") {
			pieces := strings.SplitN(part, "=", 2)
			if len(pieces) != 2 {
				continue
			}
			name := strings.TrimSpace(pieces[0])
			if name != "" {
				out[name] = strings.TrimSpace(pieces[1])
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}
