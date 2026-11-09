package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/report/collect"
)

// The Findings tab of an image: what its own archive holds, joined with what
// the daemon is running from it now (docs/DECISIONS.md, F1–F12).
//
// Opening the tab analyses the highlighted image as a job — the archive is
// streamed from GET /images/get and read once — and keeps the report by
// image ID and platform, so a second open is instant. The live half is
// recomputed every time the pane is drawn.

const tabFindings = "Findings"

// findingsCacheSize is how many reports are kept. A report is a few hundred
// findings at most; the archive itself is never kept.
const findingsCacheSize = 8

// defaultAskAbove is the image size above which analysing an image on a
// remote daemon waits for Enter: the whole archive crosses the network.
const defaultAskAbove = 500_000_000

type findingsState struct {
	cache map[string]*forensics.Report
	lru   []string // keys, least recently used first
	// storedAt is when each cached report was made (the Space view gives
	// its figures their age).
	storedAt map[string]time.Time

	order     forensics.Order
	shownKey  string // the report the cursor below belongs to
	cursor    int
	cursorKey string // identity of the highlighted finding
	off       int
	listH     int
	// revealed is the key of the one finding whose secret is on screen.
	revealed string

	run     *findingsRun      // the analysis in flight, if any
	failed  map[string]string // why the last analysis of a key ended without a report
	consent map[string]bool   // remote, large images the operator chose to analyse

	facts     map[string]forensics.ContainerFacts
	factsFor  uint64 // dataVersion the facts were asked for
	factsBusy bool
}

// findingsRun is the hand-over point between the analysis job and the desk.
type findingsRun struct {
	key, ref string
	jobID    int

	mu       sync.Mutex
	progress forensics.Progress
	rep      *forensics.Report
}

func (r *findingsRun) setProgress(p forensics.Progress) {
	r.mu.Lock()
	r.progress = p
	r.mu.Unlock()
}

func (r *findingsRun) snapshot() (forensics.Progress, *forensics.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress, r.rep
}

type factsMsg struct {
	version uint64
	facts   map[string]forensics.ContainerFacts
}

// findingsTarget is the image the tab should describe, once its platform is
// known from inspect.
type findingsTarget struct {
	key  string
	img  engine.Image
	ref  string
	plat engine.Platform
}

func (m *Model) findingsVisible() bool {
	return m.kind == KindImages && m.tabName() == tabFindings
}

// findingsTarget returns the highlighted image and its cache key; ok is false
// until inspect has said which platform it is.
func (m *Model) findingsTarget() (t findingsTarget, ok bool) {
	r, has := m.currentRow()
	if !has || m.kind != KindImages {
		return t, false
	}
	for _, x := range m.snap.Images {
		if x.ID == r.imageID {
			t.img = x
		}
	}
	d := m.detail.image
	if t.img.ID == "" || d == nil || d.ID != t.img.ID {
		return t, false
	}
	t.plat = d.Platform()
	t.ref = r.imageRef
	if t.ref == "" {
		t.ref = engine.ShortID(t.img.ID)
	}
	t.key = t.img.ID + "|" + t.plat.String()
	return t, true
}

func (m *Model) askAbove() int64 {
	if m.opts.FindingsAskAbove != 0 {
		return m.opts.FindingsAskAbove
	}
	return defaultAskAbove
}

// needsConsent reports whether analysing the image waits for Enter.
func (m *Model) needsConsent(t findingsTarget) bool {
	limit := m.askAbove()
	return m.link.Remote && limit > 0 && t.img.Size > limit && !m.fx.consent[t.key]
}

func (m *Model) cached(key string) *forensics.Report {
	rep := m.fx.cache[key]
	if rep != nil {
		for i, k := range m.fx.lru {
			if k == key {
				m.fx.lru = append(append(m.fx.lru[:i:i], m.fx.lru[i+1:]...), key)
				break
			}
		}
	}
	return rep
}

