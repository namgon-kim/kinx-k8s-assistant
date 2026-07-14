package direction

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

func InvestigationOptions(report contract.FinalReport) []contract.NextDirectionOption {
	options := make([]contract.NextDirectionOption, 0, len(report.ProblematicResources))
	for _, resource := range report.ProblematicResources {
		options = append(options, contract.NextDirectionOption{
			Kind:         "investigate_resource",
			Summary:      resource.Kind + "/" + resource.Name,
			Why:          resource.Reason,
			ResourceKind: resource.Kind,
			ResourceName: resource.Name,
			Namespace:    resource.Namespace,
		})
	}
	return options
}
