// Package escape renders untrusted names as text that cannot act on a
// terminal or forge a line. It has no dependencies, so that every layer —
// the index, the transfer, the fake daemon — can share the one mapping.
package escape

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Name renders a name — an archive's or the daemon's, both chosen by
// whoever built the image or runs the container — as text that cannot act
// on a terminal or forge a line of a listing (D8): a newline is `\n`, every
// other control character, DEL and each byte of invalid UTF-8 is `\xNN`, a
// C1 control is `\u00NN`, a character that reorders what is drawn around it
// (Reorders) is `\uNNNN`, and a backslash is doubled so that no name can
// pass for an escape. Everything else is kept as it is. The screen draws
// this and the export writes it, so the two always agree, and the mapping
// can be undone: no byte of a name is lost or invented.
func Name(s string) string {
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
		case Reorders(r):
			b.WriteString(Rune(r))
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// Reorders reports the characters that draw nothing themselves and change
// how the text around them reads: every format character (the bidirectional
// marks, embeddings, overrides and isolates, zero-width spaces and joiners,
// the soft hyphen, the byte order mark, tag characters) and the line and
// paragraph separators. Shown raw, they let a name or a file display as
// other than it is (`gnp.exe` for `exe.png`) or hide text, so they are shown
// as escapes.
func Reorders(r rune) bool {
	return unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// Rune is the visible escape of a character outside ASCII: `\uNNNN`, or
// `\UNNNNNNNN` above the basic plane.
func Rune(r rune) string {
	if r > 0xffff {
		return fmt.Sprintf(`\U%08x`, r)
	}
	return fmt.Sprintf(`\u%04x`, r)
}
