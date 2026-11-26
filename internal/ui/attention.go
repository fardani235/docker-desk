package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/attention"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/session"
)

// The Attention view: is anything wrong anywhere? One screen for every
// daemon of the session, problems first (docs/DECISIONS.md, AT1–AT17).
//
// The rules — which container needs a person, in what order — are package
// attention's; the reads — one list per connected daemon, inspects only of
// the candidates, re-read on the daemon's own events, never on a timer — are
// internal/session's (Attention). This file is the screen: it takes what the
// run has, draws it, moves a cursor over it and goes to a container.
//
// The view reads and nothing else. It has no action: Enter closes it on the
// container in the desk, where the desk's own confirmations are. Everything
// a daemon said — names, images, status prose, errors — is drawn escaped.

// keyAttention opens the view, and closes it.
const keyAttention = "H"

// attnTextMax bounds a daemon's prose (a status, an error, a reason) before
// it is laid out: no answer can make the view build an unbounded screen.
const attnTextMax = 400

// attnRowsMax bounds the rows one block lists. A daemon with more problems
// than that has the rest counted in a line under them: the screen stays
// drawable whatever a daemon holds, and nothing is silently dropped.
const attnRowsMax = 500

type attnState struct {
	open bool
	// What the desk looked like when the view opened; put back on close.
	prevFocus Pane
	prevKind  Kind
	prevKey   string

	run    *session.Attention
	ver    uint64
	sig    string // the session as the blocks were taken (sessionSig)
	blocks []session.AttentionBlock
	more   []int // per block: rows beyond attnRowsMax, counted and not listed

	cursorKey string // "<member id>/<container id>"
	cursorIdx int    // where the cursor was, should its row go
	block     int    // the block Tab moves from: an index into blocks
	follow    bool   // the cursor moved: bring it into view
	toHead    bool   // Tab: bring the block's heading to the top
	off       int
	listH     int
	onScreen  bool  // the cursor's row was drawn in the last frame
	heads     []int // the line each block's heading is drawn on
	drawnSec  int64

	// jump is an Enter still waiting for its container; back is a highlight
	// to put back once the daemon returned to has its list.
	jump *attnJump
	back *attnBack
}

// attnRow is a row the cursor rests on: one container of one member.
type attnRow struct {
	key    string
	block  int
	member int
	id     string
	name   string
}

type attnLine struct {
	text string
	row  *attnRow
}

// attnJump is Enter's wait: the daemon is focused, its list is asked for,
// and the container is selected when the list has it.
type attnJump struct {
	member int
	id     string
	name   string // already escaped
	daemon string
	asked  time.Time
	until  time.Time
	// Where to return to if the container is not there.
	moved      bool // focus moved to another daemon for the jump
	prevMember int
	prevFocus  Pane
	prevKind   Kind
	prevKey    string
}

type attnBack struct {
	member int
	kind   Kind
	key    string
	until  time.Time
}

func attnKey(member int, id string) string { return fmt.Sprintf("%d/%s", member, id) }

// clipText bounds a daemon's prose and makes it safe to draw.
func clipText(s string) string {
	if len(s) > attnTextMax {
		cut := attnTextMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut-- // not in the middle of a character
		}
		s = s[:cut] + "…"
	}
	return escape.Name(s)
}

// ---- opening and closing ---------------------------------------------------------

// openAttention is H. It changes nothing on any daemon: the focused one's
// block is the list the desk already holds, and every other connected
// member's containers are read for the view — and only while it is open.
func (m *Model) openAttention() tea.Cmd {
	m.dropAttention()
	m.at = attnState{
		open:      true,
		prevFocus: m.focus, prevKind: m.kind, prevKey: m.sel[m.kind].key,
		follow: true,
	}
	m.at.run = m.sess.OpenAttention(m.opts.Attention)
	if m.cur != nil && m.loaded[listContainers] {
		m.at.run.Seed(m.cur, m.snap.Containers, m.listAt[listContainers])
	}
	now := time.Now()
	m.at.run.Sync(now)
	m.takeAttention()
	m.at.drawnSec = now.Unix()
	return nil
}

// dropAttention ends the view's reads and forgets the view, restoring
// nothing.
func (m *Model) dropAttention() {
	if m.at.run != nil {
		m.at.run.Close()
	}
	jump, back := m.at.jump, m.at.back
	m.at = attnState{jump: jump, back: back}
	m.dirty = true
}

