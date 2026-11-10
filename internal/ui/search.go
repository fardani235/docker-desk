package ui

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/keypath"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/search"
)

// Search in the Details pane (docs/DECISIONS.md, Q1–Q12).
//
// One key, `/`, and one bar, in the pane's last content row. Where the text
// is a document — Info, Inspect, Processes, Changes — the bar finds: every
// line stays, matches are marked, Enter, n and N move between them. Where it
// is a stream — Logs — the bar filters: lines that do not match are hidden,
// and following and scrolling back work inside what is left. Nothing here
// sends a request or writes anything.

// detailSearch is the bar's state. Queries are kept per tab, in the tab's
// own view (textView.find) or here; all are dropped when the highlighted
// object changes.
type detailSearch struct {
	typing bool
	in     textinput.Model
	logQ   string            // the Logs filter
	other  map[string]string // tabs with nothing to search (Stats, Findings)
	lf     logFilter

	// Inspect key paths, kept for the inspect text they were derived from.
	paths    []string
	pathsVer uint64
}

// textFind is a find over a textView's source lines.
type textFind struct {
	query string
	cur   int  // index into matches of the current match
	pick  bool // choose cur afresh: the first match at or below the top row
	step  int  // pending n (+1) and N (−1) presses
	seek  bool // pending: bring the current match into view
	mk    search.Marks

	matches []textMatch
	memoVer uint64 // view version and query the matches were found for
	memoQ   string
	memoOK  bool
}

type textMatch struct {
	line int
	search.Range
}

// setQuery changes the query; the current match is chosen again on the next
// render, from where the view is.
func (f *textFind) setQuery(q string) {
	if q == f.query {
		return
	}
	f.query, f.pick, f.step, f.seek = q, true, 0, false
}

func (f *textFind) clear() {
	f.query, f.cur, f.pick, f.step, f.seek = "", 0, false, 0, false
}

// findMatches returns the view's matches, found again only when its lines
// or the query changed since the last time.
func (v *textView) findMatches() []textMatch {
	f := &v.find
	if f.query == "" {
		return nil
	}
	if f.memoOK && f.memoVer == v.ver && f.memoQ == f.query {
		return f.matches
	}
	f.matches = f.matches[:0]
	for i, l := range v.lines {
		for _, r := range search.Find(search.Strip(l), f.query) {
			f.matches = append(f.matches, textMatch{line: i, Range: r})
		}
	}
	f.memoVer, f.memoQ, f.memoOK = v.ver, f.query, true
	return f.matches
}

// rowOf is the wrapped row holding the start of a match. Only valid after
// layout.
func (v *textView) rowOf(m textMatch) int {
	if m.line >= len(v.lineRow) {
		return 0
	}
	r := v.lineRow[m.line]
	for r+1 < len(v.rowLine) && v.rowLine[r+1] == m.line && v.rowStart[r+1] <= m.Start {
		r++
	}
	return r
}

// resolveFind settles what the keys asked for, now that the rows exist for
// this width: which match is current, and — for a pending seek — the
// offset that brings it into view. A match already on screen is not moved;
// one off screen is placed a third of the way down, so that the lines above
// it, which say what it belongs to, are in view too.
func (v *textView) resolveFind(h int) {
	f := &v.find
	ms := v.findMatches()
	if len(ms) == 0 {
		f.cur, f.step, f.seek = 0, 0, false
		return
	}
	if f.pick {
		top := clamp(v.off, 0, max(len(v.wrapped)-h, 0))
		f.cur = 0
		for i, m := range ms {
			if v.rowOf(m) >= top {
				f.cur = i
				break
			}
		}
		f.pick = false
	}
	f.cur = clamp(f.cur, 0, len(ms)-1)
	if f.step != 0 {
		f.cur = ((f.cur+f.step)%len(ms) + len(ms)) % len(ms)
		f.step = 0
	}
	if f.seek {
		row := v.rowOf(ms[f.cur])
		top := clamp(v.off, 0, max(len(v.wrapped)-h, 0))
		if row < top || row >= top+h {
			v.off = max(row-h/3, 0)
		}
		f.seek = false
	}
}

// markRows returns rows [from, to) with the matches on them marked. The
// marks are applied to each wrapped row, with the row's own part of each
// match, so that a match broken across two rows is marked on both.
func (v *textView) markRows(from, to int) []string {
	rows := v.wrapped[from:to]
	f := &v.find
	ms := v.findMatches()
	if len(ms) == 0 || f.mk.None() {
		return rows
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		r := from + i
		line, start := v.rowLine[r], v.rowStart[r]
		end := start + len(search.Strip(row))
		j := sort.Search(len(ms), func(k int) bool { return ms[k].line >= line })
		var rs []search.Range
		cur := -1
		for ; j < len(ms) && ms[j].line == line; j++ {
			m := ms[j]
			if m.End <= start || m.Start >= end {
				continue
			}
			if j == f.cur {
				cur = len(rs)
			}
			rs = append(rs, search.Range{Start: max(m.Start, start) - start, End: min(m.End, end) - start})
		}
		out[i] = search.Mark(row, rs, f.mk, cur)
	}
	return out
}

