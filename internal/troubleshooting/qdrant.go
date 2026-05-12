package troubleshooting

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type QdrantClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func stablePointID(id string) string {
	sum := sha1.Sum([]byte(id))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func NewQdrantClient(cfg Config) *QdrantClient {
	cfg = ApplyDefaults(cfg)
	return &QdrantClient{
		baseURL: strings.TrimRight(cfg.QdrantURL, "/"),
		apiKey:  cfg.QdrantAPIKey,
		client:  &http.Client{Timeout: time.Duration(firstPositive(cfg.EndpointTimeout, 30)) * time.Second},
	}
}

type qdrantNamedCollectionRequest struct {
	Vectors map[string]qdrantVectorConfig `json:"vectors"`
}

type qdrantNamedPoint struct {
	ID      string               `json:"id"`
	Vector  map[string][]float32 `json:"vector"`
	Payload map[string]any       `json:"payload"`
}

type qdrantNamedUpsertRequest struct {
	Points []qdrantNamedPoint `json:"points"`
}

type qdrantSearchRequest struct {
	Vector      qdrantSearchVector `json:"vector"`
	Limit       int                `json:"limit"`
	WithPayload bool               `json:"with_payload"`
	WithVectors bool               `json:"with_vectors"`
	Params      qdrantSearchParams `json:"params,omitempty"`
}

type qdrantSearchVector struct {
	Name   string    `json:"name"`
	Vector []float32 `json:"vector"`
}

type qdrantSearchParams struct {
	Exact bool `json:"exact"`
}

type qdrantSearchResponse struct {
	Result []qdrantScoredPoint `json:"result"`
}

type qdrantScoredPoint struct {
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

func (c *QdrantClient) EnsureCollection(ctx context.Context, cfg Config) error {
	cfg = ApplyDefaults(cfg)
	body, err := json.Marshal(qdrantNamedCollectionRequest{
		Vectors: map[string]qdrantVectorConfig{
			cfg.VectorName: {Size: cfg.VectorSize, Distance: cfg.Distance},
		},
	})
	if err != nil {
		return err
	}
	return qdrantRequest(ctx, c.client, http.MethodPut, c.baseURL+"/collections/"+cfg.QdrantCollection, c.apiKey, body, true)
}

func (c *QdrantClient) UpsertRunbooks(ctx context.Context, cfg Config, cases []TroubleshootingCase, embedder *EmbeddingClient) error {
	cfg = ApplyDefaults(cfg)
	points := make([]qdrantNamedPoint, 0, len(cases))
	for _, runbook := range cases {
		text := truncateText(runbookEmbeddingText(runbook), cfg.EmbeddingMaxLength)
		vector, err := embedder.Embed(ctx, text, cfg)
		if err != nil {
			return fmt.Errorf("embed runbook %s: %w", runbook.ID, err)
		}
		points = append(points, qdrantNamedPoint{
			ID: stablePointID(runbook.ID),
			Vector: map[string][]float32{
				cfg.VectorName: vector,
			},
			Payload: runbookPayload(runbook),
		})
	}
	body, err := json.Marshal(qdrantNamedUpsertRequest{Points: points})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/collections/%s/points?wait=true", c.baseURL, cfg.QdrantCollection)
	return qdrantRequest(ctx, c.client, http.MethodPut, endpoint, c.apiKey, body, false)
}

func (c *QdrantClient) Search(ctx context.Context, cfg Config, queryVector []float32) ([]TroubleshootingCase, error) {
	cfg = ApplyDefaults(cfg)
	body, err := json.Marshal(qdrantSearchRequest{
		Vector:      qdrantSearchVector{Name: cfg.VectorName, Vector: queryVector},
		Limit:       cfg.QdrantLimit,
		WithPayload: cfg.QdrantWithPayload,
		WithVectors: cfg.QdrantWithVectors,
		Params:      qdrantSearchParams{Exact: cfg.QdrantExact},
	})
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/collections/%s/points/search", c.baseURL, cfg.QdrantCollection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("qdrant search returned status %d", resp.StatusCode)
	}
	var out qdrantSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	cases := make([]TroubleshootingCase, 0, len(out.Result))
	for _, point := range out.Result {
		cases = append(cases, payloadToRunbook(point.Payload, point.Score))
	}
	return cases, nil
}

func runbookPayload(c TroubleshootingCase) map[string]any {
	return map[string]any{
		"id":                c.ID,
		"title":             c.Title,
		"match_types":       detectionTypesToStrings(c.MatchTypes),
		"symptoms":          c.Symptoms,
		"evidence_keywords": c.EvidenceKeywords,
		"cause":             c.Cause,
		"likely_causes":     c.LikelyCauses,
		"resolution":        c.Resolution,
		"decision_hints":    c.DecisionHints,
		"related_objects":   c.RelatedObjects,
		"risk_level":        c.RiskLevel,
		"source":            c.Source,
		"tags":              c.Tags,
		"diagnostic_steps":  c.DiagnosticSteps,
		"remediate_steps":   c.RemediateSteps,
		"verify_steps":      c.VerifySteps,
		"rollback_steps":    c.RollbackSteps,
	}
}

func payloadToRunbook(payload map[string]any, score float64) TroubleshootingCase {
	return TroubleshootingCase{
		ID:               stringFromPayload(payload, "id"),
		Title:            stringFromPayload(payload, "title"),
		MatchTypes:       detectionTypesFromPayload(payload, "match_types"),
		Symptoms:         stringSliceFromPayload(payload, "symptoms"),
		EvidenceKeywords: stringSliceFromPayload(payload, "evidence_keywords"),
		Similarity:       score,
		Cause:            stringFromPayload(payload, "cause"),
		LikelyCauses:     stringSliceFromPayload(payload, "likely_causes"),
		Resolution:       stringFromPayload(payload, "resolution"),
		DecisionHints:    stringSliceFromPayload(payload, "decision_hints"),
		RelatedObjects:   stringSliceFromPayload(payload, "related_objects"),
		RiskLevel:        RiskLevel(stringFromPayload(payload, "risk_level")),
		Source:           stringFromPayload(payload, "source"),
		Tags:             stringSliceFromPayload(payload, "tags"),
		DiagnosticSteps:  planStepsFromPayload(payload, "diagnostic_steps"),
		RemediateSteps:   planStepsFromPayload(payload, "remediate_steps"),
		VerifySteps:      planStepsFromPayload(payload, "verify_steps"),
		RollbackSteps:    planStepsFromPayload(payload, "rollback_steps"),
	}
}

func stringFromPayload(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func stringSliceFromPayload(payload map[string]any, key string) []string {
	values, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if s, ok := value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func detectionTypesFromPayload(payload map[string]any, key string) []diagnostic.DetectionType {
	values := stringSliceFromPayload(payload, key)
	out := make([]diagnostic.DetectionType, len(values))
	for i, value := range values {
		out[i] = diagnostic.DetectionType(value)
	}
	return out
}

func planStepsFromPayload(payload map[string]any, key string) []PlanStep {
	values, ok := payload[key]
	if !ok {
		return nil
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	var steps []PlanStep
	if err := json.Unmarshal(body, &steps); err != nil {
		return nil
	}
	return steps
}
