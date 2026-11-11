package ui

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// ---- helpers ----------------------------------------------------------------------

func (b *bench) openFilesOn(k Kind, name string) {
	b.t.Helper()
	b.kind(k)
	b.selectRow(name)
	b.keys("F")
	if !b.m.fs.open {
		b.t.Fatalf("F did not open the Files view on %s:\n%s", name, b.screen())
	}
	b.settle()
}

// readArchive is Enter, and the job's end.
func (b *bench) readArchive() {
	b.t.Helper()
	b.keys("enter")
	b.until("the archive to be read", func() bool { return b.m.filesBase() != nil && b.m.fs.run == nil })
	b.settle()
}

// filesGo puts the cursor on a path, opening the directories above it, as
// the operator would by moving.
func (b *bench) filesGo(p string) {
	b.t.Helper()
	for d := parentOf(p); d != ""; d = parentOf(d) {
		b.m.fs.expanded[d] = true
	}
	b.m.fs.expVer++
	b.m.fs.cursor, b.m.fs.top = p, false
	b.m.dirty = true
	b.settle()
	if v, _ := b.m.filesView(); !v.Has(p) {
		b.t.Fatalf("%s is not a row:\n%s", p, b.screen())
	}
}

func parentOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

func (b *bench) changeRequests() int {
	return b.srv.CountRequests("GET", `^/(v[0-9.]+/)?containers/[^/]+/changes$`)
}

// screenLines returns the frame's lines, without colour.
func (b *bench) screenLines() []string { return strings.Split(b.screen(), "\n") }

func (b *bench) lineWith(sub string) string {
	b.t.Helper()
	for _, l := range b.screenLines() {
		if strings.Contains(l, sub) {
			return l
		}
	}
	b.t.Fatalf("no line shows %q:\n%s", sub, b.screen())
	return ""
}

// ---- 4.1 opening and closing ------------------------------------------------------

func TestFilesOpensAndClosesWhereItWas(t *testing.T) {
	b := newBench(t, benchOpts{})
	for _, c := range []struct {
		kind Kind
		name string
		want string
	}{{KindImages, "nginx:1.27", "Nothing has been read"}, {KindContainers, "shop-web-1", "shop-web-1 ·"}} {
		b.kind(c.kind)
		b.selectRow(c.name)
		b.m.focus = PaneDetails
		key := b.m.sel[c.kind].key
		b.keys("F")
		b.see("Files — the image's", c.want)
		// Keys go to the view, not to the panes behind it.
		b.keys("j", "j", "G", "]", "1", "tab", "[")
		if b.m.focus != PaneDetails || b.m.kind != c.kind || b.m.sel[c.kind].key != key {
			t.Fatalf("%s: the desk moved behind the view: focus %v kind %v", c.name, b.m.focus, b.m.kind)
		}
		b.keys("esc")
		if b.m.fs.open || b.m.focus != PaneDetails || b.m.kind != c.kind || b.m.sel[c.kind].key != key {
			t.Fatalf("%s: not restored: open %v focus %v kind %v sel %q", c.name, b.m.fs.open, b.m.focus, b.m.kind, b.m.sel[c.kind].key)
		}
		b.notSee("Files — the image's")
		// From every pane, and F closes it as Esc does.
		for p := PaneKinds; p < paneCount; p++ {
			b.m.focus = p
			b.keys("F")
			if !b.m.fs.open {
				t.Fatalf("F did not open the view from %s", paneNames[p])
			}
			b.keys("F")
			if b.m.fs.open || b.m.focus != p {
				t.Fatalf("F did not close the view back to %s", paneNames[p])
			}
		}
	}
	// On a volume: one sentence, no view.
	b.kind(KindVolumes)
	b.keys("F")
	if b.m.fs.open {
		t.Fatal("F opened the view on a volume")
	}
	b.see("F opens the Files view on an image or a container")
	// Typing a filter, F is a letter.
	b.kind(KindImages)
	b.m.focus = PaneList
	b.keys("/", "F")
	if b.m.fs.open {
		t.Fatal("F typed into the filter opened the view")
	}
}

// ---- 4.2 nothing before Enter, one job after, cancel keeps nothing ------------------

