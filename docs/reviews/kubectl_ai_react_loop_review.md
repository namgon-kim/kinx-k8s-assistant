# kubectl-ai ReAct 루프 분리 상세 설계

## 목적

k8s-assistant가 kubectl-ai의 ReAct Loop를 직접 소유하고, kubectl-ai는 Tool 커넥터 계층(gollm + pkg/tools)으로만 사용한다. 이 문서는 kubectl-ai v0.0.31 소스를 직접 분석하여 루프 이식에 필요한 모든 세부 사항을 기술한다.

---

## 1. kubectl-ai ReAct Loop 전체 구조

### 1.1 Agent 핵심 필드

`pkg/agent/conversation.go`의 `Agent` struct에서 ReAct 루프 동작에 관여하는 필드는 다음과 같다.

```go
type Agent struct {
    Input  chan any  // 사용자 입력 수신 (UserInputResponse, UserChoiceResponse, io.EOF)
    Output chan any  // UI 메시지 송신 (api.Message)

    RunOnce      bool   // true면 한 번만 실행 후 종료
    InitialQuery string

    // 루프 상태
    pendingFunctionCalls []ToolCallAnalysis // 현재 iteration의 pending tool calls
    currChatContent      []any              // 다음 LLM 호출에 보낼 콘텐츠 누적 버퍼 (타입: string | gollm.FunctionCallResult)
    currIteration        int

    LLM     gollm.Client
    Model   string
    Provider string

    MaxIterations   int
    SkipPermissions bool // true면 변경 명령 승인 없이 바로 실행

    Tools             tools.Tools
    EnableToolUseShim bool // shim mode: function calling 미지원 모델용

    llmChat  gollm.Chat      // 현재 활성 chat session
    workDir  string          // 임시 작업 디렉터리
    executor sandbox.Executor

    Session          *api.Session
    ChatMessageStore api.ChatMessageStore
    SessionBackend   string

    MCPClientEnabled bool
    mcpManager       *mcp.Manager

    cancel context.CancelFunc
}
```

### 1.2 Init() 흐름

`Agent.Init(ctx)`는 루프 시작 전 한 번만 호출된다.

```
Init(ctx)
  1. Input / Output 채널 생성 (버퍼 10)
  2. currIteration = 0, currChatContent = []
  3. Session 검증 및 ChatMessageStore 초기화 (InMemoryChatStore)
  4. os.MkdirTemp() → workDir 생성
  5. Sandbox 타입에 따라 executor 선택:
       ""       → sandbox.NewLocalExecutor()
       "k8s"    → sandbox.NewKubernetesSandbox()
       "seatbelt" → sandbox.NewSeatbeltExecutor() [macOS only]
  6. s.Tools = s.Tools.CloneWithExecutor(executor)
  7. s.Tools.RegisterTool(tools.NewBashTool(executor))
  8. s.Tools.RegisterTool(tools.NewKubectlTool(executor))
  9. generatePrompt() → system prompt 생성 (템플릿 + ExtraPromptPaths 병합)
  10. gollm.NewRetryChat(s.LLM.StartChat(systemPrompt, model), retryConfig) → llmChat
  11. llmChat.Initialize(ChatMessageStore.ChatMessages()) // 세션 복원 시 히스토리 주입
  12. if MCPClientEnabled: InitializeMCPClient(ctx)
  13. if !EnableToolUseShim:
        tools.AllTools() → FunctionDefinition 목록 수집
        알파벳 정렬 (KV cache 재사용 목적)
        llmChat.SetFunctionDefinitions(definitions)
```

### 1.2.1 addMessage() 이중 역할

`addMessage(source, type, payload)`는 루프 전반에서 사용되며 두 가지 역할을 동시에 수행한다.

```go
func (c *Agent) addMessage(source, messageType, payload) *api.Message:
  message := &api.Message{ID: uuid.New(), Source, Type, Payload, Timestamp}
  // 역할 1: ChatMessageStore에 영속화 (세션 저장)
  //         단, MessageTypeUserInputRequest는 예외 → UI 제어 신호이므로 저장 안 함
  if messageType != MessageTypeUserInputRequest:
    c.Session.ChatMessageStore.AddChatMessage(message)
    c.Session.LastModified = time.Now()
  // 역할 2: Output 채널로 송신 (UI에 메시지 전달)
  c.Output <- message
```

k8s-assistant 자체 루프에서도 모든 출력 메시지는 이 두 역할을 함께 처리해야 한다.

### 1.2.2 currChatContent []any 타입 다양성

`currChatContent`는 LLM에 보낼 다음 메시지를 누적하는 버퍼다. `any` 슬라이스이며 실제로 세 가지 타입이 섞인다.

| 타입 | 발생 시점 | 예시 |
|---|---|---|
| `string` | 사용자 최초 쿼리, shim 모드 tool 관찰 결과 | `"kubectl get pods 결과는 ..."`  |
| `gollm.FunctionCallResult` | tool 실행 결과 (non-shim 모드) | `{ID, Name, Result: map}` |
| `gollm.FunctionCall` (드물게) | 거부 응답이 특정 call ID를 참조할 때 | `handleChoice` case 3 |

`SendStreaming(ctx, currChatContent...)`은 이 슬라이스를 가변 인수로 받아 타입에 따라 자동으로 직렬화한다. shim 모드에서는 `FunctionCallResult`를 사용하지 않고 항상 `string` 관찰 텍스트를 넣는다.

### 1.3 Run() 상태 머신

`Agent.Run(ctx, initialQuery)`는 goroutine 안에서 무한 루프를 돈다. 상태는 `Session.AgentState`로 관리된다.

```
AgentState 종류:
  Idle / Done  → 사용자 입력 대기
  Running      → LLM 호출 및 tool 실행
  WaitingForInput → 변경 명령 승인 대기
  Exited       → 루프 종료
```

**전체 루프 흐름 (Run goroutine):**

