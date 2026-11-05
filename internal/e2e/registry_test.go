package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
	"github.com/fardani235/docker-desk/internal/registry"
)

// dockerLogin runs the real CLI's `docker login` against the fake daemon,
// into cfgDir. It never reaches a real daemon or registry: DOCKER_HOST is
// the fake's socket, and the CLI sends the login to the daemon.
func dockerLogin(t *testing.T, host, cfgDir, path string, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"login", "-u", "op", "--password-stdin"}, args...)...)
	cmd.Env = []string{"DOCKER_HOST=" + host, "DOCKER_CONFIG=" + cfgDir, "PATH=" + path, "HOME=" + cfgDir}
	cmd.Stdin = strings.NewReader("pw:with:colon\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker login %v: %v\n%s", args, err, out)
	}
}

// TestCredentialsAgreeWithTheCLI logs in with the real docker CLI, then
// resolves the same credential from the desk: the server key, the base64
// shape and the helper path must be the CLI's own.
func TestCredentialsAgreeWithTheCLI(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the docker CLI is not installed; this test checks the desk against it")
	}
	srv := startFake(t, nil)
	reg := fakeregistry.New()
	defer reg.Close()
	p := reg.Push("team/api", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.SetAuth(fakeregistry.Bearer, "op", "pw:with:colon")
	ctx := context.Background()

	// auths: a registry with a port, and Docker Hub's legacy key.
	cfg := t.TempDir()
	dockerLogin(t, srv.Host(), cfg, os.Getenv("PATH"), reg.Host())
	dockerLogin(t, srv.Host(), cfg, os.Getenv("PATH"))
	var file struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	b, _ := os.ReadFile(filepath.Join(cfg, "config.json"))
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("op:pw:with:colon"))
	for _, key := range []string{reg.Host(), registry.DockerHubKey} {
		if file.Auths[key].Auth != want {
			t.Fatalf("the CLI filed %q as %+v; keys %v", key, file.Auths[key], keys(file.Auths))
		}
	}
	for _, ref := range []string{reg.Host() + "/team/api:1", "alpine:3.20"} {
		r, _ := registry.ParseRef(ref)
		c, err := registry.ResolveCredential(ctx, cfg, r.Key)
		if err != nil || c.Username != "op" || c.Password.Reveal() != "pw:with:colon" {
			t.Errorf("%s: the desk resolved %+v, %v", ref, c, err)
		}
	}
	r, _ := registry.ParseRef(reg.Host() + "/team/api:1")
	c, _ := registry.ResolveCredential(ctx, cfg, r.Key)
	if rem, err := registry.New(registry.Options{}).Resolve(ctx, r, c, registry.Local{Digest: p.Digest}); err != nil || !rem.Same {
		t.Errorf("the CLI's credential did not authenticate: %+v %v", rem, err)
	}

	// credsStore: the CLI stores through a helper; the desk gets through it,
	// with the same server key on standard input.
	bin := t.TempDir()
	store := filepath.Join(bin, "stored.json")
	script := `#!/bin/sh
case "$1" in
store) cat > ` + store + ` ;;
get) key=$(cat); grep -q "\"ServerURL\":\"$key\"" ` + store + ` 2>/dev/null && cat ` + store + ` || { echo "credentials not found in native keychain"; exit 1; } ;;
erase) rm -f ` + store + ` ;;
list) echo '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker-credential-e2efake"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	cfg2 := t.TempDir()
	os.WriteFile(filepath.Join(cfg2, "config.json"), []byte(`{"credsStore":"e2efake"}`), 0o600)
	dockerLogin(t, srv.Host(), cfg2, path, reg.Host())
	if b, _ := os.ReadFile(filepath.Join(cfg2, "config.json")); strings.Contains(string(b), "pw:with") || strings.Contains(string(b), want) {
		t.Fatalf("the CLI wrote the password beside a credsStore: %s", b)
	}
	t.Setenv("PATH", path)
	c, err := registry.ResolveCredential(ctx, cfg2, r.Key)
	if err != nil || c.Password.Reveal() != "pw:with:colon" || c.Source != "credsStore e2efake" {
		t.Fatalf("through the helper: %+v %v", c, err)
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestImageUpstream opens Images › Upstream in the real binary on the
// sandbox's moved tag. Its registry is under .invalid, which never resolves,
// so the desk falls back to the daemon — with no network at all. Only that
// tab is opened: U would ask the demo's real-looking registries.
func TestImageUpstream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"37-image-upstream", 42, 150, nil},
		{"38-image-upstream-80x24", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo(); s.SeedUpstream() })
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, "2", "/", "a", "c", "m", "e", "/", "w", "e", "b", ptyscreen.Enter)
			d.see("acme/web:2.3")
			d.keys("3", "]", "]", "]")
			d.see("Moved (via the daemon)")
			screen := d.shot(tc.name)
			if !strings.Contains(screen, "registry.sandbox.invalid") {
				t.Errorf("the tab does not name the host:\n%s", screen)
			}
			if n := srv.CountRequests("GET", `/distribution/.+/json$`); n != 1 {
				t.Errorf("%d distribution requests, want 1", n)
			}
			for _, r := range srv.Requests() {
				if r.Mutating() {
					t.Fatalf("the check sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}
