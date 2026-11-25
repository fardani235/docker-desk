package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// copySession starts the demo daemon, a second daemon listed as the context
// "staging", and the real binary on the first with the second added from
// the picker. Images is shown, filtered to acme/report:1.4 — the demo's
// image whose archive and inspect are one fixture.
func copySession(t *testing.T, rows, cols int, seedFar func(*fakeengine.Server), args ...string) (d *desk, local, far *fakeengine.Server, home string) {
	t.Helper()
	local = startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	far = fakeengine.New()
	far.Name = "staging-host"
	far.StatsInterval = 100 * time.Millisecond
	far.AddImage(4_000_000, time.Hour, "resident:1")
	if seedFar != nil {
		seedFar(far)
	}
	cleanup, err := far.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	cfg := t.TempDir()
	writeContext(t, cfg, "staging", far.Host())
	// A home and a temporary directory of the desk's own: the copy must
	// leave both empty.
	home = t.TempDir()
	env := []string{"DOCKER_HOST=" + local.Host(), "DOCKER_CONFIG=" + cfg, "HOME=" + home, "TMPDIR=" + home}
	d = launchEnv(t, local, rows, cols, env, args...)
	d.see("Docker Desk", "2 Containers (13)")
	d.keys("M")
	d.see("Contexts on "+cfg, "staging")
	d.keys(ptyscreen.Down, ptyscreen.Down)
	d.consent(ptyscreen.Enter)
	d.see("staging  ● connected")
	d.keys(ptyscreen.Esc)
	d.see("2 staging ●")
	d.imagesRow("acme/report", "acme/report:1.4")
	return d, local, far, home
}

// TestCopyImageBetweenDaemons is one sitting with the real binary and two
// daemons: C on an image, the destination chooser, the typed confirmation
// that names both daemons, the job that streams the archive from one into
// the other, and the refusal to copy what is already there. The source
// daemon receives nothing but reads; the target exactly one load; this
// machine no file.
func TestCopyImageBetweenDaemons(t *testing.T) {
	d, local, far, home := copySession(t, 44, 160, nil)
	local.ResetRequests()
	far.ResetRequests()

	// --- the chooser: nothing sent yet ---------------------------------------
	d.keys("C")
	d.see("Copy image to another daemon", "Copy acme/report:1.4", "from this daemon to:", "staging", far.Host(), "engine staging-host", "read both daemons, then confirm")
	d.shot("81-copy-image-destination")
	if n := far.CountRequests(http.MethodGet, `^/images/`); n != 0 || local.CountRequests(http.MethodGet, `^/images/get$`) != 0 {
		t.Fatalf("the chooser asked about the image: %v", far.Requests())
	}

	// --- the confirmation: both daemons, the tag effect, the typed word ------
	d.consent(ptyscreen.Enter)
	d.see("Type 1.4 and press Enter to proceed", "from", "to         staging", far.Host(), "(context staging)", "platform   linux/amd64 · tag acme/report:1.4",
		"the tag is created there", "no file is written on this machine and no", "The source is only read")
	d.shot("82-copy-image-confirm")
	// y is not consent, nor is Enter without the word.
	d.consent("y", ptyscreen.Enter)
	d.see(`That is not "1.4". Nothing was sent.`)
	if len(far.Mutations())+len(local.Mutations()) != 0 || local.CountRequests(http.MethodGet, `^/images/get$`) != 0 {
		t.Fatal("something was sent before the word was typed")
	}
	d.keys("\x7f", "1", ".", "4", ptyscreen.Enter)

	// --- the job --------------------------------------------------------------
	if err := d.term.WaitFor(20*time.Second, "✓ copy image acme/report:1.4 on staging from"); err != nil {
		t.Fatal(err)
	}
	if !far.HasImage("acme/report:1.4") || !far.HasImage("resident:1") {
		t.Fatalf("the target's references: %v", far.ImageRefs())
	}
	if m := local.Mutations(); len(m) != 0 {
		t.Fatalf("the source received mutating requests: %v", m)
	}
	if m := far.Mutations(); len(m) != 1 || m[0].Path != "/images/load" {
		t.Fatalf("the target's mutations: %v", m)
	}
	if n := local.CountRequests(http.MethodGet, `^/images/get$`); n != 1 {
		t.Fatalf("%d archive requests on the source", n)
	}
	for _, srv := range []*fakeengine.Server{local, far} {
		for _, r := range srv.Requests() {
			if strings.Contains(r.Path, "/push") || strings.Contains(r.Path, "/distribution") || r.Path == "/images/create" || r.Path == "/auth" {
				t.Fatalf("a registry request was made: %s", r)
			}
		}
	}
	// The job's own account, in full.
	d.keys("5", ptyscreen.Enter)
	d.see("succeeded", "copy image acme/report:1.4", "on daemon "+far.Host(), "from daemon "+local.Host(),
		"streaming: GET /images/get on the source → POST /images/load on the target",
		"target: Loaded image: acme/report:1.4", "verified on the target", "acme/report:1.4 was created on the target",
		"the source was only read")
	d.shot("83-copy-image-done")
	d.keys(ptyscreen.Esc, "2")

	// --- no file on this machine ------------------------------------------------
	if left, _ := os.ReadDir(home); len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("the copy left files on this machine: %v", names)
	}

	// --- again: the same build is already there ----------------------------------
	far.ResetRequests()
	d.keys("C")
	d.see("Copy image to another daemon")
	d.consent(ptyscreen.Enter)
	d.see("REFUSED", "Already present", "Esc close")
	d.shot("84-copy-image-already-present")
	d.consent("1", ".", "4", ptyscreen.Enter)
	d.gone("REFUSED")
	if len(far.Mutations()) != 0 {
		t.Fatalf("a refused copy sent %v", far.Mutations())
	}

	// --- the target's own list, once it is focused ------------------------------
	d.keys(">")
	d.see("◆2 staging", "acme/report")

	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if left, _ := filepath.Glob(filepath.Join(home, "*")); len(left) != 0 {
		t.Fatalf("files left behind at exit: %v", left)
	}
}

