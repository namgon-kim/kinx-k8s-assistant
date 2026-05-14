package react

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
)

func TestBuildSystemPromptIncludesReadOnlyInstructions(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "prompt.tmpl")
	if err := os.WriteFile(templatePath, []byte(`base
{{if .ReadOnly}}read-only enabled{{end}}
`), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	var registry tools.Tools
	registry.Init()
	prompt, err := buildSystemPrompt(templatePath, registry, false, true, "Korean", false)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if !strings.Contains(prompt, "read-only enabled") {
		t.Fatalf("expected read-only instructions in prompt: %q", prompt)
	}
}

func TestBuildSystemPromptOmitsReadOnlyInstructions(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "prompt.tmpl")
	if err := os.WriteFile(templatePath, []byte(`base
{{if .ReadOnly}}read-only enabled{{end}}
`), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	var registry tools.Tools
	registry.Init()
	prompt, err := buildSystemPrompt(templatePath, registry, false, false, "Korean", false)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if strings.Contains(prompt, "read-only enabled") {
		t.Fatalf("unexpected read-only instructions in prompt: %q", prompt)
	}
}

func TestBuildSystemPromptIncludesTranslateOutputPolicy(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "prompt.tmpl")
	if err := os.WriteFile(templatePath, []byte(`{{if .TranslateOutput}}translate externally for {{.UserLanguage}}{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	registry := tools.Tools{}
	prompt, err := buildSystemPrompt(templatePath, registry, false, false, "Korean", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "translate externally for Korean") {
		t.Fatalf("expected translation policy in prompt: %q", prompt)
	}
}
