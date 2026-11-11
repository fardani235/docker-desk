package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
)

// openSpace presses S and waits for the account and every image's layers.
func (b *bench) openSpace() {
	b.t.Helper()
	b.keys("S")
	b.until("the account", func() bool { return b.m.sp.du != nil && b.m.sp.run == nil })
	b.settle()
}

// spaceTo puts the view's cursor on the row with the given name.
func (b *bench) spaceTo(name string) {
	b.t.Helper()
	for _, r := range b.m.spaceObjectRows() {
		if r.name == name {
			b.m.sp.cursorKey = r.key
			b.m.dirty = true
			b.settle()
			return
		}
	}
	b.t.Fatalf("no Space row %q", name)
}

func (b *bench) account() engine.DiskUsage {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	du, err := b.eng.DiskUsage(ctx)
	if err != nil {
		b.t.Fatal(err)
	}
	return du
}

func (b *bench) usage(ref string) engine.ImageUsage {
	b.t.Helper()
	for _, it := range b.m.sp.du.ImageItems {
		if imageLabel(it.RepoTags, it.ID) == ref {
			return it
		}
	}
	b.t.Fatalf("no image %s in the account", ref)
	return engine.ImageUsage{}
}

// seeFlat waits for text that may be wrapped across lines of a box: the
// screen is compared with borders removed and white space collapsed.
func (b *bench) seeFlat(subs ...string) {
	b.t.Helper()
	flat := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if strings.ContainsRune("┃│┏┓┗┛━─", r) {
				return ' '
			}
			return r
		}, s)
		return strings.Join(strings.Fields(s), " ")
	}
	b.until("screen to show "+strings.Join(subs, " + "), func() bool {
		sc := flat(b.screen())
		if b.m.dlg != nil {
			// Only the dialog: what shows beside it is the view behind.
			box, _, _ := b.m.renderDialog()
			sc = flat(ansi.Strip(strings.Join(box, "\n")))
		}
		for _, sub := range subs {
			if !strings.Contains(sc, flat(sub)) {
				return false
			}
		}
		return true
	})
}

func (b *bench) sizeRequests() int {
	n := 0
	for _, r := range b.srv.Requests() {
		if strings.Contains(r.Query, "size=1") || strings.Contains(r.Query, "size=true") {
			n++
		}
	}
	return n
}

// TestCLISize pins the formatter to go-units' HumanSize, which the docker
// CLI prints `system df` with.
func TestCLISize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0B", 999: "999B", 1000: "1kB", 63_000: "63kB", 1_990_000: "1.99MB", 1_991_200: "1.991MB",
		24_110_000: "24.11MB", 494_200_000: "494.2MB", 3_276_400_000: "3.276GB", 1_820_000_000: "1.82GB",
	} {
		if got := cliSize(n); got != want {
			t.Errorf("cliSize(%d) = %q, want %q", n, got, want)
		}
	}
	u := engine.Usage{TotalSize: 3_276_400_000, Reclaimable: 1_325_800_000}
	if got := cliReclaimable(u, true); got != "1.326GB (40%)" {
		t.Errorf("reclaimable: %q", got)
	}
}

func TestSpaceOpensAndClosesWhereItWas(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindImages)
	b.selectRow("redis:7")
	b.m.focus = PaneDetails
	key := b.m.sel[KindImages].key
	b.openSpace()
	b.see("Space — where the bytes went", "TYPE", "RECLAIMABLE")
	// Keys go to the view, not to the panes behind it.
	b.keys("j", "j", "G", "]", "1", "tab")
	if b.m.focus != PaneDetails || b.m.kind != KindImages || b.m.sel[KindImages].key != key || b.m.tab[KindImages] != 0 {
		t.Fatalf("the desk moved behind the view: focus %v kind %v sel %q", b.m.focus, b.m.kind, b.m.sel[KindImages].key)
	}
	b.keys("esc")
	if b.m.sp.open || b.m.focus != PaneDetails || b.m.kind != KindImages || b.m.sel[KindImages].key != key {
		t.Fatalf("not restored: open %v focus %v kind %v sel %q", b.m.sp.open, b.m.focus, b.m.kind, b.m.sel[KindImages].key)
	}
	b.notSee("Space — where the bytes went")

	// From every pane, and S closes it as Esc does.
	for p := PaneKinds; p < paneCount; p++ {
		b.m.focus = p
		b.keys("S")
		if !b.m.sp.open {
			t.Fatalf("S did not open the view from %s", paneNames[p])
		}
		b.keys("S")
		if b.m.sp.open || b.m.focus != p {
			t.Fatalf("S did not close the view back to %s", paneNames[p])
		}
	}
	// Typing a filter, S is a letter.
	b.m.focus = PaneList
	b.keys("/", "S")
	if b.m.sp.open {
		t.Fatal("S typed into the filter opened the view")
	}
}

