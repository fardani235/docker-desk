package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// TestBuildCachePrune drives the real binary: S, b, Tab, type prune, Enter.
// One POST /build/prune is the only mutation, the job shows the daemon's
// report, and the account is read again.
func TestBuildCachePrune(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo(); s.SeedBuildCacheScopes() })
	d := launch(t, srv, 40, 120)
	d.see("2 Containers (13)")
	d.keys("S")
	d.see("Space — where the bytes went", "b build cache…")
	srv.ResetRequests()
	d.keys("b")
	d.see("Prune build cache", "These 4 were unused at the last account")
	d.shot("62-build-cache-preview")
	d.keys(ptyscreen.Tab)
	d.see("These 6 were unused at the last account", "docker/dockerfile:1")
	d.shot("63-build-cache-all-types")
	if muts := srv.Mutations(); len(muts) != 0 {
		t.Fatalf("the preview sent %v", muts)
	}
	if n := srv.CountRequests("GET", `^/system/df$`); n < 2 {
		t.Fatalf("Tab did not read the account again (%d reads)", n)
	}
	dfBefore := srv.CountRequests("GET", `^/system/df$`)
	d.consent("p", "r", "u", "n", "e", ptyscreen.Enter)
	d.gone("Prune build cache")
	// The job's report, in the Jobs pane once Space is closed.
	err := d.term.WaitUntil(wait, "the account read again", func(string) bool {
		return srv.CountRequests("GET", `^/system/df$`) > dfBefore
	})
	if err != nil {
		t.Fatal(err)
	}
	d.see("Build Cache         1")
	d.keys(ptyscreen.Esc)
	d.gone("Space — where the bytes went")
	d.see("prune build cache 6 records")
	muts := srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/build/prune" || muts[0].Query != "all=1" {
		t.Fatalf("mutations %v", muts)
	}
	d.keys("5", ptyscreen.Enter)
	d.see("the daemon deleted 6 build-cache records")
	d.shot("64-build-cache-pruned")
	d.keys(ptyscreen.Esc, "q")
	d.quit()
}

// Read-only: b offers and opens nothing, and nothing but reads is sent.
func TestBuildCachePruneReadOnly(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo(); s.SeedBuildCacheScopes() })
	d := launch(t, srv, 24, 80, "--read-only")
	d.see("2 Containers (13)")
	d.keys("S")
	d.see("Space — where the bytes went")
	if strings.Contains(d.term.Screen(), "build cache…") {
		t.Fatal("read-only hints offer b")
	}
	d.keys("b")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(d.term.Screen(), "Prune build cache") {
		t.Fatalf("read-only opened the build-cache dialog:\n%s", d.term.Screen())
	}
	for _, r := range srv.Requests() {
		if r.Mutating() || strings.Contains(r.Path, "prune") {
			t.Fatalf("read-only sent %s", r)
		}
	}
	d.keys(ptyscreen.Esc, "q")
	d.quit()
}
