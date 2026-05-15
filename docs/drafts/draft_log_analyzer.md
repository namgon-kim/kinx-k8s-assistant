# log-analyzer 설계 draft

## 현재 상태 요약

현재 `log-analyzer-server`는 인터페이스와 MCP 도구 등록은 완성돼 있지만, 실제 구현은 대부분 placeholder다. 향후 방향은 별도 서버 중심이 아니라 `internal/loganalyzer` 패키지와 toolset adapter를 k8s-assistant 내부에서 직접 호출하는 구조다.

| 컴포넌트 | 상태 |
|---|---|
| `FetchLogs` | TODO: 파일 탐색 미구현, 빈 결과 반환 |
| `AnalyzePattern` | 구현됨 (CrashLoop, OOM, ErrorSpike 등 정형 패턴) |
| `RAGLookup` | 제거 대상. log-analyzer는 RAG를 사용하지 않음 |
| `AnalyzeAndRemediate` | 제거 대상. 조치 계획은 trouble-shooting이 담당 |

Kubernetes 실시간 로그는 k8s-assistant ReAct 루프의 `kubectl logs` 실행 흐름으로도 가져올 수 있지만, log-analyzer는 파일 기반 로그와 toolset(Loki 등)을 통한 로그 수집을 모두 지원하는 방향으로 확장한다. 원문 로그는 LLM context에 직접 넣지 않고 artifact로 저장한 뒤, 요약과 샘플만 반환한다.

---

## holmesGPT 분석

### 구조

- Python 84%, YAML 기반 toolset 정의
- `holmes/core/tool_calling_llm.py` (60KB): 내부 ReAct 루프 구현체
- `holmes/core/tools.py` (48KB): Tool/Toolset 기본 클래스
- `holmes/plugins/toolsets/`: 각 observability source별 디렉터리

확인된 toolset 목록:

| toolset | 위치 | 비고 |
|---|---|---|
| Prometheus | `toolsets/prometheus/prometheus.py` (94KB) | PromQL 생성/실행, alert 조회 |
| Grafana | `toolsets/grafana/toolset_grafana.py` (30KB) | dashboard, Tempo trace 포함 |
| Loki | `toolsets/grafana/loki/` | Grafana 하위 모듈 |
| Elasticsearch/OpenSearch | `toolsets/elasticsearch/elasticsearch.py` (36KB) | PPL query 포함 |
| Zabbix | 미존재 | holmesGPT에 없음 |
| Kafka, MongoDB, Datadog 등 | 별도 디렉터리 | 많음 |

toolset은 YAML 설정 + Python 구현 쌍으로 구성되며, 독립 패키지가 아닌 holmesGPT 모노레포 내부 모듈이다.

### 패키지 가져오기 가능성

holmesGPT는 `pyproject.toml`이 있어 pip 설치가 가능하지만, **라이브러리 패키지로 설계된 것이 아니다**. 주요 문제:

1. **언어 불일치**: holmesGPT는 Python, k8s-assistant는 Go. 직접 임포트 불가.
2. **CLI 설계**: entry point가 `holmes_cli.py`이며, 공개 API surface가 없다.
3. **CGO/gRPC 브릿지**: Python 코드를 Go에서 호출하려면 gRPC 서버나 subprocess 래핑이 필요하며 복잡도가 크게 증가한다.
4. **ReAct 중첩**: holmesGPT 내부에 자체 ReAct 루프(`tool_calling_llm.py`)가 있어, k8s-assistant ReAct → holmesGPT ReAct 중첩 구조가 된다.

**결론: holmesGPT 패키지 직접 임포트는 불가하고 적합하지 않다.**

### holmesGPT subprocess 실행 또는 MCP 연동

대안으로 holmesGPT를 subprocess로 실행하거나, holmesGPT가 MCP 서버 기능을 지원한다면 MCP로 연동하는 방법이 있다.