func TestSpaceFitsAndScrolls(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {120, 30}, {200, 50}} {
		b := newBench(t, benchOpts{width: sz[0], height: sz[1]})
		b.openSpace()
		b.assertFrameGeometry()
		b.see("Images", "1.333GB (39%)")
		rows := b.m.spaceObjectRows()
		for i := 0; i < len(rows)+2; i++ {
			b.keys("j")
			b.assertFrameGeometry()
		}
		b.see("BUILD CACHE", "exec.cachemount") // the last row shows what follows it
		if b.m.sp.off == 0 {
			t.Errorf("%v: the view did not scroll", sz)
		}
		b.keys("pgup", "pgup")
		b.assertFrameGeometry()
		b.keys("g")
		b.see("TYPE", "RECLAIMABLE") // back at the top, totals in view
		b.assertFrameGeometry()
		// A dialog over the view: the frame keeps its shape.
		b.keys("?")
		if b.m.dlg == nil || b.m.dlg.kind != dlgHelp {
			t.Fatal("? did not open the help over the view")
		}
		b.assertFrameGeometry()
		b.keys("esc")
		if !b.m.sp.open {
			t.Fatal("closing the help closed the view")
		}
		b.dump(fmt.Sprintf("space %dx%d", sz[0], sz[1]))
	}
}

// TestSpaceTotalsAreTheDaemons: every total is the daemon's figure, printed
// as the CLI prints it, from one GET; nothing walks containers on the side.
func TestSpaceTotalsAreTheDaemons(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.srv.ResetRequests()
	b.openSpace()
	du := b.account()
	for _, l := range []struct {
		name string
		u    engine.Usage
		pct  bool
	}{{"Images", du.Images, true}, {"Containers", du.Containers, true}, {"Local Volumes", du.Volumes, true}, {"Build Cache", du.BuildCache, false}} {
		want := fmt.Sprintf("%-14s %6d %7d %9s  %s", l.name, l.u.TotalCount, l.u.ActiveCount, cliSize(l.u.TotalSize), cliReclaimable(l.u, l.pct))
		b.see(want)
	}
	b.see("daemon (GET /system/df)")
	if n := b.srv.CountRequests("GET", `^/system/df$`); n != 2 { // the view's, and this test's own
		t.Fatalf("%d df requests", n)
	}
	b.noMutations("opening Space")
	if n := b.sizeRequests(); n != 0 {
		t.Fatalf("opening Space measured writable layers %d time(s)", n)
	}
	// Shared bytes are listed once: unique sizes and the groups together
	// are every byte the daemon says its images hold — no more, no less.
	var sum int64
	for _, it := range b.m.sp.du.ImageItems {
		sum += it.Unique()
	}
	for _, g := range b.m.sp.ix.Groups() {
		sum += g.Bytes
	}
	if sum != du.Images.TotalSize {
		t.Fatalf("unique + shared groups = %d, the daemon's total is %d", sum, du.Images.TotalSize)
	}
}

func TestSharedImageFreesLessAloneThanItsSize(t *testing.T) {
	// One row more than before the daemon strip, which took one.
	b := newBench(t, benchOpts{width: 160, height: 46})
	b.openSpace()
	b.spaceTo("shop/worker:dev")
	it := b.usage("shop/worker:dev")
	if it.SharedSize <= 0 || it.Unique() >= it.Size {
		t.Fatalf("shop/worker:dev should share layers: %+v", it)
	}
	b.see("alone      frees " + cliSize(it.Unique()) + " — unique; " + cliSize(it.SharedSize) + " shared stay · daemon")
	ix := b.m.sp.ix
	set := append([]string{it.ID}, ix.Sharers(it.ID)...)
	f := ix.Frees(set)
	if !f.Exact || f.Bytes <= it.Unique() {
		t.Fatalf("together: %+v (alone %d)", f, it.Unique())
	}
	b.see(fmt.Sprintf("with the %d image(s) sharing its layers: frees %s · computed from layer sizes", len(set)-1, cliSize(f.Bytes)))
	b.see("python:3.12-slim") // among the sharers named
	// A shared group names every holder, once.
	b.see("SHARED LAYERS", "56.2MB  3 layer(s)  <none> 8bae7d2a8907 · python:3.12-slim · shop/worker:dev")
	b.dump("together")
}

