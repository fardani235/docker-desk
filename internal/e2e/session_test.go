package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
	"github.com/fardani235/docker-desk/internal/sshtest"
)

// writeContext stores a Docker context the way `docker context create` does.
func writeContext(t *testing.T, cfg, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(cfg, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTwoDaemonsOneOverSSH is one sitting with two daemons: the launch one on
// a local socket, and one on "another machine" reached through the real ssh
// client and a real sshd, added from the picker. Add, focus, act there, get
// a removal refused because a pull runs, see a background exit as a notice
// and not as an event, remove it — its ssh process going with it — and quit
// with nothing left behind. Skipped without OpenSSH (sshtest).
func TestTwoDaemonsOneOverSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	local := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	far := fakeengine.New()
	far.Name = "far-away"
	far.StatsInterval = 100 * time.Millisecond
	far.PullStepDelay = 200 * time.Millisecond // a pull that is still running when removal is tried
	far.AddContainer(fakeengine.ContainerSpec{Name: "far-web", Image: "nginx:1.27"})
	far.AddContainer(fakeengine.ContainerSpec{Name: "far-db", Image: "postgres:16"})
	cleanup, err := far.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	url := sshd.URL(far.Socket())

	cfg := t.TempDir()
	writeContext(t, cfg, "far", url)
	tmp, err := os.MkdirTemp("", "dde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	env := []string{"DOCKER_HOST=" + local.Host(), "DOCKER_CONFIG=" + cfg, "DOCKER_DESK_SSH=" + sshd.CommandString(), "TMPDIR=" + tmp}
	d := launchEnv(t, local, 42, 160, env)
	P := ptyscreen.Enter
	d.see("Docker Desk", local.Host(), "Daemons", "◆1", "2 Containers (13)", "shop-web-1")
	if p := sshProcesses(sshd); p != "" {
		t.Fatalf("an ssh is running before any ssh daemon was added: %s", p)
	}

	// --- add: the picker lists the contexts; far connects, unasked ----------
	d.keys("M")
	d.see("Daemons", "In this session", "Contexts on "+cfg, "far", url, "[ssh]")
	// The entries: the launch daemon, then the contexts "default" and "far".
	d.keys(ptyscreen.Down, ptyscreen.Down)
	d.consent(P)
	d.see("Added far; connecting in the background")
	d.see("far  ● connected")
	d.keys(ptyscreen.Esc)
	d.see("2 far ●", "far: connected (Engine")
	if n := len(strings.Fields(sshProcesses(sshd))); n != 1 {
		t.Fatalf("one ssh for the added daemon, found %d", n)
	}
	// Still the launch daemon's objects, and nothing of far's.
	d.see("shop-web-1")
	if s := d.term.Screen(); strings.Contains(s, "far-web") {
		t.Fatalf("far's container on the focused daemon's screen:\n%s", s)
	}

	// --- focus: far's identity and lists, none of local's -------------------
	d.keys(">")
	d.see(url, "◆2 far", "far-web", "far-db", "2 Containers (2)")
	d.gone("shop-web-1")
	d.shot("51-second-daemon-focused")

	// --- a pull on far, and its removal refused while it runs ----------------
	d.keys("1", ptyscreen.Down, "2", "P")
	d.see("Pull image", "Image reference:")
	d.keys("h", "e", "l", "l", "o", "-", "w", "o", "r", "l", "d")
	d.see("Will pull exactly: hello-world:latest")
	d.keys(P)
	d.see("pull image hello-world:latest", "1 running")
	d.keys("M")
	d.see("1 job is running on it — pull image hello-world:latest")
	d.keys("d") // the picker opens on the focused daemon: far
	d.see("Cannot remove far")
	d.see("Cannot remove far: 1 job is running on it — pull image hello-world:latest")
	d.shot("52-remove-refused-while-pulling")
	d.keys(ptyscreen.Esc)

	// --- back to local; far's exit is a notice, not an event ---------------
	d.keys("<")
	d.see("◆1", "2 Images", "pull image hello-world:latest @far")
	far.Crash("far-db", 1)
	d.see("far: container far-db exited (code 1)")
	time.Sleep(200 * time.Millisecond)
	if s := d.term.Screen(); strings.Contains(s, "die exit 1 far-db") || strings.Count(s, "far-db") != 1 {
		t.Fatalf("far's exit should be in the status line only, never an Events line:\n%s", s)
	}
	// The pull finishes on the unfocused daemon: its outcome, named.
	if err := d.term.WaitFor(30*time.Second, "✓ pull image hello-world:latest on far"); err != nil {
		t.Fatal(err)
	}
	if !far.HasImage("hello-world:latest") || local.HasImage("hello-world:latest") {
		t.Fatal("the pull went to the wrong daemon")
	}

	// --- remove far: its streams, connection and ssh process end ------------
	d.keys("M")
	d.keys(ptyscreen.Down)
	d.keys("d")
	d.see("Remove far from the session?", "ssh process started for it ends")
	d.consent("y")
	d.see("Removed far from the session; its streams, connection and ssh process are closed.")
	for deadline := time.Now().Add(5 * time.Second); sshProcesses(sshd) != ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("ssh is still running after its daemon was removed: %s", sshProcesses(sshd))
		}
	}
	for deadline := time.Now().Add(5 * time.Second); far.EventSubscribers() != 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("far still has an event stream open")
		}
	}
	d.keys(ptyscreen.Esc)
	d.gone("2 far")

	// The launch daemon was never touched by anything done on far.
	for _, r := range local.Mutations() {
		t.Fatalf("a mutation reached the launch daemon: %s", r)
	}

	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "docker-desk-ssh-*")); len(left) != 0 {
		t.Fatalf("a tunnel's directory was left behind: %v", left)
	}
	if p := sshProcesses(sshd); p != "" {
		t.Fatalf("ssh still running after quit: %s", p)
	}
}

