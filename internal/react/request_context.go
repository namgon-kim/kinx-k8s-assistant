package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"k8s.io/klog/v2"
)

func (l *Loop) consumeRequestContext(ctx context.Context, calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name == internalRequirementAnalysisCall {
			if l.requirementAnalysis != nil {
				continue
			}
			analysis, ok := requirementAnalysisFromFunctionCall(call)
			if !ok {
				message := "Requirement analysis was invalid. Return only one corrected requirement_analysis object before choosing any action. Identify request_type, action, target.category, target.description, scope.type, resource_candidates, optional operational_focus, evidence_needs, constraints, and ambiguities. Do not classify broad cluster/environment requests as Kubernetes Cluster resources unless the user names a concrete Cluster object."
				return nil, l.applyModelOutputCorrectionGate("invalid_requirement_analysis", "반복된 requirement analysis 오류로 루프를 중단했습니다.", message)
			}
			analysis = l.applyPriorContextToFollowUpRequirementAnalysis(analysis)
			l.requirementAnalysis = &analysis
			klog.V(0).InfoS("requirement analysis accepted",
				"request_type", analysis.RequestType,
				"action", analysis.Action,
				"target_category", analysis.Target.Category,
				"resources", len(analysis.Resources),
			)
			if message, needsClarification := requirementAnalysisClarificationMessage(analysis); needsClarification {
				klog.V(0).InfoS("requirement analysis needs clarification", "message_len", len(message))
				l.addMessage(api.MessageSourceModel, api.MessageTypeText, message)
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return nil, true
			}
			if err := l.resetChatSessionAfterRequirementAnalysis(); err != nil {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "requirement analysis 후 세션 초기화 실패: "+err.Error())
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return nil, true
			}
			if request, ok := requestContextFromRequirementAnalysis(analysis); ok {
				l.requestContext = &request
				classification := l.classifyResourceByDiscovery(ctx, request.PrimaryTarget.Resource)
				l.resourceClassification = &classification
				klog.V(0).InfoS("request context derived from requirement analysis",
					"primary_resource", request.PrimaryTarget.Resource,
					"primary_name_set", strings.TrimSpace(request.PrimaryTarget.Name) != "",
					"namespace_set", strings.TrimSpace(request.Scope.Namespace) != "",
					"classification", classification.Kind,
				)
			}
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		if call.Name != internalRequestContextCall {
			remaining = append(remaining, call)
			continue
		}
		if l.requestContext != nil {
			continue
		}
		request, ok := requestContextFromFunctionCall(call)
		if !ok {
			message := "Request context was invalid. Return only one corrected request_context object before choosing any action. Namespace is a scope field unless the user explicitly asks about a Namespace object; do not set primary_target.resource=namespace while also setting scope.namespace. Do not use primary_target.resource=unknown; resource_class=unknown is allowed only as a classification hint. If no concrete resource kind is identifiable, return a final answer asking for clarification instead of inventing a kubectl action."
			return nil, l.applyModelOutputCorrectionGate("invalid_request_context", "반복된 request context 오류로 루프를 중단했습니다.", message)
		}
		l.requestContext = &request
		classification := l.classifyResourceByDiscovery(ctx, request.PrimaryTarget.Resource)
		l.resourceClassification = &classification
		klog.V(0).InfoS("request context accepted",
			"primary_resource", request.PrimaryTarget.Resource,
			"primary_name_set", strings.TrimSpace(request.PrimaryTarget.Name) != "",
			"namespace_set", strings.TrimSpace(request.Scope.Namespace) != "",
			"classification", classification.Kind,
		)
	}
	return remaining, false
}

