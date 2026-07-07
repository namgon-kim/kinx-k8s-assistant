package react

import (
	"fmt"
	"strings"
)

type toolFailureClass string

const (
	toolFailureUnknown       toolFailureClass = "unknown"
	toolFailureCommandSyntax toolFailureClass = "command_syntax"
	toolFailureRBAC          toolFailureClass = "rbac_forbidden"
	toolFailureNotFound      toolFailureClass = "resource_not_found"
	toolFailureTimeout       toolFailureClass = "timeout_or_api_unavailable"
	toolFailurePartial       toolFailureClass = "partial_success"
)

func (l *Loop) annotateToolFailureResult(call PendingCall, result map[string]any) (GateOutcome, bool) {
	if result == nil || toolResultSucceeded(result) {
		return GateOutcome{}, false
	}
	detail := toolFailureDetail(result)
	class := classifyToolFailure(detail)
	if isPartialToolResult(result) {
		class = toolFailurePartial
	}
	retryable, scope := toolFailureRetryPolicy(class)
	branch := BranchRetryStep
	if !retryable {
		branch = BranchBlockUserRequest
	}
	result["failure_class"] = string(class)
	result["retryable"] = retryable
	result["retry_scope"] = string(scope)
	result["suggested_response"] = toolFailureSuggestedResponse(class)
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	correction := toolFailureCorrection(class, call.FunctionCall.Name, command, detail)
	return GateOutcome{
		Kind:            GateOutcomeToolExecutionFailure,
		Code:            "tool_execution_" + string(class),
		Retryable:       retryable,
		RetryScope:      scope,
		ModelCorrection: correction,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    branch,
	}, true
}

func toolFailureResultFromError(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"status": "error",
		"error":  err.Error(),
	}
}

func toolFailureResultFromMapError(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"status": "error",
		"error":  "tool returned an unparseable result: " + err.Error(),
	}
}

func toolFailureDetail(result map[string]any) string {
	var parts []string
	for _, key := range []string{"error", "errors", "stderr", "message", "status", "reason"} {
		value := strings.TrimSpace(stringFromAny(result[key]))
		if value != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
	}
	if len(parts) == 0 {
		return "tool returned a failed status without detailed error text"
	}
	return strings.Join(parts, "; ")
}

func classifyToolFailure(detail string) toolFailureClass {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "rbac"):
		return toolFailureRBAC
	case strings.Contains(lower, "command not found") ||
		strings.Contains(lower, "executable file not found"):
		return toolFailureCommandSyntax
	case strings.Contains(lower, "not found") ||
		strings.Contains(lower, "notfound"):
		return toolFailureNotFound
	case strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "server is currently unable"):
		return toolFailureTimeout
	case strings.Contains(lower, "unknown flag") ||
		strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "invalid argument") ||
		strings.Contains(lower, "requires exactly") ||
		strings.Contains(lower, "usage:"):
		return toolFailureCommandSyntax
	default:
		return toolFailureUnknown
	}
}

func toolFailureRetryPolicy(class toolFailureClass) (bool, RetryScope) {
	switch class {
	case toolFailureRBAC:
		return false, RetryScopeUserRequest
	case toolFailureTimeout:
		return true, RetryScopeExternalState
	case toolFailureNotFound:
		return true, RetryScopeCurrentPhase
	case toolFailurePartial:
		return true, RetryScopeCurrentStep
	case toolFailureCommandSyntax, toolFailureUnknown:
		return true, RetryScopeAgentCommand
	default:
		return true, RetryScopeAgentCommand
	}
}

func toolFailureSuggestedResponse(class toolFailureClass) string {
	switch class {
	case toolFailureCommandSyntax:
		return "Retry with a corrected non-interactive command that observes the same evidence."
	case toolFailureRBAC:
		return "Do not repeat the same forbidden command. Use alternative permitted evidence if possible, or report the permission blocker when appropriate."
	case toolFailureNotFound:
		return "Recheck the target name, namespace, and resource kind before retrying or asking for clarification."
	case toolFailureTimeout:
		return "Retry with a narrower read-only observation or continue with an external-state wait/recheck path."
	case toolFailurePartial:
		return "Preserve the successful evidence, then collect only the missing or failed evidence before reporting completion."
	default:
		return "Choose a safer alternative diagnostic command or explain the blocker only when the active phase allows reporting."
	}
}

func isPartialToolResult(result map[string]any) bool {
	if result == nil {
		return false
	}
	if boolFromAny(result["partial_success"]) || boolFromAny(result["partial"]) {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(stringFromAny(result["status"])))
	if status == "partial" || status == "partial_success" || status == "partially_succeeded" {
		return true
	}
	if !hasNonEmptyToolErrors(result["errors"]) {
		return false
	}
	return hasSuccessfulPayload(result)
}

func hasNonEmptyToolErrors(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return strings.TrimSpace(stringFromAny(value)) != ""
	}
}

func hasSuccessfulPayload(result map[string]any) bool {
	for _, key := range []string{"items", "resources", "results", "stdout", "data", "output"} {
		value, ok := result[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(typed) != "" {
				return true
			}
		case []any:
			if len(typed) > 0 {
				return true
			}
		case []string:
			if len(typed) > 0 {
				return true
			}
		case map[string]any:
			if len(typed) > 0 {
				return true
			}
		default:
			if strings.TrimSpace(stringFromAny(value)) != "" {
				return true
			}
		}
	}
	return false
}

func toolFailureCorrection(class toolFailureClass, toolName, command, detail string) string {
	var b strings.Builder
	b.WriteString("The previous tool observation indicates tool_execution_failure.")
	b.WriteString(" failure_class=")
	b.WriteString(string(class))
	if strings.TrimSpace(toolName) != "" {
		b.WriteString(" tool=")
		b.WriteString(strings.TrimSpace(toolName))
	}
	if strings.TrimSpace(command) != "" {
		b.WriteString(" command=")
		b.WriteString(strconvQuote(command))
	}
	if strings.TrimSpace(detail) != "" {
		b.WriteString(" detail=")
		b.WriteString(strconvQuote(detail))
	}
	b.WriteString(" ")
	b.WriteString(toolFailureSuggestedResponse(class))
	if retryable, _ := toolFailureRetryPolicy(class); retryable {
		b.WriteString(" Do not return final_report solely because a retryable tool failure occurred; continue the active phase with corrected evidence or phase_progress when the phase completion condition is met.")
	} else {
		b.WriteString(" Do not repeat the blocked operation. If no permitted alternative evidence is available, report the blocker only when the active phase allows reporting.")
	}
	return b.String()
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", strings.TrimSpace(value))
}
