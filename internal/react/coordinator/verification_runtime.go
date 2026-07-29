package coordinator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/kube"
)

const (
	defaultVerificationRechecks = 5
	maxVerificationDelaySeconds = 30
)

type verificationRuntimeCheck struct {
	ID                     string
	Mode                   contract.VerificationMode
	Target                 actionTarget
	ExpectedState          string
	SuggestedCommand       string
	Status                 contract.VerificationCheckStatus
	EvidenceRefs           []string
	InitialDelaySeconds    int
	RecheckIntervalSeconds int
	RechecksUsed           int
	MaxRechecks            int
}

func verificationSpecFromAny(value any) (*contract.VerificationSpec, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var spec contract.VerificationSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if err := normalizeVerificationSpec(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func normalizeVerificationSpec(spec *contract.VerificationSpec) error {
	if spec == nil {
		return nil
	}
	if spec.Shape == "" {
		spec.Shape = contract.VerificationSingle
	}
	switch spec.Shape {
	case contract.VerificationSingle:
		if spec.Mode == "" {
			spec.Mode = contract.VerificationImmediate
		}
		if !validVerificationMode(spec.Mode) {
			return fmt.Errorf("single verification mode must be immediate or await_state")
		}
		if strings.TrimSpace(spec.ExpectedState) == "" {
			return fmt.Errorf("single verification requires expected_state")
		}
		if len(spec.Checks) != 0 {
			return fmt.Errorf("single verification must not contain checks")
		}
		if spec.Policy != "" {
			return fmt.Errorf("single verification must not declare a chain policy")
		}
		normalizeVerificationTiming(&spec.InitialDelaySeconds, &spec.RecheckIntervalSeconds)
	case contract.VerificationChain:
		if spec.Mode != "" || strings.TrimSpace(spec.ExpectedState) != "" ||
			spec.InitialDelaySeconds != 0 || spec.RecheckIntervalSeconds != 0 {
			return fmt.Errorf("verification chain timing, mode, and expected state belong to each ordered check")
		}
		if spec.Policy == "" {
			spec.Policy = contract.VerificationOrdered
		}
		if spec.Policy != contract.VerificationOrdered {
			return fmt.Errorf("verification chain policy must be ordered")
		}
		if len(spec.Checks) < 2 {
			return fmt.Errorf("verification chain requires at least two distinct checks")
		}
		seen := make(map[string]struct{}, len(spec.Checks))
		for i := range spec.Checks {
			check := &spec.Checks[i]
			if check.Mode == "" {
				check.Mode = contract.VerificationImmediate
			}
			if !validVerificationMode(check.Mode) {
				return fmt.Errorf("verification chain check %d mode must be immediate or await_state", i+1)
			}
			if strings.TrimSpace(check.ExpectedState) == "" {
				return fmt.Errorf("verification chain check %d requires expected_state", i+1)
			}
			normalizeVerificationTiming(&check.InitialDelaySeconds, &check.RecheckIntervalSeconds)
			signature := verificationCheckSignature(*check)
			if _, duplicate := seen[signature]; duplicate {
				return fmt.Errorf("verification chain check %d duplicates an earlier target and expected state", i+1)
			}
			seen[signature] = struct{}{}
		}
	default:
		return fmt.Errorf("verification shape must be single or chain")
	}
	return nil
}

func verificationCheckSignature(check contract.VerificationCheckSpec) string {
	target := actionTarget{}
	if check.Target != nil {
		target = *check.Target
	}
	target = normalizeVerificationTarget(target)
	return strings.Join([]string{
		target.Resource,
		strings.ToLower(target.Namespace),
		strings.ToLower(target.Name),
		strings.ToLower(strings.TrimSpace(check.ExpectedState)),
	}, "\x00")
}

func validVerificationMode(mode contract.VerificationMode) bool {
	return mode == contract.VerificationImmediate || mode == contract.VerificationAwaitState
}

func normalizeVerificationTiming(initialDelay, interval *int) {
	if *initialDelay < 0 {
		*initialDelay = 0
	}
	if *initialDelay > maxVerificationDelaySeconds {
		*initialDelay = maxVerificationDelaySeconds
	}
	if *interval <= 0 {
		*interval = 5
	}
	if *interval > maxVerificationDelaySeconds {
		*interval = maxVerificationDelaySeconds
	}
}

func (l *Loop) queueVerificationWait(checkID string, seconds int) error {
	if seconds <= 0 {
		return nil
	}
	if err := l.queueTurnEffect(contract.Effect{
		Kind: contract.EffectWait,
		Payload: verificationWaitEffect{
			Seconds: seconds,
			Reason:  checkID,
		},
	}); err != nil {
		return fmt.Errorf("queue verification wait for %q: %w", checkID, err)
	}
	return nil
}

func verificationChecksFromSpec(
	spec *contract.VerificationSpec,
	idPrefix string,
	defaultTarget actionTarget,
) []verificationRuntimeCheck {
	if spec == nil {
		return nil
	}
	if spec.Shape == contract.VerificationChain {
		checks := make([]verificationRuntimeCheck, 0, len(spec.Checks))
		for i, check := range spec.Checks {
			target := normalizeVerificationTarget(defaultTarget)
			if check.Target != nil {
				target = normalizeVerificationTarget(actionTarget{
					Resource:  check.Target.Resource,
					Namespace: check.Target.Namespace,
					Name:      check.Target.Name,
				})
			}
			checks = append(checks, newVerificationRuntimeCheck(
				fmt.Sprintf("%s.check-%d", idPrefix, i+1),
				check.Mode,
				target,
				check.ExpectedState,
				check.SuggestedCommand,
				check.InitialDelaySeconds,
				check.RecheckIntervalSeconds,
			))
		}
		return checks
	}
	return []verificationRuntimeCheck{newVerificationRuntimeCheck(
		idPrefix,
		spec.Mode,
		normalizeVerificationTarget(defaultTarget),
		spec.ExpectedState,
		verificationCommandHint(normalizeVerificationTarget(defaultTarget)),
		spec.InitialDelaySeconds,
		spec.RecheckIntervalSeconds,
	)}
}

func normalizeVerificationTarget(target actionTarget) actionTarget {
	target = kube.NormalizeTarget(target)
	target.Resource = normalizeKubectlResource(strings.ToLower(target.Resource))
	target.Namespace = cleanNamespaceValue(target.Namespace)
	target.Name = cleanUnknownPlaceholder(target.Name)
	if target.Resource == "unknown" {
		target.Resource = ""
	}
	return target
}

func validateDeclaredVerificationTarget(target *contract.ActionTarget, index int) error {
	if target == nil {
		return nil
	}
	normalized := normalizeVerificationTarget(*target)
	if normalized.Resource == "" || normalized.Name == "" {
		return fmt.Errorf("ordered verification check %d target requires a concrete resource and name", index)
	}
	if strings.TrimSpace(target.Namespace) != "" && normalized.Namespace == "" {
		return fmt.Errorf("ordered verification check %d target namespace must be a concrete Kubernetes namespace", index)
	}
	return nil
}

func validateMutationVerificationSpec(call PendingCall) error {
	if call.Verification == nil {
		return fmt.Errorf("a mutating action requires verification metadata")
	}
	target, hasTarget := actionTargetFromFunctionCall(call.FunctionCall)
	target = normalizeVerificationTarget(target)
	hasConcreteTarget := hasTarget &&
		target.Resource != "" &&
		target.Name != ""
	if !hasConcreteTarget {
		return fmt.Errorf("a mutating action requires a concrete action target with resource and name")
	}
	if call.Verification.Shape != contract.VerificationChain {
		return nil
	}
	for i, check := range call.Verification.Checks {
		if err := validateDeclaredVerificationTarget(check.Target, i+1); err != nil {
			return err
		}
	}
	first := call.Verification.Checks[0]
	if first.Target == nil {
		return nil
	}
	firstTarget := actionTarget{
		Resource:  first.Target.Resource,
		Namespace: first.Target.Namespace,
		Name:      first.Target.Name,
	}
	if !sameVerificationTarget(target, firstTarget) {
		return fmt.Errorf("the first ordered verification check must verify the mutated target")
	}
	return nil
}

func sameVerificationTarget(a, b actionTarget) bool {
	resourceA := normalizeKubectlResource(strings.ToLower(strings.TrimSpace(a.Resource)))
	resourceB := normalizeKubectlResource(strings.ToLower(strings.TrimSpace(b.Resource)))
	return resourceA == resourceB &&
		strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)) &&
		strings.EqualFold(strings.TrimSpace(a.Namespace), strings.TrimSpace(b.Namespace))
}