func (l *Loop) applyPriorContextToFollowUpRequirementAnalysis(analysis requirementAnalysis) requirementAnalysis {
	if l.shouldRetryPreviousRequest(analysis) {
		if prior := cloneRequirementAnalysis(l.lastRequirementAnalysis); prior != nil {
			prior.OperationalFocus = retryPreviousOperationalFocus(prior.OperationalFocus)
			prior.Evidence = append([]string{"Recompute the previous answer accurately from live evidence; do not rely on the prior assistant text."}, prior.Evidence...)
			prior.Ambiguities = nil
			return *prior
		}
	}
	if !l.shouldDefaultRequirementAnalysisFromPriorContext(analysis) {
		return analysis
	}
	prior := l.lastRequestContext
	if prior == nil {
		return analysis
	}
	resource := strings.TrimSpace(prior.PrimaryTarget.Resource)
	if resource == "" {
		return analysis
	}
	namespace := strings.TrimSpace(prior.Scope.Namespace)
	analysis.Target.Category = "kubernetes_resource"
	if strings.TrimSpace(analysis.Target.Description) == "" {
		analysis.Target.Description = "follow-up diagnosis using previous request context"
	}
	if strings.TrimSpace(analysis.Target.Name) == "" {
		analysis.Target.Name = prior.PrimaryTarget.Name
	}
	if analysis.Scope.Type == "" || analysis.Scope.Type == "unknown" {
		if namespace != "" {
			analysis.Scope.Type = "namespaced"
		} else {
			analysis.Scope.Type = "cluster_scoped"
		}
	}
	if strings.TrimSpace(analysis.Scope.Namespace) == "" {
		analysis.Scope.Namespace = namespace
	}
	primary := primaryRequirementResource(analysis.Resources)
	if shouldReplacePrimaryWithPreviousContext(primary) {
		if primary.Kind != "" && !strings.EqualFold(primary.Kind, "unknown") {
			analysis = movePrimaryResourceToOperationalFocusHint(analysis, primary)
		}
		analysis.Resources = append([]requirementResource{{
			Kind:      resource,
			Name:      prior.PrimaryTarget.Name,
			Namespace: namespace,
			Role:      "primary",
			Source:    "previous_context",
		}}, analysis.Resources...)
	} else if strings.EqualFold(normalizeKubectlResource(primary.Kind), normalizeKubectlResource(resource)) && primary.Source == "previous_context" {
		analysis.Resources = fillPreviousContextPrimaryResource(analysis.Resources, prior)
	}
	return analysis
}

func (l *Loop) shouldRetryPreviousRequest(analysis requirementAnalysis) bool {
	if l.lastRequestContext == nil {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(l.originalQuery))
	if query == "" {
		return false
	}
	retryText := strings.Contains(query, "다시") ||
		strings.Contains(query, "정확") ||
		strings.Contains(query, "아닌") ||
		strings.Contains(query, "틀렸") ||
		strings.Contains(query, "재계산") ||
		strings.Contains(query, "recalculate") ||
		strings.Contains(query, "again") ||
		strings.Contains(query, "not right") ||
		strings.Contains(query, "incorrect") ||
		strings.Contains(query, "wrong")
	if !retryText {
		return false
	}
	if strings.EqualFold(analysis.Target.Category, "conversation") ||
		strings.EqualFold(analysis.Target.Category, "unknown") ||
		strings.Contains(strings.ToLower(analysis.Action), "clarify") {
		return true
	}
	return len(analysis.Resources) == 0 && strings.TrimSpace(analysis.Scope.Namespace) == ""
}

func retryPreviousOperationalFocus(focus *requirementOperationalFocus) *requirementOperationalFocus {
	var out requirementOperationalFocus
	if focus != nil {
		out = *focus
		out.RelatedResourceHints = append([]requirementRelatedResource(nil), focus.RelatedResourceHints...)
		out.EvidenceNeeds = append([]string(nil), focus.EvidenceNeeds...)
	}
	out.RelationshipToPrimary = "same_primary"
	out.ChangedFromPrevious = false
	out.Reason = "The user rejected the previous answer and asked to recompute it accurately."
	if strings.TrimSpace(out.Summary) == "" {
		out.Summary = "Recompute previous answer accurately"
	}
	out.EvidenceNeeds = append([]string{"Fresh live evidence and deterministic aggregation for the previous request"}, out.EvidenceNeeds...)
	return &out
}

