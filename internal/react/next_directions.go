package react

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"k8s.io/klog/v2"
)

// consumeNextDirections handles the model's next_directions response after an
// inconclusive final_report. The proposed options are rendered as a
// UserChoiceRequest with extra "직접 입력" and "여기서 종료" choices, and the
// loop transitions to StateWaitingDirectionChoice until the user picks.
func (l *Loop) consumeNextDirections(ctx context.Context, calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalNextDirectionsCall {
			remaining = append(remaining, call)
			continue
		}
		nd, ok := nextDirectionsFromFunctionCall(call)
		if !ok {
			if !l.appendCorrection("invalid_next_directions", "next_directions payload was invalid. Re-emit a next_directions object with 1-3 options; each option needs `kind` (another_guide|different_approach) and `summary`.") {
				klog.Warning("next_directions remained invalid after correction; falling back to runtime continuation choices")
				nd = l.fallbackNextDirections()
				l.pendingNextDirections = &nd
				l.promptDirectionChoice(nd)
				return nil, true
			}
			l.pendingCalls = nil
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		l.pendingNextDirections = &nd
		l.promptDirectionChoice(nd)
		return nil, true
	}
	return remaining, false
}

func (l *Loop) fallbackNextDirections() nextDirections {
	nd := nextDirections{
		Note: "모델이 후속 진단 방향을 올바른 형식으로 제안하지 못해 기본 선택지를 표시합니다.",
	}
	opt := l.genericNextDirectionOption()
	if strings.TrimSpace(opt.Instruction) != "" {
		nd.Options = []nextDirectionOption{opt}
	}
	return nd
}

func (l *Loop) genericNextDirectionOption() nextDirectionOption {
	report := l.pendingFinalReport
	if report == nil {
		return nextDirectionOption{}
	}
	var clues []string
	clues = append(clues, report.Blockers...)
	clues = append(clues, report.EvidenceMissing...)
	if len(clues) == 0 {
		return nextDirectionOption{}
	}
	return nextDirectionOption{
		Kind:        "different_approach",
		Summary:     "부족한 증거를 기준으로 다른 접근을 시도",
		Why:         "이전 진단이 불충분했던 지점을 기준으로 다음 확인 대상을 좁힙니다.",
		Instruction: "Continue diagnosis by addressing these blockers or missing evidence first: " + strings.Join(clues, "; "),
	}
}

func nextDirectionsFromFunctionCall(call gollm.FunctionCall) (nextDirections, bool) {
	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		return nextDirections{}, false
	}
	var nd nextDirections
	if err := json.Unmarshal(raw, &nd); err != nil {
		return nextDirections{}, false
	}
	var clean []nextDirectionOption
	for _, opt := range nd.Options {
		kind := strings.ToLower(strings.TrimSpace(opt.Kind))
		summary := strings.TrimSpace(opt.Summary)
		if summary == "" {
			continue
		}
		switch kind {
		case "another_guide":
			if strings.TrimSpace(opt.ResourceFamily) == "" || strings.TrimSpace(opt.ProblemFocus) == "" {
				continue
			}
		case "different_approach":
			if strings.TrimSpace(opt.Instruction) == "" {
				continue
			}
		default:
			continue
		}
		opt.Kind = kind
		opt.Summary = summary
		clean = append(clean, opt)
		if len(clean) == 3 {
			break
		}
	}
	if len(clean) == 0 {
		return nextDirections{}, false
	}
	nd.Options = clean
	return nd, true
}

func (l *Loop) promptDirectionChoice(nd nextDirections) {
	prompt := strings.Builder{}
	prompt.WriteString("진단을 어떻게 계속할지 선택해 주세요.")
	if note := strings.TrimSpace(nd.Note); note != "" {
		prompt.WriteString("\n")
		prompt.WriteString(note)
	}

	var options []api.UserChoiceOption
	state := &directionPromptState{}
	for i, opt := range nd.Options {
		label := opt.Summary
		switch opt.Kind {
		case "another_guide":
			label = fmt.Sprintf("[가이드 재검색] %s", opt.Summary)
		case "different_approach":
			label = fmt.Sprintf("[다른 접근] %s", opt.Summary)
		}
		options = append(options, api.UserChoiceOption{
			Value: fmt.Sprintf("option-%d", i+1),
			Label: label,
		})
		state.Options = append(state.Options, opt)
	}
	state.FreeInputIdx = len(options) + 1
	options = append(options, api.UserChoiceOption{Value: "free-input", Label: "직접 다른 방향 입력"})
	state.HasFreeInput = true
	state.FinalizeIdx = len(options) + 1
	options = append(options, api.UserChoiceOption{Value: "finalize", Label: "여기서 진단 종료"})
	l.pendingDirectionPrompt = state

	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt:  prompt.String(),
		Options: options,
	})
	l.pendingCalls = nil
	l.state = StateWaitingDirectionChoice
}

