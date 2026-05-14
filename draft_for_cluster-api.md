# Cluster-API 기반 서비스 AIOps 설계 초안

## 목적

cluster-api 기반으로 Kubernetes 위에서 운영되는 자체 서비스를 AIOps로 관리한다. 대상은 OpenStack provider 기반으로 프로비저닝되는 클러스터와 그 위에서 동작하는 자체 컨트롤러 및 CRD들이다. OpenStack 레이어 자체는 범위 밖이고, Kubernetes 리소스·자체 컨트롤러 상태·로그 분석이 핵심이다.

이 문서는 기존 k8s-assistant 구조(ReAct 루프, log-analyzer, trouble-shooting)를 바탕으로 cluster-api와 자체 컨트롤러를 어떻게 통합하는 것이 최선인지 방안을 비교하고 권장 구조를 제시한다.

---

## 1. 환경 분석

### 1.1 운영 대상 정의

```
Management Cluster (cluster-api 설치)
  ├─ cluster-api core CRDs
  │    Cluster, Machine, MachineDeployment, MachineSet
  │    MachineHealthCheck, MachinePool
  ├─ OpenStack provider CRDs
  │    OpenStackCluster, OpenStackMachine, OpenStackMachineTemplate
  ├─ 자체 CRDs (다수)
  │    자체 컨트롤러가 관리하는 리소스 (도메인 정의 필요)
  └─ 자체 컨트롤러 Deployments (다수)

Workload Clusters (cluster-api로 프로비저닝)
  └─ 클러스터별 워크로드, 네임스페이스, 서비스
```

### 1.2 cluster-api 리소스의 상태 표현 방식

cluster-api 리소스는 표준 Kubernetes `.status.conditions`를 사용하지 않고 독자적인 condition 체계를 사용한다. AIOps가 이를 해석하려면 다음을 이해해야 한다.

**Cluster 주요 Condition:**

| Condition | 의미 |
|---|---|
| `ControlPlaneReady` | Control plane이 초기화 완료되고 API 서버가 응답하는 상태 |
| `InfrastructureReady` | 인프라 오브젝트(OpenStackCluster 등)가 준비 완료된 상태 |
| `MachinesReady` | 모든 Machine이 Running 상태 |
| `MachinesCreated` | 모든 Machine 오브젝트가 생성된 상태 |
| `Ready` | 위 조건들이 모두 true |

**Machine Phase:**

| Phase | 의미 |
|---|---|
| `Provisioning` | 인프라 리소스 생성 중 (OpenStack VM 생성 등) |
| `Provisioned` | VM 생성 완료, 아직 Kubernetes node 미등록 |
| `Running` | Node가 클러스터에 합류하고 Ready |
| `Deleting` | 삭제 중 |
| `Failed` | 프로비저닝/조인 실패 |

**KubeadmControlPlane 주요 Condition:**

| Condition | 의미 |
|---|---|
| `Available` | 최소 하나의 control plane machine이 Ready |
| `CertificatesAvailable` | PKI 인증서 발급 완료 |
| `ControlPlaneComponentsHealthy` | kube-apiserver, etcd 등 정상 |
| `EtcdClusterHealthy` | etcd 클러스터 정상 |
| `MachinesReady` | 모든 control plane machine이 Ready |

**MachineDeployment Phase:**

| Phase | 의미 |
|---|---|
| `ScalingUp` | 레플리카 증가 중 |
| `ScalingDown` | 레플리카 감소 중 |
| `Running` | 원하는 레플리카 수만큼 Running |
| `Failed` | 스케일링 실패 |

### 1.3 자체 컨트롤러의 특성

자체 컨트롤러는 일반적으로 다음 패턴을 따른다:
- Reconcile 루프 기반 상태 조정
- `.status.conditions`에 Condition 타입/메시지/이유 기록
- 컨트롤러 로그: `reconcile` 진입, `requeue`, `error`, `created/updated/deleted` 이벤트
- 공통 에러 패턴: 의존 리소스 미존재, 외부 API 타임아웃, 인증 실패, Rate Limit

---

## 2. 기존 k8s-assistant 구조와의 관계

현재 k8s-assistant의 역할 분리:

```
사용자 질의
  ↓
k8s-assistant ReAct 루프 (kubectl, bash)
  ├─ log-analyzer MCP: 로그/이벤트/메트릭 관측·패턴 분석
  └─ trouble-shooting MCP: runbook 매칭, RAG 검색, 조치 계획
```

cluster-api + 자체 컨트롤러 운영을 추가하면 다음이 필요해진다:

| 추가 필요 | 이유 |
|---|---|
| cluster-api 리소스 상태 해석 | `kubectl get cluster -A` 결과에서 Condition 의미를 LLM이 이해해야 함 |
| 자체 CRD 상태 해석 | 자체 Condition 타입과 Phase가 표준 Kubernetes와 다름 |
| 자체 컨트롤러 로그 패턴 | reconcile 에러, requeue 루프, 의존성 실패 패턴은 도메인별로 다름 |
| cluster-api 전용 runbook | Machine Failed, ControlPlane 불안정, Cluster 업그레이드 실패 등 |
| 멀티 클러스터 컨텍스트 | management cluster와 workload cluster 간 컨텍스트 전환 |

---

## 3. 방안 비교

### 방안 A: System Prompt + Runbook 확장만 (최소 변경)

기존 k8s-assistant 구조를 유지하고 system prompt와 runbook만 추가한다.

