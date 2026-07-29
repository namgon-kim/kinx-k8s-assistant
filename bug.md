# Bug Risk Review

이 문서는 제시된 리스크를 현재 코드 기준으로 재점검해 타당한 항목만 정리한다.
각 항목은 실제 사용 시나리오를 포함한다. read-only classifier 문제 중 `kubectl-readonly` 대체로
해소 가능한 항목은 표시만 하고 상세 수정안은 생략한다.

> 2026-07 package split 이후 참고: `Area`는 현재 package 위치로 갱신했다. 본문의 일부
> 설명과 field/function 이름은 이슈를 확인한 리팩터링 전 표현을 유지한다. 파일 이동만으로
> 해결됐다고 판정하지 않으며, 각 이슈는 새 위치에서 재현 테스트 또는 전이 검증을 마칠
> 때까지 유효한 review backlog로 유지한다.

## 1. 잘못된 로직: 실행은 되지만 결과가 틀림

### BUG-1. another_guide rewind가 엉뚱한 phase에 착지할 수 있음

- Severity: LOW / latent
- Area: `internal/react/coordinator/iteration.go` `preferredPreGuidanceIndex`
- Scenario: 사용자가 CRD 클러스터를 진단한다. resource guide가 주입되어 `guided_diagnosis`까지 진행했지만 결론이 나지 않아 inconclusive `final_report`가 나온다. 런타임이 "다른 guide 각도", "직접 다른 방향", "종료"를 제시하고 사용자가 다른 guide 각도를 선택한다.
- What happens: `continueWithGuideFocus`는 guide 상태를 리셋하고 guidance 이전 phase로 되감아야 한다. fallback은 `strings.Contains(name, "guidance")`와 `final_report`만 제외한다. `guided_diagnosis`에는 `guidance` 부분 문자열이 없어 제외되지 않는다. preferred pre-guidance phase가 없는 custom plan에서는 되감기가 `guided_diagnosis`에 착지할 수 있고, `guidance_lookup`은 더 낮은 index라 forward-only phase graph에서 다시 도달하지 못한다.
- Why wrong: guidance 계열 phase 판정이 부분 문자열에 의존하고 `guided_diagnosis`를 명시적으로 제외하지 않는다.
- Note: 표준 plan에는 `context_resolution` 같은 preferred phase가 있어 latent로 분류한다.

### BUG-2. delete mutation verification이 NotFound에서 deadlock될 수 있음

