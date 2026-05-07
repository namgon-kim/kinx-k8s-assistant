# kinx-k8s-assistant

**Kubernetes AI 어시스턴트** — 자연어로 Kubernetes 클러스터를 조회하고 관리하는 Go 기반 CLI 도구입니다.

`kubectl-ai`의 Agent/ReAct 루프를 래핑하여 **한국어 지원**, **민감정보 자동 마스킹**, **위험 작업 승인 흐름**, **MCP 기반 실시간 로그 분석** 기능을 제공합니다.

## 주요 기능

### 대화형 Kubernetes 관리
- **자연어 기반 쿼리**: "모든 Pod의 상태를 보여줘", "nginx Pod의 로그를 최근 100줄 가져와" 같은 자연스러운 질문으로 클러스터 제어
- `kubectl-ai` 기본 도구 + `helm`, `kustomize` custom tool 자동 연동
- Agent/ReAct 루프: LLM이 필요한 도구를 자동으로 선택하고 결과를 해석

### 안전성 및 보안
- **위험 작업 실행 전 사용자 승인**: 리소스 삭제, 재시작 같은 변경 사항은 실행 전 사용자 검토 및 승인 필요
- **Secret/민감정보 자동 마스킹**: API 키, 암호, 토큰 등이 출력되기 전 `***` 마스킹
- **접근 제어 통합**: kubeconfig 권한 범위 내에서만 작동

### 사용자 경험
- **한국어 우선**: 시스템 프롬프트, 에러 메시지, 권고사항 모두 한국어
- **인터랙티브 모드**: readline 기반 대화 루프, 명령어 히스토리 저장
- **일회성 쿼리**: 한 번의 명령으로 답변 받고 종료 가능
- **선택적 대화 로그**: `--log-file` 옵션으로 모든 상호작용 기록 저장

### 지능형 로그 분석 (MCP 서버)
- **실시간 이상 패턴 탐지**: CrashLoopBackOff, OOMKilled, 에러 급증, 타임아웃, 디스크 부족 자동 감지
- **유사 장애 사례 검색**: 증상 기반 runbook 추천 (내장 또는 커스텀 가능)
- **통합 진단**: 로그 조회 → 패턴 탐지 → 원인 분석 → 조치 제안을 한 번에 수행

## 아키텍처

### 전체 데이터 흐름

```text
┌─────────────────────────────────────────────────────────────┐
│                    k8s-assistant CLI (Go)                   │
│                                                             │
│  User Input (한국어)                                        │
│      ↓                                                      │
│  Orchestrator                                              │
│  ├─ readline 입력 처리                                      │
│  ├─ 대화 컨텍스트 유지                                      │
│  ├─ 위험 작업 승인 흐름                                      │
│  ├─ Secret/민감정보 마스킹                                   │
│  ├─ 한국어 응답 포맷팅                                       │
│  └─ 대화 로그 기록 (선택)                                    │
│      ↓                                                      │
│  kubectl-ai Agent Wrapper                                  │
│  └─ ReAct 루프 (최대 20회 반복)                             │
│      ├─ LLM에게 사용자 쿼리 전달                            │
│      ├─ 도구 선택 및 파라미터 생성                           │
│      └─ 도구 실행 결과를 LLM에 피드백                        │
│          ↓                                                  │
│          Available Tools:                                  │
│          ├─ kubectl 기본 도구                               │
│          │  └─ get, describe, logs, port-forward, etc.   │
│          ├─ Custom Tools                                  │
│          │  ├─ helm (차트 배포, 업그레이드, 조회)            │
│          │  └─ kustomize (오버레이 적용, 구성 조회)           │
│          └─ MCP Client Tools (log-analyzer-server)         │
│             ├─ fetch_logs: 특정 Pod/Container 로그 조회      │
│             ├─ analyze_pattern: 로그 패턴 이상 탐지           │
│             ├─ rag_lookup: 증상 기반 runbook 추천             │
│             └─ analyze_and_remediate: 통합 분석 및 조치 제안   │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│              log-analyzer-server (별도 프로세스)             │
│                   HTTP MCP 서버 (포트 9090)                 │
│                                                             │
│  Pattern Detector                                          │
│  ├─ CrashLoopBackOff: "back-off", "backoff" 키워드 탐지   │
│  ├─ OOMKilled: "oomkilled" 키워드 탐지                     │
│  ├─ ErrorSpike: 100개 로그 윈도우에서 ERROR/FATAL 30% 초과 │
│  ├─ SlowLatency: "timeout", "deadline" 키워드 탐지         │
│  └─ DiskFull: "no space left", "disk full" 키워드 탐지    │
│                                                             │
│  RAG Vector Store (runbook 검색)                           │
│  ├─ SimpleKeywordStore: 키워드 기반 (현재 활용 중)         │
│  └─ ChromaDB + OpenAI Embeddings: 벡터 유사도 (준비됨)     │
│                                                             │
│  Log Fetcher                                               │
│  └─ Filebeat/JSON 로그 디렉토리에서 로그 조회               │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                 Kubernetes Cluster                          │
│  (kubeconfig로 접근하는 실제 클러스터)                       │
└─────────────────────────────────────────────────────────────┘
```

