package protocol

import (
	"fmt"
	"strings"
)

func ExtractJSON(input string) (string, bool) {
	const marker = "```json"
	first := strings.Index(input, marker)
	last := strings.LastIndex(input, "```")
	if first == -1 || last == -1 || first == last {
		return "", false
	}
	return input[first+len(marker) : last], true
}

func RepairJSONStrings(input string) string {
	var out strings.Builder
	out.Grow(len(input))
	inString := false
	escaped := false
	for index := 0; index < len(input); index++ {
		char := input[index]
		if inString {
			if escaped {
				out.WriteByte(char)
				escaped = false
				continue
			}
			switch char {
			case '\\':
				if index+1 < len(input) && input[index+1] == '\'' {
					out.WriteByte('\'')
					index++
					continue
				}
				out.WriteByte(char)
				escaped = true
			case '"':
				if isStringTerminator(input, index) {
					out.WriteByte(char)
					inString = false
				} else {
					out.WriteString(`\"`)
				}
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if char < 0x20 {
					out.WriteString(fmt.Sprintf(`\u%04x`, char))
					continue
				}
				out.WriteByte(char)
			}
			continue
		}
		if char == '"' {
			inString = true
		}
		out.WriteByte(char)
	}
	return out.String()
}

func isStringTerminator(input string, quoteIndex int) bool {
	for index := quoteIndex + 1; index < len(input); index++ {
		switch input[index] {
		case ' ', '\n', '\r', '\t':
			continue
		case ':', ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}
