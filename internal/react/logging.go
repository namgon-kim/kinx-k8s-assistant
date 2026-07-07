package react

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/masking"
)

func logStateName(state State) string {
	switch state {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateWaitingApproval:
		return "waiting_approval"
	case StateWaitingDirectionChoice:
		return "waiting_direction_choice"
	case StateWaitingDirectionText:
		return "waiting_direction_text"
	case StateDone:
		return "done"
	case StateExited:
		return "exited"
	default:
		return fmt.Sprintf("unknown(%d)", state)
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
		if command, ok := commandString(call.Arguments["command"]); ok && strings.TrimSpace(command) != "" {
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