### 핵심 컴포넌트

#### Orchestrator (`internal/orchestrator/`)
- **역할**: CLI의 메인 제어 루프
- **기능**:
  - `readline` 기반 인터랙티브 입력 처리
  - kubectl-ai Agent를 래핑하여 메시지 흐름 제어
  - 대화 컨텍스트 관리 (멀티턴 대화 지원)
  - 위험 작업 감지 및 사용자 승인 요청
  - Secret/민감정보 마스킹
  - 응답 포맷팅 및 한국어 렌더링
  - 대화 내용 로깅 (선택)

#### Agent Wrapper (`internal/agent/setup.go`)
- **역할**: kubectl-ai와의 인터페이스
- **기능**:
  - kubectl, helm, kustomize 도구 초기화
  - MCP client 설정 (log-analyzer-server 연동)
  - LLM 프로바이더 및 모델 구성
  - Tool use shim 지원 (native tool calling이 없는 모델용)

#### Pattern Detector (`internal/loganalyzer/pattern.go`)
- **역할**: 로그에서 이상 패턴 자동 탐지
- **탐지 규칙**:
  | 패턴 | 판단 기준 | 심각도 |
  |------|--------|------|
  | **CrashLoopBackOff** | "back-off", "backoff" 포함 | Warning |
  | **OOMKilled** | "oomkilled" 포함 | **Critical** |
  | **ErrorSpike** | 100개 로그 윈도우에서 ERROR/FATAL ≥ 30% | Warning |
  | **SlowLatency** | "timeout", "deadline" 포함 | Warning |
  | **DiskFull** | "no space left", "disk full" 포함 | **Critical** |

#### RAG Vector Store (`internal/loganalyzer/rag/`)
- **현재 구현**: SimpleKeywordStore (키워드 기반 runbook 검색)
- **향후 예정**: ChromaDB + OpenAI Embeddings (의미론적 유사도 검색)
- **데이터**: `internal/loganalyzer/rag/runbooks/default.yaml` (YAML 포맷 runbook)

## 요구사항

### 필수 요구사항
- **Go 1.24 이상**: 프로젝트 빌드
- **Kubernetes 클러스터**: 접근 가능한 클러스터와 kubeconfig 파일
- **LLM API 키**: OpenAI, Anthropic, Google 등 지원되는 프로바이더 중 하나

### 선택 요구사항
- **helm, kustomize**: custom tool 사용할 경우 각 바이너리 필요
- **npx**: MCP `sequential-thinking` 서버 사용할 경우 필요
- **Filebeat 로그**: 로그 분석 기능 사용할 경우 Filebeat 또는 JSON 형식 로그 필요

## 빌드

### 빠른 시작

```bash
# 전체 빌드
make build

# 또는 개별 빌드
make build-k8s-assistant        # k8s-assistant CLI만
make build-log-analyzer          # log-analyzer-server만
```

생성 파일:
```
bin/k8s-assistant               # 메인 CLI 도구
bin/log-analyzer-server         # 로그 분석 MCP 서버
```

### 사용 가능한 Make targets

| Target | 설명 |
|--------|------|
| `make build` | 전체 바이너리 빌드 |
| `make build-k8s-assistant` | CLI만 빌드 |
| `make build-log-analyzer` | 로그 분석 서버만 빌드 |
| `make build-linux` | Linux 바이너리 빌드 (크로스 컴파일) |
| `make run` | k8s-assistant 실행 (개발용) |
| `make run-log-analyzer` | log-analyzer-server 실행 (개발용) |
| `make tidy` | Go 의존성 정리 |
| `make clean` | 빌드 산출물 제거 |

### 빌드 결과 확인

```bash
./bin/k8s-assistant version
./bin/log-analyzer-server --help
```

## 실행

### 기본 사용법

#### 1. 인터랙티브 모드 (권장)

대화형 CLI에서 여러 쿼리를 연속으로 실행할 수 있습니다.

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig ~/.kube/config
```

프롬프트가 표시되면 자연스러운 한국어로 질문합니다:
```bash
>>> 기본(default) 네임스페이스의 모든 Pod 상태를 보여줘
>>> nginx Pod의 최근 50줄 로그를 조회해
>>> exit
```

#### 2. 단발성 쿼리

한 번의 명령어로 질문하고 답변을 받고 종료합니다:

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  "모든 네임스페이스의 Pod 상태를 보여줘"
```

