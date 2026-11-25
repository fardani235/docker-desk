package ui

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// The reader: what one regular file in a container holds, from the Files
// view (docs/DECISIONS.md, round twenty-four, A1–A10).
//
// Enter on a file row of a container's tree opens it. One job asks the
// daemon for that one path (GET /containers/{id}/archive, what copy-out
// reads), checks that the answer is the one regular file that was asked for,
// and hands its first megabyte to files.ReadPreview, which bounds it and
// makes it inert. Nothing is sent before Enter, nothing is written, and the
// read is a GET, so it works in read-only mode.
//
// The reader is drawn in place of the tree and takes every key and wheel
// notch while it is open. What it read lives in filesState.rd alone: it is
// not in the index, the filter, the export or the job's output, and closing
// the reader drops it.

// readerMeta is what the archive's header says of the file as it is in the
// container now.
type readerMeta struct {
	size     int64
	mode     int64
	uid, gid int
	mtime    time.Time
}

// readerRun is the hand-over point between the read job and the desk.
type readerRun struct {
	jobID int

	mu      sync.Mutex
	read    int64 // bytes taken from the stream so far
	dropped bool  // the reader closed: the job must keep nothing
	done    bool
	pv      files.Preview
	meta    readerMeta
}

func (r *readerRun) progress() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read
}

// deliver hands the job's result over, unless the reader has closed.
func (r *readerRun) deliver(pv files.Preview, meta readerMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.dropped {
		r.pv, r.meta, r.done = pv, meta, true
	}
}

// take moves the result out: the run holds nothing afterwards.
func (r *readerRun) take() (files.Preview, readerMeta, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, meta, done := r.pv, r.meta, r.done
	r.pv, r.done = files.Preview{}, false
	return pv, meta, done
}

// drop makes the run keep nothing, now or when its job ends.
func (r *readerRun) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped, r.done, r.pv = true, false, files.Preview{}
}

// filesReader is the open reader.
type filesReader struct {
	path          string // raw: escaped where it is drawn
	containerID   string
	containerName string
	// What the tree says of the path: whether the container changed it, and
	// the image's size for it.
	change   files.ChangeKind
	inImage  bool
	treeSize int64

	run       *readerRun // while the job runs
	loaded    bool
	pv        files.Preview
	meta      readerMeta
	err       string
	cancelled bool

	view  textView
	pageH int
}

// ---- Enter ----------------------------------------------------------------------------

// filesEnter is Enter on the highlighted row, before the tree's own
// meaning of it (open a directory). It reports whether the key was used.
//
// On a container, a regular file opens the reader. So does a path the
// change list alone names (its type is not known until the daemon answers:
// the answer's header decides, and anything but a regular file is refused
// there, unread). Everything else that is not a directory is refused with
// what it is, and nothing is sent. With no row to act on, or on a
// directory, Enter is what it was: it reads the image's archive if that has
// not been read.
func (m *Model) filesEnter(here *files.Row) bool {
	fs := &m.fs
	if here != nil && !here.Node.IsDir() {
		n := here.Node
		name := files.Escape(n.Path)
		switch {
		case fs.kind != KindContainers && n.Type == files.TypeFile:
			m.setStatus(toneDim, "Image files are not read — nothing was sent. Open Files on a container made from it, or save the archive (w in Images).")
		case n.Change == files.Deleted || here.Gone:
			m.setStatus(toneDim, name+" was deleted by the container: there is nothing to read. Nothing was sent.")
		case n.Type == files.TypeSymlink:
			m.setStatus(toneDim, name+" is a symbolic link to "+files.Escape(n.Link)+": shown, never followed or opened. Nothing was sent.")
		case n.Type == files.TypeHardLink:
			m.setStatus(toneDim, name+" is a hard link to "+files.Escape(n.Link)+": links are not followed or opened. Nothing was sent.")
		case n.Type == files.TypeFile, n.Type == files.TypeUnknown:
			m.openReader(n)
		default:
			m.setStatus(toneDim, name+" is "+readerTypeWords(n.Type)+": only regular files are opened. Nothing was sent.")
		}
		return true
	}
	if m.filesBase() == nil {
		m.startFilesRead()
		return true
	}
	return false
}