// waitForDirectionChoice is invoked when the loop is in
// StateWaitingDirectionChoice. It reads a single UserChoiceResponse and
// dispatches to the chosen continuation.
func (l *Loop) waitForDirectionChoice(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.state = StateExited
			return false
		}
		resp, ok := raw.(*api.UserChoiceResponse)
		if !ok {
			return true
		}
		state := l.pendingDirectionPrompt
		if state == nil {
			l.state = StateDone
			return true
		}
		choice := resp.Choice
		// 1-based: first len(state.Options) are LLM options, then free-input, then finalize.
		if choice >= 1 && choice <= len(state.Options) {
			opt := state.Options[choice-1]
			l.applyDirectionOption(ctx, opt)
			return true
		}
		if state.HasFreeInput && choice == state.FreeInputIdx {
			l.pendingDirectionPrompt = nil
			l.state = StateWaitingDirectionText
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, "어떤 방향으로 계속할지 알려주세요")
			return true
		}
		if choice == state.FinalizeIdx {
			l.pendingDirectionPrompt = nil
			l.pendingNextDirections = nil
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "진단을 여기서 종료합니다.")
			l.state = StateDone
			return true
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, fmt.Sprintf("잘못된 방향 선택: %d", choice))
		l.state = StateDone
		return true
	}
}

// waitForDirectionText handles the user's free-text continuation directive
// after they picked "직접 다른 방향 입력".
func (l *Loop) waitForDirectionText(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.state = StateExited
			return false
		}
		resp, ok := raw.(*api.UserInputResponse)
		if !ok {
			return true
		}
		text := strings.TrimSpace(resp.Query)
		if text == "" {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "입력이 비어 있어 진단을 종료합니다.")
			l.state = StateDone
			return true
		}
		normalized := strings.TrimPrefix(strings.ToLower(text), "/")
		switch normalized {
		case "exit", "quit":
			l.state = StateExited
			return false
		case "clear", "reset":
			l.clearConversationState()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "대화 상태를 초기화했습니다.")
			l.state = StateDone
			return true
		}
		if strings.HasPrefix(text, "/") {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "이 입력 단계에서는 /exit, /quit, /clear, /reset만 메타 명령으로 처리할 수 있습니다.")
			l.state = StateWaitingDirectionText
			return true
		}
		l.applyDirectionOption(ctx, nextDirectionOption{
			Kind:        "different_approach",
			Summary:     "사용자가 직접 지정한 방향",
			Instruction: text,
		})
		return true
	}
}

// applyDirectionOption translates a chosen direction into runtime state and
// resumes the ReAct loop.
func (l *Loop) applyDirectionOption(ctx context.Context, opt nextDirectionOption) {
	l.pendingDirectionPrompt = nil
	l.pendingNextDirections = nil
	l.pendingFinalReport = nil
	l.finalReportRequested = false
	l.pendingResponseDirective = ""

	switch opt.Kind {
	case "another_guide":
		// Reset guide step state and trigger a refined resource-guide lookup.
		l.guideStepState = nil
		l.resourceGuideInjected = false
		family := strings.TrimSpace(opt.ResourceFamily)
		if family == "" && l.requestContext != nil {
			family = l.requestContext.PrimaryTarget.Resource
		}
		query := strings.Join([]string{
			l.originalQuery,
			"resource family: " + family,
			"problem focus: " + opt.ProblemFocus,
			"reason for refinement: user selected this continuation after the previous guide was exhausted",
			"summary: " + opt.Summary,
		}, "\n")
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("선택한 방향으로 가이드를 다시 검색합니다: %s", opt.Summary))
		l.searchAndInjectResourceGuide(ctx, family, query)
		return
	case "different_approach":
		// Inject the user-approved instruction as a user message and resume.
		var b strings.Builder
		b.WriteString("Continuation directive selected by the user. Continue diagnosis under this directive instead of repeating the exhausted guide.\n")
		fmt.Fprintf(&b, "directive_summary: %s\n", opt.Summary)
		if opt.Why != "" {
			fmt.Fprintf(&b, "rationale: %s\n", opt.Why)
		}
		fmt.Fprintf(&b, "directive: %s\n", opt.Instruction)
		b.WriteString("Treat this as the active goal alongside the original_query. Choose the single next action that advances it.")
		l.currChatContent = append(l.currChatContent, b.String())
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("선택한 방향으로 진단을 계속합니다: %s", opt.Summary))
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
		return
	}
	l.state = StateDone
}
