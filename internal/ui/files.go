package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/report/collect"
)

// The Files view: what is inside an image — its layers merged into one
// tree, each path with its type, size, mode and the layer and build step
// that put it there — and, on a container, the daemon's own list of what
// the container added, changed or deleted laid over its image's tree
// (docs/DECISIONS.md, round fifteen, V1–V22).
//
// F opens it on the highlighted image or container. It opens on facts the
// desk already holds and says nothing has been read: an image's archive is
// the one expensive read, and only Enter starts it, as one cancellable job.
// A container's change list is one small GET, read when the view opens and
// on r. No file's contents are ever read, and every name is escaped
// (files.Escape) before it is drawn or written.

// filesCacheSize is how many image indexes are kept: the one shown and the
// one before it. An index is tens of megabytes at its bound (V11).
const filesCacheSize = 2

// filesAutoOpen is the largest change list whose directories are opened
// for the operator when a container's overlay is first drawn.
const filesAutoOpen = 64

type filesEntry struct {
	imageID string
	ix      *files.Index
	plat    string // the platform the archive was read for
}

// filesRun is the hand-over point between the archive job and the desk.
type filesRun struct {
	imageID, ref string
	jobID        int

	mu   sync.Mutex
	prog forensics.Progress
	ix   *files.Index
	plat string
}

func (r *filesRun) snapshot() (forensics.Progress, *files.Index, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prog, r.ix, r.plat
}

type filesChangesMsg struct {
	seq         uint64
	containerID string
	changes     []engine.Change
	err         error
	at          time.Time
}

type filesState struct {
	open bool
	// What the desk looked like when the view opened; put back on close.
	prevFocus Pane
	prevKind  Kind
	prevKey   string

	// The object the view is on.
	kind          Kind // KindImages or KindContainers
	imageID       string
	imageRef      string
	imageSize     int64 // the daemon's, from the image list; 0 unknown
	plat          engine.Platform
	platKnown     bool
	layers        int // from the image's inspect; 0 unknown
	containerID   string
	containerName string

	cache  []filesEntry // least recently used first
	run    *filesRun
	failed map[string]string // image ID → why its last read kept no index

	// The container's change list.
	chgSeq  uint64
	chgBusy bool
	chgOf   string // the container the list below is of
	changes []files.Change
	chgRead bool
	chgAt   time.Time
	chgErr  string
	chgVer  int

	overlay struct {
		base *files.Index
		of   string
		ver  int
		ix   *files.Index
	}
	autoOpened string

	expanded map[string]bool
	expVer   int
	order    files.Order
	filter   string
	typing   bool
	before   string
	cursor   string
	off      int
	listH    int
	drawnSec int64
	// top holds the list at its first line, so a header taller than the
	// list can be read: the view opens so, and k or g at the first row
	// return to it; curShown is whether the highlighted row was drawn.
	top      bool
	curShown bool

	memo struct {
		ix     *files.Index
		expVer int
		filter string
		order  files.Order
		view   files.View
	}
}

// ---- opening and closing ------------------------------------------------------

// openFiles is F: the view on the highlighted image or container.
func (m *Model) openFiles() tea.Cmd {
	r, ok := m.currentRow()
	if !ok || (m.kind != KindImages && m.kind != KindContainers) {
		m.setStatus(toneDim, "F opens the Files view on an image or a container: highlight one in Images or Containers.")
		return nil
	}
	fs := &m.fs
	var imageID, ref, cid, cname string
	switch m.kind {
	case KindImages:
		imageID, ref = r.imageID, r.imageRef
	case KindContainers:
		for _, c := range m.snap.Containers {
			if c.ID == r.containerID {
				imageID, ref, cid, cname = c.ImageID, shortImage(c.Image), c.ID, c.Name
			}
		}
		if cid == "" {
			m.setStatus(toneDim, "The highlighted container is not in the list any more.")
			return nil
		}
	}
	if ref == "" {
		ref = engine.ShortID(imageID)
	}
	if fs.kind != m.kind || fs.imageID != imageID || fs.containerID != cid {
		// Another object: from the top, keeping the order, which is the
		// operator's choice rather than the object's.
		fs.expanded, fs.expVer, fs.cursor, fs.filter, fs.off = map[string]bool{}, fs.expVer+1, "", "", 0
	}
	if fs.expanded == nil {
		fs.expanded = map[string]bool{}
	}
	fs.open, fs.typing, fs.top = true, false, true
	fs.prevFocus, fs.prevKind, fs.prevKey = m.focus, m.kind, m.sel[m.kind].key
	fs.kind, fs.imageID, fs.imageRef, fs.containerID, fs.containerName = m.kind, imageID, ref, cid, cname
	fs.imageSize, fs.layers, fs.platKnown = 0, 0, false
	for _, img := range m.snap.Images {
		if img.ID == imageID {
			fs.imageSize = img.Size
		}
	}
	if d := m.detail.image; d != nil && d.ID == imageID {
		fs.plat, fs.platKnown, fs.layers = d.Platform(), true, d.Layers
	}
	if e := m.filesEntryFor(imageID); e != nil {
		m.touchFiles(imageID)
	}
	if fs.kind == KindContainers && fs.chgOf != cid {
		fs.changes, fs.chgRead, fs.chgErr, fs.chgOf = nil, false, "", cid
		fs.chgVer++
	}
	if fs.kind == KindContainers {
		return m.readChanges()
	}
	return nil
}

