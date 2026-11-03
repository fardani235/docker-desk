package ui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/space"
)

// The Space view: where this engine's bytes are, which of them images share,
// and what removing something frees (docs/DECISIONS.md, U1–U13).
//
// Totals are the daemon's own (GET /system/df), shown as it states them.
// Shared bytes are attributed from the daemon's per-layer sizes, matched to
// the layers' chain IDs (package space). Writable layers are measured only
// when asked. Every figure says where it came from and how old it is; one
// taken before the last refresh that changed anything is labelled stale.
// The view sends nothing that changes anything: `d` and `X` open the desk's
// own removal and prune confirmations.

const (
	srcDaemon   = "daemon"
	srcWritable = "computed writable layer"
	srcLayers   = "computed from the daemon's layer sizes"
	srcArchive  = "archive"
)

// layerInfo is what the daemon said about one image's layers. An image ID
// names its content, so this never needs to be read again.
type layerInfo struct {
	chains []string
	sizes  []int64 // nil when the history did not map onto the layers
	err    string
}

type spaceState struct {
	open bool
	// What the desk looked like when the view opened; put back on close.
	prevFocus Pane
	prevKind  Kind
	prevKey   string

	du     *engine.DiskUsage
	duAt   time.Time
	duErr  string
	layers map[string]layerInfo
	ix     *space.Index
	run    *spaceRun

	sizes    map[string]engine.ContainerSize
	sizesAt  time.Time
	sizesErr string
	sizeRun  *sizeRun

	cursorKey string
	off       int
	listH     int
	drawnSec  int64
}

// spaceRun hands the disk-usage job's results to the desk as they arrive:
// the account first, then each image's layers.
type spaceRun struct {
	jobID int
	mu    sync.Mutex
	ver   int
	taken int
	du    *engine.DiskUsage
	at    time.Time
	layer map[string]layerInfo
}

type sizeRun struct {
	jobID int
	mu    sync.Mutex
	sizes []engine.ContainerSize
	at    time.Time
}

// spRow is an object row of the view: Enter goes to it, `d` removes it.
type spRow struct {
	key         string
	kind        Kind
	name        string
	imageID     string
	containerID string
	state       string
	volume      string
}

type spLine struct {
	text string
	row  *spRow
}

// ---- opening and closing ------------------------------------------------------

func (m *Model) openSpace() tea.Cmd {
	m.sp.open = true
	m.sp.prevFocus, m.sp.prevKind = m.focus, m.kind
	m.sp.prevKey = m.sel[m.kind].key
	m.sp.off = 0
	if m.sp.run == nil && (m.sp.du == nil || m.stale(m.sp.duAt)) {
		m.startDiskUsage()
	}
	return nil
}

// closeSpace puts the desk back as it was when the view opened.
func (m *Model) closeSpace() {
	m.sp.open = false
	m.focus = m.sp.prevFocus
	m.setKind(m.sp.prevKind)
	for i, r := range m.rows[m.kind] {
		if r.key == m.sp.prevKey {
			m.selectIndex(i)
			break
		}
	}
}

// stale reports whether a figure taken at t predates the last refresh that
// found the engine different from what it was.
func (m *Model) stale(t time.Time) bool { return t.Before(m.changedAt) }

// ---- jobs ------------------------------------------------------------------------

// startDiskUsage asks the daemon for its account, then for the layers of
// every image not seen before, as one cancellable job.
func (m *Model) startDiskUsage() {
	known := make(map[string]bool, len(m.sp.layers))
	for id, l := range m.sp.layers {
		known[id] = l.err == ""
	}
	run := &spaceRun{layer: map[string]layerInfo{}}
	eng := m.eng
	run.jobID = m.jobs.Start("measure disk usage", "GET /system/df", func(ctx context.Context, r *jobs.Run) error {
		r.Status("the daemon is adding up what it holds (it walks container and volume filesystems)")
		du, err := eng.DiskUsage(ctx)
		if err != nil {
			return err
		}
		at := time.Now()
		run.mu.Lock()
		run.du, run.at = &du, at
		run.ver++
		run.mu.Unlock()
		r.Logf("images %d · containers %d · volumes %d · build cache %d", du.Images.TotalCount, du.Containers.TotalCount, du.Volumes.TotalCount, du.BuildCache.TotalCount)
		var todo []string
		for _, it := range du.ImageItems {
			if !known[it.ID] {
				todo = append(todo, it.ID)
			}
		}
		for i, id := range todo {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.Steps(i, len(todo))
			r.Status(fmt.Sprintf("layers of image %d of %d (inspect, history)", i+1, len(todo)))
			l, err := eng.ImageLayers(ctx, id)
			info := layerInfo{}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				info.err = plainError(err)
			} else {
				info.chains = space.ChainIDs(l.DiffIDs)
				if sizes, ok := space.LayerSizes(len(l.DiffIDs), l.History); ok {
					info.sizes = sizes
				}
			}
			run.mu.Lock()
			run.layer[id] = info
			run.ver++
			run.mu.Unlock()
		}
		r.Steps(len(todo), len(todo))
		r.Status(fmt.Sprintf("account read · layers of %d new image(s)", len(todo)))
		return nil
	})
	m.sp.run = run
}

