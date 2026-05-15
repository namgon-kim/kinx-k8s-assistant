package config

import (
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

const LogAnalyzerConfigFileName = "log-analyzer.yaml"

type LogAnalyzerConfig struct {
	Enabled          bool                  `json:"enabled"`
	ArtifactDir      string                `json:"artifact_dir,omitempty"`
	ArtifactTTL      int                   `json:"artifact_ttl,omitempty"`
	MaxArtifactBytes int64                 `json:"max_artifact_bytes,omitempty"`
	File             LogAnalyzerFileConfig `json:"file"`
	Loki             LogAnalyzerHTTPConfig `json:"loki"`
	Prometheus       LogAnalyzerHTTPConfig `json:"prometheus"`
	Grafana          LogAnalyzerHTTPConfig `json:"grafana"`
	OpenSearch       LogAnalyzerHTTPConfig `json:"opensearch"`
}

type LogAnalyzerFileConfig struct {
	Enabled  bool   `json:"enabled"`
	RootDir  string `json:"root_dir,omitempty"`
	MaxLines int    `json:"max_lines,omitempty"`
}

type LogAnalyzerHTTPConfig struct {
	Enabled       bool              `json:"enabled"`
	URL           string            `json:"url,omitempty"`
	Token         string            `json:"token,omitempty"`
	Username      string            `json:"username,omitempty"`
	Password      string            `json:"password,omitempty"`
	OrgID         string            `json:"org_id,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	TLSSkipVerify bool              `json:"tls_skip_verify,omitempty"`
	CAFile        string            `json:"ca_file,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`
	QueryTimeout  int               `json:"query_timeout,omitempty"`
	DefaultLimit  int               `json:"default_limit,omitempty"`
	MaxEntries    int               `json:"max_entries,omitempty"`
	DefaultIndex  string            `json:"default_index,omitempty"`
}

func LoadLogAnalyzerConfig(appDir string, enabled bool) LogAnalyzerConfig {
	cfg := DefaultLogAnalyzerConfig(appDir)
	cfg.Enabled = enabled

	path := LogAnalyzerConfigPath(appDir)
	if data, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(data, &cfg)
		cfg.Enabled = enabled
	}
	normalizeStandaloneLogAnalyzerConfig(&cfg, appDir)
	applyLogAnalyzerEnvironmentOverrides(&cfg)
	return cfg
}

func LogAnalyzerConfigPath(appDir string) string {
	if appDir != "" {
		return filepath.Join(appDir, LogAnalyzerConfigFileName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".k8s-assistant", LogAnalyzerConfigFileName)
	}
	return LogAnalyzerConfigFileName
}

func DefaultLogAnalyzerConfig(appDir string) LogAnalyzerConfig {
	artifactDir := ""
	if appDir != "" {
		artifactDir = filepath.Join(appDir, "artifacts", "loganalyzer")
	}
	return LogAnalyzerConfig{
		Enabled:          true,
		ArtifactDir:      artifactDir,
		ArtifactTTL:      24 * 60 * 60,
		MaxArtifactBytes: 50 * 1024 * 1024,
		File: LogAnalyzerFileConfig{
			Enabled:  true,
			RootDir:  "/var/log/filebeat",
			MaxLines: 1000,
		},
		Loki: LogAnalyzerHTTPConfig{
			Enabled:      true,
			Timeout:      30,
			QueryTimeout: 30,
			DefaultLimit: 1000,
			MaxEntries:   5000,
		},
		Prometheus: LogAnalyzerHTTPConfig{
			Enabled:      true,
			Timeout:      30,
			QueryTimeout: 30,
		},
		Grafana: LogAnalyzerHTTPConfig{
			Enabled:      true,
			Timeout:      30,
			QueryTimeout: 30,
		},
		OpenSearch: LogAnalyzerHTTPConfig{
			Enabled:      true,
			Timeout:      30,
			QueryTimeout: 30,
			DefaultLimit: 100,
			MaxEntries:   1000,
		},
	}
}

