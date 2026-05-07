package rag

import (
	"context"
	"fmt"

	"github.com/philippgille/chromem-go"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type ChromemStore struct {
	db       *chromem.DB
	embedder Embedder
}

func NewChromemStore(embedder Embedder) (*ChromemStore, error) {
	return &ChromemStore{
		db:       chromem.NewDB(),
		embedder: embedder,
	}, nil
}

func (cs *ChromemStore) Index(ctx context.Context, cases []loganalyzer.SimilarCase) error {
	collection, err := cs.db.GetOrCreateCollection("troubleshooting", nil, cs.embedder.Embed)
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

		metadata := map[string]string{
			"title":      c.Title,
			"cause":      c.Cause,
			"resolution": c.Resolution,
			"source":     c.Source,
		}

		err = collection.Add(ctx, []string{id}, [][]float32{embedding}, []map[string]string{metadata}, []string{combinedText})
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

	collection := cs.db.GetCollection("troubleshooting", cs.embedder.Embed)
	if collection == nil {
		return []loganalyzer.SimilarCase{}, nil
	}

	results, err := collection.Query(ctx, query, maxResults, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	cases := make([]loganalyzer.SimilarCase, 0)

	for _, result := range results {
		metadata := result.Metadata

		title := metadata["title"]
		cause := metadata["cause"]
		resolution := metadata["resolution"]
		source := metadata["source"]

		cases = append(cases, loganalyzer.SimilarCase{
			Title:      title,
			Similarity: float64(result.Similarity),
			Cause:      cause,
			Resolution: resolution,
			Source:     source,
		})
	}

	return cases, nil
}

func (cs *ChromemStore) Close() error {
	return nil
}
