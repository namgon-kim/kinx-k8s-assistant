# 로그 분석 및 트러블슈팅 기능 개발 초안

## 목표

kubectl-ai가 Kubernetes 문제를 감지하거나 문제 원인을 출력한 뒤, 사용자에게 `해결 방법을 찾아볼까요?`라고 확인한다. 사용자가 동의하면 `trouble_shooting`에서 트러블슈팅 사례와 조치 절차를 조회해 출력하고, 다시 `자동으로 해결을 진행할까요?`를 확인한다. 사용자가 자동 해결을 승인하면 조치 계획을 kubectl-ai Agent에 다시 전달해 기존 kubectl-ai 도구/승인 흐름으로 해결 과정을 진행한다.

핵심 원칙은 다음과 같다.

- RAG는 `log_analyzer`에는 사용하지 않지만, `trouble_shooting`에는 정식 구성으로 둔다. 운영 이슈 내용을 export해서 지식베이스로 축적하고, 유사 이슈 검색에 사용한다.
- `trouble_shooting`은 구조화된 runbook 매칭과 운영 이슈 RAG를 함께 사용한다. runbook은 결정적 기본 가이드이고, RAG는 과거 운영 사례/장애 회고/문서 검색을 담당한다.
- RAG를 사용하더라도 해결책을 직접 실행하지 않고, 근거가 있는 트러블슈팅 후보와 실행 가능한 조치 계획만 만든다.
- 실제 Kubernetes 변경은 kubectl-ai Agent가 수행한다.
- 리소스 변경, 삭제, 재시작, 스케일 변경 같은 작업은 기존 kubectl-ai 사용자 승인 흐름을 유지한다.
- 트러블슈팅 검색 결과와 자동 해결 지시는 로깅/마스킹 대상에 포함한다.
- `log_analyzer`와 `trouble_shooting`은 별개 구성으로 둔다. `log_analyzer`는 로그/이벤트/메트릭 수집과 패턴 분석만 담당하고, `trouble_shooting`은 runbook 매칭과 조치 계획 생성을 담당한다.
- kubectl-ai의 custom prompt에 컴포넌트 간 감지 타입과 필드 계약을 주입해 Agent가 어떤 도구를 언제 호출해야 하는지 명확히 한다.

## RAG 사용 범위

이번 구조에서 RAG는 `log_analyzer`의 로그 분석에는 사용하지 않고, `trouble_shooting`의 운영 이슈 지식 검색에는 사용한다.

로그 패턴 분석은 긴 원문 로그, 이벤트, 메트릭 시계열을 다루는 작업이다. 이 작업의 핵심은 검색 증강 생성이 아니라 정확한 수집, 필터링, 파싱, 시간 범위 집계, 샘플링, 패턴 탐지다. 따라서 Prometheus에서 가져오거나 파일에서 읽는 로그/메트릭은 `log_analyzer`가 deterministic pipeline으로 처리해야 한다.

`trouble_shooting`에서 RAG가 필요한 이유:

- 운영 중 발생한 이슈와 해결 이력을 export해서 재사용해야 한다.
- 사내 runbook, 장애 회고, 운영 문서, Kubernetes 공식 문서 조각이 많고 자연어 검색이 필요하다.
- 같은 detection type이라도 서비스/환경별 해결 절차가 달라 유사 사례 검색이 필요한 경우
- 사용자가 설명한 증상을 기존 문서 표현과 다르게 말해도 관련 사례를 찾아야 하는 경우

`log_analyzer`에서 RAG가 필요하지 않은 경우:

- Prometheus query 실행
- 파일 로그 tail/read/parse
- Kubernetes Event 조회
- OOMKilled, CrashLoopBackOff, ImagePullBackOff 같은 정형 상태 감지
- 에러율, timeout 비율, latency percentile, restart 증가량 같은 시계열 계산
- 정해진 detection type과 runbook id를 1:1 또는 N:1로 매핑하는 경우

결론:

- `log_analyzer`: RAG 미사용. Prometheus/File/Kubernetes Event 수집과 분석 엔진으로 구성한다.
- `trouble_shooting`: RAG 사용. 구조화 runbook 매칭을 기본 근거로 삼고, 운영 이슈 export/import 기반 knowledge base를 vector/keyword/hybrid 검색한다.
- `kubectl-ai`: custom prompt로 `log_analyzer`와 `trouble_shooting` 호출 조건을 판단하고, 최종 실행은 kubectl-ai 도구와 승인 흐름으로 수행한다.

## 현재 프로젝트에서 확인된 구성

현재 프로젝트는 `kubectl-ai` Agent를 감싸는 CLI와 별도 MCP 서버 형태의 로그 분석 기능으로 나뉜다.

- `internal/orchestrator/orchestrator.go`
  - 사용자 입력 루프, kubectl-ai Agent 메시지 처리, 사용자 선택 요청, 로그 기록을 담당한다.
  - `MessageTypeText`, `MessageTypeError`, `MessageTypeToolCallRequest`, `MessageTypeToolCallResponse`, `MessageTypeUserInputRequest`, `MessageTypeUserChoiceRequest`를 처리한다.
  - 현재는 kubectl-ai 출력 텍스트를 보고 자동으로 `해결 방법을 찾아볼까요?` 프롬프트를 띄우는 상태 머신은 없다.

- `internal/agent/setup.go`
  - kubectl-ai Agent 생성 및 입출력 채널을 래핑한다.
  - `MCPClientEnabled: cfg.MCPClient`로 MCP client 모드를 켤 수 있다.
  - Agent에 추가 입력을 넣을 때 `SendInput(&api.UserInputResponse{Query: ...})`를 사용한다. trouble_shooting 조치 계획 기반 자동 해결 지시도 이 경로로 다시 주입할 수 있다.

- `internal/loganalyzer`
  - MCP 서버로 노출할 로그 분석/RAG 인터페이스가 이미 있다.
  - 주요 도구는 `fetch_logs`, `analyze_pattern`, `rag_lookup`, `analyze_and_remediate`이다.
  - `AnalyzerImpl.RAGLookup`은 증상과 패턴 문자열을 합쳐 store 검색을 수행한다.
  - `AnalyzerImpl.AnalyzeAndRemediate`는 로그 조회, 패턴 탐지, RAG 검색, 조치 단계 생성을 하나의 파이프라인으로 실행한다.
  - 향후 계획에서는 이 패키지의 RAG/조치 생성 책임을 `trouble_shooting` 구성으로 분리한다. `log_analyzer`에는 Prometheus/File/Kubernetes Event 기반 관측 데이터 수집과 패턴 분석 책임만 남기는 것이 좋다.

- `internal/loganalyzer/rag/runbooks/default.yaml`
  - CrashLoopBackOff, OOMKilled, ImagePullBackOff, Pending Pod, Disk Full, Timeout, Probe 실패, Service endpoint 없음 등 기본 트러블슈팅 사례가 YAML로 정의되어 있다.

- `internal/loganalyzer/rag`
  - `VectorStore` 인터페이스가 정의되어 있다.
  - `ChromemStore`와 `OpenAIEmbedder` 구현이 준비되어 있으나, `cmd/log-analyzer-server/main.go`는 현재 `NewSimpleKeywordStore()`를 사용한다.
  - 분리 후에는 이 코드를 `internal/troubleshooting` 쪽으로 이동하거나 제거한다. `log_analyzer`에는 vector store가 필요하지 않다.

