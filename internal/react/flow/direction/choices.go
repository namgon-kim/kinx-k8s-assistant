package direction

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func Normalize(value contract.NextDirections) contract.NextDirections {
	options := make([]contract.NextDirectionOption, 0, len(value.Options))
	for _, option := range value.Options {
		option.Kind = strings.TrimSpace(option.Kind)
		option.Summary = strings.TrimSpace(option.Summary)
		if option.Kind != "" && option.Summary != "" {
			options = append(options, option)
		}
	}
	value.Note = strings.TrimSpace(value.Note)
	value.Options = options
	return value
}
