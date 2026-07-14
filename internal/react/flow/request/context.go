package request

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func ContextFromAnalysis(analysis contract.RequirementAnalysis) (contract.RequestContext, bool) {
	for _, resource := range analysis.Resources {
		if strings.EqualFold(strings.TrimSpace(resource.Role), "primary") && strings.TrimSpace(resource.Kind) != "" {
			return contract.RequestContext{
				PrimaryTarget: contract.RequestPrimaryTarget{Resource: strings.TrimSpace(resource.Kind), Name: strings.TrimSpace(resource.Name)},
				Scope:         contract.RequestScope{Namespace: strings.TrimSpace(resource.Namespace)},
			}, true
		}
	}
	return contract.RequestContext{}, false
}
