// Package loganalyzer provides read-only log and metric evidence collection.
package loganalyzer

import (
	"context"
	"time"
)

type Analyzer interface {
	FetchLogs(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error)
	QueryLokiInstant(ctx context.Context, req LokiQueryRequest) (*LogQueryResult, error)
	QueryLoki(ctx context.Context, req LokiQueryRequest) (*LogQueryResult, error)
	QueryLokiLabels(ctx context.Context, req LokiLabelsRequest) (*LokiLabelsResult, error)
	QueryLokiSeries(ctx context.Context, req LokiSeriesRequest) (*LokiSeriesResult, error)
	QueryPrometheusInstant(ctx context.Context, req PrometheusInstantRequest) (*MetricQueryResult, error)
	QueryPrometheusRange(ctx context.Context, req PrometheusRangeRequest) (*MetricQueryResult, error)
	ListPrometheusAlerts(ctx context.Context) (*PrometheusAlertsResult, error)
	ListPrometheusRules(ctx context.Context) (*PrometheusRulesResult, error)
	ListPrometheusTargets(ctx context.Context) (*PrometheusTargetsResult, error)
	ListGrafanaDatasources(ctx context.Context) (*GrafanaDatasourcesResult, error)
	QueryGrafanaDatasource(ctx context.Context, req GrafanaDatasourceQueryRequest) (*GrafanaDatasourceQueryResult, error)
	SearchGrafanaDashboards(ctx context.Context, req GrafanaDashboardSearchRequest) (*GrafanaDashboardSearchResult, error)
	GetGrafanaDashboard(ctx context.Context, req GrafanaDashboardRequest) (*GrafanaDashboardResult, error)
	ExtractGrafanaPanelQueries(ctx context.Context, req GrafanaPanelQueryRequest) (*GrafanaPanelQueryResult, error)
	ListGrafanaAlertRules(ctx context.Context) (*GrafanaAlertRulesResult, error)
	ListOpenSearchIndices(ctx context.Context) (*OpenSearchIndicesResult, error)
	GetOpenSearchMapping(ctx context.Context, req OpenSearchMappingRequest) (*OpenSearchMappingResult, error)
	QueryOpenSearch(ctx context.Context, req OpenSearchQueryRequest) (*OpenSearchQueryResult, error)
	CheckSources(ctx context.Context) (*SourceCheckResult, error)
	AnalyzePattern(ctx context.Context, req AnalyzePatternRequest) (*AnalyzePatternResult, error)
	AnalyzeMetricPattern(ctx context.Context, req AnalyzeMetricPatternRequest) (*AnalyzeMetricPatternResult, error)
	KeyEvidence(ctx context.Context, req KeyEvidenceRequest) (*KeyEvidenceResult, error)
	SummarizeEvidence(ctx context.Context, req SummarizeEvidenceRequest) (*EvidenceSummaryResult, error)
	GetArtifactSample(ctx context.Context, req ArtifactSampleRequest) (*ArtifactSampleResult, error)
	CleanArtifacts(ctx context.Context, req CleanArtifactsRequest) (*CleanArtifactsResult, error)
}

type FetchLogsRequest struct {
	Source        string `json:"source,omitempty"`
	Index         string `json:"index,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	PodName       string `json:"pod_name,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	SinceSeconds  int64  `json:"since_seconds,omitempty"`
	MaxLines      int    `json:"max_lines,omitempty"`
	Level         string `json:"level,omitempty"`
}

type FetchLogsResult struct {
	Logs       []LogEntry `json:"logs,omitempty"`
	TotalLine  int        `json:"total_line"`
	Source     string     `json:"source"`
	ArtifactID string     `json:"artifact_id,omitempty"`
	Artifact   string     `json:"artifact,omitempty"`
	Sample     []LogEntry `json:"sample,omitempty"`
	Summary    string     `json:"summary,omitempty"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp,omitempty"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message,omitempty"`
	Raw       string `json:"raw,omitempty"`
}

