// Package ui is the desk: one full-screen workspace of five panes.
//
// The model is a plain state machine in the Bubble Tea style. Two rules shape
// it (docs/DECISIONS.md, D8 and D11):
//
//   - Live data never arrives as messages. Logs, statistics, events and job
//     progress sit in bounded buffers owned by other packages; the frame tick
//     looks at their version counters and redraws only when one moved. Keys
//     are the only high-priority traffic on the message queue.
//
//   - No key handler sends a mutation. A handler can at most ask for a Plan,
//     which opens a dialog; only completing that dialog starts a job, and the
//     job executes the Plan the dialog displayed.
package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/plan"
)

// Pane identifies one of the five panes.
type Pane int

const (
	PaneKinds Pane = iota
	PaneList
	PaneDetails
	PaneEvents
	PaneJobs
	paneCount
)

var paneNames = [paneCount]string{"Kinds", "List", "Details", "Events", "Jobs"}

// Kind is a kind of Docker object.
type Kind int

const (
	KindContainers Kind = iota
	KindImages
	KindVolumes
	KindNetworks
	KindCompose
	kindCount
)

var kindNames = [kindCount]string{"Containers", "Images", "Volumes", "Networks", "Compose"}

// The four lists fetched from the daemon. Compose projects are derived from
// the container list.
const (
	listContainers = iota
	listImages
	listVolumes
	listNetworks
	listCount
)

var listDirtyBit = [listCount]uint32{live.DirtyContainers, live.DirtyImages, live.DirtyVolumes, live.DirtyNetworks}

// Minimum terminal size the panes are laid out for.
const (
	MinWidth  = 80
	MinHeight = 24
)

// Options configures a Model.
type Options struct {
	Engine   engine.Engine
	Endpoint dockerctx.Endpoint
	Info     engine.Info
	ReadOnly bool
	// Theme is the palette. The zero value picks dark or light from the
	// terminal's background.
	Theme Theme
	// LinkProblem, when set, reports why the transport underneath the
	// engine — the SSH tunnel — is down, or "" while it is up. The desk
	// shows it next to "disconnected", which by itself cannot tell a daemon
	// that stopped from a tunnel that fell.
	LinkProblem func() string

	// Timing; zero values select the defaults. Tests shorten them.
	FrameInterval time.Duration // how often live data is looked at (50 ms)
	Debounce      time.Duration // minimum gap between refreshes of one list (150 ms)
	PollInterval  time.Duration // periodic refresh of everything (5 s)
	SelectDelay   time.Duration // settle time before loading details of a new highlight (100 ms)
	// ArmDelay is how long a confirmation, picker or shell form must have
	// been open before it accepts the input that proceeds (250 ms). Keys
	// typed ahead of a dialog cannot consent to it. Negative disables.
	ArmDelay time.Duration
	// JumpWait is how long a jump from the Events pane waits for its
	// object to appear in a list that has not been refreshed yet (2 s).
	JumpWait time.Duration
	// FindingsAskAbove is the image size above which the Findings tab asks
	// before analysing an image on a remote daemon (500 MB). Negative
	// never asks.
	FindingsAskAbove int64

	// NoRegistry forbids every registry request: --no-registry.
	NoRegistry bool
	// DockerConfigDir is where registry credentials are read from
	// ($DOCKER_CONFIG, or ~/.docker); "" for none.
	DockerConfigDir string
	// Upstream, when set, is the checker the Upstream tab uses instead of
	// one built from the fields above (tests).
	Upstream *freshness.Checker

	// Version is the program's version, written into exported documents.
	Version string
	// HomeDir is what ~ means in an export path, and WorkDir what a
	// relative one is taken from; "" asks the operating system.
	HomeDir, WorkDir string
	// Now is the clock that dates an export's prefilled file name; nil is
	// time.Now.
	Now func() time.Time
}