// closeFiles puts the desk back as it was when the view opened. An archive
// read still running is not wanted any more: it is cancelled, and keeps
// nothing.
func (m *Model) closeFiles() {
	fs := &m.fs
	fs.open, fs.typing = false, false
	if fs.run != nil {
		m.jobs.Cancel(fs.run.jobID)
	}
	m.focus = fs.prevFocus
	m.setKind(fs.prevKind)
	for i, r := range m.rows[m.kind] {
		if r.key == fs.prevKey {
			m.selectIndex(i)
			break
		}
	}
}

// ---- the two reads ------------------------------------------------------------

func (m *Model) filesEntryFor(imageID string) *filesEntry {
	for i := range m.fs.cache {
		if m.fs.cache[i].imageID == imageID {
			return &m.fs.cache[i]
		}
	}
	return nil
}

func (m *Model) touchFiles(imageID string) {
	c := m.fs.cache
	for i, e := range c {
		if e.imageID == imageID {
			m.fs.cache = append(append(c[:i:i], c[i+1:]...), e)
			return
		}
	}
}

func (m *Model) storeFiles(e filesEntry) {
	fs := &m.fs
	for i, x := range fs.cache {
		if x.imageID == e.imageID {
			fs.cache = append(fs.cache[:i:i], fs.cache[i+1:]...)
			break
		}
	}
	fs.cache = append(fs.cache, e)
	for len(fs.cache) > filesCacheSize {
		fs.cache = fs.cache[1:] // the oldest index is dropped, not kept aside
	}
}

// filesBase is the image's tree, if it has been read.
func (m *Model) filesBase() *files.Index {
	if e := m.filesEntryFor(m.fs.imageID); e != nil {
		return e.ix
	}
	return nil
}

// startFilesRead is Enter before the image has been read: one job that
// streams the archive through the analyser's headers-only mode and builds
// the index. The job's title, status and log carry counts and the desk's
// own words only — never a path or a byte of the image.
func (m *Model) startFilesRead() {
	fs := &m.fs
	if fs.run != nil || fs.imageID == "" {
		return
	}
	delete(fs.failed, fs.imageID)
	run := &filesRun{imageID: fs.imageID, ref: fs.imageRef}
	eng := m.eng
	id := fs.imageID
	var plat engine.Platform
	opts := forensics.Options{Mode: forensics.ModeFiles, ImageID: id, Ref: fs.imageRef}
	if fs.platKnown {
		plat, opts.Platform = fs.plat, fs.plat.String()
	}
	size := fs.imageSize
	run.jobID = m.startJob("read the image archive", fs.imageRef, func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking the daemon for the archive")
		rc, err := eng.SaveImage(ctx, id, plat)
		if err != nil {
			return err
		}
		defer rc.Close()
		var last time.Time
		rep, err := forensics.Analyse(ctx, rc, opts, func(p forensics.Progress) {
			run.mu.Lock()
			run.prog = p
			run.mu.Unlock()
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
				last = now
				r.Status(fmt.Sprintf("%s read · %d layers · %s entries", humanBytes(p.BytesRead), p.Layers, files.Count(p.Entries)))
				if size > 0 {
					r.Fraction(min(float64(p.BytesRead)/float64(size), 0.99))
				}
			}
		})
		if err != nil {
			return err
		}
		r.Status("merging the layers")
		ix := files.Build(rep)
		if ix == nil {
			return fmt.Errorf("the archive was read but no listing was made")
		}
		r.Logf("%s layout, %d layers, %s read", rep.Layout, len(rep.Layers), humanBytes(rep.BytesRead))
		r.Logf("%s entries indexed of %s seen; %d layer(s) not read", files.Count(ix.Kept), files.Count(ix.Seen), len(ix.Unread))
		r.Status(fmt.Sprintf("%d layers · %s entries", len(rep.Layers), files.Count(ix.Entries)))
		plat := rep.Platform
		if rep.Config.Present {
			plat = rep.Config.Platform()
		}
		run.mu.Lock()
		run.ix, run.plat = ix, plat
		run.mu.Unlock()
		return nil
	})
	fs.run = run
}

