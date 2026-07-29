package coordinator

import (
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	gateflow "github.com/namgon-kim/kinx-k8s-assistant/internal/react/flow/gate"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/protocol"
)

func normalizeModelOutputEnvelope(text string, calls []gollm.FunctionCall) contract.ModelOutputEnvelope {
	converted := make([]contract.FunctionCall, 0, len(calls))
	for _, call := range calls {
		converted = append(converted, contract.FunctionCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: contract.CloneDataMap(call.Arguments),
		})
	}
	return protocol.NormalizeModelOutput(text, converted)
}

func (l *Loop) validateModelOutputEnvelope(envelope *contract.ModelOutputEnvelope, calls []gollm.FunctionCall) bool {
	if envelope == nil {
		return l.rejectTurnOutputPolicy("missing_envelope", "The model response could not be normalized. Return one valid output for the requested runtime state.")
	}
	if len(envelope.InvalidOutputs) > 0 {
		invalid := envelope.InvalidOutputs[0]
		return l.rejectTurnOutputPolicy(
			"invalid_output",
			fmt.Sprintf("Output %q was rejected: %s. Return one supported output for the requested runtime state.", invalid.Name, invalid.Reason),
		)
	}
	context := gateflow.OutputPolicyContext{
		AllowPlainAnswer:           l.phaseAllowsPlainAnswer() || l.mutableRuntime().phaseStepState == nil && l.mutableRuntime().requirementAnalysis != nil && l.requirementAnalysisNeedsDirectConversation(),
		AllowLightweightPlanAction: l.lightweightBundleEligible(calls),
		ExternalActionCount:        len(envelope.ExternalActions),
	}
	decision := gateflow.ValidateOutputKinds(l.controlState(), envelope.Kinds(), context)
	if !decision.Allow {
		return l.rejectTurnOutputPolicy(
			decision.Code,
			fmt.Sprintf("The response violates the turn output contract (%s). Return only an output allowed by the current runtime control state.", decision.Code),
		)
	}
	if !l.bindActionProposals(envelope, calls) {
		return true
	}
	return false
}

func (l *Loop) rejectTurnOutputPolicy(code, correction string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "invalid_output"
	}
	return l.applyModelOutputCorrectionGate(
		"turn_output_policy_"+code,
		"모델 출력이 현재 turn contract와 반복적으로 충돌하여 요청을 중단했습니다.",
		correction,
	)
}

func (l *Loop) lightweightBundleEligible(calls []gollm.FunctionCall) bool {
	_, eligible := l.eligibleLightweightBundlePlan(calls)
	return eligible
}

func (l *Loop) eligibleLightweightBundlePlan(calls []gollm.FunctionCall) (phasePlan, bool) {
	if l.controlState() != RuntimeControlAwaitingPhasePlan || len(calls) != 2 {
		return phasePlan{}, false
	}
	var plan *phasePlan
	var action *gollm.FunctionCall
	for i := range calls {
		call := calls[i]
		if call.Name == protocol.PhasePlanCall {
			parsed, ok := phasePlanFromFunctionCall(call)
			if !ok || !singleLightweightPhase(parsed) {
				return phasePlan{}, false
			}
			plan = &parsed
			continue
		}
		if protocol.IsRuntimeInternalCall(call.Name) || action != nil {
			return phasePlan{}, false
		}
		action = &calls[i]
	}
	if plan == nil || action == nil || !isNonMutatingKubectlInvocation(*action) {
		return phasePlan{}, false
	}
	if result := l.validatePhasePlanForRequest(*plan); !result.Valid {
		return phasePlan{}, false
	}
	analysis := l.mutableRuntime().requirementAnalysis
	if analysis == nil {
		return phasePlan{}, false
	}
	risk := strings.ToLower(strings.Join([]string{analysis.RequestType, analysis.Action}, " "))
	for _, marker := range []string{"mutation", "mutate", "remediation", "repair", "apply", "delete", "patch"} {
		if strings.Contains(risk, marker) {
			return phasePlan{}, false
		}
	}
	return *plan, true
}

func (l *Loop) bindActionProposals(envelope *contract.ModelOutputEnvelope, calls []gollm.FunctionCall) bool {
	if envelope == nil || len(envelope.ExternalActions) == 0 {
		return true
	}
	derived, ok := l.deriveActionStepRef(calls)
	if !ok {
		l.rejectTurnOutputPolicy("ambiguous_action_step", "The runtime could not bind the action to one active step. Return one action for the single active runtime step.")
		return false
	}
	for i := range envelope.ExternalActions {
		proposal := &envelope.ExternalActions[i]
		bound := derived
		if bound.Kind == StepGeneralAction && len(envelope.ExternalActions) > 1 {
			bound.Index += i
		}
		proposal.BoundStepRef = &bound
		if proposal.StepRef != nil && !proposal.StepRef.Matches(bound) {
			l.rejectTurnOutputPolicy(
				"action_step_ref_mismatch",
				fmt.Sprintf("The action step_ref %q does not match the runtime-derived active step %q. Omit step_ref or return the matching reference.", proposal.StepRef.String(), bound.String()),
			)
			return false
		}
	}
	return true
}

func (l *Loop) deriveActionStepRef(calls []gollm.FunctionCall) (StepRef, bool) {
	if plan, eligible := l.eligibleLightweightBundlePlan(calls); eligible {
		execution := l.mutableRuntime().execution
		requestID := "request-provisional"
		if execution != nil && execution.RequestID != "" {
			requestID = execution.RequestID
		}
		phaseIndex := plan.PhaseSteps[0].Index
		phaseID := fmt.Sprintf("%s.phase-%d", requestID, phaseIndex)
		stepID := phaseID + ".step-1"
		return StepRef{
			Phase:         PhaseRef{ID: phaseID, LineageID: phaseID + ".lineage", Index: phaseIndex, Name: lightweightLookupPhase},
			Kind:          StepLightweightLookup,
			ID:            stepID,
			GoalLineageID: stepID + ".lineage",
			Index:         1,
		}, true
	}
	snapshot := l.RuntimeSnapshot()
	var active []StepRef
	for _, step := range snapshot.ActiveSteps {
		if step.Status == StepActive {
			active = append(active, step.Ref)
		}
	}
	if len(active) == 1 {
		return active[0], true
	}
	if len(active) > 1 {
		return StepRef{}, false
	}
	if execution := l.mutableRuntime().execution; execution != nil && execution.Goal.ID != "" && execution.ActiveStepID == "" {
		return StepRef{}, false
	}
	phase := PhaseRef{}
	if snapshot.PhaseRuntime != nil {
		phase = snapshot.PhaseRuntime.Active
	}
	return StepRef{Phase: phase, Kind: StepGeneralAction, Index: l.mutableRuntime().actionSeq + 1}, true
}
