package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

// markGuideStepCompleted records that the model finished a numbered guide
// step. Called after an action observation is appended. Returns true if the
// state transitioned to "all steps completed" on this call.
func (l *Loop) markGuideStepCompleted(stepIndex int) bool {
	state := l.guideStepState
	if state == nil || stepIndex < 1 || stepIndex > state.TotalSteps {
		return false
	}
	if state.Completed == nil {
		state.Completed = map[int]bool{}
	}
	if state.Completed[stepIndex] {
		return false
	}
	state.Completed[stepIndex] = true
	return state.allCompleted()
}

func (l *Loop) requestPostGuideCompletionDirective() {
	if l.guideStepState == nil || !l.guideStepState.allCompleted() {
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
	if l.finalReportRequested {
		return
	}
	l.guidedPhaseProgressRequested = false
	l.finalReportRequested = true
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
			if !l.appendCorrection("invalid_final_report", "final_report payload was invalid. Re-emit a final_report with required fields. Conclusive reports require attempted, evidence_known, most_likely_cause, and conclusion. Inconclusive reports require attempted, most_likely_cause, and at least one of evidence_known, evidence_missing, or blockers.") {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "final_report 형식 오류가 반복되어 진단을 중단합니다.")
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return nil, true
			}
			l.pendingCalls = nil
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		l.pendingFinalReport = &report
		l.emitFinalReportMessage(ctx, report)
		l.guideStepState = nil
		l.finalReportRequested = false
		l.guidedPhaseProgressRequested = false
		if report.Conclusive {
			if l.promptProblematicResourceInvestigation(report) {
				return nil, true
			}
			l.state = StateDone
			l.pendingCalls = nil
			l.currIteration = 0
			return nil, true
		}
		l.requestNextDirectionsFromModel(report)
		l.pendingCalls = nil
		l.currIteration++
		l.state = StateRunning
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
	return finalReport{
		Conclusive:             boolFromAny(args["conclusive"]),
		Conclusion:             stringFromAny(args["conclusion"]),
		Attempted:              stringSliceFromAnyLoose(args["attempted"]),
		EvidenceKnown:          stringSliceFromAnyLoose(args["evidence_known"]),
		EvidenceMissing:        stringSliceFromAnyLoose(args["evidence_missing"]),
		MostLikelyCause:        stringFromAny(args["most_likely_cause"]),
		RecommendedUserActions: stringSliceFromAnyLoose(args["recommended_user_actions"]),
		ProblematicResources:   problematicResourcesFromAny(args["problematic_resources"]),
		Blockers:               stringSliceFromAnyLoose(args["blockers"]),
	}
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
