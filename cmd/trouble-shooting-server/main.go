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
	configFile := flag.String("config", "", "Trouble-shooting config YAML file (default: ~/.k8s-assistant/trouble-shooting.yaml if present)")
	port := flag.Int("port", 9091, "MCP server port")
	runbookDir := flag.String("runbook-dir", "", "Directory containing troubleshooting runbook YAML files")
	issueDir := flag.String("issue-dir", defaultIssueDir(), "Directory for exported issue YAML files")
	knowledgeDir := flag.String("knowledge-dir", defaultKnowledgeDir(), "Directory for troubleshooting knowledge files")
	searchMode := flag.String("rag-mode", "hybrid", "Knowledge search mode: keyword|hybrid")
	knowledgeProvider := flag.String("knowledge-provider", "qdrant", "Knowledge provider: local|endpoint|qdrant")
	ragEndpoint := flag.String("rag-endpoint", "", "External RAG endpoint URL for search_knowledge")
	ragAPIKey := flag.String("rag-api-key", "", "External RAG endpoint bearer token")
	embeddingURL := flag.String("embedding-url", troubleshooting.DefaultEmbeddingBaseURL, "Embedding endpoint base URL")
	embeddingAPIKey := flag.String("embedding-api-key", "", "Embedding endpoint bearer token")
	embeddingModel := flag.String("embedding-model", troubleshooting.DefaultEmbeddingModel, "Embedding model")
	vectorName := flag.String("vector-name", troubleshooting.DefaultVectorName, "Qdrant vector name")
	vectorSize := flag.Int("vector-size", troubleshooting.DefaultVectorSize, "Qdrant vector size")
	vectorDistance := flag.String("vector-distance", troubleshooting.DefaultDistance, "Qdrant vector distance")
	embeddingMaxLength := flag.Int("embedding-max-length", troubleshooting.DefaultEmbeddingMaxLen, "Embedding max length")
	rerankerEnabled := flag.Bool("reranker-enabled", true, "Enable reranker after Qdrant vector search")
	rerankerURL := flag.String("reranker-url", troubleshooting.DefaultRerankerBaseURL, "Reranker endpoint base URL")
	rerankerAPIKey := flag.String("reranker-api-key", "", "Reranker endpoint bearer token")
	rerankerModel := flag.String("reranker-model", troubleshooting.DefaultRerankerModel, "Reranker model")
	rerankerTopN := flag.Int("reranker-top-n", troubleshooting.DefaultRerankerTopN, "Reranker top N")
	rerankerMaxLength := flag.Int("reranker-max-length", troubleshooting.DefaultRerankerMaxLen, "Reranker max length")
	qdrantURL := flag.String("qdrant-url", troubleshooting.DefaultQdrantURL, "Qdrant base URL")
	qdrantAPIKey := flag.String("qdrant-api-key", "", "Qdrant API key")
	qdrantCollection := flag.String("qdrant-collection", troubleshooting.DefaultQdrantCollection, "Qdrant collection")
	qdrantLimit := flag.Int("qdrant-limit", troubleshooting.DefaultQdrantLimit, "Qdrant search limit")
	ragTimeout := flag.Int("rag-timeout", 30, "External RAG endpoint timeout seconds")
	importOnStart := flag.Bool("import-on-start", true, "Import exported issues into knowledge store on startup")
	flag.Parse()

	visited := visitedFlags()
	cfg := troubleshooting.Config{
		RunbookDir:          "",
		IssueDir:            defaultIssueDir(),
		KnowledgeDir:        defaultKnowledgeDir(),
		SearchMode:          troubleshooting.SearchModeHybrid,
		KnowledgeProvider:   troubleshooting.KnowledgeProviderQdrant,
		EndpointTimeout:     30,
		EmbeddingModel:      troubleshooting.DefaultEmbeddingModel,
		VectorName:          troubleshooting.DefaultVectorName,
		VectorSize:          troubleshooting.DefaultVectorSize,
		Distance:            troubleshooting.DefaultDistance,
		EmbeddingMaxLength:  troubleshooting.DefaultEmbeddingMaxLen,
		NormalizeEmbeddings: true,
		RerankerModel:       troubleshooting.DefaultRerankerModel,
		RerankerTopN:        troubleshooting.DefaultRerankerTopN,
		RerankerMaxLength:   troubleshooting.DefaultRerankerMaxLen,
		RerankerUseFP16:     true,
		RerankerNormalize:   true,
		QdrantURL:           troubleshooting.DefaultQdrantURL,
		QdrantCollection:    troubleshooting.DefaultQdrantCollection,
		QdrantLimit:         troubleshooting.DefaultQdrantLimit,
		QdrantWithPayload:   true,
		QdrantWithVectors:   false,
		QdrantExact:         true,
		RerankerEnabled:     true,
		RerankerEnabledSet:  true,
		MaxCases:            5,
		MaskSensitive:       true,
	}
	portValue := 9091
	importOnStartValue := true

	if fileCfg, _, err := troubleshooting.LoadOptionalFileConfig(*configFile); err != nil {
		log.Fatalf("failed to load config file: %v", err)
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
		if fileCfg.TroubleShooting.Server.Port > 0 {
			portValue = fileCfg.TroubleShooting.Server.Port
		}
		if fileCfg.TroubleShooting.Server.ImportOnStart != nil {
			importOnStartValue = *fileCfg.TroubleShooting.Server.ImportOnStart
		}
	}
	applyTroubleshootingFlagOverrides(&cfg, visited, troubleshootingFlagValues{
		runbookDir:         *runbookDir,
		issueDir:           *issueDir,
		knowledgeDir:       *knowledgeDir,
		searchMode:         *searchMode,
		knowledgeProvider:  *knowledgeProvider,
		ragEndpoint:        *ragEndpoint,
		ragAPIKey:          *ragAPIKey,
		embeddingURL:       *embeddingURL,
		embeddingAPIKey:    *embeddingAPIKey,
		embeddingModel:     *embeddingModel,
		vectorName:         *vectorName,
		vectorSize:         *vectorSize,
		vectorDistance:     *vectorDistance,
		embeddingMaxLength: *embeddingMaxLength,
		rerankerEnabled:    *rerankerEnabled,
		rerankerURL:        *rerankerURL,
		rerankerAPIKey:     *rerankerAPIKey,
		rerankerModel:      *rerankerModel,
		rerankerTopN:       *rerankerTopN,
		rerankerMaxLength:  *rerankerMaxLength,
		qdrantURL:          *qdrantURL,
		qdrantAPIKey:       *qdrantAPIKey,
		qdrantCollection:   *qdrantCollection,
		qdrantLimit:        *qdrantLimit,
		ragTimeout:         *ragTimeout,
	})
	if visited["port"] {
		portValue = *port
	}
	if visited["import-on-start"] {
		importOnStartValue = *importOnStart
	}

	runbooks, err := troubleshooting.LoadRunbooks(cfg.RunbookDir)
	if err != nil {
		log.Fatalf("failed to load runbooks: %v", err)
	}
	svc := troubleshooting.NewService(cfg, runbooks)

	ctx := context.Background()
	if importOnStartValue {
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

	server := troubleshooting.NewServer(portValue, svc)
	fmt.Printf("trouble-shooting MCP server starting on port %d\n", portValue)
	if err := server.Start(ctx); err != nil {
		log.Fatal("server error:", err)
	}
}

