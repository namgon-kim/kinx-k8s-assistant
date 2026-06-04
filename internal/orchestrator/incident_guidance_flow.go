package orchestrator

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
	guidance "github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react"
)

type incidentGuidancePhase int

const (
	incidentGuidanceIdle incidentGuidancePhase = iota
	incidentGuidanceOfferPending
	incidentGuidanceSearchRequested
	incidentGuidanceRemediationRequested
)

type IncidentGuidanceFlow struct {
	phase            incidentGuidancePhase
	lastHash         string
	lastUserQuery    string
	problemText      string
	evidence         []string
	searchBrief      []string
	remediationBrief string
}

func NewIncidentGuidanceFlow() *IncidentGuidanceFlow {
	return &IncidentGuidanceFlow{}
}

func (f *IncidentGuidanceFlow) AfterAgentText(o *Orchestrator, text string) error {
	if f.phase == incidentGuidanceSearchRequested {
		f.searchBrief = appendBounded(f.searchBrief, text, 4)
		return nil
	}
	if f.phase != incidentGuidanceIdle {
		return nil
	}
	if o.agentWrap == nil {
		return nil
	}
	if !shouldOfferIncidentGuidanceForQuery(f.lastUserQuery) {
		return nil
	}
	if isInternalRuntimeErrorText(text) {
		return nil
	}
	if !looksIncidentGuidanceWorthy(text) {
		return nil
	}

	hash := stableTextHash(text)
	if hash == f.lastHash {
		return nil
	}
	f.lastHash = hash
	f.evidence = []string{text}
	f.problemText = text
	f.phase = incidentGuidanceOfferPending

	return nil
}

func (f *IncidentGuidanceFlow) RecordEvidence(text string) {
	if f.phase == incidentGuidanceSearchRequested {
		f.searchBrief = appendBounded(f.searchBrief, text, 4)
		return
	}
	if isInternalRuntimeErrorText(text) {
		return
	}
	if f.phase == incidentGuidanceIdle && looksIncidentGuidanceWorthy(text) {
		f.evidence = appendBounded(f.evidence, text, 3)
	}
}

func (f *IncidentGuidanceFlow) ObserveUserInput(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	f.lastUserQuery = query
}

func (f *IncidentGuidanceFlow) BeforeUserInput(o *Orchestrator, activeAgent *react.Loop) (bool, error) {
	if f.phase == incidentGuidanceRemediationRequested {
		f.reset()
		return false, nil
	}
	if f.phase == incidentGuidanceOfferPending {
		return f.handleOffer(o, activeAgent)
	}
	if f.phase == incidentGuidanceSearchRequested {
		return f.handleRemediationApproval(o, activeAgent)
	}
	return false, nil
}

func (f *IncidentGuidanceFlow) handleOffer(o *Orchestrator, activeAgent *react.Loop) (bool, error) {
	input, err := getInputWithUIEchoNoHistory("감지된 문제에 대해 해결 방법을 찾아볼까요? (y/n): ", o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			f.reset()
			activeAgent.SendInput(&api.UserInputResponse{Query: ""})
			return true, nil
		}
		return true, err
	}
	if !isYes(input) {
		o.logEntry("incident_guidance_offer", "declined")
		f.reset()
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return true, nil
	}

	summary, err := f.runIncidentGuidance(o)
	if err != nil {
		fmt.Println(colorBrightMagenta + "❌ incident guidance 조회 실패: " + err.Error() + colorReset)
		o.logEntry("incident_guidance_search_error", err.Error())
		f.reset()
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return true, nil
	}

	PrintMessage(o.formatter.FormatText(summary))
	o.logEntry("incident_guidance_search", summary)
	f.phase = incidentGuidanceSearchRequested
	f.searchBrief = []string{summary}
	f.remediationBrief = summary
	activeAgent.SendInput(&api.UserInputResponse{Query: ""})
	return true, nil
}

func (f *IncidentGuidanceFlow) handleRemediationApproval(o *Orchestrator, activeAgent *react.Loop) (bool, error) {
	input, err := getInputWithUIEchoNoHistory("해결을 진행할까요? (y/n): ", o.cfg.HistoryFile)
	if err != nil {
		if err == io.EOF {
			f.reset()
			activeAgent.SendInput(&api.UserInputResponse{Query: ""})
			return true, nil
		}
		return true, err
	}
	if !isYes(input) {
		o.logEntry("incident_guidance_remediation", "declined")
		f.reset()
		activeAgent.SendInput(&api.UserInputResponse{Query: ""})
		return true, nil
	}

	prompt := f.buildRemediationPrompt()
	o.logEntry("incident_guidance_remediation", prompt)
	f.phase = incidentGuidanceRemediationRequested
	activeAgent.SendInput(&api.UserInputResponse{Query: prompt})
	return true, nil
}

