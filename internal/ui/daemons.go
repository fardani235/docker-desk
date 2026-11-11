package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/session"
)

// The session's daemons in the desk: which one the panes show, the strip that
// lists them all, the picker that adds and removes them, and the notices the
// unfocused ones send to the status line. See internal/session for the rules
// themselves, and docs/DECISIONS.md, M1–M15.

// Keys of the daemon session. None of them is used by any pane, view or the
// global key switch (TestDaemonKeysDoNotConflict).
const (
	keyDaemons   = "M" // the picker
	keyNextFocus = ">" // focus the next connected daemon
	keyPrevFocus = "<" // … the previous one
)

// daemonsState is what the desk keeps about the session, across focus changes.
type daemonsState struct {
	// idle stands in for a hub while no daemon is focused: it is never
	// started and has no engine, so nothing reads from it but the empty
	// Events ring.
	idle *live.Hub
	// sig is what the strip last showed; a change redraws.
	sig string
}

// daemonMsg is an answer to a command, tagged with the focus generation it
// was asked under.
type daemonMsg struct {
	gen uint64
	msg tea.Msg
}

// tagCmd tags the answers cmd brings back with gen. Only the desk's own
// answers — lists, details, plans and the rest — are tagged; the runtime's
// messages (quit, the shell's terminal hand-off, frame ticks) pass as they
// are, and a batch has each of its commands tagged.
func tagCmd(gen uint64, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch v := msg.(type) {
		case tea.BatchMsg:
			out := make(tea.BatchMsg, len(v))
			for i, c := range v {
				out[i] = tagCmd(gen, c)
			}
			return out
		case listMsg, infoMsg, detailMsg, procMsg, changesFactsMsg, factsMsg, planMsg,
			exportDoneMsg, filesChangesMsg, pickMsg, repoTagsMsg, repoDescMsg:
			return daemonMsg{gen: gen, msg: msg}
		}
		return msg
	}
}

// bind makes mem the daemon the panes show; nil leaves none.
func (m *Model) bind(mem *session.Member) {
	if mem != nil && mem.Hub() == nil {
		mem = nil // not answered yet: nothing to show of it
	}
	m.cur = mem
	if mem == nil {
		if m.ds2.idle == nil {
			m.ds2.idle = live.NewHub(nil)
			m.ds2.idle.SetMode(live.ModeEventsOnly)
			m.ds2.idle.MarkDown()
		}
		m.eng, m.hub, m.curGen = nil, m.ds2.idle, 0
		m.endpoint, m.link, m.info = dockerctx.Endpoint{}, dockerctx.Link{}, engine.Info{}
		return
	}
	m.curGen = mem.Gen()
	m.eng, m.hub = mem.Engine(), mem.Hub()
	m.endpoint, m.link, m.info = mem.Endpoint(), mem.Link(), mem.Info()
	m.wasConnected = m.hub.Connected()
	m.newUpstream()
	m.pending = live.DirtyAll
}

// switchTo points the desk at another member, or at none. Everything that
// describes the previous daemon — lists, highlight, selection, details,
// views, the held Events snapshot, any dialog but the picker — is dropped and
// rebuilt from nothing; what belongs to the operator or the session (the
// pane, the kind, each kind's tab and filter, the theme, the jobs, the status
// line) is kept. Answers still on their way from the previous daemon are
// dropped by their generation. The new daemon's lists are requested at once:
// the returned command.
func (m *Model) switchTo(mem *session.Member) tea.Cmd {
	m.dropRepoRequests()
	keep := *m
	dlg := keep.dlg
	if dlg != nil && dlg.kind != dlgDaemons {
		dlg = nil
	}
	keep.filterIn.Blur()
	*m = Model{
		jobs: keep.jobs, sess: keep.sess, ds2: keep.ds2, gen: keep.gen + 1,
		readOnly: keep.readOnly, st: keep.st, opts: keep.opts,
		width: keep.width, height: keep.height, focus: keep.focus, kind: keep.kind,
		filter: keep.filter, filterIn: keep.filterIn, tab: keep.tab, logWrap: keep.logWrap,
		jobSel: keep.jobSel, dlg: dlg, status: keep.status, spinner: keep.spinner,
		frames: keep.frames, started: keep.started, quitting: keep.quitting, lastPoll: keep.lastPoll,
	}
	m.initDaemonView()
	m.bind(mem)
	if m.cur == nil {
		return nil
	}
	return m.refreshAll()
}