```
변경 범위:
  - prompts/default.tmpl: cluster-api CRD 목록, Condition 의미, Phase 해석 지침 추가
  - trouble-shooting runbooks: cluster-api 장애 시나리오 추가
  - (선택) log-analyzer: 자체 컨트롤러 로그 패턴 추가
```

**장점:**
- 구현 비용 최소
- 기존 ReAct 루프와 MCP 구조 변경 없음
- 빠르게 시작 가능

**단점:**
- System prompt 비대화 → LLM context 소모 증가, 성능 저하
- cluster-api 리소스 간 관계(Cluster→MachineDeployment→MachineSet→Machine) 추론을 LLM에 위임 → 오류 가능
- 자체 CRD가 많아질수록 prompt 관리 어려움
- 멀티 클러스터 컨텍스트 전환 지원 약함

**적합한 경우:** CRD 수가 적고, cluster-api 표준 리소스만 다루는 경우

---

### 방안 B: cluster-api MCP 서버 신설

cluster-api 및 자체 컨트롤러 전용 MCP 서버를 새로 만든다.

```
cluster-api-server (신규 MCP)
  ├─ get_cluster_status      - Cluster + Condition 요약
  ├─ get_machine_status      - Machine Phase + 이벤트 요약
  ├─ get_controlplane_status - KubeadmControlPlane + etcd/kube 상태
  ├─ get_custom_resource     - 자체 CRD 상태 + Condition 요약
  ├─ get_controller_logs     - 자체 컨트롤러 로그 분석
  ├─ list_clusters           - 전체 Cluster 목록 (namespace별)
  └─ switch_workload_context - workload cluster kubeconfig 취득·전환
```

**장점:**
- cluster-api 리소스 관계와 Condition 해석을 서버에서 담당 → LLM context 절감
- 자체 CRD 스키마를 서버에서 관리 → prompt 비대화 없음
- 멀티 클러스터 컨텍스트 전환 지원
- log-analyzer, trouble-shooting과 역할 명확히 분리

**단점:**
- 새 MCP 서버 구현 비용
- `k8s.io/client-go`로 cluster-api CRD를 직접 조회하는 코드 필요
- 자체 CRD 스키마 변경 시 서버 코드도 갱신 필요

**적합한 경우:** 자체 CRD가 다수이고, cluster-api 리소스 관계 파악이 중요한 경우

---

### 방안 C: log-analyzer 확장

기존 log-analyzer에 cluster-api 리소스 조회·자체 컨트롤러 로그 분석 도구를 추가한다.

```
log-analyzer-server (기존 + 확장)
  기존: fetch_logs, analyze_pattern, rag_lookup
  추가: query_cluster_status, get_machine_events, analyze_controller_logs
```

**장점:**
- 기존 MCP 서버 재사용, 신규 서버 불필요

**단점:**
- log-analyzer의 역할이 "관측 데이터 수집"에서 벗어남 (리소스 상태 조회는 다른 책임)
- cluster-api 리소스 조회와 로그 분석이 한 서버에 혼재
- `draft_troubleshooting_v1.md`의 역할 분리 원칙과 충돌

**적합한 경우:** 빠른 프로토타이핑, 서버 수를 최소화해야 하는 경우

---

### 방안 D: 계층화된 컨텍스트 주입 (권장)

System Prompt 확장(방안 A) + cluster-api MCP 서버 신설(방안 B)을 계층화한다.

```
계층 1: System Prompt (정적 지식)
  - cluster-api CRD 목록, Phase/Condition 의미 요약
  - 자체 CRD 목록, 각 컨트롤러 책임 한 줄 요약
  - 리소스 간 소유 관계 (ownerReference 트리)
  - 진단 우선순위 규칙 (어떤 리소스를 먼저 볼 것인지)

계층 2: cluster-api-server (동적 데이터)
  - 실시간 클러스터/머신 상태 조회
  - 자체 CRD 상태 및 Condition 조회
  - 자체 컨트롤러 로그 조회 및 패턴 분석
  - 멀티 클러스터 컨텍스트 전환

계층 3: trouble-shooting (운영 지식)
  - cluster-api 장애 runbook 추가
  - 자체 컨트롤러 장애 runbook 추가
  - 과거 운영 이슈 RAG
```

**장점:**
- 정적 지식(prompt)과 동적 데이터(MCP tool)를 명확히 분리
- LLM context 최적화: 매번 전체 CRD 스키마를 prompt에 넣지 않아도 됨
- trouble-shooting runbook에 cluster-api 장애 시나리오 추가 가능
- 기존 log-analyzer, trouble-shooting 역할 유지
- 단계적 구현 가능 (System Prompt만 먼저, MCP 서버는 나중에)

**단점:**
- 방안 B의 구현 비용 포함
- 두 계층의 정합성 관리 필요 (CRD 변경 시 prompt와 서버 동시 갱신)

---

## 4. 권장 방안: 방안 D 단계적 구현

### 4.1 기본 원칙

1. **cluster-api 리소스는 kubectl로 조회, 해석은 LLM이 담당한다.** cluster-api CRD를 다루는 데 새로운 도구가 필요한 게 아니라, LLM이 Condition과 Phase를 올바르게 해석하는 것이 핵심이다.

2. **자체 컨트롤러 로그는 log-analyzer가 담당한다.** 새 MCP 서버가 필요한 것이 아니라 log-analyzer에 자체 컨트롤러 로그 패턴을 추가하는 것으로 충분하다.

3. **cluster-api 특화 진단 도구는 cluster-api-server로 분리한다.** 리소스 관계 파악, 멀티 클러스터 컨텍스트 전환, 클러스터 헬스 요약은 별도 서버가 더 적합하다.