func (l *Loop) shouldDefaultRequirementAnalysisFromPriorContext(analysis requirementAnalysis) bool {
	if l.lastRequestContext == nil || !isDiagnosticRequestType(analysis.RequestType, analysis.Action) {
		return false
	}
	if !operationalFocusUsesPreviousPrimary(analysis.OperationalFocus) {
		return false
	}
	resource := primaryRequirementResource(analysis.Resources)
	if resource.Kind == "" {
		return true
	}
	if strings.EqualFold(resource.Kind, "unknown") {
		return true
	}
	return shouldReplacePrimaryWithPreviousContext(resource) || resource.Source == "previous_context"
}

func operationalFocusUsesPreviousPrimary(focus *requirementOperationalFocus) bool {
	if focus == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(focus.RelationshipToPrimary)) {
	case "same_primary", "related_to_primary":
		return true
	default:
		return false
	}
}

func shouldReplacePrimaryWithPreviousContext(resource requirementResource) bool {
	if resource.Kind == "" || strings.EqualFold(resource.Kind, "unknown") {
		return true
	}
	switch resource.Source {
	case "model_inference":
		return true
	case "":
		return strings.TrimSpace(resource.Name) == ""
	default:
		return false
	}
}

func movePrimaryResourceToOperationalFocusHint(analysis requirementAnalysis, resource requirementResource) requirementAnalysis {
	analysis.Resources = removePrimaryRequirementResource(analysis.Resources)
	if analysis.OperationalFocus == nil {
		analysis.OperationalFocus = &requirementOperationalFocus{}
	}
	hint := requirementRelatedResource{
		Kind:      resource.Kind,
		Name:      resource.Name,
		Namespace: resource.Namespace,
		Role:      "suspected_related",
		Source:    firstNonEmptyString(resource.Source, "model_inference"),
		Evidence:  "Primary candidate was model-inferred for a related operational focus and was kept as a hint.",
	}
	analysis.OperationalFocus.RelatedResourceHints = append([]requirementRelatedResource{hint}, analysis.OperationalFocus.RelatedResourceHints...)
	return analysis
}