#### 3. 로그 분석 포함 (MCP 클라이언트)

log-analyzer-server가 실행 중일 때 로그 분석 도구를 활용합니다:

```bash
# 별도 터미널 1: log-analyzer-server 시작
./bin/log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --runbook-dir internal/loganalyzer/rag/runbooks

# 별도 터미널 2: k8s-assistant with MCP
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --mcp-client
```

이제 "nginx Pod이 자꾸 재시작돼. 로그를 분석해서 원인을 찾아줘" 같은 질문에 자동으로 로그 조회 → 패턴 탐지 → 유사 사례 검색 → 해결책 제안까지 진행됩니다.

#### 4. Tool use shim (선택 사항)

OpenAI 호환 모델 중 native tool calling을 지원하지 않는 경우:

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model nvidia/Llama-3.3-70B-Instruct-FP8 \
  --enable-tool-use-shim \
  --skip-verify-ssl
```

### 일반적인 사용 시나리오

#### 시나리오 1: Pod 문제 진단
```bash
>>> app-pod가 계속 재시작돼. 무슨 문제야?
(자동으로 로그 조회 → 패턴 분석 → 원인 제시)

>>> 메모리가 부족한 것 같은데, 현재 메모리 사용량이 얼마야?
(Pod의 메모리 리소스 상태 조회)

>>> 메모리 제한을 2Gi로 올려줘
(사용자 승인 후 리소스 업데이트)
```

#### 시나리오 2: 배포 관리
```bash
>>> nginx 차트의 최신 버전이 뭐야?
(helm repo를 검색해 최신 버전 확인)

>>> nginx를 1.25 버전으로 업그레이드해줘
(사용자 승인 후 helm upgrade 실행)

>>> 현재 배포된 차트들 목록을 보여줘
(helm list 조회)
```

#### 시나리오 3: 리소스 모니터링
```bash
>>> 네임스페이스별로 CPU 상위 3개 Pod을 보여줘
(top 명령 또는 metrics-server 조회)

>>> 지난 1시간 동안 재시작된 Pod들이 있어?
(이벤트 로그 조회)
```

### 버전 확인

```bash
./bin/k8s-assistant version
```

## CLI 옵션

### k8s-assistant 옵션

| 옵션 | 기본값 | 설명 |
|------|------|------|
| `--llm-provider` | `openai` | LLM 프로바이더 (`openai`, `anthropic`, `google` 등) |
| `--model` | 없음 | 사용할 모델명 (예: `gpt-4o`, `claude-3-5-sonnet-20241022`) |
| `--kubeconfig` | `$KUBECONFIG` 또는 `~/.kube/config` | kubeconfig 파일 경로 |
| `--skip-verify-ssl` | `false` | LLM 프로바이더 SSL 인증서 검증 생략 |
| `--enable-tool-use-shim` | `false` | Tool use shim 활성화 (native tool calling 미지원 모델용) |
| `--mcp-client` | `false` | MCP client 활성화 (log-analyzer-server 연동) |
| `--max-iterations` | `20` | Agent ReAct 루프 최대 반복 횟수 (무한 루프 방지) |
| `--show-tool-output` | `false` | 도구 실행 결과를 사용자에게 표시 (디버깅용) |
| `--prompt-template` | 자동 탐색 | 한국어 시스템 프롬프트 템플릿 경로 |
| `--session-backend` | `memory` | 세션 저장 방식 (`memory`=RAM, `filesystem`=디스크) |
| `--log-file` | 없음 | 대화 로그 저장 경로 (선택, 감사/분석용) |

### 사용 예시

```bash
# OpenAI gpt-4o로 기본 설정
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o

# Anthropic Claude로 로그 분석 포함
./bin/k8s-assistant \
  --llm-provider anthropic \
  --model claude-3-5-sonnet-20241022 \
  --mcp-client

# 대화 기록 저장, 도구 출력 표시 (디버깅)
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --log-file ./conversation.log \
  --show-tool-output

# 커스텀 kubeconfig, 최대 반복 횟수 제한
./bin/k8s-assistant \
  --kubeconfig /etc/kubernetes/config \
  --max-iterations 10