- Status: RESOLVED
- Severity: HIGH
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/verification`, `internal/react/coordinator/state.go`
- Scenario: 사용자가 "prod 네임스페이스 web 파드 삭제해줘"라고 요청한다. 모델이 `kubectl delete pod web -n prod`를 제안하고 사용자가 승인한다. 삭제는 성공한다.
- Resolution: active verification command의 Kubernetes `NotFound`, `Forbidden`, `Unauthorized` 응답은 observation으로 기록한다. NotFound는 state-bearing evidence이고 Forbidden/Unauthorized는 access blocker다. Access blocker는 `failed` 판단에는 사용할 수 있지만 `satisfied`/`waiting`을 성립시키지 못한다.

### BUG-3. exec/run/cp/debug/attach/apply -k target extraction이 잘못된 verification requirement를 만들 수 있음

- Status: RESOLVED for mutation verification
- Severity: MEDIUM
- Area: `internal/react/coordinator/execution.go`, `internal/react/coordinator/iteration.go`, `internal/react/kube/resource.go`, `internal/react/flow/verification`
- Scenario A: 사용자가 "kustomize overlay 반영해줘"라고 요청하고 모델이 `kubectl apply -k ./overlays/prod`를 실행한다.
- Scenario B: 사용자가 "이 파드에서 명령 실행해줘"라고 요청하고 모델이 `kubectl exec mypod -- ...`를 실행한다.
- Resolution: mutation verification은 command 위치 인자에서 resource/name을 추론하지 않는다. Single mutation은 실행 전에 concrete action target이 필요하고 ordered check는 action target을 상속하거나 concrete override를 선언한다. `firstKubectlResourceArg` 계열 helper는 namespace/command safety gate에만 남아 있으며 verification requirement를 만들지 않는다.

### BUG-5. shim mode에서 structured ack가 native FunctionCallResult로 주입됨

- Status: RESOLVED
- Severity: HIGH
- Area: `internal/react/coordinator/iteration.go`, `internal/react/protocol`
- Scenario: shim mode provider에서 resource guide 진단 또는 mutation verification을 사용한다. 모델이 `guide_progress` 또는 `mutation_verification_result`를 JSON shim으로 반환한다.
- Resolution: 모든 structured ack는 `appendFunctionCallResult`를 사용한다. Shim mode는 문자열 observation으로, native mode는 `gollm.FunctionCallResult`로 기록한다.

### BUG-6. correction dedup이 복구 후에도 유지되어 조기 종료할 수 있음

- Severity: MEDIUM
- Area: `internal/react/coordinator/loop.go`, `internal/react/coordinator/iteration.go`, `internal/react/coordinator/state.go`
- Scenario: 긴 진단 중 모델이 한 번 잘못된 `phase_progress`를 내고 runtime correction 후 정상 복구한다. 여러 iteration 뒤 동일한 종류의 실수를 다시 한다.
- What happens: `contextBlockHashes`가 query 내내 유지되어 같은 `(code, message)` correction이 다시 발생하면 `appendContextBlock`이 false를 반환할 수 있다. 이 경우 즉시 "반복되어 진단 중단" 경로로 갈 수 있다.
- Why wrong: 모델이 correction을 무시하고 즉시 반복한 경우와, 정상 복구 후 독립적으로 재발한 경우를 구분하지 않는다.

### BUG-7. multi-sink phase plan을 부당하게 거부할 수 있음

- Severity: LOW / latent
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/phase`
- Scenario: 사용자가 "점검하고 문제 있으면 조치안을 내고, 없으면 요약만 해줘"라고 요청한다. 모델이 `triage -> answer_path`와 `triage -> escalate_path`처럼 두 개의 leaf phase를 가진 정상 DAG plan을 낸다.
- What happens: `phaseStepHasLaterStep`는 단순히 더 큰 index가 존재하면 non-terminal로 본다. 따라서 index 2인 `answer_path` 뒤에 index 3인 `escalate_path`가 있으면 `answer_path`도 allowed_next가 필요한 non-terminal처럼 취급된다.
- Why wrong: terminal phase를 "최대 index"로만 판단한다. 실제 DAG에는 여러 sink가 있을 수 있다.

## 2. 빠진 로직: 있어야 할 판정이 없음

### BUG-8. phase_progress에 관찰 성공/evidence_useful 가드가 없음

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/phase`
- Scenario: 관찰 command가 RBAC blocked, NotFound, tool error로 돌아왔는데 모델이 `phase_progress{evidence_useful:false}` 또는 유사한 완료 보고를 낸다.
- Missing logic: `phaseProgressFromFunctionCall`은 `evidence_useful`을 파싱하지만 `acceptProgress`는 이 값을 사용하지 않는다. 최신 observation 상태도 확인하지 않는다. 반면 guide progress의 action-embedded 경로는 `guideProgressObservationUseful(result)`로 성공 관찰만 step completion에 반영한다.
- Impact: 실제 evidence 없이 top-level phase가 완료될 수 있다.

### BUG-9. progressing/unresolved mutation result가 recheck를 재무장하지 않음

- Status: RESOLVED
- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/verification`, `internal/react/coordinator/state.go`
- Scenario: 사용자가 deployment scale/remediation을 승인한다. 변경 후 검증했더니 rollout이 아직 진행 중이라 모델이 `mutation_verification_result.status=progressing`을 반환한다.
- Resolution: mutation action은 `mode=await_state`를 미리 선언한다. `waiting` result는 같은 VerificationID와 AttemptID를 유지하고 runtime wait 후 같은 check를 재무장한다. 각 result는 최신 observation을 참조해야 한다.

