# kubectl-ai ReAct 루프 분리 검토

## 목적

현재 k8s-assistant는 kubectl-ai의 `Agent`를 감싸서 사용한다. 이 문서는 ReAct 루프를 k8s-assistant에서 직접 구현하고, kubectl-ai는 Kubernetes Tool 커넥터 계층으로만 사용할 수 있는지 검토한다.

## 결론

가능은 하다. 다만 kubectl-ai가 "Tool 커넥터만 재사용하는 패키지"로 깔끔하게 분리되어 있다고 보기는 어렵다.

kubectl-ai에는 재사용 가능한 공개 Tool 계층이 있다. `pkg/tools.Tool`, `pkg/tools.Tools`, `ToolCall.InvokeTool`, `NewKubectlTool`, `NewBashTool`, MCP Tool wrapper는 k8s-assistant의 자체 ReAct 루프에서도 사용할 수 있다.

반면 ReAct 루프, 스트리밍 응답 처리, tool call 분석, 승인 요청, pending tool call 상태, session 반영, MCP 초기화 흐름은 `pkg/agent.Agent` 안에 강하게 묶여 있다. 따라서 루프를 k8s-assistant로 가져오려면 단순 설정 변경이 아니라 루프를 부분 재구현하거나 kubectl-ai 코드를 일부 포팅하는 작업이 필요하다.

## 현재 구조

현재 프로젝트의 `internal/agent/setup.go`는 다음 흐름으로 동작한다.

1. `gollm.NewClient`로 LLM client를 생성한다.
2. `kubectl-ai/pkg/agent.Agent`를 생성한다.
3. `Tools: tools.Default()`를 주입한다.
4. `Agent.Init(ctx)`를 호출한다.
5. goroutine에서 `Agent.Run(ctx, initialQuery)`를 호출한다.
6. k8s-assistant는 `Input` / `Output` channel을 통해 kubectl-ai Agent와 통신한다.

즉, 현재 ReAct 루프의 주체는 k8s-assistant가 아니라 kubectl-ai의 `pkg/agent.Agent.Run`이다.

## kubectl-ai에서 재사용 가능한 부분

### Tool 인터페이스

`pkg/tools/interfaces.go`의 `Tool` 인터페이스는 커넥터 경계로 사용할 수 있다.

- `Name()`
- `Description()`
- `FunctionDefinition()`
- `Run(ctx, args)`
- `IsInteractive(args)`
- `CheckModifiesResource(args)`

이 인터페이스는 LLM에 제공할 function schema, 실제 실행, interactive 여부, 리소스 변경 여부를 모두 포함한다. k8s-assistant가 자체 루프를 구현하더라도 이 인터페이스는 그대로 사용할 수 있다.

### Tool Registry

`pkg/tools/tools.go`의 `Tools` registry도 재사용 가능하다.

- `RegisterTool`
- `AllTools`
- `Names`
- `ParseToolInvocation`
- `ToolCall.InvokeTool`
- `ToolResultToMap`

특히 `ParseToolInvocation`과 `InvokeTool`을 쓰면 LLM이 반환한 tool call 이름과 arguments를 kubectl-ai Tool 실행으로 연결할 수 있다.

### 기본 Kubernetes 도구

`Agent.Init` 내부에서는 session executor를 만든 뒤 다음 도구를 등록한다.

- `tools.NewBashTool(executor)`
- `tools.NewKubectlTool(executor)`

자체 ReAct 루프를 만들 경우에도 동일하게 executor를 구성하고 이 도구들을 registry에 등록하면 kubectl / bash 실행 커넥터로 사용할 수 있다.

### gollm

kubectl-ai의 `gollm` 패키지도 공개 interface를 제공한다.

- `Client.StartChat(systemPrompt, model)`
- `Chat.SetFunctionDefinitions`
- `Chat.SendStreaming`
- `FunctionCall`
- `FunctionCallResult`

