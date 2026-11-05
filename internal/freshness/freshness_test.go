package freshness

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"
	mobyregistry "github.com/moby/moby/api/types/registry"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/registry"
	"github.com/fardani235/docker-desk/internal/testguard"
)

func TestMain(m *testing.M) {
	testguard.Install()
	os.Exit(m.Run())
}

// daemon is an in-memory Daemon.
type daemon struct {
	facts    map[string]engine.ImageFacts
	dist     engine.Distribution
	distErr  error
	asked    int
	factsN   int
	lastAuth string
	lastRef  string
}

func (d *daemon) ImageFacts(_ context.Context, id string) (engine.ImageFacts, error) {
	d.factsN++
	f, ok := d.facts[id]
	if !ok {
		return f, &engine.Error{Kind: engine.KindNotFound, Msg: "no such image"}
	}
	return f, nil
}

func (d *daemon) DistributionInspect(_ context.Context, ref, auth string) (engine.Distribution, error) {
	d.asked++
	d.lastAuth, d.lastRef = auth, ref
	return d.dist, d.distErr
}

func ts(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func layers(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("sha256:%s%02d", prefix, i)
	}
	return out
}

type fixture struct {
	reg    *fakeregistry.Registry
	d      *daemon
	c      *Checker
	ref    string
	old    fakeregistry.Pushed
	cfgDir string
}

// setup pushes a 13-layer build to the fake registry and puts it on the
// daemon as pulled, with a containerd-store ID (the index digest).
func setup(t *testing.T, opts ...func(*Options)) *fixture {
	t.Helper()
	reg := fakeregistry.New()
	t.Cleanup(reg.Close)
	created := ts("2026-10-01T00:00:00Z")
	old := reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &created, DiffIDs: layers("a", 13)})
	ref := reg.Host() + "/acme/api:1"
	d := &daemon{facts: map[string]engine.ImageFacts{
		"img": {ID: old.Digest, RepoTags: []string{ref}, RepoDigests: []string{reg.Host() + "/acme/api@" + old.Digest},
			Created: created, OS: "linux", Architecture: "amd64", DiffIDs: layers("a", 13)},
	}}
	o := Options{Daemon: d, DaemonID: "unix:///fake.sock", Client: registry.New(registry.Options{})}
	for _, f := range opts {
		f(&o)
	}
	return &fixture{reg: reg, d: d, c: New(o), ref: ref, old: old, cfgDir: o.ConfigDir}
}

