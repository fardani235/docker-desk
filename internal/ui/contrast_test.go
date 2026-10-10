package ui

import (
	"math"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Relative luminance and contrast ratio as WCAG 2 defines them.

func lum(c lipgloss.TerminalColor) float64 {
	r, g, b, _ := c.RGBA()
	f := func(v uint32) float64 {
		x := float64(v) / 65535
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}

func ratio(a, b lipgloss.TerminalColor) float64 {
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestThemesAreLegible: every colour that carries text has a contrast of at
// least 3:1 against the background its theme is meant for (WCAG's floor for
// bold and large text; most are far above it), unfocused borders are clearly
// visible, and the highlighted row is readable and stands out.
//
// "ansi" and "mono" have no colours of their own to check.
func TestThemesAreLegible(t *testing.T) {
	withColour(t, termenv.TrueColor) // colours have no RGB value without it
	paper := map[string][]string{
		"dark":            {"#000000", "#1e1e1e"},
		"light":           {"#ffffff", "#fafafa", "#f5f5f5"},
		"dracula":         {"#282a36"},
		"nord":            {"#2e3440"},
		"gruvbox":         {"#282828"},
		"solarized-dark":  {"#002b36"},
		"solarized-light": {"#fdf6e3"},
		"catppuccin":      {"#1e1e2e"},
	}
	for _, th := range themes {
		bgs, ok := paper[th.Name]
		if !ok {
			if th.Name != "ansi" && th.Name != "mono" {
				t.Errorf("theme %s has no background to be checked against", th.Name)
			}
			continue
		}
		for _, hex := range bgs {
			bg := lipgloss.Color(hex)
			for name, col := range map[string]lipgloss.TerminalColor{"accent": th.Accent, "dim": th.Dim, "good": th.Good, "warn": th.Warn, "bad": th.Bad, "title": th.Title} {
				if r := ratio(col, bg); r < 3.0 {
					t.Errorf("%s on %s: %s has a contrast of only %.1f:1", th.Name, hex, name, r)
				}
			}
			if r := ratio(th.Border, bg); r < 2.0 {
				t.Errorf("%s on %s: unfocused borders have a contrast of only %.1f:1", th.Name, hex, r)
			}
			if th.SelBg == nil {
				continue
			}
			if r := ratio(th.SelFg, th.SelBg); r < 4.5 {
				t.Errorf("%s: text on the highlighted row has a contrast of only %.1f:1", th.Name, r)
			}
			if r := ratio(th.SelBg, bg); r < 1.4 {
				t.Errorf("%s on %s: the highlighted row differs from the background by only %.2f:1", th.Name, hex, r)
			}
		}
	}
}
