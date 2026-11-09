package collect

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/report"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

// sandbox is the daemon `make sandbox` serves (fake-dockerd's seed), through
// a read-only engine: an export asks nothing but GETs.
func sandbox(t *testing.T) (*fakeengine.Server, engine.Engine, engine.Info) {
	t.Helper()
	srv := fakeengine.New()
	srv.SeedDemo()
	srv.SeedUpstream()
	srv.Name = "daemon-host-name-qx7" // a name no document may carry
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return srv, eng, info
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/report/collect -update writes it)", err)
	}
	if !bytes.Equal(got, want) {
		gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("%s differs from the golden file at line %d:\n got  %s\n want %s", name, i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("%s differs from the golden file in length: %d lines, want %d", name, len(gl), len(wl))
	}
}

// TestGoldenPerFormat writes every format of the sandbox's two analysed
// images and of its Space account, and compares them with testdata/: the
// documents a reader of these files relies on change only on purpose.
func TestGoldenPerFormat(t *testing.T) {
	srv, eng, info := sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ref := range []string{"python:3.12-slim", "shop/api:dev"} {
		img, err := FetchImage(ctx, eng, info, "test", ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range report.Formats {
			b, err := report.Render(f, report.Document{Image: &img})
			if err != nil {
				t.Fatal(err)
			}
			golden(t, report.Slug(ref)+"."+f.Ext(), b)
		}
	}
	s, err := FetchSpace(ctx, eng, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []report.Format{report.JSON, report.Text} {
		b, err := report.Render(f, report.Document{Space: &s})
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "space."+f.Ext(), b)
	}
	if m := srv.Mutations(); len(m) != 0 {
		t.Errorf("exporting sent %v", m)
	}
}

// TestTwoExportsAreIdentical exports each object twice, from two
// analyses, and requires the same bytes.
func TestTwoExportsAreIdentical(t *testing.T) {
	_, eng, info := sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ref := range []string{"python:3.12-slim", "shop/api:dev"} {
		a, err := FetchImage(ctx, eng, info, "test", ref)
		if err != nil {
			t.Fatal(err)
		}
		b, err := FetchImage(ctx, eng, info, "test", ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range report.Formats {
			x, _ := report.Render(f, report.Document{Image: &a})
			y, _ := report.Render(f, report.Document{Image: &b})
			if !bytes.Equal(x, y) {
				t.Errorf("%s %s: two exports differ", ref, f)
			}
		}
	}
	s1, _ := FetchSpace(ctx, eng, "test")
	s2, _ := FetchSpace(ctx, eng, "test")
	for _, f := range []report.Format{report.JSON, report.Text} {
		x, _ := report.Render(f, report.Document{Space: &s1})
		y, _ := report.Render(f, report.Document{Space: &s2})
		if !bytes.Equal(x, y) {
			t.Errorf("space %s: two exports differ", f)
		}
	}
}

// TestNothingAFileMustNotHold checks every document of the sandbox for what
// the rules keep out: a secret's value, the daemon's address and name,
// container IDs, environment values, times.
func TestNothingAFileMustNotHold(t *testing.T) {
	srv, eng, info := sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var forbidden []string
	d, err := eng.InspectImage(ctx, "shop/api:dev")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := eng.SaveImage(ctx, d.ID, d.Platform())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := forensics.Analyse(ctx, rc, AnalysisOptions(d.ID, "shop/api:dev", d.Platform(), info), nil)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if v := f.Secret.Reveal(); len(v) >= 6 {
			forbidden = append(forbidden, v)
		}
	}
	if len(forbidden) == 0 {
		t.Fatal("the demo image has no secret to look for")
	}
	for _, e := range rep.Config.Env {
		if _, v, ok := strings.Cut(e, "="); ok && len(v) >= 6 {
			forbidden = append(forbidden, v)
		}
	}
	cs, _ := eng.Containers(ctx)
	for _, c := range cs {
		forbidden = append(forbidden, c.ID, engine.ShortID(c.ID))
	}
	forbidden = append(forbidden, srv.Socket(), srv.Host(), srv.Name, time.Now().Format("2006-01-02"))

	var docs [][]byte
	for _, ref := range []string{"python:3.12-slim", "shop/api:dev"} {
		img, err := FetchImage(ctx, eng, info, "test", ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range report.Formats {
			b, _ := report.Render(f, report.Document{Image: &img})
			docs = append(docs, b)
		}
	}
	s, _ := FetchSpace(ctx, eng, "test")
	for _, f := range []report.Format{report.JSON, report.Text} {
		b, _ := report.Render(f, report.Document{Space: &s})
		docs = append(docs, b)
	}
	for i, b := range docs {
		for _, x := range forbidden {
			if bytes.Contains(b, []byte(x)) {
				t.Errorf("document %d holds %q", i, x)
			}
		}
	}
}
