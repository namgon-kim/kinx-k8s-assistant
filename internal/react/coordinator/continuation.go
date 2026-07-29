package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/session"
)

func (l *Loop) closureReserve() int {
	if execution := l.mutableRuntime().execution; execution != nil {
		if reserve := execution.AppliedBudget.ClosureReserve; reserve > 0 {
			return reserve
		}
	}
	return 2
}

func (l *Loop) closureThresholdReached() bool {
	limit := l.maxIterationLimit()
	reserve := l.closureReserve()
	if reserve >= limit {
		reserve = 1
	}
	return l.mutableRuntime().currIteration >= limit-reserve
}

func (l *Loop) closureCanInterruptCurrentControl() bool {
	switch l.controlState() {
	case RuntimeControlAwaitingModelStep,
		RuntimeControlAwaitingGuidedDiagnosisStep,
		RuntimeControlAwaitingResourceGuideLookup,
		RuntimeControlAwaitingMutationContinuation:
		return l.mutableRuntime().pendingMutationVerification == nil
	default:
		return false
	}
}

func (l *Loop) requestContinuationHandoff() {
	l.transitionControl(RuntimeControlAwaitingContinuationHandoff)
	l.queueResponseDirective(
		"Execution segment closure is required. Return exactly one continuation_handoff with conclusive=false. " +
			"Use only runtime request, goal, step, evidence, and obligation IDs. Do not return an action, plan revision, final_report, or plain answer.",
	)
}

func (l *Loop) consumeContinuationHandoff(ctx context.Context, calls []gollm.FunctionCall) ([]gollm.FunctionCall, bool) {
	var remaining []gollm.FunctionCall
	for _, call := range calls {
		if call.Name != protocol.ContinuationHandoffCall {
			remaining = append(remaining, call)
			continue
		}
		if l.controlState() != RuntimeControlAwaitingContinuationHandoff {
			return remaining, l.applyModelOutputCorrectionGate(
				"unexpected_continuation_handoff",
				"continuation handoff가 허용되지 않은 상태에서 반복되어 요청을 중단했습니다.",
				"continuation_handoff is valid only when the runtime requests segment closure.",
			)
		}
		handoff, ok := continuationHandoffFromFunctionCall(call)
		if !ok {
			return remaining, l.applyModelOutputCorrectionGate(
				"invalid_continuation_handoff",
				"continuation handoff 형식 오류가 반복되어 요청을 중단했습니다.",
				"Return one complete continuation_handoff with exact request_id, goal_id, runtime IDs, recommended_next_step, and conclusive=false.",
			)
		}
		if err := l.validateContinuationHandoff(handoff); err != nil {
			return remaining, l.applyModelOutputCorrectionGate(
				"invalid_continuation_handoff_reference",
				"continuation handoff reference 오류가 반복되어 요청을 중단했습니다.",
				err.Error(),
			)
		}
		l.mutableRuntime().currIteration++
		l.acceptContinuationHandoff(ctx, handoff)
		l.appendFunctionCallResult(call, map[string]any{"status": "accepted"})
		return nil, true
	}
	return remaining, false
}

func continuationHandoffFromFunctionCall(call gollm.FunctionCall) (contract.ContinuationHandoff, bool) {
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return contract.ContinuationHandoff{}, false
	}
	var handoff contract.ContinuationHandoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return contract.ContinuationHandoff{}, false
	}
	return handoff, strings.TrimSpace(handoff.RequestID) != "" &&
		strings.TrimSpace(handoff.GoalID) != "" &&
		strings.TrimSpace(handoff.CurrentJudgement) != "" &&
		strings.TrimSpace(handoff.RecommendedNextStep) != ""
}

func (l *Loop) validateContinuationHandoff(handoff contract.ContinuationHandoff) error {
	execution := l.mutableRuntime().execution
	if execution == nil {
		return fmt.Errorf("the runtime has no active execution state for continuation")
	}
	if handoff.Conclusive {
		return fmt.Errorf("continuation_handoff.conclusive must be false")
	}
	if handoff.RequestID != execution.RequestID || handoff.GoalID != execution.Goal.ID {
		return fmt.Errorf("continuation_handoff must use request_id=%q and goal_id=%q", execution.RequestID, execution.Goal.ID)
	}
	for _, id := range handoff.CompletedSteps {
		status, ok := execution.StepStatus[id]
		if !ok || !stepStatusTerminal(status) {
			return fmt.Errorf("completed_steps contains nonterminal or unknown step %q", id)
		}
	}
	for _, id := range handoff.UnresolvedSteps {
		status, ok := execution.StepStatus[id]
		if !ok || stepStatusTerminal(status) {
			return fmt.Errorf("unresolved_steps contains terminal or unknown step %q", id)
		}
	}
	for _, phase := range execution.Phases {
		for _, step := range phase.Steps {
			if !stepStatusTerminal(execution.StepStatus[step.ID]) &&
				!containsString(handoff.UnresolvedSteps, step.ID) {
				return fmt.Errorf("unresolved_steps omits nonterminal runtime step %q", step.ID)
			}
		}
	}
	for _, id := range handoff.EvidenceRefs {
		if !executionHasObservation(execution, id) {
			return fmt.Errorf("evidence_refs contains unknown observation %q", id)
		}
	}
	for _, obligation := range execution.BlockedObligations {
		if !containsString(handoff.MandatoryObligations, obligation) {
			return fmt.Errorf("mandatory_obligations omits runtime obligation %q", obligation)
		}
	}
	return nil
}

