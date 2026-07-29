package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/kube"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
)

func TestPendingCallFromDispatchClassifiesPreInvocationRejections(t *testing.T) {
	base := contract.ToolDispatchIntent{
		ID:               "dispatch-1",
		AttemptID:        "attempt-1",
		Call:             contract.FunctionCall{ID: "call-1", Name: "kubectl", Arguments: map[string]any{"command": "kubectl get pods"}},
		ModifiesResource: "no",
		Status:           contract.ToolDispatchPending,
	}
	hash, err := hashCanonicalDispatch(canonicalPayloadFromIntent(base))
	if err != nil {
		t.Fatalf("hash canonical dispatch: %v", err)
	}
	base.CanonicalPayloadHash = hash

	t.Run("approval missing", func(t *testing.T) {
		intent := base
		intent.ApprovalRequired = true
		if _, err := (&Loop{}).pendingCallFromDispatch(context.Background(), intent); dispatchAdmissionCodeFromError(err) != dispatchAdmissionApprovalMissing {
			t.Fatalf("error = %v, want %q", err, dispatchAdmissionApprovalMissing)
		}
	})

	t.Run("integrity mismatch", func(t *testing.T) {
		intent := base
		intent.CanonicalPayloadHash = "tampered"
		if _, err := (&Loop{}).pendingCallFromDispatch(context.Background(), intent); dispatchAdmissionCodeFromError(err) != dispatchAdmissionIntegrityMismatch {
			t.Fatalf("error = %v, want %q", err, dispatchAdmissionIntegrityMismatch)
		}
	})

	t.Run("invocation parse failure", func(t *testing.T) {
		registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
		if err != nil {
			t.Fatalf("new registry: %v", err)
		}
		defer registry.Close()

		intent := base
		intent.Call.Name = "missing-tool"
		intent.CanonicalPayloadHash, err = hashCanonicalDispatch(canonicalPayloadFromIntent(intent))
		if err != nil {
			t.Fatalf("hash invalid invocation: %v", err)
		}
		loop := &Loop{registry: registry}
		if _, err := loop.pendingCallFromDispatch(context.Background(), intent); dispatchAdmissionCodeFromError(err) != dispatchAdmissionInvocationInvalid {
			t.Fatalf("error = %v, want %q", err, dispatchAdmissionInvocationInvalid)
		}
	})
}

func TestCancelUninvokedDispatchesClosesEveryToolCallWithoutCancellingSharedAttempt(t *testing.T) {
	execution := &session.GoalExecutionState{RequestID: "request-1"}
	if err := execution.AppendAttempt(contract.AttemptRecord{ID: "attempt-owned", Status: contract.AttemptDispatchPending}); err != nil {
		t.Fatalf("append owned attempt: %v", err)
	}
	if err := execution.AppendAttempt(contract.AttemptRecord{ID: "attempt-shared", Status: contract.AttemptVerifying}); err != nil {
		t.Fatalf("append shared attempt: %v", err)
	}
	loop := &Loop{
		cfg: &config.Config{},
		runtimeState: &runtimeState{
			execution: execution,
			dispatchIntents: map[string]contract.ToolDispatchIntent{
				"dispatch-owned": {
					ID:              "dispatch-owned",
					AttemptID:       "attempt-owned",
					AttemptReserved: true,
					Call:            contract.FunctionCall{ID: "call-owned", Name: "kubectl"},
					Status:          contract.ToolDispatchPending,
				},
				"dispatch-shared": {
					ID:        "dispatch-shared",
					AttemptID: "attempt-shared",
					Call:      contract.FunctionCall{ID: "call-shared", Name: "kubectl"},
					Status:    contract.ToolDispatchPending,
				},
			},
			dispatchOrder: []string{"dispatch-owned", "dispatch-shared"},
		},
	}

	loop.cancelUninvokedDispatches(
		[]string{"dispatch-owned", "dispatch-shared"},
		"earlier dispatch failed",
		"dispatch-cause",
	)

	if got := loop.mutableRuntime().dispatchIntents["dispatch-owned"].Status; got != contract.ToolDispatchCancelled {
		t.Fatalf("owned dispatch status = %q", got)
	}
	if got := loop.mutableRuntime().dispatchIntents["dispatch-shared"].Status; got != contract.ToolDispatchCancelled {
		t.Fatalf("shared dispatch status = %q", got)
	}
	owned, _ := loop.mutableRuntime().execution.AttemptByID("attempt-owned")
	if owned.Status != contract.AttemptCancelled {
		t.Fatalf("owned attempt status = %q", owned.Status)
	}
	shared, _ := loop.mutableRuntime().execution.AttemptByID("attempt-shared")
	if shared.Status != contract.AttemptVerifying {
		t.Fatalf("shared attempt status = %q, want verifying", shared.Status)
	}

	results := dispatchFunctionCallResults(loop.mutableRuntime().currChatContent)
	if len(results) != 2 {
		t.Fatalf("function call results = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.Result["status"] != "cancelled" || result.Result["execution_state"] != "not_invoked" {
			t.Fatalf("unexpected cancellation result: %#v", result.Result)
		}
		if result.Result["caused_by_dispatch_id"] != "dispatch-cause" {
			t.Fatalf("missing cancellation cause: %#v", result.Result)
		}
	}
}