// closeAttention is Esc and H: the reads end, and the pane, the kind and the
// highlight are the ones the view interrupted.
func (m *Model) closeAttention() {
	prevFocus, prevKind, prevKey := m.at.prevFocus, m.at.prevKind, m.at.prevKey
	m.dropAttention()
	m.focus = prevFocus
	m.setKind(prevKind)
	for i, r := range m.rows[m.kind] {
		if r.key == prevKey {
			m.selectIndex(i)
			break
		}
	}
}

// collectAttention runs on every frame while the view is open: the run
// follows the session and its event rings, and what it has read since the
// last frame is taken. On a quiet session this sends and redraws nothing,
// but for the ages, which move each second.
func (m *Model) collectAttention(now time.Time) {
	at := &m.at
	if !at.open {
		return
	}
	if m.cur == nil {
		// No daemon is left to draw the view over.
		m.dropAttention()
		return
	}
	at.run.Sync(now)
	if at.run.Version() != at.ver || at.sig != m.ds2.sig {
		m.takeAttention()
	}
	if sec := now.Unix(); sec != at.drawnSec {
		at.drawnSec = sec
		m.dirty = true
	}
}

// takeAttention copies the run's blocks, the focused daemon's first.
func (m *Model) takeAttention() {
	at := &m.at
	// The version is read first: an answer that lands in between is taken
	// again on the next frame, never missed.
	at.ver = at.run.Version()
	at.sig = m.sessionSig()
	first := 0
	if m.cur != nil {
		first = m.cur.ID()
	}
	blocks := at.run.Blocks(first)
	// A daemon that left the session while the view is open: its block
	// goes, and the status line says so.
	for _, old := range at.blocks {
		gone := true
		for _, b := range blocks {
			if b.Member == old.Member {
				gone = false
			}
		}
		if gone {
			m.setStatus(toneWarn, clipText(old.Member.Name())+" left the session: its block is gone, and nothing more is read from it.")
		}
	}
	at.more = make([]int, len(blocks))
	for i := range blocks {
		if n := len(blocks[i].Block.Rows); n > attnRowsMax {
			at.more[i] = n - attnRowsMax
			blocks[i].Block.Rows = blocks[i].Block.Rows[:attnRowsMax]
		}
	}
	at.blocks = blocks
	at.block = clamp(at.block, 0, max(len(blocks)-1, 0))
	m.dirty = true
}

// attnBusy reports that a read is under way: the spinner turns.
func (m *Model) attnBusy() bool { return m.at.open && m.at.run.Reading() }

// ---- rows and totals ----------------------------------------------------------------

// attnRows lists the rows the cursor moves over, in display order.
func (m *Model) attnRows() []attnRow {
	var out []attnRow
	for bi, b := range m.at.blocks {
		if b.State != session.Connected || !b.Loaded {
			continue
		}
		for _, r := range b.Block.Rows {
			out = append(out, attnRow{key: attnKey(b.Member.ID(), r.ID), block: bi, member: b.Member.ID(), id: r.ID, name: r.Name})
		}
	}
	return out
}

// attnCursor returns the highlighted row's index: the row last chosen if it
// is still listed, else the one now at its place.
func (m *Model) attnCursor(rows []attnRow) int {
	at := &m.at
	if len(rows) == 0 {
		return -1
	}
	for i, r := range rows {
		if r.key == at.cursorKey {
			at.cursorIdx = i
			return i
		}
	}
	at.cursorIdx = clamp(at.cursorIdx, 0, len(rows)-1)
	at.cursorKey = rows[at.cursorIdx].key
	return at.cursorIdx
}

// attnTotals sums the session: the containers read, those that need a
// person, those to watch, and how many daemons were read.
type attnTotals struct {
	daemons, read       int // members; members whose containers are in
	containers          int
	needs, watch        int
	reading             bool // something is still on its way
	open                int  // containers in no class: unread, not inspected
	failed              int  // members whose last read failed
	clear               bool // every member read, and nothing listed
	oldest              time.Time
	disconnected, total int
}

func (m *Model) attnTotals() attnTotals {
	t := attnTotals{total: len(m.at.blocks), clear: true}
	for _, b := range m.at.blocks {
		if b.State != session.Connected {
			t.disconnected++
			continue
		}
		t.daemons++
		if b.Err != nil {
			t.failed++
			t.clear = false
		}
		if b.Reading {
			t.reading = true
		}
		if !b.Loaded {
			t.clear = false
			continue
		}
		t.read++
		c := b.Block.Counts
		t.containers += c.Total
		t.needs += c.NeedsYou
		t.watch += c.Watch
		t.open += c.Reading + c.NotInspected + c.InspectFailed
		if !c.Clear() || c.NotInspected+c.InspectFailed > 0 || b.Reading || b.Beyond > 0 {
			t.clear = false
		}
		if t.oldest.IsZero() || b.ReadAt.Before(t.oldest) {
			t.oldest = b.ReadAt
		}
	}
	if t.read == 0 {
		t.clear = false
	}
	return t
}

