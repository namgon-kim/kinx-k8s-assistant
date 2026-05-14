# kinx-k8s-assistant

Kubernetes 클러스터를 자연어로 조회하고 관리하는 Go 기반 CLI 도구입니다. k8s-assistant가 ReAct 루프와 승인 UX를 직접 소유하고, `kubectl-ai`의 `gollm`/`pkg/tools` 계층을 LLM 및 Kubernetes Tool 커넥터로 사용합니다.

## 현재 상태

- 기본 CLI: `bin/k8s-assistant`
- 선택 MCP 서버:
  - `log-analyzer-server`: 로그/패턴 분석
- 내장 trouble-shooting 패키지: runbook 매칭, 운영 이슈 RAG 검색, 조치 계획 생성
- Qdrant 업로드 helper: `troubleshooting-upload`
- MCP 서버는 필수가 아닙니다. `~/.k8s-assistant/mcp.yaml`에 선언된 서버만 로딩합니다.
- 아직 확정되지 않은 trouble-shooting 설계 논점은 `docs/reviews/revise_troubleshooting.md`에 분리해 정리합니다.

## 역할 분리

| 구성 | 책임 |
|---|---|
| `k8s-assistant` | CLI, 설정 로드, ReAct 루프, 승인 흐름, 마스킹, MCP tool 등록 |
| `kubectl-ai` | gollm LLM client, kubectl/bash/custom/MCP Tool 인터페이스와 실행 커넥터 |
| `log-analyzer-server` | 로그 수집 인터페이스, 로그 패턴 분석 |
| `internal/troubleshooting` | runbook 매칭, Qdrant/RAG 검색, 조치 계획 생성 |
| `troubleshooting-upload` | runbook을 embedding 후 Qdrant에 업로드 |

RAG는 `trouble-shooting`의 운영 이슈/문서 검색에 사용합니다. `log-analyzer`의 로그 패턴 분석은 RAG가 아니라 로그/이벤트/메트릭 관측 파이프라인으로 다룹니다.

## 요구사항

필수:

- Go 1.24 이상
- 접근 가능한 Kubernetes cluster와 kubeconfig
- LLM provider API key 또는 로컬/사설 LLM endpoint

선택:

- `helm`, `kustomize`: custom tool 사용 시
- `npx`: sequential-thinking MCP 서버 사용 시
- Qdrant: trouble-shooting RAG 검색 또는 runbook 업로드 사용 시
- embedding/reranker endpoint: Qdrant 업로드 및 RAG 검색 사용 시

## 빌드

```bash
# CLI만 빌드
make build

# 전체 로컬 바이너리 빌드
make build-all

# 전체 linux/amd64 바이너리 빌드
make build-linux
```

주요 산출물:

```text
bin/k8s-assistant
bin/log-analyzer-server
bin/troubleshooting-upload
bin/k8s-assistant-linux-amd64
bin/log-analyzer-server-linux-amd64
bin/troubleshooting-upload-linux-amd64
```

주요 Make target:

| Target | 설명 |
|---|---|
| `make build` | `k8s-assistant`만 빌드 |
| `make build-all` | 로컬용 전체 바이너리 빌드 |
| `make build-linux` | linux/amd64 전체 바이너리 빌드 |
| `make build-log-analyzer` | log-analyzer 서버만 빌드 |
| `make build-troubleshooting-upload` | Qdrant 업로드 helper만 빌드 |
| `make run` | CLI 실행 |
| `make run-log-analyzer` | log-analyzer 서버 실행 |
| `make run-mcp-servers` | log-analyzer 서버 실행 |

## 기본 실행

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

기본값은 `llm-provider=gemini`, `model=gemini-2.0-flash`입니다. 실제 운영에서는 `~/.k8s-assistant/config.yaml`, 환경변수, CLI flag 중 하나로 provider/model/API key를 명시하는 것을 권장합니다.

## 설정 파일

### k8s-assistant 설정

기본 경로:

```text
~/.k8s-assistant/config.yaml
```

예시:

```yaml
llmprovider: openai
model: gpt-4o
openai_apikey: sk-...
openai_endpoint: https://api.openai.com/v1
kubeconfig: ~/.kube/config
maxiterations: 20
sessionbackend: memory
showtooloutput: false
readonly: false
lang:
  language: English
  model: ""
  endpoint: ""
  apikey: ""
```

샘플 파일을 복사해서 시작할 수 있습니다.

```bash
mkdir -p ~/.k8s-assistant
cp example-config.yaml ~/.k8s-assistant/config.yaml
```

설정 우선순위:

1. CLI flag
2. 환경변수
3. `~/.k8s-assistant/config.yaml`
4. 코드 기본값

### custom tools 설정