func (m *Model) store(key string, rep *forensics.Report) {
	if m.fx.cache == nil {
		m.fx.cache = map[string]*forensics.Report{}
	}
	if _, ok := m.fx.cache[key]; !ok {
		m.fx.lru = append(m.fx.lru, key)
	}
	m.fx.cache[key] = rep
	if m.fx.storedAt == nil {
		m.fx.storedAt = map[string]time.Time{}
	}
	m.fx.storedAt[key] = time.Now()
	for len(m.fx.lru) > findingsCacheSize {
		delete(m.fx.storedAt, m.fx.lru[0])
		delete(m.fx.cache, m.fx.lru[0])
		m.fx.lru = m.fx.lru[1:]
	}
}

// syncFindings runs on every frame once the highlight has settled. It
// collects a finished analysis, starts the one the tab needs, and keeps the
// inspected container facts current. It does nothing while the tab is not
// on screen, except collect.
func (m *Model) syncFindings() tea.Cmd {
	m.collectFindings()
	if !m.findingsVisible() {
		return nil
	}
	t, ok := m.findingsTarget()
	if !ok {
		return nil
	}
	if run := m.fx.run; run != nil && run.key != t.key {
		// The operator has moved on to another image: this analysis is
		// no longer wanted. At most one runs, so browsing with the tab
		// open costs one stream at a time.
		m.jobs.Cancel(run.jobID)
		m.fx.run = nil
	}
	if m.fx.cache[t.key] == nil && m.fx.run == nil && m.fx.failed[t.key] == "" && !m.needsConsent(t) {
		m.startAnalysis(t)
	}
	if m.fx.cache[t.key] != nil && m.fx.factsFor != m.dataVersion && !m.fx.factsBusy {
		return m.inspectUsers(t.img)
	}
	return nil
}

// collectFindings takes the report of an analysis that has ended.
func (m *Model) collectFindings() {
	run := m.fx.run
	if run == nil {
		return
	}
	j, ok := m.jobs.Get(run.jobID)
	if !ok || j.State == jobs.Running {
		return
	}
	m.fx.run = nil
	m.dirty = true
	_, rep := run.snapshot()
	switch {
	case j.State == jobs.Succeeded && rep != nil:
		m.store(run.key, rep)
		delete(m.fx.failed, run.key)
	case j.State == jobs.Cancelled:
		m.fx.failed[run.key] = "Cancelled."
	default:
		m.fx.failed[run.key] = j.Err
	}
}

// startAnalysis streams the image's archive through the analyser as a job.
// The job records only counts, digests and the desk's own words: no byte of
// the image reaches its title, status, log or error.
func (m *Model) startAnalysis(t findingsTarget) {
	if m.fx.failed == nil {
		m.fx.failed = map[string]string{}
	}
	run := &findingsRun{key: t.key, ref: t.ref}
	eng := m.eng
	// The command line's --report analyses with the same options.
	opts := collect.AnalysisOptions(t.img.ID, t.ref, t.plat, m.info)
	id, plat := t.img.ID, t.plat
	run.jobID = m.jobs.Start("analyse image", t.ref, func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking the daemon for the archive")
		rc, err := eng.SaveImage(ctx, id, plat)
		if err != nil {
			return err
		}
		defer rc.Close()
		var last time.Time
		rep, err := forensics.Analyse(ctx, rc, opts, func(p forensics.Progress) {
			run.setProgress(p)
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
				last = now
				r.Status(fmt.Sprintf("%s read · %d layers found", humanBytes(p.BytesRead), p.Layers))
			}
		})
		if err != nil {
			return err
		}
		r.Logf("%s layout, %s, %d layers, %s read", rep.Layout, rep.Platform, len(rep.Layers), humanBytes(rep.BytesRead))
		for _, l := range rep.Layers {
			r.Logf("layer %d  %s  %s  %d files", l.Index, engine.ShortID(l.Digest), humanBytes(l.Size), l.Files)
		}
		r.Logf("%d findings from the archive", len(rep.Findings))
		r.Status(fmt.Sprintf("%d layers · %d findings", len(rep.Layers), len(rep.Findings)))
		run.mu.Lock()
		run.rep = rep
		run.mu.Unlock()
		return nil
	})
	m.fx.run = run
}

