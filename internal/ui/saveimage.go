package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/report"
)

// Saving an image's archive, from Images (openspec change save-image-archive,
// D1–D7).
//
// w on the highlighted row captures the image — ID, reference, name — and
// resolves the platform it is inspected as: from the Details pane's inspect
// when that is the image's, or else from one inspect of its own. Only then
// does the form open, bound to that image: it names the image, its short
// ID, what will be saved (the reference, or the ID of an untagged row), the
// platform and the local path, prefilled ~/docker-desk-image-<object>-
// <YYYYMMDD>.tar. Enter checks the path and starts one job. The job
// inspects the reference again, refuses a tag that has moved, asks the
// daemon for the archive (GET /images/get) and streams it through
// report.WriteStream: a hidden temporary file beside the destination, mode
// 0600, published under the chosen name only when complete and never over
// anything. A cancel or a failure closes the daemon's stream and removes the
// temporary file. Nothing on the daemon changes, so read-only mode allows it.

// saveInspectTimeout bounds each inspect the save makes (the form's and the
// job's): the platform and the pin are one small answer.
const saveInspectTimeout = 10 * time.Second

// saveChunk is how much of the archive is read from the daemon at a time;
// the context is checked between reads.
const saveChunk = 32 << 10

// saveImage is what the save form and its job are about: the image as the
// row named it when w was pressed, and the platform it was inspected as.
// Nothing later re-reads the highlight.
type saveImage struct {
	imageID string
	// imageRef is the reference the row was for; "" for an untagged row,
	// which is saved by ID.
	imageRef string
	name     string
	platform engine.Platform
	// size is the daemon's listed size: kept for the record, never shown
	// as the file's (the listed size is not the tar's).
	size int64
	// gen is the focus generation the image was captured under.
	gen uint64
}

// by is what the daemon is asked to save: the reference, or the ID.
func (s *saveImage) by() string {
	if s.imageRef != "" {
		return s.imageRef
	}
	return s.imageID
}

// tagged reports whether the archive will carry a tag: a reference that is
// a tag, not a digest.
func (s *saveImage) tagged() bool {
	return s.imageRef != "" && !strings.Contains(s.imageRef, "@")
}

// platformWords names a platform, or says the daemon chooses. The words
// come from the daemon: escape them where they are drawn.
func platformWords(p engine.Platform) string {
	if p.OS == "" || p.Architecture == "" {
		return "the daemon's own (it reported none)"
	}
	return p.String()
}

// escapedErr is a job's error as the Jobs pane may draw it: its text
// escaped (a daemon's message, a reference or an ID it sent can hold
// anything), its chain kept, so that a cancel is still a cancel.
type escapedErr struct{ err error }

func (e escapedErr) Error() string { return escape.Name(e.err.Error()) }
func (e escapedErr) Unwrap() error { return e.err }

// saveState is the inspect w runs when the Details pane has not inspected
// the image: at most one at a time, and an answer is taken only for the
// latest request under the current focus.
type saveState struct {
	seq     uint64
	pending *saveImage
}

// saveInspectMsg is the platform inspect's answer.
type saveInspectMsg struct {
	seq    uint64
	gen    uint64
	target saveImage
	detail engine.ImageDetail
	err    error
}

// openSaveArchive is the w action: the image is captured now, and the form
// opens once its platform is known.
func (m *Model) openSaveArchive(r row) tea.Cmd {
	if m.kind != KindImages || r.imageID == "" {
		return nil
	}
	if m.si.pending != nil {
		m.setStatus(toneDim, "Still reading the platform of "+m.si.pending.name+"…")
		return nil
	}
	t := saveImage{imageID: r.imageID, imageRef: r.imageRef, name: r.name, gen: m.gen}
	for _, img := range m.snap.Images {
		if img.ID == r.imageID {
			t.size = img.Size
		}
	}
	// The Details pane's inspect, when it is this image's and current.
	if d := m.detail.image; d != nil && !m.detail.loading && m.detail.err == "" && d.ID == r.imageID &&
		m.detail.key == fmt.Sprintf("%d:%s", KindImages, r.key) {
		t.platform = d.Platform()
		m.openSaveForm(t)
		return nil
	}
	m.si.seq++
	seq, gen, eng := m.si.seq, m.gen, m.eng
	m.si.pending = &t
	m.setStatus(toneDim, "Reading the image's platform… ("+t.name+")")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), saveInspectTimeout)
		defer cancel()
		d, err := eng.InspectImage(ctx, t.imageID)
		return saveInspectMsg{seq: seq, gen: gen, target: t, detail: d, err: err}
	}
}

