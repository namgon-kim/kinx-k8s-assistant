package troubleshooting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type EndpointClient struct {
	url     string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

func NewEndpointClient(url, apiKey string, timeoutSeconds int) *EndpointClient {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	return &EndpointClient{
		url:     url,
		apiKey:  apiKey,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *EndpointClient) Search(ctx context.Context, req TroubleshootingSearchRequest) (*TroubleshootingSearchResult, error) {
	if c == nil || c.url == "" {
		return nil, fmt.Errorf("RAG endpoint is not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("RAG endpoint returned status %d", resp.StatusCode)
	}

	var result TroubleshootingSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.SearchMode == "" {
		result.SearchMode = SearchModeEndpoint
	}
	return &result, nil
}
