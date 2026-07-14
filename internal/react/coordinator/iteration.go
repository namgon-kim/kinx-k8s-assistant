package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/guidance"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	directionflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/direction"
	gateflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/gate"
	guidanceflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/guidance"
	phaseflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/phase"
	reportflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/report"
	verificationflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/verification"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/kube"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
	"k8s.io/klog/v2"
)

// runIteration owns one model turn. The detailed gates remain private helpers
// so iteration orchestration has a single entry point.
func (l *Loop) runIteration(ctx context.Context) error {
	return l.executeIteration(ctx)
}

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
			l.mutableSession().Context.Requirement = &analysis
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
				l.transitionControl(RuntimeControlAwaitingUserQuery)
				return nil, true
			}
			if err := l.resetChatSessionAfterRequirementAnalysis(); err != nil {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "requirement analysis 후 세션 초기화 실패: "+err.Error())
				l.pendingCalls = nil
				l.currIteration = 0
				l.transitionControl(RuntimeControlAwaitingUserQuery)
				return nil, true
			}
			if request, ok := requestContextFromRequirementAnalysis(analysis); ok {
				l.requestContext = &request
				l.mutableSession().Context.Request = &request
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
			l.transitionControl(RuntimeControlAwaitingPhasePlan)
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
		l.mutableSession().Context.Request = &request
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
// appendGuideObservation; this anchor re-emits just enough lifecycle for the
// model to know which step it is on and what is left to do.
func (l *Loop) guideStepAnchor() string {
	if l.phaseStepState != nil && !strings.EqualFold(l.phaseStepState.currentStep().Name, "guided_diagnosis") {
		return ""
	}
	guide := l.guideStepState
	if guide == nil || guide.TotalSteps == 0 {
		return ""
	}
	completed := 0
	for i := 1; i <= guide.TotalSteps; i++ {
		if guide.Completed[i] {
			completed++
		}
	}
	var b strings.Builder
	b.WriteString("Active resource-guide progress. Continue following this guide unless final_report has already been emitted.\n")
	if guide.GuideID != "" {
		fmt.Fprintf(&b, "guide_id: %s\n", guide.GuideID)
	}
	if guide.Title != "" {
		fmt.Fprintf(&b, "guide_title: %s\n", guide.Title)
	}
	fmt.Fprintf(&b, "steps_completed: %d / %d\n", completed, guide.TotalSteps)
	if skipped := guide.skippedSteps(); len(skipped) > 0 {
		fmt.Fprintf(&b, "steps_skipped: %s\n", formatStepIndices(skipped))
	}
	if guide.StepFilePath != "" {
		fmt.Fprintf(&b, "step_store: %s\n", guide.StepFilePath)
	}
	if guide.StepHash != "" {
		fmt.Fprintf(&b, "step_store_hash: %s\n", guide.StepHash)
	}
	if remaining := guide.remainingSteps(); len(remaining) > 0 {
		fmt.Fprintf(&b, "remaining_step_indices: %s\n", formatStepIndices(remaining))
		next := guide.stepDetail(remaining[0])
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

type phaseStepState struct {
	RequestGoal       string
	CurrentPhaseIndex int
	PhaseSteps        []phaseStep
	Completed         map[int]bool
}

const lightweightLookupPhase = "lightweight_lookup"

type phasePlanValidationResult struct {
	Valid   bool
	Code    string
	Message string
}

func (l *Loop) consumePhasePlan(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	var accepted *phasePlan
	for _, call := range calls {
		if call.Name != internalPhasePlanCall {
			remaining = append(remaining, call)
			continue
		}
		if l.phaseStepState != nil {
			continue
		}
		plan, ok := phasePlanFromFunctionCall(call)
		if !ok {
			message := "Phase plan was invalid. Return only one corrected phase_plan object before choosing any action. Include request_goal, current_phase_index, and ordered phase_steps with index, name, goal, completion_condition, and allowed_next. Every allowed_next value must name a declared phase_steps[].name; do not reference implicit phases such as final_report or guided_diagnosis unless they are listed as phase_steps."
			return nil, l.applyModelOutputCorrectionGate("invalid_phase_plan", "반복된 phase plan 오류로 루프를 중단했습니다.", message)
		}
		if result := l.validatePhasePlanForRequest(plan); !result.Valid {
			if l.applyGateOutcome(result.gateOutcome()) {
				return nil, true
			}
		}
		accepted = &plan
		l.phaseStepState = newPhaseStepState(plan)
		l.mutableSession().Phase.Plan = &plan
		l.session.Phase.Current = reactPhaseRef(l.phaseStepState.activePhaseRef())
		klog.V(1).InfoS("phase plan accepted", "request_goal", trimForLog(plan.RequestGoal, 160), "phase_steps", len(plan.PhaseSteps), "current_phase_index", plan.CurrentPhaseIndex)
		continue
	}
	if accepted != nil {
		if len(remaining) == 0 {
			l.currIteration++
			l.transitionAfterPhaseAdvance()
			return nil, true
		}
		if !phasePlanAllowsTrailingCalls(*accepted, remaining) {
			message := "Phase plan was accepted, but the same response also included additional structured output. Return only the phase_plan object first, except for a single-step lightweight_lookup phase where exactly one action may be included with the phase_plan."
			l.phaseStepState = nil
			l.mutableSession().Phase.Reset()
			l.transitionControl(RuntimeControlAwaitingPhasePlan)
			return nil, l.applyModelOutputCorrectionGate("phase_plan_unexpected_trailing_calls", "반복된 phase plan 동시 출력 오류로 루프를 중단했습니다.", message)
		}
		// The lightweight_lookup exception can retain one action after its plan.
		// It still needs the same explicit phase-derived control transition as a
		// plan-only response before that action reaches the dispatcher.
		l.transitionAfterPhaseAdvance()
	}
	return remaining, false
}

func (r phasePlanValidationResult) gateOutcome() GateOutcome {
	if r.Valid {
		return GateOutcome{Allow: true}
	}
	code := strings.TrimSpace(r.Code)
	if code == "" {
		code = "phase_plan_request_contract"
	}
	message := strings.TrimSpace(r.Message)
	if message == "" {
		message = "The phase_plan violates the runtime request contract. Return one corrected phase_plan object before choosing an action."
	}
	return GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            code,
		UserMessage:     "phase plan이 현재 요청의 runtime contract와 맞지 않아 차단했습니다.",
		ModelCorrection: message,
		Retryable:       true,
		RetryScope:      RetryScopeCurrentPhase,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    BranchStayCurrent,
	}
}

func (l *Loop) validatePhasePlanForRequest(plan phasePlan) phasePlanValidationResult {
	if !phasePlanValid(plan) {
		return phasePlanValidationResult{
			Code:    "invalid_phase_plan",
			Message: "Phase plan was invalid. Return only one corrected phase_plan object before choosing any action. Include request_goal, current_phase_index, and ordered phase_steps with index, name, goal, completion_condition, and allowed_next.",
		}
	}
	if l.phasePlanRequiresMutationVerification(plan) && !phasePlanHasVerificationPhase(plan) {
		return phasePlanValidationResult{
			Code:    "phase_plan_missing_mutation_verification",
			Message: "The accepted request can change Kubernetes resources or the proposed plan contains a mutation/remediation execution phase. Return one corrected phase_plan that includes an explicit mutation_verification or verification_observation phase after mutation execution and before response_synthesis/final_report. Approval alone is not completion evidence.",
		}
	}
	if phasePlanHasGuidedDiagnosis(plan) && !phasePlanHasPhase(plan, "guidance_lookup") {
		return phasePlanValidationResult{
			Code:    "phase_plan_guided_diagnosis_without_lookup",
			Message: "guided_diagnosis is valid only after a declared guidance_lookup phase obtains or records resource-guide context. Return one corrected phase_plan that declares guidance_lookup before guided_diagnosis, or remove guided_diagnosis.",
		}
	}
	if phasePlanHasResourceGuidePhase(plan) && !l.phasePlanAllowsResourceGuidance() {
		return phasePlanValidationResult{
			Code:    "phase_plan_guidance_without_crd",
			Message: "resource guide phases are allowed only after runtime discovery confirms the accepted primary target is a CRD. For built-in, unknown, or unresolved targets, return one corrected phase_plan without guidance_lookup or guided_diagnosis and continue with ordinary kubectl diagnostics.",
		}
	}
	return phasePlanValidationResult{Valid: true}
}

func (l *Loop) consumePhaseProgress(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	for _, call := range calls {
		if call.Name == internalPhaseProgressCall && !onlyFunctionCall(calls, internalPhaseProgressCall) {
			message := "phase_progress must be the only structured output in a response. Do not combine phase_progress with an action, guide_progress, final_report, or another internal call. Return phase_progress first; wait for the runtime to accept the phase transition before choosing the next action."
			return nil, l.applyModelOutputCorrectionGate("phase_progress_mixed_output", "phase_progress와 다른 call을 함께 반환해 현재 phase를 전진시키지 않았습니다.", message)
		}
	}
	var remaining []gollm.FunctionCall
	handled := false
	for _, call := range calls {
		if call.Name != internalPhaseProgressCall {
			remaining = append(remaining, call)
			continue
		}
		progress, ok := phaseProgressFromFunctionCall(call)
		if ok && l.phaseProgressBlockedByGuidanceLookup(progress) {
			message := "Active phase is guidance_lookup for a CRD-backed primary target, but no resource-guide lookup result has been injected or recorded as unavailable. Return one top-level `resource_guide_lookup` object for the accepted primary resource and operational problem focus. Do not complete guidance_lookup with phase_progress, do not enter guided_diagnosis, and do not choose a kubectl action until the guide lookup result is observed."
			return nil, l.applyModelOutputCorrectionGate("guidance_lookup_missing_resource_guide_lookup", "resource_guide_lookup 없이 guidance_lookup 완료가 반복되어 진단을 중단합니다.", message)
		}
		if ok && l.phaseProgressBlockedByIncompleteGuide(progress) {
			message := "The active guided_diagnosis phase still has incomplete resource-guide steps. Continue with exactly one remaining guide step or return guide_progress only after useful live evidence completes that step. Do not emit phase_progress until all nested guide steps are complete."
			return nil, l.applyModelOutputCorrectionGate("guided_diagnosis_incomplete", "남은 guide step이 있어 guided_diagnosis phase를 완료하지 않았습니다.", message)
		}
		if !ok || l.phaseStepState == nil || !l.phaseStepState.acceptProgress(progress) {
			message := "Phase progress was invalid. Return one corrected phase_progress object for the active phase, or continue the active phase with one valid action. Do not use guide_progress for top-level phase completion."
			return nil, l.applyModelOutputCorrectionGate("invalid_phase_progress", "반복된 phase progress 오류로 루프를 중단했습니다.", message)
		}
		if l.guideStepState != nil && strings.EqualFold(l.phaseStepState.phaseName(progress.PhaseCompleted), "guided_diagnosis") {
			l.guideStepState = nil
		}
		klog.V(0).InfoS("phase progress accepted", "phase_completed", progress.PhaseCompleted, "current_phase", l.phaseStepState.CurrentPhaseIndex)
		handled = true
	}
	if handled && len(remaining) == 0 {
		l.currIteration++
		l.transitionAfterPhaseAdvance()
		return nil, true
	}
	return remaining, false
}

// transitionAfterPhaseAdvance selects the next runtime obligation at the one
// place where phase progress is allowed to alter control flow. It does not
// derive control for snapshots; it records the explicit transition caused by
// an accepted phase plan or phase_progress event.
func (l *Loop) transitionAfterPhaseAdvance() {
	if l.phaseStepState != nil {
		state := l.mutableSession()
		state.Phase.Current = reactPhaseRef(l.phaseStepState.activePhaseRef())
		state.Phase.Completed = make(map[int]bool, len(l.phaseStepState.Completed))
		for index, completed := range l.phaseStepState.Completed {
			state.Phase.Completed[index] = completed
		}
	}
	if l.phaseStepRequiresResourceGuideLookup() {
		l.transitionControl(RuntimeControlAwaitingResourceGuideLookup)
		return
	}
	if l.guideStepState != nil && len(l.guideStepState.remainingSteps()) > 0 {
		l.transitionControl(RuntimeControlAwaitingGuidedDiagnosisStep)
		return
	}
	l.transitionControl(RuntimeControlAwaitingModelStep)
}

func (l *Loop) phaseProgressBlockedByGuidanceLookup(progress phaseProgress) bool {
	if l.phaseStepState == nil {
		return false
	}
	current := l.phaseStepState.currentStep()
	if !strings.EqualFold(strings.TrimSpace(current.Name), "guidance_lookup") {
		return false
	}
	if progress.PhaseCompleted != current.Index {
		return false
	}
	if l.resourceGuideInjected {
		return false
	}
	if l.resourceClassification == nil || l.resourceClassification.Kind != resourceClassificationCRD {
		return false
	}
	return true
}

func (l *Loop) phaseProgressBlockedByIncompleteGuide(progress phaseProgress) bool {
	if l.phaseStepState == nil || l.guideStepState == nil || l.guideStepState.allCompleted() {
		return false
	}
	current := l.phaseStepState.currentStep()
	return strings.EqualFold(strings.TrimSpace(current.Name), "guided_diagnosis") && progress.PhaseCompleted == current.Index
}

func (l *Loop) requirePhasePlanBeforeAction(calls []gollm.FunctionCall) bool {
	if l.requirementAnalysis == nil || l.phaseStepState != nil {
		return false
	}
	for _, call := range calls {
		switch call.Name {
		case internalPhasePlanCall, internalRequirementAnalysisCall, internalRequestContextCall:
			return false
		}
	}
	message := "After requirement_analysis is accepted, return only one phase_plan object before choosing any action, resource_guide_lookup, final_report, or answer. The plan must define ordered phase_steps, each with a goal, completion_condition, and allowed_next values that reference only declared phase_steps[].name entries."
	return l.applyModelOutputCorrectionGate("missing_phase_plan", "반복된 phase plan 누락으로 루프를 중단했습니다.", message)
}

func (l *Loop) rejectInvalidShimStructuredCalls(calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		var code, message string
		switch call.Name {
		case internalInvalidActionCall:
			code = "invalid_action"
			message = "Action payload was invalid. Re-emit one corrected response with `action` as an object containing name, reason, target, command, and modifies_resource. Do not encode action as a string."
		case internalInvalidStructuredOutputCall:
			code = "mixed_answer_structured_output"
			message = "The previous response included `answer` together with another structured output. Return exactly one structured output. If a tool, phase_plan, phase_progress, resource_guide_lookup, final_report, or next_directions is needed, omit `answer`; if you are answering the user, omit all other structured fields."
		default:
			continue
		}
		return l.applyModelOutputCorrectionGate(code, "반복된 shim structured output 오류로 루프를 중단했습니다.", message)
	}
	return false
}

func phasePlanFromFunctionCall(call gollm.FunctionCall) (phasePlan, bool) {
	var plan phasePlan
	requestGoal, _ := call.Arguments["request_goal"].(string)
	requestGoal = strings.TrimSpace(requestGoal)
	if requestGoal == "" {
		return phasePlan{}, false
	}
	steps, ok := phaseStepsFromValue(call.Arguments["phase_steps"])
	if !ok || len(steps) == 0 {
		steps, ok = phaseStepsFromValue(call.Arguments["phases"])
		if !ok || len(steps) == 0 {
			return phasePlan{}, false
		}
	}
	current := intFromAny(call.Arguments["current_phase_index"])
	if current == 0 {
		current = steps[0].Index
	}
	plan = phasePlan{
		RequestGoal:       requestGoal,
		CurrentPhaseIndex: current,
		PhaseSteps:        steps,
	}
	return plan, phasePlanValid(plan)
}

func phasePlanAllowsTrailingCalls(plan phasePlan, calls []gollm.FunctionCall) bool {
	if len(calls) != 1 || !singleLightweightPhase(plan) {
		return false
	}
	return !isInternalReactCall(calls[0].Name)
}

func singleLightweightPhase(plan phasePlan) bool {
	if len(plan.PhaseSteps) != 1 {
		return false
	}
	step := plan.PhaseSteps[0]
	return strings.EqualFold(strings.TrimSpace(step.Name), lightweightLookupPhase)
}

func (l *Loop) phasePlanRequiresMutationVerification(plan phasePlan) bool {
	if l != nil && l.cfg != nil && l.cfg.ReadOnly {
		return false
	}
	if l != nil && l.requirementAnalysis != nil {
		requestType := strings.ToLower(strings.TrimSpace(l.requirementAnalysis.RequestType))
		action := strings.ToLower(strings.TrimSpace(l.requirementAnalysis.Action))
		if requestType == "mutation" || actionRequiresMutationVerification(action) {
			return true
		}
	}
	return phasePlanHasMutationExecutionPhase(plan)
}

func actionRequiresMutationVerification(action string) bool {
	if action == "" || strings.Contains(action, "manifest") {
		return false
	}
	tokens := []string{
		"create", "update", "patch", "delete", "scale", "restart", "apply",
		"replace", "annotate", "label", "cordon", "uncordon", "drain", "taint",
	}
	for _, token := range tokens {
		if action == token || strings.HasPrefix(action, token+"_") || strings.Contains(action, "_"+token+"_") || strings.HasSuffix(action, "_"+token) {
			return true
		}
	}
	return false
}

func phasePlanHasMutationExecutionPhase(plan phasePlan) bool {
	for _, step := range plan.PhaseSteps {
		name := normalizedPhaseName(step.Name)
		switch name {
		case "mutation_execution", "mutation_planning", "remediation_execution", "change_execution", "fix_execution", "apply_change", "execute_change":
			return true
		}
		if strings.Contains(name, "mutation") && !strings.Contains(name, "verification") {
			return true
		}
	}
	return false
}

func phasePlanHasVerificationPhase(plan phasePlan) bool {
	for _, step := range plan.PhaseSteps {
		name := normalizedPhaseName(step.Name)
		switch name {
		case "mutation_verification", "verification_observation", "post_mutation_verification":
			return true
		}
		if strings.Contains(name, "verification") || strings.Contains(name, "verify") {
			return true
		}
	}
	return false
}

func phasePlanHasResourceGuidePhase(plan phasePlan) bool {
	return phasePlanHasPhase(plan, "guidance_lookup") || phasePlanHasGuidedDiagnosis(plan)
}

func phasePlanHasGuidedDiagnosis(plan phasePlan) bool {
	return phasePlanHasPhase(plan, "guided_diagnosis")
}

func phasePlanHasPhase(plan phasePlan, name string) bool {
	return phaseflow.HasPhase(plan, name)
}

func normalizedPhaseName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (l *Loop) phasePlanAllowsResourceGuidance() bool {
	return l != nil &&
		l.resourceClassification != nil &&
		l.resourceClassification.Kind == resourceClassificationCRD
}

func isInternalReactCall(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "__")
}

