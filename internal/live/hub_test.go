package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

func startHub(t *testing.T, seed func(*fakeengine.Server)) (*fakeengine.Server, engine.Engine, *Hub) {
	t.Helper()
	srv := fakeengine.New()
	srv.StatsInterval = 15 * time.Millisecond
	if seed != nil {
		seed(srv)
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(eng)
	hub.MinBackoff, hub.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
	hub.Start()
	t.Cleanup(func() { hub.Close(); eng.Close() })
	waitFor(t, "event subscription", func() bool { return srv.EventSubscribers() == 1 })
	return srv, eng, hub
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func eventLines(h *Hub) []string {
	var out []string
	for _, e := range h.Events.Tail(EventLines) {
		if e.Marker != "" {
			out = append(out, "# "+e.Marker)
		} else {
			out = append(out, e.Event.Type+" "+e.Event.Action+" "+e.Event.Name())
		}
	}
	return out
}

func contains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestHubEventsAndDirtyKinds(t *testing.T) {
	srv, eng, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	})
	ctx := context.Background()
	if d := hub.TakeDirty(); d != 0 {
		t.Fatalf("nothing happened yet, dirty=%b", d)
	}

	if err := eng.StopContainer(ctx, srv.ContainerID("app")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop events", func() bool { return contains(eventLines(hub), "container stop app") })
	got := eventLines(hub)
	for _, want := range []string{"container kill app", "container die app", "network disconnect", "container stop app"} {
		if !contains(got, want) {
			t.Errorf("missing event %q in %v", want, got)
		}
	}
	// Container events dirty containers; the network disconnect dirties both.
	if d := hub.TakeDirty(); d != DirtyContainers|DirtyNetworks {
		t.Fatalf("dirty kinds %04b", d)
	}
	if d := hub.TakeDirty(); d != 0 {
		t.Fatalf("TakeDirty must clear, got %04b", d)
	}

	if _, err := eng.CreateVolume(ctx, "v"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "volume event", func() bool { return hub.TakeDirty() == DirtyVolumes })
}

func TestHubEventStreamSurvivesInterruption(t *testing.T) {
	srv, eng, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	})
	ctx := context.Background()
	hub.TakeDirty()

	// The daemon drops the stream but keeps answering: reconnect, mark it.
	srv.DropEventStreams()
	waitFor(t, "interruption marker", func() bool { return contains(eventLines(hub), "# event stream interrupted") })
	waitFor(t, "resume marker", func() bool { return contains(eventLines(hub), "# event stream resumed") })
	waitFor(t, "resubscription", func() bool { return srv.EventSubscribers() == 1 && hub.Connected() })
	if d := hub.TakeDirty(); d != DirtyAll {
		t.Fatalf("a reconnect must refresh everything, dirty=%04b", d)
	}
	if err := eng.PauseContainer(ctx, srv.ContainerID("app")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live events after resume", func() bool { return contains(eventLines(hub), "container pause app") })

	// The daemon goes away entirely.
	srv.Stop()
	waitFor(t, "disconnected", func() bool { return !hub.Connected() })
	time.Sleep(80 * time.Millisecond) // several failed retries
	if hub.Connected() {
		t.Fatal("hub claims to be connected to a stopped daemon")
	}
	// Something happens while the desk is disconnected…
	srv.Emit("container", "oom", srv.ContainerID("app"), map[string]string{"name": "app"})
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reconnected", func() bool { return hub.Connected() && srv.EventSubscribers() == 1 })
	// …and is replayed after reconnecting, exactly once.
	waitFor(t, "replayed event", func() bool { return contains(eventLines(hub), "container oom app") })
	n := 0
	for _, l := range eventLines(hub) {
		if strings.Contains(l, "container pause app") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("an event seen before the interruption was shown %d times after replay", n)
	}
}

func TestHubLogTarget(t *testing.T) {
	srv, eng, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1", Logs: []string{"a1", "a2"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "b", Image: "app:1", Logs: []string{"b1"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "off", Image: "app:1", State: "exited", Logs: []string{"last words"}})
	})
	ctx := context.Background()
	a, b := srv.ContainerID("a"), srv.ContainerID("b")
	logLines := func() []string { r, _ := hub.Log(); return r.Tail(LogLines) }

	hub.SetLogTarget(a)
	waitFor(t, "tail of a", func() bool { return strings.Join(logLines(), ",") == "a1,a2" })
	waitFor(t, "follower on a", func() bool { return srv.LogFollowers("a") == 1 })
	srv.WriteLog("a", 1, "a3 \x1b[2Jhostile")
	waitFor(t, "followed and sanitised line", func() bool { return contains(logLines(), "a3 hostile") })
	if _, st := hub.Log(); !st.Streaming || st.Target != a {
		t.Fatalf("status: %+v", st)
	}

	// Same target again: the stream and buffer are kept.
	ring1, _ := hub.Log()
	hub.SetLogTarget(a)
	if ring2, _ := hub.Log(); ring1 != ring2 || srv.LogFollowers("a") != 1 {
		t.Fatal("re-selecting the same target must not reopen the stream")
	}

	// Switching closes the previous stream and never mixes lines.
	hub.SetLogTarget(b)
	waitFor(t, "tail of b", func() bool { return strings.Join(logLines(), ",") == "b1" })
	waitFor(t, "previous stream closed", func() bool { return srv.LogFollowers("a") == 0 })

	// A stopped container: last output, then the stream ends cleanly.
	hub.SetLogTarget(srv.ContainerID("off"))
	waitFor(t, "ended", func() bool { _, st := hub.Log(); return st.Ended && !st.Streaming })
	if got := logLines(); len(got) != 1 || got[0] != "last words" {
		t.Fatalf("stopped container logs: %q", got)
	}
	waitFor(t, "b closed", func() bool { return srv.LogFollowers("b") == 0 })

	// When the container being shown starts again, following resumes.
	if err := eng.StartContainer(ctx, srv.ContainerID("off")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "follow resumed", func() bool { _, st := hub.Log(); return st.Streaming && srv.LogFollowers("off") == 1 })
	srv.WriteLog("off", 1, "back again")
	waitFor(t, "line after restart", func() bool { return contains(logLines(), "back again") })

	// No target: everything is closed.
	hub.SetLogTarget("")
	waitFor(t, "all closed", func() bool { return srv.LogFollowers("off") == 0 })
	if _, st := hub.Log(); st.Streaming || st.Target != "" {
		t.Fatalf("status with no target: %+v", st)
	}

	// A target that does not exist reports the daemon's error, nothing else breaks.
	hub.SetLogTarget("0000000000000000000000000000000000000000000000000000000000000000")
	waitFor(t, "error status", func() bool { _, st := hub.Log(); return strings.Contains(st.Err, "No such container") })
}

