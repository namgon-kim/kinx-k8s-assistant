package orchestrator

import "testing"

func TestRepairUTF8Mojibake(t *testing.T) {
	input := "tests ëª¨ë¥ì POD ì ìí´ ë³´ê³  ìë¤"
	got := repairUTF8Mojibake(input)
	if got != "tests 모류의 POD 을 위해 보고 있다" {
		t.Fatalf("unexpected repair:\n got: %q\nwant: %q", got, "tests 모류의 POD 을 위해 보고 있다")
	}
}

func TestRepairUTF8MojibakeKeepsNormalKorean(t *testing.T) {
	input := "정상 한국어와 tests ëª¨ë¥"
	got := repairUTF8Mojibake(input)
	if got != "정상 한국어와 tests 모류" {
		t.Fatalf("unexpected repair: %q", got)
	}
}
