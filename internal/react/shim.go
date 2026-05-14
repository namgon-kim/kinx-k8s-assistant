package react

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
)

type reActResponse struct {
	Thought string  `json:"thought"`
	Answer  string  `json:"answer,omitempty"`
	Action  *action `json:"action,omitempty"`
}

type action struct {
	Name             string `json:"name"`
	Reason           string `json:"reason"`
	Command          string `json:"command"`
	ModifiesResource string `json:"modifies_resource"`
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
		return nil, fmt.Errorf("no JSON code block found in shim response: %q", strings.TrimSpace(input))
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
	return fmt.Sprintf("Thought: %s\nAnswer: %s\nAction: %v", c.candidate.Thought, c.candidate.Answer, c.candidate.Action)
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
	if c.candidate.Action != nil {
		parts = append(parts, &shimPart{action: c.candidate.Action})
	}
	return parts
}

type shimPart struct {
	text   string
	action *action
}

func (p *shimPart) AsText() (string, bool) {
	return p.text, p.text != ""
}

func (p *shimPart) AsFunctionCalls() ([]gollm.FunctionCall, bool) {
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
