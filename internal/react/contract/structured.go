package contract

type RequirementAnalysis struct {
	RequestType      string                       `json:"request_type"`
	Action           string                       `json:"Action"`
	Target           RequirementAnalysisTarget    `json:"target"`
	Scope            RequirementScope             `json:"scope,omitempty"`
	Resources        []RequirementResource        `json:"resource_candidates,omitempty"`
	OperationalFocus *RequirementOperationalFocus `json:"operational_focus,omitempty"`
	Evidence         []string                     `json:"evidence_needs,omitempty"`
	Constraints      []string                     `json:"constraints,omitempty"`
	Ambiguities      []string                     `json:"ambiguities,omitempty"`
}

type RequirementAnalysisTarget struct {
	Category    string `json:"category"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type RequirementScope struct {
	Type      string `json:"type,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type RequirementResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"`
}

type RequirementOperationalFocus struct {
	Summary               string                       `json:"summary,omitempty"`
	RelationshipToPrimary string                       `json:"relationship_to_primary,omitempty"`
	ChangedFromPrevious   bool                         `json:"changed_from_previous,omitempty"`
	Reason                string                       `json:"reason,omitempty"`
	RelatedResourceHints  []RequirementRelatedResource `json:"related_resource_hints,omitempty"`
	EvidenceNeeds         []string                     `json:"evidence_needs,omitempty"`
}

type RequirementRelatedResource struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type RequestContext struct {
	PrimaryTarget RequestPrimaryTarget `json:"primary_target"`
	Scope         RequestScope         `json:"scope,omitempty"`
	ResourceClass string               `json:"resource_class"`
}

type RequestPrimaryTarget struct {
	Resource string `json:"resource"`
	Name     string `json:"name,omitempty"`
}

type RequestScope struct {
	Namespace string `json:"namespace,omitempty"`
}

type PhasePlan struct {
	RequestGoal       string      `json:"request_goal"`
	CurrentPhaseIndex int         `json:"current_phase_index,omitempty"`
	PhaseSteps        []PhaseStep `json:"phase_steps,omitempty"`
}

type PhaseStep struct {
	Index               int                  `json:"index"`
	Name                string               `json:"name"`
	Goal                string               `json:"goal"`
	CompletionCondition string               `json:"completion_condition"`
	AllowedNext         []string             `json:"allowed_next,omitempty"`
	Steps               []PhaseExecutionStep `json:"steps,omitempty"`
}

type PhaseExecutionStep struct {
	ID              string `json:"id,omitempty"`
	Index           int    `json:"index,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Description     string `json:"description,omitempty"`
	Command         string `json:"command,omitempty"`
	ExpectedOutcome string `json:"expected_outcome,omitempty"`
}

type PhaseProgress struct {
	PhaseCompleted   int    `json:"phase_completed"`
	EvidenceUseful   bool   `json:"evidence_useful,omitempty"`
	CompletionReason string `json:"completion_reason,omitempty"`
	NextPhase        string `json:"next_phase,omitempty"`
}

type ResourceGuideLookup struct {
	ResourceFamily string `json:"resource_family"`
	ProblemFocus   string `json:"problem_focus"`
	Reason         string `json:"reason"`
	Evidence       string `json:"evidence"`
}

type FinalReport struct {
	Conclusive             bool                  `json:"conclusive"`
	Conclusion             string                `json:"conclusion,omitempty"`
	Attempted              []string              `json:"attempted,omitempty"`
	EvidenceKnown          []string              `json:"evidence_known,omitempty"`
	EvidenceMissing        []string              `json:"evidence_missing,omitempty"`
	MostLikelyCause        string                `json:"most_likely_cause,omitempty"`
	RecommendedUserActions []string              `json:"recommended_user_actions,omitempty"`
	ProblematicResources   []ProblematicResource `json:"problematic_resources,omitempty"`
	Blockers               []string              `json:"blockers,omitempty"`
}

type ProblematicResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type NextDirections struct {
	Note    string                `json:"note,omitempty"`
	Options []NextDirectionOption `json:"options"`
}

type NextDirectionOption struct {
	Kind           string `json:"kind"`
	Summary        string `json:"summary"`
	Why            string `json:"why,omitempty"`
	ResourceFamily string `json:"resource_family,omitempty"`
	ProblemFocus   string `json:"problem_focus,omitempty"`
	Instruction    string `json:"instruction,omitempty"`
	ResourceKind   string `json:"resource_kind,omitempty"`
	ResourceName   string `json:"resource_name,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
}

type MutationVerificationResult struct {
	Status          string   `json:"status"`
	EvidenceSummary []string `json:"evidence_summary,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	NextAction      string   `json:"next_action,omitempty"`
}
