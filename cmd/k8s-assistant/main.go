package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/orchestrator"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var version = "dev"

// preloadEnvFromConfig는 config.yaml의 API 키를 env var로 설정 후 재실행합니다.
// gollm이 OPENAI_API_KEY 등을 init()에서 캐시하기 때문에, main() 진입 전에
// env var가 설정되어야 합니다. 재실행된 프로세스에서 gollm init()이 올바른 값을 읽습니다.
func preloadEnvFromConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".k8s-assistant", "config.yaml"))
	if err != nil {
		return
	}

	provider := getRawConfigValue(data, "llmprovider")
	if provider == "" {
		provider = os.Getenv("LLM_PROVIDER")
	}
	if provider == "" {
		provider = "gemini"
	}
	if p := extractFlagValue("--llm-provider"); p != "" {
		provider = p
	}

	changed := false
	switch provider {
	case "openai", "openai-compatible":
		key := firstNonEmpty(extractFlagValue("--api-key"), getRawConfigValue(data, "openai_apikey"))
		ep := firstNonEmpty(extractFlagValue("--endpoint"), getRawConfigValue(data, "openai_endpoint"))
		if setEnvIfMissing("OPENAI_API_KEY", key) {
			changed = true
		}
		if setEnvIfMissing("OPENAI_ENDPOINT", ep) {
			changed = true
		}
	case "anthropic":
		key := firstNonEmpty(extractFlagValue("--api-key"), getRawConfigValue(data, "anthropic_apikey"))
		if setEnvIfMissing("ANTHROPIC_API_KEY", key) {
			changed = true
		}
	}

	if !changed {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		return
	}
	// 재실행: 새 프로세스의 init()이 설정된 env var를 읽습니다
	_ = syscall.Exec(exe, os.Args, os.Environ())
}

// getRawConfigValue는 YAML 파싱 없이 config 파일에서 특정 키의 값을 추출합니다.
func getRawConfigValue(data []byte, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimSpace(line[len(prefix):])
			if idx := strings.Index(val, "#"); idx != -1 {
				val = strings.TrimSpace(val[:idx])
			}
			return strings.Trim(val, `"'`)
		}
	}
	return ""
}

func setEnvIfMissing(key, value string) bool {
	if value == "" || os.Getenv(key) != "" {
		return false
	}
	os.Setenv(key, value)
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func extractFlagValue(flag string) string {
	args := os.Args[1:]
	for i, arg := range args {
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func main() {
	// gollm은 OPENAI_API_KEY 등을 init()에서 캐시합니다 (main() 실행 전).
	// config.yaml의 API 키를 env var로 설정한 뒤 재실행하여 gollm init()이 읽게 합니다.
	preloadEnvFromConfig()

	cfg := config.NewConfig()

	// klog 출력 억제
	setupKlog()

	rootCmd := &cobra.Command{
		Use:   "k8s-assistant [query]",
		Short: "Kubernetes AI 어시스턴트",
		Long:  "자연어로 Kubernetes 클러스터를 조작하고 트러블슈팅하는 AI 어시스턴트입니다.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) > 0 {
				query = args[0]
			}
			return run(cmd.Context(), cfg, query)
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "버전 정보 출력",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("k8s-assistant version: %s\n", version)
		},
	})

	f := rootCmd.Flags()
	f.StringVar(&cfg.LLMProvider, "llm-provider", cfg.LLMProvider,
		"LLM 프로바이더 (openai, gemini, anthropic, ...)")
	f.StringVar(&cfg.Model, "model", cfg.Model,
		"사용할 LLM 모델 (예: gpt-4o, claude-sonnet-4-5, gemini-2.0-flash)")
	f.StringVar(&cfg.APIKey, "api-key", cfg.APIKey,
		"LLM API 키 (환경변수 OPENAI_API_KEY가 우선)")
	f.StringVar(&cfg.Endpoint, "endpoint", cfg.Endpoint,
		"LLM API 엔드포인트 (환경변수 OPENAI_ENDPOINT가 우선)")
	f.StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig,
		"kubeconfig 파일 경로 (기본: ~/.kube/config)")
	f.BoolVar(&cfg.SkipVerifySSL, "skip-verify-ssl", cfg.SkipVerifySSL,
		"LLM 프로바이더 SSL 인증서 검증 생략")
	f.BoolVar(&cfg.EnableToolUseShim, "enable-tool-use-shim", cfg.EnableToolUseShim,
		"tool use shim 활성화 (native function calling 미지원 모델용)")
	f.BoolVar(&cfg.MCPClient, "mcp-client", cfg.MCPClient,
		"MCP 클라이언트 모드 활성화 (log-analyzer 등 외부 MCP 서버 연동)")
	f.IntVar(&cfg.MaxIterations, "max-iterations", cfg.MaxIterations,
		"ReAct 루프 최대 반복 횟수")
	f.BoolVar(&cfg.ShowToolOutput, "show-tool-output", cfg.ShowToolOutput,
		"Tool 실행 결과를 화면에 출력")
	f.StringVar(&cfg.PromptTemplateFile, "prompt-template", cfg.PromptTemplateFile,
		"커스텀 시스템 프롬프트 템플릿 파일 경로 (기본: prompts/system_ko.tmpl)")
	f.StringVar(&cfg.SessionBackend, "session-backend", cfg.SessionBackend,
		"세션 저장 방식 (memory, filesystem)")
	f.StringVar(&cfg.LogFile, "log-file", cfg.LogFile,
		"대화 로그 파일 경로 (미설정 시 로깅 안 함)")

	// Ctrl+C / SIGTERM 처리
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		klog.Infof("Signal received: %v", sig)
		cancel()
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// setupKlog는 klog 콘솔 출력을 억제하고 파일에 로그를 저장합니다
func setupKlog() {
	home, _ := os.UserHomeDir()
	if home == "" {
		klog.SetOutput(io.Discard)
		return
	}

	logDir := filepath.Join(home, ".k8s-assistant", "logs")
	os.MkdirAll(logDir, 0o755)

	logFile := filepath.Join(logDir, "debug.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		klog.SetOutput(io.Discard)
		return
	}

	klog.SetOutput(f)

	// flag를 사용해서 로그 레벨 설정
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	klog.InitFlags(fs)
	fs.Set("logtostderr", "false")
	fs.Set("alsologtostderr", "false")
}

func run(ctx context.Context, cfg *config.Config, initialQuery string) error {
	// 바이너리와 같은 디렉토리의 prompts/system_ko.tmpl 자동 탐색
	if cfg.PromptTemplateFile == "" {
		execPath, err := os.Executable()
		if err == nil {
			candidate := filepath.Join(filepath.Dir(execPath), "..", "prompts", "system_ko.tmpl")
			if _, err := os.Stat(candidate); err == nil {
				cfg.PromptTemplateFile = candidate
			}
		}
	}

	orch, err := orchestrator.New(cfg)
	if err != nil {
		return fmt.Errorf("orchestrator 초기화 실패: %w", err)
	}
	defer orch.Close()

	return orch.Run(ctx, initialQuery)
}
