# event-processor

## 0. 문서 목적과 범위

이 문서는 `cmd/event-processor`가 Kubernetes 이벤트를 받은 뒤 어떤 조건에서 어떤 lifecycle 작업을 실제로 수행하는지 정리한다.

- 대상 애플리케이션: `cmd/event-processor`
- 역할: 배포된 리소스의 lifecycle 실행
- 문서 범위:
  - `ClusterReconciler`
  - `MachineDeploymentReconciler`
  - `ServiceGatewayClaim` 생성 보조 로직
- 이 문서에서 중요한 점:
  - `event-processor`는 `event-watcher`처럼 상태 코드를 계산해 DB에 쓰는 컨트롤러가 아니다.
  - `event-processor`는 annotation, deletion state, 준비 상태를 보고 실제 조치 자체를 수행한다.
  - 따라서 이 문서는 `상태 조건 -> 실행 동작` 중심으로 읽어야 한다.

## 1. 빠른 검색 인덱스

| 검색 키워드 | 관련 컨트롤러 | 핵심 동작 |
| --- | --- | --- |
| suspend cluster | `ClusterReconciler` | control plane/data plane scale down, HelmRelease suspend, cluster pause |
| resume cluster | `ClusterReconciler` | HelmRelease resume, cluster unpause, control plane/data plane scale up |
| operation-state | `ClusterReconciler` | `suspend-requested`, `suspend-applied`, `resume-requested`, `resume-applied` |
| initial deployment failed cleanup | `ClusterReconciler` | paused cluster의 control plane 삭제 |
| cluster delete cleanup | `ClusterReconciler` | LB/PV 정리, cloud credential 정리, SGW claim 삭제, finalizer 제거 |
| remote webhook setup | `ClusterReconciler` | workload cluster의 webhook/service 보정 |
| nodegroup create failed cleanup | `MachineDeploymentReconciler` | 실패한 data plane 삭제 |
| service gateway claim | `MachineDeploymentReconciler` | MD 단위 `ServiceGatewayClaim` 생성 |

## 2. 역할

`event-processor`는 이벤트를 받아 다음 작업을 수행한다.

1. Cluster suspend/resume 요청을 실제 리소스 조작으로 전환한다.
2. initial deployment 실패 후 남은 control plane을 정리한다.
3. Cluster 삭제 시 event-watcher가 남긴 외부 리소스와 credential을 정리한다.
4. workload cluster 접근 후 remote webhook 관련 리소스를 유지한다.
5. MachineDeployment별 `ServiceGatewayClaim`을 보장한다.
6. node group 생성 실패 후 pause된 MachineDeployment의 data plane을 정리한다.

## 3. 주요 디렉토리

```text
./cmd/event-processor
├── appconfig: OpenStack, webhook, DB 설정
├── controllers: alias wrapper
├── internal
│   └── controller
│       ├── cluster_controller.go: cluster lifecycle 실행
│       ├── machinedeployment_controller.go: node group 후처리, SGW claim 생성
│       ├── servicegateway_claim.go: claim naming helper
│       └── controllerutil: remote webhook 보조 로직
└── pkg
    └── clusteroperation
        └── constants.go: suspend/resume operation annotation 정의
```

## 4. 주요 annotation과 상태 값

### 4.1 cluster operation annotation

| 용도 | key |
| --- | --- |
| 현재 operation state | `cluster.x-k8s.io.am/operation-state` |
| 마지막 suspend 완료 시각 | `cluster.x-k8s.io.ep/last-suspended-done` |
| 마지막 resume 완료 시각 | `cluster.x-k8s.io.ep/last-resumed-done` |

### 4.2 operation state 값

| 의미 | 실제 값 |
| --- | --- |
| suspend 요청됨 | `suspend-requested` |
| resume 요청됨 | `resume-requested` |
| suspend 실행 적용됨 | `suspend-applied` |
| resume 실행 적용됨 | `resume-applied` |

### 4.3 event-watcher와 공유하는 주요 annotation

| 용도 | key |
| --- | --- |
| Cluster ID | `cluster.x-k8s.io.ew/cluster-id` |
| Initial deployment pause | `cluster.x-k8s.io.ew/initial-deployment-paused` |
| NodeGroup ID | `cluster.x-k8s.io.ew/nodegroup-id` |
| NodeGroup create failed | `cluster.x-k8s.io.ew/nodegroup-create-failed` |

## 5. 공통 처리 제외 조건

