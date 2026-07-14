package direction

import (
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func Normalize(value contract.NextDirections) contract.NextDirections {
	options := make([]contract.NextDirectionOption, 0, len(value.Options))
	for _, option := range value.Options {
		option.Kind = strings.ToLower(strings.TrimSpace(option.Kind))
		option.Summary = strings.TrimSpace(option.Summary)
		if option.Summary == "" {
			continue
		}
		switch option.Kind {
		case "another_guide":
			if strings.TrimSpace(option.ResourceFamily) == "" || strings.TrimSpace(option.ProblemFocus) == "" {
				continue
			}
		case "different_approach":
			if strings.TrimSpace(option.Instruction) == "" {
				continue
			}
		default:
			continue
		}
		options = append(options, option)
		if len(options) == 3 {
			break
		}
	}
	value.Options = options
	return value
}
