package session

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/attention"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/live"
)

// ---- the Attention view's reads ------------------------------------------------------
//
// An unfocused member keeps an event stream and a ping, and no list refresh
// of its own. The one exception is an explicitly opened view: while it is on
// screen, an Attention holds one read run for the whole session — each
// connected member's container list, then an inspect of the containers whose
// class or reason needs one — and it ends with the view (Close).
//
// Nothing here is periodic. A member is read when the view opens, when the
// operator asks (Refresh), when it connects while the view is open, and when
// its own event stream — which it follows anyway — reports a container
// exit, an OOM kill or a health change. Sync, called from the desk's frame
// tick, only looks at counters and ring positions; on a quiet session it
// sends nothing.
//
// Every request is a GET: the container list and container inspects.

// Defaults of AttentionConfig.
const (
	// AttentionInspects is the bound on container inspects per daemon per
	// read. Candidates beyond it are reported as not inspected.
	AttentionInspects = 200
	// AttentionWorkers is how many inspects of one daemon run at a time.
	AttentionWorkers = 4
	// AttentionGap is the shortest time between two reads of one daemon.
	AttentionGap = time.Second
	// AttentionContainers is how many containers of one daemon's list the
	// view looks at. A list longer than that has the rest counted as not
	// looked at: no daemon can make the view classify, sort and hold an
	// unbounded list on every answer.
	AttentionContainers = 5000
	// attentionFieldMax bounds each string the view keeps of a container
	// (a name, an image, a status): they are a daemon's to choose.
	attentionFieldMax = 256
)

// AttentionConfig bounds a view's reads. Zero values select the defaults.
type AttentionConfig struct {
	MaxInspects    int
	MaxContainers  int
	Workers        int
	Gap            time.Duration
	ListTimeout    time.Duration // one container list (15 s)
	InspectTimeout time.Duration // one container inspect (10 s)
	// Now is the clock that dates a read; nil is time.Now.
	Now func() time.Time
}

// Attention is one open view's read run over the session.
type Attention struct {
	s      *Session
	cfg    AttentionConfig
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	ver     uint64
	closed  bool
	members map[int]*attnMember
}

// attnMember is what the run holds about one member. Guarded by Attention.mu.
type attnMember struct {
	mem *Member

	// The connection the run follows: a change of either means a new engine
	// and a new event ring.
	connected bool
	gen       uint64
	hub       *live.Hub
	evSeen    uint64 // the ring position looked at so far

	// The read in flight, or the last one.
	seq     uint64 // an answer of another seq is dropped
	cancel  context.CancelFunc
	reading bool
	stale   bool // a read is wanted
	started time.Time
	ended   time.Time
	// seed, when hasSeed, is a list the next read uses instead of asking.
	seed    []attention.Container
	seedAt  time.Time
	hasSeed bool
	// touched are the containers an exit, OOM or health event named since
	// the last list was installed: what was read about them before is not
	// carried into the next read. touchedAll: events were lost, so any may
	// have changed.
	touched    map[string]bool
	touchedAll bool
	// followUp: the read that is due was asked for because an inspect
	// disagreed with the list; it may not ask for another.
	followUp bool

	// What was read.
	loaded     bool
	seeded     bool // the list came from the desk's own snapshot
	containers []attention.Container
	readAt     time.Time
	beyond     int // containers of the list past the view's bound
	details    map[string]attention.Detail
	skipped    map[string]attention.Skip
	err        error
	errAt      time.Time

	// rev changes with anything a block is built from; built is the block
	// as of builtRev and builtPending.
	rev          uint64
	built        attention.Block
	builtRev     uint64
	builtPending bool
	builtOK      bool
}

