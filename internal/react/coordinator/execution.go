package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/kube"
	"k8s.io/klog/v2"
)

func (l *Loop) requestApproval() {
	descriptions := make([]string, 0, len(l.pendingCalls))
	for _, call := range l.pendingCalls {
		descriptions = append(descriptions, call.ParsedToolCall.Description())
	}
	prompt := "다음 명령은 실행 전 승인이 필요합니다:\n* " + strings.Join(descriptions, "\n* ")
	prompt += "\n\n진행할까요?"
	klog.V(0).InfoS("approval requested", "calls", len(l.pendingCalls))
	klog.V(1).InfoS("approval request call summaries", "descriptions", maskedLogStrings(descriptions))
	l.transitionControl(RuntimeControlAwaitingApproval)
	l.refreshInputOwner()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt: prompt,
		Options: []api.UserChoiceOption{
			{Value: "yes", Label: "예"},
			{Value: "yes_and_dont_ask_me_again", Label: "예, 이후 묻지 않기"},
			{Value: "no", Label: "아니오"},
		},
	})
}

func (l *Loop) handleApproval(ctx context.Context, choice int) error {
	klog.V(0).InfoS("handling approval choice", "choice", choice, "pending", len(l.pendingCalls))
	switch choice {
	case 1:
		return l.dispatchToolCalls(ctx)
	case 2:
		l.skipPermissions = true
		klog.V(0).InfoS("approval granted with skip future permissions")
		return l.dispatchToolCalls(ctx)
	case 3:
		klog.V(0).InfoS("approval declined", "pending", len(l.pendingCalls))
		for _, call := range l.pendingCalls {
			l.appendToolObservation(call, map[string]any{
				"error":     "User declined to run this operation.",
				"status":    "declined",
				"retryable": false,
			})
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "사용자가 작업 실행을 거부했습니다.")
		l.applyRuntimeCleanup(cleanupApprovalDeclinedPolicy())
		l.currIteration++
		l.transitionControl(RuntimeControlAwaitingModelStep)
		return nil
	default:
		return fmt.Errorf("잘못된 승인 선택: %d", choice)
	}
}

func (l *Loop) dispatchToolCalls(ctx context.Context) error {
	klog.V(1).InfoS("tool dispatch starting", "pending", len(l.pendingCalls), "summaries", logPendingCallSummaries(l.pendingCalls))
	l.transitionControl(RuntimeControlExecutingTool)
	l.refreshInputOwner()
	defer l.refreshInputOwner()

	var failureOutcome *GateOutcome
	for _, call := range l.pendingCalls {
		description := call.ParsedToolCall.Description()
		toolStart := time.Now()
		klog.V(1).InfoS("tool invocation starting", "tool", call.FunctionCall.Name, "description", maskForSystemLog(description), "modifies_resource", call.ModifiesResource)
		l.addMessage(api.MessageSourceModel, api.MessageTypeToolCallRequest, description)

		output, err := call.ParsedToolCall.InvokeTool(ctx, tools.InvokeToolOptions{
			Kubeconfig: l.cfg.Kubeconfig,
			WorkDir:    l.workDir,
			Executor:   l.executor,
		})
		if err != nil {
			if ctx.Err() != nil {
				klog.ErrorS(err, "tool invocation cancelled", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart))
				return err
			}
			result := toolFailureResultFromError(err)
			failureOutcome = l.recordToolDispatchFailure(call, result, failureOutcome, toolStart, "tool invocation failed")
			break
		}

		result, err := tools.ToolResultToMap(output)
		if err != nil {
			result = toolFailureResultFromMapError(err)
			failureOutcome = l.recordToolDispatchFailure(call, result, failureOutcome, toolStart, "tool result conversion failed")
			break
		}
		if outcome, failed := l.annotateToolFailureResult(call, result); failed && failureOutcome == nil {
			failureOutcome = &outcome
		}
		status, errText, keys := logResultSummary(result)
		klog.V(1).InfoS("tool invocation completed", "tool", call.FunctionCall.Name, "duration", time.Since(toolStart), "status", status, "error", errText)
		klog.V(2).InfoS("tool result summary", "tool", call.FunctionCall.Name, "keys", keys)
		l.appendToolObservation(call, result)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
	}

	if failureOutcome != nil {
		klog.V(0).InfoS("tool dispatch produced failure outcome", "code", failureOutcome.Code, "kind", failureOutcome.Kind, "retryable", failureOutcome.Retryable)
		l.transitionAfterToolFailure()
		l.applyGateOutcome(*failureOutcome)
		return nil
	}

	l.pendingCalls = nil
	l.currIteration++
	l.requestPostGuideCompletionDirective()
	if l.shouldCompactBeforeNextSend() {
		l.compactBeforeNextIteration("Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it.")
	}
	if l.controlState() == RuntimeControlExecutingTool {
		l.transitionControl(RuntimeControlAwaitingModelStep)
	}
	klog.V(0).InfoS("tool dispatch completed", "next_iteration", l.currIteration+1)
	return nil
}