- `config/mcp.yaml`
  - kubectl-ai MCP client가 `http://localhost:9090/mcp`의 `log-analyzer` 서버를 바라보는 설정 예시가 있다.

- `prompts/system_ko.tmpl`
  - MCP client 모드에서 `log-analyzer_analyze_and_remediate`, `log-analyzer_fetch_logs`, `log-analyzer_rag_lookup`을 우선 활용하라는 지시가 이미 있다.
  - 향후에는 이 프롬프트를 custom prompt 계약 문서로 확장해 `log_analyzer`와 `trouble_shooting`의 호출 조건, 입력 필드, 출력 타입을 주입한다.

## 목표 아키텍처

최종 구성은 세 컴포넌트가 역할을 나눠 동작하는 형태를 권장한다.

```text
사용자
  ↓
k8s-assistant Orchestrator
  ↓
kubectl-ai Agent + custom prompt
  ├─ Kubernetes 기본 도구
  ├─ log_analyzer MCP
  │  ├─ fetch_logs_from_file
  │  ├─ query_prometheus
  │  ├─ fetch_events
  │  ├─ analyze_log_pattern
  │  ├─ analyze_metric_pattern
  │  └─ summarize_observation
  └─ trouble_shooting MCP 또는 service
     ├─ match_runbook
     ├─ search_knowledge (RAG)
     ├─ get_runbook
     ├─ build_remediation_plan
     ├─ export_issue
     ├─ import_issues
     ├─ index_knowledge
     └─ validate_remediation_plan
```

책임 분리는 다음과 같다.

| 구성 | 책임 | 하지 않아야 할 일 |
|------|------|------------------|
| kubectl-ai | Kubernetes 상태 확인, 도구 실행, 사용자 승인 기반 변경 수행, 결과 요약 | RAG 저장소 직접 관리, 검증되지 않은 자동 변경 |
| log_analyzer | Prometheus/File/Event 기반 관측 데이터 수집, 로그/메트릭 패턴 탐지, 관측 결과 요약 | RAG 검색, 조치 계획 생성, 리소스 변경 |
| trouble_shooting | detection type 기반 runbook 매칭, 운영 이슈 RAG 검색, 진단/조치/검증/롤백 계획 생성, 이슈 export/import/index | 로그 원문 대량 수집, 메트릭 계산, Kubernetes 명령 직접 실행 |
| Orchestrator | 사용자 확인 플로우, custom prompt 주입, Agent 입력 재주입, 중복 제안 제어 | Kubernetes 문제를 단독 판단해 변경 실행 |

`log_analyzer`와 `trouble_shooting`을 분리하면 로그가 없는 문제도 조치 계획으로 연결할 수 있다. 예를 들어 `kubectl get pod` 결과의 `ImagePullBackOff`, `Pending`, `FailedScheduling`은 로그 분석 없이 trouble_shooting의 runbook 매칭으로 바로 연결할 수 있다. 이후 운영 중 해결된 이슈는 export되어 knowledge base에 쌓이고, 다음 유사 장애에서는 RAG 검색 결과가 runbook을 보강한다.

## 컴포넌트 간 필드와 타입 계약

custom prompt에는 아래 타입 계약을 요약해 주입한다. Go 타입은 실제 구현 시 별도 패키지로 분리해 공유하는 것이 좋다.

권장 패키지 후보:

- `internal/diagnostic/types.go`: kubectl-ai, log_analyzer, trouble_shooting이 공유할 진단/증상 타입
- `internal/troubleshooting`: runbook 매칭, 운영 이슈 RAG 지식 검색, 조치 계획 타입
- `internal/loganalyzer`: 로그/이벤트 관측 타입

### 공통 타입

```go
type ComponentType string

const (
    ComponentKubectlAI     ComponentType = "kubectl_ai"
    ComponentLogAnalyzer   ComponentType = "log_analyzer"
    ComponentTroubleShoot  ComponentType = "trouble_shooting"
)

type DetectionSource string

const (
    DetectionSourceKubectlOutput DetectionSource = "kubectl_output"
    DetectionSourceAgentText     DetectionSource = "agent_text"
    DetectionSourceToolResult    DetectionSource = "tool_result"
    DetectionSourceLogPattern    DetectionSource = "log_pattern"
    DetectionSourceMetricPattern DetectionSource = "metric_pattern"
    DetectionSourceEvent         DetectionSource = "event"
    DetectionSourceUserQuery     DetectionSource = "user_query"
)

type Severity string

const (
    SeverityInfo     Severity = "info"
    SeverityWarning  Severity = "warning"
    SeverityCritical Severity = "critical"
)

type ConfidenceLevel string

const (
    ConfidenceCertain   ConfidenceLevel = "certain"
    ConfidenceHigh      ConfidenceLevel = "high"
    ConfidenceMedium    ConfidenceLevel = "medium"
    ConfidenceLow       ConfidenceLevel = "low"
    ConfidenceSpeculate ConfidenceLevel = "speculate"
)
```

### 문제 감지 타입

```go
type DetectionType string

const (
    DetectionCrashLoopBackOff  DetectionType = "CrashLoopBackOff"
    DetectionOOMKilled         DetectionType = "OOMKilled"
    DetectionImagePullBackOff  DetectionType = "ImagePullBackOff"
    DetectionErrImagePull      DetectionType = "ErrImagePull"
    DetectionPending           DetectionType = "Pending"
    DetectionFailedScheduling  DetectionType = "FailedScheduling"
    DetectionProbeFailed       DetectionType = "ProbeFailed"
    DetectionServiceNoEndpoint DetectionType = "ServiceNoEndpoint"
    DetectionNetworkFailure    DetectionType = "NetworkFailure"
    DetectionTimeout           DetectionType = "Timeout"
    DetectionDiskFull          DetectionType = "DiskFull"
    DetectionPermissionDenied  DetectionType = "PermissionDenied"
    DetectionConfigError       DetectionType = "ConfigError"
    DetectionUnknown           DetectionType = "Unknown"
)
```

### Kubernetes 대상 식별자

```go
type KubernetesTarget struct {
    Cluster     string `json:"cluster,omitempty"`
    Context     string `json:"context,omitempty"`
    Namespace   string `json:"namespace,omitempty"`
    Kind        string `json:"kind,omitempty"`
    Name        string `json:"name,omitempty"`
    PodName     string `json:"pod_name,omitempty"`
    Container   string `json:"container,omitempty"`
    OwnerKind   string `json:"owner_kind,omitempty"`
    OwnerName   string `json:"owner_name,omitempty"`
}
```

### kubectl-ai가 생성/전달할 문제 신호

kubectl-ai custom prompt는 문제가 의심될 때 아래 형태의 필드를 기준으로 `log_analyzer` 또는 `trouble_shooting` 호출을 판단한다.

```go
type ProblemSignal struct {
    ID             string            `json:"id"`
    Source         DetectionSource   `json:"source"`
    DetectedBy     ComponentType     `json:"detected_by"`
    DetectionTypes []DetectionType   `json:"detection_types"`
    Severity       Severity          `json:"severity"`
    Confidence     ConfidenceLevel   `json:"confidence"`
    Summary        string            `json:"summary"`
    Evidence       []Evidence        `json:"evidence"`
    Target         KubernetesTarget  `json:"target"`
    Attributes     map[string]string `json:"attributes,omitempty"`
}

type Evidence struct {
    Source    DetectionSource `json:"source"`
    Message   string          `json:"message"`
    Timestamp string          `json:"timestamp,omitempty"`
    Command   string          `json:"command,omitempty"`
    Query     string          `json:"query,omitempty"`
    RefID     string          `json:"ref_id,omitempty"`
}
```

