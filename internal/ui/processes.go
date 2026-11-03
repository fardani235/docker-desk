package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
)

const tabProcesses = "Processes"

// procState is the process snapshot of the highlighted container. It is a
// manual snapshot: fetched when the Processes tab is entered, when the
// highlight moves to another container while the tab is shown, and on r —
// never on a timer. It is separate from detailState so that a late /top
// answer and a late inspect answer cannot overwrite each other.
type procState struct {
	target  string // container ID the request and rows belong to
	seq     uint64
	loading bool
	// valid is false when the rows (if any) must be fetched again before
	// they can be trusted: never fetched, refreshed, or re-entered.
	valid   bool
	stopped bool   // the daemon refused: the container is not running
	err     string // any other failure, shown in the pane
	stale   bool   // the daemon could not be reached after rows were shown
	list    *engine.ProcessList
	at      time.Time // when list was captured
	view    textView  // process rows, for vertical scrolling
	visible bool      // the tab was drawn on the last reconciliation
}

type procMsg struct {
	target string
	seq    uint64
	list   engine.ProcessList
	err    error
}

func (m *Model) processesVisible() bool {
	return m.kind == KindContainers && m.tabName() == tabProcesses
}

// clearProcesses drops every row and invalidates any request in flight, so
// that nothing captured for one container is ever drawn under another.
func (m *Model) clearProcesses(target string) {
	p := &m.proc
	p.target, p.loading, p.valid, p.stopped, p.stale = target, false, false, false, false
	p.err, p.list, p.at = "", nil, time.Time{}
	p.seq++
	p.view.set(nil)
	p.view.off = 0
}

// invalidateProcesses marks the snapshot as needing a fresh fetch without
// requesting one; the next reconciliation with the tab visible does.
func (m *Model) invalidateProcesses() { m.proc.valid = false }

// syncProcesses decides whether a process request is needed now. It is
// called once the highlight has settled.
func (m *Model) syncProcesses() tea.Cmd {
	vis := m.processesVisible()
	entered := vis && !m.proc.visible
	m.proc.visible = vis
	if !vis {
		return nil
	}
	if entered {
		m.invalidateProcesses()
	}
	r, ok := m.currentRow()
	if !ok {
		return nil
	}
	if m.proc.target != r.containerID {
		m.clearProcesses(r.containerID)
	}
	if r.state != engine.StateRunning {
		// Rows taken while it ran describe processes that are gone.
		if m.proc.list != nil || m.proc.loading || m.proc.err != "" {
			m.clearProcesses(r.containerID)
		}
		return nil
	}
	if m.proc.valid || m.proc.loading {
		return nil
	}
	return m.loadProcesses(r.containerID)
}

func (m *Model) loadProcesses(id string) tea.Cmd {
	p := &m.proc
	p.loading = true
	p.seq++
	seq, eng := p.seq, m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := eng.Processes(ctx, id)
		return procMsg{target: id, seq: seq, list: list, err: err}
	}
}

func (m *Model) onProcesses(msg procMsg) {
	p := &m.proc
	if msg.target != p.target || msg.seq != p.seq {
		return // for another container, or overtaken by a newer request
	}
	p.loading, p.valid = false, true
	switch {
	case msg.err == nil:
		p.stopped, p.stale, p.err = false, false, ""
		list := msg.list
		p.list, p.at = &list, time.Now()
	case engine.IsConflict(msg.err):
		// It stopped between the list refresh and the request.
		p.stopped, p.err, p.list, p.stale = true, "", nil, false
		p.view.set(nil)
	case engine.IsUnavailable(msg.err) && p.list != nil:
		p.stale = true // keep the last snapshot, marked as such
	default:
		p.err, p.list, p.stale, p.stopped = plainError(msg.err), nil, false, false
		p.view.set(nil)
		if engine.IsNotFound(msg.err) {
			p.err = "This container no longer exists."
		}
	}
}

