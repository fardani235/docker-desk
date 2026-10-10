package live

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Bounds on retained live data. See docs/DECISIONS.md, D8.
const (
	LogLines     = 5000
	LogTail      = 1000 // lines requested when a log stream opens
	EventLines   = 1000
	StatsHistory = 120
	// MaxStatsStreams caps concurrent per-container statistics streams.
	MaxStatsStreams = 32

	statsStaleAfter = 5 * time.Second
	statsRetryEvery = 2 * time.Second
)

// Kinds of object whose lists an engine event can make stale.
const (
	DirtyContainers uint32 = 1 << iota
	DirtyImages
	DirtyVolumes
	DirtyNetworks
	DirtyAll = DirtyContainers | DirtyImages | DirtyVolumes | DirtyNetworks
)

// EventEntry is one line of the Events pane: an engine event, or a marker the
// desk inserted about the stream itself.
type EventEntry struct {
	Event  engine.Event
	Marker string // non-empty for "stream interrupted" / "stream resumed"
	Time   time.Time
}

// LogStatus describes the log stream for the current target.
type LogStatus struct {
	Target    string // container ID, empty when no stream is wanted
	Streaming bool
	Ended     bool   // the daemon closed the stream (container not running)
	Err       string // why the stream failed, if it did
}

// Hub owns every live stream of the desk.
type Hub struct {
	eng    engine.Engine
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Events is the engine event history.
	Events *Ring[EventEntry]

	connected atomic.Bool
	dirty     atomic.Uint32

	mu        sync.Mutex
	logTarget string
	logRing   *Ring[string]
	logCancel context.CancelFunc
	logGen    uint64
	logStatus LogStatus

	stats        map[string]*statStream
	statsVersion atomic.Uint64

	// Backoff bounds for re-establishing the event stream; tests shorten them.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// PingTimeout is how long the daemon gets to answer while the hub waits
	// for it to come back. Two seconds is plenty for a local socket; a
	// daemon a slow link and a TLS handshake away is given longer.
	PingTimeout time.Duration
}

type statStream struct {
	cancel  context.CancelFunc
	history *Ring[Sample]
	ended   atomic.Bool
	endedAt time.Time // guarded by Hub.mu
	errMsg  string    // guarded by Hub.mu
}

// NewHub returns a hub; Start begins streaming.
func NewHub(eng engine.Engine) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		eng:         eng,
		ctx:         ctx,
		cancel:      cancel,
		Events:      NewRing[EventEntry](EventLines, nil),
		logRing:     NewRing[string](LogLines, nil),
		stats:       map[string]*statStream{},
		MinBackoff:  250 * time.Millisecond,
		MaxBackoff:  5 * time.Second,
		PingTimeout: 2 * time.Second,
	}
	h.connected.Store(true)
	return h
}

// Start begins following the engine event stream.
func (h *Hub) Start() {
	h.wg.Add(1)
	go h.eventLoop()
}

// Close stops every stream and waits for their goroutines.
func (h *Hub) Close() {
	h.cancel()
	h.wg.Wait()
}

// Connected reports whether the event stream is currently established.
func (h *Hub) Connected() bool { return h.connected.Load() }

// TakeDirty returns and clears the set of kinds whose lists should be
// refreshed because of engine events.
func (h *Hub) TakeDirty() uint32 { return h.dirty.Swap(0) }

// MarkDirty requests a refresh of the given kinds.
func (h *Hub) MarkDirty(kinds uint32) {
	for {
		old := h.dirty.Load()
		if h.dirty.CompareAndSwap(old, old|kinds) {
			return
		}
	}
}

// ---- events ---------------------------------------------------------------------

func dirtyFor(eventType string) uint32 {
	switch eventType {
	case "container":
		return DirtyContainers
	case "image":
		// An image appearing or disappearing changes "in use" facts too.
		return DirtyImages
	case "volume":
		return DirtyVolumes
	case "network":
		// connect/disconnect change containers' network lists.
		return DirtyNetworks | DirtyContainers
	}
	return 0
}

