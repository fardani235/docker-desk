package files

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/escape"
)

// The reader's bounds (docs/DECISIONS.md, round twenty-four). They are
// counted in the file's own bytes, before anything is escaped.
const (
	// PreviewMaxBytes is how much of a file is read: 1 MiB.
	PreviewMaxBytes = 1 << 20
	// PreviewMaxLines is how many lines are kept.
	PreviewMaxLines = 10_000
	// PreviewMaxLine is how much of one line is kept: 2 KiB.
	PreviewMaxLine = 2 << 10
)

// previewTab is the tab stop tabs are expanded to.
const previewTab = 8

// Preview is what the reader shows of one file: its text, made inert, or
// the fact that it is not text. It holds no more than the bounds allow, and
// says which of them were reached.
type Preview struct {
	// Text is the lines, escaped: no control character, no escape sequence
	// and no invalid byte is in them. nil when Binary; empty, not nil, for
	// an empty file.
	Text   []string
	Binary bool
	// Size is the size the caller was told (a tar header's); Read is how
	// many bytes were taken from the stream.
	Size int64
	Read int64
	// Lines is len(Text).
	Lines int
	// CutBytes: the file is longer than PreviewMaxBytes, and the rest was
	// not read. CutLines: it has more than PreviewMaxLines lines, and the
	// rest were dropped. CutLine: at least one line was longer than
	// PreviewMaxLine and was kept to that; LongLines counts them.
	CutBytes  bool
	CutLines  bool
	CutLine   bool
	LongLines int
}

// Whole reports whether the preview is the whole file: nothing was cut.
func (p Preview) Whole() bool { return !p.CutBytes && !p.CutLines && !p.CutLine }

// ReadPreview reads at most PreviewMaxBytes of r — a file of size bytes —
// and returns what the reader may show of it.
//
// Text is valid UTF-8 with no NUL byte; anything else is Binary and carries
// no bytes at all. Text is made inert: a CR before a newline is dropped,
// tabs are expanded, and every other control character, DEL, C1 control
// and invisible format character (a bidirectional override, a zero-width
// space, a line separator: escape.Reorders) becomes a visible escape
// (`\x1b`, `\u202e`), so that nothing a file holds can act on a terminal
// or reorder what is drawn.
//
// It stops at ctx's end and then returns ctx's error and nothing else. A
// stream that ends before the size it was given is an error: a preview
// never claims bytes it did not read. r is not closed.
func ReadPreview(ctx context.Context, r io.Reader, size int64) (Preview, error) {
	if size < 0 {
		return Preview{}, fmt.Errorf("a file cannot have a size of %d bytes", size)
	}
	p := Preview{Size: size}
	// One byte past the bound tells a file that ends there from one that
	// goes on, whatever size the header gave.
	want := int64(PreviewMaxBytes + 1)
	buf := make([]byte, 0, min(size, want-1)+1)
	chunk := make([]byte, 32<<10)
	for int64(len(buf)) < want {
		if err := ctx.Err(); err != nil {
			return Preview{}, err
		}
		n, err := r.Read(chunk[:min(int64(len(chunk)), want-int64(len(buf)))])
		buf = append(buf, chunk[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return Preview{}, cerr
			}
			return Preview{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	if len(buf) > PreviewMaxBytes {
		buf, p.CutBytes = buf[:PreviewMaxBytes], true
	}
	if size > PreviewMaxBytes {
		p.CutBytes = true
	}
	p.Read = int64(len(buf))
	if expect := min(size, PreviewMaxBytes); p.Read < expect {
		return Preview{}, fmt.Errorf("the stream ended after %d of %d bytes", p.Read, size)
	}

	if p.CutBytes {
		buf = trimPartialRune(buf) // the bound may fall inside a character
	}
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(buf) {
		p.Binary = true
		return p, nil
	}

	p.Text = []string{}
	for len(buf) > 0 {
		if len(p.Text) == PreviewMaxLines {
			p.CutLines = true
			break
		}
		line := buf
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			line, buf = buf[:i], buf[i+1:]
		} else {
			buf = nil
		}
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1] // CRLF is a line end, not a control to show
		}
		if len(line) > PreviewMaxLine {
			line = trimPartialRune(line[:PreviewMaxLine])
			p.CutLine = true
			p.LongLines++
		}
		p.Text = append(p.Text, inertLine(line))
	}
	p.Lines = len(p.Text)
	return p, nil
}

// trimPartialRune drops the bytes of a character that a cut left
// unfinished at the end of b. Bytes that are not the start of a valid
// character are kept: they are for the caller to judge.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// inertLine renders one line of valid UTF-8 as text that cannot act on a
// terminal. A backslash is kept as it is: this is a file's text, to be
// read, not a name to be told apart from another.
func inertLine(line []byte) string {
	var b strings.Builder
	b.Grow(len(line) + 8)
	col := 0
	for i := 0; i < len(line); {
		r, n := utf8.DecodeRune(line[i:])
		esc := ""
		switch {
		case r == '\t':
			pad := previewTab - col%previewTab
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
			i += n
			continue
		case r == utf8.RuneError && n <= 1:
			esc = fmt.Sprintf(`\x%02x`, line[i]) // not reached for valid UTF-8; never a raw byte
		case r == '\r':
			esc = `\r`
		case r < 0x20 || r == 0x7f:
			esc = fmt.Sprintf(`\x%02x`, r)
		case r >= 0x80 && r <= 0x9f, escape.Reorders(r):
			esc = escape.Rune(r)
		}
		if esc != "" {
			b.WriteString(esc)
			col += len(esc)
		} else {
			b.Write(line[i : i+n])
			col++
		}
		i += n
	}
	return b.String()
}
