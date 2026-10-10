package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
)

// ---- workspace -----------------------------------------------------------------------

func TestFocusIsUnmistakableAndReachable(t *testing.T) {
	b := newBench(t, benchOpts{})
	titles := []string{"1 Kinds", "2 Containers", "3 Details", "4 Events", "5 Jobs"}
	checkFocus := func(want Pane) {
		t.Helper()
		if b.m.focus != want {
			t.Fatalf("focus is %s, want %s", paneNames[b.m.focus], paneNames[want])
		}
		s := b.screen()
		// Exactly one pane carries the ◆ marker and the heavy border — both
		// survive without colour (tests render with no colour at all).
		if n := strings.Count(s, "◆ "); n != 1 {
			t.Fatalf("%d panes look focused:\n%s", n, s)
		}
		if !strings.Contains(s, "┏━ ◆ "+titles[want]) {
			t.Fatalf("%s is not drawn as focused:\n%s", paneNames[want], s)
		}
		for p, title := range titles {
			if Pane(p) != want && !strings.Contains(s, "┌─ "+title) {
				t.Fatalf("%s should be drawn unfocused:\n%s", paneNames[p], s)
			}
		}
	}
	checkFocus(PaneList)
	// Tab visits every pane in order and wraps; Shift+Tab goes back.
	for _, want := range []Pane{PaneDetails, PaneEvents, PaneJobs, PaneKinds, PaneList} {
		b.keys("tab")
		checkFocus(want)
	}
	for _, want := range []Pane{PaneKinds, PaneJobs, PaneEvents} {
		b.keys("shift+tab")
		checkFocus(want)
	}
	for i, want := range []Pane{PaneKinds, PaneList, PaneDetails, PaneEvents, PaneJobs} {
		b.keys(fmt.Sprint(i + 1))
		checkFocus(want)
	}
	// The footer follows the focus.
	b.keys("5")
	b.see("c cancel", "x clear finished")
	b.keys("2")
	b.see("s start", "t stop")
}

func TestTooSmallTerminal(t *testing.T) {
	b := newBench(t, benchOpts{width: 60, height: 20})
	s := b.screen()
	if !strings.Contains(s, "needs at least 80×24") || !strings.Contains(s, b.srv.Host()) || strings.Contains(s, "1 Kinds") {
		t.Fatalf("too-small message:\n%s", s)
	}
	b.assertFrameGeometry()
	b.selectRow("shop-db-1")
	// Growing the window brings the panes back, with state preserved.
	b.step(tea.WindowSizeMsg{Width: 100, Height: 30})
	b.see("1 Kinds", "5 Jobs", "3 Details: shop-db-1")
}

func TestHelpAndActionMenu(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.keys("?")
	b.see("Keys", "Everywhere", b.srv.Host())
	b.keys("G")
	b.see("How changes are confirmed", "Nothing is ever forced")
	b.keys("esc")
	b.notSee("How changes are confirmed")

	// The menu lists the actions of the highlighted object; choosing one is
	// the same as pressing its key — it opens the confirmation, nothing more.
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.srv.ResetRequests()
	b.keys("enter")
	b.see("Actions: redis-cache", "start", "stop", "restart", "pause", "unpause", "kill", "remove…", "shell")
	b.keys("down") // stop
	b.keys("enter")
	b.see("Stop container", "redis-cache")
	b.noMutations("after choosing from the menu")
	b.keys("esc")

	// Compose offers stop and remove, and nothing like up/down/build.
	b.kind(KindCompose)
	b.keys("enter")
	b.see("Actions: blog", "stop project", "remove project…")
	menu := b.screen()
	for _, absent := range []string{" up ", "down ", "build", "pull", "scale"} {
		if strings.Contains(strings.ToLower(menu[strings.Index(menu, "Actions: blog"):strings.Index(menu, "Every action that changes")]), absent) {
			t.Errorf("the compose menu offers %q", absent)
		}
	}
	if n := len(kindActions[KindCompose]); n != 2 {
		t.Errorf("compose has %d actions, want exactly stop and remove", n)
	}
	b.keys("esc")
}