func (m *Model) onSaveInspect(msg saveInspectMsg) {
	if msg.seq != m.si.seq || msg.gen != m.gen {
		return // superseded, or about a daemon no longer focused
	}
	m.si.pending = nil
	t := msg.target
	name := t.name
	switch {
	case msg.err != nil && engine.IsNotFound(msg.err):
		m.setStatus(toneWarn, name+" no longer exists on this daemon: nothing was saved.")
		return
	case msg.err != nil:
		m.setStatus(toneBad, "Cannot save "+name+": the daemon did not say its platform ("+escape.Name(msg.err.Error())+"). Nothing was written.")
		return
	case msg.detail.ID != t.imageID:
		m.setStatus(toneBad, "Cannot save "+name+": the daemon answered for another image. Nothing was written.")
		return
	case m.dlg != nil:
		// Something else was opened meanwhile; a form must not replace it.
		m.setStatus(toneDim, "The save form for "+name+" was not opened: another dialog is open.")
		return
	}
	t.platform = msg.detail.Platform()
	m.status = statusLine{} // "reading the platform" is answered
	m.openSaveForm(t)
}

// saveDefaultPath is the form's prefilled path.
func saveDefaultPath(t saveImage, day time.Time) string {
	object := t.imageRef
	if object == "" {
		object = engine.ShortID(t.imageID)
	}
	return "~/docker-desk-image-" + report.Slug(object) + "-" + day.Format("20060102") + ".tar"
}

func (m *Model) openSaveForm(t saveImage) {
	f := textField("Local file", "~/image.tar", 4096)
	f.hint = "a path on this machine; ~ is your home folder, a relative path is taken from " + m.workDir()
	f.rows[0].SetValue(saveDefaultPath(t, m.now()))
	f.rows[0].CursorEnd()
	m.dlg = &dialog{kind: dlgForm, form: formSaveImage, hasInput: true, openedAt: time.Now(), fields: []formField{f}, si: &t}
	m.dlg.refocus()
}

// checkSaveDest is the form's refusal of a local path, in one sentence, or
// "". WriteStream decides again, atomically, when the archive is
// published: this only spares the operator a job that would fail.
func checkSaveDest(abs string) string {
	if fi, err := os.Lstat(abs); err == nil {
		switch {
		case fi.IsDir():
			return esc(abs) + " is a folder: name a file inside it."
		case fi.Mode()&fs.ModeSymlink != 0:
			return esc(abs) + " is a symbolic link: the desk never writes through one, and never replaces a file. Choose another name."
		default:
			return "A file is already there (" + esc(abs) + "): the desk never replaces one. Choose another name."
		}
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return "Cannot check " + esc(abs) + ": " + esc(err.Error()) + "."
	}
	dir := filepath.Dir(abs)
	fi, err := os.Stat(dir)
	dir = esc(dir) // from here on, the sentence's text
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return dir + " is not a folder: something above it is a file."
	case errors.Is(err, fs.ErrNotExist):
		return "The folder " + dir + " does not exist: the folder must exist; none is created."
	case err != nil:
		return "Cannot check " + dir + ": " + esc(err.Error()) + "."
	case !fi.IsDir():
		return dir + " is not a folder: the folder must be one."
	}
	return ""
}