// renderProcesses draws the snapshot as a single-line-per-process table: a
// header row, the rows that fit, and a status row.
func (m *Model) renderProcesses(w, h int) []string {
	st := m.st
	r, ok := m.currentRow()
	if !ok {
		return nil
	}
	p := &m.proc
	if r.state != engine.StateRunning {
		return []string{
			st.tone(toneDim, fit(fmt.Sprintf("%s is not running (%s).", r.name, r.state), w)),
			st.tone(toneDim, fit("Processes are available only for running containers.", w)),
		}
	}
	if p.target != r.containerID {
		return []string{st.tone(toneDim, fit("Loading process list…", w))}
	}
	switch {
	case p.stopped:
		return []string{
			st.tone(toneWarn, fit(r.name+" is no longer running.", w)),
			st.tone(toneDim, fit("Processes are available only for running containers.", w)),
		}
	case p.err != "":
		out := wrapLine(st.tone(toneBad, "Process listing failed: "+p.err), w)
		return append(out, st.tone(toneDim, fit("r tries again.", w)))
	case p.list == nil:
		return []string{st.tone(toneDim, fit("Loading process list…", w))}
	}

	header, rows := processTable(p.list, w)
	bodyH := max(h-2, 1)
	p.view.set(rows)
	body := p.view.render(w, bodyH)
	out := make([]string, 0, h)
	out = append(out, st.tone(toneAccent, header))
	if len(rows) == 0 {
		out = append(out, st.tone(toneDim, fit("No processes were reported.", w)))
	}
	out = append(out, body...)
	for len(out) < h-1 {
		out = append(out, "")
	}

	status := fmt.Sprintf("snapshot at %s · %d processes", p.at.Format("15:04:05"), len(rows))
	if pos := p.view.position(bodyH); pos != "" {
		status += " · " + pos
	}
	t := toneDim
	switch {
	case p.stale || !m.hub.Connected():
		status, t = "■ stale: disconnected from the daemon · "+status, toneWarn
	case p.loading:
		status = "refreshing… · " + status
	default:
		status += " · r takes a new one"
	}
	return append(out, st.tone(t, fit(status, w)))
}

// processTable lays out the daemon's titles and rows in w cells. Column
// names and count are whatever the daemon sent. A row with fewer cells than
// titles leaves the rest empty; extra cells are joined into the last column
// so nothing is silently dropped. Every row is exactly one line.
func processTable(list *engine.ProcessList, w int) (string, []string) {
	cols := len(list.Titles)
	if cols == 0 {
		for _, r := range list.Rows {
			cols = max(cols, len(r))
		}
	}
	if cols == 0 {
		return fit("", w), nil
	}
	cell := func(row []string, i int) string {
		switch {
		case i < cols-1 && i < len(row):
			return oneLine(row[i])
		case i == cols-1 && i < len(row):
			return oneLine(strings.Join(row[i:], " "))
		}
		return ""
	}
	titles := make([]string, cols)
	for i := range titles {
		titles[i] = cell(list.Titles, i)
	}
	const gap, capW = 1, 24
	widths := make([]int, cols)
	for i := range widths {
		widths[i] = ansi.StringWidth(titles[i])
		for _, r := range list.Rows {
			widths[i] = max(widths[i], ansi.StringWidth(cell(r, i)))
		}
		if i < cols-1 {
			widths[i] = min(widths[i], capW)
		}
	}
	// The last column (the command, for ps) takes whatever is left.
	used := 0
	for i := 0; i < cols-1; i++ {
		used += widths[i] + gap
	}
	widths[cols-1] = max(w-used, 1)

	line := func(cells func(int) string) string {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			if i > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}
			b.WriteString(fit(cells(i), widths[i]))
		}
		return fit(b.String(), w)
	}
	header := line(func(i int) string { return titles[i] })
	rows := make([]string, 0, len(list.Rows))
	for _, r := range list.Rows {
		rows = append(rows, line(func(i int) string { return cell(r, i) }))
	}
	return header, rows
}

// oneLine makes a daemon-supplied value safe to draw in one row: escape
// sequences are removed and control characters become spaces. The text is
// otherwise shown as the daemon sent it — no argument is masked.
func oneLine(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}
