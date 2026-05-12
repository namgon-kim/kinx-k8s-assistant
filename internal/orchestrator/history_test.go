package orchestrator

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputHistoryUpDownNavigation(t *testing.T) {
	m := newInputModel(">>> ", "")
	m.history = []string{"first", "second", "third"}
	m.historyIdx = len(m.history)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "third" {
		t.Fatalf("up should show latest command, got %q", got)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "second" {
		t.Fatalf("second up should show previous command, got %q", got)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "third" {
		t.Fatalf("down should move toward newer command, got %q", got)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "" {
		t.Fatalf("down at newest should restore current input, got %q", got)
	}
}

func TestInputHistoryPreservesCurrentDraft(t *testing.T) {
	m := newInputModel(">>> ", "")
	m.history = []string{"first", "second"}
	m.historyIdx = len(m.history)
	m.textinput.SetValue("draft")

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "second" {
		t.Fatalf("up should show latest command, got %q", got)
	}

	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(inputModel)
	if got := m.textinput.Value(); got != "draft" {
		t.Fatalf("down should restore draft, got %q", got)
	}
}
