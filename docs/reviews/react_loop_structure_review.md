# ReAct Loop Structure Review

> 상태: 구조 리뷰.
>
> 이 문서는 개별 버그 목록이 아니라 `internal/react` ReAct loop의 구조적 리스크를
> 정리한다. 구체적인 재현 증상은 최상위 [`bug.md`](../../bug.md)와 함께 본다.

## Summary

현재 ReAct loop는 동작하는 기능을 많이 갖추고 있지만, 핵심 제어가 하나의 명시적 상태 머신으로 닫혀 있지 않다. 실제 obligation은 여러 플래그와 pending struct에 흩어져 있고, 매 iteration마다 `RuntimeSnapshot.Control`로 재투영된다. 이 구조 때문에 gate 순서, 플래그 clear 타이밍, trailing function call 처리 방식이 런타임 안전성을 결정한다.

확인된 구조적 근본 원인은 다음 다섯 가지다.

1. 명시적 상태 머신 부재: 분산 플래그를 우선순위로 접어 control state를 만든다.
2. consume/enforce 혼합 파이프라인: "이번 턴 허용 출력"을 선행 확정하지 않는다.
3. forward-only phase DAG와 rewind/re-entry 요구 충돌: guide 재시도 흐름이 phase 불변식과 충돌한다.
4. verification이 반복 loop가 아니라 일회성 플래그와 command text 추론에 의존한다.
5. liveness/no-progress 감지가 MaxIterations와 correction dedup에 의존한다.

## A. State Management

### Verdict

타당하다. 현재 제어 상태의 source of truth는 단일 enum이 아니라 `Loop` 내부의 분산 상태다. `RuntimeSnapshot.Control`은 이 상태를 읽어 우선순위 기반으로 접는 derived view다.

관련 코드:

- `internal/react/runtime_state.go`: `RuntimeSnapshot.deriveControl`
- `internal/react/loop.go`: `finalReportRequested`, `guidedPhaseProgressRequested`, `mutationContinuationRequired`, `pendingFinalReport`, `pendingMutationVerification` 등 flag/state mutation

### Structural Issue

`deriveControl`은 다음처럼 상위 상태를 먼저 반환한다.

- `StateExited`
- tool dispatch
- waiting approval/choice/text
- `StateIdle` 또는 `StateDone`
- mutation verification/result/continuation
- guided phase progress/final report/next directions
- requirement/phase/resource guide/model step

이 방식은 특정 flag 조합을 불법으로 만들지 않는다. 단지 표시 시점에 하나의 control state로 접는다. 따라서 남아 있으면 안 되는 flag가 남아도 상위 case에 가려질 수 있고, 어떤 gate가 먼저 flag를 clear하느냐에 따라 뒤쪽 gate가 무력화될 수 있다.

### User Scenario

사용자가 CRD 리소스 문제를 진단한다. guide step을 모두 완료했고 runtime은 `guided_diagnosis` phase completion만 요구한다. 그런데 모델이 같은 응답에 `phase_progress`와 추가 `kubectl` action을 함께 낸다. `consumePhaseProgress`가 먼저 flag를 clear하면 이후 `deriveControl`은 더 이상 guided phase-progress obligation을 보지 못하고 trailing action이 실행될 수 있다.

### Consequence

상태 안정성이 전이표가 아니라 flag 조합과 gate 순서에 의존한다. `bug.md`의 guided phase trailing action, mutation continuation, correction dedup 문제는 이 구조의 증상이다.

### Direction

- source-of-truth control state를 명시적 enum/state object로 둔다.
- 각 transition이 어떤 flag를 set/clear하는지 전이 함수로 제한한다.
- `RuntimeSnapshot`은 source-of-truth를 표시하는 read model로만 사용하고, hidden flag 조합을 control 결정에 쓰지 않는다.
- 상태 invariant 검사 또는 audit hook으로 불법 조합을 즉시 노출한다.

## B. Gate Pipeline

### Verdict

타당하다. `runIteration`은 선형 gate pipeline이고, 구조화 출력 소비와 lifecycle enforcement가 같은 pass 안에 섞여 있다.

관련 코드:

- `internal/react/loop.go`: `runIteration` gate order
- `consumeMutationVerificationResult`
- `consumeGuideProgress`
- `consumePhaseProgress`
- `enforceRequestedStructuredDirective`
- `consumeFinalReport`
- `consumeNextDirections`

