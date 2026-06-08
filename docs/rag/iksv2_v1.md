# IKS v2 Kubernetes 상태 판정 종합 문서

## 0. 문서 범위

이 문서는 IKS v2 운영 상태를 Kubernetes 리소스에 남는 값만으로 판단하기 위한 RAG 문서다.

사용하는 정보:

- Kubernetes 리소스의 `metadata.annotations`
- Kubernetes 리소스의 `metadata.labels`
- Kubernetes 리소스의 `metadata.ownerReferences`
- Kubernetes 리소스의 `metadata.finalizers`
- Kubernetes 리소스의 `metadata.deletionTimestamp`
- Kubernetes 리소스의 `spec`
- Kubernetes 리소스의 `status`
- Kubernetes 리소스의 `conditions`
- controller 로그
- OpenStack LoadBalancer provisioning status

사용하지 않는 정보:

- 서비스 내부 저장소 값
- 서비스 내부 상태 코드
- 서비스 내부 상태 전이 코드

근거 문서:

- `common.md`
- `AM.md`
- `EW.md`
- `EP.md`

## 1. 빠른 검색 인덱스

| 검색 주제 | 관련 리소스 | 핵심 확인값 |
| --- | --- | --- |
| controller 구성 | controller deployment | namespace, controller 이름, 담당 리소스 |
| cluster 생성 | `OpenStackCluster`, `KamajiControlPlane`, `Cluster` | 상위 리소스 존재, `spec`, `conditions` |
| hosted control plane | `KamajiControlPlane`, `TenantControlPlane` | `spec.replicas`, `status.readyReplicas`, 하위 리소스 생성 |
| addon 배포 | `HelmRelease`, `HelmChartProxy`, `HelmReleaseProxy` | Ready condition, `spec.suspend`, 자동 생성 관계 |
| 최초 배포 단계 | `Cluster`, `Ingress`, 최초 `MachineDeployment`, `HelmRelease`, `HelmReleaseProxy` | `initial-deployment-completed`, `cluster-create-*` annotation |
| node group 생성 | `MachineDeployment`, `MachineSet`, `Machine`, `OpenStackMachine` | `status.phase`, replicas, 하위 리소스 생성 |
| node group scale | `MachineDeployment` | `spec.replicas`, `status.phase`, `nodegroup-scale-*` |
| node group upgrade | `MachineDeployment`, `MachineSet` | `nodegroup-upgrade-in-progress`, old/new MachineSet |
| machine 상태 | `Machine`, `OpenStackMachine` | `phase`, `InfrastructureReady`, `MachineNodeHealthy` |
| machine 삭제 | `Machine` | `DrainingSucceeded`, `VolumeDetachSucceeded`, finalizer |
| suspend | `Cluster`, `KamajiControlPlane`, `HelmRelease`, `Deployment` | `operation-state`, replicas 0, `last-suspended-done` |
| resume | `Cluster`, `KamajiControlPlane`, `HelmRelease`, `Deployment` | `operation-state`, replicas 복구, `last-resumed-done` |
| Service LB | workload `Service`, OpenStack LB | LB ID annotation, OpenStack provisioning status |
| PersistentVolume | workload `PersistentVolume`, `VolumeAttachment`, `PersistentVolumeClaim` | CSI driver, PV phase, attach 상태 |
| cluster 삭제 | `Cluster`, addon resources, `Ingress`, `ServiceGatewayClaim`, `Secret` | deletionTimestamp, finalizers, cleanup annotations |

## 2. Controller 구성

| Namespace | Controller | 역할 | 주요 리소스 | 확인 포인트 |
| --- | --- | --- | --- | --- |
| `caaph-system` | `caaph-controller-manager` | Helm 기반 addon 배포 관리 | `HelmChartProxy`, `HelmReleaseProxy`, `Cluster` | `HelmReleaseProxy` 생성 여부, addon Ready |
| `capi-kubeadm-bootstrap-system` | `capi-kubeadm-bootstrap-controller-manager` | Machine bootstrap data 생성 | `KubeadmConfigTemplate`, `KubeadmConfig`, `Secret`, `Machine` | `KubeadmConfig.status.dataSecretName`, bootstrap Secret |
| `capi-system` | `capi-controller-manager` | Cluster API 핵심 lifecycle | `Cluster`, `MachineDeployment`, `MachineSet`, `Machine`, `MachineHealthCheck` | `Cluster.status`, `MachineDeployment.status`, `Machine.status` |
| `capo-system` | `capo-controller-manager` | OpenStack 인프라 연동 | `OpenStackCluster`, `OpenStackMachineTemplate`, `OpenStackMachine`, `Machine` | VM, port, network, security group 상태 |
| `kamaji-system` | `capi-kamaji-controller-manager` | Kamaji를 CAPI control plane provider로 연결 | `KamajiControlPlane`, `TenantControlPlane`, `Cluster` | `KamajiControlPlane.status`, `TenantControlPlane` 생성 |
| `kamaji-system` | `kamaji` | Hosted control plane 실행 | `TenantControlPlane`, control plane pods, `Secret`, `Service`, `ServiceAccount` | control plane pod, API server service |
| `servicegateway` | `iksv2-servicegateway-controller-manager` | control plane network와 worker network 연결 | `KinxServiceGateway`, `ServiceGatewayClaim`, OpenStack network/router/static route | gateway 상태, route 상태 |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-watcher` | Kubernetes 리소스 상태 감시와 annotation 동기화 | `Cluster`, `MachineDeployment`, `Machine`, workload `Service`, `PersistentVolume` | status, conditions, labels, annotations |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-processor` | lifecycle 조치 실행 | `Cluster`, `KamajiControlPlane`, `HelmRelease`, `MachineDeployment`, `ServiceGatewayClaim` | operation-state, cleanup, finalizer |

## 3. 컴포넌트별 역할