func TestWritableLayersOnlyWhenAsked(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.srv.ResetRequests()
	b.openSpace()
	b.keys("r") // the view's own refresh: account and lists, still no walk
	b.until("the account again", func() bool { return b.m.sp.run == nil })
	b.m.pending |= live.DirtyAll // and the periodic one
	b.run(200 * time.Millisecond)
	if n := b.sizeRequests(); n != 0 {
		t.Fatalf("%d size=1 request(s) before m", n)
	}
	b.spaceTo("shop-api-1")
	b.see("writable layers not measured", "writable   not measured")
	b.keys("m")
	b.until("the measurement", func() bool { return b.m.sp.sizeRun == nil && !b.m.sp.sizesAt.IsZero() })
	if n := b.sizeRequests(); n != 1 {
		t.Fatalf("%d size=1 request(s) after m", n)
	}
	js := b.m.jobs.List()
	if js[0].Title != "measure writable layers" || js[0].State != jobs.Succeeded {
		t.Fatalf("job: %+v", js[0])
	}
	b.spaceTo("shop-api-1")
	b.see(srcWritable, "18.4MB — what removing it frees; its volumes stay")
	b.noMutations("measuring")
}

func TestSpaceMeasurementsAreCancellable(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.srv.SizeDelay = 5 * time.Second
	b.openSpace()
	b.spaceTo("shop-api-1")
	b.keys("m")
	b.see("measuring writable layers")
	b.keys("c")
	b.until("the cancel", func() bool { return b.m.sp.sizeRun == nil })
	b.see("Cancelled: nothing was measured.")
	if j := b.m.jobs.List()[0]; j.State != jobs.Cancelled {
		t.Fatalf("job: %+v", j)
	}

	// The account itself, too.
	b.srv.DiskUsageDelay = 5 * time.Second
	b.keys("S")
	b.m.sp.du = nil // as if never read
	b.keys("S")
	b.see("Asking the daemon for its account")
	b.keys("c")
	b.until("the cancel", func() bool { return b.m.sp.run == nil })
	b.see("Cancelled before the daemon answered.")
	b.noMutations("cancelling")
}

func TestArchiveAttributionReadsNoArchive(t *testing.T) {
	b := newBench(t, benchOpts{width: 200, height: 50})
	b.openFindings("shop/api:dev")
	rep := b.waitReport()
	if n := b.exports(); n != 1 {
		t.Fatalf("%d exports", n)
	}
	b.openSpace()
	b.spaceTo("shop/api:dev")
	b.see("archive", "of file content in 4 layers", "Largest: L", "· archive, ")
	var largest = rep.Layers[0]
	for _, l := range rep.Layers {
		if l.Size > largest.Size {
			largest = l
		}
	}
	b.see(largest.Instruction)
	if n := b.exports(); n != 1 {
		t.Fatalf("the Space view read the archive again: %d exports", n)
	}
	// An image never analysed is not analysed for this view.
	b.spaceTo("nginx:1.27")
	b.settle()
	if n := b.exports(); n != 1 || strings.Contains(b.screen(), "of file content") {
		t.Fatalf("nginx:1.27 was analysed by the Space view (%d exports)", n)
	}
}

func TestFiguresTurnStaleAfterAChange(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openSpace()
	b.spaceTo("shop-api-1")
	b.keys("m")
	b.until("the measurement", func() bool { return b.m.sp.sizeRun == nil })
	b.settle()
	// A refresh that finds nothing changed ages nothing.
	b.m.pending |= live.DirtyAll
	b.run(150 * time.Millisecond)
	if strings.Contains(b.screen(), "STALE") {
		t.Fatalf("a refresh that changed nothing made figures stale:\n%s", b.screen())
	}
	// Something changes on the daemon; the lists see it.
	time.Sleep(5 * time.Millisecond)
	b.srv.AddVolume("arrived-later", nil)
	b.m.pending |= live.DirtyAll
	b.see("STALE — r re-reads")
	if !b.m.stale(b.m.sp.duAt) || !b.m.stale(b.m.sp.sizesAt) {
		t.Fatal("both the account and the measurement predate the change")
	}
	// Read again: the account is current; the writable layers, measured
	// before the change, stay stale until measured again.
	b.keys("r")
	b.until("the account again", func() bool { return b.m.sp.run == nil && !b.m.stale(b.m.sp.duAt) })
	b.notSee("STALE — r re-reads")
	b.see(srcWritable + " · ")
	if !b.m.stale(b.m.sp.sizesAt) {
		t.Fatal("the writable layers were measured before the change")
	}
	b.see("STALE")
}

