package react

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

type contextError struct {
	Code      string
	Message   string
	Retryable bool
}

type guideRef struct {
	GuideID string `json:"guide_id"`
	Hash    string `json:"hash"`
	Content string `json:"content,omitempty"`
}

type actionRecord struct {
	Step       int            `json:"step"`
	Tool       string         `json:"tool"`
	Command    string         `json:"command,omitempty"`
	Target     *actionTarget  `json:"target,omitempty"`
	ResultHash string         `json:"result_hash"`
	Result     map[string]any `json:"result,omitempty"`
	Clues      []string       `json:"clues,omitempty"`
}

func contextHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:8])
}

func (l *Loop) appendContextBlock(kind, content string, preserve bool) bool {
	if preserve {
		l.currChatContent = append(l.currChatContent, content)
		return true
	}
	if l.contextBlockHashes == nil {
		l.contextBlockHashes = make(map[string]struct{})
	}
	key := kind + ":" + contextHash(content)
	if _, ok := l.contextBlockHashes[key]; ok {
		return false
	}
	l.contextBlockHashes[key] = struct{}{}
	l.currChatContent = append(l.currChatContent, content)
	return true
}

func (l *Loop) appendCorrection(code, message string) bool {
	if l.lastContextError != nil && l.lastContextError.Code == code && l.lastContextError.Message == message {
		return false
	}
	l.lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	return l.appendContextBlock("correction:"+code, message, false)
}

func (l *Loop) appendCorrectionWithCompaction(code, message string) bool {
	if l.lastContextError != nil && l.lastContextError.Code == code && l.lastContextError.Message == message {
		return false
	}
	l.lastContextError = &contextError{
		Code:      code,
		Message:   message,
		Retryable: true,
	}
	if !l.shouldCompactForStateRewrite() {
		return l.appendContextBlock("correction:"+code, message, false)
	}
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: correction state %q triggered compaction; preserving question, procedure order, clues, and next action. estimated context %d/%d tokens.", code, before, limit))
	if err := l.resetChatSession(); err != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+err.Error())
		return l.appendContextBlock("correction:"+code, message, false)
	}
	l.currChatContent = []any{l.compactedStateMessage("Return one corrected next response. Do not repeat the invalid response.")}
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: correction state %q preserved. estimated context %d/%d tokens.", code, after, limit))
	return true
}

func (l *Loop) compactedStateMessage(nextInstruction string) string {
	var b strings.Builder
	b.WriteString("Continue the same user request from compacted state.\n")
	l.writeConversationState(&b, true)
	if nextInstruction != "" {
		b.WriteString(nextInstruction)
	}
	return b.String()
}

func (l *Loop) priorConversationStateMessage() string {
	if !l.hasPriorConversationMemory() && !l.hasConversationState() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Previous conversation context for requirement analysis. Use it only when the new user request is a follow-up; explicit resource, name, namespace, or all-namespaces scope in the new request wins.\n")
	if l.hasPriorConversationMemory() {
		l.writePriorConversationMemory(&b)
	} else {
		l.writeConversationState(&b, false)
	}
	b.WriteString("Follow-up handling: if the new request is a follow-up without naming a new target/scope, default to the previous request_context target and scope and express the new diagnostic angle in requirement_analysis.operational_focus. Do not invent a new Kubernetes resource kind from follow-up wording alone.\n")
	b.WriteString("Do not repeat previous raw assistant JSON, guide bodies, corrections, or diagnostics unless the user asks for them.")
	return b.String()
}

func (l *Loop) hasPriorConversationMemory() bool {
	return l.lastOriginalQuery != "" ||
		l.lastRequirementAnalysis != nil ||
		l.lastRequestContext != nil ||
		strings.TrimSpace(l.lastDiagnosisSummary) != ""
}

