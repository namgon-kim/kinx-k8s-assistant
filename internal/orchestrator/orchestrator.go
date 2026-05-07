package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/chzyer/readline"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/agent"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/k8s"
	"k8s.io/klog/v2"
)

const sessionTimeout = 5 * time.Minute

// loadHistory는 히스토리 파일에서 입력 히스토리를 로드합니다
func loadHistory(historyFile string) []string {
	if historyFile == "" {
		return nil
	}
	data, err := os.ReadFile(historyFile)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var result []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// saveHistory는 입력 히스토리를 파일에 저장합니다
func saveHistory(historyFile string, history []string) error {
	if historyFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(historyFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(historyFile, []byte(strings.Join(history, "\n")), 0o644)
}

// metaCmdCompleter는 readline용 슬래시 명령어 자동완성을 구현합니다.
// readline의 기본 PrefixCompleter는 "/" 포함 이름을 삽입 시 prefix를 제거하는 버그가 있습니다.
// Do()에서 완전한 명령어 이름을 반환하고 length=현재입력길이로 설정합니다.
type metaCmdCompleter struct {
	names []string
}

func (c *metaCmdCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	input := string(line[:pos])
	if !strings.HasPrefix(input, "/") {
		return nil, 0
	}
	for _, name := range c.names {
		if strings.HasPrefix(name, input) {
			newLine = append(newLine, []rune(name))
		}
	}
	return newLine, len([]rune(input))
}

// MetaCmd는 메타 명령 정의입니다
type MetaCmd struct {
	Name        string // 명령어 (예: /config)
	Description string // 설명
}

// GetMetaCommands는 사용 가능한 메타 명령 목록을 반환합니다
func GetMetaCommands() []MetaCmd {
	return []MetaCmd{
		{"/help", "명령어 도움말"},
		{"/config", "현재 설정 표시"},
		{"/kubeconfig", "kubeconfig 파일 설정"},
		{"/kube-context", "Kubernetes 컨텍스트 관리"},
		{"/model", "LLM 모델 변경"},
		{"/save", "현재 설정을 저장"},
	}
}

// filterMetaCommands는 prefix와 매칭되는 메타 명령 목록을 반환합니다
func filterMetaCommands(prefix string) []MetaCmd {
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	metaCmds := GetMetaCommands()
	var filtered []MetaCmd
	for _, cmd := range metaCmds {
		if strings.HasPrefix(cmd.Name, prefix) {
			filtered = append(filtered, cmd)
		}
	}
	return filtered
}

// inputModel은 "/" 입력 시 즉시 라인 형태의 메타 명령 메뉴를 보여주는 bubbletea Model입니다
type inputModel struct {
	textinput    textinput.Model
	selectedIdx  int
	result       string
	history      []string // 입력 히스토리
	historyIdx   int      // 현재 히스토리 인덱스 (-1 = 현재 입력)
	originalText string   // 히스토리 네비게이션 시 원본 입력 보존
	historyFile  string   // 히스토리 파일 경로
	interrupted  bool     // Ctrl+C로 종료 요청
}

func newInputModel(prompt, historyFile string) inputModel {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.Focus()
	ti.Prompt = prompt
	ti.ShowSuggestions = false

	return inputModel{
		textinput:   ti,
		history:     loadHistory(historyFile),
		historyIdx:  -1,
		historyFile: historyFile,
	}
}

func (m inputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		text := m.textinput.Value()
		filtered := filterMetaCommands(text)
		isMeta := len(filtered) > 0

		switch keyMsg.String() {
		case "ctrl+c", "ctrl+d":
			m.interrupted = true
			return m, tea.Quit

		case "up":
			if isMeta && m.selectedIdx > 0 {
				m.selectedIdx--
			} else if !isMeta && len(m.history) > 0 {
				// 히스토리 네비게이션: 과거로 이동
				if m.historyIdx == -1 {
					m.originalText = text
				}
				m.historyIdx++
				if m.historyIdx >= len(m.history) {
					m.historyIdx = len(m.history) - 1
				}
				if m.historyIdx >= 0 {
					m.textinput.SetValue(m.history[len(m.history)-1-m.historyIdx])
				}
			}
			return m, nil

		case "down":
			if isMeta && m.selectedIdx < len(filtered)-1 {
				m.selectedIdx++
			} else if !isMeta && m.historyIdx >= 0 {
				// 히스토리 네비게이션: 최신으로 이동
				m.historyIdx--
				if m.historyIdx < 0 {
					m.textinput.SetValue(m.originalText)
				} else if m.historyIdx >= 0 {
					m.textinput.SetValue(m.history[len(m.history)-1-m.historyIdx])
				}
			}
			return m, nil

		case "enter":
			if isMeta {
				m.selectedIdx = clampSelection(m.selectedIdx, len(filtered))
				m.result = filtered[m.selectedIdx].Name
			} else {
				m.result = text
			}
			return m, tea.Quit
		}
	}

	// 일반 입력은 textinput에 위임
	prevValue := m.textinput.Value()
	var cmd tea.Cmd
	m.textinput, cmd = m.textinput.Update(msg)

	// 텍스트가 변경되면 선택 인덱스를 항상 0으로 초기화
	// (범위 클램프만으로는 /kube→/kubec→/kube 시 잘못된 인덱스 유지 문제 발생)
	newFiltered := filterMetaCommands(m.textinput.Value())
	if m.textinput.Value() != prevValue || m.selectedIdx >= len(newFiltered) {
		m.selectedIdx = 0
	}
	// 텍스트 변경 시 히스토리 인덱스 초기화
	if m.textinput.Value() != m.originalText && m.historyIdx >= 0 {
		m.historyIdx = -1
	}
	return m, cmd
}

func (m inputModel) View() string {
	view := m.textinput.View()

	text := m.textinput.Value()
	filtered := filterMetaCommands(text)
	if len(filtered) > 0 {
		m.selectedIdx = clampSelection(m.selectedIdx, len(filtered))
	}
	for i, cmd := range filtered {
		marker := "  "
		if i == m.selectedIdx {
			marker = "> "
		}
		view += fmt.Sprintf("\n  %s%-15s %s", marker, cmd.Name, cmd.Description)
	}
	if strings.HasPrefix(text, "/") {
		for i := len(filtered); i < len(GetMetaCommands()); i++ {
			view += "\n"
		}
	}

	return view
}

func clampSelection(idx, size int) int {
	if size <= 0 || idx < 0 {
		return 0
	}
	if idx >= size {
		return size - 1
	}
	return idx
}

// getInputWithUI는 bubbletea를 사용해서 사용자 입력을 받습니다
func getInputWithUI(prompt, historyFile string) (string, error) {
	m := newInputModel(prompt, historyFile)
	p, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}

	model := p.(inputModel)
	if model.interrupted {
		return "", io.EOF
	}
	// 히스토리에 추가
	if model.result != "" && model.result != "exit" {
		m.history = append(m.history, model.result)
		saveHistory(historyFile, m.history)
	}
	return model.result, nil
}

