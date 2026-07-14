package coordinator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/request"
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
	prompt, err := buildSystemPromptForTest(templatePath, registry, false, true, "Korean", false)
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
	prompt, err := buildSystemPromptForTest(templatePath, registry, false, false, "Korean", false)
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
	prompt, err := buildSystemPromptForTest(templatePath, registry, false, false, "Korean", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "translate externally for Korean") {
		t.Fatalf("expected translation policy in prompt: %q", prompt)
	}
}

func buildSystemPromptForTest(templateFile string, registry tools.Tools, enableToolUseShim bool, readOnly bool, userLanguage string, translateOutput bool) (string, error) {
	return buildSystemPromptWithOptions(templateFile, registry, promptOptions{
		EnableToolUseShim:         enableToolUseShim,
		ReadOnly:                  readOnly,
		UserLanguage:              userLanguage,
		TranslateOutput:           translateOutput,
		IncludeGuidanceProtocol:   true,
		IncludeManifestGuidelines: true,
		ToolProfile:               selectToolProfile(registry, request.General, ""),
	})
}

func TestCollectFunctionDefinitionsIncludesInternalStructuredCalls(t *testing.T) {
	var registry tools.Tools
	registry.Init()

	defs := collectFunctionDefinitionsForProfile(registry, ToolProfile{Name: "empty"}, true)
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			t.Fatalf("internal function %q has empty description", def.Name)
		}
		if def.Parameters == nil {
			t.Fatalf("internal function %q has nil parameters", def.Name)
		}
	}

	for _, name := range []string{
		internalRequirementAnalysisCall,
		internalRequestContextCall,
		internalPhasePlanCall,
		internalPhaseProgressCall,
		internalGuideProgressCall,
		internalResourceGuideLookupCall,
		internalFinalReportCall,
		internalNextDirectionsCall,
		internalMutationVerificationResultCall,
	} {
		if !names[name] {
			t.Fatalf("expected internal function definition %q", name)
		}
	}
}
