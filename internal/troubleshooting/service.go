package troubleshooting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

type Service struct {
	cfg       Config
	runbooks  []TroubleshootingCase
	knowledge *KnowledgeStore
	endpoint  *EndpointClient
}

func NewService(cfg Config, runbooks []TroubleshootingCase) *Service {
	if cfg.MaxCases <= 0 {
		cfg.MaxCases = 5
	}
	if cfg.SearchMode == "" {
		cfg.SearchMode = SearchModeHybrid
	}
	if cfg.KnowledgeProvider == "" {
		cfg.KnowledgeProvider = KnowledgeProviderLocal
	}
	var endpoint *EndpointClient
	if cfg.KnowledgeProvider == KnowledgeProviderEndpoint {
		endpoint = NewEndpointClient(cfg.EndpointURL, cfg.EndpointAPIKey, cfg.EndpointTimeout)
	}
	return &Service{
		cfg:       cfg,
		runbooks:  runbooks,
		knowledge: NewKnowledgeStore(),
		endpoint:  endpoint,
	}
}

func (s *Service) MatchRunbook(ctx context.Context, req TroubleshootingSearchRequest) (*TroubleshootingSearchResult, error) {
	_ = ctx
	max := req.TopK
	if max <= 0 {
		max = s.cfg.MaxCases
	}
	cases := matchRunbooks(s.runbooks, req, max)
	conf := diagnostic.ConfidenceSpeculate
	if len(cases) > 0 {
		conf = confidenceForScore(cases[0].Similarity)
	}
	return &TroubleshootingSearchResult{
		Query:      buildQuery(req),
		Cases:      cases,
		Confidence: conf,
		Summary:    summarizeCases(cases),
		SearchMode: SearchModeRunbook,
	}, nil
}

func (s *Service) SearchKnowledge(ctx context.Context, req TroubleshootingSearchRequest) (*TroubleshootingSearchResult, error) {
	if s.cfg.KnowledgeProvider == KnowledgeProviderEndpoint {
		return s.endpoint.Search(ctx, req)
	}

	max := req.TopK
	if max <= 0 {
		max = s.cfg.MaxCases
	}
	cases := s.knowledge.Search(buildQuery(req), max)
	conf := diagnostic.ConfidenceSpeculate
	if len(cases) > 0 {
		conf = confidenceForScore(cases[0].Similarity)
	}
	return &TroubleshootingSearchResult{
		Query:      buildQuery(req),
		Cases:      cases,
		Confidence: conf,
		Summary:    summarizeCases(cases),
		SearchMode: SearchModeKeyword,
	}, nil
}

func (s *Service) BuildRemediationPlan(ctx context.Context, req RemediationPlanRequest) (*RemediationPlan, error) {
	_ = ctx
	target := req.Target
	if target.Namespace == "" {
		target = req.Signal.Target
	}

	plan := &RemediationPlan{
		ID:                   fmt.Sprintf("plan-%d", time.Now().UnixNano()),
		Target:               target,
		RiskLevel:            RiskLow,
		RequiresUserApproval: req.Constraints.RequireConfirmation,
	}

	for _, c := range req.SelectedCases {
		plan.Assumptions = append(plan.Assumptions, fmt.Sprintf("%s: %s", c.Title, c.Cause))
		plan.Steps = append(plan.Steps, renderSteps(c.DiagnosticSteps, target)...)
		plan.Steps = append(plan.Steps, renderSteps(c.RemediateSteps, target)...)
		plan.Verification = append(plan.Verification, renderSteps(c.VerifySteps, target)...)
		plan.Rollback = append(plan.Rollback, renderSteps(c.RollbackSteps, target)...)
		plan.RiskLevel = maxRisk(plan.RiskLevel, c.RiskLevel)
	}
	if len(req.SelectedCases) > 0 {
		plan.Summary = fmt.Sprintf("%s 문제에 대한 조치 계획입니다.", req.SelectedCases[0].Title)
	} else {
		plan.Summary = "선택된 트러블슈팅 사례가 없어 추가 진단 중심의 조치 계획입니다."
		plan.Steps = append(plan.Steps, PlanStep{
			Order:                1,
			Type:                 "diagnostic",
			Description:          "대상 리소스 상태 확인",
			CommandTemplate:      "kubectl describe {{kind}} {{name}} -n {{namespace}}",
			RenderedCommand:      renderCommand("kubectl describe {{kind}} {{name}} -n {{namespace}}", target, nil),
			AutomaticCandidate:   true,
			RequiresConfirmation: false,
		})
	}

	for i := range plan.Steps {
		plan.Steps[i].Order = i + 1
		if plan.Steps[i].RequiresConfirmation || isMutationCommand(plan.Steps[i].RenderedCommand) {
			plan.RequiresUserApproval = true
			plan.Steps[i].RequiresConfirmation = true
		}
	}
	if req.Constraints.AllowMutation == false {
		for i := range plan.Steps {
			if isMutationCommand(plan.Steps[i].RenderedCommand) {
				plan.Warnings = append(plan.Warnings, "mutation step is included but allow_mutation=false")
			}
		}
	}

	return plan, nil
}

func (s *Service) ValidatePlan(ctx context.Context, plan RemediationPlan) (*ValidationResult, error) {
	_ = ctx
	result := &ValidationResult{Valid: true}
	for _, step := range plan.Steps {
		if strings.Contains(step.RenderedCommand, "{{") {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("unresolved template in step %d: %s", step.Order, step.RenderedCommand))
		}
		if isMutationCommand(step.RenderedCommand) && !step.RequiresConfirmation {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("mutation step %d requires confirmation", step.Order))
		}
	}
	if (plan.RiskLevel == RiskHigh || plan.RiskLevel == RiskCritical) && len(plan.Rollback) == 0 {
		result.Warnings = append(result.Warnings, "high risk plan has no rollback steps")
	}
	return result, nil
}

func summarizeCases(cases []TroubleshootingCase) string {
	if len(cases) == 0 {
		return "매칭된 트러블슈팅 사례가 없습니다."
	}
	titles := make([]string, 0, len(cases))
	for _, c := range cases {
		titles = append(titles, c.Title)
	}
	return "매칭된 트러블슈팅 사례: " + strings.Join(titles, ", ")
}

func maxRisk(a, b RiskLevel) RiskLevel {
	order := map[RiskLevel]int{RiskLow: 1, RiskMedium: 2, RiskHigh: 3, RiskCritical: 4}
	if order[b] > order[a] {
		return b
	}
	return a
}

func isMutationCommand(cmd string) bool {
	cmd = strings.ToLower(cmd)
	mutations := []string{" delete ", " apply ", " patch ", " scale ", " rollout restart ", " set resources ", " replace "}
	for _, mutation := range mutations {
		if strings.Contains(" "+cmd+" ", mutation) {
			return true
		}
	}
	return false
}
