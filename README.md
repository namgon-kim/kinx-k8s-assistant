# kinx-k8s-assistant

Kubernetes 클러스터를 자연어로 조회하고 관리하는 Go 기반 CLI 도구입니다. k8s-assistant가 ReAct 루프와 승인 UX를 직접 소유하고, `kubectl-ai`의 `gollm`/`pkg/tools` 계층을 LLM 및 Kubernetes Tool 커넥터로 사용합니다.

## 현재 상태

- 기본 CLI: `bin/k8s-assistant`
- 선택 MCP 서버:
  - `log-analyzer-server`: 로그/패턴 분석
- 내장 guidance 패키지: resource guide 선행 검색, incident guide 매칭, 조치 계획 생성
- Qdrant 업로드 helper: `guidance-upload`
- MCP 서버는 필수가 아닙니다. `~/.k8s-assistant/mcp.yaml`에 선언된 서버만 로딩합니다.
- 아직 확정되지 않은 guidance 설계 논점은 `docs/reviews/revise_troubleshooting.md`에 분리해 정리합니다.

## 역할 분리

| 구성 | 책임 |
|---|---|
| `k8s-assistant` | CLI, 설정 로드, ReAct 루프, 승인 흐름, 마스킹, MCP tool 등록 |
| `kubectl-ai` | gollm LLM client, kubectl/bash/custom/MCP Tool 인터페이스와 실행 커넥터 |
| `log-analyzer-server` | 로그 수집 인터페이스, 로그 패턴 분석 |
| `internal/guidance` | resource guide 선행 검색, incident guide 매칭, Qdrant/RAG 검색, 조치 계획 생성 |
| `guidance-upload` | 지정한 runbook 디렉터리를 지정한 collection에 업로드 |

RAG는 `guidance`의 incident guide 검색에 사용합니다. `log-analyzer`의 로그 패턴 분석은 RAG가 아니라 로그/이벤트/메트릭 관측 파이프라인으로 다룹니다.

## 요구사항

필수:

- Go 1.24 이상
- 접근 가능한 Kubernetes cluster와 kubeconfig
- LLM provider API key 또는 로컬/사설 LLM endpoint

선택:

