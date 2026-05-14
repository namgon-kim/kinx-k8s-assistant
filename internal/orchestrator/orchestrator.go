package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/chzyer/readline"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/k8s"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
	"k8s.io/klog/v2"
)

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
		{"/lang", "출력 언어 조회/변경"},
		{"/readonly", "read-only 모드 조회/변경"},
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
	historyIdx   int      // 현재 히스토리 인덱스 (len(history) = 현재 입력)
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
	history := loadHistory(historyFile)

	return inputModel{
		textinput:   ti,
		history:     history,
		historyIdx:  len(history),
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
				if m.historyIdx == len(m.history) {
					m.originalText = text
				}
				if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.textinput.SetValue(m.history[m.historyIdx])
			}
			return m, nil

		case "down":
			if isMeta && m.selectedIdx < len(filtered)-1 {
				m.selectedIdx++
			} else if !isMeta && len(m.history) > 0 && m.historyIdx < len(m.history) {
				m.historyIdx++
				if m.historyIdx == len(m.history) {
					m.textinput.SetValue(m.originalText)
				} else {
					m.textinput.SetValue(m.history[m.historyIdx])
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
	if m.historyIdx < len(m.history) && m.textinput.Value() != m.history[m.historyIdx] {
		m.historyIdx = len(m.history)
		m.originalText = m.textinput.Value()
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

func getInputWithUI(prompt, historyFile string) (string, error) {
	return getInputWithUIHistory(prompt, historyFile, true)
}

// getInputWithUI는 bubbletea를 사용해서 사용자 입력을 받습니다
func getInputWithUIHistory(prompt, historyFile string, saveToHistory bool) (string, error) {
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
	if saveToHistory && model.result != "" && model.result != "exit" {
		m.history = append(m.history, model.result)
		saveHistory(historyFile, m.history)
	}
	return model.result, nil
}

func getInputWithUIEcho(prompt, historyFile string) (string, error) {
	return getInputWithUIEchoHistory(prompt, historyFile, true)
}

func getInputWithUIEchoNoHistory(prompt, historyFile string) (string, error) {
	return getInputWithUIEchoHistory(prompt, "", false)
}

func getInputWithUIEchoHistory(prompt, historyFile string, saveToHistory bool) (string, error) {
	input, err := getInputWithUIHistory(prompt, historyFile, saveToHistory)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(input) != "" {
		fmt.Printf("%s%s\n", prompt, input)
	}
	return input, nil
}

// Orchestrator는 k8s-assistant ReAct loop와 사용자 입력/출력 UX를 연결하고,
// 컨텍스트 관리, 마스킹, 포맷팅, propose/commit 플로우를 처리합니다.
type Orchestrator struct {
	cfg             *config.Config
	agentWrap       *react.Loop
	outputCh        <-chan *api.Message
	agentInitErr    error // agent 초기화 실패 원인 저장
	ctx             *ConversationContext
	troubleshooting *TroubleshootingFlow
	formatter       *Formatter
	logger          *Logger
	rl              *readline.Instance
	kubeconfigInfo  *k8s.KubeconfigInfo
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
		cfg:             cfg,
		agentWrap:       nil, // 나중에 필요할 때 생성
		ctx:             NewConversationContext(),
		troubleshooting: NewTroubleshootingFlow(),
		formatter:       NewFormatter(cfg.ShowToolOutput),
		logger:          logger,
		rl:              rl,
		kubeconfigInfo:  kubeconfigInfo,
	}, nil
}

// Run은 대화 루프를 시작합니다.
// initialQuery가 비어 있으면 인터랙티브 모드로 동작합니다.
func (o *Orchestrator) Run(ctx context.Context, initialQuery string) error {
	PrintBanner(o.kubeconfigInfo, o.cfg.Kubeconfig)
	if o.cfg.MCPClient {
		if path, err := toolconnector.PrepareKinxMCPClient(); err != nil {
			return fmt.Errorf("MCP 클라이언트 준비 실패: %w", err)
		} else {
			klog.Infof("MCP 설정 준비 완료: %s", path)
		}
	}

	initialQuery = strings.TrimSpace(initialQuery)
	if initialQuery != "" {
		if inputIsQuit(initialQuery) {
			fmt.Println("종료는 exit 또는 Ctrl+C를 사용하세요.")
			return nil
		}
		if err := o.startAgent(ctx, initialQuery); err != nil {
			return err
		}
	}

	for {
		if o.agentWrap == nil {
			if err := o.readAndDispatchInput(ctx); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			continue
		}

		select {
		case <-ctx.Done():
			fmt.Println("\n👋 종료합니다.")
			return nil

		case msg, ok := <-o.outputCh:
			if !ok {
				o.clearAgent()
				continue
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

func (o *Orchestrator) readAndDispatchInput(ctx context.Context) error {
	o.printStatusWarnings(true)
	input, err := getInputWithUIEcho(o.buildPrompt(true), o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			fmt.Println("👋 종료합니다.")
			return io.EOF
		}
		return fmt.Errorf("입력 오류: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	if input == "exit" {
		fmt.Println("👋 종료합니다.")
		return io.EOF
	}
	if inputIsQuit(input) {
		fmt.Println("종료는 exit 또는 Ctrl+C를 사용하세요.")
		return nil
	}
	if strings.HasPrefix(input, "/") {
		if err := o.selectMetaCommand(input); err != nil && err != io.EOF {
			fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
		}
		return nil
	}

	if err := o.ensureReadyForAgent(); err != nil {
		fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
		o.showAgentRequirements()
		return nil
	}

	o.logEntry("user_input", input)
	o.troubleshooting.ObserveUserInput(input)
	return o.startAgent(ctx, input)
}

func (o *Orchestrator) startAgent(ctx context.Context, initialQuery string) error {
	if err := o.ensureReadyForAgent(); err != nil {
		return err
	}
	if o.agentWrap != nil {
		return nil
	}
	o.troubleshooting.ObserveUserInput(initialQuery)

	klog.Info("agent 초기화 중...", "kubeconfig", o.cfg.Kubeconfig, "context", o.cfg.CurrentContext)
	agentWrap, err := react.New(o.cfg)
	if err != nil {
		o.agentInitErr = err
		return fmt.Errorf("react loop 생성 실패: %w", err)
	}
	if err := agentWrap.Start(ctx, initialQuery); err != nil {
		agentWrap.Close()
		o.agentInitErr = err
		return fmt.Errorf("react loop 시작 실패: %w", err)
	}

	o.agentWrap = agentWrap
	o.outputCh = agentWrap.Output()
	o.agentInitErr = nil
	klog.Info("agent 준비 완료", "kubeconfig", o.cfg.Kubeconfig, "context", o.cfg.CurrentContext)
	return nil
}

func (o *Orchestrator) ensureReadyForAgent() error {
	if !o.isAPIKeyAvailable() {
		return fmt.Errorf("%s API Key가 설정되지 않았습니다", o.cfg.LLMProvider)
	}
	if o.cfg.Kubeconfig == "" {
		return fmt.Errorf("kubeconfig가 설정되지 않았습니다. /kubeconfig로 설정하세요")
	}
	if o.kubeconfigInfo == nil {
		return fmt.Errorf("kubeconfig를 로드할 수 없습니다: %s", o.cfg.Kubeconfig)
	}
	return nil
}

func (o *Orchestrator) invalidateAgent(reason string) {
	if o.agentWrap == nil {
		return
	}
	klog.Info("agent invalidated", "reason", reason)
	o.agentWrap.Close()
	o.clearAgent()
}

func (o *Orchestrator) clearAgent() {
	o.agentWrap = nil
	o.outputCh = nil
}

// handleMessage는 Agent Output 채널에서 수신한 메시지를 처리합니다.
func (o *Orchestrator) handleMessage(msg *api.Message) error {
	switch msg.Type {

	case api.MessageTypeText:
		text, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		if msg.Source == api.MessageSourceUser {
			return nil
		}
		masked := MaskSensitiveData(sanitizeDisplayText(text))
		PrintMessage(o.formatter.FormatText(masked))
		o.logEntry("response", masked)
		return o.troubleshooting.AfterAgentText(o, masked)

	case api.MessageTypeError:
		errText, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		errText = sanitizeDisplayText(errText)
		PrintMessage(o.formatter.FormatError(errText))
		o.logEntry("error", errText)
		return o.troubleshooting.AfterAgentText(o, errText)

	case api.MessageTypeToolCallRequest:
		desc, ok := msg.Payload.(string)
		if !ok {
			return nil
		}
		PrintMessage(o.formatter.FormatToolCall(desc))
		o.logEntry("tool_call", desc)

	case api.MessageTypeToolCallResponse:
		resultStr := sanitizeDisplayText(fmt.Sprintf("%v", msg.Payload))
		masked := MaskSensitiveData(MaskSecretResource(resultStr))
		refID := o.ctx.AddToolResult("tool", masked)
		PrintMessage(o.formatter.FormatToolResult(masked, refID))
		o.logEntry("tool_result", fmt.Sprintf("[%s] %s", refID, masked))
		o.troubleshooting.RecordEvidence(masked)

	case api.MessageTypeUserInputRequest:
		return o.handleAgentInputRequest()

	case api.MessageTypeUserChoiceRequest:
		choiceReq, ok := msg.Payload.(*api.UserChoiceRequest)
		if !ok {
			return nil
		}
		return o.handleAgentChoiceRequest(choiceReq)
	}

	return nil
}

func (o *Orchestrator) handleAgentInputRequest() error {
	activeAgent := o.agentWrap
	if activeAgent == nil {
		return nil
	}
	if handled, err := o.troubleshooting.BeforeUserInput(o, activeAgent); handled || err != nil {
		return err
	}

	o.printStatusWarnings(true)
	input, err := getInputWithUIEcho(o.buildPrompt(true), o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			activeAgent.SendInput(io.EOF)
			fmt.Println("👋 종료합니다.")
			return io.EOF
		}
		return fmt.Errorf("입력 오류: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "exit" {
		activeAgent.SendInput(io.EOF)
		fmt.Println("👋 종료합니다.")
		return io.EOF
	}
	if inputIsQuit(input) {
		fmt.Println("종료는 exit 또는 Ctrl+C를 사용하세요.")
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return nil
	}
	if strings.HasPrefix(input, "/") {
		if err := o.selectMetaCommand(input); err != nil && err != io.EOF {
			fmt.Println(colorBrightMagenta + "❌ " + err.Error() + colorReset)
		}
		if o.agentWrap == activeAgent {
			activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		}
		return nil
	}

	o.logEntry("user_input", input)
	o.troubleshooting.ObserveUserInput(input)
	activeAgent.SendInput(&api.UserInputResponse{Query: input})
	return nil
}

func (o *Orchestrator) handleAgentChoiceRequest(choiceReq *api.UserChoiceRequest) error {
	activeAgent := o.agentWrap
	if activeAgent == nil {
		return nil
	}

	PrintMessage(o.formatter.FormatPropose(choiceReq.Prompt))
	for i, opt := range choiceReq.Options {
		fmt.Printf("  %d. %s\n", i+1, opt.Label)
	}

	for {
		input, err := getInputWithUIEchoNoHistory("선택 (번호): ", o.cfg.HistoryFile)
		if err != nil {
			if err == io.EOF {
				activeAgent.SendInput(io.EOF)
				fmt.Println("👋 종료합니다.")
				return io.EOF
			}
			return fmt.Errorf("입력 오류: %w", err)
		}

		input = strings.TrimSpace(strings.ToLower(input))
		if input == "exit" {
			activeAgent.SendInput(io.EOF)
			fmt.Println("👋 종료합니다.")
			return io.EOF
		}
		if inputIsQuit(input) {
			fmt.Println("종료는 exit 또는 Ctrl+C를 사용하세요.")
			continue
		}
		if input == "y" || input == "yes" || input == "예" {
			input = "1"
		}
		if input == "n" || input == "no" || input == "아니오" {
			input = strconv.Itoa(findChoiceIndex(choiceReq.Options, "no", len(choiceReq.Options)))
		}

		choice, err := strconv.Atoi(input)
		if err == nil && choice >= 1 && choice <= len(choiceReq.Options) {
			o.logEntry("user_choice", input)
			activeAgent.SendInput(&api.UserChoiceResponse{Choice: choice})
			return nil
		}

		fmt.Println(colorBrightMagenta + "❌ 유효하지 않은 선택입니다" + colorReset)
	}
}

func findChoiceIndex(options []api.UserChoiceOption, value string, fallback int) int {
	for i, opt := range options {
		if strings.EqualFold(opt.Value, value) {
			return i + 1
		}
	}
	if fallback >= 1 && fallback <= len(options) {
		return fallback
	}
	return 1
}

func inputIsQuit(input string) bool {
	return strings.EqualFold(strings.TrimSpace(input), "quit")
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
	if o.kubeconfigInfo != nil && o.isAPIKeyAvailable() {
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

	case "lang":
		if len(args) == 0 || args[0] == "status" {
			o.printLangStatus()
			return nil
		}
		return o.setLang(args[0])

	case "readonly":
		if len(args) == 0 || args[0] == "status" {
			o.printReadOnlyStatus()
			return nil
		}
		return o.setReadOnly(args[0])

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
	fmt.Printf("%sread-only:%s /readonly on|off|status\n", colorYellow, colorReset)
	fmt.Printf("%s언어:%s /lang Korean|English|status\n", colorYellow, colorReset)
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
	fmt.Printf("  Language: %s\n", o.cfg.Lang.Language)
	if o.cfg.Lang.Model != "" {
		fmt.Printf("  Lang Model: openai-compatible/%s\n", o.cfg.Lang.Model)
	}
	fmt.Printf("  Read Only: %t\n", o.cfg.ReadOnly)
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
	o.cfg.AvailableContexts = info.Contexts
	o.invalidateAgent("kube-context changed")

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
	o.invalidateAgent("kubeconfig changed")

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
	o.invalidateAgent("model changed")
	fmt.Println()
	fmt.Printf("%s✓ 모델이 변경되었습니다: %s%s\n", colorBrightGreen, modelName, colorReset)
	fmt.Printf("%s💾 /save 명령으로 설정을 저장하세요%s\n", colorYellow, colorReset)
	fmt.Println()
	return nil
}

func (o *Orchestrator) printLangStatus() {
	fmt.Println()
	fmt.Printf("%slanguage: %s%s\n", colorBrightCyan, o.cfg.Lang.Language, colorReset)
	if strings.EqualFold(o.cfg.Lang.Language, "Korean") {
		if o.cfg.Lang.Model != "" && o.cfg.Lang.Endpoint != "" {
			fmt.Printf("  번역 모델: openai-compatible/%s (%s)\n", o.cfg.Lang.Model, o.cfg.Lang.Endpoint)
		} else {
			fmt.Println("  번역 모델이 설정되지 않아 primary model 출력 언어 정책을 사용합니다.")
		}
	}
	fmt.Println()
}

func (o *Orchestrator) setLang(value string) error {
	normalized := strings.ToLower(strings.TrimSpace(value))
	var language string
	switch normalized {
	case "korean", "ko":
		language = "Korean"
	case "english", "en":
		language = "English"
	default:
		return fmt.Errorf("사용법: /lang Korean|English|status")
	}

	if o.cfg.Lang.Language == language {
		o.printLangStatus()
		return nil
	}

	o.cfg.Lang.Language = language
	o.invalidateAgent("language changed")
	o.printLangStatus()
	fmt.Printf("%s💾 /save 명령으로 설정을 저장하세요%s\n", colorYellow, colorReset)
	fmt.Println()
	return nil
}

func (o *Orchestrator) printReadOnlyStatus() {
	status := "off"
	if o.cfg.ReadOnly {
		status = "on"
	}
	fmt.Println()
	fmt.Printf("%sread-only: %s%s\n", colorBrightCyan, status, colorReset)
	if o.cfg.ReadOnly {
		fmt.Println("  Kubernetes 리소스 변경 명령은 차단됩니다.")
	} else {
		fmt.Println("  Kubernetes 리소스 변경 명령은 기존 승인 흐름을 따릅니다.")
	}
	fmt.Println()
}

func (o *Orchestrator) setReadOnly(value string) error {
	normalized := strings.ToLower(strings.TrimSpace(value))
	var enabled bool
	switch normalized {
	case "on", "true", "yes", "y", "1", "enable", "enabled":
		enabled = true
	case "off", "false", "no", "n", "0", "disable", "disabled":
		enabled = false
	default:
		return fmt.Errorf("사용법: /readonly on|off|status")
	}

	if o.cfg.ReadOnly == enabled {
		o.printReadOnlyStatus()
		return nil
	}

	o.cfg.ReadOnly = enabled
	o.invalidateAgent("read-only mode changed")
	o.printReadOnlyStatus()
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
