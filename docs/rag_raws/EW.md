# event-watcher

## 0. 문서 목적과 범위

이 문서는 `cmd/event-watcher`가 Kubernetes 리소스 상태를 관찰한 뒤 DB 상태를 어떻게 갱신하고, 어떤 상태 조건에서 어떤 후속 동작을 수행하는지 정리한다.

- 대상 애플리케이션: `cmd/event-watcher`
- 역할: Kubernetes 리소스 상태 감시, DB 상태 동기화, 생성/삭제/스케일/업그레이드 완료 또는 실패 동기화
- 문서 범위:
  - `Cluster`
  - `MachineDeployment`
  - `Machine`
  - `InitialDeployment`
  - workload cluster 내부의 `Service`
  - workload cluster 내부의 `PersistentVolume`
- 제외 범위:
  - `api-manager`의 API 처리
  - `event-processor`의 lifecycle 실행 로직
  - 단순 함수 호출명 나열

이 문서는 함수명을 읽지 않아도 동작을 이해할 수 있도록, 상태 조건과 실제 결과를 중심으로 작성한다.

## 1. 빠른 검색 인덱스

| 검색 키워드 | 관련 컨트롤러 | 핵심 상태 |
| --- | --- | --- |
| control plane provisioning | `ClusterReconciler` | `CP-ST-PRV`, `CP-IST-INF-PRV`, `CP-IST-TCP-PRV` |
| cluster suspended | `ClusterReconciler` | `CP-ST-SUS`, `CP-IST-NONE` |
| cluster delete | `ClusterReconciler` | `CP-ST-DL`, `CP-ST-DLT`, `CP-ST-DLF` |
| nodegroup scale | `MachineDeploymentReconciler` | `DP-ST-SU`, `DP-ST-SD`, `DP-IST-DP-PRV`, `DP-IST-DP-DL` |
| nodegroup upgrade | `MachineDeploymentReconciler` | `DP-ST-CF`, `DP-IST-DP-UG`, `DP-ST-UF` |
| node provisioning | `MachineReconciler` | `NO-ST-PRV`, `NO-IST-VM-PRV`, `NO-IST-NO-PRV` |
| node delete | `MachineReconciler` | `NO-ST-DL`, `NO-IST-NO-DR`, `NO-IST-VO-DT`, `NO-IST-VM-DL`, `NO-IST-NO-DL` |
| initial deployment | `InitialDeploymentReconciler` | `ID-ST-PRV`, `ID-ST-SC`, `ID-ST-ERR`, `ID-ST-CC`, `ID-ST-NO` |
| load balancer service | `ServiceReconciler` | `CR-ST-CR`, `CR-ST-UD`, `CR-ST-AV`, `CR-ST-DL`, `CR-ST-DLT` |
| persistent volume | `PersistentVolumeReconciler` | `CR-ST-CRT`, `CR-ST-AT`, `CR-ST-DT`, `CR-ST-DL`, `CR-ST-DLT` |

## 2. 역할

`event-watcher`는 리소스를 직접 배포하는 컨트롤러가 아니다. 이미 존재하는 Kubernetes 리소스의 현재 상태를 읽고, 아래 작업을 수행한다.

1. Kubernetes 리소스 상태를 내부 도메인 상태 코드로 변환한다.
2. DB의 `Cluster`, `NodeGroup`, `Node`, `InitialDeploymentStatus`, `ClusterOpenStackResource` 레코드를 갱신한다.
3. 상태가 의미 있는 종착점에 도달하면 동기화 annotation을 기록한다.
4. 실패 상태가 확정되면 일부 리소스를 pause 하거나 알림을 발생시킨다.
5. 삭제 완료 상태가 확정되면 DB 레코드를 정리하고 자체 finalizer를 제거한다.

### 2.1 공통 처리 제외 조건

