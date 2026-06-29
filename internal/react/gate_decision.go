package react

import "github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"

type GateDecision struct {
	Allow           bool
	Code            string
	UserMessage     string
	ModelCorrection string
	NextState       State
}

func allowGateDecision() GateDecision {
	return GateDecision{Allow: true}
}

func (l *Loop) applyGateDecision(decision GateDecision) bool {
	if decision.Allow {
		return false
	}
	message := decision.ModelCorrection
	if message == "" {
		message = decision.UserMessage
	}
	if message == "" {
		message = "Runtime gate blocked the previous model output. Choose a safe corrected next step."
	}
	code := decision.Code
	if code == "" {
		code = "runtime_gate_blocked"
	}
	if !l.appendCorrectionWithCompaction(code, message) {
		userMessage := decision.UserMessage
		if userMessage == "" {
			userMessage = "runtime gate correction이 반복되어 루프를 중단했습니다."
		}
		l.addMessage(api.MessageSourceAgent, api.MessageTypeError, userMessage+"\n"+message)
		l.pendingCalls = nil
		l.currIteration = 0
		l.state = StateDone
		return true
	}
	l.pendingCalls = nil
	l.currIteration++
	if decision.NextState != 0 {
		l.state = decision.NextState
	} else {
		l.state = StateRunning
	}
	return true
}
