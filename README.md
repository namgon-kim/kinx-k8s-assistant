# kinx-k8s-assistant

`k8s-assistant`는 Kubernetes 클러스터를 자연어로 조회하고 운영하기 위한 Go CLI입니다.

이 프로젝트는 ReAct 루프, 승인 UX, read-only 정책, prompt rendering, output formatting, guidance orchestration을 직접 소유합니다. `kubectl-ai`는 `gollm` LLM client와 Kubernetes/tool connector 계층으로 사용합니다.

## 핵심 구성

| 구성 | 역할 |
| --- | --- |
| `cmd/k8s-assistant` | 메인 CLI |
| `internal/react` | ReAct loop, structured output, read-only/mutation/guidance gate |
| `internal/orchestrator` | interactive CLI, meta command, formatter, incident guidance side-flow |
| `internal/guidance` | resource guide, incident guide, RAG/runbook client |
| `cmd/log-analyzer-server` | 선택형 log/event/metric 분석 MCP 서버 |
| `cmd/guidance-upload` | guidance 자료를 Qdrant collection에 업로드하는 helper |

`guidance`는 standalone MCP 서버가 아닙니다. Kubernetes 변경 실행은 항상 `k8s-assistant` ReAct/tool loop와 승인 흐름 안에서 처리합니다.

## 요구사항

- Go 1.24 이상
- 접근 가능한 Kubernetes cluster와 kubeconfig
- LLM provider API key 또는 OpenAI-compatible/local endpoint

선택 기능:

- Qdrant, embedding/reranker endpoint: guidance RAG 검색 또는 runbook 업로드
- `helm`, `kustomize`: custom tool 사용
- `--mcp-client`: `~/.k8s-assistant/mcp.yaml`에 선언한 선택 MCP 서버 사용

## 빌드

```bash
make build          # bin/k8s-assistant
make build-all      # CLI, log-analyzer-server, guidance-upload
make build-linux    # linux/amd64 binaries
```

주요 산출물:

```text
bin/k8s-assistant
bin/log-analyzer-server
bin/guidance-upload
```

## 빠른 실행

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig ~/.kube/config
```

단발성 쿼리:

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  "tests 네임스페이스의 pods 상태를 확인해줘"
```

기본값은 `llm-provider=gemini`, `model=gemini-2.0-flash`입니다. 운영 환경에서는 config, 환경변수, CLI flag 중 하나로 provider/model/API key를 명시하는 것을 권장합니다.

## 최소 설정

기본 설정 경로:

```text
~/.k8s-assistant/config.yaml
```

샘플:

```bash
mkdir -p ~/.k8s-assistant
cp example-config.yaml ~/.k8s-assistant/config.yaml
```

예시:

```yaml
llmprovider: openai
model: gpt-4o
openai_apikey: sk-...
openai_endpoint: https://api.openai.com/v1
kubeconfig: ~/.kube/config
maxiterations: 20
showtooloutput: false
readonly: false
lang:
  language: English
```

설정 우선순위:

1. CLI flag
2. 환경변수
3. `~/.k8s-assistant/config.yaml`
4. 코드 기본값

주요 환경변수:

| 변수 | 설명 |
| --- | --- |
| `KUBECONFIG` | kubeconfig 경로 |
| `OPENAI_API_KEY`, `OPENAI_ENDPOINT` | OpenAI/OpenAI-compatible provider 설정 |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Gemini API key |
| `LLM_PROVIDER`, `MODEL` | 기본 provider/model override |

## 자주 쓰는 옵션과 명령

| 항목 | 설명 |
| --- | --- |
| `--read-only` | Kubernetes 리소스 변경 명령 차단 |
| `--mcp-client` | `~/.k8s-assistant/mcp.yaml` 기반 MCP client 활성화 |
| `--enable-tool-use-shim` | native tool calling 미지원 모델용 JSON ReAct shim |
| `--show-tool-output` | tool 실행 결과를 터미널에 표시 |
| `--prompt-template` | 기본값은 `prompts/default.tmpl` |