// ---- which tab holds what ----------------------------------------------------------

// tabView is the text view of the current tab, or nil where there is none.
func (m *Model) tabView() *textView {
	switch m.tabName() {
	case tabInfo:
		return &m.info1
	case tabInspect:
		return &m.inspect
	case tabProcesses:
		return &m.proc.view
	case tabChanges:
		return &m.chg.view
	case tabDeclared:
		return &m.dc.view
	}
	return nil
}

func (m *Model) tabQuery() string {
	if v := m.tabView(); v != nil {
		return v.find.query
	}
	if m.tabName() == tabLogs {
		return m.ds.logQ
	}
	return m.ds.other[m.tabName()]
}

func (m *Model) setTabQuery(q string) {
	if v := m.tabView(); v != nil {
		v.find.setQuery(q)
		return
	}
	if m.tabName() == tabLogs {
		m.ds.logQ = q
		return
	}
	if m.ds.other == nil {
		m.ds.other = map[string]string{}
	}
	m.ds.other[m.tabName()] = q
}

// searchOpen reports whether the bar is on screen for the current tab.
func (m *Model) searchOpen() bool { return m.ds.typing || m.tabQuery() != "" }

// clearSearches drops every query: the highlighted object changed. If the
// bar is being typed in it stays open, empty — the next key typed is still
// text, never an action.
func (m *Model) clearSearches() {
	for _, v := range []*textView{&m.info1, &m.inspect, &m.proc.view, &m.chg.view, &m.dc.view} {
		v.find.clear()
	}
	m.ds.logQ = ""
	m.ds.other = nil
	if m.ds.typing {
		m.ds.in.SetValue("")
	}
}

// ---- keys --------------------------------------------------------------------------

// openSearch is `/` in the Details pane: the bar opens with the tab's query,
// if it has one, to edit.
func (m *Model) openSearch() {
	m.ds.typing = true
	m.ds.in.Prompt = "find: "
	if m.tabName() == tabLogs {
		m.ds.in.Prompt = "filter: "
	}
	m.ds.in.SetValue(m.tabQuery())
	m.ds.in.CursorEnd()
	m.ds.in.Focus()
	if v := m.tabView(); v != nil {
		v.find.pick = true
	}
}

// onSearchKey edits the query. Every key ends here while the bar is typed
// in — none reaches a pane, a global key or an action. Enter keeps the
// query and jumps to the current match; Esc clears it.
func (m *Model) onSearchKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		m.ds.typing = false
		m.ds.in.Blur()
		if v := m.tabView(); v != nil && v.find.query != "" {
			v.find.seek = true
		}
		return nil
	case "esc":
		m.ds.typing = false
		m.ds.in.Blur()
		m.setTabQuery("")
		return nil
	}
	var cmd tea.Cmd
	m.ds.in, cmd = m.ds.in.Update(k)
	m.setTabQuery(m.ds.in.Value())
	return cmd
}

// onSearchOpenKey handles the keys that mean something else while a query is
// kept: n and N step through the matches, Esc clears. It reports whether the
// key was one of them. n shadows "new volume" and "new network" only here.
func (m *Model) onSearchOpenKey(key string) bool {
	if m.tabQuery() == "" {
		return false
	}
	switch key {
	case "n", "N":
		if v := m.tabView(); v != nil {
			if key == "n" {
				v.find.step++
			} else {
				v.find.step--
			}
			v.find.seek = true
		}
		return true // on Logs, Stats and Findings: nothing to step through
	case "esc":
		m.setTabQuery("")
		return true
	}
	return false
}

// ---- the bar -----------------------------------------------------------------------

// findBar is the last row of a tab that finds.
func (m *Model) findBar(w int) string {
	st := m.st
	q := m.tabQuery()
	left := "find: " + q
	if m.ds.typing {
		left = m.ds.in.View()
	}
	v := m.tabView()
	var right string
	t := toneDim
	switch {
	case v == nil:
		right, t = "nothing to search on "+m.tabName(), toneWarn
	case q == "":
	default:
		ms := v.findMatches()
		if len(ms) == 0 {
			right, t = fmt.Sprintf("no match in the %s", plural(len(v.lines), "line")), toneWarn
			break
		}
		right = fmt.Sprintf("%d of %d", v.find.cur+1, len(ms))
		if m.tabName() == tabInspect {
			if p := m.inspectPath(ms[v.find.cur].line); p != "" {
				right += "  at " + p
			}
		}
	}
	if right == "" {
		return fit(left, w)
	}
	return fit(left+"  "+st.tone(t, right), w)
}

