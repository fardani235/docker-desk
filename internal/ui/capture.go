package ui

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/capture"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/search"
)

// Capture: `W` writes the text the focused pane holds to one local file
// (docs/DECISIONS.md, PC1–PC6; OpenSpec change capture-pane-text).
//
// Each pane hands over its own source — the lines it holds, or the builder
// that made its lines, run again at a page's width — never the drawn frame,
// which is clipped, styled and bound to the terminal's width. The text is
// collected when W is pressed, as an export's document is, so nothing that
// moves behind the dialog changes what is written; package capture makes it
// plain. The dialog, the consent and the writer are the export's
// (export.go): one path, shown absolute, `y` to write, 0600, never a
// replacement, the folder must exist. No request is sent: a capture works
// in read-only mode, and writes nothing until `y`.
//
// A capture is not a report. It keeps the daemon's text as the desk holds
// it — times, environment values — and the dialog says so before consent.

// paneCapture is what W collected.
type paneCapture struct {
	kind   string // the prefilled name's: info, inspect, logs, …
	pane   string // as the desk names it: "Logs", "Events", …
	object string // the object, in words: text
	slug   string // the object, for the name
	format report.Format
	holds  string // one sentence: what the file holds
	class  string // one sentence: what class of file this is
	data   []byte
}

const (
	classText    = "A capture: the text as the desk holds it, colour removed — times and the daemon's values included. It is not a report."
	classInspect = "A capture: the daemon's inspect document as it sent it, environment variable values included. It is not a report."
)

// openCapture opens the export dialog on a collected capture.
func (m *Model) openCapture(c *paneCapture) {
	in := newInput("", 4096)
	in.SetValue(report.DefaultPath(c.kind, c.slug, c.format, m.now()))
	in.CursorEnd()
	m.dlg = &dialog{kind: dlgExport, hasInput: true, openedAt: time.Now(),
		exp: &exportDialog{kind: c.kind, object: c.object, formats: []report.Format{c.format}, input: in, capture: c}}
}

// nothingToSave says, in one sentence, why W wrote nothing.
func (m *Model) nothingToSave(why string) {
	m.setStatus(toneDim, "Nothing to save: "+why)
}

func (m *Model) captureHeader(pane, object string, facts ...string) capture.Header {
	tool := "docker-desk"
	if v := strings.TrimSpace(m.opts.Version); v != "" {
		tool += " " + v
	}
	return capture.Header{Tool: tool, Pane: pane, Object: object, Facts: facts}
}

func (m *Model) textCapture(kind, pane, object, slug, holds string, lines []string, facts ...string) *paneCapture {
	return &paneCapture{
		kind: kind, pane: pane, object: object, slug: slug,
		format: report.Text, holds: holds, class: classText,
		data: capture.Text(m.captureHeader(pane, object, facts...), lines),
	}
}

// ---- the keys ------------------------------------------------------------------------

// captureDetails is W in the Details pane. Stats, Findings and Upstream are
// not text: W does nothing there, and nothing else either.
func (m *Model) captureDetails() {
	r, ok := m.currentRow()
	if !ok {
		return
	}
	var c *paneCapture
	switch m.tabName() {
	case tabInfo:
		c = m.captureInfo(r)
	case tabInspect:
		c = m.captureInspect(r)
	case tabLogs:
		c = m.captureLogs(r)
	case tabProcesses:
		c = m.captureProcesses(r)
	case tabChanges:
		c = m.captureChanges(r)
	case tabDeclared:
		c = m.captureDeclared(r)
	case tabLayers:
		c = m.captureLayers(r)
	default:
		return
	}
	if c != nil {
		m.openCapture(c)
	}
}

// captureEventsPane is W in the Events pane.
func (m *Model) captureEventsPane() {
	if c := m.captureEvents(); c != nil {
		m.openCapture(c)
	}
}

// captureJobPane is W in the Jobs pane: the highlighted job's output.
func (m *Model) captureJobPane() {
	if c := m.captureJob(); c != nil {
		m.openCapture(c)
	}
}

// captureReaderPane is W in the file reader.
func (m *Model) captureReaderPane() {
	if c := m.captureReader(); c != nil {
		m.openCapture(c)
	}
}

