package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/session"
)

// Images' C: copy to another daemon of the session (openspec change
// copy-image-between-daemons, tasks 3.2 and 4.1–4.5).

const copyRef = "host.test:5000/acme/api:3.28.3"

// copyImageFixture is the image the copy tests move: the fake describes it
// the same way everywhere (archive, inspect, layers), and one layer is
// large enough to cross in many pieces.
func copyImageFixture(ref string) fixture.Image {
	f := fixture.ChangesReport(ref, true)
	f.Architecture, f.Variant = "arm64", "v8"
	if ref == "" {
		f.RepoTags = nil
	}
	body := make([]byte, 1<<20)
	for i := range body {
		body[i] = byte(i * 7)
	}
	f.Layers = append(f.Layers, fixture.Layer{CreatedBy: "COPY big /big", Files: []fixture.File{{Path: "big", Body: body}}})
	return f
}

// copySeed is the source daemon: the image under copyRef, an untagged
// build of the same fixture family, and a bystander.
func copySeed(s *fakeengine.Server) {
	s.SetImageFixture(s.AddImage(0, time.Hour, copyRef), copyImageFixture(copyRef))
	un := fixture.ChangesReport("", false)
	un.RepoTags = nil
	s.SetImageFixture(s.AddImage(0, 2*time.Hour), un)
	s.AddImage(1000, 3*time.Hour, "bystander:1")
}

type copyBench struct {
	*multiBench
	beta    *fakeengine.Server
	target  *session.Member
	imageID string
}

// newCopyBench is a desk focused on the source daemon, Images shown, with a
// second connected daemon "beta".
func newCopyBench(t *testing.T, o benchOpts, seedBeta func(*fakeengine.Server)) *copyBench {
	t.Helper()
	if o.seed == nil {
		o.seed = copySeed
	}
	b := &copyBench{multiBench: newMultiBench(t, o)}
	b.beta = b.daemon("beta", func(s *fakeengine.Server) {
		s.AddImage(1000, time.Hour, "resident:1")
		if seedBeta != nil {
			seedBeta(s)
		}
	})
	b.target = b.add("beta", b.beta)
	b.kind(KindImages)
	b.m.focus = PaneList
	for _, img := range b.m.snap.Images {
		for _, r := range img.Refs() {
			if r == copyRef {
				b.imageID = img.ID
			}
		}
	}
	return b
}

// chooser highlights name, presses C and waits for the destination chooser.
func (b *copyBench) chooser(name string) *dialog {
	b.t.Helper()
	b.selectRow(name)
	b.m.focus = PaneList
	b.keys("C")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker {
		b.t.Fatalf("no destination chooser:\n%s", b.screen())
	}
	return b.m.dlg
}

// confirmation chooses the first usable destination and waits for the
// typed confirmation.
func (b *copyBench) confirmation(name string) *dialog {
	b.t.Helper()
	b.chooser(name)
	b.keys("enter")
	b.until("the copy confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	return b.m.dlg
}

// dialogText is the open dialog's text with its wrapping undone: what lies
// between the box's borders on every line, joined by single spaces.
func dialogText(b *bench) string {
	b.m.dirty = true
	var parts []string
	for _, line := range strings.Split(b.screen(), "\n") {
		last := strings.LastIndex(line, "┃")
		if last <= 0 {
			continue
		}
		first := strings.LastIndex(line[:last], "┃")
		if first < 0 {
			continue
		}
		parts = append(parts, strings.Fields(line[first+len("┃"):last])...)
	}
	return strings.Join(parts, " ")
}

// jobsText is the Jobs pane's rows at a width that cuts nothing off.
func jobsText(b *bench) string {
	return ansi.Strip(strings.Join(b.m.renderJobs(240, 12, false), "\n"))
}

func (b *copyBench) loads() int { return b.beta.CountRequests(http.MethodPost, `^/images/load$`) }

func (b *copyBench) exports() int { return b.srv.CountRequests(http.MethodGet, `^/images/get$`) }

// copyJob returns the copy job once it has ended.
func (b *copyBench) copyJob() jobs.Job {
	b.t.Helper()
	var out jobs.Job
	b.until("the copy job to end", func() bool {
		for _, j := range b.m.jobs.List() {
			if j.Title == "copy image" && j.State.Finished() {
				out, _ = b.m.jobs.Get(j.ID)
				return true
			}
		}
		return false
	})
	return out
}

// ---- 4.1 the action and its refusals ------------------------------------------------

func TestCopyNeedsASecondConnectedDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: copySeed})
	b.kind(KindImages)
	b.m.focus = PaneList
	b.selectRow(copyRef)
	b.settle()
	b.srv.ResetRequests()
	b.keys("C")
	if b.m.dlg != nil {
		t.Fatalf("a dialog opened with one daemon:\n%s", b.screen())
	}
	st := b.m.status.text
	if !strings.Contains(st, "needs a second connected daemon") || !strings.Contains(st, keyDaemons+" opens the picker") || !strings.Contains(st, "Nothing was sent") {
		t.Fatalf("status %q", st)
	}
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("requests were sent: %v", b.srv.Requests())
	}

	// A second member that is not connected does not make a destination.
	beta := b.daemon("beta", nil)
	mem := b.add("beta", beta)
	beta.Stop()
	b.until("beta to be seen down", func() bool { return mem.State() != session.Connected })
	b.keys("C")
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "no other daemon of this session is connected") {
		t.Fatalf("status %q, dialog %v", b.m.status.text, b.m.dlg != nil)
	}
}