// Orchestrator는 kubectl-ai Agent를 래핑하여
// 컨텍스트 관리, 마스킹, 포맷팅, propose/commit 플로우를 처리합니다.
type Orchestrator struct {
	cfg            *config.Config
	agentWrap      *agent.AgentWrapper
	agentInitErr   error // agent 초기화 실패 원인 저장
	ctx            *ConversationContext
	formatter      *Formatter
	logger         *Logger
	rl             *readline.Instance
	kubeconfigInfo *k8s.KubeconfigInfo
}

// New는 새 Orchestrator를 생성하고 초기화합니다.
func New(cfg *config.Config) (*Orchestrator, error) {
	// agent는 나중에 필요할 때 생성 (지연 초기화)
	// 이렇게 하면 config/context 설정 후에 agent를 초기화할 수 있음

	var logger *Logger
	if cfg.LogFile != "" {
		var logErr error
		logger, logErr = NewLogger(cfg.LogFile)
		if logErr != nil {
			klog.Warningf("로그 파일 열기 실패 (%s): %v", cfg.LogFile, logErr)
		}
	}

	// kubeconfig 정보 로드
	var kubeconfigInfo *k8s.KubeconfigInfo
	if cfg.Kubeconfig != "" {
		info, err := k8s.LoadKubeconfigInfo(cfg.Kubeconfig)
		if err != nil {
			klog.Warningf("kubeconfig 로드 실패: %v", err)
		} else {
			kubeconfigInfo = info
			cfg.CurrentContext = info.CurrentContext
			cfg.AvailableContexts = info.Contexts
		}
	}

	// readline prompt: [user@cluster] >>>
	promptPrefix := ">>> "
	if kubeconfigInfo != nil {
		promptPrefix = fmt.Sprintf("[%s] >>> ", k8s.GetPromptPrefix(kubeconfigInfo))
	}

	// 메타 명령 자동완성 구성
	commands := GetMetaCommands()
	cmdNames := make([]string, len(commands))
	for i, cmd := range commands {
		cmdNames[i] = cmd.Name
	}

	// readline 설정: 자동완성 활성화
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          promptPrefix,
		HistoryFile:     cfg.HistoryFile,
		HistoryLimit:    500,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		AutoComplete:    &metaCmdCompleter{names: cmdNames},
	})
	if err != nil {
		return nil, fmt.Errorf("readline 초기화 실패: %w", err)
	}

	return &Orchestrator{
		cfg:            cfg,
		agentWrap:      nil, // 나중에 필요할 때 생성
		ctx:            NewConversationContext(),
		formatter:      NewFormatter(cfg.ShowToolOutput),
		logger:         logger,
		rl:             rl,
		kubeconfigInfo: kubeconfigInfo,
	}, nil
}

