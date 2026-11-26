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
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
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

	// The re-create gate's clone check, kept for one container and one
	// inspect of it: parsing the document on every frame would be waste.
	cloneKey     string
	cloneRefused []string
	cloneErr     string
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
		c.factsErr[msg.id] = escErr(msg.err)
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
	run := &changesRun{key: t.key, ref: esc(t.pair.Ref), oldKey: t.pair.OldID + "|" + t.plat, newKey: t.pair.NewID + "|" + t.plat}
	have := [2]*forensics.Report{m.fx.cache[run.oldKey], m.fx.cache[run.newKey]}
	eng := m.eng
	daemonOS, daemonArch := m.info.OSType, m.info.Architecture
	sides := [2]struct {
		name, id, ref string
	}{{"running", t.pair.OldID, engine.ShortID(t.pair.OldID)}, {"new", t.pair.NewID, t.pair.Ref}} // ref: what the archive is matched by, as the daemon gave it
	delete(m.chg.failed, t.key)
	run.jobID = m.startJob("compare images", run.ref, func(ctx context.Context, r *jobs.Run) error {
		for i, s := range sides {
			rep := have[i]
			if rep != nil {
				r.Logf("%s image %s: report already made, not read again", s.name, escID(s.id))
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
				r.Logf("%s image %s: %d layers, %s read, %d findings", s.name, escID(s.id), len(rep.Layers), humanBytes(rep.BytesRead), len(rep.Findings))
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

// onChangesKey handles Enter, c and A on the tab; it reports whether key
// was one, and the command A starts.
func (m *Model) onChangesKey(key string) (bool, tea.Cmd) {
	switch key {
	case "enter":
		t, ok := m.changesTarget()
		if !ok || !t.refusal.None() || t.old == nil || t.new == nil {
			return true, nil
		}
		if run := m.chg.run; run != nil {
			if run.key != t.key {
				m.setStatus(toneDim, "Another comparison is still ending.")
			}
			return true, nil
		}
		if _, _, done := m.contentFor(t); !done {
			m.startChanges(t)
		}
		return true, nil
	case "c":
		if run := m.chg.run; run != nil {
			m.jobs.Cancel(run.jobID)
			m.setStatus(toneWarn, "Cancelling: compare images "+run.ref)
		}
		return true, nil
	case "A":
		return true, m.recreateKey()
	}
	return false, nil
}

// ---- re-create (docs/DECISIONS.md, round nineteen) -----------------------------

// recreateGate is whether the tab offers A for its container and, either
// way, the sentence that says so.
type recreateGate struct {
	ok   bool
	note string
}

// recreateGate decides the offer from what the tab already holds: the
// verdict and both images' facts, the container's own inspect and the
// clone's refusals. It sends nothing.
func (m *Model) recreateGate(t changesTarget) recreateGate {
	name := escape.Name(t.name)
	switch t.refusal.Code {
	case "":
	case "same":
		return recreateGate{note: "The tag names the image " + name + " runs: there is nothing to re-create."}
	case "pinned", "from-id":
		return recreateGate{note: "This reference names no other build, so there is nothing to re-create " + name + " onto."}
	case "unresolved":
		return recreateGate{note: "The tag names no image here, so there is nothing to re-create " + name + " onto; this desk does not pull."}
	case "platform":
		return recreateGate{note: "The tag names a build for another platform, so this tab does not re-create " + name + "."}
	default:
		return recreateGate{note: "This tab does not re-create " + name + "."}
	}
	if t.old == nil || t.new == nil {
		return recreateGate{}
	}
	if orig, aside := plan.SetAsideName(t.container.Name, t.container.ID); aside {
		for _, c := range m.snap.Containers {
			if c.Name == orig && c.ID != t.container.ID {
				return recreateGate{note: fmt.Sprintf("%s was set aside by a re-create: its name %s now belongs to another container (%s), so this tab offers nothing.", name, escape.Name(orig), escID(c.ID))}
			}
		}
		return recreateGate{note: fmt.Sprintf("%s was set aside by a re-create; its name was %s. Rename it back before re-creating it.", name, escape.Name(orig))}
	}
	refused, errText := m.cloneRefusals()
	switch {
	case errText != "":
		return recreateGate{note: name + " cannot be re-created here: its configuration could not be read (" + errText + ")."}
	case len(refused) > 0:
		return recreateGate{note: name + " cannot be re-created here: it carries settings a re-create does not reproduce — " + strings.Join(refused, "; ") + "."}
	}
	return recreateGate{ok: true, note: fmt.Sprintf("A re-creates %s on that build (%s): it is stopped and renamed aside, and a new %s is created from %s with its configuration and started. Nothing is deleted.",
		name, escID(t.pair.NewID), name, esc(t.pair.Ref))}
}

// cloneRefusals is the clone check for the inspect the tab is drawn from.
func (m *Model) cloneRefusals() ([]string, string) {
	d := m.detail.container
	if d == nil {
		return nil, "not inspected yet"
	}
	def := plan.DefaultsFrom(m.info)
	key := fmt.Sprintf("%s|%d|%+v", d.ID, m.detail.seq, def)
	if m.chg.cloneKey != key {
		m.chg.cloneKey, m.chg.cloneRefused, m.chg.cloneErr = key, nil, ""
		refused, err := plan.CloneRefusals(*d, def)
		m.chg.cloneRefused = refused
		if err != nil {
			m.chg.cloneErr = err.Error() // the plan's own text: escaped there
		}
	}
	return m.chg.cloneRefused, m.chg.cloneErr
}

// recreateKey is A on the tab. Read-only mode and a selection refuse it
// before anything is read; a pair that cannot move is refused from what
// the tab knows. Otherwise the plan is built from fresh reads.
func (m *Model) recreateKey() tea.Cmd {
	if m.readOnly {
		m.setStatus(toneWarn, "Read-only mode: re-create is disabled. Nothing was sent to the daemon.")
		return nil
	}
	if n := len(m.markedRows(KindContainers)); n > 0 {
		m.setStatus(toneWarn, fmt.Sprintf("A re-create acts on one container at a time and %d are selected: Esc clears the selection. Nothing was sent.", n))
		return nil
	}
	if m.preparing {
		return nil
	}
	t, ok := m.changesTarget()
	if !ok {
		m.setStatus(toneDim, "The container's reference is still being read.")
		return nil
	}
	if !t.refusal.None() {
		m.setStatus(toneWarn, "Nothing to re-create: "+m.recreateGate(t).note)
		return nil
	}
	if t.old == nil || t.new == nil {
		m.setStatus(toneDim, "Both images are still being read.")
		return nil
	}
	return m.prepareRecreate(t.container.ID)
}

// prepareRecreate reads, as one command, every fact the confirmation
// states, rather than trusting what the tab cached minutes ago: the four
// lists, the container's inspect, the pair its reference makes now, both
// images and their platforms, and the running image's configuration (what
// the clone leaves to the new image). A pair that cannot move is
// NotApplicable; settings the clone refuses become the plan's blockers, so
// the dialog opens and names them. Nothing is sent that changes the daemon.
func (m *Model) prepareRecreate(containerID string) tea.Cmd {
	m.preparing = true
	m.planSeq++
	seq := m.planSeq
	eng, def := m.eng, plan.DefaultsFrom(m.info)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		msg := planMsg{seq: seq}
		msg.plan, msg.err = buildRecreate(ctx, eng, def, containerID)
		return msg
	}
}

func buildRecreate(ctx context.Context, eng engine.Engine, def plan.CloneDefaults, containerID string) (plan.Plan, error) {
	snap, err := plan.Take(ctx, eng)
	if err != nil {
		return plan.Plan{}, stateErr{err: err}
	}
	d, err := eng.InspectContainer(ctx, containerID)
	if engine.IsNotFound(err) {
		return plan.Plan{}, &plan.NotApplicable{Reason: "That container no longer exists."}
	}
	if err != nil {
		return plan.Plan{}, fmt.Errorf("could not inspect the container: %s", escErr(err))
	}
	pair := changes.FindPair(d.Image, d.ImageID, snap.Images)
	if !pair.Refusal.None() {
		return plan.Plan{}, &plan.NotApplicable{Reason: "Nothing to re-create: " + pair.Text}
	}
	read := func(id string) (engine.ImageDetail, error) {
		img, err := eng.InspectImage(ctx, id)
		if err != nil {
			return img, fmt.Errorf("could not inspect image %s: %s", escID(id), escErr(err))
		}
		return img, nil
	}
	old, err := read(pair.OldID)
	if err != nil {
		return plan.Plan{}, err
	}
	now, err := read(pair.NewID)
	if err != nil {
		return plan.Plan{}, err
	}
	facts := func(i engine.ImageDetail) engine.ImageFacts {
		return engine.ImageFacts{OS: i.OS, Architecture: i.Architecture, Variant: i.Variant}
	}
	if r := changes.SamePlatform(facts(old), facts(now)); !r.None() {
		return plan.Plan{}, &plan.NotApplicable{Reason: "Nothing to re-create: " + r.Text}
	}
	spec, refused, err := plan.Clone(d, old, def)
	if err != nil {
		return plan.Plan{}, err
	}
	return plan.ContainerRecreate(snap, d, spec, refused, plan.ImagePin{Ref: pair.Ref, ID: now.ID})
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
			hang(label, 9, toneDim, escID(id)+"  "+why)
			return
		}
		s := fmt.Sprintf("%s  %s old  %s", escID(f.ID), humanAge(f.Created, now), humanBytes(f.Size))
		hang(label, 9, toneNormal, s+extra)
	}

	ident("running", t.old, t.pair.OldID, "  ("+esc(t.name)+")")
	ref := esc(t.pair.Ref)
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
		hang("  "+sign, 4, tn, s) // the analysis's text: escaped there, once
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

// redeployNote says what restarting and re-creating do, and whether this
// tab re-creates the container — with which key, or why not.
func (m *Model) redeployNote(prose func(tone, string), t changesTarget) {
	g := m.recreateGate(t)
	text := "Restarting " + escape.Name(t.name) + " keeps the image it runs; only re-creating it takes the image the tag names now."
	if g.note != "" {
		text += " " + g.note
	}
	if g.ok && m.readOnly {
		text += " In read-only mode A sends nothing."
	}
	tn := toneDim
	if g.ok && !m.readOnly {
		tn = toneAccent
	}
	prose(tn, text)
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
