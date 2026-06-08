# api-manager

## 0. 문서 목적과 범위

- 목적: AI/RAG가 `api-manager`의 실제 동작을 검색하고 재사용할 수 있도록 서비스 내부 플로우를 원자적으로 기록한다.
- 기준 코드 범위: `cmd/api-manager/service/iks`
- Kubernetes 리소스 조사 범위: `cmd/api-manager/service/iks`에서 직접 사용하는 리소스만 포함한다.
- 비범위:
  - HTTP controller의 바인딩/응답 처리
  - event-watcher / event-processor 내부 구현
  - `service/iks` 밖에서만 사용되는 Kubernetes 리소스
- 해석 규칙:
  - 본문에서 `status`, `internal_status`, `initial_deployment_status`는 실제 코드값을 우선 표기한다.
  - 서비스 섹션의 각 메서드 제목은 RAG chunk 단독 검색을 위해 `Service.Method` 형식으로 표기한다.
  - helper가 존재하지만 현재 호출되지 않는 경우, “현재 실행 경로에서 호출되지 않음”을 명시한다.

## 1. 빠른 검색 인덱스

| 검색 대상 | 관련 섹션 | 핵심 키워드 |
| --- | --- | --- |
| tenant namespace | `TenantService` | `Namespace`, tenant |
| cluster + 최초 nodegroup 생성 | `ClusterResourceService.Create` | `Cluster`, `OpenStackCluster`, `KamajiControlPlane`, `HelmRelease`, `HelmChartProxy`, `MachineDeployment` |
| control plane 생성 | `ControlPlaneService.Create` | `CP-ST-PRV`, `CP-IST-ST`, OpenStack user, cloud config `Secret` |
| control plane 삭제 | `ControlPlaneService.Delete` | `CP-IST-ST`, `HelmReleaseProxy`, finalizer |
| suspend / resume | `ControlPlaneService.Suspend`, `ControlPlaneService.Resume` | `CP-ST-SUS`, `operation-state`, `suspended` |
| nodegroup 생성 | `DataPlaneService.Create` | `DP-ST-PRV`, `DP-IST-ST`, keypair copy, `OpenStackMachineTemplate` |
| nodegroup upgrade | `DataPlaneService.Upgrade` | `CP-ST-AV`, `DP-ST-AV`, `CP-ST-CF`, `DP-IST-UGF` |
| autoscaler | `AutoScalerService` | `MachineDeployment` annotations |
| dashboard | `DashboardService` | workload cluster, `resource.Getter`, GVR |
| 상태 코드 | `문서에서 사용하는 상태 코드` | `CP-ST-*`, `DP-ST-*`, `ID-ST-*` |
| Kubernetes 리소스 | `Kubernetes 리소스` | CRD, management cluster, workload cluster |

## 2. 역할

`api-manager`는 IKS v2의 외부 API 서버다. 사용자의 HTTP 요청을 받아 인증/검증을 수행하고, DB 상태와 Kubernetes 관리 클러스터의 리소스를 함께 조작한다.

- 실행 위치: Kubernetes 상의 독립 애플리케이션
- 기본 API prefix: `/v2/iks`
- 주요 책임
  - 클러스터, 노드그룹, 오토스케일러, 대시보드, 공통코드, 버전 조회 API 제공
  - 테넌트별 Kubernetes namespace 보장
  - Cluster API, Kamaji, CAPO, Flux, CAAH 계열 리소스 생성/수정/삭제
  - OpenStack 사용자와 application credential 생성
  - DB의 클러스터/노드그룹/오토스케일러 상태 저장
  - event-watcher가 후속 상태 동기화를 수행할 수 있도록 label/annotation을 심어 둠

## 3. 주요 디렉토리

```text
./cmd/api-manager
├── app                  # 의존성 조립과 애플리케이션 부트스트랩
├── auth                 # 인증 관련 도우미
├── configs              # 서버, DB, 클라이언트, 인프라, Helm, Swagger 설정
├── controller           # HTTP 요청 컨트롤러 레이어
├── docs                 # Swagger 산출물
├── errors               # 컨트롤러 공통 에러 정의
├── middleware           # 인증, 로깅, Sentry 미들웨어
├── models
│   ├── domain           # DB 도메인 모델
│   └── dto              # handler / iks / k8sclient / mapper DTO
├── pkg/resource         # 워크로드 클러스터 리소스 조회용 getter/provider
├── repository           # DB 접근 계층
├── server               # HTTP 서버와 healthcheck 서버
├── service
│   ├── iks              # 핵심 비즈니스 로직
│   ├── k8sclient        # Kubernetes 리소스 CRUD 래퍼
│   └── openstackclient  # OpenStack 연동 래퍼
├── test                 # controller/service/repository 단위 테스트
└── utils                # 설정, 검증, 템플릿, 공통 유틸리티
```

## 4. 서비스 계층 전체 동작 플로우

이 문서는 HTTP 처리 절차가 아니라 `cmd/api-manager/service/iks` 내부에서 실제로 수행되는 동작을 기준으로 정리한다.

### 4.1 문서에서 사용하는 상태 코드

#### Control Plane 상태

