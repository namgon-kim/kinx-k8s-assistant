package react

import (
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

func (l *Loop) rejectInconsistentActionTargets(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		message, invalid := inconsistentActionTargetMessage(call)
		if !invalid {
			continue
		}
		if !l.appendCorrectionWithCompaction("inconsistent_action_target", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 action target 불일치로 루프를 중단했습니다:\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.state = StateDone
			return true
		}
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
		return true
	}
	return false
}

func (l *Loop) rejectInvalidKubectlResources(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		command, ok := kubectlCommandFromFunctionCall(call)
		if !ok || !kubectlCommandUsesUnknownResource(command) {
			continue
		}
		message := fmt.Sprintf("Command %q uses \"unknown\" as a Kubernetes resource kind. `resource_class=unknown` is only a classification hint; never put `unknown` in primary_target.resource, action.target.resource, or a kubectl resource position. Return one corrected response with a concrete resource kind from the user request or observed evidence, or answer asking for clarification if no concrete resource kind is identifiable.", command)
		if !l.appendCorrectionWithCompaction("invalid_kubectl_resource", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 잘못된 kubectl 리소스로 루프를 중단했습니다:\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.state = StateDone
			return true
		}
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
		return true
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
		if !l.appendCorrectionWithCompaction("unrelated_first_diagnostic", message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 최초 진단 대상 불일치로 루프를 중단했습니다:\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.state = StateDone
			return true
		}
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
		return true
	}
	return false
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
	if target.Resource != "" && !commandMentionsResource(command, target.Resource) {
		return fmt.Sprintf("Action target declared resource %q, but command %q does not include that resource. Preserve the declared target and return one corrected next action.", target.Resource, command), true
	}
	if target.Name != "" && !commandMentionsToken(command, target.Name) && !commandUsesSelectorForName(command, target.Name) {
		return fmt.Sprintf("Action target declared name %q, but command %q does not include that name. Preserve the declared target and return one corrected next action.", target.Name, command), true
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
		Namespace: strings.TrimSpace(namespace),
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
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
	verb, ok := kubectlVerbFromFields(fields, 0)
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
		strings.Contains(lower, "cluster.x-k8s.io/cluster-name="+name)
}

func commandUsesNamespace(command, namespace string) bool {
	if isAllNamespacesValue(namespace) {
		return commandUsesAllNamespaces(command)
	}
	fields := strings.Fields(command)
	for i, field := range fields {
		trimmed := strings.Trim(field, "'\"")
		if (trimmed == "-n" || trimmed == "--namespace") && i+1 < len(fields) && strings.Trim(fields[i+1], "'\"") == namespace {
			return true
		}
		if strings.HasPrefix(trimmed, "--namespace=") && strings.TrimPrefix(trimmed, "--namespace=") == namespace {
			return true
		}
	}
	return false
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

func cleanUnknownPlaceholder(value string) string {
	value = strings.TrimSpace(value)
	if isUnknownPlaceholder(value) {
		return ""
	}
	return value
}

func isUnknownPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown", "unknown_name", "unknown-name", "n/a", "na", "null", "none":
		return true
	default:
		return false
	}
}
