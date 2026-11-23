package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// ---- helpers ----------------------------------------------------------------------

// tree builds a local tree under root: a directory, two files with their
// own modes, a symlink pointing outside, an empty directory and a FIFO.
func tree(t *testing.T, root string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(root, "conf.d", "empty"), 0o755))
	write(t, filepath.Join(root, "a.conf"), "alpha\n", 0o640)
	write(t, filepath.Join(root, "conf.d", "run.sh"), "#!/bin/sh\n", 0o755)
	must(t, os.Symlink("/etc/passwd", filepath.Join(root, "passwd")))
	must(t, syscall.Mkfifo(filepath.Join(root, "conf.d", "pipe"), 0o600))
}

func write(t *testing.T, p, body string, mode fs.FileMode) {
	t.Helper()
	must(t, os.WriteFile(p, []byte(body), mode))
	must(t, os.Chmod(p, mode))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// walk lists every path under root with its type, mode, link target and
// contents: what "the same tree" means in these tests.
func walk(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, _ := d.Info()
		line := rel + " " + fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			line += " -> " + target
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			line += " " + string(b)
		}
		out = append(out, line)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

type entry struct {
	name, link, body string
	typ              byte
	mode             int64
}

func archive(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: mode, Linkname: e.link, Size: int64(len(e.body)), Format: tar.FormatPAX}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		must(t, tw.WriteHeader(h))
		if h.Size > 0 {
			_, err := tw.Write([]byte(e.body))
			must(t, err)
		}
	}
	must(t, tw.Close())
	return &buf
}

func file(name, body string) entry { return entry{name: name, body: body, typ: tar.TypeReg} }
func dir(name string) entry        { return entry{name: name, typ: tar.TypeDir, mode: 0o755} }
func symlink(name, to string) entry {
	return entry{name: name, link: to, typ: tar.TypeSymlink, mode: 0o777}
}

// ---- 1.1 Scan ---------------------------------------------------------------------

func TestScanCountsTheTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	tree(t, root)
	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := Tree{Entries: 6, Files: 2, Dirs: 3, Links: 1, Bytes: int64(len("alpha\n") + len("#!/bin/sh\n")),
		Skipped: []string{"assets/conf.d/pipe: not a regular file, directory or symlink"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan = %+v, want %+v", got, want)
	}
	// One file.
	got, err = Scan(context.Background(), filepath.Join(root, "a.conf"))
	if err != nil || got.Entries != 1 || got.Files != 1 || got.Bytes != 6 {
		t.Fatalf("one file: %+v %v", got, err)
	}
}

func TestScanRefuses(t *testing.T) {
	dir := t.TempDir()
	must(t, syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600))
	locked := filepath.Join(dir, "locked")
	must(t, os.MkdirAll(filepath.Join(locked, "inner"), 0o755))
	must(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	for _, c := range []struct{ src, want string }{
		{filepath.Join(dir, "missing"), "does not exist"},
		{"/", "root of this machine's filesystem"},
		{filepath.Join(dir, "fifo"), "not a regular file, directory or symlink"},
	} {
		if _, err := Scan(context.Background(), c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Scan(%s) = %v, want %q", c.src, err, c.want)
		}
	}
	if os.Geteuid() != 0 {
		if _, err := Scan(context.Background(), locked); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("Scan of an unreadable directory = %v", err)
		}
	}
}

// ---- 1.2 Tar ----------------------------------------------------------------------

func TestTarRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	tree(t, root)
	long := strings.Repeat("n", 120)
	must(t, os.MkdirAll(filepath.Join(root, long), 0o750))
	write(t, filepath.Join(root, long, long+".txt"), "deep\n", 0o600)

	var buf bytes.Buffer
	var calls int
	got, err := Tar(context.Background(), &buf, root, "copied", func(Tree) { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries != 8 || got.Files != 3 || got.Dirs != 4 || got.Links != 1 || len(got.Skipped) != 1 {
		t.Fatalf("Tar totals = %+v", got)
	}
	if calls == 0 {
		t.Fatal("Tar reported no progress")
	}
	// Every header is rooted at the name given, and none follows a link.
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		names = append(names, h.Name)
		if h.Name != "copied/" && !strings.HasPrefix(h.Name, "copied/") {
			t.Errorf("header %q is not rooted at copied", h.Name)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s carries an owner: %d:%d %q %q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.Name == "copied/passwd" && (h.Typeflag != tar.TypeSymlink || h.Linkname != "/etc/passwd" || h.Size != 0) {
			t.Errorf("the symlink was not sent as a link: %+v", h)
		}
	}
	if names[0] != "copied/" {
		t.Errorf("first entry %q, want the top-level directory", names[0])
	}
	// The archive extracts to the same tree.
	out := filepath.Join(t.TempDir(), "back")
	if _, err := Extract(context.Background(), &buf, out, nil); err != nil {
		t.Fatal(err)
	}
	must(t, os.Remove(filepath.Join(root, "conf.d", "pipe"))) // skipped: not expected back
	if a, b := walk(t, root), walk(t, out); !reflect.DeepEqual(a, b) {
		t.Fatalf("round trip differs:\nsource %q\ncopy   %q", a, b)
	}
}

func TestTarOneFileIsOneEntry(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.conf")
	write(t, src, "alpha\n", 0o640)
	var buf bytes.Buffer
	if _, err := Tar(context.Background(), &buf, src, "b.conf", nil); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	must(t, err)
	if h.Name != "b.conf" || h.Mode != 0o640 || h.Size != 6 {
		t.Fatalf("header %+v", h)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("more than one entry: %v", err)
	}
}

func TestTarSendsAHardLinkAsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "d")
	must(t, os.Mkdir(root, 0o755))
	write(t, filepath.Join(root, "a"), "same\n", 0o644)
	must(t, os.Link(filepath.Join(root, "a"), filepath.Join(root, "b")))
	var buf bytes.Buffer
	got, err := Tar(context.Background(), &buf, root, "d", nil)
	if err != nil || got.Files != 2 || got.Bytes != 10 {
		t.Fatalf("%+v %v", got, err)
	}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if h.Typeflag == tar.TypeLink {
			t.Fatalf("%s was sent as a hard link", h.Name)
		}
	}
}

func TestTarStopsWhenCancelled(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big")
	write(t, src, strings.Repeat("x", 1<<20), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Tar(ctx, io.Discard, src, "big", func(tr Tree) {
		if tr.Bytes > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Tar after cancel = %v", err)
	}
}

// ---- 1.3 Extract ------------------------------------------------------------------

func TestExtractAFileAndADirectoryToExactlyDest(t *testing.T) {
	parent := t.TempDir()
	// A file named state.db in the archive lands at the name given.
	dest := filepath.Join(parent, "copy.db")
	got, err := Extract(context.Background(), archive(t, entry{name: "state.db", body: "rows", typ: tar.TypeReg, mode: 0o600}), dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 1 || got.Bytes != 4 {
		t.Fatalf("totals %+v", got)
	}
	if fi, err := os.Lstat(dest); err != nil || fi.Mode() != 0o600 {
		t.Fatalf("file: %v %v", fi.Mode(), err)
	}
	// A directory: its entries beneath dest, with their modes, the
	// directory's own last.
	dest2 := filepath.Join(parent, "nginx-copy")
	_, err = Extract(context.Background(), archive(t,
		entry{name: "nginx/", typ: tar.TypeDir, mode: 0o550},
		file("nginx/nginx.conf", "user nginx;\n"),
		dir("nginx/conf.d/"),
		file("nginx/conf.d/default.conf", "server {}\n"),
		symlink("nginx/current", "nginx.conf"),
	), dest2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest2, 0o755) })
	want := []string{
		". dr-xr-x---",
		"conf.d drwxr-xr-x",
		"conf.d/default.conf -rw-r--r-- server {}\n",
		"current Lrwxrwxrwx -> nginx.conf",
		"nginx.conf -rw-r--r-- user nginx;\n",
	}
	if got := walk(t, dest2); !reflect.DeepEqual(got, want) {
		t.Fatalf("extracted\n%q\nwant\n%q", got, want)
	}
	// Nothing else appeared beside them.
	entries, _ := os.ReadDir(parent)
	if len(entries) != 2 {
		t.Fatalf("the parent holds %d entries", len(entries))
	}
}

func TestExtractRefusesAnExistingDestination(t *testing.T) {
	parent := t.TempDir()
	for _, kind := range []string{"file", "dir", "link"} {
		dest := filepath.Join(parent, kind)
		switch kind {
		case "file":
			write(t, dest, "keep", 0o644)
		case "dir":
			must(t, os.Mkdir(dest, 0o755))
		case "link":
			must(t, os.Symlink(filepath.Join(parent, "elsewhere"), dest))
		}
		before := walk(t, parent)
		for _, a := range []*bytes.Buffer{archive(t, file("x", "new")), archive(t, dir("x/"), file("x/y", "new"))} {
			_, err := Extract(context.Background(), a, dest, nil)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Errorf("%s: Extract = %v", kind, err)
			}
		}
		if after := walk(t, parent); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s: the parent changed:\n%q\n%q", kind, before, after)
		}
	}
	if _, err := os.Lstat(filepath.Join(parent, "elsewhere")); err == nil {
		t.Fatal("a write went through the symlink")
	}
	// A missing parent is not created.
	_, err := Extract(context.Background(), archive(t, file("x", "new")), filepath.Join(parent, "no", "x"), nil)
	if err == nil {
		t.Fatal("extracted under a missing parent")
	}
	if _, err := os.Lstat(filepath.Join(parent, "no")); err == nil {
		t.Fatal("the missing parent was created")
	}
}