| 대상 | 제외 조건 |
| --- | --- |
| `Cluster` | `cluster-id` annotation으로 DB cluster를 찾지 못하면 관리 대상이 아니라고 보고 skip |
| `MachineDeployment` | 소속 cluster를 찾지 못하면 skip |
| `MachineDeployment`의 SGW claim | MachineDeployment 또는 cluster가 삭제 중이면 생성하지 않음 |
| `MachineDeployment`의 SGW claim | `nodegroup-id` annotation으로 DB node group을 찾지 못하면 생성하지 않음 |

## 6. ClusterReconciler

- 소스: `cmd/event-processor/internal/controller/cluster_controller.go`
- 감시 리소스: `Cluster`
- 주요 부가 리소스:
  - `KamajiControlPlane`
  - `HelmRelease`
  - workload cluster의 `ValidatingWebhookConfiguration`
  - workload cluster의 `Service`
  - workload cluster의 `Endpoint`
  - OpenStack LoadBalancer, Volume/Share metadata, keypair, user
  - `ServiceGatewayClaim`

### 6.1 일반 reconcile 순서

| 조건 | 실행 동작 |
| --- | --- |
| Cluster가 존재하고 삭제 중이 아님 | event-processor finalizer 추가 |
| `initial-deployment-paused=true` | control plane 삭제 요청 실행 |
| `ControlPlaneInitialized=False` | 아직 remote setup을 하지 않고 5초 뒤 재시도 |
| suspend/resume 전환 annotation 존재 | 전환 처리 수행 |
| 전환 처리가 더 필요함 | 5초 뒤 재시도 |
| 전환이 없거나 완료됨 | workload cluster remote setup 수행 |

### 6.2 initial deployment 실패 후 cleanup

| 조건 | 실행 동작 |
| --- | --- |
| `cluster.x-k8s.io.ew/initial-deployment-paused=true` | `cluster-id` annotation을 확인한 뒤 control plane 삭제 요청 실행 |
| `cluster-id` annotation 없음 | control plane 삭제를 수행하지 못하고 에러 반환 |
| pause annotation이 없거나 `false` | cleanup 없이 다음 단계 진행 |

이 흐름은 initial deployment 실패 뒤 cluster가 pause된 경우에 남은 control plane을 정리하기 위한 것이다.

### 6.3 suspend/resume 상태 전이

| 현재 annotation 상태 | 판정 | 실행 동작 |
| --- | --- | --- |
| `operation-state` 없음, `last-suspended-done`만 있음 | 이미 suspend 완료 | 일반 reconcile을 중단 |
| `operation-state` 없음, `last-suspended-done`과 `last-resumed-done` 둘 다 있음 | 비정상 상태 | 에러 |
| `operation-state=suspend-requested`, suspend 완료 annotation 없음 | suspend 실행 시작 | 아래 suspend 실행 절차 수행 |
| `operation-state=suspend-requested`, suspend 완료 annotation 있음 | 비정상 상태 | 에러 |
| `operation-state=resume-requested`, suspend 완료 annotation 있음 | resume 실행 시작 | 아래 resume 실행 절차 수행 |
| `operation-state=resume-requested`, suspend 완료 annotation 없음 | 비정상 상태 | 에러 |
| `operation-state=suspend-applied`, 완료 annotation 둘 다 없음 | suspend 완료 대기 | replica 수가 목표치인지 검사 |
| `operation-state=resume-applied`, 완료 annotation 둘 다 없음 | resume 완료 대기 | replica 수가 목표치인지 검사 |
| `operation-state=suspend-applied` 또는 `resume-applied`인데 done annotation이 이미 있음 | 비정상 상태 | 에러 |

### 6.4 suspend 실행 절차

| 단계 | 실제 동작 |
| --- | --- |
| 1 | 모든 cluster 소속 `HelmRelease`의 values에서 `replicaCount=0`으로 변경 |
| 2 | `KamajiControlPlane.Spec.Replicas=0`으로 변경 |
| 3 | 기존 `last-suspended-done`, `last-resumed-done`, `operation-state` annotation 제거 |
| 4 | `operation-state=suspend-applied` 기록 |
| 5 | 후속 reconcile에서 실제 replica가 0인지 검사 |
| 6 | control plane ready replicas가 0이고 각 component deployment의 `replicas`, `readyReplicas`, `updatedReplicas`가 모두 0이며 `unavailableReplicas=0`이면 완료 |
| 7 | 완료 시 모든 `HelmRelease.Spec.Suspend=true`, cluster의 CAPI paused annotation 추가 |
| 8 | `operation-state` 제거, `last-suspended-done=<RFC3339 timestamp>` 기록 |

### 6.5 resume 실행 절차