func (l *Loop) recordToolDispatchFailure(call PendingCall, result map[string]any, current *GateOutcome, started time.Time, message string) *GateOutcome {
	status, errText, keys := logResultSummary(result)
	klog.V(0).InfoS(message, "tool", call.FunctionCall.Name, "duration", time.Since(started), "status", status, "error", errText)
	klog.V(2).InfoS("tool failure result summary", "tool", call.FunctionCall.Name, "keys", keys)
	if outcome, failed := l.annotateToolFailureResult(call, result); failed && current == nil {
		current = &outcome
	}
	l.appendToolObservation(call, result)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeToolCallResponse, result)
	return current
}

func (l *Loop) appendToolObservation(call PendingCall, result map[string]any) {
	status, errText, keys := logResultSummary(result)
	klog.V(2).InfoS("appending tool observation", "tool", call.FunctionCall.Name, "status", status, "error", errText, "keys", keys, "shim", l.cfg.EnableToolUseShim)
	l.recordAction(call, result)
	l.trackMutationVerification(call, result)
	if l.cfg.EnableToolUseShim {
		l.currChatContent = append(l.currChatContent, fmt.Sprintf("Result of running %q:\n%v", call.FunctionCall.Name, result))
		return
	}
	l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
		ID:     call.FunctionCall.ID,
		Name:   call.FunctionCall.Name,
		Result: result,
	})
}

func (l *Loop) recordAction(call PendingCall, result map[string]any) {
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	target, ok := actionTargetFromFunctionCall(call.FunctionCall)
	l.actionSeq++
	phase := l.currentPhaseRef()
	record := actionRecord{
		Step:       l.actionSeq,
		Tool:       call.FunctionCall.Name,
		Command:    command,
		ResultHash: contextHash(fmt.Sprintf("%v", result)),
		Result:     compactObservationResult(result),
		Clues:      extractObservationClues(result),
	}
	if phase.Index != 0 || strings.TrimSpace(phase.Name) != "" {
		record.Phase = &phase
	}
	if ok {
		record.Target = &target
	}
	l.completedActions = append(l.completedActions, record)
	klog.V(1).InfoS("action recorded", "step", record.Step, "tool", record.Tool, "command", trimForLog(record.Command, 180), "result_hash", record.ResultHash)
	if len(l.completedActions) > 12 {
		l.completedActions = l.completedActions[len(l.completedActions)-12:]
	}
	if guideProgressObservationUseful(result) && l.guideProgressAllowedForCurrentPhase() {
		if step, ok := guideStepCompletedFromFunctionCall(call.FunctionCall); ok {
			l.markGuideStepCompleted(step)
		} else if step, ok := l.inferGuideStepCompletedFromFunctionCall(call.FunctionCall); ok {
			l.markGuideStepCompleted(step)
		}
	}
}

func (l *Loop) analyzeToolCalls(ctx context.Context, calls []gollm.FunctionCall) ([]PendingCall, error) {
	pending := make([]PendingCall, len(calls))
	for i, call := range calls {
		klog.V(2).InfoS("parsing tool invocation", "name", call.Name, "argument_keys", logMapKeys(call.Arguments))
		parsed, err := l.registry.Tools.ParseToolInvocation(ctx, call.Name, call.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call 파싱 실패: %w", err)
		}
		isInteractive, interactiveErr := parsed.GetTool().IsInteractive(call.Arguments)
		pending[i] = PendingCall{
			FunctionCall:     call,
			ParsedToolCall:   parsed,
			IsInteractive:    isInteractive,
			InteractiveError: interactiveErr,
			ModifiesResource: l.modifiesResource(parsed, call),
		}
		klog.V(1).InfoS("tool invocation parsed", "name", call.Name, "modifies_resource", pending[i].ModifiesResource, "interactive", isInteractive)
	}
	return pending, nil
}

