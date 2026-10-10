// Package e2e drives the real docker-desk binary on a pseudo-terminal against
// the fake daemon: keys in, emulated screen out.
package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

var (
	updateScreens = flag.Bool("update-screens", false, "write captured screens to docs/screens")
	showScreens   = flag.Bool("show-screens", false, "print every captured screen")
	binary        string
)

const wait = 8 * time.Second

func TestMain(m *testing.M) {
	flag.Parse()
	engine.InstallTestGuard()
	dir, err := os.MkdirTemp("", "dd-e2e")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "docker-desk")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/docker-desk")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building docker-desk failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type desk struct {
	t    *testing.T
	srv  *fakeengine.Server
	term *ptyscreen.Term
}

func startFake(t *testing.T, seed func(*fakeengine.Server)) *fakeengine.Server {
	t.Helper()
	srv := fakeengine.New()
	srv.StatsInterval = 100 * time.Millisecond
	srv.PullStepDelay = 15 * time.Millisecond
	if seed != nil {
		seed(srv)
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func launch(t *testing.T, srv *fakeengine.Server, rows, cols int, args ...string) *desk {
	t.Helper()
	term, err := ptyscreen.Start(rows, cols, []string{"DOCKER_HOST=" + srv.Host()}, binary, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return &desk{t: t, srv: srv, term: term}
}

func (d *desk) see(subs ...string) {
	d.t.Helper()
	if err := d.term.WaitFor(wait, subs...); err != nil {
		d.t.Fatal(err)
	}
}

func (d *desk) gone(sub string) {
	d.t.Helper()
	if err := d.term.WaitGone(wait, sub); err != nil {
		d.t.Fatal(err)
	}
}

func (d *desk) keys(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		if err := d.term.Send(k); err != nil {
			d.t.Fatal(err)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// consent presses the keys that proceed with a dialog, after reading it for
// a moment as a person does: a dialog ignores such keys in its first quarter
// of a second, so that nothing typed ahead of it can consent.
func (d *desk) consent(keys ...string) {
	d.t.Helper()
	time.Sleep(320 * time.Millisecond)
	d.keys(keys...)
}

// shot records the current screen under a name.
func (d *desk) shot(name string) string {
	d.t.Helper()
	screen := d.term.Screen()
	if *showScreens {
		fmt.Printf("\n===== %s =====\n%s\n", name, screen)
	}
	if *updateScreens {
		dir := filepath.Join("..", "..", "docs", "screens")
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(screen+"\n"), 0o644); err != nil {
			d.t.Fatal(err)
		}
	}
	return screen
}

func (d *desk) quit() int {
	d.t.Helper()
	code, err := d.term.Wait(wait)
	if err != nil {
		d.t.Fatal(err)
	}
	return code
}

// TestSession is one continuous sitting at the desk: look around, act, watch
// the engine answer, send a slow job off, prune, get refused, drop into a
// shell and come back, quit. Each step asserts on the screen and on what the
// fake daemon actually received.
func TestSession(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 42, 150)
	P := ptyscreen.Enter

	// --- the desk: five panes, and which daemon it is pointed at ----------
	d.see("Docker Desk", srv.Host(), "ctx default", "● connected",
		"1 Kinds", "2 Containers (12)", "3 Details", "4 Events", "5 Jobs", "shop-web-1")
	if !d.term.AltScreen() {
		t.Fatal("the desk is not on the alternate screen")
	}
	d.see("%") // live CPU figures have arrived in the list
	d.shot("01-desk")

	// --- moving between kinds: the list follows the highlight -------------
	d.keys("1", ptyscreen.Down)
	d.see("2 Images (14, 15 refs)", "REPOSITORY", "nginx", "lts-alpine", "USED BY")
	d.shot("02-images")
	d.keys(ptyscreen.Down)
	d.see("2 Volumes (7)", "shop_pgdata", "(anonymous)")
	d.keys(ptyscreen.Down)
	d.see("2 Networks (7)", "shop_default", "bridge")
	d.keys(ptyscreen.Down)
	d.see("2 Compose (2)", "blog", "shop", "Grouped by the com.docker.compose.project label")
	d.shot("03-compose")
	d.keys(ptyscreen.Up, ptyscreen.Up, ptyscreen.Up, ptyscreen.Up)
	d.see("2 Containers (12)")

	// --- filter to one container ------------------------------------------
	d.keys("2", "/", "s", "h", "o", "p", "-", "w", "e", "b", P)
	d.see("2 Containers (1 of 12)", "filter: shop-web", "3 Details: shop-web-1")

	// --- act: stop. One key opens a confirmation and sends nothing --------
	srv.ResetRequests()
	d.keys("t")
	d.see("Stop container", "shop-web-1", "daemon", srv.Host(), "stop signal", "y proceed", "Enter does nothing here")
	d.shot("04-confirm-stop")
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("the action key alone sent %d mutating request(s): %v", n, srv.Mutations())
	}
	d.keys(P, " ", "x") // Enter and other keys are not consent
	time.Sleep(150 * time.Millisecond)
	d.see("Stop container")
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("Enter confirmed a state change: %v", srv.Mutations())
	}
	d.consent("y")

	// --- the engine answers in the other pane ------------------------------
	d.see("ctr stop       shop-web-1", "ctr die exit 0 shop-web-1", "✓ stop container shop-web-1", "○ exited")
	d.gone("Stop container")
	if st, _ := srv.ContainerState("shop-web-1"); st != "exited" {
		t.Fatalf("shop-web-1 is %s, want exited", st)
	}
	if muts := srv.Mutations(); len(muts) != 1 || !strings.HasSuffix(muts[0].Path, "/stop") {
		t.Fatalf("want exactly one stop request, got %v", muts)
	}
	d.shot("05-engine-answered")

	// --- logs of a stopped container, then start it and follow ------------
	d.keys("]")
	d.see("[Logs]", "nginx/1.27.0 started", "end of output: the container is not running")
	d.keys("s")
	d.see("Start container", "shop-web-1")
	d.consent("y")
	d.see("● following", "container started", "request handled")
	d.shot("06-logs-following")

	// --- live statistics ------------------------------------------------------
	d.keys("]")
	d.see("[Stats]", "CPU", "MEM", "NET", "DISK", "PIDS", "samples")
	d.shot("07-stats")
	d.keys("]")
	d.see("[Inspect]", "\"Id\":")
	d.keys("]")
	d.see("[Info]")

	// --- a slow job, sent off; the desk stays usable ----------------------------
	d.keys(ptyscreen.Esc) // clear the filter
	d.see("2 Containers (12)")
	d.keys("1", ptyscreen.Down, "2", "P")
	d.see("Pull image", "Image reference:", "one tag, never all of them")
	d.keys("h", "e", "l", "l", "o", "-", "w", "o", "r", "l", "d")
	d.see("Will pull exactly: hello-world:latest")
	d.shot("08-pull-form")
	d.keys(P)
	d.see("pull image hello-world:latest", "1 running")
	// While it runs: move to another kind and back. Nothing waits for the pull.
	d.keys("1", ptyscreen.Down)
	d.see("2 Volumes (7)")
	d.shot("09-pull-running")
	d.see("✓ pull image hello-world:latest", "img pull       hello-world")
	if !srv.HasImage("hello-world:latest") {
		t.Fatal("the pulled image is missing")
	}

	// --- prune: the exact set is shown, typed word required, only that set dies --
	before := srv.VolumeNames()
	srv.ResetRequests()
	d.keys("2", "X")
	d.see("Prune volumes", "anonymous volumes no container references", "Exactly these 2 will be deleted",
		"(anonymous)", "Type prune and press Enter", "Tab switch to all unused")
	d.shot("10-prune-preview")
	d.keys(ptyscreen.Tab)
	d.see("Exactly these 3 will be deleted", "old-backups", "Named volumes usually hold data")
	d.keys(ptyscreen.Tab)
	d.see("Exactly these 2 will be deleted")
	d.consent("p", "r", "u", "n", P) // not the word
	d.see("That is not \"prune\". Nothing was sent.")
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("a mismatched word sent %d request(s)", n)
	}
	d.keys("e", P)
	d.see("✓ prune volumes 2 volumes", "2 Volumes (5)", "vol destroy")
	after := srv.VolumeNames()
	if len(after) != len(before)-2 {
		t.Fatalf("prune removed %d volumes, the preview listed 2", len(before)-len(after))
	}
	for _, v := range after {
		if v == "old-backups" || v == "shop_pgdata" {
			continue
		}
	}
	if muts := srv.Mutations(); len(muts) != 2 {
		t.Fatalf("want exactly 2 DELETEs, got %v", muts)
	}

	// --- refusal: a running container is not force-removed ------------------------
	srv.ResetRequests()
	d.keys("1", ptyscreen.Up, ptyscreen.Up, "2", "/", "r", "e", "d", "i", "s", P, "d")
	d.see("Remove container", "redis-cache", "REFUSED — nothing will be sent to the daemon", "never force-removes")
	d.shot("11-remove-refused")
	d.keys("r", "e", "m", "o", "v", "e", P) // there is nothing to type into
	time.Sleep(150 * time.Millisecond)
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("a refused removal sent %d request(s)", n)
	}
	if _, ok := srv.ContainerState("redis-cache"); !ok {
		t.Fatal("redis-cache was removed")
	}
	d.keys(ptyscreen.Esc)
	d.gone("REFUSED")

	// --- the shell: the terminal is handed over, and handed back --------------------
	d.keys(ptyscreen.Esc, "/", "s", "h", "o", "p", "-", "a", "p", "i", P)
	d.see("3 Details: shop-api-1")
	d.keys("e")
	d.see("Open a shell", "shop-api-1", "Command to run inside the container:", "exec bash || exec sh")
	d.shot("12-shell-form")
	d.consent(P)
	d.see("docker-desk: sh in shop-api-1", ":/#")
	if d.term.AltScreen() {
		t.Fatal("the desk is still on screen while the shell runs")
	}
	d.keys("e", "c", "h", "o", " ", "i", "n", "s", "i", "d", "e", "-", "o", "k", P)
	d.see("inside-ok\n")
	d.keys("s", "t", "t", "y", " ", "s", "i", "z", "e", P)
	d.see("42 150")
	d.shot("13-in-shell")
	d.keys("e", "x", "i", "t", P)
	d.see("Back from the shell in shop-api-1", "2 Containers (1 of 12)", "3 Details: shop-api-1", "4 Events", "exec_die exit 0")
	if !d.term.AltScreen() {
		t.Fatal("the desk did not return to the alternate screen")
	}
	d.shot("14-back-from-shell")
	// The desk is fully alive again: keys work and streams are current.
	d.keys(ptyscreen.Esc)
	d.see("2 Containers (12)")

	// --- help, then quit: the terminal is given back ---------------------------------
	d.keys("?")
	d.see("Keys", "Docker Desk talks to one daemon: "+srv.Host(), "Everywhere")
	d.keys("G")
	d.see("How changes are confirmed", "Nothing is ever forced")
	d.shot("15-help")
	d.keys(ptyscreen.Esc)
	d.gone("How changes are confirmed")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	raw := d.term.Raw()
	if !strings.HasSuffix(strings.TrimRight(raw[strings.LastIndex(raw, "\x1b[?1049"):], "\r\n\x1b[?25h"), "\x1b[?1049l") && d.term.AltScreen() {
		t.Fatal("the alternate screen was not left on quit")
	}
	if d.term.AltScreen() {
		t.Fatal("still on the alternate screen after quit")
	}
	// Nothing the session did was forced.
	for _, r := range srv.Requests() {
		if strings.Contains(strings.ToLower(r.Query), "force") {
			t.Fatalf("a forced request was sent: %s", r)
		}
	}
}

