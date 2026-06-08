# IKS v2 상태 판정 지식 문서

## 0. 문서 범위

이 문서는 IKS v2의 Kubernetes 리소스 상태, controller 동작, DB 상태의 대응 관계를 정리한다.

- `kubectl`로 조회한 Kubernetes 리소스의 `status`, `conditions`, `annotations`, `labels`, `finalizers`
- `api-manager`, `event-watcher`, `event-processor` 로그
- DB의 `Cluster`, `NodeGroup`, `Node`, `InitialDeploymentStatus`, `InitialAddonDeploymentStatus`, `ClusterOpenStackResource`

이 문서는 다음 사실을 함께 설명한다.

1. 각 상태 코드의 실제 값
2. Kubernetes 리소스 상태와 DB 상태의 대응 관계
3. 상위 리소스와 자동 생성 하위 리소스의 관계
4. 각 lifecycle 단계에서 확인해야 하는 annotation, label, finalizer
5. 각 controller가 어떤 상태를 만들고 어떤 동작을 수행하는지

## 1. 문서 간 역할 분리

| 문서 | 컴포넌트 | 핵심 역할 |
| --- | --- | --- |
| `AM.md` | `api-manager` | API 요청을 받아 DB row와 선언 리소스를 만든다 |
| `EW.md` | `event-watcher` | 실제 리소스 상태를 읽고 DB status/internal_status를 갱신한다 |
| `EP.md` | `event-processor` | suspend/resume, cleanup, 외부 리소스 정리 같은 lifecycle 동작을 실행한다 |
| `iks.md` | 상태 판정 지식 문서 | Kubernetes 상태, controller 동작, DB 값의 대응 관계를 정리한다 |

## 1.1 검색 키워드 인덱스

| 검색 주제 | 관련 섹션 | 핵심 키워드 |
| --- | --- | --- |
| cluster provisioning | `8.2 Cluster provisioning 단계` | `CP-ST-PRV`, `CP-IST-INF-PRV`, `CP-IST-TCP-PRV` |
| initial deployment | `8.3 Initial deployment` | `ID-ST-PRV`, `ID-ST-SC`, `ID-ST-ERR` |
| nodegroup create | `9.1 NodeGroup 생성` | `MachineDeployment`, `MachineSet`, `DP-ST-SU` |
| nodegroup timeout | `9.2 NodeGroup timeout 실패` | `DP-ST-CRF`, `DP-ST-SF`, `DP-ST-UF` |
| node status | `10. Node 상태 판정` | `NO-ST-PRV`, `NO-ST-AV`, `NO-ST-DL` |
| suspend resume | `11. suspend / resume 판정` | `suspend-requested`, `suspend-applied`, `last-suspended-done` |
| load balancer | `12.1 Service(type=LoadBalancer)` | `CR-ST-CR`, `CR-ST-AV`, `CR-ST-DLT` |
| persistent volume | `12.2 PersistentVolume` | `CR-ST-CRT`, `CR-ST-AT`, `CR-ST-DLT` |
| cluster delete | `13.1 Cluster 삭제` | `CP-ST-DL`, `CP-ST-DLF`, `CP-ST-DLT` |

## 2. 시스템 관점의 핵심 원칙

### 2.1 API 요청과 실제 배포 완료는 다르다

`api-manager`가 리소스를 생성했다고 해서 서비스가 곧바로 사용 가능한 것은 아니다.

- `api-manager`
  - DB row 생성
  - 상위 Kubernetes 리소스 생성
  - 초기 status를 `*-ST-PRV`, `*-IST-ST` 또는 `*-IST-APD`로 둔다
- `event-watcher`
  - 실제 Kubernetes 상태를 읽는다
  - `Available`, `Deleting`, `Failed` 등 실제 상태를 DB에 반영한다
- `event-processor`
  - suspend/resume, cleanup 같은 후속 lifecycle 작업을 수행한다

운영 판정은 반드시 `Kubernetes 실제 상태 + DB 상태 + controller log`를 함께 봐야 한다.

### 2.2 상위 리소스와 자동 생성 하위 리소스

다음 리소스들은 `api-manager`가 모두 직접 만들지 않는다. 상위 리소스가 생기면 각 관리 controller가 하위 리소스를 만든다.

| 상위 리소스 | 자동 생성 하위 리소스 | 생성 주체 |
| --- | --- | --- |
| `KamajiControlPlane` | `TenantControlPlane` | Kamaji controller |
| `MachineDeployment` | `MachineSet` | Cluster API controller |
| `MachineSet` | `Machine` | Cluster API controller |
| `Machine` | `OpenStackMachine` | CAPO controller |
| `HelmChartProxy` | `HelmReleaseProxy` | CAAH/Helm addon controller |

따라서 하위 리소스가 아직 없다고 해서 곧바로 `api-manager` 실패로 보면 안 된다.

예:

- `KamajiControlPlane`은 있는데 `TenantControlPlane`이 아직 없다
  - Kamaji controller 생성 대기 단계일 수 있다
- `MachineDeployment`는 있는데 `Machine`이 없다
  - `MachineSet` 생성 또는 replica 전개 대기 단계일 수 있다
- `HelmChartProxy`는 있는데 `HelmReleaseProxy`가 없다
  - addon controller 동기화 대기 단계일 수 있다