func (l *Loop) modifiesResource(parsed *tools.ToolCall, call gollm.FunctionCall) string {
	if isObservationToolName(call.Name) {
		return "no"
	}
	toolDecision := parsed.GetTool().CheckModifiesResource(call.Arguments)
	if toolDecision == "yes" || hasKnownMutatingKubectlInvocation(call) {
		return "yes"
	}
	if hasBlockedReadOnlyFastPathFeature(call) {
		return "unknown"
	}
	if isNonMutatingKubectlInvocation(call) {
		return "no"
	}
	return toolDecision
}

func hasKnownMutatingKubectlInvocation(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := kube.SplitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if kube.ContainsMutatingKubectlCommand(command) {
			return true
		}
	}
	return false
}

func hasBlockedReadOnlyFastPathFeature(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := kube.SplitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if kube.ContainsShellEvaluation(command) || kube.ContainsDisallowedReadOnlyKubectlSubcommand(command) {
			return true
		}
	}
	return false
}

func isNonMutatingKubectlInvocation(call gollm.FunctionCall) bool {
	script, ok := readonlyShellScriptFromFunctionCall(call)
	if !ok {
		return false
	}
	commands := kube.SplitShellCommandList(script)
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if !kube.IsReadOnlyKubectlPipeline(command) {
			return false
		}
	}
	return true
}

func readonlyShellScriptFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	raw, ok := call.Arguments["command"].(string)
	if !ok {
		return "", false
	}
	command := strings.TrimSpace(raw)
	if command == "" {
		return "", false
	}
	if script, ok := kube.ExtractShellScript(command); ok {
		return script, true
	}

	switch strings.ToLower(call.Name) {
	case "kubectl":
		if strings.HasPrefix(command, "kubectl ") {
			return command, true
		}
		return "", false
	case "bash", "sh", "shell":
		if strings.HasPrefix(command, "kubectl ") {
			return command, true
		}
		return "", false
	default:
		return "", false
	}
}

func isAssistantManagedToolName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "guidance_")
}

func isObservationToolName(name string) bool {
	name = strings.ToLower(name)
	return isAssistantManagedToolName(name) ||
		strings.HasPrefix(name, "log-analyzer_") ||
		strings.HasPrefix(name, "log_analyzer_")
}

func (l *Loop) hasModifyingCalls() bool {
	for _, call := range l.pendingCalls {
		if call.ModifiesResource != "no" {
			return true
		}
	}
	return false
}