- **subprocess 방식**: 입력/출력 제어 어려움, 세션 유지 불가, 오버헤드 큼
- **MCP 연동**: holmesGPT 0.5+ 에서 MCP 지원 추가됐으나, 운영 환경 추가 프로세스 관리 필요
- **ReAct 중첩 문제는 동일하게 발생**: k8s-assistant가 holmesGPT를 외부 tool로 호출하면 holmesGPT 내부에서 다시 ReAct를 수행한다. 결과는 tool call result로만 받으므로 동작은 하지만, 2단계 LLM 호출 구조가 된다.

---

## observability toolset 오픈소스 현황 (Go)

holmesGPT toolset과 대응하는 Go 클라이언트:

| 소스 | Go 패키지 | 상태 |
|---|---|---|
| Prometheus | `github.com/prometheus/client_golang/api/prometheus/v1` | 공식, 안정 |
| Grafana API | `github.com/grafana/grafana-openapi-client-go` 또는 HTTP API 직접 호출 | 공식, 안정 |
| Loki | oki HTTP API 직접 호출 + 내부 Go wrapper 구현, `github.com/grafana/loki` 일부 | 공식 클라이언트 없음, REST 직접 |
| Elasticsearch | `github.com/elastic/go-elasticsearch/v8` | 공식, 안정 |
| OpenSearch | `github.com/opensearch-project/opensearch-go/v4` | 공식, 안정 |
| Zabbix | REST API 직접 | 비공식, REST 직접 권장 |

모두 OSS이며 Go 모듈로 사용 가능하다. holmesGPT처럼 toolset-per-source로 log-analyzer 내부에 구현할 수 있다.

---

## 통합 전략 비교

| 전략 | 장점 | 단점 |
|---|---|---|
| holmesGPT 패키지 임포트 | 30+ toolset 즉시 활용 | Python/Go 언어 불일치, 불가능 |
| holmesGPT MCP 서버 연동 | toolset 재구현 없음 | 프로세스 추가, ReAct 중첩, 의존성 증가 |
| holmesGPT subprocess 실행 | 구현 단순 | 세션 없음, 제어 어려움, 오버헤드 |
| **toolset 개별 구현 (Go)** | 기존 구조 유지, 언어 통일, k8s-assistant 내부 tool adapter로 자연스럽게 호출 | 초기 구현 비용 |

**권장: toolset 개별 구현.** holmesGPT의 toolset 설계를 참조해 Go로 재구현하고, k8s-assistant 내부 tool adapter로 노출한다. 가장 많이 필요한 Prometheus와 Loki부터 시작해 점진적으로 확장한다. 별도 MCP 서버 실행은 기본 경로로 두지 않는다.

---

## 기존 k8s-assistant 구조와의 정합성

### 현재 역할 분리

```
k8s-assistant ReAct 루프
    ├─ kubectl/bash/custom tools
    ├─ internal log-analyzer tool adapter
    │    ├─ file log source
    │    ├─ Loki log source
    │    ├─ Prometheus metric source
    │    └─ evidence analyzers
    └─ internal trouble-shooting client
         ├─ runbook match
         ├─ RAG search
         └─ remediation plan
```

### log-analyzer 확장 방향

```
internal/loganalyzer
    ├─ client
    ├─ toolsets
    │    ├─ prometheus      — PromQL 실행, alert 조회, metric evidence 생성
    │    ├─ loki            — LogQL 실행, 로그 tail, log evidence 생성
    │    ├─ file            — Filebeat/Fluent-bit 파일 로그 조회
    │    ├─ elasticsearch   — 인덱스 조회, 로그/상태 조회 (후순위)
    │    └─ grafana/zabbix  — 선택
    ├─ analyzers
    │    ├─ AnalyzePattern
    │    ├─ AnalyzeMetricPattern
    │    ├─ KeyEvidence
    │    └─ SummarizeEvidence
    └─ artifact store
         └─ raw 로그/메트릭 결과를 session별 tmp 파일로 저장
```