// TestResponsiveUnderFlood measures, on the real binary through a real pty,
// how long a key takes to show on screen while a container floods its log at
// 200,000 lines per second with statistics and events streaming too.
func TestResponsiveUnderFlood(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "aa-flood", Image: "app:1", LogRate: 200_000})
		for i := 0; i < 6; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("svc-%d", i), Image: "app:1", LogRate: 20})
		}
	})
	d := launch(t, srv, 45, 160)
	d.see("2 Containers (7)", "aa-flood")
	d.keys("]")
	d.see("[Logs]", "● following", "older lines scrolled out")
	before := srv.LinesWritten("aa-flood")
	t0 := time.Now()
	// The log view's own counter says how many lines the desk has taken in.
	scrolledOut := func() float64 {
		m := regexp.MustCompile(`(\d+) older lines scrolled out`).FindStringSubmatch(d.term.Screen())
		if m == nil {
			return 0
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		return n
	}
	ingestedBefore := scrolledOut()

	titles := []string{"◆ 1 Kinds", "◆ 2 Containers", "◆ 3 Details", "◆ 4 Events", "◆ 5 Jobs"}
	focus := 1
	var lat []time.Duration
	for i := 0; i < 40; i++ {
		focus = (focus + 1) % len(titles)
		want := titles[focus]
		start := time.Now()
		if err := d.term.Send(ptyscreen.Tab); err != nil {
			t.Fatal(err)
		}
		deadline := start.Add(2 * time.Second)
		for !strings.Contains(d.term.Screen(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("Tab %d did not show %q within 2 s under flood", i, want)
			}
			time.Sleep(time.Millisecond)
		}
		lat = append(lat, time.Since(start))
		time.Sleep(40 * time.Millisecond)
	}
	elapsed := time.Since(t0).Seconds()
	rate := float64(srv.LinesWritten("aa-flood")-before) / elapsed
	ingest := (scrolledOut() - ingestedBefore) / elapsed
	sorted := append([]time.Duration(nil), lat...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	p50, p95, worst := sorted[len(sorted)/2], sorted[len(sorted)*95/100], sorted[len(sorted)-1]
	t.Logf("flood: written %.0f lines/s, ingested by the desk %.0f lines/s; key → screen through the pty: p50 %s, p95 %s, max %s",
		rate, ingest, p50.Round(time.Millisecond), p95.Round(time.Millisecond), worst.Round(time.Millisecond))
	if ingest < 50_000 {
		t.Fatalf("only %.0f lines/s reached the desk (%.0f written); the test proves nothing below 50,000", ingest, rate)
	}
	if p95 > 100*time.Millisecond {
		if !raceEnabled {
			t.Fatalf("p95 key-to-screen latency under flood is %s (bound 100 ms)", p95)
		}
		t.Logf("(race detector on: the harness itself is slowed, so the 100 ms bound is not asserted in this run)")
	}
	d.shot("17-under-flood")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestCompactLayoutAndResize(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 24, 80)
	d.see("1 Kinds", "2 Containers (12)", "3 Details", "4 Events", "5 Jobs", srv.Host(), "connected")
	d.shot("16-compact-80x24")

	// Selection and focus survive a resize.
	d.keys("/", "r", "e", "d", "i", "s", ptyscreen.Enter, "]")
	d.see("3 Details: redis-cache", "[Logs]")
	if err := d.term.Resize(20, 60); err != nil {
		t.Fatal(err)
	}
	d.see("needs at least 80×24", srv.Host())
	d.gone("1 Kinds")
	if err := d.term.Resize(40, 150); err != nil {
		t.Fatal(err)
	}
	d.see("1 Kinds", "5 Jobs", "3 Details: redis-cache", "[Logs]", "filter: redis", "◆ 2 Containers")
	d.keys("q")
	d.quit()
}

// TestReadOnlyFlag: with --read-only nothing but GET/HEAD ever leaves the
// program, whatever is pressed.
func TestReadOnlyFlag(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140, "--read-only")
	d.see("[READ-ONLY]", "2 Containers (12)")
	for _, k := range []string{"s", "t", "R", "p", "u", "K", "d", "e"} {
		d.keys(k)
		d.see("Read-only mode")
		d.keys(ptyscreen.Esc)
	}
	d.keys("1", ptyscreen.Down, "2", "P")
	d.see("Read-only mode: \"pull…\" is disabled")
	d.keys("X", "d", "1", ptyscreen.Down, "2", "n", "X", "d", "1", ptyscreen.Down, "2", "n", "c", "D", "X", "d", "1", ptyscreen.Down, "2", "t", "d")
	// Looking still works.
	d.keys("1", "g", "2", "]")
	d.see("[Logs]", "● following")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	for _, r := range srv.Requests() {
		if r.Mutating() {
			t.Fatalf("read-only mode sent %s", r)
		}
	}
}