func phaseStepsFromValue(value any) ([]phaseStep, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	steps := make([]phaseStep, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		step := phaseStep{
			Index:               intFromAny(m["index"]),
			Name:                stringFromAny(m["name"]),
			Goal:                stringFromAny(m["goal"]),
			CompletionCondition: stringFromAny(m["completion_condition"]),
			AllowedNext:         stringSliceFromAny(m["allowed_next"]),
		}
		if stepsValue, exists := m["steps"]; exists {
			explicitSteps, ok := phaseExecutionStepsFromValue(stepsValue)
			if !ok {
				return nil, false
			}
			step.Steps = explicitSteps
		}
		if step.Index == 0 || step.Name == "" || step.Goal == "" || step.CompletionCondition == "" {
			return nil, false
		}
		steps = append(steps, step)
	}
	return steps, true
}

func phaseExecutionStepsFromValue(value any) ([]phaseExecutionStep, bool) {
	if value == nil {
		return nil, true
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	steps := make([]phaseExecutionStep, 0, len(raw))
	seenID := map[string]struct{}{}
	seenIndex := map[int]struct{}{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		step := phaseExecutionStep{
			ID:              strings.TrimSpace(stringFromAny(m["id"])),
			Index:           intFromAny(m["index"]),
			Kind:            strings.TrimSpace(stringFromAny(m["kind"])),
			Description:     strings.TrimSpace(stringFromAny(m["description"])),
			Command:         strings.TrimSpace(stringFromAny(m["command"])),
			ExpectedOutcome: strings.TrimSpace(stringFromAny(m["expected_outcome"])),
		}
		if !phaseExecutionStepValid(step) {
			return nil, false
		}
		if id := strings.ToLower(strings.TrimSpace(step.ID)); id != "" {
			if _, ok := seenID[id]; ok {
				return nil, false
			}
			seenID[id] = struct{}{}
		}
		if step.Index > 0 {
			if _, ok := seenIndex[step.Index]; ok {
				return nil, false
			}
			seenIndex[step.Index] = struct{}{}
		}
		steps = append(steps, step)
	}
	return steps, true
}

func phaseExecutionStepValid(step phaseExecutionStep) bool {
	return phaseflow.ExecutionStepValid(step)
}

func phaseExecutionStepsValid(steps []phaseExecutionStep) bool {
	return phaseflow.ExecutionStepsValid(steps)
}

func phasePlanValid(plan phasePlan) bool {
	return phaseflow.Validate(plan)
}

func phaseProgressFromFunctionCall(call gollm.FunctionCall) (phaseProgress, bool) {
	progress := phaseProgress{
		PhaseCompleted:   intFromAny(call.Arguments["phase_completed"]),
		EvidenceUseful:   boolFromAny(call.Arguments["evidence_useful"]),
		CompletionReason: stringFromAny(call.Arguments["completion_reason"]),
		NextPhase:        stringFromAny(call.Arguments["next_phase"]),
	}
	if progress.PhaseCompleted == 0 || strings.TrimSpace(progress.CompletionReason) == "" {
		return phaseProgress{}, false
	}
	return progress, true
}

func newPhaseStepState(plan phasePlan) *phaseStepState {
	return &phaseStepState{
		RequestGoal:       strings.TrimSpace(plan.RequestGoal),
		CurrentPhaseIndex: plan.CurrentPhaseIndex,
		PhaseSteps:        append([]phaseStep(nil), plan.PhaseSteps...),
		Completed:         make(map[int]bool),
	}
}

func (s *phaseStepState) activePhaseRef() PhaseRef {
	current := s.currentStep()
	if current.Index == 0 {
		return PhaseRef{}
	}
	return PhaseRef{
		Index: current.Index,
		Name:  strings.TrimSpace(current.Name),
	}
}

func (l *Loop) currentPhaseRef() PhaseRef {
	if l == nil || l.phaseStepState == nil {
		return PhaseRef{}
	}
	return l.phaseStepState.activePhaseRef()
}

func (s *phaseStepState) runtimeState() *PhaseRuntimeState {
	if s == nil {
		return nil
	}
	state := &PhaseRuntimeState{
		RequestGoal: strings.TrimSpace(s.RequestGoal),
		Active:      s.activePhaseRef(),
		Phases:      make([]PhaseSpec, 0, len(s.PhaseSteps)),
		Completed:   map[int]bool{},
	}
	for index, completed := range s.Completed {
		if completed {
			state.Completed[index] = true
		}
	}
	for _, step := range s.PhaseSteps {
		status := PhasePending
		if state.Completed[step.Index] {
			status = PhaseCompleted
		}
		if step.Index == state.Active.Index {
			status = PhaseActive
		}
		state.Phases = append(state.Phases, PhaseSpec{
			Ref: PhaseRef{
				Index: step.Index,
				Name:  strings.TrimSpace(step.Name),
			},
			Goal:                strings.TrimSpace(step.Goal),
			CompletionCondition: strings.TrimSpace(step.CompletionCondition),
			AllowedNext:         append([]string(nil), step.AllowedNext...),
			Status:              status,
			Steps:               phaseExecutionStepRuntimeStates(step, PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)}, status),
		})
	}
	return state
}

func phaseExecutionStepRuntimeStates(s phaseStep, phase PhaseRef, phaseStatus PhaseStatus) []StepRuntimeState {
	if len(s.Steps) == 0 {
		return nil
	}
	steps := make([]StepRuntimeState, 0, len(s.Steps))
	for i, step := range s.Steps {
		index := step.Index
		if index == 0 {
			index = i + 1
		}
		status := StepPending
		if phaseStatus == PhaseCompleted {
			status = StepCompleted
		}
		steps = append(steps, StepRuntimeState{
			Ref: StepRef{
				Phase: phase,
				Kind:  StepExplicitPhase,
				ID:    strings.TrimSpace(step.ID),
				Index: index,
			},
			Status:          status,
			Description:     strings.TrimSpace(step.Description),
			Command:         strings.TrimSpace(step.Command),
			ExpectedOutcome: strings.TrimSpace(step.ExpectedOutcome),
		})
	}
	return steps
}

func (s *phaseStepState) acceptProgress(progress phaseProgress) bool {
	if s == nil {
		return false
	}
	current := s.currentStep()
	if !phaseflow.MatchesCurrent(current.Index, progress.PhaseCompleted) {
		return false
	}
	if s.Completed == nil {
		s.Completed = make(map[int]bool)
	}
	s.Completed[progress.PhaseCompleted] = true
	if strings.TrimSpace(progress.NextPhase) != "" {
		if next := s.allowedNextIndex(current, progress.NextPhase); next != 0 {
			s.CurrentPhaseIndex = next
		} else {
			delete(s.Completed, progress.PhaseCompleted)
			return false
		}
	} else if len(current.AllowedNext) > 0 {
		if next := s.firstAllowedNextIndex(current); next != 0 {
			s.CurrentPhaseIndex = next
		} else {
			delete(s.Completed, progress.PhaseCompleted)
			return false
		}
	} else if s.firstIncompleteAfter(progress.PhaseCompleted) != 0 {
		delete(s.Completed, progress.PhaseCompleted)
		return false
	}
	return true
}

func (s *phaseStepState) currentStep() phaseStep {
	if s == nil {
		return phaseStep{}
	}
	for _, step := range s.PhaseSteps {
		if step.Index == s.CurrentPhaseIndex {
			return step
		}
	}
	return phaseStep{}
}

func (s *phaseStepState) phaseName(index int) string {
	if s == nil {
		return ""
	}
	for _, step := range s.PhaseSteps {
		if step.Index == index {
			return step.Name
		}
	}
	return ""
}

func (s *phaseStepState) phaseStepForRef(ref PhaseRef) (phaseStep, bool) {
	if s == nil {
		return phaseStep{}, false
	}
	for _, step := range s.PhaseSteps {
		candidate := PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)}
		if candidate.Matches(ref) {
			return step, true
		}
	}
	return phaseStep{}, false
}

func (s *phaseStepState) moveToPhase(ref PhaseRef) error {
	if s == nil {
		return fmt.Errorf("phase state is nil")
	}
	for _, step := range s.PhaseSteps {
		candidate := PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)}
		if candidate.Matches(ref) {
			if s.Completed != nil && s.Completed[step.Index] {
				return fmt.Errorf("target phase %s is already completed", candidate.String())
			}
			s.CurrentPhaseIndex = step.Index
			return nil
		}
	}
	return fmt.Errorf("target phase does not exist: %s", ref.String())
}

func (s *phaseStepState) rewindToPhase(ref PhaseRef) (PhaseRef, error) {
	if s == nil {
		return PhaseRef{}, fmt.Errorf("phase state is nil")
	}
	for _, step := range s.PhaseSteps {
		candidate := PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)}
		if !candidate.Matches(ref) {
			continue
		}
		if s.Completed == nil {
			s.Completed = map[int]bool{}
		}
		for _, phase := range s.PhaseSteps {
			if phase.Index >= step.Index {
				delete(s.Completed, phase.Index)
			}
		}
		s.CurrentPhaseIndex = step.Index
		return candidate, nil
	}
	return PhaseRef{}, fmt.Errorf("target phase does not exist: %s", ref.String())
}

func (s *phaseStepState) phasesAtOrAfter(ref PhaseRef) []PhaseRef {
	if s == nil {
		return nil
	}
	start := 0
	for _, step := range s.PhaseSteps {
		candidate := PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)}
		if candidate.Matches(ref) {
			start = step.Index
			break
		}
	}
	if start == 0 {
		return nil
	}
	var refs []PhaseRef
	for _, step := range s.PhaseSteps {
		if step.Index >= start {
			refs = append(refs, PhaseRef{Index: step.Index, Name: strings.TrimSpace(step.Name)})
		}
	}
	return refs
}

func (s *phaseStepState) allowedNextIndex(current phaseStep, name string) int {
	name = strings.TrimSpace(strings.ToLower(name))
	if s == nil || name == "" {
		return 0
	}
	if len(current.AllowedNext) == 0 {
		return 0
	}
	allowed := false
	for _, candidate := range current.AllowedNext {
		if strings.EqualFold(strings.TrimSpace(candidate), name) {
			allowed = true
			break
		}
	}
	if !allowed {
		return 0
	}
	for _, step := range s.PhaseSteps {
		if strings.ToLower(strings.TrimSpace(step.Name)) == name && !s.Completed[step.Index] {
			return step.Index
		}
	}
	return 0
}

func (s *phaseStepState) firstAllowedNextIndex(current phaseStep) int {
	if s == nil {
		return 0
	}
	for _, candidate := range current.AllowedNext {
		if next := s.allowedNextIndex(current, candidate); next != 0 {
			return next
		}
	}
	return 0
}

func (s *phaseStepState) firstIncompleteAfter(index int) int {
	if s == nil {
		return 0
	}
	for _, step := range s.PhaseSteps {
		if step.Index > index && !s.Completed[step.Index] {
			return step.Index
		}
	}
	return 0
}

func (l *Loop) phaseStepAnchor() string {
	state := l.phaseStepState
	if state == nil {
		return ""
	}
	current := state.currentStep()
	if current.Index == 0 {
		return ""
	}
	var rawPlan []byte
	if compact := state.compactPlan(); compact != nil {
		rawPlan, _ = json.Marshal(compact)
	}
	var b strings.Builder
	b.WriteString("Active phase-step progress. Follow the model-declared phase plan; do not skip or replace it with guide progress.\n")
	if len(rawPlan) > 0 {
		fmt.Fprintf(&b, "phase_plan: %s\n", string(rawPlan))
	}
	fmt.Fprintf(&b, "current_phase_index: %d\n", current.Index)
	fmt.Fprintf(&b, "current_phase_name: %s\n", current.Name)
	fmt.Fprintf(&b, "current_phase_goal: %s\n", current.Goal)
	fmt.Fprintf(&b, "current_phase_completion_condition: %s\n", current.CompletionCondition)
	if len(current.AllowedNext) > 0 {
		fmt.Fprintf(&b, "allowed_next_phases: %s\n", strings.Join(current.AllowedNext, ","))
	}
	if l.resourceClassification != nil {
		fmt.Fprintf(&b, "resource_classification: %s\n", l.resourceClassification.Kind)
		if l.resourceClassification.Source != "" {
			fmt.Fprintf(&b, "resource_classification_source: %s\n", l.resourceClassification.Source)
		}
		if l.resourceClassification.APIGroup != "" {
			fmt.Fprintf(&b, "resource_api_group: %s\n", l.resourceClassification.APIGroup)
		}
	}
	b.WriteString("Rules:\n")
	b.WriteString("- Use `phase_progress` to complete the active top-level phase_step when its completion condition is satisfied.\n")
	b.WriteString("- When emitting an action after a tool observation, summarize what the latest executed command showed before explaining the next action. Include important statuses, missing objects, empty results, or errors.\n")
	b.WriteString("- Do not use `guide_progress` for top-level phase completion; `guide_progress` is only for nested guidance_step entries while current_phase_name=guided_diagnosis.\n")
	if strings.EqualFold(strings.TrimSpace(current.Name), "guidance_lookup") {
		b.WriteString("- current_phase_name=guidance_lookup: return one top-level `resource_guide_lookup` object. Do not emit kubectl action, `phase_progress`, or `guide_progress` until the resource-guide lookup result is observed or recorded unavailable.\n")
	} else {
		b.WriteString("- If guidance is useful, enter it through guidance_decision/guidance_lookup; do not assume runtime will automatically inject RAG.\n")
	}
	return b.String()
}

func (l *Loop) requestGuidedDiagnosisPhaseProgress() {
	if !l.requestOnlyGuidedDiagnosisPhaseProgress() {
		return
	}
	var b strings.Builder
	b.WriteString("All nested resource-guide guidance_step entries have been completed for the active guided_diagnosis phase.\n")
	b.WriteString("Your next response MUST be a `phase_progress` object completing the active guided_diagnosis phase; do not emit another action or final_report yet.\n")
	b.WriteString("Set next_phase to final_report unless live evidence requires a different allowed next phase from the accepted phase_plan.")
	l.queueResponseDirective(b.String())
}

func (l *Loop) requestOnlyGuidedDiagnosisPhaseProgress() bool {
	if l.controlState() == RuntimeControlAwaitingGuidedPhaseProgress {
		return false
	}
	l.transitionControl(RuntimeControlAwaitingGuidedPhaseProgress)
	return true
}

func (l *Loop) enterGuidedDiagnosisPhase() bool {
	if l.phaseStepState == nil {
		return false
	}
	if index := l.phaseStepState.indexByName("guided_diagnosis"); index != 0 {
		l.phaseStepState.CurrentPhaseIndex = index
		delete(l.phaseStepState.Completed, index)
		return true
	}
	return false
}

func (l *Loop) rewindPhaseBeforeGuidance() {
	if l.phaseStepState == nil {
		return
	}
	index := l.phaseStepState.preferredPreGuidanceIndex()
	if index == 0 {
		return
	}
	l.phaseStepState.CurrentPhaseIndex = index
	for _, step := range l.phaseStepState.PhaseSteps {
		if step.Index >= index {
			delete(l.phaseStepState.Completed, step.Index)
		}
	}
}

func (l *Loop) phaseAllowsPlainAnswer() bool {
	if l.phaseStepState == nil {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(l.phaseStepState.currentStep().Name))
	switch name {
	case lightweightLookupPhase, "response_synthesis", "clarification", "explanation", "final_report":
		return true
	default:
		return false
	}
}

func (l *Loop) rejectPlainAnswerOutsideResponsePhase() bool {
	current := ""
	if l.phaseStepState != nil {
		current = l.phaseStepState.currentStep().Name
	}
	message := "Plain answers are only allowed from response_synthesis, clarification, explanation, or final_report phases. Complete or advance the active phase with phase_progress, or continue it with one valid action."
	if current != "" {
		message = fmt.Sprintf("Active phase is %q. %s", current, message)
	}
	return l.applyModelOutputCorrectionGate("plain_answer_wrong_phase", "잘못된 phase의 일반 응답이 반복되어 루프를 중단했습니다.", message)
}

func (s *phaseStepState) compactPlan() map[string]any {
	if s == nil {
		return nil
	}
	completed := make([]int, 0, len(s.Completed))
	for _, step := range s.PhaseSteps {
		if s.Completed[step.Index] {
			completed = append(completed, step.Index)
		}
	}
	return map[string]any{
		"request_goal":        s.RequestGoal,
		"current_phase_index": s.CurrentPhaseIndex,
		"completed_phases":    completed,
	}
}

func (s *phaseStepState) indexByName(name string) int {
	name = strings.TrimSpace(strings.ToLower(name))
	if s == nil || name == "" {
		return 0
	}
	for _, step := range s.PhaseSteps {
		if strings.ToLower(strings.TrimSpace(step.Name)) == name {
			return step.Index
		}
	}
	return 0
}

func (s *phaseStepState) preferredPreGuidanceIndex() int {
	if s == nil {
		return 0
	}
	for _, preferred := range []string{"observation_execution", "observation_planning", "observation_completion", "context_resolution"} {
		if index := s.indexByName(preferred); index != 0 {
			return index
		}
	}
	for i := len(s.PhaseSteps) - 1; i >= 0; i-- {
		step := s.PhaseSteps[i]
		name := strings.ToLower(strings.TrimSpace(step.Name))
		if name == "" || strings.Contains(name, "guidance") || name == "final_report" {
			continue
		}
		return step.Index
	}
	return 0
}

func stringFromAny(value any) string {
	v, _ := value.(string)
	return strings.TrimSpace(v)
}

func boolFromAny(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(v))
		return parsed
	default:
		return false
	}
}