| 대상 | 제외 조건 |
| --- | --- |
| `Cluster` | Kubernetes에는 남아 있지만 DB 레코드가 이미 없으면 삭제 완료로만 반영 |
| `Machine` | 소속 `Cluster` 또는 `MachineDeployment`를 찾지 못하면 관리 대상이 아니라고 보고 skip |
| `Service` | tenant ID 또는 cluster name annotation이 없거나, root cluster를 찾지 못하면 skip |
| `Service` | DB 레코드가 없고 Service type이 `LoadBalancer`가 아니면 새 리소스로 저장하지 않고 skip |
| `PersistentVolume` | root cluster를 찾지 못하면 skip |
| `PersistentVolume` | CSI driver가 cinder/manila가 아니면 skip |

## 3. 주요 디렉토리

```text
./cmd/event-watcher
├── api: 상태 코드, annotation key, 공통 타입
├── appconfig: timeout 등 controller 설정
├── config: kubebuilder 배포 설정
├── controllers: manager 등록부
├── errors: 정의 에러
├── internal
│   ├── controller: root cluster 리소스 감시 컨트롤러
│   └── soot
│       ├── manager.go: workload cluster 접근용 soot manager
│       └── controller: workload cluster 내부 리소스 감시 컨트롤러
└── util
    ├── db: 트랜잭션 처리
    ├── patch: annotation patch helper
    ├── patchhelper: DB 변경 감지 helper
    └── sync: 완료/실패 동기화 helper
```

## 4. 실제 상태 코드 사전

### 4.1 Cluster 상태

| 의미 | 실제 값 |
| --- | --- |
| Provisioning | `CP-ST-PRV` |
| Configuring | `CP-ST-CF` |
| Available | `CP-ST-AV` |
| Unavailable | `CP-ST-UAV` |
| Suspended | `CP-ST-SUS` |
| Deleting | `CP-ST-DL` |
| Deleted | `CP-ST-DLT` |
| Deletion Failed | `CP-ST-DLF` |
| Unknown | `CP-ST-UN` |

| 내부 의미 | 실제 값 |
| --- | --- |
| Infrastructure provisioning | `CP-IST-INF-PRV` |
| Tenant control plane provisioning | `CP-IST-TCP-PRV` |
| Tenant control plane upgrading | `CP-IST-TCP-UG` |
| Tenant control plane deleting | `CP-IST-TCP-DL` |
| Infrastructure deleting | `CP-IST-INF-DL` |
| Unknown | `CP-IST-UN` |
| None | `CP-IST-NONE` |

### 4.2 NodeGroup 상태

| 의미 | 실제 값 |
| --- | --- |
| Scaling Up | `DP-ST-SU` |
| Scaling Down | `DP-ST-SD` |
| Configuring | `DP-ST-CF` |
| Available | `DP-ST-AV` |
| Deleting | `DP-ST-DL` |
| Deleted | `DP-ST-DLT` |
| Deletion Failed | `DP-ST-DLF` |
| Unknown | `DP-ST-UN` |
| Create Failed by timeout | `DP-ST-CRF` |
| Scale Failed by timeout | `DP-ST-SF` |
| Upgrade Failed by timeout | `DP-ST-UF` |

| 내부 의미 | 실제 값 |
| --- | --- |
| Data plane provisioning | `DP-IST-DP-PRV` |
| Data plane unavailable | `DP-IST-DP-UAV` |
| Data plane deleting | `DP-IST-DP-DL` |
| Data plane upgrading | `DP-IST-DP-UG` |
| Unknown | `DP-IST-UN` |
| None | `DP-IST-NONE` |

### 4.3 Node 상태

| 의미 | 실제 값 |
| --- | --- |
| Provisioning | `NO-ST-PRV` |
| Available | `NO-ST-AV` |
| Unavailable | `NO-ST-UAV` |
| Deleting | `NO-ST-DL` |
| Deleted | `NO-ST-DLT` |
| Unknown | `NO-ST-UN` |

| 내부 의미 | 실제 값 |
| --- | --- |
| VM provisioning | `NO-IST-VM-PRV` |
| Node provisioning | `NO-IST-NO-PRV` |
| Node unavailable | `NO-IST-NO-UAV` |
| Node draining | `NO-IST-NO-DR` |
| Volume detaching | `NO-IST-VO-DT` |
| VM deleting | `NO-IST-VM-DL` |
| Node deleting | `NO-IST-NO-DL` |
| Unknown | `NO-IST-UN` |
| None | `NO-IST-NONE` |

