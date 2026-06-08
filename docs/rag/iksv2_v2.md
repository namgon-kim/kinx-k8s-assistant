# IKS v2 Hosted Control Plane 트러블슈팅 종합 문서

## 0. 문서 범위

이 문서는 cluster-api(CAPI) + cluster-api-provider-openstack(CAPO) + cluster-api-addon-provider-helm(CAAPH) + Kamaji + 자체 컨트롤러로 구성된 IKS v2 Hosted Control Plane 서비스를, Kubernetes 리소스 신호만으로 진단하기 위한 RAG 문서다. tenant cluster의 control plane은 물리 노드가 아니라 management cluster 위의 Pod(Kamaji `TenantControlPlane`)로 동작하고, worker는 CAPI/CAPO로 OpenStack VM에 프로비저닝된다.

사용하는 정보:

- Kubernetes 리소스의 `metadata.annotations`, `metadata.labels`, `metadata.ownerReferences`, `metadata.finalizers`, `metadata.deletionTimestamp`
- Kubernetes 리소스의 `spec`, `status`, `status.conditions`
- OpenStack LoadBalancer provisioning status (workload `Service` 경유)

사용하지 않는 정보:

- 서비스 내부 저장소(DB) 값, 내부 상태 코드, 내부 상태 전이 코드

OpenStack 추론과 로그 처리:

- OpenStack을 직접 조회하는 도구가 없다. OpenStack 자원 상태는 CAPO CRD(`OpenStackCluster`, `OpenStackMachine`)의 `status`/`conditions`/`failureMessage`로 추론한다. 클러스터를 생성했고 OpenStack 관련 condition/에러가 없으면 정상 생성으로 간주한다.
- 로그 수집 도구가 없다. 로그가 필요한 단계에서는 직접 실행하지 않고, 사용자에게 실행할 명령을 안내한다.

구성 요소와 API group(버전 번호는 운영 환경마다 다를 수 있어 표기하지 않는다):

- CAPI: `cluster.x-k8s.io` — `Cluster`, `Machine`, `MachineDeployment`, `MachineSet`, `MachineHealthCheck`
- CAPO: `infrastructure.cluster.x-k8s.io` — `OpenStackCluster`, `OpenStackMachine`, `OpenStackMachineTemplate`
- CAAPH: `addons.cluster.x-k8s.io` — `HelmChartProxy`, `HelmReleaseProxy`
- Kamaji: `controlplane.cluster.x-k8s.io` `KamajiControlPlane`, `kamaji.clastix.io` `TenantControlPlane`

진단 원칙(요약): annotation/label로 "시스템이 마킹한 단계"를 먼저 확정하고, 그다음 `conditions`/`status`로 실제 상태가 그 마킹과 일치하는지 추적한다. 불일치하는 지점이 곧 문제 지점이다. condition 이름이 버전에 따라 안 보이면 같은 의미의 `.status` 신호를 직접 읽는다.

## 1. 빠른 검색 인덱스 (현상 → typed 앵커)

RAG 검색은 typed 신호(`target.kind`, `detection_type`, `evidence_keywords`, `tags`)로 매칭된다. 아래 표는 현상에서 그 typed 앵커와 확인 섹션으로 연결한다. `evidence_keywords`는 실제 리소스에 나타나는 condition/reason/status/annotation 토큰이다.

| 현상 | target.kind | detection_type | evidence_keywords (condition/reason/status/annotation 토큰) | tags | 섹션 |
| --- | --- | --- | --- | --- | --- |
| 클러스터 생성이 오래/안 끝난다 | Cluster | Timeout | `cluster-create-started`, `InfrastructureReady=False`, `ControlPlaneReady=False` | cluster-create, provisioning | 7,8,9 |
| 생성 중(인프라)에서 안 넘어간다 | OpenStackCluster | Timeout, ConfigError | `status.ready=false`, `network=null`, `router=null`, `failureMessage` | infrastructure, openstack, network | 7.2 |
| 인프라 quota/용량 부족 | OpenStackCluster, OpenStackMachine | FailedScheduling | `Quota exceeded`, `No valid host was found` | openstack, quota, capacity | 6.9,7.2,11.1 |
| API 서버 접속/ kubeconfig 안 됨 | TenantControlPlane | NetworkFailure, Timeout | `controlPlaneEndpoint=""`, `version.status` | control-plane, endpoint, kamaji | 8 |
| 컨트롤 플레인 Pod 안 뜸/재시작 | TenantControlPlane | CrashLoopBackOff, Pending | `version.status=Provisioning`, `version.status=NotReady`, `WriteLimited` | control-plane, kamaji, datastore | 8 |
| 초기 배포 실패/paused | Cluster | Timeout | `initial-deployment-completed=false`, `initial-deployment-paused=true`, `paused=true` | initial-deployment, paused | 9 |
| 노드(VM)가 안 생긴다 | MachineDeployment, MachineSet, Machine, OpenStackMachine | Pending | 하위 리소스 부재, `InstanceReady=False` | nodegroup, provisioning, capo | 10.1,11.1 |
| VM 생성 실패 (이미지/flavor/port/volume) | OpenStackMachine | ConfigError, FailedScheduling | `InstanceCreateFailed`, `InstanceStateError`, `InvalidMachineSpec`, `failureMessage` | openstack, vm, instance | 11.1,6.9 |
| VM은 떴는데 클러스터에 안 붙는다 (join 실패) | Machine, OpenStackMachine | Timeout | `providerID` 있음, `phase=Provisioned`, `nodeRef=""`, `WaitingForNodeRef`, `BootstrapReady`, `WaitingForDataSecret` | join, bootstrap, kubelet, servicegateway | 11.2 |
| 노드가 NotReady에서 안 올라온다 | Machine, Node | ProbeFailed | `nodeRef` 있음, `NodeHealthy=False`, `NodeConditionsFailed` | notready, cni, calico | 11.2 |
| 노드가 자꾸 지워지고 다시 생긴다 | Machine, MachineHealthCheck | Timeout | `OwnerRemediated`, `HealthCheckSucceeded=False`, `NodeStartupTimeout`, `UnhealthyNode` | mhc, remediation | 11.3 |
| 증설/축소(scale)가 안 끝난다 | MachineDeployment | Timeout | `phase=ScalingUp`, `phase=ScalingDown`, `nodegroup-scale-failed` | scale, nodegroup | 10.2 |
| 업그레이드가 안 끝난다 | MachineDeployment, TenantControlPlane | Timeout | `nodegroup-upgrade-in-progress`, old MachineSet, `version.status=Upgrading` | upgrade, nodegroup, control-plane | 10.3,8 |
| addon(calico/CSI/CCM)이 안 깔린다 | HelmReleaseProxy, HelmChartProxy | ConfigError | `HelmReleaseReady=False`, `HelmInstallOrUpgradeFailed`, `HelmReleaseProxySpecsUpToDate`, `ClusterAvailable=False` | addon, helm, calico, csi, ccm | 12 |
| addon 설치가 너무 느림/오래 pending | HelmReleaseProxy | Timeout | `HelmReleasePending`, `status.status=pending-install`, `status.status=pending-upgrade`, `options.wait`, `atomic` | addon, helm, wait, pending | 12 |
| LB 서비스 EXTERNAL-IP 안 붙음 | Service | ServiceNoEndpoint, NetworkFailure | `kinx-load-balancer-id` 없음, `PENDING_CREATE`, `ERROR` | loadbalancer, ccm, openstack | 14.1 |
| PVC Pending/볼륨 안 붙음 | PersistentVolume, PersistentVolumeClaim | Pending | PV phase, `VolumeAttachment.attached`, `cinder.csi.openstack.org`, `manila.csi.openstack.org` | pv, csi, cinder, manila | 14.2 |
| suspend/resume이 안 끝난다 | Cluster, KamajiControlPlane | Timeout | `operation-state=suspend-applied`, `operation-state=resume-applied`, `last-suspended-done`, `last-resumed-done` | suspend, resume | 13 |
| 클러스터 삭제가 안 끝남/Deleting 멈춤 | Cluster | Timeout | `deletionTimestamp`, `paused=true`, 잔존 HelmRelease/HelmChartProxy/Ingress, finalizer, `InstanceDeleteFailed` | delete, cleanup, finalizer, deleting | 15.1,15.2 |
| 노드그룹 삭제가 안 끝난다 | MachineDeployment | Timeout | `deletionTimestamp`, `nodegroup-delete-failed`, `MachineDeploymentFinalizer` | delete, nodegroup, finalizer | 15.5 |