## 3. 리소스 계층도

```text
Cluster
├── OpenStackCluster
├── KamajiControlPlane
│   └── TenantControlPlane
├── HelmRelease
│   ├── cloud-controller-manager
│   └── cluster-autoscaler
├── HelmChartProxy
│   └── HelmReleaseProxy
│       ├── calico
│       ├── cinder-csi
│       ├── nfs-csi
│       └── manila-csi
└── MachineDeployment
    ├── MachineSet
    │   └── Machine
    │       └── OpenStackMachine
    ├── KubeadmConfigTemplate
    ├── OpenStackMachineTemplate
    └── MachineHealthCheck
```

workload cluster 내부 관찰 대상:

```text
Service(type=LoadBalancer)
└── OpenStack LoadBalancer

PersistentVolume(Cinder/Manila CSI)
├── PersistentVolumeClaim
└── VolumeAttachment
```

## 4. 조회에 필요한 기본 정보

### 4.1 핵심 식별자

| 항목 | 이유 |
| --- | --- |
| tenant namespace | 대부분의 management cluster 리소스가 여기에 존재 |
| cluster name | 모든 label selector의 중심 |
| DB cluster ID | `cluster.x-k8s.io.ew/cluster-id` annotation과 매칭 |
| 원하는 작업 | 생성, 초기 배포, 스케일, 업그레이드, suspend, resume, 삭제 중 무엇인지 |

### 4.2 kubectl 조회 예시

```bash
NS=<tenant-id>
CLUSTER=<cluster-name>
MD=<machine-deployment-name>
```

### 4.3 management cluster 리소스 조회

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
kubectl -n "$NS" get helmrelease -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
kubectl -n "$NS" get helmchartproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
kubectl -n "$NS" get helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o wide
```

### 4.4 annotation, label, finalizer 조회

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'

kubectl -n "$NS" get machinedeployment "$MD" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'
```

