package loganalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	port     int
	analyzer Analyzer
}

func NewServer(port int, analyzer Analyzer) *Server {
	return &Server{
		port:     port,
		analyzer: analyzer,
	}
}

func (s *Server) Start(ctx context.Context) error {
	mcpServer := server.NewMCPServer("log-analyzer", "0.1.0",
		server.WithToolCapabilities(true),
	)

	s.registerTools(mcpServer)

	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)

	addr := fmt.Sprintf(":%d", s.port)
	return httpServer.Start(addr)
}

func (s *Server) registerTools(mcpServer *server.MCPServer) {
	s.registerFetchLogsTool(mcpServer)
	s.registerAnalyzePatternTool(mcpServer)
	s.registerQueryLokiTool(mcpServer)
	s.registerQueryLokiInstantTool(mcpServer)
	s.registerQueryLokiLabelsTool(mcpServer)
	s.registerQueryLokiSeriesTool(mcpServer)
	s.registerQueryPrometheusInstantTool(mcpServer)
	s.registerQueryPrometheusRangeTool(mcpServer)
	s.registerListPrometheusAlertsTool(mcpServer)
	s.registerListPrometheusRulesTool(mcpServer)
	s.registerListPrometheusTargetsTool(mcpServer)
	s.registerListGrafanaDatasourcesTool(mcpServer)
	s.registerQueryGrafanaDatasourceTool(mcpServer)
	s.registerSearchGrafanaDashboardsTool(mcpServer)
	s.registerGetGrafanaDashboardTool(mcpServer)
	s.registerExtractGrafanaPanelQueriesTool(mcpServer)
	s.registerListGrafanaAlertRulesTool(mcpServer)
	s.registerListOpenSearchIndicesTool(mcpServer)
	s.registerGetOpenSearchMappingTool(mcpServer)
	s.registerQueryOpenSearchTool(mcpServer)
	s.registerCheckSourcesTool(mcpServer)
}

