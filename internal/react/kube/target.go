package kube

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func NormalizeTarget(target contract.ActionTarget) contract.ActionTarget {
	target.Resource = NormalizeResource(strings.ToLower(strings.TrimSpace(target.Resource)))
	target.Name = strings.TrimSpace(target.Name)
	target.Namespace = strings.TrimSpace(target.Namespace)
	return target
}
