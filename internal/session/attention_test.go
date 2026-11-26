package session

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/attention"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// ---- helpers ---------------------------------------------------------------------------

const (
	listPath    = `^/containers/json$`
	inspectPath = `^/containers/[^/]+/json$`
)

func lists(srv *fakeengine.Server) int    { return srv.CountRequests("GET", listPath) }
func inspects(srv *fakeengine.Server) int { return srv.CountRequests("GET", inspectPath) }

// fastAttention is a run whose gap is short enough for a test to wait out.
func fastAttention(t *testing.T, s *Session, cfg AttentionConfig) *Attention {
	t.Helper()
	if cfg.Gap == 0 {
		cfg.Gap = 30 * time.Millisecond
	}
	a := s.OpenAttention(cfg)
	t.Cleanup(func() { a.Close(); a.Wait() })
	return a
}

// pump calls Sync, as the frame tick does, until cond holds.
func pump(t *testing.T, a *Attention, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		a.Sync(time.Now())
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func blockOf(a *Attention, m *Member) AttentionBlock {
	for _, b := range a.Blocks(0) {
		if b.Member == m {
			return b
		}
	}
	return AttentionBlock{}
}

// settledBlock reports whether m's block has a list and nothing on its way.
func settledBlock(a *Attention, m *Member) bool {
	b := blockOf(a, m)
	return b.Loaded && !b.Reading && b.Block.Counts.Reading == 0
}

func rowNames(b AttentionBlock) []string {
	var out []string
	for _, r := range b.Block.Rows {
		out = append(out, r.Name)
	}
	return out
}

func troubled(name string) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: name + "-ok", Image: "app:1", Health: "healthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: name + "-nocheck", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: name + "-failed", Image: "app:1", State: "exited", ExitCode: 3})
		s.AddContainer(fakeengine.ContainerSpec{Name: name + "-done", Image: "app:1", State: "exited"})
		s.AddContainer(fakeengine.ContainerSpec{Name: name + "-sick", Image: "app:1", Health: "unhealthy"})
	}
}