// startWritable measures every container's writable layer. Only `m` starts
// it: the daemon walks each container's filesystem to answer.
func (m *Model) startWritable() {
	run := &sizeRun{}
	eng := m.eng
	run.jobID = m.jobs.Start("measure writable layers", "GET /containers/json?size=1", func(ctx context.Context, r *jobs.Run) error {
		r.Status("the daemon is walking every container's filesystem")
		cs, err := eng.ContainerSizes(ctx, nil)
		if err != nil {
			return err
		}
		var total int64
		for _, c := range cs {
			total += c.SizeRw
		}
		r.Logf("%d containers · writable layers %s", len(cs), cliSize(total))
		run.mu.Lock()
		run.sizes, run.at = cs, time.Now()
		run.mu.Unlock()
		return nil
	})
	m.sp.sizeRun = run
}

// collectSpace takes what the jobs have produced since the last frame.
func (m *Model) collectSpace() {
	if run := m.sp.run; run != nil {
		run.mu.Lock()
		changed := run.ver != run.taken
		run.taken = run.ver
		du, at := run.du, run.at
		layers := make(map[string]layerInfo, len(run.layer))
		for k, v := range run.layer {
			layers[k] = v
		}
		run.mu.Unlock()
		if changed {
			if du != nil && !at.Equal(m.sp.duAt) {
				m.takeAccount(*du, at)
			}
			if m.sp.layers == nil {
				m.sp.layers = map[string]layerInfo{}
			}
			for k, v := range layers {
				m.sp.layers[k] = v
			}
			m.rebuildIndex()
			m.dirty = true
		}
		if j, ok := m.jobs.Get(run.jobID); !ok || j.State != jobs.Running {
			m.sp.run = nil
			m.dirty = true
			if du == nil {
				switch {
				case ok && j.State == jobs.Cancelled:
					m.sp.duErr = "Cancelled before the daemon answered."
				case ok:
					m.sp.duErr = j.Err
				}
			}
		}
	}
	if run := m.sp.sizeRun; run != nil {
		j, ok := m.jobs.Get(run.jobID)
		if ok && j.State == jobs.Running {
			return
		}
		m.sp.sizeRun = nil
		m.dirty = true
		run.mu.Lock()
		sizes, at := run.sizes, run.at
		run.mu.Unlock()
		switch {
		case sizes != nil:
			m.sp.sizes = map[string]engine.ContainerSize{}
			for _, c := range sizes {
				m.sp.sizes[c.ID] = c
			}
			m.sp.sizesAt, m.sp.sizesErr = at, ""
		case ok && j.State == jobs.Cancelled:
			m.sp.sizesErr = "Cancelled: nothing was measured."
		case ok:
			m.sp.sizesErr = j.Err
		}
	}
}

// takeAccount installs a new account. One that differs from the last makes
// everything taken before it stale, exactly as a changed list does.
func (m *Model) takeAccount(du engine.DiskUsage, at time.Time) {
	if old := m.sp.du; old != nil {
		if old.Images != du.Images || old.Containers != du.Containers || old.Volumes != du.Volumes || old.BuildCache != du.BuildCache {
			if at.After(m.changedAt) {
				m.changedAt = at
			}
		}
	}
	m.sp.du, m.sp.duAt, m.sp.duErr = &du, at, ""
	present := map[string]bool{}
	for _, it := range du.ImageItems {
		present[it.ID] = true
	}
	for id := range m.sp.layers {
		if !present[id] {
			delete(m.sp.layers, id) // bounded by what the daemon holds
		}
	}
}

func (m *Model) rebuildIndex() {
	if m.sp.du == nil {
		m.sp.ix = nil
		return
	}
	imgs := make([]space.Image, 0, len(m.sp.du.ImageItems))
	for _, it := range m.sp.du.ImageItems {
		si := space.Image{ID: it.ID, Size: it.Size, SharedSize: it.SharedSize}
		if l, ok := m.sp.layers[it.ID]; ok && l.err == "" {
			si.Chains, si.Sizes = l.chains, l.sizes
		}
		imgs = append(imgs, si)
	}
	m.sp.ix = space.Build(imgs)
}

func (m *Model) cancelSpaceJobs() {
	n := 0
	if m.sp.run != nil {
		m.jobs.Cancel(m.sp.run.jobID)
		n++
	}
	if m.sp.sizeRun != nil {
		m.jobs.Cancel(m.sp.sizeRun.jobID)
		n++
	}
	if n == 0 {
		m.setStatus(toneDim, "Nothing is being measured.")
		return
	}
	m.setStatus(toneWarn, "Cancelling the measurement. Nothing on the daemon changes either way.")
}

// ---- keys -----------------------------------------------------------------------

