package orchestrator

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react"
)

func TestMetaCommandFilterKubePrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "kube matches both kube commands",
			in:   "/kube",
			want: []string{"/kubeconfig", "/kube-context"},
		},
		{
			name: "kubec narrows to kubeconfig",
			in:   "/kubec",
			want: []string{"/kubeconfig"},
		},
		{
			name: "kube matches both again after deleting c",
			in:   "/kube",
			want: []string{"/kubeconfig", "/kube-context"},
		},
		{
			name: "readonly matches readonly command",
			in:   "/read",
			want: []string{"/readonly"},
		},
		{
			name: "lang matches language command",
			in:   "/la",
			want: []string{"/lang"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterMetaCommands(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len(filterMetaCommands(%q)) = %d, want %d", tt.in, len(got), len(tt.want))
			}
			for i, cmd := range got {
				if cmd.Name != tt.want[i] {
					t.Fatalf("filterMetaCommands(%q)[%d] = %q, want %q", tt.in, i, cmd.Name, tt.want[i])
				}
			}
		})
	}
}

func TestInputModelViewKubePrefixTransitions(t *testing.T) {
	m := newInputModel("[test|✓] >>> ", "")

	m.textinput.SetValue("/kube")
	m = updateInputModelForTest(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m.textinput.SetValue("/kube")
	view := m.View()
	assertContains(t, view, "> /kubeconfig")
	assertContains(t, view, "  /kube-context")

	m.textinput.SetValue("/kubec")
	m = updateInputModelForTest(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m.textinput.SetValue("/kubec")
	view = m.View()
	assertContains(t, view, "> /kubeconfig")
	assertNotContains(t, view, "/kube-context")

	m.textinput.SetValue("/kube")
	m = updateInputModelForTest(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m.textinput.SetValue("/kube")
	view = m.View()
	assertContains(t, view, "> /kubeconfig")
	assertContains(t, view, "  /kube-context")
}

func TestInputModelCtrlCInterrupts(t *testing.T) {
	m := newInputModel("[test|✓] >>> ", "")
	m = updateInputModelForTest(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.interrupted {
		t.Fatal("Ctrl+C should mark input model as interrupted")
	}
}

func TestInputModelQuitIsPlainInput(t *testing.T) {
	m := newInputModel("[test|✓] >>> ", "")
	m.textinput.SetValue("quit")
	m = updateInputModelForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.interrupted {
		t.Fatal("quit should not interrupt the input model")
	}
	if m.result != "quit" {
		t.Fatalf("result = %q, want %q", m.result, "quit")
	}
}

func TestSetReadOnlyTogglesConfig(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}

	if err := o.setReadOnly("on"); err != nil {
		t.Fatalf("setReadOnly(on): %v", err)
	}
	if !o.cfg.ReadOnly {
		t.Fatal("ReadOnly should be enabled")
	}

	if err := o.setReadOnly("off"); err != nil {
		t.Fatalf("setReadOnly(off): %v", err)
	}
	if o.cfg.ReadOnly {
		t.Fatal("ReadOnly should be disabled")
	}
}

func TestSetReadOnlyRejectsInvalidValue(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}

	if err := o.setReadOnly("maybe"); err == nil {
		t.Fatal("expected invalid readonly value to fail")
	}
}

func TestSetLangTogglesConfig(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}

	if err := o.setLang("Korean"); err != nil {
		t.Fatalf("setLang(Korean): %v", err)
	}
	if o.cfg.Lang.Language != "Korean" {
		t.Fatalf("Lang.Language = %q, want Korean", o.cfg.Lang.Language)
	}

	if err := o.setLang("English"); err != nil {
		t.Fatalf("setLang(English): %v", err)
	}
	if o.cfg.Lang.Language != "English" {
		t.Fatalf("Lang.Language = %q, want English", o.cfg.Lang.Language)
	}
}

func TestSetLangRejectsInvalidValue(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}

	if err := o.setLang("Japanese"); err == nil {
		t.Fatal("expected invalid lang value to fail")
	}
}

func TestLooksIncidentGuidanceWorthyIgnoresGenericUserProblemRequest(t *testing.T) {
	if looksIncidentGuidanceWorthy("tests 네임스페이스의 pods의 문제를 해결해줘") {
		t.Fatal("generic user problem request should not trigger incident guidance offer")
	}
}