```
[초기 처리 - initialQuery가 있을 때]
  addMessage(User, Text, initialQuery)
  handleMetaQuery(initialQuery)
    → meta query면: state=Done, 결과 출력
    → "exit"/"quit"이면: state=Exited, Output 채널 close
    → 일반 쿼리면: state=Running, currIteration=0,
                   currChatContent=[]any{initialQuery}

[메인 루프]
for {
  switch AgentState() {

  case Idle, Done:
    if RunOnce → Exited, return
    addMessage(Agent, UserInputRequest, ">>>")  // UI에 입력 요청 신호
    select {
      case <-ctx.Done(): return
      case userInput = <-Input:
        if io.EOF → Exited, 작별 메시지, return
        if SessionPickerResponse → LoadSession()
        if UserInputResponse:
          handleMetaQuery(query)
            → meta면: state=Done, 결과 출력
            → "exit"면: state=Exited, close(Output), return
            → WaitingForInput 상태 설정된 경우 continue (picker 대기)
          state=Running, currIteration=0,
          currChatContent=[]any{query.Query}
    }

  case WaitingForInput:
    if RunOnce → Exited, 에러, return
    select {
      case <-ctx.Done(): return
      case userInput = <-Input:
        if UserChoiceResponse:
          dispatchOK = handleChoice(ctx, response)
          if dispatchOK:
            DispatchToolCalls(ctx)
            pendingFunctionCalls = []
            state = Running
            currIteration++
          else:
            currIteration++
            pendingFunctionCalls = []
            state = Running
    }

  case Running:
    [아래 Running 블록 참조]

  case Exited:
    return
  }

  // Running 블록
  if AgentState() == Running {
    if currIteration >= MaxIterations:
      state = Done
      addMessage("Maximum number of iterations reached.")
      continue

    stream, err = llmChat.SendStreaming(ctx, currChatContent...)
    currChatContent = nil  // 보낸 후 즉시 비움

    if EnableToolUseShim:
      stream = candidateToShimCandidate(stream)  // ReAct JSON 파싱 shim

    // 스트리밍 수집
    var functionCalls []gollm.FunctionCall
    var streamedText string
    for response, err := range stream:
      candidate = response.Candidates()[0]
      for part := range candidate.Parts():
        if text, ok = part.AsText(): streamedText += text
        if calls, ok = part.AsFunctionCalls(): functionCalls = append(...)

    if streamedText != "":
      addMessage(Model, Text, streamedText)

    if len(functionCalls) == 0:
      state = Done
      currChatContent = []
      currIteration = 0
      pendingFunctionCalls = []
      continue  // 작업 완료

    toolCallAnalysisResults = analyzeToolCalls(ctx, functionCalls)
    pendingFunctionCalls = toolCallAnalysisResults

    // interactive 명령 차단
    if interactiveIdx >= 0:
      addMessage(Agent, Error, 에러 메시지)
      currChatContent = append(FunctionCallResult{error: ...})  // LLM에 실패 알림
      pendingFunctionCalls = []
      currIteration++
      continue

    // 변경 명령 승인
    if !SkipPermissions && modifiesResourceIdx >= 0:
      if RunOnce: Exited, 에러, return
      addMessage(Agent, UserChoiceRequest, {prompt, [Yes, Yes&DontAsk, No]})
      state = WaitingForInput
      continue  // WaitingForInput 루프로 분기

    // 승인 불필요 → 바로 실행
    DispatchToolCalls(ctx)
    currIteration++
    pendingFunctionCalls = []
  }
}
```

### 1.3.1 handleMetaQuery() - 내장 메타 명령

`handleMetaQuery(ctx, query)`는 Run() 루프에서 사용자 입력을 LLM에 보내기 전에 먼저 확인한다. 일치하면 LLM 호출 없이 직접 처리하고 `handled=true`를 반환한다.

| 명령 | 동작 |
|---|---|
| `clear` / `reset` | `ChatMessageStore.ClearChatMessages()` → `llmChat.Initialize([])` |
| `exit` / `quit` | `state=Exited` → `close(Output)` 후 goroutine 종료 |
| `model` | 현재 model 이름 반환 |
| `models` | `LLM.ListModels(ctx)` 결과 반환 |
| `tools` | `Tools.Names()` 목록 반환 |
| `session` | 세션 정보 표시 (filesystem backend만 의미 있음) |
| `save-session` | `SaveSession()` → 세션 ID 반환 |
| `sessions` | `ListSessions()` → 탭 구분 테이블 반환 |
| `resume-session <id>` | `LoadSession(id)` |

k8s-assistant 자체 루프에서는 이 메타 명령들 중 `clear` / `exit`는 반드시 구현해야 한다. 나머지는 Orchestrator의 기존 `/` 명령 체계로 흡수할 수 있다.

### 1.3.2 Output 채널 close() 종료 신호

루프가 정상 종료할 때(exit/quit 명령 또는 RunOnce 완료) `close(c.Output)`을 호출한다. Orchestrator(소비자)는 채널이 닫혔을 때 루프가 끝났음을 감지해야 한다.

```go
// exit/quit meta query 처리 시
c.setAgentState(AgentStateExited)
c.addMessage(Agent, Text, "It has been a pleasure assisting you...")
close(c.Output)
return

// io.EOF 수신 시 (stdin 닫힘)
c.setAgentState(AgentStateExited)
c.addMessage(Agent, Text, "It has been a pleasure assisting you...")
return  // Output은 닫지 않음 (차이점)
```

k8s-assistant 자체 루프에서는 `close()` 대신 별도 done 채널이나 context cancel을 쓸 수 있다. 단, 소비자가 루프 종료를 감지하는 계약을 명확히 정의해야 한다.

### 1.3.3 RunOnce 모드 제약사항

`RunOnce = true`는 CLI 단발 실행(`kubectl-ai --once "query"`)에 사용된다. k8s-assistant에서는 주로 자동화 파이프라인에서 활용될 수 있다.

| 상황 | RunOnce 동작 |
|---|---|
| 작업 완료 (function call 없음) | state=Exited, 정상 종료 |
| `WaitingForInput` 진입 | 즉시 에러 반환 (`RunOnce cannot handle user choice`) |
| interactive 명령 감지 | 즉시 에러 반환 |
| 변경 명령 감지 (`modifiesResource != "no"`) | 에러 반환 (단, `SkipPermissions=true`면 그냥 실행) |
| max iteration 도달 | 에러 없이 state=Exited 종료 |

에러는 `c.lastErr`에 저장되고 `Agent.LastErr()`로 조회한다.

### 1.4 analyzeToolCalls()

LLM이 반환한 `[]gollm.FunctionCall`을 분석해서 `[]ToolCallAnalysis`를 만든다.

```go
type ToolCallAnalysis struct {
    FunctionCall        gollm.FunctionCall  // LLM이 요청한 원본
    ParsedToolCall      *tools.ToolCall     // registry에서 찾은 실행 가능한 call
    IsInteractive       bool
    IsInteractiveError  error
    ModifiesResourceStr string              // "yes" | "no" | "unknown"
}

func analyzeToolCalls(ctx, toolCalls []FunctionCall) ([]ToolCallAnalysis, error):
  for each call:
    toolCall = Tools.ParseToolInvocation(ctx, call.Name, call.Arguments)
    isInteractive, err = toolCall.GetTool().IsInteractive(call.Arguments)
    modifiesResource = toolCall.GetTool().CheckModifiesResource(call.Arguments)
```

**판단 기준 - `IsInteractive`:**
- `kubectl exec -it` → true (interactive mode)
- `kubectl port-forward` → true
- `kubectl edit` → true
- 그 외 → false

**판단 기준 - `CheckModifiesResource`:**
- `kubectl apply`, `create`, `delete`, `patch`, `scale`, `rollout restart` 등 → `"yes"`
- `kubectl get`, `describe`, `logs`, `diff` 등 → `"no"`
- bash 명령어 → `"unknown"` (conservative: 항상 승인 요청)

