package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// Images › Layers (OpenSpec change add-layers-tab; docs/DECISIONS.md, LY).

func layersSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.SeedLayers()
}

// The sealed build argument of the seeded application's RUN step: no view,
// file, job or status line may ever hold it.
const layersToken = "tok-5ecr3t-value-0123456789"

var remoteEndpoint = dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: &dockerctx.TLS{CAFile: "/c/ca.pem"}}

// openLayers highlights an image and opens its Layers tab with the Details
// pane focused, and waits for the read of the image's own layers.
func (b *bench) openLayers(ref string) {
	b.t.Helper()
	b.kind(KindImages)
	b.selectRow(ref)
	b.m.focus = PaneDetails
	for b.m.tabName() != tabLayers {
		b.keys("]")
	}
	b.until("the image's own layers", func() bool {
		t, ok := b.m.layersTarget()
		_, held := b.m.lc.byID[t.img.ID]
		return ok && held
	})
	b.settle()
}

func (b *bench) layers() lyView {
	b.t.Helper()
	t, ok := b.m.layersTarget()
	if !ok {
		b.t.Fatalf("no Layers target:\n%s", b.screen())
	}
	return b.m.layersView(t)
}

// readHolders presses s and waits for the engine-wide read.
func (b *bench) readHolders() {
	b.t.Helper()
	b.keys("s")
	b.until("the layer read", func() bool { return b.m.lc.run == nil })
	b.settle()
}

func (b *bench) histories() int { return b.srv.CountRequests("GET", `^/images/.+/history$`) }

// details is the Details pane's text: the lines between its borders.
func (b *bench) details() string {
	var out []string
	for _, l := range strings.Split(b.screen(), "\n") {
		i, j := strings.Index(l, "┃"), strings.LastIndex(l, "┃")
		if i >= 0 && j > i {
			out = append(out, strings.TrimRight(l[i+len("┃"):j], " "))
		}
	}
	return strings.Join(out, "\n")
}

// stepRows is the drawn step rows: the lines that start with a step number.
func stepRows(details string) []string {
	var rows []string
	for _, l := range strings.Split(details, "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "▸"))
		if len(f) >= 2 {
			var n int
			if _, err := fmt.Sscanf(f[0], "%d", &n); err == nil && fmt.Sprint(n) == f[0] && (f[1] == "config" || f[1] == "?" || strings.HasPrefix(f[1], "L")) {
				rows = append(rows, l)
			}
		}
	}
	return rows
}

// builtImage is a fixture of n layers under a history of steps entries: the
// first n-1 layer-making entries, then the config-only ones, then the last
// layer — so config-only steps sit between layers, as in a real build.
func builtImage(layers, steps int) fixture.Image {
	img := fixture.Image{OS: "linux", Architecture: "amd64", Cmd: []string{"/bin/app"}}
	for i := 0; i < layers; i++ {
		by := fmt.Sprintf("RUN step-%02d", i+1)
		img.Layers = append(img.Layers, fixture.Layer{CreatedBy: by, Files: []fixture.File{{Path: fmt.Sprintf("opt/f%02d", i+1), Body: []byte(by)}}})
	}
	for i := 0; i < layers-1; i++ {
		img.History = append(img.History, fixture.History{CreatedBy: img.Layers[i].CreatedBy})
	}
	for i := 0; i < steps-layers; i++ {
		img.History = append(img.History, fixture.History{CreatedBy: fmt.Sprintf("/bin/sh -c #(nop)  ENV K%02d=v", i+1), EmptyLayer: true})
	}
	img.History = append(img.History, fixture.History{CreatedBy: img.Layers[layers-1].CreatedBy})
	return img
}

func sizesOf(n int, each int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = each
	}
	return out
}

// ---- 3: one layer cache -----------------------------------------------------------

func TestSpaceOpenedTwiceReadsNoLayersAgain(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openSpace()
	first := b.histories()
	if first == 0 || len(b.m.lc.byID) == 0 {
		t.Fatalf("the Space view read no layers into the desk's cache: %d history calls, %d cached", first, len(b.m.lc.byID))
	}
	b.keys("S") // close
	b.openSpace()
	b.keys("r") // the account again: every image's layers are already held
	b.until("the account", func() bool { return b.m.sp.run == nil })
	b.settle()
	if n := b.histories(); n != first {
		t.Errorf("%d history calls after the second open, %d after the first: the layers were read again", n, first)
	}
	b.noMutations("the Space view")
}

func TestLayersTabUsesWhatSpaceRead(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed})
	b.openSpace()
	first := b.histories()
	b.keys("S")
	b.openLayers(fakeengine.LayersApp)
	// The holders are known without `s`, and without a request.
	if v := b.layers(); v.sharers == nil || v.missing != 0 {
		t.Fatalf("the tab does not use the layers the Space view read: %+v", v)
	}
	if n := b.histories(); n != first {
		t.Errorf("%d history calls, want the Space view's %d", n, first)
	}
	b.seeFlat("3 of 5 layers are held by another image here")
}

func TestLayerReadFetchesOnlyWhatTheCacheLacks(t *testing.T) {
	var ids []string
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		for _, ref := range []string{"one:1", "two:1", "three:1"} {
			ids = append(ids, "sha256:"+s.AddImage(1_000_000, time.Hour, ref))
		}
	}})
	if len(b.m.snap.Images) != 3 {
		t.Fatalf("%d images listed", len(b.m.snap.Images))
	}
	b.m.putLayers(b.m.snap.Images[0].ID, layerInfo{diffs: []string{"sha256:held"}})
	b.srv.ResetRequests()
	if n := b.m.startLayerRead(); n != 2 {
		t.Fatalf("the read covers %d images, want 2", n)
	}
	id := b.m.lc.run.jobID
	b.until("the layer read", func() bool { return b.m.lc.run == nil })
	if n := b.histories(); n != 2 {
		t.Errorf("%d history calls, want 2", n)
	}
	if n := b.srv.CountRequests("GET", `^/images/.+/json$`); n != 2 {
		t.Errorf("%d inspect calls, want 2", n)
	}
	for _, img := range b.m.snap.Images {
		if !b.m.layerKnown(img.ID) {
			t.Errorf("%s is not in the cache after the read", img.ID)
		}
	}
	if got := layerProgress(12, 412); got != "layers of image 12 of 412" {
		t.Errorf("progress line %q", got)
	}
	j, _ := b.m.jobs.Get(id)
	if j.State != jobs.Succeeded || !strings.Contains(j.Status, "layers of 2 image(s) read") {
		t.Errorf("job: %s %q", j.State, j.Status)
	}
	// Nothing left to read: a second read starts no job and sends nothing.
	b.srv.ResetRequests()
	if n := b.m.startLayerRead(); n != 0 || b.m.lc.run != nil {
		t.Errorf("a second read started for %d images", n)
	}
	b.noMutations("the layer read")
}