- **k8s-assistant가 ReAct 루프를 소유한다.** log-analyzer는 내부 tool adapter로 호출되며 별도 서버 실행을 기본으로 하지 않는다.
- **holmesGPT의 ReAct 중첩 없음.** log-analyzer 내부에서 다시 LLM ReAct를 돌리지 않는다.
- **trouble-shooting과의 경계 유지.** log-analyzer는 데이터 수집·분석·근거 요약만 담당하고, 조치 계획은 trouble-shooting이 담당한다.
- **RAG 제거.** log-analyzer의 RAGLookup, chromem store, runbook yaml은 제거 대상이다.

---

## 구현 우선순위 제안

### Phase 1: 내부 패키지와 toolset 기반 구조 정리 (필수)

별도 `log-analyzer-server` 중심 구조가 아니라 `internal/loganalyzer.Client`와 toolset adapter를 k8s-assistant 내부에서 호출하는 구조로 정리한다.

구성:
- `Client`: k8s-assistant에서 호출하는 진입점
- `Toolset`: Prometheus, Loki, file 등 source별 구현 단위
- `LogSource`: 파일, Loki 등 로그 계열 source interface
- `MetricSource`: Prometheus 등 메트릭 계열 source interface
- `EvidenceAnalyzer`: 로그/메트릭 결과를 패턴과 핵심 근거로 변환
- `EvidenceStore`: raw artifact 저장, 조회, cleanup 담당

LLM 호출 경로는 log-analyzer 전용 설정을 둘 수 있게 하되, 설정이 없으면 k8s-assistant의 model client 설정 전체를 상속한다. 다만 기본 분석은 deterministic하게 동작해야 하며, LLM은 `SummarizeEvidence` 같은 요약 보조에 제한적으로 사용한다.

내부 tool adapter 이름은 k8s-assistant tool registry에 다음 형태로 등록한다.

| Tool name | 역할 |
|---|---|
| `log_analyzer_fetch_logs` | file/Loki/kubectl artifact source에서 로그 수집 |
| `log_analyzer_query_loki` | LogQL 실행과 로그 artifact 생성 |
| `log_analyzer_query_loki_labels` | Loki label 탐색 |
| `log_analyzer_query_prometheus_instant` | instant PromQL 실행 |
| `log_analyzer_query_prometheus_range` | range PromQL 실행 |
| `log_analyzer_list_prometheus_alerts` | 활성 alert 조회 |
| `log_analyzer_analyze_pattern` | 로그 기반 패턴 분석 |
| `log_analyzer_analyze_metric_pattern` | 메트릭 기반 패턴 분석 |
| `log_analyzer_key_evidence` | source 결과에서 핵심 근거 추출 |
| `log_analyzer_summarize_evidence` | 여러 evidence를 ProblemSignal용 요약으로 압축 |
| `log_analyzer_get_artifact_sample` | artifact 일부 샘플 조회 |
| `log_analyzer_clean_artifacts` | TTL/max_bytes 기준 artifact 정리 |

모든 tool은 읽기 전용이며 `modifies_resource=no`로 등록한다.

### Phase 2: Prometheus 연동 (고우선순위)

- `github.com/prometheus/client_golang/api/prometheus/v1` 사용
- internal tools: `query_prometheus_range`, `query_prometheus_instant`, `list_prometheus_alerts`
- holmesGPT `prometheus.py` (94KB)의 PromQL 생성 로직은 LLM이 담당하므로 클라이언트 구현만 필요
- Prometheus는 log source가 아니라 metric analyzer로 분리한다.
- 메모리/CPU/재시작/5xx/latency p95 등 특정 상황에서의 factor 분석기로 사용한다.

### Phase 3: Loki 연동 (고우선순위)

- Loki REST API (`/loki/api/v1/query_range`) 직접 사용
- internal tools: `query_loki_logs`, `query_loki_labels`
- holmesGPT에서는 Grafana 하위 모듈로 구현됨
- 로그 원문은 artifact로 저장하고, LLM에는 요약과 샘플만 반환한다.