// submitSaveImage is Enter in the save form: the path's checks, then the
// job. A refused path keeps the form open and sends nothing.
func (m *Model) submitSaveImage() tea.Cmd {
	d := m.dlg
	t := d.si
	abs, err := report.ExpandPath(d.formText(), m.homeDir(), m.workDir())
	if err != nil {
		d.formErr = capitalise(esc(err.Error())) + "."
		return nil
	}
	if why := checkSaveDest(abs); why != "" {
		d.formErr = why
		return nil
	}
	if t.gen != m.gen {
		m.dlg = nil
		m.setStatus(toneWarn, "The daemon in focus changed since the form opened: nothing was saved.")
		return nil
	}
	if m.loaded[listImages] {
		found := false
		for _, img := range m.snap.Images {
			found = found || img.ID == t.imageID
		}
		if !found {
			m.dlg = nil
			m.setStatus(toneWarn, t.name+" no longer exists on this daemon: nothing was read or written.")
			return nil
		}
	}
	m.dlg = nil
	m.startSaveImage(*t, abs)
	return nil
}

// startSaveImage is the save job (D3, D5). Its title, target and output
// name the image, the platform and paths; no byte of the archive.
func (m *Model) startSaveImage(t saveImage, abs string) int {
	eng := m.eng
	readOnly := m.readOnly
	name := t.name
	jobID := m.startJob("save image archive", name, func(ctx context.Context, r *jobs.Run) error {
		if err := saveJob(ctx, r, eng, t, abs, readOnly); err != nil {
			return escapedErr{err}
		}
		return nil
	})
	if jobID != 0 {
		m.jobSel = jobID
		m.setStatus(toneAccent, "started: save "+name+" to "+escape.Name(abs))
	}
	return jobID
}

// saveJob is the save job's body. Its errors are plain text, escaped once
// by the caller; what it logs and its status it escapes itself.
func saveJob(ctx context.Context, r *jobs.Run, eng engine.Engine, t saveImage, abs string, readOnly bool) error {
	by := t.by()
	short := escID(t.imageID)
	plat := t.platform
	if t.imageRef != "" {
		r.Logf("image %s, by its reference, pinned to %s", escape.Name(t.imageRef), short)
	} else {
		r.Logf("image %s, untagged, by its ID", short)
	}
	r.Logf("platform %s", escape.Name(platformWords(plat)))
	r.Logf("to %s", escape.Name(abs))
	nothing := func(err error) error {
		r.Logf("nothing was written on this machine")
		return err
	}

	// The pin: what the reference names now, immediately before the
	// stream (D3). The daemon offers no atomic save-this-ID-under-this-
	// tag; this narrows the window to the one the removals accept.
	r.Status("checking what " + escape.Name(by) + " names now")
	ictx, cancel := context.WithTimeout(ctx, saveInspectTimeout)
	d, err := eng.InspectImage(ictx, by)
	cancel()
	switch {
	case err != nil && ctx.Err() != nil:
		return nothing(ctx.Err())
	case err != nil && engine.IsNotFound(err):
		return nothing(fmt.Errorf("%s no longer exists on this daemon", by))
	case err != nil:
		return nothing(err)
	case d.ID != t.imageID:
		return nothing(fmt.Errorf("the tag moved: %s now names %s, not the image the form showed (%s)",
			by, escID(d.ID), short))
	case d.Platform() != plat:
		return nothing(fmt.Errorf("%s is now inspected as %s, not the %s the form showed",
			by, platformWords(d.Platform()), platformWords(plat)))
	}
	plat = d.Platform() // the fresh inspect's, which is the form's

	r.Status("asking the daemon for the archive")
	var n int64
	asked := false
	err = report.WriteStream(abs, func(w io.Writer) error {
		asked = true
		rc, err := eng.SaveImage(ctx, by, plat)
		if err != nil {
			return err
		}
		defer rc.Close() // closes the daemon's stream on every way out
		buf := make([]byte, saveChunk)
		var last time.Time
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			k, rerr := rc.Read(buf)
			if k > 0 {
				if _, werr := w.Write(buf[:k]); werr != nil {
					return werr
				}
				n += int64(k)
				if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
					last = now
					r.Status("wrote " + humanBytes(n))
				}
			}
			switch {
			case rerr == io.EOF:
				return nil
			case rerr != nil && ctx.Err() != nil:
				return ctx.Err()
			case rerr != nil:
				return fmt.Errorf("the daemon's archive stream failed: %w", rerr)
			}
		}
	})
	if err != nil {
		var we *report.WriteError
		switch {
		case errors.As(err, &we) && we.Exists:
			r.Logf("a file is at %s that the desk did not write; it was not replaced", escape.Name(abs))
			if asked {
				r.Logf("the temporary file was removed")
			}
		case n > 0:
			r.Logf("the partial archive (%s) was removed; nothing remains at %s", humanBytes(n), escape.Name(abs))
		case asked:
			r.Logf("nothing was written on this machine: the temporary file was removed")
		default:
			r.Logf("nothing was written on this machine")
		}
		if errors.Is(err, context.Canceled) {
			r.Logf("cancelled: the daemon's stream was closed")
		}
		return err
	}
	what := "untagged: it loads as an image without a name"
	switch {
	case t.tagged():
		what = "tagged " + escape.Name(t.imageRef)
	case t.imageRef != "":
		what = "by its digest reference " + escape.Name(t.imageRef) + ": it carries no tag"
	}
	r.Logf("wrote %s (%d bytes) to %s, mode 0600", humanBytes(n), n, escape.Name(abs))
	r.Logf("platform %s; the archive is %s", escape.Name(platformWords(plat)), what)
	if readOnly {
		r.Logf("nothing was changed on the daemon: the image was only read (GET), as read-only mode allows")
	} else {
		r.Logf("nothing was changed on the daemon: the image was only read (GET)")
	}
	r.Status("wrote " + humanBytes(n) + " · " + escape.Name(platformWords(plat)))
	return nil
}