// inspectPath is the key path of an inspect line, derived once per document.
func (m *Model) inspectPath(line int) string {
	if m.ds.pathsVer != m.inspect.ver || m.ds.paths == nil {
		m.ds.paths = keypath.Paths(m.inspect.lines, 2)
		m.ds.pathsVer = m.inspect.ver
	}
	if line < 0 || line >= len(m.ds.paths) {
		return ""
	}
	return m.ds.paths[line]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// ---- Logs: filter ------------------------------------------------------------------

// logFilter holds the absolute ring indices of the lines that match, for one
// ring and one query. Each frame tests only the lines that arrived since the
// last; a new query, or a new ring (another container), starts again.
type logFilter struct {
	ring    *live.Ring[string]
	query   string
	scanned uint64   // lines before this absolute index have been tested
	idx     []uint64 // matching lines, ascending
}

// logMatches brings the filter up to date with the ring and returns it. The
// work is the lines that arrived since the last call, never more than the
// ring holds.
func (m *Model) logMatches(ring *live.Ring[string], q string) []uint64 {
	f := &m.ds.lf
	first, end := ring.Bounds()
	if f.ring != ring || f.query != q || end < f.scanned {
		f.ring, f.query, f.scanned, f.idx = ring, q, first, f.idx[:0]
	}
	// Lines that left the ring leave the filter.
	if k := sort.Search(len(f.idx), func(i int) bool { return f.idx[i] >= first }); k > 0 {
		f.idx = f.idx[:copy(f.idx, f.idx[k:])]
	}
	if f.scanned < first {
		f.scanned = first
	}
	if f.scanned < end {
		lines, from := ring.Read(f.scanned, end)
		for i, l := range lines {
			if search.Contains(l, q) {
				f.idx = append(f.idx, from+uint64(i))
			}
		}
		f.scanned = from + uint64(len(lines))
	}
	return f.idx
}

// filteredLogLines returns the matching lines that end before viewEnd, at
// most n of them, oldest first: only those are copied out of the ring.
func filteredLogLines(ring *live.Ring[string], idx []uint64, viewEnd uint64, n int) []string {
	k := sort.Search(len(idx), func(i int) bool { return idx[i] >= viewEnd })
	from := max(k-n, 0)
	out := make([]string, 0, k-from)
	for _, a := range idx[from:k] {
		if l, _ := ring.Read(a, a+1); len(l) == 1 {
			out = append(out, l[0])
		}
	}
	return out
}

// scrollFilteredLogs moves through matching lines only. The bottom of the
// view is a matching line; going past the newest one follows again.
func (m *Model) scrollFilteredLogs(ring *live.Ring[string], delta int) {
	idx := m.logMatches(ring, m.ds.logQ)
	if len(idx) == 0 {
		return
	}
	_, end := ring.Bounds()
	cur := end
	if !m.logFollow {
		cur = m.logEnd
	}
	shown := sort.Search(len(idx), func(i int) bool { return idx[i] >= cur })
	bottom := max(shown-1+delta, 0)
	if bottom >= len(idx)-1 {
		if delta > 0 {
			m.logFollow = true
			return
		}
		bottom = len(idx) - 1
	}
	m.logFollow = false
	m.logEnd = idx[bottom] + 1
}

// logRows wraps one log line for the pane and marks the query in it.
func (m *Model) logRows(line string, w int) []string {
	parts := []string{line}
	if m.logWrap {
		parts = wrapLine(line, w)
	}
	q, mk := m.ds.logQ, m.st.mark
	if q == "" || mk.None() {
		return parts
	}
	rs := search.Find(search.Strip(line), q)
	if len(rs) == 0 {
		return parts
	}
	at := 0
	for i, p := range parts {
		n := len(search.Strip(p))
		parts[i] = search.Mark(p, search.Clip(rs, at, at+n), mk, -1)
		at += n
	}
	return parts
}

// logFilterStatus is the Logs status row while a filter is set or typed.
func (m *Model) logFilterStatus(state string, matched int, held uint64) (string, bool) {
	left := "filter: " + m.ds.logQ
	if m.ds.typing {
		left = m.ds.in.View()
	}
	if m.ds.logQ == "" {
		return left + "  " + state, false
	}
	if matched == 0 {
		return left + "  no match in the " + plural(int(held), "line") + " held", true
	}
	return left + "  " + state + fmt.Sprintf(" · %d of %d match", matched, held), false
}

// ---- hints -------------------------------------------------------------------------

func (m *Model) searchHints(add func(k, v string)) {
	logs := m.tabName() == tabLogs
	switch {
	case m.ds.typing && logs:
		add("type", "to filter")
		add("Enter", "keep")
		add("Esc", "clear")
	case m.ds.typing:
		add("type", "to find")
		add("Enter", "jump")
		add("Esc", "clear")
	case m.tabQuery() != "" && m.tabView() != nil:
		add("n N", "next / prev")
		add("Esc", "clear find")
	case m.tabQuery() != "":
		add("Esc", "clear "+map[bool]string{true: "filter", false: "find"}[logs])
	case logs:
		add("/", "filter")
	default:
		add("/", "find")
	}
}

// shadowed reports whether an action key means something else in the
// Details pane right now.
func (m *Model) shadowed(key string) bool {
	return m.focus == PaneDetails && m.tabQuery() != "" && (key == "n" || key == "N")
}
