package ui

import (
	"fmt"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
)

// ---- Events pane -----------------------------------------------------------------------

// eventTone marks events that mean failure or destruction.
func eventTone(e engine.Event) tone {
	action := e.Action
	if i := strings.IndexByte(action, ':'); i >= 0 {
		action = action[:i] // "exec_start: sh" → "exec_start"
	}
	switch action {
	case "die":
		if code := e.Attributes["exitCode"]; code != "" && code != "0" {
			return toneBad
		}
		return toneWarn
	case "oom", "kill", "destroy", "delete", "remove", "untag":
		return toneBad
	case "stop", "pause", "disconnect", "unmount":
		return toneWarn
	case "start", "create", "pull", "unpause", "connect", "restart":
		return toneGood
	case "exec_create", "exec_start", "exec_die", "health_status", "mount", "top", "resize", "attach", "detach":
		return toneDim
	}
	return toneNormal
}

// eventKinds abbreviates the object type so that the name and outcome fit
// in a narrow pane.
var eventKinds = map[string]string{"container": "ctr", "image": "img", "volume": "vol", "network": "net"}

// formatEvent renders one event as: time, kind, action (with its outcome),
// object. The outcome — exit code, signal — sits right after the action so
// that it is never the part cut off by a narrow pane. nameOf resolves a
// container ID to its name when the desk knows it.
func formatEvent(e live.EventEntry, st styles, w int, nameOf func(id string) string) string {
	ts := e.Time.Local().Format("15:04:05")
	if e.Marker != "" {
		return st.tone(toneWarn, fit(ts+" ── "+e.Marker+" ──", w))
	}
	ev := e.Event
	name := ev.Name()
	extra := ""
	action := ev.Action
	if i := strings.IndexByte(action, ':'); i >= 0 {
		extra, action = strings.TrimSpace(action[i+1:]), action[:i]
	}
	switch ev.Type {
	case "container":
		switch action {
		case "die", "exec_die":
			action += " exit " + ev.Attributes["exitCode"]
		case "kill":
			action += " sig " + ev.Attributes["signal"]
		}
	case "network":
		if c := ev.Attributes["container"]; c != "" {
			extra = "← " + nameOf(c)
		}
	case "image":
		if n := ev.Attributes["name"]; n != "" {
			name = n
		} else {
			name = engine.ShortID(ev.ID)
		}
	case "volume":
		name = ev.ID
		if len(name) == 64 {
			name = name[:12] + "…"
		}
	}
	kind, ok := eventKinds[ev.Type]
	if !ok {
		kind = ev.Type
		if len(kind) > 3 {
			kind = kind[:3]
		}
	}
	line := fmt.Sprintf("%s %-3s %-10s %s", ts, kind, action, name)
	if extra != "" {
		line += "  " + extra
	}
	return st.tone(eventTone(ev), fit(line, w))
}

func (m *Model) renderEvents(w, h int) []string {
	m.eventHeight = h
	first, end := m.hub.Events.Bounds()
	viewEnd := end
	if !m.evFollow {
		viewEnd = m.evEnd
		if viewEnd > end {
			viewEnd = end
		}
		if viewEnd < first+1 && end > first {
			viewEnd = first + 1
		}
		m.evEnd = viewEnd
	}
	from := first
	if viewEnd > uint64(h) && viewEnd-uint64(h) > first {
		from = viewEnd - uint64(h)
	}
	entries, _ := m.hub.Events.Read(from, viewEnd)
	if len(entries) == 0 {
		return []string{
			m.st.tone(toneDim, "Waiting for engine events…"),
			m.st.tone(toneDim, "Everything the daemon does"),
			m.st.tone(toneDim, "shows up here as it happens."),
		}
	}
	names := make(map[string]string, len(m.snap.Containers))
	for _, c := range m.snap.Containers {
		names[c.ID] = c.Name
	}
	nameOf := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		return engine.ShortID(id)
	}
	out := make([]string, 0, h)
	for _, e := range entries {
		out = append(out, formatEvent(e, m.st, w, nameOf))
	}
	return out
}

func (m *Model) eventsTitle() (title, right string) {
	title = "4 Events"
	_, end := m.hub.Events.Bounds()
	switch {
	case !m.hub.Connected():
		right = "✗ disconnected"
	case !m.evFollow:
		right = fmt.Sprintf("‖ +%d newer", end-m.evEnd)
	default:
		right = "● live"
	}
	return title, right
}

func (m *Model) scrollEvents(delta int) {
	first, end := m.hub.Events.Bounds()
	cur := end
	if !m.evFollow {
		cur = m.evEnd
	}
	next := int64(cur) + int64(delta)
	if next < int64(first)+1 {
		next = int64(first) + 1
	}
	if next >= int64(end) {
		m.evFollow = true
		return
	}
	m.evFollow = false
	m.evEnd = uint64(next)
}

