package files

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Escape renders a name — an archive's or the daemon's, both chosen by
// whoever built the image or runs the container — as text that cannot act
// on a terminal or forge a line of a listing (D8): a newline is `\n`, every
// other control character, DEL and each byte of invalid UTF-8 is `\xNN`, a
// C1 control is `\u00NN`, and a backslash is doubled so that no name can
// pass for an escape. Everything else is kept as it is. The screen draws
// this and the export writes it, so the two always agree, and the mapping
// can be undone: no byte of a name is lost or invented.
func Escape(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' || c >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}
