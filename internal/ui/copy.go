package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/transfer"
)

// Copying between this machine and a container, from the Files view
// (docs/DECISIONS.md, round seventeen, W1–W10).
//
// o copies the highlighted path out: a form asks for the local path, shows
// the absolute path it will write, refuses one that exists or whose folder
// does not, and Enter starts one job that streams the daemon's archive and
// extracts it there (transfer.Extract), removing what it wrote if it fails
// or is cancelled. It only reads the daemon, so it works in read-only mode.
//
// i copies a local path in: a form asks for the source and the container
// destination (the highlighted path), Enter walks the source off the UI's
// goroutine, and the plan dialog asks for y. The job streams the source as a
// tar (plan.Execute, OpCopyIn); when it ends, the view reads the change list
// again. Read-only mode refuses i before any form opens.
//
// In both directions the destination is the path named, never a folder the
// source is put in (W3), and its parent must exist.

// The two copy forms' fields.
const (
	copyInSource = iota
	copyInDest
)

// copyRule is the one sentence both forms state (W3).
const copyRule = "The path is the destination itself — the copy is written there, not inside it — and its parent must already exist."

// copyForm is what a copy form is about.
type copyForm struct {
	containerID   string
	containerName string
	// path is the container path copy-out reads.
	path string
	// cancel stops copy-in's walk of the source when the form is closed.
	cancel context.CancelFunc
}

// copyWatch is a copy-in job the Files view waits for, to read the
// container's change list again when it ends (W7).
type copyWatch struct {
	jobID       int
	containerID string
	name        string
}

// onFilesCopyKey is o and i in the Files view.
func (m *Model) onFilesCopyKey(key string, here *files.Row) {
	fs := &m.fs
	if fs.kind != KindContainers {
		m.setStatus(toneDim, "Copies are between a container and this machine: open the Files view on a container (Containers, then F). Nothing was sent.")
		return
	}
	if key == "i" && m.readOnly {
		m.setStatus(toneWarn, "Read-only mode: copying into a container is disabled. Nothing was sent to the daemon.")
		return
	}
	if here == nil {
		m.setStatus(toneDim, "Nothing is highlighted: move to a path first.")
		return
	}
	n := here.Node
	cp := &copyForm{containerID: fs.containerID, containerName: fs.containerName, path: n.Path}
	if key == "o" {
		switch {
		case n.Path == "/" || n.Path == "":
			m.setStatus(toneDim, "Copy out takes one path inside the container, not its whole root: move to one first.")
			return
		case n.Change == files.Deleted || here.Gone:
			m.setStatus(toneDim, files.Escape(n.Path)+" was deleted by the container: there is nothing to copy out.")
			return
		}
		f := textField("Local destination", "~/"+path.Base(n.Path), 4096)
		f.hint = "a path on this machine; ~ is your home folder, a relative path is taken from " + m.workDir()
		m.dlg = &dialog{kind: dlgForm, form: formCopyOut, hasInput: true, openedAt: time.Now(), fields: []formField{f}, cp: cp}
	} else {
		src := textField("Local source", "~/assets", 4096)
		src.hint = "a file or directory on this machine; ~ is your home folder"
		dest := textField("Container destination", "/srv/assets", 4096)
		dest.hint = "an absolute path in the container: " + copyRule
		dest.rows[0].SetValue(n.Path)
		dest.rows[0].CursorEnd()
		m.dlg = &dialog{kind: dlgForm, form: formCopyIn, hasInput: true, openedAt: time.Now(), fields: []formField{src, dest}, cp: cp}
	}
	m.dlg.refocus()
}

// copyLocal resolves what was typed for a local path, as the export dialog
// does: ~ and relative paths mean what they mean there.
func (m *Model) copyLocal(text string) (string, error) {
	return report.ExpandPath(text, m.homeDir(), m.workDir())
}

// checkCopyOutDest is the copy-out form's refusal, in one sentence, or "".
// The extractor decides again, atomically: this only spares the operator a
// job that would fail.
func checkCopyOutDest(abs string) string {
	if _, err := os.Lstat(abs); err == nil {
		return abs + " already exists: a copy never replaces a path on this machine. Name one that does not exist."
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return "Cannot check " + abs + ": " + err.Error() + "."
	}
	parent := filepath.Dir(abs)
	fi, err := os.Stat(parent)
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return parent + " is not a folder: something above it is a file."
	case errors.Is(err, fs.ErrNotExist):
		return parent + " does not exist: the parent must exist; no folder is created."
	case err != nil:
		return "Cannot check " + parent + ": " + err.Error() + "."
	case !fi.IsDir():
		return parent + " is not a folder: the parent must be one."
	}
	return ""
}

