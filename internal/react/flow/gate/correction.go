package gate

type CorrectionMode string

const (
	CorrectionNone            CorrectionMode = "none"
	CorrectionAppendCompacted CorrectionMode = "append_compacted"
	CorrectionAppendPlain     CorrectionMode = "append_plain"
	CorrectionUserMessageOnly CorrectionMode = "user_message_only"
)

func Message(modelCorrection, userMessage, fallback string) string {
	if modelCorrection != "" {
		return modelCorrection
	}
	if userMessage != "" {
		return userMessage
	}
	return fallback
}
