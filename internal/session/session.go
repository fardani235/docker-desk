// Package session is the set of Docker daemons one running desk holds: an
// ordered list of members, one of them focused.
//
// Each member owns what it takes to talk to its daemon — the engine, the
// live-stream hub and, for an ssh:// endpoint, the tunnel. The focused
// member's hub streams everything the panes ask for; every other member's hub
// keeps only its event stream and its ping (live.ModeEventsOnly), and the
// little that matters from it reaches the operator as a notice (TakeNotices).
//
// The single-daemon desk is the case of a session with one member: the
// launch daemon, added with AddLaunch. Members added later come from the
// picker (Add) and are connected in the background, never asking anything
// on the terminal (Connect). A member is removed with Remove, which is
// refused while a job runs on its daemon — checked under the job manager's
// own lock, so that a job cannot start in between (jobs.Manager.CloseDaemon).
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
)

// State is where a member is, as the strip and the picker show it.
type State int

const (
	// Connecting: added, and the first attempt to reach it is under way.
	Connecting State = iota
	// Connected: its event stream is established.
	Connected
	// Disconnected: it does not answer, and is being retried. Reason says why.
	Disconnected
	// Removing: on its way out of the session.
	Removing
)

func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	case Disconnected:
		return "disconnected"
	case Removing:
		return "removing"
	}
	return "unknown"
}

// Errors from the session's operations.
var (
	ErrNoMember     = errors.New("no such daemon in the session")
	ErrNotConnected = errors.New("the daemon is not connected")
	ErrClosed       = errors.New("the session is closed")
)

// RefusedError is a removal refused because jobs are running on the daemon.
type RefusedError struct {
	Member *Member
	Jobs   []jobs.Job
}

func (e *RefusedError) Error() string {
	names := make([]string, 0, len(e.Jobs))
	for _, j := range e.Jobs {
		names = append(names, strings.TrimSpace(j.Title+" "+j.Target))
	}
	return fmt.Sprintf("%s has %d running job(s): %s — let them finish, or cancel them in the Jobs pane, then remove it",
		e.Member.Name(), len(e.Jobs), strings.Join(names, "; "))
}

// Member is one daemon in the session.
type Member struct {
	id       int
	endpoint dockerctx.Endpoint
	link     dockerctx.Link
	key      string
	launch   bool

	mu       sync.Mutex
	eng      engine.Engine
	hub      *live.Hub
	info     engine.Info
	problem  func() string
	closer   func()
	reason   string // why it is not connected, while it has no engine
	tries    int    // failed attempts to reach it
	started  bool   // the hub's streams were started
	gen      uint64 // changes whenever eng and hub do
	removing bool

	cancel context.CancelFunc // ends the connect loop
	done   chan struct{}      // closed when the connect loop has ended (nil for the launch member)
	closed chan struct{}      // closed once everything the member held is released

	// Notice bookkeeping, used only by TakeNotices (under Session.noticeMu).
	nInit      bool
	nUp        bool
	nSeen      uint64
	nReason    string
	nPending   []string // containers that exited, not yet announced
	nLastShown time.Time
}

// ID identifies the member within its session.
func (m *Member) ID() int { return m.id }

// Endpoint is the daemon as the operator named it.
func (m *Member) Endpoint() dockerctx.Endpoint { return m.endpoint }

// Link describes how the daemon is reached.
func (m *Member) Link() dockerctx.Link { return m.link }

// Key is the member's canonical endpoint (Canonical).
func (m *Member) Key() string { return m.key }

// Launch reports whether this is the daemon the program was started on.
func (m *Member) Launch() bool { return m.launch }

// Name is how the member is called in the strip, the picker and notices: its
// context's name; or, for an endpoint named directly (the "default" context),
// a short form of it — "local" for the default socket, the socket path for
// another one on this machine, [user@]host for ssh, host:port for tcp. The
// endpoint itself is always shown in full beside it where it matters: in the
// header, the picker and every confirmation.
func (m *Member) Name() string {
	if c := m.endpoint.Context; c != "" && c != dockerctx.DefaultContext {
		return c
	}
	t, err := m.endpoint.Target()
	if err != nil {
		return m.endpoint.Host
	}
	switch t.Transport {
	case dockerctx.TransportUnix:
		if m.endpoint.Host == dockerctx.DefaultHost {
			return "local"
		}
		return t.Socket
	case dockerctx.TransportSSH:
		if t.User != "" {
			return t.User + "@" + t.Hostname
		}
		return t.Hostname
	}
	return t.Address()
}

