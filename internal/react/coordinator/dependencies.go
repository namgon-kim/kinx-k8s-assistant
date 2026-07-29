package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/request"
	reactprompt "github.com/namgon-kim/kinx-k8s-assistant/internal/react/prompt"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/provider"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
)

func newModelClient(cfg *config.Config) (gollm.Client, error) {
	provider.Setup(cfg)
	client, err := gollm.NewClient(context.Background(), cfg.LLMProvider)
	if err != nil {
		return nil, fmt.Errorf("LLM 클라이언트 생성 실패 (%s): %w", cfg.LLMProvider, err)
	}
	return client, nil
}

func newExecutor() sandbox.Executor {
	return sandbox.NewLocalExecutor()
}

func newToolRegistry(ctx context.Context, executor sandbox.Executor, cfg *config.Config) (*toolconnector.Registry, error) {
	return toolconnector.NewRegistry(ctx, executor, cfg.MCPClient)
}

func newGuidanceClient(cfg *config.Config) (*guidance.Client, error) {
	return guidance.NewResourceGuideClient(cfg)
}

type loopDependencies struct {
	modelClient    func(*config.Config) (gollm.Client, error)
	executor       func() sandbox.Executor
	toolRegistry   func(context.Context, sandbox.Executor, *config.Config) (*toolconnector.Registry, error)
	guidanceClient func(*config.Config) (*guidance.Client, error)
}

func defaultLoopDependencies() loopDependencies {
	return loopDependencies{
		modelClient:    newModelClient,
		executor:       newExecutor,
		toolRegistry:   newToolRegistry,
		guidanceClient: newGuidanceClient,
	}
}

func (d loopDependencies) withDefaults() loopDependencies {
	defaults := defaultLoopDependencies()
	if d.modelClient == nil {
		d.modelClient = defaults.modelClient
	}
	if d.executor == nil {
		d.executor = defaults.executor
	}
	if d.toolRegistry == nil {
		d.toolRegistry = defaults.toolRegistry
	}
	if d.guidanceClient == nil {
		d.guidanceClient = defaults.guidanceClient
	}
	return d
}

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

var promptCache = struct {
	sync.Mutex
	values map[string]string
}{values: map[string]string{}}

func buildSystemPromptWithOptions(templateFile string, registry tools.Tools, opts promptOptions) (string, error) {
	path := templateFile
	if strings.TrimSpace(path) == "" {
		path = filepath.Join("prompts", "default.tmpl")
	}

	if len(opts.ToolProfile.ToolNames) == 0 {
		opts.ToolProfile = selectToolProfile(registry, request.General, "")
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

	result, err := reactprompt.Render(path, promptData{
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
	})
	if err != nil {
		return "", err
	}
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

func selectToolProfile(registry tools.Tools, _ request.Intent, _ string) ToolProfile {
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
		definition := cloneFunctionDefinition(tool.FunctionDefinition())
		augmentRuntimeActionMetadataSchema(definition)
		defs = append(defs, definition)
	}
	if includeInternal {
		defs = append(defs, internalStructuredFunctionDefinitions()...)
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name < defs[j].Name
	})
	return defs
}

func cloneFunctionDefinition(source *gollm.FunctionDefinition) *gollm.FunctionDefinition {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Parameters = cloneFunctionSchema(source.Parameters)
	return &clone
}

func cloneFunctionSchema(source *gollm.Schema) *gollm.Schema {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Required = append([]string(nil), source.Required...)
	clone.Items = cloneFunctionSchema(source.Items)
	if source.Properties != nil {
		clone.Properties = make(map[string]*gollm.Schema, len(source.Properties))
		for name, property := range source.Properties {
			clone.Properties[name] = cloneFunctionSchema(property)
		}
	}
	return &clone
}

