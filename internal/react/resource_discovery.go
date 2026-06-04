package react

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
)

const (
	resourceClassificationBuiltin = "built_in"
	resourceClassificationCRD     = "crd"
	resourceClassificationUnknown = "unknown"
)

type resourceClassification struct {
	Kind     string
	Source   string
	APIGroup string
	Reason   string
}

type crdList struct {
	Items []struct {
		Spec struct {
			Group string `json:"group"`
			Names struct {
				Plural     string   `json:"plural"`
				Singular   string   `json:"singular"`
				Kind       string   `json:"kind"`
				ShortNames []string `json:"shortNames"`
			} `json:"names"`
		} `json:"spec"`
	} `json:"items"`
}

func (l *Loop) classifyResourceByDiscovery(ctx context.Context, resource string) resourceClassification {
	resource = strings.ToLower(strings.TrimSpace(resource))
	if resource == "" {
		return resourceClassification{Kind: resourceClassificationUnknown, Source: "empty"}
	}
	normalized := normalizeKubectlResource(resource)
	if isBuiltinKubernetesResource(normalized) {
		return resourceClassification{Kind: resourceClassificationBuiltin, Source: "builtin_catalog"}
	}
	if l.resourceDiscoveryCache != nil {
		if cached, ok := l.resourceDiscoveryCache[resource]; ok {
			return cached
		}
	} else {
		l.resourceDiscoveryCache = make(map[string]resourceClassification)
	}

	classification := l.classifyNonBuiltinResourceByDiscovery(ctx, resource)
	l.resourceDiscoveryCache[resource] = classification
	return classification
}

func (l *Loop) classifyNonBuiltinResourceByDiscovery(ctx context.Context, resource string) resourceClassification {
	if l.executor == nil {
		return resourceClassification{
			Kind:   resourceClassificationUnknown,
			Source: "discovery_unavailable",
			Reason: "executor unavailable",
		}
	}

	if crd, ok, err := l.lookupCRDResource(ctx, resource); err == nil && ok {
		return crd
	} else if err != nil {
		return resourceClassification{
			Kind:   resourceClassificationUnknown,
			Source: "crd_discovery_error",
			Reason: err.Error(),
		}
	}

	if exists, err := l.lookupAPIResource(ctx, resource); err == nil && exists {
		return resourceClassification{
			Kind:   resourceClassificationBuiltin,
			Source: "api_resources_non_crd",
		}
	}

	return resourceClassification{
		Kind:   resourceClassificationUnknown,
		Source: "discovery",
		Reason: "resource was not found in CRD discovery",
	}
}

func (l *Loop) lookupCRDResource(ctx context.Context, resource string) (resourceClassification, bool, error) {
	out, err := l.runDiscoveryCommand(ctx, "kubectl get customresourcedefinitions.apiextensions.k8s.io -o json")
	if err != nil {
		return resourceClassification{}, false, err
	}
	var list crdList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return resourceClassification{}, false, fmt.Errorf("parse CRD discovery output: %w", err)
	}
	for _, item := range list.Items {
		names := []string{
			item.Spec.Names.Plural,
			item.Spec.Names.Singular,
			item.Spec.Names.Kind,
			item.Spec.Names.Plural + "." + item.Spec.Group,
		}
		names = append(names, item.Spec.Names.ShortNames...)
		if nameMatchesResource(resource, names) {
			return resourceClassification{
				Kind:     resourceClassificationCRD,
				Source:   "crd_discovery",
				APIGroup: item.Spec.Group,
			}, true, nil
		}
	}
	return resourceClassification{}, false, nil
}

func (l *Loop) lookupAPIResource(ctx context.Context, resource string) (bool, error) {
	out, err := l.runDiscoveryCommand(ctx, "kubectl api-resources -o name")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if nameMatchesResource(resource, []string{line, strings.Split(line, ".")[0]}) {
			return true, nil
		}
	}
	return false, nil
}

func (l *Loop) runDiscoveryCommand(ctx context.Context, command string) (string, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	env := os.Environ()
	if l.cfg != nil && strings.TrimSpace(l.cfg.Kubeconfig) != "" {
		kubeconfig, err := tools.ExpandShellVar(l.cfg.Kubeconfig)
		if err != nil {
			return "", err
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
	}

	result, err := l.executor.Execute(discoveryCtx, command, env, l.workDir)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.Error != "" {
		errText := strings.TrimSpace(firstNonEmptyGuideText(result.Stderr, result.Error))
		if errText == "" {
			errText = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return "", fmt.Errorf("%s failed: %s", command, errText)
	}
	return result.Stdout, nil
}

func nameMatchesResource(resource string, names []string) bool {
	resource = strings.ToLower(strings.TrimSpace(resource))
	resourceBase := strings.Split(resource, ".")[0]
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if resource == name || resourceBase == name {
			return true
		}
		if strings.Split(name, ".")[0] == resource {
			return true
		}
	}
	return false
}