func TestHubLogBufferIsBounded(t *testing.T) {
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "flood", Image: "app:1", LogRate: 200_000})
	})
	hub.SetLogTarget(srv.ContainerID("flood"))
	ring, _ := hub.Log()
	waitFor(t, "buffer full", func() bool { _, end := ring.Bounds(); return end > 3*LogLines })
	if ring.Len() != LogLines || ring.Cap() != LogLines {
		t.Fatalf("buffer holds %d lines, bound is %d", ring.Len(), LogLines)
	}
}

func TestHubStats(t *testing.T) {
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "b", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "off", Image: "app:1", State: "exited"})
	})
	a, b, off := srv.ContainerID("a"), srv.ContainerID("b"), srv.ContainerID("off")

	if st := hub.Stats(a); st.Collected {
		t.Fatal("no stream should exist before SyncStats")
	}
	v0 := hub.StatsVersion()
	hub.SyncStats([]string{a, b}, a)
	waitFor(t, "samples for both", func() bool { return hub.Stats(a).HasSample && hub.Stats(b).HasSample })
	if hub.StatsVersion() == v0 {
		t.Fatal("stats version must advance when samples arrive")
	}
	st := hub.Stats(a)
	if !st.Collected || st.Stale || st.Ended || st.Latest.CPUPercent <= 0 || st.Latest.MemUsed == 0 || st.Latest.MemLimit == 0 {
		t.Fatalf("stats: %+v", st)
	}
	waitFor(t, "history", func() bool { return len(hub.StatsHistory(a, 10)) >= 5 })
	if hub.Stats(off).Collected {
		t.Fatal("a stopped container must not have a statistics stream")
	}

	// One stream stalls; the other keeps delivering.
	srv.StallStats("a", true)
	time.Sleep(50 * time.Millisecond)
	before := hub.Stats(b).Latest.Time
	waitFor(t, "b keeps updating while a is stalled", func() bool { return hub.Stats(b).Latest.Time.After(before) })
	srv.StallStats("a", false)

	// The container stops: the daemon ends the stream; that is not an error.
	srv.Crash("b", 1)
	waitFor(t, "stream ended", func() bool { s := hub.Stats(b); return s.Ended && s.Err == "" })

	// The next sync (with b no longer running) drops its stream.
	hub.SyncStats([]string{a}, a)
	if hub.Stats(b).Collected || hub.StatsStreams() != 1 {
		t.Fatalf("after sync: b collected=%v streams=%d", hub.Stats(b).Collected, hub.StatsStreams())
	}
	hub.SyncStats(nil, "")
	waitFor(t, "all streams closed", func() bool { return hub.StatsStreams() == 0 })
}

func TestHubStatsStreamCap(t *testing.T) {
	const total = MaxStatsStreams + 8
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		for i := 0; i < total; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("c%02d", i), Image: "app:1"})
		}
	})
	var ids []string
	for i := 0; i < total; i++ {
		ids = append(ids, srv.ContainerID(fmt.Sprintf("c%02d", i)))
	}
	selected := ids[total-1] // beyond the cap by position
	hub.SyncStats(ids, selected)
	collected := 0
	for _, id := range ids {
		if hub.Stats(id).Collected {
			collected++
		}
	}
	if collected != MaxStatsStreams {
		t.Fatalf("%d streams for %d running containers, cap is %d", collected, total, MaxStatsStreams)
	}
	if !hub.Stats(selected).Collected {
		t.Fatal("the highlighted container must always be collected")
	}
	// Moving the highlight keeps the cap and follows the highlight.
	other := ids[total-2]
	hub.SyncStats(ids, other)
	collected = 0
	for _, id := range ids {
		if hub.Stats(id).Collected {
			collected++
		}
	}
	if collected != MaxStatsStreams || !hub.Stats(other).Collected {
		t.Fatalf("after moving the highlight: %d streams, highlighted collected=%v", collected, hub.Stats(other).Collected)
	}
}

func TestHubCloseStopsEverything(t *testing.T) {
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1", LogRate: 100})
	})
	a := srv.ContainerID("a")
	hub.SetLogTarget(a)
	hub.SyncStats([]string{a}, a)
	waitFor(t, "streams open", func() bool { return srv.LogFollowers("a") == 1 && hub.Stats(a).HasSample })
	done := make(chan struct{})
	go func() { hub.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return: a stream goroutine is stuck")
	}
	waitFor(t, "daemon sees the streams closed", func() bool { return srv.LogFollowers("a") == 0 && srv.EventSubscribers() == 0 })
}