// collectFiles takes the index of a read that has ended. Cancelled, failed
// or broken, nothing is kept, and the view says why.
func (m *Model) collectFiles() {
	fs := &m.fs
	run := fs.run
	if run == nil {
		return
	}
	j, ok := m.jobs.Get(run.jobID)
	if !ok || j.State == jobs.Running {
		return
	}
	fs.run = nil
	m.dirty = true
	_, ix, plat := run.snapshot()
	if fs.failed == nil {
		fs.failed = map[string]string{}
	}
	switch {
	case j.State == jobs.Succeeded && ix != nil:
		m.storeFiles(filesEntry{imageID: run.imageID, ix: ix, plat: plat})
		delete(fs.failed, run.imageID)
	case j.State == jobs.Cancelled:
		fs.failed[run.imageID] = "cancelled: the stream was closed and nothing was kept."
	default:
		fs.failed[run.imageID] = j.Err
	}
}

// readChanges asks the daemon for the container's change list: one GET.
func (m *Model) readChanges() tea.Cmd {
	fs := &m.fs
	if fs.containerID == "" {
		return nil
	}
	fs.chgSeq++
	fs.chgBusy = true
	seq, id, eng := fs.chgSeq, fs.containerID, m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cs, err := eng.ContainerChanges(ctx, id)
		return filesChangesMsg{seq: seq, containerID: id, changes: cs, err: err, at: time.Now()}
	}
}

func (m *Model) onFilesChanges(msg filesChangesMsg) {
	fs := &m.fs
	if msg.seq != fs.chgSeq || msg.containerID != fs.containerID {
		return // the operator has asked again, or moved on
	}
	fs.chgBusy = false
	if msg.err != nil {
		fs.chgErr = plainError(msg.err)
		return
	}
	cs := make([]files.Change, 0, len(msg.changes))
	for _, c := range msg.changes {
		k := files.ChangeKind(-1) // a kind the API does not define is ignored
		switch c.Kind {
		case engine.ChangeModified:
			k = files.Changed
		case engine.ChangeAdded:
			k = files.Added
		case engine.ChangeDeleted:
			k = files.Deleted
		}
		cs = append(cs, files.Change{Path: c.Path, Kind: k})
	}
	fs.changes, fs.chgRead, fs.chgErr, fs.chgAt, fs.chgOf = cs, true, "", msg.at, msg.containerID
	fs.chgVer++
}

// filesTree is the tree the view draws: the image's, or, on a container,
// the image's with the changes over it — or the changes alone.
func (m *Model) filesTree() *files.Index {
	fs := &m.fs
	base := m.filesBase()
	if fs.kind != KindContainers || !fs.chgRead {
		return base
	}
	o := &fs.overlay
	if o.ix == nil || o.base != base || o.of != fs.containerID || o.ver != fs.chgVer {
		o.base, o.of, o.ver = base, fs.containerID, fs.chgVer
		o.ix = files.Apply(base, fs.changes)
		// A short list is opened for the operator: every directory with a
		// change beneath it. A long one is left closed; the counts on the
		// collapsed rows say where to look.
		key := fmt.Sprintf("%s|%d|%v", fs.containerID, fs.chgVer, base != nil)
		if fs.autoOpened != key && o.ix.Applied.Total() <= filesAutoOpen {
			fs.autoOpened = key
			var walk func(n *files.Node)
			walk = func(n *files.Node) {
				for _, k := range n.Children() {
					if k.Beneath > 0 && k.Expandable() {
						fs.expanded[k.Path] = true
						walk(k)
					}
				}
			}
			walk(o.ix.Root)
			fs.expVer++
		}
	}
	return o.ix
}

func (m *Model) filesView() (files.View, *files.Index) {
	fs := &m.fs
	ix := m.filesTree()
	mm := &fs.memo
	if mm.ix != ix || mm.expVer != fs.expVer || mm.filter != fs.filter || mm.order != fs.order || mm.view.Rows == nil && ix != nil {
		mm.ix, mm.expVer, mm.filter, mm.order = ix, fs.expVer, fs.filter, fs.order
		mm.view = files.Flatten(ix, fs.expanded, fs.filter, fs.order)
		if mm.view.Rows == nil {
			mm.view.Rows = []files.Row{}
		}
	}
	return mm.view, ix
}

// filesCursor returns the row the cursor is drawn on: its path's, or its
// nearest shown ancestor's. A cursor with neither goes to the first row.
func (m *Model) filesCursor(v files.View) int {
	if len(v.Rows) == 0 {
		return -1
	}
	if i := v.Find(m.fs.cursor); i >= 0 && m.fs.cursor != "" {
		return i
	}
	m.fs.cursor = v.Rows[0].Node.Path
	return 0
}

// ---- keys ----------------------------------------------------------------------------