> `detection_type`은 표준 enum(CrashLoopBackOff, OOMKilled, ImagePullBackOff, Pending, FailedScheduling, ProbeFailed, ServiceNoEndpoint, NetworkFailure, Timeout, DiskFull, PermissionDenied, ConfigError, Unknown) 중 가장 가까운 값이다. CAPI/CAPO/Kamaji 고유 현상은 표준 enum이 정확히 없을 수 있으므로 `target.kind` + `evidence_keywords` + `tags`가 1차 매칭 신호다.

## 2. Controller 구성

| Namespace | Controller | 역할 | 주요 리소스 | 확인 포인트 |
| --- | --- | --- | --- | --- |
| `capi-system` | `capi-controller-manager` | CAPI 핵심 lifecycle | `Cluster`, `MachineDeployment`, `MachineSet`, `Machine`, `MachineHealthCheck` | `Cluster.status`, `MachineDeployment.status`, `Machine.status` |
| `capi-kubeadm-bootstrap-system` | `capi-kubeadm-bootstrap-controller-manager` | Machine bootstrap data 생성 | `KubeadmConfigTemplate`, `KubeadmConfig`, `Secret`, `Machine` | `KubeadmConfig.status.dataSecretName`, bootstrap Secret |
| `capo-system` | `capo-controller-manager` | OpenStack 인프라/VM 연동 | `OpenStackCluster`, `OpenStackMachineTemplate`, `OpenStackMachine` | network/router/SG/endpoint, VM/port/volume |
| `kamaji-system` | `capi-kamaji-controller-manager` | Kamaji를 CAPI control plane provider로 연결 | `KamajiControlPlane`, `TenantControlPlane`, `Cluster` | `KamajiControlPlane.status`, `TenantControlPlane` 생성 |
| `kamaji-system` | `kamaji` | Hosted control plane 실행 | `TenantControlPlane`, control plane pods, `Secret`, `Service` | control plane pod, `version.status`, endpoint |
| `caaph-system` | `caaph-controller-manager` | Helm 기반 addon 배포 관리 | `HelmChartProxy`, `HelmReleaseProxy`, `Cluster` | `HelmReleaseProxy` 생성, `HelmReleaseReady` |
| `servicegateway` | `iksv2-servicegateway-controller-manager` | control plane network와 worker network 연결 | `KinxServiceGateway`, `ServiceGatewayClaim`, OpenStack network/router/static route | gateway/route 상태 |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-watcher` | Kubernetes 상태 감시와 annotation 동기화 | `Cluster`, `MachineDeployment`, `Machine`, workload `Service`/`PersistentVolume` | status, conditions, labels, annotations |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-processor` | lifecycle 조치 실행 | `Cluster`, `KamajiControlPlane`, `HelmRelease`, `MachineDeployment`, `ServiceGatewayClaim` | operation-state, cleanup, finalizer |

## 3. 자동 생성 리소스 관계

| 상위 리소스 | 자동 생성 하위 리소스 | 생성 주체 | 없을 때 판단 |
| --- | --- | --- | --- |
| `KamajiControlPlane` | `TenantControlPlane` | Kamaji provider | Kamaji provider 생성 대기 또는 실패 |
| `MachineDeployment` | `MachineSet` | CAPI controller | CAPI 전개 대기 또는 실패 |
| `MachineSet` | `Machine` | CAPI controller | replica 전개 대기 또는 실패 |
| `Machine` | `OpenStackMachine` | CAPO controller | OpenStackMachine 생성 대기 또는 실패 |
| `HelmChartProxy` | `HelmReleaseProxy` | CAAPH addon controller | addon 전개 대기 또는 실패 |

판정 원칙:

- 상위 리소스가 없으면 하위 리소스 부재를 별도 장애로 판단하지 않는다.
- 상위 리소스가 있고 5분 이상 하위 리소스가 없으면 해당 하위 리소스를 만드는 controller를 의심한다.
- 하위 리소스가 생긴 뒤에는 그 `status`/`conditions`/`finalizers`로 다음 단계를 판단한다.

## 4. 리소스 계층