### 4.4 InitialDeployment 상태

| 의미 | 실제 값 |
| --- | --- |
| Success | `ID-ST-SC` |
| Provisioning | `ID-ST-PRV` |
| Error | `ID-ST-ERR` |
| Cancelled | `ID-ST-CC` |
| No initial component | `ID-ST-NO` |

### 4.5 ClusterResource 상태

| 의미 | 실제 값 |
| --- | --- |
| Unknown | `CR-ST-UN` |
| Created | `CR-ST-CRT` |
| Attached | `CR-ST-AT` |
| Detached | `CR-ST-DT` |
| Creating | `CR-ST-CR` |
| Updating | `CR-ST-UD` |
| Available | `CR-ST-AV` |
| Deleting | `CR-ST-DL` |
| Deleted | `CR-ST-DLT` |

`Service`가 관찰하는 OpenStack LoadBalancer provisioning status:

| OpenStack 값 | 의미 |
| --- | --- |
| `ACTIVE` | 사용 가능 |
| `ERROR` | 오류 |
| `PENDING_CREATE` | 생성 중 |
| `PENDING_UPDATE` | 갱신 중 |
| `PENDING_DELETE` | 삭제 중 |

## 5. 컨트롤러별 상태 조건과 동작

### 5.1 ClusterReconciler

- 소스: `cmd/event-watcher/internal/controller/cluster_controller.go`
- 감시 리소스: `Cluster`
- DB 대상: `Cluster`
- 부가 감시 리소스: `KamajiControlPlane`, `TenantControlPlane`, `OpenStackCluster`, `HelmRelease`, `HelmChartProxy`, `Ingress`

### 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| `clusteroperation...SuspendedDone` annotation 존재 | `CP-ST-SUS` | `CP-IST-NONE` | 클러스터가 suspend 완료된 것으로 DB 반영 |
| TenantControlPlane version status가 upgrading | `CP-ST-CF` | `CP-IST-TCP-UG` | 제어 plane upgrade 중으로 간주 |
| `InfrastructureReady=false` | `CP-ST-PRV` | `CP-IST-INF-PRV` | 인프라 생성 중으로 반영 |
| `InfrastructureReady=true`, `ControlPlaneReady=false`, control plane version status가 비어 있거나 provisioning | `CP-ST-PRV` | `CP-IST-TCP-PRV` | tenant control plane 생성 중으로 반영 |
| `InfrastructureReady=true`, `ControlPlaneReady=false`, control plane version status가 provisioning이 아님 | `CP-ST-UAV` | `CP-IST-NONE` | 인프라는 준비됐지만 control plane이 사용 불가한 상태로 반영 |
| `InfrastructureReady=true`, `ControlPlaneReady=true` | `CP-ST-AV` | `CP-IST-NONE` | 사용 가능 상태로 반영 |

### 삭제 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| `deletionTimestamp` 존재 | `CP-ST-DL` | 아래 삭제 내부 상태 규칙에 따름 | 삭제 진행 상태로 DB 반영 |
| 삭제 중이며 `ControlPlaneReady=false`이고 삭제 이유가 `DeletedReason` | `CP-ST-DL` | `CP-IST-INF-DL` | 인프라 삭제 단계로 반영 |
| 삭제 중이며 위 조건이 아님 | `CP-ST-DL` | `CP-IST-TCP-DL` | tenant control plane 삭제 단계로 반영 |
| 삭제 시작 후 timeout 초과 | `CP-ST-DLF` | `CP-IST-NONE` | 삭제 실패로 확정 |
| `HelmRelease`, `HelmChartProxy`, 동일 이름 `Ingress`가 모두 사라졌고 CAPI `ClusterFinalizer`도 없음 | `CP-ST-DLT` | `CP-IST-NONE` | 삭제 완료로 확정 |

### 삭제 중 대기 조건

삭제 중에는 아래 리소스가 남아 있으면 `CP-ST-DL` 상태를 유지하고 재시도한다.

