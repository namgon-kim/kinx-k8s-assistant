package react

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"k8s.io/klog/v2"
)

func (l *Loop) handleRequestedResourceGuideLookup(ctx context.Context, calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		if call.Name != internalResourceGuideLookupCall {
			continue
		}
		request, ok := resourceGuideLookupFromFunctionCall(call)
		if !ok {
			if !l.appendCorrectionWithCompaction("invalid_resource_guide_lookup", "Resource guide lookup request was invalid. Continue with the next safest kubectl diagnostic.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "resource_guide_lookup 형식 오류가 반복되어 진단을 중단합니다.")
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return true
			}
			l.currIteration++
			l.state = StateRunning
			return true
		}
		if l.resourceClassification == nil || l.resourceClassification.Kind != resourceClassificationCRD {
			if !l.appendCorrectionWithCompaction("resource_guide_without_confirmed_crd", "Resource guide lookup is only available after runtime discovery confirms the primary target is a CRD. Continue with the next safest kubectl diagnostic and do not infer a CRD or Cluster API family from the name alone.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "확인되지 않은 CRD resource_guide_lookup 요청이 반복되어 진단을 중단합니다.")
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return true
			}
			l.currIteration++
			l.state = StateRunning
			return true
		}
		query := l.resourceGuideRefinementQuery(request)
		if l.resourceGuideQueryAlreadyUsed(query) {
			if !l.appendCorrectionWithCompaction("duplicate_resource_guide_lookup", "That refined resource-guide lookup was already performed for the same problem focus and evidence. Do not repeat it; choose the next kubectl diagnostic or answer from the evidence.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "중복 resource_guide_lookup 요청이 반복되어 진단을 중단합니다.")
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return true
			}
			l.currIteration++
			l.state = StateRunning
			return true
		}
		l.searchAndInjectResourceGuide(ctx, request.ResourceFamily, query)
		return true
	}
	return false
}

func (l *Loop) interceptCustomResourceFunctionCalls(ctx context.Context, calls []gollm.FunctionCall) bool {
	if l.resourceGuideInjected || l.initialGuideAttempted {
		return false
	}
	if l.requestContext == nil {
		return false
	}
	for _, call := range calls {
		command, ok := kubectlCommandFromFunctionCall(call)
		if !ok {
			continue
		}
		resource, ok := customResourceCandidateFromKubectl(command)
		if !ok {
			continue
		}
		classification := l.classifyResourceByDiscovery(ctx, resource)
		if classification.Kind != resourceClassificationCRD {
			continue
		}
		l.resourceClassification = &classification
		l.resourceGuideEvidence = append(l.resourceGuideEvidence, command)
		l.initialGuideAttempted = true
		l.searchAndInjectResourceGuide(ctx, resource, l.resourceGuideQuery(resource))
		return true
	}
	return false
}

func (l *Loop) searchAndInjectResourceGuide(ctx context.Context, resource, query string) {
	l.markResourceGuideQuery(query)
	client, err := guidance.NewResourceGuideClient(l.cfg)
	if err != nil {
		klog.Warningf("resource guidance client init failed: %v", err)
		l.injectResourceGuideUnavailable(resource, "client_init_failed")
		return
	}
	if client.KnowledgeProvider() != guidance.KnowledgeProviderQdrant {
		l.injectResourceGuideUnavailable(resource, "provider="+string(client.KnowledgeProvider()))
		return
	}
	searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	found, err := client.SearchGuides(searchCtx, query)
	cancel()
	if err != nil {
		klog.Warningf("resource guidance search failed: %v", err)
		l.injectResourceGuideUnavailable(resource, "search_failed")
		return
	}
	l.injectResourceGuideAttempt(resource, found)
}

func (l *Loop) resourceGuideQueryAlreadyUsed(query string) bool {
	if l.resourceGuideQueries == nil {
		return false
	}
	_, ok := l.resourceGuideQueries[query]
	return ok
}

func (l *Loop) markResourceGuideQuery(query string) {
	if l.resourceGuideQueries == nil {
		l.resourceGuideQueries = make(map[string]struct{})
	}
	l.resourceGuideQueries[query] = struct{}{}
}