func TestFilesReadsNothingUntilEnter(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindImages)
	b.selectRow("nginx:1.27")
	b.settle()
	b.srv.ResetRequests()
	b.keys("F")
	b.see("nginx:1.27 ·", "linux/amd64", "3 layers", "188MB (the daemon's size)", "Nothing has been read", "Enter reads the image's archive")
	b.run(150 * time.Millisecond)
	if reqs := b.srv.Requests(); len(reqs) != 0 {
		t.Fatalf("opening the view sent %d request(s): %v", len(reqs), reqs)
	}
	b.readArchive()
	if n := b.exports(); n != 1 {
		t.Fatalf("%d archive requests", n)
	}
	// Beside it, only the reads every finished job is followed by: the
	// lists, and the highlighted image's inspect.
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" || strings.Contains(r.Path, "/changes") || strings.Contains(r.Path, "/archive") {
			t.Fatalf("an unexpected request: %v", r)
		}
	}
	b.see("entries indexed, every one the archive holds", "NAME", "SIZE", "TYPE", "LAYER")
	if j := b.m.jobs.List()[0]; j.Title != "read the image archive" || j.State != jobs.Succeeded {
		t.Fatalf("job %q %v", j.Title, j.State)
	}
	// Reopening is instant: the archive is read once.
	b.keys("esc", "F")
	b.see("entries indexed")
	if n := b.exports(); n != 1 {
		t.Fatalf("reopening read the archive again (%d)", n)
	}
}

func TestFilesReadIsCancellable(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.ExportChunkDelay = 40 * time.Millisecond
		big := fixture.DemoFiles("nginx:1.27")
		big.Layers[0].Files = append(big.Layers[0].Files, fixture.File{Path: "opt/blob", Body: make([]byte, 2<<20)})
		s.SetImageArchive("nginx:1.27", fixture.Legacy(big).Archive)
	}})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.keys("enter")
	b.see("reading", "the image's archive:", "c cancels")
	b.keys("c")
	b.until("the job to end", func() bool { return b.m.fs.run == nil })
	b.see("Cancelled: the stream was closed and nothing was kept. Enter reads it again.")
	if b.m.filesBase() != nil || len(b.m.fs.cache) != 0 {
		t.Fatal("a cancelled read kept an index")
	}
	if j := b.m.jobs.List()[0]; j.State != jobs.Cancelled {
		t.Fatalf("job %v", j.State)
	}
	// Closing the view while it reads cancels it too.
	b.keys("enter")
	b.see("reading")
	b.keys("esc")
	b.until("the job to end", func() bool { return b.m.fs.run == nil })
	if b.m.filesBase() != nil {
		t.Fatal("closing kept an index")
	}
	if j := b.m.jobs.List()[0]; j.State != jobs.Cancelled {
		t.Fatalf("closing did not cancel: %v", j.State)
	}
	b.noMutations("cancelling")
}

// ---- 4.3 the tree at 80 columns ----------------------------------------------------

func TestFilesTreeAt80Columns(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 40})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.readArchive()
	b.assertFrameGeometry()
	b.keys("j") // the first key brings the highlighted row into view
	b.filesGo("/usr")
	b.keys("right")
	b.see("- usr/")
	want := []string{"  bin/", "  lib/", "  sbin/", "  share/"}
	order := func() []string {
		var got []string
		for _, l := range b.screenLines() {
			for _, w := range want {
				if strings.Contains(l, "┃     + "+strings.TrimSpace(w)) {
					got = append(got, w)
				}
			}
		}
		return got
	}
	if got := order(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("by name: %q\n%s", got, b.screen())
	}
	b.keys("s")
	b.see("by size")
	bySize := []string{"  bin/", "  sbin/", "  lib/", "  share/"}
	if got := order(); strings.Join(got, ",") != strings.Join(bySize, ",") {
		t.Fatalf("by size: %q\n%s", got, b.screen())
	}
	if b.m.fs.cursor != "/usr" {
		t.Fatalf("ordering moved the cursor to %s", b.m.fs.cursor)
	}
	// The columns, at 80: name, size, type, layer.
	row := b.lineWith("+ bin/")
	if !strings.Contains(row, "2.2kB") || !strings.Contains(row, "dir 3") {
		t.Fatalf("row %q", row)
	}
	b.filesGo("/usr/sbin/nginx")
	row = b.lineWith("nginx  ")
	if !strings.Contains(row, "1.4kB") || !strings.Contains(row, "file") || !strings.Contains(row, "L2") {
		t.Fatalf("file row %q", row)
	}
	// Left goes to the parent, then closes it.
	b.keys("left")
	if b.m.fs.cursor != "/usr/sbin" {
		t.Fatalf("left: %s", b.m.fs.cursor)
	}
	b.keys("left")
	b.see("+ sbin/")
	// A symbolic link is shown with its target and does not open.
	b.filesGo("/bin/sh")
	b.keys("right")
	b.see("sh -> /usr/bin/bash", "never followed")
	b.assertFrameGeometry()
}