| 컴포넌트 | Kubernetes 관점의 역할 |
| --- | --- |
| `api-manager` | API 요청 결과로 상위 Kubernetes 선언 리소스를 만들고, 필요한 label/annotation/spec을 기록한다 |
| `event-watcher` | Kubernetes 리소스 상태를 관찰하고 완료/실패/삭제 동기화 annotation을 기록한다 |
| `event-processor` | annotation과 readiness 상태를 보고 suspend/resume, cleanup, remote setup을 실제로 수행한다 |
| 외부 controller | 상위 리소스를 보고 하위 리소스를 자동 생성하거나 실제 인프라 상태를 수렴시킨다 |

### 3.1 최초 배포 단계의 의미

`최초 배포 단계`는 Kubernetes 리소스 이름이 아니다. 별도 객체를 조회하거나 삭제하는 흐름은 없으며, Cluster 생성 직후 여러 리소스가 준비되는지를 묶어서 판단하는 운영상 개념이다.

이 단계는 다음 Kubernetes metadata로만 관찰한다.

| 기록 위치 | key / 값 | 의미 |
| --- | --- | --- |
| `MachineDeployment.metadata.labels` | `cluster.x-k8s.io.ew/initial-deployment=true` | 최초 배포 단계에 포함되는 최초 node group 식별 marker |
| `Cluster.metadata.annotations` | `cluster.x-k8s.io.ew/initial-deployment-completed=true|false` | 최초 배포 단계 완료 또는 실패 marker |
| `Cluster.metadata.annotations` | `cluster.x-k8s.io.ew/initial-deployment-paused=true` | 최초 배포 실패 후 cleanup 대상 marker |
| `Cluster.metadata.annotations` | `cluster.x-k8s.io.ew/cluster-create-started=true` | 최초 배포 단계 시작 동기화 marker |
| `Cluster.metadata.annotations` | `cluster.x-k8s.io.ew/cluster-create-completed=true` | 최초 배포 단계 성공 동기화 marker |
| `Cluster.metadata.annotations` | `cluster.x-k8s.io.ew/cluster-create-failed=true` | 최초 배포 단계 실패 동기화 marker |

대응하는 `*-timestamp` annotation이 있으면 timestamp는 위 marker가 기록된 시각으로 해석한다.

## 4. 자동 생성 리소스 관계

| 상위 리소스 | 자동 생성 하위 리소스 | 생성 주체 | 없을 때 판단 |
| --- | --- | --- | --- |
| `KamajiControlPlane` | `TenantControlPlane` | Kamaji controller | Kamaji controller 생성 대기 또는 실패 |
| `MachineDeployment` | `MachineSet` | CAPI controller | CAPI 전개 대기 또는 실패 |
| `MachineSet` | `Machine` | CAPI controller | replica 전개 대기 또는 실패 |
| `Machine` | `OpenStackMachine` | CAPO controller | OpenStackMachine 생성 대기 또는 실패 |
| `HelmChartProxy` | `HelmReleaseProxy` | CAAH / Helm addon controller | addon 전개 대기 또는 실패 |

판정 원칙:

- 상위 리소스가 없으면 하위 리소스 부재를 별도 장애로 판단하지 않는다.
- 상위 리소스가 있고 하위 리소스가 없으면 해당 하위 리소스를 만드는 controller 로그를 확인한다.
- 하위 리소스가 생성된 뒤에는 하위 리소스의 `status`, `conditions`, `finalizers`를 기준으로 다음 단계를 판단한다.

## 5. 리소스 계층

```text
Namespace(<tenant-id>)
└── Cluster
    ├── OpenStackCluster
    ├── KamajiControlPlane
    │   └── TenantControlPlane
    ├── Secret
    │   ├── <cluster-name>-cloud-conf
    │   └── <cluster-name>-clouds-secret
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
        ├── KubeadmConfigTemplate
        ├── OpenStackMachineTemplate
        ├── MachineHealthCheck
        └── MachineSet
            └── Machine
                └── OpenStackMachine
```

workload cluster 내부 리소스:

```text
Service(type=LoadBalancer)
└── OpenStack LoadBalancer

PersistentVolume(Cinder/Manila CSI)
├── PersistentVolumeClaim
└── VolumeAttachment
```

service gateway 리소스:

```text
MachineDeployment
└── ServiceGatewayClaim
    └── KinxServiceGateway
```

## 6. 공통 조회 명령

기본 변수:

```bash
NS=<tenant-id>
CLUSTER=<cluster-name>
MD=<machine-deployment-name>
```

management cluster 리소스 조회:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get kamajicontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" -o yaml
kubectl -n "$NS" get secret "$CLUSTER-cloud-conf" -o yaml
kubectl -n "$NS" get helmrelease -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get helmchartproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machinedeployment -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machineset -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
```

metadata 요약 조회:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'

kubectl -n "$NS" get machinedeployment "$MD" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'
```

condition 요약 조회:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} message={.message}{"\n"}{end}'

kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} reason={.reason} message={.message}{"\n"}{end}{end}'
```

controller 로그 조회:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher | grep "$CLUSTER"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor | grep "$CLUSTER"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor | grep "$CLUSTER" | grep "ERROR"
```

## 7. 핵심 metadata 사전

### 7.1 cluster metadata

| 용도 | key / 값 |
| --- | --- |
| cluster name label | `cluster.x-k8s.io/cluster-name=<cluster-name>` |
| Cluster ID | `cluster.x-k8s.io.ew/cluster-id` |
| generated security group ID | `cluster.x-k8s.io.ew/cluster-sg-id` |
| CAPI pause | `cluster.x-k8s.io/paused=true` |
| event-watcher pause | `cluster.x-k8s.io.ew/paused=true` |
| 최초 배포 단계 완료 marker | `cluster.x-k8s.io.ew/initial-deployment-completed=true|false` |
| 최초 배포 실패 후 pause marker | `cluster.x-k8s.io.ew/initial-deployment-paused=true` |

### 7.2 operation metadata

| 용도 | key / 값 |
| --- | --- |
| operation state | `cluster.x-k8s.io.am/operation-state` |
| suspend requested | `cluster.x-k8s.io.am/operation-state=suspend-requested` |
| suspend applied | `cluster.x-k8s.io.am/operation-state=suspend-applied` |
| resume requested | `cluster.x-k8s.io.am/operation-state=resume-requested` |
| resume applied | `cluster.x-k8s.io.am/operation-state=resume-applied` |
| suspend completed | `cluster.x-k8s.io.ep/last-suspended-done=<RFC3339 timestamp>` |
| resume completed | `cluster.x-k8s.io.ep/last-resumed-done=<RFC3339 timestamp>` |