func TestLooksIncidentGuidanceWorthyDetectsConcreteKubernetesFailure(t *testing.T) {
	cases := []string{
		"pod test-oom is in CrashLoopBackOff",
		"Last State: Terminated Reason: OOMKilled",
		"Back-off restarting failed container",
	}
	for _, tc := range cases {
		if !looksIncidentGuidanceWorthy(tc) {
			t.Fatalf("expected concrete failure to trigger incident guidance: %q", tc)
		}
	}
}

func TestShouldOfferIncidentGuidanceForQuerySkipsReadOnlyRequests(t *testing.T) {
	cases := []string{
		"pods을 보여줘",
		"default 네임스페이스의 pods 목록 조회",
		"pod 상세 정보를 describe 형식으로 출력해줘",
		"최근 이벤트 로그를 보고 어떤 문제가 있었는지 요약해줘",
	}
	for _, tc := range cases {
		if shouldOfferIncidentGuidanceForQuery(tc) {
			t.Fatalf("read-only query should not offer incident guidance: %q", tc)
		}
	}
}

func TestShouldOfferIncidentGuidanceForQueryAllowsDiagnosticRequests(t *testing.T) {
	cases := []string{
		"tests 네임스페이스의 pods 문제를 해결해줘",
		"OOMKilled 원인을 분석해줘",
		"CrashLoopBackOff debug 해줘",
	}
	for _, tc := range cases {
		if !shouldOfferIncidentGuidanceForQuery(tc) {
			t.Fatalf("diagnostic query should offer incident guidance: %q", tc)
		}
	}
}

func TestDetectTypesMapsConcreteIncidentSignals(t *testing.T) {
	cases := []struct {
		text string
		want diagnostic.DetectionType
	}{
		{text: "0/3 nodes are available: FailedScheduling", want: diagnostic.DetectionFailedScheduling},
		{text: "Node node-a is NotReady", want: diagnostic.DetectionType("NodeNotReady")},
		{text: "CreateContainerConfigError because ConfigMap is missing", want: diagnostic.DetectionConfigError},
		{text: "Error from server (Forbidden)", want: diagnostic.DetectionPermissionDenied},
	}
	for _, tc := range cases {
		if !detectionTypesContain(detectTypes(tc.text), tc.want) {
			t.Fatalf("detectTypes(%q) missing %q", tc.text, tc.want)
		}
	}
}

func detectionTypesContain(types []diagnostic.DetectionType, want diagnostic.DetectionType) bool {
	for _, got := range types {
		if got == want {
			return true
		}
	}
	return false
}

func TestLooksIncidentGuidanceWorthyIgnoresNoErrorSummary(t *testing.T) {
	if looksIncidentGuidanceWorthy("제공된 로그에는 오류 이벤트가 없어 클러스터가 안정적으로 작동했습니다.") {
		t.Fatal("no-error summary should not trigger incident guidance")
	}
}

func TestLooksIncidentGuidanceWorthyIgnoresInternalRuntimeErrors(t *testing.T) {
	cases := []string{
		"next_directions 형식 오류가 반복되어 진단을 중단합니다.",
		"Error: parsing shim JSON",
		"Action target declared namespace \"all\"",
	}
	for _, tc := range cases {
		if looksIncidentGuidanceWorthy(tc) {
			t.Fatalf("internal runtime error should not trigger incident guidance: %q", tc)
		}
	}
}

func TestExtractTargetDoesNotDefaultToPod(t *testing.T) {
	target := extractTarget("deployment web is unavailable in namespace prod")
	if target.Kind != "deployment" || target.Name != "web" || target.PodName != "" {
		t.Fatalf("unexpected deployment target: %#v", target)
	}

	unknown := extractTarget("the workload is unhealthy")
	if unknown.Kind != "" || unknown.Name != "" || unknown.PodName != "" {
		t.Fatalf("unidentified target should stay empty, got %#v", unknown)
	}
}

