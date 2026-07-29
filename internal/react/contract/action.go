package contract

type Action struct {
	Name                string            `json:"name"`
	Reason              string            `json:"reason"`
	Goal                string            `json:"goal,omitempty"`
	Target              *ActionTarget     `json:"target,omitempty"`
	Command             string            `json:"command"`
	ExpectedObservation string            `json:"expected_observation,omitempty"`
	ModifiesResource    string            `json:"modifies_resource"`
	Verification        *VerificationSpec `json:"verification,omitempty"`
	GuideProgress       *GuideProgress    `json:"guide_progress,omitempty"`
	StepRef             *StepRef          `json:"step_ref,omitempty"`
	RetryOf             string            `json:"retry_of,omitempty"`
	RetryReason         string            `json:"retry_reason,omitempty"`
	ChangedSince        []string          `json:"changed_since,omitempty"`
	Risk                *CommandRisk      `json:"risk,omitempty"`
}

type CommandRisk struct {
	Risky  bool   `json:"risky"`
	Reason string `json:"reason,omitempty"`
}

type ActionTarget struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

type GuideProgress struct {
	StepCompleted  int  `json:"step_completed,omitempty"`
	EvidenceUseful bool `json:"evidence_useful,omitempty"`
}
