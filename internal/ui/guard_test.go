package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestGuardLine(t *testing.T) {
	sgr := "\x1b[1;38;5;203m"
	reset := "\x1b[0m"
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "shop-web-1  ● running", "shop-web-1  ● running"},
		{"colours are kept", sgr + "red" + reset + " plain", sgr + "red" + reset + " plain"},
		{"a bare reset is kept", "a\x1b[mb", "a\x1b[mb"},
		{"an OSC title", "a\x1b]0;pwned\x07b", "ab"},
		{"an OSC ended by ST", "a\x1b]0;pwned\x1b\\b", "ab"},
		{"an OSC that never ends takes the rest", "a\x1b]0;pwned", "a"},
		{"a screen clear and a cursor move", "a\x1b[2J\x1b[Hb", "ab"},
		{"a cursor position", "a\x1b[10;20Hb", "ab"},
		{"an alternate-screen switch", "a\x1b[?1049lb", "ab"},
		{"a device query", "a\x1b[6nb", "ab"},
		{"a DCS", "a\x1bP1$r0m\x1b\\b", "ab"},
		{"a lone ESC", "a\x1bb", "a"},
		{"an SGR look-alike with a letter", "a\x1b[1;3Xmb", "amb"},
		{"newline, carriage return, tab, bell, backspace, DEL", "a\nb\rc\td\x07e\x08f\x7fg", "abcdefg"},
		{"NUL", "a\x00b", "ab"},
		{"a C1 CSI as a character", "a\u009b2Jb", "a2Jb"},
		{"every C1", "a\u0080\u008d\u009fb", "ab"},
		{"a byte that is no character", "a\xffb", "ab"},
		{"other characters are kept", "café 世界 ▸ ✓", "café 世界 ▸ ✓"},
		{"colour around an attack", sgr + "x\x1b]0;t\x07y" + reset, sgr + "xy" + reset},
	} {
		got := guardLine(tc.in)
		if got != tc.want {
			t.Errorf("%s: %q → %q, want %q", tc.name, tc.in, got, tc.want)
		}
		// What is removed is what the layout counted as no cells: the line
		// keeps the width it was laid out to.
		if tc.name != "an SGR look-alike with a letter" && tc.name != "a C1 CSI as a character" && tc.name != "a lone ESC" {
			if a, b := ansi.StringWidth(tc.in), ansi.StringWidth(got); a != b {
				t.Errorf("%s: width %d became %d", tc.name, a, b)
			}
		}
		// Nothing but SGR is left that begins with ESC, and no control byte.
		for i := 0; i < len(got); i++ {
			switch c := got[i]; {
			case c == 0x1b:
				n := sgrLen(got[i:])
				if n == 0 {
					t.Fatalf("%s: an escape that is not a colour is left in %q", tc.name, got)
				}
				i += n - 1
			case c < 0x20, c == 0x7f:
				t.Fatalf("%s: control byte %#x is left in %q", tc.name, c, got)
			}
		}
		if !lineIsClean(got) {
			t.Errorf("%s: the guarded line is not clean: %q", tc.name, got)
		}
		if guardLine(got) != got {
			t.Errorf("%s: guarding twice changes it", tc.name)
		}
	}
	if sgrLen("\x1b["+strings.Repeat("1;", 200)+"m") != 0 {
		t.Fatal("an endless parameter list is accepted as a colour")
	}
}

// Every frame the desk itself draws passes the guard with nothing removed:
// the guard removes only what should never have been there. The one thing it
// may add is the reset that ends a line (D7), and that is asserted here, not
// tolerated: a line is written as it was drawn, or as it was drawn and then
// a reset — and no line ends with an attribute still set.
func TestGuardLeavesTheDesksOwnFramesAlone(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newBench(t, benchOpts{width: size[0], height: size[1]})
		check := func(what string) {
			t.Helper()
			b.m.dirty = true
			b.m.View()
			for i, l := range b.m.render() {
				if !lineIsClean(l) {
					t.Fatalf("%s, %dx%d: line %d of the desk's own frame would be changed by the guard: %q", what, size[0], size[1], i, l)
				}
				g := guardLine(l)
				switch {
				case g == l:
					if sgrOpen(l) {
						t.Fatalf("%s, %dx%d: line %d ends with an attribute set and was given no reset: %q", what, size[0], size[1], i, l)
					}
				case g == l+sgrReset:
					if !sgrOpen(l) {
						t.Fatalf("%s, %dx%d: line %d was given a reset it did not need: %q", what, size[0], size[1], i, l)
					}
				default:
					t.Fatalf("%s, %dx%d: line %d was changed by more than the reset: %q → %q", what, size[0], size[1], i, l, g)
				}
				if ansi.StringWidth(g) != ansi.StringWidth(l) {
					t.Fatalf("%s, %dx%d: line %d changed width", what, size[0], size[1], i)
				}
			}
		}
		check("the desk")
		for _, k := range []string{"?", "esc", "S", "esc", "H", "esc", "M", "esc", "/", "esc", "enter", "esc", "3", "]", "]", "]", "2"} {
			b.keys(k)
			b.settle()
			check("after " + k)
		}
		for k := Kind(0); k < kindCount; k++ {
			b.kind(k)
			check(kindNames[k])
		}
	}
}

