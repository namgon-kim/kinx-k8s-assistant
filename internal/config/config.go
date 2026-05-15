package config

import (
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Config는 k8s-assistant 전체 설정을 담습니다.
type Config struct {
	// LLM 설정
	LLMProvider string `json:"llmprovider"`
	Model       string `json:"model"`
	APIKey      string `json:"apikey"`
	Endpoint    string `json:"endpoint"`

	// Provider별 인증 (환경변수가 config 값보다 우선)
	AnthropicAPIKey     string `json:"anthropic_apikey,omitempty"`
	GeminiAPIKey        string `json:"gemini_apikey,omitempty"`
	OpenAIAPIKey        string `json:"openai_apikey,omitempty"`
	OpenAIEndpoint      string `json:"openai_endpoint,omitempty"`
	AzureOpenAIAPIKey   string `json:"azopenai_apikey,omitempty"`
	AzureOpenAIEndpoint string `json:"azopenai_endpoint,omitempty"`
	GrokAPIKey          string `json:"grok_apikey,omitempty"`
	OllamaHost          string `json:"ollama_host,omitempty"`
	LlamaCppHost        string `json:"llamacpp_host,omitempty"`
	GCPProject          string `json:"gcp_project,omitempty"`
	GCPLocation         string `json:"gcp_location,omitempty"`

	// ReAct loop 설정
	Kubeconfig         string   `json:"kubeconfig"`
	CurrentContext     string   `json:"-"`
	AvailableContexts  []string `json:"-"`
	SkipVerifySSL      bool     `json:"skipverifyssl"`
	EnableToolUseShim  bool     `json:"enabletoolshim"`
	MCPClient          bool     `json:"mcp_client"`
	MaxIterations      int      `json:"maxiterations"`
	ShowToolOutput     bool     `json:"showtooloutput"`
	ReadOnly           bool     `json:"readonly"`
	PromptTemplateFile string   `json:"prompttemplatefile,omitempty"`
	SessionBackend     string   `json:"sessionbackend"`

	// 앱 디렉토리 (~/.k8s-assistant)
	AppDir      string `json:"-"`
	HistoryFile string `json:"-"`

	// 대화 로그
	LogFile string `json:"logfile,omitempty"`

	// 시스템 로그
	SystemLogDir  string `json:"systemlogdir,omitempty"`
	LogLevel      int    `json:"loglevel"`
	ShowLogOutput bool   `json:"showlogoutput"`

	// 사용자 출력 언어/번역 설정
	Lang LangConfig `json:"lang"`

	// 로그/메트릭 분석 설정
	LogAnalyzer LogAnalyzerToggle `json:"log_analyzer"`
}

type LangConfig struct {
	Language string `json:"language"`
	Model    string `json:"model,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"apikey,omitempty"`
}

type LogAnalyzerToggle struct {
	Enabled bool `json:"enabled"`
}

// NewConfig는 기본값이 설정된 Config를 반환합니다.
// ~/.k8s-assistant/config.yaml 파일이 있으면 로드합니다.
func NewConfig() *Config {
	cfg := &Config{
		LLMProvider:    "gemini",
		Model:          "gemini-2.0-flash",
		MaxIterations:  20,
		SessionBackend: "memory",
		LogLevel:       0,
		Lang: LangConfig{
			Language: "English",
		},
		LogAnalyzer: LogAnalyzerToggle{Enabled: true},
	}

	home, _ := os.UserHomeDir()

	// 앱 디렉토리 설정
	if home != "" {
		cfg.AppDir = filepath.Join(home, ".k8s-assistant")
		cfg.HistoryFile = filepath.Join(cfg.AppDir, "history")
		cfg.SystemLogDir = filepath.Join(cfg.AppDir, "logs")

		// Config 파일 로드 시도
		configFile := filepath.Join(cfg.AppDir, "config.yaml")
		if data, err := os.ReadFile(configFile); err == nil {
			if err := yaml.Unmarshal(data, cfg); err == nil {
				// ~ 경로 확장
				if cfg.Kubeconfig != "" {
					cfg.Kubeconfig = expandHome(cfg.Kubeconfig, home)
				} else {
					cfg.Kubeconfig = filepath.Join(home, ".kube", "config")
				}
				if cfg.SystemLogDir == "" {
					cfg.SystemLogDir = filepath.Join(cfg.AppDir, "logs")
				} else {
					cfg.SystemLogDir = expandHome(cfg.SystemLogDir, home)
				}
				normalizeLangConfig(cfg)
				applyEnvironmentOverrides(cfg)
				return cfg
			}
		}
	}

	// config.yaml이 없는 경우, 기본값 설정
	if home != "" {
		candidate := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(candidate); err == nil {
			cfg.Kubeconfig = candidate
		}
	}
	normalizeLangConfig(cfg)
	applyEnvironmentOverrides(cfg)

	return cfg
}

func normalizeLangConfig(cfg *Config) {
	switch strings.ToLower(strings.TrimSpace(cfg.Lang.Language)) {
	case "english", "en":
		cfg.Lang.Language = "English"
	case "korean", "ko":
		cfg.Lang.Language = "Korean"
	case "":
		cfg.Lang.Language = "English"
	default:
		cfg.Lang.Language = strings.TrimSpace(cfg.Lang.Language)
	}
}

// expandHome은 경로에서 ~ 를 홈 디렉토리로 확장합니다
func expandHome(path, home string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		return home
	}
	return path
}

