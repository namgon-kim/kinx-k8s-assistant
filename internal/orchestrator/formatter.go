package orchestrator

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
)

// MessageKind는 출력 메시지의 종류를 나타냅니다.
type MessageKind int

const (
	KindText       MessageKind = iota // 일반 에이전트 응답
	KindError                         // 에러
	KindToolCall                      // Tool 호출 알림
	KindToolResult                    // Tool 결과
	KindPropose                       // 변경 제안 (propose)
	KindPrompt                        // 사용자 입력 프롬프트
)

// FormattedMessage는 CLI 출력을 위해 포맷된 메시지입니다.
type FormattedMessage struct {
	Kind    MessageKind
	Content string
	RefID   string
}

// Formatter는 에이전트 출력을 CLI 포맷으로 변환합니다.
type Formatter struct {
	showToolOutput bool
	renderer       *glamour.TermRenderer
}

// NewFormatter는 새 Formatter를 반환합니다.
func NewFormatter(showToolOutput bool) *Formatter {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	if err != nil {
		renderer = nil
	}
	return &Formatter{
		showToolOutput: showToolOutput,
		renderer:       renderer,
	}
}

// FormatText는 에이전트 텍스트 응답을 마크다운 렌더링하여 포맷합니다.
func (f *Formatter) FormatText(text string) *FormattedMessage {
	rendered := text
	if f.renderer != nil {
		if out, err := f.renderer.Render(text); err == nil {
			rendered = out
		}
	}
	return &FormattedMessage{
		Kind:    KindText,
		Content: rendered,
	}
}

// FormatError는 에러 메시지를 포맷합니다.
func (f *Formatter) FormatError(errMsg string) *FormattedMessage {
	return &FormattedMessage{
		Kind:    KindError,
		Content: fmt.Sprintf("❌ 오류: %s", errMsg),
	}
}

// FormatToolCall은 Tool 호출 알림을 포맷합니다. (bright magenta)
func (f *Formatter) FormatToolCall(toolDesc string) *FormattedMessage {
	return &FormattedMessage{
		Kind:    KindToolCall,
		Content: fmt.Sprintf("%s  ⚙️  실행 중: %s%s", colorBrightMagenta, toolDesc, colorReset),
	}
}

// FormatToolResult는 Tool 결과를 포맷합니다. (bright cyan)
// showToolOutput이 false이면 nil을 반환합니다.
func (f *Formatter) FormatToolResult(result, refID string) *FormattedMessage {
	if !f.showToolOutput {
		return nil
	}
	content := strings.TrimSpace(result)
	if len(content) > 2000 {
		content = content[:2000] + fmt.Sprintf("\n\n... (이하 생략, 참조 ID: %s)", refID)
	}
	return &FormattedMessage{
		Kind:    KindToolResult,
		Content: fmt.Sprintf("%s[%s]%s\n%s%s%s", colorBrightCyan, refID, colorReset, colorBrightCyan, content, colorReset),
		RefID:   refID,
	}
}

// FormatPropose는 변경 제안 메시지를 포맷합니다.
func (f *Formatter) FormatPropose(proposal string) *FormattedMessage {
	separator := strings.Repeat("─", 60)
	return &FormattedMessage{
		Kind: KindPropose,
		Content: fmt.Sprintf("\n%s\n📋 변경 제안\n%s\n%s\n%s",
			separator, separator, proposal, separator),
	}
}

// FormatConfirmPrompt는 y/n 승인 프롬프트를 포맷합니다.
func (f *Formatter) FormatConfirmPrompt() *FormattedMessage {
	return &FormattedMessage{
		Kind:    KindPrompt,
		Content: "실행하시겠습니까? (y/n): ",
	}
}

// PrintMessage는 포맷된 메시지를 stdout에 출력합니다.
func PrintMessage(msg *FormattedMessage) {
	if msg == nil {
		return
	}
	switch msg.Kind {
	case KindPrompt:
		fmt.Print(msg.Content)
	default:
		fmt.Print(msg.Content)
	}
}