func (f *fixture) check(t *testing.T, force bool) Verdict {
	t.Helper()
	v, err := f.c.Check(context.Background(), "img", f.ref, CheckOptions{Force: force})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerdictStates(t *testing.T) {
	f := setup(t)
	if v := f.check(t, false); v.State != Current || f.reg.Count() != 1 {
		t.Fatalf("current: %+v after %d requests", v, f.reg.Count())
	}

	// Moved: the explored pair — 11 of 13 layers shared, 2 rebuilt, later.
	later := ts("2026-10-05T00:00:00Z")
	newLayers := append(layers("a", 11), "sha256:b11", "sha256:b12")
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &later, DiffIDs: newLayers})
	v := f.check(t, true)
	if v.State != Moved || v.Shared != 11 || v.RemoteLyrs != 13 || v.Local != 13 || !v.LayersKnown || v.Delta() != "+4d" {
		t.Fatalf("moved: %+v", v)
	}
	text := strings.Join(v.Sentences(), "\n")
	for _, want := range []string{"later, by 4d", "First 11 of 13 layers shared; 2 upstream layers differ", "2026-10-01", "2026-10-05"} {
		if !strings.Contains(text, want) {
			t.Errorf("moved lacks %q:\n%s", want, text)
		}
	}
	for _, banned := range []string{"newer", "stale", "Newer", "Stale"} {
		if strings.Contains(text, banned) {
			t.Errorf("verdict says %q", banned)
		}
	}

	// Earlier upstream build.
	earlier := ts("2026-09-01T00:00:00Z")
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &earlier, DiffIDs: layers("c", 3)})
	v = f.check(t, true)
	if s := strings.Join(v.Sentences(), " "); v.State != Moved || !strings.Contains(s, "earlier, by 30d") || v.Delta() != "" || !strings.Contains(s, "First 0 of 3") {
		t.Errorf("earlier: %s", s)
	}
	// Identical time.
	same := ts("2026-10-01T00:00:00Z")
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &same, DiffIDs: layers("d", 2)})
	if s := strings.Join(f.check(t, true).Sentences(), " "); !strings.Contains(s, "same time") {
		t.Errorf("identical: %s", s)
	}
	// Absent created.
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", DiffIDs: layers("e", 2)})
	v = f.check(t, true)
	if s := strings.Join(v.Sentences(), " "); !v.RemoteCreatedMissing || !strings.Contains(s, "no created time") || !strings.Contains(s, "no direction") || v.Delta() != "" {
		t.Errorf("absent created: %s", s)
	}
	// The epoch.
	epoch := time.Unix(0, 0)
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &epoch, DiffIDs: layers("f", 2)})
	v = f.check(t, true)
	if s := strings.Join(v.Sentences(), " "); !strings.Contains(s, "epoch") || !strings.Contains(s, "no direction") || strings.Contains(s, "later") || strings.Contains(s, "earlier") {
		t.Errorf("epoch: %s", s)
	}
	// Gone.
	f.reg.Delete("acme/api", "1")
	if v := f.check(t, true); v.State != Gone || f.d.asked != 0 {
		t.Errorf("gone: %+v, daemon asked %d", v, f.d.asked)
	}
	// Not applicable: digest-only, and no RepoDigests.
	if v, _ := f.c.Check(context.Background(), "img", "alpine@sha256:"+strings.Repeat("a", 64), CheckOptions{}); v.State != NotApplicable || !strings.Contains(v.Sentences()[0], "no tag to follow") {
		t.Errorf("digest only: %+v", v)
	}
	f.d.facts["built"] = engine.ImageFacts{ID: "sha256:x", RepoTags: []string{"mine:dev"}}
	if v, _ := f.c.Check(context.Background(), "built", "mine:dev", CheckOptions{}); v.State != NotApplicable || f.d.asked != 0 {
		t.Errorf("locally built: %+v", v)
	}
}

func TestRepoDigestsNotID(t *testing.T) {
	for _, store := range []string{"containerd", "classic"} {
		f := setup(t)
		facts := f.d.facts["img"]
		if store == "classic" {
			// A classic store's ID is the config digest; it never equals the
			// index digest the tag names.
			facts.ID = f.old.Configs[0]
		}
		f.d.facts["img"] = facts
		if v := f.check(t, false); v.State != Current {
			t.Errorf("%s store: %+v", store, v)
		}
		// And with no RepoDigests the ID is never taken for the digest.
		facts.RepoDigests = nil
		f.d.facts["img2"] = facts
		if v, _ := f.c.Check(context.Background(), "img2", f.ref, CheckOptions{}); v.State != NotApplicable {
			t.Errorf("%s store without RepoDigests: %+v", store, v)
		}
	}
}

func TestPlatformMissing(t *testing.T) {
	f := setup(t)
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "arm64", DiffIDs: layers("a", 13)})
	v := f.check(t, true)
	if v.State != Unknown || !v.PlatformMissing || v.LayersKnown || !strings.Contains(v.Sentences()[0], "no longer publishes linux/amd64") {
		t.Errorf("missing platform: %+v %v", v, v.Sentences())
	}
}

func TestCaches(t *testing.T) {
	f := setup(t)
	now := ts("2026-11-05T10:00:00Z")
	f.c.o.Now = func() time.Time { return now }
	later := ts("2026-10-05T00:00:00Z")
	f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &later, DiffIDs: layers("a", 12)})
	first := f.check(t, false)
	n, factsN := f.reg.Count(), f.d.factsN
	if n != 3 || first.At.IsZero() {
		t.Fatalf("first check: %d requests, at %v", n, first.At)
	}
	// A second open sends nothing and keeps the age.
	second := f.check(t, false)
	if f.reg.Count() != n || f.d.factsN != factsN || !second.At.Equal(first.At) {
		t.Errorf("second open sent %d requests, %d facts reads; at %v vs %v", f.reg.Count()-n, f.d.factsN-factsN, second.At, first.At)
	}
	if v, ok := f.c.Cached("img", f.ref); !ok || v.State != Moved {
		t.Errorf("cached: %+v %v", v, ok)
	}
	// Asking again replaces; the config is still cached by digest.
	time.Sleep(5 * time.Millisecond)
	third := f.check(t, true)
	if f.reg.Count() != n+2 || !third.At.After(first.At) {
		t.Errorf("force: %d new requests, at %v", f.reg.Count()-n, third.At)
	}
}

