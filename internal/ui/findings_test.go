package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// openFindings highlights an image and opens its Findings tab with the
// Details pane focused.
func (b *bench) openFindings(ref string) {
	b.t.Helper()
	b.kind(KindImages)
	b.selectRow(ref)
	b.m.focus = PaneDetails
	for b.m.tabName() != tabFindings {
		b.keys("]")
	}
	b.settle()
}

func (b *bench) report() *forensics.Report {
	t, ok := b.m.findingsTarget()
	if !ok {
		return nil
	}
	return b.m.fx.cache[t.key]
}

func (b *bench) waitReport() *forensics.Report {
	b.t.Helper()
	b.until("the analysis to finish", func() bool { return b.report() != nil })
	b.settle()
	return b.report()
}

func (b *bench) exports() int { return b.srv.CountRequests("GET", `^/images(/.+)?/get$`) }

// cursorTo moves the findings cursor onto the first finding of a rule, by
// identity — the way the desk itself follows it while the list reorders.
func (b *bench) cursorTo(rule string) forensics.Finding {
	b.t.Helper()
	b.until("container facts", func() bool { return b.m.fx.factsFor == b.m.dataVersion && !b.m.fx.factsBusy })
	b.screen() // the tab takes up the image when it is first drawn
	t, _ := b.m.findingsTarget()
	for _, f := range b.m.currentFindings(b.report(), t.img) {
		if f.Rule == rule {
			b.m.fx.cursorKey = f.Key()
			b.m.fx.revealed = ""
			b.m.dirty = true
			b.settle()
			return f
		}
	}
	b.t.Fatalf("no %s finding:\n%s", rule, b.screen())
	return forensics.Finding{}
}

func TestFindingsTabRendersAndScrollsAtBothLayouts(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {120, 30}} {
		b := newBench(t, benchOpts{width: sz[0], height: sz[1]})
		b.openFindings("shop/api:dev")
		rep := b.waitReport()
		b.see("[Findings]", "findings · by layer · 4 layers · 79.0kB · oci", "live: 2 container(s)")
		b.assertFrameGeometry()
		if !strings.Contains(b.screen(), "L--") && !strings.Contains(b.screen(), "-- ") {
			t.Fatalf("%v: no config-level finding row:\n%s", sz, b.screen())
		}
		// Scroll through every finding; the frame holds its shape all the way.
		total := len(b.m.currentFindings(rep, b.m.snap.Images[0]))
		for i := 0; i < total+3; i++ {
			b.keys("j")
			b.assertFrameGeometry()
		}
		if b.m.fx.off == 0 {
			t.Errorf("%v: the list did not scroll (list height %d)", sz, b.m.fx.listH)
		}
		b.keys("G")
		b.see("L4 ") // by layer: the last layer's findings come last
		b.keys("g")
		b.screen() // the offset follows the cursor when the pane is drawn
		if b.m.fx.cursor != 0 || b.m.fx.off != 0 {
			t.Errorf("%v: g did not return to the top", sz)
		}
		b.dump("findings")
	}
}

func TestDeletedSecretIsShownAsStillInTheImageBytes(t *testing.T) {
	b := newBench(t, benchOpts{width: 220, height: 50})
	b.openFindings("shop/api:dev")
	b.waitReport()
	b.cursorTo("secret.deleted")
	b.see("secret.deleted", "/root/.aws/credentials", "still in the image bytes", "COPY . /root", "source   image · certain · layer 2")
	for _, s := range fixture.DemoSecrets() {
		if strings.Contains(b.screen(), s) {
			t.Fatalf("a secret is on screen without being revealed:\n%s", b.screen())
		}
	}
}

