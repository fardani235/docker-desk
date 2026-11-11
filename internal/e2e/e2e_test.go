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
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
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

// shot records the current screen under a name. It waits for the screen to
// hold still for a moment first: the text a test waited for can be on screen
// while the rest of that frame is still arriving, and a capture taken then is
// torn (docs/screens/12-shell-form.txt was, until round two).
func (d *desk) shot(name string) string {
	d.t.Helper()
	screen := d.term.Screen()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		time.Sleep(40 * time.Millisecond)
		again := d.term.Screen()
		if again == screen {
			break
		}
		screen = again
	}
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
		"1 Kinds", "2 Containers (13)", "3 Details", "4 Events", "5 Jobs", "shop-web-1")
	if !d.term.AltScreen() {
		t.Fatal("the desk is not on the alternate screen")
	}
	d.see("%") // live CPU figures have arrived in the list
	d.shot("01-desk")

	// --- moving between kinds: the list follows the highlight -------------
	d.keys("1", ptyscreen.Down)
	d.see("2 Images (16, 17 refs)", "REPOSITORY", "nginx", "lts-alpine", "USED BY")
	d.shot("02-images")
	d.keys(ptyscreen.Down)
	d.see("2 Volumes (7)", "shop_pgdata", "(anonymous)")
	d.keys(ptyscreen.Down)
	d.see("2 Networks (7)", "shop_default", "bridge")
	d.keys(ptyscreen.Down)
	d.see("2 Compose (2)", "blog", "shop", "Grouped by the com.docker.compose.project label")
	d.shot("03-compose")
	d.keys(ptyscreen.Up, ptyscreen.Up, ptyscreen.Up, ptyscreen.Up)
	d.see("2 Containers (13)")

	// --- filter to one container ------------------------------------------
	d.keys("2", "/", "s", "h", "o", "p", "-", "w", "e", "b", P)
	d.see("2 Containers (1 of 13)", "filter: shop-web", "3 Details: shop-web-1")

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
	d.see("[Changes]", "same image on both sides")
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
	d.see("[Processes]", "CMD", "snapshot at")
	d.keys("]")
	d.see("[Inspect]", "\"Id\":")
	d.keys("]")
	d.see("[Info]")

	// --- a slow job, sent off; the desk stays usable ----------------------------
	d.keys(ptyscreen.Esc) // clear the filter
	d.see("2 Containers (13)")
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
	// The Esc above cleared the filter (Enter had closed the refusal); a
	// second Esc in the list would now leave it for Kinds, so focus it.
	d.keys("2", "/", "s", "h", "o", "p", "-", "a", "p", "i", P)
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
	d.see("Back from the shell in shop-api-1", "2 Containers (1 of 13)", "3 Details: shop-api-1", "4 Events", "exec_die exit 0")
	if !d.term.AltScreen() {
		t.Fatal("the desk did not return to the alternate screen")
	}
	d.shot("14-back-from-shell")
	// The desk is fully alive again: keys work and streams are current.
	d.keys(ptyscreen.Esc)
	d.see("2 Containers (13)")

	// --- help, then quit: the terminal is given back ---------------------------------
	d.keys("?")
	d.see("Keys", "Focused daemon: "+srv.Host(), "Everywhere")
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
	d.keys("]", "]")
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
	d.see("1 Kinds", "2 Containers (13)", "3 Details", "4 Events", "5 Jobs", srv.Host(), "connected")
	d.shot("16-compact-80x24")

	// Selection and focus survive a resize.
	d.keys("/", "r", "e", "d", "i", "s", ptyscreen.Enter, "]", "]")
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
	d.see("[READ-ONLY]", "2 Containers (13)")
	for _, k := range []string{"s", "t", "R", "p", "u", "K", "d", "e"} {
		d.keys(k)
		d.see("Read-only mode")
		d.keys(ptyscreen.Esc)
	}
	d.keys("1", ptyscreen.Down, "2", "P")
	d.see("Read-only mode: \"pull…\" is disabled")
	d.keys("X", "d", "1", ptyscreen.Down, "2", "n", "X", "d", "1", ptyscreen.Down, "2", "n", "c", "D", "X", "d", "1", ptyscreen.Down, "2", "t", "d")
	// Looking still works.
	d.keys("1", "g", "2", "]", "]")
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
		{"a scheme the desk does not speak", "npipe:////./pipe/docker_engine", []string{"cannot use this Docker endpoint", "npipe:////./pipe/docker_engine", "unix://, ssh:// and tcp://", "The workspace was not opened"}},
		{"nothing listening on a TCP port", "tcp://" + closedPort(t), []string{"the Docker daemon is not reachable", "via        TCP to 127.0.0.1:", "without TLS", "connection refused at 127.0.0.1:", "The workspace was not opened"}},
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
	for _, tab := range []string{"[Changes]", "[Logs]", "[Stats]", "[Processes]", "[Inspect]", "[Info]"} {
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
	d.see("Keys", "Focused daemon: "+host)
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
	d.see("┏━ ◆ 2 Containers (13)", "┌─ 1 Kinds", "┌─ 3 Details", "▸ ")
	d.keys(ptyscreen.Tab)
	d.see("┏━ ◆ 3 Details", "┌─ 2 Containers (13)")
	d.keys("]", "]")
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

// TestSearchInTheSandbox drives `make sandbox` itself and shows the Details
// pane's two kinds of search on the demo daemon: find in Inspect (nothing
// hidden, the key path named) and filter in Logs (under a flood of 20 000
// lines a second, counted against the lines held).
func TestSearchInTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	const rows, cols = 41, 140 // one row more since the daemon strip took one (M11)
	sock := filepath.Join(t.TempDir(), "sb.sock")
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	term, err := ptyscreen.Start(rows, cols, []string{"GOFLAGS=", "GOPATH=" + os.Getenv("GOPATH"), "GOCACHE=" + os.Getenv("GOCACHE")},
		"make", "-C", root, "--no-print-directory", "sandbox", "SANDBOX_SOCKET="+sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, term: term}
	if err := term.WaitFor(60*time.Second, "on sandbox", "shop-web-1"); err != nil {
		t.Fatal(err)
	}
	typeText := func(s string) {
		for _, r := range s {
			d.keys(string(r))
		}
	}

	// Find, in Inspect: every line stays, the bar counts and names the key.
	d.keys("/")
	typeText("shop-db")
	d.keys(ptyscreen.Enter)
	d.see("3 Details: shop-db-1")
	d.keys("3", "[") // Info wraps back to Inspect
	d.see("[Inspect]", `"Id":`)
	d.keys("/")
	typeText("status")
	d.see("find: status", "1 of 2", "at State.Status")
	d.keys(ptyscreen.Enter, "n")
	d.see("find: status  2 of 2  at State.Health.Status", `"Status": "healthy"`, `"Id":`)
	checkFrame(t, d.shot("35-inspect-find"), rows, cols)
	d.keys("n")
	d.see("1 of 2  at State.Status") // n past the last wraps to the first

	// Esc clears the find, a second Esc goes back to the List, a third
	// clears the List's filter.
	d.keys(ptyscreen.Esc)
	d.gone("find: status")
	d.keys(ptyscreen.Esc, ptyscreen.Esc)
	d.see("2 Containers (13)")

	// Filter, in Logs, under a flood.
	d.keys("/")
	typeText("noisy")
	d.keys(ptyscreen.Enter)
	d.see("3 Details: noisy-logger")
	d.keys("3", "]", "]", "]") // Inspect, Info, Changes, Logs
	d.see("[Logs]", "● following")
	d.keys("/")
	typeText("error")
	d.see("filter: error", " of 5000 match", "upstream timed out")
	d.keys(ptyscreen.Enter)
	d.see("filter: error  ● following · ")
	screen := d.shot("36-logs-filter")
	checkFrame(t, screen, rows, cols)
	if strings.Contains(screen, "request handled") {
		t.Fatal("a line that does not match the filter is on screen")
	}
	d.keys("/")
	typeText("zzz")
	d.see("no match in the 5000 lines held")
	d.keys(ptyscreen.Esc)
	d.see("● following · 5000 lines", "request handled")

	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("make sandbox exited with %d", code)
	}
}