func (h *Hub) eventLoop() {
	defer h.wg.Done()
	var last time.Time // time of the newest event seen
	backoff := h.MinBackoff
	first := true
	for {
		since := time.Time{}
		if !first {
			// Ask the daemon to replay what happened while disconnected.
			since = last
		}
		streamStart := time.Now()
		err := h.eng.Events(h.ctx, since, func(e engine.Event) {
			if !first && !e.Time.After(last) {
				return // replayed event already shown
			}
			if e.Time.After(last) {
				last = e.Time
			}
			h.Events.Append(EventEntry{Event: e, Time: e.Time})
			h.MarkDirty(dirtyFor(e.Type))
			h.onEvent(e)
		})
		if h.ctx.Err() != nil {
			return
		}
		if last.IsZero() {
			last = streamStart
		}
		first = false
		msg := "event stream interrupted"
		if err != nil {
			msg += ": " + err.Error()
		}
		if h.connected.Swap(false) {
			h.Events.Append(EventEntry{Marker: msg, Time: time.Now()})
		}
		if time.Since(streamStart) > 2*h.MaxBackoff {
			backoff = h.MinBackoff // the stream had been healthy; start over
		}

		// Wait until the daemon answers again.
		for {
			select {
			case <-h.ctx.Done():
				return
			case <-time.After(backoff):
			}
			pctx, cancel := context.WithTimeout(h.ctx, h.PingTimeout)
			perr := h.eng.Ping(pctx)
			cancel()
			if perr == nil {
				break
			}
			if backoff *= 2; backoff > h.MaxBackoff {
				backoff = h.MaxBackoff
			}
		}
		h.connected.Store(true)
		h.Events.Append(EventEntry{Marker: "event stream resumed", Time: time.Now()})
		h.MarkDirty(DirtyAll)
		h.restartLogIfEnded()
	}
}

// onEvent reacts to events that affect the streams themselves.
func (h *Hub) onEvent(e engine.Event) {
	if e.Type != "container" {
		return
	}
	switch e.Action {
	case "start", "unpause":
		// The daemon closes a log follow when the container exits; pick it
		// up again when the container we are showing comes back.
		h.mu.Lock()
		restart := h.logTarget == e.ID && !h.logStatus.Streaming
		h.mu.Unlock()
		if restart {
			h.openLog(e.ID)
		}
	}
}

// ---- logs -----------------------------------------------------------------------

// SetLogTarget makes the hub follow the logs of one container, or none when
// id is empty. Any previous stream is closed. Calling it again with the same
// target keeps the existing stream and buffer.
func (h *Hub) SetLogTarget(id string) {
	h.mu.Lock()
	same := h.logTarget == id
	h.mu.Unlock()
	if same {
		return
	}
	h.openLog(id)
}

func (h *Hub) restartLogIfEnded() {
	h.mu.Lock()
	id := h.logTarget
	need := id != "" && !h.logStatus.Streaming
	h.mu.Unlock()
	if need {
		h.openLog(id)
	}
}

func (h *Hub) openLog(id string) {
	h.mu.Lock()
	if h.logCancel != nil {
		h.logCancel()
		h.logCancel = nil
	}
	h.logGen++
	gen := h.logGen
	h.logTarget = id
	// A fresh ring per stream: the view never sees another container's lines.
	ring := NewRing[string](LogLines, nil)
	h.logRing = ring
	h.logStatus = LogStatus{Target: id, Streaming: id != ""}
	if id == "" || h.ctx.Err() != nil {
		h.logStatus.Streaming = false
		h.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.logCancel = cancel
	h.mu.Unlock()

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer cancel()
		w := NewLineWriter(ring)
		err := h.eng.Logs(ctx, id, LogTail, true, w)
		w.Flush()
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.logGen != gen {
			return // superseded by a newer stream
		}
		h.logStatus.Streaming = false
		switch {
		case err == nil:
			h.logStatus.Ended = true
		case errors.Is(err, context.Canceled):
		default:
			h.logStatus.Err = err.Error()
		}
		ring.bump() // make the view redraw its status line
	}()
}

