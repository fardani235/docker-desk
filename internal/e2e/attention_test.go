package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// attentionSession starts the demo daemon with one failing probe added, a
// second daemon with trouble of every kind listed as the context "staging",
// and the real binary on the first with the second added from the picker.
func attentionSession(t *testing.T, rows, cols int, args ...string) (d *desk, local, far *fakeengine.Server) {
	t.Helper()
	local = startFake(t, func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddContainer(fakeengine.ContainerSpec{Name: "payments-api", Image: "acme/payments:2.1", Health: "unhealthy", Age: 3 * time.Hour})
	})
	far = fakeengine.New()
	far.Name = "staging-host"
	far.StatsInterval = 100 * time.Millisecond
	far.AddContainer(fakeengine.ContainerSpec{Name: "billing-api", Image: "acme/billing:7", State: "restarting", RestartCount: 14, ExitCode: 1})
	far.AddContainer(fakeengine.ContainerSpec{Name: "billing-db", Image: "postgres:16", Health: "healthy", Age: 26 * time.Hour})
	far.AddContainer(fakeengine.ContainerSpec{Name: "report-batch", Image: "acme/report:1.4", State: "exited", ExitCode: 137, OOMKilled: true, Age: 4 * time.Minute})
	far.AddContainer(fakeengine.ContainerSpec{Name: "edge-proxy", Image: "nginx:1.27", RestartCount: 5, Age: 2 * time.Hour})
	far.AddContainer(fakeengine.ContainerSpec{Name: "queue", Image: "redis:7", Age: 26 * time.Hour})
	far.AddContainer(fakeengine.ContainerSpec{Name: "migrate", Image: "acme/billing:7", State: "exited", Age: 26 * time.Hour})
	cleanup, err := far.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	cfg := t.TempDir()
	writeContext(t, cfg, "staging", far.Host())
	home := t.TempDir()
	env := []string{"DOCKER_HOST=" + local.Host(), "DOCKER_CONFIG=" + cfg, "HOME=" + home, "TMPDIR=" + home}
	d = launchEnv(t, local, rows, cols, env, args...)
	d.see("Docker Desk", "2 Containers (14)")
	d.keys("M")
	d.see("Contexts on "+cfg, "staging")
	d.keys(ptyscreen.Down, ptyscreen.Down)
	d.consent(ptyscreen.Enter)
	d.see("staging  ● connected")
	d.keys(ptyscreen.Esc)
	d.see("2 staging ●")
	return d, local, far
}

var attentionReads = regexp.MustCompile(`^(/_ping|/events|/version|/info|/containers/json|/containers/[^/]+/json)$`)