### BUG-10. verification phase의 순서/역할 제약 검증이 없음

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/phase`
- Scenario: 모델이 `mutation_execution -> final_report -> verification_observation` 순서의 phase plan을 낸다. 또는 `verification_planning`처럼 실제 관찰이 아닌 phase 이름만 넣는다.
- Missing logic: `phasePlanHasVerificationPhase`는 verification phase가 어딘가 존재하는지만 확인하고, mutation execution 이후이면서 response/final 이전인지 검증하지 않는다. 이름도 `verify`/`verification` substring으로 통과한다.
- Impact: mutation request에 verification phase가 필요하다는 gate가 실제 실행 순서를 보장하지 못한다.

### BUG-11. ReAct-owned prompt에서 빈 입력 가드가 없음

- Severity: MEDIUM
- Area: `internal/orchestrator/orchestrator.go`, `internal/react/coordinator/input.go`, `internal/react/session/control.go`, `internal/react/contract/enums.go`
- Scenario: agent가 "직접 다른 방향 입력"을 기다리는 중 사용자가 실수로 Enter만 누른다.
- Missing logic: 일반 input path에는 빈 입력을 무시하는 guard가 있지만, `handleAgentInputRequest`에는 없다. `DecideInputDispatch`도 `RuntimeControlAwaitingContinuationText`와 `RuntimeControlAwaitingUserQuery`에서 `InputEmpty`를 accepted로 처리한다.
- Impact: 빈 문자열이 loop로 전달되어 continuation text 취소/종료처럼 동작할 수 있다. 문서의 "빈 응답을 loop에 보내지 않고 prompt 유지" 계약과 불일치한다.

### BUG-12. standalone __guide_progress__가 최신 관찰 성공 여부를 검증하지 않음

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/guidance`
- Scenario: guide step 관찰 command가 blocked/error였는데 모델이 별도 `__guide_progress__` call로 step 완료를 보고한다.
- Missing logic: `consumeGuideProgress`는 `evidence_useful=false`와 invalid step만 거부한다. 직전 observation이 성공했는지는 확인하지 않는다.
- Impact: failed/blocked evidence 뒤에도 guide step이 완료되고, 모든 step 완료 시 post-guide directive로 넘어갈 수 있다.

## 3. Dead / unreachable / latent branches

### BUG-13. different_approach inline continuation branch is unreachable after final_report

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/direction`
- Scenario: inconclusive report 후 사용자가 "guide 말고 다른 방식으로 진단해줘"라는 `different_approach` option을 선택한다.
- What happens: `continuingAfterFinalReport := l.pendingFinalReport != nil`가 option 처리 전에 계산된다. final report 이후 continuation에서는 이 값이 true라서 `different_approach`도 `continueAfterFinalReport -> continueWithGuideFocus` 경로를 탄다. `applyDirectionOption` 안의 inline directive injection branch는 final report 이후에는 도달하지 않는다.
- Why wrong: 문서상 `different_approach`는 guide rewind 없이 사용자 지시를 주입하고 재개해야 한다. 현재는 `another_guide`와 유사하게 guide lookup을 다시 열 수 있다.

### BUG-14. mutation recheck budget exhausted branch is effectively dead

- Status: RESOLVED
- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/verification`
- Scenario: mutation verification이 계속 `progressing`이라 세 번 recheck 후 inconclusive report를 강제해야 한다.
- Resolution: await-state check는 request-fixed verification evidence budget에서 계산한 bounded recheck counter를 직접 증가시킨다. 기본값은 initial observation 1회와 recheck 5회이며, 소진 시 mutation attempt를 unknown으로 닫고 mutation 자동 반복 없이 다른 safe strategy 또는 inconclusive 종료로 전환한다.

### BUG-15. choiceInputYesNo path is production-dead and bypass-prone if reintroduced

- Status: RESOLVED
- Severity: LOW / latent
- Area: `internal/orchestrator/orchestrator.go`, `internal/react/coordinator/input.go`
- Scenario: 현재 approval과 continuation choice는 모두 numbered `UserChoiceRequest`로 렌더링된다. agent prompt 중 `(y/n)` 문자열을 쓰는 production `UserChoiceRequest`는 보이지 않는다.
- Resolution: `UserChoiceRequest`는 번호 입력만 사용하고 `DecideInputDispatch`가
  `InputHandlerReactChoice`를 허용한 경우에만 전달한다. 문자열 `(y/n)` 감지와 remapping은 제거했다.

### BUG-16. guideProgressAllowedForCurrentPhase nil-phase branch is dead but unsafe

