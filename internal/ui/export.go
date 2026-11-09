package ui

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/report/collect"
)

// Export: `w` in Images › Findings, in the Packages view and in the Space
// view writes what the desk has already computed to one local file
// (docs/DECISIONS.md, X1–X16).
//
// The view collects what it shows into the plain structs of package report
// (through report/collect, the same code the command line uses) when `w`
// is pressed; a dialog then asks for a format and a path, prefilled
// ~/docker-desk-<kind>-<object>-<YYYYMMDD>.<ext>; Enter shows the absolute
// path that will be written, its size and its mode; only y writes it. The
// file is written off the UI's goroutine, and the status line says what
// came of it in one sentence. Nothing is sent to the daemon or anywhere
// else, so export works in read-only mode, and nothing in the desk waits
// for it.

// exportDialog is the state of the export dialog.
type exportDialog struct {
	doc     report.Document
	kind    string // findings, packages, space: the prefilled name's
	object  string // the object, in words
	formats []report.Format
	fsel    int
	input   textinput.Model

	// confirm is the second step: the path below will be written.
	confirm bool
	abs     string
	data    []byte
}

type exportDoneMsg struct {
	path   string
	format report.Format
	bytes  int
	err    error
}

func (m *Model) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

func (m *Model) homeDir() string {
	if m.opts.HomeDir != "" {
		return m.opts.HomeDir
	}
	h, _ := os.UserHomeDir()
	return h
}

func (m *Model) workDir() string {
	if m.opts.WorkDir != "" {
		return m.opts.WorkDir
	}
	w, _ := os.Getwd()
	return w
}

// openExport opens the dialog on a collected document.
func (m *Model) openExport(doc report.Document, kind, object, slug string, def report.Format) {
	formats := report.FormatsFor(doc)
	sel := 0
	for i, f := range formats {
		if f == def {
			sel = i
		}
	}
	in := newInput("", 4096)
	in.SetValue(report.DefaultPath(kind, slug, formats[sel], m.now()))
	in.CursorEnd()
	m.dlg = &dialog{kind: dlgExport, hasInput: true, openedAt: time.Now(),
		exp: &exportDialog{doc: doc, kind: kind, object: object, formats: formats, fsel: sel, input: in}}
}

// imageDocument collects the image document from a Findings report and the
// live join as the tab draws it now.
func (m *Model) imageDocument(rep *forensics.Report, ref string) report.Document {
	var img engine.Image
	for _, x := range m.snap.Images {
		if x.ID == rep.ImageID {
			img = x
		}
	}
	live := forensics.LiveFindings(rep, forensics.LiveInput{Image: img, Images: m.snap.Images, Containers: m.snap.Containers, Facts: m.fx.facts})
	d := collect.Image(m.opts.Version, ref, rep, live)
	return report.Document{Image: &d}
}

func imageObject(rep *forensics.Report, ref string) (words, slug string) {
	words = ref + "  " + engine.ShortID(rep.ImageID) + "  " + rep.Platform
	return words, ref
}

// exportFindings is `w` on the Findings tab.
func (m *Model) exportFindings() {
	t, ok := m.findingsTarget()
	var rep *forensics.Report
	if ok {
		rep = m.fx.cache[t.key]
	}
	if rep == nil {
		m.setStatus(toneDim, "Nothing to export yet: the analysis has not finished.")
		return
	}
	words, slug := imageObject(rep, t.ref)
	m.openExport(m.imageDocument(rep, t.ref), "findings", words, slug, report.JSON)
}

// exportPackages is `w` in the Packages view: the same document, named for
// the view, with SPDX first.
func (m *Model) exportPackages() {
	if m.pk.rep == nil {
		return
	}
	words, slug := imageObject(m.pk.rep, m.pk.ref)
	m.openExport(m.imageDocument(m.pk.rep, m.pk.ref), "packages", words, slug, report.SPDX)
}

// exportSpace is `w` in the Space view: the account as it is shown.
func (m *Model) exportSpace() {
	if m.sp.du == nil {
		m.setStatus(toneDim, "Nothing to export yet: the daemon's account has not been read.")
		return
	}
	in := collect.SpaceInput{
		Version: m.opts.Version, DU: *m.sp.du, Index: m.sp.ix,
		Stale: m.stale(m.sp.duAt), Reading: m.sp.run != nil,
	}
	if m.sp.sizes != nil {
		in.Writable, in.WritableStale, in.Containers = m.sp.sizes, m.stale(m.sp.sizesAt), m.snap.Containers
	}
	s := collect.Space(in)
	object := m.endpoint.Context
	if object == "" {
		object = "engine"
	}
	m.openExport(report.Document{Space: &s}, "space", "the daemon's account (context "+object+")", object, report.JSON)
}

// ---- keys ---------------------------------------------------------------------

