package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

// mark highlights each named row and presses space on it.
func (b *bench) mark(names ...string) {
	b.t.Helper()
	b.m.focus = PaneList
	for _, n := range names {
		b.selectRow(n)
		b.keys(" ")
	}
}

func (b *bench) markedNames() []string {
	var out []string
	for _, r := range b.m.markedRows(b.m.kind) {
		out = append(out, r.name)
	}
	sort.Strings(out)
	return out
}

func (b *bench) planDialog(what string) *dialog {
	b.t.Helper()
	b.until(what, func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	return b.m.dlg
}

func TestSelectionSurvivesRefreshAndDropsVanishedObjects(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.mark("redis-cache", "old-migration", "shop-web-1")
	b.see("2 Containers (12) - 3 selected")
	// Space again takes a row out.
	b.selectRow("shop-web-1")
	b.keys(" ")
	b.see("2 Containers (12) - 2 selected")
	if got := strings.Join(b.markedNames(), ","); got != "old-migration,redis-cache" {
		t.Fatalf("marked = %s", got)
	}
	// The marker is in the gutter, highlighted or not.
	screen := b.screen()
	for _, name := range []string{"redis-cache", "old-migration"} {
		found := false
		for _, line := range strings.Split(screen, "\n") {
			if strings.Contains(line, "✓"+name) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s carries no marker:\n%s", name, screen)
		}
	}

	// Rows appear above them and states change: the marks follow the objects.
	b.srv.AddContainer(fakeengine.ContainerSpec{Name: "aaa", Image: "alpine:3.20"})
	b.srv.Crash("redis-cache", 137)
	b.srv.Emit("container", "create", "x", nil)
	b.until("refresh", func() bool { return len(b.m.snap.Containers) == 13 })
	b.see("2 Containers (13) - 2 selected")
	if got := strings.Join(b.markedNames(), ","); got != "old-migration,redis-cache" {
		t.Fatalf("after a refresh, marked = %s", got)
	}
	if r := b.m.markedRows(KindContainers); r[0].state != "exited" && r[1].state != "exited" {
		t.Fatalf("marks were not refreshed with the list: %+v", r)
	}

	// One is removed elsewhere: its mark goes with it.
	if err := b.eng.RemoveContainer(context.Background(), b.srv.ContainerID("old-migration")); err != nil {
		t.Fatal(err)
	}
	b.until("refresh", func() bool { return len(b.m.snap.Containers) == 12 })
	b.see("2 Containers (12) - 1 selected")
	if got := strings.Join(b.markedNames(), ","); got != "redis-cache" {
		t.Fatalf("after removal, marked = %s", got)
	}

	// Each kind has its own selection.
	b.kind(KindVolumes)
	b.see("2 Volumes (7)")
	b.notSee("selected")
	b.kind(KindContainers)
	b.see("- 1 selected")
}

// TestSetPlanHoldsExactlyTheSelectedRunnableObjects: the action key applies
// to the selection, the confirmation lists exactly the objects that will be
// acted on and names the one left out with its reason, and confirming sends
// exactly those requests.
func TestSetPlanHoldsExactlyTheSelectedRunnableObjects(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.mark("redis-cache", "old-migration", "shop-web-1")
	b.selectRow("noisy-logger") // the highlight is not part of the selection
	b.srv.ResetRequests()
	b.keys("t")
	d := b.planDialog("set confirmation")
	b.noMutations("after the action key")

	want := []string{b.srv.ContainerID("shop-web-1"), b.srv.ContainerID("redis-cache")}
	var got []string
	for _, st := range d.plan.Steps {
		got = append(got, st.ID)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan steps = %v, want exactly the running selected containers %v", got, want)
	}
	b.see("Stop containers", "selected", "2 containers · 1 not included",
		"Exactly these 2 will be stopped:", "redis-cache", "shop-web-1",
		"Not included (1)", "old-migration — old-migration is not running (exited)",
		"Type stop 2 to confirm", b.srv.Host())
	if strings.Contains(b.screen(), "noisy-logger is") {
		t.Fatal("the highlighted row slipped into the set")
	}

	b.typeText("stop 2")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatal("the confirmation stayed open")
	}
	if n := len(b.m.marks[KindContainers]); n != 0 {
		t.Fatalf("the selection was not cleared when the job started: %d left", n)
	}
	b.notSee("selected")
	b.finishJobs()
	muts := b.srv.Mutations()
	if len(muts) != 2 {
		t.Fatalf("want exactly 2 requests, got %v", muts)
	}
	for _, r := range muts {
		if r.Method != "POST" || !strings.HasSuffix(r.Path, "/stop") || strings.Contains(strings.ToLower(r.Query), "force") {
			t.Fatalf("unexpected request %s", r)
		}
	}
	if st, _ := b.srv.ContainerState("noisy-logger"); st != "running" {
		t.Fatal("the highlighted, unselected container was stopped")
	}
	b.see("✓ stop 2 containers")
}

