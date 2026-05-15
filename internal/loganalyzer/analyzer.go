package loganalyzer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	grafclient "github.com/grafana/grafana-openapi-client-go/client"
	grafdashboards "github.com/grafana/grafana-openapi-client-go/client/dashboards"
	grafdatasources "github.com/grafana/grafana-openapi-client-go/client/datasources"
	grafprovisioning "github.com/grafana/grafana-openapi-client-go/client/provisioning"
	grafsearch "github.com/grafana/grafana-openapi-client-go/client/search"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type AnalyzerImpl struct {
	cfg       config.LogAnalyzerConfig
	lokiHTTP  *http.Client
	promHTTP  *http.Client
	promAPI   promv1.API
	grafHTTP  *http.Client
	grafAPI   *grafclient.GrafanaHTTPAPI
	osHTTP    *http.Client
	osClient  *opensearch.Client
	detector  *PatternDetector
	artifacts *ArtifactStore
	parser    LogParser
}

func NewAnalyzerFromConfig(cfg config.LogAnalyzerConfig) Analyzer {
	promClient := newPrometheusAPI(cfg.Prometheus)
	grafClient := newGrafanaAPI(cfg.Grafana)
	osClient := newOpenSearchClient(cfg.OpenSearch)
	return &AnalyzerImpl{
		cfg:       cfg,
		lokiHTTP:  newSourceHTTPClient(cfg.Loki),
		promHTTP:  newSourceHTTPClient(cfg.Prometheus),
		promAPI:   promClient,
		grafHTTP:  newSourceHTTPClient(cfg.Grafana),
		grafAPI:   grafClient,
		osHTTP:    newSourceHTTPClient(cfg.OpenSearch),
		osClient:  osClient,
		detector:  NewPatternDetector(),
		artifacts: NewArtifactStore(cfg.ArtifactDir),
		parser:    NewJSONParser(),
	}
}

var _ Analyzer = (*AnalyzerImpl)(nil)

func (a *AnalyzerImpl) FetchLogs(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error) {
	if strings.EqualFold(req.Source, "loki") {
		query := lokiSelector(req.Namespace, req.PodName, req.ContainerName)
		res, err := a.QueryLoki(ctx, LokiQueryRequest{Query: query, Limit: req.MaxLines})
		if err != nil {
			return nil, err
		}
		return &FetchLogsResult{
			Logs:       nil,
			TotalLine:  res.TotalLine,
			Source:     res.Source,
			ArtifactID: res.ArtifactID,
			Artifact:   res.Artifact,
			Sample:     res.Sample,
			Summary:    res.Summary,
		}, nil
	}
	if strings.EqualFold(req.Source, "opensearch") {
		start := time.Time{}
		if req.SinceSeconds > 0 {
			start = time.Now().Add(-time.Duration(req.SinceSeconds) * time.Second)
		}
		res, err := a.QueryOpenSearch(ctx, OpenSearchQueryRequest{
			Index:         req.Index,
			Namespace:     req.Namespace,
			PodName:       req.PodName,
			ContainerName: req.ContainerName,
			Level:         req.Level,
			Limit:         req.MaxLines,
			Start:         start,
		})
		if err != nil {
			return nil, err
		}
		return &FetchLogsResult{
			Logs:       nil,
			TotalLine:  res.Total,
			Source:     res.Source,
			ArtifactID: res.ArtifactID,
			Artifact:   res.Artifact,
			Sample:     res.Sample,
			Summary:    res.Summary,
		}, nil
	}
	if !a.cfg.File.Enabled {
		return nil, errors.New("file log source is disabled")
	}

	if req.Namespace == "" {
		req.Namespace = "default"
	}
	maxLines := boundedLimit(req.MaxLines, a.cfg.File.MaxLines, 1000, 0)

	files, err := a.matchLogFiles(req)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return &FetchLogsResult{
			TotalLine: 0,
			Source:    "file",
			Summary:   "no matching file logs found",
		}, nil
	}
	logs := make([]LogEntry, 0, maxLines)
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := a.readLogFile(path, req, maxLines-len(logs))
		if err != nil {
			return nil, err
		}
		logs = append(logs, entries...)
		if len(logs) >= maxLines {
			break
		}
	}

	artifactID, artifactPath, err := a.artifacts.WriteJSON("file-logs", logs)
	if err != nil {
		return nil, fmt.Errorf("write file log artifact: %w", err)
	}
	return &FetchLogsResult{
		Logs:       nil,
		TotalLine:  len(logs),
		Source:     strings.Join(files, ","),
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Sample:     limitLogs(logs, 20),
		Summary:    fmt.Sprintf("fetched %d log lines from file source", len(logs)),
	}, nil
}

func (a *AnalyzerImpl) QueryLoki(ctx context.Context, req LokiQueryRequest) (*LogQueryResult, error) {
	if err := validateHTTPSource("loki", a.cfg.Loki); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("loki query is required")
	}
	limit := boundedLimit(req.Limit, a.cfg.Loki.DefaultLimit, 1000, a.cfg.Loki.MaxEntries)

	return a.queryLoki(ctx, "/loki/api/v1/query_range", lokiQueryValues(req, limit), req.Query)
}

func (a *AnalyzerImpl) QueryLokiInstant(ctx context.Context, req LokiQueryRequest) (*LogQueryResult, error) {
	if err := validateHTTPSource("loki", a.cfg.Loki); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("loki query is required")
	}
	limit := boundedLimit(req.Limit, a.cfg.Loki.DefaultLimit, 1000, a.cfg.Loki.MaxEntries)
	values := url.Values{}
	values.Set("query", req.Query)
	values.Set("limit", strconv.Itoa(limit))
	if !req.End.IsZero() {
		values.Set("time", strconv.FormatInt(req.End.UnixNano(), 10))
	}
	if req.Direction != "" {
		values.Set("direction", req.Direction)
	}
	return a.queryLoki(ctx, "/loki/api/v1/query", values, req.Query)
}

func (a *AnalyzerImpl) queryLoki(ctx context.Context, path string, values url.Values, query string) (*LogQueryResult, error) {
	endpoint, err := url.JoinPath(strings.TrimRight(a.cfg.Loki.URL, "/"), path)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, a.lokiHTTP, endpoint+"?"+values.Encode(), a.cfg.Loki, "loki")
	if err != nil {
		return nil, err
	}
	entries, err := parseLokiLogs(body)
	if err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("loki-logs", body)
	if err != nil {
		return nil, fmt.Errorf("write loki artifact: %w", err)
	}
	return &LogQueryResult{
		Entries:    nil,
		TotalLine:  len(entries),
		Source:     a.cfg.Loki.URL,
		Query:      query,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Sample:     limitLogs(entries, 20),
		Summary:    fmt.Sprintf("loki returned %d log entries", len(entries)),
	}, nil
}

