package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// withColour makes the renderer behave as on a 24-bit terminal for one test.
// (Under `go test` there is no terminal, so the default is no colour at all —
// which is what every other test in this package runs with.)
func withColour(t *testing.T, p termenv.Profile) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

func TestThemeByName(t *testing.T) {
	for _, name := range ThemeNames() {
		th, err := ThemeByName(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if name != AutoTheme && th.Name != name {
			t.Errorf("%s resolved to %s", name, th.Name)
		}
	}
	// No setting at all, and "auto", choose by the terminal's background;
	// without a terminal that is the dark palette the desk always had.
	for _, name := range []string{"", "auto", " AUTO "} {
		if th, err := ThemeByName(name); err != nil || th.Name != "dark" {
			t.Errorf("%q: got %q, %v", name, th.Name, err)
		}
	}
	if th, err := ThemeByName(" Solarized-Light "); err != nil || th.Name != "solarized-light" {
		t.Errorf("names are case-insensitive: %q, %v", th.Name, err)
	}
	// A typo must not silently become the default.
	_, err := ThemeByName("draculla")
	if err == nil || !strings.Contains(err.Error(), `unknown theme "draculla"`) || !strings.Contains(err.Error(), "dracula") {
		t.Errorf("unknown name: %v", err)
	}
	seen := map[string]bool{}
	for _, th := range themes {
		if seen[th.Name] || th.About == "" || th.Name != strings.ToLower(th.Name) {
			t.Errorf("theme %q: names are unique and lower-case, and each has a description", th.Name)
		}
		seen[th.Name] = true
	}
	if !strings.Contains(ThemeHelp(), "auto") || strings.Count(ThemeHelp(), "\n") != len(themes)+1 {
		t.Errorf("the help lists every theme and auto:\n%s", ThemeHelp())
	}
}

// TestDefaultThemeIsTheOriginalPalette pins the colours the desk was first
// drawn with: starting it with no theme setting must look as it always did.
func TestDefaultThemeIsTheOriginalPalette(t *testing.T) {
	th, _ := ThemeByName("")
	want := Theme{
		Name: "dark", About: th.About,
		Accent: lipgloss.Color("39"), Dim: lipgloss.Color("244"), Good: lipgloss.Color("42"),
		Warn: lipgloss.Color("214"), Bad: lipgloss.Color("203"), Border: lipgloss.Color("240"), Title: lipgloss.Color("250"),
	}
	if th != want {
		t.Fatalf("the default palette changed:\n got  %+v\n want %+v", th, want)
	}
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// colours returns the distinct colour-setting sequences in a frame: any SGR
// sequence with a foreground or background parameter, whether one of the 16
// ANSI colours, a 256-colour index or a 24-bit value.
func colours(frame string) map[string]bool {
	out := map[string]bool{}
	for _, seq := range sgr.FindAllString(frame, -1) {
		params := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
		for _, p := range strings.Split(params, ";") {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			if (n >= 30 && n <= 38) || (n >= 40 && n <= 48) || (n >= 90 && n <= 97) || (n >= 100 && n <= 107) {
				out[params] = true
				break
			}
		}
	}
	return out
}

// frames draws the desk as it stands — every kind in two layouts, and a
// refused deletion, where the destructive colour matters most — in the given
// theme. The model and its data are the same for every theme, so two calls
// differ only by what the theme changes.
func (b *bench) frames(th Theme) (text, raw []string) {
	b.t.Helper()
	b.m.st = newStyles(th)
	shoot := func() {
		b.assertFrameGeometry()
		b.m.dirty = true
		f := b.m.View()
		raw = append(raw, f)
		text = append(text, ansi.Strip(f))
	}
	for _, sz := range [][2]int{{140, 40}, {80, 24}} {
		b.m.width, b.m.height = sz[0], sz[1]
		for k := Kind(0); k < kindCount; k++ {
			b.m.setKind(k)
			b.m.afterChange()
			shoot()
		}
	}
	b.m.width, b.m.height = 140, 40
	b.m.setKind(KindContainers)
	b.m.afterChange()
	shoot()
	return text, raw
}

// quietBench is a desk on which nothing moves by itself: no statistics, no
// status message, a confirmation already open. What it draws depends only on
// the theme.
func quietBench(t *testing.T) *bench {
	t.Helper()
	b := newBench(t, benchOpts{stats: time.Hour})
	b.selectRow("shop-web-1")
	b.keys("d")
	b.see("REFUSED")
	b.m.status = statusLine{}
	// Scroll positions adapt to the sizes frames() goes through; one pass
	// brings them to where every later pass finds them.
	b.frames(themes[0])
	return b
}

// TestAThemeChangesColourAndNothingElse: in every theme the desk is the same
// text in the same cells — every pane, kind and layout — and only the colour
// differs.
func TestAThemeChangesColourAndNothingElse(t *testing.T) {
	withColour(t, termenv.TrueColor)
	b := quietBench(t)
	base, _ := b.frames(themes[0])
	palettes := map[string]string{}
	for _, th := range themes {
		text, raw := b.frames(th)
		all := map[string]bool{}
		for i := range text {
			if text[i] != base[i] {
				t.Fatalf("%s: frame %d differs from the dark theme in more than colour:\n%s", th.Name, i, firstDifference(base[i], text[i]))
			}
			for c := range colours(raw[i]) {
				all[c] = true
			}
		}
		if th.Name == "mono" {
			if len(all) != 0 {
				t.Errorf("mono must set no colour at all, found %v", sortedKeys(all))
			}
			continue
		}
		if len(all) < 6 {
			t.Errorf("%s: only %d distinct colours on screen; accent, dim, good, warn, bad, border and title should all show: %v", th.Name, len(all), sortedKeys(all))
		}
		sig := strings.Join(sortedKeys(all), "|")
		if other, dup := palettes[sig]; dup {
			t.Errorf("%s draws exactly the colours of %s", th.Name, other)
		}
		palettes[sig] = th.Name
	}
}

func firstDifference(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d\n  dark:  %q\n  other: %q", i, al[i], bl[i])
		}
	}
	return fmt.Sprintf("%d lines against %d", len(al), len(bl))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestHighlightIsVisibleInEveryTheme: the highlighted row of the focused
// pane is drawn with the theme's own selection colours, or in reverse video
// when the theme has none.
func TestHighlightIsVisibleInEveryTheme(t *testing.T) {
	withColour(t, termenv.TrueColor)
	b := newBench(t, benchOpts{stats: time.Hour})
	b.selectRow("shop-web-1")
	reverse := regexp.MustCompile(`\x1b\[([0-9]+;)*7(;[0-9]+)*m`)
	for _, th := range themes {
		b.m.st = newStyles(th)
		b.m.dirty = true
		row := ""
		for _, line := range strings.Split(b.m.View(), "\n") {
			if strings.Contains(ansi.Strip(line), "▸ shop-web-1") {
				row = line
			}
		}
		if row == "" {
			t.Fatalf("%s: the highlighted row is not on screen", th.Name)
		}
		cell := row[strings.Index(row, "▸ shop-web-1")-24:]
		switch {
		case th.SelBg != nil && !strings.Contains(cell[:24], "48;2;"):
			t.Errorf("%s: the highlighted row should carry the theme's selection background: %q", th.Name, cell[:40])
		case th.SelBg == nil && !reverse.MatchString(cell[:24]):
			t.Errorf("%s: the highlighted row should be in reverse video: %q", th.Name, cell[:40])
		}
	}
}

// TestThemesWithoutColour: with NO_COLOR (or a terminal without colour)
// every theme draws the same bytes, and the cues that never depended on
// colour are all still there.
func TestThemesWithoutColour(t *testing.T) {
	withColour(t, termenv.Ascii)
	b := quietBench(t)
	_, base := b.frames(themes[0])
	for _, th := range themes {
		_, raw := b.frames(th)
		for i := range raw {
			if len(colours(raw[i])) != 0 {
				t.Fatalf("%s: colour was emitted although the terminal shows none", th.Name)
			}
			if raw[i] != base[i] {
				t.Fatalf("%s: without colour every theme should draw the same frame; frame %d differs:\n%s", th.Name, i, firstDifference(base[i], raw[i]))
			}
		}
		for _, cue := range []string{"┏━ ◆ ", "┌─ 1 Kinds", "▸ ", "● running", "✗ REFUSED"} {
			if !strings.Contains(raw[len(raw)-1], cue) {
				t.Errorf("%s: %q is missing without colour", th.Name, cue)
			}
		}
	}
}

// TestThemeKeyCycles: T repaints the desk in the next theme, wraps around,
// says which theme it is and how to keep it — and is an ordinary character
// wherever text is being typed.
func TestThemeKeyCycles(t *testing.T) {
	withColour(t, termenv.TrueColor)
	b := newBench(t, benchOpts{})
	if b.m.st.theme != "dark" {
		t.Fatalf("starts in %q", b.m.st.theme)
	}
	b.srv.ResetRequests()
	before := b.m.View()
	var visited []string
	for range themes {
		b.keys("T")
		visited = append(visited, b.m.st.theme)
		b.see("Theme: "+b.m.st.theme, "--theme "+b.m.st.theme, "DOCKER_DESK_THEME="+b.m.st.theme)
		b.assertFrameGeometry()
	}
	want := []string{"light", "ansi", "dracula", "nord", "gruvbox", "solarized-dark", "solarized-light", "catppuccin", "mono", "dark"}
	if strings.Join(visited, ",") != strings.Join(want, ",") {
		t.Fatalf("cycle order:\n got  %v\n want %v", visited, want)
	}
	b.keys("T")
	b.m.dirty = true
	if after := b.m.View(); after == before || len(colours(after)) == 0 {
		t.Fatal("the frame after T should be drawn in the new theme's colours")
	}
	b.keys("?")
	b.see("next colour theme (light now)")
	b.keys("T") // under an overlay it does nothing
	if b.m.st.theme != "light" {
		t.Fatalf("T changed the theme under the help overlay: %s", b.m.st.theme)
	}
	b.keys("esc")

	// In the filter it is the letter T.
	b.keys("2", "/", "T")
	if b.m.st.theme != "light" || b.m.filter[KindContainers] != "T" {
		t.Fatalf("T typed into the filter: theme %s, filter %q", b.m.st.theme, b.m.filter[KindContainers])
	}
	b.keys("esc")

	// And it is not an action: nothing was sent that could change anything.
	if muts := b.srv.Mutations(); len(muts) != 0 {
		t.Fatalf("changing the theme sent %v", muts)
	}
	for _, as := range kindActions {
		for _, a := range as {
			if a.key == "T" {
				t.Fatalf("T is also bound to the action %q", a.label)
			}
		}
	}
}

func TestThemeKeyWithoutColourSaysSo(t *testing.T) {
	withColour(t, termenv.Ascii)
	b := newBench(t, benchOpts{})
	b.keys("T")
	b.see("Theme: light", "shows no colour")
}
