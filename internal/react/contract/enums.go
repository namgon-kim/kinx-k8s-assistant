package contract

// LoopLifecycleState describes the loop goroutine's execution mechanism. It
// is derived from RuntimeControlState and must never carry workflow intent.
type LoopLifecycleState int

const (
	LoopLifecycleAwaitingUserInput LoopLifecycleState = iota
	LoopLifecycleModelTurn
	LoopLifecycleWaitingApproval
	LoopLifecycleWaitingContinuationChoice
	LoopLifecycleWaitingContinuationText
	LoopLifecycleExited
)

type InputOwner string

const (
	InputOwnerOrchestrator InputOwner = "orchestrator"
	InputOwnerReactChoice  InputOwner = "react_choice"
	InputOwnerReactText    InputOwner = "react_text"
	InputOwnerApproval     InputOwner = "approval"
)

// RuntimeControlState owns the runtime's next accepted obligation. Phase and
// step progression remain separate workflow axes.
type RuntimeControlState string

const (
	RuntimeControlUnset                                     RuntimeControlState = "unset"
	RuntimeControlAwaitingUserQuery                         RuntimeControlState = "awaiting_user_query"
	RuntimeControlAwaitingRequirementAnalysis               RuntimeControlState = "awaiting_requirement_analysis"
	RuntimeControlAwaitingPhasePlan                         RuntimeControlState = "awaiting_phase_plan"
	RuntimeControlAwaitingModelStep                         RuntimeControlState = "awaiting_model_step"
	RuntimeControlAwaitingResourceGuideLookup               RuntimeControlState = "awaiting_resource_guide_lookup"
	RuntimeControlAwaitingGuidedDiagnosisStep               RuntimeControlState = "awaiting_guided_diagnosis_step"
	RuntimeControlAwaitingGuidedPhaseProgress               RuntimeControlState = "awaiting_guided_phase_progress"
	RuntimeControlAwaitingFinalReport                       RuntimeControlState = "awaiting_final_report"
	RuntimeControlAwaitingNextDirections                    RuntimeControlState = "awaiting_next_directions"
	RuntimeControlAwaitingApproval                          RuntimeControlState = "awaiting_approval"
	RuntimeControlAwaitingToolResult                        RuntimeControlState = "awaiting_tool_result"
	RuntimeControlAwaitingMutationVerificationEvidence      RuntimeControlState = "awaiting_mutation_verification_evidence"
	RuntimeControlAwaitingMutationVerificationResult        RuntimeControlState = "awaiting_mutation_verification_result"
	RuntimeControlAwaitingMutationVerificationChainEvidence RuntimeControlState = "awaiting_mutation_verification_chain_evidence"
	RuntimeControlAwaitingMutationVerificationChainResult   RuntimeControlState = "awaiting_mutation_verification_chain_result"
	RuntimeControlAwaitingMutationContinuation              RuntimeControlState = "awaiting_mutation_continuation"
	RuntimeControlAwaitingContinuationHandoff               RuntimeControlState = "awaiting_continuation_handoff"
	RuntimeControlAwaitingContinuationChoice                RuntimeControlState = "awaiting_continuation_choice"
	RuntimeControlAwaitingContinuationText                  RuntimeControlState = "awaiting_continuation_text"
	RuntimeControlExited                                    RuntimeControlState = "exited"
)

type PhaseStatus string

const (
	PhasePending    PhaseStatus = "pending"
	PhaseActive     PhaseStatus = "active"
	PhaseCompleted  PhaseStatus = "completed"
	PhaseSkipped    PhaseStatus = "skipped"
	PhaseSuperseded PhaseStatus = "superseded"
)

type StepKind string

const (
	StepGeneralAction               StepKind = "general_action"
	StepLightweightLookup           StepKind = "lightweight_lookup"
	StepExplicitPhase               StepKind = "explicit_phase_step"
	StepResourceGuideDiagnostic     StepKind = "resource_guide_diagnostic"
	StepMutationEvidenceRequirement StepKind = "mutation_evidence_requirement"
)

type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepActive     StepStatus = "active"
	StepCompleted  StepStatus = "completed"
	StepAchieved   StepStatus = "achieved"
	StepBlocked    StepStatus = "blocked"
	StepSkipped    StepStatus = "skipped"
	StepRetrying   StepStatus = "retrying"
	StepSuperseded StepStatus = "superseded"
)

type UserInputKind string

const (
	InputChoiceNumber UserInputKind = "choice_number"
	InputApproval     UserInputKind = "approval"
	InputSlashMeta    UserInputKind = "slash_meta"
	InputFreeText     UserInputKind = "free_text"
	InputEmpty        UserInputKind = "empty"
)

type InputHandlerKind string

const (
	InputHandlerNone             InputHandlerKind = "none"
	InputHandlerOrchestratorMeta InputHandlerKind = "orchestrator_meta"
	InputHandlerReactChoice      InputHandlerKind = "react_choice"
	InputHandlerReactText        InputHandlerKind = "react_text"
	InputHandlerReactApproval    InputHandlerKind = "react_approval"
	InputHandlerUserQuery        InputHandlerKind = "user_query"
)
