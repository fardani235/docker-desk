package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func buildScopesSeed(s *fakeengine.Server) { s.SeedDemo(); s.SeedBuildCacheScopes() }

func (b *bench) buildCacheDialog() {
	b.t.Helper()
	b.until("the build-cache confirmation", func() bool {
		return b.m.dlg != nil && b.m.dlg.kind == dlgPlan && b.m.dlg.plan.BuildCache != nil && !b.m.dlg.recomputing
	})
}

// b reads the account (and nothing else) and opens the preview: every
// candidate, never an in-use record, and never a claim that the set is
// exact.
func TestBuildCacheKeyReadsOnlyTheAccount(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, width: 140, height: 50})
	b.openSpace()
	b.see("b build cache…")
	b.srv.ResetRequests()
	b.keys("b")
	b.buildCacheDialog()
	// The desk's own background reads (details) may interleave; b adds
	// exactly one account read and nothing that changes the daemon.
	if n := b.srv.CountRequests("GET", `^/system/df$`); n != 1 {
		t.Fatalf("b read the account %d times, want 1: %v", n, b.srv.Requests())
	}
	b.noMutations("b")
	b.seeFlat("Prune build cache", "These 4 were unused at the last account; the daemon decides the final set:",
		"[worker 3/4] RUN pip install -r requirements.txt", "mount / from exec /bin/sh -c npm ci --omit=dev",
		"[api 4/5] RUN npm ci --omit=dev", "local source for context", "shared",
		"the daemon decides the final set", "may also remove", "1 record in use at the account is kept",
		"the daemon's estimate", "Tab switch to all unused", "Type prune")
	text := b.dialogText()
	if strings.Contains(text, "Exactly these") || strings.Contains(text, "[api 5/5] COPY . .") {
		t.Fatalf("the preview claims an exact set or lists an in-use record:\n%s", text)
	}
	b.noMutations("opening the build-cache confirmation")
}

// Tab reads the account again for the other scope, clears the typed word,
// and confirms nothing until the new preview is drawn.
func TestBuildCacheTabSwitchesScope(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, width: 140, height: 50})
	b.openSpace()
	b.srv.ResetRequests()
	b.srv.Stall("GET", `^/system/df$`, 300*time.Millisecond)
	b.keys("b")
	b.buildCacheDialog()
	b.typeText("pru")
	// Step the keys directly: keys() would wait for the new preview.
	b.step(keyMsg("tab"))
	if !b.m.dlg.recomputing {
		t.Fatal("Tab did not start recomputing")
	}
	if !strings.Contains(b.screen(), "recomputing the list for the other scope") {
		t.Fatalf("no recomputing notice:\n%s", b.screen())
	}
	// While recomputing nothing confirms, even the whole word.
	for _, k := range []string{"n", "e", "enter"} {
		b.step(keyMsg(k))
	}
	b.noMutations("typing while the scope was recomputed")
	b.buildCacheDialog()
	b.srv.ClearFailures()
	if !b.m.dlg.scopeAll || !b.m.dlg.plan.BuildCache.All || b.m.dlg.input.Value() != "" {
		t.Fatalf("scope %v, typed %q", b.m.dlg.scopeAll, b.m.dlg.input.Value())
	}
	if n := b.srv.CountRequests("GET", `^/system/df$`); n != 2 {
		t.Fatalf("account read %d times, want 2", n)
	}
	b.seeFlat("These 6 were unused at the last account", "local source for dockerfile", "docker/dockerfile:1", "Tab switch to the default scope")
	b.noMutations("switching scope")
}

// The whole path: one POST with the chosen scope, the daemon's report in the
// job, and a fresh account once the job ends.
func TestBuildCachePruneEndToEnd(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, width: 140, height: 50})
	b.openSpace()
	before := b.m.sp.duAt
	b.keys("b")
	b.buildCacheDialog()
	b.typeText("purge")
	b.keys("enter")
	b.see("That is not \"prune\". Nothing was sent.")
	b.noMutations("a wrong word")
	for range "purge" {
		b.keys("backspace")
	}
	b.srv.ResetRequests()
	b.typeText("prune")
	b.keys("enter")
	b.finishJobs()
	muts := b.srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/build/prune" || muts[0].Query != "" {
		t.Fatalf("mutations %v", muts)
	}
	for _, r := range b.srv.Requests() {
		if r.Mutating() && r.Path != "/build/prune" {
			t.Fatalf("also sent %s", r)
		}
	}
	var job jobs.Job
	for _, j := range b.m.jobs.List() {
		if j.Title == "prune build cache" {
			job, _ = b.m.jobs.Get(j.ID)
		}
	}
	log := strings.Join(job.Log, "\n")
	if job.State != jobs.Succeeded || !strings.Contains(log, "the daemon deleted 4 build-cache records and reported 573.0 MB reclaimed") {
		t.Fatalf("job %v: %s", job.State, log)
	}
	b.until("a fresh account", func() bool { return b.m.sp.run == nil && b.m.sp.duAt.After(before) })
	if got := b.m.sp.du.BuildCache.TotalCount; got != 3 {
		t.Fatalf("the refreshed account counts %d records", got)
	}
}

