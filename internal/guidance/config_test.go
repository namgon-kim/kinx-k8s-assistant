package guidance

import "testing"

func TestApplyDefaultsPreservesExplicitFalseBooleans(t *testing.T) {
	cfg := ApplyDefaults(Config{
		NormalizeEmbeddings:    false,
		NormalizeEmbeddingsSet: true,
		QdrantWithPayload:      false,
		QdrantWithPayloadSet:   true,
		QdrantExact:            false,
		QdrantExactSet:         true,
		RerankerUseFP16:        false,
		RerankerUseFP16Set:     true,
		RerankerNormalize:      false,
		RerankerNormalizeSet:   true,
	})

	if cfg.NormalizeEmbeddings {
		t.Fatal("NormalizeEmbeddings explicit false was overwritten")
	}
	if cfg.QdrantWithPayload {
		t.Fatal("QdrantWithPayload explicit false was overwritten")
	}
	if cfg.QdrantExact {
		t.Fatal("QdrantExact explicit false was overwritten")
	}
	if cfg.RerankerUseFP16 {
		t.Fatal("RerankerUseFP16 explicit false was overwritten")
	}
	if cfg.RerankerNormalize {
		t.Fatal("RerankerNormalize explicit false was overwritten")
	}
}

func TestFileConfigApplyToConfigMarksExplicitFalseBooleans(t *testing.T) {
	no := false
	cfg := (&FileConfig{Guidance: GuidanceFileConfig{RAG: RAGFileConfig{
		Embedding: EmbeddingFileConfig{NormalizeEmbeddings: &no},
		Qdrant: QdrantFileConfig{
			WithPayload: &no,
			SearchParams: QdrantSearchFileConfig{
				Exact: &no,
			},
		},
		Reranker: RerankerFileConfig{
			UseFP16:   &no,
			Normalize: &no,
		},
	}}}).ApplyToConfig(Config{})

	cfg = ApplyDefaults(cfg)
	if cfg.NormalizeEmbeddings || cfg.QdrantWithPayload || cfg.QdrantExact || cfg.RerankerUseFP16 || cfg.RerankerNormalize {
		t.Fatalf("explicit false values were not preserved: %+v", cfg)
	}
}
