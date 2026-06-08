# IKS v2 Kubernetes 운영 가이드

## 0. 문서 범위

이 문서는 IKS v2를 Kubernetes 리소스 정보만으로 판정하기 위한 운영 가이드다.

- 사용 정보:
  - Kubernetes 리소스의 `status`
  - Kubernetes 리소스의 `conditions`
  - Kubernetes 리소스의 `annotations`
  - Kubernetes 리소스의 `labels`
  - Kubernetes 리소스의 `finalizers`
  - controller 로그
- 사용하지 않는 정보:
  - DB 값
  - DB status
  - DB internal status
- 근거 문서:
  - `AM.md`
  - `EW.md`
  - `EP.md`

## 1. 검색 인덱스

| 검색 주제 | 관련 섹션 | 핵심 키워드 |
| --- | --- | --- |
| cluster 생성 | `7. Cluster 생성과 초기 준비` | `Cluster`, `OpenStackCluster`, `KamajiControlPlane`, `TenantControlPlane` |
| initial deployment | `8. Initial deployment` | `initial-deployment`, `cluster-create-started`, `cluster-create-completed` |
| nodegroup 생성 | `9. NodeGroup 생성과 전개` | `MachineDeployment`, `MachineSet`, `Machine`, `OpenStackMachine` |
| nodegroup scale | `10. NodeGroup scale` | `last-scaled`, `nodegroup-scale-completed`, `nodegroup-scale-failed` |
| nodegroup upgrade | `11. NodeGroup upgrade` | `nodegroup-upgrade-in-progress`, `last-upgraded` |
| node 삭제 | `12. Machine 상태와 삭제` | `DrainingSucceeded`, `VolumeDetachSucceeded` |
| suspend resume | `13. Cluster suspend / resume` | `operation-state`, `suspend-requested`, `last-suspended-done` |
| service loadbalancer | `14. Service LoadBalancer` | `kinx-load-balancer-id`, `resource-create-completed` |
| persistent volume | `15. PersistentVolume` | `VolumeAttachment`, `resource-attach-completed` |
| cluster 삭제 | `16. Cluster 삭제` | `cluster-delete-started`, `cluster-delete-completed`, finalizer |

## 2. 컴포넌트 역할

| 컴포넌트 | Kubernetes 관점의 역할 |
| --- | --- |
| `api-manager` | 상위 선언 리소스를 생성하고 필요한 label/annotation을 기록한다 |
| `event-watcher` | 리소스 상태를 관찰하고 완료/실패 동기화 annotation을 기록한다 |
| `event-processor` | suspend/resume, cleanup, remote setup 같은 실제 lifecycle 동작을 실행한다 |

## 3. 자동 생성 리소스 관계

다음 하위 리소스는 상위 리소스 생성 후 각 관리 controller가 자동 생성한다.

| 상위 리소스 | 자동 생성 하위 리소스 |
| --- | --- |
| `KamajiControlPlane` | `TenantControlPlane` |
| `MachineDeployment` | `MachineSet` |
| `MachineSet` | `Machine` |
| `Machine` | `OpenStackMachine` |
| `HelmChartProxy` | `HelmReleaseProxy` |

하위 리소스가 없을 때는 다음처럼 판단한다.

| 관찰 | 판단 |
| --- | --- |
| 상위 리소스도 없음 | 상위 리소스 생성 전 또는 생성 실패 |
| 상위 리소스는 있고 하위 리소스 없음 | 하위 controller의 생성 대기 또는 실패 |
| 상위와 하위 모두 있음 | 다음 단계 상태 조건을 확인 |

## 4. 리소스 계층

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

PersistentVolume
├── PersistentVolumeClaim
└── VolumeAttachment
```

## 5. 공통 조회 명령

```bash
NS=<tenant-id>
CLUSTER=<cluster-name>
MD=<machine-deployment-name>
```

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get helmrelease -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get helmchartproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

metadata만 빠르게 볼 때:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'

kubectl -n "$NS" get machinedeployment "$MD" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'
```

condition만 빠르게 볼 때:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} message={.message}{"\n"}{end}'

kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} reason={.reason}{"\n"}{end}{end}'
```

## 6. 핵심 metadata 사전

### 6.1 cluster metadata

| 용도 | key |
| --- | --- |
| cluster name label | `cluster.x-k8s.io/cluster-name` |
| cluster ID | `cluster.x-k8s.io.ew/cluster-id` |
| cluster security group ID | `cluster.x-k8s.io.ew/cluster-sg-id` |
| event-watcher pause | `cluster.x-k8s.io.ew/paused` |
| CAPI pause | `cluster.x-k8s.io/paused` |
| initial deployment completed | `cluster.x-k8s.io.ew/initial-deployment-completed` |
| initial deployment paused | `cluster.x-k8s.io.ew/initial-deployment-paused` |

### 6.2 initial deployment label

| 용도 | key |
| --- | --- |
| 최초 node group 식별 | `cluster.x-k8s.io.ew/initial-deployment=true` |

### 6.3 node group / node metadata

| 용도 | key |
| --- | --- |
| node group ID | `cluster.x-k8s.io.ew/nodegroup-id` |
| node group paused | `cluster.x-k8s.io.ew/nodegroup-paused` |
| node group upgrade in progress | `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress` |
| node ID | `cluster.x-k8s.io.ew/node-id` |

### 6.4 suspend / resume metadata

| 용도 | key |
| --- | --- |
| current operation state | `cluster.x-k8s.io.am/operation-state` |
| suspend completed timestamp | `cluster.x-k8s.io.ep/last-suspended-done` |
| resume completed timestamp | `cluster.x-k8s.io.ep/last-resumed-done` |

| operation state 값 | 의미 |
| --- | --- |
| `suspend-requested` | suspend 요청 접수 |
| `suspend-applied` | suspend 실행 적용 |
| `resume-requested` | resume 요청 접수 |
| `resume-applied` | resume 실행 적용 |

### 6.5 동기화 annotation

| 이벤트 | annotation |
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

대부분의 동기화 annotation에는 대응하는 `-timestamp` annotation도 함께 기록된다.

## 7. Cluster 생성과 초기 준비

### 7.1 api-manager가 직접 생성하는 상위 리소스

cluster 생성 요청 후 다음 리소스가 직접 생성된다.

| 리소스 |
| --- |
| `OpenStackCluster` |
| `KamajiControlPlane` |
| `Cluster` |
| `HelmRelease` |
| `HelmChartProxy` |
| 초기 `MachineDeployment` |
| `KubeadmConfigTemplate` |
| `OpenStackMachineTemplate` |
| 조건부 `MachineHealthCheck` |

### 7.2 cluster 생성 초기 단계 판정

| 관찰 | 판단 |
| --- | --- |
| `OpenStackCluster`, `KamajiControlPlane`, `Cluster` 중 일부가 없음 | 상위 선언 리소스 생성이 끝나지 않음 |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji controller가 하위 리소스를 아직 만들지 못함 |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | addon controller가 아직 하위 리소스를 만들지 못함 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller 전개 전 |

### 7.3 Cluster 상태 조건

| Kubernetes 조건 | 의미 |
| --- | --- |
| `Cluster.status.infrastructureReady=false` | 인프라 준비 전 |
| `InfrastructureReady=true`, `ControlPlaneReady=false`, `TenantControlPlane` version status가 비어 있거나 provisioning | tenant control plane 준비 중 |
| `InfrastructureReady=true`, `ControlPlaneReady=false`, tenant control plane이 provisioning이 아님 | control plane 이상 가능 |
| `InfrastructureReady=true`, `ControlPlaneReady=true` | cluster control plane 준비 완료 |
| `TenantControlPlane` version status가 upgrading | control plane upgrade 중 |
| `cluster.x-k8s.io.ep/last-suspended-done` 존재 | suspend 완료 상태 |

### 7.4 cluster 생성 과정에서 기록되는 annotation

| 조건 | 기록되는 annotation |
| --- | --- |
| initial deployment가 아직 provisioning이고 cluster create started 동기화가 없음 | `cluster-create-started=true` |
| initial deployment 전체 성공 | `cluster-create-completed=true` |
| initial deployment 실패 | `cluster-create-failed=true` |

## 8. Initial deployment

### 8.1 성공 조건

| 구성 요소 | 성공 조건 |
| --- | --- |
| bastion | cluster와 같은 이름의 `Ingress` 존재, 또는 초기 bastion 불필요 |
| control plane | `Cluster`의 `ControlPlaneReady=True` |
| data plane | `cluster.x-k8s.io.ew/initial-deployment=true` label이 붙은 `MachineDeployment`가 정확히 1개이고 Available condition true |
| addon | 관련 `HelmReleaseProxy` 또는 `HelmRelease` Ready true |

### 8.2 성공 시 기록되는 annotation과 후속 상태

| 조건 | 기록 / 변경 |
| --- | --- |
| 모든 구성 요소 성공 | `cluster.x-k8s.io.ew/initial-deployment-completed=true` |
| 모든 구성 요소 성공 | `cluster-create-completed=true` |
| 모든 구성 요소 성공 | 모든 node group에 생성 완료 동기화 수행 |

### 8.3 실패 시 기록되는 annotation과 후속 상태

| 조건 | 기록 / 변경 |
| --- | --- |
| 허용 시간 안에 성공하지 못함 | `cluster.x-k8s.io.ew/initial-deployment-completed=false` |
| 허용 시간 안에 성공하지 못함 | `cluster-create-failed=true` |
| 허용 시간 안에 성공하지 못함 | 모든 `HelmRelease.spec.suspend=true` |
| 허용 시간 안에 성공하지 못함 | `cluster.x-k8s.io/paused=true` |
| 허용 시간 안에 성공하지 못함 | `cluster.x-k8s.io.ew/initial-deployment-paused=true` |

### 8.4 initial deployment 실패 후 event-processor 동작

| 조건 | 동작 |
| --- | --- |
| `cluster.x-k8s.io.ew/initial-deployment-paused=true` | control plane 삭제 요청 실행 |

## 9. NodeGroup 생성과 전개

### 9.1 전개 계층

```text
MachineDeployment
└── MachineSet
    └── Machine
        └── OpenStackMachine