```

## 환경변수

| 변수 | 설명 | 예시 |
|------|------|------|
| `KUBECONFIG` | kubeconfig 파일 경로 (CLI 옵션으로도 지정 가능) | `/home/user/.kube/config` |
| `OPENAI_API_KEY` | OpenAI API 키 (--llm-provider=openai 사용 시) | `sk-...` |
| `ANTHROPIC_API_KEY` | Anthropic API 키 (--llm-provider=anthropic 사용 시) | `sk-ant-...` |
| `GOOGLE_API_KEY` | Google Gemini API 키 (--llm-provider=google 사용 시) | `AIza...` |
| `XDG_CONFIG_HOME` | custom tool 설정 파일 탐색 디렉토리 | `~/.config` |

### 환경변수 설정

```bash
# .bashrc 또는 .zshrc에 추가
export KUBECONFIG=~/.kube/config
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...

# 또는 명령어 실행 전에 직접 설정
OPENAI_API_KEY=sk-... ./bin/k8s-assistant --llm-provider openai --model gpt-4o
```

## 설정

### 1. Custom Tools 설정 (helm, kustomize)

kubectl-ai에서 `helm`, `kustomize`를 도구로 사용하려면 설정 파일을 복사합니다.

```bash
mkdir -p ~/.config/kubectl-ai
cp config/tools.yaml ~/.config/kubectl-ai/tools.yaml
```

**탐색 경로** (우선순위 순):
1. `~/.config/kubectl-ai/tools.yaml`
2. `$XDG_CONFIG_HOME/kubectl-ai/tools.yaml`

**config/tools.yaml 예시**:
```yaml
tools:
  - name: helm
    description: Helm 패키지 매니저
    binary: helm
  - name: kustomize
    description: Kustomize 설정 관리
    binary: kustomize
```

### 2. MCP Client 설정 (로그 분석 서버 연동)

log-analyzer-server를 kubectl-ai와 연동하려면:

```bash
mkdir -p ~/.config/kubectl-ai
cp config/mcp.yaml ~/.config/kubectl-ai/mcp.yaml
```

**탐색 경로**: `~/.config/kubectl-ai/mcp.yaml`

**config/mcp.yaml 기본 내용**:
```yaml
servers:
  log-analyzer:
    type: http
    url: http://localhost:9090/mcp

  sequential-thinking:
    type: stdio
    command: npx
    args:
      - -y
      - '@modelcontextprotocol/server-sequential-thinking'
```

**사용 가능한 MCP 서버**:
- **log-analyzer**: 로그 분석, 패턴 탐지, runbook 검색 (자체 구현)
- **sequential-thinking**: 단계적 추론 (Anthropic 제공)
- 기타: 필요에 따라 추가 가능

### 3. 시스템 프롬프트 (한국어)

기본 한국어 프롬프트는 `prompts/system_ko.tmpl`에 있습니다.

**자동 탐색 경로** (우선순위 순):
1. `--prompt-template` 옵션으로 지정한 경로
2. 실행 파일 기준 `../prompts/system_ko.tmpl`
3. 실행 파일 기준 `../../prompts/system_ko.tmpl`

커스텀 프롬프트를 사용하려면:
```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --prompt-template /path/to/custom_prompt.tmpl
```

## log-analyzer-server

Kubernetes 환경에서 Pod 로그를 분석하는 MCP(Model Context Protocol) 서버입니다. k8s-assistant와 별도 프로세스로 실행되어 HTTP/MCP 인터페이스를 제공합니다.

### 빠른 시작

```bash
# 터미널 1: log-analyzer-server 시작
./bin/log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --runbook-dir internal/loganalyzer/rag/runbooks

