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

func TestShimCandidateFinalAnswerOmitsThought(t *testing.T) {
	candidate := &shimCandidate{candidate: &reActResponse{
		Thought: "default 네임스페이스의 팟 정보가 제공되었습니다.",
		Answer:  "Name: example-nginx\nNamespace: default",
	}}
	parts := candidate.Parts()
	if len(parts) != 1 {
		t.Fatalf("unexpected part count: %d", len(parts))
	}
	answer, ok := parts[0].AsText()
	if !ok {
		t.Fatal("expected answer text part")
	}
	if answer != "Name: example-nginx\nNamespace: default" {
		t.Fatalf("unexpected answer text: %q", answer)
	}
}

func TestParseReActResponseMovesTopLevelGuideProgressIntoAction(t *testing.T) {
	parsed, err := parseReActResponse("```json\n" + `{
  "thought": "inspect",
  "guide_progress": {
    "step_completed": 1,
    "evidence_useful": true
  },
  "action": {
    "name": "kubectl",
    "reason": "inspect guide step",
    "target": {
      "resource": "cluster",
      "namespace": "tenant-a",
      "name": "clst-a"
    },
    "command": "kubectl -n tenant-a get cluster/clst-a -o yaml",
    "modifies_resource": "no"
  }
}` + "\n```")
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if parsed.Action == nil || parsed.Action.GuideProgress == nil {
		t.Fatalf("expected action guide progress, got %#v", parsed.Action)
	}
	if parsed.Action.GuideProgress.StepCompleted != 1 || !parsed.Action.GuideProgress.EvidenceUseful {
		t.Fatalf("unexpected guide progress: %#v", parsed.Action.GuideProgress)
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

func TestParseReActResponseRejectsPlainText(t *testing.T) {
	parsed, err := parseReActResponse("plain final answer")
	if err == nil {
		t.Fatal("expected plain text shim response to be rejected")
	}
	if parsed != nil {
		t.Fatalf("expected nil parsed response, got %#v", parsed)
	}
}

func TestParseReActResponseTreatsStringFinalReportAsInvalidCall(t *testing.T) {
	parsed, err := parseReActResponse("```json\n" + `{
  "thought": "done",
  "final_report": "The cluster has a problem."
}` + "\n```")
	if err != nil {
		t.Fatalf("parse invalid final_report shape: %v", err)
	}
	if !parsed.InvalidFinalReport {
		t.Fatal("expected invalid final_report marker")
	}
	parts := (&shimCandidate{candidate: parsed}).Parts()
	if len(parts) != 2 {
		t.Fatalf("unexpected part count: %d", len(parts))
	}
	calls, ok := parts[1].AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected invalid final_report function call, got %#v", calls)
	}
	if calls[0].Name != internalFinalReportCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if len(calls[0].Arguments) != 0 {
		t.Fatalf("expected empty arguments for correction path, got %#v", calls[0].Arguments)
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

func TestShimPartConvertsPhasePlanToInternalCall(t *testing.T) {
	part := &shimPart{phasePlan: &phasePlan{
		RequestGoal:       "diagnose cluster health",
		CurrentPhaseIndex: 1,
		PhaseSteps: []phaseStep{{
			Index:               1,
			Name:                "observation_planning",
			Goal:                "choose first observation",
			CompletionCondition: "next observation action is selected",
			AllowedNext:         []string{"observation_execution"},
		}},
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal phase-plan call, got %#v", calls)
	}
	if calls[0].Name != internalPhasePlanCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if calls[0].Arguments["request_goal"] != "diagnose cluster health" {
		t.Fatalf("unexpected request goal: %#v", calls[0].Arguments["request_goal"])
	}
}

func TestShimPartConvertsPhaseProgressToInternalCall(t *testing.T) {
	part := &shimPart{phaseProgress: &phaseProgress{
		PhaseCompleted:   2,
		EvidenceUseful:   true,
		CompletionReason: "cluster status was observed",
		NextPhase:        "observation_completion",
	}}

	calls, ok := part.AsFunctionCalls()
	if !ok || len(calls) != 1 {
		t.Fatalf("expected one internal phase-progress call, got %#v", calls)
	}
	if calls[0].Name != internalPhaseProgressCall {
		t.Fatalf("unexpected call name: %q", calls[0].Name)
	}
	if calls[0].Arguments["phase_completed"] != float64(2) {
		t.Fatalf("unexpected completed phase: %#v", calls[0].Arguments["phase_completed"])
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
		OperationalFocus: &requirementOperationalFocus{
			Summary:               "worker group availability",
			RelationshipToPrimary: "related_to_primary",
			ChangedFromPrevious:   true,
			RelatedResourceHints: []requirementRelatedResource{{
				Kind:   "machinedeployment",
				Role:   "suspected_related",
				Source: "previous_context",
			}},
			EvidenceNeeds: []string{"MachineDeployment status"},
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
	focus, ok := calls[0].Arguments["operational_focus"].(map[string]any)
	if !ok {
		t.Fatalf("expected operational_focus argument, got %#v", calls[0].Arguments["operational_focus"])
	}
	if focus["summary"] != "worker group availability" {
		t.Fatalf("unexpected operational focus: %#v", focus)
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