func seededFake(t *testing.T, name string, seed func(*fakeengine.Server)) *fakeengine.Server {
	t.Helper()
	srv := fakeengine.New()
	srv.Name = name
	srv.StatsInterval = 15 * time.Millisecond
	seed(srv)
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

// two is a session of two connected fake daemons, a focused.
func two(t *testing.T, readOnly bool) (s *Session, a, b *fakeengine.Server, ma, mb *Member) {
	t.Helper()
	a, b = seededFake(t, "a", troubled("a")), seededFake(t, "b", troubled("b"))
	s = newSession(t, Config{ReadOnly: readOnly})
	ma = launch(t, s, a, readOnly)
	mb = add(t, s, endpoint(b.Host(), "b"))
	connected(t, mb)
	waitFor(t, "b's event stream", func() bool { return b.EventSubscribers() == 1 })
	return s, a, b, ma, mb
}

// ---- 2.1 the read run -------------------------------------------------------------------

// Both daemons are read: one list each, then inspects only of the
// candidates; the answers are classified per member.
func TestAttentionReadsEveryConnectedMember(t *testing.T) {
	s, a, b, ma, mb := two(t, false)
	a.ResetRequests()
	b.ResetRequests()
	at := fastAttention(t, s, AttentionConfig{})
	if lists(a)+lists(b) != 0 {
		t.Fatal("opening sent something before the first Sync")
	}
	pump(t, at, "both blocks", func() bool { return settledBlock(at, ma) && settledBlock(at, mb) })

	for _, x := range []struct {
		srv *fakeengine.Server
		mem *Member
		p   string
	}{{a, ma, "a"}, {b, mb, "b"}} {
		blk := blockOf(at, x.mem)
		want := attention.Counts{Total: 5, NeedsYou: 2, Fine: 1, Unknown: 1, NotAProblem: 1}
		if blk.Block.Counts != want {
			t.Fatalf("%s: counts %+v, want %+v", x.p, blk.Block.Counts, want)
		}
		if got := strings.Join(rowNames(blk), ","); got != x.p+"-sick,"+x.p+"-failed" {
			t.Fatalf("%s: rows %s", x.p, got)
		}
		if r := blk.Block.Rows[1]; !r.HasDetail || r.Detail.ExitCode != 3 || r.Detail.FinishedAt.IsZero() {
			t.Fatalf("%s: the failed exit has no reason: %+v", x.p, r)
		}
		if blk.ReadAt.IsZero() || blk.Err != nil || blk.Seeded {
			t.Fatalf("%s: block %+v", x.p, blk)
		}
		// One list; an inspect for each candidate (two exited, two running
		// that are not unhealthy) and none for the unhealthy one.
		if n := lists(x.srv); n != 1 {
			t.Fatalf("%s: %d list requests", x.p, n)
		}
		if n := inspects(x.srv); n != 4 {
			t.Fatalf("%s: %d inspects, want 4", x.p, n)
		}
		if n := len(x.srv.Mutations()); n != 0 {
			t.Fatalf("%s: %d mutations", x.p, n)
		}
	}
	// The focused member comes first when asked for.
	if bs := at.Blocks(mb.ID()); len(bs) != 2 || bs[0].Member != mb || bs[1].Member != ma {
		t.Fatal("Blocks does not put the given member first")
	}
}

// One member's list fails: its block says why, the other's is whole.
func TestAttentionOneMemberFailsTheRestStaysUsable(t *testing.T) {
	s, a, b, ma, mb := two(t, false)
	b.FailNext("GET", listPath, 500, "the list is on fire", -1)
	at := fastAttention(t, s, AttentionConfig{})
	pump(t, at, "a read and b failed", func() bool {
		return settledBlock(at, ma) && blockOf(at, mb).Err != nil && !blockOf(at, mb).Reading
	})
	bb := blockOf(at, mb)
	if bb.Loaded || len(bb.Block.Rows) != 0 || !strings.Contains(bb.Err.Error(), "the list is on fire") || bb.ErrAt.IsZero() {
		t.Fatalf("b's block: %+v", bb)
	}
	if ba := blockOf(at, ma); ba.Block.Counts.NeedsYou != 2 || ba.Err != nil {
		t.Fatalf("a's block: %+v", ba)
	}
	_ = a

	// A failed re-read keeps the rows it had, with the time they were read.
	b.ClearFailures()
	at.Refresh()
	pump(t, at, "b read", func() bool { return settledBlock(at, mb) && blockOf(at, mb).Err == nil })
	read := blockOf(at, mb).ReadAt
	b.FailNext("GET", listPath, 500, "again", -1)
	at.Refresh()
	pump(t, at, "b's re-read failed", func() bool { return blockOf(at, mb).Err != nil && !blockOf(at, mb).Reading })
	bb = blockOf(at, mb)
	if !bb.Loaded || bb.Block.Counts.NeedsYou != 2 || !bb.ReadAt.Equal(read) || !bb.ErrAt.After(read) {
		t.Fatalf("after a failed re-read: %+v", bb)
	}
}

// Closing the run stops its reads: nothing more is sent, and its goroutines
// end.
func TestAttentionCloseStopsReads(t *testing.T) {
	a := seededFake(t, "a", func(s *fakeengine.Server) {
		for i := 0; i < 40; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("x%02d", i), Image: "app:1", State: "exited", ExitCode: 1})
		}
	})
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	a.ResetRequests()
	a.Stall("GET", inspectPath, 40*time.Millisecond)
	before := runtime.NumGoroutine()
	at := s.OpenAttention(AttentionConfig{Workers: 2})
	pump(t, at, "the first inspects", func() bool { return inspects(a) >= 2 })
	if !blockOf(at, ma).Reading || !at.Reading() {
		t.Fatal("not reading mid-run")
	}
	at.Close()
	at.Wait()
	sent := inspects(a)
	if sent >= 40 {
		t.Fatalf("every inspect was sent (%d) before the close", sent)
	}
	ver := at.Version()
	time.Sleep(150 * time.Millisecond)
	at.Sync(time.Now()) // a closed run starts nothing
	at.Refresh()
	at.Sync(time.Now())
	time.Sleep(50 * time.Millisecond)
	if n := inspects(a); n != sent {
		t.Fatalf("%d inspects were sent after the close", n-sent)
	}
	if lists(a) != 1 || at.Version() != ver {
		t.Fatalf("lists %d, version moved %v", lists(a), at.Version() != ver)
	}
	waitFor(t, "the run's goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
	at.Close() // closing twice is harmless
}

