package dockerctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) Env { return func(k string) string { return m[k] } }

func writeContext(t *testing.T, cfgDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(cfgDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, cfgDir, body string) {
	t.Helper()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".docker")
	writeConfig(t, cfg, `{"currentContext":"colima"}`)
	writeContext(t, cfg, "colima", "unix:///home/u/.colima/docker.sock")
	writeContext(t, cfg, "other", "unix:///run/other.sock")
	writeContext(t, cfg, "remote", "ssh://ops@prod")

	altCfg := filepath.Join(home, "alt")
	writeConfig(t, altCfg, `{"auths":{}}`)

	emptyHome := t.TempDir()

	cases := []struct {
		name        string
		env         map[string]string
		home        string
		wantHost    string
		wantContext string
		wantSource  string
		wantErr     string
	}{
		{
			name: "nothing configured", env: nil, home: emptyHome,
			wantHost: DefaultHost, wantContext: "default", wantSource: "Docker default",
		},
		{
			name: "DOCKER_HOST wins over everything",
			env:  map[string]string{"DOCKER_HOST": "unix:///tmp/x.sock", "DOCKER_CONTEXT": "other"}, home: home,
			wantHost: "unix:///tmp/x.sock", wantContext: "default", wantSource: "DOCKER_HOST",
		},
		{
			name: "DOCKER_CONTEXT wins over config",
			env:  map[string]string{"DOCKER_CONTEXT": "other"}, home: home,
			wantHost: "unix:///run/other.sock", wantContext: "other", wantSource: "DOCKER_CONTEXT",
		},
		{
			name: "currentContext from config", env: nil, home: home,
			wantHost: "unix:///home/u/.colima/docker.sock", wantContext: "colima", wantSource: "currentContext",
		},
		{
			name: "DOCKER_CONFIG relocates the config dir",
			env:  map[string]string{"DOCKER_CONFIG": altCfg}, home: home,
			wantHost: DefaultHost, wantContext: "default", wantSource: "Docker default",
		},
		{
			name: "explicit default context",
			env:  map[string]string{"DOCKER_CONTEXT": "default"}, home: home,
			wantHost: DefaultHost, wantContext: "default", wantSource: "DOCKER_CONTEXT",
		},
		{
			name: "missing context is an error naming it",
			env:  map[string]string{"DOCKER_CONTEXT": "ghost"}, home: home,
			wantErr: `docker context "ghost"`,
		},
		{
			name: "remote context resolves (refusal happens in SocketPath)",
			env:  map[string]string{"DOCKER_CONTEXT": "remote"}, home: home,
			wantHost: "ssh://ops@prod", wantContext: "remote", wantSource: "DOCKER_CONTEXT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Resolve(envOf(tc.env), tc.home)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ep.Host != tc.wantHost || ep.Context != tc.wantContext {
				t.Fatalf("got %+v, want host %q context %q", ep, tc.wantHost, tc.wantContext)
			}
			if !strings.Contains(ep.Source, tc.wantSource) {
				t.Fatalf("source %q does not mention %q", ep.Source, tc.wantSource)
			}
		})
	}
}

func TestResolveBadConfig(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".docker"), `{not json`)
	if _, err := Resolve(envOf(nil), home); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("want a parse error naming config.json, got %v", err)
	}
}

func TestSocketPath(t *testing.T) {
	if p, err := SocketPath("unix:///var/run/docker.sock"); err != nil || p != "/var/run/docker.sock" {
		t.Fatalf("got %q, %v", p, err)
	}
	for _, h := range []string{"tcp://10.0.0.1:2375", "ssh://ops@prod", "npipe:////./pipe/docker_engine", "fd://", "http://x"} {
		if _, err := SocketPath(h); !errors.Is(err, ErrRemote) {
			t.Errorf("%s: want ErrRemote, got %v", h, err)
		}
	}
	if _, err := SocketPath("unix://"); err == nil || errors.Is(err, ErrRemote) {
		t.Errorf("empty path: want a non-remote error, got %v", err)
	}
}

func TestCheckSocket(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	t.Run("missing socket", func(t *testing.T) {
		ep := Endpoint{Host: "unix://" + filepath.Join(dir, "nope.sock"), Context: "default"}
		err := CheckSocket(ctx, ep, time.Second)
		var u *Unreachable
		if !errors.As(err, &u) {
			t.Fatalf("want *Unreachable, got %v", err)
		}
		if !strings.Contains(u.Reason, "does not exist") || !strings.Contains(u.Error(), ep.Host) {
			t.Fatalf("message should say the socket does not exist and name the endpoint: %q / %q", u.Reason, u.Error())
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		path := filepath.Join(dir, "dead.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		// Keep the socket file but stop accepting: connection refused.
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
		ep := Endpoint{Host: "unix://" + path}
		err = CheckSocket(ctx, ep, time.Second)
		var u *Unreachable
		if !errors.As(err, &u) || !strings.Contains(u.Reason, "connection refused") {
			t.Fatalf("want connection refused, got %v", err)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores socket permissions")
		}
		path := filepath.Join(dir, "locked.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		ep := Endpoint{Host: "unix://" + path}
		err = CheckSocket(ctx, ep, time.Second)
		var u *Unreachable
		if !errors.As(err, &u) || !strings.Contains(u.Reason, "permission denied") {
			t.Fatalf("want permission denied, got %v", err)
		}
	})

	t.Run("listening socket is fine", func(t *testing.T) {
		path := filepath.Join(dir, "ok.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if err := CheckSocket(ctx, Endpoint{Host: "unix://" + path}, time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("remote endpoint", func(t *testing.T) {
		if err := CheckSocket(ctx, Endpoint{Host: "tcp://127.0.0.1:2375"}, time.Second); !errors.Is(err, ErrRemote) {
			t.Fatalf("want ErrRemote, got %v", err)
		}
	})

	t.Run("timeout is classified", func(t *testing.T) {
		u := Classify(Endpoint{Host: "unix:///x.sock"}, context.DeadlineExceeded)
		if !strings.Contains(u.Reason, "did not answer in time") {
			t.Fatalf("got %q", u.Reason)
		}
	})
}