따라서 k8s-assistant가 자체 loop에서 LLM chat을 직접 열고, `tools.AllTools()`에서 얻은 function definitions를 주입하는 구조도 가능하다.

## k8s-assistant가 직접 구현해야 하는 부분

ReAct 루프를 k8s-assistant로 가져오면 다음 책임을 직접 가져와야 한다.

1. system prompt 생성 및 custom prompt 병합
2. chat session 초기화
3. tool/function definitions 주입
4. 사용자 입력을 LLM 메시지로 변환
5. streaming 응답 수신
6. text 응답과 function call 분리
7. tool call 이름과 arguments 검증
8. interactive command 차단
9. 리소스 변경 명령에 대한 승인 요청
10. 승인 결과에 따른 실행 또는 거절 결과를 LLM에 다시 전달
11. tool 실행 결과를 `FunctionCallResult` 또는 observation text로 변환
12. max iteration 관리
13. session 저장 및 UI message 변환
14. MCP server 연결 및 tool 등록

이 중 상당수는 현재 `pkg/agent/conversation.go`의 `Run`, `analyzeToolCalls`, `DispatchToolCalls`, `handleChoice`에 들어 있다. 일부 메서드는 exported이지만 내부 상태인 `pendingFunctionCalls`, `currChatContent`, `llmChat`, `executor`, `workDir`에 의존한다. 즉, 필요한 동작만 골라서 호출하기보다는 자체 상태 머신을 만들어야 한다.

## 제안 구조

```text
k8s-assistant
  internal/react
    loop.go              # 자체 ReAct loop
    state.go             # iteration, pending tool calls, approval state
    approval.go          # 승인 요청/응답 처리
    prompt.go            # k8s-assistant system prompt 구성
    tool_result.go       # tool result -> LLM observation 변환

  internal/toolconnector
    registry.go          # kubectl-ai tools.Tools 구성
    kubectl.go           # NewKubectlTool 등록
    bash.go              # NewBashTool 등록 여부 제어
    mcp.go               # mcp.yaml 기반 MCP tool 등록

  internal/orchestrator
    gate.go              # LLM confidence / troubleshooting 필요 여부 판단
    troubleshooting.go   # trouble-shooting 호출 및 결과 주입
```

이 구조에서는 kubectl-ai의 `pkg/agent.Agent`를 직접 실행하지 않는다. 대신 k8s-assistant가 `gollm.Chat`과 `pkg/tools.Tools`를 연결해서 루프를 소유한다.

## Tool 실행 흐름

```text
User
  -> k8s-assistant ReAct loop
  -> gollm.Chat.SendStreaming
  -> text 또는 function call 수신
  -> tools.ParseToolInvocation
  -> IsInteractive / CheckModifiesResource 검사
  -> 필요 시 사용자 승인
  -> ToolCall.InvokeTool
  -> FunctionCallResult 또는 observation 생성
  -> 다시 gollm.Chat.SendStreaming
```

## trouble-shooting 연동 관점

ReAct 루프를 k8s-assistant가 소유하면 trouble-shooting 개입 지점을 더 명확히 제어할 수 있다.

현재처럼 kubectl-ai Agent 루프 안에서 custom prompt로 trouble-shooting 사용을 유도하면, LLM이 자체 지식으로 해결하려는 흐름과 trouble-shooting 결과가 충돌할 수 있다.

자체 루프에서는 다음 계약을 명시적으로 둘 수 있다.

- LLM이 높은 확신도로 단순 진단/조치를 판단하면 trouble-shooting을 호출하지 않는다.
- LLM이 낮은 확신도이거나 운영 지식이 필요한 문제라고 판단하면 trouble-shooting을 호출한다.
- trouble-shooting 결과가 들어온 뒤에는 LLM이 새로운 해결책을 임의 생성하지 않고, runbook/RAG 결과에 기반한 실행 계획만 작성한다.
- 실제 kubectl 실행은 항상 Tool connector를 통해 수행한다.
- 변경 명령은 k8s-assistant approval layer에서만 승인받는다.

