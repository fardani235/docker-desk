package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// TestComposeDeclared opens shop › Compose › Declared in the real binary
// against the demo's compose files: the header reads nothing; Enter compares
// and shows the template image not compared and the service with no
// container; blog, whose file is absent, cannot be compared. Read-only mode
// allows it, since it only reads. Nothing mutating is sent.
func TestComposeDeclared(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"44-compose-declared", 40, 140, nil},
		{"45-compose-declared-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			srv := startFake(t, func(s *fakeengine.Server) {
				s.ComposeDir = dir
				s.SeedDemo()
			})
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, ptyscreen.Down, ptyscreen.Down, ptyscreen.Down, "2")
			d.see("2 Compose (2)", "blog", "shop")
			d.keys(ptyscreen.Down) // shop
			d.see("3 Details: shop")
			d.keys("]")
			d.see("[Declared]", "project", "shop · 4 containers, 3 running")
			d.keys("3")
			d.scrollTo("nothing has been read")
			d.keys(ptyscreen.Enter)
			d.see("✓ compare with the compos")
			// The rows are below the header: scroll to them.
			d.scrollTo("1 declared only, 1 differs")
			d.scrollTo("declared only   cache")
			if tc.rows >= 40 {
				d.scrollTo("the image is a template")
				d.see("present only", "not compared", "match")
			}
			d.shot(tc.name)
			if tc.rows >= 40 {
				// blog's file is deliberately absent.
				d.keys("2", ptyscreen.Up, "3", ptyscreen.Enter)
				d.see("3 Details: blog", "Cannot be compared: there is no file at")
				d.shot("46-compose-declared-absent")
			} else {
				d.scrollTo("template")
			}
			if n := len(srv.Mutations()); n != 0 {
				t.Fatalf("the comparison sent %d mutating request(s): %v", n, srv.Mutations())
			}
		})
	}
}

// scrollTo pages the focused pane down until sub is on screen.
func (d *desk) scrollTo(sub string) {
	d.t.Helper()
	for i := 0; i < 12; i++ {
		if strings.Contains(d.term.Screen(), sub) {
			return
		}
		d.keys(ptyscreen.PgDn)
		time.Sleep(120 * time.Millisecond)
	}
	d.see(sub)
}