| 구분 | 코드 | 의미 |
| --- | --- | --- |
| status | `CP-ST-PRV` | provisioning |
| status | `CP-ST-CF` | configuring |
| status | `CP-ST-AV` | available |
| status | `CP-ST-UAV` | unavailable |
| status | `CP-ST-SUS` | suspended |
| status | `CP-ST-DL` | deleting |
| status | `CP-ST-DLT` | deleted |
| status | `CP-ST-DLF` | deletion failed |
| status | `CP-ST-F` | failed |
| status | `CP-ST-UN` | unknown |
| internal_status | `CP-IST-ST` | start |
| internal_status | `CP-IST-APD` | applied |
| internal_status | `CP-IST-INF-PRV` | provisioning infra |
| internal_status | `CP-IST-TCP-PRV` | provisioning control plane |
| internal_status | `CP-IST-TCP-UG` | upgrading control plane |
| internal_status | `CP-IST-TCP-DL` | deleting control plane |
| internal_status | `CP-IST-INF-DL` | deleting infra |
| internal_status | `CP-IST-NONE` | none |
| internal_status | `CP-IST-UN` | unknown |

#### Data Plane 상태

| 구분 | 코드 | 의미 |
| --- | --- | --- |
| status | `DP-ST-PRV` | provisioning |
| status | `DP-ST-CF` | configuring |
| status | `DP-ST-AV` | available |
| status | `DP-ST-SU` | scale up |
| status | `DP-ST-SD` | scale down |
| status | `DP-ST-DL` | deleting |
| status | `DP-ST-DLT` | deleted |
| status | `DP-ST-DLF` | delete failed |
| status | `DP-ST-CRF` | create failed |
| status | `DP-ST-SF` | scale failed |
| status | `DP-ST-UF` | upgrade failed |
| status | `DP-ST-F` | failed |
| status | `DP-ST-UN` | unknown |
| internal_status | `DP-IST-ST` | start |
| internal_status | `DP-IST-APD` | applied |
| internal_status | `DP-IST-DP-PRV` | provisioning data plane |
| internal_status | `DP-IST-DP-UAV` | unavailable data plane |
| internal_status | `DP-IST-DP-DL` | deleting data plane |
| internal_status | `DP-IST-DP-UG` | upgrading data plane |
| internal_status | `DP-IST-NONE` | none |
| internal_status | `DP-IST-UGF` | upgrade failed |
| internal_status | `DP-IST-UN` | unknown |

#### Initial Deployment 상태

| 구분 | 코드 | 의미 |
| --- | --- | --- |
| status | `ID-ST-PRV` | provisioning |
| status | `ID-ST-SC` | success |
| status | `ID-ST-ERR` | error |
| status | `ID-ST-CC` | cancel |
| status | `ID-ST-NO` | no init |

### 4.2 TenantService

책임: tenant와 Kubernetes `Namespace`를 연결한다.

소스 파일: `cmd/api-manager/service/iks/tenant.go`

주요 저장소: 없음

주요 Kubernetes 리소스: `Namespace`

#### `TenantService.Get`

1. tenant ID를 namespace 이름으로 사용해 `Namespace`를 조회한다.
2. namespace 이름을 tenant DTO로 변환해 반환한다.

#### `TenantService.Create`

1. tenant ID 이름으로 `Namespace`를 생성한다.

#### `TenantService.List`

1. 현재 구현은 빈 목록을 그대로 반환한다.

#### `TenantService.Delete`

1. tenant ID 이름의 `Namespace`를 삭제한다.

### 4.3 ClusterResourceService

책임: control plane과 최초 data plane을 하나의 생성 흐름으로 묶는다.

소스 파일: `cmd/api-manager/service/iks/clusterresource.go`

주요 저장소: cluster, nodegroup

주요 Kubernetes 리소스: `OpenStackCluster`, `KamajiControlPlane`, `Cluster`, `HelmRelease`, `HelmChartProxy`, `KubeadmConfigTemplate`, `OpenStackMachineTemplate`, `MachineDeployment`, `MachineHealthCheck`

#### `ClusterResourceService.Create`

1. DB에 control plane용 cluster row를 먼저 만든다.
   - `status=CP-ST-PRV`
   - `internal_status=CP-IST-ST`
   - replicas, CIDR, endpoint, version, CNI 저장
2. cluster 전용 OpenStack 사용자를 만든다.
3. 그 사용자로 application credential을 만든다.
4. 관리 클러스터에 cloud config `Secret` 2개를 만든다.
   - CCM용 password 인증 `cloud.conf`
   - CAPI용 application credential 인증 `clouds.yaml`
5. tenant namespace의 `default` `ServiceAccount`에 image pull secret을 추가한다.
6. control plane 선언 리소스를 순서대로 만든다.
   - `OpenStackCluster`
   - `KamajiControlPlane`
   - `Cluster`
7. addon 배포 선언 리소스를 만든다.
   - `HelmRelease`: cloud-controller-manager, cluster-autoscaler
   - `HelmChartProxy`: Calico, Cinder CSI, NFS CSI, Manila CSI
