package ui

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fardani235/docker-desk/internal/composefile"
	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// countingReader is the real bounded reader, counted.
type countingReader struct{ n atomic.Int32 }

func (c *countingReader) read(p string) ([]byte, fs.FileInfo, error) {
	c.n.Add(1)
	return composefile.ReadInfo(p)
}

// composeSeed seeds the demo with its compose files written into dir.
func composeSeed(dir string) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		s.ComposeDir = dir
		s.SeedDemo()
	}
}

// declaredBench is the demo with real compose files and a counted reader.
func declaredBench(t *testing.T, o benchOpts) (*bench, *countingReader, string) {
	t.Helper()
	dir := t.TempDir()
	r := &countingReader{}
	if o.seed == nil {
		o.seed = composeSeed(dir)
	}
	if o.composeReader == nil {
		o.composeReader = r.read
	}
	return newBench(t, o), r, dir
}

// openDeclaredTab highlights a project, shows its Declared tab and focuses
// the Details pane (Enter and c are Details keys there).
func (b *bench) openDeclaredTab(project string) {
	b.t.Helper()
	b.kind(KindCompose)
	b.m.focus = PaneList
	b.selectRow(project)
	for b.m.tabName() != tabDeclared {
		b.keys("]")
	}
	b.m.focus = PaneDetails
	b.settle()
}

// declaredText is the whole tab, however far it scrolls.
func (b *bench) declaredText() string {
	b.t.Helper()
	b.screen()
	t, ok := b.m.declaredTarget()
	if !ok {
		b.t.Fatal("no project highlighted")
	}
	return strings.Join(b.m.declaredLines(t, 200), "\n")
}