func (m *Model) onExportKey(k tea.KeyMsg) tea.Cmd {
	d := m.dlg
	x := d.exp
	key := k.String()
	if x.confirm {
		switch key {
		case "y", "Y":
			if !m.armed() {
				return nil // typed ahead of the question: not consent
			}
			path, data, f := x.abs, x.data, x.formats[x.fsel]
			m.dlg = nil
			m.setStatus(toneDim, "Writing "+path+"…")
			return func() tea.Msg {
				err := report.WriteFile(path, data, false)
				return exportDoneMsg{path: path, format: f, bytes: len(data), err: err}
			}
		case "n", "N", "esc", "backspace":
			x.confirm, x.abs, x.data = false, "", nil
			d.formErr = ""
		}
		return nil
	}
	switch key {
	case "esc":
		m.closeDialog()
		return nil
	case "tab", "down":
		m.exportFormat(1)
		return nil
	case "shift+tab", "up":
		m.exportFormat(-1)
		return nil
	case "enter":
		m.exportReview()
		return nil
	}
	d.formErr = ""
	var cmd tea.Cmd
	x.input, cmd = x.input.Update(k)
	return cmd
}

func (m *Model) exportFormat(delta int) {
	x := m.dlg.exp
	x.fsel = (x.fsel + delta + len(x.formats)) % len(x.formats)
	x.input.SetValue(report.SwapExt(x.input.Value(), x.formats[x.fsel]))
	x.input.CursorEnd()
	m.dlg.formErr = ""
}

// exportReview checks the path and serializes the document, then asks.
// The checks are made again when the file is written: this only spares
// the operator a confirmation that would fail.
func (m *Model) exportReview() {
	d := m.dlg
	x := d.exp
	abs, err := report.ExpandPath(x.input.Value(), m.homeDir(), m.workDir())
	if err != nil {
		d.formErr = capitalise(err.Error()) + "."
		return
	}
	if fi, err := os.Lstat(abs); err == nil {
		if fi.IsDir() {
			d.formErr = abs + " is a folder: name a file inside it."
		} else {
			d.formErr = "A file is already there; the desk never replaces one. Choose another name."
		}
		return
	}
	data, err := report.Render(x.formats[x.fsel], x.doc)
	if err != nil {
		d.formErr = capitalise(err.Error()) + "."
		return
	}
	x.abs, x.data, x.confirm = abs, data, true
	d.formErr = ""
	d.openedAt = time.Now() // the question is new: it arms afresh
}

func (m *Model) onExportDone(msg exportDoneMsg) {
	if msg.err != nil {
		var we *report.WriteError
		if errors.As(msg.err, &we) {
			m.setStatus(toneBad, "Not exported: "+we.Error()+".")
			return
		}
		m.setStatus(toneBad, "Not exported: "+msg.err.Error()+".")
		return
	}
	m.setStatus(toneGood, fmt.Sprintf("Exported %s to %s (%d bytes, mode 0600).", msg.format, msg.path, msg.bytes))
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// ---- drawing --------------------------------------------------------------------

func (m *Model) exportBody(d *dialog, w int) (string, []string) {
	st := m.st
	x := d.exp
	title := map[string]string{"findings": "Export findings", "packages": "Export packages", "space": "Export the Space account"}[x.kind]
	kv := func(k, v string, t tone) []string {
		var out []string
		for i, part := range wrapLine(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			out = append(out, label+st.tone(t, part))
		}
		return out
	}
	body := kv("object", x.object, toneNormal)
	space := x.doc.Space != nil
	if x.confirm {
		f := x.formats[x.fsel]
		body = append(body, "", st.dialogTitle.Render("Write this file?"), "")
		body = append(body, kv("path", x.abs, toneNormal)...)
		body = append(body, kv("format", f.Describe(space), toneNormal)...)
		body = append(body, kv("size", fmt.Sprintf("%d bytes", len(x.data)), toneNormal)...)
		body = append(body, kv("mode", "0600: only you can read it", toneNormal)...)
		body = append(body, "")
		body = append(body, bullets(st, "•", toneDim, []string{
			"It is written on this machine and sent nowhere. Secret values are not in it: a finding says only that it had one.",
		}, w)...)
		body = append(body, "", m.hints("y", "write", "n Esc", "back"))
		return title, body
	}
	body = append(body, "", "Format "+st.tone(toneDim, "(Tab changes)"))
	for i, f := range x.formats {
		mark := "  "
		t := toneDim
		if i == x.fsel {
			mark, t = "▸ ", toneNormal
		}
		body = append(body, mark+st.tone(t, fit(string(f), 10)+" "+f.Describe(space)))
	}
	x.input.Width = max(w-4, 10) // a long path scrolls; the cursor stays in view
	body = append(body, "", "Path", x.input.View(), "")
	body = append(body, bullets(st, "•", toneDim, []string{
		"~ is your home folder. The folder must exist; a file already there is never replaced.",
	}, w)...)
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	body = append(body, "", m.hints("Enter", "review", "Tab", "format", "Esc", "cancel"))
	return title, body
}