// filesReadable reports whether Enter on the row would open the reader.
func (m *Model) filesReadable(here *files.Row) bool {
	if here == nil || m.fs.kind != KindContainers {
		return false
	}
	n := here.Node
	return (n.Type == files.TypeFile || n.Type == files.TypeUnknown) && n.Change != files.Deleted && !here.Gone
}

func readerTypeWords(t files.Type) string {
	switch t {
	case files.TypeChar:
		return "a character device"
	case files.TypeBlock:
		return "a block device"
	case files.TypeFIFO:
		return "a named pipe (FIFO)"
	case files.TypeDir:
		return "a directory"
	}
	return "an entry of another type (a socket, or one the archive does not name)"
}

// ---- the read -------------------------------------------------------------------------

// openReader opens the reader on a path of the container and starts its
// one request. The reader is on screen at once, in its reading state.
func (m *Model) openReader(n *files.Node) {
	fs := &m.fs
	rd := &filesReader{
		path: n.Path, containerID: fs.containerID, containerName: fs.containerName,
		change: n.Change, inImage: !n.FromChanges, treeSize: n.Size,
	}
	rd.view.wrap = true
	run := &readerRun{}
	eng := m.eng
	id, p := rd.containerID, rd.path
	target := files.Escape(rd.containerName) + ":" + files.Escape(p)
	run.jobID = m.startJob("read a container file", target, func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking the daemon for the file")
		var last time.Time
		pv, meta, err := readContainerFile(ctx, eng, id, p, func(read, size int64) {
			run.mu.Lock()
			run.read = read
			run.mu.Unlock()
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
				last = now
				r.Status(humanBytes(read) + " read")
				if want := min(size, files.PreviewMaxBytes); want > 0 {
					r.Fraction(min(float64(read)/float64(want), 0.99))
				}
			}
		})
		if err != nil {
			r.Logf("nothing was kept, and nothing was written on this machine")
			return err
		}
		// Counts and bounds only: never a byte of the file.
		r.Logf("from %s — a regular file of %s, by the archive's header", target, humanBytes(meta.size))
		r.Logf("%s", readerShownWords(pv))
		r.Logf("held in memory for the reader only; nothing was written, and the daemon's archive was only read (GET)")
		r.Status(humanBytes(pv.Read) + " read")
		run.deliver(pv, meta)
		return nil
	})
	rd.run = run
	fs.rd = rd
}