func (l *Loop) rejectReadOnlyModifyingCalls() {
	var descriptions []string
	hasUnknown := false
	hasKnownMutation := false
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		if call.ModifiesResource == "unknown" {
			hasUnknown = true
		} else {
			hasKnownMutation = true
		}
		descriptions = append(descriptions, call.ParsedToolCall.Description())
	}
	for _, call := range l.pendingCalls {
		if call.ModifiesResource == "no" {
			continue
		}
		errorMessage := "read-only mode is enabled; commands that modify Kubernetes resources are blocked."
		if call.ModifiesResource == "unknown" {
			errorMessage = "read-only mode is enabled; command safety could not be verified, so the command was blocked."
		}
		l.appendToolObservation(call, map[string]any{
			"error":              errorMessage,
			"status":             "blocked",
			"retryable":          readOnlyBlockRetryable(call.ModifiesResource, hasKnownMutation),
			"retry_scope":        readOnlyBlockRetryScope(call.ModifiesResource, hasKnownMutation),
			"modifies_resource":  call.ModifiesResource,
			"read_only_mode":     true,
			"allowed_operation":  "read-only diagnostics such as get, describe, logs, top, events",
			"blocked_operation":  call.ParsedToolCall.Description(),
			"suggested_response": readOnlyBlockedSuggestedResponse(call.ModifiesResource, hasKnownMutation),
		})
	}
	klog.V(0).InfoS("read-only modifying calls rejected", "has_unknown", hasUnknown, "has_known_mutation", hasKnownMutation, "calls", len(descriptions))
	klog.V(1).InfoS("read-only rejected call summaries", "descriptions", maskedLogStrings(descriptions))
	if len(descriptions) == 0 {
		descriptions = append(descriptions, "안전성이 확인되지 않은 명령")
	}
	headline := "read-only 모드가 활성화되어 리소스 변경 명령을 차단했습니다"
	code := "readonly_mutation_blocked"
	correction := "Read-only mode blocked a command that can modify Kubernetes resources. This is a user/request blocker, not an agent retry. Do not repeat the same mutation. If the user asked for a change, explain that read-only mode blocks it and provide one manual recommendation."
	if hasUnknown {
		headline = "안전한 read-only 진단 명령으로 확인되지 않아 실행하지 않았습니다"
	}
	if hasUnknown && hasKnownMutation {
		headline = "read-only 모드에서 변경 명령과 안전성이 확인되지 않은 명령을 차단했습니다"
	}
	if hasUnknown && !hasKnownMutation {
		code = "readonly_unknown_command_blocked"
		correction = "The runtime did not execute the previous command because it could not verify it as a safe Kubernetes diagnostic. This is an agent retry, not a user-request blocker. Retry with one real read-only kubectl command such as get, describe, logs, top, api-resources, api-versions, version, config, or auth can-i/whoami. If the active phase is complete, emit phase_progress. Do not return final_report only because this command was blocked."
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeError, headline+":\n* "+strings.Join(descriptions, "\n* "))
	kind := GateOutcomeUserRequestBlocked
	retryable := false
	retryScope := RetryScopeUserRequest
	branch := BranchBlockUserRequest
	if hasUnknown && !hasKnownMutation {
		kind = GateOutcomeAgentCommandRetry
		retryable = true
		retryScope = RetryScopeAgentCommand
		branch = BranchRetryStep
	}
	l.applyGateOutcome(GateOutcome{
		Kind:            kind,
		Code:            code,
		Retryable:       retryable,
		RetryScope:      retryScope,
		UserMessage:     "read-only 차단 correction이 반복되어 진단을 중단합니다.",
		ModelCorrection: correction,
		CorrectionMode:  CorrectionModeAppendPlain,
		BranchPolicy:    branch,
	})
}

func readOnlyBlockedSuggestedResponse(modifiesResource string, hasKnownMutation bool) string {
	if modifiesResource == "unknown" && !hasKnownMutation {
		return "Agent retry: replace the blocked command with a concrete read-only kubectl diagnostic, or emit phase_progress if the active phase is complete. Do not finalize solely because this command was blocked."
	}
	return "User/request blocker: do not repeat this mutation while read-only mode is enabled. Explain the read-only blocker only when a final response is otherwise appropriate."
}

func readOnlyBlockRetryable(modifiesResource string, hasKnownMutation bool) bool {
	return modifiesResource == "unknown" && !hasKnownMutation
}

func readOnlyBlockRetryScope(modifiesResource string, hasKnownMutation bool) string {
	if modifiesResource == "unknown" && !hasKnownMutation {
		return "agent_correct_command"
	}
	return "user_request_blocked_by_read_only"
}

func (l *Loop) rejectInconsistentActionTargets(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		message, invalid := inconsistentActionTargetMessage(call)
		if !invalid {
			message, invalid = l.requestNamespaceInvariantMessage(call)
		}
		if !invalid {
			continue
		}
		return l.applyAgentCommandRetryGate("inconsistent_action_target", "반복된 action target 불일치로 루프를 중단했습니다.", message)
	}
	return false
}