func (a *AnalyzerImpl) QueryLokiLabels(ctx context.Context, req LokiLabelsRequest) (*LokiLabelsResult, error) {
	if err := validateHTTPSource("loki", a.cfg.Loki); err != nil {
		return nil, err
	}
	path := "/loki/api/v1/labels"
	if req.Name != "" {
		path = "/loki/api/v1/label/" + url.PathEscape(req.Name) + "/values"
	}
	endpoint, err := url.JoinPath(strings.TrimRight(a.cfg.Loki.URL, "/"), path)
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	addLokiMatchers(values, req.Matcher, req.Query)
	if !req.Start.IsZero() {
		values.Set("start", strconv.FormatInt(req.Start.UnixNano(), 10))
	}
	if !req.End.IsZero() {
		values.Set("end", strconv.FormatInt(req.End.UnixNano(), 10))
	}
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	body, err := a.get(ctx, a.lokiHTTP, endpoint, a.cfg.Loki, "loki")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	sort.Strings(parsed.Data)
	return &LokiLabelsResult{Labels: parsed.Data, Source: a.cfg.Loki.URL}, nil
}

func (a *AnalyzerImpl) QueryLokiSeries(ctx context.Context, req LokiSeriesRequest) (*LokiSeriesResult, error) {
	if err := validateHTTPSource("loki", a.cfg.Loki); err != nil {
		return nil, err
	}
	endpoint, err := url.JoinPath(strings.TrimRight(a.cfg.Loki.URL, "/"), "/loki/api/v1/series")
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	addLokiMatchers(values, req.Matcher, req.Query)
	if !req.Start.IsZero() {
		values.Set("start", strconv.FormatInt(req.Start.UnixNano(), 10))
	}
	if !req.End.IsZero() {
		values.Set("end", strconv.FormatInt(req.End.UnixNano(), 10))
	}
	body, err := a.get(ctx, a.lokiHTTP, endpoint+"?"+values.Encode(), a.cfg.Loki, "loki")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return &LokiSeriesResult{Series: parsed.Data, Source: a.cfg.Loki.URL}, nil
}

func (a *AnalyzerImpl) QueryPrometheusInstant(ctx context.Context, req PrometheusInstantRequest) (*MetricQueryResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("prometheus query is required")
	}
	if err := a.ensurePrometheus(); err != nil {
		return nil, err
	}
	queryTime := time.Now()
	if !req.Time.IsZero() {
		queryTime = req.Time
	}
	value, warnings, err := a.promAPI.Query(ctx, req.Query, queryTime, promv1.WithTimeout(time.Duration(a.cfg.Prometheus.QueryTimeout)*time.Second))
	if err != nil {
		return nil, err
	}
	return a.prometheusMetricResult(req.Query, value, warnings)
}

func (a *AnalyzerImpl) QueryPrometheusRange(ctx context.Context, req PrometheusRangeRequest) (*MetricQueryResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("prometheus query is required")
	}
	if req.Start.IsZero() || req.End.IsZero() {
		return nil, errors.New("prometheus range start and end are required")
	}
	if req.Step == "" {
		req.Step = "60s"
	}
	if err := a.ensurePrometheus(); err != nil {
		return nil, err
	}
	step, err := time.ParseDuration(req.Step)
	if err != nil {
		return nil, fmt.Errorf("invalid prometheus range step: %w", err)
	}
	value, warnings, err := a.promAPI.QueryRange(ctx, req.Query, promv1.Range{Start: req.Start, End: req.End, Step: step}, promv1.WithTimeout(time.Duration(a.cfg.Prometheus.QueryTimeout)*time.Second))
	if err != nil {
		return nil, err
	}
	return a.prometheusMetricResult(req.Query, value, warnings)
}

func (a *AnalyzerImpl) ListPrometheusAlerts(ctx context.Context) (*PrometheusAlertsResult, error) {
	if err := a.ensurePrometheus(); err != nil {
		return nil, err
	}
	raw, err := a.promAPI.Alerts(ctx)
	if err != nil {
		return nil, err
	}
	alerts := []PrometheusAlert{}
	if err := convertViaJSON(raw.Alerts, &alerts); err != nil {
		return nil, err
	}
	for i := range alerts {
		if alerts[i].Name == "" {
			alerts[i].Name = alerts[i].Labels["alertname"]
		}
	}
	artifactID, artifactPath, err := a.artifacts.WriteJSON("prometheus-alerts", raw)
	if err != nil {
		return nil, fmt.Errorf("write prometheus alerts artifact: %w", err)
	}
	return &PrometheusAlertsResult{
		Alerts:     alerts,
		Source:     a.cfg.Prometheus.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("prometheus returned %d alerts", len(alerts)),
	}, nil
}

func (a *AnalyzerImpl) ListPrometheusRules(ctx context.Context) (*PrometheusRulesResult, error) {
	if err := a.ensurePrometheus(); err != nil {
		return nil, err
	}
	raw, err := a.promAPI.Rules(ctx)
	if err != nil {
		return nil, err
	}
	groups := []map[string]any{}
	if err := convertViaJSON(raw.Groups, &groups); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteJSON("prometheus-rules", raw)
	if err != nil {
		return nil, fmt.Errorf("write prometheus rules artifact: %w", err)
	}
	return &PrometheusRulesResult{
		Groups:     groups,
		Source:     a.cfg.Prometheus.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("prometheus returned %d rule groups", len(groups)),
	}, nil
}

func (a *AnalyzerImpl) ListPrometheusTargets(ctx context.Context) (*PrometheusTargetsResult, error) {
	if err := a.ensurePrometheus(); err != nil {
		return nil, err
	}
	raw, err := a.promAPI.Targets(ctx)
	if err != nil {
		return nil, err
	}
	activeTargets := []map[string]any{}
	droppedTargets := []map[string]any{}
	if err := convertViaJSON(raw.Active, &activeTargets); err != nil {
		return nil, err
	}
	if err := convertViaJSON(raw.Dropped, &droppedTargets); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteJSON("prometheus-targets", raw)
	if err != nil {
		return nil, fmt.Errorf("write prometheus targets artifact: %w", err)
	}
	return &PrometheusTargetsResult{
		ActiveTargets:  activeTargets,
		DroppedTargets: droppedTargets,
		Source:         a.cfg.Prometheus.URL,
		ArtifactID:     artifactID,
		Artifact:       artifactPath,
		Summary:        fmt.Sprintf("prometheus returned %d active targets and %d dropped targets", len(activeTargets), len(droppedTargets)),
	}, nil
}

func (a *AnalyzerImpl) ListGrafanaDatasources(ctx context.Context) (*GrafanaDatasourcesResult, error) {
	if err := a.ensureGrafana(); err != nil {
		return nil, err
	}
	params := grafdatasources.NewGetDataSourcesParamsWithContext(ctx).WithHTTPClient(a.grafHTTP)
	raw, err := a.grafAPI.Datasources.GetDataSourcesWithParams(params)
	if err != nil {
		return nil, fmt.Errorf("grafana list datasources failed: %w", err)
	}
	body, err := rawJSON(raw.GetPayload())
	if err != nil {
		return nil, err
	}
	var datasources []GrafanaDatasource
	if err := json.Unmarshal(body, &datasources); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("grafana-datasources", body)
	if err != nil {
		return nil, fmt.Errorf("write grafana datasources artifact: %w", err)
	}
	return &GrafanaDatasourcesResult{
		Datasources: datasources,
		Source:      a.cfg.Grafana.URL,
		ArtifactID:  artifactID,
		Artifact:    artifactPath,
		Summary:     fmt.Sprintf("grafana returned %d datasources", len(datasources)),
	}, nil
}