func countNoun(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- keys ----------------------------------------------------------------------------

func (m *Model) onAttentionKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	at := &m.at
	rows := m.attnRows()
	cur := m.attnCursor(rows)
	page := max(at.listH-1, 1)
	switch key {
	case keyAttention, "esc":
		m.closeAttention()
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "down", "j":
		m.attnMove(rows, cur, 1)
	case "up", "k":
		m.attnMove(rows, cur, -1)
	case "pgdown", "ctrl+f", " ":
		m.attnMove(rows, cur, page)
	case "pgup", "ctrl+b":
		m.attnMove(rows, cur, -page)
	case "g", "home":
		at.off, at.block, at.follow = 0, 0, false
		if len(rows) > 0 {
			at.cursorKey, at.cursorIdx = rows[0].key, 0
		}
	case "G", "end":
		at.off, at.block, at.follow = 1<<30, max(len(at.blocks)-1, 0), false // clamped when drawn
		if n := len(rows); n > 0 {
			at.cursorKey, at.cursorIdx = rows[n-1].key, n-1
		}
	case "tab":
		m.attnBlock(rows, 1)
	case "shift+tab":
		m.attnBlock(rows, -1)
	case "enter":
		if cur >= 0 {
			// Where the row is — on screen or scrolled away — is decided
			// by the layout the next frame is drawn with, not by a frame
			// that keys have moved on from since.
			m.attentionBox(m.width, max(m.height-headerLines-2, 3))
		}
		switch {
		case cur < 0:
			m.setStatus(toneDim, "No container is listed to go to.")
		case !at.onScreen:
			// Enter acts on what is on screen: a highlighted row that
			// was scrolled out of view is brought back first.
			at.follow = true
			m.setStatus(toneDim, "The highlighted container is "+clipText(rows[cur].name)+": Enter again goes to it.")
		default:
			return m.attentionJump(rows[cur])
		}
	case "r":
		// Every connected daemon is read again: at most one read a second
		// per daemon, so a held key cannot become a stream of requests.
		at.run.Refresh()
		at.run.Sync(time.Now())
		m.setStatus(toneDim, "Reading every connected daemon's containers again…")
	case "T":
		m.setTheme(nextTheme(m.st.theme))
	default:
		for _, a := range kindActions[KindContainers] {
			if a.key == key {
				m.setStatus(toneDim, "Attention only reads: nothing was sent. Enter goes to the container in the desk, where its actions are.")
			}
		}
	}
	return nil
}

// attnMove moves the cursor delta rows. Where it cannot move — no rows, or
// already at an end — the view scrolls instead, so that what lies beyond the
// last row can still be read.
func (m *Model) attnMove(rows []attnRow, cur, delta int) {
	at := &m.at
	if len(rows) == 0 {
		at.off, at.follow = at.off+delta, false
		return
	}
	i := clamp(cur+delta, 0, len(rows)-1)
	if i == cur {
		at.off, at.follow = at.off+delta, false
		return
	}
	at.cursorKey, at.cursorIdx, at.block, at.follow = rows[i].key, i, rows[i].block, true
}

// attnBlock is Tab and Shift+Tab: the next or previous daemon's block comes
// to the top, and the cursor goes to its first row if it has one.
func (m *Model) attnBlock(rows []attnRow, delta int) {
	at := &m.at
	if len(at.blocks) == 0 {
		return
	}
	at.block = clamp(at.block+delta, 0, len(at.blocks)-1)
	at.toHead = true
	for i, r := range rows {
		if r.block == at.block {
			at.cursorKey, at.cursorIdx = r.key, i
			break
		}
	}
}

func (m *Model) mouseAttention(h hit, delta int) {
	rows := m.attnRows()
	cur := m.attnCursor(rows)
	if delta != 0 {
		m.attnMove(rows, cur, mouseWheelRows*delta)
		return
	}
	if h.kind != hitAttnRow {
		return
	}
	// The pointer navigates: a click highlights a row and goes nowhere.
	for i, r := range rows {
		if r.key == h.key {
			m.at.cursorKey, m.at.cursorIdx, m.at.block = r.key, i, r.block
		}
	}
}

// ---- Enter: to the container on the desk --------------------------------------------