### 4.5 condition 조회

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} message={.message}{"\n"}{end}'
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} reason={.reason}{"\n"}{end}{end}'
```

### 4.6 controller 로그 식별자

컴포넌트별로 아래 controller 이름 또는 메시지를 먼저 찾는다.

| 컴포넌트 | 대표 controller/log 검색어 |
| --- | --- |
| event-watcher cluster | `EventWatcherClusterController`, `Starting Reconcile for Cluster` |
| event-watcher initial deployment | `InitialDeploymentController` |
| event-watcher machine deployment | `EventWatcherMachineDeploymentController` |
| event-watcher machine | `EventWatcherMachineController` |
| event-watcher soot service | `EventWatcherSootServiceController` |
| event-watcher soot pv | `EventWatcherSootPersistentVolumeController` |
| event-processor cluster | `eventprocessor-cluster`, `Reconciling Cluster` |
| event-processor machine deployment | `eventprocessor-md`, `Starting Reconcile for machineDeployment` |

로그 조회 예시:

```bash
kubectl -n <controller-namespace> logs deploy/<event-watcher-deployment> --since=30m | grep "$CLUSTER"
kubectl -n <controller-namespace> logs deploy/<event-processor-deployment> --since=30m | grep "$CLUSTER"
```

## 5. DB에서 확인할 모델과 핵심 필드

정확한 물리 테이블명은 DB naming strategy에 따라 달라질 수 있으므로, 이 문서는 domain model 이름 기준으로 설명한다.

### 5.1 `Cluster`

| 필드 | 의미 |
| --- | --- |
| `id` | cluster 식별자, `cluster-id` annotation과 매칭 |
| `name`, `project_id` | 조회 키 |
| `current_count`, `total_count` | control plane replica 관련 값 |
| `suspended` | suspend 여부 |
| `kubernetes_version` | cluster version |
| `status` | control plane 외부 상태 |
| `internal_status` | control plane 세부 단계 |
| `deleted_at` | soft delete 여부 |

### 5.2 `NodeGroup`

| 필드 | 의미 |
| --- | --- |
| `id` | node group 식별자, `nodegroup-id` annotation과 매칭 |
| `cluster_id` | 소속 cluster |
| `name`, `project_id` | 조회 키 |
| `network_id`, `subnet_id` | SGW claim 생성 기준 |
| `kubernetes_version` | node group version |
| `current_count`, `total_count` | replica 상태 |
| `label`, `annotation` | 생성 시 요청 metadata |
| `status` | data plane 외부 상태 |
| `internal_status` | data plane 세부 단계 |
| `deleted_at` | soft delete 여부 |

### 5.3 `Node`

| 필드 | 의미 |
| --- | --- |
| `id` | node 식별자, `node-id` annotation과 매칭 |
| `node_group_id` | 소속 node group |
| `instance_id` | OpenStack instance ID |
| `name`, `project_id` | 조회 키 |
| `status` | node 외부 상태 |
| `internal_status` | node 세부 단계 |
| `deleted_at` | soft delete 여부 |

### 5.4 `InitialDeploymentStatus`

| 필드 | 의미 |
| --- | --- |
| `cluster_id` | 대상 cluster |
| `status` | 전체 initial deployment 상태 |
| `bastion_status` | bastion 상태 |
| `control_plane_status` | control plane 상태 |
| `data_plane_status` | initial node group 상태 |
| `*_err_reason` | 실패 또는 미완료 원인 |
| `max_wait_limit` | timeout 기준 |
| `alert_yn` | alert 발송 여부 |

### 5.5 `InitialAddonDeploymentStatus`

| 필드 | 의미 |
| --- | --- |
| `initial_deployment_status_id` | 상위 initial deployment |
| `addon_code` | addon 종류 |
| `status` | addon 상태 |
| `err_reason` | addon 미완료 또는 실패 원인 |

### 5.6 `ClusterOpenStackResource`

| 필드 | 의미 |
| --- | --- |
| `id` | resource 식별자, `resource-id` annotation과 매칭 |
| `cluster_id` | 소속 cluster |
| `resource_type` | `Service`, `PersistentVolume` 등 |
| `resource_name`, `name`, `namespace` | Kubernetes 자원과 매칭 |
| `identifier` | OpenStack LB ID 또는 volume/share ID |
| `status` | `CR-ST-*` |
| `extra` | external IP, attached VM ID, storage, provider 등 |
| `deleted_at` | soft delete 여부 |

## 6. 공통 metadata 사전

### 6.1 cluster metadata

| 용도 | key |
| --- | --- |
| cluster name label | `cluster.x-k8s.io/cluster-name` |
| cluster ID | `cluster.x-k8s.io.ew/cluster-id` |
| cluster security group ID | `cluster.x-k8s.io.ew/cluster-sg-id` |
| cluster paused | `cluster.x-k8s.io.ew/paused` |
| CAPI paused | `cluster.x-k8s.io/paused` |
| initial deployment label | `cluster.x-k8s.io.ew/initial-deployment` |
| initial deployment paused | `cluster.x-k8s.io.ew/initial-deployment-paused` |
| initial deployment completed | `cluster.x-k8s.io.ew/initial-deployment-completed` |

### 6.2 node group / node metadata

| 용도 | key |
| --- | --- |
| node group ID | `cluster.x-k8s.io.ew/nodegroup-id` |
| node group paused | `cluster.x-k8s.io.ew/nodegroup-paused` |
| node ID | `cluster.x-k8s.io.ew/node-id` |
| node group upgrading | `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress` |

### 6.3 cluster operation metadata

| 용도 | key |
| --- | --- |
| 현재 suspend/resume state | `cluster.x-k8s.io.am/operation-state` |
| suspend 완료 시각 | `cluster.x-k8s.io.ep/last-suspended-done` |
| resume 완료 시각 | `cluster.x-k8s.io.ep/last-resumed-done` |

### 6.4 sync annotation

| 이벤트 | key |
| --- | --- |
| cluster create started | `cluster.x-k8s.io.ew/cluster-create-started` |
| cluster create completed | `cluster.x-k8s.io.ew/cluster-create-completed` |
| cluster create failed | `cluster.x-k8s.io.ew/cluster-create-failed` |
| cluster delete started | `cluster.x-k8s.io.ew/cluster-delete-started` |
| cluster delete completed | `cluster.x-k8s.io.ew/cluster-delete-completed` |
| cluster delete failed | `cluster.x-k8s.io.ew/cluster-delete-failed` |
| nodegroup create completed | `cluster.x-k8s.io.ew/nodegroup-create-completed` |
| nodegroup create failed | `cluster.x-k8s.io.ew/nodegroup-create-failed` |
| nodegroup scale completed | `cluster.x-k8s.io.ew/nodegroup-scale-completed` |
| nodegroup scale failed | `cluster.x-k8s.io.ew/nodegroup-scale-failed` |
| nodegroup upgrade completed | `cluster.x-k8s.io.ew/nodegroup-upgrade-completed` |
| nodegroup upgrade failed | `cluster.x-k8s.io.ew/nodegroup-upgrade-failed` |
| nodegroup delete completed | `cluster.x-k8s.io.ew/nodegroup-delete-completed` |
| nodegroup delete failed | `cluster.x-k8s.io.ew/nodegroup-delete-failed` |
| node create completed | `cluster.x-k8s.io.ew/node-create-completed` |
| node delete completed | `cluster.x-k8s.io.ew/node-delete-completed` |
| resource create completed | `cluster.x-k8s.io.ew/resource-create-completed` |
| resource update completed | `cluster.x-k8s.io.ew/resource-update-completed` |
| resource attach completed | `cluster.x-k8s.io.ew/resource-attach-completed` |
| resource detach completed | `cluster.x-k8s.io.ew/resource-detach-completed` |
| resource delete completed | `cluster.x-k8s.io.ew/resource-delete-completed` |

각 sync annotation에는 대응하는 `-timestamp` annotation이 함께 존재할 수 있다.

## 7. 상태 코드 사전

### 7.1 Cluster

| status | 의미 |
| --- | --- |
| `CP-ST-PRV` | provisioning |
| `CP-ST-CF` | configuring |
| `CP-ST-AV` | available |
| `CP-ST-UAV` | unavailable |
| `CP-ST-SUS` | suspended |
| `CP-ST-DL` | deleting |
| `CP-ST-DLT` | deleted |
| `CP-ST-DLF` | deletion failed |
| `CP-ST-F` | failed |
| `CP-ST-UN` | unknown |

| internal_status | 의미 |
| --- | --- |
| `CP-IST-ST` | api-manager가 생성 시작 |
| `CP-IST-APD` | 선언 리소스 적용 완료 |
| `CP-IST-INF-PRV` | infra provisioning |
| `CP-IST-TCP-PRV` | tenant control plane provisioning |
| `CP-IST-TCP-UG` | tenant control plane upgrading |
| `CP-IST-TCP-DL` | tenant control plane deleting |
| `CP-IST-INF-DL` | infra deleting |
| `CP-IST-NONE` | 세부 상태 없음 |
| `CP-IST-UN` | unknown |

### 7.2 NodeGroup

| status | 의미 |
| --- | --- |
| `DP-ST-PRV` | provisioning |
| `DP-ST-CF` | configuring |
| `DP-ST-AV` | available |
| `DP-ST-SU` | scaling up |
| `DP-ST-SD` | scaling down |
| `DP-ST-DL` | deleting |
| `DP-ST-DLT` | deleted |
| `DP-ST-DLF` | deletion failed |
| `DP-ST-CRF` | create failed |
| `DP-ST-SF` | scale failed |
| `DP-ST-UF` | upgrade failed |
| `DP-ST-F` | failed |
| `DP-ST-UN` | unknown |

| internal_status | 의미 |
| --- | --- |
| `DP-IST-ST` | api-manager가 생성 시작 |
| `DP-IST-APD` | 선언 리소스 적용 완료 |
| `DP-IST-DP-PRV` | data plane provisioning |
| `DP-IST-DP-UAV` | data plane unavailable |
| `DP-IST-DP-DL` | data plane deleting |
| `DP-IST-DP-UG` | data plane upgrading |
| `DP-IST-NONE` | 세부 상태 없음 |
| `DP-IST-UGF` | upgrade failed |
| `DP-IST-UN` | unknown |

### 7.3 Node

| status | 의미 |
| --- | --- |
| `NO-ST-PRV` | provisioning |
| `NO-ST-AV` | available |
| `NO-ST-UAV` | unavailable |
| `NO-ST-DL` | deleting |
| `NO-ST-DLT` | deleted |
| `NO-ST-UN` | unknown |

| internal_status | 의미 |
| --- | --- |
| `NO-IST-VM-PRV` | VM provisioning |
| `NO-IST-NO-PRV` | node provisioning |
| `NO-IST-NO-UAV` | node unhealthy |
| `NO-IST-NO-DR` | node draining |
| `NO-IST-VO-DT` | volume detaching |
| `NO-IST-VM-DL` | VM deleting |
| `NO-IST-NO-DL` | node deleting |
| `NO-IST-NONE` | 세부 상태 없음 |
| `NO-IST-UN` | unknown |

### 7.4 InitialDeployment

| status | 의미 |
| --- | --- |
| `ID-ST-PRV` | provisioning |
| `ID-ST-SC` | success |
| `ID-ST-ERR` | error |
| `ID-ST-CC` | cancelled |
| `ID-ST-NO` | no init |

### 7.5 ClusterResource

| status | 의미 |
| --- | --- |
| `CR-ST-UN` | unknown |
| `CR-ST-CRT` | created |
| `CR-ST-AT` | attached |
| `CR-ST-DT` | detached |
| `CR-ST-CR` | creating |
| `CR-ST-UD` | updating |
| `CR-ST-AV` | available |
| `CR-ST-DL` | deleting |
| `CR-ST-DLT` | deleted |

## 8. 생성 상태 판정

### 8.1 Cluster 생성 직후

#### 기대되는 초기 흔적

| 위치 | 기대 값 |
| --- | --- |
| DB `Cluster.status` | `CP-ST-PRV` |
| DB `Cluster.internal_status` | 처음 `CP-IST-ST`, 이후 `CP-IST-APD` |
| Kubernetes | `OpenStackCluster`, `KamajiControlPlane`, `Cluster` 존재 |
| Kubernetes | addon용 `HelmRelease`, `HelmChartProxy` 존재 |
| Kubernetes | 최초 `MachineDeployment` 존재 |

#### 판정 방법

| 관찰 | 판단 |
| --- | --- |
| DB는 `CP-IST-ST`, Kubernetes 상위 리소스가 일부 없음 | `api-manager`가 생성 초반에서 실패했을 가능성 |
| DB는 `CP-IST-APD`, `OpenStackCluster`만 있고 `Cluster`가 없음 | 선언 리소스 일부 적용 후 실패 가능성 |
| `KamajiControlPlane`은 있는데 `TenantControlPlane`이 없음 | Kamaji controller가 아직 하위 리소스를 만들지 않았거나 실패 |
| `HelmChartProxy`는 있는데 `HelmReleaseProxy`가 없음 | addon controller 동기화 전 단계 |

### 8.2 Cluster provisioning 단계

| Kubernetes 조건 | DB 기대 상태 | 의미 |
| --- | --- | --- |
| `Cluster.status.infrastructureReady=false` | `CP-ST-PRV`, `CP-IST-INF-PRV` | infra 생성 중 |
| infra ready, control plane not ready, TenantControlPlane version이 비어 있거나 provisioning | `CP-ST-PRV`, `CP-IST-TCP-PRV` | tenant control plane 생성 중 |
| infra ready, control plane not ready, provisioning이 아님 | `CP-ST-UAV`, `CP-IST-NONE` | 제어 plane 이상 |
| infra ready, control plane ready | `CP-ST-AV`, `CP-IST-NONE` | cluster 자체는 available |

#### 확인 명령

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
```

