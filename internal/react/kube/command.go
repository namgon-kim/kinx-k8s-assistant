package kube

import (
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

func KubectlCommandFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	return KubectlCommandString(call.Arguments["command"])
}

func RawCommandString(value any) (string, bool) {
	command, ok := value.(string)
	if !ok {
		return "", false
	}
	command = strings.TrimSpace(command)
	return command, command != ""
}

func KubectlCommandString(value any) (string, bool) {
	command, ok := RawCommandString(value)
	if !ok {
		return "", false
	}
	if !IsKubectlCommand(command) {
		return "", false
	}
	return command, true
}

func IsKubectlCommand(command string) bool {
	command = strings.TrimSpace(strings.ToLower(command))
	return strings.HasPrefix(command, "kubectl ")
}
