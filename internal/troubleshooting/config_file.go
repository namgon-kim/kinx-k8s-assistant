package troubleshooting

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type FileConfig struct {
	TroubleShooting TroubleShootingFileConfig `yaml:"trouble_shooting"`
}

type TroubleShootingFileConfig struct {
	Server        ServerFileConfig    `yaml:"server"`
	Runbook       RunbookFileConfig   `yaml:"runbook"`
	RAG           RAGFileConfig       `yaml:"rag"`
	IssueExport   IssueFileConfig     `yaml:"issue_export"`
	KnowledgeBase KnowledgeFileConfig `yaml:"knowledge_base"`
}

type ServerFileConfig struct {
	Port          int   `yaml:"port"`
	ImportOnStart *bool `yaml:"import_on_start"`
}

type RunbookFileConfig struct {
	Dir      string `yaml:"dir"`
	MaxCases int    `yaml:"max_cases"`
}

type KnowledgeFileConfig struct {
	Dir string `yaml:"dir"`
}

type IssueFileConfig struct {
	Dir string `yaml:"dir"`
}

type RAGFileConfig struct {
	Provider  string                `yaml:"provider"`
	Mode      string                `yaml:"mode"`
	Endpoint  RAGEndpointFileConfig `yaml:"endpoint"`
	Embedding EmbeddingFileConfig   `yaml:"embedding"`
	Qdrant    QdrantFileConfig      `yaml:"qdrant"`
	Reranker  RerankerFileConfig    `yaml:"reranker"`
}

