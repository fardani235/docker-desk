package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/sshtest"
)

// The same tunnel with nothing standing in for anything on the SSH side: the
// real ssh client, a real sshd on a loopback port, public-key authentication,
// and OpenSSH's own forwarding to the fake daemon's socket. Skipped where
// OpenSSH's server is not installed.

func realOptions(srv *sshtest.Server, socket string) Options {
	return Options{
		User: srv.User, Host: "127.0.0.1", Port: srv.Port,
		RemoteSocket: socket,
		Command:      srv.Command(),
		MinBackoff:   50 * time.Millisecond,
		MaxBackoff:   200 * time.Millisecond,
		RetryTimeout: 5 * time.Second,
	}
}

func TestRealSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	fake := fakeengine.New()
	fake.StatsInterval = 20 * time.Millisecond
	for i := 0; i < 40; i++ {
		fake.AddContainer(fakeengine.ContainerSpec{Name: "c" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Image: "app:1"})
	}
	cleanup, err := fake.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	tun := open(t, realOptions(sshd, fake.Socket()))

	// The local socket is the operator's alone: whoever can connect to it
	// controls the remote daemon.
	sock := strings.TrimPrefix(tun.Host(), "unix://")
	if st, err := os.Stat(sock); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("local socket permissions: %v %v", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(filepath.Dir(sock)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory permissions: %v %v", st.Mode().Perm(), err)
	}

	eng, err := engine.New(engine.Options{Host: tun.Host()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := eng.Ping(ctx); err != nil {
		t.Fatalf("ping through real ssh: %v\nsshd said:\n%s", err, sshd.Log())
	}
	cs, err := eng.Containers(ctx)
	if err != nil || len(cs) != 40 {
		t.Fatalf("containers: %d %v", len(cs), err)
	}

	// A mutation, and the event stream reporting it.
	var mu sync.Mutex
	var actions []string
	go func() {
		_ = eng.Events(ctx, time.Time{}, func(e engine.Event) {
			mu.Lock()
			actions = append(actions, e.Action)
			mu.Unlock()
		})
	}()
	eventually(t, "the event stream", func() bool { return fake.EventSubscribers() == 1 })
	if err := eng.PauseContainer(ctx, cs[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the pause event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(actions, " "), "pause")
	})

	// The reason for one tunnel instead of one ssh per connection: the desk
	// keeps a statistics stream open per running container, and all of them
	// must fit — here 40 at once, well past sshd's default limits on
	// sessions per connection (10) and on connections being set up (10).
	var wg sync.WaitGroup
	var got sync.Map
	sctx, stop := context.WithCancel(ctx)
	for _, c := range cs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = eng.Stats(sctx, id, func(engine.RawStats) { got.Store(id, true) })
		}(c.ID)
	}
	eventually(t, "a sample from every one of 40 concurrent streams", func() bool {
		n := 0
		got.Range(func(_, _ any) bool { n++; return true })
		return n == 40
	})
	stop()
	wg.Wait()

	// The hijacked connection: an interactive shell through the tunnel.
	id, err := eng.ExecCreate(ctx, cs[1].ID, []string{"sh"}, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := eng.ExecAttach(ctx, id, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var out lockedBuffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&out, conn); close(done) }()
	eventually(t, "the prompt", func() bool { return strings.Contains(out.String(), ":/# ") })
	_, _ = conn.Write([]byte("echo through-ssh\r"))
	eventually(t, "the echo", func() bool { return strings.Contains(out.String(), "\r\nthrough-ssh\r\n") })
	_, _ = conn.Write([]byte("exit\r"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell stream did not end after exit")
	}

	if tun.Restarts() != 0 {
		t.Fatalf("all of that should have used one ssh connection; it was restarted %d time(s)", tun.Restarts())
	}
}

func TestRealSSHFailures(t *testing.T) {
	fake := startEngine(t)

	t.Run("the key is not accepted", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		sshd.UseWrongKey()
		_, err := Open(context.Background(), realOptions(sshd, fake.Socket()))
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Reason, "Permission denied (publickey)") || !strings.Contains(te.Hint, "could not log in") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("the host key is unknown", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		sshd.ForgetHostKey()
		_, err := Open(context.Background(), realOptions(sshd, fake.Socket()))
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Reason, "Host key verification failed") || !strings.Contains(te.Hint, "review it") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("nothing listens on the port", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		sshd.Stop()
		_, err := Open(context.Background(), realOptions(sshd, fake.Socket()))
		var te *Error
		if !errors.As(err, &te) || !strings.Contains(te.Reason, "Connection refused") {
			t.Fatalf("got %v", err)
		}
	})
}