kubectl-ai의 helm/kustomize custom tool 설정은 kubectl-ai 기본 경로를 사용합니다.

```bash
mkdir -p ~/.config/kubectl-ai
cp config/tools.yaml ~/.config/kubectl-ai/tools.yaml
```

### MCP client 설정

기본 경로:

```text
~/.k8s-assistant/mcp.yaml
```

`--mcp-client` 실행 시 k8s-assistant는 이 파일을 읽고, kubectl-ai가 읽는 `~/.config/kubectl-ai/mcp.yaml`로 동기화합니다. 이 파일에 없는 MCP 서버는 로딩하지 않습니다.

예시 복사:

```bash
mkdir -p ~/.k8s-assistant
cp config/mcp.yaml ~/.k8s-assistant/mcp.yaml
```

log-analyzer를 사용할 때:

```yaml
servers:
  - name: log-analyzer
    url: http://localhost:9090/mcp
    use_streaming: true
    timeout: 60
```

trouble-shooting은 별도 MCP 서버를 실행하지 않습니다. k8s-assistant가 내부 패키지로 runbook/RAG 검색과 조치 계획 생성을 수행합니다.

## MCP 서버 실행

MCP 서버는 선택 사항입니다. `--mcp-client`를 사용할 때만 필요하고, `~/.k8s-assistant/mcp.yaml`에 등록한 서버만 실행하면 됩니다.

### log-analyzer-server

```bash
./bin/log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --runbook-dir internal/loganalyzer/rag/runbooks
```

제공 도구:

| 도구 | 설명 |
|---|---|
| `fetch_logs` | 지정 Pod/Container 로그 조회 |
| `analyze_pattern` | 로그 기반 이상 패턴 탐지 |
| `rag_lookup` | legacy runbook 검색 |
| `analyze_and_remediate` | 로그 조회, 패턴 탐지, legacy 조치 제안 |

현재 `log-analyzer`의 파일 로그 fetcher는 실제 파일 시스템 로그 읽기 구현이 제한적입니다. Kubernetes 실시간 로그는 k8s-assistant ReAct 루프의 `kubectl logs` tool 실행 흐름을 우선 사용합니다.

## trouble-shooting 설정

기본 설정 경로:

```text
~/.k8s-assistant/trouble-shooting.yaml
```

설정 예시:

```bash
mkdir -p ~/.k8s-assistant
cp config/trouble-shooting.yaml ~/.k8s-assistant/trouble-shooting.yaml
```

trouble-shooting은 MCP tool로 노출하지 않습니다. k8s-assistant가 문제 감지 후 사용자 확인을 받고 내부 패키지로 runbook/RAG 검색과 조치 계획 생성을 수행합니다. Kubernetes 명령 실행은 k8s-assistant ReAct 루프와 승인 흐름에서 처리합니다.

`config/trouble-shooting.yaml` 예시:

```yaml
trouble_shooting:
  rag:
    provider: qdrant
    mode: hybrid

    embedding:
      url: http://1.201.177.120:4000
      api_key: ""
      model: bge-m3
      vector_name: dense
      vector_size: 1024
      distance: Cosine
      max_length: 1024
      normalize_embeddings: true

    qdrant:
      url: http://localhost:6333
      api_key: ""
      collection: k8s_troubleshooting_runbooks_v1
      limit: 11
      with_payload: true
      with_vectors: false
      search_params:
        exact: true

    reranker:
      enabled: true
      url: http://1.201.177.120:4000
      api_key: ""
      model: bge-reranker-v2-m3
      top_n: 3
      max_length: 1024
      use_fp16: true
      normalize: true
```

설정 파일이 없으면 k8s-assistant는 기본값과 embedded runbook으로 동작합니다. Qdrant 검색을 사용하려면 `rag.provider: qdrant` 설정과 Qdrant/embedding endpoint가 필요합니다.

### runbook 업로드

```bash
./bin/troubleshooting-upload
```

명시 설정:

```bash
./bin/troubleshooting-upload --config config/trouble-shooting.yaml
```

업로드 helper는 runbook text를 embedding endpoint로 vector화한 뒤 Qdrant에 upsert합니다. 이는 k8s-assistant 런타임 필수 기능이 아니라 RAG 검색 검증과 초기 데이터 적재를 위한 helper입니다.

## CLI 옵션

