package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/session"
)

// ---- helpers -----------------------------------------------------------------------

// multiBench is a bench whose picker lists the contexts in ctxs, which a test
// fills as it starts more fake daemons.
type multiBench struct {
	*bench
	ctxs []dockerctx.Context
}

func newMultiBench(t *testing.T, o benchOpts) *multiBench {
	t.Helper()
	mb := &multiBench{}
	o.contexts = func() []dockerctx.Context { return mb.ctxs }
	mb.bench = newBench(t, o)
	return mb
}

// daemon starts another fake daemon and lists it in the picker as a context
// of that name.
func (b *multiBench) daemon(name string, seed func(*fakeengine.Server)) *fakeengine.Server {
	b.t.Helper()
	srv := fakeengine.New()
	srv.Name = name
	srv.StatsInterval = 20 * time.Millisecond
	srv.PullStepDelay = 2 * time.Millisecond
	if seed != nil {
		seed(srv)
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(cleanup)
	b.context(name, srv.Host())
	return srv
}

func (b *multiBench) context(name, host string) {
	b.ctxs = append(b.ctxs, dockerctx.Context{Name: name, Endpoint: dockerctx.Endpoint{Host: host, Context: name, Source: "test"}})
}

func containers(names ...string) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		for _, n := range names {
			s.AddContainer(fakeengine.ContainerSpec{Name: n, Image: "app:1"})
		}
	}
}

// pickerTo moves the picker's highlight to the entry with the given key.
func (b *multiBench) pickerTo(key string) {
	b.t.Helper()
	if b.m.dlg == nil || b.m.dlg.kind != dlgDaemons {
		b.keys(keyDaemons)
	}
	b.keys("g")
	for i := 0; i < 50 && b.m.dlg.dm.selKey != key; i++ {
		b.keys("down")
	}
	if b.m.dlg.dm.selKey != key {
		b.t.Fatalf("no picker entry %q\n%s", key, b.screen())
	}
}

// add adds a context through the picker and waits for its member to connect.
func (b *multiBench) add(name string, srv *fakeengine.Server) *session.Member {
	b.t.Helper()
	b.pickerTo("c:" + name)
	b.keys("enter")
	var mem *session.Member
	b.until(name+" to join and connect", func() bool {
		for _, m := range b.m.sess.Members() {
			if m.Name() == name && m.State() == session.Connected {
				mem = m
			}
		}
		return mem != nil && (srv == nil || srv.EventSubscribers() == 1)
	})
	b.keys("esc")
	return mem
}

func (b *multiBench) focusName(name string) {
	b.t.Helper()
	for i := 0; i < 10 && (b.m.cur == nil || b.m.cur.Name() != name); i++ {
		b.keys(keyNextFocus)
	}
	if b.m.cur == nil || b.m.cur.Name() != name {
		b.t.Fatalf("could not focus %s", name)
	}
}

// ---- 3.2 focus change -------------------------------------------------------------------

// Moving focus from one daemon to another shows only the second one's
// objects — from the first frame after the key on, and also when an answer
// from the first daemon arrives late.
func TestFocusChangeShowsOnlyTheNewDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: containers("alpha-web", "alpha-db")})
	beta := b.daemon("beta", containers("beta-web", "beta-db", "beta-cache"))
	b.selectRow("alpha-web")
	b.keys("]", "]") // Logs, so that a log stream is open
	b.see("[Logs]")
	b.until("alpha's streams", func() bool { return b.srv.StatsStreams() == 2 && b.srv.LogFollowers("alpha-web") == 1 })
	mem := b.add("beta", beta)

	// alpha's container list is slow to answer: one is on its way when
	// focus moves.
	b.srv.Stall("GET", "/containers/json", 300*time.Millisecond)
	b.keys("r")
	b.run(30 * time.Millisecond)
	b.keys(keyNextFocus)
	if b.m.cur != mem {
		t.Fatalf("focus is on %v", b.m.cur.Name())
	}
	// At once: beta's identity, and nothing of alpha.
	s := b.screen()
	if strings.Contains(s, "alpha-") || !strings.Contains(s, beta.Host()) || !strings.Contains(s, "Loading") {
		t.Fatalf("the first frame after the focus change:\n%s", s)
	}
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		b.run(20 * time.Millisecond)
		if s := b.screen(); strings.Contains(s, "alpha-") {
			t.Fatalf("alpha's objects appeared on beta's screen:\n%s", s)
		}
	}
	b.see("beta-web", "beta-db", "beta-cache", "2 Containers (3)")
	b.notSee("alpha-")

	// alpha now holds only its event stream.
	b.until("alpha's statistics and log streams closed", func() bool {
		return b.srv.StatsStreams() == 0 && b.srv.LogFollowers("alpha-web") == 0
	})
	if b.srv.EventSubscribers() != 1 {
		t.Fatal("alpha's event stream was closed")
	}
	// beta's statistics are streamed now that it is focused.
	b.until("beta's statistics", func() bool { return beta.StatsStreams() == 3 })

	// And back: the same in the other direction.
	b.keys(keyPrevFocus)
	b.notSee("beta-")
	b.see("alpha-web", "2 Containers (2)")
	b.until("beta's streams closed", func() bool { return beta.StatsStreams() == 0 })
}

