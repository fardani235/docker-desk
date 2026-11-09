package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// goEnv is the Go set-up a build under another HOME still needs: the
// caches, so that nothing is downloaded.
func goEnv(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOPATH", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	v := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(v) != 3 {
		t.Fatalf("go env: %q", out)
	}
	return []string{"GOFLAGS=", "GOTOOLCHAIN=local", "GOPATH=" + v[0], "GOCACHE=" + v[1], "GOMODCACHE=" + v[2]}
}

func one(t *testing.T, pattern string) string {
	t.Helper()
	m, _ := filepath.Glob(pattern)
	if len(m) != 1 {
		t.Fatalf("%d files match %s: %v", len(m), pattern, m)
	}
	return m[0]
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %v, want 0600", path, fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
}

type pkgRow struct {
	State     string
	Type      string
	Why       string
	Declared  *struct{ Name, Version string }
	Installed *struct {
		Name, Version string
		Layer         int
	}
}

func (r pkgRow) name() string {
	if r.Installed != nil {
		return r.Installed.Name
	}
	if r.Declared != nil {
		return r.Declared.Name
	}
	return ""
}

// TestExportFromTheSandbox drives `make sandbox` itself, read-only: w on
// python:3.12-slim's Findings tab, in its Packages view and in the Space
// view, each through the dialog's confirmation, into a HOME of the test's
// own. While the sandbox runs, the same binary exports the same image and
// the account headless. Every file is parsed and its content asserted;
// the TUI's image document and the command line's are the same bytes.
func TestExportFromTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	home := t.TempDir()
	sock := filepath.Join(t.TempDir(), "sb.sock")
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	env := append(goEnv(t), "HOME="+home)
	term, err := ptyscreen.Start(40, 140, env, "make", "-C", root, "--no-print-directory", "sandbox", "SANDBOX_SOCKET="+sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, term: term}
	if err := term.WaitFor(60*time.Second, "Docker Desk", "on sandbox", "shop-web-1"); err != nil {
		t.Fatal(err)
	}

	d.keys("1", ptyscreen.Down, "2", "/", "p", "y", "t", "h", "o", "n", ptyscreen.Enter)
	d.see("3 Details: python:3.12-slim")
	d.keys("3", "]", "]")
	d.see("[Findings]", "findings · by layer")
	d.keys("w")
	d.see("Export findings", "▸ json", "docker-desk-findings-python-3.12-slim-")
	d.shot("42-export-findings")
	d.keys(ptyscreen.Enter)
	d.see("Write this file?", "0600: only you can read it")
	d.shot("43-export-confirm")
	d.consent("y")
	d.see("Exported json to")

	d.keys("i")
	d.see("Packages — ")
	d.keys("w")
	d.see("Export packages", "▸ spdx", ".spdx.json")
	d.keys(ptyscreen.Enter)
	d.see("Write this file?")
	d.consent("y")
	d.see("Exported spdx to")
	d.keys(ptyscreen.Esc)
	d.gone("Packages — ")

	d.keys("S")
	d.see("Space — where the bytes went", "RECLAIMABLE")
	d.see("computed from layer sizes") // every image's layers are read
	d.keys("w")
	d.see("Export the Space account", "docker-desk-space-")
	d.keys(ptyscreen.Enter)
	d.see("Write this file?")
	d.consent("y")
	d.see("Exported json to")

	// Headless, against the same sandbox, while it runs, by the binary
	// make built (its version is in every document).
	head := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(root, "bin", "docker-desk"), args...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "DOCKER_HOST=unix://" + sock}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker-desk %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	cliImage := filepath.Join(home, "cli-python.json")
	head("--read-only", "--report", cliImage, "--report-object", "python:3.12-slim")
	cliCDX := filepath.Join(home, "cli-python.cdx.json")
	head("--report", cliCDX, "--report-format", "cyclonedx", "--report-object", "python:3.12-slim")
	cliSpace := filepath.Join(home, "cli-space.txt")
	head("--report", cliSpace, "--report-format", "text", "--report-object", ":space")

	d.keys(ptyscreen.Esc, "q")
	if code := d.quit(); code != 0 {
		t.Fatalf("make sandbox exited with %d", code)
	}

	// -- the Findings export ----------------------------------------------
	tuiImage := one(t, filepath.Join(home, "docker-desk-findings-python-3.12-slim-*.json"))
	var img struct {
		Schema, ID, Reference, Platform string
		Findings                        []struct{ Rule, Group string }
		Packages                        struct {
			Databases []struct{ Ecosystem, State string }
			Rows      []pkgRow
		}
	}
	readJSON(t, tuiImage, &img)
	if img.Schema != "docker-desk/image-report/v1" || img.Reference != "python:3.12-slim" || img.Platform != "linux/amd64" || !strings.HasPrefix(img.ID, "sha256:") {
		t.Errorf("image document: %+v", img)
	}
	rules := map[string]bool{}
	for _, f := range img.Findings {
		rules[f.Rule] = true
	}
	for _, r := range []string{"supply.sbom", "supply.undeclared", "supply.sbom-drift"} {
		if !rules[r] {
			t.Errorf("no %s finding in %v", r, rules)
		}
	}
	states := map[string]pkgRow{}
	for _, r := range img.Packages.Rows {
		states[r.name()] = r
	}
	if r := states["libssl3"]; r.State != "version differs" || r.Declared == nil || r.Installed == nil || r.Declared.Version == r.Installed.Version {
		t.Errorf("libssl3: %+v", r)
	}
	if r := states["curl"]; r.State != "undeclared" || r.Installed == nil || r.Installed.Layer != 4 {
		t.Errorf("curl: %+v", r)
	}
	for _, n := range []string{"pip", "requests"} {
		if r := states[n]; r.State != "not checkable" || r.Type != "pypi" || r.Why == "" || r.Installed != nil {
			t.Errorf("%s: %+v", n, r)
		}
	}
	if r := states["python"]; r.State != "not checkable" || !strings.Contains(r.Why, "no purl") {
		t.Errorf("python (no purl): %+v", r)
	}
	tuiBytes, _ := os.ReadFile(tuiImage)
	cliBytes, _ := os.ReadFile(cliImage)
	if string(tuiBytes) != string(cliBytes) {
		tl, cl := strings.Split(string(tuiBytes), "\n"), strings.Split(string(cliBytes), "\n")
		for i := 0; i < len(tl) && i < len(cl); i++ {
			if tl[i] != cl[i] {
				t.Errorf("the TUI's export and --report's of the same image differ at line %d:\n tui %s\n cli %s", i+1, tl[i], cl[i])
				break
			}
		}
	}

	// -- the Packages export (SPDX) ----------------------------------------
	var spdx struct {
		SPDXVersion string
		Packages    []struct {
			Name, VersionInfo, Comment string
		}
	}
	readJSON(t, one(t, filepath.Join(home, "docker-desk-packages-python-3.12-slim-*.spdx.json")), &spdx)
	if spdx.SPDXVersion != "SPDX-2.3" || len(spdx.Packages) != len(img.Packages.Rows) {
		t.Errorf("SPDX: %s, %d packages for %d rows", spdx.SPDXVersion, len(spdx.Packages), len(img.Packages.Rows))
	}
	for i := 1; i < len(spdx.Packages); i++ {
		a, b := spdx.Packages[i-1], spdx.Packages[i]
		if a.Name > b.Name || (a.Name == b.Name && a.VersionInfo > b.VersionInfo) {
			t.Errorf("not by name then version: %s %s before %s %s", a.Name, a.VersionInfo, b.Name, b.VersionInfo)
		}
	}
	for _, p := range spdx.Packages {
		if p.Name == "pip" && !strings.Contains(p.Comment, "state: not checkable") {
			t.Errorf("pip lost its state: %q", p.Comment)
		}
	}

	// -- the CycloneDX export ----------------------------------------------
	var cdx struct {
		BOMFormat, SpecVersion string
		Components             []struct{ Name string }
	}
	readJSON(t, cliCDX, &cdx)
	if cdx.BOMFormat != "CycloneDX" || cdx.SpecVersion != "1.5" || len(cdx.Components) != len(img.Packages.Rows) {
		t.Errorf("CycloneDX: %+v", cdx)
	}

	// -- the Space export ----------------------------------------------------
	var sp struct {
		Schema string
		Totals []struct {
			Type string
			Size int64
		}
		Images []struct {
			References []string
			Alone      struct{ Measure string }
			Together   *struct{ Measure, Reason string }
		}
		SharedGroups []struct {
			Images []string
			Size   struct {
				Bytes   int64
				Measure string
			}
		} `json:"shared_groups"`
	}
	readJSON(t, one(t, filepath.Join(home, "docker-desk-space-*.json")), &sp)
	if sp.Schema != "docker-desk/space-report/v1" || len(sp.Totals) != 4 || sp.Totals[0].Type != "Images" || sp.Totals[0].Size <= 0 {
		t.Errorf("Space totals: %+v", sp.Totals)
	}
	if len(sp.SharedGroups) == 0 {
		t.Error("no shared groups")
	}
	for _, g := range sp.SharedGroups {
		if g.Size.Measure != "exact" && g.Size.Measure != "at least" && g.Size.Measure != "unknown" {
			t.Errorf("a group without its measure: %+v", g)
		}
	}
	for _, im := range sp.Images {
		if im.Alone.Measure == "" || (im.Together != nil && im.Together.Measure != "exact" && im.Together.Reason == "") {
			t.Errorf("an image figure without its measure or reason: %+v", im)
		}
	}
	txt, _ := os.ReadFile(cliSpace)
	if !strings.Contains(string(txt), "total\tImages\t") || !strings.Contains(string(txt), "\nshared\texact\t") {
		t.Errorf("Space text:\n%s", txt)
	}
	for _, f := range []string{tuiImage, cliImage, cliCDX, cliSpace} {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), home) || strings.Contains(string(b), sock) {
			t.Errorf("%s holds a host path", f)
		}
	}
}
