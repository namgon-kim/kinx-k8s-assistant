# Agent Instructions for kinx-k8s-assistant

## Project Summary

This repository implements `k8s-assistant`, a Go CLI for operating Kubernetes clusters through natural language.

The project intentionally owns the ReAct loop, approval UX, prompt rendering, output formatting, read-only enforcement, and troubleshooting orchestration. It uses `kubectl-ai` primarily as a library for `gollm` and Kubernetes/tool connector primitives.

## Core Architecture

- `cmd/k8s-assistant`: main CLI entrypoint and flags.
- `internal/react`: k8s-assistant-owned ReAct loop, prompt rendering, shim parser, read-only enforcement, language translation.
- `internal/toolconnector`: kubectl-ai tool registry integration and MCP config sync.
- `internal/orchestrator`: interactive CLI, meta commands, output formatting, troubleshooting flow coordination.
- `internal/troubleshooting`: internal runbook/RAG client and planning logic. This is not a standalone MCP server.
- `cmd/log-analyzer-server`, `internal/loganalyzer`: log analysis service and domain logic.
- `cmd/troubleshooting-upload`: helper for embedding runbooks/issues and uploading to Qdrant.
- `prompts/default.tmpl`: default runtime prompt. Treat this as the primary production prompt.
- `prompts/system_ko.tmpl`: Korean reference/convenience prompt. Do not assume it is used by default.
- `docs/drafts`, `docs/reviews`: design and review notes.

## Implementation Rules

- Prefer existing package boundaries and local patterns.
- Do not reintroduce `trouble-shooting-server`; troubleshooting should stay as an internal package/client.
- MCP servers are optional. Only servers declared in `~/.k8s-assistant/mcp.yaml` should be loaded.
- `log-analyzer` and `troubleshooting` are separate domains:
  - log-analyzer: logs/events/metrics observation and pattern analysis.
  - troubleshooting: runbooks, operating issue RAG, remediation planning.
- Do not use RAG for log pattern analysis unless explicitly requested.
- Keep Kubernetes execution in the ReAct/tool loop and approval flow.
- Do not let troubleshooting directly execute Kubernetes changes.

## ReAct Loop and Prompt Rules

- `internal/react` owns the ReAct loop.
- `prompts/default.tmpl` is the main prompt path.
- Tool/function calling and shim mode must both remain supported.
- Shim mode expects a single JSON object in a `json` code block and is repaired by `internal/react/shim.go`.
- Do not translate or mutate tool calls, function call JSON, kubectl commands, resource names, field names, JSON/YAML, or raw command output.
- If `lang.language=Korean` and `lang.model`/`lang.endpoint` are set, the main model should operate in English and only user-facing natural-language text should be translated through the language model client.
- `lang` provider is fixed to OpenAI-compatible. Do not add generic provider selection unless explicitly requested.
- Translation failures should not leak English fallback text by default; surface a Korean error message.

## Read-Only Mode

- Config key: `readonly`.
- CLI flag: `--read-only`.
- Meta command: `/readonly on|off|status`.
- Read-only mode must block Kubernetes resource mutations even if the user previously selected “do not ask again.”
- Non-mutating kubectl calls such as `get`, `describe`, `logs`, `top`, `api-resources`, `api-versions`, `version`, `config`, and `auth` may run.
- Read-only pipelines are allowed only when the first segment is read-only kubectl and later segments are safe local text processors such as `tail`, `head`, `grep`, `awk`, `sed`, `sort`, `uniq`, `wc`, `cut`, `jq`, `yq`, or `column`.
- Pipelines containing mutating kubectl verbs must be blocked. Example: `kubectl get pod app -o yaml | kubectl apply -f -` is forbidden.

## Meta Commands

Current user-facing meta commands include:

- `/config`
- `/model`
- `/lang Korean|English|status`
- `/readonly on|off|status`
- `/kubeconfig`
- `/kube-context`
- `/save`

Meta command changes that affect runtime behavior should invalidate the active agent when needed.

## Language Defaults

- Default `lang.language` is `English`.
- Korean output requires explicit config or `/lang Korean`.
- Example config should use `language: English` unless the user explicitly asks to default to Korean.

## Troubleshooting Gate

- Do not trigger troubleshooting from generic lookup/summary requests.
- Words like “문제” in a request such as “이벤트 로그를 보고 어떤 문제가 있었는지 요약해줘” should not alone trigger troubleshooting.
- Troubleshooting should be offered only for concrete failure signals or explicit repair/diagnostic intent.
- No-error/stable summaries should not trigger troubleshooting.

## Documentation Rules

- Keep README/GUIDE focused on stable behavior.
- Put uncertain designs, reviews, and drafts under `docs/drafts` or `docs/reviews`.
- Do not leave new root-level draft/review documents unless the user explicitly asks for that location.
- `draft_log_analyzer.md` may exist as user work; do not move or delete it unless requested.

## Verification

- Do not run build commands unless the user explicitly asks. The user requested that builds are not needed.
- Prefer focused tests for changed packages, for example:
  - `GOCACHE=/Users/ngkim/workspaces/kinx-k8s-assistant/.cache/go-build go test ./internal/react ./internal/orchestrator ./internal/config -count=1`
  - `GOCACHE=/Users/ngkim/workspaces/kinx-k8s-assistant/.cache/go-build go test ./...`
- Use workspace-local `GOCACHE` if the default Go cache is blocked by sandbox permissions.
- Remove temporary `.cache/` after test runs.
- Always run `git diff --check` after edits.

## Editing and Git Hygiene

- Use `rg` for searching.
- Use `apply_patch` for manual file edits.
- Do not revert unrelated user changes.
- The worktree may be dirty; inspect before touching files that already have changes.
- Avoid destructive commands.
- Keep generated or temporary files out of the final worktree.