func removePrimaryRequirementResource(resources []requirementResource) []requirementResource {
	var out []requirementResource
	removed := false
	for _, resource := range resources {
		if !removed && (resource.Role == "primary" || len(resources) == 1 && resource.Role == "") {
			removed = true
			continue
		}
		out = append(out, resource)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fillPreviousContextPrimaryResource(resources []requirementResource, prior *requestContext) []requirementResource {
	if prior == nil {
		return resources
	}
	out := append([]requirementResource(nil), resources...)
	for i, resource := range out {
		if resource.Role != "primary" {
			continue
		}
		if strings.TrimSpace(resource.Name) == "" {
			out[i].Name = prior.PrimaryTarget.Name
		}
		if strings.TrimSpace(resource.Namespace) == "" {
			out[i].Namespace = prior.Scope.Namespace
		}
		if strings.TrimSpace(resource.Source) == "" {
			out[i].Source = "previous_context"
		}
		break
	}
	return out
}

func requirementAnalysisFromFunctionCall(call gollm.FunctionCall) (requirementAnalysis, bool) {
	var analysis requirementAnalysis
	requestType, _ := call.Arguments["request_type"].(string)
	action, _ := call.Arguments["action"].(string)
	analysis.RequestType = strings.ToLower(strings.TrimSpace(requestType))
	analysis.Action = strings.ToLower(strings.TrimSpace(action))
	if targetRaw, ok := call.Arguments["target"].(map[string]any); ok {
		category, _ := targetRaw["category"].(string)
		name, _ := targetRaw["name"].(string)
		description, _ := targetRaw["description"].(string)
		analysis.Target = requirementAnalysisTarget{
			Category:    strings.ToLower(strings.TrimSpace(category)),
			Name:        cleanUnknownPlaceholder(name),
			Description: strings.TrimSpace(description),
		}
	}
	resourcesRaw, _ := call.Arguments["resource_candidates"].([]any)
	for _, item := range resourcesRaw {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := raw["kind"].(string)
		name, _ := raw["name"].(string)
		namespace, _ := raw["namespace"].(string)
		role, _ := raw["role"].(string)
		source, _ := raw["source"].(string)
		resource := requirementResource{
			Kind:      strings.ToLower(strings.TrimSpace(kind)),
			Name:      cleanUnknownPlaceholder(name),
			Namespace: cleanNamespaceValue(namespace),
			Role:      normalizeRequirementResourceRole(role),
			Source:    normalizeRequirementResourceSource(source),
		}
		if resource.Kind != "" || resource.Name != "" {
			analysis.Resources = append(analysis.Resources, resource)
		}
	}
	if scopeRaw, ok := call.Arguments["scope"].(map[string]any); ok {
		scopeType, _ := scopeRaw["type"].(string)
		namespace, _ := scopeRaw["namespace"].(string)
		analysis.Scope.Type = strings.ToLower(strings.TrimSpace(scopeType))
		if !isAllNamespacesValue(namespace) {
			analysis.Scope.Namespace = cleanNamespaceValue(namespace)
		}
	}
	if focusRaw, ok := call.Arguments["operational_focus"].(map[string]any); ok {
		analysis.OperationalFocus = operationalFocusFromArgument(focusRaw)
	}
	analysis.Evidence = stringSliceFromArgument(call.Arguments["evidence_needs"])
	analysis.Constraints = stringSliceFromArgument(call.Arguments["constraints"])
	analysis.Ambiguities = stringSliceFromArgument(call.Arguments["ambiguities"])
	if analysis.RequestType == "" || analysis.Action == "" || analysis.Target.Category == "" {
		return requirementAnalysis{}, false
	}
	for _, resource := range analysis.Resources {
		if resource.Role != "" && !validRequirementResourceRole(resource.Role) {
			return requirementAnalysis{}, false
		}
	}
	return analysis, true
}

func requirementAnalysisClarificationMessage(analysis requirementAnalysis) (string, bool) {
	if !isDiagnosticRequestType(analysis.RequestType, analysis.Action) {
		return "", false
	}
	if analysis.Target.Category == "unknown" {
		return "진단 대상을 특정할 수 없습니다. 확인할 리소스 종류, 이름, namespace 또는 클러스터 정보를 알려주세요.", true
	}
	resource := primaryRequirementResource(analysis.Resources)
	if resource.Kind == "" {
		switch analysis.Target.Category {
		case "cluster_environment", "namespace_scope", "logs", "events", "metrics":
			return "", false
		default:
			return "어떤 대상을 진단해야 하는지 정보가 부족합니다. 리소스 종류와 이름, namespace 또는 관련 클러스터 이름을 알려주세요.", true
		}
	}
	return "", false
}

func isDiagnosticRequestType(requestType, action string) bool {
	requestType = strings.ToLower(strings.TrimSpace(requestType))
	action = strings.ToLower(strings.TrimSpace(action))
	switch requestType {
	case "diagnosis", "remediation":
		return true
	}
	return strings.Contains(action, "diagnose") || strings.Contains(action, "remediate") || strings.Contains(action, "repair")
}

func normalizeRequirementResourceRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "target", "subject":
		return "primary"
	case "context":
		return "scope"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func validRequirementResourceRole(role string) bool {
	switch role {
	case "primary", "scope", "related", "evidence", "owner", "dependent":
		return true
	default:
		return false
	}
}

func normalizeRequirementResourceSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "user_request", "previous_context", "live_evidence", "guide_context", "model_inference":
		return strings.ToLower(strings.TrimSpace(source))
	default:
		return ""
	}
}

