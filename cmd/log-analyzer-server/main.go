package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
	"gopkg.in/yaml.v3"
)

func main() {
	port := flag.Int("port", 9090, "MCP server port")
	logDir := flag.String("log-dir", "/var/log/filebeat", "Filebeat log directory")
	runbookDir := flag.String("runbook-dir", "", "Directory containing runbook YAML files")
	flag.Parse()

	ctx := context.Background()

	jsonParser := loganalyzer.NewJSONParser()
	logFetcher := loganalyzer.NewLogFetcher(*logDir, jsonParser)

	detector := loganalyzer.NewPatternDetector()

	store := loganalyzer.NewSimpleKeywordStore()

	if err := loadRunbooks(ctx, store, *runbookDir); err != nil {
		log.Printf("warning: failed to load runbooks: %v", err)
	}

	analyzer := loganalyzer.NewAnalyzer(logFetcher, detector, store)
	server := loganalyzer.NewServer(*port, analyzer)

	fmt.Printf("log-analyzer MCP server starting on port %d\n", *port)
	if err := server.Start(ctx); err != nil {
		log.Fatal("server error:", err)
	}
}

func loadRunbooks(ctx context.Context, store loganalyzer.VectorStore, runbookDir string) error {
	if runbookDir == "" {
		runbookDir = filepath.Join(filepath.Dir(os.Args[0]), "..", "..", "internal", "loganalyzer", "rag", "runbooks")
	}

	var cases []loganalyzer.SimilarCase

	if err := loadYAMLRunbooks(filepath.Join(runbookDir, "default.yaml"), &cases); err != nil {
		return fmt.Errorf("failed to load default runbooks: %w", err)
	}

	if len(cases) == 0 {
		return fmt.Errorf("no cases loaded from runbooks")
	}

	if err := store.Index(ctx, cases); err != nil {
		return fmt.Errorf("failed to index runbooks: %w", err)
	}

	fmt.Printf("loaded %d runbook cases\n", len(cases))
	return nil
}

func loadYAMLRunbooks(path string, cases *[]loganalyzer.SimilarCase) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var wrapper struct {
		Cases []map[string]interface{} `yaml:"cases"`
	}

	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return err
	}

	for _, raw := range wrapper.Cases {
		c := loganalyzer.SimilarCase{
			Title:      fmt.Sprintf("%v", raw["title"]),
			Cause:      fmt.Sprintf("%v", raw["cause"]),
			Resolution: fmt.Sprintf("%v", raw["resolution"]),
			Source:     fmt.Sprintf("%v", raw["source"]),
		}
		*cases = append(*cases, c)
	}

	return nil
}