# 터미널 2: k8s-assistant와 연동
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --mcp-client
```

### 옵션

| 옵션 | 기본값 | 설명 |
|---------|--------|------|
| `--port` | `9090` | MCP HTTP 서버 포트 |
| `--log-dir` | `/var/log/filebeat` | Filebeat 로그 디렉터리 |
| `--runbook-dir` | 자동 탐색 | Runbook YAML 파일 디렉터리 |

### 제공 MCP 도구

k8s-assistant의 Agent가 다음 도구를 자동으로 사용할 수 있습니다:

#### 1. `fetch_logs`
Pod/Container의 로그를 조회합니다.

**파라미터**:
- `namespace`: Pod이 속한 네임스페이스 (기본: `default`)
- `pod`: Pod 이름
- `container`: Container 이름 (선택, 여러 컨테이너 중 특정 컨테이너)
- `lines`: 조회할 로그 라인 수 (선택, 기본: 100)

**사용 예**:
```bash
Agent: "nginx Pod의 최근 50줄 로그를 조회하겠습니다"
→ fetch_logs(namespace="default", pod="nginx", lines=50)
```

#### 2. `analyze_pattern`
로그에서 이상 패턴을 자동으로 탐지합니다.

**파라미터**:
- `logs`: 분석할 로그 배열
- `pod_name`: Pod 이름 (컨텍스트용)
- `namespace`: 네임스페이스 (선택)

**반환값**: 탐지된 패턴 목록 + 심각도 + 요약
- 패턴 유형: CrashLoopBackOff, OOMKilled, ErrorSpike, SlowLatency, DiskFull
- 심각도: info, warning, critical

#### 3. `rag_lookup`
증상 설명을 기반으로 유사한 장애 사례(runbook)를 검색합니다.

**파라미터**:
- `symptom`: 증상 설명 (예: "Pod이 반복적으로 재시작됨")
- `top_k`: 상위 결과 개수 (선택, 기본: 3)

**반환값**: 매칭된 runbook 목록
- 각 runbook은 제목, 원인, 해결책, 출처 포함

#### 4. `analyze_and_remediate` (통합 분석)
로그 조회 → 패턴 탐지 → 유사 사례 검색 → 조치 제안까지 한 번에 수행합니다.

**파라미터**:
- `namespace`: 네임스페이스
- `pod`: Pod 이름
- `lines`: 로그 라인 수 (선택)

**반환값**: 종합 분석 리포트
```json
{
  "logs": [...],
  "patterns": [...],
  "similar_cases": [...],
  "recommendation": "..."
}
```

### 패턴 탐지 규칙

로그 분석 엔진이 자동으로 감지하는 패턴:

| 패턴 | 탐지 규칙 | 심각도 | 의미 |
|------|---------|------|------|
| **CrashLoopBackOff** | 로그에 "back-off" 또는 "backoff" 포함 | Warning | Pod이 계속 재시작 중 |
| **OOMKilled** | 로그에 "oomkilled" 포함 | **Critical** | 메모리 부족으로 종료 |
| **ErrorSpike** | 100개 로그 윈도우에서 ERROR/FATAL ≥ 30% | Warning | 에러 비율 급증 |
| **SlowLatency** | 로그에 "timeout" 또는 "deadline" 포함 | Warning | 응답 시간 초과 |
| **DiskFull** | 로그에 "no space left" 또는 "disk full" 포함 | **Critical** | 디스크 용량 부족 |

### Runbook 관리

Runbook은 YAML 형식으로 정의되며, `internal/loganalyzer/rag/runbooks/default.yaml`에 저장됩니다.

**Runbook YAML 형식**:
```yaml
cases:
  - title: "CrashLoopBackOff 진단"
    cause: "Pod이 시작 직후 자꾸 종료됨 (유효성 검사 실패, 설정 오류 등)"
    resolution: |
      1. Pod 이벤트 조회: kubectl describe pod <pod>
      2. 로그 확인: kubectl logs <pod> --previous
      3. 설정 검증: helm template 또는 kubectl apply --dry-run
      4. 리소스 확인: 메모리/CPU 부족 여부
    source: "Kubernetes 공식 문서"
  - title: "OOMKilled 대응"
    cause: "메모리 제한(limit)을 초과하여 Pod 강제 종료"
    resolution: |
      1. 현재 메모리 사용량 확인: kubectl top pod <pod>
      2. 메모리 제한 상향: kubectl set resources pod <pod> --limits=memory=2Gi
      3. 또는 HPA 설정으로 자동 확장
    source: "내부 운영 경험"
