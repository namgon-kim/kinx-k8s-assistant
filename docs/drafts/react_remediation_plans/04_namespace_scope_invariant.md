# Plan 04: Namespace and Scope Invariant

> 상태: 부분 구현됨.
>
> request context namespace와 mutating kubectl command/action target의 namespace
> invariant는 구현되어 있다. `kubectl apply -f ...` manifest 내부 namespace 검증은
> 아직 남아 있다.
> 현재 관련 위치는 `contract/action.go`, `contract/structured.go`, `kube/target.go`,
> `kube/resource.go`, `flow/request`, `coordinator/iteration.go`다. 아래 옛 루트 파일
> 경로는 구현 이력이다.

## Problem

사용자 요청이나 이전 evidence에서 namespace가 확정되었는데, model이 action target이나 command에서 namespace를 누락할 수 있다. 현재 검증은 `action.target.namespace`가 있을 때 command가 이를 포함하는지만 확인한다. `requestContext.Scope.Namespace`와 command를 직접 비교하는 invariant가 약하다.

## Current Code Evidence

- `internal/react/coordinator/execution.go`, `internal/react/coordinator/iteration.go`, `internal/react/kube/target.go`
  - target namespace가 있으면 command namespace 누락을 잡는다.
  - 하지만 target namespace 자체가 비어 있으면 request context namespace가 있어도 잡기 어렵다.
- `internal/react/flow/request`, `internal/react/session/context.go`
  - requirement analysis에서 request context와 namespace가 만들어진다.
- `prompts/default.tmpl`
  - "namespace를 유지하라"는 prompt rule이 있지만 deterministic gate는 아니다.

## Desired Contract

Namespace는 scope invariant다.

요청 또는 evidence에서 namespace가 확정된 경우:

- action target namespace는 동일해야 한다.
- command도 동일 namespace를 사용해야 한다.
- mutation command는 namespace omission을 절대 허용하지 않는다.
- namespaced resource인데 namespace가 unknown이면 mutation을 수행하지 않고 먼저 namespace를 resolve해야 한다.

## Proposed Changes

1. effective namespace 계산 함수를 추가한다.

```go
func (l *Loop) effectiveNamespaceForAction(call gollm.FunctionCall) (string, bool)
```

우선순위:

1. action target namespace
2. request context scope namespace
3. primary target namespace
4. observed resolved namespace

2. action target validation에서 request context namespace도 검증한다.

3. mutating command는 namespace가 필요한 resource인지 판정한다.

4. namespace가 필요한 mutation인데 namespace가 없으면 block한다.

5. `default` namespace를 implicit fallback으로 쓰지 않는다.

## Example

요청:

```text
web 네임스페이스의 web-app deployment를 고쳐줘
```

잘못된 command:

```bash
kubectl create configmap app-config
```

왜 잘못인가:

- request namespace는 `web`
- command namespace 없음
- ConfigMap은 namespaced resource
- mutation이므로 default namespace fallback은 위험함

수정 command:

```bash
kubectl -n web create configmap app-config ...
```

## Acceptance Criteria

- request context namespace가 있으면 action target namespace가 비어 있어도 command namespace 검증이 실행된다.
- mutating namespaced resource command가 namespace를 누락하면 실행 전 차단된다.
- cluster-scoped resource에는 namespace requirement를 적용하지 않는다.
- `-A`는 read-only lookup에는 허용될 수 있지만 namespaced mutation에는 허용하지 않는다.

## Implementation Status

- coordinator target validation과 `kube/target.go` 경계에 request scope namespace invariant gate를 유지한다.
- 적용 범위는 request context namespace가 확정된 `kubectl` mutating command 중, CLI command 또는 `action.target.resource`에서 namespaced resource를 판정할 수 있는 경우다.
- `action.target.namespace`가 request namespace와 다르거나, command namespace가 다르거나, command namespace가 누락된 경우 correction을 추가하고 실행을 막는다.
- cluster-scoped resource는 namespace 강제 대상에서 제외한다.
- manifest 내부 namespace를 읽어야 하는 `kubectl apply -f ...`류의 파일 기반 검증은 아직 구현하지 않았다. 이 경우는 파일/manifest 파서와 approval summary를 함께 설계해야 한다.

## Regression Scenarios

1. request namespace `web`, command `kubectl create configmap app-config`
   - Expected: blocked.

2. request namespace `web`, command `kubectl -n default create configmap app-config`
   - Expected: blocked.

3. request namespace `web`, command `kubectl -n web create configmap app-config`
   - Expected: passes namespace gate.

4. cluster-scoped mutation such as `kubectl label node node-a ...`
   - Expected: namespace not required, but approval still required.

## Risks

- Some commands include namespace through manifest metadata instead of CLI flag.
- For `kubectl apply -f`, runtime should inspect manifest namespace when feasible or require dry-run/rendered manifest summary before approval.
