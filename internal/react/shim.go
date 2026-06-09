package react

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

type reActResponse struct {
	Thought                    string               `json:"thought"`
	Answer                     string               `json:"answer,omitempty"`
	RequirementAnalysis        *requirementAnalysis `json:"requirement_analysis,omitempty"`
	RequestContext             *requestContext      `json:"request_context,omitempty"`
	PhasePlan                  *phasePlan           `json:"phase_plan,omitempty"`
	PhaseProgress              *phaseProgress       `json:"phase_progress,omitempty"`
	Action                     *action              `json:"action,omitempty"`
	GuideProgress              *guideProgress       `json:"guide_progress,omitempty"`
	ResourceGuideLookup        *resourceGuideLookup `json:"resource_guide_lookup,omitempty"`
	FinalReport                *finalReport         `json:"final_report,omitempty"`
	NextDirections             *nextDirections      `json:"next_directions,omitempty"`
	InvalidPhaseProgress       bool                 `json:"-"`
	InvalidAction              bool                 `json:"-"`
	InvalidResourceGuideLookup bool                 `json:"-"`
	InvalidFinalReport         bool                 `json:"-"`
	InvalidDirections          bool                 `json:"-"`
	InvalidStructuredAnswer    bool                 `json:"-"`
}

type requirementAnalysis struct {
	RequestType      string                       `json:"request_type"`
	Action           string                       `json:"action"`
	Target           requirementAnalysisTarget    `json:"target"`
	Scope            requirementScope             `json:"scope,omitempty"`
	Resources        []requirementResource        `json:"resource_candidates,omitempty"`
	OperationalFocus *requirementOperationalFocus `json:"operational_focus,omitempty"`
	Evidence         []string                     `json:"evidence_needs,omitempty"`
	Constraints      []string                     `json:"constraints,omitempty"`
	Ambiguities      []string                     `json:"ambiguities,omitempty"`
}

type requirementAnalysisTarget struct {
	Category    string `json:"category"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type requirementScope struct {
	Type      string `json:"type,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type requirementResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"`
}

type requirementOperationalFocus struct {
	Summary               string                       `json:"summary,omitempty"`
	RelationshipToPrimary string                       `json:"relationship_to_primary,omitempty"`
	ChangedFromPrevious   bool                         `json:"changed_from_previous,omitempty"`
	Reason                string                       `json:"reason,omitempty"`
	RelatedResourceHints  []requirementRelatedResource `json:"related_resource_hints,omitempty"`
	EvidenceNeeds         []string                     `json:"evidence_needs,omitempty"`
}

type requirementRelatedResource struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type requestContext struct {
	PrimaryTarget requestPrimaryTarget `json:"primary_target"`
	Scope         requestScope         `json:"scope,omitempty"`
	ResourceClass string               `json:"resource_class"`
}

type requestPrimaryTarget struct {
	Resource string `json:"resource"`
	Name     string `json:"name,omitempty"`
}

type requestScope struct {
	Namespace string `json:"namespace,omitempty"`
}

type action struct {
	Name                string         `json:"name"`
	Reason              string         `json:"reason"`
	Goal                string         `json:"goal,omitempty"`
	Target              *actionTarget  `json:"target,omitempty"`
	Command             string         `json:"command"`
	ExpectedObservation string         `json:"expected_observation,omitempty"`
	ModifiesResource    string         `json:"modifies_resource"`
	GuideProgress       *guideProgress `json:"guide_progress,omitempty"`
}

type actionTarget struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

type guideProgress struct {
	StepCompleted  int  `json:"step_completed,omitempty"`
	EvidenceUseful bool `json:"evidence_useful,omitempty"`
}

type phasePlan struct {
	RequestGoal       string      `json:"request_goal"`
	CurrentPhaseIndex int         `json:"current_phase_index,omitempty"`
	PhaseSteps        []phaseStep `json:"phase_steps,omitempty"`
}

type phaseStep struct {
	Index               int      `json:"index"`
	Name                string   `json:"name"`
	Goal                string   `json:"goal"`
	CompletionCondition string   `json:"completion_condition"`
	AllowedNext         []string `json:"allowed_next,omitempty"`
}

type phaseProgress struct {
	PhaseCompleted   int    `json:"phase_completed"`
	EvidenceUseful   bool   `json:"evidence_useful,omitempty"`
	CompletionReason string `json:"completion_reason,omitempty"`
	NextPhase        string `json:"next_phase,omitempty"`
}

type resourceGuideLookup struct {
	ResourceFamily string `json:"resource_family"`
	ProblemFocus   string `json:"problem_focus"`
	Reason         string `json:"reason"`
	Evidence       string `json:"evidence"`
}

type finalReport struct {
	Conclusive             bool                  `json:"conclusive"`
	Conclusion             string                `json:"conclusion,omitempty"`
	Attempted              []string              `json:"attempted,omitempty"`
	EvidenceKnown          []string              `json:"evidence_known,omitempty"`
	EvidenceMissing        []string              `json:"evidence_missing,omitempty"`
	MostLikelyCause        string                `json:"most_likely_cause,omitempty"`
	RecommendedUserActions []string              `json:"recommended_user_actions,omitempty"`
	ProblematicResources   []problematicResource `json:"problematic_resources,omitempty"`
	Blockers               []string              `json:"blockers,omitempty"`
}

type problematicResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type nextDirections struct {
	Note    string                `json:"note,omitempty"`
	Options []nextDirectionOption `json:"options"`
}

type nextDirectionOption struct {
	Kind           string `json:"kind"`
	Summary        string `json:"summary"`
	Why            string `json:"why,omitempty"`
	ResourceFamily string `json:"resource_family,omitempty"`
	ProblemFocus   string `json:"problem_focus,omitempty"`
	Instruction    string `json:"instruction,omitempty"`
	ResourceKind   string `json:"resource_kind,omitempty"`
	ResourceName   string `json:"resource_name,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
}

