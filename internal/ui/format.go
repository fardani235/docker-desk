package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// fit returns s cut or padded to exactly w terminal cells. Escape sequences
// in s are preserved and never counted.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	switch {
	case sw == w:
		return s
	case sw > w:
		if w == 1 {
			return ansi.Truncate(s, 1, "")
		}
		s = ansi.Truncate(s, w, "…")
		if d := w - ansi.StringWidth(s); d > 0 {
			s += strings.Repeat(" ", d)
		}
		return s
	default:
		return s + strings.Repeat(" ", w-sw)
	}
}

// fitRight right-aligns s in w cells.
func fitRight(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw >= w {
		return fit(s, w)
	}
	return strings.Repeat(" ", w-sw) + s
}

// wrapLine breaks one line into rows of at most w cells.
func wrapLine(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	if ansi.StringWidth(s) <= w {
		return []string{s}
	}
	return strings.Split(ansi.Hardwrap(s, w, true), "\n")
}

// wrapWords breaks prose at word boundaries (hard-breaking only words that
// are longer than a row).
func wrapWords(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	if ansi.StringWidth(s) <= w {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

func humanBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f%cB", v, "kMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%cB", v, "kMGTPE"[exp])
}

// humanIEC formats memory the way `docker stats` does (MiB, GiB).
func humanIEC(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f%ciB", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%ciB", v, "KMGTPE"[exp])
}

func humanRate(bytesPerSec float64) string {
	if bytesPerSec < 0 {
		bytesPerSec = 0
	}
	return humanBytes(int64(bytesPerSec)) + "/s"
}

func humanAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 2*365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// sparkline draws values as one row of block characters, scaled to max
// (or to the largest value when max <= 0), right-aligned in w cells.
func sparkline(values []float64, w int, max float64) string {
	const blocks = "▁▂▃▄▅▆▇█"
	runes := []rune(blocks)
	if len(values) > w {
		values = values[len(values)-w:]
	}
	if max <= 0 {
		for _, v := range values {
			if v > max {
				max = v
			}
		}
	}
	var sb strings.Builder
	sb.WriteString(strings.Repeat(" ", w-len(values)))
	for _, v := range values {
		idx := 0
		if max > 0 {
			idx = int(v / max * float64(len(runes)-1))
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(runes) {
			idx = len(runes) - 1
		}
		sb.WriteRune(runes[idx])
	}
	return sb.String()
}

// bar draws a horizontal gauge of w cells filled to fraction.
func bar(fraction float64, w int) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	full := int(fraction*float64(w) + 0.5)
	return strings.Repeat("█", full) + strings.Repeat("░", w-full)
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
