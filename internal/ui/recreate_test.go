package ui

import (
	"context"
	"errors"
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
)

// uiSentinel is an environment value of secret-svc's own: it must never be
// drawn, logged or put in a status line.
const uiSentinel = "ui-s3cret-sentinel-0451"

// recreateSeed is the demo, the Changes tab's refusal cases, a privileged
// container and a container with a secret, both on tags that moved.
func recreateSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.SeedChanges()
	day := 24 * time.Hour
	s.SeedMovedTag(fakeengine.ContainerSpec{Name: "priv-svc", Image: "acme/priv:1", Cmd: []string{"svc"}, Privileged: true, State: "exited", Age: day},
		fixture.ChangesReport("acme/priv:1", false), fixture.ChangesReport("acme/priv:1", true), nil, nil, 2*day)
	s.SeedMovedTag(fakeengine.ContainerSpec{Name: "secret-svc", Image: "acme/secret:1", Cmd: []string{"svc"}, Env: []string{"TOKEN=" + uiSentinel, "LOG_LEVEL=info"}, Age: day},
		fixture.ChangesReport("acme/secret:1", false), fixture.ChangesReport("acme/secret:1", true), nil, nil, 2*day)
}

// openRecreatable opens the Changes tab of name and waits until both
// images are read, so the tab has decided whether to offer A.
func (b *bench) openRecreatable(name string) {
	b.t.Helper()
	b.openChanges(name)
	b.until(name+": both images read", func() bool {
		t, ok := b.m.changesTarget()
		return ok && (t.old != nil && t.new != nil || !t.refusal.None())
	})
	b.settle()
}

// pressRecreate presses A and waits for the dialog.
func (b *bench) pressRecreate() *dialog {
	b.t.Helper()
	b.keys("A")
	b.until("the re-create confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	return b.m.dlg
}

// noSentinel fails if the secret is anywhere the operator or a log could
// read it.
func (b *bench) noSentinel(when string) {
	b.t.Helper()
	hay := []string{b.screen(), b.m.status.text}
	if b.m.dlg != nil {
		hay = append(hay, b.dialogText())
	}
	for _, j := range b.m.jobs.List() {
		full, _ := b.m.jobs.Get(j.ID)
		hay = append(hay, full.Title, full.Target, full.Status, full.Err, strings.Join(full.Log, "\n"))
	}
	if all := strings.Join(hay, "\n"); strings.Contains(all, uiSentinel) {
		b.t.Fatalf("%s: the environment value is visible:\n%s", when, all)
	}
}

func TestRecreateOfferedOnAMovedTag(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 40})
	b.srv.ResetRequests()
	b.openRecreatable("report-job")
	all := b.changesText()
	for _, want := range []string{
		"Restarting report-job keeps the image it runs; only re-creating it takes the image the tag names now.",
		"A re-creates report-job on that build (", "created from acme/report:1.4", "Nothing is deleted.",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "which this desk does not do") {
		t.Error("the old sentence is still there")
	}
	b.see("A re-create")
	// Opening the tab sends nothing that changes the daemon.
	b.noMutations("opening the Changes tab")
}

