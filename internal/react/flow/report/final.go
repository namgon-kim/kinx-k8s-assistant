package report

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func Normalize(value contract.FinalReport) contract.FinalReport {
	value.Conclusion = strings.TrimSpace(value.Conclusion)
	value.MostLikelyCause = strings.TrimSpace(value.MostLikelyCause)
	value.Attempted = nonEmpty(value.Attempted)
	value.EvidenceKnown = nonEmpty(value.EvidenceKnown)
	value.EvidenceMissing = nonEmpty(value.EvidenceMissing)
	value.RecommendedUserActions = nonEmpty(value.RecommendedUserActions)
	value.Blockers = nonEmpty(value.Blockers)
	return value
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
