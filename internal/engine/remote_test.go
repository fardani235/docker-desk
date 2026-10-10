package engine

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/testguard"
)

// The same engine, reached the way a remote daemon is: over TCP, in the
// clear and with mutual TLS. Everything the desk needs must work through
// each — plain requests, long-lived streams, and the hijacked connection of
// an interactive exec, which the client library dials by another code path.

func startFakeTCP(t *testing.T, withTLS bool) (*fakeengine.Server, Options) {
	t.Helper()
	srv := fakeengine.New()
	srv.StatsInterval = 20 * time.Millisecond
	srv.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	opts := Options{}
	if withTLS {
		dir := t.TempDir()
		if err := srv.StartTLS(dir); err != nil {
			t.Fatal(err)
		}
		opts.TLS = &TLS{CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem")}
	} else if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	opts.Host = srv.Host()
	return srv, opts
}

func TestOverTCPAndTLS(t *testing.T) {
	for _, withTLS := range []bool{false, true} {
		name := "plain tcp"
		if withTLS {
			name = "mutual tls"
		}
		t.Run(name, func(t *testing.T) {
			srv, opts := startFakeTCP(t, withTLS)
			if !strings.HasPrefix(opts.Host, "tcp://127.0.0.1:") {
				t.Fatalf("the fake should be on a loopback port, got %s", opts.Host)
			}
			eng, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			ctx := ctxT(t)

			// Requests.
			if err := eng.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			info, err := eng.Info(ctx)
			if err != nil || info.ServerVersion != fakeengine.EngineVersion {
				t.Fatalf("info: %+v %v", info, err)
			}
			cs, err := eng.Containers(ctx)
			if err != nil || len(cs) != 1 || cs[0].Name != "app" {
				t.Fatalf("containers: %+v %v", cs, err)
			}

			// A stream that stays open, and a mutation it reports.
			events := &syncBuffer{}
			go func() {
				_ = eng.Events(ctx, time.Time{}, func(e Event) { _, _ = events.Write([]byte(e.Action + " ")) })
			}()
			waitFor(t, "event subscription", func() bool { return srv.EventSubscribers() == 1 })
			if err := eng.PauseContainer(ctx, cs[0].ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "pause event", func() bool { return strings.Contains(events.String(), "pause") })

			if err := eng.UnpauseContainer(ctx, cs[0].ID); err != nil {
				t.Fatal(err)
			}

			// The hijacked connection: an interactive shell.
			id, err := eng.ExecCreate(ctx, cs[0].ID, []string{"sh"}, 24, 80)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := eng.ExecAttach(ctx, id, 24, 80)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			out := &syncBuffer{}
			done := make(chan struct{})
			go func() { _, _ = io.Copy(out, conn); close(done) }()
			waitFor(t, "prompt", func() bool { return strings.Contains(out.String(), ":/# ") })
			_, _ = conn.Write([]byte("echo through-the-network\r"))
			waitFor(t, "echo", func() bool { return strings.Contains(out.String(), "\r\nthrough-the-network\r\n") })
			_, _ = conn.Write([]byte("exit\r"))
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the shell stream did not end after exit")
			}
		})
	}
}

