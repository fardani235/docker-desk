package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// openPackages analyses an image from its Findings tab and opens its
// Packages view.
func (b *bench) openPackages(ref string) *forensics.Report {
	b.t.Helper()
	b.openFindings(ref)
	rep := b.waitReport()
	b.keys("i")
	if !b.m.pk.open {
		b.t.Fatalf("i did not open the Packages view:\n%s", b.screen())
	}
	return rep
}

func (b *bench) pkgShown() []forensics.PkgRow {
	_, rows := b.m.pkgRows()
	return rows
}

func (b *bench) pkgCurrent() forensics.PkgRow {
	b.t.Helper()
	rows := b.pkgShown()
	i := b.m.pkgCursor(rows)
	if i < 0 {
		b.t.Fatalf("no row highlighted:\n%s", b.screen())
	}
	return rows[i]
}

func TestPackagesViewRendersAndScrollsAtBothLayouts(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {120, 30}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			b := newBench(t, benchOpts{width: sz[0], height: sz[1]})
			b.openPackages("python:3.12-slim")
			b.see("Packages — ", "python:3.12-slim", "SBOM", "SPDX-2.3 · 70 packages", "installed",
				"dpkg · /var/lib/dpkg/status (layer 4) · 66 packages", "no apk or rpm database in any layer",
				"3 undeclared", "1 version differs", "1 declared only", "6 not checkable", "62 both",
				"NAME", "VERSION", "LICENCE", "TYPE", "LAYER", "STATE", "by name · showing all")
			b.assertFrameGeometry()
			b.dump("packages")
			rows := b.pkgShown()
			if len(rows) != 73 {
				t.Fatalf("%d rows, want 73", len(rows))
			}
			for i := 0; i < len(rows)+3; i++ {
				b.keys("j")
				b.assertFrameGeometry()
			}
			if b.m.pk.off == 0 {
				t.Errorf("the list did not scroll (list height %d)", b.m.pk.listH)
			}
			b.see(fmt.Sprintf(" %d of %d ", len(rows), len(rows)), "zlib1g")
			b.keys("g")
			b.screen()
			if b.m.pk.cursor != 0 || b.m.pk.off != 0 {
				t.Errorf("g did not return to the top: cursor %d off %d", b.m.pk.cursor, b.m.pk.off)
			}
			b.see("SBOM", "SPDX-2.3") // the header is back in view at the top
			for i := 0; i < 4; i++ {
				b.keys("pgdown")
				b.assertFrameGeometry()
			}
		})
	}
}

