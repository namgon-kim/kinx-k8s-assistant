package rag

import (
	"context"
	"fmt"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type OpenAIEmbedder struct {
	client *openai.Client
	model  string
}

func NewOpenAIEmbedder(apiKey string, model string) *OpenAIEmbedder {
	if model == "" {
		model = "text-embedding-3-small"
	}
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &OpenAIEmbedder{
		client: client,
		model:  model,
	}
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.F[openai.EmbeddingNewParamsInputUnion](
			openai.EmbeddingNewParamsInputUnionString(text),
		),
		Model: openai.F(e.model),
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI embedding failed: %w", err)
	}

	if len(embeddings.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return embeddings.Data[0].Embedding, nil
}

func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	embeddings, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.F[openai.EmbeddingNewParamsInputUnion](
			openai.EmbeddingNewParamsInputUnionArrayOfString(texts),
		),
		Model: openai.F(e.model),
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI batch embedding failed: %w", err)
	}

	result := make([][]float32, len(embeddings.Data))
	for i, emb := range embeddings.Data {
		result[i] = emb.Embedding
	}
	return result, nil
}
