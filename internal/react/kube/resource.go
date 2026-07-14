package kube

import "strings"

func FirstKubectlResourceArg(fields []string, start int) (string, bool) {
	for i := start; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"")
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, "--") {
			if strings.Contains(field, "=") {
				continue
			}
			if FlagRequiresValue(field) && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			if ShortFlagRequiresValue(field) && len(field) == 2 && i+1 < len(fields) {
				i++
			}
			continue
		}
		resource := ResourceKindFromArg(strings.Trim(field, ","))
		return resource, resource != ""
	}
	return "", false
}

func ResourceKindFromArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	if slash := strings.Index(arg, "/"); slash > 0 {
		arg = arg[:slash]
	}
	return strings.Trim(arg, ",")
}

func FlagRequiresValue(flag string) bool {
	return kubectlGlobalFlagRequiresValue(flag) || kubectlCommandFlagRequiresValue(flag)
}

func ShortFlagRequiresValue(flag string) bool {
	return kubectlShortGlobalFlagRequiresValue(flag) || kubectlShortCommandFlagRequiresValue(flag)
}

func kubectlCommandFlagRequiresValue(flag string) bool {
	switch flag {
	case "--filename", "--field-selector", "--label-columns", "--output", "--output-watch-events",
		"--raw", "--selector", "--server-print", "--sort-by", "--template", "--watch-only":
		return true
	default:
		return false
	}
}

func kubectlShortCommandFlagRequiresValue(flag string) bool {
	switch flag {
	case "-f", "-l", "-o", "-R", "-w":
		return true
	default:
		return false
	}
}

var builtinKubernetesResources = map[string]string{
	"bindings": "binding", "componentstatuses": "componentstatus", "pods": "pod", "nodes": "node",
	"services": "service", "endpoints": "endpoint", "limitranges": "limitrange", "deployments": "deployment",
	"replicasets": "replicaset", "statefulsets": "statefulset", "daemonsets": "daemonset", "jobs": "job",
	"cronjobs": "cronjob", "configmaps": "configmap", "secrets": "secret", "namespaces": "namespace",
	"events": "event", "podtemplates": "podtemplate", "replicationcontrollers": "replicationcontroller",
	"resourcequotas": "resourcequota", "ingresses": "ingress", "persistentvolumes": "persistentvolume",
	"persistentvolumeclaims": "persistentvolumeclaim", "serviceaccounts": "serviceaccount", "roles": "role",
	"rolebindings": "rolebinding", "clusterroles": "clusterrole", "clusterrolebindings": "clusterrolebinding",
	"mutatingwebhookconfigurations": "mutatingwebhookconfiguration", "validatingwebhookconfigurations": "validatingwebhookconfiguration",
	"customresourcedefinitions": "customresourcedefinition", "apiservices": "apiservice",
	"controllerrevisions": "controllerrevision", "tokenreviews": "tokenreview",
	"localsubjectaccessreviews": "localsubjectaccessreview", "selfsubjectaccessreviews": "selfsubjectaccessreview",
	"selfsubjectrulesreviews": "selfsubjectrulesreview", "subjectaccessreviews": "subjectaccessreview",
	"horizontalpodautoscalers": "horizontalpodautoscaler", "certificatesigningrequests": "certificatesigningrequest",
	"leases": "lease", "flowschemas": "flowschema", "prioritylevelconfigurations": "prioritylevelconfiguration",
	"ingressclasses": "ingressclass", "networkpolicies": "networkpolicy", "runtimeclasses": "runtimeclass",
	"poddisruptionbudgets": "poddisruptionbudget", "podsecuritypolicies": "podsecuritypolicy",
	"priorityclasses": "priorityclass", "csidrivers": "csidriver", "csinodes": "csinode",
	"csistoragecapacities": "csistoragecapacity", "storageclasses": "storageclass",
	"endpointslices": "endpointslice", "volumeattachments": "volumeattachment",
	"cs": "componentstatus", "cm": "configmap", "ep": "endpoint", "ev": "event", "limits": "limitrange",
	"ns": "namespace", "no": "node", "pvc": "persistentvolumeclaim", "pv": "persistentvolume",
	"po": "pod", "rc": "replicationcontroller", "quota": "resourcequota", "sa": "serviceaccount",
	"svc": "service", "crd": "customresourcedefinition", "crds": "customresourcedefinition",
	"ds": "daemonset", "deploy": "deployment", "rs": "replicaset", "sts": "statefulset",
	"hpa": "horizontalpodautoscaler", "cj": "cronjob", "csr": "certificatesigningrequest",
	"ing": "ingress", "netpol": "networkpolicy", "pdb": "poddisruptionbudget", "psp": "podsecuritypolicy",
	"pc": "priorityclass", "sc": "storageclass",
}

func NormalizeResource(resource string) string {
	if normalized, ok := builtinKubernetesResources[resource]; ok {
		return normalized
	}
	return resource
}

func IsBuiltinResource(resource string) bool {
	_, ok := builtinKubernetesResources[resource]
	if ok {
		return true
	}
	for _, normalized := range builtinKubernetesResources {
		if resource == normalized {
			return true
		}
	}
	return false
}
