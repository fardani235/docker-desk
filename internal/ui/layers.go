package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
)

// The Layers tab of an image: one row per build step, oldest first, what
// each step made, and which other images on this daemon hold the same layer
// content (docs/DECISIONS.md, LY1–LY11; OpenSpec change add-layers-tab).
//
// Three sources, each named where its figure is drawn:
//
//   - the daemon's history and inspect (one read of the highlighted image,
//     made when the tab is shown): the steps, their sizes, the diff IDs;
//   - the archive, through the Findings analysis — the same job, cache and
//     consent rule: the exact step list (the config's empty_layer), each
//     layer's files, bytes, digest and findings;
//   - the engine-wide layer read, started only by `s`: which images hold a
//     diff ID. It shares its cache with the Space view.
//
// A step is bound to a layer only from evidence that cannot be wrong. The
// daemon's EmptyLayer field is never read: the engine does not decode it.
// Every instruction drawn is the masked form (forensics.Instruction); the
// daemon's created_by is not kept. The tab sends GET requests only.

const tabLayers = "Layers"

type layersState struct {
	shownID string // the image the cursor below belongs to
	cursor  int    // an index into the steps: it survives the list's source changing
	off     int
	listH   int
	w       int // the pane's width when last drawn
}

// layersTarget is the image the tab describes.
type layersTarget struct {
	img engine.Image
	ref string // as the daemon names it; escaped where drawn
}

func (m *Model) layersVisible() bool {
	return m.kind == KindImages && m.tabName() == tabLayers
}

func (m *Model) layersTarget() (t layersTarget, ok bool) {
	r, has := m.currentRow()
	if !has || m.kind != KindImages {
		return t, false
	}
	for _, x := range m.snap.Images {
		if x.ID == r.imageID {
			t.img = x
		}
	}
	if t.img.ID == "" {
		return t, false
	}
	t.ref = r.imageRef
	if t.ref == "" {
		t.ref = engine.ShortID(t.img.ID)
	}
	return t, true
}

// syncLayers runs once the highlight has settled: it reads the highlighted
// image's own layers, once. The analysis is the Findings one and is started
// by syncFindings; the engine-wide read waits for `s`. It does nothing
// while the tab is not shown.
func (m *Model) syncLayers() tea.Cmd {
	if !m.layersVisible() {
		return nil
	}
	t, ok := m.layersTarget()
	if !ok {
		return nil
	}
	id := t.img.ID
	if _, held := m.lc.byID[id]; held || m.lc.own[id] {
		return nil // read, or failed: `s` reads it again, the frame does not
	}
	if run := m.lc.run; run != nil && run.ids[id] {
		return nil // on its way with the engine-wide read
	}
	return m.readOwnLayers(id)
}

// ---- the view's facts ------------------------------------------------------------

// lyStep is one row. Its instruction is text already: forensics masked and
// escaped it, for the daemon's history and the archive's alike.
type lyStep struct {
	instr   string
	layer   int  // 1-based; 0 when it made none, or when nothing binds
	config  bool // the step made no layer
	size    int64
	hasSize bool // the daemon's history gave a size for this step
}

// lyView is everything the tab states about one image at one moment. The
// drawn rows, the block and the capture are all made from it, so they
// cannot disagree.
type lyView struct {
	id, ref string // ref is text
	have    bool   // the image's own layers were read
	ownErr  string
	own     layerInfo
	rep     *forensics.Report
	// repNote says why a report that exists is not used.
	repNote string

	steps []lyStep
	exact bool   // the steps are the archive's config history
	bound bool   // the steps name their layers
	why   string // why no step names a layer
	cut   bool   // the daemon's history was longer than the desk keeps

	layers  int
	sizes   []int64 // per layer; nil when not known
	sizeSrc string

	// sharers is, per layer, the other images holding the same content;
	// nil until every listed image has been asked.
	sharers [][]string
	others  int // listed images other than this one
	missing int // of those, never asked
	failed  int // of those, asked and not readable
	reading int // images the read in flight covers; 0 when none runs
	names   map[string]string
}

const (
	lySrcHistory = "the daemon's history"
	lySrcArchive = "the archive's file bytes"
)

