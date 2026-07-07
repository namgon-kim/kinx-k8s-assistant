package masking

import (
	"regexp"
	"strings"
)

type rule struct {
	pattern *regexp.Regexp
	replace string
}

var sensitiveRules = []rule{
	{
		pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		replace: "***AWS_KEY***",
	},
	{
		pattern: regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret)\s*[=:]\s*\S+`),
		replace: "***AWS_SECRET***",
	},
	{
		pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
		replace: "***JWT***",
	},
	{
		pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^-]*-----END [A-Z ]*PRIVATE KEY-----`),
		replace: "***PRIVATE_KEY***",
	},
	{
		pattern: regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		replace: "***GH_TOKEN***",
	},
	{
		pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`),
		replace: "***OPENAI_KEY***",
	},
	{
		pattern: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]+`),
		replace: "***SLACK_TOKEN***",
	},
	{
		pattern: regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key|apikey)\s*[=:]\s*\S{8,}`),
		replace: "***REDACTED***",
	},
}

func MaskSensitiveData(input string) string {
	result := input
	for _, rule := range sensitiveRules {
		result = rule.pattern.ReplaceAllString(result, rule.replace)
	}
	return result
}

func MaskSecretResource(input string) string {
	if !strings.Contains(input, "kind: Secret") &&
		!strings.Contains(input, "\"kind\": \"Secret\"") {
		return input
	}

	yamlValuePattern := regexp.MustCompile(`(?m)^(\s{2,}[^:\n]+:\s+)[A-Za-z0-9+/=]{8,}$`)
	return yamlValuePattern.ReplaceAllString(input, "${1}***REDACTED***")
}