```text
Namespace(<tenant-id>)
└── Cluster
    ├── OpenStackCluster                         # 인프라(network/router/SG/endpoint)
    ├── KamajiControlPlane                        # hosted control plane provider
    │   └── TenantControlPlane                    # 실제 control plane Pod/Deployment
    ├── Secret(<cluster-name>-cloud-conf 등)
    ├── HelmRelease(cloud-controller-manager, cluster-autoscaler)
    ├── HelmChartProxy
    │   └── HelmReleaseProxy(calico, cinder-csi, nfs-csi, manila-csi)
    └── MachineDeployment                          # node group
        ├── KubeadmConfigTemplate                  # worker join(kubeadm) 설정
        ├── OpenStackMachineTemplate
        ├── MachineHealthCheck
        └── MachineSet
            └── Machine
                └── OpenStackMachine               # VM/port/root volume
```

workload cluster 내부:

```text
Service(type=LoadBalancer) → OpenStack LoadBalancer
PersistentVolume(Cinder/Manila CSI) ── PersistentVolumeClaim, VolumeAttachment
```

service gateway:

```text
MachineDeployment → ServiceGatewayClaim → KinxServiceGateway
```

## 5. 공통 조회 명령

기본 변수:

```bash
NS=<tenant-id>
CLUSTER=<cluster-name>
MD=<machine-deployment-name>
```

1단계 — annotation/label/finalizer 스냅샷(가장 먼저):

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'
kubectl -n "$NS" get machinedeployment "$MD" \
  -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}{.metadata.finalizers}{"\n"}'
```

2단계 — condition/status 추적:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} message={.message}{"\n"}{end}'
kubectl -n "$NS" get machine -l cluster.x-k8s.io/cluster-name="$CLUSTER" \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}phase={.status.phase}{"\t"}providerID={.spec.providerID}{"\t"}nodeRef={.status.nodeRef.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} reason={.reason}{"\n"}{end}{end}'
```

3단계 — OpenStack/control plane 추론:

```bash
kubectl -n "$NS" get openstackcluster "$CLUSTER" -o yaml
kubectl -n "$NS" get openstackmachine -l cluster.x-k8s.io/cluster-name="$CLUSTER" -o yaml
kubectl -n "$NS" get tenantcontrolplane "$CLUSTER" \
  -o jsonpath='version={.status.kubernetesResources.version.status}{"\t"}endpoint={.status.controlPlaneEndpoint}{"\n"}'
kubectl -n "$NS" get helmreleaseproxy -l cluster.x-k8s.io/cluster-name="$CLUSTER" \
  -o custom-columns=NAME:.metadata.name,CHART:.spec.chartName,READY:'.status.conditions[?(@.type=="Ready")].status',REASON:'.status.conditions[?(@.type=="Ready")].reason',HELM:.status.status
```

## 6. 상태 신호 사전

### 6.1 Cluster condition (CAPI)

| condition | 의미 | 대표 reason |
| --- | --- | --- |
| `InfrastructureReady` | `OpenStackCluster` 준비됨 | `WaitingForInfrastructure` |
| `ControlPlaneReady` | control plane 준비됨 | `WaitingForControlPlane`, `WaitingForControlPlaneAvailable` |
| `Ready` | 위 조건 종합 | — |

### 6.2 Machine phase / condition (CAPI)

| `.status.phase` | 의미 |
| --- | --- |
| `Pending` | 처리 시작 전 |
| `Provisioning` | 인프라(VM) 생성 중 |
| `Provisioned` | VM 생성 완료, 아직 Node 미등록(`nodeRef` 없음) |
| `Running` | Node 합류, 정상 |
| `Deleting` / `Deleted` | 삭제 진행/완료 |
| `Failed` | 프로비저닝/조인 실패(`status.failureReason`/`failureMessage` 확인) |

| condition | 주요 reason | 의미 |
| --- | --- | --- |
| `BootstrapReady` | `WaitingForDataSecret` | `KubeadmConfig` bootstrap data secret 대기 |
| `InfrastructureReady` | `WaitingForInfrastructure` | `OpenStackMachine` 대기 |
| `NodeHealthy` | `WaitingForNodeRef` | `providerID` 미할당, Node 미등록 |
| `NodeHealthy` | `NodeProvisioning` | Node 등록 진행 중 |
| `NodeHealthy` | `NodeNotFound` | 등록됐던 Node가 사라짐 |
| `NodeHealthy` | `NodeConditionsFailed` | kubelet condition 실패(NotReady 등) |
| `HealthCheckSucceeded` | `NodeStartupTimeout` | MHC 시간 내 Node 미등장 |
| `HealthCheckSucceeded` | `UnhealthyNode` | MHC unhealthy 조건 충족 |
| `OwnerRemediated` | `RemediationInProgress`/`RemediationFailed` | MHC 재생성 진행/실패 |
| `DrainingSucceeded` | `Draining`/`DrainingFailed` | 삭제 시 node drain |
| `VolumeDetachSucceeded` | `WaitingForVolumeDetach` | 삭제 시 volume detach 대기 |

보조 필드: `.spec.providerID`(있으면 OpenStack instance 생성됨), `.status.nodeRef`(있으면 Node 등록=join 성공).

### 6.3 MachineDeployment phase (CAPI)

| `.status.phase` | 의미 |
| --- | --- |
| `ScalingUp` | desired보다 적어 증설 중 |
| `ScalingDown` | desired보다 많아 축소 중 |
| `Running` | desired와 일치, 안정 |
| `Failed` | 스케일링 실패 |

업그레이드 판정: 현재 MD version과 다른 old `MachineSet`이 replica ≥ 1로 남아 있으면 rolling upgrade 진행 중.

### 6.4 OpenStackCluster .status (인프라 추론)

세분 condition이 없는 구성이 많으므로, 어떤 `.status` 객체가 채워졌는지와 `.status.ready`/`.status.failureMessage`로 막힌 단계를 추론한다.

| `.status` 필드 | 채워지면 | 비어 있음(+`ready=false`) |
| --- | --- | --- |
| `network` / `externalNetwork` | network·subnet 준비됨 | network/subnet 단계에서 막힘 |
| `router` | router·게이트웨이 준비됨 | router 단계에서 막힘 |
| `controlPlaneSecurityGroup` / `workerSecurityGroup` | security group 준비됨 | SG 단계에서 막힘 |
| `apiServerLoadBalancer` | API endpoint(LB) 구성됨 | endpoint 단계에서 막힘 |

종료성 오류는 `.status.failureReason`/`.status.failureMessage`에 기록된다.

### 6.5 OpenStackMachine InstanceReady reason (VM 추론)

