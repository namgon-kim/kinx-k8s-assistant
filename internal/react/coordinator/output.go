package coordinator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/google/uuid"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/masking"
	"k8s.io/klog/v2"
)

func (l *Loop) translateModelText(ctx context.Context, text string) string {
	if l.lang == nil || !l.lang.Enabled() || strings.TrimSpace(text) == "" {
		return text
	}
	if strings.EqualFold(strings.TrimSpace(l.cfg.Lang.Language), "English") {
		return text
	}
	translated, err := l.lang.Translate(ctx, text)
	if err != nil {
		klog.Warningf("lang translation failed: %v", err)
		return "번역 모델 호출에 실패했습니다. /lang status의 model과 endpoint 설정을 확인하세요."
	}
	if strings.TrimSpace(translated) == "" {
		return "번역 모델이 빈 응답을 반환했습니다. /lang status의 model과 endpoint 설정을 확인하세요."
	}
	return translated
}

func (l *Loop) addMessage(source api.MessageSource, messageType api.MessageType, payload any) {
	if l != nil && l.activeTransaction != nil {
		l.activeTransaction.messages = append(l.activeTransaction.messages, deferredMessage{
			source:      source,
			messageType: messageType,
			payload:     payload,
		})
		return
	}
	l.emitMessage(source, messageType, payload)
}

func (l *Loop) addTranslatedModelMessage(ctx context.Context, text string) {
	if l != nil && l.activeTransaction != nil {
		l.activeTransaction.messages = append(l.activeTransaction.messages, deferredMessage{
			source:      api.MessageSourceModel,
			messageType: api.MessageTypeText,
			payload:     text,
			translate:   true,
			context:     ctx,
		})
		return
	}
	l.emitMessage(api.MessageSourceModel, api.MessageTypeText, l.translateModelText(ctx, text))
}

func (l *Loop) emitMessage(source api.MessageSource, messageType api.MessageType, payload any) {
	if l == nil || l.output == nil {
		klog.V(2).InfoS("react output message dropped because output channel is unavailable", "source", source, "type", messageType, "payload_type", fmt.Sprintf("%T", payload))
		return
	}
	l.publishCommittedRuntimeSnapshot()
	klog.V(2).InfoS("react output message queued", "source", source, "type", messageType, "payload_type", fmt.Sprintf("%T", payload))
	l.output <- &api.Message{
		ID:        uuid.NewString(),
		Source:    source,
		Type:      messageType,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}

func logStateName(lifecycle LoopLifecycleState) string {
	switch lifecycle {
	case LoopLifecycleAwaitingUserInput:
		return "awaiting_user_input"
	case LoopLifecycleModelTurn:
		return "running"
	case LoopLifecycleWaitingApproval:
		return "waiting_approval"
	case LoopLifecycleWaitingContinuationChoice:
		return "waiting_direction_choice"
	case LoopLifecycleWaitingContinuationText:
		return "waiting_direction_text"
	case LoopLifecycleExited:
		return "exited"
	default:
		return fmt.Sprintf("unknown(%d)", lifecycle)
	}
}

func logFunctionCallNames(calls []gollm.FunctionCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, strings.TrimSpace(call.Name))
	}
	return names
}

func logFunctionCallSummaries(calls []gollm.FunctionCall) []string {
	summaries := make([]string, 0, len(calls))
	for _, call := range calls {
		summary := strings.TrimSpace(call.Name)
		if command, ok := rawCommandString(call.Arguments["command"]); ok {
			summary += " command=" + trimForLog(masking.MaskSensitiveData(command), 180)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func logPendingCallSummaries(calls []PendingCall) []string {
	summaries := make([]string, 0, len(calls))
	for _, call := range calls {
		summary := fmt.Sprintf("%s modifies=%s interactive=%t", call.FunctionCall.Name, call.ModifiesResource, call.IsInteractive)
		if call.ParsedToolCall != nil {
			summary += " desc=" + trimForLog(masking.MaskSensitiveData(call.ParsedToolCall.Description()), 180)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func logResultSummary(result map[string]any) (string, string, []string) {
	keys := logMapKeys(result)
	status := strings.TrimSpace(stringFromAny(result["status"]))
	if status == "" {
		if toolResultSucceeded(result) {
			status = "success"
		} else {
			status = "unknown"
		}
	}
	errText := strings.TrimSpace(stringFromAny(result["error"]))
	if errText == "" {
		errText = strings.TrimSpace(stringFromAny(result["stderr"]))
	}
	if errText != "" {
		errText = fmt.Sprintf("present(len=%d)", len(errText))
	}
	return status, errText, keys
}

func logMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func maskedLogStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, maskForSystemLog(value))
	}
	return out
}

func maskForSystemLog(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(masking.MaskSensitiveData(value))), " ")
}

func trimForLog(value string, limit int) string {
	value = maskForSystemLog(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