// Daemon is the name its jobs are recorded under: the endpoint as named.
func (m *Member) Daemon() string { return m.endpoint.Host }

// Engine is the member's engine; nil until it has first answered.
func (m *Member) Engine() engine.Engine {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eng
}

// Hub is the member's live-stream hub; nil until it has first answered.
func (m *Member) Hub() *live.Hub {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hub
}

// Info is what the daemon said about itself when it last answered.
func (m *Member) Info() engine.Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

// SetInfo records a newer answer to the info request.
func (m *Member) SetInfo(info engine.Info) {
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
}

// Gen changes whenever the member's engine and hub are replaced: a view
// holding the old ones must take the new.
func (m *Member) Gen() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen
}

// State is the member's current state.
func (m *Member) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.removing:
		return Removing
	case m.hub == nil && m.reason == "":
		return Connecting
	case m.hub == nil:
		return Disconnected
	case m.hub.Connected():
		return Connected
	}
	return Disconnected
}

// Reason says why the member is not connected; "" when it is, or when the
// first attempt is still under way. For a member that did connect once and
// lost the daemon, it is the transport's own report, if it has one.
func (m *Member) Reason() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hub == nil {
		return m.reason
	}
	if m.hub.Connected() {
		return ""
	}
	if m.problem != nil {
		if p := m.problem(); p != "" {
			return p
		}
	}
	return "the event stream ended; retrying"
}

// Problem is what the transport underneath the engine — the SSH tunnel —
// says about itself, or "" while it is up (or there is none).
func (m *Member) Problem() string {
	m.mu.Lock()
	p := m.problem
	m.mu.Unlock()
	if p == nil {
		return ""
	}
	return p()
}

// Attempts is the number of failed attempts to reach a member that has not
// answered yet.
func (m *Member) Attempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tries
}

// Closed is closed once a removed member has released its streams, its
// engine and any ssh process started for it.
func (m *Member) Closed() <-chan struct{} { return m.closed }

// release ends everything the member holds. It is called once, after the
// member has left the session.
func (m *Member) release() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		<-m.done
	}
	m.mu.Lock()
	hub, closer := m.hub, m.closer
	m.mu.Unlock()
	if hub != nil {
		hub.Close()
	}
	if closer != nil {
		closer()
	}
	close(m.closed)
}

// Config is what a session is built with.
type Config struct {
	// ReadOnly is the session's single read-only flag. It applies to every
	// member Add connects (the launch member is built read-only by its
	// caller, from the same flag).
	ReadOnly bool
	// Jobs is the job manager; removal checks it for running jobs.
	Jobs *jobs.Manager
	// Dial reaches a picked daemon. The default is Connect with
	// DialOptions (ReadOnly is forced to the session's flag either way).
	Dial func(ctx context.Context, ep dockerctx.Endpoint, o DialOptions) (*Conn, error)
	// DialOptions are passed to Dial.
	DialOptions DialOptions
	// SetupHub, when set, adjusts each new hub before it starts (timeouts
	// for a daemon over a network; shorter backoffs in tests).
	SetupHub func(*live.Hub, dockerctx.Link)
	// RetryMin and RetryMax bound the wait between attempts to reach a
	// member that has not answered yet (defaults 1 s and 15 s).
	RetryMin, RetryMax time.Duration
	// NoticeGap is the shortest time between two container-exit notices
	// from one background member (default 2 s); exits in between are
	// collected into the next one.
	NoticeGap time.Duration
}

// Session is the set of members.
type Session struct {
	cfg Config

	mu      sync.Mutex
	members []*Member
	focus   *Member
	nextID  int
	started bool
	closed  bool
	wg      sync.WaitGroup

	noticeMu sync.Mutex
}

// New returns an empty session.
func New(cfg Config) *Session {
	if cfg.Jobs == nil {
		cfg.Jobs = jobs.NewManager()
	}
	if cfg.Dial == nil {
		cfg.Dial = Connect
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = time.Second
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 15 * time.Second
	}
	if cfg.NoticeGap <= 0 {
		cfg.NoticeGap = 2 * time.Second
	}
	cfg.DialOptions.ReadOnly = cfg.ReadOnly
	return &Session{cfg: cfg}
}

