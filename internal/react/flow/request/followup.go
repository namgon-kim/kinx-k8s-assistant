package request

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func ApplyPriorContext(current contract.RequirementAnalysis, prior *contract.RequestContext) contract.RequirementAnalysis {
	if prior == nil || hasPrimaryResource(current.Resources) {
		return current
	}
	current.Resources = append(current.Resources, contract.RequirementResource{
		Kind:      prior.PrimaryTarget.Resource,
		Name:      prior.PrimaryTarget.Name,
		Namespace: prior.Scope.Namespace,
		Role:      "primary",
		Source:    "previous_context",
	})
	return current
}

func hasPrimaryResource(resources []contract.RequirementResource) bool {
	for _, resource := range resources {
		if strings.EqualFold(strings.TrimSpace(resource.Role), "primary") && strings.TrimSpace(resource.Kind) != "" {
			return true
		}
	}
	return false
}
