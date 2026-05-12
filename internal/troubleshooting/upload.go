package troubleshooting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type RunbookUploadRequest struct {
	Cases []TroubleshootingCase `json:"cases"`
}

type RunbookUploadResult struct {
	Uploaded int    `json:"uploaded"`
	Status   string `json:"status,omitempty"`
}

func UploadRunbooks(ctx context.Context, endpoint, apiKey string, timeoutSeconds int, cases []TroubleshootingCase) (*RunbookUploadResult, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("upload endpoint is required")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}

	body, err := json.Marshal(RunbookUploadRequest{Cases: cases})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upload endpoint returned status %d", resp.StatusCode)
	}

	var result RunbookUploadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &RunbookUploadResult{Uploaded: len(cases), Status: "ok"}, nil
	}
	if result.Uploaded == 0 {
		result.Uploaded = len(cases)
	}
	return &result, nil
}

type QdrantUploadConfig struct {
	URL                 string
	APIKey              string
	Collection          string
	EmbeddingBaseURL    string
	EmbeddingAPIKey     string
	EmbeddingModel      string
	VectorName          string
	VectorSize          int
	Distance            string
	EmbeddingMaxLength  int
	NormalizeEmbeddings bool
	CreateIfMissing     bool
	TimeoutSeconds      int
}

type qdrantVectorConfig struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

func UploadRunbooksToQdrant(ctx context.Context, cfg QdrantUploadConfig, cases []TroubleshootingCase) (*RunbookUploadResult, error) {
	serviceCfg := ApplyDefaults(Config{
		EmbeddingBaseURL:    cfg.EmbeddingBaseURL,
		EmbeddingAPIKey:     cfg.EmbeddingAPIKey,
		EmbeddingModel:      cfg.EmbeddingModel,
		VectorName:          cfg.VectorName,
		VectorSize:          cfg.VectorSize,
		Distance:            cfg.Distance,
		EmbeddingMaxLength:  cfg.EmbeddingMaxLength,
		NormalizeEmbeddings: cfg.NormalizeEmbeddings,
		QdrantURL:           cfg.URL,
		QdrantAPIKey:        cfg.APIKey,
		QdrantCollection:    cfg.Collection,
		EndpointTimeout:     cfg.TimeoutSeconds,
		KnowledgeProvider:   KnowledgeProviderQdrant,
	})
	qdrant := NewQdrantClient(serviceCfg)
	if cfg.CreateIfMissing {
		if err := qdrant.EnsureCollection(ctx, serviceCfg); err != nil {
			return nil, err
		}
	}
	embedder := NewEmbeddingClient(serviceCfg)
	if err := qdrant.UpsertRunbooks(ctx, serviceCfg, cases, embedder); err != nil {
		return nil, err
	}
	return &RunbookUploadResult{Uploaded: len(cases), Status: "qdrant"}, nil
}

func qdrantRequest(ctx context.Context, client *http.Client, method, url, apiKey string, body []byte, allowConflict bool) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict && allowConflict {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant returned status %d", resp.StatusCode)
	}
	return nil
}

func runbookEmbeddingText(c TroubleshootingCase) string {
	return strings.Join([]string{
		c.ID,
		c.Title,
		strings.Join(detectionTypesToStrings(c.MatchTypes), " "),
		strings.Join(c.Symptoms, " "),
		strings.Join(c.EvidenceKeywords, " "),
		c.Cause,
		strings.Join(c.LikelyCauses, " "),
		c.Resolution,
		strings.Join(c.DecisionHints, " "),
		strings.Join(c.RelatedObjects, " "),
		strings.Join(c.Tags, " "),
	}, "\n")
}

func detectionTypesToStrings(types []diagnostic.DetectionType) []string {
	values := make([]string, len(types))
	for i, t := range types {
		values[i] = string(t)
	}
	return values
}
