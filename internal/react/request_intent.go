package react

import "strings"

type RequestIntent string

const (
	RequestIntentGeneral  RequestIntent = "general"
	RequestIntentManifest RequestIntent = "manifest"
	RequestIntentLogs     RequestIntent = "logs"
)

func classifyRequestIntent(query string) RequestIntent {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return RequestIntentGeneral
	}
	manifestLike := strings.Contains(q, "manifest") ||
		strings.Contains(q, "yaml") ||
		strings.Contains(q, "매니페스트")
	createLike := strings.Contains(q, "create") ||
		strings.Contains(q, "generate") ||
		strings.Contains(q, "write") ||
		strings.Contains(q, "생성") ||
		strings.Contains(q, "만들") ||
		strings.Contains(q, "작성")
	if manifestLike && createLike {
		return RequestIntentManifest
	}
	for _, marker := range []string{
		"manifest 생성", "generate manifest", "create manifest",
		"yaml 생성", "generate yaml", "create yaml",
	} {
		if strings.Contains(q, marker) {
			return RequestIntentManifest
		}
	}
	for _, marker := range []string{
		"log", "logs", "event", "events", "metric", "metrics",
		"로그", "이벤트", "메트릭",
	} {
		if strings.Contains(q, marker) {
			return RequestIntentLogs
		}
	}
	return RequestIntentGeneral
}