// TestPackagesViewClosesOntoTheDeskAsItWas: Esc puts back every pane, the
// tab, and the finding the cursor was on.
func TestPackagesViewClosesOntoTheDeskAsItWas(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openFindings("python:3.12-slim")
	b.waitReport()
	b.until("container facts", func() bool { return b.m.fx.factsFor == b.m.dataVersion && !b.m.fx.factsBusy })
	b.keys("j", "j", "j")
	b.settle()
	before := b.screen()
	cursorKey, focus, kind, tab := b.m.fx.cursorKey, b.m.focus, b.m.kind, b.m.tabName()
	b.keys("i")
	b.see("Packages — ")
	b.keys("j", "j", "s", "o", "/", "l", "i", "b", "enter")
	b.keys("esc") // clears the search
	if !b.m.pk.open {
		t.Fatal("Esc with a search closed the view instead of clearing it")
	}
	b.keys("esc")
	b.settle()
	if b.m.pk.open {
		t.Fatal("Esc did not close the view")
	}
	if b.m.fx.cursorKey != cursorKey || b.m.focus != focus || b.m.kind != kind || b.m.tabName() != tab {
		t.Errorf("the desk changed: cursor %q→%q focus %v→%v", cursorKey, b.m.fx.cursorKey, focus, b.m.focus)
	}
	if after := b.screen(); after != before {
		t.Errorf("the desk is not as it was.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// Opened again on the same image, the view is where it was left.
	b.keys("i")
	b.see("by state")
}

// TestPackagesSearchOrderAndStateFilter: / searches name, version, licence
// and purl; o orders; s shows one state at a time; the cursor stays on its
// package through all of them.
func TestPackagesSearchOrderAndStateFilter(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 40})
	b.openPackages("python:3.12-slim")

	// Search, by each of the four fields.
	for q, want := range map[string]string{"netcat": "netcat-openbsd", "3.0.11": "libssl3", "Zlib": "zlib1g", "pkg:pypi/req": "requests"} {
		b.keys("/")
		b.typeText(q)
		b.keys("enter")
		rows := b.pkgShown()
		if len(rows) != 1 || rows[0].Name() != want {
			var names []string
			for _, r := range rows {
				names = append(names, r.Name())
			}
			t.Errorf("search %q: %v, want [%s]", q, names, want)
		}
		b.see("search: " + q)
		b.keys("esc")
	}
	// Esc while typing puts back the search as it was.
	b.keys("/")
	b.typeText("curl")
	b.keys("esc")
	if b.m.pk.query != "" || len(b.pkgShown()) != 73 {
		t.Errorf("Esc while typing: query %q, %d rows", b.m.pk.query, len(b.pkgShown()))
	}

	// The cursor follows the package, not the row index.
	b.keys("/")
	b.typeText("curl")
	b.keys("enter")
	for b.pkgCurrent().Name() != "libcurl4" {
		b.keys("j")
	}
	b.keys("esc") // clear the search: libcurl4 moves far down the list
	if r := b.pkgCurrent(); r.Name() != "libcurl4" || b.m.pk.cursor < 10 {
		t.Errorf("the cursor did not follow libcurl4: on %s at %d", r.Name(), b.m.pk.cursor)
	}
	b.see("▸ libcurl4")

	// s: the disagreements first, since they are why the view is opened.
	want := []struct {
		label string
		n     int
	}{{"disagreements", 5}, {"undeclared", 3}, {"version differs", 1}, {"declared only", 1}, {"not checkable", 6}, {"both", 62}, {"all", 73}}
	for _, w := range want {
		b.keys("s")
		b.see("showing " + w.label)
		if n := len(b.pkgShown()); n != w.n {
			t.Errorf("%s: %d rows, want %d", w.label, n, w.n)
		}
		b.assertFrameGeometry()
	}
	b.keys("s", "s") // undeclared
	if r := b.pkgCurrent(); r.Name() != "libcurl4" {
		t.Errorf("libcurl4 is undeclared and should still be highlighted, not %s", r.Name())
	}
	b.see("installed, and the SBOM does not list it", "added by", "layer 4 · RUN apt-get update && apt-get install -y curl netcat-openbsd")
	b.keys("S")

	// o: name, state, layer, licence.
	for _, w := range []struct{ order, first string }{{"state", "curl"}, {"layer", "adduser"}, {"licence", "ca-certificates"}, {"name", "adduser"}} {
		b.keys("o")
		b.see("by " + w.order)
		if r := b.pkgCurrent(); r.Name() != "libcurl4" {
			t.Errorf("by %s: the cursor left libcurl4 for %s", w.order, r.Name())
		}
		if first := b.pkgShown()[0].Name(); w.order != "licence" && first != w.first {
			t.Errorf("by %s: first row %s, want %s", w.order, first, w.first)
		}
	}
	// A state that leaves nothing says so and keeps the frame.
	b.keys("/")
	b.typeText("no-such-package")
	b.keys("enter")
	b.see("No package matches", "no rows")
	b.assertFrameGeometry()
}

// TestUndeclaredIsAFinding: the Findings tab names the undeclared packages
// in one supply finding, and the drift as a hint.
func TestUndeclaredIsAFinding(t *testing.T) {
	b := newBench(t, benchOpts{width: 220, height: 50})
	b.openFindings("python:3.12-slim")
	b.waitReport()
	b.cursorTo("supply.undeclared")
	b.see("supply.undeclared", "3 installed packages the SBOM does not list: curl, libcurl4, netcat-openbsd",
		"source   image · certain · layer 4", "apt-get install -y curl netcat-openbsd", "cannot tell which")
	b.cursorTo("supply.sbom-drift")
	b.see("1 declared OS package is not installed, 1 installed at another version")
	b.see("i packages")
}

func TestPackagesViewInReadOnlyMode(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true, width: 80, height: 24})
	b.openPackages("python:3.12-slim")
	before := len(b.srv.Requests())
	b.keys("j", "s", "o", "/", "c", "enter", "esc", "esc")
	b.settle()
	b.noMutations("browsing packages in read-only mode")
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("read-only desk sent %s", r)
		}
	}
	if n := b.exports(); n != 1 {
		t.Errorf("%d exports: the view must not read the archive again", n)
	}
	_ = before
}

