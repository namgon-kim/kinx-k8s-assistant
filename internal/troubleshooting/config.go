package troubleshooting

import "strings"

const (
	DefaultEmbeddingBaseURL = "http://1.201.177.120:4000"
	DefaultEmbeddingModel   = "bge-m3"
	DefaultVectorName       = "dense"
	DefaultVectorSize       = 1024
	DefaultDistance         = "Cosine"
	DefaultEmbeddingMaxLen  = 1024
	DefaultQdrantURL        = "http://localhost:6333"
	DefaultQdrantCollection = "k8s_troubleshooting_runbooks_v1"
	DefaultQdrantLimit      = 11
	DefaultRerankerBaseURL  = "http://1.201.177.120:4000"
	DefaultRerankerModel    = "bge-reranker-v2-m3"
	DefaultRerankerTopN     = 3
	DefaultRerankerMaxLen   = 1024
)

func ApplyDefaults(cfg Config) Config {
	if cfg.MaxCases <= 0 {
		cfg.MaxCases = 5
	}
	if cfg.SearchMode == "" {
		cfg.SearchMode = SearchModeHybrid
	}
	if cfg.KnowledgeProvider == "" {
		cfg.KnowledgeProvider = KnowledgeProviderLocal
	}
	if cfg.EmbeddingBaseURL == "" {
		cfg.EmbeddingBaseURL = DefaultEmbeddingBaseURL
	}
	cfg.EmbeddingBaseURL = normalizeHTTPBaseURL(cfg.EmbeddingBaseURL)
	if cfg.EmbeddingModel == "" {
		cfg.EmbeddingModel = DefaultEmbeddingModel
	}
	if cfg.VectorName == "" {
		cfg.VectorName = DefaultVectorName
	}
	if cfg.VectorSize <= 0 {
		cfg.VectorSize = DefaultVectorSize
	}
	if cfg.Distance == "" {
		cfg.Distance = DefaultDistance
	}
	if cfg.EmbeddingMaxLength <= 0 {
		cfg.EmbeddingMaxLength = DefaultEmbeddingMaxLen
	}
	cfg.NormalizeEmbeddings = true
	if cfg.QdrantURL == "" {
		cfg.QdrantURL = DefaultQdrantURL
	}
	cfg.QdrantURL = normalizeHTTPBaseURL(cfg.QdrantURL)
	if cfg.QdrantCollection == "" {
		cfg.QdrantCollection = DefaultQdrantCollection
	}
	if cfg.QdrantLimit <= 0 {
		cfg.QdrantLimit = DefaultQdrantLimit
	}
	cfg.QdrantWithPayload = true
	cfg.QdrantExact = true
	if !cfg.RerankerEnabledSet {
		cfg.RerankerEnabled = true
		cfg.RerankerEnabledSet = true
	}
	if cfg.RerankerBaseURL == "" {
		cfg.RerankerBaseURL = DefaultRerankerBaseURL
	}
	cfg.RerankerBaseURL = normalizeHTTPBaseURL(cfg.RerankerBaseURL)
	if cfg.RerankerModel == "" {
		cfg.RerankerModel = DefaultRerankerModel
	}
	if cfg.RerankerTopN <= 0 {
		cfg.RerankerTopN = DefaultRerankerTopN
	}
	if cfg.RerankerMaxLength <= 0 {
		cfg.RerankerMaxLength = DefaultRerankerMaxLen
	}
	cfg.RerankerUseFP16 = true
	cfg.RerankerNormalize = true
	return cfg
}

func normalizeHTTPBaseURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return strings.TrimRight(value, "/")
	}
	return "http://" + strings.TrimRight(value, "/")
}