// attentionJump closes the view on a row's container: its daemon is focused,
// its Containers list is asked for, and the container is selected as soon as
// the list has it. A daemon that is not connected is not focused: the view
// stays, and says so.
func (m *Model) attentionJump(r attnRow) tea.Cmd {
	name := clipText(r.name)
	mem := m.sess.Get(r.member)
	switch {
	case mem == nil:
		m.setStatus(toneWarn, "That daemon has left the session: "+name+" cannot be gone to. Nothing moved.")
		return nil
	case mem.State() != session.Connected:
		text := clipText(mem.Name()) + " is " + mem.State().String()
		if why := mem.Reason(); why != "" {
			text += " (" + clipText(why) + ")"
		}
		m.setStatus(toneWarn, text+": "+name+" cannot be gone to. Nothing moved.")
		return nil
	}
	m.closeAttention() // the desk as it was: what a failed jump leaves
	j := &attnJump{
		member: mem.ID(), id: r.id, name: name, daemon: clipText(mem.Name()),
		prevFocus: m.focus, prevKind: m.kind, prevKey: m.sel[m.kind].key,
	}
	if m.cur != nil {
		j.prevMember = m.cur.ID()
	}
	var cmd tea.Cmd
	if mem != m.cur {
		if _, err := m.sess.Focus(mem.ID()); err != nil {
			m.setStatus(toneWarn, "Cannot focus "+j.daemon+": "+clipText(err.Error())+". Nothing moved.")
			return nil
		}
		cmd = m.switchTo(mem) // its lists are asked for at once
		j.moved = true
		m.setStatus(toneAccent, "Focused "+j.daemon+" — looking for "+name+" in its Containers list…")
	} else {
		if m.attnLand(j) {
			return nil
		}
		// Not in the list the desk holds: ask again, and wait for it.
		m.pending |= live.DirtyContainers
		m.lastStart[listContainers] = time.Time{}
		m.setStatus(toneDim, "Looking for "+name+": refreshing the Containers list…")
	}
	j.asked = time.Now()
	j.until = j.asked.Add(m.opts.JumpWait)
	m.at.jump, m.at.back = j, nil
	return cmd
}

// dropAttentionJump is what a key or a click does to a jump still waiting
// for its container: the jump is given up, said so, and the event that
// ended it is used up — a key typed for the container the jump was going to
// must not act on whatever row is highlighted instead. It reports whether
// there was a jump to drop.
func (m *Model) dropAttentionJump() bool {
	j := m.at.jump
	m.at.jump, m.at.back = nil, nil
	if j == nil {
		return false
	}
	text := "Stopped looking for " + j.name + " on " + j.daemon + ": that key or click came first, and did nothing else."
	if j.moved {
		text += " The focus stays on " + j.daemon + "."
	}
	m.setStatus(toneWarn, text)
	return true
}

// attnLand selects the jump's container if the focused daemon's list has it.
func (m *Model) attnLand(j *attnJump) bool {
	if m.cur == nil || m.cur.ID() != j.member || !m.loaded[listContainers] {
		return false
	}
	listed := false
	for _, c := range m.snap.Containers {
		if c.ID == j.id {
			listed = true
		}
	}
	if !listed {
		return false
	}
	if !m.landOn(KindContainers, j.name, func(x row) bool { return x.containerID == j.id }) {
		return false
	}
	m.setStatus(toneGood, j.name+" on "+j.daemon+".")
	return true
}

// syncAttentionJump runs on every frame: a pending jump lands when its
// container is listed, and gives up — saying so, and putting the desk back
// where it was — when the list answers without it, when its daemon is no
// longer the focused one, or when the wait is over.
func (m *Model) syncAttentionJump(now time.Time) tea.Cmd {
	at := &m.at
	if b := at.back; b != nil {
		switch {
		case m.cur == nil || m.cur.ID() != b.member || now.After(b.until):
			at.back = nil
		case m.loaded[listOf(b.kind)]:
			at.back = nil
			m.ensureRows(b.kind)
			for i, r := range m.rows[b.kind] {
				if r.key == b.key && m.kind == b.kind {
					m.selectIndex(i)
					m.dirty = true
				}
			}
		}
	}
	j := at.jump
	if j == nil {
		return nil
	}
	switch {
	case m.cur == nil || m.cur.ID() != j.member:
		at.jump = nil
		where := "No daemon is focused now."
		if m.cur != nil {
			where = "The focused daemon is " + clipText(m.cur.Name()) + "."
		}
		m.setStatus(toneWarn, j.daemon+" is no longer the focused daemon: "+j.name+" was not gone to. "+where)
		return nil
	case m.attnLand(j):
		at.jump = nil
		return nil
	case m.loaded[listContainers] && !m.listAsked[listContainers].Before(j.asked):
		// A list asked for after the jump began does not have it.
		at.jump = nil
		m.setStatus(toneWarn, j.name+" is no longer on "+j.daemon+": it is not in its Containers list. The desk is where it was.")
		return m.attnReturn(j, now)
	case now.After(j.until):
		at.jump = nil
		m.setStatus(toneWarn, j.name+" did not appear in "+j.daemon+"'s Containers list in time. The desk is where it was.")
		return m.attnReturn(j, now)
	}
	return nil
}

