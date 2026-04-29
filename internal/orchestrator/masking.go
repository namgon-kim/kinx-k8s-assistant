package orchestrator

import (
	"regexp"
	"strings"
)

// maskingRule은 단일 마스킹 규칙을 정의합니다.
type maskingRule struct {
	name    string
	pattern *regexp.Regexp
	replace string
}

var maskingRules = []maskingRule{
	{
		name:    "AWS Access Key",
		pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		replace: "***AWS_KEY***",
	},
	{
		name:    "AWS Secret Key",
		pattern: regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret)\s*[=:]\s*\S+`),
		replace: "***AWS_SECRET***",
	},
	{
		name:    "JWT Token",
		pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
		replace: "***JWT***",
	},
	{
		name:    "Private Key",
		pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^-]*-----END [A-Z ]*PRIVATE KEY-----`),
		replace: "***PRIVATE_KEY***",
	},
	{
		name:    "GitHub Token",
		pattern: regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		replace: "***GH_TOKEN***",
	},
	{
		name:    "OpenAI API Key",
		pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`),
		replace: "***OPENAI_KEY***",
	},
	{
		name:    "Slack Token",
		pattern: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]+`),
		replace: "***SLACK_TOKEN***",
	},
	{
		name:    "Generic API Key (password= pattern)",
		pattern: regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key|apikey)\s*[=:]\s*\S{8,}`),
		replace: "***REDACTED***",
	},
}

// MaskSensitiveData는 주어진 문자열에서 민감정보를 마스킹합니다.
func MaskSensitiveData(input string) string {
	result := input
	for _, rule := range maskingRules {
		result = rule.pattern.ReplaceAllString(result, rule.replace)
	}
	return result
}

// MaskSecretResource는 kubectl Secret 리소스 출력에서
// data/stringData의 value를 ***REDACTED***로 치환합니다.
// key 이름은 유지합니다.
func MaskSecretResource(input string) string {
	if !strings.Contains(input, "kind: Secret") &&
		!strings.Contains(input, "\"kind\": \"Secret\"") {
		return input
	}

	// YAML 형식: "  key: base64value" 패턴
	yamlValuePattern := regexp.MustCompile(`(?m)^(\s{2,}[^:\n]+:\s+)[A-Za-z0-9+/=]{8,}$`)
	result := yamlValuePattern.ReplaceAllString(input, "${1}***REDACTED***")

	// JSON 형식: "key": "value" 패턴 (data 섹션 이후)
	return result
}
