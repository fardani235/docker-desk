package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/changes"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// The Changes tab of a container: what differs between the image it runs
// and the image its reference names now (docs/DECISIONS.md, C1–C12).
//
// Drawn at once from cheap reads: the reference comes from the inspect the
// Details pane has already made, and each image costs one inspect and one
// history — four requests at most, kept by image ID, which never changes
// meaning. Enter compares the contents as a job that reads both archives,
// as Findings does, and keeps the reports in Findings' own cache.

const tabChanges = "Changes"

// changesFactsCap bounds the image facts kept; each is a few kilobytes.
const changesFactsCap = 16

type changesState struct {
	facts    map[string]*engine.ImageFacts // by image ID
	factsLRU []string
	factsErr map[string]string // why the last read of an image failed
	loading  map[string]bool

	run    *changesRun
	failed map[string]string // by pair key: why the last comparison ended without one
	// content holds finished comparisons by pair key; the reports they
	// were made from are in Findings' cache.
	content map[string]*changes.Content

	shownFor string // container the scroll offset belongs to
	view     textView
	visible  bool
}

type changesFactsMsg struct {
	id    string
	facts engine.ImageFacts
	err   error
}

// changesRun is the hand-over point between the comparison job and the desk.
type changesRun struct {
	key, ref       string
	oldKey, newKey string // Findings cache keys
	jobID          int

	mu       sync.Mutex
	side     string // "running" or "new": which archive is being read
	progress forensics.Progress
	old, new *forensics.Report
}

func (r *changesRun) snapshot() (string, forensics.Progress, *forensics.Report, *forensics.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.side, r.progress, r.old, r.new
}

func (m *Model) changesVisible() bool {
	return m.kind == KindContainers && m.tabName() == tabChanges
}

// changesTarget is what the tab compares for the highlighted container.
type changesTarget struct {
	name      string
	container engine.Container
	pair      changes.Pair
	old, new  *engine.ImageFacts // nil until read
	refusal   changes.Refusal    // pair refusal, or platform once both are read
	key       string             // pair key, once there is a pair
	plat      string
}

// changesTarget works out the pair; ok is false until the container's own
// inspect has said which reference it was created from.
func (m *Model) changesTarget() (t changesTarget, ok bool) {
	r, has := m.currentRow()
	if !has || m.kind != KindContainers {
		return t, false
	}
	for _, c := range m.snap.Containers {
		if c.ID == r.containerID {
			t.container = c
		}
	}
	d := m.detail.container
	if t.container.ID == "" || d == nil || d.ID != t.container.ID {
		return t, false
	}
	t.name = t.container.Name
	t.pair = changes.FindPair(d.Image, t.container.ImageID, m.snap.Images)
	t.refusal = t.pair.Refusal
	t.old = m.chg.facts[t.pair.OldID]
	if t.pair.NewID != "" {
		t.new = m.chg.facts[t.pair.NewID]
		t.key = t.pair.OldID + "|" + t.pair.NewID
	}
	if t.old != nil && t.new != nil && t.refusal.None() {
		t.refusal = changes.SamePlatform(*t.old, *t.new)
		t.plat = t.old.Platform().String()
	}
	return t, true
}