func TestCompactCount(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1k", 12_345: "12k", 200_000: "200k", 1_234_567: "1.2M"} {
		if got := compactCount(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func TestLeftFitKeepsTheBasename(t *testing.T) {
	got := leftFit("/usr/local/lib/python3.12/site-packages/x/python3.12", 24)
	if ansi.StringWidth(got) != 24 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "x/python3.12") {
		t.Fatalf("%q", got)
	}
	if got := leftFit("short", 8); got != "short   " {
		t.Fatalf("%q", got)
	}
}

// ---- 4.4 the filter --------------------------------------------------------------

func TestFilesFilterShowsAncestorsAndClearsOntoThePath(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFilesOn(KindImages, "python:3.12-slim")
	b.readArchive()
	b.keys("/")
	b.typeText("requests")
	b.keys("enter")
	b.see("filter: requests", "2 matches of", "searched")
	for _, a := range []string{"- usr/", "- local/", "- lib/", "- python3.12/", "- site-packages/", "- requests/", "__init__.py"} {
		b.see(a)
	}
	b.notSee("etc/")
	b.keys("G")
	deep := "/usr/local/lib/python3.12/site-packages/requests/__init__.py"
	if b.m.fs.cursor != deep {
		t.Fatalf("cursor %s", b.m.fs.cursor)
	}
	b.keys("esc")
	if b.m.fs.filter != "" || b.m.fs.cursor != deep || !b.m.fs.open {
		t.Fatalf("filter %q cursor %s open %v", b.m.fs.filter, b.m.fs.cursor, b.m.fs.open)
	}
	if l := b.lineWith("__init__.py"); !strings.HasPrefix(strings.TrimLeft(l, "┃ "), "▸") {
		t.Fatalf("the cursor is not drawn on its path: %q", l)
	}
	b.see("etc/")
	// No match says how many were searched.
	b.keys("/")
	b.typeText("zzz")
	b.see(`No path matches "zzz" —`, "searched.")
	b.keys("esc") // undoes the typing
	if b.m.fs.filter != "" {
		t.Fatalf("filter %q", b.m.fs.filter)
	}
}

func TestFilesFilterSaysItMayMiss(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		img := fixture.DemoFiles("nginx:1.27")
		img.Layers[1].Zstd = true
		s.SetImageArchive("nginx:1.27", fixture.OCI(img).Archive)
	}})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.readArchive()
	b.see("layer 2: the layer is zstd-compressed", "no path is shown, or said to be")
	// What layer 1 wrote may have been replaced or removed by layer 2.
	b.filesGo("/etc/passwd")
	if row := b.lineWith("passwd"); !strings.Contains(row, "L1?") {
		t.Fatalf("attribution not marked unsure: %q", row)
	}
	b.see("layer      1 of 3", "unsure     layer 2 was not read (the layer is zstd-compressed")
	b.filesGo("/docker-entrypoint.sh")
	if row := b.lineWith("docker-entrypoint.sh"); !strings.Contains(row, "L3 ") || strings.Contains(row, "L3?") {
		t.Fatalf("a path above every unread layer is certain: %q", row)
	}
	b.keys("/")
	b.typeText("nginx")
	b.see("the filter may miss a path the image holds")
}

// ---- 4.5 the detail ---------------------------------------------------------------

