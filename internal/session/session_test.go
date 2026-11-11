package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
)

// The test binary doubles as ssh (as in internal/sshtunnel): with
// DD_SESSION_FAKE_SSH set it records how it was started and then forwards
// its local socket to the "remote" one on this machine — or fails the way a
// key with a passphrase makes ssh fail.
func TestMain(m *testing.M) {
	if mode := os.Getenv("DD_SESSION_FAKE_SSH"); mode != "" {
		fakeSSH(mode)
		return
	}
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

type invocation struct {
	PID    int
	Batch  bool
	Leader bool // started without a controlling terminal
	TTY    bool // could open /dev/tty
	Local  string
	Remote string
}

func fakeSSH(mode string) {
	args := os.Args[1:]
	inv := invocation{PID: os.Getpid()}
	if sid, err := unix.Getsid(0); err == nil {
		inv.Leader = sid == os.Getpid()
	}
	if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		inv.TTY = true
		f.Close()
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" && i+1 < len(args):
			i++
			if args[i] == "BatchMode=yes" {
				inv.Batch = true
			}
		case args[i] == "-L" && i+1 < len(args):
			i++
			inv.Local, inv.Remote, _ = strings.Cut(args[i], ":")
		}
	}
	if path := os.Getenv("DD_SESSION_FAKE_SSH_LOG"); path != "" {
		line, _ := json.Marshal(inv)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(append(line, '\n'))
			f.Close()
		}
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	if mode == "passphrase" {
		if !inv.Batch {
			// What ssh does when it may ask: open the terminal and wait.
			if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
				fmt.Fprint(f, "Enter passphrase for key '/home/ops/.ssh/id_ed25519': ")
				f.Close()
			}
			<-term
			os.Exit(255)
		}
		// In batch mode a key that needs its passphrase is skipped.
		fmt.Fprintln(os.Stderr, "ops@prod: Permission denied (publickey).")
		os.Exit(255)
	}
	_ = os.Remove(inv.Local)
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
					return
				}
				defer r.Close()
				go func() { _, _ = io.Copy(r, c); _ = r.(*net.UnixConn).CloseWrite() }()
				_, _ = io.Copy(c, r)
			}()
		}
	}()
	<-term
	os.Exit(255)
}

// stubSSH makes Connect start this binary as ssh, in mode.
func stubSSH(t *testing.T, mode string) (cmd []string, invocations func() []invocation) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "invocations")
	t.Setenv("DD_SESSION_FAKE_SSH", mode)
	t.Setenv("DD_SESSION_FAKE_SSH_LOG", log)
	return []string{os.Args[0]}, func() []invocation {
		data, _ := os.ReadFile(log)
		var out []invocation
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var inv invocation
			if err := json.Unmarshal([]byte(line), &inv); err != nil {
				t.Fatalf("bad invocation %q: %v", line, err)
			}
			out = append(out, inv)
		}
		return out
	}
}