type LokiQueryRequest struct {
	Query     string    `json:"query"`
	Start     time.Time `json:"start,omitempty"`
	End       time.Time `json:"end,omitempty"`
	Step      string    `json:"step,omitempty"`
	Limit     int       `json:"limit,omitempty"`
	Direction string    `json:"direction,omitempty"`
}

type LokiLabelsRequest struct {
	Name    string    `json:"name,omitempty"`
	Matcher []string  `json:"matcher,omitempty"`
	Query   string    `json:"query,omitempty"`
	Start   time.Time `json:"start,omitempty"`
	End     time.Time `json:"end,omitempty"`
}

type LokiLabelsResult struct {
	Labels []string `json:"labels"`
	Source string   `json:"source"`
}

type LogQueryResult struct {
	Entries    []LogEntry `json:"entries,omitempty"`
	TotalLine  int        `json:"total_line"`
	Source     string     `json:"source"`
	Query      string     `json:"query,omitempty"`
	ArtifactID string     `json:"artifact_id,omitempty"`
	Artifact   string     `json:"artifact,omitempty"`
	Sample     []LogEntry `json:"sample,omitempty"`
	Summary    string     `json:"summary,omitempty"`
}

type LokiSeriesRequest struct {
	Matcher []string  `json:"matcher,omitempty"`
	Query   string    `json:"query,omitempty"`
	Start   time.Time `json:"start,omitempty"`
	End     time.Time `json:"end,omitempty"`
}

type LokiSeriesResult struct {
	Series []map[string]string `json:"series"`
	Source string              `json:"source"`
}

type PrometheusInstantRequest struct {
	Query string    `json:"query"`
	Time  time.Time `json:"time,omitempty"`
}

type PrometheusRangeRequest struct {
	Query string    `json:"query"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Step  string    `json:"step"`
}

type MetricQueryResult struct {
	Query      string         `json:"query"`
	Source     string         `json:"source"`
	ResultType string         `json:"result_type,omitempty"`
	Series     []MetricSeries `json:"series,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Infos      []string       `json:"infos,omitempty"`
	ArtifactID string         `json:"artifact_id,omitempty"`
	Artifact   string         `json:"artifact,omitempty"`
	Summary    string         `json:"summary,omitempty"`
}

type MetricSeries struct {
	Metric map[string]string `json:"metric,omitempty"`
	Values []MetricPoint     `json:"values,omitempty"`
}

type MetricPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
	Raw       string  `json:"raw,omitempty"`
}

type PrometheusAlertsResult struct {
	Alerts     []PrometheusAlert `json:"alerts"`
	Source     string            `json:"source"`
	Warnings   []string          `json:"warnings,omitempty"`
	Infos      []string          `json:"infos,omitempty"`
	ArtifactID string            `json:"artifact_id,omitempty"`
	Artifact   string            `json:"artifact,omitempty"`
	Summary    string            `json:"summary,omitempty"`
}

type PrometheusAlert struct {
	State       string            `json:"state,omitempty"`
	Name        string            `json:"name,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	ActiveAt    string            `json:"activeAt,omitempty"`
	Value       string            `json:"value,omitempty"`
}

type PrometheusRulesResult struct {
	Groups     []map[string]any `json:"groups"`
	Source     string           `json:"source"`
	Warnings   []string         `json:"warnings,omitempty"`
	Infos      []string         `json:"infos,omitempty"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	Artifact   string           `json:"artifact,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

type PrometheusTargetsResult struct {
	ActiveTargets  []map[string]any `json:"active_targets"`
	DroppedTargets []map[string]any `json:"dropped_targets,omitempty"`
	Source         string           `json:"source"`
	Warnings       []string         `json:"warnings,omitempty"`
	Infos          []string         `json:"infos,omitempty"`
	ArtifactID     string           `json:"artifact_id,omitempty"`
	Artifact       string           `json:"artifact,omitempty"`
	Summary        string           `json:"summary,omitempty"`
}

type GrafanaDatasourcesResult struct {
	Datasources []GrafanaDatasource `json:"datasources"`
	Source      string              `json:"source"`
	ArtifactID  string              `json:"artifact_id,omitempty"`
	Artifact    string              `json:"artifact,omitempty"`
	Summary     string              `json:"summary,omitempty"`
}

type GrafanaDatasource struct {
	ID        int    `json:"id,omitempty"`
	UID       string `json:"uid,omitempty"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	URL       string `json:"url,omitempty"`
	Access    string `json:"access,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

type GrafanaDatasourceQueryRequest struct {
	DatasourceUID string            `json:"datasource_uid"`
	Path          string            `json:"path,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	Query         string            `json:"query,omitempty"`
	Start         time.Time         `json:"start,omitempty"`
	End           time.Time         `json:"end,omitempty"`
	Time          time.Time         `json:"time,omitempty"`
	Step          string            `json:"step,omitempty"`
	Limit         int               `json:"limit,omitempty"`
}