func candidateToShimCandidate(iterator gollm.ChatResponseIterator) (gollm.ChatResponseIterator, error) {
	return func(yield func(gollm.ChatResponse, error) bool) {
		var buffer strings.Builder
		for response, err := range iterator {
			if err != nil {
				yield(nil, err)
				return
			}
			if response == nil {
				break
			}
			if len(response.Candidates()) == 0 {
				yield(nil, fmt.Errorf("no candidates in LLM response"))
				return
			}
			for _, part := range response.Candidates()[0].Parts() {
				text, ok := part.AsText()
				if !ok {
					yield(nil, fmt.Errorf("shim mode expects text-only LLM response"))
					return
				}
				buffer.WriteString(text)
			}
		}

		if strings.TrimSpace(buffer.String()) == "" {
			yield(nil, nil)
			return
		}

		parsed, err := parseReActResponse(buffer.String())
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&shimResponse{candidate: parsed}, nil)
	}, nil
}

func parseReActResponse(input string) (*reActResponse, error) {
	cleaned, found := extractJSON(input)
	if !found {
		if strings.TrimSpace(input) == "" {
			return nil, fmt.Errorf("empty shim response")
		}
		return nil, fmt.Errorf("shim response missing json code block")
	}
	cleaned = repairUnescapedQuotesInJSONStrings(strings.TrimSpace(cleaned))

	parsed, err := unmarshalReActResponse([]byte(cleaned))
	if err != nil {
		return nil, fmt.Errorf("parsing shim JSON %q: %w", cleaned, err)
	}
	return parsed, nil
}

