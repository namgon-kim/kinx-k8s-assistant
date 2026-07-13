# Bug Risk Review

이 문서는 제시된 리스크를 현재 코드 기준으로 재점검해 타당한 항목만 정리한다.
각 항목은 실제 사용 시나리오를 포함한다. read-only classifier 문제 중 `kubectl-readonly` 대체로
해소 가능한 항목은 표시만 하고 상세 수정안은 생략한다.

## 1. 잘못된 로직: 실행은 되지만 결과가 틀림

### BUG-1. another_guide rewind가 엉뚱한 phase에 착지할 수 있음

- Severity: LOW / latent
- Area: `internal/react/phase_plan.go` `preferredPreGuidanceIndex`
- Scenario: 사용자가 CRD 클러스터를 진단한다. resource guide가 주입되어 `guided_diagnosis`까지 진행했지만 결론이 나지 않아 inconclusive `final_report`가 나온다. 런타임이 "다른 guide 각도", "직접 다른 방향", "종료"를 제시하고 사용자가 다른 guide 각도를 선택한다.
- What happens: `continueWithGuideFocus`는 guide 상태를 리셋하고 guidance 이전 phase로 되감아야 한다. fallback은 `strings.Contains(name, "guidance")`와 `final_report`만 제외한다. `guided_diagnosis`에는 `guidance` 부분 문자열이 없어 제외되지 않는다. preferred pre-guidance phase가 없는 custom plan에서는 되감기가 `guided_diagnosis`에 착지할 수 있고, `guidance_lookup`은 더 낮은 index라 forward-only phase graph에서 다시 도달하지 못한다.
- Why wrong: guidance 계열 phase 판정이 부분 문자열에 의존하고 `guided_diagnosis`를 명시적으로 제외하지 않는다.
- Note: 표준 plan에는 `context_resolution` 같은 preferred phase가 있어 latent로 분류한다.

### BUG-2. delete mutation verification이 NotFound에서 deadlock될 수 있음

- Severity: HIGH
- Area: `internal/react/mutation_lifecycle.go`
- Scenario: 사용자가 "prod 네임스페이스 web 파드 삭제해줘"라고 요청한다. 모델이 `kubectl delete pod web -n prod`를 제안하고 사용자가 승인한다. 삭제는 성공한다.
- What happens: runtime은 direct-effect requirement를 만들고 `kubectl get pod web -n prod -o yaml` 같은 검증 command를 요구한다. 삭제가 성공했으므로 검증 command는 NotFound/error를 반환한다. requirement satisfaction은 `toolResultSucceeded(result)`가 true일 때만 기록되므로 NotFound는 만족으로 처리되지 않는다. 이름 `web`이 포함된 다른 command도 계속 NotFound가 되고, 이름 없는 command는 requirement matching에서 거부될 수 있다.
- Why wrong: 삭제의 성공 증거는 객체가 사라진 것인데, 현재 검증 성공 판정은 generic tool success/error만 본다.
- Impact: `AwaitingResult`로 진입하지 못하고 MaxIterations까지 소진될 수 있다.

### BUG-3. exec/run/cp/debug/attach/apply -k target extraction이 잘못된 verification requirement를 만들 수 있음

- Severity: MEDIUM
- Area: `internal/react/action_target_validation.go`, `internal/react/kubectl_resource.go`, `internal/react/mutation_lifecycle.go`
- Scenario A: 사용자가 "kustomize overlay 반영해줘"라고 요청하고 모델이 `kubectl apply -k ./overlays/prod`를 실행한다.
- Scenario B: 사용자가 "이 파드에서 명령 실행해줘"라고 요청하고 모델이 `kubectl exec mypod -- ...`를 실행한다.
- What happens: `isKubectlApplyFileCommand`는 `-f/--filename`만 파일 apply 예외로 본다. `-k`는 예외가 아니므로 `./overlays/prod`가 resource kind처럼 추출될 수 있다. `exec`, `run`, `cp`, `debug`, `attach`에서도 첫 위치 인자의 의미가 verb별로 다른데 `firstKubectlResourceArg`는 이를 구분하지 않는다.
- Why wrong: mutating kubectl verb의 인자 형태를 verb별로 해석하지 않고 공통 위치 인자 추출로 처리한다.
- Impact: 매칭 불가능하거나 의미 없는 verification requirement가 생겨 verification deadlock으로 이어질 수 있다.

