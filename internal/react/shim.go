package react

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

type reActResponse struct {
	Thought             string               `json:"thought"`
	Answer              string               `json:"answer,omitempty"`
	RequirementAnalysis *requirementAnalysis `json:"requirement_analysis,omitempty"`
	RequestContext      *requestContext      `json:"request_context,omitempty"`
	Action              *action              `json:"action,omitempty"`
	ResourceGuideLookup *resourceGuideLookup `json:"resource_guide_lookup,omitempty"`
	FinalReport         *finalReport         `json:"final_report,omitempty"`
	NextDirections      *nextDirections      `json:"next_directions,omitempty"`
}

type requirementAnalysis struct {
	RequestType string                    `json:"request_type"`
	Action      string                    `json:"action"`
	Target      requirementAnalysisTarget `json:"target"`
	Scope       requirementScope          `json:"scope,omitempty"`
	Resources   []requirementResource     `json:"resource_candidates,omitempty"`
	Evidence    []string                  `json:"evidence_needs,omitempty"`
	Constraints []string                  `json:"constraints,omitempty"`
	Ambiguities []string                  `json:"ambiguities,omitempty"`
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

type resourceGuideLookup struct {
	ResourceFamily string `json:"resource_family"`
	ProblemFocus   string `json:"problem_focus"`
	Reason         string `json:"reason"`
	Evidence       string `json:"evidence"`
}

type finalReport struct {
	Conclusive             bool     `json:"conclusive"`
	Conclusion             string   `json:"conclusion,omitempty"`
	Attempted              []string `json:"attempted,omitempty"`
	EvidenceKnown          []string `json:"evidence_known,omitempty"`
	EvidenceMissing        []string `json:"evidence_missing,omitempty"`
	MostLikelyCause        string   `json:"most_likely_cause,omitempty"`
	RecommendedUserActions []string `json:"recommended_user_actions,omitempty"`
	Blockers               []string `json:"blockers,omitempty"`
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
		answer := strings.TrimSpace(input)
		if answer == "" {
			return nil, fmt.Errorf("empty shim response")
		}
		return &reActResponse{Answer: answer}, nil
	}
	cleaned = repairUnescapedQuotesInJSONStrings(strings.TrimSpace(cleaned))

	var parsed reActResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("parsing shim JSON %q: %w", cleaned, err)
	}
	return &parsed, nil
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
	return fmt.Sprintf("Thought: %s\nAnswer: %s\nRequirementAnalysis: %v\nRequestContext: %v\nAction: %v\nResourceGuideLookup: %v\nFinalReport: %v\nNextDirections: %v", c.candidate.Thought, c.candidate.Answer, c.candidate.RequirementAnalysis, c.candidate.RequestContext, c.candidate.Action, c.candidate.ResourceGuideLookup, c.candidate.FinalReport, c.candidate.NextDirections)
}

func (c *shimCandidate) Parts() []gollm.Part {
	var parts []gollm.Part
	if c.candidate.Thought != "" {
		text := c.candidate.Thought
		if c.candidate.Answer != "" {
			text += "\n\n"
		}
		parts = append(parts, &shimPart{text: text})
	}
	if c.candidate.Answer != "" {
		parts = append(parts, &shimPart{text: c.candidate.Answer})
	}
	if c.candidate.RequirementAnalysis != nil {
		parts = append(parts, &shimPart{requirementAnalysis: c.candidate.RequirementAnalysis})
	}
	if c.candidate.RequestContext != nil {
		parts = append(parts, &shimPart{requestContext: c.candidate.RequestContext})
	}
	if c.candidate.Action != nil {
		parts = append(parts, &shimPart{action: c.candidate.Action})
	}
	if c.candidate.ResourceGuideLookup != nil {
		parts = append(parts, &shimPart{resourceGuideLookup: c.candidate.ResourceGuideLookup})
	}
	if c.candidate.FinalReport != nil {
		parts = append(parts, &shimPart{finalReport: c.candidate.FinalReport})
	}
	if c.candidate.NextDirections != nil {
		parts = append(parts, &shimPart{nextDirections: c.candidate.NextDirections})
	}
	return parts
}

type shimPart struct {
	text                string
	requirementAnalysis *requirementAnalysis
	requestContext      *requestContext
	action              *action
	resourceGuideLookup *resourceGuideLookup
	finalReport         *finalReport
	nextDirections      *nextDirections
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