func TestQuit(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.keys("q")
	b.until("quit", func() bool { return b.quit })

	// With a job running, quitting asks first and names the job.
	b = newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("slow-stopper") // takes 4 s to stop
	b.keys("t", "y")
	b.until("job running", func() bool { return b.m.jobs.RunningCount() == 1 })
	b.keys("q")
	b.see("Quit Docker Desk?", "Jobs are still running", "stop container slow-stopper")
	if b.quit {
		t.Fatal("quit without asking while a job was running")
	}
	b.keys("ctrl+c") // not consent either
	b.settle()
	if b.quit {
		t.Fatal("Ctrl+C in the quit dialog quit")
	}
	b.keys("n")
	b.notSee("Quit Docker Desk?")
	b.keys("ctrl+c")
	b.see("Quit Docker Desk?")
	b.keys("y")
	b.until("quit after y", func() bool { return b.quit })
}

// ---- browsing ------------------------------------------------------------------------

func TestKindsAndLists(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.see("Containers    12", "Images        14", "Volumes        7", "Networks       7", "Compose        2")

	// Moving the highlight in the Kinds pane is enough: no Enter needed.
	b.keys("1", "down")
	b.see("2 Images (14, 15 refs)", "REPOSITORY", "TAG", "SIZE")
	// One row per tag; untagged images are <none>.
	rows := strings.Join(b.rowNames(), "|")
	if !strings.Contains(rows, "node:22-alpine") || !strings.Contains(rows, "node:lts-alpine") || !strings.Contains(rows, "<untagged>") {
		t.Fatalf("image rows: %s", rows)
	}
	b.keys("2", "G")
	b.see("<none>", "dangling")
	b.keys("1", "down")
	b.see("2 Volumes (7)", "IN USE", "shop-db-1", "unused", "(anonymous)")
	b.keys("down")
	b.see("2 Networks (7)", "CONTAINERS", "172.17.0.0/16")
	b.keys("down")
	b.see("2 Compose (2)", "PROJECT", "RUNNING", "/home/dev/src/shop")
	b.keys("g")
	b.see("2 Containers (12)", "STATE", "● running", "○ exited", "‖ paused")

	// Counts follow the daemon without any key.
	if _, err := b.eng.CreateVolume(context.Background(), "fresh"); err != nil {
		t.Fatal(err)
	}
	b.see("Volumes        8")
}

func TestEmptyAndFailingLists(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {}})
	b.see("No containers on this daemon.")
	b.kind(KindCompose)
	b.see("No compose projects found")
	b.kind(KindImages)
	b.see("No images on this daemon.")
	b.kind(KindNetworks)
	b.see("bridge", "host", "none") // predefined networks always exist

	// A daemon error is shown in the pane; the rest keeps working; the next
	// refresh recovers.
	b = newBench(t, benchOpts{})
	b.srv.FailNext("GET", `^/containers/json$`, 500, "layer store is corrupted", 1)
	b.keys("r")
	b.see("stale list — refresh failed: layer store is corrupted", "shop-web-1")
	b.kind(KindVolumes)
	b.see("shop_pgdata")
	b.kind(KindContainers)
	b.keys("r")
	b.notSee("refresh failed")
	b.assertFrameGeometry()
}

