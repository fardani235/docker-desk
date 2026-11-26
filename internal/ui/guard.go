package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// The last line of defence for the terminal (docs/DECISIONS.md, AT16).
//
// A frame is drawn from styled text: characters, and the colour sequences
// (SGR) the styles add. Nothing else belongs in it. Text that came from a
// daemon is escaped where it is drawn — but the desk draws a great many such
// strings, in code written at different times, and one that is missed must
// not be able to retitle the window, move the cursor, clear the screen or
// forge a line.
//
// So every line of every frame passes guardLine on its way out: anything
// that takes no cell and is not a colour is removed — control characters, a
// newline or carriage return inside a line, C1 controls, bytes that are not
// UTF-8, and every escape sequence other than SGR, whole. These are exactly the bytes the layout
// counted as zero cells wide, so removing them leaves every line the width
// it was laid out to.
//
// This is not the escaping: an escaped name shows what it contains
// (`evil\x1b]0;…`), while a string that only this guard caught is drawn
// with its sequences gone. It is what makes the missed case harmless.
//
// A colour sequence is kept, and a colour sequence can hide: SGR 8 is
// conceal, and a foreground equal to the background does the same. So what
// survives the guard cannot move, invent or forge text, and it can hide it.
// That is bounded to the line it is on: a line whose last colour sequence is
// not a reset is given one (endLine), so nothing a line set — the desk's
// style or a daemon's sequence in a string nobody escaped — is still set
// when the next line begins. The reset takes no cell.

// sgrReset ends every attribute a line set.
const sgrReset = "\x1b[0m"

// isSGRReset reports whether seq, a whole colour sequence, is the reset and
// nothing else: ESC [ m or ESC [ 0 m.
func isSGRReset(seq string) bool {
	return seq == "\x1b[m" || seq == sgrReset
}

// endLine returns a guarded line with a reset after it, unless no attribute
// is set where it ends: it has no colour sequence, or its last one is a
// reset already — which is how every line the desk styles itself ends.
func endLine(s string) string {
	open := false
	for i := strings.IndexByte(s, 0x1b); i >= 0 && i < len(s); {
		if n := sgrLen(s[i:]); n > 0 {
			open = !isSGRReset(s[i : i+n])
			i += n
		} else {
			i++
		}
		j := strings.IndexByte(s[i:], 0x1b)
		if j < 0 {
			break
		}
		i += j
	}
	if open {
		return s + sgrReset
	}
	return s
}

// sgrLen is the length of the colour sequence s begins with — ESC [, then
// parameters, then m — or 0 when s does not begin with one.
func sgrLen(s string) int {
	if len(s) < 3 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s) && i < 64; i++ {
		switch c := s[i]; {
		case c == 'm':
			return i + 1
		case c >= '0' && c <= '9', c == ';', c == ':':
		default:
			return 0
		}
	}
	return 0
}

// lineIsClean reports that guardLine would change nothing: the common case,
// decided without allocating.
func lineIsClean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0x1b:
			n := sgrLen(s[i:])
			if n == 0 {
				return false
			}
			i += n - 1
		case c < 0x20, c == 0x7f:
			return false
		case c == 0xc2:
			// U+0080–U+009F, the C1 controls, are C2 80 … C2 9F.
			if i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f {
				return false
			}
		}
	}
	return utf8.ValidString(s)
}

// guardLine returns one line of a frame with everything removed that could
// act on the terminal, and no attribute left set at its end: see the note
// above.
func guardLine(s string) string {
	return endLine(stripLine(s))
}

// stripLine removes what the guard removes.
func stripLine(s string) string {
	if lineIsClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			if n := sgrLen(s[i:]); n > 0 {
				b.WriteString(s[i : i+n])
				i += n
				continue
			}
			// Not a colour: the whole sequence goes, as far as the
			// terminal — and the layout's width count — would read it.
			_, _, n, _ := ansi.DecodeSequence(s[i:], 0, nil)
			if n < 1 {
				n = 1
			}
			i += n
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && n <= 1:
				// a byte that is no character: counted as no cell, gone
			case r >= 0x80 && r <= 0x9f:
				// a C1 control: gone
			default:
				b.WriteString(s[i : i+n])
			}
			i += n
		}
	}
	return b.String()
}

// guardFrame passes every line of a frame through guardLine.
func guardFrame(lines []string) []string {
	for i, l := range lines {
		lines[i] = guardLine(l)
	}
	return lines
}