func (m *Model) onFilesKey(k tea.KeyMsg) tea.Cmd {
	fs := &m.fs
	if fs.typing {
		m.onFilesFilterKey(k)
		return nil
	}
	key := k.String()
	v, ix := m.filesView()
	cur := m.filesCursor(v)
	page := max(fs.listH-1, 1)
	var here *files.Row
	if cur >= 0 {
		here = &v.Rows[cur]
	}
	switch key {
	case "esc", "F":
		if fs.filter != "" && key == "esc" {
			m.clearFilesFilter() // Esc clears the filter first, then closes
			return nil
		}
		m.closeFiles()
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "T":
		m.setTheme(nextTheme(m.st.theme))
	case "/":
		if ix != nil {
			fs.typing, fs.before = true, fs.filter
		}
	case "s":
		fs.order = fs.order.Next()
	case "w":
		m.exportFiles()
	case "r":
		if fs.kind == KindContainers && !fs.chgBusy {
			return m.readChanges() // the change list only; the image's tree is not read again
		}
	case "c":
		if fs.run != nil {
			m.jobs.Cancel(fs.run.jobID)
		}
	case "enter", "right", "l":
		if key == "enter" && m.filesBase() == nil {
			m.startFilesRead()
			return nil
		}
		if here == nil {
			return nil
		}
		fs.cursor = here.Node.Path
		switch {
		case !here.Node.Expandable():
		case !here.Open && fs.filter == "":
			fs.expanded[here.Node.Path] = true
			fs.expVer++
		case here.Open && cur+1 < len(v.Rows):
			fs.cursor = v.Rows[cur+1].Node.Path
		}
	case "left", "h":
		if here == nil {
			return nil
		}
		fs.cursor = here.Node.Path
		if here.Open && fs.filter == "" {
			delete(fs.expanded, here.Node.Path)
			fs.expVer++
		} else if p := files.Parent(here.Node.Path); p != "" {
			fs.cursor = p
		}
	case "down", "j", "pgdown", "ctrl+f", " ", "G", "end":
		if fs.top && !fs.curShown && len(v.Rows) > 0 {
			fs.top = false // first bring the highlighted row into view
			return nil
		}
		fs.top = false
		switch key {
		case "down", "j":
			fs.cursor = v.Step(fs.cursor, 1)
		case "G", "end":
			fs.cursor = v.Bottom()
		default:
			fs.cursor = v.Step(fs.cursor, page)
		}
	case "up", "k", "pgup", "ctrl+b", "g", "home":
		switch key {
		case "up", "k":
			fs.cursor = v.Step(fs.cursor, -1)
		case "g", "home":
			fs.cursor = v.Top()
		default:
			fs.cursor = v.Step(fs.cursor, -page)
		}
		fs.top = cur <= 0 || key == "g" || key == "home"
	}
	return nil
}

// clearFilesFilter drops the filter and keeps the cursor on its path: the
// directories above it are opened, so it is still on screen.
func (m *Model) clearFilesFilter() {
	fs := &m.fs
	fs.filter = ""
	if fs.cursor != "" {
		for p := files.Parent(fs.cursor); p != ""; p = files.Parent(p) {
			fs.expanded[p] = true
		}
		fs.expVer++
	}
}

