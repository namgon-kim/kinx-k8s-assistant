package coordinator

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/toolconnector"
)

func TestDeterministicLoopHarnessRunsNativeAndShimToolTurns(t *testing.T) {
	for _, shim := range []bool{false, true} {
		name := "native"
		if shim {
			name = "shim"
		}
		t.Run(name, func(t *testing.T) {
			harness := newDeterministicLoopHarness(t, shim)
			snapshot, err := harness.RunTurn(context.Background())
			if err != nil {
				t.Fatalf("run deterministic turn: %v", err)
			}
			if got := harness.executor.CallCount(); got != 1 {
				t.Fatalf("tool executions = %d, want 1", got)
			}
			if got := harness.chat.SendCount(); got != 1 {
				t.Fatalf("model sends = %d, want 1", got)
			}
			if snapshot.Control != RuntimeControlAwaitingModelStep {
				t.Fatalf("control = %s, want %s", snapshot.Control, RuntimeControlAwaitingModelStep)
			}
			if got := len(harness.loop.mutableRuntime().execution.OrderedAttempts()); got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
		})
	}
}

func TestDeterministicLoopHarnessPreservesAcceptedLightweightPlan(t *testing.T) {
	tests := []struct {
		name             string
		mismatchedTarget bool
		result           *sandbox.ExecResult
		wantToolCalls    int
		wantAttempts     int
	}{
		{
			name:          "success",
			result:        &sandbox.ExecResult{Stdout: "web-0\n", ExitCode: 0},
			wantToolCalls: 1,
			wantAttempts:  1,
		},
		{
			name:             "action pre-validation failure",
			mismatchedTarget: true,
			result:           &sandbox.ExecResult{Stdout: "must not execute", ExitCode: 0},
		},
		{
			name:          "tool failure",
			result:        &sandbox.ExecResult{Stderr: "unknown flag: --bad", ExitCode: 1},
			wantToolCalls: 1,
			wantAttempts:  1,
		},
	}
	for _, shim := range []bool{false, true} {
		mode := "native"
		if shim {
			mode = "shim"
		}
		for _, tt := range tests {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				harness := newDeterministicLoopHarness(t, shim)
				harness.chat.responses = []gollm.ChatResponse{scriptedLightweightBundleResponse(shim, tt.mismatchedTarget)}
				harness.executor.results = []*sandbox.ExecResult{tt.result}
				harness.loop.mutableRuntime().requirementAnalysis = &requirementAnalysis{
					RequestType: "lookup",
					Action:      "count_pods",
					Target: requirementAnalysisTarget{
						Category:    "kubernetes_resource",
						Description: "pods",
					},
					Scope:     requirementScope{Type: "namespaced", Namespace: "default"},
					Resources: []requirementResource{{Kind: "pods"}},
				}
				harness.loop.mutableRuntime().phaseStepState = nil
				harness.loop.transitionControl(RuntimeControlAwaitingPhasePlan)

				if _, err := harness.RunTurn(context.Background()); err != nil {
					t.Fatalf("run lightweight bundle: %v", err)
				}
				if harness.loop.mutableRuntime().phaseStepState == nil || harness.loop.mutableRuntime().phaseStepState.currentStep().Name != lightweightLookupPhase {
					t.Fatalf("accepted lightweight plan was not retained: %#v", harness.loop.mutableRuntime().phaseStepState)
				}
				if got := harness.executor.CallCount(); got != tt.wantToolCalls {
					t.Fatalf("tool executions = %d, want %d", got, tt.wantToolCalls)
				}
				if got := len(harness.loop.mutableRuntime().execution.OrderedAttempts()); got != tt.wantAttempts {
					t.Fatalf("attempt records = %d, want %d", got, tt.wantAttempts)
				}
			})
		}
	}
}

type deterministicLoopHarness struct {
	t        *testing.T
	loop     *Loop
	chat     *scriptedChat
	executor *recordingExecutor
	turns    int
	maxTurns int
}