예시:

```json
{
  "source": "kubectl_output",
  "detected_by": "kubectl_ai",
  "detection_types": ["ImagePullBackOff"],
  "severity": "warning",
  "confidence": "high",
  "summary": "default/nginx Pod이 ImagePullBackOff 상태입니다.",
  "target": {
    "namespace": "default",
    "kind": "Pod",
    "name": "nginx",
    "pod_name": "nginx"
  }
}
```

### log_analyzer 입력/출력 필드

`log_analyzer`는 `ProblemSignal`을 보강하거나 새 `ProblemSignal`을 생성한다.

로그/메트릭 원천:

```go
type ObservationSourceType string

const (
    ObservationSourceFile       ObservationSourceType = "file"
    ObservationSourcePrometheus ObservationSourceType = "prometheus"
    ObservationSourceK8sEvent   ObservationSourceType = "k8s_event"
)

type ObservationSource struct {
    Type       ObservationSourceType `json:"type"`
    Path       string                `json:"path,omitempty"`
    Endpoint   string                `json:"endpoint,omitempty"`
    Query      string                `json:"query,omitempty"`
    Labels     map[string]string     `json:"labels,omitempty"`
}
```

입력:

```go
type LogAnalysisRequest struct {
    Signal        ProblemSignal      `json:"signal"`
    Target        KubernetesTarget   `json:"target"`
    Sources       []ObservationSource `json:"sources,omitempty"`
    SinceSeconds  int64              `json:"since_seconds,omitempty"`
    MaxLines      int                `json:"max_lines,omitempty"`
    SampleStrategy string            `json:"sample_strategy,omitempty"`
}

type PrometheusQueryRequest struct {
    Target      KubernetesTarget `json:"target"`
    Query       string           `json:"query"`
    Start       string           `json:"start,omitempty"`
    End         string           `json:"end,omitempty"`
    Step        string           `json:"step,omitempty"`
    Labels      map[string]string `json:"labels,omitempty"`
}
```

출력:

```go
type LogAnalysisResult struct {
    Signal          ProblemSignal     `json:"signal"`
    Patterns        []LogPattern      `json:"patterns"`
    Metrics         []MetricPattern   `json:"metrics,omitempty"`
    Summary         string            `json:"summary"`
    RecommendedNext []NextAction      `json:"recommended_next"`
}

type LogPattern struct {
    Type        DetectionType   `json:"type"`
    Count       int             `json:"count"`
    Severity    Severity        `json:"severity"`
    Confidence  ConfidenceLevel `json:"confidence"`
    Description string          `json:"description"`
    Evidence    []Evidence      `json:"evidence"`
}

type MetricPattern struct {
    Type        DetectionType   `json:"type"`
    MetricName  string          `json:"metric_name"`
    Value       float64         `json:"value"`
    Threshold   float64         `json:"threshold,omitempty"`
    Window      string          `json:"window,omitempty"`
    Severity    Severity        `json:"severity"`
    Confidence  ConfidenceLevel `json:"confidence"`
    Description string          `json:"description"`
    Evidence    []Evidence      `json:"evidence"`
}
```

`log_analyzer`의 권장 도구 종류:

| 도구 | 입력 | 출력 | 목적 |
|------|------|------|------|
| `fetch_logs_from_file` | `KubernetesTarget`, `path`, `since_seconds`, `max_lines`, `sample_strategy` | 로그 라인 또는 샘플 | 파일 기반 로그 조회 |
| `query_prometheus` | `PrometheusQueryRequest` | 시계열/instant vector | Prometheus 메트릭 조회 |
| `fetch_events` | `KubernetesTarget` | 이벤트 목록 | `FailedScheduling`, pull 실패, probe 실패 등 이벤트 조회 |
| `analyze_log_pattern` | 로그 라인 또는 `LogAnalysisRequest` | `LogAnalysisResult` | 로그 기반 패턴 탐지 |
| `analyze_metric_pattern` | Prometheus query 결과 | `LogAnalysisResult` | 메트릭 기반 패턴 탐지 |
| `summarize_observation` | 로그/메트릭/이벤트/상태 | `ProblemSignal` | trouble_shooting에 넘길 증상 요약 |

긴 로그 처리 원칙:

- 긴 로그 전체를 LLM이나 RAG에 넘기지 않는다.
- `log_analyzer`가 시간 범위, namespace, pod, container, label 기준으로 먼저 좁힌다.
- 분석에는 tail, head, error-only filter, 시간 윈도우 샘플링, 중복 stack trace 접기, error code 집계, latency bucket 집계를 사용한다.
- `trouble_shooting`에는 원문 전체가 아니라 `ProblemSignal`, `LogPattern`, `MetricPattern`, 대표 evidence만 넘긴다.
- Prometheus는 로그 저장소가 아니라 메트릭/시계열 소스로 본다. 로그가 Loki가 아니라 파일에 있다면 파일 fetcher를 쓰고, 메트릭 증거가 필요하면 Prometheus query를 쓴다.

### trouble_shooting 입력/출력 필드

`trouble_shooting`은 `ProblemSignal`을 받아 runbook 매칭, 운영 이슈 RAG 검색, 조치 계획 생성을 수행한다.

입력:

```go
type TroubleshootingSearchRequest struct {
    Signal     ProblemSignal    `json:"signal"`
    Query      string           `json:"query,omitempty"`
    Target     KubernetesTarget `json:"target"`
    TopK       int              `json:"top_k,omitempty"`
    Locale     string           `json:"locale,omitempty"`
}
```

출력:

```go
type TroubleshootingSearchResult struct {
    Query       string                 `json:"query"`
    Cases       []TroubleshootingCase  `json:"cases"`
    Confidence  ConfidenceLevel        `json:"confidence"`
    Summary     string                 `json:"summary"`
    SearchMode  string                 `json:"search_mode"`
}

type TroubleshootingCase struct {
    ID              string              `json:"id"`
    Title           string              `json:"title"`
    MatchTypes      []DetectionType     `json:"match_types"`
    Similarity      float64             `json:"similarity"`
    Cause           string              `json:"cause"`
    Resolution      string              `json:"resolution"`
    DiagnosticSteps []PlanStep          `json:"diagnostic_steps"`
    RemediateSteps  []PlanStep          `json:"remediate_steps"`
    VerifySteps     []PlanStep          `json:"verify_steps"`
    RollbackSteps   []PlanStep          `json:"rollback_steps"`
    RiskLevel       string              `json:"risk_level"`
    Source          string              `json:"source"`
}
```

운영 이슈 export/import:

```go
type ExportedIssue struct {
    ID               string             `json:"id"`
    Title            string             `json:"title"`
    SourceType       string             `json:"source_type"`
    Signal           ProblemSignal      `json:"signal"`
    LogSummary       string             `json:"log_summary,omitempty"`
    MetricSummary    string             `json:"metric_summary,omitempty"`
    SelectedCases    []TroubleshootingCase `json:"selected_cases,omitempty"`
    Plan             *RemediationPlan   `json:"plan,omitempty"`
    ExecutionResult  string             `json:"execution_result,omitempty"`
    Cause            string             `json:"cause,omitempty"`
    Resolution       string             `json:"resolution,omitempty"`
    Tags             []string           `json:"tags,omitempty"`
    CreatedAt        string             `json:"created_at"`
    Source           string             `json:"source,omitempty"`
}

type KnowledgeIndexRequest struct {
    Sources      []string `json:"sources"`
    Rebuild      bool     `json:"rebuild"`
    IncludeIssues bool    `json:"include_issues"`
    IncludeRunbooks bool  `json:"include_runbooks"`
}
```