func (l *Loop) requestNamespaceInvariantMessage(call gollm.FunctionCall) (string, bool) {
	requestNamespace := l.requestScopeNamespace()
	if requestNamespace == "" {
		return "", false
	}
	command, ok := kubectlCommandFromFunctionCall(call)
	if !ok || !kube.ContainsMutatingKubectlVerb(command) {
		return "", false
	}
	target, hasTarget := actionTargetFromFunctionCall(call)
	targetResource := ""
	if hasTarget {
		targetResource = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(target.Resource)))
		if target.Namespace != "" && !isAllNamespacesValue(target.Namespace) && target.Namespace != requestNamespace && kubectlResourceUsuallyNamespaced(targetResource) {
			return fmt.Sprintf("Request namespace is %q, but action target namespace is %q. Preserve the request namespace for namespaced resource mutations and return one corrected action with target.namespace=%q and `-n %s` or `--namespace=%s` in the command.", requestNamespace, target.Namespace, requestNamespace, requestNamespace, requestNamespace), true
		}
	}
	resource := targetResource
	if resource == "" {
		resource, _ = primaryMutatingKubectlResource(command)
	}
	if resource == "" || !kubectlResourceUsuallyNamespaced(resource) {
		return "", false
	}
	if commandUsesAllNamespaces(command) {
		return fmt.Sprintf("Request namespace is %q, but command %q uses all-namespaces scope for a namespaced resource mutation. Mutating actions must target one resolved namespace. Return one corrected action with `-n %s` or `--namespace=%s`.", requestNamespace, command, requestNamespace, requestNamespace), true
	}
	if namespace, ok := commandNamespaceValue(command); ok {
		if namespace != requestNamespace {
			return fmt.Sprintf("Request namespace is %q, but command %q targets namespace %q. Mutating actions must preserve the resolved request namespace. Return one corrected action with `-n %s` or `--namespace=%s`.", requestNamespace, command, namespace, requestNamespace, requestNamespace), true
		}
		return "", false
	}
	return fmt.Sprintf("Request namespace is %q, but mutating command %q omits namespace for namespaced resource %q. Do not rely on kubectl's implicit default namespace. Return one corrected action with `-n %s` or `--namespace=%s`.", requestNamespace, command, resource, requestNamespace, requestNamespace), true
}

func (l *Loop) requestScopeNamespace() string {
	if l == nil || l.requestContext == nil {
		return ""
	}
	namespace := cleanNamespaceValue(l.requestContext.Scope.Namespace)
	if namespace == "" || isAllNamespacesValue(namespace) {
		return ""
	}
	return namespace
}

func (l *Loop) rejectInvalidKubectlResources(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		command, ok := kubectlCommandFromFunctionCall(call)
		if !ok || !kubectlCommandUsesUnknownResource(command) {
			continue
		}
		message := fmt.Sprintf("Command %q uses \"unknown\" as a Kubernetes resource kind. `resource_class=unknown` is only a classification hint; never put `unknown` in primary_target.resource, action.target.resource, or a kubectl resource position. Return one corrected response with a concrete resource kind from the user request or observed evidence, or answer asking for clarification if no concrete resource kind is identifiable.", command)
		return l.applyAgentCommandRetryGate("invalid_kubectl_resource", "반복된 잘못된 kubectl 리소스로 루프를 중단했습니다.", message)
	}
	return false
}

func (l *Loop) rejectUnrelatedFirstDiagnostic(calls []gollm.FunctionCall) bool {
	if l.actionSeq > 0 || l.requestContext == nil {
		return false
	}
	target := l.requestContext.PrimaryTarget
	if target.Resource == "" || target.Name == "" {
		return false
	}
	for _, call := range calls {
		command, ok := kubectlCommandFromFunctionCall(call)
		if !ok {
			continue
		}
		if commandMentionsResource(command, target.Resource) &&
			(commandMentionsToken(command, target.Name) || commandUsesSelectorForName(command, target.Name)) {
			continue
		}
		message := fmt.Sprintf("First diagnostic for explicit target %q %q must query that target or use a selector for that target before broadening. Command %q is unrelated. Start with the declared target and namespace scope.", target.Resource, target.Name, command)
		return l.applyAgentCommandRetryGate("unrelated_first_diagnostic", "반복된 최초 진단 대상 불일치로 루프를 중단했습니다.", message)
	}
	return false
}

func (l *Loop) applyAgentCommandRetryGate(code, userMessage, correction string) bool {
	return l.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeAgentCommandRetry,
		Code:            code,
		Retryable:       true,
		RetryScope:      RetryScopeAgentCommand,
		UserMessage:     userMessage,
		ModelCorrection: correction,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchRetryStep,
	})
}

