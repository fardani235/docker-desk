package files

import (
	"archive/tar"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/forensics"
)

// ---- helpers ------------------------------------------------------------------

func file(p string, size int64) forensics.FileEntry {
	return forensics.FileEntry{Path: p, Type: tar.TypeReg, Size: size, Mode: 0o644}
}

func dirE(p string) forensics.FileEntry {
	return forensics.FileEntry{Path: p, Type: tar.TypeDir, Mode: 0o755}
}

func layer(entries ...forensics.FileEntry) forensics.LayerFiles {
	return forensics.LayerFiles{Entries: entries}
}

// paths lists every path of the tree, depth first, with its layer.
func paths(ix *Index) []string {
	var out []string
	var walk func(n *Node)
	walk = func(n *Node) {
		for _, k := range n.Children() {
			s := k.Path
			if !k.IsDir() {
				s += fmt.Sprintf("@%d", k.Layer)
			}
			out = append(out, s)
			walk(k)
		}
	}
	walk(ix.Root)
	return out
}

func rowPaths(v View) []string {
	var out []string
	for _, r := range v.Rows {
		out = append(out, strings.Repeat("  ", r.Depth)+r.Node.Path)
	}
	return out
}

// ---- 2.1 the merge ----------------------------------------------------------

func TestMerge(t *testing.T) {
	cases := []struct {
		name    string
		layers  []forensics.LayerFiles
		want    []string
		removed int
		hidden  int
	}{{
		name: "a later layer replaces a path and is its writer",
		layers: []forensics.LayerFiles{
			layer(dirE("/usr"), dirE("/usr/bin"), file("/usr/bin/app", 10)),
			layer(),
			layer(),
			layer(),
			layer(file("/usr/bin/app", 99)),
		},
		want: []string{"/usr", "/usr/bin", "/usr/bin/app@5"},
	}, {
		name: "a whiteout removes the path, and the count is the whiteouts that hit",
		layers: []forensics.LayerFiles{
			layer(dirE("/tmp"), file("/tmp/seed", 4), file("/tmp/keep", 1)),
			{Whiteouts: []string{"/tmp/seed", "/tmp/never-there"}},
		},
		want:    []string{"/tmp", "/tmp/keep@1"},
		removed: 1,
	}, {
		name: "a whiteout of a directory takes what is beneath it",
		layers: []forensics.LayerFiles{
			layer(dirE("/root"), dirE("/root/.aws"), file("/root/.aws/credentials", 100), file("/root/.aws/config", 20)),
			{Whiteouts: []string{"/root/.aws"}},
		},
		want:    []string{"/root"},
		removed: 1, hidden: 2,
	}, {
		name: "an opaque directory keeps only its own layer's and later entries",
		layers: []forensics.LayerFiles{
			layer(dirE("/var"), dirE("/var/cache"), file("/var/cache/old", 5), dirE("/var/cache/apt"), file("/var/cache/apt/a.deb", 7)),
			{Opaques: []string{"/var/cache"}, Entries: []forensics.FileEntry{dirE("/var/cache"), file("/var/cache/new", 3)}},
			layer(file("/var/cache/later", 1)),
		},
		want:   []string{"/var", "/var/cache", "/var/cache/later@3", "/var/cache/new@2"},
		hidden: 3,
	}, {
		name: "a directory written by several layers keeps everything they put in it",
		layers: []forensics.LayerFiles{
			layer(dirE("/etc"), file("/etc/a", 1)),
			layer(dirE("/etc"), file("/etc/b", 1)),
			layer(forensics.FileEntry{Path: "/etc", Type: tar.TypeDir, Mode: 0o700}, file("/etc/c", 1)),
		},
		want: []string{"/etc", "/etc/a@1", "/etc/b@2", "/etc/c@3"},
	}, {
		name: "a hard link is one entry naming another, with no bytes of its own",
		layers: []forensics.LayerFiles{
			layer(file("/bin/gzip", 100), forensics.FileEntry{Path: "/bin/gunzip", Type: tar.TypeLink, Link: "/bin/gzip"}),
		},
		want: []string{"/bin", "/bin/gunzip@1", "/bin/gzip@1"},
	}, {
		name: "a file replaced by a directory, and a directory by a file",
		layers: []forensics.LayerFiles{
			layer(file("/opt/x", 1), dirE("/opt/y"), file("/opt/y/inner", 2)),
			layer(dirE("/opt/x"), file("/opt/x/now", 3), file("/opt/y", 4)),
		},
		want: []string{"/opt", "/opt/x", "/opt/x/now@2", "/opt/y@2"},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ix := Merge(c.layers)
			if got := paths(ix); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("tree:\n got %q\nwant %q", got, c.want)
			}
			if ix.Removed != c.removed || ix.Hidden != c.hidden {
				t.Fatalf("removed %d hidden %d, want %d and %d", ix.Removed, ix.Hidden, c.removed, c.hidden)
			}
		})
	}

	ix := Merge([]forensics.LayerFiles{
		layer(file("/usr/bin/app", 10)),
		layer(forensics.FileEntry{Path: "/usr/bin/app", Type: tar.TypeReg, Size: 99, Mode: 0o750}),
	})
	if n := ix.Lookup("/usr/bin/app"); n.Size != 99 || n.Mode != 0o750 || n.Layer != 2 {
		t.Fatalf("replaced file: %+v", *n)
	}
	if n := ix.Lookup("/usr"); !n.Implicit || n.Layer != 0 {
		t.Fatalf("a directory no layer names is implicit: %+v", *n)
	}
	hl := Merge([]forensics.LayerFiles{layer(file("/bin/gzip", 100), forensics.FileEntry{Path: "/bin/gunzip", Type: tar.TypeLink, Link: "/bin/gzip"})})
	if n := hl.Lookup("/bin/gunzip"); n.Type != TypeHardLink || n.Link != "/bin/gzip" || n.Size != 0 || hl.Lookup("/bin").Size != 100 {
		t.Fatalf("hard link: %+v, dir size %d", *n, hl.Lookup("/bin").Size)
	}
}

