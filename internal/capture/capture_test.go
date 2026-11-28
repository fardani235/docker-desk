package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestTextIsPlainAndTrimmed: lines that carry colour and the padding fit
// adds become plain, trimmed lines; the header is # lines; the file ends in
// one newline.
func TestTextIsPlainAndTrimmed(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"sgr", "\x1b[1;31mred\x1b[0m text", "red text"},
		{"fit padding", "padded" + strings.Repeat(" ", 40), "padded"},
		{"sgr and padding", "\x1b[2m  dim  \x1b[0m     ", "  dim"},
		{"colon sgr", "\x1b[38:2:1:2:3mtrue\x1b[m", "true"},
		{"osc title, bel", "a\x1b]0;owned\x07b", "ab"},
		{"osc 52 clipboard, st", "a\x1b]52;c;ZXZpbA==\x1b\\b", "ab"},
		{"osc unterminated", "a\x1b]8;;http://x", "a"},
		{"dcs", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"cursor moves", "\x1b[2J\x1b[Hx\x1b[10;10Hy", "xy"},
		{"short escapes", "\x1bcreset\x1b7saved\x1b(Bcharset", "resetsavedcharset"},
		{"lone esc", "end\x1b", "end"},
		{"8-bit csi", "a\u009b31mb", "a31mb"},
		{"c0 but tab", "a\x07b\x08c\td\re\x7f", "abc\tde"},
		{"invalid utf-8", "a\xffb", "a\ufffdb"},
		{"visible escapes are text", `a\x1b[31m\n`, `a\x1b[31m\n`},
		{"wide and combining", "日本 e\u0301  ", "日本 e\u0301"},
		{"bidi override", "a\u202eevil\u2066b", `a\u202eevil\u2066b`},
		{"line separator and tag", "x\u2028y\U000e0041z", `x\u2028y\U000e0041z`},
		{"osc ended by c1 st", "a\x1b]0;t\u009c rest", "a rest"},
	}
	var in, want []string
	for _, c := range cases {
		if got := Plain(c.in); got != c.want {
			t.Errorf("%s: Plain(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		in, want = append(in, c.in), append(want, c.want)
	}

	out := Text(Header{Tool: "docker-desk test", Pane: "Logs", Object: "shop-web-1",
		Facts: []string{"filter: err — 2 of 9 held lines match"}}, append(in, "", "   "))
	s := string(out)
	if strings.ContainsAny(s, "\x1b\u009b\u009c\x07\u202e\u2066\u2028") {
		t.Errorf("an escape or control character is in the file: %q", s)
	}
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("padding left on %q", l)
		}
	}
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		t.Errorf("does not end in exactly one newline: %q", s[max(len(s)-20, 0):])
	}
	head, body, ok := strings.Cut(s, "#\n")
	if !ok {
		t.Fatalf("no header end:\n%s", s)
	}
	for _, l := range strings.Split(strings.TrimSuffix(head, "\n"), "\n") {
		if !strings.HasPrefix(l, "# ") {
			t.Errorf("header line %q is not a comment", l)
		}
	}
	for _, w := range []string{"# docker-desk test — pane capture\n", "# pane: Logs\n", "# object: shop-web-1\n",
		"# filter: err — 2 of 9 held lines match\n", "not a report", "laid out for a page",
		"# this header ends at the first line that is only #"} {
		if !strings.Contains(head, w) {
			t.Errorf("header lacks %q:\n%s", w, head)
		}
	}
	// "c0 but tab" holds a \r: it is removed, never a line break.
	if got := strings.Join(want, "\n") + "\n"; body != got {
		t.Errorf("body:\n%q\nwant\n%q", body, got)
	}
}