func lastBytes(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// TestImageFindings opens Images › Findings on the demo's shop/api:dev, the
// image with a credential deleted one layer after it was added, and walks to
// that finding — at the wide layout, and at 80×24 in read-only mode.
func TestImageFindings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"24-image-findings", 42, 150, nil},
		{"25-image-findings-80x24", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, "2", "/", "s", "h", "o", "p", "/", "a", ptyscreen.Enter)
			d.see("3 Details: shop/api:dev")
			d.keys("3", "]", "]")
			d.see("[Findings]", "findings · by layer", "live: 2 container(s)")
			// Down to the deleted credential.
			err := d.term.WaitUntil(wait, "the cursor on secret.deleted", func(screen string) bool {
				if strings.Contains(screen, "▸ L2  certain secret.deleted") {
					return true
				}
				_ = d.term.Send("j")
				time.Sleep(30 * time.Millisecond)
				return false
			})
			if err != nil {
				t.Fatal(err)
			}
			screen := d.shot(tc.name)
			if !strings.Contains(screen, "in the image bytes") {
				t.Errorf("the finding does not say the bytes remain:\n%s", screen)
			}
			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 1 {
				t.Errorf("%d exports", n)
			}
			for _, r := range srv.Requests() {
				if r.Mutating() {
					t.Fatalf("analysing sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}

// TestProcessesView opens Containers › Processes on a running container,
// refreshes the snapshot, moves to another container and to a stopped one,
// at the wide layout and at 80×24, without tearing the frame.
func TestProcessesView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
	}{
		{"29-processes", 40, 140},
		{"30-processes-80x24", 24, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols)
			d.see("2 Containers (13)")
			d.keys("/", "s", "h", "o", "p", "-", "w", "e", "b", ptyscreen.Enter)
			d.see("3 Details: shop-web-1")
			d.keys("3", "]", "]", "]", "]")
			d.see("UID", "PID", "CMD", "snapshot at")
			checkFrame(t, d.shot(tc.name), tc.rows, tc.cols)
			tops := func() int { return srv.CountRequests("GET", `^/containers/[^/]+/top$`) }
			if n := tops(); n != 1 {
				t.Fatalf("%d process requests on entry", n)
			}

			d.keys("r")
			if err := d.term.WaitUntil(wait, "a second snapshot", func(string) bool { return tops() == 2 }); err != nil {
				t.Fatal(err)
			}

			// Another running container, then a stopped one.
			d.keys("2", ptyscreen.Esc, "/", "r", "e", "d", "i", "s", ptyscreen.Enter)
			d.see("3 Details: redis-cache", "redis-server")
			d.gone("nginx")
			if err := d.term.WaitUntil(wait, "a snapshot for redis", func(string) bool { return tops() == 3 }); err != nil {
				t.Fatal(err)
			}
			d.keys(ptyscreen.Esc, "/", "w", "o", "r", "k", "e", "r", ptyscreen.Enter)
			d.see("3 Details: shop-worker-1", "is not running", "available only for running containers")
			checkFrame(t, d.term.Screen(), tc.rows, tc.cols)
			if n := tops(); n != 3 {
				t.Fatalf("a stopped container was asked for processes: %d requests", n)
			}
			if muts := srv.Mutations(); len(muts) != 0 {
				t.Fatalf("the Processes view sent %v", muts)
			}
			d.keys("q")
			d.quit()
		})
	}
}

