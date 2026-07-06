package react

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
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

func TestKubectlVerbAndIndexFromFieldsHandlesPreVerbFlagsConservatively(t *testing.T) {
	tests := []struct {
		name      string
		fields    []string
		wantVerb  string
		wantOK    bool
		wantIndex int
	}{
		{
			name:      "global namespace before verb",
			fields:    []string{"kubectl", "-n", "prod", "get", "pods"},
			wantVerb:  "get",
			wantOK:    true,
			wantIndex: 3,
		},
		{
			name:      "global context before verb",
			fields:    []string{"kubectl", "--context", "prod", "describe", "pod", "app"},
			wantVerb:  "describe",
			wantOK:    true,
			wantIndex: 3,
		},
		{
			name:      "global flag with equals before verb",
			fields:    []string{"kubectl", "--request-timeout=5s", "get", "pods"},
			wantVerb:  "get",
			wantOK:    true,
			wantIndex: 2,
		},
		{
			name:      "boolean global flag before verb",
			fields:    []string{"kubectl", "--insecure-skip-tls-verify", "get", "pods"},
			wantVerb:  "get",
			wantOK:    true,
			wantIndex: 2,
		},
		{
			name:   "command output flag before verb is unsupported",
			fields: []string{"kubectl", "-o", "json", "get", "pods"},
			wantOK: false,
		},
		{
			name:   "unknown flag before verb is unsupported",
			fields: []string{"kubectl", "--unknown", "value", "get", "pods"},
			wantOK: false,
		},
		{
			name:   "global flag missing value is unsupported",
			fields: []string{"kubectl", "--namespace"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVerb, gotIndex, gotOK := kubectlVerbAndIndexFromFields(tt.fields, 0)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (verb=%q index=%d)", gotOK, tt.wantOK, gotVerb, gotIndex)
			}
			if !tt.wantOK {
				return
			}
			if gotVerb != tt.wantVerb || gotIndex != tt.wantIndex {
				t.Fatalf("verb/index = %q/%d, want %q/%d", gotVerb, gotIndex, tt.wantVerb, tt.wantIndex)
			}
		})
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

func TestAnalyzeToolCallsBlocksReadOnlyShellEvaluation(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	tests := []string{
		`kubectl get pods $(kubectl delete pod app -n tests)`,
		"kubectl get pods `kubectl delete pod app -n tests`",
		`kubectl get pods <(kubectl get pods -o name)`,
		"kubectl get pods <<EOF\nEOF",
	}
	for _, command := range tests {
		pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": command,
			},
		}})
		if err != nil {
			t.Fatalf("analyze tool calls for %q: %v", command, err)
		}
		if len(pending) != 1 {
			t.Fatalf("unexpected pending count for %q: %d", command, len(pending))
		}
		if pending[0].ModifiesResource == "no" {
			t.Fatalf("expected shell evaluation to be blocked for %q, got %q", command, pending[0].ModifiesResource)
		}
	}
}

func TestAnalyzeToolCallsAllowsOnlySafeKubectlAuthReadOnlySubcommands(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{cfg: &config.Config{ReadOnly: true}, registry: registry}

	allowed := []string{
		"kubectl auth can-i get pods -n tests",
		"kubectl auth whoami",
	}
	for _, command := range allowed {
		pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": command,
			},
		}})
		if err != nil {
			t.Fatalf("analyze allowed auth command %q: %v", command, err)
		}
		if pending[0].ModifiesResource != "no" {
			t.Fatalf("expected auth command %q to be read-only, got %q", command, pending[0].ModifiesResource)
		}
	}

	blocked := []string{
		"kubectl auth reconcile -f role.yaml",
		"kubectl auth",
	}
	for _, command := range blocked {
		pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
			Name: "kubectl",
			Arguments: map[string]any{
				"command": command,
			},
		}})
		if err != nil {
			t.Fatalf("analyze blocked auth command %q: %v", command, err)
		}
		if pending[0].ModifiesResource == "no" {
			t.Fatalf("expected auth command %q to be blocked, got %q", command, pending[0].ModifiesResource)
		}
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

func TestRejectNonObservationShellToolCallsRetriesBeforeReadOnlyGate(t *testing.T) {
	loop := &Loop{
		state:  StateRunning,
		output: make(chan *api.Message, 1),
	}
	calls := []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `echo "Resource kinds will be fetched in the following order"`,
		},
	}}

	if !loop.rejectNonObservationShellToolCalls(calls) {
		t.Fatal("expected echo self-talk command to be rejected")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if !strings.Contains(loop.pendingResponseDirective+strings.Join(stringContent(loop.currChatContent), "\n"), "phase_progress") {
		t.Fatalf("expected correction to request phase_progress or real kubectl action, got %#v", loop.currChatContent)
	}
}