func inconsistentActionTargetMessage(call gollm.FunctionCall) (string, bool) {
	command, ok := kubectlCommandFromFunctionCall(call)
	if !ok {
		return "", false
	}
	target, ok := actionTargetFromFunctionCall(call)
	if !ok {
		return "", false
	}
	if normalizeKubectlResource(strings.ToLower(strings.TrimSpace(target.Resource))) == "unknown" {
		return fmt.Sprintf("Action target declared resource %q. `unknown` is not a Kubernetes resource kind; it is only allowed as request_context.resource_class. Return one corrected next action with a concrete target resource, or answer asking for clarification if no concrete resource kind is identifiable.", target.Resource), true
	}
	if normalizeKubectlResource(strings.ToLower(strings.TrimSpace(target.Resource))) == "namespace" && target.Namespace != "" {
		return fmt.Sprintf("Action target declared resource %q with namespace scope %q, but Namespace objects are cluster-scoped. Use resource=namespace only when diagnosing a Namespace object itself; otherwise keep namespace as scope for the real target resource.", target.Resource, target.Namespace), true
	}
	if target.Namespace != "" && !isAllNamespacesValue(target.Namespace) && !isValidKubernetesNamespace(target.Namespace) {
		return fmt.Sprintf("Action target declared namespace %q, but namespace fields must contain a real Kubernetes namespace name, not a placeholder or explanatory phrase. Return one corrected response: use the actual namespace if known; if the object name is known but the namespace is not, omit target.namespace or use all-namespaces scope and locate it with `kubectl get <kind> -A --field-selector metadata.name=<name>`; ask for clarification only when the target cannot be located safely.", target.Namespace), true
	}
	if target.Resource != "" && !commandMentionsResource(command, target.Resource) {
		return fmt.Sprintf("Action target declared resource %q, but command %q does not include that resource. Preserve the declared target and return one corrected next action.", target.Resource, command), true
	}
	if target.Name != "" && !commandMentionsToken(command, target.Name) && !commandUsesSelectorForName(command, target.Name) {
		return fmt.Sprintf("Action target declared name %q, but command %q does not include that name. Preserve the declared target and return one corrected next action.", target.Name, command), true
	}
	if target.Name != "" && commandUsesAllNamespaces(command) && commandUsesPositionalObjectName(command, target.Resource, target.Name) {
		return fmt.Sprintf("Command %q combines all-namespaces scope with a positional object name. To locate a namespaced object when namespace is unknown, use an all-namespaces list filtered by field selector, for example `kubectl get %s -A --field-selector metadata.name=%s -o yaml`, then use the discovered namespace for exact object observation.", command, target.Resource, target.Name), true
	}
	if target.Namespace != "" && !commandUsesNamespace(command, target.Namespace) {
		if isAllNamespacesValue(target.Namespace) {
			return fmt.Sprintf("Action target declared all-namespaces scope, but command %q does not include `-A` or `--all-namespaces`. Preserve the all-namespaces scope and return one corrected next action.", command), true
		}
		return fmt.Sprintf("Action target declared namespace %q, but command %q omits that namespace. Preserve the declared target and return one corrected next action with `-n %s` or `--namespace=%s`.", target.Namespace, command, target.Namespace, target.Namespace), true
	}
	return "", false
}

func actionTargetFromFunctionCall(call gollm.FunctionCall) (actionTarget, bool) {
	raw, ok := call.Arguments["target"].(map[string]any)
	if !ok {
		return actionTarget{}, false
	}
	resource, _ := raw["resource"].(string)
	namespace, _ := raw["namespace"].(string)
	name, _ := raw["name"].(string)
	target := actionTarget{
		Resource:  strings.TrimSpace(resource),
		Namespace: cleanUnknownPlaceholder(namespace),
		Name:      cleanUnknownPlaceholder(name),
	}
	if target.Resource == "" && target.Namespace == "" && target.Name == "" {
		return actionTarget{}, false
	}
	return target, true
}

func commandMentionsToken(command, token string) bool {
	tokens := normalizedTokenList(token)
	if len(tokens) == 0 {
		return true
	}
	for _, token := range tokens {
		if !commandMentionsSingleToken(command, token) {
			return false
		}
	}
	return true
}

func commandMentionsSingleToken(command, token string) bool {
	for _, field := range strings.Fields(command) {
		field = strings.ToLower(strings.Trim(field, "'\""))
		for _, part := range strings.FieldsFunc(field, func(r rune) bool {
			return r == '/' || r == ','
		}) {
			if strings.TrimSpace(part) == token {
				return true
			}
		}
	}
	return false
}

func normalizedTokenList(token string) []string {
	var tokens []string
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(token)), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