// TestInUseObjectsAreNotIncludedWithTheirReason, for every kind that can be
// removed as a set.
func TestInUseObjectsAreNotIncludedWithTheirReason(t *testing.T) {
	cases := []struct {
		kind    Kind
		rows    []string
		include string
		want    []string
	}{
		{KindContainers, []string{"redis-cache", "old-migration"}, "old-migration",
			[]string{"redis-cache — redis-cache is running. Docker Desk never force-removes"}},
		{KindImages, []string{"nginx:1.27", "python:3.12-slim"}, "python:3.12-slim",
			[]string{"nginx:1.27 — In use by 1 container: shop-web-1 (running)."}},
		{KindVolumes, []string{"shop_pgdata", "old-backups"}, "old-backups",
			[]string{"shop_pgdata — In use by 1 container: shop-db-1 (running)."}},
		{KindNetworks, []string{"bridge", "orphan-net"}, "orphan-net",
			[]string{"bridge —", "predefined network"}},
	}
	b := newBench(t, benchOpts{})
	for _, tc := range cases {
		t.Run(kindNames[tc.kind], func(t *testing.T) {
			b.kind(tc.kind)
			b.mark(tc.rows...)
			b.srv.ResetRequests()
			b.keys("d")
			d := b.planDialog("removal of a set")
			b.see(append([]string{"Not included (1)", "Exactly these 1 will be deleted", "Type remove 1 to confirm"}, tc.want...)...)
			if len(d.plan.Steps) != 1 || d.plan.Steps[0].Name != tc.include {
				t.Fatalf("steps = %+v, want only %s", d.plan.Steps, tc.include)
			}
			if !d.plan.Runnable() || len(d.plan.Blockers) != 0 {
				t.Fatal("the rest of the set must remain runnable")
			}
			b.keys("esc")
			b.noMutations("after declining")
			if len(b.m.marks[tc.kind]) != 2 {
				t.Fatal("declining must keep the selection")
			}
			b.keys("esc") // clear it
		})
	}

	// A set of which nothing can be done: it is said, with every reason,
	// and nothing can be confirmed.
	b.kind(KindContainers)
	b.mark("redis-cache", "shop-web-1")
	b.keys("s")
	d := b.planDialog("start of running containers")
	b.see("Nothing to do: none of the 2 containers selected can be started", "redis-cache is already running", "shop-web-1 is already running")
	if d.hasInput || d.plan.Runnable() {
		t.Fatal("an empty set can be confirmed")
	}
	b.typeText("start 0")
	b.keys("enter", "y")
	b.settle()
	b.noMutations("after trying to confirm an empty set")
}

// TestSetConfirmationMustTypeTheCount: neither `y`, nor the bare verb, nor
// another count consents to a set — only the word with the count shown.
func TestSetConfirmationMustTypeTheCount(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.mark("redis-cache", "shop-web-1")
	b.srv.ResetRequests()
	b.keys("t")
	d := b.planDialog("set stop")
	if d.plan.Tier != plan.TierTyped || d.plan.Word != "stop 2" || !d.hasInput {
		t.Fatalf("tier=%v word=%q input=%v", d.plan.Tier, d.plan.Word, d.hasInput)
	}
	b.keys("y", "enter")
	b.settle()
	b.noMutations("y and Enter on a set")
	for _, wrong := range []string{"stop", "stop 3", "stop  2", "STOP 2"} {
		b.m.dlg.input.SetValue("")
		b.typeText(wrong)
		b.keys("enter")
		b.settle()
		b.noMutations("typing " + wrong)
		if b.m.dlg == nil || !strings.Contains(b.screen(), `That is not "stop 2". Nothing was sent.`) {
			t.Fatalf("%q: the dialog must stay open and say so:\n%s", wrong, b.screen())
		}
	}
	b.m.dlg.input.SetValue("")
	b.typeText("stop 2")
	b.keys("enter")
	b.finishJobs()
	if n := len(b.srv.Mutations()); n != 2 {
		t.Fatalf("want 2 requests, got %d", n)
	}

	// A single object keeps its own wording and its `y`.
	b.selectRow("noisy-logger")
	b.keys("t")
	b.see("Stop container", "y proceed")
	if b.m.dlg.plan.Tier != plan.TierConfirm {
		t.Fatal("a single object's confirmation changed")
	}
	b.keys("esc")
	b.selectRow("old-migration")
	b.keys("d")
	b.see("Type remove and press Enter to proceed:")
	b.keys("esc")
}