// ---- each pane's source ---------------------------------------------------------------

// captureInfo: the summary lines, built as the tab builds them. They are
// not laid out for a width; only the key column is padded, to its own.
func (m *Model) captureInfo(r row) *paneCapture {
	lines := m.infoLines()
	return m.textCapture("info", "Info", r.name, r.name,
		"the Info tab's summary as the desk shows it, colour removed", lines)
}

// captureInspect: the daemon's document, indented, no header.
func (m *Model) captureInspect(r row) *paneCapture {
	if m.detail.key != m.currentDetailKey() || m.detail.key == "" {
		m.nothingToSave("the inspect document has not arrived yet.")
		return nil
	}
	var raw []byte
	envs := true
	switch m.kind {
	case KindContainers:
		if d := m.detail.container; d != nil {
			raw = d.Raw
		}
	case KindImages:
		if d := m.detail.image; d != nil {
			raw = d.Raw
		}
	case KindVolumes:
		if d := m.detail.volume; d != nil {
			raw, envs = d.Raw, false
		}
	case KindNetworks:
		if d := m.detail.network; d != nil {
			raw, envs = d.Raw, false
		}
	}
	if len(raw) == 0 {
		if m.detail.err != "" {
			m.nothingToSave("the inspect failed, so there is no document (" + m.detail.err + ").")
			return nil
		}
		m.nothingToSave("the inspect document has not arrived yet.")
		return nil
	}
	data, err := capture.JSON(raw)
	if err != nil {
		m.nothingToSave("the daemon's inspect document is not JSON, so it is not written as .json.")
		return nil
	}
	holds := "the daemon's inspect document, indented, as JSON"
	if envs {
		holds += " (environment variable values included)"
	}
	return &paneCapture{
		kind: "inspect", pane: "Inspect", object: r.name, slug: r.name,
		format: report.JSON, holds: holds, class: classInspect, data: data,
	}
}

// captureLogs: every line the buffer holds, oldest first, markers
// included; with a filter, the lines it matches — the same test the pane
// filters with.
func (m *Model) captureLogs(r row) *paneCapture {
	ring, st := m.hub.Log()
	if st.Target == "" || st.Target != r.containerID {
		m.nothingToSave("the log stream for this container is not open yet, so no line is held.")
		return nil
	}
	first, end := ring.Bounds()
	if end <= first {
		m.nothingToSave("no log line has arrived, so there is nothing held.")
		return nil
	}
	lines, from := ring.Read(first, end)
	held := len(lines)
	bound := fmt.Sprintf("the desk holds at most %s lines (the last %s when the stream opened); "+
		"older output on the daemon is not in this file", files.Count(live.LogLines), files.Count(live.LogTail))
	if from > 0 {
		bound += fmt.Sprintf("; %s older lines had already scrolled out", files.Count(int(from)))
	}
	if q := m.ds.logQ; q != "" {
		// The lines read above, matched by the test the pane's filter uses
		// (logMatches is search.Contains over the ring): matching what was
		// read, not the ring again, keeps the count and the lines one set
		// while the stream goes on appending.
		var kept []string
		for _, l := range lines {
			if search.Contains(l, q) {
				kept = append(kept, l)
			}
		}
		filter := fmt.Sprintf("filter: %s — %s of %s held lines match; only those are below", q, files.Count(len(kept)), files.Count(held))
		holds := fmt.Sprintf("%s of %s held log lines — those the filter %q matches — as the desk shows them, colour removed",
			files.Count(len(kept)), files.Count(held), q)
		return m.textCapture("logs", "Logs", r.name, r.name, holds, kept, filter, bound)
	}
	count := fmt.Sprintf("lines: %s held, oldest first — every held line is below", files.Count(held))
	holds := fmt.Sprintf("%s held log lines as the desk shows them, colour removed", files.Count(held))
	return m.textCapture("logs", "Logs", r.name, r.name, holds, lines, count, bound)
}

