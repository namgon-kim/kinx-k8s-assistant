package verification

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

type Requirement struct {
	ID     string
	Target contract.ActionTarget
}
