# IKS v2 장애 추적형 RAG 통합 가이드

이 문서는 IKS v2 Hosted Control Plane 환경에서 "무엇이 문제인가"를 검색하고 추적하기 위한 통합 RAG 문서다.
기존 `iksv2_v1.md`와 `iksv2_v2.md`의 리소스 관계, 상태 판정 기준, 삭제/생성/운영 시나리오를 하나의 진단 흐름으로 재구성했다.

목적은 특정 상태값 하나를 보고 바로 결론을 내리는 것이 아니라, 사용자가 말한 대상에서 출발해 현재 lifecycle, 관련 리소스 계층, 조건과 annotation의 불일치 지점을 찾아 문제 리소스 후보를 좁히는 것이다.

## 1. 검색 관점

### 1.1 이 문서를 검색해야 하는 요청

| 사용자 의도 | 검색 목적 |
| --- | --- |
| 클러스터가 왜 문제인지 묻는 요청 | `Cluster`의 현재 lifecycle과 하위 계층 중 막힌 지점 찾기 |
| 노드 그룹, worker group, MachineDeployment가 정상 동작하지 않는 요청 | `MachineDeployment -> MachineSet -> Machine -> OpenStackMachine -> Node` 계층 추적 |
| 생성이 멈춘 요청 | 상위 리소스 생성 여부, 자동 생성 리소스 부재, condition failureMessage 확인 |
| 삭제가 실패했거나 오래 걸리는 요청 | `deletionTimestamp`, pause, 잔존 child, finalizer, cleanup annotation 확인 |
| addon, control plane, infra, VM, LB, PV 문제 요청 | 해당 계층의 CRD status/conditions와 동기화 annotation 확인 |
| 과거 진단 후 "그럼 이건?", "아까 말한 노드 그룹은?" 같은 후속 요청 | 이전 primary target과 scope를 유지하고 operational focus만 관련 계층으로 전환 |

### 1.2 검색 전에 고정할 anchor

RAG 검색은 다음 anchor를 먼저 고정한 뒤 수행한다.

| Anchor | 의미 | 예시 |
| --- | --- | --- |
| `primary_kind` | 사용자가 직접 지정한 주 대상 리소스 kind | `Cluster`, `MachineDeployment`, `Service` |
| `primary_name` | 사용자가 지정한 이름 | `clst-...`, `today-new` |
| `namespace` | 명시 namespace 또는 이전 대화에서 이어받은 namespace | `35293abe-...` |
| `lifecycle` | 현재 작업 국면 | `creating`, `ready`, `deleting`, `scaling`, `upgrading`, `suspend`, `resume` |
| `operational_focus` | 사용자가 실제로 궁금해하는 운영 초점 | worker group, infra, addon, deletion blocker |

사용자가 "namespace의 `<uuid>` `<name>` 클러스터"처럼 표현하면 `<uuid>`는 먼저 namespace 후보로 해석한다. 다만 runtime에서 문자열을 하드코딩으로 변환하지 않고, requirement analysis가 namespace ambiguity를 명시해야 한다.

namespace가 비어 있거나 확실하지 않으면 positional object lookup에 `-A`를 붙이지 않는다. 이름으로 namespace를 찾을 때는 field selector를 사용한다.

```bash
kubectl get cluster -A --field-selector metadata.name="$CLUSTER"
```

확인된 namespace가 있으면 그때 namespaced object를 조회한다.

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
```

## 2. 장애 추적 기본 절차

### 2.1 첫 관측

첫 관측은 사용자가 지정한 primary resource의 `metadata`, `spec`, `status`, `conditions`를 확인하는 데 집중한다.

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
```

처음부터 Pod 목록, Node 목록, controller logs, events로 넓히지 않는다. primary resource가 CRD이면 CRD 자체의 spec/status가 현재 lifecycle과 다음 검색 방향을 정한다.

### 2.2 lifecycle 판정

첫 관측에서 다음 필드를 우선 확인한다.

| 필드 | 판정 |
| --- | --- |
| `metadata.deletionTimestamp` 존재 | 현재 삭제 중이다. 생성 시점 failureMessage보다 삭제 blocker를 우선 추적한다. |
| `metadata.annotations["cluster.x-k8s.io.ew/cluster-delete-failed"]` | 삭제 timeout 또는 cleanup 실패로 본다. |
| `metadata.annotations["cluster.x-k8s.io.ew/cluster-create-failed"]` | 생성 실패 동기화 기록이다. 삭제 중이면 과거 실패일 수 있다. |
| `metadata.annotations["cluster.x-k8s.io/paused"]` 또는 `cluster.x-k8s.io.ew/paused` | CAPI reconcile이 중단될 수 있다. 삭제 중이면 child 삭제가 멈출 수 있다. |
| `operation-state=suspend-requested/resume-requested` | suspend/resume 처리 시작 전 또는 processor 지연이다. |
| `operation-state=suspend-applied/resume-applied` | spec 변경은 적용됐고 replica 수렴을 기다리는 단계다. |
| `status.conditions` | 현재 readiness와 계층별 실패 신호를 확인한다. |

