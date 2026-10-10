package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

// mutation is one mutating action reachable from a key: where it lives, the
// object it is aimed at, how it is confirmed and what must reach the daemon.
type mutation struct {
	name    string
	kind    Kind
	row     string // row to highlight ("" = none needed)
	key     string
	tier    plan.Tier
	word    string
	title   string // dialog heading
	wantReq string // "METHOD path-suffix" of the single request expected
	// pick is the container to choose in the picker (connect/disconnect).
	pick string
}

func mutations(srv *fakeengine.Server) []mutation {
	cid := func(n string) string { return srv.ContainerID(n) }
	return []mutation{
		{name: "start", kind: KindContainers, row: "old-migration", key: "s", tier: plan.TierConfirm, title: "Start container", wantReq: "POST /containers/" + cid("old-migration") + "/start"},
		{name: "stop", kind: KindContainers, row: "redis-cache", key: "t", tier: plan.TierConfirm, title: "Stop container", wantReq: "POST /containers/" + cid("redis-cache") + "/stop"},
		{name: "restart", kind: KindContainers, row: "redis-cache", key: "R", tier: plan.TierConfirm, title: "Restart container", wantReq: "POST /containers/" + cid("redis-cache") + "/restart"},
		{name: "pause", kind: KindContainers, row: "redis-cache", key: "p", tier: plan.TierConfirm, title: "Pause container", wantReq: "POST /containers/" + cid("redis-cache") + "/pause"},
		{name: "unpause", kind: KindContainers, row: "paused-batch", key: "u", tier: plan.TierConfirm, title: "Unpause container", wantReq: "POST /containers/" + cid("paused-batch") + "/unpause"},
		{name: "kill", kind: KindContainers, row: "redis-cache", key: "K", tier: plan.TierConfirm, title: "Kill container", wantReq: "POST /containers/" + cid("redis-cache") + "/kill"},
		{name: "remove container", kind: KindContainers, row: "old-migration", key: "d", tier: plan.TierTyped, word: "remove", title: "Remove container", wantReq: "DELETE /containers/" + cid("old-migration")},
		{name: "remove image", kind: KindImages, row: "python:3.12-slim", key: "d", tier: plan.TierTyped, word: "remove", title: "Remove image", wantReq: "DELETE /images/python:3.12-slim"},
		{name: "untag image", kind: KindImages, row: "node:lts-alpine", key: "d", tier: plan.TierTyped, word: "remove", title: "Untag image", wantReq: "DELETE /images/node:lts-alpine"},
		{name: "remove volume", kind: KindVolumes, row: "old-backups", key: "d", tier: plan.TierTyped, word: "remove", title: "Remove volume", wantReq: "DELETE /volumes/old-backups"},
		{name: "remove network", kind: KindNetworks, row: "orphan-net", key: "d", tier: plan.TierTyped, word: "remove", title: "Remove network", wantReq: "DELETE /networks/" + srv.NetworkID("orphan-net")},
		{name: "connect", kind: KindNetworks, row: "orphan-net", key: "c", pick: "redis-cache", tier: plan.TierConfirm, title: "Connect container to network", wantReq: "POST /networks/" + srv.NetworkID("orphan-net") + "/connect"},
		{name: "disconnect", kind: KindNetworks, row: "staging", key: "D", pick: "redis-cache", tier: plan.TierConfirm, title: "Disconnect container from network", wantReq: "POST /networks/" + srv.NetworkID("staging") + "/disconnect"},
		{name: "stop project", kind: KindCompose, row: "shop", key: "t", tier: plan.TierConfirm, title: "Stop compose project", wantReq: "POST 3×/stop"},
		{name: "remove project", kind: KindCompose, row: "blog", key: "d", tier: plan.TierTyped, word: "remove", title: "Remove compose project", wantReq: "DELETE 2×/containers/"},
		{name: "prune images", kind: KindImages, key: "X", tier: plan.TierTyped, word: "prune", title: "Prune images", wantReq: "DELETE 2×/images/"},
		{name: "prune volumes", kind: KindVolumes, key: "X", tier: plan.TierTyped, word: "prune", title: "Prune volumes", wantReq: "DELETE 2×/volumes/"},
		{name: "prune networks", kind: KindNetworks, key: "X", tier: plan.TierTyped, word: "prune", title: "Prune networks", wantReq: "DELETE 1×/networks/"},
	}
}

