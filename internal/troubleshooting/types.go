package troubleshooting

import "github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"

type SearchMode string

const (
	SearchModeRunbook  SearchMode = "runbook"
	SearchModeKeyword  SearchMode = "keyword"
	SearchModeHybrid   SearchMode = "hybrid"
	SearchModeEndpoint SearchMode = "endpoint"
)

type KnowledgeProvider string

const (
	KnowledgeProviderLocal    KnowledgeProvider = "local"
	KnowledgeProviderEndpoint KnowledgeProvider = "endpoint"
	KnowledgeProviderQdrant   KnowledgeProvider = "qdrant"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type TroubleshootingSearchRequest struct {
	Signal diagnostic.ProblemSignal    `json:"signal" yaml:"signal"`
	Query  string                      `json:"query,omitempty" yaml:"query,omitempty"`
	Target diagnostic.KubernetesTarget `json:"target,omitempty" yaml:"target,omitempty"`
	TopK   int                         `json:"top_k,omitempty" yaml:"top_k,omitempty"`
	Locale string                      `json:"locale,omitempty" yaml:"locale,omitempty"`
}

type TroubleshootingSearchResult struct {
	Query      string                     `json:"query" yaml:"query"`
	Cases      []TroubleshootingCase      `json:"cases" yaml:"cases"`
	Confidence diagnostic.ConfidenceLevel `json:"confidence" yaml:"confidence"`
	Summary    string                     `json:"summary" yaml:"summary"`
	SearchMode SearchMode                 `json:"search_mode" yaml:"search_mode"`
}

type TroubleshootingCase struct {
	ID               string                     `json:"id" yaml:"id"`
	Title            string                     `json:"title" yaml:"title"`
	MatchTypes       []diagnostic.DetectionType `json:"match_types,omitempty" yaml:"match_types,omitempty"`
	Symptoms         []string                   `json:"symptoms,omitempty" yaml:"symptoms,omitempty"`
	EvidenceKeywords []string                   `json:"evidence_keywords,omitempty" yaml:"evidence_keywords,omitempty"`
	Similarity       float64                    `json:"similarity,omitempty" yaml:"similarity,omitempty"`
	Cause            string                     `json:"cause,omitempty" yaml:"cause,omitempty"`
	LikelyCauses     []string                   `json:"likely_causes,omitempty" yaml:"likely_causes,omitempty"`
	Resolution       string                     `json:"resolution,omitempty" yaml:"resolution,omitempty"`
	DecisionHints    []string                   `json:"decision_hints,omitempty" yaml:"decision_hints,omitempty"`
	RelatedObjects   []string                   `json:"related_objects,omitempty" yaml:"related_objects,omitempty"`
	DiagnosticSteps  []PlanStep                 `json:"diagnostic_steps,omitempty" yaml:"diagnostic_steps,omitempty"`
	RemediateSteps   []PlanStep                 `json:"remediate_steps,omitempty" yaml:"remediate_steps,omitempty"`
	VerifySteps      []PlanStep                 `json:"verify_steps,omitempty" yaml:"verify_steps,omitempty"`
	RollbackSteps    []PlanStep                 `json:"rollback_steps,omitempty" yaml:"rollback_steps,omitempty"`
	RiskLevel        RiskLevel                  `json:"risk_level,omitempty" yaml:"risk_level,omitempty"`
	Source           string                     `json:"source,omitempty" yaml:"source,omitempty"`
	Tags             []string                   `json:"tags,omitempty" yaml:"tags,omitempty"`
}

type RemediationPlanRequest struct {
	Signal        diagnostic.ProblemSignal    `json:"signal" yaml:"signal"`
	SelectedCases []TroubleshootingCase       `json:"selected_cases,omitempty" yaml:"selected_cases,omitempty"`
	Target        diagnostic.KubernetesTarget `json:"target,omitempty" yaml:"target,omitempty"`
	Constraints   RemediationConstraints      `json:"constraints,omitempty" yaml:"constraints,omitempty"`
}

type RemediationPlan struct {
	ID                   string                      `json:"id" yaml:"id"`
	Target               diagnostic.KubernetesTarget `json:"target" yaml:"target"`
	Summary              string                      `json:"summary" yaml:"summary"`
	Assumptions          []string                    `json:"assumptions,omitempty" yaml:"assumptions,omitempty"`
	Steps                []PlanStep                  `json:"steps,omitempty" yaml:"steps,omitempty"`
	Verification         []PlanStep                  `json:"verification,omitempty" yaml:"verification,omitempty"`
	Rollback             []PlanStep                  `json:"rollback,omitempty" yaml:"rollback,omitempty"`
	RiskLevel            RiskLevel                   `json:"risk_level" yaml:"risk_level"`
	RequiresUserApproval bool                        `json:"requires_user_approval" yaml:"requires_user_approval"`
	Warnings             []string                    `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

type PlanStep struct {
	Order                int               `json:"order" yaml:"order"`
	Type                 string            `json:"type" yaml:"type"`
	Description          string            `json:"description" yaml:"description"`
	CommandTemplate      string            `json:"command_template,omitempty" yaml:"command_template,omitempty"`
	RenderedCommand      string            `json:"rendered_command,omitempty" yaml:"rendered_command,omitempty"`
	AutomaticCandidate   bool              `json:"automatic_candidate" yaml:"automatic_candidate"`
	RequiresConfirmation bool              `json:"requires_confirmation" yaml:"requires_confirmation"`
	Preconditions        []string          `json:"preconditions,omitempty" yaml:"preconditions,omitempty"`
	ExpectedOutcome      string            `json:"expected_outcome,omitempty" yaml:"expected_outcome,omitempty"`
	Variables            map[string]string `json:"variables,omitempty" yaml:"variables,omitempty"`
}

type RemediationConstraints struct {
	AllowMutation       bool `json:"allow_mutation" yaml:"allow_mutation"`
	RequireDryRun       bool `json:"require_dry_run" yaml:"require_dry_run"`
	RequireConfirmation bool `json:"require_confirmation" yaml:"require_confirmation"`
}

type ExportedIssue struct {
	ID              string                   `json:"id" yaml:"id"`
	Title           string                   `json:"title" yaml:"title"`
	SourceType      string                   `json:"source_type" yaml:"source_type"`
	Signal          diagnostic.ProblemSignal `json:"signal" yaml:"signal"`
	LogSummary      string                   `json:"log_summary,omitempty" yaml:"log_summary,omitempty"`
	MetricSummary   string                   `json:"metric_summary,omitempty" yaml:"metric_summary,omitempty"`
	SelectedCases   []TroubleshootingCase    `json:"selected_cases,omitempty" yaml:"selected_cases,omitempty"`
	Plan            *RemediationPlan         `json:"plan,omitempty" yaml:"plan,omitempty"`
	ExecutionResult string                   `json:"execution_result,omitempty" yaml:"execution_result,omitempty"`
	Cause           string                   `json:"cause,omitempty" yaml:"cause,omitempty"`
	Resolution      string                   `json:"resolution,omitempty" yaml:"resolution,omitempty"`
	Tags            []string                 `json:"tags,omitempty" yaml:"tags,omitempty"`
	CreatedAt       string                   `json:"created_at" yaml:"created_at"`
	Source          string                   `json:"source,omitempty" yaml:"source,omitempty"`
}

type KnowledgeIndexRequest struct {
	Sources         []string `json:"sources" yaml:"sources"`
	Rebuild         bool     `json:"rebuild" yaml:"rebuild"`
	IncludeIssues   bool     `json:"include_issues" yaml:"include_issues"`
	IncludeRunbooks bool     `json:"include_runbooks" yaml:"include_runbooks"`
}

type ValidationResult struct {
	Valid    bool     `json:"valid" yaml:"valid"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty" yaml:"errors,omitempty"`
}

type Config struct {
	RunbookDir          string
	IssueDir            string
	KnowledgeDir        string
	SearchMode          SearchMode
	KnowledgeProvider   KnowledgeProvider
	EndpointURL         string
	EndpointAPIKey      string
	EndpointTimeout     int
	EmbeddingBaseURL    string
	EmbeddingAPIKey     string
	EmbeddingModel      string
	VectorName          string
	VectorSize          int
	Distance            string
	EmbeddingMaxLength  int
	NormalizeEmbeddings bool
	QdrantURL           string
	QdrantAPIKey        string
	QdrantCollection    string
	QdrantLimit         int
	QdrantWithPayload   bool
	QdrantWithVectors   bool
	QdrantExact         bool
	RerankerEnabled     bool
	RerankerEnabledSet  bool
	RerankerBaseURL     string
	RerankerAPIKey      string
	RerankerModel       string
	RerankerTopN        int
	RerankerMaxLength   int
	RerankerUseFP16     bool
	RerankerNormalize   bool
	MinMatchScore       float64
	MaxCases            int
	MaskSensitive       bool
	IncludeRawLogs      bool
}