func TestSurvivesDaemonRestart(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140)
	d.see("● connected", "shop-web-1")
	srv.Stop()
	d.see("DISCONNECTED", "Lost the connection to the Docker daemon at "+srv.Host())
	d.keys("1", ptyscreen.Down)
	d.see("2 Images", "nginx") // last known data, still navigable
	d.shot("18-disconnected")
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	d.see("● connected", "event stream resumed")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestDoesNotOpenWithoutADaemon: the workspace must not be drawn at all.
func TestDoesNotOpenWithoutADaemon(t *testing.T) {
	cases := []struct {
		name, host string
		want       []string
	}{
		{"missing socket", "unix:///tmp/docker-desk-e2e-no-such.sock", []string{"the Docker daemon is not reachable", "unix:///tmp/docker-desk-e2e-no-such.sock", "does not exist", "The workspace was not opened"}},
		{"remote endpoint", "tcp://192.0.2.10:2375", []string{"refusing to start", "tcp://192.0.2.10:2375", "only talks to a local daemon", "The workspace was not opened"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, err := ptyscreen.Start(30, 100, []string{"DOCKER_HOST=" + tc.host}, binary)
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			code, err := term.Wait(wait)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			raw := term.Raw()
			if strings.Contains(raw, "\x1b[?1049h") || term.AltScreen() {
				t.Error("the full-screen workspace was opened")
			}
			for _, w := range tc.want {
				if !strings.Contains(raw, w) {
					t.Errorf("message does not contain %q:\n%s", w, raw)
				}
			}
		})
	}
}