### 7.3 node group / node metadata

| 용도 | key / 값 |
| --- | --- |
| node group ID | `cluster.x-k8s.io.ew/nodegroup-id` |
| 최초 배포 단계의 최초 node group label | `cluster.x-k8s.io.ew/initial-deployment=true` |
| node group paused | `cluster.x-k8s.io.ew/nodegroup-paused=true` |
| node group upgrade in progress | `cluster.x-k8s.io.ew/nodegroup-upgrade-in-progress=true` |
| node ID | `cluster.x-k8s.io.ew/node-id` |
| CAPI pause on MachineDeployment | `cluster.x-k8s.io/paused=true` |

### 7.4 workload resource metadata

| 용도 | key / 값 |
| --- | --- |
| cluster resource ID | `cluster.x-k8s.io.ew/resource-id` |
| workload resource tenant | `tenant.meta.x-k8s.io/id=<tenant-id>` |
| workload resource cluster name | `cluster.meta.x-k8s.io/name=<cluster-name>` |
| Service LB ID | `service.beta.kubernetes.io/kinx-load-balancer-id=<lb-id>` |
| Service LB cleanup finalizer | `service.kubernetes.io/load-balancer-cleanup` |
| PV protection finalizer | `kubernetes.io/pv-protection` |
| event-watcher resource finalizer | `cluster.x-k8s.io.ew/resource` |

### 7.5 동기화 annotation

| 이벤트 | annotation |
| --- | --- |
| cluster create started | `cluster.x-k8s.io.ew/cluster-create-started=true` |
| cluster create completed | `cluster.x-k8s.io.ew/cluster-create-completed=true` |
| cluster create failed | `cluster.x-k8s.io.ew/cluster-create-failed=true` |
| cluster delete started | `cluster.x-k8s.io.ew/cluster-delete-started=true` |
| cluster delete completed | `cluster.x-k8s.io.ew/cluster-delete-completed=true` |
| cluster delete failed | `cluster.x-k8s.io.ew/cluster-delete-failed=true` |
| node group create completed | `cluster.x-k8s.io.ew/nodegroup-create-completed=true` |
| node group create failed | `cluster.x-k8s.io.ew/nodegroup-create-failed=true` |
| node group scale completed | `cluster.x-k8s.io.ew/nodegroup-scale-completed=true` |
| node group scale failed | `cluster.x-k8s.io.ew/nodegroup-scale-failed=true` |
| node group upgrade completed | `cluster.x-k8s.io.ew/nodegroup-upgrade-completed=true` |
| node group upgrade failed | `cluster.x-k8s.io.ew/nodegroup-upgrade-failed=true` |
| node group delete completed | `cluster.x-k8s.io.ew/nodegroup-delete-completed=true` |
| node group delete failed | `cluster.x-k8s.io.ew/nodegroup-delete-failed=true` |
| node create completed | `cluster.x-k8s.io.ew/node-create-completed=true` |
| node delete completed | `cluster.x-k8s.io.ew/node-delete-completed=true` |
| resource create completed | `cluster.x-k8s.io.ew/resource-create-completed=true` |
| resource update completed | `cluster.x-k8s.io.ew/resource-update-completed=true` |
| resource attach completed | `cluster.x-k8s.io.ew/resource-attach-completed=true` |
| resource detach completed | `cluster.x-k8s.io.ew/resource-detach-completed=true` |
| resource delete completed | `cluster.x-k8s.io.ew/resource-delete-completed=true` |

대부분의 동기화 annotation에는 대응하는 `*-timestamp` annotation이 함께 기록될 수 있다.

## 8. Cluster 생성 단계

### 8.1 api-manager가 생성하는 상위 리소스

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation / label |
| --- | --- | --- | --- |
| tenant 생성 | `Namespace.metadata.name=<tenant-id>` | tenant namespace 생성 | 없음 |
| cluster 생성 | `Secret.metadata.name=<cluster-name>-cloud-conf` | CCM cloud config secret 생성 | 없음 |
| cluster 생성 | `Secret.metadata.name=<cluster-name>-clouds-secret` | CAPO cloud credential secret 생성 | 없음 |
| cluster 생성 | `ServiceAccount/default.imagePullSecrets` | image pull secret 연결 | 없음 |
| cluster 생성 | `OpenStackCluster.spec` | infra network, subnet, cloud config 참조 설정 | cluster ID 관련 label |
| cluster 생성 | `KamajiControlPlane.spec` | version, replicas, datastore, service type, DNS, domain 설정 | 없음 |
| cluster 생성 | `Cluster.spec.controlPlaneRef` | `KamajiControlPlane` 참조 | watcher가 `cluster-id` 보정 가능 |
| cluster 생성 | `Cluster.spec.infrastructureRef` | `OpenStackCluster` 참조 | default addon selector label |
| cluster 생성 후 | cloud config `Secret.metadata.ownerReferences` | `Cluster` owner reference patch | 없음 |

### 8.2 addon 선언 리소스

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation / label |
| --- | --- | --- | --- |
| cluster 생성 | `HelmRelease` | cloud-controller-manager 선언 | `cluster.x-k8s.io/cluster-name=<cluster-name>` |
| cluster 생성 | `HelmRelease` | cluster-autoscaler 선언 | `cluster.x-k8s.io/cluster-name=<cluster-name>` |
| cluster 생성 | `HelmChartProxy` | Calico addon 선언 | cluster 관련 label |
| cluster 생성 | `HelmChartProxy` | Cinder CSI addon 선언 | cluster 관련 label |
| cluster 생성 | `HelmChartProxy` | NFS CSI addon 선언 | cluster 관련 label |
| cluster 생성 | `HelmChartProxy` | Manila CSI addon 선언 | cluster 관련 label |
| `HelmChartProxy` 생성 후 | `HelmReleaseProxy` | addon controller가 자동 생성 | cluster 관련 label |

