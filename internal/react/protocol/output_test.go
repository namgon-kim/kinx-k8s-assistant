package protocol

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestClassifyNativeCallName(t *testing.T) {
	tests := []struct {
		name    string
		kind    contract.ModelOutputKind
		support OutputSupport
	}{
		{name: RequirementAnalysisCall, kind: contract.OutputRequirementAnalysis, support: OutputSupported},
		{name: "requirement_analysis", kind: contract.OutputRequirementAnalysis, support: OutputSupported},
		{name: RequestContextCall, kind: contract.OutputRequirementAnalysis, support: OutputSupported},
		{name: PhasePlanCall, kind: contract.OutputPhasePlan, support: OutputSupported},
		{name: "step_result", kind: contract.OutputStepResult, support: OutputSupported},
		{name: "__step_result__", kind: contract.OutputStepResult, support: OutputSupported},
		{name: "phase_plan_revision", kind: contract.OutputPhasePlanRevision, support: OutputSupported},
		{name: "__phase_plan_revision__", kind: contract.OutputPhasePlanRevision, support: OutputSupported},
		{name: ResourceGuideLookupCall, kind: contract.OutputResourceGuideLookup, support: OutputSupported},
		{name: MutationVerificationResultCall, kind: contract.OutputMutationVerificationResult, support: OutputSupported},
		{name: ContinuationHandoffCall, kind: contract.OutputContinuationHandoff, support: OutputSupported},
		{name: "kubectl", kind: contract.OutputAction, support: OutputSupported},
		{name: "__future_internal_call__", support: OutputUnknown},
		{name: "", support: OutputUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyNativeCallName(tt.name)
			if got.Kind != tt.kind || got.Support != tt.support {
				t.Fatalf("classification = %#v, want kind=%q support=%q", got, tt.kind, tt.support)
			}
		})
	}
}

func TestClassifyShimKeyFailsClosedForUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		kind    contract.ModelOutputKind
		support OutputSupport
	}{
		{name: "action", kind: contract.OutputAction, support: OutputSupported},
		{name: "answer", kind: contract.OutputPlainAnswer, support: OutputSupported},
		{name: "phase_plan", kind: contract.OutputPhasePlan, support: OutputSupported},
		{name: "step_result", kind: contract.OutputStepResult, support: OutputSupported},
		{name: "future_internal_call", support: OutputUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyShimKey(tt.name)
			if got.Kind != tt.kind || got.Support != tt.support {
				t.Fatalf("classification = %#v, want kind=%q support=%q", got, tt.kind, tt.support)
			}
		})
	}
}

func TestClassifyPlainAnswerRejectsEmptyText(t *testing.T) {
	if got := ClassifyPlainAnswer("answer"); got.Kind != contract.OutputPlainAnswer || got.Support != OutputSupported {
		t.Fatalf("classification = %#v", got)
	}
	if got := ClassifyPlainAnswer("  "); got.Support != OutputUnknown {
		t.Fatalf("empty answer support = %q, want unknown", got.Support)
	}
}

func TestStructuredCallNamesMatchSupportedOutputKinds(t *testing.T) {
	classified := map[contract.ModelOutputKind]bool{}
	for _, callName := range StructuredCallNames {
		native := ClassifyNativeCallName(callName)
		shim := ClassifyShimKey(BareInternalCallName(callName))
		if native.Support != OutputSupported {
			t.Errorf("native call %q support = %q", callName, native.Support)
		}
		if native.Kind != shim.Kind || shim.Support != OutputSupported {
			t.Errorf("native/shim classification mismatch for %q: native=%#v shim=%#v", callName, native, shim)
		}
		classified[native.Kind] = true
	}
	for _, kind := range SupportedStructuredOutputKinds() {
		if !classified[kind] {
			t.Errorf("supported output kind %q has no structured call mapping", kind)
		}
		delete(classified, kind)
	}
	for kind := range classified {
		t.Errorf("structured call maps to unregistered supported kind %q", kind)
	}
}
