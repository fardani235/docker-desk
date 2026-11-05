package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/jobs"
)

const (
	sandboxMoved   = fakeengine.SandboxRegistry + "/acme/web:2.3"
	sandboxCurrent = fakeengine.SandboxRegistry + "/acme/cron:1"
	sandboxGone    = fakeengine.SandboxRegistry + "/acme/legacy:0.9"
)

func seedSandbox(s *fakeengine.Server) { s.SeedDemo(); s.SeedUpstream() }

// openUpstream highlights an image and opens its Upstream tab with the
// Details pane focused.
func (b *bench) openUpstream(ref string) {
	b.t.Helper()
	b.kind(KindImages)
	b.selectRow(ref)
	b.m.focus = PaneDetails
	for b.m.tabName() != tabUpstream {
		b.keys("]")
	}
	b.settle()
}

func (b *bench) verdict(ref string) freshness.Verdict {
	b.t.Helper()
	var v freshness.Verdict
	b.until("the upstream answer for "+ref, func() bool {
		t, ok := b.m.upstreamTarget()
		if !ok || t.ref != ref {
			return false
		}
		var kept bool
		v, kept = b.m.up.checker.Cached(t.imageID, t.ref)
		return kept && b.m.up.run == nil
	})
	return v
}

func (b *bench) distributionRequests() int {
	n := 0
	for _, r := range b.srv.Requests() {
		if strings.HasPrefix(r.Path, "/distribution/") {
			n++
		}
	}
	return n
}

// The sandbox: a registry that never resolves, so every answer comes
// through the daemon — with no network at all.
func TestUpstreamSandboxFallback(t *testing.T) {
	b := newBench(t, benchOpts{seed: seedSandbox, width: 200, height: 50})
	b.srv.ResetRequests()
	b.kind(KindImages)
	b.see("USED BY")
	b.notSee("UPSTREAM") // no column until a check has answered
	b.settle()
	if n := b.distributionRequests(); n != 0 {
		t.Fatalf("the list asked upstream %d times without being asked", n)
	}

	b.openUpstream(sandboxMoved)
	v := b.verdict(sandboxMoved)
	if v.State != freshness.Moved || v.Source != freshness.FromDaemon || v.LayersKnown || !v.RemoteCreated.IsZero() {
		t.Fatalf("moved: %+v", v)
	}
	b.see("asks "+fakeengine.SandboxRegistry+" anonymously", "Moved (via the daemon)", "build time and the layer", "answered by the daemon", "r asks again")
	b.notSee("newer")

	// Reopening sends nothing; r asks again.
	n := b.distributionRequests()
	b.keys("[", "]")
	b.settle()
	if b.distributionRequests() != n {
		t.Error("reopening the tab asked again")
	}
	b.keys("r")
	b.until("asked again", func() bool { return b.distributionRequests() == n+1 && b.m.up.run == nil })

	b.openUpstream(sandboxCurrent)
	if v := b.verdict(sandboxCurrent); v.State != freshness.Current || v.Source != freshness.FromDaemon {
		t.Errorf("current: %+v", v)
	}
	b.see("Current (via the daemon)")
	b.openUpstream(sandboxGone)
	if v := b.verdict(sandboxGone); v.State != freshness.Gone {
		t.Errorf("gone: %+v", v)
	}
	b.see("Gone (via the daemon)")

	// Untagged: nothing to ask.
	b.kind(KindImages)
	for _, r := range b.m.rows[KindImages] {
		if r.imageRef == "" {
			b.selectRow(r.name)
			break
		}
	}
	b.settle()
	b.see("untagged")

	// The column, one word each, blank where nothing was checked.
	b.m.focus = PaneList
	b.dirty()
	scr := b.screen()
	for _, want := range []string{"moved", "current", "gone"} {
		if !strings.Contains(scr, want) {
			t.Errorf("the list column lacks %q:\n%s", want, scr)
		}
	}
	for _, r := range b.m.rows[KindImages] {
		if r.imageRef == "redis:7" && b.m.upstreamCell(r) != "" {
			t.Error("an unchecked image has an UPSTREAM cell")
		}
	}
}