### 8.3 최초 node group 선언 리소스

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation / label |
| --- | --- | --- | --- |
| 최초 node group 생성 | `MachineDeployment.metadata.labels` | 최초 배포 단계에 포함되는 node group 식별 label 추가 | `cluster.x-k8s.io.ew/initial-deployment=true` |
| node group 생성 | `KubeadmConfigTemplate.spec` | kubeadm join 설정 생성 | cluster/name 관련 label |
| node group 생성 | `OpenStackMachineTemplate.spec` | flavor, image, volume, network, subnet, keypair, security group 설정 | cluster/name 관련 label |
| node group 생성 | `MachineDeployment.spec.clusterName` | 대상 cluster 이름 설정 | `cluster.x-k8s.io/cluster-name=<cluster-name>` |
| node group 생성 | `MachineDeployment.spec.replicas` | 요청 replica 수 설정 | last-created 계열 annotation |
| node group 생성 | `MachineDeployment.spec.template.spec.version` | Kubernetes version 설정 | kubeadm version skew skip annotation |
| auto-healer 활성화 | `MachineHealthCheck.spec.clusterName` | health check 생성 | cluster/nodegroup 관련 label |

### 8.4 생성 초기 이상 판정

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `OpenStackCluster`, `KamajiControlPlane`, `Cluster` 중 일부가 없음 | 상위 선언 리소스 생성 전 또는 생성 실패 | api-manager 로그 |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji controller 생성 대기 또는 실패 | `capi-kamaji-controller-manager`, `kamaji` 로그 |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | addon controller 생성 대기 또는 실패 | `caaph-controller-manager` 로그 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI 전개 대기 또는 실패 | `capi-controller-manager` 로그 |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO 전개 대기 또는 실패 | `capo-controller-manager` 로그 |

## 9. Cluster 준비 상태 판정

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| infra 준비 전 | `Cluster.status.infrastructureReady=false` 또는 `InfrastructureReady=False` condition | event-watcher가 infra 준비 전 단계로 판단 | 없음 |
| control plane 준비 전 | `InfrastructureReady=True`, `ControlPlaneReady=False` | control plane 준비 전 단계로 판단 | 없음 |
| control plane 이상 가능 | `InfrastructureReady=True`, `ControlPlaneReady=False`, `TenantControlPlane`이 provisioning 상태가 아님 | unavailable 상황으로 판단 | 없음 |
| control plane 준비 완료 | `InfrastructureReady=True`, `ControlPlaneReady=True` | 최초 배포 단계의 control plane 성공 조건 충족 | 없음 |
| control plane upgrade 중 | `TenantControlPlane.status`의 version 상태가 upgrading | upgrade 진행 중으로 판단 | 없음 |
| suspend 완료 | `cluster.x-k8s.io.ep/last-suspended-done` 존재 | suspended 상태로 판단 | 없음 |

## 10. 최초 배포 단계

최초 배포 단계는 리소스가 아니라 Cluster 생성 직후의 종합 준비 상태를 나타내는 추상 개념이다. 관찰 대상은 `Cluster`, `Ingress`, 최초 node group `MachineDeployment`, `HelmRelease`, `HelmReleaseProxy`이며, 결과는 `Cluster` annotation과 최초 node group label로만 남는다.

### 10.1 성공 조건

| 구성 요소 | 관찰할 Kubernetes 필드 | 성공 조건 |
| --- | --- | --- |
| bastion | `Ingress.metadata.name=<cluster-name>` | cluster와 같은 이름의 `Ingress` 존재 |
| control plane | `Cluster.status.conditions` | `ControlPlaneReady=True` |
| 최초 node group | `MachineDeployment.metadata.labels` + condition | `initial-deployment=true` label이 붙은 `MachineDeployment`가 1개이고 Available condition true |
| addon | `HelmReleaseProxy.status.conditions` | Ready true |
| addon | `HelmRelease.status.conditions` | Ready true |

### 10.2 진행 / 성공 / 실패 annotation

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| component 중 하나 이상 미준비 | component conditions | 생성 시작 동기화가 없으면 시작 기록 | `cluster-create-started=true` |
| 모든 component 준비 | bastion, control plane, 최초 node group, addon 조건 모두 충족 | 최초 배포 단계 성공 처리 | `initial-deployment-completed=true`, `cluster-create-completed=true` |
| 허용 시간 내 완료 실패 | component conditions 미충족 | 실패 처리, addon suspend, cluster pause | `initial-deployment-completed=false`, `cluster-create-failed=true`, `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/initial-deployment-paused=true` |
| 실패 후 addon 정지 | cluster label을 가진 `HelmRelease` | 모든 `HelmRelease.spec.suspend=true` | 실패 annotation 유지 |

### 10.3 최초 배포 실패 후 processor cleanup

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| 최초 배포 실패 후 pause marker | `cluster.x-k8s.io.ew/initial-deployment-paused=true` | event-processor가 hosted control plane 삭제 요청 | 없음 |
| cluster ID 없음 | `cluster.x-k8s.io.ew/cluster-id` 없음 | cleanup 불가, 에러 로그 | 없음 |

## 11. NodeGroup 생성과 전개

### 11.1 전개 계층 판정

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `MachineDeployment` 없음 | node group 선언 전 또는 삭제됨 | api-manager 로그 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller 전개 전 | `capi-controller-manager` 로그 |
| `MachineSet` 있음, `Machine` 없음 | replica 전개 전 | MachineSet status |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO 전개 전 | `capo-controller-manager` 로그 |
| `Machine.phase=Running`, `MachineNodeHealthy=True` | node 사용 가능 | node create completed annotation |

### 11.2 MachineDeployment 상태 판정

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| 증설 중 | `MachineDeployment.status.phase=ScalingUp`, desired replicas와 current replicas 다름 | node group 생성 또는 scale up 진행 | 없음 |
| 증설 안정화 전 | `MachineDeployment.status.phase=ScalingUp`, desired replicas와 current replicas 같음 | 수량은 맞지만 안정화 전 | 없음 |
| 축소 중 | `MachineDeployment.status.phase=ScalingDown` | scale down 또는 delete 진행 | 없음 |
| 축소 중 | `MachineDeployment.status.phase=Running`, desired replicas < current replicas | scale down 진행 | 없음 |
| 사용 가능 | `MachineDeployment.status.phase=Running`, desired/current/available 조건 충족 | 완료 동기화 가능 | create/scale/upgrade completed annotation |

