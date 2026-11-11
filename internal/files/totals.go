package files

import "fmt"

// Totals is what the view's header says about the index as a whole: what
// it kept of what it saw and why, what later layers removed, and its own
// total beside the daemon's figure for the image.
type Totals struct {
	Kept, Seen                                   int
	DroppedEntries, DroppedLongPath, DroppedText int
	MaxEntries, MaxPath                          int
	MaxText                                      int64
	Removed, Hidden                              int
	Unread                                       []Unread
	// TreeBytes is the sum of the files in the merged tree; DaemonBytes
	// the image size the daemon reports (0 when it is not known).
	TreeBytes, DaemonBytes int64
}

// Totals collects the header's figures; daemonSize is the image list's.
func (ix *Index) Totals(daemonSize int64) Totals {
	return Totals{
		Kept: ix.Kept, Seen: ix.Seen,
		DroppedEntries: ix.DroppedEntries, DroppedLongPath: ix.DroppedLongPath, DroppedText: ix.DroppedText,
		MaxEntries: ix.MaxEntries, MaxPath: ix.MaxPath, MaxText: ix.MaxText,
		Removed: ix.Removed, Hidden: ix.Hidden, Unread: ix.Unread,
		TreeBytes: ix.Total, DaemonBytes: daemonSize,
	}
}

// Line is one labelled line of the header; Warn marks what the operator
// must read before trusting an absence.
type Line struct {
	Label, Text string
	Warn        bool
}

// DifferentMeasures is said whenever both totals are shown.
const DifferentMeasures = "different measurements, not a contradiction: layers overlap"

// DifferentMeasuresWhy is the longer reason, for the detail and the help.
const DifferentMeasuresWhy = "the daemon counts every layer's bytes, including what a later layer replaced or removed, " +
	"and its own storage; the tree counts only the files left at the end"

// Lines says the totals in words, the way the header shows them.
func (t Totals) Lines() []Line {
	var out []Line
	idx := fmt.Sprintf("%s entries indexed of %s seen", Count(t.Kept), Count(t.Seen))
	if t.Kept == t.Seen {
		idx = fmt.Sprintf("%s indexed, every one the archive holds", Plural(t.Kept, "entry", "entries"))
	}
	out = append(out, Line{Label: "indexed", Text: idx, Warn: t.Kept != t.Seen})
	if t.DroppedEntries > 0 {
		out = append(out, Line{Label: "bound", Warn: true, Text: fmt.Sprintf("%s dropped past the bound of %s entries", Count(t.DroppedEntries), Count(t.MaxEntries))})
	}
	if t.DroppedLongPath > 0 {
		out = append(out, Line{Label: "bound", Warn: true, Text: fmt.Sprintf("%s dropped: a path longer than %s bytes, the length bound", Count(t.DroppedLongPath), Count(t.MaxPath))})
	}
	if t.DroppedText > 0 {
		out = append(out, Line{Label: "bound", Warn: true, Text: fmt.Sprintf("%s dropped past the bound of %s of path text", Count(t.DroppedText), Bytes(t.MaxText))})
	}
	for _, u := range t.Unread {
		out = append(out, Line{Label: "not read", Warn: true, Text: fmt.Sprintf("layer %d: %s; no path is shown, or said to be absent, on its account", u.Layer, u.Reason)})
	}
	if t.Removed > 0 {
		s := Plural(t.Removed, "path", "paths") + " removed by later layers"
		if t.Hidden > 0 {
			s += ", " + Plural(t.Hidden, "entry", "entries") + " beneath or under opaque directories"
		}
		out = append(out, Line{Label: "removed", Text: s + "; not drawn"})
	} else if t.Hidden > 0 {
		out = append(out, Line{Label: "removed", Text: Plural(t.Hidden, "entry", "entries") + " under opaque directories; not drawn"})
	}
	size := "the tree's files " + Bytes(t.TreeBytes)
	if t.DaemonBytes > 0 {
		size += " · the daemon's size " + Bytes(t.DaemonBytes)
		if t.DaemonBytes != t.TreeBytes {
			size += " — " + DifferentMeasures
		}
	}
	out = append(out, Line{Label: "size", Text: size})
	return out
}

// Count writes n with thousands separators: 200,000.
func Count(n int) string {
	s := fmt.Sprint(n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Bytes is the desk's size format: decimal units, one decimal below 100.
func Bytes(n int64) string {
	if n < 0 {
		return "-"
	}
	if n < 1000 {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(1000), 0
	for v := n / 1000; v >= 1000; v /= 1000 {
		div *= 1000
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f%cB", v, "kMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%cB", v, "kMGTPE"[exp])
}

// Plural is n with the word that agrees with it: "1 path", "2 paths".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return Count(n) + " " + many
}
