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
			message, invalid = l.requestNamespaceInvariantMessage(call)
		}
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

func (l *Loop) requestNamespaceInvariantMessage(call gollm.FunctionCall) (string, bool) {
	requestNamespace := l.requestScopeNamespace()
	if requestNamespace == "" {
		return "", false
	}
	command, ok := kubectlCommandFromFunctionCall(call)
	if !ok || !containsMutatingKubectlVerb(command) {
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
		if !ok || !isKubectlMutatingVerb(verb) {
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
