package e2e

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
	"github.com/fardani235/docker-desk/internal/sshtest"
)

// The real binary, on a pseudo-terminal, against a daemon that is not a
// local socket: plain TCP, TLS with client certificates, and SSH — the last
// with the real ssh client and a real sshd. The daemon at the far end is
// always the fake.

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newFake(seed func(*fakeengine.Server)) *fakeengine.Server {
	srv := fakeengine.New()
	srv.StatsInterval = 100 * time.Millisecond
	srv.PullStepDelay = 15 * time.Millisecond
	srv.SeedDemo()
	srv.Name = "far-away"
	if seed != nil {
		seed(srv)
	}
	return srv
}

func launchEnv(t *testing.T, srv *fakeengine.Server, rows, cols int, env []string, args ...string) *desk {
	t.Helper()
	term, err := ptyscreen.Start(rows, cols, env, binary, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return &desk{t: t, srv: srv, term: term}
}

// stopAndWatch stops shop-web-1 through its confirmation and waits for the
// engine's answer in the Events pane: one whole act-and-answer round trip.
func (d *desk) stopAndWatch(shot string, daemon ...string) {
	d.t.Helper()
	d.keys("2", "/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
	d.see("2 Containers (1 of 12)")
	d.srv.ResetRequests()
	d.keys("t")
	d.see(append([]string{"Stop container", "shop-web-1"}, daemon...)...)
	if shot != "" {
		d.shot(shot)
	}
	if n := len(d.srv.Mutations()); n != 0 {
		d.t.Fatalf("the action key alone sent %d mutating request(s)", n)
	}
	d.consent("y")
	d.see("ctr die exit 0 shop-web-1", "✓ stop container shop-web-1", "○ exited")
	if st, _ := d.srv.ContainerState("shop-web-1"); st != "exited" {
		d.t.Fatalf("shop-web-1 is %s, want exited", st)
	}
}

// shellRoundTrip opens a shell in shop-api-1 — the hijacked connection, the
// one a transport is most likely to get wrong — types into it and leaves.
func (d *desk) shellRoundTrip() {
	d.t.Helper()
	P := ptyscreen.Enter
	d.keys(ptyscreen.Esc, "/", "s", "h", "o", "p", "-", "a", "p", "i", P)
	d.see("3 Details: shop-api-1")
	d.keys("e")
	d.see("Open a shell", "shop-api-1")
	d.consent(P)
	d.see("docker-desk: sh in shop-api-1", ":/#")
	d.keys("e", "c", "h", "o", " ", "f", "a", "r", "-", "o", "k", P)
	d.see("far-ok\n")
	d.keys("e", "x", "i", "t", P)
	d.see("Back from the shell in shop-api-1", "4 Events")
	d.keys(ptyscreen.Esc)
	d.see("2 Containers (12)")
}

func TestRemoteOverTCP(t *testing.T) {
	srv := newFake(nil)
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	d := launchEnv(t, srv, 40, 150, nil, "--host", srv.Host())
	// A loopback address is not called remote; that it is not encrypted is
	// said all the same.
	d.see("Docker Desk", srv.Host(), "[tcp, NO TLS]", "on far-away", "● connected", "2 Containers (12)", "shop-web-1")
	d.see("%") // statistics streams are running over TCP
	d.stopAndWatch("19-remote-tcp-confirm", srv.Host()+"  (context default)  [tcp, NO TLS]")
	d.shellRoundTrip()

	// The daemon goes away and comes back, as over a socket.
	srv.Stop()
	d.see("DISCONNECTED", "Lost the connection to the Docker daemon at "+srv.Host())
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	d.see("● connected", "event stream resumed")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestRemoteOverTLS(t *testing.T) {
	srv := newFake(nil)
	certs := t.TempDir()
	if err := srv.StartTLS(certs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	env := []string{"DOCKER_HOST=" + srv.Host(), "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=" + certs}
	d := launchEnv(t, srv, 40, 150, env)
	d.see("Docker Desk", srv.Host(), "[tls]", "on far-away", "● connected", "2 Containers (12)")
	d.see("%")
	d.stopAndWatch("", srv.Host()+"  (context default)  [tls]")
	d.shellRoundTrip()
	d.keys("?")
	d.see("reached over: TCP to " + strings.TrimPrefix(srv.Host(), "tcp://") + " with TLS: daemon verified")
	d.keys(ptyscreen.Esc, "q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Without the certificates the same daemon is refused before anything
	// is drawn, with the reason.
	term, err := ptyscreen.Start(30, 120, []string{"DOCKER_HOST=" + srv.Host()}, binary)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if code, err := term.Wait(wait); err != nil || code != 1 {
		t.Fatalf("exit %d, %v", code, err)
	}
	time.Sleep(50 * time.Millisecond)
	raw := term.Raw()
	if strings.Contains(raw, "\x1b[?1049h") {
		t.Fatal("the workspace was opened on a daemon that could not be reached")
	}
	for _, want := range []string{"expects TLS, and none is configured", "The workspace was not opened"} {
		if !strings.Contains(raw, want) {
			t.Errorf("message does not contain %q:\n%s", want, raw)
		}
	}
}

// sshProcesses lists running ssh clients that use this test server's
// configuration file.
func sshProcesses(sshd *sshtest.Server) string {
	out, _ := exec.Command("pgrep", "-f", "--", "-F "+sshd.Command()[2]).Output()
	return strings.TrimSpace(string(out))
}

func TestRemoteOverSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	url := sshd.URL(srv.Socket())
	// A temporary directory of its own, so that what the program leaves
	// behind can be told from other tests' tunnels.
	tmp, err := os.MkdirTemp("", "dde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	env := []string{"DOCKER_HOST=" + url, "DOCKER_DESK_SSH=" + sshd.CommandString(), "TMPDIR=" + tmp}
	d := launchEnv(t, srv, 42, 170, env)
	d.see("Docker Desk", url, "[ssh]", "on far-away", "● connected", "2 Containers (12)", "shop-web-1")
	// Said on the ordinary screen, before the desk took the terminal.
	if raw := d.term.Raw(); !strings.Contains(raw[:strings.Index(raw, "\x1b[?1049h")], "opening an SSH tunnel to "+srv.Socket()) {
		t.Errorf("the launch should say that it is opening a tunnel:\n%q", lastBytes(raw, 300))
	}
	d.see("%") // a statistics stream per running container, all in one SSH connection
	if n := len(strings.Fields(sshProcesses(sshd))); n != 1 {
		t.Fatalf("the desk should run exactly one ssh, found %d", n)
	}
	d.shot("20-remote-ssh")

	// Logs follow, a mutation and its events, and the shell — through ssh.
	d.keys("]")
	d.see("[Logs]", "● following")
	d.keys("[")
	// The endpoint names the socket and is long: the context and the tag
	// are on the line below it, never cut off.
	d.stopAndWatch("21-remote-ssh-confirm", "daemon     "+url, "(context default)", "[ssh]")
	d.shellRoundTrip()

	// The remote machine drops off the network: the header says so, the
	// tunnel says why, the desk keeps answering keys.
	sshd.Stop()
	d.see("DISCONNECTED", "The ssh link to "+url+" is down — retrying")
	d.keys("1", ptyscreen.Down)
	d.see("2 Images", "nginx")
	d.shot("22-ssh-link-down")

	// And comes back: the desk reconnects by itself, without a question.
	if err := sshd.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := d.term.WaitFor(30*time.Second, "● connected", "event stream resumed"); err != nil {
		t.Fatal(err)
	}
	d.keys(ptyscreen.Up, "2")
	d.see("2 Containers (12)")

	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	// Nothing is left behind: no ssh process, no socket, no directory.
	for deadline := time.Now().Add(3 * time.Second); sshProcesses(sshd) != ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("ssh is still running after the desk quit: %s", sshProcesses(sshd))
		}
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "docker-desk-ssh-*")); len(left) != 0 {
		t.Fatalf("the tunnel's directory was left behind: %v", left)
	}
	// Nothing the session did was forced.
	for _, r := range srv.Requests() {
		if strings.Contains(strings.ToLower(r.Query), "force") {
			t.Fatalf("a forced request was sent: %s", r)
		}
	}
}

// TestSSHAsksOnTheTerminalBeforeTheDesk: a key with a passphrase (or a
// password, or an unknown host key) means ssh has a question. It is asked on
// the ordinary screen, before the desk takes the terminal — and never again
// afterwards: a reconnection that would need the answer fails, and says so,
// instead of prompting underneath a full-screen program.
func TestSSHAsksOnTheTerminalBeforeTheDesk(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{KeyPassphrase: "correct horse"})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	url := sshd.URL(srv.Socket())

	d := launchEnv(t, srv, 40, 170, []string{"DOCKER_HOST=" + url, "DOCKER_DESK_SSH=" + sshd.CommandString()})
	d.see("opening an SSH tunnel", "Enter passphrase for key")
	if d.term.AltScreen() {
		t.Fatal("the desk took the terminal while ssh was still asking")
	}
	if err := d.term.Send("correct horse\r"); err != nil {
		t.Fatal(err)
	}
	d.see("Docker Desk", url, "[ssh]", "● connected", "2 Containers (12)")
	if !d.term.AltScreen() {
		t.Fatal("the desk did not open after the passphrase was given")
	}

	// The link drops. Reconnecting would need the passphrase again; the
	// desk must not ask — it says why it cannot, and stays usable.
	sshd.Stop()
	d.see("DISCONNECTED")
	if err := sshd.Restart(); err != nil {
		t.Fatal(err)
	}
	d.see("is down — retrying", "Permission denied")
	if strings.Contains(d.term.Screen(), "Enter passphrase") {
		t.Fatal("ssh asked for the passphrase underneath the desk")
	}
	d.keys("1", ptyscreen.Down)
	d.see("2 Images", "nginx")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if strings.Count(d.term.Raw(), "Enter passphrase") != 1 {
		t.Fatalf("the passphrase should have been asked for exactly once, on the ordinary screen")
	}
}

// TestKilledDeskTakesItsSSHAlong: a desk that is killed outright cannot
// clean up, and an ssh left behind would keep an authenticated tunnel to the
// remote daemon open. On Linux the kernel is asked to end ssh when the desk
// dies.
//
// The desk is started by a shell that outlives it, as it is in real use:
// were the desk the terminal's session leader — as it is when the harness
// starts it directly — its death would hang up the terminal, and that, not
// the mechanism under test, would end ssh.
func TestKilledDeskTakesItsSSHAlong(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tying a child's life to its parent's is a Linux facility; elsewhere this is a documented limit")
	}
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	tmp, err := os.MkdirTemp("", "dde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	env := []string{"DOCKER_HOST=" + sshd.URL(srv.Socket()), "DOCKER_DESK_SSH=" + sshd.CommandString(), "TMPDIR=" + tmp}
	term, err := ptyscreen.Start(40, 170, env, "/bin/sh", "-c", binary+"; echo desk-ended-with-$?; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	if err := term.WaitFor(wait, "● connected", "2 Containers (12)"); err != nil {
		t.Fatal(err)
	}
	ssh := sshProcesses(sshd)
	if len(strings.Fields(ssh)) != 1 {
		t.Fatalf("expected exactly one ssh process, found %q", ssh)
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", ssh).Output()
	if err != nil {
		t.Fatal(err)
	}
	deskPID := strings.TrimSpace(string(out))
	if err := exec.Command("kill", "-9", deskPID).Run(); err != nil {
		t.Fatal(err)
	}
	// 137 = killed by signal 9; the shell is still there, holding the terminal.
	if err := term.WaitFor(wait, "desk-ended-with-137"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); sshProcesses(sshd) != ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("ssh (pid %s) outlived the desk that was killed", sshProcesses(sshd))
		}
	}
}