8. 앞서 만든 두 `Secret`에 `Cluster` ownerReference를 patch한다.
9. DB cluster `internal_status`를 `CP-IST-APD`로 바꾼다.
10. 생성된 cluster ID와 Kubernetes version을 최초 nodegroup 요청에 주입한다.
11. 최초 배포 식별용 label `InitialDeploymentLabel=true`를 nodegroup label에 추가한다.
12. 최초 nodegroup 생성 작업을 수행한다.
    - cluster를 조회한다.
    - nodegroup version을 cluster version과 같게 맞춘다.
    - 현재 사용자 keypair를 조회하고 cluster 전용 credential로 같은 keypair를 복사한다.
    - DB에 `status=DP-ST-PRV`, `internal_status=DP-IST-ST`인 nodegroup row를 저장한다.
    - `KubeadmConfigTemplate`, `OpenStackMachineTemplate`, `MachineDeployment`를 만든다.
    - auto-healer가 활성화되어 있으면 `MachineHealthCheck`도 만든다.
    - DB nodegroup `internal_status`를 `DP-IST-APD`로 바꾼다.
13. 최초 nodegroup 생성이 실패해도 cluster 생성 결과는 실패 처리하지 않고 로그만 남긴다.
14. 최종 반환값은 control plane ID다.

#### `ClusterResourceService.Delete`

1. DB에서 cluster를 조회한다.
2. cluster `internal_status=CP-IST-ST`이면
   - `KamajiControlPlane` 삭제
   - `OpenStackCluster` 삭제
   - DB transaction으로 cluster row 삭제
3. cluster `internal_status!=CP-IST-ST`이면
   - `Cluster`의 paused annotation을 제거
   - `Cluster` 삭제
4. 후처리로 addon 관련 리소스를 정리한다.
   - cluster ID 기반 이름의 `HelmChartProxy` 4개 삭제
   - cluster-name label을 가진 `HelmReleaseProxy` 목록 조회
   - 각 `HelmReleaseProxy` 삭제 요청
   - 각 `HelmReleaseProxy` finalizer 제거
   - cloud-controller-manager와 cluster-autoscaler `HelmRelease` 삭제
5. 개별 data plane 삭제를 직접 수행하지는 않는다.

#### `ClusterResourceService.DeleteAllClusters`

1. tenant 기준으로 cluster 목록을 조회한다.
2. `onlySuspend=true`면 DB `suspended=true`인 cluster만 필터링한다.
3. 각 cluster마다 다음 삭제 절차를 순차 수행한다.
   - DB에서 cluster를 조회한다.
   - `internal_status=CP-IST-ST`이면 `KamajiControlPlane`, `OpenStackCluster`, DB cluster row를 삭제한다.
   - `internal_status!=CP-IST-ST`이면 `Cluster` paused annotation을 제거하고 `Cluster`를 삭제한다.
   - `HelmChartProxy`, `HelmReleaseProxy`, `HelmRelease`를 정리한다.
4. 개별 실패는 누적하고, 마지막에 join error로 반환한다.

### 4.4 ControlPlaneService

책임: cluster DB row, hosted control plane, cluster addon 선언 리소스, suspend/resume, kubeconfig를 관리한다.

소스 파일: `cmd/api-manager/service/iks/controlplane.go`, `cmd/api-manager/service/iks/kubeconfig.go`

주요 저장소: cluster, cluster-openstack-resource, initial deployment status

주요 Kubernetes 리소스: `Secret`, `ServiceAccount`, `OpenStackCluster`, `KamajiControlPlane`, `Cluster`, `HelmRelease`, `HelmChartProxy`, `HelmReleaseProxy`

#### `ControlPlaneService.List`

1. tenant ID를 기본 조건으로 쿼리를 만든다.
2. version, CNI, status, internal status, include deleted 필터를 적용한다.
3. 정렬과 pagination을 적용한다.
4. DB의 cluster 목록을 조회한다.
5. 각 cluster를 control plane DTO로 매핑한다.
6. 매핑 중 nodegroup의 `CurrentCount`를 합산해 `NodeCount`를 계산한다.
7. initial deployment status를 응답 포맷으로 변환한다.
   - `ID-ST-ERR`이면 bastion, control plane, data plane, addon 순으로 error reason을 선택한다.
   - 그 외에는 기존 `ID-ST-*` 값을 그대로 사용한다.

#### `ControlPlaneService.AdminList`

1. tenant 제약 없이 cluster query를 만든다.
2. 일반 list와 같은 필터/정렬/pagination을 적용한다.
3. cluster 목록을 조회하고 DTO로 매핑한다.

#### `ControlPlaneService.Get`

1. tenant ID와 cluster ID 또는 cluster name으로 DB에서 cluster를 찾는다.
2. control plane DTO로 매핑한다.

#### `ControlPlaneService.AdminGet`

1. tenant 제약 없이 cluster ID로 조회한다.
2. 필요하면 soft-deleted row도 포함한다.
3. control plane DTO로 매핑한다.

#### `ControlPlaneService.AdminGetAllClusters`

1. tenant ID로 cluster 목록을 조회한다.
2. 필요하면 soft-deleted row도 포함한다.
3. control plane DTO 목록으로 매핑한다.

#### `ControlPlaneService.Create`