### Phase 4: 파일 로그 source 완성 (중우선순위)

- Filebeat/Fluent-bit 파일 로그를 읽는 `FileLogSource` 구현
- 설정 가능한 root path, namespace/pod/container 매핑, max_lines, level filter 지원
- Loki가 없는 환경의 fallback 경로로 사용

### Phase 5: Elasticsearch/OpenSearch (중우선순위)

- `github.com/elastic/go-elasticsearch/v8` 또는 `opensearch-go`
- internal tools: `query_elasticsearch`, `get_elasticsearch_cluster_health`

### Phase 6: Grafana, Zabbix (선택)

- Grafana: dashboard panel 조회용. Prometheus/Loki 연동으로 대부분 대체 가능
- Zabbix: REST API 직접 구현 필요. holmesGPT에도 없음

---

## trouble-shooting 전 실행 vs 개별 요구 실행

log-analyzer를 언제 실행할지 두 가지 시나리오:

1. **trouble-shooting 전 전처리**: k8s-assistant가 문제를 감지하면 trouble-shooting 호출 전에 log-analyzer로 관련 로그/메트릭을 수집해 ProblemSignal에 포함
2. **독립 요구**: 사용자가 직접 "Prometheus 메트릭 확인해줘", "Loki에서 에러 로그 조회해줘"를 요청

둘 다 지원 가능하며, 내부 tool adapter로 노출하면 k8s-assistant ReAct 루프가 자연스럽게 상황에 따라 호출한다. trouble-shooting 호출 조건 게이트(`revise_troubleshooting.md` 참조)와 별개로 동작한다.

---

## logFetch 방식 검토

### 로그 소스와 수집 경로

로그 크기와 수집 인프라 유무에 따라 수집 경로가 달라진다.

kube-controller-manager, kube-scheduler, etcd 같은 system component나 트래픽이 많은 workload의 로그는 수십~수백 MB에 달할 수 있다. k8s-assistant가 `kubectl logs`로 직접 가져오면 로그 원문이 ReAct 루프의 LLM context에 그대로 포함되어 토큰 소모가 폭발적으로 증가하고 `--max-iterations` 한도도 쉽게 소진된다. 이 범주의 로그는 ES/Loki에 aggregation 쿼리를 실행해 에러 카운트 시계열과 상위 N개 에러 메시지만 가져오는 방식이 적합하다. 로그 수집 인프라(Filebeat, Fluent-bit, Promtail)가 없는 환경에서는 이 경로를 사용할 수 없다는 전제 조건이 있다.

user namespace의 application 로그는 다르다. 수백 줄 이내라면 k8s-assistant 현재 흐름(`kubectl logs --tail=N`)으로도 처리 가능하다. log-analyzer의 추가 가치는 로그 원문을 LLM에 직접 넘기지 않고 `AnalyzePattern`으로 먼저 처리해 구조화된 패턴 분류 결과(PatternType, severity, summary)를 ProblemSignal로 trouble-shooting에 전달하는 것이다.

Prometheus는 로그가 아닌 메트릭 시계열로 접근하므로 별도다. 에러율, latency percentile, restart count 같은 지표로 이상 신호를 먼저 포착하면 로그 수집을 건너뛰고 trouble-shooting으로 직행할 수 있다.

`FetchLogs`는 파일 source와 toolset source를 모두 지원한다.

| source | 역할 |
|---|---|
| `file` | Filebeat/Fluent-bit가 저장한 로컬 파일 로그 조회 |
| `loki` | LogQL 기반 로그 조회와 label 탐색 |
| `kubectl` | ReAct 루프의 kubectl tool 결과를 분석 입력으로 재사용 |

`FetchLogs` 자체는 source dispatch만 담당하고, 실제 source별 연결과 인증은 각 toolset config가 담당한다.

### 인프라 문제와 애플리케이션 문제 구분