func (f *IncidentGuidanceFlow) buildRemediationPrompt() string {
	return fmt.Sprintf(`사용자가 incident guidance 조치 계획 기반 진행을 승인했습니다.

아래 incident guidance 결과를 바탕으로 문제 해결을 진행하세요.

진행 규칙:
1. 먼저 현재 클러스터 상태를 다시 확인하세요.
2. 진단 명령은 실행해도 됩니다.
3. 리소스 변경, 삭제, 재시작, scale, patch, apply, set resources 작업 전에는 반드시 구체적인 변경 내용을 사용자에게 승인받으세요.
4. incident guidance 결과는 계획 근거입니다. 새로운 incident guidance/log-analyzer 도구 호출을 반복하지 마세요.
5. 실행 결과와 다음 조치를 한국어로 요약하세요.

incident guidance 결과 요약:
%s`, f.remediationBrief)
}

func (f *IncidentGuidanceFlow) runIncidentGuidance(o *Orchestrator) (string, error) {
	client, err := guidance.NewIncidentClient(o.cfg)
	if err != nil {
		return "", err
	}

	signal := f.buildProblemSignal(o)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.Analyze(ctx, signal)
	if err != nil {
		return "", err
	}

	return formatIncidentGuidanceSummary(result), nil
}

func (f *IncidentGuidanceFlow) buildProblemSignal(o *Orchestrator) diagnostic.ProblemSignal {
	text := strings.Join(append([]string{f.problemText}, f.evidence...), "\n")
	target := extractTarget(text)
	if o.kubeconfigInfo != nil {
		target.Context = o.kubeconfigInfo.CurrentContext
	}
	return diagnostic.ProblemSignal{
		ID:             fmt.Sprintf("signal-%x", sha1.Sum([]byte(text)))[:20],
		Source:         diagnostic.DetectionSourceAgentText,
		DetectedBy:     diagnostic.ComponentKubectlAI,
		DetectionTypes: detectTypes(text),
		Severity:       severityForText(text),
		Confidence:     diagnostic.ConfidenceHigh,
		Summary:        strings.TrimSpace(f.problemText),
		Target:         target,
		Evidence: []diagnostic.Evidence{{
			Source:  diagnostic.DetectionSourceAgentText,
			Message: strings.TrimSpace(text),
		}},
	}
}

