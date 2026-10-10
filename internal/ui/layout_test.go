package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func TestFrameGeometryAtEverySize(t *testing.T) {
	b := newBench(t, benchOpts{})
	sizes := [][2]int{{140, 40}, {200, 60}, {120, 30}, {119, 30}, {100, 30}, {80, 24}, {81, 25}, {160, 24}, {80, 50}}
	for _, sz := range sizes {
		b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		for k := Kind(0); k < kindCount; k++ {
			b.kind(k)
			for range kindTabs[k] {
				b.assertFrameGeometry()
				b.keys("]")
				b.settle()
			}
			for p := Pane(0); p < paneCount; p++ {
				b.m.focus = p
				b.assertFrameGeometry()
			}
		}
	}
}

// TestGlyphsHaveUnambiguousWidth guards against characters that terminals
// (and width libraries) disagree about — emoji-style symbols above all. One
// such character makes a row a cell wider or narrower than its neighbours
// and tears every border to its right. Every non-ASCII character that
// appears anywhere in this package's source is checked.
func TestGlyphsHaveUnambiguousWidth(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[rune]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range string(data) {
			if r > 127 {
				seen[r] = f
			}
		}
	}
	if len(seen) < 20 {
		t.Fatalf("only %d non-ASCII glyphs found; is the scan looking at the right files?", len(seen))
	}
	for r, f := range seen {
		if w := runewidth.RuneWidth(r); w != 1 {
			t.Errorf("%s uses %q (U+%04X), which is %d cells wide according to go-runewidth", f, r, r, w)
		}
		// Characters with an emoji presentation are drawn two cells wide by
		// some terminals whatever the tables say.
		if r >= 0x1F000 || (r >= 0x2600 && r <= 0x26FF) || (r >= 0x23E9 && r <= 0x23FF) || r == 0x25B6 || r == 0x25C0 {
			t.Errorf("%s uses %q (U+%04X), which some terminals draw as a double-width emoji", f, r, r)
		}
	}
}

func TestLayoutShowsAllPanes(t *testing.T) {
	b := newBench(t, benchOpts{})
	for _, sz := range [][2]int{{140, 40}, {100, 30}, {80, 24}} {
		b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		s := b.screen()
		for _, want := range []string{"1 Kinds", "2 Containers", "3 Details", "4 Events", "5 Jobs", b.srv.Host(), "connected"} {
			if !strings.Contains(s, want) {
				t.Errorf("%dx%d: %q is not on screen\n%s", sz[0], sz[1], want, s)
			}
		}
		b.dump("layout")
	}
}
