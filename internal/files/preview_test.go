package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/escape"
)

func preview(t *testing.T, data []byte) Preview {
	t.Helper()
	p, err := ReadPreview(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadPreview: %v", err)
	}
	return p
}

// inert fails if s holds anything that could act on a terminal: a control
// character, a C1 control, a reordering character or a byte of invalid UTF-8.
func inert(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("invalid UTF-8 reached the text: %q", s)
	}
	for _, r := range s {
		if unicode.IsControl(r) || escape.Reorders(r) {
			t.Fatalf("a raw %U reached the text: %q", r, s)
		}
	}
}

func TestPreviewText(t *testing.T) {
	var src strings.Builder
	src.WriteString("#!/bin/sh\n")
	for i := 0; i < 38; i++ {
		src.WriteString("echo line\n")
	}
	src.WriteString("exit 0\n")
	p := preview(t, []byte(src.String()))
	if p.Binary || p.Lines != 40 || len(p.Text) != 40 || !p.Whole() {
		t.Fatalf("a 40-line script: %+v", p)
	}
	if p.Text[0] != "#!/bin/sh" || p.Text[39] != "exit 0" {
		t.Fatalf("lines: %q … %q", p.Text[0], p.Text[39])
	}
	if p.Size != int64(src.Len()) || p.Read != p.Size {
		t.Fatalf("size %d read %d of %d", p.Size, p.Read, src.Len())
	}

	// No final newline: the last line is still a line, and no empty one
	// is invented after a final newline.
	if p := preview(t, []byte("a\nb")); p.Lines != 2 || p.Text[1] != "b" {
		t.Fatalf("no final newline: %+v", p)
	}
	if p := preview(t, []byte("a\n\nb\n")); p.Lines != 3 || p.Text[1] != "" {
		t.Fatalf("an empty line: %+v", p)
	}
}

func TestPreviewEmptyFile(t *testing.T) {
	p := preview(t, nil)
	if p.Binary || p.Text == nil || len(p.Text) != 0 || p.Lines != 0 || p.Read != 0 || !p.Whole() {
		t.Fatalf("an empty file: %+v", p)
	}
}

// countingReader records how much was asked of it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

