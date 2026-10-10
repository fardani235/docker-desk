package live

import (
	"strings"
	"unicode/utf8"
)

const (
	// MaxLineBytes is the longest log line kept; longer output is split.
	MaxLineBytes = 2048
	tabWidth     = 8
)

// LineWriter is an io.Writer that turns a byte stream into sanitised lines in
// a Ring. It is written to by exactly one stream goroutine.
//
// Bounds: a line never exceeds MaxLineBytes (a writer that never sends a
// newline cannot grow memory), and each Write appends its lines under one
// lock acquisition.
type LineWriter struct {
	ring    *Ring[string]
	pending []byte
	sawCR   bool
	batch   []string
}

// NewLineWriter returns a writer appending to ring.
func NewLineWriter(ring *Ring[string]) *LineWriter {
	return &LineWriter{ring: ring, pending: make([]byte, 0, 256)}
}

// Write never fails and never blocks on a reader.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.batch = w.batch[:0]
	for _, b := range p {
		switch {
		case b == '\n':
			w.sawCR = false
			w.emit()
		case b == '\r':
			// Decide when the next byte arrives: CRLF ends the line, a lone
			// CR rewinds it (progress bars redraw the same line).
			w.sawCR = true
		default:
			if w.sawCR {
				w.sawCR = false
				w.pending = w.pending[:0]
			}
			w.pending = append(w.pending, b)
			if len(w.pending) >= MaxLineBytes {
				w.emitSplit()
			}
		}
	}
	w.ring.AppendMany(w.batch)
	return len(p), nil
}

// Flush emits a final line that did not end with a newline.
func (w *LineWriter) Flush() {
	if len(w.pending) > 0 {
		w.batch = w.batch[:0]
		w.emit()
		w.ring.AppendMany(w.batch)
	}
}

func (w *LineWriter) emit() {
	w.batch = append(w.batch, Sanitize(w.pending))
	w.pending = w.pending[:0]
}

// emitSplit cuts an over-long line, taking care not to cut inside a UTF-8
// sequence.
func (w *LineWriter) emitSplit() {
	cut := len(w.pending)
	for back := 1; back <= utf8.UTFMax && back <= len(w.pending); back++ {
		i := len(w.pending) - back
		if utf8.RuneStart(w.pending[i]) {
			if !utf8.FullRune(w.pending[i:]) {
				cut = i // an incomplete sequence starts here; keep it for the next line
			}
			break
		}
	}
	if cut == 0 {
		cut = len(w.pending)
	}
	rest := append([]byte(nil), w.pending[cut:]...)
	w.pending = w.pending[:cut]
	w.emit()
	w.pending = append(w.pending, rest...)
}

// Sanitize makes one line of untrusted container output safe to draw inside
// a pane. Colour (SGR) sequences are kept; every other escape sequence and
// control character is removed, tabs become spaces, invalid UTF-8 becomes
// U+FFFD. The result never moves the cursor, clears the screen, sets the
// window title or touches the clipboard.
func Sanitize(line []byte) string {
	// Fast path: plain printable ASCII needs no work and no allocation
	// beyond the string itself.
	clean := true
	for _, b := range line {
		if b < 0x20 || b >= 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return string(line)
	}

	var sb strings.Builder
	sb.Grow(len(line) + 8)
	col := 0
	sgr := false
	for i := 0; i < len(line); {
		b := line[i]
		switch {
		case b == 0x1b:
			n, keep := escapeLen(line[i:])
			if keep {
				sb.Write(line[i : i+n])
				sgr = true
			}
			i += n
		case b == '\t':
			spaces := tabWidth - col%tabWidth
			sb.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			i++
		case b < 0x20 || b == 0x7f:
			i++ // other C0 controls and DEL are dropped
		case b < utf8.RuneSelf:
			sb.WriteByte(b)
			col++
			i++
		default:
			r, size := utf8.DecodeRune(line[i:])
			i += size
			switch {
			case r == utf8.RuneError && size <= 1:
				sb.WriteRune(utf8.RuneError)
				col++
			case r >= 0x80 && r <= 0x9f:
				// C1 controls (including the 8-bit CSI) are dropped.
			default:
				sb.WriteRune(r)
				col++
			}
		}
	}
	if sgr {
		sb.WriteString("\x1b[0m") // never let a colour leak into the pane
	}
	return sb.String()
}

// escapeLen returns the length of the escape sequence at the start of b and
// whether it is an SGR (colour) sequence worth keeping.
func escapeLen(b []byte) (n int, keep bool) {
	if len(b) < 2 {
		return len(b), false
	}
	switch b[1] {
	case '[': // CSI: parameters 0x30–0x3f, intermediates 0x20–0x2f, final 0x40–0x7e
		i := 2
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x3f {
			i++
		}
		if i >= len(b) {
			return len(b), false // unterminated: drop the rest
		}
		if b[i] < 0x40 || b[i] > 0x7e {
			return i, false
		}
		// Keep only plain SGR: digits, ';' and ':' followed by 'm'.
		if b[i] == 'm' {
			for _, c := range b[2:i] {
				if !(c >= '0' && c <= '9') && c != ';' && c != ':' {
					return i + 1, false
				}
			}
			return i + 1, true
		}
		return i + 1, false
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: until BEL or ST
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1, false
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2, false
			}
		}
		return len(b), false
	default:
		// Two-byte escapes (ESC c, ESC 7, …) and charset selections (ESC ( B).
		if b[1] >= 0x20 && b[1] <= 0x2f && len(b) >= 3 {
			return 3, false
		}
		return 2, false
	}
}