func TestEnterGoesToTheObject(t *testing.T) {
	b := newBench(t, benchOpts{})
	for _, c := range []struct {
		name string
		kind Kind
	}{{"old-backups", KindVolumes}, {"redis-cache", KindContainers}, {"shop/worker:dev", KindImages}} {
		b.openSpace()
		b.spaceTo(c.name)
		b.keys("enter")
		r, ok := b.m.currentRow()
		if b.m.sp.open || b.m.kind != c.kind || b.m.focus != PaneList || !ok || r.name != c.name {
			t.Fatalf("%s: open %v kind %v focus %v row %q", c.name, b.m.sp.open, b.m.kind, b.m.focus, r.name)
		}
	}
	// A filter that hides the object is cleared, and the status says so.
	b.kind(KindVolumes)
	b.keys("/", "s", "h", "o", "p", "enter")
	b.openSpace()
	b.spaceTo("old-backups")
	b.keys("enter")
	if r, _ := b.m.currentRow(); r.name != "old-backups" || b.m.filter[KindVolumes] != "" {
		t.Fatalf("row %q filter %q", r.name, b.m.filter[KindVolumes])
	}
	b.see("Cleared the Volumes filter")
}

// TestSpaceReclaimUsesTheExistingDialogs: d and X open the desk's own
// confirmations; they state what would be freed; only the confirmed
// request is sent, never forced, never a prune endpoint.
func TestSpaceReclaimUsesTheExistingDialogs(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openSpace()
	old := b.usage("<none> 8bae7d2a8907")

	// One image, alone: its unique size, not its size.
	b.spaceTo("<none> 8bae7d2a8907")
	b.srv.ResetRequests()
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	if b.m.dlg.plan.Title != "Remove image" {
		t.Fatalf("dialog %q", b.m.dlg.plan.Title)
	}
	b.seeFlat("frees", cliSize(old.Unique())+" — its unique size; the "+cliSize(old.SharedSize)+" it shares with other images stays · daemon")
	if strings.Contains(b.screen(), "frees     "+cliSize(old.Size)) {
		t.Fatal("the confirmation offers the image's whole size as freed")
	}
	b.noMutations("opening the removal from Space")
	b.typeText("remove")
	b.keys("enter")
	b.finishJobs()
	muts := b.srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "DELETE" || !strings.HasSuffix(muts[0].Path, old.ID[len("sha256:"):]) || strings.Contains(muts[0].Query, "force") {
		t.Fatalf("requests: %v", muts)
	}
	if !b.m.sp.open {
		t.Fatal("the view closed under the confirmation")
	}

	// An image with two references: removing one frees nothing.
	b.spaceTo("node:22-alpine")
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("Untag image", "frees", "nothing — the image stays, under its other references")
	b.keys("esc")

	// The prune preview, with the set's figure.
	b.srv.ResetRequests()
	b.keys("r")
	b.until("the account again", func() bool { return b.m.sp.run == nil })
	b.spaceTo("python:3.12-slim")
	b.keys("X")
	b.until("the preview", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	b.see("Prune images", "frees")
	if strings.Contains(b.screen(), "(about ") {
		t.Fatal("the preview still sums image sizes as if they were freed")
	}
	b.keys("esc")

	// A container: from the measurement, once there is one.
	b.spaceTo("old-migration")
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("Remove container", "not measured — in Space (S), m measures writable layers")
	b.keys("esc")
	b.keys("m")
	b.until("the measurement", func() bool { return b.m.sp.sizeRun == nil && !b.m.sp.sizesAt.IsZero() })
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("2.1MB — its writable layer; its volumes stay · computed writable layer")
	b.keys("esc")

	// A volume.
	b.spaceTo("old-backups")
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("Remove volume", "1.82GB — the data in it · daemon")
	b.keys("esc")

	for _, r := range b.srv.Requests() {
		if r.Mutating() || strings.Contains(r.Path, "prune") {
			t.Fatalf("Space sent %s", r)
		}
	}
}

// TestRemovalConfirmationStatesWhatItFrees from the desk itself: with an
// account, the List's own d says what removing frees; the set of a prune
// is figured as a set.
func TestRemovalConfirmationStatesWhatItFrees(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openSpace()
	b.keys("esc")
	// Dangling prune: two images; what only they hold.
	b.m.focus = PaneList
	b.kind(KindImages)
	b.keys("X")
	b.until("the preview", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	var ids []string
	for _, s := range b.m.dlg.plan.Steps {
		ids = append(ids, s.ID)
	}
	f := b.m.sp.ix.Frees(ids)
	if !f.Exact {
		t.Fatalf("the dangling set: %+v", f)
	}
	b.seeFlat("frees", cliSize(f.Bytes)+" — their own layers and the layers only they share · computed from layer sizes")
	b.keys("esc")
	// python:3.12-slim holds nothing of its own: removing it frees 0B.
	b.selectRow("python:3.12-slim")
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("0B — its unique size; the 131MB it shares with other images stays")
	b.keys("esc")
}

func TestRemovalConfirmationWithoutAnAccountSaysSo(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.m.focus = PaneList
	b.kind(KindImages)
	b.selectRow("python:3.12-slim")
	b.srv.ResetRequests()
	b.keys("d")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	b.seeFlat("frees", "not measured — S opens Space")
	if n := b.srv.CountRequests("GET", `^/system/df$`); n != 0 {
		t.Fatalf("a confirmation asked for the account itself (%d)", n)
	}
}

func TestSpaceInReadOnlyMode(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true, width: 80, height: 24})
	b.srv.ResetRequests()
	b.openSpace()
	b.see("[READ-ONLY]", "1.333GB (39%)", "alone      frees")
	hints := strings.Split(b.screen(), "\n")
	last := hints[len(hints)-1]
	if strings.Contains(last, "remove") || strings.Contains(last, "prune") {
		t.Fatalf("read-only hints offer reclaim: %q", last)
	}
	for _, k := range []string{"d", "X"} {
		b.keys(k)
		b.settle()
		if b.m.dlg != nil || b.m.preparing {
			t.Fatalf("%s opened something in read-only mode", k)
		}
	}
	b.keys("m")
	b.until("the measurement", func() bool { return b.m.sp.sizeRun == nil && !b.m.sp.sizesAt.IsZero() })
	b.spaceTo("shop-api-1")
	b.see("18.4MB")
	b.keys("?")
	if strings.Contains(b.screen(), "the desk's own removal confirmation") {
		t.Fatal("the help offers reclaim in read-only mode")
	}
	b.keys("esc")
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Fatalf("read-only Space sent %s", r)
		}
	}
	b.assertFrameGeometry()
}

