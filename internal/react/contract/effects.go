package contract

type EffectKind string

const (
	EffectInvokeTool     EffectKind = "invoke_tool"
	EffectLookupGuidance EffectKind = "lookup_guidance"
	EffectPrepareRequest EffectKind = "prepare_request"
	EffectResetChat      EffectKind = "reset_chat"
	EffectWait           EffectKind = "wait"
)

// Effect describes work that must be executed by coordinator. Flow packages
// return effects instead of performing I/O directly.
type Effect struct {
	Kind    EffectKind
	Payload any
}
