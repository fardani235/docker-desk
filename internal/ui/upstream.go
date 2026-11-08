package ui

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/registry"
)

// The Upstream tab of an image: does the tag it carries name a different
// build upstream today (docs/DECISIONS.md, round eleven, U1–U12).
//
// Opening the tab asks the registry for the highlighted reference as a job,
// once; the answer is kept for the session with the time it was obtained,
// and r asks again. U checks every listed image as one sequential job. A
// plain list refresh asks nothing, and nothing polls.

const tabUpstream = "Upstream"

type upstreamState struct {
	checker *freshness.Checker
	run     *upstreamRun
	batch   *upstreamBatch
	// failed says why a check of a key ended without an answer
	// (cancelled); the tab waits for r instead of asking again.
	failed map[string]string
	// described is what the tab says before asking, per reference: read
	// from the Docker configuration when a check starts, not every frame.
	described map[string]freshness.Target
}

type upstreamRun struct {
	key, ref string
	jobID    int
}

type upstreamBatch struct {
	jobID int
	// done counts answers; a change redraws the list column.
	done, seen atomic.Int64
}

// upstreamTarget is the highlighted image and reference.
type upstreamTarget struct {
	key, imageID, ref string
}

func (m *Model) newUpstream() {
	// One client for the tab and the repository browser: one token cache,
	// one --no-registry.
	client := registry.New(registry.Options{Disabled: m.opts.NoRegistry})
	m.up.checker = m.opts.Upstream
	if m.up.checker == nil {
		m.up.checker = freshness.New(freshness.Options{
			Daemon:    m.eng,
			DaemonID:  m.endpoint.Host,
			ConfigDir: m.opts.DockerConfigDir,
			Client:    client,
		})
	}
	m.newRepository(client)
	m.up.failed = map[string]string{}
	m.up.described = map[string]freshness.Target{}
}

func (m *Model) describeUpstream(ref string, fresh bool) freshness.Target {
	tg, ok := m.up.described[ref]
	if !ok || fresh {
		tg = m.up.checker.Describe(ref)
		m.up.described[ref] = tg
	}
	return tg
}

func (m *Model) upstreamVisible() bool {
	return m.kind == KindImages && m.tabName() == tabUpstream
}

func (m *Model) upstreamTarget() (upstreamTarget, bool) {
	r, ok := m.currentRow()
	if !ok || m.kind != KindImages || r.imageID == "" {
		return upstreamTarget{}, false
	}
	return upstreamTarget{key: r.imageID + "|" + r.imageRef, imageID: r.imageID, ref: r.imageRef}, true
}

// syncUpstream collects a finished check and starts the one an opened tab
// needs. Nothing is asked while the tab is not on screen.
func (m *Model) syncUpstream() {
	m.collectUpstream()
	if !m.upstreamVisible() || m.up.checker.Disabled() {
		return
	}
	t, ok := m.upstreamTarget()
	if !ok || t.ref == "" {
		return
	}
	if _, kept := m.up.checker.Cached(t.imageID, t.ref); kept || m.up.failed[t.key] != "" {
		return
	}
	if run := m.up.run; run != nil {
		if run.key == t.key {
			return
		}
		// The operator has moved on: that answer is no longer wanted.
		m.jobs.Cancel(run.jobID)
		m.up.run = nil
	}
	if m.up.batch != nil {
		return // the batch will reach it
	}
	m.startUpstream(t, false)
}

func (m *Model) collectUpstream() {
	if b := m.up.batch; b != nil {
		if n := b.done.Load(); n != b.seen.Load() {
			b.seen.Store(n)
			m.dirty = true
		}
		if j, ok := m.jobs.Get(b.jobID); !ok || j.State.Finished() {
			m.up.batch = nil
			m.dirty = true
		}
	}
	run := m.up.run
	if run == nil {
		return
	}
	j, ok := m.jobs.Get(run.jobID)
	if ok && j.State == jobs.Running {
		return
	}
	m.up.run = nil
	m.dirty = true
	if ok && j.State == jobs.Cancelled {
		m.up.failed[run.key] = "Cancelled."
	}
}