#### 로그에서 볼 것

| 검색어 | 의미 |
| --- | --- |
| `Starting Reconcile for Cluster` | event-watcher cluster reconcile 진입 |
| `status CP-ST-PRV` 관련 로그 | watcher가 provisioning으로 판정 |
| `TenantControlPlane` 조회 실패 | 하위 리소스 생성 지연 또는 누락 |

### 8.3 Initial deployment

#### 판단 기준

| 대상 | 성공 조건 |
| --- | --- |
| bastion | cluster 이름의 `Ingress` 존재 또는 init 불필요 |
| control plane | `ControlPlaneReady=True` |
| data plane | initial label이 붙은 `MachineDeployment` 1개가 available |
| addon | `HelmReleaseProxy`와 `HelmRelease` Ready |

#### 전체 DB 상태

| 조건 | DB `InitialDeploymentStatus.status` |
| --- | --- |
| 모든 addon 성공, bastion 성공 또는 no-init, control plane 성공, data plane 성공 | `ID-ST-SC` |
| 허용 시간 초과 | `ID-ST-ERR` |
| 삭제 중 취소 | `ID-ST-CC` |
| 그 외 | `ID-ST-PRV` |

#### Kubernetes에서 볼 metadata

| key | 의미 |
| --- | --- |
| `cluster.x-k8s.io.ew/initial-deployment=true` | 최초 node group |
| `cluster.x-k8s.io.ew/initial-deployment-completed=true` | initial deployment 성공 |
| `cluster.x-k8s.io.ew/initial-deployment-completed=false` | initial deployment 실패 |
| `cluster.x-k8s.io.ew/initial-deployment-paused=true` | 실패 후 pause |