// syncChanges runs on every frame once the highlight has settled: it
// collects a finished comparison and reads the facts the tab needs. It
// requests nothing while the tab is not on screen.
func (m *Model) syncChanges() tea.Cmd {
	m.collectChanges()
	vis := m.changesVisible()
	entered := vis && !m.chg.visible
	m.chg.visible = vis
	if !vis {
		return nil
	}
	if entered {
		m.chg.factsErr = nil // a failed read is tried again on the next visit
	}
	t, ok := m.changesTarget()
	if !ok {
		return nil
	}
	if run := m.chg.run; run != nil && run.key != t.key {
		// Moved on to another container: that comparison is not wanted.
		m.jobs.Cancel(run.jobID)
	}
	var cmds []tea.Cmd
	for _, id := range []string{t.pair.OldID, t.pair.NewID} {
		if id != "" && m.chg.facts[id] == nil && !m.chg.loading[id] && m.chg.factsErr[id] == "" {
			cmds = append(cmds, m.loadImageFacts(id))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) loadImageFacts(id string) tea.Cmd {
	if m.chg.loading == nil {
		m.chg.loading = map[string]bool{}
	}
	m.chg.loading[id] = true
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f, err := eng.ImageFacts(ctx, id)
		return changesFactsMsg{id: id, facts: f, err: err}
	}
}

func (m *Model) onChangesFacts(msg changesFactsMsg) {
	c := &m.chg
	delete(c.loading, msg.id)
	if msg.err != nil {
		if c.factsErr == nil {
			c.factsErr = map[string]string{}
		}
		c.factsErr[msg.id] = plainError(msg.err)
		if engine.IsNotFound(msg.err) {
			c.factsErr[msg.id] = "the image no longer exists"
		}
		return
	}
	if c.facts == nil {
		c.facts = map[string]*engine.ImageFacts{}
	}
	f := msg.facts
	if _, ok := c.facts[msg.id]; !ok {
		c.factsLRU = append(c.factsLRU, msg.id)
	}
	c.facts[msg.id] = &f
	for len(c.factsLRU) > changesFactsCap {
		delete(c.facts, c.factsLRU[0])
		c.factsLRU = c.factsLRU[1:]
	}
}

// collectChanges takes the result of a comparison that has ended.
func (m *Model) collectChanges() {
	run := m.chg.run
	if run == nil {
		return
	}
	j, ok := m.jobs.Get(run.jobID)
	if !ok || j.State == jobs.Running {
		return
	}
	m.chg.run = nil
	m.dirty = true
	_, _, old, new := run.snapshot()
	// A report read for one side is kept even when the other failed: the
	// next comparison, and Findings, start from it.
	if old != nil {
		m.store(run.oldKey, old)
	}
	if new != nil {
		m.store(run.newKey, new)
	}
	if m.chg.failed == nil {
		m.chg.failed = map[string]string{}
	}
	switch {
	case j.State == jobs.Succeeded && old != nil && new != nil:
		delete(m.chg.failed, run.key)
	case j.State == jobs.Cancelled:
		m.chg.failed[run.key] = "Cancelled."
	default:
		m.chg.failed[run.key] = j.Err
	}
}

// contentFor returns the finished comparison for t, computing it from the
// cached reports when both are there.
func (m *Model) contentFor(t changesTarget) (*changes.Content, changes.Refusal, bool) {
	old, new := m.fx.cache[t.pair.OldID+"|"+t.plat], m.fx.cache[t.pair.NewID+"|"+t.plat]
	if old == nil || new == nil {
		return nil, changes.Refusal{}, false
	}
	if r := changes.Unreadable(old, new); !r.None() {
		return nil, r, true
	}
	if c := m.chg.content[t.key]; c != nil {
		return c, changes.Refusal{}, true
	}
	c := changes.CompareContent(old, new)
	if m.chg.content == nil {
		m.chg.content = map[string]*changes.Content{}
	}
	if len(m.chg.content) >= findingsCacheSize {
		m.chg.content = map[string]*changes.Content{} // recomputed from the reports when wanted
	}
	m.chg.content[t.key] = &c
	return &c, changes.Refusal{}, true
}

// startChanges reads whichever of the two archives Findings has not
// already analysed, as one job. Like the analysis job, it records only
// counts, digests and the desk's own words.
func (m *Model) startChanges(t changesTarget) {
	plat := t.old.Platform()
	run := &changesRun{key: t.key, ref: oneLine(t.pair.Ref), oldKey: t.pair.OldID + "|" + t.plat, newKey: t.pair.NewID + "|" + t.plat}
	have := [2]*forensics.Report{m.fx.cache[run.oldKey], m.fx.cache[run.newKey]}
	eng := m.eng
	daemonOS, daemonArch := m.info.OSType, m.info.Architecture
	sides := [2]struct {
		name, id, ref string
	}{{"running", t.pair.OldID, engine.ShortID(t.pair.OldID)}, {"new", t.pair.NewID, run.ref}}
	delete(m.chg.failed, t.key)
	run.jobID = m.jobs.Start("compare images", run.ref, func(ctx context.Context, r *jobs.Run) error {
		for i, s := range sides {
			rep := have[i]
			if rep != nil {
				r.Logf("%s image %s: report already made, not read again", s.name, engine.ShortID(s.id))
			} else {
				run.mu.Lock()
				run.side, run.progress = s.name, forensics.Progress{}
				run.mu.Unlock()
				r.Status(fmt.Sprintf("asking the daemon for the %s image's archive", s.name))
				rc, err := eng.SaveImage(ctx, s.id, plat)
				if err != nil {
					return err
				}
				var last time.Time
				rep, err = forensics.Analyse(ctx, rc, forensics.Options{
					ImageID: s.id, Ref: s.ref, Platform: plat.String(),
					DaemonOS: daemonOS, DaemonArch: daemonArch,
					Limits: forensics.DefaultLimits(), Thresholds: forensics.DefaultThresholds(),
				}, func(p forensics.Progress) {
					run.mu.Lock()
					run.progress = p
					run.mu.Unlock()
					if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
						last = now
						r.Status(fmt.Sprintf("%s image: %s read · %d layers found", s.name, humanBytes(p.BytesRead), p.Layers))
					}
				})
				rc.Close()
				if err != nil {
					return err
				}
				r.Logf("%s image %s: %d layers, %s read, %d findings", s.name, engine.ShortID(s.id), len(rep.Layers), humanBytes(rep.BytesRead), len(rep.Findings))
			}
			run.mu.Lock()
			if i == 0 {
				run.old = rep
			} else {
				run.new = rep
			}
			run.mu.Unlock()
		}
		_, _, old, new := run.snapshot()
		if rf := changes.Unreadable(old, new); !rf.None() {
			r.Status("nothing to compare: a layer could not be read")
			return nil
		}
		c := changes.CompareContent(old, new)
		sum := fmt.Sprintf("packages +%d −%d ~%d · findings +%d −%d", len(c.Added), len(c.Removed), len(c.Changed), len(c.Appeared), len(c.Disappeared))
		r.Logf("%s", sum)
		r.Status(sum)
		return nil
	})
	m.chg.run = run
}

