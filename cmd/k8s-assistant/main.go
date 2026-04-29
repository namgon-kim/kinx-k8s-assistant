package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/orchestrator"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var version = "dev"

func main() {
	cfg := config.NewConfig()

	// klog를 파일로 리다이렉트 (콘솔 출력 억제)
	setupKlog(cfg.AppDir)

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
	f.StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig,
		"kubeconfig 파일 경로 (기본: KUBECONFIG 환경변수 또는 ~/.kube/config)")
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

// setupKlog는 klog 출력을 ~/.k8s-assistant/logs/에 리다이렉트하고
// 콘솔(stderr) 출력을 억제합니다.
func setupKlog(appDir string) {
	if appDir == "" {
		return
	}
	logDir := filepath.Join(appDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	logFilePath := filepath.Join(logDir, "k8s-assistant-"+time.Now().Format("20060102")+".log")

	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	_ = fs.Set("logtostderr", "false")
	_ = fs.Set("alsologtostderr", "false")
	_ = fs.Set("v", "0")
	_ = fs.Set("log_file", logFilePath)
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