func TestHighlightFollowsTheObject(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	before := b.m.sel[KindContainers].idx

	// Rows appear above it.
	b.srv.AddContainer(fakeengine.ContainerSpec{Name: "aaa", Image: "alpine:3.20"})
	b.srv.Emit("container", "create", "x", nil)
	b.until("refresh", func() bool { return len(b.m.snap.Containers) == 13 })
	if r, _ := b.m.currentRow(); r.name != "redis-cache" || b.m.sel[KindContainers].idx != before+1 {
		t.Fatalf("highlight is on %q at %d (was %d)", r.name, b.m.sel[KindContainers].idx, before)
	}
	b.see("3 Details: redis-cache")

	// It changes state elsewhere: the row shows it without any key.
	b.srv.Crash("redis-cache", 137)
	b.see("Exited (137)")
	if r, _ := b.m.currentRow(); r.name != "redis-cache" {
		t.Fatalf("highlight left the object when it changed state: %q", r.name)
	}

	// It is removed while its details are shown: the highlight moves to a
	// neighbouring row, the Details pane follows, and no dialog appears.
	idx := b.m.sel[KindContainers].idx
	if err := b.eng.RemoveContainer(context.Background(), b.srv.ContainerID("redis-cache")); err != nil {
		t.Fatal(err)
	}
	b.until("refresh", func() bool { return len(b.m.snap.Containers) == 12 })
	b.settle()
	r, ok := b.m.currentRow()
	if !ok || r.name == "redis-cache" || b.m.sel[KindContainers].idx != idx {
		t.Fatalf("after removal the highlight is %q at %d (want the row now at %d)", r.name, b.m.sel[KindContainers].idx, idx)
	}
	b.see("3 Details: " + r.name)
	if b.m.dlg != nil {
		t.Fatal("removing the highlighted object raised a dialog")
	}
}

func TestFilter(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.keys("/")
	b.typeText("SHOP-") // case-insensitive, live
	b.see("2 Containers (4 of 12)", "/SHOP-")
	b.keys("enter")
	b.see("filter: SHOP-")
	for _, n := range b.rowNames() {
		if !strings.HasPrefix(n, "shop-") {
			t.Fatalf("filter let %q through", n)
		}
	}
	// Esc while editing restores the previous filter.
	b.keys("/")
	b.typeText("db")
	b.see("(1 of 12)")
	b.keys("esc")
	b.see("(4 of 12)")
	// Esc in the list clears it.
	b.keys("esc")
	b.see("2 Containers (12)")
	b.keys("/")
	b.typeText("zzz")
	b.keys("enter")
	b.see("Nothing matches the filter")
	// Filter keys must never reach actions: typing "d" filtered, it did not
	// open a removal dialog.
	b.keys("esc", "/", "d", "t", "K")
	if b.m.dlg != nil || len(b.srv.Mutations()) != 0 {
		t.Fatal("keys typed into the filter reached an action")
	}
}

func TestDetails(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("shop-db-1")
	b.see("3 Details: shop-db-1", "Name", "shop-db-1", "postgres:16", "● running", "Health", "healthy",
		"Compose", "shop / db", "Started", "Restart", "no (restarted 0×)", "Command", "postgres",
		"5432/tcp  (not published)", "shop_default  172.", "volume shop_pgdata → /data/shop_pgdata (rw)")
	b.selectRow("shop-worker-1")
	b.see("Finished", "exit code 1", "(no address: not running)")

	// Raw inspect: the daemon's whole document, scrollable.
	b.selectRow("shop-web-1")
	b.see("0.0.0.0:8080 → 80/tcp")
	b.keys("]", "]", "]")
	b.see("[Inspect]", `"Id": "`+b.srv.ContainerID("shop-web-1")+`"`, `"State": {`)
	top := b.screen()
	b.keys("3", "pgdown")
	if b.screen() == top {
		t.Fatal("the inspect view did not scroll")
	}
	b.keys("g")
	b.see(`"Id": "`)
	b.keys("]")
	b.see("[Info]")

	b.kind(KindNetworks)
	b.selectRow("shop_default")
	b.see("Driver", "bridge", "Subnets", "Containers", "shop-api-1  172.", "shop-worker-1", "configured, not running")
	b.kind(KindVolumes)
	b.selectRow("blog_db")
	b.see("Mountpoint", "Referenced by", "blog-db-1", "exited at /data/blog_db", "com.docker.compose.project=blog")
	b.kind(KindImages)
	b.selectRow("nginx:1.27")
	b.see("Platform", "linux/amd64", "Used by", "shop-web-1")
	b.keys("]")
	b.see("[Inspect]", `"Architecture": "amd64"`)

	b.kind(KindCompose)
	b.selectRow("shop")
	b.see("Project", "4, 3 running", "Services", "web", "shop-web-1", "worker", "shop-worker-1",
		"network  shop_default", "volume   shop_pgdata", "never reads or runs compose files")
	// The last container of a project disappears: so does the project.
	for _, c := range []string{"blog-app-1", "blog-db-1"} {
		if err := b.eng.RemoveContainer(context.Background(), b.srv.ContainerID(c)); err != nil {
			t.Fatal(err)
		}
	}
	b.see("2 Compose (1)")
	b.notSee("blog ")
}

