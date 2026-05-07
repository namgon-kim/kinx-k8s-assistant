# K8s-Assistant 사용 가이드

Kubernetes 클러스터를 자연어로 관리하는 AI 어시스턴트입니다.

## 설치 및 실행

### Linux 빌드
```bash
make build-linux
./bin/k8s-assistant-linux-amd64
```

### 설정

#### 1. Config 파일 (권장)
`~/.k8s-assistant/config.yaml`:
```yaml
# LLM 설정
llmprovider: openai
model: gpt-4o
apikey: sk-...                 # API 키 (선택사항, env가 우선)
endpoint: https://...          # API 엔드포인트 (선택사항, env가 우선)

# Kubernetes 설정
kubeconfig: ~/.kube/config

# 동작 설정
maxiterations: 20
sessionbackend: memory
showtooloutput: true
```

#### 2. 명령줄 옵션 (CLI 플래그)
```bash
./k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig ~/.kube/config \
  --max-iterations 20
```

#### 3. 환경 변수 (CLI 플래그 없을 때만 적용)
```bash
export KUBECONFIG=$HOME/.kube/config
export LLM_PROVIDER=openai
export MODEL=gpt-4o
```

**설정 우선순위:**
1. CLI 플래그 (`--kubeconfig`, `--model`, `--api-key`, 등) - 최우선
2. 환경 변수 (`OPENAI_API_KEY`, `OPENAI_ENDPOINT`, `LLM_PROVIDER`, `MODEL`) 
3. `~/.k8s-assistant/config.yaml` - 권장 설정 방식
4. 기본값 - 모든 설정이 없을 때

## API 키 설정 방법

### 방법 1: config.yaml에 직접 기록
```yaml
llmprovider: openai
model: gpt-4o
apikey: sk-...
endpoint: https://api.openai.com/v1  # 선택사항
```

### 방법 2: 환경 변수 사용 (권장 - CI/CD)
```bash
export OPENAI_API_KEY=sk-...
export OPENAI_ENDPOINT=https://api.openai.com/v1  # 선택사항
./k8s-assistant
```

### 방법 3: CLI 플래그 사용
```bash
./k8s-assistant --api-key sk-... --endpoint https://api.openai.com/v1
```

**주의:** 환경 변수가 config.yaml과 CLI 플래그보다 우선하므로, 민감한 정보는 환경 변수로 전달하는 것이 권장됩니다.

## 메타 명령어

메타 명령어는 `/` 로 시작합니다. `/` 입력 시 자동으로 메타 명령 메뉴가 표시됩니다.

### 메뉴 표시
```bash
>>> /              # 메타 명령 메뉴 표시
```

메뉴에서 번호(1-4)를 선택하거나 명령어를 직접 입력할 수 있습니다.

### 설정 조회
```bash
>>> /config        # 현재 LLM, Kubeconfig, Context 설정 표시
```

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

**주의:** 메타 명령(/kubeconfig, /kube-context 등)은 설정을 변경하지만 자동 저장되지 않습니다. 
변경 사항을 저장하려면 명시적으로 `/save` 명령을 실행해야 합니다.

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

```bash
./bin/k8s-assistant-linux-amd64 --log-file /tmp/conversation.log
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