// TestMergeNamesAreKeptUnderTheRoot: a name shaped like a traversal is a
// name in the tree, under the directory the archive places it in.
func TestUnsureAboveAnUnreadLayer(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(file("/a", 1)), {Unread: "zstd"}, layer(file("/b", 1))})
	if u := ix.Unsure(1); len(u) != 1 || u[0].Layer != 2 || u[0].Reason != "zstd" {
		t.Fatalf("layer 1: %+v", u)
	}
	if u := ix.Unsure(3); len(u) != 0 {
		t.Fatalf("layer 3: %+v", u)
	}
}

func TestMergeNamesAreKeptUnderTheRoot(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(file("../../etc/passwd", 1), file("/app/../../x", 2))})
	if got := paths(ix); !reflect.DeepEqual(got, []string{"/etc", "/etc/passwd@1", "/x@1"}) {
		t.Fatalf("tree %q", got)
	}
}

// TestMergeSymlinkIsNotFollowed: a symlink to a directory is a leaf.
func TestMergeSymlinkIsNotFollowed(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(dirE("/usr/bin"), file("/usr/bin/sh", 5),
		forensics.FileEntry{Path: "/bin", Type: tar.TypeSymlink, Link: "usr/bin"},
		forensics.FileEntry{Path: "/dev/null", Type: tar.TypeChar})})
	n := ix.Lookup("/bin")
	if n.Type != TypeSymlink || n.Link != "usr/bin" || n.Expandable() || len(n.Children()) != 0 {
		t.Fatalf("symlink: %+v", *n)
	}
	v := Flatten(ix, map[string]bool{"/bin": true}, "", ByName)
	for _, r := range v.Rows {
		if r.Node.Path == "/bin" && r.Open {
			t.Fatal("a symlink was opened")
		}
	}
	if d := ix.Lookup("/dev/null"); d.Type != TypeChar || d.Expandable() {
		t.Fatalf("device: %+v", *d)
	}
}

// ---- 2.2 directory sizes -----------------------------------------------------