```

**커스텀 Runbook 추가**:
1. `internal/loganalyzer/rag/runbooks/` 디렉토리에 YAML 파일 생성
2. `default.yaml` 형식에 맞춰 작성
3. log-analyzer-server 재시작

### 현재 상태 및 제한사항

#### ✅ 준비 완료
- 패턴 탐지 엔진 (정규식 기반)
- MCP 서버 인터페이스
- 심각도 판정 로직
- Runbook 인덱싱 (키워드 기반)

#### ⚠️ 주의 사항
- **Filebeat Fetcher**: 현재 실제 파일 시스템 로그 읽기 구현이 없어 빈 결과 반환
  - 대신 k8s-assistant의 `fetch_logs` 도구와 `analyze_and_remediate`를 사용하세요
- **벡터 DB**: Chroma/OpenAI embedding 코드는 준비되어 있지만 아직 활성화 안 됨
  - 현재는 키워드 기반 검색만 지원

#### 📋 향후 개선 예정사항

1. **Filebeat 로그 직접 읽기**: 파일 시스템에서 실제 로그 조회
2. **벡터 DB 업그레이드**: 의미론적 유사도 기반 runbook 검색
3. **Prometheus 연동**: 메트릭 기반 성능 분석
4. **자동 조치 실행**: 사용자 승인 후 자동 복구
   - Pod 재시작
   - 리소스 조정 (메모리, CPU 제한)
   - 설정 롤백
5. **통합 대시보드**: 로그, 메트릭, 이벤트를 한눈에 보기

## 프로젝트 구조

```text
kinx-k8s-assistant/
├── cmd/                                    # 실행 가능한 바이너리 진입점
│   ├── k8s-assistant/
│   │   └── main.go                        # k8s-assistant CLI 진입점
│   └── log-analyzer-server/
│       └── main.go                        # log-analyzer-server 진입점
│
├── config/                                 # 기본 설정 파일 (~ 복사)
│   ├── mcp.yaml                           # MCP 서버 설정 (log-analyzer, sequential-thinking)
│   └── tools.yaml                         # kubectl-ai custom tools 설정 (helm, kustomize)
│
├── internal/                               # 핵심 로직 (비공개 패키지)
│   │
│   ├── agent/
│   │   └── setup.go                       # kubectl-ai agent 초기화, 도구 설정
│   │
│   ├── config/
│   │   └── config.go                      # CLI 옵션 파싱, 환경변수 처리
│   │
│   ├── orchestrator/                      # 메인 컨트롤 루프 및 UX
│   │   ├── orchestrator.go                # 대화 루프, 메시지 흐름 제어
│   │   ├── context.go                     # 대화 컨텍스트 관리 (멀티턴 지원)
│   │   ├── formatter.go                   # 응답 포맷팅, 마크다운 렌더링
│   │   ├── logger.go                      # 대화 로그 파일 기록
│   │   ├── masking.go                     # Secret/민감정보 자동 마스킹
│   │   ├── colors.go                      # 한국어 색상 매핑
│   │   └── banner.go                      # 시작 배너 출력
│   │
│   └── loganalyzer/                       # 로그 분석 엔진 (MCP 서버)
│       ├── server.go                      # MCP HTTP 서버
│       ├── analyzer.go                    # 통합 분석 오케스트레이션
│       ├── interface.go                   # 로그/패턴/RAG 인터페이스 정의
│       ├── pattern.go                     # 패턴 탐지 규칙 엔진
│       ├── fetcher.go                     # 로그 조회 (Filebeat, JSON 형식)
│       ├── rag.go                         # 유사 사례 검색 인터페이스
│       │
│       ├── pattern/                       # 패턴 탐지 구현체
│       │   └── detector.go
│       │
│       ├── fetcher/                       # 로그 소스별 fetcher 구현
│       │   └── fetcher.go
│       │
│       └── rag/                           # 벡터 저장소 및 임베딩
│           ├── store.go                   # SimpleKeywordStore (현재 활용 중)
│           ├── chromem.go                 # Chroma 기반 벡터 DB (준비됨)
│           ├── openai_embedder.go         # OpenAI embedding (준비됨)
│           └── runbooks/
│               └── default.yaml           # 장애 사례 및 조치 방법 (YAML)
│
├── prompts/
│   └── system_ko.tmpl                     # 한국어 시스템 프롬프트 템플릿
│
├── go.mod                                  # Go 모듈 정의
├── go.sum                                  # 의존성 체크섬
├── Makefile                                # 빌드 자동화
├── README.md                               # 이 파일
└── .gitignore
```

### 핵심 패키지 역할

| 패키지 | 역할 | 주요 기능 |
|--------|------|---------|
| `cmd/k8s-assistant` | CLI 진입점 | 플래그 파싱, Orchestrator 시작 |
| `cmd/log-analyzer-server` | 로그 분석 서버 | MCP HTTP 서버 실행 |
| `internal/agent` | kubectl-ai 래퍼 | 도구 초기화, Agent 생성 |
| `internal/config` | 설정 관리 | CLI 옵션, 환경변수 처리 |
| `internal/orchestrator` | 메인 루프 | readline, 메시지 흐름, 마스킹, 포맷팅 |
| `internal/loganalyzer` | 로그 분석 | 패턴 탐지, runbook 검색, MCP 도구 제공 |

## 개발 가이드

### 의존성 관리

#### 주요 의존성
```bash
go list -m all | grep -E "kubectl-ai|openai|anthropic|chromem"
```

**핵심 라이브러리**:
- `github.com/GoogleCloudPlatform/kubectl-ai v0.0.31`: kubectl 통합, Agent/ReAct 루프
- `github.com/mark3labs/mcp-go v0.41.1`: MCP 프로토콜 구현
- `github.com/openai/openai-go v1.12.0`: OpenAI API 클라이언트
- `github.com/philippgille/chromem-go v0.4.0`: Chroma 벡터 DB 클라이언트 (준비됨)
- `github.com/chzyer/readline v1.5.1`: readline 입력 처리

**주의**: `kubectl-ai`는 Go module로 사용되며, 현재 저장소에는 `go.work` 파일이 없습니다. 로컬 수정이 필요하면 `go mod replace` 또는 `go.work`를 사용하세요.

### 로컬 설정 및 출력

#### 로그 디렉토리
```bash
# klog 출력 (kubectl-ai 관련)
~/.k8s-assistant/logs/k8s-assistant-YYYYMMDD.log

