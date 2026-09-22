// Package sanitize 清洗会进入日志、终端与页面的外来字符串。
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// String 剔除控制字符（C0、C1、DEL）与双向控制符，并在 maxBytes 处按完整字符
// 截断。其他格式字符（ZWNJ、ZWJ 等）保留：它们是合法文字的一部分，剔除会毁掉
// 波斯语、印地语等文字里的正常词。
func String(s string, maxBytes int) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