// ---- logs and stats ------------------------------------------------------------------------

func TestLogsView(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1", Logs: []string{"boot ok"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "other", Image: "app:1", Logs: []string{"other's line"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "zz-dead", Image: "app:1", State: "exited", Logs: []string{"last words"}})
	}})
	b.m.focus = PaneList
	b.selectRow("app")
	b.keys("]")
	b.see("[Logs]", "boot ok", "● following")
	if b.srv.LogFollowers("app") != 1 {
		t.Fatal("no log stream is open")
	}

	// New output appears by itself.
	b.srv.WriteLog("app", 1, "request 1")
	b.see("request 1")

	// Hostile output cannot escape the pane.
	b.srv.WriteLog("app", 2, "evil\x1b[2J\x1b[1;1H\x1b]0;owned\x07 payload")
	b.see("evil payload")
	if raw := b.m.View(); strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b]0;") {
		t.Fatal("a control sequence from a container reached the screen")
	}
	b.assertFrameGeometry()

	// Scrolling back holds the view while lines keep arriving, and says so.
	for i := 0; i < 80; i++ {
		b.srv.WriteLog("app", 1, fmt.Sprintf("filler %02d", i))
	}
	b.see("filler 79")
	b.keys("3", "pgup")
	b.see("‖ scrolled back")
	held := b.screen()
	b.srv.WriteLog("app", 1, "arrived while scrolled back")
	b.see("newer lines")
	if strings.Contains(b.screen(), "arrived while scrolled back") {
		t.Fatal("the view jumped while scrolled back")
	}
	if !strings.Contains(held, "filler") {
		t.Fatal("scroll-back shows nothing")
	}
	b.keys("G")
	b.see("● following", "arrived while scrolled back")
	b.keys("f")
	b.see("‖ scrolled back")
	b.keys("f")
	b.see("● following")
	b.keys("w")
	b.see("wrap off")
	b.keys("w")

	// Another container: its lines only, and the old stream is closed.
	b.keys("2")
	b.selectRow("other")
	b.see("3 Details: other", "other's line")
	if strings.Contains(b.screen(), "boot ok") {
		t.Fatal("the previous container's lines are still shown")
	}
	b.until("old stream closed", func() bool { return b.srv.LogFollowers("app") == 0 })

	// A stopped container shows its last output.
	b.selectRow("zz-dead")
	b.see("last words", "end of output: the container is not running")
	// …and following resumes when it is started.
	if err := b.eng.StartContainer(context.Background(), b.srv.ContainerID("zz-dead")); err != nil {
		t.Fatal(err)
	}
	b.see("● following", "container started")

	// Leaving the Logs tab closes the stream.
	b.keys("]")
	b.until("stream closed on leaving the tab", func() bool { return b.srv.LogFollowers("zz-dead") == 0 })
	// So does leaving the kind.
	b.keys("[")
	b.until("stream reopened", func() bool { return b.srv.LogFollowers("zz-dead") == 1 })
	b.kind(KindImages)
	b.until("stream closed on leaving the kind", func() bool { return b.srv.LogFollowers("zz-dead") == 0 })
}