func newDeterministicLoopHarness(t *testing.T, shim bool) *deterministicLoopHarness {
	t.Helper()
	executor := &recordingExecutor{results: []*sandbox.ExecResult{{
		Stdout:   "NAME  READY  STATUS\nweb-0 1/1 Running\n",
		ExitCode: 0,
	}}}
	chat := &scriptedChat{responses: []gollm.ChatResponse{scriptedToolResponse(shim)}}
	client := &scriptedClient{chat: chat}
	registryFactory := func(_ context.Context, injected sandbox.Executor, _ *config.Config) (*toolconnector.Registry, error) {
		registry := &toolconnector.Registry{}
		registry.Tools.Init()
		registry.Tools.RegisterTool(tools.NewKubectlTool(injected))
		return registry, nil
	}
	cfg := &config.Config{
		Model:             "scripted-model",
		MaxIterations:     4,
		EnableToolUseShim: shim,
		ReadOnly:          true,
		SessionBackend:    "memory",
		Lang:              config.LangConfig{Language: "English"},
	}
	loop, err := newLoopWithDependencies(cfg, loopDependencies{
		modelClient:  func(*config.Config) (gollm.Client, error) { return client, nil },
		executor:     func() sandbox.Executor { return executor },
		toolRegistry: registryFactory,
	})
	if err != nil {
		t.Fatalf("create loop with test dependencies: %v", err)
	}
	loop.executor = loop.deps.executor()
	loop.registry, err = loop.deps.toolRegistry(context.Background(), loop.executor, cfg)
	if err != nil {
		t.Fatalf("create test registry: %v", err)
	}
	loop.chat = client.StartChat("test system prompt", cfg.Model)
	loop.workDir = t.TempDir()
	loop.mutableRuntime().originalQuery = "check pods in default"
	loop.mutableRuntime().requirementAnalysis = &requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "inspect",
		Target: requirementAnalysisTarget{
			Category:    "kubernetes_resource",
			Description: "pods",
		},
		Scope:     requirementScope{Type: "namespaced", Namespace: "default"},
		Resources: []requirementResource{{Kind: "pods"}},
	}
	loop.mutableRuntime().requestContext = &requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "pods"},
		Scope:         requestScope{Namespace: "default"},
	}
	plan := phasePlan{
		RequestGoal:       "observe pod health",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{{
			Index:               1,
			Name:                "initial_diagnosis",
			Goal:                "observe pod health",
			CompletionCondition: "pod state observed",
		}},
	}
	loop.mutableRuntime().phaseStepState = &phaseStepState{
		RequestGoal:       plan.RequestGoal,
		CurrentPhaseIndex: plan.CurrentPhaseIndex,
		PhaseSteps:        append([]phaseStep(nil), plan.PhaseSteps...),
		Completed:         map[int]bool{},
	}
	loop.acceptGoalExecutionPlan(plan)
	loop.transitionControl(RuntimeControlAwaitingModelStep)
	loop.mutableRuntime().currChatContent = []any{"continue the active diagnostic step"}
	return &deterministicLoopHarness{
		t:        t,
		loop:     loop,
		chat:     chat,
		executor: executor,
		maxTurns: 2,
	}
}

func (h *deterministicLoopHarness) RunTurn(ctx context.Context) (RuntimeSnapshot, error) {
	h.t.Helper()
	if h.turns >= h.maxTurns {
		return RuntimeSnapshot{}, fmt.Errorf("deterministic harness exceeded max turns: %d", h.maxTurns)
	}
	h.turns++
	if err := h.loop.runIteration(ctx); err != nil {
		return RuntimeSnapshot{}, err
	}
	return h.loop.RuntimeSnapshot(), nil
}

func scriptedToolResponse(shim bool) gollm.ChatResponse {
	call := gollm.FunctionCall{
		ID:   "call-1",
		Name: "kubectl",
		Arguments: map[string]any{
			"reason":               "observe pod status",
			"goal":                 "observe pod health",
			"command":              "kubectl get pods -n default",
			"expected_observation": "pod readiness and phase",
			"modifies_resource":    "no",
			"risk":                 map[string]any{"risky": false},
			"target": map[string]any{
				"resource":  "pods",
				"namespace": "default",
			},
		},
	}
	if shim {
		return scriptedResponse{candidates: []gollm.Candidate{scriptedCandidate{parts: []gollm.Part{
			scriptedPart{text: "```json\n{\n  \"thought\": \"observe pods\",\n  \"action\": {\n    \"name\": \"kubectl\",\n    \"reason\": \"observe pod status\",\n    \"goal\": \"observe pod health\",\n    \"target\": {\"resource\": \"pods\", \"namespace\": \"default\"},\n    \"command\": \"kubectl get pods -n default\",\n    \"expected_observation\": \"pod readiness and phase\",\n    \"modifies_resource\": \"no\",\n    \"risk\": {\"risky\": false}\n  }\n}\n```"},
		}}}}
	}
	return scriptedResponse{candidates: []gollm.Candidate{scriptedCandidate{parts: []gollm.Part{
		scriptedPart{calls: []gollm.FunctionCall{call}},
	}}}}
}

