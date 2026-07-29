package contract

import (
	"fmt"
	"strings"
)

type PhaseRef struct {
	ID        string `json:"id,omitempty"`
	LineageID string `json:"lineage_id,omitempty"`
	Index     int    `json:"index,omitempty"`
	Name      string `json:"name,omitempty"`
}

type StepRef struct {
	Phase         PhaseRef `json:"phase,omitempty"`
	Kind          StepKind `json:"kind,omitempty"`
	ID            string   `json:"id,omitempty"`
	GoalLineageID string   `json:"goal_lineage_id,omitempty"`
	Index         int      `json:"index,omitempty"`
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
	matched := false
	if r.ID != "" && other.ID != "" && r.ID != other.ID {
		return false
	} else if r.ID != "" && other.ID != "" {
		matched = true
	}
	if r.LineageID != "" && other.LineageID != "" && r.LineageID != other.LineageID {
		return false
	} else if r.LineageID != "" && other.LineageID != "" {
		matched = true
	}
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	} else if r.Index != 0 && other.Index != 0 {
		matched = true
	}
	if strings.TrimSpace(r.Name) != "" && strings.TrimSpace(other.Name) != "" && !strings.EqualFold(r.Name, other.Name) {
		return false
	} else if strings.TrimSpace(r.Name) != "" && strings.TrimSpace(other.Name) != "" {
		matched = true
	}
	return matched
}

func (r StepRef) Matches(other StepRef) bool {
	matched := false
	if r.Kind != "" && other.Kind != "" && r.Kind != other.Kind {
		return false
	} else if r.Kind != "" && other.Kind != "" {
		matched = true
	}
	if r.ID != "" && other.ID != "" && r.ID != other.ID {
		return false
	} else if r.ID != "" && other.ID != "" {
		matched = true
	}
	if r.GoalLineageID != "" && other.GoalLineageID != "" && r.GoalLineageID != other.GoalLineageID {
		return false
	} else if r.GoalLineageID != "" && other.GoalLineageID != "" {
		matched = true
	}
	if r.Index != 0 && other.Index != 0 && r.Index != other.Index {
		return false
	} else if r.Index != 0 && other.Index != 0 {
		matched = true
	}
	rHasPhase := r.Phase.ID != "" || r.Phase.LineageID != "" || r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != ""
	otherHasPhase := other.Phase.ID != "" || other.Phase.LineageID != "" || other.Phase.Index != 0 || strings.TrimSpace(other.Phase.Name) != ""
	if rHasPhase && otherHasPhase && !r.Phase.Matches(other.Phase) {
		return false
	}
	return matched
}

func (r PhaseRef) String() string {
	if r.ID != "" {
		return r.ID
	}
	if strings.TrimSpace(r.Name) == "" {
		if r.LineageID != "" {
			return "lineage=" + r.LineageID
		}
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
	if r.GoalLineageID != "" {
		parts = append(parts, "lineage="+r.GoalLineageID)
	}
	if r.Index != 0 {
		parts = append(parts, fmt.Sprintf("index=%d", r.Index))
	}
	if r.Phase.ID != "" || r.Phase.LineageID != "" || r.Phase.Index != 0 || strings.TrimSpace(r.Phase.Name) != "" {
		parts = append(parts, "phase="+r.Phase.String())
	}
	return strings.Join(parts, " ")
}
