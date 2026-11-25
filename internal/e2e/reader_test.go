package e2e

import (
	"bytes"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// fileReads are the container archive reads, with the path each asked for.
func fileReads(t *testing.T, srv *fakeengine.Server) []string {
	t.Helper()
	var out []string
	for _, r := range srv.Requests() {
		if r.Method == "GET" && strings.HasSuffix(r.Path, "/archive") {
			q, err := url.ParseQuery(r.Query)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, q.Get("path"))
		}
	}
	return out
}

// filesFilter replaces the Files view's filter and goes to the last row it
// finds.
func (d *desk) filesFilter(filter string) {
	d.t.Helper()
	d.keys("/", ctrlU)
	for _, r := range filter {
		d.keys(string(r))
	}
	d.keys(ptyscreen.Enter, "G")
	d.see("filter: " + filter)
}

// readerFrame checks the reader's frame: no line wider than the terminal,
// the box closed at the bottom, and its own way out in the footer.
func readerFrame(t *testing.T, screen string, cols int) {
	t.Helper()
	for i, l := range strings.Split(screen, "\n") {
		if n := utf8.RuneCountInString(l); n > cols {
			t.Fatalf("line %d is %d cells, terminal is %d:\n%s", i, n, cols, screen)
		}
	}
	if !strings.Contains(screen, "┗━") || !strings.Contains(screen, "Esc q close") {
		t.Fatalf("the reader's frame is not whole:\n%s", screen)
	}
}