// ---- keys -------------------------------------------------------------------

// onChangesKey handles Enter and c on the tab; it reports whether key was one.
func (m *Model) onChangesKey(key string) bool {
	switch key {
	case "enter":
		t, ok := m.changesTarget()
		if !ok || !t.refusal.None() || t.old == nil || t.new == nil {
			return true
		}
		if run := m.chg.run; run != nil {
			if run.key != t.key {
				m.setStatus(toneDim, "Another comparison is still ending.")
			}
			return true
		}
		if _, _, done := m.contentFor(t); !done {
			m.startChanges(t)
		}
		return true
	case "c":
		if run := m.chg.run; run != nil {
			m.jobs.Cancel(run.jobID)
			m.setStatus(toneWarn, "Cancelling: compare images "+run.ref)
		}
		return true
	}
	return false
}

// ---- drawing ---------------------------------------------------------------------

func (m *Model) renderChanges(w, h int) []string {
	st := m.st
	if r, ok := m.currentRow(); ok && r.containerID != m.chg.shownFor {
		m.chg.shownFor, m.chg.view.off = r.containerID, 0
	}
	t, ok := m.changesTarget()
	if !ok {
		if m.detail.err != "" && m.detail.key == m.wantDetail {
			return wrapLine(st.tone(toneBad, "Inspect failed, so the reference this container was created from is unknown: "+m.detail.err), w)
		}
		return []string{st.tone(toneDim, "Reading the container's reference…")}
	}
	lines := m.changesLines(t, w)
	bodyH := h
	if len(lines) > h {
		bodyH = h - 1
	}
	m.chg.view.set(lines)
	out := append([]string{}, m.chg.view.render(w, bodyH)...)
	if pos := m.chg.view.position(bodyH); pos != "" {
		for len(out) < bodyH {
			out = append(out, "")
		}
		out = append(out, st.tone(toneDim, fit("↑↓ scroll · "+pos, w)))
	}
	return out
}