// The progress line is what the job shows while it runs.
func TestLayerReadProgressLine(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindImages)
	b.srv.SetHistoryDelay(30 * time.Millisecond)
	n := b.m.startLayerRead()
	id := b.m.lc.run.jobID
	b.until("progress", func() bool {
		j, _ := b.m.jobs.Get(id)
		return strings.HasPrefix(j.Status, "layers of image ") && strings.HasSuffix(j.Status, fmt.Sprintf(" of %d", n))
	})
	b.srv.SetHistoryDelay(0)
	b.m.jobs.Cancel(id)
	b.until("the layer read", func() bool { return b.m.lc.run == nil })
}

// ---- 4.1: state, target and sync -----------------------------------------------------

func TestLayersFirstOpenSecondOpenAndMovingOn(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.ExportChunkDelay = 40 * time.Millisecond
	}})
	// Nothing is asked while the tab is not shown.
	b.kind(KindImages)
	b.selectRow(fakeengine.LayersApp)
	b.run(60 * time.Millisecond)
	if b.histories() != 0 || b.exports() != 0 {
		t.Fatalf("%d history and %d archive requests before the tab was shown", b.histories(), b.exports())
	}

	b.srv.ExportChunkDelay = 0
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.run(60 * time.Millisecond)
	if b.histories() != 1 || b.exports() != 1 {
		t.Fatalf("first open: %d history calls and %d archive requests, want 1 and 1", b.histories(), b.exports())
	}

	// A second open on the analysed image asks for nothing. (] wraps to
	// Info and [ comes back: Upstream, on the other side, asks the daemon
	// about the image on its own account.)
	b.keys("]")
	b.settle()
	if b.m.tabName() != tabInfo {
		t.Fatalf("on %s", b.m.tabName())
	}
	b.keys("[")
	b.run(60 * time.Millisecond)
	b.seeFlat("the archive's history")
	if b.histories() != 1 || b.exports() != 1 {
		t.Errorf("second open: %d history calls and %d archive requests, want 1 and 1", b.histories(), b.exports())
	}

	// Moving to another image cancels the analysis of the first.
	b.srv.ExportChunkDelay = 500 * time.Millisecond
	b.m.focus = PaneList
	b.selectRow("shop/api:dev")
	b.until("the analysis of shop/api:dev", func() bool { return b.m.fx.run != nil })
	first := b.m.fx.run.jobID
	b.selectRow("python:3.12-slim")
	b.until("the first analysis to be cancelled", func() bool {
		j, _ := b.m.jobs.Get(first)
		return j.State == jobs.Cancelled
	})
	b.srv.ExportChunkDelay = 0
	b.noMutations("the Layers tab")
}

// Findings was opened first: the exact steps are drawn at once, and no
// archive is requested for the Layers tab.
func TestLayersDrawsTheFindingsReport(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed})
	b.openFindings(fakeengine.LayersApp)
	b.waitReport()
	n := b.exports()
	b.keys("]", "]") // Upstream, Layers
	if b.m.tabName() != tabLayers {
		t.Fatalf("on %s", b.m.tabName())
	}
	b.seeFlat("the archive's history (exact")
	b.run(60 * time.Millisecond)
	if b.exports() != n {
		t.Errorf("%d archive requests, want the Findings tab's %d", b.exports(), n)
	}
}

// ---- 4.2: the list before the archive ------------------------------------------------

func TestLayersDaemonListBindsOnlyByCount(t *testing.T) {
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		id := s.AddImage(0, time.Hour, "built/eighteen:1")
		s.SetImageFixture(id, builtImage(7, 18), sizesOf(7, 100_000_000)...)
	}, width: 160, height: 90})
	b.openLayers("built/eighteen:1")
	if b.exports() != 0 {
		t.Fatal("the archive was requested before consent")
	}
	v := b.layers()
	if v.exact || !v.bound || len(v.steps) != 18 || v.layers != 7 {
		t.Fatalf("view: exact %v bound %v, %d steps, %d layers", v.exact, v.bound, len(v.steps), v.layers)
	}
	config, bound := 0, 0
	for i, s := range v.steps {
		switch {
		case s.config:
			config++
			if s.hasSize || s.layer != 0 {
				t.Errorf("step %d made no layer and is given a size or a layer: %+v", i+1, s)
			}
		case s.layer > 0:
			bound++
			if s.layer != bound {
				t.Errorf("step %d names L%d, want L%d", i+1, s.layer, bound)
			}
		}
	}
	if config != 11 || bound != 7 {
		t.Errorf("%d config-only and %d bound rows, want 11 and 7", config, bound)
	}
	b.seeFlat("18 steps · 7 layers · the daemon's history, bound by counting")
	rows := stepRows(b.details())
	if len(rows) != 18 {
		t.Fatalf("%d step rows drawn, want 18:\n%s", len(rows), b.details())
	}
	if !strings.Contains(rows[0], "L1") || !strings.Contains(rows[17], "L7") || !strings.Contains(rows[6], "config") {
		t.Errorf("rows:\n%s", strings.Join(rows, "\n"))
	}
}