| `InstanceReady` reason | 의미 |
| --- | --- |
| `WaitingForClusterInfrastructure` | 인프라 준비 대기(정상 진행) |
| `WaitingForBootstrapData` | bootstrap data 미생성(→ kubeadm bootstrap) |
| `InvalidMachineSpec` | spec 오류(flavor/image/template 참조) |
| `InstanceCreateFailed` | instance 생성 실패(quota/flavor/image/port/volume) |
| `InstanceStateError` | instance가 ERROR 상태(예: No valid host) |
| `InstanceNotReady` | instance가 아직 pending 상태 |
| `InstanceNotFound` | instance를 찾을 수 없음(외부 삭제) |
| `InstanceDeleteFailed` | 삭제 실패 |
| `OpenStackError` | OpenStack API 통신/인증 오류 |

보조 condition: `APIServerIngressReady`(reason `LoadBalancerMemberError`/`FloatingIPError`), `FloatingAddressFromPoolReady`(reason `UnableToFindFloatingIPNetwork`/`WaitingForIPAMProvider`). 보조 필드: `.status.failureReason`, `.status.failureMessage`(OpenStack 원문), `.status.instanceState`.

### 6.6 TenantControlPlane version.status (control plane 추론)

`.status.kubernetesResources.version.status` 값:

| 값 | 의미 |
| --- | --- |
| `Provisioning` | control plane 기동 중 |
| `Ready` | 정상 |
| `NotReady` | control plane component 비정상 |
| `Upgrading` | Kubernetes 버전 업그레이드 중 |
| `CertificateAuthorityRotating` | CA 인증서 교체 중 |
| `Migrating` | datastore 마이그레이션 중 |
| `Sleeping` | replica 0(suspend 등으로 정지) |
| `WriteLimited` | datastore write 제한(용량/quorum) |
| `Unknown` | 판정 불가 |

보조 필드: `.status.controlPlaneEndpoint`(비어 있으면 endpoint 미구성), `.status.kubernetesResources.deployment`의 replicas/readyReplicas, `.status.storage.dataStoreName`, `.status.kubeconfig.admin.secretName`.

### 6.7 HelmChartProxy / HelmReleaseProxy condition (CAAPH)

| 리소스 | condition | reason | 의미 |
| --- | --- | --- | --- |
| `HelmChartProxy` | `HelmReleaseProxySpecsUpToDate` | `HelmReleaseProxySpecsUpdating` | 자식 HRP 생성/갱신 진행(정상) |
| `HelmChartProxy` | `HelmReleaseProxySpecsUpToDate` | `ValueParsingFailed` | `valuesTemplate` 렌더링 실패 |
| `HelmChartProxy` | `HelmReleaseProxySpecsUpToDate` | `ClusterSelectionFailed` | `clusterSelector`가 대상 못 고름 |
| `HelmChartProxy` | `HelmReleaseProxySpecsUpToDate` | `HelmReleaseProxyCreationFailed` | 자식 HRP 생성 실패 |
| `HelmChartProxy` | `HelmReleaseProxiesReady` | — | 자식 HRP 종합 준비 상태 |
| `HelmReleaseProxy` | `ClusterAvailable` | `WaitingForControlPlaneAvailable`(info) | control plane 미준비로 설치 대기 |
| `HelmReleaseProxy` | `ClusterAvailable` | `GetClusterFailed`/`GetKubeconfigFailed` | 워크로드 클러스터 접근 실패 |
| `HelmReleaseProxy` | `ClusterAvailable` | `GetCredentialsFailed`/`GetCACertificateFailed` | helm registry 자격/CA 문제 |
| `HelmReleaseProxy` | `HelmReleaseReady` | `PreparingToHelmInstall`(info) | 설치 준비 중 |
| `HelmReleaseProxy` | `HelmReleaseReady` | `HelmReleasePending`(info) | release가 pending 상태(`.status.status=pending-*`) |
| `HelmReleaseProxy` | `HelmReleaseReady` | `HelmInstallOrUpgradeFailed`(error) | install/upgrade 실패 |
| `HelmReleaseProxy` | `HelmReleaseReady` | `HelmReleaseGetFailed`(error) | release 조회 실패 |

보조 필드: `HelmReleaseProxy.status.status`(helm release 원상태), `.status.revision`. 옵션: `spec.options.wait`, `waitForJobs`, `timeout`(미설정 시 helm 기본 5분), `atomic`(wait 자동 활성 + 실패 시 rollback/uninstall), `skipCRDs`.

### 6.8 자체 서비스 annotation / label 마커

| 용도 | key / 값 |
| --- | --- |
| cluster name label | `cluster.x-k8s.io/cluster-name=<cluster-name>` |
| cluster ID / SG ID | `cluster.x-k8s.io.ew/cluster-id`, `cluster.x-k8s.io.ew/cluster-sg-id` |
| 최초 노드그룹 label | `cluster.x-k8s.io.ew/initial-deployment=true` |
| cluster 생성 동기화 | `cluster.x-k8s.io.ew/cluster-create-started`/`-completed`/`-failed=true` |
| 초기 배포 | `cluster.x-k8s.io.ew/initial-deployment-completed=true|false`, `…/initial-deployment-paused=true` |
| CAPI pause / watcher pause | `cluster.x-k8s.io/paused=true`, `cluster.x-k8s.io.ew/paused=true` |
| node group 동기화 | `cluster.x-k8s.io.ew/nodegroup-create`/`-scale`/`-upgrade`/`-delete-completed`/`-failed=true`, `…/nodegroup-upgrade-in-progress=true`, `…/nodegroup-paused=true` |
| node 동기화 | `cluster.x-k8s.io.ew/node-create-completed`, `…/node-delete-completed=true` |
| suspend/resume | `cluster.x-k8s.io.am/operation-state`(suspend/resume-requested/applied), `cluster.x-k8s.io.ep/last-suspended-done`, `…/last-resumed-done` |
| cluster 삭제 동기화 | `cluster.x-k8s.io.ew/cluster-delete-started`/`-completed`/`-failed=true` |
| workload 리소스 | `cluster.x-k8s.io.ew/resource-id`, `service.beta.kubernetes.io/kinx-load-balancer-id`, `resource-*-completed` |

### 6.9 OpenStack failureMessage 해석

| 원문 패턴 | 추정 |
| --- | --- |
| `No valid host was found` | 컴퓨트 용량 부족 또는 flavor/AZ 불일치 |
| `Quota exceeded for instances|cores|ram|ports|volumes|security_groups|floatingips` | 해당 quota 초과 |
| `Image ... could not be found` / `Flavor ... could not be found` | 이미지/flavor 미존재(템플릿 확인) |
| `Network|Subnet ... could not be found`, port 관련 | 네트워크/포트 자원 문제 |
| `401|403 Unauthorized|Forbidden` | cluster 전용 application credential 문제(`OpenStackError`) |

