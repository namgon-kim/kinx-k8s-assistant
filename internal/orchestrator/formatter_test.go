package orchestrator

import "testing"

func TestFormatTextPreservesPlainMultilineOutput(t *testing.T) {
	formatter := NewFormatter(false)
	input := "header: value\n  indented line\nplain status output"
	got := formatter.FormatText(input)
	if got == nil {
		t.Fatal("expected formatted message")
	}
	if got.Content != input {
		t.Fatalf("unexpected content:\n got: %q\nwant: %q", got.Content, input)
	}
}

func TestShouldRenderMarkdownDetectsTable(t *testing.T) {
	input := "name | status\n--- | ---\napi | ok"
	if !shouldRenderMarkdown(input) {
		t.Fatal("expected markdown table to render")
	}
}

func TestShouldRenderMarkdownIgnoresPlainPipeOutput(t *testing.T) {
	input := "command output includes a | pipe\nbut no markdown table separator"
	if shouldRenderMarkdown(input) {
		t.Fatal("plain pipe output should not render as markdown")
	}
}