func (l *Loop) writePriorConversationMemory(b *strings.Builder) {
	if l.lastOriginalQuery != "" {
		b.WriteString("previous_original_query: ")
		b.WriteString(compactPriorString(l.lastOriginalQuery, 1000))
		b.WriteString("\n")
	}
	if l.lastRequirementAnalysis != nil {
		if raw, err := json.Marshal(compactPriorRequirementAnalysis(l.lastRequirementAnalysis)); err == nil {
			b.WriteString("previous_requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.lastRequestContext != nil {
		if raw, err := json.Marshal(compactPriorRequestContext(l.lastRequestContext)); err == nil {
			b.WriteString("previous_request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastDiagnosisSummary) != "" {
		if raw, err := json.Marshal(l.lastDiagnosisSummary); err == nil {
			b.WriteString("previous_diagnosis_summary: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
}

func compactPriorRequirementAnalysis(analysis *requirementAnalysis) map[string]any {
	if analysis == nil {
		return nil
	}
	out := map[string]any{
		"request_type": analysis.RequestType,
		"action":       analysis.Action,
		"target": map[string]any{
			"category":    analysis.Target.Category,
			"name":        analysis.Target.Name,
			"description": compactPriorString(analysis.Target.Description, 500),
		},
		"scope": analysis.Scope,
	}
	if len(analysis.Resources) > 0 {
		limit := len(analysis.Resources)
		if limit > 3 {
			limit = 3
		}
		out["resource_candidates"] = append([]requirementResource(nil), analysis.Resources[:limit]...)
	}
	if analysis.OperationalFocus != nil {
		focus := map[string]any{
			"summary":                 compactPriorString(analysis.OperationalFocus.Summary, 500),
			"relationship_to_primary": analysis.OperationalFocus.RelationshipToPrimary,
			"changed_from_previous":   analysis.OperationalFocus.ChangedFromPrevious,
			"reason":                  compactPriorString(analysis.OperationalFocus.Reason, 500),
			"evidence_needs":          compactPriorStringSlice(analysis.OperationalFocus.EvidenceNeeds, 3, 300),
		}
		if len(analysis.OperationalFocus.RelatedResourceHints) > 0 {
			limit := len(analysis.OperationalFocus.RelatedResourceHints)
			if limit > 3 {
				limit = 3
			}
			hints := append([]requirementRelatedResource(nil), analysis.OperationalFocus.RelatedResourceHints[:limit]...)
			for i := range hints {
				hints[i].Evidence = compactPriorString(hints[i].Evidence, 300)
			}
			focus["related_resource_hints"] = hints
		}
		out["operational_focus"] = focus
	}
	if len(analysis.Evidence) > 0 {
		out["evidence_needs"] = compactPriorStringSlice(analysis.Evidence, 3, 300)
	}
	return out
}

func compactPriorRequestContext(ctx *requestContext) map[string]any {
	if ctx == nil {
		return nil
	}
	return map[string]any{
		"primary_target": ctx.PrimaryTarget,
		"scope":          ctx.Scope,
		"resource_class": ctx.ResourceClass,
	}
}

func compactPriorStringSlice(values []string, limit int, maxBytes int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) < limit {
		limit = len(values)
	}
	out := make([]string, 0, limit)
	for _, value := range values[:limit] {
		if text := compactPriorString(value, maxBytes); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func compactPriorString(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return safeStringHead(value, maxBytes) + " ...[truncated " + contextHash(value) + "]"
}

func (l *Loop) hasConversationState() bool {
	return l.originalQuery != "" ||
		l.requirementAnalysis != nil ||
		l.requestContext != nil ||
		l.resourceClassification != nil ||
		l.lastContextError != nil ||
		len(l.injectedGuides) > 0 ||
		len(l.completedActions) > 0 ||
		strings.TrimSpace(l.lastAssistantText) != ""
}

func (l *Loop) compactDiagnosisSummary() string {
	var b strings.Builder
	if len(l.completedActions) > 0 {
		if raw, err := json.Marshal(l.compactedActionSummaries()); err == nil {
			b.WriteString("completed_procedure_and_clues: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.lastAssistantText)); err == nil {
			b.WriteString("last_assistant_text: ")
			b.Write(raw)
		}
	}
	return strings.TrimSpace(b.String())
}

func cloneRequirementAnalysis(value *requirementAnalysis) *requirementAnalysis {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Resources = append([]requirementResource(nil), value.Resources...)
	if value.OperationalFocus != nil {
		focus := *value.OperationalFocus
		focus.RelatedResourceHints = append([]requirementRelatedResource(nil), value.OperationalFocus.RelatedResourceHints...)
		focus.EvidenceNeeds = append([]string(nil), value.OperationalFocus.EvidenceNeeds...)
		cloned.OperationalFocus = &focus
	}
	cloned.Evidence = append([]string(nil), value.Evidence...)
	cloned.Constraints = append([]string(nil), value.Constraints...)
	cloned.Ambiguities = append([]string(nil), value.Ambiguities...)
	return &cloned
}

func cloneRequestContext(value *requestContext) *requestContext {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (l *Loop) writeConversationState(b *strings.Builder, includeGuideContent bool) {
	if l.originalQuery != "" {
		b.WriteString("original_query: ")
		b.WriteString(l.originalQuery)
		b.WriteString("\n")
	}
	if l.requirementAnalysis != nil {
		if raw, err := json.Marshal(l.requirementAnalysis); err == nil {
			b.WriteString("requirement_analysis: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.requestContext != nil {
		if raw, err := json.Marshal(l.requestContext); err == nil {
			b.WriteString("request_context: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.resourceClassification != nil {
		if raw, err := json.Marshal(l.resourceClassification); err == nil {
			b.WriteString("resource_classification: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if l.lastContextError != nil {
		if raw, err := json.Marshal(l.lastContextError); err == nil {
			b.WriteString("last_error: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if len(l.injectedGuides) > 0 {
		keys := make([]string, 0, len(l.injectedGuides))
		for key := range l.injectedGuides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		refs := make([]guideRef, 0, len(keys))
		for _, key := range keys {
			ref := l.injectedGuides[key]
			if !includeGuideContent {
				ref.Content = ""
			}
			refs = append(refs, ref)
		}
		if raw, err := json.Marshal(refs); err == nil {
			b.WriteString("guide_contexts: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if len(l.completedActions) > 0 {
		if raw, err := json.Marshal(l.compactedActionSummaries()); err == nil {
			b.WriteString("completed_procedure_and_clues: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(l.lastAssistantText) != "" {
		if raw, err := json.Marshal(compactStateText(l.lastAssistantText)); err == nil {
			b.WriteString("last_assistant_answer: ")
			b.Write(raw)
			b.WriteString("\n")
		}
	}
}

func (l *Loop) compactedActionSummaries() []map[string]any {
	out := make([]map[string]any, 0, len(l.completedActions))
	for _, action := range l.completedActions {
		item := map[string]any{
			"step":        action.Step,
			"tool":        action.Tool,
			"result_hash": action.ResultHash,
		}
		if action.Command != "" {
			item["command"] = action.Command
		}
		if action.Target != nil {
			item["target"] = action.Target
		}
		if len(action.Clues) > 0 {
			item["clues"] = action.Clues
		}
		out = append(out, item)
	}
	return out
}

func (l *Loop) shouldCompactBeforeNextSend() bool {
	if l.actionSeq == l.lastCompactedActionSeq {
		return false
	}
	estimated := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	return estimated >= l.contextCompactThresholdTokens()
}

func (l *Loop) shouldCompactForStateRewrite() bool {
	return l.contextApproxTokens+estimateContextTokens(l.currChatContent...) >= l.contextCompactThresholdTokens()
}

func (l *Loop) compactBeforeNextIteration(nextInstruction string) {
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting: estimated context %d/%d tokens (>=80%%). Preserving question, procedure order, clues, and next action.", before, limit))
	if err := l.resetChatSession(); err != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+err.Error())
		return
	}
	if l.pendingResponseDirective != "" {
		nextInstruction = "Continue from compacted state and follow the pending runtime directive below."
	}
	l.currChatContent = []any{l.compactedStateMessage(nextInstruction)}
	l.appendPendingResponseDirectiveAfterCompaction()
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted: estimated context %d/%d tokens; %d completed diagnostic steps preserved.", after, limit, len(l.completedActions)))
}

func (l *Loop) compactAfterContextLengthError(err error) bool {
	before := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	limit := l.contextLimitTokens()
	l.lastContextError = &contextError{
		Code:      "context_length_exceeded",
		Message:   err.Error(),
		Retryable: true,
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("↻ context compacting after provider context-length error: estimated context %d/%d tokens. Retrying once with compacted procedure/clues.", before, limit))
	if resetErr := l.resetChatSession(); resetErr != nil {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+resetErr.Error())
		return false
	}
	nextInstruction := "The previous LLM request exceeded the provider context limit. Continue from this compacted state. Next action: choose exactly one remaining diagnostic step from the clues; do not repeat completed commands unless new evidence requires it."
	if l.pendingResponseDirective != "" {
		nextInstruction = "The previous LLM request exceeded the provider context limit. Continue from this compacted state and follow the pending runtime directive below."
	}
	l.currChatContent = []any{l.compactedStateMessage(nextInstruction)}
	l.appendPendingResponseDirectiveAfterCompaction()
	l.contextBlockHashes = nil
	l.lastCompactedActionSeq = l.actionSeq
	after := l.contextApproxTokens + estimateContextTokens(l.currChatContent...)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, fmt.Sprintf("✓ context compacted after context-length error: estimated context %d/%d tokens; retrying now.", after, limit))
	return true
}

func (l *Loop) appendPendingResponseDirectiveAfterCompaction() {
	if strings.TrimSpace(l.pendingResponseDirective) == "" {
		return
	}
	l.currChatContent = append(l.currChatContent, "Pending runtime directive for the next model response:\n"+l.pendingResponseDirective)
}

func (l *Loop) appendGuideObservation(ref guideRef, content string) {
	if l.injectedGuides == nil {
		l.injectedGuides = make(map[string]guideRef)
	}
	key := ref.GuideID
	if key == "" {
		key = ref.Hash
	}
	if previous, ok := l.injectedGuides[key]; ok && previous.Hash == ref.Hash {
		l.appendContextBlock("guide-ref", fmt.Sprintf("Guide context already injected; use guide_ref %s (%s) without repeating the guide body.", key, ref.Hash), false)
		return
	}
	ref.Content = content
	l.injectedGuides[key] = ref
	l.appendContextBlock("guide", content, false)
}

func compactObservationResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	out := make(map[string]any, len(result))
	for key, value := range result {
		out[key] = compactObservationValue(value)
	}
	return out
}

func compactObservationValue(value any) any {
	switch v := value.(type) {
	case string:
		return compactObservationString(v)
	case map[string]any:
		return compactObservationResult(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, compactObservationValue(item))
		}
		return out
	default:
		return value
	}
}

func compactObservationString(value string) any {
	const maxObservationChars = 16000
	if len(value) <= maxObservationChars {
		return value
	}
	const headChars = 10000
	const tailChars = 4000
	return map[string]any{
		"content_head": safeStringHead(value, headChars),
		"content_tail": safeStringTail(value, tailChars),
		"content_hash": contextHash(value),
		"original_len": len(value),
		"truncated":    true,
	}
}

func compactStateText(value string) any {
	const maxStateChars = 8000
	if len(value) <= maxStateChars {
		return value
	}
	const headChars = 5000
	const tailChars = 2000
	return map[string]any{
		"content_head": safeStringHead(value, headChars),
		"content_tail": safeStringTail(value, tailChars),
		"content_hash": contextHash(value),
		"original_len": len(value),
		"truncated":    true,
	}
}

func safeStringHead(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	if end == 0 {
		return ""
	}
	return value[:end]
}

func safeStringTail(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	if start >= len(value) {
		return ""
	}
	return value[start:]
}

func extractObservationClues(result map[string]any) []string {
	if result == nil {
		return nil
	}
	var clues []string
	seen := map[string]struct{}{}
	for _, text := range observationStrings(result) {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !isClueLine(line) {
				continue
			}
			if len(line) > 300 {
				line = line[:300] + "..."
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			clues = append(clues, line)
			if len(clues) >= 16 {
				return clues
			}
		}
	}
	if len(clues) > 0 {
		return clues
	}
	hash := contextHash(fmt.Sprintf("%v", result))
	return []string{"no concise clue extracted; result_hash=" + hash}
}

func observationStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case map[string]any:
		var out []string
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, observationStrings(v[key])...)
		}
		return out
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, observationStrings(item)...)
		}
		return out
	default:
		return nil
	}
}

func isClueLine(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"condition", "status:", "phase:", "reason:", "message:", "ready", "available",
		"replicas", "providerid", "annotation", "annotations:", "label", "labels:",
		"paused", "failed", "error", "warning", "waiting", "notavailable", "false",
		"true", "unhealthy", "unknown", "deletiontimestamp", "finalizers:",
		"ownerreferences:", "name:", "namespace:", ".io/", ".com/", ".net/", "/",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