func TestQuitWhileAJobRuns(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140)
	d.see("slow-stopper")
	d.keys("/", "s", "l", "o", "w", ptyscreen.Enter, "t")
	d.see("Stop container", "slow-stopper")
	d.consent("y")
	d.see("1 running")
	d.keys("q")
	d.see("Quit Docker Desk?", "stop container slow-stopper")
	d.keys("n")
	d.gone("Quit Docker Desk?")
	d.keys("q", "y")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	// The note is written as the process exits; give the pty a moment to
	// deliver its last bytes.
	const note = "job(s) were still running and have been cancelled"
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(d.term.Raw(), note); {
		if time.Now().After(deadline) {
			t.Fatalf("no note about the cancelled job after quitting; last output:\n%q", lastBytes(d.term.Raw(), 300))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRealDaemonReadOnlyTour runs the real binary against a REAL daemon in
// read-only mode and visits every pane, kind and tab. It is opt-in
// (DOCKER_DESK_REAL_HOST) and can only read: --read-only makes the program
// refuse anything but GET/HEAD below the UI. Screens are printed with
// -show-screens and never written to docs (they describe a real machine).
func TestRealDaemonReadOnlyTour(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST to run the read-only tour of a real daemon")
	}
	term, err := ptyscreen.Start(45, 160, []string{"DOCKER_HOST=" + host}, binary, "--read-only")
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desk{t: t, term: term}
	save := *updateScreens
	*updateScreens = false
	defer func() { *updateScreens = save }()

	d.see("Docker Desk", host, "[READ-ONLY]", "● connected", "1 Kinds", "2 Containers", "3 Details", "4 Events", "5 Jobs")
	time.Sleep(1500 * time.Millisecond) // let statistics arrive
	d.shot("real-containers")
	for _, tab := range []string{"[Logs]", "[Stats]", "[Inspect]", "[Info]"} {
		d.keys("]")
		d.see(tab)
		time.Sleep(700 * time.Millisecond)
		d.shot("real-container-" + strings.Trim(tab, "[]"))
	}
	d.keys(ptyscreen.Down)
	time.Sleep(400 * time.Millisecond)
	d.shot("real-second-container")
	// Every mutating key, on real objects: refused before anything is sent.
	for _, k := range []string{"s", "t", "R", "p", "u", "K", "d", "e"} {
		d.keys(k)
		d.see("Read-only mode")
	}
	for _, kind := range []string{"2 Images", "2 Volumes", "2 Networks", "2 Compose"} {
		d.keys("1", ptyscreen.Down, "2")
		d.see(kind)
		time.Sleep(400 * time.Millisecond)
		d.shot("real-" + strings.ToLower(strings.TrimPrefix(kind, "2 ")))
		for _, k := range []string{"d", "X", "n", "P", "c", "D", "t"} {
			d.keys(k)
		}
		d.keys(ptyscreen.Esc)
		d.keys("]")
		time.Sleep(300 * time.Millisecond)
		d.keys("[")
	}
	d.keys("4", ptyscreen.Up, "G", "5", "?")
	d.see("Keys", "Docker Desk talks to one daemon: "+host)
	d.keys("G")
	d.see("This desk is READ-ONLY")
	d.keys(ptyscreen.Esc, "q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if strings.Contains(d.term.Raw(), "started:") {
		t.Fatal("a job was started against the real daemon")
	}
}

// TestNoColor: with NO_COLOR the desk emits no colour at all and focus is
// still unmistakable (heavy border and marker).
func TestNoColor(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	term, err := ptyscreen.Start(40, 140, []string{"DOCKER_HOST=" + srv.Host(), "NO_COLOR=1"}, binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, srv: srv, term: term}
	d.see("┏━ ◆ 2 Containers (12)", "┌─ 1 Kinds", "┌─ 3 Details", "▸ ")
	d.keys(ptyscreen.Tab)
	d.see("┏━ ◆ 3 Details", "┌─ 2 Containers (12)")
	d.keys("]")
	d.see("[Logs]")
	d.keys("q")
	d.quit()
	raw := d.term.Raw()
	for _, colour := range []string{"\x1b[38;", "\x1b[48;", "\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[34m", "\x1b[9"} {
		if strings.Contains(raw, colour) {
			t.Fatalf("NO_COLOR is set but the output contains the colour sequence %q", colour)
		}
	}
}

// TestMakeSandbox runs the documented one-liner: fake daemon plus desk.
func TestMakeSandbox(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	sock := filepath.Join(t.TempDir(), "sb.sock")
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	term, err := ptyscreen.Start(40, 140, []string{"GOFLAGS=", "GOPATH=" + os.Getenv("GOPATH"), "GOCACHE=" + os.Getenv("GOCACHE")},
		"make", "-C", root, "--no-print-directory", "sandbox", "SANDBOX_SOCKET="+sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, term: term}
	if err := term.WaitFor(60*time.Second, "Docker Desk", "unix://"+sock, "on sandbox", "shop-web-1"); err != nil {
		t.Fatal(err)
	}
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("make sandbox exited with %d:\n%s", code, term.Raw())
	}
	// The fake daemon it started is gone again.
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sock); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sandbox daemon is still running after the desk quit")
		}
	}
}

func lastBytes(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