func TestSecondOpenIsInstant(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFindings("shop/api:dev")
	b.waitReport()
	if n := b.exports(); n != 1 {
		t.Fatalf("%d exports for one open", n)
	}
	jobsBefore := len(b.m.jobs.List())

	// Away to Info, and back: drawn from the cache in the same frame.
	b.keys("[")
	b.settle()
	b.step(keyMsg("]"))
	if !strings.Contains(b.screen(), "4 layers · 79.0kB · oci · linux/amd64") {
		t.Fatalf("second open is not instant:\n%s", b.screen())
	}
	// Away to another image, and back.
	b.m.focus = PaneList
	b.selectRow("python:3.12-slim")
	b.waitReport()
	b.selectRow("shop/api:dev")
	b.see("4 layers · 79.0kB · oci · linux/amd64")
	b.settle()
	if n := b.exports(); n != 2 {
		t.Errorf("%d exports after reopening shop/api:dev (want 2: it and python)", n)
	}
	if n := len(b.m.jobs.List()); n != jobsBefore+1 {
		t.Errorf("%d jobs, want %d: reopening started an analysis", n, jobsBefore+1)
	}
	// The platform is passed with the request.
	for _, r := range b.srv.Requests() {
		if strings.HasSuffix(r.Path, "/get") && !strings.Contains(r.Query, "platform=") {
			t.Errorf("export without a platform: %s", r)
		}
	}
}

func TestRevealIsOneAtATimeAndHidesWhenTheCursorMoves(t *testing.T) {
	b := newBench(t, benchOpts{width: 220, height: 50})
	b.openFindings("shop/api:dev")
	b.waitReport()
	f := b.cursorTo("secret.deleted")
	b.keys("v")
	shown := f.Secret.Reveal()
	b.see(shown)
	count := 0
	for _, s := range fixture.DemoSecrets() {
		if strings.Contains(b.screen(), s) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d secrets on screen, want exactly one:\n%s", count, b.screen())
	}
	b.keys("j")
	b.notSee(shown)
	// v again on a revealed finding hides it; o hides it too.
	b.keys("k", "v")
	b.see(shown)
	b.keys("v")
	b.notSee(shown)
	b.keys("v", "o")
	b.notSee(shown)
	// Leaving the pane hides it.
	b.keys("o", "o")
	b.cursorTo("secret.deleted")
	b.keys("v")
	b.see(shown)
	b.keys("esc")
	b.notSee(shown)
}

// TestASecretNeverLeavesTheScreen: the job record, its output, the status
// line, and errors carry no secret — whatever was revealed.
func TestASecretNeverLeavesTheScreen(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openFindings("shop/api:dev")
	b.waitReport()
	b.cursorTo("secret.deleted")
	b.keys("v")
	b.cursorTo("secret.buildarg")
	b.keys("v")
	b.settle()
	var texts []string
	for _, j := range b.m.jobs.List() {
		full, _ := b.m.jobs.Get(j.ID)
		texts = append(texts, full.Title, full.Target, full.Status, full.Err)
		texts = append(texts, full.Log...)
	}
	texts = append(texts, b.m.status.text)
	texts = append(texts, b.m.fx.failed["x"])
	for _, s := range fixture.DemoSecrets() {
		for _, x := range texts {
			if strings.Contains(x, s) {
				t.Fatalf("secret %q in %q", s, x)
			}
		}
	}
	// The job output dialog, opened from the Jobs pane.
	b.m.focus = PaneJobs
	b.keys("enter")
	b.see("analyse image")
	for _, s := range fixture.DemoSecrets() {
		if strings.Contains(b.screen(), s) {
			t.Fatalf("secret in the job output:\n%s", b.screen())
		}
	}
}

func TestFindingsInReadOnlyMode(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.openFindings("shop/api:dev")
	rep := b.waitReport()
	if len(rep.Findings) == 0 {
		t.Fatal("read-only analysis found nothing")
	}
	b.until("the live join", func() bool { return b.m.fx.factsFor != 0 })
	b.see("privileged")
	b.noMutations("analysing in read-only mode")
	if b.exports() != 1 {
		t.Errorf("exports: %d", b.exports())
	}
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("read-only desk sent %s", r)
		}
	}
	if j := b.m.jobs.List()[0]; j.State != jobs.Succeeded || j.Title != "analyse image" {
		t.Errorf("job: %+v", j)
	}
}