func TestExtractCreatesLinksLastAndRefusesChildrenOfALink(t *testing.T) {
	outside := t.TempDir()
	for name, a := range map[string]*bytes.Buffer{
		"link first": archive(t, dir("d/"), symlink("d/out", outside), file("d/out/escape", "x")),
		"link after": archive(t, dir("d/"), file("d/out/escape", "x"), symlink("d/out", outside)),
		"top link":   archive(t, symlink("d", outside), file("d/escape", "x")),
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			_, err := Extract(context.Background(), a, filepath.Join(parent, "dest"), nil)
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Extract = %v", err)
			}
			if got := walk(t, outside); len(got) != 1 {
				t.Fatalf("something was written outside: %q", got)
			}
			if got := walk(t, parent); len(got) != 1 {
				t.Fatalf("what was written was not removed: %q", got)
			}
		})
	}
	// A symlink alone is recreated, pointing where it did, never followed.
	parent := t.TempDir()
	dest := filepath.Join(parent, "pw")
	got, err := Extract(context.Background(), archive(t, symlink("passwd", "/etc/passwd")), dest, nil)
	if err != nil || got.Links != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if target, err := os.Readlink(dest); err != nil || target != "/etc/passwd" {
		t.Fatalf("link %q %v", target, err)
	}
}

// ---- 1.4 hostile archives ---------------------------------------------------------

func TestExtractHostileNames(t *testing.T) {
	for _, c := range []struct {
		name    string
		entries []entry
		want    string
	}{
		{"absolute", []entry{file("/etc/passwd", "x")}, "absolute name"},
		{"absolute inside", []entry{dir("d/"), file("/d/x", "x")}, "absolute name"},
		{"dot-dot", []entry{file("../../etc/passwd", "x")}, "climbs out"},
		{"dot-dot inside", []entry{dir("d/"), file("d/../../x", "x")}, "climbs out"},
		{"dot-dot that cleans in", []entry{dir("d/"), file("d/a/../b", "x")}, "climbs out"},
		// A NUL cannot be written into a tar name at all (archive/tar
		// refuses to encode it, and USTAR ends the name there); CheckName's
		// own test covers it.
		{"two tops", []entry{file("a", "x"), file("b", "y")}, "more than one top-level"},
		{"twice", []entry{dir("d/"), file("d/a", "x"), file("d/a", "y")}, "twice"},
		{"file then dir", []entry{dir("d/"), file("d/a", "x"), dir("d/a/")}, "as a file and as a directory"},
		{"hard link outside", []entry{dir("d/"), {name: "d/h", link: "/etc/passwd", typ: tar.TypeLink}}, "hard link"},
		{"hard link climbing", []entry{dir("d/"), {name: "d/h", link: "../../etc/passwd", typ: tar.TypeLink}}, "hard link"},
		{"hard link to another top", []entry{dir("d/"), {name: "d/h", link: "e/x", typ: tar.TypeLink}}, "hard link"},
		{"hard link to a link", []entry{dir("d/"), symlink("d/s", "/etc/passwd"), {name: "d/h", link: "d/s", typ: tar.TypeLink}}, "hard link"},
		{"empty", nil, "empty"},
		{"only a FIFO", []entry{{name: "pipe", typ: tar.TypeFifo}}, "nothing was written"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir() // nothing may appear in it but dest, and not even that
			parent := filepath.Join(root, "parent")
			must(t, os.Mkdir(parent, 0o755))
			before := walk(t, root)
			_, err := Extract(context.Background(), archive(t, c.entries...), filepath.Join(parent, "dest"), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Extract = %v, want %q", err, c.want)
			}
			if after := walk(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("the temp root changed:\n%q\n%q", before, after)
			}
		})
	}
}

