# K8s-Assistant 사용 가이드

Kubernetes 클러스터를 자연어로 관리하는 AI 어시스턴트입니다.

## 설치 및 실행

### Linux 빌드
```bash
make build-linux
./bin/k8s-assistant-linux-amd64
```

### 전체 빌드
```bash
make build-all
```

생성 바이너리:

```text
bin/k8s-assistant
bin/log-analyzer-server
bin/guidance-upload
```

### 설정

#### 1. Config 파일 (권장)
`~/.k8s-assistant/config.yaml`:
```yaml
# LLM 설정
llmprovider: openai
model: gpt-4o
openai_apikey: sk-...          # Provider별 API 키 (env가 우선)
openai_endpoint: https://...   # Provider별 API 엔드포인트 (필요한 provider만 설정)

# Kubernetes 설정
kubeconfig: ~/.kube/config

# 동작 설정
maxiterations: 20
sessionbackend: memory
showtooloutput: true
readonly: false
lang:
  language: English
  model: ""
  endpoint: ""
  apikey: ""
```

샘플 파일:

```bash
mkdir -p ~/.k8s-assistant
cp example-config.yaml ~/.k8s-assistant/config.yaml
```

native tool/function calling을 지원하지 않는 모델은 `enabletoolshim: true`로 JSON ReAct shim을 사용할 수 있습니다.

기본 프롬프트 템플릿은 `prompts/default.tmpl`입니다. `--prompt-template` 또는 `prompttemplatefile`로 다른 템플릿을 지정할 수 있습니다.

#### 2. 명령줄 옵션 (CLI 플래그)
```bash
./k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig ~/.kube/config \
  --max-iterations 20
```

#### 3. 환경 변수
```bash
export KUBECONFIG=$HOME/.kube/config
export LLM_PROVIDER=openai
export MODEL=gpt-4o
```

**설정 우선순위:**
1. CLI 플래그 (`--kubeconfig`, `--model`, `--llm-provider` 등)
2. 환경 변수 (`OPENAI_API_KEY`, `OPENAI_ENDPOINT`, `GEMINI_API_KEY`, `LLM_PROVIDER`, `MODEL` 등)
3. `~/.k8s-assistant/config.yaml`의 provider별 API/endpoint 필드
4. 기본값 - 모든 설정이 없을 때

## API 키 설정 방법

### 방법 1: config.yaml에 직접 기록
```yaml
llmprovider: openai
model: gpt-4o
openai_apikey: sk-...
openai_endpoint: https://api.openai.com/v1  # 선택사항
```

### 방법 2: 환경 변수 사용 (권장 - CI/CD)
```bash
export OPENAI_API_KEY=sk-...
export OPENAI_ENDPOINT=https://api.openai.com/v1  # 선택사항
./k8s-assistant
```

### 방법 3: 실행 옵션으로 provider/model 선택
```bash
./k8s-assistant --llm-provider openai --model gpt-4o
```

**주의:** kubectl-ai/gollm은 provider별 환경 변수가 설정되어 있어야 동작합니다. k8s-assistant는 시작 시 config.yaml의 provider별 API/endpoint 값을 필요한 환경 변수로 올린 뒤 gollm이 읽도록 합니다. 이미 환경 변수가 설정되어 있으면 환경 변수 값을 우선합니다.

## 메타 명령어

메타 명령어는 `/` 로 시작합니다. `/` 입력 시 자동으로 메타 명령 메뉴가 표시됩니다.

### 메뉴 표시
```bash
>>> /              # 메타 명령 메뉴 표시
```

메뉴에서 번호를 선택하거나 명령어를 직접 입력할 수 있습니다.

### 설정 조회
```bash
>>> /config        # 현재 LLM, Kubeconfig, Context 설정 표시
```

### Read-only 모드
```bash
>>> /readonly status  # 현재 read-only 상태 표시
>>> /readonly on      # Kubernetes 리소스 변경 명령 차단
>>> /readonly off     # 기존 승인 흐름으로 변경 명령 허용
```

**주의:** `/readonly` 변경 후 `/save` 명령으로 저장해야 다음 실행에도 유지됩니다.