// The session's end ends an open run.
func TestSessionCloseEndsAttention(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := New(Config{SetupHub: fastHubs})
	launch(t, s, a, false)
	a.Stall("GET", inspectPath, 200*time.Millisecond)
	at := s.OpenAttention(AttentionConfig{})
	at.Sync(time.Now())
	// Its inspects are with the daemon: the read is under way in earnest.
	waitFor(t, "a read under way", func() bool { return at.Reading() && inspects(a) > 0 })
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	at.Wait()
	// A run opened on a closed session reads nothing.
	late := s.OpenAttention(AttentionConfig{})
	a.ResetRequests()
	late.Sync(time.Now())
	time.Sleep(30 * time.Millisecond)
	if lists(a) != 0 {
		t.Fatal("a run on a closed session read a daemon")
	}
}

// ---- 2.2 the focused daemon's block ----------------------------------------------------

// A seeded member's block is whole before any request: its list is the one
// handed over, and its read asks for the details only.
func TestAttentionSeedIsInstantAndAsksNoList(t *testing.T) {
	s, a, b, ma, mb := two(t, false)
	eng := ma.Engine()
	held, err := eng.Containers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	heldAt := time.Now().Add(-7 * time.Second)
	a.ResetRequests()
	b.ResetRequests()
	b.Stall("GET", listPath, 300*time.Millisecond)

	at := fastAttention(t, s, AttentionConfig{})
	at.Seed(ma, held, heldAt)
	// Before any Sync — before anything is sent anywhere.
	ba := blockOf(at, ma)
	if !ba.Loaded || !ba.Seeded || !ba.ReadAt.Equal(heldAt) || ba.Block.Counts.Total != 5 {
		t.Fatalf("the seeded block: %+v", ba)
	}
	// What the list alone says is already there; what needs an inspect is
	// being read, and counted as neither.
	if got := strings.Join(rowNames(ba), ","); got != "a-sick,a-done,a-failed" {
		t.Fatalf("rows before the details: %s", got)
	}
	if c := ba.Block.Counts; c.NeedsYou != 1 || c.Reading != 4 || c.Fine != 0 || c.NotAProblem != 0 {
		t.Fatalf("counts before the details: %+v", c)
	}
	if lists(a)+inspects(a)+lists(b) != 0 {
		t.Fatal("a request was sent before Sync")
	}

	pump(t, at, "a's details", func() bool { return settledBlock(at, ma) })
	if blockOf(at, mb).Loaded {
		t.Fatal("b answered before a's block was whole: the stall did not hold")
	}
	if lists(a) != 0 || inspects(a) != 4 {
		t.Fatalf("a: %d lists, %d inspects", lists(a), inspects(a))
	}
	if ba = blockOf(at, ma); ba.Block.Counts.NeedsYou != 2 || !ba.Seeded || !ba.ReadAt.Equal(heldAt) {
		t.Fatalf("a's settled block: %+v", ba)
	}
	pump(t, at, "b", func() bool { return settledBlock(at, mb) })

	// r reads the seeded member itself: a list, and a new read time.
	at.Refresh()
	pump(t, at, "a re-read", func() bool { return lists(a) == 1 && settledBlock(at, ma) })
	if ba = blockOf(at, ma); ba.Seeded || !ba.ReadAt.After(heldAt) {
		t.Fatalf("after r: %+v", ba)
	}
	// A seed for a member already read changes nothing.
	at.Seed(ma, nil, time.Now())
	if blockOf(at, ma).Block.Counts.Total != 5 {
		t.Fatal("a late seed replaced what was read")
	}
	at.Seed(nil, nil, time.Now())
}

// ---- 2.3 the bound ---------------------------------------------------------------------

