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

func TestParseReActResponseRepairsRawNewlinesInAnswer(t *testing.T) {
	parsed, err := parseReActResponse("```json\n" + `{
  "thought": "describe 확인",
  "answer": "Name: test-oom
Namespace: tests
Command:
  python
  -c
  import time; time.sleep(3600)"
}` + "\n```")
	if err != nil {
		t.Fatalf("parse ReAct response with raw newlines: %v", err)
	}
	want := "Name: test-oom\nNamespace: tests\nCommand:\n  python\n  -c\n  import time; time.sleep(3600)"
	if parsed.Answer != want {
		t.Fatalf("unexpected answer:\n got: %q\nwant: %q", parsed.Answer, want)
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