// submitCopyOut is Enter in the copy-out form: the checks, then the job.
func (m *Model) submitCopyOut() tea.Cmd {
	d := m.dlg
	abs, err := m.copyLocal(d.formText())
	if err != nil {
		d.formErr = capitalise(err.Error()) + "."
		return nil
	}
	if why := checkCopyOutDest(abs); why != "" {
		d.formErr = why
		return nil
	}
	cp := d.cp
	m.dlg = nil
	m.startCopyOut(cp, abs)
	return nil
}

// startCopyOut is the copy-out job (W5): the archive, then the extraction.
// The job's title, target and output name paths and counts only.
func (m *Model) startCopyOut(cp *copyForm, abs string) int {
	eng := m.eng
	readOnly := m.readOnly
	src, name, id := cp.path, cp.containerName, cp.containerID
	target := files.Escape(name) + ":" + files.Escape(src)
	jobID := m.startJob("copy out of container", target, func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking the daemon for the archive")
		a, err := eng.CopyFromContainer(ctx, id, src)
		if err != nil {
			r.Logf("nothing was written on this machine")
			return err
		}
		defer a.Content.Close()
		if a.Stat.Name != path.Base(src) {
			r.Logf("nothing was written on this machine")
			return fmt.Errorf("the daemon answered for %s, not %s: nothing was written", files.Escape(a.Stat.Name), files.Escape(path.Base(src)))
		}
		what := "a file of " + humanBytes(a.Stat.Size)
		switch {
		case a.Stat.IsDir():
			what = "a directory"
		case a.Stat.Mode&fs.ModeSymlink != 0:
			what = "a symlink to " + files.Escape(a.Stat.LinkTarget)
		}
		r.Logf("from %s — %s, by the daemon's stat", target, what)
		r.Logf("to %s", abs)
		var last time.Time
		total := int64(0)
		if a.Stat.Mode.IsRegular() {
			total = a.Stat.Size
		}
		tree, err := transfer.Extract(ctx, a.Content, abs, func(t transfer.Tree) {
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
				last = now
				r.Status(fmt.Sprintf("wrote %s · %s", humanBytes(t.Bytes), files.Plural(t.Entries, "entry", "entries")))
				if total > 0 {
					r.Fraction(min(float64(t.Bytes)/float64(total), 0.99))
				}
			}
		})
		for _, s := range tree.Skipped {
			r.Logf("skipped %s", s)
		}
		if err != nil {
			switch {
			case tree.Removed > 0:
				r.Logf("removed what was written: %s (%s)", abs, files.Plural(tree.Removed, "path", "paths"))
			default:
				r.Logf("nothing was written on this machine")
			}
			return err
		}
		r.Logf("wrote %s — %s — to %s", humanBytes(tree.Bytes), copyWords(tree), abs)
		if readOnly {
			r.Logf("nothing was changed on the daemon: its archive was only read (GET), as read-only mode allows")
		} else {
			r.Logf("nothing was changed on the daemon: its archive was only read (GET)")
		}
		r.Status(fmt.Sprintf("wrote %s · %s", humanBytes(tree.Bytes), files.Plural(tree.Entries, "entry", "entries")))
		return nil
	})
	m.jobSel = jobID
	m.setStatus(toneAccent, "started: copy out of "+target+" to "+abs)
	return jobID
}

func copyWords(t transfer.Tree) string {
	parts := []string{files.Plural(t.Files, "file", "files")}
	if t.Dirs > 0 {
		parts = append(parts, files.Plural(t.Dirs, "directory", "directories"))
	}
	if t.Links > 0 {
		parts = append(parts, files.Plural(t.Links, "symlink", "symlinks"))
	}
	if len(t.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", len(t.Skipped)))
	}
	return strings.Join(parts, ", ")
}

// submitCopyIn is Enter in the copy-in form: the source is resolved and
// walked off the UI's goroutine (formBusy), and the answer opens the plan
// dialog — or keeps the form open with the reason. Escape stops the walk.
func (m *Model) submitCopyIn() tea.Cmd {
	d := m.dlg
	abs, err := m.copyLocal(d.fields[copyInSource].value())
	if err != nil {
		d.formErr = "Local source: " + err.Error() + "."
		return nil
	}
	dest := strings.TrimSpace(d.fields[copyInDest].value())
	ctx, cancel := context.WithCancel(context.Background())
	d.cp.cancel = cancel
	d.formBusy = true
	m.planSeq++
	seq := m.planSeq
	ref := plan.ContainerRef{ID: d.cp.containerID, Name: d.cp.containerName}
	return func() tea.Msg {
		defer cancel()
		p, err := plan.CopyIn(ctx, ref, plan.CopySpec{Source: abs, Dest: dest})
		return planMsg{seq: seq, plan: p, err: err, fromForm: true, confirm: true}
	}
}

