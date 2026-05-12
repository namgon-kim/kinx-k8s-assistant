# Trouble-shooting Revise Notes

## 목적

현재 trouble-shooting 기능은 구현을 진행하면서 몇 가지 설계 쟁점이 남아 있다. 이 문서는 확정된 구현 사항과 아직 결정하지 않은 revise 논점을 분리해 정리한다.

## 현재 구현 기준

- `log-analyzer`와 `trouble-shooting`은 별도 MCP 서버로 분리한다.
- `log-analyzer`는 로그/이벤트/메트릭 관측과 패턴 분석을 담당한다.
- `trouble-shooting`은 구조화 runbook 매칭, 운영 이슈 RAG 검색, 조치 계획 생성을 담당한다.
- 실제 Kubernetes 명령 실행은 kubectl-ai가 담당한다.
- k8s-assistant의 `--mcp-client`는 `~/.k8s-assistant/mcp.yaml`에 선언된 MCP 서버만 kubectl-ai MCP 설정으로 동기화한다.
- `log-analyzer`와 `trouble-shooting`은 모두 선택 사항이다.
- `trouble-shooting` 설정 기본 경로는 `~/.k8s-assistant/trouble-shooting.yaml`이다.
- Qdrant runbook 업로드는 런타임 기능이 아니라 검증/초기 적재용 helper인 `troubleshooting-upload`가 수행한다.
- Qdrant 업로드에는 runbook text를 vector로 저장해야 하므로 embedding endpoint가 필요하다.

## 미결정 논점

### 1. trouble-shooting 호출 게이트

간단하고 명확한 문제까지 항상 RAG를 검색하면 오버헤드가 크다. 반대로 kubectl-ai가 모든 해결책을 자체 판단하면 LLM의 사전 학습된 해결책과 runbook/RAG 결과가 충돌한다.

현재 논의된 방향:

```text
kubectl-ai가 1차 진단과 확신도 판단을 수행한다.
확신도가 높고 수정 대상/절차가 명확하면 trouble-shooting을 호출하지 않는다.
확신도가 낮거나 조치 대상/절차가 불명확하면 trouble-shooting을 호출한다.
```

후보 self assessment:

```yaml
self_assessment:
  issue_detected: true
  confidence: high | medium | low
  can_remediate_directly: true | false
  needs_troubleshooting: true | false
  remediation_risk: low | medium | high
  reason: "<short Korean reason>"
```

검토할 점:

- LLM이 `low` 또는 `unknown`을 잘 선택하지 않을 수 있다.
- Orchestrator가 LLM 판단을 그대로 믿을지, heuristic으로 보정할지 결정해야 한다.
- RAG 호출 비용과 운영 안정성 사이의 기준선을 정해야 한다.

### 2. 최종 판단권

명령 실행은 kubectl-ai가 담당하지만, 해결책을 다시 설계하면 trouble-shooting 계획과 충돌한다.

논의된 선택지:

| 선택지 | 설명 | 장점 | 위험 |
|---|---|---|---|
| kubectl-ai 중심 | kubectl-ai가 자체 판단으로 조치하고 trouble-shooting은 참고만 함 | 간단한 문제 처리 빠름 | RAG/runbook과 충돌, LLM 임의 해결 가능 |
| trouble-shooting 중심 | trouble-shooting이 해결 방향을 결정하고 kubectl-ai는 실행만 함 | 일관된 runbook 기반 조치 | 간단한 문제에도 RAG/계획 생성 비용 발생 |
| 하이브리드 | kubectl-ai가 고확신 단순 문제는 직접 처리, 불확실하면 trouble-shooting이 판단 | 비용과 안정성 균형 | confidence/게이트 설계 필요 |

현재 선호 방향은 하이브리드다. 단, 하이브리드에서도 trouble-shooting 결과가 나온 뒤에는 kubectl-ai가 임의로 다른 해결책을 만들지 않도록 실행 프롬프트를 제한해야 한다.

### 3. tool name과 MCP server name

kubectl-ai는 MCP tool을 `<server_name>_<tool_name>` 형태로 등록한다. `trouble-shooting`처럼 server name에 hyphen이 들어가면 tool calling 구현에 따라 `trouble-shooting_match_runbook` 같은 이름이 안정적으로 호출되지 않을 수 있다.

검토 방향:

- `~/.k8s-assistant/mcp.yaml`의 server name을 `troubleshooting`으로 바꾸는 방안
- prompt에서도 `troubleshooting_match_runbook`처럼 hyphen 없는 이름을 사용하는 방안
- 더 좋은 방향은 prompt에 tool name을 하드코딩하지 않고 실제 등록된 tool 목록을 주입하는 방안

현재 코드는 `trouble-shooting` 이름을 사용한다. 이 변경은 설정 호환성과 문서 변경을 동반하므로 별도 결정이 필요하다.

### 4. 언어 출력 안정화

한국어 지시가 있어도 모델이 영어, 일본어, 중국어를 섞는 현상이 발생했다. prompt에 다음 영문 지시를 추가하는 방향을 검토한다.

```text
All natural language responses must be written in Korean only.
Do not mix English, Japanese, Chinese, or any other language in explanations.
Keep Kubernetes resource names, field names, command names, flags, error messages, and raw command output in their original form.
If command output is in English, summarize and explain it in Korean.
If a tool or model produces non-Korean text, translate it to Korean before answering the user.
```

### 5. delete/recreate 조치 절차

`patch` 또는 `delete` 자체가 금지는 아니다. 문제는 복구 가능한 manifest 없이 먼저 삭제하고, 이후 재생성 방법을 LLM이 임의로 찾는 흐름이다.

정리된 원칙:

- 단순 patch/delete/apply도 사용자 승인 후 가능하다.
- delete/recreate가 필요하면 먼저 현재 리소스 YAML을 확보한다.
- runtime field를 제거하고 수정안을 만든 뒤 사용자에게 보여준다.
- 수정된 YAML 또는 owner resource 변경 방법이 준비된 뒤에만 삭제/재생성을 진행한다.
- Pod 문제에서는 먼저 `ownerReferences`를 확인한다.
- owner가 Deployment/StatefulSet/DaemonSet/Job/CronJob이면 Pod가 아니라 owner resource를 수정한다.
- standalone Pod이면 YAML export -> runtime field 제거 -> 수정안 제시 -> 승인 -> apply/delete 순서로 진행한다.
- `kubectl run -it --rm`을 기존 리소스 재생성 대체 수단으로 사용하지 않는다.

## 다음 revise 후보

1. self assessment block을 system prompt에 추가한다.
2. Orchestrator가 self assessment를 파싱해 trouble-shooting 호출 여부를 결정한다.
3. trouble-shooting 결과가 tool call 실패 또는 빈 계획이면 자동 진행 질문을 표시하지 않는다.
4. MCP server name을 `troubleshooting`으로 변경할지 결정한다.
5. trouble-shooting 결과를 자유 텍스트가 아니라 decision contract 형태로 구조화할지 결정한다.
6. delete/recreate 절차를 runbook/RAG에 반영하고, kubectl-ai 실행 prompt에는 복구 가능한 절차만 허용하도록 제한한다.