func TestAttentionStopsAtTheBound(t *testing.T) {
	a := seededFake(t, "a", func(s *fakeengine.Server) {
		for i := 0; i < 9; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("failed%d", i), Image: "app:1", State: "exited", ExitCode: 2})
		}
		for i := 0; i < 6; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("up%d", i), Image: "app:1", Health: "healthy"})
		}
		s.AddContainer(fakeengine.ContainerSpec{Name: "looping", Image: "app:1", State: "restarting"})
	})
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	a.ResetRequests()
	a.Stall("GET", inspectPath, 15*time.Millisecond)
	at := fastAttention(t, s, AttentionConfig{MaxInspects: 5, Workers: 1})

	// Rows fill as answers arrive: partial results are seen on the way.
	partial := false
	pump(t, at, "the bound", func() bool {
		b := blockOf(at, ma)
		if n := b.Block.Counts.NeedsYou; b.Reading && n > 1 && n < 6 {
			partial = true
		}
		return b.Loaded && !b.Reading
	})
	if !partial {
		t.Fatal("no partial answer was ever visible")
	}
	b := blockOf(at, ma)
	// Five exited containers were inspected; four exited and six running
	// were not, and are in no class. The restarting one needs no inspect to
	// be listed.
	want := attention.Counts{Total: 16, NeedsYou: 6, NotInspected: 10}
	if b.Block.Counts != want || b.Bound != 5 {
		t.Fatalf("counts %+v (bound %d), want %+v", b.Block.Counts, b.Bound, want)
	}
	if n := inspects(a); n != 5 {
		t.Fatalf("%d inspects for a bound of 5", n)
	}
	if b.Block.Rows[0].Name != "looping" || b.Block.Rows[0].HasDetail {
		t.Fatalf("first row: %+v", b.Block.Rows[0])
	}
}