func TestStatsView(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("shop-api-1")
	// The list carries live CPU and memory for running containers.
	b.until("live cells", func() bool {
		cpu, mem := b.m.liveCells(b.m.rows[KindContainers][b.m.sel[KindContainers].idx])
		return strings.HasSuffix(cpu, "%") && strings.HasSuffix(mem, "iB")
	})
	b.keys("]", "]")
	b.see("[Stats]", "CPU", "%", "MEM", "/ 16.0GiB", "NET", "↓", "↑", "DISK", "PIDS", "samples")
	first := b.screen()
	b.until("figures move", func() bool { b.settle(); return b.screen() != first })
	b.until("history grows", func() bool { return len(b.m.hub.StatsHistory(b.srv.ContainerID("shop-api-1"), 100)) >= 10 })

	b.selectRow("old-migration")
	b.see("old-migration is not running (exited)", "only collected for running containers")

	// A watched container stops: no error, the view says so.
	b.selectRow("shop-api-1")
	b.see("PIDS")
	b.srv.Crash("shop-api-1", 1)
	b.see("shop-api-1 is not running (exited)")
	if b.m.dlg != nil || b.m.status.tone == toneBad {
		t.Fatalf("a container stopping while watched raised an error: %+v", b.m.status)
	}
}

func TestStatsStreamCapIsVisible(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		for i := 0; i < live.MaxStatsStreams+5; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("c%02d", i), Image: "app:1"})
		}
	}, width: 140, height: 60})
	b.m.focus = PaneList
	b.keys("G")
	b.settle()
	if n := b.m.hub.StatsStreams(); n != live.MaxStatsStreams {
		t.Fatalf("%d statistics streams, cap is %d", n, live.MaxStatsStreams)
	}
	// The highlighted container is always collected; rows beyond the cap say so.
	r, _ := b.m.currentRow()
	if !b.m.hub.Stats(r.containerID).Collected {
		t.Fatal("the highlighted container has no statistics stream")
	}
	notCollected := 0
	for _, row := range b.m.rows[KindContainers] {
		if cpu, _ := b.m.liveCells(row); cpu == "n/c" {
			notCollected++
		}
	}
	if notCollected != 5 {
		t.Fatalf("%d rows are marked as not collected, want the 5 beyond the cap", notCollected)
	}
}

// ---- events ----------------------------------------------------------------------------

func TestEventsPane(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.see("Waiting for engine events")
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.keys("t", "y")
	b.see("ctr kill sig 15 redis-cache", "ctr die exit 0 redis-cache", "ctr stop       redis-cache", "● live")

	// Something else changes the engine.
	if _, err := b.eng.CreateNetwork(context.Background(), "made-elsewhere"); err != nil {
		t.Fatal(err)
	}
	b.see("net create     made-elsewhere")

	// A crash stands out and carries its exit code.
	b.srv.Crash("shop-api-1", 3)
	b.see("ctr die exit 3 shop-api-1")
	var crash, clean live.EventEntry
	for _, e := range b.m.hub.Events.Tail(100) {
		if e.Event.Action == "die" && e.Event.Attributes["exitCode"] == "3" {
			crash = e
		}
		if e.Event.Action == "die" && e.Event.Attributes["exitCode"] == "0" {
			clean = e
		}
	}
	if eventTone(crash.Event) != toneBad || eventTone(clean.Event) == toneBad {
		t.Fatalf("crash tone %v, clean exit tone %v", eventTone(crash.Event), eventTone(clean.Event))
	}

	// Scroll back: the view holds, new events are still recorded and counted.
	for i := 0; i < 60; i++ {
		b.srv.Emit("container", "top", b.srv.ContainerID("shop-web-1"), map[string]string{"name": fmt.Sprintf("probe-%02d", i)})
	}
	b.see("probe-59")
	b.keys("4", "pgup")
	b.see("‖ +")
	b.srv.Emit("container", "oom", b.srv.ContainerID("shop-web-1"), map[string]string{"name": "late-oom"})
	b.until("event recorded", func() bool { last, _ := b.m.hub.Events.Last(); return last.Event.Action == "oom" })
	b.settle()
	if strings.Contains(b.screen(), "late-oom") {
		t.Fatal("the events view jumped while scrolled back")
	}
	b.keys("G")
	b.see("● live", "ctr oom        late-oom")

	// The stream is interrupted and comes back.
	b.srv.DropEventStreams()
	b.see("event stream interrupted", "event stream resumed")
}