## 7. Cluster 생성과 인프라

### 7.1 생성 초기 이상 판정

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `OpenStackCluster`/`KamajiControlPlane`/`Cluster` 중 일부 없음 | 상위 선언 리소스 생성 전 또는 실패 | api-manager 로그 |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji provider 생성 대기 또는 실패 | `capi-kamaji-controller-manager`, `kamaji` 로그 |
| `Cluster.status.conditions` `InfrastructureReady=False` reason `WaitingForInfrastructure` 지속 | 인프라 프로비저닝 단계에서 막힘 | 7.2 |
| `Cluster` `InfrastructureReady=True`, `ControlPlaneReady=True`, `cluster-create-completed` 없음 | control plane 준비됨, 초기 노드그룹/addon 대기 | 9장 |
| `Cluster.status.failureReason`/`failureMessage` 존재 | 하위 실패가 전파됨 | 해당 하위 단계 |

### 7.2 인프라(OpenStackCluster) 단계 판정

`OpenStackCluster.status.ready=false`에서 어느 `.status` 객체가 비었는지로 막힌 단계를 나눈다. 공통으로 `.status.failureMessage`, Events, `capo-system` 로그를 본다.

| 관찰 | 판단 |
| --- | --- |
| `.status.network`/`externalNetwork` 비어 있음 | network/subnet 단계에서 막힘(외부 네트워크 미존재/CIDR 충돌/quota) |
| `.status.network` 있음, `.status.router` 비어 있음 | router/게이트웨이 단계에서 막힘 |
| `.status.controlPlaneSecurityGroup`/`workerSecurityGroup` 비어 있고 `cluster-sg-id` 미기록 | security group 단계에서 막힘(rule/quota) |
| `.status.apiServerLoadBalancer` 비어 있음 | API endpoint(LB/FIP) 구성 실패 → control plane 접근 경로 미구성 |
| `.status.failureMessage`에 `401/403`·endpoint 오류 또는 `OpenStackMachine` reason `OpenStackError` | OpenStack API 통신/인증 오류(application credential/endpoint) |

## 8. Control Plane (Kamaji)

control plane은 `KamajiControlPlane`(provider)과 하위 `TenantControlPlane`(실제 Pod/Deployment)으로 동작한다. 판정은 `TenantControlPlane.status.kubernetesResources.version.status`, control plane Deployment replica, `controlPlaneEndpoint`로 한다.

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji provider가 하위 미생성 | `capi-kamaji-controller-manager` 로그 |
| `version.status=Provisioning` 지속 | control plane Pod 기동 중/멈춤 | deployment replicas, control plane Pod 상태(스케줄/이미지/datastore 대기) |
| `version.status=NotReady` | control plane component 비정상 | apiserver 등 Pod CrashLoop/오류, 인증서·datastore 접근 |
| `version.status=WriteLimited` | datastore write 제한(용량/quorum) | `.status.storage.dataStoreName`과 datastore Pod |
| `.status.controlPlaneEndpoint` 비어 있음 | API server 노출 경로 미구성 | `OpenStackCluster.status.apiServerLoadBalancer`(7.2), KamajiControlPlane service 설정 — worker join에도 영향(11.2) |
| `version.status=Upgrading` 지속 | control plane 업그레이드 멈춤 | deployment updated/ready replica, rollout |
| `version.status=CertificateAuthorityRotating`/`Migrating` 지속 | CA 교체/datastore 마이그레이션 미완료 | `kamaji-system` 로그 |

## 9. 초기 배포 단계

초기 배포 성공 = control plane `ControlPlaneReady=True` + 초기 노드그룹(`initial-deployment=true` label) 1개 Available + addon(`HelmReleaseProxy`/`HelmRelease`) Ready.

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `cluster-create-started`만 있고 완료/실패 마킹 없음 | 진행 중 또는 멈춤 | 세 구성요소 중 미완료 지점 추적 |
| `initial-deployment-completed=true` | 성공 | 정상 |
| `initial-deployment-completed=false` + `initial-deployment-paused=true` + `paused` + 모든 `HelmRelease.spec.suspend=true` | 허용 시간 내 미완료로 실패 확정, processor가 control plane cleanup 진입 | 미완료 구성요소: control plane(8장)/초기 노드그룹(10·11장)/addon(12장) |
| control plane 미완료 | `version.status`가 `Provisioning`/`NotReady` | 8장 |
| 초기 노드그룹 미완료 | `initial-deployment=true` MD Available=false, 하위 Machine | 10·11장 |

## 10. NodeGroup 전개 / scale / upgrade

### 10.1 전개 계층 판정

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `MachineDeployment` 없음 | 선언 전 또는 삭제됨 | api-manager 로그 |
| `MachineDeployment` 있음, `MachineSet` 없음 | CAPI 전개 전/실패 | `capi-controller-manager` 로그 |
| `MachineSet` 있음, `Machine` 없음 | replica 전개 전 또는 template 문제 | MachineSet status, template |
| `Machine` 있음, `OpenStackMachine` 없음 | CAPO 전개 전/실패 | `capo-controller-manager` 로그 |
| `Machine` `InfrastructureReady=False`, providerID 비어 있음 | VM 생성 단계 | 11.1 |

### 10.2 scale

| 관찰 | 판단 |
| --- | --- |
| `last-scaled` 있음, `nodegroup-scale-completed` 없음, `phase=ScalingUp`, desired>current 지속 | 신규 Machine 생성 실패(11.1) 또는 신규 노드 join 실패(11.2) |
| `phase=ScalingDown` 또는 Running인데 desired<current | 축소 중 — 삭제 대상 Machine drain/detach(15.3) |
| `nodegroup-scale-failed=true` + pause | scale timeout 실패 확정 |

### 10.3 upgrade

| 관찰 | 판단 |
| --- | --- |
| `nodegroup-upgrade-in-progress=true` 지속, old `MachineSet` replica 유지, 신규 Machine이 Running 못 됨 | 새 template(이미지) 문제(11.1) 또는 신규 노드 join 실패(11.2) |
| `nodegroup-upgrade-failed=true` + pause | upgrade timeout 실패 확정 |

## 11. Machine 상태와 join 실패

### 11.1 VM(OpenStackMachine) 생성 실패

`Machine` `InfrastructureReady=False` + providerID 비어 있을 때 `OpenStackMachine`의 `InstanceReady` reason으로 좁힌다(6.5). failureMessage 해석은 6.9.