func TestRejectNonObservationShellToolCallsAllowsKubectlPipeline(t *testing.T) {
	loop := &Loop{state: StateRunning}
	calls := []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -c "kubectl get pods -A | wc -l"`,
		},
	}}
	if loop.rejectNonObservationShellToolCalls(calls) {
		t.Fatal("read-only kubectl pipeline must not be treated as self-talk")
	}
}

func TestRejectReadOnlyUnknownRetriesInsteadOfForcingFinal(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{
		cfg:      &config.Config{ReadOnly: true},
		registry: registry,
		state:    StateRunning,
		output:   make(chan *api.Message, 1),
	}
	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "bash",
		Arguments: map[string]any{
			"command": `bash -c "kubectl get secret app -o yaml > /tmp/leak.yaml"`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	loop.pendingCalls = pending
	loop.rejectReadOnlyModifyingCalls()

	text := strings.Join(stringContent(loop.currChatContent), "\n")
	if strings.Contains(text, "single final answer") {
		t.Fatalf("unknown read-only block must not force final answer, got %q", text)
	}
	if !strings.Contains(text, "Retry with one real read-only kubectl command") {
		t.Fatalf("expected retry correction, got %q", text)
	}
	result := firstFunctionCallResult(t, loop.currChatContent)
	if result["retryable"] != true {
		t.Fatalf("unknown read-only block must be agent-retryable, got %#v", result["retryable"])
	}
	if result["retry_scope"] != "agent_correct_command" {
		t.Fatalf("retry_scope = %#v, want agent_correct_command", result["retry_scope"])
	}
}

func TestRejectReadOnlyMutationIsUserRequestBlocker(t *testing.T) {
	registry, err := toolconnector.NewRegistry(context.Background(), sandbox.NewLocalExecutor(), false)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	loop := &Loop{
		cfg:      &config.Config{ReadOnly: true},
		registry: registry,
		state:    StateRunning,
		output:   make(chan *api.Message, 1),
	}
	pending, err := loop.analyzeToolCalls(context.Background(), []gollm.FunctionCall{{
		Name: "kubectl",
		Arguments: map[string]any{
			"command": `kubectl delete pod app -n tests`,
		},
	}})
	if err != nil {
		t.Fatalf("analyze tool calls: %v", err)
	}
	loop.pendingCalls = pending
	loop.rejectReadOnlyModifyingCalls()

	result := firstFunctionCallResult(t, loop.currChatContent)
	if result["retryable"] != false {
		t.Fatalf("mutation block must not be agent-retryable, got %#v", result["retryable"])
	}
	if result["retry_scope"] != "user_request_blocked_by_read_only" {
		t.Fatalf("retry_scope = %#v, want user_request_blocked_by_read_only", result["retry_scope"])
	}
	text := strings.Join(stringContent(loop.currChatContent), "\n")
	if !strings.Contains(text, "user/request blocker") {
		t.Fatalf("expected user/request blocker correction, got %q", text)
	}
}

func TestRejectInteractiveToolCallsUsesPolicyBlockOutcome(t *testing.T) {
	loop := &Loop{
		cfg:    &config.Config{},
		state:  StateRunning,
		output: make(chan *api.Message, 1),
		pendingCalls: []PendingCall{{
			FunctionCall: gollm.FunctionCall{
				ID:        "call-1",
				Name:      "bash",
				Arguments: map[string]any{"command": "read -p proceed"},
			},
			IsInteractive:    true,
			InteractiveError: fmt.Errorf("interactive command requires stdin"),
		}},
	}

	if !loop.rejectInteractiveToolCalls() {
		t.Fatal("expected interactive command to be rejected")
	}
	if loop.state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", loop.state)
	}
	if len(loop.pendingCalls) != 0 {
		t.Fatalf("pendingCalls = %#v, want cleared", loop.pendingCalls)
	}
	text := strings.Join(stringContent(loop.currChatContent), "\n")
	if !strings.Contains(text, "non-interactive command") {
		t.Fatalf("expected non-interactive correction, got %q", text)
	}
	result := firstFunctionCallResult(t, loop.currChatContent)
	if result["policy"] != "interactive_command_blocked" {
		t.Fatalf("policy = %#v, want interactive_command_blocked", result["policy"])
	}
	if result["retryable"] != true {
		t.Fatalf("retryable = %#v, want true", result["retryable"])
	}
	if result["retry_scope"] != "agent_correct_command" {
		t.Fatalf("retry_scope = %#v, want agent_correct_command", result["retry_scope"])
	}
}

func stringContent(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func firstFunctionCallResult(t *testing.T, values []any) map[string]any {
	t.Helper()
	for _, value := range values {
		if result, ok := value.(gollm.FunctionCallResult); ok {
			return result.Result
		}
	}
	t.Fatalf("no function call result in %#v", values)
	return nil
}
