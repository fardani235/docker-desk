package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/sshtest"
)

// These tests run the program's own launch sequence — flags, resolution,
// transport, the first requests, the messages — in process, with --check, so
// that nothing needs a terminal. The daemon is always the fake one.

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

// launch runs the program with a clean Docker environment plus env.
func launch(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	home := t.TempDir()
	base := map[string]string{
		"HOME": home, "DOCKER_CONFIG": filepath.Join(home, ".docker"),
		"DOCKER_HOST": "", "DOCKER_CONTEXT": "", "DOCKER_TLS_VERIFY": "", "DOCKER_TLS": "", "DOCKER_CERT_PATH": "",
		"DOCKER_DESK_THEME": "", "DOCKER_DESK_SSH": "",
	}
	for k, v := range env {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code = run(args, os.Stdin, &out, &errb)
	return code, out.String(), errb.String()
}

func wantAll(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("%s does not contain %q:\n%s", what, s, got)
		}
	}
}

func newFake(t *testing.T) *fakeengine.Server {
	t.Helper()
	srv := fakeengine.New()
	srv.SeedDemo()
	srv.Name = "fake-remote"
	return srv
}

func unixFake(t *testing.T) *fakeengine.Server {
	t.Helper()
	srv := newFake(t)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func tcpFake(t *testing.T) *fakeengine.Server {
	t.Helper()
	srv := newFake(t)
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func tlsFake(t *testing.T) (srv *fakeengine.Server, certDir string) {
	t.Helper()
	srv = newFake(t)
	certDir = t.TempDir()
	if err := srv.StartTLS(certDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, certDir
}

func TestCheckLocalSocket(t *testing.T) {
	srv := unixFake(t)
	code, out, errs := launch(t, map[string]string{"DOCKER_HOST": srv.Host()}, "--check")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out,
		"endpoint   "+srv.Host(), "context    default", "chosen by  DOCKER_HOST environment variable",
		"via        a unix socket on this machine", "engine     "+fakeengine.EngineVersion+" on fake-remote", "mode       read-write")
}

func TestCheckOverTCP(t *testing.T) {
	srv := tcpFake(t)
	code, out, errs := launch(t, nil, "--host", srv.Host(), "--check", "--read-only")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out, "endpoint   "+srv.Host(), "chosen by  --host flag",
		"via        TCP to "+strings.TrimPrefix(srv.Host(), "tcp://")+" without TLS: not encrypted", "on fake-remote", "mode       read-only")

	// -H is the same flag, and wins over DOCKER_HOST.
	code, out, errs = launch(t, map[string]string{"DOCKER_HOST": "unix:///nonexistent.sock"}, "-H", srv.Host(), "--check")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out, "endpoint   "+srv.Host())
}

