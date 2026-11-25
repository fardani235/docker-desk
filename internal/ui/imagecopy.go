package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/session"
)

// Copying an image to another daemon of the session, from Images (openspec
// change copy-image-between-daemons, D1–D11).
//
// C on the highlighted row captures the image — ID, reference, name — and
// opens the destination chooser: every other member of the session, the
// connected ones choosable, the others listed with the reason they are
// not. Nothing has been sent. Choosing a destination makes the dialog's
// reads (the source's lists and its inspect of the image, the target's
// inspect of the name the copy will carry) and opens the typed
// confirmation, which names both daemons, the image, the platform and what
// happens to the tag on the target. The typed word is the tag, or the short
// ID of an image that arrives untagged. Only then does one job start, on
// both daemons (jobs.StartOnPair): plan.ExecuteCopy streams the source's
// archive into the target's load and inspects the result.
//
// It is the one action that names a second daemon. The source is whatever
// daemon is focused when C is pressed; the target is identified by its
// session key, never by its position, and both engines are looked up from
// the session when the job starts. Read-only refuses it in invokeOn, before
// the chooser; --no-registry has nothing to refuse, because no registry is
// asked anything.

// copyImageAction is Images' C. mutating makes read-only refuse it;
// needsRow without setCapable refuses a selection as one-at-a-time.
var copyImageAction = action{key: "C", label: "copy to daemon…",
	desc:     "copy this image to another connected daemon: streamed directly, no file, no registry",
	needsRow: true, mutating: true, run: (*Model).openCopyImage}

// copyReadTimeout bounds the reads made for the copy's confirmation.
const copyReadTimeout = 10 * time.Second

// copyDaemon describes a member as a copy plan carries it.
func copyDaemon(mem *session.Member) plan.CopyDaemon {
	ep, link := mem.Endpoint(), mem.Link()
	return plan.CopyDaemon{Key: mem.Key(), Name: mem.Name(), Endpoint: ep.Host, Context: ep.Context,
		LinkLabel: link.Label, Insecure: link.Insecure, Remote: link.Remote}
}

// memberByKey finds a member of the session by its canonical endpoint.
func (m *Model) memberByKey(key string) *session.Member {
	if key == "" {
		return nil
	}
	for _, mem := range m.sess.Members() {
		if mem.Key() == key {
			return mem
		}
	}
	return nil
}

// usableTarget says why mem cannot receive a copy now, or "".
func usableTarget(mem *session.Member) string {
	switch st := mem.State(); {
	case st == session.Connected && mem.Engine() != nil:
		return ""
	case st == session.Connecting:
		return "still connecting"
	case st == session.Removing:
		return "being removed from the session"
	}
	if r := mem.Reason(); r != "" {
		return "not connected: " + r
	}
	return "not connected"
}

// openCopyImage is the C action: the image is captured now, and the
// destination chooser opens. Nothing is sent.
func (m *Model) openCopyImage(r row) tea.Cmd {
	if m.kind != KindImages || r.imageID == "" || m.cur == nil {
		return nil
	}
	var items []pickItem
	usable := 0
	for _, mem := range m.sess.Members() {
		if mem == m.cur {
			continue
		}
		ep := mem.Endpoint().Host
		it := pickItem{id: mem.Key(), label: escape.Name(mem.Name()) + "  " + escape.Name(ep), tone: toneNormal}
		it.more = []string{escape.Name(mem.Name()) + " — " + escape.Name(ep) + " (context " + escape.Name(mem.Endpoint().Context) + ")" + linkWords(mem)}
		if why := usableTarget(mem); why != "" {
			it.disabled, it.reason, it.detail, it.tone = true, escape.Name(why), "unusable", toneDim
			it.more = append(it.more, "cannot be chosen — "+it.reason)
		} else {
			usable++
			info := mem.Info()
			it.detail = "Docker " + escape.Name(info.ServerVersion)
			it.more = append(it.more, "engine "+escape.Name(info.Name)+", Docker "+escape.Name(info.ServerVersion)+", API "+escape.Name(info.APIVersion))
		}
		items = append(items, it)
	}
	if usable == 0 {
		why := "this session has only this daemon"
		if len(items) > 0 {
			why = "no other daemon of this session is connected"
		}
		m.setStatus(toneWarn, "Copying an image needs a second connected daemon: "+why+". "+keyDaemons+" opens the picker to add one. Nothing was sent.")
		return nil
	}
	name, short := escape.Name(r.name), engine.ShortID(r.imageID)
	imageID, imageRef, gen := r.imageID, r.imageRef, m.gen
	sel := 0
	for i, it := range items {
		if !it.disabled {
			sel = i
			break
		}
	}
	m.dlg = &dialog{kind: dlgPicker, openedAt: time.Now(),
		pickTitle: "Copy image to another daemon",
		pickNote:  "Copy " + name + " (" + short + ") from this daemon to:",
		pickVerb:  "read both daemons, then confirm",
		items:     items, pickSel: sel,
		onPick: func(m *Model, key string) tea.Cmd {
			if gen != m.gen {
				return nil // the focus moved: the row was another daemon's
			}
			return m.prepareCopyImage(imageID, imageRef, key)
		},
	}
	return nil
}