| 남아 있는 리소스 | 대기 이유 |
| --- | --- |
| `HelmRelease` | `helm releases exist` |
| `HelmChartProxy` | `helm chart proxies exist` |
| cluster와 같은 이름의 `Ingress` | `ingress exists` |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `CP-ST-DL` | cluster delete started 동기화 annotation을 기록 |
| `CP-ST-DLF` | cluster delete failed 동기화, 실패 알림, cluster pause annotation 추가 |
| `CP-ST-DLT` | cluster delete completed 동기화, 보안 그룹 ID를 함께 전달, event-watcher finalizer 제거 |

### 공통 갱신 규칙

- DB 트랜잭션이 성공한 뒤 `cluster.x-k8s.io.ew/cluster-id` annotation을 보정한다.
- security group ID가 있으면 `cluster.x-k8s.io.ew/cluster-sg-id`도 유지한다.
- DB 레코드가 이미 없으면 현재 cluster annotation ID를 사용해 `CP-ST-DLT`, `CP-IST-NONE`으로 간주한다.

### 5.2 MachineDeploymentReconciler

- 소스: `cmd/event-watcher/internal/controller/machinedeployment_controller.go`
- 감시 리소스: `MachineDeployment`
- DB 대상: `NodeGroup`
- 부가 감시 리소스: `MachineSet`, `Cluster`

### 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| rolling upgrade 진행 annotation `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress=true` | `DP-ST-CF` | `DP-IST-DP-UG` | 업그레이드 진행 상태로 반영 |
| `MachineDeployment.Status.Phase=ScalingUp`이고 desired replicas와 실제 replicas가 다름 | `DP-ST-SU` | `DP-IST-DP-PRV` | 새 data plane 인스턴스가 아직 충분히 생성되지 않은 상태 |
| `MachineDeployment.Status.Phase=ScalingUp`이고 desired replicas와 실제 replicas가 같음 | `DP-ST-SU` | `DP-IST-DP-UAV` | 수량은 맞지만 아직 안정화되지 않은 상태 |
| `MachineDeployment.Status.Phase=Running`이고 desired replicas < current replicas | `DP-ST-SD` | `DP-IST-DP-DL` | 축소 중 상태 |
| `MachineDeployment.Status.Phase=Running`이고 위 조건이 아님 | `DP-ST-AV` | `DP-IST-NONE` | 사용 가능 상태 |
| `MachineDeployment.Status.Phase=ScalingDown` | `DP-ST-SD` | `DP-IST-DP-DL` | 축소 중 상태 |

### rolling upgrade 감지

| 조건 | 동작 |
| --- | --- |
| 현재 `MachineDeployment` 버전과 다른 `MachineSet`이 있고, 그 `MachineSet` replica가 1 이상 | `nodegroup-upgrade-in-progress=true` annotation을 추가 |
| 오래된 `MachineSet`이 없고, updated/ready/available replicas가 모두 desired replicas와 같음 | `nodegroup-upgrade-in-progress` annotation 제거 |

### timeout에 의한 실패 상태

초기 배포가 성공한 뒤에만 아래 timeout을 검사한다.

| timeout 기준 annotation | 실패 status | 조건 |
| --- | --- | --- |
| `last-created` | `DP-ST-CRF` | 생성 완료 annotation이 없고 허용 시간 초과 |
| `last-scaled` | `DP-ST-SF` | scale 완료 전 허용 시간 초과 |
| `last-upgraded` | `DP-ST-UF` | upgrade 완료 전 허용 시간 초과 |

추가 규칙:

- 관리자가 수동으로 pause를 풀어 둔 경우, 이미 실패 처리된 작업은 timeout 재검사를 건너뛴다.
- 생성 timeout은 initial deployment node 또는 이미 생성 완료된 node group에는 적용하지 않는다.