### Structural Issue

현재 pipeline은 대략 다음 순서로 진행된다.

```text
consume mutation_verification_result
consume guide_progress
consume phase_progress
resource_guide_lookup gate
conversation gate
enforce requested structured directive
consume final_report
consume next_directions
target/read-only/tool dispatch
```

즉 `phase_progress`는 requested-output enforcement보다 먼저 소비된다. 반면 `final_report`와 `next_directions`는 enforcement 뒤에서 소비된다. 같은 "runtime이 특정 structured output만 요구한다"는 규칙이 출력 종류마다 다른 순서로 적용된다.

### User Scenario

guide 완료 후 runtime이 `phase_progress`만 요구했는데 모델이 `phase_progress + kubectl get ...`을 함께 낸다. phase consumer가 먼저 완료 처리하면서 flag를 지우면, 뒤의 enforcer는 위반을 보지 못한다. 반대로 `final_report + action` 또는 `next_directions + action`은 enforcement가 먼저 걸려 보호된다.

### Consequence

출력 허용 규칙이 pipeline 위치에 종속된다. trailing call을 버릴지, 계속 넘길지, correction할지가 consumer별 임기응변으로 갈라진다.

### Direction

- pipeline 진입 직후 현재 control state로부터 `allowed_outputs`, `forbidden_outputs`, `exclusive_output_required`를 확정한다.
- 이후 모든 consumer는 이 precomputed output lock을 위반할 수 없게 한다.
- structured output과 real tool call이 한 응답에 섞이면, 먼저 output lock으로 허용 조합인지 검사한 뒤 소비한다.
- "consume"은 상태 전진만 담당하고, "enforce"는 consume 전에 한 번만 적용한다.

## C. Phase and Guidance Flow

### Verdict

부분적으로 타당하다. forward-only phase plan과 `another_guide` rewind/re-entry 요구는 실제로 긴장 관계가 있다. 다만 resource guidance와 incident guidance의 분리는 의도된 safety boundary이므로, 버그라기보다 일관성/복잡도 리스크로 보는 것이 맞다.

관련 코드:

- `internal/react/phase_plan.go`: forward-only validation, `preferredPreGuidanceIndex`
- `internal/react/next_directions.go`: `continueWithGuideFocus`
- `internal/react/resource_guidance.go`: resource guide injection
- `internal/orchestrator/incident_guidance_flow.go`: incident guidance side-flow

### Structural Issue

phase plan은 `allowed_next`가 forward-only edge여야 한다. 그런데 `another_guide`는 이미 guided diagnosis까지 간 뒤, guide 전 단계로 되감아 다시 `guidance_lookup`에 도달해야 한다. 이 동작은 phase graph 자체의 edge가 아니라 runtime의 imperative rewind로 구현되어 있다.

또한 resource guide는 `react.Loop` 내부 phase로 관리되고, incident guidance는 orchestrator side-flow로 관리된다. 둘은 모두 "guide"라는 사용자 관점의 기능이지만 trigger, lifecycle, state owner가 다르다.

### User Scenario

사용자가 Cluster API CRD 문제를 진단한다. 첫 guide로 결론이 나지 않아 `another_guide`를 선택한다. runtime은 phase를 guide 이전으로 돌려야 하지만, phase graph는 forward-only다. preferred phase가 없으면 fallback이 잘못된 phase에 착지할 수 있고, 설령 착지하더라도 이 흐름은 phase plan이 선언한 edge가 아니라 runtime override에 의존한다.

### Consequence

재시도/re-entry가 phase plan의 명시적 모델이 아니라 예외 경로다. phase invariant와 retry UX가 분리되어 있어 guide 재검색, 다른 접근, incident runbook 선택의 규칙이 서로 다르게 보인다.

### Direction

- guide retry를 phase graph 밖의 "continuation session"으로 모델링하거나, phase plan에 retry/re-entry edge를 명시적으로 허용한다.
- forward-only invariant를 유지한다면 `another_guide`는 기존 phase를 rewind하지 않고 새 child query/session으로 시작한다.
- resource guidance와 incident guidance는 실행 owner는 분리하되, 사용자-visible guidance policy는 하나의 문서/contract로 통합한다.

## D. Verification Lifecycle

### Verdict

타당하다. 현재 mutation verification은 goal-level lifecycle을 표방하지만, 구현은 command-derived requirement와 일회성 continuation flag에 가깝다.

