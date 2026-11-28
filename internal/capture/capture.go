// Package capture lays out the text a pane holds as a page: the bytes `W`
// writes (docs/DECISIONS.md, PC1–PC6).
//
// A capture is its own class of file, not a report. A report (package
// report) is computed evidence: deterministic, and free of times, host
// paths, daemon addresses and environment values. A capture is the text the
// desk holds, kept as it is — times and the daemon's values included — so
// that it outlives the desk's alternate screen. What this package adds is
// only what turns screen text into a document:
//
//   - every escape sequence is removed (colour is the desk's, not the
//     daemon's, and a file that paints a terminal is not text), and with
//     it every control character that could introduce one or act on a
//     terminal when the file is printed: ESC, the other C0 controls but the
//     tab, DEL and the C1 controls. What the desk draws visibly — `\x1b`,
//     `\n` as escaped text — is kept: it is not a control character;
//   - a character that draws nothing and reorders what is around it (a
//     format character such as U+202E, a line or paragraph separator) is
//     written as its visible escape, `\uNNNN`, as the desk writes it in a
//     name: shown raw, it lets a line read as other than it is;
//   - padding is trimmed from the right of every line;
//   - a text capture begins with `#` lines that say what it is;
//   - the file ends in exactly one newline.
//
// JSON is the daemon's document, indented, with no header: JSON has no
// comments. It stays the same document.
//
// The package imports the standard library only: nothing in it can reach the
// daemon, the UI or a file. The UI hands it strings and writes the bytes it
// returns through report.WriteFile.
package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// PageWidth is the width prose is wrapped at in a capture: a readable line
// in an editor that still leaves values whole. It is not the terminal's.
const PageWidth = 200

// Header is what a text capture says about itself, one fact a line.
type Header struct {
	Tool   string   // "docker-desk v1.2.3"
	Pane   string   // "Logs", "Events", …
	Object string   // what the pane is about, as the desk names it
	Facts  []string // anything else: a filter and its counts, a snapshot's time, a bound
}

// pageNote is the last header line of every text capture.
const pageNote = "laid out for a page, not for the screen: no value cut, prose wrapped at " +
	"200 columns, colour removed, padding trimmed"

// headerEnd says where the header stops: a line of the pane's text may
// itself begin with #.
const headerEnd = "this header ends at the first line that is only #; every line after it is the pane's text"

// classNote says what class of file this is, so that a reader of the file —
// not only of the dialog — knows it is not a report.
const classNote = "a capture: the text as the desk holds it, times and the daemon's values " +
	"included; not a report"

// Text is a text capture: the header, then each line made plain (Plain), and
// one trailing newline. A line holding newlines becomes several lines. Blank
// lines at the end are dropped, so the file ends in exactly one newline.
func Text(h Header, lines []string) []byte {
	var b strings.Builder
	head := func(k, v string) {
		v = headerValue(v)
		if k != "" {
			v = k + ": " + v
		}
		b.WriteString(strings.TrimRight("# "+v, " "))
		b.WriteByte('\n')
	}
	head("", orDefault(h.Tool, "docker-desk")+" — pane capture")
	head("pane", h.Pane)
	head("object", h.Object)
	for _, f := range h.Facts {
		head("", f)
	}
	head("", classNote)
	head("", pageNote)
	head("", headerEnd)
	b.WriteString("#\n")

	var body []string
	for _, l := range lines {
		for _, part := range strings.Split(l, "\n") {
			body = append(body, Plain(part))
		}
	}
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	for _, l := range body {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// headerValue is one header fact on one line: plain, and with any line
// break turned into a space, so that no value can end the header early or
// forge a line of the body.
func headerValue(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.TrimSpace(Plain(s))
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// Plain is one line of drawn text as page text: escape sequences removed,
// then every control character but the tab (C0, DEL, C1) removed, every
// character that reorders what is drawn around it written as its visible
// escape (\uNNNN, or \UNNNNNNNN above the basic plane), invalid UTF-8 made
// U+FFFD, and the spaces on the right trimmed.
func Plain(s string) string {
	s = strip(s)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f || c >= 0x80 {
			clean = false
			break
		}
	}
	if !clean {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			switch {
			case (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f):
				continue
			case reorders(r):
				if r > 0xffff {
					fmt.Fprintf(&b, `\U%08x`, r)
				} else {
					fmt.Fprintf(&b, `\u%04x`, r)
				}
				continue
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	return strings.TrimRight(s, " ")
}

// strip removes escape sequences: CSI (ESC [ … final), the string
// sequences OSC, DCS, SOS, PM and APC (to BEL or ST; unterminated, to the
// end of the line), and the short escapes (ESC, intermediates, final). A lone
// ESC is removed too. Whatever is left of a malformed sequence is ordinary
// text and stays: without its ESC it cannot act.
func strip(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		i += seqLen(s[i:])
	}
	return b.String()
}

// seqLen is the length of the escape sequence at the start of s (s[0] is
// ESC); at least 1.
func seqLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters 0x30–0x3f, intermediates 0x20–0x2f, final 0x40–0x7e
		i := 2
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
		return i // malformed: drop what was read; the rest is text
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: to BEL or ST (ESC \ or U+009C)
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0xc2 && i+1 < len(s) && s[i+1] == 0x9c {
				return i + 2
			}
			if s[i] == 0x1b {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // another sequence begins: this one ended unterminated
			}
		}
		return len(s)
	default: // ESC, intermediates 0x20–0x2f, final 0x30–0x7e
		i := 1
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			return i + 1
		}
		return i
	}
}

// ErrNotJSON is why an inspect document is not written: it does not parse.
var ErrNotJSON = errors.New("the daemon's document is not JSON")

// JSON is the daemon's document indented two spaces, with no header and
// one trailing newline. It is the same document: a character JSON allows
// raw inside a string but that could act on a terminal or reorder what is
// drawn around it — DEL, a C1 control, a format character such as U+202E,
// a line or paragraph separator — is written as JSON's own \uNNNN, and a
// byte that is no character as �, which is what any decoder makes of
// it. An input that is not one JSON value returns ErrNotJSON and no bytes.
func JSON(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	out := bytes.TrimRight(buf.Bytes(), " \t\r\n")
	if len(out) == 0 {
		return nil, ErrNotJSON
	}
	out = inertJSON(out)
	if !json.Valid(out) {
		return nil, ErrNotJSON // cannot happen: the escapes are JSON's own
	}
	return append(out, '\n'), nil
}

// inertJSON rewrites what inertRune names as \u escapes. Outside a string
// valid JSON holds ASCII only, so every such rune is inside one, where the
// escape means the same character.
func inertJSON(b []byte) []byte {
	clean := true
	for _, c := range b {
		if c == 0x7f || c >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return b
	}
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n <= 1:
			out = append(out, `�`...)
		case inertRune(r):
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			} else {
				out = fmt.Appendf(out, `\u%04x`, r)
			}
		default:
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}

func inertRune(r rune) bool {
	return r == 0x7f || (r >= 0x80 && r <= 0x9f) || reorders(r)
}

// reorders reports the characters that draw nothing themselves and change
// how the text around them reads — escape.Reorders' set, restated here so
// that this package needs nothing of the desk's.
func reorders(r rune) bool {
	return unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}