func TestRecreateNotOffered(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 40})
	for _, c := range []struct{ name, want string }{
		{"priv-svc", "priv-svc cannot be re-created here: it carries settings a re-create does not reproduce — Privilege: runs privileged."},
		{"pinned-svc", "This reference names no other build, so there is nothing to re-create pinned-svc onto."},
		{"by-id-svc", "This reference names no other build"},
		{"shop-web-1", "The tag names the image shop-web-1 runs: there is nothing to re-create."},
		{"edge-svc", "The tag names a build for another platform, so this tab does not re-create edge-svc."},
		{"untagged-svc", "The tag names no image here, so there is nothing to re-create untagged-svc onto; this desk does not pull."},
	} {
		b.openRecreatable(c.name)
		all := b.changesText()
		if !strings.Contains(all, c.want) || !strings.Contains(all, "only re-creating it takes the image the tag names now") {
			t.Errorf("%s: missing %q in:\n%s", c.name, c.want, all)
		}
		if strings.Contains(b.screen(), "A re-create") {
			t.Errorf("%s: the footer offers A", c.name)
		}
		b.keys("esc")
	}

	// A on a pair that cannot move is refused from what the tab knows.
	b.openRecreatable("shop-web-1")
	b.srv.ResetRequests()
	b.keys("A")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "Nothing to re-create: The tag names the image shop-web-1 runs") || len(b.srv.Requests()) != 0 {
		t.Errorf("A on the same image: dialog %v, status %q, %d requests", b.m.dlg != nil, b.m.status.text, len(b.srv.Requests()))
	}
	b.keys("esc")

	// A on refused settings opens the dialog, which names them and offers no way on.
	b.openRecreatable("priv-svc")
	b.srv.ResetRequests()
	d := b.pressRecreate()
	if d.plan.Runnable() || d.hasInput {
		t.Fatalf("a refused plan can be confirmed: %+v", d.plan.Blockers)
	}
	text := b.dialogText()
	if !strings.Contains(text, "REFUSED — nothing will be sent to the daemon") || !strings.Contains(text, "Privilege: runs privileged") {
		t.Errorf("dialog:\n%s", text)
	}
	b.typeText("recreate")
	b.keys("enter")
	b.settle()
	b.noMutations("a refused re-create")
}

// 4.1: the reads behind the key and their three outcomes.
func TestRecreatePrepareOutcomes(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed})
	ctx := context.Background()
	def := plan.DefaultsFrom(b.m.info)

	b.srv.ResetRequests()
	p, err := buildRecreate(ctx, b.eng, def, b.srv.ContainerID("report-job"))
	if err != nil || !p.Runnable() || p.Word != "recreate" || p.PinRef != "acme/report:1.4" {
		t.Fatalf("report-job: %v %+v", err, p.Blockers)
	}
	b.noMutations("preparing a re-create")
	for _, r := range b.srv.Requests() {
		if strings.Contains(r.Path, "/images/get") || strings.HasSuffix(r.Path, "/archive") {
			t.Errorf("an archive was read: %s", r)
		}
	}

	_, err = buildRecreate(ctx, b.eng, def, b.srv.ContainerID("shop-web-1"))
	var na *plan.NotApplicable
	if !errors.As(err, &na) || !strings.Contains(na.Reason, "same image on both sides") {
		t.Errorf("same image: %v", err)
	}
	_, err = buildRecreate(ctx, b.eng, def, b.srv.ContainerID("edge-svc"))
	if !errors.As(err, &na) || !strings.Contains(na.Reason, "Different platforms") {
		t.Errorf("platform: %v", err)
	}
	_, err = buildRecreate(ctx, b.eng, def, b.srv.ContainerID("pinned-svc"))
	if !errors.As(err, &na) || !strings.Contains(na.Reason, "Pinned to a digest") {
		t.Errorf("pinned: %v", err)
	}
	p, err = buildRecreate(ctx, b.eng, def, b.srv.ContainerID("priv-svc"))
	if err != nil || p.Runnable() || len(p.Blockers) != 1 || !strings.HasPrefix(p.Blockers[0], "Privilege: ") {
		t.Errorf("privileged: %v %v", err, p.Blockers)
	}
	_, err = buildRecreate(ctx, b.eng, def, strings.Repeat("f", 64))
	if !errors.As(err, &na) {
		t.Errorf("gone: %v", err)
	}
}

// 4.2
func TestRecreateKeyRefusedBeforeAnyRequest(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, readOnly: true, width: 120, height: 40})
	b.openRecreatable("report-job")
	if all := b.changesText(); !strings.Contains(all, "In read-only mode A sends nothing.") {
		t.Errorf("read-only note:\n%s", all)
	}
	if strings.Contains(b.screen(), "A re-create") {
		t.Error("read-only footer offers A")
	}
	b.srv.ResetRequests()
	b.keys("A")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "Read-only mode") || len(b.srv.Requests()) != 0 {
		t.Errorf("read-only: dialog %v status %q requests %v", b.m.dlg != nil, b.m.status.text, b.srv.Requests())
	}

	b = newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 40})
	b.kind(KindContainers)
	b.m.focus = PaneList
	b.selectRow("shop-web-1")
	b.keys(" ")
	b.openRecreatable("report-job")
	b.srv.ResetRequests()
	b.keys("A")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "A re-create acts on one container at a time and 1 are selected") || len(b.srv.Requests()) != 0 {
		t.Errorf("selection: dialog %v status %q requests %v", b.m.dlg != nil, b.m.status.text, b.srv.Requests())
	}
}

