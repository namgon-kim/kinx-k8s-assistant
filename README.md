# kinx-k8s-assistant

Kubernetes AI 어시스턴트 - 자연어로 클러스터를 조작하고 트러블슈팅합니다.

## 아키텍처

```
k8s-assistant (Go 바이너리)
  ├── Orchestrator        컨텍스트 관리, Secret 마스킹, 응답 포맷, propose/commit
  │     └── kubectl-ai Agent (gopkg)   ReAct 루프, kubectl/helm/kustomize 실행
  │           └── MCP Client → log-analyzer-server (HTTP)
  │               ├── fetch_logs          로그 조회
  │               ├── analyze_pattern     패턴 탐지
  │               ├── rag_lookup          유사 사례 검색
  │               └── analyze_and_remediate  통합 분석
  └── CLI                사용자 입출력
```

## 의존성 설정

kubectl-ai를 Go 워크스페이스로 참조합니다.

```bash
# 1. kubectl-ai 클론 (kinx-k8s-assistant와 같은 레벨에)
make deps

# 디렉토리 구조
# workspaces/
# ├── kubectl-ai/          ← make deps가 클론
# └── kinx-k8s-assistant/  ← 이 프로젝트
```

## 빌드

```bash
# k8s-assistant 빌드
make build
# bin/k8s-assistant 생성

# log-analyzer-server 빌드
make build-log-analyzer
# bin/log-analyzer-server 생성
```

## 설정

### custom tools (helm, kustomize)

```bash
mkdir -p ~/.config/kubectl-ai
cp config/tools.yaml ~/.config/kubectl-ai/tools.yaml
```

### MCP 클라이언트 (log-analyzer 연동)

```bash
# 1. log-analyzer-server 시작 (별도 터미널)
./bin/log-analyzer-server --port 9090 --log-dir /var/log/filebeat

# 2. MCP 설정 복사
cp config/mcp.yaml ~/.config/kubectl-ai/mcp.yaml

# 3. k8s-assistant 실행 시 --mcp-client 플래그 사용
./bin/k8s-assistant --mcp-client --llm-provider openai --model gpt-4o
```

## 실행

```bash
# 기본 실행 (인터랙티브 모드)
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --kubeconfig /path/to/kubeconfig

# 단발성 쿼리
./bin/k8s-assistant --llm-provider openai --model gpt-4o \
  "모든 네임스페이스의 Pod 상태를 보여줘"

# log-analyzer MCP 연동 활성화
./bin/k8s-assistant --mcp-client \
  --llm-provider openai --model gpt-4o

# tool use shim 활성화 (native tool calling 미지원 모델용)
./bin/k8s-assistant \
  --llm-provider openai \
  --model nvidia/Llama-3.3-70B-Instruct-FP8 \
  --enable-tool-use-shim \
  --skip-verify-ssl
```

## 환경변수

| 변수 | 설명 |
|---|---|
| `KUBECONFIG` | kubeconfig 파일 경로 |
| `OPENAI_API_KEY` | OpenAI API 키 |
| `ANTHROPIC_API_KEY` | Anthropic API 키 |
| `GOOGLE_API_KEY` | Gemini API 키 |

## 프로젝트 구조

```
kinx-k8s-assistant/
├── cmd/
│   ├── k8s-assistant/main.go              CLI 진입점
│   └── log-analyzer-server/main.go        MCP 서버 진입점 (로그 분석)
├── internal/
│   ├── config/config.go                  설정 구조체
│   ├── orchestrator/                     메인 어시스턴트
│   │   ├── orchestrator.go               메인 루프
│   │   ├── context.go                    대화 컨텍스트
│   │   ├── masking.go                    Secret/민감정보 마스킹
│   │   ├── formatter.go                  한국어 응답 포맷
│   │   └── logger.go                     대화 로그 기록
│   ├── agent/setup.go                    kubectl-ai AgentWrapper
│   └── loganalyzer/                      로그 분석 엔진
│       ├── interface.go                  Analyzer 인터페이스
│       ├── server.go                     MCP HTTP 서버 (4개 도구)
│       ├── analyzer.go                   분석 파이프라인 구현
│       ├── fetcher.go                    Filebeat 로그 파서
│       ├── pattern.go                    패턴 탐지 (CrashLoop, OOMKilled 등)
│       ├── rag.go                        유사 사례 검색 (키워드 기반)
│       └── rag/runbooks/default.yaml     11가지 K8s 트러블슈팅 사례
├── prompts/system_ko.tmpl                한국어 시스템 프롬프트
├── config/
│   ├── tools.yaml                        helm, kustomize 도구 설정
│   └── mcp.yaml                          MCP 클라이언트 설정 (log-analyzer)
├── go.mod / go.sum                       의존성
├── go.work                               kubectl-ai 로컬 참조
└── Makefile
```

## 로그 분석 / 트러블슈팅

`log-analyzer-server`는 Pod 로그를 자동으로 분석하고 K8s 트러블슈팅 가이드를 제시합니다.

### MCP 도구 (4개)

1. **fetch_logs** — Filebeat에서 수집된 Pod/컨테이너 로그 조회
2. **analyze_pattern** — 이상 패턴 탐지
   - CrashLoopBackOff (반복 재시작)
   - OOMKilled (메모리 부족)
   - ErrorSpike (에러 로그 급증)
   - SlowLatency (응답 지연/timeout)
   - DiskFull (디스크 가득 참)
3. **rag_lookup** — 증상 기반 유사 장애 사례 검색 (11가지 Runbook)
4. **analyze_and_remediate** — 로그 분석 + 패턴 탐지 + 유사 사례 + 조치 방안 통합 제시

### 사용 예시

```bash
# 1. log-analyzer-server 시작
./bin/log-analyzer-server --port 9090 --log-dir /var/log/filebeat

# 2. k8s-assistant에서 사용 (--mcp-client 필수)
./bin/k8s-assistant --mcp-client --llm-provider openai --model gpt-4o

# 3. 자연어 쿼리
>>> nginx-deployment pod의 최근 에러를 분석해줘
>>> 메모리 부족으로 꺼지는 Pod이 있어. 어떻게 해야 해?
```

### 아키텍처

```
k8s-assistant (MCP 클라이언트)
  → log-analyzer-server (MCP 서버, http://localhost:9090/mcp)
    ├── [LogFetcher] Filebeat 로그 파일 읽기
    ├── [PatternDetector] 규칙 기반 패턴 탐지
    ├── [VectorStore] 키워드 기반 유사 사례 검색
    └── [AnalyzerImpl] 파이프라인 통합
```

### 향후 개선사항

- 실제 Filebeat 로그 파일 탐색 구현 (현재는 placeholder)
- OpenAI/Anthropic 임베딩으로 벡터 DB 업그레이드
- Prometheus 메트릭 기반 분석 추가
- 제한된 조치 자동 실행 (예: Pod 재시작, 리소스 조정)
