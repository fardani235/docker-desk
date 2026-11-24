package e2e

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

const (
	e2ePushRef      = "registry.sandbox.invalid/acme/api:3.28.3"
	e2ePushPassword = "e2e-pa55-never-on-screen"
)

func pushSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.AddImage(48_000_000, 2*24*time.Hour, e2ePushRef, "registry.sandbox.invalid/acme/api:latest")
}

// launchWithLogin starts the desk with a home whose Docker configuration
// holds a credential for the sandbox registry.
func launchWithLogin(t *testing.T, srv *fakeengine.Server, rows, cols int, args ...string) *desk {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString([]byte("ci:" + e2ePushPassword))
	cfg := `{"auths":{"registry.sandbox.invalid":{"auth":"` + enc + `"}}}`
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// The harness points DOCKER_CONFIG at nothing; this test names its own.
	term, err := ptyscreen.Start(rows, cols, []string{"DOCKER_HOST=" + srv.Host(), "HOME=" + home,
		"DOCKER_CONFIG=" + filepath.Join(home, ".docker")}, binary, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return &desk{t: t, srv: srv, term: term}
}

func (d *desk) noPasswordOnScreen() {
	d.t.Helper()
	s := d.term.Screen()
	if strings.Contains(s, e2ePushPassword) || strings.Contains(s, base64.StdEncoding.EncodeToString([]byte("ci:"+e2ePushPassword))) {
		d.t.Fatalf("a credential value is on screen:\n%s", s)
	}
}

// TestPushImage pushes one tag through the real binary: p on the row, the
// typed confirmation naming the registry and the credential's source, `y`
// refused, the tag typed, and exactly one push request carrying the tag and
// X-Registry-Auth; the job shows layer progress and the daemon's digest.
func TestPushImage(t *testing.T) {
	srv := startFake(t, pushSeed)
	srv.PushStepDelay = 200 * time.Millisecond // about three seconds of stream
	d := launchWithLogin(t, srv, 42, 150)
	d.imagesRow("acme/api", e2ePushRef)
	d.keys("2", "p")
	d.see("Push image", "registry", "registry.sandbox.invalid", "login", "auths", "Type 3.28.3 and press Enter",
		"cannot be undone from this desk")
	d.noPasswordOnScreen()
	d.shot("68-push-confirm")
	if n := len(srv.PushRequests()); n != 0 {
		t.Fatalf("opening the confirmation sent %d push request(s)", n)
	}

	// y is not consent; neither is a wrong word.
	d.consent("y", ptyscreen.Enter)
	d.see(`That is not "3.28.3". Nothing was sent.`)
	d.keys("\x7f") // backspace: the y goes
	if n := len(srv.PushRequests()); n != 0 {
		t.Fatalf("y sent %d push request(s)", n)
	}
	d.keys("3", ".", "2", "8", ".", "3", ptyscreen.Enter)
	d.gone("Type 3.28.3 and press Enter")

	d.keys("5")
	// Layer progress while it runs: the row's fraction, from the daemon's
	// Pushing messages.
	d.see("1 running", "push image", "%")
	d.see("1 done", "✓ push image")
	d.keys(ptyscreen.Enter)
	d.see("✓ push image "+e2ePushRef+" done", "digest sha256:", "the local image and tag are unchanged")
	d.noPasswordOnScreen()
	d.shot("69-push-done")

	reqs := srv.PushRequests()
	if len(reqs) != 1 || reqs[0].Tag != "3.28.3" || reqs[0].Ref != e2ePushRef {
		t.Fatalf("push requests: %+v", reqs)
	}
	ac, err := authconfig.Decode(reqs[0].Auth)
	if err != nil || ac.Username != "ci" || ac.Password != e2ePushPassword || ac.ServerAddress != "registry.sandbox.invalid" {
		t.Fatalf("X-Registry-Auth: %+v %v", ac, err)
	}
	digest := srv.PushedManifest(e2ePushRef)
	if digest == "" || !strings.Contains(d.term.Screen(), digest) {
		t.Fatalf("the daemon's digest %q is not on screen:\n%s", digest, d.term.Screen())
	}
	var muts []string
	for _, r := range srv.Mutations() {
		muts = append(muts, r.String())
	}
	if len(muts) != 1 || !strings.Contains(muts[0], "/push?tag=3.28.3") {
		t.Fatalf("mutations: %v", muts)
	}
	if refs := strings.Join(srv.ImageRefs(), " "); !strings.Contains(refs, e2ePushRef) || !strings.Contains(refs, "registry.sandbox.invalid/acme/api:latest") {
		t.Fatalf("local tags changed: %s", refs)
	}
	d.keys(ptyscreen.Esc, "q")
	d.quit()
}

// At 80×24, read-only and --no-registry each refuse p with nothing sent.
func TestPushImageRefused(t *testing.T) {
	for _, tc := range []struct {
		name, flag, want, shot string
	}{
		{"read-only", "--read-only", "Read-only mode", "70-push-80x24-read-only"},
		{"no-registry", "--no-registry", "--no-registry: a push is a registry request", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, pushSeed)
			d := launchWithLogin(t, srv, 24, 80, tc.flag)
			d.see("Containers")
			d.keys("1", ptyscreen.Down, "2", "/", "a", "c", "m", "e", "/", "a", "p", "i", ptyscreen.Enter)
			d.see("acme/api")
			d.keys("p")
			d.see(tc.want)
			if tc.shot != "" {
				d.shot(tc.shot)
			}
			if n := len(srv.PushRequests()); n != 0 {
				t.Fatalf("%s pushed %d time(s)", tc.flag, n)
			}
			if m := srv.Mutations(); len(m) != 0 {
				t.Fatalf("%s sent %v", tc.flag, m)
			}
			d.keys("q")
			d.quit()
		})
	}
}