// linkWords is a member's "how is it reached" tag as plain text.
func linkWords(mem *session.Member) string {
	if l := mem.Link().Label; l != "" {
		return "  [" + escape.Name(l) + "]"
	}
	return ""
}

// prepareCopyImage makes the reads the confirmation is built from (D5): the
// source's lists and its inspect of the image, and the target's inspect of
// the name the copy will carry. Nothing that changes a daemon is sent.
func (m *Model) prepareCopyImage(imageID, imageRef, targetKey string) tea.Cmd {
	src := m.cur
	dst := m.memberByKey(targetKey)
	switch {
	case src == nil || m.eng == nil:
		return nil
	case dst == nil:
		m.setStatus(toneWarn, "That daemon is no longer in this session: nothing was sent.")
		return nil
	case dst == src:
		m.setStatus(toneWarn, "A copy needs two different daemons: nothing was sent.")
		return nil
	}
	if why := usableTarget(dst); why != "" {
		m.setStatus(toneWarn, escape.Name(dst.Name())+" cannot receive a copy: "+escape.Name(why)+". Nothing was sent.")
		return nil
	}
	// The name both daemons are asked about is validated before any
	// request carries it.
	by, err := plan.CopyName(imageID, imageRef)
	if err != nil {
		m.setStatus(toneWarn, err.Error())
		return nil
	}
	m.preparing = true
	m.planSeq++
	seq := m.planSeq
	srcEng, dstEng := m.eng, dst.Engine()
	source := plan.CopySource{Daemon: copyDaemon(src), ImageID: imageID, Ref: imageRef}
	dest := plan.CopyDestination{Daemon: copyDaemon(dst)}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), copyReadTimeout)
		defer cancel()
		snap, err := plan.Take(ctx, srcEng)
		if err != nil {
			return planMsg{seq: seq, err: fmt.Errorf("could not read the source daemon's current state: %s", escape.Name(plainError(err)))}
		}
		facts, err := srcEng.ImageFacts(ctx, imageID)
		switch {
		case err != nil && engine.IsNotFound(err):
			return planMsg{seq: seq, err: &plan.NotApplicable{Reason: "that image no longer exists on this daemon; nothing was sent"}}
		case err != nil:
			return planMsg{seq: seq, err: fmt.Errorf("could not inspect the image on the source daemon: %s. Nothing was sent.", escape.Name(plainError(err)))}
		}
		source.Facts = facts
		switch held, err := dstEng.ImageFacts(ctx, by); {
		case err == nil:
			dest.Held, dest.Facts = true, held
		case engine.IsNotFound(err):
			// The target has nothing under that name.
		default:
			dest.Err = plainError(err)
		}
		p, err := plan.CopyImage(snap, source, dest)
		return planMsg{seq: seq, plan: p, err: err}
	}
}