### 2.3 계층 추적

IKS v2 리소스는 상위 리소스가 하위 리소스를 자동 생성한다. 장애 추적은 계층을 따라가며 "처음으로 기대 상태와 어긋난 지점"을 찾는다.

```text
Cluster
  -> OpenStackCluster
  -> KamajiControlPlane
      -> TenantControlPlane
  -> HelmChartProxy
      -> HelmReleaseProxy
          -> HelmRelease
  -> MachineDeployment
      -> MachineSet
          -> Machine
              -> OpenStackMachine
              -> Node
```

자동 생성 child가 없을 때는 parent가 존재하는지, parent가 paused/deleting 상태인지, parent status가 실패를 보고하는지를 먼저 확인한다. child 부재 자체만으로 root cause를 확정하지 않는다.

### 2.4 문제 리소스 후보 선정

최종 보고서나 후속 조사 제안에서 "문제 리소스"는 증상을 표시한 primary resource가 아니라 실제 blocker 또는 추가 조사해야 할 관련 리소스를 우선한다.

| 상황 | 문제 리소스 후보 |
| --- | --- |
| `Cluster.WorkersAvailable=False` | `MachineDeployment`, `Machine`, `OpenStackMachine`, `Node` 중 처음 어긋난 계층 |
| `Cluster.deletionTimestamp` 있고 child 잔존 | 잔존 child 또는 finalizer를 가진 리소스 |
| `OpenStackCluster.failureMessage`가 현재 infra 생성 실패를 설명 | `OpenStackCluster` |
| `Machine.providerID` 없고 `OpenStackMachine.InstanceReady=False` | `OpenStackMachine` |
| `Machine.providerID` 있고 `nodeRef` 없음 | `Machine` 또는 join/network/bootstrap 계층 |
| addon pending/fail | `HelmChartProxy`, `HelmReleaseProxy`, `HelmRelease` 중 실패 reason이 있는 리소스 |
| Service LB cleanup stuck | `Service`와 LB cleanup finalizer |
| PV delete stuck | `PersistentVolume`, `VolumeAttachment`, CSI finalizer |

Cluster 자체는 증상 집계 리소스일 수 있다. Cluster의 pause, finalizer, deletionTimestamp 자체가 blocker일 때만 문제 리소스로 둔다.

## 3. 주요 컨트롤러와 관찰 위치

| 영역 | Namespace | 주요 리소스/컨트롤러 | 역할 |
| --- | --- | --- | --- |
| CAPI core | `capi-system` | `capi-controller-manager` | Cluster, MachineDeployment, MachineSet, Machine reconcile |
| CAPO | `capo-system` | `capo-controller-manager` | OpenStackCluster, OpenStackMachine, infra/VM reconcile |
| Bootstrap | `capi-kubeadm-bootstrap-system` | `capi-kubeadm-bootstrap-controller-manager` | KubeadmConfig, bootstrap secret 생성 |
| CAAPH | `caaph-system` | `caaph-controller-manager` | HelmChartProxy, HelmReleaseProxy, addon 배포 |
| Kamaji/CAPKH | `kamaji-system` | `capi-kamaji-controller-manager`, `kamaji` | KamajiControlPlane, TenantControlPlane |
| Service Gateway | `servicegateway` | `iksv2-servicegateway-controller-manager` | worker와 hosted control plane 연결 |
| IKS event sync | `dev-k8saas` | event-watcher, event-processor | 상태 동기화, annotation, cleanup |

로그는 primary 증거가 아니다. CRD의 spec/status/conditions와 annotation으로 막힌 계층을 좁힌 뒤, 해당 계층이 로그 없이는 해석되지 않을 때 사용자에게 직접 확인을 안내한다.

## 4. 상태 신호 사전

### 4.1 Cluster

| 신호 | 의미 |
| --- | --- |
| `status.conditions[InfrastructureReady]` | OpenStackCluster infra 준비 여부 |
| `status.conditions[ControlPlaneReady]` | KamajiControlPlane/TenantControlPlane 준비 여부 |
| `status.conditions[WorkersAvailable]` | worker replica availability 집계 |
| `status.conditions[Ready]` 또는 `Available` | 전체 Cluster availability 집계 |
| `status.failureReason`, `status.failureMessage` | CAPI 또는 provider가 보고한 실패 메시지. 현재 lifecycle과 맞춰 해석한다. |
| `metadata.finalizers` | 삭제 완료를 막는 controller cleanup 지점 |

### 4.2 OpenStackCluster

| 신호 | 의미 |
| --- | --- |
| `status.ready=true` | infra resource 생성 완료로 추론 |
| `status.failureReason`, `status.failureMessage` | OpenStack quota/capacity/network/security group/router/LB endpoint 실패 추론 |
| `status.network`, `status.bastion`, `status.apiServerLoadBalancer` | infra 구성 산출물 존재 여부 |
| 리소스 부재 | api-manager 생성 실패, 삭제 완료, 또는 parent/owner 문제를 추가 확인 |

