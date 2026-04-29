package parser

import "github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"

type LogParser interface {
	Parse(rawLine string) loganalyzer.LogEntry
}
