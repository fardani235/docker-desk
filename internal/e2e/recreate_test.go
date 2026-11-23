package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// recreateSeed is the demo and a privileged container whose tag moved: a
// re-create refuses it.
func recreateSeed(s *fakeengine.Server) {
	s.SeedDemo()
	day := 24 * time.Hour
	s.SeedMovedTag(fakeengine.ContainerSpec{Name: "priv-svc", Image: "acme/priv:1", Cmd: []string{"svc"}, Privileged: true, State: "exited", Age: day},
		fixture.ChangesReport("acme/priv:1", false), fixture.ChangesReport("acme/priv:1", true), nil, nil, 2*day)
}

// fits fails when a line of a captured dialog is wider than the terminal.
func fits(t *testing.T, screen string, cols int) {
	t.Helper()
	for i, l := range strings.Split(screen, "\n") {
		if n := utf8.RuneCountInString(l); n > cols {
			t.Fatalf("line %d is %d cells, terminal is %d:\n%s", i, n, cols, screen)
		}
	}
}

// openChangesOf filters the containers to name and opens its Changes tab.
func (d *desk) openChangesOf(filter, name string) {
	d.t.Helper()
	d.keys("2", "/")
	d.keys(filter, ptyscreen.Enter)
	d.see("3 Details: " + name)
	d.keys("3", "]")
	d.see("[Changes]", "base intact") // both images read: the tab has decided
}

// TestRecreate opens report-job's Changes tab on its moved tag, presses A,
// types recreate, and checks the list, the job and the derived name — at
// 100 columns and at 80×24, with the confirmation captured at both. Only the
// three requests of the plan change anything.
func TestRecreate(t *testing.T) {
	for _, tc := range []struct {
		shot       string
		rows, cols int
	}{
		{"58-recreate-confirm", 40, 100},
		{"59-recreate-confirm-80x24", 24, 80},
	} {
		t.Run(tc.shot, func(t *testing.T) {
			srv := startFake(t, recreateSeed)
			oldID := srv.ContainerID("report-job")
			prev := "report-job-previous-" + engine.ShortID(oldID)
			d := launch(t, srv, tc.rows, tc.cols)
			d.see("2 Containers (14)")
			d.openChangesOf("report-job", "report-job")
			if tc.cols >= 100 {
				d.see("A re-create")
			}
			if n := len(srv.Mutations()); n != 0 {
				t.Fatalf("opening the tab sent %v", srv.Mutations())
			}
			d.keys("A")
			d.see("Re-create container", "Type recreate and press Enter")
			fits(t, d.shot(tc.shot), tc.cols)
			if n := len(srv.Mutations()); n != 0 {
				t.Fatalf("the confirmation sent %v", srv.Mutations())
			}
			d.consent("recreate", ptyscreen.Enter)
			d.see("re-create container report-job")
			waitFor(t, "the new container", func() bool {
				st, _ := srv.ContainerState("report-job")
				return st == "running" && srv.ContainerID("report-job") != oldID
			})
			if srv.ContainerID(prev) != oldID {
				t.Fatalf("the old container is not %s: %v", prev, srv.ContainerNames())
			}
			if st, _ := srv.ContainerState(prev); st != "exited" {
				t.Fatalf("the old container is %s", st)
			}
			// report-job had exited: no stop.
			if log := mutationPaths(srv); !matchInOrder(log,
				`^POST /containers/[0-9a-f]{64}/rename$`, `^POST /containers/create$`, `^POST /containers/[0-9a-f]{64}/start$`) {
				t.Fatalf("requests: %v", log)
			}
			d.keys(ptyscreen.Esc, "2", "/", ctrlU)
			d.keys("previous", ptyscreen.Enter)
			d.see(prev[:20])
			d.keys("q")
			d.quit()
		})
	}
}

// TestRecreateRefused presses A on a privileged container: the dialog names
// the setting, offers no way to proceed, and nothing is sent.
func TestRecreateRefused(t *testing.T) {
	for _, tc := range []struct {
		shot       string
		rows, cols int
	}{
		{"60-recreate-refused", 40, 100},
		{"61-recreate-refused-80x24", 24, 80},
	} {
		t.Run(tc.shot, func(t *testing.T) {
			srv := startFake(t, recreateSeed)
			d := launch(t, srv, tc.rows, tc.cols)
			d.see("2 Containers (14)")
			d.openChangesOf("priv-svc", "priv-svc")
			d.keys("A")
			d.see("Re-create container", "REFUSED", "Privilege: runs")
			fits(t, d.shot(tc.shot), tc.cols)
			d.consent("recreate", ptyscreen.Enter)
			time.Sleep(200 * time.Millisecond)
			if n := len(srv.Mutations()); n != 0 {
				t.Fatalf("a refused re-create sent %v", srv.Mutations())
			}
			if !strings.Contains(d.term.Screen(), "priv-svc") {
				t.Fatal("the desk lost the container")
			}
			d.keys(ptyscreen.Esc, "q")
			d.quit()
		})
	}
}

// A moved tag on a container with a named volume: the mode the daemon
// reports for it is its default, so A is offered and the re-create carries
// the volume (docs/DECISIONS.md, round twenty).
func TestRecreateCarriesADefaultVolume(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) {
		s.SeedDemo()
		day := 24 * time.Hour
		s.SeedMovedTag(fakeengine.ContainerSpec{
			Name: "db-svc", Image: "acme/db:1", Cmd: []string{"db"},
			Volumes: []string{"db-data"}, State: "running", Age: day,
		}, fixture.ChangesReport("acme/db:1", false), fixture.ChangesReport("acme/db:1", true), nil, nil, 2*day)
	})
	oldID := srv.ContainerID("db-svc")
	d := launch(t, srv, 40, 100)
	d.openChangesOf("db-svc", "db-svc")
	d.see("A re-create")
	d.keys("A")
	d.see("Re-create container", "Type recreate and press Enter")
	d.consent("recreate", ptyscreen.Enter)
	d.see("re-create container db-svc")
	waitFor(t, "the new container", func() bool {
		st, _ := srv.ContainerState("db-svc")
		return st == "running" && srv.ContainerID("db-svc") != oldID
	})
	eng, err := engine.New(engine.Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	detail, err := eng.InspectContainer(context.Background(), srv.ContainerID("db-svc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range detail.Mounts {
		if m.Type == "volume" && m.Destination == "/data/db-data" {
			return
		}
	}
	t.Errorf("the new container does not mount the volume: %+v", detail.Mounts)
}