### 1.5 DispatchToolCalls()

```go
func DispatchToolCalls(ctx) error:
  for each call in pendingFunctionCalls:
    // 1. 실행 알림
    addMessage(Model, ToolCallRequest, call.ParsedToolCall.Description())

    // 2. 실행
    output, err = call.ParsedToolCall.InvokeTool(ctx, InvokeToolOptions{
        Kubeconfig: c.Kubeconfig,
        WorkDir:    c.workDir,
        Executor:   c.executor,
    })

    // 3. 결과를 LLM 대화에 추가 (shim 모드 분기)
    if EnableToolUseShim:
      observation = fmt.Sprintf("Result of running %q:\n%v", call.Name, output)
      currChatContent = append(currChatContent, observation)  // plain text
      payload = observation
    else:
      result = tools.ToolResultToMap(output)
      currChatContent = append(currChatContent, gollm.FunctionCallResult{
          ID:     call.FunctionCall.ID,
          Name:   call.FunctionCall.Name,
          Result: result,
      })
      payload = result

    // 3-1. timeout 감지 (sandbox.ExecResult.StreamType == "timeout")
    if execResult.StreamType == "timeout":
      addMessage(Agent, Error, "\nTimeout reached after 7 seconds\n")
      // 이후 결과는 정상적으로 LLM에 전달됨 (에러 반환 안 함)

    // 4. UI 출력
    addMessage(Agent, ToolCallResponse, payload)
```

**Tool call 원자성 처리 정책**

코드에 명시된 설계 원칙:

> "The key idea is to treat all tool calls to be executed atomically or not. If all tool calls are readonly, it is straight forward. If some of the tool calls are not readonly, the permission is asked only ONCE for all the tool calls."

이 정책의 함의:
- LLM이 한 iteration에서 3개의 tool call을 반환했고, 그 중 하나라도 `modifiesResource != "no"`이면 **3개 전체에 대해 한 번의 승인**을 요청한다.
- 사용자가 No(거부)를 선택하면 `pendingFunctionCalls[0]`의 ID만 거부 결과로 LLM에 전달한다. 나머지 call은 결과 없이 버려진다.
- 이는 의도적인 설계이며, k8s-assistant 자체 루프에서도 동일하게 구현해야 한다.

### 1.6 handleChoice()

```go
func handleChoice(ctx, choice *UserChoiceResponse) (dispatchToolCalls bool):
  switch choice.Choice:
  case 1:  // Yes
    return true
  case 2:  // Yes and don't ask again
    SkipPermissions = true
    return true
  case 3:  // No
    currChatContent = append(FunctionCallResult{
        ID:   pendingFunctionCalls[0].FunctionCall.ID,
        Name: pendingFunctionCalls[0].FunctionCall.Name,
        Result: {"error": "User declined", "status": "declined", "retryable": false},
    })
    pendingFunctionCalls = []
    addMessage(Agent, Error, "Operation was skipped.")
    return false
```

### 1.7 ToolUseShim 모드

function calling을 지원하지 않는 모델(Ollama, LLaMA.cpp 등)을 위한 모드. `EnableToolUseShim = true`일 때 동작이 달라진다.

```
Init():
  SetFunctionDefinitions() 호출 안 함 (function calling 미등록)
  system prompt에 아래 JSON 형식 지시가 포함됨

Run():
  stream = candidateToShimCandidate(stream)
    → 스트리밍 텍스트 전체를 버퍼에 누적 (중간 yield 없음)
    → 스트림 종료 후 ```json ... ``` 블록 추출
    → parseReActResponse() → ReActResponse 파싱
    → ShimCandidate로 변환:
        Thought → ShimPart{text}
        Answer  → ShimPart{text}
        Action  → ShimPart{action} → AsFunctionCalls()에서 FunctionCall로 변환

DispatchToolCalls():
  FunctionCallResult 대신 plain text observation 사용
  currChatContent = append(currChatContent, "Result of running ...")
```

**shim 모드에서 LLM이 반환해야 하는 JSON 포맷:**

도구를 사용할 때:
```json
{
    "thought": "현재 상황에 대한 추론",
    "action": {
        "name": "kubectl",
        "reason": "이 도구를 선택한 이유 (100단어 이하)",
        "command": "kubectl get pods -n default",
        "modifies_resource": "no"
    }
}
```

최종 답변을 줄 때:
```json
{
    "thought": "최종 추론 과정",
    "answer": "사용자에게 전달할 최종 답변"
}
```

**`Action` 구조체:**
```go
type Action struct {
    Name             string `json:"name"`             // 도구 이름 (tool registry의 Name()과 일치해야 함)
    Reason           string `json:"reason"`
    Command          string `json:"command"`           // 실제 실행 명령
    ModifiesResource string `json:"modifies_resource"` // "yes" | "no" | "unknown"
}
```

`AsFunctionCalls()` 변환 시 `name` 필드는 별도 전달되므로 arguments map에서 삭제된다. `reason`과 `modifies_resource`는 arguments에 남아 있어 tool의 `CheckModifiesResource()` 대신 LLM 자체 판단을 그대로 전달한다.

### 1.8 System Prompt 구성

`generatePrompt(ctx, defaultTemplate, PromptData)`는 Go 템플릿을 사용하며 `PromptData`를 주입한다.

```go
type PromptData struct {
    Query                string       // (미사용: 템플릿에서 직접 참조 안 함)
    Tools                tools.Tools
    EnableToolUseShim    bool         // {{if .EnableToolUseShim}} 조건부 프롬프트
    SessionIsInteractive bool         // {{if .SessionIsInteractive}} 조건부 프롬프트
}

func (a *PromptData) ToolsAsJSON() string     // FunctionDefinition 목록을 JSON으로 직렬화
func (a *PromptData) ToolNames() string        // "kubectl, bash" 형식의 도구 이름 목록
```

**시스템 프롬프트 구조 (`systemprompt_template_default.txt`):**

```
[공통] You are kubectl-ai, an AI assistant for Kubernetes...

[shim 모드 전용]
  - <tools> 태그 안에 ToolsAsJSON() 결과 삽입
  - LLM에게 ```json 블록으로 응답하도록 지시
  - 도구 사용 / 최종 답변 두 가지 JSON 포맷 명시

[non-shim 모드]
  - function calling을 사용하므로 도구 스키마를 프롬프트에 포함하지 않음
  - 행동 지침만 포함: "도구를 사용해서 해결하라, 직접 명령어를 알려주지 말고 실행하라"

[공통 명령 지침]
  - kubectl 명령 형식 강제: kubectl <verb> 순서 고정
  - non-interactive 명령 선호

[SessionIsInteractive == true 전용]
  - 리소스 생성 전 사용자 확인 절차 (네임스페이스, 이미지, 스토리지 등 구체 정보 수집)
  - 매니페스트를 가정 없이 생성하지 말 것
  - 구성 요약 후 사용자 확인 요청