func stringSliceFromAny(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := stringFromAny(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(v))
		return i
	default:
		return 0
	}
}

func (l *Loop) handleRequestedResourceGuideLookup(ctx context.Context, calls []gollm.FunctionCall) bool {
	for _, call := range calls {
		if call.Name != internalResourceGuideLookupCall {
			continue
		}
		klog.V(0).InfoS("resource guide lookup requested")
		if !l.phaseAllowsResourceGuideLookup() {
			return l.applyModelOutputCorrectionGate("resource_guide_wrong_phase", "잘못된 phase의 resource_guide_lookup 요청이 반복되어 진단을 중단합니다.", "Resource guide lookup is only allowed from the guidance_lookup phase after observation evidence is available. Complete or advance the current phase with phase_progress, or continue the active phase with the next safest diagnostic.")
		}
		request, ok := resourceGuideLookupFromFunctionCall(call)
		if !ok {
			return l.applyModelOutputCorrectionGate("invalid_resource_guide_lookup", "resource_guide_lookup 형식 오류가 반복되어 진단을 중단합니다.", "Resource guide lookup request was invalid. Continue with the next safest kubectl diagnostic.")
		}
		if l.resourceClassification == nil || l.resourceClassification.Kind != resourceClassificationCRD {
			return l.applyModelOutputCorrectionGate("resource_guide_without_confirmed_crd", "확인되지 않은 CRD resource_guide_lookup 요청이 반복되어 진단을 중단합니다.", "Resource guide lookup is only available after runtime discovery confirms the primary target is a CRD. Continue with the next safest kubectl diagnostic and do not infer a CRD or Cluster API family from the name alone.")
		}
		query := l.resourceGuideRefinementQuery(request)
		if l.resourceGuideQueryAlreadyUsed(query) {
			return l.applyModelOutputCorrectionGate("duplicate_resource_guide_lookup", "중복 resource_guide_lookup 요청이 반복되어 진단을 중단합니다.", "That refined resource-guide lookup was already performed for the same problem focus and evidence. Do not repeat it; choose the next kubectl diagnostic or answer from the evidence.")
		}
		klog.V(0).InfoS("resource guide lookup accepted", "resource_family", request.ResourceFamily, "query_len", len(query))
		l.searchAndInjectResourceGuide(ctx, request.ResourceFamily, query)
		return true
	}
	return false
}

func (l *Loop) rejectActionDuringGuidanceLookupWithoutGuide(calls []gollm.FunctionCall) bool {
	if l.phaseStepState == nil || l.resourceGuideInjected {
		return false
	}
	if l.resourceClassification == nil || l.resourceClassification.Kind != resourceClassificationCRD {
		return false
	}
	current := l.phaseStepState.currentStep()
	if !strings.EqualFold(strings.TrimSpace(current.Name), "guidance_lookup") {
		return false
	}
	for _, call := range calls {
		if call.Name == internalResourceGuideLookupCall || call.Name == internalPhaseProgressCall {
			continue
		}
		if _, ok := kubectlCommandFromFunctionCall(call); !ok {
			continue
		}
		message := "Active phase is guidance_lookup for a CRD-backed primary target. This phase must request a top-level `resource_guide_lookup` before any kubectl action. Return one `resource_guide_lookup` object for the accepted primary resource and operational problem focus."
		return l.applyModelOutputCorrectionGate("guidance_lookup_action_before_lookup", "resource_guide_lookup 전 kubectl action이 반복되어 진단을 중단합니다.", message)
	}
	return false
}

func (l *Loop) phaseAllowsResourceGuideLookup() bool {
	if l.phaseStepState == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(l.phaseStepState.currentStep().Name))
	switch name {
	case "guidance_lookup":
		return true
	default:
		return false
	}
}

func (l *Loop) searchAndInjectResourceGuide(ctx context.Context, resource, query string) {
	klog.V(0).InfoS("resource guidance search starting", "resource", resource, "query_len", len(query))
	client, err := newGuidanceClient(l.cfg)
	if err != nil {
		klog.Warningf("resource guidance client init failed: %v", err)
		l.markResourceGuideQuery(query)
		l.injectResourceGuideUnavailable(resource, "client_init_failed")
		return
	}
	if client.KnowledgeProvider() != guidance.KnowledgeProviderQdrant {
		klog.V(0).InfoS("resource guidance provider unsupported", "provider", client.KnowledgeProvider())
		l.markResourceGuideQuery(query)
		l.injectResourceGuideUnavailable(resource, "provider_not_implemented_for_resource_guides="+string(client.KnowledgeProvider()))
		return
	}
	searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	found, err := client.SearchGuides(searchCtx, query)
	cancel()
	if err != nil {
		klog.Warningf("resource guidance search failed: %v", err)
		l.markResourceGuideQuery(query)
		l.injectResourceGuideUnavailable(resource, "search_failed")
		return
	}
	originalCaseCount := 0
	if found != nil {
		originalCaseCount = len(found.Cases)
	}
	klog.V(0).InfoS("resource guidance search completed", "resource", resource, "cases", originalCaseCount)
	found = l.filterResourceGuidesForRequest(found, query)
	if originalCaseCount > 0 && (found == nil || len(found.Cases) == 0) {
		l.markResourceGuideQuery(query)
		l.injectResourceGuideUnavailable(resource, "filtered_no_applicable_guides")
		return
	}
	if l.injectResourceGuideAttempt(resource, found) {
		l.markResourceGuideQuery(query)
	}
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

func (l *Loop) injectResourceGuideAttempt(resource string, found *guidance.GuideSearchResult) bool {
	l.guideStepState = l.buildGuideStepState(found)
	caseCount := 0
	if found != nil {
		caseCount = len(found.Cases)
	}
	klog.V(0).InfoS("injecting resource guide attempt", "resource", resource, "cases", caseCount, "has_steps", l.guideStepState != nil)
	l.pendingResponseDirective = ""
	if l.guideStepState != nil {
		if !l.enterGuidedDiagnosisPhase() {
			l.guideStepState = nil
			l.phaseStepState = nil
			message := "A resource guide was found, but the accepted phase_plan has no guided_diagnosis phase_step. Return one corrected phase_plan that declares guidance_lookup, guided_diagnosis, and the final response phase as explicit phase_steps before using guide steps. guidance_step entries can run only inside a declared guided_diagnosis phase_step."
			l.applyModelOutputCorrectionGate("guided_diagnosis_phase_missing", "guided_diagnosis phase 누락 오류가 반복되어 진단을 중단합니다.", message)
			return false
		}
	}
	l.resourceGuideInjected = true
	includeClusterAPI := guideResultImpliesClusterAPI(found)
	l.promptOptions = l.newPromptOptions(l.requestIntent, true, includeClusterAPI)
	if l.shouldCompactForStateRewrite() {
		before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		limit := l.contextLimitTokens()
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: injecting CRD guide context for %s; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", resource, before, limit))
		if err := l.resetChatSession(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return false
		}
		l.currChatContent = []any{l.compactedStateMessage("Use the following guide context as decision support, then choose the next safest step.")}
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: CRD guide context injected for %s. estimated context %d/%d tokens.", resource, after, limit))
	} else {
		if err := l.resetChatSessionPreservingCurrentContent(); err != nil {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
			l.pendingCalls = nil
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return false
		}
		l.appendGuideObservation(guideRefFromResult(resource, found), formatResourceGuideObservation(resource, found))
		klog.Infof("resource guide injected for CRD %s without context compact", resource)
	}
	l.pendingCalls = nil
	l.currIteration++
	l.transitionAfterPhaseAdvance()
	return true
}

func (l *Loop) injectResourceGuideUnavailable(resource, reason string) {
	l.resourceGuideInjected = true
	l.guideStepState = nil
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
			l.transitionControl(RuntimeControlAwaitingUserQuery)
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
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return
		}
		l.appendGuideObservation(guideRef{GuideID: "unavailable:" + resource + ":" + reason, Hash: contextHash(content)}, content)
		klog.Infof("resource guide unavailable for CRD %s (%s); continuing without context compact", resource, reason)
	}
	l.pendingCalls = nil
	l.currIteration++
	l.transitionControl(RuntimeControlAwaitingModelStep)
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

func (l *Loop) resourceGuideRefinementQuery(request resourceGuideLookup) string {
	return strings.Join([]string{
		l.originalQuery,
		"resource family: " + request.ResourceFamily,
		"problem focus: " + request.ProblemFocus,
		"reason for refinement: " + request.Reason,
		"observed evidence: " + request.Evidence,
	}, "\n")
}

func (l *Loop) filterResourceGuidesForRequest(found *guidance.GuideSearchResult, query string) *guidance.GuideSearchResult {
	if found == nil || len(found.Cases) == 0 {
		return found
	}
	if requestOrQuerySuggestsDeletion(l.originalQuery, query) {
		return found
	}
	filtered := *found
	filtered.Cases = nil
	for _, c := range found.Cases {
		if guideCaseIsDeletionOrCleanup(c) {
			continue
		}
		filtered.Cases = append(filtered.Cases, c)
	}
	return &filtered
}

func requestOrQuerySuggestsDeletion(values ...string) bool {
	for _, value := range values {
		lower := strings.ToLower(value)
		for _, marker := range []string{
			"delete", "deletion", "deleting", "cleanup", "clean up", "deletiontimestamp",
			"cluster-delete", "nodegroup-delete", "resource-delete",
			"삭제", "삭제중", "삭제 중", "정리",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func guideCaseIsDeletionOrCleanup(c guidance.GuideCase) bool {
	fields := []string{
		c.ID,
		c.Title,
		c.Cause,
		c.Resolution,
		strings.Join(c.Symptoms, " "),
		strings.Join(c.EvidenceKeywords, " "),
		strings.Join(c.DecisionHints, " "),
		strings.Join(c.Tags, " "),
	}
	return requestOrQuerySuggestsDeletion(fields...)
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
		Skipped:    map[int]bool{},
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

const (
	resourceClassificationBuiltin = "built_in"
	resourceClassificationCRD     = "crd"
	resourceClassificationUnknown = "unknown"
)

type resourceClassification struct {
	Kind     string
	Source   string
	APIGroup string
	Reason   string
}

type crdList struct {
	Items []struct {
		Spec struct {
			Group string `json:"group"`
			Names struct {
				Plural     string   `json:"plural"`
				Singular   string   `json:"singular"`
				Kind       string   `json:"kind"`
				ShortNames []string `json:"shortNames"`
			} `json:"names"`
		} `json:"spec"`
	} `json:"items"`
}

func (l *Loop) classifyResourceByDiscovery(ctx context.Context, resource string) resourceClassification {
	resource = strings.ToLower(strings.TrimSpace(resource))
	if resource == "" {
		return resourceClassification{Kind: resourceClassificationUnknown, Source: "empty"}
	}
	normalized := normalizeKubectlResource(resource)
	if isBuiltinKubernetesResource(normalized) {
		return resourceClassification{Kind: resourceClassificationBuiltin, Source: "builtin_catalog"}
	}
	if l.resourceDiscoveryCache != nil {
		if cached, ok := l.resourceDiscoveryCache[resource]; ok {
			return cached
		}
	} else {
		l.resourceDiscoveryCache = make(map[string]resourceClassification)
	}

	classification := l.classifyNonBuiltinResourceByDiscovery(ctx, resource)
	l.resourceDiscoveryCache[resource] = classification
	return classification
}

func (l *Loop) classifyNonBuiltinResourceByDiscovery(ctx context.Context, resource string) resourceClassification {
	if l.executor == nil {
		return resourceClassification{
			Kind:   resourceClassificationUnknown,
			Source: "discovery_unavailable",
			Reason: "executor unavailable",
		}
	}

	if crd, ok, err := l.lookupCRDResource(ctx, resource); err == nil && ok {
		return crd
	} else if err != nil {
		return resourceClassification{
			Kind:   resourceClassificationUnknown,
			Source: "crd_discovery_error",
			Reason: err.Error(),
		}
	}

	if exists, err := l.lookupAPIResource(ctx, resource); err == nil && exists {
		return resourceClassification{
			Kind:   resourceClassificationBuiltin,
			Source: "api_resources_non_crd",
		}
	}

	return resourceClassification{
		Kind:   resourceClassificationUnknown,
		Source: "discovery",
		Reason: "resource was not found in CRD discovery",
	}
}

func (l *Loop) lookupCRDResource(ctx context.Context, resource string) (resourceClassification, bool, error) {
	out, err := l.runDiscoveryCommand(ctx, "kubectl get customresourcedefinitions.apiextensions.k8s.io -o json")
	if err != nil {
		return resourceClassification{}, false, err
	}
	var list crdList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return resourceClassification{}, false, fmt.Errorf("parse CRD discovery output: %w", err)
	}
	for _, item := range list.Items {
		names := []string{
			item.Spec.Names.Plural,
			item.Spec.Names.Singular,
			item.Spec.Names.Kind,
			item.Spec.Names.Plural + "." + item.Spec.Group,
		}
		names = append(names, item.Spec.Names.ShortNames...)
		if nameMatchesResource(resource, names) {
			return resourceClassification{
				Kind:     resourceClassificationCRD,
				Source:   "crd_discovery",
				APIGroup: item.Spec.Group,
			}, true, nil
		}
	}
	return resourceClassification{}, false, nil
}

func (l *Loop) lookupAPIResource(ctx context.Context, resource string) (bool, error) {
	out, err := l.runDiscoveryCommand(ctx, "kubectl api-resources -o name")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if nameMatchesResource(resource, []string{line, strings.Split(line, ".")[0]}) {
			return true, nil
		}
	}
	return false, nil
}

func (l *Loop) runDiscoveryCommand(ctx context.Context, command string) (string, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	env := os.Environ()
	if l.cfg != nil && strings.TrimSpace(l.cfg.Kubeconfig) != "" {
		kubeconfig, err := tools.ExpandShellVar(l.cfg.Kubeconfig)
		if err != nil {
			return "", err
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
	}

	result, err := l.executor.Execute(discoveryCtx, command, env, l.workDir)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.Error != "" {
		errText := strings.TrimSpace(firstNonEmptyGuideText(result.Stderr, result.Error))
		if errText == "" {
			errText = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return "", fmt.Errorf("%s failed: %s", command, errText)
	}
	return result.Stdout, nil
}

func nameMatchesResource(resource string, names []string) bool {
	resource = strings.ToLower(strings.TrimSpace(resource))
	resourceBase := strings.Split(resource, ".")[0]
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if resource == name || resourceBase == name {
			return true
		}
		if strings.Split(name, ".")[0] == resource {
			return true
		}
	}
	return false
}

func (l *Loop) consumeGuideProgress(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	handled := false
	for _, call := range calls {
		if call.Name != internalGuideProgressCall {
			remaining = append(remaining, call)
			continue
		}
		handled = true
		if !l.guideProgressAllowedForCurrentPhase() {
			return remaining, l.applyModelOutputCorrectionGate("guide_progress_wrong_phase", "잘못된 phase의 guide_progress가 반복되어 진단을 중단합니다.", "guide_progress is only valid while a resource guide is active inside guided_diagnosis. Continue the current phase with a valid structured output or action.")
		}
		step, ok := guideStepCompletedFromArguments(call.Arguments)
		if !ok {
			return remaining, l.applyModelOutputCorrectionGate("invalid_guide_progress", "guide_progress 형식 오류가 반복되어 진단을 중단합니다.", "guide_progress payload was invalid. Return guide_progress with step_completed as a positive 1-based guide step index and evidence_useful=true only when live evidence advanced that step.")
		}
		completedAll := l.markGuideStepCompleted(step)
		klog.V(0).InfoS("guide progress consumed", "step_completed", step, "completed_all", completedAll)
		l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
			ID:   call.ID,
			Name: call.Name,
			Result: map[string]any{
				"status":         "recorded",
				"step_completed": step,
			},
		})
		if completedAll {
			l.requestPostGuideCompletionDirective()
		}
	}
	if l.controlState() == RuntimeControlAwaitingGuidedPhaseProgress && len(remaining) > 0 {
		message := "The final guide_progress completed the nested guide steps. Return it alone; the runtime will request phase_progress in the next response. Do not combine final guide_progress with phase_progress, an action, final_report, or another internal call."
		return nil, l.applyModelOutputCorrectionGate("guide_progress_mixed_after_completion", "마지막 guide_progress와 다른 call을 함께 반환해 phase를 전진시키지 않았습니다.", message)
	}
	if handled && len(remaining) == 0 {
		l.currIteration++
		return nil, true
	}
	return remaining, false
}

func (l *Loop) guideProgressAllowedForCurrentPhase() bool {
	if l.guideStepState == nil {
		return false
	}
	if l.phaseStepState == nil {
		return true
	}
	return strings.EqualFold(l.phaseStepState.currentStep().Name, "guided_diagnosis")
}

func guideProgressObservationUseful(result map[string]any) bool {
	return toolResultSucceeded(result)
}

func guideStepCompletedFromFunctionCall(call gollm.FunctionCall) (int, bool) {
	raw, ok := call.Arguments["guide_progress"].(map[string]any)
	if !ok {
		return 0, false
	}
	return guideStepCompletedFromArguments(raw)
}

func guideStepCompletedFromArguments(raw map[string]any) (int, bool) {
	if useful, ok := raw["evidence_useful"]; ok && !boolFromAny(useful) {
		return 0, false
	}
	value, ok := raw["step_completed"]
	if !ok {
		return 0, false
	}
	step := intFromAny(value)
	return step, step > 0
}

func (l *Loop) inferGuideStepCompletedFromFunctionCall(call gollm.FunctionCall) (int, bool) {
	guide := l.guideStepState
	if guide == nil {
		return 0, false
	}
	command, ok := commandString(call.Arguments["command"])
	if !ok {
		return 0, false
	}
	remaining := guide.remainingSteps()
	if len(remaining) == 0 {
		return 0, false
	}
	nextStep := remaining[0]
	if guideStepCommandMatches(guide.stepDetail(nextStep), command) {
		return nextStep, true
	}
	return 0, false
}

func guideStepCommandMatches(step guideStepDetail, command string) bool {
	rendered := strings.TrimSpace(step.RenderedCommand)
	if rendered == "" || strings.Contains(rendered, "{{") || strings.TrimSpace(command) == "" {
		return false
	}
	return normalizeGuideCommand(rendered) == normalizeGuideCommand(command)
}

func normalizeGuideCommand(command string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
}

type GateOutcomeKind = gateflow.OutcomeKind

const (
	GateOutcomeModelOutputCorrection = gateflow.ModelOutputCorrection
	GateOutcomeAgentCommandRetry     = gateflow.AgentCommandRetry
	GateOutcomeUserRequestBlocked    = gateflow.UserRequestBlocked
	GateOutcomePolicyBlock           = gateflow.PolicyBlock
	GateOutcomeToolExecutionFailure  = gateflow.ToolExecutionFailure
	GateOutcomeRetrievalResultGate   = gateflow.RetrievalResultGate
	GateOutcomeExternalStateWait     = gateflow.ExternalStateWait
)

type RetryScope = gateflow.RetryScope

const (
	RetryScopeNone          = gateflow.RetryNone
	RetryScopeCurrentStep   = gateflow.RetryCurrentStep
	RetryScopeCurrentPhase  = gateflow.RetryCurrentPhase
	RetryScopeAgentCommand  = gateflow.RetryAgentCommand
	RetryScopeUserRequest   = gateflow.RetryUserRequest
	RetryScopeExternalState = gateflow.RetryExternalState
)

type CorrectionMode = gateflow.CorrectionMode

const (
	CorrectionModeNone            = gateflow.CorrectionNone
	CorrectionModeAppendCompacted = gateflow.CorrectionAppendCompacted
	CorrectionModeAppendPlain     = gateflow.CorrectionAppendPlain
	CorrectionModeUserMessageOnly = gateflow.CorrectionUserMessageOnly
)

type BranchPolicy = gateflow.BranchPolicy

const (
	BranchStayCurrent      = gateflow.StayCurrent
	BranchRetryStep        = gateflow.RetryStep
	BranchRecheckStep      = gateflow.RecheckStep
	BranchSkipStep         = gateflow.SkipStep
	BranchMovePhase        = gateflow.MovePhase
	BranchRewindPhase      = gateflow.RewindPhase
	BranchBlockUserRequest = gateflow.BlockUserRequest
)

type GateOutcome gateflow.Outcome

func (o GateOutcome) Validate(snapshot RuntimeSnapshot) error {
	return gateflow.Validate(gateflow.Outcome(o), gateflow.ValidationContext{
		HasTargetPhase: o.TargetPhase == nil || snapshot.hasPhaseRef(*o.TargetPhase),
		HasTargetStep:  o.TargetStep == nil || snapshot.hasStepRef(*o.TargetStep),
	})
}

func (l *Loop) applyGateOutcome(outcome GateOutcome) bool {
	return l.applyGateOutcomeWithRepeatedCorrection(outcome, nil)
}

func (l *Loop) applyGateOutcomeWithRepeatedCorrection(outcome GateOutcome, repeated func(message string) bool) bool {
	if outcome.Allow {
		klog.V(2).InfoS("runtime gate outcome allowed", "code", outcome.Code, "kind", outcome.Kind)
		return false
	}
	klog.V(0).InfoS("runtime gate outcome blocking",
		"code", outcome.Code,
		"kind", outcome.Kind,
		"retryable", outcome.Retryable,
		"retry_scope", outcome.RetryScope,
		"branch_policy", outcome.BranchPolicy,
		"correction_mode", outcome.CorrectionMode,
	)
	if err := outcome.Validate(l.RuntimeSnapshot()); err != nil {
		klog.ErrorS(err, "runtime gate outcome validation failed", "code", outcome.Code, "kind", outcome.Kind)
		l.pendingCalls = nil
		l.currIteration = 0
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate outcome이 현재 runtime snapshot과 맞지 않아 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	message := gateflow.Message(strings.TrimSpace(outcome.ModelCorrection), strings.TrimSpace(outcome.UserMessage), "Runtime gate blocked the previous model output. Choose a safe corrected next step.")
	code := strings.TrimSpace(outcome.Code)
	if code == "" {
		code = "runtime_gate_blocked"
	}
	mode := outcome.CorrectionMode
	if mode == "" {
		mode = CorrectionModeAppendCompacted
	}
	if mode != CorrectionModeUserMessageOnly && mode != CorrectionModeNone {
		appended := false
		if mode == CorrectionModeAppendPlain {
			appended = l.appendCorrection(code, message)
		} else {
			appended = l.appendCorrectionWithCompaction(code, message)
		}
		if !appended {
			klog.V(0).InfoS("runtime gate correction repeated", "code", code)
			if repeated != nil {
				return repeated(message)
			}
			userMessage := strings.TrimSpace(outcome.UserMessage)
			if userMessage == "" {
				userMessage = "runtime gate correction이 반복되어 루프를 중단했습니다."
			}
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, userMessage+"\n"+message)
			l.pendingCalls = nil
			l.currIteration = 0
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return true
		}
	}
	if outcome.UserVisible && strings.TrimSpace(outcome.UserMessage) != "" {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, strings.TrimSpace(outcome.UserMessage))
	}
	if err := l.applyGateBranch(outcome); err != nil {
		klog.ErrorS(err, "runtime gate branch failed", "code", code, "branch_policy", outcome.BranchPolicy)
		l.pendingCalls = nil
		l.currIteration = 0
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate branch 적용 실패로 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	l.pendingCalls = nil
	l.currIteration++
	// A correction retries the same control obligation. Gate branches that
	// intentionally change phase or step own their explicit control transition.
	// This is a post-condition assertion only. Branch side effects are not
	// rolled back on failure, so production gates should avoid ExpectedControl
	// unless the branch outcome is already safe to keep if the assertion fails.
	if err := outcome.AssertExpectedControl(l.RuntimeSnapshot()); err != nil {
		klog.ErrorS(err, "runtime gate expected control assertion failed", "code", code, "expected", outcome.ExpectedControl)
		l.pendingCalls = nil
		l.currIteration = 0
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "runtime gate outcome 적용 후 control assertion이 맞지 않아 루프를 중단했습니다.\n"+err.Error())
		return true
	}
	klog.V(1).InfoS("runtime gate outcome applied", "code", code, "next_iteration", l.currIteration+1)
	return true
}