// A refused prune fails the job with the daemon's words; the account is read
// again all the same.
func TestBuildCachePruneRefusedRereadsTheAccount(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, width: 140, height: 50})
	b.openSpace()
	before := b.m.sp.duAt
	b.keys("b")
	b.buildCacheDialog()
	b.srv.FailNext("POST", `^/build/prune$`, 500, "builder is busy", 1)
	b.typeText("prune")
	b.keys("enter")
	b.finishJobs()
	var job jobs.Job
	for _, j := range b.m.jobs.List() {
		if j.Title == "prune build cache" {
			job, _ = b.m.jobs.Get(j.ID)
		}
	}
	if job.State != jobs.Failed || !strings.Contains(job.Err, "builder is busy") {
		t.Fatalf("job %v %q", job.State, job.Err)
	}
	b.until("a fresh account", func() bool { return b.m.sp.run == nil && b.m.sp.duAt.After(before) })
}

func TestBuildCacheNothingToPrune(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		for _, r := range s.BuildCache() {
			s.SetBuildCacheInUse(r.ID, true)
		}
	}, width: 140, height: 50})
	b.openSpace()
	b.keys("b")
	b.buildCacheDialog()
	b.seeFlat("Nothing to prune: every build-cache record is in use")
	if b.m.dlg.hasInput || b.m.dlg.plan.Runnable() {
		t.Fatal("a plan with nothing to prune offers a confirmation")
	}
	b.keys("enter")
	b.noMutations("nothing to prune")
}

// A long candidate list scrolls through the dialog's own list path, and a
// daemon-supplied description cannot act on the terminal.
func TestBuildCacheLongListScrollsAndIsEscaped(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		for i := 0; i < 40; i++ {
			s.AddBuildCache(fakeengine.BuildRecord{ID: fmt.Sprintf("long%020d", i), Type: "regular",
				Description: fmt.Sprintf("step %02d \x1b]0;owned\x07", i), Size: int64(1000 + i)})
		}
	}, width: 120, height: 44})
	b.openSpace()
	b.keys("b")
	b.buildCacheDialog()
	b.see("of 44 · ↑↓ PgUp PgDn scroll the list")
	if strings.Contains(b.m.View(), "\x1b]0;owned") {
		t.Fatal("a daemon description reached the terminal unescaped")
	}
	b.seeFlat(`\x1b]0;owned\x07`)
	for i := 0; i < 10; i++ {
		b.keys("pgdown")
	}
	b.seeFlat("step 00") // the smallest record, last in a largest-first list
	b.keys("esc")
	b.keys("esc")
	if strings.Contains(b.m.View(), "\x1b]0;owned") {
		t.Fatal("the Space build-cache list drew a daemon description unescaped")
	}
}

// While a dialog is open, b is the dialog's key.
func TestBuildCacheKeyInsideADialog(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, width: 140, height: 50})
	b.openSpace()
	b.spaceTo("old-backups")
	b.keys("d")
	b.until("the removal", func() bool { return b.m.dlg != nil })
	b.srv.ResetRequests()
	b.keys("b")
	b.settle()
	if b.m.dlg == nil || b.m.dlg.plan.BuildCache != nil || b.m.preparing {
		t.Fatal("b opened the build-cache confirmation behind a dialog")
	}
	if n := b.srv.CountRequests("GET", `^/system/df$`); n != 0 {
		t.Fatalf("b inside a dialog read the account (%d)", n)
	}
}

func TestBuildCacheReadOnly(t *testing.T) {
	b := newBench(t, benchOpts{seed: buildScopesSeed, readOnly: true, width: 140, height: 50})
	b.openSpace()
	if strings.Contains(b.screen(), "build cache…") {
		t.Fatal("read-only hints offer b")
	}
	b.srv.ResetRequests()
	b.keys("b")
	b.settle()
	if b.m.dlg != nil || b.m.preparing {
		t.Fatal("b opened something in read-only mode")
	}
	// The desk's own background reads (details, refresh) may run; b must
	// add no account read and nothing mutating.
	for _, r := range b.srv.Requests() {
		if r.Mutating() || r.Path == "/system/df" {
			t.Fatalf("b in read-only sent %s", r)
		}
	}
	// Bypass the UI: the model's own preparation refuses too.
	if cmd := b.m.prepareBuildCachePrune(false); cmd != nil {
		t.Fatal("read-only preparation returned a command")
	}
	b.keys("?")
	if strings.Contains(b.screen(), "prune the build cache") {
		t.Fatal("read-only help offers b")
	}
}