func TestPartiallyFailingSetJobReportsSucceededAndFailed(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindVolumes)
	b.keys("a") // every volume, the four in use included
	b.see("2 Volumes (7) - 7 selected")
	b.keys("d")
	d := b.planDialog("set removal")
	if len(d.plan.Steps) != 3 || len(d.plan.Skipped) != 4 {
		t.Fatalf("steps=%d skipped=%d", len(d.plan.Steps), len(d.plan.Skipped))
	}
	b.srv.ResetRequests()
	b.srv.FailNext("DELETE", "^/volumes/old-backups$", 409, "volume is being backed up", 1)
	b.typeText("remove 3")
	b.keys("enter")
	b.finishJobs()
	b.see("✗ remove 3 volumes", "1 of 3 failed")
	if n := len(b.srv.Mutations()); n != 3 {
		t.Fatalf("the failure must not stop the rest: %d requests", n)
	}
	js := b.m.jobs.List()
	if len(js) != 1 || js[0].State != jobs.Failed {
		t.Fatalf("jobs: %+v", js)
	}
	b.keys("5", "enter")
	b.see("✗ remove volume old-backups — volume is being backed up", "✓ remove volume", "1 of 3 failed")
	names := strings.Join(b.srv.VolumeNames(), ",")
	for _, keep := range []string{"old-backups", "shop_pgdata", "shop_uploads", "blog_db"} {
		if !strings.Contains(names, keep) {
			t.Errorf("%s is gone", keep)
		}
	}
}

// TestSingleObjectActionsRefuseASet: shell, connect and disconnect pair the
// desk with one object; with a selection they say why they refuse and send
// nothing. Prune chooses its own objects and refuses as well; pull and
// create do not involve existing objects and are unaffected.
func TestSingleObjectActionsRefuseASet(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.mark("shop-api-1", "redis-cache")
	b.srv.ResetRequests()
	b.keys("e")
	b.settle()
	if b.m.dlg != nil || b.execs != 0 {
		t.Fatalf("shell with a selection opened something: dlg=%v execs=%d", b.m.dlg != nil, b.execs)
	}
	b.see("shell works on one object at a time and 2 are selected", "Nothing was sent")
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("shell with a selection sent %d request(s): %v", n, b.srv.Requests())
	}
	// The action menu offers only what applies to the selection.
	b.keys("enter")
	b.see("Actions: 2 selected", "stop", "remove")
	for _, a := range b.m.dlg.menu {
		if a.key == "e" {
			t.Fatal("the menu offers a shell for a selection")
		}
	}
	b.keys("esc")

	b.kind(KindNetworks)
	b.mark("orphan-net", "staging")
	for _, key := range []string{"c", "D", "X"} {
		b.srv.ResetRequests()
		b.keys(key)
		b.settle()
		if b.m.dlg != nil {
			t.Fatalf("%s with a selection opened a dialog", key)
		}
		if n := len(b.srv.Requests()); n != 0 {
			t.Fatalf("%s with a selection sent %d request(s)", key, n)
		}
	}
	b.see("prune chooses its own objects, not the 2 selected")
	b.keys("n")
	b.see("Create network")
	b.keys("esc")
	b.noMutations("single-object actions with a selection")
}

func TestReadOnlyRefusesASet(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.mark("redis-cache", "old-migration") // looking and selecting is allowed
	b.see("- 2 selected")
	for _, key := range []string{"t", "d", "K", "e", "s"} {
		b.srv.ResetRequests()
		b.keys(key)
		b.settle()
		if b.m.dlg != nil {
			t.Fatalf("%s opened a dialog in read-only mode", key)
		}
		b.see("Read-only mode")
		if n := len(b.srv.Requests()); n != 0 {
			t.Fatalf("%s sent %d request(s) in read-only mode: %v", key, n, b.srv.Requests())
		}
	}
	if len(b.m.jobs.List()) != 0 {
		t.Fatal("read-only mode started a job")
	}
}