// TestJumpHostNeverAsksUnderTheDesk: found by the review, which reproduced
// it. The target is reached through a jump host, and logging in to either
// needs a passphrase. Before the desk opens, both are asked for. After the
// link drops, the reconnection runs ssh in batch mode — but ssh does not pass
// that on to the ssh it starts for the jump host, which then asked for the
// passphrase on the terminal the desk was drawn on, and read the keys typed
// for the desk. A reconnection now has no terminal to ask on.
func TestJumpHostNeverAsksUnderTheDesk(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{KeyPassphrase: "correct horse", ViaJumpHost: true})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	url := sshd.JumpURL(srv.Socket())

	d := launchEnv(t, srv, 40, 170, []string{"DOCKER_HOST=" + url, "DOCKER_DESK_SSH=" + sshd.CommandString()})
	prompts := func() int { return strings.Count(d.term.Raw(), "Enter passphrase") }
	answer := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(wait); prompts() < n; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("prompt %d never came; screen:\n%s", n, d.term.Screen())
			}
		}
		if d.term.AltScreen() {
			t.Fatal("the desk took the terminal while ssh was still asking")
		}
		if err := d.term.Send("correct horse\r"); err != nil {
			t.Fatal(err)
		}
	}
	answer(1) // the jump host
	answer(2) // the target, through it
	d.see("Docker Desk", url, "[REMOTE ssh]", "● connected", "2 Containers (12)")

	sshd.Stop()
	d.see("DISCONNECTED")
	if err := sshd.Restart(); err != nil {
		t.Fatal(err)
	}
	// Several reconnection attempts' worth of time.
	d.see("is down — retrying")
	time.Sleep(4 * time.Second)
	if n := prompts(); n != 2 {
		t.Fatalf("ssh asked %d more time(s) after the desk had opened; screen:\n%s", n-2, d.term.Screen())
	}
	// And the keys are the desk's: it navigates, and it quits.
	d.keys("1", ptyscreen.Down)
	d.see("2 Images", "nginx")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestHangUpCleansUp: closing the terminal window sends SIGHUP — an ordinary