func (m *Model) onFilesFilterKey(k tea.KeyMsg) {
	fs := &m.fs
	fs.top = false // what the filter finds is what the operator looks at
	switch k.Type {
	case tea.KeyEnter:
		fs.typing = false
	case tea.KeyEsc:
		fs.typing, fs.filter = false, fs.before
	case tea.KeyBackspace:
		if r := []rune(fs.filter); len(r) > 0 {
			fs.filter = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		fs.filter = ""
	case tea.KeyRunes, tea.KeySpace:
		if len(fs.filter) < 120 {
			fs.filter += string(k.Runes)
		}
	}
}

func (m *Model) filesHints() []string {
	fs := &m.fs
	if fs.typing {
		return []string{"type", "to filter paths", "Enter", "keep", "Esc", "undo"}
	}
	var pairs []string
	if m.filesBase() == nil && fs.run == nil {
		pairs = append(pairs, "⏎", "read the archive")
	}
	if fs.run != nil {
		pairs = append(pairs, "c", "cancel")
	}
	pairs = append(pairs, "↑↓", "move", "→←", "open", "s", "order", "/", "filter", "w", "export")
	if fs.kind == KindContainers {
		pairs = append(pairs, "r", "changes")
	}
	if fs.filter != "" {
		pairs = append(pairs, "Esc", "clear filter")
	}
	return pairs
}

// ---- drawing ---------------------------------------------------------------------

type filesCols struct{ name, size, typ, layer, change int }

const filesGap = 2

func filesColumns(w int, container bool) filesCols {
	c := filesCols{size: 7, typ: 8, layer: 5}
	gaps := 3
	if container {
		c.change, gaps = 9, 4
	}
	c.name = max(w-c.size-c.typ-c.layer-c.change-gaps*filesGap, 16)
	return c
}

func (c filesCols) join(name, size, typ, layer, change string) string {
	gap := strings.Repeat(" ", filesGap)
	s := leftFit(name, c.name) + gap + fitRight(size, c.size) + gap + fit(typ, c.typ) + gap + fit(layer, c.layer)
	if c.change > 0 {
		s += gap + fit(change, c.change)
	}
	return s
}

// leftFit fits s in w cells, cutting on the left: the end of a name — the
// part a person reads — stays visible (…/local/bin/python3.12).
func leftFit(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw <= w {
		return s + strings.Repeat(" ", w-sw)
	}
	if w <= 1 {
		return fit("…", w)
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return fit("…"+string(r), w)
}

func filesColumnTitles(c filesCols) string {
	return c.join("NAME", "SIZE", "TYPE", "LAYER", "CHANGE")
}

func filesSize(n *files.Node) string {
	if n.FromChanges {
		return "—"
	}
	return humanBytes(n.Size)
}

func filesLayer(n *files.Node, ix *files.Index) string {
	switch {
	case n.FromChanges:
		return "—"
	case n.IsDir() || n.Layer == 0:
		return ""
	case ix != nil && len(ix.Unsure(n.Layer)) > 0:
		return fmt.Sprintf("L%d?", n.Layer) // a later layer was not read
	}
	return fmt.Sprintf("L%d", n.Layer)
}

func filesChange(r files.Row) string {
	n := r.Node
	switch {
	case n.Change != files.Unchanged:
		return n.Change.String()
	case r.Gone:
		return "(deleted)"
	case n.Beneath > 0 && !r.Open:
		return fmt.Sprintf("%d below", n.Beneath)
	}
	return ""
}

var strike = lipgloss.NewStyle().Strikethrough(true)

// compactCount fits a count in four cells: 999, 12k, 200k, 1.2M.
func compactCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

func (m *Model) filesRowText(r files.Row, c filesCols, ix *files.Index) string {
	n := r.Node
	mark := "  "
	if n.Expandable() {
		mark = "+ "
		if r.Open {
			mark = "- "
		}
	}
	name := files.Escape(n.Name)
	if n.IsDir() {
		name += "/"
	}
	if n.Type == files.TypeSymlink || n.Type == files.TypeHardLink {
		name += " -> " + files.Escape(n.Link)
	}
	typ := n.Type.String()
	if n.IsDir() && !n.FromChanges {
		typ += " " + compactCount(n.Files) // the files it holds, beside the size they add up to
	}
	text := c.join(strings.Repeat("  ", r.Depth)+mark+name, filesSize(n), typ, filesLayer(n, ix), filesChange(r))
	switch {
	case n.Change == files.Deleted || r.Gone:
		return m.st.tone(toneDim, strike.Render(text))
	case n.Change == files.Added:
		return m.st.tone(toneGood, text)
	case n.Change == files.Changed:
		return m.st.tone(toneWarn, text)
	}
	return text
}

// filesBox draws the view over the desk's body: w×h cells.
func (m *Model) filesBox(w, h int) []string {
	st := m.st
	fs := &m.fs
	inner := w - 4
	bodyH := h - 2
	v, ix := m.filesView()
	cur := m.filesCursor(v)
	container := fs.kind == KindContainers
	cols := filesColumns(inner-2, container)

	head := m.filesHeaderLines(inner, ix)
	var lines []string // the scrolling part: header, titles, rows
	lines = append(lines, head...)
	rowsAt := -1
	switch {
	case ix == nil:
	case len(v.Rows) == 0 && fs.filter != "":
		lines = append(lines, st.tone(toneDim, filesColumnTitles(cols)),
			st.tone(toneWarn, fmt.Sprintf("No path matches %q — %s searched.", files.Escape(fs.filter), files.Count(v.Searched))))
	case len(v.Rows) == 0:
		lines = append(lines, st.tone(toneDim, filesColumnTitles(cols)), st.tone(toneDim, "The tree is empty."))
	default:
		lines = append(lines, st.tone(toneDim, filesColumnTitles(cols)))
		rowsAt = len(lines)
	}
	total := len(lines)
	if rowsAt >= 0 {
		total += len(v.Rows)
	}

	var detail []string
	if cur >= 0 {
		detail = m.filesDetail(v.Rows[cur], ix, inner)
	}
	detailH := clamp(bodyH/3, 3, 8)
	if len(detail) > detailH {
		detail = detail[:detailH]
	}
	for len(detail) < detailH {
		detail = append(detail, "")
	}
	fixed := m.filesShowingLines(inner, v, ix)
	listH := max(bodyH-len(detail)-len(fixed)-1, 3)
	fs.listH = listH

	curLine := -1
	if cur >= 0 && rowsAt >= 0 {
		curLine = rowsAt + cur
	}
	if curLine >= 0 {
		fs.off = clamp(fs.off, curLine-listH+1, curLine)
		if cur == 0 && curLine < listH {
			fs.off = 0 // the header above the first row stays in view
		}
	}
	fs.off = clamp(fs.off, 0, max(total-listH, 0))
	if fs.top {
		fs.off = 0
	}
	fs.curShown = curLine >= fs.off && curLine < fs.off+listH

	body := append([]string{}, fixed...)
	for i := fs.off; i < total && len(body) < listH+len(fixed); i++ {
		if rowsAt < 0 || i < rowsAt {
			body = append(body, "  "+lines[i])
			continue
		}
		r := i - rowsAt
		text := m.filesRowText(v.Rows[r], cols, ix)
		if r == cur {
			body = append(body, st.selected.Render("▸ "+fit(ansi.Strip(text), inner-2)))
		} else {
			body = append(body, "  "+text)
		}
	}
	for len(body) < listH+len(fixed) {
		body = append(body, "")
	}
	pos := " no rows "
	if cur >= 0 {
		pos = fmt.Sprintf(" %s of %s ", files.Count(cur+1), files.Count(len(v.Rows)))
	}
	body = append(body, st.tone(toneDim, strings.Repeat("─", max(inner-len([]rune(pos))-1, 0))+pos+"─"))
	body = append(body, detail...)

	padded := make([]string, 0, len(body))
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner)+" ")
	}
	title := "Files — the image's layers merged into one tree"
	if container {
		title = "Files — the image's tree with the container's changes over it"
	}
	if w < wideMinWidth {
		title = "Files"
	}
	right := fs.imageRef
	if container {
		right = fs.containerName
	}
	return st.box(title, right, padded, w, h, true)
}