// Run은 대화 루프를 시작합니다.
// initialQuery가 비어 있으면 인터랙티브 모드로 동작합니다.
func (o *Orchestrator) Run(ctx context.Context, initialQuery string) error {
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	// 1단계: 배너 출력 (빠름)
	PrintBanner(o.kubeconfigInfo, o.cfg.Kubeconfig)

	// 2단계: agent 생성 (지연 초기화 - 이 시점에서 kubectl-ai 로드)
	klog.Info("agent 초기화 중...")

	// goroutine에서 agent 생성 (블로킹 방지)
	agentCh := make(chan *agent.AgentWrapper)
	errCh := make(chan error)

	go func() {
		klog.Info("→ LLM 클라이언트 로드 중...")
		agentWrap, err := agent.NewAgentWrapper(o.cfg)
		if err != nil {
			errCh <- err
		} else {
			agentCh <- agentWrap
		}
	}()

	// agent 생성 대기
	select {
	case agentWrap := <-agentCh:
		klog.Info("✓ agent 준비 완료")
		o.agentWrap = agentWrap
	case err := <-errCh:
		o.agentInitErr = err
		fmt.Printf("%s✗ agent 생성 실패: %v%s\n", colorBrightRed, err, colorReset)
		fmt.Printf("%s메타 명령만 사용 가능합니다%s\n", colorYellow, colorReset)
		return o.runMetaCommandOnly()
	case <-ctx.Done():
		return fmt.Errorf("agent 생성 중 타임아웃")
	}

	// 3단계: agent 시작
	if err := o.agentWrap.Start(ctx, initialQuery); err != nil {
		klog.Warningf("agent 시작 실패: %v", err)
		return o.runMetaCommandOnly()
	}

	outputCh := o.agentWrap.Output()

	// initialQuery가 없으면 첫 입력 대기 (bubbletea 사용)
	// 재귀 호출 대신 루프 사용: 빈 입력·메타 명령 시 배너 재출력 방지
	for initialQuery == "" {
		o.printStatusWarnings(true)
		promptText := o.buildPrompt(true)

		input, err := getInputWithUI(promptText, o.cfg.HistoryFile)
		if err != nil {
			if err == io.EOF {
				fmt.Println("👋 종료합니다.")
				return nil
			}
			return fmt.Errorf("입력 오류: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if input == "exit" {
			fmt.Println("👋 종료합니다.")
			return nil
		}

		// "/" 입력 시 명령어 실행 후 배너 없이 재프롬프트
		if strings.HasPrefix(input, "/") {
			if err := o.selectMetaCommand(input); err != nil && err != io.EOF {
				fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
			}
			continue
		}

		o.logEntry("user_input", input)
		initialQuery = input
		o.agentWrap.SendInput(&api.UserInputResponse{Query: input})
	}

	// 4단계: 메인 루프 - agent 응답 대기
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n⏱️  세션이 종료되었습니다.")
			return nil

		case msg, ok := <-outputCh:
			if !ok {
				return nil
			}
			if err := o.handleMessage(msg); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}

// runMetaCommandOnly는 agent 없이 메타 명령만 실행합니다.
func (o *Orchestrator) runMetaCommandOnly() error {
	for {
		o.printStatusWarnings(false)
		promptText := o.buildPrompt(false)
		input, err := getInputWithUI(promptText, o.cfg.HistoryFile)
		if err != nil {
			if err == io.EOF {
				fmt.Println("👋 종료합니다.")
				return nil
			}
			return fmt.Errorf("입력 오류: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if input == "exit" {
			fmt.Println("👋 종료합니다.")
			return nil
		}

		// "/" 입력 시 명령어 실행
		if strings.HasPrefix(input, "/") {
			if err := o.selectMetaCommand(input); err != nil && err != io.EOF {
				fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
			}
			continue
		}
		o.showAgentRequirements()
	}
}

// handleMessage는 Agent Output 채널에서 수신한 메시지를 처리합니다.
func (o *Orchestrator) handleMessage(msg *api.Message) error {
	switch msg.Type {

	case api.MessageTypeText:
		text, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		masked := MaskSensitiveData(text)
		PrintMessage(o.formatter.FormatText(masked))
		o.logEntry("response", masked)

	case api.MessageTypeError:
		errText, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		PrintMessage(o.formatter.FormatError(errText))
		o.logEntry("error", errText)

	case api.MessageTypeToolCallRequest:
		desc, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		PrintMessage(o.formatter.FormatToolCall(desc))
		o.logEntry("tool_call", desc)

	case api.MessageTypeToolCallResponse:
		resultStr := fmt.Sprintf("%v", msg.Payload)
		masked := MaskSensitiveData(MaskSecretResource(resultStr))
		refID := o.ctx.AddToolResult("tool", masked)
		PrintMessage(o.formatter.FormatToolResult(masked, refID))
		o.logEntry("tool_result", fmt.Sprintf("[%s] %s", refID, masked))

	case api.MessageTypeUserInputRequest:
		fmt.Println()
		prompt := o.buildPrompt(true) // agent 있음
		o.rl.SetPrompt(prompt)
		input, err := o.rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				o.agentWrap.SendInput(io.EOF)
				fmt.Println("👋 종료합니다.")
				return io.EOF
			}
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		if input == "exit" {
			o.agentWrap.SendInput(io.EOF)
			fmt.Println("👋 종료합니다.")
			return io.EOF
		}

		// 메타 명령어 처리 (/ 로 시작)
		if strings.HasPrefix(input, "/") {
			if err := o.handleMetaCommand(input); err != nil {
				fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
			}
			return nil
		}

		o.logEntry("user_input", input)
		o.agentWrap.SendInput(&api.UserInputResponse{Query: input})

	case api.MessageTypeUserChoiceRequest:
		choiceReq, ok := msg.Payload.(*api.UserChoiceRequest)
		if !ok {
			return nil
		}

		PrintMessage(o.formatter.FormatPropose(choiceReq.Prompt))
		for i, opt := range choiceReq.Options {
			fmt.Printf("  %d. %s\n", i+1, opt.Label)
		}

		o.rl.SetPrompt("실행하시겠습니까? (y/n): ")
		input, err := o.rl.Readline()
		o.rl.SetPrompt(">>> ")
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				o.agentWrap.SendInput(&api.UserChoiceResponse{Choice: 1})
				return nil
			}
			return err
		}

		input = strings.TrimSpace(strings.ToLower(input))
		o.logEntry("user_choice", input)

		choice := 1
		if input == "y" || input == "yes" || input == "예" {
			for i, opt := range choiceReq.Options {
				label := strings.ToLower(opt.Label)
				if strings.Contains(label, "yes") ||
					strings.Contains(label, "confirm") ||
					strings.Contains(label, "실행") {
					choice = i + 1
					break
				}
			}
		}
		o.agentWrap.SendInput(&api.UserChoiceResponse{Choice: choice})
	}

	return nil
}

