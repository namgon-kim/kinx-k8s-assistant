package orchestrator

import (
	"strings"
	"unicode/utf8"
)

func sanitizeDisplayText(text string) string {
	return repairUTF8Mojibake(text)
}

func repairUTF8Mojibake(text string) string {
	if !looksLikeUTF8Mojibake(text) {
		return text
	}

	var out strings.Builder
	var segment []rune
	flush := func() {
		if len(segment) == 0 {
			return
		}
		part := string(segment)
		if segmentLooksLikeMojibake(segment) {
			if repaired, ok := decodeLatin1RunesAsUTF8(segment); ok {
				out.WriteString(repaired)
			} else {
				out.WriteString(part)
			}
		} else {
			out.WriteString(part)
		}
		segment = nil
	}

	for _, r := range text {
		if r <= 0xff {
			segment = append(segment, r)
			continue
		}
		flush()
		out.WriteRune(r)
	}
	flush()
	return out.String()
}

func decodeLatin1RunesAsUTF8(runes []rune) (string, bool) {
	bytes := make([]byte, len(runes))
	for i, r := range runes {
		if r > 0xff {
			return "", false
		}
		bytes[i] = byte(r)
	}
	if !utf8.Valid(bytes) {
		return "", false
	}
	return string(bytes), true
}

func looksLikeUTF8Mojibake(text string) bool {
	for _, r := range text {
		if isMojibakeMarker(r) {
			return true
		}
	}
	return false
}

func segmentLooksLikeMojibake(runes []rune) bool {
	for _, r := range runes {
		if isMojibakeMarker(r) {
			return true
		}
	}
	return false
}

func isMojibakeMarker(r rune) bool {
	switch r {
	case 'ì', 'ë', 'ê', 'í', 'ã', 'Â':
		return true
	}
	return r >= 0x80 && r <= 0x9f
}
