package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The headless export: --report runs the launch sequence, the same requests
// as the TUI's `w`, writes one file and exits — 0 with the file written,
// non-zero with nothing at the path.

func names(t *testing.T, dir string) string {
	t.Helper()
	des, _ := os.ReadDir(dir)
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return strings.Join(out, ",")
}

func TestReportHeadlessWritesAndExits(t *testing.T) {
	srv := unixFake(t)
	dir := t.TempDir()
	env := map[string]string{"DOCKER_HOST": srv.Host()}
	p := filepath.Join(dir, "py.json")
	code, out, errs := launch(t, env, "--read-only", "--report", p, "--report-object", "python:3.12-slim")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	wantAll(t, "stdout", out, "wrote "+p, "json", "mode 0600")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", fi, err)
	}
	b, _ := os.ReadFile(p)
	var doc struct {
		Schema    string
		Reference string
		Packages  struct{ Rows []struct{ State string } }
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Schema != "docker-desk/image-report/v1" || doc.Reference != "python:3.12-slim" || len(doc.Packages.Rows) == 0 {
		t.Fatalf("document %+v %v", doc, err)
	}
	if m := srv.Mutations(); len(m) != 0 {
		t.Errorf("a headless export sent %v", m)
	}

	// The same object again, to another path: the same bytes.
	p2 := filepath.Join(dir, "py2.json")
	if code, _, errs := launch(t, env, "--report", p2, "--report-format", "json", "--report-object", "python:3.12-slim"); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	b2, _ := os.ReadFile(p2)
	if !bytes.Equal(b, b2) {
		t.Error("two headless exports of one image differ")
	}

	for _, c := range []struct{ format, object, file, want string }{
		{"spdx", "python:3.12-slim", "py.spdx.json", `"spdxVersion": "SPDX-2.3"`},
		{"cyclonedx", "python:3.12-slim", "py.cdx.json", `"specVersion": "1.5"`},
		{"text", "shop/api:dev", "api.txt", "## secrets\t5"},
		{"json", ":space", "space.json", `"schema": "docker-desk/space-report/v1"`},
		{"text", ":space", "space.txt", "## shared"},
	} {
		p := filepath.Join(dir, c.file)
		code, _, errs := launch(t, env, "--report", p, "--report-format", c.format, "--report-object", c.object)
		if code != 0 {
			t.Errorf("%s %s: exit %d: %s", c.format, c.object, code, errs)
			continue
		}
		b, _ := os.ReadFile(p)
		wantAll(t, c.file, string(b), c.want)
	}
}

func TestReportHeadlessRefusals(t *testing.T) {
	srv := unixFake(t)
	dir := t.TempDir()
	env := map[string]string{"DOCKER_HOST": srv.Host()}
	existing := filepath.Join(dir, "old.json")
	os.WriteFile(existing, []byte("evidence"), 0o644)

	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"existing file", []string{"--report", existing, "--report-object", "python:3.12-slim"}, 1, "already exists; nothing was overwritten (--force replaces it)"},
		{"directory", []string{"--report", dir, "--report-object", "python:3.12-slim", "--force"}, 1, "is a folder"},
		{"missing parent", []string{"--report", filepath.Join(dir, "no", "x.json"), "--report-object", "python:3.12-slim"}, 1, "does not exist"},
		{"no such image", []string{"--report", filepath.Join(dir, "x.json"), "--report-object", "nosuch:1"}, 1, `the daemon has no image "nosuch:1"; nothing was written`},
		{"no object", []string{"--report", filepath.Join(dir, "x.json")}, 2, "--report needs --report-object"},
		{"bad format", []string{"--report", filepath.Join(dir, "x.json"), "--report-format", "xml", "--report-object", "a"}, 2, "unknown report format"},
		{"spdx of space", []string{"--report", filepath.Join(dir, "x.json"), "--report-format", "spdx", "--report-object", ":space"}, 2, "json or text"},
		{"format alone", []string{"--report-format", "json"}, 2, "needs --report PATH"},
		{"force alone", []string{"--force"}, 2, "the workspace never replaces a file"},
		{"with --check", []string{"--check", "--report", filepath.Join(dir, "x.json"), "--report-object", "a"}, 2, "cannot be used together"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs := launch(t, env, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d: %s", code, c.code, errs)
			}
			wantAll(t, "stderr", errs, c.want)
			if out != "" {
				t.Errorf("stdout %q", out)
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "evidence" {
		t.Errorf("the existing file was changed: %q", b)
	}
	if got := names(t, dir); got != "old.json" {
		t.Errorf("left behind: %s", got)
	}

	// --force replaces it, atomically, with mode 0600.
	code, _, errs := launch(t, env, "--report", existing, "--report-object", "python:3.12-slim", "--force")
	fi, _ := os.Stat(existing)
	if b, _ := os.ReadFile(existing); code != 0 || !bytes.HasPrefix(b, []byte("{")) || fi.Mode().Perm() != 0o600 {
		t.Errorf("--force: exit %d %s, mode %v", code, errs, fi.Mode().Perm())
	}
}

// TestReportHeadlessLeavesNoPartialFile makes the archive fail part-way:
// the analysis ends in an error, the program exits non-zero, and neither
// the path nor a temporary file is left.
func TestReportHeadlessLeavesNoPartialFile(t *testing.T) {
	srv := unixFake(t)
	dir := t.TempDir()
	env := map[string]string{"DOCKER_HOST": srv.Host()}
	srv.FailNext("GET", `^/images/get$`, 500, "export exploded", 1)
	p := filepath.Join(dir, "x.json")
	code, _, errs := launch(t, env, "--report", p, "--report-object", "python:3.12-slim")
	if code == 0 {
		t.Fatal("a failed export exited 0")
	}
	wantAll(t, "stderr", errs, "not exported", "nothing was written")
	if got := names(t, dir); got != "" {
		t.Errorf("left behind: %s", got)
	}

	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o500)
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		code, _, errs := launch(t, env, "--report", filepath.Join(ro, "x.json"), "--report-object", ":space")
		if code != 1 {
			t.Errorf("unwritable folder: exit %d", code)
		}
		wantAll(t, "stderr", errs, "cannot be written to (permission denied)")
		os.Chmod(ro, 0o700)
		if got := names(t, ro); got != "" {
			t.Errorf("left in the unwritable folder: %s", got)
		}
	}
}