### 11.3 node group 생성 annotation

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| node group 생성 요청 | `MachineDeployment.metadata.annotations` | 생성 시작 marker 기록 | last-created 계열 annotation |
| 생성 완료 | `MachineDeployment.status` available 조건 | 생성 완료 동기화 | `nodegroup-create-completed=true` |
| 생성 timeout | last-created 있음, create completed 없음, 허용 시간 초과 | MachineDeployment pause | `nodegroup-create-failed=true`, `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/nodegroup-paused=true` |

## 12. NodeGroup scale

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| scale 요청 | `MachineDeployment.spec.replicas` | 요청 replica 수로 patch | last-scaled 계열 annotation |
| scale 요청 전 | `MachineDeployment.metadata.annotations` | 기존 완료 marker 제거 가능 | create/scale/delete completed annotation 제거 |
| scale up 진행 | `MachineDeployment.status.phase=ScalingUp` | replicas 수렴 대기 | 없음 |
| scale down 진행 | `MachineDeployment.status.phase=ScalingDown` 또는 desired < current | replicas 감소 대기 | 없음 |
| scale 완료 | desired/current/available 조건 충족 | scale 완료 동기화 | `nodegroup-scale-completed=true` |
| scale timeout | last-scaled 있음, scale completed 없음, 허용 시간 초과 | MachineDeployment pause | `nodegroup-scale-failed=true`, `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/nodegroup-paused=true` |

## 13. NodeGroup upgrade

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| upgrade 요청 | `MachineDeployment.spec.template.spec.version` | target Kubernetes version으로 patch | last-upgraded 계열 annotation |
| image 변경 | `OpenStackMachineTemplate.spec.template.spec.image` | target image로 새 template 또는 patch | 없음 |
| upgrade 진행 | 현재 version과 다른 old `MachineSet`이 있고 replicas > 0 | rolling upgrade 진행 표시 | `nodegroup-upgrade-in-progress=true` |
| upgrade 수렴 | old `MachineSet` 없음, updated/ready/available replicas가 desired와 같음 | progress marker 제거 | `nodegroup-upgrade-in-progress` 제거 |
| upgrade 완료 | marker 제거 + replica 조건 충족 | upgrade 완료 동기화 | `nodegroup-upgrade-completed=true` |
| upgrade timeout | last-upgraded 있음, upgrade completed 없음, 허용 시간 초과 | MachineDeployment pause | `nodegroup-upgrade-failed=true`, `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/nodegroup-paused=true` |

## 14. Machine 상태와 node annotation

### 14.1 Machine 준비

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| VM 생성 중 | `Machine.status.infrastructureReady=false` | VM provisioning 단계 | 없음 |
| node 등록 중 | `Machine.status.infrastructureReady=true`, phase가 Running 아님 | node provisioning 단계 | 없음 |
| node 사용 가능 | `Machine.status.phase=Running`, `MachineNodeHealthy=True` | node 생성 완료 동기화 가능 | `node-create-completed=true` |
| node unhealthy | `Machine.status.phase=Running`, `MachineNodeHealthy=False` | node unhealthy로 판단 | 없음 |

### 14.2 Machine 삭제

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| parent cluster 삭제 중 | `Cluster.metadata.deletionTimestamp` 존재 | VM 삭제 흐름으로 판단 | 없음 |
| drain 중 | `DrainingSucceeded=Unknown` 또는 `False` | node drain 진행 중 | 없음 |
| volume detach 중 | `VolumeDetachSucceeded=Unknown` 또는 `False` | volume detach 진행 중 | 없음 |
| VM 삭제 중 | `InfrastructureReady=Unknown` 또는 `True` | infra 삭제 중 | 없음 |
| machine 삭제 완료 | CAPI `MachineFinalizer` 없음 | node 삭제 완료 동기화 가능 | `node-delete-completed=true` |

## 15. Cluster suspend / resume

### 15.1 suspend 요청과 적용

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| suspend API 요청 | `Cluster.metadata.annotations` | api-manager가 요청 marker 기록 | `operation-state=suspend-requested` |
| suspend 요청 처리 | `operation-state=suspend-requested`, `last-suspended-done` 없음 | event-processor가 실행 시작 | `operation-state=suspend-applied` |
| component scale down | cluster label을 가진 `HelmRelease.spec.values` | `replicaCount=0`으로 patch | 없음 |
| control plane scale down | `KamajiControlPlane.spec.replicas` | `0`으로 patch | 없음 |
| state marker 갱신 | `Cluster.metadata.annotations` | 기존 done marker 제거 후 applied 기록 | `operation-state=suspend-applied` |

### 15.2 suspend 완료

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| KCP 아직 축소 전 | `KamajiControlPlane.status.readyReplicas != 0` | 재시도 | 없음 |
| component 아직 축소 전 | component `Deployment.status.replicas`, `readyReplicas`, `updatedReplicas`, `unavailableReplicas`가 0으로 수렴하지 않음 | 재시도 | 없음 |
| 모든 replica 0 | KCP ready replicas 0, component replicas 0 | `HelmRelease.spec.suspend=true`, CAPI pause 추가 | `last-suspended-done=<timestamp>`, `operation-state` 제거 |

### 15.3 resume 요청과 적용

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| resume API 요청 | `Cluster.metadata.annotations` | api-manager가 요청 marker 기록 | `operation-state=resume-requested` |
| resume 요청 처리 | `operation-state=resume-requested`, `last-suspended-done` 존재 | event-processor가 실행 시작 | `operation-state=resume-applied` |
| HelmRelease resume | cluster label을 가진 `HelmRelease.spec.suspend` | `false`로 patch | 없음 |
| cluster unpause | `Cluster.metadata.annotations["cluster.x-k8s.io/paused"]` | CAPI pause 제거 | 없음 |
| component scale up | cluster label을 가진 `HelmRelease.spec.values` | `replicaCount=1`로 patch | 없음 |
| control plane scale up | `KamajiControlPlane.spec.replicas` | `3`으로 patch | 없음 |