조치 계획:

```go
type RemediationPlanRequest struct {
    Signal        ProblemSignal                 `json:"signal"`
    SelectedCases []TroubleshootingCase          `json:"selected_cases"`
    Target        KubernetesTarget              `json:"target"`
    Constraints   RemediationConstraints        `json:"constraints"`
}

type RemediationPlan struct {
    ID            string           `json:"id"`
    Target        KubernetesTarget `json:"target"`
    Summary       string           `json:"summary"`
    Assumptions   []string         `json:"assumptions"`
    Steps         []PlanStep       `json:"steps"`
    Verification  []PlanStep       `json:"verification"`
    Rollback      []PlanStep       `json:"rollback"`
    RiskLevel     string           `json:"risk_level"`
    RequiresUserApproval bool      `json:"requires_user_approval"`
}

type PlanStep struct {
    Order                int               `json:"order"`
    Type                 string            `json:"type"`
    Description          string            `json:"description"`
    CommandTemplate      string            `json:"command_template,omitempty"`
    RenderedCommand      string            `json:"rendered_command,omitempty"`
    AutomaticCandidate   bool              `json:"automatic_candidate"`
    RequiresConfirmation bool              `json:"requires_confirmation"`
    Preconditions        []string          `json:"preconditions,omitempty"`
    ExpectedOutcome      string            `json:"expected_outcome,omitempty"`
    Variables            map[string]string `json:"variables,omitempty"`
}

type RemediationConstraints struct {
    AllowMutation       bool `json:"allow_mutation"`
    RequireDryRun       bool `json:"require_dry_run"`
    RequireConfirmation bool `json:"require_confirmation"`
}
```

`trouble_shooting`의 권장 도구 종류:

| 도구 | 입력 | 출력 | 목적 |
|------|------|------|------|
| `match_runbook` | `ProblemSignal`, `KubernetesTarget` | `TroubleshootingSearchResult` | detection type/라벨 기반 구조화 runbook 매칭 |
| `search_knowledge` | `TroubleshootingSearchRequest` | `TroubleshootingSearchResult` | RAG 기반 운영 이슈/문서/사례 검색 |
| `get_runbook` | runbook id | `TroubleshootingCase` | 특정 runbook 상세 조회 |
| `build_remediation_plan` | `RemediationPlanRequest` | `RemediationPlan` | 실행 전 조치 계획 생성 |
| `validate_remediation_plan` | `RemediationPlan` | validation result | 위험도, 누락 필드, 자동 실행 가능 여부 검증 |
| `export_issue` | `ExportedIssue` 또는 실행 컨텍스트 | exported issue file | 운영 이슈를 지식베이스 입력으로 저장 |
| `import_issues` | issue file/dir | import result | export된 이슈를 knowledge base로 적재 |
| `index_knowledge` | `KnowledgeIndexRequest` | index result | runbook/문서/이슈를 chunking/embedding/indexing |

### custom prompt에 주입할 호출 규칙

kubectl-ai custom prompt에는 다음 규칙을 넣는다.

```text
문제가 의심되면 먼저 ProblemSignal을 내부적으로 구성한다.

log_analyzer 호출 조건:
- 로그, 이벤트, 재시작 원인, probe 실패, OOMKilled 여부가 필요한 경우
- 사용자가 "로그 분석", "원인 분석", "왜 재시작"을 물은 경우
- ProblemSignal의 confidence가 medium 이하이고 로그/이벤트 근거가 필요한 경우
- CPU, memory, latency, error rate, restart count 같은 시계열 근거가 필요한 경우 Prometheus query를 사용한다.
- 대량 로그 원문은 LLM에 직접 전달하지 말고 log_analyzer가 요약/패턴/evidence로 줄인다.

trouble_shooting 호출 조건:
- ProblemSignal의 detection_types가 하나 이상 있고 해결책/조치 방법을 찾아야 하는 경우
- 사용자가 해결 방법 검색에 동의한 경우
- log_analyzer 결과가 있고 runbook 매칭이 필요한 경우
- 먼저 match_runbook으로 구조화 runbook을 찾는다.
- 이어서 search_knowledge로 과거 운영 이슈와 문서를 검색해 runbook 결과를 보강한다.
- 운영 이슈 해결이 끝나면 export_issue로 ProblemSignal, 분석 요약, 조치 계획, 실행 결과를 저장할 수 있다.

자동 해결 규칙:
- trouble_shooting은 실행 계획만 만든다.
- kubectl-ai만 Kubernetes 명령을 실행한다.
- mutation 명령은 사용자 승인 전 실행하지 않는다.
- trouble_shooting 결과의 PlanStep 중 RequiresConfirmation=true 또는 risk_level이 medium 이상이면 반드시 사용자 확인을 받는다.
```

## 변경 또는 추가되어야 할 구성

### 1. 트러블슈팅 제안 감지기

kubectl-ai의 출력이 문제 진단/오류/실패 상황인지 판단하는 얇은 감지 계층이 필요하다.

추가 위치 후보:

- `internal/orchestrator/troubleshooting.go`
- `internal/orchestrator/orchestrator.go`의 `MessageTypeText`, `MessageTypeError`, `MessageTypeToolCallResponse` 처리 직후 호출

역할:

- Agent 출력 또는 tool result에서 문제 후보를 감지한다.
- 감지한 증상을 요약해 공통 계약인 `ProblemSignal`로 만든다.
- 이미 같은 증상에 대해 제안한 경우 반복 제안을 막는다.

예상 구조:

```go
type ProblemSignal struct {
    ID             string
    Source         DetectionSource
    DetectedBy     ComponentType
    DetectionTypes []DetectionType
    Severity       Severity
    Confidence     ConfidenceLevel
    Summary        string
    Evidence       []Evidence
    Target         KubernetesTarget
}
```

초기 감지 기준:

- Kubernetes 상태 키워드: `CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, `OOMKilled`, `Pending`, `FailedScheduling`, `BackOff`, `Unhealthy`, `Readiness probe failed`, `Liveness probe failed`
- 로그/오류 키워드: `timeout`, `deadline exceeded`, `connection refused`, `no space left`, `permission denied`
- kubectl-ai 출력 문장: `문제`, `오류`, `실패`, `원인`, `조치`, `해결`, `재시작`

초기에는 Orchestrator의 규칙 기반 감지와 kubectl-ai custom prompt 기반 감지를 병행한다. 이후 custom prompt가 안정화되면 Agent가 구조화된 `ProblemSignal`을 만들고, Orchestrator는 사용자 확인과 중복 제어에 집중한다.

### 2. 사용자 확인 플로우

Orchestrator 안에 다음 2단계 확인 흐름이 필요하다.

1. 트러블슈팅 검색 확인
   - 출력 예: `감지된 문제에 대해 해결 방법을 찾아볼까요? (y/n):`
   - `y`이면 trouble_shooting runbook 매칭 실행
   - `n`이면 일반 대화 루프로 복귀

2. 자동 해결 확인
   - 조치 계획 출력 후 `이 조치 계획을 kubectl-ai로 자동 진행할까요? (y/n):`
   - `y`이면 조치 지시를 kubectl-ai Agent에 다시 입력
   - `n`이면 조치 계획만 보여주고 종료

구현 위치 후보:

- `internal/orchestrator/troubleshooting_flow.go`
- `Orchestrator.handleMessage`에서 텍스트/에러/tool result 처리 후 `maybeOfferTroubleshooting(ctx, signal)` 호출

주의점:

- `readline` prompt를 임시 변경한 뒤 반드시 원래 prompt로 복구한다.
- 사용자의 `ctrl+c`, `EOF`는 자동 해결 거절로 처리한다.
- Agent가 `UserInputRequest`를 기다리는 시점이 아니어도 `SendInput`이 가능한지 검증해야 한다. 불안정하면 다음 `MessageTypeUserInputRequest`까지 pending remediation prompt를 보관한다.

### 3. 트러블슈팅 검색 호출 계층

트러블슈팅 검색은 `log_analyzer`가 아니라 `trouble_shooting` 구성에서 담당한다. 기본 흐름은 구조화 runbook 매칭 후 운영 이슈 RAG 검색으로 보강하는 방식이다. Orchestrator가 직접 MCP client 도구를 호출하기 어렵다면 두 가지 선택지가 있다.

권장 1차 구현:

- 트러블슈팅 검색 자체는 kubectl-ai Agent에게 지시한다.
- 사용자가 검색을 승인하면 Orchestrator가 다음 입력을 Agent에 주입한다.
- Agent는 custom prompt에 정의된 `ProblemSignal` 필드를 기준으로 `trouble_shooting_match_runbook` 또는 `trouble_shooting_build_remediation_plan`을 호출한다.
- runbook 매칭 이후 `trouble_shooting_search_knowledge`를 호출해 과거 운영 이슈와 문서를 검색한다. RAG 결과는 조치 계획의 근거로만 사용한다.

예시:

```text
방금 진단한 문제에 대해 trouble_shooting_match_runbook 도구를 사용해 트러블슈팅 사례를 찾아줘.

ProblemSignal:
- detection_types: <signal.DetectionTypes>
- severity: <signal.Severity>
- confidence: <signal.Confidence>
- summary: <signal.Summary>
- target.namespace: <namespace>
- target.kind: <kind>
- target.name: <name>

결과는 원인, 근거, 권장 조치, 자동 실행 가능/불가 구분으로 한국어 요약해줘.
아직 Kubernetes 변경은 실행하지 마.
```

장점:

- 기존 kubectl-ai MCP client 경로를 그대로 사용한다.
- 별도 MCP client 구현을 Orchestrator에 추가하지 않아도 된다.
- LLM이 이미 가진 도구 선택/결과 해석 루프를 활용한다.

단점:

- trouble_shooting 결과가 구조화 데이터로 Orchestrator에 직접 들어오지 않는다.
- 자동 해결 단계에서 조치 계획을 정확히 재사용하려면 최근 Agent 출력 또는 tool result를 컨텍스트에 보관해야 한다.

권장 2차 구현:

- Orchestrator 내부에 `TroubleshootingClient` 인터페이스를 추가한다.
- HTTP MCP 또는 troubleshooting 패키지를 직접 호출해 `TroubleshootingSearchResult`/`RemediationPlan`을 구조화 데이터로 받는다.

예상 구조:

```go
type TroubleshootingClient interface {
    MatchRunbook(ctx context.Context, req TroubleshootingSearchRequest) (*TroubleshootingSearchResult, error)
    BuildPlan(ctx context.Context, req RemediationPlanRequest) (*RemediationPlan, error)
}
```

이 방식은 UI에서 조치 계획을 안정적으로 렌더링하고 자동 해결 프롬프트를 정교하게 만들 수 있지만, MCP client 호출 또는 서버 직접 연결 설정이 추가로 필요하다.

### 4. 자동 해결 지시 생성기

trouble_shooting 결과를 그대로 실행하지 말고 kubectl-ai에 전달할 “계획 확인형” 프롬프트로 변환한다.

예시:

```text
다음 트러블슈팅 조치 계획을 바탕으로 문제 해결을 진행해줘.

대상:
- namespace: default
- pod: app-pod

진단:
- CrashLoopBackOff / OOMKilled 의심

권장 조치:
1. kubectl describe pod로 이벤트 확인
2. kubectl logs로 종료 직전 로그 확인
3. memory limit/request 확인
4. 필요한 경우 memory limit 증설 제안

진행 규칙:
- 먼저 현재 클러스터 상태를 다시 확인해.
- 변경 작업 전에는 구체적으로 어떤 리소스를 어떻게 바꿀지 사용자 승인을 받아.
- 확실하지 않은 조치는 실행하지 말고 추가 확인 명령을 먼저 수행해.
- 실행 결과와 다음 조치를 한국어로 요약해.
```

이 프롬프트를 `AgentWrapper.SendInput(&api.UserInputResponse{Query: prompt})`로 주입한다. 자동 해결 프롬프트에는 `ProblemSignal`, 선택된 `TroubleshootingCase`, `RemediationPlan`의 핵심 필드를 포함한다.

### 5. Runbook 데이터 모델 확장

현재 `SimilarCase`는 `Title`, `Cause`, `Resolution`, `Source` 중심이다. 자동 해결까지 고려하려면 runbook YAML을 더 구조화해야 한다.

추가 필드 후보:

```yaml
cases:
  - id: crashloop-oom
    title: "CrashLoopBackOff - 메모리 부족 (OOMKilled)"
    symptoms:
      - "CrashLoopBackOff"
      - "OOMKilled"
    cause: "..."
    resolution: "..."
    diagnostic_steps:
      - description: "Pod 이벤트 확인"
        command_template: "kubectl describe pod {{pod}} -n {{namespace}}"
        automatic: true
    remediation_steps:
      - description: "메모리 limit 증설"
        command_template: "kubectl set resources deployment {{deployment}} -n {{namespace}} --limits=memory={{memory_limit}}"
        automatic: false
        requires_confirmation: true
    rollback_steps:
      - description: "이전 리소스 limit으로 복구"
    risk_level: "medium"
    source: "..."
```

필요한 Go 타입:

- `ProblemSignal`
- `TroubleshootingCase`
- `PlanStep`
- `RemediationPlan`
- `RiskLevel`
- `CommandTemplate` 렌더링 함수

### 6. 운영 이슈 RAG 설정

현재 서버 실행 경로는 `SimpleKeywordStore`를 사용한다. 향후에는 검색 저장소를 `trouble_shooting` 구성으로 이동하고, 운영 이슈 export/import 기반 RAG 지식베이스를 구성한다. 검색 흐름은 `match_runbook`으로 구조화 runbook을 먼저 찾고, `search_knowledge`로 과거 운영 이슈와 문서를 보강하는 방식이다.

설정 예시:

```yaml
trouble_shooting:
  enabled: true

  server:
    host: 127.0.0.1
    port: 9091
    mcp_endpoint: /mcp
    timeout_seconds: 60

  rag:
    enabled: true
    provider: qdrant           # local|endpoint|qdrant
    mode: hybrid               # keyword|vector|hybrid
    top_k: 11
    min_score: 0.65
    rerank_enabled: true
    max_context_chars: 12000
    include_sources: true

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

    # provider=endpoint는 Qdrant를 직접 붙이지 않고 별도 RAG API 서버에 위임할 때만 사용한다.
    # 이 경우 endpoint.url/api_key/timeout_seconds를 설정하고 qdrant/embedding/reranker 블록은 서버 구현에 맡긴다.

  remediation:
    allow_auto_remediate: false
    require_confirmation: true
    require_plan_validation: true
    max_risk_level: medium
    require_rollback_for_high_risk: true

  audit:
    enabled: true
    file: ~/.k8s-assistant/troubleshooting/audit.log