func startFake(t *testing.T, name string, containers ...string) *fakeengine.Server {
	t.Helper()
	srv := fakeengine.New()
	srv.Name = name
	srv.StatsInterval = 15 * time.Millisecond
	for _, c := range containers {
		srv.AddContainer(fakeengine.ContainerSpec{Name: c, Image: "app:1"})
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fastHubs(h *live.Hub, _ dockerctx.Link) {
	h.MinBackoff, h.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
}

func newSession(t *testing.T, cfg Config) *Session {
	t.Helper()
	cfg.SetupHub = fastHubs
	if cfg.RetryMin == 0 {
		cfg.RetryMin, cfg.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	}
	cfg.NoticeGap = time.Nanosecond
	s := New(cfg)
	t.Cleanup(s.Close)
	return s
}

func endpoint(host, context string) dockerctx.Endpoint {
	return dockerctx.Endpoint{Host: host, Context: context, Source: "test"}
}

// launch adds srv as the launch member, the way the program does.
func launch(t *testing.T, s *Session, srv *fakeengine.Server, readOnly bool) *Member {
	t.Helper()
	eng, err := engine.New(engine.Options{Host: srv.Host(), ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	m := s.AddLaunch(endpoint(srv.Host(), "default"), eng, engine.Info{Name: srv.Name}, nil, func() { _ = eng.Close() })
	s.Start()
	waitFor(t, "launch member connected", func() bool { return m.State() == Connected && srv.EventSubscribers() == 1 })
	return m
}

func add(t *testing.T, s *Session, ep dockerctx.Endpoint) *Member {
	t.Helper()
	m, existing, err := s.Add(ep)
	if err != nil || existing {
		t.Fatalf("Add(%s): existing=%v err=%v", ep.Host, existing, err)
	}
	return m
}

func connected(t *testing.T, m *Member) {
	t.Helper()
	waitFor(t, m.Name()+" connected", func() bool { return m.State() == Connected })
}

// ---- 1.1 the member list and focus -----------------------------------------------

func checkFocus(t *testing.T, s *Session) {
	t.Helper()
	f := s.Focused()
	if f == nil {
		return
	}
	for _, m := range s.Members() {
		if m == f {
			return
		}
	}
	t.Fatalf("focus is on %s, which is not a member", f.Name())
}

func TestFocusNeverPointsAtAMissingMember(t *testing.T) {
	a, b, c := startFake(t, "a"), startFake(t, "b"), startFake(t, "c")
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	if s.Focused() != ma {
		t.Fatal("the launch member is focused")
	}
	mb := add(t, s, endpoint(b.Host(), "b"))
	mc := add(t, s, endpoint(c.Host(), "c"))
	down := add(t, s, endpoint("unix://"+filepath.Join(t.TempDir(), "nothing.sock"), "down"))
	connected(t, mb)
	connected(t, mc)
	waitFor(t, "down to be disconnected", func() bool { return down.State() == Disconnected })
	checkFocus(t, s)
	if s.Focused() != ma {
		t.Fatal("adding a member moved focus")
	}

	// Focus moves in order, wrapping, and passes over the one that is down.
	var order []string
	for i := 0; i < 4; i++ {
		m, err := s.Next(1)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, m.Name())
		checkFocus(t, s)
	}
	if got := strings.Join(order, " "); got != "b c "+ma.Name()+" b" {
		t.Fatalf("focus went %s", got)
	}
	if m, _ := s.Next(-1); m != ma {
		t.Fatalf("Next(-1) went to %v", m.Name())
	}
	if _, err := s.Focus(down.ID()); !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("focusing a member that is down: %v", err)
	}
	if s.Focused() != ma {
		t.Fatal("a refused focus moved focus")
	}

	// Removing everything, in an order that removes the focused one more
	// than once; focus is always a member, or nothing.
	for _, m := range []*Member{mb, ma, down, mc} {
		if _, err := s.Focus(mc.ID()); err != nil && m != mc {
			t.Fatal(err)
		}
		if _, err := s.Remove(m.ID()); err != nil {
			t.Fatal(err)
		}
		checkFocus(t, s)
		if _, err := s.Remove(m.ID()); !errors.Is(err, ErrNoMember) {
			t.Fatalf("removing twice: %v", err)
		}
	}
	if s.Focused() != nil || s.Len() != 0 {
		t.Fatalf("empty session: focus %v, %d members", s.Focused(), s.Len())
	}
	if _, err := s.Next(1); !errors.Is(err, ErrNoMember) {
		t.Fatalf("Next on an empty session: %v", err)
	}
}

func TestRemovingTheFocusedMemberMovesFocusToAConnectedOne(t *testing.T) {
	a, c := startFake(t, "a"), startFake(t, "c")
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	down := add(t, s, endpoint("unix://"+filepath.Join(t.TempDir(), "nothing.sock"), "down"))
	mc := add(t, s, endpoint(c.Host(), "c"))
	connected(t, mc)
	waitFor(t, "down", func() bool { return down.State() == Disconnected })

	// The member after the focused one is down: focus skips it.
	f, err := s.Remove(ma.ID())
	if err != nil {
		t.Fatal(err)
	}
	if f != mc || s.Focused() != mc {
		t.Fatalf("focus moved to %v, want c", f)
	}
	if mc.Hub().Mode() != live.ModeFull {
		t.Fatal("the newly focused member's hub is not streaming in full")
	}
	// The removed member let go of its daemon.
	<-ma.Closed()
	waitFor(t, "a's event stream closed", func() bool { return a.EventSubscribers() == 0 })

	// With only a member that never answered left, nothing is focused.
	if f, err := s.Remove(mc.ID()); err != nil || f != nil {
		t.Fatalf("after removing c: focus %v err %v", f, err)
	}
	if s.Focused() != nil || s.Len() != 1 {
		t.Fatal("focus on a member with no engine")
	}
}

// A member that answers while nothing is focused takes focus.
func TestFirstMemberToAnswerTakesFocus(t *testing.T) {
	b := startFake(t, "b")
	s := newSession(t, Config{})
	s.Start()
	mb := add(t, s, endpoint(b.Host(), "b"))
	connected(t, mb)
	if s.Focused() != mb || mb.Hub().Mode() != live.ModeFull {
		t.Fatal("the only member to answer is not focused")
	}
}

// ---- 1.2 one member per daemon -----------------------------------------------------

func TestTwoContextsForOneDaemonAreOneMember(t *testing.T) {
	a := startFake(t, "a")
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)

	// The same socket, under another context name and another spelling.
	dir := filepath.Dir(a.Socket())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{
		a.Host(),
		"unix://" + dir + "/./" + filepath.Base(a.Socket()),
		"unix://" + filepath.Join(link, filepath.Base(a.Socket())),
	} {
		m, existing, err := s.Add(endpoint(host, "other-name"))
		if err != nil || !existing || m != ma {
			t.Fatalf("Add(%s): member %v existing=%v err=%v", host, m, existing, err)
		}
	}
	if s.Len() != 1 {
		t.Fatalf("%d members, want 1", s.Len())
	}
}

func TestCanonical(t *testing.T) {
	ca1, ca2, ca3 := filepath.Join(t.TempDir(), "ca.pem"), filepath.Join(t.TempDir(), "ca.pem"), filepath.Join(t.TempDir(), "ca.pem")
	for _, f := range []struct{ path, body string }{{ca1, "CA one"}, {ca2, "CA one"}, {ca3, "CA two"}} {
		if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tls := func(ca string, skip bool) *dockerctx.TLS { return &dockerctx.TLS{CAFile: ca, SkipVerify: skip} }
	same := [][2]dockerctx.Endpoint{
		{{Host: "ssh://ops@Prod-1"}, {Host: "ssh://ops@prod-1:22/var/run/docker.sock"}},
		{{Host: "ssh://ops@prod-1/run/docker.sock"}, {Host: "ssh://ops@prod-1/run//docker.sock"}},
		{{Host: "tcp://10.0.0.5"}, {Host: "tcp://10.0.0.5:2375"}},
		{{Host: "tcp://Build.example:2376", TLS: tls(ca1, false)}, {Host: "tcp://build.example:2376", TLS: tls(ca2, false)}},
	}
	differ := [][2]dockerctx.Endpoint{
		{{Host: "ssh://ops@prod-1"}, {Host: "ssh://root@prod-1"}},
		{{Host: "ssh://prod-1"}, {Host: "ssh://prod-1:2222"}},
		{{Host: "ssh://prod-1"}, {Host: "ssh://prod-1/run/user/1000/docker.sock"}},
		{{Host: "tcp://10.0.0.5:2375"}, {Host: "tcp://10.0.0.5:2376"}},
		{{Host: "tcp://10.0.0.5:2376"}, {Host: "tcp://10.0.0.5:2376", TLS: tls(ca1, false)}},
		{{Host: "tcp://10.0.0.5:2376", TLS: tls(ca1, false)}, {Host: "tcp://10.0.0.5:2376", TLS: tls(ca3, false)}},
		{{Host: "tcp://10.0.0.5:2376", TLS: tls(ca1, false)}, {Host: "tcp://10.0.0.5:2376", TLS: tls(ca1, true)}},
		{{Host: "unix:///var/run/a.sock"}, {Host: "tcp://localhost:2375"}},
	}
	key := func(ep dockerctx.Endpoint) string {
		k, err := Canonical(ep)
		if err != nil {
			t.Fatalf("Canonical(%s): %v", ep.Host, err)
		}
		return k
	}
	for _, p := range same {
		if a, b := key(p[0]), key(p[1]); a != b {
			t.Errorf("%s and %s are one daemon: %q != %q", p[0].Host, p[1].Host, a, b)
		}
	}
	for _, p := range differ {
		if a, b := key(p[0]), key(p[1]); a == b {
			t.Errorf("%s and %s are different daemons, both %q", p[0].Host, p[1].Host, a)
		}
	}
	if _, err := Canonical(dockerctx.Endpoint{Host: "npipe:////./pipe/docker"}); err == nil {
		t.Error("an endpoint the desk cannot use has no canonical form")
	}
}

// ---- 1.3 / 4.2 removal and jobs ----------------------------------------------------

func TestRemovalRefusedWhileAJobRuns(t *testing.T) {
	a, b := startFake(t, "a"), startFake(t, "b")
	mgr := jobs.NewManager()
	t.Cleanup(mgr.Close)
	s := newSession(t, Config{Jobs: mgr})
	launch(t, s, a, false)
	mb := add(t, s, endpoint(b.Host(), "staging"))
	connected(t, mb)
	waitFor(t, "b's event stream", func() bool { return b.EventSubscribers() == 1 })

	gate := make(chan struct{})
	id, err := mgr.StartOn(mb.Daemon(), "pull image", "nginx:1.27", func(ctx context.Context, r *jobs.Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Remove(mb.ID())
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("removal with a running job: %v", err)
	}
	if len(refused.Jobs) != 1 || refused.Jobs[0].ID != id || !strings.Contains(err.Error(), "pull image nginx:1.27") || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("the refusal does not name the job: %v", err)
	}
	if s.Get(mb.ID()) != mb || mb.State() != Connected || b.EventSubscribers() != 1 {
		t.Fatal("a refused removal changed the member")
	}
	// Jobs can still start there: nothing was marked.
	if _, err := mgr.StartOn(mb.Daemon(), "x", "y", func(context.Context, *jobs.Run) error { return nil }); err != nil {
		t.Fatalf("a refused removal left the daemon closing: %v", err)
	}

	// Cancelled, the job no longer holds the daemon.
	mgr.Cancel(id)
	waitFor(t, "no job running on b", func() bool { return len(mgr.RunningOn(mb.Daemon())) == 0 })
	if _, err := s.Remove(mb.ID()); err != nil {
		t.Fatal(err)
	}
	ran := false
	if _, err := mgr.StartOn(mb.Daemon(), "x", "y", func(context.Context, *jobs.Run) error { ran = true; return nil }); !errors.Is(err, jobs.ErrDaemonClosing) || ran {
		t.Fatalf("a job started on a removed daemon: err %v, ran %v", err, ran)
	}
	// Added back, it takes jobs again.
	mb2 := add(t, s, endpoint(b.Host(), "staging"))
	connected(t, mb2)
	if _, err := mgr.StartOn(mb2.Daemon(), "x", "y", func(context.Context, *jobs.Run) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestJobStartAndRemovalRace(t *testing.T) {
	a := startFake(t, "a")
	for i := 0; i < 200; i++ {
		mgr := jobs.NewManager()
		s := New(Config{Jobs: mgr, SetupHub: fastHubs})
		eng, err := engine.New(engine.Options{Host: a.Host()})
		if err != nil {
			t.Fatal(err)
		}
		m := s.AddLaunch(endpoint(a.Host(), "default"), eng, engine.Info{}, nil, func() { _ = eng.Close() })
		var startErr, removeErr error
		ran := make(chan struct{}, 1)
		hold := make(chan struct{})
		var wg sync.WaitGroup
		go1 := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-go1
			_, startErr = mgr.StartOn(m.Daemon(), "pull image", "alpine", func(ctx context.Context, r *jobs.Run) error {
				ran <- struct{}{}
				<-hold // still running when the removal is decided
				return nil
			})
		}()
		go func() {
			defer wg.Done()
			<-go1
			_, removeErr = s.Remove(m.ID())
		}()
		close(go1)
		wg.Wait()
		close(hold)
		mgr.Wait()
		jobWon, removalWon := startErr == nil, removeErr == nil
		if jobWon == removalWon {
			t.Fatalf("round %d: job err %v, removal err %v — exactly one must win", i, startErr, removeErr)
		}
		didRun := len(ran) == 1
		if didRun != jobWon {
			t.Fatalf("round %d: job won=%v but ran=%v", i, jobWon, didRun)
		}
		if !removalWon && s.Len() != 1 {
			t.Fatalf("round %d: a refused removal removed the member", i)
		}
		s.Close()
		mgr.Close()
	}
}

// ---- 5.1 a picked daemon that is down ---------------------------------------------

func TestUnreachableMemberIsDisconnectedAndTheSessionStaysUsable(t *testing.T) {
	a := startFake(t, "a", "web")
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)

	sock := filepath.Join(t.TempDir(), "later.sock")
	down := add(t, s, endpoint("unix://"+sock, "later"))
	waitFor(t, "disconnected with a reason", func() bool { return down.State() == Disconnected })
	if r := down.Reason(); !strings.Contains(r, "does not exist") {
		t.Fatalf("reason %q", r)
	}
	if down.Engine() != nil || down.Hub() != nil {
		t.Fatal("a member that never answered has an engine")
	}
	// The rest of the session works: the launch member is focused and
	// answers, and its events still arrive.
	if s.Focused() != ma || ma.State() != Connected {
		t.Fatal("the launch member was disturbed")
	}
	if _, err := ma.Engine().Containers(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Crash("web", 2)
	waitFor(t, "a's die event", func() bool {
		for _, e := range ma.Hub().Events.Tail(10) {
			if e.Event.Action == "die" {
				return true
			}
		}
		return false
	})
	n := TakeAll(s)
	if !containsText(n, "later: not reachable — ") {
		t.Fatalf("notices %v", n)
	}

	// It is retried, and connects when its daemon appears.
	b := fakeengine.New()
	b.Name = "later"
	if err := b.Start(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	connected(t, down)
	if down.Info().Name != "later" || down.Attempts() == 0 {
		t.Fatalf("info %+v after %d attempts", down.Info(), down.Attempts())
	}
	if s.Focused() != ma || down.Hub().Mode() != live.ModeEventsOnly {
		t.Fatal("a member that came back took focus, or streams in full")
	}
	if !containsText(TakeAll(s), "later: connected (Engine ") {
		t.Fatal("no notice that it connected")
	}
}

// ---- 5.2 a picked ssh daemon never asks ---------------------------------------------

func TestPickedSSHNeverAsks(t *testing.T) {
	cmd, invocations := stubSSH(t, "passphrase")
	a := startFake(t, "a")
	s := newSession(t, Config{DialOptions: DialOptions{SSHCommand: cmd}})
	ma := launch(t, s, a, false)

	m := add(t, s, endpoint("ssh://ops@prod:2222"+a.Socket(), "prod"))
	waitFor(t, "disconnected", func() bool { return m.State() == Disconnected })
	if r := m.Reason(); !strings.Contains(r, "Permission denied (publickey)") {
		t.Fatalf("the reason should be ssh's own words: %q", r)
	}
	invs := invocations()
	if len(invs) == 0 {
		t.Fatal("ssh was never started")
	}
	for _, inv := range invs {
		if !inv.Batch || !inv.Leader || inv.TTY {
			t.Fatalf("ssh could have asked: %+v (want batch mode, no controlling terminal)", inv)
		}
	}
	if s.Focused() != ma || ma.State() != Connected {
		t.Fatal("the session stopped working")
	}
	// Removing it stops the retries, and leaves no ssh behind.
	if _, err := s.Remove(m.ID()); err != nil {
		t.Fatal(err)
	}
	<-m.Closed()
	time.Sleep(50 * time.Millisecond)
	before := len(invocations())
	time.Sleep(150 * time.Millisecond)
	if after := len(invocations()); after != before {
		t.Fatalf("ssh was started %d more time(s) after the member was removed", after-before)
	}
	for _, inv := range invocations() {
		if alive(inv.PID) {
			t.Fatalf("ssh %d is still running", inv.PID)
		}
	}
}

func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// A zombie is not alive; it is reaped by whoever started it.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	s := string(data)
	if i := strings.LastIndex(s, ")"); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

func TestPickedSSHThatConnects(t *testing.T) {
	cmd, invocations := stubSSH(t, "forward")
	a, b := startFake(t, "a"), startFake(t, "far-away", "remote-web")
	s := newSession(t, Config{DialOptions: DialOptions{SSHCommand: cmd}})
	launch(t, s, a, false)
	m := add(t, s, endpoint("ssh://ops@prod"+b.Socket(), "prod"))
	connected(t, m)
	if m.Info().Name != "far-away" || !m.Link().Remote || m.Link().Transport != dockerctx.TransportSSH {
		t.Fatalf("info %+v link %+v", m.Info(), m.Link())
	}
	invs := invocations()
	if len(invs) != 1 || !invs[0].Batch {
		t.Fatalf("ssh invocations %+v, want one, in batch mode", invs)
	}
	if _, err := s.Remove(m.ID()); err != nil {
		t.Fatal(err)
	}
	<-m.Closed()
	waitFor(t, "ssh ended", func() bool { return !alive(invs[0].PID) })
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "docker-desk-ssh-*", "*.sock")); len(left) > 0 {
		for _, l := range left {
			if strings.Contains(l, fmt.Sprint(invs[0].PID)) {
				t.Fatalf("socket left behind: %s", l)
			}
		}
	}
}

// ---- 5.3 read-only applies to every member -------------------------------------------

func TestMemberAddedToAReadOnlySessionIsReadOnly(t *testing.T) {
	a, b := startFake(t, "a"), startFake(t, "b", "web")
	s := newSession(t, Config{ReadOnly: true})
	launch(t, s, a, true)
	m := add(t, s, endpoint(b.Host(), "b"))
	connected(t, m)
	b.ResetRequests()
	err := m.Engine().StopContainer(context.Background(), b.ContainerID("web"))
	if !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("StopContainer on a member of a read-only session: %v", err)
	}
	if !engine.IsReadOnly(m.Engine()) {
		t.Fatal("the member's engine is not the read-only wrapper")
	}
	for _, r := range b.Requests() {
		if r.Mutating() || strings.Contains(r.Path, "/stop") {
			t.Fatalf("a request reached the daemon: %s", r)
		}
	}
	if st, _ := b.ContainerState("web"); st != "running" {
		t.Fatalf("web is %s", st)
	}
}

// ---- background members --------------------------------------------------------------

func TestBackgroundMemberHoldsOnlyItsEventStream(t *testing.T) {
	a, b := startFake(t, "a", "a-web"), startFake(t, "b", "b-web")
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	mb := add(t, s, endpoint(b.Host(), "b"))
	connected(t, mb)
	waitFor(t, "b's event stream", func() bool { return b.EventSubscribers() == 1 })

	// The focused member's panes open streams; the background one's cannot.
	ma.Hub().SyncStats([]string{a.ContainerID("a-web")}, "")
	mb.Hub().SyncStats([]string{b.ContainerID("b-web")}, "")
	mb.Hub().SetLogTarget(b.ContainerID("b-web"))
	waitFor(t, "a's stats stream", func() bool { return a.StatsStreams() == 1 })
	time.Sleep(50 * time.Millisecond)
	if b.StatsStreams() != 0 || b.LogFollowers("b-web") != 0 || b.EventSubscribers() != 1 {
		t.Fatalf("background daemon: %d stats, %d log, %d event streams", b.StatsStreams(), b.LogFollowers("b-web"), b.EventSubscribers())
	}

	// Focus moves: a keeps only its event stream.
	if _, err := s.Focus(mb.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a's stats stream closed", func() bool { return a.StatsStreams() == 0 })
	if a.EventSubscribers() != 1 || ma.Hub().Mode() != live.ModeEventsOnly || mb.Hub().Mode() != live.ModeFull {
		t.Fatal("after the focus change")
	}

	// An event recorded while b was in the background is in its own ring.
	if _, err := s.Focus(ma.ID()); err != nil {
		t.Fatal(err)
	}
	b.Crash("b-web", 1)
	waitFor(t, "b's die event in b's history", func() bool {
		for _, e := range mb.Hub().Events.Tail(20) {
			if e.Event.Action == "die" && e.Event.Name() == "b-web" {
				return true
			}
		}
		return false
	})
	for _, e := range ma.Hub().Events.Tail(20) {
		if e.Event.Name() == "b-web" {
			t.Fatal("b's event reached a's history")
		}
	}
}

func TakeAll(s *Session) []Notice { return s.TakeNotices(time.Now()) }

func containsText(ns []Notice, sub string) bool {
	for _, n := range ns {
		if strings.Contains(n.Text, sub) {
			return true
		}
	}
	return false
}

func TestNoticesFromBackgroundMembers(t *testing.T) {
	a, b := startFake(t, "a", "a-web"), startFake(t, "b", "b-web", "b-db")
	s := newSession(t, Config{})
	launch(t, s, a, false)
	mb := add(t, s, endpoint(b.Host(), "staging"))
	connected(t, mb)
	waitFor(t, "b's event stream", func() bool { return b.EventSubscribers() == 1 })
	if n := TakeAll(s); !containsText(n, "staging: connected") {
		t.Fatalf("first notices %v", n)
	}

	// An exit on the focused daemon is the Events pane's: no notice.
	a.Crash("a-web", 1)
	time.Sleep(60 * time.Millisecond)
	if n := TakeAll(s); len(n) != 0 {
		t.Fatalf("notices for the focused daemon: %v", n)
	}
	// An exit in the background is one notice, naming member and container.
	b.Crash("b-web", 137)
	var got []Notice
	waitFor(t, "a notice", func() bool { got = append(got, TakeAll(s)...); return len(got) > 0 })
	if len(got) != 1 || got[0].Member != mb || got[0].Text != "staging: container b-web exited (code 137)" || !got[0].Bad {
		t.Fatalf("notices %+v", got)
	}
	// Anything else on a background daemon is not a notice.
	b.Emit("network", "create", "n1", map[string]string{"name": "n1"})
	time.Sleep(60 * time.Millisecond)
	if n := TakeAll(s); len(n) != 0 {
		t.Fatalf("a network event made a notice: %v", n)
	}

	// The daemon goes away, and comes back.
	b.Stop()
	waitFor(t, "lost", func() bool { return containsText(TakeAll(s), "staging: lost the connection — retrying") })
	if err := b.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "back", func() bool { return containsText(TakeAll(s), "staging: reconnected") })
}

func TestBackgroundExitsAreRateLimited(t *testing.T) {
	a, b := startFake(t, "a"), startFake(t, "b", "c1", "c2", "c3")
	s := New(Config{SetupHub: fastHubs, NoticeGap: time.Hour})
	t.Cleanup(s.Close)
	launch(t, s, a, false)
	mb := add(t, s, endpoint(b.Host(), "staging"))
	connected(t, mb)
	waitFor(t, "b's event stream", func() bool { return b.EventSubscribers() == 1 })
	TakeAll(s)
	b.Crash("c1", 1)
	waitFor(t, "first exit", func() bool {
		n := TakeAll(s)

		return containsText(n, "c1 exited")
	})
	b.Crash("c2", 1)
	b.Crash("c3", 1)
	time.Sleep(80 * time.Millisecond)
	if n := TakeAll(s); len(n) != 0 {
		t.Fatalf("within the gap: %v", n)
	}
	// After the gap, one notice for both.
	n := s.TakeNotices(time.Now().Add(2 * time.Hour))
	if len(n) != 1 || n[0].Text != "staging: container c2 exited (code 1), and 1 more container event(s)" {
		t.Fatalf("after the gap: %+v", n)
	}
}
