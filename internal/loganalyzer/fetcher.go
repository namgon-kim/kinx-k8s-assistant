package loganalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type LogFetcher interface {
	Fetch(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error)
}

type logFetcher struct {
	logDir string
	parser LogParser
}

var _ LogFetcher = (*logFetcher)(nil)

type LogParser interface {
	Parse(rawLine string) LogEntry
}

type JSONLogParser struct{}

func (p *JSONLogParser) Parse(rawLine string) LogEntry {
	entry := LogEntry{
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

type TextLogParser struct {
	re *regexp.Regexp
}

func (p *TextLogParser) Parse(rawLine string) LogEntry {
	entry := LogEntry{
		Raw:   rawLine,
		Level: "INFO",
	}

	matches := p.re.FindStringSubmatch(rawLine)
	if len(matches) >= 4 {
		entry.Timestamp = matches[1]
		entry.Level = strings.ToUpper(matches[2])
		entry.Message = matches[3]
	}

	return entry
}

func NewLogFetcher(logDir string, parser LogParser) LogFetcher {
	if parser == nil {
		parser = &JSONLogParser{}
	}
	return &logFetcher{logDir: logDir, parser: parser}
}

func NewJSONParser() LogParser {
	return &JSONLogParser{}
}

func NewTextParser() LogParser {
	re := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\S+)\s+(INFO|WARN|WARNING|ERROR|FATAL|DEBUG|TRACE)\s+(.+)$`)
	return &TextLogParser{re: re}
}

func (f *logFetcher) Fetch(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error) {
	if req.MaxLines <= 0 {
		req.MaxLines = 1000
	}

	logs := make([]LogEntry, 0, req.MaxLines)

	// TODO: Implement filebeat log file traversal and reading
	// For now, return empty result as placeholder

	return &FetchLogsResult{
		Logs:      logs,
		TotalLine: len(logs),
		Source:    fmt.Sprintf("%s/%s/%s/%s", f.logDir, req.Namespace, req.PodName, req.ContainerName),
	}, nil
}