OpenStack API를 직접 조회하지 않는 경우, OpenStack 문제는 `OpenStackCluster`와 `OpenStackMachine` status에서 추론한다.

### 4.3 KamajiControlPlane / TenantControlPlane

| 신호 | 의미 |
| --- | --- |
| `KamajiControlPlane.status.readyReplicas` | hosted control plane Pod replica 수렴 상태 |
| `KamajiControlPlane.status.conditions` | control plane provider 상태 |
| `TenantControlPlane.status.kubernetes.version.status` | tenant API server provisioning/readiness 상태 |
| `TenantControlPlane` 부재 | Kamaji provider 또는 KCP reconcile 지점 |

### 4.4 MachineDeployment / MachineSet

| 신호 | 의미 |
| --- | --- |
| `MachineDeployment.status.replicas` | desired 하위 replica 수 |
| `MachineDeployment.status.availableReplicas` | 사용 가능한 worker 수 |
| `MachineDeployment.status.phase` | ScalingUp, Running 등 CAPI 판정 |
| `MachineDeployment.metadata.annotations["cluster.x-k8s.io/paused"]` | node group reconcile 중단 |
| `MachineSet` 부재 | CAPI controller 또는 MD spec 문제 |

### 4.5 Machine / OpenStackMachine / Node

| 신호 | 의미 |
| --- | --- |
| `Machine.status.phase=Provisioned`, `spec.providerID` 없음 | VM provider object 또는 instance 생성 전/실패 |
| `Machine.spec.providerID` 있음, `status.nodeRef` 없음 | VM은 떴지만 Kubernetes Node join 실패 |
| `Machine.status.conditions[BootstrapReady]=False` | bootstrap data 생성 또는 kubeadm config 문제 |
| `Machine.status.conditions[InfrastructureReady]=False` | OpenStackMachine/infra 문제 |
| `Machine.status.conditions[NodeHealthy]=False` | Node condition 문제 또는 kubelet/node readiness 문제 |
| `OpenStackMachine.status.conditions[InstanceReady]=False` | VM 생성/삭제/상태 문제 |
| `OpenStackMachine.status.failureMessage` | CAPO가 보고한 OpenStack 실패 |
| Node 부재 | join 실패 또는 아직 등록 전. `providerID`, `nodeRef`, bootstrap 상태와 함께 본다. |

### 4.6 Addon

| 신호 | 의미 |
| --- | --- |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | CAAPH selector/value parsing/reconcile 문제 |
| `HelmReleaseProxy.status.conditions[HelmReleaseReady]` | addon release 상태 |
| reason `ValueParsingFailed` | values 템플릿 문제 |
| reason `ClusterSelectionFailed` | target cluster selector 문제 |
| reason `HelmReleasePending` | Helm release pending 또는 wait 블로킹 |
| reason `HelmInstallOrUpgradeFailed` | Helm install/upgrade 실패 |
| `HelmRelease.spec.suspend=true` | 실패 후 addon 정지 또는 suspend 흐름 |

### 4.7 Workload Service / PV

| 대상 | 신호 | 의미 |
| --- | --- | --- |
| Service LB | `spec.type=LoadBalancer` | LB resource 관찰 대상 |
| Service LB | `service.beta.kubernetes.io/kinx-load-balancer-id` 없음 | LB 생성 중 또는 아직 동기화 전 |
| Service LB | OpenStack provisioning `PENDING_CREATE/PENDING_UPDATE` | 진행 중 |
| Service LB | OpenStack provisioning `ERROR` | LB 오류 |
| Service delete | `service.kubernetes.io/load-balancer-cleanup` finalizer 남음 | LB cleanup 대기 |
| PV | `spec.csi.driver=cinder.csi.openstack.org` | Cinder volume |
| PV | `spec.csi.driver=manila.csi.openstack.org` | Manila volume |
| PV | `VolumeAttachment.status.attached=true` | attach 완료 |
| PV delete | `kubernetes.io/pv-protection` finalizer 남음 | PV protection/cleanup 대기 |

## 5. Annotation / Label 마커

### 5.1 Cluster lifecycle markers

| Key | 의미 |
| --- | --- |
| `cluster.x-k8s.io/cluster-name` | CAPI cluster linkage |
| `cluster.x-k8s.io.ew/cluster-id` | IKS cluster ID |
| `cluster.x-k8s.io.ew/cluster-sg-id` | cluster security group ID |
| `cluster.x-k8s.io/paused` | CAPI reconcile pause |
| `cluster.x-k8s.io.ew/paused` | IKS event flow pause |
| `cluster.x-k8s.io.ew/initial-deployment` | 최초 배포 대상 |
| `cluster.x-k8s.io.ew/initial-deployment-completed` | 최초 배포 완료/실패 판정 |
| `cluster.x-k8s.io.ew/initial-deployment-paused` | 최초 배포 실패 후 cleanup/pause 대상 |
| `cluster.x-k8s.io.ew/cluster-create-started` | 생성 시작 동기화 |
| `cluster.x-k8s.io.ew/cluster-create-completed` | 생성 완료 동기화 |
| `cluster.x-k8s.io.ew/cluster-create-failed` | 생성 실패 동기화 |
| `cluster.x-k8s.io.ew/cluster-delete-started` | 삭제 시작 동기화 |
| `cluster.x-k8s.io.ew/cluster-delete-completed` | 삭제 완료 동기화 |
| `cluster.x-k8s.io.ew/cluster-delete-failed` | 삭제 실패 동기화 |