// inspectUsers fetches what only inspect tells — privileged, user, the
// reference a container was created from — for the containers the live join
// is about: those using the image, and those whose listed image is an ID
// (the list shows that once the tag they were created from has moved).
func (m *Model) inspectUsers(img engine.Image) tea.Cmd {
	ids := collect.FactsTargets(img, m.snap.Containers)
	m.fx.factsBusy = true
	version, eng := m.dataVersion, m.eng
	return func() tea.Msg {
		out := map[string]forensics.ContainerFacts{}
		for _, id := range ids {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			d, err := eng.InspectContainer(ctx, id)
			cancel()
			if err == nil {
				out[id] = forensics.ContainerFacts{Privileged: d.Privileged, User: d.User, Ref: d.Image}
			}
		}
		return factsMsg{version: version, facts: out}
	}
}

func (m *Model) onFacts(msg factsMsg) {
	m.fx.factsBusy = false
	m.fx.factsFor = msg.version
	if m.fx.facts == nil {
		m.fx.facts = map[string]forensics.ContainerFacts{}
	}
	for id, f := range msg.facts {
		m.fx.facts[id] = f
	}
}

// currentFindings is the report's findings joined with live state, in the
// chosen order.
func (m *Model) currentFindings(rep *forensics.Report, img engine.Image) []forensics.Finding {
	in := forensics.LiveInput{Image: img, Images: m.snap.Images, Containers: m.snap.Containers, Facts: m.fx.facts}
	fs := append(append([]forensics.Finding{}, rep.Findings...), forensics.LiveFindings(rep, in)...)
	forensics.Sort(fs, m.fx.order)
	return fs
}

// ---- keys -------------------------------------------------------------------

// onFindingsKey handles the tab's own keys; it reports whether key was one.
func (m *Model) onFindingsKey(key string) bool {
	t, ok := m.findingsTarget()
	switch key {
	case "enter":
		if !ok {
			return true
		}
		if m.fx.cache[t.key] == nil && m.fx.run == nil {
			if m.fx.consent == nil {
				m.fx.consent = map[string]bool{}
			}
			m.fx.consent[t.key] = true
			delete(m.fx.failed, t.key)
			m.startAnalysis(t)
		}
		return true
	case "c":
		if run := m.fx.run; run != nil && ok && run.key == t.key {
			m.jobs.Cancel(run.jobID)
			m.setStatus(toneWarn, "Cancelling: analyse image "+run.ref)
		}
		return true
	case "i":
		m.openPackages()
		return true
	case "w":
		m.exportFindings()
		return true
	case "o":
		m.fx.order = m.fx.order.Next()
		m.fx.revealed = ""
		m.setStatus(toneDim, "Findings ordered by "+m.fx.order.String()+".")
		return true
	case "v":
		if !ok {
			return true
		}
		rep := m.fx.cache[t.key]
		if rep == nil {
			return true
		}
		fs := m.currentFindings(rep, t.img)
		i := m.findingsCursor(fs)
		if i < 0 || !fs[i].Secret.Present() {
			m.setStatus(toneDim, "The highlighted finding has no value to reveal.")
			return true
		}
		if m.fx.revealed == fs[i].Key() {
			m.fx.revealed = ""
			return true
		}
		m.fx.revealed = fs[i].Key()
		m.setStatus(toneWarn, "Showing one secret value. It hides when the cursor moves, or with v.")
		return true
	}
	page := max(m.fx.listH-1, 1)
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
	m.moveFindingsCursor(delta)
	return true
}

func (m *Model) moveFindingsCursor(delta int) {
	t, ok := m.findingsTarget()
	if !ok || m.fx.cache[t.key] == nil {
		return
	}
	fs := m.currentFindings(m.fx.cache[t.key], t.img)
	if len(fs) == 0 {
		return
	}
	i := max(m.findingsCursor(fs), 0)
	i = clamp(i+delta, 0, len(fs)-1)
	m.fx.cursor, m.fx.cursorKey = i, fs[i].Key()
	m.fx.revealed = "" // a revealed value never outlives the cursor resting on it
}

