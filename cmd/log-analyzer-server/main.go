package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

func main() {
	port := flag.Int("port", 9090, "MCP server port")
	logDir := flag.String("log-dir", "/var/log/filebeat", "file log root directory")
	artifactDir := flag.String("artifact-dir", "", "artifact directory")
	lokiURL := flag.String("loki-url", os.Getenv("K8S_ASSISTANT_LOKI_URL"), "Loki base URL")
	promURL := flag.String("prometheus-url", os.Getenv("K8S_ASSISTANT_PROMETHEUS_URL"), "Prometheus base URL")
	grafanaURL := flag.String("grafana-url", os.Getenv("K8S_ASSISTANT_GRAFANA_URL"), "Grafana base URL")
	openSearchURL := flag.String("opensearch-url", os.Getenv("K8S_ASSISTANT_OPENSEARCH_URL"), "OpenSearch base URL")
	openSearchIndex := flag.String("opensearch-index", os.Getenv("K8S_ASSISTANT_OPENSEARCH_DEFAULT_INDEX"), "OpenSearch default index")
	lokiOrgID := flag.String("loki-org-id", os.Getenv("K8S_ASSISTANT_LOKI_ORG_ID"), "Loki X-Scope-OrgID")
	grafanaOrgID := flag.String("grafana-org-id", os.Getenv("K8S_ASSISTANT_GRAFANA_ORG_ID"), "Grafana org id")
	flag.Parse()

	cfg := config.LogAnalyzerConfig{
		Enabled:          true,
		ArtifactDir:      *artifactDir,
		ArtifactTTL:      24 * 60 * 60,
		MaxArtifactBytes: 50 * 1024 * 1024,
		File: config.LogAnalyzerFileConfig{
			Enabled:  true,
			RootDir:  *logDir,
			MaxLines: 1000,
		},
		Loki: config.LogAnalyzerHTTPConfig{
			Enabled:      true,
			URL:          *lokiURL,
			Token:        os.Getenv("K8S_ASSISTANT_LOKI_TOKEN"),
			Username:     os.Getenv("K8S_ASSISTANT_LOKI_USERNAME"),
			Password:     os.Getenv("K8S_ASSISTANT_LOKI_PASSWORD"),
			OrgID:        *lokiOrgID,
			Timeout:      30,
			QueryTimeout: 30,
			DefaultLimit: 1000,
			MaxEntries:   5000,
		},
		Prometheus: config.LogAnalyzerHTTPConfig{
			Enabled:      true,
			URL:          *promURL,
			Token:        os.Getenv("K8S_ASSISTANT_PROMETHEUS_TOKEN"),
			Username:     os.Getenv("K8S_ASSISTANT_PROMETHEUS_USERNAME"),
			Password:     os.Getenv("K8S_ASSISTANT_PROMETHEUS_PASSWORD"),
			Timeout:      30,
			QueryTimeout: 30,
		},
		Grafana: config.LogAnalyzerHTTPConfig{
			Enabled:      true,
			URL:          *grafanaURL,
			Token:        os.Getenv("K8S_ASSISTANT_GRAFANA_TOKEN"),
			Username:     os.Getenv("K8S_ASSISTANT_GRAFANA_USERNAME"),
			Password:     os.Getenv("K8S_ASSISTANT_GRAFANA_PASSWORD"),
			OrgID:        *grafanaOrgID,
			Timeout:      30,
			QueryTimeout: 30,
		},
		OpenSearch: config.LogAnalyzerHTTPConfig{
			Enabled:      true,
			URL:          *openSearchURL,
			Token:        os.Getenv("K8S_ASSISTANT_OPENSEARCH_TOKEN"),
			Username:     os.Getenv("K8S_ASSISTANT_OPENSEARCH_USERNAME"),
			Password:     os.Getenv("K8S_ASSISTANT_OPENSEARCH_PASSWORD"),
			Timeout:      30,
			QueryTimeout: 30,
			DefaultLimit: 100,
			MaxEntries:   1000,
			DefaultIndex: *openSearchIndex,
		},
	}

	analyzer := loganalyzer.NewAnalyzerFromConfig(cfg)
	server := loganalyzer.NewServer(*port, analyzer)

	fmt.Printf("log-analyzer MCP server starting on port %d\n", *port)
	if err := server.Start(context.Background()); err != nil {
		log.Fatal("server error:", err)
	}
}