// The daemon's history of the seeded application has 3 sized entries for 5
// layers (two layers hold nothing), and the fake — like the engine this was
// measured on — says EmptyLayer false for all 8: nothing binds, and the tab
// does not conclude that 8 steps made layers.
func TestLayersDaemonListRefusesAMismatch(t *testing.T) {
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.SetImageSize(fakeengine.LayersApp, 2_400_000_000)
	}, width: 160, height: 60})
	b.openLayers(fakeengine.LayersApp)
	v := b.layers()
	if v.exact || v.bound || len(v.steps) != 8 || v.layers != 5 {
		t.Fatalf("view: exact %v bound %v, %d steps, %d layers", v.exact, v.bound, len(v.steps), v.layers)
	}
	for i, s := range v.steps {
		if s.layer != 0 || s.config {
			t.Errorf("step %d is bound or called config only from a history that does not bind: %+v", i+1, s)
		}
	}
	b.seeFlat("8 steps · 5 layers · the daemon's history; no step names a layer",
		"3 of the daemon's 8 history entries carry a size, for 5 layers",
		"the daemon's history does not say which step made which layer; the archive's history would")
	for _, r := range stepRows(b.details()) {
		if f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(r), "▸")); f[1] != "?" {
			t.Errorf("a row names a layer: %q", r)
		}
	}
	b.seeFlat("which layer it made is unknown")
}

// ---- 4.3: the list after the archive -------------------------------------------------

func TestLayersExactListReplacesTheCountedOne(t *testing.T) {
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.SetImageSize(fakeengine.LayersApp, 2_400_000_000)
	}, width: 160, height: 60})
	b.openLayers(fakeengine.LayersApp)
	b.seeFlat("2.4GB", "the daemon is remote", "Enter analyses it")
	b.keys("j", "j", "j", "j")
	if b.m.ly.cursor != 4 {
		t.Fatalf("cursor %d", b.m.ly.cursor)
	}
	b.seeFlat("the daemon's history; no step names a layer")
	b.keys("enter")
	b.waitReport()
	b.seeFlat("8 steps · 5 layers · the archive's history (exact")
	b.notSee("no step names a layer")
	if b.m.ly.cursor != 4 {
		t.Errorf("the cursor moved to %d when the exact list arrived", b.m.ly.cursor)
	}
	v := b.layers()
	want := []int{1, 0, 2, 3, 4, 0, 5, 0}
	for i, s := range v.steps {
		if s.layer != want[i] || s.config != (want[i] == 0) {
			t.Errorf("step %d: layer %d config %v, want layer %d", i+1, s.layer, s.config, want[i])
		}
	}
	b.seeFlat("5 of 8 · made layer L4 of 5 (bound by the archive's history)")
	if b.exports() != 1 {
		t.Errorf("%d archive requests", b.exports())
	}
}

// A report with fewer steps than the daemon's history: the cursor is an
// index, clamped, and the block follows it.
func TestLayersCursorIsClampedWhenTheListShrinks(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 160, height: 60})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.keys("G")
	if b.m.ly.cursor != 7 {
		t.Fatalf("cursor %d", b.m.ly.cursor)
	}
	rep := b.report()
	rep.Steps = rep.Steps[:3] // the archive lists fewer steps than the daemon did
	b.m.dirty = true
	b.settle()
	b.screen()
	if b.m.ly.cursor != 2 {
		t.Errorf("cursor %d, want the last of 3 steps", b.m.ly.cursor)
	}
	b.assertFrameGeometry()
}

// ---- 4.4: rows and block at three widths ----------------------------------------------

func TestLayersRowsAtThreeWidths(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.readHolders()
	v := b.layers()
	for _, c := range []struct {
		w                 int
		size, files, held bool
	}{
		{120, true, true, true},
		{80, true, false, true},
		{60, false, false, true},
	} {
		cols := layersCols(v, c.w)
		if cols.size != c.size || cols.files != c.files || cols.holder != c.held {
			t.Errorf("%d cells: columns size %v files %v held %v, want %v %v %v", c.w, cols.size, cols.files, cols.holder, c.size, c.files, c.held)
		}
		for _, h := range []int{7, 12, 30} {
			b.m.ly.cursor = 0
			lines := b.m.renderLayers(c.w, h)
			if len(lines) > h {
				t.Errorf("%dx%d: %d lines drawn", c.w, h, len(lines))
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > c.w {
					t.Errorf("%dx%d: a line is %d cells wide: %q", c.w, h, ansi.StringWidth(l), ansi.Strip(l))
				}
			}
			text := ansi.Strip(strings.Join(lines, "\n"))
			rows := stepRows(text)
			if len(rows) == 0 {
				t.Fatalf("%dx%d: no step row:\n%s", c.w, h, text)
			}
			// The instruction and the layer stay at every width.
			if !strings.Contains(rows[0], "L1") || !strings.Contains(rows[0], "ADD file:51c0ffee in /") {
				t.Errorf("%dx%d: the first row lacks its layer or instruction: %q", c.w, h, rows[0])
			}
			if got := strings.Contains(rows[0], "28.4MB"); got != c.size {
				t.Errorf("%dx%d: size in the row %v, want %v: %q", c.w, h, got, c.size, rows[0])
			}
			f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(rows[0]), "▸"))
			wantFields := 2 // number, layer
			for _, on := range []bool{c.size, c.files, c.held} {
				if on {
					wantFields++
				}
			}
			if f[wantFields] != "ADD" {
				t.Errorf("%dx%d: %d columns before the instruction, want %d: %q", c.w, h, strings.Index(strings.Join(f, " "), "ADD"), wantFields, rows[0])
			}
			if h == 30 {
				// A tall pane holds every row and the whole block: at 60
				// cells the block still states every figure the row dropped.
				if len(rows) != 8 {
					t.Errorf("%dx%d: %d rows, want 8", c.w, h, len(rows))
				}
				flat := strings.Join(strings.Fields(text), " ")
				for _, want := range []string{"28.4MB (the daemon's history)", "its files hold", "wrote 1 file", "held by 1 other image: acme/steps-base:1", "matches the config's diff_id"} {
					if !strings.Contains(flat, want) {
						t.Errorf("%dx%d: the block lacks %q:\n%s", c.w, h, want, text)
					}
				}
			}
		}
	}
	// On the real frame, at the two layouts: the shape holds on every step.
	for _, sz := range [][2]int{{120, 40}, {80, 24}} {
		b := newBench(t, benchOpts{seed: layersSeed, width: sz[0], height: sz[1]})
		b.openLayers(fakeengine.LayersApp)
		b.waitReport()
		b.readHolders()
		for i := 0; i < 10; i++ {
			b.keys("j")
			b.assertFrameGeometry()
		}
		if b.m.ly.cursor != 7 {
			t.Errorf("%v: cursor %d after walking past the end", sz, b.m.ly.cursor)
		}
	}
}

