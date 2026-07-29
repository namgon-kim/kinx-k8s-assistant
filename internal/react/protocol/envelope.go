package protocol

import (
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

// NormalizeModelOutput classifies a fully collected provider response without
// applying workflow state. Shim responses reach this boundary after their JSON
// object has been converted to the same provider-neutral function calls.
func NormalizeModelOutput(text string, calls []contract.FunctionCall) contract.ModelOutputEnvelope {
	envelope := contract.ModelOutputEnvelope{}
	trimmedText := strings.TrimSpace(text)
	if len(calls) == 0 {
		envelope.PlainAnswer = trimmedText
	} else {
		envelope.ProgressText = trimmedText
	}

	for _, call := range calls {
		classification := ClassifyNativeCallName(call.Name)
		switch classification.Support {
		case OutputUnsupported:
			envelope.InvalidOutputs = append(envelope.InvalidOutputs, contract.InvalidOutput{
				Name:   call.Name,
				Reason: fmt.Sprintf("output kind %q is not supported by the active protocol", classification.Kind),
			})
		case OutputUnknown:
			reason := "unknown model output"
			switch call.Name {
			case InvalidActionCall:
				reason = "malformed shim action"
			case InvalidStructuredOutputCall:
				reason = "invalid shim structured output"
			}
			envelope.InvalidOutputs = append(envelope.InvalidOutputs, contract.InvalidOutput{Name: call.Name, Reason: reason})
		case OutputSupported:
			if classification.Kind == contract.OutputAction {
				envelope.ExternalActions = append(envelope.ExternalActions, contract.ActionProposal{
					Call:    call,
					StepRef: stepRefFromArguments(call.Arguments),
				})
				continue
			}
			envelope.InternalEvents = append(envelope.InternalEvents, contract.ModelOutputEvent{
				Kind: classification.Kind,
				Call: call,
			})
		}
	}
	return envelope
}

func stepRefFromArguments(arguments map[string]any) *contract.StepRef {
	raw, ok := arguments["step_ref"].(map[string]any)
	if !ok {
		return nil
	}
	ref := contract.StepRef{
		Kind:  contract.StepKind(strings.TrimSpace(stringValue(raw["kind"]))),
		ID:    strings.TrimSpace(stringValue(raw["id"])),
		Index: intValue(raw["index"]),
	}
	if phase, ok := raw["phase"].(map[string]any); ok {
		ref.Phase = contract.PhaseRef{
			ID:        strings.TrimSpace(stringValue(phase["id"])),
			LineageID: strings.TrimSpace(stringValue(phase["lineage_id"])),
			Index:     intValue(phase["index"]),
			Name:      strings.TrimSpace(stringValue(phase["name"])),
		}
	}
	ref.GoalLineageID = strings.TrimSpace(stringValue(raw["goal_lineage_id"]))
	if ref.Kind == "" && ref.ID == "" && ref.GoalLineageID == "" && ref.Index == 0 &&
		ref.Phase.ID == "" && ref.Phase.LineageID == "" && ref.Phase.Index == 0 && strings.TrimSpace(ref.Phase.Name) == "" {
		return nil
	}
	return &ref
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