func (b *bench) noMutations(when string) {
	b.t.Helper()
	if muts := b.srv.Mutations(); len(muts) != 0 {
		b.t.Fatalf("%s: %d mutating request(s) reached the daemon: %v", when, len(muts), muts)
	}
}

func (b *bench) finishJobs() {
	b.t.Helper()
	b.until("jobs to finish", func() bool { return b.m.jobs.RunningCount() == 0 })
	b.settle()
}

// open brings the desk to the confirmation dialog of a mutation, pressing
// only the action key (and choosing a container in the picker when the
// action has one).
func (b *bench) open(mu mutation) {
	b.t.Helper()
	b.m.focus = PaneList
	b.kind(mu.kind)
	if mu.row != "" {
		b.selectRow(mu.row)
	}
	b.srv.ResetRequests()
	b.keys(mu.key)
	if mu.pick != "" {
		b.until("picker", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPicker })
		b.noMutations("after the action key (picker open)")
		found := false
		for i, it := range b.m.dlg.items {
			if it.label == mu.pick {
				b.m.dlg.pickSel, found = i, true
			}
		}
		if !found {
			b.t.Fatalf("%s: %q is not offered by the picker: %+v", mu.name, mu.pick, b.m.dlg.items)
		}
		b.keys("enter")
	}
	b.until("confirmation for "+mu.name, func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
}

// TestNoSingleKeystrokeMutates is the central safety property. For every
// mutating action the desk has: pressing its key sends nothing and opens a
// confirmation that names the object, the effect and the daemon; declining
// sends nothing and leaves no job; Enter alone never consents to a state
// change; a mistyped word never proceeds; and the deliberate input sends
// exactly the request that was announced.
func TestNoSingleKeystrokeMutates(t *testing.T) {
	probe := fakeengine.New()
	probe.SeedDemo()
	all := mutations(probe)

	for i := range all {
		name := all[i].name
		t.Run(name, func(t *testing.T) {
			b := newBench(t, benchOpts{})
			mu := mutations(b.srv)[i] // IDs of this bench's daemon

			// 1. The action key alone.
			b.open(mu)
			b.noMutations("after the action key")
			d := b.m.dlg
			if d.plan.Title != mu.title || d.plan.Tier != mu.tier || !d.plan.Runnable() {
				t.Fatalf("dialog: title=%q tier=%v runnable=%v blockers=%v", d.plan.Title, d.plan.Tier, d.plan.Runnable(), d.plan.Blockers)
			}
			screen := b.screen()
			for _, want := range []string{mu.title, "daemon", b.srv.Host()} {
				if !strings.Contains(screen, want) {
					t.Fatalf("the confirmation does not show %q:\n%s", want, screen)
				}
			}
			if mu.row != "" && !strings.Contains(screen, mu.row) {
				t.Fatalf("the confirmation does not name %q:\n%s", mu.row, screen)
			}
			if len(d.plan.Effects) == 0 {
				t.Fatal("the confirmation states no effect")
			}

			// 2. Declining.
			b.keys("esc")
			if b.m.dlg != nil {
				t.Fatal("Esc did not close the confirmation")
			}
			b.settle()
			b.noMutations("after declining")
			if n := len(b.m.jobs.List()); n != 0 {
				t.Fatalf("declining left %d job(s)", n)
			}

			// 3. Input that is not consent.
			b.open(mu)
			if mu.tier == plan.TierConfirm {
				b.keys("enter", " ", "Y", "x", "tab")
				b.settle()
				b.noMutations("after Enter/space/other keys in a `y` confirmation")
				if b.m.dlg == nil {
					t.Fatal("a key other than y/n/Esc closed the confirmation")
				}
				b.keys("n")
				if b.m.dlg != nil {
					t.Fatal("n did not close the confirmation")
				}
			} else {
				b.keys("enter") // nothing typed
				b.settle()
				b.noMutations("after Enter on an empty typed confirmation")
				b.typeText(mu.word[:len(mu.word)-1])
				b.keys("enter")
				b.typeText("y")
				b.keys("enter")
				b.settle()
				b.noMutations("after a mistyped word")
				if b.m.dlg == nil || !strings.Contains(b.screen(), "Nothing was sent") {
					t.Fatalf("a mistyped word must keep the dialog open and say so:\n%s", b.screen())
				}
				b.keys("esc")
			}
			b.settle()
			b.noMutations("after non-consenting input")

			// 4. The deliberate input.
			b.open(mu)
			if mu.tier == plan.TierConfirm {
				b.keys("y")
			} else {
				b.typeText(mu.word)
				b.keys("enter")
			}
			if b.m.dlg != nil {
				t.Fatal("the confirmation stayed open after consent")
			}
			b.finishJobs()
			muts := b.srv.Mutations()
			var method, suffix string
			count := 1
			if n, err := fmt.Sscanf(mu.wantReq, "%s %d×%s", &method, &count, &suffix); n != 3 || err != nil {
				parts := strings.SplitN(mu.wantReq, " ", 2)
				method, suffix, count = parts[0], parts[1], 1
			}
			if len(muts) != count {
				t.Fatalf("want %d request(s) %s …%s, got %v", count, method, suffix, muts)
			}
			for _, r := range muts {
				ok := r.Method == method && (r.Path == suffix || strings.Contains(r.Path, suffix))
				if !ok {
					t.Fatalf("unexpected request %s (want %s …%s)", r, method, suffix)
				}
				if strings.Contains(strings.ToLower(r.Query), "force") {
					t.Fatalf("forced request: %s", r)
				}
			}
			js := b.m.jobs.List()
			if len(js) != 1 || js[0].State != jobs.Succeeded {
				t.Fatalf("job: %+v", js)
			}
		})
	}
}