func TestPreviewLargeFileIsCutAtTheByteBound(t *testing.T) {
	line := strings.Repeat("x", 1023) + "\n" // 1 KiB a line
	data := []byte(strings.Repeat(line, 3<<10))
	if len(data) != 3<<20 {
		t.Fatal("fixture")
	}
	cr := &countingReader{r: bytes.NewReader(data)}
	p, err := ReadPreview(context.Background(), cr, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !p.CutBytes || p.Read != PreviewMaxBytes || p.Size != 3<<20 || p.Binary {
		t.Fatalf("3 MiB: %+v", Preview{Size: p.Size, Read: p.Read, CutBytes: p.CutBytes, Binary: p.Binary})
	}
	if p.Lines != 1024 || p.CutLines || p.CutLine {
		t.Fatalf("lines %d cutLines %v cutLine %v", p.Lines, p.CutLines, p.CutLine)
	}
	// The read stops at the bound: one byte past it, to see that there is more.
	if cr.n > PreviewMaxBytes+1 {
		t.Fatalf("%d bytes were read of a 3 MiB file", cr.n)
	}
}

func TestPreviewExactSizeRead(t *testing.T) {
	// A file of exactly the bound is whole.
	data := bytes.Repeat([]byte("abcdefg\n"), PreviewMaxBytes/8)
	p := preview(t, data)
	if p.CutBytes || p.Read != PreviewMaxBytes {
		t.Fatalf("exactly 1 MiB: read %d cut %v", p.Read, p.CutBytes)
	}
	// One byte more is cut, and says so.
	p = preview(t, append(data, 'z'))
	if !p.CutBytes || p.Read != PreviewMaxBytes {
		t.Fatalf("1 MiB + 1: read %d cut %v", p.Read, p.CutBytes)
	}
	// A stream longer than the size it was given is cut too, not trusted.
	p, err := ReadPreview(context.Background(), bytes.NewReader(append(data, 'z')), 10)
	if err != nil || !p.CutBytes {
		t.Fatalf("a stream longer than its size: %v cut %v", err, p.CutBytes)
	}
	// A stream shorter than its size is an error, and keeps nothing.
	p, err = ReadPreview(context.Background(), strings.NewReader("abc"), 10)
	if err == nil || p.Text != nil || p.Read != 0 {
		t.Fatalf("a short stream: %v %+v", err, p)
	}
	if _, err := ReadPreview(context.Background(), strings.NewReader(""), -1); err == nil {
		t.Fatal("a negative size was accepted")
	}
	// The byte bound falling inside a character does not make text binary.
	multi := append(bytes.Repeat([]byte("a"), PreviewMaxBytes-1), []byte("é and more")...)
	p = preview(t, multi)
	if p.Binary || !p.CutBytes {
		t.Fatalf("a character split by the bound: binary %v cut %v", p.Binary, p.CutBytes)
	}
}

func TestPreviewManyLines(t *testing.T) {
	p := preview(t, []byte(strings.Repeat("row\n", 20_000)))
	if !p.CutLines || p.Lines != PreviewMaxLines || len(p.Text) != PreviewMaxLines || p.CutBytes || p.CutLine {
		t.Fatalf("20,000 lines: lines %d cutLines %v cutBytes %v cutLine %v", p.Lines, p.CutLines, p.CutBytes, p.CutLine)
	}
	// Exactly the bound is whole.
	p = preview(t, []byte(strings.Repeat("row\n", PreviewMaxLines)))
	if p.CutLines || p.Lines != PreviewMaxLines {
		t.Fatalf("10,000 lines: lines %d cut %v", p.Lines, p.CutLines)
	}
}

func TestPreviewLongLine(t *testing.T) {
	data := []byte("short\n" + strings.Repeat("y", 3<<10) + "\nafter\n")
	p := preview(t, data)
	if !p.CutLine || p.LongLines != 1 || p.Lines != 3 || p.CutBytes || p.CutLines {
		t.Fatalf("a 3 KiB line: %+v", Preview{Lines: p.Lines, CutLine: p.CutLine, LongLines: p.LongLines})
	}
	if len(p.Text[1]) != PreviewMaxLine || p.Text[0] != "short" || p.Text[2] != "after" {
		t.Fatalf("line lengths: %d, %q, %q", len(p.Text[1]), p.Text[0], p.Text[2])
	}
	// Exactly 2 KiB is whole; a cut inside a character drops the character.
	if p := preview(t, []byte(strings.Repeat("y", PreviewMaxLine))); p.CutLine {
		t.Fatal("a 2 KiB line was cut")
	}
	p = preview(t, []byte(strings.Repeat("y", PreviewMaxLine-1)+"éé"))
	if !p.CutLine || !utf8.ValidString(p.Text[0]) || len(p.Text[0]) != PreviewMaxLine-1 {
		t.Fatalf("a cut inside a character: cut %v len %d", p.CutLine, len(p.Text[0]))
	}
}

func TestPreviewBinary(t *testing.T) {
	for name, data := range map[string][]byte{
		"NUL":                         []byte("SQLite format 3\x00rest"),
		"invalid UTF-8":               []byte("caf\xe9\n"),
		"a lone 0xff":                 {0xff},
		"an ELF header":               {0x7f, 'E', 'L', 'F', 2, 1, 1, 0},
		"NUL at the end of the bound": append(bytes.Repeat([]byte("a"), PreviewMaxBytes-1), 0, 'b'),
	} {
		p := preview(t, data)
		if !p.Binary || p.Text != nil || p.Lines != 0 {
			t.Errorf("%s: %+v", name, p)
		}
		if p.Read != min(int64(len(data)), PreviewMaxBytes) || p.Size != int64(len(data)) {
			t.Errorf("%s: read %d size %d", name, p.Read, p.Size)
		}
	}
	// A NUL past the bound is not read, and so not judged.
	if p := preview(t, append(bytes.Repeat([]byte("a"), PreviewMaxBytes), 0)); p.Binary || !p.CutBytes {
		t.Fatalf("a NUL past the bound: binary %v cut %v", p.Binary, p.CutBytes)
	}
}

func TestPreviewEscapesControls(t *testing.T) {
	src := "\x1b[31mred\x1b[0m\x1b]0;title\x07\r\n" + // CSI, OSC, BEL, CRLF
		"a\rb\x7f\x08\x0c\x0b\n" + // lone CR, DEL, BS, FF, VT
		"\u009b31m \u0085 \u202eevil\u2066 \u2028 \ufeff\u200f\n" + // C1 CSI, NEL, bidi, separators, BOM
		"\ta\tbc\tx\n" +
		`back\slash \x1b stays` + "\n"
	p := preview(t, []byte(src))
	if p.Binary || p.Lines != 5 {
		t.Fatalf("%+v", p)
	}
	for _, l := range p.Text {
		inert(t, l)
	}
	want := []string{
		`\x1b[31mred\x1b[0m\x1b]0;title\x07`,
		`a\rb\x7f\x08\x0c\x0b`,
		`\u009b31m \u0085 \u202eevil\u2066 \u2028 \ufeff\u200f`,
		"        a       bc      x",
		`back\slash \x1b stays`,
	}
	for i, w := range want {
		if p.Text[i] != w {
			t.Errorf("line %d:\n got %q\nwant %q", i+1, p.Text[i], w)
		}
	}
}

// Every byte value, alone and in a line, comes out inert or as binary.
func TestPreviewEveryByteIsInert(t *testing.T) {
	for c := 0; c < 256; c++ {
		p := preview(t, []byte{'a', byte(c), 'b', '\n'})
		if p.Binary {
			if c != 0 && c < 0x80 {
				t.Errorf("byte %#02x made a line binary", c)
			}
			continue
		}
		for _, l := range p.Text {
			inert(t, l)
		}
	}
	// And every character there is, through the line escaper itself.
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue // surrogates are not characters
		}
		inert(t, inertLine([]byte("a"+string(r)+"b")))
	}
}

// slowReader gives one byte a call, and cancels the context after n.
type slowReader struct {
	n      int
	cancel context.CancelFunc
}

func (s *slowReader) Read(b []byte) (int, error) {
	if s.n--; s.n == 0 {
		s.cancel()
	}
	b[0] = 'a'
	return 1, nil
}

func TestPreviewCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := ReadPreview(ctx, &slowReader{n: 100, cancel: cancel}, 1<<20)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if p.Text != nil || p.Read != 0 || p.Size != 0 {
		t.Fatalf("a cancelled read kept something: %+v", p)
	}
	// Cancelled before it starts: nothing is read at all.
	cr := &countingReader{r: strings.NewReader("secret")}
	if _, err := ReadPreview(ctx, cr, 6); !errors.Is(err, context.Canceled) || cr.n != 0 {
		t.Fatalf("err = %v after %d bytes", err, cr.n)
	}
	// A reader's own failure is reported, and keeps nothing.
	boom := errors.New("connection reset")
	p, err = ReadPreview(context.Background(), io.MultiReader(strings.NewReader("abc"), errReader{boom}), 100)
	if !errors.Is(err, boom) || p.Text != nil {
		t.Fatalf("err = %v, %+v", err, p)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