func TestEventBurstCausesBoundedRefreshes(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.settle()
	b.srv.ResetRequests()
	id := b.srv.ContainerID("shop-web-1")
	for i := 0; i < 100; i++ {
		b.srv.Emit("container", "top", id, map[string]string{"name": "shop-web-1"})
	}
	// One real change at the very end of the burst.
	if err := b.eng.PauseContainer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	b.until("final state on screen", func() bool {
		for _, c := range b.m.snap.Containers {
			if c.ID == id && c.State == "paused" {
				return true
			}
		}
		return false
	})
	b.run(200 * time.Millisecond)
	if n := b.srv.CountRequests("GET", `^/containers/json$`); n > 6 {
		t.Fatalf("101 events caused %d container list requests; they must be coalesced", n)
	} else {
		t.Logf("101 events → %d container list request(s)", n)
	}
}

// ---- jobs ------------------------------------------------------------------------------

func TestJobsPane(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.srv.PullStepDelay = 15 * time.Millisecond
	b.see("No jobs yet.")
	b.m.focus = PaneList
	b.kind(KindImages)

	// A pull is sent off; the desk stays usable while it runs.
	b.keys("P")
	b.typeText("alpine:edge")
	b.see("Will pull exactly: alpine:edge")
	b.keys("enter")
	b.until("pull running", func() bool { return b.m.jobs.RunningCount() == 1 })
	b.until("progress", func() bool { j := b.m.jobs.List()[0]; return j.Fraction > 0 && j.Fraction < 1 })
	b.see("pull image alpine:edge", "%", "1 running")
	b.kind(KindVolumes) // navigating does not wait for the pull
	b.see("2 Volumes (7)")
	if b.m.jobs.RunningCount() != 1 {
		t.Fatal("the pull finished before the navigation could prove anything; slow the fake down")
	}
	b.finishJobs()
	b.see("✓ pull image alpine:edge")
	b.kind(KindImages)
	b.see("alpine", "edge")

	// A failure keeps the daemon's message, in the pane and the status line.
	b.keys("P")
	b.typeText("nosuch/image:1")
	b.keys("enter")
	b.finishJobs()
	b.see("✗ pull image nosuch/image:1", "pull access denied", "1 failed")
	if b.m.status.tone != toneBad {
		t.Fatalf("a failed job must be announced as an error: %+v", b.m.status)
	}

	// Reading a job's output.
	b.keys("5")
	list := b.m.jobs.List()
	if list[0].State != jobs.Failed {
		t.Fatalf("newest job: %+v", list[0])
	}
	b.keys("enter")
	b.see("output", "✗ failed", "Error: pull access denied for nosuch/image")
	b.keys("esc")

	// A multi-object job shows how far it is; cancelling stops it there.
	b.keys("2")
	b.kind(KindCompose)
	b.selectRow("shop")
	b.keys("t")
	b.see("Stop compose project", "Exactly these 3 will be stopped", "shop-api-1", "shop-db-1", "shop-web-1")
	b.keys("y")
	b.until("project stop in progress", func() bool {
		j := b.m.jobs.List()[0]
		return j.State == jobs.Running && j.Total == 3 && j.Done >= 1
	})
	b.see("stop project shop", "/3")
	b.keys("5", "g", "c") // newest job is highlighted; cancel it
	b.finishJobs()
	j := b.m.jobs.List()[0]
	if j.State != jobs.Cancelled || j.Done >= 3 {
		t.Fatalf("cancelled job: %+v", j)
	}
	b.see("⊘ stop project shop")
	b.keys("enter")
	b.see("✓ stop container shop-api-1", "cancelled")
	b.keys("esc")
	running := 0
	for _, n := range []string{"shop-api-1", "shop-db-1", "shop-web-1"} {
		if st, _ := b.srv.ContainerState(n); st == "running" {
			running++
		}
	}
	if running == 0 {
		t.Fatal("cancelling did not stop the job: every container was stopped")
	}

	// Clearing removes finished jobs.
	b.keys("x")
	b.see("No jobs yet.")
}

