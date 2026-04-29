package loganalyzer

import (
	"context"
	"strings"
)

type VectorStore interface {
	Index(ctx context.Context, cases []SimilarCase) error
	Search(ctx context.Context, query string, maxResults int) ([]SimilarCase, error)
}

type SimpleKeywordStore struct {
	cases []SimilarCase
}

func NewSimpleKeywordStore() VectorStore {
	return &SimpleKeywordStore{
		cases: make([]SimilarCase, 0),
	}
}

func (s *SimpleKeywordStore) Index(ctx context.Context, cases []SimilarCase) error {
	s.cases = make([]SimilarCase, len(cases))
	copy(s.cases, cases)
	return nil
}

func (s *SimpleKeywordStore) Search(ctx context.Context, query string, maxResults int) ([]SimilarCase, error) {
	if maxResults <= 0 {
		maxResults = 5
	}

	queryLower := strings.ToLower(query)
	results := make([]SimilarCase, 0)

	for _, c := range s.cases {
		score := 0.0

		if strings.Contains(strings.ToLower(c.Title), queryLower) {
			score += 0.5
		}
		if strings.Contains(strings.ToLower(c.Cause), queryLower) {
			score += 0.3
		}
		if strings.Contains(strings.ToLower(c.Resolution), queryLower) {
			score += 0.2
		}

		caseWords := strings.Fields(strings.ToLower(c.Title + " " + c.Cause))
		for _, word := range strings.Fields(queryLower) {
			for _, cw := range caseWords {
				if strings.Contains(cw, word) || strings.Contains(word, cw) {
					score += 0.1
				}
			}
		}

		if score > 0 {
			c.Similarity = score
			results = append(results, c)
		}
	}

	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Similarity < results[j].Similarity {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results, nil
}
