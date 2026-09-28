package api

import (
	"testing"
	"unicode/utf8"
)

func TestLogoBudgetEncodingContract(t *testing.T) {
	t.Run("prefixes", func(t *testing.T) {
		for _, mediaType := range logoTypes {
			prefix := "data:" + mediaType + ";base64,"
			if got, want := jsonStringBytes(prefix), len(prefix)+2; got != want {
				t.Errorf("logo prefix %q JSON bytes=%d, want unescaped bytes=%d", prefix, got, want)
			}
		}
	})
	t.Run("alphabet", func(t *testing.T) {
		for r := rune(0); r <= utf8.MaxRune; r++ {
			if !isBase64Char(r) {
				continue
			}
			value := string(r)
			if got, want := jsonStringBytes(value), len(value)+2; got != want {
				t.Errorf("logo alphabet U+%04X JSON bytes=%d, want unescaped bytes=%d", r, got, want)
			}
		}
	})
	t.Run("raw_payloads", func(t *testing.T) {
		for _, payload := range []string{"<svg/>", "\x01"} {
			logo := "data:image/svg+xml;utf8," + payload
			if err := checkLogo(logo); err == nil {
				t.Errorf("checkLogo accepted non-base64 payload %q requiring JSON escaping", payload)
			}
		}
	})
}
