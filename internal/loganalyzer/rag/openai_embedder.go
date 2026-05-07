package rag

import (
	"context"
	"fmt"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

type OpenAIEmbedder struct {
	client openai.Client
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
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: param.NewOpt(text),
		},
		Model: e.model,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI embedding failed: %w", err)
	}

	if len(embeddings.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return float64ToFloat32(embeddings.Data[0].Embedding), nil
}

func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	embeddings, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: texts,
		},
		Model: e.model,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI batch embedding failed: %w", err)
	}

	result := make([][]float32, len(embeddings.Data))
	for i, emb := range embeddings.Data {
		result[i] = float64ToFloat32(emb.Embedding)
	}
	return result, nil
}

func float64ToFloat32(values []float64) []float32 {
	converted := make([]float32, len(values))
	for i, v := range values {
		converted[i] = float32(v)
	}
	return converted
}