1. DB에 cluster row를 먼저 저장한다.
   - `status=CP-ST-PRV`
   - `internal_status=CP-IST-ST`
   - replicas, CIDR, endpoint, version, CNI를 함께 저장
2. OpenStack 전용 사용자를 만든다.
3. 해당 사용자로 application credential을 만든다.
4. CCM 전용 cloud config `Secret`을 만든다.
   - password 인증 방식
   - `cloud.conf`
5. CAPI OpenStack identity용 cloud config `Secret`을 만든다.
   - application credential 인증 방식
   - `clouds.yaml`
6. tenant namespace의 `default` `ServiceAccount`에 image pull secret을 추가한다.
7. `OpenStackCluster`를 생성한다.
   - infra network/subnet
   - cloud config secret 참조
   - cluster ID label
8. `KamajiControlPlane`를 생성한다.
   - service type, replica 수, Kubernetes version, datastore, DNS IP, 외부 domain
   - `NodePort` 타입이면 사용 가능한 port를 먼저 탐색
9. `Cluster`를 생성한다.
   - `controlPlaneRef=KamajiControlPlane`
   - `infrastructureRef=OpenStackCluster`
   - default addon selector label
10. `HelmRelease` 2개를 만든다.
    - cloud-controller-manager
    - cluster-autoscaler
11. `HelmChartProxy` 4개를 만든다.
    - Calico
    - Cinder CSI
    - NFS CSI
    - Manila CSI
12. 앞에서 만든 두 `Secret`에 `Cluster` ownerReference를 patch한다.
13. DB cluster `internal_status`를 `CP-IST-APD`로 바꾼다.
14. 중간 실패 시 DB cluster status를 `CP-ST-F`로 바꾸고 service error를 반환한다.

#### `ControlPlaneService.Upgrade`

1. DB에서 cluster를 조회한다.
2. 상태 코드는 확인하지 않고, 요청 버전이 현재 버전보다 허용 가능한 control plane minor upgrade인지 검증한다.
3. `KamajiControlPlane`을 조회한다.
4. target Kubernetes version으로 `KamajiControlPlane`을 patch한다.
5. 응답은 기존 DB cluster를 기반으로 만든 control plane DTO다.

#### `ControlPlaneService.Scale`

1. DB에서 cluster를 조회한다.
2. `KamajiControlPlane`을 조회한다.
3. replica 수를 요청값으로 바꿔 patch한다.
4. DB cluster의 total count를 갱신한다.

#### `ControlPlaneService.Update`

1. DB에서 cluster를 조회한다.
2. 현재 구현에서는 description만 변경 후보로 수집한다.
3. DB cluster row만 업데이트한다.
4. Kubernetes 리소스는 수정하지 않는다.

#### `ControlPlaneService.Suspend`

1. DB에서 cluster를 조회한다.
2. DB `suspended=true`이면 bad request를 반환한다.
3. `Cluster`를 조회한다.
4. `cluster.x-k8s.io.am/operation-state` annotation이 이미 있으면 중복 요청 또는 다른 작업 진행 중으로 처리한다.
5. 문제가 없으면 `cluster.x-k8s.io.am/operation-state=suspend-requested` annotation을 기록한다.
6. DB의 `suspended` 값을 `true`로 갱신한다.

#### `ControlPlaneService.Resume`

1. DB에서 cluster를 조회한다.
2. DB `suspended=false`이면 bad request를 반환한다.
3. `Cluster`를 조회한다.
4. `cluster.x-k8s.io.am/operation-state` annotation이 이미 있으면 중복 요청 또는 다른 작업 진행 중으로 처리한다.
5. 문제가 없으면 `cluster.x-k8s.io.am/operation-state=resume-requested` annotation을 기록한다.
6. DB의 `suspended` 값을 `false`로 갱신한다.

#### `ControlPlaneService.AdminSuspendAllClusters`

1. tenant 내 `suspended=false` cluster를 전부 조회한다.
2. 각 cluster마다 `Cluster`를 조회한다.
3. 각 cluster에 `cluster.x-k8s.io.am/operation-state=suspend-requested` annotation을 기록한다.
4. 각 DB cluster row의 `suspended` 값을 `true`로 갱신한다.
5. 이미 operation-state annotation이 있으면 해당 cluster는 실패로 누적한다.
6. 실패한 cluster는 누적하고 join error로 반환한다.

#### `ControlPlaneService.AdminResumeAllClusters`

1. tenant 내 `suspended=true` 이면서 `status=CP-ST-SUS`인 cluster를 전부 조회한다.
2. 각 cluster마다 `Cluster`를 조회한다.
3. 각 cluster에 `cluster.x-k8s.io.am/operation-state=resume-requested` annotation을 기록한다.
4. 각 DB cluster row의 `suspended` 값을 `false`로 갱신한다.
5. 이미 operation-state annotation이 있으면 해당 cluster는 실패로 누적한다.
6. 실패한 cluster는 누적하고 join error로 반환한다.

#### `ControlPlaneService.Delete`

1. DB에서 cluster를 조회한다.
2. `deleteClusterResources()`를 호출한다.
3. `internal_status=CP-IST-ST`이면
   - `KamajiControlPlane` 삭제
   - `OpenStackCluster` 삭제
   - DB transaction 시작
   - cluster row 삭제
   - transaction commit
