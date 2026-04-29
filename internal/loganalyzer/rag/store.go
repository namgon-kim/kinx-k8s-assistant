package rag

import (
	"context"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type VectorStore interface {
	Index(ctx context.Context, cases []loganalyzer.SimilarCase) error
	Search(ctx context.Context, query string, maxResults int) ([]loganalyzer.SimilarCase, error)
}