read-only 모드는 Kubernetes 리소스 변경 명령을 차단하지만 `kubectl get`, `describe`, `logs`, `top`, `api-resources` 같은 진단 명령은 허용합니다. `kubectl -n <namespace> get ...`처럼 namespace flag가 verb 앞에 있는 명령도 read-only 진단 명령으로 인식합니다. `bash -c`/`bash -lc` 안의 명령이 read-only `kubectl`과 안전한 텍스트 처리 파이프라인으로만 구성된 경우도 진단 명령으로 허용합니다.

### 출력 언어
```bash
>>> /lang status   # 현재 출력 언어 표시
>>> /lang Korean   # 자연어 출력 한국어
>>> /lang English  # 자연어 출력 영어
```

`lang.language: Korean`이고 `lang.model`/`lang.endpoint`가 설정되어 있으면 primary model은 영어로 ReAct/tool loop를 수행하고, 사용자에게 보여줄 자연어 설명만 openai-compatible 번역 모델로 한국어 변환합니다.

### Kubeconfig 관리
```bash
>>> /kubeconfig         # 대화형으로 kubeconfig 경로 설정
>>> /kubeconfig <path>  # kubeconfig 경로 직접 설정 (예: ~/.kube/config)
```

**주의:** kubeconfig 설정 후 `/save` 명령으로 저장해야 합니다.

### Context 관리
```bash
>>> /kube-context          # 대화형 Context 선택 메뉴
>>> /kube-context list     # Context 목록 표시
>>> /kube-context current  # 현재 Context 정보 표시
>>> /kube-context switch <context-name>  # Context 변경
```

### 설정 저장
```bash
>>> /save          # 현재 설정을 ~/.k8s-assistant/config.yaml에 저장
```

**주의:** 메타 명령(/kubeconfig, /kube-context, /readonly, /lang 등)은 설정을 변경하지만 자동 저장되지 않습니다.
변경 사항을 저장하려면 명시적으로 `/save` 명령을 실행해야 합니다.

## MCP 서버 설정

`--mcp-client`는 `~/.k8s-assistant/mcp.yaml`에 선언된 서버만 kubectl-ai MCP 설정으로 동기화한 뒤, k8s-assistant의 Tool connector registry에 등록합니다. guidance는 내부 패키지로 실행되므로 MCP 서버 설정이 필요하지 않습니다.

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

주의: MCP tool 이름은 kubectl-ai tool connector 규칙에 따라 `<server_name>_<tool_name>` 형태로 노출됩니다.

## guidance 설정

guidance는 별도 서버를 실행하지 않습니다. k8s-assistant가 custom resource 작업/진단 요청에는 resource guide를 먼저 조회하고, 장애 흐름에는 사용자 확인 후 incident guide 검색과 조치 계획 생성을 수행합니다. Kubernetes 명령 실행은 k8s-assistant ReAct 루프와 승인 흐름이 담당합니다.

### CRD-first resource guide 조회

k8s-assistant는 LLM이 리소스 이름만 보고 custom resource 여부를 맞히는 것에 의존하지 않습니다. 사용자 요청은 먼저 별도 requirement-analysis 단계에서 자연어 대상과 Kubernetes 리소스 후보를 분리합니다. 대상은 리소스가 아닐 수도 있으므로, "이 클러스터의 문제를 해결해줘" 같은 요청은 구체적인 Kubernetes `Cluster` 오브젝트가 명시되지 않은 한 현재 접속한 클러스터 환경으로 분류합니다.

1. 첫 LLM 응답은 `requirement_analysis`만 반환합니다.
2. `target.category`, `scope.type`, `resource_candidates`, `request_type`, `action`으로 요청을 분류합니다.
3. requirement-analysis 값 정의표는 `docs/requirement_analysis.md`를 기준으로 합니다.
4. 예전 target 값 대신 새 `target.category`와 `resource_candidates` 계약만 사용합니다.
5. `target.category`와 `scope.type`은 권장값을 우선 쓰되, 자연어 표현을 막지 않도록 런타임 enum으로 강제하지 않습니다.
6. `resource_candidates`가 비어 있으면 Kubernetes 리소스 컨텍스트와 CRD resource guide/RAG 조회를 만들지 않습니다.
7. primary Kubernetes 리소스 후보가 있을 때만 런타임이 discovery로 built-in/CRD 여부를 확인합니다.
8. CRD로 확인된 경우에만 resource guide/RAG를 조회합니다.
9. guide 결과에 근거가 있을 때만 해당 CRD family 전용 주의사항을 조건부로 주입합니다.
10. resource guide/RAG 조회가 불가능하거나 결과가 없으면 guide를 가정하지 않고 일반 kubectl 증거 수집과 LLM 판단으로 진행합니다.