// TestSpaceOnAnOlderDaemon: below API 1.52 the totals are the client
// library's sums, and the view says whose they are.
func TestSpaceOnAnOlderDaemon(t *testing.T) {
	b := newBench(t, benchOpts{width: 160, height: 40, seed: func(s *fakeengine.Server) { s.SeedDemo(); s.AdvertiseAPI = "1.51" }})
	b.openSpace()
	b.see("summed by the client library from the daemon's lists (API < 1.52 states no totals)")
}

// TestSpaceWhenLayerSizesDoNotAddUp is the containerd image store's case:
// the daemon's image sizes count more than the history's layer sizes, so no
// shared byte can be confirmed. The view says so, keeps the daemon's
// figures, and gives what a set frees as a floor — never a guess.
func TestSpaceWhenLayerSizesDoNotAddUp(t *testing.T) {
	b := newBench(t, benchOpts{width: 160, height: 50, seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		for _, id := range s.ImageIDs() {
			s.SetImageSize(id, s.ImageSize(id)+3_000_000) // compressed content, as the containerd store adds
		}
	}})
	b.openSpace()
	if len(b.m.sp.ix.Disagreeing()) != len(b.m.sp.du.ImageItems) {
		t.Fatalf("%d of %d disagree", len(b.m.sp.ix.Disagreeing()), len(b.m.sp.du.ImageItems))
	}
	b.spaceTo("shop/worker:dev")
	it := b.usage("shop/worker:dev")
	b.see("alone      frees " + cliSize(it.Unique())) // the daemon's figure stands
	b.seeFlat("frees at least", "shared layers have no size the daemon's own figures confirm")
	b.keys("j", "j", "j", "j", "j", "j", "j", "j")
	b.seeFlat("Sizes not attributed: no image's layer sizes add up to the daemon's size for it.")
	b.see("       ?  ")
	if strings.Contains(b.screen(), "≥0B") {
		t.Fatal("an unknown group size is drawn as a figure")
	}
}