| 단계 | 실제 동작 |
| --- | --- |
| 1 | 모든 cluster 소속 `HelmRelease.Spec.Suspend=false` |
| 2 | cluster의 CAPI paused annotation 제거 |
| 3 | 모든 cluster 소속 `HelmRelease`의 values에서 `replicaCount=1`로 변경 |
| 4 | `KamajiControlPlane.Spec.Replicas=3`으로 변경 |
| 5 | 기존 `last-suspended-done`, `last-resumed-done`, `operation-state` annotation 제거 |
| 6 | `operation-state=resume-applied` 기록 |
| 7 | 후속 reconcile에서 control plane ready replicas가 3인지, 각 component deployment가 `replicas=1`, `readyReplicas=1`, `updatedReplicas=1`, `unavailableReplicas=0`인지 검사 |
| 8 | 완료 시 `operation-state` 제거, `last-resumed-done=<RFC3339 timestamp>` 기록 |

### 6.6 suspend/resume 준비 완료 판정

| 전환 | 기대 control plane replicas | 기대 component deployment replicas |
| --- | --- | --- |
| suspend | `0` | `0` |
| resume | `3` | `1` |

준비가 덜 되었으면 현재 전환을 유지하고 재시도한다.

### 6.7 Cluster 삭제 흐름

| 순서 | 실행 동작 |
| --- | --- |
| 1 | `cluster-id` annotation 존재 여부 확인 |
| 2 | event-processor finalizer만 남았는지 확인 |
| 3 | 다른 finalizer가 남아 있으면 외부 리소스 삭제를 시작하지 않고 5초 뒤 재시도 |
| 4 | DB에서 identifier가 비어 있지 않은 `ClusterOpenStackResource` 조회 |
| 5 | resource type별 외부 리소스 정리 |
| 6 | cluster cloud credential 정리 |
| 7 | cluster label을 가진 `ServiceGatewayClaim` 일괄 삭제 |
| 8 | 모든 cleanup 뒤 event-processor finalizer 제거 |

### 6.8 ClusterResource 삭제 처리

#### Service resource

| 조건 | 실행 동작 |
| --- | --- |
| OpenStack LoadBalancer가 존재하고 provisioning status가 `PENDING_DELETE`가 아님 | listener, pool, health monitor, member를 포함해 LoadBalancer dependency까지 삭제 |
| LoadBalancer 삭제 요청이 `409 Conflict` | 이미 삭제 진행 중으로 보고 성공 취급 |
| OpenStack LoadBalancer 조회 결과 404 | load balancer 삭제 완료 동기화를 수행하고 DB `ClusterOpenStackResource` 삭제 |

#### PersistentVolume resource

| 조건 | 실행 동작 |
| --- | --- |
| resource extra를 파싱 가능 | provider가 Manila인지 Cinder인지 판별 |
| 전용 KINX metadata key가 존재 | OpenStack share 또는 volume metadata에서 해당 key 제거 |
| metadata 조회/삭제 실패 | 로그만 남기고 정리는 계속 진행 |
| cleanup 완료 | DB `ClusterOpenStackResource` 삭제 |

### 6.9 cloud credential 정리

| 조건 | 실행 동작 |
| --- | --- |
| `<cluster-name>-cloud-conf` Secret 없음 | cloud cleanup skip |
| Secret 존재 | `clouds.yaml`에서 `user_id`, `application_credential_id`, `application_credential_secret` 추출 |
| keypair 목록 조회 성공 | 해당 user의 keypair 모두 삭제 |
| keypair 삭제 404 | 이미 삭제된 것으로 보고 성공 취급 |
| user 삭제 404 | 이미 삭제된 것으로 보고 성공 취급 |
| credential 정리 완료 | cloud-conf Secret 삭제 |

### 6.10 ServiceGatewayClaim 정리

| 조건 | 실행 동작 |
| --- | --- |
| cluster label `iksv2.kinx.net/cluster-name=<cluster-name>`을 가진 claim 존재 | 모두 삭제 |
| claim이 이미 없음 | 그대로 종료 |

이 정리는 MachineDeployment owner reference 기반 GC가 기본 경로이고, owner reference 없이 남은 구버전 claim을 정리하는 안전망이다.

### 6.11 workload cluster remote setup

| 조건 | 실행 동작 |
| --- | --- |
| remote client 연결 실패가 `ErrClusterNotConnected` | 5초 뒤 재시도 |
| remote client 확보 | workload cluster 대상 watcher 등록 |
| workload cluster webhook 리소스 reconcile | validating/mutating webhook, headless service, CA bundle 등을 원하는 상태로 보정 |

## 7. MachineDeploymentReconciler