// syncFocus follows the session: a focused member removed, one that answered
// while none was focused, or the focused one given a new engine.
func (m *Model) syncFocus(now time.Time) tea.Cmd {
	if sig := m.sessionSig(); sig != m.ds2.sig {
		m.ds2.sig = sig
		m.dirty = true
	}
	f := m.sess.Focused()
	if f == m.cur && (f == nil || f.Gen() == m.curGen) {
		return nil
	}
	prev := m.cur
	cmd := m.switchTo(f)
	switch {
	case m.cur != nil && prev == nil:
		m.setStatus(toneGood, "Focused "+m.cur.Name()+" ("+m.endpoint.Host+"), the first daemon to answer.")
	case m.cur == nil && m.sess.Len() == 0:
		m.setStatus(toneWarn, "No daemon is connected. "+keyDaemons+" opens the picker.")
	}
	return cmd
}

// sessionSig is what the strip and the picker depend on.
func (m *Model) sessionSig() string {
	var sb strings.Builder
	for _, mem := range m.sess.Members() {
		fmt.Fprintf(&sb, "%d:%s:%s|", mem.ID(), mem.State(), mem.Reason())
	}
	if f := m.sess.Focused(); f != nil {
		fmt.Fprintf(&sb, "f%d", f.ID())
	}
	return sb.String()
}

// collectNotices puts what the unfocused daemons have to say on the status
// line. Their events never enter the focused daemon's Events pane.
func (m *Model) collectNotices(now time.Time) {
	ns := m.sess.TakeNotices(now)
	if len(ns) == 0 {
		return
	}
	texts := make([]string, 0, len(ns))
	t := toneAccent
	for _, n := range ns {
		texts = append(texts, n.Text)
		if n.Bad {
			t = toneBad
		}
	}
	m.setStatus(t, strings.Join(texts, "  ·  "))
}

// focusMember moves focus to mem, at the operator's request.
func (m *Model) focusMember(id int) tea.Cmd {
	mem, err := m.sess.Focus(id)
	if err != nil {
		m.setStatus(toneWarn, "Cannot focus that daemon: "+err.Error())
		return nil
	}
	return m.focused(mem)
}

func (m *Model) focused(mem *session.Member) tea.Cmd {
	if mem == m.cur {
		return nil
	}
	cmd := m.switchTo(mem)
	m.setStatus(toneAccent, "Focused "+mem.Name()+" — "+mem.Endpoint().Host+". Loading its lists…")
	return cmd
}

// cycleFocus is > and <.
func (m *Model) cycleFocus(delta int) tea.Cmd {
	mem, err := m.sess.Next(delta)
	if err != nil {
		m.setStatus(toneDim, "No other connected daemon to focus. "+keyDaemons+" adds one.")
		return nil
	}
	return m.focused(mem)
}

// ---- jobs and their daemon ------------------------------------------------------

// startJob starts a job on the focused daemon, recording which one it is. A
// job cannot start on a daemon being removed; it is then recorded as failed,
// and fn never runs.
func (m *Model) startJob(title, target string, fn jobs.Func) int {
	daemon := ""
	if m.cur != nil {
		daemon = m.cur.Daemon()
	}
	id, err := m.jobs.StartOn(daemon, title, target, fn)
	if err != nil {
		m.setStatus(toneBad, "Not started: "+title+" "+target+" — "+err.Error())
	}
	return id
}