| 관찰(reason) | 판단 |
| --- | --- |
| `WaitingForClusterInfrastructure` | 인프라 대기(정상 진행). 오래 막히면 7장 |
| `WaitingForBootstrapData` | bootstrap data 미생성 → `capi-kubeadm-bootstrap-system` |
| `InvalidMachineSpec` | template의 flavor/image/네트워크 참조 오류 |
| `InstanceCreateFailed` | 생성 실패 → failureMessage로 quota/flavor/image/port/volume 좁힘. 일부 Machine만 실패면 quota/용량 우선 |
| `InstanceStateError` | instance ERROR(컴퓨트 용량 부족 등) |
| `OpenStackError` | API 통신/인증 오류(credential/endpoint) |
| `InstanceNotFound` | 외부에서 instance 삭제됨 |

### 11.2 ★ join 실패 (VM은 떴는데 노드가 동작 안 함)

핵심 전제: `Machine.spec.providerID`가 채워졌다 = OpenStack instance가 정상 생성됐다. 이후에도 노드가 동작하지 않으면 VM 문제가 아니라 kubelet/join 문제로 추정하고, 최종적으로 "노드가 join되지 않은 것"으로 간주한다. 분기 기준은 `Machine.status.nodeRef`(또는 워크로드 클러스터 Node 객체) 존재 여부다.

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| providerID 있음, `phase=Provisioned`, `NodeHealthy=False` reason `WaitingForNodeRef`/`NodeProvisioning`, `nodeRef` 없음 | join 실패(공통) | 아래 세 분기 |
| 위 + `BootstrapReady=False` reason `WaitingForDataSecret`, `KubeadmConfig.status.dataSecretName` 비어 있음 | bootstrap data 문제 | `capi-kubeadm-bootstrap-system` 로그 |
| 위 + `BootstrapReady=True`인데 `nodeRef` 미생성 | control plane endpoint 도달 불가 | `TenantControlPlane.status.controlPlaneEndpoint`, `KinxServiceGateway`/라우팅/SG/FIP, 노드 콘솔 `kubeadm join` 로그 |
| 위 + endpoint/네트워크 정상인데 join 안 됨 | cloud-init/kubelet 구성 또는 join token 만료 | VM 콘솔/시리얼·`cloud-init`·`kubelet` 로그(사용자 직접 확인) |
| `nodeRef` 있음(=join 성공) + `NodeHealthy=False` reason `NodeConditionsFailed` | NotReady — 대개 CNI(calico) 미배포 | calico `HelmReleaseProxy`(12장), 워크로드 calico Pod |

### 11.3 MachineHealthCheck / remediation

| 관찰 | 판단 |
| --- | --- |
| `HealthCheckSucceeded=False` reason `NodeStartupTimeout` | 시간 내 Node 미등장 — join 실패(11.2)의 결과로 나타나기 쉬움 |
| `HealthCheckSucceeded=False` reason `UnhealthyNode` | MHC unhealthy 조건 충족 |
| `OwnerRemediated` reason `RemediationInProgress`/`RemediationFailed` 반복, Machine 재생성 루프 | 근본 원인(join 실패/CNI 미배포) 미해소로 MHC가 노드를 계속 지우고 재생성 — 근본 원인 먼저 해결 |

## 12. addon (CAAPH)

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | `HelmReleaseProxySpecsUpToDate` reason 확인: `ValueParsingFailed`/`ClusterSelectionFailed`/`HelmReleaseProxyCreationFailed`/(진행)`...Updating` | `caaph-controller-manager` 로그 |
| calico `HelmReleaseProxy` `HelmReleaseReady=False` | CNI 미배포 → 노드 NotReady(11.2), 초기 배포 실패(9장) 유발 | reason과 `.status.status`, 워크로드 calico Pod |
| `HelmReleaseReady=False` reason `HelmReleasePending`, `.status.status=pending-install`/`pending-upgrade` | wait 블로킹 또는 잔여 pending(아래) | `caaph-system` 로그 |
| addon이 결국 설치되나 전반적으로 느림, HRP가 한참 not-ready | `options.wait`/`atomic`로 install/upgrade가 리소스 Ready까지 동기 블록(미설정 timeout 5분). CNI 전후 상호 대기로 매 reconcile이 timeout 소진 → 초기 배포 timeout(9장) 유발 | `options.wait`/`atomic`/`timeout` 설정, wait 타임라인 |
| pending이 자동으로 안 풀림, "another operation in progress" | 컨트롤러 재시작/데드라인으로 release가 `pending-*`에 잔류 | 잔여 pending 해소(rollback/cleanup)는 영향 큼 → 사용자 확인 후 |
| `HelmReleaseReady=False` reason `HelmInstallOrUpgradeFailed` | values/chart/CRD/소유권 충돌. `atomic`이면 rollback→reinstall 루프(`HelmReleaseProxyReinstalling`) | `caaph-system` 로그 helm 오류 원문 |
| `ClusterAvailable=False` reason `WaitingForControlPlaneAvailable` | CP 미준비로 설치 대기(정상). CP가 안 뜨면 8장 | — |
| `ClusterAvailable=False` reason `GetKubeconfigFailed`/`GetClusterFailed` | 워크로드 접근 실패 | control plane endpoint(8·11.2), `<cluster>-kubeconfig` Secret |
| CSI/CCM addon Ready=false | cinder/manila CSI→PV(14.2), cloud-controller-manager→LB Service(14.1) | 해당 HRP/HelmRelease, 워크로드 addon Pod |

## 13. suspend / resume

| 관찰 | 판단 |
| --- | --- |
| `operation-state=suspend-requested` 유지(전이 없음) | `event-processor` reconcile 미수행/에러. 비정상 조합(13.1) 확인 |
| `operation-state=suspend-applied`인데 안 끝남 | `KamajiControlPlane.spec.replicas=0`/component replica가 0으로 안 내려감, `version.status=Sleeping` 미도달 → `last-suspended-done` 미기록 |
| `operation-state=resume-requested`/`resume-applied`에서 멈춤 | resume 목표(KCP replicas=3, component=1, `HelmRelease.spec.suspend=false`, paused 해제, `version.status=Ready`, `last-resumed-done`) 미충족 |

### 13.1 비정상 operation 조합

| 조건 | 판단 |
| --- | --- |
| `operation-state` 없음인데 `last-suspended-done`과 `last-resumed-done` 둘 다 존재 | 비정상 |
| `operation-state=suspend-requested`인데 `last-suspended-done` 존재 | 비정상 |
| `operation-state=resume-requested`인데 `last-suspended-done` 없음 | 비정상 |
| `suspend-applied`/`resume-applied`인데 done marker 존재 | 비정상 |

