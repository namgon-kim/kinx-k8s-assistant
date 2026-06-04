package react

import (
	"testing"
	"unicode/utf8"
)

func TestSafeStringSlicesPreserveUTF8(t *testing.T) {
	value := "가나다라마바사"
	head := safeStringHead(value, 5)
	if !utf8.ValidString(head) {
		t.Fatalf("head is invalid UTF-8: %q", head)
	}
	tail := safeStringTail(value, 5)
	if !utf8.ValidString(tail) {
		t.Fatalf("tail is invalid UTF-8: %q", tail)
	}
}