func scriptedLightweightBundleResponse(shim, mismatchedTarget bool) gollm.ChatResponse {
	targetResource := "pods"
	if mismatchedTarget {
		targetResource = "deployments"
	}
	plan := gollm.FunctionCall{
		ID:   "plan-1",
		Name: protocol.PhasePlanCall,
		Arguments: map[string]any{
			"request_goal":        "count pods",
			"current_phase_index": 1,
			"phase_steps": []any{map[string]any{
				"index":                1,
				"name":                 lightweightLookupPhase,
				"goal":                 "count pods",
				"completion_condition": "successful observation received",
			}},
		},
	}
	action := gollm.FunctionCall{
		ID:   "call-1",
		Name: "kubectl",
		Arguments: map[string]any{
			"reason":               "count pods",
			"goal":                 "count pods",
			"command":              "kubectl get pods -n default --no-headers",
			"expected_observation": "one line per pod",
			"modifies_resource":    "no",
			"risk":                 map[string]any{"risky": false},
			"target": map[string]any{
				"resource":  targetResource,
				"namespace": "default",
			},
		},
	}
	if !shim {
		return scriptedResponse{candidates: []gollm.Candidate{scriptedCandidate{parts: []gollm.Part{
			scriptedPart{calls: []gollm.FunctionCall{plan, action}},
		}}}}
	}
	text := fmt.Sprintf("```json\n{\n  \"thought\": \"count pods\",\n  \"phase_plan\": {\"request_goal\": \"count pods\", \"current_phase_index\": 1, \"phase_steps\": [{\"index\": 1, \"name\": \"lightweight_lookup\", \"goal\": \"count pods\", \"completion_condition\": \"successful observation received\"}]},\n  \"action\": {\"name\": \"kubectl\", \"reason\": \"count pods\", \"goal\": \"count pods\", \"target\": {\"resource\": %q, \"namespace\": \"default\"}, \"command\": \"kubectl get pods -n default --no-headers\", \"expected_observation\": \"one line per pod\", \"modifies_resource\": \"no\", \"risk\": {\"risky\": false}}\n}\n```", targetResource)
	return scriptedResponse{candidates: []gollm.Candidate{scriptedCandidate{parts: []gollm.Part{
		scriptedPart{text: text},
	}}}}
}

type scriptedClient struct {
	chat *scriptedChat
}

func (c *scriptedClient) StartChat(string, string) gollm.Chat { return c.chat }
func (*scriptedClient) GenerateCompletion(context.Context, *gollm.CompletionRequest) (gollm.CompletionResponse, error) {
	return nil, fmt.Errorf("completion is not configured in deterministic harness")
}
func (*scriptedClient) SetResponseSchema(*gollm.Schema) error { return nil }
func (*scriptedClient) ListModels(context.Context) ([]string, error) {
	return []string{"scripted-model"}, nil
}
func (*scriptedClient) Close() error { return nil }

type scriptedChat struct {
	mu          sync.Mutex
	responses   []gollm.ChatResponse
	sendCount   int
	definitions []*gollm.FunctionDefinition
}

func (c *scriptedChat) Send(ctx context.Context, contents ...any) (gollm.ChatResponse, error) {
	iterator, err := c.SendStreaming(ctx, contents...)
	if err != nil {
		return nil, err
	}
	for response, streamErr := range iterator {
		return response, streamErr
	}
	return nil, nil
}

func (c *scriptedChat) SendStreaming(context.Context, ...any) (gollm.ChatResponseIterator, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.responses) == 0 {
		return nil, fmt.Errorf("scripted model has no remaining response")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	c.sendCount++
	return gollm.ChatResponseIterator(func(yield func(gollm.ChatResponse, error) bool) {
		yield(response, nil)
	}), nil
}

func (c *scriptedChat) SetFunctionDefinitions(definitions []*gollm.FunctionDefinition) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.definitions = append([]*gollm.FunctionDefinition(nil), definitions...)
	return nil
}
func (*scriptedChat) IsRetryableError(error) bool     { return false }
func (*scriptedChat) Initialize([]*api.Message) error { return nil }

func (c *scriptedChat) SendCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendCount
}

type scriptedResponse struct {
	candidates []gollm.Candidate
}

func (r scriptedResponse) UsageMetadata() any            { return nil }
func (r scriptedResponse) Candidates() []gollm.Candidate { return r.candidates }

type scriptedCandidate struct {
	parts []gollm.Part
}

func (c scriptedCandidate) String() string      { return "scripted candidate" }
func (c scriptedCandidate) Parts() []gollm.Part { return c.parts }

type scriptedPart struct {
	text  string
	calls []gollm.FunctionCall
}

func (p scriptedPart) AsText() (string, bool) {
	return p.text, p.text != ""
}

func (p scriptedPart) AsFunctionCalls() ([]gollm.FunctionCall, bool) {
	return p.calls, len(p.calls) != 0
}

type recordingExecutor struct {
	mu      sync.Mutex
	results []*sandbox.ExecResult
	calls   []string
}

func (e *recordingExecutor) Execute(_ context.Context, command string, _ []string, _ string) (*sandbox.ExecResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, command)
	if len(e.results) == 0 {
		return nil, fmt.Errorf("fake executor has no remaining result")
	}
	result := *e.results[0]
	e.results = e.results[1:]
	result.Command = command
	return &result, nil
}

func (*recordingExecutor) Close(context.Context) error { return nil }

func (e *recordingExecutor) CallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}