func TestLayersBlockForALayerAConfigStepAndAnUnreadLayer(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		z := builtImage(3, 4)
		z.Layers[2].Zstd = true
		id := s.AddImage(0, time.Hour, "built/zstd:1")
		s.SetImageFixture(id, z, 1_000_000, 2_000_000, 3_000_000)
	}, width: 170, height: 60})
	b.exportHome()
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.readHolders()

	// A layer step: every figure, each with its source.
	b.keys("g", "j", "j") // step 3: RUN, L2
	b.seeFlat("3 of 8 · made layer L2 of 5 (bound by the archive's history)",
		"held by no other image here",
		"61.2MB (the daemon's history) · its files hold 17B · the archive stores",
		"wrote 1 file · 0 directories · deleted 0 paths · 0 opaque directories",
		"matches the config's diff_id",
		"findings 1 on this layer: secret.buildarg",
		"by RUN make build (build args: BUILD_TOKEN)")
	if strings.Contains(b.screen(), layersToken) {
		t.Fatal("the build argument's value is on screen")
	}

	// A config-only step: what it did, and no digest.
	b.keys("g", "j")
	b.seeFlat("2 of 8 · config only: it changed the image's configuration, not its files, and made no layer", "by ENV LANG=C.UTF-8")
	d := b.details()
	block := d[strings.Index(d, "2 of 8 · config only"):]
	for _, no := range []string{"digest", "sha256:", "wrote", "held by", "size "} {
		if strings.Contains(block, no) {
			t.Errorf("a config-only step's block says %q:\n%s", no, block)
		}
	}

	// A layer the archive could not walk: said, with the reason, and no
	// file count or archive byte figure claimed for it.
	b.m.focus = PaneList
	b.selectRow("built/zstd:1")
	b.m.focus = PaneDetails
	b.waitReport()
	b.keys("G")
	b.seeFlat("4 of 4 · made layer L3 of 3", "wrote not known: the layer could not be walked:")
	d = b.details()
	block = d[strings.Index(d, "4 of 4"):]
	for _, no := range []string{"its files hold", "the archive stores", " files ·"} {
		if strings.Contains(block, no) {
			t.Errorf("an unread layer's block claims %q:\n%s", no, block)
		}
	}
	if !strings.Contains(block, "3.0MB (the daemon's history)") {
		t.Errorf("the daemon's size is not shown for the unread layer:\n%s", block)
	}
}

// Before the archive is read the block shows the daemon's figures and says
// the rest is not known yet.
func TestLayersBlockBeforeTheArchive(t *testing.T) {
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		id := s.AddImage(0, time.Hour, "built/eighteen:1")
		s.SetImageFixture(id, builtImage(7, 18), sizesOf(7, 100_000_000)...)
	}, width: 170, height: 60})
	b.openLayers("built/eighteen:1")
	b.seeFlat("1 of 18 · made layer L1 of 7 (bound by counting the sizes in the daemon's history)",
		"100MB (the daemon's history)",
		"wrote not known yet: the archive has not been read",
		"the daemon's diff ID; not checked against the archive")
	if strings.Contains(b.details(), "its files hold") {
		t.Error("the block states an archive figure before the archive was read")
	}
}

// ---- 4.5: the summary line -------------------------------------------------------------

func TestLayersSummary(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 220, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()

	// Before s: not read, and the key that reads them.
	b.seeFlat("Holders not known: s reads the layers of 17 other images, one inspect each (GET only)")
	if v := b.layers(); v.sharers != nil {
		t.Fatal("holders are stated before the read")
	}
	if strings.Contains(b.details(), " held ") {
		t.Error("a holders column is drawn before the read")
	}

	// A shared base: both figures, each with its source.
	b.readHolders()
	b.seeFlat("3 of 5 layers are held by another image here · the other 2 hold 63.0MB (the daemon's history) · holders from the diff IDs of 18 images (the daemon's inspect)")

	// Nothing shared: said, not "0 of 0".
	b.m.focus = PaneList
	b.selectRow("busybox:1.36")
	b.m.focus = PaneDetails
	b.seeFlat("No other image here holds its 1 layer · it holds 4.3MB (the daemon's history)")
	b.notSee("0 of")
}

func TestLayersSummaryOfAnIncompleteRead(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 240, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	// Three images cannot be inspected.
	b.srv.FailNext("GET", `^/images/.+/json$`, 500, "layer store is busy", 3)
	b.readHolders()
	v := b.layers()
	if v.failed != 3 || v.missing != 0 || v.sharers == nil {
		t.Fatalf("failed %d missing %d", v.failed, v.missing)
	}
	b.seeFlat("at least ", "of 5 layers are held by another image here", "3 images were not read, so a layer may have more holders: s tries again",
		"holders from the diff IDs of 15 images")
	// The own figure is an upper bound while images are unread, and says so.
	if sum := strings.Join(strings.Fields(b.details()), " "); !strings.Contains(sum, " at most ") || !strings.Contains(sum, "at least "+fmt.Sprint(countShared(v))+" of 5 layers") {
		t.Errorf("the summary does not bound both figures:\n%s", b.details())
	}
	// s reads only those three.
	b.srv.ResetRequests()
	b.readHolders()
	if n := b.histories(); n != 3 {
		t.Errorf("the second read made %d history calls, want 3", n)
	}
	b.seeFlat("3 of 5 layers are held by another image here")
	b.notSee("at least")
}

// ---- 4.6: the states with nothing to show ------------------------------------------------

