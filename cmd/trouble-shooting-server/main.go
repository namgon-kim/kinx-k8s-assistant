package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/troubleshooting"
)

func main() {
	port := flag.Int("port", 9091, "MCP server port")
	runbookDir := flag.String("runbook-dir", "", "Directory containing troubleshooting runbook YAML files")
	issueDir := flag.String("issue-dir", defaultIssueDir(), "Directory for exported issue YAML files")
	knowledgeDir := flag.String("knowledge-dir", defaultKnowledgeDir(), "Directory for troubleshooting knowledge files")
	searchMode := flag.String("rag-mode", "hybrid", "Knowledge search mode: keyword|hybrid")
	knowledgeProvider := flag.String("knowledge-provider", "local", "Knowledge provider: local|endpoint")
	ragEndpoint := flag.String("rag-endpoint", "", "External RAG endpoint URL for search_knowledge")
	ragAPIKey := flag.String("rag-api-key", "", "External RAG endpoint bearer token")
	ragTimeout := flag.Int("rag-timeout", 30, "External RAG endpoint timeout seconds")
	importOnStart := flag.Bool("import-on-start", true, "Import exported issues into knowledge store on startup")
	flag.Parse()

	runbooks, err := troubleshooting.LoadRunbooks(*runbookDir)
	if err != nil {
		log.Fatalf("failed to load runbooks: %v", err)
	}

	cfg := troubleshooting.Config{
		RunbookDir:        *runbookDir,
		IssueDir:          *issueDir,
		KnowledgeDir:      *knowledgeDir,
		SearchMode:        troubleshooting.SearchMode(*searchMode),
		KnowledgeProvider: troubleshooting.KnowledgeProvider(*knowledgeProvider),
		EndpointURL:       *ragEndpoint,
		EndpointAPIKey:    *ragAPIKey,
		EndpointTimeout:   *ragTimeout,
		MaxCases:          5,
		MaskSensitive:     true,
	}
	svc := troubleshooting.NewService(cfg, runbooks)

	ctx := context.Background()
	if *importOnStart {
		if count, err := svc.IndexKnowledge(ctx, troubleshooting.KnowledgeIndexRequest{
			Rebuild:         true,
			IncludeIssues:   true,
			IncludeRunbooks: true,
		}); err != nil {
			log.Printf("warning: failed to index knowledge: %v", err)
		} else {
			log.Printf("indexed %d troubleshooting knowledge cases", count)
		}
	}

	server := troubleshooting.NewServer(*port, svc)
	fmt.Printf("trouble-shooting MCP server starting on port %d\n", *port)
	if err := server.Start(ctx); err != nil {
		log.Fatal("server error:", err)
	}
}

func defaultIssueDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".k8s-assistant", "troubleshooting", "issues")
	}
	return filepath.Join(home, ".k8s-assistant", "troubleshooting", "issues")
}

func defaultKnowledgeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".k8s-assistant", "troubleshooting", "kb")
	}
	return filepath.Join(home, ".k8s-assistant", "troubleshooting", "kb")
}