```

### 9.2 전개 단계 판정

| 관찰 | 판단 |
| --- | --- |
| `MachineDeployment` 없음 | node group 선언 전 또는 삭제됨 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller 전개 전 |
| `MachineSet` 있음, `Machine` 없음 | replica 전개 전 |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO controller 전개 전 |
| `Machine.phase=Running`, `MachineNodeHealthy=True` | node 사용 가능 |

### 9.3 MachineDeployment 상태 조건

| 조건 | 의미 |
| --- | --- |
| `MachineDeployment.status.phase=ScalingUp`, desired replicas와 실제 replicas 다름 | 생성 또는 증설 진행 중 |
| `MachineDeployment.status.phase=ScalingUp`, desired replicas와 실제 replicas 같음 | 수량은 맞지만 안정화 전 |
| `MachineDeployment.status.phase=Running`, desired replicas < current replicas | 축소 진행 중 |
| `MachineDeployment.status.phase=Running`, desired replicas가 현재와 맞음 | 사용 가능 상태 |
| `MachineDeployment.status.phase=ScalingDown` | 축소 진행 중 |

### 9.4 생성 관련 annotation

| 조건 | annotation |
| --- | --- |
| 새 node group 생성 시작 | `last-created` 계열 annotation |
| 생성 완료 | `nodegroup-create-completed=true` |
| 생성 timeout 실패 | `nodegroup-create-failed=true` |
| 생성 timeout 실패 후 pause | `cluster.x-k8s.io.ew/nodegroup-paused=true`, `cluster.x-k8s.io/paused=true` |

## 10. NodeGroup scale

### 10.1 scale up

| 조건 | 의미 |
| --- | --- |
| desired replicas 증가 |
| `MachineDeployment.status.phase=ScalingUp` |
| 실제 replicas가 desired보다 적음 |

### 10.2 scale down

| 조건 | 의미 |
| --- | --- |
| desired replicas 감소 |
| `MachineDeployment.status.phase=ScalingDown` 또는 Running 상태에서 desired < current |

### 10.3 scale 관련 annotation

| 조건 | annotation |
| --- | --- |
| scale 작업 시작 | `last-scaled` 계열 annotation |
| scale 완료 | `nodegroup-scale-completed=true` |
| scale timeout 실패 | `nodegroup-scale-failed=true` |
| scale timeout 실패 후 pause | `cluster.x-k8s.io.ew/nodegroup-paused=true`, `cluster.x-k8s.io/paused=true` |

## 11. NodeGroup upgrade

### 11.1 upgrade 시작 판정

| 조건 | 의미 |
| --- | --- |
| 현재 `MachineDeployment` version과 다른 old `MachineSet`이 존재 |
| old `MachineSet` replicas가 1 이상 |
| watcher가 `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress=true` 기록 |

### 11.2 upgrade 완료 판정

| 조건 | 의미 |
| --- | --- |
| old `MachineSet` 없음 |
| `updatedReplicas`, `readyReplicas`, `availableReplicas`가 모두 desired와 같음 |
| watcher가 `nodegroup-upgrade-in-progress` annotation 제거 |
| 완료 시 `nodegroup-upgrade-completed=true` 기록 |

### 11.3 upgrade 실패

| 조건 | annotation |
| --- | --- |
| `last-upgraded` 기준 timeout | `nodegroup-upgrade-failed=true` |
| upgrade 실패 후 pause | `cluster.x-k8s.io.ew/nodegroup-paused=true`, `cluster.x-k8s.io/paused=true` |

## 12. Machine 상태와 삭제

### 12.1 provisioning

| Machine 조건 | 의미 |
| --- | --- |
| `InfrastructureReady=false` | VM 생성 중 |
| `InfrastructureReady=true`, 아직 Running 아님 | VM은 준비됐고 node 등록 중 |

### 12.2 available / unavailable

| Machine 조건 | 의미 |
| --- | --- |
| `phase=Running`, `MachineNodeHealthy=True` | 사용 가능 |
| `phase=Running`, `MachineNodeHealthy=False` | node unhealthy |
| `phase=Running`, healthy가 명확하지 않음 | 사용 불가 원인 불명 |

### 12.3 삭제 상태

| Machine 조건 | 의미 |
| --- | --- |
| cluster가 삭제 중 | VM 삭제 흐름 |
| `DrainingSucceeded=Unknown` 또는 `False` | node draining 중 |
| `VolumeDetachSucceeded=Unknown` 또는 `False` | volume detach 중 |
| `InfrastructureReady=Unknown` 또는 `True` | VM 삭제 중 |
| `MachineFinalizer` 없음 | machine 삭제 완료 |

### 12.4 node annotation

| 조건 | annotation |
| --- | --- |
| node 사용 가능, initial deployment 성공 이후 | `node-create-completed=true` |
| node 삭제 완료, initial deployment 성공 이후 | `node-delete-completed=true` |

## 13. Cluster suspend / resume

### 13.1 suspend 요청

| 조건 | annotation |
| --- | --- |
| suspend API 요청 성공 | `cluster.x-k8s.io.am/operation-state=suspend-requested` |

### 13.2 suspend 실행

| 단계 | Kubernetes 변화 |
| --- | --- |
| 1 | 모든 cluster 소속 `HelmRelease` values의 `replicaCount=0` |
| 2 | `KamajiControlPlane.spec.replicas=0` |
| 3 | 기존 `last-suspended-done`, `last-resumed-done`, `operation-state` 제거 |
| 4 | `operation-state=suspend-applied` 기록 |

### 13.3 suspend 완료

| 완료 조건 | 완료 시 기록 / 변경 |
| --- | --- |
| `KamajiControlPlane.status.readyReplicas=0` |
| 모든 component deployment의 `replicas=0`, `readyReplicas=0`, `updatedReplicas=0`, `unavailableReplicas=0` |
| 완료 시 모든 `HelmRelease.spec.suspend=true` |
| 완료 시 `cluster.x-k8s.io/paused=true` |
| 완료 시 `operation-state` 제거 |
| 완료 시 `cluster.x-k8s.io.ep/last-suspended-done=<timestamp>` |

### 13.4 resume 요청

| 조건 | annotation |
| --- | --- |
| resume API 요청 성공 | `cluster.x-k8s.io.am/operation-state=resume-requested` |

### 13.5 resume 실행

| 단계 | Kubernetes 변화 |
| --- | --- |
| 1 | 모든 `HelmRelease.spec.suspend=false` |
| 2 | cluster의 `cluster.x-k8s.io/paused` annotation 제거 |
| 3 | 모든 cluster 소속 `HelmRelease` values의 `replicaCount=1` |
| 4 | `KamajiControlPlane.spec.replicas=3` |
| 5 | 기존 완료 annotation과 `operation-state` 제거 |
| 6 | `operation-state=resume-applied` 기록 |

### 13.6 resume 완료

| 완료 조건 | 완료 시 기록 |
| --- | --- |
| `KamajiControlPlane.status.readyReplicas=3` |
| 모든 component deployment의 `replicas=1`, `readyReplicas=1`, `updatedReplicas=1`, `unavailableReplicas=0` |
| `operation-state` 제거 |
| `cluster.x-k8s.io.ep/last-resumed-done=<timestamp>` |

### 13.7 비정상 annotation 조합

| 조합 | 의미 |
| --- | --- |
| `operation-state` 없음 + `last-suspended-done`과 `last-resumed-done` 둘 다 존재 | 비정상 |
| `suspend-requested` + 이미 `last-suspended-done` 존재 | 비정상 |
| `resume-requested` + `last-suspended-done` 없음 | 비정상 |
| `suspend-applied` 또는 `resume-applied` + 완료 annotation 이미 존재 | 비정상 |

## 14. Service LoadBalancer

### 14.1 대상 조건

| 조건 |
| --- |
| workload cluster 내부 `Service` |
| `type=LoadBalancer` |
| `tenant.meta.x-k8s.io/id` annotation 존재 |
| `cluster.meta.x-k8s.io/name` annotation 존재 |

### 14.2 관련 annotation

| 용도 | annotation |
| --- | --- |
| resource ID | `cluster.x-k8s.io.ew/resource-id` |
| OpenStack LB ID | `service.beta.kubernetes.io/kinx-load-balancer-id` |

### 14.3 상태 판정

| 관찰 | 의미 |
| --- | --- |
| LB ID annotation 없음 | LB 생성 중 |
| OpenStack LB `PENDING_CREATE` | LB 생성 중 |
| OpenStack LB `PENDING_UPDATE` | LB 갱신 중 |
| OpenStack LB `ACTIVE` | LB 사용 가능 |
| OpenStack LB `ERROR` | LB 오류 |
| OpenStack LB `PENDING_DELETE` | LB 삭제 중 |
| `deletionTimestamp` 존재 | Service 삭제 중 |
| `service.kubernetes.io/load-balancer-cleanup` finalizer 없음 | Service 삭제 완료 |

### 14.4 후속 annotation

| 조건 | annotation |
| --- | --- |
| LB 생성 완료 | `resource-create-completed=true` |
| LB 갱신 완료 | `resource-update-completed=true` |
| LB 삭제 완료 | `resource-delete-completed=true` |

## 15. PersistentVolume

### 15.1 대상 조건

| 조건 |
| --- |
| workload cluster 내부 `PersistentVolume` |
| `spec.csi.driver=cinder.csi.openstack.org` 또는 `manila.csi.openstack.org` |

### 15.2 상태 판정

| 관찰 | 의미 |
| --- | --- |
| PV phase `Available` 또는 `Bound`, CSI volume handle 존재 | volume 생성 완료 |
| 위 조건 + 대응 `VolumeAttachment.status.attached=true` | volume attach 완료 |
| PV phase `Released` | volume detach 완료 |
| `deletionTimestamp` 존재 | 삭제 중 |
| `kubernetes.io/pv-protection` finalizer 없음 | 삭제 완료 |

### 15.3 후속 annotation

| 조건 | annotation |
| --- | --- |
| volume 생성 완료 | `resource-create-completed=true` |
| volume attach 완료 | `resource-attach-completed=true` |
| volume detach 완료 | `resource-detach-completed=true` |
| volume 삭제 완료 | `resource-delete-completed=true` |

## 16. Cluster 삭제

### 16.1 삭제 시작

| 관찰 | 의미 |
| --- | --- |
| `Cluster.metadata.deletionTimestamp` 존재 | cluster 삭제 시작 |
| 삭제 감지 후 watcher 동기화 | `cluster-delete-started=true` |

### 16.2 삭제 중 대기 조건

cluster 삭제 중 아래 리소스가 남아 있으면 삭제 완료로 보지 않는다.

| 남아 있는 리소스 |
| --- |
| `HelmRelease` |
| `HelmChartProxy` |
| cluster와 같은 이름의 `Ingress` |

### 16.3 삭제 완료 조건

| 조건 |
| --- |
| `HelmRelease` 없음 |
| `HelmChartProxy` 없음 |
| cluster와 같은 이름의 `Ingress` 없음 |
| CAPI `ClusterFinalizer` 없음 |

### 16.4 삭제 관련 annotation

| 조건 | annotation |
| --- | --- |
| 삭제 시작 | `cluster-delete-started=true` |
| 삭제 실패 | `cluster-delete-failed=true` |
| 삭제 완료 | `cluster-delete-completed=true` |

### 16.5 삭제 실패 시 Kubernetes 변화

| 조건 | 변경 |
| --- | --- |
| 삭제 timeout | cluster delete failed annotation 기록 |
| 삭제 timeout | cluster pause annotation 추가 |

### 16.6 event-processor가 삭제 중 수행하는 Kubernetes cleanup

| 조건 | 동작 |
| --- | --- |
| event-processor finalizer만 남음 | 외부 cleanup을 진행할 수 있는 단계 |
| cluster label `iksv2.kinx.net/cluster-name=<cluster-name>`을 가진 `ServiceGatewayClaim` 존재 | 모두 삭제 |
| cleanup 완료 | event-processor finalizer 제거 |

## 17. NodeGroup 삭제

| 관찰 | 의미 |
| --- | --- |
| `MachineDeployment.metadata.deletionTimestamp` 존재 | node group 삭제 시작 |
| 삭제 timeout | `nodegroup-delete-failed=true` |
| `MachineDeploymentFinalizer` 없음 | node group 삭제 완료 |
| 삭제 완료 | `nodegroup-delete-completed=true` |

## 18. controller 로그 확인 키워드

| 컴포넌트 | 검색 키워드 |
| --- | --- |
| event-watcher cluster | `Starting Reconcile for Cluster` |
| event-watcher initial deployment | `InitialDeploymentController` |
| event-watcher machine deployment | `Starting Reconcile for MachineDeployment` 또는 machine deployment controller 로그 |
| event-watcher machine | `Starting Machine reconciliation` |
| event-watcher soot service | `Reconciling Soot Service` |
| event-watcher soot pv | `Reconciling Soot PersistentVolume` |
| event-processor cluster | `Reconciling Cluster` |
| event-processor machine deployment | `Starting Reconcile for machineDeployment` |

## 19. Kubernetes-only 판정 원칙

1. 상위 리소스가 없으면 하위 리소스 부재를 별도 문제로 해석하지 않는다.
2. 상위 리소스가 있고 5분 뒤에도 하위 리소스가 없으면 해당 하위 리소스를 만드는 controller의 상태를 의심한다.
3. 완료 판정은 단일 `phase`보다 관련 condition과 완료 annotation을 함께 본다.
4. 삭제 완료 판정은 `deletionTimestamp`만으로 하지 않는다. 남은 하위 리소스와 finalizer 제거 여부를 함께 본다.
5. suspend/resume 판정은 `operation-state`, replica 실제값, done annotation을 함께 본다.
6. initial deployment 판정은 control plane, initial node group, addon, bastion을 함께 본다.

## 20. 추가 확인이 필요한 지점

1. `TenantControlPlane`, `OpenStackMachine`, `HelmReleaseProxy`는 외부 controller가 자동 생성한다. 하위 리소스가 생성되지 않을 때는 해당 외부 controller 로그가 추가로 필요하다.
2. workload cluster 내부 `Service`, `PersistentVolume` 판정은 soot 연결이 되어 있어야 완전하다. root cluster 정보만으로는 workload 내부 상태를 모두 볼 수 없다.
3. `ServiceGatewayClaim`은 MachineDeployment owner reference 기반 GC와 cluster label 기반 cleanup이 함께 존재한다. 실제 운영에서 어느 경로가 먼저 작동했는지는 리소스와 로그를 같이 봐야 한다.
