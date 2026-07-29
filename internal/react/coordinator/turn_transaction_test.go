package coordinator

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
)

func TestRuntimeTransactionRejectsInvalidCandidateWithoutPartialState(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	loop.mutableRuntime()
	beforeRevision := loop.stateStore.Revision()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	loop.mutableRuntime().control = RuntimeControlAwaitingFinalReport
	loop.mutableRuntime().pendingMutationVerification = &pendingMutationVerification{
		Checks: []verificationRuntimeCheck{{ID: "still-pending"}},
	}
	if err := loop.commitRuntimeTransaction(tx); err == nil {
		t.Fatal("invalid candidate was committed")
	}
	if loop.controlState() != RuntimeControlAwaitingModelStep || loop.mutableRuntime().pendingMutationVerification != nil {
		t.Fatalf("partial state remained after rejection: %#v", loop.RuntimeSnapshot())
	}
	if loop.stateStore.Revision() != beforeRevision {
		t.Fatalf("revision changed from %d to %d", beforeRevision, loop.stateStore.Revision())
	}
}

func TestRuntimeTransactionCommitAdvancesRevisionOnce(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	loop.mutableRuntime()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	loop.mutableRuntime().currIteration++
	if err := loop.commitRuntimeTransaction(tx); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	if loop.stateStore.Revision() != 1 || loop.RuntimeSnapshot().Revision != 1 {
		t.Fatalf("revision = %d/%d, want 1", loop.stateStore.Revision(), loop.RuntimeSnapshot().Revision)
	}
}

func TestAtomicRuntimeMutationRejectsWholeInvalidUpdate(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	loop.mutableRuntime()

	err := loop.mutateRuntimeAtomically(func() error {
		loop.mutableRuntime().originalQuery = "partially initialized query"
		loop.mutableRuntime().pendingMutationVerification = &pendingMutationVerification{
			Checks: []verificationRuntimeCheck{{ID: "still-pending"}},
		}
		loop.transitionControl(RuntimeControlAwaitingFinalReport)
		return nil
	})
	if err == nil {
		t.Fatal("invalid atomic runtime update was committed")
	}
	if loop.controlState() != RuntimeControlAwaitingModelStep {
		t.Fatalf("control = %q, want original model-step control", loop.controlState())
	}
	if loop.mutableRuntime().originalQuery != "" || loop.mutableRuntime().pendingMutationVerification != nil {
		t.Fatal("rejected atomic runtime update leaked partial state")
	}
	if loop.stateStore.Revision() != 0 {
		t.Fatalf("revision = %d, want 0", loop.stateStore.Revision())
	}
}

func TestRuntimeTransactionRejectsStaleTurnEntryRevision(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	loop.mutableRuntime()
	turnRevision := loop.stateStore.Revision()
	loop.transitionControl(RuntimeControlAwaitingModelStep)
	if _, err := loop.beginRuntimeTransaction(turnRevision); err == nil {
		t.Fatal("stale turn-entry revision was accepted")
	}
	if loop.activeTransaction != nil {
		t.Fatal("stale response left an active transaction")
	}
}

func TestRuntimeTransactionRejectsNestedCandidate(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
	}
	loop.mutableRuntime()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer loop.rollbackRuntimeTransaction(tx)

	if _, err := loop.beginRuntimeTransaction(); err == nil {
		t.Fatal("nested runtime transaction was accepted")
	}
	if loop.activeTransaction != tx || loop.mutableRuntime() != tx.candidate {
		t.Fatal("nested transaction attempt replaced the active candidate")
	}
}

func TestRuntimeSnapshotDeepCopiesNestedActionData(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingApproval,
			pendingCalls: []PendingCall{{
				FunctionCall: gollm.FunctionCall{Arguments: map[string]any{
					"target": map[string]any{"resource": "pods"},
				}},
			}},
		},
	}
	loop.mutableRuntime()

	snapshot := loop.RuntimeSnapshot()
	target := snapshot.PendingCalls[0].FunctionCall.Arguments["target"].(map[string]any)
	target["resource"] = "deployments"

	committedTarget := loop.mutableRuntime().pendingCalls[0].FunctionCall.Arguments["target"].(map[string]any)
	if got := committedTarget["resource"]; got != "pods" {
		t.Fatalf("snapshot mutation leaked into session root: %v", got)
	}
}

func TestRuntimeCandidateDeepCopiesChatObservation(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			currChatContent: []any{gollm.FunctionCallResult{
				Name:   "kubectl",
				Result: map[string]any{"target": map[string]any{"resource": "pods"}},
			}},
		},
	}
	loop.mutableRuntime()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer loop.rollbackRuntimeTransaction(tx)

	result := loop.mutableRuntime().currChatContent[0].(gollm.FunctionCallResult)
	result.Result["target"].(map[string]any)["resource"] = "deployments"

	committed := tx.previous.currChatContent[0].(gollm.FunctionCallResult)
	if got := committed.Result["target"].(map[string]any)["resource"]; got != "pods" {
		t.Fatalf("candidate chat mutation leaked into committed root: %v", got)
	}
}