// captureEvents: every event the ring holds, each line whole.
func (m *Model) captureEvents() *paneCapture {
	first, end := m.hub.Events.Bounds()
	entries, _ := m.hub.Events.Read(first, end)
	if len(entries) == 0 {
		m.nothingToSave("no engine event has arrived yet.")
		return nil
	}
	nameOf := m.eventNameOf()
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i], _ = eventLine(e, nameOf)
	}
	object := m.endpoint.Context
	if object == "" {
		object = "engine"
	}
	words := "the daemon's events (context " + esc(object) + ")"
	count := fmt.Sprintf("events: %s held, oldest first; the desk holds at most %s, from when it connected",
		files.Count(len(entries)), files.Count(live.EventLines))
	holds := fmt.Sprintf("%s held engine events as the desk shows them, each line whole", files.Count(len(entries)))
	return m.textCapture("events", "Events", words, object, holds, lines, count, "times are this machine's local time")
}

// captureProcesses: the snapshot that was taken, laid out with no cell cut.
func (m *Model) captureProcesses(r row) *paneCapture {
	p := &m.proc
	if r.state != engine.StateRunning || p.target != r.containerID || p.list == nil {
		m.nothingToSave("no process snapshot is held for this container.")
		return nil
	}
	header, rows := processTableWhole(p.list)
	lines := append([]string{header}, rows...)
	if len(rows) == 0 {
		lines = append(lines, "No processes were reported.")
	}
	snap := "snapshot taken at " + p.at.Format(time.RFC3339) + " (GET /containers/{id}/top); not taken again for this file"
	if p.stale {
		snap += "; stale: the daemon could not be reached since"
	}
	holds := fmt.Sprintf("the process snapshot taken at %s: %s, command lines whole", p.at.Format("15:04:05"), files.Plural(len(rows), "process", "processes"))
	return m.textCapture("processes", "Processes", r.name, r.name, holds, lines, snap)
}

// captureChanges: the comparison built again at the page's width.
func (m *Model) captureChanges(r row) *paneCapture {
	t, ok := m.changesTarget()
	if !ok || (t.refusal.None() && (t.old == nil || t.new == nil)) {
		m.nothingToSave("the comparison has not been built yet.")
		return nil
	}
	lines := m.changesLines(t, capture.PageWidth)
	return m.textCapture("changes", "Changes", r.name, r.name,
		"the re-creation diff as the desk built it, prose wrapped for a page", lines)
}

// captureDeclared: the comparison with the compose file, at the page's
// width, once there is one.
func (m *Model) captureDeclared(r row) *paneCapture {
	t, ok := m.declaredTarget()
	if !ok {
		m.nothingToSave("this project no longer has any containers.")
		return nil
	}
	res := m.dc.results[t.key]
	if res == nil || (m.dc.run != nil && m.dc.run.key == t.key) {
		m.nothingToSave("the comparison with the compose file has not been made yet (Enter makes it).")
		return nil
	}
	lines := m.declaredLines(t, capture.PageWidth)
	return m.textCapture("declared", "Declared", r.name, r.name,
		"the comparison with the compose file as the desk built it, prose wrapped for a page", lines,
		"the file's side is what it said at "+res.at.Format(time.RFC3339)+"; the containers' side is as listed when captured")
}

// captureLayers: the whole build — every step with the facts its block
// states — built again at the page's width from the tab's own facts, so the
// file does not depend on the terminal, the scroll or the cursor.
func (m *Model) captureLayers(r row) *paneCapture {
	t, ok := m.layersTarget()
	if !ok {
		return nil
	}
	v := m.layersView(t)
	switch {
	case v.ownErr != "":
		m.nothingToSave("this image's layers could not be read (s reads them again).")
		return nil
	case !v.have:
		m.nothingToSave("this image's layers have not been read yet.")
		return nil
	}
	lines := m.layersLines(v, capture.PageWidth)
	holds := "the build of this image: " + files.Plural(len(v.steps), "step", "steps") + ", each with its layer and what the desk knows of it"
	if v.rep == nil {
		holds += "; the archive was not read, so the layers' contents are not in it"
	}
	return m.textCapture("layers", "Layers", r.name, r.name, holds, lines,
		"image "+escID(v.id)+"; instructions are masked: build-argument values left out, secret-shaped text hidden")
}

