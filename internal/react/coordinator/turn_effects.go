package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func (l *Loop) executeTurnEffects(ctx context.Context, effects []contract.Effect) error {
	for _, effect := range effects {
		switch effect.Kind {
		case contract.EffectInvokeTool:
			payload, ok := effect.Payload.(contract.InvokeToolEffect)
			if !ok {
				return fmt.Errorf("invalid tool invocation effect payload %T", effect.Payload)
			}
			if err := l.dispatchToolCalls(ctx, payload.DispatchIDs); err != nil {
				return err
			}
		case contract.EffectLookupGuidance:
			payload, ok := effect.Payload.(resourceGuideEffect)
			if !ok {
				return fmt.Errorf("invalid guidance lookup effect payload %T", effect.Payload)
			}
			if err := l.executeGuidanceLookupEffect(ctx, payload); err != nil {
				return err
			}
		case contract.EffectPrepareRequest:
			if err := l.executeRequestPreparationEffect(ctx); err != nil {
				return err
			}
		case contract.EffectResetChat:
			payload, ok := effect.Payload.(chatResetEffect)
			if !ok {
				return fmt.Errorf("invalid chat reset effect payload %T", effect.Payload)
			}
			if err := l.executeChatResetEffect(payload); err != nil {
				return err
			}
		case contract.EffectWait:
			payload, ok := effect.Payload.(verificationWaitEffect)
			if !ok {
				return fmt.Errorf("invalid verification wait effect payload %T", effect.Payload)
			}
			if err := executeVerificationWaitEffect(ctx, payload); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported turn effect %q", effect.Kind)
		}
	}
	return nil
}

type verificationWaitEffect struct {
	Seconds int
	Reason  string
}

func executeVerificationWaitEffect(ctx context.Context, effect verificationWaitEffect) error {
	seconds := effect.Seconds
	if seconds < 0 {
		seconds = 0
	}
	if seconds > maxVerificationDelaySeconds {
		seconds = maxVerificationDelaySeconds
	}
	if seconds == 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C:
		return nil
	}
}

type chatResetEffect struct {
	Reason string
	Code   string
	Limit  int
}

type compactionState struct {
	Reason          string
	Code            string
	Limit           int
	OriginalContent []any
	OriginalHashes  map[string]struct{}
}

func (l *Loop) executeChatResetEffect(effect chatResetEffect) error {
	systemPrompt, toolProfile, chat, err := l.newChatSession()
	if err != nil {
		return l.finishChatResetFailure(effect, err)
	}
	if err := l.finishChatResetSuccess(effect, systemPrompt, toolProfile); err != nil {
		return err
	}
	l.chat = chat
	return nil
}

func (l *Loop) finishChatResetSuccess(effect chatResetEffect, systemPrompt string, toolProfile ToolProfile) (returnErr error) {
	tx, err := l.beginRuntimeTransaction()
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		returnErr = l.commitRuntimeTransaction(tx)
	}()
	pending := l.mutableRuntime().pendingCompaction
	if pending == nil || pending.Reason != effect.Reason || pending.Code != effect.Code {
		return fmt.Errorf("chat reset effect no longer matches pending compaction")
	}
	l.mutableRuntime().systemPrompt = systemPrompt
	l.mutableRuntime().toolProfile = toolProfile
	l.mutableRuntime().contextApproxTokens = estimateContextTokens(systemPrompt)
	l.mutableRuntime().pendingCompaction = nil
	after := l.mutableRuntime().contextApproxTokens + estimateContextTokens(l.mutableRuntime().currChatContent...)
	l.addMessage(
		api.MessageSourceAgent,
		api.MessageTypeText,
		fmt.Sprintf("✓ context compacted: correction state %q preserved. estimated context %d/%d tokens.", effect.Code, after, effect.Limit),
	)
	return nil
}

func (l *Loop) finishChatResetFailure(effect chatResetEffect, resetErr error) (returnErr error) {
	tx, err := l.beginRuntimeTransaction()
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		returnErr = l.commitRuntimeTransaction(tx)
	}()
	pending := l.mutableRuntime().pendingCompaction
	if pending != nil && pending.Reason == effect.Reason && pending.Code == effect.Code {
		l.mutableRuntime().currChatContent = cloneChatContent(pending.OriginalContent)
		l.mutableRuntime().contextBlockHashes = cloneStringSet(pending.OriginalHashes)
		l.mutableRuntime().pendingCompaction = nil
	}
	l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "context compact failed: "+resetErr.Error())
	return nil
}

func (l *Loop) executeRequestPreparationEffect(ctx context.Context) (returnErr error) {
	tx, err := l.beginRuntimeTransaction()
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		returnErr = l.commitRuntimeTransaction(tx)
	}()
	if l.mutableRuntime().requirementAnalysis != nil {
		if err := l.resetChatSessionAfterRequirementAnalysis(); err != nil {
			return fmt.Errorf("prepare accepted requirement chat: %w", err)
		}
	}
	l.classifyAcceptedRequestResource(ctx)
	return nil
}

func (l *Loop) executeGuidanceLookupEffect(ctx context.Context, effect resourceGuideEffect) (returnErr error) {
	tx, err := l.beginRuntimeTransaction()
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			l.rollbackRuntimeTransaction(tx)
			return
		}
		returnErr = l.commitRuntimeTransaction(tx)
	}()
	l.searchAndInjectResourceGuide(ctx, effect.Resource, effect.Query)
	return nil
}