```

구현 순서:

1. 구조화 runbook 매칭 구현
2. 운영 이슈 export schema와 저장 경로 구현
3. exported issue importer 구현
4. keyword/vector/hybrid indexer 구현
5. `search_knowledge`에서 runbook과 exported issue를 함께 검색
6. 검색 결과와 조치 계획, 사용자 승인 결과를 audit log에 남김

### 7. 프롬프트 보강

`prompts/system_ko.tmpl`에 다음 원칙을 추가한다.

- custom prompt는 `ProblemSignal`, `DetectionType`, `KubernetesTarget`, `LogAnalysisRequest`, `PrometheusQueryRequest`, `TroubleshootingSearchRequest`, `RemediationPlan` 필드 계약을 포함한다.
- log_analyzer는 Prometheus/File/Event 관측과 패턴 분석 도구이고, trouble_shooting은 runbook/운영 이슈 RAG/조치 계획 도구임을 명확히 구분한다.
- 문제 진단 후에는 사용자가 원할 때만 트러블슈팅 검색을 수행한다.
- 트러블슈팅 조회 단계에서는 Kubernetes 변경을 실행하지 않는다.
- 자동 해결 단계에서도 변경 전 사용자 승인을 받아야 한다.
- 트러블슈팅 결과의 출처와 확신도를 함께 제시한다.
- 조치 계획의 근거가 약하면 추가 진단 명령을 먼저 제안한다.

### 8. 상태와 중복 제어

Orchestrator에 트러블슈팅 상태를 보관한다.

예상 상태:

```go
type TroubleshootingState struct {
    LastCandidateHash string
    PendingSignal     *ProblemSignal
    LastTroubleshootingSummary string
    LastPlan          *RemediationPlan
    LastOfferedAt     time.Time
}
```

필요한 제어:

- 같은 Agent 응답에 대해 여러 번 제안하지 않는다.
- 사용자가 거절한 증상은 일정 시간 또는 같은 세션에서 다시 묻지 않는다.
- 자동 해결 진행 중에는 추가 트러블슈팅 제안을 잠시 막는다.

## 개발 계획

### 1단계: custom prompt 계약 기반 MVP

목표:

- kubectl-ai custom prompt에 `ProblemSignal` 생성 규칙과 컴포넌트 호출 규칙 주입
- kubectl-ai 문제 출력 또는 tool result에서 `ProblemSignal` 후보 감지
- `해결 방법을 찾아볼까요?` 확인
- 승인 시 kubectl-ai에 `trouble_shooting` runbook 매칭 지시 주입
- 트러블슈팅 결과 출력
- `자동으로 해결을 진행할까요?` 확인
- 승인 시 kubectl-ai에 해결 진행 지시 주입

구현 범위:

- Orchestrator 상태 머신 추가
- 문제 감지 규칙 추가
- custom prompt에 주입할 타입/필드 계약 템플릿 추가
- 트러블슈팅 검색용 Agent prompt builder 추가
- 자동 해결용 Agent prompt builder 추가
- 단위 테스트는 문제 감지기와 prompt builder 중심으로 작성

완료 기준:

- `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff` 문구가 출력되면 트러블슈팅 제안이 뜬다.
- 사용자가 `y`를 입력하면 Agent가 `trouble_shooting_match_runbook` 사용을 시도한다.
- 사용자가 자동 해결을 승인하면 Agent가 먼저 상태 확인 명령을 수행하고 변경 전 승인을 요청한다.

### 2단계: log_analyzer와 trouble_shooting 분리

목표:

- 현재 `internal/loganalyzer`에 섞여 있는 RAG/runbook/조치 생성 책임을 `trouble_shooting` 구성으로 분리한다.

구현 범위:

- `internal/troubleshooting` 패키지 추가
- `cmd/trouble-shooting-server` 추가 여부 결정
- 기존 `rag_lookup`, `analyze_and_remediate`를 trouble_shooting의 `match_runbook`, `search_knowledge`, `build_remediation_plan`으로 이동 또는 대체
- `log_analyzer`는 `fetch_logs_from_file`, `fetch_events`, `query_prometheus`, `analyze_log_pattern`, `analyze_metric_pattern`, `summarize_observation` 중심으로 정리
- Prometheus 연동 도구 `query_prometheus`, `analyze_metric_pattern` 추가
- MCP 설정에 `log-analyzer`와 `trouble-shooting` 서버를 별도 등록

완료 기준:

- kubectl-ai custom prompt에서 두 도구군의 책임이 명확히 구분된다.
- 로그가 필요한 문제는 `log_analyzer`를 먼저 호출하고, 해결책 검색은 `trouble_shooting`을 호출한다.

### 3단계: 구조화된 트러블슈팅 결과 렌더링

목표:

- Orchestrator 또는 kubectl-ai가 trouble_shooting 결과를 구조화 데이터로 받아 일관되게 출력한다.

구현 범위:

- `TroubleshootingClient` 추가
- trouble_shooting MCP HTTP 호출 또는 패키지 직접 호출 방식 결정
- `TroubleshootingSearchResult`와 `RemediationPlan` 전용 formatter 추가
- `TroubleshootingCase`/`PlanStep` 필드 확장

완료 기준:

- 트러블슈팅 결과가 원인, 근거, 조치, 위험도, 출처로 일관되게 출력된다.
- 자동 해결 프롬프트가 `RemediationPlan`의 특정 step을 포함해 생성된다.

### 4단계: Runbook 품질 개선

목표:

- 단순 문장형 `resolution`을 실행 가능한 진단/조치 단계로 분해한다.

구현 범위:

- runbook YAML schema 확장
- template 변수 검증
- command template 렌더링
- 위험도와 자동 실행 가능 여부 분리
- rollback/검증 단계 추가

완료 기준:

- 각 runbook case에 diagnostic, remediation, verification, rollback 단계가 포함된다.
- 자동 해결 후보는 `automatic`, `requires_confirmation`, `risk_level`로 필터링된다.

### 5단계: 운영 이슈 RAG 활성화

목표:

- 운영 중 export한 이슈와 runbook/문서를 knowledge base로 인덱싱하고, 현재 `ProblemSignal`과 유사한 과거 사례를 검색한다.

구현 범위:

- `export_issue`, `import_issues`, `index_knowledge`, `search_knowledge` 구현
- `cmd/trouble-shooting-server/main.go` 또는 trouble_shooting 설정에 RAG 설정 추가
- `OpenAIEmbedder` + `ChromemStore` 실행 경로 연결
- persistent directory 지원 여부 검토
- embedding batch indexing 적용
- keyword fallback

완료 기준:

- 운영 이슈 export 파일이 knowledge base에 적재된다.
- 같은 의미의 다른 표현으로 검색해도 관련 운영 이슈/문서/사례가 상위에 나온다.
- vector search 장애 시 keyword fallback 또는 runbook 매칭으로 degraded 동작한다.

### 6단계: 안전장치와 운영성

목표:

- 자동 해결의 안전성과 추적성을 높인다.

구현 범위:

- 자동 해결 지시와 사용자 승인 로그 기록
- 민감정보 마스킹 범위 확장
- dry-run 가능 명령은 먼저 dry-run 수행하도록 prompt 강화
- `--disable-auto-remediate` 또는 config option 추가
- 트러블슈팅 검색/자동 해결 audit log 추가
- 이슈 export 시 원문 로그 포함 여부, 마스킹 여부, 실행 결과 포함 여부를 설정으로 제어

완료 기준:

- 운영 환경에서 자동 해결 기능을 끌 수 있다.
- 누가 어떤 트러블슈팅 결과를 보고 어떤 자동 해결을 승인했는지 로그로 확인할 수 있다.

## 테스트 방법

### 1. 빌드 확인

```bash
make build-log-analyzer
make build-k8s-assistant
```

### 2. log-analyzer-server 실행

로그/이벤트/메트릭 분석용 서버를 실행한다.

```bash
./bin/log-analyzer-server \
  --port 9090 \
  --log-dir /var/log/filebeat \
  --prometheus-url http://prometheus.monitoring.svc:9090
