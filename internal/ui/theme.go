package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme is the desk's palette: one colour for each thing a colour is used to
// say. A theme changes nothing else — not the layout, not the glyphs, and
// none of the cues that work without colour (border weight, the ◆ focus
// marker, the ▸ cursor, states spelled out). See docs/DECISIONS.md, T1–T4.
type Theme struct {
	// Name is what --theme and DOCKER_DESK_THEME take.
	Name string
	// About is one line for --help.
	About string

	// Accent marks the focused pane, the endpoint and things in progress.
	Accent lipgloss.TerminalColor
	// Dim is for labels, hints and anything secondary.
	Dim lipgloss.TerminalColor
	// Good, Warn and Bad carry state: running / paused or caution / failed,
	// refused or destructive.
	Good lipgloss.TerminalColor
	Warn lipgloss.TerminalColor
	Bad  lipgloss.TerminalColor
	// Border and Title draw the panes that do not have focus.
	Border lipgloss.TerminalColor
	Title  lipgloss.TerminalColor
	// SelFg and SelBg colour the highlighted row of the focused pane. When
	// SelBg is nil the row is drawn in reverse video instead, which follows
	// whatever colours the terminal itself uses.
	SelFg lipgloss.TerminalColor
	SelBg lipgloss.TerminalColor
}

// AutoTheme is the name that picks "dark" or "light" from the terminal's
// background colour.
const AutoTheme = "auto"

func c(s string) lipgloss.Color { return lipgloss.Color(s) }

// themes are the built-in palettes, in the order T cycles through them.
//
// "dark" is the palette the desk was first drawn with, unchanged, so that a
// desk started without any theme setting looks exactly as it did. The named
// palettes are given as 24-bit colours; on a terminal with fewer colours
// each is mapped to the nearest one it has.
//
// The desk never paints the background: a named palette is meant for a
// terminal that already uses that scheme, and its colours are checked for
// contrast against that scheme's background (TestThemesAreLegible). Where a
// scheme's own colour was too faint for text that is read all the time —
// hints, labels, unfocused borders — a lighter one from the same family is
// used instead.
var themes = []Theme{
	{
		Name: "dark", About: "the original palette, for dark backgrounds",
		Accent: c("39"), Dim: c("244"), Good: c("42"), Warn: c("214"), Bad: c("203"),
		Border: c("240"), Title: c("250"),
	},
	{
		Name: "light", About: "darker hues that stay readable on a light background",
		Accent: c("26"), Dim: c("243"), Good: c("28"), Warn: c("130"), Bad: c("160"),
		Border: c("247"), Title: c("238"),
	},
	{
		Name: "ansi", About: "only your terminal's own 16 colours — follows its colour scheme",
		Accent: c("4"), Dim: c("8"), Good: c("2"), Warn: c("3"), Bad: c("1"),
		Border: c("8"), Title: c("7"),
	},
	{
		Name: "dracula", About: "Dracula",
		Accent: c("#bd93f9"), Dim: c("#7f8cba"), Good: c("#50fa7b"), Warn: c("#ffb86c"), Bad: c("#ff5555"),
		Border: c("#6272a4"), Title: c("#f8f8f2"), SelFg: c("#f8f8f2"), SelBg: c("#44475a"),
	},
	{
		Name: "nord", About: "Nord",
		Accent: c("#88c0d0"), Dim: c("#7b88a1"), Good: c("#a3be8c"), Warn: c("#ebcb8b"), Bad: c("#bf616a"),
		Border: c("#616e88"), Title: c("#d8dee9"), SelFg: c("#eceff4"), SelBg: c("#4c566a"),
	},
	{
		Name: "gruvbox", About: "Gruvbox (dark)",
		Accent: c("#83a598"), Dim: c("#928374"), Good: c("#b8bb26"), Warn: c("#fabd2f"), Bad: c("#fb4934"),
		Border: c("#7c6f64"), Title: c("#d5c4a1"), SelFg: c("#fbf1c7"), SelBg: c("#504945"),
	},
	{
		Name: "solarized-dark", About: "Solarized, dark",
		Accent: c("#268bd2"), Dim: c("#839496"), Good: c("#859900"), Warn: c("#b58900"), Bad: c("#dc322f"),
		Border: c("#586e75"), Title: c("#93a1a1"), SelFg: c("#fdf6e3"), SelBg: c("#586e75"),
	},
	{
		Name: "solarized-light", About: "Solarized, light",
		Accent: c("#268bd2"), Dim: c("#657b83"), Good: c("#718200"), Warn: c("#9a7500"), Bad: c("#dc322f"),
		Border: c("#93a1a1"), Title: c("#586e75"), SelFg: c("#002b36"), SelBg: c("#93a1a1"),
	},
	{
		Name: "catppuccin", About: "Catppuccin (Mocha)",
		Accent: c("#89b4fa"), Dim: c("#7f849c"), Good: c("#a6e3a1"), Warn: c("#f9e2af"), Bad: c("#f38ba8"),
		Border: c("#6c7086"), Title: c("#cdd6f4"), SelFg: c("#cdd6f4"), SelBg: c("#45475a"),
	},
	{
		Name: "mono", About: "no colour at all: bold, reverse video and glyphs only",
	},
}

// ThemeNames lists what --theme accepts, AutoTheme first.
func ThemeNames() []string {
	names := []string{AutoTheme}
	for _, t := range themes {
		names = append(names, t.Name)
	}
	return names
}

// ThemeHelp is one line per theme, for --help.
func ThemeHelp() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %-16s %s\n", AutoTheme, "dark or light, from your terminal's background (the default)")
	for _, t := range themes {
		fmt.Fprintf(&sb, "  %-16s %s\n", t.Name, t.About)
	}
	return sb.String()
}

// ThemeByName returns the named built-in theme. The empty name and "auto"
// pick by the terminal's background. An unknown name is an error that lists
// the known ones: a typo must not silently become the default.
func ThemeByName(name string) (Theme, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == AutoTheme {
		if lipgloss.HasDarkBackground() {
			return themes[0], nil
		}
		return themes[1], nil
	}
	for _, t := range themes {
		if t.Name == name {
			return t, nil
		}
	}
	return Theme{}, fmt.Errorf("unknown theme %q (known: %s)", name, strings.Join(ThemeNames(), ", "))
}

// nextTheme returns the theme after the named one, wrapping around.
func nextTheme(name string) Theme {
	for i, t := range themes {
		if t.Name == name {
			return themes[(i+1)%len(themes)]
		}
	}
	return themes[0]
}
