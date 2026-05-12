package react

import (
	"os"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
)

func setupProviderEnv(cfg *config.Config) {
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
	case "openai", "openai-compatible":
		if cfg.APIKey != "" {
			os.Setenv("OPENAI_API_KEY", cfg.APIKey)
		}
		if cfg.Endpoint != "" {
			os.Setenv("OPENAI_ENDPOINT", cfg.Endpoint)
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