func TestLayersStatesWithNothingToShow(t *testing.T) {
	noRows := func(t *testing.T, b *bench) {
		t.Helper()
		if rows := stepRows(b.details()); len(rows) != 0 {
			t.Errorf("rows were drawn:\n%s", strings.Join(rows, "\n"))
		}
		if v := b.layers(); len(v.steps) != 0 {
			t.Errorf("%d steps in the view", len(v.steps))
		}
	}

	t.Run("no history", func(t *testing.T) {
		// Remote and over the threshold, so the archive stays unread and
		// the daemon's (empty) history is all there is.
		b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
			layersSeed(s)
			s.SetImageHistory(fakeengine.LayersBase)
			s.SetImageSize(fakeengine.LayersBase, 2_400_000_000)
		}, width: 200, height: 50})
		b.openLayers(fakeengine.LayersBase)
		b.seeFlat("The daemon reports no build history for this image.")
		noRows(t, b)
		// What is shared is still shown once the layers are known.
		b.readHolders()
		b.seeFlat("All 2 layers are held by another image here")
	})

	t.Run("no layers", func(t *testing.T) {
		b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
			id := s.AddImage(0, time.Hour, "built/scratch:1")
			s.SetImageFixture(id, fixture.Image{OS: "linux", Architecture: "amd64"})
			s.SetImageHistory("built/scratch:1")
		}, width: 200, height: 50})
		b.openLayers("built/scratch:1")
		b.seeFlat("This image has no layers")
		noRows(t, b)
	})

	t.Run("own layers unreadable", func(t *testing.T) {
		b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
		b.srv.FailNext("GET", `^/images/.+/history$`, 500, "history is unavailable", 1)
		b.openLayers(fakeengine.LayersApp)
		b.seeFlat("This image's layers could not be read:", "history is unavailable", "s reads them again")
		noRows(t, b)
		b.run(80 * time.Millisecond)
		if n := b.histories(); n != 1 {
			t.Fatalf("the failed read was repeated without being asked: %d history calls", n)
		}
		b.readHolders()
		if v := b.layers(); !v.have || len(v.steps) != 8 {
			t.Errorf("s did not read the image again: have %v, %d steps", v.have, len(v.steps))
		}
	})

	t.Run("analysis running", func(t *testing.T) {
		b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
			layersSeed(s)
			s.ExportChunkDelay = 500 * time.Millisecond
		}, width: 200, height: 50})
		b.openLayers("shop/api:dev")
		b.seeFlat("Analysing the archive for the exact steps and each layer's contents", "c cancels")
		// The daemon's list is drawn meanwhile; nothing of the archive is.
		if v := b.layers(); v.exact || len(v.steps) == 0 {
			t.Errorf("exact %v, %d steps while the analysis runs", v.exact, len(v.steps))
		}
		b.seeFlat("wrote not known yet: the archive has not been read")
		b.keys("c")
		b.seeFlat("The analysis ended without a report: Cancelled.", "Enter analyses again")
		b.srv.ExportChunkDelay = 0
	})

	t.Run("analysis failed", func(t *testing.T) {
		b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
		b.srv.FailNext("GET", `^/images(/.+)?/get$`, 500, "export is not possible", 1)
		b.openLayers(fakeengine.LayersApp)
		b.seeFlat("The analysis ended without a report:", "export is not possible", "Enter analyses again")
		if b.layers().exact {
			t.Error("exact steps without a report")
		}
		b.keys("enter")
		b.waitReport()
		b.seeFlat("the archive's history (exact")
	})

	t.Run("archive of other layers", func(t *testing.T) {
		// The demo's shop/api:dev is listed by the fake with six layers and
		// its archive holds four: nothing of the archive is laid over them.
		b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
		b.openLayers("shop/api:dev")
		b.waitReport()
		b.seeFlat("The archive that was analysed holds 4 layers that are not the 6 the daemon's inspect names")
		if v := b.layers(); v.exact || v.rep != nil {
			t.Error("a report of other layers is drawn")
		}
	})
}

// ---- 4.7: the holders ------------------------------------------------------------------

func TestLayersManyHolders(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		base := fakeengine.Layer{Name: "shared-base", Size: 50_000_000, CreatedBy: "/bin/sh -c #(nop) ADD file:base in / "}
		s.AddImage(0, time.Hour, "app/main:1")
		s.SetImageLayers("app/main:1", base, fakeengine.Layer{Name: "main", Size: 1_000_000, CreatedBy: "COPY . /app"})
		for i := 0; i < 39; i++ {
			ref := fmt.Sprintf("holder/h%02d:1", i)
			s.AddImage(0, time.Hour, ref)
			s.SetImageLayers(ref, base, fakeengine.Layer{Name: ref, Size: 1_000, CreatedBy: "COPY . /srv"})
		}
		// An untagged holder: named by its short ID.
		s.AddImage(0, time.Hour)
	}, width: 200, height: 50})
	// The dangling image holds the base too.
	var dangling string
	for _, img := range b.m.snap.Images {
		if len(img.Refs()) == 0 {
			dangling = img.ID
		}
	}
	if dangling == "" {
		t.Fatal("no untagged image listed")
	}
	b.srv.SetImageLayers(dangling, fakeengine.Layer{Name: "shared-base", Size: 50_000_000, CreatedBy: "/bin/sh -c #(nop) ADD file:base in / "})
	b.exportHome()
	b.openLayers("app/main:1")
	b.readHolders()
	v := b.layers()
	ids, known := v.held(1)
	if !known || len(ids) != 40 {
		t.Fatalf("%d holders of the base, want 40 (known %v)", len(ids), known)
	}
	row := stepRows(b.details())[0]
	if f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(row), "▸")); len(f) < 4 || f[3] != "40" {
		t.Errorf("the row does not say 40: %q", row)
	}
	// The block names as many as the line holds, and the remainder.
	var held string
	for _, l := range strings.Split(b.details(), "\n") {
		if strings.HasPrefix(l, "held by") {
			held = l
		}
	}
	if !strings.Contains(held, "40 other images: ") || !strings.Contains(held, " more") {
		t.Fatalf("held-by line: %q", held)
	}
	var more int
	if _, err := fmt.Sscanf(held[strings.LastIndex(held, "+"):], "+%d more", &more); err != nil {
		t.Fatalf("no remainder in %q", held)
	}
	named := strings.Count(held, " · ") + 1
	if named+more != 40 || named < 2 {
		t.Errorf("%d named + %d more, want 40: %q", named, more, held)
	}
	// The file names every one of them, the untagged one by its short ID.
	file := string(b.captureDialog().data)
	for i := 0; i < 39; i++ {
		if ref := fmt.Sprintf("holder/h%02d:1", i); !strings.Contains(file, ref) {
			t.Errorf("the file does not name %s", ref)
		}
	}
	if short := escID(dangling); !strings.Contains(file, short) || !strings.Contains(held+file, short) {
		t.Errorf("the untagged holder is not named by its short ID %s", short)
	}
	if strings.Contains(file, " more") {
		t.Error("the file leaves holders out")
	}
}

