# K8saas Controller 구성 및 동작 분석 기준

## 1. 목적

현재 시스템에서 Cluster 배포 및 동작 분석 시 필요한 Controller 구성, Namespace, 주요 역할, 관련 Kubernetes 리소스를 정리한다.

이 문서는 서비스 API 서버의 내부 상태값을 직접 참조하지 않고, Kubernetes 리소스에 기록되는 `Status`, `Labels`, `Annotations`를 중심으로 상태를 판단하기 위한 기준 문서이다.

---

## 2. Controller 구성

| Namespace | Controller | 용도 | 관련 리소스 | 확인 포인트 |
|---|---|---|---|---|
| `caaph-system` | `caaph-controller-manager` | Cluster API Add-on Provider Helm. CAPI Cluster에 Helm Chart 기반 Add-on 배포를 관리한다. | `HelmChartProxy`, `HelmReleaseProxy`, `Cluster` | Add-on 배포 상태, `HelmReleaseProxy` 생성 여부 |
| `capi-kubeadm-bootstrap-system` | `capi-kubeadm-bootstrap-controller-manager` | Machine이 Kubernetes Node로 조인할 수 있도록 kubeadm bootstrap data를 생성한다. | `KubeadmConfigTemplate`, `KubeadmConfig`, `Secret`, `Machine` | `KubeadmConfig.status.dataSecretName`, bootstrap Secret |
| `capi-system` | `capi-controller-manager` | Cluster API Core Controller. Cluster와 Machine 계층의 라이프사이클을 관리한다. | `Cluster`, `MachineDeployment`, `MachineSet`, `Machine`, `MachineHealthCheck` | `Cluster.status`, `MachineDeployment.status`, `Machine` 상태 |
| `capo-system` | `capo-controller-manager` | Cluster API Provider OpenStack. CAPI 리소스와 OpenStack 인프라 리소스를 연동한다. | `OpenStackCluster`, `OpenStackMachineTemplate`, `OpenStackMachine`, `Machine` | VM, Port, Network, SecurityGroup 생성 상태 |
| `kamaji-system` | `capi-kamaji-controller-manager` | Kamaji Control Plane Provider. `KamajiControlPlane`을 CAPI Control Plane으로 사용한다. | `KamajiControlPlane`, `TenantControlPlane`, `Cluster` | `KamajiControlPlane.status`, `TenantControlPlane` 생성 여부 |
| `kamaji-system` | `kamaji` | Hosted Control Plane Manager. Tenant Cluster Control Plane을 Management Cluster 내부 Pod로 실행한다. | `TenantControlPlane`, Control Plane Pods, `Secret`, `Service`, `ServiceAccount` | Control Plane Pod 상태, `TenantControlPlane.status`, API Server Service |
| `servicegateway` | `iksv2-servicegateway-controller-manager` | `MachineDeployment` 네트워크와 Control Plane 네트워크 연결을 위한 Gateway 추상 리소스를 관리한다. | `KinxServiceGateway`, `MachineDeployment`, OpenStack Network, Subnet, Router, Static Route | Gateway 상태, 라우팅, Static Route |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-processor` | Cluster 배포 과정의 종속 리소스 이벤트를 처리한다. | `Cluster`, `OpenStackCluster`, `KamajiControlPlane`, `MachineDeployment`, `Machine`, `OpenStackMachine`, `HelmChartProxy`, `HelmReleaseProxy`, `KinxServiceGateway` | Cluster key와 error를 함께 필터링 |
| `dev-k8saas` | `k8saas-cluster-api-manager-event-watcher` | Cluster 배포 라이프사이클과 상태를 감시한다. | `Cluster`, `OpenStackCluster`, `KamajiControlPlane`, `MachineDeployment`, `MachineHealthCheck`, `KinxServiceGateway` | `Status`, `Labels`, `Annotations`, 최초 배포 단계 annotation |

> `caaph-controller-manager`는 Hosted Control Plane Provider가 아니라 Cluster API Add-on Provider Helm 계열이다. `HelmChartProxy`와 `HelmReleaseProxy`를 통해 Cluster API Cluster 대상 Helm Add-on 배포를 관리한다.  
> `capi-kubeadm-bootstrap-controller-manager`는 Node를 직접 만들지 않고, Machine을 Kubernetes Node로 만들기 위한 bootstrap data와 Secret 생성을 담당한다.  
> `kamaji`는 Tenant Control Plane을 Management Cluster 내부 Pod로 실행하며, `capi-kamaji-controller-manager`는 이를 Cluster API Control Plane Provider로 연결한다. 

---

## 3. 서비스 관리 요소

### 3.1 Event Watcher

`k8saas-cluster-api-manager-event-watcher`는 주로 Cluster 배포의 라이프사이클과 상태를 감시한다.

서비스 API 서버에서 정의하는 Status 값을 보고 DB 업데이트 또는 상태 변경을 수행하지만, 현재 문서에서는 서비스 API를 참조하지 않는다.

따라서 분석 시에는 Kubernetes 리소스에 기록된 다음 정보를 기준으로 판단한다.

- `Status`
- `Conditions`
- `Labels`
- `Annotations`
- 최초 배포 단계 관련 annotation

---

### 3.2 Event Processor

`k8saas-cluster-api-manager-event-processor`는 Watcher 외의 종속 리소스 이벤트 처리를 담당한다.

Processor 로그를 확인할 때는 반드시 해당 Cluster의 key 값과 에러 메시지를 함께 필터링해야 한다.

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor \
  | grep "<cluster-key>" \
  | grep "ERROR"
````

주의사항:

* 에러 문자열만 검색하면 다른 Cluster 이벤트와 섞일 수 있다.
* Cluster key 또는 Cluster 이름으로 범위를 먼저 좁혀야 한다.
* Watcher 로그와 같은 시간대 기준으로 비교해야 한다.

---

## 4. 자동 생성 리소스 관계

생성이 생략된 리소스들은 상위 리소스가 생성될 때 각 Controller가 자동 생성한다.

| 상위 리소스               | 생성되는 리소스                                    |
| -------------------- | ------------------------------------------- |
| `KamajiControlPlane` | `TenantControlPlane`                        |
| `MachineDeployment`  | `MachineSet`, `Machine`, `OpenStackMachine` |
| `HelmChartProxy`     | `HelmReleaseProxy`                          |

---

## 5. Cluster 최초 배포 시 생성되는 리소스

서비스 개념에서 Cluster를 배포하면 다음 리소스들이 처음 생성된다.

### 5.1 Hosted Control Plane

Hosted Control Plane에서는 Control Plane이 물리 서버가 아니라 Management Cluster Worker 위의 Pod로 생성된다.

관련 리소스:

* `Secret`
* `ServiceAccount`
* `OpenStackCluster`
* `KamajiControlPlane`
* `Cluster`
* `HelmRelease`
* `HelmChartProxy`
* `HelmReleaseProxy`
* `KubeadmConfigTemplate`
* `OpenStackMachineTemplate`

역할:

* Tenant Cluster Control Plane 정의
* Control Plane Pod 실행
* Cluster API와 Kamaji 연동
* Helm 기반 Add-on 배포
* Worker Node 생성을 위한 Template 제공

---

### 5.2 Data Plane / Worker

Data Plane은 Tenant Cluster의 Worker Node 영역이다.

관련 리소스:

* `Secret`
* `MachineDeployment`
* `MachineHealthCheck`
* `KinxServiceGateway`

역할:

* Worker Node 생성 및 관리
* Worker Node 상태 확인
* Control Plane 네트워크와 Worker Node 네트워크 연결
* 장애 Node 감지 및 복구 기준 제공

---

## 6. 최초 배포 단계 관리

Cluster 최초 배포와 관련된 사항은 `최초 배포 단계`라는 개념으로 관리된다.

이 단계는 Kubernetes 리소스 이름이 아니다. 별도 최초 배포 객체를 조회하는 방식이 아니라, 관련 Kubernetes 리소스의 상태와 annotation, label, timestamp를 조합해 판단한다.

이 정보는 Kubernetes 리소스의 annotation 또는 label에 기록된다.

분석 시 확인 대상:

* 최초 배포 단계 annotation 존재 여부
* 최초 배포 완료 여부
* 배포 중 실패 상태 기록 여부
* Watcher 또는 Processor가 기록한 labels, annotations 변경 여부

---

## 7. 동작 분석 기준

다음 Kubernetes 리소스 정보를 기준으로 판단한다.

### 7.1 Status 확인 대상

* `<resource>.status`

### 7.2 Conditions 확인 대상


### 7.3 Labels 확인 대상

* Cluster 식별 label
* 소유 관계 label
* Controller가 기록한 상태 label
* Watcher 또는 Processor가 기록한 관리용 label

### 7.4 Annotations 확인 대상

* 최초 배포 단계 annotation
* 배포 상태 기록
* Watcher 또는 Processor가 기록한 상태 변경 정보
* 서비스 관리용 key 값

---

## 8. 로그 분석 기준

`dev-k8saas` namespace의 Watcher와 Processor 로그는 Cluster 배포 상태 분석에 중요하다.

Processor 로그 확인:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor
```

Watcher 로그 확인:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher
```

Cluster key 기준 필터링:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-watcher \
  | grep "<cluster-key>"
```

Processor 에러 필터링:

```bash
kubectl logs -n dev-k8saas deploy/k8saas-cluster-api-manager-event-processor \
  | grep "<cluster-key>" \
  | grep "ERROR"
```

분석 기준:

* Cluster key 값으로 필터링
* 에러 메시지로 필터링
* 특정 리소스 이름으로 필터링
* 동일 시간대의 Watcher 로그와 Processor 로그 비교
