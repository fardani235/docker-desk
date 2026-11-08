package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// TestRepositoryBrowser drives the real binary through the repository
// browser on the sandbox's registry (plain HTTP on loopback): B on an image,
// the tag list, the HERE column, d, and a pinned pull the fake daemon
// receives as fromImage + the digest — never the tag.
func TestRepositoryBrowser(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedRepository(reg) })
	d := launch(t, srv, 42, 150)
	d.see("2 Containers")
	d.keys("1", ptyscreen.Down, "2", "/", "3", ".", "2", "8", ".", "2", ptyscreen.Enter)
	d.see("acme/api:3.28.2")
	d.keys("B")
	d.see("100 tags held", "text order", "here — local image")
	if n := countPath(reg, "/tags/list"); n != 1 {
		t.Fatalf("opening sent %d tag list requests", n)
	}
	d.keys("d")
	d.see("linux/amd64 build's config")
	d.see("2026-07-12 amd64") // the last row on screen
	d.shot("39-repository")

	d.keys("j")
	d.see("3.28.3  · described")
	d.keys(ptyscreen.Enter)
	d.see("Pull image by digest", "Type 3.28.3 and press Enter", "fetches the digest, not the tag")
	d.shot("40-repository-pinned-pull")
	d.consent("3", ".", "2", "8", ".", "3", ptyscreen.Enter)
	d.see("here — local image")

	var pulls []string
	for _, r := range srv.Mutations() {
		pulls = append(pulls, r.String())
	}
	if len(pulls) != 1 || !strings.Contains(pulls[0], "/images/create") || !strings.Contains(pulls[0], "tag=sha256%3A") || !strings.Contains(pulls[0], "acme%2Fapi") {
		t.Fatalf("the pinned pull sent %v", pulls)
	}
	for _, r := range reg.Requests() {
		if r.Method != "GET" {
			t.Fatalf("registry request %s %s", r.Method, r.Path)
		}
	}
	d.keys("B", "q")
	d.quit()
}

// At 80×24, read-only: it browses, and the pull is refused.
func TestRepositoryBrowserCompactReadOnly(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedRepository(reg) })
	d := launch(t, srv, 24, 80, "--read-only")
	d.see("Containers")
	d.keys("1", ptyscreen.Down, "2", "/", "3", ".", "2", "8", ".", "2", ptyscreen.Enter)
	d.see("acme/api:3.28.2")
	d.keys("B")
	d.see("tags held", "here")
	d.keys("j", "j")
	d.see("3.29.0-rc.1", "no amd64")
	time.Sleep(200 * time.Millisecond)
	d.keys(ptyscreen.Enter)
	d.see("Read-only mode")
	d.shot("41-repository-80x24-read-only")
	if m := srv.Mutations(); len(m) != 0 {
		t.Fatalf("read-only sent %v", m)
	}
	d.keys("B", "q")
	d.quit()
}

func countPath(reg *fakeregistry.Registry, suffix string) int {
	n := 0
	for _, r := range reg.Requests() {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}