// attnReturn puts the desk back where a failed jump found it. A jump within
// one daemon moved nothing; one to another daemon focuses the previous one
// again, with its pane, kind and — once its list is back — highlight.
func (m *Model) attnReturn(j *attnJump, now time.Time) tea.Cmd {
	if !j.moved {
		return nil
	}
	prev := m.sess.Get(j.prevMember)
	if prev == m.cur {
		return nil
	}
	if prev == nil {
		m.setStatus(toneWarn, strings.TrimSuffix(m.status.text, " The desk is where it was.")+" The daemon focused before has left the session.")
		return nil
	}
	if _, err := m.sess.Focus(prev.ID()); err != nil {
		m.setStatus(toneWarn, strings.TrimSuffix(m.status.text, " The desk is where it was.")+" The daemon focused before cannot be returned to: "+clipText(err.Error()))
		return nil
	}
	status := m.status
	cmd := m.switchTo(prev)
	m.status = status
	m.focus = j.prevFocus
	m.setKind(j.prevKind)
	m.at.back = &attnBack{member: prev.ID(), kind: j.prevKind, key: j.prevKey, until: now.Add(10 * time.Second)}
	return cmd
}

// ---- drawing --------------------------------------------------------------------------

func attnGlyph(r attention.Row) (string, tone) {
	switch r.Signal {
	case attention.SignalRestarting:
		return "↻", toneBad
	case attention.SignalUnhealthy:
		return "●", toneBad
	case attention.SignalDead:
		return "✗", toneBad
	case attention.SignalExited:
		return "○", toneBad
	case attention.SignalFlapping:
		return "●", toneWarn
	case attention.SignalCreated:
		return "○", toneWarn
	}
	return "…", toneDim
}

// wordMax is the widest a state or health word is drawn: the daemon's own
// are at most ten cells ("restarting"); anything longer is not one of them.
const wordMax = 16

// daemonWord is a state or a health status as the desk draws it: the
// daemon's own word, escaped and bounded in cells — never one the daemon did
// not send, and never wide enough to push a column off the list.
func daemonWord(s string) string {
	if len(s) > 4*wordMax {
		cut := 4 * wordMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	s = escape.Name(s)
	if ansi.StringWidth(s) > wordMax {
		s = ansi.Truncate(s, wordMax, "…")
	}
	return s
}

// healthWord is daemonWord for a health status.
func healthWord(h string) string { return daemonWord(h) }

// attnWhat says why a row is listed, from what was read and nothing else.
func attnWhat(r attention.Row, now time.Time) string {
	d := r.Detail
	var parts []string
	add := func(s string) { parts = append(parts, s) }
	restarts := func() {
		if r.HasDetail && d.RestartCount > 0 {
			add(countNoun(d.RestartCount, "restart", "restarts"))
		}
	}
	finished := func() {
		if r.HasDetail && !d.FinishedAt.IsZero() && d.FinishedAt.Year() > 1 {
			add("finished " + d.FinishedAt.Local().Format("15:04:05") + ", " + humanAge(d.FinishedAt, now) + " ago")
		}
	}
	exit := func() {
		if !r.HasDetail {
			return
		}
		if d.OOMKilled {
			add(fmt.Sprintf("OOM-killed · exit %d", d.ExitCode))
		} else {
			add(fmt.Sprintf("exit %d", d.ExitCode))
		}
	}
	switch r.Signal {
	case attention.SignalRestarting:
		add("restarting")
		if r.HasDetail {
			exit()
			restarts()
		}
	case attention.SignalUnhealthy:
		add("running · unhealthy")
	case attention.SignalDead:
		add("dead")
		exit()
	case attention.SignalExited:
		if d.OOMKilled {
			add(fmt.Sprintf("exited %d · OOM-killed", d.ExitCode))
		} else {
			add(fmt.Sprintf("exited %d", d.ExitCode))
		}
		finished()
		restarts()
	case attention.SignalFlapping:
		s := "running"
		if r.Health != "" {
			s += " · " + healthWord(r.Health)
		}
		add(s)
		restarts()
	case attention.SignalCreated:
		add("created, never started")
	default:
		add("exited · reading its exit code…")
	}
	if r.HasDetail && d.Error != "" {
		add(clipText(d.Error))
	}
	switch r.Signal {
	case attention.SignalRestarting, attention.SignalUnhealthy, attention.SignalDead:
		// The daemon's own words for the state, as words: never parsed.
		if r.Status != "" && !strings.EqualFold(r.Status, r.State) {
			add(clipText(r.Status))
		}
	}
	return strings.Join(parts, " · ")
}

// attnCounts is a block's counts line: every class that is not listed.
func attnCounts(c attention.Counts, bound int) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%d fine", c.Fine))
	parts = append(parts, fmt.Sprintf("%d unknown (no health verdict)", c.Unknown))
	parts = append(parts, fmt.Sprintf("%d not a problem (paused, exited 0)", c.NotAProblem))
	if c.Reading > 0 {
		parts = append(parts, fmt.Sprintf("%d still being read", c.Reading))
	}
	if c.NotInspected > 0 {
		parts = append(parts, fmt.Sprintf("%d not inspected (the view stops at %d inspects a daemon)", c.NotInspected, bound))
	}
	if c.InspectFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d could not be inspected", c.InspectFailed))
	}
	return strings.Join(parts, " · ")
}