### 5.2 Operation markers

| Key | 의미 |
| --- | --- |
| `cluster.x-k8s.io.am/operation-state=suspend-requested` | suspend 요청됨, 아직 적용 전 |
| `cluster.x-k8s.io.am/operation-state=suspend-applied` | suspend spec 적용, replica 수렴 대기 |
| `cluster.x-k8s.io.am/operation-state=resume-requested` | resume 요청됨, 아직 적용 전 |
| `cluster.x-k8s.io.am/operation-state=resume-applied` | resume spec 적용, replica 수렴 대기 |
| `cluster.x-k8s.io.ew/last-suspended-done` | suspend 완료 기록 |
| `cluster.x-k8s.io.ew/last-resumed-done` | resume 완료 기록 |

### 5.3 Node group markers

| Key | 의미 |
| --- | --- |
| `cluster.x-k8s.io.ew/nodegroup-id` | IKS node group ID |
| `cluster.x-k8s.io.ew/initial-deployment=true` | 최초 node group |
| `cluster.x-k8s.io.ew/nodegroup-paused` | node group pause |
| `cluster.x-k8s.io.ew/nodegroup-create-failed` | node group create timeout/failure |
| `cluster.x-k8s.io.ew/nodegroup-create-completed` | node group create 완료 |
| `cluster.x-k8s.io.ew/nodegroup-scale-completed` | scale 완료 |
| `cluster.x-k8s.io.ew/nodegroup-scale-failed` | scale 실패 |
| `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress` | upgrade 진행 중 |
| `cluster.x-k8s.io.ew/nodegroup-upgrade-completed` | upgrade 완료 |
| `cluster.x-k8s.io.ew/nodegroup-upgrade-failed` | upgrade 실패 |
| `cluster.x-k8s.io.ew/nodegroup-delete-completed` | delete 완료 |
| `cluster.x-k8s.io.ew/nodegroup-delete-failed` | delete 실패 |

## 6. 생성 문제 추적

### 6.1 Cluster 생성 stuck

검색 키워드:

- `cluster create stuck`
- `InfrastructureReady False`
- `ControlPlaneReady False`
- `OpenStackCluster failureMessage`
- `TenantControlPlane version.status`
- `initial-deployment`

추적 순서:

1. `Cluster` spec/status/conditions를 확인한다.
2. `OpenStackCluster`, `KamajiControlPlane`이 존재하는지 확인한다.
3. `OpenStackCluster.status.failureMessage`가 있으면 infra/OpenStack 계층으로 좁힌다.
4. `KamajiControlPlane`이 있고 `TenantControlPlane`이 없으면 Kamaji provider 계층을 본다.
5. control plane ready 이후 addon과 최초 node group을 본다.

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
```

빠른 판정:

| 관찰 | 의심 지점 |
| --- | --- |
| `OpenStackCluster` 없음 | api-manager 생성 단계 또는 Cluster spec/owner 문제 |
| `InfrastructureReady=False` | CAPO/OpenStack infra |
| `OpenStackCluster.failureMessage` quota/capacity | OpenStack quota/capacity |
| `ControlPlaneReady=False` | Kamaji/TenantControlPlane |
| `TenantControlPlane` 없음 | Kamaji provider |
| `TenantControlPlane.version.status=Provisioning/NotReady` | tenant API server provisioning |

### 6.2 최초 배포 실패

최초 배포는 별도 리소스가 아니라 annotation과 자동 생성된 addon/node group 조합으로 판정한다.

확인 대상:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get helmchartproxy,helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

| 관찰 | 의미 |
| --- | --- |
| `initial-deployment-completed=false` | watcher가 최초 배포 실패를 기록 |
| `cluster-create-failed=true` | 생성 실패가 동기화됨 |
| `initial-deployment-paused=true` | 실패 후 cleanup 대상 |
| `HelmRelease.spec.suspend=true` | addon 정지 |
| `cluster.x-k8s.io/paused=true` | CAPI reconcile 정지 |

### 6.3 OpenStack infra 실패

OpenStackCluster 또는 OpenStackMachine의 failureMessage를 해석한다.

| 메시지 패턴 | 해석 |
| --- | --- |
| `Quota exceeded` | quota 부족. 생성 중이면 root cause 후보, 삭제 중이면 과거 생성 실패일 수 있음 |
| `No valid host` | compute capacity 또는 flavor/image/host 제약 |
| `Security group` | security group 생성/규칙/quota 문제 |
| `Port` 또는 `Network` | Neutron port/network/subnet 문제 |
| `LoadBalancer` 또는 `Octavia` | API endpoint/LB provisioning 문제 |
| `Unauthorized`/`Forbidden` | cloud credential 권한 문제 |

삭제 중인 Cluster에서 오래된 quota failureMessage만 보고 삭제 실패 원인으로 확정하지 않는다. `deletionTimestamp`가 있으면 삭제 blocker를 먼저 찾는다.

## 7. Control Plane 문제 추적

검색 키워드:

- `KamajiControlPlane`
- `TenantControlPlane`
- `ControlPlaneReady False`
- `version.status`
- `readyReplicas`

추적 순서:

1. `Cluster.status.conditions[ControlPlaneReady]` 확인.
2. `KamajiControlPlane` 존재와 `status.readyReplicas` 확인.
3. `TenantControlPlane` 존재와 `status.kubernetes.version.status` 확인.
4. control plane endpoint, kubeconfig secret, servicegateway 연결 여부를 관련 증거로 확인.

```bash
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get secret "$CLUSTER-admin-kubeconfig" -o yaml
```

| 관찰 | 의심 지점 |
| --- | --- |
| KCP 있음, TCP 없음 | Kamaji provider reconcile |
| KCP `readyReplicas=0` | control plane pod 미기동 |
| TCP `version.status=NotReady` | tenant API server provisioning |
| kubeconfig secret 없음 | control plane credential 생성 문제 |

## 8. Node group / Worker 문제 추적

`노드 그룹`, `worker group`, `node group`은 Kubernetes/CAPI 운영 문맥에서 `MachineDeployment` 계층과 관련될 수 있다. runtime에서 문자열을 특정 kind로 강제 치환하지 말고, 이전 Cluster 문맥과 live evidence로 구체화한다.

검색 키워드:

- `node group not ready`
- `WorkersAvailable False`
- `MachineDeployment availableReplicas 0`
- `Machine providerID nodeRef`
- `OpenStackMachine InstanceReady`
- `MachineNodeHealthy`

### 8.1 MachineDeployment에서 시작

```bash
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get openstackmachine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