func TestDirectorySizes(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{
		layer(dirE("/srv"), file("/srv/a", 100_000_000), dirE("/srv/sub"), file("/srv/sub/b", 300_000_000), file("/srv/sub/c", 12_800_000)),
		layer(forensics.FileEntry{Path: "/srv/link", Type: tar.TypeSymlink, Link: "a"}),
	})
	srv := ix.Lookup("/srv")
	if srv.Size != 412_800_000 || !srv.Computed || srv.Files != 4 {
		t.Fatalf("/srv: size %d computed %v files %d", srv.Size, srv.Computed, srv.Files)
	}
	if Bytes(srv.Size) != "413MB" || Bytes(412_800) != "413kB" || Bytes(41_280_000) != "41.3MB" {
		t.Fatalf("format: %s", Bytes(srv.Size))
	}
	sub := ix.Lookup("/srv/sub")
	if sub.Size != 312_800_000 || sub.Files != 2 {
		t.Fatalf("/srv/sub: %d, %d", sub.Size, sub.Files)
	}
	if f := ix.Lookup("/srv/a"); f.Computed {
		t.Fatal("a file's size is the archive's, not computed")
	}
	if ix.Total != 412_800_000 || ix.Entries != 6 {
		t.Fatalf("total %d entries %d", ix.Total, ix.Entries)
	}
}

// ---- 2.3 flattening and the cursor ----------------------------------------

func sample() *Index {
	return Merge([]forensics.LayerFiles{layer(
		dirE("/etc"), file("/etc/hostname", 10),
		dirE("/usr"), dirE("/usr/lib"), file("/usr/lib/big.so", 5000), dirE("/usr/bin"), file("/usr/bin/a", 1), file("/usr/bin/z", 3000),
		file("/zz", 100_000),
	)})
}

func TestFlattenShowsOnlyWhatIsExpanded(t *testing.T) {
	ix := sample()
	v := Flatten(ix, nil, "", ByName)
	if got := rowPaths(v); !reflect.DeepEqual(got, []string{"/etc", "/usr", "/zz"}) {
		t.Fatalf("collapsed: %q", got)
	}
	exp := map[string]bool{"/usr": true, "/usr/bin": true}
	v = Flatten(ix, exp, "", ByName)
	want := []string{"/etc", "/usr", "  /usr/bin", "    /usr/bin/a", "    /usr/bin/z", "  /usr/lib", "/zz"}
	if got := rowPaths(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded:\n got %q\nwant %q", got, want)
	}
	v = Flatten(ix, exp, "", BySize)
	want = []string{"/zz", "/usr", "  /usr/lib", "  /usr/bin", "    /usr/bin/z", "    /usr/bin/a", "/etc"}
	if got := rowPaths(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("by size:\n got %q\nwant %q", got, want)
	}
}

func TestCursorFollowsItsPath(t *testing.T) {
	ix := sample()
	exp := map[string]bool{"/usr": true, "/usr/bin": true}
	byName := Flatten(ix, exp, "", ByName)
	cur := "/usr/bin/z"
	if byName.Find(cur) != 4 {
		t.Fatalf("row %d", byName.Find(cur))
	}
	// Re-ordered: another row, the same path.
	bySize := Flatten(ix, exp, "", BySize)
	if i := bySize.Find(cur); i != 4 || bySize.Rows[i].Node.Path != cur {
		t.Fatalf("after re-order the cursor is on row %d", i)
	}
	// Its parent collapsed: drawn on the parent, and still the same path,
	// so it is back on its own row when the parent opens again.
	delete(exp, "/usr/bin")
	collapsed := Flatten(ix, exp, "", ByName)
	if i := collapsed.Find(cur); collapsed.Rows[i].Node.Path != "/usr/bin" {
		t.Fatalf("collapsed: drawn on %q", collapsed.Rows[i].Node.Path)
	}
	exp["/usr/bin"] = true
	if i := Flatten(ix, exp, "", ByName).Find(cur); i != 4 {
		t.Fatalf("reopened: row %d", i)
	}

	// Movement.
	if got := byName.Step(cur, 1); got != "/usr/lib" {
		t.Fatalf("down: %s", got)
	}
	if got := byName.Step(cur, -1); got != "/usr/bin/a" {
		t.Fatalf("up: %s", got)
	}
	if got := byName.Step(cur, 100); got != "/zz" {
		t.Fatalf("page down past the end: %s", got)
	}
	if got := byName.Step(cur, -100); got != "/etc" {
		t.Fatalf("page up past the top: %s", got)
	}
	if byName.Top() != "/etc" || byName.Bottom() != "/zz" {
		t.Fatalf("top %s bottom %s", byName.Top(), byName.Bottom())
	}
	if got := collapsed.Step(cur, 1); got != "/usr/lib" {
		t.Fatalf("down from a hidden path starts at the row it is drawn on: %s", got)
	}
	if Parent("/usr/bin/z") != "/usr/bin" || Parent("/usr") != "" {
		t.Fatal("parent")
	}
}

