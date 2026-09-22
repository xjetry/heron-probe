package sanitize

import "testing"

func TestStringStripsControlsKeepsFormatChars(t *testing.T) {
	got := String("a\x00b\x1fc\x7fd\u0085\u009b‮‌é", 64)
	if got != "abcd‌é" {
		t.Fatalf("got %q", got)
	}
}

func TestStringTruncatesOnRuneBoundary(t *testing.T) {
	if got := String("ééé", 5); got != "éé" { // 每个 é 两字节，第三个放不下就整个不放
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
	if got := String("abc", 0); got != "" {
		t.Fatalf("got %q", got)
	}
}