```

현재 코드에는 runbook 로딩이 `log-analyzer-server`에 남아 있다. 분리 후에는 `trouble-shooting-server`에서 runbook을 로딩한다.

```bash
./bin/trouble-shooting-server
```

기본 설정 파일 경로는 `~/.k8s-assistant/trouble-shooting.yaml`이다. 예시 파일은 `config/trouble-shooting.yaml`에 있으며, 필요하면 명시적으로 지정할 수 있다.

```bash
./bin/trouble-shooting-server --config config/trouble-shooting.yaml
```

동일 설정은 개별 flag로도 재정의할 수 있다. CLI flag가 config file 값보다 우선한다.

```bash
./bin/trouble-shooting-server \
  --port 9091 \
  --runbook-dir internal/troubleshooting/runbooks \
  --knowledge-provider qdrant \
  --embedding-url http://1.201.177.120:4000 \
  --reranker-enabled=true \
  --reranker-url http://1.201.177.120:4000 \
  --rag-mode hybrid \
  --qdrant-url http://localhost:6333 \
  --qdrant-collection k8s_troubleshooting_runbooks_v1 \
  --knowledge-dir ~/.k8s-assistant/troubleshooting/kb \
  --issue-dir ~/.k8s-assistant/troubleshooting/issues
```

runbook을 Qdrant에 업로드할 때도 같은 설정 파일을 사용할 수 있다.
이 업로드 도구는 현재 프로젝트 런타임 기능이 아니라 동작 검증과 초기 데이터 적재를 위한 helper다.
Qdrant에는 vector가 저장되어야 하므로 업로드 과정에서 embedding endpoint를 호출한다.

```bash
./bin/troubleshooting-upload
```

예시 파일을 직접 지정하려면 `./bin/troubleshooting-upload --config config/trouble-shooting.yaml`처럼 실행한다.

기대 출력:

```text
log-analyzer MCP server starting on port 9090
loaded 11 runbook cases
trouble-shooting MCP server starting on port 9091
```

### 3. kubectl-ai MCP 설정 확인

`~/.k8s-assistant/mcp.yaml`에 사용할 MCP 서버만 선언한다. `log-analyzer`와 `trouble-shooting`은 모두 선택 사항이며, 이 파일에 있는 서버만 kubectl-ai MCP 설정으로 동기화된다.

```yaml
servers:
  - name: trouble-shooting
    url: http://localhost:9091/mcp
    use_streaming: true
    timeout: 60
```

프로젝트의 예시는 `config/mcp.yaml`에 있다. 두 서버를 모두 쓰려면 두 항목을 모두 남기고, trouble-shooting만 쓰려면 `log-analyzer` 항목을 제거한다.

### 4. k8s-assistant에서 직접 runbook 매칭

별도 터미널에서 MCP client 모드로 실행한다.

```bash
./bin/k8s-assistant \
  --llm-provider openai \
  --model gpt-4o \
  --mcp-client
```

프롬프트에서 다음처럼 질문한다.

```text
다음 ProblemSignal을 기준으로 trouble_shooting_match_runbook 도구를 사용해서 트러블슈팅 사례를 찾아줘. 아직 조치는 실행하지 마.