func twelveLayers(ref string) fixture.Image {
	img := fixture.Image{RepoTags: []string{ref}}
	for i := 1; i <= 12; i++ {
		l := fixture.Layer{CreatedBy: fmt.Sprintf("/bin/sh -c #(nop) ADD layer-%d /", i), Files: []fixture.File{{Path: fmt.Sprintf("layers/l%02d", i), Body: []byte("x")}}}
		if i == 7 {
			l.CreatedBy = "RUN /bin/sh -c pip install --no-cache-dir flask # buildkit"
			l.Files = append(l.Files, fixture.File{Path: "app/bin/tool", Body: make([]byte, 4321), Mode: 0o755})
		}
		img.Layers = append(img.Layers, l)
	}
	return img
}

func seedTwelve(s *fakeengine.Server) {
	s.SeedDemo()
	s.AddImage(9_000_000, 24*time.Hour, "acme/tool:12")
	s.SetImageArchive("acme/tool:12", fixture.OCI(twelveLayers("acme/tool:12")).Archive)
	s.AddContainer(fakeengine.ContainerSpec{Name: "tool-run", Image: "acme/tool:12", Cmd: []string{"tool"}})
	s.SetChanges("tool-run", fakeengine.Changed("/app"), fakeengine.Changed("/app/bin"), fakeengine.Changed("/app/bin/tool"))
}

func TestFilesDetailNamesTheLayerAndItsInstruction(t *testing.T) {
	b := newBench(t, benchOpts{seed: seedTwelve})
	b.openFilesOn(KindImages, "acme/tool:12")
	b.readArchive()
	b.filesGo("/app/bin/tool")
	b.see("path       /app/bin/tool", "regular file · mode 0755 (-rwxr-xr-x) · uid 0 gid 0", "4.3kB, as the archive records it",
		"layer      7 of 12 · RUN pip install --no-cache-dir flask")
	b.filesGo("/app")
	b.see("computed from the 1 file it holds", "none shown: several layers can write one directory")
	b.keys("esc")

	b.openFilesOn(KindContainers, "tool-run")
	b.readArchive()
	b.filesGo("/app/bin/tool")
	b.see("4.3kB, the image's: the container changed this file, and its own size is not read",
		"layer      7 of 12", "change     changed by the container since its image")
}

// ---- 4.6 a container's states --------------------------------------------------------

func TestFilesContainerBeforeAndAfterTheImage(t *testing.T) {
	withColour(t, termenv.ANSI256) // the struck-through row is checked below
	b := newBench(t, benchOpts{})
	b.kind(KindContainers)
	b.selectRow("shop-web-1")
	b.settle()
	b.srv.ResetRequests()
	b.keys("F")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	b.settle()
	// State 1: the changes alone, no sizes, and the image not read.
	if n := b.changeRequests(); n != 1 || b.exports() != 0 {
		t.Fatalf("%d change requests, %d archive requests", n, b.exports())
	}
	b.see("15 listed by the daemon, read", "ago · r reads them again",
		"The image's files have not been read: Enter reads its archive", "without sizes")
	b.filesGo("/etc/nginx/nginx.conf")
	if row := b.lineWith("nginx.conf"); !strings.Contains(row, "—") || !strings.Contains(row, "changed") {
		t.Fatalf("an un-indexed change: %q", row)
	}
	b.see("not known: the image's index does not hold this path")

	// State 2: the image read; the changes over its tree, not asked again.
	b.readArchive()
	if n := b.changeRequests(); n != 1 {
		t.Fatalf("reading the image asked for the changes again: %d", n)
	}
	b.see("3 added · 10 changed · 1 deleted; sizes and layers are the image's",
		"1 change beneath a directory the container added: not drawn, its contents are not read")
	b.filesGo("/etc/nginx/nginx.conf")
	if row := b.lineWith("nginx.conf"); !strings.Contains(row, "changed") || !strings.Contains(row, "88B") || !strings.Contains(row, "L2") {
		t.Fatalf("changed: %q", row)
	}
	b.see("88B, the image's")
	b.filesGo("/var/lib/nginx/state.db")
	if row := b.lineWith("state.db"); !strings.Contains(row, "added") || !strings.Contains(row, "—") {
		t.Fatalf("added file: %q", row)
	}
	b.see("not read: the container added this path after its image")
	b.filesGo("/tmp/seed")
	if row := b.lineWith("seed"); !strings.Contains(row, "deleted") || !strings.Contains(row, "29B") || !strings.Contains(row, "L1") {
		t.Fatalf("deleted: %q", row)
	}
	b.see("deleted by the container: still in the image, gone from the container")
	b.filesGo("/tmp") // the highlighted row is drawn plain; look at it unhighlighted
	if !strings.Contains(b.m.View(), "\x1b[9m") {
		t.Error("the deleted row is not struck through")
	}
	b.filesGo("/var/cache/nginx/client_temp")
	b.keys("right")
	b.see("client_temp/", "added by the container: a directory, with 1 changes listed beneath it; its contents are not read")
	b.notSee("00000001")

	// r reads the changes again, and only them.
	b.srv.ResetRequests()
	b.keys("r")
	b.until("the change list", func() bool { return !b.m.fs.chgBusy })
	if n, e := b.changeRequests(), b.exports(); n != 1 || e != 0 {
		t.Fatalf("r: %d change requests, %d archive requests", n, e)
	}
	b.noMutations("the Files view")
}