[공통 remember]
  - 현재 클러스터 상태 먼저 조회
  - 확신이 없으면 더 조회하고 답변
  - 이모지 사용 허용
```

k8s-assistant 자체 루프에서 시스템 프롬프트를 작성할 때:
- `ExtraPromptPaths`에 k8s-assistant 전용 지침 파일을 추가하거나
- 기본 템플릿을 `PromptTemplateFile`로 완전히 교체할 수 있다
- troubleshooting 결과 활용 지침, 승인 흐름 설명, 언어 정책 등을 추가 prompt로 주입

---

## 2. gollm 인터페이스 상세

`github.com/GoogleCloudPlatform/kubectl-ai/gollm@v0.0.0-20260325022250-08cf256aa2f5/interfaces.go`

### 2.1 Client

```go
type Client interface {
    io.Closer
    StartChat(systemPrompt, model string) Chat
    GenerateCompletion(ctx context.Context, req *CompletionRequest) (CompletionResponse, error)
    SetResponseSchema(schema *Schema) error
    ListModels(ctx context.Context) ([]string, error)
}
```

- `gollm.NewClient(provider, ...)` → 프로바이더별 구현체 반환
- `StartChat()` → 새 Chat session 시작. system prompt와 model을 여기서 고정

### 2.2 Chat

```go
type Chat interface {
    Send(ctx context.Context, contents ...any) (ChatResponse, error)
    SendStreaming(ctx context.Context, contents ...any) (ChatResponseIterator, error)
    SetFunctionDefinitions(functionDefinitions []*FunctionDefinition) error
    IsRetryableError(error) bool
    Initialize(messages []*api.Message) error
}
```

- `SendStreaming(ctx, contents...)` → `ChatResponseIterator` 반환. contents는 `string`, `gollm.FunctionCallResult`, `gollm.FunctionCall` 등을 혼합 가능
- `Initialize(messages)` → 이전 대화 히스토리로 chat 상태를 초기화 (세션 복원)
- `SetFunctionDefinitions()` → function calling 모드에서만 사용. shim 모드에서는 미호출

### 2.3 핵심 타입

```go
// LLM이 tool 호출을 요청할 때 반환
type FunctionCall struct {
    ID        string         `json:"id,omitempty"`
    Name      string         `json:"name,omitempty"`
    Arguments map[string]any `json:"arguments,omitempty"`
}

// tool 실행 결과를 LLM에 다시 보낼 때 사용
type FunctionCallResult struct {
    ID     string         `json:"id,omitempty"`
    Name   string         `json:"name,omitempty"`
    Result map[string]any `json:"result,omitempty"`
}

// tool 스키마 정의
type FunctionDefinition struct {
    Name        string  `json:"name,omitempty"`
    Description string  `json:"description,omitempty"`
    Parameters  *Schema `json:"parameters,omitempty"`
}

// streaming iterator (Go 1.23 iter.Seq2)
type ChatResponseIterator iter.Seq2[ChatResponse, error]

// 응답 파싱
type ChatResponse interface {
    Candidates() []Candidate
    UsageMetadata() any
}

type Candidate interface {
    fmt.Stringer
    Parts() []Part
}

type Part interface {
    AsText() (string, bool)
    AsFunctionCalls() ([]FunctionCall, bool)
}
```

### 2.4 RetryChat Wrapper

`gollm.NewRetryChat(chat, config)`는 Chat을 감싸서 자동 재시도를 제공한다.

```go
type RetryConfig struct {
    MaxAttempts    int           // 기본 3
    InitialBackoff time.Duration // 기본 10s
    MaxBackoff     time.Duration // 기본 60s
    BackoffFactor  float64       // 기본 2
    Jitter         bool
}
```

k8s-assistant 자체 루프에서도 그대로 사용할 수 있다.

---

## 3. tools 인터페이스 상세

`pkg/tools/interfaces.go`, `pkg/tools/tools.go`

### 3.1 Tool 인터페이스

```go
type Tool interface {
    Name() string
    Description() string
    FunctionDefinition() *gollm.FunctionDefinition
    Run(ctx context.Context, args map[string]any) (any, error)
    IsInteractive(args map[string]any) (bool, error)
    CheckModifiesResource(args map[string]any) string  // "yes" | "no" | "unknown"
}
```

### 3.2 Tools Registry

```go
type Tools struct {
    tools map[string]Tool
}

func (t *Tools) RegisterTool(tool Tool)
func (t *Tools) Lookup(name string) Tool
func (t *Tools) AllTools() []Tool
func (t *Tools) Names() []string
func (t *Tools) CloneWithExecutor(executor sandbox.Executor) Tools
```

**`CloneWithExecutor()` 동작:**

```go
func (t *Tools) CloneWithExecutor(executor sandbox.Executor) Tools:
  새 Tools 인스턴스 생성
  for name, tool in t.tools:
    if tool is *CustomTool:
      newTools[name] = ct.CloneWithExecutor(executor)  // executor 교체
    else:
      newTools[name] = tool  // 동일 인스턴스 재사용 (MCP tool 포함)
```

`KubectlTool`과 `BashTool`은 `NewKubectlTool(executor)` / `NewBashTool(executor)` 생성 시 executor가 이미 바인딩되므로 `CloneWithExecutor` 이후에 `RegisterTool`로 다시 등록해야 한다. `CloneWithExecutor`는 `CustomTool`만 교체하고 나머지는 그대로 유지한다.

Init() 전형적 순서:
```go
s.Tools = s.Tools.CloneWithExecutor(executor)    // CustomTool executor 교체
s.Tools.RegisterTool(tools.NewBashTool(executor)) // 새 executor로 BashTool 재등록
s.Tools.RegisterTool(tools.NewKubectlTool(executor)) // 새 executor로 KubectlTool 재등록
```

### 3.2.1 CustomTool 로딩

`tools.LoadAndRegisterCustomTools(configPath)`는 YAML 파일에서 커스텀 도구를 로드해서 global registry에 등록한다.

```yaml
# 예: helm, kustomize 커스텀 도구 정의
- name: helm
  description: "Helm chart management tool"
  command: "helm"
  args: ["{{.command}}"]
  schema:
    type: object
    properties:
      command:
        type: string
        description: "helm command to run"
```

k8s-assistant의 현재 `setup.go`는 이 함수를 사용해서 `helm`, `kustomize` 등 커스텀 도구를 로드한다. 자체 루프에서도 Init 전에 동일하게 호출해야 한다.

**주의:** `LoadAndRegisterCustomTools`는 global `allTools`에 등록한다. 자체 루프에서 session-local registry를 쓰려면 global 등록 후 `tools.Default()`를 base로 사용하거나, 직접 registry에 등록하는 래퍼를 만들어야 한다.

### 3.3 ParseToolInvocation + InvokeTool

```go
// LLM 요청 → ToolCall 객체로 파싱
func (t *Tools) ParseToolInvocation(ctx, name string, arguments map[string]any) (*ToolCall, error)