### BUG-4. guided_diagnosis 완료 응답에 딸려온 action이 실행될 수 있음

- Severity: HIGH
- Area: `internal/react/loop.go`, `internal/react/phase_plan.go`
- Scenario: guide step을 모두 마친 뒤 runtime이 "이제 `guided_diagnosis` phase를 `phase_progress`로만 완료하라"고 요청한다. 모델이 같은 응답에 `phase_progress`와 추가 `kubectl` action을 함께 낸다.
- What happens: `consumePhaseProgress`가 `enforceRequestedStructuredDirective`보다 먼저 실행되어 phase를 완료하고 `guidedPhaseProgressRequested=false`를 clear한다. 그 뒤 directive enforcement는 더 이상 `ControlAwaitingGuidedPhaseProgress`를 보지 못하고, 남은 action이 dispatch pipeline으로 내려갈 수 있다.
- Why wrong: 이 경로만 requested structured output enforcement보다 consumer가 먼저 돈다. 문서 계약상 phase completion과 동반된 action은 실행되면 안 된다.

### BUG-5. shim mode에서 structured ack가 native FunctionCallResult로 주입됨

- Severity: HIGH
- Area: `internal/react/loop.go`, `internal/react/mutation_lifecycle.go`
- Scenario: shim mode provider에서 resource guide 진단 또는 mutation verification을 사용한다. 모델이 `guide_progress` 또는 `mutation_verification_result`를 JSON shim으로 반환한다.
- What happens: `consumeGuideProgress`와 `consumeMutationVerificationResult`가 shim 여부와 무관하게 `gollm.FunctionCallResult{ID: call.ID}`를 history에 append한다. shim synthetic call은 ID가 비어 있을 수 있어 다음 provider 요청에 선행 tool_use 없는 tool_result가 포함될 수 있다.
- Why wrong: `appendToolObservation`에는 shim일 때 문자열로 기록하는 분기가 있지만, 이 두 structured consumer에는 같은 분기가 없다.
- Impact: Anthropic-style shim 요청이 malformed될 수 있다.

### BUG-6. correction dedup이 복구 후에도 유지되어 조기 종료할 수 있음

- Severity: MEDIUM
- Area: `internal/react/context_state.go`
- Scenario: 긴 진단 중 모델이 한 번 잘못된 `phase_progress`를 내고 runtime correction 후 정상 복구한다. 여러 iteration 뒤 동일한 종류의 실수를 다시 한다.
- What happens: `contextBlockHashes`가 query 내내 유지되어 같은 `(code, message)` correction이 다시 발생하면 `appendContextBlock`이 false를 반환할 수 있다. 이 경우 즉시 "반복되어 진단 중단" 경로로 갈 수 있다.
- Why wrong: 모델이 correction을 무시하고 즉시 반복한 경우와, 정상 복구 후 독립적으로 재발한 경우를 구분하지 않는다.

### BUG-7. multi-sink phase plan을 부당하게 거부할 수 있음

- Severity: LOW / latent
- Area: `internal/react/phase_plan.go`
- Scenario: 사용자가 "점검하고 문제 있으면 조치안을 내고, 없으면 요약만 해줘"라고 요청한다. 모델이 `triage -> answer_path`와 `triage -> escalate_path`처럼 두 개의 leaf phase를 가진 정상 DAG plan을 낸다.
- What happens: `phaseStepHasLaterStep`는 단순히 더 큰 index가 존재하면 non-terminal로 본다. 따라서 index 2인 `answer_path` 뒤에 index 3인 `escalate_path`가 있으면 `answer_path`도 allowed_next가 필요한 non-terminal처럼 취급된다.
- Why wrong: terminal phase를 "최대 index"로만 판단한다. 실제 DAG에는 여러 sink가 있을 수 있다.

## 2. 빠진 로직: 있어야 할 판정이 없음

### BUG-8. phase_progress에 관찰 성공/evidence_useful 가드가 없음

