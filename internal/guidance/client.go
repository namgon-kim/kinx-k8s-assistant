package guidance

import (
	"context"
	"fmt"

	appconfig "github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type Client struct {
	cfg Config
	svc *Service
}

type ClientResult struct {
	Signal     diagnostic.ProblemSignal
	Runbook    *GuideSearchResult
	Knowledge  *GuideSearchResult
	Plan       *RemediationPlan
	Validation *ValidationResult
}

func NewIncidentClient(appCfg *appconfig.Config) (*Client, error) {
	cfg := Config{}
	if fileCfg, path, err := LoadOptionalFileConfig(""); err != nil {
		return nil, fmt.Errorf("load guidance config %s: %w", path, err)
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
	}
	if appCfg != nil {
		cfg.QdrantCollection = appCfg.Guidance.IncidentGuides
	}
	cfg = ApplyDefaults(cfg)

	runbooks, err := LoadRunbooks(cfg.RunbookDir)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, svc: NewService(cfg, runbooks)}, nil
}

func NewResourceGuideClient(appCfg *appconfig.Config) (*Client, error) {
	cfg := Config{}
	if fileCfg, path, err := LoadOptionalFileConfig(""); err != nil {
		return nil, fmt.Errorf("load guidance config %s: %w", path, err)
	} else if fileCfg != nil {
		cfg = fileCfg.ApplyToConfig(cfg)
	}
	if appCfg != nil {
		cfg.QdrantCollection = appCfg.Guidance.ResourceGuides
	}
	cfg = ApplyDefaults(cfg)
	return &Client{cfg: cfg, svc: NewService(cfg, nil)}, nil
}

func (c *Client) SearchGuides(ctx context.Context, query string) (*GuideSearchResult, error) {
	req := GuideSearchRequest{Query: query, TopK: c.cfg.MaxCases, Locale: "ko"}
	return c.svc.SearchKnowledge(ctx, req)
}

func (c *Client) KnowledgeProvider() KnowledgeProvider {
	return c.cfg.KnowledgeProvider
}

func (c *Client) Analyze(ctx context.Context, signal diagnostic.ProblemSignal) (*ClientResult, error) {
	req := GuideSearchRequest{
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

	var knowledgeResult *GuideSearchResult
	if c.cfg.KnowledgeProvider != KnowledgeProviderLocal || c.cfg.SearchMode == SearchModeHybrid {
		if result, err := c.svc.SearchKnowledge(ctx, req); err == nil {
			knowledgeResult = result
		}
	}

	var selected []GuideCase
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
