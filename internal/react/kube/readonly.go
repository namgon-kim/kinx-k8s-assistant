// Package kube classifies shell-wrapped kubectl commands without
// executing them. It has no dependency on the ReAct runtime.
package kube

import "strings"

func ContainsDisallowedReadOnlyKubectlSubcommand(command string) bool {
	segments := splitShellPipeline(command)
	for _, segment := range segments {
		fields := strings.Fields(segment)
		if len(fields) == 0 || fields[0] != "kubectl" {
			continue
		}
		verb, verbIndex, ok := KubectlVerbAndIndexFromFields(fields, 0)
		if !ok || strings.ToLower(verb) != "auth" {
			continue
		}
		if !isReadOnlyKubectlSubcommand(fields, verbIndex) {
			return true
		}
	}
	return false
}
func ExtractShellScript(command string) (string, bool) {
	fields := ShellWords(command)
	if len(fields) < 3 || !isShellBinary(fields[0]) {
		return "", false
	}
	for i := 1; i < len(fields)-1; i++ {
		field := strings.TrimSpace(fields[i])
		if field == "-c" || isShellShortFlagWithC(field) {
			script := strings.TrimSpace(fields[i+1])
			return script, script != ""
		}
	}
	return "", false
}

func isShellShortFlagWithC(field string) bool {
	field = strings.TrimSpace(field)
	return strings.HasPrefix(field, "-") &&
		!strings.HasPrefix(field, "--") &&
		len(field) > 2 &&
		strings.Contains(field[1:], "c")
}

func isShellBinary(value string) bool {
	value = strings.Trim(strings.ToLower(value), "'\"")
	return value == "bash" || value == "sh" || strings.HasSuffix(value, "/bash") || strings.HasSuffix(value, "/sh")
}

func IsReadOnlyKubectlPipeline(command string) bool {
	segments := splitShellPipeline(command)
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if ContainsShellEvaluation(segment) || ContainsShellRedirection(segment) || ContainsMutatingKubectlVerb(segment) {
			return false
		}
	}

	firstFields := strings.Fields(segments[0])
	verb, verbIndex, ok := KubectlVerbAndIndexFromFields(firstFields, 0)
	if len(firstFields) < 2 || firstFields[0] != "kubectl" || !ok || !isKubectlReadOnlyVerb(verb) {
		return false
	}
	if !isReadOnlyKubectlSubcommand(firstFields, verbIndex) {
		return false
	}
	// Later pipeline segments are only allowed when they are local text
	// processors. This makes `kubectl get ... | tail -20` read-only while
	// keeping `kubectl get ... | kubectl apply -f -` blocked.
	for _, segment := range segments[1:] {
		fields := strings.Fields(segment)
		if len(fields) == 0 || !isSafeLocalPipelineCommand(fields[0]) {
			return false
		}
	}
	return true
}

func ContainsShellEvaluation(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle {
			continue
		}
		if ch == '`' {
			return true
		}
		if ch == '$' && i+1 < len(command) && command[i+1] == '(' {
			return true
		}
		if (ch == '<' || ch == '>') && i+1 < len(command) && command[i+1] == '(' {
			return true
		}
	}
	return containsHeredocOperator(command)
}

func containsHeredocOperator(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command)-1; i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && ch == '<' && command[i+1] == '<' {
			return true
		}
	}
	return false
}

func ContainsShellRedirection(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && (ch == '>' || ch == '<') {
			return true
		}
	}
	return false
}

func SplitShellCommandList(command string) []string {
	return splitShellBy(command, func(s string, i int) (int, bool) {
		switch s[i] {
		case ';':
			return 1, true
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				return 2, true
			}
		}
		return 0, false
	})
}

func splitShellPipeline(command string) []string {
	return splitShellBy(command, func(s string, i int) (int, bool) {
		if s[i] == '|' && !(i+1 < len(s) && s[i+1] == '|') {
			return 1, true
		}
		return 0, false
	})
}

func splitShellBy(command string, isSeparator func(string, int) (int, bool)) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			current.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			current.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			current.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if width, ok := isSeparator(command, i); ok {
				if segment := strings.TrimSpace(current.String()); segment != "" {
					segments = append(segments, segment)
				}
				current.Reset()
				i += width - 1
				continue
			}
		}
		current.WriteByte(ch)
	}
	if segment := strings.TrimSpace(current.String()); segment != "" {
		segments = append(segments, segment)
	}
	return segments
}

func ShellWords(command string) []string {
	var words []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, current.String())
		current.Reset()
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') {
			flush()
			continue
		}
		current.WriteByte(ch)
	}
	flush()
	return words
}

func ContainsMutatingKubectlVerb(segment string) bool {
	fields := ShellWords(strings.ToLower(segment))
	kubectlIndex, ok := kubectlExecutableIndexFromFields(fields)
	if !ok {
		return false
	}
	verb, verbIndex, ok := KubectlVerbAndIndexFromFields(fields, kubectlIndex)
	if !ok {
		return false
	}
	if IsKubectlMutatingVerb(verb) {
		return true
	}
	if subcommand, ok := kubectlSubcommandFromFields(fields, verbIndex); ok && isKubectlMutatingSubcommand(verb, subcommand) {
		return true
	}
	return false
}

func ContainsMutatingKubectlCommand(command string) bool {
	for _, segment := range splitShellPipeline(command) {
		if ContainsMutatingKubectlVerb(segment) {
			return true
		}
	}
	return false
}