func newVerificationRuntimeCheck(
	id string,
	mode contract.VerificationMode,
	target actionTarget,
	expectedState string,
	suggestedCommand string,
	initialDelaySeconds int,
	recheckIntervalSeconds int,
) verificationRuntimeCheck {
	if mode == "" {
		mode = contract.VerificationImmediate
	}
	normalizeVerificationTiming(&initialDelaySeconds, &recheckIntervalSeconds)
	if strings.TrimSpace(expectedState) == "" {
		expectedState = "Verify this specific mutation was applied to the changed resource."
	}
	if strings.TrimSpace(suggestedCommand) == "" {
		suggestedCommand = verificationCommandHint(target)
	}
	return verificationRuntimeCheck{
		ID:                     strings.TrimSpace(id),
		Mode:                   mode,
		Target:                 target,
		ExpectedState:          strings.TrimSpace(expectedState),
		SuggestedCommand:       strings.TrimSpace(suggestedCommand),
		Status:                 contract.VerificationCheckActive,
		InitialDelaySeconds:    initialDelaySeconds,
		RecheckIntervalSeconds: recheckIntervalSeconds,
		MaxRechecks:            defaultVerificationRechecks,
	}
}

func (v *pendingMutationVerification) activeCheck() *verificationRuntimeCheck {
	if v == nil || v.ActiveIndex < 0 || v.ActiveIndex >= len(v.Checks) {
		return nil
	}
	return &v.Checks[v.ActiveIndex]
}

