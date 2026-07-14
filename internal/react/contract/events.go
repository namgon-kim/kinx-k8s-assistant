package contract

// Event is immutable input consumed by a workflow reducer.
type Event interface {
	eventName() string
}

type ModelTurnEvent struct {
	Text  string
	Calls []FunctionCall
}

func (ModelTurnEvent) eventName() string { return "model_turn" }

type UserInputEvent struct {
	Kind   UserInputKind
	Text   string
	Choice int
}

func (UserInputEvent) eventName() string { return "user_input" }

type ToolObservationEvent struct {
	Call   FunctionCall
	Result map[string]any
	Err    error
}

func (ToolObservationEvent) eventName() string { return "tool_observation" }

// FunctionCall is the provider-neutral function-call contract used by flows.
type FunctionCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}