// openWebFiles opens shop-web-1's Files view, whatever its change list
// holds, and filters to a path.
func (d *desk) openWebFiles(filter string) {
	d.t.Helper()
	d.see("2 Containers (13)")
	d.keys("2", "/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
	d.see("3 Details: shop-web-1")
	d.keys("F")
	d.see("listed by the daemon")
	d.filesFilter(filter)
}

// TestFileReader is one sitting with the reader, through the real binary:
// shop-web-1 › Files, Enter on a file the container changed shows its text
// with its metadata, before the image's archive was ever read; a binary
// file shows metadata and no bytes; a slow read is cancelled and leaves the
// tree; an image's file is refused with nothing sent. Then the same file at
// 80×24 in read-only mode. Only GETs reach the daemon, one archive read a
// file, and nothing is written on this machine.
func TestFileReader(t *testing.T) {
	seed := func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/srv/big.log", Content: bytes.Repeat([]byte("a line of the big log\n"), 20_000)})
		s.SetChanges("shop-web-1", append(fakeengine.DemoChanges(), fakeengine.Changed("/srv"), fakeengine.Added("/srv/big.log"))...)
		// Only an archive of more than one 32 KiB piece waits: the big log.
		s.ArchiveChunkDelay = 1500 * time.Millisecond
	}
	conf, _ := func() (fakeengine.FileSpec, bool) {
		for _, f := range fakeengine.DemoContainerFiles() {
			if f.Path == "/etc/nginx/nginx.conf" {
				return f, true
			}
		}
		return fakeengine.FileSpec{}, false
	}()
	lines := strings.Split(strings.TrimSuffix(string(conf.Content), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("the demo's nginx.conf: %q", conf.Content)
	}

	t.Run("140x40", func(t *testing.T) {
		srv := startFake(t, seed)
		d, home := launchHome(t, srv, 40, 140)
		d.openWebFiles("nginx.conf")
		d.see("▸", "nginx.conf", "⏎ read the file", "a read the archive")
		time.Sleep(200 * time.Millisecond)
		if got := fileReads(t, srv); len(got) != 0 {
			t.Fatalf("a file was read before Enter: %q", got)
		}

		// A regular file: its text, with what it is and where it came from.
		d.keys(ptyscreen.Enter)
		d.see(append([]string{"File — read from the container, shown here only",
			"path       /etc/nginx/nginx.conf", "container  shop-web-1 ·",
			"type       regular file · mode 0644 (-rw-r--r--) · uid 0 gid 0 · modified 2026-11-01 09:30:00 UTC",
			"size       85B in the container now",
			"source     the container's current filesystem",
			"shown      the whole file: 4 lines, 85B",
			"Esc q close"}, lines...)...)
		readerFrame(t, d.shot("71-file-reader"), 140)
		if got := fileReads(t, srv); len(got) != 1 || got[0] != "/etc/nginx/nginx.conf" {
			t.Fatalf("archive reads: %q", got)
		}
		if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 0 {
			t.Fatalf("the image's archive was read (%d) for a file's contents", n)
		}
		// Keys go to the reader: the tree behind it does not take them.
		d.keys("s", "/", "x", "o", "i", "r", "j", "k")
		d.see("File — read from the container", lines[1])
		d.keys(ptyscreen.Esc)
		d.see("filter: nginx.conf", "▸", "by name")
		d.gone(lines[1])
		if n := srv.CountRequests("GET", `/containers/[^/]+/changes$`); n != 1 {
			t.Fatalf("%d change requests: a key reached the tree", n)
		}

		// A binary file: metadata, and not a byte.
		d.filesFilter("state.db")
		d.keys(ptyscreen.Enter)
		d.see("path       /var/lib/nginx/state.db", "mode 0640 (-rw-r-----)", "4.1kB in the container now",
			"shown      not text:", "there is no hex view", "Not text.")
		screen := d.shot("72-file-reader-binary")
		readerFrame(t, screen, 140)
		if strings.Contains(screen, "SQLite") {
			t.Fatal("bytes of a binary file are on screen")
		}
		d.keys("q") // closes the reader; the desk does not quit
		d.see("filter: state.db")

		// A slow read, cancelled: the reader says so; Esc is the tree.
		d.filesFilter("big.log")
		d.keys(ptyscreen.Enter)
		d.see("reading", "one request for this path", "c cancels")
		d.keys("c")
		d.see("The read was cancelled: the stream was closed and nothing was kept.")
		if s := d.term.Screen(); strings.Contains(s, "a line of the big log") {
			t.Fatal("a cancelled read shows contents")
		}
		d.keys(ptyscreen.Esc)
		d.see("filter: big.log", "▸")
		d.keys(ptyscreen.Esc, ptyscreen.Esc) // the filter, then the view
		d.gone("Files —")

		// An image's file: refused, and nothing is sent for it.
		d.keys("1", ptyscreen.Down, "2", "/", "n", "g", "i", "n", "x", ptyscreen.Enter)
		d.see("3 Details: nginx:1.27")
		d.keys("F")
		d.see("Nothing has been read")
		d.keys(ptyscreen.Enter)
		d.see("NAME", "LAYER")
		d.filesFilter("nginx.conf")
		d.see("▸", "nginx.conf")
		reads, saves := len(fileReads(t, srv)), srv.CountRequests("GET", `/images(/.+)?/get$`)
		d.keys(ptyscreen.Enter)
		d.see("Image files are not read — nothing was sent.")
		time.Sleep(200 * time.Millisecond)
		if len(fileReads(t, srv)) != reads || srv.CountRequests("GET", `/images(/.+)?/get$`) != saves {
			t.Fatalf("an image's file sent a request: %v", srv.Requests())
		}
		if s := d.term.Screen(); strings.Contains(s, "File — read from the container") {
			t.Fatal("a reader opened on an image's file")
		}
		d.keys(ptyscreen.Esc, ptyscreen.Esc)

		if got := fileReads(t, srv); len(got) != 3 {
			t.Fatalf("archive reads: %q", got)
		}
		for _, r := range srv.Requests() {
			if r.Mutating() || (r.Method != "GET" && r.Method != "HEAD") {
				t.Fatalf("the reader sent %s", r)
			}
		}
		if ents, _ := os.ReadDir(home); len(ents) != 0 {
			t.Fatalf("something was written on this machine: %v", ents)
		}
		d.keys("q")
		d.quit()
	})

	t.Run("80x24-read-only", func(t *testing.T) {
		srv := startFake(t, seed)
		d, home := launchHome(t, srv, 24, 80, "--read-only")
		d.openWebFiles("nginx.conf")
		d.keys(ptyscreen.Enter)
		d.see(append([]string{"File", "path       /etc/nginx/nginx.conf", "85B in the container now"}, lines...)...)
		readerFrame(t, d.shot("73-file-reader-80x24-read-only"), 80)
		d.keys(ptyscreen.Esc)
		d.see("filter: nginx.conf")
		d.keys(ptyscreen.Esc, ptyscreen.Esc)
		if got := fileReads(t, srv); len(got) != 1 || got[0] != "/etc/nginx/nginx.conf" {
			t.Fatalf("archive reads: %q", got)
		}
		for _, r := range srv.Requests() {
			if r.Method != "GET" && r.Method != "HEAD" {
				t.Fatalf("read-only mode sent %s", r)
			}
		}
		if ents, _ := os.ReadDir(home); len(ents) != 0 {
			t.Fatalf("something was written on this machine: %v", ents)
		}
		d.keys("q")
		d.quit()
	})
}