func (m *Model) onSpaceKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	rows := m.spaceObjectRows()
	cur := m.spaceCursor(rows)
	page := max(m.sp.listH-1, 1)
	move := func(i int) {
		if len(rows) == 0 {
			return
		}
		i = clamp(i, 0, len(rows)-1)
		m.sp.cursorKey = rows[i].key
	}
	switch key {
	case "S", "esc":
		m.closeSpace()
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "down", "j":
		move(cur + 1)
	case "up", "k":
		move(cur - 1)
	case "pgdown", "ctrl+f", " ":
		move(cur + page)
	case "pgup", "ctrl+b":
		move(cur - page)
	case "g", "home":
		move(0)
		m.sp.off = 0
	case "G", "end":
		move(len(rows) - 1)
	case "enter":
		if cur >= 0 {
			m.spaceJump(rows[cur])
		}
	case "r":
		// The lists too: a re-read that finds them changed is what makes
		// older figures stale.
		m.pending |= 0xf
		for i := range m.lastStart {
			m.lastStart[i] = time.Time{}
		}
		if m.sp.run != nil {
			m.setStatus(toneDim, "The account is already being read.")
			return nil
		}
		m.startDiskUsage()
	case "m":
		if m.sp.sizeRun != nil {
			m.setStatus(toneDim, "The writable layers are already being measured.")
			return nil
		}
		m.startWritable()
	case "c":
		m.cancelSpaceJobs()
	case "T":
		m.setTheme(nextTheme(m.st.theme))
	case "d", "X":
		if m.readOnly {
			return nil // no reclaim in read-only mode: the keys are not offered
		}
		if cur < 0 {
			return nil
		}
		return m.spaceReclaim(rows[cur], key)
	}
	return nil
}

// spaceReclaim opens the desk's own removal or prune confirmation for the
// highlighted object. The view itself never sends anything.
func (m *Model) spaceReclaim(r spRow, key string) tea.Cmd {
	var a action
	found := false
	for _, x := range kindActions[r.kind] {
		if x.key == key {
			a, found = x, true
		}
	}
	if !found {
		switch {
		case key == "X":
			m.setStatus(toneWarn, "The desk prunes images and volumes only: d removes the highlighted "+strings.ToLower(strings.TrimSuffix(kindNames[r.kind], "s"))+". Nothing was sent.")
		default:
			m.setStatus(toneWarn, "Nothing removes that from the desk. Nothing was sent.")
		}
		return nil
	}
	if a.needsRow {
		return m.invokeOn(a, m.deskRowFor(r), true, nil)
	}
	return m.invokeOn(a, row{}, false, nil)
}

// deskRowFor is the List row the desk's own actions take for the object.
func (m *Model) deskRowFor(r spRow) row {
	switch r.kind {
	case KindImages:
		out := row{key: r.imageID, name: r.name, imageID: r.imageID}
		for _, img := range m.snap.Images {
			if img.ID == r.imageID {
				if refs := img.Refs(); len(refs) > 0 {
					out.imageRef, out.name = refs[0], refs[0]
					out.key = img.ID + "|" + refs[0]
				} else {
					out.name = "<untagged> " + engine.ShortID(img.ID)
					out.key = img.ID + "|"
				}
			}
		}
		return out
	case KindContainers:
		return row{key: r.containerID, name: r.name, containerID: r.containerID, state: r.state}
	case KindVolumes:
		return row{key: r.volume, name: r.volume, volume: r.volume}
	}
	return row{}
}

// spaceJump closes the view on the object: its kind, its row highlighted,
// the List focused.
func (m *Model) spaceJump(r spRow) {
	m.sp.open = false
	m.focus = PaneList
	m.setKind(r.kind)
	find := func() int {
		for i, x := range m.rows[r.kind] {
			switch {
			case r.imageID != "" && x.imageID == r.imageID,
				r.containerID != "" && x.containerID == r.containerID,
				r.volume != "" && x.volume == r.volume:
				return i
			}
		}
		return -1
	}
	i := find()
	if i < 0 && m.filter[r.kind] != "" {
		m.filter[r.kind] = ""
		m.invalidateRows(r.kind)
		m.ensureRows(r.kind)
		if i = find(); i >= 0 {
			m.setStatus(toneDim, "Cleared the "+kindNames[r.kind]+" filter: it hid "+r.name+".")
		}
	}
	if i < 0 {
		m.setStatus(toneWarn, r.name+" is no longer in the "+kindNames[r.kind]+" list.")
		return
	}
	m.selectIndex(i)
}

// ---- rows -------------------------------------------------------------------------

func imageLabel(tags []string, id string) string {
	for _, t := range (engine.Image{RepoTags: tags}).Refs() {
		return t
	}
	return "<none> " + engine.ShortID(id)
}

// spaceImages is the account's images, most unique bytes first.
func (m *Model) spaceImages() []engine.ImageUsage {
	if m.sp.du == nil {
		return nil
	}
	out := append([]engine.ImageUsage(nil), m.sp.du.ImageItems...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Unique() != out[j].Unique() {
			return out[i].Unique() > out[j].Unique()
		}
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return imageLabel(out[i].RepoTags, out[i].ID) < imageLabel(out[j].RepoTags, out[j].ID)
	})
	return out
}

func (m *Model) spaceContainers() []engine.Container {
	cs := append([]engine.Container(nil), m.snap.Containers...)
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := m.sp.sizes[cs[i].ID].SizeRw, m.sp.sizes[cs[j].ID].SizeRw
		if a != b {
			return a > b
		}
		return cs[i].Name < cs[j].Name
	})
	return cs
}

func (m *Model) spaceVolumes() []engine.VolumeUsage {
	if m.sp.du == nil {
		return nil
	}
	vs := append([]engine.VolumeUsage(nil), m.sp.du.VolumeItems...)
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Size != vs[j].Size {
			return vs[i].Size > vs[j].Size
		}
		return vs[i].Name < vs[j].Name
	})
	return vs
}