// Model is the whole desk.
type Model struct {
	eng      engine.Engine
	hub      *live.Hub
	jobs     *jobs.Manager
	endpoint dockerctx.Endpoint
	link     dockerctx.Link
	info     engine.Info
	readOnly bool
	st       styles
	opts     Options

	width, height int
	focus         Pane
	kind          Kind

	// ---- data ----
	snap        plan.Snapshot
	projects    []plan.Project
	loaded      [listCount]bool
	listErr     [listCount]string
	inflight    [listCount]bool
	reqSeq      [listCount]uint64
	lastStart   [listCount]time.Time
	pending     uint32 // lists waiting for a refresh
	lastPoll    time.Time
	dataVersion uint64

	// ---- list pane ----
	rows       [kindCount][]row
	rowsFor    [kindCount]uint64 // dataVersion the rows were built from
	sel        [kindCount]selection
	filter     [kindCount]string
	filtering  bool
	filterPrev string
	filterIn   textinput.Model
	listHeight int // rows visible, for paging
	// marks is each kind's selection: the objects an action applies to
	// instead of the highlighted one. Keyed by row key (object identity),
	// so a mark follows its object across refreshes and goes with it.
	marks [kindCount]map[string]mark

	// ---- details pane ----
	tab          [kindCount]int
	detail       detailState
	wantDetail   string
	selChangedAt time.Time
	info1        textView     // Info tab
	inspect      textView     // Inspect tab
	proc         procState    // Processes tab
	chg          changesState // Changes tab
	logFollow    bool
	logEnd       uint64
	logWrap      bool
	detailHeight int
	ds           detailSearch // the Details pane's search bar (search.go)

	// ---- findings tab (Images) ----
	fx findingsState
	// up is the Upstream tab and the list's UPSTREAM column.
	up upstreamState

	// ---- packages view (Images › Findings, then i) ----
	pk pkgView

	// ---- space view ----
	sp spaceState
	// ---- repository browser (B) ----
	rb repoState
	// changedAt is when a refresh last found the engine different from what
	// the desk knew: a list, or the disk account. A figure taken before it
	// is stale. listPrint is what each list said, for comparison.
	changedAt time.Time
	listPrint [listCount]string

	// ---- events pane ----
	// While held, the pane shows evSnap — a copy of the ring taken when the
	// hold began — and a cursor on it; the ring keeps filling underneath.
	evHeld      bool
	evSnap      []live.EventEntry
	evSnapEnd   uint64 // the ring's end when the snapshot was taken
	evCur       int    // cursor, an index into evSnap
	evOff       int    // first snapshot row drawn
	evJump      *eventJump
	eventHeight int

	// ---- jobs pane ----
	jobSel    int // ID of the highlighted job, 0 = newest
	jobHeight int

	// ---- overlays ----
	dlg       *dialog
	preparing bool
	planSeq   uint64 // identifies the latest plan request; older answers are dropped
	status    statusLine
	spinner   int

	// ---- rendering ----
	dirty        bool
	frame        string
	drawn        drawnVersions
	frames       uint64 // number of frames actually rendered
	wasConnected bool
	linkProblem  string // the transport problem last reported in the status line
	quitting     bool
	started      bool
}

type selection struct {
	key string
	idx int
	off int
}

// mark is one selected object: the row as last built (names and IDs are
// refreshed with the list) and its position in the unfiltered list, which
// orders the set.
type mark struct {
	r   row
	pos int
}

type statusLine struct {
	text  string
	tone  tone
	until time.Time
}

type drawnVersions struct {
	events, logs, logGen, stats, jobs uint64
	connected                         bool
}