- Severity: MEDIUM
- Area: `internal/react/phase_plan.go`
- Scenario: 관찰 command가 RBAC blocked, NotFound, tool error로 돌아왔는데 모델이 `phase_progress{evidence_useful:false}` 또는 유사한 완료 보고를 낸다.
- Missing logic: `phaseProgressFromFunctionCall`은 `evidence_useful`을 파싱하지만 `acceptProgress`는 이 값을 사용하지 않는다. 최신 observation 상태도 확인하지 않는다. 반면 guide progress의 action-embedded 경로는 `guideProgressObservationUseful(result)`로 성공 관찰만 step completion에 반영한다.
- Impact: 실제 evidence 없이 top-level phase가 완료될 수 있다.

### BUG-9. progressing/unresolved mutation result가 recheck를 재무장하지 않음

- Severity: MEDIUM
- Area: `internal/react/mutation_lifecycle.go`
- Scenario: 사용자가 deployment scale/remediation을 승인한다. 변경 후 검증했더니 rollout이 아직 진행 중이라 모델이 `mutation_verification_result.status=progressing`을 반환한다.
- Missing logic: `progressing`/`unresolved` 이후 `mutationContinuationRequired`로 action 한 번은 강제하지만, 다음 successful observation 하나면 flag가 clear된다. 같은 mutation에 대한 `pendingMutationVerification`과 `AwaitingResult`가 다시 만들어지지 않아 두 번째 verification result 요구로 이어지기 어렵다.
- Impact: `maxMutationContinuationAttempts=3` recheck budget과 "resolved 될 때까지 recheck" 계약이 실질적으로 강제되지 않는다.

### BUG-10. verification phase의 순서/역할 제약 검증이 없음

- Severity: MEDIUM
- Area: `internal/react/phase_plan.go`
- Scenario: 모델이 `mutation_execution -> final_report -> verification_observation` 순서의 phase plan을 낸다. 또는 `verification_planning`처럼 실제 관찰이 아닌 phase 이름만 넣는다.
- Missing logic: `phasePlanHasVerificationPhase`는 verification phase가 어딘가 존재하는지만 확인하고, mutation execution 이후이면서 response/final 이전인지 검증하지 않는다. 이름도 `verify`/`verification` substring으로 통과한다.
- Impact: mutation request에 verification phase가 필요하다는 gate가 실제 실행 순서를 보장하지 못한다.

### BUG-11. ReAct-owned prompt에서 빈 입력 가드가 없음

- Severity: MEDIUM
- Area: `internal/orchestrator/orchestrator.go`, `internal/react/runtime_state.go`
- Scenario: agent가 "직접 다른 방향 입력"을 기다리는 중 사용자가 실수로 Enter만 누른다.
- Missing logic: 일반 input path에는 빈 입력을 무시하는 guard가 있지만, `handleAgentInputRequest`에는 없다. `DecideInputDispatch`도 `ControlAwaitingContinuationText`와 `ControlAwaitingUserQuery`에서 `InputEmpty`를 accepted로 처리한다.
- Impact: 빈 문자열이 loop로 전달되어 continuation text 취소/종료처럼 동작할 수 있다. 문서의 "빈 응답을 loop에 보내지 않고 prompt 유지" 계약과 불일치한다.

### BUG-12. standalone __guide_progress__가 최신 관찰 성공 여부를 검증하지 않음

- Severity: MEDIUM
- Area: `internal/react/loop.go`
- Scenario: guide step 관찰 command가 blocked/error였는데 모델이 별도 `__guide_progress__` call로 step 완료를 보고한다.
- Missing logic: `consumeGuideProgress`는 `evidence_useful=false`와 invalid step만 거부한다. 직전 observation이 성공했는지는 확인하지 않는다.
- Impact: failed/blocked evidence 뒤에도 guide step이 완료되고, 모든 step 완료 시 post-guide directive로 넘어갈 수 있다.

## 3. Dead / unreachable / latent branches

### BUG-13. different_approach inline continuation branch is unreachable after final_report

- Severity: MEDIUM
- Area: `internal/react/next_directions.go`
- Scenario: inconclusive report 후 사용자가 "guide 말고 다른 방식으로 진단해줘"라는 `different_approach` option을 선택한다.
- What happens: `continuingAfterFinalReport := l.pendingFinalReport != nil`가 option 처리 전에 계산된다. final report 이후 continuation에서는 이 값이 true라서 `different_approach`도 `continueAfterFinalReport -> continueWithGuideFocus` 경로를 탄다. `applyDirectionOption` 안의 inline directive injection branch는 final report 이후에는 도달하지 않는다.
- Why wrong: 문서상 `different_approach`는 guide rewind 없이 사용자 지시를 주입하고 재개해야 한다. 현재는 `another_guide`와 유사하게 guide lookup을 다시 열 수 있다.