func augmentRuntimeActionMetadataSchema(definition *gollm.FunctionDefinition) {
	if definition == nil || definition.Parameters == nil {
		return
	}
	if definition.Parameters.Properties == nil {
		definition.Parameters.Properties = map[string]*gollm.Schema{}
	}
	runtimeTargetName := "target"
	if definition.Parameters.Properties[runtimeTargetName] != nil {
		runtimeTargetName = "runtime_target"
	}
	definition.Parameters.Properties[runtimeTargetName] = buildFunctionSchema(reflect.TypeOf(contract.ActionTarget{}))
	definition.Parameters.Properties[runtimeTargetName].Description = "Coordinator-owned Kubernetes action target. This is named runtime_target only when the tool owns a different target argument."
	describeSchemaProperty(definition.Parameters.Properties[runtimeTargetName], "resource", "Concrete Kubernetes resource kind; required for a mutating action and never unknown.")
	describeSchemaProperty(definition.Parameters.Properties[runtimeTargetName], "namespace", "Exact namespace for a known namespaced target; include the same namespace in the command.")
	describeSchemaProperty(definition.Parameters.Properties[runtimeTargetName], "name", "Concrete resource name; required for a mutating action.")
	definition.Parameters.Properties["step_ref"] = buildFunctionSchema(reflect.TypeOf(StepRef{}))
	definition.Parameters.Properties["retry_of"] = &gollm.Schema{Type: gollm.TypeString}
	definition.Parameters.Properties["retry_reason"] = &gollm.Schema{Type: gollm.TypeString}
	definition.Parameters.Properties["changed_since"] = &gollm.Schema{
		Type:  gollm.TypeArray,
		Items: &gollm.Schema{Type: gollm.TypeString},
	}
	definition.Parameters.Properties["verification"] = mutationVerificationProposalSchema()
	if definition.Parameters.Properties["command"] != nil {
		risk := buildFunctionSchema(reflect.TypeOf(contract.CommandRisk{}))
		risk.Description = "Required runtime risk declaration for every command action."
		describeSchemaProperty(risk, "risky", "Set true when the exact command or its effects require explicit human review.")
		describeSchemaProperty(risk, "reason", "Required when risky=true. Explain the concrete risk shown to the user.")
		risk.Required = appendUniqueString(risk.Required, "risky")
		definition.Parameters.Properties["risk"] = risk
		definition.Parameters.Required = appendUniqueString(definition.Parameters.Required, "risk")
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func mutationVerificationProposalSchema() *gollm.Schema {
	schema := buildFunctionSchema(reflect.TypeOf(contract.VerificationSpec{}))
	schema.Description = "Required for every mutating action. Declares direct post-mutation verification; omit for ordinary read-only observations."
	describeSchemaProperty(schema, "shape", "Use single for one direct condition or chain for two or more distinct direct conditions evaluated in order.")
	describeSchemaProperty(schema, "mode", "For shape=single: immediate or await_state. Use await_state only when the same expected state may need temporal rechecks.")
	describeSchemaProperty(schema, "expected_state", "For shape=single: the exact state that proves the mutation directly affected its target.")
	describeSchemaProperty(schema, "initial_delay_seconds", "Optional delay before the first read-only verification, clamped by runtime to 0-30 seconds.")
	describeSchemaProperty(schema, "recheck_interval_seconds", "For await_state: delay between read-only rechecks, clamped by runtime to 1-30 seconds.")
	describeSchemaProperty(schema, "policy", "For shape=chain: must be ordered.")
	describeSchemaProperty(schema, "checks", "For shape=chain: at least two distinct direct checks. The runtime activates one check at a time in the declared order.")

	checks := schema.Properties["checks"]
	if checks == nil || checks.Items == nil {
		return schema
	}
	check := checks.Items
	describeSchemaProperty(check, "mode", "immediate or await_state for this check.")
	describeSchemaProperty(check, "target", "Optional concrete target override for this distinct direct check. Omit it to inherit the mutating action target.")
	describeSchemaProperty(check, "expected_state", "One exact state this check must establish.")
	describeSchemaProperty(check, "suggested_command", "A read-only kubectl command suitable for collecting this check's evidence.")
	describeSchemaProperty(check, "initial_delay_seconds", "Optional delay before the first observation for this check, clamped to 0-30 seconds.")
	describeSchemaProperty(check, "recheck_interval_seconds", "For await_state: delay between observations, clamped to 1-30 seconds.")
	if target := check.Properties["target"]; target != nil {
		describeSchemaProperty(target, "resource", "Concrete override resource kind; never use unknown.")
		describeSchemaProperty(target, "namespace", "Exact override namespace for a namespaced target.")
		describeSchemaProperty(target, "name", "Exact override resource name.")
	}
	return schema
}

func describeSchemaProperty(schema *gollm.Schema, name, description string) {
	if schema == nil || schema.Properties == nil || schema.Properties[name] == nil {
		return
	}
	schema.Properties[name].Description = description
}

func internalStructuredFunctionDefinitions() []*gollm.FunctionDefinition {
	return []*gollm.FunctionDefinition{
		internalStructuredFunctionDefinition(
			protocol.RequirementAnalysisCall,
			"Submit the required first-pass classification of the user's request before choosing any tool action.",
			requirementAnalysis{},
		),
		internalStructuredFunctionDefinition(
			protocol.RequestContextCall,
			"Submit the accepted runtime request context derived from requirement_analysis.",
			requestContext{},
		),
		internalStructuredFunctionDefinition(
			protocol.PhasePlanCall,
			"Submit the ordered forward-only phase plan before choosing actions for the accepted request.",
			phasePlan{},
		),
		internalStructuredFunctionDefinition(
			protocol.PhasePlanRevisionCall,
			"Replace the active and remaining plan graph using existing observation references while preserving phase and step goal lineage.",
			phasePlanRevision{},
		),
		internalStructuredFunctionDefinition(
			protocol.StepResultCall,
			"Close the active execution step as achieved, blocked, or requiring a plan revision, with runtime observation references.",
			stepResult{},
		),
		internalStructuredFunctionDefinition(
			protocol.PhaseProgressCall,
			"Complete or advance the active top-level phase_step when its completion condition is satisfied.",
			phaseProgress{},
		),
		internalStructuredFunctionDefinition(
			protocol.GuideProgressCall,
			"Record completion of a nested resource-guide diagnostic step while guided_diagnosis is active.",
			guideProgress{},
		),
		internalStructuredFunctionDefinition(
			protocol.ResourceGuideLookupCall,
			"Request a runtime-managed resource-guide lookup from the guidance_lookup phase for a CRD-backed resource family and operational problem focus.",
			resourceGuideLookup{},
		),
		internalStructuredFunctionDefinition(
			protocol.FinalReportCall,
			"Submit the structured final diagnostic report when enough evidence has been collected or blockers are known.",
			finalReport{},
		),
		internalStructuredFunctionDefinition(
			protocol.NextDirectionsCall,
			"Submit 1-3 continuation options after an inconclusive final_report.",
			nextDirections{},
		),
		internalStructuredFunctionDefinition(
			protocol.MutationVerificationResultCall,
			"Classify the active mutation verification as satisfied, waiting on the same await-state ID, or failed using exact runtime evidence references.",
			mutationVerificationResult{},
		),
		internalStructuredFunctionDefinition(
			protocol.ContinuationHandoffCall,
			"Close the bounded execution segment without resetting the active request, lineage, evidence, or safety budgets.",
			contract.ContinuationHandoff{},
		),
	}
}

func internalStructuredFunctionDefinition(name, description string, value any) *gollm.FunctionDefinition {
	parameters := buildFunctionSchema(reflect.TypeOf(value))
	if name == protocol.MutationVerificationResultCall {
		parameters.Description = "Required only when runtime requests the result for the active mutation verification."
		describeSchemaProperty(parameters, "verification_id", "Exact active verification ID supplied by runtime.")
		describeSchemaProperty(parameters, "status", "satisfied, waiting, or failed. waiting is valid only for the same await_state verification.")
		describeSchemaProperty(parameters, "evidence_refs", "Observation IDs recorded for the active verification; include its latest observation.")
		describeSchemaProperty(parameters, "evidence_summary", "Concise facts established by the referenced observations.")
		describeSchemaProperty(parameters, "reason", "Why the referenced evidence supports this status.")
		describeSchemaProperty(parameters, "next_action", "Required only for failed: a materially different diagnostic or remediation direction.")
	}
	if name == protocol.ContinuationHandoffCall {
		parameters.Description = "Required only when runtime requests bounded execution-segment closure. This does not create a new request or reset lineage and budgets."
		describeSchemaProperty(parameters, "request_id", "Exact active runtime request ID.")
		describeSchemaProperty(parameters, "goal_id", "Exact active runtime goal ID.")
		describeSchemaProperty(parameters, "current_judgement", "Concise evidence-based status at segment closure; do not claim completion.")
		describeSchemaProperty(parameters, "completed_steps", "Only terminal runtime step IDs from the execution anchor.")
		describeSchemaProperty(parameters, "evidence_refs", "Only existing runtime observation IDs.")
		describeSchemaProperty(parameters, "unresolved_steps", "Every active or pending runtime step ID; do not omit a nonterminal step.")
		describeSchemaProperty(parameters, "mandatory_obligations", "Every unresolved mandatory obligation supplied by runtime; do not omit any.")
		describeSchemaProperty(parameters, "recommended_next_step", "The first safe action or decision after explicit continuation.")
		describeSchemaProperty(parameters, "conclusive", "Must be false because this closes only the current bounded segment.")
	}
	return &gollm.FunctionDefinition{
		Name:        name,
		Description: description,
		Parameters:  parameters,
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