func TestPartialFailureContinuesAndReports(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.kind(KindImages)
	b.keys("X")
	b.see("Exactly these 2 will be deleted")
	// One of the listed images becomes in use after the preview.
	victim := strings.TrimPrefix(b.m.dlg.plan.Steps[0].ID, "sha256:")
	b.srv.AddContainer(fakeengine.ContainerSpec{Name: "latecomer", Image: victim, State: "exited"})
	b.typeText("prune")
	b.keys("enter")
	b.finishJobs()
	b.see("✗ prune images 2 images", "1 of 2 failed")
	if !b.srv.HasImage(victim) {
		t.Fatal("an image that became in use was deleted")
	}
	b.keys("5", "enter")
	b.see("✗ remove image", "image is being used by stopped container", "✓ remove image")
}

// ---- forms -----------------------------------------------------------------------------

func TestForms(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList

	// Pull: empty and malformed references send nothing.
	b.kind(KindImages)
	b.srv.ResetRequests()
	b.keys("P")
	b.see("Pull image", "Image reference:", "daemon", b.srv.Host(), "one tag, never all of them")
	b.keys("enter")
	b.see("enter an image reference")
	b.typeText("Not Valid")
	b.see("cannot contain spaces")
	b.keys("enter")
	b.settle()
	b.noMutations("after submitting bad references")
	if b.m.dlg == nil {
		t.Fatal("a bad reference closed the form")
	}
	b.keys("esc")
	if len(b.m.jobs.List()) != 0 {
		t.Fatal("a cancelled form left a job")
	}
	// No tag → exactly :latest.
	b.keys("P")
	b.typeText("busybox")
	b.see("Will pull exactly: busybox:latest")
	b.keys("enter")
	b.finishJobs()
	muts := b.srv.Mutations()
	if len(muts) != 1 || !strings.Contains(muts[0].Query, "tag=latest") {
		t.Fatalf("pull request: %v", muts)
	}

	// Volume: invalid, taken, then created.
	b.kind(KindVolumes)
	b.srv.ResetRequests()
	b.keys("n")
	b.see("Create volume", "Volume name:")
	b.keys("enter")
	b.see("enter a name for the volume")
	b.typeText("bad name")
	b.keys("enter")
	b.see("may only contain")
	for i := 0; i < 8; i++ {
		b.keys("backspace")
	}
	b.typeText("old-backups")
	b.keys("enter")
	b.see(`a volume named "old-backups" already exists`)
	b.noMutations("after invalid volume names")
	for i := 0; i < 11; i++ {
		b.keys("backspace")
	}
	b.typeText("scratch")
	b.keys("enter")
	b.finishJobs()
	b.see("✓ create volume scratch", "2 Volumes (8)")

	// Network: taken, then created as a bridge.
	b.kind(KindNetworks)
	b.srv.ResetRequests()
	b.keys("n")
	b.typeText("bridge")
	b.keys("enter")
	b.see(`a network named "bridge" already exists`)
	b.noMutations("after a taken network name")
	for i := 0; i < 6; i++ {
		b.keys("backspace")
	}
	b.typeText("frontend")
	b.keys("enter")
	b.finishJobs()
	b.see("✓ create network frontend", "2 Networks (8)")

	// Keys typed into a form are text, never actions.
	b.kind(KindContainers)
	b.selectRow("redis-cache")
	b.kind(KindImages)
	b.srv.ResetRequests()
	b.keys("P")
	b.typeText("dXtKy")
	b.settle()
	b.noMutations("typing action letters into a form")
	b.keys("esc")
}