// ---- 3.3 one event history per daemon ---------------------------------------------

func TestBackgroundEventAppearsOnlyAfterFocusingItsDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("beta", containers("beta-web"))
	b.add("beta", beta)

	beta.Emit("network", "create", "n1", map[string]string{"name": "bg-net"})
	b.run(150 * time.Millisecond)
	if s := b.screen(); strings.Contains(s, "bg-net") {
		t.Fatalf("an event of the unfocused daemon reached the Events pane:\n%s", s)
	}
	b.keys(keyNextFocus)
	b.see("net create", "bg-net")
	// The focused daemon's own events are there too, and none of the other's.
	b.srv.Emit("network", "create", "n2", map[string]string{"name": "fg-net"})
	b.run(100 * time.Millisecond)
	b.notSee("fg-net")
	b.keys(keyPrevFocus)
	b.see("net create", "fg-net")
	b.notSee("bg-net")
}

// ---- 4.1 jobs record their daemon ----------------------------------------------------

func TestJobsRecordAndShowTheirDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("beta", func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "beta-slow", Image: "busybox:1.36", StopDelay: 600 * time.Millisecond})
	})
	mem := b.add("beta", beta)
	b.keys(keyNextFocus)
	b.see("beta-slow")
	b.selectRow("beta-slow")
	b.keys("t", "y")
	b.until("the stop job", func() bool { return b.m.jobs.RunningCount() == 1 })
	list := b.m.jobs.List()
	if list[0].Daemon != mem.Daemon() || list[0].Daemon != beta.Host() {
		t.Fatalf("the job records daemon %q, want %q", list[0].Daemon, beta.Host())
	}
	// Focus moves while it runs: it keeps running, and is listed with beta.
	b.keys(keyPrevFocus)
	b.see("stop container beta-slow @beta")
	if b.m.jobs.RunningCount() != 1 {
		t.Fatal("the job stopped when focus moved")
	}
	// It ends on the unfocused daemon: its outcome, with the daemon's name.
	b.see("✓ stop container beta-slow on beta")
	if st, _ := beta.ContainerState("beta-slow"); st != "exited" {
		t.Fatalf("beta-slow is %s", st)
	}
	if n := len(b.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutations reached the other daemon", n)
	}
}

// ---- 6.1 identity and the strip ---------------------------------------------------------

func TestStripAndIdentityInEveryLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			b := newMultiBench(t, benchOpts{width: size[0], height: size[1]})
			beta := b.daemon("beta", containers("beta-web"))
			b.context("gone", "unix:///tmp/dd-no-daemon-here.sock")
			b.add("beta", beta)
			b.pickerTo("c:gone")
			b.keys("enter")
			b.until("gone disconnected", func() bool {
				for _, m := range b.m.sess.Members() {
					if m.Name() == "gone" && m.State() == session.Disconnected {
						return true
					}
				}
				return false
			})
			b.keys("esc")
			b.settle()
			b.assertFrameGeometry()

			s := b.screen()
			lines := strings.Split(s, "\n")
			if !strings.Contains(lines[0], b.srv.Host()) && !strings.Contains(lines[0], "…") {
				t.Fatalf("the header does not name the focused daemon:\n%s", s)
			}
			strip := lines[1]
			for _, want := range []string{"Daemons", "◆1", "2 beta ●", "3 gone ✗"} {
				if !strings.Contains(strip, want) {
					t.Fatalf("the strip lacks %q: %q", want, strip)
				}
			}
			for _, pane := range []string{"1 Kinds", "2 Containers", "3 Details", "4 Events", "5 Jobs"} {
				if !strings.Contains(s, pane) {
					t.Fatalf("%s is not on screen:\n%s", pane, s)
				}
			}

			// Focus moves: the header and the marker follow.
			b.keys(keyNextFocus)
			b.see(beta.Host())
			lines = strings.Split(b.screen(), "\n")
			if !strings.Contains(lines[1], "◆2 beta") || strings.Contains(lines[1], "◆1") {
				t.Fatalf("the strip does not mark beta: %q", lines[1])
			}
			if !strings.Contains(lines[0], "ctx beta") && size[0] >= 120 {
				t.Fatalf("the header does not name beta's context: %q", lines[0])
			}
			b.assertFrameGeometry()
		})
	}
}