- Status: RESOLVED
- Severity: LOW / latent
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/guidance`
- Scenario: resource guide가 정상 주입되면 `guideStepState`는 `guided_diagnosis` phase 진입과 함께 설정된다.
- Resolution: `guideStepState` 또는 `phaseStepState`가 없으면 fail-closed 처리하고, 현재 phase가
  `guided_diagnosis`일 때만 guide progress를 허용한다.

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
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/phase`
- Scenario: 모델이 `current_phase_index`를 mutation execution phase로 지정하고 앞선 observation/planning phase를 선언만 한 plan을 낸다.
- Evidence: `phasePlanValid`는 current index가 선언되어 있는지만 확인한다.
- Impact: 앞선 필수 phase completion 근거 없이 execution phase에서 시작할 수 있다.

### BUG-22. shim JSON extraction mishandles multiple json code blocks

- Severity: LOW
- Area: `internal/react/protocol/shim.go`, `internal/react/coordinator/iteration.go`
- Scenario: shim mode 모델이 설명용 JSON block과 실제 ReAct JSON block을 함께 출력한다.
- Evidence: `extractJSON`은 첫 ` ```json` marker부터 마지막 fence까지 자른다.
- Impact: 복구 가능한 응답이 parse failure/query abort로 이어질 수 있다.

## 6. follow-up / request context issues

### BUG-23. keyword-based previous request retry can false-positive on common Korean wording

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/request`
- Scenario: 직전 요청이 pod 진단이었다. 다음에 사용자가 새 설명 요청으로 "설명이 아닌 예시를 보여줘" 또는 "정확한 용어 예시를 알려줘"라고 입력한다.
- Evidence: `shouldRetryPreviousRequest`는 `다시`, `정확`, `아닌` 같은 흔한 단어가 `originalQuery`에 있으면 retry 후보로 본다. 새 requirement analysis가 `conversation`/`unknown`이거나 clarify action이면 `applyPriorContextToFollowUpRequirementAnalysis`가 새 분석을 통째로 `lastRequirementAnalysis` clone으로 교체한다.
- Why wrong: "이전 답이 틀렸으니 같은 작업을 다시 하라"는 의도와, 새 질문 안의 일반 부정/정확성 표현을 구분하지 않는다.
- Impact: 새 conversation/follow-up 질문이 직전 Kubernetes 진단 재실행으로 바뀔 수 있다.

### BUG-24. follow-up all-namespaces intent can be overwritten by prior namespace

- Severity: MEDIUM
- Area: `internal/react/coordinator/iteration.go`, `internal/react/flow/request`, `internal/react/coordinator/state.go`
- Scenario: 직전 요청이 `tenant-a` namespace의 cluster 진단이었다. 다음에 사용자가 "이번엔 모든 namespace에서 관련 pod를 확인해줘"라고 한다. 모델이 `scope.namespace="all_namespaces"`로 표현하지만 `scope.type="all_namespaces"`는 빠뜨린다.
- Evidence: `requirementAnalysisFromFunctionCall`은 `scope.namespace`가 all-namespaces 값이면 `analysis.Scope.Namespace`를 비워 둔다. 이후 prior context merge에서 `analysis.Scope.Namespace`가 비어 있으면 이전 namespace를 다시 채운다. `requestContextFromRequirementAnalysis`의 all-namespaces 보정은 `scope.type=="all_namespaces"`일 때만 확실히 동작한다.
- Why wrong: explicit all-namespaces intent가 namespace 값 표현으로 들어온 경우, follow-up defaulting이 이를 "namespace 없음"으로 오해한다.
- Impact: 사용자는 전체 namespace 조회를 의도했지만 runtime context가 이전 단일 namespace로 좁아질 수 있다.

## Resolved during state/package renewal

### BUG-4. guided_diagnosis phase_progress와 동반 action 실행

- Status: RESOLVED in `issue-12/renew-state` working tree.
- Current evidence: `coordinator/loop.go`가 requested structured-output lock을 consumer보다 먼저 적용하고, guide completion 뒤에도 lock을 다시 적용한다. `consumePhaseProgress`도 `phase_progress`가 유일한 call이 아니면 `phase_progress_mixed_output`으로 거부한다.
- Scenario result: `phase_progress + kubectl action` 응답은 phase를 전진시키거나 action을 dispatch하지 않는다.
