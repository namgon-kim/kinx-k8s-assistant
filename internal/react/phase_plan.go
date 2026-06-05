package react

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

type phaseStepState struct {
	RequestGoal       string
	CurrentPhaseIndex int
	PhaseSteps        []phaseStep
	Completed         map[int]bool
}

func (l *Loop) consumePhasePlan(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
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
			message := "Phase plan was invalid. Return only one corrected phase_plan object before choosing any action. Include request_goal, current_phase_index, and ordered phase_steps with index, name, goal, completion_condition, and allowed_next."
			if !l.appendCorrectionWithCompaction("invalid_phase_plan", message) {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 phase plan 오류로 루프를 중단했습니다:\n"+message)
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
		l.phaseStepState = newPhaseStepState(plan)
		l.currIteration++
		l.state = StateRunning
		return nil, true
	}
	return remaining, false
}

func (l *Loop) consumePhaseProgress(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != internalPhaseProgressCall {
			remaining = append(remaining, call)
			continue
		}
		progress, ok := phaseProgressFromFunctionCall(call)
		if !ok || l.phaseStepState == nil || !l.phaseStepState.acceptProgress(progress) {
			message := "Phase progress was invalid. Return one corrected phase_progress object for the active phase, or continue the active phase with one valid action. Do not use guide_progress for top-level phase completion."
			if !l.appendCorrectionWithCompaction("invalid_phase_progress", message) {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 phase progress 오류로 루프를 중단했습니다:\n"+message)
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
		if l.guideStepState != nil && strings.EqualFold(l.phaseStepState.phaseName(progress.PhaseCompleted), "guided_diagnosis") {
			l.guideStepState = nil
		}
		l.currIteration++
		l.state = StateRunning
		return nil, true
	}
	return remaining, false
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
	message := "After requirement_analysis is accepted, return only one phase_plan object before choosing any action, resource_guide_lookup, final_report, or answer. The plan must define ordered phase_steps, each with a goal and completion_condition."
	if !l.appendCorrectionWithCompaction("missing_phase_plan", message) {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 phase plan 누락으로 루프를 중단했습니다:\n"+message)
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

func phaseStepsFromValue(value any) ([]phaseStep, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	steps := make([]phaseStep, 0, len(raw))
	seen := make(map[int]struct{})
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
		if step.Index == 0 || step.Name == "" || step.Goal == "" || step.CompletionCondition == "" {
			return nil, false
		}
		if _, ok := seen[step.Index]; ok {
			return nil, false
		}
		seen[step.Index] = struct{}{}
		steps = append(steps, step)
	}
	return steps, true
}

func phasePlanValid(plan phasePlan) bool {
	if strings.TrimSpace(plan.RequestGoal) == "" || len(plan.PhaseSteps) == 0 {
		return false
	}
	foundCurrent := false
	for _, step := range plan.PhaseSteps {
		if step.Index == plan.CurrentPhaseIndex {
			foundCurrent = true
		}
	}
	return foundCurrent
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

func (s *phaseStepState) acceptProgress(progress phaseProgress) bool {
	if s == nil {
		return false
	}
	current := s.currentStep()
	if current.Index == 0 || progress.PhaseCompleted != current.Index {
		return false
	}
	s.Completed[progress.PhaseCompleted] = true
	if next := s.nextIndex(progress.NextPhase); next != 0 {
		s.CurrentPhaseIndex = next
	} else if next := s.firstIncompleteAfter(progress.PhaseCompleted); next != 0 {
		s.CurrentPhaseIndex = next
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

func (s *phaseStepState) nextIndex(name string) int {
	name = strings.TrimSpace(strings.ToLower(name))
	if s == nil || name == "" {
		return 0
	}
	for _, step := range s.PhaseSteps {
		if strings.ToLower(strings.TrimSpace(step.Name)) == name && !s.Completed[step.Index] {
			return step.Index
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
	b.WriteString("- Do not use `guide_progress` for top-level phase completion; `guide_progress` is only for nested guidance_step entries while current_phase_name=guided_diagnosis.\n")
	b.WriteString("- If guidance is useful, enter it through guidance_decision/guidance_lookup; do not assume runtime will automatically inject RAG.\n")
	return b.String()
}

func (l *Loop) requestGuidedDiagnosisPhaseProgress() {
	var b strings.Builder
	b.WriteString("All nested resource-guide guidance_step entries have been completed for the active guided_diagnosis phase.\n")
	b.WriteString("Your next response MUST be a `phase_progress` object completing the active guided_diagnosis phase; do not emit another action or final_report yet.\n")
	b.WriteString("Set next_phase to final_report unless live evidence requires a different allowed next phase from the accepted phase_plan.")
	l.queueResponseDirective(b.String())
}

func (l *Loop) phaseAllowsPlainAnswer() bool {
	if l.phaseStepState == nil {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(l.phaseStepState.currentStep().Name))
	switch name {
	case "response_synthesis", "clarification", "explanation", "final_report":
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
	if !l.appendCorrectionWithCompaction("plain_answer_wrong_phase", message) {
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "잘못된 phase의 일반 응답이 반복되어 루프를 중단했습니다:\n"+message)
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