// attnLines is the scrolling part of the view: one block per member, and
// the line each block's heading is on.
func (m *Model) attnLines(w int, now time.Time) (lines []attnLine, heads []int) {
	st := m.st
	at := &m.at
	text := func(t tone, indent int, s string) {
		pad := strings.Repeat(" ", indent)
		for _, part := range wrapWords(s, max(w-indent, 8)) {
			lines = append(lines, attnLine{text: pad + st.tone(t, part)})
		}
	}
	tot := m.attnTotals()
	focus := m.sess.Focused()

	if len(at.blocks) == 0 {
		text(toneDim, 0, "No daemon is in the session. "+keyDaemons+" opens the picker.")
		return lines, nil
	}
	if tot.clear {
		// Said once for the session, not once per daemon.
		s := "✓ Nothing needs attention on " + countNoun(tot.read, "daemon", "daemons") + ": " + countNoun(tot.containers, "container", "containers") + " read."
		text(toneGood, 0, s)
		if tot.disconnected > 0 {
			text(toneWarn, 0, countNoun(tot.disconnected, "daemon is", "daemons are")+" not connected and could not be read.")
		}
	}

	// The name column is as wide as the longest name on screen, within bounds.
	nameW := 12
	for _, b := range at.blocks {
		for _, r := range b.Block.Rows {
			nameW = max(nameW, ansi.StringWidth(escape.Name(r.Name)))
		}
	}
	nameW = min(nameW, max(w/3, 12), 32)
	imageW := 0
	if w >= 100 {
		imageW = clamp(w/5, 16, 28)
	}

	for bi, b := range at.blocks {
		if len(lines) > 0 {
			lines = append(lines, attnLine{})
		}
		heads = append(heads, len(lines))
		mem := b.Member
		info := mem.Info()

		// -- heading: who, where, how reached, what engine ---------------
		marker := "  "
		if mem == focus {
			marker = "◆ "
		}
		head := marker + st.dialogTitle.Render(clipText(mem.Name()))
		switch b.State {
		case session.Connected:
			head += "  " + st.tone(toneGood, "● connected")
		case session.Connecting:
			head += "  " + st.tone(toneDim, "… connecting")
		case session.Removing:
			head += "  " + st.tone(toneDim, "removing")
		default:
			head += "  " + st.tone(toneBad, "✗ disconnected")
		}
		if l := mem.Link(); l.Label != "" {
			head += "  " + st.tone(toneForLink(l), "["+clipText(l.Label)+"]")
		}
		if c := mem.Endpoint().Context; c != "" {
			head += "  " + st.tone(toneDim, "ctx "+clipText(c))
		}
		if b.State == session.Connected && info.ServerVersion != "" {
			head += "  " + st.tone(toneDim, "Engine "+clipText(info.ServerVersion))
		}
		if mem == focus {
			head += "  " + st.tone(toneDim, "focused")
		}
		lines = append(lines, attnLine{text: fit(head, w)})
		// The endpoint in full when it fits; its middle gives way when not.
		lines = append(lines, attnLine{text: "  " + st.tone(toneAccent, middleTruncate(clipText(mem.Endpoint().Host), max(w-2, 12)))})

		if b.State != session.Connected {
			why := b.Reason
			if why == "" {
				why = "no answer yet"
			}
			text(toneBad, 2, "Not read: "+clipText(why)+". No container of this daemon is shown.")
			continue
		}

		// -- totals and provenance ----------------------------------------
		c := b.Block.Counts
		switch {
		case b.Loaded:
			s := countNoun(c.Total, "container", "containers") + " · " + fmt.Sprintf("%d need you", c.NeedsYou)
			if c.NeedsYou == 1 {
				s = countNoun(c.Total, "container", "containers") + " · 1 needs you"
			}
			s += fmt.Sprintf(" · %d to watch", c.Watch)
			s += " · read " + b.ReadAt.Local().Format("15:04:05") + ", " + humanAge(b.ReadAt, now) + " ago"
			if b.Seeded {
				s += " (the desk's list)"
			}
			if b.Reading {
				s += " · " + spinnerFrames[m.spinner] + " reading"
			}
			t := toneNormal
			if c.NeedsYou > 0 {
				t = toneWarn
			}
			text(t, 2, s)
		case b.Reading:
			text(toneAccent, 2, spinnerFrames[m.spinner]+" reading its containers (GET /containers/json)…")
		case b.Err == nil:
			text(toneDim, 2, "Not read yet. r reads it.")
		}
		if b.Err != nil {
			s := "▲ The read at " + b.ErrAt.Local().Format("15:04:05") + " failed: " + clipText(plainError(b.Err)) + " — r reads again."
			if b.Loaded {
				s += " The rows below are those of " + b.ReadAt.Local().Format("15:04:05") + "."
			} else {
				s += " Nothing of this daemon is shown."
			}
			text(toneBad, 2, s)
		}
		if !b.Loaded {
			continue
		}

		// -- rows: needs you, then watch, then still being read ---------
		watchHead := false
		for _, r := range b.Block.Rows {
			if r.Class == attention.Watch && !watchHead {
				watchHead = true
				if c.NeedsYou > 0 {
					lines = append(lines, attnLine{text: "  " + st.tone(toneDim, "watch")})
				}
			}
			glyph, t := attnGlyph(r)
			what := attnWhat(r, now)
			if imageW == 0 && r.Project != "" {
				what += " · project " + clipText(r.Project)
			}
			line := glyph + " " + fit(escape.Name(r.Name), nameW) + "  "
			rest := w - 4 - ansi.StringWidth(line) // the cursor's gutter, and a margin
			if imageW > 0 {
				tail := escape.Name(shortImage(r.Image))
				if r.Project != "" {
					tail += "  [" + clipText(r.Project) + "]"
				}
				line += fit(what, max(rest-imageW-2, 10)) + "  " + fit(tail, imageW)
			} else {
				line += fit(what, max(rest, 10))
			}
			row := attnRow{key: attnKey(mem.ID(), r.ID), block: bi, member: mem.ID(), id: r.ID, name: r.Name}
			lines = append(lines, attnLine{text: "  " + st.tone(t, line), row: &row})
		}
		if bi < len(at.more) && at.more[bi] > 0 {
			text(toneWarn, 2, fmt.Sprintf("… and %d more that need you or are watched: the view lists %d a daemon. They are in the counts above.", at.more[bi], attnRowsMax))
		}
		if len(b.Block.Rows) == 0 && !tot.clear {
			switch {
			case b.Reading:
				text(toneDim, 2, "Nothing listed so far.")
			case c.NotInspected+c.InspectFailed > 0 || b.Beyond > 0:
				text(toneWarn, 2, "Nothing that was read here needs you — but not everything was read.")
			default:
				text(toneGood, 2, "✓ Nothing here needs you.")
			}
		}
		text(toneDim, 2, attnCounts(c, b.Bound))
		if b.Beyond > 0 {
			text(toneWarn, 2, fmt.Sprintf("%d more containers of this daemon were not looked at: the view reads the first %d of a list. They are in no class and in no count above.", b.Beyond, b.ListBound))
		}
	}
	return lines, heads
}