func (s *Server) registerFetchLogsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("fetch_logs",
		mcp.WithDescription("지정된 Pod/컨테이너의 로그를 조회합니다"),
		mcp.WithString("source", mcp.Description("로그 source: file, loki, opensearch")),
		mcp.WithString("index", mcp.Description("OpenSearch index or index pattern")),
		mcp.WithString("file_path", mcp.Description("직접 읽을 로그 파일 경로")),
		mcp.WithString("namespace", mcp.Description("쿠버네티스 네임스페이스")),
		mcp.WithString("pod_name", mcp.Description("Pod 이름")),
		mcp.WithString("container_name", mcp.Description("컨테이너 이름")),
		mcp.WithString("level", mcp.Description("로그 레벨 필터")),
		mcp.WithNumber("since_seconds", mcp.Description("조회 시간 범위(초)")),
		mcp.WithNumber("max_lines", mcp.Description("최대 로그 라인 수 (기본: 1000)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		source := req.GetString("source", "file")
		index := req.GetString("index", "")
		filePath := req.GetString("file_path", "")
		namespace := req.GetString("namespace", "default")
		podName := req.GetString("pod_name", "")
		containerName := req.GetString("container_name", "")
		level := req.GetString("level", "")
		sinceSeconds := int64(req.GetFloat("since_seconds", 0))
		maxLines := int(req.GetFloat("max_lines", 1000))

		fetchReq := FetchLogsRequest{
			Source:        source,
			Index:         index,
			FilePath:      filePath,
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
			Level:         level,
			SinceSeconds:  int64(sinceSeconds),
			MaxLines:      int(maxLines),
		}

		result, err := s.analyzer.FetchLogs(ctx, fetchReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to fetch logs: %v", err)), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerAnalyzePatternTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("analyze_pattern",
		mcp.WithDescription("로그에서 이상 패턴을 탐지합니다"),
		mcp.WithString("source", mcp.Description("로그 source: file, loki, opensearch")),
		mcp.WithString("index", mcp.Description("OpenSearch index or index pattern")),
		mcp.WithString("file_path", mcp.Description("직접 읽을 로그 파일 경로")),
		mcp.WithString("namespace", mcp.Description("쿠버네티스 네임스페이스")),
		mcp.WithString("pod_name", mcp.Description("Pod 이름")),
		mcp.WithString("container_name", mcp.Description("컨테이너 이름")),
		mcp.WithString("level", mcp.Description("로그 레벨 필터")),
		mcp.WithNumber("since_seconds", mcp.Description("조회 시간 범위(초)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		source := req.GetString("source", "file")
		index := req.GetString("index", "")
		filePath := req.GetString("file_path", "")
		namespace := req.GetString("namespace", "default")
		podName := req.GetString("pod_name", "")
		containerName := req.GetString("container_name", "")
		level := req.GetString("level", "")
		sinceSeconds := int64(req.GetFloat("since_seconds", 0))

		fetchReq := FetchLogsRequest{
			Source:        source,
			Index:         index,
			FilePath:      filePath,
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
			Level:         level,
			SinceSeconds:  sinceSeconds,
			MaxLines:      1000,
		}

		logs, err := s.analyzer.FetchLogs(ctx, fetchReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to fetch logs for pattern analysis: %v", err)), nil
		}

		patternReq := AnalyzePatternRequest{
			Logs:       logs.Logs,
			ArtifactID: logs.ArtifactID,
			PodName:    podName,
			Namespace:  namespace,
		}

		result, err := s.analyzer.AnalyzePattern(ctx, patternReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to analyze pattern: %v", err)), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryLokiTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_loki",
		mcp.WithDescription("LogQL query_range를 실행하고 원문은 artifact로 저장합니다"),
		mcp.WithString("query", mcp.Required(), mcp.Description("LogQL query")),
		mcp.WithNumber("limit", mcp.Description("최대 로그 라인 수")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.QueryLoki(ctx, LokiQueryRequest{
			Query: req.GetString("query", ""),
			Limit: int(req.GetFloat("limit", 0)),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query loki: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryLokiInstantTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_loki_instant",
		mcp.WithDescription("LogQL instant query를 실행하고 원문은 artifact로 저장합니다"),
		mcp.WithString("query", mcp.Required(), mcp.Description("LogQL query")),
		mcp.WithString("time", mcp.Description("RFC3339 query time")),
		mcp.WithNumber("limit", mcp.Description("최대 로그 라인 수")),
		mcp.WithString("direction", mcp.Description("forward 또는 backward")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		queryTime, _ := time.Parse(time.RFC3339, req.GetString("time", ""))
		result, err := s.analyzer.QueryLokiInstant(ctx, LokiQueryRequest{
			Query:     req.GetString("query", ""),
			End:       queryTime,
			Limit:     int(req.GetFloat("limit", 0)),
			Direction: req.GetString("direction", ""),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query loki instant: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryLokiLabelsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_loki_labels",
		mcp.WithDescription("Loki label 이름 또는 label value 목록을 조회합니다"),
		mcp.WithString("name", mcp.Description("비우면 label 이름 목록, 설정하면 해당 label 값 목록")),
		mcp.WithString("matcher", mcp.Description("콤마 구분 LogQL stream matcher")),
		mcp.WithString("query", mcp.Description("LogQL stream matcher")),
		mcp.WithString("start", mcp.Description("RFC3339 start time")),
		mcp.WithString("end", mcp.Description("RFC3339 end time")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start, _ := time.Parse(time.RFC3339, req.GetString("start", ""))
		end, _ := time.Parse(time.RFC3339, req.GetString("end", ""))
		result, err := s.analyzer.QueryLokiLabels(ctx, LokiLabelsRequest{
			Name:    req.GetString("name", ""),
			Matcher: splitCSV(req.GetString("matcher", "")),
			Query:   req.GetString("query", ""),
			Start:   start,
			End:     end,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query loki labels: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryLokiSeriesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_loki_series",
		mcp.WithDescription("Loki series를 조회합니다"),
		mcp.WithString("matcher", mcp.Description("콤마 구분 LogQL stream matcher")),
		mcp.WithString("query", mcp.Description("LogQL stream matcher")),
		mcp.WithString("start", mcp.Description("RFC3339 start time")),
		mcp.WithString("end", mcp.Description("RFC3339 end time")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start, _ := time.Parse(time.RFC3339, req.GetString("start", ""))
		end, _ := time.Parse(time.RFC3339, req.GetString("end", ""))
		result, err := s.analyzer.QueryLokiSeries(ctx, LokiSeriesRequest{
			Matcher: splitCSV(req.GetString("matcher", "")),
			Query:   req.GetString("query", ""),
			Start:   start,
			End:     end,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query loki series: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryPrometheusInstantTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_prometheus_instant",
		mcp.WithDescription("instant PromQL을 실행합니다"),
		mcp.WithString("query", mcp.Required(), mcp.Description("PromQL query")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.QueryPrometheusInstant(ctx, PrometheusInstantRequest{Query: req.GetString("query", "")})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query prometheus: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryPrometheusRangeTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_prometheus_range",
		mcp.WithDescription("range PromQL을 실행합니다"),
		mcp.WithString("query", mcp.Required(), mcp.Description("PromQL query")),
		mcp.WithString("start", mcp.Required(), mcp.Description("RFC3339 start time")),
		mcp.WithString("end", mcp.Required(), mcp.Description("RFC3339 end time")),
		mcp.WithString("step", mcp.Description("query step, e.g. 60s")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start, err := time.Parse(time.RFC3339, req.GetString("start", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start time: %v", err)), nil
		}
		end, err := time.Parse(time.RFC3339, req.GetString("end", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid end time: %v", err)), nil
		}
		result, err := s.analyzer.QueryPrometheusRange(ctx, PrometheusRangeRequest{
			Query: req.GetString("query", ""),
			Start: start,
			End:   end,
			Step:  req.GetString("step", "60s"),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query prometheus range: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListPrometheusAlertsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_prometheus_alerts",
		mcp.WithDescription("Prometheus active alert 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListPrometheusAlerts(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list prometheus alerts: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListPrometheusRulesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_prometheus_rules",
		mcp.WithDescription("Prometheus rule group 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListPrometheusRules(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list prometheus rules: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListPrometheusTargetsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_prometheus_targets",
		mcp.WithDescription("Prometheus target 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListPrometheusTargets(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list prometheus targets: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListGrafanaDatasourcesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_grafana_datasources",
		mcp.WithDescription("Grafana datasource 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListGrafanaDatasources(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list grafana datasources: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryGrafanaDatasourceTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_grafana_datasource",
		mcp.WithDescription("Grafana datasource proxy로 read-only GET query를 실행합니다"),
		mcp.WithString("datasource_uid", mcp.Required(), mcp.Description("Grafana datasource UID")),
		mcp.WithString("path", mcp.Description("Datasource proxy path, e.g. /api/v1/query")),
		mcp.WithString("params", mcp.Description("JSON object 또는 comma-separated key=value query params")),
		mcp.WithString("query", mcp.Description("query expression")),
		mcp.WithString("start", mcp.Description("RFC3339 start time")),
		mcp.WithString("end", mcp.Description("RFC3339 end time")),
		mcp.WithString("time", mcp.Description("RFC3339 query time")),
		mcp.WithString("step", mcp.Description("query step")),
		mcp.WithNumber("limit", mcp.Description("result limit")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start, _ := time.Parse(time.RFC3339, req.GetString("start", ""))
		end, _ := time.Parse(time.RFC3339, req.GetString("end", ""))
		queryTime, _ := time.Parse(time.RFC3339, req.GetString("time", ""))
		result, err := s.analyzer.QueryGrafanaDatasource(ctx, GrafanaDatasourceQueryRequest{
			DatasourceUID: req.GetString("datasource_uid", ""),
			Path:          req.GetString("path", ""),
			Params:        parseStringMap(req.GetString("params", "")),
			Query:         req.GetString("query", ""),
			Start:         start,
			End:           end,
			Time:          queryTime,
			Step:          req.GetString("step", ""),
			Limit:         int(req.GetFloat("limit", 0)),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query grafana datasource: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerSearchGrafanaDashboardsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("search_grafana_dashboards",
		mcp.WithDescription("Grafana dashboard를 검색합니다"),
		mcp.WithString("query", mcp.Description("dashboard search query")),
		mcp.WithString("type", mcp.Description("Grafana search type, e.g. dash-db")),
		mcp.WithNumber("limit", mcp.Description("result limit")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.SearchGrafanaDashboards(ctx, GrafanaDashboardSearchRequest{
			Query: req.GetString("query", ""),
			Type:  req.GetString("type", ""),
			Limit: int(req.GetFloat("limit", 0)),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to search grafana dashboards: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerGetGrafanaDashboardTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("get_grafana_dashboard",
		mcp.WithDescription("Grafana dashboard를 UID로 조회합니다"),
		mcp.WithString("uid", mcp.Required(), mcp.Description("Grafana dashboard UID")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.GetGrafanaDashboard(ctx, GrafanaDashboardRequest{UID: req.GetString("uid", "")})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get grafana dashboard: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerExtractGrafanaPanelQueriesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("extract_grafana_panel_queries",
		mcp.WithDescription("Grafana dashboard UID 또는 artifact에서 panel query를 추출합니다"),
		mcp.WithString("uid", mcp.Description("Grafana dashboard UID")),
		mcp.WithString("artifact_id", mcp.Description("dashboard artifact id")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ExtractGrafanaPanelQueries(ctx, GrafanaPanelQueryRequest{
			UID:        req.GetString("uid", ""),
			ArtifactID: req.GetString("artifact_id", ""),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to extract grafana panel queries: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListGrafanaAlertRulesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_grafana_alert_rules",
		mcp.WithDescription("Grafana unified alert rule 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListGrafanaAlertRules(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list grafana alert rules: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerListOpenSearchIndicesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("list_opensearch_indices",
		mcp.WithDescription("OpenSearch index 목록을 조회합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.ListOpenSearchIndices(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list opensearch indices: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerGetOpenSearchMappingTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("get_opensearch_mapping",
		mcp.WithDescription("OpenSearch index mapping을 조회합니다"),
		mcp.WithString("index", mcp.Description("OpenSearch index or index pattern")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.GetOpenSearchMapping(ctx, OpenSearchMappingRequest{Index: req.GetString("index", "")})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get opensearch mapping: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerQueryOpenSearchTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("query_opensearch",
		mcp.WithDescription("OpenSearch log search를 실행하고 원문은 artifact로 저장합니다"),
		mcp.WithString("index", mcp.Description("OpenSearch index or index pattern")),
		mcp.WithString("query_string", mcp.Description("OpenSearch query_string query")),
		mcp.WithString("namespace", mcp.Description("Kubernetes namespace")),
		mcp.WithString("pod_name", mcp.Description("Pod name")),
		mcp.WithString("container_name", mcp.Description("Container name")),
		mcp.WithString("level", mcp.Description("Log level")),
		mcp.WithString("message", mcp.Description("Message text filter")),
		mcp.WithString("start", mcp.Description("RFC3339 start time")),
		mcp.WithString("end", mcp.Description("RFC3339 end time")),
		mcp.WithNumber("limit", mcp.Description("Maximum hits")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start, _ := time.Parse(time.RFC3339, req.GetString("start", ""))
		end, _ := time.Parse(time.RFC3339, req.GetString("end", ""))
		result, err := s.analyzer.QueryOpenSearch(ctx, OpenSearchQueryRequest{
			Index:         req.GetString("index", ""),
			QueryString:   req.GetString("query_string", ""),
			Namespace:     req.GetString("namespace", ""),
			PodName:       req.GetString("pod_name", ""),
			ContainerName: req.GetString("container_name", ""),
			Level:         req.GetString("level", ""),
			Message:       req.GetString("message", ""),
			Start:         start,
			End:           end,
			Limit:         int(req.GetFloat("limit", 0)),
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to query opensearch: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerCheckSourcesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("check_sources",
		mcp.WithDescription("log-analyzer source 설정과 연결 상태를 확인합니다"),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.analyzer.CheckSources(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to check sources: %v", err)), nil
		}
		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
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

func parseStringMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed
	}
	values := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		pieces := strings.SplitN(part, "=", 2)
		if len(pieces) != 2 {
			continue
		}
		key := strings.TrimSpace(pieces[0])
		if key != "" {
			values[key] = strings.TrimSpace(pieces[1])
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}
