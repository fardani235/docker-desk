package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

const ctrlU = "\x15" // a text input's "delete to the start"

// launchHome starts the desk with HOME pointing at a fresh folder: what ~
// means in the copy forms.
func launchHome(t *testing.T, srv *fakeengine.Server, rows, cols int, args ...string) (*desk, string) {
	t.Helper()
	home := t.TempDir()
	term, err := ptyscreen.Start(rows, cols, []string{"DOCKER_HOST=" + srv.Host(), "HOME=" + home}, binary, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return &desk{t: t, srv: srv, term: term}, home
}

// webFiles opens shop-web-1's Files view and puts the cursor on the last
// row the filter finds.
func (d *desk) webFiles(filter string) {
	d.t.Helper()
	d.see("2 Containers (13)")
	d.keys("2", "/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
	d.see("3 Details: shop-web-1")
	d.keys("F")
	d.see("15 listed by the daemon")
	d.keys("/")
	for _, r := range filter {
		d.keys(string(r))
	}
	d.keys(ptyscreen.Enter, "G")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCopyOut copies a file the container added out of shop-web-1 through
// the real binary, in read-only mode: the local file has the container's
// bytes and mode, and only GETs reached the daemon.
func TestCopyOut(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d, home := launchHome(t, srv, 40, 140, "--read-only")
	d.webFiles("state.db")
	d.see("▸", "state.db", "added")
	d.keys("o")
	d.see("Copy out of the container", "/var/lib/nginx/state.db", "Local destination")
	d.keys("~/state.db")
	dest := filepath.Join(home, "state.db")
	d.see("will write " + dest)
	d.keys(ptyscreen.Enter)
	// The status line says "started", then, a moment later, how the job
	// ended: the file itself is the witness.
	want, _ := srv.ContainerFile("shop-web-1", "/var/lib/nginx/state.db")
	waitFor(t, "the copied file", func() bool {
		fi, err := os.Lstat(dest)
		return err == nil && fi.Size() == int64(len(want.Content))
	})
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(want.Content) {
		t.Fatalf("the local file differs from the container's: %v", err)
	}
	if fi, _ := os.Lstat(dest); fi.Mode() != 0o640 {
		t.Fatalf("mode %v, the container's is 0640", fi.Mode())
	}
	d.keys(ptyscreen.Esc, ptyscreen.Esc, "5")
	d.see("copy out of container", "shop-web-1:/var/lib/nginx/state.db")
	for _, r := range srv.Requests() {
		if r.Mutating() {
			t.Fatalf("a copy out sent %s", r)
		}
	}
	if n := srv.CountRequests("GET", `/containers/[^/]+/archive$`); n != 1 {
		t.Fatalf("%d archive reads", n)
	}
	d.keys("q")
	d.quit()
}

// TestCopyIn copies a local directory into shop-web-1: the form, the
// confirmation (captured), y, the fake's tree, and the change list read
// again. Then an existing local destination for a copy out is refused
// before any archive request.
func TestCopyIn(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d, home := launchHome(t, srv, 42, 150)
	src := filepath.Join(home, "assets")
	_ = os.MkdirAll(filepath.Join(src, "js"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>new front page</h1>\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "js", "app.js"), []byte("console.log('shop')\n"), 0o644)
	_ = os.Symlink("js/app.js", filepath.Join(src, "main.js"))

	d.webFiles("nginx.conf")
	d.see("▸", "nginx.conf")
	d.keys("i")
	d.see("Copy into the container", "Local source", "> /etc/nginx/nginx.conf")
	d.keys("~/assets", ptyscreen.Tab, ctrlU, "/srv/assets")
	d.see("will read "+src, "> /srv/assets")
	d.shot("56-copy-in-form")
	d.keys(ptyscreen.Enter)
	d.see("Copy into container", "2 files, 2", "1 symlink (5 entries), 44 B.", "Into the container at /srv/assets", "are replaced", "y proceed")
	d.shot("57-copy-in-confirm")
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("the form and the confirmation sent %v", srv.Mutations())
	}
	changes := srv.CountRequests("GET", `/containers/[^/]+/changes$`)
	d.consent("y")
	d.see("Copied into shop-web-1; its change list was read again")
	if n := srv.CountRequests("GET", `/containers/[^/]+/changes$`); n != changes+1 {
		t.Fatalf("the change list was read %d more times", n-changes)
	}
	var tree []string
	for _, f := range srv.ContainerFiles("shop-web-1", "/srv/assets") {
		line := f.Path
		if f.Link != "" {
			line += " -> " + f.Link
		}
		tree = append(tree, line)
	}
	if got := strings.Join(tree, " | "); got != "/srv/assets | /srv/assets/index.html | /srv/assets/js | /srv/assets/js/app.js | /srv/assets/main.js -> js/app.js" {
		t.Fatalf("the container's tree: %s", got)
	}
	if f, _ := srv.ContainerFile("shop-web-1", "/srv/assets/js/app.js"); string(f.Content) != "console.log('shop')\n" {
		t.Fatalf("app.js holds %q", f.Content)
	}
	log := mutationPaths(srv)
	if !matchInOrder(log, `^PUT /containers/[0-9a-f]{64}/archive$`) {
		t.Fatalf("want one upload and nothing else; got %v", log)
	}
	if ups := srv.Uploads(); len(ups) != 1 || ups[0].Path != "/srv" {
		t.Fatalf("uploads %+v", ups)
	}
	// The view shows it: an added directory, its contents counted.
	d.keys(ptyscreen.Esc, "/", "a", "s", "s", "e", "t", "s", ptyscreen.Enter, "G")
	d.see("assets/", "added")

	// Copy out to a path that exists: refused in the form, nothing read.
	d.keys(ptyscreen.Esc, "/", "n", "g", "i", "n", "x", ".", "c", "o", "n", "f", ptyscreen.Enter, "G")
	d.keys("o")
	d.see("Copy out of the container")
	d.keys("~/assets", ptyscreen.Enter)
	d.see("already exists:", "Name one that does not exist.")
	if n := srv.CountRequests("GET", `/containers/[^/]+/archive$`); n != 0 {
		t.Fatalf("a refused copy out read the archive %d times", n)
	}
	d.keys(ptyscreen.Esc, ptyscreen.Esc, "q")
	d.quit()
}