### 삭제 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| `deletionTimestamp` 존재 | `DP-ST-DL` | `DP-IST-DP-DL` | 삭제 진행 상태 |
| 삭제 시작 후 timeout 초과 | `DP-ST-DLF` | `DP-IST-NONE` | 삭제 실패 확정 |
| CAPI `MachineDeploymentFinalizer` 없음 | `DP-ST-DLT` | `DP-IST-NONE` | 삭제 완료 확정 |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `DP-ST-AV` | scale 완료, upgrade 완료, 생성 완료 여부를 각각 확인하고 필요한 동기화 annotation 기록 |
| `DP-ST-CRF` | node group 생성 실패 동기화, MachineDeployment pause |
| `DP-ST-SF` | node group scale 실패 동기화, MachineDeployment pause |
| `DP-ST-UF` | node group upgrade 실패 동기화, MachineDeployment pause |
| `DP-ST-DLF` | node group 삭제 실패 동기화 |
| `DP-ST-DLT` | node group 삭제 완료 동기화 후 event-watcher finalizer 제거 |

### 공통 갱신 규칙

- `cluster.x-k8s.io.ew/nodegroup-id` annotation으로 DB 레코드와 연결한다.
- 요청 replica 수, 가용 replica 수, Kubernetes version, 상태, 내부 상태를 DB에 반영한다.
- initial deployment가 끝나지 않았다면 생성/스케일/업그레이드 timeout 판단을 하지 않는다.

### 5.3 MachineReconciler

- 소스: `cmd/event-watcher/internal/controller/machine_controller.go`
- 감시 리소스: `Machine`
- DB 대상: `Node`
- 부가 감시 리소스: `MachineDeployment`

### 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| `Machine.Status.Phase=Running`이고 `MachineNodeHealthy=True` | `NO-ST-AV` | `NO-IST-NONE` | 노드 사용 가능 |
| `Machine.Status.Phase=Running`이고 `MachineNodeHealthy=False` | `NO-ST-UAV` | `NO-IST-NO-UAV` | 노드 health 불량 |
| `Machine.Status.Phase=Running`이지만 healthy 조건이 false가 아님 | `NO-ST-UAV` | `NO-IST-UN` | 사용 불가이나 원인이 명확하지 않음 |
| 위 조건이 아니고 `InfrastructureReady=false` | `NO-ST-PRV` | `NO-IST-VM-PRV` | VM 생성 중 |
| 위 조건이 아니고 `InfrastructureReady=true` | `NO-ST-PRV` | `NO-IST-NO-PRV` | VM은 준비됐고 node 등록 중 |

### 삭제 상태 판정

| 조건 | status | internal_status | 실제 동작 |
| --- | --- | --- | --- |
| `deletionTimestamp` 존재 | `NO-ST-DL` | 아래 삭제 내부 상태 규칙에 따름 | 삭제 진행 상태 |
| cluster 자체가 삭제 중 | `NO-ST-DL` | `NO-IST-VM-DL` | cluster 삭제에 종속된 VM 삭제로 간주 |
| drain timeout 전이고 `DrainingSucceeded`가 `Unknown` 또는 `False` | `NO-ST-DL` | `NO-IST-NO-DR` | node drain 중 |
| drain timeout 전이고 volume detach timeout 전이며 `VolumeDetachSucceeded`가 `Unknown` 또는 `False` | `NO-ST-DL` | `NO-IST-VO-DT` | volume detach 중 |
| `InfrastructureReady`가 `Unknown` 또는 `True` | `NO-ST-DL` | `NO-IST-VM-DL` | VM 삭제 중 |
| 그 외 | `NO-ST-DL` | `NO-IST-NO-DL` | node 삭제 단계 |
| CAPI `MachineFinalizer` 없음 | `NO-ST-DLT` | `NO-IST-NONE` | 삭제 완료 |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `NO-ST-AV` | initial deployment가 성공한 뒤 node 생성 완료 동기화 annotation 기록 |
| `NO-ST-DLT` | node 생성 완료 이력이 있고 initial deployment가 성공한 뒤 node 삭제 완료 동기화, event-watcher finalizer 제거 |

### 공통 갱신 규칙

- 최초 DB 저장 시 기본 상태는 `NO-ST-UN`, `NO-IST-UN`이다.
- `Machine.Spec.ProviderID`에서 instance ID를 추출한다.
- machine address를 DB address 구조로 변환해 저장한다.
- `cluster.x-k8s.io.ew/node-id` annotation으로 DB 레코드와 연결한다.

### 5.4 InitialDeploymentReconciler