- `helm`, `kustomize`: custom tool 사용 시
- `npx`: sequential-thinking MCP 서버 사용 시
- Qdrant: guidance RAG 검색 또는 runbook 업로드 사용 시
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
bin/guidance-upload
bin/k8s-assistant-linux-amd64
bin/log-analyzer-server-linux-amd64
bin/guidance-upload-linux-amd64
```

주요 Make target:

| Target | 설명 |
|---|---|
| `make build` | `k8s-assistant`만 빌드 |
| `make build-all` | 로컬용 전체 바이너리 빌드 |
| `make build-linux` | linux/amd64 전체 바이너리 빌드 |
| `make build-log-analyzer` | log-analyzer 서버만 빌드 |
| `make build-guidance-upload` | Qdrant 업로드 helper만 빌드 |
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

guidance는 별도 MCP 서버를 실행하지 않습니다. k8s-assistant가 내부 패키지로 resource guide/incident guide 검색과 조치 계획 생성을 수행합니다.

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

## guidance 설정

기본 설정 경로:

```text
~/.k8s-assistant/guidance.yaml
```

설정 예시:

```bash
mkdir -p ~/.k8s-assistant
cp config/guidance.yaml ~/.k8s-assistant/guidance.yaml
```

guidance는 MCP tool로 노출하지 않습니다. k8s-assistant가 custom resource 작업/진단 요청에는 resource guide를 먼저 조회하고, 장애 흐름에는 사용자 확인 후 incident guide 검색과 조치 계획 생성을 수행합니다. Kubernetes 명령 실행은 k8s-assistant ReAct 루프와 승인 흐름에서 처리합니다.

### CRD-first resource guide 조회

k8s-assistant는 `cluster`, `machine`, `machinedeployment` 같은 단어만 보고 custom resource 여부를 LLM이 추측하게 하지 않습니다. 각 사용자 요청은 먼저 별도 requirement-analysis 단계에서 자연어 대상과 Kubernetes 리소스 후보를 분리합니다. 대상은 클러스터 환경, 네임스페이스 범위, 로그/이벤트/메트릭, 설정, 네트워크, 스토리지처럼 Kubernetes 리소스 오브젝트가 아닐 수도 있습니다.

- 첫 응답은 `requirement_analysis`만 허용하며, `target.category`, `scope.type`, `resource_candidates`, `request_type`, `action`으로 요청을 분류합니다.
- requirement-analysis 값 정의표는 `docs/requirement_analysis.md`를 기준으로 합니다.
- 예전 target 값 대신 새 `target.category`와 `resource_candidates` 계약만 사용합니다.
- `target.category`와 `scope.type`은 권장값을 우선 쓰되, 자연어 표현을 막지 않도록 런타임 enum으로 강제하지 않습니다.
- `resource_candidates`가 비어 있으면 런타임은 Kubernetes 리소스 컨텍스트를 만들지 않고 CRD resource guide/RAG 조회도 실행하지 않습니다.
- `resource_candidates`의 primary 후보가 있을 때만 런타임이 Kubernetes discovery로 해당 리소스가 built-in인지 CRD인지 확인합니다.
- built-in Kubernetes 리소스는 resource guide/RAG 조회를 건너뜁니다.
- CRD로 확인된 리소스만 resource guide/RAG 조회 대상이 됩니다.
- resource guide/RAG 조회가 불가능하거나 결과가 없으면, assistant는 guide가 있다고 가정하지 않고 일반 kubectl 증거 수집과 LLM 판단으로 진행합니다.
- guide가 특정 CRD family 근거를 제공한 경우에만 관련 guardrail을 조건부로 주입합니다.
- guide가 제공한 label selector, annotation, command template은 진단 컨텍스트에서 보존합니다.
- ReAct action target 검증은 comma-separated resource와 CRD plural/singular 차이를 허용합니다. 예를 들어 `machinedeployment,tenantcontrolplane` 또는 `machine`/`machines` 형태가 같은 명령 안에서 일관되게 쓰이면 불필요하게 correction loop를 만들지 않습니다. `kubectl logs <pod>`처럼 resource token을 생략하는 kubectl 관용형도 Pod target으로 인식합니다. 동일한 target correction이 반복되면 같은 LLM 재시도를 계속하지 않고 루프를 중단해 오류를 노출합니다.
- 명시적인 `resource kind + name + namespace` 요청의 첫 진단은 primary `resource_candidates` 항목을 직접 조회하거나 해당 target selector를 사용해야 합니다. 예를 들어 `namespace X의 Y cluster` 요청에서 첫 명령이 단순 `kubectl get pods -n X`로 빠지면 runtime이 correction합니다.
- `Namespace`/`namespace` 대소문자와 무관하게, namespace는 scope로 취급합니다. 사용자가 Namespace object 자체를 묻지 않았는데 Namespace 리소스 후보와 namespace scope를 동시에 내보내면 Kubernetes 리소스 컨텍스트로 승격하지 않습니다.
- `unknown`은 분류 값일 뿐 Kubernetes resource kind가 아닙니다. runtime은 `resource_candidates.kind=unknown`, `action.target.resource=unknown`, `kubectl get/describe unknown ...` 형태를 Kubernetes 리소스 대상으로 사용하지 않으며, 구체적인 리소스 종류가 없으면 명확화를 요구하도록 correction합니다.

Cluster API 계열 guide가 주입된 경우, 관리 클러스터의 `kubectl get node` 결과는 workload cluster node 등록/건강/providerID 판단 근거로 사용하지 않습니다. workload cluster node를 확인해야 하면 먼저 해당 workload cluster kubeconfig/context임을 확인해야 합니다.

### Iteration anchor (요청/가이드 정렬)

긴 ReAct 진단에서는 tool observation이 누적되며 모델 attention이 최근 출력으로 쏠려, 원래 요청과 주입된 가이드에서 표류하기 쉽습니다. 이를 완화하기 위해 매 iteration 전송 메시지 앞에 두 개의 짧은 anchor를 prepend합니다.

- **requirement_analysis anchor**: 승인된 `requirement_analysis`(필요 시 파생된 `request_context`)를 다시 명시하여 `target.category`, `resource_candidates`, `request_type`을 고정합니다. 모델이 임의로 진단 대상을 다른 리소스 종류로 바꾸지 않도록, 새로운 운영 문제 초점이 필요한 경우 `resource_guide_lookup`을 사용하도록 안내합니다.
- **guide_step anchor (L2)**: 활성 resource guide의 `diagnostic_steps`를 체크리스트 형식으로 요약하여 현재까지 완료된 step과 남은 step을 표시합니다. 가이드 본문 전체는 첫 주입 시 1회만 들어가고, 이후 iteration에는 이 경량 anchor만 반복됩니다.

guide_step anchor는 `injectResourceGuideAttempt`가 가이드를 주입할 때 `GuideCase.DiagnosticSteps`로부터 생성됩니다. 모델은 각 `action`에 `guide_progress.step_completed`(1-based step 번호)와 `guide_progress.evidence_useful`을 함께 보고하여 진행도를 자가 표기합니다.

자세한 흐름과 schema는 `docs/guide_progress_and_continuation.md`를 참고하세요.

### final_report와 사용자 선택 흐름

resource guide의 모든 `diagnostic_steps`가 완료되면 (또는 모델이 자체적으로 더 진단할 단계가 무의미하다고 판단하면) 모델은 `action` 대신 `final_report` JSON 객체를 emit합니다.

- `final_report.conclusive=true`이면 conclusion을 사용자에게 표시하고 진단을 종료합니다.
- `final_report.conclusive=false`이면 inconclusive 상태로 보고서를 출력하고, 모델에게 다시 `next_directions` 1~3개 옵션을 요청합니다.

`next_directions`가 도착하면 사용자에게 다음과 같이 선택지를 보여줍니다.

```text
진단을 어떻게 계속할지 선택해 주세요.

  1. [가이드 재검색] <option summary>
  2. [다른 접근] <option summary>
  3. 직접 다른 방향 입력
  4. 여기서 진단 종료