로그에서 감지한 패턴이 인프라 원인인지 애플리케이션 원인인지는 trouble-shooting이 올바른 runbook을 매칭하기 위해 중요하다. OOMKilled, CrashLoopBackOff, ImagePullBackOff, PVC mount 실패는 Kubernetes 이벤트와 1:1 대응되므로 인프라 패턴으로 분류할 수 있다. 반면 스택 트레이스, 특정 엔드포인트의 반복적 HTTP 5xx, 애플리케이션 레벨 assertion 실패는 애플리케이션 버그 시그널이다.

완전한 구분은 현실적으로 어렵다. OOMKilled 하나만 봐도 애플리케이션 메모리 누수인지 리소스 limit 과소 설정인지 다르고, 조치도 다르다(코드 수정 vs 리소스 증가). CrashLoop도 애플리케이션 시작 버그인지 환경 변수/시크릿 누락인지 로그를 더 파야 알 수 있다. 따라서 log-analyzer가 InfraPattern/AppPattern으로 PatternType을 분리 태깅하되, 두 패턴이 동시에 감지된 경우 trouble-shooting 조치 계획에 두 원인 후보를 병기하는 방식이 실용적이다. 코드 레벨 디버깅은 log-analyzer와 trouble-shooting 범위 밖임을 명시한다.

### context 오버 문제

로그가 LLM context에 그대로 포함될 때 발생하는 context window 초과와 토큰 비용 증가는 holmesGPT도 `holmes/core/truncation/` 전담 모듈을 별도로 둘 만큼 실질적인 문제다.

현재 구현에 `max_lines` 제한이 있고 `AnalyzePattern`이 로그 원문 대신 분석 결과를 반환하는 구조이므로, 로그 원문이 LLM context에 들어가지 않는 경로는 부분적으로 확보돼 있다. 여기에 에러/경고 레벨 필터링과 동일 메시지 중복 압축(메시지, count, first_seen, last_seen)을 수집 단계에서 적용하면 수천 줄 로그도 수십 줄 수준으로 줄일 수 있다.

ES/Loki 집계 쿼리가 가능한 환경에서는 로그 원문 fetch 자체를 하지 않는 것이 가장 효과적이다. aggregation 결과(시간대별 에러 카운트, 상위 N개 에러 메시지)는 수백만 줄 로그도 수십 바이트로 압축된다. 수집 인프라가 없을 때만 `kubectl logs` 직접 경로로 fallback한다.

청크 분할 맵-리듀스(시간 범위별로 나눠 패턴 추출 후 병합)는 장기 과제다. 현재는 에러 레벨 필터링 + max_lines + AnalyzePattern 분리만으로도 실용적인 context 제어가 가능하다.

### raw artifact 저장

로그 원문과 대량 메트릭 결과는 LLM context에 넣지 않는다. source toolset은 raw 결과를 session별 artifact 파일로 저장하고, tool 응답에는 참조 가능한 요약만 반환한다.

반환 필드:

| 필드 | 설명 |
|---|---|
| `artifact_id` | 원문 로그/메트릭 artifact 식별자 |
| `summary` | 사람이 읽을 수 있는 짧은 요약 |
| `top_patterns` | 감지된 상위 패턴과 count |
| `sample_lines` | 대표 로그 라인 또는 대표 metric point |
| `time_range` | 조회 시간 범위 |
| `query` | 실제 실행한 LogQL/PromQL/file filter |
| `source` | file, loki, prometheus 등 source 이름 |

저장 경로와 cleanup:

```text
~/.k8s-assistant/tmp/log-analyzer/<session_id>/
```

- CLI 정상 종료 시 현재 session 디렉토리 삭제
- 오류 종료 시 `keep_on_error=true`이면 현재 session 디렉토리를 보존
- 시작 시 TTL이 지난 session 디렉토리 삭제
- 시작 시 `max_bytes`를 초과한 오래된 artifact부터 삭제
- 기본 TTL은 24h
- 기본 `max_bytes`는 100MiB
- `log_analyzer_clean_artifacts` tool로 수동 정리 가능
- 설정으로 `ttl`, `max_bytes`, `keep_on_error`를 제공
- artifact 파일은 LLM에게 직접 전달하지 않고, 필요한 경우 사용자가 별도 조회하거나 후속 tool이 참조한다.