// filesShowingLines is what stays above the list: its order, the rows,
// the filter and what it found, and the may-miss warning.
func (m *Model) filesShowingLines(w int, v files.View, ix *files.Index) []string {
	st := m.st
	fs := &m.fs
	if ix == nil {
		return nil
	}
	parts := []string{"by " + st.tone(toneAccent, fs.order.String()), fmt.Sprintf("%s rows", files.Count(len(v.Rows)))}
	switch {
	case fs.typing:
		parts = append(parts, "filter: "+st.tone(toneAccent, files.Escape(fs.filter)+"▏"))
	case fs.filter != "":
		parts = append(parts, "filter: "+st.tone(toneAccent, files.Escape(fs.filter)))
	}
	if fs.filter != "" {
		parts = append(parts, fmt.Sprintf("%s of %s searched", files.Plural(v.Matches, "match", "matches"), files.Count(v.Searched)))
	}
	out := []string{fit(strings.Join(parts, st.tone(toneDim, " · ")), w)}
	if fs.filter != "" && v.MayMiss {
		out = append(out, st.tone(toneWarn, fit("The index is incomplete (a bound, or a layer not read): the filter may miss a path the image holds.", w)))
	}
	return out
}

// filesHeaderLines is what the view opens on: the object, what has been
// read and what has not, and the index's totals.
func (m *Model) filesHeaderLines(w int, ix *files.Index) []string {
	st := m.st
	fs := &m.fs
	now := time.Now()
	var out []string
	add := func(label, text string, tn tone) {
		for i, part := range wrapWords(text, max(w-13, 20)) {
			l := "           "
			if i == 0 {
				l = st.tone(toneDim, fit(label, 10)) + " "
			}
			out = append(out, l+st.tone(tn, part))
		}
	}
	base := m.filesBase()
	if d := m.detail.image; !fs.platKnown && d != nil && d.ID == fs.imageID {
		// The Details pane's inspect arrived after the view opened.
		fs.plat, fs.platKnown, fs.layers = d.Platform(), true, d.Layers
	}
	size := "not known"
	if fs.imageSize > 0 {
		size = humanBytes(fs.imageSize)
	}

	if fs.kind == KindContainers {
		add("container", fmt.Sprintf("%s · %s · from %s (%s)", files.Escape(fs.containerName), engine.ShortID(fs.containerID), fs.imageRef, engine.ShortID(fs.imageID)), toneNormal)
		switch {
		case fs.chgErr != "":
			add("changes", "not read: "+fs.chgErr+" — r tries again", toneBad)
		case !fs.chgRead:
			add("changes", "reading the daemon's change list (GET /containers/{id}/changes)…", toneDim)
		default:
			s := fmt.Sprintf("%s listed by the daemon, read %s ago · r reads them again", files.Count(len(fs.changes)), humanAge(fs.chgAt, now))
			if fs.chgBusy {
				s += " (reading…)"
			}
			add("changes", s, toneNormal)
			if ix != nil && ix.Applied != nil {
				m.filesOverlayLines(add, ix, base != nil)
			}
		}
	}

	facts := []string{fs.imageRef, engine.ShortID(fs.imageID)}
	if e := m.filesEntryFor(fs.imageID); e != nil && e.plat != "" {
		facts = append(facts, files.Escape(e.plat))
	} else if fs.platKnown {
		facts = append(facts, fs.plat.String())
	} else {
		facts = append(facts, "platform: the daemon's default")
	}
	switch {
	case base != nil:
		facts = append(facts, fmt.Sprintf("%d layers", base.Layers))
	case fs.layers > 0:
		facts = append(facts, fmt.Sprintf("%d layers", fs.layers))
	}
	facts = append(facts, size+" (the daemon's size)")
	add("image", strings.Join(facts, " · "), toneNormal)

	switch {
	case base != nil:
		for _, l := range base.Totals(fs.imageSize).Lines() {
			tn := toneNormal
			if l.Warn {
				tn = toneWarn
			}
			add(l.Label, l.Text, tn)
		}
	case fs.run != nil:
		p, _, _ := fs.run.snapshot()
		add("reading", fmt.Sprintf("the image's archive: %s read · %d layers · %s entries — c cancels", humanBytes(p.BytesRead), p.Layers, files.Count(p.Entries)), toneAccent)
	case fs.failed[fs.imageID] != "":
		add("not read", capitalise(fs.failed[fs.imageID])+" Enter reads it again.", toneWarn)
	case fs.kind == KindContainers:
		add("image", "The image's files have not been read: Enter reads its archive ("+size+" from the daemon) as one job. Until then the rows are the container's changes alone, without sizes.", toneWarn)
	default:
		add("not read", "Nothing has been read. Enter reads the image's archive ("+size+" from the daemon) as one job and builds the index of its paths; c cancels it. No file's contents are read.", toneWarn)
	}
	out = append(out, "")
	return out
}