// AttentionBlock is one member's part of the view, as of the last answer.
type AttentionBlock struct {
	Member *Member
	// State and Reason are the member's when the block was taken; a member
	// that is not connected has no rows.
	State  State
	Reason string
	// Loaded reports that a container list is in; Reading that a read —
	// the list, or its inspects — is under way.
	Loaded  bool
	Reading bool
	// Seeded reports that the list is the one the desk already held.
	Seeded bool
	// ReadAt is when the list shown was read.
	ReadAt time.Time
	// Err is why the last read failed, and ErrAt when; the rows, if any,
	// are those of ReadAt.
	Err   error
	ErrAt time.Time
	// Bound is the number of inspects a read stops at.
	Bound int
	// Beyond is how many containers of the daemon's list were past the
	// view's bound (MaxContainers) and not looked at; they are in no class
	// and not in Block.Counts.
	Beyond int
	// ListBound is that bound.
	ListBound int
	Block     attention.Block
}

// OpenAttention starts a run for a view that has just opened. Nothing is
// read until Sync is called. The run ends with Close, or with the session.
func (s *Session) OpenAttention(cfg AttentionConfig) *Attention {
	if cfg.MaxInspects <= 0 {
		cfg.MaxInspects = AttentionInspects
	}
	if cfg.MaxContainers <= 0 {
		cfg.MaxContainers = AttentionContainers
	}
	if cfg.Workers <= 0 {
		cfg.Workers = AttentionWorkers
	}
	if cfg.Gap <= 0 {
		cfg.Gap = AttentionGap
	}
	if cfg.ListTimeout <= 0 {
		cfg.ListTimeout = 15 * time.Second
	}
	if cfg.InspectTimeout <= 0 {
		cfg.InspectTimeout = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Attention{s: s, cfg: cfg, ctx: ctx, cancel: cancel, members: map[int]*attnMember{}}
	s.mu.Lock()
	if s.closed {
		a.closed = true
		cancel()
	} else {
		if s.attention == nil {
			s.attention = map[*Attention]struct{}{}
		}
		s.attention[a] = struct{}{}
	}
	s.mu.Unlock()
	return a
}

// Close ends the run: reads in flight are cancelled and no further request
// is made for it. It does not wait for them; Wait does.
func (a *Attention) Close() {
	a.mu.Lock()
	already := a.closed
	a.closed = true
	a.mu.Unlock()
	a.cancel()
	if already {
		return
	}
	// The run stays known to the session until its reads have ended, so
	// that a session closing right behind it still waits for them before
	// it releases the engines they use.
	go func() {
		a.wg.Wait()
		a.s.mu.Lock()
		delete(a.s.attention, a)
		a.s.mu.Unlock()
	}()
}

// Wait returns once every read of a closed run has ended.
func (a *Attention) Wait() { a.wg.Wait() }

// Version changes whenever Blocks would return something else.
func (a *Attention) Version() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ver
}

// Reading reports whether any read is under way.
func (a *Attention) Reading() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, am := range a.members {
		if am.reading {
			return true
		}
	}
	return false
}

// Seed gives the run the container list the desk already holds for a
// member, read at the given time: its block is filled at once, and its first
// read asks only for the details. It has no effect once the member has been
// read.
func (a *Attention) Seed(mem *Member, cs []engine.Container, at time.Time) {
	if mem == nil {
		return
	}
	list, beyond := convertContainers(cs, a.cfg.MaxContainers)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	am := a.memberLocked(mem)
	if am.loaded || am.reading {
		return
	}
	am.seed, am.seedAt, am.hasSeed = list, at, true
	am.containers, am.readAt, am.beyond = list, at, beyond
	am.loaded, am.seeded = true, true
	am.details, am.skipped = map[string]attention.Detail{}, map[string]attention.Skip{}
	a.changed(am)
}

// changed records that what am's block is built from is different.
func (a *Attention) changed(am *attnMember) {
	am.rev++
	a.ver++
}

// Refresh asks for every connected member to be read again: the operator's
// r. A member read less than the gap ago is read once the gap has passed.
func (a *Attention) Refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	for _, am := range a.members {
		if !am.connected {
			continue
		}
		am.stale, am.followUp = true, false
		am.hasSeed, am.seed = false, nil
		a.changed(am) // its block says it is being read from now on
	}
}

func (a *Attention) memberLocked(mem *Member) *attnMember {
	am := a.members[mem.id]
	if am == nil {
		am = &attnMember{mem: mem}
		a.members[mem.id] = am
	}
	return am
}