func (o GateOutcome) AssertExpectedControl(snapshot RuntimeSnapshot) error {
	return gateflow.AssertExpectedControl(o.ExpectedControl, snapshot.Control)
}

func (l *Loop) applyGateBranch(outcome GateOutcome) error {
	switch outcome.BranchPolicy {
	case "", BranchStayCurrent, BranchRetryStep, BranchBlockUserRequest:
		return nil
	case BranchRecheckStep:
		return l.applyRecheckStepBranch(outcome)
	case BranchMovePhase:
		target, err := l.validateMovePhaseBranch(outcome)
		if err != nil {
			return err
		}
		if err := l.phaseStepState.moveToPhase(PhaseRef{Index: target.Index, Name: strings.TrimSpace(target.Name)}); err != nil {
			return err
		}
		l.transitionAfterPhaseAdvance()
		return nil
	case BranchSkipStep:
		if outcome.TargetStep == nil {
			return fmt.Errorf("branch policy %s requires target step", outcome.BranchPolicy)
		}
		if !l.SkipStep(*outcome.TargetStep) {
			return fmt.Errorf("branch policy %s cannot skip target step %s", outcome.BranchPolicy, outcome.TargetStep.String())
		}
		if outcome.TargetStep.Kind == StepResourceGuideDiagnostic && l.guideStepState != nil && l.guideStepState.allCompleted() {
			l.requestPostGuideCompletionDirective()
		}
		if outcome.TargetStep.Kind == StepMutationEvidenceRequirement {
			l.transitionMutationVerification()
		}
		return nil
	case BranchRewindPhase:
		target, err := l.validateRewindPhaseBranch(outcome)
		if err != nil {
			return err
		}
		targetRef, err := l.phaseStepState.rewindToPhase(PhaseRef{Index: target.Index, Name: strings.TrimSpace(target.Name)})
		if err != nil {
			return err
		}
		l.resetPhaseScopedState(targetRef, l.defaultPhaseScopedResetPolicy(targetRef))
		l.transitionAfterPhaseAdvance()
		return nil
	default:
		return fmt.Errorf("unsupported branch policy %q", outcome.BranchPolicy)
	}
}

func (l *Loop) validateMovePhaseBranch(outcome GateOutcome) (phaseStep, error) {
	if outcome.TargetPhase == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires target phase", outcome.BranchPolicy)
	}
	if l == nil || l.phaseStepState == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires active phase state", outcome.BranchPolicy)
	}
	target, ok := l.phaseStepState.phaseStepForRef(*outcome.TargetPhase)
	if !ok {
		return phaseStep{}, fmt.Errorf("target phase does not exist: %s", outcome.TargetPhase.String())
	}
	if l.phaseStepState.Completed != nil && l.phaseStepState.Completed[target.Index] {
		return phaseStep{}, fmt.Errorf("target phase %s is already completed", PhaseRef{Index: target.Index, Name: target.Name}.String())
	}
	current := l.phaseStepState.currentStep()
	if current.Index == 0 {
		return phaseStep{}, fmt.Errorf("branch policy %s requires current phase", outcome.BranchPolicy)
	}
	if target.Index == current.Index {
		return target, nil
	}
	if target.Index < current.Index {
		return phaseStep{}, fmt.Errorf("branch policy %s cannot move backward from %s to %s; use %s with cleanup instead", outcome.BranchPolicy, PhaseRef{Index: current.Index, Name: current.Name}.String(), PhaseRef{Index: target.Index, Name: target.Name}.String(), BranchRewindPhase)
	}
	if l.phaseStepState.allowedNextIndex(current, target.Name) == target.Index {
		return target, nil
	}
	if l.runtimeAllowsPhaseMove(outcome, current, target) {
		return target, nil
	}
	return phaseStep{}, fmt.Errorf("target phase %s is not in allowed_next for current phase %s and no runtime override applies", PhaseRef{Index: target.Index, Name: target.Name}.String(), PhaseRef{Index: current.Index, Name: current.Name}.String())
}

func (l *Loop) validateRewindPhaseBranch(outcome GateOutcome) (phaseStep, error) {
	if outcome.TargetPhase == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires target phase", outcome.BranchPolicy)
	}
	if strings.TrimSpace(outcome.Code) == "" {
		return phaseStep{}, fmt.Errorf("branch policy %s requires source gate code for cleanup audit", outcome.BranchPolicy)
	}
	if l == nil || l.phaseStepState == nil {
		return phaseStep{}, fmt.Errorf("branch policy %s requires active phase state", outcome.BranchPolicy)
	}
	target, ok := l.phaseStepState.phaseStepForRef(*outcome.TargetPhase)
	if !ok {
		return phaseStep{}, fmt.Errorf("target phase does not exist: %s", outcome.TargetPhase.String())
	}
	current := l.phaseStepState.currentStep()
	if current.Index == 0 {
		return phaseStep{}, fmt.Errorf("branch policy %s requires current phase", outcome.BranchPolicy)
	}
	if target.Index > current.Index {
		return phaseStep{}, fmt.Errorf("branch policy %s cannot rewind forward from %s to %s", outcome.BranchPolicy, PhaseRef{Index: current.Index, Name: current.Name}.String(), PhaseRef{Index: target.Index, Name: target.Name}.String())
	}
	return target, nil
}

func (l *Loop) runtimeAllowsPhaseMove(outcome GateOutcome, current, target phaseStep) bool {
	if strings.TrimSpace(outcome.Code) == "" {
		return false
	}
	switch outcome.Kind {
	case GateOutcomeExternalStateWait:
		return outcome.RetryScope == RetryScopeExternalState
	case GateOutcomeRetrievalResultGate:
		return outcome.RetryScope == RetryScopeCurrentPhase || outcome.RetryScope == RetryScopeCurrentStep
	case GateOutcomePolicyBlock, GateOutcomeAgentCommandRetry, GateOutcomeModelOutputCorrection:
		return target.Index == current.Index
	default:
		return false
	}
}

func (l *Loop) applyRecheckStepBranch(outcome GateOutcome) error {
	if l == nil || l.controlState() != RuntimeControlAwaitingMutationContinuation {
		return fmt.Errorf("branch policy %s requires active external-state mutation continuation", outcome.BranchPolicy)
	}
	attempt, exhausted := l.consumeMutationContinuationAttempt()
	if exhausted {
		l.transitionControl(RuntimeControlAwaitingFinalReport)
		l.finalReportMustBeInconclusive = true
		l.queueResponseDirective(fmt.Sprintf("External state recheck budget is exhausted after %d recheck attempts for source_gate=%s. Return final_report with conclusive=false, summarize the recheck attempts, and explain that the external state did not reach a resolved condition within the runtime budget.", attempt, strings.TrimSpace(outcome.Code)))
		return nil
	}
	l.transitionControl(RuntimeControlAwaitingMutationContinuation)
	l.queueResponseDirective(fmt.Sprintf("External state still needs verification for source_gate=%s. Continue with one read-only recheck action or a valid mutation_verification_result after the observation. recheck_attempt: %d/%d.", strings.TrimSpace(outcome.Code), attempt, maxMutationContinuationAttempts))
	return nil
}

func (l *Loop) applyModelOutputCorrectionGate(code, userMessage, correction string) bool {
	return l.applyModelOutputCorrectionGateWithMode(code, userMessage, correction, CorrectionModeAppendCompacted)
}

func (l *Loop) applyPlainModelOutputCorrectionGate(code, userMessage, correction string) bool {
	return l.applyModelOutputCorrectionGateWithMode(code, userMessage, correction, CorrectionModeAppendPlain)
}

func (l *Loop) applyModelOutputCorrectionGateWithMode(code, userMessage, correction string, mode CorrectionMode) bool {
	return l.applyGateOutcome(GateOutcome{
		Kind:            GateOutcomeModelOutputCorrection,
		Code:            code,
		Retryable:       true,
		RetryScope:      RetryScopeCurrentPhase,
		UserMessage:     userMessage,
		ModelCorrection: correction,
		CorrectionMode:  mode,
		BranchPolicy:    BranchStayCurrent,
	})
}

func (s RuntimeSnapshot) hasPhaseRef(ref PhaseRef) bool {
	if ref.Index == 0 && strings.TrimSpace(ref.Name) == "" {
		return false
	}
	if s.PhaseRuntime == nil {
		return false
	}
	for _, phase := range s.PhaseRuntime.Phases {
		if phase.Ref.Matches(ref) {
			return true
		}
	}
	return false
}

func (s RuntimeSnapshot) hasStepRef(ref StepRef) bool {
	if ref.Kind == "" && ref.ID == "" && ref.Index == 0 {
		return false
	}
	for _, step := range s.ActiveSteps {
		if step.Ref.Matches(ref) {
			return true
		}
	}
	return false
}

type pendingMutationVerification struct {
	MutationStep    int
	MutationCommand string
	Requirements    []mutationEvidenceRequirement
	Satisfied       map[string]bool
	Skipped         map[string]bool
	AwaitingResult  bool
}

type mutationEvidenceRequirement struct {
	ID               string
	Kind             string
	Target           actionTarget
	Purpose          string
	SuggestedCommand string
}

const maxMutationContinuationAttempts = 3

func (l *Loop) mutationVerificationAnchor() string {
	if l.pendingMutationVerification == nil {
		return ""
	}
	return "Active mutation verification obligation:\n" + l.pendingMutationVerification.requiredMessage()
}

func (l *Loop) rejectMissingMutationVerificationOnNoCalls() bool {
	if l.controlState() != RuntimeControlAwaitingMutationVerificationEvidence && l.controlState() != RuntimeControlAwaitingMutationVerificationResult {
		return false
	}
	return l.rejectMissingMutationVerification("mutation_verification_required_no_call")
}

func (l *Loop) rejectMutationContinuationOnNoCalls() bool {
	if l.controlState() != RuntimeControlAwaitingMutationContinuation {
		return false
	}
	return l.rejectMutationContinuationRequired("mutation_continuation_required_no_call")
}

func (l *Loop) enforceMutationContinuation(calls []gollm.FunctionCall) bool {
	if l.controlState() != RuntimeControlAwaitingMutationContinuation || len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		switch call.Name {
		case internalFinalReportCall, internalPhaseProgressCall, internalNextDirectionsCall:
			return l.rejectMutationContinuationRequired("mutation_continuation_required")
		}
	}
	return false
}

func (l *Loop) rejectMutationContinuationRequired(kind string) bool {
	message := "The previous mutation verification result was progressing or unresolved. Continue the ReAct loop with the next best action based on the verification evidence; do not emit final_report, phase_progress, next_directions, or a plain answer yet."
	return l.applyModelOutputCorrectionGate(kind, "mutation continuation 요구가 반복적으로 무시되어 루프를 중단했습니다.", message)
}

func (l *Loop) enforcePendingMutationVerification(calls []gollm.FunctionCall) bool {
	if (l.controlState() != RuntimeControlAwaitingMutationVerificationEvidence && l.controlState() != RuntimeControlAwaitingMutationVerificationResult) || len(calls) == 0 {
		return false
	}
	if l.controlState() == RuntimeControlAwaitingMutationVerificationResult {
		if len(calls) == 1 && calls[0].Name == internalMutationVerificationResultCall {
			return false
		}
		return l.rejectMissingMutationVerification("mutation_verification_result_required")
	}
	if l.mutationVerificationCallsMatch(calls) {
		return false
	}
	return l.rejectMissingMutationVerification("mutation_verification_required")
}

func (l *Loop) rejectMissingMutationVerification(kind string) bool {
	verification := l.pendingMutationVerification
	if verification == nil {
		return false
	}
	message := verification.requiredMessage()
	return l.applyModelOutputCorrectionGate(kind, "mutation verification 요구가 반복적으로 무시되어 루프를 중단했습니다.", message)
}