// TestEveryActionKeyIsCovered makes the table above exhaustive: an action
// added to the desk without a row there fails this test.
func TestEveryActionKeyIsCovered(t *testing.T) {
	probe := fakeengine.New()
	probe.SeedDemo()
	covered := map[string]bool{}
	for _, mu := range mutations(probe) {
		covered[fmt.Sprintf("%d/%s", mu.kind, mu.key)] = true
	}
	// Form-tier actions are covered by TestForms and the shell tests.
	forms := map[string]bool{
		fmt.Sprintf("%d/P", KindImages): true, fmt.Sprintf("%d/n", KindVolumes): true,
		fmt.Sprintf("%d/n", KindNetworks): true, fmt.Sprintf("%d/e", KindContainers): true,
	}
	for k := Kind(0); k < kindCount; k++ {
		for _, a := range kindActions[k] {
			id := fmt.Sprintf("%d/%s", k, a.key)
			if !a.mutating {
				t.Errorf("%s %q is not marked mutating: read-only mode would let it through", kindNames[k], a.label)
			}
			if !covered[id] && !forms[id] {
				t.Errorf("%s action %q (key %s) has no safety test", kindNames[k], a.label, a.key)
			}
		}
	}
}

func TestBlockedDeletionsCannotBeConfirmed(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		row  string
		want []string
	}{
		{"running container", KindContainers, "redis-cache", []string{"REFUSED", "redis-cache is running", "never force-removes"}},
		{"paused container", KindContainers, "paused-batch", []string{"REFUSED", "paused-batch is paused"}},
		{"image used by a running container", KindImages, "nginx:1.27", []string{"REFUSED", "In use by 1 container", "shop-web-1 (running)"}},
		{"image used only by a stopped container", KindImages, "ghost:5", []string{"REFUSED", "blog-app-1 (exited)"}},
		{"volume in use", KindVolumes, "shop_pgdata", []string{"REFUSED", "shop-db-1 (running)"}},
		{"volume of a stopped container", KindVolumes, "blog_db", []string{"REFUSED", "blog-db-1 (exited)"}},
		{"network with containers", KindNetworks, "shop_default", []string{"REFUSED", "Used by 4 containers"}},
		{"network of stopped containers only", KindNetworks, "blog_default", []string{"REFUSED", "Stopped containers count"}},
		{"predefined network", KindNetworks, "bridge", []string{"REFUSED", "predefined network"}},
		{"running compose project", KindCompose, "shop", []string{"REFUSED", "3 containers are still running", "stop the project first"}},
	}
	b := newBench(t, benchOpts{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b.m.focus = PaneList
			b.kind(tc.kind)
			b.selectRow(tc.row)
			b.srv.ResetRequests()
			b.keys("d")
			b.until("dialog", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
			screen := b.screen()
			for _, w := range tc.want {
				if !strings.Contains(screen, w) {
					t.Errorf("the refusal does not say %q:\n%s", w, screen)
				}
			}
			if b.m.dlg.hasInput {
				t.Error("a refused deletion offers an input to type into")
			}
			// Nothing the operator can press proceeds.
			b.typeText("remove")
			b.settle()
			if b.m.dlg != nil { // letters such as n/q close a refusal; reopen
				b.keys("y", " ", "tab")
			}
			if b.m.dlg == nil {
				b.keys("d")
				b.until("dialog", func() bool { return b.m.dlg != nil })
			}
			b.keys("y")
			b.keys("enter")
			b.settle()
			b.noMutations("after trying every key on a refused deletion")
			if n := len(b.m.jobs.List()); n != 0 {
				t.Errorf("a refused deletion started %d job(s)", n)
			}
			if b.m.dlg != nil {
				b.keys("esc")
			}
		})
	}
}

