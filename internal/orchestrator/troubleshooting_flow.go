package orchestrator

import (
	"crypto/sha1"
	"fmt"
	"io"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/agent"
)

type troubleshootingPhase int

const (
	troubleshootingIdle troubleshootingPhase = iota
	troubleshootingOfferPending
	troubleshootingSearchRequested
	troubleshootingRemediationRequested
)

type TroubleshootingFlow struct {
	phase       troubleshootingPhase
	lastHash    string
	problemText string
	evidence    []string
	searchBrief []string
}

func NewTroubleshootingFlow() *TroubleshootingFlow {
	return &TroubleshootingFlow{}
}

func (f *TroubleshootingFlow) AfterAgentText(o *Orchestrator, text string) error {
	if f.phase == troubleshootingSearchRequested {
		f.searchBrief = appendBounded(f.searchBrief, text, 4)
		return nil
	}
	if f.phase != troubleshootingIdle {
		return nil
	}
	if !o.cfg.MCPClient {
		return nil
	}
	if o.agentWrap == nil {
		return nil
	}
	if !looksTroubleshootable(text) {
		return nil
	}

	hash := stableTextHash(text)
	if hash == f.lastHash {
		return nil
	}
	f.lastHash = hash
	f.evidence = []string{text}
	f.problemText = text
	f.phase = troubleshootingOfferPending

	return nil
}

func (f *TroubleshootingFlow) RecordEvidence(text string) {
	if f.phase == troubleshootingSearchRequested {
		f.searchBrief = appendBounded(f.searchBrief, text, 4)
		return
	}
	if f.phase == troubleshootingIdle && looksTroubleshootable(text) {
		f.evidence = appendBounded(f.evidence, text, 3)
	}
}

func (f *TroubleshootingFlow) BeforeUserInput(o *Orchestrator, activeAgent *agent.AgentWrapper) (bool, error) {
	if f.phase == troubleshootingRemediationRequested {
		f.reset()
		return false, nil
	}
	if f.phase == troubleshootingOfferPending {
		return f.handleOffer(o, activeAgent)
	}
	if f.phase == troubleshootingSearchRequested {
		return f.handleRemediationApproval(o, activeAgent)
	}
	return false, nil
}

func (f *TroubleshootingFlow) handleOffer(o *Orchestrator, activeAgent *agent.AgentWrapper) (bool, error) {
	input, err := getInputWithUIEcho("감지된 문제에 대해 해결 방법을 찾아볼까요? (y/n): ", o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			f.reset()
			activeAgent.SendInput(&api.UserInputResponse{Query: ""})
			return true, nil
		}
		return true, err
	}
	if !isYes(input) {
		o.logEntry("troubleshooting_offer", "declined")
		f.reset()
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return true, nil
	}

	prompt := f.buildSearchPrompt(f.problemText)
	o.logEntry("troubleshooting_search", prompt)
	f.phase = troubleshootingSearchRequested
	f.searchBrief = nil
	activeAgent.SendInput(&api.UserInputResponse{Query: prompt})
	return true, nil
}

func (f *TroubleshootingFlow) handleRemediationApproval(o *Orchestrator, activeAgent *agent.AgentWrapper) (bool, error) {
	input, err := getInputWithUIEcho("이 조치 계획을 kubectl-ai로 자동 진행할까요? (y/n): ", o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			f.reset()
			activeAgent.SendInput(&api.UserInputResponse{Query: ""})
			return true, nil
		}
		return true, err
	}
	if !isYes(input) {
		o.logEntry("troubleshooting_remediation", "declined")
		f.reset()
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return true, nil
	}

	prompt := f.buildRemediationPrompt()
	o.logEntry("troubleshooting_remediation", prompt)
	f.phase = troubleshootingRemediationRequested
	activeAgent.SendInput(&api.UserInputResponse{Query: prompt})
	return true, nil
}

func (f *TroubleshootingFlow) buildSearchPrompt(problemText string) string {
	return fmt.Sprintf(`방금 응답에서 Kubernetes 문제가 감지되었습니다.

아래 내용을 ProblemSignal로 요약한 뒤, trouble-shooting 도구를 사용해 해결 방법을 찾아주세요.

수행 순서:
1. trouble-shooting_match_runbook으로 구조화 runbook을 매칭합니다.
2. trouble-shooting_search_knowledge로 과거 운영 이슈 RAG를 검색합니다.
3. trouble-shooting_build_remediation_plan으로 실행 전 조치 계획을 만듭니다.
4. log-analyzer 도구는 MCP 설정에 명시적으로 등록되어 있고 도구 목록에 보일 때만 사용합니다.
5. 아직 Kubernetes 변경 작업은 실행하지 마세요.
6. 원인, 근거, 권장 조치, 위험도, 사용자 확인이 필요한 작업을 한국어로 요약하세요.

감지된 문제:
%s`, problemText)
}

func (f *TroubleshootingFlow) buildRemediationPrompt() string {
	return fmt.Sprintf(`사용자가 trouble-shooting 조치 계획 기반 진행을 승인했습니다.

아래 trouble-shooting 결과를 바탕으로 kubectl-ai가 문제 해결을 진행하세요.

진행 규칙:
1. 먼저 현재 클러스터 상태를 다시 확인하세요.
2. 진단 명령은 실행해도 됩니다.
3. 리소스 변경, 삭제, 재시작, scale, patch, apply, set resources 작업 전에는 반드시 구체적인 변경 내용을 사용자에게 승인받으세요.
4. trouble-shooting은 계획 근거일 뿐이며, 실제 실행은 kubectl-ai 기본 도구와 기존 승인 흐름으로 수행하세요.
5. 실행 결과와 다음 조치를 한국어로 요약하세요.

trouble-shooting 결과 요약:
%s`, strings.Join(f.searchBrief, "\n\n"))
}

func (f *TroubleshootingFlow) reset() {
	f.phase = troubleshootingIdle
	f.problemText = ""
	f.evidence = nil
	f.searchBrief = nil
}

func looksTroubleshootable(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"crashloopbackoff", "imagepullbackoff", "errimagepull", "oomkilled",
		"failedscheduling", "pending", "back-off", "probe failed",
		"no endpoints", "no space left", "permission denied", "connection refused",
		"timeout", "deadline exceeded", "notready", "forbidden", "createcontainerconfigerror",
		"오류", "실패", "장애", "조치가 필요",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func isYes(input string) bool {
	normalized := strings.TrimSpace(strings.ToLower(input))
	return normalized == "y" || normalized == "yes" || normalized == "예" || normalized == "네"
}

func stableTextHash(text string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(text)))
	return fmt.Sprintf("%x", sum[:])
}

func appendBounded(values []string, value string, max int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	values = append(values, value)
	if len(values) > max {
		return values[len(values)-max:]
	}
	return values
}