// findingsCursor returns the index of the highlighted finding: the one last
// chosen if it is still listed, else the same position.
func (m *Model) findingsCursor(fs []forensics.Finding) int {
	if len(fs) == 0 {
		return -1
	}
	for i, f := range fs {
		if f.Key() == m.fx.cursorKey {
			m.fx.cursor = i
			return i
		}
	}
	m.fx.cursor = clamp(m.fx.cursor, 0, len(fs)-1)
	m.fx.cursorKey = fs[m.fx.cursor].Key()
	return m.fx.cursor
}

// ---- drawing ---------------------------------------------------------------------

func (m *Model) renderFindings(w, h int) []string {
	st := m.st
	t, ok := m.findingsTarget()
	if !ok {
		if m.detail.err != "" && m.detail.key == m.wantDetail {
			return wrapLine(st.tone(toneBad, "Inspect failed, so the image's platform is unknown: "+m.detail.err), w)
		}
		return []string{st.tone(toneDim, "Reading the image's platform…")}
	}
	if t.key != m.fx.shownKey {
		m.fx.shownKey, m.fx.cursor, m.fx.cursorKey, m.fx.off, m.fx.revealed = t.key, 0, "", 0, ""
	}
	rep := m.cached(t.key)
	if rep == nil {
		return m.renderFindingsPending(t, w)
	}
	fs := m.currentFindings(rep, t.img)
	cur := m.findingsCursor(fs)

	var out []string
	sum := fmt.Sprintf("%d findings · by %s · %d layers · %s · %s · %s", len(fs), m.fx.order, len(rep.Layers), humanBytes(rep.TotalSize()), rep.Layout, rep.Platform)
	switch n := len(rep.Notes); n {
	case 0:
	case 1:
		sum += " · 1 unknown"
	default:
		sum += fmt.Sprintf(" · %d unknowns", n)
	}
	out = append(out, st.tone(toneDim, fit(sum, w)))
	out = append(out, fit(m.liveLine(t.img), w))

	detailH := clamp(h/3+1, 3, 9)
	if len(fs) == 0 {
		detailH = 0
	}
	listH := max(h-len(out)-detailH, 1)
	if h >= 14 && detailH > 0 {
		listH-- // a rule between the list and the finding
	}
	m.fx.listH = listH
	if len(fs) == 0 {
		out = append(out, st.tone(toneGood, "Nothing to report."))
		for _, n := range rep.Notes {
			out = append(out, wrapLine(st.tone(toneDim, "unknown: "+n), w)...)
		}
		return out
	}
	m.fx.off = clamp(m.fx.off, max(cur-listH+1, 0), cur)
	focused := m.focus == PaneDetails
	for i := m.fx.off; i < len(fs) && i < m.fx.off+listH; i++ {
		out = append(out, m.findingRow(fs[i], w, i == cur, focused))
	}
	for len(out) < 2+listH {
		out = append(out, "")
	}
	if h >= 14 {
		pos := fmt.Sprintf(" %d of %d ", cur+1, len(fs))
		out = append(out, st.tone(toneDim, strings.Repeat("─", max(w-len([]rune(pos))-1, 0))+pos+"─"))
	}
	detail := m.findingDetail(fs[cur], w)
	if len(detail) > detailH {
		detail = detail[:detailH]
	}
	return append(out, detail...)
}

