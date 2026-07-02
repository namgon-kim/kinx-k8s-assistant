package react

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

func TestInputDispatchDecisionTable(t *testing.T) {
	tests := []struct {
		name     string
		control  ControlState
		input    string
		accepted bool
		handler  InputHandlerKind
	}{
		{
			name:     "continuation choice accepts number",
			control:  ControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
			handler:  InputHandlerReactChoice,
		},
		{
			name:     "continuation choice rejects approval token",
			control:  ControlAwaitingContinuationChoice,
			input:    "y",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "choice rejects slash meta",
			control:  ControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "text accepts slash meta",
			control:  ControlAwaitingContinuationText,
			input:    "/help",
			accepted: true,
			handler:  InputHandlerOrchestratorMeta,
		},
		{
			name:     "text accepts free text",
			control:  ControlAwaitingContinuationText,
			input:    "네임스페이스가 달라",
			accepted: true,
			handler:  InputHandlerReactText,
		},
		{
			name:     "approval rejects slash meta",
			control:  ControlAwaitingApproval,
			input:    "/readonly status",
			accepted: false,
			handler:  InputHandlerNone,
		},
		{
			name:     "user query accepts slash meta",
			control:  ControlAwaitingUserQuery,
			input:    "/readonly status",
			accepted: true,
			handler:  InputHandlerOrchestratorMeta,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := DecideInputDispatch(tt.control, ClassifyUserInput(tt.input))
			if decision.Accepted != tt.accepted || decision.Handler != tt.handler {
				t.Fatalf("decision = %#v, want accepted=%v handler=%s", decision, tt.accepted, tt.handler)
			}
		})
	}
}

func TestNextDirectionsRequiredGateBlocksOtherStructuredOutput(t *testing.T) {
	loop := &Loop{
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.enforceRequestedStructuredDirective([]gollm.FunctionCall{{Name: "kubectl"}}) {
		t.Fatal("next_directions_required gate should block action")
	}
	if !strings.Contains(loop.pendingResponseDirective, "next_directions") {
		t.Fatalf("expected next_directions directive, got %q", loop.pendingResponseDirective)
	}
}

func TestRuntimeStateAuditAllowsMutationVerificationToPrecedeRequestedReport(t *testing.T) {
	snapshot := RuntimeSnapshot{
		LoopState:                    StateRunning,
		PendingMutationVerification:  &pendingMutationVerification{},
		FinalReportRequested:         true,
		GuidedPhaseProgressRequested: false,
	}
	if got := snapshot.AuditError(); got != "" {
		t.Fatalf("audit error = %q, want none because mutation verification control takes precedence", got)
	}
	if got := snapshot.deriveControl(); got != ControlAwaitingMutationVerificationEvidence {
		t.Fatalf("control = %s, want %s", got, ControlAwaitingMutationVerificationEvidence)
	}
}

func TestRuntimeStateAuditDetectsConflictingRequestedReports(t *testing.T) {
	snapshot := RuntimeSnapshot{
		LoopState:                    StateRunning,
		FinalReportRequested:         true,
		GuidedPhaseProgressRequested: true,
	}
	if got := snapshot.AuditError(); got == "" {
		t.Fatal("expected final report plus guided phase progress request to be invalid")
	}
}

func TestRefreshInputOwnerPublishesExecutingToolSnapshot(t *testing.T) {
	loop := &Loop{
		state:                  StateRunning,
		toolDispatchInProgress: true,
	}
	loop.refreshInputOwner()
	snapshot, ok := loop.PublishedRuntimeSnapshot()
	if !ok {
		t.Fatal("expected published snapshot")
	}
	if snapshot.Control != ControlExecutingTool {
		t.Fatalf("control = %s, want %s", snapshot.Control, ControlExecutingTool)
	}
}

func TestAuditRuntimeStateHandlesWaitingStateInvariants(t *testing.T) {
	tests := []struct {
		name string
		loop *Loop
	}{
		{
			name: "approval without pending calls",
			loop: &Loop{state: StateWaitingApproval},
		},
		{
			name: "direction choice without prompt",
			loop: &Loop{state: StateWaitingDirectionChoice},
		},
		{
			name: "direction text with stale choice prompt",
			loop: &Loop{
				state:                  StateWaitingDirectionText,
				pendingDirectionPrompt: &directionPromptState{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.loop.output = make(chan *api.Message, 1)
			if !tt.loop.auditRuntimeState() {
				t.Fatal("expected audit to handle invalid waiting state")
			}
			if tt.loop.state != StateDone {
				t.Fatalf("state = %v, want StateDone", tt.loop.state)
			}
		})
	}
}

func TestNextDirectionsRequiredGateBlocksPlainAnswer(t *testing.T) {
	loop := &Loop{
		state: StateRunning,
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("next_directions plain-answer gate should block text")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if !strings.Contains(loop.pendingResponseDirective, "next_directions") {
		t.Fatalf("expected next_directions directive, got %q", loop.pendingResponseDirective)
	}
}

func TestNextDirectionsRequiredGateStopsAfterRepeatedPlainAnswer(t *testing.T) {
	loop := &Loop{
		state:  StateRunning,
		output: make(chan *api.Message, 1),
		pendingFinalReport: &finalReport{
			Conclusive:      false,
			Attempted:       []string{"observed deployment"},
			MostLikelyCause: "inconclusive",
			EvidenceMissing: []string{"pod events"},
		},
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("first plain answer should be handled")
	}
	if !loop.rejectPlainAnswerDuringNextDirections("I can answer now.") {
		t.Fatal("repeated plain answer should be handled")
	}
	if loop.state != StateDone {
		t.Fatalf("state = %v, want StateDone", loop.state)
	}
}