| 관찰 | 의심 지점 |
| --- | --- |
| `MachineDeployment` 없음 | api-manager node group 생성 단계 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller |
| `MachineSet` 있음, `Machine` 없음 | MachineSet replica 전개 |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO infra object 생성 |
| `OpenStackMachine.InstanceReady=False` | VM 생성 실패 |
| `Machine.providerID` 있음, `nodeRef` 없음 | VM은 떴지만 node join 실패 |
| `nodeRef` 있음, `NodeHealthy=False` | Node condition/CNI/kubelet 문제 |

### 8.2 생성/scale/upgrade 구분

| 상황 | 주요 marker |
| --- | --- |
| node group create | `nodegroup-create-*`, `initial-deployment=true` |
| scale | `MachineDeployment.spec.replicas`, `nodegroup-scale-*` |
| upgrade | `nodegroup-upgrade-in-progress`, rollout annotation, Machine replacement |
| delete | `MachineDeployment.deletionTimestamp`, `nodegroup-delete-*`, finalizer |

scale 문제는 desired replicas와 actual/available replicas의 차이를 본다.

```bash
kubectl -n "$NS" get machinedeployment "$MD" -o yaml
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/deployment-name="$MD" -o yaml
```

upgrade 문제는 새 MachineSet 생성 여부, old Machine drain/delete, OpenStackMachine 생성/삭제 실패를 함께 본다.

### 8.3 Join 실패와 Node 상태

| 관찰 | 해석 |
| --- | --- |
| `providerID` 없음 | VM 생성 전/실패 |
| `providerID` 있음 | VM은 생성됨 |
| `nodeRef` 없음 | Kubernetes Node 등록 실패 |
| `BootstrapReady=False` | bootstrap secret/kubeadm config |
| `BootstrapReady=True`, `nodeRef` 없음 | endpoint/network/security group/servicegateway/join 실패 |
| `nodeRef` 있음, Node NotReady | CNI, kubelet, node condition |

Node OS 로그는 management cluster의 일반 리소스 관측이 아니다. 위 CRD 증거로 join 실패를 좁힌 뒤 필요한 경우 사용자에게 VM 내부 `cloud-init`, `kubelet`, `kubeadm join` 확인을 요청한다.

## 9. Addon 문제 추적

검색 키워드:

- `addon pending`
- `HelmChartProxy`
- `HelmReleaseProxy`
- `HelmReleaseReady`
- `ValueParsingFailed`
- `ClusterSelectionFailed`
- `HelmInstallOrUpgradeFailed`

