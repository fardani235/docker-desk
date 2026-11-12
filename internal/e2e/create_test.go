package e2e

import (
	"regexp"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

const ctrlN = "\x0e"

// mutationPaths is the fake's request log, mutations only, as "METHOD path".
func mutationPaths(srv *fakeengine.Server) []string {
	var out []string
	for _, r := range srv.Mutations() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// matchInOrder reports whether the log has one request matching each pattern,
// in that order, and nothing else.
func matchInOrder(log []string, patterns ...string) bool {
	if len(log) != len(patterns) {
		return false
	}
	for i, p := range patterns {
		if !regexp.MustCompile(p).MatchString(log[i]) {
			return false
		}
	}
	return true
}

// openCreate opens the create form on the Containers kind and types the
// image, then the name.
func (d *desk) openCreate(image, name string) {
	d.t.Helper()
	d.see("2 Containers (13)")
	d.keys("n")
	d.see("Create container", "Will create and start, on this daemon:")
	d.keys(image)
	if name != "" {
		d.keys(ptyscreen.Tab, name)
	}
}

// TestCreate drives the create form in the real binary: an image the sandbox
// does not have is pulled first, then the container is created and started,
// and appears in the list as running.
func TestCreate(t *testing.T) {
	for _, tc := range []struct {
		shot       string
		rows, cols int
	}{
		{"54-create-form", 42, 150},
		{"55-create-form-80x24", 24, 80},
	} {
		t.Run(tc.shot, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d := launch(t, srv, tc.rows, tc.cols)
			d.openCreate("hello-world", "made-by-e2e")
			d.keys(ptyscreen.Tab, "127.0.0.1:18080:80", ctrlN, "53:53/udp")
			d.keys(ptyscreen.Tab, "LOG_LEVEL=info")
			d.keys(ptyscreen.Tab, "cache:/var/cache")
			if tc.rows >= 40 {
				d.see("hello-world:latest", "it will be pulled first", "made-by-e2e",
					"127.0.0.1:18080 → 80/tcp", "53 → 53/udp", "LOG_LEVEL=info", "cache → /var/cache (volume)")
			} else {
				d.see("field 5 of 7", "Esc cancel")
			}
			d.shot(tc.shot)
			if n := len(srv.Mutations()); n != 0 {
				t.Fatalf("the form sent %v", srv.Mutations())
			}
			d.consent(ptyscreen.Enter)
			d.see("✓ create container made-by-e2e")
			if st, _ := srv.ContainerState("made-by-e2e"); st != "running" {
				t.Fatalf("state %q", st)
			}
			if !srv.HasImage("hello-world:latest") {
				t.Fatal("the image was not pulled")
			}
			log := mutationPaths(srv)
			if !matchInOrder(log, `^POST /images/create$`, `^POST /containers/create$`, `^POST /containers/[0-9a-f]{64}/start$`) {
				t.Fatalf("want pull, create, start of the returned ID, and nothing else; got %v", log)
			}
			// The list shows it, running, from the daemon's events.
			d.keys(ptyscreen.Esc, "2", "/", "made-by")
			d.see("made-by-e2e", "running")
		})
	}
}

// An image the daemon has is not pulled; an image that cannot be pulled
// creates and starts nothing. The fake's request log is the witness.
func TestCreatePullOrNot(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
		d := launch(t, srv, 40, 140)
		d.openCreate("nginx:1.27", "from-present")
		d.see("present on this daemon")
		d.consent(ptyscreen.Enter)
		d.see("✓ create container from-present")
		log := mutationPaths(srv)
		if !matchInOrder(log, `^POST /containers/create$`, `^POST /containers/[0-9a-f]{64}/start$`) {
			t.Fatalf("no pull for a present image; got %v", log)
		}
	})
	t.Run("failing pull", func(t *testing.T) {
		srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
		d := launch(t, srv, 40, 140)
		d.openCreate("nosuch/image:1", "never-made")
		d.see("it will be pulled first")
		d.consent(ptyscreen.Enter)
		d.see("failed")
		d.keys("5", ptyscreen.Enter)
		d.see("↷ skipped create container never-made", "↷ skipped start container never-made")
		log := mutationPaths(srv)
		if !matchInOrder(log, `^POST /images/create$`) {
			t.Fatalf("only the pull may be sent; got %v", log)
		}
		for _, n := range srv.ContainerNames() {
			if strings.Contains(n, "never-made") {
				t.Fatal("a container was created")
			}
		}
	})
}