func TestCopyRefusesASelectionAndReadOnly(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.selectRow(copyRef)
	b.keys(" ")
	b.srv.ResetRequests()
	b.beta.ResetRequests()
	b.keys("C")
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "works on one object at a time") || !strings.Contains(b.m.status.text, "Nothing was sent") {
		t.Fatalf("a selection: status %q, dialog %v", b.m.status.text, b.m.dlg != nil)
	}
	if len(b.beta.Requests()) != 0 || b.exports() != 0 {
		t.Fatalf("a selection sent requests: %v", b.beta.Requests())
	}

	// Read-only: refused before the chooser, and nothing reaches either
	// daemon — from the key and from the action menu alike.
	ro := newCopyBench(t, benchOpts{readOnly: true}, nil)
	ro.selectRow(copyRef)
	ro.settle()
	ro.srv.ResetRequests()
	ro.beta.ResetRequests()
	for _, keys := range [][]string{{"C"}, {"enter", "C"}} {
		ro.keys(keys...)
		if ro.m.dlg != nil && ro.m.dlg.kind != dlgMenu {
			t.Fatalf("read-only opened a dialog:\n%s", ro.screen())
		}
		if !strings.Contains(ro.m.status.text, "Read-only mode") || !strings.Contains(ro.m.status.text, "Nothing was sent") {
			t.Fatalf("read-only status %q", ro.m.status.text)
		}
		ro.keys("esc")
	}
	if len(ro.srv.Requests()) != 0 || len(ro.beta.Requests()) != 0 {
		t.Fatalf("read-only sent %v / %v", ro.srv.Requests(), ro.beta.Requests())
	}
}

func TestCopyIsAvailableUnderNoRegistry(t *testing.T) {
	b := newCopyBench(t, benchOpts{noRegistry: true}, nil)
	d := b.confirmation(copyRef)
	if !d.plan.Runnable() || d.plan.ImageCopy == nil {
		t.Fatalf("--no-registry refused the copy:\n%s", b.screen())
	}
	b.typeText("3.28.3")
	b.keys("enter")
	if j := b.copyJob(); j.State != jobs.Succeeded {
		t.Fatalf("job %+v", j)
	}
}

// ---- 4.2 the destination chooser and the dialog's reads -----------------------------

