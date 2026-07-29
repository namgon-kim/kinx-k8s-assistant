package coordinator

import (
	"context"
	"io"
	"strings"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
	"k8s.io/klog/v2"
)

func (l *Loop) waitForInput(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			klog.V(0).InfoS("react waitForInput received EOF")
			if err := l.mutateRuntimeAtomically(func() error {
				if warning := l.finalizePendingMutationVerification("input stream reached EOF", contract.AttemptUnknown); warning != "" {
					l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
				}
				l.applyRuntimeCleanup(cleanupExitPolicy())
				l.addMessage(api.MessageSourceAgent, api.MessageTypeText, "종료합니다.")
				l.transitionControl(RuntimeControlExited)
				return nil
			}); err != nil {
				klog.ErrorS(err, "react EOF cleanup rejected")
			}
			return false
		}
		input, ok := raw.(*api.UserInputResponse)
		if !ok {
			return true
		}
		query := strings.TrimSpace(input.Query)
		if query == "" {
			klog.V(1).InfoS("react waitForInput received empty query")
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			return true
		}
		klog.V(0).InfoS("react waitForInput received query", "query_len", len(query))
		if handled := l.handleMetaQuery(ctx, query); handled {
			return true
		}
		if err := l.startQuery(query); err != nil {
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
		return true
	}
}

func (l *Loop) waitForApproval(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case raw := <-l.input:
		if raw == io.EOF {
			klog.V(0).InfoS("react waitForApproval received EOF")
			if err := l.mutateRuntimeAtomically(func() error {
				if warning := l.finalizePendingMutationVerification("approval input stream reached EOF", contract.AttemptUnknown); warning != "" {
					l.addMessage(api.MessageSourceAgent, api.MessageTypeError, warning)
				}
				l.applyRuntimeCleanup(cleanupExitPolicy())
				l.transitionControl(RuntimeControlExited)
				return nil
			}); err != nil {
				klog.ErrorS(err, "react approval EOF cleanup rejected")
			}
			return false
		}
		choice, ok := raw.(*api.UserChoiceResponse)
		if !ok {
			return true
		}
		klog.V(0).InfoS("react approval choice received", "choice", choice.Choice)
		if err := l.handleApproval(ctx, choice.Choice); err != nil {
			l.transitionControl(RuntimeControlAwaitingUserQuery)
			l.addMessage(api.MessageSourceAgent, api.MessageTypeError, "Error: "+err.Error())
		}
		return true
	}
}