#### 실패 판정

| 관찰 | 판단 |
| --- | --- |
| DB 전체 `ID-ST-ERR` | initial deployment 최종 실패 |
| cluster annotation `initial-deployment-completed=false` | watcher가 실패 동기화 완료 |
| cluster annotation `initial-deployment-paused=true` | event-processor가 control plane cleanup 대상으로 본다 |
| HelmRelease `spec.suspend=true` | 실패 후 addon 정지 |
| Cluster `cluster.x-k8s.io/paused=true` | 실패 후 cluster pause |

## 9. NodeGroup 상태 판정

### 9.1 NodeGroup 생성

#### 계층 확인 순서

```text
MachineDeployment
└── MachineSet
    └── Machine
        └── OpenStackMachine
```

#### Kubernetes와 DB 매핑

| Kubernetes 조건 | DB 기대 상태 |
| --- | --- |
| `MachineDeployment.phase=ScalingUp`, desired != current | `DP-ST-SU`, `DP-IST-DP-PRV` |
| `MachineDeployment.phase=ScalingUp`, desired == current | `DP-ST-SU`, `DP-IST-DP-UAV` |
| `MachineDeployment.phase=Running`, desired == current | `DP-ST-AV`, `DP-IST-NONE` |

#### 하위 리소스가 없을 때 판단

| 관찰 | 판단 |
| --- | --- |
| `MachineDeployment` 없음 | `api-manager` 생성 또는 삭제 단계 문제 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller가 아직 전개하지 못한 상태 |
| `MachineSet` 있음, `Machine` 없음 | replica 전개 또는 template 문제 |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO controller 생성 대기 또는 실패 |

### 9.2 NodeGroup timeout 실패

| annotation 기준 | 실패 status |
| --- | --- |
| last-created timeout | `DP-ST-CRF` |
| last-scaled timeout | `DP-ST-SF` |
| last-upgraded timeout | `DP-ST-UF` |

#### 함께 확인할 것

| 관찰 | 의미 |
| --- | --- |
| DB status가 `DP-ST-CRF`, `DP-ST-SF`, `DP-ST-UF` | watcher가 timeout을 확정 |
| `nodegroup-create-failed`, `nodegroup-scale-failed`, `nodegroup-upgrade-failed` annotation | 실패 동기화 완료 |
| `cluster.x-k8s.io.ew/nodegroup-paused=true` 또는 CAPI paused | watcher가 실패 후 pause |
| event-processor log의 data plane 삭제 | 생성 실패 cleanup 실행 여부 |

### 9.3 NodeGroup scale

| Kubernetes 조건 | DB 상태 |
| --- | --- |
| desired replicas 증가, 실제 replica가 따라가는 중 | `DP-ST-SU`, `DP-IST-DP-PRV` |
| desired replicas 감소 | `DP-ST-SD`, `DP-IST-DP-DL` |
| Running, desired == current | `DP-ST-AV`, `DP-IST-NONE` |

### 9.4 NodeGroup upgrade