// tells reports whether an event is one the view re-reads for: a container
// exit, an OOM kill or a health change.
func tells(e live.EventEntry) bool {
	if e.Marker != "" || e.Event.Type != "container" {
		return false
	}
	act := e.Event.Action
	return act == "die" || act == "oom" || strings.HasPrefix(act, "health_status")
}

// Sync follows the session and the members' event rings, and starts the
// reads that are due. It is called on every frame while the view is open; it
// sends nothing by itself, and nothing at all when no member has anything
// new to say.
func (a *Attention) Sync(now time.Time) {
	members := a.s.Members()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	present := make(map[int]bool, len(members))
	for _, mem := range members {
		present[mem.id] = true
		am := a.memberLocked(mem)
		state, hub, gen := mem.State(), mem.Hub(), mem.Gen()
		if state != Connected || hub == nil {
			if am.connected || am.reading || am.loaded || am.err != nil {
				// Lost while the view is open: its read is cancelled, and
				// rows that can no longer be vouched for are not shown.
				a.stopLocked(am)
				am.loaded, am.seeded, am.containers, am.details, am.skipped = false, false, nil, nil, nil
				am.err, am.hasSeed, am.seed, am.beyond = nil, false, nil, 0
				am.touched, am.touchedAll, am.followUp = nil, false, false
				a.changed(am)
			}
			am.connected, am.stale = false, false
			continue
		}
		if !am.connected || am.gen != gen || am.hub != hub {
			// Connected, or connected again on another engine: start at
			// the ring's end, and read it once.
			if am.reading {
				a.stopLocked(am)
			}
			am.connected, am.gen, am.hub = true, gen, hub
			_, am.evSeen = hub.Events.Bounds()
			am.stale = true
			a.changed(am)
		} else {
			first, end := hub.Events.Bounds()
			switch {
			case end < am.evSeen:
				am.evSeen = end // the ring was reset
			case end > am.evSeen:
				if first > am.evSeen {
					// More arrived than the ring holds: what was lost may
					// have been an exit, of any container.
					am.stale, am.touchedAll = true, true
				}
				entries, _ := hub.Events.Read(am.evSeen, end)
				am.evSeen = end
				for _, e := range entries {
					if !tells(e) {
						continue
					}
					am.stale, am.followUp = true, false
					if am.touched == nil {
						am.touched = map[string]bool{}
					}
					am.touched[e.Event.ID] = true
				}
			}
		}
		if am.stale && !am.reading && now.Sub(am.started) >= a.cfg.Gap && now.Sub(am.ended) >= a.cfg.Gap {
			a.startLocked(am, now)
		}
	}
	for id, am := range a.members {
		if !present[id] {
			// Removed from the session: its read is cancelled and its
			// block goes.
			a.stopLocked(am)
			delete(a.members, id)
			a.ver++
		}
	}
}

// stopLocked cancels am's read, if one is under way, and disowns its answers.
func (a *Attention) stopLocked(am *attnMember) {
	if am.cancel != nil {
		am.cancel()
		am.cancel = nil
	}
	if am.reading {
		am.reading = false
		am.seq++
		a.changed(am)
	}
}

