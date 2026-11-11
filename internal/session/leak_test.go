package session

import (
	"runtime"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

// settledGoroutines waits for the goroutine count to stop falling, and
// returns it.
func settledGoroutines() int {
	n := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

// Adding members, streaming from them, removing them and closing the
// session leaves no goroutine and no ssh process behind: the counts return
// to what they were before the session existed.
func TestNoGoroutineOrSSHLeftBehind(t *testing.T) {
	cmd, invocations := stubSSH(t, "forward")
	a, b, c := startFake(t, "a", "a-web"), startFake(t, "b", "b-web"), startFake(t, "c")
	baseline := settledGoroutines()

	s := New(Config{SetupHub: fastHubs, RetryMin: 10 * time.Millisecond, RetryMax: 40 * time.Millisecond, DialOptions: DialOptions{SSHCommand: cmd}})
	eng, err := engine.New(engine.Options{Host: a.Host()})
	if err != nil {
		t.Fatal(err)
	}
	ma := s.AddLaunch(endpoint(a.Host(), "default"), eng, engine.Info{}, nil, func() { _ = eng.Close() })
	s.Start()
	mb := add(t, s, endpoint(b.Host(), "b"))
	mssh := add(t, s, endpoint("ssh://ops@prod"+c.Socket(), "far"))
	down := add(t, s, endpoint("unix:///tmp/dd-no-such-daemon.sock", "down"))
	connected(t, mb)
	connected(t, mssh)
	waitFor(t, "down", func() bool { return down.State() == Disconnected })

	// Streams on the focused member, then on another after a focus change.
	ma.Hub().SyncStats([]string{a.ContainerID("a-web")}, "")
	ma.Hub().SetLogTarget(a.ContainerID("a-web"))
	waitFor(t, "a streams", func() bool { return a.StatsStreams() == 1 })
	if _, err := s.Focus(mb.ID()); err != nil {
		t.Fatal(err)
	}
	mb.Hub().SyncStats([]string{b.ContainerID("b-web")}, "")
	waitFor(t, "b streams", func() bool { return b.StatsStreams() == 1 })
	withMembers := runtime.NumGoroutine()

	// Removing members one at a time: the ssh member's process ends with it.
	pid := invocations()[0].PID
	for _, m := range []*Member{mssh, down, ma} {
		if _, err := s.Remove(m.ID()); err != nil {
			t.Fatal(err)
		}
		<-m.Closed()
	}
	waitFor(t, "ssh ended", func() bool { return !alive(pid) })
	waitFor(t, "a's streams closed", func() bool { return a.EventSubscribers() == 0 && a.StatsStreams() == 0 && a.LogFollowers("a-web") == 0 })
	if n := settledGoroutines(); n >= withMembers {
		t.Fatalf("removing three of four members freed no goroutine: %d, was %d", n, withMembers)
	}

	// Quit: the last member goes with the session.
	s.Close()
	waitFor(t, "b's streams closed", func() bool { return b.EventSubscribers() == 0 && b.StatsStreams() == 0 })
	var n int
	deadline := time.Now().Add(5 * time.Second)
	for n = settledGoroutines(); n > baseline && time.Now().Before(deadline); n = settledGoroutines() {
	}
	if n > baseline {
		buf := make([]byte, 1<<20)
		t.Fatalf("%d goroutines after Close, %d before the session:\n%s", n, baseline, buf[:runtime.Stack(buf, true)])
	}
	for _, inv := range invocations() {
		if alive(inv.PID) {
			t.Fatalf("ssh %d still running after Close", inv.PID)
		}
	}
}