| 관찰 | DB 상태 |
| --- | --- |
| 현재 MD version과 다른 old MachineSet이 replica를 가짐 | `DP-ST-CF`, `DP-IST-DP-UG` |
| old MachineSet 제거, updated/ready/available 모두 desired와 같음 | `DP-ST-AV`, `DP-IST-NONE` |
| timeout | `DP-ST-UF` |

#### 확인 명령

```bash
kubectl -n "$NS" get machinedeployment "$MD" -o yaml
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/cluster-name="$CLUSTER",cluster.x-k8s.io/deployment-name="$MD" -o wide
```

## 10. Node 상태 판정

### 10.1 provisioning

| Kubernetes 조건 | DB 상태 |
| --- | --- |
| machine not running, `InfrastructureReady=false` | `NO-ST-PRV`, `NO-IST-VM-PRV` |
| machine not running, `InfrastructureReady=true` | `NO-ST-PRV`, `NO-IST-NO-PRV` |

### 10.2 available / unavailable

| Kubernetes 조건 | DB 상태 |
| --- | --- |
| `Machine.phase=Running`, `MachineNodeHealthy=True` | `NO-ST-AV`, `NO-IST-NONE` |
| `Machine.phase=Running`, `MachineNodeHealthy=False` | `NO-ST-UAV`, `NO-IST-NO-UAV` |
| `Machine.phase=Running`, healthy 원인이 명확하지 않음 | `NO-ST-UAV`, `NO-IST-UN` |

### 10.3 deleting

| Kubernetes 조건 | DB 상태 |
| --- | --- |
| cluster 자체가 삭제 중 | `NO-ST-DL`, `NO-IST-VM-DL` |
| drain 진행 중 | `NO-ST-DL`, `NO-IST-NO-DR` |
| volume detach 진행 중 | `NO-ST-DL`, `NO-IST-VO-DT` |
| VM 삭제 중 | `NO-ST-DL`, `NO-IST-VM-DL` |
| node 삭제 단계 | `NO-ST-DL`, `NO-IST-NO-DL` |
| `MachineFinalizer` 제거됨 | `NO-ST-DLT`, `NO-IST-NONE` |

## 11. suspend / resume 판정

### 11.1 suspend 시작

| 관찰 | 의미 |
| --- | --- |
| cluster annotation `cluster.x-k8s.io.am/operation-state=suspend-requested` | suspend 요청 접수 |
| event-processor가 이를 처리하면 `suspend-applied`로 변경 | 실제 축소 작업 시작 |

### 11.2 suspend 완료

| 관찰 | 의미 |
| --- | --- |
| `KamajiControlPlane.spec.replicas=0` |
| 각 component `HelmRelease` values의 `replicaCount=0` |
| 실제 Deployment replicas도 모두 0 |
| `HelmRelease.spec.suspend=true` |
| cluster CAPI paused annotation 존재 |
| `last-suspended-done` 존재 |
| DB `Cluster.status=CP-ST-SUS`, `internal_status=CP-IST-NONE` |

### 11.3 resume 시작과 완료

| 관찰 | 의미 |
| --- | --- |
| `operation-state=resume-requested` | resume 요청 접수 |
| `operation-state=resume-applied` | 실제 복구 작업 진행 중 |
| `KamajiControlPlane.spec.replicas=3` |
| component `replicaCount=1` |
| actual deployment replicas가 모두 1 |
| `HelmRelease.spec.suspend=false` |
| cluster CAPI paused annotation 제거 |
| `last-resumed-done` 존재 |

### 11.4 suspend/resume이 멈췄을 때

| 관찰 | 의심 지점 |
| --- | --- |
| `suspend-requested`에서 안 바뀜 | event-processor reconcile 미수행 또는 에러 |
| `suspend-applied`에서 안 끝남 | KCP 또는 component deployment replica가 목표치에 도달하지 않음 |
| `resume-requested`에서 안 바뀜 | event-processor reconcile 미수행 또는 비정상 annotation 조합 |
| `resume-applied`에서 안 끝남 | component readiness 미충족 |
| `last-suspended-done`과 `last-resumed-done`이 둘 다 있음 | 코드상 비정상 상태 |

## 12. workload cluster 리소스 판정

### 12.1 Service(type=LoadBalancer)

#### 확인 대상

```bash
kubectl --context <workload-context> -n <namespace> get svc <service-name> -o yaml
```

#### metadata

| key | 의미 |
| --- | --- |
| `tenant.meta.x-k8s.io/id` | root cluster namespace |
| `cluster.meta.x-k8s.io/name` | root cluster name |
| `cluster.x-k8s.io.ew/resource-id` | DB `ClusterOpenStackResource.id` |
| `service.beta.kubernetes.io/kinx-load-balancer-id` | OpenStack LB ID |

#### 상태 판정

| Kubernetes/OpenStack 관찰 | DB 상태 |
| --- | --- |
| LB ID annotation 없음 | `CR-ST-CR` |
| OpenStack LB `PENDING_CREATE` | `CR-ST-CR` |
| OpenStack LB `PENDING_UPDATE` | `CR-ST-UD` |
| OpenStack LB `ACTIVE` | `CR-ST-AV` |
| OpenStack LB `ERROR` | `CR-ST-UN` |
| deletion timestamp 존재 | `CR-ST-DL` |
| LB cleanup finalizer 제거 | `CR-ST-DLT` |

