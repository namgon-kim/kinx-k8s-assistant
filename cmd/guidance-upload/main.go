package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
)

func main() {
	configFile := flag.String("config", "", "Guidance config YAML file (default: ~/.k8s-assistant/guidance.yaml if present)")
	runbookDir := flag.String("runbook-dir", "", "Directory containing runbook YAML files (required)")
	collection := flag.String("collection", "", "Qdrant collection name (required)")
	target := flag.String("target", "qdrant", "Upload target: qdrant|endpoint")
	endpoint := flag.String("endpoint", "", "Runbook upload endpoint URL")
	apiKey := flag.String("api-key", "", "Upload endpoint bearer token")
	embeddingURL := flag.String("embedding-url", guidance.DefaultEmbeddingBaseURL, "Embedding endpoint base URL")
	embeddingAPIKey := flag.String("embedding-api-key", "", "Embedding endpoint bearer token")
	embeddingModel := flag.String("embedding-model", guidance.DefaultEmbeddingModel, "Embedding model")
	vectorName := flag.String("vector-name", guidance.DefaultVectorName, "Qdrant vector name")
	embeddingMaxLength := flag.Int("embedding-max-length", guidance.DefaultEmbeddingMaxLen, "Embedding max length")
	qdrantURL := flag.String("qdrant-url", guidance.DefaultQdrantURL, "Qdrant base URL, e.g. http://localhost:6333")
	qdrantAPIKey := flag.String("qdrant-api-key", "", "Qdrant API key")
	qdrantVectorSize := flag.Int("qdrant-vector-size", guidance.DefaultVectorSize, "Qdrant vector size")
	qdrantDistance := flag.String("qdrant-distance", guidance.DefaultDistance, "Qdrant vector distance")
	qdrantCreate := flag.Bool("qdrant-create-collection", true, "Create Qdrant collection if missing")
	timeout := flag.Int("timeout", 30, "Upload timeout seconds")
	dryRun := flag.Bool("dry-run", false, "Validate and print loaded runbook count without uploading")
	flag.Parse()

	visited := visitedFlags()
	cfg := guidance.Config{
		RunbookDir:          "",
		EmbeddingModel:      guidance.DefaultEmbeddingModel,
		VectorName:          guidance.DefaultVectorName,
		VectorSize:          guidance.DefaultVectorSize,
		Distance:            guidance.DefaultDistance,
		EmbeddingMaxLength:  guidance.DefaultEmbeddingMaxLen,
		NormalizeEmbeddings: true,
		QdrantURL:           guidance.DefaultQdrantURL,
	}
	if fileCfg, _, err := guidance.LoadOptionalFileConfig(*configFile); err != nil {
		log.Fatalf("failed to load config file: %v", err)
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
	}
	if !visited["runbook-dir"] || *runbookDir == "" {
		log.Fatal("--runbook-dir is required")
	}
	if !visited["collection"] || *collection == "" {
		log.Fatal("--collection is required")
	}
	cfg.RunbookDir = *runbookDir
	cfg.QdrantCollection = *collection
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

	cases, err := guidance.LoadRunbooks(cfg.RunbookDir)
	if err != nil {
		log.Fatalf("failed to load runbooks: %v", err)
	}
	if *dryRun {
		fmt.Printf("loaded %d runbook cases from %s\n", len(cases), cfg.RunbookDir)
		return
	}

	var result *guidance.RunbookUploadResult
	switch *target {
	case "endpoint":
		result, err = guidance.UploadRunbooks(context.Background(), endpointValue, apiKeyValue, timeoutValue, cases)
	case "qdrant":
		result, err = guidance.UploadRunbooksToQdrant(context.Background(), guidance.QdrantUploadConfig{
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