// ReadOnly reports the session's read-only flag.
func (s *Session) ReadOnly() bool { return s.cfg.ReadOnly }

// Jobs is the job manager the session checks removals against.
func (s *Session) Jobs() *jobs.Manager { return s.cfg.Jobs }

func (s *Session) newHub(eng engine.Engine, link dockerctx.Link) *live.Hub {
	h := live.NewHub(eng)
	if s.cfg.SetupHub != nil {
		s.cfg.SetupHub(h, link)
	}
	return h
}

// AddLaunch adds the daemon the program was started on, which has already
// answered: its engine, what it said, the transport's report on itself (may
// be nil) and what releases engine and transport when it leaves the session
// (may be nil). It becomes the focused member when nothing is focused. Its
// hub starts with Start.
func (s *Session) AddLaunch(ep dockerctx.Endpoint, eng engine.Engine, info engine.Info, problem func() string, closer func()) *Member {
	key, err := Canonical(ep)
	if err != nil {
		key = "unparsed:" + ep.Host // refused long before this, at launch
	}
	m := &Member{
		endpoint: ep, link: ep.Link(), key: key, launch: true,
		eng: eng, info: info, problem: problem, closer: closer,
		closed: make(chan struct{}),
	}
	m.hub = s.newHub(eng, m.link)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	m.id = s.nextID
	s.members = append(s.members, m)
	s.cfg.Jobs.OpenDaemon(m.Daemon())
	if s.focus == nil {
		s.focus = m
	} else {
		m.hub.SetMode(live.ModeEventsOnly)
	}
	if s.started {
		m.hub.Start()
		m.started = true
	}
	return m
}

// Start starts the streams of every member that has a hub. Until then
// nothing is contacted.
func (s *Session) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	for _, m := range s.members {
		m.mu.Lock()
		if m.hub != nil && !m.started {
			m.hub.Start()
			m.started = true
		}
		m.mu.Unlock()
	}
}

// Find returns the member already in the session for ep's daemon, if any.
func (s *Session) Find(ep dockerctx.Endpoint) *Member {
	key, err := Canonical(ep)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		if m.key == key {
			return m
		}
	}
	return nil
}

// Add adds the daemon at ep and starts connecting to it in the background.
// When ep reaches a daemon that is already a member — by any context name,
// any spelling — that member is returned with existing set, and nothing is
// added. The new member is not focused, unless nothing was.
func (s *Session) Add(ep dockerctx.Endpoint) (m *Member, existing bool, err error) {
	key, err := Canonical(ep)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, ErrClosed
	}
	for _, old := range s.members {
		if old.key == key {
			return old, true, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.nextID++
	m = &Member{
		id: s.nextID, endpoint: ep, link: ep.Link(), key: key,
		cancel: cancel, done: make(chan struct{}), closed: make(chan struct{}),
	}
	s.members = append(s.members, m)
	// A daemon removed earlier and added back may run jobs again.
	s.cfg.Jobs.OpenDaemon(m.Daemon())
	s.wg.Add(1)
	go s.connect(ctx, m)
	return m, false, nil
}

// connect tries to reach a picked member until it answers, it is removed, or
// the session closes.
func (s *Session) connect(ctx context.Context, m *Member) {
	defer s.wg.Done()
	defer close(m.done)
	backoff := s.cfg.RetryMin
	for {
		conn, err := s.cfg.Dial(ctx, m.endpoint, s.cfg.DialOptions)
		if ctx.Err() != nil {
			conn.Close()
			return
		}
		if err == nil {
			hub := s.newHub(conn.Engine, m.link)
			s.mu.Lock()
			if m.removing || s.closed {
				s.mu.Unlock()
				conn.Close()
				return
			}
			if s.focus == nil {
				// Nothing was focused — the session was empty, or no
				// other member had answered: this one is.
				s.focus = m
			} else if s.focus != m {
				hub.SetMode(live.ModeEventsOnly)
			}
			m.mu.Lock()
			m.eng, m.hub, m.info = conn.Engine, hub, conn.Info
			m.problem, m.closer = conn.Problem, conn.Close
			m.reason = ""
			m.gen++
			if s.started {
				hub.Start()
				m.started = true
			}
			m.mu.Unlock()
			s.mu.Unlock()
			return
		}
		reason := err.Error()
		var u *dockerctx.Unreachable
		if errors.As(err, &u) && u.Reason != "" {
			reason = u.Reason
		}
		m.mu.Lock()
		m.reason = reason
		m.tries++
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > s.cfg.RetryMax {
			backoff = s.cfg.RetryMax
		}
	}
}

// Members returns the members in order.
func (s *Session) Members() []*Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Member(nil), s.members...)
}

