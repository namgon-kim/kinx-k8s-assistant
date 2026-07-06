package react

func (l *Loop) ActiveStepRefs() []StepRef {
	var refs []StepRef
	for _, step := range l.RuntimeSnapshot().ActiveSteps {
		if step.Status == StepActive {
			refs = append(refs, step.Ref)
		}
	}
	return refs
}

func (l *Loop) RemainingStepRefs(kind StepKind) []StepRef {
	var refs []StepRef
	for _, step := range l.RuntimeSnapshot().ActiveSteps {
		if kind != "" && step.Ref.Kind != kind {
			continue
		}
		if step.Status == StepCompleted || step.Status == StepSkipped {
			continue
		}
		refs = append(refs, step.Ref)
	}
	return refs
}

func (l *Loop) MarkStepCompleted(ref StepRef) bool {
	switch ref.Kind {
	case StepResourceGuideDiagnostic:
		return l.markGuideRuntimeStepCompleted(ref)
	case StepMutationEvidenceRequirement:
		return l.markMutationEvidenceRuntimeStepCompleted(ref)
	default:
		return false
	}
}

func (l *Loop) RetryStep(ref StepRef) bool {
	switch ref.Kind {
	case StepResourceGuideDiagnostic:
		return l.retryGuideRuntimeStep(ref)
	case StepMutationEvidenceRequirement:
		return l.retryMutationEvidenceRuntimeStep(ref)
	default:
		return false
	}
}

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

func (l *Loop) markGuideRuntimeStepCompleted(ref StepRef) bool {
	if l == nil || l.guideStepState == nil || ref.Index <= 0 || ref.Index > l.guideStepState.TotalSteps {
		return false
	}
	if l.guideStepState.Completed == nil {
		l.guideStepState.Completed = map[int]bool{}
	}
	if l.guideStepState.Completed[ref.Index] || l.guideStepState.Skipped[ref.Index] {
		return false
	}
	l.guideStepState.Completed[ref.Index] = true
	return true
}

func (l *Loop) retryGuideRuntimeStep(ref StepRef) bool {
	if l == nil || l.guideStepState == nil || ref.Index <= 0 || ref.Index > l.guideStepState.TotalSteps {
		return false
	}
	if (l.guideStepState.Completed == nil || !l.guideStepState.Completed[ref.Index]) &&
		(l.guideStepState.Skipped == nil || !l.guideStepState.Skipped[ref.Index]) {
		return false
	}
	delete(l.guideStepState.Completed, ref.Index)
	delete(l.guideStepState.Skipped, ref.Index)
	return true
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

func (l *Loop) markMutationEvidenceRuntimeStepCompleted(ref StepRef) bool {
	if l == nil || l.pendingMutationVerification == nil || ref.ID == "" {
		return false
	}
	if !l.pendingMutationVerification.hasRequirement(ref.ID) {
		return false
	}
	if l.pendingMutationVerification.Satisfied == nil {
		l.pendingMutationVerification.Satisfied = map[string]bool{}
	}
	if l.pendingMutationVerification.Satisfied[ref.ID] || l.pendingMutationVerification.Skipped[ref.ID] {
		return false
	}
	l.pendingMutationVerification.Satisfied[ref.ID] = true
	if l.pendingMutationVerification.allSatisfied() {
		l.pendingMutationVerification.AwaitingResult = true
	}
	return true
}

func (l *Loop) retryMutationEvidenceRuntimeStep(ref StepRef) bool {
	if l == nil || l.pendingMutationVerification == nil || ref.ID == "" {
		return false
	}
	if !l.pendingMutationVerification.hasRequirement(ref.ID) {
		return false
	}
	if (l.pendingMutationVerification.Satisfied == nil || !l.pendingMutationVerification.Satisfied[ref.ID]) &&
		(l.pendingMutationVerification.Skipped == nil || !l.pendingMutationVerification.Skipped[ref.ID]) {
		return false
	}
	delete(l.pendingMutationVerification.Satisfied, ref.ID)
	delete(l.pendingMutationVerification.Skipped, ref.ID)
	l.pendingMutationVerification.AwaitingResult = false
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