// way to leave. The desk then ends as if it had been quit: no ssh left, and
// no socket or directory in the temporary directory (the review found them
// left behind).
func TestHangUpCleansUp(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	tmp, err := os.MkdirTemp("", "dde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	env := []string{"DOCKER_HOST=" + sshd.URL(srv.Socket()), "DOCKER_DESK_SSH=" + sshd.CommandString(), "TMPDIR=" + tmp}
	term, err := ptyscreen.Start(40, 170, env, "/bin/sh", "-c", binary+"; echo desk-ended-with-$?; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	if err := term.WaitFor(wait, "● connected", "2 Containers (12)"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", sshProcesses(sshd)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-HUP", strings.TrimSpace(string(out))).Run(); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitFor(wait, "desk-ended-with-0"); err != nil {
		t.Fatal(err)
	}
	if term.AltScreen() {
		t.Fatal("the alternate screen was not left")
	}
	if left := sshProcesses(sshd); left != "" {
		t.Fatalf("ssh is still running: %s", left)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) != 0 {
		t.Fatalf("left in the temporary directory: %v", left)
	}
}

// TestSSHFailuresDoNotOpenTheWorkspace: each way the tunnel can fail is
// explained on the ordinary screen.
func TestSSHFailuresDoNotOpenTheWorkspace(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	srv := newFake(nil)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	refused := sshtest.Start(t, sshtest.Options{NoSocketForwarding: true})

	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{"no socket on the far side",
			[]string{"DOCKER_HOST=" + sshd.URL("/tmp/docker-desk-e2e-no-such.sock"), "DOCKER_DESK_SSH=" + sshd.CommandString()},
			[]string{"the Docker daemon is not reachable", "there is no socket at /tmp/docker-desk-e2e-no-such.sock on 127.0.0.1", "rootless"}},
		{"the server forbids socket forwarding",
			[]string{"DOCKER_HOST=" + refused.URL(srv.Socket()), "DOCKER_DESK_SSH=" + refused.CommandString()},
			[]string{srv.Socket() + " on 127.0.0.1 is there", "yet the SSH server could not connect to it", "AllowStreamLocalForwarding"}},
		{"nothing answers on the SSH port",
			[]string{"DOCKER_HOST=ssh://" + closedPort(t) + srv.Socket(), "DOCKER_DESK_SSH=" + sshd.CommandString()},
			[]string{"Connection refused", "works on its own"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, err := ptyscreen.Start(30, 200, tc.env, binary)
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			code, err := term.Wait(30 * time.Second)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			raw := term.Raw()
			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if strings.Contains(raw, "\x1b[?1049h") || term.AltScreen() {
				t.Error("the full-screen workspace was opened")
			}
			for _, w := range append(tc.want, "The workspace was not opened") {
				if !strings.Contains(raw, w) {
					t.Errorf("message does not contain %q:\n%s", w, raw)
				}
			}
		})
	}
}