ProblemSignal:
- detection_types: ["CrashLoopBackOff", "OOMKilled"]
- severity: critical
- confidence: high
- summary: Pod이 CrashLoopBackOff 상태이고 OOMKilled 이벤트가 보인다.
- target.namespace: default
- target.kind: Pod
- target.name: app-pod
```

기대 결과:

- `CrashLoopBackOff - 메모리 부족 (OOMKilled)` 사례가 상위에 나온다.
- 원인, 해결책, 출처가 한국어로 요약된다.
- Kubernetes 변경 명령은 실행하지 않는다.

이 테스트는 구조화 runbook 매칭 테스트다. 이후 같은 `ProblemSignal`로 `trouble_shooting_search_knowledge`를 호출해 운영 이슈 RAG 검색 품질도 별도로 검증한다.

### 5. log_analyzer 파일 로그 분석 테스트

Filebeat 또는 수집된 파일 로그가 준비된 경우 다음처럼 질의한다.

```text
default 네임스페이스의 app-pod 로그를 파일 로그 소스에서 최근 30분 범위로 분석해줘. 긴 원문 전체를 출력하지 말고 에러 패턴, 대표 evidence, ProblemSignal만 요약해줘.
```

기대 결과:

- `fetch_logs_from_file`이 namespace/pod/container/time range 기준으로 로그를 좁힌다.
- 긴 로그 전체가 LLM 응답에 노출되지 않는다.
- `analyze_log_pattern`이 `CrashLoopBackOff`, `Timeout`, `DiskFull`, `PermissionDenied` 같은 패턴을 탐지한다.
- `summarize_observation` 결과가 `ProblemSignal` 형태로 정리된다.

### 6. log_analyzer Prometheus 분석 테스트

Prometheus가 준비된 경우 다음처럼 질의한다.

```text
default 네임스페이스 app-pod의 최근 30분 CPU, memory, restart count, request latency를 Prometheus에서 조회해서 이상 패턴을 분석해줘. 필요한 경우에만 trouble_shooting 조치 계획을 찾아줘.
```

기대 결과:

- `query_prometheus`가 사전에 정의된 PromQL 또는 custom prompt가 구성한 PromQL을 실행한다.
- `analyze_metric_pattern`이 임계치 초과, 증가 추세, restart count 증가, latency percentile 상승을 탐지한다.
- 결과는 `MetricPattern`과 `ProblemSignal`로 요약된다.
- 메트릭 분석 자체에는 RAG를 사용하지 않는다.

### 7. log_analyzer와 trouble_shooting 연계 테스트

Filebeat 로그 또는 테스트 로그가 준비된 경우 다음처럼 질의한다.

```text
default 네임스페이스의 app-pod 로그를 log_analyzer로 분석해서 ProblemSignal을 만들고, 그 결과를 trouble_shooting_match_runbook에 넘겨 원인과 조치 방안을 찾아줘. 아직 변경 작업은 하지 마.
```

기대 결과:

- `fetch_logs_from_file` 또는 현재 구현의 `fetch_logs`에 해당하는 로그 조회가 수행된다.
- 패턴 탐지 결과가 나온다.
- `ProblemSignal` 형태로 증상이 요약된다.
- trouble_shooting에서 구조화 runbook이 매칭된다.
- `RemediationPlan` 또는 조치 단계가 제안된다.

### 8. 운영 이슈 export/import/index 테스트

조치가 끝난 운영 이슈를 저장하고 RAG 지식베이스에 반영하는 흐름을 검증한다.

```text
방금 분석한 ProblemSignal, 로그 요약, 메트릭 요약, 선택된 조치 계획, 실행 결과를 trouble_shooting_export_issue로 저장해줘. 민감정보는 마스킹하고 raw log는 포함하지 마.
```

기대 결과:

- issue export 파일이 markdown/yaml/json 중 설정된 포맷으로 생성된다.
- `ProblemSignal`, `LogSummary`, `MetricSummary`, `RemediationPlan`, `ExecutionResult`, `Tags`, `CreatedAt`이 포함된다.
- raw log는 설정이 꺼져 있으면 포함되지 않는다.
- export 직후 또는 재시작 시 `import_issues`/`index_knowledge`가 해당 이슈를 지식베이스에 반영한다.

검색 검증:

```text
이전 운영 이슈 중 CrashLoopBackOff와 OOMKilled가 같이 발생했고 memory limit 조정으로 해결한 사례를 trouble_shooting_search_knowledge로 찾아줘.
```

기대 결과:

- 방금 export한 운영 이슈가 top-k 안에 나온다.
- 검색 결과에 출처 파일, 유사도, 원인, 해결 내용이 포함된다.
- 검색 결과는 조치 실행이 아니라 `build_remediation_plan`의 근거로만 사용된다.

### 9. MCP 서버에 직접 요청하는 테스트

MCP Streamable HTTP는 일반 REST API처럼 단순 호출하기 어렵다. 빠른 수동 검증은 k8s-assistant를 통해 수행하는 것이 가장 간단하다.

서버 내부 로직만 빠르게 확인하려면 Go 테스트 또는 작은 개발용 command를 추가하는 방식이 좋다.

테스트 코드 방향:

```go
func TestMatchRunbookOOMKilled(t *testing.T) {
    ctx := context.Background()
    store := loganalyzer.NewSimpleKeywordStore()
    cases := loadDefaultRunbookCasesForTest(t)
    require.NoError(t, store.Index(ctx, cases))

    analyzer := loganalyzer.NewAnalyzer(fakeFetcher{}, loganalyzer.NewPatternDetector(), store)
    svc := troubleshooting.NewService(store)
    result, err := svc.MatchRunbook(ctx, troubleshooting.SearchRequest{
        Signal: diagnostic.ProblemSignal{
            DetectionTypes: []diagnostic.DetectionType{
                diagnostic.DetectionCrashLoopBackOff,
                diagnostic.DetectionOOMKilled,
            },
            Severity: diagnostic.SeverityCritical,
            Confidence: diagnostic.ConfidenceHigh,
            Summary: "Pod이 CrashLoopBackOff이고 OOMKilled 이벤트가 발생함",
            Target: diagnostic.KubernetesTarget{
                Namespace: "default",
                Kind: "Pod",
                Name: "app-pod",
            },
        },
        TopK: 3,
    })

    require.NoError(t, err)
    require.NotEmpty(t, result.Cases)
    require.Contains(t, result.Cases[0].Title, "OOMKilled")
}
```

### 10. runbook/RAG 검색 품질 테스트 케이스

다음 질의를 고정 테스트셋으로 둔다.

| 질의 | 기대 상위 사례 |
|------|----------------|
| `Pod이 CrashLoopBackOff이고 OOMKilled가 보인다` | CrashLoopBackOff - 메모리 부족 |
| `이미지 pull이 실패하고 ErrImagePull 상태다` | ImagePullBackOff |
| `Pod이 Pending이고 스케줄링이 안 된다` | Pending Pod - 리소스 부족 |
| `no space left on device 로그가 반복된다` | Disk Full |
| `readiness probe가 실패해서 service endpoint가 없다` | Liveliness/Readiness Probe 실패 또는 Service 엔드포인트 없음 |
| `요청 timeout과 deadline exceeded가 많다` | Timeout - 느린 응답 시간 |

품질 기준:

- top-1 또는 top-3 안에 기대 사례가 포함된다.
- 원인과 해결책이 비어 있지 않다.
- 출처가 포함된다.
- 자동 해결 가능 여부가 명확히 구분된다.

RAG 검색 품질은 같은 테스트셋에 대해 자연어 변형 질의를 추가한다. 단, RAG 품질은 trouble_shooting 지식 검색 품질이고 log_analyzer 분석 품질과 분리해서 평가한다.

### 11. 자동 해결 플로우 수동 테스트 시나리오

MVP 구현 후 다음 시나리오로 확인한다.

1. kubectl-ai가 custom prompt 규칙에 따라 `CrashLoopBackOff` 또는 `OOMKilled`를 `ProblemSignal`로 인식하도록 한다.
2. CLI가 `해결 방법을 찾아볼까요? (y/n):`를 표시하는지 확인한다.
3. `y` 입력 시 `ProblemSignal` 기반 runbook 매칭 지시가 Agent에 전달되는지 확인한다.
4. Agent가 `trouble_shooting_match_runbook`을 호출하는지 확인한다.
5. 조치 계획 출력 후 `자동으로 해결을 진행할까요? (y/n):`가 표시되는지 확인한다.
6. `n` 입력 시 아무 변경 없이 일반 대화로 돌아오는지 확인한다.
7. 다시 같은 증상에서 `y` 후 자동 해결도 `y`로 승인한다.
8. Agent가 `RemediationPlan`을 기준으로 먼저 `kubectl describe`, `kubectl logs`, `kubectl get` 등 확인 명령을 수행하는지 확인한다.
9. 실제 변경 명령 전 기존 kubectl-ai 승인 흐름이 동작하는지 확인한다.

## 구현 시 우선순위 제안

1. custom prompt에 주입할 `ProblemSignal`/`DetectionType`/`KubernetesTarget` 계약 정의
2. `log_analyzer`와 `trouble_shooting` 책임 분리
3. Orchestrator의 문제 감지 및 사용자 확인 상태 머신
4. `trouble_shooting` runbook 매칭 지시/자동 해결 지시 prompt builder
5. 반복 제안 방지 상태
6. 감지기와 prompt builder 단위 테스트
7. runbook schema 확장
8. 구조화 troubleshooting client
9. 운영 이슈 export/import/index
10. trouble_shooting RAG 검색 활성화

가장 먼저 구현할 MVP는 Orchestrator가 직접 trouble_shooting 서버를 호출하지 않고 kubectl-ai Agent에 `ProblemSignal` 기반 `trouble_shooting_match_runbook` 사용 지시를 다시 주입하는 방식이 적합하다. 현재 프로젝트의 Agent/MCP 연결을 그대로 활용할 수 있고, 자동 해결도 기존 kubectl-ai 승인 흐름 안에서 처리할 수 있기 때문이다. 동시에 custom prompt에 `log_analyzer`와 `trouble_shooting`의 역할/필드 계약을 주입해 Agent가 두 컴포넌트를 혼동하지 않도록 해야 한다.
