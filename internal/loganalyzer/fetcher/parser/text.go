package parser

import (
	"regexp"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
)

type TextParser struct {
	re *regexp.Regexp
}

func NewTextParser() LogParser {
	re := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\S+)\s+(INFO|WARN|WARNING|ERROR|FATAL|DEBUG|TRACE)\s+(.+)$`)
	return &TextParser{re: re}
}

func (p *TextParser) Parse(rawLine string) loganalyzer.LogEntry {
	entry := loganalyzer.LogEntry{
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