func TestApprovalDependsOnlyOnExplicitRiskFlag(t *testing.T) {
	if callRequiresApproval(PendingCall{ModifiesResource: "yes", Risk: &contract.CommandRisk{Risky: false}}) {
		t.Fatal("non-risky mutation unexpectedly required approval")
	}
	if !callRequiresApproval(PendingCall{ModifiesResource: "no", Risk: &contract.CommandRisk{Risky: true, Reason: "privileged command"}}) {
		t.Fatal("risky command did not require approval")
	}
}

func TestRejectToolDispatchRecordsNotInvokedResultWithoutToolObservation(t *testing.T) {
	loop := &Loop{
		cfg:    &config.Config{},
		output: make(chan *api.Message, 4),
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingToolResult,
			execution: &session.GoalExecutionState{
				SessionID:   "session-1",
				RequestID:   "request-1",
				Corrections: map[contract.CorrectionKey]contract.CorrectionState{},
			},
			dispatchIntents: map[string]contract.ToolDispatchIntent{
				"dispatch-1": {
					ID:              "dispatch-1",
					AttemptID:       "attempt-1",
					AttemptReserved: false,
					Call:            contract.FunctionCall{ID: "call-1", Name: "kubectl"},
					Status:          contract.ToolDispatchPending,
				},
			},
			dispatchOrder: []string{"dispatch-1"},
		},
	}
	admission := &dispatchAdmissionError{
		Code:       dispatchAdmissionIntegrityMismatch,
		DispatchID: "dispatch-1",
		Err:        context.Canceled,
	}
	if err := loop.rejectToolDispatch(context.Background(), "dispatch-1", admission, nil); err != nil {
		t.Fatalf("rejectToolDispatch() error = %v", err)
	}

	intent := loop.mutableRuntime().dispatchIntents["dispatch-1"]
	if intent.Status != contract.ToolDispatchRejected {
		t.Fatalf("dispatch status = %q, want rejected", intent.Status)
	}
	results := dispatchFunctionCallResults(loop.mutableRuntime().currChatContent)
	if len(results) != 1 {
		t.Fatalf("function call results = %d, want 1", len(results))
	}
	if results[0].ID != "call-1" || results[0].Result["execution_state"] != "not_invoked" {
		t.Fatalf("unexpected rejection result: %#v", results[0])
	}
	if observations := loop.mutableRuntime().execution.OrderedObservations(); len(observations) != 1 || observations[0].Tool != "tool_dispatch_rejected" {
		t.Fatalf("unexpected internal observations: %#v", observations)
	}
	if loop.controlState() != RuntimeControlAwaitingUserQuery {
		t.Fatalf("control = %q, want awaiting_user_query", loop.controlState())
	}
}