func (l *Loop) consumeMutationVerificationResult(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	if l.controlState() == RuntimeControlAwaitingMutationVerificationResult && !onlyFunctionCall(calls, internalMutationVerificationResultCall) {
		return nil, l.rejectMissingMutationVerification("mutation_verification_result_required")
	}
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalMutationVerificationResultCall {
			remaining = append(remaining, call)
			continue
		}
		if l.controlState() != RuntimeControlAwaitingMutationVerificationResult || l.pendingMutationVerification == nil {
			return remaining, l.applyModelOutputCorrectionGate("unexpected_mutation_verification_result", "unexpected mutation_verification_result가 반복되어 루프를 중단했습니다.", "mutation_verification_result is only valid immediately after all required mutation verification evidence has been collected. Continue with the active phase using a valid action, phase_progress, or final_report.")
		}
		result, ok := mutationVerificationResultFromFunctionCall(call)
		if !ok {
			return remaining, l.applyModelOutputCorrectionGate("invalid_mutation_verification_result", "mutation_verification_result 형식 오류가 반복되어 루프를 중단했습니다.", "mutation_verification_result payload was invalid. Return status as one of resolved, progressing, or unresolved. Include evidence_summary and a next_action when status is progressing or unresolved.")
		}
		l.pendingMutationVerification = nil
		switch result.Status {
		case "resolved":
			l.mutationContinuationAttempts = 0
			if l.guideStepState != nil && l.guideStepState.allCompleted() {
				l.requestPostGuideCompletionDirective()
			} else {
				l.queueResponseDirective("Mutation verification result is resolved. You may now complete the active phase or emit a final_report grounded in the verification evidence. Do not claim more than the evidence supports.")
			}
		case "progressing":
			l.requestMutationContinuationOrBudgetReport(result)
		case "unresolved":
			l.requestMutationContinuationOrBudgetReport(result)
		}
		l.currChatContent = append(l.currChatContent, gollm.FunctionCallResult{
			ID:   call.ID,
			Name: call.Name,
			Result: map[string]any{
				"status":           result.Status,
				"evidence_summary": result.EvidenceSummary,
				"reason":           result.Reason,
				"next_action":      result.NextAction,
			},
		})
	}
	if len(remaining) == 0 {
		l.currIteration++
		if l.controlState() == RuntimeControlAwaitingMutationVerificationResult {
			l.transitionControl(RuntimeControlAwaitingModelStep)
		}
		return nil, true
	}
	return remaining, false
}

func mutationContinuationDirective(result mutationVerificationResult) string {
	var b strings.Builder
	switch result.Status {
	case "progressing":
		b.WriteString("Mutation verification result is progressing. Continue the ReAct loop with the next best read-only observation after an appropriate wait/recheck interval. Do not emit a conclusive final_report yet.")
	case "unresolved":
		b.WriteString("Mutation verification result is unresolved. Continue the ReAct loop with a different diagnostic or remediation approach based on the observed evidence. Do not emit a conclusive final_report yet.")
	default:
		return ""
	}
	if next := strings.TrimSpace(result.NextAction); next != "" {
		fmt.Fprintf(&b, "\nmodel_proposed_next_action: %s", next)
	}
	return b.String()
}

func (l *Loop) requestMutationContinuationOrBudgetReport(result mutationVerificationResult) {
	attempt, exhausted := l.consumeMutationContinuationAttempt()
	if exhausted {
		l.transitionControl(RuntimeControlAwaitingFinalReport)
		l.finalReportMustBeInconclusive = true
		l.queueResponseDirective(mutationContinuationBudgetExhaustedDirective(result, attempt))
		return
	}
	l.transitionControl(RuntimeControlAwaitingMutationContinuation)
	directive := mutationContinuationDirective(result)
	if directive != "" {
		directive = fmt.Sprintf("%s\nrecheck_attempt: %d/%d", directive, attempt, maxMutationContinuationAttempts)
	}
	l.queueResponseDirective(directive)
}

func (l *Loop) consumeMutationContinuationAttempt() (int, bool) {
	l.mutationContinuationAttempts++
	if l.mutationContinuationAttempts > maxMutationContinuationAttempts {
		return maxMutationContinuationAttempts, true
	}
	return l.mutationContinuationAttempts, false
}

func mutationContinuationBudgetExhaustedDirective(result mutationVerificationResult, attempts int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mutation verification continuation budget is exhausted after %d recheck attempts. Your next response MUST be exactly one final_report object with conclusive=false. Summarize the mutation verification evidence, lifecycle that the external lifecycle did not settle within the runtime recheck budget, and recommend the next manual or follow-up observation. Do not emit another action, phase_progress, next_directions, or plain answer.", attempts)
	if next := strings.TrimSpace(result.NextAction); next != "" {
		fmt.Fprintf(&b, "\nlast_model_proposed_next_action: %s", next)
	}
	if reason := strings.TrimSpace(result.Reason); reason != "" {
		fmt.Fprintf(&b, "\nlast_reason: %s", reason)
	}
	return b.String()
}

func mutationVerificationResultFromFunctionCall(call gollm.FunctionCall) (mutationVerificationResult, bool) {
	result := mutationVerificationResult{
		Status:          strings.ToLower(strings.TrimSpace(stringFromAny(call.Arguments["status"]))),
		EvidenceSummary: stringSliceFromAnyLoose(call.Arguments["evidence_summary"]),
		Reason:          stringFromAny(call.Arguments["reason"]),
		NextAction:      stringFromAny(call.Arguments["next_action"]),
	}
	switch result.Status {
	case "resolved":
		return result, len(result.EvidenceSummary) > 0 || strings.TrimSpace(result.Reason) != ""
	case "progressing", "unresolved":
		return result, (len(result.EvidenceSummary) > 0 || strings.TrimSpace(result.Reason) != "") && strings.TrimSpace(result.NextAction) != ""
	default:
		return mutationVerificationResult{}, false
	}
}

func (v pendingMutationVerification) requiredMessage() string {
	var b strings.Builder
	if v.AwaitingResult {
		b.WriteString("Required mutation verification evidence has been collected. Your next response MUST be exactly one `mutation_verification_result` object. Do not emit final_report, phase_progress, next_directions, answer, or action in the same response.\n")
		b.WriteString("Set status to `resolved` only when the evidence shows the original target is healthy or the requested mutation goal is achieved. Set `progressing` when the system is still rolling out or not yet settled; set `unresolved` when the evidence shows the problem remains or a new blocker is visible. If status is progressing or unresolved, include the next read-only observation or remediation angle in next_action.")
		return b.String()
	}
	b.WriteString("A Kubernetes mutation has just been executed. Do not emit final_report, answer, phase_progress, next_directions, or another mutation yet. The next response must be exactly one read-only kubectl action that satisfies one remaining verification evidence requirement.\n")
	if v.MutationCommand != "" {
		fmt.Fprintf(&b, "mutation_command: %s\n", v.MutationCommand)
	}
	remaining := v.remainingRequirements()
	if len(remaining) > 0 {
		b.WriteString("remaining_evidence_requirements:\n")
		for _, req := range remaining {
			fmt.Fprintf(&b, "- id: %s\n", req.ID)
			if req.Kind != "" {
				fmt.Fprintf(&b, "  kind: %s\n", req.Kind)
			}
			if req.Target.Resource != "" {
				fmt.Fprintf(&b, "  resource: %s\n", req.Target.Resource)
			}
			if req.Target.Name != "" {
				fmt.Fprintf(&b, "  name: %s\n", req.Target.Name)
			}
			if req.Target.Namespace != "" {
				fmt.Fprintf(&b, "  namespace: %s\n", req.Target.Namespace)
			}
			if req.Purpose != "" {
				fmt.Fprintf(&b, "  purpose: %s\n", req.Purpose)
			}
			if req.SuggestedCommand != "" {
				fmt.Fprintf(&b, "  suggested_command: %s\n", req.SuggestedCommand)
			}
		}
	}
	b.WriteString("Use read-only kubectl observations only. Preserve namespace scope exactly when it is known. Direct-effect evidence proves the mutation was applied; outcome evidence proves the original user-visible problem improved.")
	return b.String()
}

func (v pendingMutationVerification) remainingRequirements() []mutationEvidenceRequirement {
	var out []mutationEvidenceRequirement
	for _, req := range v.Requirements {
		if v.Satisfied != nil && v.Satisfied[req.ID] {
			continue
		}
		if v.Skipped != nil && v.Skipped[req.ID] {
			continue
		}
		out = append(out, req)
	}
	return out
}

func (v pendingMutationVerification) stepRuntimeStates(phase PhaseRef) []StepRuntimeState {
	steps := make([]StepRuntimeState, 0, len(v.Requirements))
	for _, req := range v.Requirements {
		status := StepPending
		if v.Satisfied != nil && v.Satisfied[req.ID] {
			status = StepCompleted
		} else if v.Skipped != nil && v.Skipped[req.ID] {
			status = StepSkipped
		} else if !v.AwaitingResult {
			status = StepActive
		}
		steps = append(steps, StepRuntimeState{
			Ref: StepRef{
				Phase: phase,
				Kind:  StepMutationEvidenceRequirement,
				ID:    strings.TrimSpace(req.ID),
			},
			Status:          status,
			Description:     strings.TrimSpace(req.Purpose),
			Command:         strings.TrimSpace(req.SuggestedCommand),
			ExpectedOutcome: strings.TrimSpace(req.Kind),
		})
	}
	return steps
}

func (v pendingMutationVerification) allSatisfied() bool {
	return len(v.remainingRequirements()) == 0
}

func (l *Loop) trackMutationVerification(call PendingCall, result map[string]any) {
	if l.controlState() == RuntimeControlAwaitingMutationContinuation && toolResultSucceeded(result) {
		l.transitionControl(RuntimeControlAwaitingModelStep)
	}
	if l.pendingMutationVerification != nil {
		if l.shouldStartMutationVerification(call, result) {
			if verification, ok := l.mutationVerificationFromCall(call); ok {
				l.mergeMutationVerification(verification)
				l.mutationContinuationAttempts = 0
			}
			l.transitionMutationVerification()
			return
		}
		if reqID, ok := l.mutationVerificationCallMatchID(call.FunctionCall); ok && toolResultSucceeded(result) {
			if l.pendingMutationVerification.Satisfied == nil {
				l.pendingMutationVerification.Satisfied = map[string]bool{}
			}
			l.pendingMutationVerification.Satisfied[reqID] = true
		}
		if l.pendingMutationVerification.allSatisfied() {
			l.pendingMutationVerification.AwaitingResult = true
		}
		l.transitionMutationVerification()
		return
	}
	if !l.shouldStartMutationVerification(call, result) {
		return
	}
	verification, ok := l.mutationVerificationFromCall(call)
	if !ok {
		return
	}
	l.pendingMutationVerification = &verification
	l.mutationContinuationAttempts = 0
	l.transitionMutationVerification()
}

func (l *Loop) transitionMutationVerification() {
	if l == nil || l.pendingMutationVerification == nil {
		return
	}
	next := verificationflow.Next(
		len(l.pendingMutationVerification.remainingRequirements()) > 0,
		l.pendingMutationVerification.AwaitingResult,
	)
	if next == verificationflow.ContinueResult {
		l.transitionControl(RuntimeControlAwaitingMutationVerificationResult)
		return
	}
	l.transitionControl(RuntimeControlAwaitingMutationVerificationEvidence)
}

func (l *Loop) mergeMutationVerification(next pendingMutationVerification) {
	if l.pendingMutationVerification == nil {
		l.pendingMutationVerification = &next
		l.mutationContinuationAttempts = 0
		return
	}
	if next.MutationCommand != "" {
		if l.pendingMutationVerification.MutationCommand == "" {
			l.pendingMutationVerification.MutationCommand = next.MutationCommand
		} else if !strings.Contains(l.pendingMutationVerification.MutationCommand, next.MutationCommand) {
			l.pendingMutationVerification.MutationCommand += "\n" + next.MutationCommand
		}
	}
	if l.pendingMutationVerification.Satisfied == nil {
		l.pendingMutationVerification.Satisfied = map[string]bool{}
	}
	if l.pendingMutationVerification.Skipped == nil {
		l.pendingMutationVerification.Skipped = map[string]bool{}
	}
	seen := map[string]bool{}
	for _, req := range l.pendingMutationVerification.Requirements {
		seen[req.ID] = true
	}
	expanded := false
	for _, req := range next.Requirements {
		if seen[req.ID] || l.hasPendingEquivalentOutcomeEvidence(req) {
			continue
		}
		l.pendingMutationVerification.Requirements = append(l.pendingMutationVerification.Requirements, req)
		seen[req.ID] = true
		expanded = true
	}
	if expanded {
		l.mutationContinuationAttempts = 0
		l.pendingMutationVerification.AwaitingResult = false
	}
}

func (l *Loop) hasPendingEquivalentOutcomeEvidence(next mutationEvidenceRequirement) bool {
	if l == nil || l.pendingMutationVerification == nil || next.Kind != "outcome_evidence" {
		return false
	}
	verification := l.pendingMutationVerification
	for _, existing := range verification.Requirements {
		if existing.Kind != next.Kind || verification.Satisfied[existing.ID] || verification.Skipped[existing.ID] {
			continue
		}
		if sameActionTarget(existing.Target, next.Target) {
			return true
		}
	}
	return false
}

func (l *Loop) shouldStartMutationVerification(call PendingCall, result map[string]any) bool {
	if l == nil || l.cfg == nil || l.cfg.ReadOnly {
		return false
	}
	if strings.TrimSpace(call.ModifiesResource) == "" || call.ModifiesResource == "no" {
		return false
	}
	return toolResultSucceeded(result)
}

func (l *Loop) mutationVerificationFromCall(call PendingCall) (pendingMutationVerification, bool) {
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	if command == "" {
		command = strings.TrimSpace(stringFromAny(call.FunctionCall.Arguments["command"]))
	}
	target, hasTarget := actionTargetFromFunctionCall(call.FunctionCall)
	if !hasTarget {
		target = actionTarget{}
	}
	if target.Resource == "" {
		if resource, ok := primaryMutatingKubectlResource(command); ok {
			target.Resource = resource
		}
	}
	if target.Name == "" {
		if name, ok := primaryKubectlObjectName(command); ok {
			target.Name = name
		}
	}
	if target.Namespace == "" {
		if namespace, ok := commandNamespaceValue(command); ok {
			target.Namespace = namespace
		} else if l.requestContext != nil {
			target.Namespace = l.requestScopeNamespace()
		}
	}
	target.Resource = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(target.Resource)))
	target.Namespace = cleanNamespaceValue(target.Namespace)
	target.Name = cleanUnknownPlaceholder(target.Name)
	if target.Resource == "unknown" {
		target.Resource = ""
	}
	requirements := l.mutationEvidenceRequirements(target, command, stringFromAny(call.FunctionCall.Arguments["expected_observation"]))
	if len(requirements) == 0 {
		return pendingMutationVerification{}, false
	}
	verification := pendingMutationVerification{
		MutationStep:    l.actionSeq,
		MutationCommand: command,
		Requirements:    requirements,
		Satisfied:       map[string]bool{},
		Skipped:         map[string]bool{},
	}
	return verification, true
}

func (l *Loop) mutationVerificationCallsMatch(calls []gollm.FunctionCall) bool {
	if len(calls) == 0 || l.pendingMutationVerification == nil {
		return false
	}
	satisfied := map[string]bool{}
	for key, value := range l.pendingMutationVerification.Satisfied {
		satisfied[key] = value
	}
	for key, value := range l.pendingMutationVerification.Skipped {
		if value {
			satisfied[key] = true
		}
	}
	for _, call := range calls {
		reqID, ok := l.mutationVerificationCallMatchIDWithSatisfied(call, satisfied)
		if !ok {
			return false
		}
		satisfied[reqID] = true
	}
	return true
}

func (l *Loop) mutationVerificationCallMatchID(call gollm.FunctionCall) (string, bool) {
	if l.pendingMutationVerification == nil {
		return "", false
	}
	return l.mutationVerificationCallMatchIDWithSatisfied(call, l.pendingMutationVerification.Satisfied)
}

func (l *Loop) mutationVerificationCallMatchIDWithSatisfied(call gollm.FunctionCall, satisfied map[string]bool) (string, bool) {
	verification := l.pendingMutationVerification
	if verification == nil {
		return "", false
	}
	command, ok := kubectlCommandFromFunctionCall(call)
	if !ok || !isNonMutatingKubectlInvocation(call) {
		return "", false
	}
	bestID := ""
	bestScore := -1
	for _, req := range verification.Requirements {
		if satisfied != nil && satisfied[req.ID] {
			continue
		}
		if verification.Skipped != nil && verification.Skipped[req.ID] {
			continue
		}
		if evidenceRequirementMatchesCommand(req, command) {
			score := evidenceRequirementSpecificity(req)
			if score > bestScore {
				bestID = req.ID
				bestScore = score
			}
		}
	}
	if bestID == "" {
		return "", false
	}
	return bestID, true
}

func (l *Loop) mutationEvidenceRequirements(target actionTarget, command, expectedObservation string) []mutationEvidenceRequirement {
	var requirements []mutationEvidenceRequirement
	idPrefix := fmt.Sprintf("mutation_%d", l.actionSeq)
	if target.Resource == "" && target.Name == "" && isKubectlApplyFileCommand(command) {
		return nil
	}
	if target.Resource != "" || target.Name != "" {
		direct := mutationEvidenceRequirement{
			ID:               idPrefix + "_direct_effect",
			Kind:             "direct_effect",
			Target:           target,
			Purpose:          strings.TrimSpace(expectedObservation),
			SuggestedCommand: verificationCommandHint(target),
		}
		if direct.Purpose == "" {
			direct.Purpose = "Verify this specific mutation was applied to the changed resource."
		}
		requirements = append(requirements, direct)
	}

	if outcome, ok := l.outcomeEvidenceRequirement(idPrefix, target); ok {
		requirements = append(requirements, outcome)
	}
	return requirements
}

func isKubectlApplyFileCommand(command string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok || verb != "apply" {
			continue
		}
		for j := verbIndex + 1; j < len(fields); j++ {
			field := strings.Trim(fields[j], "'\"")
			if field == "-f" || field == "--filename" || strings.HasPrefix(field, "--filename=") {
				return true
			}
		}
	}
	return false
}

