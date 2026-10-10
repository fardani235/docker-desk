// Package live holds everything that arrives from the daemon as a stream:
// log lines, statistics samples and engine events.
//
// The rule that keeps the desk responsive is enforced here (see
// docs/DECISIONS.md, D8): a stream is read by its own goroutine into a
// bounded ring and bumps a version counter. Nothing in this package ever
// sends a message to the UI. The UI looks at version counters on its frame
// tick and copies only the rows it is about to draw, so the cost of a frame
// does not depend on how much data arrived.
package live

import (
	"sync"
	"sync/atomic"
)

// Ring is a fixed-capacity buffer that discards its oldest entry when full.
// Entries are addressed by an absolute sequence number that never changes,
// which lets a view hold its place while new entries keep arriving.
type Ring[T any] struct {
	mu    sync.Mutex
	buf   []T
	start int    // index in buf of the oldest entry
	n     int    // entries currently held
	total uint64 // entries ever appended; absolute index of the next entry

	version atomic.Uint64
	notify  *atomic.Uint64 // optional shared counter bumped on every change
}

// NewRing returns a ring holding at most capacity entries. notify, when not
// nil, is incremented on every change in addition to the ring's own version.
func NewRing[T any](capacity int, notify *atomic.Uint64) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring[T]{buf: make([]T, capacity), notify: notify}
}

func (r *Ring[T]) bump() {
	r.version.Add(1)
	if r.notify != nil {
		r.notify.Add(1)
	}
}

// Append adds one entry, discarding the oldest when full.
func (r *Ring[T]) Append(v T) {
	r.mu.Lock()
	r.appendLocked(v)
	r.mu.Unlock()
	r.bump()
}

// AppendMany adds entries under a single lock acquisition.
func (r *Ring[T]) AppendMany(vs []T) {
	if len(vs) == 0 {
		return
	}
	r.mu.Lock()
	// Only the last cap(buf) entries can survive; skip the rest outright.
	if len(vs) > len(r.buf) {
		r.total += uint64(len(vs) - len(r.buf))
		vs = vs[len(vs)-len(r.buf):]
	}
	for _, v := range vs {
		r.appendLocked(v)
	}
	r.mu.Unlock()
	r.bump()
}

func (r *Ring[T]) appendLocked(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
	} else {
		r.buf[r.start] = v
		r.start = (r.start + 1) % len(r.buf)
	}
	r.total++
}

// Version changes whenever the ring's contents change.
func (r *Ring[T]) Version() uint64 { return r.version.Load() }

// Cap returns the capacity.
func (r *Ring[T]) Cap() int { return len(r.buf) }

// Len returns the number of entries currently held.
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Bounds returns the absolute indices [first, end) of the entries held.
// end is also the number of entries ever appended.
func (r *Ring[T]) Bounds() (first, end uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total - uint64(r.n), r.total
}

// Read copies the entries with absolute indices [from, to), clamped to what
// is still held, and returns them with the absolute index of the first one.
func (r *Ring[T]) Read(from, to uint64) (out []T, first uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldest := r.total - uint64(r.n)
	if from < oldest {
		from = oldest
	}
	if to > r.total {
		to = r.total
	}
	if from >= to {
		return nil, from
	}
	out = make([]T, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, r.buf[(r.start+int(i-oldest))%len(r.buf)])
	}
	return out, from
}

// Tail copies the newest n entries.
func (r *Ring[T]) Tail(n int) []T {
	if n <= 0 {
		return nil
	}
	_, end := r.Bounds()
	from := uint64(0)
	if end > uint64(n) {
		from = end - uint64(n)
	}
	out, _ := r.Read(from, end)
	return out
}

// Last returns the newest entry.
func (r *Ring[T]) Last() (v T, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return v, false
	}
	return r.buf[(r.start+r.n-1)%len(r.buf)], true
}

// Reset discards everything, including the sequence numbering.
func (r *Ring[T]) Reset() {
	r.mu.Lock()
	var zero T
	for i := range r.buf {
		r.buf[i] = zero
	}
	r.start, r.n, r.total = 0, 0, 0
	r.mu.Unlock()
	r.bump()
}
