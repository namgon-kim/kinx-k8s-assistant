package verification

import "strings"

func MatchesCommand(command string, requirement Requirement) bool {
	command = strings.ToLower(command)
	return strings.Contains(command, strings.ToLower(requirement.Target.Resource)) &&
		(requirement.Target.Name == "" || strings.Contains(command, strings.ToLower(requirement.Target.Name))) &&
		(requirement.Target.Namespace == "" || strings.Contains(command, strings.ToLower(requirement.Target.Namespace)))
}