// A daemon's strings on the desk itself — the Containers list, the Details
// title and tab, the status line — cannot act on the terminal either.
func TestDeskDrawsHostileContainerTextAsText(t *testing.T) {
	const osc = "\x1b]0;pwned\x07"
	const clear = "\x1b[2J\x1b[H"
	name := "evil" + osc + clear + "\nforged-row"
	b := newBench(t, benchOpts{width: 160, height: 44, seed: func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: name, Image: "img" + osc + ":1", Health: "un" + clear + "healthy",
			Labels: map[string]string{"com.docker.compose.project": "proj" + clear}, Cmd: []string{"sh", "-c", "echo " + osc}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "plain", Image: "app:1"})
	}})
	frame := func(what string) string {
		t.Helper()
		b.m.dirty = true
		raw := b.m.View()
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[H", "\x07"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("%s: the frame carries %q", what, bad)
			}
		}
		b.assertFrameGeometry()
		return ansi.Strip(raw)
	}
	s := frame("the Containers list")
	if !strings.Contains(s, `evil\x1b]0;pwned\x07`) {
		t.Fatalf("the name is not drawn escaped in the list:\n%s", s)
	}
	b.selectRow(`evil\x1b]0;pwned\x07\x1b[2J\x1b[H\nforged-row`)
	b.settle()
	for tab := 0; tab < len(kindTabs[KindContainers]); tab++ {
		frame("Details tab " + kindTabs[KindContainers][tab])
		b.keys("]")
		b.settle()
	}
	// An action's confirmation names it too.
	b.keys("2", "t")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	frame("the stop confirmation")
	b.keys("esc")
	b.kind(KindCompose)
	frame("Compose")
}

// A colour or conceal sequence that reaches a line cannot carry into the
// next one: every guarded line that holds a colour sequence ends with a
// reset (D7). SGR 8 is conceal — what survives the guard can hide.
func TestGuardEndsEveryColouredLineWithAReset(t *testing.T) {
	const reset = "\x1b[0m"
	clean := "the next line, as the desk laid it out"
	got := guardFrame([]string{"label note=\x1b[8mhidden", clean, "\x1b[1mbold\x1b[0m", "\x1b[31mred\x1b[m", "\x1b[31mred\x1b[0m tail"})
	if !strings.HasSuffix(got[0], reset) {
		t.Fatalf("a line carrying a conceal sequence does not end with a reset: %q", got[0])
	}
	if got[1] != clean {
		t.Fatalf("a line without a colour sequence was changed: %q", got[1])
	}
	// A line that already ends with a reset is not given a second one.
	if got[2] != "\x1b[1mbold\x1b[0m" || got[3] != "\x1b[31mred\x1b[m" {
		t.Fatalf("a line already ending with a reset was changed: %q, %q", got[2], got[3])
	}
	if got[4] != "\x1b[31mred\x1b[0m tail" {
		t.Fatalf("plain text after a reset was given another: %q", got[4])
	}
	for i, l := range got {
		// The reset takes no cell, and guarding again changes nothing.
		if guardFrame([]string{l})[0] != l {
			t.Errorf("line %d: guarding twice changes it", i)
		}
	}
	if a, b := ansi.StringWidth("label note=\x1b[8mhidden"), ansi.StringWidth(got[0]); a != b {
		t.Fatalf("the reset changed the line's width: %d → %d", a, b)
	}
	// What the terminal is left in after each line: no attribute set.
	for i, l := range got {
		if open := sgrOpen(l); open {
			t.Errorf("line %d leaves an attribute set: %q", i, l)
		}
	}
}

// sgrOpen reports whether a line ends with an attribute still set: its last
// colour sequence is not a reset.
func sgrOpen(l string) bool {
	open := false
	for i := 0; i < len(l); i++ {
		if n := sgrLen(l[i:]); n > 0 {
			open = !isSGRReset(l[i : i+n])
			i += n - 1
		}
	}
	return open
}
