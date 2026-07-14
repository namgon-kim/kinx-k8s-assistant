# Guidance Revise Notes

> 상태: 현재 구현 경계와 미결정 guidance 정책을 함께 추적하는 review.

## 목적

현재 guidance/log-analyzer 기능은 구현을 진행하면서 몇 가지 설계 쟁점이 남아 있다. 이 문서는 확정된 구현 사항과 아직 결정하지 않은 revise 논점을 분리해 정리한다.

## 현재 구현 기준

- `log-analyzer`와 `guidance`는 별도 도메인이다.
- `log-analyzer`는 optional MCP 서버로 로그/이벤트/메트릭 관측과 패턴 분석을 담당한다.
- `guidance`는 standalone MCP 서버가 아니라 `internal/guidance` 내장 client/package로 동작한다.
- `guidance`는 phase-owned resource guide 검색, incident guide 매칭, 운영 이슈 RAG 검색, 조치 계획 생성을 담당한다.
- Resource guide는 live observation 이후 accepted `guidance_lookup` phase와 CRD discovery 조건이 모두 충족될 때만 조회한다.
- 실제 Kubernetes 명령 실행은 k8s-assistant의 ReAct/tool loop와 승인 흐름이 담당한다.
- k8s-assistant의 `--mcp-client`는 `~/.k8s-assistant/mcp.yaml`에 선언된 MCP 서버만 kubectl-ai MCP 설정으로 동기화한다.
- `log-analyzer` MCP 서버는 선택 사항이다. `guidance`는 설정 파일이 없으면 embedded runbook/default로 동작한다.
- `guidance` 설정 기본 경로는 `~/.k8s-assistant/guidance.yaml`이다.
- Qdrant runbook 업로드는 런타임 기능이 아니라 검증/초기 적재용 helper인 `guidance-upload`가 수행한다.
- Qdrant 업로드에는 runbook text를 vector로 저장해야 하므로 embedding endpoint가 필요하다.

## 미결정 논점

### 1. incident guidance 호출 게이트

간단하고 명확한 문제까지 항상 RAG를 검색하면 오버헤드가 크다. 반대로 ReAct loop가 모든 해결책을 자체 판단하면 LLM의 사전 학습된 해결책과 runbook/RAG 결과가 충돌한다.

현재 논의된 방향:

```text
k8s-assistant ReAct loop가 1차 진단과 evidence 수집을 수행한다.
resource guide는 CRD-backed primary target과 guidance_lookup phase에서만 조회한다.
incident guidance는 ReAct continuation choice에 명시적 runbook 검색 option이 있을 때만 실행한다.
```

후보 self assessment:

```yaml
self_assessment:
  issue_detected: true
  confidence: high | medium | low
  can_remediate_directly: true | false
  needs_incident_guidance: true | false
  remediation_risk: low | medium | high
  reason: "<short Korean reason>"
```

검토할 점:

- LLM이 `low` 또는 `unknown`을 잘 선택하지 않을 수 있다.
- Orchestrator가 LLM 판단을 그대로 믿을지, runtime evidence/heuristic으로 보정할지 결정해야 한다.
- RAG 호출 비용과 운영 안정성 사이의 기준선을 정해야 한다.

### 2. 최종 판단권

명령 실행은 ReAct/tool loop가 담당하지만, 해결책을 다시 설계하면 guidance 계획과 충돌한다.

논의된 선택지:

| 선택지 | 설명 | 장점 | 위험 |
|---|---|---|---|
| ReAct 중심 | ReAct loop가 자체 판단으로 조치하고 guidance는 참고만 함 | 간단한 문제 처리 빠름 | RAG/runbook과 충돌, LLM 임의 해결 가능 |
| guidance 중심 | guidance가 해결 방향을 결정하고 ReAct loop는 실행만 함 | 일관된 runbook 기반 조치 | 간단한 문제에도 RAG/계획 생성 비용 발생 |
| 하이브리드 | ReAct loop가 고확신 단순 문제는 직접 처리, 불확실하면 guidance를 참고 | 비용과 안정성 균형 | confidence/게이트 설계 필요 |

현재 구현은 하이브리드에 가깝다. 단, guidance 결과가 나온 뒤에도 Kubernetes 변경 실행은 반드시 ReAct/tool loop, approval, mutation verification lifecycle을 통과해야 한다.

### 3. MCP server name과 tool name

kubectl-ai는 MCP tool을 `<server_name>_<tool_name>` 형태로 등록한다. 현재 guidance는 MCP 서버가 아니므로 이 논점은 log-analyzer 같은 optional MCP 서버에만 적용된다.

검토 방향:

- optional MCP server name에는 hyphen이 없는 이름을 권장한다.
- prompt에는 특정 MCP tool name을 하드코딩하지 않고 실제 등록된 tool 목록을 주입한다.
- guidance 관련 동작은 MCP tool name이 아니라 internal structured output과 client 호출 기준으로 문서화한다.

현재 코드 기준으로 `trouble-shooting` MCP 서버는 존재하지 않는다.

### 4. 언어 출력 안정화

상태: 구현됨.

- `lang.language=Korean`이고 별도 `lang.model`/`lang.endpoint`가 없으면 production prompt가 아래 언어 규칙을 main model에 적용한다.
- 별도 번역 model/endpoint가 있으면 main ReAct loop는 영어로 동작하고 `internal/react/language.Translator`가 user-facing 자연어만 번역한다.
- 번역 실패나 빈 응답은 영어 원문을 fallback으로 노출하지 않고 한국어 설정 오류를 반환한다.
- Kubernetes 이름, command, flag, JSON/YAML, field, literal, raw output은 번역하지 않는다.

Main-model Korean mode의 prompt 규칙:

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
2. Orchestrator가 self assessment를 파싱해 incident guidance 호출 여부를 결정한다.
3. optional MCP server name에는 hyphen 없는 이름을 권장한다.
4. guidance 결과를 자유 텍스트가 아니라 decision contract 형태로 구조화할지 결정한다.
5. delete/recreate 절차를 runbook/RAG에 반영하고, ReAct 실행 prompt에는 복구 가능한 절차만 허용하도록 제한한다.