---

## Evidence 공통 구조

로그, 메트릭, alert, 이벤트 결과는 source별 raw 타입을 그대로 ReAct 루프에 전달하지 않고 공통 evidence 구조로 정규화한다.

```go
type EvidenceSet struct {
    SessionID  string
    Target     TargetRef
    Items      []Evidence
    Artifacts  []ArtifactRef
    Summary    string
}

type Evidence struct {
    ID          string
    Source      string // file, loki, prometheus, kubectl
    Kind        string // log_pattern, metric_pattern, alert, sample
    Severity    string
    Confidence  string
    Summary     string
    Reason      string
    TimeRange   TimeRange
    Query       string
    ArtifactID  string
    SampleLines []string
    Labels      map[string]string
}

type ArtifactRef struct {
    ID        string
    Source    string
    Path      string
    SizeBytes int64
    CreatedAt time.Time
    ExpiresAt time.Time
}
```

`EvidenceSet`은 trouble-shooting의 `ProblemSignal.Evidence`로 변환 가능한 형태를 유지한다. 변환 시에는 raw artifact path가 아니라 `summary`, `reason`, `sample_lines`, `time_range`, `query`, `artifact_id`만 포함한다.

---

## 설정 구조

모든 toolset은 연결 정보와 인증 정보를 독립 config section으로 가진다.

기본 경로:

```text
~/.k8s-assistant/log-analyzer.yaml
```

예시:

```yaml
log_analyzer:
  llm:
    inherit_from_assistant: true
    provider: ""
    model: ""
    endpoint: ""
    api_key: ""

  artifacts:
    dir: ~/.k8s-assistant/tmp/log-analyzer
    ttl: 24h
    max_bytes: 104857600
    keep_on_error: true

  sources:
    file:
      enabled: true
      root_dir: /var/log/filebeat
      max_lines: 1000

    loki:
      enabled: true
      url: http://localhost:3100
      timeout: 30s
      tenant_id: ""
      headers: {}
      auth:
        type: bearer
        token: ""
      tls:
        skip_verify: false

    prometheus:
      enabled: true
      url: http://localhost:9090
      timeout: 30s
      headers: {}
      auth:
        type: bearer
        token: ""
      tls:
        skip_verify: false
```

LLM 설정은 log-analyzer 전용 값이 있으면 그 model client 설정 전체를 사용하고, 없으면 k8s-assistant의 provider/model/endpoint/api key를 그대로 상속한다. 필드 단위 부분 상속은 하지 않는다. 단, log-analyzer의 기본 분석은 LLM 없이 동작해야 한다.

---

## API / Tool 구성

### 제거 대상

| 대상 | 이유 |
|---|---|
| `RAGLookup` | log-analyzer는 RAG를 사용하지 않음 |
| `AnalyzeAndRemediate` | 조치 계획은 trouble-shooting 책임 |
| `internal/loganalyzer/rag/*` | chromem 기반 RAG 제거 |
| `internal/loganalyzer/rag/runbooks/*.yaml` | log-analyzer runbook 제거 |

### 유지/신규 API

| API | 역할 |
|---|---|
| `FetchLogs` | file/Loki/kubectl 결과 등 로그 source에서 로그 조회 |
| `AnalyzePattern` | 로그 기반 정형 패턴 탐지 |
| `QueryPrometheusInstant` | instant PromQL 실행 |
| `QueryPrometheusRange` | range PromQL 실행 |
| `ListPrometheusAlerts` | 활성 alert 조회 |
| `QueryLokiLogs` | LogQL 로그 조회 |
| `QueryLokiLabels` | Loki label 탐색 |
| `AnalyzeMetricPattern` | 메트릭 기반 이상 신호 탐지 |
| `KeyEvidence` | source별 결과에서 ProblemSignal 후보 근거 추출 |
| `SummarizeEvidence` | 다중 source 근거를 짧게 압축 |