func (a *Attention) startLocked(am *attnMember, now time.Time) {
	eng := am.mem.Engine()
	if eng == nil {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	am.seq++
	am.cancel, am.reading, am.stale, am.started = cancel, true, false, now
	seed, seedAt, hasSeed := am.seed, am.seedAt, am.hasSeed
	am.seed, am.hasSeed = nil, false
	followUp := am.followUp
	am.followUp = false
	a.changed(am)
	a.wg.Add(1)
	go a.read(ctx, cancel, am, am.seq, eng, seed, seedAt, hasSeed, followUp)
}

// read is one read of one member: its container list — unless the desk's own
// was handed over — and then an inspect of each candidate, up to the bound,
// a few at a time. Every answer is published as it arrives.
func (a *Attention) read(ctx context.Context, cancel context.CancelFunc, am *attnMember, seq uint64, eng engine.Engine, seed []attention.Container, seedAt time.Time, hasSeed, followUp bool) {
	defer a.wg.Done()
	defer cancel()
	// disagreed: an inspect described a container in another state than
	// the list did — it changed in between. Guarded by a.mu.
	disagreed := false
	defer func() { a.finish(am, seq, disagreed && !followUp) }()

	cs, at := seed, seedAt
	beyond := 0
	if hasSeed {
		a.mu.Lock()
		beyond = am.beyond
		a.mu.Unlock()
	} else {
		lctx, lcancel := context.WithTimeout(ctx, a.cfg.ListTimeout)
		list, err := eng.Containers(lctx)
		lcancel()
		if ctx.Err() != nil {
			return
		}
		at = a.cfg.Now()
		if err != nil {
			a.mu.Lock()
			if am.seq == seq && !a.closed {
				// The rows of the last read stay, with their own time.
				am.err, am.errAt = err, at
				a.changed(am)
			}
			a.mu.Unlock()
			return
		}
		cs, beyond = convertContainers(list, a.cfg.MaxContainers)
	}

	cands := attention.Candidates(cs)
	todo := cands
	if len(todo) > a.cfg.MaxInspects {
		todo = todo[:a.cfg.MaxInspects]
	}
	a.mu.Lock()
	if am.seq != seq || a.closed {
		a.mu.Unlock()
		return
	}
	// A detail read before is kept, until this read's own answer replaces
	// it, for a container that is still in the state it was read in and
	// that no exit, OOM or health event has named since: an exit code and
	// a restart count change only through an exit, so what is kept is
	// still what the daemon would say. Anything an event named — or
	// everything, when events were lost — is read before it is classed
	// again. Every candidate is inspected again regardless.
	was := make(map[string]string, len(am.containers))
	for _, c := range am.containers {
		was[c.ID] = c.State
	}
	state := make(map[string]string, len(cs))
	details := make(map[string]attention.Detail, len(cs))
	for _, c := range cs {
		state[c.ID] = c.State
		if d, ok := am.details[c.ID]; ok && was[c.ID] == c.State && !am.touchedAll && !am.touched[c.ID] {
			details[c.ID] = d
		}
	}
	am.touched, am.touchedAll = nil, false
	skipped := map[string]attention.Skip{}
	for _, id := range cands[len(todo):] {
		skipped[id] = attention.SkipBound
		delete(details, id) // not read again, so not vouched for
	}
	am.containers, am.details, am.skipped, am.beyond = cs, details, skipped, beyond
	am.loaded, am.readAt, am.seeded = true, at, hasSeed
	am.err = nil
	a.changed(am)
	a.mu.Unlock()

	ids := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < min(a.cfg.Workers, len(todo)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				if ctx.Err() != nil {
					continue // drain: nothing more is sent
				}
				ictx, icancel := context.WithTimeout(ctx, a.cfg.InspectTimeout)
				d, err := eng.InspectContainer(ictx, id)
				icancel()
				if ctx.Err() != nil {
					continue
				}
				a.mu.Lock()
				if am.seq == seq && !a.closed {
					switch {
					case err != nil:
						delete(am.details, id)
						am.skipped[id] = attention.SkipFailed
					case d.State != "" && d.State != state[id]:
						// It changed between the list and the inspect:
						// the two cannot be read together. It is in no
						// class, and the daemon is read once more.
						delete(am.details, id)
						am.skipped[id] = attention.SkipFailed
						disagreed = true
					default:
						am.details[id] = attention.Detail{
							ExitCode: d.ExitCode, OOMKilled: d.OOMKilled, RestartCount: d.RestartCount,
							FinishedAt: d.FinishedAt, Error: clipField(d.Error),
						}
					}
					a.changed(am)
				}
				a.mu.Unlock()
			}
		}()
	}
feed:
	for _, id := range todo {
		select {
		case ids <- id:
		case <-ctx.Done():
			break feed
		}
	}
	close(ids)
	wg.Wait()
}