| 옵션 | 기본값 | 설명 |
|---|---|---|
| `--llm-provider` | `gemini` | LLM provider |
| `--model` | `gemini-2.0-flash` | 사용할 모델 |
| `--kubeconfig` | `~/.kube/config` if exists | kubeconfig 경로 |
| `--skip-verify-ssl` | `false` | LLM provider SSL 검증 생략 |
| `--enable-tool-use-shim` | `false` | native tool calling 미지원 모델용 JSON ReAct shim |
| `--mcp-client` | `false` | `~/.k8s-assistant/mcp.yaml` 기반 MCP client 활성화 |
| `--max-iterations` | `20` | k8s-assistant ReAct 루프 최대 반복 |
| `--show-tool-output` | `false` | 도구 실행 결과 표시 |
| `--read-only` | `false` | Kubernetes 리소스 변경 명령 차단 |
| `--prompt-template` | 자동 탐색 (`prompts/default.tmpl`) | 시스템 프롬프트 템플릿 경로 |
| `--session-backend` | `memory` | 세션 저장 방식 |
| `--log-file` | 없음 | 대화 로그 파일 |

`read-only`는 config 파일의 `readonly: true`, CLI의 `--read-only`, 또는 실행 중 `/readonly on|off|status` 메타 명령으로 제어합니다. 실행 중 변경한 값은 `/save`를 입력해야 `~/.k8s-assistant/config.yaml`에 저장됩니다.

`lang.language`는 `Korean` 또는 `English`를 사용합니다. `Korean`이고 `lang.model`/`lang.endpoint`가 설정되어 있으면 primary model은 영어 중심으로 ReAct/tool loop를 수행하고, 사용자에게 보여줄 자연어 출력만 openai-compatible 번역 모델로 한국어 변환합니다.
| `--log-dir` | `~/.k8s-assistant/logs` | 시스템 로그 디렉토리 |
| `--log-level` | `0` | klog verbosity |
| `--show-log-output` | `false` | 시스템 로그 콘솔 출력 |

## 환경변수

| 변수 | 설명 |
|---|---|
| `KUBECONFIG` | kubeconfig 경로 |
| `OPENAI_API_KEY` | OpenAI API key |
| `OPENAI_ENDPOINT` | OpenAI 호환 endpoint |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Gemini API key |
| `LLM_PROVIDER` | 기본 LLM provider override |
| `MODEL` | 기본 model override |

## 주요 경로

```text
~/.k8s-assistant/
├── config.yaml
├── mcp.yaml
├── trouble-shooting.yaml
├── history
└── logs/

~/.config/kubectl-ai/
├── tools.yaml
└── mcp.yaml  # k8s-assistant가 ~/.k8s-assistant/mcp.yaml에서 동기화
```

## 프로젝트 구조

```text
kinx-k8s-assistant/
├── cmd/
│   ├── k8s-assistant/
│   ├── log-analyzer-server/
│   └── troubleshooting-upload/
├── config/
│   ├── mcp.yaml
│   ├── tools.yaml
│   └── trouble-shooting.yaml
├── internal/
│   ├── config/
│   ├── diagnostic/
│   ├── loganalyzer/
│   ├── orchestrator/
│   ├── react/
│   ├── toolconnector/
│   └── troubleshooting/
├── prompts/
│   ├── default.tmpl
│   └── system_ko.tmpl
├── docs/
│   ├── drafts/
│   └── reviews/
├── GUIDE.md
├── Makefile
└── README.md
```

## 개발

```bash
# 전체 테스트
go test ./...

# 특정 패키지 테스트
go test ./internal/orchestrator -count=1
go test ./internal/troubleshooting -count=1

# 포맷
go fmt ./...

# 의존성 정리
go mod tidy
```

## 문제 해결

### `--mcp-client`가 특정 서버를 요구함

`~/.k8s-assistant/mcp.yaml`에 해당 서버가 등록되어 있는지 확인합니다. k8s-assistant는 이 파일에 선언된 서버만 연결 체크합니다.

```bash
cat ~/.k8s-assistant/mcp.yaml
```

### 시스템 로그가 콘솔에 보임

기본값은 콘솔 출력 비활성화입니다. `--show-log-output`을 사용한 경우에만 stderr에도 출력합니다.

### RAG 검색 결과가 비어 있음

확인 항목:

- `trouble_shooting.rag.provider`가 `qdrant`인지
- Qdrant가 실행 중인지
- collection 이름이 맞는지
- `troubleshooting-upload`로 runbook을 업로드했는지
- embedding endpoint가 접근 가능한지

## 설계 메모

확정되지 않은 항목은 README에 섞지 않고 `docs/reviews/revise_troubleshooting.md`에 기록합니다.

현재 주요 논점:

- 간단한 문제는 k8s-assistant ReAct 루프가 직접 해결하고, 불확실한 문제만 trouble-shooting/RAG를 호출할지
- LLM self assessment를 trouble-shooting 호출 게이트로 쓸지
- trouble-shooting 결과와 LLM 자체 해결책이 충돌할 때 최종 판단권을 어디에 둘지
- delete/recreate 전에 YAML export, runtime field 제거, 수정안 승인 절차를 어떻게 강제할지
