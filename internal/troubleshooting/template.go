package troubleshooting

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/diagnostic"
)

func renderCommand(tmpl string, target diagnostic.KubernetesTarget, vars map[string]string) string {
	replacements := map[string]string{
		"cluster":        target.Cluster,
		"context":        target.Context,
		"namespace":      target.Namespace,
		"kind":           target.Kind,
		"name":           target.Name,
		"pod_name":       firstNonEmpty(target.PodName, target.Name),
		"container":      target.Container,
		"container_name": target.Container,
		"owner_kind":     target.OwnerKind,
		"owner_name":     target.OwnerName,
		"node_name":      vars["node_name"],
	}
	for k, v := range vars {
		replacements[k] = v
	}

	result := tmpl
	for k, v := range replacements {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func renderSteps(steps []PlanStep, target diagnostic.KubernetesTarget) []PlanStep {
	out := make([]PlanStep, len(steps))
	for i := range steps {
		out[i] = steps[i]
		out[i].Order = i + 1
		if out[i].CommandTemplate != "" {
			out[i].RenderedCommand = renderCommand(out[i].CommandTemplate, target, out[i].Variables)
		}
	}
	return out
}