// TestCopyImageMovesATagAndCancels: the target's tag names another image —
// the dialog says so first — and a copy cancelled mid-stream claims nothing,
// while neither daemon can be removed as it runs. At 80×24.
func TestCopyImageMovesATagAndCancels(t *testing.T) {
	var other string
	d, local, far, _ := copySession(t, 24, 80, func(s *fakeengine.Server) {
		other = s.AddImage(9_000_000, time.Hour, "acme/report:1.4")
	})
	// Slow enough to be cancelled: the fake reads the archive in pieces.
	far.LoadChunkDelay = 400 * time.Millisecond
	d.keys("C")
	d.see("Copy image to another daemon")
	d.consent(ptyscreen.Enter)
	d.see("Type 1.4 and press Enter to proceed")
	withinTerminal(t, d.shot("85-copy-image-tag-moves-80x24"), 24, 80)
	// The warning is in the part that scrolls on so small a terminal.
	d.keys(ptyscreen.PgDn, ptyscreen.PgDn, ptyscreen.PgDn)
	d.see("names image "+other[:12], "moves the tag")
	d.consent("1", ".", "4", ptyscreen.Enter)
	d.see("1 running")

	// Neither daemon can be removed while the copy runs.
	d.keys("M")
	d.see("1 job is running on it — copy image acme/report:1.4")
	d.keys("d") // the picker opens on the focused daemon: the source
	d.see("Cannot remove", "copy image acme/report:1.4")
	d.keys(ptyscreen.Down, "d") // and the target
	d.see("Cannot remove staging")
	d.keys(ptyscreen.Esc)

	// Cancel it from the Jobs pane.
	d.keys("5", "c")
	if err := d.term.WaitFor(20*time.Second, "cancelled: copy image acme/report:1.4"); err != nil {
		t.Fatal(err)
	}
	d.keys(ptyscreen.Enter)
	d.see("cancelled", "both streams were closed", "no image and no moved tag is claimed")
	withinTerminal(t, d.shot("86-copy-image-cancelled-80x24"), 24, 80)
	d.keys(ptyscreen.Esc)

	// The tag still names the image it named: nothing moved, nothing loaded.
	waitFor(t, "the target to see its request end", func() bool { return len(far.LoadRequests()) == 1 })
	if r := far.LoadRequests()[0]; r.ImageID != "" || r.Err == "" {
		t.Fatalf("the target after a cancel: %+v", r)
	}
	if ids := far.ImageIDs(); len(ids) != 2 {
		t.Fatalf("the target's images after a cancel: %v", ids)
	}
	if m := local.Mutations(); len(m) != 0 {
		t.Fatalf("the source received %v", m)
	}
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestCopyImageReadOnly: with --read-only the action is refused before the
// chooser, and neither daemon receives anything but reads.
func TestCopyImageReadOnly(t *testing.T) {
	d, local, far, _ := copySession(t, 40, 140, nil, "--read-only")
	local.ResetRequests()
	far.ResetRequests()
	d.keys("C")
	d.see(`Read-only mode: "copy to daemon…" is disabled. Nothing was sent to the daemon.`)
	time.Sleep(200 * time.Millisecond)
	if s := d.term.Screen(); strings.Contains(s, "Copy image to another daemon") {
		t.Fatalf("read-only opened the chooser:\n%s", s)
	}
	for _, srv := range []*fakeengine.Server{local, far} {
		for _, r := range srv.Requests() {
			if r.Mutating() || r.Path == "/images/get" {
				t.Fatalf("read-only sent %s", r)
			}
		}
	}
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// withinTerminal checks a captured screen against the terminal's size: a dialog may
// cover the panes, but nothing may be drawn outside the terminal.
func withinTerminal(t *testing.T, screen string, rows, cols int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) > rows {
		t.Fatalf("the screen has %d lines, the terminal %d:\n%s", len(lines), rows, screen)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > cols {
			t.Fatalf("line %d is %d cells, the terminal has %d:\n%s", i+1, n, cols, screen)
		}
	}
}
