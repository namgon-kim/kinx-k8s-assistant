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

func TestAnalyzeToolCallsAllowsKubectlReadOnlyWithGlobalFlagsBeforeVerb(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n 43e3c8fe-8674-4ccf-88e9-7084805034bb get cluster clst-pz02-shs1006-04 -o yaml",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected namespace-before-verb get to be read-only, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsBlocksKubectlMutationWithGlobalFlagsBeforeVerb(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": "kubectl -n tests delete pod app",
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource == "no" {
		t.Fatalf("expected namespace-before-verb delete to be blocked, got %q", pending[0].ModifiesResource)
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

func TestAnalyzeToolCallsAllowsReadOnlyBashCPipelineList(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -c "kubectl api-resources | grep -i tenantcontrolplane; kubectl api-resources | grep -i helmreleaseproxy"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected read-only bash -c kubectl pipelines, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsAllowsReadOnlyBashLCPipelineList(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -lc "kubectl api-resources | grep -i tenantcontrolplane; kubectl api-resources | grep -i helmreleaseproxy"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected read-only bash -lc kubectl pipelines, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsAllowsReadOnlyBashCCommandThroughKubectlToolName(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": `bash -c "kubectl api-resources | grep -i tenantcontrolplane"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource != "no" {
		t.Fatalf("expected read-only bash -c command through kubectl tool name, got %q", pending[0].ModifiesResource)
	}
}

func TestAnalyzeToolCallsBlocksReadOnlyBashCRedirection(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -c "kubectl get secret app -o yaml > /tmp/leak.yaml"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource == "no" {
		t.Fatalf("expected bash -c redirection to be blocked, got %q", pending[0].ModifiesResource)
	}
}

func TestExtractShellScriptDoesNotTreatLongFlagAsDashC(t *testing.T) {
	if script, ok := extractShellScript(`bash --rcfile "kubectl get pods"`); ok {
		t.Fatalf("did not expect --rcfile to be treated as -c, got %q", script)
	}
}

func TestAnalyzeToolCallsBlocksMutatingBashCCommand(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -c "kubectl get pods; kubectl delete pod app -n tests"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unexpected pending count: %d", len(pending))
	}
	if pending[0].ModifiesResource == "no" {
		t.Fatalf("expected mutating bash -c command to be blocked, got %q", pending[0].ModifiesResource)
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

func TestAnalyzeToolCallsBlocksKubectlApplyAfterReadOnlyPipeline(t *testing.T) {
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
		t.Fatalf("expected kubectl apply pipeline to be blocked, got %q", pending[0].ModifiesResource)
	}
}