// startUpstream runs one check as a job. The job's lines carry the
// reference, the host and the verdict's words — never a credential.
func (m *Model) startUpstream(t upstreamTarget, force bool) {
	delete(m.up.failed, t.key)
	ck := m.up.checker
	id, ref := t.imageID, t.ref
	tg := m.describeUpstream(ref, true)
	run := &upstreamRun{key: t.key, ref: ref}
	run.jobID = m.jobs.Start("check upstream", ref, func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking " + orDash(tg.Host))
		v, err := ck.Check(ctx, id, ref, freshness.CheckOptions{Force: force})
		if err != nil {
			return err
		}
		for _, s := range v.Sentences() {
			r.Logf("%s", s)
		}
		r.Status(v.State.Word() + sourceWords(v))
		return nil
	})
	m.up.run = run
}

func sourceWords(v freshness.Verdict) string {
	if v.Source == freshness.FromDaemon {
		return " (via the daemon)"
	}
	return ""
}

// askUpstreamAgain is r on the tab: the kept answer is replaced.
func (m *Model) askUpstreamAgain() {
	t, ok := m.upstreamTarget()
	if !ok || t.ref == "" || m.up.checker.Disabled() {
		return
	}
	if run := m.up.run; run != nil {
		m.jobs.Cancel(run.jobID)
		m.up.run = nil
	}
	m.startUpstream(t, true)
}

// startUpstreamBatch is U: every listed image, one at a time, as one job.
func (m *Model) startUpstreamBatch() {
	if m.up.checker.Disabled() {
		m.setStatus(toneWarn, "Registry access is switched off (--no-registry): nothing was asked.")
		return
	}
	if m.up.batch != nil {
		m.setStatus(toneDim, "An upstream check of every image is already running; c on the Upstream tab cancels it.")
		return
	}
	m.ensureRows(KindImages)
	var items []freshness.Item
	seen := map[string]bool{}
	for _, r := range m.rows[KindImages] {
		k := r.imageID + "|" + r.imageRef
		if r.imageRef == "" || seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, freshness.Item{ImageID: r.imageID, Ref: r.imageRef})
	}
	if len(items) == 0 {
		m.setStatus(toneDim, "No tagged image to check.")
		return
	}
	if run := m.up.run; run != nil {
		m.jobs.Cancel(run.jobID)
		m.up.run = nil
	}
	ck := m.up.checker
	b := &upstreamBatch{}
	b.jobID = m.jobs.Start("check upstream", fmt.Sprintf("%d images", len(items)), func(ctx context.Context, r *jobs.Run) error {
		res := ck.Batch(ctx, items, func(done int, it freshness.Item, v freshness.Verdict) {
			b.done.Add(1)
			r.Steps(done, len(items))
			r.Logf("%s  %s%s", it.Ref, v.State.Word(), sourceWords(v))
			r.Status(fmt.Sprintf("%d of %d · %s %s", done, len(items), it.Ref, v.State.Word()))
		})
		switch {
		case res.Cancelled:
			return context.Canceled
		case res.Quota:
			msg := "stopped: a registry refused for quota"
			if !res.Reset.IsZero() {
				msg += "; it resets at " + res.Reset.Local().Format("15:04")
			}
			r.Logf("%s", msg)
			return errors.New(msg)
		}
		r.Status(fmt.Sprintf("%d checked · %d not applicable", res.Checked, res.Skipped))
		return nil
	})
	m.up.batch = b
	m.setStatus(toneDim, fmt.Sprintf("Checking %d references upstream, one at a time (a job; c on the Upstream tab cancels).", len(items)))
}

func (m *Model) cancelUpstream() {
	if run := m.up.run; run != nil {
		m.jobs.Cancel(run.jobID)
		m.setStatus(toneWarn, "Cancelling: check upstream "+run.ref)
	}
	if b := m.up.batch; b != nil {
		m.jobs.Cancel(b.jobID)
		m.setStatus(toneWarn, "Cancelling the upstream check of every image; answers already obtained are kept.")
	}
}

// onUpstreamKey handles the tab's own keys.
func (m *Model) onUpstreamKey(key string) bool {
	switch key {
	case "c":
		m.cancelUpstream()
		return true
	case "enter":
		if t, ok := m.upstreamTarget(); ok && m.up.failed[t.key] != "" {
			m.askUpstreamAgain()
		}
		return true
	}
	return false
}