// spaceObjectRows lists the rows the cursor moves over, in display order.
// There are none until the account is in: the cursor then starts on the
// first image, not on whatever was listed before it arrived.
func (m *Model) spaceObjectRows() []spRow {
	if m.sp.du == nil {
		return nil
	}
	var out []spRow
	for _, it := range m.spaceImages() {
		out = append(out, spRow{key: "i:" + it.ID, kind: KindImages, name: imageLabel(it.RepoTags, it.ID), imageID: it.ID})
	}
	for _, c := range m.spaceContainers() {
		out = append(out, spRow{key: "c:" + c.ID, kind: KindContainers, name: c.Name, containerID: c.ID, state: c.State})
	}
	for _, v := range m.spaceVolumes() {
		out = append(out, spRow{key: "v:" + v.Name, kind: KindVolumes, name: v.Name, volume: v.Name})
	}
	return out
}

// spaceCursor returns the highlighted row's index: the one last chosen if
// it is still there, else the first.
func (m *Model) spaceCursor(rows []spRow) int {
	if len(rows) == 0 {
		return -1
	}
	for i, r := range rows {
		if r.key == m.sp.cursorKey {
			return i
		}
	}
	m.sp.cursorKey = rows[0].key
	return 0
}

// ---- drawing ------------------------------------------------------------------------

// cliSize formats bytes exactly as the docker CLI does (go-units HumanSize:
// four significant digits, powers of 1000), so that the view's figures can
// be compared with `docker system df` character for character.
func cliSize(n int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	v := float64(n)
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.4g%s", v, units[i])
}

// cliReclaimable is the RECLAIMABLE column of `docker system df`.
func cliReclaimable(u engine.Usage, percent bool) string {
	if percent && u.TotalSize > 0 {
		return fmt.Sprintf("%s (%d%%)", cliSize(u.Reclaimable), u.Reclaimable*100/u.TotalSize)
	}
	return cliSize(u.Reclaimable)
}

// provenance is the source and age of a figure.
func (m *Model) provenance(src string, at time.Time, now time.Time) string {
	if at.IsZero() {
		return src
	}
	s := src + " · " + at.Local().Format("15:04:05") + ", " + humanAge(at, now) + " ago"
	if m.stale(at) {
		s += " · STALE"
	}
	return s
}

// prov is the short form, for the detail lines: the clock time is in the
// view's title and its totals.
func (m *Model) prov(src string, at time.Time, now time.Time) string {
	if at.IsZero() {
		return src
	}
	s := src + ", " + humanAge(at, now) + " ago"
	if m.stale(at) {
		s += ", STALE"
	}
	return s
}

func (m *Model) provTone(at time.Time) tone {
	if m.stale(at) {
		return toneWarn
	}
	return toneDim
}

// spaceBox draws the view over the desk's body: w×h cells, a box like the
// help screen's.
func (m *Model) spaceBox(w, h int) []string {
	inner := w - 4
	bodyH := h - 2
	now := time.Now()
	st := m.st

	lines := m.spaceLines(inner, now)
	rows := m.spaceObjectRows()
	cur := m.spaceCursor(rows)

	detail := m.spaceDetail(rows, cur, inner, now)
	listH := max(bodyH-len(detail)-1, 3)
	m.sp.listH = listH

	// Keep the highlighted row in view; at either end, show the end.
	curLine := -1
	if cur >= 0 {
		for i, l := range lines {
			if l.row != nil && l.row.key == rows[cur].key {
				curLine = i
			}
		}
	}
	maxOff := max(len(lines)-listH, 0)
	if curLine >= 0 {
		m.sp.off = clamp(m.sp.off, curLine-listH+1, curLine)
		switch {
		case cur == 0 && curLine < listH:
			m.sp.off = 0 // the totals above the first row stay in view
		case cur == len(rows)-1:
			m.sp.off = min(maxOff, curLine) // and what follows the last
		}
	}
	m.sp.off = clamp(m.sp.off, 0, maxOff)

	var body []string
	for i := m.sp.off; i < len(lines) && len(body) < listH; i++ {
		l := lines[i]
		text := l.text
		if l.row != nil && curLine == i {
			text = st.selected.Render("▸ " + fit(ansi.Strip(text), inner-2))
		} else {
			text = "  " + text
		}
		body = append(body, text)
	}
	for len(body) < listH {
		body = append(body, "")
	}
	pos := fmt.Sprintf(" lines %d–%d of %d ", m.sp.off+1, min(m.sp.off+listH, len(lines)), len(lines))
	body = append(body, st.tone(toneDim, strings.Repeat("─", max(inner-len([]rune(pos))-1, 0))+pos+"─"))
	body = append(body, detail...)

	padded := make([]string, 0, len(body))
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner)+" ")
	}
	right := ""
	switch {
	case m.sp.run != nil:
		right = spinnerFrames[m.spinner] + " reading the account"
	case m.sp.sizeRun != nil:
		right = spinnerFrames[m.spinner] + " measuring writable layers"
	case m.sp.du != nil && m.stale(m.sp.duAt):
		right = "STALE — r re-reads"
	case m.sp.du != nil:
		right = "as of " + m.sp.duAt.Local().Format("15:04:05")
	}
	title := "Space — where the bytes went, and what removing something frees"
	if w < wideMinWidth {
		title = "Space — where the bytes went"
	}
	return st.box(title, right, padded, w, h, true)
}