func (l *Loop) outcomeEvidenceRequirement(idPrefix string, directTarget actionTarget) (mutationEvidenceRequirement, bool) {
	if l == nil || l.requestContext == nil {
		return mutationEvidenceRequirement{}, false
	}
	target := actionTarget{
		Resource:  normalizeKubectlResource(strings.ToLower(strings.TrimSpace(l.requestContext.PrimaryTarget.Resource))),
		Name:      cleanUnknownPlaceholder(l.requestContext.PrimaryTarget.Name),
		Namespace: l.requestScopeNamespace(),
	}
	if target.Resource == "" || target.Resource == "unknown" {
		return mutationEvidenceRequirement{}, false
	}
	if sameActionTarget(target, directTarget) {
		return mutationEvidenceRequirement{}, false
	}
	req := mutationEvidenceRequirement{
		ID:               idPrefix + "_outcome_primary_target",
		Kind:             "outcome_evidence",
		Target:           target,
		Purpose:          "Verify the original user-visible target after the mutation. Prefer the highest-signal object first; if it is still progressing, wait or collect the next most relevant clue instead of concluding.",
		SuggestedCommand: verificationCommandHint(target),
	}
	return req, true
}

func sameActionTarget(a, b actionTarget) bool {
	return normalizeKubectlResource(strings.ToLower(strings.TrimSpace(a.Resource))) == normalizeKubectlResource(strings.ToLower(strings.TrimSpace(b.Resource))) &&
		strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)) &&
		strings.EqualFold(strings.TrimSpace(a.Namespace), strings.TrimSpace(b.Namespace))
}

func evidenceRequirementMatchesCommand(req mutationEvidenceRequirement, command string) bool {
	target := req.Target
	return verificationflow.Matches(
		verificationflow.Requirement{ID: req.ID, Target: target},
		verificationflow.MatchEvidence{
			Namespace: target.Namespace == "" ||
				(target.Resource != "" && !kubectlResourceUsuallyNamespaced(target.Resource)) ||
				commandUsesNamespace(command, target.Namespace),
			Resource: commandMentionsResource(command, target.Resource) || commandMentionsResourceKind(command, target.Resource),
			Name:     commandMentionsToken(command, target.Name) || commandUsesSelectorForName(command, target.Name),
		},
	)
}

func evidenceRequirementSpecificity(req mutationEvidenceRequirement) int {
	score := 0
	if strings.TrimSpace(req.Target.Resource) != "" {
		score++
	}
	if strings.TrimSpace(req.Target.Name) != "" {
		score++
	}
	if strings.TrimSpace(req.Target.Namespace) != "" {
		score++
	}
	return score
}

func commandMentionsResourceKind(command, resource string) bool {
	resource = normalizeKubectlResource(strings.ToLower(strings.TrimSpace(resource)))
	if resource == "" {
		return true
	}
	for _, field := range strings.Fields(strings.ToLower(command)) {
		field = strings.Trim(field, "'\",")
		if field == "" || strings.HasPrefix(field, "-") {
			continue
		}
		kind := normalizeKubectlResource(kubectlResourceKindFromArg(field))
		if kind == resource {
			return true
		}
	}
	return false
}

func toolResultSucceeded(result map[string]any) bool {
	if result == nil {
		return false
	}
	if isPartialToolResult(result) {
		return false
	}
	if err, ok := result["error"].(string); ok && strings.TrimSpace(err) != "" {
		return false
	}
	status, _ := result["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "blocked", "declined", "denied", "failed", "failure", "error", "partial", "partial_success", "partially_succeeded":
		return false
	default:
		return true
	}
}

func primaryKubectlObjectName(command string) (string, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	for i := 0; i < len(fields); i++ {
		if strings.Trim(fields[i], "'\"") != "kubectl" {
			continue
		}
		verb, verbIndex, ok := kube.KubectlVerbAndIndexFromFields(fields, i)
		if !ok || !kube.IsKubectlMutatingVerb(verb) {
			continue
		}
		if name, ok := kubectlObjectNameAfterResource(fields, verbIndex+1); ok {
			return name, true
		}
	}
	return "", false
}

func kubectlObjectNameAfterResource(fields []string, start int) (string, bool) {
	seenResource := false
	for i := start; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if kubectlFlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if kubectlShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		if !seenResource {
			seenResource = true
			if slash := strings.Index(field, "/"); slash > 0 && slash+1 < len(field) {
				return strings.TrimSpace(field[slash+1:]), true
			}
			continue
		}
		return field, true
	}
	return "", false
}

func verificationCommandHint(target actionTarget) string {
	resource := strings.TrimSpace(target.Resource)
	if resource == "" {
		return ""
	}
	name := strings.TrimSpace(target.Name)
	namespace := strings.TrimSpace(target.Namespace)
	var b strings.Builder
	b.WriteString("kubectl get ")
	b.WriteString(resource)
	if name != "" {
		b.WriteString(" ")
		b.WriteString(name)
	}
	if namespace != "" && kubectlResourceUsuallyNamespaced(resource) {
		b.WriteString(" -n ")
		b.WriteString(namespace)
	}
	b.WriteString(" -o yaml")
	return b.String()
}

// markGuideStepCompleted records that the model finished a numbered guide
// step. Called after an action observation is appended. Returns true if the
// lifecycle transitioned to "all steps completed" on this call.
func (l *Loop) markGuideStepCompleted(stepIndex int) bool {
	guide := l.guideStepState
	if guide == nil || stepIndex < 1 || stepIndex > guide.TotalSteps {
		return false
	}
	completed, changed, allCompleted := guidanceflow.MarkCompleted(
		guide.Completed,
		guide.Skipped,
		stepIndex,
		guide.TotalSteps,
	)
	guide.Completed = completed
	return changed && allCompleted
}

func (l *Loop) requestPostGuideCompletionDirective() {
	if l.guideStepState == nil || !l.guideStepState.allCompleted() {
		return
	}
	if l.pendingMutationVerification != nil {
		return
	}
	if l.phaseStepState != nil && strings.EqualFold(l.phaseStepState.currentStep().Name, "guided_diagnosis") {
		l.requestGuidedDiagnosisPhaseProgress()
		return
	}
	l.requestFinalReportFromModel()
}

// requestFinalReportFromModel prompts the model to emit a final_report. Used
// when all diagnostic_steps have been completed (or are no longer useful).
// The instruction is appended to currChatContent so it goes out in the next
// LLM call alongside the latest observation.
func (l *Loop) requestFinalReportFromModel() {
	if !l.requestOnlyFinalReport() {
		return
	}
	var b strings.Builder
	b.WriteString("All resource-guide diagnostic_steps have been completed (see guide_step anchor).\n")
	b.WriteString("Your next response MUST be a `final_report` object — do not emit another `action`.\n")
	b.WriteString("Required fields:\n")
	b.WriteString("- conclusive: true if the gathered evidence is sufficient to answer the original_query, otherwise false.\n")
	b.WriteString("- conclusion: when conclusive=true, a concise answer grounded in observed evidence.\n")
	b.WriteString("- attempted: short bullets summarizing the diagnostic steps actually run.\n")
	b.WriteString("- evidence_known: facts directly observed from tool output; required when conclusive=true.\n")
	b.WriteString("- evidence_missing: facts that would have helped but were not obtainable; for conclusive=false include this or blockers if evidence_known is empty.\n")
	b.WriteString("- most_likely_cause: best-guess cause given partial evidence; use the literal string \"inconclusive\" if no cause is supported.\n")
	b.WriteString("- problematic_resources: suspected blocker/root-cause investigation targets only. Include kind, name, namespace, and reason when a related resource is the likely next object to investigate. Do not list the original primary resource merely because it reports a symptom; if the related resource name is unknown, put that gap in evidence_missing or recommended_user_actions instead.\n")
	b.WriteString("- recommended_user_actions: concrete next steps the user can run outside this session (optional).\n")
	b.WriteString("- blockers: hard constraints that prevented full diagnosis (optional). Examples: \"workload kubeconfig not available in this session\".\n")
	b.WriteString("Do not emit `action`, `resource_guide_lookup`, or `next_directions` in this response.")
	l.queueResponseDirective(b.String())
}

func (l *Loop) requestOnlyFinalReport() bool {
	if l.controlState() == RuntimeControlAwaitingFinalReport {
		return false
	}
	l.transitionControl(RuntimeControlAwaitingFinalReport)
	return true
}

// consumeFinalReport intercepts the model's final_report output. If the
// report is conclusive, the loop ends with the conclusion shown to the user.
// Otherwise the loop asks the model to propose next_directions in the next
// iteration.
func (l *Loop) consumeFinalReport(ctx context.Context, calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalFinalReportCall {
			remaining = append(remaining, call)
			continue
		}
		report, ok := finalReportFromFunctionCall(call)
		if !ok {
			return nil, l.applyPlainModelOutputCorrectionGate("invalid_final_report", "final_report 형식 오류가 반복되어 진단을 중단합니다.", "final_report payload was invalid. Re-emit a final_report with required fields. Conclusive reports require attempted, evidence_known, most_likely_cause, and conclusion. Inconclusive reports require attempted, most_likely_cause, and at least one of evidence_known, evidence_missing, or blockers.")
		}
		if l.finalReportMustBeInconclusive && report.Conclusive {
			message := "Mutation verification recheck budget was exhausted without a resolved result. Return exactly one final_report with conclusive=false, summarize the unresolved evidence and exhausted rechecks, and recommend the next manual or follow-up observation."
			l.queueResponseDirective(message)
			return nil, l.applyPlainModelOutputCorrectionGate("mutation_budget_final_report_must_be_inconclusive", "mutation verification budget 소진 후 conclusive=true 보고가 반복되어 진단을 중단합니다.", message)
		}
		l.pendingFinalReport = &report
		l.emitFinalReportMessage(ctx, report)
		l.guideStepState = nil
		if report.Conclusive {
			if l.promptProblematicResourceInvestigation(report) {
				return nil, true
			}
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.pendingCalls = nil
			l.currIteration = 0
			return nil, true
		}
		l.requestNextDirectionsFromModel(report)
		l.pendingCalls = nil
		l.currIteration++
		l.transitionControl(RuntimeControlAwaitingNextDirections)
		return nil, true
	}
	return remaining, false
}

func finalReportFromFunctionCall(call gollm.FunctionCall) (finalReport, bool) {
	report := finalReportFromArguments(call.Arguments)
	if len(report.Attempted) == 0 || strings.TrimSpace(report.MostLikelyCause) == "" {
		return finalReport{}, false
	}
	if report.Conclusive {
		if len(report.EvidenceKnown) == 0 || strings.TrimSpace(report.Conclusion) == "" {
			return finalReport{}, false
		}
		return report, true
	}
	if len(report.EvidenceKnown) == 0 && len(report.EvidenceMissing) == 0 && len(report.Blockers) == 0 {
		return finalReport{}, false
	}
	return report, true
}

func finalReportFromArguments(args map[string]any) finalReport {
	return reportflow.Normalize(finalReport{
		Conclusive:             boolFromAny(args["conclusive"]),
		Conclusion:             stringFromAny(args["conclusion"]),
		Attempted:              stringSliceFromAnyLoose(args["attempted"]),
		EvidenceKnown:          stringSliceFromAnyLoose(args["evidence_known"]),
		EvidenceMissing:        stringSliceFromAnyLoose(args["evidence_missing"]),
		MostLikelyCause:        stringFromAny(args["most_likely_cause"]),
		RecommendedUserActions: stringSliceFromAnyLoose(args["recommended_user_actions"]),
		ProblematicResources:   problematicResourcesFromAny(args["problematic_resources"]),
		Blockers:               stringSliceFromAnyLoose(args["blockers"]),
	})
}

func stringSliceFromAnyLoose(value any) []string {
	switch v := value.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if text := strings.TrimSpace(v); text != "" {
			return []string{text}
		}
	}
	return nil
}

func problematicResourcesFromAny(value any) []problematicResource {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]problematicResource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		res := problematicResource{
			Kind:      stringFromAny(m["kind"]),
			Name:      stringFromAny(m["name"]),
			Namespace: stringFromAny(m["namespace"]),
			Reason:    stringFromAny(m["reason"]),
		}
		if res.Kind == "" && res.Name == "" && res.Namespace == "" && res.Reason == "" {
			continue
		}
		out = append(out, res)
	}
	return out
}

func (l *Loop) emitFinalReportMessage(ctx context.Context, report finalReport) {
	rendered := renderFinalReport(report)
	l.contextApproxTokens += estimateContextTokens(rendered)
	l.lastAssistantText = rendered
	displayText := l.translateModelText(ctx, rendered)
	l.addMessage(api.MessageSourceModel, api.MessageTypeText, displayText)
}

func renderFinalReport(report finalReport) string {
	var b strings.Builder
	b.WriteString("📋 Final report\n")
	if report.Conclusive {
		b.WriteString("Status: conclusive\n")
		if strings.TrimSpace(report.Conclusion) != "" {
			fmt.Fprintf(&b, "\nConclusion:\n%s\n", strings.TrimSpace(report.Conclusion))
		}
	} else {
		b.WriteString("Status: inconclusive — additional diagnosis may be needed\n")
	}
	appendBulletList(&b, "Attempted", report.Attempted)
	appendBulletList(&b, "Evidence known", report.EvidenceKnown)
	appendBulletList(&b, "Evidence missing", report.EvidenceMissing)
	if cause := strings.TrimSpace(report.MostLikelyCause); cause != "" {
		fmt.Fprintf(&b, "\nMost likely cause: %s\n", cause)
	}
	appendProblematicResources(&b, report.ProblematicResources)
	appendBulletList(&b, "Recommended user actions", report.RecommendedUserActions)
	appendBulletList(&b, "Blockers", report.Blockers)
	return strings.TrimRight(b.String(), "\n")
}

func appendProblematicResources(b *strings.Builder, resources []problematicResource) {
	if len(resources) == 0 {
		return
	}
	b.WriteString("\nProblematic resources:\n")
	for _, res := range resources {
		label := resourceLabel(res)
		if label == "" {
			label = "resource"
		}
		if reason := strings.TrimSpace(res.Reason); reason != "" {
			fmt.Fprintf(b, "- %s: %s\n", label, reason)
		} else {
			fmt.Fprintf(b, "- %s\n", label)
		}
	}
}

func resourceLabel(res problematicResource) string {
	var b strings.Builder
	if res.Kind != "" {
		b.WriteString(res.Kind)
	}
	if res.Name != "" {
		if b.Len() > 0 {
			b.WriteString("/")
		}
		b.WriteString(res.Name)
	}
	if res.Namespace != "" {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "(namespace %s)", res.Namespace)
	}
	return strings.TrimSpace(b.String())
}

func appendBulletList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", label)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", item)
	}
}

// requestNextDirectionsFromModel queues the second turn after an inconclusive
// final_report: the model is asked to propose 1–3 next_directions options so
// the user can pick how to continue (or finalize).
func (l *Loop) requestNextDirectionsFromModel(report finalReport) {
	l.transitionControl(RuntimeControlAwaitingNextDirections)
	reportJSON, _ := json.Marshal(report)
	var b strings.Builder
	b.WriteString("The previous final_report was inconclusive. Your next response MUST be a `next_directions` object — do not emit another `action` or `final_report`.\n")
	b.WriteString("Goal: propose 1 to 3 concrete options for how the diagnosis can proceed, so the user can choose. Use the report below as context.\n")
	fmt.Fprintf(&b, "previous_final_report: %s\n", string(reportJSON))
	b.WriteString("Schema for each option:\n")
	b.WriteString("- kind: \"another_guide\" or \"different_approach\".\n")
	b.WriteString("- summary: one short user-facing sentence describing what this option will do.\n")
	b.WriteString("- why: one short sentence describing why it might unblock progress.\n")
	b.WriteString("- For kind=another_guide: include `resource_family` and `problem_focus` so the runtime can resume the phase workflow with that guidance focus; the model must reach guidance_lookup before emitting resource_guide_lookup.\n")
	b.WriteString("- For kind=different_approach: include `instruction`, a short directive describing the alternative diagnostic angle (e.g., inspect related controller status/events, ask the user for workload kubeconfig).\n")
	b.WriteString("Keep options distinct. Do not repeat ideas already exhausted in the previous attempts.")
	l.queueResponseDirective(b.String())
}

func (l *Loop) queueResponseDirective(directive string) {
	directive = strings.TrimSpace(directive)
	if directive == "" {
		return
	}
	if directive == strings.TrimSpace(l.pendingResponseDirective) {
		return
	}
	l.pendingResponseDirective = directive
	l.currChatContent = append(l.currChatContent, directive)
}

// consumeNextDirections handles the model's next_directions response after an
// inconclusive final_report. The proposed options are rendered as a
// UserChoiceRequest with extra "직접 입력" and "여기서 종료" choices, and the
// loop transitions to LoopLifecycleWaitingContinuationChoice until the user picks.
func (l *Loop) consumeNextDirections(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalNextDirectionsCall {
			remaining = append(remaining, call)
			continue
		}
		nd, ok := nextDirectionsFromFunctionCall(call)
		if !ok {
			message := "next_directions payload was invalid. Re-emit a next_directions object with 1-3 options; each option needs `kind` (another_guide|different_approach) and `summary`."
			l.applyGateOutcomeWithRepeatedCorrection(GateOutcome{
				Kind:            GateOutcomeModelOutputCorrection,
				Code:            "invalid_next_directions",
				Retryable:       true,
				RetryScope:      RetryScopeCurrentPhase,
				UserMessage:     "next_directions 형식 오류가 반복되어 기본 선택지를 표시합니다.",
				ModelCorrection: message,
				CorrectionMode:  CorrectionModeAppendCompacted,
				BranchPolicy:    BranchStayCurrent,
			}, func(message string) bool {
				klog.Warning("next_directions remained invalid after correction; falling back to runtime continuation choices")
				nd = l.fallbackNextDirections()
				l.pendingNextDirections = &nd
				l.promptDirectionChoice(nd)
				return true
			})
			return nil, true
		}
		l.pendingNextDirections = &nd
		l.promptDirectionChoice(nd)
		return nil, true
	}
	return remaining, false
}

func (l *Loop) fallbackNextDirections() nextDirections {
	nd := nextDirections{
		Note: "모델이 후속 진단 방향을 올바른 형식으로 제안하지 못해 기본 선택지를 표시합니다.",
	}
	opt := l.genericNextDirectionOption()
	if strings.TrimSpace(opt.Instruction) != "" {
		nd.Options = []nextDirectionOption{opt}
	}
	return nd
}