// watchCopyIn records a copy-in job, so that the view reads the change list
// again when it ends.
func (m *Model) watchCopyIn(jobID int, p plan.Plan) {
	if len(p.Steps) != 1 || p.Steps[0].Op != plan.OpCopyIn || jobID == 0 {
		return
	}
	st := p.Steps[0]
	m.fs.copyWatch = &copyWatch{jobID: jobID, containerID: st.ID, name: st.Name}
}

// collectCopyIn is the frame tick's look at a copy-in job. Once it has
// ended, the change list of its container is read again — once, and only
// while the view is still open on that container: closing the view, or
// moving to another object, ends the watch.
func (m *Model) collectCopyIn() tea.Cmd {
	fs := &m.fs
	w := fs.copyWatch
	if w == nil {
		return nil
	}
	if !fs.open || fs.kind != KindContainers || fs.containerID != w.containerID {
		fs.copyWatch = nil
		return nil
	}
	j, ok := m.jobs.Get(w.jobID)
	if ok && j.State == jobs.Running {
		return nil
	}
	fs.copyWatch = nil
	if !ok {
		return nil // cleared from the Jobs pane: nothing to say
	}
	// A copy that failed or was cancelled may have written part of the
	// archive before it stopped: the list is read again whatever the end.
	fs.copyReread, fs.copyOK = true, j.State == jobs.Succeeded
	return m.readChanges()
}

// copyRereadStatus says, when the change list read after a copy-in
// arrives, why it was read.
func (m *Model) copyRereadStatus() {
	fs := &m.fs
	if !fs.copyReread {
		return
	}
	fs.copyReread = false
	if fs.copyOK {
		m.setStatus(toneGood, fmt.Sprintf("Copied into %s; its change list was read again: %s.", files.Escape(fs.containerName), files.Plural(len(fs.changes), "change", "changes")))
		return
	}
	m.setStatus(toneWarn, fmt.Sprintf("The copy into %s did not complete; its change list was read again, since part of it may have been written.", files.Escape(fs.containerName)))
}

// ---- drawing ------------------------------------------------------------------------

// copyBody lays out the two copy forms: the container and the daemon, the
// fields, the absolute paths they resolve to, the rule and the effects.
func (m *Model) copyBody(d *dialog, w int) (string, []string) {
	st := m.st
	cp := d.cp
	var body []string
	kv := func(k, v string, t tone) {
		for i, part := range wrapLine(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			body = append(body, label+st.tone(t, part))
		}
	}
	kv("container", files.Escape(cp.containerName)+"  ("+engine.ShortID(cp.containerID)+")", toneNormal)
	body = append(body, m.daemonLines(w, true)...)
	notes := func(items ...string) {
		body = append(body, "")
		body = append(body, bullets(st, "•", toneDim, items, w)...)
	}
	field := func(i int) {
		f := &d.fields[i]
		f.rows[0].Width = max(w-4, 10)
		label := f.label
		if len(d.fields) > 1 && i == d.focus {
			label = st.tone(toneAccent, "▸ ") + st.dialogTitle.Render(label)
		} else if len(d.fields) > 1 {
			label = "  " + st.tone(toneDim, label)
		}
		body = append(body, "", label, f.rows[0].View())
	}
	resolved := func(label, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if abs, err := m.copyLocal(text); err != nil {
			body = append(body, st.tone(toneWarn, "  "+capitalise(err.Error())+"."))
		} else {
			body = append(body, "  "+st.tone(toneDim, label)+" "+st.dialogTitle.Render(abs))
		}
	}

	var title, verb string
	switch d.form {
	case formCopyOut:
		title, verb = "Copy out of the container", "copy out"
		kv("path", files.Escape(cp.path), toneNormal)
		field(0)
		resolved("will write", d.formText())
		notes(copyRule,
			"A path already on this machine is never replaced, and no folder is created.",
			"Contents and modes are copied, not ownership: what is written is yours. Symlinks are copied as links and never followed; devices, FIFOs and sockets are skipped and listed.",
			"The daemon's archive is read (GET) and nothing in the container changes. If the copy fails or is cancelled, what it wrote is removed.")
	case formCopyIn:
		title, verb = "Copy into the container", "walk the source, then confirm"
		field(copyInSource)
		resolved("will read", d.fields[copyInSource].value())
		field(copyInDest)
		notes(copyRule,
			"Files already at the destination with the same names are replaced; other files there are left alone. The next step shows what will be sent and asks for y.",
			"Contents and modes are sent, not ownership: the daemon's default applies. Symlinks are sent as links; devices, FIFOs and sockets are skipped.")
	}
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	switch {
	case d.formBusy:
		body = append(body, "", st.tone(toneDim, spinnerFrames[m.spinner]+" walking the local source… Esc stops it"))
	case len(d.fields) > 1:
		body = append(body, "", m.hints("Tab", "field", "Enter", verb, "Esc", "cancel"))
	default:
		body = append(body, "", m.hints("Enter", verb, "Esc", "cancel"))
	}
	return title, body
}
