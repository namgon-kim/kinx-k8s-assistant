package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/troubleshooting"
)

func main() {
	configFile := flag.String("config", "", "Trouble-shooting config YAML file (default: ~/.k8s-assistant/trouble-shooting.yaml if present)")
	runbookDir := flag.String("runbook-dir", troubleshooting.DefaultRunbookDir(), "Directory containing troubleshooting runbook YAML files")
	target := flag.String("target", "qdrant", "Upload target: qdrant|endpoint")
	endpoint := flag.String("endpoint", "", "Runbook upload endpoint URL")
	apiKey := flag.String("api-key", "", "Upload endpoint bearer token")
	embeddingURL := flag.String("embedding-url", troubleshooting.DefaultEmbeddingBaseURL, "Embedding endpoint base URL")
	embeddingAPIKey := flag.String("embedding-api-key", "", "Embedding endpoint bearer token")
	embeddingModel := flag.String("embedding-model", troubleshooting.DefaultEmbeddingModel, "Embedding model")
	vectorName := flag.String("vector-name", troubleshooting.DefaultVectorName, "Qdrant vector name")
	embeddingMaxLength := flag.Int("embedding-max-length", troubleshooting.DefaultEmbeddingMaxLen, "Embedding max length")
	qdrantURL := flag.String("qdrant-url", troubleshooting.DefaultQdrantURL, "Qdrant base URL, e.g. http://localhost:6333")
	qdrantAPIKey := flag.String("qdrant-api-key", "", "Qdrant API key")
	qdrantCollection := flag.String("qdrant-collection", troubleshooting.DefaultQdrantCollection, "Qdrant collection name")
	qdrantVectorSize := flag.Int("qdrant-vector-size", troubleshooting.DefaultVectorSize, "Qdrant vector size")
	qdrantDistance := flag.String("qdrant-distance", troubleshooting.DefaultDistance, "Qdrant vector distance")
	qdrantCreate := flag.Bool("qdrant-create-collection", true, "Create Qdrant collection if missing")
	timeout := flag.Int("timeout", 30, "Upload timeout seconds")
	dryRun := flag.Bool("dry-run", false, "Validate and print loaded runbook count without uploading")
	flag.Parse()

	visited := visitedFlags()
	cfg := troubleshooting.Config{
		RunbookDir:          "",
		EmbeddingModel:      troubleshooting.DefaultEmbeddingModel,
		VectorName:          troubleshooting.DefaultVectorName,
		VectorSize:          troubleshooting.DefaultVectorSize,
		Distance:            troubleshooting.DefaultDistance,
		EmbeddingMaxLength:  troubleshooting.DefaultEmbeddingMaxLen,
		NormalizeEmbeddings: true,
		QdrantURL:           troubleshooting.DefaultQdrantURL,
		QdrantCollection:    troubleshooting.DefaultQdrantCollection,
	}
	if fileCfg, _, err := troubleshooting.LoadOptionalFileConfig(*configFile); err != nil {
		log.Fatalf("failed to load config file: %v", err)
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
	}
	if visited["runbook-dir"] {
		cfg.RunbookDir = *runbookDir
	}
	if visited["embedding-url"] {
		cfg.EmbeddingBaseURL = *embeddingURL
	}
	if visited["embedding-api-key"] {
		cfg.EmbeddingAPIKey = *embeddingAPIKey
	}
	if visited["embedding-model"] {
		cfg.EmbeddingModel = *embeddingModel
	}
	if visited["vector-name"] {
		cfg.VectorName = *vectorName
	}
	if visited["embedding-max-length"] {
		cfg.EmbeddingMaxLength = *embeddingMaxLength
	}
	if visited["qdrant-url"] {
		cfg.QdrantURL = *qdrantURL
	}
	if visited["qdrant-api-key"] {
		cfg.QdrantAPIKey = *qdrantAPIKey
	}
	if visited["qdrant-collection"] {
		cfg.QdrantCollection = *qdrantCollection
	}
	if visited["qdrant-vector-size"] {
		cfg.VectorSize = *qdrantVectorSize
	}
	if visited["qdrant-distance"] {
		cfg.Distance = *qdrantDistance
	}
	timeoutValue := *timeout
	if !visited["timeout"] && cfg.EndpointTimeout > 0 {
		timeoutValue = cfg.EndpointTimeout
	}
	endpointValue := *endpoint
	if !visited["endpoint"] && cfg.EndpointURL != "" {
		endpointValue = cfg.EndpointURL
	}
	apiKeyValue := *apiKey
	if !visited["api-key"] && cfg.EndpointAPIKey != "" {
		apiKeyValue = cfg.EndpointAPIKey
	}

	cases, err := troubleshooting.LoadRunbooks(cfg.RunbookDir)
	if err != nil {
		log.Fatalf("failed to load runbooks: %v", err)
	}
	if *dryRun {
		fmt.Printf("loaded %d runbook cases from %s\n", len(cases), cfg.RunbookDir)
		return
	}

	var result *troubleshooting.RunbookUploadResult
	switch *target {
	case "endpoint":
		result, err = troubleshooting.UploadRunbooks(context.Background(), endpointValue, apiKeyValue, timeoutValue, cases)
	case "qdrant":
		result, err = troubleshooting.UploadRunbooksToQdrant(context.Background(), troubleshooting.QdrantUploadConfig{
			URL:                 cfg.QdrantURL,
			APIKey:              cfg.QdrantAPIKey,
			Collection:          cfg.QdrantCollection,
			EmbeddingBaseURL:    cfg.EmbeddingBaseURL,
			EmbeddingAPIKey:     cfg.EmbeddingAPIKey,
			EmbeddingModel:      cfg.EmbeddingModel,
			VectorName:          cfg.VectorName,
			VectorSize:          cfg.VectorSize,
			Distance:            cfg.Distance,
			EmbeddingMaxLength:  cfg.EmbeddingMaxLength,
			NormalizeEmbeddings: cfg.NormalizeEmbeddings,
			CreateIfMissing:     *qdrantCreate,
			TimeoutSeconds:      timeoutValue,
		}, cases)
	default:
		log.Fatalf("unknown target: %s", *target)
	}
	if err != nil {
		log.Fatalf("failed to upload runbooks: %v", err)
	}
	fmt.Printf("uploaded %d runbook cases (%s)\n", result.Uploaded, result.Status)
}

func visitedFlags() map[string]bool {
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}