// TestConfirmationIsBoundToTheObjectItShowed: the list is reshuffled under
// an open confirmation; confirming still acts on the object the dialog named.
func TestConfirmationIsBoundToTheObjectItShowed(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	target := b.srv.ContainerID("redis-cache")
	b.keys("t")
	b.see("Stop container", "redis-cache")

	// While the dialog is open the world changes: containers sorted before
	// the highlighted row appear and disappear, so a different object ends
	// up under the highlight's old position.
	idxBefore := b.m.sel[KindContainers].idx
	b.srv.AddContainer(fakeengine.ContainerSpec{Name: "aaa-newcomer", Image: "alpine:3.20"})
	b.srv.AddContainer(fakeengine.ContainerSpec{Name: "aab-newcomer", Image: "alpine:3.20"})
	b.srv.Emit("container", "create", "x", map[string]string{"name": "aaa-newcomer"})
	b.until("list to pick up the newcomers", func() bool { return len(b.m.snap.Containers) == 14 })
	b.settle()
	if got := b.m.rows[KindContainers][idxBefore].name; got == "redis-cache" {
		t.Fatalf("test setup: the row at the old position is still redis-cache")
	}
	// The highlight itself followed the object…
	if r, _ := b.m.currentRow(); r.name != "redis-cache" {
		t.Fatalf("highlight moved to %q", r.name)
	}
	// …and even if it had not, the dialog holds the identifier.
	b.m.selectIndex(0)
	b.srv.ResetRequests()
	b.keys("y")
	b.finishJobs()
	muts := b.srv.Mutations()
	if len(muts) != 1 || muts[0].Path != "/containers/"+target+"/stop" {
		t.Fatalf("confirming acted on something else: %v (want stop of %s)", muts, target)
	}
	if st, _ := b.srv.ContainerState("aaa-newcomer"); st != "running" {
		t.Fatal("the container that slid under the highlight was stopped")
	}
}