// attentionBox draws the view over the desk's body: w×h cells, a box like
// the Space view's.
func (m *Model) attentionBox(w, h int) []string {
	inner := w - 4
	bodyH := h - 2
	now := time.Now()
	st := m.st
	at := &m.at

	lines, heads := m.attnLines(inner, now)
	at.heads = heads
	rows := m.attnRows()
	cur := m.attnCursor(rows)
	listH := max(bodyH-1, 3)
	at.listH = listH

	curLine := -1
	if cur >= 0 {
		for i, l := range lines {
			if l.row != nil && l.row.key == rows[cur].key {
				curLine = i
				break
			}
		}
	}
	maxOff := max(len(lines)-listH, 0)
	if at.toHead && at.block < len(heads) {
		at.off = heads[at.block]
	} else if at.follow && curLine >= 0 {
		at.off = clamp(at.off, curLine-listH+1, curLine)
		// The block's heading stays in view with its first rows.
		if head := heads[rows[cur].block]; curLine-head < listH && at.off > head {
			at.off = head
		}
		if cur == 0 && curLine < listH {
			at.off = 0
		}
	}
	at.toHead, at.follow = false, false
	at.off = clamp(at.off, 0, maxOff)
	at.onScreen = curLine >= at.off && curLine < at.off+listH

	var body []string
	for i := at.off; i < len(lines) && len(body) < listH; i++ {
		l := lines[i]
		text := l.text
		if l.row != nil {
			m.recordRow(len(body), hitAttnRow, paneNone, 0, 0, l.row.key)
		}
		if l.row != nil && curLine == i {
			text = st.selected.Render("▸ " + fit(strings.TrimPrefix(ansi.Strip(text), "  "), inner-2))
		}
		body = append(body, text)
	}
	for len(body) < listH {
		body = append(body, "")
	}
	pos := fmt.Sprintf(" lines %d–%d of %d ", min(at.off+1, len(lines)), min(at.off+listH, len(lines)), len(lines))
	body = append(body, st.tone(toneDim, strings.Repeat("─", max(inner-len([]rune(pos))-1, 0))+pos+"─"))

	padded := make([]string, 0, len(body))
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner)+" ")
	}

	tot := m.attnTotals()
	title := "Attention — "
	switch {
	case tot.read == 0 && tot.reading:
		title += "reading"
	case tot.read == 0:
		title += "nothing read"
	case tot.clear:
		title += "nothing needs you"
	default:
		if tot.needs == 1 {
			title += "1 needs you"
		} else {
			title += fmt.Sprintf("%d need you", tot.needs)
		}
		title += fmt.Sprintf(" · %d to watch", tot.watch)
	}
	if tot.read > 0 {
		title += " · " + countNoun(tot.containers, "container", "containers")
		switch {
		case tot.total > 1 && tot.read < tot.total:
			title += fmt.Sprintf(" on %d of %d daemons", tot.read, tot.total)
		case tot.total > 1:
			title += fmt.Sprintf(" on %d daemons", tot.total)
		}
	}
	right := ""
	switch {
	case tot.reading:
		right = spinnerFrames[m.spinner] + " reading"
	case !tot.oldest.IsZero() && tot.read > 1:
		right = "oldest read " + tot.oldest.Local().Format("15:04:05")
	case !tot.oldest.IsZero():
		right = "read " + tot.oldest.Local().Format("15:04:05")
	}
	return st.box(title, right, padded, w, h, true)
}