4. **trouble-shooting runbook에 cluster-api 장애 시나리오를 추가한다.** 기존 runbook 확장으로 처리한다.

### 4.2 전체 구조

```
사용자 질의
  ↓
k8s-assistant ReAct 루프
  ├─ kubectl/bash: cluster-api CRD 직접 조회
  │    kubectl get cluster -A
  │    kubectl get machine -A
  │    kubectl get <자체CRD> -A
  │    kubectl describe <리소스>
  │
  ├─ cluster-api-server MCP (신규)
  │    ├─ get_cluster_health      - Cluster + 하위 리소스 헬스 요약
  │    ├─ get_machine_health      - Machine Phase + 이유 요약
  │    ├─ get_custom_resource     - 자체 CRD 상태 + Condition 구조화 요약
  │    ├─ list_workload_clusters  - 전체 workload cluster 목록
  │    └─ get_workload_kubeconfig - workload cluster kubeconfig 취득
  │
  ├─ log-analyzer MCP (확장)
  │    기존: fetch_logs, analyze_pattern, fetch_events
  │    추가: analyze_controller_logs  - 자체 컨트롤러 reconcile 에러 패턴
  │          query_prometheus          - 클러스터 메트릭 (기존)
  │
  └─ trouble-shooting MCP (runbook 추가)
       기존: match_runbook, search_knowledge, build_remediation_plan
       추가 runbook: cluster-api 장애, 자체 컨트롤러 장애
```

---

## 5. System Prompt 확장 설계

### 5.1 cluster-api 지식 주입

`prompts/default.tmpl` 또는 `ExtraPromptPaths`에 추가할 내용:

```
## Cluster-API 리소스 해석 지침

### 리소스 소유 관계
Cluster → KubeadmControlPlane (control plane)
Cluster → MachineDeployment → MachineSet → Machine (worker)
Machine → Node (Kubernetes node)
OpenStackCluster → Cluster (infra provider)
OpenStackMachine → Machine (infra provider)

### 진단 순서
클러스터 문제를 진단할 때는 다음 순서로 조회한다:
1. `kubectl get cluster -A` → Phase, Ready condition 확인
2. `kubectl get kcp -A` (KubeadmControlPlane) → ControlPlaneComponentsHealthy, EtcdClusterHealthy 확인
3. `kubectl get machine -A` → Phase가 Provisioning/Failed인 Machine 확인
4. `kubectl describe machine <name>` → FailureReason, FailureMessage, Events 확인
5. 해당 Machine의 컨트롤러 로그 확인

### Condition 해석 규칙
- Condition.Reason이 있으면 반드시 포함해 해석한다.
- Condition.Message가 있으면 그대로 인용한다.
- Status=False인 Condition이 있으면 최우선으로 보고한다.

### Phase 해석 규칙
- Machine Phase=Provisioning이 5분 이상 지속되면 OpenStack VM 생성 문제를 의심한다.
- Machine Phase=Failed이면 FailureReason, FailureMessage, Events를 함께 조회한다.
- Cluster Phase=Provisioning이 10분 이상이면 control plane 초기화 문제를 의심한다.

### 자체 컨트롤러 조회 방법
자체 CRD를 조회할 때는 다음을 항상 확인한다:
- `.status.conditions` (Ready, Degraded 등 자체 정의 Condition)
- `.status.phase` (있는 경우)
- `.status.failureReason`, `.status.failureMessage` (있는 경우)
- 관련 Kubernetes Event (`kubectl get events --field-selector involvedObject.name=<name>`)
- 컨트롤러 Pod 로그 (에러 수준 필터링)
```

### 5.2 자체 CRD 목록 주입 방법

자체 CRD가 다수이므로 전체를 system prompt에 포함하면 context가 비대해진다.

**권장 방식: 카테고리별 한 줄 요약만 prompt에 포함**

```
## 자체 CRD 목록

### 네트워크 관련
- <CRD종류>: <한 줄 책임 설명>
- ...

### 컴퓨팅 관련
- <CRD종류>: <한 줄 책임 설명>
- ...

상세 스키마가 필요하면 `kubectl get crd <name> -o jsonpath='{.spec.versions[0].schema}'`로 조회한다.
```

**상세 스키마는 필요 시 kubectl로 조회**하도록 유도해 prompt를 최소화한다.

---

## 6. cluster-api-server MCP 설계

### 6.1 제공 도구

| 도구 | 입력 | 출력 | 목적 |
|---|---|---|---|
| `get_cluster_health` | namespace (옵션), cluster name (옵션) | ClusterHealthSummary | Cluster + KCP + Machine 상태를 한 번에 구조화 요약 |
| `get_machine_health` | namespace, machine name (옵션) | MachineHealthSummary[] | Machine Phase, FailureReason, Event 요약 |
| `get_custom_resource` | group, version, kind, namespace, name | CustomResourceSummary | 자체 CRD의 status.conditions + phase + failureReason 구조화 |
| `list_workload_clusters` | - | ClusterList | 전체 workload cluster 목록 (name, namespace, phase, ready) |
| `get_workload_kubeconfig` | namespace, cluster name | kubeconfig 경로 또는 context name | workload cluster kubectl 접근용 kubeconfig 추출 |

### 6.2 핵심 타입