```bash
kubectl -n "$NS" get helmchartproxy,helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get helmrelease -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

| 관찰 | 의심 지점 |
| --- | --- |
| `HelmChartProxy` 없음 | api-manager addon 생성 단계 |
| HCP 있음, HRP 없음 | CAAPH controller, selector, values |
| HRP `ValueParsingFailed` | values template |
| HRP `ClusterSelectionFailed` | cluster selector |
| HRP `HelmReleasePending` | wait/atomic/pending release |
| HRP `HelmInstallOrUpgradeFailed` | chart install/upgrade 실패 |
| `ClusterAvailable=False` | target cluster API 접근 불가 |

## 10. Suspend / Resume 문제 추적

검색 키워드:

- `operation-state suspend-requested`
- `operation-state suspend-applied`
- `operation-state resume-requested`
- `operation-state resume-applied`
- `last-suspended-done`
- `last-resumed-done`

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

| 관찰 | 의심 지점 |
| --- | --- |
| `suspend-requested` 유지 | event-processor 미처리 |
| `suspend-applied`, KCP `readyReplicas != 0` | control plane scale down 지연 |
| `suspend-applied`, addon/worker replicas 남음 | component scale down 지연 |
| `resume-requested` 유지 | event-processor 미처리 |
| `resume-applied`, KCP `readyReplicas != 3` | control plane scale up 지연 |
| `resume-applied`, addon/worker readyReplicas 부족 | component 복구 지연 |

## 11. 삭제 문제 추적

삭제 진단은 생성 진단과 다르게 진행한다. `deletionTimestamp`가 있으면 현재 문제는 삭제 lifecycle이다. 과거 `failureMessage`, `cluster-create-failed`, quota error가 남아 있어도 삭제 blocker를 먼저 찾는다.

검색 키워드:

- `cluster delete stuck`
- `deletionTimestamp finalizers`
- `cluster-delete-failed`
- `paused deleting`
- `Machine InstanceDeleteFailed`
- `OpenStackMachine InstanceDeleteFailed`
- `HelmRelease finalizer`
- `ServiceGatewayClaim cleanup`

### 11.1 Cluster 삭제 순서

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get helmrelease,helmchartproxy,helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get ingress "$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment,machineset,machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get openstackmachine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get secret "$CLUSTER-cloud-conf" "$CLUSTER-ccm-cloud-config" "$CLUSTER-admin-kubeconfig" -o yaml
kubectl -n "$NS" get servicegatewayclaim -l iksv2.kinx.net/cluster-name="$CLUSTER" -o yaml
```

삭제 완료 조건은 `Cluster`가 deleting인지만 보지 않는다.

| 관찰 | 의심 지점 |
| --- | --- |
| `cluster.x-k8s.io/paused=true` + `deletionTimestamp` | CAPI reconcile 중단. 하위 삭제 미시작 가능 |
| `HelmRelease` 남음 | addon cleanup 대기 |
| `HelmChartProxy`/`HelmReleaseProxy` 남음 | CAAPH cleanup 대기 |
| 같은 이름 `Ingress` 남음 | watcher가 삭제 완료로 보지 않음 |
| `MachineDeployment`/`MachineSet` 남음 | worker 계층 cleanup 대기 |
| `Machine` 남음 | drain, volume detach, node cleanup, infra delete 대기 |
| `OpenStackMachine` `InstanceDeleteFailed` | VM 삭제 실패 |
| `TenantControlPlane` 남음 | Kamaji cleanup 대기 |
| `OpenStackCluster` 남음 | CAPO infra cleanup 대기 |
| CAPI cluster finalizer 남음 | CAPI cleanup 대기 |
| event-processor 외 finalizer 남음 | external cleanup 시작 전 |
| event-processor finalizer만 남음 | credential, SGW claim, 외부 resource cleanup 중 |
| `cluster-delete-failed=true` | watcher가 삭제 timeout 실패 기록 |

### 11.2 삭제 중 과거 생성 오류와 현재 삭제 오류 구분

| 증거 | 해석 기준 |
| --- | --- |
| `deletionTimestamp` 없음 + quota failureMessage | 생성 실패 가능성이 높다. |
| `deletionTimestamp` 있음 + quota failureMessage | 과거 생성 실패가 남아 있을 수 있다. 삭제 blocker를 우선 찾는다. |
| `cluster-delete-failed=true` | 현재 삭제 실패 신호다. |
| child 리소스 잔존 + finalizer 잔존 | 현재 삭제 blocker다. |
| OpenStackMachine `InstanceDeleteFailed` | 현재 삭제 실패 신호다. |
| `cloud-conf` secret 또는 SGW claim 잔존 | event-processor cleanup blocker 가능성 |

예시 판정:

- "Cluster는 Deleting이고 security_group quota exceeded가 보임"만으로 삭제 실패 원인을 quota로 확정하지 않는다.
- 같은 시점에 `OpenStackMachine`이 삭제 실패 상태인지, `Machine` finalizer가 남았는지, `OpenStackCluster`가 삭제 중인지 확인한다.
- 삭제 failure marker나 잔존 child가 없으면 "과거 생성 실패 메시지가 남아 있으나 현재 삭제 blocker는 아직 확인되지 않음"으로 보고 추가 관측을 제안한다.

### 11.3 Machine 삭제 stuck

