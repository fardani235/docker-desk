package live

import (
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// A background daemon's hub holds its event stream and nothing else: asked
// for statistics and logs, it opens neither.
func TestEventsOnlyHubOpensNoStatsOrLogStreams(t *testing.T) {
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1", LogRate: 50})
		s.AddContainer(fakeengine.ContainerSpec{Name: "b", Image: "app:1"})
	})
	a, b := srv.ContainerID("a"), srv.ContainerID("b")
	hub.SetMode(ModeEventsOnly)
	if hub.Mode() != ModeEventsOnly {
		t.Fatalf("mode is %s", hub.Mode())
	}
	hub.SyncStats([]string{a, b}, a)
	hub.SetLogTarget(a)
	time.Sleep(80 * time.Millisecond)
	if n := srv.CountRequests("GET", `/containers/[^/]+/stats`); n != 0 {
		t.Fatalf("%d statistics requests from an events-only hub", n)
	}
	if n := srv.CountRequests("GET", `/containers/[^/]+/logs`); n != 0 {
		t.Fatalf("%d log requests from an events-only hub", n)
	}
	if hub.StatsStreams() != 0 || hub.Stats(a).Collected {
		t.Fatal("the hub reports statistics streams it should not have")
	}
	if _, st := hub.Log(); st.Streaming || st.Target != "" {
		t.Fatalf("log status %+v", st)
	}
	// The event stream is still there, and still records.
	if srv.EventSubscribers() != 1 {
		t.Fatalf("%d event subscribers, want 1", srv.EventSubscribers())
	}
	srv.Crash("b", 3)
	waitFor(t, "the die event", func() bool { return contains(eventLines(hub), "container die b") })
}

// Focus arriving opens the streams; focus leaving closes them on the daemon,
// and leaves the event stream alone.
func TestHubModeSwitchClosesStreams(t *testing.T) {
	srv, _, hub := startHub(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1", LogRate: 50})
	})
	a := srv.ContainerID("a")
	hub.SyncStats([]string{a}, a)
	hub.SetLogTarget(a)
	waitFor(t, "streams open", func() bool { return srv.LogFollowers("a") == 1 && srv.StatsStreams() == 1 })

	v := hub.StatsVersion()
	hub.SetMode(ModeEventsOnly)
	waitFor(t, "streams closed on the daemon", func() bool { return srv.LogFollowers("a") == 0 && srv.StatsStreams() == 0 })
	if hub.StatsVersion() == v {
		t.Fatal("closing the statistics streams must be visible to the view")
	}
	if srv.EventSubscribers() != 1 {
		t.Fatal("the event stream was closed with the others")
	}

	hub.SetMode(ModeFull)
	hub.SyncStats([]string{a}, a)
	hub.SetLogTarget(a)
	waitFor(t, "streams open again", func() bool { return srv.LogFollowers("a") == 1 && srv.StatsStreams() == 1 })
}

// A hub for a daemon added while it was down starts disconnected, and the
// first answer connects it.
func TestHubMarkDown(t *testing.T) {
	_, eng, _ := startHub(t, nil)
	h := NewHub(eng)
	h.MinBackoff, h.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
	h.MarkDown()
	if h.Connected() {
		t.Fatal("MarkDown left the hub connected")
	}
	h.Start()
	defer h.Close()
	// The daemon is in fact up: the stream opens, and since it was never
	// connected there is no "interrupted" marker either.
	waitFor(t, "connected", h.Connected)
	for _, l := range eventLines(h) {
		if l == "# event stream interrupted" {
			t.Fatalf("a marker for an interruption that never happened: %v", eventLines(h))
		}
	}
}