- 소스: `cmd/event-watcher/internal/controller/initial_deployment_controller.go`
- 감시 리소스: `Cluster`
- DB 대상: `InitialDeploymentStatus`, `InitialAddonDeploymentStatus`
- 평가 대상: bastion ingress, control plane, initial `MachineDeployment`, addon `HelmReleaseProxy`, addon `HelmRelease`

### 최초 상태

| 대상 | 초기 status |
| --- | --- |
| 전체 initial deployment | `ID-ST-PRV` |
| bastion | `ID-ST-PRV` |
| control plane | `ID-ST-PRV` |
| data plane | `ID-ST-PRV` |
| addon들 | `ID-ST-PRV` |

### 컴포넌트별 상태 판정

| 대상 | 조건 | status | 실제 동작 |
| --- | --- | --- | --- |
| bastion | cluster와 같은 이름의 `Ingress` 존재 | `ID-ST-SC` | bastion 준비 완료 |
| bastion | 초기 bastion이 없는 구성 | `ID-ST-NO` | 성공 판정 시 허용 |
| control plane | `ControlPlaneReady=True` | `ID-ST-SC` | control plane 준비 완료 |
| control plane | 준비되지 않음 | `ID-ST-PRV` | condition message를 에러 사유로 저장 |
| data plane | initial label을 가진 `MachineDeployment`가 정확히 1개이고 Available condition true | `ID-ST-SC` | initial data plane 준비 완료 |
| data plane | 위 조건 불충족 | `ID-ST-PRV` | condition message를 에러 사유로 저장 |
| addon | 관련 `HelmReleaseProxy` 또는 `HelmRelease` Ready true | `ID-ST-SC` | addon 준비 완료 |
| addon | Ready false | `ID-ST-PRV` | condition message를 에러 사유로 저장 |

### 전체 상태 판정

| 조건 | 전체 status |
| --- | --- |
| 모든 addon이 `ID-ST-SC`이고, bastion이 `ID-ST-SC` 또는 `ID-ST-NO`이며, control plane과 data plane이 모두 `ID-ST-SC` | `ID-ST-SC` |
| 삭제 중에 아직 provisioning 상태 | `ID-ST-CC` |
| 허용 시간 안에 성공하지 못함 | `ID-ST-ERR` |
| 그 외 진행 중 | `ID-ST-PRV` |

### 기존 종착 상태 재진입 규칙

| 기존 DB status | 동작 |
| --- | --- |
| `ID-ST-SC` | 추가 계산을 중단하고 성공 상태를 유지 |
| `ID-ST-ERR` | 추가 계산을 중단하고 에러 상태를 유지 |
| `ID-ST-CC` | 추가 계산을 중단하고 취소 상태를 유지 |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `ID-ST-PRV` | 아직 cluster create started 동기화가 없다면 생성 시작 동기화 annotation 기록 |
| `ID-ST-SC` | `initial-deployment-completed=true` 기록, cluster creation completed 동기화, 모든 node group에 creation completed 동기화 |
| `ID-ST-ERR` | Sentry 알림, `initial-deployment-completed=false` 기록, cluster creation failed 동기화, 모든 `HelmRelease` suspend, cluster에 CAPI pause와 event-watcher pause annotation 추가 |
| `ID-ST-CC` | 삭제 흐름 중 취소 상태 유지 |

### 5.5 ServiceReconciler

- 소스: `cmd/event-watcher/internal/soot/controller/service_controller.go`
- 감시 리소스: workload cluster 내부 `Service`
- 대상 조건: tenant/cluster annotation으로 root cluster를 찾을 수 있는 Service
- DB 대상: `ClusterOpenStackResource`
- 외부 조회 대상: OpenStack LoadBalancer

### 상태 판정

