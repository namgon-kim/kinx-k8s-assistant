package react

import (
	"fmt"
	"strings"
)

func (l *Loop) runtimeStateAnchor() string {
	if l.requirementAnalysis == nil && l.phaseStepState == nil && l.pendingMutationVerification == nil && l.guideStepState == nil && strings.TrimSpace(l.pendingResponseDirective) == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("Runtime state summary. Treat this as the concise decision contract for the next response; detailed anchors below remain authoritative for their domain.\n")
	fmt.Fprintf(&b, "loop_state: %s\n", reactStateName(l.state))
	if strings.TrimSpace(l.originalQuery) != "" {
		fmt.Fprintf(&b, "original_query: %s\n", l.originalQuery)
	}
	if l.requirementAnalysis != nil {
		fmt.Fprintf(&b, "request_type: %s\n", l.requirementAnalysis.RequestType)
		fmt.Fprintf(&b, "requested_action: %s\n", l.requirementAnalysis.Action)
		if l.requirementAnalysis.Target.Category != "" {
			fmt.Fprintf(&b, "target_category: %s\n", l.requirementAnalysis.Target.Category)
		}
		if l.requirementAnalysis.Target.Name != "" {
			fmt.Fprintf(&b, "target_name: %s\n", l.requirementAnalysis.Target.Name)
		}
	}
	if l.requestContext != nil {
		if l.requestContext.PrimaryTarget.Resource != "" {
			fmt.Fprintf(&b, "primary_target_resource: %s\n", l.requestContext.PrimaryTarget.Resource)
		}
		if l.requestContext.PrimaryTarget.Name != "" {
			fmt.Fprintf(&b, "primary_target_name: %s\n", l.requestContext.PrimaryTarget.Name)
		}
		if l.requestContext.Scope.Namespace != "" {
			fmt.Fprintf(&b, "scope_namespace: %s\n", l.requestContext.Scope.Namespace)
		}
		if l.requestContext.ResourceClass != "" {
			fmt.Fprintf(&b, "resource_class: %s\n", l.requestContext.ResourceClass)
		}
	}
	if l.resourceClassification != nil {
		fmt.Fprintf(&b, "resource_classification: %s\n", l.resourceClassification.Kind)
	}

	l.writeRuntimePhaseSummary(&b)
	l.writeRuntimeNestedStateSummary(&b)

	fmt.Fprintf(&b, "active_gate: %s\n", l.runtimeActiveGate())
	fmt.Fprintf(&b, "required_next_output: %s\n", l.runtimeRequiredNextOutput())
	if forbidden := l.runtimeForbiddenOutputs(); len(forbidden) > 0 {
		fmt.Fprintf(&b, "forbidden_next_outputs: %s\n", strings.Join(forbidden, ","))
	}
	if directive := strings.TrimSpace(l.pendingResponseDirective); directive != "" {
		fmt.Fprintf(&b, "pending_runtime_directive: %s\n", compactSingleLine(directive))
	}
	return b.String()
}

func (l *Loop) writeRuntimePhaseSummary(b *strings.Builder) {
	if l.phaseStepState == nil {
		if l.requirementAnalysis != nil {
			b.WriteString("current_phase: phase_plan_required\n")
		}
		return
	}
	current := l.phaseStepState.currentStep()
	if current.Index == 0 {
		b.WriteString("current_phase: unknown\n")
		return
	}
	fmt.Fprintf(b, "current_phase_index: %d\n", current.Index)
	fmt.Fprintf(b, "current_phase_name: %s\n", current.Name)
	if current.Goal != "" {
		fmt.Fprintf(b, "current_phase_goal: %s\n", current.Goal)
	}
	if current.CompletionCondition != "" {
		fmt.Fprintf(b, "current_phase_completion_condition: %s\n", current.CompletionCondition)
	}
	if len(current.AllowedNext) > 0 {
		fmt.Fprintf(b, "allowed_next_phases: %s\n", strings.Join(current.AllowedNext, ","))
	}
	if completed := l.phaseStepState.completedPhaseIndices(); len(completed) > 0 {
		fmt.Fprintf(b, "completed_phase_indices: %s\n", formatStepIndices(completed))
	}
}

