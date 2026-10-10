package shell

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

type harness struct {
	t    *testing.T
	srv  *fakeengine.Server
	eng  engine.Engine
	ptmx *os.File // the "keyboard and screen" side
	tty  *os.File // the terminal the session is given

	mu  sync.Mutex
	out bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := fakeengine.New()
	srv.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	srv.AddContainer(fakeengine.ContainerSpec{Name: "bare", Image: "distroless:1", NoShell: true})
	srv.AddContainer(fakeengine.ContainerSpec{Name: "off", Image: "app:1", State: "exited"})
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 33, Cols: 111}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, srv: srv, eng: eng, ptmx: ptmx, tty: tty}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				h.mu.Lock()
				h.out.Write(buf[:n])
				h.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return h
}

func (h *harness) screen() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.out.String()
}

func (h *harness) waitFor(what, sub string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.screen(), sub) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s (%q) in:\n%q", what, sub, h.screen())
}

func (h *harness) typeKeys(s string) {
	h.t.Helper()
	if _, err := io.WriteString(h.ptmx, s); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) termios() unix.Termios {
	h.t.Helper()
	tio, err := unix.IoctlGetTermios(int(h.tty.Fd()), unix.TCGETS)
	if err != nil {
		h.t.Fatal(err)
	}
	return *tio
}

func (h *harness) start(container string, cmd []string) (*Session, chan error) {
	s := New(h.eng, h.srv.ContainerID(container), container, cmd)
	s.SetStdin(h.tty)
	s.SetStdout(h.tty)
	s.SetStderr(h.tty)
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	return s, done
}

func (h *harness) finish(done chan error) error {
	h.t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatalf("session did not end; screen:\n%q", h.screen())
		return nil
	}
}

func TestShellSessionRoundTripAndTerminalRestore(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	if before.Lflag&unix.ECHO == 0 || before.Lflag&unix.ICANON == 0 {
		t.Fatal("test terminal should start in cooked mode")
	}

	s, done := h.start("app", DefaultCommand)
	h.waitFor("banner", "docker-desk: sh in app")
	h.waitFor("prompt", ":/# ")

	// While the shell runs the terminal is raw: no local echo, no line mode.
	during := h.termios()
	if during.Lflag&unix.ECHO != 0 || during.Lflag&unix.ICANON != 0 || during.Lflag&unix.ISIG != 0 {
		t.Fatalf("terminal is not raw during the session: lflag=%#x", during.Lflag)
	}

	// Keys reach the process; its output comes back.
	h.typeKeys("echo round-trip-ok\r")
	h.waitFor("command output", "\r\nround-trip-ok\r\n")

	// The container was told the real size of the terminal.
	h.typeKeys("stty size\r")
	h.waitFor("initial size", "\r\n33 111\r\n")

	// A resize is forwarded.
	if err := pty.Setsize(h.ptmx, &pty.Winsize{Rows: 48, Cols: 160}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.typeKeys("stty size\r")
		time.Sleep(30 * time.Millisecond)
		if strings.Contains(h.screen(), "\r\n48 160\r\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resize was not propagated; screen:\n%q", h.screen())
		}
	}

	// Ctrl-C is a byte for the remote shell, not a signal for the desk.
	h.typeKeys("\x03")
	h.waitFor("ctrl-c reached the shell", "^C")

	h.typeKeys("exit 3\r")
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !s.Result.Started || s.Result.ExitCode != 3 || s.Result.Detached {
		t.Fatalf("result: %+v", s.Result)
	}

	// The terminal is exactly as it was.
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal modes were not restored:\nbefore %+v\nafter  %+v", before, after)
	}

	// The session's keyboard reader is gone: a key typed now is still there
	// for whoever reads the terminal next (the desk).
	h.typeKeys("x\n")
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := h.tty.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case v := <-got:
		if v != "x\n" {
			t.Fatalf("next reader got %q, want %q", v, "x\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a leftover reader swallowed the key typed after the session")
	}
}

func TestShellEndsWhenContainerDies(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	s, done := h.start("app", DefaultCommand)
	h.waitFor("prompt", ":/# ")
	h.srv.Crash("app", 137)
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !s.Result.Started || s.Result.ExitCode != 137 {
		t.Fatalf("result: %+v", s.Result)
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes were not restored after the container died")
	}
}