// ---- drawing ------------------------------------------------------------------------

// saveBody lays out the save form: the image, the daemon, what is saved,
// the path and what it resolves to, and the rules. No size is quoted: the
// daemon's listed size is not the tar's.
func (m *Model) saveBody(d *dialog, w int) (string, []string) {
	st := m.st
	t := d.si
	var body []string
	kv := func(k, v string, tn tone) {
		for i, part := range wrapLine(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			body = append(body, label+st.tone(tn, part))
		}
	}
	kv("image", t.name+"  ("+escID(t.imageID)+")", toneNormal)
	body = append(body, m.daemonLines(w, true)...)
	switch {
	case t.tagged():
		kv("saves", "the reference "+escape.Name(t.imageRef)+": the archive keeps the tag", toneNormal)
	case t.imageRef != "":
		kv("saves", "the digest reference "+escape.Name(t.imageRef)+": the archive carries no tag", toneNormal)
	default:
		kv("saves", "the image ID "+escID(t.imageID)+": the archive will be untagged", toneNormal)
	}
	kv("platform", escape.Name(platformWords(t.platform)), toneNormal)

	f := &d.fields[0]
	f.rows[0].Width = max(w-4, 10)
	body = append(body, "", f.label)
	m.recordDialogRow(len(body), hitFormRow, 0, 0, "")
	body = append(body, escInput(f.rows[0].View()))
	if text := d.formText(); strings.TrimSpace(text) != "" {
		if abs, err := report.ExpandPath(text, m.homeDir(), m.workDir()); err != nil {
			body = append(body, st.tone(toneWarn, "  "+capitalise(esc(err.Error()))+"."))
		} else {
			// Wrapped, never cut: the operator reads the whole path that
			// will be written.
			for i, part := range wrapLine(escape.Name(abs), max(w-13, 10)) {
				label := "             "
				if i == 0 {
					label = "  " + st.tone(toneDim, "will write") + " "
				}
				body = append(body, label+st.dialogTitle.Render(part))
			}
		}
	}
	body = append(body, "")
	body = append(body, bullets(st, "•", toneDim, []string{
		"The archive is a tar, as `docker save` writes it, for this one platform.",
		"The file is written mode 0600. A file already there is never replaced; the folder must exist and none is created.",
		"Nothing on the daemon changes: the image is only read (GET /images/get). If the save fails or is cancelled, the partial file is removed.",
	}, w)...)
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	body = append(body, "", m.hints("Enter", "save", "Esc", "cancel"))
	return "Save the image archive", body
}