type GrafanaDatasourceQueryResult struct {
	DatasourceUID string         `json:"datasource_uid"`
	Path          string         `json:"path"`
	Source        string         `json:"source"`
	Result        map[string]any `json:"result,omitempty"`
	ArtifactID    string         `json:"artifact_id,omitempty"`
	Artifact      string         `json:"artifact,omitempty"`
	Summary       string         `json:"summary,omitempty"`
}

type GrafanaDashboardSearchRequest struct {
	Query string `json:"query,omitempty"`
	Type  string `json:"type,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type GrafanaDashboardSearchResult struct {
	Dashboards []map[string]any `json:"dashboards"`
	Source     string           `json:"source"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	Artifact   string           `json:"artifact,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

type GrafanaDashboardRequest struct {
	UID string `json:"uid"`
}

type GrafanaDashboardResult struct {
	UID        string         `json:"uid"`
	Title      string         `json:"title,omitempty"`
	Dashboard  map[string]any `json:"dashboard,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
	Source     string         `json:"source"`
	ArtifactID string         `json:"artifact_id,omitempty"`
	Artifact   string         `json:"artifact,omitempty"`
	Summary    string         `json:"summary,omitempty"`
}

type GrafanaPanelQueryRequest struct {
	UID        string `json:"uid,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

type GrafanaPanelQueryResult struct {
	UID     string              `json:"uid,omitempty"`
	Queries []GrafanaPanelQuery `json:"queries"`
	Source  string              `json:"source,omitempty"`
	Summary string              `json:"summary,omitempty"`
}

type GrafanaPanelQuery struct {
	PanelID    int            `json:"panel_id,omitempty"`
	PanelTitle string         `json:"panel_title,omitempty"`
	Datasource map[string]any `json:"datasource,omitempty"`
	RefID      string         `json:"ref_id,omitempty"`
	Expression string         `json:"expression,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

type GrafanaAlertRulesResult struct {
	Rules      []map[string]any `json:"rules"`
	Source     string           `json:"source"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	Artifact   string           `json:"artifact,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

type OpenSearchIndicesResult struct {
	Indices    []map[string]any `json:"indices"`
	Source     string           `json:"source"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	Artifact   string           `json:"artifact,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

type OpenSearchMappingRequest struct {
	Index string `json:"index,omitempty"`
}

type OpenSearchMappingResult struct {
	Index      string         `json:"index"`
	Source     string         `json:"source"`
	Fields     []string       `json:"fields,omitempty"`
	Mapping    map[string]any `json:"mapping,omitempty"`
	ArtifactID string         `json:"artifact_id,omitempty"`
	Artifact   string         `json:"artifact,omitempty"`
	Summary    string         `json:"summary,omitempty"`
}

type OpenSearchQueryRequest struct {
	Index         string    `json:"index,omitempty"`
	QueryString   string    `json:"query_string,omitempty"`
	Namespace     string    `json:"namespace,omitempty"`
	PodName       string    `json:"pod_name,omitempty"`
	ContainerName string    `json:"container_name,omitempty"`
	Level         string    `json:"level,omitempty"`
	Message       string    `json:"message,omitempty"`
	Start         time.Time `json:"start,omitempty"`
	End           time.Time `json:"end,omitempty"`
	Limit         int       `json:"limit,omitempty"`
}

type OpenSearchQueryResult struct {
	Index      string     `json:"index"`
	Source     string     `json:"source"`
	Total      int        `json:"total"`
	Sample     []LogEntry `json:"sample,omitempty"`
	ArtifactID string     `json:"artifact_id,omitempty"`
	Artifact   string     `json:"artifact,omitempty"`
	Summary    string     `json:"summary,omitempty"`
}

type SourceCheckResult struct {
	Sources []SourceStatus `json:"sources"`
}

type SourceStatus struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Error      string `json:"error,omitempty"`
}

type AnalyzePatternRequest struct {
	Logs       []LogEntry `json:"logs,omitempty"`
	ArtifactID string     `json:"artifact_id,omitempty"`
	PodName    string     `json:"pod_name,omitempty"`
	Namespace  string     `json:"namespace,omitempty"`
}

type AnalyzePatternResult struct {
	Patterns []DetectedPattern `json:"patterns"`
	Severity string            `json:"severity"`
	Summary  string            `json:"summary"`
}

type DetectedPattern struct {
	Type        PatternType `json:"type"`
	Description string      `json:"description"`
	Count       int         `json:"count"`
	Timestamps  []string    `json:"timestamps,omitempty"`
}

type PatternType string

const (
	PatternCrashLoop   PatternType = "CrashLoop"
	PatternOOMKilled   PatternType = "OOMKilled"
	PatternErrorSpike  PatternType = "ErrorSpike"
	PatternSlowLatency PatternType = "SlowLatency"
	PatternDiskFull    PatternType = "DiskFull"
)

type AnalyzeMetricPatternRequest struct {
	Result     *MetricQueryResult `json:"result,omitempty"`
	ArtifactID string             `json:"artifact_id,omitempty"`
	Query      string             `json:"query,omitempty"`
}

type AnalyzeMetricPatternResult struct {
	Patterns []MetricPattern `json:"patterns"`
	Severity string          `json:"severity"`
	Summary  string          `json:"summary"`
}

type MetricPattern struct {
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Series      int     `json:"series"`
	MaxValue    float64 `json:"max_value,omitempty"`
}

type KeyEvidenceRequest struct {
	Logs       []LogEntry        `json:"logs,omitempty"`
	Metrics    []MetricSeries    `json:"metrics,omitempty"`
	Patterns   []DetectedPattern `json:"patterns,omitempty"`
	ArtifactID string            `json:"artifact_id,omitempty"`
	Limit      int               `json:"limit,omitempty"`
}

type KeyEvidenceResult struct {
	Items   []EvidenceItem `json:"items"`
	Summary string         `json:"summary"`
}

type EvidenceItem struct {
	Source    string `json:"source"`
	Timestamp string `json:"timestamp,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Message   string `json:"message"`
}

type SummarizeEvidenceRequest struct {
	Items      []EvidenceItem `json:"items,omitempty"`
	Patterns   []string       `json:"patterns,omitempty"`
	ArtifactID string         `json:"artifact_id,omitempty"`
}

type EvidenceSummaryResult struct {
	Summary   string   `json:"summary"`
	Signals   []string `json:"signals,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type ArtifactSampleRequest struct {
	ArtifactID string `json:"artifact_id"`
	MaxLines   int    `json:"max_lines,omitempty"`
}

type ArtifactSampleResult struct {
	ArtifactID string   `json:"artifact_id"`
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	Truncated  bool     `json:"truncated"`
}

type CleanArtifactsRequest struct {
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
	MaxBytes   int64 `json:"max_bytes,omitempty"`
}

type CleanArtifactsResult struct {
	Removed int   `json:"removed"`
	Bytes   int64 `json:"bytes"`
}