func TestCopyChooserListsMembers(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	gamma := b.daemon("gamma", nil)
	down := b.add("gamma", gamma)
	gamma.Stop()
	b.until("gamma to be seen down", func() bool { return down.State() != session.Connected })
	b.settle()
	b.srv.ResetRequests()
	b.beta.ResetRequests()

	d := b.chooser(copyRef)
	// Opening the chooser asked neither daemon anything about the image.
	if b.exports() != 0 || len(b.beta.Requests()) != 0 || b.srv.CountRequests(http.MethodGet, `/history$`) != 0 {
		t.Fatalf("the chooser sent requests: %v / %v", b.srv.Requests(), b.beta.Requests())
	}
	if len(d.items) != 2 || d.items[0].id != b.target.Key() || d.items[0].disabled || !d.items[1].disabled || d.items[1].id != down.Key() {
		t.Fatalf("items %+v", d.items)
	}
	s := b.screen()
	for _, want := range []string{
		"Copy image to another daemon", "Copy " + copyRef, "from this daemon to:",
		"beta", b.beta.Host(), "Docker " + fakeengine.EngineVersion, "engine beta", "gamma", "unusable",
		"read both daemons, then confirm",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the chooser lacks %q:\n%s", want, s)
		}
	}
	// The disconnected member: named, with its reason, and not choosable.
	b.keys("down")
	s = b.screen()
	if !strings.Contains(s, "cannot be chosen — not connected") || !strings.Contains(s, "Enter does nothing on this one") {
		t.Fatalf("the unusable member:\n%s", s)
	}
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker || b.m.preparing {
		t.Fatalf("Enter chose an unusable member:\n%s", b.screen())
	}
	if len(b.beta.Requests()) != 0 || b.exports() != 0 {
		t.Fatal("Enter on an unusable member sent requests")
	}
	// The connected one: the confirmation opens for it, and still nothing
	// that changes a daemon — or reads the archive — has been sent.
	b.keys("up", "enter")
	b.until("the confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	c := b.m.dlg.plan.ImageCopy
	if c == nil || c.Target.Key != b.target.Key() || c.Target.Name != "beta" || c.Source.Key != b.m.cur.Key() {
		t.Fatalf("spec %+v", c)
	}
	if b.loads() != 0 || b.exports() != 0 || len(b.beta.Mutations())+len(b.srv.Mutations()) != 0 {
		t.Fatal("building the confirmation sent more than reads")
	}
	b.keys("esc")
}

func TestCopyConfirmationTagEffect(t *testing.T) {
	// New on the target.
	b := newCopyBench(t, benchOpts{}, nil)
	d := b.confirmation(copyRef)
	s := dialogText(b.bench)
	short := engine.ShortID(b.imageID)
	for _, want := range []string{
		"image " + copyRef + " (" + short + ")",
		"from " + b.m.cur.Name() + " " + b.srv.Host() + " (context default)",
		"to beta " + b.beta.Host() + " (context beta)",
		"platform linux/arm64/v8 · tag " + copyRef,
		"from " + b.m.cur.Name() + " to beta.",
		copyRef + " does not exist on beta: the tag is created there",
		"no file is written on this machine and no registry is contacted",
		"Type 3.28.3 and press Enter to proceed",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, s)
		}
	}
	if d.plan.Word != "3.28.3" || d.plan.ImageCopy.TargetOwner != "" {
		t.Fatalf("plan %+v", d.plan.ImageCopy)
	}
	b.keys("esc")

	// The tag names another image there: named by its short ID, and typed.
	var other string
	b2 := newCopyBench(t, benchOpts{}, func(s *fakeengine.Server) { other = s.AddImage(5000, time.Hour, copyRef) })
	d = b2.confirmation(copyRef)
	s = dialogText(b2.bench)
	if !strings.Contains(s, "names image "+other[:12]) || !strings.Contains(s, "moves the tag") || !strings.Contains(s, "Type 3.28.3") ||
		d.plan.ImageCopy.TargetOwner != "sha256:"+other || !d.plan.Runnable() {
		t.Fatalf("a tag that will move:\n%s", s)
	}
	b2.keys("esc")

	// The same build is already there: blocked, and nothing can be sent.
	b3 := newCopyBench(t, benchOpts{}, func(s *fakeengine.Server) {
		s.SetImageFixture(s.AddImage(0, time.Hour, copyRef), copyImageFixture(copyRef))
	})
	d = b3.confirmation(copyRef)
	s = dialogText(b3.bench)
	if d.plan.Runnable() || !strings.Contains(s, "REFUSED") || !strings.Contains(s, "Already present") {
		t.Fatalf("already present:\n%s", s)
	}
	b3.typeText("3.28.3")
	b3.keys("y")
	if b3.loads() != 0 || b3.exports() != 0 || len(b3.m.jobs.List()) != 0 {
		t.Fatal("a blocked copy sent requests")
	}
	b3.keys("enter")
	if b3.m.dlg != nil {
		t.Fatal("Enter did not dismiss the refusal")
	}

	// The target cannot be read: said, and no way to proceed.
	b4 := newCopyBench(t, benchOpts{}, nil)
	b4.chooser(copyRef)
	b4.beta.FailNext(http.MethodGet, `^/images/.+/json$`, 500, "graph driver is locked", 1)
	b4.keys("enter")
	b4.until("the confirmation", func() bool { return b4.m.dlg != nil && b4.m.dlg.kind == dlgPlan })
	s = dialogText(b4.bench)
	if b4.m.dlg.plan.Runnable() || !strings.Contains(s, "REFUSED") || !strings.Contains(s, "could not be read") || !strings.Contains(s, "graph driver is locked") {
		t.Fatalf("an unreadable target:\n%s", s)
	}
}

