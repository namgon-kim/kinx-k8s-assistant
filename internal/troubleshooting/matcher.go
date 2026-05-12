package troubleshooting

import (
	"sort"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

func matchRunbooks(cases []TroubleshootingCase, req TroubleshootingSearchRequest, max int) []TroubleshootingCase {
	query := buildQuery(req)
	results := make([]TroubleshootingCase, 0, len(cases))

	for _, c := range cases {
		score := scoreCase(c, req, query)
		if score <= 0 {
			continue
		}
		c.Similarity = score
		results = append(results, c)
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if max <= 0 {
		max = 5
	}
	if len(results) > max {
		results = results[:max]
	}
	return results
}

func scoreCase(c TroubleshootingCase, req TroubleshootingSearchRequest, query string) float64 {
	score := 0.0
	for _, want := range req.Signal.DetectionTypes {
		for _, got := range c.MatchTypes {
			if strings.EqualFold(string(want), string(got)) {
				score += 0.45
			}
		}
	}

	target := req.Target
	if target.Namespace == "" {
		target = req.Signal.Target
	}
	targetText := strings.ToLower(strings.Join([]string{
		target.Namespace, target.Kind, target.Name, target.PodName,
		target.Container, target.OwnerKind, target.OwnerName,
	}, " "))
	caseText := strings.ToLower(strings.Join([]string{
		c.ID, c.Title, c.Cause, c.Resolution, strings.Join(c.Tags, " "),
		strings.Join(c.Symptoms, " "),
		strings.Join(c.EvidenceKeywords, " "),
		strings.Join(c.LikelyCauses, " "),
		strings.Join(c.DecisionHints, " "),
		strings.Join(c.RelatedObjects, " "),
	}, " "))

	for _, word := range strings.Fields(strings.ToLower(query)) {
		if len(word) < 2 {
			continue
		}
		if strings.Contains(caseText, word) {
			score += 0.03
		}
	}
	for _, tag := range c.Tags {
		if strings.Contains(targetText, strings.ToLower(tag)) {
			score += 0.05
		}
	}
	if c.RiskLevel != "" {
		score += 0.02
	}
	if score > 1 {
		return 1
	}
	return score
}

func buildQuery(req TroubleshootingSearchRequest) string {
	parts := []string{req.Query, req.Signal.Summary}
	for _, t := range req.Signal.DetectionTypes {
		parts = append(parts, string(t))
	}
	for _, e := range req.Signal.Evidence {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, " ")
}

func confidenceForScore(score float64) diagnostic.ConfidenceLevel {
	switch {
	case score >= 0.85:
		return diagnostic.ConfidenceCertain
	case score >= 0.65:
		return diagnostic.ConfidenceHigh
	case score >= 0.4:
		return diagnostic.ConfidenceMedium
	case score > 0:
		return diagnostic.ConfidenceLow
	default:
		return diagnostic.ConfidenceSpeculate
	}
}
