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
    "command": "kubectl get pods -n tests",
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
		Name:             "kubectl",
		Reason:           "need pod status",
		Command:          "kubectl get pods -n tests",
		ModifiesResource: "no",
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
}