4. cluster `internal_status!=CP-IST-ST`이면
   - `Cluster` paused annotation 해제
   - `Cluster` 삭제
5. 공통 후처리
   - `HelmChartProxy` 삭제
   - `HelmReleaseProxy` 삭제
   - `HelmRelease` finalizer 제거 및 삭제

#### `ControlPlaneService.SetClusterPaused`

1. `Cluster`를 조회한다.
2. paused 요청이면 `cluster.x-k8s.io/paused=true` annotation 추가
3. resume 요청이면 해당 annotation 삭제
4. label/annotation patch를 수행한다.

#### `ControlPlaneService.SetHelmReleasePaused`

1. tenant namespace의 모든 `HelmRelease`를 조회한다.
2. cluster name prefix가 맞는 release만 필터링한다.
3. 각 release의 `spec.suspend` 값을 patch한다.

#### `ControlPlaneService.GetClusterKubeconfig`

1. DB에서 cluster를 조회한다.
2. `<cluster-name>-admin-kubeconfig` `Secret`을 읽는다.
3. `admin.conf`를 파싱한다.
4. kubeconfig 내부 cluster server 주소를 `<cluster-id>.<base-domain>`으로 교체한다.
5. 다시 kubeconfig 바이트로 직렬화한다.

#### `ControlPlaneService.GetOpenstackResources`

1. cluster ID 기준으로 DB의 cluster-openstack-resource 목록을 조회한다.
2. API 응답 DTO로 변환한다.

#### `ControlPlaneService.IsClusterAvailable`

1. DB에서 cluster를 조회한다.
2. `initial_deployment_status`가 `ID-ST-PRV`, `ID-ST-NO`, `ID-ST-ERR` 중 하나면 `false`를 반환한다.
3. cluster `status`가 `CP-ST-DL`, `CP-ST-DLT`, `CP-ST-DLF`, `CP-ST-SUS` 중 하나면 `false`를 반환한다.
4. 그 외에는 `true`를 반환한다.

### 4.5 DataPlaneService

책임: nodegroup DB row와 worker node 선언 리소스를 관리한다.

소스 파일: `cmd/api-manager/service/iks/dataplane.go`

주요 저장소: nodegroup, cluster, node

주요 Kubernetes 리소스: `Secret`, `KubeadmConfigTemplate`, `OpenStackMachineTemplate`, `MachineDeployment`, `MachineHealthCheck`, `KinxServiceGateway`

#### `DataPlaneService.List`

1. tenant ID와 cluster ID를 기본 조건으로 nodegroup query를 만든다.
2. version, flavor, image, status, internal status 필터를 적용한다.
3. 정렬과 pagination을 적용한다.
4. DB nodegroup 목록을 조회해 DTO로 매핑한다.

#### `DataPlaneService.AdminList`

1. tenant 제약 없이 nodegroup query를 만든다.
2. 필요하면 cluster ID로 제한한다.
3. 필요하면 soft-deleted row를 포함한다.
4. 일반 list와 같은 필터/정렬/pagination을 적용한다.
5. admin repository로 조회해 DTO로 매핑한다.

#### `DataPlaneService.AdminGet`

1. cluster ID와 nodegroup ID로 조회한다.
2. 필요하면 soft-deleted row를 포함한다.
3. DTO로 매핑한다.

#### `DataPlaneService.Get`

1. tenant ID + cluster ID + nodegroup ID 또는 nodegroup name으로 조회한다.
2. DTO로 매핑한다.

#### `DataPlaneService.Create`

1. cluster를 조회한다.
2. nodegroup version을 cluster version과 동일하게 강제한다.
3. nodegroup version이 cluster보다 높거나 minor 차이가 허용 범위를 넘는지 검증한다.
4. 현재 로그인 사용자의 credential로 원본 OpenStack keypair를 조회한다.
5. cluster 전용 cloud config `Secret`에서 application credential을 읽는다.
6. cluster 전용 credential로 같은 public key를 가진 keypair를 생성한다.
   - 이미 있으면 conflict는 무시한다.
7. label/annotation을 DB 저장용 JSONMap으로 변환한다.
8. volume availability zone 기본값을 채운다.
9. security group 목록을 DB domain 객체로 변환한다.
10. DB에 nodegroup row를 먼저 저장한다.
    - `status=DP-ST-PRV`
    - `internal_status=DP-IST-ST`
11. `KubeadmConfigTemplate`를 생성한다.
12. `OpenStackMachineTemplate`를 생성한다.
13. `MachineDeployment`를 생성한다.
    - last-created annotation
    - kubeadm version skew skip annotation
14. auto-healer가 켜져 있으면 `MachineHealthCheck`를 생성한다.
15. DB nodegroup `internal_status`를 `DP-IST-APD`로 바꾼다.
16. Kubernetes 리소스 생성 중 실패하면 DB nodegroup status를 `DP-ST-F`로 바꾸고 에러를 반환한다.

#### `DataPlaneService.Upgrade`