func TestPruneShowsTheExactSetAndDestroysOnlyThat(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.kind(KindVolumes)
	b.keys("X")
	b.see("Prune volumes", "Exactly these 2 will be deleted", "(anonymous)", "anonymous volumes no container references")
	listed := []string{b.m.dlg.plan.Steps[0].ID, b.m.dlg.plan.Steps[1].ID}

	// Scope toggle recomputes the preview; named volumes come with a warning.
	b.keys("tab")
	b.see("Exactly these 3 will be deleted", "old-backups", "named volume", "Named volumes usually hold data")
	b.keys("tab")
	b.see("Exactly these 2 will be deleted")

	// A new eligible volume appears after the preview was shown.
	late := b.srv.AddAnonymousVolume("appeared-after-the-preview")
	b.srv.ResetRequests()
	b.typeText("prune")
	b.keys("enter")
	b.finishJobs()

	names := strings.Join(b.srv.VolumeNames(), ",")
	if !strings.Contains(names, late) {
		t.Fatal("a volume that was never listed was deleted")
	}
	for _, l := range listed {
		if strings.Contains(names, l) {
			t.Fatalf("listed volume %s survived", l)
		}
	}
	if muts := b.srv.Mutations(); len(muts) != 2 {
		t.Fatalf("want exactly the 2 listed deletions, got %v", muts)
	}
	b.see("✓ prune volumes 2 volumes")

	// Nothing eligible: the dialog says so and offers nothing to confirm.
	b.kind(KindNetworks)
	b.keys("X")
	b.see("Prune networks", "orphan-net")
	b.typeText("prune")
	b.keys("enter")
	b.finishJobs()
	b.until("network list refresh", func() bool { return len(b.m.snap.Networks) == 6 })
	b.srv.ResetRequests()
	b.keys("X")
	b.see("Nothing to prune")
	if b.m.dlg.hasInput || b.m.dlg.plan.Runnable() {
		t.Fatal("an empty prune can be confirmed")
	}
	b.typeText("prune")
	b.keys("enter")
	b.settle()
	b.noMutations("after confirming an empty prune")
	// blog_default is used only by stopped containers and must still exist.
	if !strings.Contains(strings.Join(b.srv.NetworkNames(), ","), "blog_default") {
		t.Fatal("a network referenced only by stopped containers was pruned")
	}
}

func TestPrunePreviewScrolls(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		for i := 0; i < 60; i++ {
			s.AddVolume(fmt.Sprintf("scratch-%02d", i), nil)
		}
	}, width: 100, height: 30})
	b.m.focus = PaneList
	b.kind(KindVolumes)
	b.keys("X", "tab")
	b.see("Exactly these 60 will be deleted", "scratch-00", "rows 1–")
	if strings.Contains(b.screen(), "scratch-59") {
		t.Fatal("60 rows cannot fit; the list must be windowed")
	}
	for i := 0; i < 10; i++ {
		b.keys("pgdown")
	}
	b.see("scratch-59", "of 60")
	b.assertFrameGeometry()
}

func TestReadOnlyDisablesEveryAction(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.see("[READ-ONLY]")
	for k := Kind(0); k < kindCount; k++ {
		b.m.focus = PaneList
		b.kind(k)
		for _, a := range kindActions[k] {
			b.srv.ResetRequests()
			b.keys(a.key)
			b.settle()
			if b.m.dlg != nil {
				t.Errorf("%s %q opened a dialog in read-only mode", kindNames[k], a.label)
				b.keys("esc")
			}
			if !strings.Contains(b.screen(), "Read-only mode") {
				t.Errorf("%s %q: the desk does not say it is read-only:\n%s", kindNames[k], a.label, b.screen())
			}
			if n := len(b.srv.Requests()); n != 0 {
				t.Errorf("%s %q sent %d request(s) in read-only mode: %v", kindNames[k], a.label, n, b.srv.Requests())
			}
		}
	}
	if b.execs != 0 || len(b.m.jobs.List()) != 0 {
		t.Fatalf("read-only mode started %d shell(s) and %d job(s)", b.execs, len(b.m.jobs.List()))
	}
	// Observation is untouched.
	b.kind(KindContainers)
	b.selectRow("shop-web-1")
	b.keys("]")
	b.see("[Logs]", "nginx/1.27.0 started", "● following")
}

