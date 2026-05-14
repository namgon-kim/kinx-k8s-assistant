package troubleshooting

import (
	"context"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type Client struct {
	cfg Config
	svc *Service
}

type ClientResult struct {
	Signal     diagnostic.ProblemSignal
	Runbook    *TroubleshootingSearchResult
	Knowledge  *TroubleshootingSearchResult
	Plan       *RemediationPlan
	Validation *ValidationResult
}

func NewClientFromDefaultConfig() (*Client, error) {
	cfg := Config{}
	if fileCfg, _, err := LoadOptionalFileConfig(""); err != nil {
		return nil, err
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
	}
	cfg = ApplyDefaults(cfg)

	runbooks, err := LoadRunbooks(cfg.RunbookDir)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, svc: NewService(cfg, runbooks)}, nil
}

func (c *Client) Analyze(ctx context.Context, signal diagnostic.ProblemSignal) (*ClientResult, error) {
	req := TroubleshootingSearchRequest{
		Signal: signal,
		Query:  signal.Summary,
		Target: signal.Target,
		TopK:   c.cfg.MaxCases,
		Locale: "ko",
	}

	runbookResult, err := c.svc.MatchRunbook(ctx, req)
	if err != nil {
		return nil, err
	}

	var knowledgeResult *TroubleshootingSearchResult
	if c.cfg.KnowledgeProvider != KnowledgeProviderLocal || c.cfg.SearchMode == SearchModeHybrid {
		if result, err := c.svc.SearchKnowledge(ctx, req); err == nil {
			knowledgeResult = result
		}
	}

	var selected []TroubleshootingCase
	if len(runbookResult.Cases) > 0 {
		selected = append(selected, runbookResult.Cases[0])
	}

	plan, err := c.svc.BuildRemediationPlan(ctx, RemediationPlanRequest{
		Signal:        signal,
		SelectedCases: selected,
		Target:        signal.Target,
		Constraints: RemediationConstraints{
			AllowMutation:       true,
			RequireDryRun:       true,
			RequireConfirmation: true,
		},
	})
	if err != nil {
		return nil, err
	}
	validation, err := c.svc.ValidatePlan(ctx, *plan)
	if err != nil {
		return nil, err
	}

	return &ClientResult{
		Signal:     signal,
		Runbook:    runbookResult,
		Knowledge:  knowledgeResult,
		Plan:       plan,
		Validation: validation,
	}, nil
}