func unmarshalReActResponse(data []byte) (*reActResponse, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	parsed := &reActResponse{}
	if err := unmarshalOptional(raw, "thought", &parsed.Thought); err != nil {
		return nil, err
	}
	if err := unmarshalOptional(raw, "answer", &parsed.Answer); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPointer(raw, "requirement_analysis", &parsed.RequirementAnalysis); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPointer(raw, "request_context", &parsed.RequestContext); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPhasePlan(raw, &parsed.PhasePlan); err != nil {
		return nil, err
	}
	unmarshalOptionalPointerOrInvalid(raw, "phase_progress", &parsed.PhaseProgress, &parsed.InvalidPhaseProgress)
	unmarshalOptionalPointerOrInvalid(raw, "action", &parsed.Action, &parsed.InvalidAction)
	if err := unmarshalOptionalPointer(raw, "guide_progress", &parsed.GuideProgress); err != nil {
		return nil, err
	}
	if parsed.Action != nil && parsed.Action.GuideProgress == nil && parsed.GuideProgress != nil {
		parsed.Action.GuideProgress = parsed.GuideProgress
	}
	unmarshalOptionalPointerOrInvalid(raw, "resource_guide_lookup", &parsed.ResourceGuideLookup, &parsed.InvalidResourceGuideLookup)
	if rawFinal, ok := raw["final_report"]; ok && string(rawFinal) != "null" {
		var reportArgs map[string]any
		if err := json.Unmarshal(rawFinal, &reportArgs); err != nil {
			parsed.InvalidFinalReport = true
		} else {
			report := finalReportFromArguments(reportArgs)
			parsed.FinalReport = &report
		}
	}
	if rawDirections, ok := raw["next_directions"]; ok && string(rawDirections) != "null" {
		var directions nextDirections
		if err := json.Unmarshal(rawDirections, &directions); err != nil {
			parsed.InvalidDirections = true
		} else {
			parsed.NextDirections = &directions
		}
	}
	if parsed.Answer != "" && parsed.hasStructuredOutput() {
		parsed.InvalidStructuredAnswer = true
	}
	return parsed, nil
}

func unmarshalOptional(raw map[string]json.RawMessage, key string, target any) error {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return nil
	}
	return json.Unmarshal(value, target)
}

func unmarshalOptionalPointer[T any](raw map[string]json.RawMessage, key string, target **T) error {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return nil
	}
	var parsed T
	if err := json.Unmarshal(value, &parsed); err != nil {
		return err
	}
	*target = &parsed
	return nil
}

func unmarshalOptionalPointerOrInvalid[T any](raw map[string]json.RawMessage, key string, target **T, invalid *bool) {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return
	}
	var parsed T
	if err := json.Unmarshal(value, &parsed); err != nil {
		*invalid = true
		return
	}
	*target = &parsed
}

func unmarshalOptionalPhasePlan(raw map[string]json.RawMessage, target **phasePlan) error {
	value, ok := raw["phase_plan"]
	if !ok || string(value) == "null" {
		return nil
	}
	var parsed phasePlan
	if err := json.Unmarshal(value, &parsed); err == nil {
		if len(parsed.PhaseSteps) > 0 {
			*target = &parsed
			return nil
		}
	}
	var compat struct {
		RequestGoal       string      `json:"request_goal"`
		CurrentPhaseIndex int         `json:"current_phase_index,omitempty"`
		Phases            []phaseStep `json:"phases,omitempty"`
	}
	if err := json.Unmarshal(value, &compat); err != nil {
		return err
	}
	parsed = phasePlan{
		RequestGoal:       compat.RequestGoal,
		CurrentPhaseIndex: compat.CurrentPhaseIndex,
		PhaseSteps:        compat.Phases,
	}
	*target = &parsed
	return nil
}

func extractJSON(input string) (string, bool) {
	const marker = "```json"
	first := strings.Index(input, marker)
	last := strings.LastIndex(input, "```")
	if first == -1 || last == -1 || first == last {
		return "", false
	}
	return input[first+len(marker) : last], true
}

