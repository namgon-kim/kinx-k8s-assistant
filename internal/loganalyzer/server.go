package loganalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	s.registerRAGLookupTool(mcpServer)
	s.registerAnalyzeRemediateTool(mcpServer)
}

func (s *Server) registerFetchLogsTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("fetch_logs",
		mcp.WithDescription("지정된 Pod/컨테이너의 로그를 조회합니다"),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("쿠버네티스 네임스페이스")),
		mcp.WithString("pod_name", mcp.Required(), mcp.Description("Pod 이름")),
		mcp.WithString("container_name", mcp.Description("컨테이너 이름")),
		mcp.WithNumber("since_seconds", mcp.Description("조회 시간 범위(초)")),
		mcp.WithNumber("max_lines", mcp.Description("최대 로그 라인 수 (기본: 1000)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := req.GetString("namespace", "default")
		podName := req.GetString("pod_name", "")
		containerName := req.GetString("container_name", "")
		sinceSeconds := int64(req.GetFloat("since_seconds", 0))
		maxLines := int(req.GetFloat("max_lines", 1000))

		fetchReq := FetchLogsRequest{
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
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
		mcp.WithString("namespace", mcp.Required(), mcp.Description("쿠버네티스 네임스페이스")),
		mcp.WithString("pod_name", mcp.Required(), mcp.Description("Pod 이름")),
		mcp.WithString("container_name", mcp.Description("컨테이너 이름")),
		mcp.WithNumber("since_seconds", mcp.Description("조회 시간 범위(초)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := req.GetString("namespace", "default")
		podName := req.GetString("pod_name", "")
		containerName := req.GetString("container_name", "")
		sinceSeconds := int64(req.GetFloat("since_seconds", 0))

		fetchReq := FetchLogsRequest{
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
			SinceSeconds:  sinceSeconds,
			MaxLines:      1000,
		}

		logs, err := s.analyzer.FetchLogs(ctx, fetchReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to fetch logs for pattern analysis: %v", err)), nil
		}

		patternReq := AnalyzePatternRequest{
			Logs:      logs.Logs,
			PodName:   podName,
			Namespace: namespace,
		}

		result, err := s.analyzer.AnalyzePattern(ctx, patternReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to analyze pattern: %v", err)), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerRAGLookupTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("rag_lookup",
		mcp.WithDescription("증상 설명으로 유사 장애 사례를 검색합니다"),
		mcp.WithString("symptom", mcp.Required(), mcp.Description("증상 설명 (자연어)")),
		mcp.WithString("patterns", mcp.Description("감지된 패턴 목록 (콤마 구분)")),
		mcp.WithNumber("max_results", mcp.Description("최대 결과 수 (기본: 5)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		symptom := req.GetString("symptom", "")
		patternsStr := req.GetString("patterns", "")
		maxResults := int(req.GetFloat("max_results", 5))

		patterns := []string{}
		if patternsStr != "" {
			patterns = strings.Split(strings.ReplaceAll(patternsStr, " ", ""), ",")
		}

		ragReq := RAGLookupRequest{
			Symptom:    symptom,
			Patterns:   patterns,
			MaxResults: maxResults,
		}

		result, err := s.analyzer.RAGLookup(ctx, ragReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to perform RAG lookup: %v", err)), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}

func (s *Server) registerAnalyzeRemediateTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("analyze_and_remediate",
		mcp.WithDescription("로그를 분석하고 조치 방안을 제시합니다"),
		mcp.WithString("namespace", mcp.Required(), mcp.Description("쿠버네티스 네임스페이스")),
		mcp.WithString("pod_name", mcp.Required(), mcp.Description("Pod 이름")),
		mcp.WithString("container_name", mcp.Description("컨테이너 이름")),
		mcp.WithNumber("since_seconds", mcp.Description("조회 시간 범위(초)")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := req.GetString("namespace", "default")
		podName := req.GetString("pod_name", "")
		containerName := req.GetString("container_name", "")
		sinceSeconds := int64(req.GetFloat("since_seconds", 0))

		remediateReq := RemediateRequest{
			Namespace:     namespace,
			PodName:       podName,
			ContainerName: containerName,
			SinceSeconds:  sinceSeconds,
		}

		result, err := s.analyzer.AnalyzeAndRemediate(ctx, remediateReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to analyze and remediate: %v", err)), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})
}
