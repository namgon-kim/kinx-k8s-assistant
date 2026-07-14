package contract

type EffectKind string

const (
	EffectSendModel       EffectKind = "send_model"
	EffectInvokeTool      EffectKind = "invoke_tool"
	EffectRequestInput    EffectKind = "request_input"
	EffectRequestApproval EffectKind = "request_approval"
	EffectEmitMessage     EffectKind = "emit_message"
	EffectTranslateText   EffectKind = "translate_text"
)

// Effect describes work that must be executed by coordinator. Flow packages
// return effects instead of performing I/O directly.
type Effect struct {
	Kind    EffectKind
	Payload any
}
