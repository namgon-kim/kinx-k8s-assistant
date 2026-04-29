package orchestrator

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/chzyer/readline"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/agent"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"k8s.io/klog/v2"
)

const sessionTimeout = 5 * time.Minute

// Orchestrator는 kubectl-ai Agent를 래핑하여
// 컨텍스트 관리, 마스킹, 포맷팅, propose/commit 플로우를 처리합니다.
type Orchestrator struct {
	cfg       *config.Config
	agentWrap *agent.AgentWrapper
	ctx       *ConversationContext
	formatter *Formatter
	logger    *Logger
	rl        *readline.Instance
}

// New는 새 Orchestrator를 생성하고 초기화합니다.
func New(cfg *config.Config) (*Orchestrator, error) {
	agentWrap, err := agent.NewAgentWrapper(cfg)
	if err != nil {
		return nil, fmt.Errorf("agent 생성 실패: %w", err)
	}

	var logger *Logger
	if cfg.LogFile != "" {
		var logErr error
		logger, logErr = NewLogger(cfg.LogFile)
		if logErr != nil {
			klog.Warningf("로그 파일 열기 실패 (%s): %v", cfg.LogFile, logErr)
		}
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          ">>> ",
		HistoryFile:     cfg.HistoryFile,
		HistoryLimit:    500,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return nil, fmt.Errorf("readline 초기화 실패: %w", err)
	}

	return &Orchestrator{
		cfg:       cfg,
		agentWrap: agentWrap,
		ctx:       NewConversationContext(),
		formatter: NewFormatter(cfg.ShowToolOutput),
		logger:    logger,
		rl:        rl,
	}, nil
}

// Run은 대화 루프를 시작합니다.
// initialQuery가 비어 있으면 인터랙티브 모드로 동작합니다.
func (o *Orchestrator) Run(ctx context.Context, initialQuery string) error {
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	if err := o.agentWrap.Start(ctx, initialQuery); err != nil {
		return fmt.Errorf("agent 시작 실패: %w", err)
	}

	printBanner()

	outputCh := o.agentWrap.Output()

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
		o.rl.SetPrompt(">>> ")
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
		if input == "exit" || input == "quit" {
			o.agentWrap.SendInput(io.EOF)
			fmt.Println("👋 종료합니다.")
			return io.EOF
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
	o.agentWrap.Close()
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