// captureJob: the highlighted job's output so far, as the output viewer
// shows it.
func (m *Model) captureJob() *paneCapture {
	list := m.jobs.List()
	i, ok := m.selectedJob(list)
	if !ok {
		m.nothingToSave("there is no job.")
		return nil
	}
	j, ok := m.jobs.Get(list[i].ID) // the list's entries carry no output
	if !ok {
		m.nothingToSave("there is no job.")
		return nil
	}
	if len(j.Log) == 0 && j.Status == "" && j.Err == "" {
		m.nothingToSave("this job has no output yet.")
		return nil
	}
	lines := jobOutput(j, m.st)
	what := strings.TrimSpace(j.Title + " " + j.Target) // text already (jobOutput)
	slug := j.Target
	if strings.TrimSpace(slug) == "" {
		slug = j.Title
	}
	state := "the job's output so far"
	if j.State != jobs.Running {
		state = "the job's output"
	}
	return m.textCapture("output", "Jobs", what, slug, state+" as the desk shows it, colour removed", lines)
}

// captureReader: the preview the reader holds — already inert text.
func (m *Model) captureReader() *paneCapture {
	rd := m.fs.rd
	if rd == nil {
		return nil
	}
	switch {
	case rd.run != nil:
		m.nothingToSave("the file is still being read.")
		return nil
	case !rd.loaded:
		m.nothingToSave("no contents of this file are held.")
		return nil
	case rd.pv.Binary:
		m.nothingToSave("the file is not text, so no contents are held.")
		return nil
	}
	name := files.Escape(rd.path)
	object := name + " in " + files.Escape(rd.containerName)
	holds := "the file's text as the reader holds it: " + readerShownWords(rd.pv)
	return m.textCapture("file", "File", object, path.Base(rd.path), holds, rd.view.lines,
		"contents: "+readerShownWords(rd.pv),
		"modified "+rd.meta.mtime.Format(time.RFC3339)+", as the container's archive states it")
}

// ---- the dialog ----------------------------------------------------------------------

// captureBody is the export dialog's body for a capture: one format, the
// sentence saying what the file holds, and the class it belongs to.
func (m *Model) captureBody(d *dialog, w int) (string, []string) {
	st := m.st
	x := d.exp
	c := x.capture
	title := "Save this " + c.pane + " text"
	if c.format == report.JSON {
		title = "Save the inspect document"
	}
	kv := func(k, v string, t tone) []string {
		var out []string
		for i, part := range wrapWords(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			out = append(out, label+st.tone(t, part))
		}
		return out
	}
	body := kv("object", x.object, toneNormal)
	body = append(body, kv("holds", c.holds, toneNormal)...)
	if x.confirm {
		body = append(body, "", st.dialogTitle.Render("Write this file?"), "")
		body = append(body, kv("path", esc(x.abs), toneNormal)...)
		body = append(body, kv("format", captureFormatWords(c.format), toneNormal)...)
		body = append(body, kv("size", fmt.Sprintf("%d bytes", len(x.data)), toneNormal)...)
		body = append(body, kv("mode", "0600: only you can read it", toneNormal)...)
		body = append(body, "")
		body = append(body, bullets(st, "•", toneWarn, []string{c.class}, w)...)
		body = append(body, bullets(st, "•", toneDim, []string{"It is written on this machine and sent nowhere; nothing is asked of the daemon for it."}, w)...)
		body = append(body, "", m.hints("y", "write", "n Esc", "back"))
		return title, body
	}
	body = append(body, "", "Format "+st.tone(toneDim, "(one for this pane)"))
	body = append(body, "▸ "+fit(string(c.format), 10)+" "+captureFormatWords(c.format))
	x.input.Width = max(w-4, 10)
	body = append(body, "", "Path")
	m.recordDialogRow(len(body), hitText, int(textExportPath), 0, "")
	body = append(body, escInput(x.input.View()), "")
	body = append(body, bullets(st, "•", toneDim, []string{
		c.class,
		"~ is your home folder. The folder must exist; a file already there is never replaced.",
	}, w)...)
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	body = append(body, "", m.hints("Enter", "review", "Esc", "cancel"))
	return title, body
}

func captureFormatWords(f report.Format) string {
	if f == report.JSON {
		return "JSON — the daemon's document, indented; no header"
	}
	return "text — # header lines, then the pane's text laid out for a page"
}