// Close는 Orchestrator 리소스를 정리합니다.
func (o *Orchestrator) Close() {
	if o.agentWrap != nil {
		o.agentWrap.Close()
	}
	if o.logger != nil {
		o.logger.Close()
	}
	if o.rl != nil {
		o.rl.Close()
	}
}

func (o *Orchestrator) logEntry(kind, content string) {
	if o.logger == nil {
		return
	}
	o.logger.Write(kind, content)
}

// isAPIKeyAvailable은 현재 프로바이더에 맞는 인증 정보가 준비되었는지 확인합니다.
// applyEnvironmentOverrides 이후 cfg.APIKey에 집약되므로 단순 체크로 충분합니다.
func (o *Orchestrator) isAPIKeyAvailable() bool {
	switch o.cfg.LLMProvider {
	case "ollama", "llamacpp", "vertexai", "bedrock":
		return true // API Key 불필요 또는 클라우드 SDK 자동 처리
	default:
		return o.cfg.APIKey != ""
	}
}

// buildPrompt는 현재 상태에 따른 프롬프트를 생성합니다
func (o *Orchestrator) buildPrompt(hasAgent bool) string {
	contextPart := "none"
	if o.kubeconfigInfo != nil {
		contextPart = o.kubeconfigInfo.CurrentContext
	}

	statusSymbol := "⚠️ "
	if hasAgent && o.kubeconfigInfo != nil && o.isAPIKeyAvailable() {
		statusSymbol = "✓"
	}

	return fmt.Sprintf("%s[%s|%s]%s >>> ", colorBrightCyan, contextPart, statusSymbol, colorReset)
}

