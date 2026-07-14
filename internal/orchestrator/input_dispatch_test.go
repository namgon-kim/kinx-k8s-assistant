package orchestrator

import (
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react"
)

func TestDecideOrchestratorInput(t *testing.T) {
	tests := []struct {
		name     string
		control  react.RuntimeControlState
		input    string
		accepted bool
		handler  react.InputHandlerKind
	}{
		{
			name:     "user query accepts slash meta",
			control:  react.RuntimeControlAwaitingUserQuery,
			input:    "/readonly status",
			accepted: true,
			handler:  react.InputHandlerOrchestratorMeta,
		},
		{
			name:     "user query accepts free text",
			control:  react.RuntimeControlAwaitingUserQuery,
			input:    "pods가 많은 node 알려줘",
			accepted: true,
			handler:  react.InputHandlerUserQuery,
		},
		{
			name:     "continuation text sends slash meta to orchestrator",
			control:  react.RuntimeControlAwaitingContinuationText,
			input:    "/help",
			accepted: true,
			handler:  react.InputHandlerOrchestratorMeta,
		},
		{
			name:     "continuation text sends free text to react",
			control:  react.RuntimeControlAwaitingContinuationText,
			input:    "네임스페이스가 달라",
			accepted: true,
			handler:  react.InputHandlerReactText,
		},
		{
			name:     "continuation choice rejects slash meta",
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
			handler:  react.InputHandlerNone,
		},
		{
			name:     "continuation choice accepts number",
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
			handler:  react.InputHandlerReactChoice,
		},
		{
			name:     "approval rejects slash meta",
			control:  react.RuntimeControlAwaitingApproval,
			input:    "/readonly status",
			accepted: false,
			handler:  react.InputHandlerNone,
		},
		{
			name:     "approval control can classify yes token",
			control:  react.RuntimeControlAwaitingApproval,
			input:    "y",
			accepted: true,
			handler:  react.InputHandlerReactApproval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := decideOrchestratorInput(tt.control, tt.input)
			if decision.Accepted != tt.accepted || decision.Handler != tt.handler {
				t.Fatalf("decision = %#v, want accepted=%v handler=%s", decision, tt.accepted, tt.handler)
			}
		})
	}
}

func TestChoiceInputAcceptedFollowsPresentedInputMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     choiceInputKind
		control  react.RuntimeControlState
		input    string
		accepted bool
	}{
		{
			name:     "number mode accepts number",
			mode:     choiceInputNumber,
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
		},
		{
			name:     "number mode rejects yes token even for approval control",
			mode:     choiceInputNumber,
			control:  react.RuntimeControlAwaitingApproval,
			input:    "y",
			accepted: false,
		},
		{
			name:     "yes-no mode accepts yes token",
			mode:     choiceInputYesNo,
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "y",
			accepted: true,
		},
		{
			name:     "yes-no mode rejects number",
			mode:     choiceInputYesNo,
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "1",
			accepted: false,
		},
		{
			name:     "yes-no mode rejects slash meta",
			mode:     choiceInputYesNo,
			control:  react.RuntimeControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputKind := react.ClassifyUserInput(tt.input)
			decision := decideOrchestratorInput(tt.control, tt.input)
			if got := choiceInputAccepted(tt.mode, inputKind, decision); got != tt.accepted {
				t.Fatalf("accepted = %v, want %v; decision=%#v inputKind=%s", got, tt.accepted, decision, inputKind)
			}
		})
	}
}

func TestChoiceInputMode(t *testing.T) {
	if got := choiceInputMode(nil); got != choiceInputNumber {
		t.Fatalf("nil mode = %s, want %s", got, choiceInputNumber)
	}
	if got := choiceInputMode(&api.UserChoiceRequest{Prompt: "추가 조사할까요? (y/n)"}); got != choiceInputYesNo {
		t.Fatalf("mode = %s, want %s", got, choiceInputYesNo)
	}
	if got := choiceInputMode(&api.UserChoiceRequest{Prompt: "진단을 어떻게 계속할지 선택해 주세요."}); got != choiceInputNumber {
		t.Fatalf("mode = %s, want %s", got, choiceInputNumber)
	}
}