// An inspect that fails leaves its container unclassified, and the rest read.
func TestAttentionInspectFailureIsCounted(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	a.FailNext("GET", `/containers/`+a.ContainerID("a-failed")+`/json$`, 500, "no", -1)
	at := fastAttention(t, s, AttentionConfig{})
	pump(t, at, "the read", func() bool { b := blockOf(at, ma); return b.Loaded && !b.Reading })
	want := attention.Counts{Total: 5, NeedsYou: 1, Fine: 1, Unknown: 1, NotAProblem: 1, InspectFailed: 1}
	if c := blockOf(at, ma).Block.Counts; c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

// ---- 2.4 events, the debounce and the quiet view ---------------------------------------

func TestAttentionRereadsOnTellingEventsOnly(t *testing.T) {
	s, a, b, _, mb := two(t, false)
	at := fastAttention(t, s, AttentionConfig{Gap: 40 * time.Millisecond})
	pump(t, at, "first read", func() bool { return settledBlock(at, mb) })
	first := blockOf(at, mb).ReadAt
	a.ResetRequests()
	b.ResetRequests()

	// Events that are not an exit, an OOM kill or a health change: nothing.
	b.Emit("container", "start", b.ContainerID("b-ok"), map[string]string{"name": "b-ok"})
	b.Emit("container", "exec_start: sh", b.ContainerID("b-ok"), map[string]string{"name": "b-ok"})
	b.Emit("image", "pull", "busybox:latest", map[string]string{"name": "busybox"})
	b.Emit("network", "die", "n1", nil) // the action alone is not enough
	waitFor(t, "the events to arrive", func() bool { _, end := mb.Hub().Events.Bounds(); return end >= 4 })
	for i := 0; i < 30; i++ {
		at.Sync(time.Now())
		time.Sleep(3 * time.Millisecond)
	}
	if lists(b) != 0 || inspects(b) != 0 {
		t.Fatalf("an untelling event caused %d lists, %d inspects", lists(b), inspects(b))
	}

	// A crash on the background daemon: read again, read time advances.
	b.Crash("b-ok", 9)
	pump(t, at, "the re-read after die", func() bool {
		blk := blockOf(at, mb)
		return settledBlock(at, mb) && blk.ReadAt.After(first) && blk.Block.Counts.NeedsYou == 3
	})
	if lists(b) != 1 {
		t.Fatalf("%d lists for one event", lists(b))
	}
	if lists(a) != 0 {
		t.Fatal("the other daemon was read for b's event")
	}

	// A health change, and an OOM kill.
	second := blockOf(at, mb).ReadAt
	b.SetHealth("b-sick", "healthy")
	pump(t, at, "the re-read after health_status", func() bool {
		blk := blockOf(at, mb)
		return settledBlock(at, mb) && blk.ReadAt.After(second) && blk.Block.Counts.NeedsYou == 2
	})
	b.OOMKill("b-nocheck")
	pump(t, at, "the re-read after oom", func() bool {
		blk := blockOf(at, mb)
		return settledBlock(at, mb) && blk.Block.Counts.NeedsYou == 3
	})
	var oom *attention.Row
	blk := blockOf(at, mb)
	for i, r := range blk.Block.Rows {
		if r.Name == "b-nocheck" {
			oom = &blk.Block.Rows[i]
		}
	}
	if oom == nil || !oom.Detail.OOMKilled || oom.Detail.ExitCode != 137 {
		t.Fatalf("the OOM-killed row: %+v", oom)
	}
	// Newest failure first.
	if blk.Block.Rows[0].Name != "b-nocheck" {
		t.Fatalf("rows: %v", rowNames(blk))
	}
}

// A burst of events is one read per member per gap, not one per event.
func TestAttentionDebouncesABurst(t *testing.T) {
	a := seededFake(t, "a", func(s *fakeengine.Server) {
		for i := 0; i < 30; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("c%02d", i), Image: "app:1"})
		}
	})
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	const gap = 200 * time.Millisecond
	at := fastAttention(t, s, AttentionConfig{Gap: gap})
	pump(t, at, "first read", func() bool { return settledBlock(at, ma) })
	a.ResetRequests()
	start := time.Now()
	for i := 0; i < 30; i++ {
		a.Crash(fmt.Sprintf("c%02d", i), 1)
		at.Sync(time.Now())
		time.Sleep(10 * time.Millisecond) // 300 ms of exits
	}
	for time.Since(start) < 500*time.Millisecond {
		at.Sync(time.Now())
		time.Sleep(3 * time.Millisecond)
	}
	// At most one read per gap begun, plus the one under way at the start.
	limit := int(time.Since(start)/gap) + 1
	if n := lists(a); n < 1 || n > limit {
		t.Fatalf("%d list requests in %s with a gap of %s (limit %d)", n, time.Since(start).Round(time.Millisecond), gap, limit)
	}
	pump(t, at, "the last read", func() bool { return settledBlock(at, ma) && blockOf(at, ma).Block.Counts.NeedsYou == 30 })
}

// A view left open on a quiet session sends nothing at all.
func TestAttentionQuietViewSendsNothing(t *testing.T) {
	s, a, b, ma, mb := two(t, false)
	at := fastAttention(t, s, AttentionConfig{Gap: 10 * time.Millisecond})
	pump(t, at, "first read", func() bool { return settledBlock(at, ma) && settledBlock(at, mb) })
	a.ResetRequests()
	b.ResetRequests()
	ver := at.Version()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		at.Sync(time.Now())
		time.Sleep(2 * time.Millisecond)
	}
	for _, srv := range []*fakeengine.Server{a, b} {
		for _, r := range srv.Requests() {
			if !strings.HasSuffix(r.Path, "/_ping") {
				t.Fatalf("%s received %s %s while nothing happened", srv.Name, r.Method, r.Path)
			}
		}
	}
	if at.Version() != ver {
		t.Fatal("the version moved while nothing happened")
	}
	// Until r.
	at.Refresh()
	pump(t, at, "r", func() bool { return lists(a) == 1 && lists(b) == 1 })
}

// ---- 2.5 a member removed or lost mid-read ---------------------------------------------