func (a *AnalyzerImpl) QueryGrafanaDatasource(ctx context.Context, req GrafanaDatasourceQueryRequest) (*GrafanaDatasourceQueryResult, error) {
	if strings.TrimSpace(req.DatasourceUID) == "" {
		return nil, errors.New("grafana datasource_uid is required")
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/api/v1/query"
	}
	if strings.Contains(path, "://") || strings.Contains(path, "..") {
		return nil, errors.New("grafana datasource proxy path is invalid")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	values := url.Values{}
	for key, value := range req.Params {
		if strings.TrimSpace(key) != "" {
			values.Set(key, value)
		}
	}
	if req.Query != "" {
		values.Set("query", req.Query)
	}
	if !req.Start.IsZero() {
		values.Set("start", req.Start.Format(time.RFC3339))
	}
	if !req.End.IsZero() {
		values.Set("end", req.End.Format(time.RFC3339))
	}
	if !req.Time.IsZero() {
		values.Set("time", req.Time.Format(time.RFC3339))
	}
	if req.Step != "" {
		values.Set("step", req.Step)
	}
	if req.Limit > 0 {
		values.Set("limit", strconv.Itoa(req.Limit))
	}
	basePath := "/api/datasources/proxy/uid/" + url.PathEscape(req.DatasourceUID)
	body, err := a.getGrafanaAPI(ctx, basePath+path, values)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	_ = json.Unmarshal(body, &result)
	artifactID, artifactPath, err := a.artifacts.WriteRaw("grafana-datasource-query", body)
	if err != nil {
		return nil, fmt.Errorf("write grafana datasource query artifact: %w", err)
	}
	return &GrafanaDatasourceQueryResult{
		DatasourceUID: req.DatasourceUID,
		Path:          path,
		Source:        a.cfg.Grafana.URL,
		Result:        compactGrafanaResult(result),
		ArtifactID:    artifactID,
		Artifact:      artifactPath,
		Summary:       "grafana datasource proxy query completed",
	}, nil
}

func (a *AnalyzerImpl) SearchGrafanaDashboards(ctx context.Context, req GrafanaDashboardSearchRequest) (*GrafanaDashboardSearchResult, error) {
	if err := a.ensureGrafana(); err != nil {
		return nil, err
	}
	params := grafsearch.NewSearchParamsWithContext(ctx).WithHTTPClient(a.grafHTTP)
	if req.Query != "" {
		params = params.WithQuery(&req.Query)
	}
	if req.Type != "" {
		params = params.WithType(&req.Type)
	}
	limit := int64(50)
	if req.Limit > 0 {
		limit = int64(req.Limit)
	}
	params = params.WithLimit(&limit)
	raw, err := a.grafAPI.Search.Search(params)
	if err != nil {
		return nil, fmt.Errorf("grafana search dashboards failed: %w", err)
	}
	body, err := rawJSON(raw.GetPayload())
	if err != nil {
		return nil, err
	}
	var dashboards []map[string]any
	if err := json.Unmarshal(body, &dashboards); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("grafana-dashboard-search", body)
	if err != nil {
		return nil, fmt.Errorf("write grafana dashboard search artifact: %w", err)
	}
	return &GrafanaDashboardSearchResult{
		Dashboards: limitMapSlice(dashboards, 50),
		Source:     a.cfg.Grafana.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("grafana returned %d dashboards", len(dashboards)),
	}, nil
}

func (a *AnalyzerImpl) GetGrafanaDashboard(ctx context.Context, req GrafanaDashboardRequest) (*GrafanaDashboardResult, error) {
	if strings.TrimSpace(req.UID) == "" {
		return nil, errors.New("grafana dashboard uid is required")
	}
	if err := a.ensureGrafana(); err != nil {
		return nil, err
	}
	params := grafdashboards.NewGetDashboardByUIDParamsWithContext(ctx).WithHTTPClient(a.grafHTTP).WithUID(req.UID)
	rawResp, err := a.grafAPI.Dashboards.GetDashboardByUIDWithParams(params)
	if err != nil {
		return nil, fmt.Errorf("grafana get dashboard failed: %w", err)
	}
	body, err := rawJSON(rawResp.GetPayload())
	if err != nil {
		return nil, err
	}
	var raw struct {
		Dashboard map[string]any `json:"dashboard"`
		Meta      map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("grafana-dashboard", body)
	if err != nil {
		return nil, fmt.Errorf("write grafana dashboard artifact: %w", err)
	}
	title, _ := raw.Dashboard["title"].(string)
	return &GrafanaDashboardResult{
		UID:        req.UID,
		Title:      title,
		Dashboard:  nil,
		Meta:       raw.Meta,
		Source:     a.cfg.Grafana.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("grafana dashboard %q loaded", title),
	}, nil
}

func (a *AnalyzerImpl) ExtractGrafanaPanelQueries(ctx context.Context, req GrafanaPanelQueryRequest) (*GrafanaPanelQueryResult, error) {
	var body []byte
	uid := req.UID
	if req.ArtifactID != "" {
		data, err := a.artifacts.ReadRaw(req.ArtifactID)
		if err != nil {
			return nil, err
		}
		body = data
	} else {
		if strings.TrimSpace(req.UID) == "" {
			return nil, errors.New("grafana dashboard uid or artifact_id is required")
		}
		dashboard, err := a.GetGrafanaDashboard(ctx, GrafanaDashboardRequest{UID: req.UID})
		if err != nil {
			return nil, err
		}
		data, err := a.artifacts.ReadRaw(dashboard.ArtifactID)
		if err != nil {
			return nil, err
		}
		body = data
	}
	var raw struct {
		Dashboard map[string]any `json:"dashboard"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	queries := extractPanelQueries(raw.Dashboard)
	return &GrafanaPanelQueryResult{
		UID:     uid,
		Queries: queries,
		Source:  a.cfg.Grafana.URL,
		Summary: fmt.Sprintf("extracted %d grafana panel queries", len(queries)),
	}, nil
}

func (a *AnalyzerImpl) ListGrafanaAlertRules(ctx context.Context) (*GrafanaAlertRulesResult, error) {
	if err := a.ensureGrafana(); err != nil {
		return nil, err
	}
	params := grafprovisioning.NewGetAlertRulesParamsWithContext(ctx).WithHTTPClient(a.grafHTTP)
	raw, err := a.grafAPI.Provisioning.GetAlertRulesWithParams(params)
	if err != nil {
		return nil, fmt.Errorf("grafana list alert rules failed: %w", err)
	}
	body, err := rawJSON(raw.GetPayload())
	if err != nil {
		return nil, err
	}
	var rules []map[string]any
	if err := json.Unmarshal(body, &rules); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("grafana-alert-rules", body)
	if err != nil {
		return nil, fmt.Errorf("write grafana alert rules artifact: %w", err)
	}
	return &GrafanaAlertRulesResult{
		Rules:      limitMapSlice(rules, 50),
		Source:     a.cfg.Grafana.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("grafana returned %d alert rules", len(rules)),
	}, nil
}

func (a *AnalyzerImpl) ListOpenSearchIndices(ctx context.Context) (*OpenSearchIndicesResult, error) {
	body, err := a.getOpenSearchAPI(ctx, "/_cat/indices", url.Values{"format": []string{"json"}}, nil)
	if err != nil {
		return nil, err
	}
	var indices []map[string]any
	if err := json.Unmarshal(body, &indices); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("opensearch-indices", body)
	if err != nil {
		return nil, fmt.Errorf("write opensearch indices artifact: %w", err)
	}
	return &OpenSearchIndicesResult{
		Indices:    limitMapSlice(indices, 100),
		Source:     a.cfg.OpenSearch.URL,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("opensearch returned %d indices", len(indices)),
	}, nil
}

func (a *AnalyzerImpl) GetOpenSearchMapping(ctx context.Context, req OpenSearchMappingRequest) (*OpenSearchMappingResult, error) {
	index, err := a.openSearchIndex(req.Index)
	if err != nil {
		return nil, err
	}
	body, err := a.getOpenSearchAPI(ctx, "/"+url.PathEscape(index)+"/_mapping", nil, nil)
	if err != nil {
		return nil, err
	}
	var mapping map[string]any
	if err := json.Unmarshal(body, &mapping); err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("opensearch-mapping", body)
	if err != nil {
		return nil, fmt.Errorf("write opensearch mapping artifact: %w", err)
	}
	fields := extractOpenSearchFields(mapping)
	return &OpenSearchMappingResult{
		Index:      index,
		Source:     a.cfg.OpenSearch.URL,
		Fields:     fields,
		Mapping:    nil,
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("opensearch mapping for %q returned %d fields", index, len(fields)),
	}, nil
}

func (a *AnalyzerImpl) QueryOpenSearch(ctx context.Context, req OpenSearchQueryRequest) (*OpenSearchQueryResult, error) {
	index, err := a.openSearchIndex(req.Index)
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(req.Limit, a.cfg.OpenSearch.DefaultLimit, 100, a.cfg.OpenSearch.MaxEntries)
	body, err := json.Marshal(openSearchQueryBody(req, limit))
	if err != nil {
		return nil, err
	}
	raw, err := a.getOpenSearchAPI(ctx, "/"+url.PathEscape(index)+"/_search", nil, body)
	if err != nil {
		return nil, err
	}
	total, entries, err := parseOpenSearchLogs(raw)
	if err != nil {
		return nil, err
	}
	artifactID, artifactPath, err := a.artifacts.WriteRaw("opensearch-search", raw)
	if err != nil {
		return nil, fmt.Errorf("write opensearch search artifact: %w", err)
	}
	return &OpenSearchQueryResult{
		Index:      index,
		Source:     a.cfg.OpenSearch.URL,
		Total:      total,
		Sample:     limitLogs(entries, min(limit, 20)),
		ArtifactID: artifactID,
		Artifact:   artifactPath,
		Summary:    fmt.Sprintf("opensearch returned %d hits", total),
	}, nil
}

func (a *AnalyzerImpl) CheckSources(ctx context.Context) (*SourceCheckResult, error) {
	sources := []SourceStatus{
		a.checkFileSource(),
		a.checkHTTPSource(ctx, "loki", a.cfg.Loki, a.lokiHTTP, "/ready"),
		a.checkHTTPSource(ctx, "prometheus", a.cfg.Prometheus, a.promHTTP, "/-/ready"),
		a.checkHTTPSource(ctx, "grafana", a.cfg.Grafana, a.grafHTTP, "/api/health"),
		a.checkHTTPSource(ctx, "opensearch", a.cfg.OpenSearch, a.osHTTP, "/_cluster/health"),
	}
	return &SourceCheckResult{Sources: sources}, nil
}

func (a *AnalyzerImpl) AnalyzePattern(ctx context.Context, req AnalyzePatternRequest) (*AnalyzePatternResult, error) {
	logs := req.Logs
	if len(logs) == 0 && req.ArtifactID != "" {
		artifactLogs, err := a.artifacts.ReadLogEntries(req.ArtifactID, 5000)
		if err != nil {
			return nil, err
		}
		logs = artifactLogs
	}
	if len(logs) == 0 {
		return &AnalyzePatternResult{Patterns: []DetectedPattern{}, Severity: "info", Summary: "분석할 로그가 없습니다"}, nil
	}
	return a.detector.Detect(logs, req.PodName, req.Namespace), nil
}

func (a *AnalyzerImpl) AnalyzeMetricPattern(ctx context.Context, req AnalyzeMetricPatternRequest) (*AnalyzeMetricPatternResult, error) {
	result := req.Result
	if result == nil && req.ArtifactID != "" {
		loaded, err := a.artifacts.ReadMetricResult(req.ArtifactID)
		if err != nil {
			return nil, err
		}
		result = loaded
	}
	if result == nil {
		return &AnalyzeMetricPatternResult{Patterns: []MetricPattern{}, Severity: "info", Summary: "분석할 메트릭이 없습니다"}, nil
	}
	var patterns []MetricPattern
	for _, series := range result.Series {
		maxValue := 0.0
		for _, point := range series.Values {
			if point.Value > maxValue {
				maxValue = point.Value
			}
		}
		query := strings.ToLower(result.Query + " " + req.Query)
		switch {
		case strings.Contains(query, "restart") && maxValue > 0:
			patterns = append(patterns, MetricPattern{Type: "RestartIncrease", Description: "restart-related metric has non-zero values", Series: 1, MaxValue: maxValue})
		case strings.Contains(query, "error") || strings.Contains(query, "5xx"):
			if maxValue > 0 {
				patterns = append(patterns, MetricPattern{Type: "ErrorRate", Description: "error-related metric has non-zero values", Series: 1, MaxValue: maxValue})
			}
		case strings.Contains(query, "latency") || strings.Contains(query, "duration"):
			if maxValue > 1 {
				patterns = append(patterns, MetricPattern{Type: "Latency", Description: "latency-related metric exceeded one second", Series: 1, MaxValue: maxValue})
			}
		}
	}
	severity := "info"
	if len(patterns) > 0 {
		severity = "warning"
	}
	return &AnalyzeMetricPatternResult{
		Patterns: patterns,
		Severity: severity,
		Summary:  fmt.Sprintf("detected %d metric patterns", len(patterns)),
	}, nil
}

func (a *AnalyzerImpl) KeyEvidence(ctx context.Context, req KeyEvidenceRequest) (*KeyEvidenceResult, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	logs := req.Logs
	metrics := req.Metrics
	if len(logs) == 0 && req.ArtifactID != "" {
		loaded, err := a.artifacts.ReadLogEntries(req.ArtifactID, 5000)
		if err == nil && hasMeaningfulLogEntries(loaded) {
			logs = loaded
		}
		if len(logs) == 0 {
			if metricResult, err := a.artifacts.ReadMetricResult(req.ArtifactID); err == nil {
				metrics = metricResult.Series
			}
		}
	}
	items := make([]EvidenceItem, 0, limit)
	for _, log := range logs {
		if len(items) >= limit {
			break
		}
		level := strings.ToUpper(log.Level)
		msg := strings.ToLower(log.Message + " " + log.Raw)
		if level == "ERROR" || level == "FATAL" || strings.Contains(msg, "error") || strings.Contains(msg, "exception") || strings.Contains(msg, "failed") || strings.Contains(msg, "oom") {
			items = append(items, EvidenceItem{Source: "log", Timestamp: log.Timestamp, Severity: level, Message: firstNonEmpty(log.Message, log.Raw)})
		}
	}
	for _, p := range req.Patterns {
		if len(items) >= limit {
			break
		}
		items = append(items, EvidenceItem{Source: "pattern", Severity: string(p.Type), Message: p.Description})
	}
	for _, series := range metrics {
		if len(items) >= limit {
			break
		}
		maxPoint, ok := maxMetricPoint(series.Values)
		if !ok {
			continue
		}
		items = append(items, EvidenceItem{
			Source:    "metric",
			Timestamp: time.Unix(maxPoint.Timestamp, 0).Format(time.RFC3339),
			Severity:  "info",
			Message:   fmt.Sprintf("metric %s max=%s", formatMetricLabels(series.Metric), maxPoint.Raw),
		})
	}
	return &KeyEvidenceResult{Items: items, Summary: fmt.Sprintf("selected %d key evidence items", len(items))}, nil
}

func (a *AnalyzerImpl) SummarizeEvidence(ctx context.Context, req SummarizeEvidenceRequest) (*EvidenceSummaryResult, error) {
	signals := make([]string, 0, len(req.Patterns)+len(req.Items))
	signals = append(signals, req.Patterns...)
	for _, item := range req.Items {
		if item.Message != "" {
			signals = append(signals, item.Message)
		}
		if len(signals) >= 8 {
			break
		}
	}
	artifacts := []string{}
	if req.ArtifactID != "" {
		artifacts = append(artifacts, req.ArtifactID)
	}
	summary := "no evidence signals found"
	if len(signals) > 0 {
		summary = fmt.Sprintf("%d evidence signals found: %s", len(signals), strings.Join(signals[:min(len(signals), 3)], "; "))
	}
	return &EvidenceSummaryResult{Summary: summary, Signals: signals, Artifacts: artifacts}, nil
}

func (a *AnalyzerImpl) GetArtifactSample(ctx context.Context, req ArtifactSampleRequest) (*ArtifactSampleResult, error) {
	return a.artifacts.Sample(req.ArtifactID, req.MaxLines)
}

func (a *AnalyzerImpl) CleanArtifacts(ctx context.Context, req CleanArtifactsRequest) (*CleanArtifactsResult, error) {
	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = int64(a.cfg.ArtifactTTL)
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = a.cfg.MaxArtifactBytes
	}
	return a.artifacts.Clean(time.Duration(ttl)*time.Second, maxBytes)
}

func (a *AnalyzerImpl) prometheusMetricResult(query string, value model.Value, warnings promv1.Warnings) (*MetricQueryResult, error) {
	series := metricSeriesFromPrometheusValue(value)
	result := &MetricQueryResult{
		Query:      query,
		Source:     a.cfg.Prometheus.URL,
		ResultType: prometheusValueType(value),
		Series:     series,
		Warnings:   []string(warnings),
		Summary:    fmt.Sprintf("prometheus returned %d series", len(series)),
	}
	artifactID, artifactPath, err := a.artifacts.WriteJSON("prometheus-metric", result)
	if err != nil {
		return nil, fmt.Errorf("write prometheus metric artifact: %w", err)
	}
	result.ArtifactID = artifactID
	result.Artifact = artifactPath
	return result, nil
}

func (a *AnalyzerImpl) ensurePrometheus() error {
	if err := validateHTTPSource("prometheus", a.cfg.Prometheus); err != nil {
		return err
	}
	if a.promAPI == nil {
		return errors.New("prometheus client is not configured")
	}
	return nil
}

func (a *AnalyzerImpl) ensureGrafana() error {
	if err := validateHTTPSource("grafana", a.cfg.Grafana); err != nil {
		return err
	}
	if a.grafAPI == nil {
		return errors.New("grafana client is not configured")
	}
	return nil
}

func rawJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func convertViaJSON(value any, out any) error {
	data, err := rawJSON(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (a *AnalyzerImpl) getGrafanaAPI(ctx context.Context, path string, values url.Values) ([]byte, error) {
	if err := validateHTTPSource("grafana", a.cfg.Grafana); err != nil {
		return nil, err
	}
	endpoint, err := url.JoinPath(strings.TrimRight(a.cfg.Grafana.URL, "/"), path)
	if err != nil {
		return nil, err
	}
	if values != nil && values.Encode() != "" {
		endpoint += "?" + values.Encode()
	}
	return a.get(ctx, a.grafHTTP, endpoint, a.cfg.Grafana, "grafana")
}

func (a *AnalyzerImpl) getOpenSearchAPI(ctx context.Context, path string, values url.Values, body []byte) ([]byte, error) {
	if err := validateHTTPSource("opensearch", a.cfg.OpenSearch); err != nil {
		return nil, err
	}
	if a.osClient == nil {
		return nil, errors.New("opensearch client is not configured")
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, reader)
	if err != nil {
		return nil, err
	}
	if values != nil {
		req.URL.RawQuery = values.Encode()
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.osClient.Perform(req)
	if err != nil {
		return nil, fmt.Errorf("opensearch request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("opensearch request failed with status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (a *AnalyzerImpl) get(ctx context.Context, client *http.Client, rawURL string, auth config.LogAnalyzerHTTPConfig, source string) ([]byte, error) {
	return a.getWithBody(ctx, client, rawURL, nil, auth, source)
}

func (a *AnalyzerImpl) getWithBody(ctx context.Context, client *http.Client, rawURL string, body []byte, auth config.LogAnalyzerHTTPConfig, source string) ([]byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, reader)
	if err != nil {
		return nil, err
	}
	applyHTTPHeaders(req, auth, source)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", source, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s request failed with status %d: %s", source, resp.StatusCode, string(data))
	}
	return data, nil
}

func applyHTTPHeaders(req *http.Request, auth config.LogAnalyzerHTTPConfig, source string) {
	for key, value := range auth.Headers {
		req.Header.Set(key, value)
	}
	if auth.Token != "" {
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	}
	if auth.Username != "" || auth.Password != "" {
		req.SetBasicAuth(auth.Username, auth.Password)
	}
	if auth.OrgID != "" {
		if source == "grafana" {
			req.Header.Set("X-Grafana-Org-Id", auth.OrgID)
		} else {
			req.Header.Set("X-Scope-OrgID", auth.OrgID)
		}
	}
}

func newSourceHTTPClient(cfg config.LogAnalyzerHTTPConfig) *http.Client {
	timeout := cfg.QueryTimeout
	if timeout <= 0 {
		timeout = cfg.Timeout
	}
	if timeout <= 0 {
		timeout = 30
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig := tlsConfigFromSource(cfg); tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: transport}
}

func newPrometheusAPI(cfg config.LogAnalyzerHTTPConfig) promv1.API {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil
	}
	client, err := promapi.NewClient(promapi.Config{
		Address:      strings.TrimRight(cfg.URL, "/"),
		RoundTripper: sourceRoundTripper{base: newSourceTransport(cfg), auth: cfg, source: "prometheus"},
	})
	if err != nil {
		return nil
	}
	return promv1.NewAPI(client)
}

func newGrafanaAPI(cfg config.LogAnalyzerHTTPConfig) *grafclient.GrafanaHTTPAPI {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	if basePath == "" {
		basePath = grafclient.DefaultBasePath
	} else {
		basePath += grafclient.DefaultBasePath
	}
	headers := map[string]string{}
	for key, value := range cfg.Headers {
		headers[key] = value
	}
	if cfg.OrgID != "" {
		headers[grafclient.OrgIDHeader] = cfg.OrgID
	}
	transportCfg := &grafclient.TransportConfig{
		Host:        parsed.Host,
		BasePath:    basePath,
		Schemes:     []string{scheme},
		APIKey:      cfg.Token,
		HTTPHeaders: headers,
		TLSConfig:   tlsConfigFromSource(cfg),
		Client:      newSourceHTTPClient(cfg),
	}
	if cfg.OrgID != "" {
		if orgID, err := strconv.ParseInt(cfg.OrgID, 10, 64); err == nil {
			transportCfg.OrgID = orgID
		}
	}
	if cfg.Username != "" || cfg.Password != "" {
		transportCfg.BasicAuth = url.UserPassword(cfg.Username, cfg.Password)
	}
	return grafclient.NewHTTPClientWithConfig(nil, transportCfg)
}

func newOpenSearchClient(cfg config.LogAnalyzerHTTPConfig) *opensearch.Client {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil
	}
	client, err := opensearch.NewClient(opensearch.Config{
		Addresses: []string{strings.TrimRight(cfg.URL, "/")},
		Username:  cfg.Username,
		Password:  cfg.Password,
		Transport: sourceRoundTripper{base: newSourceTransport(cfg), auth: cfg, source: "opensearch"},
	})
	if err != nil {
		return nil
	}
	return client
}

func newSourceTransport(cfg config.LogAnalyzerHTTPConfig) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig := tlsConfigFromSource(cfg); tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return transport
}

func tlsConfigFromSource(cfg config.LogAnalyzerHTTPConfig) *tls.Config {
	if !cfg.TLSSkipVerify && cfg.CAFile == "" {
		return nil
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify} //nolint:gosec
	if cfg.CAFile != "" {
		if data, err := os.ReadFile(cfg.CAFile); err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(data) {
				tlsConfig.RootCAs = pool
			}
		}
	}
	return tlsConfig
}

type sourceRoundTripper struct {
	base   http.RoundTripper
	auth   config.LogAnalyzerHTTPConfig
	source string
}

func (t sourceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	applyHTTPHeaders(clone, t.auth, t.source)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func validateHTTPSource(name string, cfg config.LogAnalyzerHTTPConfig) error {
	if !cfg.Enabled {
		return fmt.Errorf("%s source is disabled", name)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("%s url is not configured", name)
	}
	if cfg.CAFile != "" {
		if _, err := os.Stat(cfg.CAFile); err != nil {
			return fmt.Errorf("%s ca_file is invalid: %w", name, err)
		}
	}
	return nil
}

func lokiQueryValues(req LokiQueryRequest, limit int) url.Values {
	values := url.Values{}
	values.Set("query", req.Query)
	values.Set("limit", strconv.Itoa(limit))
	if req.Start.IsZero() {
		req.Start = time.Now().Add(-1 * time.Hour)
	}
	if req.End.IsZero() {
		req.End = time.Now()
	}
	values.Set("start", strconv.FormatInt(req.Start.UnixNano(), 10))
	values.Set("end", strconv.FormatInt(req.End.UnixNano(), 10))
	if req.Step != "" {
		values.Set("step", req.Step)
	}
	if req.Direction != "" {
		values.Set("direction", req.Direction)
	}
	return values
}

func addLokiMatchers(values url.Values, matchers []string, query string) {
	for _, matcher := range matchers {
		matcher = strings.TrimSpace(matcher)
		if matcher != "" {
			values.Add("match[]", matcher)
		}
	}
	if strings.TrimSpace(query) != "" {
		values.Add("match[]", strings.TrimSpace(query))
	}
}

func boundedLimit(requested, configuredDefault, fallback, maxAllowed int) int {
	limit := requested
	if limit <= 0 {
		limit = configuredDefault
	}
	if limit <= 0 {
		limit = fallback
	}
	if maxAllowed > 0 && limit > maxAllowed {
		return maxAllowed
	}
	return limit
}

func (a *AnalyzerImpl) checkFileSource() SourceStatus {
	status := SourceStatus{Name: "file", Enabled: a.cfg.File.Enabled, Configured: a.cfg.File.RootDir != ""}
	if !status.Enabled {
		return status
	}
	if !status.Configured {
		status.Error = "file root_dir is not configured"
		return status
	}
	info, err := os.Stat(a.cfg.File.RootDir)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if !info.IsDir() {
		status.Error = "file root_dir is not a directory"
		return status
	}
	status.Reachable = true
	return status
}

func (a *AnalyzerImpl) checkHTTPSource(ctx context.Context, name string, cfg config.LogAnalyzerHTTPConfig, client *http.Client, readyPath string) SourceStatus {
	status := SourceStatus{Name: name, Enabled: cfg.Enabled, Configured: strings.TrimSpace(cfg.URL) != ""}
	if !status.Enabled || !status.Configured {
		if status.Enabled && !status.Configured {
			status.Error = name + " url is not configured"
		}
		return status
	}
	endpoint, err := url.JoinPath(strings.TrimRight(cfg.URL, "/"), readyPath)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	applyHTTPHeaders(req, cfg, name)
	resp, err := client.Do(req)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	status.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 500
	if !status.Reachable {
		status.Error = fmt.Sprintf("status %d", resp.StatusCode)
	}
	return status
}

func (a *AnalyzerImpl) matchLogFiles(req FetchLogsRequest) ([]string, error) {
	root := a.cfg.File.RootDir
	if root == "" && strings.TrimSpace(req.FilePath) == "" {
		return nil, errors.New("file log root_dir is not configured")
	}
	if strings.TrimSpace(req.FilePath) != "" {
		path, err := a.resolveLogFilePath(req.FilePath)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}

	type candidate struct {
		path    string
		score   int
		modTime time.Time
	}
	candidates := []candidate{}
	parts := []string{req.Namespace, req.PodName, req.ContainerName}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isSupportedLogFile(path) {
			return nil
		}
		score, ok := scoreLogFile(path, root, parts)
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		candidates = append(candidates, candidate{path: path, score: score, modTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if !candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].modTime.After(candidates[j].modTime)
		}
		return candidates[i].path < candidates[j].path
	})
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.path)
	}
	return paths, nil
}

func (a *AnalyzerImpl) readLogFile(path string, req FetchLogsRequest, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	cutoff := time.Time{}
	if req.SinceSeconds > 0 {
		cutoff = time.Now().Add(-time.Duration(req.SinceSeconds) * time.Second)
	}
	levelFilter := strings.ToUpper(strings.TrimSpace(req.Level))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	entries := make([]LogEntry, 0, limit)
	next := 0
	wrapped := false
	for scanner.Scan() {
		entry := a.parser.Parse(scanner.Text())
		if entry.Message == "" {
			entry.Message = entry.Raw
		}
		if levelFilter != "" && strings.ToUpper(entry.Level) != levelFilter {
			continue
		}
		if !cutoff.IsZero() && entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil && ts.Before(cutoff) {
				continue
			}
		}
		if len(entries) < limit {
			entries = append(entries, entry)
			continue
		}
		entries[next] = entry
		next = (next + 1) % limit
		wrapped = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !wrapped {
		return entries, nil
	}
	ordered := make([]LogEntry, 0, len(entries))
	ordered = append(ordered, entries[next:]...)
	ordered = append(ordered, entries[:next]...)
	return ordered, nil
}

func (a *AnalyzerImpl) resolveLogFilePath(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", errors.New("file_path is empty")
	}
	path := rawPath
	if !filepath.IsAbs(path) {
		if a.cfg.File.RootDir == "" {
			return "", errors.New("relative file_path requires file log root_dir")
		}
		path = filepath.Join(a.cfg.File.RootDir, path)
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(rawPath) && !isPathWithin(path, a.cfg.File.RootDir) {
		return "", fmt.Errorf("file_path %q is outside file log root_dir", rawPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("file_path %q is a directory", rawPath)
	}
	return path, nil
}

func isPathWithin(path, root string) bool {
	if root == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func isSupportedLogFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl")
}

func scoreLogFile(path, root string, parts []string) (int, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	lowerPath := strings.ToLower(rel)
	segments := strings.FieldsFunc(lowerPath, func(r rune) bool {
		return r == filepath.Separator || r == '/' || r == '\\' || r == '_' || r == '-'
	})
	score := 0
	for idx, raw := range parts {
		part := strings.ToLower(strings.TrimSpace(raw))
		if part == "" {
			continue
		}
		matched := false
		for _, segment := range segments {
			if segment == part {
				score += 100 - idx
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if strings.Contains(lowerPath, part) {
			score += 10 - idx
			continue
		}
		return 0, false
	}
	return score, true
}

func extractPanelQueries(dashboard map[string]any) []GrafanaPanelQuery {
	queries := []GrafanaPanelQuery{}
	visitPanels(dashboard["panels"], &queries)
	return queries
}

func visitPanels(value any, queries *[]GrafanaPanelQuery) {
	panels, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range panels {
		panel, ok := item.(map[string]any)
		if !ok {
			continue
		}
		panelID := intFromAny(panel["id"])
		title, _ := panel["title"].(string)
		datasource := mapFromAny(panel["datasource"])
		targets, _ := panel["targets"].([]any)
		for _, rawTarget := range targets {
			target, ok := rawTarget.(map[string]any)
			if !ok {
				continue
			}
			refID, _ := target["refId"].(string)
			expression := firstTargetExpression(target)
			*queries = append(*queries, GrafanaPanelQuery{
				PanelID:    panelID,
				PanelTitle: title,
				Datasource: datasource,
				RefID:      refID,
				Expression: expression,
				Raw:        target,
			})
		}
		visitPanels(panel["panels"], queries)
	}
}

func firstTargetExpression(target map[string]any) string {
	for _, key := range []string{"expr", "query", "rawSql", "rawQuery", "logql", "promql"} {
		if value, ok := target[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	if model, ok := target["model"].(map[string]any); ok {
		return firstTargetExpression(model)
	}
	return ""
}

func mapFromAny(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	if text, ok := value.(string); ok && text != "" {
		return map[string]any{"name": text}
	}
	return nil
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func compactGrafanaResult(result map[string]any) map[string]any {
	if len(result) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"status", "message", "error", "errorType"} {
		if value, ok := result[key]; ok {
			out[key] = value
		}
	}
	if data, ok := result["data"].(map[string]any); ok {
		compactData := map[string]any{}
		for _, key := range []string{"resultType", "stats"} {
			if value, ok := data[key]; ok {
				compactData[key] = value
			}
		}
		if values, ok := data["result"].([]any); ok {
			compactData["result_count"] = len(values)
			compactData["sample"] = values[:min(len(values), 5)]
		}
		if len(compactData) > 0 {
			out["data"] = compactData
		}
	}
	if len(out) == 0 {
		out["keys"] = mapKeys(result)
	}
	return out
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func limitMapSlice(values []map[string]any, limit int) []map[string]any {
	if limit > 0 && len(values) > limit {
		return values[:limit]
	}
	return values
}

func (a *AnalyzerImpl) openSearchIndex(index string) (string, error) {
	index = strings.TrimSpace(index)
	if index == "" {
		index = strings.TrimSpace(a.cfg.OpenSearch.DefaultIndex)
	}
	if index == "" {
		return "", errors.New("opensearch index is required")
	}
	if strings.Contains(index, "/") || strings.Contains(index, "\\") || strings.Contains(index, "..") {
		return "", errors.New("opensearch index is invalid")
	}
	return index, nil
}

func openSearchQueryBody(req OpenSearchQueryRequest, limit int) map[string]any {
	must := []map[string]any{}
	filter := []map[string]any{}
	if strings.TrimSpace(req.QueryString) != "" {
		must = append(must, map[string]any{"query_string": map[string]any{"query": req.QueryString}})
	}
	if strings.TrimSpace(req.Message) != "" {
		must = append(must, map[string]any{"multi_match": map[string]any{
			"query":  req.Message,
			"fields": []string{"message", "log", "body", "msg"},
		}})
	}
	if req.Namespace != "" {
		filter = append(filter, openSearchAnyFieldFilter([]string{"namespace", "kubernetes.namespace_name", "kubernetes.namespace", "kubernetes.namespace_name.keyword"}, req.Namespace))
	}
	if req.PodName != "" {
		filter = append(filter, openSearchAnyFieldFilter([]string{"pod", "pod_name", "kubernetes.pod_name", "kubernetes.pod.name", "kubernetes.pod_name.keyword"}, req.PodName))
	}
	if req.ContainerName != "" {
		filter = append(filter, openSearchAnyFieldFilter([]string{"container", "container_name", "kubernetes.container_name", "kubernetes.container.name", "kubernetes.container_name.keyword"}, req.ContainerName))
	}
	if req.Level != "" {
		filter = append(filter, openSearchAnyFieldFilter([]string{"level", "log.level", "severity", "level.keyword", "log.level.keyword"}, req.Level))
	}
	if !req.Start.IsZero() || !req.End.IsZero() {
		rangeQuery := map[string]any{}
		if !req.Start.IsZero() {
			rangeQuery["gte"] = req.Start.Format(time.RFC3339)
		}
		if !req.End.IsZero() {
			rangeQuery["lte"] = req.End.Format(time.RFC3339)
		}
		filter = append(filter, map[string]any{"range": map[string]any{"@timestamp": rangeQuery}})
	}
	query := map[string]any{"match_all": map[string]any{}}
	if len(must) > 0 || len(filter) > 0 {
		boolQuery := map[string]any{}
		if len(must) > 0 {
			boolQuery["must"] = must
		}
		if len(filter) > 0 {
			boolQuery["filter"] = filter
		}
		query = map[string]any{"bool": boolQuery}
	}
	return map[string]any{
		"size":  limit,
		"query": query,
		"sort":  []map[string]any{{"@timestamp": map[string]any{"order": "desc", "unmapped_type": "date"}}},
	}
}

func openSearchAnyFieldFilter(fields []string, value string) map[string]any {
	should := []map[string]any{}
	for _, field := range fields {
		should = append(should, map[string]any{"term": map[string]any{field: value}})
	}
	return map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}}
}

func extractOpenSearchFields(mapping map[string]any) []string {
	fields := map[string]bool{}
	for _, value := range mapping {
		indexMap, ok := value.(map[string]any)
		if !ok {
			continue
		}
		mappings, _ := indexMap["mappings"].(map[string]any)
		properties, _ := mappings["properties"].(map[string]any)
		collectOpenSearchFields("", properties, fields)
	}
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func collectOpenSearchFields(prefix string, properties map[string]any, fields map[string]bool) {
	for name, value := range properties {
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		fields[full] = true
		if valueMap, ok := value.(map[string]any); ok {
			if nested, ok := valueMap["properties"].(map[string]any); ok {
				collectOpenSearchFields(full, nested, fields)
			}
		}
	}
}

func parseLokiLogs(body []byte) ([]LogEntry, error) {
	var raw struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	entries := []LogEntry{}
	parser := NewJSONParser()
	for _, result := range raw.Data.Result {
		for _, value := range result.Values {
			if len(value) < 2 {
				continue
			}
			entry := parser.Parse(value[1])
			entry.Raw = value[1]
			if ts, err := strconv.ParseInt(value[0], 10, 64); err == nil {
				entry.Timestamp = time.Unix(0, ts).Format(time.RFC3339)
			}
			if entry.Message == "" {
				entry.Message = value[1]
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func parseOpenSearchLogs(body []byte) (int, []LogEntry, error) {
	var raw struct {
		Hits struct {
			Total any `json:"total"`
			Hits  []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, nil, err
	}
	total := openSearchTotal(raw.Hits.Total)
	entries := make([]LogEntry, 0, len(raw.Hits.Hits))
	for _, hit := range raw.Hits.Hits {
		if len(hit.Source) == 0 {
			continue
		}
		entry := logEntryFromOpenSearchSource(hit.Source)
		if entry.Raw != "" || entry.Message != "" {
			entries = append(entries, entry)
		}
	}
	return total, entries, nil
}

func openSearchTotal(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case map[string]any:
		if f, ok := v["value"].(float64); ok {
			return int(f)
		}
	}
	return 0
}

func logEntryFromOpenSearchSource(source map[string]any) LogEntry {
	raw, _ := json.Marshal(source)
	return LogEntry{
		Timestamp: firstStringValue(source, "@timestamp", "timestamp", "time", "ts", "event.created"),
		Level:     firstStringValue(source, "level", "log.level", "severity", "loglevel"),
		Message:   firstStringValue(source, "message", "msg", "log", "body"),
		Raw:       string(raw),
	}
}

func firstStringValue(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := nestedValue(source, key); value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func nestedValue(source map[string]any, path string) any {
	if value, ok := source[path]; ok {
		return value
	}
	current := any(source)
	for _, part := range strings.Split(path, ".") {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = asMap[part]
		if !ok {
			return nil
		}
	}
	return current
}

func parsePrometheusMetric(body []byte) (string, []MetricSeries, []string, []string, error) {
	var raw struct {
		Warnings []string `json:"warnings"`
		Infos    []string `json:"infos"`
		Data     struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, nil, nil, err
	}
	series := make([]MetricSeries, 0, len(raw.Data.Result))
	for _, result := range raw.Data.Result {
		s := MetricSeries{Metric: result.Metric}
		if len(result.Value) >= 2 {
			if point, ok := metricPoint(result.Value); ok {
				s.Values = append(s.Values, point)
			}
		}
		for _, value := range result.Values {
			if point, ok := metricPoint(value); ok {
				s.Values = append(s.Values, point)
			}
		}
		series = append(series, s)
	}
	return raw.Data.ResultType, series, raw.Warnings, raw.Infos, nil
}

func metricSeriesFromPrometheusValue(value model.Value) []MetricSeries {
	switch v := value.(type) {
	case model.Vector:
		series := make([]MetricSeries, 0, len(v))
		for _, sample := range v {
			series = append(series, MetricSeries{
				Metric: labelsFromPrometheusMetric(sample.Metric),
				Values: []MetricPoint{{
					Timestamp: sample.Timestamp.Time().Unix(),
					Value:     float64(sample.Value),
					Raw:       sample.Value.String(),
				}},
			})
		}
		return series
	case model.Matrix:
		series := make([]MetricSeries, 0, len(v))
		for _, stream := range v {
			points := make([]MetricPoint, 0, len(stream.Values))
			for _, point := range stream.Values {
				points = append(points, MetricPoint{
					Timestamp: point.Timestamp.Time().Unix(),
					Value:     float64(point.Value),
					Raw:       point.Value.String(),
				})
			}
			series = append(series, MetricSeries{Metric: labelsFromPrometheusMetric(stream.Metric), Values: points})
		}
		return series
	case *model.Scalar:
		return []MetricSeries{{
			Metric: map[string]string{"__name__": "scalar"},
			Values: []MetricPoint{{
				Timestamp: v.Timestamp.Time().Unix(),
				Value:     float64(v.Value),
				Raw:       v.Value.String(),
			}},
		}}
	case *model.String:
		return []MetricSeries{{
			Metric: map[string]string{"__name__": "string"},
			Values: []MetricPoint{{
				Timestamp: v.Timestamp.Time().Unix(),
				Raw:       v.Value,
			}},
		}}
	default:
		return nil
	}
}

func labelsFromPrometheusMetric(metric model.Metric) map[string]string {
	labels := map[string]string{}
	for key, value := range metric {
		labels[string(key)] = string(value)
	}
	return labels
}

func prometheusValueType(value model.Value) string {
	if value == nil {
		return ""
	}
	return string(value.Type())
}

func metricPoint(value []any) (MetricPoint, bool) {
	if len(value) < 2 {
		return MetricPoint{}, false
	}
	tsFloat, ok := value[0].(float64)
	if !ok {
		return MetricPoint{}, false
	}
	raw := fmt.Sprint(value[1])
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return MetricPoint{}, false
	}
	return MetricPoint{Timestamp: int64(tsFloat), Value: parsed, Raw: raw}, true
}

func lokiSelector(namespace, pod, container string) string {
	labels := []string{}
	if namespace != "" {
		labels = append(labels, fmt.Sprintf(`namespace="%s"`, escapeLabel(namespace)))
	}
	if pod != "" {
		labels = append(labels, fmt.Sprintf(`pod=~"%s"`, escapeLabel(pod)))
	}
	if container != "" {
		labels = append(labels, fmt.Sprintf(`container="%s"`, escapeLabel(container)))
	}
	if len(labels) == 0 {
		return `{job=~".+"}`
	}
	return "{" + strings.Join(labels, ",") + "}"
}

func escapeLabel(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hasMeaningfulLogEntries(entries []LogEntry) bool {
	for _, entry := range entries {
		if entry.Message != "" || entry.Level != "" || entry.Timestamp != "" {
			return true
		}
	}
	return false
}

func maxMetricPoint(points []MetricPoint) (MetricPoint, bool) {
	if len(points) == 0 {
		return MetricPoint{}, false
	}
	maxPoint := points[0]
	for _, point := range points[1:] {
		if point.Value > maxPoint.Value {
			maxPoint = point
		}
	}
	return maxPoint, true
}

func formatMetricLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