// LogGeneration changes every time a log stream is opened or closed.
func (h *Hub) LogGeneration() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logGen
}

// Log returns the buffer and status of the current log stream.
func (h *Hub) Log() (*Ring[string], LogStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logRing, h.logStatus
}

// ---- statistics -----------------------------------------------------------------

// SyncStats makes the set of statistics streams match the running
// containers: at most MaxStatsStreams, always including selected when it is
// running. Streams for containers no longer in the set are closed; streams
// that ended while their container is still running are reopened.
func (h *Hub) SyncStats(running []string, selected string) {
	want := make(map[string]bool, len(running))
	isRunning := false
	for _, id := range running {
		if id == selected {
			isRunning = true
			break
		}
	}
	if isRunning {
		want[selected] = true
	}
	for _, id := range running {
		if len(want) >= MaxStatsStreams {
			break
		}
		want[id] = true
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	changed := false
	for id, st := range h.stats {
		if !want[id] {
			st.cancel()
			delete(h.stats, id)
			changed = true
			continue
		}
		if st.ended.Load() && time.Since(st.endedAt) >= statsRetryEvery {
			st.cancel()
			delete(h.stats, id)
		}
	}
	if h.ctx.Err() != nil {
		return
	}
	for id := range want {
		if _, ok := h.stats[id]; ok {
			continue
		}
		h.startStatsLocked(id)
		changed = true
	}
	if changed {
		h.statsVersion.Add(1)
	}
}

func (h *Hub) startStatsLocked(id string) {
	ctx, cancel := context.WithCancel(h.ctx)
	st := &statStream{cancel: cancel, history: NewRing[Sample](StatsHistory, &h.statsVersion)}
	h.stats[id] = st
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer cancel()
		var prev engine.RawStats
		have := false
		err := h.eng.Stats(ctx, id, func(raw engine.RawStats) {
			if have {
				if s, ok := Derive(prev, raw); ok {
					st.history.Append(s)
				}
			}
			prev, have = raw, true
		})
		h.mu.Lock()
		st.endedAt = time.Now()
		if err != nil && !errors.Is(err, context.Canceled) {
			st.errMsg = err.Error()
		}
		h.mu.Unlock()
		st.ended.Store(true)
		h.statsVersion.Add(1)
	}()
}

// StatsVersion changes whenever any statistics sample arrives or a stream
// starts or ends.
func (h *Hub) StatsVersion() uint64 { return h.statsVersion.Load() }

// StatsState says what is known about one container's statistics.
type StatsState struct {
	// Collected is false when no stream exists for the container (it is not
	// running, or it is beyond the stream cap).
	Collected bool
	// Latest is the newest sample; HasSample is false until two raw samples
	// have arrived.
	Latest    Sample
	HasSample bool
	// Stale is true when the newest sample is old: the stream stalled.
	Stale bool
	Ended bool
	Err   string
}

// Stats returns the current statistics state of a container.
func (h *Hub) Stats(id string) StatsState {
	h.mu.Lock()
	st := h.stats[id]
	var errMsg string
	if st != nil {
		errMsg = st.errMsg
	}
	h.mu.Unlock()
	if st == nil {
		return StatsState{}
	}
	out := StatsState{Collected: true, Ended: st.ended.Load(), Err: errMsg}
	if s, ok := st.history.Last(); ok {
		out.Latest, out.HasSample = s, true
		out.Stale = time.Since(s.Time) > statsStaleAfter
	}
	return out
}

// StatsHistory returns up to n of the newest samples for a container, oldest
// first.
func (h *Hub) StatsHistory(id string, n int) []Sample {
	h.mu.Lock()
	st := h.stats[id]
	h.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.history.Tail(n)
}

// StatsStreams returns the number of open statistics streams.
func (h *Hub) StatsStreams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, st := range h.stats {
		if !st.ended.Load() {
			n++
		}
	}
	return n
}
