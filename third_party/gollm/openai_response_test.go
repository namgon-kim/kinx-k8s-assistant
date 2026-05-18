package gollm

import (
	"testing"

	"github.com/openai/openai-go/responses"
)

func TestConvertResponseToolCallToFunctionCall(t *testing.T) {
	t.Run("valid arguments", func(t *testing.T) {
		got, err := convertResponseToolCallToFunctionCall(responses.ResponseFunctionToolCall{
			CallID:    "call-1",
			Name:      "kubectl",
			Arguments: `{"command":"kubectl get pods"}`,
		})
		if err != nil {
			t.Fatalf("convert response tool call: %v", err)
		}
		if got.ID != "call-1" || got.Name != "kubectl" {
			t.Fatalf("unexpected function call: %#v", got)
		}
		if got.Arguments["command"] != "kubectl get pods" {
			t.Fatalf("unexpected arguments: %#v", got.Arguments)
		}
	})

	t.Run("empty arguments", func(t *testing.T) {
		got, err := convertResponseToolCallToFunctionCall(responses.ResponseFunctionToolCall{
			CallID: "call-2",
			Name:   "kubectl",
		})
		if err != nil {
			t.Fatalf("convert response tool call: %v", err)
		}
		if len(got.Arguments) != 0 {
			t.Fatalf("expected empty arguments, got %#v", got.Arguments)
		}
	})

	t.Run("invalid json becomes empty arguments", func(t *testing.T) {
		got, err := convertResponseToolCallToFunctionCall(responses.ResponseFunctionToolCall{
			CallID:    "call-3",
			Name:      "kubectl",
			Arguments: `{`,
		})
		if err != nil {
			t.Fatalf("convert response tool call: %v", err)
		}
		if len(got.Arguments) != 0 {
			t.Fatalf("expected empty arguments, got %#v", got.Arguments)
		}
	})
}