// ---- 5: the tab in the desk -------------------------------------------------------------

func TestLayersTabPositionAndCursorKeys(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
	if got := strings.Join(kindTabs[KindImages], " "); got != "Info Inspect Findings Upstream Layers" {
		t.Fatalf("Images tabs: %s", got)
	}
	b.kind(KindImages)
	b.selectRow(fakeengine.LayersApp)
	b.m.focus = PaneDetails
	b.seeFlat("[Info] Inspect Findings Upstream Layers")
	for i, want := range []string{tabInspect, tabFindings, tabUpstream, tabLayers, tabInfo} {
		b.keys("]")
		if b.m.tabName() != want {
			t.Fatalf("] ×%d is %s, want %s", i+1, b.m.tabName(), want)
		}
	}
	b.keys("[")
	if b.m.tabName() != tabLayers {
		t.Fatalf("[ from Info is %s", b.m.tabName())
	}
	b.seeFlat("Info Inspect Findings Upstream [Layers]")
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()

	at := func(want int, key string) {
		t.Helper()
		b.keys(key)
		if b.m.ly.cursor != want {
			t.Errorf("after %q the cursor is on step %d, want %d", key, b.m.ly.cursor+1, want+1)
		}
	}
	at(7, "G")
	b.seeFlat("8 of 8 · config only")
	at(0, "g")
	b.seeFlat("1 of 8 · made layer L1")
	at(1, "j")
	at(2, "down")
	at(1, "k")
	at(0, "up")
	at(0, "up")
	at(7, "end")
	at(0, "home")
	page := max(b.m.ly.listH-1, 1)
	at(min(page, 7), " ")
	at(0, "pgup")
	at(min(page, 7), "pgdown")
	// No mutating request, and no archive beyond the one analysis.
	b.noMutations("moving on the Layers tab")
	if b.exports() != 1 {
		t.Errorf("%d archive requests", b.exports())
	}
}

func TestLayersKeysStartConsentAndCancel(t *testing.T) {
	// s starts the read, and says how many images that is before it runs.
	b := newBench(t, benchOpts{seed: layersSeed, width: 220, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.seeFlat("s read holders")
	b.srv.SetHistoryDelay(20 * time.Millisecond)
	b.keys("s")
	if b.m.lc.run == nil || b.m.lc.run.total != 17 {
		t.Fatalf("s started no read of the 17 other images: %+v", b.m.lc.run)
	}
	b.seeFlat("Reading the layers of 17 images, one inspect each (GET only)")
	job, _ := b.m.jobs.Get(b.m.lc.run.jobID)
	if job.Title != "read image layers" || job.State != jobs.Running {
		t.Errorf("job %q %s", job.Title, job.State)
	}
	// A second s starts nothing.
	b.keys("s")
	if b.m.jobs.RunningCount() != 1 {
		t.Errorf("%d jobs running", b.m.jobs.RunningCount())
	}
	// Cancelled in the Jobs pane: not known, and s starts it again.
	b.m.jobs.Cancel(job.ID)
	b.until("the read to end", func() bool { return b.m.lc.run == nil })
	b.srv.SetHistoryDelay(0)
	b.settle()
	b.seeFlat("Holders not known: s reads the layers of ")
	if b.layers().sharers != nil {
		t.Error("holders are stated after a cancelled read")
	}
	before := b.histories()
	b.readHolders()
	if got := b.histories() - before; got == 0 || got > 17 {
		t.Errorf("the second read made %d history calls", got)
	}
	b.seeFlat("3 of 5 layers are held by another image here")
	b.notSee("s read holders")
	b.noMutations("the layer read")

	// Enter gives the consent a remote image over the threshold waits for.
	r := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.SetImageSize(fakeengine.LayersApp, 2_400_000_000)
	}, width: 220, height: 50})
	r.openLayers(fakeengine.LayersApp)
	r.seeFlat("2.4GB", "the daemon is remote (tcp://10.0.0.5:2376)", "Enter analyses it; nothing is requested until you do", "⏎ analyse")
	r.run(80 * time.Millisecond)
	if r.exports() != 0 {
		t.Fatalf("%d archive requests before consent", r.exports())
	}
	r.keys("enter")
	r.waitReport()
	if r.exports() != 1 {
		t.Errorf("%d archive requests after Enter", r.exports())
	}

	// c cancels the analysis in progress.
	c := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.ExportChunkDelay = 500 * time.Millisecond
	}, width: 220, height: 50})
	c.openLayers("shop/api:dev")
	c.until("the analysis", func() bool { return c.m.fx.run != nil })
	c.seeFlat("c cancel")
	id := c.m.fx.run.jobID
	c.keys("c")
	c.until("the cancellation", func() bool {
		j, _ := c.m.jobs.Get(id)
		return j.State == jobs.Cancelled
	})
	c.srv.ExportChunkDelay = 0
}

func TestLayersKeysAreInTheHelpAndTheFooter(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 220, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.seeFlat("↑↓ step", "s read holders", "W save build", "[ ] tab")
	help := ansi.Strip(strings.Join(b.m.helpLines(100), "\n"))
	i := strings.Index(help, "Images › Layers tab")
	if i < 0 {
		t.Fatalf("the help has no Layers section:\n%s", help)
	}
	sect := help[i:]
	if j := strings.Index(sect, "Images › Upstream tab"); j > 0 {
		sect = sect[:j]
	}
	for _, want := range []string{"↑ ↓  j k", "g G  PgUp PgDn", "  s  ", "  Enter  ", "  c  ", "  W  ",
		"read every listed image's layers", "cancel the analysis in progress", "save the whole build to a text file",
		"content, not stored bytes"} {
		if !strings.Contains(sect, want) {
			t.Errorf("the help's Layers section does not say %q:\n%s", want, sect)
		}
	}
}

// ---- read-only, and what is never shown ------------------------------------------------