// applyEnvironmentOverrides는 환경변수 > config.yaml 필드 > 기본값 순으로 설정을 적용합니다.
// 결과는 cfg.APIKey / cfg.Endpoint 로 집약되어 setupProviderEnv가 읽어 간다.
func applyEnvironmentOverrides(cfg *Config) {
	switch cfg.LLMProvider {
	case "anthropic":
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if cfg.AnthropicAPIKey != "" {
			cfg.APIKey = cfg.AnthropicAPIKey
		}

	case "gemini":
		if v := os.Getenv("GEMINI_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if v := os.Getenv("GOOGLE_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if cfg.GeminiAPIKey != "" {
			cfg.APIKey = cfg.GeminiAPIKey
		}

	case "vertexai":
		if v := os.Getenv("GOOGLE_CLOUD_PROJECT"); v != "" {
			cfg.GCPProject = v
		}
		if v := os.Getenv("GOOGLE_CLOUD_LOCATION"); v != "" {
			cfg.GCPLocation = v
		}

	case "openai", "openai-compatible":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if cfg.OpenAIAPIKey != "" {
			cfg.APIKey = cfg.OpenAIAPIKey
		}
		if v := os.Getenv("OPENAI_ENDPOINT"); v != "" {
			cfg.Endpoint = v
		} else if v := os.Getenv("OPENAI_API_BASE"); v != "" {
			cfg.Endpoint = v
		} else if cfg.OpenAIEndpoint != "" {
			cfg.Endpoint = cfg.OpenAIEndpoint
		}

	case "azopenai":
		if v := os.Getenv("AZURE_OPENAI_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if cfg.AzureOpenAIAPIKey != "" {
			cfg.APIKey = cfg.AzureOpenAIAPIKey
		}
		if v := os.Getenv("AZURE_OPENAI_ENDPOINT"); v != "" {
			cfg.Endpoint = v
		} else if cfg.AzureOpenAIEndpoint != "" {
			cfg.Endpoint = cfg.AzureOpenAIEndpoint
		}

	case "grok":
		if v := os.Getenv("GROK_API_KEY"); v != "" {
			cfg.APIKey = v
		} else if cfg.GrokAPIKey != "" {
			cfg.APIKey = cfg.GrokAPIKey
		}
		if v := os.Getenv("GROK_ENDPOINT"); v != "" {
			cfg.Endpoint = v
		}

	case "ollama":
		if v := os.Getenv("OLLAMA_HOST"); v != "" {
			cfg.OllamaHost = v
		}

	case "llamacpp":
		if v := os.Getenv("LLAMACPP_HOST"); v != "" {
			cfg.LlamaCppHost = v
		}

	case "bedrock":
		// AWS SDK 자동 처리
	}

	if v := os.Getenv("LLM_PROVIDER"); v != "" {
		cfg.LLMProvider = v
	}
	if v := os.Getenv("MODEL"); v != "" {
		cfg.Model = v
	}
}

// Save는 현재 설정을 ~/.k8s-assistant/config.yaml에 저장합니다.
func (c *Config) Save() error {
	if c.AppDir == "" {
		return nil // 저장 불가
	}

	if err := os.MkdirAll(c.AppDir, 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	configFile := filepath.Join(c.AppDir, "config.yaml")
	return os.WriteFile(configFile, data, 0o600)
}