// ---- Jobs pane -------------------------------------------------------------------------

func jobGlyph(j jobs.Job, spinner string) (string, tone) {
	switch j.State {
	case jobs.Running:
		return spinner, toneAccent
	case jobs.Succeeded:
		return "✓", toneGood
	case jobs.Failed:
		return "✗", toneBad
	case jobs.Cancelled:
		return "⊘", toneWarn
	}
	return "?", toneNormal
}

func jobProgress(j jobs.Job) string {
	switch {
	case j.State != jobs.Running:
		return humanDuration(j.Elapsed())
	case j.Total > 0:
		return fmt.Sprintf("%d/%d", j.Done, j.Total)
	case j.Fraction >= 0:
		return fmt.Sprintf("%3.0f%%", j.Fraction*100)
	default:
		return humanDuration(j.Elapsed())
	}
}

// selectedJob returns the highlighted job: the one last chosen, or the newest.
func (m *Model) selectedJob(list []jobs.Job) (idx int, ok bool) {
	if len(list) == 0 {
		return 0, false
	}
	for i, j := range list {
		if j.ID == m.jobSel {
			return i, true
		}
	}
	return 0, true
}

func (m *Model) moveJob(delta int) {
	list := m.jobs.List()
	if len(list) == 0 {
		return
	}
	i, _ := m.selectedJob(list)
	i = clamp(i+delta, 0, len(list)-1)
	m.jobSel = list[i].ID
}

func (m *Model) renderJobs(w, h int, focused bool) []string {
	m.jobHeight = h
	list := m.jobs.List()
	if len(list) == 0 {
		return []string{
			m.st.tone(toneDim, "No jobs yet."),
			m.st.tone(toneDim, "Everything that changes the daemon"),
			m.st.tone(toneDim, "runs here, in the background."),
		}
	}
	sel, _ := m.selectedJob(list)
	off := 0
	if sel >= h {
		off = sel - h + 1
	}
	out := make([]string, 0, h)
	for i := off; i < len(list) && len(out) < h; i++ {
		j := list[i]
		glyph, t := jobGlyph(j, spinnerFrames[m.spinner])
		prog := jobProgress(j)
		head := j.Title
		if j.Target != "" {
			head += " " + j.Target
		}
		detail := j.Status
		if j.State == jobs.Failed {
			detail = j.Err
		}
		text := glyph + " " + head
		room := w - 2 - len([]rune(prog)) - 1
		line := fit(text, room) + " " + prog
		if detail != "" && j.State != jobs.Succeeded {
			// Use what is left of the head's room for the status or error.
			if used := len([]rune(text)); used+3 < room {
				line = fit(text+" — "+detail, room) + " " + prog
			}
		}
		switch {
		case i == sel && focused:
			out = append(out, m.st.selected.Render("▸ "+line))
		case i == sel:
			out = append(out, "▸ "+m.st.tone(t, line))
		default:
			out = append(out, "  "+m.st.tone(t, line))
		}
	}
	return out
}

func (m *Model) jobsTitle() (title, right string) {
	title = "5 Jobs"
	list := m.jobs.List()
	running, failed := 0, 0
	for _, j := range list {
		switch j.State {
		case jobs.Running:
			running++
		case jobs.Failed:
			failed++
		}
	}
	switch {
	case running > 0:
		right = fmt.Sprintf("%s %d running", spinnerFrames[m.spinner], running)
	case failed > 0:
		right = fmt.Sprintf("%d failed", failed)
	case len(list) > 0:
		right = fmt.Sprintf("%d done", len(list))
	}
	return title, right
}

// jobOutput renders a job's full output for the output viewer.
func jobOutput(j jobs.Job, st styles) []string {
	glyph, t := jobGlyph(j, "…")
	lines := []string{
		st.tone(t, glyph+" "+j.State.String()) + "   " + j.Title + " " + j.Target,
		st.tone(toneDim, fmt.Sprintf("started %s · ran %s", j.Started.Local().Format("15:04:05"), humanDuration(j.Elapsed()))),
	}
	if j.Total > 0 {
		lines = append(lines, st.tone(toneDim, fmt.Sprintf("%d of %d objects processed", j.Done, j.Total)))
	}
	lines = append(lines, "")
	if j.LogDropped > 0 {
		lines = append(lines, st.tone(toneDim, fmt.Sprintf("… %d earlier lines not kept …", j.LogDropped)))
	}
	if len(j.Log) == 0 && j.Status != "" {
		lines = append(lines, j.Status)
	}
	lines = append(lines, j.Log...)
	if j.State == jobs.Running && j.Status != "" {
		lines = append(lines, "", st.tone(toneAccent, "now: "+j.Status))
	}
	if j.Err != "" {
		lines = append(lines, "", st.tone(toneBad, "Error: "+j.Err))
	}
	return lines
}