// TestHeaderValuesStayOnTheirLine: no fact can end the header or forge a
// body line.
func TestHeaderValuesStayOnTheirLine(t *testing.T) {
	out := string(Text(Header{Pane: "Logs", Object: "x\n#\nforged\r\nline\x1b[2J"}, []string{"body"}))
	want := "# docker-desk — pane capture\n# pane: Logs\n# object: x # forged line\n"
	if !strings.HasPrefix(out, want) {
		t.Errorf("header:\n%q\nwant prefix\n%q", out, want)
	}
	if strings.Count(out, "\n#\n") != 1 || !strings.HasSuffix(out, "#\nbody\n") {
		t.Errorf("the header's end was forged:\n%s", out)
	}
}

// TestTextSplitsEmbeddedNewlines: a held line with a newline in it is two
// lines of the file, never one line with a control character.
func TestTextSplitsEmbeddedNewlines(t *testing.T) {
	out := string(Text(Header{}, []string{"one\ntwo", "three"}))
	if !strings.HasSuffix(out, "#\none\ntwo\nthree\n") {
		t.Errorf("%q", out)
	}
	if empty := string(Text(Header{}, nil)); !strings.HasSuffix(empty, "#\n") {
		t.Errorf("an empty body: %q", empty)
	}
}

// TestJSONIsTheDocument: indented, no header, one newline, the same
// document, and a character that could act or reorder is JSON's own escape.
func TestJSONIsTheDocument(t *testing.T) {
	raw := []byte("  {\"Id\":\"abc\",\"Config\":{\"Env\":[\"TOKEN=s3cret\"],\"Cmd\":[\"a\u202eb\",\"c\u0085d\",\"e\u007ff\",\"g\U000e0041h\"]},\"Name\":\"/web\"}\n\n")
	out, err := JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("does not parse:\n%s", out)
	}
	if out[0] != '{' {
		t.Errorf("first byte %q, want {", out[0])
	}
	if !bytes.HasSuffix(out, []byte("}\n")) || bytes.HasSuffix(out, []byte("\n\n")) {
		t.Errorf("does not end in one newline: %q", out)
	}
	if !bytes.Contains(out, []byte("\n  \"Config\": {\n")) {
		t.Errorf("not indented:\n%s", out)
	}
	for _, r := range []rune{0x202e, 0x85, 0x7f, 0xe0041} {
		if bytes.ContainsRune(out, r) {
			t.Errorf("U+%04X is raw in the file", r)
		}
	}
	for _, w := range []string{`a\u202eb`, `c\u0085d`, `e\u007ff`, `g\udb40\udc41h`, `TOKEN=s3cret`} {
		if !bytes.Contains(out, []byte(w)) {
			t.Errorf("%s is not in the file:\n%s", w, out)
		}
	}
	var a, b any
	json.Unmarshal(raw, &a)
	json.Unmarshal(out, &b)
	if ja, _ := json.Marshal(a); string(ja) != func() string { jb, _ := json.Marshal(b); return string(jb) }() {
		t.Errorf("not the same document:\n%s\n%s", raw, out)
	}
}

func TestJSONRefusesWhatDoesNotParse(t *testing.T) {
	for _, in := range []string{"", "   ", "{", "{\"a\":1} trailing", "not json", "{\"a\":\"\x01\"}"} {
		out, err := JSON([]byte(in))
		if err == nil || out != nil {
			t.Errorf("JSON(%q) = %q, %v; want no bytes and an error", in, out, err)
		}
		if err != nil && !errors.Is(err, ErrNotJSON) {
			t.Errorf("JSON(%q): %v is not ErrNotJSON", in, err)
		}
	}
	// Invalid UTF-8 inside a string: the document a decoder would make.
	out, err := JSON([]byte("{\"a\":\"x\xffy\"}"))
	if err != nil || !bytes.Contains(out, []byte("x\ufffdy")) || !json.Valid(out) {
		t.Errorf("%q %v", out, err)
	}
}

// TestStandardLibraryOnly: the package imports nothing of the desk's, so it
// cannot reach the daemon, the UI or a file.
func TestStandardLibraryOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "github.com/fardani235/docker-desk/internal/capture" {
			continue
		}
		if strings.Contains(p, ".") && !strings.HasPrefix(p, "vendor/") {
			t.Errorf("imports %s: the standard library only", p)
		}
	}
}