```go
type ClusterHealthSummary struct {
    Name                 string            `json:"name"`
    Namespace            string            `json:"namespace"`
    Phase                string            `json:"phase"`
    Ready                bool              `json:"ready"`
    Conditions           []ConditionSummary `json:"conditions"`
    ControlPlane         ControlPlaneSummary `json:"control_plane"`
    Workers              []MachineHealthSummary `json:"workers"`
    FailureReason        string            `json:"failure_reason,omitempty"`
    FailureMessage       string            `json:"failure_message,omitempty"`
}

type ConditionSummary struct {
    Type    string `json:"type"`
    Status  string `json:"status"`   // "True" | "False" | "Unknown"
    Reason  string `json:"reason,omitempty"`
    Message string `json:"message,omitempty"`
    Severity string `json:"severity,omitempty"`
}

type ControlPlaneSummary struct {
    Ready              bool              `json:"ready"`
    Replicas           int32             `json:"replicas"`
    ReadyReplicas      int32             `json:"ready_replicas"`
    Conditions         []ConditionSummary `json:"conditions"`
    EtcdHealthy        bool              `json:"etcd_healthy"`
}

type MachineHealthSummary struct {
    Name           string            `json:"name"`
    Namespace      string            `json:"namespace"`
    Phase          string            `json:"phase"`
    NodeRef        string            `json:"node_ref,omitempty"`
    FailureReason  string            `json:"failure_reason,omitempty"`
    FailureMessage string            `json:"failure_message,omitempty"`
    Conditions     []ConditionSummary `json:"conditions"`
    RecentEvents   []EventSummary    `json:"recent_events,omitempty"`
}

type CustomResourceSummary struct {
    Kind             string             `json:"kind"`
    Name             string             `json:"name"`
    Namespace        string             `json:"namespace"`
    Phase            string             `json:"phase,omitempty"`
    Ready            *bool              `json:"ready,omitempty"`
    Conditions       []ConditionSummary `json:"conditions"`
    FailureReason    string             `json:"failure_reason,omitempty"`
    FailureMessage   string             `json:"failure_message,omitempty"`
    RawStatus        map[string]any     `json:"raw_status,omitempty"`
}
```

### 6.3 구현 방식

`k8s.io/client-go`의 dynamic client를 사용해 cluster-api CRD를 조회한다.

```go
// cluster-api 리소스는 dynamic client로 조회
dynamicClient.Resource(schema.GroupVersionResource{
    Group:    "cluster.x-k8s.io",
    Version:  "v1beta1",
    Resource: "clusters",
}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})

// 자체 CRD도 동일하게 dynamic client 사용
// GVR은 config 파일에서 정의
```

**자체 CRD 설정 파일 (`cluster-api.yaml`):**

```yaml
custom_resources:
  - group: <자체그룹>.io
    version: v1alpha1
    kind: <자체CRD>
    resource: <복수형>
    health_fields:
      phase: .status.phase
      ready_condition: Ready
      failure_reason: .status.failureReason
      failure_message: .status.failureMessage

controllers:
  - name: <컨트롤러명>
    deployment: <deployment명>
    namespace: <namespace>
    managed_crds:
      - <CRD kind1>
      - <CRD kind2>
```

이 설정으로 자체 CRD 스키마가 변경돼도 서버 코드 수정 없이 config만 갱신한다.

### 6.4 멀티 클러스터 컨텍스트 전환

workload cluster에 대한 kubectl 명령은 kubeconfig가 필요하다.

```
get_workload_kubeconfig 흐름:
  1. management cluster에서 cluster-api Secret 조회
     kubectl get secret <cluster-name>-kubeconfig -n <namespace> -o jsonpath='{.data.value}' | base64 -d
  2. 임시 파일로 저장
  3. KUBECONFIG 경로 반환
  4. ReAct 루프가 이후 kubectl 명령에 --kubeconfig 옵션 사용
```

---

## 7. log-analyzer 확장: 자체 컨트롤러 로그

### 7.1 추가 도구

`log-analyzer-server`에 다음 도구를 추가한다:

```
analyze_controller_logs
  입력: controller_name, namespace, since_seconds, max_lines
  출력: ControllerLogAnalysis
  목적: 자체 컨트롤러 reconcile 에러, requeue 루프, 의존성 실패 패턴 분석
```

### 7.2 컨트롤러 로그 패턴

일반적인 controller-runtime 기반 컨트롤러의 공통 에러 패턴:

```go
// ControllerLogPattern 추가
const (
    PatternReconcileError     = "reconcile_error"       // "Reconciler error" 키워드
    PatternRateLimited        = "rate_limited"           // "rate: Wait returned an error" 키워드
    PatternObjectNotFound     = "object_not_found"       // "not found" + resource name
    PatternDependencyNotReady = "dependency_not_ready"   // "waiting for" 또는 "not ready yet"
    PatternInfraError         = "infra_provider_error"   // OpenStack API 에러 패턴
    PatternWebhookError       = "webhook_error"          // "webhook" + 에러
    PatternRequeueLoop        = "requeue_loop"           // 짧은 시간 내 동일 오브젝트 반복 재시도
)
```

**Requeue 루프 감지 로직:**
같은 namespace/name 조합이 30초 이내에 5회 이상 reconcile에 진입하면 requeue 루프로 판단.

### 7.3 컨트롤러 로그 분석 결과 타입

```go
type ControllerLogAnalysis struct {
    ControllerName string                    `json:"controller_name"`
    Patterns       []ControllerLogPattern    `json:"patterns"`
    AffectedObjects []AffectedObject         `json:"affected_objects"`
    Summary        string                    `json:"summary"`
    RecommendedNext []NextAction             `json:"recommended_next"`
}

type ControllerLogPattern struct {
    Type        string          `json:"type"`
    Count       int             `json:"count"`
    Severity    Severity        `json:"severity"`
    Confidence  ConfidenceLevel `json:"confidence"`
    Description string          `json:"description"`
    Evidence    []Evidence      `json:"evidence"`
}

type AffectedObject struct {
    Kind      string `json:"kind"`
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
    ErrorCount int   `json:"error_count"`
    LastError string `json:"last_error"`
}
```