func normalizeStandaloneLogAnalyzerConfig(cfg *LogAnalyzerConfig, appDir string) {
	defaults := DefaultLogAnalyzerConfig(appDir)
	if cfg.ArtifactTTL <= 0 {
		cfg.ArtifactTTL = defaults.ArtifactTTL
	}
	if cfg.MaxArtifactBytes <= 0 {
		cfg.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = defaults.ArtifactDir
	} else if home, _ := os.UserHomeDir(); home != "" {
		cfg.ArtifactDir = expandHome(cfg.ArtifactDir, home)
	}
	if cfg.File.RootDir == "" {
		cfg.File.RootDir = defaults.File.RootDir
	} else if home, _ := os.UserHomeDir(); home != "" {
		cfg.File.RootDir = expandHome(cfg.File.RootDir, home)
	}
	if cfg.File.MaxLines <= 0 {
		cfg.File.MaxLines = defaults.File.MaxLines
	}
	normalizeHTTPConfig(&cfg.Loki, defaults.Loki)
	normalizeHTTPConfig(&cfg.Prometheus, defaults.Prometheus)
	normalizeHTTPConfig(&cfg.Grafana, defaults.Grafana)
	normalizeHTTPConfig(&cfg.OpenSearch, defaults.OpenSearch)
}

func normalizeHTTPConfig(cfg *LogAnalyzerHTTPConfig, defaults LogAnalyzerHTTPConfig) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaults.Timeout
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = cfg.Timeout
	}
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = defaults.DefaultLimit
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaults.MaxEntries
	}
	if cfg.CAFile != "" {
		if home, _ := os.UserHomeDir(); home != "" {
			cfg.CAFile = expandHome(cfg.CAFile, home)
		}
	}
}

func applyLogAnalyzerEnvironmentOverrides(cfg *LogAnalyzerConfig) {
	if v := os.Getenv("K8S_ASSISTANT_LOKI_URL"); v != "" {
		cfg.Loki.URL = v
	}
	if v := os.Getenv("K8S_ASSISTANT_LOKI_TOKEN"); v != "" {
		cfg.Loki.Token = v
	}
	if v := os.Getenv("K8S_ASSISTANT_LOKI_USERNAME"); v != "" {
		cfg.Loki.Username = v
	}
	if v := os.Getenv("K8S_ASSISTANT_LOKI_PASSWORD"); v != "" {
		cfg.Loki.Password = v
	}
	if v := os.Getenv("K8S_ASSISTANT_LOKI_ORG_ID"); v != "" {
		cfg.Loki.OrgID = v
	}
	if v := os.Getenv("K8S_ASSISTANT_PROMETHEUS_URL"); v != "" {
		cfg.Prometheus.URL = v
	}
	if v := os.Getenv("K8S_ASSISTANT_PROMETHEUS_TOKEN"); v != "" {
		cfg.Prometheus.Token = v
	}
	if v := os.Getenv("K8S_ASSISTANT_PROMETHEUS_USERNAME"); v != "" {
		cfg.Prometheus.Username = v
	}
	if v := os.Getenv("K8S_ASSISTANT_PROMETHEUS_PASSWORD"); v != "" {
		cfg.Prometheus.Password = v
	}
	if v := os.Getenv("K8S_ASSISTANT_GRAFANA_URL"); v != "" {
		cfg.Grafana.URL = v
	}
	if v := os.Getenv("K8S_ASSISTANT_GRAFANA_TOKEN"); v != "" {
		cfg.Grafana.Token = v
	}
	if v := os.Getenv("K8S_ASSISTANT_GRAFANA_USERNAME"); v != "" {
		cfg.Grafana.Username = v
	}
	if v := os.Getenv("K8S_ASSISTANT_GRAFANA_PASSWORD"); v != "" {
		cfg.Grafana.Password = v
	}
	if v := os.Getenv("K8S_ASSISTANT_GRAFANA_ORG_ID"); v != "" {
		cfg.Grafana.OrgID = v
	}
	if v := os.Getenv("K8S_ASSISTANT_OPENSEARCH_URL"); v != "" {
		cfg.OpenSearch.URL = v
	}
	if v := os.Getenv("K8S_ASSISTANT_OPENSEARCH_TOKEN"); v != "" {
		cfg.OpenSearch.Token = v
	}
	if v := os.Getenv("K8S_ASSISTANT_OPENSEARCH_USERNAME"); v != "" {
		cfg.OpenSearch.Username = v
	}
	if v := os.Getenv("K8S_ASSISTANT_OPENSEARCH_PASSWORD"); v != "" {
		cfg.OpenSearch.Password = v
	}
	if v := os.Getenv("K8S_ASSISTANT_OPENSEARCH_DEFAULT_INDEX"); v != "" {
		cfg.OpenSearch.DefaultIndex = v
	}
}
