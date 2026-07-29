package contract

// VerificationShape distinguishes one verification condition from an ordered
// set of distinct conditions. Rechecking one condition over time remains a
// single verification and does not become a chain.
type VerificationShape string

const (
	VerificationSingle VerificationShape = "single"
	VerificationChain  VerificationShape = "chain"
)

// VerificationMode controls whether one verification is evaluated once or may
// be rechecked while an external Kubernetes state converges.
type VerificationMode string

const (
	VerificationImmediate  VerificationMode = "immediate"
	VerificationAwaitState VerificationMode = "await_state"
)

type VerificationChainPolicy string

const (
	VerificationOrdered VerificationChainPolicy = "ordered"
)

type VerificationCheckStatus string

const (
	VerificationCheckPending   VerificationCheckStatus = "pending"
	VerificationCheckActive    VerificationCheckStatus = "active"
	VerificationCheckSatisfied VerificationCheckStatus = "satisfied"
	VerificationCheckFailed    VerificationCheckStatus = "failed"
	VerificationCheckSkipped   VerificationCheckStatus = "skipped"
)

type VerificationResultStatus string

const (
	VerificationSatisfied VerificationResultStatus = "satisfied"
	VerificationWaiting   VerificationResultStatus = "waiting"
	VerificationFailed    VerificationResultStatus = "failed"
)

// VerificationSpec is model-proposed metadata attached to an action. IDs and
// retry limits are assigned by runtime; model-provided values are descriptive.
type VerificationSpec struct {
	Shape                  VerificationShape       `json:"shape"`
	Mode                   VerificationMode        `json:"mode,omitempty"`
	ExpectedState          string                  `json:"expected_state,omitempty"`
	InitialDelaySeconds    int                     `json:"initial_delay_seconds,omitempty"`
	RecheckIntervalSeconds int                     `json:"recheck_interval_seconds,omitempty"`
	Policy                 VerificationChainPolicy `json:"policy,omitempty"`
	Checks                 []VerificationCheckSpec `json:"checks,omitempty"`
}

type VerificationCheckSpec struct {
	Mode                   VerificationMode `json:"mode"`
	Target                 *ActionTarget    `json:"target,omitempty"`
	ExpectedState          string           `json:"expected_state"`
	SuggestedCommand       string           `json:"suggested_command,omitempty"`
	InitialDelaySeconds    int              `json:"initial_delay_seconds,omitempty"`
	RecheckIntervalSeconds int              `json:"recheck_interval_seconds,omitempty"`
}