`AnalyzePattern`과 `AnalyzeMetricPattern`은 원인 후보와 근거를 만들고, `SummarizeEvidence`는 trouble-shooting에 넘길 수 있는 압축된 evidence를 만든다. remediation plan은 만들지 않는다.

### Query 생성 방식

PromQL/LogQL은 두 경로를 모두 지원한다.

1. **Preset query**: 자주 쓰는 상황은 코드에 안전한 query builder로 제공한다.
   - Pod restart 증가
   - container memory working set 증가
   - CPU throttling
   - HTTP 5xx rate
   - latency p95/p99 상승
   - Kubernetes alert 조회
2. **LLM 생성 query**: 사용자가 구체적인 질의를 요청하거나 preset으로 표현하기 어려운 경우 LLM이 PromQL/LogQL을 생성한다.

LLM 생성 query는 실행 전 다음 제한을 통과해야 한다.

- read-only query만 허용
- 최대 time range 제한 적용
- 최대 step/point 수 제한 적용
- label selector가 너무 넓으면 실행 거부 또는 축소
- timeout과 response size limit 적용

### 안전 정책과 제한

log-analyzer tool은 클러스터 리소스를 변경하지 않는다.

| 항목 | 정책 |
|---|---|
| Kubernetes mutation | 금지 |
| 외부 endpoint 호출 | timeout 필수 |
| Loki 로그 조회 | max_lines, max_bytes, max_range 적용 |
| Prometheus range 조회 | max_points, max_range, min_step 적용 |
| Artifact 응답 | raw 전체를 LLM에 직접 반환하지 않음 |
| Retry/fallback | 최대 5회 |
| Fallback 종료 | 분석 가능한 시간대/source를 찾으면 추가 fallback 중단 |

fallback 예시:

1. Prometheus preset query 실패
2. time range 축소 후 재시도
3. 대체 metric query 실행
4. Loki에서 관련 로그 조회
5. file source 또는 kubectl artifact source 조회

분석에 충분한 evidence가 확보되면 남은 fallback은 실행하지 않는다.

---

## 구현 시 삭제 범위

log-analyzer에서는 RAG와 조치 계획 계열 코드를 남기지 않는다. deprecated wrapper도 두지 않고 삭제한다.

삭제 대상:
- `RAGLookup`
- `AnalyzeAndRemediate`
- `internal/loganalyzer/rag/*`
- `internal/loganalyzer/rag/runbooks/*.yaml`
- `rag_lookup`, `analyze_and_remediate` tool 등록
- README/GUIDE/config의 log-analyzer RAG/runbook 설명

대체:
- RAG/runbook 기반 조치 근거는 trouble-shooting이 담당
- log-analyzer는 `EvidenceSet`과 `SummarizeEvidence`만 제공
- 기존 RAG test는 제거하고 evidence/analyzer/toolset test로 대체

---

## 구현 흐름

### 1. 내부 tool adapter 등록

k8s-assistant 시작 시 `~/.k8s-assistant/log-analyzer.yaml`을 로드하고, 설정된 source와 무관하게 log-analyzer internal tool schema를 registry에 등록한다. source가 disabled이거나 설정이 없으면 해당 tool은 명확한 설정 오류를 반환한다.

등록 흐름:
1. k8s-assistant config 로드
2. log-analyzer config 로드
3. log-analyzer LLM 설정 결정
4. artifact store 초기화 및 startup cleanup 수행
5. enabled source별 toolset client 초기화
6. internal tool adapter를 ReAct tool registry에 등록

### 2. source 호출과 evidence 생성

source tool은 raw 데이터를 artifact로 저장하고, 즉시 `EvidenceSet` 또는 `ArtifactRef` 포함 결과를 반환한다. 이후 analyzer tool이 artifact_id 또는 inline sample을 입력으로 받아 패턴을 분석한다.

