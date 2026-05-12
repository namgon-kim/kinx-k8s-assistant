package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	kubectlAgent "github.com/GoogleCloudPlatform/kubectl-ai/pkg/agent"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sessions"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// AgentWrapper는 kubectl-ai Agent를 래핑하여
// 생명주기(Init, Run, Close)와 Input/Output 채널을 관리합니다.
type AgentWrapper struct {
	agent    *kubectlAgent.Agent
	cancel   context.CancelFunc
	outputCh chan *api.Message // Output() 호출 시 한 번만 생성
}

// setupProviderEnv는 gollm이 읽을 프로바이더별 환경변수를 설정합니다.
// applyEnvironmentOverrides가 cfg.APIKey/Endpoint에 집약한 값을 env로 내보냅니다.
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

	case "bedrock":
		// AWS SDK 자동 처리
	}

	if cfg.SkipVerifySSL {
		os.Setenv("LLM_SKIP_VERIFY_SSL", "1")
	}
}

// NewAgentWrapper는 Config를 기반으로 AgentWrapper를 생성합니다.
func NewAgentWrapper(cfg *config.Config) (*AgentWrapper, error) {
	ctx := context.Background()

	// 프로바이더별 환경변수 설정
	setupProviderEnv(cfg)
	if cfg.MCPClient {
		if path, err := PrepareKinxMCPClient(); err != nil {
			return nil, fmt.Errorf("MCP 설정 준비 실패: %w", err)
		} else {
			klog.Infof("MCP 설정 준비 완료: %s", path)
		}
	}

	// LLM 클라이언트 생성
	llmClient, err := gollm.NewClient(ctx, cfg.LLMProvider)
	if err != nil {
		return nil, fmt.Errorf("LLM 클라이언트 생성 실패 (%s): %w", cfg.LLMProvider, err)
	}

	// custom-tools (helm, kustomize) 로드
	loadCustomTools()

	a := &kubectlAgent.Agent{
		Model:              cfg.Model,
		Provider:           cfg.LLMProvider,
		Kubeconfig:         cfg.Kubeconfig,
		LLM:                llmClient,
		MaxIterations:      cfg.MaxIterations,
		PromptTemplateFile: cfg.PromptTemplateFile,
		Tools:              tools.Default(),
		SkipPermissions:    false,
		EnableToolUseShim:  cfg.EnableToolUseShim,
		MCPClientEnabled:   cfg.MCPClient,
		SessionBackend:     cfg.SessionBackend,
		RunOnce:            false,
	}

	return &AgentWrapper{agent: a}, nil
}

// Start는 새 세션을 생성하고 Agent ReAct 루프를 시작합니다.
func (w *AgentWrapper) Start(ctx context.Context, initialQuery string) error {
	agentCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	sessionManager, err := sessions.NewSessionManager(w.agent.SessionBackend)
	if err != nil {
		cancel()
		return fmt.Errorf("세션 매니저 생성 실패: %w", err)
	}

	session, err := sessionManager.NewSession(sessions.Metadata{
		ProviderID: w.agent.Provider,
		ModelID:    w.agent.Model,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("세션 생성 실패: %w", err)
	}
	w.agent.Session = session

	w.agent.Input = make(chan any, 1)
	w.agent.Output = make(chan any, 32)

	w.outputCh = make(chan *api.Message, 32)
	go func() {
		defer close(w.outputCh)
		for raw := range w.agent.Output {
			if msg, ok := raw.(*api.Message); ok {
				w.outputCh <- msg
			}
		}
	}()

	if err := w.agent.Init(agentCtx); err != nil {
		cancel()
		return fmt.Errorf("agent 초기화 실패: %w", err)
	}

	go func() {
		if err := w.agent.Run(agentCtx, initialQuery); err != nil {
			klog.Warningf("agent 루프 오류: %v", err)
		}
	}()

	return nil
}

// Output은 Agent Output 채널(*api.Message)을 반환합니다.
func (w *AgentWrapper) Output() <-chan *api.Message {
	return w.outputCh
}

// SendInput은 Agent Input 채널로 입력을 전달합니다.
func (w *AgentWrapper) SendInput(input any) {
	select {
	case w.agent.Input <- input:
	default:
		klog.Warningf("agent input 채널이 가득 찼습니다. 입력 버림: %v", input)
	}
}

// Close는 Agent를 종료하고 리소스를 정리합니다.
func (w *AgentWrapper) Close() {
	if w.cancel != nil {
		w.cancel()
	}
	select {
	case w.agent.Input <- io.EOF:
	default:
	}
	if err := w.agent.Close(); err != nil {
		klog.Warningf("agent 종료 중 오류: %v", err)
	}
}

// loadCustomTools는 유효한 경로에서 custom-tools를 로드합니다.
func loadCustomTools() {
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), ".config", "kubectl-ai", "tools.yaml"),
	}
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		candidates = append(candidates, filepath.Join(xdgConfig, "kubectl-ai", "tools.yaml"))
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			if err := loadAndRegisterCustomTools(p); err != nil {
				klog.Warningf("custom tools 로드 실패 (%s): %v", p, err)
			} else {
				klog.Infof("custom tools 로드 완료: %s", p)
			}
		}
	}
}

// loadAndRegisterCustomTools는 YAML 파일에서 custom tool을 읽어 전역 레지스트리에 등록합니다.
// 이미 등록된 tool은 건너뜁니다.
func loadAndRegisterCustomTools(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var configs []tools.CustomToolConfig
	if err := yaml.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("YAML 파싱 실패: %w", err)
	}

	for _, cfg := range configs {
		if tools.Lookup(cfg.Name) != nil {
			continue
		}
		tool, err := tools.NewCustomTool(cfg)
		if err != nil {
			klog.Warningf("custom tool 생성 실패 (%s): %v", cfg.Name, err)
			continue
		}
		tools.RegisterTool(tool)
	}

	return nil
}
