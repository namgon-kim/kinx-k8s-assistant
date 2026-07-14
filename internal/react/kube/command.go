package kube

import (
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

func KubectlCommandFromFunctionCall(call gollm.FunctionCall) (string, bool) {
	return CommandString(call.Arguments["command"])
}

func CommandString(value any) (string, bool) {
	command, ok := value.(string)
	if !ok {
		return "", false
	}
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(strings.ToLower(command), "kubectl ") {
		return "", false
	}
	return command, true
}

func IsKubectlCommand(command string) bool {
	command = strings.TrimSpace(strings.ToLower(command))
	return command == "kubectl" || strings.HasPrefix(command, "kubectl ")
}
