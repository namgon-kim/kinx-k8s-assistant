package react

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
)

type promptData struct {
	EnableToolUseShim    bool
	ToolsAsJSON          string
	ToolNames            string
	SessionIsInteractive bool
}

func buildSystemPrompt(templateFile string, registry tools.Tools, enableToolUseShim bool) (string, error) {
	path := templateFile
	if strings.TrimSpace(path) == "" {
		path = filepath.Join("prompts", "default.tmpl")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("system prompt 읽기 실패 (%s): %w", path, err)
	}

	defs := collectFunctionDefinitions(registry)
	rawDefs, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return "", fmt.Errorf("tool definition 직렬화 실패: %w", err)
	}

	tmpl, err := template.New(filepath.Base(path)).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("system prompt 템플릿 파싱 실패: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{
		EnableToolUseShim:    enableToolUseShim,
		ToolsAsJSON:          string(rawDefs),
		ToolNames:            strings.Join(registry.Names(), ", "),
		SessionIsInteractive: true,
	}); err != nil {
		return "", fmt.Errorf("system prompt 템플릿 실행 실패: %w", err)
	}
	return buf.String(), nil
}