```bash
kubectl -n "$NS" get machine "$MACHINE" -o yaml
kubectl -n "$NS" get openstackmachine "$MACHINE" -o yaml
```

| 관찰 | 의심 지점 |
| --- | --- |
| `Machine.deletionTimestamp` 존재 | 삭제 진행 |
| `DrainingSucceeded=False` | pod drain 지연 |
| `VolumeDetachSucceeded=False` | volume detach 지연 |
| `MachineFinalizer` 남음 | CAPI cleanup 대기 |
| `OpenStackMachine.InstanceReady=False` reason `InstanceDeleteFailed` | VM 삭제 실패 |

### 11.4 NodeGroup 삭제 stuck

| 관찰 | 의심 지점 |
| --- | --- |
| `MachineDeployment.deletionTimestamp` 존재 | 삭제 진행 |
| `nodegroup-delete-failed=true` | timeout 실패 |
| `MachineSet`/`Machine` 남음 | 하위 worker 삭제 대기 |
| `MachineDeploymentFinalizer` 제거됨 | node group 삭제 완료 |

## 12. Service LoadBalancer / PV 문제 추적

### 12.1 Service LoadBalancer

```bash
kubectl -n "$WORKLOAD_NS" get service "$SERVICE" -o yaml
```

| 관찰 | 판단 |
| --- | --- |
| tenant/cluster annotation 존재 | root tenant/cluster와 연결 |
| `spec.type=LoadBalancer` | LB 대상 |
| LB ID 없음 | LB 생성 중 또는 동기화 전 |
| provisioning `PENDING_CREATE` | 생성 중 |
| provisioning `PENDING_UPDATE` | 갱신 중 |
| provisioning `ACTIVE` | 생성/갱신 완료 |
| provisioning `ERROR` | LB 오류 |
| `deletionTimestamp` + LB cleanup finalizer | 삭제 cleanup 대기 |
| LB not found + cleanup finalizer 제거 | 삭제 완료로 동기화 가능 |

### 12.2 PersistentVolume

```bash
kubectl get persistentvolume "$PV" -o yaml
kubectl get volumeattachment -o yaml
```

| 관찰 | 판단 |
| --- | --- |
| CSI driver `cinder.csi.openstack.org` | Cinder PV |
| CSI driver `manila.csi.openstack.org` | Manila PV |
| phase `Available`/`Bound` + volumeHandle | volume 생성됨 |
| `VolumeAttachment.status.attached=true` | attach 완료 |
| phase `Released` | detach 완료 후보 |
| delete 중 `pv-protection` finalizer 남음 | PV protection cleanup 대기 |
| reclaim policy `Retain` | OpenStack metadata cleanup이 별도로 필요할 수 있음 |

## 13. 빠른 검색 인덱스

| 증상/질문 | 검색 키워드 | 먼저 볼 리소스 | 다음 계층 |
| --- | --- | --- | --- |
| 클러스터가 문제 | `Cluster conditions lifecycle` | `Cluster` | OSC/KCP/MD |
| 생성 실패 | `cluster-create-failed failureMessage` | `Cluster` | `OpenStackCluster`, `KamajiControlPlane` |
| 삭제 실패 | `deletionTimestamp finalizers cluster-delete-failed` | `Cluster` | addons, machines, KCP, OSC, secrets, SGW |
| worker 없음 | `WorkersAvailable MachineDeployment availableReplicas` | `Cluster`, `MachineDeployment` | MachineSet/Machine/OSM/Node |
| 노드 join 실패 | `providerID nodeRef BootstrapReady` | `Machine` | OpenStackMachine, Node |
| VM 생성 실패 | `InstanceReady OpenStackMachine failureMessage` | `OpenStackMachine` | OpenStackCluster |
| control plane 문제 | `ControlPlaneReady TenantControlPlane version.status` | `KamajiControlPlane` | TenantControlPlane |
| addon 문제 | `HelmReleaseReady HelmReleasePending` | `HelmReleaseProxy` | HelmRelease |
| suspend 문제 | `operation-state suspend` | `Cluster` | KCP, addon, MD |
| resume 문제 | `operation-state resume` | `Cluster` | KCP, addon, MD |
| LB 문제 | `LoadBalancer resource-id provisioning` | `Service` | LB cleanup |
| PV 문제 | `cinder manila VolumeAttachment finalizer` | `PersistentVolume` | VolumeAttachment |

## 14. 최종 응답 작성 기준

### 14.1 결론 강도

| 결론 상태 | 조건 |
| --- | --- |
| 결론 도출 | 현재 lifecycle에 맞는 직접 실패 신호와 blocker 리소스가 확인됨 |
| 가능성 높음 | status/condition이 한 계층을 강하게 지목하지만 직접 failureMessage는 없음 |
| 추가 진단 필요 | 증상은 확인됐지만 blocker 계층이 아직 확인되지 않음 |
| 불명확 | primary resource조차 확인되지 않거나 namespace/scope가 모호함 |

### 14.2 보고서에 포함할 항목