func TestAuditRuntimeStateReturnsRecoveryCommitFailure(t *testing.T) {
	loop := &Loop{
		cfg: &config.Config{},
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingApproval,
			dispatchIntents: map[string]contract.ToolDispatchIntent{
				"dispatch-1": {
					ID:               "dispatch-1",
					Call:             contract.FunctionCall{ID: "call-1", Name: "kubectl"},
					ModifiesResource: "no",
					Status:           contract.ToolDispatchPending,
				},
			},
			dispatchOrder: []string{"dispatch-1"},
		},
	}
	handled, err := loop.auditRuntimeState()
	if err == nil {
		t.Fatal("audit recovery commit failure was swallowed")
	}
	if handled {
		t.Fatal("failed recovery reported itself as handled")
	}
	if !strings.Contains(err.Error(), "recover committed tool dispatch") {
		t.Fatalf("unexpected recovery error: %v", err)
	}
}

func TestAuditRuntimeStateRecoversCommittedReadOnlyDispatchOnce(t *testing.T) {
	loop := &Loop{
		cfg:    &config.Config{},
		output: make(chan *api.Message, 2),
		runtimeState: &runtimeState{
			control: RuntimeControlAwaitingToolResult,
			dispatchIntents: map[string]contract.ToolDispatchIntent{
				"dispatch-1": {
					ID:               "dispatch-1",
					Call:             contract.FunctionCall{ID: "call-1", Name: "kubectl"},
					ModifiesResource: "no",
					Status:           contract.ToolDispatchPending,
				},
			},
			dispatchOrder: []string{"dispatch-1"},
		},
	}
	handled, err := loop.auditRuntimeState()
	if err != nil {
		t.Fatalf("auditRuntimeState() error = %v", err)
	}
	if !handled {
		t.Fatal("committed pending dispatch was not recovered")
	}
	if status := loop.mutableRuntime().dispatchIntents["dispatch-1"].Status; status != contract.ToolDispatchCancelled {
		t.Fatalf("dispatch status = %q, want cancelled", status)
	}
	if loop.controlState() != RuntimeControlAwaitingModelStep {
		t.Fatalf("control = %q, want awaiting_model_step", loop.controlState())
	}
	if results := dispatchFunctionCallResults(loop.mutableRuntime().currChatContent); len(results) != 1 {
		t.Fatalf("function call results = %d, want 1", len(results))
	}
}

func TestDiscoveryCommandAllowlistIsReadOnly(t *testing.T) {
	for _, operation := range []discoveryOperation{discoveryListCRDs, discoveryListAPIResources} {
		command, err := discoveryCommand(operation)
		if err != nil {
			t.Fatalf("discoveryCommand(%q): %v", operation, err)
		}
		if !strings.HasPrefix(command, "kubectl ") {
			t.Fatalf("discovery command is not kubectl: %q", command)
		}
		if !kube.IsReadOnlyKubectlPipeline(command) {
			t.Fatalf("discovery command is not classified read-only: %q", command)
		}
	}
	if _, err := discoveryCommand("delete_resources"); err == nil {
		t.Fatal("unknown discovery operation was accepted")
	}
}

func dispatchAdmissionCodeFromError(err error) dispatchAdmissionCode {
	admission, ok := err.(*dispatchAdmissionError)
	if !ok || admission == nil {
		return ""
	}
	return admission.Code
}

func dispatchFunctionCallResults(values []any) []gollm.FunctionCallResult {
	var results []gollm.FunctionCallResult
	for _, value := range values {
		if result, ok := value.(gollm.FunctionCallResult); ok {
			results = append(results, result)
		}
	}
	return results
}