---

## 8. Trouble-shooting Runbook 확장

### 8.1 cluster-api 장애 시나리오 추가

기존 `internal/troubleshooting/runbooks` 에 다음 시나리오를 추가한다.

**cluster-api 전용 runbook 예시 구조:**

```yaml
cases:
  - id: capi-machine-stuck-provisioning
    title: "Machine이 Provisioning 상태에서 멈춤"
    symptoms:
      - "Machine.Phase=Provisioning"
      - "Provisioning 5분 이상 지속"
    detection_types:
      - "MachineProvisioningTimeout"
    cause: "OpenStack VM 생성 실패 또는 cloud-init 타임아웃"
    diagnostic_steps:
      - description: "Machine 이벤트 확인"
        command_template: "kubectl describe machine {{machine_name}} -n {{namespace}}"
        automatic: true
      - description: "OpenStackMachine 상태 확인"
        command_template: "kubectl get openstackmachine -n {{namespace}} -l cluster.x-k8s.io/machine-name={{machine_name}}"
        automatic: true
      - description: "CAPO(cluster-api-provider-openstack) 컨트롤러 로그 확인"
        command_template: "kubectl logs -n capi-system deployment/capo-controller-manager --tail=100 | grep {{machine_name}}"
        automatic: true
    remediation_steps:
      - description: "Machine 삭제 후 MachineDeployment가 재생성하도록 유도"
        command_template: "kubectl delete machine {{machine_name}} -n {{namespace}}"
        automatic: false
        requires_confirmation: true
        risk_level: "medium"
    verify_steps:
      - description: "새 Machine이 Running 상태로 전환되는지 확인"
        command_template: "kubectl get machine -n {{namespace}} -w"
        automatic: true
    risk_level: "medium"
    source: "cluster-api runbook"

  - id: capi-controlplane-not-ready
    title: "Control Plane이 Ready 상태가 아님"
    symptoms:
      - "Cluster.ControlPlaneReady=False"
      - "KubeadmControlPlane.Available=False"
    detection_types:
      - "ControlPlaneNotReady"
    cause: "etcd 불안정, kube-apiserver 시작 실패, 인증서 문제"
    diagnostic_steps:
      - description: "KubeadmControlPlane 상태 확인"
        command_template: "kubectl describe kcp -n {{namespace}}"
        automatic: true
      - description: "Control plane Machine 상태 확인"
        command_template: "kubectl get machine -n {{namespace}} -l cluster.x-k8s.io/control-plane=''"
        automatic: true
      - description: "CAPI 컨트롤러 로그 확인"
        command_template: "kubectl logs -n capi-system deployment/capi-controller-manager --tail=200"
        automatic: true
    risk_level: "high"
    source: "cluster-api runbook"

  - id: custom-controller-reconcile-loop
    title: "자체 컨트롤러 Reconcile 루프"
    symptoms:
      - "컨트롤러 로그에서 동일 오브젝트 반복 재시도"
      - "오브젝트 상태 변화 없음"
    detection_types:
      - "ControllerRequeueLoop"
    cause: "의존 리소스 미존재, 외부 API 실패, Finalizer 처리 오류"
    diagnostic_steps:
      - description: "대상 오브젝트 Condition 확인"
        command_template: "kubectl describe {{kind}} {{name}} -n {{namespace}}"
        automatic: true
      - description: "컨트롤러 로그에서 에러 추출"
        command_template: "kubectl logs -n {{controller_namespace}} deployment/{{controller_name}} --tail=200 | grep -i error"
        automatic: true
      - description: "Kubernetes Events 확인"
        command_template: "kubectl get events -n {{namespace}} --field-selector involvedObject.name={{name}}"
        automatic: true
    risk_level: "medium"
    source: "custom controller runbook"
```

### 8.2 DetectionType 확장

`internal/diagnostic/types.go`에 cluster-api 전용 DetectionType 추가:

```go
const (
    // cluster-api 관련
    DetectionMachineProvisioningTimeout DetectionType = "MachineProvisioningTimeout"
    DetectionMachineFailed              DetectionType = "MachineFailed"
    DetectionControlPlaneNotReady       DetectionType = "ControlPlaneNotReady"
    DetectionEtcdUnhealthy              DetectionType = "EtcdUnhealthy"
    DetectionClusterUpgradeFailed       DetectionType = "ClusterUpgradeFailed"
    DetectionMachineHealthCheckFailed   DetectionType = "MachineHealthCheckFailed"

    // 자체 컨트롤러 관련
    DetectionControllerRequeueLoop      DetectionType = "ControllerRequeueLoop"
    DetectionControllerDependencyMissing DetectionType = "ControllerDependencyMissing"
    DetectionControllerInfraError       DetectionType = "ControllerInfraError"
    DetectionCustomResourceFailed       DetectionType = "CustomResourceFailed"
    DetectionCustomResourceDegraded     DetectionType = "CustomResourceDegraded"
)
```

---

## 9. 멀티 클러스터 운영 패턴

### 9.1 Management Cluster vs Workload Cluster

```
진단 흐름:
  1. management cluster에서 cluster-api 리소스 상태 조회
     → Cluster, Machine, KCP 상태 파악
  2. 문제가 workload cluster 내부에 있으면 kubeconfig 전환
     → get_workload_kubeconfig로 kubeconfig 취득
     → kubectl --kubeconfig=<path> get pods -A 등 실행
  3. 자체 컨트롤러가 management cluster에 있으면 management cluster에서 로그 조회
  4. 자체 컨트롤러가 workload cluster에 있으면 workload kubeconfig로 전환 후 조회
```

