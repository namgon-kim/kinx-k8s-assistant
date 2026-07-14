package guidance

import "strings"

func LookupRequired(classification string, injected bool, phaseName string) bool {
	return strings.EqualFold(strings.TrimSpace(classification), "crd") && !injected &&
		strings.EqualFold(strings.TrimSpace(phaseName), "guidance_lookup")
}