// upstreamCell is the list's UPSTREAM column: one word, and +4d only when
// both build times were obtained. Blank until a check has answered.
func (m *Model) upstreamCell(r row) string {
	if r.imageRef == "" || m.up.checker == nil {
		return ""
	}
	v, ok := m.up.checker.Cached(r.imageID, r.imageRef)
	if !ok {
		return ""
	}
	if d := v.Delta(); d != "" {
		return v.State.Word() + " " + d
	}
	return v.State.Word()
}

// ---- drawing ---------------------------------------------------------------

func (m *Model) renderUpstream(w, _ int) []string {
	st := m.st
	var out []string
	add := func(tn tone, s string) { out = append(out, wrapLine(st.tone(tn, s), w)...) }
	t, ok := m.upstreamTarget()
	if !ok {
		return []string{st.tone(toneDim, "No image highlighted.")}
	}
	if t.ref == "" {
		add(toneDim, "Not applicable: the image is untagged, so there is no tag to follow.")
		return out
	}
	tg := m.describeUpstream(t.ref, false)
	if tg.NotApplicable != "" {
		add(toneDim, "Not applicable: "+tg.NotApplicable)
		return out
	}
	who := "anonymously"
	if tg.Credential != "" {
		who = "with the credential from " + tg.Credential
	}
	add(toneDim, fmt.Sprintf("%s · asks %s %s", tg.Ref, tg.Host, who))
	if m.up.checker.Disabled() {
		add(toneWarn, "Registry access is switched off (--no-registry): nothing is asked, by the desk or through the daemon.")
		return out
	}

	v, kept := m.up.checker.Cached(t.imageID, t.ref)
	running := m.up.run != nil && m.up.run.key == t.key
	switch {
	case running:
		add(toneAccent, fmt.Sprintf("%s Asking %s what %s names now… c cancels.", spinnerFrames[m.spinner], tg.Host, tg.Ref))
		if !kept {
			return out
		}
	case !kept && m.up.failed[t.key] != "":
		add(toneWarn, "The check ended without an answer: "+m.up.failed[t.key])
		add(toneDim, "r or Enter asks again.")
		return out
	case !kept && m.up.batch != nil:
		add(toneDim, "The check of every image (U) will reach this one; c cancels it.")
		return out
	case !kept:
		add(toneDim, "Asking…")
		return out
	}

	out = append(out, "")
	for i, s := range v.Sentences() {
		tn := toneNormal
		if i == 0 {
			tn = verdictTone(v.State)
		}
		add(tn, s)
	}
	out = append(out, "")
	if v.LocalDigest != "" {
		add(toneDim, "local digest     "+v.LocalDigest)
	}
	if v.RemoteDigest != "" {
		add(toneDim, "upstream digest  "+v.RemoteDigest)
	}
	src := "the registry, asked from this machine"
	if v.Source == freshness.FromDaemon {
		src = "the daemon (" + m.endpoint.Host + "), because the desk could not ask"
	}
	add(toneDim, "answered by "+src)
	add(toneDim, fmt.Sprintf("%s ago · r asks again", humanAge(v.At, time.Now())))
	if p, ok := m.upstreamPinned(); ok && !m.readOnly {
		if ref, err := plan.PinnedRef(p); err == nil {
			add(toneDim, "P pulls the build the tag names now, by its digest: "+ref)
		}
	}
	return out
}

// upstreamPinned is the build a moved verdict names upstream, as a pinned
// pull: the tag, and the digest the check read. ok only while the Upstream
// tab shows a moved verdict for the highlighted image.
func (m *Model) upstreamPinned() (plan.Pinned, bool) {
	if !m.upstreamVisible() {
		return plan.Pinned{}, false
	}
	t, ok := m.upstreamTarget()
	if !ok || t.ref == "" {
		return plan.Pinned{}, false
	}
	v, kept := m.up.checker.Cached(t.imageID, t.ref)
	if !kept || v.State != freshness.Moved || v.RemoteDigest == "" {
		return plan.Pinned{}, false
	}
	r, err := registry.ParseRef(t.ref)
	if err != nil {
		return plan.Pinned{}, false
	}
	return plan.Pinned{Name: r.Name, Tag: r.Tag, Digest: v.RemoteDigest}, true
}

func verdictTone(s freshness.State) tone {
	switch s {
	case freshness.Current:
		return toneGood
	case freshness.Moved:
		return toneAccent
	case freshness.Gone, freshness.Unknown:
		return toneWarn
	}
	return toneDim
}