// ---- 2.4 the filter -------------------------------------------------------

func TestFilterShowsAncestors(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(
		dirE("/usr"), dirE("/usr/local"), dirE("/usr/local/bin"), file("/usr/local/bin/python3.12", 1),
		file("/usr/local/bin/pip", 1), dirE("/etc"), file("/etc/PYTHONSTARTUP", 1),
	)})
	v := Flatten(ix, nil, "PYTHON3", ByName)
	want := []string{"/usr", "  /usr/local", "    /usr/local/bin", "      /usr/local/bin/python3.12"}
	if got := rowPaths(v); !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered:\n got %q\nwant %q", got, want)
	}
	if v.Matches != 1 || !v.Rows[3].Match || v.Rows[0].Match || !v.Rows[0].Open {
		t.Fatalf("matches %d, rows %+v", v.Matches, v.Rows)
	}
	if v.Searched != ix.Entries {
		t.Fatalf("searched %d of %d", v.Searched, ix.Entries)
	}
	v = Flatten(ix, nil, "python", ByName)
	if v.Matches != 2 || len(v.Rows) != 6 {
		t.Fatalf("two matches, case-insensitive: %d, %q", v.Matches, rowPaths(v))
	}
	v = Flatten(ix, nil, "ruby", ByName)
	if len(v.Rows) != 0 || v.Matches != 0 || v.Searched != 7 || v.MayMiss {
		t.Fatalf("no match: %+v", v)
	}
	ix.DroppedEntries = 1
	if v = Flatten(ix, nil, "ruby", ByName); !v.MayMiss {
		t.Fatal("a cut index must say the filter may miss")
	}
}

// ---- 2.5 escaping -----------------------------------------------------------

func TestEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain-name.txt", "plain-name.txt"},
		{"\x1b[31mred\x1b[0m", `\x1b[31mred\x1b[0m`},
		{"two\nlines", `two\nlines`},
		{"carriage\rreturn", `carriage\x0dreturn`},
		{"bad\xff\xfebytes", `bad\xff\xfebytes`},
		{"tab\there", `tab\x09here`},
		{"del\x7f", `del\x7f`},
		{"c1\u009bcsi", `c1\u009bcsi`},
		{`back\slash`, `back\\slash`},
		{"naïve ünïcode — ok", "naïve ünïcode — ok"},
		{"� is a real character", "� is a real character"},
		{"\x1b]0;title\x07\n\xff", `\x1b]0;title\x07\n\xff`},
	}
	for _, c := range cases {
		got := Escape(c.in)
		if got != c.want {
			t.Errorf("Escape(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "\x1b\n\r\x7f") {
			t.Errorf("Escape(%q) left a raw control character: %q", c.in, got)
		}
		for i := 0; i < len(got); i++ {
			if got[i] < 0x20 {
				t.Errorf("Escape(%q) left byte %#x", c.in, got[i])
			}
		}
	}
}

// ---- 2.6 the overlay --------------------------------------------------------

func overlayBase() *Index {
	return Merge([]forensics.LayerFiles{layer(
		dirE("/etc"), dirE("/etc/nginx"), file("/etc/nginx/nginx.conf", 1200),
		dirE("/tmp"), file("/tmp/seed", 40),
		dirE("/var"), dirE("/var/lib"),
	)})
}