// New builds the desk. Nothing is contacted until Init runs.
func New(opts Options) *Model {
	if opts.FrameInterval <= 0 {
		opts.FrameInterval = 50 * time.Millisecond
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 150 * time.Millisecond
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.SelectDelay <= 0 {
		opts.SelectDelay = 100 * time.Millisecond
	}
	if opts.JumpWait <= 0 {
		opts.JumpWait = 2 * time.Second
	}
	if opts.ArmDelay == 0 {
		opts.ArmDelay = 250 * time.Millisecond
	}
	theme := opts.Theme
	if theme.Name == "" {
		theme, _ = ThemeByName(AutoTheme)
	}
	in := textinput.New()
	in.Prompt = "/"
	in.CharLimit = 80
	m := &Model{
		eng:          opts.Engine,
		hub:          live.NewHub(opts.Engine),
		jobs:         jobs.NewManager(),
		endpoint:     opts.Endpoint,
		link:         opts.Endpoint.Link(),
		info:         opts.Info,
		readOnly:     opts.ReadOnly || engine.IsReadOnly(opts.Engine),
		st:           newStyles(theme),
		opts:         opts,
		focus:        PaneList,
		kind:         KindContainers,
		filterIn:     in,
		logFollow:    true,
		logWrap:      true,
		dirty:        true,
		wasConnected: true,
	}
	m.info1.wrap = true
	m.newUpstream()
	m.inspect.wrap = true
	m.ds.in = textinput.New()
	m.ds.in.CharLimit = 80
	if m.link.Transport != dockerctx.TransportUnix {
		// "Is it back?" is asked with a short timeout. Over a network a
		// new connection can take longer than that to set up, and the desk
		// would then call a slow daemon a missing one for ever.
		m.hub.PingTimeout = 10 * time.Second
	}
	return m
}

// Hub exposes the live-stream hub (for tests).
func (m *Model) Hub() *live.Hub { return m.hub }

// Jobs exposes the job manager (for tests).
func (m *Model) Jobs() *jobs.Manager { return m.jobs }

// Close stops every stream and cancels running jobs. Requests the daemon has
// already accepted complete or fail on the daemon's side.
func (m *Model) Close() {
	m.hub.Close()
	m.jobs.Close()
}

// ---- messages ---------------------------------------------------------------------

type frameMsg time.Time

type listMsg struct {
	list       int
	seq        uint64
	containers []engine.Container
	images     []engine.Image
	volumes    []engine.Volume
	networks   []engine.Network
	err        error
}

type infoMsg struct {
	info engine.Info
	err  error
}

// Init starts the streams, loads every list and arms the frame tick.
func (m *Model) Init() tea.Cmd {
	if !m.started {
		m.started = true
		m.hub.Start()
	}
	m.lastPoll = time.Now()
	cmds := []tea.Cmd{m.frameTick()}
	for li := 0; li < listCount; li++ {
		cmds = append(cmds, m.refreshCmd(li))
	}
	return tea.Batch(cmds...)
}

func (m *Model) frameTick() tea.Cmd {
	return tea.Tick(m.opts.FrameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m *Model) refreshCmd(li int) tea.Cmd {
	m.inflight[li] = true
	m.reqSeq[li]++
	m.lastStart[li] = time.Now()
	m.pending &^= listDirtyBit[li]
	seq := m.reqSeq[li]
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		msg := listMsg{list: li, seq: seq}
		switch li {
		case listContainers:
			msg.containers, msg.err = eng.Containers(ctx)
		case listImages:
			msg.images, msg.err = eng.Images(ctx)
		case listVolumes:
			msg.volumes, msg.err = eng.Volumes(ctx)
		case listNetworks:
			msg.networks, msg.err = eng.Networks(ctx)
		}
		return msg
	}
}

func (m *Model) infoCmd() tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := eng.Info(ctx)
		return infoMsg{info: info, err: err}
	}
}

func (m *Model) setStatus(t tone, text string) {
	d := 4 * time.Second
	if t == toneBad || t == toneWarn {
		d = 12 * time.Second
	}
	m.status = statusLine{text: text, tone: t, until: time.Now().Add(d)}
	m.dirty = true
}

// Update is the state machine.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case frameMsg:
		return m, m.onFrame(time.Time(msg))

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.dirty = true
		return m, nil

	case tea.KeyMsg:
		m.dirty = true
		cmd := m.onKey(msg)
		m.afterChange()
		return m, cmd

	case listMsg:
		m.dirty = true
		cmd := m.onList(msg)
		m.afterChange()
		return m, cmd

	case infoMsg:
		if msg.err == nil {
			m.info = msg.info
			m.dirty = true
		}
		return m, nil

	case detailMsg:
		m.dirty = true
		m.onDetail(msg)
		return m, nil

	case procMsg:
		m.dirty = true
		m.onProcesses(msg)
		return m, nil

	case changesFactsMsg:
		m.dirty = true
		m.onChangesFacts(msg)
		return m, nil

	case factsMsg:
		m.dirty = true
		m.onFacts(msg)
		return m, nil

	case planMsg:
		m.dirty = true
		cmd := m.onPlan(msg)
		return m, cmd

	case exportDoneMsg:
		m.dirty = true
		m.onExportDone(msg)
		return m, nil

	case pickMsg:
		m.dirty = true
		m.onPick(msg)
		return m, nil

	case repoTagsMsg:
		m.dirty = true
		m.onRepoTags(msg)
		return m, nil

	case repoDescMsg:
		m.dirty = true
		m.onRepoDesc(msg)
		return m, nil

	case shellDoneMsg:
		m.dirty = true
		cmd := m.onShellDone(msg)
		return m, cmd
	}
	// Anything else belongs to the focused text input (cursor blink).
	if m.dlg == nil && m.rb.open && m.rb.editing {
		var cmd tea.Cmd
		m.rb.name, cmd = m.rb.name.Update(msg)
		m.dirty = true
		return m, cmd
	}
	if m.dlg != nil && m.dlg.hasInput {
		var cmd tea.Cmd
		m.dlg.input, cmd = m.dlg.input.Update(msg)
		m.dirty = true
		return m, cmd
	}
	return m, nil
}

