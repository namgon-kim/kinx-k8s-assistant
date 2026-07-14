package guidance

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func LookupRequired(classification string, injected bool, current contract.PhaseRef) bool {
	return strings.EqualFold(strings.TrimSpace(classification), "crd") && !injected && strings.EqualFold(strings.TrimSpace(current.Name), "resource_guide_lookup")
}