- 소스: `cmd/event-processor/internal/controller/machinedeployment_controller.go`
- 감시 리소스: `MachineDeployment`
- 주요 역할:
  - `ServiceGatewayClaim` 보장
  - node group 생성 실패 후 data plane cleanup

### 7.1 일반 reconcile 순서

| 조건 | 실행 동작 |
| --- | --- |
| MachineDeployment와 소속 Cluster가 존재 | service gateway claim reconcile 실행 |
| 이후 | node group create failed cleanup 실행 |

### 7.2 ServiceGatewayClaim 생성

| 조건 | 실행 동작 |
| --- | --- |
| MachineDeployment 또는 Cluster가 삭제 중 | claim 생성 skip |
| `nodegroup-id` annotation으로 DB node group을 찾지 못함 | claim 생성 skip |
| claim이 이미 존재 | no-op |
| claim이 없음 | 새 `ServiceGatewayClaim` 생성 |

생성되는 claim의 특징:

| 항목 | 값 |
| --- | --- |
| 이름 | `iks-sgwclaim-<subnetID 앞 8자>-<machineDeploymentName>` |
| namespace | cluster namespace |
| label | `iksv2.kinx.net/cluster-name=<cluster-name>` |
| customer 정보 | node group의 `ProjectID`, `NetworkID`, `SubnetID` |
| management 정보 | config의 service gateway project/network/VIP |
| consumer | `Cluster`, `<cluster-name>` |
| owner reference | MachineDeployment를 controller owner로 지정 |

동시 생성 race로 `AlreadyExists`가 발생해도 성공 취급한다.

### 7.3 node group 생성 실패 cleanup

| 조건 | 실행 동작 |
| --- | --- |
| `cluster.x-k8s.io.ew/nodegroup-create-failed=true`가 아니면 | cleanup 없음 |
| create failed이고 CAPI paused annotation이 `true`가 아니면 | cleanup 없음 |
| create failed이고 paused 상태이며 DB node group 조회 성공 | data plane 삭제 요청 실행 |
| DB node group이 이미 없음 | cleanup skip |

이 cleanup은 node group 생성 실패 뒤 pause된 MachineDeployment에 대해, 남은 data plane 리소스를 정리하는 경로다.

## 8. finalizer와 보정 책임

| 대상 | event-processor 역할 |
| --- | --- |
| `Cluster` | 자체 finalizer를 추가하고, cluster 삭제 cleanup을 모두 끝낸 뒤 제거 |
| `MachineDeployment` | 별도 cleanup controller 역할은 있으나 현재 본문 흐름에서 finalizer 기반 삭제 로직은 사용하지 않음 |
| `ServiceGatewayClaim` | owner reference를 MachineDeployment에 걸어 Kubernetes GC가 기본 삭제를 맡도록 함 |

## 9. event-watcher와의 역할 구분

| 구분 | event-watcher | event-processor |
| --- | --- | --- |
| 핵심 책임 | 관찰, 상태 계산, DB 갱신, 완료/실패 동기화 | 실제 lifecycle 조치 실행 |
| cluster suspend | `CP-ST-SUS` 등 상태 반영 | replica 축소, HelmRelease suspend, pause annotation 반영 |
| nodegroup 실패 | `DP-ST-CRF`, `DP-ST-SF`, `DP-ST-UF` 등 상태 반영 | 생성 실패 후 남은 data plane 삭제 |
| resource delete | Service/PV 상태 관찰 | cluster 삭제 시 남은 외부 LB/PV metadata 직접 정리 |
| remote workload setup | soot controller로 상태 관찰 | remote webhook/service 구성을 원하는 상태로 보정 |

## 10. 운영상 중요한 메모

1. `event-processor`는 상태 코드보다 annotation 상태 기계를 더 중요하게 사용한다.
2. suspend/resume은 단일 patch가 아니라 여러 단계 전환이다.
   - 요청 annotation
   - replica 조정
   - 실제 replica readiness 확인
   - done annotation 기록
3. suspend 완료 상태에서는 일반 reconcile을 건너뛴다.
4. cluster 삭제 cleanup은 다른 finalizer가 모두 빠진 뒤에만 시작한다. 즉, event-watcher 등 다른 컨트롤러가 먼저 자기 책임을 끝내야 한다.
5. cloud credential cleanup은 OpenStack keypair, user, secret 삭제까지 포함한다.
6. cluster 삭제 중 외부 LB가 이미 사라진 경우에도 load balancer 삭제 완료 동기화와 DB 정리는 수행한다.
7. MachineDeployment의 `ServiceGatewayClaim` 생성은 idempotent 하다. claim이 이미 있거나 동시 생성으로 충돌해도 정상 경로로 본다.