// onFrame runs on every frame tick. It is the only place where live data is
// looked at, and it does a constant amount of work regardless of how much
// arrived.
func (m *Model) onFrame(now time.Time) tea.Cmd {
	cmds := []tea.Cmd{m.frameTick()}
	if m.quitting {
		return nil
	}

	// 1. Lists that engine events (or the periodic poll) made stale.
	m.pending |= m.hub.TakeDirty()
	if now.Sub(m.lastPoll) >= m.opts.PollInterval {
		m.lastPoll = now
		m.pending |= live.DirtyAll
	}
	for li := 0; li < listCount; li++ {
		if m.pending&listDirtyBit[li] != 0 && !m.inflight[li] && now.Sub(m.lastStart[li]) >= m.opts.Debounce {
			cmds = append(cmds, m.refreshCmd(li))
		}
	}

	// 2. Once the highlight has settled (so that holding ↓ does not open a
	// stream per row): load its details and point the log stream at it.
	if now.Sub(m.selChangedAt) >= m.opts.SelectDelay {
		if m.wantDetail != m.detail.key {
			if cmd := m.loadDetail(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		m.syncStreams() // a no-op unless the wanted target changed
		if cmd := m.syncProcesses(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.syncChanges(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.syncFindings(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.syncUpstream()
	} else {
		m.collectFindings()
		m.collectUpstream()
	}

	// A jump from the Events pane still waiting for its row.
	m.expireEventJump(now)

	// The Space view's measurements, and its ages, which move each second.
	m.collectSpace()
	if m.sp.open {
		if sec := now.Unix(); sec != m.sp.drawnSec {
			m.sp.drawnSec = sec
			m.dirty = true
		}
	}

	// The repository browser: its build-time job, and the row the
	// highlight rests on.
	if cmd := m.syncRepository(now); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// 3. Jobs that finished since the last frame.
	if fin := m.jobs.TakeFinished(); len(fin) > 0 {
		m.announce(fin)
		m.pending |= live.DirtyAll
	}

	// 4. Connection state.
	connected := m.hub.Connected()
	if connected && !m.wasConnected {
		m.setStatus(toneGood, "Reconnected to the Docker daemon.")
		m.invalidateProcesses() // a stale snapshot is taken again if shown
		cmds = append(cmds, m.infoCmd())
	} else if !connected && m.wasConnected {
		m.setStatus(toneBad, "Lost the connection to the Docker daemon at "+m.endpoint.Host+" — retrying. Data on screen may be stale.")
	}
	m.wasConnected = connected
	if m.opts.LinkProblem != nil {
		// A fallen tunnel explains itself a moment after the streams it
		// carried have failed; say so once per distinct reason.
		problem := ""
		if !connected {
			problem = m.opts.LinkProblem()
		}
		if problem != "" && problem != m.linkProblem {
			m.setStatus(toneBad, "The "+m.link.Transport.String()+" link to "+m.endpoint.Host+" is down — retrying: "+problem)
		}
		m.linkProblem = problem
	}

	// 5. Did anything visible change?
	v := drawnVersions{
		events:    m.hub.Events.Version(),
		stats:     m.hub.StatsVersion(),
		jobs:      m.jobs.Version(),
		connected: connected,
	}
	if m.logsVisible() {
		ring, _ := m.hub.Log()
		v.logs = ring.Version()
		v.logGen = m.hub.LogGeneration()
	}
	if v != m.drawn {
		m.drawn = v
		m.dirty = true
	}
	if m.status.text != "" && now.After(m.status.until) {
		m.status = statusLine{}
		m.dirty = true
	}
	if m.jobs.RunningCount() > 0 || m.preparing || m.repoBusy() {
		// Spinner and elapsed times move only while something runs.
		if next := int(now.UnixMilli()/120) % len(spinnerFrames); next != m.spinner {
			m.spinner = next
			m.dirty = true
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) announce(fin []jobs.Job) {
	var failed *jobs.Job
	for i := range fin {
		if fin[i].State == jobs.Failed {
			failed = &fin[i]
		}
	}
	if failed != nil {
		m.setStatus(toneBad, "✗ "+failed.Title+" "+failed.Target+": "+failed.Err)
		return
	}
	last := fin[len(fin)-1]
	switch last.State {
	case jobs.Succeeded:
		m.setStatus(toneGood, "✓ "+last.Title+" "+last.Target)
	case jobs.Cancelled:
		m.setStatus(toneWarn, "cancelled: "+last.Title+" "+last.Target)
	}
}

func (m *Model) onList(msg listMsg) tea.Cmd {
	li := msg.list
	if msg.seq != m.reqSeq[li] {
		return nil // an older response that arrived late
	}
	m.inflight[li] = false
	if msg.err != nil {
		m.listErr[li] = errText(msg.err)
		return nil
	}
	m.listErr[li] = ""
	m.loaded[li] = true
	if p := listFingerprint(msg); p != m.listPrint[li] {
		m.listPrint[li] = p
		m.changedAt = time.Now()
	}
	switch li {
	case listContainers:
		m.snap.Containers = msg.containers
		m.projects = plan.Projects(m.snap)
	case listImages:
		m.snap.Images = msg.images
	case listVolumes:
		m.snap.Volumes = msg.volumes
	case listNetworks:
		m.snap.Networks = msg.networks
	}
	m.dataVersion++
	// The highlighted object may have changed state: reload its details.
	if m.detail.key != "" && m.detail.key == m.wantDetail && !m.detail.loading {
		return m.loadDetail()
	}
	return nil
}

// afterChange reconciles derived state after anything that may have changed
// the data, the kind, the highlight or the tab.
func (m *Model) afterChange() {
	m.ensureRows(m.kind)
	m.retryEventJump()
	key := m.currentDetailKey()
	if key != m.wantDetail {
		m.wantDetail = key
		m.selChangedAt = time.Now()
		if m.detail.key == "" {
			m.selChangedAt = time.Time{} // first load: no need to wait
		}
		m.info1.off, m.inspect.off = 0, 0
		m.clearSearches() // a query belongs to the object it was typed for
		if m.proc.target != m.currentContainerID() {
			m.clearProcesses("") // never leave one container's rows under another
		}
		m.logFollow = true
	}
	if !m.findingsVisible() || m.focus != PaneDetails {
		m.fx.revealed = "" // a revealed value is never left behind, off screen
	}
	m.syncStats()
}

// syncStats keeps one statistics stream per running container (capped),
// always including the highlighted one.
func (m *Model) syncStats() {
	if !m.loaded[listContainers] {
		return
	}
	running := make([]string, 0, len(m.snap.Containers))
	for _, c := range m.snap.Containers {
		if c.State == engine.StateRunning {
			running = append(running, c.ID)
		}
	}
	selected := ""
	if m.kind == KindContainers {
		if r, ok := m.currentRow(); ok {
			selected = r.containerID
		}
	}
	m.hub.SyncStats(running, selected)
}

// syncStreams opens the log stream the Details pane needs and closes the one
// it no longer needs.
func (m *Model) syncStreams() {
	target := ""
	if m.logsVisible() {
		if r, ok := m.currentRow(); ok {
			target = r.containerID
		}
	}
	m.hub.SetLogTarget(target)
}

func (m *Model) logsVisible() bool {
	return m.kind == KindContainers && m.tabName() == tabLogs
}

// errText is the daemon's own message, without the operation prefix the
// engine layer adds: the pane it is shown in already says what failed.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return plainError(err)
}

// listFingerprint is what a list says that the disk account depends on:
// which objects exist and, for containers, their state. A refresh that finds
// the same fingerprint has changed nothing that a figure could be stale for.
func listFingerprint(msg listMsg) string {
	var parts []string
	switch msg.list {
	case listContainers:
		for _, c := range msg.containers {
			parts = append(parts, c.ID+"="+c.State)
		}
	case listImages:
		for _, i := range msg.images {
			parts = append(parts, i.ID+"="+strings.Join(i.RepoTags, ","))
		}
	case listVolumes:
		for _, v := range msg.volumes {
			parts = append(parts, v.Name)
		}
	case listNetworks:
		return "" // networks hold no bytes
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