// TestTLSIsActuallyEnforced: the fake's TLS port really is one — a client
// without TLS, without a certificate, or trusting another CA gets nowhere —
// and the failures are of the kind the launch check explains.
func TestTLSIsActuallyEnforced(t *testing.T) {
	_, good := startFakeTCP(t, true)
	otherDir := t.TempDir()
	other := fakeengine.New()
	if err := other.StartTLS(otherDir); err != nil { // only for its CA
		t.Fatal(err)
	}
	t.Cleanup(other.Close)

	cases := []struct {
		name string
		tls  *TLS
		want string
	}{
		{"no TLS at all", nil, "HTTP request to an HTTPS server"},
		{"no client certificate", &TLS{CAFile: good.TLS.CAFile}, "certificate"},
		{"another CA", &TLS{CAFile: filepath.Join(otherDir, "ca.pem"), CertFile: good.TLS.CertFile, KeyFile: good.TLS.KeyFile}, "certificate signed by unknown authority"},
		{"a certificate from another CA", &TLS{CAFile: good.TLS.CAFile, CertFile: filepath.Join(otherDir, "cert.pem"), KeyFile: filepath.Join(otherDir, "key.pem")}, "certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := New(Options{Host: good.Host, TLS: tc.tls})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			err = eng.Ping(ctxT(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a failure mentioning %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("skipping verification still needs the client certificate", func(t *testing.T) {
		eng, err := New(Options{Host: good.Host, TLS: &TLS{SkipVerify: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		if err := eng.Ping(ctxT(t)); err == nil {
			t.Fatal("a client without a certificate was let in")
		}
		eng2, err := New(Options{Host: good.Host, TLS: &TLS{SkipVerify: true, CertFile: good.TLS.CertFile, KeyFile: good.TLS.KeyFile}})
		if err != nil {
			t.Fatal(err)
		}
		defer eng2.Close()
		if err := eng2.Ping(ctxT(t)); err != nil {
			t.Fatalf("unverified TLS with the client certificate should connect: %v", err)
		}
	})

	t.Run("TLS towards a port that does not speak it", func(t *testing.T) {
		_, plain := startFakeTCP(t, false)
		eng, err := New(Options{Host: plain.Host, TLS: &TLS{SkipVerify: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		if err := eng.Ping(ctxT(t)); err == nil || !strings.Contains(err.Error(), "HTTP response to HTTPS client") {
			t.Fatalf("got %v", err)
		}
	})
}

// TestTLSMaterialIsCheckedAtConstruction: a certificate problem is a launch
// error naming the file, not the reason some later request fails.
func TestTLSMaterialIsCheckedAtConstruction(t *testing.T) {
	_, good := startFakeTCP(t, true)
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"missing CA file", Options{Host: good.Host, TLS: &TLS{CAFile: filepath.Join(dir, "nope.pem")}}, "reading the CA certificate"},
		{"CA file without a certificate", Options{Host: good.Host, TLS: &TLS{CAFile: junk}}, "contains no PEM certificate"},
		{"certificate without its key", Options{Host: good.Host, TLS: &TLS{CertFile: good.TLS.CertFile}}, "both its certificate and its key"},
		{"unreadable key pair", Options{Host: good.Host, TLS: &TLS{CertFile: junk, KeyFile: junk}}, "loading the client certificate"},
		{"TLS on a unix socket", Options{Host: "unix:///tmp/dd-x.sock", TLS: &TLS{}}, "TLS applies to tcp://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestGuardAllowsOnlyTheFakesOwnListeners: a TCP address has no directory to
// tell a fake daemon by, so under the guard only an address the fake engine
// itself listens on may be opened read-write.
func TestGuardAllowsOnlyTheFakesOwnListeners(t *testing.T) {
	_, opts := startFakeTCP(t, false)
	if _, err := New(Options{Host: opts.Host}); err != nil {
		t.Fatalf("the fake's own listener must be allowed: %v", err)
	}
	for _, host := range []string{"tcp://127.0.0.1:2375", "tcp://127.0.0.1:2376", "tcp://localhost:2375", "tcp://10.0.0.5:2376", "ssh://ops@prod"} {
		if _, err := New(Options{Host: host}); err == nil || !strings.Contains(err.Error(), "test guard") {
			t.Errorf("%s: a read-write engine must be refused under test, got %v", host, err)
		}
	}
	// Read-only is how a real daemon may be looked at; nothing is sent here.
	eng, err := New(Options{Host: "tcp://127.0.0.1:2375", ReadOnly: true})
	if err != nil {
		t.Fatalf("a read-only engine is allowed anywhere: %v", err)
	}
	eng.Close()
}

// TestAttachIsBoundedOverTLS: found by the review. For a TLS endpoint the
// client library dials the hijacked connection of an exec without a context,
// so a peer that accepts the connection and then says nothing would hold the
// caller for ever — and the caller is the shell, which by then has been
// handed the terminal. The engine bounds it by the context it was given.
func TestAttachIsBoundedOverTLS(t *testing.T) {
	// A port that accepts and never speaks: the TLS handshake stalls.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	}()
	host := "tcp://" + l.Addr().String()
	// Not a fake daemon, but nothing that could be mutated either: a
	// listener of this test's own that never answers.
	testguard.Allow(host)

	eng, err := New(Options{Host: host, TLS: &TLS{SkipVerify: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = eng.ExecAttach(ctx, "0123456789abcdef", 24, 80)
	if err == nil || !strings.Contains(err.Error(), "did not attach the process in time") {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the attach outlived its context by far: %s", d)
	}
}

// TestErrorsNameTheDaemonNotTheTunnel: found by the review. An ssh:// daemon
// is dialled through a socket in a temporary directory, and the client
// library quotes the address it dialled; the desk then appeared to be
// talking to "unix:///tmp/docker-desk-ssh-…". Messages name the endpoint the
// operator knows.
func TestErrorsNameTheDaemonNotTheTunnel(t *testing.T) {
	dir := t.TempDir()
	gone := "unix://" + filepath.Join(dir, "docker-desk-ssh-123", "d.sock")
	eng, err := New(Options{Host: gone, ShownAs: "ssh://ops@prod-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	_, err = eng.Containers(ctxT(t))
	if !IsUnavailable(err) {
		t.Fatalf("want an unavailable daemon, got %v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "docker-desk-ssh-123") || strings.Contains(msg, "%2F") || !strings.Contains(msg, "ssh://ops@prod-1") {
		t.Fatalf("the message should name ssh://ops@prod-1 and not the tunnel's socket: %q", msg)
	}

	// Without an alias nothing is rewritten.
	plain, err := New(Options{Host: gone})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.Containers(ctxT(t)); err == nil || !strings.Contains(err.Error(), "docker-desk-ssh-123") {
		t.Fatalf("got %v", err)
	}
}