| 조건 | status | 실제 동작 |
| --- | --- | --- |
| `deletionTimestamp` 존재 | `CR-ST-DL` | 삭제 진행 상태 |
| `deletionTimestamp` 존재하고 `service.kubernetes.io/load-balancer-cleanup` finalizer 없음 | `CR-ST-DLT` | 삭제 완료로 확정 |
| LB ID annotation 없음 | `CR-ST-CR` | 생성 중으로 반영하고 재시도 |
| LB ID는 있으나 OpenStack 조회 결과 404 | `CR-ST-DLT` | 이미 외부 LB가 사라진 것으로 간주 |
| OpenStack provisioning status `ACTIVE` | `CR-ST-AV` | identifier, resource name, external IP 저장 |
| OpenStack provisioning status `ERROR` | `CR-ST-UN` | 오류 상태 |
| OpenStack provisioning status `PENDING_CREATE` | `CR-ST-CR` | 생성 중, 재시도 |
| OpenStack provisioning status `PENDING_UPDATE` | `CR-ST-UD` | 갱신 중, 재시도 |
| OpenStack provisioning status `PENDING_DELETE` | `CR-ST-DL` | 삭제 흐름으로 전환 |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `CR-ST-AV` | load balancer 생성 완료 동기화를 보장하고, 방금 생성된 리소스면 DB commit 이후 재평가하도록 재시도하며, 이미 생성된 리소스면 update 완료 동기화를 수행 |
| `CR-ST-DLT` | load balancer 삭제 완료 동기화, cleanup annotation 제거, DB 레코드 삭제, event-watcher finalizer 제거 |

### 공통 갱신 규칙

- `tenant.meta.x-k8s.io/id`, `cluster.meta.x-k8s.io/name`으로 root cluster를 찾는다.
- `cluster.x-k8s.io.ew/resource-id` annotation으로 DB 레코드와 연결한다.
- 기존 DB 레코드가 없을 때 Service type이 `LoadBalancer`이면 새 `ClusterOpenStackResource`를 만든다.
- 기존 DB 레코드가 없고 Service type이 `LoadBalancer`가 아니면 관리 대상에서 제외한다.
- `CR-ST-AV`일 때만 identifier, resource name, extra external IP를 DB에 저장한다.

### 5.6 PersistentVolumeReconciler

- 소스: `cmd/event-watcher/internal/soot/controller/pv_controller.go`
- 감시 리소스: workload cluster 내부 `PersistentVolume`
- 대상 조건: CSI driver가 `cinder.csi.openstack.org` 또는 `manila.csi.openstack.org`
- DB 대상: `ClusterOpenStackResource`
- 부가 감시 리소스: `VolumeAttachment`, `PersistentVolumeClaim`

### 상태 판정

| 조건 | status | 실제 동작 |
| --- | --- | --- |
| PV phase가 `Available` 또는 `Bound`이고 CSI volume handle 존재 | `CR-ST-CRT` | 외부 volume이 생성된 상태 |
| 위 조건이며 연결된 `VolumeAttachment`가 attached=true | `CR-ST-AT` | 외부 volume attach 완료 |
| PV phase가 `Released` | `CR-ST-DT` | detach 완료 상태 |
| 위 조건에 속하지 않음 | `CR-ST-UN` | 판정 불가 |
| `deletionTimestamp` 존재 | `CR-ST-DL` | 삭제 진행 |
| `deletionTimestamp` 존재하고 `kubernetes.io/pv-protection` finalizer 없음 | `CR-ST-DLT` | 삭제 완료 |

### 상태별 후속 동작

| status | 후속 동작 |
| --- | --- |
| `CR-ST-CRT` | volume 생성 완료 보장, resize 완료 여부 확인, detach 필요 여부 확인 |
| `CR-ST-AT` | attach 완료 보장, resize 완료 여부 확인 |
| `CR-ST-DT` | detach 완료 보장 |
| `CR-ST-DLT` | detach 완료 보장, volume 삭제 완료 보장, event-watcher finalizer 제거 |

### 삭제 시 추가 동작

| 조건 | 동작 |
| --- | --- |
| reclaim policy가 `Retain`이고 cinder 또는 manila volume이며 전용 metadata key가 존재 | OpenStack volume/share metadata에서 전용 KINX metadata key 제거 |
| 삭제 완료 확정 | attached VM ID를 빈 값으로 두고, storage/provider/static provisioning 여부를 extra에 저장 |

### 공통 갱신 규칙

