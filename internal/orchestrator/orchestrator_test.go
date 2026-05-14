package orchestrator

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/config"
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

func TestLooksTroubleshootableIgnoresGenericUserProblemRequest(t *testing.T) {
	if looksTroubleshootable("tests 네임스페이스의 pods의 문제를 해결해줘") {
		t.Fatal("generic user problem request should not trigger troubleshooting offer")
	}
}

func TestLooksTroubleshootableDetectsConcreteKubernetesFailure(t *testing.T) {
	cases := []string{
		"pod test-oom is in CrashLoopBackOff",
		"Last State: Terminated Reason: OOMKilled",
		"Back-off restarting failed container",
	}
	for _, tc := range cases {
		if !looksTroubleshootable(tc) {
			t.Fatalf("expected concrete failure to trigger troubleshooting: %q", tc)
		}
	}
}

func TestShouldOfferTroubleshootingForQuerySkipsReadOnlyRequests(t *testing.T) {
	cases := []string{
		"pods을 보여줘",
		"default 네임스페이스의 pods 목록 조회",
		"pod 상세 정보를 describe 형식으로 출력해줘",
		"최근 이벤트 로그를 보고 어떤 문제가 있었는지 요약해줘",
	}
	for _, tc := range cases {
		if shouldOfferTroubleshootingForQuery(tc) {
			t.Fatalf("read-only query should not offer troubleshooting: %q", tc)
		}
	}
}

func TestShouldOfferTroubleshootingForQueryAllowsDiagnosticRequests(t *testing.T) {
	cases := []string{
		"tests 네임스페이스의 pods 문제를 해결해줘",
		"OOMKilled 원인을 분석해줘",
		"CrashLoopBackOff debug 해줘",
	}
	for _, tc := range cases {
		if !shouldOfferTroubleshootingForQuery(tc) {
			t.Fatalf("diagnostic query should offer troubleshooting: %q", tc)
		}
	}
}

func TestLooksTroubleshootableIgnoresNoErrorSummary(t *testing.T) {
	if looksTroubleshootable("제공된 로그에는 오류 이벤트가 없어 클러스터가 안정적으로 작동했습니다.") {
		t.Fatal("no-error summary should not trigger troubleshooting")
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