guide가 제공한 label selector, annotation, command template은 진단 컨텍스트에서 보존됩니다. Cluster API 계열 guide가 주입된 경우, 관리 클러스터의 `kubectl get node` 결과를 workload cluster node 등록/건강/providerID 판단 근거로 사용하지 않습니다. workload cluster node를 확인하려면 먼저 해당 workload cluster kubeconfig/context임을 확인해야 합니다.

ReAct action target 검증은 comma-separated resource와 CRD plural/singular 차이를 허용합니다. 예를 들어 `machinedeployment,tenantcontrolplane` 또는 `machine`/`machines` 형태가 같은 명령 안에서 일관되게 쓰이면 불필요하게 correction loop를 만들지 않습니다. `kubectl logs <pod>`처럼 resource token을 생략하는 kubectl 관용형도 Pod target으로 인식합니다. 동일한 target correction이 반복되면 같은 LLM 재시도를 계속하지 않고 루프를 중단해 오류를 노출합니다.

명시적인 `resource kind + name + namespace` 요청의 첫 진단은 primary `resource_candidates` 항목을 직접 조회하거나 해당 target selector를 사용해야 합니다. 예를 들어 `namespace X의 Y cluster` 요청에서 첫 명령이 단순 `kubectl get pods -n X`로 빠지면 runtime이 correction합니다.

`Namespace`/`namespace` 대소문자와 무관하게, namespace는 scope로 취급합니다. 사용자가 Namespace object 자체를 묻지 않았는데 Namespace 리소스 후보와 namespace scope를 동시에 내보내면 Kubernetes 리소스 컨텍스트로 승격하지 않습니다.

`unknown`은 분류 값일 뿐 Kubernetes resource kind가 아닙니다. runtime은 `resource_candidates.kind=unknown`, `action.target.resource=unknown`, `kubectl get/describe unknown ...` 형태를 Kubernetes 리소스 대상으로 사용하지 않으며, 구체적인 리소스 종류가 없으면 명확화를 요구하도록 correction합니다.

### 가이드 진행 추적과 종결 선택

resource guide가 주입되면 가이드의 진단 step들이 체크리스트로 추적되며, 매 iteration 시작 시 모델에 다음 두 가지를 다시 안내합니다.

- 사용자 요청 분류 (`requirement_analysis`): 진단 대상이 도중에 다른 리소스로 흘러가지 않도록 고정합니다.
- 가이드 진행도: 어떤 step이 완료됐고 어떤 step이 남았는지, 다음에 무엇을 수행해야 하는지를 짧게 다시 제공합니다.

가이드의 모든 step이 완료되면 모델은 일반 `kubectl` 명령을 또 실행하지 않고 진단 결과 보고서(`final_report`)를 생성합니다.

- 결론이 충분히 도출된 경우: 보고서를 사용자에게 출력하고 진단을 종료합니다.
- 결론이 충분하지 않은 경우: 보고서를 출력한 뒤 1~3개의 다음 진단 방향 옵션을 사용자에게 묻습니다.

다음과 같이 선택지가 표시됩니다.

```text
진단을 어떻게 계속할지 선택해 주세요.

  1. [가이드 재검색] 다른 운영 문제 초점으로 RAG 재조회
  2. [다른 접근] 관련 controller 로그 확인
  3. 직접 다른 방향 입력
  4. 여기서 진단 종료
```

`[가이드 재검색]`을 고르면 모델이 제안한 운영 문제 초점으로 새 RAG 검색이 실행되고, `[다른 접근]`을 고르면 모델이 제시한 방향으로 ReAct 루프가 계속됩니다. `직접 다른 방향 입력`을 고르면 사용자가 자유 텍스트로 진단 방향을 알려줄 수 있고, `여기서 진단 종료`를 고르면 보고서까지만 보여주고 마칩니다.