// TestAttentionAcrossDaemons is one sitting with the real binary and two
// daemons: the ambient count on the desk, H, both daemons' blocks with their
// reasons, an exit on the unfocused daemon read without being asked for,
// Enter to that daemon's container, and H and Esc back. Neither daemon
// receives anything but reads, and the unfocused one nothing but the view's.
func TestAttentionAcrossDaemons(t *testing.T) {
	const rows, cols = 44, 160
	d, local, far := attentionSession(t, rows, cols)

	// --- without the view: health in the STATE cell, and the count ----------
	d.see("payments-api", "● unhealthy", "1 needs attention — H reads exit codes and restarts too", "▲ 1 needs you")
	checkFrame(t, d.shot("87-attention-cue"), rows, cols)

	// Until H, the unfocused daemon has been asked for no container.
	if n := far.CountRequests("GET", `^/containers/`); n != 0 {
		t.Fatalf("%d container requests to the unfocused daemon before the view opened", n)
	}
	local.ResetRequests()
	far.ResetRequests()

	// --- H: every daemon, problems first --------------------------------------
	d.keys("H")
	d.see("Attention — 4 need you · 1 to watch · 20 containers on 2 daemons",
		"14 containers · 2 need you · 0 to watch", "(the desk's list)",
		"payments-api", "running · unhealthy",
		"shop-worker-1", "exited 1 · finished",
		"6 containers · 2 need you · 1 to watch",
		"billing-api", "restarting · exit 1 · 14 restarts",
		"report-batch", "exited 137 · OOM-killed · finished",
		"edge-proxy", "running · 5 restarts",
		"1 fine · 1 unknown (no health verdict) · 1 not a problem (paused, exited 0)",
		far.Host(), local.Host(), "H Esc close")
	screen := d.shot("88-attention")
	// The focused daemon's block first; needs-you before watch.
	for _, pair := range [][2]string{{"payments-api", "billing-api"}, {"billing-api", "report-batch"}, {"report-batch", "edge-proxy"}} {
		if strings.Index(screen, pair[0]) > strings.Index(screen, pair[1]) {
			t.Fatalf("%s is listed after %s:\n%s", pair[0], pair[1], screen)
		}
	}
	// Containers that are fine, unknown or not a problem are not listed.
	for _, hidden := range []string{"billing-db", "queue", "migrate", "redis-cache", "shop-db-1"} {
		if strings.Contains(screen, hidden) {
			t.Fatalf("%s is listed:\n%s", hidden, screen)
		}
	}
	// One list and the candidates' inspects for the unfocused daemon; no
	// list at all for the focused one, whose block is the desk's own.
	if n := far.CountRequests("GET", `^/containers/json$`); n != 1 {
		t.Fatalf("%d list requests to the unfocused daemon", n)
	}
	if n := far.CountRequests("GET", `^/containers/[^/]+/json$`); n != 6 {
		t.Fatalf("%d inspects on the unfocused daemon, want 6", n)
	}

	// --- a quiet view asks nothing ---------------------------------------------
	far.ResetRequests()
	time.Sleep(1500 * time.Millisecond)
	if n := far.CountRequests("GET", `^/containers/`); n != 0 {
		t.Fatalf("%d container requests to the unfocused daemon while nothing happened", n)
	}

	// --- an exit over there: read again, unasked --------------------------------
	far.Crash("queue", 3)
	d.see("6 containers · 3 need you", "queue", "exited 3 · finished")
	if n := far.CountRequests("GET", `^/containers/json$`); n != 1 {
		t.Fatalf("%d list requests for one exit", n)
	}

	// --- Enter: to the container, on its daemon ---------------------------------
	d.keys(ptyscreen.Tab) // staging's block: its first row is the restart loop
	d.keys(ptyscreen.Enter)
	d.see("2 Containers (6)", "billing-api on staging.", "3 Details: billing-api", far.Host())
	d.gone("Attention —")
	checkFrame(t, d.shot("89-attention-jump"), rows, cols)

	// --- back on staging: H, then Esc leaves the desk as it was -----------------
	d.keys("H")
	d.see("Attention — ", "6 containers · 3 need you", "(the desk's list)")
	d.keys(ptyscreen.Esc)
	d.gone("Attention —")
	d.see("3 Details: billing-api")

	for _, srv := range []*fakeengine.Server{local, far} {
		if m := srv.Mutations(); len(m) != 0 {
			t.Fatalf("%s received mutating requests: %v", srv.Name, m)
		}
	}
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// At 80×24 and read-only: the view opens, one block is on screen at a time,
// Tab brings the next, and nothing but reads is sent to either daemon.
func TestAttentionCompactReadOnly(t *testing.T) {
	const rows, cols = 24, 80
	d, local, far := attentionSession(t, rows, cols, "--read-only")
	d.see("READ-ONLY", "1 needs attention, H reads more")
	local.ResetRequests()
	far.ResetRequests()

	d.keys("H")
	d.see("Attention — 4 need you · 1 to watch", "payments-api", "shop-worker-1", "exited 1")
	checkAttentionFrame(t, d.shot("90-attention-80x24-read-only"), rows, cols)
	d.keys(ptyscreen.Tab)
	d.see("billing-api", "restarting · exit 1 · 14 restarts", "report-batch", "OOM-killed", far.Host())
	checkAttentionFrame(t, d.term.Screen(), rows, cols)

	// A mutating key does nothing there but say so.
	d.keys("d")
	d.see("Attention only reads: nothing was sent.")
	d.keys("K", "s", "t", "R", "X", "e", "n", "p", "u")
	d.keys("r")
	if err := d.term.WaitUntil(wait, "r to read staging again", func(string) bool {
		return far.CountRequests("GET", `^/containers/json$`) == 2
	}); err != nil {
		t.Fatal(err)
	}
	d.keys(ptyscreen.Esc)
	d.gone("Attention —")
	d.see("2 Containers (14)")

	for _, srv := range []*fakeengine.Server{local, far} {
		for _, r := range srv.Requests() {
			if r.Method != "GET" && r.Method != "HEAD" {
				t.Fatalf("%s received %s in read-only mode", srv.Name, r)
			}
		}
	}
	// The unfocused daemon saw nothing but its ping, its event stream and
	// the view's reads.
	for _, r := range far.Requests() {
		if !attentionReads.MatchString(r.Path) {
			t.Fatalf("the unfocused daemon received %s", r)
		}
	}
	d.keys("q")
	d.quit()
}

// checkAttentionFrame is checkFrame for a view that covers the panes.
func checkAttentionFrame(t *testing.T, screen string, rows, cols int) {
	t.Helper()
	lines := strings.Split(screen, "\n")
	if len(lines) < rows-1 {
		t.Fatalf("screen has %d lines, terminal has %d", len(lines), rows)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > cols {
			t.Fatalf("line %d is %d cells, terminal is %d:\n%s", i, n, cols, screen)
		}
	}
	if !strings.Contains(screen, "Attention — ") || !strings.Contains(screen, "Daemons") {
		t.Fatalf("the view or the strip went missing:\n%s", screen)
	}
}
