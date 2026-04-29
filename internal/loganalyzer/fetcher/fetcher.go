package fetcher

import (
	"context"
	"fmt"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/loganalyzer/fetcher/parser"
)

type LogFetcher interface {
	Fetch(ctx context.Context, req loganalyzer.FetchLogsRequest) (*loganalyzer.FetchLogsResult, error)
}

func NewFetcher(logDir string, p parser.LogParser) LogFetcher {
	if p == nil {
		p = parser.NewJSONParser()
	}
	return &filebeat{logDir: logDir, parser: p}
}

type filebeat struct {
	logDir string
	parser parser.LogParser
}

var _ LogFetcher = (*filebeat)(nil)

func (f *filebeat) Fetch(ctx context.Context, req loganalyzer.FetchLogsRequest) (*loganalyzer.FetchLogsResult, error) {
	if req.MaxLines <= 0 {
		req.MaxLines = 1000
	}

	logs := make([]loganalyzer.LogEntry, 0, req.MaxLines)

	// TODO: Implement filebeat log file traversal and reading
	// For now, return empty result as placeholder

	return &loganalyzer.FetchLogsResult{
		Logs:      logs,
		TotalLine: len(logs),
		Source:    fmt.Sprintf("%s/%s/%s/%s", f.logDir, req.Namespace, req.PodName, req.ContainerName),
	}, nil
}