내부 schema와 상태 전이는 `docs/guide_progress_and_continuation.md`에 정리되어 있습니다.

기본 설정 경로:

```text
~/.k8s-assistant/guidance.yaml
```

예시 설정 복사:

```bash
mkdir -p ~/.k8s-assistant
cp config/guidance.yaml ~/.k8s-assistant/guidance.yaml
```

주요 설정:

`~/.k8s-assistant/config.yaml`:

```yaml
guidance:
  resource_guides: k8s_resource_guides_v1
  incident_guides: k8s_incident_guides_v1
```

`~/.k8s-assistant/guidance.yaml`:

```yaml
guidance:
  rag:
    provider: qdrant
    embedding:
      url: http://1.201.177.120:4000
      model: bge-m3
    qdrant:
      url: http://localhost:6333
    reranker:
      enabled: true
      url: http://1.201.177.120:4000
      model: bge-reranker-v2-m3
```

runbook을 Qdrant에 업로드:

```bash
./bin/guidance-upload --runbook-dir <dir> --collection <name>
# 또는
./bin/guidance-upload --config config/guidance.yaml --runbook-dir <dir> --collection <name>
```

업로드 helper는 runbook text를 embedding endpoint로 vector화한 뒤 Qdrant에 저장합니다. 이는 런타임 필수 기능이 아니라 초기 적재/검증용 도구입니다.

## Context compact

긴 진단 흐름에서는 tool 결과와 correction이 누적되어 LLM context limit에 도달할 수 있습니다. k8s-assistant는 context가 커지면 raw history를 계속 누적하지 않고 compact state로 전환합니다.

compact state에는 다음이 포함됩니다.

- 원 질문
- primary target과 namespace scope
- CRD discovery 결과와 guide ref/hash
- 수행한 절차와 순서
- 각 절차에서 얻은 단서
- result hash
- 다음 동작

추정 context 사용량이 모델 context limit의 80% 이상이면 compact가 실행됩니다. correction이나 guide injection만으로 낮은 token 사용량에서 compact하지 않습니다. provider가 context length 오류를 반환한 경우에도 compact 후 1회 재시도합니다. compact가 발생하면 터미널에 다음 형태의 안내가 출력됩니다.

```text
↻ context compacting: ...
✓ context compacted: ...
```

context limit은 모델명으로 추정합니다. 운영 환경에서 정확한 값을 지정하려면 다음 환경변수를 사용합니다.

```bash
export K8S_ASSISTANT_CONTEXT_LIMIT_TOKENS=32768
```

JSON ReAct shim 사용 시 모델이 최종 답변을 JSON code block 없이 plain text로 반환해도, k8s-assistant는 이를 shim parse error가 아니라 최종 답변으로 처리합니다.

## Prompt / tool context 관리

k8s-assistant는 runtime prompt를 section 단위로 조립합니다. core ReAct, output contract, language policy, target/scope 보존, command guideline은 항상 포함하고, read-only, guidance protocol, manifest generation, Cluster API guardrail은 현재 요청과 RAG 결과에 따라 조건부로 포함합니다.

tool schema는 안전성을 위해 pruning하지 않고 등록된 전체 tool set을 유지합니다. 대신 ToolProfile hash를 사용해 동일한 tool schema 조합을 캐싱/참조 가능한 단위로 관리합니다.

## guidance revise 논의

아직 확정되지 않은 설계 논점은 `docs/reviews/revise_troubleshooting.md`에 정리합니다.

현재 논의 중인 항목:

- 간단한 문제는 k8s-assistant ReAct 루프가 직접 처리하고, 불확실한 문제만 incident guidance/RAG를 호출할지 여부
- LLM self assessment를 이용해 incident guidance 호출 여부를 판단하는 방식
- incident guidance 결과와 LLM 자체 해결책이 충돌하지 않도록 최종 판단권을 어디에 둘지
- delete/recreate 작업 시 YAML export, runtime field 제거, 수정안 제시, 승인, apply/delete 순서를 어떻게 강제할지

## 프롬프트

프롬프트는 `[context|status] >>> ` 형식입니다.