func TestCopyUntaggedImage(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	var name, id string
	for _, r := range b.m.rows[KindImages] {
		if r.imageRef == "" {
			name, id = r.name, r.imageID
		}
	}
	if name == "" {
		t.Fatalf("no untagged row: %v", b.rowNames())
	}
	d := b.confirmation(name)
	short := engine.ShortID(id)
	s := dialogText(b.bench)
	if d.plan.Word != short || !strings.Contains(s, "arrives untagged") || !strings.Contains(s, "Type "+short) || !strings.Contains(s, "no tag: it arrives untagged") {
		t.Fatalf("an untagged row:\n%s", s)
	}
	refs := b.beta.ImageRefs()
	b.typeText(short)
	b.keys("enter")
	j := b.copyJob()
	if j.State != jobs.Succeeded || strings.Join(b.beta.ImageRefs(), ",") != strings.Join(refs, ",") || len(b.beta.ImageIDs()) != 2 {
		t.Fatalf("job %v %q; target refs %v ids %v\n%s", j.State, j.Err, b.beta.ImageRefs(), b.beta.ImageIDs(), strings.Join(j.Log, "\n"))
	}
}

// ---- 4.3 the confirmation -----------------------------------------------------------

func TestCopyConfirmationIsTyped(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.confirmation(copyRef)
	// y, Enter alone, the wrong word: nothing is sent and the dialog stays.
	b.keys("enter")
	if b.m.dlg == nil || !b.m.dlg.mismatch {
		t.Fatalf("Enter without the word:\n%s", b.screen())
	}
	b.keys("y")
	b.keys("enter")
	b.see("That is not \"3.28.3\". Nothing was sent.")
	b.keys("backspace")
	b.typeText("3.28")
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPlan || b.loads() != 0 || b.exports() != 0 || len(b.m.jobs.List()) != 0 {
		t.Fatalf("something was sent without the word:\n%s", b.screen())
	}
	if len(b.beta.Mutations())+len(b.srv.Mutations()) != 0 {
		t.Fatal("a mutation was sent before the word")
	}
	// The word: the copy starts.
	b.typeText(".3")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the dialog stayed open:\n%s", b.screen())
	}
	if j := b.copyJob(); j.State != jobs.Succeeded {
		t.Fatalf("job %+v", j)
	}
}