func TestFallbackDecision(t *testing.T) {
	ctx := context.Background()
	// Transport failure: the registry is gone from the desk's network.
	f := setup(t)
	f.reg.Server.Close()
	f.d.dist = engine.Distribution{Digest: "sha256:" + strings.Repeat("9", 64), Platforms: []engine.Platform{{OS: "linux", Architecture: "amd64"}}}
	v, _ := f.c.Check(ctx, "img", f.ref, CheckOptions{})
	if v.State != Moved || v.Source != FromDaemon || f.d.asked != 1 || v.LayersKnown || !v.RemoteCreated.IsZero() {
		t.Fatalf("transport fallback: %+v", v)
	}
	if s := strings.Join(v.Sentences(), " "); !strings.Contains(s, "via the daemon") || !strings.Contains(s, "unknown") {
		t.Errorf("fallback sentences: %s", s)
	}
	// Kept per daemon: no second ask.
	f.c.Check(ctx, "img", f.ref, CheckOptions{})
	if f.d.asked != 1 {
		t.Errorf("fallback answer not kept: asked %d", f.d.asked)
	}
	// Both fail: one line, both reasons.
	f.d.distErr = &engine.Error{Kind: engine.KindUnavailable, Op: "ask", Msg: "dial tcp: i/o timeout"}
	v, _ = f.c.Check(ctx, "img", f.ref, CheckOptions{Force: true})
	if lines := v.Sentences(); v.State != Unknown || len(lines) != 1 || !strings.Contains(lines[0], "the desk:") || !strings.Contains(lines[0], "the daemon:") || !strings.Contains(lines[0], "i/o timeout") {
		t.Errorf("both fail: %q", v.Sentences())
	}
	// A daemon that does not serve the endpoint: cannot ask, not gone.
	f.d.distErr = &engine.Error{Kind: engine.KindUnsupported, Msg: "page not found"}
	v, _ = f.c.Check(ctx, "img", f.ref, CheckOptions{Force: true})
	if v.State != Unknown || !strings.Contains(v.Sentences()[0], "cannot ask") {
		t.Errorf("unsupported: %q", v.Sentences())
	}
	f.d.distErr = &engine.Error{Kind: engine.KindNotFound, Msg: "manifest unknown"}
	if v, _ = f.c.Check(ctx, "img", f.ref, CheckOptions{Force: true}); v.State != Gone || v.Source != FromDaemon {
		t.Errorf("gone via daemon: %+v", v)
	}

	// Quota falls back; a batch's quota does not.
	f = setup(t)
	f.reg.SetQuota(1)
	f.d.dist = engine.Distribution{Digest: f.old.Digest}
	if v, _ = f.c.Check(ctx, "img", f.ref, CheckOptions{}); v.State != Current || v.Source != FromDaemon {
		t.Errorf("quota fallback: %+v", v)
	}
	f.reg.SetQuota(1)
	asked := f.d.asked
	if v, _ = f.c.Check(ctx, "img", f.ref, CheckOptions{Force: true, NoQuotaFallback: true}); v.State != Unknown || !v.Quota || f.d.asked != asked {
		t.Errorf("batch quota: %+v", v)
	}

	// Answers are not retried: current, moved, gone, 401, 403.
	for _, tc := range []struct {
		name string
		set  func(*fixture)
		want State
	}{
		{"current", func(*fixture) {}, Current},
		{"moved", func(f *fixture) {
			f.reg.Push("acme/api", "1", true, fakeregistry.Image{OS: "linux", Arch: "amd64", DiffIDs: layers("z", 1)})
		}, Moved},
		{"gone", func(f *fixture) { f.reg.Delete("acme/api", "1") }, Gone},
		{"401", func(f *fixture) { f.reg.SetAuth(fakeregistry.Basic, "u", "p") }, Unknown},
		{"403", func(f *fixture) { f.reg.SetAuth(fakeregistry.Bearer, "u", "p") }, Unknown},
	} {
		f := setup(t)
		tc.set(f)
		v, _ := f.c.Check(ctx, "img", f.ref, CheckOptions{})
		if v.State != tc.want || f.d.asked != 0 || v.Source != FromRegistry {
			t.Errorf("%s: %+v, daemon asked %d times", tc.name, v, f.d.asked)
		}
	}
}