type troubleshootingFlagValues struct {
	runbookDir         string
	issueDir           string
	knowledgeDir       string
	searchMode         string
	knowledgeProvider  string
	ragEndpoint        string
	ragAPIKey          string
	embeddingModel     string
	embeddingURL       string
	embeddingAPIKey    string
	vectorName         string
	vectorSize         int
	vectorDistance     string
	embeddingMaxLength int
	rerankerEnabled    bool
	rerankerURL        string
	rerankerAPIKey     string
	rerankerModel      string
	rerankerTopN       int
	rerankerMaxLength  int
	qdrantURL          string
	qdrantAPIKey       string
	qdrantCollection   string
	qdrantLimit        int
	ragTimeout         int
}

func visitedFlags() map[string]bool {
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

func applyTroubleshootingFlagOverrides(cfg *troubleshooting.Config, visited map[string]bool, values troubleshootingFlagValues) {
	if visited["runbook-dir"] {
		cfg.RunbookDir = values.runbookDir
	}
	if visited["issue-dir"] {
		cfg.IssueDir = values.issueDir
	}
	if visited["knowledge-dir"] {
		cfg.KnowledgeDir = values.knowledgeDir
	}
	if visited["rag-mode"] {
		cfg.SearchMode = troubleshooting.SearchMode(values.searchMode)
	}
	if visited["knowledge-provider"] {
		cfg.KnowledgeProvider = troubleshooting.KnowledgeProvider(values.knowledgeProvider)
	}
	if visited["rag-endpoint"] {
		cfg.EndpointURL = values.ragEndpoint
	}
	if visited["rag-api-key"] {
		cfg.EndpointAPIKey = values.ragAPIKey
	}
	if visited["rag-timeout"] {
		cfg.EndpointTimeout = values.ragTimeout
	}
	if visited["embedding-url"] {
		cfg.EmbeddingBaseURL = values.embeddingURL
	}
	if visited["embedding-api-key"] {
		cfg.EmbeddingAPIKey = values.embeddingAPIKey
	}
	if visited["embedding-model"] {
		cfg.EmbeddingModel = values.embeddingModel
	}
	if visited["vector-name"] {
		cfg.VectorName = values.vectorName
	}
	if visited["vector-size"] {
		cfg.VectorSize = values.vectorSize
	}
	if visited["vector-distance"] {
		cfg.Distance = values.vectorDistance
	}
	if visited["embedding-max-length"] {
		cfg.EmbeddingMaxLength = values.embeddingMaxLength
	}
	if visited["reranker-enabled"] {
		cfg.RerankerEnabled = values.rerankerEnabled
		cfg.RerankerEnabledSet = true
	}
	if visited["reranker-url"] {
		cfg.RerankerBaseURL = values.rerankerURL
	}
	if visited["reranker-api-key"] {
		cfg.RerankerAPIKey = values.rerankerAPIKey
	}
	if visited["reranker-model"] {
		cfg.RerankerModel = values.rerankerModel
	}
	if visited["reranker-top-n"] {
		cfg.RerankerTopN = values.rerankerTopN
	}
	if visited["reranker-max-length"] {
		cfg.RerankerMaxLength = values.rerankerMaxLength
	}
	if visited["qdrant-url"] {
		cfg.QdrantURL = values.qdrantURL
	}
	if visited["qdrant-api-key"] {
		cfg.QdrantAPIKey = values.qdrantAPIKey
	}
	if visited["qdrant-collection"] {
		cfg.QdrantCollection = values.qdrantCollection
	}
	if visited["qdrant-limit"] {
		cfg.QdrantLimit = values.qdrantLimit
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
