package protocol

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestNormalizeModelOutputSeparatesEventsActionsAndProgress(t *testing.T) {
	envelope := NormalizeModelOutput("checking pods", []contract.FunctionCall{
		{Name: PhaseProgressCall, Arguments: map[string]any{"phase_completed": 1}},
		{Name: "kubectl", Arguments: map[string]any{"command": "kubectl get pods"}},
	})
	if envelope.ProgressText != "checking pods" || envelope.PlainAnswer != "" {
		t.Fatalf("text classification = %#v", envelope)
	}
	if len(envelope.InternalEvents) != 1 || envelope.InternalEvents[0].Kind != contract.OutputPhaseProgress {
		t.Fatalf("internal events = %#v", envelope.InternalEvents)
	}
	if len(envelope.ExternalActions) != 1 || envelope.ExternalActions[0].Call.Name != "kubectl" {
		t.Fatalf("external actions = %#v", envelope.ExternalActions)
	}
}

func TestNormalizeModelOutputRejectsUnknownInternalCall(t *testing.T) {
	envelope := NormalizeModelOutput("", []contract.FunctionCall{{Name: "__future_internal__"}})
	if len(envelope.InvalidOutputs) != 1 || len(envelope.InternalEvents) != 0 || len(envelope.ExternalActions) != 0 {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestNormalizeModelOutputReadsOptionalStepRef(t *testing.T) {
	envelope := NormalizeModelOutput("", []contract.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{"step_ref": map[string]any{
			"kind":  string(contract.StepResourceGuideDiagnostic),
			"index": float64(2),
			"phase": map[string]any{"name": "guided_diagnosis", "index": float64(3)},
		}},
	}})
	ref := envelope.ExternalActions[0].StepRef
	if ref == nil || ref.Kind != contract.StepResourceGuideDiagnostic || ref.Index != 2 || ref.Phase.Index != 3 {
		t.Fatalf("step ref = %#v", ref)
	}
}

func TestNormalizeModelOutputPreservesPhaseOnlyStepRefAssertion(t *testing.T) {
	envelope := NormalizeModelOutput("", []contract.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{"step_ref": map[string]any{
			"phase": map[string]any{"id": "phase-1"},
		}},
	}})
	ref := envelope.ExternalActions[0].StepRef
	if ref == nil || ref.Phase.ID != "phase-1" {
		t.Fatalf("phase-only step ref = %#v", ref)
	}
}
