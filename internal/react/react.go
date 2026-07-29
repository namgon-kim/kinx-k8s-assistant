// Package react exposes the public ReAct-loop API. The implementation lives
// in coordinator; callers should not depend on its internal workflow layout.
package react

import (
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/coordinator"
)

type Loop = coordinator.Loop
type RuntimeSnapshot = coordinator.RuntimeSnapshot
type RuntimeControlState = coordinator.RuntimeControlState
type ControlState = RuntimeControlState
type InputOwner = coordinator.InputOwner
type UserInputKind = coordinator.UserInputKind
type InputHandlerKind = coordinator.InputHandlerKind
type InputDispatchDecision = coordinator.InputDispatchDecision
type PhaseRef = coordinator.PhaseRef
type PhaseRuntimeState = coordinator.PhaseRuntimeState
type PhaseSpec = coordinator.PhaseSpec
type StepRef = coordinator.StepRef
type StepRuntimeState = coordinator.StepRuntimeState
type PhaseStatus = coordinator.PhaseStatus
type StepKind = coordinator.StepKind
type StepStatus = coordinator.StepStatus

const (
	RuntimeControlUnset                                     = coordinator.RuntimeControlUnset
	RuntimeControlAwaitingUserQuery                         = coordinator.RuntimeControlAwaitingUserQuery
	RuntimeControlAwaitingRequirementAnalysis               = coordinator.RuntimeControlAwaitingRequirementAnalysis
	RuntimeControlAwaitingPhasePlan                         = coordinator.RuntimeControlAwaitingPhasePlan
	RuntimeControlAwaitingModelStep                         = coordinator.RuntimeControlAwaitingModelStep
	RuntimeControlAwaitingResourceGuideLookup               = coordinator.RuntimeControlAwaitingResourceGuideLookup
	RuntimeControlAwaitingGuidedDiagnosisStep               = coordinator.RuntimeControlAwaitingGuidedDiagnosisStep
	RuntimeControlAwaitingGuidedPhaseProgress               = coordinator.RuntimeControlAwaitingGuidedPhaseProgress
	RuntimeControlAwaitingFinalReport                       = coordinator.RuntimeControlAwaitingFinalReport
	RuntimeControlAwaitingNextDirections                    = coordinator.RuntimeControlAwaitingNextDirections
	RuntimeControlAwaitingContinuationChoice                = coordinator.RuntimeControlAwaitingContinuationChoice
	RuntimeControlAwaitingContinuationText                  = coordinator.RuntimeControlAwaitingContinuationText
	RuntimeControlAwaitingApproval                          = coordinator.RuntimeControlAwaitingApproval
	RuntimeControlAwaitingToolResult                        = coordinator.RuntimeControlAwaitingToolResult
	RuntimeControlAwaitingMutationVerificationEvidence      = coordinator.RuntimeControlAwaitingMutationVerificationEvidence
	RuntimeControlAwaitingMutationVerificationResult        = coordinator.RuntimeControlAwaitingMutationVerificationResult
	RuntimeControlAwaitingMutationVerificationChainEvidence = coordinator.RuntimeControlAwaitingMutationVerificationChainEvidence
	RuntimeControlAwaitingMutationVerificationChainResult   = coordinator.RuntimeControlAwaitingMutationVerificationChainResult
	RuntimeControlAwaitingMutationContinuation              = coordinator.RuntimeControlAwaitingMutationContinuation
	RuntimeControlAwaitingContinuationHandoff               = coordinator.RuntimeControlAwaitingContinuationHandoff
	RuntimeControlExited                                    = coordinator.RuntimeControlExited

	// Compatibility aliases preserve the facade used before the package split.
	ControlIdle                                      = RuntimeControlUnset
	ControlAwaitingUserQuery                         = RuntimeControlAwaitingUserQuery
	ControlAwaitingRequirementAnalysis               = RuntimeControlAwaitingRequirementAnalysis
	ControlAwaitingPhasePlan                         = RuntimeControlAwaitingPhasePlan
	ControlAwaitingModelStep                         = RuntimeControlAwaitingModelStep
	ControlAwaitingResourceGuideLookup               = RuntimeControlAwaitingResourceGuideLookup
	ControlAwaitingGuidedDiagnosisStep               = RuntimeControlAwaitingGuidedDiagnosisStep
	ControlAwaitingGuidedPhaseProgress               = RuntimeControlAwaitingGuidedPhaseProgress
	ControlAwaitingFinalReport                       = RuntimeControlAwaitingFinalReport
	ControlAwaitingNextDirections                    = RuntimeControlAwaitingNextDirections
	ControlAwaitingContinuationChoice                = RuntimeControlAwaitingContinuationChoice
	ControlAwaitingContinuationText                  = RuntimeControlAwaitingContinuationText
	ControlAwaitingApproval                          = RuntimeControlAwaitingApproval
	ControlAwaitingToolResult                        = RuntimeControlAwaitingToolResult
	ControlAwaitingMutationVerificationEvidence      = RuntimeControlAwaitingMutationVerificationEvidence
	ControlAwaitingMutationVerificationResult        = RuntimeControlAwaitingMutationVerificationResult
	ControlAwaitingMutationVerificationChainEvidence = RuntimeControlAwaitingMutationVerificationChainEvidence
	ControlAwaitingMutationVerificationChainResult   = RuntimeControlAwaitingMutationVerificationChainResult
	ControlAwaitingMutationContinuation              = RuntimeControlAwaitingMutationContinuation
	ControlAwaitingContinuationHandoff               = RuntimeControlAwaitingContinuationHandoff
	ControlExited                                    = RuntimeControlExited

	InputOwnerOrchestrator = coordinator.InputOwnerOrchestrator
	InputOwnerReactChoice  = coordinator.InputOwnerReactChoice
	InputOwnerReactText    = coordinator.InputOwnerReactText
	InputOwnerApproval     = coordinator.InputOwnerApproval

	InputChoiceNumber = coordinator.InputChoiceNumber
	InputApproval     = coordinator.InputApproval
	InputSlashMeta    = coordinator.InputSlashMeta
	InputFreeText     = coordinator.InputFreeText
	InputEmpty        = coordinator.InputEmpty

	InputHandlerNone             = coordinator.InputHandlerNone
	InputHandlerOrchestratorMeta = coordinator.InputHandlerOrchestratorMeta
	InputHandlerReactChoice      = coordinator.InputHandlerReactChoice
	InputHandlerReactText        = coordinator.InputHandlerReactText
	InputHandlerReactApproval    = coordinator.InputHandlerReactApproval
	InputHandlerUserQuery        = coordinator.InputHandlerUserQuery

	PhasePending    = coordinator.PhasePending
	PhaseActive     = coordinator.PhaseActive
	PhaseCompleted  = coordinator.PhaseCompleted
	PhaseSkipped    = coordinator.PhaseSkipped
	PhaseSuperseded = coordinator.PhaseSuperseded

	StepGeneralAction               = coordinator.StepGeneralAction
	StepLightweightLookup           = coordinator.StepLightweightLookup
	StepExplicitPhase               = coordinator.StepExplicitPhase
	StepResourceGuideDiagnostic     = coordinator.StepResourceGuideDiagnostic
	StepMutationEvidenceRequirement = coordinator.StepMutationEvidenceRequirement

	StepPending    = coordinator.StepPending
	StepActive     = coordinator.StepActive
	StepCompleted  = coordinator.StepCompleted
	StepAchieved   = coordinator.StepAchieved
	StepBlocked    = coordinator.StepBlocked
	StepSkipped    = coordinator.StepSkipped
	StepRetrying   = coordinator.StepRetrying
	StepSuperseded = coordinator.StepSuperseded
)

func New(cfg *config.Config) (*Loop, error) {
	return coordinator.New(cfg)
}

func ClassifyUserInput(input string) UserInputKind {
	return coordinator.ClassifyUserInput(input)
}

func DecideInputDispatch(control RuntimeControlState, kind UserInputKind) InputDispatchDecision {
	return coordinator.DecideInputDispatch(control, kind)
}