#### 완료 판정

| 관찰 | 의미 |
| --- | --- |
| DB `status=CR-ST-AV` |
| `identifier=<lb-id>` |
| `resource_name=<openstack lb name>` |
| `extra.external_ip_address` 존재 가능 |
| `resource-create-completed` 또는 `resource-update-completed` annotation |

### 12.2 PersistentVolume

#### 확인 대상

```bash
kubectl --context <workload-context> get pv <pv-name> -o yaml
kubectl --context <workload-context> get volumeattachment
kubectl --context <workload-context> -n <namespace> get pvc <pvc-name> -o yaml
```

#### 대상 PV 조건

| 조건 |
| --- |
| `spec.csi.driver=cinder.csi.openstack.org` 또는 `manila.csi.openstack.org` |

#### 상태 판정

| 관찰 | DB 상태 |
| --- | --- |
| PV `Available` 또는 `Bound`, CSI volume handle 있음 | `CR-ST-CRT` |
| 위 조건 + attached `VolumeAttachment` 있음 | `CR-ST-AT` |
| PV `Released` | `CR-ST-DT` |
| deletion timestamp 존재 | `CR-ST-DL` |
| `pv-protection` finalizer 제거 | `CR-ST-DLT` |

## 13. 삭제 상태 판정

### 13.1 Cluster 삭제

#### watcher 기준

| 관찰 | DB 상태 |
| --- | --- |
| `deletionTimestamp` 존재 | `CP-ST-DL` |
| `ControlPlaneReady=False`, deleted reason | `CP-IST-INF-DL` |
| 그 외 삭제 중 | `CP-IST-TCP-DL` |
| 삭제 timeout | `CP-ST-DLF` |
| `HelmRelease`, `HelmChartProxy`, 같은 이름 `Ingress`가 모두 없고 CAPI finalizer도 없음 | `CP-ST-DLT` |

#### processor 기준

| 조건 | 동작 |
| --- | --- |
| event-processor finalizer만 남음 | 외부 resource cleanup 시작 가능 |
| 다른 finalizer 남음 | 5초 재시도, cleanup 시작 안 함 |
| DB에 외부 resource identifier 남음 | LB/PV 정리 |
| cloud-conf secret 존재 | keypair, user, secret 정리 |
| cluster label을 가진 `ServiceGatewayClaim` 존재 | 일괄 삭제 |

#### 삭제가 멈춘 위치 찾기

| 관찰 | 의심 지점 |
| --- | --- |
| `HelmRelease` 남음 | watcher는 `CP-ST-DL` 유지 |
| `HelmChartProxy` 남음 | watcher는 `CP-ST-DL` 유지 |
| 같은 이름 `Ingress` 남음 | watcher는 `CP-ST-DL` 유지 |
| 다른 finalizer 남음 | processor는 외부 cleanup 시작 안 함 |
| DB `ClusterOpenStackResource.identifier` 남음 | processor가 외부 resource cleanup 중 |
| cloud-conf secret 남음 | processor가 credential cleanup 전 또는 실패 |

### 13.2 NodeGroup 삭제

| 관찰 | DB 상태 |
| --- | --- |
| `MachineDeployment.deletionTimestamp` 존재 | `DP-ST-DL`, `DP-IST-DP-DL` |
| 삭제 timeout | `DP-ST-DLF`, `DP-IST-NONE` |
| `MachineDeploymentFinalizer` 제거 | `DP-ST-DLT`, `DP-IST-NONE` |

### 13.3 Node 삭제

| 관찰 | DB 상태 |
| --- | --- |
| drain 중 | `NO-ST-DL`, `NO-IST-NO-DR` |
| volume detach 중 | `NO-ST-DL`, `NO-IST-VO-DT` |
| VM 삭제 중 | `NO-ST-DL`, `NO-IST-VM-DL` |
| machine finalizer 제거 | `NO-ST-DLT`, `NO-IST-NONE` |

## 14. 컴포넌트별 로그 의미

### 14.1 api-manager

| 로그 주제 | 해석 |
| --- | --- |
| cluster/control plane 생성 | DB row와 선언 리소스 생성 단계 |
| nodegroup 생성 | `MachineDeployment`까지 선언했는지 확인 |
| OpenStack credential 생성 | cluster별 user/application credential 준비 |
| keypair copy | nodegroup provisioning 전제 |

### 14.2 event-watcher

| 로그 주제 | 해석 |
| --- | --- |
| cluster reconcile | DB cluster status 계산 |
| initial deployment reconcile | 초기 배포 성공/실패 판정 |
| machine deployment reconcile | nodegroup 상태 계산 |
| machine reconcile | node 상태 계산 |
| soot service reconcile | Service LB 상태 계산 |
| soot pv reconcile | PV 상태 계산 |

### 14.3 event-processor

| 로그 주제 | 해석 |
| --- | --- |
| transition state | suspend/resume 적용 단계 |
| cleanup initial deployment | 실패 후 control plane 삭제 |
| cluster deletion resource cleanup | 외부 LB/PV 정리 |
| cloud-conf cleanup | keypair/user/secret 삭제 |
| service gateway claim reconcile | MD별 claim 보장 |
| nodegroup cleanup | 생성 실패 후 data plane 삭제 |