// TestCtrlCWhileSSHConnects: until the desk owns the terminal, Ctrl+C ends
// the launch and the ssh it started — which may be sitting at a password
// prompt — instead of leaving either behind.
func TestCtrlCWhileSSHConnects(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "slow-ssh")
	// An "ssh" that never gets anywhere, like one waiting to be answered.
	// It ignores the interrupt the terminal sends to everything in the
	// foreground, so that only the desk ending it can end it — otherwise
	// this test passes whether the desk does or not (the review found that).
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap '' INT\necho $$ > "+pidFile+"\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	term, err := ptyscreen.Start(30, 120, []string{"DOCKER_HOST=ssh://ops@far-away.invalid", "DOCKER_DESK_SSH=" + script}, binary)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.WaitFor(wait, "opening an SSH tunnel to /var/run/docker.sock (ssh ops@far-away.invalid)"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(wait); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ssh stand-in was never started")
		}
	}
	if err := term.Send("\x03"); err != nil {
		t.Fatal(err)
	}
	code, err := term.Wait(wait)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	raw := term.Raw()
	if code != 130 || !strings.Contains(raw, "interrupted. The workspace was not opened.") || strings.Contains(raw, "\x1b[?1049h") {
		t.Fatalf("exit %d:\n%s", code, raw)
	}
	pid, _ := os.ReadFile(pidFile)
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err := exec.Command("kill", "-0", strings.TrimSpace(string(pid))).Run(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ssh stand-in (pid %s) outlived the interrupted launch", strings.TrimSpace(string(pid)))
		}
	}
}