func TestAttentionMemberRemovedMidRead(t *testing.T) {
	s, a, b, ma, mb := two(t, false)
	b.ResetRequests()
	b.Stall("GET", listPath, 300*time.Millisecond)
	at := fastAttention(t, s, AttentionConfig{})
	pump(t, at, "a read, b's under way", func() bool { return settledBlock(at, ma) && blockOf(at, mb).Reading })
	if _, err := s.Remove(mb.ID()); err != nil {
		t.Fatal(err)
	}
	at.Sync(time.Now())
	bs := at.Blocks(ma.ID())
	if len(bs) != 1 || bs[0].Member != ma || bs[0].Block.Counts.NeedsYou != 2 {
		t.Fatalf("after the removal: %d blocks", len(bs))
	}
	if at.Reading() {
		t.Fatal("the removed member's read is still counted as under way")
	}
	<-mb.Closed()
	// Its late answer changes nothing, and no inspect follows it.
	time.Sleep(350 * time.Millisecond)
	at.Sync(time.Now())
	if n := inspects(b); n != 0 {
		t.Fatalf("%d inspects were sent to the removed daemon", n)
	}
	if bs = at.Blocks(ma.ID()); len(bs) != 1 {
		t.Fatal("the removed member came back")
	}
	_ = a
}

func TestAttentionMemberDisconnectedMidRead(t *testing.T) {
	s, _, b, ma, mb := two(t, false)
	b.Stall("GET", inspectPath, 100*time.Millisecond)
	at := fastAttention(t, s, AttentionConfig{Workers: 1})
	pump(t, at, "b's list in, details under way", func() bool {
		blk := blockOf(at, mb)
		return settledBlock(at, ma) && blk.Loaded && blk.Reading
	})
	b.Stop()
	pump(t, at, "b seen disconnected", func() bool { return blockOf(at, mb).State == Disconnected })
	blk := blockOf(at, mb)
	if blk.Loaded || blk.Reading || len(blk.Block.Rows) != 0 || blk.Block.Counts.Total != 0 || blk.Reason == "" {
		t.Fatalf("the disconnected block: %+v", blk)
	}
	if ba := blockOf(at, ma); ba.Block.Counts.NeedsYou != 2 {
		t.Fatalf("a's block suffered: %+v", ba)
	}
	if at.Reading() {
		t.Fatal("still reading a daemon that is gone")
	}

	// It comes back: read once, without being asked.
	b.ClearFailures()
	if err := b.Restart(); err != nil {
		t.Fatal(err)
	}
	pump(t, at, "b read again after reconnecting", func() bool {
		return blockOf(at, mb).State == Connected && settledBlock(at, mb)
	})
	if c := blockOf(at, mb).Block.Counts; c.NeedsYou != 2 || c.Total != 5 {
		t.Fatalf("after the reconnect: %+v", c)
	}
}

// A member that never connected is a block with its reason and no rows.
func TestAttentionUnreachableMemberHasNoRows(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	gone := add(t, s, endpoint("unix:///nonexistent/attention.sock", "gone"))
	waitFor(t, "the reason", func() bool { return gone.Reason() != "" })
	at := fastAttention(t, s, AttentionConfig{})
	pump(t, at, "a", func() bool { return settledBlock(at, ma) })
	bs := at.Blocks(ma.ID())
	if len(bs) != 2 || bs[1].Member != gone || bs[1].State != Disconnected || bs[1].Reason == "" || bs[1].Loaded {
		t.Fatalf("blocks: %+v", bs)
	}
}

// ---- read-only ------------------------------------------------------------------------

// In a read-only session the run reads as in any other, and every request
// either daemon receives is a GET or a HEAD.
func TestAttentionReadOnlySessionSendsOnlyReads(t *testing.T) {
	s, a, b, ma, mb := two(t, true)
	if !engine.IsReadOnly(ma.Engine()) || !engine.IsReadOnly(mb.Engine()) {
		t.Fatal("the session is not read-only")
	}
	a.ResetRequests()
	b.ResetRequests()
	at := fastAttention(t, s, AttentionConfig{})
	pump(t, at, "both blocks", func() bool { return settledBlock(at, ma) && settledBlock(at, mb) })
	b.Crash("b-ok", 1)
	pump(t, at, "the re-read", func() bool { return blockOf(at, mb).Block.Counts.NeedsYou == 3 })
	for _, srv := range []*fakeengine.Server{a, b} {
		if lists(srv) == 0 {
			t.Fatalf("%s was not read", srv.Name)
		}
		for _, r := range srv.Requests() {
			if r.Method != "GET" && r.Method != "HEAD" {
				t.Fatalf("%s received %s %s", srv.Name, r.Method, r.Path)
			}
			ok := strings.HasSuffix(r.Path, "/_ping") || strings.HasSuffix(r.Path, "/events") ||
				strings.HasSuffix(r.Path, "/containers/json") || strings.Contains(r.Path, "/containers/") && strings.HasSuffix(r.Path, "/json")
			if !ok {
				t.Fatalf("%s received a request the view has no business sending: %s %s", srv.Name, r.Method, r.Path)
			}
		}
	}
}