### 9.2 System Prompt에 멀티 클러스터 규칙 추가

```
## 멀티 클러스터 진단 규칙

- cluster-api 리소스(Cluster, Machine, KCP)는 management cluster에서 조회한다.
- workload cluster 내부 리소스 조회가 필요하면 먼저 get_workload_kubeconfig를 호출한다.
- kubeconfig 전환 후에는 --kubeconfig 옵션을 명시하거나 KUBECONFIG 환경변수를 설정한다.
- 자체 컨트롤러가 어느 클러스터에 있는지 사용자에게 먼저 확인한다.
```

---

## 10. 단계별 구현 계획

### Phase 1: System Prompt + Runbook 확장 (즉시 시작 가능)

구현 범위:
- `prompts/default.tmpl`에 cluster-api CRD 목록, Condition/Phase 해석 지침 추가
- `internal/troubleshooting/runbooks`에 cluster-api 장애 시나리오 추가 (YAML)
- `internal/diagnostic/types.go`에 cluster-api/컨트롤러 DetectionType 추가
- trouble-shooting keyword store에 cluster-api 키워드 추가

완료 기준:
- `kubectl get cluster -A` 결과에서 ControlPlaneReady=False를 감지하면 trouble-shooting이 "ControlPlaneNotReady" runbook을 매칭한다.
- Machine Phase=Failed에서 적절한 진단 단계를 제안한다.

**코드 수정 없이 runbook YAML 추가만으로 가능한 범위이므로 가장 먼저 실행한다.**

---

### Phase 2: log-analyzer 자체 컨트롤러 로그 패턴 추가

구현 범위:
- `internal/loganalyzer/pattern.go`에 컨트롤러 로그 패턴 추가
  - `PatternReconcileError`, `PatternRateLimited`, `PatternRequeueLoop` 등
- `analyze_controller_logs` MCP tool 추가
- 자체 컨트롤러 설정(`cluster-api.yaml`)의 controller 목록 기반 로그 조회 경로 확보

완료 기준:
- "컨트롤러 로그 분석해줘"에 컨트롤러별 reconcile 에러 패턴과 영향받는 오브젝트 목록이 출력된다.

---

### Phase 3: cluster-api-server MCP 신설

구현 범위:
- `cmd/cluster-api-server/main.go` 추가
- `internal/clusterapiobserver/` 패키지 추가
  - `client.go`: dynamic client 기반 cluster-api 리소스 조회
  - `health.go`: ClusterHealthSummary 생성
  - `custom.go`: 자체 CRD 조회 (config 기반)
  - `kubeconfig.go`: workload cluster kubeconfig 추출
- `config/cluster-api.yaml`: 자체 CRD 목록, 컨트롤러 목록 정의
- `~/.k8s-assistant/mcp.yaml`에 cluster-api-server 추가

완료 기준:
- "전체 클러스터 상태 요약해줘"에 각 Cluster의 Phase, ControlPlane ready 여부, 문제 있는 Machine이 구조화돼서 나온다.
- "workload-cluster-1에 접속해서 Pod 상태 봐줘"에 kubeconfig 전환 후 kubectl 결과가 나온다.

---

### Phase 4: 자체 CRD 도메인 지식 심화

구현 범위:
- 자체 CRD 각각에 대한 runbook 추가 (컨트롤러 담당자와 협업)
- system prompt에 자체 CRD 간 의존 관계 추가
- `analyze_controller_logs`에 자체 컨트롤러별 특화 패턴 추가

완료 기준:
- 자체 CRD가 Degraded/Failed 상태일 때 관련 컨트롤러 로그와 runbook이 연결된다.
- 자체 컨트롤러 requeue 루프를 자동으로 감지해 trouble-shooting을 제안한다.

---

### Phase 5: 운영 이슈 RAG 활성화

구현 범위 (기존 `draft_troubleshooting_v1.md` Phase 5와 동일):
- cluster-api 장애 해결 이슈를 export/import/index
- 자체 컨트롤러 장애 이슈 축적
- `search_knowledge`에서 cluster-api/컨트롤러 관련 이슈 검색

완료 기준:
- 이전에 해결한 cluster-api 장애 사례가 RAG 검색에서 나온다.
- 자체 컨트롤러 관련 장애 이슈가 knowledge base에 쌓인다.

---

## 11. 미결 논점

### 1. 자체 CRD 스키마 공유 방식

자체 CRD가 다수이고 변경이 잦을 수 있다. 두 가지 방식이 있다:

- **A. config 파일 방식** (`cluster-api.yaml`): 운영팀이 직접 관리. 코드 재배포 없음.
- **B. 클러스터 자동 감지**: cluster-api-server 시작 시 `kubectl get crd`로 자체 CRD를 스캔해 자동 등록.

B 방식은 구현이 복잡하지만 CRD 추가/삭제 시 자동 반영된다. 단, 어떤 CRD가 "자체" CRD인지 판단 기준(`group` 도메인 필터링 등)이 필요하다.

### 2. 자체 컨트롤러 로그 접근 방식

현재 log-analyzer의 log fetcher는 파일 기반(Filebeat)을 전제한다. 자체 컨트롤러 로그는 주로 `kubectl logs` 경로를 사용한다.