```

- `[가이드 재검색]`: 모델이 제안한 `resource_family`/`problem_focus`로 `searchAndInjectResourceGuide`를 다시 실행합니다.
- `[다른 접근]`: 모델이 제시한 `instruction`을 추가 directive로 주입하고 ReAct 루프를 재개합니다.
- `직접 다른 방향 입력`: 사용자가 자유 텍스트로 진단 방향을 지정하면 그 텍스트를 directive로 주입합니다.
- `여기서 진단 종료`: 보고서까지만 보여주고 진단을 종료합니다.

이 흐름은 `MaxIterations`와는 독립적이며, 강제 차단 없이 anchor와 prompt만으로 모델이 종결 시점을 인식하도록 설계되어 있습니다.

collection 이름은 `~/.k8s-assistant/config.yaml`에서 지정합니다.

```yaml
guidance:
  resource_guides: k8s_resource_guides_v1
  incident_guides: k8s_incident_guides_v1
```

`config/guidance.yaml` 예시:

```yaml
guidance:
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
./bin/guidance-upload --runbook-dir <dir> --collection <name>
```

명시 설정:

```bash
./bin/guidance-upload --config config/guidance.yaml --runbook-dir <dir> --collection <name>
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
| `--log-dir` | `~/.k8s-assistant/logs` | 시스템 로그 디렉토리 |
| `--log-level` | `0` | klog verbosity |
| `--show-log-output` | `false` | 시스템 로그 콘솔 출력 |

`read-only`는 config 파일의 `readonly: true`, CLI의 `--read-only`, 또는 실행 중 `/readonly on|off|status` 메타 명령으로 제어합니다. 실행 중 변경한 값은 `/save`를 입력해야 `~/.k8s-assistant/config.yaml`에 저장됩니다.

read-only 모드는 `kubectl get`, `describe`, `logs`, `top`, `api-resources` 같은 진단 명령은 허용하고, `apply`, `delete`, `patch`, `scale`, `auth reconcile` 같은 변경 명령은 차단합니다. `kubectl auth`는 `can-i`와 `whoami`만 read-only로 허용합니다. `kubectl -n <namespace> get ...`처럼 global flag가 verb 앞에 오는 read-only 명령도 진단 명령으로 인식합니다. `bash -c`/`bash -lc` 안의 명령이 read-only `kubectl`과 안전한 텍스트 처리 파이프라인으로만 구성된 경우도 진단 명령으로 허용합니다. `$()`, backtick, heredoc, process substitution, shell redirection처럼 안전성이 확인되지 않는 command는 변경 명령과 구분해 agent가 더 구체적인 read-only 명령으로 재시도하게 합니다.

JSON ReAct shim 사용 시 모델이 최종 답변을 JSON code block 없이 plain text로 반환해도, k8s-assistant는 이를 shim parse error가 아니라 최종 답변으로 처리합니다.

## Prompt / tool context 관리

k8s-assistant는 runtime prompt를 section 단위로 조립합니다. core ReAct, output contract, language policy, target/scope 보존, command guideline은 항상 포함하고, read-only, guidance protocol, manifest generation, Cluster API guardrail은 현재 요청과 RAG 결과에 따라 조건부로 포함합니다.

