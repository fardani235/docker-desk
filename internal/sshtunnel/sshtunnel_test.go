package sshtunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/testguard"
)

// These tests run the tunnel against a stand-in for the ssh client: this
// test binary itself, started with DD_FAKE_SSH set. It takes ssh's command
// line, forwards the local socket to the "remote" one on this machine, and
// can be told to fail the ways ssh fails. That makes every path of the
// supervisor testable anywhere; real_test.go then repeats what matters with
// the real ssh and a real sshd.

func TestMain(m *testing.M) {
	if os.Getenv("DD_FAKE_SSH") != "" {
		fakeSSH()
		return
	}
	testguard.Install()
	os.Exit(m.Run())
}

// invocation is one start of the fake ssh, as it recorded itself.
type invocation struct {
	PID     int
	Args    []string
	Local   string
	Remote  string
	Batch   bool
	Command string
	// Leader is whether the process leads a session of its own — that is,
	// was started without a controlling terminal.
	Leader bool
	// Askpass is SSH_ASKPASS_REQUIRE as the process saw it.
	Askpass string
	// StayInFront is whether ForkAfterAuthentication=no was given.
	StayInFront bool
}

func fakeSSH() {
	args := os.Args[1:]
	inv := invocation{PID: os.Getpid(), Args: args, Askpass: os.Getenv("SSH_ASKPASS_REQUIRE")}
	if sid, err := unix.Getsid(0); err == nil {
		inv.Leader = sid == os.Getpid()
	}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" && i+1 < len(args):
			i++
			switch args[i] {
			case "BatchMode=yes":
				inv.Batch = true
			case "ForkAfterAuthentication=no":
				inv.StayInFront = true
			}
		case a == "-L" && i+1 < len(args):
			i++
			inv.Local, inv.Remote, _ = strings.Cut(args[i], ":")
		case (a == "-l" || a == "-p" || a == "-F") && i+1 < len(args):
			i++
		case a == "--":
			if i+2 < len(args) {
				inv.Command = strings.Join(args[i+2:], " ")
			}
			i = len(args)
		}
	}
	if path := os.Getenv("DD_FAKE_SSH_LOG"); path != "" {
		line, _ := json.Marshal(inv)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	mode := os.Getenv("DD_FAKE_SSH")
	if data, err := os.ReadFile(os.Getenv("DD_FAKE_SSH_MODE_FILE")); err == nil {
		mode = strings.TrimSpace(string(data))
	}
	if inv.Command != "" { // the remote probe of Explain
		fmt.Println(os.Getenv("DD_FAKE_SSH_PROBE"))
		os.Exit(0)
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	if mode == "oldssh" && inv.StayInFront {
		// An OpenSSH from before the option existed.
		fmt.Fprintln(os.Stderr, "command-line: line 0: Bad configuration option: forkafterauthentication")
		os.Exit(255)
	}
	switch mode {
	case "background":
		os.Exit(0) // -f / ForkAfterAuthentication: the parent leaves quietly
	case "denied":
		fmt.Fprintln(os.Stderr, "Warning: Permanently added '[prod]:2222' (ED25519) to the list of known hosts.")
		fmt.Fprintln(os.Stderr, "ops@prod: Permission denied (publickey).")
		os.Exit(255)
	case "hostkey":
		fmt.Fprintln(os.Stderr, "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
		fmt.Fprintln(os.Stderr, "Host key verification failed.")
		os.Exit(255)
	case "silent":
		os.Exit(255)
	case "hang":
		<-term
		os.Exit(255)
	case "nobind":
		fmt.Fprintf(os.Stderr, "unix_listener: cannot bind to path %s: Permission denied\n", inv.Local)
		fmt.Fprintln(os.Stderr, "Could not request local forwarding.")
		<-term
		os.Exit(255)
	}
	_ = os.Remove(inv.Local) // StreamLocalBindUnlink=yes
	l, err := net.Listen("unix", inv.Local)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake ssh:", err)
		os.Exit(255)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, err := net.Dial("unix", inv.Remote)
				if err != nil {
					fmt.Fprintln(os.Stderr, "channel 2: open failed: connect failed: open failed")
					return
				}
				defer r.Close()
				go func() { _, _ = io.Copy(r, c); _ = r.(*net.UnixConn).CloseWrite() }()
				_, _ = io.Copy(c, r)
			}()
		}
	}()
	<-term
	fmt.Fprintln(os.Stderr, "Connection to prod closed by remote host.")
	os.Exit(255)
}