// printStatusWarnings는 상태가 정상이 아닐 때 원인을 출력합니다
func (o *Orchestrator) printStatusWarnings(hasAgent bool) {
	var issues []string

	if !o.isAPIKeyAvailable() {
		switch o.cfg.LLMProvider {
		case "anthropic":
			issues = append(issues, "ANTHROPIC_API_KEY 미설정 (또는 config.yaml: anthropic_apikey)")
		case "gemini":
			issues = append(issues, "GEMINI_API_KEY 미설정 (또는 config.yaml: gemini_apikey)")
		case "openai", "openai-compatible":
			issues = append(issues, "OPENAI_API_KEY 미설정 (또는 config.yaml: openai_apikey)")
		case "azopenai":
			issues = append(issues, "AZURE_OPENAI_API_KEY 미설정 (또는 config.yaml: azopenai_apikey)")
		case "grok":
			issues = append(issues, "GROK_API_KEY 미설정 (또는 config.yaml: grok_apikey)")
		default:
			issues = append(issues, fmt.Sprintf("%s API Key 미설정", o.cfg.LLMProvider))
		}
	}
	if o.cfg.Kubeconfig == "" {
		issues = append(issues, "Kubeconfig 미설정 (/kubeconfig로 설정)")
	} else if o.kubeconfigInfo == nil {
		issues = append(issues, fmt.Sprintf("Kubeconfig 로드 실패: %s", o.cfg.Kubeconfig))
	}
	if !hasAgent {
		issues = append(issues, "Agent 초기화 실패")
	}

	if len(issues) > 0 {
		fmt.Printf("  %s⚠️  %s%s\n", colorYellow, strings.Join(issues, " | "), colorReset)
	}
}

// showAgentRequirements는 agent가 없을 때 필요한 설정을 알려줍니다
func (o *Orchestrator) showAgentRequirements() {
	fmt.Println()
	fmt.Printf("%s⚠️  Agent가 준비되지 않았습니다%s\n", colorBrightRed, colorReset)
	fmt.Println()

	fmt.Printf("%s필요한 설정:%s\n", colorYellow, colorReset)

	// API KEY 확인
	if o.cfg.APIKey == "" {
		fmt.Printf("  %s❌ API Key%s - OPENAI_API_KEY 환경변수 또는 /kubeconfig 명령으로 설정\n", colorBrightRed, colorReset)
	} else {
		fmt.Printf("  %s✓ API Key%s\n", colorBrightGreen, colorReset)
	}

	// Kubeconfig 확인
	if o.cfg.Kubeconfig == "" {
		fmt.Printf("  %s❌ Kubeconfig%s - /kubeconfig 명령으로 설정\n", colorBrightRed, colorReset)
	} else if o.kubeconfigInfo == nil {
		fmt.Printf("  %s⚠️  Kubeconfig%s - 파일을 찾을 수 없음\n", colorYellow, colorReset)
	} else {
		fmt.Printf("  %s✓ Kubeconfig%s - %s\n", colorBrightGreen, colorReset, o.cfg.Kubeconfig)
	}

	fmt.Println()
	fmt.Printf("%s다음 메타 명령으로 설정하세요:%s\n", colorBrightCyan, colorReset)
	fmt.Printf("  %s/kubeconfig%s - Kubeconfig 파일 경로 설정\n", colorBrightCyan, colorReset)
	fmt.Println()
}

// selectMetaCommand는 메타 명령을 실행합니다
func (o *Orchestrator) selectMetaCommand(input string) error {
	return o.handleMetaCommand(input)
}

