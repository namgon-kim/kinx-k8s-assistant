package react

import (
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

type pendingMutationVerification struct {
	MutationStep    int
	MutationCommand string
	Requirements    []mutationEvidenceRequirement
	Satisfied       map[string]bool
	AwaitingResult  bool
}

type mutationEvidenceRequirement struct {
	ID               string
	Kind             string
	Target           actionTarget
	Purpose          string
	SuggestedCommand string
}

func (l *Loop) mutationVerificationAnchor() string {
	if l.pendingMutationVerification == nil {
		return ""
	}
	return "Active mutation verification obligation:\n" + l.pendingMutationVerification.requiredMessage()
}

func (l *Loop) rejectPlainAnswerDuringMutationVerification(text string) bool {
	if l.pendingMutationVerification == nil {
		return false
	}
	return l.rejectMissingMutationVerification("mutation_verification_required_plain_answer")
}

func (l *Loop) rejectPlainAnswerDuringMutationContinuation(text string) bool {
	if !l.mutationContinuationRequired {
		return false
	}
	return l.rejectMutationContinuationRequired("mutation_continuation_required_plain_answer")
}

func (l *Loop) enforceMutationContinuation(calls []gollm.FunctionCall) bool {
	if !l.mutationContinuationRequired || len(calls) == 0 {
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
	if !l.appendCorrectionWithCompaction(kind, message) {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "mutation continuation 요구가 반복적으로 무시되어 루프를 중단했습니다:\n"+message)
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

func (l *Loop) enforcePendingMutationVerification(calls []gollm.FunctionCall) bool {
	if l.pendingMutationVerification == nil || len(calls) == 0 {
		return false
	}
	if l.pendingMutationVerification.AwaitingResult {
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
	if !l.appendCorrectionWithCompaction(kind, message) {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "mutation verification 요구가 반복적으로 무시되어 루프를 중단했습니다:\n"+message)
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

func (l *Loop) consumeMutationVerificationResult(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalMutationVerificationResultCall {
			remaining = append(remaining, call)
			continue
		}
		if l.pendingMutationVerification == nil || !l.pendingMutationVerification.AwaitingResult {
			if !l.appendCorrectionWithCompaction("unexpected_mutation_verification_result", "mutation_verification_result is only valid immediately after all required mutation verification evidence has been collected. Continue with the active phase using a valid action, phase_progress, or final_report.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "unexpected mutation_verification_result가 반복되어 루프를 중단했습니다.")
				l.currIteration = 0
				l.state = StateDone
				return remaining, true
			}
			l.currIteration++
			l.state = StateRunning
			return remaining, true
		}
		result, ok := mutationVerificationResultFromFunctionCall(call)
		if !ok {
			if !l.appendCorrectionWithCompaction("invalid_mutation_verification_result", "mutation_verification_result payload was invalid. Return status as one of resolved, progressing, or unresolved. Include evidence_summary and a next_action when status is progressing or unresolved.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "mutation_verification_result 형식 오류가 반복되어 루프를 중단했습니다.")
				l.currIteration = 0
				l.state = StateDone
				return remaining, true
			}
			l.currIteration++
			l.state = StateRunning
			return remaining, true
		}
		l.pendingMutationVerification = nil
		switch result.Status {
		case "resolved":
			l.mutationContinuationRequired = false
			l.queueResponseDirective("Mutation verification result is resolved. You may now complete the active phase or emit a final_report grounded in the verification evidence. Do not claim more than the evidence supports.")
		case "progressing":
			l.mutationContinuationRequired = true
			l.queueResponseDirective("Mutation verification result is progressing. Continue the ReAct loop with the next best read-only observation after an appropriate wait/recheck interval. Do not emit a conclusive final_report yet.")
		case "unresolved":
			l.mutationContinuationRequired = true
			l.queueResponseDirective("Mutation verification result is unresolved. Continue the ReAct loop with a different diagnostic or remediation approach based on the observed evidence. Do not emit a conclusive final_report yet.")
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
		l.state = StateRunning
		return nil, true
	}
	return remaining, false
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
		out = append(out, req)
	}
	return out
}

func (v pendingMutationVerification) allSatisfied() bool {
	return len(v.remainingRequirements()) == 0
}

func (l *Loop) trackMutationVerification(call PendingCall, result map[string]any) {
	if l.mutationContinuationRequired && toolResultSucceeded(result) {
		l.mutationContinuationRequired = false
	}
	if l.pendingMutationVerification != nil {
		if l.shouldStartMutationVerification(call, result) {
			if verification, ok := l.mutationVerificationFromCall(call); ok {
				l.mergeMutationVerification(verification)
				l.queueResponseDirective(l.pendingMutationVerification.requiredMessage())
			}
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
			l.queueResponseDirective(l.pendingMutationVerification.requiredMessage())
		} else if l.pendingMutationVerification != nil {
			l.queueResponseDirective(l.pendingMutationVerification.requiredMessage())
		}
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
	l.queueResponseDirective(verification.requiredMessage())
}

func (l *Loop) mergeMutationVerification(next pendingMutationVerification) {
	if l.pendingMutationVerification == nil {
		l.pendingMutationVerification = &next
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
	seen := map[string]bool{}
	for _, req := range l.pendingMutationVerification.Requirements {
		seen[req.ID] = true
	}
	for _, req := range next.Requirements {
		if !seen[req.ID] {
			l.pendingMutationVerification.Requirements = append(l.pendingMutationVerification.Requirements, req)
			seen[req.ID] = true
		}
	}
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
	}
	return verification, true
}

func (l *Loop) mutationVerificationCallMatches(call gollm.FunctionCall) bool {
	_, ok := l.mutationVerificationCallMatchID(call)
	return ok
}

func (l *Loop) mutationVerificationCallsMatch(calls []gollm.FunctionCall) bool {
	if len(calls) == 0 || l.pendingMutationVerification == nil {
		return false
	}
	satisfied := map[string]bool{}
	for key, value := range l.pendingMutationVerification.Satisfied {
		satisfied[key] = value
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
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
	if target.Namespace != "" && (target.Resource == "" || kubectlResourceUsuallyNamespaced(target.Resource)) && !commandUsesNamespace(command, target.Namespace) {
		return false
	}
	if target.Resource != "" && !commandMentionsResource(command, target.Resource) && !commandMentionsResourceKind(command, target.Resource) {
		return false
	}
	if target.Name != "" && !commandMentionsToken(command, target.Name) && !commandUsesSelectorForName(command, target.Name) {
		return false
	}
	return true
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
	if err, ok := result["error"].(string); ok && strings.TrimSpace(err) != "" {
		return false
	}
	status, _ := result["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "blocked", "declined", "denied", "failed", "failure", "error":
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
		verb, verbIndex, ok := kubectlVerbAndIndexFromFields(fields, i)
		if !ok || !isKubectlMutatingVerb(verb) {
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