# Readline 히스토리
~/.k8s-assistant/history
```

#### 사용자 설정 디렉토리
```bash
~/.config/kubectl-ai/
├── tools.yaml         # custom tools (helm, kustomize)
└── mcp.yaml          # MCP 서버 설정
```

### 시스템 프롬프트 탐색

한국어 프롬프트 탐색 순서:
1. `--prompt-template` 옵션으로 지정한 경로
2. 실행 파일 기준 `../prompts/system_ko.tmpl`
3. 실행 파일 기준 `../../prompts/system_ko.tmpl`
4. 현재 디렉토리 기준 `prompts/system_ko.tmpl`

### 개발 팁

#### 디버깅
```bash
# 도구 실행 결과 표시 (디버깅용)
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --show-tool-output

# 대화 로그 저장
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --log-file debug.log
```

#### 테스트
```bash
# 단위 테스트
go test ./...

# 특정 패키지만
go test ./internal/loganalyzer/...

# 커버리지 리포트
go test -cover ./...
```

#### 포맷팅 및 린팅
```bash
# 코드 포맷팅
go fmt ./...

# 정적 분석 (필요 시 golangci-lint 설치)
golangci-lint run

# 의존성 정리
go mod tidy
```

### 확장 포인트

#### 1. 새로운 패턴 탐지 규칙 추가
파일: `internal/loganalyzer/pattern.go`

```go
func (d *PatternDetector) detectMyPattern(logs []LogEntry) []DetectedPattern {
    // 로그 분석 로직
    // LogEntry.Message, LogEntry.Level 활용
}
```

#### 2. 새로운 Runbook 추가
파일: `internal/loganalyzer/rag/runbooks/default.yaml`

```yaml
cases:
  - title: "내 장애 케이스"
    cause: "원인 설명"
    resolution: "해결 방법"
    source: "출처"
```

#### 3. 벡터 DB 업그레이드 (Chroma)
파일: `internal/loganalyzer/rag/chromem.go`

이미 기본 구현이 있으며, `cmd/log-analyzer-server/main.go`에서 활성화하면 됩니다:
```go
// 현재 (키워드 기반)
store := loganalyzer.NewSimpleKeywordStore()

// 변경 (벡터 기반)
// store := loganalyzer.NewChromaStore("http://localhost:8000")
```

#### 4. 새로운 LLM 프로바이더 추가
파일: `internal/agent/setup.go`

```go
case "myai":
    // MyAI 클라이언트 초기화 로직
```

### 코드 구조 가이드

#### 메시지 흐름 (Orchestrator)
```text
User Input
  ↓
readline 입력
  ↓
agentWrap.SendMessage()
  ↓
Agent (ReAct 루프)
  ↓
Tool 실행 (kubectl, helm, MCP)
  ↓
Agent 최종 응답
  ↓
Formatter (마크다운 렌더링)
  ↓
Masking (민감정보 제거)
  ↓
User Output (화면 출력)
```

#### Tool 추가 방법
`internal/agent/setup.go`에서 `tools.Register()` 호출:
```go
tools.Register("my-tool", "tool-description", myToolHandler)
```

### 성능 최적화

- **Agent 반복 제한**: `--max-iterations` (기본 20)로 무한 루프 방지
- **로그 조회 제한**: `--log-file`로 불필요한 디스크 I/O 최소화
- **MCP 연결 풀**: 다중 요청 시 HTTP 연결 재사용

### 보안 고려사항

- **Secret 마스킹**: `orchestrator/masking.go`에서 정규식 기반 탐지
- **kubeconfig 관리**: `$KUBECONFIG` 환경변수 또는 `--kubeconfig` 옵션으로 명시적 관리
- **API 키 노출 방지**: `--show-tool-output` 사용 시 주의
- **로그 파일 권한**: 대화 로그에 민감정보 포함 가능 → 파일 권한 적절히 설정

### 릴리스 체크리스트

- [ ] `go mod tidy` 실행
- [ ] 모든 테스트 통과 (`go test ./...`)
- [ ] 마크다운 파일 린팅 (README.md, 코멘트)
- [ ] 버전 번호 업데이트 (if applicable)
- [ ] 변경 로그 작성
- [ ] 태그 생성 (`git tag v0.x.x`)

## 문제 해결

### Pod 로그가 안 나와요
- **Filebeat 로그 경로 확인**: `--log-dir` 옵션에서 지정한 디렉토리가 존재하는지, 읽기 권한이 있는지 확인
- **현재 제한사항**: Filebeat fetcher가 아직 실제 파일 읽기를 구현하지 않았습니다. 대신 k8s-assistant에서 직접 `kubectl logs` 명령을 통해 로그를 조회합니다.

### log-analyzer-server 연결 안 됨
```bash
# 1. 포트 확인
netstat -tlnp | grep 9090