func TestApplyMarksEachKind(t *testing.T) {
	base := overlayBase()
	before := paths(base)
	ov := Apply(base, []Change{
		{Path: "/etc/nginx/nginx.conf", Kind: Changed},
		{Path: "/etc/nginx", Kind: Changed},
		{Path: "/etc", Kind: Changed},
		{Path: "/tmp/seed", Kind: Deleted},
		{Path: "/var/lib/app", Kind: Added},
		{Path: "/var/lib/app/state.db", Kind: Added},
		{Path: "/var/log/new.log", Kind: Added},
	})
	if n := ov.Lookup("/etc/nginx/nginx.conf"); n.Change != Changed || n.Size != 1200 || n.Layer != 1 || n.FromChanges {
		t.Fatalf("changed: %+v", *n)
	}
	if n := ov.Lookup("/tmp/seed"); n.Change != Deleted || n.Size != 40 {
		t.Fatalf("deleted: %+v", *n)
	}
	app := ov.Lookup("/var/lib/app")
	if app.Change != Added || !app.FromChanges || app.Size != 0 || app.Type != TypeDir || app.Expandable() || app.Beneath != 1 {
		t.Fatalf("added directory: %+v", *app)
	}
	if ov.Lookup("/var/lib/app/state.db") != nil {
		t.Fatal("a change under an added path was drawn")
	}
	if n := ov.Lookup("/var/log/new.log"); n.Change != Added || n.Type != TypeUnknown {
		t.Fatalf("added file: %+v", *n)
	}
	if n := ov.Lookup("/var/log"); n == nil || !n.Implicit || n.Type != TypeDir || n.Change != Unchanged {
		t.Fatalf("an implied directory: %+v", n)
	}
	a := ov.Applied
	if a.Added != 2 || a.Changed != 3 || a.Deleted != 1 || a.Beneath != 1 || a.Outside != 0 || a.DeletedMissing != 0 || a.Total() != 7 {
		t.Fatalf("applied: %+v", *a)
	}
	if ov.Root.Beneath != 6 || ov.Lookup("/var").Beneath != 2 {
		t.Fatalf("beneath: root %d, /var %d", ov.Root.Beneath, ov.Lookup("/var").Beneath)
	}
	// The image's own tree is untouched.
	if got := paths(base); !reflect.DeepEqual(got, before) {
		t.Fatalf("the base changed: %q", got)
	}
	if base.Lookup("/tmp/seed").Change != Unchanged || base.Applied != nil || base.Lookup("/var/log") != nil {
		t.Fatal("the base was marked")
	}
	// A deleted path is drawn struck through, and so is what lies beneath.
	ov2 := Apply(base, []Change{{Path: "/etc/nginx", Kind: Deleted}})
	v := Flatten(ov2, map[string]bool{"/etc": true, "/etc/nginx": true}, "", ByName)
	for _, r := range v.Rows {
		if r.Node.Path == "/etc/nginx/nginx.conf" && !r.Gone {
			t.Fatal("beneath a deleted directory is not marked gone")
		}
	}
}

func TestApplyCleansPathsUnderTheRoot(t *testing.T) {
	ov := Apply(overlayBase(), []Change{
		{Path: "../../etc/nginx/nginx.conf", Kind: Changed},
		{Path: "/../../../outside", Kind: Added},
		{Path: "/", Kind: Changed},
		{Path: "", Kind: Changed},
	})
	if n := ov.Lookup("/etc/nginx/nginx.conf"); n.Change != Changed {
		t.Fatalf("cleaned path not marked: %+v", *n)
	}
	if n := ov.Lookup("/outside"); n == nil || n.Change != Added {
		t.Fatal("a path above the root is a path under it")
	}
	if ov.Applied.Ignored != 2 {
		t.Fatalf("the root is no change: %+v", *ov.Applied)
	}
	for p := range ov.own {
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			t.Fatalf("an overlay path escaped: %q", p)
		}
	}
}

func TestApplyOverACutIndex(t *testing.T) {
	base := overlayBase()
	base.DroppedEntries = 10
	ov := Apply(base, []Change{
		{Path: "/opt/big/missing.bin", Kind: Deleted},
		{Path: "/opt/big/changed.bin", Kind: Changed},
	})
	if ov.Applied.DeletedMissing != 1 || ov.Applied.Outside != 1 {
		t.Fatalf("applied: %+v", *ov.Applied)
	}
	if ov.Lookup("/opt/big/missing.bin") != nil {
		t.Fatal("a deleted path the index does not hold was drawn")
	}
	if n := ov.Lookup("/opt/big/changed.bin"); n == nil || !n.FromChanges || n.Change != Changed {
		t.Fatal("a changed path outside the index is drawn, marked")
	}
	if !ov.Incomplete() {
		t.Fatal("the overlay keeps the cut")
	}
}