func commandMentionsResource(command, resource string) bool {
	wants := normalizedResourceList(resource)
	if len(wants) == 0 {
		return true
	}
	mentioned := kubectlMentionedResources(command)
	if len(mentioned) == 0 && commandIsKubectlLogs(command) {
		mentioned = append(mentioned, "pod")
	}
	if len(mentioned) == 0 {
		for _, field := range strings.Fields(strings.ToLower(command)) {
			for _, part := range strings.Split(strings.Trim(field, "'\","), ",") {
				part = normalizeKubectlResource(strings.TrimSpace(part))
				if part != "" {
					mentioned = append(mentioned, part)
				}
			}
		}
	}
	for _, want := range wants {
		if !resourceListContains(mentioned, want) {
			return false
		}
	}
	return true
}

func kubectlCommandUsesUnknownResource(command string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok {
			continue
		}
		switch verb {
		case "get", "describe":
		default:
			continue
		}
		resource, ok := firstKubectlResourceArg(fields, verbIndex+1)
		if !ok {
			continue
		}
		for _, part := range strings.Split(resource, ",") {
			if normalizeKubectlResource(strings.TrimSpace(part)) == "unknown" {
				return true
			}
		}
	}
	return false
}

func kubectlMentionedResources(command string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	var resources []string
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok {
			continue
		}
		switch verb {
		case "get", "describe", "logs":
		default:
			continue
		}
		if verb == "logs" {
			resources = append(resources, "pod")
			continue
		}
		for _, resource := range kubectlResourceArgs(fields, verbIndex+1) {
			for _, part := range strings.Split(resource, ",") {
				part = kubectlResourceKindFromArg(part)
				part = normalizeKubectlResource(strings.TrimSpace(part))
				if part != "" {
					resources = append(resources, part)
				}
			}
		}
	}
	return resources
}

func kubectlResourceArgs(fields []string, start int) []string {
	var resources []string
	firstSeen := false
	for i := start; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		if !firstSeen {
			resource := kubectlResourceKindFromArg(strings.Trim(field, ","))
			if resource != "" {
				resources = append(resources, resource)
				firstSeen = true
			}
			continue
		}
		if strings.Contains(field, "/") || strings.Contains(field, ",") {
			resource := kubectlResourceKindFromArg(strings.Trim(field, ","))
			if resource != "" {
				resources = append(resources, resource)
			}
		}
	}
	return resources
}

func commandIsKubectlLogs(command string) bool {
	fields := strings.Fields(strings.ToLower(command))
	verb, ok := kube.KubectlVerbFromFields(fields, 0)
	return ok && verb == "logs"
}

func normalizedResourceList(resource string) []string {
	var resources []string
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(resource)), ",") {
		part = normalizeKubectlResource(strings.TrimSpace(part))
		if part != "" {
			resources = append(resources, part)
		}
	}
	return resources
}

func resourceListContains(resources []string, want string) bool {
	for _, resource := range resources {
		if resourceNamesEquivalent(resource, want) {
			return true
		}
	}
	return false
}

func resourceNamesEquivalent(a, b string) bool {
	a = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(a)))
	b = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(b)))
	if a == "" || b == "" {
		return false
	}
	a = strings.Split(a, ".")[0]
	b = strings.Split(b, ".")[0]
	if a == b {
		return true
	}
	return singularizeResourceName(a) == b || singularizeResourceName(b) == a
}

func singularizeResourceName(resource string) string {
	switch {
	case strings.HasSuffix(resource, "ies") && len(resource) > 3:
		return strings.TrimSuffix(resource, "ies") + "y"
	case strings.HasSuffix(resource, "s") && len(resource) > 1:
		return strings.TrimSuffix(resource, "s")
	default:
		return resource
	}
}

func commandUsesSelectorForName(command, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	lower := strings.ToLower(command)
	return strings.Contains(lower, "cluster-name="+name) ||
		strings.Contains(lower, "cluster-name: "+name) ||
		strings.Contains(lower, "cluster.x-k8s.io/cluster-name="+name) ||
		strings.Contains(lower, "metadata.name="+name)
}

func commandUsesPositionalObjectName(command, resource, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok || verb != "get" {
			continue
		}
		if kubectlGetUsesPositionalObjectName(fields, verbIndex+1, resource, name) {
			return true
		}
	}
	return false
}