### 15.4 resume 완료

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| KCP 아직 복구 전 | `KamajiControlPlane.status.readyReplicas != 3` | 재시도 | 없음 |
| component 아직 복구 전 | component `Deployment.status.replicas`, `readyReplicas`, `updatedReplicas`, `unavailableReplicas`가 1로 수렴하지 않음 | 재시도 | 없음 |
| 모든 replica 복구 | KCP ready replicas 3, component replicas 1 | resume 완료 | `last-resumed-done=<timestamp>`, `operation-state` 제거 |

### 15.5 비정상 operation 조합

| 조건 | 판단 |
| --- | --- |
| `last-suspended-done`과 `last-resumed-done`이 모두 있고 operation state 없음 | 비정상 |
| `operation-state=suspend-requested`인데 `last-suspended-done` 존재 | 비정상 |
| `operation-state=resume-requested`인데 `last-suspended-done` 없음 | 비정상 |
| `operation-state=suspend-applied` 또는 `resume-applied`인데 done marker 존재 | 비정상 |

## 16. AutoScaler

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| autoscaler 생성 | `MachineDeployment.metadata.annotations` | cluster-autoscaler 관련 annotation 추가 | autoscaler min/max/enabled 계열 annotation |
| autoscaler 수정 | `MachineDeployment.metadata.annotations` | cluster-autoscaler 관련 annotation patch | autoscaler min/max/enabled 계열 annotation |
| autoscaler 삭제 | `MachineDeployment.metadata.annotations` | autoscaler 관련 annotation 제거 | 제거됨 |

## 17. ServiceGatewayClaim

### 17.1 생성 조건과 결과

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 label / ownerReference |
| --- | --- | --- | --- |
| MachineDeployment 삭제 중 아님 | `MachineDeployment.metadata.deletionTimestamp` 없음 | claim 생성 가능 | 없음 |
| Cluster 삭제 중 아님 | `Cluster.metadata.deletionTimestamp` 없음 | claim 생성 가능 | 없음 |
| node group ID 존재 | `MachineDeployment.metadata.annotations["cluster.x-k8s.io.ew/nodegroup-id"]` | claim 생성 가능 | 없음 |
| claim 없음 | `ServiceGatewayClaim` not found | 새 claim 생성 | `iksv2.kinx.net/cluster-name=<cluster-name>` |
| claim 생성 | `ServiceGatewayClaim.metadata.ownerReferences` | MachineDeployment controller owner reference 설정 | ownerReference |

### 17.2 생성되는 claim 형태

| 항목 | 값 |
| --- | --- |
| 이름 | `iks-sgwclaim-<subnet-id-prefix>-<machineDeploymentName>` |
| namespace | cluster namespace |
| label | `iksv2.kinx.net/cluster-name=<cluster-name>` |
| consumer | `Resource=Cluster`, `Name=<cluster-name>` |
| owner reference | MachineDeployment |

### 17.3 생성 실패 cleanup

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| node group create failed 없음 | `nodegroup-create-failed` annotation 없음 | cleanup 없음 | 없음 |
| create failed이나 paused 아님 | `nodegroup-create-failed=true`, CAPI pause 없음 | cleanup 없음 | 없음 |
| create failed + paused | `nodegroup-create-failed=true`, `cluster.x-k8s.io/paused=true` | data plane 삭제 요청 | 없음 |
| 삭제 요청 후 | `MachineDeployment.metadata.deletionTimestamp` | 하위 리소스 삭제 진행 | watcher가 delete annotation 기록 가능 |

## 18. Service LoadBalancer

### 18.1 대상 조건

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| workload Service | `Service.metadata.annotations["tenant.meta.x-k8s.io/id"]` 존재 | root tenant와 연결 | 없음 |
| workload Service | `Service.metadata.annotations["cluster.meta.x-k8s.io/name"]` 존재 | root cluster와 연결 | 없음 |
| LB Service | `Service.spec.type=LoadBalancer` | LB resource로 관찰 | `cluster.x-k8s.io.ew/resource-id` 보정 가능 |

### 18.2 OpenStack LB 상태와 annotation

| 조건 | 관찰할 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| LB ID 없음 | `service.beta.kubernetes.io/kinx-load-balancer-id` 없음 | LB 생성 중으로 판단 | 없음 |
| LB 생성 중 | OpenStack provisioning `PENDING_CREATE` | 재확인 | 없음 |
| LB 갱신 중 | OpenStack provisioning `PENDING_UPDATE` | 재확인 | 없음 |
| LB 사용 가능 | OpenStack provisioning `ACTIVE` | 생성 또는 갱신 완료 동기화 | `resource-create-completed=true` 또는 `resource-update-completed=true` |
| LB 오류 | OpenStack provisioning `ERROR` | 오류로 판단 | 없음 |
| LB 삭제 중 | OpenStack provisioning `PENDING_DELETE` | 삭제 흐름 | 없음 |
| LB 없음 | OpenStack not found | 삭제 완료 동기화 가능 | `resource-delete-completed=true` |

### 18.3 Service 삭제

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| Service 삭제 시작 | `Service.metadata.deletionTimestamp` 존재 | 삭제 진행 | 없음 |
| LB cleanup 완료 | `service.kubernetes.io/load-balancer-cleanup` finalizer 없음 | 삭제 완료 동기화, cleanup annotation 제거, resource finalizer 제거 | `resource-delete-completed=true` |

## 19. PersistentVolume

### 19.1 대상 조건

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| Cinder PV | `PersistentVolume.spec.csi.driver=cinder.csi.openstack.org` | 관리 대상 | `resource-id` 보정 가능 |
| Manila PV | `PersistentVolume.spec.csi.driver=manila.csi.openstack.org` | 관리 대상 | `resource-id` 보정 가능 |
| root cluster 연결 | tenant/cluster annotation 존재 | root cluster와 연결 | 없음 |