func checkFrame(t *testing.T, screen string, rows, cols int) {
	t.Helper()
	lines := strings.Split(screen, "\n")
	if len(lines) < rows-1 {
		t.Fatalf("screen has %d lines, terminal has %d", len(lines), rows)
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > cols {
			t.Fatalf("line %d is %d cells, terminal is %d:\n%s", i, n, cols, screen)
		}
	}
	if !strings.Contains(screen, "5 Jobs") || !strings.Contains(screen, "4 Events") {
		t.Fatalf("a pane went missing:\n%s", screen)
	}
}

// TestPackagesView opens Images › Findings › Packages on the image whose
// SBOM disagrees with its package database, cycles the state shown,
// searches, scrolls, and closes back onto the Findings tab — wide, and at
// 80×24 read-only — sending nothing but the one archive read.
func TestPackagesView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"31-packages-disagreements", 40, 120, nil},
		{"32-packages-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, "2", "/", "p", "y", "t", "h", "o", "n", ptyscreen.Enter)
			d.see("3 Details: python:3.12-slim")
			d.keys("3", "]", "]")
			d.see("[Findings]", "findings · by layer")
			d.keys("i")
			d.see("Packages — ", "SPDX-2.3 · 70 packages", "dpkg · /var/lib/dpkg/status (layer 4) · 66 packages")
			if tc.cols < 100 {
				d.see("3 undeclared", "6 not checkable")
				packagesFrame(t, d.shot(tc.name), tc.cols)
			} else {
				d.keys("s")
				d.see("showing disagreements", "5 of 73 rows", "libgnutls30", "netcat-openbsd")
				d.keys("j")
				d.see("▸ libcurl4", "added by", "layer 4 · RUN apt-get update && apt-get install -y curl netcat-openbsd")
				packagesFrame(t, d.shot(tc.name), tc.cols)
				d.keys("s", "s", "s", "s", "s", "s") // back to all
				d.see("showing all")
			}
			// Search, then scroll the whole list.
			d.keys("/", "s", "s", "l", ptyscreen.Enter)
			d.see("search: ssl", "libssl3", "openssl")
			d.keys(ptyscreen.Esc)
			for i := 0; i < 80; i++ {
				d.keys("j") // paced: keys sent together arrive as one "jjj…"
			}
			d.see(" 73 of 73 ", "zlib1g")
			packagesFrame(t, d.term.Screen(), tc.cols)
			d.keys(ptyscreen.Esc)
			d.see("[Findings]", "findings · by layer")
			d.gone("Packages — ")
			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 1 {
				t.Errorf("%d exports", n)
			}
			for _, r := range srv.Requests() {
				if r.Mutating() {
					t.Fatalf("the Packages view sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}

// packagesFrame checks a full-screen view's frame: no line wider than the
// terminal, and the box closed at the bottom.
func packagesFrame(t *testing.T, screen string, cols int) {
	t.Helper()
	for i, l := range strings.Split(screen, "\n") {
		if n := utf8.RuneCountInString(l); n > cols {
			t.Fatalf("line %d is %d cells, terminal is %d:\n%s", i, n, cols, screen)
		}
	}
	if !strings.Contains(screen, "┗━") || !strings.Contains(screen, "Esc close") {
		t.Fatalf("the view's frame is not whole:\n%s", screen)
	}
}

// TestChangesView opens Containers › Changes on report-job, whose tag has
// moved to a newer build: the lineage and config are drawn at once, and
// Enter compares the archives as a job — at 120 columns, and at 80×24 in
// read-only mode. No archive is read before Enter, no environment value
// is ever on screen, and nothing but GET and HEAD is sent.
func TestChangesView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"33-changes", 40, 120, nil},
		{"34-changes-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("/", "r", "e", "p", "o", "r", "t", ptyscreen.Enter)
			d.see("3 Details: report-job")
			d.keys("3", "]")
			d.see("[Changes]", "base intact: all 3 layers shared, 2 added", "+ L4")
			exports := func() int { return srv.CountRequests("GET", `^/images(/.+)?/get$`) }
			if n := exports(); n != 0 {
				t.Fatalf("%d archives read before Enter", n)
			}
			d.keys(ptyscreen.Enter)
			d.see("compare images")
			if err := d.term.WaitUntil(wait, "both archives", func(string) bool { return exports() == 2 }); err != nil {
				t.Fatal(err)
			}
			d.see("compare images acme/report:1.4")
			for i := 0; i < 12 && !strings.Contains(d.term.Screen(), "version changed"); i++ {
				d.keys(ptyscreen.PgDn)
				time.Sleep(80 * time.Millisecond)
			}
			d.see("version changed")
			checkFrame(t, d.shot(tc.name), tc.rows, tc.cols)
			for _, v := range fixture.ChangesEnvValues {
				if strings.Contains(d.term.Screen(), v) {
					t.Fatalf("an environment value is on screen")
				}
			}
			for _, r := range srv.Requests() {
				if r.Method != "GET" && r.Method != "HEAD" {
					t.Fatalf("the Changes view sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}