// daemonName is what a job's daemon is called: its member's name, or the
// endpoint once the member has left.
func (m *Model) daemonName(daemon string) string {
	for _, mem := range m.sess.Members() {
		if mem.Daemon() == daemon {
			return mem.Name()
		}
	}
	return daemon
}

// jobDaemon names a job's daemon wherever it could be mistaken: with more
// than one daemon in the session, or a job not of the focused one. With one
// daemon the header already says it, and nothing is added.
func (m *Model) jobDaemon(j jobs.Job) string {
	if j.Daemon == "" {
		return ""
	}
	if m.sess.Len() <= 1 && m.cur != nil && j.Daemon == m.cur.Daemon() {
		return ""
	}
	return m.daemonName(j.Daemon)
}

// onDaemon is jobDaemon as a phrase for the status line.
func (m *Model) onDaemon(j jobs.Job) string {
	if d := m.jobDaemon(j); d != "" {
		return " on " + d
	}
	return ""
}

// ---- the strip -------------------------------------------------------------------

// stripLabel is one member as the strip shows it: the focus marker, its
// place, its name, its state and — in a read-only session — "ro".
func (m *Model) stripLabel(i int, mem *session.Member, focused bool) (text string, t tone) {
	marker := " "
	if focused {
		marker = "◆"
	}
	// One glyph each, the same as in the header and the picker: ● connected,
	// ✗ disconnected, … connecting. The picker says it in words.
	glyph, t := "●", toneGood
	switch mem.State() {
	case session.Connecting:
		glyph, t = "…", toneDim
	case session.Disconnected:
		glyph, t = "✗", toneBad
	case session.Removing:
		glyph, t = "…", toneDim
	}
	if focused && glyph == "✗" {
		glyph = "✗ disconnected"
	}
	name := middleTruncate(mem.Name(), 22)
	text = fmt.Sprintf("%s%d %s %s", marker, i+1, name, glyph)
	if m.readOnly {
		text += " ro"
	}
	return text, t
}

// renderStrip is the second line: every member, the focused one marked, each
// one's state. When they do not all fit, the focused one stays and the
// strip says how many are hidden on either side.
func (m *Model) renderStrip() string {
	st := m.st
	members := m.sess.Members()
	focus := m.sess.Focused()
	head := st.headerKey.Render(" Daemons ")
	right := m.hints(keyDaemons, "picker", keyPrevFocus+" "+keyNextFocus, "focus") + " "
	if len(members) == 0 {
		line := head + st.tone(toneWarn, "none connected")
		return fitPair(line, right, m.width)
	}
	type label struct {
		text string
		t    tone
	}
	labels := make([]label, len(members))
	at := 0
	for i, mem := range members {
		text, t := m.stripLabel(i, mem, mem == focus)
		if mem == focus {
			at = i
			text = "[" + text + "]"
		}
		labels[i] = label{text, t}
	}
	sep := st.tone(toneDim, " │ ")
	room := m.width - ansi.StringWidth(head) - ansi.StringWidth(right) - 1
	width := func(lo, hi int) int {
		w := 0
		for i := lo; i <= hi; i++ {
			w += ansi.StringWidth(labels[i].text)
			if i > lo {
				w += 3
			}
		}
		if lo > 0 {
			w += len(fmt.Sprintf("‹%d more ", lo))
		}
		if hi < len(labels)-1 {
			w += len(fmt.Sprintf(" %d more›", len(labels)-1-hi))
		}
		return w
	}
	window := func(room int) (lo, hi int) {
		lo, hi = at, at
		for grew := true; grew; {
			grew = false
			if hi+1 < len(labels) && width(lo, hi+1) <= room {
				hi++
				grew = true
			}
			if lo > 0 && width(lo-1, hi) <= room {
				lo--
				grew = true
			}
		}
		return lo, hi
	}
	lo, hi := window(room)
	if lo > 0 || hi < len(labels)-1 {
		// Not all of them fit: the members matter more than the reminder
		// of the keys, which is in the help and the hints.
		right = ""
		room = m.width - ansi.StringWidth(head) - 1
		lo, hi = window(room)
	}
	var sb strings.Builder
	sb.WriteString(head)
	if lo > 0 {
		sb.WriteString(st.tone(toneDim, fmt.Sprintf("‹%d more ", lo)))
	}
	for i := lo; i <= hi; i++ {
		if i > lo {
			sb.WriteString(sep)
		}
		text := labels[i].text
		if i == at && ansi.StringWidth(text) > room {
			text = fit(text, max(room, 8))
		}
		if i == at && focus != nil {
			sb.WriteString(st.dialogTitle.Render(text))
		} else {
			sb.WriteString(st.tone(labels[i].t, text))
		}
	}
	if hi < len(labels)-1 {
		sb.WriteString(st.tone(toneDim, fmt.Sprintf(" %d more›", len(labels)-1-hi)))
	}
	return fitPair(sb.String(), right, m.width)
}