// showMetaCommandMenu는 메타 명령 선택 메뉴를 표시합니다
func (o *Orchestrator) showMetaCommandMenu() {
	fmt.Println()
	fmt.Printf("%s메타 명령:%s\n", colorBrightCyan, colorReset)

	metaCmds := GetMetaCommands()
	for i, c := range metaCmds {
		fmt.Printf("  %s%d%s. %s%-20s%s %s\n", colorYellow, i+1, colorReset, colorBrightCyan, c.Name, colorReset, c.Description)
	}
	fmt.Println()

	maxNum := len(metaCmds)
	o.rl.SetPrompt(fmt.Sprintf("선택 (1-%d 또는 명령어 입력): ", maxNum))
	choice, err := o.rl.Readline()
	if err != nil {
		return
	}

	choice = strings.TrimSpace(choice)
	var cmd string

	// 숫자 선택 처리
	if num, err := strconv.Atoi(choice); err == nil {
		if num >= 1 && num <= maxNum {
			cmd = metaCmds[num-1].Name
		} else {
			fmt.Printf("%s❌ 범위를 벗어난 선택%s\n", colorBrightRed, colorReset)
			return
		}
	} else {
		// 직접 입력한 명령어 처리
		if strings.HasPrefix(choice, "/") {
			cmd = choice
		} else {
			fmt.Printf("%s❌ 잘못된 선택%s\n", colorBrightRed, colorReset)
			return
		}
	}

	if err := o.handleMetaCommand(cmd); err != nil {
		fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
	}

	// 원래 프롬프트로 복원 (agent 있는지 없는지 판단)
	hasAgent := o.agentWrap != nil
	prompt := o.buildPrompt(hasAgent)
	o.rl.SetPrompt(prompt)
}

// handleMetaCommand는 /config, /kube-context 등 메타 명령어를 처리합니다.
func (o *Orchestrator) handleMetaCommand(input string) error {
	parts := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(parts) == 0 {
		return fmt.Errorf("알 수 없는 명령어")
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "help":
		o.printHelp()
		return nil

	case "config":
		o.printConfig()
		return nil

	case "kube-context":
		if len(args) == 0 {
			return o.selectContextInteractively()
		}
		subCmd := args[0]
		switch subCmd {
		case "list":
			return o.listContexts()
		case "current":
			return o.printCurrentContext()
		case "switch":
			if len(args) < 2 {
				return fmt.Errorf("사용법: /kube-context switch <context-name>")
			}
			return o.switchContext(args[1])
		default:
			return fmt.Errorf("알 수 없는 subcommand: %s", subCmd)
		}

	case "kubeconfig":
		if len(args) == 0 {
			return o.setKubeconfigInteractively()
		}
		return o.setKubeconfig(strings.Join(args, " "))

	case "model":
		if len(args) == 0 {
			return o.selectModelInteractively()
		}
		return o.setModel(strings.Join(args, " "))

	case "save":
		if err := o.cfg.Save(); err != nil {
			return fmt.Errorf("설정 저장 실패: %w", err)
		}
		fmt.Printf("%s✓ 설정이 저장되었습니다%s\n", colorBrightGreen, colorReset)
		return nil

	default:
		return fmt.Errorf("알 수 없는 명령어: /%s", cmd)
	}
}

// printHelp는 사용 가능한 메타 명령과 일반 사용법을 출력합니다.
func (o *Orchestrator) printHelp() {
	fmt.Println()
	fmt.Printf("%s=== K8s-Assistant 도움말 ===%s\n", colorBrightCyan, colorReset)
	fmt.Println()
	fmt.Printf("%s메타 명령어:%s\n", colorYellow, colorReset)
	for _, cmd := range GetMetaCommands() {
		fmt.Printf("  %s%-20s%s %s\n", colorBrightCyan, cmd.Name, colorReset, cmd.Description)
	}
	fmt.Println()
	fmt.Printf("%s일반 사용법:%s\n", colorYellow, colorReset)
	fmt.Printf("  자연어로 Kubernetes 작업을 입력하세요\n")
	fmt.Printf("  예) \"현재 실행 중인 pod 목록 보여줘\"\n")
	fmt.Printf("  예) \"nginx deployment를 3개로 스케일링해줘\"\n")
	fmt.Println()
	fmt.Printf("%s종료:%s exit 또는 Ctrl+C\n", colorYellow, colorReset)
	fmt.Println()
}

// printConfig는 현재 설정을 출력합니다.
func (o *Orchestrator) printConfig() {
	fmt.Println()
	fmt.Printf("%s=== K8s-Assistant 설정 ===%s\n", colorBrightCyan, colorReset)
	fmt.Printf("  LLM Provider: %s\n", o.cfg.LLMProvider)
	fmt.Printf("  Model: %s\n", o.cfg.Model)
	fmt.Printf("  Kubeconfig: %s\n", o.cfg.Kubeconfig)
	if o.kubeconfigInfo != nil {
		fmt.Printf("  Current Context: %s\n", o.kubeconfigInfo.CurrentContext)
		fmt.Printf("  Available Contexts: %d개\n", len(o.kubeconfigInfo.Contexts))
	}
	fmt.Printf("  Session Backend: %s\n", o.cfg.SessionBackend)
	fmt.Printf("  Max Iterations: %d\n", o.cfg.MaxIterations)
	fmt.Println()
}