func TestStripWithManyMembersKeepsTheFocusedOneVisible(t *testing.T) {
	b := newMultiBench(t, benchOpts{width: 80, height: 24})
	var last *session.Member
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("daemon-%d", i)
		last = b.add(name, b.daemon(name, nil))
	}
	b.focusName(last.Name())
	strip := strings.Split(b.screen(), "\n")[1]
	if !strings.Contains(strip, "◆8 daemon-6") || !strings.Contains(strip, "more") {
		t.Fatalf("strip %q: the focused member and a count of the hidden ones", strip)
	}
	b.assertFrameGeometry()
	b.keys(keyNextFocus) // wraps to the first
	strip = strings.Split(b.screen(), "\n")[1]
	if !strings.Contains(strip, "◆1") || !strings.Contains(strip, "more›") {
		t.Fatalf("after wrapping: %q", strip)
	}
}

// ---- 6.2 the keys ---------------------------------------------------------------------

// M, > and < do what they say in every pane and every kind, and nothing else:
// no pane, kind, tab, selection or dialog of another sort changes, and no
// request is sent.
func TestDaemonKeysDoNotConflict(t *testing.T) {
	for _, k := range []string{keyDaemons, keyNextFocus, keyPrevFocus} {
		for kind, acts := range kindActions {
			for _, a := range acts {
				if a.key == k {
					t.Fatalf("%s is also %s's %q action", k, kindNames[kind], a.label)
				}
			}
		}
	}
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("beta", nil)
	b.add("beta", beta)
	for kind := Kind(0); kind < kindCount; kind++ {
		for pane := Pane(0); pane < paneCount; pane++ {
			b.kind(kind)
			b.m.focus = pane
			before := b.m.cur
			b.srv.ResetRequests()
			beta.ResetRequests()
			tab := b.m.tab[kind]

			b.keys(keyDaemons)
			if b.m.dlg == nil || b.m.dlg.kind != dlgDaemons {
				t.Fatalf("%s in %s/%s did not open the picker", keyDaemons, kindNames[kind], paneNames[pane])
			}
			b.keys("esc")
			for _, k := range []string{keyNextFocus, keyPrevFocus} {
				b.keys(k)
				if b.m.cur == before {
					t.Fatalf("%s in %s/%s did not move focus", k, kindNames[kind], paneNames[pane])
				}
				if b.m.dlg != nil || b.m.focus != pane || b.m.kind != kind || b.m.tab[kind] != tab || b.m.filtering {
					t.Fatalf("%s in %s/%s changed more than the focus", k, kindNames[kind], paneNames[pane])
				}
				before = b.m.cur
			}
			if n := len(b.srv.Mutations()) + len(beta.Mutations()); n != 0 {
				t.Fatalf("%d mutations", n)
			}
		}
	}
}

// ---- 6.3 the picker --------------------------------------------------------------------