func (b *bench) dirty() { b.m.dirty = true; b.settle() }

func TestUpstreamLayouts(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 40}} {
		for _, ro := range []bool{false, true} {
			b := newBench(t, benchOpts{seed: seedSandbox, width: size[0], height: size[1], readOnly: ro})
			b.openUpstream(sandboxMoved)
			b.verdict(sandboxMoved)
			for _, th := range themes {
				b.m.setTheme(th)
				b.assertFrameGeometry()
			}
			if mut := b.srv.Mutations(); len(mut) != 0 {
				t.Errorf("%v read-only=%v: mutations %v", size, ro, mut)
			}
			b.m.focus = PaneList
			b.dirty()
			hdr := ""
			for _, l := range strings.Split(b.screen(), "\n") {
				if strings.Contains(l, "REPOSITORY") {
					hdr = l
				}
			}
			has := strings.Contains(hdr, "UPSTREAM")
			if size[0] == 80 && has {
				t.Errorf("at 80 columns UPSTREAM must drop first: %q", hdr)
			}
			if !strings.Contains(hdr, "SIZE") {
				t.Errorf("%v: a CLI column went before UPSTREAM: %q", size, hdr)
			}
			if has && (!strings.Contains(hdr, "USED BY") || !strings.Contains(hdr, "AGE")) {
				t.Errorf("%v: UPSTREAM kept while a CLI column was dropped: %q", size, hdr)
			}
			if size[0] == 200 && !has {
				t.Errorf("at 200 columns UPSTREAM is missing: %q", hdr)
			}
		}
	}
}

// pushed seeds an image on the fake daemon whose ID is the digest the fake
// registry serves for it.
func pushed(s *fakeengine.Server, reg *fakeregistry.Registry, repo, tag string) (ref string) {
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p := reg.Push(repo, tag, true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &created, DiffIDs: []string{"sha256:" + repo}})
	ref = reg.Host() + "/" + repo + ":" + tag
	s.AddImageWithID(p.Digest, 1_000_000, 9*24*time.Hour, ref)
	return ref
}

func TestUpstreamBatchQuotaAndCancel(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	var refs []string
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		for i := 0; i < 5; i++ {
			refs = append(refs, pushed(s, reg, fmt.Sprintf("team/svc%d", i), "1"))
		}
	}})
	b.kind(KindImages)
	reg.SetQuota(3)
	b.keys("U")
	b.until("the batch to stop", func() bool { return b.m.up.batch == nil && len(b.m.jobs.List()) == 1 })
	j, _ := b.m.jobs.Get(b.m.jobs.List()[0].ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "quota") || !strings.Contains(j.Err, "resets at") {
		t.Fatalf("batch: %+v", j)
	}
	if reg.Count() != 3 || b.distributionRequests() != 0 {
		t.Errorf("after the quota: %d registry requests, %d daemon asks", reg.Count(), b.distributionRequests())
	}
	kept := 0
	for _, r := range refs {
		for _, row := range b.m.rows[KindImages] {
			if row.imageRef == r && b.m.upstreamCell(row) == "current" {
				kept++
			}
		}
	}
	if kept != 2 {
		t.Errorf("%d answers kept before the quota, want 2", kept)
	}

	// Cancel midway: a slow registry, c on the tab.
	reg.SetQuota(0)
	reg.Reset()
	b.keys("U")
	b.until("the batch to start", func() bool { return b.m.up.batch != nil })
	b.m.focus = PaneDetails
	for b.m.tabName() != tabUpstream {
		b.keys("]")
	}
	b.keys("c")
	b.until("the batch to end", func() bool { return b.m.up.batch == nil })
	j, _ = b.m.jobs.Get(b.m.jobs.List()[0].ID)
	if j.State != jobs.Cancelled && j.State != jobs.Succeeded {
		t.Errorf("cancelled batch: %+v", j)
	}
	if reg.Count() > 5 {
		t.Errorf("cancel did not stop the batch: %d requests", reg.Count())
	}
}