func TestFallbackCredentialEncoding(t *testing.T) {
	dir := t.TempDir()
	f := setup(t, func(o *Options) { o.ConfigDir = dir })
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"`+f.reg.Host()+`":{"auth":"`+b64("op:s3cr3t:x")+`"}}}`), 0o600)
	f.reg.Server.Close()
	f.d.dist = engine.Distribution{Digest: f.old.Digest}
	v, _ := f.c.Check(context.Background(), "img", f.ref, CheckOptions{})
	if v.State != Current || v.Credential != "auths" {
		t.Fatalf("%+v", v)
	}
	want, _ := authconfig.Encode(mobyregistry.AuthConfig{Username: "op", Password: "s3cr3t:x", ServerAddress: f.reg.Host()})
	if f.d.lastAuth != want {
		t.Errorf("X-Registry-Auth\n got %q\nwant %q", f.d.lastAuth, want)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v %v", v, v, v.Sentences()), "s3cr3t") {
		t.Error("the credential reached the verdict")
	}
}

func TestDisabled(t *testing.T) {
	f := setup(t, func(o *Options) { o.Client = registry.New(registry.Options{Disabled: true}) })
	v := f.check(t, false)
	if v.State != Unknown || !strings.Contains(v.Sentences()[0], "switched off") || f.reg.Count() != 0 || f.d.asked != 0 {
		t.Errorf("--no-registry: %+v, %d requests, daemon %d", v, f.reg.Count(), f.d.asked)
	}
}

func TestDescribe(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"`+b64("a:b")+`"}}}`), 0o600)
	c := New(Options{ConfigDir: dir})
	if tg := c.Describe("alpine:3.20"); tg.Host != "registry-1.docker.io" || tg.Credential != "auths" || tg.Ref != "alpine:3.20" {
		t.Errorf("%+v", tg)
	}
	if tg := c.Describe("ghcr.io/x/y"); tg.Host != "ghcr.io" || tg.Credential != "" {
		t.Errorf("%+v", tg)
	}
}

func TestSanitize(t *testing.T) {
	in := "bad\x1b[2J\x1b]0;title\x07 thing\x00\x9b\nend\xff"
	if got := Sanitize(in); got != "bad thing� end�" {
		t.Errorf("%q", got)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestBatch(t *testing.T) {
	f := setup(t)
	for i := 0; i < 4; i++ {
		facts := f.d.facts["img"]
		f.d.facts[fmt.Sprint("img", i)] = facts
	}
	f.d.facts["local"] = engine.ImageFacts{ID: "sha256:l", RepoTags: []string{"mine:dev"}}
	items := []Item{{"img0", f.ref}, {"local", "mine:dev"}, {"img1", f.ref}, {"img2", f.ref}, {"img3", f.ref}}

	// The third registry request is refused for quota: two images answer
	// (one request each), the third stops the batch.
	f.reg.SetQuota(3)
	var seen []string
	res := f.c.Batch(context.Background(), items, func(_ int, it Item, v Verdict) { seen = append(seen, it.ImageID+"="+v.State.Word()) })
	if !res.Quota || res.Reset.IsZero() || res.Checked != 3 || res.Skipped != 1 || f.d.asked != 0 {
		t.Fatalf("quota batch: %+v %v, daemon %d", res, seen, f.d.asked)
	}
	if f.reg.Count() != 3 {
		t.Errorf("batch kept asking after the quota: %d requests", f.reg.Count())
	}
	if v, ok := f.c.Cached("img0", f.ref); !ok || v.State != Current {
		t.Errorf("answers obtained before the stop were lost: %+v", v)
	}

	// Cancelled midway: stops before the next request.
	f.reg.SetQuota(0)
	f.reg.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	res = f.c.Batch(ctx, items, func(done int, _ Item, _ Verdict) {
		if done == 1 {
			cancel()
		}
	})
	if !res.Cancelled || f.reg.Count() != 1 {
		t.Errorf("cancel: %+v after %d requests", res, f.reg.Count())
	}
}
