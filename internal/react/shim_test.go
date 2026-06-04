package react

import (
	"testing"
)

func TestParseReActResponseWithAction(t *testing.T) {
	parsed, err := parseReActResponse(`prefix
` + "```json" + `
{
  "thought": "check pods",
  "action": {
    "name": "kubectl",
    "reason": "need pod status",
    "goal": "verify whether the pod is running",
    "target": {
      "resource": "pods",
      "namespace": "tests",
      "name": "app"
    },
    "command": "kubectl get pods app -n tests",
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
	if parsed.Action.Target == nil || parsed.Action.Target.Namespace != "tests" {
		t.Fatalf("unexpected action target: %#v", parsed.Action.Target)
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
		Target:              &actionTarget{Resource: "pods", Namespace: "tests", Name: "app"},
		Command:             "kubectl get pods app -n tests",
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
	if calls[0].Arguments["command"] != "kubectl get pods app -n tests" {
		t.Fatalf("unexpected command: %#v", calls[0].Arguments["command"])
	}
	if calls[0].Arguments["goal"] != "verify whether the pod is running" {
		t.Fatalf("unexpected goal: %#v", calls[0].Arguments["goal"])
	}
}

func TestParseReActResponseTreatsPlainTextAsFinalAnswer(t *testing.T) {
	parsed, err := parseReActResponse("plain final answer")
	if err != nil {
		t.Fatalf("parse plain final answer: %v", err)
	}
	if parsed.Answer != "plain final answer" {
		t.Fatalf("unexpected answer: %q", parsed.Answer)
	}
	if parsed.Action != nil {
		t.Fatalf("did not expect action: %#v", parsed.Action)
	}
}

func TestShimPartConvertsResourceGuideLookupToInternalCall(t *testing.T) {
	part := &shimPart{resourceGuideLookup: &resourceGuideLookup{
		ResourceFamily: "cluster-api",
		ProblemFocus:   "nodegroup reconciliation",
		Reason:         "live evidence requires a more specific guide than the initial resource-family guide",
		Evidence:       "controller reported nodegroup reconciliation is blocked",
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal lookup call, got %#v", calls)
	}
	if calls[0].Name != internalResourceGuideLookupCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if calls[0].Arguments["problem_focus"] != "nodegroup reconciliation" {
		t.Fatalf("unexpected problem focus: %#v", calls[0].Arguments["problem_focus"])
	}
}

func TestShimPartConvertsRequirementAnalysisToInternalCall(t *testing.T) {
	part := &shimPart{requirementAnalysis: &requirementAnalysis{
		RequestType: "diagnosis",
		Action:      "diagnose_problem",
		Target: requirementAnalysisTarget{
			Category:    "cluster",
			Description: "connected Kubernetes cluster",
		},
		Evidence: []string{"current cluster health evidence"},
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal requirement-analysis call, got %#v", calls)
	}
	if calls[0].Name != internalRequirementAnalysisCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if calls[0].Arguments["request_type"] != "diagnosis" {
		t.Fatalf("unexpected request type: %#v", calls[0].Arguments["request_type"])
	}
}

func TestShimPartConvertsRequestContextToInternalCall(t *testing.T) {
	part := &shimPart{requestContext: &requestContext{
		PrimaryTarget: requestPrimaryTarget{Resource: "cluster", Name: "cluster-a"},
		Scope:         requestScope{Namespace: "tenant-a"},
		ResourceClass: "unknown",
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal request-context call, got %#v", calls)
	}
	if calls[0].Name != internalRequestContextCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
}
