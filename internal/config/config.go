package config

import (
	"os"
	"path/filepath"
)

// Config는 k8s-assistant 전체 설정을 담습니다.
type Config struct {
	// LLM 설정
	LLMProvider string
	Model       string

	// kubectl-ai Agent 설정
	Kubeconfig         string
	SkipVerifySSL      bool
	EnableToolUseShim  bool
	MCPClient          bool
	MaxIterations      int
	ShowToolOutput     bool
	PromptTemplateFile string
	SessionBackend     string

	// 앱 디렉토리 (~/.k8s-assistant)
	AppDir      string
	HistoryFile string

	// 대화 로그
	LogFile string
}

// NewConfig는 기본값이 설정된 Config를 반환합니다.
func NewConfig() *Config {
	kubeconfig := os.Getenv("KUBECONFIG")

	home, _ := os.UserHomeDir()

	if kubeconfig == "" && home != "" {
		candidate := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(candidate); err == nil {
			kubeconfig = candidate
		}
	}

	appDir := ""
	historyFile := ""
	if home != "" {
		appDir = filepath.Join(home, ".k8s-assistant")
		historyFile = filepath.Join(appDir, "history")
	}

	return &Config{
		LLMProvider:    "openai",
		Model:          "",
		Kubeconfig:     kubeconfig,
		MaxIterations:  20,
		SessionBackend: "memory",
		AppDir:         appDir,
		HistoryFile:    historyFile,
	}
}