func TestCopyConfirmationMustBeReadFirst(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.m.opts.ArmDelay = 300 * time.Millisecond
	b.selectRow(copyRef)
	b.keys("C")
	// The chooser has only just appeared: Enter is ignored.
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker {
		t.Fatal("the chooser accepted an Enter typed ahead of it")
	}
	b.run(350 * time.Millisecond)
	b.keys("enter")
	b.until("the confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	b.typeText("3.28.3")
	b.keys("enter")
	if b.m.dlg == nil || b.loads() != 0 || len(b.m.jobs.List()) != 0 {
		t.Fatal("the confirmation accepted input typed ahead of it")
	}
}

func TestCopyTargetRemovedWhileTheDialogIsOpen(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.confirmation(copyRef)
	b.srv.ResetRequests()
	b.beta.ResetRequests()
	if _, err := b.m.sess.Remove(b.target.ID()); err != nil {
		t.Fatal(err)
	}
	s := dialogText(b.bench)
	if !strings.Contains(s, "REFUSED") || !strings.Contains(s, "The target is gone") || !strings.Contains(s, "no longer in this session") || strings.Contains(s, "press Enter to proceed") {
		t.Fatalf("after the target left:\n%s", s)
	}
	// Nothing proceeds: not the word, not y.
	b.typeText("3.28.3")
	b.keys("y")
	if len(b.m.jobs.List()) != 0 || b.exports() != 0 || b.loads() != 0 {
		t.Fatal("a copy started for a target that is gone")
	}
	b.keys("enter")
	if b.m.dlg != nil || len(b.m.jobs.List()) != 0 {
		t.Fatal("the refusal was not dismissed, or a job started")
	}

	// A target that only lost its daemon blocks the same way.
	b2 := newCopyBench(t, benchOpts{}, nil)
	b2.confirmation(copyRef)
	b2.beta.Stop()
	b2.until("beta to be seen down", func() bool { return b2.target.State() != session.Connected })
	if s := dialogText(b2.bench); !strings.Contains(s, "REFUSED") || !strings.Contains(s, "cannot receive a copy now") {
		t.Fatalf("after the target disconnected:\n%s", s)
	}
	b2.typeText("3.28.3")
	b2.keys("enter")
	if len(b2.m.jobs.List()) != 0 {
		t.Fatal("a copy started for a disconnected target")
	}
}

// TestCopyFocusChangeDropsTheDialog: the source is the focused daemon; when
// focus moves, the confirmation is about another daemon's row and goes.
func TestCopyFocusChangeDropsTheDialog(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.confirmation(copyRef)
	b.exec(b.m.focusMember(b.target.ID()))
	b.settle()
	if b.m.dlg != nil || b.m.cur != b.target || b.loads() != 0 {
		t.Fatalf("the dialog survived a focus change:\n%s", b.screen())
	}
}

func TestCopyConfirmationFitsLongEndpointsAndTheMinimumTerminal(t *testing.T) {
	// The target listens on the longest socket path a unix socket allows.
	dir, err := os.MkdirTemp("", "dd-a-daemon-with-a-very-long-socket-directory-name-for-the-copy-dialog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	long := fakeengine.New()
	long.Name = "longhost"
	if err := long.Start(dir + "/docker.sock"); err != nil {
		t.Skipf("this system refuses the long socket path: %v", err)
	}
	t.Cleanup(long.Close)

	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		b := newMultiBench(t, benchOpts{seed: copySeed, width: size[0], height: size[1]})
		b.context("a-context-with-quite-a-long-name-of-its-own", long.Host())
		mem := b.add("a-context-with-quite-a-long-name-of-its-own", nil)
		b.kind(KindImages)
		b.m.focus = PaneList
		b.selectRow(copyRef)
		b.keys("C")
		b.assertFrameGeometry()
		b.dump(fmt.Sprintf("copy chooser %dx%d", size[0], size[1]))
		b.keys("enter")
		b.until("the confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
		b.assertFrameGeometry()
		b.dump(fmt.Sprintf("copy confirmation %dx%d", size[0], size[1]))
		s := b.screen()
		// The prompt and the input are on screen at every size.
		if !strings.Contains(s, "Type 3.28.3 and press Enter to proceed") || !strings.Contains(s, "> ") {
			t.Fatalf("%dx%d: the prompt is not on screen:\n%s", size[0], size[1], s)
		}
		// Both endpoints are there in full, however many lines they take.
		inner := clamp(b.m.width-4, 40, dialogMaxWidth) - 4
		var ident strings.Builder
		for _, line := range b.m.copyIdent(b.m.dlg.plan, inner) {
			if w := ansi.StringWidth(line); w > inner {
				t.Fatalf("%dx%d: an identity line is %d cells, the dialog has %d: %q", size[0], size[1], w, inner, ansi.Strip(line))
			}
			text := ansi.Strip(line)
			if len(text) > 11 {
				text = text[:11] + strings.TrimSpace(text[11:])
			}
			ident.WriteString(strings.TrimSpace(text))
		}
		for _, want := range []string{long.Host(), b.srv.Host(), "a-context-with-quite-a-long-name-of-its-own", "linux/arm64/v8"} {
			if !strings.Contains(ident.String(), want) {
				t.Errorf("%dx%d: the identity lacks %q in full: %q", size[0], size[1], want, ident.String())
			}
		}
		// The whole of it can be read by scrolling, and the word still works.
		b.keys("pgdown", "pgdown", "pgup")
		b.assertFrameGeometry()
		b.typeText("3.28.3")
		b.keys("enter")
		b.until("the copy job", func() bool {
			for _, j := range b.m.jobs.List() {
				if j.Title == "copy image" && j.State.Finished() {
					return true
				}
			}
			return false
		})
		b.assertFrameGeometry()
		if !long.HasImage(copyRef) {
			t.Fatalf("%dx%d: the image did not arrive", size[0], size[1])
		}
		// For the next size the target must not hold the tag: untagged, the
		// build stays there and the copy creates the tag again.
		_ = mem
		long.Untag(copyRef)
	}
}

// ---- 4.4 the job --------------------------------------------------------------------

func TestCopyJobCopiesTheImage(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	srcRefs, srcIDs := b.srv.ImageRefs(), b.srv.ImageIDs()
	b.confirmation(copyRef)
	b.srv.ResetRequests()
	b.beta.ResetRequests()
	b.typeText("3.28.3")
	b.keys("enter")
	srcName := b.m.cur.Name()
	if !strings.Contains(b.m.status.text, "started: copy image "+copyRef+" to beta from "+srcName) {
		t.Fatalf("status %q", b.m.status.text)
	}
	j := b.copyJob()
	log := strings.Join(j.Log, "\n")
	if j.State != jobs.Succeeded {
		t.Fatalf("job %+v\n%s", j, log)
	}
	// The job is recorded against both daemons, and names both.
	if j.Daemon != b.target.Daemon() || j.From != b.m.cur.Daemon() {
		t.Fatalf("job daemons %q from %q", j.Daemon, j.From)
	}
	b.see("✓ copy image " + copyRef + " on beta from " + srcName)
	if jt := jobsText(b.bench); !strings.Contains(jt, "copy image "+copyRef+" on beta from "+srcName) {
		t.Fatalf("the Jobs pane: %q", jt)
	}
	for _, want := range []string{"streamed", "1.1 MB", "platform linux/arm64/v8", "verified on the target", "was created on the target", plan.CopySourceNote} {
		if !strings.Contains(log, want) {
			t.Errorf("the job's log lacks %q:\n%s", want, log)
		}
	}
	// The image is on the target; the source is exactly as it was.
	if !b.beta.HasImage(copyRef) || !b.beta.HasImage("resident:1") {
		t.Fatalf("the target's references: %v", b.beta.ImageRefs())
	}
	if m := b.srv.Mutations(); len(m) != 0 {
		t.Fatalf("the source received mutating requests: %v", m)
	}
	if strings.Join(b.srv.ImageRefs(), ",") != strings.Join(srcRefs, ",") || strings.Join(b.srv.ImageIDs(), ",") != strings.Join(srcIDs, ",") {
		t.Fatal("the source's images changed")
	}
	if m := b.beta.Mutations(); len(m) != 1 || m[0].Path != "/images/load" {
		t.Fatalf("the target's mutations: %v", m)
	}
	if b.exports() != 1 {
		t.Fatalf("%d archive requests on the source", b.exports())
	}
	// The job's output names both daemons in full.
	b.m.focus = PaneJobs
	b.keys("enter")
	b.see("on daemon "+b.target.Daemon(), "from daemon "+b.m.cur.Daemon())
	b.keys("esc")

	// The target's list shows it once the target is focused.
	b.focusName("beta")
	b.kind(KindImages)
	b.see(copyRef)
}

func TestCopyJobCancelAndHeldDaemons(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	gamma := b.daemon("gamma", nil)
	third := b.add("gamma", gamma)
	b.beta.LoadChunkDelay = 15 * time.Millisecond
	source := b.m.cur
	b.confirmation(copyRef)
	b.typeText("3.28.3")
	b.keys("enter")
	var id int
	b.until("the stream to be under way", func() bool {
		for _, j := range b.m.jobs.List() {
			if j.Title == "copy image" && strings.HasPrefix(j.Status, "streamed ") && j.Status != "streamed 0 B" {
				id = j.ID
				return true
			}
		}
		return false
	})
	// The Jobs pane shows the bytes and both names while it runs.
	if jt := jobsText(b.bench); !strings.Contains(jt, "copy image "+copyRef+" on beta from "+source.Name()) || !strings.Contains(jt, "streamed ") {
		t.Fatalf("the Jobs pane during the copy: %q", jt)
	}

	// Neither daemon can be removed: the refusal names the copy.
	for _, mem := range []*session.Member{source, b.target} {
		_, err := b.m.sess.Remove(mem.ID())
		var refused *session.RefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "copy image "+copyRef) {
			t.Fatalf("removing %s during the copy: %v", mem.Name(), err)
		}
	}
	// A third daemon is not held.
	if on := b.m.sess.RunningJobs(third); len(on) != 0 {
		t.Fatalf("the third daemon holds %v", on)
	}
	// Focus moves to a third daemon: the copy goes on, still named in full.
	b.focusName("gamma")
	if j, _ := b.m.jobs.Get(id); j.State != jobs.Running {
		t.Fatalf("the copy is %v after a focus change", j.State)
	}
	if jt := jobsText(b.bench); !strings.Contains(jt, "copy image "+copyRef+" on beta from "+source.Name()) {
		t.Fatalf("the Jobs pane after a focus change: %q", jt)
	}

	// Cancelled from the Jobs pane: both streams close.
	b.m.focus = PaneJobs
	b.m.jobSel = id
	b.keys("c")
	j := b.copyJob()
	log := strings.Join(j.Log, "\n")
	if j.State != jobs.Cancelled || !strings.Contains(log, plan.CopyCancelledNote) || !strings.Contains(log, plan.CopySourceNote) {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
	b.until("the target to see its request end", func() bool { return len(b.beta.LoadRequests()) == 1 })
	if r := b.beta.LoadRequests()[0]; r.Err == "" || r.ImageID != "" || b.beta.HasImage(copyRef) {
		t.Fatalf("the target after a cancel: %+v", r)
	}
	if len(b.srv.Mutations()) != 0 {
		t.Fatalf("the source received %v", b.srv.Mutations())
	}
	b.see("cancelled: copy image " + copyRef + " on beta from " + source.Name())
	// Once it has ended, the daemons can leave.
	if _, err := b.m.sess.Remove(b.target.ID()); err != nil {
		t.Fatalf("removing the target after the copy: %v", err)
	}
}

// TestCopyTargetRemovedBeforeTheJobStarts: a target that is being removed
// when the confirmation is completed gets no job that runs.
func TestCopyNotStartedOnADaemonBeingRemoved(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	d := b.confirmation(copyRef)
	p := d.plan
	// The job manager is told the target is closing, as Session.Remove
	// does first; the member is still listed.
	if running := b.m.jobs.CloseDaemon(b.target.Daemon()); running != nil {
		t.Fatal(running)
	}
	b.m.dlg = nil
	b.m.runPlan(p)
	b.settle()
	if !strings.Contains(b.m.status.text, "copy image") || !strings.Contains(b.m.status.text, "being removed") {
		t.Fatalf("status %q", b.m.status.text)
	}
	if b.loads() != 0 || b.exports() != 0 {
		t.Fatal("a copy ran on a daemon being removed")
	}
}

// TestCopyFailureIsAnnouncedWithBothDaemons: the target fails the load; the
// status line and the job say which daemon, and nothing is claimed.
func TestCopyFailureIsAnnouncedWithBothDaemons(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	b.confirmation(copyRef)
	b.beta.FailLoad("write /var/lib/docker/tmp: no space left on device\x1b[2J")
	b.typeText("3.28.3")
	b.keys("enter")
	j := b.copyJob()
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the target beta") || !strings.Contains(j.Err, "no space left on device") {
		t.Fatalf("job %v %q", j.State, j.Err)
	}
	b.see("✗ copy image " + copyRef + " on beta from " + b.m.cur.Name())
	// The daemon's text reached the screen as text.
	for _, s := range append(append([]string{}, j.Log...), j.Err, b.m.status.text) {
		if strings.Contains(s, "\x1b") {
			t.Fatalf("an escape sequence reached the job's text: %q", s)
		}
	}
	if b.beta.HasImage(copyRef) {
		t.Fatal("a failed load produced the image")
	}
}

// TestCopyEscapesTheTargetsOwnName: what a daemon calls itself is drawn as
// text in the chooser.
func TestCopyEscapesTheTargetsOwnName(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: copySeed})
	evil := b.daemon("beta", nil)
	evil.Name = "evil\x1b[2J\x1b]0;owned\x07"
	b.add("beta", evil)
	b.kind(KindImages)
	b.m.focus = PaneList
	b.selectRow(copyRef)
	b.keys("C")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker {
		t.Fatal("no chooser")
	}
	b.m.dirty = true
	if raw := b.m.View(); strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b]0;") || strings.Contains(raw, "\x07") {
		t.Fatal("the daemon's name reached the terminal as control characters")
	}
}