func (v *pendingMutationVerification) isChain() bool {
	return v != nil && (v.Shape == contract.VerificationChain || len(v.Checks) > 1)
}

func (v *pendingMutationVerification) matchesEvidenceControl(control RuntimeControlState) bool {
	if v == nil || v.AwaitingResult {
		return false
	}
	check := v.activeCheck()
	if check == nil || strings.TrimSpace(check.ID) == "" || check.Status != contract.VerificationCheckActive {
		return false
	}
	if v.isChain() {
		return control == RuntimeControlAwaitingMutationVerificationChainEvidence
	}
	return control == RuntimeControlAwaitingMutationVerificationEvidence
}

func (v *pendingMutationVerification) matchesResultControl(control RuntimeControlState) bool {
	if v == nil || !v.AwaitingResult {
		return false
	}
	check := v.activeCheck()
	if check == nil || strings.TrimSpace(check.ID) == "" || len(check.EvidenceRefs) == 0 {
		return false
	}
	if check.Status != contract.VerificationCheckActive && check.Status != contract.VerificationCheckSkipped {
		return false
	}
	if v.isChain() {
		return control == RuntimeControlAwaitingMutationVerificationChainResult
	}
	return control == RuntimeControlAwaitingMutationVerificationResult
}

func (v *pendingMutationVerification) advanceCheck() bool {
	if v == nil {
		return false
	}
	for i := v.ActiveIndex + 1; i < len(v.Checks); i++ {
		if v.Checks[i].Status == contract.VerificationCheckPending {
			v.ActiveIndex = i
			v.Checks[i].Status = contract.VerificationCheckActive
			v.AwaitingResult = false
			return true
		}
	}
	return false
}