func TestPickerAt80x24(t *testing.T) {
	b := newMultiBench(t, benchOpts{width: 80, height: 24})
	long := "ssh://operations-team@build-host-0042.eu-central.internal.example.com:2222/run/user/1000/docker.sock"
	b.context("far", long)
	b.ctxs = append(b.ctxs, dockerctx.Context{Name: "pipe", Endpoint: dockerctx.Endpoint{Host: "npipe:////./pipe/docker_engine"}, Problem: "unsupported endpoint scheme \"npipe\""})
	beta := b.daemon("beta", nil)
	b.keys(keyDaemons)
	b.pickerTo("c:far")
	s := b.screen()
	b.assertFrameGeometry()
	// The endpoint in full, however long: wrapped, never cut.
	for _, part := range wrapLine(long, 66) {
		if !strings.Contains(s, part) {
			t.Fatalf("the endpoint is not shown in full (missing %q):\n%s", part, s)
		}
	}
	for _, want := range []string{"Daemons", "In this session", "Contexts on this machine", "[REMOTE ssh]", "pipe", "✗ cannot be used", "unsupported endpoint scheme", "beta", beta.Host()} {
		if !strings.Contains(s, want) {
			t.Fatalf("the picker lacks %q:\n%s", want, s)
		}
	}
	// An unusable context cannot be added.
	b.pickerTo("c:pipe")
	b.keys("enter")
	b.see("pipe cannot be added")
	if b.m.sess.Len() != 1 {
		t.Fatal("an unusable context was added")
	}

	// Adding beta, and adding it again under another name.
	b.add("beta", beta)
	b.context("beta-again", beta.Host())
	b.pickerTo("c:beta-again")
	b.keys("enter")
	b.see("beta-again is already in the session as beta")
	if b.m.sess.Len() != 2 {
		t.Fatalf("%d members after adding one daemon twice", b.m.sess.Len())
	}

	// Removal asks first, and n keeps it.
	mem := b.m.sess.Members()[1]
	b.pickerTo(fmt.Sprintf("m:%d", mem.ID()))
	b.keys("d")
	b.see("Remove beta from the session?", "y remove")
	b.assertFrameGeometry()
	b.keys("n")
	b.see("Kept in the session.")
	if b.m.sess.Len() != 2 {
		t.Fatal("n removed it")
	}
	b.keys("d", "y")
	b.see("Removed beta from the session")
	<-mem.Closed()
	b.until("beta's event stream closed", func() bool { return beta.EventSubscribers() == 0 })
	if b.m.sess.Len() != 1 {
		t.Fatal("y did not remove it")
	}
}

func TestPickerConfirmationMustBeReadFirst(t *testing.T) {
	b := newMultiBench(t, benchOpts{arm: 300 * time.Millisecond})
	beta := b.daemon("beta", nil)
	b.keys(keyDaemons)
	time.Sleep(320 * time.Millisecond)
	b.pickerTo("c:beta")
	b.keys("enter")
	b.until("beta joined", func() bool {
		return b.m.sess.Len() == 2 && b.m.sess.Members()[1].State() == session.Connected && beta.EventSubscribers() == 1
	})
	mem := b.m.sess.Members()[1]
	b.pickerTo(fmt.Sprintf("m:%d", mem.ID()))
	b.keys("d", "y") // typed ahead of the question
	if b.m.sess.Len() != 2 {
		t.Fatal("a y typed ahead of the confirmation removed a daemon")
	}
	time.Sleep(320 * time.Millisecond)
	b.keys("y")
	b.until("removed", func() bool { return b.m.sess.Len() == 1 })
}

func TestRemovalRefusedInThePickerWhileAJobRuns(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("beta", nil)
	mem := b.add("beta", beta)
	b.keys(keyNextFocus)
	gate := make(chan struct{})
	b.m.startJob("pull image", "nginx:1.27", func(ctx context.Context, r *jobs.Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	})
	b.keys(keyPrevFocus) // the job runs on beta; alpha is focused
	b.pickerTo(fmt.Sprintf("m:%d", mem.ID()))
	b.see("1 job is running on it — pull image nginx:1.27")
	b.keys("d")
	b.see("Cannot remove beta: 1 job is running on it — pull image nginx:1.27")
	if b.m.sess.Len() != 2 || mem.State() != session.Connected {
		t.Fatal("the refusal changed the session")
	}
	close(gate)
	b.until("job finished", func() bool { return b.m.jobs.RunningCount() == 0 })
	b.keys("d", "y")
	b.until("removed", func() bool { return b.m.sess.Len() == 1 })
}

