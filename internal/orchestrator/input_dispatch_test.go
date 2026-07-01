package orchestrator

import (
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react"
)

func TestDecideOrchestratorInput(t *testing.T) {
	tests := []struct {
		name     string
		control  react.ControlState
		input    string
		accepted bool
		handler  react.InputHandlerKind
	}{
		{
			name:     "user query accepts slash meta",
			control:  react.ControlAwaitingUserQuery,
			input:    "/readonly status",
			accepted: true,
			handler:  react.InputHandlerOrchestratorMeta,
		},
		{
			name:     "user query accepts free text",
			control:  react.ControlAwaitingUserQuery,
			input:    "pods가 많은 node 알려줘",
			accepted: true,
			handler:  react.InputHandlerUserQuery,
		},
		{
			name:     "continuation text sends slash meta to orchestrator",
			control:  react.ControlAwaitingContinuationText,
			input:    "/help",
			accepted: true,
			handler:  react.InputHandlerOrchestratorMeta,
		},
		{
			name:     "continuation text sends free text to react",
			control:  react.ControlAwaitingContinuationText,
			input:    "네임스페이스가 달라",
			accepted: true,
			handler:  react.InputHandlerReactText,
		},
		{
			name:     "continuation choice rejects slash meta",
			control:  react.ControlAwaitingContinuationChoice,
			input:    "/help",
			accepted: false,
			handler:  react.InputHandlerNone,
		},
		{
			name:     "continuation choice accepts number",
			control:  react.ControlAwaitingContinuationChoice,
			input:    "2",
			accepted: true,
			handler:  react.InputHandlerReactChoice,
		},
		{
			name:     "approval rejects slash meta",
			control:  react.ControlAwaitingApproval,
			input:    "/readonly status",
			accepted: false,
			handler:  react.InputHandlerNone,
		},
		{
			name:     "approval accepts yes token",
			control:  react.ControlAwaitingApproval,
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
