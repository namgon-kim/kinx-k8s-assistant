package react

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

type langTranslator struct {
	language string
	model    string
	endpoint string
	apiKey   string
	client   *http.Client
}

func newLangTranslator(cfg *config.Config) *langTranslator {
	lang := strings.TrimSpace(cfg.Lang.Language)
	if strings.EqualFold(lang, "English") {
		return nil
	}
	model := strings.TrimSpace(cfg.Lang.Model)
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Lang.Endpoint), "/")
	if model == "" || endpoint == "" {
		return nil
	}
	return &langTranslator{
		language: lang,
		model:    model,
		endpoint: endpoint,
		apiKey:   cfg.Lang.APIKey,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *langTranslator) enabled() bool {
	return t != nil && t.model != "" && t.endpoint != "" && !strings.EqualFold(t.language, "English")
}

func (t *langTranslator) translate(ctx context.Context, text string) (string, error) {
	if !t.enabled() {
		return text, nil
	}
	reqBody := openAIChatCompletionRequest{
		Model: t.model,
		Messages: []openAIChatMessage{
			{
				Role:    "system",
				Content: "You are a precise Kubernetes operations translator. Translate the entire input into Korean. Preserve every fact, sentence, bullet, order, and level of detail. Do not summarize, omit, shorten, add commentary, or answer the content. Keep Kubernetes resource names, namespaces, commands, flags, JSON/YAML, field names, and raw command output unchanged. Return only the translated text without markdown fences.",
			},
			{
				Role:    "user",
				Content: text,
			},
		},
		Temperature: 0,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(t.endpoint), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(t.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(t.apiKey))
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("lang endpoint returned status %d", resp.StatusCode)
	}

	var decoded openAIChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("lang endpoint returned no choices")
	}
	return decoded.Choices[0].Message.Content, nil
}

func chatCompletionsURL(endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(base, "/v1/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

type openAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
}