func (l *Loop) injectResourceGuideAttempt(resource string, found *guidance.GuideSearchResult) {
	l.resourceGuideInjected = true
	l.guideStepState = l.buildGuideStepState(found)
	l.finalReportRequested = false
	l.pendingResponseDirective = ""
	includeClusterAPI := guideResultImpliesClusterAPI(found)
	l.promptOptions = l.newPromptOptions(l.requestIntent, true, includeClusterAPI)
	if l.shouldCompactForStateRewrite() {
		before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		limit := l.contextLimitTokens()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: injecting CRD guide context for %s; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", resource, before, limit))
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.currChatContent = []any{l.compactedStateMessage("Use the following guide context as decision support, then choose the next safest step.")}
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: CRD guide context injected for %s. estimated context %d/%d tokens.", resource, after, limit))
	} else {
		if err := l.resetChatSessionPreservingCurrentContent(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		klog.Infof("resource guide injected for CRD %s without context compact", resource)
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) injectResourceGuideUnavailable(resource, reason string) {
	l.resourceGuideInjected = true
	l.guideStepState = nil
	l.finalReportRequested = false
	l.pendingResponseDirective = ""
	l.promptOptions = l.newPromptOptions(l.requestIntent, true, false)
	content := formatResourceGuideUnavailableObservation(resource, reason)
	if l.shouldCompactForStateRewrite() {
		before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		limit := l.contextLimitTokens()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: recording unavailable CRD guide context for %s; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", resource, before, limit))
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.currChatContent = []any{l.compactedStateMessage("Resource guide lookup was unavailable; continue with custom-resource-aware diagnostics.")}
		l.appendGuideObservation(guideRef{GuideID: "unavailable:" + resource + ":" + reason, Hash: contextHash(content)}, content)
		after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: unavailable CRD guide context recorded for %s. estimated context %d/%d tokens.", resource, after, limit))
	} else {
		if err := l.resetChatSessionPreservingCurrentContent(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.state = StateDone
			return
		}
		l.appendGuideObservation(guideRef{GuideID: "unavailable:" + resource + ":" + reason, Hash: contextHash(content)}, content)
		klog.Infof("resource guide unavailable for CRD %s (%s); continuing without context compact", resource, reason)
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
}

func (l *Loop) resetChatSessionPreservingCurrentContent() error {
	current := append([]any(nil), l.currChatContent...)
	hashes := l.contextBlockHashes
	if err := l.resetChatSession(); err != nil {
		return err
	}
	l.currChatContent = current
	l.contextBlockHashes = hashes
	return nil
}

func (l *Loop) resourceGuideQuery(resource string) string {
	lines := []string{l.originalQuery, "observed custom resource: " + resource}
	if l.resourceClassification != nil {
		lines = append(lines,
			"resource classification: "+l.resourceClassification.Kind,
			"classification source: "+l.resourceClassification.Source,
		)
		if l.resourceClassification.APIGroup != "" {
			lines = append(lines, "api group: "+l.resourceClassification.APIGroup)
		}
	}
	return strings.Join(append(lines, l.resourceGuideEvidence...), "\n")
}

func (l *Loop) resourceGuideRefinementQuery(request resourceGuideLookup) string {
	return strings.Join([]string{
		l.originalQuery,
		"resource family: " + request.ResourceFamily,
		"problem focus: " + request.ProblemFocus,
		"reason for refinement: " + request.Reason,
		"observed evidence: " + request.Evidence,
	}, "\n")
}

func resourceGuideLookupFromFunctionCall(call gollm.FunctionCall) (resourceGuideLookup, bool) {
	var request resourceGuideLookup
	resourceFamily, familyOK := call.Arguments["resource_family"].(string)
	problemFocus, focusOK := call.Arguments["problem_focus"].(string)
	reason, reasonOK := call.Arguments["reason"].(string)
	evidence, evidenceOK := call.Arguments["evidence"].(string)
	if !familyOK || !focusOK || !reasonOK || !evidenceOK {
		return request, false
	}
	request = resourceGuideLookup{
		ResourceFamily: strings.TrimSpace(resourceFamily),
		ProblemFocus:   strings.TrimSpace(problemFocus),
		Reason:         strings.TrimSpace(reason),
		Evidence:       strings.TrimSpace(evidence),
	}
	if request.ResourceFamily == "" || request.ProblemFocus == "" || request.Reason == "" || request.Evidence == "" {
		return resourceGuideLookup{}, false
	}
	return request, true
}

func formatResourceGuideObservation(resource string, found *guidance.GuideSearchResult) string {
	var b strings.Builder
	b.WriteString("Resource guide lookup was triggered after runtime discovery confirmed this resource as a CRD: ")
	b.WriteString(resource)
	if found == nil || len(found.Cases) == 0 {
		b.WriteString(".\nNo matching resource guide was found. Continue only with custom-resource-aware diagnostics; do not treat this resource as a built-in Kubernetes workload object.")
		return b.String()
	}
	b.WriteString(".\nUse this top guide before continuing diagnosis:\n")
	for i, c := range found.Cases {
		if i >= 1 {
			break
		}
		fmt.Fprintf(&b, "- %s", c.Title)
		if c.ID != "" {
			fmt.Fprintf(&b, " (guide_id: %s)", c.ID)
		}
		b.WriteString("\n")
		if len(c.EvidenceKeywords) > 0 {
			fmt.Fprintf(&b, "  evidence keywords: %s\n", strings.Join(c.EvidenceKeywords, ", "))
		}
		if len(c.DecisionHints) > 0 {
			fmt.Fprintf(&b, "  decision hints: %s\n", strings.Join(c.DecisionHints, " | "))
		}
		if len(c.RelatedObjects) > 0 {
			fmt.Fprintf(&b, "  related objects: %s\n", strings.Join(c.RelatedObjects, ", "))
		}
		if len(c.Tags) > 0 {
			fmt.Fprintf(&b, "  tags: %s\n", strings.Join(c.Tags, ", "))
		}
		if cues := guideMetadataCues(c); len(cues) > 0 {
			fmt.Fprintf(&b, "  metadata cues to inspect: %s\n", strings.Join(cues, ", "))
		}
		if len(c.DiagnosticSteps) > 0 {
			fmt.Fprintf(&b, "  diagnostic steps: %d steps are tracked by the runtime; follow the per-iteration guide_step anchor for the current step detail.\n", len(c.DiagnosticSteps))
		}
		if summary := firstNonEmptyGuideText(c.Resolution, c.Cause); summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", summary)
		}
	}
	if guideResultImpliesClusterAPI(found) {
		b.WriteString("Cluster API guardrails are enabled because the injected guide context indicates a Cluster API CRD family:\n")
		b.WriteString("- Treat an operator-managed `Cluster` custom resource as a management-cluster control-plane object that describes a workload cluster, not as the workload cluster itself.\n")
		b.WriteString("- A management-cluster `kubectl get node` result is not workload-cluster evidence; do not use it to conclude workload-cluster node registration, node health, or providerID presence.\n")
		b.WriteString("- Do not run `kubectl get node` for workload-cluster diagnosis until the current kubeconfig/context is confirmed to be that workload cluster's kubeconfig. If it is not confirmed, continue with guide-supported CRDs, labels, annotations, infrastructure/bootstrap resources, and providerID evidence instead.\n")
		b.WriteString("- Inspect workload-cluster nodes only after obtaining that workload cluster's kubeconfig by the project-approved method, unless the user explicitly asks about management-cluster nodes.\n")
	}
	b.WriteString("Choose the next action by comparing live evidence against the guide keywords and decision hints. If the current evidence does not match a guide, collect the missing evidence instead of assuming the guide is correct.")
	return b.String()
}

func guideMetadataCues(c guidance.GuideCase) []string {
	seen := map[string]struct{}{}
	var cues []string
	addCue := func(value string) {
		value = strings.Trim(value, " ,.;:()[]{}\"'`")
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		cues = append(cues, value)
	}
	fields := []string{
		c.Cause,
		c.Resolution,
		strings.Join(c.Symptoms, " "),
		strings.Join(c.EvidenceKeywords, " "),
		strings.Join(c.DecisionHints, " "),
		strings.Join(c.Tags, " "),
	}
	for _, step := range c.DiagnosticSteps {
		fields = append(fields, step.Description, step.CommandTemplate, step.ExpectedOutcome, strings.Join(step.Preconditions, " "))
	}
	for _, text := range fields {
		for _, token := range strings.FieldsFunc(text, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == ';'
		}) {
			lower := strings.ToLower(token)
			if strings.Contains(lower, "annotation") ||
				strings.Contains(lower, "label") ||
				strings.Contains(lower, "/") ||
				strings.Contains(lower, "=") {
				addCue(token)
			}
		}
	}
	if len(cues) > 12 {
		return cues[:12]
	}
	return cues
}

func guideResultImpliesClusterAPI(found *guidance.GuideSearchResult) bool {
	if found == nil || len(found.Cases) == 0 {
		return false
	}
	return guideCaseImpliesClusterAPI(found.Cases[0])
}

func guideRefFromResult(resource string, found *guidance.GuideSearchResult) guideRef {
	if found == nil || len(found.Cases) == 0 {
		id := "none:" + resource
		return guideRef{GuideID: id, Hash: contextHash(id)}
	}
	c := found.Cases[0]
	id := c.ID
	if id == "" {
		id = c.Title
	}
	content := strings.Join([]string{
		c.ID,
		c.Title,
		strings.Join(c.EvidenceKeywords, ","),
		strings.Join(c.DecisionHints, "|"),
		firstNonEmptyGuideText(c.Resolution, c.Cause),
	}, "\n")
	return guideRef{GuideID: id, Hash: contextHash(content)}
}

func guideCaseImpliesClusterAPI(c guidance.GuideCase) bool {
	var parts []string
	parts = append(parts, c.ID, c.Title, c.Cause, c.Resolution, c.Source)
	parts = append(parts, c.Symptoms...)
	parts = append(parts, c.EvidenceKeywords...)
	parts = append(parts, c.DecisionHints...)
	parts = append(parts, c.RelatedObjects...)
	parts = append(parts, c.Tags...)
	for _, step := range c.DiagnosticSteps {
		parts = append(parts, step.Description, step.CommandTemplate, step.RenderedCommand)
	}
	text := strings.ToLower(strings.Join(parts, "\n"))
	for _, marker := range []string{
		"cluster-api",
		"cluster api",
		"cluster.x-k8s.io",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func formatResourceGuideUnavailableObservation(resource, reason string) string {
	return fmt.Sprintf(
		"Resource guide lookup is required before diagnosing CRD resource %s, but lookup was not executed (%s). Do not claim that no guide matched. Continue only with custom-resource-aware diagnostics and state that resource guidance was unavailable.",
		resource,
		reason,
	)
}

func firstNonEmptyGuideText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// buildGuideStepState extracts the minimal step bookkeeping the loop needs to
// track the model's progress through the guide's diagnostic_steps. Only the
// top guide case is used because the runtime injects one case at a time.
func (l *Loop) buildGuideStepState(found *guidance.GuideSearchResult) *guideStepState {
	if found == nil || len(found.Cases) == 0 {
		return nil
	}
	c := found.Cases[0]
	if len(c.DiagnosticSteps) == 0 {
		return nil
	}
	state := &guideStepState{
		GuideID:    c.ID,
		Title:      c.Title,
		TotalSteps: len(c.DiagnosticSteps),
		Completed:  map[int]bool{},
	}
	for i, step := range c.DiagnosticSteps {
		desc := strings.TrimSpace(step.Description)
		if desc == "" {
			desc = strings.TrimSpace(step.CommandTemplate)
		}
		state.StepDetails = append(state.StepDetails, guideStepDetail{
			Index:           i + 1,
			Description:     desc,
			CommandTemplate: strings.TrimSpace(step.CommandTemplate),
			RenderedCommand: l.renderGuideStepCommand(step),
			ExpectedOutcome: strings.TrimSpace(step.ExpectedOutcome),
			Preconditions:   append([]string(nil), step.Preconditions...),
		})
	}
	l.persistGuideStepDetails(state)
	return state
}

func (l *Loop) renderGuideStepCommand(step guidance.PlanStep) string {
	rendered := strings.TrimSpace(step.RenderedCommand)
	if rendered == "" {
		rendered = strings.TrimSpace(step.CommandTemplate)
	}
	if rendered == "" {
		return ""
	}
	replacements := map[string]string{}
	if l.requestContext != nil {
		replacements["namespace"] = l.requestContext.Scope.Namespace
		replacements["name"] = l.requestContext.PrimaryTarget.Name
		replacements["cluster_name"] = l.requestContext.PrimaryTarget.Name
		replacements["kind"] = l.requestContext.PrimaryTarget.Resource
	}
	for key, value := range step.Variables {
		replacements[key] = value
	}
	for key, value := range replacements {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}
	if strings.Contains(rendered, "{{") {
		return ""
	}
	return rendered
}

func (l *Loop) persistGuideStepDetails(state *guideStepState) {
	if state == nil || l.workDir == "" || len(state.StepDetails) == 0 {
		return
	}
	data, err := json.MarshalIndent(map[string]any{
		"guide_id": state.GuideID,
		"title":    state.Title,
		"steps":    state.StepDetails,
	}, "", "  ")
	if err != nil {
		return
	}
	state.StepHash = contextHash(string(data))
	dir := filepath.Join(l.workDir, "guides")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := strings.TrimPrefix(state.StepHash, "sha256:")
	path := filepath.Join(dir, "guide-steps-"+name+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return
	}
	state.StepFilePath = path
}