func (m *Model) attentionHints() []string {
	pairs := []string{"↑↓", "move", "⏎", "go to container"}
	if len(m.at.blocks) > 1 {
		pairs = append(pairs, "Tab", "next daemon")
	}
	return append(pairs, "r", "read again")
}

// attentionCue is the ambient count: the containers of the focused daemon
// that the list alone says need a person — dead, restarting, unhealthy. It
// is a subset of what the view finds, and says so wherever it is shown.
func (m *Model) attentionCue() int {
	if !m.loaded[listContainers] {
		return 0
	}
	n := 0
	for _, c := range m.snap.Containers {
		if attention.Certain(attention.Container{State: c.State, Health: c.Health}) {
			n++
		}
	}
	return n
}

// cueTitle is the cue as the Containers title carries it, in the longest
// form that leaves room in a title of the given width. Every form names H:
// the count is of the certain signals only, and the view reads the rest.
func cueTitle(n, room int) string {
	if n <= 0 {
		return ""
	}
	verb := "need"
	if n == 1 {
		verb = "needs"
	}
	for _, s := range []string{
		fmt.Sprintf(" · %d %s attention — %s reads exit codes and restarts too", n, verb, keyAttention),
		fmt.Sprintf(" · %d %s attention — %s reads exit codes, restarts", n, verb, keyAttention),
		fmt.Sprintf(" · %d %s attention, %s reads more", n, verb, keyAttention),
		fmt.Sprintf(" · ▲%d+ %s", n, keyAttention),
	} {
		if ansi.StringWidth(s) <= room {
			return s
		}
	}
	return ""
}