### BUG-14. mutation recheck budget exhausted branch is effectively dead

- Severity: MEDIUM
- Area: `internal/react/mutation_lifecycle.go`
- Scenario: mutation verification이 계속 `progressing`이라 세 번 recheck 후 inconclusive report를 강제해야 한다.
- What happens: BUG-9 때문에 단일 mutation에서는 `mutationContinuationAttempts`가 1을 넘기 어렵다. 같은 mutation에 대해 두 번째 `mutation_verification_result`가 요구되지 않으면 budget exhausted branch도 실행되지 않는다.
- Why wrong: recheck budget을 둔 목적은 외부 상태가 안정되지 않을 때 deterministic하게 inconclusive report로 닫기 위함인데, 현재 흐름에서는 그 분기가 실질적으로 죽어 있다.

### BUG-15. choiceInputYesNo path is production-dead and bypass-prone if reintroduced

- Severity: LOW / latent
- Area: `internal/orchestrator/orchestrator.go`
- Scenario: 현재 approval과 continuation choice는 모두 numbered `UserChoiceRequest`로 렌더링된다. agent prompt 중 `(y/n)` 문자열을 쓰는 production `UserChoiceRequest`는 보이지 않는다.
- What happens: `choiceInputYesNo` 판정과 y/n remapping은 테스트 외 production에서 실행될 가능성이 낮다. 재도입되면 `choiceInputAccepted`가 `decision.Accepted`를 보지 않고 `inputKind == InputApproval`만으로 통과시켜 `DecideInputDispatch` gate를 우회할 수 있다.
- Why wrong: dead path가 다시 활성화될 때 input owner/control-state gate와 어긋날 수 있다.

### BUG-16. guideProgressAllowedForCurrentPhase nil-phase branch is dead but unsafe

- Severity: LOW / latent
- Area: `internal/react/loop.go`
- Scenario: resource guide가 정상 주입되면 `guideStepState`는 `guided_diagnosis` phase 진입과 함께 설정된다.
- What happens: 이 invariant가 유지되는 동안 `guideProgressAllowedForCurrentPhase`의 `phaseStepState == nil -> true` branch는 실행될 일이 없다. 하지만 향후 리팩터링으로 `guideStepState`만 남고 `phaseStepState`가 nil이 되면 guide progress를 허용한다.
- Why wrong: nil phase state는 "허용"보다 "불변식 위반"에 가깝다. dead branch가 향후 오작동의 fail-open 지점이 될 수 있다.

## 4. read-only classifier items covered by kubectl-readonly replacement

### BUG-17. direct kubectl redirection classifier gap

- Severity: HIGH if current classifier remains
- Scenario: read-only 모드에서 `kubectl get secret app -o yaml > /tmp/leak.yaml`를 실행하려 한다.
- Current evidence: `isReadOnlyKubectlPipeline`은 redirection을 거부하지만 `hasBlockedReadOnlyFastPathFeature`가 redirection을 unknown으로 올리지 않는다. 기존 test는 `bash -c` redirection이 `no`가 아님을 확인하지만 직접 kubectl redirection은 별도 커버가 필요하다.
- Handling: `kubectl-readonly` 대체 대상. 대체 시 해소 가능한 항목으로 표시만 한다.

### BUG-18. bash -c newline/background/logical separator mutation hiding

- Severity: HIGH if current classifier remains
- Scenario: read-only 모드에서 `bash -c "kubectl get pods\nkubectl delete pod app -n tests"` 또는 `bash -c "kubectl get pods & kubectl delete pod app -n tests"`를 실행하려 한다.
- Current evidence: `splitShellCommandList`는 `;`와 `&&`만 분리한다. newline, single `&`, `||`가 같은 segment로 남으면 segment 안 첫 kubectl 중심 판정이 뒤쪽 mutation을 놓칠 수 있다.
- Handling: `kubectl-readonly` 대체 대상. 대체 시 해소 가능한 항목으로 표시만 한다.

### BUG-19. kubectl config write subcommands are currently policy-allowed