func TestFilesContainerWithNoChangesAndAFailedList(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFilesOn(KindContainers, "redis-cache")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	b.see("0 listed by the daemon", "The daemon lists no changes.")
	b.readArchive()
	b.see("This container has written nothing since its image.")
	b.keys("esc")

	b.srv.FailNext("GET", `/changes$`, 500, "storage driver busy", 1)
	b.openFilesOn(KindContainers, "shop-api-1")
	b.see("not read:", "storage driver busy", "r tries again")
	b.keys("r")
	b.see("listed by the daemon, read")
}

// ---- hostile names ----------------------------------------------------------------

func TestFilesHostileNameIsDrawnEscapedOnOneLine(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.readArchive()
	hostile := "/usr/share/nginx/html/" + fixture.DemoHostileName
	b.filesGo(hostile)
	escaped := `\x1b[31mred\nforged-row\xff.html`
	b.see(escaped, "path       /usr/share/nginx/html/"+escaped)
	for _, l := range b.screenLines() {
		if strings.HasPrefix(strings.TrimLeft(l, "┃ "), "forged-row") {
			t.Fatalf("a name forged a line: %q", l)
		}
	}
	if strings.Contains(b.m.View(), "\x1b[31mred") || strings.Contains(b.m.View(), "\xff") {
		t.Fatal("a raw escape sequence or byte of the name reached the frame")
	}
	// A traversal-shaped name is a name under the root.
	b.filesGo("/etc/nginx/traversal.conf")
	b.see("traversal.conf")
	b.assertFrameGeometry()
}

// ---- 3.3 read-only, GETs only ----------------------------------------------------------

func TestFilesReadOnlySendsOnlyGETs(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.kind(KindContainers)
	b.selectRow("shop-web-1")
	b.settle()
	b.srv.ResetRequests()
	b.keys("F")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	b.readArchive()
	b.keys("r")
	b.until("the change list", func() bool { return !b.m.fs.chgBusy })
	b.keys("w", "esc", "esc")
	b.settle()
	if len(b.srv.Requests()) < 3 {
		t.Fatalf("requests: %v", b.srv.Requests())
	}
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" {
			t.Fatalf("a %s reached the daemon: %v", r.Method, r)
		}
	}
	b.noMutations("read-only Files view")
}

// ---- 4.7 help and footer ------------------------------------------------------------

func TestFilesHelpAndFooter(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	b.openFilesOn(KindContainers, "shop-web-1")
	foot := b.screenLines()[len(b.screenLines())-1]
	for _, k := range []string{"⏎ read the archive", "↑↓ move", "→← open", "s order", "/ filter", "w export", "r changes", "F Esc close"} {
		if !strings.Contains(foot, k) {
			t.Errorf("footer lacks %q: %q", k, foot)
		}
	}
	b.keys("?")
	text := strings.Join(b.m.helpLines(120), "\n")
	text = ansi.Strip(text)
	i := strings.Index(text, "Files view (F, on an image or a container)")
	if i < 0 {
		t.Fatalf("no Files section in the help:\n%s", text)
	}
	sect := text[i:]
	if j := strings.Index(sect[1:], "\n\n"); j > 0 {
		sect = sect[:j+1]
	}
	for _, k := range []string{"F ", "↑ ↓", "PgUp PgDn", "g  G", "→  Enter", "←", "s ", "/ ", "w ", "r ", "c ", "Esc"} {
		if !strings.Contains(sect, k) {
			t.Errorf("the Files help lacks %q:\n%s", k, sect)
		}
	}
}

