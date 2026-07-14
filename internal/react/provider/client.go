package provider

import (
	"os"
	"strconv"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

func Setup(cfg *config.Config) {
	clearMainLLMParamEnv()

	switch cfg.LLMProvider {
	case "anthropic":
		if cfg.APIKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", cfg.APIKey)
		}
	case "gemini":
		if cfg.APIKey != "" {
			os.Setenv("GEMINI_API_KEY", cfg.APIKey)
		}
	case "vertexai":
		if cfg.GCPProject != "" {
			os.Setenv("GOOGLE_CLOUD_PROJECT", cfg.GCPProject)
		}
		if cfg.GCPLocation != "" {
			os.Setenv("GOOGLE_CLOUD_LOCATION", cfg.GCPLocation)
		}
	case "openai":
		if cfg.APIKey != "" {
			os.Setenv("OPENAI_API_KEY", cfg.APIKey)
		}
		if cfg.Endpoint != "" {
			os.Setenv("OPENAI_ENDPOINT", cfg.Endpoint)
		}
	case "openai-compatible":
		if cfg.APIKey != "" {
			os.Setenv("OPENAI_API_KEY", cfg.APIKey)
		}
		if cfg.Endpoint != "" {
			os.Setenv("OPENAI_ENDPOINT", cfg.Endpoint)
		}
		os.Setenv("K8S_ASSISTANT_MAIN_TEMPERATURE", strconv.FormatFloat(cfg.Temperature, 'f', -1, 64))
		os.Setenv("K8S_ASSISTANT_MAIN_TOP_P", strconv.FormatFloat(cfg.TopP, 'f', -1, 64))
		if cfg.ReasoningEffort != "" {
			os.Setenv("K8S_ASSISTANT_MAIN_REASONING_EFFORT", cfg.ReasoningEffort)
		}
	case "azopenai":
		if cfg.APIKey != "" {
			os.Setenv("AZURE_OPENAI_API_KEY", cfg.APIKey)
		}
		if cfg.Endpoint != "" {
			os.Setenv("AZURE_OPENAI_ENDPOINT", cfg.Endpoint)
		}
	case "grok":
		if cfg.APIKey != "" {
			os.Setenv("GROK_API_KEY", cfg.APIKey)
		}
		if cfg.Endpoint != "" {
			os.Setenv("GROK_ENDPOINT", cfg.Endpoint)
		}
	case "ollama":
		if cfg.OllamaHost != "" {
			os.Setenv("OLLAMA_HOST", cfg.OllamaHost)
		}
	case "llamacpp":
		if cfg.LlamaCppHost != "" {
			os.Setenv("LLAMACPP_HOST", cfg.LlamaCppHost)
		}
	}

	if cfg.SkipVerifySSL {
		os.Setenv("LLM_SKIP_VERIFY_SSL", "1")
	}
}

func clearMainLLMParamEnv() {
	os.Unsetenv("K8S_ASSISTANT_MAIN_TEMPERATURE")
	os.Unsetenv("K8S_ASSISTANT_MAIN_TOP_P")
	os.Unsetenv("K8S_ASSISTANT_MAIN_REASONING_EFFORT")
}
