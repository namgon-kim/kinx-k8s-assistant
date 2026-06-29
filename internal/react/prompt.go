package react

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
)

type PromptProfile struct {
	Name     string
	Sections []PromptSection
	Hash     string
}

type PromptSection struct {
	Name     string
	Required bool
	Enabled  bool
}

type ToolProfile struct {
	Name      string
	ToolNames []string
	Hash      string
}

type promptOptions struct {
	EnableToolUseShim          bool
	ReadOnly                   bool
	UserLanguage               string
	TranslateOutput            bool
	IncludeGuidanceProtocol    bool
	IncludeManifestGuidelines  bool
	IncludeClusterAPIGuardrail bool
	ToolProfile                ToolProfile
}

type promptData struct {
	EnableToolUseShim          bool
	ToolsAsJSON                string
	ToolNames                  string
	SessionIsInteractive       bool
	ReadOnly                   bool
	UserLanguage               string
	TranslateOutput            bool
	IncludeGuidanceProtocol    bool
	IncludeManifestGuidelines  bool
	IncludeClusterAPIGuardrail bool
}

func buildSystemPrompt(templateFile string, registry tools.Tools, enableToolUseShim bool, readOnly bool, userLanguage string, translateOutput bool) (string, error) {
	return buildSystemPromptWithOptions(templateFile, registry, promptOptions{
		EnableToolUseShim:         enableToolUseShim,
		ReadOnly:                  readOnly,
		UserLanguage:              userLanguage,
		TranslateOutput:           translateOutput,
		IncludeGuidanceProtocol:   true,
		IncludeManifestGuidelines: true,
		ToolProfile:               selectToolProfile(registry, RequestIntentGeneral, ""),
	})
}

var promptCache = struct {
	sync.Mutex
	values map[string]string
}{values: map[string]string{}}

func buildSystemPromptWithOptions(templateFile string, registry tools.Tools, opts promptOptions) (string, error) {
	path := templateFile
	if strings.TrimSpace(path) == "" {
		path = filepath.Join("prompts", "default.tmpl")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("system prompt 읽기 실패 (%s): %w", path, err)
	}

	if len(opts.ToolProfile.ToolNames) == 0 {
		opts.ToolProfile = selectToolProfile(registry, RequestIntentGeneral, "")
	}
	profile := buildPromptProfile(opts)
	cacheKey := strings.Join([]string{
		path,
		profile.Hash,
		opts.ToolProfile.Hash,
		fmt.Sprintf("shim=%v", opts.EnableToolUseShim),
		fmt.Sprintf("readonly=%v", opts.ReadOnly),
		opts.UserLanguage,
		fmt.Sprintf("translate=%v", opts.TranslateOutput),
	}, "|")
	promptCache.Lock()
	if cached, ok := promptCache.values[cacheKey]; ok {
		promptCache.Unlock()
		return cached, nil
	}
	promptCache.Unlock()

	defs := collectFunctionDefinitionsForProfile(registry, opts.ToolProfile, false)
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
		EnableToolUseShim:          opts.EnableToolUseShim,
		ToolsAsJSON:                string(rawDefs),
		ToolNames:                  strings.Join(opts.ToolProfile.ToolNames, ", "),
		SessionIsInteractive:       true,
		ReadOnly:                   opts.ReadOnly,
		UserLanguage:               opts.UserLanguage,
		TranslateOutput:            opts.TranslateOutput,
		IncludeGuidanceProtocol:    opts.IncludeGuidanceProtocol,
		IncludeManifestGuidelines:  opts.IncludeManifestGuidelines,
		IncludeClusterAPIGuardrail: opts.IncludeClusterAPIGuardrail,
	}); err != nil {
		return "", fmt.Errorf("system prompt 템플릿 실행 실패: %w", err)
	}
	result := buf.String()
	promptCache.Lock()
	promptCache.values[cacheKey] = result
	promptCache.Unlock()
	return result, nil
}

func buildPromptProfile(opts promptOptions) PromptProfile {
	sections := []PromptSection{
		{Name: "core_react", Required: true, Enabled: true},
		{Name: "output_contract", Required: true, Enabled: true},
		{Name: "language_policy", Required: true, Enabled: true},
		{Name: "readonly", Enabled: opts.ReadOnly},
		{Name: "guidance_protocol", Enabled: opts.IncludeGuidanceProtocol},
		{Name: "target_scope_preservation", Required: true, Enabled: true},
		{Name: "cluster_api_post_rag", Enabled: opts.IncludeClusterAPIGuardrail},
		{Name: "command_guidelines", Required: true, Enabled: true},
		{Name: "manifest_generation", Enabled: opts.IncludeManifestGuidelines},
	}
	var enabled []string
	for _, section := range sections {
		if section.Required || section.Enabled {
			enabled = append(enabled, section.Name)
		}
	}
	return PromptProfile{
		Name:     "runtime",
		Sections: sections,
		Hash:     shortHash(strings.Join(enabled, "|")),
	}
}

