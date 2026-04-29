package loganalyzer

import (
	"context"
	"fmt"
	"strings"
)

type AnalyzerImpl struct {
	fetcher  LogFetcher
	detector *PatternDetector
	store    VectorStore
}

func NewAnalyzer(
	f LogFetcher,
	d *PatternDetector,
	s VectorStore,
) Analyzer {
	return &AnalyzerImpl{
		fetcher:  f,
		detector: d,
		store:    s,
	}
}

var _ Analyzer = (*AnalyzerImpl)(nil)

func (a *AnalyzerImpl) FetchLogs(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error) {
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	if req.MaxLines <= 0 {
		req.MaxLines = 1000
	}
	return a.fetcher.Fetch(ctx, req)
}

func (a *AnalyzerImpl) AnalyzePattern(ctx context.Context, req AnalyzePatternRequest) (*AnalyzePatternResult, error) {
	if len(req.Logs) == 0 {
		return &AnalyzePatternResult{
			Patterns: []DetectedPattern{},
			Severity: "info",
			Summary:  "분석할 로그가 없습니다",
		}, nil
	}
	return a.detector.Detect(req.Logs, req.PodName, req.Namespace), nil
}

func (a *AnalyzerImpl) RAGLookup(ctx context.Context, req RAGLookupRequest) (*RAGLookupResult, error) {
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}

	queryText := req.Symptom
	if len(req.Patterns) > 0 {
		queryText = fmt.Sprintf("%s. 감지된 패턴: %s", req.Symptom, strings.Join(req.Patterns, ", "))
	}

	cases, err := a.store.Search(ctx, queryText, req.MaxResults)
	if err != nil {
		return nil, fmt.Errorf("RAG lookup failed: %w", err)
	}

	return &RAGLookupResult{Cases: cases}, nil
}

func (a *AnalyzerImpl) AnalyzeAndRemediate(ctx context.Context, req RemediateRequest) (*RemediateResult, error) {
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	fetchReq := FetchLogsRequest{
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		ContainerName: req.ContainerName,
		SinceSeconds:  req.SinceSeconds,
		MaxLines:      1000,
	}
	logs, err := a.FetchLogs(ctx, fetchReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch logs: %w", err)
	}

	patternReq := AnalyzePatternRequest{
		Logs:      logs.Logs,
		PodName:   req.PodName,
		Namespace: req.Namespace,
	}
	patternResult, err := a.AnalyzePattern(ctx, patternReq)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze pattern: %w", err)
	}

	var symptomDesc strings.Builder
	if len(patternResult.Patterns) > 0 {
		symptomDesc.WriteString("감지된 이상 패턴: ")
		for i, p := range patternResult.Patterns {
			if i > 0 {
				symptomDesc.WriteString(", ")
			}
			symptomDesc.WriteString(p.Description)
		}
	} else {
		symptomDesc.WriteString("비정상적인 로그 패턴 없음")
	}

	patternStrs := make([]string, len(patternResult.Patterns))
	for i, p := range patternResult.Patterns {
		patternStrs[i] = string(p.Type)
	}

	ragReq := RAGLookupRequest{
		Symptom:    symptomDesc.String(),
		Patterns:   patternStrs,
		MaxResults: 5,
	}
	ragResult, err := a.RAGLookup(ctx, ragReq)
	if err != nil {
		return nil, fmt.Errorf("failed to perform RAG lookup: %w", err)
	}

	remediationSteps := generateRemediationSteps(patternResult.Patterns, ragResult.Cases, req.PodName, req.Namespace)

	confidence := ConfidenceLow
	if len(patternResult.Patterns) > 0 && len(ragResult.Cases) > 0 {
		if ragResult.Cases[0].Similarity > 0.8 {
			confidence = ConfidenceCertain
		} else if ragResult.Cases[0].Similarity > 0.6 {
			confidence = ConfidenceHigh
		} else {
			confidence = ConfidenceMedium
		}
	}

	return &RemediateResult{
		Summary:      patternResult.Summary,
		Patterns:     patternResult.Patterns,
		SimilarCases: ragResult.Cases,
		Remediation:  remediationSteps,
		Confidence:   confidence,
	}, nil
}

func generateRemediationSteps(patterns []DetectedPattern, cases []SimilarCase, podName, namespace string) []RemediationStep {
	steps := make([]RemediationStep, 0)
	order := 1

	if len(patterns) > 0 {
		for _, p := range patterns {
			switch p.Type {
			case PatternOOMKilled:
				steps = append(steps, RemediationStep{
					Order:       order,
					Description: "메모리 limit 증가",
					Command:     fmt.Sprintf("kubectl set resources pod %s -n %s --limits=memory=2Gi", podName, namespace),
					IsAutomatic: false,
				})
				order++
			case PatternDiskFull:
				steps = append(steps, RemediationStep{
					Order:       order,
					Description: "디스크 정리: 로그 또는 임시 파일 삭제",
					Command:     fmt.Sprintf("kubectl exec %s -n %s -- rm -rf /tmp/*", podName, namespace),
					IsAutomatic: false,
				})
				order++
			case PatternCrashLoop:
				steps = append(steps, RemediationStep{
					Order:       order,
					Description: "Pod 상세 정보 및 이벤트 확인",
					Command:     fmt.Sprintf("kubectl describe pod %s -n %s", podName, namespace),
					IsAutomatic: false,
				})
				order++
			}
		}
	}

	if len(cases) > 0 && cases[0].Similarity > 0.5 {
		steps = append(steps, RemediationStep{
			Order:       order,
			Description: fmt.Sprintf("유사 사례 검토: %s", cases[0].Title),
			Command:     cases[0].Source,
			IsAutomatic: false,
		})
		order++

		steps = append(steps, RemediationStep{
			Order:       order,
			Description: cases[0].Resolution,
			Command:     "",
			IsAutomatic: false,
		})
		order++
	}

	if len(steps) == 0 {
		steps = append(steps, RemediationStep{
			Order:       1,
			Description: "상세 로그 검토",
			Command:     fmt.Sprintf("kubectl logs %s -n %s --tail=200", podName, namespace),
			IsAutomatic: false,
		})
	}

	return steps
}