func kubectlExecutableIndexFromFields(fields []string) (int, bool) {
	for i, field := range fields {
		field = strings.Trim(field, "'\"")
		if field == "" {
			continue
		}
		if i == 0 || isShellAssignment(fields[i-1]) {
			if isShellAssignment(field) {
				continue
			}
		}
		if field == "kubectl" {
			return i, true
		}
		return -1, false
	}
	return -1, false
}

func isShellAssignment(field string) bool {
	field = strings.Trim(field, "'\"")
	if field == "" || strings.HasPrefix(field, "-") {
		return false
	}
	index := strings.IndexByte(field, '=')
	if index <= 0 {
		return false
	}
	name := field[:index]
	for i, ch := range name {
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func KubectlVerbFromFields(fields []string, kubectlIndex int) (string, bool) {
	verb, _, ok := KubectlVerbAndIndexFromFields(fields, kubectlIndex)
	return verb, ok
}

func KubectlVerbAndIndexFromFields(fields []string, kubectlIndex int) (string, int, bool) {
	if kubectlIndex < 0 || kubectlIndex >= len(fields) || strings.Trim(fields[kubectlIndex], "'\"") != "kubectl" {
		return "", -1, false
	}
	for i := kubectlIndex + 1; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if field == "--" {
			if i+1 < len(fields) {
				return strings.ToLower(strings.Trim(fields[i+1], "'\"")), i + 1, true
			}
			return "", -1, false
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				if kubectlGlobalFlagName(strings.SplitN(field, "=", 2)[0]) {
					continue
				}
				return "", -1, false
			}
			if kubectlGlobalFlagRequiresValue(field) {
				if i+1 >= len(fields) {
					return "", -1, false
				}
				i++
				continue
			}
			if kubectlGlobalFlagName(field) {
				continue
			}
			return "", -1, false
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortGlobalFlagRequiresValue(field) && len(field) == 2 {
				if i+1 >= len(fields) {
					return "", -1, false
				}
				i++
				continue
			}
			if kubectlShortGlobalFlagName(field) {
				continue
			}
			return "", -1, false
		}
		return strings.ToLower(field), i, true
	}
	return "", -1, false
}

func kubectlSubcommandFromFields(fields []string, verbIndex int) (string, bool) {
	if verbIndex < 0 || verbIndex >= len(fields) {
		return "", false
	}
	for i := verbIndex + 1; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if FlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if ShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		return strings.ToLower(field), true
	}
	return "", false
}

func kubectlGlobalFlagRequiresValue(flag string) bool {
	switch flag {
	case "--as", "--as-group", "--cache-dir", "--certificate-authority", "--client-certificate",
		"--client-key", "--cluster", "--context", "--kubeconfig", "--log-flush-frequency",
		"--namespace", "--profile", "--profile-output", "--request-timeout", "--server",
		"--tls-server-name", "--token", "--user", "--v", "--vmodule":
		return true
	default:
		return false
	}
}

func kubectlGlobalFlagName(flag string) bool {
	if kubectlGlobalFlagRequiresValue(flag) {
		return true
	}
	switch flag {
	case "--alsologtostderr", "--insecure-skip-tls-verify", "--match-server-version",
		"--skip-headers", "--skip-log-headers", "--stderrthreshold", "--use-openapi-print-columns",
		"--warnings-as-errors":
		return true
	default:
		return false
	}
}

func kubectlShortGlobalFlagRequiresValue(flag string) bool {
	switch flag {
	case "-n", "-s", "-v":
		return true
	default:
		return false
	}
}

func kubectlShortGlobalFlagName(flag string) bool {
	return kubectlShortGlobalFlagRequiresValue(flag)
}

func isKubectlReadOnlyVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "get", "describe", "logs", "top", "api-resources", "api-versions", "version", "config", "auth":
		return true
	default:
		return false
	}
}

func isReadOnlyKubectlSubcommand(fields []string, verbIndex int) bool {
	if verbIndex < 0 || verbIndex >= len(fields) {
		return false
	}
	if strings.ToLower(strings.Trim(fields[verbIndex], "'\"")) != "auth" {
		return true
	}
	for i := verbIndex + 1; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlAuthFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlAuthShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		switch strings.ToLower(field) {
		case "can-i", "whoami":
			return true
		default:
			return false
		}
	}
	return false
}

func kubectlAuthFlagRequiresValue(flag string) bool {
	return FlagRequiresValue(flag)
}

func kubectlAuthShortFlagRequiresValue(flag string) bool {
	return ShortFlagRequiresValue(flag)
}

func IsKubectlMutatingVerb(verb string) bool {
	switch strings.ToLower(verb) {
	case "apply", "delete", "patch", "replace", "edit", "scale", "autoscale", "set", "create",
		"annotate", "label", "cordon", "uncordon", "drain", "taint", "expose", "run", "exec",
		"debug", "attach", "cp":
		return true
	default:
		return false
	}
}

func isKubectlMutatingSubcommand(verb, subcommand string) bool {
	switch strings.ToLower(verb) {
	case "rollout":
		switch strings.ToLower(subcommand) {
		case "pause", "restart", "resume", "undo":
			return true
		}
	case "auth":
		return strings.EqualFold(subcommand, "reconcile")
	case "certificate":
		switch strings.ToLower(subcommand) {
		case "approve", "deny":
			return true
		}
	}
	return false
}

func isSafeLocalPipelineCommand(command string) bool {
	switch strings.ToLower(command) {
	case "tail", "head", "grep", "egrep", "fgrep", "awk", "sed", "sort", "uniq", "wc", "cut", "jq", "yq", "column":
		return true
	default:
		return false
	}
}