선택지:
- **A. kubectl logs 위임**: k8s-assistant ReAct 루프가 `kubectl logs deployment/<controller>` 실행 후 analyze_controller_logs에 결과 전달
- **B. client-go 직접 조회**: cluster-api-server가 직접 Pod 로그를 조회해 패턴 분석까지 수행

A 방식이 단순하고 기존 구조와 정합성이 높다. B는 파이프라인 통합이 좋지만 중복 구현이 생긴다.

### 3. Workload Cluster 내부 진단 범위

workload cluster 내부 리소스(Pod, Service 등)까지 AIOps 범위에 포함할지 결정 필요.

- **포함**: management cluster의 cluster-api 상태 + workload cluster 내부 상태를 통합 진단
- **미포함**: cluster-api 리소스(infrastructure/machine/controlplane)만 다루고, workload 내부는 별도 k8s-assistant 인스턴스가 담당

workload cluster 수가 많으면 "포함" 방식의 kubeconfig 관리 복잡도가 높아진다.

### 4. OpenStack 에러 메시지 해석

Machine Phase=Failed의 FailureMessage에는 OpenStack API 에러가 그대로 포함될 수 있다. OpenStack 레이어를 범위에서 제외하더라도, 에러 메시지 해석은 필요하다.

예: `"failed to create server: 500 Internal Server Error: No valid host was found"` → "OpenStack 컴퓨트 노드 리소스 부족"으로 해석

이 해석은 system prompt에 대표적인 OpenStack 에러 메시지 패턴만 추가하는 것으로 충분할 수 있다.

### 5. cluster-api 업그레이드 지원

cluster-api 버전 업그레이드와 Kubernetes 버전 업그레이드는 복잡한 절차를 따른다. AIOps 범위에 포함할지 결정 필요.

- **포함**: 업그레이드 전 preflight check, 업그레이드 진행 모니터링, 롤백 시나리오 runbook 추가
- **미포함**: 현재 운영 상태 진단과 장애 대응만 다룸

초기에는 "미포함"으로 시작해 운영 경험이 쌓인 후 추가하는 것을 권장한다.

---

## 13. 설계 재검토: 최종 권장 방안

> 방안 D를 검토하는 과정에서 cluster-api MCP 서버의 구조적 문제가 드러났다. 이 섹션은 그 논의를 정리하고 수정된 최종 방안을 제시한다.

### 13.1 방안 D cluster-api-server의 역할 충돌

방안 D에서 제안한 `cluster-api-server` MCP 서버는 `client-go`로 K8s API를 직접 조회해 `ClusterHealthSummary` 같은 구조화된 데이터를 반환한다. 그런데 이것은 **kubectl(ReAct 루프)이 단독으로 담당해야 할 데이터 수집 책임**과 겹친다.

구체적인 문제:

| 문제 | 내용 |
|---|---|
| 이중 K8s 클라이언트 | kubectl(ReAct 루프)과 MCP 서버가 별도로 K8s API를 호출 |
| kubeconfig 불일치 | `/kube-context switch`로 컨텍스트를 바꿔도 MCP 서버의 client는 그대로 유지 |
| 인증 경로 분리 | 두 경로를 각각 디버깅해야 하는 운영 부담 |
| 기존 원칙 위반 | log-analyzer, trouble-shooting MCP가 K8s를 직접 건드리지 않는 이유와 같은 문제 |

**원칙: K8s 데이터 수집은 kubectl(ReAct 루프)이 단독으로 담당한다. MCP 서버는 분석·검색 전용이다.**

cluster-api MCP 서버가 정당성을 갖는 것은 "해석·요약" 역할뿐인데, LLM은 이미 kubectl JSON/YAML 파싱을 잘 하고 있어서 별도 서버 없이 System Prompt 어휘만 있어도 해석이 가능하다.

---

### 13.2 최종 권장 방안: Minimal System Prompt + RAG

cluster-api MCP 서버를 제거하고, LLM에 필요한 지식을 두 계층으로 나눈다.

```
계층 1: System Prompt (최소 어휘, 정적)
  └─ cluster-api CRD 이름 + API group      ← kubectl 명령을 올바르게 짜기 위한 최소 요건
  └─ Phase / Condition 핵심 어휘           ← 결과 해석을 위한 최소 요건
  └─ 리소스 소유관계 한 줄 요약
  └─ "cluster-api 관련 문제는 trouble-shooting을 먼저 호출하라"는 지침

계층 2: trouble-shooting RAG (동적 주입, 쿼리 시에만)
  └─ 장애 패턴별 진단 흐름 (few-shot 형태로 runbook에 포함)
  └─ 에러 메시지 → 원인 → 조치 매핑
  └─ 운영 경험 이슈 사례 (시간이 지날수록 축적)
```

**System Prompt vs RAG 분류 기준:**

| 구분 | System Prompt | RAG |
|---|---|---|
| 성격 | 항상 필요한 어휘·구조 지식 | 문제 발생 시에만 필요한 운영 지식 |
| 내용 | CRD 이름, API group, Phase 의미 | 진단 흐름, 에러→원인 매핑, 해결 사례 |
| 변경 주기 | cluster-api 버전 업그레이드 시 | 장애 패턴이 추가될 때마다 |
| 양 | 수십 줄 이하 | 제한 없음 (Qdrant에 저장) |

---

### 13.3 수정된 아키텍처