// Len is the number of members.
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members)
}

// Get returns the member with the given ID, or nil.
func (s *Session) Get(id int) *Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findLocked(id)
}

func (s *Session) findLocked(id int) *Member {
	for _, m := range s.members {
		if m.id == id {
			return m
		}
	}
	return nil
}

// Focused is the focused member; nil when no member has answered yet, or the
// session is empty. It is always a member of the session.
func (s *Session) Focused() *Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.focus
}

// Focus moves focus to the member with the given ID, which must be
// connected. The previous member's statistics and log streams close; its
// event stream stays.
func (s *Session) Focus(id int) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.findLocked(id)
	if m == nil {
		return nil, ErrNoMember
	}
	if st := m.State(); st != Connected {
		if r := m.Reason(); r != "" {
			return nil, fmt.Errorf("%w (%s): %s", ErrNotConnected, st, r)
		}
		return nil, fmt.Errorf("%w (%s)", ErrNotConnected, st)
	}
	s.setFocusLocked(m)
	return m, nil
}

// Next moves focus delta places through the members in order — wrapping,
// and passing over members that are not connected — and returns the newly
// focused member. With no other connected member it changes nothing and
// returns ErrNotConnected.
func (s *Session) Next(delta int) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.members)
	if n == 0 {
		return nil, ErrNoMember
	}
	at := -1
	for i, m := range s.members {
		if m == s.focus {
			at = i
		}
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	if at < 0 {
		at = 0
		if step < 0 {
			at = n - 1
		}
		if m := s.members[at]; m.State() == Connected && m != s.focus {
			s.setFocusLocked(m)
			return m, nil
		}
	}
	for i := 1; i < n+1; i++ {
		m := s.members[((at+step*i)%n+n)%n]
		if m == s.focus {
			continue
		}
		if m.State() == Connected {
			s.setFocusLocked(m)
			return m, nil
		}
	}
	return nil, ErrNotConnected
}

func (s *Session) setFocusLocked(m *Member) {
	if s.focus == m {
		return
	}
	if old := s.focus; old != nil {
		if h := old.Hub(); h != nil {
			h.SetMode(live.ModeEventsOnly)
		}
	}
	s.focus = m
	if m != nil {
		if h := m.Hub(); h != nil {
			h.SetMode(live.ModeFull)
		}
	}
}

// RunningJobs returns the jobs running on the member's daemon.
func (s *Session) RunningJobs(m *Member) []jobs.Job { return s.cfg.Jobs.RunningOn(m.Daemon()) }

// Remove takes the member with the given ID out of the session. It is
// refused with a *RefusedError, naming them, while jobs run on its daemon;
// the check and the mark that stops new jobs from starting there are made
// under the job manager's lock (jobs.Manager.CloseDaemon). When the focused
// member leaves, focus moves to the next connected member — returned as
// focus — or, failing that, to one that has at least answered once, or to
// nothing.
//
// The member's streams, its engine and any ssh process started for it are
// released in the background; Member.Closed says when that is done, and
// Close waits for it.
func (s *Session) Remove(id int) (focus *Member, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.findLocked(id)
	if m == nil {
		return nil, ErrNoMember
	}
	if running := s.cfg.Jobs.CloseDaemon(m.Daemon()); len(running) > 0 {
		return nil, &RefusedError{Member: m, Jobs: running}
	}
	m.mu.Lock()
	m.removing = true
	m.mu.Unlock()
	at := 0
	for i, x := range s.members {
		if x == m {
			at = i
		}
	}
	s.members = append(s.members[:at:at], s.members[at+1:]...)
	if s.focus == m {
		s.focus = nil // its hub is closing; no mode to switch
		s.setFocusLocked(s.successorLocked(at))
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		m.release()
	}()
	return s.focus, nil
}