// changesLines is the whole tab as lines w cells wide, for scrolling.
func (m *Model) changesLines(t changesTarget, w int) []string {
	st := m.st
	var out []string
	// hang wraps text under a label of width lw, continuing indented.
	hang := func(label string, lw int, tn tone, text string) {
		pad := strings.Repeat(" ", lw)
		for i, part := range wrapSpaces(text, max(w-lw, 10)) {
			l := pad
			if i == 0 {
				l = st.tone(toneDim, fit(label, lw))
			}
			out = append(out, l+st.tone(tn, part))
		}
	}
	prose := func(tn tone, s string) {
		for _, part := range wrapSpaces(s, w) {
			out = append(out, st.tone(tn, part))
		}
	}
	now := time.Now()
	ident := func(label string, f *engine.ImageFacts, id, extra string) {
		if f == nil {
			why := "reading…"
			if e := m.chg.factsErr[id]; e != "" {
				why = "unknown: " + e + " (opening the tab again retries)"
			}
			hang(label, 9, toneDim, engine.ShortID(id)+"  "+why)
			return
		}
		s := fmt.Sprintf("%s  %s old  %s", engine.ShortID(f.ID), humanAge(f.Created, now), humanBytes(f.Size))
		hang(label, 9, toneNormal, s+extra)
	}

	ident("running", t.old, t.pair.OldID, "  ("+t.name+")")
	ref := oneLine(t.pair.Ref)
	if t.pair.NewID == "" {
		hang("from", 9, toneNormal, ref)
	} else {
		extra := ""
		if t.old != nil && t.new != nil {
			extra = "  Δ " + signedBytes(t.new.Size-t.old.Size)
		}
		ident("tag now", t.new, t.pair.NewID, extra+"  "+ref)
	}
	if !t.refusal.None() {
		out = append(out, "")
		prose(toneWarn, "Nothing to compare. "+t.refusal.Text)
		out = append(out, "")
		m.redeployNote(prose, t)
		return out
	}
	if t.old == nil || t.new == nil {
		out = append(out, "")
		prose(toneDim, "Reading both images (inspect and history, two requests each)…")
		return out
	}

	// Layers.
	out = append(out, "")
	l := changes.CompareLayers(*t.old, *t.new)
	switch {
	case l.BaseIntact():
		hang("layers", 9, toneGood, fmt.Sprintf("base intact: all %d layers shared, %d added", l.Shared, len(l.NewOnly)))
	case l.Shared == 0:
		hang("layers", 9, toneWarn, fmt.Sprintf("no layer in common: %d running, %d new", len(t.old.DiffIDs), len(t.new.DiffIDs)))
	default:
		hang("layers", 9, toneWarn, fmt.Sprintf("diverge after layer %d: %d shared, %d replaced by %d", l.Shared, l.Shared, len(l.OldOnly), len(l.NewOnly)))
	}
	layer := func(sign string, tn tone, ln changes.LayerLine) {
		size := "?"
		if ln.Size >= 0 {
			size = humanBytes(ln.Size)
		}
		instr := ln.Instruction
		if instr == "" {
			instr = "(instruction unknown)"
		}
		hang(fmt.Sprintf("  %s L%-2d %7s ", sign, ln.Index, size), 16, tn, instr)
	}
	for _, ln := range l.OldOnly {
		layer("−", toneBad, ln)
	}
	for _, ln := range l.NewOnly {
		layer("+", toneGood, ln)
	}
	if l.OldUnmapped != "" {
		hang("", 9, toneDim, "unknown: running image's instructions: "+l.OldUnmapped)
	}
	if l.NewUnmapped != "" {
		hang("", 9, toneDim, "unknown: new image's instructions: "+l.NewUnmapped)
	}

	// Config.
	out = append(out, "")
	cfg := changes.CompareConfig(*t.old, *t.new)
	if len(cfg) == 0 {
		hang("config", 9, toneGood, "no difference in command, entrypoint, user, working directory, ports, health check, labels or variable names")
	} else {
		hang("config", 9, toneNormal, fmt.Sprintf("%d differences", len(cfg)))
	}
	for _, c := range cfg {
		label := "  " + c.Field
		var text string
		tn := toneNormal
		switch c.Op {
		case "added":
			tn = toneGood
			text = "+ " + strings.TrimSpace(c.Name+" "+c.New)
		case "removed":
			tn = toneBad
			text = "− " + strings.TrimSpace(c.Name+" "+c.Old)
		default:
			text = strings.TrimSpace(c.Name+" "+c.Old) + " → " + c.New
		}
		hang(label, 14, tn, text)
	}
	hang("", 9, toneDim, "Variables are compared by name; their values are never read, so one whose value changed is not shown.")

	// Contents.
	out = append(out, "")
	m.contentLines(t, hang, prose)
	out = append(out, "")
	m.redeployNote(prose, t)
	return out
}