```
사용자 질의
  ↓
k8s-assistant ReAct 루프
  │
  ├─ kubectl/bash (유일한 K8s 데이터 수집 경로)
  │    └─ cluster-api CRD 조회, Machine 상태, 컨트롤러 로그 등 모두 여기서
  │
  ├─ log-analyzer MCP (변경 없음 또는 컨트롤러 패턴 소폭 추가)
  │    └─ 로그 수집은 kubectl logs 위임, 패턴 분석만 담당
  │
  └─ trouble-shooting MCP (runbook + RAG 확장)
       └─ cluster-api 장애 runbook (Section 8 예시 그대로 사용)
       └─ cluster-api 진단 흐름 few-shot을 runbook description에 포함
       └─ troubleshooting-upload로 Qdrant에 업로드

  * cluster-api-server MCP: 제거 (kubectl 역할 충돌)
```

**기존 방안 D 대비 변경점:**

| 항목 | 방안 D | 최종 방안 |
|---|---|---|
| cluster-api 데이터 수집 | cluster-api-server MCP | kubectl (ReAct 루프) |
| 상태 해석·요약 | cluster-api-server MCP | LLM (System Prompt 어휘 기반) |
| 진단 흐름 가이드 | cluster-api-server MCP | trouble-shooting RAG |
| 신규 MCP 서버 | 필요 (cluster-api-server) | 불필요 |
| 구현 비용 | 높음 | 낮음 |

---

### 13.4 System Prompt 최소 추가 내용

`prompts/default.tmpl` 또는 `ExtraPromptPaths`에 추가할 실제 내용:

```
## cluster-api 리소스

### API Group 및 리소스명
- cluster.x-k8s.io/v1beta1: Cluster, Machine, MachineDeployment, MachineSet, MachineHealthCheck
- controlplane.cluster.x-k8s.io/v1beta1: KubeadmControlPlane (조회 시 약칭 kcp 사용 가능)
- infrastructure.cluster.x-k8s.io/v1alpha7: OpenStackCluster, OpenStackMachine, OpenStackMachineTemplate

### 소유 관계
Cluster → KubeadmControlPlane (control plane)
Cluster → MachineDeployment → MachineSet → Machine (worker)
OpenStackCluster → Cluster, OpenStackMachine → Machine

### 진단 순서
cluster-api 문제 진단 시 다음 순서로 조회한다:
1. kubectl get cluster -A
2. kubectl get kcp -A
3. kubectl get machine -A
4. kubectl describe machine <name> -n <ns>  # Phase=Failed 또는 Provisioning 지속 시

### 핵심 Phase / Condition
- Machine Phase: Provisioning(VM 생성 중), Provisioned(VM 완료·미조인), Running(정상), Failed(실패)
- Cluster Condition: ControlPlaneReady, InfrastructureReady, MachinesReady, Ready
- KCP Condition: Available, EtcdClusterHealthy, ControlPlaneComponentsHealthy

### 주의 사항
- Machine Phase=Provisioning이 5분 이상 지속되면 OpenStack VM 생성 문제를 의심한다.
- Machine Phase=Failed이면 FailureReason, FailureMessage, Events를 함께 조회한다.
- cluster-api 관련 장애는 trouble-shooting 도구를 먼저 호출해 runbook을 확인한다.
```

---

### 13.5 RAG에 올릴 few-shot 형태 runbook 예시

Section 8의 runbook YAML에 `diagnostic_steps`를 순서대로 기술하면 그 자체가 진단 흐름 few-shot이 된다. 별도 포맷이 필요 없고, `troubleshooting-upload`로 그대로 Qdrant에 업로드한다.

RAG 검색 시 LLM은 "Machine이 Provisioning에서 멈춤"이라는 쿼리로 Section 8의 `capi-machine-stuck-provisioning` runbook을 받아 진단 단계를 그대로 따라간다.

---

### 13.6 개정된 구현 순서

기존 Phase 3(cluster-api-server 신설)을 제거하고, 다음 순서로 재조정한다.

| Phase | 내용 | 선행 조건 |
|---|---|---|
| **Phase 1** | System Prompt에 cluster-api 어휘 추가 (13.4 내용) | 없음, 즉시 시작 가능 |
| **Phase 2** | trouble-shooting runbook에 cluster-api 장애 시나리오 YAML 추가 후 Qdrant 업로드 | Qdrant + trouble-shooting-server 실행 중 |
| **Phase 3** | log-analyzer에 컨트롤러 로그 패턴 추가 (`analyze_controller_logs` tool) | log-analyzer-server 실행 중 |
| **Phase 4** | 자체 CRD 도메인 지식 심화 (runbook 추가, System Prompt 자체 CRD 목록 보완) | Phase 1·2 완료 후 운영 경험 축적 |
| **Phase 5** | 운영 이슈 RAG 활성화 (실제 장애 사례 축적) | Phase 2 완료 후 |

---

## 12. 관련 파일 및 참고

```
# 현재 프로젝트 (수정/추가 대상)
prompts/default.tmpl                              ← cluster-api 지침 추가
internal/diagnostic/types.go                      ← DetectionType 확장
internal/troubleshooting/runbooks/                ← cluster-api runbook 추가
internal/loganalyzer/pattern.go                   ← 컨트롤러 로그 패턴 추가

# 신규 추가
cmd/cluster-api-server/main.go
internal/clusterapiobserver/
config/cluster-api.yaml

# 참고 설계 문서
draft_troubleshooting_v1.md   ← ProblemSignal, 역할 분리, 구현 순서
draft_log_analyzer.md         ← observability toolset, 로그 수집 전략
revise_troubleshooting.md     ← 미결 논점, 호출 게이트 설계

# cluster-api 공식 문서
https://cluster-api.sigs.k8s.io/reference/glossary
https://cluster-api.sigs.k8s.io/developer/architecture/controllers/cluster
```
