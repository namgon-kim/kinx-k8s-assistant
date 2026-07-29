package verification

import "github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"

func CanRequestResult(qualification contract.EvidenceQualification) bool {
	switch qualification {
	case contract.EvidenceStateBearing, contract.EvidenceAccessBlocker:
		return true
	default:
		return false
	}
}

func SupportsResult(
	status contract.VerificationResultStatus,
	qualifications map[contract.EvidenceQualification]bool,
) bool {
	switch status {
	case contract.VerificationSatisfied, contract.VerificationWaiting:
		return qualifications[contract.EvidenceStateBearing]
	case contract.VerificationFailed:
		return qualifications[contract.EvidenceStateBearing] ||
			qualifications[contract.EvidenceAccessBlocker]
	default:
		return false
	}
}