// fitPair puts left and right on one line of width w; right gives way first.
func fitPair(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if lw+rw+1 > w {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

// ---- the empty state ---------------------------------------------------------------

// emptyBody is what the panes' place holds when no daemon is connected.
func (m *Model) emptyBody(w, h int) []string {
	st := m.st
	lines := []string{
		"",
		st.dialogTitle.Render("No daemon is connected."),
		"",
		"Nothing here can act on a daemon until one is: there is nothing to list,",
		"and no action key does anything.",
		"",
		m.hints(keyDaemons, "open the picker: add a daemon from this machine's Docker contexts"),
		m.hints("q", "quit"),
	}
	members := m.sess.Members()
	if len(members) > 0 {
		lines = append(lines, "", st.tone(toneAccent, "Still trying:"))
		for _, mem := range members {
			text := "  " + mem.Name() + " — " + mem.State().String()
			if r := mem.Reason(); r != "" {
				text += ": " + r
			}
			for _, part := range wrapWords(text, w-6) {
				lines = append(lines, st.tone(toneDim, part))
			}
		}
	}
	out := m.st.box("No daemon", "", lines, w, h, true)
	return out
}

// onEmptyKey: with no daemon, only the picker, help, the theme and quitting
// do anything.
func (m *Model) onEmptyKey(key string) tea.Cmd {
	switch key {
	case "q":
		return m.requestQuit()
	case keyDaemons:
		m.openDaemons()
		return nil
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
		return nil
	case "T":
		m.setTheme(nextTheme(m.st.theme))
		return nil
	}
	m.setStatus(toneWarn, "No daemon is connected: nothing to act on. "+keyDaemons+" opens the picker; q quits.")
	return nil
}

// ---- the picker ---------------------------------------------------------------------

// daemonPicker is the open picker.
type daemonPicker struct {
	contexts []dockerctx.Context
	from     string // where the contexts were read from, for the heading
	selKey   string // "m:<id>" or "c:<name>": the highlighted entry
	off      int    // first line drawn of the entries
	confirm  int    // the member whose removal awaits y; 0 for none
	note     string // the outcome of the last key, said in the picker
	noteTone tone
}

type pickerEntry struct {
	key    string
	member *session.Member
	ctx    *dockerctx.Context
}

func (m *Model) contexts() ([]dockerctx.Context, string) {
	if m.opts.Contexts != nil {
		return m.opts.Contexts(), "this machine"
	}
	home := m.opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dir := dockerctx.ConfigDir(os.Getenv, home)
	return dockerctx.ListContexts(os.Getenv, home), dir
}

// openDaemons is M.
func (m *Model) openDaemons() {
	p := &daemonPicker{}
	p.contexts, p.from = m.contexts()
	if m.cur != nil {
		p.selKey = fmt.Sprintf("m:%d", m.cur.ID())
	}
	m.dlg = &dialog{kind: dlgDaemons, dm: p, openedAt: time.Now()}
	m.dlg.text.wrap = true
}

func (m *Model) pickerEntries(p *daemonPicker) []pickerEntry {
	var out []pickerEntry
	for _, mem := range m.sess.Members() {
		out = append(out, pickerEntry{key: fmt.Sprintf("m:%d", mem.ID()), member: mem})
	}
	for i := range p.contexts {
		out = append(out, pickerEntry{key: "c:" + p.contexts[i].Name, ctx: &p.contexts[i]})
	}
	return out
}

func (m *Model) pickerSelected(p *daemonPicker) (int, []pickerEntry) {
	entries := m.pickerEntries(p)
	for i, e := range entries {
		if e.key == p.selKey {
			return i, entries
		}
	}
	if len(entries) > 0 {
		p.selKey = entries[0].key
	}
	return 0, entries
}

func (m *Model) pickerNote(p *daemonPicker, t tone, text string) {
	p.note, p.noteTone = text, t
}

func (m *Model) onDaemonsKey(key string) tea.Cmd {
	p := m.dlg.dm
	sel, entries := m.pickerSelected(p)
	if p.confirm != 0 {
		switch key {
		case "y":
			if !m.armed() {
				return nil
			}
			id := p.confirm
			p.confirm = 0
			return m.removeMember(p, id)
		case "n", "esc", "q":
			p.confirm = 0
			m.pickerNote(p, toneDim, "Kept in the session.")
		}
		return nil
	}
	switch key {
	case "esc", "q", keyDaemons:
		m.dlg = nil
	case "down", "j":
		if sel+1 < len(entries) {
			p.selKey = entries[sel+1].key
		}
	case "up", "k":
		if sel > 0 {
			p.selKey = entries[sel-1].key
		}
	case "g", "home":
		if len(entries) > 0 {
			p.selKey = entries[0].key
		}
	case "G", "end":
		if len(entries) > 0 {
			p.selKey = entries[len(entries)-1].key
		}
	case "r":
		p.contexts, p.from = m.contexts()
		m.pickerNote(p, toneDim, "Contexts read again.")
	case "enter":
		if !m.armed() || len(entries) == 0 {
			return nil
		}
		e := entries[sel]
		if e.member != nil {
			mem, err := m.sess.Focus(e.member.ID())
			if err != nil {
				m.pickerNote(p, toneWarn, "Cannot focus "+e.member.Name()+": "+err.Error())
				return nil
			}
			m.dlg = nil
			return m.focused(mem)
		}
		m.addContext(p, *e.ctx)
	case "d", "x", "delete":
		if len(entries) == 0 || entries[sel].member == nil {
			m.pickerNote(p, toneDim, "Only a daemon in the session can be removed; the contexts are only read.")
			return nil
		}
		mem := entries[sel].member
		if running := m.sess.RunningJobs(mem); len(running) > 0 {
			m.pickerNote(p, toneBad, "Cannot remove "+mem.Name()+": "+jobsNamed(running)+". Let them finish, or cancel them in the Jobs pane (c), then remove it.")
			return nil
		}
		p.confirm = mem.ID()
		m.dlg.openedAt = time.Now() // the consent must be read first, as any other
		m.pickerNote(p, toneNormal, "")
	}
	return nil
}

func jobsNamed(js []jobs.Job) string {
	names := make([]string, 0, len(js))
	for _, j := range js {
		names = append(names, strings.TrimSpace(j.Title+" "+j.Target))
	}
	if len(js) == 1 {
		return "1 job is running on it — " + names[0]
	}
	return fmt.Sprintf("%d jobs are running on it — %s", len(js), strings.Join(names, "; "))
}

// addContext adds a picked context to the session.
func (m *Model) addContext(p *daemonPicker, c dockerctx.Context) {
	if !c.Usable() {
		m.pickerNote(p, toneBad, c.Name+" cannot be added: "+c.Problem)
		return
	}
	if old := m.sess.Find(c.Endpoint); old != nil {
		p.selKey = fmt.Sprintf("m:%d", old.ID())
		m.pickerNote(p, toneWarn, c.Name+" is already in the session as "+old.Name()+" ("+old.Endpoint().Host+"): one member per daemon.")
		return
	}
	mem, existing, err := m.sess.Add(c.Endpoint)
	switch {
	case err != nil:
		m.pickerNote(p, toneBad, c.Name+" cannot be added: "+err.Error())
	case existing:
		p.selKey = fmt.Sprintf("m:%d", mem.ID())
		m.pickerNote(p, toneWarn, c.Name+" is already in the session as "+mem.Name()+": one member per daemon.")
	default:
		p.selKey = fmt.Sprintf("m:%d", mem.ID())
		note := "Added " + mem.Name() + "; connecting in the background — nothing will be asked on the terminal."
		if m.readOnly {
			note += " It is read-only, as the whole session is."
		}
		m.pickerNote(p, toneGood, note)
	}
}

// removeMember removes a member after its confirmation. The job check is
// made again here, under the job manager's lock: a job started since the
// confirmation opened still refuses it.
func (m *Model) removeMember(p *daemonPicker, id int) tea.Cmd {
	mem := m.sess.Get(id)
	if mem == nil {
		m.pickerNote(p, toneDim, "That daemon has already left the session.")
		return nil
	}
	wasFocused := mem == m.cur
	focus, err := m.sess.Remove(id)
	if err != nil {
		var refused *session.RefusedError
		if errors.As(err, &refused) {
			m.pickerNote(p, toneBad, "Cannot remove "+mem.Name()+": "+jobsNamed(refused.Jobs)+". Let them finish, or cancel them in the Jobs pane (c), then remove it.")
		} else {
			m.pickerNote(p, toneBad, "Cannot remove "+mem.Name()+": "+err.Error())
		}
		return nil
	}
	text := "Removed " + mem.Name() + " from the session; its streams and connection are closed."
	if mem.Link().Transport == dockerctx.TransportSSH {
		text = "Removed " + mem.Name() + " from the session; its streams, connection and ssh process are closed."
	}
	var cmd tea.Cmd
	if wasFocused {
		cmd = m.switchTo(focus)
		switch {
		case focus != nil:
			text += " Focus moved to " + focus.Name() + "."
		default:
			text += " No daemon is connected now."
		}
	}
	if m.dlg != nil && m.dlg.dm == p {
		m.pickerNote(p, toneGood, text)
		if focus != nil {
			p.selKey = fmt.Sprintf("m:%d", focus.ID())
		} else {
			p.selKey = ""
		}
	}
	m.setStatus(toneGood, text)
	return cmd
}

// stateWords is a member's state as the picker says it.
func stateWords(mem *session.Member) (string, tone) {
	switch mem.State() {
	case session.Connected:
		return "● connected", toneGood
	case session.Connecting:
		return "… connecting", toneDim
	case session.Removing:
		return "removing", toneDim
	}
	return "✗ disconnected — retrying", toneBad
}

func (m *Model) daemonsBody(d *dialog, w, maxBody int) (string, []string) {
	p := d.dm
	st := m.st
	sel, entries := m.pickerSelected(p)
	const indent = 6
	pad := strings.Repeat(" ", indent)
	wrapped := func(text string, t tone) []string {
		var out []string
		for _, part := range wrapLine(text, w-indent) {
			out = append(out, pad+st.tone(t, part))
		}
		return out
	}
	words := func(text string, t tone) []string {
		var out []string
		for _, part := range wrapWords(text, w-indent) {
			out = append(out, pad+st.tone(t, part))
		}
		return out
	}
	tag := func(l dockerctx.Link) string {
		if l.Label == "" {
			return ""
		}
		return "  " + st.tone(toneForLink(l), "["+l.Label+"]")
	}

	var head []string
	if m.readOnly {
		head = append(head, st.tone(toneWarn, "▲ Read-only session: every daemon in it — and any added here — refuses"),
			st.tone(toneWarn, "  every change, before anything is sent (--read-only)."), "")
	}

	// The entries, each a block of lines.
	var blocks [][]string
	section := map[int]string{}
	for i, e := range entries {
		cursor := "  "
		if i == sel {
			cursor = "▸ "
		}
		var b []string
		if e.member != nil {
			if i == 0 {
				section[i] = "In this session"
			}
			marker := "  "
			if e.member == m.sess.Focused() {
				marker = "◆ "
			}
			words1, t := stateWords(e.member)
			line := cursor + marker + st.dialogTitle.Render(e.member.Name()) + "  " + st.tone(t, words1) + tag(e.member.Link())
			if m.readOnly {
				line += "  " + st.tone(toneWarn, "read-only")
			}
			b = append(b, line)
			b = append(b, wrapped(e.member.Endpoint().Host, toneNormal)...)
			if r := e.member.Reason(); r != "" {
				b = append(b, words(r, toneBad)...)
			}
			if running := m.sess.RunningJobs(e.member); len(running) > 0 {
				b = append(b, words(jobsNamed(running), toneWarn)...)
			}
			if p.confirm == e.member.ID() {
				b = append(b, "")
				b = append(b, words("Remove "+e.member.Name()+" from the session? Its streams and its connection close"+
					map[bool]string{true: ", and the ssh process started for it ends", false: ""}[e.member.Link().Transport == dockerctx.TransportSSH]+
					". Nothing on the daemon changes; it can be added again.", toneWarn)...)
				b = append(b, pad+m.hints("y", "remove", "n", "keep"))
			}
		} else {
			c := e.ctx
			if i == 0 || entries[i-1].member != nil {
				section[i] = "Contexts on " + p.from
			}
			name := c.Name
			if c.Current {
				name += " *"
			}
			line := cursor + "  " + st.dialogTitle.Render(name)
			switch {
			case !c.Usable():
				line += "  " + st.tone(toneBad, "✗ cannot be used")
			case m.sess.Find(c.Endpoint) != nil:
				line += "  " + st.tone(toneDim, "in the session")
			}
			if c.Usable() {
				line += tag(c.Endpoint.Link())
			}
			b = append(b, line)
			if c.Endpoint.Host != "" {
				b = append(b, wrapped(c.Endpoint.Host, toneNormal)...)
			}
			if c.Problem != "" {
				b = append(b, words(c.Problem, toneBad)...)
			}
		}
		blocks = append(blocks, b)
	}

	var tail []string
	if p.note != "" {
		tail = append(tail, "")
		for _, part := range wrapWords(p.note, w) {
			tail = append(tail, st.tone(p.noteTone, part))
		}
	}
	tail = append(tail, "")
	if p.confirm != 0 {
		tail = append(tail, m.hints("y", "remove", "n Esc", "keep"))
	} else {
		tail = append(tail, m.hints("↑↓", "move", "⏎", "focus / add", "d", "remove", "r", "re-read", "Esc", "close"))
	}

	// Lay the entries out with their section headings, and scroll so that
	// the highlighted one is whole on screen.
	var lines []string
	start, end := 0, 0
	for i, b := range blocks {
		if s, ok := section[i]; ok {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, st.tone(toneAccent, s))
		}
		if i == sel {
			start = len(lines)
		}
		lines = append(lines, b...)
		if i == sel {
			end = len(lines)
		}
	}
	if len(entries) == 0 {
		lines = append(lines, st.tone(toneDim, "No daemon in the session, and no context to add."))
	}
	room := max(maxBody-len(head)-len(tail)-2, 4)
	if p.off > start {
		p.off = start
	}
	if end-p.off > room {
		p.off = end - room
	}
	if p.off > len(lines)-room {
		p.off = max(len(lines)-room, 0)
	}
	if p.off < 0 {
		p.off = 0
	}
	shown := lines[p.off:min(p.off+room, len(lines))]
	body := append(append(append([]string{}, head...), shown...), tail...)
	return "Daemons", body
}

func toneForLink(l dockerctx.Link) tone {
	switch {
	case l.Insecure:
		return toneBad
	case l.Remote:
		return toneWarn
	}
	return toneDim
}