### 19.2 PV 상태와 annotation

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| volume 생성됨 | PV phase `Available` 또는 `Bound`, CSI volume handle 존재 | volume created로 판단 | `resource-create-completed=true` |
| volume attached | 대응 `VolumeAttachment.status.attached=true` | attach 완료로 판단 | `resource-attach-completed=true` |
| volume detached | PV phase `Released` | detach 완료로 판단 | `resource-detach-completed=true` |
| 판정 불가 | 위 조건에 속하지 않음 | 재확인 | 없음 |

### 19.3 PV 삭제

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| PV 삭제 시작 | `PersistentVolume.metadata.deletionTimestamp` 존재 | 삭제 진행 | 없음 |
| protection finalizer 제거 | `kubernetes.io/pv-protection` finalizer 없음 | detach와 삭제 완료 보장, resource finalizer 제거 | `resource-delete-completed=true` |
| retain volume metadata | reclaim policy `Retain`, cinder/manila volume, 전용 metadata key 존재 | OpenStack metadata key cleanup 시도 | 없음 |

## 20. Cluster 삭제

### 20.1 api-manager 삭제 요청 결과

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| 초기 적용 전 단계 삭제 | `KamajiControlPlane.metadata.deletionTimestamp` | KCP 삭제 요청 | 없음 |
| 초기 적용 전 단계 삭제 | `OpenStackCluster.metadata.deletionTimestamp` | OpenStackCluster 삭제 요청 | 없음 |
| 일반 cluster 삭제 | `Cluster.metadata.annotations["cluster.x-k8s.io/paused"]` | CAPI pause 제거 | `cluster.x-k8s.io/paused` 제거 |
| 일반 cluster 삭제 | `Cluster.metadata.deletionTimestamp` | Cluster 삭제 시작 | watcher가 `cluster-delete-started=true` 기록 |
| addon cleanup | `HelmChartProxy.metadata.deletionTimestamp` | chart proxy 삭제 요청 | 없음 |
| addon cleanup | `HelmReleaseProxy.metadata.deletionTimestamp` | release proxy 삭제 요청 | 없음 |
| finalizer cleanup | `HelmReleaseProxy.metadata.finalizers` | finalizer 제거 | 없음 |
| finalizer cleanup | `HelmRelease.metadata.finalizers` | finalizer 제거 후 삭제 요청 | 없음 |

### 20.2 event-watcher 삭제 완료 조건

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| 삭제 시작 | `Cluster.metadata.deletionTimestamp` 존재 | 삭제 진행으로 판단 | `cluster-delete-started=true` |
| addon 남음 | cluster label을 가진 `HelmRelease` 존재 | 완료로 보지 않음 | 없음 |
| addon 남음 | cluster label을 가진 `HelmChartProxy` 존재 | 완료로 보지 않음 | 없음 |
| ingress 남음 | cluster와 같은 이름의 `Ingress` 존재 | 완료로 보지 않음 | 없음 |
| finalizer 남음 | CAPI cluster finalizer 존재 | 완료로 보지 않음 | 없음 |
| 삭제 완료 | addon 없음, ingress 없음, CAPI finalizer 없음 | 삭제 완료 동기화 | `cluster-delete-completed=true` |
| 삭제 timeout | 허용 시간 초과 | 실패 동기화, cluster pause | `cluster-delete-failed=true`, `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/paused=true` |

### 20.3 event-processor 삭제 cleanup

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| 다른 finalizer 남음 | `Cluster.metadata.finalizers`에 event-processor finalizer 외 항목 존재 | cleanup 시작하지 않고 재시도 | 없음 |
| event-processor finalizer만 남음 | `Cluster.metadata.finalizers` | 외부 cleanup 진행 가능 | 없음 |
| cloud-conf secret 존재 | `<cluster-name>-cloud-conf` `Secret` | keypair, user cleanup 후 secret 삭제 | 없음 |
| ServiceGatewayClaim 남음 | `iksv2.kinx.net/cluster-name=<cluster-name>` label | claim 삭제 요청 | 없음 |
| cleanup 완료 | 외부 resource, credential, SGW claim cleanup 완료 | event-processor finalizer 제거 | 없음 |

## 21. NodeGroup 삭제

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| delete 요청 | `MachineDeployment.metadata.annotations["cluster.x-k8s.io/paused"]` | CAPI pause 제거 | pause annotation 제거 |
| delete 요청 | `MachineDeployment.metadata.deletionTimestamp` | 삭제 시작 | 없음 |
| 하위 리소스 삭제 | `MachineSet`, `Machine`, `OpenStackMachine` | CAPI/CAPO가 하위 리소스 삭제 | 없음 |
| 삭제 timeout | 허용 시간 초과 | 실패 동기화 | `nodegroup-delete-failed=true` |
| 삭제 완료 | CAPI `MachineDeploymentFinalizer` 없음 | 완료 동기화, watcher finalizer 제거 | `nodegroup-delete-completed=true` |

## 22. Soot manager와 workload cluster 관찰

| 조건 | 관찰할 Kubernetes 필드 | 후속 변화 | 기록되는 annotation |
| --- | --- | --- | --- |
| root cluster 접근 가능 | `Cluster` | workload watcher 집합 시작 | `cluster.x-k8s.io.ew/soot-manager-start-completed` 가능 |
| root cluster 삭제 | `Cluster.metadata.deletionTimestamp` | workload watcher context 종료, cache entry 제거 | soot manager finalizer 제거 |
| remote client 미연결 | cluster cache connection error | 재시도 | 없음 |
| remote client 확보 | workload cluster API 접근 가능 | remote watcher 등록 | 없음 |
| webhook 보정 | workload `ValidatingWebhookConfiguration` 또는 `MutatingWebhookConfiguration` | predefined webhook, CA bundle, label 보정 | `cluster.x-k8s.io/cluster-name=<cluster-name>` label |
| webhook service 보정 | workload `Service` | headless service 생성 또는 갱신 | `cluster.x-k8s.io/cluster-name=<cluster-name>` label |

## 23. 로그 분석 기준

