package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func changesSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.SeedChanges()
}

// openChanges highlights name, switches the Details pane to Changes and
// focuses it (Enter and c are Details keys).
func (b *bench) openChanges(name string) {
	b.t.Helper()
	b.kind(KindContainers)
	b.m.focus = PaneList
	b.selectRow(name)
	b.until("container inspect", func() bool {
		d := b.m.detail.container
		return d != nil && d.ID == b.srv.ContainerID(name)
	})
	for b.m.tabName() != tabChanges {
		b.keys("]")
	}
	b.m.focus = PaneDetails
	b.settle()
}

// imageReads counts what the tab may send: image inspects and histories.
func (b *bench) imageReads() int {
	return b.srv.CountRequests("GET", `^/images/[^/]+(/[^/]+)*/(json|history)$`)
}

// noEnvValue fails if any environment value of the demo's two builds is
// anywhere the operator or a log could read it.
func (b *bench) noEnvValue(when string) {
	b.t.Helper()
	var hay []string
	hay = append(hay, b.screen(), b.m.status.text)
	for _, j := range b.m.jobs.List() {
		full, _ := b.m.jobs.Get(j.ID)
		hay = append(hay, full.Title, full.Target, full.Status, full.Err, strings.Join(full.Log, "\n"))
	}
	hay = append(hay, fmt.Sprintf("%+v", b.m.chg.facts))
	all := strings.Join(hay, "\n")
	for _, v := range fixture.ChangesEnvValues {
		if strings.Contains(all, v) {
			b.t.Fatalf("%s: an environment value (%s…) is visible", when, v[:6])
		}
	}
}

func TestChangesTabIsSecondOnContainersOnly(t *testing.T) {
	if kindTabs[KindContainers][1] != tabChanges {
		t.Fatalf("container tabs: %v", kindTabs[KindContainers])
	}
	for k := Kind(0); k < kindCount; k++ {
		for _, tab := range kindTabs[k] {
			if tab == tabChanges && k != KindContainers {
				t.Errorf("kind %d has a Changes tab", k)
			}
		}
	}
}

func TestChangesMovedTag(t *testing.T) {
	for _, sz := range [][2]int{{120, 40}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			b := newBench(t, benchOpts{width: sz[0], height: sz[1]})
			b.kind(KindContainers)
			b.m.focus = PaneList
			b.selectRow("report-job")
			b.until("container inspect", func() bool { return b.m.detail.container != nil && b.m.detail.container.Name == "report-job" })
			b.settle()
			b.srv.ResetRequests()

			b.keys("]")
			b.see("[Changes]")
			b.until("both images read", func() bool { return len(b.m.chg.facts) == 2 })
			b.settle()
			b.assertFrameGeometry()
			if n := len(b.srv.Requests()); n > 4 {
				t.Fatalf("opening the tab sent %d requests, want at most 4: %v", n, b.srv.Requests())
			}
			if n := b.imageReads(); n != 4 {
				t.Errorf("%d image reads, want 4 (inspect and history of each)", n)
			}
			if n := b.exports(); n != 0 {
				t.Fatalf("%d archives read before Enter", n)
			}

			// The whole tab, scrolled through, holds the lineage and config.
			b.m.focus = PaneDetails
			all := b.changesText()
			for _, want := range []string{
				"running", "tag now", "acme/report:1.4", "Δ +6.9MB",
				"base intact: all 3 layers shared, 2 added",
				"+ L4", "apt-get install -y curl fonts-dejavu-core", "+ L5", "COPY deploy/config.yml /app/config.yml",
				"command", `["python3", "-m", "report"] → ["python3", "-m", "report", "--serve"]`,
				"user", "+ report", "port", "+ 8000/tcp", "healthcheck", "label", "org.opencontainers.image.version 1.4.0 → 1.4.2",
				"env", "− DEBUG", "+ FONT_DIR",
				"values are never read",
				"Restarting report-job keeps the image it runs", "which this desk does not do",
				"Enter compares packages and findings",
			} {
				if !strings.Contains(all, want) {
					t.Errorf("missing %q in:\n%s", want, all)
				}
			}
			b.noEnvValue("cheap half")

			// Enter reads both archives, as a job.
			b.keys("enter")
			b.until("comparison done", b.compared)
			b.settle()
			b.assertFrameGeometry()
			if n := b.exports(); n != 2 {
				t.Errorf("%d exports, want 2", n)
			}
			all = b.changesText()
			for _, want := range []string{
				"3 added, 1 removed, 2 version changed (dpkg)",
				"+ curl", "+ fonts-dejavu-core:all", "+ libcurl4", "− netbase:all",
				"~ openssl 3.0.11-1~deb12u2 → 3.0.15-1~deb12u1",
				"appeared", "secret.", "/app/config.yml",
				"− config.root-user", "− config.healthcheck",
			} {
				if !strings.Contains(all, want) {
					t.Errorf("missing %q in:\n%s", want, all)
				}
			}
			if strings.Contains(all, fixture.ChangesToken) {
				t.Errorf("the token in the added layer is shown unmasked")
			}
			b.noEnvValue("after the comparison")
			var j jobs.Job
			for _, x := range b.m.jobs.List() {
				if x.Title == "compare images" {
					j = x
				}
			}
			if j.State != jobs.Succeeded || j.Target != "acme/report:1.4" {
				t.Errorf("job: %+v", j)
			}

			// Away and back: drawn at once, nothing read again.
			b.keys("[")
			b.settle()
			b.srv.ResetRequests()
			b.step(keyMsg("]"))
			if !strings.Contains(b.screen(), "base intact") {
				t.Fatalf("second open is not instant:\n%s", b.screen())
			}
			b.settle()
			if n := len(b.srv.Requests()); n != 0 {
				t.Errorf("second open sent %d requests: %v", n, b.srv.Requests())
			}
			// The reports are Findings' own: that tab has them too.
			if len(b.m.fx.cache) != 2 {
				t.Errorf("findings cache holds %d reports, want 2", len(b.m.fx.cache))
			}
		})
	}
}