func TestActionThatDoesNotApplyOpensNoDialog(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	cases := []struct{ row, key, want string }{
		{"redis-cache", "s", "redis-cache is already running"},
		{"old-migration", "t", "old-migration is not running"},
		{"old-migration", "p", "old-migration is not running"},
		{"redis-cache", "u", "redis-cache is not paused"},
		{"old-migration", "K", "old-migration is not running"},
		{"paused-batch", "s", "unpause it instead"},
		{"old-migration", "e", "is not running"},
	}
	for _, tc := range cases {
		b.selectRow(tc.row)
		b.srv.ResetRequests()
		b.keys(tc.key)
		b.settle()
		if b.m.dlg != nil {
			t.Errorf("%s on %s opened a dialog", tc.key, tc.row)
			b.keys("esc")
		}
		if !strings.Contains(b.screen(), tc.want) {
			t.Errorf("%s on %s: want the desk to say %q:\n%s", tc.key, tc.row, tc.want, b.screen())
		}
		b.noMutations(tc.key + " on " + tc.row)
	}
	// Compose: nothing to stop.
	b.kind(KindCompose)
	b.selectRow("blog")
	b.keys("t")
	b.see("nothing to stop")
	if b.m.dlg != nil {
		t.Fatal("stopping a stopped project opened a dialog")
	}
}

// TestAnswersToAbandonedRequestsDoNothing covers the gaps between asking the
// daemon for its current state and getting the answer. Whatever the operator
// does in that gap, a late answer must never submit a form they cancelled,
// replace a dialog under their fingers, or let them confirm a preview that
// is about to change.
func TestAnswersToAbandonedRequestsDoNothing(t *testing.T) {
	// step delivers a key WITHOUT letting any pending answer arrive first.
	t.Run("a cancelled form is not submitted by its late check", func(t *testing.T) {
		b := newBench(t, benchOpts{})
		b.m.focus = PaneList
		b.kind(KindVolumes)
		b.keys("n")
		b.typeText("late-volume")
		b.srv.ResetRequests()
		b.step(keyMsg("enter")) // the name check is now in flight
		if !b.m.dlg.formBusy {
			t.Fatal("test setup: the form is not waiting for its check")
		}
		b.step(keyMsg("esc")) // …and the operator backs out
		b.run(300 * time.Millisecond)
		b.noMutations("after cancelling a form whose check was in flight")
		if n := len(b.m.jobs.List()); n != 0 || b.m.dlg != nil {
			t.Fatalf("a cancelled form produced %d job(s), dialog=%v", n, b.m.dlg != nil)
		}
		if b.m.preparing {
			t.Fatal("the desk is stuck waiting for an abandoned request")
		}
		// The desk is not jammed: the same form works afterwards.
		b.keys("n")
		b.typeText("late-volume")
		b.keys("enter")
		b.finishJobs()
		b.see("✓ create volume late-volume")
	})

	t.Run("a preview being recomputed cannot be confirmed", func(t *testing.T) {
		b := newBench(t, benchOpts{})
		b.m.focus = PaneList
		b.kind(KindVolumes)
		b.keys("X")
		b.see("Exactly these 2 will be deleted")
		b.srv.ResetRequests()
		b.step(keyMsg("tab")) // recompute for the other scope: in flight
		for _, r := range "prune" {
			b.step(keyMsg(string(r)))
		}
		b.step(keyMsg("enter"))
		b.noMutations("typing the word while the preview was being recomputed")
		if len(b.m.jobs.List()) != 0 {
			t.Fatal("a job started from a preview that was being replaced")
		}
		// The new preview arrives, with an empty input.
		b.see("Exactly these 3 will be deleted")
		if b.m.dlg.input.Value() != "" {
			t.Fatalf("typed text carried over into the new preview: %q", b.m.dlg.input.Value())
		}
		// Esc during a recompute abandons it: nothing pops up later.
		b.step(keyMsg("tab"))
		b.step(keyMsg("esc"))
		b.run(300 * time.Millisecond)
		if b.m.dlg != nil || b.m.preparing {
			t.Fatal("an abandoned recompute opened a dialog or left the desk waiting")
		}
		b.noMutations("after abandoning a recompute")
	})

	t.Run("a late confirmation does not replace another dialog", func(t *testing.T) {
		b := newBench(t, benchOpts{})
		b.m.focus = PaneList
		b.selectRow("redis-cache")
		b.srv.ResetRequests()
		b.step(keyMsg("t")) // plan in flight
		b.step(keyMsg("?")) // the operator opens help instead
		b.run(300 * time.Millisecond)
		if b.m.dlg == nil || b.m.dlg.kind != dlgHelp {
			t.Fatal("a late confirmation replaced the help overlay")
		}
		b.keys("esc")
		if b.m.dlg != nil {
			t.Fatal("a confirmation was waiting behind the help overlay")
		}
		b.noMutations("after the plan was dropped")
		// Two action keys in a row: the first is dropped, one dialog opens —
		// for the key pressed last.
		b.step(keyMsg("t"))
		b.step(keyMsg("K"))
		b.see("Kill container")
		b.keys("esc")
		b.run(200 * time.Millisecond)
		if b.m.dlg != nil {
			t.Fatal("a second dialog appeared for the key that had been superseded")
		}
		b.noMutations("after two action keys in a row")
	})

	t.Run("keys typed for something else cannot confirm a dialog that opens late", func(t *testing.T) {
		b := newBench(t, benchOpts{})
		b.m.focus = PaneList
		b.selectRow("redis-cache")
		b.srv.ResetRequests()
		// `t` asks for a stop confirmation; before it arrives the operator
		// starts a filter and types a word containing "y".
		b.step(keyMsg("t"))
		for _, k := range []string{"/", "m", "y", "s", "q", "l"} {
			b.step(keyMsg(k))
		}
		b.run(300 * time.Millisecond)
		if b.m.dlg != nil {
			t.Fatal("the confirmation opened under the filter")
		}
		if !b.m.filtering || b.m.filterIn.Value() != "mysql" {
			t.Fatalf("the filter did not receive the typed text: filtering=%v value=%q", b.m.filtering, b.m.filterIn.Value())
		}
		b.noMutations("after typing into the filter while a confirmation was pending")
		if st, _ := b.srv.ContainerState("redis-cache"); st != "running" {
			t.Fatalf("redis-cache is %s", st)
		}
		b.keys("esc")

		// The same with the picker: `c` then typing must not pick anything.
		b.kind(KindNetworks)
		b.selectRow("orphan-net")
		b.step(keyMsg("c"))
		for _, k := range []string{"/", "x", "enter"} {
			b.step(keyMsg(k))
		}
		b.run(300 * time.Millisecond)
		if b.m.dlg != nil {
			t.Fatal("the picker opened under the filter")
		}
		b.noMutations("after typing while a picker was pending")

		// And type-ahead: `t` immediately followed by `y`.
		b.keys("esc")
		b.kind(KindContainers)
		b.selectRow("redis-cache")
		b.step(keyMsg("t"))
		b.step(keyMsg("y"))
		b.run(300 * time.Millisecond)
		b.noMutations("after t,y typed ahead of the dialog")
		if b.m.dlg != nil {
			t.Fatal("a dialog opened although a key had been pressed while it was pending")
		}
	})
}