## 14. workload 리소스 (LoadBalancer / PV)

> 워크로드 클러스터 내부 리소스는 자체 컨트롤러가 root cluster와 연결된 뒤에만 관찰된다. 연결 실패 시 root cluster만으로는 완전 판정 불가(원인은 control plane endpoint/kubeconfig — 8·11.2).

### 14.1 Service LoadBalancer

| 관찰 | 판단 |
| --- | --- |
| `service.beta.kubernetes.io/kinx-load-balancer-id` 없음 | LB 생성 중 — 오래면 CCM(12장) 또는 LB quota |
| OpenStack LB `PENDING_CREATE`/`PENDING_UPDATE` | 생성/갱신 중 |
| OpenStack LB `ACTIVE` | 정상 |
| OpenStack LB `ERROR` | OpenStack LB 오류 |

### 14.2 PersistentVolume

| 관찰 | 판단 |
| --- | --- |
| PV phase가 `Available`/`Bound`로 못 감, CSI driver=cinder/manila | 생성 단계 — cinder/manila CSI(12장) 또는 volume quota/AZ/type |
| `VolumeAttachment.status.attached`가 true 안 됨 | attach 단계 멈춤 |

## 15. 삭제

### 15.1 Cluster 삭제 완료 조건

| 관찰 | 판단 |
| --- | --- |
| `Cluster.metadata.deletionTimestamp` 존재 | 삭제 진행(`cluster-delete-started=true`) |
| cluster label `HelmRelease`/`HelmChartProxy` 또는 같은 이름 `Ingress` 잔존 | watcher가 삭제 완료로 보지 않음 |
| CAPI `ClusterFinalizer` 잔존 | CAPI cleanup 대기 |
| 위가 모두 없고 finalizer 제거됨 | 삭제 완료(`cluster-delete-completed=true`) |
| `cluster-delete-failed=true` + pause | 삭제 timeout 실패 |

### 15.2 ★ Cluster가 Deleting 상태로 장기 지속 — 멈춘 계층 찾기

삭제는 위에서 아래로(addon → worker → control plane → infra → 외부 cleanup → finalizer 제거) 진행되므로, 남아 있는 가장 바깥 계층이 멈춘 지점이다.

| 순서 | 확인 | 남아 있으면 멈춘 지점 |
| --- | --- | --- |
| 1 | `cluster.x-k8s.io/paused` annotation | paused면 CAPI가 삭제를 reconcile하지 않음(15.4) |
| 2 | cluster label `HelmRelease`/`HelmChartProxy`/같은 이름 `Ingress` | addon 정리 전(watcher가 삭제 진행 유지) |
| 3 | `MachineDeployment`/`MachineSet` | worker 계층 삭제 중(15.5) |
| 4 | `Machine` | drain/detach/VM 삭제 멈춤(15.3) 또는 `OpenStackMachine` 삭제 실패(아래) |
| 5 | `KamajiControlPlane`/`TenantControlPlane` | Kamaji가 control plane finalizer 미제거 |
| 6 | `OpenStackCluster` | CAPO가 router/port/SG 등 인프라 정리 차단 |
| 7 | 위가 모두 없는데 `Cluster.metadata.finalizers` 잔존 | finalizer 미제거(15.6) |

빠른 명령:

```bash
kubectl -n "$NS" get cluster "$CLUSTER" \
  -o jsonpath='paused={.metadata.annotations.cluster\.x-k8s\.io/paused}{"\n"}finalizers={.metadata.finalizers}{"\n"}deletionTimestamp={.metadata.deletionTimestamp}{"\n"}'
kubectl -n "$NS" get helmrelease,helmchartproxy,helmreleaseproxy,machinedeployment,machineset,machine,openstackmachine,kamajicontrolplane,tenantcontrolplane,openstackcluster \
  -l cluster.x-k8s.io/cluster-name="$CLUSTER" 2>/dev/null
kubectl -n "$NS" get ingress "$CLUSTER" 2>/dev/null
```

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `Machine` `Deleting`인데 `OpenStackMachine` `InstanceReady=False` reason `InstanceDeleteFailed`/`OpenStackError` | OpenStack instance 삭제 거부(외부 의존/port·volume 분리 실패) | `capo-system` 로그, failureMessage |
| `OpenStackMachine` reason `InstanceNotFound` | VM은 이미 사라지고 finalizer만 남음(곧 정리) | — |

### 15.3 Machine 삭제 멈춤

| 관찰 | 판단 |
| --- | --- |
| `DrainingSucceeded` reason `Draining`/`DrainingFailed` | node drain 대상 워크로드가 안 빠짐 |
| `VolumeDetachSucceeded` reason `WaitingForVolumeDetach` | OpenStack volume detach 지연 |
| `MachineFinalizer` 제거됨 | 삭제 완료 |

### 15.4 paused로 인해 삭제가 진행되지 않음 (자주 발생)

| 관찰 | 판단 | 다음 확인 |
| --- | --- | --- |
| `cluster.x-k8s.io/paused=true`(또는 `…ew/paused`)인데 `deletionTimestamp` 존재 | CAPI가 paused cluster를 reconcile하지 않아 하위 삭제 미시작. 초기 배포 실패/suspend 후 삭제 요청 시 발생 | 잔존 하위 리소스 condition이 변화 없음. pause 제거 필요 여부를 `event-processor`/`event-watcher` 로그로 확인하고 무엇이 pause를 걸었는지 근거 제시 — 해제는 영향 크므로 사용자 확인 후 |

### 15.5 NodeGroup 삭제

| 관찰 | 판단 |
| --- | --- |
| `MachineDeployment.deletionTimestamp` 존재 | 삭제 진행 |
| `nodegroup-delete-failed=true` | 삭제 timeout 실패 |
| `MachineDeploymentFinalizer` 제거됨 | 삭제 완료 |

### 15.6 외부 자원 cleanup 단계 (event-processor)

| 관찰 | 판단 |
| --- | --- |
| `Cluster.metadata.finalizers`에 `event-processor` 외 다른 finalizer 잔존 | 외부 cleanup 시작 안 함(다른 컨트롤러 대기) |
| `event-processor` finalizer만 남음 | OpenStack LB/PV metadata, cluster credential(`<cluster>-cloud-conf`의 keypair/user/secret), `ServiceGatewayClaim` 정리 중/실패 → `event-processor` 로그 |