이 방식이 현재 논의 중인 “간단한 건 직접 처리하고, 확신이 낮은 건 trouble-shooting으로 보강” 구조에 더 잘 맞는다.

## 리스크

### 구현량 증가

kubectl-ai Agent가 해주던 상태 머신을 k8s-assistant가 직접 가져와야 한다. 단순히 tool만 연결하는 수준보다 작업량이 크다.

### kubectl-ai 내부 변경 영향

Tool 계층은 공개 API에 가깝지만, Agent 루프 내부 동작을 참고해서 포팅하면 kubectl-ai 버전 변경 시 차이가 생길 수 있다.

### MCP 등록 방식

kubectl-ai의 `InitializeMCPClient`는 Agent 메서드이며 내부에서 `mcp.InitializeManager`와 `tools.RegisterTool`을 호출한다. 자체 루프에서는 MCP manager 초기화와 tool 등록 흐름을 별도로 구현해야 한다.

### 승인 UX 중복 제거 필요

현재 승인 요청은 kubectl-ai Agent가 만든다. 루프를 k8s-assistant로 옮기면 승인 요청은 k8s-assistant에서만 만들어야 한다. 그래야 기존에 발생한 `y/n` 입력과 kubectl-ai 번호 입력이 섞이는 문제가 사라진다.

## 권장 단계

### 1단계: 현재 구조 유지 + 판단 게이트 추가

단기적으로는 kubectl-ai Agent를 유지하고, k8s-assistant orchestrator에서 trouble-shooting 호출 여부를 판단하는 게 가장 안전하다. 다만 이 방식은 kubectl-ai 내부 ReAct 흐름을 완전히 통제하지 못한다.

### 2단계: Tool connector 계층 분리

`internal/toolconnector`를 먼저 만든다. 여기서 kubectl-ai의 `tools.Tools`, `NewKubectlTool`, `NewBashTool`, MCP tool 등록을 담당하게 한다.

이 단계에서는 아직 Agent 루프를 바꾸지 않아도 된다. 이후 자체 ReAct 루프에서 그대로 재사용할 수 있는 기반을 만든다.

### 3단계: k8s-assistant ReAct loop MVP

다음 최소 기능만 갖춘 루프를 만든다.

- LLM streaming 응답 처리
- function call 파싱
- kubectl tool 실행
- 변경 명령 승인
- tool result 재주입
- max iteration 종료

초기에는 session 저장, meta command, tool-use shim, 복잡한 MCP status UI는 제외하는 것이 좋다.

### 4단계: kubectl-ai Agent 의존 제거 여부 결정

MVP가 안정화되면 `internal/agent/setup.go`에서 `kubectl-ai/pkg/agent.Agent` 직접 실행을 제거할지 결정한다.

## 최종 판단

ReAct 루프를 k8s-assistant에서 구현하고 kubectl-ai를 Tool 커넥터로만 사용하는 구조는 가능하다. 하지만 kubectl-ai가 이 구조를 위해 명확히 분리된 라이브러리 형태를 제공하는 것은 아니다.

따라서 이 변경은 작은 리팩터링이 아니라 아키텍처 변경이다. trouble-shooting 개입 기준, 승인 흐름, 반복 제어, 언어 정책, 실행 정책을 k8s-assistant가 확실히 소유해야 한다면 자체 ReAct 루프 구현이 맞다. 반대로 빠르게 안정화하는 것이 우선이면 현재 Agent 루프를 유지하고 외부 판단 게이트만 보강하는 것이 낫다.

## 관련 코드

- `internal/agent/setup.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/agent/conversation.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/agent/mcp_client.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools/interfaces.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools/tools.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools/kubectl_tool.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools/bash_tool.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools/mcp_tool.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/pkg/mcp/manager.go`
- `github.com/GoogleCloudPlatform/kubectl-ai/gollm/interfaces.go`