관련 코드:

- `internal/react/mutation_lifecycle.go`
- `trackMutationVerification`
- `consumeMutationVerificationResult`
- `requestMutationContinuationOrBudgetReport`
- `mutationVerificationFromCall`

### Structural Issue

현재 flow는 다음에 가깝다.

```text
mutation success -> derive requirements from command text
read-only evidence satisfies requirements
mutation_verification_result requested once
progressing/unresolved -> mutationContinuationRequired=true
next successful observation -> mutationContinuationRequired=false
```

`progressing` 또는 `unresolved` 이후 같은 mutation에 대해 다시 evidence requirement를 만들고 다시 `mutation_verification_result`를 요구하는 loop가 1급 상태로 존재하지 않는다. recheck attempt counter는 있지만, 같은 mutation이 계속 progressing인 상황에서 반복 판정으로 자연스럽게 재무장되지 않는다.

검증 대상 산출도 실제 cluster diff가 아니라 command text parsing에 크게 의존한다. `delete`는 NotFound가 성공 증거일 수 있고, `apply -k`, `exec`, `cp`, `debug` 같은 command는 위치 인자 의미가 verb별로 다르다.

### User Scenario

사용자가 Deployment scale 변경을 승인한다. 첫 검증에서 rollout이 아직 progressing이다. 모델이 `mutation_verification_result.status=progressing`을 낸다. runtime은 다음 read-only observation 하나를 강제하지만, 성공 observation이 들어오면 continuation flag가 풀린다. 같은 변경에 대해 두 번째 판정 요구가 자동으로 생기지 않아, rollout이 실제 resolved인지 다시 판정하기 전에 final report로 갈 수 있다.

### Consequence

문서의 "goal-level verification"과 실제 "one-shot result + continuation flag" 사이에 차이가 있다. delete deadlock, recheck budget dead branch, target extraction 기반 verification 오류가 여기서 발생한다.

### Direction

- mutation verification을 explicit state machine으로 둔다.
- states 예시:
  - `verifying_initial`
  - `awaiting_evidence`
  - `awaiting_judgement`
  - `progressing_wait`
  - `rechecking`
  - `resolved`
  - `unresolved_budget_exhausted`
- `progressing`은 새 evidence requirement cycle을 재무장해야 한다.
- command text parsing은 fallback으로 낮추고, 가능한 경우 server-side dry-run, apply result, object UID/resourceVersion, owner/selector evidence 등 typed target metadata를 사용한다.
- delete는 NotFound/absence를 성공 evidence로 표현할 수 있어야 한다.

## E. Input and Liveness

### Verdict

타당하다. 현재 liveness는 MaxIterations, correction dedup, 일부 branch policy에 의존하며, no-progress/deadlock 자체를 1급으로 감지하지 않는다. 입력 처리도 control-state dispatch와 UI mode gate가 동시에 존재한다.

관련 코드:

- `internal/react/context_state.go`: correction dedup
- `internal/react/loop.go`: MaxIterations, gate/correction application
- `internal/orchestrator/orchestrator.go`: choice/input dispatch, y/n mode
- `internal/react/runtime_state.go`: `DecideInputDispatch`

### Structural Issue

No-progress 상황은 다음처럼 종료된다.

- 동일 correction hash 반복 -> 반복 오류로 중단
- MaxIterations 도달 -> 최대 반복 도달로 종료

하지만 "이 requirement는 충족 불가능하다", "동일 evidence를 반복 수집한다", "같은 phase에서 관찰이 새로워지지 않는다" 같은 progress semantics는 별도 상태로 추적하지 않는다.

입력 처리도 두 층이다.

- `DecideInputDispatch`: control state와 input kind 기준
- `choiceInputMode`/`choiceInputAccepted`: prompt 표시 방식 기준

현재 number choice가 주류라 큰 문제는 제한적이지만, `(y/n)` 모드가 다시 production에 들어오면 `decision.Accepted`를 우회할 수 있다. 또한 일반 입력 path와 active agent input path의 empty input 계약이 다르다.

### User Scenario

delete 검증이 NotFound 때문에 충족되지 않는다. runtime은 이 requirement가 삭제 성공의 evidence라는 것을 모른다. 모델은 계속 get/describe를 시도하고, loop는 의미 있는 진전이 없다는 것을 인식하지 못한다. 결국 "최대 반복 도달"로 끝난다.