func TestIncidentSummaryStepCommandUsesStepLevelSafety(t *testing.T) {
	target := diagnostic.KubernetesTarget{
		Namespace: "prod",
		Kind:      "pod",
		Name:      "app",
	}

	if cmd, ok := incidentSummaryStepCommand(guidance.PlanStep{
		CommandTemplate: "kubectl describe {{kind}} {{name}} -n {{namespace}}",
		RenderedCommand: "kubectl describe pod app -n prod",
	}, target); !ok || cmd != "kubectl describe pod app -n prod" {
		t.Fatalf("valid rendered command should be shown, got ok=%v cmd=%q", ok, cmd)
	}

	if _, ok := incidentSummaryStepCommand(guidance.PlanStep{
		CommandTemplate: "kubectl describe {{kind}} {{name}} -n {{namespace}}",
		RenderedCommand: "kubectl describe pod app -n",
	}, diagnostic.KubernetesTarget{Kind: "pod", Name: "app"}); ok {
		t.Fatal("missing namespace value should hide only the command text")
	}

	if _, ok := incidentSummaryStepCommand(guidance.PlanStep{
		RenderedCommand: "kubectl describe pod app -n",
	}, target); ok {
		t.Fatal("raw rendered command with namespace flag missing a value should be hidden")
	}

	if cmd, ok := incidentSummaryStepCommand(guidance.PlanStep{
		RenderedCommand: "kubectl describe pod app-n",
	}, target); !ok || cmd != "kubectl describe pod app-n" {
		t.Fatalf("resource names ending in -n should not be filtered, got ok=%v cmd=%q", ok, cmd)
	}

	if _, ok := incidentSummaryStepCommand(guidance.PlanStep{
		RenderedCommand:      "kubectl delete pod app -n prod",
		RequiresConfirmation: true,
	}, target); ok {
		t.Fatal("confirmation-required command should not be shown as a copyable summary command")
	}
}

func TestIncidentGuidanceFlowTransitionsFromEvidenceToOffer(t *testing.T) {
	flow := NewIncidentGuidanceFlow()
	flow.ObserveUserInput("OOMKilled 원인을 분석해줘")
	o := &Orchestrator{agentWrap: &react.Loop{}}

	if err := flow.AfterAgentText(o, "Last State: Terminated Reason: OOMKilled"); err != nil {
		t.Fatalf("after agent text: %v", err)
	}
	if flow.phase != incidentGuidanceOfferPending {
		t.Fatalf("phase = %v, want offer pending", flow.phase)
	}
	if flow.problemText == "" || len(flow.evidence) != 1 {
		t.Fatalf("unexpected captured lifecycle: problem=%q evidence=%#v", flow.problemText, flow.evidence)
	}

	flow.deferOffer()
	flow.RecordEvidence("pod logs show OOMKilled")
	if flow.phase != incidentGuidanceIdle {
		t.Fatalf("deferred offer should stay idle, got phase=%v", flow.phase)
	}
}

func TestIncidentGuidanceResultUsableRequiresRunbookMatchAndTargetCompatibility(t *testing.T) {
	base := &guidance.ClientResult{
		Signal: diagnostic.ProblemSignal{
			DetectionTypes: []diagnostic.DetectionType{diagnostic.DetectionPending},
			Target:         diagnostic.KubernetesTarget{Kind: "deployment", Name: "web"},
		},
		Runbook: &guidance.GuideSearchResult{Cases: []guidance.GuideCase{{
			Title:          "Node NotReady",
			MatchTypes:     []diagnostic.DetectionType{"NodeNotReady"},
			RelatedObjects: []string{"Node", "Pod"},
		}}},
		Plan: &guidance.RemediationPlan{
			Steps: []guidance.PlanStep{{Description: "check node"}},
		},
	}
	if incidentGuidanceResultUsable(base) {
		t.Fatal("mismatched detection and target should not produce a usable incident runbook")
	}

	base.Runbook.Cases[0] = guidance.GuideCase{
		Title:          "Pending / FailedScheduling",
		MatchTypes:     []diagnostic.DetectionType{diagnostic.DetectionPending},
		RelatedObjects: []string{"Pod", "Node"},
	}
	if incidentGuidanceResultUsable(base) {
		t.Fatal("deployment target should not use a pod/node-only runbook without validation compatibility")
	}

	base.Signal.Target.Kind = "pod"
	if !incidentGuidanceResultUsable(base) {
		t.Fatal("matching detection and compatible target should be usable")
	}
}

func updateInputModelForTest(t *testing.T, m inputModel, msg tea.Msg) inputModel {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(inputModel)
	if !ok {
		t.Fatalf("updated model type = %T, want inputModel", updated)
	}
	return next
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected view to contain %q, got:\n%s", substr, s)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("expected view not to contain %q, got:\n%s", substr, s)
	}
}
