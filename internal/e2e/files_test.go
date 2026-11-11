package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// TestFilesView is one sitting with the Files view: python:3.12-slim ›
// Files opens on facts and reads nothing; Enter starts the archive read and
// c cancels it, keeping nothing; Enter again reads it; a filter finds a deep
// path with the directories above it; w exports the listing; then
// shop-web-1's Files reads its change list alone and lays it over its
// image's tree once that is read. At 120×30, and at 80×24 in read-only
// mode. Only GETs are sent, and the archive of each image once.
func TestFilesView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"120x30", 30, 120, nil},
		{"80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			narrow := tc.cols < 100
			shot := func(d *desk, base string) {
				t.Helper()
				name := base
				if narrow {
					name += "-80x24"
				}
				packagesFrame(t, d.shot(name), tc.cols)
			}
			srv := startFake(t, func(s *fakeengine.Server) {
				s.SeedDemo()
				// The archive is held back for a moment, so that the read
				// can be seen and cancelled.
				s.ExportChunkDelay = 1500 * time.Millisecond
			})
			home := t.TempDir()
			term, err := ptyscreen.Start(tc.rows, tc.cols, []string{"DOCKER_HOST=" + srv.Host(), "HOME=" + home}, binary, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(term.Close)
			d := &desk{t: t, srv: srv, term: term}
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, "2", "/", "p", "y", "t", "h", "o", "n", ptyscreen.Enter)
			d.see("3 Details: python:3.12-slim")
			d.keys("F")
			d.see("Files", "python:3.12-slim ·", "Nothing has been read")
			shot(d, "47-files-not-read")
			time.Sleep(200 * time.Millisecond)
			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 0 {
				t.Fatalf("opening the view read the archive (%d)", n)
			}

			// Enter, then c: the stream is closed and nothing is kept.
			d.keys(ptyscreen.Enter)
			d.see("reading", "the image's archive:")
			d.keys("c")
			d.see("Cancelled: the stream was closed and nothing was kept.")
			// Read the request log (under the fake's lock) before changing
			// the delay the export handler read under it.
			srv.CountRequests("GET", `/images(/.+)?/get$`)
			srv.ExportChunkDelay = 0
			d.keys(ptyscreen.Enter)
			d.see("entries indexed, every one the archive holds", "NAME", "LAYER")

			// The filter: a deep path, with its directories.
			d.keys("/", "r", "e", "q", "u", "e", "s", "t", "s", ptyscreen.Enter)
			d.see("filter: requests", "2 matches of", "- usr/", "- local/")
			d.keys("G")
			d.see("▸", "__init__.py", "path       /usr/local/lib/python3.12/site-packages/")
			shot(d, "48-files-filter")
			d.keys(ptyscreen.Esc)
			d.gone("filter: requests")

			// The export: text, through the evidence dialog.
			d.keys("w")
			d.see("Export the file listing", "▸ text", "docker-desk-files-python-3.12-slim-")
			d.keys(ptyscreen.Enter)
			d.see("Write this file?")
			d.consent("y")
			d.see("Exported text to")
			files, _ := filepath.Glob(filepath.Join(home, "docker-desk-files-python-3.12-slim-*.txt"))
			if len(files) != 1 {
				t.Fatalf("exported files: %v", files)
			}
			txt, _ := os.ReadFile(files[0])
			if !strings.Contains(string(txt), "/usr/local/lib/python3.12/site-packages/requests/__init__.py\tfile\t23\t0644\t3\t-\t-") {
				t.Fatalf("the listing:\n%s", txt)
			}
			if strings.Contains(string(txt), "__version__") {
				t.Fatal("a file's contents reached the export")
			}
			d.keys(ptyscreen.Esc)
			d.gone("Files —")

			// A container: its change list at once, its image on Enter.
			d.keys("1", ptyscreen.Up, "2", "/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
			d.see("3 Details: shop-web-1")
			d.keys("F")
			d.see("15 listed by the daemon, read", "The image's files have not been read")
			if !narrow {
				shot(d, "49-files-container-changes-only")
			}
			d.keys(ptyscreen.Enter)
			d.see("3 added · 10 changed · 1 deleted", "1 change beneath a directory the container added")
			d.keys("/", "n", "g", "i", "n", "x", ".", "c", "o", "n", "f", ptyscreen.Enter, "G")
			d.see("▸", "nginx.conf", "the image's: the container changed this file")
			shot(d, "50-files-container")
			d.keys(ptyscreen.Esc)
			d.keys(ptyscreen.Esc)

			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 3 {
				t.Errorf("%d archive requests: python cancelled and read, nginx read", n)
			}
			if n := srv.CountRequests("GET", `/containers/[^/]+/changes$`); n != 1 {
				t.Errorf("%d change requests", n)
			}
			for _, r := range srv.Requests() {
				if r.Mutating() || (r.Method != "GET" && r.Method != "HEAD") {
					t.Fatalf("the Files view sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}

// TestFilesInTheSandbox drives `make sandbox` itself: nginx:1.27's tree is
// complete — a whiteout counted, an opaque directory, links, a device, and
// a hostile name drawn escaped — and shop-web-1 shows every overlay state.
func TestFilesInTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	sock := filepath.Join(t.TempDir(), "sb.sock")
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	term, err := ptyscreen.Start(40, 140, goEnv(t), "make", "-C", root, "--no-print-directory", "sandbox", "SANDBOX_SOCKET="+sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, term: term}
	if err := term.WaitFor(60*time.Second, "Docker Desk", "on sandbox", "shop-web-1"); err != nil {
		t.Fatal(err)
	}
	d.keys("1", ptyscreen.Down, "2", "/", "n", "g", "i", "n", "x", ptyscreen.Enter)
	d.see("3 Details: nginx:1.27")
	d.keys("F")
	d.see("nginx:1.27 ·", "3 layers", "Nothing has been read")
	d.keys(ptyscreen.Enter)
	d.see("1 path removed by later layers", "the tree's files", "different measurements, not a contradiction")
	d.keys("/", "f", "o", "r", "g", "e", "d", ptyscreen.Enter)
	d.see(`\x1b[31mred\nforged-row\xff.html`, "1 match of")
	if strings.Contains(term.Raw(), "forged-row\xff") {
		t.Fatal("a raw byte of the hostile name reached the terminal")
	}
	d.keys(ptyscreen.Esc, "/", "s", "h", ptyscreen.Enter)
	d.see("sh -> /usr/bin/bash")
	d.keys(ptyscreen.Esc, "/", "d", "e", "v", ptyscreen.Enter)
	d.see("null", "chardev", "initctl", "fifo")
	d.keys(ptyscreen.Esc, ptyscreen.Esc)
	d.gone("Files —")

	d.keys("1", ptyscreen.Up, "2", "/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
	d.see("3 Details: shop-web-1")
	d.keys("F")
	d.see("15 listed by the daemon", "3 added · 10 changed · 1 deleted")
	d.keys("/", "s", "t", "a", "t", "e", ptyscreen.Enter)
	d.see("state.db", "added")
	d.keys(ptyscreen.Esc, "/", "s", "e", "e", "d", ptyscreen.Enter)
	d.see("seed", "deleted")
	d.keys(ptyscreen.Esc, "/", "c", "l", "i", "e", "n", "t", ptyscreen.Enter)
	d.see("client_temp/", "added")
	d.keys("G")
	d.see("its contents are not read")
	d.keys(ptyscreen.Esc, "/", "n", "g", "i", "n", "x", ".", "c", "o", "n", "f", ptyscreen.Enter)
	d.see("nginx.conf", "changed")
	d.keys(ptyscreen.Esc, ptyscreen.Esc, "q")
	if code := d.quit(); code != 0 {
		t.Fatalf("make sandbox exited with %d", code)
	}
}

// TestRealDaemonFilesTour opens the Files view on a real container in
// read-only mode: its change list at once, then its image's archive on
// Enter, then a filter. Opt-in, and only reads.
//
//	DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock DOCKER_DESK_REAL_FILES_CONTAINER=<name> \
//	go test ./internal/e2e/ -run TestRealDaemonFilesTour -v
func TestRealDaemonFilesTour(t *testing.T) {
	host, name := os.Getenv("DOCKER_DESK_REAL_HOST"), os.Getenv("DOCKER_DESK_REAL_FILES_CONTAINER")
	if host == "" || name == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST and DOCKER_DESK_REAL_FILES_CONTAINER to open the Files view on a real container")
	}
	term, err := ptyscreen.Start(45, 160, []string{"DOCKER_HOST=" + host, "HOME=" + t.TempDir()}, binary, "--read-only")
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desk{t: t, term: term}
	save := *updateScreens
	*updateScreens = false
	defer func() { *updateScreens = save }()
	d.see("[READ-ONLY]", "● connected", "2 Containers")
	d.keys("2", "/")
	for _, r := range name {
		d.keys(string(r))
	}
	d.keys(ptyscreen.Enter)
	d.see("3 Details: " + name)
	d.keys("F")
	d.see("listed by the daemon, read", "The image's files have not been read")
	d.shot("real-files-changes-only")
	d.keys(ptyscreen.Enter)
	if err := term.WaitFor(3*time.Minute, "entries indexed"); err != nil {
		t.Fatal(err)
	}
	d.see("sizes and layers are the image's", "the tree's files")
	d.shot("real-files-overlay")
	d.keys("/", "e", "t", "c", ptyscreen.Enter)
	d.see("filter: etc", "searched")
	d.shot("real-files-filter")
	d.keys(ptyscreen.Esc, ptyscreen.Esc, "q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}