// compared reports whether the tab shows a finished comparison.
func (b *bench) compared() bool {
	return b.m.chg.run == nil && strings.Contains(b.changesText(), "version changed")
}

// changesText is the whole tab, every line, as plain text.
func (b *bench) changesText() string {
	b.t.Helper()
	b.screen() // draw once, so the view holds this width's lines
	// Words wrap: compare as single-spaced text.
	return strings.Join(strings.Fields(ansi.Strip(strings.Join(b.m.chg.view.lines, "\n"))), " ")
}

func TestChangesEveryTheme(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 24})
	b.openChanges("report-job")
	b.until("both images read", func() bool { return len(b.m.chg.facts) == 2 })
	for _, th := range themes {
		for _, sz := range [][2]int{{80, 24}, {120, 40}} {
			b.m.setTheme(th)
			b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			b.assertFrameGeometry()
			b.see("[Changes]", "base intact")
		}
	}
}

func TestChangesCancelAndRetry(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.ExportChunkDelay = 400 * time.Millisecond
	}})
	b.openChanges("report-job")
	b.until("both images read", func() bool { return len(b.m.chg.facts) == 2 })
	b.keys("enter")
	b.until("comparing", func() bool {
		return strings.Contains(b.changesText(), "comparing: reading the running image's archive") && strings.Contains(b.changesText(), "c cancels.")
	})
	b.keys("c")
	b.until("cancelled", func() bool { return b.m.chg.run == nil })
	b.settle()
	if all := b.changesText(); !strings.Contains(all, "the comparison ended without a result: Cancelled. Enter compares again.") {
		t.Fatalf("after cancelling:\n%s", all)
	}
	b.srv.ExportChunkDelay = 0
	b.keys("enter")
	b.until("comparison done", b.compared)
}

func TestChangesRefusals(t *testing.T) {
	b := newBench(t, benchOpts{seed: changesSeed, width: 120, height: 40})
	cases := []struct{ name, want string }{
		{"pinned-svc", "Pinned to a digest"},
		{"by-id-svc", "Created from an image ID"},
		{"untagged-svc", "acme/gone:2 no longer resolves here"},
		{"shop-web-1", "same image on both sides"},
		{"edge-svc", "Different platforms: the container runs linux/amd64, the tag names linux/arm64/v8"},
	}
	for _, c := range cases {
		b.srv.ResetRequests()
		b.openChanges(c.name)
		b.until(c.name+" refusal", func() bool { return strings.Contains(b.changesText(), c.want) })
		all := b.changesText()
		if !strings.Contains(all, "Nothing to compare.") || !strings.Contains(all, "Restarting "+c.name+" keeps the image") {
			t.Errorf("%s:\n%s", c.name, all)
		}
		// Nothing to compare: Enter reads nothing.
		b.keys("enter")
		b.settle()
		if b.m.chg.run != nil || b.exports() != 0 {
			t.Errorf("%s: Enter started a comparison", c.name)
		}
		if n := b.imageReads(); n > 4 {
			t.Errorf("%s: %d image reads", c.name, n)
		}
		b.keys("esc")
	}

	// An unreadable layer is known only once the archives are read.
	b.openChanges("zstd-svc")
	b.until("facts", func() bool { return strings.Contains(b.changesText(), "base intact") })
	b.keys("enter")
	b.until("zstd refusal", func() bool {
		return strings.Contains(b.changesText(), "nothing to compare. Layer 5 of the new image could not be read")
	})

	// A rebuild on another base: where the stacks part.
	b.keys("esc")
	b.openChanges("rebased-svc")
	b.until("rebased", func() bool {
		return strings.Contains(b.changesText(), "diverge after layer 1: 1 shared, 2 replaced by 2")
	})
	all := b.changesText()
	for _, want := range []string{"− L2", "− L3", "+ L2", "+ L3", "apt-get install -y --no-install-recommends python3 &&"} {
		if !strings.Contains(all, want) {
			t.Errorf("rebased: missing %q in:\n%s", want, all)
		}
	}
}

func TestChangesReadOnly(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true, width: 80, height: 24})
	b.openChanges("report-job")
	b.until("both images read", func() bool { return len(b.m.chg.facts) == 2 })
	b.keys("enter")
	b.until("comparison done", b.compared)
	b.keys("c", "j", "k", "enter")
	b.settle()
	b.see("[READ-ONLY]")
	b.noMutations("comparing in read-only mode")
	for _, r := range b.srv.Requests() {
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("read-only desk sent %s", r)
		}
	}
	b.noEnvValue("read-only")
}
