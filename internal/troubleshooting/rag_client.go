package troubleshooting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type EmbeddingClient struct {
	baseURL string
	apiKey  string
	model   string
	timeout time.Duration
	client  *http.Client
}

type embeddingRequest struct {
	Model               string `json:"model"`
	Input               any    `json:"input"`
	MaxLength           int    `json:"max_length,omitempty"`
	NormalizeEmbeddings bool   `json:"normalize_embeddings,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Embedding []float32 `json:"embedding"`
}

func NewEmbeddingClient(cfg Config) *EmbeddingClient {
	cfg = ApplyDefaults(cfg)
	return &EmbeddingClient{
		baseURL: cfg.EmbeddingBaseURL,
		apiKey:  cfg.EmbeddingAPIKey,
		model:   cfg.EmbeddingModel,
		timeout: time.Duration(cfg.EndpointTimeout) * time.Second,
		client:  &http.Client{Timeout: time.Duration(firstPositive(cfg.EndpointTimeout, 30)) * time.Second},
	}
}

func (c *EmbeddingClient) Embed(ctx context.Context, text string, cfg Config) ([]float32, error) {
	cfg = ApplyDefaults(cfg)
	if c.baseURL == "" {
		return nil, fmt.Errorf("embedding endpoint URL is required")
	}
	body, err := json.Marshal(embeddingRequest{
		Model:               cfg.EmbeddingModel,
		Input:               text,
		MaxLength:           cfg.EmbeddingMaxLength,
		NormalizeEmbeddings: cfg.NormalizeEmbeddings,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding endpoint returned status %d", resp.StatusCode)
	}
	var out embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) > 0 {
		return normalizeVector(out.Data[0].Embedding, cfg.NormalizeEmbeddings), nil
	}
	if len(out.Embedding) > 0 {
		return normalizeVector(out.Embedding, cfg.NormalizeEmbeddings), nil
	}
	return nil, fmt.Errorf("embedding endpoint returned no embedding")
}

type RerankerClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
	MaxLength int      `json:"max_length,omitempty"`
	UseFP16   bool     `json:"use_fp16,omitempty"`
	Normalize bool     `json:"normalize,omitempty"`
}

type rerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

type rerankResponse struct {
	Results []rerankResult `json:"results"`
	Data    []rerankResult `json:"data"`
}

func NewRerankerClient(cfg Config) *RerankerClient {
	cfg = ApplyDefaults(cfg)
	return &RerankerClient{
		baseURL: cfg.RerankerBaseURL,
		apiKey:  cfg.RerankerAPIKey,
		model:   cfg.RerankerModel,
		client:  &http.Client{Timeout: time.Duration(firstPositive(cfg.EndpointTimeout, 30)) * time.Second},
	}
}

func (c *RerankerClient) Rerank(ctx context.Context, query string, cases []TroubleshootingCase, cfg Config) ([]TroubleshootingCase, error) {
	cfg = ApplyDefaults(cfg)
	if len(cases) == 0 {
		return cases, nil
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("reranker endpoint URL is required")
	}
	docs := make([]string, len(cases))
	for i, c := range cases {
		docs[i] = runbookEmbeddingText(c)
	}
	body, err := json.Marshal(rerankRequest{
		Model:     cfg.RerankerModel,
		Query:     query,
		Documents: docs,
		TopN:      cfg.RerankerTopN,
		MaxLength: cfg.RerankerMaxLength,
		UseFP16:   cfg.RerankerUseFP16,
		Normalize: cfg.RerankerNormalize,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("reranker endpoint returned status %d", resp.StatusCode)
	}
	var out rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	results := out.Results
	if len(results) == 0 {
		results = out.Data
	}
	if len(results) == 0 {
		return cases, nil
	}
	reranked := make([]TroubleshootingCase, 0, len(results))
	for _, r := range results {
		if r.Index < 0 || r.Index >= len(cases) {
			continue
		}
		c := cases[r.Index]
		c.Similarity = r.Score
		reranked = append(reranked, c)
	}
	sort.SliceStable(reranked, func(i, j int) bool {
		return reranked[i].Similarity > reranked[j].Similarity
	})
	if cfg.RerankerTopN > 0 && len(reranked) > cfg.RerankerTopN {
		reranked = reranked[:cfg.RerankerTopN]
	}
	return reranked, nil
}

func normalizeVector(values []float32, enabled bool) []float32 {
	if !enabled {
		return values
	}
	var norm float64
	for _, v := range values {
		norm += float64(v * v)
	}
	if norm == 0 {
		return values
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range values {
		values[i] *= scale
	}
	return values
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func truncateText(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