func TestCheckOverTLS(t *testing.T) {
	srv, certs := tlsFake(t)
	plain := tcpFake(t)
	_, otherCerts := tlsFake(t)
	junk := t.TempDir()
	if err := os.WriteFile(filepath.Join(junk, "ca.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	caOnly := t.TempDir()
	ca, _ := os.ReadFile(filepath.Join(certs, "ca.pem"))
	if err := os.WriteFile(filepath.Join(caOnly, "ca.pem"), ca, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("verified, with the client certificate", func(t *testing.T) {
		code, out, errs := launch(t, map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs}, "--check")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		wantAll(t, "output", out, "via        TCP to "+strings.TrimPrefix(srv.Host(), "tcp://")+" with TLS: daemon verified, client certificate presented", "on fake-remote")
	})

	t.Run("unverified is allowed, and said", func(t *testing.T) {
		code, out, errs := launch(t, map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS": "1", "DOCKER_CERT_PATH": certs}, "--check")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		wantAll(t, "output", out, "with TLS, but the daemon's certificate is NOT verified")
	})

	failures := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"no TLS towards a TLS port", map[string]string{"DOCKER_HOST": srv.Host()},
			[]string{"via        TCP to ", "without TLS", "expects TLS, and none is configured", "DOCKER_CERT_PATH"}},
		{"TLS towards a plain port", map[string]string{"DOCKER_HOST": plain.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs},
			[]string{"TLS was requested, but", "does not speak TLS", "Unset DOCKER_TLS_VERIFY"}},
		{"no client certificate", map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": caOnly},
			[]string{"requires a client certificate, and none is configured"}},
		{"the wrong CA", map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": otherCerts},
			[]string{"not signed by the CA in " + filepath.Join(otherCerts, "ca.pem"), "not the daemon you expect"}},
		{"verification asked for without a CA", map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": t.TempDir()},
			[]string{"cannot work out which Docker daemon to use", "the CA certificate to verify"}},
		// Not "the daemon is not reachable": nothing was tried. The fault
		// is a file on this machine, and the message says which.
		{"a CA file that holds no certificate", map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": junk},
			[]string{"cannot set up the connection to the Docker daemon", filepath.Join(junk, "ca.pem") + " contains no PEM certificate"}},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := launch(t, tc.env, "--check")
			if code != 1 || out != "" {
				t.Fatalf("exit %d, stdout %q; want a refusal on stderr:\n%s", code, out, errs)
			}
			wantAll(t, "message", errs, append(tc.want, "The workspace was not opened.")...)
			if strings.Contains(tc.name, "CA file") && strings.Contains(errs, "not reachable") {
				t.Errorf("a local set-up fault was reported as an unreachable daemon:\n%s", errs)
			}
		})
	}
}

// TestDefaultContextFollowsTheCLI: found by the review and checked against
// the docker CLI itself — "default" is not "the local socket".
func TestDefaultContextFollowsTheCLI(t *testing.T) {
	srv := unixFake(t)
	code, out, errs := launch(t, map[string]string{"DOCKER_HOST": srv.Host()}, "--context", "default", "--check")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out, "endpoint   "+srv.Host(), "context    default",
		"chosen by  DOCKER_HOST environment variable (the default context, selected by --context flag)")

	// With TLS asked for and no host named anywhere, the CLI goes to
	// tcp://localhost:2376; so does the desk, and says why.
	tlsSrv, certs := tlsFake(t)
	_ = tlsSrv
	code, _, errs = launch(t, map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": certs, "HTTP_PROXY": "", "HTTPS_PROXY": "", "NO_PROXY": "*"}, "--check")
	if code != 1 {
		t.Fatalf("exit %d (nothing listens on localhost:2376 in this test)", code)
	}
	wantAll(t, "message", errs, "endpoint   tcp://localhost:2376", "the Docker CLI's default when DOCKER_TLS_VERIFY or DOCKER_TLS is set")
}

// TestContextWrittenByDockerItself: the context store — where the endpoint
// and its certificates live — is read by code written here, not by Docker's
// (DECISIONS D4). So the store is written by the real docker CLI, the same
// CLI is shown to reach the fake through it, and then the desk must agree.
func TestContextWrittenByDockerItself(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("the docker CLI is not installed")
	}
	srv, certs := tlsFake(t)
	cfg := filepath.Join(t.TempDir(), "dockercfg")
	cli := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(docker, append([]string{"--config", cfg}, args...)...)
		// Nothing of the caller's Docker environment: only this store.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	cli("context", "create", "fake-tls", "--docker",
		"host="+srv.Host()+",ca="+filepath.Join(certs, "ca.pem")+",cert="+filepath.Join(certs, "cert.pem")+",key="+filepath.Join(certs, "key.pem"))
	if got := strings.TrimSpace(cli("--context", "fake-tls", "version", "--format", "{{.Server.Version}}")); got != fakeengine.EngineVersion {
		t.Fatalf("the docker CLI itself should reach the fake through this context; it reported %q", got)
	}

	code, out, errs := launch(t, map[string]string{"DOCKER_CONFIG": cfg}, "--context", "fake-tls", "--check")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out, "endpoint   "+srv.Host(), "context    fake-tls", "chosen by  --context flag",
		"with TLS: daemon verified, client certificate presented", "on fake-remote")

	// `docker context use` is honoured too, with no flag at all.
	cli("context", "use", "fake-tls")
	code, out, errs = launch(t, map[string]string{"DOCKER_CONFIG": cfg}, "--check")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "output", out, "context    fake-tls", "chosen by  currentContext in "+filepath.Join(cfg, "config.json"))
}

func TestCheckOverSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := unixFake(t)
	env := map[string]string{"DOCKER_DESK_SSH": sshd.CommandString()}

	t.Run("reaches the daemon", func(t *testing.T) {
		url := sshd.URL(srv.Socket())
		code, out, errs := launch(t, env, "--host", url, "--check")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		wantAll(t, "output", out, "endpoint   "+url, "via        an SSH tunnel to "+srv.Socket()+" on 127.0.0.1", "on fake-remote")
	})

	t.Run("no daemon socket on the far side", func(t *testing.T) {
		gone := filepath.Join(filepath.Dir(srv.Socket()), "gone.sock")
		code, _, errs := launch(t, env, "--host", sshd.URL(gone), "--check")
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		wantAll(t, "message", errs, "the Docker daemon is not reachable", "via        an SSH tunnel",
			"reason     there is no socket at "+gone+" on 127.0.0.1", "rootless", "The workspace was not opened.")
	})

	t.Run("ssh cannot log in", func(t *testing.T) {
		other := sshtest.Start(t, sshtest.Options{})
		other.UseWrongKey()
		code, _, errs := launch(t, map[string]string{"DOCKER_DESK_SSH": other.CommandString()}, "--host", other.URL(srv.Socket()), "--check")
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		wantAll(t, "message", errs, "Permission denied (publickey)", "hint       ssh could not log in", "The workspace was not opened.")
	})

	t.Run("no ssh client", func(t *testing.T) {
		code, _, errs := launch(t, map[string]string{"DOCKER_DESK_SSH": "docker-desk-no-such-ssh"}, "--host", sshd.URL(srv.Socket()), "--check")
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		wantAll(t, "message", errs, `the "docker-desk-no-such-ssh" program was not found`, "DOCKER_DESK_SSH")
	})

	// Found by the review: a daemon that accepts the connection and then
	// says nothing was explained as "the SSH server could not connect to
	// it". The server did; silence is not a refusal.
	t.Run("a daemon that does not answer", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "ddhung")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		hung := filepath.Join(dir, "d.sock")
		l, err := net.Listen("unix", hung)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close() // held open, never answered
			}
		}()
		old := remoteTimeout
		remoteTimeout = 1500 * time.Millisecond
		defer func() { remoteTimeout = old }()
		code, _, errs := launch(t, env, "--host", sshd.URL(hung), "--check")
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		wantAll(t, "message", errs, "is up, but nothing answered on "+hung+" within 1.5s", "hung or very busy")
		if strings.Contains(errs, "could not connect to it") || strings.Contains(errs, "AllowStreamLocalForwarding") {
			t.Errorf("silence was explained as a refusal:\n%s", errs)
		}
	})

	// A destination ssh would do something with never gets as far as ssh.
	t.Run("a socket path that ssh would expand", func(t *testing.T) {
		code, _, errs := launch(t, env, "--host", "ssh://127.0.0.1:"+sshd.Port+"/tmp/${HOME}/d.sock", "--check")
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		wantAll(t, "message", errs, "cannot use this Docker endpoint", "may contain only letters, digits")
	})
}

// TestLaunchLeavesNothingBehind: after a launch over SSH — one that worked
// and one that did not — there is no tunnel directory, no socket and no ssh
// process. The temporary directory is this test's own, so that other test
// binaries' tunnels are not in the picture.
func TestLaunchLeavesNothingBehind(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := unixFake(t)
	tmp, err := os.MkdirTemp("", "ddmain")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	env := map[string]string{"DOCKER_DESK_SSH": sshd.CommandString(), "TMPDIR": tmp}

	if code, _, errs := launch(t, env, "--host", sshd.URL(srv.Socket()), "--check"); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if code, _, _ := launch(t, env, "--host", sshd.URL(filepath.Join(filepath.Dir(srv.Socket()), "gone.sock")), "--check"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) != 0 {
		t.Fatalf("left in the temporary directory: %v", left)
	}
	if out, _ := exec.Command("pgrep", "-f", "--", "-F "+sshd.Command()[2]).Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("ssh is still running: %s", out)
	}
}

