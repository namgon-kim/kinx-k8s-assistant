package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyEnvironmentOverridesAppliesProviderBeforeCredentials(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	cfg := &Config{LLMProvider: "gemini"}
	applyEnvironmentOverrides(cfg)

	if cfg.LLMProvider != "openai" {
		t.Fatalf("LLMProvider = %q, want openai", cfg.LLMProvider)
	}
	if cfg.APIKey != "openai-key" {
		t.Fatalf("APIKey = %q, want openai key", cfg.APIKey)
	}
}

func TestApplyProviderCredentialsRefreshesAfterCLIProviderChange(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	cfg := &Config{LLMProvider: "gemini"}
	ApplyProviderCredentials(cfg)
	if cfg.APIKey != "gemini-key" {
		t.Fatalf("initial APIKey = %q, want gemini key", cfg.APIKey)
	}

	cfg.LLMProvider = "openai"
	ApplyProviderCredentials(cfg)
	if cfg.APIKey != "openai-key" {
		t.Fatalf("refreshed APIKey = %q, want openai key", cfg.APIKey)
	}
}

func TestSaveOmitsRuntimeGenericAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		AppDir:       dir,
		LLMProvider:  "openai",
		Model:        "model",
		APIKey:       "runtime-secret",
		Endpoint:     "https://runtime.example",
		OpenAIAPIKey: "configured-secret",
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "runtime-secret") || strings.Contains(text, "https://runtime.example") {
		t.Fatalf("saved config leaked runtime generic credentials:\n%s", text)
	}
	if !strings.Contains(text, "configured-secret") {
		t.Fatalf("saved config should preserve provider-specific configured key:\n%s", text)
	}
}
