package contract

type EvidenceKind string

const (
	EvidenceObservation EvidenceKind = "observation"
	EvidenceMutation    EvidenceKind = "mutation_verification"
	EvidenceUserInput   EvidenceKind = "user_input"
	EvidenceExternal    EvidenceKind = "external_state"
)

type GoalContract struct {
	ID                 string              `json:"id"`
	Statement          string              `json:"statement"`
	CompletionCriteria []CriterionContract `json:"completion_criteria"`
	MaxPlanRevisions   int                 `json:"max_plan_revisions"`
}

type CriterionContract struct {
	ID                   string       `json:"id"`
	Description          string       `json:"description"`
	RequiredEvidenceKind EvidenceKind `json:"required_evidence_kind"`
}

type CriterionResult struct {
	CriterionID  string   `json:"criterion_id"`
	Satisfied    bool     `json:"satisfied"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

type PhaseContract struct {
	ID                 string              `json:"id"`
	PhaseLineageID     string              `json:"phase_lineage_id"`
	ReplacesPhaseID    string              `json:"replaces_phase_id,omitempty"`
	Index              int                 `json:"index"`
	Name               string              `json:"name"`
	Goal               string              `json:"goal"`
	CompletionCriteria []CriterionContract `json:"completion_criteria"`
	Steps              []StepContract      `json:"steps"`
	AllowedNext        []string            `json:"allowed_next,omitempty"`
}

type StepContract struct {
	ID                 string              `json:"id"`
	GoalLineageID      string              `json:"goal_lineage_id"`
	Kind               StepKind            `json:"kind"`
	Index              int                 `json:"index"`
	Goal               string              `json:"goal"`
	CompletionCriteria []CriterionContract `json:"completion_criteria"`
	RequiredEvidence   []EvidenceKind      `json:"required_evidence"`
	MaxAttempts        int                 `json:"max_attempts"`
}

// StepLineageMapping preserves the same execution goal when a later plan
// revision replaces a step.
type StepLineageMapping struct {
	PreviousStepID    string `json:"previous_step_id"`
	ReplacementStepID string `json:"replacement_step_id"`
	GoalLineageID     string `json:"goal_lineage_id"`
}

type StepResultStatus string

const (
	StepResultAchieved       StepResultStatus = "achieved"
	StepResultBlocked        StepResultStatus = "blocked"
	StepResultReplanRequired StepResultStatus = "replan_required"
)

type StepResult struct {
	StepID        string            `json:"step_id"`
	Status        StepResultStatus  `json:"status"`
	Criteria      []CriterionResult `json:"criteria"`
	EvidenceRefs  []string          `json:"evidence_refs,omitempty"`
	RemainingGap  string            `json:"remaining_gap,omitempty"`
	SuggestedNext string            `json:"suggested_next,omitempty"`
}

type AttemptStatus string

const (
	AttemptDispatchPending AttemptStatus = "dispatch_pending"
	AttemptVerifying       AttemptStatus = "verifying"
	AttemptSucceeded       AttemptStatus = "succeeded"
	AttemptFailed          AttemptStatus = "failed"
	AttemptUnknown         AttemptStatus = "unknown"
	AttemptCancelled       AttemptStatus = "cancelled"
)

type AttemptRecord struct {
	ID              string        `json:"id"`
	SessionID       string        `json:"session_id"`
	RequestID       string        `json:"request_id"`
	PlanRevision    int           `json:"plan_revision"`
	PhaseID         string        `json:"phase_id"`
	StepID          string        `json:"step_id"`
	GoalLineageID   string        `json:"goal_lineage_id"`
	Strategy        string        `json:"strategy,omitempty"`
	Action          FunctionCall  `json:"action"`
	ObservationRefs []string      `json:"observation_refs"`
	Status          AttemptStatus `json:"status"`
	RetryOf         string        `json:"retry_of,omitempty"`
	RetryReason     string        `json:"retry_reason,omitempty"`
	ChangedSince    []string      `json:"changed_since,omitempty"`
}

type ObservationRecord struct {
	ID            string                `json:"id"`
	AttemptID     string                `json:"attempt_id"`
	Kind          EvidenceKind          `json:"kind"`
	Tool          string                `json:"tool"`
	ResultHash    string                `json:"result_hash"`
	Result        map[string]any        `json:"result,omitempty"`
	Clues         []string              `json:"clues,omitempty"`
	Qualification EvidenceQualification `json:"qualification,omitempty"`
}

type EvidenceQualification string

const (
	EvidenceStateBearing   EvidenceQualification = "state_bearing"
	EvidenceAccessBlocker  EvidenceQualification = "access_blocker"
	EvidenceRetryableError EvidenceQualification = "retryable_failure"
	EvidenceUncertain      EvidenceQualification = "uncertain"
	EvidenceUnusable       EvidenceQualification = "unusable"
)

type CorrectionClass string

const (
	CorrectionProtocolSchema CorrectionClass = "protocol_schema"
	CorrectionDomain         CorrectionClass = "domain"
	CorrectionSafety         CorrectionClass = "safety"
)

type CorrectionKey struct {
	Code         string
	RetryScope   string
	PhaseID      string
	StepID       string
	ObligationID string
}

type CorrectionState struct {
	Count        int
	LastRevision uint64
	LastTurn     int
	LastOutcome  string
}

type BudgetPolicy struct {
	StepAttempts                 map[StepKind]int        `json:"step_attempts"`
	PlanRevisions                int                     `json:"plan_revisions"`
	CorrectionRetries            map[CorrectionClass]int `json:"correction_retries"`
	VerificationEvidenceAttempts int                     `json:"verification_evidence_attempts"`
	MutationContinuationAttempts int                     `json:"mutation_continuation_attempts"`
	MaxIterations                int                     `json:"max_iterations"`
	ClosureReserve               int                     `json:"closure_reserve"`
	Profile                      string                  `json:"profile"`
}

type ContinuationHandoff struct {
	RequestID            string   `json:"request_id"`
	GoalID               string   `json:"goal_id"`
	CurrentJudgement     string   `json:"current_judgement"`
	CompletedSteps       []string `json:"completed_steps,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
	UnresolvedSteps      []string `json:"unresolved_steps,omitempty"`
	MandatoryObligations []string `json:"mandatory_obligations,omitempty"`
	RecommendedNextStep  string   `json:"recommended_next_step"`
	Conclusive           bool     `json:"conclusive"`
}

type ContinuationState struct {
	Handoff           ContinuationHandoff `json:"handoff"`
	TotalIterations   int                 `json:"total_iterations"`
	Segment           int                 `json:"segment"`
	SegmentIterations int                 `json:"segment_iterations"`
}
