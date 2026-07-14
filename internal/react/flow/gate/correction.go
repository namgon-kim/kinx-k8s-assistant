package gate

type CorrectionMode string

const (
	CorrectionNone            CorrectionMode = "none"
	CorrectionAppendCompacted CorrectionMode = "append_compacted"
	CorrectionAppendPlain     CorrectionMode = "append_plain"
	CorrectionUserMessageOnly CorrectionMode = "user_message_only"
)

func Correction(outcome Outcome) string {
	if outcome.ModelCorrection != "" {
		return outcome.ModelCorrection
	}
	return outcome.UserMessage
}