func TestShellThatCannotStart(t *testing.T) {
	h := newHarness(t)
	before := h.termios()

	// No shell in the image: the daemon accepts the exec, reports the
	// failure in the stream and through exit code 127.
	s, done := h.start("bare", DefaultCommand)
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s.Result.ExitCode != 127 || !strings.Contains(s.Result.Tail, "executable file not found") {
		t.Fatalf("result: %+v", s.Result)
	}

	// Container not running: the daemon refuses before anything starts.
	s2, done2 := h.start("off", DefaultCommand)
	err := h.finish(done2)
	if !engine.IsConflict(err) || !strings.Contains(err.Error(), "is not running") || s2.Result.Started {
		t.Fatalf("exec in a stopped container: err=%v result=%+v", err, s2.Result)
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes changed by a session that never started")
	}
}

func TestShellEndsWhenDaemonGoesAway(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	s, done := h.start("app", DefaultCommand)
	h.waitFor("prompt", ":/# ")
	h.srv.Stop()
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !s.Result.Started {
		t.Fatalf("result: %+v", s.Result)
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes were not restored after the daemon went away")
	}
}

func TestShellReadOnlyNeverReachesTheDaemon(t *testing.T) {
	h := newHarness(t)
	ro := engine.ReadOnly(h.eng)
	h.srv.ResetRequests()
	s := New(ro, h.srv.ContainerID("app"), "app", DefaultCommand)
	s.SetStdin(h.tty)
	s.SetStdout(h.tty)
	if err := s.Run(); err != engine.ErrReadOnly {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
	if n := len(h.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating requests in read-only mode", n)
	}
}

// TestShellLocalEscape: the way out when the daemon has stopped answering
// but the connection is still open — Ctrl-] three times, handled locally.
func TestShellLocalEscape(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	s, done := h.start("app", DefaultCommand)
	h.waitFor("banner", "Ctrl-] three times forces it")
	h.waitFor("prompt", ":/# ")

	// One or two are just bytes for the remote program.
	h.typeKeys("\x1d\x1d")
	h.typeKeys("echo still-here\r")
	h.waitFor("session survives two", "still-here")

	h.typeKeys("\x1d\x1d\x1d")
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !s.Result.Forced || !s.Result.Detached {
		t.Fatalf("result: %+v", s.Result)
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes were not restored after the local escape")
	}
}

// TestShellEndsOnSIGTERM: Bubble Tea ignores signals while the terminal is
// handed over; the session itself must turn a termination request into a
// clean end, or the only way out would leave the terminal raw.
func TestShellEndsOnSIGTERM(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	_, done := h.start("app", DefaultCommand)
	h.waitFor("prompt", ":/# ")
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := h.finish(done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes were not restored after SIGTERM")
	}
}

// TestShellStartCannotHang: a daemon that accepts the request and never
// answers must not leave the operator at a dead screen.
func TestShellStartCannotHang(t *testing.T) {
	h := newHarness(t)
	before := h.termios()
	old := startTimeout
	startTimeout = 200 * time.Millisecond
	defer func() { startTimeout = old }()
	h.srv.Stall("POST", `/exec$`, 5*time.Second)

	begin := time.Now()
	s, done := h.start("app", DefaultCommand)
	err := h.finish(done)
	if err == nil || !strings.Contains(err.Error(), "did not start the process within") {
		t.Fatalf("want a start timeout, got %v", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("giving up took %s", d)
	}
	if s.Result.Started {
		t.Fatal("the session claims to have started")
	}
	if after := h.termios(); !reflect.DeepEqual(before, after) {
		t.Fatal("terminal modes changed by a session that never started")
	}
}

// TestShellResetsTerminalModes: what a full-screen program inside the
// container switched on is switched off when the session ends.
func TestShellResetsTerminalModes(t *testing.T) {
	h := newHarness(t)
	_, done := h.start("app", DefaultCommand)
	h.waitFor("prompt", ":/# ")
	h.srv.Crash("app", 137) // the program had no chance to clean up
	if err := h.finish(done); err != nil {
		t.Fatal(err)
	}
	h.waitFor("mouse reporting off", "\x1b[?1000l")
	h.waitFor("alternate screen left", "\x1b[?1049l")
	h.waitFor("cursor shown", "\x1b[?25h")
}