// TestZstdImageBrowsesItsSBOM: the layers cannot be read, so what is
// installed is unknown, and said; the SBOM's list is still there.
func TestZstdImageBrowsesItsSBOM(t *testing.T) {
	declared := []fixture.Pkg{{Name: "libc6", Version: "2.36-9"}, {Name: "openssl", Version: "3.0.15"}, {Name: "left-pad", Version: "1.3.0", PURL: "pkg:npm/left-pad@1.3.0"}}
	img := fixture.Image{
		Cmd: []string{"/bin/sh"}, RepoTags: []string{"python:3.12-slim"},
		Layers: []fixture.Layer{{CreatedBy: "ADD rootfs /", Zstd: true, Files: []fixture.File{{Path: "var/lib/dpkg/status", Body: fixture.DpkgStatus(declared[:2])}}}},
		SBOM:   true, SBOMDoc: fixture.SPDX("debian", declared),
	}
	b := newBench(t, benchOpts{width: 80, height: 24, seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SetImageArchive("python:3.12-slim", fixture.OCI(img).Archive)
	}})
	b.openPackages("python:3.12-slim")
	b.see("dpkg, apk and rpm unknown: layer 1 could not be read", "zstd", "3 not checkable")
	b.keys("j") // the header first; then the list
	b.see("libc6", "left-pad")
	b.notSee("undeclared")
	b.assertFrameGeometry()
	b.keys("j", "j")
	b.see("not checkable: what dpkg has installed is unknown")
	b.keys("g")
	b.see("SBOM       SPDX-2.3 · 3 packages")
}

// TestLargeSBOMBrowsesInTheView: an SBOM above the JSON bound, through the
// fake daemon and into the view.
func TestLargeSBOMBrowsesInTheView(t *testing.T) {
	var doc strings.Builder
	doc.WriteString(`{"spdxVersion":"SPDX-2.3","comment":"` + strings.Repeat("x", 5<<20) + `","packages":[`)
	var installed []fixture.Pkg
	for i := 0; i < 300; i++ {
		if i > 0 {
			doc.WriteString(",")
		}
		fmt.Fprintf(&doc, `{"name":"lib%03d","versionInfo":"1.%d","licenseDeclared":"MIT","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:deb/debian/lib%03d@1.%d"}]}`, i, i, i, i)
		installed = append(installed, fixture.Pkg{Name: fmt.Sprintf("lib%03d", i), Version: fmt.Sprintf("1.%d", i)})
	}
	doc.WriteString(`]}`)
	installed = append(installed, fixture.Pkg{Name: "zz-sneaky", Version: "6.6.6"})
	img := fixture.Image{
		Cmd: []string{"/bin/sh"}, RepoTags: []string{"python:3.12-slim"},
		Layers: []fixture.Layer{{CreatedBy: "ADD rootfs /", Files: []fixture.File{{Path: "var/lib/dpkg/status", Body: fixture.DpkgStatus(installed)}}}},
		SBOM:   true, SBOMDoc: []byte(doc.String()),
	}
	b := newBench(t, benchOpts{width: 120, height: 40, seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SetImageArchive("python:3.12-slim", fixture.OCI(img).Archive)
	}})
	b.openPackages("python:3.12-slim")
	b.see("SPDX-2.3 · 300 packages", "300 both", "1 undeclared")
	b.keys("o") // by state: the undeclared one first; the cursor stays on its package
	b.see("by state", "▸ lib000")
	b.keys("g")
	b.see("▸ zz-sneaky")
}

func TestPackagesNeedAFinishedAnalysis(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.ExportChunkDelay = 40e6
	}})
	b.openFindings("python:3.12-slim")
	b.keys("i")
	if b.m.pk.open {
		t.Fatal("the view opened without a report")
	}
	b.see("opens once the analysis has finished")
}

func TestPackagesHelp(t *testing.T) {
	b := newBench(t, benchOpts{})
	found := false
	for _, l := range b.m.helpLines(80) {
		if strings.Contains(l, "Packages view (i, from Images › Findings)") {
			found = true
		}
	}
	if !found {
		t.Error("the help does not describe the Packages view")
	}
}