func TestConnectAndDisconnectPickers(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.kind(KindNetworks)

	b.selectRow("shop_default")
	b.keys("c")
	b.see("Connect a container to shop_default", "Containers not yet attached")
	for _, it := range b.m.dlg.items {
		if strings.HasPrefix(it.label, "shop-") {
			t.Errorf("already-attached container offered: %s", it.label)
		}
	}
	if len(b.m.dlg.items) != 8 {
		t.Errorf("want 8 candidates, got %d", len(b.m.dlg.items))
	}
	b.keys("esc")

	for _, n := range []string{"host", "none"} {
		b.selectRow(n)
		b.keys("c")
		b.see("cannot be connected to or disconnected from")
		if b.m.dlg != nil {
			t.Fatalf("connect on %s opened a dialog", n)
		}
		b.keys("D")
		if b.m.dlg != nil {
			t.Fatalf("disconnect on %s opened a dialog", n)
		}
	}
	b.selectRow("orphan-net")
	b.keys("D")
	b.see("nothing to disconnect")
	if b.m.dlg != nil {
		t.Fatal("disconnect on an empty network opened a picker")
	}

	// Disconnecting a container's only network warns.
	b.selectRow("shop_default")
	b.keys("D")
	b.see("Disconnect a container from shop_default")
	b.keys("enter")
	b.see("Disconnect container from network", "only network")
	b.keys("esc")
}

func TestShellFormAndHandOff(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("shop-api-1")
	b.srv.ResetRequests()
	b.keys("e")
	b.see("Open a shell", "shop-api-1", "Command to run inside the container:", "exec bash || exec sh", b.srv.Host())
	b.noMutations("opening the shell form")
	if b.execs != 0 {
		t.Fatal("the terminal was handed over before the form was submitted")
	}
	b.keys("esc")
	if b.execs != 0 {
		t.Fatal("Esc started a shell")
	}
	b.keys("e", "enter")
	b.until("terminal hand-off", func() bool { return b.execs == 1 })
	b.settle()
	if b.execs != 1 {
		t.Fatalf("Enter in the shell form must hand the terminal over once, got %d", b.execs)
	}
	if b.m.dlg != nil {
		t.Fatal("the form is still open during the hand-off")
	}

	// The command can be edited; quoting is respected, an open quote refused.
	for in, want := range map[string]string{
		"sh":                       "sh",
		"bash -l":                  "bash|-l",
		`sh -c 'echo "a b"; exit'`: `sh|-c|echo "a b"; exit`,
		`  psql   -U  "my user"  `: "psql|-U|my user",
	} {
		got, err := splitCommand(in)
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("splitCommand(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := splitCommand("sh -c 'oops"); err == nil {
		t.Error("an unterminated quote must be refused")
	}
	if _, err := splitCommand("   "); err == nil {
		t.Error("an empty command must be refused")
	}
}

// ---- connection ------------------------------------------------------------------------

func TestDaemonGoesAwayAndComesBack(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.see("● connected")
	b.srv.Stop()
	b.see("DISCONNECTED", "Lost the connection to the Docker daemon at "+b.srv.Host())
	// The last known data stays, marked, and the desk keeps answering keys.
	b.keys("r")
	b.see("stale list — refresh failed", "shop-web-1")
	b.keys("1", "down", "down")
	b.see("2 Volumes", "shop_pgdata")
	b.keys("tab", "tab")
	if b.m.focus != PaneDetails {
		t.Fatal("the desk stopped responding to keys while disconnected")
	}
	// An action while disconnected fails visibly and harmlessly.
	b.keys("2", "X")
	b.see("could not read the daemon's current state")
	if b.m.dlg != nil {
		t.Fatal("a dialog opened on stale data")
	}
	b.assertFrameGeometry()

	if err := b.srv.Restart(); err != nil {
		t.Fatal(err)
	}
	b.see("● connected", "event stream resumed")
	b.notSee("refresh failed")
	// Live again: a change shows up by itself.
	if _, err := b.eng.CreateVolume(context.Background(), "after-reconnect"); err != nil {
		t.Fatal(err)
	}
	b.see("after-reconnect")
}

func TestDaemonErrorOnMutationIsShownAndTheDeskLives(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.srv.FailNext("POST", `/stop$`, 500, "cannot stop container: tried to kill container, but did not receive an exit event", 1)
	b.keys("t", "y")
	b.finishJobs()
	b.see("✗ stop container redis-cache", "did not receive an exit event")
	if b.m.status.tone != toneBad {
		t.Fatal("the failure was not announced")
	}
	// Still usable: the same action works on the next try.
	b.keys("t", "y")
	b.finishJobs()
	b.see("✓ stop container redis-cache")
}