1. cluster와 nodegroup을 조회한다.
2. cluster `status=CP-ST-AV`인지, nodegroup `status=DP-ST-AV`인지 확인한다.
3. 요청 버전이 현재보다 높은지 검증한다.
4. cluster와 nodegroup 간 minor version 차이 제한을 검증한다.
5. 동일 cluster 내 모든 nodegroup과의 minor version 차이도 검증한다.
6. rollback을 위해 이전 nodegroup version을 context에 저장한다.
7. 조건부 DB update로 cluster와 nodegroup version을 선반영해 동시 요청을 제어한다.
8. 요청 버전이 cluster보다 높으면 cluster upgrade가 필요하다고 판단한다.
9. cluster upgrade가 필요한 경우
   - 해당 `MachineDeployment`를 paused 처리
   - DB에서 cluster를 다시 조회
   - 요청 버전이 현재 control plane보다 한 단계 높은 허용 업그레이드인지 검증
   - `KamajiControlPlane`을 조회
   - `KamajiControlPlane.spec.version`을 target version으로 patch
   - 별도 goroutine이 cluster가 `CP-ST-CF`를 거쳐 `CP-ST-AV`가 되는지 polling
   - 복귀 후 paused annotation 해제
10. nodegroup upgrade 수행
    - `MachineDeployment` 조회
    - 현재 `OpenStackMachineTemplate` 조회
    - target version에 맞는 image ID 조회
    - 새 `OpenStackMachineTemplate` 생성
    - `MachineDeployment`의 infraRef와 Kubernetes version patch
    - DB nodegroup image ID 갱신
11. 새 template 생성 또는 machine deployment patch 실패 시
    - nodegroup `status=DP-ST-AV`, `internal_status=DP-IST-UGF`, 이전 version으로 rollback
    - 필요하면 새로 만든 `OpenStackMachineTemplate` 삭제

#### `DataPlaneService.Scale`

1. DB에서 nodegroup을 조회한다.
2. nodegroup이 속한 cluster를 조회한다.
3. `MachineDeployment`용 metadata에 last-scaled 및 event-watcher 동기화 annotation을 채운다.
4. `MachineDeployment`를 조회한다.
5. replicas를 요청값으로 patch한다.
6. 완료 annotation을 제거한 metadata로 한 번 더 patch한다.
7. DB의 최종 상태 갱신은 여기서 수행하지 않고 event-watcher 동기화를 전제로 둔다.

#### `DataPlaneService.Update`

1. DB에서 nodegroup을 조회한다.
2. cluster를 조회해 실제 `MachineDeployment` 이름을 계산한다.
3. `MachineDeployment`를 조회한다.
4. description, label, annotation 변경분을 분리한다.
5. label/annotation은 `MachineDeployment`에 반영한다.
6. description/label/annotation은 DB에도 업데이트한다.

#### `DataPlaneService.Delete`

1. DB에서 nodegroup을 조회한다.
2. cluster를 조회한다.
3. `MachineHealthCheck`를 삭제한다.
4. `MachineDeployment` paused annotation을 제거한다.
5. `MachineDeployment`를 삭제한다.
6. 현재 version 이름의 `OpenStackMachineTemplate`를 삭제한다.
7. `KubeadmConfigTemplate`를 삭제한다.
8. `internal_status=DP-IST-ST`이면 DB nodegroup row도 삭제한다.

#### `DataPlaneService.ListNode`

1. nodegroup ID와 tenant ID로 node query를 만든다.
2. status/internal status filter를 적용한다.
3. 정렬과 pagination을 적용한다.
4. DB node 목록을 조회해 DTO로 매핑한다.

#### `DataPlaneService.SetMachineDeploymentPaused`

1. `MachineDeployment`를 조회한다.
2. 없으면 skip한다.
3. paused 요청이면 `cluster.x-k8s.io/paused=true` annotation 추가
4. resume 요청이면 해당 annotation 제거
5. label/annotation patch를 수행한다.

#### `DataPlaneService.ensureServiceGateway`

1. nodegroup network ID를 label selector로 만든다.
2. 같은 network ID를 가진 `KinxServiceGateway`를 조회한다.
3. 이미 있으면 생성하지 않고 종료한다.
4. 없으면 infrastructure 설정값과 nodegroup network 정보를 조합해 `KinxServiceGateway`를 만든다.
5. 현재 `Create()` 내부 호출은 주석 처리되어 있어 실사용 경로에서는 실행되지 않는다.

### 4.6 AutoScalerService

책임: nodegroup autoscaler DB row와 `MachineDeployment` autoscaler annotation을 동기화한다.

소스 파일: `cmd/api-manager/service/iks/autoscaler.go`

주요 저장소: nodegroup, autoscaler

주요 Kubernetes 리소스: `MachineDeployment`

#### `AutoScalerService.Get`

1. nodegroup과 cluster 조건으로 nodegroup을 찾는다.
2. nodegroup에 연결된 autoscaler가 요청 ID와 일치하는지 확인한다.
3. 일치하면 autoscaler DTO로 반환한다.

#### `AutoScalerService.Create`