// successorLocked picks who is focused after the member at index at left:
// the next connected member in order (wrapping), else the next that has an
// engine at all, else nobody.
func (s *Session) successorLocked(at int) *Member {
	n := len(s.members)
	for _, want := range []func(*Member) bool{
		func(m *Member) bool { return m.State() == Connected },
		func(m *Member) bool { return m.Hub() != nil },
	} {
		for i := 0; i < n; i++ {
			if m := s.members[(at+i)%n]; want(m) {
				return m
			}
		}
	}
	return nil
}

// Close removes every member — whatever runs — and waits until each has
// released its streams, engine and ssh process.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	ms := s.members
	s.members, s.focus = nil, nil
	for _, m := range ms {
		m.mu.Lock()
		m.removing = true
		m.mu.Unlock()
		s.wg.Add(1)
		go func(m *Member) {
			defer s.wg.Done()
			m.release()
		}(m)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// ---- notices ----------------------------------------------------------------------

// Notice is one thing a background member has to say in the status line.
type Notice struct {
	Member *Member
	Text   string // begins with the member's name
	Bad    bool
}

// TakeNotices returns what happened on members other than the focused one
// since the last call: a member that answered for the first time or could not
// be reached, a connection lost or regained, and containers that exited —
// the die and oom events, and nothing else (D6). Exits are rate-limited per
// member (Config.NoticeGap): those within the gap are collected into the next
// notice. The focused member's own news is the panes' business; it is passed
// over, and its events are never announced later either.
func (s *Session) TakeNotices(now time.Time) []Notice {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	s.mu.Lock()
	ms := append([]*Member(nil), s.members...)
	focus := s.focus
	s.mu.Unlock()

	var out []Notice
	for _, m := range ms {
		hub := m.Hub()
		if hub == nil {
			// Not reached yet: say why, once per distinct reason.
			if r := m.Reason(); r != "" && r != m.nReason {
				m.nReason = r
				out = append(out, Notice{Member: m, Bad: true, Text: m.Name() + ": not reachable — " + r + " (retrying)"})
			}
			continue
		}
		up := hub.Connected()
		_, end := hub.Events.Bounds()
		if !m.nInit {
			m.nInit, m.nUp, m.nSeen = true, up, end
			if !m.launch && m != focus {
				info := m.Info()
				out = append(out, Notice{Member: m, Text: m.Name() + ": connected (Engine " + info.ServerVersion + " on " + info.Name + ") — > or M to focus it"})
			}
			continue
		}
		if m == focus {
			m.nUp, m.nSeen, m.nPending = up, end, nil
			continue
		}
		if up != m.nUp {
			m.nUp = up
			if up {
				out = append(out, Notice{Member: m, Text: m.Name() + ": reconnected"})
			} else {
				text := m.Name() + ": lost the connection — retrying"
				if p := m.Problem(); p != "" {
					text += ": " + p
				}
				out = append(out, Notice{Member: m, Bad: true, Text: text})
			}
		}
		if end > m.nSeen {
			entries, _ := hub.Events.Read(m.nSeen, end)
			m.nSeen = end
			for _, e := range entries {
				if e.Marker != "" || e.Event.Type != "container" || (e.Event.Action != "die" && e.Event.Action != "oom") {
					continue
				}
				what := e.Event.Name()
				if e.Event.Action == "oom" {
					what += " ran out of memory"
				} else if code := e.Event.Attributes["exitCode"]; code != "" {
					what += " exited (code " + code + ")"
				} else {
					what += " exited"
				}
				m.nPending = append(m.nPending, what)
			}
		}
		if len(m.nPending) > 0 && now.Sub(m.nLastShown) >= s.cfg.NoticeGap {
			text := m.Name() + ": container " + m.nPending[0]
			if more := len(m.nPending) - 1; more > 0 {
				text += fmt.Sprintf(", and %d more container event(s)", more)
			}
			out = append(out, Notice{Member: m, Bad: true, Text: text})
			m.nPending, m.nLastShown = nil, now
		}
	}
	return out
}