### 형식
- **context**: 현재 Kubernetes Context (kubeconfig가 없으면 "none")
- **status**: Agent 및 설정 준비 상태
  - `✓`: 모든 것이 준비됨 (Agent 있음 + Kubeconfig 설정 + API Key 있음)
  - `⚠️ `: 일부 설정 누락 (API Key 또는 Kubeconfig 없음)

### 예시
```
[admin@kubernetes|✓] >>> pod 목록 보여줘
[admin@kubernetes|✓] >>> deployment를 2개 replica로 스케일해줘
[none|⚠️ ] >>> /kubeconfig  # kubeconfig 설정 필요
```

### 상태 해석
- `[admin|✓]`: 준비 완료, 자연어 명령어 사용 가능
- `[none|⚠️ ]`: 설정 부족, `/save` 전에 `/kubeconfig` 또는 API Key 설정 필요

## 로그

- **시스템 로그**: `~/.k8s-assistant/logs/k8s-assistant-YYYYMMDD.log`
- **대화 로그**: `--log-file` 옵션으로 지정
- **콘솔 시스템 로그**: 기본 비활성화, `--show-log-output`으로 활성화

```bash
./bin/k8s-assistant-linux-amd64 --log-file /tmp/conversation.log
./bin/k8s-assistant-linux-amd64 --show-log-output
```

## 명령어 라인 옵션

```bash
./bin/k8s-assistant-linux-amd64 \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig ~/.kube/config \
  --max-iterations 20 \
  --mcp-client \
  --show-tool-output \
  --read-only \
  --show-log-output \
  --log-file /tmp/chat.log
```

## 기본 동작

1. **배너 표시**
   - KINX 로고 및 AI 어시스턴트 정보
   - 현재 kubeconfig 경로 및 상태
   - Powered by Claude · Gemini · GPT · Ollama

2. **초기 프롬프트**
   - `[context|status] >>>` 형식의 프롬프트 표시
   - Agent 자동 초기화 (로그 숨김)

3. **대화 루프**
   - 자연어 명령어 입력 (예: "pod 상태 확인")
   - AI가 kubectl 명령어 실행 및 결과 분석
   - 변경 사항은 사용자 승인 후 적용

4. **메타 명령어**
   - `/` 입력 시 메타 명령 메뉴 자동 표시
   - kubeconfig, context, 설정 관리 가능
   - 변경사항은 `/save`로 저장

5. **화살표 키 지원**
   - ↑/↓: 이전 명령어 히스토리 탐색
   - ←/→: 커서 이동
   - history: `~/.k8s-assistant/history`

## 색상 설정

- **KINX 배너 텍스트**: Bright Green
- **Kubernetes 심볼**: Cyan
- **Tool 호출 (실행 중)**: Bright Magenta
- **Tool 결과**: Cyan
- **Context/강조**: Bright Magenta
- **경로/일반 정보**: Yellow
- **오류/미설정**: Bright Red
- **구분선/배경**: Green (Dim)

검정/흰색 배경 모두에서 가시성이 확보됩니다.

## 주요 기능

✅ **Kubernetes 관리**
- Pod, Deployment, Service 등 리소스 조회/수정
- Custom tools: helm, kustomize
- Multi-context 지원

✅ **AI 능력**
- Claude, Gemini, GPT-4 등 지원
- ReAct 루프 기반 자동 실행
- 민감정보 마스킹 (API Key, 비밀번호 등)

✅ **로깅**
- 대화 히스토리 저장
- 시스템 로그 자동 기록

## 환경 설정

### Mac에서 실행
```bash
make build
./bin/k8s-assistant
```

### 수동 Linux 크로스컴파일
```bash
GOOS=linux GOARCH=amd64 go build -o bin/k8s-assistant-linux-amd64 ./cmd/k8s-assistant
```

## 트러블슈팅

### Context가 보이지 않음
```bash
# kubeconfig 경로 확인
echo $KUBECONFIG
# 또는
cat ~/.kube/config | grep current-context
```

### 로그 위치 확인
```bash
ls -la ~/.k8s-assistant/logs/
```

### 명령어 히스토리
```bash
cat ~/.k8s-assistant/history
```

## 저작권

Created by kimnamgon
Powered by Claude · Gemini · GPT · Ollama