func (l *Loop) genericNextDirectionOption() nextDirectionOption {
	report := l.pendingFinalReport
	if report == nil {
		return nextDirectionOption{}
	}
	var clues []string
	clues = append(clues, report.Blockers...)
	clues = append(clues, report.EvidenceMissing...)
	if len(clues) == 0 {
		return nextDirectionOption{}
	}
	return nextDirectionOption{
		Kind:        "different_approach",
		Summary:     "부족한 증거를 기준으로 다른 접근을 시도",
		Why:         "이전 진단이 불충분했던 지점을 기준으로 다음 확인 대상을 좁힙니다.",
		Instruction: "Continue diagnosis by addressing these blockers or missing evidence first: " + strings.Join(clues, "; "),
	}
}

func nextDirectionsFromFunctionCall(call gollm.FunctionCall) (nextDirections, bool) {
	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		return nextDirections{}, false
	}
	var nd nextDirections
	if err := json.Unmarshal(raw, &nd); err != nil {
		return nextDirections{}, false
	}
	nd = directionflow.Normalize(nd)
	if len(nd.Options) == 0 {
		return nextDirections{}, false
	}
	return nd, true
}

func (l *Loop) promptDirectionChoice(nd nextDirections) {
	prompt := strings.Builder{}
	prompt.WriteString("진단을 어떻게 계속할지 선택해 주세요.")
	if note := strings.TrimSpace(nd.Note); note != "" {
		prompt.WriteString("\n")
		prompt.WriteString(note)
	}

	var options []api.UserChoiceOption
	promptState := &directionPromptState{}
	for i, opt := range nd.Options {
		label := opt.Summary
		switch opt.Kind {
		case "another_guide":
			if l.pendingFinalReport != nil {
				label = fmt.Sprintf("[추가 추론] %s", opt.Summary)
			} else {
				label = fmt.Sprintf("[가이드 재검색] %s", opt.Summary)
			}
		case "different_approach":
			label = fmt.Sprintf("[다른 접근] %s", opt.Summary)
		case "investigate_resource":
			label = fmt.Sprintf("[리소스 추가 조사] %s", opt.Summary)
		}
		options = append(options, api.UserChoiceOption{
			Value: fmt.Sprintf("option-%d", i+1),
			Label: label,
		})
		promptState.Options = append(promptState.Options, opt)
	}
	promptState.FreeInputIdx = len(options) + 1
	options = append(options, api.UserChoiceOption{Value: "free-input", Label: "직접 다른 방향 입력"})
	promptState.HasFreeInput = true
	promptState.FinalizeIdx = len(options) + 1
	options = append(options, api.UserChoiceOption{Value: "finalize", Label: "여기서 진단 종료"})
	l.pendingDirectionPrompt = promptState
	l.transitionControl(RuntimeControlAwaitingContinuationChoice)
	l.refreshInputOwner()

	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt:  prompt.String(),
		Options: options,
	})
	l.pendingCalls = nil
}

// waitForDirectionChoice is invoked when the loop is in
// LoopLifecycleWaitingContinuationChoice. It reads a single UserChoiceResponse and
// dispatches to the chosen continuation.
func (l *Loop) waitForDirectionChoice(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.applyRuntimeCleanup(cleanupExitPolicy())
			l.transitionControl(RuntimeControlExited)
			return false
		}
		resp, ok := raw.(*api.UserChoiceResponse)
		if !ok {
			return true
		}
		promptState := l.pendingDirectionPrompt
		if promptState == nil {
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return true
		}
		choice := resp.Choice
		// 1-based: first len(promptState.Options) are LLM options, then free-input, then finalize.
		if choice >= 1 && choice <= len(promptState.Options) {
			opt := promptState.Options[choice-1]
			l.applyDirectionOption(opt)
			return true
		}
		if promptState.HasFreeInput && choice == promptState.FreeInputIdx {
			l.pendingDirectionPrompt = nil
			l.transitionControl(RuntimeControlAwaitingContinuationText)
			l.refreshInputOwner()
			l.addMessage(api.MessageSourceAgent, api.MessageTypeUserInputRequest, "어떤 방향으로 계속할지 알려주세요")
			return true
		}
		if choice == promptState.FinalizeIdx {
			l.applyRuntimeCleanup(cleanupDirectionPromptPolicy())
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "진단을 여기서 종료합니다.")
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return true
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, fmt.Sprintf("잘못된 방향 선택: %d", choice))
		l.transitionControl(RuntimeControlAwaitingUserQuery)
		return true
	}
}

// waitForDirectionText handles the user's free-text continuation directive
// after they picked "직접 다른 방향 입력".
func (l *Loop) waitForDirectionText(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			l.applyRuntimeCleanup(cleanupExitPolicy())
			l.transitionControl(RuntimeControlExited)
			return false
		}
		resp, ok := raw.(*api.UserInputResponse)
		if !ok {
			return true
		}
		text := strings.TrimSpace(resp.Query)
		if text == "" {
			l.applyRuntimeCleanup(cleanupDirectionPromptPolicy())
			l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "입력이 비어 있어 진단을 종료합니다.")
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return true
		}
		l.applyDirectionOption(nextDirectionOption{
			Kind:        "different_approach",
			Summary:     "사용자가 직접 지정한 방향",
			Instruction: text,
		})
		return true
	}
}

// applyDirectionOption translates a chosen direction into runtime lifecycle and
// resumes the ReAct loop.
func (l *Loop) applyDirectionOption(opt nextDirectionOption) {
	continuingAfterFinalReport := l.pendingFinalReport != nil
	l.applyRuntimeCleanup(cleanupDirectionPromptPolicy())

	switch opt.Kind {
	case "another_guide":
		l.continueWithGuideFocus(opt)
		return
	case "different_approach":
		if continuingAfterFinalReport {
			l.continueAfterFinalReport(opt)
			return
		}
		// Inject the user-approved instruction as a user message and resume.
		var b strings.Builder
		b.WriteString("Continuation directive selected by the user. Continue diagnosis under this directive instead of repeating the exhausted guide.\n")
		fmt.Fprintf(&b, "directive_summary: %s\n", opt.Summary)
		if opt.Why != "" {
			fmt.Fprintf(&b, "rationale: %s\n", opt.Why)
		}
		fmt.Fprintf(&b, "directive: %s\n", opt.Instruction)
		b.WriteString("Treat this as the active goal alongside the original_query. Choose the single next action that advances it.")
		l.currChatContent = append(l.currChatContent, b.String())
		l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("선택한 방향으로 진단을 계속합니다: %s", opt.Summary))
		l.pendingCalls = nil
		l.currIteration++
		l.transitionControl(RuntimeControlAwaitingModelStep)
		return
	case "investigate_resource":
		l.startResourceInvestigation(opt)
		return
	}
	l.transitionControl(RuntimeControlAwaitingUserQuery)
}

func (l *Loop) continueAfterFinalReport(opt nextDirectionOption) {
	l.continueWithGuideFocus(opt)
}

func (l *Loop) continueWithGuideFocus(opt nextDirectionOption) {
	l.guideStepState = nil
	l.resourceGuideInjected = false
	l.rewindPhaseBeforeGuidance()

	var b strings.Builder
	b.WriteString("The user chose to continue the diagnosis. Resume from the appropriate diagnostic phase for this continuation. Guidance lookup is allowed when the accepted phase plan reaches guidance_lookup and runtime discovery confirms CRD eligibility.\n")
	fmt.Fprintf(&b, "continuation_summary: %s\n", opt.Summary)
	if opt.Why != "" {
		fmt.Fprintf(&b, "rationale: %s\n", opt.Why)
	}
	if opt.ResourceFamily != "" {
		fmt.Fprintf(&b, "requested_resource_family_focus: %s\n", opt.ResourceFamily)
	}
	if opt.ProblemFocus != "" {
		fmt.Fprintf(&b, "requested_problem_focus: %s\n", opt.ProblemFocus)
	}
	if opt.Kind == "another_guide" {
		b.WriteString("Use the requested focus as a continuation angle. Do not inject or assume guide steps directly; if guidance is useful, reach the declared guidance_lookup phase and emit resource_guide_lookup there. guidance_step entries are valid only inside a declared guided_diagnosis phase after a guide result is observed.\n")
	} else if opt.Instruction != "" {
		fmt.Fprintf(&b, "directive: %s\n", opt.Instruction)
	}
	b.WriteString("Choose the next response according to the active phase_plan: continue the pre-guidance phase with one valid action, complete it with phase_progress, or produce a final_report when enough evidence exists.")
	l.currChatContent = append(l.currChatContent, b.String())
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("선택한 방향으로 추가 추론을 계속합니다: %s", opt.Summary))
	l.pendingCalls = nil
	l.currIteration++
	l.transitionControl(RuntimeControlAwaitingModelStep)
}

func (l *Loop) promptProblematicResourceInvestigation(report finalReport) bool {
	options := l.investigationOptionsFromReport(report)
	if len(options) == 0 {
		return false
	}
	var choices []api.UserChoiceOption
	promptState := &directionPromptState{}
	for i, opt := range options {
		choices = append(choices, api.UserChoiceOption{
			Value: fmt.Sprintf("option-%d", i+1),
			Label: fmt.Sprintf("[리소스 추가 조사] %s", opt.Summary),
		})
		promptState.Options = append(promptState.Options, opt)
	}
	promptState.FinalizeIdx = len(choices) + 1
	choices = append(choices, api.UserChoiceOption{Value: "no", Label: "여기서 종료"})
	l.pendingDirectionPrompt = promptState
	l.transitionControl(RuntimeControlAwaitingContinuationChoice)
	l.refreshInputOwner()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt:  "문제가 있는 관련 리소스가 확인되었습니다. 추가 조사할 리소스를 선택해 주세요.",
		Options: choices,
	})
	l.pendingCalls = nil
	return true
}

func (l *Loop) investigationOptionsFromReport(report finalReport) []nextDirectionOption {
	var options []nextDirectionOption
	for _, res := range report.ProblematicResources {
		kind := strings.TrimSpace(res.Kind)
		name := strings.TrimSpace(res.Name)
		if kind == "" && name == "" {
			continue
		}
		if l.problematicResourceIsPrimarySymptom(res) {
			continue
		}
		summary := resourceLabel(res)
		if summary == "" {
			summary = "문제 리소스 추가 조사"
		}
		options = append(options, directionflow.InvestigationOption(res, summary))
		if len(options) == 3 {
			break
		}
	}
	return options
}

func (l *Loop) problematicResourceIsPrimarySymptom(res problematicResource) bool {
	if l.requestContext == nil {
		return false
	}
	primary := l.requestContext.PrimaryTarget
	if primary.Resource == "" || primary.Name == "" {
		return false
	}
	if !resourceNamesEquivalent(res.Kind, primary.Resource) || !strings.EqualFold(strings.TrimSpace(res.Name), strings.TrimSpace(primary.Name)) {
		return false
	}
	if l.requestContext.Scope.Namespace != "" && res.Namespace != "" && !strings.EqualFold(strings.TrimSpace(res.Namespace), strings.TrimSpace(l.requestContext.Scope.Namespace)) {
		return false
	}
	reason := strings.ToLower(strings.TrimSpace(res.Reason))
	if strings.Contains(reason, "spec") || strings.Contains(reason, "metadata") || strings.Contains(reason, "configuration") || strings.Contains(reason, "misconfig") || strings.Contains(reason, "설정") || strings.Contains(reason, "구성") {
		return false
	}
	return true
}

func (l *Loop) startResourceInvestigation(opt nextDirectionOption) {
	var b strings.Builder
	if opt.Namespace != "" {
		fmt.Fprintf(&b, "네임스페이스 %s에서 ", opt.Namespace)
	}
	if opt.ResourceName != "" {
		fmt.Fprintf(&b, "%s ", opt.ResourceName)
	}
	if opt.ResourceKind != "" {
		fmt.Fprintf(&b, "%s 리소스가 ", opt.ResourceKind)
	} else {
		b.WriteString("해당 리소스가 ")
	}
	b.WriteString("왜 문제인지 추가로 진단해줘.")
	if opt.Why != "" {
		fmt.Fprintf(&b, " 이전 진단에서 문제 근거는 다음과 같아: %s", opt.Why)
	}
	query := strings.TrimSpace(b.String())
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("문제 리소스를 추가 조사합니다: %s", opt.Summary))
	if err := l.startQuery(query); err != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		l.transitionControl(RuntimeControlAwaitingUserQuery)
	}
}

type reActResponse struct {
	Thought                    string                      `json:"thought"`
	Answer                     string                      `json:"answer,omitempty"`
	RequirementAnalysis        *requirementAnalysis        `json:"requirement_analysis,omitempty"`
	RequestContext             *requestContext             `json:"request_context,omitempty"`
	PhasePlan                  *phasePlan                  `json:"phase_plan,omitempty"`
	PhaseProgress              *phaseProgress              `json:"phase_progress,omitempty"`
	Action                     *action                     `json:"action,omitempty"`
	GuideProgress              *guideProgress              `json:"guide_progress,omitempty"`
	ResourceGuideLookup        *resourceGuideLookup        `json:"resource_guide_lookup,omitempty"`
	FinalReport                *finalReport                `json:"final_report,omitempty"`
	NextDirections             *nextDirections             `json:"next_directions,omitempty"`
	MutationVerificationResult *mutationVerificationResult `json:"mutation_verification_result,omitempty"`
	InvalidPhaseProgress       bool                        `json:"-"`
	InvalidAction              bool                        `json:"-"`
	InvalidResourceGuideLookup bool                        `json:"-"`
	InvalidFinalReport         bool                        `json:"-"`
	InvalidDirections          bool                        `json:"-"`
	InvalidStructuredAnswer    bool                        `json:"-"`
}

type requirementAnalysis = contract.RequirementAnalysis
type requirementAnalysisTarget = contract.RequirementAnalysisTarget
type requirementScope = contract.RequirementScope
type requirementResource = contract.RequirementResource
type requirementOperationalFocus = contract.RequirementOperationalFocus
type requirementRelatedResource = contract.RequirementRelatedResource
type requestContext = contract.RequestContext
type requestPrimaryTarget = contract.RequestPrimaryTarget
type requestScope = contract.RequestScope
type action = contract.Action
type actionTarget = contract.ActionTarget
type guideProgress = contract.GuideProgress
type phasePlan = contract.PhasePlan
type phaseStep = contract.PhaseStep
type phaseExecutionStep = contract.PhaseExecutionStep
type phaseProgress = contract.PhaseProgress
type resourceGuideLookup = contract.ResourceGuideLookup
type finalReport = contract.FinalReport
type problematicResource = contract.ProblematicResource
type nextDirections = contract.NextDirections
type nextDirectionOption = contract.NextDirectionOption
type mutationVerificationResult = contract.MutationVerificationResult

func candidateToShimCandidate(iterator gollm.ChatResponseIterator) (gollm.ChatResponseIterator, error) {
	return func(yield func(gollm.ChatResponse, error) bool) {
		var buffer strings.Builder
		for response, err := range iterator {
			if err != nil {
				yield(nil, err)
				return
			}
			if response == nil {
				break
			}
			if len(response.Candidates()) == 0 {
				yield(nil, fmt.Errorf("no candidates in LLM response"))
				return
			}
			for _, part := range response.Candidates()[0].Parts() {
				text, ok := part.AsText()
				if !ok {
					yield(nil, fmt.Errorf("shim mode expects text-only LLM response"))
					return
				}
				buffer.WriteString(text)
			}
		}

		if strings.TrimSpace(buffer.String()) == "" {
			yield(nil, nil)
			return
		}

		parsed, err := parseReActResponse(buffer.String())
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&shimResponse{candidate: parsed}, nil)
	}, nil
}

func parseReActResponse(input string) (*reActResponse, error) {
	cleaned, found := extractJSON(input)
	if !found {
		if strings.TrimSpace(input) == "" {
			return nil, fmt.Errorf("empty shim response")
		}
		return nil, fmt.Errorf("shim response missing json code block")
	}
	cleaned = repairUnescapedQuotesInJSONStrings(strings.TrimSpace(cleaned))

	parsed, err := unmarshalReActResponse([]byte(cleaned))
	if err != nil {
		return nil, fmt.Errorf("parsing shim JSON %q: %w", cleaned, err)
	}
	return parsed, nil
}

func unmarshalReActResponse(data []byte) (*reActResponse, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	parsed := &reActResponse{}
	if err := unmarshalOptional(raw, "thought", &parsed.Thought); err != nil {
		return nil, err
	}
	if err := unmarshalOptional(raw, "answer", &parsed.Answer); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPointer(raw, "requirement_analysis", &parsed.RequirementAnalysis); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPointer(raw, "request_context", &parsed.RequestContext); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalPhasePlan(raw, &parsed.PhasePlan); err != nil {
		return nil, err
	}
	unmarshalOptionalPointerOrInvalid(raw, "phase_progress", &parsed.PhaseProgress, &parsed.InvalidPhaseProgress)
	unmarshalOptionalPointerOrInvalid(raw, "action", &parsed.Action, &parsed.InvalidAction)
	if err := unmarshalOptionalPointer(raw, "guide_progress", &parsed.GuideProgress); err != nil {
		return nil, err
	}
	if parsed.Action != nil && parsed.Action.GuideProgress == nil && parsed.GuideProgress != nil {
		parsed.Action.GuideProgress = parsed.GuideProgress
	}
	unmarshalOptionalPointerOrInvalid(raw, "resource_guide_lookup", &parsed.ResourceGuideLookup, &parsed.InvalidResourceGuideLookup)
	if rawFinal, ok := raw["final_report"]; ok && string(rawFinal) != "null" {
		var reportArgs map[string]any
		if err := json.Unmarshal(rawFinal, &reportArgs); err != nil {
			parsed.InvalidFinalReport = true
		} else {
			report := finalReportFromArguments(reportArgs)
			parsed.FinalReport = &report
		}
	}
	if rawDirections, ok := raw["next_directions"]; ok && string(rawDirections) != "null" {
		var directions nextDirections
		if err := json.Unmarshal(rawDirections, &directions); err != nil {
			parsed.InvalidDirections = true
		} else {
			parsed.NextDirections = &directions
		}
	}
	if err := unmarshalOptionalPointer(raw, "mutation_verification_result", &parsed.MutationVerificationResult); err != nil {
		return nil, err
	}
	if parsed.Answer != "" && parsed.hasStructuredOutput() {
		parsed.InvalidStructuredAnswer = true
	}
	return parsed, nil
}