1. nodegroup을 조회한다.
2. 이미 autoscaler가 있으면 conflict를 반환한다.
3. 요청값으로 autoscaler annotation map을 만든다.
4. `MachineDeployment`를 조회해 autoscaler annotation을 patch한다.
5. DB에 autoscaler row를 저장한다.

#### `AutoScalerService.Update`

1. 기존 autoscaler를 조회한다.
2. 변경 요청값만 반영한 autoscaler annotation map을 만든다.
3. `MachineDeployment` annotation을 patch한다.
4. 변경된 필드만 DB autoscaler row에 update한다.

#### `AutoScalerService.Delete`

1. 기존 autoscaler를 조회한다.
2. `MachineDeployment`에서 autoscaler 관련 annotation을 모두 제거한다.
3. DB autoscaler row를 삭제한다.

### 4.7 DashboardService

책임: workload cluster 리소스를 읽어 dashboard용 목록과 summary를 만든다.

소스 파일: `cmd/api-manager/service/iks/dashboard.go`

주요 저장소: cluster

주요 Kubernetes 리소스: workload cluster의 `Namespace`, `Pod`, `Deployment`, `ReplicaSet`, `StatefulSet`, `DaemonSet`, `Job`, `CronJob`, `Service`, `Endpoints`, `ConfigMap`, `Secret`, `Event`

#### 초기화 시 등록 리소스

1. cluster-scoped resource
   - `namespaces`
2. namespaced resource
   - `pods`
   - `deployments`
   - `replicasets`
   - `statefulsets`
   - `daemonsets`
   - `jobs`
   - `cronjobs`
   - `services`
   - `endpoints`
   - `configmaps`
   - `secrets`
   - `events`

#### `DashboardService.List`

1. DB에서 cluster를 조회한다.
2. 요청 resource 문자열을 GVR로 매핑한다.
3. cluster name과 tenant namespace로 target cluster key를 만든다.
4. cluster cache에서 target cluster reader를 얻는다.
5. 요청 GVR이 namespaced resource registry에 있는지 먼저 찾는다.
6. 없으면 cluster-scoped resource registry에서 찾는다.
7. 등록된 provider가 reader로 실제 workload cluster 리소스를 조회한다.
8. provider는 결과를 정렬하고 pagination을 적용해 반환한다.
9. 어느 registry에도 없으면 unsupported resource error를 반환한다.

#### `DashboardService.Summary`

1. DB에서 cluster를 조회한다.
2. optional namespace filter를 계산한다.
3. workload cluster의 pod 목록을 조회한다.
4. `deployments`, `replicasets`, `statefulsets`, `daemonsets`, `jobs`, `cronjobs`를 순회하며 수량을 집계한다.
5. pod runtime 상태 정보와 workload count를 함께 반환한다.

### 4.8 CommonCodeService

책임: 공통코드와 코드 타입을 조회한다.

소스 파일: `cmd/api-manager/service/iks/commoncode.go`

주요 저장소: commoncode

주요 Kubernetes 리소스: 없음

#### `CommonCodeService.List`

1. code type filter가 있으면 조건을 추가한다.
2. DB에서 common code 목록을 조회한다.
3. DTO 목록으로 매핑한다.

#### `CommonCodeService.Get`

1. code 값으로 DB 단건 조회한다.
2. DTO로 매핑한다.

#### `CommonCodeService.ListCodeTypes`

1. DB에서 distinct code type 목록을 조회한다.

### 4.9 KubernetesVersionService

책임: 설정 파일에서 사용 가능한 Kubernetes version과 image ID를 읽는다.

소스 파일: `cmd/api-manager/service/iks/version.go`

주요 저장소: 없음

주요 Kubernetes 리소스: 없음

#### `KubernetesVersionService.List`

1. 설정 파일의 version-image map을 순회한다.
2. version 문자열을 정규화한다.
3. semver 기준 정렬을 수행한다.
4. version과 image ID 쌍을 반환한다.

### 4.10 ResourceCheckerService

책임: cluster 변경 가능 여부 판단에 필요한 DB 상태를 조회한다.

소스 파일: `cmd/api-manager/service/iks/resourcechecker.go`

주요 저장소: cluster

주요 Kubernetes 리소스: 없음

#### `ResourceCheckerService.CheckClusterSuspend`

1. cluster ID와 tenant ID로 DB cluster를 조회한다.
2. `suspended` boolean을 그대로 반환한다.

## 5. `cmd/api-manager/service/iks`에서 사용하는 Kubernetes 리소스

아래 표는 `cmd/api-manager/service/iks`에서 직접 생성/조회/수정/삭제되는 리소스만 기준으로 작성했다.

### 5.1 관리 클러스터에서 직접 다루는 리소스