// 4.4
func TestRecreateConfirmation(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 50})
	id := b.srv.ContainerID("report-job")
	b.openRecreatable("report-job")
	tgt, _ := b.m.changesTarget()
	b.srv.ResetRequests()
	d := b.pressRecreate()
	if !d.plan.Runnable() || !d.hasInput {
		t.Fatalf("not confirmable: %v", d.plan.Blockers)
	}
	// A long name may wrap after one of its hyphens.
	text := strings.ReplaceAll(b.dialogText(), "- ", "-")
	prev := "report-job-previous-" + engine.ShortID(id)
	for _, want := range []string{
		"Re-create container", "container report-job (" + engine.ShortID(id) + ")", "daemon",
		"acme/report:1.4", engine.ShortID(tgt.pair.OldID), engine.ShortID(tgt.pair.NewID),
		"renamed " + prev + " and kept", "Nothing is deleted; the old container stays stopped under " + prev,
		"Carried:", "Type recreate and press Enter to proceed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dialog does not say %q:\n%s", want, text)
		}
	}
	b.noMutations("opening the confirmation")

	// Enter alone, y, and a wrong word send nothing.
	b.keys("enter")
	b.keys("y", "enter")
	b.settle()
	b.noMutations("Enter or y without the word")
	if b.m.dlg == nil || !strings.Contains(b.dialogText(), `That is not "recreate". Nothing was sent.`) {
		t.Fatalf("mismatch not said:\n%s", b.screen())
	}
	b.keys("backspace")
	b.typeText("recreat")
	b.keys("enter")
	b.settle()
	b.noMutations("a word that does not match")

	// Escape declines: no job.
	jobsBefore := len(b.m.jobs.List())
	b.keys("esc")
	if b.m.dlg != nil || len(b.m.jobs.List()) != jobsBefore {
		t.Fatal("Escape did not just close")
	}
	b.noMutations("declining")

	// The word: one job, four steps (report-job is not running: three).
	b.pressRecreate()
	b.typeText("recreate")
	b.keys("enter")
	b.finishJobs()
	j := b.lastJob()
	if j.State != jobs.Succeeded || j.Title != "re-create container" || j.Target != "report-job" {
		t.Fatalf("job %+v", j)
	}
	if b.srv.ContainerID(prev) != id || b.srv.ContainerID("report-job") == id {
		t.Errorf("containers: %v", b.srv.ContainerNames())
	}
}

func TestRecreateTypedAheadSendsNothing(t *testing.T) {
	const arm = 150 * time.Millisecond
	b := newBench(t, benchOpts{seed: recreateSeed, arm: arm, width: 120, height: 40})
	b.openRecreatable("report-job")
	b.srv.ResetRequests()
	b.keys("A")
	b.typeText("recreate")
	b.keys("enter")
	b.settle()
	b.noMutations("the word typed before the confirmation could have been read")
	if b.m.dlg == nil || b.m.dlg.input.Value() != "" {
		t.Fatal("the dialog closed, or kept early keystrokes")
	}
	b.run(arm)
	b.typeText("recreate")
	b.keys("enter")
	b.finishJobs()
	if j := b.lastJob(); j.State != jobs.Succeeded {
		t.Fatalf("armed: %+v", j)
	}
}

