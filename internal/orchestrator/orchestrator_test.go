package orchestrator

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