// TestConfirmationsMustBeReadFirst: a dialog that has only just appeared
// accepts nothing that proceeds. What was typed before it could be read does
// not count — not `y`, not the confirmation word, not Enter on a pre-filled
// shell command.
func TestConfirmationsMustBeReadFirst(t *testing.T) {
	const arm = 150 * time.Millisecond
	b := newBench(t, benchOpts{arm: arm})
	b.m.focus = PaneList

	// `y` tier.
	b.selectRow("redis-cache")
	b.srv.ResetRequests()
	b.keys("t")
	b.see("Stop container")
	b.keys("y", "y", "enter")
	b.settle()
	b.noMutations("y pressed before the confirmation could have been read")
	if b.m.dlg == nil {
		t.Fatal("the dialog closed on an early key")
	}
	b.run(arm)
	b.keys("y")
	b.finishJobs()
	if n := len(b.srv.Mutations()); n != 1 {
		t.Fatalf("after the dialog was armed, y must proceed: %d requests", n)
	}

	// Typed tier: text typed early is not kept.
	b.selectRow("old-migration")
	b.srv.ResetRequests()
	b.keys("d")
	b.see("Remove container")
	b.typeText("remove")
	b.keys("enter")
	b.settle()
	b.noMutations("the word typed before the confirmation could have been read")
	if got := b.m.dlg.input.Value(); got != "" {
		t.Fatalf("early keystrokes were kept in the input: %q", got)
	}
	b.run(arm)
	b.typeText("remove")
	b.keys("enter")
	b.finishJobs()
	if n := len(b.srv.Mutations()); n != 1 {
		t.Fatalf("after the dialog was armed, the word must proceed: %d requests", n)
	}

	// The shell form is pre-filled: Enter typed ahead must not run it.
	b.selectRow("shop-api-1")
	b.keys("e")
	b.see("Open a shell")
	b.keys("enter")
	b.settle()
	if b.execs != 0 {
		t.Fatal("Enter typed ahead of the shell form handed the terminal over")
	}
	b.run(arm)
	b.keys("enter")
	b.until("hand-off", func() bool { return b.execs == 1 })

	// Declining is never delayed.
	b.selectRow("shop-web-1")
	b.keys("t")
	b.see("Stop container")
	b.keys("esc")
	if b.m.dlg != nil {
		t.Fatal("Esc was delayed by arming")
	}
}