### 23.1 기본 로그 명령

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor
```

cluster 기준 필터링:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher | grep "<cluster-key>"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor | grep "<cluster-key>"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor | grep "<cluster-key>" | grep "ERROR"
```

### 23.2 로그 해석 원칙

| 조건 | 판단 |
| --- | --- |
| 에러 문자열만 검색 | 다른 cluster 이벤트와 섞일 수 있음 |
| cluster key 없이 processor 로그 확인 | 잘못된 리소스 이벤트를 볼 수 있음 |
| watcher와 processor 시간이 다름 | 같은 lifecycle 단계를 비교하지 못할 수 있음 |
| watcher 로그에 상태 관찰만 있음 | 실제 조치가 필요한 경우 processor 로그도 확인 |
| processor 로그에 cleanup 지연 | finalizer 또는 외부 resource 상태 확인 |

## 24. 상태별 빠른 판정 시나리오

### 24.1 cluster 생성이 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `OpenStackCluster` 없음 | api-manager 생성 단계 또는 Kubernetes create 실패 |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji controller |
| `Cluster.status.infrastructureReady=false` | CAPO / OpenStack infra |
| `ControlPlaneReady=False` | Kamaji / TenantControlPlane |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | CAAH addon controller |
| 최초 node group `MachineDeployment` 있음, `MachineSet` 없음 | CAPI controller |

### 24.2 최초 배포 단계가 실패한 경우

| 관찰 | 의미 |
| --- | --- |
| `initial-deployment-completed=false` | watcher가 최초 배포 단계 실패를 기록 |
| `cluster-create-failed=true` | cluster creation 실패 동기화 기록 |
| `HelmRelease.spec.suspend=true` | 실패 후 addon 정지 |
| `cluster.x-k8s.io/paused=true` | cluster pause |
| `initial-deployment-paused=true` | 최초 배포 실패 후 processor cleanup 대상 |

### 24.3 node group 생성이 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `MachineDeployment` 없음 | api-manager 생성 단계 |
| `MachineSet` 없음 | CAPI controller |
| `Machine` 없음 | MachineSet replica 전개 |
| `OpenStackMachine` 없음 | CAPO controller |
| `MachineNodeHealthy=False` | node join 또는 kubelet 상태 |
| `nodegroup-create-failed=true` | watcher가 생성 timeout 실패 기록 |

### 24.4 suspend가 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `operation-state=suspend-requested` | processor가 아직 처리하지 못함 |
| `operation-state=suspend-applied` | replica 수렴 대기 |
| `KamajiControlPlane.status.readyReplicas != 0` | control plane pod scale down 지연 |
| component `Deployment.status.replicas != 0` | addon component scale down 지연 |
| `last-suspended-done` 없음 | suspend 완료 전 |

### 24.5 resume이 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `operation-state=resume-requested` | processor가 아직 처리하지 못함 |
| `operation-state=resume-applied` | replica 수렴 대기 |
| `KamajiControlPlane.status.readyReplicas != 3` | control plane pod scale up 지연 |
| component `Deployment.status.readyReplicas != 1` | addon component 복구 지연 |
| `last-resumed-done` 없음 | resume 완료 전 |

### 24.6 cluster 삭제가 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `HelmRelease` 남음 | watcher가 삭제 완료로 보지 않음 |
| `HelmChartProxy` 남음 | watcher가 삭제 완료로 보지 않음 |
| 같은 이름 `Ingress` 남음 | watcher가 삭제 완료로 보지 않음 |
| CAPI cluster finalizer 남음 | CAPI cleanup 대기 |
| event-processor 외 finalizer 남음 | processor cleanup 시작 전 |
| cloud-conf `Secret` 남음 | credential cleanup 전 또는 실패 |
| `ServiceGatewayClaim` 남음 | servicegateway cleanup 전 또는 실패 |
| `cluster-delete-failed=true` | watcher가 삭제 timeout 실패 기록 |

## 25. 운영상 불명확하거나 외부 controller 확인이 필요한 지점

| 항목 | 이유 | 확인 대상 |
| --- | --- | --- |
| `TenantControlPlane` 미생성 | `KamajiControlPlane` 이후 외부 controller가 생성 | `capi-kamaji-controller-manager`, `kamaji` |
| `MachineSet`, `Machine` 미생성 | CAPI controller 자동 생성 | `capi-controller-manager` |
| `OpenStackMachine` 미생성 | CAPO controller 자동 생성 | `capo-controller-manager` |
| `HelmReleaseProxy` 미생성 | CAAH addon controller 자동 생성 | `caaph-controller-manager` |
| `KubeadmConfig` 또는 bootstrap Secret 없음 | kubeadm bootstrap controller 담당 | `capi-kubeadm-bootstrap-controller-manager` |
| `ServiceGatewayClaim`은 있으나 gateway 없음 | servicegateway operator 담당 | `iksv2-servicegateway-controller-manager` |
| workload `Service`/`PersistentVolume` 관찰 누락 | soot manager 또는 remote client 연결 필요 | event-watcher, cluster cache |
| OpenStack LB/volume 삭제 지연 | Kubernetes finalizer와 외부 cloud 상태가 함께 관여 | event-processor 로그, OpenStack API |

## 26. 최종 판정 원칙

1. API 요청 결과는 `api-manager`가 만든 상위 리소스와 metadata로 판단한다.
2. 실제 readiness는 Kubernetes `status`와 `conditions`로 판단한다.
3. 완료/실패/삭제 동기화는 `cluster.x-k8s.io.ew/*` annotation으로 판단한다.
4. suspend/resume은 `operation-state`, `spec.replicas`, component `Deployment.status`, done annotation을 함께 본다.
5. 삭제 완료는 `deletionTimestamp`만으로 판단하지 않는다. 하위 리소스 잔존 여부와 finalizer 제거 여부를 함께 본다.
6. 자동 생성 리소스가 없으면 상위 리소스 존재 여부를 먼저 확인하고, 그 다음 담당 controller 로그를 확인한다.
7. processor 로그는 cluster key 또는 cluster name으로 먼저 좁힌 뒤 에러를 확인한다.