// TestSelectAllEscAndSpaceElsewhere: `a` selects what the filter shows; Esc
// steps back one thing at a time; space is still page-down outside the list.
func TestSelectAllEscAndSpaceElsewhere(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.keys("/")
	b.typeText("shop-")
	b.keys("enter", "a")
	b.see("2 Containers (4 of 12) - 4 selected")
	for _, n := range b.markedNames() {
		if !strings.HasPrefix(n, "shop-") {
			t.Fatalf("a selected %q, which the filter hides", n)
		}
	}
	b.keys("esc")
	b.see("2 Containers (4 of 12)")
	b.notSee("selected")
	b.keys("esc")
	b.see("2 Containers (12)")
	if b.m.focus != PaneList {
		t.Fatal("Esc left the pane while there was a filter to clear")
	}
	b.keys("esc")
	if b.m.focus != PaneKinds {
		t.Fatalf("the third Esc should leave the list for Kinds, focus is %v", b.m.focus)
	}

	// Marked rows the filter hides are still in the selection, and listed.
	b.m.focus = PaneList
	b.mark("redis-cache", "shop-web-1")
	b.keys("/")
	b.typeText("redis")
	b.keys("enter", "t")
	b.planDialog("set stop")
	b.see("Exactly these 2 will be stopped", "shop-web-1")
	b.keys("esc", "esc", "esc")

	// Space in Details and Events pages; it does not select.
	b.m.focus = PaneList
	b.selectRow("noisy-logger")
	b.keys("3", " ")
	b.keys("4", " ")
	if n := len(b.m.marks[KindContainers]); n != 0 {
		t.Fatalf("space outside the list selected %d row(s)", n)
	}

	// Compose projects are not selected.
	b.kind(KindCompose)
	b.m.focus = PaneList
	b.keys(" ", "a")
	b.see("cannot be selected as a set")
	if len(b.m.marks[KindCompose]) != 0 {
		t.Fatal("a compose project was selected")
	}
}

// TestSetConfirmationFitsTheSmallestTerminal: at 80×24, with a long remote
// endpoint and a set longer than the screen, what is acted on, the daemon,
// the prompt and the input stay on screen; the listed objects — and those
// not included — scroll, and every one of them can be reached.
func TestSetConfirmationFitsTheSmallestTerminal(t *testing.T) {
	ep := dockerctx.Endpoint{
		Host:    "ssh://deploy@build-runner-17.eu-west-1.internal.example.com:2222/run/user/1000/docker.sock",
		Context: "production-eu-west-1-cluster-admin", Source: "test",
	}
	b := remoteBench(t, ep, benchOpts{width: 80, height: 24, seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		for i := 0; i < 25; i++ {
			s.AddVolume(fmt.Sprintf("scratch-%02d", i), nil)
		}
	}})
	b.kind(KindVolumes)
	b.m.focus = PaneList
	b.keys("a", "d")
	d := b.planDialog("set removal")
	n := len(d.plan.Steps)
	if n != 28 || len(d.plan.Skipped) != 4 {
		t.Fatalf("steps=%d skipped=%d", n, len(d.plan.Skipped))
	}
	prompt := fmt.Sprintf("Type remove %d to confirm", n)
	check := func() {
		t.Helper()
		screen := b.screen()
		for _, w := range []string{"Remove volumes", "selected", prompt, "Enter proceed", "Esc cancel"} {
			if !strings.Contains(screen, w) {
				t.Fatalf("%q is not on screen at 80×24:\n%s", w, screen)
			}
		}
		inner := clamp(b.m.width-4, 40, dialogMaxWidth) - 4
		for _, line := range b.m.daemonLines(inner, true) {
			if text := strings.TrimSpace(ansi.Strip(line)); !strings.Contains(screen, text) {
				t.Fatalf("daemon line %q is not on screen:\n%s", text, screen)
			}
		}
		if _, _, h := b.m.renderDialog(); h > b.m.height-1 {
			t.Fatalf("the dialog is %d rows high on a %d-row terminal", h, b.m.height)
		}
		b.assertFrameGeometry()
	}
	check()
	b.typeText("remove")
	if !strings.Contains(b.screen(), "> remove") {
		t.Fatalf("the typed text is not visible:\n%s", b.screen())
	}
	seen := map[string]bool{}
	for i := 0; i < 80; i++ {
		for _, line := range strings.Split(b.screen(), "\n") {
			seen[line] = true
		}
		b.keys("down")
	}
	check()
	reachable := func(s string) bool {
		for line := range seen {
			if strings.Contains(line, s) {
				return true
			}
		}
		return false
	}
	for _, st := range d.plan.Steps {
		if !reachable(st.Name) {
			t.Fatalf("%q is in the set but cannot be scrolled to", st.Name)
		}
	}
	for _, sk := range d.plan.Skipped {
		if !reachable(sk.Name) {
			t.Fatalf("%q is not included but is never named", sk.Name)
		}
	}
	if !reachable("Not included (4)") {
		t.Fatal("the not-included heading cannot be scrolled to")
	}
	b.noMutations("scrolling a set confirmation")
}