// listContexts는 사용 가능한 kubeconfig contexts를 나열합니다.
func (o *Orchestrator) listContexts() error {
	if o.kubeconfigInfo == nil {
		return fmt.Errorf("kubeconfig 정보 로드되지 않음")
	}

	fmt.Println()
	fmt.Printf("%s=== Kubeconfig Contexts ===%s\n", colorBrightCyan, colorReset)
	current := o.kubeconfigInfo.CurrentContext
	for i, ctx := range o.kubeconfigInfo.Contexts {
		prefix := "  "
		if ctx == current {
			prefix = fmt.Sprintf("%s* %s", colorYellow, colorReset)
		} else {
			prefix = "  "
		}
		fmt.Printf("%s %d. %s\n", prefix, i+1, ctx)
	}
	fmt.Println()
	return nil
}

// printCurrentContext는 현재 context를 출력합니다.
func (o *Orchestrator) printCurrentContext() error {
	if o.kubeconfigInfo == nil {
		return fmt.Errorf("kubeconfig 정보 로드되지 않음")
	}
	fmt.Println()
	fmt.Printf("%sCurrent Context: %s%s\n", colorYellow, o.kubeconfigInfo.CurrentContext, colorReset)
	if o.kubeconfigInfo.UserName != "" {
		fmt.Printf("  User: %s\n", o.kubeconfigInfo.UserName)
	}
	if o.kubeconfigInfo.ClusterName != "" {
		fmt.Printf("  Cluster: %s\n", o.kubeconfigInfo.ClusterName)
	}
	fmt.Println()
	return nil
}

// switchContext는 kubeconfig context를 변경합니다.
func (o *Orchestrator) switchContext(contextName string) error {
	if o.kubeconfigInfo == nil {
		return fmt.Errorf("kubeconfig 정보 로드되지 않음")
	}

	if o.cfg.Kubeconfig == "" {
		return fmt.Errorf("kubeconfig 경로 설정되지 않음")
	}

	if err := k8s.SwitchContext(o.cfg.Kubeconfig, contextName); err != nil {
		return err
	}

	// kubeconfig 정보 다시 로드
	info, err := k8s.LoadKubeconfigInfo(o.cfg.Kubeconfig)
	if err != nil {
		return err
	}

	o.kubeconfigInfo = info
	o.cfg.CurrentContext = info.CurrentContext

	// prompt 업데이트
	newPrompt := o.buildPrompt(o.agentWrap != nil)
	o.rl.SetPrompt(newPrompt)

	fmt.Println()
	fmt.Printf("%s✓ Context 변경됨: %s%s\n", colorBrightGreen, info.CurrentContext, colorReset)
	fmt.Println()

	return nil
}

// selectContextInteractively는 대화형으로 context를 선택합니다.
func (o *Orchestrator) selectContextInteractively() error {
	if o.kubeconfigInfo == nil {
		return fmt.Errorf("kubeconfig 정보 로드되지 않음")
	}

	fmt.Println()
	fmt.Printf("%s=== Kubeconfig Contexts ===%s\n", colorBrightCyan, colorReset)
	current := o.kubeconfigInfo.CurrentContext
	for i, ctx := range o.kubeconfigInfo.Contexts {
		prefix := "  "
		if ctx == current {
			prefix = fmt.Sprintf("%s* %s", colorYellow, colorReset)
		}
		fmt.Printf("%s %d. %s\n", prefix, i+1, ctx)
	}

	o.rl.SetPrompt("선택 (1-" + strconv.Itoa(len(o.kubeconfigInfo.Contexts)) + ", q: 취소): ")
	input, err := o.rl.Readline()
	o.rl.SetPrompt(">>> ")
	if err != nil {
		if err == readline.ErrInterrupt || err == io.EOF {
			return nil
		}
		return err
	}

	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" || input == "q" || input == "cancel" {
		fmt.Println()
		return nil
	}

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(o.kubeconfigInfo.Contexts) {
		return fmt.Errorf("유효하지 않은 선택: %s", input)
	}

	selectedContext := o.kubeconfigInfo.Contexts[choice-1]
	if selectedContext == current {
		fmt.Println()
		fmt.Printf("%s이미 선택된 context입니다: %s%s\n", colorYellow, selectedContext, colorReset)
		fmt.Println()
		return nil
	}

	return o.switchContext(selectedContext)
}

