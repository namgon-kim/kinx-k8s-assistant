package request

import "strings"

type Intent string

const (
	General  Intent = "general"
	Manifest Intent = "manifest"
	Logs     Intent = "logs"
)

func Classify(query string) Intent {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return General
	}
	manifestLike := strings.Contains(q, "manifest") || strings.Contains(q, "yaml") || strings.Contains(q, "매니페스트")
	createLike := strings.Contains(q, "create") || strings.Contains(q, "generate") || strings.Contains(q, "write") ||
		strings.Contains(q, "생성") || strings.Contains(q, "만들") || strings.Contains(q, "작성")
	if manifestLike && createLike {
		return Manifest
	}
	for _, marker := range []string{"log", "logs", "event", "events", "metric", "metrics", "로그", "이벤트", "메트릭"} {
		if strings.Contains(q, marker) {
			return Logs
		}
	}
	return General
}