// copyProblem is why a copy plan cannot run now, though it could when its
// dialog was built: the session has changed under it. "" when it can.
func (m *Model) copyProblem(p plan.Plan) string {
	c := p.ImageCopy
	if c == nil {
		return ""
	}
	target := escape.Name(c.Target.Name)
	src, dst := m.memberByKey(c.Source.Key), m.memberByKey(c.Target.Key)
	switch {
	case m.readOnly:
		return "Read-only mode: a copy writes to the target daemon. Nothing was sent."
	case dst == nil:
		return "The target is gone: " + target + " (" + escape.Name(c.Target.Endpoint) + ") is no longer in this session. Nothing was sent."
	case src == nil || src != m.cur || m.eng == nil:
		return "The source is gone: " + escape.Name(c.Source.Name) + " is no longer the focused daemon of this session. Nothing was sent."
	case src == dst:
		return "A copy needs two different daemons. Nothing was sent."
	}
	if why := usableTarget(dst); why != "" {
		return "The target cannot receive a copy now: " + target + " is " + escape.Name(why) + ". Nothing was sent."
	}
	return ""
}

// livePlan is the plan a confirmation acts on at this moment: a copy whose
// target (or source) has left the session, or lost its daemon, is blocked,
// whatever its dialog said when it opened.
func (m *Model) livePlan(p plan.Plan) plan.Plan {
	if why := m.copyProblem(p); why != "" {
		p.Blockers = append(append([]string(nil), p.Blockers...), why)
	}
	return p
}

// runCopyImage starts a confirmed copy: both members are looked up in the
// session now, by key, and the one job is recorded against both daemons, so
// neither can be removed while it runs (D6, D8).
func (m *Model) runCopyImage(p plan.Plan) int {
	c := p.ImageCopy
	if why := m.copyProblem(p); why != "" {
		m.setStatus(toneBad, "Not started: "+p.JobTitle+" "+escape.Name(p.JobTarget)+" — "+why)
		return 0
	}
	src, dst := m.memberByKey(c.Source.Key), m.memberByKey(c.Target.Key)
	srcEng, dstEng := src.Engine(), dst.Engine()
	if srcEng == nil || dstEng == nil {
		m.setStatus(toneBad, "Not started: "+p.JobTitle+" — a daemon of the copy is not connected. Nothing was sent.")
		return 0
	}
	target := escape.Name(p.JobTarget)
	id, err := m.jobs.StartOnPair(dst.Daemon(), src.Daemon(), p.JobTitle, target, func(ctx context.Context, run *jobs.Run) error {
		return plan.ExecuteCopy(ctx, srcEng, dstEng, p, run)
	})
	if err != nil {
		reason := err.Error()
		if errors.Is(err, jobs.ErrDaemonClosing) {
			reason = "one of its daemons is being removed from the session"
		}
		m.setStatus(toneBad, "Not started: "+p.JobTitle+" "+target+" — "+reason+". Nothing was sent.")
		return id
	}
	m.jobSel = id
	m.setStatus(toneAccent, "started: "+p.JobTitle+" "+target+" to "+escape.Name(dst.Name())+" from "+escape.Name(src.Name()))
	return id
}

// copyIdent is the identity block of a copy's confirmation: the image, then
// the two daemons — each by name, endpoint, context and link — and the
// platform. It replaces the single "daemon" line every other plan has.
func (m *Model) copyIdent(p plan.Plan, w int) []string {
	c := p.ImageCopy
	st := m.st
	end := func(label string, d plan.CopyDaemon) []string {
		t := toneDim
		switch {
		case d.Insecure:
			t = toneBad
		case d.Remote:
			t = toneWarn
		}
		return m.endpointLines(w, endpointIdent{label: label, name: d.Name, host: d.Endpoint, context: d.Context, withContext: true,
			link: d.LinkLabel, linkTone: t})
	}
	lines := append(end("from", c.Source), end("to", c.Target)...)
	plat := escape.Name(c.Platform.String())
	if c.Platform.OS == "" || c.Platform.Architecture == "" {
		plat = "the daemon's own (it reported none)"
	}
	carries := "tag " + escape.Name(c.Ref)
	if c.Ref == "" {
		carries = "no tag: it arrives untagged"
	}
	for i, part := range wrapLine(plat+" · "+carries, max(w-11, 8)) {
		label := strings.Repeat(" ", 11)
		if i == 0 {
			label = st.tone(toneDim, fit("platform", 10)) + " "
		}
		lines = append(lines, label+part)
	}
	return lines
}