system prompt 자체는 매 iteration 동일하지만, 모델 attention이 최근 observation으로 쏠리는 문제를 줄이기 위해 매 iteration 전송 직전에 짧은 runtime anchor들을 prepend합니다.

- **runtime_state anchor**: 현재 `ControlState`, active gate, required/forbidden next output을 명시합니다.
- **requirement_analysis anchor**: 승인된 요청 분류와 `request_context`를 다시 명시합니다.
- **phase_step anchor**: accepted `phase_plan`의 현재 phase와 allowed next phase를 유지합니다.
- **guide_step anchor**: 활성 resource guide의 step checklist와 진행 상황을 다시 명시합니다.
- **mutation_verification anchor**: 변경 이후 남은 read-only evidence requirement 또는 `mutation_verification_result` 요구를 유지합니다.

가이드의 모든 step이 완료되면 모델에게 `final_report`를 요청하고, inconclusive면 `next_directions` 옵션을 받아 사용자 선택을 거쳐 진단을 계속하거나 종료합니다. 자세한 schema와 state machine은 `docs/guide_progress_and_continuation.md`에 정리되어 있습니다.

tool schema는 안전성을 위해 pruning하지 않고 등록된 전체 tool set을 유지합니다. 대신 ToolProfile hash를 사용해 동일한 tool schema 조합을 캐싱/참조 가능한 단위로 관리합니다.

## Context compact

긴 ReAct 진단이 이어져 LLM context가 커지면 k8s-assistant는 원문 tool 결과를 계속 누적하지 않고 compact state로 전환합니다.

compact state에는 다음 정보만 보존합니다.

- 원 질문
- primary target과 namespace scope
- CRD discovery 결과와 guide ref/hash
- 수행한 절차와 순서
- 각 절차에서 얻은 단서
- result hash
- 다음에 수행해야 할 동작

compact는 추정 context 사용량이 모델 context limit의 80% 이상이 되면 실행됩니다. correction이나 guide injection만으로 낮은 token 사용량에서 compact하지 않습니다. provider가 context length 오류를 반환한 경우에도 compact 후 1회 재시도합니다. compact가 발생하면 터미널에 `↻ context compacting...`, `✓ context compacted...` 안내가 출력됩니다.

context limit은 모델명으로 추정하며, 명시적으로 지정하려면 `K8S_ASSISTANT_CONTEXT_LIMIT_TOKENS` 환경변수를 사용합니다.

`lang.language`는 `Korean` 또는 `English`를 사용합니다. `Korean`이고 `lang.model`/`lang.endpoint`가 설정되어 있으면 primary model은 영어 중심으로 ReAct/tool loop를 수행하고, 사용자에게 보여줄 자연어 출력만 openai-compatible 번역 모델로 한국어 변환합니다.

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
| `K8S_ASSISTANT_CONTEXT_LIMIT_TOKENS` | context compact 기준 계산에 사용할 모델 context limit override |

## 주요 경로

```text
~/.k8s-assistant/
├── config.yaml
├── mcp.yaml
├── guidance.yaml
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
│   └── guidance-upload/
├── config/
│   ├── mcp.yaml
│   ├── tools.yaml
│   └── guidance.yaml
├── internal/
│   ├── config/
│   ├── diagnostic/
│   ├── loganalyzer/
│   ├── orchestrator/
│   ├── react/
│   ├── toolconnector/
│   └── guidance/
├── prompts/
│   ├── default.tmpl
│   └── system_ko.tmpl
├── docs/
│   ├── drafts/
│   ├── reviews/
│   ├── requirement_analysis.md
│   └── guide_progress_and_continuation.md
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
go test ./internal/guidance -count=1

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

- `guidance.rag.provider`가 `qdrant`인지
- Qdrant가 실행 중인지
- collection 이름이 맞는지
- `guidance-upload`로 runbook을 업로드했는지
- embedding endpoint가 접근 가능한지

## 설계 메모

확정되지 않은 항목은 README에 섞지 않고 `docs/reviews/revise_troubleshooting.md`에 기록합니다.

현재 주요 논점:

- 간단한 문제는 k8s-assistant ReAct 루프가 직접 해결하고, 불확실한 문제만 incident guidance/RAG를 호출할지
- LLM self assessment를 incident guidance 호출 게이트로 쓸지
- incident guidance 결과와 LLM 자체 해결책이 충돌할 때 최종 판단권을 어디에 둘지
- delete/recreate 전에 YAML export, runtime field 제거, 수정안 승인 절차를 어떻게 강제할지
