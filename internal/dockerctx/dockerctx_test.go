package dockerctx

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) Env { return func(k string) string { return m[k] } }

func contextID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func writeContext(t *testing.T, cfgDir, name, host string) {
	t.Helper()
	writeContextMeta(t, cfgDir, name, host, false)
}

func writeContextMeta(t *testing.T, cfgDir, name, host string, skipVerify bool) {
	t.Helper()
	dir := filepath.Join(cfgDir, "contexts", "meta", contextID(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skip := "false"
	if skipVerify {
		skip = "true"
	}
	body := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":` + skip + `}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeContextTLS stores the named files as the context's TLS material, the
// way `docker context create --docker ca=…,cert=…,key=…` does.
func writeContextTLS(t *testing.T, cfgDir, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(cfgDir, "contexts", "tls", contextID(name), "docker")
	touch(t, dir, files...)
	return dir
}

func touch(t *testing.T, dir string, files ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	writeContext(t, cfg, "escaped", `tcp://prod:2375/\u001b]0;owned\u0007`)
	certs := filepath.Join(home, "certs")
	touch(t, certs, "ca.pem")

	altCfg := filepath.Join(home, "alt")
	writeConfig(t, altCfg, `{"auths":{}}`)

	emptyHome := t.TempDir()

	cases := []struct {
		name        string
		sel         Selection
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
			name: "a context that points at another machine resolves like any other",
			env:  map[string]string{"DOCKER_CONTEXT": "remote"}, home: home,
			wantHost: "ssh://ops@prod", wantContext: "remote", wantSource: "DOCKER_CONTEXT",
		},
		{
			name: "--host wins over DOCKER_HOST and the configured context",
			sel:  Selection{Host: "ssh://me@box"},
			env:  map[string]string{"DOCKER_HOST": "unix:///tmp/x.sock"}, home: home,
			wantHost: "ssh://me@box", wantContext: "default", wantSource: "--host flag",
		},
		{
			name: "--context wins over DOCKER_HOST, as in the CLI",
			sel:  Selection{Context: "other"},
			env:  map[string]string{"DOCKER_HOST": "unix:///tmp/x.sock", "DOCKER_CONTEXT": "remote"}, home: home,
			wantHost: "unix:///run/other.sock", wantContext: "other", wantSource: "--context flag",
		},
		{
			name: "--context default is the default socket even when a context is current",
			sel:  Selection{Context: "default"}, home: home,
			wantHost: DefaultHost, wantContext: "default", wantSource: "--context flag",
		},
		{
			// Found by the review, and checked against the CLI itself:
			// the default context is not "the local socket", it is
			// "whatever DOCKER_HOST says".
			name: "--context default does not get past DOCKER_HOST, as in the CLI",
			sel:  Selection{Context: "default"},
			env:  map[string]string{"DOCKER_HOST": "ssh://ops@prod"}, home: home,
			wantHost: "ssh://ops@prod", wantContext: "default", wantSource: "DOCKER_HOST environment variable (the default context, selected by --context flag)",
		},
		{
			name: "DOCKER_TLS_VERIFY without any host means tcp://localhost:2376, as in the CLI",
			env:  map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs}, home: emptyHome,
			wantHost: "tcp://localhost:2376", wantContext: "default", wantSource: "the Docker CLI's default when DOCKER_TLS_VERIFY or DOCKER_TLS is set",
		},
		{
			name: "…and so does DOCKER_TLS",
			env:  map[string]string{"DOCKER_TLS": "1"}, home: emptyHome,
			wantHost: "tcp://localhost:2376", wantContext: "default", wantSource: "the Docker CLI's default",
		},
		{
			name: "…but a current context still wins over that default",
			env:  map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs}, home: home,
			wantHost: "unix:///home/u/.colima/docker.sock", wantContext: "colima", wantSource: "currentContext",
		},
		{
			name: "no home directory and no DOCKER_CONFIG: no configuration is read, and none is guessed at",
			env:  nil, home: "",
			wantHost: DefaultHost, wantContext: "default", wantSource: "Docker default",
		},
		{
			name: "a named context cannot be looked up without a configuration directory",
			sel:  Selection{Context: "other"}, home: "",
			wantErr: "no Docker configuration directory",
		},
		{
			name: "control characters in an endpoint are refused, not printed",
			env:  map[string]string{"DOCKER_HOST": "unix:///tmp/x\x1b[2Jy.sock"}, home: home,
			wantErr: "contains control characters",
		},
		{
			name: "…also when they come from a context file",
			env:  map[string]string{"DOCKER_CONTEXT": "escaped"}, home: home,
			wantErr: "contains control characters",
		},
		{
			name: "--host and --context together are refused",
			sel:  Selection{Host: "tcp://h:1", Context: "other"}, home: home,
			wantErr: "cannot be used together",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Resolve(tc.sel, envOf(tc.env), tc.home)
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
	if _, err := Resolve(Selection{}, envOf(nil), home); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("want a parse error naming config.json, got %v", err)
	}
}

// TestResolveTLS: TLS for a tcp:// endpoint comes from where the endpoint
// came from — the environment for a host given directly, the context store
// for a named context — with the Docker CLI's rules.
func TestResolveTLS(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".docker")
	writeConfig(t, cfg, `{}`)
	certs := filepath.Join(home, "certs")
	touch(t, certs, "ca.pem", "cert.pem", "key.pem")
	caOnly := filepath.Join(home, "ca-only")
	touch(t, caOnly, "ca.pem", "cert.pem") // a certificate without its key is not used
	empty := filepath.Join(home, "empty")
	touch(t, empty)

	writeContext(t, cfg, "plain", "tcp://10.0.0.5:2375")
	writeContext(t, cfg, "mtls", "tcp://10.0.0.5:2376")
	mtlsDir := writeContextTLS(t, cfg, "mtls", "ca.pem", "cert.pem", "key.pem")
	writeContextMeta(t, cfg, "lax", "tcp://10.0.0.5:2376", true)
	writeContext(t, cfg, "ssh-with-tls-files", "ssh://ops@prod")
	writeContextTLS(t, cfg, "ssh-with-tls-files", "ca.pem")
	writeContext(t, cfg, "half", "tcp://10.0.0.5:2376")
	writeContextTLS(t, cfg, "half", "cert.pem")

	const host = "tcp://10.0.0.5:2376"
	cases := []struct {
		name    string
		sel     Selection
		env     map[string]string
		want    *TLS // nil: no TLS
		wantErr string
	}{
		{name: "tcp without any TLS setting is plain", env: map[string]string{"DOCKER_HOST": host}},
		{
			name: "DOCKER_TLS_VERIFY with DOCKER_CERT_PATH: verified, with the client certificate",
			env:  map[string]string{"DOCKER_HOST": host, "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs},
			want: &TLS{CAFile: filepath.Join(certs, "ca.pem"), CertFile: filepath.Join(certs, "cert.pem"), KeyFile: filepath.Join(certs, "key.pem")},
		},
		{
			name: "any non-empty DOCKER_TLS_VERIFY counts, as in the CLI",
			env:  map[string]string{"DOCKER_HOST": host, "DOCKER_TLS_VERIFY": "0", "DOCKER_CERT_PATH": certs},
			want: &TLS{CAFile: filepath.Join(certs, "ca.pem"), CertFile: filepath.Join(certs, "cert.pem"), KeyFile: filepath.Join(certs, "key.pem")},
		},
		{
			name: "a certificate without its key is left out",
			env:  map[string]string{"DOCKER_HOST": host, "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": caOnly},
			want: &TLS{CAFile: filepath.Join(caOnly, "ca.pem")},
		},
		{
			name:    "verifying without a CA certificate is an error, not a guess",
			env:     map[string]string{"DOCKER_HOST": host, "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty},
			wantErr: "CA certificate",
		},
		{
			name: "certificates default to the config directory",
			env:  map[string]string{"DOCKER_HOST": host, "DOCKER_TLS_VERIFY": "1", "DOCKER_CONFIG": certs},
			want: &TLS{CAFile: filepath.Join(certs, "ca.pem"), CertFile: filepath.Join(certs, "cert.pem"), KeyFile: filepath.Join(certs, "key.pem")},
		},
		{
			name: "DOCKER_TLS alone encrypts without verifying",
			env:  map[string]string{"DOCKER_HOST": host, "DOCKER_TLS": "1", "DOCKER_CERT_PATH": empty},
			want: &TLS{SkipVerify: true},
		},
		{
			name: "--host takes its TLS from the environment too",
			sel:  Selection{Host: host},
			env:  map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": caOnly},
			want: &TLS{CAFile: filepath.Join(caOnly, "ca.pem")},
		},
		{
			name: "the TLS variables mean nothing for a unix socket",
			env:  map[string]string{"DOCKER_HOST": "unix:///tmp/x.sock", "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty},
		},
		{
			name: "…or for ssh",
			env:  map[string]string{"DOCKER_HOST": "ssh://ops@prod", "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty},
		},
		{name: "a context without stored certificates is plain TCP", sel: Selection{Context: "plain"}},
		{
			name: "a context takes its TLS from its own store, not from the environment",
			sel:  Selection{Context: "mtls"},
			env:  map[string]string{"DOCKER_CERT_PATH": empty},
			want: &TLS{CAFile: filepath.Join(mtlsDir, "ca.pem"), CertFile: filepath.Join(mtlsDir, "cert.pem"), KeyFile: filepath.Join(mtlsDir, "key.pem")},
		},
		{name: "SkipTLSVerify in a context: TLS, unverified", sel: Selection{Context: "lax"}, want: &TLS{SkipVerify: true}},
		{name: "TLS files stored for an ssh context are ignored", sel: Selection{Context: "ssh-with-tls-files"}},
		// Found by the review, and checked against the CLI: any stored TLS
		// file means TLS. Falling back to an unencrypted connection because
		// the key pair is incomplete would be the worst possible reading.
		{name: "a context storing only half a key pair is still TLS, never plain TCP", sel: Selection{Context: "half"}, want: &TLS{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Resolve(tc.sel, envOf(tc.env), home)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (ep.TLS == nil) != (tc.want == nil) {
				t.Fatalf("TLS = %+v, want %+v", ep.TLS, tc.want)
			}
			if tc.want == nil {
				return
			}
			got := *ep.TLS
			if got.Source == "" {
				t.Error("TLS settings must say where they came from")
			}
			got.Source = ""
			if got != *tc.want {
				t.Fatalf("TLS = %+v, want %+v", got, *tc.want)
			}
		})
	}
}

func TestTarget(t *testing.T) {
	ok := []struct {
		host string
		tls  bool
		want Target
	}{
		{"unix:///var/run/docker.sock", false, Target{Transport: TransportUnix, Socket: "/var/run/docker.sock", DialHost: "unix:///var/run/docker.sock"}},
		{"ssh://prod", false, Target{Transport: TransportSSH, Hostname: "prod", Socket: DefaultRemoteSocket}},
		{"ssh://ops@prod/", false, Target{Transport: TransportSSH, User: "ops", Hostname: "prod", Socket: DefaultRemoteSocket}},
		{"ssh://ops@prod:2222", false, Target{Transport: TransportSSH, User: "ops", Hostname: "prod", Port: "2222", Socket: DefaultRemoteSocket}},
		{"ssh://ops@prod/run/user/1000/docker.sock", false, Target{Transport: TransportSSH, User: "ops", Hostname: "prod", Socket: "/run/user/1000/docker.sock"}},
		{"ssh://[::1]:22/tmp/d.sock", false, Target{Transport: TransportSSH, Hostname: "::1", Port: "22", Socket: "/tmp/d.sock"}},
		{"tcp://10.0.0.5:2375", false, Target{Transport: TransportTCP, Hostname: "10.0.0.5", Port: "2375", DialHost: "tcp://10.0.0.5:2375"}},
		{"tcp://docker.example", false, Target{Transport: TransportTCP, Hostname: "docker.example", Port: "2375", DialHost: "tcp://docker.example:2375"}},
		// With TLS too: the CLI assumes 2375 whenever no port is given,
		// and the desk must dial where the CLI dials.
		{"tcp://docker.example", true, Target{Transport: TransportTCP, Hostname: "docker.example", Port: "2375", DialHost: "tcp://docker.example:2375"}},
		{"tcp://[::1]:2376/", true, Target{Transport: TransportTCP, Hostname: "::1", Port: "2376", DialHost: "tcp://[::1]:2376"}},
		{"tcp://gw.example:8443/docker", true, Target{Transport: TransportTCP, Hostname: "gw.example", Port: "8443", DialHost: "tcp://gw.example:8443/docker"}},
	}
	for _, tc := range ok {
		ep := Endpoint{Host: tc.host}
		if tc.tls {
			ep.TLS = &TLS{}
		}
		got, err := ep.Target()
		if err != nil {
			t.Errorf("%s: %v", tc.host, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n got  %+v\n want %+v", tc.host, got, tc.want)
		}
	}

	bad := []struct{ host, want string }{
		{"/var/run/docker.sock", "not a URL"},
		{"unix://", "no socket path"},
		{"npipe:////./pipe/docker_engine", "unsupported"},
		{"fd://", "unsupported"},
		{"http://x:2375", "unsupported"},
		{"https://x:2376", "unsupported"},
		{"ssh://", "no host"},
		{"ssh://ops:secret@prod", "password"},
		{"ssh://prod/var/run/docker.sock?x=1", "nothing may follow"},
		{"ssh://prod/var/run/docker.sock#frag", "nothing may follow"},
		// Either of these would be read by ssh as an option.
		{"ssh://-oProxyCommand=id/", "may not start with '-'"},
		{"ssh://-oProxyCommand=x@prod", "may not start with '-'"},
		{"ssh://prod/tmp/a:b.sock", "may contain only"},
		{`ssh://prod/tmp/a\b.sock`, "may contain only"},
		// ssh itself expands these in a forwarding's path — the first would
		// send the variable's value to the server. Plain or percent-encoded.
		{"ssh://evil.example/${AWS_SECRET_ACCESS_KEY}", "may contain only"},
		{"ssh://evil.example/%24%7BAWS_SECRET_ACCESS_KEY%7D", "may contain only"},
		{"ssh://prod/run/user/%25i/docker.sock", "may contain only"},
		{"ssh://prod/tmp/with%20space.sock", "may contain only"},
		{"ssh://prod/tmp/x%1b%5B2J.sock", "may contain only"},
		// Hosts and users that a shell, or the terminal, would act on.
		{"ssh://$(id)/", ""},
		{"ssh://a;b/", "the host"},
		{"ssh://user%0Ax@prod/", "the user"},
		{"ssh://us'er@prod/", "the user"},
		{"ssh://ops@prod:22x/", ""},
		{"ssh://%2DoProxyCommand=id@prod/", "may not start with '-'"},
		{"tcp://", "no host"},
		{"tcp://:2375", "no host"},
		{"tcp://user@host:2375", "expected tcp://host:port"},
	}
	for _, tc := range bad {
		_, err := Endpoint{Host: tc.host}.Target()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error containing %q, got %v", tc.host, tc.want, err)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\x1b\n\x07") {
			t.Errorf("%s: the refusal itself carries control characters: %q", tc.host, err.Error())
		}
	}
	if _, err := (Endpoint{Host: "npipe:////./pipe/x"}).Target(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an unknown scheme should be ErrUnsupported, got %v", err)
	}
}

// TestLink: what the operator is told about how the daemon is reached. A
// daemon on another machine is called remote; a link that can be read or
// impersonated is called insecure, in capitals.
func TestLink(t *testing.T) {
	verified := &TLS{CAFile: "/c/ca.pem", CertFile: "/c/cert.pem", KeyFile: "/c/key.pem"}
	cases := []struct {
		ep       Endpoint
		label    string
		remote   bool
		insecure bool
		words    string
	}{
		{Endpoint{Host: "unix:///var/run/docker.sock"}, "", false, false, "unix socket on this machine"},
		{Endpoint{Host: "ssh://ops@prod-1"}, "REMOTE ssh", true, false, "SSH tunnel to /var/run/docker.sock on prod-1"},
		{Endpoint{Host: "ssh://127.0.0.1:2222/tmp/d.sock"}, "ssh", false, false, "SSH tunnel to /tmp/d.sock"},
		{Endpoint{Host: "tcp://10.0.0.5:2376", TLS: verified}, "REMOTE tls", true, false, "TCP to 10.0.0.5:2376 with TLS: daemon verified, client certificate presented"},
		// The port that will be dialled is said, since it may not be the one expected.
		{Endpoint{Host: "tcp://prod-1", TLS: verified}, "REMOTE tls", true, false, "TCP to prod-1:2375 with TLS"},
		{Endpoint{Host: "tcp://10.0.0.5:2376", TLS: &TLS{SkipVerify: true}}, "REMOTE tls, UNVERIFIED", true, true, "NOT verified"},
		{Endpoint{Host: "tcp://10.0.0.5:2375"}, "REMOTE tcp, NO TLS", true, true, "not encrypted"},
		{Endpoint{Host: "tcp://localhost:2375"}, "tcp, NO TLS", false, true, "not encrypted"},
		{Endpoint{Host: "tcp://[::1]:2375"}, "tcp, NO TLS", false, true, "not encrypted"},
		// Never fails: an endpoint that does not parse is refused elsewhere.
		{Endpoint{Host: "bogus"}, "", false, false, "unix socket"},
	}
	for _, tc := range cases {
		l := tc.ep.Link()
		if l.Label != tc.label || l.Remote != tc.remote || l.Insecure != tc.insecure || !strings.Contains(l.Words, tc.words) {
			t.Errorf("%s: got %+v, want label %q remote %v insecure %v words containing %q", tc.ep.Host, l, tc.label, tc.remote, tc.insecure, tc.words)
		}
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

	t.Run("only a local socket is checked this way", func(t *testing.T) {
		if err := CheckSocket(ctx, Endpoint{Host: "tcp://127.0.0.1:2375"}, time.Second); err == nil || !strings.Contains(err.Error(), "not a local unix socket") {
			t.Fatalf("want a refusal, got %v", err)
		}
	})

	t.Run("timeout is classified", func(t *testing.T) {
		u := Classify(Endpoint{Host: "unix:///x.sock"}, context.DeadlineExceeded)
		if !strings.Contains(u.Reason, "did not answer in time") {
			t.Fatalf("got %q", u.Reason)
		}
	})
}

// TestClassifyTCP: each way a TCP daemon can fail to answer is named for
// what it is. The errors are real ones where a real one is cheap to make,
// and the standard library's own types otherwise.
func TestClassifyTCP(t *testing.T) {
	// A port nothing listens on: take one, then give it back.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	_, refused := net.DialTimeout("tcp", closed, time.Second)
	if refused == nil {
		t.Fatal("expected the closed port to refuse")
	}

	plain := Endpoint{Host: "tcp://" + closed}
	withTLS := Endpoint{Host: "tcp://docker.example:2376", TLS: &TLS{CAFile: "/certs/ca.pem", CertFile: "/certs/cert.pem", KeyFile: "/certs/key.pem"}}
	noCert := Endpoint{Host: "tcp://docker.example:2376", TLS: &TLS{CAFile: "/certs/ca.pem"}}
	fromContext := Endpoint{Host: "tcp://docker.example:2376", Context: "prod", TLS: &TLS{CAFile: "/store/ca.pem"}}

	cases := []struct {
		name       string
		ep         Endpoint
		err        error
		reason     string
		hintSubstr string
	}{
		{"connection refused", plain, refused, "connection refused at " + closed, "listen on TCP"},
		{"name does not resolve", withTLS, &net.DNSError{Err: "no such host", Name: "docker.example", IsNotFound: true}, `the host name "docker.example" could not be resolved`, ""},
		{"timeout", withTLS, context.DeadlineExceeded, "no answer from docker.example:2376 in time", "firewall"},
		{"unknown CA", withTLS, fmt.Errorf("ping: %w", x509.UnknownAuthorityError{}), "not signed by the CA in /certs/ca.pem", "not the daemon you expect"},
		{"wrong name", withTLS, fmt.Errorf("ping: %w", x509.HostnameError{Host: "docker.example", Certificate: &x509.Certificate{}}), `not valid for "docker.example"`, "issued for"},
		{"expired", withTLS, fmt.Errorf("ping: %w", x509.CertificateInvalidError{Reason: x509.Expired}), "certificate was rejected", ""},
		{"TLS to a plain port", withTLS, errors.New("http: server gave HTTP response to HTTPS client"), "does not speak TLS", "Unset DOCKER_TLS_VERIFY"},
		{"plain to a TLS port", Endpoint{Host: "tcp://docker.example:2376"}, errors.New("Client sent an HTTP request to an HTTPS server."), "expects TLS, and none is configured", "DOCKER_CERT_PATH"},
		{"client certificate refused", withTLS, errors.New("remote error: tls: bad certificate"), "refused this client's certificate", "signed by its CA"},
		{"client certificate missing", noCert, errors.New("remote error: tls: certificate required"), "requires a client certificate, and none is configured", "cert.pem"},
		{"anything else is shown as it is", plain, errors.New("something new"), "something new", ""},
		// A named context ignores the environment, so a hint about
		// environment variables would send the operator to the wrong place.
		{"TLS to a plain port, from a context", fromContext, errors.New("http: server gave HTTP response to HTTPS client"), "does not speak TLS", `The context "prod" asks for TLS`},
		{"plain to a TLS port, from a context", Endpoint{Host: "tcp://docker.example:2376", Context: "prod"}, errors.New("Client sent an HTTP request to an HTTPS server."), "expects TLS", `The context "prod" stores no certificates`},
		{"the library's one sentence", plain, errors.New("Cannot connect to the Docker daemon at tcp://x. Is the docker daemon running?"), "nothing answered at " + closed, "listening there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := Classify(tc.ep, tc.err)
			if !strings.Contains(u.Reason, tc.reason) {
				t.Errorf("reason %q does not contain %q", u.Reason, tc.reason)
			}
			if tc.hintSubstr != "" && !strings.Contains(u.Hint, tc.hintSubstr) {
				t.Errorf("hint %q does not contain %q", u.Hint, tc.hintSubstr)
			}
			if tc.ep.Context == "prod" && strings.Contains(u.Hint, "DOCKER_TLS") {
				t.Errorf("a context's TLS does not come from the environment; the hint says: %q", u.Hint)
			}
			if !strings.Contains(u.Error(), tc.ep.Host) || !errors.Is(u, tc.err) {
				t.Errorf("the explanation must name the endpoint and keep the cause: %v", u)
			}
		})
	}
}

// TestCheckTCP: a TCP endpoint is tried directly first, because the client
// library has one sentence for every failure to connect.
func TestCheckTCP(t *testing.T) {
	ctx := context.Background()
	// The check is skipped when the address would go through a proxy.
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().String()
	defer l.Close()
	if err := CheckTCP(ctx, Endpoint{Host: "tcp://" + open}, time.Second); err != nil {
		t.Fatalf("a listening port: %v", err)
	}

	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l2.Addr().String()
	l2.Close()
	err = CheckTCP(ctx, Endpoint{Host: "tcp://" + closed}, time.Second)
	var u *Unreachable
	if !errors.As(err, &u) || !strings.Contains(u.Reason, "connection refused at "+closed) {
		t.Fatalf("a closed port: %v", err)
	}

	if err := CheckTCP(ctx, Endpoint{Host: "unix:///x.sock"}, time.Second); err == nil {
		t.Fatal("only a TCP endpoint is checked this way")
	}
}