// flat is text with every run of white space one space: wrapped prose reads
// as one line.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func (b *bench) declaredJobs() []jobs.Job {
	var out []jobs.Job
	for _, j := range b.m.jobs.List() {
		if j.Title == "compare with the compose file" {
			full, _ := b.m.jobs.Get(j.ID)
			out = append(out, full)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *bench) declaredDone() bool { return b.m.dc.run == nil && len(b.m.dc.results) > 0 }

func TestDeclaredTabIsSecondOnComposeOnly(t *testing.T) {
	if got := kindTabs[KindCompose]; len(got) != 2 || got[0] != tabInfo || got[1] != tabDeclared {
		t.Fatalf("compose tabs: %v", got)
	}
	for k := Kind(0); k < kindCount; k++ {
		for _, tab := range kindTabs[k] {
			if tab == tabDeclared && k != KindCompose {
				t.Errorf("kind %d has a Declared tab", k)
			}
		}
	}
}

// Before Enter the tab shows only what the labels say. Highlighting,
// refreshing and opening the tab open no file.
func TestDeclaredHeaderReadsNothing(t *testing.T) {
	b, reads, dir := declaredBench(t, benchOpts{})
	b.kind(KindCompose)
	b.m.focus = PaneList
	b.selectRow("blog")
	b.selectRow("shop")
	b.see("No file has been read yet")
	b.m.pending = ^uint32(0) // a refresh of everything
	b.settle()
	b.openDeclaredTab("shop")
	b.assertFrameGeometry()
	b.see("[Declared]")
	all := b.declaredText()
	for _, want := range []string{
		"project", "shop · 4 containers, 3 running",
		"working dir", filepath.Join(dir, "shop"),
		"config file", filepath.Join(dir, "shop", "compose.yaml"),
		"local (unix socket)",
		"newest", "written", "unknown: nothing has been read",
		"nothing has been read. Enter reads the file once",
		"compared as it is now",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, absent := range []string{"declared only", "nginx:1.26", "redis:7-alpine"} {
		if strings.Contains(all, absent) {
			t.Errorf("%q shown before anything was read", absent)
		}
	}
	b.keys("]", "[") // leave and come back
	b.settle()
	if n := reads.n.Load(); n != 0 {
		t.Fatalf("%d reads before Enter", n)
	}
	if len(b.declaredJobs()) != 0 {
		t.Fatal("a job started before Enter")
	}
	b.noMutations("opening the Declared tab")
}

// The demo's shop file against the demo's shop containers: one of each row.
func TestDeclaredComparisonShowsEachVerdict(t *testing.T) {
	b, reads, _ := declaredBench(t, benchOpts{readOnly: true}) // it only reads
	b.openDeclaredTab("shop")
	b.keys("enter")
	b.until("comparison", b.declaredDone)
	b.settle()
	b.assertFrameGeometry()
	if n := reads.n.Load(); n != 1 {
		t.Fatalf("%d reads, want 1", n)
	}
	all := b.declaredText()
	for _, want := range []string{
		"services", "6: 1 declared only, 1 differs, 1 present only, 2 not compared, 1 match",
		"declared only", "cache", "declares redis:7-alpine", "no container of the project carries this service",
		"differs (tag)", "web", "declares nginx:1.26", "shop-web-1 runs nginx:1.27",
		"present only", "worker", "shop-worker-1 runs shop/worker:dev", "the file does not declare it",
		"not compared", "api", "the image is a template (shop/api:${API_TAG:-dev}); the desk does not interpolate",
		"debug", "declared with profiles (debug); the desk cannot know if they were enabled",
		"match", "db", "declares postgres:16", "shop-db-1 runs postgres:16",
		"config hash",
		"read", "compared as it is now",
		"after these were created",
		"The file was written", "That alone does not make it wrong.",
		"Not compared: scale, ports, volumes, environment, command, health checks.",
	} {
		if !strings.Contains(flat(all), want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	// Disagreements first.
	rows := all[strings.Index(all, "1 match")+len("1 match"):]
	order := []string{"declared only", "differs (tag)", "present only", "not compared", "match "}
	at := -1
	for _, v := range order {
		i := strings.Index(rows, v)
		if i < at {
			t.Errorf("%q is listed before what precedes it:\n%s", v, all)
		}
		at = i
	}
	// No line of the file that is not part of the comparison is shown.
	for _, never := range []string{"x-defaults", "postgres://shop@db/shop", "max-size", "sleep", "pgdata:/var"} {
		if strings.Contains(b.screen()+all, never) {
			t.Errorf("file content %q is on screen", never)
		}
	}
	b.keys("[") // the Info tab now says when it was read
	b.see("The Declared tab read them at")
	b.noMutations("comparing")
}

// One job per Enter, none while one runs; c cancels; the job's lines carry
// no byte of the file.
func TestDeclaredJob(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	var reads atomic.Int32
	b, _, _ := declaredBench(t, benchOpts{composeReader: func(p string) ([]byte, fs.FileInfo, error) {
		reads.Add(1)
		<-gate
		return composefile.ReadInfo(p)
	}})
	t.Cleanup(release)
	b.openDeclaredTab("shop")
	b.keys("enter")
	b.until("job started", func() bool { return len(b.declaredJobs()) == 1 })
	b.see("reading the compose file", "c cancel")
	b.keys("enter", "enter")
	b.settle()
	if n := len(b.declaredJobs()); n != 1 {
		t.Fatalf("%d jobs after three Enters while reading, want 1", n)
	}
	b.see("still being read")

	// c cancels; the read in flight completes, and nothing is compared.
	b.keys("c")
	release()
	b.until("cancelled", b.declaredDone)
	b.settle()
	if j := b.declaredJobs()[0]; j.State != jobs.Cancelled {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	b.see("Cancelled; nothing was compared.")

	// Enter again: a second job, which finishes.
	b.keys("enter")
	b.until("second comparison", func() bool { return len(b.declaredJobs()) == 2 && b.declaredDone() })
	b.settle()
	js := b.declaredJobs()
	if js[1].State != jobs.Succeeded || js[1].Target != "shop" {
		t.Fatalf("second job: %+v", js[1])
	}
	if !strings.Contains(js[1].Status, "1 declared only, 1 differs") {
		t.Errorf("status %q", js[1].Status)
	}
	// Every token below is in the file and is not a path, a count or a
	// word of the desk's own.
	var hay []string
	for _, j := range js {
		hay = append(hay, j.Title, j.Target, j.Status, j.Err, strings.Join(j.Log, "\n"))
	}
	joined := strings.Join(hay, "\n")
	for _, tok := range []string{"nginx", "postgres", "redis", "busybox", "API_TAG", "x-defaults", "unless-stopped", "web", "cache", "debug", "pgdata", "8080"} {
		if strings.Contains(joined, tok) {
			t.Errorf("a job line carries %q from the file:\n%s", tok, joined)
		}
	}
}

// Each refusal is one sentence, and no rows.
func TestDeclaredRefusals(t *testing.T) {
	type tc struct {
		name    string
		opts    func(t *testing.T) benchOpts
		project string
		want    string
		reads   int32 // reads expected (the flag and the transport allow none)
	}
	single := func(path string) func(*fakeengine.Server) {
		return func(s *fakeengine.Server) {
			s.SeedDemo()
			s.AddContainer(fakeengine.ContainerSpec{Name: "solo-app-1", Image: "alpine:3.20", Labels: map[string]string{
				"com.docker.compose.project": "solo", "com.docker.compose.service": "app",
				"com.docker.compose.project.config_files": path,
			}})
		}
	}
	file := func(t *testing.T, content []byte) string {
		p := filepath.Join(t.TempDir(), "compose.yaml")
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []tc{
		{"not found", func(t *testing.T) benchOpts { return benchOpts{} }, "blog",
			"Cannot be compared: there is no file at", 1},
		{"not a regular file", func(t *testing.T) benchOpts { return benchOpts{seed: single(t.TempDir())} }, "solo",
			"is not a regular file.", 1},
		{"over 1 MiB", func(t *testing.T) benchOpts {
			return benchOpts{seed: single(file(t, bytes.Repeat([]byte("#"), composefile.MaxSize+1)))}
		}, "solo", "is larger than 1 MiB", 1},
		{"unparseable", func(t *testing.T) benchOpts {
			return benchOpts{seed: single(file(t, []byte("services:\n  app: [\n    image: secret-value\n")))}
		}, "solo", "is not a compose file the desk can read: it is not valid YAML (line", 1},
		{"remote transport", func(t *testing.T) benchOpts {
			return benchOpts{endpoint: &dockerctx.Endpoint{Host: "ssh://ops@prod-1", Context: "prod", Source: "test"}}
		}, "shop", "Not compared: the daemon is reached over ssh, so the path belongs to the machine that ran compose.", 0},
		{"--no-compose-files", func(t *testing.T) benchOpts { return benchOpts{noComposeFiles: true} }, "shop",
			"Not compared: --no-compose-files forbids reading compose files.", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, reads, _ := declaredBench(t, c.opts(t))
			b.openDeclaredTab(c.project)
			b.keys("enter")
			b.until("outcome", func() bool { return c.reads == 0 || b.declaredDone() })
			b.settle()
			b.assertFrameGeometry()
			all := b.declaredText()
			if !strings.Contains(flat(all), c.want) {
				t.Fatalf("missing %q in:\n%s", c.want, all)
			}
			for _, row := range []string{"declared only", "differs", "present only", "match ", "services ", "secret-value"} {
				if strings.Contains(all, row) {
					t.Errorf("%q shown with a refusal:\n%s", row, all)
				}
			}
			if n := reads.n.Load(); n != c.reads {
				t.Errorf("%d reads, want %d", n, c.reads)
			}
			if c.reads == 0 && len(b.declaredJobs()) != 0 {
				t.Error("a job started for a refusal known in advance")
			}
		})
	}
}

// The menu offers exactly stop, remove and compare; compare shows the tab
// and reads nothing.
func TestDeclaredFromTheActionMenu(t *testing.T) {
	b, reads, _ := declaredBench(t, benchOpts{readOnly: true})
	b.kind(KindCompose)
	b.m.focus = PaneList
	b.selectRow("shop")
	b.keys("enter")
	b.see("Actions: shop", "stop project", "remove project…", "compare with the compose file")
	var labels []string
	for _, a := range b.m.dlg.menu {
		labels = append(labels, a.label)
	}
	if got := strings.Join(labels, "|"); got != "stop project|remove project…|compare with the compose file" {
		t.Fatalf("menu: %s", got)
	}
	b.keys("down", "down", "enter")
	b.settle()
	if b.m.tabName() != tabDeclared || b.m.focus != PaneDetails {
		t.Fatalf("tab %s, focus %v", b.m.tabName(), b.m.focus)
	}
	b.see("[Declared]", "nothing has been read")
	if n := reads.n.Load(); n != 0 {
		t.Fatalf("%d reads", n)
	}
	b.keys("enter")
	b.until("comparison", b.declaredDone)
	b.see("declared only")
}

func TestDeclaredHelpAndFooter(t *testing.T) {
	b, _, _ := declaredBench(t, benchOpts{})
	b.openDeclaredTab("shop")
	b.see("⏎ compare with the file")
	b.keys("?")
	b.see("Keys")
	help := strings.Join(b.m.helpLines(200), "\n")
	for _, want := range []string{"Compose › Declared tab", "read the compose file and compare", "cancel the read in progress", "--no-compose-files"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}