func TestLayersInReadOnlyModeSendsOnlyGets(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, readOnly: true, width: 200, height: 50})
	b.exportHome()
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.readHolders()
	b.seeFlat("3 of 5 layers are held by another image here", "the archive's history (exact")
	b.keys("G", "g", "j")
	b.captureDialog()
	b.keys("esc")
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("the tab sent %s %s", r.Method, r.Path)
		}
	}
	b.noMutations("the Layers tab in read-only mode")
}

// The daemon's created_by can hold a build argument's value and bytes that
// act on a terminal. Neither reaches the cache, the screen, the capture, a
// job or the status line: the masked, escaped form is the only form.
func TestLayersNeverHoldTheDaemonsRawInstruction(t *testing.T) {
	const esc1 = "\x1b[2J\x1b]0;owned\x07"
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.SetImageHistory(fakeengine.LayersBase,
			fakeengine.HistoryStep{CreatedBy: "/bin/sh -c #(nop) ADD file:1 in / " + esc1, Size: 28_400_000},
			fakeengine.HistoryStep{CreatedBy: "RUN |1 API_KEY=" + layersToken + " /bin/sh -c curl -H 'Authorization: Bearer ghp_0123456789abcdefghijklmnopqrstuvwxyzAB' https://x # buildkit", Size: 1},
		)
		// No archive without consent: the daemon's list is what is drawn.
		s.SetImageSize(fakeengine.LayersBase, 2_400_000_000)
	}, width: 220, height: 50})
	b.exportHome()
	b.openLayers(fakeengine.LayersBase)
	b.readHolders()

	held := func(what, s string) {
		t.Helper()
		if strings.Contains(s, layersToken) {
			t.Errorf("%s holds the build argument's value", what)
		}
		if strings.Contains(s, "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB") {
			t.Errorf("%s holds a secret-shaped token unmasked", what)
		}
		if strings.Contains(s, "\x1b]0;owned") || strings.Contains(s, "\x1b[2J") {
			t.Errorf("%s holds the daemon's escape sequence", what)
		}
	}
	for id, info := range b.m.lc.byID {
		for _, st := range info.steps {
			held("the cache ("+id+")", st.instr)
		}
	}
	held("the frame", b.m.View())
	b.keys("j")
	held("the frame on the RUN step", b.m.View())
	b.seeFlat("(build args: API_KEY)")
	file := b.captureDialog().data
	held("the capture", string(file))
	noEscapes(t, "layers capture", file)
	b.keys("esc")
	for _, j := range b.m.jobs.List() {
		held("job "+j.Title, j.Title+j.Target+j.Status+j.Err+strings.Join(j.Log, "\n"))
	}
	held("the status line", b.m.status.text)
}

// ---- 6: keeping the build ----------------------------------------------------------------

func TestCaptureLayersIsTheWholeBuildAtAnyWidth(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 80, height: 24})
	b.exportHome()
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	b.readHolders()
	b.keys("G") // scrolled: the last step is in view, the first is not
	if rows := stepRows(b.details()); len(rows) == 0 || strings.Contains(strings.Join(rows, "\n"), "ADD file:51c0ffee") {
		t.Fatalf("the tab is not scrolled past the first step:\n%s", b.details())
	}
	c := b.captureDialog()
	narrow := c.data
	if got := b.exportPath(); !strings.HasSuffix(got, "/docker-desk-layers-acme-steps-1-"+exportDay.Format("20060102")+".txt") {
		t.Errorf("prefilled path %q", got)
	}
	if c.kind != "layers" || c.pane != "Layers" {
		t.Errorf("kind %q pane %q", c.kind, c.pane)
	}
	b.keys("esc")
	b.resize(200, 50)
	wide := b.captureDialog().data
	b.keys("esc")
	if string(narrow) != string(wide) {
		t.Fatalf("80 and 200 columns differ:\n%s\n---\n%s", narrow, wide)
	}
	noEscapes(t, "layers", wide)
	file := string(wide)
	for _, want := range []string{
		"# pane: Layers\n", "# object: acme/steps:1\n",
		"3 of 5 layers are held by another image here",
		"\nstep 1 · L1\n", "ADD file:51c0ffee in /",
		"\nstep 2 · config only\n", "it changed the image's configuration, not its files",
		"\nstep 3 · L2\n", "(build args: BUILD_TOKEN)", "findings 1 on this layer: secret.buildarg",
		"\nstep 7 · L5\n", "held by  1 other image: acme/steps-base:1",
		"\nstep 8 · config only\n",
		"matches the config's diff_id",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the file lacks %q:\n%s", want, file)
		}
	}
	if strings.Contains(file, layersToken) {
		t.Fatal("the file holds the build argument's value")
	}
	// The cursor is not part of the file: the same bytes from another step.
	b.keys("g")
	if again := b.captureDialog().data; string(again) != string(wide) {
		t.Error("the file depends on the cursor")
	}
	b.noMutations("capturing the Layers tab")
}

func TestCaptureLayersWithoutTheArchive(t *testing.T) {
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		id := s.AddImage(0, time.Hour, "built/eighteen:1")
		s.SetImageFixture(id, builtImage(7, 18), sizesOf(7, 100_000_000)...)
	}, width: 120, height: 40})
	b.exportHome()
	b.openLayers("built/eighteen:1")
	c := b.captureDialog()
	file := string(c.data)
	for _, want := range []string{
		"The archive was not read: what each layer wrote, its digest check and its findings are not in this file.",
		"18 steps · 7 layers · the daemon's history, bound by counting",
		"\nstep 1 · L1\n", "\nstep 18 · L7\n", "\nstep 7 · config only\n",
		"wrote    not known yet: the archive has not been read",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the file lacks %q:\n%s", want, file)
		}
	}
	if !strings.Contains(c.holds, "the archive was not read") {
		t.Errorf("the dialog does not say the contents are missing: %q", c.holds)
	}
	if b.exports() != 0 {
		t.Error("capturing requested the archive")
	}
}