// finish marks am's read as ended, if it is still the current one. again
// asks for one more read: an inspect disagreed with the list. That read is a
// follow-up and cannot ask for another, so a daemon whose answers never
// agree is read twice, not for ever.
func (a *Attention) finish(am *attnMember, seq uint64, again bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if am.seq != seq || !am.reading {
		return
	}
	am.reading, am.cancel = false, nil
	am.ended = a.cfg.Now()
	if again && !a.closed {
		am.stale, am.followUp = true, true
	}
	a.changed(am)
}

// clipField bounds one string a daemon chose, on a character boundary.
func clipField(s string) string {
	if len(s) <= attentionFieldMax {
		return s
	}
	cut := attentionFieldMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// convertContainers is a daemon's list as the view keeps it: at most max
// containers, one per ID — a list that names an ID twice is not two
// containers — each string bounded. beyond is how many were left out by the
// bound.
func convertContainers(cs []engine.Container, max int) (out []attention.Container, beyond int) {
	out = make([]attention.Container, 0, min(len(cs), max))
	seen := make(map[string]bool, len(cs))
	for _, c := range cs {
		if c.ID == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if len(out) >= max {
			beyond++
			continue
		}
		out = append(out, attention.Container{
			ID: c.ID, Name: clipField(c.Name), Image: clipField(c.Image), State: clipField(c.State),
			Health: clipField(c.Health), Status: clipField(c.Status),
			Project: clipField(c.Project()), Service: clipField(c.Service()),
		})
	}
	return out, beyond
}

// Blocks returns one block per member of the session: first the member
// whose ID is first — the focused one — then the others in the session's
// order. A member that is not connected has its state and reason, and no
// rows.
func (a *Attention) Blocks(first int) []AttentionBlock {
	members := a.s.Members()
	ordered := make([]*Member, 0, len(members))
	for _, mem := range members {
		if mem.id == first {
			ordered = append(ordered, mem)
		}
	}
	for _, mem := range members {
		if mem.id != first {
			ordered = append(ordered, mem)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AttentionBlock, 0, len(ordered))
	for _, mem := range ordered {
		b := AttentionBlock{Member: mem, State: mem.State(), Reason: mem.Reason(), Bound: a.cfg.MaxInspects, ListBound: a.cfg.MaxContainers}
		am := a.members[mem.id]
		if am == nil || b.State != Connected {
			out = append(out, b)
			continue
		}
		// A read that is due — asked for, or a seed whose details have not
		// been asked yet — is under way as far as the block is concerned.
		pending := am.reading || am.stale || am.hasSeed
		b.Loaded, b.Reading, b.Seeded = am.loaded, pending, am.seeded
		b.ReadAt, b.Err, b.ErrAt, b.Beyond = am.readAt, am.err, am.errAt, am.beyond
		if am.loaded {
			// A block is built again only when what it is built from
			// changed: an answer from one daemon costs the others nothing.
			if !am.builtOK || am.builtRev != am.rev || am.builtPending != pending {
				skipped := am.skipped
				if !pending {
					// Nothing is on its way any more: what has no detail
					// now was not inspected.
					skipped = settled(am)
				}
				am.built = attention.Build(am.containers, am.details, skipped)
				am.builtRev, am.builtPending, am.builtOK = am.rev, pending, true
			}
			b.Block = am.built
			// The caller may cut the rows it lists; the cache keeps its own.
			b.Block.Rows = append([]attention.Row(nil), am.built.Rows...)
		}
		out = append(out, b)
	}
	return out
}

// settled is am.skipped for a member whose read has ended: a candidate left
// without a detail — the read was cut short — counts as not inspected.
func settled(am *attnMember) map[string]attention.Skip {
	var out map[string]attention.Skip
	for _, c := range am.containers {
		if !attention.DecidedByDetail(c) {
			continue
		}
		if _, ok := am.details[c.ID]; ok {
			continue
		}
		if _, ok := am.skipped[c.ID]; ok {
			continue
		}
		if out == nil {
			out = make(map[string]attention.Skip, len(am.skipped)+1)
			for k, v := range am.skipped {
				out[k] = v
			}
		}
		out[c.ID] = attention.SkipFailed
	}
	if out == nil {
		return am.skipped
	}
	return out
}
