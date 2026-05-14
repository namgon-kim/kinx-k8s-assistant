package react

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
)

func TestAnalyzeToolCallsMarksKubectlMutation(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl delete pod test-oom -n tests",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource == "no" {
		t.Fatalf("expected mutation to be marked as modifying, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsMarksKubectlReadOnly(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get pods -n tests",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected read-only command, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsAllowsReadOnlyPipeline(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get events --sort-by='{.metadata.creationTimestamp}' | tail -20",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected read-only pipeline command, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsBlocksMutationPipeline(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl get pod app -o yaml | kubectl apply -f -",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource == "no" {
		t.Fatalf("expected mutation pipeline to be blocked, got %q", pending[0].ModifiesResource)
	}
}