// readContainerFile is the reader's one request and what it accepts of the
// answer: exactly one tar entry, a regular file, named as the path's last
// element. Anything else is refused with what was received, and none of it
// is read. The stream is always closed.
func readContainerFile(ctx context.Context, eng engine.Engine, id, p string, progress func(read, size int64)) (files.Preview, readerMeta, error) {
	var none files.Preview
	base := path.Base(p)
	a, err := eng.CopyFromContainer(ctx, id, p)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return none, readerMeta{}, cerr
		}
		if engine.IsNotFound(err) {
			return none, readerMeta{}, fmt.Errorf("the path, or its container, is not there any more — the daemon says: %s", daemonWords(err))
		}
		return none, readerMeta{}, fmt.Errorf("the daemon refused: %s", daemonWords(err))
	}
	defer a.Content.Close()

	// The daemon's stat comes with the answer's headers: a directory or a
	// link is refused before a byte of the archive is read.
	st := a.Stat
	switch {
	case st.Name != base:
		return none, readerMeta{}, fmt.Errorf("the daemon answered for %s, not %s: nothing was read", shown(st.Name), shown(base))
	case st.IsDir():
		return none, readerMeta{}, fmt.Errorf("the daemon answered with a directory, not a regular file: nothing was read")
	case st.Mode&iofs.ModeSymlink != 0:
		return none, readerMeta{}, fmt.Errorf("the daemon answered with a symbolic link to %s: it is not followed, and nothing was read", shown(st.LinkTarget))
	case !st.Mode.IsRegular():
		return none, readerMeta{}, fmt.Errorf("the daemon answered with %s, not a regular file: nothing was read", statWords(st))
	}

	tr := tar.NewReader(a.Content)
	hdr, err := tr.Next()
	switch {
	case ctx.Err() != nil:
		return none, readerMeta{}, ctx.Err()
	case err == io.EOF:
		return none, readerMeta{}, fmt.Errorf("the daemon's archive is empty: nothing was read")
	case err != nil:
		return none, readerMeta{}, fmt.Errorf("the daemon's archive could not be read: %s", shown(err.Error()))
	case hdr.Typeflag != tar.TypeReg:
		return none, readerMeta{}, fmt.Errorf("the daemon's archive holds %s named %s, not a regular file: nothing was read", tarTypeWords(hdr), shown(hdr.Name))
	case hdr.Name != base:
		return none, readerMeta{}, fmt.Errorf("the daemon's archive holds %s, not %s: nothing was read", shown(hdr.Name), shown(base))
	case hdr.Size < 0:
		return none, readerMeta{}, fmt.Errorf("the daemon's archive gives the file a size of %d bytes: nothing was read", hdr.Size)
	}
	meta := readerMeta{size: hdr.Size, mode: hdr.Mode, uid: hdr.Uid, gid: hdr.Gid, mtime: hdr.ModTime}

	pv, err := files.ReadPreview(ctx, &progressReader{r: tr, size: hdr.Size, fn: progress}, hdr.Size)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return none, readerMeta{}, cerr
		}
		return none, readerMeta{}, fmt.Errorf("the read stopped: %s", shown(err.Error()))
	}
	// A file read to its end must be the archive's only entry. One that was
	// cut is not read on to find out: that would be the megabytes the bound
	// is there to spare.
	if !pv.CutBytes {
		switch next, err := tr.Next(); {
		case ctx.Err() != nil:
			return none, readerMeta{}, ctx.Err()
		case err == nil:
			return none, readerMeta{}, fmt.Errorf("the daemon's archive holds more than the one file asked for (%s as well): nothing was kept", shown(next.Name))
		case err != io.EOF:
			return none, readerMeta{}, fmt.Errorf("the daemon's archive did not end as an archive does: nothing was kept (%s)", shown(err.Error()))
		}
	}
	return pv, meta, nil
}

func statWords(st engine.PathStat) string {
	switch m := st.Mode; {
	case m&iofs.ModeNamedPipe != 0:
		return "a named pipe (FIFO)"
	case m&iofs.ModeSocket != 0:
		return "a socket"
	case m&iofs.ModeDevice != 0:
		return "a device"
	}
	return "something that is not a regular file"
}

func tarTypeWords(h *tar.Header) string {
	switch h.Typeflag {
	case tar.TypeDir:
		return "a directory"
	case tar.TypeSymlink:
		return "a symbolic link to " + shown(h.Linkname)
	case tar.TypeLink:
		return "a hard link to " + shown(h.Linkname)
	case tar.TypeChar:
		return "a character device"
	case tar.TypeBlock:
		return "a block device"
	case tar.TypeFifo:
		return "a named pipe (FIFO)"
	}
	return fmt.Sprintf("an entry of type %q", string(rune(h.Typeflag)))
}

// readerErrMax is how much of a daemon's message the reader repeats. A
// daemon may answer with a megabyte of text; a sentence is what is read.
const readerErrMax = 400

// shown is a string the daemon or an archive chose, as the reader repeats
// it in a sentence: escaped, and kept to readerErrMax of its bytes.
func shown(s string) string {
	if len(s) <= readerErrMax {
		return files.Escape(s)
	}
	return files.Escape(s[:readerErrMax]) + "… (cut)" // a character split by the cut is escaped byte by byte
}

func daemonWords(err error) string { return shown(plainError(err)) }