// ---- what the review asked for (docs/DECISIONS.md, AT16) --------------------------------

// A daemon's list is bounded, one container per ID, each string bounded.
func TestConvertContainersBoundsADaemonsList(t *testing.T) {
	long := strings.Repeat("é", 400) // two bytes each
	in := []engine.Container{
		{ID: "a", Name: "first", State: "running"},
		{ID: "a", Name: "again", State: "exited"}, // the same ID twice
		{ID: "", Name: "no-id"},
		{ID: "b", Name: long, Image: long, Status: long, Health: long, State: long,
			Labels: map[string]string{engine.LabelComposeProject: long, engine.LabelComposeService: long}},
		{ID: "c", Name: "third"},
		{ID: "d", Name: "fourth"},
		{ID: "d", Name: "fourth again"},
	}
	out, beyond := convertContainers(in, 3)
	if len(out) != 3 || beyond != 1 {
		t.Fatalf("%d kept, %d beyond", len(out), beyond)
	}
	if out[0].Name != "first" || out[1].ID != "b" || out[2].ID != "c" {
		t.Fatalf("kept %+v", out)
	}
	for _, f := range []string{out[1].Name, out[1].Image, out[1].Status, out[1].Health, out[1].State, out[1].Project, out[1].Service} {
		if len(f) > attentionFieldMax+4 || !strings.HasSuffix(f, "…") || !strings.HasPrefix(f, "é") || strings.ContainsRune(f, '�') {
			t.Fatalf("a field of %d bytes: %q…", len(f), f[:8])
		}
	}
	if out, beyond := convertContainers(nil, 10); len(out) != 0 || beyond != 0 {
		t.Fatal("an empty list")
	}
	if got := clipField("short"); got != "short" {
		t.Fatalf("%q", got)
	}
}

func TestAttentionListBeyondTheBoundIsCounted(t *testing.T) {
	a := seededFake(t, "a", func(s *fakeengine.Server) {
		for i := 0; i < 9; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("c%d", i), Image: "app:1", State: "dead"})
		}
	})
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	at := fastAttention(t, s, AttentionConfig{MaxContainers: 4})
	pump(t, at, "the read", func() bool { return settledBlock(at, ma) })
	b := blockOf(at, ma)
	if b.Beyond != 5 || b.ListBound != 4 || b.Block.Counts.Total != 4 || b.Block.Counts.NeedsYou != 4 {
		t.Fatalf("%+v", b)
	}
	// The same for a list the desk hands over.
	held, err := ma.Engine().Containers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	at2 := fastAttention(t, s, AttentionConfig{MaxContainers: 4})
	at2.Seed(ma, held, time.Now())
	if b := blockOf(at2, ma); b.Beyond != 5 || b.Block.Counts.Total != 4 {
		t.Fatalf("seeded: %+v", b)
	}
	pump(t, at2, "the seeded read", func() bool { return settledBlock(at2, ma) })
	if b := blockOf(at2, ma); b.Beyond != 5 {
		t.Fatalf("after the seeded read: %+v", b)
	}
}