func TestLiveJoinFollowsTheDaemon(t *testing.T) {
	b := newBench(t, benchOpts{width: 220, height: 50})
	b.openFindings("shop/api:dev")
	b.waitReport()
	b.until("container facts", func() bool { return b.m.fx.factsFor != 0 })
	b.see("live.privileged", "live.published")
	b.see("old-migration exited", "privileged")
	before := b.exports()

	// The daemon changes; the tab follows without a new analysis.
	b.srv.Crash("shop-api-1", 137)
	b.until("the stop to show", func() bool { return strings.Contains(b.screen(), "shop-api-1 exited") })
	b.notSee("live.secret-exposed")
	if b.exports() != before {
		t.Error("a state change re-analysed the archive")
	}
}

func TestOrderCycles(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFindings("shop/api:dev")
	b.waitReport()
	for _, want := range []string{"by group", "by certainty", "by layer"} {
		b.keys("o")
		b.see(want)
	}
}

func TestRemoteLargeImageAsksFirst(t *testing.T) {
	ep := dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: &dockerctx.TLS{CAFile: "/c/ca.pem"}}
	b := remoteBench(t, ep, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SetImageSize("shop/api:dev", 2_400_000_000)
	}})
	b.openFindings("shop/api:dev")
	b.see("2.4GB", "the daemon is remote", "Enter analyses it")
	b.run(100 * time.Millisecond)
	if n := b.exports(); n != 0 {
		t.Fatalf("%d exports before consent", n)
	}
	b.keys("enter")
	b.waitReport()
	if b.exports() != 1 {
		t.Errorf("exports: %d", b.exports())
	}
	// A small image on the same daemon does not ask.
	b.m.focus = PaneList
	b.selectRow("python:3.12-slim")
	b.waitReport()
}

func TestAnalysisIsACancellableJob(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.ExportChunkDelay = 40 * time.Millisecond
	}})
	b.openFindings("shop/api:dev")
	b.see("Analysing shop/api:dev", "layers found")
	b.until("progress", func() bool {
		j := b.m.jobs.List()
		return len(j) > 0 && strings.Contains(j[0].Status, "read ·")
	})
	b.keys("c")
	b.see("Cancelled.", "Enter analyses again")
	if j := b.m.jobs.List()[0]; j.State != jobs.Cancelled {
		t.Fatalf("job: %+v", j)
	}
	b.srv.ExportChunkDelay = 0
	b.keys("enter")
	b.waitReport()
}

func TestMovingAwayCancelsTheRunningAnalysis(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.ExportChunkDelay = 40 * time.Millisecond
	}})
	b.openFindings("shop/api:dev")
	b.see("Analysing shop/api:dev")
	b.m.focus = PaneList
	b.srv.ExportChunkDelay = 0
	b.selectRow("python:3.12-slim")
	b.waitReport()
	b.until("the abandoned job to end", func() bool { return b.m.jobs.RunningCount() == 0 })
	states := map[jobs.State]int{}
	for _, j := range b.m.jobs.List() {
		states[j.State]++
	}
	if states[jobs.Cancelled] != 1 || states[jobs.Succeeded] != 1 {
		t.Fatalf("jobs: %v", states)
	}
}

func TestFindingsHintsAndHelp(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFindings("shop/api:dev")
	b.waitReport()
	b.see("o order", "v reveal")
	b.step(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	b.m.dlg.text.off = 0
	found := false
	for _, l := range b.m.helpLines(80) {
		if strings.Contains(l, "Findings tab") {
			found = true
		}
	}
	if !found {
		t.Error("the help does not describe the Findings tab")
	}
}