// spaceLines is the scrolling part of the view. Object rows carry their row.
func (m *Model) spaceLines(w int, now time.Time) []spLine {
	st := m.st
	var out []spLine
	text := func(t tone, s string) {
		for _, part := range wrapWords(s, w-2) {
			out = append(out, spLine{text: st.tone(t, part)})
		}
	}
	blank := func() { out = append(out, spLine{}) }
	head := func(title, note string) {
		blank()
		s := st.tone(toneAccent, title)
		if note != "" {
			s += "  " + st.tone(toneDim, note)
		}
		out = append(out, spLine{text: fit(s, w-2)})
	}

	du := m.sp.du
	switch {
	case du == nil && m.sp.run != nil:
		text(toneAccent, spinnerFrames[m.spinner]+" Asking the daemon for its account (GET /system/df)…")
		text(toneDim, "The daemon walks container and volume filesystems to answer; on a busy engine that takes a while. c cancels.")
		return out
	case du == nil && m.sp.duErr != "":
		text(toneBad, "The daemon's account could not be read: "+m.sp.duErr)
		text(toneDim, "r asks again.")
		return out
	case du == nil:
		text(toneDim, "No account yet. r asks the daemon.")
		return out
	}

	// -- totals: the daemon's, verbatim ----------------------------------
	cols := fmt.Sprintf("%-14s %6s %7s %9s  %s", "TYPE", "TOTAL", "ACTIVE", "SIZE", "RECLAIMABLE")
	out = append(out, spLine{text: st.tone(toneDim, cols)})
	tot := func(name string, u engine.Usage, pct bool) {
		out = append(out, spLine{text: fmt.Sprintf("%-14s %6d %7d %9s  %s", name, u.TotalCount, u.ActiveCount, cliSize(u.TotalSize), cliReclaimable(u, pct))})
	}
	tot("Images", du.Images, true)
	tot("Containers", du.Containers, true)
	tot("Local Volumes", du.Volumes, true)
	tot("Build Cache", du.BuildCache, false)
	src := m.provenance(srcDaemon+" (GET /system/df)", m.sp.duAt, now)
	if du.Derived {
		src = m.provenance("summed by the client library from the daemon's lists (API < 1.52 states no totals)", m.sp.duAt, now)
	}
	text(m.provTone(m.sp.duAt), src)
	text(toneDim, "Reclaimable is the daemon's estimate. Nothing is freed until something is removed; with the containerd image store, the bytes go when its garbage collector runs.")

	// -- images -----------------------------------------------------------
	nameW := max(w-2-47, 12)
	head("IMAGES", "size, unique and shared as the daemon reports them · "+humanAge(m.sp.duAt, now)+" ago")
	out = append(out, spLine{text: st.tone(toneDim, fit("", nameW)+fmt.Sprintf(" %8s %8s %8s %7s  %s", "SIZE", "UNIQUE", "SHARED", "USED", "A"))})
	for _, it := range m.spaceImages() {
		name := imageLabel(it.RepoTags, it.ID)
		used := "-"
		if it.Containers > 0 {
			used = fmt.Sprintf("%d ctr", it.Containers)
		}
		unique, shared := "?", "?"
		if it.Unique() >= 0 {
			unique, shared = cliSize(it.Unique()), cliSize(it.SharedSize)
		}
		mark := " "
		if m.analysed(it.ID) != nil {
			mark = "A"
		}
		t := toneNormal
		if it.Containers <= 0 {
			t = toneDim
		}
		line := st.tone(t, fit(name, nameW)+fmt.Sprintf(" %8s %8s %8s %7s  %s", cliSize(it.Size), unique, shared, used, mark))
		r := spRow{key: "i:" + it.ID}
		out = append(out, spLine{text: line, row: &r})
	}
	text(toneDim, "A: analysed in Images › Findings — its layers are attributed below when highlighted.")

	// -- shared -------------------------------------------------------------
	head("SHARED LAYERS", "each byte once, against the images that hold it · "+srcLayers)
	out = append(out, m.sharedLines(w-2)...)

	// -- containers -----------------------------------------------------------
	note := "writable layers not measured — m measures them (GET /containers/json?size=1)"
	if m.sp.sizeRun != nil {
		note = spinnerFrames[m.spinner] + " measuring writable layers… c cancels"
	} else if !m.sp.sizesAt.IsZero() {
		note = m.provenance(srcWritable, m.sp.sizesAt, now)
	} else if m.sp.sizesErr != "" {
		note = "writable layers: " + m.sp.sizesErr + " m measures them"
	}
	head("CONTAINERS", "")
	text(m.provTone(m.sp.sizesAt), note)
	cnameW := max(w-2-32, 12)
	out = append(out, spLine{text: st.tone(toneDim, fit("", cnameW)+fmt.Sprintf(" %-12s %8s %8s", "STATE", "WRITABLE", "VIRTUAL"))})
	for _, c := range m.spaceContainers() {
		rw, virt := "-", "-"
		if s, ok := m.sp.sizes[c.ID]; ok {
			rw, virt = cliSize(s.SizeRw), cliSize(s.SizeRootFs)
		}
		line := st.tone(stateTone(c.State), fit(c.Name, cnameW)+fmt.Sprintf(" %-12s %8s %8s", stateGlyph(c.State)+" "+c.State, rw, virt))
		r := spRow{key: "c:" + c.ID}
		out = append(out, spLine{text: line, row: &r})
	}

	// -- volumes ---------------------------------------------------------------
	head("LOCAL VOLUMES", srcDaemon+" · "+humanAge(m.sp.duAt, now)+" ago")
	vnameW := max(w-2-22, 12)
	out = append(out, spLine{text: st.tone(toneDim, fit("", vnameW)+fmt.Sprintf(" %8s %11s", "SIZE", "LINKS"))})
	for _, v := range m.spaceVolumes() {
		size := "?"
		if v.Size >= 0 {
			size = cliSize(v.Size)
		}
		links := "?"
		if v.RefCount >= 0 {
			links = fmt.Sprintf("%d", v.RefCount)
		}
		t := toneNormal
		if v.RefCount == 0 {
			t = toneDim
		}
		name := v.Name
		if len(name) == 64 {
			name = name[:12] + "… (anonymous)"
		}
		r := spRow{key: "v:" + v.Name}
		out = append(out, spLine{text: st.tone(t, fit(name, vnameW)+fmt.Sprintf(" %8s %11s", size, links)), row: &r})
	}

	// -- build cache ------------------------------------------------------------
	head("BUILD CACHE", srcDaemon+" · the desk has no builder, so nothing here is removed from it")
	if len(du.BuildItems) == 0 {
		text(toneDim, "No build-cache records.")
	}
	items := append([]engine.BuildCacheUsage(nil), du.BuildItems...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	for _, b := range items {
		flags := ""
		if b.InUse {
			flags += " in use"
		}
		if b.Shared {
			flags += " shared"
		}
		text(toneDim, fmt.Sprintf("%8s  %-15s %s%s", cliSize(b.Size), b.Type, b.Description, flags))
	}
	return out
}

// sharedLines lists every set of images that share bytes, once.
func (m *Model) sharedLines(w int) []spLine {
	st := m.st
	var out []spLine
	ix := m.sp.ix
	text := func(t tone, s string) {
		for _, part := range wrapWords(s, w) {
			out = append(out, spLine{text: st.tone(t, part)})
		}
	}
	if ix == nil {
		return out
	}
	names := map[string]string{}
	for _, it := range m.sp.du.ImageItems {
		names[it.ID] = imageLabel(it.RepoTags, it.ID)
	}
	if un := ix.Unindexed(); len(un) > 0 && m.sp.run != nil {
		text(toneDim, fmt.Sprintf("%s reading the layers of %d image(s)…", spinnerFrames[m.spinner], len(un)))
	}
	groups := ix.Groups()
	if len(groups) == 0 && len(ix.Unindexed()) == 0 {
		text(toneDim, "No two images share a layer.")
	}
	for _, g := range groups {
		var hs []string
		for _, h := range g.Holders {
			hs = append(hs, names[h])
		}
		sort.Strings(hs)
		size := cliSize(g.Bytes)
		switch {
		case g.Unknown > 0 && g.Bytes == 0:
			size = "?" // which images share is known; how much is not
		case g.Unknown > 0:
			size = "≥" + size
		}
		lead := fmt.Sprintf("%8s  %d layer(s)  ", size, g.Layers)
		for i, part := range wrapWords(strings.Join(hs, " · "), max(w-len([]rune(lead)), 10)) {
			if i == 0 {
				out = append(out, spLine{text: lead + part})
			} else {
				out = append(out, spLine{text: strings.Repeat(" ", len([]rune(lead))) + part})
			}
		}
	}
	if dis := ix.Disagreeing(); len(dis) > 0 {
		indexed := len(m.sp.du.ImageItems) - len(ix.Unindexed())
		if len(dis) == indexed {
			text(toneWarn, "Sizes not attributed: no image's layer sizes add up to the daemon's size for it. (The containerd image store counts each layer's compressed content as well as its unpacked files; the history gives only the second.) Which images share is shown; what a set frees is given as at least its members' unique sizes.")
		} else {
			var ns []string
			for _, id := range dis {
				ns = append(ns, names[id])
			}
			sort.Strings(ns)
			if len(ns) > 5 {
				ns = append(ns[:5], fmt.Sprintf("and %d more", len(ns)-5))
			}
			text(toneWarn, "Not attributed: the layer sizes of "+strings.Join(ns, ", ")+" do not map onto their layers, or do not add up to the daemon's size for them. Their figures are the daemon's alone.")
		}
	}
	if un := ix.Unindexed(); len(un) > 0 && m.sp.run == nil {
		text(toneWarn, fmt.Sprintf("The layers of %d image(s) could not be read: what they share is not known. r tries again.", len(un)))
	}
	return out
}

// analysed returns a cached Findings report for the image, if any. The
// cache is looked at, never filled: no archive is read for this view.
func (m *Model) analysed(id string) *forensics.Report {
	for key, rep := range m.fx.cache {
		if strings.HasPrefix(key, id+"|") {
			return rep
		}
	}
	return nil
}

func (m *Model) analysedAt(id string) time.Time {
	for key, at := range m.fx.storedAt {
		if strings.HasPrefix(key, id+"|") {
			return at
		}
	}
	return time.Time{}
}

// spaceDetail is the fixed part at the bottom: what the highlighted object
// holds, and what removing it frees.
func (m *Model) spaceDetail(rows []spRow, cur, w int, now time.Time) []string {
	st := m.st
	var out []string
	kv := func(k, v string, t tone) {
		label := st.tone(toneDim, fit(k, 10)) + " "
		if k == "" {
			// A continuation (the names of a set): one line, cut short.
			out = append(out, strings.Repeat(" ", 11)+st.tone(t, fit(v, max(w-11, 10))))
			return
		}
		for i, part := range wrapWords(v, max(w-11, 10)) {
			if i > 0 {
				label = strings.Repeat(" ", 11)
			}
			out = append(out, label+st.tone(t, part))
		}
	}
	if cur < 0 || m.sp.du == nil {
		return []string{st.tone(toneDim, "Nothing highlighted.")}
	}
	r := rows[cur]
	switch r.kind {
	case KindImages:
		m.imageDetail(r, kv, now)
	case KindContainers:
		var c engine.Container
		for _, x := range m.snap.Containers {
			if x.ID == r.containerID {
				c = x
			}
		}
		if s, ok := m.sp.sizes[c.ID]; ok {
			kv("writable", cliSize(s.SizeRw)+" — what removing it frees; its volumes stay · "+m.prov(srcWritable, m.sp.sizesAt, now), m.provTone(m.sp.sizesAt))
		} else {
			kv("writable", "not measured — m measures every container's writable layer", toneDim)
		}
		if c.IsActive() {
			kv("removal", c.Name+" is "+c.State+": it must be stopped before it can be removed", toneWarn)
		}
	case KindVolumes:
		for _, v := range m.sp.du.VolumeItems {
			if v.Name != r.volume {
				continue
			}
			if v.Size >= 0 {
				kv("size", cliSize(v.Size)+" — what removing it frees · "+m.prov(srcDaemon, m.sp.duAt, now), m.provTone(m.sp.duAt))
			} else {
				kv("size", "not measured by the daemon (driver "+v.Driver+")", toneDim)
			}
			if v.RefCount > 0 {
				kv("removal", fmt.Sprintf("referenced by %d container(s): refused until they are removed", v.RefCount), toneWarn)
			}
		}
	}
	return out
}

func (m *Model) imageDetail(r spRow, kv func(k, v string, t tone), now time.Time) {
	var it engine.ImageUsage
	for _, x := range m.sp.du.ImageItems {
		if x.ID == r.imageID {
			it = x
		}
	}
	pt := m.provTone(m.sp.duAt)
	if it.Unique() < 0 {
		kv("alone", "not known: the daemon did not compute the shared size", toneDim)
	} else {
		kv("alone", fmt.Sprintf("frees %s — unique; %s shared stay · %s", cliSize(it.Unique()), cliSize(it.SharedSize), m.prov(srcDaemon, m.sp.duAt, now)), pt)
	}

	ix := m.sp.ix
	if ix != nil {
		sharers := ix.Sharers(it.ID)
		var names []string
		for _, id := range sharers {
			for _, x := range m.sp.du.ImageItems {
				if x.ID == id {
					names = append(names, imageLabel(x.RepoTags, x.ID))
				}
			}
		}
		switch {
		case !ix.Indexed(it.ID) && m.sp.run != nil:
			kv("together", "reading its layers…", toneDim)
		case !ix.Indexed(it.ID):
			kv("together", "not known: its layers could not be read", toneDim)
		case len(sharers) == 0:
			kv("together", "no other image shares its layers", toneDim)
		default:
			f := ix.Frees(append([]string{it.ID}, sharers...))
			lead := fmt.Sprintf("with the %d image(s) sharing its layers", len(sharers))
			switch {
			case f.Exact:
				kv("together", fmt.Sprintf("%s: frees %s · %s", lead, cliSize(f.Bytes), m.prov("computed from layer sizes", m.sp.duAt, now)), pt)
			case f.Known:
				kv("together", fmt.Sprintf("%s: frees at least %s (their unique sizes) — %s", lead, cliSize(f.Bytes), f.Reason), toneWarn)
			default:
				kv("together", lead+": not known — "+f.Reason, toneWarn)
			}
			sort.Strings(names)
			kv("", strings.Join(names, " · "), toneDim)
		}
	}

	if rep := m.analysed(it.ID); rep != nil {
		at := m.analysedAt(it.ID)
		var largest *forensics.Layer
		var parts []string
		for i := range rep.Layers {
			l := &rep.Layers[i]
			parts = append(parts, fmt.Sprintf("L%d %s", l.Index, cliSize(l.Size)))
			if largest == nil || l.Size > largest.Size {
				largest = l
			}
		}
		line := fmt.Sprintf("%s of file content in %d layers: %s", cliSize(rep.TotalSize()), len(rep.Layers), strings.Join(parts, ", "))
		if largest != nil && largest.Instruction != "" {
			pct := 0
			if t := rep.TotalSize(); t > 0 {
				pct = int(math.Round(float64(largest.Size) * 100 / float64(t)))
			}
			line += fmt.Sprintf(". Largest: L%d, %d%%, by %s", largest.Index, pct, largest.Instruction)
		} else if largest != nil {
			line += fmt.Sprintf(". Largest: L%d (the history does not map onto the layers, so no instruction is named)", largest.Index)
		}
		kv("archive", line+" · "+m.prov(srcArchive, at, now), m.provTone(at))
	}

	users := 0
	for _, c := range m.snap.Containers {
		if c.ImageID == it.ID {
			users++
		}
	}
	refs := (engine.Image{RepoTags: it.RepoTags}).Refs()
	switch {
	case users > 0:
		kv("removal", fmt.Sprintf("used by %d container(s): refused until they are removed — the alone figure is what it frees then", users), toneWarn)
	case len(refs) > 1:
		kv("removal", fmt.Sprintf("%d references: removing one only untags it and frees nothing; selecting all of them in Images removes the image", len(refs)), toneDim)
	}
}

func (m *Model) spaceHints() []string {
	pairs := []string{"↑↓", "move", "⏎", "go to", "r", "re-read", "m", "writable"}
	if m.sp.run != nil || m.sp.sizeRun != nil {
		pairs = append(pairs, "c", "cancel")
	}
	if !m.readOnly {
		pairs = append(pairs, "d", "remove…", "X", "prune…")
	}
	return pairs
}

// ---- what a confirmation frees ----------------------------------------------------------

type freeLine struct {
	text string
	tone tone
}

// freesLines says what a removal or prune would free, from the view's last
// account — no request is sent for it — with that account's age, and STALE
// when the lists have changed since.
func (m *Model) freesLines(p plan.Plan) []freeLine {
	if !p.Runnable() || len(p.Steps) == 0 {
		return nil
	}
	op := p.Steps[0].Op
	for _, s := range p.Steps {
		if s.Op != op {
			return nil
		}
	}
	now := time.Now()
	notYet := freeLine{"not measured — S opens Space, which reads the daemon's account (GET /system/df)", toneDim}
	switch op {
	case plan.OpRemoveImage:
		var ids []string
		for _, s := range p.Steps {
			if s.KeepsData || s.Size == 0 {
				continue // an untag: the image stays
			}
			id := s.Pin
			if id == "" {
				id = s.ID
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return []freeLine{{"nothing — the image stays, under its other references", toneNormal}}
		}
		du := m.sp.du
		if du == nil {
			return []freeLine{notYet}
		}
		var unique []engine.ImageUsage
		for _, id := range ids {
			for _, it := range du.ImageItems {
				if it.ID == id {
					unique = append(unique, it)
				}
			}
		}
		if len(unique) < len(ids) {
			return []freeLine{{"not known — the Space account predates these images; r there reads it again", toneWarn}}
		}
		tn := m.provTone(m.sp.duAt)
		if len(ids) == 1 {
			it := unique[0]
			if it.Unique() < 0 {
				return []freeLine{{"not known — the daemon did not compute the shared size", toneWarn}}
			}
			return []freeLine{{fmt.Sprintf("%s — its unique size; the %s it shares with other images stays · %s",
				cliSize(it.Unique()), cliSize(it.SharedSize), m.prov(srcDaemon, m.sp.duAt, now)), tn}}
		}
		if m.sp.ix == nil {
			return []freeLine{notYet}
		}
		f := m.sp.ix.Frees(ids)
		switch {
		case f.Exact:
			return []freeLine{{fmt.Sprintf("%s — their own layers and the layers only they share · %s",
				cliSize(f.Bytes), m.prov("computed from layer sizes", m.sp.duAt, now)), tn}}
		case f.Known:
			return []freeLine{{fmt.Sprintf("at least %s — their unique sizes (%s) · %s", cliSize(f.Bytes), f.Reason, m.prov(srcDaemon, m.sp.duAt, now)), toneWarn}}
		}
		return []freeLine{{"not known — " + f.Reason, toneWarn}}

	case plan.OpRemoveContainer:
		if m.sp.sizesAt.IsZero() {
			return []freeLine{{"not measured — in Space (S), m measures writable layers", toneDim}}
		}
		var total int64
		missing := 0
		for _, s := range p.Steps {
			if c, ok := m.sp.sizes[s.ID]; ok {
				total += c.SizeRw
			} else {
				missing++
			}
		}
		what := "its writable layer; its volumes stay"
		if len(p.Steps) > 1 {
			what = "their writable layers; their volumes stay"
		}
		if missing > 0 {
			return []freeLine{{fmt.Sprintf("at least %s — %s; %d created since the measurement · %s", cliSize(total), what, missing, m.prov(srcWritable, m.sp.sizesAt, now)), toneWarn}}
		}
		return []freeLine{{fmt.Sprintf("%s — %s · %s", cliSize(total), what, m.prov(srcWritable, m.sp.sizesAt, now)), m.provTone(m.sp.sizesAt)}}

	case plan.OpRemoveVolume:
		du := m.sp.du
		if du == nil {
			return []freeLine{notYet}
		}
		var total int64
		missing := 0
		for _, s := range p.Steps {
			found := false
			for _, v := range du.VolumeItems {
				if v.Name == s.ID && v.Size >= 0 {
					total, found = total+v.Size, true
				}
			}
			if !found {
				missing++
			}
		}
		if missing > 0 {
			return []freeLine{{fmt.Sprintf("at least %s — %d volume(s) not measured in the account · %s", cliSize(total), missing, m.prov(srcDaemon, m.sp.duAt, now)), toneWarn}}
		}
		return []freeLine{{fmt.Sprintf("%s — the data in it · %s", cliSize(total), m.prov(srcDaemon, m.sp.duAt, now)), m.provTone(m.sp.duAt)}}
	}
	return nil
}
