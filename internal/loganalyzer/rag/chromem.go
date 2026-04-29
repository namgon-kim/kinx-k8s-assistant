package rag

import (
	"context"
	"fmt"

	"github.com/philippgille/chromem-go"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type ChromemStore struct {
	client   *chromem.Client
	embedder Embedder
}

func NewChromemStore(embedder Embedder) (*ChromemStore, error) {
	client := chromem.NewClient()
	return &ChromemStore{
		client:   client,
		embedder: embedder,
	}, nil
}

func (cs *ChromemStore) Index(ctx context.Context, cases []loganalyzer.SimilarCase) error {
	collection, err := cs.client.GetOrCreateCollection(ctx, "troubleshooting", nil)
	if err != nil {
		return fmt.Errorf("failed to get or create collection: %w", err)
	}

	for i, c := range cases {
		combinedText := fmt.Sprintf("제목: %s\n원인: %s\n해결책: %s", c.Title, c.Cause, c.Resolution)

		embedding, err := cs.embedder.Embed(ctx, combinedText)
		if err != nil {
			return fmt.Errorf("failed to embed case %d: %w", i, err)
		}

		id := fmt.Sprintf("case_%d", i)

		metadata := map[string]interface{}{
			"title":      c.Title,
			"cause":      c.Cause,
			"resolution": c.Resolution,
			"source":     c.Source,
		}

		err = collection.Add(ctx, []string{id}, [][]float32{embedding}, nil, []map[string]interface{}{metadata}, []string{combinedText})
		if err != nil {
			return fmt.Errorf("failed to add to collection: %w", err)
		}
	}

	return nil
}

func (cs *ChromemStore) Search(ctx context.Context, query string, maxResults int) ([]loganalyzer.SimilarCase, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	collection, err := cs.client.GetCollection(ctx, "troubleshooting", nil)
	if err != nil {
		// collection doesn't exist yet, return empty results
		return []loganalyzer.SimilarCase{}, nil
	}

	queryEmbedding, err := cs.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	results, err := collection.Query(ctx, [][]float32{queryEmbedding}, int32(maxResults), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	cases := make([]loganalyzer.SimilarCase, 0)

	if len(results.Distances) == 0 || len(results.Distances[0]) == 0 {
		return cases, nil
	}

	for i, distance := range results.Distances[0] {
		if i >= len(results.Metadatas) || i >= len(results.Metadatas[0]) {
			continue
		}

		metadata := results.Metadatas[0][i]

		title, _ := metadata["title"].(string)
		cause, _ := metadata["cause"].(string)
		resolution, _ := metadata["resolution"].(string)
		source, _ := metadata["source"].(string)

		similarity := 1.0 / (1.0 + distance)

		cases = append(cases, loganalyzer.SimilarCase{
			Title:      title,
			Similarity: similarity,
			Cause:      cause,
			Resolution: resolution,
			Source:     source,
		})
	}

	return cases, nil
}

func (cs *ChromemStore) Close() error {
	return cs.client.Close()
}