type InvokeToolOptions struct {
    WorkDir    string
    Kubeconfig string
    Executor   sandbox.Executor
}

// 실제 실행 (context에 kubeconfig, workDir, executor 주입)
func (t *ToolCall) InvokeTool(ctx context.Context, opt InvokeToolOptions) (any, error)
```

InvokeTool 내부:
1. `journal.Recorder`에 tool-request 이벤트 기록
2. context에 옵션 주입 (아래 Context Key 참조)
3. `t.tool.Run(ctx, t.arguments)` 실제 실행
4. tool-response 이벤트 기록

**Context Key 기반 설정 전달:**

`InvokeTool`은 `InvokeToolOptions`를 context에 주입해서 하위 `tool.Run()`에 전달한다. 개별 tool 구현체는 이 key로 설정을 꺼낸다.

```go
const (
    KubeconfigKey ContextKey = "kubeconfig"  // kubectl이 사용할 kubeconfig 경로
    WorkDirKey    ContextKey = "work_dir"    // 작업 디렉터리 (bash tool의 CWD)
    ExecutorKey   ContextKey = "executor"   // sandbox.Executor 인스턴스
)

// InvokeTool 내부 주입
ctx = context.WithValue(ctx, KubeconfigKey, opt.Kubeconfig)
ctx = context.WithValue(ctx, WorkDirKey, opt.WorkDir)
ctx = context.WithValue(ctx, ExecutorKey, opt.Executor)
```

k8s-assistant 자체 루프에서 `InvokeToolOptions`를 구성할 때 이 세 값이 모두 채워져야 한다. `Executor`가 nil이면 tool 내부에서 default local executor를 사용하는 경우도 있지만, 명시적으로 주입하는 것이 안전하다.

### 3.4 ToolResultToMap

```go
func ToolResultToMap(result any) (map[string]any, error)
```

- string → `{"content": str}`
- nil → `{"content": ""}`
- 그 외 → JSON 직렬화 후 map으로 변환
- JSON 실패 → `{"content": result}`

### 3.5 기본 도구 등록

```go
// Init()에서 등록
s.Tools.RegisterTool(tools.NewBashTool(executor))
s.Tools.RegisterTool(tools.NewKubectlTool(executor))
```

- `KubectlTool`: `kubectl get/apply/delete/...` 실행. kubeconfig는 context에서 추출
- `BashTool`: `bash -c <command>` 실행. `IsInteractive` 항상 false. `CheckModifiesResource` → `"unknown"`

### 3.6 IsInteractiveCommand (도구 레벨 구현체)

```go
func IsInteractiveCommand(command string) (bool, error):
  words := strings.Fields(command)
  base := filepath.Base(words[0])
  if base != "kubectl": return false, nil

  isExec := strings.Contains(command, " exec ") && strings.Contains(command, " -it")
  isPortForward := strings.Contains(command, " port-forward ")
  isEdit := strings.Contains(command, " edit ")

  if isExec || isPortForward || isEdit:
    return true, fmt.Errorf("interactive mode not supported for kubectl...")
  return false, nil
```

---

## 4. api 패키지 메시지 타입

`pkg/api/`에 정의된 메시지 타입들. Output 채널로 전달된다.

```go
const (
    MessageTypeText              // 텍스트 응답 (LLM answer)
    MessageTypeError             // 에러 메시지
    MessageTypeToolCallRequest   // tool 실행 시작 알림
    MessageTypeToolCallResponse  // tool 실행 결과
    MessageTypeUserInputRequest  // 사용자 텍스트 입력 요청 (">>>")
    MessageTypeUserChoiceRequest // 승인/거부 선택 요청
)

const (
    MessageSourceUser  // 사용자 발신
    MessageSourceModel // LLM 발신
    MessageSourceAgent // Agent 자체 발신 (에러, 알림 등)
)

type Message struct {
    ID        string
    Source    MessageSource
    Type      MessageType
    Payload   any
    Timestamp time.Time
}

// Input 채널로 보내는 타입들
type UserInputResponse struct {
    Query string
}
type UserChoiceResponse struct {
    Choice int  // 1=Yes, 2=Yes&DontAsk, 3=No
}
type UserChoiceRequest struct {
    Prompt  string
    Options []UserChoiceOption
}
type UserChoiceOption struct {
    Value string
    Label string
}

type AgentState string
const (
    AgentStateIdle           AgentState = "idle"
    AgentStateRunning        AgentState = "running"
    AgentStateWaitingForInput AgentState = "waiting_for_input"
    AgentStateDone           AgentState = "done"
    AgentStateExited         AgentState = "exited"
    AgentStateInitializing   AgentState = "initializing"
)
```

---

## 5. k8s-assistant가 재사용할 부분

kubectl-ai에서 그대로 사용 가능한 공개 API:

| 구성 요소 | 패키지 | 재사용 여부 |
|---|---|---|
| `gollm.Client` | gollm | 그대로 사용 |
| `gollm.Chat` / `NewRetryChat` | gollm | 그대로 사용 |
| `gollm.FunctionCall` / `FunctionCallResult` | gollm | 그대로 사용 |
| `gollm.ChatResponseIterator` | gollm | 그대로 사용 |
| `tools.Tool` 인터페이스 | pkg/tools | 그대로 사용 |
| `tools.Tools` registry | pkg/tools | 그대로 사용 |
| `tools.ParseToolInvocation` | pkg/tools | 그대로 사용 |
| `tools.ToolCall.InvokeTool` | pkg/tools | 그대로 사용 |
| `tools.ToolResultToMap` | pkg/tools | 그대로 사용 |
| `tools.NewKubectlTool` | pkg/tools | 그대로 사용 |
| `tools.NewBashTool` | pkg/tools | 그대로 사용 |
| `tools.IsInteractiveCommand` | pkg/tools | 그대로 사용 |
| `sandbox.NewLocalExecutor` | pkg/sandbox | 그대로 사용 |
| `api.Message` / `MessageType` 등 | pkg/api | 그대로 사용 |

---

## 6. k8s-assistant가 직접 구현해야 하는 부분

kubectl-ai `Agent.Run()`의 로직을 k8s-assistant가 직접 가져와야 하는 책임:

| 책임 | kubectl-ai 위치 | k8s-assistant 구현 위치 |
|---|---|---|
| system prompt 생성 | `generatePrompt()` + 템플릿 | `internal/react/prompt.go` |
| chat session 초기화 | `Init()` | `internal/react/loop.go` |
| function definitions 주입 | `Init()` | `internal/react/loop.go` |
| streaming 응답 수신 및 파싱 | `Run()` Running 블록 | `internal/react/loop.go` |
| text / function call 분리 | `Run()` streaming 루프 | `internal/react/loop.go` |
| analyzeToolCalls | `analyzeToolCalls()` | `internal/react/loop.go` |
| interactive 명령 차단 | `Run()` | `internal/react/loop.go` |
| 변경 명령 승인 요청 | `Run()` WaitingForInput | `internal/react/approval.go` |
| 승인/거부 처리 → LLM 재주입 | `handleChoice()` | `internal/react/approval.go` |
| DispatchToolCalls | `DispatchToolCalls()` | `internal/react/loop.go` |
| tool result → FunctionCallResult | `DispatchToolCalls()` | `internal/react/tool_result.go` |
| max iteration 관리 | `Run()` | `internal/react/loop.go` |
| ToolUseShim 처리 | `candidateToShimCandidate()` | `internal/react/shim.go` (optional) |
| MCP server 초기화 및 tool 등록 | `InitializeMCPClient()` | `internal/toolconnector/mcp.go` |
| session 저장/복원 | `SaveSession()` / `LoadSession()` | `internal/react/session.go` (optional) |
| state 머신 관리 | `AgentState` | `internal/react/state.go` |

---

## 7. 제안 구조

```
internal/
  react/
    loop.go          # 메인 ReAct 루프 (Init + Run 해당 로직)
    state.go         # LoopState, iteration counter, currChatContent 버퍼
    approval.go      # 변경 명령 승인 요청/응답 처리
    prompt.go        # system prompt 조립 (kubectl-ai 템플릿 + k8s-assistant 확장)
    tool_result.go   # tool 실행 결과 → LLM observation 변환
    shim.go          # ToolUseShim (선택적, 비지원 모델 대응용)

  toolconnector/
    registry.go      # tools.Tools 구성 및 executor 주입
    kubectl.go       # NewKubectlTool 등록
    bash.go          # NewBashTool 등록 (enable 여부 설정 가능)
    mcp.go           # mcp.yaml 기반 MCP tool 등록

  orchestrator/
    orchestrator.go  # 기존 역할 유지: readline 루프, meta 명령, 출력 처리
    gate.go          # NEW: troubleshooting 개입 필요 여부 판단
    troubleshooting.go # 기존 역할 유지: TroubleshootingFlow
