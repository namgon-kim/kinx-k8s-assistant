package direction

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func InvestigationOption(resource contract.ProblematicResource, summary string) contract.NextDirectionOption {
	return contract.NextDirectionOption{
		Kind:         "investigate_resource",
		Summary:      strings.TrimSpace(summary),
		Why:          strings.TrimSpace(resource.Reason),
		ResourceKind: strings.TrimSpace(resource.Kind),
		ResourceName: strings.TrimSpace(resource.Name),
		Namespace:    strings.TrimSpace(resource.Namespace),
	}
}
