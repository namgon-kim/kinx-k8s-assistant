package contract

type ToolDispatchStatus string

const (
	ToolDispatchPending    ToolDispatchStatus = "dispatch_pending"
	ToolDispatchReconciled ToolDispatchStatus = "reconciled"
	ToolDispatchUncertain  ToolDispatchStatus = "uncertain"
	ToolDispatchRejected   ToolDispatchStatus = "rejected"
	ToolDispatchCancelled  ToolDispatchStatus = "cancelled"
)

type ToolDispatchIntent struct {
	ID                   string             `json:"id"`
	AttemptID            string             `json:"attempt_id"`
	AttemptReserved      bool               `json:"attempt_reserved"`
	Call                 FunctionCall       `json:"call"`
	StepRef              *StepRef           `json:"step_ref,omitempty"`
	Target               *ActionTarget      `json:"target,omitempty"`
	ModifiesResource     string             `json:"modifies_resource"`
	Verification         *VerificationSpec  `json:"verification,omitempty"`
	Risk                 *CommandRisk       `json:"risk,omitempty"`
	ApprovalRequired     bool               `json:"approval_required"`
	ApprovalGranted      bool               `json:"approval_granted"`
	CanonicalPayloadHash string             `json:"canonical_payload_hash"`
	Status               ToolDispatchStatus `json:"status"`
}

type InvokeToolEffect struct {
	DispatchIDs []string
}