func (l *Loop) deterministicContinuationHandoff(reason string) contract.ContinuationHandoff {
	execution := l.mutableRuntime().execution
	handoff := contract.ContinuationHandoff{
		CurrentJudgement:    strings.TrimSpace(reason),
		RecommendedNextStep: "Resume the first unresolved active or pending step using existing evidence and lineage.",
		Conclusive:          false,
	}
	if execution == nil {
		return handoff
	}
	handoff.RequestID = execution.RequestID
	handoff.GoalID = execution.Goal.ID
	for _, phase := range execution.Phases {
		for _, step := range phase.Steps {
			if stepStatusTerminal(execution.StepStatus[step.ID]) {
				handoff.CompletedSteps = append(handoff.CompletedSteps, step.ID)
			} else {
				handoff.UnresolvedSteps = append(handoff.UnresolvedSteps, step.ID)
			}
		}
	}
	for _, observation := range execution.OrderedObservations() {
		handoff.EvidenceRefs = append(handoff.EvidenceRefs, observation.ID)
	}
	handoff.MandatoryObligations = append([]string(nil), execution.BlockedObligations...)
	return handoff
}

func (l *Loop) acceptContinuationHandoff(ctx context.Context, handoff contract.ContinuationHandoff) {
	segment := 1
	total := l.mutableRuntime().currIteration
	if previous := l.mutableRuntime().continuation; previous != nil {
		segment = previous.Segment
		total += previous.TotalIterations
	}
	l.mutableRuntime().continuation = &contract.ContinuationState{
		Handoff:           handoff,
		TotalIterations:   total,
		Segment:           segment,
		SegmentIterations: l.mutableRuntime().currIteration,
	}
	l.mutableRuntime().continuationResumePending = true
	l.mutableRuntime().pendingDirectionPrompt = &directionPromptState{
		Options: []nextDirectionOption{{
			Kind:        "different_approach",
			Summary:     "같은 요청과 실행 이력으로 계속",
			Instruction: handoff.RecommendedNextStep,
		}},
		FinalizeIdx: 2,
	}
	l.transitionControl(RuntimeControlAwaitingContinuationChoice)
	l.refreshInputOwner()
	l.addTranslatedModelMessage(
		ctx,
		"Execution segment summary\n"+
			"Current judgement: "+strings.TrimSpace(handoff.CurrentJudgement)+"\n"+
			"Recommended next step: "+strings.TrimSpace(handoff.RecommendedNextStep),
	)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeUserChoiceRequest, &api.UserChoiceRequest{
		Prompt: "현재 실행 구간이 종료되었습니다. 기존 request, lineage, 시도 횟수를 유지한 채 계속할까요?",
		Options: []api.UserChoiceOption{
			{Value: "continue", Label: "계속"},
			{Value: "finalize", Label: "여기서 종료"},
		},
	})
}

func (l *Loop) resumeContinuationSegment(opt nextDirectionOption) {
	state := l.mutableRuntime().continuation
	if state == nil {
		return
	}
	state.Segment++
	state.SegmentIterations = 0
	l.mutableRuntime().continuationResumePending = false
	l.applyRuntimeCleanup(cleanupDirectionPromptPolicy())
	l.mutableRuntime().currIteration = 0
	l.mutableRuntime().currChatContent = append(
		l.mutableRuntime().currChatContent,
		"Continue the same request and goal lineage from the accepted continuation handoff.\n"+
			"recommended_next_step: "+firstNonEmptyString(opt.Instruction, state.Handoff.RecommendedNextStep),
	)
	l.transitionControl(RuntimeControlAwaitingModelStep)
	l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "기존 요청과 누적 실행 이력을 유지해 다음 실행 구간을 시작합니다.")
}

func stepStatusTerminal(status contract.StepStatus) bool {
	switch status {
	case contract.StepCompleted, contract.StepAchieved, contract.StepBlocked, contract.StepSkipped, contract.StepSuperseded:
		return true
	default:
		return false
	}
}

func executionHasObservation(execution *session.GoalExecutionState, id string) bool {
	_, ok := execution.ObservationByID(id)
	return ok
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