func (m *Model) layersView(t layersTarget) lyView {
	v := lyView{id: t.img.ID, ref: esc(t.ref)}
	if ft, ok := m.findingsTarget(); ok && ft.img.ID == v.id {
		v.rep = m.fx.cache[ft.key]
	}
	if info, ok := m.lc.byID[v.id]; ok {
		v.own, v.ownErr, v.have = info, info.err, info.err == ""
	}
	if run := m.lc.run; run != nil {
		v.reading = run.total
	}
	for _, img := range m.snap.Images {
		if img.ID == v.id {
			continue
		}
		v.others++
		switch l, ok := m.lc.byID[img.ID]; {
		case !ok:
			v.missing++
		case l.err != "":
			v.failed++
		}
	}
	if !v.have {
		return v // nothing is listed from an image whose layers were not read
	}
	v.layers = len(v.own.diffs)
	if v.rep != nil && !archiveMatches(v.rep, v.own.diffs) {
		// The archive describes other layers than the daemon's inspect
		// does: nothing of it is laid over the daemon's list.
		v.repNote = fmt.Sprintf("The archive that was analysed holds %s that are not the %d the daemon's inspect names: its steps and contents are not shown.",
			countWord(len(v.rep.Layers), "layer", "layers"), v.layers)
		v.rep = nil
	}
	v.cut = v.own.cut

	// The daemon's own binding: only when exactly as many entries carry a
	// size as there are layers.
	var sized []int64
	for _, s := range v.own.steps {
		if s.size > 0 {
			sized = append(sized, s.size)
		}
	}
	// A history that was cut is not counted from: the part not kept may
	// hold sized entries too.
	counted := v.layers > 0 && len(sized) == v.layers && !v.cut

	switch {
	case v.rep != nil && len(v.rep.Steps) > 0:
		v.exact = true
		// The daemon's sizes are laid on the archive's steps only where
		// the two lists agree: as long, no size on a step the config says
		// made no layer, and no negative size.
		aligned := len(v.own.steps) == len(v.rep.Steps)
		for i := 0; aligned && i < len(v.rep.Steps); i++ {
			if n := v.own.steps[i].size; n < 0 || (v.rep.Steps[i].Empty && n != 0) {
				aligned = false
			}
		}
		made := 0
		for i, s := range v.rep.Steps {
			st := lyStep{instr: s.Instruction, config: s.Empty}
			if !s.Empty {
				made++
				if s.Layer >= 1 && s.Layer <= v.layers {
					st.layer = s.Layer
					v.bound = true
				}
				if aligned {
					st.size, st.hasSize = v.own.steps[i].size, true
				}
			}
			v.steps = append(v.steps, st)
		}
		switch {
		case made == 0 && v.layers == 0:
			v.bound = true // every step is config only, and there is no layer to bind
		case !v.bound:
			v.why = fmt.Sprintf("the archive's history has %d layer-making entries for %d layers", made, v.layers)
		case aligned:
			v.sizes = make([]int64, v.layers)
			for _, st := range v.steps {
				if st.layer > 0 {
					v.sizes[st.layer-1] = st.size
				}
			}
			v.sizeSrc = lySrcHistory
		}
	default:
		next := 0
		for _, s := range v.own.steps {
			st := lyStep{instr: s.instr}
			switch {
			case v.layers == 0:
				st.config = true // an image of no layers: no step made one
			case counted && s.size > 0:
				next++
				st.layer, st.size, st.hasSize = next, s.size, true
			case counted:
				st.config = true
			case s.size > 0:
				st.size, st.hasSize = s.size, true // the entry's own figure; bound to nothing
			}
			v.steps = append(v.steps, st)
		}
		v.bound = counted || v.layers == 0
		if !v.bound {
			v.why = fmt.Sprintf("%d of the daemon's %d history entries carry a size, for %d layers: the daemon's history does not say which step made which layer; the archive's history would",
				len(sized), len(v.own.steps), v.layers)
		}
	}
	if v.sizes == nil && counted {
		v.sizes, v.sizeSrc = sized, lySrcHistory
		if v.exact && v.bound {
			for i := range v.steps {
				if l := v.steps[i].layer; l > 0 {
					v.steps[i].size, v.steps[i].hasSize = sized[l-1], true
				}
			}
		}
	}
	if v.sizes == nil && v.rep != nil && len(v.rep.Layers) == v.layers && v.layers > 0 {
		sizes := make([]int64, v.layers)
		whole := true
		for i, l := range v.rep.Layers {
			sizes[i] = l.Size
			whole = whole && !l.Missing && l.Unreadable == ""
		}
		if whole {
			v.sizes, v.sizeSrc = sizes, lySrcArchive
		}
	}

	if v.missing == 0 {
		v.sharers = m.layersIndex().ContentSharers(v.id)
		v.names = map[string]string{}
		for _, img := range m.snap.Images {
			v.names[img.ID] = holderName(img)
		}
	}
	return v
}