func TestNoRegistry(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	var ref string
	b := newBench(t, benchOpts{noRegistry: true, seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedUpstream()
		ref = pushed(s, reg, "team/api", "1")
	}})
	b.openUpstream(ref)
	b.see("switched off")
	b.openUpstream(sandboxMoved)
	b.see("switched off")
	b.kind(KindImages)
	b.keys("U")
	b.settle()
	if reg.Count() != 0 || b.distributionRequests() != 0 || len(b.m.jobs.List()) != 0 {
		t.Fatalf("--no-registry: %d registry requests, %d daemon asks, %d jobs", reg.Count(), b.distributionRequests(), len(b.m.jobs.List()))
	}
}

func TestUpstreamReadOnlyCompletes(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	var ref string
	b := newBench(t, benchOpts{readOnly: true, seed: func(s *fakeengine.Server) { ref = pushed(s, reg, "team/api", "1") }})
	b.openUpstream(ref)
	if v := b.verdict(ref); v.State != freshness.Current || v.Source != freshness.FromRegistry {
		t.Fatalf("read-only: %+v", v)
	}
	if m := b.srv.Mutations(); len(m) != 0 {
		t.Fatalf("mutations: %v", m)
	}
	for _, r := range reg.Requests() {
		if r.Method != "GET" {
			t.Errorf("registry request %s %s", r.Method, r.Path)
		}
	}
}

// Every surface is searched for the fixture's secret: the tab, the list,
// job lines and errors, the inspect document, the help.
func TestNoCredentialAnywhere(t *testing.T) {
	const secret = "fixture-s3cret-0b5e"
	reg := fakeregistry.New()
	defer reg.Close()
	reg.SetAuth(fakeregistry.Bearer, "op", secret)
	var good, bad string
	dir := t.TempDir()
	b := newBench(t, benchOpts{dockerConfig: dir, width: 200, height: 50, seed: func(s *fakeengine.Server) {
		good = pushed(s, reg, "team/api", "1")
		created := time.Now().Add(-24*time.Hour + time.Minute)
		reg.Push("team/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &created, DiffIDs: []string{"sha256:team/api", "sha256:b"}})
		bad = pushed(s, reg, "team/other", "1")
	}})
	auth := base64.StdEncoding.EncodeToString([]byte("op:" + secret))
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"`+reg.Host()+`":{"auth":"`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	look := func() { seen = append(seen, b.screen()) }

	b.openUpstream(good)
	if v := b.verdict(good); v.State != freshness.Moved || v.Credential != "auths" {
		t.Fatalf("credentialed check: %+v", v)
	}
	b.see("with the credential from auths", "later, by 8d")
	look()
	b.keys("[") // Findings
	b.keys("[") // Inspect
	b.settle()
	look()
	b.keys("]", "]")

	// A refused credential: its error must not quote it.
	reg.SetAuth(fakeregistry.Bearer, "op", "rotated")
	b.openUpstream(bad)
	if v := b.verdict(bad); v.State != freshness.Unknown {
		t.Fatalf("refused: %+v", v)
	}
	look()
	b.kind(KindImages)
	b.keys("U")
	b.until("the batch", func() bool { return b.m.up.batch == nil })
	b.m.focus = PaneList
	b.dirty()
	b.see("moved +8d")
	look()
	b.keys("?")
	b.settle()
	look()
	b.keys("esc")

	for _, j := range b.m.jobs.List() {
		full, _ := b.m.jobs.Get(j.ID)
		seen = append(seen, fmt.Sprintf("%+v", full), strings.Join(full.Log, "\n"))
	}
	for _, s := range seen {
		if strings.Contains(s, secret) || strings.Contains(s, auth) || strings.Contains(s, "rotated") {
			t.Fatalf("a credential reached a surface:\n%s", s)
		}
	}
}
