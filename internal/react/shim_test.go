package react

import "testing"

func TestParseReActResponseWithAction(t *testing.T) {
	parsed, err := parseReActResponse(`prefix
` + "```json" + `
{
  "thought": "check pods",
  "action": {
    "name": "kubectl",
    "reason": "need pod status",
    "goal": "verify whether the pod is running",
    "command": "kubectl get pods -n tests",
    "expected_observation": "pod phase and readiness",
    "modifies_resource": "no"
  }
}
` + "```" + `
suffix`)
	if err != nil {
		t.Fatalf("parse ReAct response: %v", err)
	}
	if parsed.Thought != "check pods" {
		t.Fatalf("unexpected thought: %q", parsed.Thought)
	}
	if parsed.Action == nil {
		t.Fatal("expected action")
	}
	if parsed.Action.Name != "kubectl" {
		t.Fatalf("unexpected action name: %q", parsed.Action.Name)
	}
	if parsed.Action.Goal != "verify whether the pod is running" {
		t.Fatalf("unexpected action goal: %q", parsed.Action.Goal)
	}
}

func TestParseReActResponseRepairsUnescapedQuotesInAnswer(t *testing.T) {
	parsed, err := parseReActResponse("```json\n" + `{
  "thought": "describe 확인",
  "answer": "Normal Pulled kubelet Successfully pulled image "busybox" in 1.233s"
}` + "\n```")
	if err != nil {
		t.Fatalf("parse ReAct response with unescaped quotes: %v", err)
	}
	want := `Normal Pulled kubelet Successfully pulled image "busybox" in 1.233s`
	if parsed.Answer != want {
		t.Fatalf("unexpected answer:\n got: %q\nwant: %q", parsed.Answer, want)
	}
}

func TestParseReActResponseRepairsRawMultilineDescribeAnswer(t *testing.T) {
	parsed, err := parseReActResponse("```json\n" + `{
  "thought": "describe 확인",
  "answer": "Name:             example-nginx
Namespace:        default
Containers:
  sidecar:
    Image:         busybox
Events:
  Normal  Pulled   kubelet  Successfully pulled image \"busybox\" in 1.193s
  Normal  Started  kubelet  Container started"
}` + "\n```")
	if err != nil {
		t.Fatalf("parse ReAct response with raw multiline describe answer: %v", err)
	}
	want := "Name:             example-nginx\n" +
		"Namespace:        default\n" +
		"Containers:\n" +
		"  sidecar:\n" +
		"    Image:         busybox\n" +
		"Events:\n" +
		"  Normal  Pulled   kubelet  Successfully pulled image \"busybox\" in 1.193s\n" +
		"  Normal  Started  kubelet  Container started"
	if parsed.Answer != want {
		t.Fatalf("unexpected answer:\n got: %q\nwant: %q", parsed.Answer, want)
	}
}

func TestShimCandidateSeparatesThoughtAndAnswerWithBlankLine(t *testing.T) {
	candidate := &shimCandidate{candidate: &reActResponse{
		Thought: "default 네임스페이스의 팟 정보가 제공되었습니다.",
		Answer:  "Name: example-nginx\nNamespace: default",
	}}
	parts := candidate.Parts()
	if len(parts) != 2 {
		t.Fatalf("unexpected part count: %d", len(parts))
	}
	thought, ok := parts[0].AsText()
	if !ok {
		t.Fatal("expected thought text part")
	}
	if thought != "default 네임스페이스의 팟 정보가 제공되었습니다.\n\n" {
		t.Fatalf("unexpected thought text: %q", thought)
	}
	answer, ok := parts[1].AsText()
	if !ok {
		t.Fatal("expected answer text part")
	}
	if answer != "Name: example-nginx\nNamespace: default" {
		t.Fatalf("unexpected answer text: %q", answer)
	}
}

func TestShimPartConvertsActionToFunctionCall(t *testing.T) {
	part := &shimPart{action: &action{
		Name:                "kubectl",
		Reason:              "need pod status",
		Goal:                "verify whether the pod is running",
		Command:             "kubectl get pods -n tests",
		ExpectedObservation: "pod phase and readiness",
		ModifiesResource:    "no",
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok {
		t.Fatal("expected function call")
	}
	if len(calls) != 1 {
		t.Fatalf("unexpected call count: %d", len(calls))
	}
	if calls[0].Name != "kubectl" {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if _, exists := calls[0].Arguments["name"]; exists {
		t.Fatal("name should be removed from function call arguments")
	}
	if calls[0].Arguments["command"] != "kubectl get pods -n tests" {
		t.Fatalf("unexpected command: %#v", calls[0].Arguments["command"])
	}
	if calls[0].Arguments["goal"] != "verify whether the pod is running" {
		t.Fatalf("unexpected goal: %#v", calls[0].Arguments["goal"])
	}
}

func TestShimPartConvertsResourceGuideLookupToInternalCall(t *testing.T) {
	part := &shimPart{resourceGuideLookup: &resourceGuideLookup{
		ResourceFamily: "cluster-api",
		ProblemFocus:   "worker scale / replica availability",
		Reason:         "desired replicas exceed available replicas",
		Evidence:       "spec.replicas=3, availableReplicas=1",
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal lookup call, got %#v", calls)
	}
	if calls[0].Name != internalResourceGuideLookupCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if calls[0].Arguments["problem_focus"] != "worker scale / replica availability" {
		t.Fatalf("unexpected problem focus: %#v", calls[0].Arguments["problem_focus"])
	}
}
