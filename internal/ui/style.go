package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// tone is the semantic colour of a piece of text.
type tone int

const (
	toneNormal tone = iota
	toneDim
	toneGood
	toneWarn
	toneBad
	toneAccent
)

// styles holds every style the desk uses. Colour is never the only carrier
// of meaning: focus also changes the border weight and adds a marker,
// selection adds a cursor glyph, states are spelled out. With NO_COLOR (or a
// terminal without colour) all of these render as plain text, whatever the
// theme.
type styles struct {
	color bool
	// theme is the name of the theme the styles were built from.
	theme string

	tones [6]lipgloss.Style

	borderFocus lipgloss.Style
	borderBlur  lipgloss.Style
	titleFocus  lipgloss.Style
	titleBlur   lipgloss.Style

	header      lipgloss.Style
	headerKey   lipgloss.Style
	headerAlert lipgloss.Style
	selected    lipgloss.Style
	selectedDim lipgloss.Style
	colHeader   lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	hintKey     lipgloss.Style
	hintText    lipgloss.Style
	dialogTitle lipgloss.Style
	danger      lipgloss.Style
}

// newStyles builds every style from a theme. Colours come only from the
// theme; weight, underline and reverse video are the same in all of them.
func newStyles(t Theme) styles {
	s := styles{color: lipgloss.ColorProfile() != termenv.Ascii, theme: t.Name}
	// A colour the theme leaves out is simply not applied.
	fg := func(c lipgloss.TerminalColor) lipgloss.Style {
		if c == nil {
			return lipgloss.NewStyle()
		}
		return lipgloss.NewStyle().Foreground(c)
	}
	s.tones = [6]lipgloss.Style{
		toneNormal: lipgloss.NewStyle(),
		toneDim:    fg(t.Dim),
		toneGood:   fg(t.Good),
		toneWarn:   fg(t.Warn),
		toneBad:    fg(t.Bad),
		toneAccent: fg(t.Accent),
	}
	s.borderFocus = fg(t.Accent).Bold(true)
	s.borderBlur = fg(t.Border)
	s.titleFocus = fg(t.Accent).Bold(true)
	s.titleBlur = fg(t.Title)
	s.header = lipgloss.NewStyle().Bold(true)
	s.headerKey = fg(t.Dim)
	s.headerAlert = fg(t.Bad).Bold(true)
	// The highlighted row: the theme's own selection colours when it has
	// them, reverse video otherwise. (A terminal without colour is sent
	// neither; there the ▸ cursor marks the row, as it always has.)
	s.selected = lipgloss.NewStyle().Reverse(true).Bold(true)
	if t.SelBg != nil {
		s.selected = fg(t.SelFg).Background(t.SelBg).Bold(true)
	}
	s.selectedDim = lipgloss.NewStyle().Bold(true)
	s.colHeader = fg(t.Dim).Bold(true)
	s.tabActive = lipgloss.NewStyle().Bold(true).Underline(true)
	s.tabInactive = fg(t.Dim)
	s.hintKey = lipgloss.NewStyle().Bold(true)
	s.hintText = fg(t.Dim)
	s.dialogTitle = lipgloss.NewStyle().Bold(true)
	s.danger = fg(t.Bad).Bold(true)
	return s
}

func (s styles) tone(t tone, text string) string {
	if t == toneNormal {
		return text
	}
	return s.tones[t].Render(text)
}

// box draws a titled frame of exactly w×h cells around lines.
//
// A focused box uses heavy lines and a ◆ marker in its title, an unfocused
// one light lines — so focus is visible without colour. right is optional
// status text placed at the right end of the top border.
func (s styles) box(title, right string, lines []string, w, h int, focused bool) []string {
	if w < 4 || h < 2 {
		return blank(w, h)
	}
	tl, tr, bl, br, hz, vt := "┌", "┐", "└", "┘", "─", "│"
	bs, ts := s.borderBlur, s.titleBlur
	if focused {
		tl, tr, bl, br, hz, vt = "┏", "┓", "┗", "┛", "━", "┃"
		bs, ts = s.borderFocus, s.titleFocus
		title = "◆ " + title
	}
	inner := w - 2

	// Top border: ┌─ title ───── right ─┐
	avail := inner - 3 // corner, one border cell and the two spaces around the title
	label := ansi.Truncate(title, max(avail, 0), "…")
	lw := ansi.StringWidth(label)
	rw := ansi.StringWidth(right)
	top := bs.Render(tl+hz) + ts.Render(" "+label+" ")
	fill := inner - 1 - (lw + 2)
	switch {
	case right != "" && fill >= rw+4:
		top += bs.Render(strings.Repeat(hz, fill-rw-3)) + ts.Render(" "+right+" ") + bs.Render(hz+tr)
	case fill >= 0:
		top += bs.Render(strings.Repeat(hz, fill) + tr)
	default:
		top = bs.Render(tl + strings.Repeat(hz, inner) + tr)
	}

	out := make([]string, 0, h)
	out = append(out, top)
	side := bs.Render(vt)
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, side+fit(line, inner)+side)
	}
	out = append(out, bs.Render(bl+strings.Repeat(hz, inner)+br))
	return out
}

func blank(w, h int) []string {
	out := make([]string, max(h, 0))
	for i := range out {
		out[i] = strings.Repeat(" ", max(w, 0))
	}
	return out
}

// joinH places blocks of lines side by side. Every block must already have
// lines of its own exact width.
func joinH(blocks ...[]string) []string {
	h := 0
	for _, b := range blocks {
		if len(b) > h {
			h = len(b)
		}
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		var sb strings.Builder
		for _, b := range blocks {
			if i < len(b) {
				sb.WriteString(b[i])
			} else if len(b) > 0 {
				sb.WriteString(strings.Repeat(" ", ansi.StringWidth(b[0])))
			}
		}
		out[i] = sb.String()
	}
	return out
}

// overlay draws block onto base with its top-left corner at (x, y).
func (s styles) overlay(base []string, block []string, x, y, w int) []string {
	reset := ""
	if s.color {
		reset = "\x1b[0m"
	}
	out := append([]string(nil), base...)
	for i, line := range block {
		row := y + i
		if row < 0 || row >= len(out) {
			continue
		}
		left := ansi.Truncate(out[row], x, "")
		if d := x - ansi.StringWidth(left); d > 0 {
			left += strings.Repeat(" ", d)
		}
		right := ansi.TruncateLeft(out[row], x+w, "")
		out[row] = left + reset + fit(line, w) + reset + right
	}
	return out
}