func operationalFocusFromArgument(raw map[string]any) *requirementOperationalFocus {
	summary, _ := raw["summary"].(string)
	relationship, _ := raw["relationship_to_primary"].(string)
	changed, _ := raw["changed_from_previous"].(bool)
	reason, _ := raw["reason"].(string)
	focus := &requirementOperationalFocus{
		Summary:               strings.TrimSpace(summary),
		RelationshipToPrimary: normalizeOperationalFocusRelationship(relationship),
		ChangedFromPrevious:   changed,
		Reason:                strings.TrimSpace(reason),
		EvidenceNeeds:         stringSliceFromArgument(raw["evidence_needs"]),
	}
	hintsRaw, _ := raw["related_resource_hints"].([]any)
	for _, item := range hintsRaw {
		hintRaw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := hintRaw["kind"].(string)
		name, _ := hintRaw["name"].(string)
		namespace, _ := hintRaw["namespace"].(string)
		role, _ := hintRaw["role"].(string)
		source, _ := hintRaw["source"].(string)
		evidence, _ := hintRaw["evidence"].(string)
		hint := requirementRelatedResource{
			Kind:      strings.ToLower(strings.TrimSpace(kind)),
			Name:      cleanUnknownPlaceholder(name),
			Namespace: cleanUnknownPlaceholder(namespace),
			Role:      strings.ToLower(strings.TrimSpace(role)),
			Source:    normalizeRequirementResourceSource(source),
			Evidence:  strings.TrimSpace(evidence),
		}
		if hint.Kind != "" || hint.Name != "" || hint.Evidence != "" {
			focus.RelatedResourceHints = append(focus.RelatedResourceHints, hint)
		}
	}
	if focus.Summary == "" &&
		focus.RelationshipToPrimary == "" &&
		!focus.ChangedFromPrevious &&
		focus.Reason == "" &&
		len(focus.RelatedResourceHints) == 0 &&
		len(focus.EvidenceNeeds) == 0 {
		return nil
	}
	return focus
}