func (m *Model) contentLines(t changesTarget, hang func(string, int, tone, string), prose func(tone, string)) {
	if run := m.chg.run; run != nil && run.key == t.key {
		side, p, _, _ := run.snapshot()
		hang("contents", 9, toneAccent, fmt.Sprintf("%s comparing: reading the %s image's archive · %s read · %d layers", spinnerFrames[m.spinner], side, humanBytes(p.BytesRead), p.Layers))
		hang("", 9, toneDim, "c cancels.")
		return
	}
	c, refusal, done := m.contentFor(t)
	switch {
	case !done && m.chg.failed[t.key] != "":
		hang("contents", 9, toneBad, "the comparison ended without a result: "+m.chg.failed[t.key])
		hang("", 9, toneDim, "Enter compares again.")
		return
	case !done:
		cached := 0
		if m.fx.cache[t.pair.OldID+"|"+t.plat] != nil {
			cached++
		}
		if m.fx.cache[t.pair.NewID+"|"+t.plat] != nil {
			cached++
		}
		what := fmt.Sprintf("Enter compares packages and findings: reads %d archive(s) from the daemon (GET /images/get), once, writing nothing to disk.", 2-cached)
		if cached > 0 {
			what += fmt.Sprintf(" %d already analysed.", cached)
		}
		hang("contents", 9, toneDim, what)
		return
	case !refusal.None():
		hang("contents", 9, toneWarn, "nothing to compare. "+refusal.Text)
		return
	}
	compared := "no package database on either side"
	if len(c.Compared) > 0 {
		compared = strings.Join(c.Compared, ", ")
	}
	hang("packages", 9, toneNormal, fmt.Sprintf("%d added, %d removed, %d version changed (%s)", len(c.Added), len(c.Removed), len(c.Changed), compared))
	pkg := func(sign string, tn tone, name, arch, ver string, layer int) {
		s := name
		if arch != "" && arch != "amd64" && arch != "arm64" {
			s += ":" + arch
		}
		s += "  " + ver
		if layer > 0 {
			s += fmt.Sprintf("  (L%d)", layer)
		}
		hang("  "+sign, 4, tn, s)
	}
	for _, p := range c.Added {
		pkg("+", toneGood, p.Name, p.Arch, p.Version, p.Layer)
	}
	for _, p := range c.Removed {
		pkg("−", toneBad, p.Name, p.Arch, p.Version, 0)
	}
	for _, p := range c.Changed {
		pkg("~", toneNormal, p.Name, p.Arch, p.Old+" → "+p.New, 0)
	}
	hang("findings", 9, toneNormal, fmt.Sprintf("%d appeared, %d disappeared (from the archives; live findings are on the image's own tab)", len(c.Appeared), len(c.Disappeared)))
	finding := func(sign string, tn tone, f forensics.Finding) {
		s := f.Rule
		if f.Layer > 0 {
			s = fmt.Sprintf("L%d %s", f.Layer, s)
		}
		if f.Path != "" {
			s += "  " + f.Path
		}
		if f.Evidence != "" {
			s += "  " + f.Evidence // masked by the analyser, as on Findings
		}
		hang("  "+sign, 4, tn, oneLine(s))
	}
	for _, f := range c.Appeared {
		finding("+", toneWarn, f)
	}
	for _, f := range c.Disappeared {
		finding("−", toneGood, f)
	}
	for _, u := range c.Unknowns {
		hang("", 9, toneDim, "unknown: "+u)
	}
}

// redeployNote says what restarting and re-creating do, once per view.
func (m *Model) redeployNote(prose func(tone, string), t changesTarget) {
	prose(toneDim, "Restarting "+t.name+" keeps the image it runs; only re-creating it takes the image the tag names now — which this desk does not do.")
}

func signedBytes(n int64) string {
	switch {
	case n > 0:
		return "+" + humanBytes(n)
	case n < 0:
		return "−" + humanBytes(-n)
	}
	return "0 B"
}

// wrapSpaces breaks text at spaces only — instructions and argument lists
// keep their hyphens and commas — and hard-breaks a word longer than w.
func wrapSpaces(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		for ansi.StringWidth(word) > w {
			if line != "" {
				out, line = append(out, line), ""
			}
			head := ansi.Truncate(word, w, "")
			out = append(out, head)
			word = word[len(head):]
		}
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
			line += " " + word
		default:
			out, line = append(out, line), word
		}
	}
	if line != "" || len(out) == 0 {
		out = append(out, line)
	}
	return out
}