func (m *Model) filesOverlayLines(add func(label, text string, tn tone), ix *files.Index, image bool) {
	a := ix.Applied
	if a.Total() == 0 {
		if image {
			add("overlay", "This container has written nothing since its image.", toneGood)
		} else {
			add("overlay", "The daemon lists no changes.", toneNormal)
		}
		return
	}
	parts := []string{}
	for _, x := range []struct {
		n int
		w string
	}{{a.Added, "added"}, {a.Changed, "changed"}, {a.Deleted, "deleted"}} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", files.Count(x.n), x.w))
		}
	}
	add("overlay", strings.Join(parts, " · ")+"; sizes and layers are the image's", toneNormal)
	if a.Beneath > 0 {
		add("not read", fmt.Sprintf("%s beneath a directory the container added: not drawn, its contents are not read", files.Plural(a.Beneath, "change", "changes")), toneDim)
	}
	why := ""
	if image && ix.Incomplete() {
		why = " — the index is incomplete, which may be why"
	}
	if image && a.Outside > 0 {
		add("outside", fmt.Sprintf("%s changed paths the image's index does not hold are drawn, marked%s", files.Count(a.Outside), why), toneWarn)
	}
	if a.DeletedMissing > 0 {
		w := "deleted paths the image's index does not hold are not drawn"
		if !image {
			w = "deleted paths are not drawn until the image's files are read"
		}
		add("deleted", fmt.Sprintf("%s %s%s", files.Count(a.DeletedMissing), w, why), toneWarn)
	}
}