- `tenant.meta.x-k8s.io/id`, `cluster.meta.x-k8s.io/name`으로 root cluster를 찾는다.
- `cluster.x-k8s.io.ew/resource-id` annotation으로 DB 레코드와 연결한다.
- 정적 프로비저닝 PV도 상태는 관리하되, 외부 생성/삭제 동작은 제한적으로 처리한다.
- `VolumeAttachment` 변경과 `PersistentVolumeClaim` 변경도 PV 재조정 트리거로 사용한다.

### 5.7 soot manager

- 소스: `cmd/event-watcher/internal/soot/manager.go`
- 역할: root cluster별 workload cluster 접근용 controller 집합을 시작하고, cluster 삭제 시 해당 controller 집합을 정리한다.

| 조건 | 동작 |
| --- | --- |
| workload cluster 접근 대상 cluster 발견 | cluster별 soot manager entry 생성 |
| cluster 삭제 | 내부 controller context cancel, 종료 대기, cache entry 제거, soot manager finalizer 제거 |

## 6. 주요 annotation과 finalizer

### 6.1 식별자 annotation

| 용도 | key |
| --- | --- |
| Cluster ID | `cluster.x-k8s.io.ew/cluster-id` |
| Cluster security group ID | `cluster.x-k8s.io.ew/cluster-sg-id` |
| NodeGroup ID | `cluster.x-k8s.io.ew/nodegroup-id` |
| Node ID | `cluster.x-k8s.io.ew/node-id` |
| ClusterResource ID | `cluster.x-k8s.io.ew/resource-id` |

### 6.2 동기화 annotation

| 이벤트 | key prefix |
| --- | --- |
| cluster create started/completed/failed | `cluster-create-*` |
| cluster delete started/completed/failed | `cluster-delete-*` |
| nodegroup create/scale/upgrade/delete completed or failed | `nodegroup-*` |
| node create/delete completed | `node-*` |
| cluster resource create/update/attach/detach/delete completed | `resource-*` |

실제 key는 모두 `cluster.x-k8s.io.ew/` prefix를 가진다.

### 6.3 pause 관련 annotation

| 용도 | key |
| --- | --- |
| Cluster pause | `cluster.x-k8s.io.ew/paused` |
| Initial deployment pause | `cluster.x-k8s.io.ew/initial-deployment-paused` |
| Initial deployment completed | `cluster.x-k8s.io.ew/initial-deployment-completed` |
| NodeGroup pause | `cluster.x-k8s.io.ew/nodegroup-paused` |

### 6.4 finalizer

| 리소스 | finalizer |
| --- | --- |
| ClusterResource 공통 | `cluster.x-k8s.io.ew/resource` |
| PersistentVolume 삭제 판정 기준 | `kubernetes.io/pv-protection` |
| Service LB 삭제 판정 기준 | `service.kubernetes.io/load-balancer-cleanup` |
| soot manager | `cluster.x-k8s.io.ew/soot-manager` |

## 7. 운영상 중요한 메모

1. `event-watcher`는 상태를 계산하는 주체이며, 실제 생성 요청의 시작점은 아니다.
2. 상태 코드가 같아도 `internal_status`가 다르면 운영 의미가 달라진다. 예를 들어:
   - `CP-ST-PRV` + `CP-IST-INF-PRV`
   - `CP-ST-PRV` + `CP-IST-TCP-PRV`
   - `NO-ST-DL` + `NO-IST-NO-DR`
   - `NO-ST-DL` + `NO-IST-VO-DT`
3. 삭제 완료 판단은 단순 `deletionTimestamp`가 아니라 finalizer 제거와 하위 리소스 정리까지 포함한다.
4. initial deployment가 성공하기 전에는 node group 생성/스케일/업그레이드 timeout을 판정하지 않는다.
5. 실패 상태 일부는 리소스를 자동 pause 한다.
   - initial deployment 실패: `HelmRelease` suspend, cluster pause
   - node group 생성/스케일/업그레이드 실패: `MachineDeployment` pause
   - cluster 삭제 실패: cluster pause
6. workload cluster 내부 리소스(`Service`, `PersistentVolume`)는 soot controller가 root cluster와 연결한 뒤에만 관리한다.
