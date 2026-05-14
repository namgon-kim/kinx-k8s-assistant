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
bin/troubleshooting-upload
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

`--mcp-client`는 `~/.k8s-assistant/mcp.yaml`에 선언된 서버만 kubectl-ai MCP 설정으로 동기화한 뒤, k8s-assistant의 Tool connector registry에 등록합니다. trouble-shooting은 내부 패키지로 실행되므로 MCP 서버 설정이 필요하지 않습니다.

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

## trouble-shooting 설정

trouble-shooting은 별도 서버를 실행하지 않습니다. k8s-assistant가 문제 감지 후 사용자 확인을 받고 내부 패키지로 runbook/RAG 검색과 조치 계획 생성을 수행합니다. Kubernetes 명령 실행은 k8s-assistant ReAct 루프와 승인 흐름이 담당합니다.

기본 설정 경로:

```text
~/.k8s-assistant/trouble-shooting.yaml
```

예시 설정 복사:

```bash
mkdir -p ~/.k8s-assistant
cp config/trouble-shooting.yaml ~/.k8s-assistant/trouble-shooting.yaml
```

주요 설정:

```yaml
trouble_shooting:
  rag:
    provider: qdrant
    embedding:
      url: http://1.201.177.120:4000
      model: bge-m3
    qdrant:
      url: http://localhost:6333
      collection: k8s_troubleshooting_runbooks_v1
    reranker:
      enabled: true
      url: http://1.201.177.120:4000
      model: bge-reranker-v2-m3
```

runbook을 Qdrant에 업로드:

```bash
./bin/troubleshooting-upload
# 또는
./bin/troubleshooting-upload --config config/trouble-shooting.yaml
```

업로드 helper는 runbook text를 embedding endpoint로 vector화한 뒤 Qdrant에 저장합니다. 이는 런타임 필수 기능이 아니라 초기 적재/검증용 도구입니다.

## trouble-shooting revise 논의

아직 확정되지 않은 설계 논점은 `docs/reviews/revise_troubleshooting.md`에 정리합니다.

현재 논의 중인 항목:

- 간단한 문제는 k8s-assistant ReAct 루프가 직접 처리하고, 불확실한 문제만 trouble-shooting/RAG를 호출할지 여부
- LLM self assessment를 이용해 trouble-shooting 호출 여부를 판단하는 방식
- trouble-shooting 결과와 LLM 자체 해결책이 충돌하지 않도록 최종 판단권을 어디에 둘지
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