// ---- 3.2 a two-daemon job among single-daemon ones ----------------------------------

func TestTwoDaemonJobIsNamedWithBoth(t *testing.T) {
	b := newCopyBench(t, benchOpts{}, nil)
	source := b.m.cur
	gate := make(chan struct{})
	defer close(gate)
	id, err := b.m.jobs.StartOnPair(b.target.Daemon(), source.Daemon(), "copy image", "acme/api:1", func(ctx context.Context, r *jobs.Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	single := b.m.startJob("pull image", "nginx:1.27", func(ctx context.Context, r *jobs.Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	})
	b.settle()
	jt := jobsText(b.bench)
	if !strings.Contains(jt, "copy image acme/api:1 on beta from "+source.Name()) || !strings.Contains(jt, "pull image nginx:1.27 @"+source.Name()) ||
		strings.Contains(jt, "pull image nginx:1.27 on ") {
		t.Fatalf("the Jobs pane: %q", jt)
	}
	j, _ := b.m.jobs.Get(id)
	if got := b.m.onDaemon(j); got != " on beta from "+source.Name() {
		t.Fatalf("onDaemon = %q", got)
	}
	sj, _ := b.m.jobs.Get(single)
	if got := b.m.onDaemon(sj); got != " on "+source.Name() {
		t.Fatalf("a single-daemon job's phrase = %q", got)
	}
	// A source that has since left the session is still named, by endpoint.
	j.From = "tcp://gone.example:2376"
	if got := b.m.onDaemon(j); got != " on beta from tcp://gone.example:2376" {
		t.Fatalf("onDaemon = %q", got)
	}
}

// ---- 4.5 documentation --------------------------------------------------------------

func TestCopyIsDocumented(t *testing.T) {
	b := newCopyBench(t, benchOpts{width: 140, height: 50}, nil)
	help := strings.Join(b.m.helpLines(120), "\n")
	for _, want := range []string{
		copyImageAction.desc, "C copies one image (never a selection)", "no file on this machine",
		"--no-registry leaves it available; read-only refuses it", "arrives untagged",
		"neither can be removed while it runs", "layers the target already accepted may remain",
		"copy to daemon: type the tag shown", "on <target> from <source>",
		"one that names any other image or tag is stopped",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the keys lack %q", want)
		}
	}
	// The action menu offers it for an image.
	b.selectRow(copyRef)
	b.keys("enter")
	if s := b.screen(); !strings.Contains(s, "C   copy to daemon…") || !strings.Contains(s, "copy this image to another connected daemon") {
		t.Fatalf("the action menu lacks the copy:\n%s", s)
	}
	b.keys("C")
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker {
		t.Fatalf("C in the action menu did not open the chooser:\n%s", b.screen())
	}
	b.keys("esc")

	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		"`C` copy to daemon…", "### Copy — one image, from one daemon to another",
		"no file on this machine", "no registry", "`POST /images/load`", "arrives untagged",
		"Already present", "neither can be removed", "layers the target already accepted may remain",
		"one action may name two", "between daemons",
		"The archive may name only what was confirmed", "before the bytes that complete it are passed on",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
}
