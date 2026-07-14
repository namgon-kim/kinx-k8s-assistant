package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

func Render(templateFile string, data any) (string, error) {
	content, err := os.ReadFile(templateFile)
	if err != nil {
		return "", fmt.Errorf("system prompt 읽기 실패 (%s): %w", templateFile, err)
	}
	tmpl, err := template.New(filepath.Base(templateFile)).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("system prompt 템플릿 파싱 실패: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return "", fmt.Errorf("system prompt 템플릿 실행 실패: %w", err)
	}
	return output.String(), nil
}
