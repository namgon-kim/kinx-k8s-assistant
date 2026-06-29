package orchestrator

import (
	"context"
	"crypto/sha1"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
	guidance "github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
)

type incidentGuidancePhase int

const (
	incidentGuidanceIdle incidentGuidancePhase = iota
	incidentGuidanceOfferPending
)

type IncidentGuidanceFlow struct {
	phase         incidentGuidancePhase
	lastHash      string
	lastUserQuery string
	problemText   string
	evidence      []string
}

func NewIncidentGuidanceFlow() *IncidentGuidanceFlow {
	return &IncidentGuidanceFlow{}
}

func (f *IncidentGuidanceFlow) AfterAgentText(o *Orchestrator, text string) error {
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

func (f *IncidentGuidanceFlow) hasPendingOffer() bool {
	return f != nil && f.phase == incidentGuidanceOfferPending
}

func (f *IncidentGuidanceFlow) deferOffer() {
	if f != nil && f.phase == incidentGuidanceOfferPending {
		f.phase = incidentGuidanceIdle
	}
}

func (f *IncidentGuidanceFlow) handleChoiceRunbookSearch(o *Orchestrator) error {
	message := "관련 runbook을 검색하고 있습니다. 잠시만 기다려 주세요."
	PrintMessage(o.formatter.FormatText(message))
	o.logEntry("incident_guidance_search_started", message)

	summary, found, err := f.runIncidentGuidance(o)
	if err != nil {
		fmt.Println(colorBrightMagenta + "❌ runbook 검색 실패: " + err.Error() + colorReset)
		o.logEntry("incident_guidance_search_error", err.Error())
		f.reset()
		return nil
	}
	if !found {
		message := "검색된 runbook이 없어 incident guidance를 종료합니다."
		PrintMessage(o.formatter.FormatText(message))
		o.logEntry("incident_guidance_search", "no_runbook_match")
		f.reset()
		return nil
	}
	PrintMessage(o.formatter.FormatText(summary))
	o.logEntry("incident_guidance_search", summary)
	f.reset()
	return nil
}

func (f *IncidentGuidanceFlow) runIncidentGuidance(o *Orchestrator) (string, bool, error) {
	client, err := guidance.NewIncidentClient(o.cfg)
	if err != nil {
		return "", false, err
	}

	signal := f.buildProblemSignal(o)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.Analyze(ctx, signal)
	if err != nil {
		return "", false, err
	}
	if !incidentGuidanceResultUsable(result) {
		return "", false, nil
	}

	return formatIncidentGuidanceSummary(result), true, nil
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
	target := diagnostic.KubernetesTarget{}
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

	target.Kind, target.Name = extractKubernetesKindName(text)
	if strings.EqualFold(target.Kind, "pod") {
		target.PodName = target.Name
	}
	if target.Name == "" {
		if match := regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9-]*oom[a-z0-9-]*)\b`).FindStringSubmatch(text); len(match) == 2 {
			target.Kind = "pod"
			target.Name = match[1]
			target.PodName = match[1]
		}
	}
	if target.Name == "" {
		if match := regexp.MustCompile(`(?i)\b(test-[a-z0-9-]+)\b`).FindStringSubmatch(text); len(match) == 2 {
			target.Kind = "pod"
			target.Name = match[1]
			target.PodName = match[1]
		}
	}
	return target
}

func extractKubernetesKindName(text string) (string, string) {
	kindPattern := `(?i)\b(pod|pods|deployment|deployments|node|nodes|statefulset|statefulsets|daemonset|daemonsets|job|jobs|cronjob|cronjobs|service|services|ingress|ingresses|cluster|clusters)\b`
	namePattern := `['"]?([a-z0-9][a-z0-9_.-]*)['"]?`
	patterns := []string{
		kindPattern + `\s*(?:named|name)?\s+` + namePattern,
		namePattern + `\s*(?:이라는|인)?\s*` + kindPattern,
		kindPattern + `/` + namePattern,
	}
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if len(match) == 3 {
			if strings.Contains(pattern, namePattern+`\s*`) {
				return normalizeTargetKind(match[2]), match[1]
			}
			return normalizeTargetKind(match[1]), match[2]
		}
	}
	return "", ""
}

func normalizeTargetKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "pods":
		return "pod"
	case "deployments":
		return "deployment"
	case "nodes":
		return "node"
	case "statefulsets":
		return "statefulset"
	case "daemonsets":
		return "daemonset"
	case "jobs":
		return "job"
	case "cronjobs":
		return "cronjob"
	case "services":
		return "service"
	case "ingresses":
		return "ingress"
	case "clusters":
		return "cluster"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
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
	if strings.Contains(lower, "errimagepull") {
		types = append(types, diagnostic.DetectionErrImagePull)
	}
	if strings.Contains(lower, "pending") {
		types = append(types, diagnostic.DetectionPending)
	}
	if strings.Contains(lower, "failedscheduling") {
		types = append(types, diagnostic.DetectionFailedScheduling)
	}
	if strings.Contains(lower, "probe failed") || strings.Contains(lower, "readiness probe") || strings.Contains(lower, "liveness probe") {
		types = append(types, diagnostic.DetectionProbeFailed)
	}
	if strings.Contains(lower, "no endpoints") {
		types = append(types, diagnostic.DetectionServiceNoEndpoint)
	}
	if strings.Contains(lower, "connection refused") {
		types = append(types, diagnostic.DetectionNetworkFailure)
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") {
		types = append(types, diagnostic.DetectionTimeout)
	}
	if strings.Contains(lower, "no space left") {
		types = append(types, diagnostic.DetectionDiskFull)
	}
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "forbidden") {
		types = append(types, diagnostic.DetectionPermissionDenied)
	}
	if strings.Contains(lower, "createcontainerconfigerror") {
		types = append(types, diagnostic.DetectionConfigError)
	}
	if strings.Contains(lower, "nodenotready") || strings.Contains(lower, "notready") {
		types = append(types, diagnostic.DetectionType("NodeNotReady"))
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
	if summary := strings.TrimSpace(result.Plan.Summary); summary != "" {
		b.WriteString("- 권장 방향: " + summary + "\n")
	} else if len(result.Runbook.Cases) > 0 {
		b.WriteString("- 권장 방향: " + firstNonEmptyIncidentText(result.Runbook.Cases[0].Resolution, result.Runbook.Cases[0].Cause, result.Runbook.Cases[0].Title) + "\n")
	}
	b.WriteString(fmt.Sprintf("- 위험도: %s\n", result.Plan.RiskLevel))
	if result.Validation != nil && !result.Validation.Valid {
		b.WriteString("- 주의: 일부 runbook 명령은 대상 정보가 부족해 자동 실행 후보에서 제외했습니다.\n")
	}

	steps := executableIncidentSummarySteps(result.Plan.Steps, 5)
	b.WriteString("\n**권장 단계**\n")
	for i, step := range steps {
		b.WriteString(fmt.Sprintf("%d. %s", i+1, step.Description))
		if cmd, ok := incidentSummaryStepCommand(step, result.Plan.Target); ok {
			b.WriteString(fmt.Sprintf(" `%s`", cmd))
		}
		b.WriteString("\n")
	}
	verifySteps := executableIncidentSummarySteps(result.Plan.Verification, 3)
	if len(verifySteps) > 0 {
		b.WriteString("\n**검증**\n")
		for _, step := range verifySteps {
			b.WriteString(fmt.Sprintf("- %s", step.Description))
			if cmd, ok := incidentSummaryStepCommand(step, result.Plan.Target); ok {
				b.WriteString(fmt.Sprintf(" `%s`", cmd))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func incidentGuidanceResultUsable(result *guidance.ClientResult) bool {
	if result == nil || result.Runbook == nil || len(result.Runbook.Cases) == 0 || result.Plan == nil {
		return false
	}
	if detectionTypesUnknown(result.Signal.DetectionTypes) {
		return false
	}
	if result.Validation != nil && !result.Validation.Valid {
		return false
	}
	if !incidentRunbookMatchesSignal(result.Runbook.Cases[0], result.Signal) {
		return false
	}
	if !incidentRunbookTargetCompatible(result.Runbook.Cases[0], result.Signal.Target) {
		return false
	}
	if len(result.Plan.Steps) == 0 && len(result.Plan.Verification) == 0 {
		return false
	}
	return true
}

func incidentRunbookMatchesSignal(c guidance.GuideCase, signal diagnostic.ProblemSignal) bool {
	signalTypes := nonUnknownDetectionTypes(signal.DetectionTypes)
	if len(signalTypes) == 0 {
		return false
	}
	if len(c.MatchTypes) == 0 {
		return false
	}
	for _, signalType := range signalTypes {
		for _, matchType := range c.MatchTypes {
			if strings.EqualFold(strings.TrimSpace(string(signalType)), strings.TrimSpace(string(matchType))) {
				return true
			}
		}
	}
	return false
}

func nonUnknownDetectionTypes(types []diagnostic.DetectionType) []diagnostic.DetectionType {
	var result []diagnostic.DetectionType
	for _, t := range types {
		if t != diagnostic.DetectionUnknown {
			result = append(result, t)
		}
	}
	return result
}

func incidentRunbookTargetCompatible(c guidance.GuideCase, target diagnostic.KubernetesTarget) bool {
	kind := normalizeTargetKind(target.Kind)
	if kind == "" {
		return true
	}
	if len(c.RelatedObjects) == 0 {
		return true
	}
	for _, related := range c.RelatedObjects {
		if normalizeTargetKind(related) == kind {
			return true
		}
	}
	return false
}

func detectionTypesUnknown(types []diagnostic.DetectionType) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t != diagnostic.DetectionUnknown {
			return false
		}
	}
	return true
}

func firstNonEmptyIncidentText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func executableIncidentSummarySteps(steps []guidance.PlanStep, limit int) []guidance.PlanStep {
	var result []guidance.PlanStep
	for _, step := range steps {
		result = append(result, step)
		if len(result) >= limit {
			return result
		}
	}
	return result
}

func incidentSummaryStepCommand(step guidance.PlanStep, target diagnostic.KubernetesTarget) (string, bool) {
	cmd := strings.TrimSpace(step.RenderedCommand)
	if cmd == "" || step.RequiresConfirmation || strings.Contains(cmd, "{{") || strings.Contains(cmd, " / ") {
		return "", false
	}
	if !incidentRenderedCommandComplete(cmd) {
		return "", false
	}
	if !incidentStepTemplateValuesAvailable(step, target) {
		return "", false
	}
	return cmd, true
}

func incidentRenderedCommandComplete(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, field := range fields {
		switch {
		case field == "-n" || field == "--namespace":
			if i+1 >= len(fields) || strings.HasPrefix(fields[i+1], "-") {
				return false
			}
		case strings.HasPrefix(field, "--namespace="):
			if strings.TrimSpace(strings.TrimPrefix(field, "--namespace=")) == "" {
				return false
			}
		}
	}
	return true
}

func incidentStepTemplateValuesAvailable(step guidance.PlanStep, target diagnostic.KubernetesTarget) bool {
	if strings.TrimSpace(step.CommandTemplate) == "" {
		return true
	}
	for _, name := range incidentTemplatePlaceholders(step.CommandTemplate) {
		if incidentTemplateValue(name, step, target) == "" {
			return false
		}
	}
	return true
}

func incidentTemplatePlaceholders(tmpl string) []string {
	seen := map[string]struct{}{}
	var names []string
	for {
		start := strings.Index(tmpl, "{{")
		if start < 0 {
			return names
		}
		tmpl = tmpl[start+2:]
		end := strings.Index(tmpl, "}}")
		if end < 0 {
			return names
		}
		name := strings.TrimSpace(tmpl[:end])
		if name != "" {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				names = append(names, name)
			}
		}
		tmpl = tmpl[end+2:]
	}
}

func incidentTemplateValue(name string, step guidance.PlanStep, target diagnostic.KubernetesTarget) string {
	if step.Variables != nil {
		if value := strings.TrimSpace(step.Variables[name]); value != "" {
			return value
		}
	}
	switch name {
	case "cluster":
		return strings.TrimSpace(target.Cluster)
	case "context":
		return strings.TrimSpace(target.Context)
	case "namespace":
		return strings.TrimSpace(target.Namespace)
	case "kind":
		return strings.TrimSpace(target.Kind)
	case "name":
		return strings.TrimSpace(target.Name)
	case "pod_name":
		if value := strings.TrimSpace(target.PodName); value != "" {
			return value
		}
		return strings.TrimSpace(target.Name)
	case "container", "container_name":
		return strings.TrimSpace(target.Container)
	case "owner_kind":
		return strings.TrimSpace(target.OwnerKind)
	case "owner_name":
		return strings.TrimSpace(target.OwnerName)
	case "node_name":
		return ""
	default:
		return ""
	}
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
		"requirement analysis 오류",
		"request context 오류",
		"action target 불일치",
		"parsing shim json",
		"context compact failed",
		"openai streaming error",
		"llm 응답 후보가 없습니다",
		"반복된 requirement analysis 오류",
		"반복된 request context 오류",
		"반복된 action target 불일치",
		"반복된 phase plan 오류",
		"반복된 phase progress 오류",
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
