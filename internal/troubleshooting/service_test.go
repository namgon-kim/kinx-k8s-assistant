package troubleshooting

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

func TestMatchRunbookByDetectionType(t *testing.T) {
	svc := NewService(Config{MaxCases: 3}, []TroubleshootingCase{
		{
			ID:         "crashloop-oom",
			Title:      "CrashLoopBackOff - OOMKilled",
			MatchTypes: []diagnostic.DetectionType{diagnostic.DetectionCrashLoopBackOff, diagnostic.DetectionOOMKilled},
			Cause:      "memory limit exceeded",
		},
	})

	result, err := svc.MatchRunbook(context.Background(), TroubleshootingSearchRequest{
		Signal: diagnostic.ProblemSignal{
			DetectionTypes: []diagnostic.DetectionType{diagnostic.DetectionOOMKilled},
			Summary:        "Pod was OOMKilled",
		},
	})
	if err != nil {
		t.Fatalf("MatchRunbook returned error: %v", err)
	}
	if len(result.Cases) != 1 {
		t.Fatalf("expected 1 case, got %d", len(result.Cases))
	}
	if result.Cases[0].ID != "crashloop-oom" {
		t.Fatalf("unexpected case: %s", result.Cases[0].ID)
	}
}

func TestBuildRemediationPlanMarksMutationConfirmation(t *testing.T) {
	svc := NewService(Config{}, nil)
	target := diagnostic.KubernetesTarget{
		Namespace: "default",
		Kind:      "pod",
		Name:      "app",
		PodName:   "app",
		OwnerKind: "deployment",
		OwnerName: "app",
	}

	plan, err := svc.BuildRemediationPlan(context.Background(), RemediationPlanRequest{
		Target: target,
		SelectedCases: []TroubleshootingCase{
			{
				ID:        "oom",
				Title:     "OOM",
				RiskLevel: RiskMedium,
				RemediateSteps: []PlanStep{
					{
						Type:            "remediate",
						Description:     "increase memory",
						CommandTemplate: "kubectl set resources {{owner_kind}}/{{owner_name}} -n {{namespace}} --limits=memory=2Gi",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildRemediationPlan returned error: %v", err)
	}
	if !plan.RequiresUserApproval {
		t.Fatal("expected plan to require user approval")
	}
	if len(plan.Steps) != 1 || !plan.Steps[0].RequiresConfirmation {
		t.Fatalf("expected mutation step to require confirmation: %+v", plan.Steps)
	}
	if plan.Steps[0].RenderedCommand != "kubectl set resources deployment/app -n default --limits=memory=2Gi" {
		t.Fatalf("unexpected rendered command: %s", plan.Steps[0].RenderedCommand)
	}
}

func TestExportAndSearchKnowledge(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Config{IssueDir: dir, MaxCases: 3}, nil)

	_, err := svc.ExportIssue(context.Background(), ExportedIssue{
		ID:    "issue-oom",
		Title: "prod api OOMKilled",
		Signal: diagnostic.ProblemSignal{
			DetectionTypes: []diagnostic.DetectionType{diagnostic.DetectionOOMKilled},
			Summary:        "api pod OOMKilled",
		},
		Cause:      "heap increased after burst traffic",
		Resolution: "increased memory limit",
	})
	if err != nil {
		t.Fatalf("ExportIssue returned error: %v", err)
	}

	count, err := svc.IndexKnowledge(context.Background(), KnowledgeIndexRequest{
		Rebuild:       true,
		IncludeIssues: true,
	})
	if err != nil {
		t.Fatalf("IndexKnowledge returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 indexed issue, got %d", count)
	}

	result, err := svc.SearchKnowledge(context.Background(), TroubleshootingSearchRequest{
		Query: "OOMKilled memory limit",
	})
	if err != nil {
		t.Fatalf("SearchKnowledge returned error: %v", err)
	}
	if len(result.Cases) != 1 || result.Cases[0].ID != "issue-oom" {
		t.Fatalf("unexpected search result: %+v", result.Cases)
	}
}

func TestSearchKnowledgeUsesEndpointProvider(t *testing.T) {
	var authHeader string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authHeader = r.Header.Get("Authorization")
		var req TroubleshootingSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Query != "oom memory" {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		var body bytes.Buffer
		_ = json.NewEncoder(&body).Encode(TroubleshootingSearchResult{
			Query: "oom memory",
			Cases: []TroubleshootingCase{
				{ID: "remote-issue", Title: "Remote OOM issue", Similarity: 0.9},
			},
			Confidence: diagnostic.ConfidenceHigh,
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(&body),
			Header:     make(http.Header),
		}, nil
	})

	svc := NewService(Config{
		KnowledgeProvider: KnowledgeProviderEndpoint,
		EndpointURL:       "http://rag.example/search",
		EndpointAPIKey:    "token",
		MaxCases:          3,
	}, nil)
	svc.endpoint.client = &http.Client{Transport: transport}

	result, err := svc.SearchKnowledge(context.Background(), TroubleshootingSearchRequest{
		Query: "oom memory",
	})
	if err != nil {
		t.Fatalf("SearchKnowledge returned error: %v", err)
	}
	if authHeader != "Bearer token" {
		t.Fatalf("unexpected auth header: %s", authHeader)
	}
	if len(result.Cases) != 1 || result.Cases[0].ID != "remote-issue" {
		t.Fatalf("unexpected endpoint result: %+v", result.Cases)
	}
	if result.SearchMode != SearchModeEndpoint {
		t.Fatalf("expected endpoint search mode, got %s", result.SearchMode)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
