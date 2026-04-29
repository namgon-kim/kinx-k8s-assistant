package parser

import (
	"encoding/json"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type JSONParser struct{}

func NewJSONParser() LogParser {
	return &JSONParser{}
}

func (p *JSONParser) Parse(rawLine string) loganalyzer.LogEntry {
	entry := loganalyzer.LogEntry{
		Raw: rawLine,
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(rawLine), &data); err != nil {
		return entry
	}

	if ts, ok := data["@timestamp"].(string); ok {
		entry.Timestamp = ts
	}

	if msg, ok := data["message"].(string); ok {
		entry.Message = msg
	}

	if log, ok := data["log"].(map[string]interface{}); ok {
		if level, ok := log["level"].(string); ok {
			entry.Level = strings.ToUpper(level)
		}
	}

	if entry.Level == "" {
		entry.Level = "INFO"
	}

	return entry
}