// archiveMatches reports whether the analysed archive is of the layers the
// daemon's inspect names: as many, and the same diff ID wherever the
// archive's layer could be hashed.
func archiveMatches(rep *forensics.Report, diffs []string) bool {
	if len(rep.Layers) != len(diffs) {
		return false
	}
	for i, l := range rep.Layers {
		if l.DiffID != "" && l.DiffID != diffs[i] {
			return false
		}
	}
	return true
}

// holderName is how a holder is named: its reference, or its short ID when
// it has none. Text.
func holderName(img engine.Image) string {
	for _, r := range img.Refs() {
		return esc(r)
	}
	return escID(img.ID)
}

// repLayer is the archive's account of a 1-based layer, if there is one.
func (v *lyView) repLayer(layer int) *forensics.Layer {
	if v.rep == nil || layer < 1 || layer > len(v.rep.Layers) {
		return nil
	}
	return &v.rep.Layers[layer-1]
}

// held is the other images holding a 1-based layer's content; known is
// false until every listed image has been asked.
func (v *lyView) held(layer int) (ids []string, known bool) {
	if v.sharers == nil || layer < 1 || layer > len(v.sharers) {
		return nil, false
	}
	return v.sharers[layer-1], true
}

func countWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summary is the tab's first line: how many layers another image here
// holds, what the others cost, where each figure came from, and what is
// not known. animate adds the spinner; a capture has none.
func (m *Model) layersSummary(v lyView, animate bool) (tone, string) {
	switch {
	case v.ownErr != "":
		return toneBad, "This image's layers could not be read: " + v.ownErr + " — s reads them again."
	case !v.have:
		return toneDim, "Reading this image's layers (inspect, history)…"
	case v.layers == 0:
		return toneDim, "This image has no layers: nothing to share."
	}
	if v.sharers == nil {
		lead := ""
		if animate {
			lead = spinnerFrames[m.spinner] + " "
		}
		switch {
		case v.reading > 0:
			return toneAccent, fmt.Sprintf("%sReading the layers of %s, one inspect each (GET only); the Jobs pane shows the progress and cancels it.",
				lead, countWord(v.reading, "image", "images"))
		case m.sp.run != nil:
			return toneAccent, lead + "The Space view's measurement is reading the images' layers; the holders follow from the same read."
		}
		return toneWarn, fmt.Sprintf("Holders not known: s reads the layers of %s, one inspect each (GET only), to say which hold these %s.",
			countWord(v.missing+v.failed, "other image", "other images"), countWord(v.layers, "layer", "layers"))
	}
	shared := 0
	var own int64
	for i := 0; i < v.layers; i++ {
		if len(v.sharers[i]) > 0 {
			shared++
		} else if v.sizes != nil {
			own += v.sizes[i]
		}
	}
	rest := v.layers - shared
	atLeast := ""
	if v.failed > 0 {
		atLeast = "at least "
	}
	read := fmt.Sprintf(" · holders from the diff IDs of %s (the daemon's inspect)", countWord(v.others-v.failed+1, "image", "images"))
	var s string
	switch {
	case shared == 0 && v.failed == 0 && v.layers == 1:
		s = "No other image here holds its 1 layer"
	case shared == 0 && v.failed == 0:
		s = fmt.Sprintf("No other image here holds any of its %d layers", v.layers)
	case shared == v.layers:
		s = fmt.Sprintf("All %d layers are held by another image here", v.layers)
	default:
		verb := "are"
		if shared == 1 {
			verb = "is"
		}
		s = fmt.Sprintf("%s%d of %d layers %s held by another image here", atLeast, shared, v.layers, verb)
	}
	if rest > 0 {
		which, verb := fmt.Sprintf("the other %d", rest), "hold"
		switch {
		case shared == 0 && rest == 1:
			which, verb = "it", "holds"
		case shared == 0:
			which = "they"
		case rest == 1:
			which, verb = "the other one", "holds"
		}
		if v.sizes != nil {
			most := ""
			if v.failed > 0 {
				most = "at most " // an unread image can only take a layer out of this set
			}
			s += fmt.Sprintf(" · %s %s %s%s (%s)", which, verb, most, humanBytes(own), v.sizeSrc)
		} else {
			s += fmt.Sprintf(" · what %s %s is not known (the daemon's history does not give each layer a size)", which, verb)
		}
	}
	if v.failed > 0 {
		s += fmt.Sprintf(" · %s not read, so a layer may have more holders: s tries again", countWord(v.failed, "image was", "images were"))
		return toneWarn, s + read
	}
	return toneNormal, s + read
}

