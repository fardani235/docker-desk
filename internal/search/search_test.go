package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestFindFoldsCaseAndDoesNotOverlap(t *testing.T) {
	cases := []struct {
		s, q string
		want []Range
	}{
		{"Health: healthy", "health", []Range{{0, 6}, {8, 14}}},
		{"aaaa", "aa", []Range{{0, 2}, {2, 4}}},
		{"no match here", "zzz", nil},
		{"anything", "", nil},
		{"", "x", nil},
		{"tail", "tails", nil},
		// Non-ASCII: offsets are bytes of s, even where cases differ in size.
		{"Größe GRÖSSE größe", "größe", []Range{{0, 7}, {16, 23}}}, // ß does not fold to SS
		{"ÉTAT état", "état", []Range{{0, 5}, {6, 11}}},
		// The Kelvin sign folds to k.
		{"Kube", "kube", []Range{{0, 6}}},
	}
	for _, c := range cases {
		if got := Find(c.s, c.q); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Find(%q, %q) = %v, want %v", c.s, c.q, got, c.want)
		}
	}
}

func TestNoRegularExpressions(t *testing.T) {
	if got := Find("a.b axb", "a.b"); !reflect.DeepEqual(got, []Range{{0, 3}}) {
		t.Errorf("a dot is a dot: %v", got)
	}
	if Count("abc", "[a-c]") != 0 || Count("x", ".*") != 0 {
		t.Error("a query is never a pattern")
	}
}

// Matching always looks at the text a reader sees: a query that spans a
// colour change matches, and an escape sequence's own bytes never do.
func TestMatchingStripsEscapes(t *testing.T) {
	line := "\x1b[31mERR\x1b[0mOR at \x1b[1;32mboot\x1b[0m"
	if !Contains(line, "error") {
		t.Error("a word split by a colour change must match")
	}
	if !Contains(line, "at boot") {
		t.Error("text after a sequence must match")
	}
	for _, q := range []string{"31m", "[0m", "1;32", "\x1b"} {
		if Contains(line, q) {
			t.Errorf("%q is part of an escape sequence and must not match", q)
		}
	}
	if Strip(line) != "ERROR at boot" {
		t.Errorf("Strip = %q", Strip(line))
	}
}

var angle = Marks{On: "<", Off: ">", CurOn: "<", CurOff: ">"}

func TestMarkPlainText(t *testing.T) {
	got := Mark("one two one", []Range{{0, 3}, {8, 11}}, Marks{"<", ">", "{", "}"}, 1)
	if want := "<one> two {one}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Mark("abc", nil, angle, 0); got != "abc" {
		t.Errorf("no ranges must change nothing: %q", got)
	}
	if got := Mark("abc", []Range{{0, 3}}, Marks{}, 0); got != "abc" {
		t.Errorf("empty markers must change nothing: %q", got)
	}
}

// The offsets are of the stripped text; escapes in the line are kept where
// they were, a colour change inside a mark is followed by the mark again, and
// the line's own colour is restored after it.
func TestMarkHighlightOffsetsInStyledText(t *testing.T) {
	red, reset := "\x1b[31m", "\x1b[0m"
	line := "ok " + red + "ERROR" + reset + " done"
	rs := Find(Strip(line), "error")
	if !reflect.DeepEqual(rs, []Range{{3, 8}}) {
		t.Fatalf("ranges %v", rs)
	}
	got := Mark(line, rs, angle, -1)
	// The mark opens before the colour sequence is copied (the sequence
	// sits at the boundary), is re-asserted after it, closes, and the red
	// that was in effect is written again before the reset that follows.
	want := "ok <" + red + "<ERROR>" + red + reset + " done"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if Strip(got) != "ok <<ERROR> done" && Strip(got) != "ok <<ERROR>"+" done" {
		t.Errorf("visible text %q", Strip(got))
	}

	// A mark that starts in the middle of a coloured run.
	line = red + "abcdef" + reset
	got = Mark(line, []Range{{2, 4}}, angle, -1)
	if want := red + "ab<cd>" + red + "ef" + reset; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// A mark at the very end of the line closes and restores.
	got = Mark(red+"abc", []Range{{1, 3}}, angle, -1)
	if want := red + "a<bc>" + red; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestMarkWideAndMultibyte(t *testing.T) {
	line := "名前: 日本語テキスト"
	rs := Find(Strip(line), "日本語")
	got := Mark(line, rs, Marks{"»", "«", "»", "«"}, -1)
	if got != "名前: »日本語«テキスト" {
		t.Errorf("got %q", got)
	}
}

func TestClip(t *testing.T) {
	rs := []Range{{2, 5}, {8, 12}, {20, 22}}
	if got := Clip(rs, 4, 10); !reflect.DeepEqual(got, []Range{{0, 1}, {4, 6}}) {
		t.Errorf("Clip = %v", got)
	}
	if got := Clip(rs, 12, 20); got != nil {
		t.Errorf("touching ends must not be included: %v", got)
	}
}

func BenchmarkFind5000Lines(b *testing.B) {
	lines := make([]string, 5000)
	for i := range lines {
		lines[i] = strings.Repeat("2026-11-05T10:00:00Z GET /api/v1/items 200 12ms ", 4)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, l := range lines {
			n += Count(l, "error")
		}
	}
}