// progressReader reports how much has been read.
type progressReader struct {
	r    io.Reader
	n    int64
	size int64
	fn   func(read, size int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.fn != nil {
		p.fn(p.n, p.size)
	}
	return n, err
}

// collectReader is the frame tick's look at the read job: once it has
// ended, the reader shows what it kept, or why it kept nothing.
func (m *Model) collectReader() {
	rd := m.fs.rd
	if rd == nil || rd.run == nil {
		return
	}
	j, ok := m.jobs.Get(rd.run.jobID)
	if ok && j.State == jobs.Running {
		return
	}
	run := rd.run
	rd.run = nil
	m.dirty = true
	pv, meta, done := run.take()
	switch {
	case ok && j.State == jobs.Succeeded && done:
		rd.pv, rd.meta, rd.loaded = pv, meta, true
		rd.view.set(pv.Text)
	case ok && j.State == jobs.Cancelled:
		rd.cancelled = true
	case ok && j.Err != "":
		rd.err = j.Err
	default:
		rd.err = "the read ended without an answer"
	}
}

// closeReader drops the reader and everything it read. A read still
// running is cancelled, and its job keeps nothing.
func (m *Model) closeReader() {
	rd := m.fs.rd
	if rd == nil {
		return
	}
	if rd.run != nil {
		rd.run.drop()
		m.jobs.Cancel(rd.run.jobID)
	}
	m.fs.rd = nil
	m.dirty = true
}

// ---- keys and the pointer -------------------------------------------------------------

// onReaderKey takes every key while the reader is open: it scrolls, wraps,
// cancels, opens the key list or closes, and nothing else. No key reaches
// the tree behind it.
func (m *Model) onReaderKey(k tea.KeyMsg) tea.Cmd {
	rd := m.fs.rd
	page := max(rd.pageH-1, 1)
	switch k.String() {
	case "esc", "q":
		m.closeReader()
	case "c":
		if rd.run != nil {
			m.jobs.Cancel(rd.run.jobID)
		}
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "w":
		rd.view.toggleWrap()
	case "down", "j":
		rd.view.scroll(1)
	case "up", "k":
		rd.view.scroll(-1)
	case "pgdown", "ctrl+f", " ":
		rd.view.scroll(page)
	case "pgup", "ctrl+b":
		rd.view.scroll(-page)
	case "g", "home":
		rd.view.off = 0
	case "G", "end":
		rd.view.off = len(rd.view.wrapped) // render holds it at the last row
	}
	return nil
}

// mouseReader is the pointer while the reader is open: the wheel scrolls
// it, and a click does nothing — there is nothing behind it to reach.
func (m *Model) mouseReader(delta int) {
	if delta != 0 {
		m.fs.rd.view.scroll(mouseWheelRows * delta)
	}
}

func (m *Model) readerHints() []string {
	rd := m.fs.rd
	if rd.run != nil {
		return []string{"c", "cancel the read"}
	}
	if !rd.loaded || rd.pv.Binary {
		return nil
	}
	wrap := "wrap"
	if rd.view.wrap {
		wrap = "no wrap"
	}
	return []string{"↑↓", "scroll", "PgUp PgDn", "page", "g G", "ends", "w", wrap}
}

// ---- drawing --------------------------------------------------------------------------

// readerShownWords says how much of the file the preview holds, and names
// every bound that was reached.
func readerShownWords(pv files.Preview) string {
	if pv.Binary {
		s := fmt.Sprintf("not text: the %s read hold a NUL byte or are not valid UTF-8, so no contents are shown, and there is no hex view", humanBytes(pv.Read))
		if pv.CutBytes {
			s = fmt.Sprintf("not text: the first 1 MiB of %s holds a NUL byte or is not valid UTF-8, so no contents are shown, and there is no hex view", humanBytes(pv.Size))
		}
		return s
	}
	if pv.Whole() {
		if pv.Read == 0 {
			return "the whole file: it is empty"
		}
		return fmt.Sprintf("the whole file: %s, %s", files.Plural(pv.Lines, "line", "lines"), humanBytes(pv.Read))
	}
	var parts []string
	if pv.CutBytes {
		parts = append(parts, fmt.Sprintf("the first 1 MiB of %s: the rest was cut, unread", humanBytes(pv.Size)))
	}
	if pv.CutLines {
		parts = append(parts, fmt.Sprintf("the first %s lines: the lines after them were cut", files.Count(files.PreviewMaxLines)))
	} else {
		parts = append(parts, files.Plural(pv.Lines, "line", "lines"))
	}
	if pv.CutLine {
		parts = append(parts, fmt.Sprintf("%s longer than 2 KiB cut there", files.Plural(pv.LongLines, "line", "lines")))
	}
	return "cut — " + strings.Join(parts, " · ") + " (o copies the whole file out)"
}

// readerHead is what stays above the text: the path, the container, what
// the file is now, where it was read from and how much of it is shown.
func (m *Model) readerHead(w int) []string {
	st := m.st
	rd := m.fs.rd
	var out []string
	kv := func(k, v string, tn tone) {
		wrap := wrapWords
		if k == "path" {
			wrap = wrapLine // a path breaks anywhere
		}
		for i, part := range wrap(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			out = append(out, label+st.tone(tn, part))
		}
	}
	kv("path", files.Escape(rd.path), toneNormal)
	kv("container", files.Escape(rd.containerName)+" · "+engine.ShortID(rd.containerID), toneNormal)
	const source = "the container's current filesystem, as the daemon read it for this request — not a snapshot, and not the image's file"
	switch {
	case rd.run != nil:
		kv("reading", fmt.Sprintf("%s one request for this path (GET /containers/{id}/archive): %s read — c cancels", spinnerFrames[m.spinner], humanBytes(rd.run.progress())), toneAccent)
	case rd.cancelled:
		kv("cancelled", "The read was cancelled: the stream was closed and nothing was kept.", toneWarn)
	case rd.err != "":
		kv("not read", capitalise(rd.err)+". No contents were kept.", toneBad)
	case rd.loaded:
		me := rd.meta
		what := fmt.Sprintf("regular file · mode %04o (%s) · uid %d gid %d", me.mode&0o7777, modeString(&files.Node{Type: files.TypeFile, Mode: me.mode}), me.uid, me.gid)
		if !me.mtime.IsZero() {
			what += " · modified " + me.mtime.UTC().Format("2006-01-02 15:04:05") + " UTC"
		}
		kv("type", what, toneNormal)
		size := humanBytes(me.size) + " in the container now"
		tn := toneNormal
		switch {
		case rd.inImage && rd.change == files.Changed:
			size += fmt.Sprintf("; the tree's %s is the image's — the container changed this file", humanBytes(rd.treeSize))
			tn = toneWarn
		case rd.inImage:
			size += fmt.Sprintf("; the tree's %s is the image's", humanBytes(rd.treeSize))
		}
		kv("size", size, tn)
		kv("source", source, toneNormal)
		tn = toneNormal
		if rd.pv.Binary || !rd.pv.Whole() {
			tn = toneWarn
		}
		kv("shown", readerShownWords(rd.pv), tn)
	}
	if !rd.loaded {
		kv("source", source, toneDim)
	}
	return out
}

// readerBox draws the reader in place of the tree: w×h cells. It records
// nothing in the hit map, so the box the view covers the panes with is all
// a click can land on.
func (m *Model) readerBox(w, h int) []string {
	st := m.st
	rd := m.fs.rd
	inner := w - 4
	bodyH := h - 2
	head := m.readerHead(inner)
	if room := max(bodyH-4, 1); len(head) > room {
		head = head[:room] // a short terminal: the text keeps three rows
	}
	textH := max(bodyH-len(head)-1, 0)
	rd.pageH = textH

	var text []string
	note := ""
	switch {
	case !rd.loaded:
	case rd.pv.Binary:
		for _, l := range wrapWords("Not text. The reader shows valid UTF-8 without NUL bytes only: no bytes of this file are drawn, and there is no hex view. o in the Files view copies it out.", inner) {
			text = append(text, st.tone(toneDim, l))
		}
	case len(rd.pv.Text) == 0:
		text = append(text, st.tone(toneDim, "The file is empty."))
	default:
		text = rd.view.render(inner, textH)
		note = rd.view.position(textH)
		if note == "" {
			note = "all of it"
		} else {
			note = "rows " + note
		}
		if rd.view.wrap {
			note += " · wrapped"
		} else {
			note += " · long lines end at the edge, w wraps"
		}
	}
	if len(text) > textH {
		text = text[:textH]
	}
	rule := strings.Repeat("─", max(inner, 0))
	if note != "" {
		note = " " + note + " "
		rule = strings.Repeat("─", max(inner-len([]rune(note))-1, 0)) + note + "─"
	}
	body := append(append([]string{}, head...), st.tone(toneDim, rule))
	body = append(body, text...)
	for len(body) < bodyH {
		body = append(body, "")
	}
	padded := make([]string, 0, len(body))
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner)+" ")
	}
	title := "File — read from the container, shown here only"
	if w < wideMinWidth {
		title = "File"
	}
	return st.box(title, files.Escape(rd.containerName), padded, w, h, true)
}