func (l *Loop) writeRuntimeNestedStateSummary(b *strings.Builder) {
	if l.pendingMutationVerification != nil {
		if l.pendingMutationVerification.AwaitingResult {
			b.WriteString("active_nested_state: mutation_verification_result\n")
		} else {
			b.WriteString("active_nested_state: mutation_verification_evidence\n")
			if remaining := l.pendingMutationVerification.remainingRequirements(); len(remaining) > 0 {
				var ids []string
				for _, req := range remaining {
					ids = append(ids, req.ID)
				}
				fmt.Fprintf(b, "remaining_mutation_evidence_ids: %s\n", strings.Join(ids, ","))
			}
		}
		return
	}
	if l.guideStepState != nil {
		if remaining := l.guideStepState.remainingSteps(); len(remaining) > 0 {
			b.WriteString("active_nested_state: resource_guide_steps\n")
			fmt.Fprintf(b, "remaining_guide_step_indices: %s\n", formatStepIndices(remaining))
			fmt.Fprintf(b, "next_guide_step_index: %d\n", remaining[0])
		} else {
			b.WriteString("active_nested_state: resource_guide_steps_complete\n")
		}
		return
	}
	b.WriteString("active_nested_state: none\n")
}

func (l *Loop) runtimeActiveGate() string {
	switch {
	case l.pendingMutationVerification != nil && l.pendingMutationVerification.AwaitingResult:
		return "mutation_verification_result_required"
	case l.pendingMutationVerification != nil:
		return "mutation_verification_evidence_required"
	case l.mutationContinuationRequired:
		return "mutation_continuation_required"
	case l.guidedPhaseProgressRequested:
		return "guided_diagnosis_phase_progress_required"
	case l.finalReportRequested:
		return "final_report_required"
	case l.phaseStepState == nil && l.requirementAnalysis != nil:
		return "phase_plan_required"
	case l.phaseStepState != nil && l.phaseStepRequiresResourceGuideLookup():
		return "resource_guide_lookup_required"
	default:
		return "none"
	}
}

func (l *Loop) runtimeRequiredNextOutput() string {
	switch l.runtimeActiveGate() {
	case "mutation_verification_result_required":
		return "mutation_verification_result"
	case "mutation_verification_evidence_required":
		return "one read-only action satisfying a remaining mutation evidence requirement"
	case "mutation_continuation_required":
		return "next best action based on verification evidence"
	case "guided_diagnosis_phase_progress_required":
		return "phase_progress"
	case "final_report_required":
		return "final_report"
	case "phase_plan_required":
		return "phase_plan"
	case "resource_guide_lookup_required":
		return "resource_guide_lookup"
	default:
		if l.phaseStepState != nil {
			return "action or phase_progress according to the current phase completion condition"
		}
		if l.requirementAnalysis == nil {
			return "requirement_analysis"
		}
		return "valid structured output for the current request"
	}
}

func (l *Loop) runtimeForbiddenOutputs() []string {
	switch l.runtimeActiveGate() {
	case "mutation_verification_result_required":
		return []string{"action", "final_report", "phase_progress", "next_directions", "answer"}
	case "mutation_verification_evidence_required":
		return []string{"mutating action", "final_report", "phase_progress", "next_directions", "answer", "mutation_verification_result"}
	case "mutation_continuation_required":
		return []string{"final_report", "phase_progress", "next_directions", "answer"}
	case "guided_diagnosis_phase_progress_required":
		return []string{"action", "final_report", "next_directions", "answer"}
	case "final_report_required":
		return []string{"action", "phase_progress", "next_directions", "answer"}
	case "phase_plan_required":
		return []string{"action", "phase_progress", "final_report", "next_directions", "answer"}
	case "resource_guide_lookup_required":
		return []string{"action", "phase_progress", "guide_progress", "final_report", "next_directions", "answer"}
	default:
		return nil
	}
}

func (l *Loop) phaseStepRequiresResourceGuideLookup() bool {
	if l.phaseStepState == nil || l.resourceGuideInjected {
		return false
	}
	current := l.phaseStepState.currentStep()
	return strings.EqualFold(strings.TrimSpace(current.Name), "guidance_lookup") &&
		l.resourceClassification != nil &&
		l.resourceClassification.Kind == resourceClassificationCRD
}

func (s *phaseStepState) completedPhaseIndices() []int {
	if s == nil {
		return nil
	}
	var out []int
	for _, step := range s.PhaseSteps {
		if s.Completed[step.Index] {
			out = append(out, step.Index)
		}
	}
	return out
}

func compactSingleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func reactStateName(state State) string {
	switch state {
	case StateIdle:
		return "Idle"
	case StateRunning:
		return "Running"
	case StateWaitingApproval:
		return "WaitingApproval"
	case StateWaitingDirectionChoice:
		return "WaitingDirectionChoice"
	case StateWaitingDirectionText:
		return "WaitingDirectionText"
	case StateDone:
		return "Done"
	case StateExited:
		return "Exited"
	default:
		return fmt.Sprintf("State(%d)", state)
	}
}