// filesDetail is the block under the list: everything known of one path.
func (m *Model) filesDetail(r files.Row, ix *files.Index, w int) []string {
	st := m.st
	n := r.Node
	var out []string
	kv := func(k, v string, tn tone) {
		wrap := wrapWords // prose breaks between words; a path, anywhere
		if k == "path" || k == "target" || k == "links to" {
			wrap = wrapLine
		}
		for i, part := range wrap(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			out = append(out, label+st.tone(tn, part))
		}
	}
	kv("path", files.Escape(n.Path), toneNormal)
	what := n.Type.Words()
	if n.Implicit && n.IsDir() && n.Path != "/" {
		what = "directory, implied by the paths beneath it: no layer has an entry of its own for it, so it has no mode, owner or time"
		if n.FromChanges {
			what = "directory, implied by the changes beneath it"
		}
	} else if !n.FromChanges && n.Type != files.TypeUnknown {
		what += fmt.Sprintf(" · mode %04o (%s) · uid %d gid %d", n.Mode&0o7777, modeString(n), n.UID, n.GID)
		if !n.ModTime.IsZero() {
			what += " · modified " + n.ModTime.UTC().Format("2006-01-02 15:04:05") + " UTC"
		}
	}
	kv("type", what, toneNormal)
	if n.Type == files.TypeSymlink {
		kv("target", files.Escape(n.Link)+" — as recorded; never followed", toneNormal)
	}
	if n.Type == files.TypeHardLink {
		kv("links to", files.Escape(n.Link)+" — the same file; its bytes are counted there", toneNormal)
	}
	switch {
	case n.FromChanges && n.Change == files.Added:
		kv("size", "not read: the container added this path after its image, and the desk does not read a container's archive", toneDim)
	case n.FromChanges && n.Implicit:
		kv("size", "not known: a directory the container's change list implies", toneDim)
	case n.FromChanges:
		kv("size", "not known: the image's index does not hold this path", toneDim)
	case n.IsDir():
		kv("size", fmt.Sprintf("%s, computed from the %s it holds", humanBytes(n.Size), files.Plural(n.Files, "file", "files")), toneNormal)
	case n.Change == files.Changed:
		kv("size", humanBytes(n.Size)+", the image's: the container changed this file, and its own size is not read", toneWarn)
	default:
		kv("size", humanBytes(n.Size)+", as the archive records it", toneNormal)
	}
	switch {
	case n.FromChanges:
		kv("layer", "none: not in the image's tree", toneDim)
	case n.IsDir():
		kv("layer", "none shown: several layers can write one directory", toneDim)
	case ix != nil && n.Layer > 0:
		kv("layer", fmt.Sprintf("%d of %d · %s", n.Layer, ix.Layers, ix.Instruction(n.Layer)), toneNormal)
		for _, u := range ix.Unsure(n.Layer) {
			kv("unsure", fmt.Sprintf("layer %d was not read (%s): it may have replaced or removed this path", u.Layer, u.Reason), toneWarn)
		}
	}
	if m.fs.kind == KindContainers && ix != nil && ix.Applied != nil {
		switch {
		case n.Change == files.Added && n.Beneath > 0:
			kv("change", fmt.Sprintf("added by the container: a directory, with %s changes listed beneath it; its contents are not read", files.Count(n.Beneath)), toneGood)
		case n.Change == files.Added:
			kv("change", "added by the container since its image", toneGood)
		case n.Change == files.Changed:
			kv("change", "changed by the container since its image", toneWarn)
		case n.Change == files.Deleted:
			kv("change", "deleted by the container: still in the image, gone from the container", toneDim)
		case r.Gone:
			kv("change", "beneath a directory the container deleted", toneDim)
		case n.Beneath > 0:
			kv("change", fmt.Sprintf("%s changes beneath it", files.Count(n.Beneath)), toneNormal)
		default:
			kv("change", "none: as in the image", toneDim)
		}
	}
	return out
}

func modeString(n *files.Node) string {
	b := []byte("-rwxrwxrwx")
	switch n.Type {
	case files.TypeDir:
		b[0] = 'd'
	case files.TypeSymlink:
		b[0] = 'l'
	case files.TypeChar:
		b[0] = 'c'
	case files.TypeBlock:
		b[0] = 'b'
	case files.TypeFIFO:
		b[0] = 'p'
	}
	for i := 0; i < 9; i++ {
		if n.Mode&(1<<uint(8-i)) == 0 {
			b[i+1] = '-'
		}
	}
	if n.Mode&0o4000 != 0 {
		b[3] = map[bool]byte{true: 's', false: 'S'}[b[3] == 'x']
	}
	if n.Mode&0o2000 != 0 {
		b[6] = map[bool]byte{true: 's', false: 'S'}[b[6] == 'x']
	}
	if n.Mode&0o1000 != 0 {
		b[9] = map[bool]byte{true: 't', false: 'T'}[b[9] == 'x']
	}
	return string(b)
}

// ---- export ------------------------------------------------------------------------

// exportFiles is w: the listing as the view holds it, through the evidence
// dialog. With no index there is nothing to write, and it says so.
func (m *Model) exportFiles() {
	fs := &m.fs
	base := m.filesBase()
	if base == nil {
		m.setStatus(toneDim, "Nothing to export: the image's archive has not been read, so there is no index to write. Enter reads it.")
		return
	}
	ix := m.filesTree()
	plat := ""
	if e := m.filesEntryFor(fs.imageID); e != nil {
		plat = e.plat
	}
	in := collect.FilesInput{
		Version: m.opts.Version, Index: ix, ImageID: fs.imageID, Ref: fs.imageRef, Platform: plat, DaemonSize: fs.imageSize,
	}
	words, slug := fs.imageRef+"  "+engine.ShortID(fs.imageID), fs.imageRef
	if fs.kind == KindContainers && ix.Applied != nil {
		in.ContainerID, in.ContainerName = fs.containerID, fs.containerName
		words = files.Escape(fs.containerName) + " over " + words
		slug = fs.containerName
	}
	f := collect.Files(in)
	m.openExport(report.Document{Files: &f}, "files", words, slug, report.Text)
}
