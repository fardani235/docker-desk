package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/space"
)

// The desk's layer cache: what the daemon said about each image's layers
// (one inspect and one history call), read once per image per session and
// shared by the Space view and Images › Layers (docs/DECISIONS.md, LY10).
//
// It belongs to the focused daemon: a focus change rebuilds the model and
// the cache with it, so one daemon's layers are never shown as another's.

// histStep is one entry of the daemon's history as the desk keeps it. The
// instruction is masked where it is stored (forensics.Instruction): the
// daemon's created_by, which can carry a build argument's value, is never
// held, so no view can draw it.
type histStep struct {
	instr string // text
	size  int64
}

// layerInfo is what the daemon said about one image's layers. An image ID
// names its content, so this never needs to be read again.
type layerInfo struct {
	diffs  []string // diff IDs, bottom first
	chains []string
	sizes  []int64    // nil when the history did not map onto the layers
	steps  []histStep // the history, oldest first
	cut    bool       // the daemon's history was longer than maxKeptSteps
	err    string
}

// maxKeptSteps bounds the history kept for one image. An image has at most
// a few hundred steps; a daemon that answers with more is not believed
// beyond this, and the tab says the list was cut.
const maxKeptSteps = 2000

func newLayerInfo(l engine.ImageLayers) layerInfo {
	info := layerInfo{diffs: append([]string{}, l.DiffIDs...), chains: space.ChainIDs(l.DiffIDs)}
	if sizes, ok := space.LayerSizes(len(l.DiffIDs), l.History); ok {
		info.sizes = sizes
	}
	hist := l.History
	if len(hist) > maxKeptSteps {
		hist, info.cut = hist[:maxKeptSteps], true
	}
	info.steps = make([]histStep, len(hist))
	for i, h := range hist {
		info.steps[i] = histStep{instr: forensics.Instruction(h.CreatedBy), size: h.Size}
	}
	return info
}

type layerCache struct {
	byID map[string]layerInfo
	// ver counts changes to byID; ix is the content index built from it
	// and from the image list (layersIndex).
	ver   uint64
	ix    *space.Index
	ixFor [2]uint64 // ver and dataVersion ix was built from

	run *layerRun       // the engine-wide read (`s` on Layers)
	own map[string]bool // images whose own read is on its way
}

// layerRun hands the read job's results to the desk as they arrive.
type layerRun struct {
	jobID int
	total int
	ids   map[string]bool
	mu    sync.Mutex
	ver   int
	taken int
	layer map[string]layerInfo
}

// layerKnown reports whether the image's layers are held and readable.
func (m *Model) layerKnown(id string) bool {
	l, ok := m.lc.byID[id]
	return ok && l.err == ""
}

func (m *Model) putLayers(id string, info layerInfo) {
	if m.lc.byID == nil {
		m.lc.byID = map[string]layerInfo{}
	}
	m.lc.byID[id] = info
	m.lc.ver++
}

// pruneLayers drops what no list names any more, so the cache is bounded by
// what the daemon holds: the image list, and the Space account's.
func (m *Model) pruneLayers() {
	if len(m.lc.byID) == 0 || !m.loaded[listImages] {
		return
	}
	present := make(map[string]bool, len(m.snap.Images))
	for _, img := range m.snap.Images {
		present[img.ID] = true
	}
	if m.sp.du != nil {
		for _, it := range m.sp.du.ImageItems {
			present[it.ID] = true
		}
	}
	for id := range m.lc.byID {
		if !present[id] {
			delete(m.lc.byID, id)
			m.lc.ver++
		}
	}
}

// layerProgress is the read's one progress line.
func layerProgress(i, n int) string {
	return fmt.Sprintf("layers of image %d of %d", i, n)
}

// readLayers asks the daemon for the layers of each image of todo, in
// order, and hands each answer to put. An image that cannot be read is
// recorded with its reason and the read goes on; only cancellation stops
// it. Two GETs per image, nothing else.
func readLayers(ctx context.Context, r *jobs.Run, eng engine.Engine, todo []string, note string, put func(id string, info layerInfo)) error {
	for i, id := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.Steps(i, len(todo))
		r.Status(layerProgress(i+1, len(todo)) + note)
		l, err := eng.ImageLayers(ctx, id)
		var info layerInfo
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			info.err = escErr(err)
		} else {
			info = newLayerInfo(l)
		}
		put(id, info)
	}
	r.Steps(len(todo), len(todo))
	return nil
}

// layersToRead is every listed image whose layers the cache lacks, or
// holds only a failure for, in list order.
func (m *Model) layersToRead() []string {
	var todo []string
	seen := map[string]bool{}
	for _, img := range m.snap.Images {
		if !seen[img.ID] && !m.layerKnown(img.ID) && !m.lc.own[img.ID] {
			todo = append(todo, img.ID)
		}
		seen[img.ID] = true
	}
	return todo
}