func kubectlGetUsesPositionalObjectName(fields []string, start int, resource, name string) bool {
	resource = strings.ToLower(strings.TrimSpace(resource))
	seenResource := false
	for i := start; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		if !seenResource {
			parts := strings.SplitN(field, "/", 2)
			if !resourceNamesEquivalent(parts[0], resource) {
				return false
			}
			seenResource = true
			if len(parts) == 2 && strings.TrimSpace(parts[1]) == name {
				return true
			}
			continue
		}
		return strings.TrimSpace(field) == name
	}
	return false
}

func commandUsesNamespace(command, namespace string) bool {
	if isAllNamespacesValue(namespace) {
		return commandUsesAllNamespaces(command)
	}
	if actual, ok := commandNamespaceValue(command); ok && actual == namespace {
		return true
	}
	return false
}

func commandNamespaceValue(command string) (string, bool) {
	fields := strings.Fields(command)
	for i, field := range fields {
		trimmed := strings.Trim(field, "'\"")
		if (trimmed == "-n" || trimmed == "--namespace") && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "'\""), true
		}
		if strings.HasPrefix(trimmed, "--namespace=") {
			return strings.TrimPrefix(trimmed, "--namespace="), true
		}
	}
	return "", false
}

func commandUsesAllNamespaces(command string) bool {
	for _, field := range strings.Fields(command) {
		trimmed := strings.Trim(field, "'\"")
		if trimmed == "-A" || trimmed == "--all-namespaces" || strings.HasPrefix(trimmed, "--all-namespaces=") {
			return true
		}
	}
	return false
}

func isAllNamespacesValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "all_namespaces", "all-namespaces", "*":
		return true
	default:
		return false
	}
}

func primaryMutatingKubectlResource(command string) (string, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok || !kube.IsKubectlMutatingVerb(verb) {
			continue
		}
		resource, ok := firstKubectlResourceArg(fields, verbIndex+1)
		if !ok {
			continue
		}
		resource = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(resource)))
		if resource != "" {
			return resource, true
		}
	}
	return "", false
}

func kubectlResourceUsuallyNamespaced(resource string) bool {
	resource = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(resource)))
	if resource == "" || resource == "unknown" {
		return false
	}
	switch resource {
	case "apiservice",
		"certificatesigningrequest",
		"clusterrole",
		"clusterrolebinding",
		"componentstatus",
		"csidriver",
		"csinode",
		"customresourcedefinition",
		"flowschema",
		"ingressclass",
		"mutatingwebhookconfiguration",
		"namespace",
		"node",
		"persistentvolume",
		"priorityclass",
		"prioritylevelconfiguration",
		"runtimeclass",
		"selfsubjectaccessreview",
		"selfsubjectrulesreview",
		"storageclass",
		"subjectaccessreview",
		"tokenreview",
		"validatingwebhookconfiguration",
		"volumeattachment":
		return false
	default:
		return true
	}
}

func cleanUnknownPlaceholder(value string) string {
	value = strings.TrimSpace(value)
	if isUnknownPlaceholder(value) {
		return ""
	}
	return value
}

func cleanNamespaceValue(value string) string {
	value = cleanUnknownPlaceholder(value)
	if value == "" || isAllNamespacesValue(value) {
		return value
	}
	if !isValidKubernetesNamespace(value) {
		return ""
	}
	return value
}

func isValidKubernetesNamespace(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 63 {
		return false
	}
	for i, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !valid {
			return false
		}
		if (i == 0 || i == len(value)-1) && r == '-' {
			return false
		}
	}
	return true
}

func isUnknownPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown", "unknown_name", "unknown-name", "undefined", "undefined_namespace", "undefined-namespace", "n/a", "na", "null", "none":
		return true
	default:
		return false
	}
}

func kubectlCommandFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	return kube.KubectlCommandFromFunctionCall(call)
}
func commandString(value any) (string, bool) { return kube.CommandString(value) }
func firstKubectlResourceArg(fields []string, start int) (string, bool) {
	return kube.FirstKubectlResourceArg(fields, start)
}
func kubectlResourceKindFromArg(arg string) string     { return kube.ResourceKindFromArg(arg) }
func kubectlFlagRequiresValue(flag string) bool        { return kube.FlagRequiresValue(flag) }
func kubectlShortFlagRequiresValue(flag string) bool   { return kube.ShortFlagRequiresValue(flag) }
func normalizeKubectlResource(resource string) string  { return kube.NormalizeResource(resource) }
func isBuiltinKubernetesResource(resource string) bool { return kube.IsBuiltinResource(resource) }
