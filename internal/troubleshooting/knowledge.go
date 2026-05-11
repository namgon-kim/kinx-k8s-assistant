package troubleshooting

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type KnowledgeStore struct {
	cases []TroubleshootingCase
}

func NewKnowledgeStore() *KnowledgeStore {
	return &KnowledgeStore{cases: make([]TroubleshootingCase, 0)}
}

func (s *KnowledgeStore) Index(cases []TroubleshootingCase, rebuild bool) {
	if rebuild {
		s.cases = make([]TroubleshootingCase, 0, len(cases))
	}
	s.cases = append(s.cases, cases...)
}

func (s *KnowledgeStore) Search(query string, max int) []TroubleshootingCase {
	if max <= 0 {
		max = 5
	}
	results := make([]TroubleshootingCase, 0)
	for _, c := range s.cases {
		score := keywordScore(c, query)
		if score <= 0 {
			continue
		}
		c.Similarity = score
		results = append(results, c)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})
	if len(results) > max {
		results = results[:max]
	}
	return results
}

func keywordScore(c TroubleshootingCase, query string) float64 {
	text := strings.ToLower(strings.Join([]string{
		c.ID, c.Title, c.Cause, c.Resolution, c.Source, strings.Join(c.Tags, " "),
	}, " "))
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return 0
	}
	score := 0.0
	for _, word := range words {
		if len(word) < 2 {
			continue
		}
		if strings.Contains(text, word) {
			score += 1.0 / float64(len(words))
		}
	}
	if score > 1 {
		return 1
	}
	return score
}

func (s *Service) ExportIssue(ctx context.Context, issue ExportedIssue) (string, error) {
	_ = ctx
	if s.cfg.IssueDir == "" {
		return "", fmt.Errorf("issue export dir is not configured")
	}
	if issue.ID == "" {
		issue.ID = fmt.Sprintf("issue-%s", time.Now().Format("20060102-150405"))
	}
	if issue.CreatedAt == "" {
		issue.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if issue.SourceType == "" {
		issue.SourceType = "exported_issue"
	}
	if issue.Title == "" {
		issue.Title = issue.Signal.Summary
	}

	if err := os.MkdirAll(s.cfg.IssueDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(s.cfg.IssueDir, issue.ID+".yaml")
	data, err := yaml.Marshal(issue)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Service) ImportIssues(ctx context.Context, dir string) (int, error) {
	_ = ctx
	if dir == "" {
		dir = s.cfg.IssueDir
	}
	cases, err := LoadIssuesAsCases(dir)
	if err != nil {
		return 0, err
	}
	s.knowledge.Index(cases, false)
	return len(cases), nil
}

func (s *Service) IndexKnowledge(ctx context.Context, req KnowledgeIndexRequest) (int, error) {
	_ = ctx
	var cases []TroubleshootingCase
	if req.IncludeRunbooks {
		cases = append(cases, s.runbooks...)
	}
	if req.IncludeIssues {
		dir := s.cfg.IssueDir
		if len(req.Sources) > 0 {
			dir = req.Sources[0]
		}
		issueCases, err := LoadIssuesAsCases(dir)
		if err != nil {
			return 0, err
		}
		cases = append(cases, issueCases...)
	}
	s.knowledge.Index(cases, req.Rebuild)
	return len(cases), nil
}

func LoadIssuesAsCases(dir string) ([]TroubleshootingCase, error) {
	if dir == "" {
		return nil, fmt.Errorf("issue dir is not configured")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []TroubleshootingCase{}, nil
		}
		return nil, err
	}

	var cases []TroubleshootingCase
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var issue ExportedIssue
		if err := yaml.Unmarshal(data, &issue); err != nil {
			return nil, err
		}
		cases = append(cases, issueToCase(issue, path))
	}
	return cases, nil
}

func issueToCase(issue ExportedIssue, path string) TroubleshootingCase {
	matchTypes := make([]string, 0, len(issue.Signal.DetectionTypes))
	for _, t := range issue.Signal.DetectionTypes {
		matchTypes = append(matchTypes, string(t))
	}
	title := issue.Title
	if title == "" {
		title = issue.Signal.Summary
	}
	return TroubleshootingCase{
		ID:         issue.ID,
		Title:      title,
		MatchTypes: issue.Signal.DetectionTypes,
		Cause:      firstNonEmpty(issue.Cause, issue.LogSummary, issue.MetricSummary),
		Resolution: firstNonEmpty(issue.Resolution, issue.ExecutionResult),
		Source:     firstNonEmpty(issue.Source, path),
		Tags:       append(issue.Tags, matchTypes...),
		RiskLevel:  RiskLow,
	}
}