// ---- 5.1 export -------------------------------------------------------------------------

func TestFilesExport(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	home := b.exportHome()
	b.openFilesOn(KindImages, "nginx:1.27")
	b.keys("w")
	if b.m.dlg != nil {
		t.Fatal("an export dialog opened with nothing indexed")
	}
	b.see("Nothing to export: the image's archive has not been read, so there is no index to write.")
	b.readArchive()

	b.keys("w")
	if got, want := b.exportPath(), "~/docker-desk-files-nginx-1.27-20261109.txt"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	b.see("Export the file listing", "▸ text", "json")
	b.keys("enter")
	b.see("Write this file?", "No file's contents are in it")
	b.keys("y")
	if s := b.exported(); !strings.HasPrefix(s, "Exported text") {
		t.Fatalf("status %q", s)
	}
	txt, err := os.ReadFile(filepath.Join(home, "docker-desk-files-nginx-1.27-20261109.txt"))
	if err != nil {
		t.Fatal(err)
	}
	escaped := `/usr/share/nginx/html/\x1b[31mred\nforged-row\xff.html`
	if !strings.Contains(string(txt), escaped+"\tfile\t") {
		t.Fatalf("the hostile name is not escaped in the file:\n%s", txt)
	}
	for _, bad := range []string{"\x1b", "\xff", "forged-row\xff"} {
		if strings.Contains(string(txt), bad) {
			t.Fatalf("a raw byte %q of a name reached the file", bad)
		}
	}
	for _, l := range strings.Split(string(txt), "\n") {
		if strings.HasPrefix(l, "forged-row") {
			t.Fatal("a name forged a line of the file")
		}
	}
	if !strings.Contains(string(txt), "/usr/sbin/nginx\tfile\t1430\t0755\t2\t-\t-") || strings.Contains(string(txt), "worker_processes") {
		t.Fatalf("rows, or contents:\n%s", txt)
	}

	// JSON, of the container's view.
	b.keys("esc")
	b.openFilesOn(KindContainers, "shop-web-1")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	b.keys("w", "tab")
	b.see("▸ json")
	b.keys("enter", "y")
	b.exported()
	raw, err := os.ReadFile(filepath.Join(home, "docker-desk-files-shop-web-1-20261109.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\x1b") || strings.Contains(string(raw), `\u001b`) || strings.Contains(string(raw), `\ufffd`) {
		t.Fatal("JSON carries the raw or replaced name instead of the escaped one")
	}
	var doc struct {
		Schema    string
		Container struct{ Name string } `json:"container"`
		Entries   []struct {
			Path, Type, Change string
			Size               *int64
			Layer              int
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, e := range doc.Entries {
		found[e.Path] = e.Change
		if e.Path == "/var/lib/nginx/state.db" && e.Size != nil {
			t.Error("an added path has a size")
		}
		if e.Path == "/usr/share/nginx/html/\\x1b[31mred\\nforged-row\\xff.html" && e.Type != "file" {
			t.Error("hostile entry type")
		}
	}
	if doc.Schema != "docker-desk/files-listing/v1" || doc.Container.Name != "shop-web-1" ||
		found["/tmp/seed"] != "deleted" || found["/etc/nginx/nginx.conf"] != "changed" || found["/var/lib/nginx/state.db"] != "added" {
		t.Fatalf("document: %s %+v %v", doc.Schema, doc.Container, found)
	}
	if _, ok := found[`/usr/share/nginx/html/\x1b[31mred\nforged-row\xff.html`]; !ok {
		t.Fatal("the escaped name is not the JSON path")
	}
	b.noMutations("export")
}

var _ = tar.TypeReg
