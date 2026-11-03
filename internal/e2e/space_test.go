package e2e

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// dockerSystemDF runs the real docker CLI against the fake — explicitly, by
// -H, with nothing inherited from the environment — and returns one line per
// type: type, size, reclaimable. It is a GET; it changes nothing.
func dockerSystemDF(t *testing.T, srv *fakeengine.Server) []string {
	t.Helper()
	path, err := exec.LookPath("docker")
	if err != nil {
		t.Log("no docker CLI on PATH: the totals are not compared with docker system df")
		return nil
	}
	cmd := exec.Command(path, "-H", srv.Host(), "system", "df", "--format", "{{.Type}}\t{{.Size}}\t{{.Reclaimable}}")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DOCKER_HOST=") && !strings.HasPrefix(kv, "DOCKER_CONTEXT=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker system df: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// TestSpace opens the Space view in the real binary: the totals are
// docker system df's, character for character; the view scrolls; Esc puts
// the desk back; the removal it leads to states what it frees; read-only
// mode shows everything and offers no reclaim. Nothing mutating is sent.
func TestSpace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"26-space", 40, 120, nil},
		{"28-space-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (12)")
			d.keys("1", ptyscreen.Down, "2") // the Images list
			d.see("2 Images (14")
			d.keys("S")
			d.see("Space — where the bytes went", "RECLAIMABLE", "alone")

			// The totals, against the CLI.
			screen := d.term.Screen()
			for _, line := range dockerSystemDF(t, srv) {
				f := strings.Split(line, "\t")
				if len(f) != 3 {
					t.Fatalf("docker system df: %q", line)
				}
				re := regexp.MustCompile(regexp.QuoteMeta(f[0]) + `\s+\d+\s+\d+\s+` + regexp.QuoteMeta(f[1]) + `\s+` + regexp.QuoteMeta(f[2]) + `\s`)
				if !re.MatchString(screen) {
					t.Errorf("docker system df says %q; the view does not:\n%s", line, screen)
				}
			}

			// Down to shop/worker:dev: alone, together, and its sharers.
			err := d.term.WaitUntil(wait, "the cursor on shop/worker:dev", func(s string) bool {
				if strings.Contains(s, "▸ shop/worker:dev") {
					return true
				}
				_ = d.term.Send("j")
				time.Sleep(30 * time.Millisecond)
				return false
			})
			if err != nil {
				t.Fatal(err)
			}
			d.see("frees 88.7MB", "together")
			d.shot(tc.name)
			if tc.args == nil {
				// The removal the view leads to: the desk's own dialog,
				// with what it frees — unique bytes, not the size.
				// Up to <none> 8bae7d2a8907, the earlier build of the worker.
				err := d.term.WaitUntil(wait, "the cursor on the earlier build", func(s string) bool {
					if strings.Contains(s, "▸ <none> 8bae7d2a8907") {
						return true
					}
					_ = d.term.Send("k")
					time.Sleep(30 * time.Millisecond)
					return false
				})
				if err != nil {
					t.Fatal(err)
				}
				d.keys("d")
				d.see("Remove image", "frees", "279MB — its unique size")
				d.shot("27-space-remove-frees")
				d.keys(ptyscreen.Esc)
				d.gone("Remove image")
			} else {
				hints := d.term.Screen()
				lines := strings.Split(hints, "\n")
				if last := lines[len(lines)-1]; strings.Contains(last, "remove") || strings.Contains(last, "prune") {
					t.Errorf("read-only hints offer reclaim: %q", last)
				}
				d.keys("d", "X")
				time.Sleep(300 * time.Millisecond)
				if s := d.term.Screen(); strings.Contains(s, "Remove image") || strings.Contains(s, "Prune images") {
					t.Fatalf("read-only mode opened a reclaim dialog:\n%s", s)
				}
			}
			d.keys(ptyscreen.Esc)
			d.gone("Space — where the bytes went")
			d.see("◆ 2 Images (14") // back where it was
			for _, r := range srv.Requests() {
				if r.Mutating() || strings.Contains(r.Query, "size=1") || strings.Contains(r.Path, "prune") {
					t.Fatalf("the Space view sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}