// TestThemes: the palette comes from --theme or DOCKER_DESK_THEME, T cycles
// it while the desk is open, and the colours on the wire are the theme's.
func TestThemes(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	const (
		darkAccent    = "\x1b[1;38;5;39m"  // the original palette: 256-colour blue, bold — a focused border
		lightAccent   = "\x1b[1;38;5;26m"  // the darker blue of the light palette
		reverseVideo  = "\x1b[1;7m"        // a highlighted row in a theme without a selection colour
		draculaAccent = "38;2;189;147;249" // #bd93f9
		draculaSelect = ";48;2;68;71;"     // #44475a behind the highlighted row (the last digit is rounded)
		nordAccent    = "38;2;136;192;208" // #88c0d0
		firstPane     = "┏━ ◆ 2 Containers (12)"
	)
	host := "DOCKER_HOST=" + srv.Host()

	t.Run("no setting: the original palette", func(t *testing.T) {
		d := launchEnv(t, srv, 40, 140, []string{host})
		d.see(firstPane, "shop-web-1")
		d.keys("q")
		d.quit()
		raw := d.term.Raw()
		if !strings.Contains(raw, darkAccent) || !strings.Contains(raw, reverseVideo) || strings.Contains(raw, "38;2;") {
			t.Fatalf("the default look changed: accent %v, reverse-video highlight %v, 24-bit colour %v",
				strings.Contains(raw, darkAccent), strings.Contains(raw, reverseVideo), strings.Contains(raw, "38;2;"))
		}
	})

	t.Run("--theme, then T", func(t *testing.T) {
		d := launchEnv(t, srv, 40, 140, []string{host, "COLORTERM=truecolor"}, "--theme", "dracula")
		d.see(firstPane, "shop-web-1")
		if raw := d.term.Raw(); !strings.Contains(raw, draculaAccent) || !strings.Contains(raw, draculaSelect) || strings.Contains(raw, nordAccent) {
			t.Fatal("--theme dracula should draw in Dracula's colours, with its selection colour behind the highlighted row")
		}
		d.keys("T")
		d.see("Theme: nord", "--theme nord", "DOCKER_DESK_THEME=nord")
		d.shot("23-theme-switched")
		if raw := d.term.Raw(); !strings.Contains(raw, nordAccent) {
			t.Fatal("after T the desk should be redrawn in Nord's colours")
		}
		d.keys("?")
		d.see("next colour theme (nord now)")
		d.keys(ptyscreen.Esc, "q")
		if code := d.quit(); code != 0 {
			t.Fatalf("exit code %d", code)
		}
	})

	t.Run("DOCKER_DESK_THEME", func(t *testing.T) {
		d := launchEnv(t, srv, 40, 140, []string{host, "DOCKER_DESK_THEME=light"})
		d.see(firstPane)
		d.keys("q")
		d.quit()
		if raw := d.term.Raw(); !strings.Contains(raw, lightAccent) || strings.Contains(raw, darkAccent) {
			t.Fatal("DOCKER_DESK_THEME=light should draw in the light palette")
		}
	})

	t.Run("an unknown theme is refused before anything is drawn", func(t *testing.T) {
		term, err := ptyscreen.Start(30, 120, []string{host}, binary, "--theme", "neon")
		if err != nil {
			t.Fatal(err)
		}
		defer term.Close()
		code, err := term.Wait(wait)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if raw := term.Raw(); code != 2 || !strings.Contains(raw, `unknown theme "neon"`) || strings.Contains(raw, "\x1b[?1049h") {
			t.Fatalf("exit %d:\n%s", code, raw)
		}
	})
}