// 4.5: the value goes to the daemon and nowhere else.
func TestRecreateNeverShowsAnEnvironmentValue(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 50})
	b.openRecreatable("secret-svc")
	b.noSentinel("the tab")
	b.pressRecreate()
	if !strings.Contains(b.dialogText(), "TOKEN") {
		t.Errorf("the variable's name is not shown:\n%s", b.dialogText())
	}
	b.noSentinel("the dialog")

	// A refused create, whose message quotes the value.
	b.srv.FailNext("POST", `^/containers/create$`, 400, "invalid environment TOKEN="+uiSentinel, 1)
	b.typeText("recreate")
	b.keys("enter")
	b.finishJobs()
	j, _ := b.m.jobs.Get(b.lastJob().ID)
	if j.State != jobs.Failed || !strings.Contains(strings.Join(j.Log, "\n"), "‹value hidden›") {
		t.Fatalf("job %+v", j)
	}
	b.noSentinel("the job, the status line and the refused create")

	// On success, what was sent carried it, and nothing shows it.
	b = newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 50})
	b.openRecreatable("secret-svc")
	b.pressRecreate()
	b.typeText("recreate")
	b.keys("enter")
	b.finishJobs()
	if j := b.lastJob(); j.State != jobs.Succeeded {
		t.Fatalf("job %+v", j)
	}
	b.noSentinel("a re-create that succeeded")
	reqs := b.srv.CreateRequests()
	if len(reqs) == 0 {
		t.Fatal("no create was sent")
	}
	if body := reqs[len(reqs)-1].Body; body.Config == nil || !strings.Contains(strings.Join(body.Config.Env, " "), "TOKEN="+uiSentinel) {
		t.Errorf("the value was not sent: %+v", body.Config)
	}
}

// 4.6: after the job both rows say why there is nothing to offer.
func TestRecreateAfterTheJob(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed, width: 120, height: 40})
	oldID := b.srv.ContainerID("report-job")
	prev := "report-job-previous-" + engine.ShortID(oldID)
	b.openRecreatable("report-job")
	b.pressRecreate()
	b.typeText("recreate")
	b.keys("enter")
	b.finishJobs()
	b.until("the list shows both", func() bool {
		names := strings.Join(b.rowNames(), " ")
		return strings.Contains(names, prev) && b.srv.ContainerID("report-job") != oldID
	})

	b.keys("esc")
	b.openRecreatable("report-job")
	if all := b.changesText(); !strings.Contains(all, "The tag names the image report-job runs: there is nothing to re-create.") {
		t.Errorf("new row:\n%s", all)
	}
	if strings.Contains(b.screen(), "A re-create") {
		t.Error("the new row offers A")
	}

	b.keys("esc")
	b.openRecreatable(prev)
	all := b.changesText()
	if !strings.Contains(all, prev+" was set aside by a re-create: its name report-job now belongs to another container") {
		t.Errorf("old row:\n%s", all)
	}
	if strings.Contains(b.screen(), "A re-create") {
		t.Error("the old row offers A")
	}
}

func TestRecreateHelpAndFooterAgree(t *testing.T) {
	b := newBench(t, benchOpts{seed: recreateSeed})
	help := strings.Join(b.m.helpLines(120), "\n")
	if !strings.Contains(help, "re-create the container on the build the tag names now (type recreate)") {
		t.Errorf("help:\n%s", help)
	}
}

// 5.1: the README names A with the help's own words, links the screens, and
// no longer says the desk does not re-create.
func TestRecreateIsDocumented(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(data)), " ")
	b := newBench(t, benchOpts{})
	help := ansi.Strip(strings.Join(b.m.helpLines(200), "\n"))
	words := "re-create the container on the build the tag names now (type recreate)"
	if !strings.Contains(help, words) {
		t.Errorf("the help does not say %q", words)
	}
	if !strings.Contains(readme, "**`A` "+words+".**") {
		t.Errorf("README.md does not document A as %q", words)
	}
	for _, want := range []string{
		"`A` re-create on the build the tag names now", "type `recreate` and press Enter",
		"Nothing is deleted.", "nothing at all is sent", "<name>-previous-<id>",
		"docs/screens/58-recreate-confirm.txt", "docs/screens/59-recreate-confirm-80x24.txt",
		"docs/screens/60-recreate-refused.txt", "docs/screens/61-recreate-refused-80x24.txt",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
	for _, gone := range []string{"which this desk does not do", "never reconfigures one"} {
		if strings.Contains(readme, gone) {
			t.Errorf("README.md still says %q", gone)
		}
	}
}