Interactive meta command:

```text
/config
/model
/lang Korean|English|status
/readonly on|off|status
/kubeconfig
/kube-context
/save
```

`/readonly` 같은 runtime 설정 변경은 `/save`를 실행해야 config 파일에 저장됩니다.

## 선택 기능

### MCP

MCP 서버는 필수가 아닙니다. `--mcp-client`를 켰을 때만 `~/.k8s-assistant/mcp.yaml`에 선언된 서버를 로딩합니다.

```bash
mkdir -p ~/.k8s-assistant
cp config/mcp.yaml ~/.k8s-assistant/mcp.yaml
```

`log-analyzer-server`를 사용할 때만 별도 서버를 실행합니다.

```bash
./bin/log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --runbook-dir internal/loganalyzer/rag/runbooks
```

### Guidance/RAG

guidance 기본 설정 경로:

```text
~/.k8s-assistant/guidance.yaml
```

```bash
mkdir -p ~/.k8s-assistant
cp config/guidance.yaml ~/.k8s-assistant/guidance.yaml
```

Qdrant에 runbook 또는 resource guide 자료를 올릴 때:

```bash
./bin/guidance-upload --runbook-dir <dir> --collection <name>
```

설정 파일이 없으면 embedded runbook/default 기반으로 동작합니다. Qdrant 검색을 사용하려면 `config/guidance.yaml`의 provider/endpoint/collection 설정이 필요합니다.

## 문서

| 문서 | 내용 |
| --- | --- |
| [`GUIDE.md`](./GUIDE.md) | 사용자 실행/설정 가이드 |
| [`docs/README.md`](./docs/README.md) | 문서 분류, 구현/미구현 상태, 레이아웃 감사 결과 |
| [`docs/architecture_orchestrator_react.md`](./docs/architecture_orchestrator_react.md) | Orchestrator와 ReAct loop 구조 |
| [`docs/reviews/react_loop_structure_review.md`](./docs/reviews/react_loop_structure_review.md) | ReAct loop 구조 리스크 리뷰 |
| [`docs/requirement_analysis.md`](./docs/requirement_analysis.md) | 요청 분석 계약 |
| [`docs/request_processing_phases.md`](./docs/request_processing_phases.md) | phase plan/progress 계약 |
| [`docs/guide_progress_and_continuation.md`](./docs/guide_progress_and_continuation.md) | guide progress, final_report, next_directions 흐름 |
| [`docs/TODO.md`](./docs/TODO.md) | 남은 TODO와 drop 사유 |
| [`bug.md`](./bug.md) | 현재 확인한 버그/리스크 목록 |

확정되지 않은 설계, 리뷰, 초안은 `docs/drafts/`와 `docs/reviews/`에 둡니다. RAG 입력 자료는 `docs/rag/`, 정제 전 원천은 `docs/rag_raws/`에 둡니다.

## 프로젝트 구조

```text
cmd/
  k8s-assistant/          # main CLI
  log-analyzer-server/    # optional log analyzer MCP server
  guidance-upload/        # Qdrant upload helper
internal/
  react/                  # ReAct loop and runtime gates
  orchestrator/           # CLI interaction and meta commands
  guidance/               # resource/incident guide client
  loganalyzer/            # log/event/metric analysis domain
  toolconnector/          # kubectl-ai tool registry integration
prompts/
  default.tmpl            # production prompt
  system_ko.tmpl          # Korean reference prompt
docs/
  README.md               # documentation index
  drafts/, reviews/       # design notes and audits
  rag/, rag_raws/         # guidance knowledge sources
```

## 개발

```bash
go test ./...
go test ./internal/orchestrator -count=1
go test ./internal/guidance -count=1
go fmt ./...
go mod tidy
```

테스트나 빌드가 필요할 때는 변경 범위에 맞춰 focused test를 우선 실행합니다.