func TestExtractSkipsSpecialFilesAndNamesThem(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "d")
	got, err := Extract(context.Background(), archive(t,
		dir("dev/"),
		entry{name: "dev/null", typ: tar.TypeChar},
		entry{name: "dev/sda", typ: tar.TypeBlock},
		entry{name: "dev/initctl", typ: tar.TypeFifo},
		entry{name: "dev/\x1b[31mred", typ: tar.TypeFifo},
		file("dev/keep", "x"),
	), dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dev/null: not a regular file, directory or symlink",
		"dev/sda: not a regular file, directory or symlink",
		"dev/initctl: not a regular file, directory or symlink",
		`dev/\x1b[31mred: not a regular file, directory or symlink`,
	}
	if !reflect.DeepEqual(got.Skipped, want) {
		t.Fatalf("Skipped = %q", got.Skipped)
	}
	if w := walk(t, dest); len(w) != 2 {
		t.Fatalf("made %q", w)
	}
}

func TestExtractRecreatesAHardLinkInside(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "d")
	_, err := Extract(context.Background(), archive(t,
		dir("bin/"), file("bin/gzip", "gz"), entry{name: "bin/gunzip", link: "bin/gzip", typ: tar.TypeLink},
	), dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(filepath.Join(dest, "gzip"))
	b, _ := os.Stat(filepath.Join(dest, "gunzip"))
	if !os.SameFile(a, b) {
		t.Fatal("the hard link was not recreated as a link")
	}
}

func TestCheckName(t *testing.T) {
	for in, want := range map[string]string{"a": "a", "a/": "a", "./a/b": "a/b", "a//b/": "a/b"} {
		if got, err := CheckName(in); err != nil || got != want {
			t.Errorf("CheckName(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", ".", "/", "/a", "..", "a/..", "a/../b", "a\x00"} {
		if _, err := CheckName(in); err == nil {
			t.Errorf("CheckName(%q) accepted", in)
		}
	}
}

// ---- 1.5 clean-up -----------------------------------------------------------------

// failAfter is a reader that hands out a valid archive's first n bytes and
// then blocks on cancel or fails.
type cancelAt struct {
	r      io.Reader
	n      int
	cancel func()
}

func (c *cancelAt) Read(b []byte) (int, error) {
	if c.n <= 0 {
		c.cancel()
	}
	if len(b) > 512 {
		b = b[:512]
	}
	n, err := c.r.Read(b)
	c.n -= n
	return n, err
}

func bigArchive(t *testing.T) *bytes.Buffer {
	return archive(t,
		dir("d/"), dir("d/sub/"), file("d/sub/a", strings.Repeat("a", 4096)),
		entry{name: "d/sub/ro/", typ: tar.TypeDir, mode: 0o500},
		file("d/sub/ro/x", "x"),
		file("d/b", strings.Repeat("b", 64<<10)),
		symlink("d/link", "sub/a"),
	)
}

func TestExtractCleansUpWhenCancelled(t *testing.T) {
	parent := t.TempDir()
	before := walk(t, parent)
	ctx, cancel := context.WithCancel(context.Background())
	got, err := Extract(ctx, &cancelAt{r: bigArchive(t), n: 8 << 10, cancel: cancel}, filepath.Join(parent, "dest"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract = %v", err)
	}
	if got.Removed == 0 {
		t.Fatal("Removed is 0: nothing had been written before the cancel")
	}
	if after := walk(t, parent); !reflect.DeepEqual(before, after) {
		t.Fatalf("left behind:\n%q", after)
	}
}

type failWriter struct {
	f     *os.File
	after int
}

func (w *failWriter) Write(b []byte) (int, error) {
	if w.after -= len(b); w.after < 0 {
		return 0, syscall.ENOSPC
	}
	return w.f.Write(b)
}
func (w *failWriter) Close() error { return w.f.Close() }

func TestExtractCleansUpAfterAWriteError(t *testing.T) {
	old := newFile
	t.Cleanup(func() { newFile = old })
	n := 0
	newFile = func(p string) (io.WriteCloser, error) {
		f, err := old(p)
		if err != nil {
			return nil, err
		}
		n++
		if n == 3 { // d/b: after two files and a read-only directory
			return &failWriter{f: f.(*os.File), after: 1000}, nil
		}
		return f, nil
	}
	parent := t.TempDir()
	before := walk(t, parent)
	got, err := Extract(context.Background(), bigArchive(t), filepath.Join(parent, "dest"), nil)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Extract = %v", err)
	}
	if got.Removed < 5 {
		t.Fatalf("Removed = %d", got.Removed)
	}
	if after := walk(t, parent); !reflect.DeepEqual(before, after) {
		t.Fatalf("left behind:\n%q", after)
	}
}