func normalizeOperationalFocusRelationship(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "same_primary", "related_to_primary", "new_primary", "unclear":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func requestContextFromRequirementAnalysis(analysis requirementAnalysis) (requestContext, bool) {
	resource := primaryRequirementResource(analysis.Resources)
	resourceKind := normalizeKubectlResource(strings.ToLower(strings.TrimSpace(resource.Kind)))
	if resourceKind == "" || resourceKind == "unknown" {
		return requestContext{}, false
	}
	namespace := strings.TrimSpace(resource.Namespace)
	if namespace == "" {
		namespace = strings.TrimSpace(analysis.Scope.Namespace)
	}
	if isAllNamespacesValue(namespace) || analysis.Scope.Type == "all_namespaces" {
		namespace = ""
	}
	if resourceKind == "namespace" && namespace != "" {
		return requestContext{}, false
	}
	return requestContext{
		PrimaryTarget: requestPrimaryTarget{
			Resource: resourceKind,
			Name:     resource.Name,
		},
		Scope:         requestScope{Namespace: namespace},
		ResourceClass: "unknown",
	}, true
}

func primaryRequirementResource(resources []requirementResource) requirementResource {
	for _, resource := range resources {
		if resource.Role == "primary" {
			return resource
		}
	}
	if len(resources) == 1 && resources[0].Role == "" {
		return resources[0]
	}
	return requirementResource{}
}

func stringSliceFromArgument(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func (l *Loop) requireRequirementAnalysisBeforeAction(calls []gollm.FunctionCall) bool {
	if l.requirementAnalysis != nil {
		return false
	}
	for _, call := range calls {
		if call.Name == internalRequirementAnalysisCall {
			return false
		}
	}
	message := "The first response for this user request must be only a requirement_analysis object. Identify request_type, action, target.category, target.description, scope.type, resource_candidates, optional operational_focus, evidence_needs, constraints, and ambiguities before selecting any tool, resource guide lookup, or final answer. Do not turn a broad cluster/environment target into a Kubernetes Cluster resource unless the user names a concrete Cluster object."
	return l.applyModelOutputCorrectionGate("missing_requirement_analysis", "반복된 requirement analysis 누락으로 루프를 중단했습니다.", message)
}

func requestContextFromFunctionCall(call gollm.FunctionCall) (requestContext, bool) {
	var request requestContext
	primaryRaw, ok := call.Arguments["primary_target"].(map[string]any)
	if !ok {
		return request, false
	}
	resource, _ := primaryRaw["resource"].(string)
	name, _ := primaryRaw["name"].(string)
	resourceClass, _ := call.Arguments["resource_class"].(string)
	resource = strings.ToLower(strings.TrimSpace(resource))
	request.PrimaryTarget = requestPrimaryTarget{
		Resource: resource,
		Name:     cleanUnknownPlaceholder(name),
	}
	if scopeRaw, ok := call.Arguments["scope"].(map[string]any); ok {
		namespace, _ := scopeRaw["namespace"].(string)
		if !isAllNamespacesValue(namespace) {
			request.Scope.Namespace = cleanNamespaceValue(namespace)
		}
	}
	switch strings.ToLower(strings.TrimSpace(resourceClass)) {
	case "built_in", "custom_resource", "unknown":
		request.ResourceClass = strings.ToLower(strings.TrimSpace(resourceClass))
	default:
		return requestContext{}, false
	}
	if request.PrimaryTarget.Resource == "" {
		return requestContext{}, false
	}
	if normalizeKubectlResource(request.PrimaryTarget.Resource) == "unknown" {
		return requestContext{}, false
	}
	if normalizeKubectlResource(request.PrimaryTarget.Resource) == "namespace" && request.Scope.Namespace != "" {
		return requestContext{}, false
	}
	return request, true
}

func (l *Loop) resetChatSessionAfterRequirementAnalysis() error {
	if err := l.resetChatSession(); err != nil {
		return err
	}
	l.currChatContent = []any{
		l.requirementAnalysisContextMessage(),
		l.originalQuery,
	}
	l.contextBlockHashes = nil
	return nil
}

func (l *Loop) requirementAnalysisContextMessage() string {
	var raw []byte
	if l.requirementAnalysis != nil {
		raw, _ = json.Marshal(l.requirementAnalysis)
	}
	return fmt.Sprintf("Accepted requirement_analysis:\n%s\nUse this analysis as the request classification. If resource_candidates is empty, do not invent a Kubernetes resource target. If operational_focus is present, treat it as the current diagnostic angle, not as an automatic RAG request. Your next response must be only a `phase_plan` object that defines ordered `phase_steps`, each with goal, completion_condition, and allowed_next. Do not choose an action, resource_guide_lookup, final_report, or answer until the phase_plan is accepted.", string(raw))
}

// guideStepAnchor returns a compact reaffirmation of the active resource
// guide so the model keeps following it across many iterations of tool
// observations. The full guide body is injected only once via
// appendGuideObservation; this anchor re-emits just enough state for the
// model to know which step it is on and what is left to do.
func (l *Loop) guideStepAnchor() string {
	if l.phaseStepState != nil && !strings.EqualFold(l.phaseStepState.currentStep().Name, "guided_diagnosis") {
		return ""
	}
	state := l.guideStepState
	if state == nil || state.TotalSteps == 0 {
		return ""
	}
	completed := 0
	for i := 1; i <= state.TotalSteps; i++ {
		if state.Completed[i] {
			completed++
		}
	}
	var b strings.Builder
	b.WriteString("Active resource-guide progress. Continue following this guide unless final_report has already been emitted.\n")
	if state.GuideID != "" {
		fmt.Fprintf(&b, "guide_id: %s\n", state.GuideID)
	}
	if state.Title != "" {
		fmt.Fprintf(&b, "guide_title: %s\n", state.Title)
	}
	fmt.Fprintf(&b, "steps_completed: %d / %d\n", completed, state.TotalSteps)
	if skipped := state.skippedSteps(); len(skipped) > 0 {
		fmt.Fprintf(&b, "steps_skipped: %s\n", formatStepIndices(skipped))
	}
	if state.StepFilePath != "" {
		fmt.Fprintf(&b, "step_store: %s\n", state.StepFilePath)
	}
	if state.StepHash != "" {
		fmt.Fprintf(&b, "step_store_hash: %s\n", state.StepHash)
	}
	if remaining := state.remainingSteps(); len(remaining) > 0 {
		fmt.Fprintf(&b, "remaining_step_indices: %s\n", formatStepIndices(remaining))
		next := state.stepDetail(remaining[0])
		fmt.Fprintf(&b, "next_step_index: %d\n", remaining[0])
		if next.Description != "" {
			fmt.Fprintf(&b, "next_step_description: %s\n", next.Description)
		}
		if next.CommandTemplate != "" {
			fmt.Fprintf(&b, "next_step_command_template: %s\n", next.CommandTemplate)
		}
		if next.RenderedCommand != "" {
			fmt.Fprintf(&b, "next_step_rendered_command: %s\n", next.RenderedCommand)
		}
		if next.ExpectedOutcome != "" {
			fmt.Fprintf(&b, "next_step_expected_outcome: %s\n", next.ExpectedOutcome)
		}
		if len(next.Preconditions) > 0 {
			fmt.Fprintf(&b, "next_step_preconditions: %s\n", strings.Join(next.Preconditions, " | "))
		}
	}
	b.WriteString("Rules:\n")
	b.WriteString("- For each action, set `action.guide_progress.step_completed` to the 1-based step index this action advances, and `action.guide_progress.evidence_useful` to whether the observation moved diagnosis forward.\n")
	b.WriteString("- Follow the next_step unless live evidence makes it redundant; if skipping, explain why and mark only the step that was actually advanced.\n")
	b.WriteString("- The full diagnostic step list is stored in step_store for runtime bookkeeping; do not invent step indices outside remaining_step_indices.\n")
	b.WriteString("- When every step is completed (or further steps are clearly redundant for the live evidence), emit `final_report` instead of another `action`.\n")
	return b.String()
}

func (g *guideStepState) stepDetail(index int) guideStepDetail {
	if g == nil || index <= 0 || index > len(g.StepDetails) {
		return guideStepDetail{}
	}
	return g.StepDetails[index-1]
}

func formatStepIndices(indices []int) string {
	var parts []string
	for _, index := range indices {
		parts = append(parts, fmt.Sprintf("%d", index))
	}
	return strings.Join(parts, ",")
}

// requirementAnalysisAnchor returns a short authoritative restatement of the
// accepted requirement_analysis. It is re-emitted at the start of every
// runIteration send so the model keeps serving the originally determined
// request instead of drifting toward whatever the most recent observation
// happens to highlight. The anchor intentionally repeats original_query and
// the analysis JSON because (a) chat history attention decays over many
// iterations and (b) tool observations otherwise dominate the recent context.
func (l *Loop) requirementAnalysisAnchor() string {
	if l.requirementAnalysis == nil {
		return ""
	}
	raw, err := json.Marshal(l.requirementAnalysis)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Active request anchor. Treat this as the authoritative classification of the user request you are still serving; reaffirm it on every iteration.\n")
	fmt.Fprintf(&b, "original_query: %s\n", l.originalQuery)
	fmt.Fprintf(&b, "requirement_analysis: %s\n", string(raw))
	if l.requestContext != nil {
		if ctx, err := json.Marshal(l.requestContext); err == nil {
			fmt.Fprintf(&b, "request_context: %s\n", string(ctx))
		}
	}
	b.WriteString("Alignment rules:\n")
	b.WriteString("- The next action must advance this analysis. Keep target.category, resource_candidates (and the derived primary_target when present) stable.\n")
	b.WriteString("- Do not silently switch the diagnosis subject because a status string mentions another resource kind. If live evidence implies a different operational focus on the same target family, use `resource_guide_lookup` instead of pivoting the target.\n")
	b.WriteString("- Before emitting `action`, mentally check whether it serves this anchor. If it does not, choose a different action or return a final answer that addresses the original_query.")
	return b.String()
}
