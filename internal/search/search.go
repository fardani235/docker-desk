// Package search finds a query in lines of terminal text and marks what it
// found. It is pure: no I/O, no styles of its own.
//
// Matching is a case-insensitive substring test on the text a reader sees —
// escape sequences removed — whatever the terminal's colour. There are no
// regular expressions: a query is the characters typed, nothing more.
package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Range is a match: byte offsets [Start, End) into the stripped text.
type Range struct{ Start, End int }

// Find returns the non-overlapping occurrences of query in s, which must
// already be stripped of escape sequences, in order. Case is folded rune by
// rune, so offsets always refer to s itself, even where a rune's other case
// is encoded in a different number of bytes. An empty query finds nothing.
func Find(s, query string) []Range {
	if query == "" || len(s) == 0 {
		return nil
	}
	if isASCII(s) && isASCII(query) {
		return findASCII(s, query)
	}
	q := []rune(query)
	var out []Range
	for i := 0; i < len(s); {
		if end, ok := foldPrefix(s[i:], q); ok {
			out = append(out, Range{i, i + end})
			i += end
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return out
}

// Count is len(Find(s, query)) without keeping the ranges.
func Count(s, query string) int { return len(Find(s, query)) }

// Contains reports whether s, stripped of escape sequences, holds query.
func Contains(s, query string) bool {
	if query == "" {
		return false
	}
	return len(Find(Strip(s), query)) > 0
}

// Strip removes escape sequences from s. Offsets into its result are the
// offsets Mark takes: both walk s the same way.
func Strip(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += seqLen(s[i:])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func findASCII(s, query string) []Range {
	var out []Range
	n := len(query)
	for i := 0; i+n <= len(s); {
		j := 0
		for j < n && lowerASCII(s[i+j]) == lowerASCII(query[j]) {
			j++
		}
		if j == n {
			out = append(out, Range{i, i + n})
			i += n
			continue
		}
		i++
	}
	return out
}

// foldPrefix reports whether s starts with q, case folded, and how many
// bytes of s that took.
func foldPrefix(s string, q []rune) (int, bool) {
	i := 0
	for _, want := range q {
		if i >= len(s) {
			return 0, false
		}
		got, size := utf8.DecodeRuneInString(s[i:])
		if !sameFold(got, want) {
			return 0, false
		}
		i += size
	}
	return i, true
}

func sameFold(a, b rune) bool {
	if a == b {
		return true
	}
	// SimpleFold cycles through every case of a rune: K, k and the Kelvin
	// sign are one orbit.
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// Marks are what Mark writes around a match, and around the current one.
type Marks struct{ On, Off, CurOn, CurOff string }

// None reports whether the marks would change nothing.
func (k Marks) None() bool { return k == Marks{} }

// Mark wraps each range of s in k.On … k.Off (the range at index cur in
// k.CurOn … k.CurOff). The ranges are byte offsets into Strip(s) and must be
// ordered and non-overlapping; s itself may carry SGR (colour) sequences.
// Two things keep its own styling intact:
//
//   - a sequence of s met inside a range is copied, then the opening mark is
//     written again, so that a colour change in the text cannot undo it;
//   - after the closing mark, every sequence of s seen since its last reset
//     is written again, so that the text after a match looks as before.
//
// Text outside the ranges is copied byte for byte.
func Mark(s string, ranges []Range, k Marks, cur int) string {
	if len(ranges) == 0 || k.None() {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(ranges)*(len(k.On)+len(k.Off)+8))
	var active []string // SGR sequences in effect, since the last reset
	pos := 0            // offset in the stripped text
	ri := 0             // next or current range
	in := false
	open := func() string {
		if ri == cur {
			return k.CurOn
		}
		return k.On
	}
	closeMark := func() {
		if ri == cur {
			b.WriteString(k.CurOff)
		} else {
			b.WriteString(k.Off)
		}
		for _, a := range active {
			b.WriteString(a)
		}
	}
	for i := 0; i < len(s); {
		// Range boundaries fall between visible bytes, so they are checked
		// before the byte at pos is written.
		if in && pos == ranges[ri].End {
			closeMark()
			in = false
			ri++
		}
		if !in && ri < len(ranges) && pos == ranges[ri].Start {
			b.WriteString(open())
			in = true
		}
		if s[i] == 0x1b {
			n := seqLen(s[i:])
			seq := s[i : i+n]
			b.WriteString(seq)
			if isSGR(seq) {
				if isReset(seq) {
					active = active[:0]
				} else if len(active) < 16 {
					active = append(active, seq)
				}
			}
			if in {
				b.WriteString(open())
			}
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
		pos++
	}
	if in {
		closeMark()
	}
	return b.String()
}

// seqLen is the length of the escape sequence at the start of s, measured
// the way ansi.Strip measures it, so that offsets agree.
func seqLen(s string) int {
	_, _, n, _ := ansi.DecodeSequence(s, ansi.NormalState, nil)
	if n <= 0 {
		return 1
	}
	return n
}

func isSGR(seq string) bool {
	return len(seq) >= 3 && seq[1] == '[' && seq[len(seq)-1] == 'm'
}

func isReset(seq string) bool {
	p := seq[2 : len(seq)-1]
	return p == "" || p == "0"
}

// Clip returns the parts of ranges that fall within [from, to), shifted so
// that from is 0: the matches of one wrapped row of a longer line.
func Clip(ranges []Range, from, to int) []Range {
	var out []Range
	for _, r := range ranges {
		if r.End <= from || r.Start >= to {
			continue
		}
		out = append(out, Range{max(r.Start, from) - from, min(r.End, to) - from})
	}
	return out
}