func repairUnescapedQuotesInJSONStrings(input string) string {
	var out strings.Builder
	out.Grow(len(input))

	inString := false
	escaped := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if inString {
			if escaped {
				out.WriteByte(ch)
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				if i+1 < len(input) && input[i+1] == '\'' {
					out.WriteByte('\'')
					i++
					continue
				}
				out.WriteByte(ch)
				escaped = true
			case '"':
				if isJSONStringTerminator(input, i) {
					out.WriteByte(ch)
					inString = false
				} else {
					out.WriteString(`\"`)
				}
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if ch < 0x20 {
					out.WriteString(fmt.Sprintf(`\u%04x`, ch))
					continue
				}
				out.WriteByte(ch)
			}
			continue
		}

		if ch == '"' {
			inString = true
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func isJSONStringTerminator(input string, quoteIndex int) bool {
	for i := quoteIndex + 1; i < len(input); i++ {
		switch input[i] {
		case ' ', '\n', '\r', '\t':
			continue
		case ':', ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

type shimResponse struct {
	candidate *reActResponse
}

func (r *shimResponse) UsageMetadata() any {
	return nil
}

func (r *shimResponse) Candidates() []gollm.Candidate {
	return []gollm.Candidate{&shimCandidate{candidate: r.candidate}}
}

type shimCandidate struct {
	candidate *reActResponse
}

func (c *shimCandidate) String() string {
	return fmt.Sprintf("Thought: %s\nAnswer: %s\nRequirementAnalysis: %v\nRequestContext: %v\nPhasePlan: %v\nPhaseProgress: %v\nAction: %v\nGuideProgress: %v\nResourceGuideLookup: %v\nFinalReport: %v\nInvalidFinalReport: %v\nNextDirections: %v\nInvalidDirections: %v", c.candidate.Thought, c.candidate.Answer, c.candidate.RequirementAnalysis, c.candidate.RequestContext, c.candidate.PhasePlan, c.candidate.PhaseProgress, c.candidate.Action, c.candidate.GuideProgress, c.candidate.ResourceGuideLookup, c.candidate.FinalReport, c.candidate.InvalidFinalReport, c.candidate.NextDirections, c.candidate.InvalidDirections)
}

func (c *shimCandidate) Parts() []gollm.Part {
	var parts []gollm.Part
	structured := c.hasStructuredOutput()
	if c.candidate.Thought != "" && (structured || c.candidate.Answer == "") {
		parts = append(parts, &shimPart{text: c.candidate.Thought})
	}
	if c.candidate.Answer != "" && !structured {
		parts = append(parts, &shimPart{text: c.candidate.Answer})
	}
	if c.candidate.RequirementAnalysis != nil {
		parts = append(parts, &shimPart{requirementAnalysis: c.candidate.RequirementAnalysis})
	}
	if c.candidate.RequestContext != nil {
		parts = append(parts, &shimPart{requestContext: c.candidate.RequestContext})
	}
	if c.candidate.PhasePlan != nil {
		parts = append(parts, &shimPart{phasePlan: c.candidate.PhasePlan})
	}
	if c.candidate.PhaseProgress != nil {
		parts = append(parts, &shimPart{phaseProgress: c.candidate.PhaseProgress})
	} else if c.candidate.InvalidPhaseProgress {
		parts = append(parts, &shimPart{invalidPhaseProgress: true})
	}
	if c.candidate.Action != nil {
		parts = append(parts, &shimPart{action: c.candidate.Action})
	} else if c.candidate.InvalidAction {
		parts = append(parts, &shimPart{invalidAction: true})
	}
	if c.candidate.ResourceGuideLookup != nil {
		parts = append(parts, &shimPart{resourceGuideLookup: c.candidate.ResourceGuideLookup})
	} else if c.candidate.InvalidResourceGuideLookup {
		parts = append(parts, &shimPart{invalidResourceGuideLookup: true})
	}
	if c.candidate.FinalReport != nil {
		parts = append(parts, &shimPart{finalReport: c.candidate.FinalReport})
	} else if c.candidate.InvalidFinalReport {
		parts = append(parts, &shimPart{invalidFinalReport: true})
	}
	if c.candidate.NextDirections != nil {
		parts = append(parts, &shimPart{nextDirections: c.candidate.NextDirections})
	} else if c.candidate.InvalidDirections {
		parts = append(parts, &shimPart{invalidDirections: true})
	}
	if c.candidate.InvalidStructuredAnswer {
		parts = append(parts, &shimPart{invalidStructuredAnswer: true})
	}
	return parts
}

func (c *shimCandidate) hasStructuredOutput() bool {
	return c.candidate.hasStructuredOutput()
}

func (c *reActResponse) hasStructuredOutput() bool {
	return c.RequirementAnalysis != nil ||
		c.RequestContext != nil ||
		c.PhasePlan != nil ||
		c.PhaseProgress != nil ||
		c.InvalidPhaseProgress ||
		c.Action != nil ||
		c.InvalidAction ||
		c.ResourceGuideLookup != nil ||
		c.InvalidResourceGuideLookup ||
		c.FinalReport != nil ||
		c.InvalidFinalReport ||
		c.NextDirections != nil ||
		c.InvalidDirections
}

type shimPart struct {
	text                       string
	requirementAnalysis        *requirementAnalysis
	requestContext             *requestContext
	phasePlan                  *phasePlan
	phaseProgress              *phaseProgress
	invalidPhaseProgress       bool
	action                     *action
	invalidAction              bool
	resourceGuideLookup        *resourceGuideLookup
	invalidResourceGuideLookup bool
	finalReport                *finalReport
	invalidFinalReport         bool
	nextDirections             *nextDirections
	invalidDirections          bool
	invalidStructuredAnswer    bool
}

func (p *shimPart) AsText() (string, bool) {
	return p.text, p.text != ""
}

func (p *shimPart) AsFunctionCalls() ([]gollm.FunctionCall, bool) {
	if p.requirementAnalysis != nil {
		args, err := toMap(p.requirementAnalysis)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalRequirementAnalysisCall,
			Arguments: args,
		}}, true
	}
	if p.requestContext != nil {
		args, err := toMap(p.requestContext)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalRequestContextCall,
			Arguments: args,
		}}, true
	}
	if p.phasePlan != nil {
		args, err := toMap(p.phasePlan)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalPhasePlanCall,
			Arguments: args,
		}}, true
	}
	if p.phaseProgress != nil {
		args, err := toMap(p.phaseProgress)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalPhaseProgressCall,
			Arguments: args,
		}}, true
	}
	if p.invalidPhaseProgress {
		return []gollm.FunctionCall{{
			Name:      internalPhaseProgressCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.resourceGuideLookup != nil {
		args, err := toMap(p.resourceGuideLookup)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalResourceGuideLookupCall,
			Arguments: args,
		}}, true
	}
	if p.invalidResourceGuideLookup {
		return []gollm.FunctionCall{{
			Name:      internalResourceGuideLookupCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.finalReport != nil {
		args, err := toMap(p.finalReport)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalFinalReportCall,
			Arguments: args,
		}}, true
	}
	if p.invalidFinalReport {
		return []gollm.FunctionCall{{
			Name:      internalFinalReportCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.nextDirections != nil {
		args, err := toMap(p.nextDirections)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalNextDirectionsCall,
			Arguments: args,
		}}, true
	}
	if p.invalidDirections {
		return []gollm.FunctionCall{{
			Name:      internalNextDirectionsCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.invalidStructuredAnswer {
		return []gollm.FunctionCall{{
			Name:      internalInvalidStructuredOutputCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.invalidAction {
		return []gollm.FunctionCall{{
			Name:      internalInvalidActionCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.action == nil {
		return nil, false
	}
	args, err := toMap(p.action)
	if err != nil {
		return nil, false
	}
	delete(args, "name")
	return []gollm.FunctionCall{{
		Name:      p.action.Name,
		Arguments: args,
	}}, true
}

func toMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("converting %T to json: %w", v, err)
	}
	result := make(map[string]any)
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("converting json to map: %w", err)
	}
	return result, nil
}