# 2. MCP 클라이언트 설정 확인
cat ~/.config/kubectl-ai/mcp.yaml

# 3. log-analyzer-server 로그 확인
./bin/log-analyzer-server --port 9090 &
tail -f ~/.k8s-assistant/logs/...
```

### Secret 마스킹이 작동하지 않음
- `internal/orchestrator/masking.go`의 정규식 규칙 확인
- 새로운 패턴 추가 필요 시 코드 수정 후 재빌드

### kubeconfig 경로 문제
```bash
# 환경변수 확인
echo $KUBECONFIG

# kubeconfig 검증
kubectl config view

# CLI에서 명시적 지정
./bin/k8s-assistant --kubeconfig /path/to/kubeconfig
```

### Agent가 도구를 선택하지 않음
- `--show-tool-output` 옵션으로 디버깅 (도구 호출 과정 확인)
- `--max-iterations` 확인 (너무 낮으면 도구 호출 중단)
- LLM 모델 버전 확인 (tool use를 지원하는 모델인지)

## 기여 가이드

### 패치/버그 리포트
1. 현재 상태 재현 방법 명시
2. 예상 동작 vs 실제 동작 설명
3. 환경 정보 (OS, Go 버전, kubeconfig 설정 등)

### 기능 요청
1. 사용 사례 설명
2. 구현 방식 제안 (있으면)
3. 우선순위 (필수/선택/향후)

### 코드 기여
1. Fork + feature branch 생성
2. 코드 작성 (go fmt, go vet 적용)
3. 테스트 추가/수정
4. Pull request 생성

## 참고 자료

### Kubernetes
- [kubectl 공식 문서](https://kubernetes.io/docs/reference/kubectl/)
- [Kubernetes API 서버](https://kubernetes.io/docs/concepts/overview/kubernetes-api/)

### 관련 프로젝트
- [kubectl-ai](https://github.com/GoogleCloudPlatform/kubectl-ai): Kubernetes Agent 핵심 라이브러리
- [Model Context Protocol (MCP)](https://modelcontextprotocol.io/): 클라우드 모델 통합 표준
- [Chroma](https://www.trychroma.com/): 벡터 데이터베이스

### LLM 프로바이더
- [OpenAI API](https://platform.openai.com/docs/)
- [Anthropic Claude](https://www.anthropic.com/)
- [Google Gemini](https://ai.google.dev/)

## 라이센스

[프로젝트 라이센스 명시 예정]

## 기본 정보

- **작성자**: 김남곤
- **프로젝트**: kinx-k8s-assistant
- **상태**: Active Development
- **마지막 업데이트**: 2026년 5월

## FAQ

### Q: 사설 Kubernetes 클러스터에서 사용할 수 있나요?
**A**: 네, kubeconfig 권한 범위 내에서 모든 클러스터를 지원합니다. `--kubeconfig` 옵션으로 클러스터별 설정 파일을 지정하면 됩니다.

### Q: 여러 네임스페이스를 동시에 모니터링할 수 있나요?
**A**: 네, 대화 중에 "쿠버네티스의 모든 네임스페이스에서..." 같은 질문을 하면 Agent가 전체 클러스터를 검색합니다. 단, 접근 권한은 kubeconfig에 따라 결정됩니다.

### Q: 자동 복구(auto-remediate) 기능이 있나요?
**A**: 현재는 권고사항 제시만 제공합니다. 자동 복구는 향후 릴리스에 추가될 예정입니다.

### Q: 로그 데이터가 외부로 전송되나요?
**A**:
- **k8s-assistant** → **LLM 프로바이더**: 쿼리와 분석 결과가 LLM에 전달됩니다 (선택한 프로바이더의 정책 참조)
- **log-analyzer-server**: 로컬 호스트에서만 실행 (외부 전송 없음, 단 embedding 사용 시 OpenAI/Anthropic으로 전송)
- **민감정보 마스킹**: 자동으로 적용되지만, 모든 패턴을 감지할 수 없으므로 민감한 환경에서는 주의가 필요합니다.

### Q: 방화벽 뒤의 격리된 환경에서 사용할 수 있나요?
**A**:
- LLM API가 필요하므로 LLM 프로바이더로의 아웃바운드 연결이 필요합니다
- 로컬 LLM(Ollama 등)을 사용하려면 추가 개발이 필요합니다
- log-analyzer-server는 로컬에서만 실행되므로 제약이 없습니다
