package freshness

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize makes untrusted text — a registry's or a daemon's message — safe
// to draw: escape sequences and control characters are removed, invalid
// UTF-8 becomes U+FFFD, and the result is one line.
func Sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune('�')
		case r == 0x1b:
			i = skipEscape(s, i)
			continue
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
		i += n
	}
	return b.String()
}

// skipEscape returns the index after the escape sequence at s[i]: a CSI
// (ESC [ … final byte), an OSC (ESC ] … BEL or ESC \), or ESC and one byte.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return j
	}
	switch s[j] {
	case '[':
		for j++; j < len(s) && (s[j] < 0x40 || s[j] > 0x7e); j++ {
		}
		return j + 1
	case ']':
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return j
	}
	return j + 1
}
