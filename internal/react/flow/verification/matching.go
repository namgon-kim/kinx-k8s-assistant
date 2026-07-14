package verification

type MatchEvidence struct {
	Namespace bool
	Resource  bool
	Name      bool
}

func Matches(requirement Requirement, evidence MatchEvidence) bool {
	return (requirement.Target.Namespace == "" || evidence.Namespace) &&
		(requirement.Target.Resource == "" || evidence.Resource) &&
		(requirement.Target.Name == "" || evidence.Name)
}