```

이 구조에서 `internal/agent/setup.go`의 `AgentWrapper`와 kubectl-ai `pkg/agent.Agent` 직접 실행을 제거하고, `internal/react/loop.go`가 루프의 주인이 된다.

---

## 8. 자체 ReAct 루프 구현

### 8.1 핵심 타입

```go
// internal/react/state.go

type LoopState int

const (
    StateIdle LoopState = iota
    StateRunning
    StateWaitingApproval
    StateDone
    StateExited
)

type ReactLoop struct {
    // gollm
    llmClient gollm.Client
    llmChat   gollm.Chat

    // tools
    tools    tools.Tools
    executor sandbox.Executor
    workDir  string
    kubeconfig string

    // 루프 상태
    state           LoopState
    currIteration   int
    maxIterations   int
    currChatContent []any
    pendingCalls    []PendingCall

    skipPermissions   bool
    enableToolUseShim bool

    // k8s-assistant 확장
    troubleshooting *TroubleshootingFlow
    approval        *ApprovalHandler
    out             OutputSink // Orchestrator에 메시지 전달
}

type PendingCall struct {
    FunctionCall   gollm.FunctionCall
    ParsedToolCall *tools.ToolCall
    IsInteractive  bool
    InteractiveErr error
    ModifiesResource string
}
```

### 8.2 루프 초기화

```go
// internal/react/loop.go

func (r *ReactLoop) Init(ctx context.Context, systemPrompt string) error {
    var err error

    // 임시 작업 디렉터리
    r.workDir, err = os.MkdirTemp("", "k8s-assistant-*")
    if err != nil {
        return err
    }

    // executor
    r.executor = sandbox.NewLocalExecutor()

    // tool 등록
    r.tools = r.tools.CloneWithExecutor(r.executor)
    r.tools.RegisterTool(tools.NewBashTool(r.executor))
    r.tools.RegisterTool(tools.NewKubectlTool(r.executor))
    // MCP tools는 toolconnector/mcp.go에서 별도 등록

    // LLM chat session
    r.llmChat = gollm.NewRetryChat(
        r.llmClient.StartChat(systemPrompt, r.model),
        gollm.RetryConfig{
            MaxAttempts:    3,
            InitialBackoff: 10 * time.Second,
            MaxBackoff:     60 * time.Second,
            BackoffFactor:  2,
            Jitter:         true,
        },
    )

    // function definitions 주입 (shim 모드 아닐 때만)
    if !r.enableToolUseShim {
        var defs []*gollm.FunctionDefinition
        for _, tool := range r.tools.AllTools() {
            defs = append(defs, tool.FunctionDefinition())
        }
        sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
        if err := r.llmChat.SetFunctionDefinitions(defs); err != nil {
            return err
        }
    }

    r.state = StateIdle
    return nil
}
```

### 8.3 메인 루프

```go
func (r *ReactLoop) Run(ctx context.Context, query string) error {
    r.currChatContent = []any{query}
    r.currIteration = 0
    r.state = StateRunning

    for {
        if r.state != StateRunning {
            break
        }
        if err := r.runIteration(ctx); err != nil {
            return err
        }
    }
    return nil
}

func (r *ReactLoop) runIteration(ctx context.Context) error {
    if r.currIteration >= r.maxIterations {
        r.out.Send(MessageMaxIterationsReached)
        r.state = StateDone
        return nil
    }

    // ── [1] LLM 호출 ──────────────────────────────────────────────
    stream, err := r.llmChat.SendStreaming(ctx, r.currChatContent...)
    if err != nil {
        return err
    }
    r.currChatContent = nil

    // ── [2] 스트리밍 응답 수집 ──────────────────────────────────────
    var streamedText string
    var functionCalls []gollm.FunctionCall

    for response, err := range stream {
        if err != nil {
            return err
        }
        if response == nil || len(response.Candidates()) == 0 {
            break
        }
        for _, part := range response.Candidates()[0].Parts() {
            if text, ok := part.AsText(); ok {
                streamedText += text
            }
            if calls, ok := part.AsFunctionCalls(); ok {
                functionCalls = append(functionCalls, calls...)
            }
        }
    }

    // ── [3] 텍스트 응답 출력 ─────────────────────────────────────
    if streamedText != "" {
        r.out.Send(OutputText{Text: streamedText})
        // troubleshooting 개입 판단 시점
        r.troubleshooting.AfterLLMText(streamedText)
    }

    // ── [4] function call 없으면 종료 ───────────────────────────
    if len(functionCalls) == 0 {
        r.state = StateDone
        r.currIteration = 0
        return nil
    }

    // ── [5] tool call 분석 ───────────────────────────────────────
    pending, err := r.analyzeToolCalls(ctx, functionCalls)
    if err != nil {
        return err
    }
    r.pendingCalls = pending

    // ── [6] interactive 명령 차단 ────────────────────────────────
    for _, call := range pending {
        if call.IsInteractive {
            r.out.Send(OutputError{Err: call.InteractiveErr})
            r.currChatContent = append(r.currChatContent, gollm.FunctionCallResult{
                ID:     call.FunctionCall.ID,
                Name:   call.FunctionCall.Name,
                Result: map[string]any{"error": call.InteractiveErr.Error()},
            })
            r.pendingCalls = nil
            r.currIteration++
            return nil
        }
    }

    // ── [7] 변경 명령 승인 요청 ──────────────────────────────────
    if !r.skipPermissions && r.hasModifyingCalls() {
        r.state = StateWaitingApproval
        return r.approval.RequestApproval(ctx, r.pendingCalls)
        // 승인 응답은 HandleApproval()에서 처리 후 RunIteration 재개
    }

    // ── [8] tool 실행 ─────────────────────────────────────────────
    return r.dispatchToolCalls(ctx)
}

