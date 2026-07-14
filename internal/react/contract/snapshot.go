package contract

import (
	"fmt"
	"strings"
)

type PhaseRef struct {
	Index int
	Name  string
}

type StepRef struct {
	Phase PhaseRef
	Kind  StepKind
	ID    string
	Index int
}

type PhaseRuntime struct {
	RequestGoal string
	Active      PhaseRef
	Phases      []PhaseRuntimeSpec
	Completed   map[int]bool
}

type PhaseRuntimeSpec struct {
	Ref                 PhaseRef
	Goal                string
	CompletionCondition string
	AllowedNext         []string
	Status              PhaseStatus
	Steps               []StepRuntime
}

type StepRuntime struct {
	Ref             StepRef
	Status          StepStatus
	Description     string
	Command         string
	ExpectedOutcome string
}

func (r PhaseRef) Matches(other PhaseRef) bool {
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	}
	if strings.TrimSpace(r.Name) != "" && strings.TrimSpace(other.Name) != "" && !strings.EqualFold(r.Name, other.Name) {
		return false
	}
	return (r.Index != 0 || strings.TrimSpace(r.Name) != "") &&
		(other.Index != 0 || strings.TrimSpace(other.Name) != "")
}

func (r StepRef) Matches(other StepRef) bool {
	if r.Kind != "" && other.Kind != "" && r.Kind != other.Kind {
		return false
	}
	if r.ID != "" && other.ID != "" && r.ID != other.ID {
		return false
	}
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	}
	if (r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != "") &&
		(other.Phase.Index != 0 || strings.TrimSpace(other.Phase.Name) != "") &&
		!r.Phase.Matches(other.Phase) {
		return false
	}
	return r.Kind != "" || r.ID != "" || r.Index != 0
}

func (r PhaseRef) String() string {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Sprintf("#%d", r.Index)
	}
	if r.Index == 0 {
		return strings.TrimSpace(r.Name)
	}
	return fmt.Sprintf("%s#%d", strings.TrimSpace(r.Name), r.Index)
}

func (r StepRef) String() string {
	parts := []string{string(r.Kind)}
	if r.ID != "" {
		parts = append(parts, "id="+r.ID)
	}
	if r.Index != 0 {
		parts = append(parts, fmt.Sprintf("index=%d", r.Index))
	}
	if r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != "" {
		parts = append(parts, "phase="+r.Phase.String())
	}
	return strings.Join(parts, " ")
}

// RuntimeSnapshot is the immutable projection exposed outside session.
type RuntimeSnapshot struct {
	Lifecycle     LoopLifecycleState
	Control       RuntimeControlState
	InputOwner    InputOwner
	OriginalQuery string
	Phase         *PhaseRuntime
	ActiveSteps   []StepRuntime
}