## 15. 상태 판정 예시

### 15.1 “클러스터 생성 중인데 오래 걸린다”

1. DB `Cluster.status`, `internal_status` 확인
2. `Cluster`, `OpenStackCluster`, `KamajiControlPlane`, `TenantControlPlane` 상태 확인
3. `InitialDeploymentStatus`와 addon 상태 확인
4. `HelmChartProxy -> HelmReleaseProxy` 자동 생성 여부 확인
5. `MachineDeployment -> MachineSet -> Machine -> OpenStackMachine` 전개 여부 확인

빠른 해석:

| 현재 값 | 해석 |
| --- | --- |
| `CP-ST-PRV / CP-IST-INF-PRV` | infra 준비 전 |
| `CP-ST-PRV / CP-IST-TCP-PRV` | control plane 준비 전 |
| cluster는 `CP-ST-AV`인데 initial deployment가 `ID-ST-PRV` | cluster 자체는 준비됐고 addon/bastion/data plane 대기 |

### 15.2 “노드그룹 생성 실패처럼 보인다”

1. DB `NodeGroup.status`, `internal_status`
2. `MachineDeployment`, `MachineSet`, `Machine`, `OpenStackMachine`
3. `nodegroup-create-failed` annotation
4. event-watcher machine deployment log
5. event-processor nodegroup cleanup log

빠른 해석:

| 현재 값 | 해석 |
| --- | --- |
| `DP-ST-SU / DP-IST-DP-PRV` | 아직 생성 중 |
| `DP-ST-CRF` | timeout으로 실패 확정 |
| create failed annotation + paused | 실패 후 cleanup 대상 |

### 15.3 “suspend가 끝났는지 모르겠다”

1. `operation-state`
2. `last-suspended-done`
3. KCP replicas
4. HelmRelease `spec.suspend`
5. component deployment replicas
6. DB cluster status

빠른 해석:

| 현재 값 | 해석 |
| --- | --- |
| `suspend-requested` | 요청만 접수 |
| `suspend-applied` | 실행 중 |
| `last-suspended-done` + `CP-ST-SUS` | 완료 |

### 15.4 “클러스터 삭제가 안 끝난다”

1. `Cluster.metadata.finalizers`
2. `HelmRelease`, `HelmChartProxy`, `Ingress` 잔존 여부
3. DB `ClusterOpenStackResource`
4. cloud-conf secret
5. event-watcher cluster log
6. event-processor cluster log

빠른 해석:

| 현재 관찰 | 해석 |
| --- | --- |
| addon 리소스 남음 | watcher 삭제 완료 전 |
| 다른 finalizer 남음 | processor cleanup 시작 전 |
| LB identifier 남음 | 외부 LB cleanup 진행 중 |
| cloud-conf secret 남음 | credential cleanup 전 또는 실패 |

## 16. 추가 확인이 필요한 지점

아래 항목은 현재 코드만으로는 확정하기 어려운 부분이다.

1. `api-manager`의 `CP-ST-F`, `DP-ST-F`는 상태 코드로 정의되어 있지만, 현재 조사한 `event-watcher` 상태 판정 경로에서는 직접 확정되는 흐름이 보이지 않는다.
   - 운영에서 이 값이 실제로 어디서 세팅되는지 추가 확인이 필요하다.
2. 물리 DB 테이블명은 domain model에서 직접 확정할 수 없다.
   - GORM naming strategy 또는 실제 schema 기준으로 별도 표기가 필요하면 DB schema를 추가 확인해야 한다.
3. `TenantControlPlane`, `OpenStackMachine`, `HelmReleaseProxy`는 상위 리소스에서 자동 생성된다고 보이지만, 이 문서 범위에서는 각 외부 controller의 세부 실패 조건까지는 추적하지 않았다.
   - 특정 하위 리소스가 생성되지 않을 때는 해당 외부 controller 로그도 함께 봐야 한다.
4. workload cluster 내부 리소스 점검은 soot controller가 연결된 뒤 가능하다.
   - workload cluster 연결 실패 시 root cluster만 봐서는 `Service`, `PersistentVolume` 상태를 완전히 판정할 수 없다.
5. `ServiceGatewayClaim`은 MachineDeployment owner reference 기반 GC가 기본 경로이고, cluster 삭제 시 label 기반 일괄 삭제가 안전망으로 추가되어 있다.
   - 운영상 어느 경로가 실제로 더 자주 동작하는지는 코드만으로는 알 수 없다.

## 17. 상태 판정 원칙

리소스 상태는 아래 세 가지가 함께 일치할 때 가장 신뢰도가 높다.

1. Kubernetes 리소스의 실제 `status`, `conditions`, `annotations`, `finalizers`
2. controller 로그가 보여 주는 최근 reconcile 결과
3. DB의 `status`, `internal_status`, 연관 row

셋 중 하나라도 다르면 다음 순서로 본다.

1. Kubernetes 실제 상태
2. event-watcher log
3. event-processor log
4. DB 반영 지연 또는 실패 여부

DB는 현재 상태의 사용자 노출값이고, Kubernetes는 실제 리소스 상태이며, controller log는 그 둘 사이에서 어떤 판정이 내려졌는지를 보여 주는 연결 고리다.