func (r *ReactLoop) HandleApproval(ctx context.Context, choice int) error {
    switch choice {
    case 1: // Yes
        if err := r.dispatchToolCalls(ctx); err != nil {
            return err
        }
    case 2: // Yes and don't ask again
        r.skipPermissions = true
        if err := r.dispatchToolCalls(ctx); err != nil {
            return err
        }
    case 3: // No
        r.currChatContent = append(r.currChatContent, gollm.FunctionCallResult{
            ID:     r.pendingCalls[0].FunctionCall.ID,
            Name:   r.pendingCalls[0].FunctionCall.Name,
            Result: map[string]any{
                "error":     "User declined to run this operation.",
                "status":    "declined",
                "retryable": false,
            },
        })
        r.pendingCalls = nil
    }
    r.currIteration++
    r.state = StateRunning
    return nil
}
```

### 8.4 dispatchToolCalls

```go
func (r *ReactLoop) dispatchToolCalls(ctx context.Context) error {
    for _, call := range r.pendingCalls {
        r.out.Send(OutputToolCall{Description: call.ParsedToolCall.Description()})

        output, err := call.ParsedToolCall.InvokeTool(ctx, tools.InvokeToolOptions{
            Kubeconfig: r.kubeconfig,
            WorkDir:    r.workDir,
            Executor:   r.executor,
        })
        if err != nil {
            r.out.Send(OutputError{Err: err})
            return err
        }

        if r.enableToolUseShim {
            obs := fmt.Sprintf("Result of running %q:\n%v", call.FunctionCall.Name, output)
            r.currChatContent = append(r.currChatContent, obs)
            r.out.Send(OutputToolResult{Content: obs})
        } else {
            result, err := tools.ToolResultToMap(output)
            if err != nil {
                return err
            }
            r.currChatContent = append(r.currChatContent, gollm.FunctionCallResult{
                ID:     call.FunctionCall.ID,
                Name:   call.FunctionCall.Name,
                Result: result,
            })
            r.out.Send(OutputToolResult{Content: result})
        }
    }

    r.pendingCalls = nil
    r.currIteration++
    r.state = StateRunning
    return nil
}
```

---

## 9. Orchestrator 연동 변경점

현재 Orchestrator는 `AgentWrapper`를 통해 kubectl-ai `Agent`와 채널로 통신한다. 루프를 가져오면 다음과 같이 바뀐다.

### 현재 (kubectl-ai Agent 위임)

```
Orchestrator
  → AgentWrapper.Start(query)
  → kubectl-ai Agent.Run() [goroutine]
      → LLM 호출, tool 실행, 승인 요청
  ← Output 채널 수신
  → handleMessage(msg)
```

### 변경 후 (k8s-assistant 직접 소유)

```
Orchestrator
  → ReactLoop.Init(ctx, systemPrompt)
  → ReactLoop.Run(ctx, query)  // 동기 또는 goroutine
      → runIteration() 반복
          → LLM 호출
          → out.Send() → Orchestrator 출력 처리
          → 승인 요청 시: out.Send(ApprovalRequest)
  ← Orchestrator.HandleApprovalInput(choice)
  → ReactLoop.HandleApproval(ctx, choice)
  → runIteration() 재개
```

Orchestrator의 `handleMessage()` 역할은 `OutputSink` 인터페이스로 분리하거나, 기존 채널 패턴을 유지하되 채널 발신자를 `ReactLoop`으로 교체할 수 있다.

---

## 10. Troubleshooting 개입 지점

자체 루프에서는 다음 시점에 troubleshooting을 개입시킬 수 있다.

```
[개입 시점 1] 텍스트 응답 수신 후 (AfterLLMText)
  LLM이 "CrashLoopBackOff", "ImagePullBackOff" 등 에러 키워드를 언급하면
  troubleshooting 플로우를 시작할지 사용자에게 질문

[개입 시점 2] 사용자 입력 수신 전 (BeforeUserInput)
  troubleshooting 결과가 있으면 그 내용을 다음 LLM 입력으로 주입

[개입 시점 3] DispatchToolCalls 결과 수신 후
  tool 실행 결과에 에러가 포함되면 troubleshooting runbook 검색

[주입 방법]
  troubleshooting 결과를 currChatContent에 prepend하여 다음 LLM iteration에 포함
  예: currChatContent = append([]any{runbookObservation}, currChatContent...)
```

현재 구조(kubectl-ai Agent 위임)에서는 이 개입 시점을 prompt 수준에서만 간접 제어할 수 있다. 자체 루프에서는 iteration 경계마다 직접 개입할 수 있다.

---

## 11. MCP Tool 연동

kubectl-ai의 `InitializeMCPClient()`는 `Agent` 메서드로 묶여 있다. 자체 루프에서는 분리 구현한다.

### 11.1 kubectl-ai InitializeMCPClient() 실제 흐름

```go
func (a *Agent) InitializeMCPClient(ctx context.Context) error:
  1. mcp.InitializeManager()
       → ~/.config/kubectl-ai/mcp.yaml 또는 환경변수 지정 경로 읽기
       → 각 서버 연결 (stdio 기반 프로세스 또는 HTTP/SSE)

  2. manager.RegisterWithToolSystem(ctx, callback)
       → 각 서버에서 tool 목록 조회 (ListTools)
       → 각 tool에 대해 callback 호출:
           a. tools.ConvertToolToGollm(&toolInfo) → gollm.FunctionDefinition 변환
           b. tools.NewMCPTool(serverName, toolName, desc, schema, manager) 생성
           c. schema.Name = mcpTool.UniqueToolName()  // "serverName_toolName" 형식
           d. schema.Description += " (from serverName)"  // 출처 명시
           e. tools.RegisterTool(mcpTool)  // 주의: global registry에 등록

  3. a.mcpManager = manager  // 이후 UpdateMCPStatus()에서 사용

  4. UpdateMCPStatus(ctx) → a.Session.MCPStatus 갱신
       → ConnectedCount, TotalTools, ServerInfoList 포함
       → GetMCPStatusText()로 사람이 읽을 수 있는 상태 텍스트 생성 가능
