package loganalyzer

import (
	"encoding/json"
	"strings"
)

type LogParser interface {
	Parse(rawLine string) LogEntry
}

type JSONLogParser struct{}

func (p *JSONLogParser) Parse(rawLine string) LogEntry {
	entry := LogEntry{
		Raw: rawLine,
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(rawLine), &data); err != nil {
		return entry
	}

	if ts, ok := data["@timestamp"].(string); ok {
		entry.Timestamp = ts
	} else if ts, ok := data["timestamp"].(string); ok {
		entry.Timestamp = ts
	} else if ts, ok := data["time"].(string); ok {
		entry.Timestamp = ts
	}

	if msg, ok := data["message"].(string); ok {
		entry.Message = msg
	} else if msg, ok := data["msg"].(string); ok {
		entry.Message = msg
	} else if msg, ok := data["log"].(string); ok {
		entry.Message = msg
	}

	if log, ok := data["log"].(map[string]any); ok {
		if level, ok := log["level"].(string); ok {
			entry.Level = strings.ToUpper(level)
		}
	}
	if entry.Level == "" {
		if level, ok := data["level"].(string); ok {
			entry.Level = strings.ToUpper(level)
		} else if level, ok := data["severity"].(string); ok {
			entry.Level = strings.ToUpper(level)
		}
	}

	if entry.Level == "" {
		entry.Level = "INFO"
	}

	return entry
}

func NewJSONParser() LogParser {
	return &JSONLogParser{}
}
