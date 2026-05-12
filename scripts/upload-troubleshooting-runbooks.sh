#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARGS=(--target qdrant)

if [[ -n "${TROUBLE_SHOOTING_CONFIG:-}" ]]; then
  ARGS+=(--config "$TROUBLE_SHOOTING_CONFIG")
fi
if [[ -n "${RUNBOOK_DIR:-}" ]]; then
  ARGS+=(--runbook-dir "$RUNBOOK_DIR")
fi
if [[ -n "${EMBEDDING_URL:-}" ]]; then
  ARGS+=(--embedding-url "$EMBEDDING_URL")
fi
if [[ -n "${EMBEDDING_API_KEY:-}" ]]; then
  ARGS+=(--embedding-api-key "$EMBEDDING_API_KEY")
fi
if [[ -n "${EMBEDDING_MODEL:-}" ]]; then
  ARGS+=(--embedding-model "$EMBEDDING_MODEL")
fi
if [[ -n "${VECTOR_NAME:-}" ]]; then
  ARGS+=(--vector-name "$VECTOR_NAME")
fi
if [[ -n "${EMBEDDING_MAX_LENGTH:-}" ]]; then
  ARGS+=(--embedding-max-length "$EMBEDDING_MAX_LENGTH")
fi
QDRANT_URL_VALUE="${QDRANT_URL:-${1:-}}"
if [[ -n "$QDRANT_URL_VALUE" ]]; then
  ARGS+=(--qdrant-url "$QDRANT_URL_VALUE")
fi
if [[ -n "${QDRANT_API_KEY:-}" ]]; then
  ARGS+=(--qdrant-api-key "$QDRANT_API_KEY")
fi
if [[ -n "${QDRANT_COLLECTION:-}" ]]; then
  ARGS+=(--qdrant-collection "$QDRANT_COLLECTION")
fi
if [[ -n "${QDRANT_VECTOR_SIZE:-}" ]]; then
  ARGS+=(--qdrant-vector-size "$QDRANT_VECTOR_SIZE")
fi
if [[ -n "${QDRANT_DISTANCE:-}" ]]; then
  ARGS+=(--qdrant-distance "$QDRANT_DISTANCE")
fi
if [[ -n "${TROUBLESHOOTING_UPLOAD_TIMEOUT:-}" ]]; then
  ARGS+=(--timeout "$TROUBLESHOOTING_UPLOAD_TIMEOUT")
fi

GOCACHE="${GOCACHE:-/private/tmp/kinx-go-cache}" \
  go run "$ROOT_DIR/cmd/troubleshooting-upload" "${ARGS[@]}"
