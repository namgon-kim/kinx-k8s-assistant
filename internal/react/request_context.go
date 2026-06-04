package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
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
				message := "Requirement analysis was invalid. Return only one corrected requirement_analysis object before choosing any action. Identify request_type, action, target.category, target.description, scope.type, resource_candidates, evidence_needs, constraints, and ambiguities. Do not classify broad cluster/environment requests as Kubernetes Cluster resources unless the user names a concrete Cluster object."
				if !l.appendCorrectionWithCompaction("invalid_requirement_analysis", message) {
					l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 requirement analysis 오류로 루프를 중단했습니다:\n"+message)
					l.pendingCalls = nil
					l.currIteration = 0
					l.state = StateDone
					return nil, true
				}
				l.currIteration++
				l.state = StateRunning
				return nil, true
			}
			l.requirementAnalysis = &analysis
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
				if l.shouldRunInitialResourceGuideLookup(request, classification) {
					l.initialGuideAttempted = true
					resource := normalizeKubectlResource(request.PrimaryTarget.Resource)
					l.searchAndInjectResourceGuide(ctx, resource, l.initialResourceGuideQuery(request))
					return nil, true
				}
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
			if !l.appendCorrectionWithCompaction("invalid_request_context", message) {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 request context 오류로 루프를 중단했습니다:\n"+message)
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return nil, true
			}
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		l.requestContext = &request
		classification := l.classifyResourceByDiscovery(ctx, request.PrimaryTarget.Resource)
		l.resourceClassification = &classification
		if l.shouldRunInitialResourceGuideLookup(request, classification) {
			l.initialGuideAttempted = true
			resource := normalizeKubectlResource(request.PrimaryTarget.Resource)
			l.searchAndInjectResourceGuide(ctx, resource, l.initialResourceGuideQuery(request))
			return nil, true
		}
	}
	return remaining, false
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
			Name:        strings.TrimSpace(name),
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
		resource := requirementResource{
			Kind:      strings.ToLower(strings.TrimSpace(kind)),
			Name:      strings.TrimSpace(name),
			Namespace: strings.TrimSpace(namespace),
			Role:      normalizeRequirementResourceRole(role),
		}
		if resource.Kind != "" || resource.Name != "" {
			analysis.Resources = append(analysis.Resources, resource)
		}
	}
	if scopeRaw, ok := call.Arguments["scope"].(map[string]any); ok {
		scopeType, _ := scopeRaw["type"].(string)
		namespace, _ := scopeRaw["namespace"].(string)
		analysis.Scope.Type = strings.ToLower(strings.TrimSpace(scopeType))
		analysis.Scope.Namespace = strings.TrimSpace(namespace)
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

func requirementAnalysisToArguments(analysis *requirementAnalysis) map[string]any {
	if analysis == nil {
		return nil
	}
	args, err := toMap(analysis)
	if err != nil {
		return nil
	}
	return args
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
	message := "The first response for this user request must be only a requirement_analysis object. Identify request_type, action, target.category, target.description, scope.type, resource_candidates, evidence_needs, constraints, and ambiguities before selecting any tool, resource guide lookup, or final answer. Do not turn a broad cluster/environment target into a Kubernetes Cluster resource unless the user names a concrete Cluster object."
	if !l.appendCorrectionWithCompaction("missing_requirement_analysis", message) {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 requirement analysis 누락으로 루프를 중단했습니다:\n"+message)
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		return true
	}
	l.pendingCalls = nil
	l.currIteration++
	l.state = StateRunning
	return true
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
		Name:     strings.TrimSpace(name),
	}
	if scopeRaw, ok := call.Arguments["scope"].(map[string]any); ok {
		namespace, _ := scopeRaw["namespace"].(string)
		request.Scope.Namespace = strings.TrimSpace(namespace)
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

func (l *Loop) shouldRunInitialResourceGuideLookup(request requestContext, classification resourceClassification) bool {
	if l.initialGuideAttempted {
		return false
	}
	resource := normalizeKubectlResource(strings.ToLower(request.PrimaryTarget.Resource))
	if resource == "" || isBuiltinKubernetesResource(resource) {
		return false
	}
	return classification.Kind == resourceClassificationCRD
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
	return fmt.Sprintf("Accepted requirement_analysis:\n%s\nUse this analysis as the request classification. If resource_candidates is empty, do not invent a Kubernetes resource target. Continue by choosing exactly one next step: resource-guide lookup when runtime injected one or a refined lookup is needed, a kubectl/tool diagnostic, or a final answer.", string(raw))
}

// guideStepAnchor returns a compact reaffirmation of the active resource
// guide so the model keeps following it across many iterations of tool
// observations. The full guide body is injected only once via
// appendGuideObservation; this anchor re-emits just enough state for the
// model to know which step it is on and what is left to do.
func (l *Loop) guideStepAnchor() string {
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
		if next.ExpectedOutcome != "" {
			fmt.Fprintf(&b, "next_step_expected_outcome: %s\n", next.ExpectedOutcome)
		}
		if len(next.Preconditions) > 0 {
			fmt.Fprintf(&b, "next_step_preconditions: %s\n", strings.Join(next.Preconditions, " | "))
		}
	}
	b.WriteString("Rules:\n")
	b.WriteString("- For each action, set `guide_progress.step_completed` to the 1-based step index this action advances, and `guide_progress.evidence_useful` to whether the observation moved diagnosis forward.\n")
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

func (l *Loop) initialResourceGuideQuery(request requestContext) string {
	lines := []string{
		l.originalQuery,
		"primary target resource: " + request.PrimaryTarget.Resource,
	}
	if l.resourceClassification != nil {
		lines = append(lines,
			"resource classification: "+l.resourceClassification.Kind,
			"classification source: "+l.resourceClassification.Source,
		)
		if l.resourceClassification.APIGroup != "" {
			lines = append(lines, "api group: "+l.resourceClassification.APIGroup)
		}
	}
	if request.PrimaryTarget.Name != "" {
		lines = append(lines, "primary target name: "+request.PrimaryTarget.Name)
	}
	if request.Scope.Namespace != "" {
		lines = append(lines, "scope namespace: "+request.Scope.Namespace)
	}
	return strings.Join(lines, "\n")
}