func TestRefusals(t *testing.T) {
	srv := unixFake(t)
	cases := []struct {
		name string
		env  map[string]string
		args []string
		code int
		want []string
	}{
		{"--host with --context", nil, []string{"--host", srv.Host(), "--context", "x", "--check"}, 1,
			[]string{"cannot work out which Docker daemon to use", "cannot be used together"}},
		{"a scheme the desk does not speak", map[string]string{"DOCKER_HOST": "npipe:////./pipe/docker_engine"}, []string{"--check"}, 1,
			[]string{"cannot use this Docker endpoint", "npipe:////./pipe/docker_engine", "unsupported endpoint scheme", "unix://, ssh:// and tcp://"}},
		{"an SSH host that ssh would read as an option", nil, []string{"--host", "ssh://-oProxyCommand=id/", "--check"}, 1,
			[]string{"cannot use this Docker endpoint", "may not start with '-'"}},
		{"a password in the URL", nil, []string{"--host", "ssh://ops:hunter2@prod", "--check"}, 1,
			[]string{"a password in the URL is not supported"}},
		{"nothing listens on the TCP port", nil, []string{"--host", "tcp://127.0.0.1:1", "--check"}, 1,
			[]string{"the Docker daemon is not reachable", "connection refused at 127.0.0.1:1", "listen on TCP"}},
		{"a context that does not exist", nil, []string{"--context", "ghost", "--check"}, 1,
			[]string{`docker context "ghost" (selected by --context flag)`, "context not found"}},
		{"an unknown theme", map[string]string{"DOCKER_HOST": srv.Host()}, []string{"--theme", "neon", "--check"}, 2,
			[]string{`unknown theme "neon"`, "dracula", "light"}},
		{"an unknown theme in the environment", map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_DESK_THEME": "neon"}, []string{"--check"}, 2,
			[]string{`unknown theme "neon"`}},
		{"an unusable DOCKER_DESK_SSH", map[string]string{"DOCKER_DESK_SSH": `ssh "-F`}, []string{"--host", "ssh://prod", "--check"}, 2,
			[]string{"DOCKER_DESK_SSH is not usable"}},
		{"the workspace needs a terminal", map[string]string{"DOCKER_HOST": srv.Host()}, nil, 1,
			[]string{"needs an interactive terminal", "--check"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := launch(t, tc.env, tc.args...)
			if code != tc.code || out != "" {
				t.Fatalf("exit %d (want %d), stdout %q\n%s", code, tc.code, out, errs)
			}
			wantAll(t, "message", errs, tc.want...)
		})
	}
}

func TestThemeIsAcceptedEitherWay(t *testing.T) {
	srv := unixFake(t)
	for _, tc := range []struct {
		env  map[string]string
		args []string
	}{
		{map[string]string{"DOCKER_HOST": srv.Host()}, []string{"--theme", "nord", "--check"}},
		{map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_DESK_THEME": "Solarized-Light"}, []string{"--check"}},
		{map[string]string{"DOCKER_HOST": srv.Host(), "DOCKER_DESK_THEME": "neon"}, []string{"--theme", "auto", "--check"}}, // the flag wins
	} {
		if code, _, errs := launch(t, tc.env, tc.args...); code != 0 {
			t.Errorf("%v %v: exit %d: %s", tc.env, tc.args, code, errs)
		}
	}
}

func TestHelp(t *testing.T) {
	code, out, _ := launch(t, nil, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantAll(t, "help", out, "-H, --host URL", "-c, --context NAME", "--theme NAME", "ssh://[user@]host[:port][/path]", "tcp://host[:port]",
		"DOCKER_TLS_VERIFY", "DOCKER_DESK_SSH", "Themes:", "auto", "dracula", "solarized-light", "mono")
}