// fake prepares the environment so that Options.Command = fakeCommand()
// starts this binary as ssh in the given mode.
type fake struct {
	t        *testing.T
	log      string
	modeFile string
}

func newFake(t *testing.T, mode string) *fake {
	t.Helper()
	dir := t.TempDir()
	f := &fake{t: t, log: filepath.Join(dir, "invocations"), modeFile: filepath.Join(dir, "mode")}
	t.Setenv("DD_FAKE_SSH", mode)
	t.Setenv("DD_FAKE_SSH_LOG", f.log)
	t.Setenv("DD_FAKE_SSH_MODE_FILE", f.modeFile)
	return f
}

func (f *fake) command() []string { return []string{os.Args[0]} }

func (f *fake) setMode(mode string) {
	f.t.Helper()
	if err := os.WriteFile(f.modeFile, []byte(mode), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fake) invocations() []invocation {
	f.t.Helper()
	data, _ := os.ReadFile(f.log)
	var out []invocation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var inv invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			f.t.Fatalf("bad invocation record %q: %v", line, err)
		}
		out = append(out, inv)
	}
	return out
}

func startEngine(t *testing.T) *fakeengine.Server {
	t.Helper()
	srv := fakeengine.New()
	srv.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func (f *fake) options(srv *fakeengine.Server) Options {
	return Options{
		User: "ops", Host: "prod", Port: "2222",
		RemoteSocket: srv.Socket(),
		Command:      f.command(),
		MinBackoff:   20 * time.Millisecond,
		MaxBackoff:   80 * time.Millisecond,
		RetryTimeout: 2 * time.Second,
	}
}

func open(t *testing.T, o Options) *Tunnel {
	t.Helper()
	tun, err := Open(context.Background(), o)
	if err != nil {
		t.Fatalf("opening the tunnel: %v", err)
	}
	t.Cleanup(tun.Close)
	return tun
}

func ping(t *testing.T, host string) error {
	t.Helper()
	eng, err := engine.New(engine.Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return eng.Ping(ctx)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// ---- the command line ----------------------------------------------------------

func TestCommandLine(t *testing.T) {
	o := Options{User: "ops", Host: "prod-1", Port: "2222", RemoteSocket: "/run/user/1000/docker.sock", Command: []string{"ssh", "-F", "/etc/my.conf"}}
	want := []string{
		"-F", "/etc/my.conf",
		"-N", "-T",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "StreamLocalBindMask=0177",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"-o", "ForkAfterAuthentication=no",
		"-o", "BatchMode=yes",
		"-L", "/tmp/x/d.sock:/run/user/1000/docker.sock",
		"-l", "ops", "-p", "2222", "--", "prod-1",
	}
	if got := o.args("/tmp/x/d.sock", true, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch command line:\n got  %q\n want %q", got, want)
	}
	// Interactive: the same without BatchMode, so that ssh may ask; and for
	// an ssh that predates it, without ForkAfterAuthentication.
	for _, a := range o.args("/tmp/x/d.sock", false, false) {
		if strings.Contains(a, "BatchMode") || strings.Contains(a, "ForkAfterAuthentication") {
			t.Fatalf("unexpected option %q", a)
		}
	}
	// User and port are ssh's to decide when the URL does not give them,
	// and whatever the host is, it comes after "--": never an option.
	bare := Options{Host: "-oProxyCommand=evil", RemoteSocket: "/s", Command: []string{"ssh"}}
	got := bare.args("/l", true, true)
	if n := len(got); got[n-2] != "--" || got[n-1] != "-oProxyCommand=evil" || strings.Contains(strings.Join(got, " "), "-l ") {
		t.Fatalf("destination handling: %q", got)
	}
	if d := o.Destination(); d != "-p 2222 ops@prod-1" {
		t.Fatalf("destination for messages: %q", d)
	}
}

func TestSplitCommand(t *testing.T) {
	ok := map[string][]string{
		"ssh":                             {"ssh"},
		"  ssh   -F  /x/y  ":              {"ssh", "-F", "/x/y"},
		`"/opt/my ssh/bin/ssh" -i 'a b'`:  {"/opt/my ssh/bin/ssh", "-i", "a b"},
		`ssh -o "ProxyCommand=nc %h %p"`:  {"ssh", "-o", "ProxyCommand=nc %h %p"},
		`ssh -o UserKnownHostsFile=""`:    {"ssh", "-o", "UserKnownHostsFile="},
		"/usr/bin/ssh\t-4":                {"/usr/bin/ssh", "-4"},
		`ssh -i ~/.ssh/key -o "A=b"'c'`:   {"ssh", "-i", "~/.ssh/key", "-o", "A=bc"},
		`ssh '' x`:                        {"ssh", "", "x"},
		`ssh -J jump.example -p 22`:       {"ssh", "-J", "jump.example", "-p", "22"},
		`"ssh"`:                           {"ssh"},
		`ssh -o 'IdentityFile=/k/id rsa'`: {"ssh", "-o", "IdentityFile=/k/id rsa"},
	}
	for in, want := range ok {
		got, err := SplitCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", `ssh "unterminated`} {
		if _, err := SplitCommand(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

// ---- opening -------------------------------------------------------------------

func TestForwardsToTheRemoteSocket(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	tun := open(t, f.options(srv))

	if !strings.HasPrefix(tun.Host(), "unix://") || !testguard.UnderTemp(strings.TrimPrefix(tun.Host(), "unix://")) {
		t.Fatalf("the local end should be a unix socket in the temporary directory, got %s", tun.Host())
	}
	if st, err := os.Stat(tun.dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the socket's directory must be the operator's alone: %v %v", st.Mode().Perm(), err)
	}
	if err := ping(t, tun.Host()); err != nil {
		t.Fatalf("ping through the tunnel: %v", err)
	}
	if !tun.Up() || tun.Problem() != "" || tun.Restarts() != 0 {
		t.Fatalf("a healthy tunnel: up=%v problem=%q restarts=%d", tun.Up(), tun.Problem(), tun.Restarts())
	}
	inv := f.invocations()
	if len(inv) != 1 || inv[0].Remote != srv.Socket() || "unix://"+inv[0].Local != tun.Host() || !inv[0].Batch {
		t.Fatalf("ssh should have been started once, in batch mode, forwarding to the daemon's socket: %+v", inv)
	}
}

func TestInteractiveOnlyForTheFirstConnection(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	o := f.options(srv)
	o.Interactive = true
	tun := open(t, o)

	first := f.invocations()[0]
	if first.Batch {
		t.Fatal("the first connection of an interactive launch must let ssh ask its questions")
	}
	_ = syscall.Kill(first.PID, syscall.SIGTERM)
	eventually(t, "the tunnel to come back", func() bool { return tun.Restarts() == 1 && tun.Up() })
	for _, inv := range f.invocations()[1:] {
		if !inv.Batch {
			t.Fatal("a reconnection happens under a full-screen program and must never ask anything")
		}
	}
}

func TestOpenFailsInSSHsOwnWords(t *testing.T) {
	srv := startEngine(t)
	cases := []struct {
		mode       string
		reason     string
		hint       string
		notInWords string
	}{
		{"denied", "ssh: ops@prod: Permission denied (publickey).", " -p 2222 ops@prod` works from this terminal", "Permanently added"},
		{"hostkey", "Host key verification failed.", "review it", ""},
		{"silent", "ssh exited without saying why (exit status 255)", "works on its own", ""},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFake(t, tc.mode)
			_, err := Open(context.Background(), f.options(srv))
			var te *Error
			if !errors.As(err, &te) {
				t.Fatalf("want *Error, got %v", err)
			}
			if !strings.Contains(te.Reason, tc.reason) || !strings.Contains(te.Hint, tc.hint) {
				t.Fatalf("reason %q / hint %q; want %q / %q", te.Reason, te.Hint, tc.reason, tc.hint)
			}
			if tc.notInWords != "" && strings.Contains(te.Reason, tc.notInWords) {
				t.Fatalf("a notice is not a reason: %q", te.Reason)
			}
			if n := len(f.invocations()); n != 1 {
				t.Fatalf("a failed first connection is not retried behind the operator's back: %d starts", n)
			}
		})
	}

	t.Run("no ssh program", func(t *testing.T) {
		f := newFake(t, "ok")
		o := f.options(srv)
		o.Command = []string{"docker-desk-no-such-ssh"}
		_, err := Open(context.Background(), o)
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Reason, `"docker-desk-no-such-ssh" program was not found`) || !strings.Contains(te.Hint, "DOCKER_DESK_SSH") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("never gets there", func(t *testing.T) {
		f := newFake(t, "hang")
		o := f.options(srv)
		o.FirstTimeout = 300 * time.Millisecond
		start := time.Now()
		_, err := Open(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "did not finish connecting in time") {
			t.Fatalf("got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("the wait is bounded; took %s", d)
		}
		eventually(t, "the hanging ssh to be ended", func() bool { return !alive(f.invocations()[0].PID) })
	})

	t.Run("forwarding cannot be set up", func(t *testing.T) {
		// ssh stays alive when its listener cannot be created; waiting
		// for the socket would only end at the timeout.
		f := newFake(t, "nobind")
		start := time.Now()
		_, err := Open(context.Background(), f.options(srv))
		if err == nil || !strings.Contains(err.Error(), "could not set up the forwarding") {
			t.Fatalf("got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("should fail as soon as ssh says so; took %s", d)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		f := newFake(t, "hang")
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(150*time.Millisecond, cancel)
		_, err := Open(ctx, f.options(srv))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want a cancellation, got %v", err)
		}
		eventually(t, "ssh to be ended", func() bool { return !alive(f.invocations()[0].PID) })
	})

	t.Run("nothing is left behind", func(t *testing.T) {
		// A temporary directory of this test's own, so that tunnels opened
		// by other test binaries running at the same time are not counted.
		tmp, err := os.MkdirTemp("", "ddt")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmp) })
		t.Setenv("TMPDIR", tmp)
		own := startEngine(t)
		f := newFake(t, "denied")
		if _, err := Open(context.Background(), f.options(own)); err == nil {
			t.Fatal("expected a failure")
		}
		if left, _ := filepath.Glob(filepath.Join(tmp, "docker-desk-ssh-*")); len(left) != 0 {
			t.Fatalf("a failed tunnel left its directory behind: %v", left)
		}
	})
}

func TestMissingArguments(t *testing.T) {
	for _, o := range []Options{{RemoteSocket: "/tmp/x.sock"}, {Host: "prod"}} {
		if _, err := Open(context.Background(), o); err == nil {
			t.Errorf("%+v: want an error", o)
		}
	}
}

// ---- staying up ----------------------------------------------------------------

func TestSupervisorReconnects(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	tun := open(t, f.options(srv))
	host := tun.Host()

	_ = syscall.Kill(f.invocations()[0].PID, syscall.SIGTERM)
	eventually(t, "the tunnel to notice", func() bool { return tun.Restarts() >= 1 || !tun.Up() })
	eventually(t, "the tunnel to come back", func() bool { return tun.Up() && tun.Restarts() == 1 })
	if tun.Host() != host {
		t.Fatal("the local socket must stay where it was: the engine client keeps dialling it")
	}
	if err := ping(t, host); err != nil {
		t.Fatalf("ping after the reconnect: %v", err)
	}
	if tun.Problem() != "" {
		t.Fatalf("a tunnel that is up has no problem: %q", tun.Problem())
	}
}

func TestSupervisorKeepsTryingAndSaysWhy(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	tun := open(t, f.options(srv))

	f.setMode("denied")
	_ = syscall.Kill(f.invocations()[0].PID, syscall.SIGTERM)
	eventually(t, "the reason to be ssh's", func() bool { return strings.Contains(tun.Problem(), "Permission denied") })
	if tun.Up() {
		t.Fatal("still reported as up")
	}
	if err := ping(t, tun.Host()); !engine.IsUnavailable(err) {
		t.Fatalf("with the tunnel down the daemon must read as unavailable, got %v", err)
	}
	eventually(t, "several attempts", func() bool { return len(f.invocations()) >= 4 })

	f.setMode("ok")
	eventually(t, "the tunnel to come back", func() bool { return tun.Up() })
	if err := ping(t, tun.Host()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseEndsSSHAndRemovesTheSocket(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	tun, err := Open(context.Background(), f.options(srv))
	if err != nil {
		t.Fatal(err)
	}
	pid, dir := f.invocations()[0].PID, tun.dir
	tun.Close()
	if alive(pid) {
		t.Fatal("ssh is still running after Close")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the tunnel's directory is still there: %v", err)
	}
	tun.Close() // twice is fine
	time.Sleep(150 * time.Millisecond)
	if n := len(f.invocations()); n != 1 {
		t.Fatalf("Close must not be mistaken for a dropped link: %d starts", n)
	}
}

func TestCloseWhileReconnecting(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	tun, err := Open(context.Background(), f.options(srv))
	if err != nil {
		t.Fatal(err)
	}
	f.setMode("hang")
	_ = syscall.Kill(f.invocations()[0].PID, syscall.SIGTERM)
	eventually(t, "a reconnection attempt", func() bool { return len(f.invocations()) >= 2 })
	done := make(chan struct{})
	go func() { tun.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked behind a reconnection attempt")
	}
	for _, inv := range f.invocations() {
		pid := inv.PID
		eventually(t, "every ssh to be gone", func() bool { return !alive(pid) })
	}
}

// TestDetach: what ssh says reaches the terminal only until the desk takes
// the terminal over; afterwards it is kept for Problem and shown by the desk.
func TestDetach(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	var shown lockedBuffer
	o := f.options(srv)
	o.Stderr = &shown
	tun := open(t, o)

	tun.Detach()
	f.setMode("denied")
	_ = syscall.Kill(f.invocations()[0].PID, syscall.SIGTERM)
	eventually(t, "the failure to be recorded", func() bool { return strings.Contains(tun.Problem(), "Permission denied") })
	if s := shown.String(); s != "" {
		t.Fatalf("ssh wrote to the terminal after Detach: %q", s)
	}

	// Before Detach it is passed on, so that a prompt's context is seen.
	f2 := newFake(t, "denied")
	var shown2 lockedBuffer
	o2 := f2.options(srv)
	o2.Stderr = &shown2
	if _, err := Open(context.Background(), o2); err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(shown2.String(), "Permission denied") {
		t.Fatalf("ssh's message was not passed on: %q", shown2.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// ---- the guard and the explanation ---------------------------------------------

// TestGuardRefusesTunnelsToRealSockets: the tunnel's local end is always in
// the temporary directory, so without this the engine's guard could be
// walked around by tunnelling to a real daemon.
func TestGuardRefusesTunnelsToRealSockets(t *testing.T) {
	f := newFake(t, "ok")
	for _, sock := range []string{"/var/run/docker.sock", "/run/user/1000/docker.sock"} {
		_, err := Open(context.Background(), Options{Host: "prod", RemoteSocket: sock, Command: f.command()})
		if err == nil || !strings.Contains(err.Error(), "test guard") {
			t.Errorf("%s: want a refusal by the test guard, got %v", sock, err)
		}
	}
	if n := len(f.invocations()); n != 0 {
		t.Fatalf("ssh was started %d time(s) for a refused tunnel", n)
	}
}

func TestExplain(t *testing.T) {
	srv := startEngine(t)
	cases := []struct{ probe, reason, hint string }{
		{"dd-probe:missing", "there is no socket at " + srv.Socket() + " on prod", "ssh://user@host/run/user/1000/docker.sock"},
		{"dd-probe:notsocket", "exists but is not a socket", ""},
		{"dd-probe:denied", "the remote user ops may not open", "`docker` group"},
		{"dd-probe:hidden /run/user/1001", "the remote user ops may not look inside /run/user/1001 on prod", "rootless"},
		{"dd-probe:ok", "is there and the remote user ops may open it, yet the SSH server could not connect to it", "AllowStreamLocalForwarding"},
		{"This account is currently not available.", "the SSH connection to prod is up, but the server could not open " + srv.Socket(), "DisableForwarding"},
	}
	for _, tc := range cases {
		t.Run(tc.probe, func(t *testing.T) {
			f := newFake(t, "ok")
			t.Setenv("DD_FAKE_SSH_PROBE", tc.probe)
			tun := open(t, f.options(srv))
			reason, hint := tun.Explain(context.Background())
			if !strings.Contains(reason, tc.reason) || !strings.Contains(hint, tc.hint) {
				t.Fatalf("reason %q / hint %q; want %q / %q", reason, hint, tc.reason, tc.hint)
			}
			probe := f.invocations()[1]
			if !probe.Batch || !strings.HasPrefix(probe.Command, "sh -c '") || !strings.Contains(probe.Command, srv.Socket()) {
				t.Fatalf("the probe must be a batch-mode `sh -c` naming the socket: %+v", probe)
			}
		})
	}
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/var/run/docker.sock": "'/var/run/docker.sock'",
		"it's":                 `'it'\''s'`,
		"$(reboot) `x` ; rm":   "'$(reboot) `x` ; rm'",
	} {
		if got := shQuote(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

func TestLastLines(t *testing.T) {
	said := "Warning: Permanently added 'x' (ED25519) to the list of known hosts.\r\n** WARNING: connection is not using a post-quantum key exchange algorithm.\n\nfirst\nsecond\nthird\n"
	if got := lastLines(said, 2); got != "second / third" {
		t.Fatalf("got %q", got)
	}
	if got := lastLines("Warning: Permanently added 'x'.\n", 3); got != "" {
		t.Fatalf("a notice alone is nothing to report: %q", got)
	}
}

// ---- found by the independent review -------------------------------------------

// TestCheckDestination: what may reach ssh's command line. ssh expands
// ${VAR} and %-tokens in the paths of a forwarding, so a socket path is not
// "just a path": /${AWS_SECRET_ACCESS_KEY} would be sent, expanded, to the
// server the URL names.
func TestCheckDestination(t *testing.T) {
	ok := [][4]string{
		{"", "prod", "", "/var/run/docker.sock"},
		{"ops", "prod-1.example.com", "2222", "/run/user/1000/docker.sock"},
		{"deploy.bot+ci@corp", "10.0.0.5", "22", "/home/u/.docker/run/docker.sock"},
		{"", "::1", "", "/tmp/a_b,c=d@e/x.sock"},
		{"", "my_alias", "", "/var/run/docker.sock"},
	}
	for _, d := range ok {
		if err := CheckDestination(d[0], d[1], d[2], d[3]); err != nil {
			t.Errorf("%q: %v", d, err)
		}
	}
	bad := [][4]string{
		{"", "", "", "/s"},
		{"", "-oProxyCommand=id", "", "/s"},
		{"-oX", "prod", "", "/s"},
		{"", "pr od", "", "/s"},
		{"", "$(id)", "", "/s"},
		{"", "a;b", "", "/s"},
		{"", "prod\n", "", "/s"},
		{"us'er", "prod", "", "/s"},
		{"user\x1b[2J", "prod", "", "/s"},
		{"", "prod", "22;id", "/s"},
		{"", "prod", "123456", "/s"},
		{"", "prod", "", "relative/docker.sock"},
		{"", "prod", "", ""},
		{"", "prod", "", "/tmp/${HOME}/x.sock"},
		{"", "prod", "", "/run/user/%i/docker.sock"},
		{"", "prod", "", "/tmp/a:b.sock"},
		{"", "prod", "", `/tmp/a\b.sock`},
		{"", "prod", "", "/tmp/a b.sock"},
		{"", "prod", "", "/tmp/~x.sock"},
		{"", "prod", "", "/tmp/x\n.sock"},
	}
	for _, d := range bad {
		if err := CheckDestination(d[0], d[1], d[2], d[3]); err == nil {
			t.Errorf("%q: accepted", d)
		}
	}
}

// TestUnsafeDestinationNeverReachesSSH: the tunnel checks for itself, and
// before the guard — whose own check reads the path literally and would have
// let "/tmp/${X}" through whatever X held.
func TestUnsafeDestinationNeverReachesSSH(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	for _, sock := range []string{filepath.Dir(srv.Socket()) + "/${DD_SECRET}", "/tmp/%h.sock", srv.Socket() + ":x"} {
		o := f.options(srv)
		o.RemoteSocket = sock
		_, err := Open(context.Background(), o)
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Reason, "cannot be used") {
			t.Errorf("%s: want a refusal, got %v", sock, err)
		}
	}
	if n := len(f.invocations()); n != 0 {
		t.Fatalf("ssh was started %d time(s) with an unsafe destination", n)
	}

	// The same for the local end, which the temporary directory decides.
	tmp, err := os.MkdirTemp("", "dd%t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)
	if _, err := Open(context.Background(), f.options(srv)); err == nil || !strings.Contains(err.Error(), "set TMPDIR") {
		t.Fatalf("a temporary directory ssh would expand: got %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// TestBatchRunsHaveNoTerminal: BatchMode alone does not keep ssh quiet — it
// is not passed on to the ssh started for a jump host, and a ProxyCommand
// can open the terminal by itself. A run that must not ask is therefore
// started in a session of its own, where there is no terminal to ask on, and
// told not to try a graphical prompt instead.
func TestBatchRunsHaveNoTerminal(t *testing.T) {
	f := newFake(t, "ok")
	srv := startEngine(t)
	o := f.options(srv)
	o.Interactive = true
	tun := open(t, o)
	_ = syscall.Kill(f.invocations()[0].PID, syscall.SIGTERM)
	eventually(t, "the tunnel to come back", func() bool { return tun.Restarts() == 1 && tun.Up() })

	inv := f.invocations()
	if first := inv[0]; first.Batch || first.Leader || first.Askpass != "" {
		t.Fatalf("the interactive first connection keeps the terminal, to ask on: %+v", first)
	}
	if again := inv[1]; !again.Batch || !again.Leader || again.Askpass != "never" {
		t.Fatalf("a reconnection must have no terminal and no other way of asking: %+v", again)
	}

	// The probe that Explain sends is non-interactive too.
	t.Setenv("DD_FAKE_SSH_PROBE", "dd-probe:missing")
	tun.Explain(context.Background())
	if probe := f.invocations()[2]; probe.Command == "" || !probe.Batch || !probe.Leader || probe.Askpass != "never" {
		t.Fatalf("the probe: %+v", probe)
	}
}

// TestSSHThatPredatesAnOption: ForkAfterAuthentication is newer than
// everything else on the command line. A client that does not know it is
// run without it — once, and from then on.
func TestSSHThatPredatesAnOption(t *testing.T) {
	f := newFake(t, "oldssh")
	srv := startEngine(t)
	var shown lockedBuffer
	o := f.options(srv)
	o.Stderr = &shown
	tun := open(t, o)
	if err := ping(t, tun.Host()); err != nil {
		t.Fatal(err)
	}
	inv := f.invocations()
	if len(inv) != 2 || !inv[0].StayInFront || inv[1].StayInFront {
		t.Fatalf("want one refused start with the option and one without: %+v", inv)
	}
	// The operator is not shown an error about something that then works.
	if s := shown.String(); s != "" {
		t.Fatalf("the refused option was reported on the terminal: %q", s)
	}
	_ = syscall.Kill(inv[1].PID, syscall.SIGTERM)
	eventually(t, "the tunnel to come back", func() bool { return tun.Restarts() == 1 && tun.Up() })
	if again := f.invocations()[2]; again.StayInFront {
		t.Fatal("a reconnection tried the option again")
	}
}

// TestSSHThatGoesToTheBackground: with -f, or ForkAfterAuthentication in an
// old client's configuration, ssh logs in and its parent exits quietly with
// status 0. That is reported as what it is.
func TestSSHThatGoesToTheBackground(t *testing.T) {
	f := newFake(t, "background")
	srv := startEngine(t)
	_, err := Open(context.Background(), f.options(srv))
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Reason, "went into the background") || !strings.Contains(te.Hint, "ForkAfterAuthentication") {
		t.Fatalf("got %v / %+v", err, te)
	}
}

// TestHintsFitTheFailure: found by the review — a local socket that cannot
// be created says "Permission denied" too, and got the hint for a failed
// login. And the command suggested for trying by hand is the one that was
// run, not a bare ssh.
func TestHintsFitTheFailure(t *testing.T) {
	srv := startEngine(t)
	f := newFake(t, "nobind")
	_, err := Open(context.Background(), f.options(srv))
	var te *Error
	if !errors.As(err, &te) || strings.Contains(te.Hint, "log in") || !strings.Contains(te.Hint, "could not create its local socket") {
		t.Fatalf("a bind failure: %+v", te)
	}

	f = newFake(t, "denied")
	o := f.options(srv)
	o.Command = []string{os.Args[0], "-F", "/my/config"}
	_, err = Open(context.Background(), o)
	if !errors.As(err, &te) || !strings.Contains(te.Hint, "`"+os.Args[0]+" -F /my/config -p 2222 ops@prod`") {
		t.Fatalf("the hint should repeat the command that was run: %+v", te)
	}
}

func TestDownReason(t *testing.T) {
	said := "channel 3: open failed: connect failed: open failed\r\nchannel 4: open failed: connect failed: open failed\nTimeout, server prod not responding.\n"
	if got := downReason(said); got != "Timeout, server prod not responding." {
		t.Fatalf("got %q", got)
	}
	// Refused connections alone are about the daemon, not about the link.
	if got := downReason("channel 3: open failed: connect failed: open failed\n"); got != "the ssh process ended" {
		t.Fatalf("got %q", got)
	}
}