func selectToolProfile(registry tools.Tools, _ RequestIntent, _ string) ToolProfile {
	// Tool schema pruning is intentionally conservative. The runtime cannot
	// reliably know every tool a model may need from the user's wording alone,
	// so the profile keeps the full registered tool set and only gives it a
	// stable hash/name for prompt caching and future provider-side references.
	names := append([]string(nil), registry.Names()...)
	sort.Strings(names)
	return ToolProfile{
		Name:      "all",
		ToolNames: names,
		Hash:      shortHash(strings.Join(names, "|")),
	}
}

func collectFunctionDefinitionsForProfile(registry tools.Tools, profile ToolProfile, includeInternal bool) []*gollm.FunctionDefinition {
	defs := make([]*gollm.FunctionDefinition, 0, len(profile.ToolNames)+8)
	for _, name := range profile.ToolNames {
		tool := registry.Lookup(name)
		if tool == nil {
			continue
		}
		defs = append(defs, tool.FunctionDefinition())
	}
	if includeInternal {
		defs = append(defs, internalStructuredFunctionDefinitions()...)
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name < defs[j].Name
	})
	return defs
}

func internalStructuredFunctionDefinitions() []*gollm.FunctionDefinition {
	return []*gollm.FunctionDefinition{
		internalStructuredFunctionDefinition(
			internalRequirementAnalysisCall,
			"Submit the required first-pass classification of the user's request before choosing any tool action.",
			requirementAnalysis{},
		),
		internalStructuredFunctionDefinition(
			internalRequestContextCall,
			"Submit the accepted runtime request context derived from requirement_analysis.",
			requestContext{},
		),
		internalStructuredFunctionDefinition(
			internalPhasePlanCall,
			"Submit the ordered forward-only phase plan before choosing actions for the accepted request.",
			phasePlan{},
		),
		internalStructuredFunctionDefinition(
			internalPhaseProgressCall,
			"Complete or advance the active top-level phase_step when its completion condition is satisfied.",
			phaseProgress{},
		),
		internalStructuredFunctionDefinition(
			internalGuideProgressCall,
			"Record completion of a nested resource-guide diagnostic step while guided_diagnosis is active.",
			guideProgress{},
		),
		internalStructuredFunctionDefinition(
			internalResourceGuideLookupCall,
			"Request a runtime-managed resource-guide lookup from the guidance_lookup phase for a CRD-backed resource family and operational problem focus.",
			resourceGuideLookup{},
		),
		internalStructuredFunctionDefinition(
			internalFinalReportCall,
			"Submit the structured final diagnostic report when enough evidence has been collected or blockers are known.",
			finalReport{},
		),
		internalStructuredFunctionDefinition(
			internalNextDirectionsCall,
			"Submit 1-3 continuation options after an inconclusive final_report.",
			nextDirections{},
		),
		internalStructuredFunctionDefinition(
			internalMutationVerificationResultCall,
			"Interpret collected mutation verification evidence before final reporting or further remediation.",
			mutationVerificationResult{},
		),
	}
}

func internalStructuredFunctionDefinition(name, description string, value any) *gollm.FunctionDefinition {
	return &gollm.FunctionDefinition{
		Name:        name,
		Description: description,
		Parameters:  buildFunctionSchema(reflect.TypeOf(value)),
	}
}

func buildFunctionSchema(t reflect.Type) *gollm.Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return &gollm.Schema{Type: gollm.TypeString}
	case reflect.Bool:
		return &gollm.Schema{Type: gollm.TypeBoolean}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &gollm.Schema{Type: gollm.TypeInteger}
	case reflect.Slice, reflect.Array:
		return &gollm.Schema{Type: gollm.TypeArray, Items: buildFunctionSchema(t.Elem())}
	case reflect.Map:
		return &gollm.Schema{Type: gollm.TypeObject}
	case reflect.Struct:
		schema := &gollm.Schema{
			Type:       gollm.TypeObject,
			Properties: map[string]*gollm.Schema{},
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name, required, ok := jsonFieldName(field)
			if !ok {
				continue
			}
			schema.Properties[name] = buildFunctionSchema(field.Type)
			if required {
				schema.Required = append(schema.Required, name)
			}
		}
		return schema
	default:
		return &gollm.Schema{Type: gollm.TypeString}
	}
}

func jsonFieldName(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	if tag == "" {
		return field.Name, true, true
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}
	required := true
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			required = false
			break
		}
	}
	return name, required, true
}

func shortHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:8])
}