func unmarshalOptional(raw map[string]json.RawMessage, key string, target any) error {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return nil
	}
	return json.Unmarshal(value, target)
}

func unmarshalOptionalPointer[T any](raw map[string]json.RawMessage, key string, target **T) error {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return nil
	}
	var parsed T
	if err := json.Unmarshal(value, &parsed); err != nil {
		return err
	}
	*target = &parsed
	return nil
}

func unmarshalOptionalPointerOrInvalid[T any](raw map[string]json.RawMessage, key string, target **T, invalid *bool) {
	value, ok := raw[key]
	if !ok || string(value) == "null" {
		return
	}
	var parsed T
	if err := json.Unmarshal(value, &parsed); err != nil {
		*invalid = true
		return
	}
	*target = &parsed
}

func unmarshalOptionalPhasePlan(raw map[string]json.RawMessage, target **phasePlan) error {
	value, ok := raw["phase_plan"]
	if !ok || string(value) == "null" {
		return nil
	}
	var parsed phasePlan
	if err := json.Unmarshal(value, &parsed); err == nil {
		if len(parsed.PhaseSteps) > 0 {
			*target = &parsed
			return nil
		}
	}
	var compat struct {
		RequestGoal       string      `json:"request_goal"`
		CurrentPhaseIndex int         `json:"current_phase_index,omitempty"`
		Phases            []phaseStep `json:"phases,omitempty"`
	}
	if err := json.Unmarshal(value, &compat); err != nil {
		return err
	}
	parsed = phasePlan{
		RequestGoal:       compat.RequestGoal,
		CurrentPhaseIndex: compat.CurrentPhaseIndex,
		PhaseSteps:        compat.Phases,
	}
	*target = &parsed
	return nil
}

func extractJSON(input string) (string, bool) {
	return protocol.ExtractJSON(input)
}

func repairUnescapedQuotesInJSONStrings(input string) string {
	return protocol.RepairJSONStrings(input)
}

type shimResponse struct {
	candidate *reActResponse
}

func (r *shimResponse) UsageMetadata() any {
	return nil
}

func (r *shimResponse) Candidates() []gollm.Candidate {
	return []gollm.Candidate{&shimCandidate{candidate: r.candidate}}
}

type shimCandidate struct {
	candidate *reActResponse
}

func (c *shimCandidate) String() string {
	return fmt.Sprintf("Thought: %s\nAnswer: %s\nRequirementAnalysis: %v\nRequestContext: %v\nPhasePlan: %v\nPhaseProgress: %v\nAction: %v\nGuideProgress: %v\nResourceGuideLookup: %v\nFinalReport: %v\nInvalidFinalReport: %v\nNextDirections: %v\nInvalidDirections: %v", c.candidate.Thought, c.candidate.Answer, c.candidate.RequirementAnalysis, c.candidate.RequestContext, c.candidate.PhasePlan, c.candidate.PhaseProgress, c.candidate.Action, c.candidate.GuideProgress, c.candidate.ResourceGuideLookup, c.candidate.FinalReport, c.candidate.InvalidFinalReport, c.candidate.NextDirections, c.candidate.InvalidDirections)
}

func (c *shimCandidate) Parts() []gollm.Part {
	var parts []gollm.Part
	structured := c.hasStructuredOutput()
	if c.candidate.Thought != "" && (structured || c.candidate.Answer == "") {
		parts = append(parts, &shimPart{text: c.candidate.Thought})
	}
	if c.candidate.Answer != "" && !structured {
		parts = append(parts, &shimPart{text: c.candidate.Answer})
	}
	if c.candidate.RequirementAnalysis != nil {
		parts = append(parts, &shimPart{requirementAnalysis: c.candidate.RequirementAnalysis})
	}
	if c.candidate.RequestContext != nil {
		parts = append(parts, &shimPart{requestContext: c.candidate.RequestContext})
	}
	if c.candidate.PhasePlan != nil {
		parts = append(parts, &shimPart{phasePlan: c.candidate.PhasePlan})
	}
	if c.candidate.PhaseProgress != nil {
		parts = append(parts, &shimPart{phaseProgress: c.candidate.PhaseProgress})
	} else if c.candidate.InvalidPhaseProgress {
		parts = append(parts, &shimPart{invalidPhaseProgress: true})
	}
	if c.candidate.Action != nil {
		parts = append(parts, &shimPart{action: c.candidate.Action})
	} else if c.candidate.InvalidAction {
		parts = append(parts, &shimPart{invalidAction: true})
	}
	if c.candidate.GuideProgress != nil && (c.candidate.Action == nil || c.candidate.Action.GuideProgress == nil) {
		parts = append(parts, &shimPart{guideProgress: c.candidate.GuideProgress})
	}
	if c.candidate.ResourceGuideLookup != nil {
		parts = append(parts, &shimPart{resourceGuideLookup: c.candidate.ResourceGuideLookup})
	} else if c.candidate.InvalidResourceGuideLookup {
		parts = append(parts, &shimPart{invalidResourceGuideLookup: true})
	}
	if c.candidate.FinalReport != nil {
		parts = append(parts, &shimPart{finalReport: c.candidate.FinalReport})
	} else if c.candidate.InvalidFinalReport {
		parts = append(parts, &shimPart{invalidFinalReport: true})
	}
	if c.candidate.NextDirections != nil {
		parts = append(parts, &shimPart{nextDirections: c.candidate.NextDirections})
	} else if c.candidate.InvalidDirections {
		parts = append(parts, &shimPart{invalidDirections: true})
	}
	if c.candidate.MutationVerificationResult != nil {
		parts = append(parts, &shimPart{mutationVerificationResult: c.candidate.MutationVerificationResult})
	}
	if c.candidate.InvalidStructuredAnswer {
		parts = append(parts, &shimPart{invalidStructuredAnswer: true})
	}
	return parts
}

func functionCallsFromParsedReActResponse(parsed *reActResponse) []gollm.FunctionCall {
	if parsed == nil {
		return nil
	}
	candidate := &shimCandidate{candidate: parsed}
	var calls []gollm.FunctionCall
	for _, part := range candidate.Parts() {
		if partCalls, ok := part.AsFunctionCalls(); ok {
			calls = append(calls, partCalls...)
		}
	}
	return calls
}

func (c *shimCandidate) hasStructuredOutput() bool {
	return c.candidate.hasStructuredOutput()
}

func (c *reActResponse) hasStructuredOutput() bool {
	return c.RequirementAnalysis != nil ||
		c.RequestContext != nil ||
		c.PhasePlan != nil ||
		c.PhaseProgress != nil ||
		c.InvalidPhaseProgress ||
		c.Action != nil ||
		c.InvalidAction ||
		c.GuideProgress != nil ||
		c.ResourceGuideLookup != nil ||
		c.InvalidResourceGuideLookup ||
		c.FinalReport != nil ||
		c.InvalidFinalReport ||
		c.NextDirections != nil ||
		c.InvalidDirections ||
		c.MutationVerificationResult != nil
}

type shimPart struct {
	text                       string
	requirementAnalysis        *requirementAnalysis
	requestContext             *requestContext
	phasePlan                  *phasePlan
	phaseProgress              *phaseProgress
	invalidPhaseProgress       bool
	action                     *action
	invalidAction              bool
	guideProgress              *guideProgress
	resourceGuideLookup        *resourceGuideLookup
	invalidResourceGuideLookup bool
	finalReport                *finalReport
	invalidFinalReport         bool
	nextDirections             *nextDirections
	invalidDirections          bool
	mutationVerificationResult *mutationVerificationResult
	invalidStructuredAnswer    bool
}

func (p *shimPart) AsText() (string, bool) {
	return p.text, p.text != ""
}

func (p *shimPart) AsFunctionCalls() ([]gollm.FunctionCall, bool) {
	if p.requirementAnalysis != nil {
		args, err := toMap(p.requirementAnalysis)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalRequirementAnalysisCall,
			Arguments: args,
		}}, true
	}
	if p.requestContext != nil {
		args, err := toMap(p.requestContext)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalRequestContextCall,
			Arguments: args,
		}}, true
	}
	if p.phasePlan != nil {
		args, err := toMap(p.phasePlan)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalPhasePlanCall,
			Arguments: args,
		}}, true
	}
	if p.phaseProgress != nil {
		args, err := toMap(p.phaseProgress)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalPhaseProgressCall,
			Arguments: args,
		}}, true
	}
	if p.invalidPhaseProgress {
		return []gollm.FunctionCall{{
			Name:      internalPhaseProgressCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.guideProgress != nil {
		args, err := toMap(p.guideProgress)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalGuideProgressCall,
			Arguments: args,
		}}, true
	}
	if p.resourceGuideLookup != nil {
		args, err := toMap(p.resourceGuideLookup)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalResourceGuideLookupCall,
			Arguments: args,
		}}, true
	}
	if p.invalidResourceGuideLookup {
		return []gollm.FunctionCall{{
			Name:      internalResourceGuideLookupCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.finalReport != nil {
		args, err := toMap(p.finalReport)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalFinalReportCall,
			Arguments: args,
		}}, true
	}
	if p.invalidFinalReport {
		return []gollm.FunctionCall{{
			Name:      internalFinalReportCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.nextDirections != nil {
		args, err := toMap(p.nextDirections)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalNextDirectionsCall,
			Arguments: args,
		}}, true
	}
	if p.mutationVerificationResult != nil {
		args, err := toMap(p.mutationVerificationResult)
		if err != nil {
			return nil, false
		}
		return []gollm.FunctionCall{{
			Name:      internalMutationVerificationResultCall,
			Arguments: args,
		}}, true
	}
	if p.invalidDirections {
		return []gollm.FunctionCall{{
			Name:      internalNextDirectionsCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.invalidStructuredAnswer {
		return []gollm.FunctionCall{{
			Name:      internalInvalidStructuredOutputCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.invalidAction {
		return []gollm.FunctionCall{{
			Name:      internalInvalidActionCall,
			Arguments: map[string]any{},
		}}, true
	}
	if p.action == nil {
		return nil, false
	}
	args, err := toMap(p.action)
	if err != nil {
		return nil, false
	}
	delete(args, "name")
	return []gollm.FunctionCall{{
		Name:      p.action.Name,
		Arguments: args,
	}}, true
}

func toMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("converting %T to json: %w", v, err)
	}
	result := make(map[string]any)
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("converting json to map: %w", err)
	}
	return result, nil
}

const (
	internalResourceGuideLookupCall        = protocol.ResourceGuideLookupCall
	internalRequestContextCall             = protocol.RequestContextCall
	internalRequirementAnalysisCall        = protocol.RequirementAnalysisCall
	internalPhasePlanCall                  = protocol.PhasePlanCall
	internalPhaseProgressCall              = protocol.PhaseProgressCall
	internalGuideProgressCall              = protocol.GuideProgressCall
	internalFinalReportCall                = protocol.FinalReportCall
	internalNextDirectionsCall             = protocol.NextDirectionsCall
	internalMutationVerificationResultCall = protocol.MutationVerificationResultCall
	internalInvalidActionCall              = protocol.InvalidActionCall
	internalInvalidStructuredOutputCall    = protocol.InvalidStructuredOutputCall
)

func onlyFunctionCall(calls []gollm.FunctionCall, name string) bool {
	return protocol.OnlyFunctionCall(calls, name)
}

func isRuntimeInternalCall(name string) bool {
	return protocol.IsRuntimeInternalCall(name)
}

func normalizeAssistantStructuredFunctionCalls(calls []gollm.FunctionCall) []gollm.FunctionCall {
	return protocol.NormalizeAssistantStructuredFunctionCalls(calls)
}

type toolFailureClass string

const (
	toolFailureUnknown       toolFailureClass = "unknown"
	toolFailureCommandSyntax toolFailureClass = "command_syntax"
	toolFailureRBAC          toolFailureClass = "rbac_forbidden"
	toolFailureNotFound      toolFailureClass = "resource_not_found"
	toolFailureTimeout       toolFailureClass = "timeout_or_api_unavailable"
	toolFailurePartial       toolFailureClass = "partial_success"
)

func (l *Loop) annotateToolFailureResult(call PendingCall, result map[string]any) (GateOutcome, bool) {
	if result == nil || toolResultSucceeded(result) {
		return GateOutcome{}, false
	}
	detail := toolFailureDetail(result)
	class := classifyToolFailure(detail)
	if isPartialToolResult(result) {
		class = toolFailurePartial
	}
	retryable, scope := toolFailureRetryPolicy(class)
	branch := BranchRetryStep
	if !retryable {
		branch = BranchBlockUserRequest
	}
	result["failure_class"] = string(class)
	result["retryable"] = retryable
	result["retry_scope"] = string(scope)
	result["suggested_response"] = toolFailureSuggestedResponse(class)
	command, _ := commandString(call.FunctionCall.Arguments["command"])
	correction := toolFailureCorrection(class, call.FunctionCall.Name, command, detail)
	return GateOutcome{
		Kind:            GateOutcomeToolExecutionFailure,
		Code:            "tool_execution_" + string(class),
		Retryable:       retryable,
		RetryScope:      scope,
		ModelCorrection: correction,
		CorrectionMode:  CorrectionModeAppendCompacted,
		BranchPolicy:    branch,
	}, true
}

func toolFailureResultFromError(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"status": "error",
		"error":  err.Error(),
	}
}

func toolFailureResultFromMapError(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"status": "error",
		"error":  "tool returned an unparseable result: " + err.Error(),
	}
}

func toolFailureDetail(result map[string]any) string {
	var parts []string
	for _, key := range []string{"error", "errors", "stderr", "message", "status", "reason"} {
		value := strings.TrimSpace(stringFromAny(result[key]))
		if value != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
	}
	if len(parts) == 0 {
		return "tool returned a failed status without detailed error text"
	}
	return strings.Join(parts, "; ")
}

func classifyToolFailure(detail string) toolFailureClass {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "rbac"):
		return toolFailureRBAC
	case strings.Contains(lower, "command not found") ||
		strings.Contains(lower, "executable file not found"):
		return toolFailureCommandSyntax
	case strings.Contains(lower, "not found") ||
		strings.Contains(lower, "notfound"):
		return toolFailureNotFound
	case strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "server is currently unable"):
		return toolFailureTimeout
	case strings.Contains(lower, "unknown flag") ||
		strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "invalid argument") ||
		strings.Contains(lower, "requires exactly") ||
		strings.Contains(lower, "usage:"):
		return toolFailureCommandSyntax
	default:
		return toolFailureUnknown
	}
}

func toolFailureRetryPolicy(class toolFailureClass) (bool, RetryScope) {
	switch class {
	case toolFailureRBAC:
		return false, RetryScopeUserRequest
	case toolFailureTimeout:
		return true, RetryScopeExternalState
	case toolFailureNotFound:
		return true, RetryScopeCurrentPhase
	case toolFailurePartial:
		return true, RetryScopeCurrentStep
	case toolFailureCommandSyntax, toolFailureUnknown:
		return true, RetryScopeAgentCommand
	default:
		return true, RetryScopeAgentCommand
	}
}

func toolFailureSuggestedResponse(class toolFailureClass) string {
	switch class {
	case toolFailureCommandSyntax:
		return "Retry with a corrected non-interactive command that observes the same evidence."
	case toolFailureRBAC:
		return "Do not repeat the same forbidden command. Use alternative permitted evidence if possible, or report the permission blocker when appropriate."
	case toolFailureNotFound:
		return "Recheck the target name, namespace, and resource kind before retrying or asking for clarification."
	case toolFailureTimeout:
		return "Retry with a narrower read-only observation or continue with an external-state wait/recheck path."
	case toolFailurePartial:
		return "Preserve the successful evidence, then collect only the missing or failed evidence before reporting completion."
	default:
		return "Choose a safer alternative diagnostic command or explain the blocker only when the active phase allows reporting."
	}
}

func isPartialToolResult(result map[string]any) bool {
	if result == nil {
		return false
	}
	if boolFromAny(result["partial_success"]) || boolFromAny(result["partial"]) {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(stringFromAny(result["status"])))
	if status == "partial" || status == "partial_success" || status == "partially_succeeded" {
		return true
	}
	if !hasNonEmptyToolErrors(result["errors"]) {
		return false
	}
	return hasSuccessfulPayload(result)
}

func hasNonEmptyToolErrors(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return strings.TrimSpace(stringFromAny(value)) != ""
	}
}

func hasSuccessfulPayload(result map[string]any) bool {
	for _, key := range []string{"items", "resources", "results", "stdout", "data", "output"} {
		value, ok := result[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(typed) != "" {
				return true
			}
		case []any:
			if len(typed) > 0 {
				return true
			}
		case []string:
			if len(typed) > 0 {
				return true
			}
		case map[string]any:
			if len(typed) > 0 {
				return true
			}
		default:
			if strings.TrimSpace(stringFromAny(value)) != "" {
				return true
			}
		}
	}
	return false
}

func toolFailureCorrection(class toolFailureClass, toolName, command, detail string) string {
	var b strings.Builder
	b.WriteString("The previous tool observation indicates tool_execution_failure.")
	b.WriteString(" failure_class=")
	b.WriteString(string(class))
	if strings.TrimSpace(toolName) != "" {
		b.WriteString(" tool=")
		b.WriteString(strings.TrimSpace(toolName))
	}
	if strings.TrimSpace(command) != "" {
		b.WriteString(" command=")
		b.WriteString(strconvQuote(command))
	}
	if strings.TrimSpace(detail) != "" {
		b.WriteString(" detail=")
		b.WriteString(strconvQuote(detail))
	}
	b.WriteString(" ")
	b.WriteString(toolFailureSuggestedResponse(class))
	if retryable, _ := toolFailureRetryPolicy(class); retryable {
		b.WriteString(" Do not return final_report solely because a retryable tool failure occurred; continue the active phase with corrected evidence or phase_progress when the phase completion condition is met.")
	} else {
		b.WriteString(" Do not repeat the blocked operation. If no permitted alternative evidence is available, report the blocker only when the active phase allows reporting.")
	}
	return b.String()
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", strings.TrimSpace(value))
}