// TestRealSSHExplain: ssh reports every failure to open the far end with the
// same words, so the remote machine is asked what is actually wrong.
func TestRealSSHExplain(t *testing.T) {
	fake := startEngine(t)
	dir := filepath.Dir(fake.Socket())

	explain := func(t *testing.T, sshd *sshtest.Server, socket string) (string, string) {
		t.Helper()
		tun := open(t, realOptions(sshd, socket))
		if err := ping(t, tun.Host()); !engine.IsUnavailable(err) {
			t.Fatalf("a far end that cannot be opened must read as unavailable, got %v", err)
		}
		return tun.Explain(context.Background())
	}

	t.Run("no socket there", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		reason, hint := explain(t, sshd, filepath.Join(dir, "nope.sock"))
		if !strings.Contains(reason, "there is no socket at") || !strings.Contains(hint, "rootless") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})

	t.Run("not a socket", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		plain := filepath.Join(dir, "plain-file")
		if err := os.WriteFile(plain, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		reason, _ := explain(t, sshd, plain)
		if !strings.Contains(reason, "exists but is not a socket") {
			t.Fatalf("%q", reason)
		}
	})

	// Found by the review: a socket in a directory the user may not enter
	// used to be reported as "there is no socket", with "is Docker
	// running?" — it is, for someone else.
	t.Run("in a directory the user may not enter", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root enters every directory")
		}
		sshd := sshtest.Start(t, sshtest.Options{})
		private := filepath.Join(dir, "private")
		if err := os.Mkdir(private, 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("unix", filepath.Join(private, "docker.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if err := os.Chmod(private, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(private, 0o700)
		reason, hint := explain(t, sshd, filepath.Join(private, "docker.sock"))
		if !strings.Contains(reason, "may not look inside "+private) || strings.Contains(reason, "there is no socket") || !strings.Contains(hint, "private to another user") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})

	t.Run("not permitted", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores socket permissions")
		}
		sshd := sshtest.Start(t, sshtest.Options{})
		locked := fakeengine.New()
		cleanup, err := locked.StartTemp()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		if err := os.Chmod(locked.Socket(), 0o000); err != nil {
			t.Fatal(err)
		}
		reason, hint := explain(t, sshd, locked.Socket())
		if !strings.Contains(reason, "may not open") || !strings.Contains(hint, "`docker` group") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})

	// Two causes look the same from the client and from the remote shell: a
	// socket nobody listens on, and a server that forbids the forwarding.
	// The message names both rather than guess.
	t.Run("the server forbids forwarding to sockets", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{NoSocketForwarding: true})
		reason, hint := explain(t, sshd, fake.Socket())
		if !strings.Contains(reason, "could not connect to it") || !strings.Contains(hint, "AllowStreamLocalForwarding") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})

	t.Run("a socket nobody listens on", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{})
		stale := filepath.Join(dir, "stale.sock")
		l, err := net.Listen("unix", stale)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
		reason, hint := explain(t, sshd, stale)
		if !strings.Contains(reason, "is there and") || !strings.Contains(reason, "could not connect to it") || !strings.Contains(hint, "nothing is listening on it") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})

	t.Run("the server answers no questions", func(t *testing.T) {
		sshd := sshtest.Start(t, sshtest.Options{NoSocketForwarding: true, ForceCommand: "/bin/false"})
		reason, hint := explain(t, sshd, fake.Socket())
		if !strings.Contains(reason, "is up, but the server could not open") || !strings.Contains(hint, "sshd_config") {
			t.Fatalf("%q / %q", reason, hint)
		}
	})
}

// TestRealSSHReconnect: the remote machine goes away and comes back.
func TestRealSSHReconnect(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	fake := startEngine(t)
	tun := open(t, realOptions(sshd, fake.Socket()))
	if err := ping(t, tun.Host()); err != nil {
		t.Fatal(err)
	}

	sshd.Stop()
	eventually(t, "the tunnel to notice", func() bool { return !tun.Up() })
	eventually(t, "a reason from ssh", func() bool { return tun.Problem() != "" })
	if err := ping(t, tun.Host()); !engine.IsUnavailable(err) {
		t.Fatalf("with the server gone the daemon must read as unavailable, got %v", err)
	}
	t.Logf("while down: %s", tun.Problem())

	if err := sshd.Restart(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the tunnel to come back", func() bool { return tun.Up() })
	if err := ping(t, tun.Host()); err != nil {
		t.Fatalf("ping after the server came back: %v", err)
	}
	if tun.Restarts() < 1 {
		t.Fatal("the supervisor should have started ssh again")
	}
}