흐름:
1. `log_analyzer_query_prometheus_range` 또는 `log_analyzer_query_loki`
2. raw 결과를 artifact store에 저장
3. source별 기본 summary/sample 생성
4. `log_analyzer_analyze_metric_pattern` 또는 `log_analyzer_analyze_pattern`
5. `log_analyzer_key_evidence`
6. 필요 시 `log_analyzer_summarize_evidence`
7. trouble-shooting이 필요한 경우 ProblemSignal evidence로 전달

### 3. 분석 파이프라인 트리거

observability source query tool을 내부 tool adapter로 노출하면, 실제 "수집 → 패턴 분석 → evidence 요약 → trouble-shooting 전달" 조합은 k8s-assistant ReAct 루프가 판단한다.

다만 자주 쓰는 조합은 helper API로 제공할 수 있다.

- `CollectPodEvidence`: Pod 기준 로그/메트릭 근거 수집
- `AnalyzeWorkloadHealth`: workload 기준 Prometheus/Loki 근거 수집
- `SummarizeEvidence`: 여러 source 결과를 ProblemSignal용 evidence로 압축

조치 제안이나 실행 계획 생성은 포함하지 않는다.

### 4. 테스트 기준

구현 시 다음 테스트를 최소 기준으로 둔다.

| 영역 | 테스트 |
|---|---|
| config | `~/.k8s-assistant/log-analyzer.yaml` 로드, 기본값, model client 전체 상속 |
| artifact | 저장, sample 조회, TTL cleanup, max_bytes cleanup, keep_on_error |
| tool adapter | tool schema, read-only flag, disabled source 오류 |
| Prometheus | mock server 기반 instant/range/alert 조회, max range/points 제한 |
| Loki | mock server 기반 query/label 조회, max lines/bytes 제한 |
| analyzer | log pattern, metric pattern, KeyEvidence, SummarizeEvidence |
| 삭제 검증 | RAGLookup, AnalyzeAndRemediate, rag package 제거 후 `go test ./...` |

---

## 후순위 검토

### 1. Zabbix 우선순위

holmesGPT에도 없는 toolset이며, REST API 직접 구현 필요. 실제 운영 환경에서 Zabbix 사용 여부 확인 후 우선순위 결정 필요.

### 2. holmesGPT MCP 연동 재검토 조건

observability source가 많아질수록 Go로 재구현하는 비용이 증가한다. holmesGPT MCP 연동은 "지금은 아니지만" 옵션으로 남겨둔다. 조건: toolset이 5개 이상 필요하고 holmesGPT MCP 서버 관리 비용과 ReAct 중첩 비용이 허용 가능한 경우.

---

## 확정 방향 요약

1. log-analyzer는 내부 패키지로 구성하고, source별 toolset을 k8s-assistant 내부 tool adapter로 호출한다.
2. `FetchLogs`는 file source와 toolset source를 모두 지원한다.
3. Prometheus는 log와 별개인 metric analyzer로 구현한다.
4. log-analyzer RAG는 사용하지 않고, 기존 RAG 관련 코드는 deprecated 없이 삭제한다.
5. `AnalyzeAndRemediate`는 제거하고, `AnalyzePattern`, `AnalyzeMetricPattern`, `KeyEvidence`, `SummarizeEvidence` 중심으로 재구성한다.
6. 우선 구현 순서는 Prometheus, Loki, file source 순으로 둔다.
7. 모든 toolset은 독립된 연결/인증 config를 가진다.
8. raw 로그/메트릭은 session artifact로 저장하고, LLM에는 요약과 샘플만 반환한다.
9. 기본 설정 파일은 `~/.k8s-assistant/log-analyzer.yaml`을 사용한다.
10. log-analyzer 전용 LLM 설정이 없으면 k8s-assistant model client 설정 전체를 상속한다.
11. PromQL/LogQL은 preset query와 LLM 생성 query를 모두 지원한다.
12. fallback은 최대 5회이며, 분석 가능한 evidence가 확보되면 중단한다.
