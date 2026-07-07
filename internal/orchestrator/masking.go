package orchestrator

import "github.com/namgon-kim/kinx-k8s-assistant/internal/masking"

// MaskSensitiveData는 주어진 문자열에서 민감정보를 마스킹합니다.
func MaskSensitiveData(input string) string {
	return masking.MaskSensitiveData(input)
}

// MaskSecretResource는 kubectl Secret 리소스 출력에서
// data/stringData의 value를 ***REDACTED***로 치환합니다.
// key 이름은 유지합니다.
func MaskSecretResource(input string) string {
	return masking.MaskSecretResource(input)
}