// layersUnbound names the held layers by number when no row can: the
// history does not bind, or there is none. "" when a row names each layer,
// or when the holders are not known. whole names every holder.
func layersUnbound(v lyView, w int, whole bool) string {
	if v.sharers == nil || v.layers == 0 || (v.bound && len(v.steps) > 0) {
		return ""
	}
	var parts []string
	for i, ids := range v.sharers {
		if len(ids) == 0 {
			continue
		}
		names := make([]string, len(ids))
		for j, id := range ids {
			names[j] = v.names[id]
		}
		lead := fmt.Sprintf("L%d by ", i+1)
		if whole {
			parts = append(parts, lead+strings.Join(names, ", "))
		} else {
			parts = append(parts, lead+fitNames(names, max(w/3, 16)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Held, by layer number (no step is bound to one): " + strings.Join(parts, " · ")
}

// layersSource says which list is shown and how its steps were bound.
func layersSource(v lyView) (tone, string) {
	if !v.have {
		return toneDim, ""
	}
	if len(v.steps) == 0 {
		if v.exact {
			return toneDim, "" // cannot be: exact means the archive listed steps
		}
		return toneDim, "The daemon reports no build history for this image."
	}
	head := fmt.Sprintf("%s · %s · ", countWord(len(v.steps), "step", "steps"), countWord(v.layers, "layer", "layers"))
	var s string
	tn := toneDim
	switch {
	case v.exact && v.bound:
		s = "the archive's history (exact: its empty_layer says which steps made a layer)"
	case v.exact:
		s = "the archive's history; no step names a layer: " + v.why
		tn = toneWarn
	case v.bound && v.layers == 0:
		s = "the daemon's history; none made a layer"
	case v.bound:
		s = "the daemon's history, bound by counting: exactly as many entries carry a size as there are layers"
	default:
		s = "the daemon's history; no step names a layer: " + v.why
		tn = toneWarn
	}
	if v.rep != nil && !v.exact {
		s += " (the archive was read and holds no history)"
	}
	if v.cut {
		s += fmt.Sprintf(" · only the first %d entries are kept", maxKeptSteps)
	}
	return tn, head + s
}

// layersAnalysis is the one line about the archive while there is no
// report: running, failed, waiting for consent. "" once it has been read.
func (m *Model) layersAnalysis(v lyView, animate bool) (tone, string) {
	if v.rep != nil {
		return toneDim, ""
	}
	if v.repNote != "" {
		return toneWarn, v.repNote
	}
	t, ok := m.findingsTarget()
	if !ok || t.img.ID != v.id {
		if m.detail.err != "" && m.detail.key == m.wantDetail {
			return toneBad, "The archive is not read: inspect failed, so the image's platform is unknown: " + m.detail.err
		}
		return toneDim, "The archive is not read yet: reading the image's platform…"
	}
	lead := ""
	if animate {
		lead = spinnerFrames[m.spinner] + " "
	}
	switch run := m.fx.run; {
	case run != nil && run.key == t.key:
		p, _ := run.snapshot()
		return toneAccent, fmt.Sprintf("%sAnalysing the archive for the exact steps and each layer's contents: %s read · %d layers found. c cancels.",
			lead, humanBytes(p.BytesRead), p.Layers)
	case m.fx.failed[t.key] != "":
		return toneBad, "The analysis ended without a report: " + m.fx.failed[t.key] + " — Enter analyses again."
	case m.needsConsent(t):
		return toneWarn, fmt.Sprintf("%s is %s, and the daemon is remote (%s): Enter analyses it; nothing is requested until you do. The whole archive crosses the network, once.",
			v.ref, humanBytes(t.img.Size), esc(m.endpoint.Host))
	}
	return toneDim, "Starting the analysis of the archive…"
}

// ---- the highlighted step's block -------------------------------------------------

type lyFact struct {
	k, v string
	t    tone
}

// layersBlock is what the tab states about one step. whole names every
// holder and every finding (a capture); otherwise each list is one line of
// w cells and says how many more there are.
func (m *Model) layersBlock(v lyView, i, w int, whole bool) []lyFact {
	s := v.steps[i]
	var out []lyFact
	add := func(k, val string, t tone) { out = append(out, lyFact{k, val, t}) }
	pos := fmt.Sprintf("%d of %d", i+1, len(v.steps))
	room := max(w-10, 16)

	switch {
	case s.config:
		add("step", pos+" · config only: it changed the image's configuration, not its files, and made no layer", toneNormal)
		add("by", s.instr, toneNormal)
		return out
	case s.layer == 0:
		why := v.why
		if why == "" {
			why = "the history does not bind to the layers"
		}
		add("step", pos+" · which layer it made is unknown: "+why, toneWarn)
		if s.hasSize && s.size > 0 {
			add("size", humanBytes(s.size)+" ("+lySrcHistory+", for this entry)", toneNormal)
		}
		add("by", s.instr, toneNormal)
		return out
	}

	how := "counting the sizes in " + lySrcHistory
	if v.exact {
		how = "the archive's history"
	}
	add("step", fmt.Sprintf("%s · made layer L%d of %d (bound by %s)", pos, s.layer, v.layers, how), toneNormal)

	// Who else holds this content.
	switch ids, known := v.held(s.layer); {
	case !known && v.reading > 0:
		add("held by", "being read", toneDim)
	case !known:
		add("held by", fmt.Sprintf("not known: s reads the layers of %s", countWord(v.missing+v.failed, "other image", "other images")), toneDim)
	case len(ids) == 0 && v.failed > 0:
		add("held by", fmt.Sprintf("no image that was read; %s not read", countWord(v.failed, "image was", "images were")), toneWarn)
	case len(ids) == 0:
		add("held by", "no other image here", toneDim)
	default:
		names := make([]string, len(ids))
		for j, id := range ids {
			names[j] = v.names[id]
		}
		lead := countWord(len(ids), "other image", "other images") + ": "
		if v.failed > 0 {
			lead = "at least " + lead
		}
		if whole {
			add("held by", lead+strings.Join(names, " · "), toneAccent)
		} else {
			add("held by", lead+fitNames(names, room-ansi.StringWidth(lead)), toneAccent)
		}
	}

	// Bytes: the daemon's figure, then the archive's two.
	l := v.repLayer(s.layer)
	var bytes []string
	if s.hasSize {
		bytes = append(bytes, humanBytes(s.size)+" ("+lySrcHistory+")")
	}
	walked := l != nil && !l.Missing && l.Unreadable == ""
	if walked {
		stored := "uncompressed"
		if l.Compression != "" && l.Compression != "none" {
			stored = esc(l.Compression)
		}
		bytes = append(bytes, "its files hold "+humanBytes(l.Size), "the archive stores "+humanBytes(l.Stored)+" ("+stored+")")
	}
	if len(bytes) == 0 {
		bytes = append(bytes, "not known: the daemon's history gives this step no size")
	}
	add("size", strings.Join(bytes, " · "), toneNormal)

	// Contents.
	switch {
	case l == nil:
		add("wrote", "not known yet: the archive has not been read", toneDim)
	case l.Missing:
		add("wrote", "not known: the manifest lists this layer and the archive does not hold it", toneWarn)
	case l.Unreadable != "":
		add("wrote", "not known: the layer could not be walked: "+l.Unreadable, toneWarn)
	default:
		add("wrote", fmt.Sprintf("%s · %s · deleted %s · %s",
			countWord(l.Files, "file", "files"), countWord(l.Dirs, "directory", "directories"),
			countWord(l.Whiteouts, "path", "paths"), countWord(l.Opaques, "opaque directory", "opaque directories")), toneNormal)
	}

	// The content digest: the archive's, checked against the config; the
	// daemon's until then.
	switch {
	case l != nil && l.DiffID != "" && l.Verified:
		add("digest", esc(l.DiffID)+" · matches the config's diff_id", toneNormal)
	case l != nil && l.DiffID != "":
		add("digest", esc(l.DiffID)+" · does NOT match the config's diff_id", toneBad)
	default:
		add("digest", esc(v.own.diffs[s.layer-1])+" · the daemon's diff ID; not checked against the archive", toneDim)
	}

	if v.rep != nil {
		var on []string
		for _, f := range v.rep.Findings {
			if f.Layer == s.layer {
				on = append(on, strings.TrimSpace(f.Rule+" "+f.Path))
			}
		}
		switch {
		case len(on) == 0:
			add("findings", "none on this layer", toneDim)
		case whole:
			add("findings", fmt.Sprintf("%d on this layer: %s", len(on), strings.Join(on, " · ")), toneWarn)
		default:
			lead := fmt.Sprintf("%d on this layer: ", len(on))
			add("findings", lead+fitNames(on, room-ansi.StringWidth(lead)), toneWarn)
		}
	}
	add("by", s.instr, toneNormal)
	return out
}

// fitNames joins as many names as w cells hold and says how many more
// there are; at least one name is always given.
func fitNames(names []string, w int) string {
	const sep = " · "
	out, used := "", 0
	for i, n := range names {
		more := ""
		if rest := len(names) - i - 1; rest > 0 {
			more = fmt.Sprintf(" +%d more", rest)
		}
		add := ansi.StringWidth(n)
		if i > 0 {
			add += len([]rune(sep))
		}
		if i > 0 && used+add+ansi.StringWidth(more) > w {
			return out + fmt.Sprintf(" +%d more", len(names)-i)
		}
		if i > 0 {
			out += sep
		}
		out += n
		used += add
	}
	return out
}

// blockLines lays facts out under a key column, values wrapped to w.
func (m *Model) blockLines(facts []lyFact, w int) []string {
	st := m.st
	var out []string
	for _, f := range facts {
		for i, part := range wrapWords(f.v, max(w-9, 10)) {
			label := "         "
			if i == 0 {
				label = st.tone(toneDim, fit(f.k, 8)) + " "
			}
			out = append(out, label+st.tone(f.t, part))
		}
	}
	return out
}

// ---- keys -------------------------------------------------------------------------

// onLayersKey handles the tab's own keys; it reports whether key was one.
func (m *Model) onLayersKey(key string) bool {
	switch key {
	case "enter":
		if t, ok := m.findingsTarget(); ok {
			m.analyseNow(t)
		}
		return true
	case "c":
		if t, ok := m.findingsTarget(); ok {
			m.cancelAnalysis(t)
		}
		return true
	case "s":
		m.readAllLayers()
		return true
	}
	page := max(m.ly.listH-1, 1)
	delta := 0
	switch key {
	case "down", "j":
		delta = 1
	case "up", "k":
		delta = -1
	case "pgdown", "ctrl+f", " ":
		delta = page
	case "pgup", "ctrl+b":
		delta = -page
	case "g", "home":
		delta = -1 << 30
	case "G", "end":
		delta = 1 << 30
	default:
		return false
	}
	m.moveLayersCursor(delta)
	return true
}

// readAllLayers is `s`: the engine-wide read, said before it starts.
func (m *Model) readAllLayers() {
	switch {
	case m.lc.run != nil:
		m.setStatus(toneDim, "The layers are already being read; the Jobs pane cancels it.")
		return
	case m.sp.run != nil:
		m.setStatus(toneDim, "The Space view's measurement is already reading the images' layers.")
		return
	}
	n := m.startLayerRead()
	if n == 0 {
		if len(m.layersToRead()) == 0 {
			m.setStatus(toneDim, "The layers of every listed image are already read: nothing to ask the daemon.")
		}
		return
	}
	m.setStatus(toneAccent, fmt.Sprintf("Reading the layers of %s, one inspect each (GET only).", countWord(n, "image", "images")))
}

func (m *Model) moveLayersCursor(delta int) {
	t, ok := m.layersTarget()
	if !ok {
		return
	}
	n := len(m.layersView(t).steps)
	if n == 0 {
		return
	}
	if t.img.ID != m.ly.shownID {
		m.ly = layersState{shownID: t.img.ID, listH: m.ly.listH}
	}
	// Saturating: g and G pass the largest deltas there are.
	m.ly.cursor = clamp(clamp(m.ly.cursor, 0, n-1)+clamp(delta, -n, n), 0, n-1)
}

// ---- drawing ------------------------------------------------------------------------

// lyCols is which columns a pane w cells wide holds. The instruction always
// stays; the files go first, then the size.
type lyCols struct {
	numW                int
	size, files, holder bool
}

func layersCols(v lyView, w int) lyCols {
	c := lyCols{numW: max(len(fmt.Sprint(len(v.steps))), 2)}
	c.size = w >= 72
	c.files = w >= 100 && v.rep != nil
	c.holder = v.sharers != nil
	return c
}

func layersColHeader(c lyCols) string {
	s := fmt.Sprintf("%*s %-6s", c.numW, "#", "layer")
	if c.size {
		s += fmt.Sprintf(" %8s", "size")
	}
	if c.files {
		s += fmt.Sprintf(" %7s", "files")
	}
	if c.holder {
		s += fmt.Sprintf(" %4s", "held")
	}
	return s + "  instruction"
}

func layersRowText(v lyView, i int, c lyCols) string {
	s := v.steps[i]
	layer := "?"
	switch {
	case s.config:
		layer = "config"
	case s.layer > 0:
		layer = fmt.Sprintf("L%d", s.layer)
	}
	out := fmt.Sprintf("%*d %-6s", c.numW, i+1, layer)
	if c.size {
		size := ""
		if s.hasSize && !s.config {
			size = humanBytes(s.size)
		}
		out += fmt.Sprintf(" %8s", size)
	}
	if c.files {
		files := ""
		if l := v.repLayer(s.layer); l != nil && !l.Missing && l.Unreadable == "" {
			files = fmt.Sprint(l.Files)
		}
		out += fmt.Sprintf(" %7s", files)
	}
	if c.holder {
		held := ""
		if ids, known := v.held(s.layer); known {
			held = fmt.Sprint(len(ids))
			if len(ids) == 0 {
				held = "-"
			}
		}
		out += fmt.Sprintf(" %4s", held)
	}
	return out + "  " + s.instr
}

func (m *Model) renderLayers(w, h int) []string {
	st := m.st
	t, ok := m.layersTarget()
	if !ok {
		return []string{st.tone(toneDim, "Highlight an image to see its build steps here.")}
	}
	if t.img.ID != m.ly.shownID {
		m.ly = layersState{shownID: t.img.ID}
	}
	m.ly.w = w
	if ft, ok := m.findingsTarget(); ok {
		m.cached(ft.key) // drawn: the report is in use, as on Findings
	}
	v := m.layersView(t)

	type headLine struct {
		tn   tone
		text string
	}
	var head []headLine
	push := func(tn tone, s string) {
		if s != "" {
			head = append(head, headLine{tn, s})
		}
	}
	push(m.layersSummary(v, true))
	push(layersSource(v))
	push(toneAccent, layersUnbound(v, w, false))
	push(m.layersAnalysis(v, true))

	if len(v.steps) == 0 {
		// Nothing to list: every line whole, and no row invented.
		var out []string
		for _, l := range head {
			for _, part := range wrapWords(l.text, w) {
				out = append(out, st.tone(l.tn, part))
			}
		}
		return out
	}

	var out []string
	if h < 12 && len(head) > 2 {
		// A short pane: the summary, and the line that asks for something.
		head = []headLine{head[0], head[len(head)-1]}
	}
	// A head line takes one row in a short pane and up to two in a tall
	// one; what does not fit ends in an ellipsis, and W writes it whole.
	rows := 1
	if h >= 16 {
		rows = 2
	}
	for _, l := range head {
		parts := wrapWords(l.text, w)
		if len(parts) > rows {
			parts = append(parts[:rows-1], fit(strings.Join(parts[rows-1:], " "), w))
		}
		for _, part := range parts {
			out = append(out, st.tone(l.tn, part))
		}
	}
	room := h - len(out)
	cols := layersCols(v, w)
	cur := clamp(m.ly.cursor, 0, len(v.steps)-1)
	m.ly.cursor = cur
	block := m.blockLines(m.layersBlock(v, cur, w, false), w)
	header, rule := room >= 10, room >= 9
	// The block takes the rows it needs, up to a little under half the
	// pane; the list has the rest.
	blockH := clamp(room*45/100, 2, 11)
	if room < 8 {
		blockH = max(room-2, 1)
	}
	blockH = min(blockH, len(block))
	listH := room - blockH
	if header {
		listH--
	}
	if rule {
		listH--
	}
	listH = max(listH, 1)
	m.ly.listH = listH

	m.ly.off = clamp(m.ly.off, max(cur-listH+1, 0), cur)
	if header {
		out = append(out, st.tone(toneDim, fit("  "+layersColHeader(cols), w)))
	}
	focused := m.focus == PaneDetails
	top := len(out)
	for i := m.ly.off; i < len(v.steps) && i < m.ly.off+listH; i++ {
		line := fit(layersRowText(v, i, cols), w-2)
		s := v.steps[i]
		tn := toneNormal
		if s.config {
			tn = toneDim
		}
		switch {
		case i == cur && focused:
			out = append(out, st.selected.Render("▸ "+line))
		case i == cur:
			out = append(out, "▸ "+st.tone(tn, line))
		default:
			out = append(out, "  "+st.tone(tn, line))
		}
	}
	for len(out) < top+listH {
		out = append(out, "")
	}
	if rule {
		pos := fmt.Sprintf(" %d of %d ", cur+1, len(v.steps))
		out = append(out, st.tone(toneDim, strings.Repeat("─", max(w-len([]rune(pos))-1, 0))+pos+"─"))
	}
	if len(block) > blockH {
		block = block[:blockH]
	}
	return append(out, block...)
}

// layersLines is the whole build as text: the head, then every step with
// its block, laid out for a page w cells wide. It is what `W` writes; it
// depends on the facts alone, never on the pane's width, the scroll or the
// frame.
func (m *Model) layersLines(v lyView, w int) []string {
	var out []string
	say := func(_ tone, s string) {
		if s != "" {
			out = append(out, wrapWords(s, w)...)
		}
	}
	say(m.layersSummary(v, false))
	say(layersSource(v))
	say(toneAccent, layersUnbound(v, w, true))
	switch {
	case v.repNote != "":
		say(toneWarn, v.repNote)
	case v.rep == nil:
		say(toneDim, "The archive was not read: what each layer wrote, its digest check and its findings are not in this file.")
	}
	for i := range v.steps {
		s := v.steps[i]
		title := fmt.Sprintf("step %d", i+1)
		switch {
		case s.config:
			title += " · config only"
		case s.layer > 0:
			title += fmt.Sprintf(" · L%d", s.layer)
		default:
			title += " · layer unknown"
		}
		out = append(out, "", title)
		for _, f := range m.layersBlock(v, i, w, true) {
			for j, part := range wrapWords(f.v, max(w-11, 10)) {
				label := strings.Repeat(" ", 11)
				if j == 0 {
					label = "  " + fit(f.k, 8) + " "
				}
				out = append(out, label+part)
			}
		}
	}
	return out
}

// layersHints is the footer's part for the tab: only what would do
// something now.
func (m *Model) layersHints() []string {
	var pairs []string
	t, ok := m.layersTarget()
	if !ok {
		return nil
	}
	if m.lc.run == nil && m.sp.run == nil && len(m.layersToRead()) > 0 {
		pairs = append(pairs, "s", "read holders")
	}
	if ft, ok := m.findingsTarget(); ok && ft.img.ID == t.img.ID && m.fx.cache[ft.key] == nil {
		// No hint for a report that exists and is not used (repNote).
		switch run := m.fx.run; {
		case run != nil && run.key == ft.key:
			pairs = append(pairs, "c", "cancel")
		case m.fx.failed[ft.key] != "" || m.needsConsent(ft):
			pairs = append(pairs, "⏎", "analyse")
		}
	}
	return pairs
}
