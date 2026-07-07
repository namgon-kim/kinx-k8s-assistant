package react

// SkipStep marks a stored guide or mutation evidence step as terminal without
// requiring successful evidence. General action steps cannot be skipped.
func (l *Loop) SkipStep(ref StepRef) bool {
	switch ref.Kind {
	case StepResourceGuideDiagnostic:
		return l.skipGuideRuntimeStep(ref)
	case StepMutationEvidenceRequirement:
		return l.skipMutationEvidenceRuntimeStep(ref)
	default:
		return false
	}
}

func (l *Loop) skipGuideRuntimeStep(ref StepRef) bool {
	if l == nil || l.guideStepState == nil || ref.Index <= 0 || ref.Index > l.guideStepState.TotalSteps {
		return false
	}
	if l.guideStepState.Skipped == nil {
		l.guideStepState.Skipped = map[int]bool{}
	}
	if l.guideStepState.Completed[ref.Index] || l.guideStepState.Skipped[ref.Index] {
		return false
	}
	l.guideStepState.Skipped[ref.Index] = true
	return true
}

func (l *Loop) skipMutationEvidenceRuntimeStep(ref StepRef) bool {
	if l == nil || l.pendingMutationVerification == nil || ref.ID == "" {
		return false
	}
	if !l.pendingMutationVerification.hasRequirement(ref.ID) {
		return false
	}
	if l.pendingMutationVerification.Skipped == nil {
		l.pendingMutationVerification.Skipped = map[string]bool{}
	}
	if l.pendingMutationVerification.Satisfied[ref.ID] || l.pendingMutationVerification.Skipped[ref.ID] {
		return false
	}
	l.pendingMutationVerification.Skipped[ref.ID] = true
	if l.pendingMutationVerification.allSatisfied() {
		l.pendingMutationVerification.AwaitingResult = true
	}
	return true
}

func (v pendingMutationVerification) hasRequirement(id string) bool {
	for _, req := range v.Requirements {
		if req.ID == id {
			return true
		}
	}
	return false
}