// setKubeconfigInteractively는 대화형으로 kubeconfig 경로를 입력받습니다.
func (o *Orchestrator) setKubeconfigInteractively() error {
	fmt.Println()
	fmt.Printf("%sKubeconfig 경로를 입력하세요 (기본값: ~/.kube/config):%s\n", colorBrightCyan, colorReset)
	o.rl.SetPrompt("경로: ")
	input, err := o.rl.Readline()
	o.rl.SetPrompt(">>> ")
	if err != nil {
		if err == readline.ErrInterrupt || err == io.EOF {
			return nil
		}
		return err
	}

	input = strings.TrimSpace(input)
	if input == "" {
		fmt.Printf("%s💡 kubeconfig 설정이 건너뛰어졌습니다%s\n", colorYellow, colorReset)
		return nil
	}

	return o.setKubeconfig(input)
}

// setKubeconfig는 kubeconfig 경로를 설정합니다.
func (o *Orchestrator) setKubeconfig(kubeconfigPath string) error {
	// 경로 확장 (~ 사용 가능)
	expandedPath := kubeconfigPath
	if strings.HasPrefix(kubeconfigPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("홈 디렉토리 조회 실패: %w", err)
		}
		expandedPath = filepath.Join(home, kubeconfigPath[1:])
	}

	// 파일 존재 확인
	if _, err := os.Stat(expandedPath); err != nil {
		fmt.Println()
		fmt.Printf("%s✗ 파일을 찾을 수 없습니다: %s%s\n", colorBrightRed, expandedPath, colorReset)
		fmt.Println()
		return nil
	}

	// kubeconfig 정보 다시 로드
	info, err := k8s.LoadKubeconfigInfo(expandedPath)
	if err != nil {
		fmt.Println()
		fmt.Printf("%s✗ kubeconfig 로드 실패: %v%s\n", colorBrightRed, err, colorReset)
		fmt.Println()
		return nil
	}

	// 설정 업데이트
	o.cfg.Kubeconfig = expandedPath
	o.kubeconfigInfo = info
	o.cfg.CurrentContext = info.CurrentContext
	o.cfg.AvailableContexts = info.Contexts

	fmt.Printf("%s✓ kubeconfig 설정 완료%s\n", colorBrightGreen, colorReset)
	fmt.Printf("%s💾 /save 명령으로 설정을 저장하세요%s\n", colorYellow, colorReset)

	fmt.Println()
	fmt.Printf("%s✓ kubeconfig 설정됨: %s%s\n", colorBrightGreen, expandedPath, colorReset)
	fmt.Printf("  Context: %s%s%s\n", colorBrightMagentaBg, info.CurrentContext, colorReset)
	fmt.Println()

	return nil
}

// setModel은 LLM 모델을 변경합니다.
func (o *Orchestrator) setModel(modelName string) error {
	if modelName == "" {
		return fmt.Errorf("모델 이름이 비어있습니다")
	}
	o.cfg.Model = modelName
	fmt.Println()
	fmt.Printf("%s✓ 모델이 변경되었습니다: %s%s\n", colorBrightGreen, modelName, colorReset)
	fmt.Printf("%s💾 /save 명령으로 설정을 저장하세요%s\n", colorYellow, colorReset)
	fmt.Println()
	return nil
}

// selectModelInteractively는 대화형으로 모델을 선택합니다.
func (o *Orchestrator) selectModelInteractively() error {
	fmt.Println()
	fmt.Printf("%s현재 모델: %s%s\n", colorBrightCyan, o.cfg.Model, colorReset)
	fmt.Printf("%s새 모델 이름을 입력하세요 (현재 provider: %s):%s\n", colorBrightCyan, o.cfg.LLMProvider, colorReset)
	o.rl.SetPrompt("모델: ")
	input, err := o.rl.Readline()
	o.rl.SetPrompt(">>> ")
	if err != nil {
		if err == readline.ErrInterrupt || err == io.EOF {
			fmt.Println()
			return nil
		}
		return err
	}

	input = strings.TrimSpace(input)
	if input == "" {
		fmt.Printf("%s💡 모델 변경이 취소되었습니다%s\n", colorYellow, colorReset)
		fmt.Println()
		return nil
	}

	return o.setModel(input)
}