// startLayerRead reads the layers of every listed image the cache lacks,
// as one cancellable job. It reports how many images that is; 0 starts
// nothing.
func (m *Model) startLayerRead() int {
	todo := m.layersToRead()
	if len(todo) == 0 || m.lc.run != nil {
		return 0
	}
	run := &layerRun{total: len(todo), ids: map[string]bool{}, layer: map[string]layerInfo{}}
	for _, id := range todo {
		run.ids[id] = true
	}
	eng := m.eng
	run.jobID = m.startJob("read image layers", fmt.Sprintf("%d image(s), GET inspect and history", len(todo)), func(ctx context.Context, r *jobs.Run) error {
		err := readLayers(ctx, r, eng, todo, "", func(id string, info layerInfo) {
			run.mu.Lock()
			run.layer[id] = info
			run.ver++
			run.mu.Unlock()
		})
		if err != nil {
			return err
		}
		failed := 0
		run.mu.Lock()
		for _, l := range run.layer {
			if l.err != "" {
				failed++
			}
		}
		run.mu.Unlock()
		r.Status(fmt.Sprintf("layers of %d image(s) read · %d could not be read", len(todo)-failed, failed))
		return nil
	})
	if run.jobID == 0 {
		return 0 // not started; the status line says why
	}
	m.lc.run = run
	return len(todo)
}

// collectLayers takes what the read job has produced since the last frame.
func (m *Model) collectLayers() {
	run := m.lc.run
	if run == nil {
		return
	}
	// State before results, as collectSpace does: a job seen finished has
	// written everything.
	j, ok := m.jobs.Get(run.jobID)
	finished := !ok || j.State != jobs.Running
	run.mu.Lock()
	changed := run.ver != run.taken
	run.taken = run.ver
	var got map[string]layerInfo
	if changed {
		got = make(map[string]layerInfo, len(run.layer))
		for k, v := range run.layer {
			got[k] = v
		}
		run.layer = map[string]layerInfo{}
	}
	run.mu.Unlock()
	if changed {
		for id, info := range got {
			// A failure never replaces what another read already holds.
			if info.err == "" || !m.layerKnown(id) {
				m.putLayers(id, info)
			}
		}
		if m.sp.du != nil {
			m.rebuildIndex() // the Space view reads the same cache
		}
		m.dirty = true
	}
	if finished {
		m.lc.run = nil
		m.pruneLayers()
		m.dirty = true
	}
}

// ownLayersMsg is the answer to the read of one image's own layers.
type ownLayersMsg struct {
	gen  uint64
	id   string
	info layerInfo
}

const ownLayersTimeout = 20 * time.Second

// readOwnLayers reads one image's layers: what Images › Layers needs
// before anything is asked of the operator.
func (m *Model) readOwnLayers(id string) tea.Cmd {
	if m.lc.own == nil {
		m.lc.own = map[string]bool{}
	}
	m.lc.own[id] = true
	gen, eng := m.gen, m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), ownLayersTimeout)
		defer cancel()
		l, err := eng.ImageLayers(ctx, id)
		if err != nil {
			return ownLayersMsg{gen: gen, id: id, info: layerInfo{err: escErr(err)}}
		}
		return ownLayersMsg{gen: gen, id: id, info: newLayerInfo(l)}
	}
}

func (m *Model) onOwnLayers(msg ownLayersMsg) {
	if msg.gen != m.gen {
		return // another daemon's answer
	}
	delete(m.lc.own, msg.id)
	if m.layerKnown(msg.id) {
		return // the engine-wide read answered first
	}
	m.putLayers(msg.id, msg.info)
	if m.sp.du != nil {
		m.rebuildIndex()
	}
}

// layersIndex is the content index of every listed image whose layers are
// held: which images hold a diff ID, at any depth. It is rebuilt only when
// the cache or the image list changed.
func (m *Model) layersIndex() *space.Index {
	want := [2]uint64{m.lc.ver, m.dataVersion}
	if m.lc.ix != nil && m.lc.ixFor == want {
		return m.lc.ix
	}
	imgs := make([]space.Image, 0, len(m.snap.Images))
	for _, img := range m.snap.Images {
		si := space.Image{ID: img.ID, SharedSize: -1}
		if l, ok := m.lc.byID[img.ID]; ok && l.err == "" {
			si.Diffs = l.diffs
		}
		imgs = append(imgs, si)
	}
	m.lc.ix, m.lc.ixFor = space.Build(imgs), want
	return m.lc.ix
}