- 현재 lifecycle: creating/running/deleting/scaling/upgrading/suspend/resume
- 확인한 primary resource와 namespace
- 관측한 핵심 condition/annotation/finalizer
- 증상 리소스와 blocker 후보 리소스 구분
- 추가 조사할 리소스가 있으면 kind/name/namespace를 명시
- 오래된 failureMessage와 현재 lifecycle이 충돌하면 그 차이를 명시

### 14.3 잘못된 결론을 피하는 규칙

1. `WorkersAvailable=False`만으로 worker root cause를 확정하지 않는다. MachineDeployment 이하 계층을 봐야 한다.
2. `OpenStackCluster` 부재만으로 infra 삭제 완료나 생성 실패를 확정하지 않는다. Cluster lifecycle과 deletionTimestamp를 같이 본다.
3. 삭제 중에는 과거 생성 failureMessage를 현재 삭제 원인으로 단정하지 않는다.
4. Node 상태나 logs는 primary CRD 관측 이후 필요한 경우에만 확장한다.
5. 문제 리소스는 증상 집계 리소스가 아니라, 실제 막힌 계층의 리소스를 우선한다.
6. controller logs는 마지막 보조 증거다. 실행했다고 말하지 말고, 필요하면 사용자에게 직접 확인 명령을 안내한다.

## 15. 로그 확인이 필요한 경우

CRD 상태만으로 controller가 왜 child를 만들지 않았는지 해석되지 않을 때 로그 확인을 안내한다.

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher --since=30m | grep "$CLUSTER"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor --since=30m | grep "$CLUSTER" | grep -i error
```

계층별 controller 로그:

| 막힌 계층 | 확인 대상 |
| --- | --- |
| OpenStackCluster/OpenStackMachine | `capo-system/deploy/capo-controller-manager` |
| MachineDeployment/MachineSet/Machine | `capi-system/deploy/capi-controller-manager` |
| BootstrapReady/KubeadmConfig | `capi-kubeadm-bootstrap-system/deploy/capi-kubeadm-bootstrap-controller-manager` |
| KamajiControlPlane/TenantControlPlane | `kamaji-system/deploy/capi-kamaji-controller-manager`, `kamaji-system/deploy/kamaji` |
| HelmChartProxy/HelmReleaseProxy | `caaph-system/deploy/caaph-controller-manager` |
| ServiceGatewayClaim | `servicegateway/deploy/iksv2-servicegateway-controller-manager` |

로그 해석 원칙:

- cluster name 또는 cluster id로 먼저 좁힌다.
- error 문자열만 검색하면 다른 cluster 이벤트와 섞일 수 있다.
- watcher는 관찰/동기화, processor는 cleanup/외부 조치를 주로 본다.
- 로그는 원인 확정용 보조 증거이며, CRD status/condition과 모순되면 CRD lifecycle을 먼저 설명한다.

## 16. RAG retrieval query 작성 예시

RAG 검색 쿼리는 사용자의 표현을 그대로 넣는 것보다 lifecycle과 계층을 함께 넣으면 정확도가 높다.

| 상황 | 좋은 검색 쿼리 |
| --- | --- |
| Cluster 문제 일반 | `IKS v2 Cluster conditions InfrastructureReady ControlPlaneReady WorkersAvailable problem tracing` |
| deletionTimestamp 존재 | `IKS v2 Cluster deletionTimestamp finalizers cluster-delete-failed child resources cleanup` |
| worker unavailable | `IKS v2 WorkersAvailable False MachineDeployment Machine OpenStackMachine nodeRef providerID` |
| OpenStack quota | `IKS v2 OpenStackCluster failureMessage Quota exceeded creation versus deletion` |
| control plane | `IKS v2 KamajiControlPlane TenantControlPlane ControlPlaneReady version.status` |
| addon | `IKS v2 HelmChartProxy HelmReleaseProxy HelmReleaseReady ValueParsingFailed HelmReleasePending` |
| suspend/resume | `IKS v2 operation-state suspend-applied resume-applied readyReplicas` |

후속 요청에서는 이전 primary target을 유지하고 operational focus를 쿼리에 추가한다.

예:

```text
previous target: Cluster/clst-a namespace ns-a
user focus: node group not working
query: IKS v2 Cluster worker group MachineDeployment WorkersAvailable False node group tracing
```

## 17. 요약 원칙

1. primary CRD의 spec/status/conditions를 첫 관측으로 삼는다.
2. annotation/label로 시스템이 마킹한 lifecycle을 확인한다.
3. 현재 lifecycle에 맞는 계층만 따라간다.
4. 자동 생성 리소스 부재는 parent 상태와 controller 담당 영역으로 해석한다.
5. 삭제는 deletionTimestamp, pause, 잔존 child, finalizer, delete marker를 함께 본다.
6. 생성 failureMessage와 삭제 failure를 섞지 않는다.
7. 문제 리소스는 symptom holder와 blocker를 구분해서 제시한다.
8. 로그와 이벤트는 CRD 관측 이후 필요한 경우에만 보조 증거로 사용한다.