func TestApplyWithNoImage(t *testing.T) {
	ov := Apply(nil, []Change{
		{Path: "/var", Kind: Changed}, {Path: "/var/lib", Kind: Changed}, {Path: "/var/lib/app", Kind: Added},
		{Path: "/etc/motd", Kind: Deleted},
	})
	if ov.Image {
		t.Fatal("no image tree is underneath")
	}
	v := Flatten(ov, map[string]bool{"/var": true, "/var/lib": true}, "", ByName)
	if got := rowPaths(v); !reflect.DeepEqual(got, []string{"/var", "  /var/lib", "    /var/lib/app"}) {
		t.Fatalf("rows %q", got)
	}
	if ov.Lookup("/var").Size != 0 || ov.Applied.DeletedMissing != 1 {
		t.Fatal("sizes or the deleted count")
	}
}

// ---- 2.7 totals ---------------------------------------------------------------

func atCap(n int) *forensics.Report {
	es := make([]forensics.FileEntry, 0, n)
	for i := 0; i < n; i++ {
		es = append(es, file(fmt.Sprintf("/data/d%03d/f%06d", i%500, i), 100))
	}
	return &forensics.Report{
		Instructions: []string{"COPY data /data"},
		Files: &forensics.FileListing{
			Layers: []forensics.LayerFiles{{Index: 1, Entries: es, Instruction: "COPY data /data"}},
			Seen:   n + 1, Kept: n, DroppedEntries: 1, MaxEntries: n, MaxPath: 4096, MaxText: 48 << 20,
		},
	}
}

func lineTexts(ls []Line) string {
	var b strings.Builder
	for _, l := range ls {
		fmt.Fprintf(&b, "%s: %s\n", l.Label, l.Text)
	}
	return b.String()
}

func TestTotalsAtTheEntryBound(t *testing.T) {
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	ix := Build(atCap(200_000))
	took := time.Since(start)
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("index of 200,000 entries: built in %v, %.1f MB held", took, float64(after.HeapAlloc-before.HeapAlloc)/1e6)
	runtime.KeepAlive(ix)

	tot := ix.Totals(0)
	s := lineTexts(tot.Lines())
	for _, want := range []string{"200,000 entries indexed of 200,001 seen", "1 dropped past the bound of 200,000 entries"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if !ix.Cut() || !Flatten(ix, nil, "x", ByName).MayMiss {
		t.Error("the index is cut and the filter must say so")
	}
	if ix.Instruction(1) != "COPY data /data" || ix.Instruction(2) != "unknown" {
		t.Error("instructions")
	}
}

func TestTotalsNameTheLengthBound(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(file("/a", 1))})
	ix.Seen, ix.Kept, ix.DroppedLongPath, ix.MaxPath = 2, 1, 1, 4096
	s := lineTexts(ix.Totals(0).Lines())
	if !strings.Contains(s, "1 entries indexed of 2 seen") || !strings.Contains(s, "1 dropped: a path longer than 4,096 bytes, the length bound") {
		t.Fatalf("lines:\n%s", s)
	}
}

func TestTotalsShowBothSizesLabelled(t *testing.T) {
	ix := Merge([]forensics.LayerFiles{layer(file("/a", 980_000_000))})
	ix.Seen, ix.Kept = 1, 1
	s := lineTexts(ix.Totals(1_200_000_000).Lines())
	if !strings.Contains(s, "the tree's files 980MB · the daemon's size 1.2GB — different measurements, not a contradiction: layers overlap") {
		t.Fatalf("lines:\n%s", s)
	}
	if strings.Contains(lineTexts(ix.Totals(980_000_000).Lines()), "contradiction") {
		t.Fatal("equal totals need no sentence")
	}
	if !strings.Contains(s, "1 entry indexed, every one the archive holds") {
		t.Fatalf("complete index: %s", s)
	}
	if Count(1234567) != "1,234,567" || Count(12) != "12" || Count(-1000) != "-1,000" {
		t.Fatal("count format")
	}
}