```

**주의: global vs session-local registry**

`tools.RegisterTool(mcpTool)`은 `pkg/tools/tools.go`의 global `allTools` 변수에 등록한다. 이는 프로세스 전체에서 공유된다. 세션별로 다른 MCP tool 세트를 쓰려면 session-local `Tools` 인스턴스를 별도로 관리해야 한다.

**`MCPTool.UniqueToolName()`:**
```
"서버이름_툴이름" 형식으로 built-in tool과 이름 충돌 방지
예: "my-mcp-server_list_pods" → kubectl과 구분
```

### 11.2 자체 루프에서의 MCP 분리 구현

```go
// internal/toolconnector/mcp.go

func RegisterMCPTools(ctx context.Context, registry *tools.Tools) (*mcp.Manager, error) {
    // 1. manager 초기화 (설정 파일 읽기 및 서버 연결)
    manager, err := mcp.InitializeManager()
    if err != nil {
        return nil, err
    }

    // 2. tool 발견 및 등록 (global이 아닌 session registry에 등록)
    err = manager.RegisterWithToolSystem(ctx, func(serverName string, toolInfo mcp.Tool) error {
        schema, err := tools.ConvertToolToGollm(&toolInfo)
        if err != nil {
            return err
        }
        mcpTool := tools.NewMCPTool(serverName, toolInfo.Name, toolInfo.Description, schema, manager)
        schema.Name = mcpTool.UniqueToolName()
        schema.Description = fmt.Sprintf("%s (from %s)", toolInfo.Description, serverName)
        registry.RegisterTool(mcpTool)  // session-local registry에 등록
        return nil
    })

    return manager, err
}
```

Init()에서 `tools.RegisterTool()` (global) 대신 `registry.RegisterTool()` (session-local)을 쓰는 것이 핵심이다. 이를 통해 MCP tool이 세션별로 격리된다.

---

## 12. 승인 UX 단일화

현재 문제: kubectl-ai가 생성하는 숫자 선택지(1/2/3)와 k8s-assistant가 처리하는 y/n 입력이 섞인다.

자체 루프에서는 승인 요청이 항상 `ReactLoop`에서 발생하므로 UX를 k8s-assistant 스타일로 통일할 수 있다.

```go
// internal/react/approval.go

type ApprovalHandler struct {
    out    OutputSink
    input  chan ApprovalResult
}

type ApprovalResult struct {
    Approved bool
    Remember bool  // "다시 묻지 않기" 선택 시 true
}

func (a *ApprovalHandler) RequestApproval(ctx context.Context, calls []PendingCall) error {
    // 변경될 명령 목록 표시
    var descriptions []string
    for _, c := range calls {
        descriptions = append(descriptions, c.ParsedToolCall.Description())
    }
    a.out.Send(OutputApprovalRequest{Commands: descriptions})
    return nil
}

// Orchestrator에서 y/n/yes/no 입력을 받아 여기로 전달
func (a *ApprovalHandler) Respond(approved bool, remember bool) {
    a.input <- ApprovalResult{Approved: approved, Remember: remember}
}
```

---

## 13. 단계별 구현 계획

### 1단계: toolconnector 분리 (현재 구조 유지)

`internal/toolconnector/registry.go`를 만들고 kubectl-ai tools 등록 로직을 이동한다. 아직 Agent 루프는 바꾸지 않는다. `AgentWrapper.Start()` 내부에서 이 registry를 쓰도록 리팩터링.

**목표:** 다음 단계에서 재사용할 tool 등록 레이어 확보.

### 2단계: ReactLoop MVP

`internal/react/loop.go`에 최소 루프를 구현한다. 지원 범위:

- LLM streaming 응답 처리
- function call 파싱 (shim 모드 제외)
- kubectl / bash tool 실행
- `CheckModifiesResource == "yes"` 명령에 대해 y/n 승인
- `FunctionCallResult` 재주입
- max iteration 종료

이 단계에서는 session 저장, MCP, troubleshooting 연동 제외.

### 3단계: Orchestrator 연결 교체

`internal/agent/setup.go`의 `AgentWrapper` 대신 `ReactLoop`을 Orchestrator에 직접 연결한다. 출력 포맷은 기존 `Formatter`를 그대로 사용.

### 4단계: troubleshooting 개입 시점 추가

`ReactLoop.runIteration()` 안에 `troubleshooting.AfterLLMText()` / `troubleshooting.InjectObservation()` 호출 지점을 추가한다. 현재 `TroubleshootingFlow`의 상태 머신은 그대로 재사용.

### 5단계: MCP 연동 복원

`internal/toolconnector/mcp.go`를 구현하고 Init 시 MCP tool을 registry에 등록한다.

### 6단계: ToolUseShim 지원 (선택)

`internal/react/shim.go`에 `candidateToShimCandidate` 상당 로직을 이식한다. Ollama 등 function calling 미지원 모델 사용 시 활성화.

---

## 14. 리스크 및 대응

### 구현량

kubectl-ai `Agent.Run()`이 약 400줄. 상태 머신, 에러 처리, RunOnce 모드, session picker 등 세부 케이스가 많다. MVP는 RunOnce 모드와 session picker를 제외하고 시작한다.

### kubectl-ai 버전 변경

Tool 계층(`pkg/tools/interfaces.go`)은 인터페이스가 안정적이다. `gollm` 인터페이스도 변동이 적다. 단, `api.MessageType` 상수나 `AgentState` 정의는 의존하지 않고 k8s-assistant 자체 타입으로 정의한다.

### MCP 초기화 순서

MCP server 연결은 Init 시 동기 실행이다. 느린 MCP server가 있으면 시작 지연이 생긴다. 향후 lazy 초기화 또는 timeout 처리를 고려한다.

### 승인 UX

기존 `Orchestrator.handleAgentChoiceRequest()`의 숫자 → choice 변환 로직을 `ApprovalHandler`로 이전한다. 기존 y/n 입력 처리는 유지하고 choice 번호 입력은 제거한다.

---

## 15. 관련 코드 위치

```
# k8s-assistant
internal/agent/setup.go
internal/orchestrator/orchestrator.go
internal/orchestrator/troubleshooting_flow.go
internal/orchestrator/formatter.go

# kubectl-ai v0.0.31 (모듈 캐시)
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/agent/conversation.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/agent/mcp_client.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/tools/interfaces.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/tools/tools.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/tools/kubectl_tool.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/tools/bash_tool.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/tools/mcp_tool.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/mcp/manager.go

# gollm
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai/gollm@v0.0.0-20260325022250-08cf256aa2f5/interfaces.go
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai/gollm@v0.0.0-20260325022250-08cf256aa2f5/shims.go

# api
$GOPATH/pkg/mod/github.com/!google!cloud!platform/kubectl-ai@v0.0.31/pkg/api/
```