func TestReadOnlyPickerSaysSoAndAddedDaemonsAreReadOnly(t *testing.T) {
	b := newMultiBench(t, benchOpts{readOnly: true})
	beta := b.daemon("beta", containers("beta-web"))
	b.keys(keyDaemons)
	b.see("Read-only session", "and any added here")
	mem := b.add("beta", beta)
	b.see("2 beta ● ro")
	b.keys(keyNextFocus)
	b.see("beta-web")
	b.selectRow("beta-web")
	beta.ResetRequests()
	b.keys("t")
	b.see("Read-only mode", "Nothing was sent to the daemon")
	if n := len(beta.Mutations()); n != 0 {
		t.Fatalf("%d mutation(s) reached the added daemon", n)
	}
	if err := mem.Engine().StopContainer(context.Background(), beta.ContainerID("beta-web")); err == nil {
		t.Fatal("the added daemon's engine accepts mutations")
	}
}

// ---- 6.4 the empty state -------------------------------------------------------------------

func TestEmptyStateAfterTheLastDaemonIsRemoved(t *testing.T) {
	b := newMultiBench(t, benchOpts{width: 80, height: 24})
	b.pickerTo(fmt.Sprintf("m:%d", b.m.cur.ID()))
	b.keys("d", "y")
	b.see("No daemon is connected now.")
	b.keys("esc")
	b.see("No daemon is connected.", "no daemon connected", "M open the picker")
	b.assertFrameGeometry()
	if b.quit {
		t.Fatal("the desk quit")
	}
	b.until("the launch daemon let go", func() bool { return b.srv.EventSubscribers() == 0 })

	// Action keys change nothing and say why.
	b.srv.ResetRequests()
	for _, k := range []string{"t", "d", "s", "enter", "/", "r", "1", "tab", keyNextFocus, "S", "F", "B"} {
		b.keys(k)
		if b.m.dlg != nil || b.m.filtering || b.m.sp.open || b.m.fs.open || b.m.rb.open {
			t.Fatalf("%q opened something in the empty state", k)
		}
	}
	b.see("No daemon is connected: nothing to act on")
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("%d request(s) sent with no daemon in the session", n)
	}

	// The picker is one key away; a daemon added there is focused when it answers.
	b.daemon("beta", containers("beta-web"))
	b.pickerTo("c:beta")
	b.keys("enter", "esc")
	b.see("beta-web", "Focused beta")
	b.notSee("No daemon is connected.")

	// Quitting works in the empty state too.
	b.pickerTo(fmt.Sprintf("m:%d", b.m.cur.ID()))
	b.keys("d", "y", "esc")
	b.see("No daemon is connected.")
	b.keys("q")
	b.until("quit", func() bool { return b.quit })
}

// ---- 6.5 notices -----------------------------------------------------------------------------

func TestBackgroundExitIsOneNoticeAndNotAnEvent(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("staging", containers("beta-web"))
	b.add("staging", beta)
	b.settle()
	steps := b.steps
	beta.Crash("beta-web", 3)
	b.see("staging: container beta-web exited (code 3)")
	if s := b.screen(); strings.Count(s, "beta-web") != 1 {
		t.Fatalf("the exit should appear once, in the status line:\n%s", s)
	}
	if b.steps-steps > 200 {
		t.Fatalf("%d messages for one notice", b.steps-steps)
	}
	// Disconnect and reconnect of the unfocused daemon are notices too.
	beta.Stop()
	b.see("staging: lost the connection — retrying", "2 staging ✗")
	if err := beta.Restart(); err != nil {
		t.Fatal(err)
	}
	b.see("staging: reconnected", "2 staging ●")
	// The focused daemon's Events pane never saw any of it.
	for _, e := range b.m.hub.Events.Tail(100) {
		if strings.Contains(e.Event.Name(), "beta-web") {
			t.Fatal("staging's event is in the focused daemon's history")
		}
	}
}

// A daemon added while down is shown disconnected with the reason, in the
// picker and the strip, and comes back by itself.
func TestAddedDaemonDownThenUp(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	sock := fmt.Sprintf("/tmp/dd-later-%d.sock", time.Now().UnixNano())
	b.context("later", "unix://"+sock)
	b.pickerTo("c:later")
	b.keys("enter")
	b.see("✗ disconnected — retrying", "does not exist")
	b.keys("esc")
	b.see("2 later ✗", "later: not reachable")
	// The rest of the desk works.
	b.selectRow("redis-cache")
	b.keys("enter")
	b.see("Actions: redis-cache")
	b.keys("esc")

	srv := fakeengine.New()
	srv.Name = "later"
	if err := srv.Start(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	b.see("2 later ●", "later: connected")
}