func (m *Model) renderFindingsPending(t findingsTarget, w int) []string {
	st := m.st
	var out []string
	add := func(tn tone, s string) { out = append(out, wrapLine(st.tone(tn, s), w)...) }
	switch run := m.fx.run; {
	case run != nil && run.key == t.key:
		p, _ := run.snapshot()
		add(toneAccent, fmt.Sprintf("%s Analysing %s (%s) from its own archive…", spinnerFrames[m.spinner], t.ref, t.plat))
		add(toneNormal, fmt.Sprintf("%s read · %d layers found", humanBytes(p.BytesRead), p.Layers))
		add(toneDim, "The archive streams from the daemon (GET /images/get) and is read once; nothing is written to disk. c cancels.")
	case m.fx.failed[t.key] != "":
		add(toneBad, "The analysis ended without a report: "+m.fx.failed[t.key])
		add(toneDim, "Enter analyses again.")
	case m.needsConsent(t):
		add(toneWarn, fmt.Sprintf("%s is %s, and the daemon is remote (%s).", t.ref, humanBytes(t.img.Size), m.endpoint.Host))
		add(toneNormal, "Analysing streams the whole archive across the network, once.")
		add(toneDim, "Enter analyses it. Nothing is requested until you do.")
	default:
		add(toneDim, "Starting the analysis…")
	}
	return out
}

func (m *Model) liveLine(img engine.Image) string {
	st := m.st
	users := forensics.Users(forensics.LiveInput{Image: img, Containers: m.snap.Containers})
	if len(users) == 0 {
		return st.tone(toneDim, "live: no container uses this image")
	}
	var parts []string
	for _, c := range users {
		p := st.tone(stateTone(c.State), stateGlyph(c.State)) + " " + c.Name + " " + st.tone(toneDim, c.State)
		if ports := formatPorts(c.Ports); ports != "" {
			p += " " + ports
		}
		if f, ok := m.fx.facts[c.ID]; ok && f.Privileged {
			p += " " + st.tone(toneBad, "privileged")
		}
		if c.NetworkMode == "host" {
			p += " " + st.tone(toneWarn, "host-net")
		}
		parts = append(parts, p)
	}
	return st.tone(toneDim, fmt.Sprintf("live: %d container(s): ", len(users))) + strings.Join(parts, st.tone(toneDim, " · "))
}

func certaintyTone(c forensics.Certainty) tone {
	switch c {
	case forensics.Certain:
		return toneNormal
	case forensics.Likely:
		return toneWarn
	}
	return toneDim
}

func (m *Model) findingRow(f forensics.Finding, w int, selected, focused bool) string {
	layer := "--"
	if f.Layer > 0 {
		layer = fmt.Sprintf("L%d", f.Layer)
	}
	parts := []string{fmt.Sprintf("%-3s %-7s %s", layer, f.Certainty, fit(f.Rule, 19))}
	if f.Path != "" {
		parts = append(parts, f.Path)
	}
	if f.Evidence != "" {
		parts = append(parts, f.Evidence) // a live finding's evidence starts with its container
	}
	text := strings.Join(parts, "  ")
	line := fit(text, w-2)
	switch {
	case selected && focused:
		return m.st.selected.Render("▸ " + line)
	case selected:
		return "▸ " + m.st.tone(certaintyTone(f.Certainty), line)
	}
	return "  " + m.st.tone(certaintyTone(f.Certainty), line)
}

func (m *Model) findingDetail(f forensics.Finding, w int) []string {
	st := m.st
	var out []string
	kv := func(k, v string, tn tone) {
		for i, part := range wrapLine(v, max(w-9, 10)) {
			label := "         "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 8)) + " "
			}
			out = append(out, label+st.tone(tn, part))
		}
	}
	kv("why", f.Reason, toneNormal)
	ev := f.Evidence
	if f.Secret.Present() {
		if m.fx.revealed == f.Key() {
			kv("value", f.Secret.Reveal(), toneBad)
		} else {
			ev += "   (v reveals)"
		}
	}
	kv("evidence", ev, toneNormal)
	src := string(f.Source) + " · " + f.Certainty.String()
	if f.Layer > 0 {
		src += fmt.Sprintf(" · layer %d", f.Layer)
	}
	kv("source", src, toneDim)
	if f.Instruction != "" {
		kv("by", f.Instruction, toneNormal)
	}
	if f.Unknown != "" {
		kv("unknown", f.Unknown, toneDim)
	}
	for i, d := range f.Detail {
		k := ""
		if i == 0 {
			k = "modules"
		}
		kv(k, d, toneDim)
	}
	return out
}