// What was read about a container is not carried into a re-read once an
// event has named it: a container that exited 0, ran again and exited 1 is
// being read — never "not a problem" on the old code — until its inspect is
// in. What no event named keeps its reason, so the rows do not blink.
func TestAttentionDoesNotClassifyOnAStaleDetail(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	at := fastAttention(t, s, AttentionConfig{Workers: 1})
	pump(t, at, "first read", func() bool { return settledBlock(at, ma) })
	if c := blockOf(at, ma).Block.Counts; c.NotAProblem != 1 || c.NeedsYou != 2 {
		t.Fatalf("before: %+v", c)
	}
	first := blockOf(at, ma).ReadAt

	// a-done exited 0. It runs again, and this time fails.
	a.Stall("GET", inspectPath, 150*time.Millisecond)
	if err := ma.Engine().StartContainer(t.Context(), a.ContainerID("a-done")); err != nil {
		t.Fatal(err)
	}
	a.Crash("a-done", 1)
	sawReading := false
	pump(t, at, "the re-read", func() bool {
		b := blockOf(at, ma)
		if !b.ReadAt.After(first) {
			return false
		}
		for _, r := range b.Block.Rows {
			if r.Name == "a-done" && r.Class == attention.Reading {
				sawReading = true
				// While it is read: no longer "not a problem", not yet
				// "needs you"; and a-failed, which nothing happened to,
				// still has its reason.
				if b.Block.Counts.NotAProblem != 0 {
					t.Fatalf("the old exit code still counts: %+v", b.Block.Counts)
				}
				kept := false
				for _, o := range b.Block.Rows {
					if o.Name == "a-failed" && o.Class == attention.NeedsYou && o.HasDetail && o.Detail.ExitCode == 3 {
						kept = true
					}
				}
				if !kept {
					t.Fatalf("an untouched container lost its reason during the re-read: %v", rowNames(b))
				}
			}
		}
		return settledBlock(at, ma)
	})
	if !sawReading {
		t.Fatal("a-done was never seen being read: the test proves nothing")
	}
	b := blockOf(at, ma)
	if c := b.Block.Counts; c.NeedsYou != 3 || c.NotAProblem != 0 {
		t.Fatalf("after: %+v", c)
	}
}

// A container that changes between the list and its inspect is in no class,
// and the daemon is read once more — once, not for ever.
func TestAttentionListAndInspectDisagree(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := newSession(t, Config{})
	ma := launch(t, s, a, false)
	a.ResetRequests()
	a.Stall("GET", inspectPath, 120*time.Millisecond)
	at := fastAttention(t, s, AttentionConfig{Workers: 4})
	pump(t, at, "the list", func() bool { return blockOf(at, ma).Loaded && inspects(a) > 0 })
	// The list said running; by the time the inspect answers it is paused.
	// A pause is not an event the view re-reads for.
	if err := ma.Engine().PauseContainer(t.Context(), a.ContainerID("a-ok")); err != nil {
		t.Fatal(err)
	}
	pump(t, at, "the follow-up read", func() bool { return lists(a) == 2 && settledBlock(at, ma) })
	if c := blockOf(at, ma).Block.Counts; c.NotAProblem != 2 || c.Fine != 0 || c.InspectFailed != 0 {
		t.Fatalf("after the follow-up: %+v", c)
	}
	// Nothing more follows.
	time.Sleep(150 * time.Millisecond)
	for i := 0; i < 20; i++ {
		at.Sync(time.Now())
		time.Sleep(3 * time.Millisecond)
	}
	if n := lists(a); n != 2 {
		t.Fatalf("%d list requests: the follow-up asked for another", n)
	}
}

// A run closed by its view is still waited for by a session that closes
// right behind it.
func TestSessionCloseWaitsForAClosedRun(t *testing.T) {
	a := seededFake(t, "a", troubled("a"))
	s := New(Config{SetupHub: fastHubs})
	launch(t, s, a, false)
	a.Stall("GET", inspectPath, 300*time.Millisecond)
	at := s.OpenAttention(AttentionConfig{})
	at.Sync(time.Now())
	waitFor(t, "a read under way", func() bool { return inspects(a) > 0 })
	at.Close() // as the desk does on its way out
	s.mu.Lock()
	known := len(s.attention)
	s.mu.Unlock()
	if known != 1 {
		t.Fatalf("a run with reads under way is no longer known to the session (%d)", known)
	}
	s.Close()
	done := make(chan struct{})
	go func() { at.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("the session closed before the run's reads had ended")
	}
	waitFor(t, "the run forgotten", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.attention) == 0 })
}
