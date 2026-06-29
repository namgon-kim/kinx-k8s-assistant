package react

import (
	"fmt"
	"strings"
)

func (l *Loop) runtimeStateAnchor() string {
	snapshot := l.RuntimeSnapshot()
	if !snapshot.ShouldEmitAnchor() {
		return ""
	}

	var b strings.Builder
	b.WriteString("Runtime state summary. Treat this as the concise decision contract for the next response; detailed anchors below remain authoritative for their domain.\n")
	fmt.Fprintf(&b, "loop_state: %s\n", reactStateName(snapshot.LoopState))
	fmt.Fprintf(&b, "control_state: %s\n", snapshot.Control)
	if snapshot.OriginalQuery != "" {
		fmt.Fprintf(&b, "original_query: %s\n", snapshot.OriginalQuery)
	}
	if snapshot.Requirement != nil {
		fmt.Fprintf(&b, "request_type: %s\n", snapshot.Requirement.RequestType)
		fmt.Fprintf(&b, "requested_action: %s\n", snapshot.Requirement.Action)
		if snapshot.Requirement.Target.Category != "" {
			fmt.Fprintf(&b, "target_category: %s\n", snapshot.Requirement.Target.Category)
		}
		if snapshot.Requirement.Target.Name != "" {
			fmt.Fprintf(&b, "target_name: %s\n", snapshot.Requirement.Target.Name)
		}
	}
	if snapshot.Request != nil {
		if snapshot.Request.PrimaryTarget.Resource != "" {
			fmt.Fprintf(&b, "primary_target_resource: %s\n", snapshot.Request.PrimaryTarget.Resource)
		}
		if snapshot.Request.PrimaryTarget.Name != "" {
			fmt.Fprintf(&b, "primary_target_name: %s\n", snapshot.Request.PrimaryTarget.Name)
		}
		if snapshot.Request.Scope.Namespace != "" {
			fmt.Fprintf(&b, "scope_namespace: %s\n", snapshot.Request.Scope.Namespace)
		}
		if snapshot.Request.ResourceClass != "" {
			fmt.Fprintf(&b, "resource_class: %s\n", snapshot.Request.ResourceClass)
		}
	}
	if snapshot.ResourceClassification != nil {
		fmt.Fprintf(&b, "resource_classification: %s\n", snapshot.ResourceClassification.Kind)
	}

	snapshot.writeRuntimePhaseSummary(&b)
	snapshot.writeRuntimeNestedStateSummary(&b)

	fmt.Fprintf(&b, "active_gate: %s\n", snapshot.ActiveGate())
	fmt.Fprintf(&b, "required_next_output: %s\n", snapshot.RequiredNextOutput())
	if forbidden := snapshot.ForbiddenNextOutputs(); len(forbidden) > 0 {
		fmt.Fprintf(&b, "forbidden_next_outputs: %s\n", strings.Join(forbidden, ","))
	}
	if snapshot.PendingDirective != "" {
		fmt.Fprintf(&b, "pending_runtime_directive: %s\n", compactSingleLine(snapshot.PendingDirective))
	}
	return b.String()
}

func (s RuntimeSnapshot) writeRuntimePhaseSummary(b *strings.Builder) {
	if s.Phase == nil {
		if s.Requirement != nil {
			b.WriteString("current_phase: phase_plan_required\n")
		}
		return
	}
	current := s.Phase.currentStep()
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
	if completed := s.Phase.completedPhaseIndices(); len(completed) > 0 {
		fmt.Fprintf(b, "completed_phase_indices: %s\n", formatStepIndices(completed))
	}
}

func (s RuntimeSnapshot) writeRuntimeNestedStateSummary(b *strings.Builder) {
	fmt.Fprintf(b, "active_nested_state: %s\n", s.NestedStateName())
	if s.PendingMutationVerification != nil && !s.PendingMutationVerification.AwaitingResult {
		if remaining := s.PendingMutationVerification.remainingRequirements(); len(remaining) > 0 {
			var ids []string
			for _, req := range remaining {
				ids = append(ids, req.ID)
			}
			fmt.Fprintf(b, "remaining_mutation_evidence_ids: %s\n", strings.Join(ids, ","))
		}
		return
	}
	if s.Guide != nil {
		if remaining := s.Guide.remainingSteps(); len(remaining) > 0 {
			fmt.Fprintf(b, "remaining_guide_step_indices: %s\n", formatStepIndices(remaining))
			fmt.Fprintf(b, "next_guide_step_index: %d\n", remaining[0])
		}
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