### Consequence

실패 원인이 사용자에게 명확히 노출되지 않는다. loop가 "진단 결과"가 아니라 "예산 소진"으로 닫힌다. correction dedup도 정상 복구 후 독립 재발을 반복 오류로 오판할 수 있다.

### Direction

- progress ledger를 둔다.
  - phase/step id
  - evidence hash
  - requirement id
  - branch/retry count
  - last useful observation time/order
- no-progress를 MaxIterations보다 먼저 감지해 user-facing blocker 또는 bounded fallback으로 전환한다.
- correction dedup은 "연속 반복"과 "복구 후 재발"을 구분한다.
- 입력 dispatch는 control-state decision을 단일 source로 삼고, UI mode는 표시/normalization만 담당하게 한다.
- empty input 정책을 state별로 명시하고 orchestrator path 전체에서 동일하게 적용한다.

## F. Protocol Channel Mixing

### Verdict

타당하다. runtime-internal structured calls와 real tool calls가 같은 `[]gollm.FunctionCall` 리스트를 공유한다. normalize 후 순차 consumer가 내부 호출을 제거하고 남은 call을 tool dispatch로 넘긴다.

관련 코드:

- `internal/react/loop.go`: `normalizeAssistantStructuredFunctionCalls`, `consume*`, trailing call handling
- `internal/react/shim.go`: JSON shim -> synthetic function call

### Structural Issue

한 model response 안에 다음이 함께 들어올 수 있다.

- `__phase_plan__`
- `__phase_progress__`
- `__guide_progress__`
- `__final_report__`
- `__next_directions__`
- real `kubectl`/`bash` call

이때 의미는 consumer 순서로 결정된다. 어떤 structured output은 단독이어야 하고, 어떤 것은 trailing call을 허용하거나 남긴다. "internal protocol event"와 "external action"이 같은 채널이라 exclusive-output rule이 구조적으로 보장되지 않는다.

### User Scenario

모델이 "phase를 완료했다"는 structured call과 "추가로 kubectl get ..." action을 한 번에 낸다. runtime은 먼저 어떤 structured consumer가 이 call을 먹는지에 따라 action을 버리거나, correction하거나, 그대로 실행할 수 있다.

### Consequence

동반 call 누수, shim/native ack 차이, trailing call 처리 불일치가 발생한다. 이 문제는 개별 consumer 수정으로 완화할 수 있지만, 같은 채널을 공유하는 한 새 structured output이 추가될 때 반복될 가능성이 높다.

### Direction

- model response를 internal control event와 external action proposal로 분리한다.
- 한 turn에서 허용되는 channel 조합을 schema로 먼저 검증한다.
- runtime-internal call은 provider tool result channel에 다시 넣지 않고, 별도 internal event log로 기록한다.
- shim mode도 native function-call emulation이 아니라 internal event parser로 다룬다.

## Recommended Refactor Order

1. **Output lock first**: `runIteration` 초반에 current control로 allowed/exclusive outputs를 확정한다. guided phase trailing action 같은 순서 의존 버그를 먼저 줄인다.
2. **Internal event vs tool action split**: structured call consumer와 tool dispatch path를 분리한다. shim/native ack 차이를 함께 정리한다.
3. **Verification state machine**: mutation verification을 repeated evidence/judgement loop로 승격한다. delete absence, progressing recheck, budget exhaustion을 여기서 해결한다.
4. **Continuation model cleanup**: `another_guide`, `different_approach`, incident runbook 선택을 continuation policy로 통합한다. rewind 대신 child query/session 모델을 검토한다.
5. **State invariant audit**: RuntimeSnapshot은 read model로 유지하되, source-of-truth state transition과 invariant checker를 추가한다.
6. **Liveness ledger**: evidence hash/requirement id/phase id 기준으로 no-progress를 감지하고 MaxIterations 전에 의미 있는 blocker로 전환한다.

## Relation to bug.md

이 구조 리뷰는 다음 bug.md 항목의 공통 원인을 설명한다.

- guided phase completion trailing action
- shim structured acknowledgement
- delete mutation verification deadlock
- progressing/unresolved recheck budget dead branch
- another_guide rewind issue
- different_approach unreachable branch
- correction dedup overreach
- empty input contract mismatch
- standalone guide_progress observation check gap