| 리소스 | API 그룹 / Kind | CRD 여부 | 사용 목적 |
| --- | --- | --- | --- |
| `Namespace` | core/v1 `Namespace` | 아니오 | tenant namespace 생성/조회/삭제 |
| `Secret` | core/v1 `Secret` | 아니오 | CCM cloud config, CAPI cloud config, kubeconfig 저장/조회 |
| `ServiceAccount` | core/v1 `ServiceAccount` | 아니오 | tenant namespace의 `default` SA에 image pull secret 연결 |
| `Cluster` | `cluster.x-k8s.io/v1beta1` `Cluster` | 예 | CAPI 클러스터 선언, suspend/resume annotation, delete |
| `OpenStackCluster` | `infrastructure.cluster.x-k8s.io/v1beta1` `OpenStackCluster` | 예 | OpenStack 인프라 참조 리소스 |
| `KamajiControlPlane` | `controlplane.cluster.x-k8s.io` 계열 `KamajiControlPlane` | 예 | hosted control plane 선언 |
| `HelmRelease` | `helm.toolkit.fluxcd.io/v2` `HelmRelease` | 예 | CCM, cluster-autoscaler 배포 |
| `HelmChartProxy` | `addons.cluster.x-k8s.io/v1alpha1` `HelmChartProxy` | 예 | workload cluster addon 배포 선언 |
| `HelmReleaseProxy` | CAAH 계열 `HelmReleaseProxy` | 예 | HelmChartProxy가 파생시키는 release proxy 정리 |
| `KubeadmConfigTemplate` | `bootstrap.cluster.x-k8s.io/v1beta1` `KubeadmConfigTemplate` | 예 | worker join 설정 템플릿 |
| `OpenStackMachineTemplate` | `infrastructure.cluster.x-k8s.io/v1beta1` `OpenStackMachineTemplate` | 예 | worker VM 템플릿 |
| `MachineDeployment` | `cluster.x-k8s.io/v1beta1` `MachineDeployment` | 예 | 노드그룹 선언, scale/update/upgrade/autoscaler annotation |
| `MachineHealthCheck` | `cluster.x-k8s.io/v1beta1` `MachineHealthCheck` | 예 | auto-healer용 노드 상태 감시 |
| `KinxServiceGateway` | `iksv2-servicegateway` 계열 `KinxServiceGateway` | 예 | helper 함수에서 조회/생성 가능하나 현재 `Create()` 경로에서는 호출이 주석 처리됨 |

### 5.2 대시보드에서 workload cluster 대상으로 읽는 리소스

| 리소스 | API 그룹 / Kind | CRD 여부 | 사용 목적 |
| --- | --- | --- | --- |
| `Namespace` | core/v1 `Namespace` | 아니오 | 대시보드 목록 |
| `Pod` | core/v1 `Pod` | 아니오 | 대시보드 목록, summary |
| `Service` | core/v1 `Service` | 아니오 | 대시보드 목록 |
| `Endpoints` | core/v1 `Endpoints` | 아니오 | 대시보드 목록 |
| `ConfigMap` | core/v1 `ConfigMap` | 아니오 | 대시보드 목록 |
| `Secret` | core/v1 `Secret` | 아니오 | 대시보드 목록 |
| `Event` | core/v1 `Event` | 아니오 | 대시보드 목록 |
| `Deployment` | apps/v1 `Deployment` | 아니오 | 대시보드 목록, summary |
| `ReplicaSet` | apps/v1 `ReplicaSet` | 아니오 | 대시보드 목록, summary |
| `StatefulSet` | apps/v1 `StatefulSet` | 아니오 | 대시보드 목록, summary |
| `DaemonSet` | apps/v1 `DaemonSet` | 아니오 | 대시보드 목록, summary |
| `Job` | batch/v1 `Job` | 아니오 | 대시보드 목록, summary |
| `CronJob` | batch/v1 `CronJob` | 아니오 | 대시보드 목록, summary |

### 5.3 CRD 필수 목록

`api-manager`의 핵심 provisioning 경로는 아래 CRD가 존재한다는 전제 위에서 동작한다.

1. `Cluster`
2. `OpenStackCluster`
3. `KamajiControlPlane`
4. `HelmRelease`
5. `HelmChartProxy`
6. `HelmReleaseProxy`
7. `KubeadmConfigTemplate`
8. `OpenStackMachineTemplate`
9. `MachineDeployment`
10. `MachineHealthCheck`
11. `KinxServiceGateway`

## 6. 운영상 중요한 메모

- cluster-resource 생성 흐름은 반환 시점에 전체 배포 완료를 보장하지 않는다. 선언형 리소스 생성 후 후속 컨트롤러가 상태를 완성한다.
- 클러스터 suspend/resume은 DB boolean만 바꾸는 것이 아니라 `Cluster` annotation으로 operation state를 남긴다.
- 노드그룹 scale/upgrade는 event-watcher가 식별할 수 있도록 timestamp 및 상태 annotation을 명시적으로 갱신한다.
- `AutoScalerService`는 DB와 `MachineDeployment` annotation을 함께 수정하므로, annotation drift가 생기면 cluster-autoscaler 동작과 DB 표현이 달라질 수 있다.
- 대시보드 조회 경로는 `IsClusterAvailable` 판정상 `initial_deployment_status`가 `ID-ST-PRV`, `ID-ST-NO`, `ID-ST-ERR`가 아니고, cluster `status`가 `CP-ST-DL`, `CP-ST-DLT`, `CP-ST-DLF`, `CP-ST-SUS`가 아닐 때만 읽기를 허용한다.
- 삭제 경로는 ownerReference와 finalizer 정리에 의존한다. 특히 `HelmRelease`는 finalizer 제거가 별도 수행된다.