// W is offered on Layers and on no tab that is not text: one set of rules.
func TestCaptureOffersLayersAndNotTheTabsThatAreNotText(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
	b.exportHome()
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	if c := b.captureDialog(); c.kind != "layers" {
		t.Fatalf("W on Layers collected %q", c.kind)
	}
	b.keys("esc")
	b.seeFlat("W save build")
	for _, tab := range []string{tabFindings, tabUpstream} {
		b.toTab(KindImages, fakeengine.LayersApp, tab)
		b.m.status.text = ""
		b.keys("W")
		if b.m.dlg != nil || b.m.status.text != "" {
			t.Errorf("W did something on %s", tab)
		}
	}
	// Before the image's own layers are read there is nothing to save, and
	// it is said.
	b.toTab(KindImages, "busybox:1.36", tabInfo)
	for _, img := range b.m.snap.Images {
		delete(b.m.lc.byID, img.ID)
	}
	b.m.tab[KindImages] = len(kindTabs[KindImages]) - 1
	b.keys("W")
	if b.m.dlg != nil || !strings.HasPrefix(b.m.status.text, "Nothing to save: this image's layers") {
		t.Errorf("W before the layers were read: dialog %v, status %q", b.m.dlg != nil, b.m.status.text)
	}
}

// ---- 7.1: the README ------------------------------------------------------------------

func TestDocumentedLayersTab(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		"### Layers — what the build did, step by step",
		"`Info`, `Inspect`, `Findings`, `Upstream`, `Layers`",
		"| `s` | Layers: read every listed image's layers",
		"| `Enter` | Layers: analyse",
		"| `c` | Layers: cancel the analysis",
		"| `g` `G` | Layers: first / last step",
		"| `PgUp` `PgDn` `Space` | Layers: a page of steps",
		"| `" + keyCapture + "` | save the focused pane's text to a file",
		"`~/docker-desk-layers-shop-api-dev-<YYYYMMDD>.txt`",
		"the archive's history",
		"the daemon's history",
		"content, not stored bytes",
		"`EmptyLayer`",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
	// Every key the README's Layers table lists is one the tab handles.
	b := newBench(t, benchOpts{seed: layersSeed})
	b.openLayers(fakeengine.LayersApp)
	for _, key := range []string{"s", "enter", "c", "g", "G", "pgup", "pgdown", " ", "j", "k", "up", "down"} {
		if !b.m.onLayersKey(key) {
			t.Errorf("the README lists %q and the tab does not handle it", key)
		}
	}
	b.until("the read s started", func() bool { return b.m.lc.run == nil })
}

func countShared(v lyView) int {
	n := 0
	for _, ids := range v.sharers {
		if len(ids) > 0 {
			n++
		}
	}
	return n
}

// Review findings, kept fixed.
func TestLayersReviewFindingsStayFixed(t *testing.T) {
	// A history that does not bind still names who holds which layer, on
	// screen and in the file.
	b := remoteBench(t, remoteEndpoint, benchOpts{seed: func(s *fakeengine.Server) {
		layersSeed(s)
		s.SetImageSize(fakeengine.LayersApp, 2_400_000_000)
	}, width: 220, height: 60})
	b.exportHome()
	b.openLayers(fakeengine.LayersApp)
	b.readHolders()
	if v := b.layers(); v.bound || v.sharers == nil {
		t.Fatalf("bound %v, holders known %v", v.bound, v.sharers != nil)
	}
	b.seeFlat("Held, by layer number (no step is bound to one): L1 by acme/steps-base:1 · L3 by acme/steps-base:1 · L5 by acme/steps-base:1")
	if file := string(b.captureDialog().data); !strings.Contains(file, "L1 by acme/steps-base:1 · L3 by acme/steps-base:1 · L5 by acme/steps-base:1") {
		t.Errorf("the file does not name the holders:\n%s", file)
	}
	b.keys("esc")

	// Every step marked empty_layer over an image that has layers is not
	// "exact": nothing binds, and the reason is given.
	b.keys("enter")
	r := b.waitReport()
	for i := range r.Steps {
		r.Steps[i].Empty, r.Steps[i].Layer = true, 0
	}
	if v := b.layers(); v.bound || v.why == "" {
		t.Errorf("all-empty history over 5 layers: bound %v, why %q", v.bound, v.why)
	}

	// A failed read never replaces layers already held, and s does not ask
	// again for an image whose own read is on its way.
	id := b.layers().id
	b.m.lc.run = &layerRun{layer: map[string]layerInfo{id: {err: "transient"}}, ver: 1, jobID: -1}
	b.m.collectLayers()
	if !b.m.layerKnown(id) {
		t.Error("a failure replaced a good cache entry")
	}
	delete(b.m.lc.byID, id)
	b.m.lc.own = map[string]bool{id: true}
	for _, todo := range b.m.layersToRead() {
		if todo == id {
			t.Error("s would read an image whose own read is in flight")
		}
	}
	delete(b.m.lc.own, id)
}

func TestLayersSizesNeedAnAgreeingHistory(t *testing.T) {
	b := newBench(t, benchOpts{seed: layersSeed, width: 200, height: 50})
	b.openLayers(fakeengine.LayersApp)
	b.waitReport()
	id := b.layers().id
	good := b.m.lc.byID[id]
	if v := b.layers(); v.sizeSrc != lySrcHistory || v.sizes[1] != 61_200_000 {
		t.Fatalf("sizes %v from %q", v.sizes, v.sizeSrc)
	}
	// A size on a step the config says made no layer: the two lists do not
	// agree, and the daemon's sizes are not attributed through them.
	bad := good
	bad.steps = append([]histStep(nil), good.steps...)
	bad.steps[1].size = 5_000 // ENV, empty_layer
	b.m.putLayers(id, bad)
	if v := b.layers(); v.sizeSrc == lySrcHistory {
		t.Errorf("sizes attributed from a history that disagrees with the archive: %v", v.sizes)
	}
	// A negative size is never added up.
	neg := good
	neg.steps = append([]histStep(nil), good.steps...)
	neg.steps[2].size = -1
	b.m.putLayers(id, neg)
	if v := b.layers(); v.sizeSrc == lySrcHistory {
		t.Errorf("a negative size was attributed: %v", v.sizes)
	}
	// A cut history is not counted from.
	cut := layerInfo{diffs: []string{"sha256:a"}, steps: []histStep{{instr: "ADD x", size: 9}}, cut: true}
	b.m.fx.cache = nil
	b.m.putLayers(id, cut)
	if v := b.layers(); v.bound || !v.cut {
		t.Errorf("a cut history was bound by counting: bound %v cut %v", v.bound, v.cut)
	}
}