func TestImmediateEffectMessagePublishesCommittedSnapshot(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
		},
		output: make(chan *api.Message, 1),
	}
	loop.mutableRuntime()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer loop.rollbackRuntimeTransaction(tx)
	loop.mutableRuntime().currIteration = 9
	loop.mutableRuntime().originalQuery = "uncommitted query"

	loop.emitMessage(api.MessageSourceModel, api.MessageTypeToolCallRequest, "kubectl get pods")

	snapshot, ok := loop.PublishedRuntimeSnapshot()
	if !ok {
		t.Fatal("effect message did not publish a snapshot")
	}
	if snapshot.Revision != tx.expected {
		t.Fatalf("published revision = %d, want committed revision %d", snapshot.Revision, tx.expected)
	}
	if snapshot.OriginalQuery != "" {
		t.Fatalf("published snapshot leaked candidate query %q", snapshot.OriginalQuery)
	}
	if loop.mutableRuntime().currIteration != 9 {
		t.Fatal("candidate was replaced while publishing committed snapshot")
	}
}

func TestRequirementAcceptanceQueuesPreparationAfterCandidateCommit(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingRequirementAnalysis,
		},
	}
	loop.mutableRuntime()
	tx, err := loop.beginRuntimeTransaction()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	remaining, handled := loop.consumeRequestContext(t.Context(), []gollm.FunctionCall{{
		Name: protocol.RequirementAnalysisCall,
		Arguments: map[string]any{
			"request_type": "lookup",
			"action":       "list",
			"target": map[string]any{
				"category":    "kubernetes_resource",
				"description": "pods",
			},
			"scope": map[string]any{"type": "namespaced", "namespace": "default"},
			"resource_candidates": []any{map[string]any{
				"kind": "pods",
				"role": "primary",
			}},
		},
	}})
	if !handled || len(remaining) != 0 {
		loop.rollbackRuntimeTransaction(tx)
		t.Fatalf("requirement output was not consumed: handled=%v remaining=%d", handled, len(remaining))
	}
	if loop.mutableRuntime().resourceClassification != nil {
		loop.rollbackRuntimeTransaction(tx)
		t.Fatal("resource discovery ran before candidate commit")
	}
	if len(tx.effects) != 1 || tx.effects[0].Kind != contract.EffectPrepareRequest {
		loop.rollbackRuntimeTransaction(tx)
		t.Fatalf("effects = %#v, want prepare_request", tx.effects)
	}
	if err := loop.commitRuntimeTransaction(tx); err != nil {
		t.Fatalf("commit requirement candidate: %v", err)
	}
	if loop.controlState() != RuntimeControlAwaitingPhasePlan || loop.mutableRuntime().requirementAnalysis == nil {
		t.Fatal("accepted requirement was not committed before preparation effect")
	}
}

func TestTurnOutputPolicyRejectsStateEventActionMixBeforePhaseMutation(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingModelStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps: []phaseStep{{
					Index: 1, Name: "diagnosis", Goal: "inspect", CompletionCondition: "observed",
				}},
				Completed: map[int]bool{},
			},
		},
	}
	calls := []gollm.FunctionCall{
		{Name: protocol.PhaseProgressCall, Arguments: map[string]any{"phase_completed": 1}},
		{Name: "kubectl", Arguments: map[string]any{"command": "kubectl get pods"}},
	}
	envelope := normalizeModelOutputEnvelope("", calls)
	if !loop.validateModelOutputEnvelope(&envelope, calls) {
		t.Fatal("mixed state event/action output was accepted")
	}
	if loop.mutableRuntime().phaseStepState.Completed[1] {
		t.Fatal("phase mutated before output policy rejection")
	}
}

func TestActionProposalBindsToSingleActiveGuideStep(t *testing.T) {
	loop := &Loop{
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingGuidedDiagnosisStep,
			phaseStepState: &phaseStepState{
				CurrentPhaseIndex: 1,
				PhaseSteps:        []phaseStep{{Index: 1, Name: "guided_diagnosis"}},
				Completed:         map[int]bool{},
			},
			guideStepState: &guideStepState{
				TotalSteps:  1,
				StepDetails: []guideStepDetail{{Index: 1, Description: "inspect pods"}},
				Completed:   map[int]bool{},
				Skipped:     map[int]bool{},
			},
		},
	}
	calls := []gollm.FunctionCall{{Name: "kubectl", Arguments: map[string]any{"command": "kubectl get pods"}}}
	envelope := normalizeModelOutputEnvelope("", calls)
	if handled := loop.validateModelOutputEnvelope(&envelope, calls); handled {
		t.Fatal("valid guide action was rejected")
	}
	ref := envelope.ExternalActions[0].BoundStepRef
	if ref == nil || ref.Kind != contract.StepResourceGuideDiagnostic || ref.Index != 1 {
		t.Fatalf("bound step = %#v", ref)
	}
}
