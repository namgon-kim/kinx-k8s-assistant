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

const lightweightLookupPhase = "lightweight_lookup"

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
		accepted = &plan
		l.phaseStepState = newPhaseStepState(plan)
		continue
	}
	if accepted != nil {
		if len(remaining) == 0 {
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
		if !phasePlanAllowsTrailingCalls(*accepted, remaining) {
			message := "Phase plan was accepted, but the same response also included additional structured output. Return only the phase_plan object first, except for a single-step lightweight_lookup phase where exactly one action may be included with the phase_plan."
			if !l.appendCorrectionWithCompaction("phase_plan_unexpected_trailing_calls", message) {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 phase plan 동시 출력 오류로 루프를 중단했습니다:\n"+message)
				l.pendingCalls = nil
				l.currIteration = 0
				l.state = StateDone
				return nil, true
			}
			l.phaseStepState = nil
			l.pendingCalls = nil
			l.currIteration++
			l.state = StateRunning
			return nil, true
		}
	}
	return remaining, false
}

func (l *Loop) consumePhaseProgress(calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
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
			if !l.appendCorrectionWithCompaction("guidance_lookup_missing_resource_guide_lookup", message) {
				l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "resource_guide_lookup 없이 guidance_lookup 완료가 반복되어 진단을 중단합니다:\n"+message)
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
		l.guidedPhaseProgressRequested = false
		handled = true
	}
	if handled && len(remaining) == 0 {
		l.currIteration++
		l.state = StateRunning
		return nil, true
	}
	return remaining, false
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
		if !l.appendCorrectionWithCompaction(code, message) {
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "반복된 shim structured output 오류로 루프를 중단했습니다:\n"+message)
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
		if step.Index == 0 || step.Name == "" || step.Goal == "" || step.CompletionCondition == "" {
			return nil, false
		}
		steps = append(steps, step)
	}
	return steps, true
}

func phasePlanValid(plan phasePlan) bool {
	if strings.TrimSpace(plan.RequestGoal) == "" || len(plan.PhaseSteps) == 0 {
		return false
	}
	foundCurrent := false
	seenIndex := make(map[int]struct{}, len(plan.PhaseSteps))
	seenName := make(map[string]struct{}, len(plan.PhaseSteps))
	indexByName := make(map[string]int, len(plan.PhaseSteps))
	for _, step := range plan.PhaseSteps {
		if step.Index == 0 || strings.TrimSpace(step.Name) == "" {
			return false
		}
		if _, ok := seenIndex[step.Index]; ok {
			return false
		}
		seenIndex[step.Index] = struct{}{}
		name := strings.ToLower(strings.TrimSpace(step.Name))
		if _, ok := seenName[name]; ok {
			return false
		}
		seenName[name] = struct{}{}
		indexByName[name] = step.Index
		if step.Index == plan.CurrentPhaseIndex {
			foundCurrent = true
		}
	}
	for _, step := range plan.PhaseSteps {
		if phaseStepHasLaterStep(plan.PhaseSteps, step.Index) && len(nonEmptyStrings(step.AllowedNext)) == 0 {
			return false
		}
		for _, next := range step.AllowedNext {
			nextName := strings.ToLower(strings.TrimSpace(next))
			if nextName == "" {
				continue
			}
			if _, ok := seenName[nextName]; !ok {
				return false
			}
			if indexByName[nextName] <= step.Index {
				return false
			}
		}
	}
	return foundCurrent
}

func phaseStepHasLaterStep(steps []phaseStep, index int) bool {
	for _, step := range steps {
		if step.Index > index {
			return true
		}
	}
	return false
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
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
	if l.guidedPhaseProgressRequested {
		return
	}
	l.guidedPhaseProgressRequested = true
	var b strings.Builder
	b.WriteString("All nested resource-guide guidance_step entries have been completed for the active guided_diagnosis phase.\n")
	b.WriteString("Your next response MUST be a `phase_progress` object completing the active guided_diagnosis phase; do not emit another action or final_report yet.\n")
	b.WriteString("Set next_phase to final_report unless live evidence requires a different allowed next phase from the accepted phase_plan.")
	l.queueResponseDirective(b.String())
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