## 16. 상태별 빠른 판정 시나리오

### 16.1 cluster 생성이 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `OpenStackCluster` 없음 | api-manager 생성 단계 |
| `KamajiControlPlane` 있음, `TenantControlPlane` 없음 | Kamaji provider |
| `InfrastructureReady=False` 지속 | CAPO / OpenStack infra(7.2) |
| `ControlPlaneReady=False`, `version.status=Provisioning/NotReady` | Kamaji control plane(8장) |
| `HelmChartProxy` 있음, `HelmReleaseProxy` 없음 | CAAPH addon controller |
| 최초 node group MD 있음, `MachineSet` 없음 | CAPI controller |

### 16.2 노드가 동작 안 하는 경우 (★)

| 관찰 | 의심 지점 |
| --- | --- |
| providerID 없음, `InstanceReady=False` | VM 생성 실패(11.1) |
| providerID 있음, `phase=Provisioned`, `nodeRef` 없음, `BootstrapReady=False` | bootstrap data(11.2) |
| providerID 있음, `nodeRef` 없음, `BootstrapReady=True` | control plane endpoint 도달 불가(11.2) — servicegateway/SG/FIP |
| `nodeRef` 있음, `NodeHealthy=False` reason `NodeConditionsFailed` | CNI(calico) 미배포(12장) |
| `OwnerRemediated` 반복, Machine 재생성 루프 | MHC remediation — 근본 원인 미해소(11.3) |

### 16.3 addon이 멈춘/느린 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `HelmReleaseProxy` 없음, HCP reason `ValueParsingFailed`/`ClusterSelectionFailed` | values 템플릿/selector |
| `HelmReleaseReady` reason `HelmReleasePending`, `.status.status=pending-*` | wait 블로킹 또는 잔여 pending(12장) |
| `HelmReleaseReady` reason `HelmInstallOrUpgradeFailed` | helm install/upgrade 실패 |
| `ClusterAvailable=False` | control plane/kubeconfig/registry(12장) |

### 16.4 suspend/resume이 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `operation-state=suspend-requested`/`resume-requested` 유지 | event-processor 미처리 |
| `suspend-applied`, KCP `readyReplicas != 0` | control plane scale down 지연 |
| `resume-applied`, KCP `readyReplicas != 3` | control plane scale up 지연 |
| done marker 없음 | 완료 전 |

### 16.5 cluster 삭제가 멈춘 경우

| 관찰 | 의심 지점 |
| --- | --- |
| `cluster.x-k8s.io/paused=true` + `deletionTimestamp` | CAPI reconcile 중단(15.4) |
| `HelmRelease`/`HelmChartProxy`/같은 이름 `Ingress` 남음 | watcher가 삭제 완료로 안 봄 |
| `Machine` 남고 `OpenStackMachine` `InstanceDeleteFailed` | VM 삭제 거부(15.2) |
| `TenantControlPlane`/`OpenStackCluster` 남음 | Kamaji/CAPO finalizer 미제거 |
| event-processor 외 finalizer 남음 | processor cleanup 시작 전 |
| `event-processor` finalizer만 남음 | 외부 cleanup 중/실패(15.6) |

## 17. 사용자에게 로그 확인을 지시하는 방법

로그 도구가 없으므로 다음 형식으로 사용자에게 직접 실행을 요청한다(실행했다고 말하지 않는다): "로그 확인 도구가 없어 직접 실행이 필요합니다. 아래 명령을 실행하고 결과를 알려주세요."

자체 서비스 컨트롤러:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher --since=30m | grep "$CLUSTER"
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor --since=30m | grep "$CLUSTER" | grep -i error
```

업스트림 컨트롤러(막힌 단계별):

| 막힌 단계 | namespace / deploy |
| --- | --- |
| 인프라(network/router/SG/endpoint), VM(instance/port/volume) | `capo-system` / `capo-controller-manager` |
| Machine/MachineSet 전개 | `capi-system` / `capi-controller-manager` |
| bootstrap data / join 설정 | `capi-kubeadm-bootstrap-system` / `capi-kubeadm-bootstrap-controller-manager` |
| control plane(TenantControlPlane) | `kamaji-system` / `capi-kamaji-controller-manager`, `kamaji` |
| addon(calico/csi/ccm) | `caaph-system` / `caaph-controller-manager` |
| worker↔control plane 네트워크 | `servicegateway` / `iksv2-servicegateway-controller-manager` |

리소스 Events / 노드 내부:

```bash
kubectl -n "$NS" describe openstackcluster "$CLUSTER"
kubectl -n "$NS" describe openstackmachine <openstackmachine-name>
kubectl -n "$NS" describe machine <machine-name>
kubectl -n "$NS" describe tenantcontrolplane "$CLUSTER"
```

join 실패 의심 시 VM 콘솔/시리얼 로그, `cloud-init`·`kubelet`·`kubeadm join` 결과를 노드에서 직접 확인하도록 안내한다(management cluster에서 워커 OS 로그를 가져오는 도구는 없다).

## 18. 최종 판정 원칙

1. annotation/label로 "시스템이 마킹한 단계"를 먼저 확정하고, 그다음 `conditions`/`status`로 실제 상태와의 간극을 찾는다.
2. DB는 사용하지 않는다. control plane은 `TenantControlPlane.status...version.status`, node group은 `MachineDeployment.status.phase`, node는 `Machine.status`(phase/conditions/nodeRef/providerID)로 판정한다.
3. OpenStack은 직접 조회하지 않고 `OpenStackCluster.status`(ready/필드/failureMessage)와 `OpenStackMachine`의 `InstanceReady` reason으로 추론한다. 오류가 없으면 정상 생성으로 간주한다.
4. `Machine.spec.providerID`가 있으면 VM은 떴다고 보고, 동작 이상은 `nodeRef` 유무로 join 실패와 CNI 미배포를 구분한다.
5. addon은 `HelmReleaseReady` reason과 `.status.status`로 진행(`HelmReleasePending`)과 실패(`HelmInstallOrUpgradeFailed`)를 구분하고, wait/atomic로 인한 지연 가능성을 함께 본다.
6. 삭제는 `deletionTimestamp`만으로 판단하지 않는다. paused 여부, 남은 계층, finalizer 제거 여부를 함께 본다.
7. annotation·condition만으로 확정되지 않으면 사용자에게 로그 확인을 지시한다. 컨트롤러 로그는 cluster name으로 먼저 좁힌 뒤 에러를 확인한다.