type RAGEndpointFileConfig struct {
	URL            string `yaml:"url"`
	APIKey         string `yaml:"api_key"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type EmbeddingFileConfig struct {
	URL                 string `yaml:"url"`
	APIKey              string `yaml:"api_key"`
	Model               string `yaml:"model"`
	VectorName          string `yaml:"vector_name"`
	VectorSize          int    `yaml:"vector_size"`
	Distance            string `yaml:"distance"`
	MaxLength           int    `yaml:"max_length"`
	NormalizeEmbeddings *bool  `yaml:"normalize_embeddings"`
}

type QdrantFileConfig struct {
	URL          string                 `yaml:"url"`
	APIKey       string                 `yaml:"api_key"`
	Collection   string                 `yaml:"collection"`
	Limit        int                    `yaml:"limit"`
	WithPayload  *bool                  `yaml:"with_payload"`
	WithVectors  *bool                  `yaml:"with_vectors"`
	SearchParams QdrantSearchFileConfig `yaml:"search_params"`
}

type QdrantSearchFileConfig struct {
	Exact *bool `yaml:"exact"`
}

type RerankerFileConfig struct {
	Enabled   *bool  `yaml:"enabled"`
	URL       string `yaml:"url"`
	APIKey    string `yaml:"api_key"`
	Model     string `yaml:"model"`
	TopN      int    `yaml:"top_n"`
	MaxLength int    `yaml:"max_length"`
	UseFP16   *bool  `yaml:"use_fp16"`
	Normalize *bool  `yaml:"normalize"`
}

func LoadFileConfig(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg FileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func DefaultFileConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".k8s-assistant", "trouble-shooting.yaml")
	}
	return filepath.Join(home, ".k8s-assistant", "trouble-shooting.yaml")
}

func LoadOptionalFileConfig(path string) (*FileConfig, string, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultFileConfigPath()
	}
	path = expandConfigPath(path)
	cfg, err := LoadFileConfig(path)
	if err == nil {
		return cfg, path, nil
	}
	if os.IsNotExist(err) && !explicit {
		return nil, path, nil
	}
	return nil, path, err
}

func (fc *FileConfig) ApplyToConfig(cfg Config) Config {
	if fc == nil {
		return cfg
	}
	ts := fc.TroubleShooting
	if ts.Runbook.Dir != "" {
		cfg.RunbookDir = expandConfigPath(ts.Runbook.Dir)
	}
	if ts.Runbook.MaxCases > 0 {
		cfg.MaxCases = ts.Runbook.MaxCases
	}
	if ts.IssueExport.Dir != "" {
		cfg.IssueDir = expandConfigPath(ts.IssueExport.Dir)
	}
	if ts.KnowledgeBase.Dir != "" {
		cfg.KnowledgeDir = expandConfigPath(ts.KnowledgeBase.Dir)
	}

	rag := ts.RAG
	if rag.Provider != "" {
		cfg.KnowledgeProvider = KnowledgeProvider(rag.Provider)
	}
	if rag.Mode != "" {
		cfg.SearchMode = SearchMode(rag.Mode)
	}
	if rag.Endpoint.URL != "" {
		cfg.EndpointURL = rag.Endpoint.URL
	}
	if rag.Endpoint.APIKey != "" {
		cfg.EndpointAPIKey = rag.Endpoint.APIKey
	}
	if rag.Endpoint.TimeoutSeconds > 0 {
		cfg.EndpointTimeout = rag.Endpoint.TimeoutSeconds
	}

	if rag.Embedding.Model != "" {
		cfg.EmbeddingModel = rag.Embedding.Model
	}
	if rag.Embedding.URL != "" {
		cfg.EmbeddingBaseURL = rag.Embedding.URL
	}
	if rag.Embedding.APIKey != "" {
		cfg.EmbeddingAPIKey = rag.Embedding.APIKey
	}
	if rag.Embedding.VectorName != "" {
		cfg.VectorName = rag.Embedding.VectorName
	}
	if rag.Embedding.VectorSize > 0 {
		cfg.VectorSize = rag.Embedding.VectorSize
	}
	if rag.Embedding.Distance != "" {
		cfg.Distance = rag.Embedding.Distance
	}
	if rag.Embedding.MaxLength > 0 {
		cfg.EmbeddingMaxLength = rag.Embedding.MaxLength
	}
	if rag.Embedding.NormalizeEmbeddings != nil {
		cfg.NormalizeEmbeddings = *rag.Embedding.NormalizeEmbeddings
	}

	if rag.Qdrant.URL != "" {
		cfg.QdrantURL = rag.Qdrant.URL
	}
	if rag.Qdrant.APIKey != "" {
		cfg.QdrantAPIKey = rag.Qdrant.APIKey
	}
	if rag.Qdrant.Collection != "" {
		cfg.QdrantCollection = rag.Qdrant.Collection
	}
	if rag.Qdrant.Limit > 0 {
		cfg.QdrantLimit = rag.Qdrant.Limit
	}
	if rag.Qdrant.WithPayload != nil {
		cfg.QdrantWithPayload = *rag.Qdrant.WithPayload
	}
	if rag.Qdrant.WithVectors != nil {
		cfg.QdrantWithVectors = *rag.Qdrant.WithVectors
	}
	if rag.Qdrant.SearchParams.Exact != nil {
		cfg.QdrantExact = *rag.Qdrant.SearchParams.Exact
	}

	if rag.Reranker.Model != "" {
		cfg.RerankerModel = rag.Reranker.Model
	}
	if rag.Reranker.Enabled != nil {
		cfg.RerankerEnabled = *rag.Reranker.Enabled
		cfg.RerankerEnabledSet = true
	}
	if rag.Reranker.URL != "" {
		cfg.RerankerBaseURL = rag.Reranker.URL
	}
	if rag.Reranker.APIKey != "" {
		cfg.RerankerAPIKey = rag.Reranker.APIKey
	}
	if rag.Reranker.TopN > 0 {
		cfg.RerankerTopN = rag.Reranker.TopN
	}
	if rag.Reranker.MaxLength > 0 {
		cfg.RerankerMaxLength = rag.Reranker.MaxLength
	}
	if rag.Reranker.UseFP16 != nil {
		cfg.RerankerUseFP16 = *rag.Reranker.UseFP16
	}
	if rag.Reranker.Normalize != nil {
		cfg.RerankerNormalize = *rag.Reranker.Normalize
	}
	return cfg
}

func expandConfigPath(path string) string {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