func extractTarget(text string) diagnostic.KubernetesTarget {
	target := diagnostic.KubernetesTarget{Kind: "pod"}
	if match := regexp.MustCompile(`([A-Za-z0-9_-]+)\s*네임스페이스`).FindStringSubmatch(text); len(match) == 2 {
		target.Namespace = match[1]
	}
	if target.Namespace == "" {
		if match := regexp.MustCompile(`(?i)-n\s+([a-z0-9-]+)`).FindStringSubmatch(text); len(match) == 2 {
			target.Namespace = match[1]
		}
	}
	if target.Namespace == "" {
		if match := regexp.MustCompile(`(?i)namespace\s+([a-z0-9-]+)`).FindStringSubmatch(text); len(match) == 2 {
			target.Namespace = match[1]
		}
	}

	patterns := []string{
		`(?i)([a-z0-9][a-z0-9_.-]*)이라는\s*포드`,
		`(?i)포드\s+['"]?([a-z0-9][a-z0-9_.-]*)['"]?`,
		`(?i)pod\s+named\s+['"]?([a-z0-9][a-z0-9_.-]*)['"]?`,
		`(?i)\bpod\s+['"]?([a-z0-9][a-z0-9_.-]*)['"]?\b`,
	}
	for _, pattern := range patterns {
		if match := regexp.MustCompile(pattern).FindStringSubmatch(text); len(match) == 2 {
			target.Name = match[1]
			target.PodName = match[1]
			break
		}
	}
	if target.Name == "" {
		if match := regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9-]*oom[a-z0-9-]*)\b`).FindStringSubmatch(text); len(match) == 2 {
			target.Name = match[1]
			target.PodName = match[1]
		}
	}
	if target.Name == "" {
		if match := regexp.MustCompile(`(?i)\b(test-[a-z0-9-]+)\b`).FindStringSubmatch(text); len(match) == 2 {
			target.Name = match[1]
			target.PodName = match[1]
		}
	}
	return target
}

func detectTypes(text string) []diagnostic.DetectionType {
	lower := strings.ToLower(text)
	var types []diagnostic.DetectionType
	if strings.Contains(lower, "crashloopbackoff") {
		types = append(types, diagnostic.DetectionCrashLoopBackOff)
	}
	if strings.Contains(lower, "oomkilled") || strings.Contains(lower, "out of memory") || strings.Contains(lower, "메모리 부족") {
		types = append(types, diagnostic.DetectionOOMKilled)
	}
	if strings.Contains(lower, "imagepullbackoff") {
		types = append(types, diagnostic.DetectionImagePullBackOff)
	}
	if strings.Contains(lower, "pending") {
		types = append(types, diagnostic.DetectionPending)
	}
	if len(types) == 0 {
		types = append(types, diagnostic.DetectionUnknown)
	}
	return types
}

func severityForText(text string) diagnostic.Severity {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "oomkilled") || strings.Contains(lower, "crashloopbackoff") {
		return diagnostic.SeverityCritical
	}
	return diagnostic.SeverityWarning
}

func formatIncidentGuidanceSummary(result *guidance.ClientResult) string {
	var b strings.Builder
	b.WriteString("**해결 방법 요약**\n\n")
	b.WriteString("- 추정 원인: " + summarizeDetectionTypes(result.Signal.DetectionTypes) + "\n")
	if len(result.Runbook.Cases) > 0 {
		b.WriteString("- 참고 runbook: " + result.Runbook.Cases[0].Title + "\n")
	}
	if result.Knowledge != nil && len(result.Knowledge.Cases) > 0 {
		b.WriteString("- 유사 사례: " + result.Knowledge.Cases[0].Title + "\n")
	}
	b.WriteString("- 권장 방향: 현재 Pod/이벤트/이전 로그로 OOMKilled 여부를 재확인한 뒤, 컨트롤러가 있는 워크로드라면 memory request/limit을 조정하고 rollout 상태를 검증합니다.\n")
	b.WriteString(fmt.Sprintf("- 위험도: %s\n", result.Plan.RiskLevel))
	if result.Validation != nil && !result.Validation.Valid {
		b.WriteString("- 주의: 일부 runbook 명령은 대상 정보가 부족해 자동 실행 후보에서 제외했습니다.\n")
	}

	steps := executableIncidentSummarySteps(result.Plan.Steps, 5)
	b.WriteString("\n**권장 단계**\n")
	for i, step := range steps {
		b.WriteString(fmt.Sprintf("%d. %s", i+1, step.Description))
		if step.RenderedCommand != "" && !step.RequiresConfirmation {
			b.WriteString(fmt.Sprintf(" `%s`", step.RenderedCommand))
		}
		b.WriteString("\n")
	}
	verifySteps := executableIncidentSummarySteps(result.Plan.Verification, 3)
	if len(verifySteps) > 0 {
		b.WriteString("\n**검증**\n")
		for _, step := range verifySteps {
			b.WriteString(fmt.Sprintf("- %s", step.Description))
			if step.RenderedCommand != "" {
				b.WriteString(fmt.Sprintf(" `%s`", step.RenderedCommand))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func executableIncidentSummarySteps(steps []guidance.PlanStep, limit int) []guidance.PlanStep {
	var result []guidance.PlanStep
	for _, step := range steps {
		cmd := strings.TrimSpace(step.RenderedCommand)
		if strings.Contains(cmd, "{{") || strings.Contains(cmd, " -n  ") || strings.Contains(cmd, " / ") || strings.HasSuffix(cmd, " -n") || strings.HasSuffix(cmd, "-n") {
			continue
		}
		result = append(result, step)
		if len(result) >= limit {
			return result
		}
	}
	return result
}

func summarizeDetectionTypes(types []diagnostic.DetectionType) string {
	values := make([]string, 0, len(types))
	for _, t := range types {
		values = append(values, string(t))
	}
	return strings.Join(values, ", ")
}

func (f *IncidentGuidanceFlow) reset() {
	f.phase = incidentGuidanceIdle
	f.problemText = ""
	f.evidence = nil
	f.searchBrief = nil
	f.remediationBrief = ""
}

func looksIncidentGuidanceWorthy(text string) bool {
	lower := strings.ToLower(text)
	if isInternalRuntimeErrorText(lower) {
		return false
	}
	keywords := []string{
		"crashloopbackoff", "imagepullbackoff", "errimagepull", "oomkilled",
		"failedscheduling", "pending", "back-off", "probe failed",
		"no endpoints", "no space left", "permission denied", "connection refused",
		"timeout", "deadline exceeded", "notready", "forbidden", "createcontainerconfigerror",
		"장애", "조치가 필요",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func isInternalRuntimeErrorText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	markers := []string{
		"next_directions 형식 오류",
		"final_report 형식 오류",
		"resource_guide_lookup 형식 오류",
		"requirement analysis",
		"request context",
		"action target",
		"shim json",
		"parsing shim json",
		"context compact failed",
		"openai streaming error",
		"llm 응답 후보가 없습니다",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func shouldOfferIncidentGuidanceForQuery(query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return false
	}
	actionIntent := []string{
		"해결", "수정", "고쳐", "조치", "복구",
		"troubleshoot", "troubleshooting", "fix", "resolve", "repair",
	}
	for _, keyword := range actionIntent {
		if strings.Contains(query, keyword) {
			return true
		}
	}
	if isKubernetesLookupOrSummaryQuery(query) {
		return false
	}
	diagnosticIntent := []string{
		"원인", "왜", "분석", "진단",
		"debug", "diagnose", "analyze", "why",
	}
	for _, keyword := range diagnosticIntent {
		if strings.Contains(query, keyword) {
			return true
		}
	}
	return false
}

func isKubernetesLookupOrSummaryQuery(query string) bool {
	readOnlyIntent := []string{
		"보여", "조회", "목록", "상세", "알려", "출력", "확인", "요약", "보고",
		"describe", "get", "list", "show", "summarize",
	}
	for _, keyword := range readOnlyIntent {
		if strings.Contains(query, keyword) {
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
		// max is tiny here (3-4); retaining the backing array is acceptable.
		return values[len(values)-max:]
	}
	return values
}