- Severity: LOW / policy review
- Scenario: read-only 모드에서 `kubectl config set-context`, `use-context`, `delete-cluster`, `unset` 등 local kubeconfig 상태를 바꾸는 command를 실행한다.
- Current evidence: `isKubectlReadOnlyVerb`는 `config`를 read-only verb로 포함하고, subcommand restriction은 `auth`에만 적용된다.
- Handling: 문서와 현재 코드 계약은 일치하지만, `kubectl-readonly` 대체 또는 별도 policy로 재검토할 항목이다.

### BUG-20. attached short flags can degrade mutation classification

- Severity: LOW
- Scenario: `kubectl -nfoo delete pod app`처럼 value-taking short flag가 붙은 형태를 사용한다.
- Current evidence: short global flag handling은 `len(field) == 2`인 경우만 value-taking flag로 처리한다. 실행 차단 자체는 유지될 가능성이 높지만 known mutation이 unknown/retry로 강등될 수 있다.
- Handling: `kubectl-readonly` 대체 대상. 대체 시 해소 가능한 classifier 세부 항목으로 표시만 한다.

## 5. 기타 latent parser/phase issues

### BUG-21. phase_plan can start from arbitrary declared index

- Severity: LOW / latent
- Area: `internal/react/phase_plan.go`
- Scenario: 모델이 `current_phase_index`를 mutation execution phase로 지정하고 앞선 observation/planning phase를 선언만 한 plan을 낸다.
- Evidence: `phasePlanValid`는 current index가 선언되어 있는지만 확인한다.
- Impact: 앞선 필수 phase completion 근거 없이 execution phase에서 시작할 수 있다.

### BUG-22. shim JSON extraction mishandles multiple json code blocks

- Severity: LOW
- Area: `internal/react/shim.go`
- Scenario: shim mode 모델이 설명용 JSON block과 실제 ReAct JSON block을 함께 출력한다.
- Evidence: `extractJSON`은 첫 ` ```json` marker부터 마지막 fence까지 자른다.
- Impact: 복구 가능한 응답이 parse failure/query abort로 이어질 수 있다.

## 6. follow-up / request context issues

### BUG-23. keyword-based previous request retry can false-positive on common Korean wording

- Severity: MEDIUM
- Area: `internal/react/request_context.go`
- Scenario: 직전 요청이 pod 진단이었다. 다음에 사용자가 새 설명 요청으로 "설명이 아닌 예시를 보여줘" 또는 "정확한 용어 예시를 알려줘"라고 입력한다.
- Evidence: `shouldRetryPreviousRequest`는 `다시`, `정확`, `아닌` 같은 흔한 단어가 `originalQuery`에 있으면 retry 후보로 본다. 새 requirement analysis가 `conversation`/`unknown`이거나 clarify action이면 `applyPriorContextToFollowUpRequirementAnalysis`가 새 분석을 통째로 `lastRequirementAnalysis` clone으로 교체한다.
- Why wrong: "이전 답이 틀렸으니 같은 작업을 다시 하라"는 의도와, 새 질문 안의 일반 부정/정확성 표현을 구분하지 않는다.
- Impact: 새 conversation/follow-up 질문이 직전 Kubernetes 진단 재실행으로 바뀔 수 있다.

### BUG-24. follow-up all-namespaces intent can be overwritten by prior namespace

- Severity: MEDIUM
- Area: `internal/react/request_context.go`
- Scenario: 직전 요청이 `tenant-a` namespace의 cluster 진단이었다. 다음에 사용자가 "이번엔 모든 namespace에서 관련 pod를 확인해줘"라고 한다. 모델이 `scope.namespace="all_namespaces"`로 표현하지만 `scope.type="all_namespaces"`는 빠뜨린다.
- Evidence: `requirementAnalysisFromFunctionCall`은 `scope.namespace`가 all-namespaces 값이면 `analysis.Scope.Namespace`를 비워 둔다. 이후 prior context merge에서 `analysis.Scope.Namespace`가 비어 있으면 이전 namespace를 다시 채운다. `requestContextFromRequirementAnalysis`의 all-namespaces 보정은 `scope.type=="all_namespaces"`일 때만 확실히 동작한다.
- Why wrong: explicit all-namespaces intent가 namespace 값 표현으로 들어온 경우, follow-up defaulting이 이를 "namespace 없음"으로 오해한다.
- Impact: 사용자는 전체 namespace 조회를 의도했지만 runtime context가 이전 단일 namespace로 좁아질 수 있다.