// TestNothingActsWhileTheDeskCannotBeDrawn: below the minimum size the desk
// shows only a message. No key may act on, or confirm, what is not on screen.
func TestNothingActsWhileTheDeskCannotBeDrawn(t *testing.T) {
	b := newBench(t, benchOpts{width: 60, height: 20})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.srv.ResetRequests()
	for _, k := range []string{"t", "y", "d", "r", "e", "m", "o", "v", "e", "enter", "K", "y", "X", "tab", "1", "/"} {
		b.keys(k)
	}
	b.settle()
	b.noMutations("keys pressed while the terminal was too small")
	if b.m.dlg != nil || b.m.filtering || b.m.focus != PaneList || b.m.preparing {
		t.Fatalf("keys changed state while nothing could be drawn: dlg=%v filtering=%v focus=%v", b.m.dlg != nil, b.m.filtering, b.m.focus)
	}
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("%d request(s) were sent: %v", n, b.srv.Requests())
	}

	// A dialog open when the window shrinks cannot be confirmed blind…
	b.step(tea.WindowSizeMsg{Width: 140, Height: 40})
	b.keys("t")
	b.see("Stop container", "redis-cache")
	b.step(tea.WindowSizeMsg{Width: 60, Height: 20})
	b.notSee("Stop container")
	b.keys("y")
	b.settle()
	b.noMutations("confirming a dialog that is not on screen")
	// …and is there again, still unconfirmed, when the window grows back.
	b.step(tea.WindowSizeMsg{Width: 140, Height: 40})
	b.see("Stop container", "redis-cache")
	b.keys("esc")

	// q still quits.
	b.step(tea.WindowSizeMsg{Width: 60, Height: 20})
	b.keys("q")
	b.until("quit", func() bool { return b.quit })
}

// TestActionMenuActsOnTheObjectItNames: the menu's title names an object;
// what is chosen from it applies to that object even if the highlight has
// moved to another one in the meantime.
func TestActionMenuActsOnTheObjectItNames(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.keys("enter")
	b.see("Actions: redis-cache")

	// The highlight moves under the open menu.
	b.m.selectIndex(0)
	if r, _ := b.m.currentRow(); r.name == "redis-cache" {
		t.Fatal("test setup: the highlight did not move")
	}
	b.keys("t")
	b.see("Stop container", "redis-cache")
	if got := b.m.dlg.plan.Steps[0].ID; got != b.srv.ContainerID("redis-cache") {
		t.Fatalf("the menu acted on %s, not on the object it named", got)
	}
	b.keys("esc")

	// The named object disappears while the menu is open: nothing else is
	// acted on in its place.
	b.selectRow("old-migration")
	b.keys("enter")
	b.see("Actions: old-migration")
	if err := b.eng.RemoveContainer(context.Background(), b.srv.ContainerID("old-migration")); err != nil {
		t.Fatal(err)
	}
	b.until("list refresh", func() bool { return len(b.m.snap.Containers) == 11 })
	b.srv.ResetRequests()
	b.keys("d")
	b.see("that container no longer exists")
	if b.m.dlg != nil {
		t.Fatal("a dialog opened for a different object than the menu named")
	}
	b.noMutations("choosing from a menu whose object is gone")
}
