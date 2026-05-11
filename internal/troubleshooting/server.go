package troubleshooting

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type Server struct {
	port int
	svc  *Service
}

func NewServer(port int, svc *Service) *Server {
	return &Server{port: port, svc: svc}
}

func (s *Server) Start(ctx context.Context) error {
	mcpServer := server.NewMCPServer("trouble-shooting", "0.1.0",
		server.WithToolCapabilities(true),
	)
	s.registerTools(mcpServer)

	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)
	return httpServer.Start(fmt.Sprintf(":%d", s.port))
}

func (s *Server) registerTools(mcpServer *server.MCPServer) {
	s.registerMatchRunbookTool(mcpServer)
	s.registerSearchKnowledgeTool(mcpServer)
	s.registerBuildPlanTool(mcpServer)
	s.registerValidatePlanTool(mcpServer)
	s.registerExportIssueTool(mcpServer)
	s.registerImportIssuesTool(mcpServer)
	s.registerIndexKnowledgeTool(mcpServer)
}

func (s *Server) registerMatchRunbookTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("match_runbook",
		mcp.WithDescription("ProblemSignal을 기반으로 구조화 runbook을 매칭합니다"),
		mcp.WithString("signal_json", mcp.Required(), mcp.Description("diagnostic.ProblemSignal JSON")),
		mcp.WithString("target_json", mcp.Description("diagnostic.KubernetesTarget JSON")),
		mcp.WithNumber("top_k", mcp.Description("최대 결과 수")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		searchReq, err := searchRequestFromMCP(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		result, err := s.svc.MatchRunbook(ctx, searchReq)
		return jsonToolResult(result, err)
	})
}

func (s *Server) registerSearchKnowledgeTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("search_knowledge",
		mcp.WithDescription("운영 이슈 RAG 지식베이스에서 유사 사례를 검색합니다"),
		mcp.WithString("signal_json", mcp.Description("diagnostic.ProblemSignal JSON")),
		mcp.WithString("query", mcp.Description("검색 질의")),
		mcp.WithNumber("top_k", mcp.Description("최대 결과 수")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		searchReq, err := searchRequestFromMCP(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		searchReq.Query = req.GetString("query", searchReq.Query)
		result, err := s.svc.SearchKnowledge(ctx, searchReq)
		return jsonToolResult(result, err)
	})
}

func (s *Server) registerBuildPlanTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("build_remediation_plan",
		mcp.WithDescription("선택된 runbook/RAG 사례로 실행 전 조치 계획을 생성합니다"),
		mcp.WithString("request_json", mcp.Required(), mcp.Description("troubleshooting.RemediationPlanRequest JSON")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var planReq RemediationPlanRequest
		if err := json.Unmarshal([]byte(req.GetString("request_json", "")), &planReq); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid request_json: %v", err)), nil
		}
		result, err := s.svc.BuildRemediationPlan(ctx, planReq)
		return jsonToolResult(result, err)
	})
}

func (s *Server) registerValidatePlanTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("validate_remediation_plan",
		mcp.WithDescription("조치 계획의 위험도와 필수 필드를 검증합니다"),
		mcp.WithString("plan_json", mcp.Required(), mcp.Description("troubleshooting.RemediationPlan JSON")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var plan RemediationPlan
		if err := json.Unmarshal([]byte(req.GetString("plan_json", "")), &plan); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid plan_json: %v", err)), nil
		}
		result, err := s.svc.ValidatePlan(ctx, plan)
		return jsonToolResult(result, err)
	})
}

func (s *Server) registerExportIssueTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("export_issue",
		mcp.WithDescription("운영 이슈를 YAML 파일로 export하여 RAG 지식베이스 입력으로 저장합니다"),
		mcp.WithString("issue_json", mcp.Required(), mcp.Description("troubleshooting.ExportedIssue JSON")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var issue ExportedIssue
		if err := json.Unmarshal([]byte(req.GetString("issue_json", "")), &issue); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid issue_json: %v", err)), nil
		}
		path, err := s.svc.ExportIssue(ctx, issue)
		return jsonToolResult(map[string]string{"path": path}, err)
	})
}

func (s *Server) registerImportIssuesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("import_issues",
		mcp.WithDescription("export된 운영 이슈를 knowledge store에 적재합니다"),
		mcp.WithString("dir", mcp.Description("이슈 파일 디렉토리")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		count, err := s.svc.ImportIssues(ctx, req.GetString("dir", ""))
		return jsonToolResult(map[string]int{"imported": count}, err)
	})
}

func (s *Server) registerIndexKnowledgeTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("index_knowledge",
		mcp.WithDescription("runbook과 운영 이슈를 RAG knowledge store에 인덱싱합니다"),
		mcp.WithString("request_json", mcp.Description("troubleshooting.KnowledgeIndexRequest JSON")),
	)
	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexReq := KnowledgeIndexRequest{Rebuild: true, IncludeIssues: true, IncludeRunbooks: true}
		if raw := req.GetString("request_json", ""); raw != "" {
			if err := json.Unmarshal([]byte(raw), &indexReq); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid request_json: %v", err)), nil
			}
		}
		count, err := s.svc.IndexKnowledge(ctx, indexReq)
		return jsonToolResult(map[string]int{"indexed": count}, err)
	})
}

func searchRequestFromMCP(req mcp.CallToolRequest) (TroubleshootingSearchRequest, error) {
	var signal diagnostic.ProblemSignal
	if raw := req.GetString("signal_json", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &signal); err != nil {
			return TroubleshootingSearchRequest{}, fmt.Errorf("invalid signal_json: %w", err)
		}
	}

	var target diagnostic.KubernetesTarget
	if raw := req.GetString("target_json", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &target); err != nil {
			return TroubleshootingSearchRequest{}, fmt.Errorf("invalid target_json: %w", err)
		}
	}

	return TroubleshootingSearchRequest{
		Signal: signal,
		Target: target,
		TopK:   int(req.GetFloat("top_k", 5)),
	}, nil
}

func jsonToolResult(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	data, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return mcp.NewToolResultError(marshalErr.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