// TestRemovingTheLaunchDaemonOverSSH: the launch daemon is a member like any
// other. Removed, its ssh process ends at once — not at quit — and the desk
// stays open on the empty state, from which another daemon can be added.
func TestRemovingTheLaunchDaemonOverSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	far := newFake(nil)
	cleanup, err := far.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	local := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "local-web", Image: "nginx:1.27"})
	})
	cfg := t.TempDir()
	writeContext(t, cfg, "here", local.Host())
	tmp, err := os.MkdirTemp("", "dde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	url := sshd.URL(far.Socket())
	env := []string{"DOCKER_HOST=" + url, "DOCKER_CONFIG=" + cfg, "DOCKER_DESK_SSH=" + sshd.CommandString(), "TMPDIR=" + tmp}
	d := launchEnv(t, far, 40, 150, env)
	P := ptyscreen.Enter
	d.see(url, "[ssh]", "● connected", "2 Containers (13)")
	if n := len(strings.Fields(sshProcesses(sshd))); n != 1 {
		t.Fatalf("one ssh, found %d", n)
	}

	d.keys("M")
	d.see("In this session", url)
	d.keys("d")
	d.see("Remove 127.0.0.1 from the session?")
	d.consent("y")
	d.see("No daemon is connected now.")
	for deadline := time.Now().Add(5 * time.Second); sshProcesses(sshd) != ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the launch daemon's ssh is still running after its removal: %s", sshProcesses(sshd))
		}
	}
	d.keys(ptyscreen.Esc)
	d.see("No daemon is connected.", "no daemon connected")
	d.keys("t")
	d.see("No daemon is connected: nothing to act on")
	d.shot("53-no-daemon")

	// The picker is one key away; the context "here" is the second entry
	// after "default".
	d.keys("M")
	d.see("Contexts on", "here")
	d.keys(ptyscreen.Down)
	d.consent(P)
	d.see("Added here")
	d.keys(ptyscreen.Esc)
	d.see("local-web", "Focused here")
	d.keys("q")
	if code := d.quit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}
