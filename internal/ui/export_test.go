package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/report/collect"
)

var exportDay = time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)

// exportHome points ~ at a fresh folder and fixes the clock the prefilled
// name is dated by.
func (b *bench) exportHome() string {
	home := b.t.TempDir()
	b.m.opts.HomeDir, b.m.opts.WorkDir = home, home
	b.m.opts.Now = func() time.Time { return exportDay }
	return home
}

func (b *bench) exportPath() string {
	b.t.Helper()
	if b.m.dlg == nil || b.m.dlg.kind != dlgExport {
		b.t.Fatalf("no export dialog:\n%s", b.screen())
	}
	return b.m.dlg.exp.input.Value()
}

// typePath replaces the dialog's path with p.
func (b *bench) typePath(p string) {
	b.step(tea.KeyMsg{Type: tea.KeyCtrlU})
	b.step(tea.KeyMsg{Type: tea.KeyCtrlK})
	b.typeText(p)
}

func (b *bench) exported() string {
	b.t.Helper()
	b.until("the export to end", func() bool {
		return strings.HasPrefix(b.m.status.text, "Exported") || strings.HasPrefix(b.m.status.text, "Not exported")
	})
	return b.m.status.text
}

func sameBytes(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s: %d bytes, the command line's document is %d", what, len(got), len(want))
	}
}

// TestExportFindingsIsTheCommandLinesDocument: w on the Findings tab, in
// read-only mode, writes the same bytes --report writes for the image.
func TestExportFindingsIsTheCommandLinesDocument(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	home := b.exportHome()
	b.openFindings("shop/api:dev")
	b.waitReport()
	b.until("container facts", func() bool { return b.m.fx.factsFor == b.m.dataVersion && !b.m.fx.factsBusy })
	b.keys("w")
	if got, want := b.exportPath(), "~/docker-desk-findings-shop-api-dev-20261109.json"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	b.see("Export findings", "shop/api:dev", "▸ json", "spdx", "cyclonedx", "text")
	b.keys("enter")
	want := filepath.Join(home, "docker-desk-findings-shop-api-dev-20261109.json")
	b.see("Write this file?", "0600: only you can read it")
	if x := b.m.dlg.exp; !x.confirm || x.abs != want {
		t.Fatalf("asks to write %q, want %q", x.abs, want)
	}
	if _, err := os.Stat(want); err == nil {
		t.Fatal("written before y")
	}
	b.keys("y")
	if s := b.exported(); !strings.Contains(s, "Exported json to "+want) {
		t.Fatalf("status %q", s)
	}
	fi, err := os.Stat(want)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	got, _ := os.ReadFile(want)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	img, err := collect.FetchImage(ctx, b.eng, b.m.info, "", "shop/api:dev")
	if err != nil {
		t.Fatal(err)
	}
	cli, _ := report.Render(report.JSON, report.Document{Image: &img})
	sameBytes(t, "the TUI's findings export", got, cli)
	b.noMutations("exporting findings")

	// Text, by Tab, from the same dialog: the extension follows the format.
	b.keys("w", "tab", "tab", "tab")
	if p := b.exportPath(); !strings.HasSuffix(p, "-20261109.txt") {
		t.Errorf("the extension did not follow the format: %q", p)
	}
	b.keys("enter", "y")
	b.exported()
	txt, _ := os.ReadFile(filepath.Join(home, "docker-desk-findings-shop-api-dev-20261109.txt"))
	if !strings.Contains(string(txt), "## secrets\t5\n") {
		t.Errorf("text export:\n%s", txt)
	}
}

// TestExportRefusalsNeverBlockTheDesk: an existing file and a folder are
// refused in the dialog; a folder that cannot be written to fails after
// consent, in one sentence on the status line, and the desk goes on.
func TestExportRefusalsNeverBlockTheDesk(t *testing.T) {
	b := newBench(t, benchOpts{})
	home := b.exportHome()
	b.openFindings("python:3.12-slim")
	b.waitReport()

	existing := filepath.Join(home, "docker-desk-findings-python-3.12-slim-20261109.json")
	os.WriteFile(existing, []byte("evidence"), 0o644)
	b.keys("w", "enter")
	b.see("A file is already there; the desk never replaces one.")
	if b.m.dlg.exp.confirm {
		t.Fatal("an existing file reached the confirmation")
	}

	b.typePath(home)
	b.keys("enter")
	if e := b.m.dlg.formErr; e != home+" is a folder: name a file inside it." {
		t.Errorf("a folder: %q", e)
	}

	b.typePath("~bob/x.json")
	b.keys("enter")
	b.see("Only ~/ is expanded")
	b.keys("esc")
	if b.m.dlg != nil {
		t.Fatal("Esc did not close the dialog")
	}
	if got, _ := os.ReadFile(existing); string(got) != "evidence" {
		t.Errorf("the existing file was changed: %q", got)
	}

	if os.Geteuid() != 0 {
		ro := filepath.Join(home, "ro")
		os.Mkdir(ro, 0o500)
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		b.keys("w")
		b.typePath(filepath.Join(ro, "x.json"))
		b.keys("enter", "y")
		s := b.exported()
		if !strings.HasPrefix(s, "Not exported: cannot write "+filepath.Join(ro, "x.json")+": the folder") ||
			!strings.HasSuffix(s, "cannot be written to (permission denied).") || strings.Count(s, ". ") != 0 {
			t.Errorf("status is not one sentence saying why: %q", s)
		}
		b.see("Not exported")
		os.Chmod(ro, 0o700)
		if des, _ := os.ReadDir(ro); len(des) != 0 {
			t.Errorf("left behind: %v", des)
		}
		// The desk is still the desk.
		before := b.m.fx.order
		b.keys("o")
		if b.m.fx.order == before {
			t.Error("the Findings tab stopped taking keys after a failed export")
		}
	}
	if des, _ := os.ReadDir(home); len(des) != 2 && len(des) != 1 {
		t.Errorf("left in the home folder: %v", des)
	}
}

// TestExportConsentIsNotTypedAhead: a y pressed as the question appears is
// not consent (B25), and n goes back to the path.
func TestExportConsentIsNotTypedAhead(t *testing.T) {
	b := newBench(t, benchOpts{arm: 300 * time.Millisecond})
	home := b.exportHome()
	b.openFindings("python:3.12-slim")
	b.waitReport()
	b.keys("w", "enter", "y")
	p := filepath.Join(home, "docker-desk-findings-python-3.12-slim-20261109.json")
	b.settle()
	if _, err := os.Stat(p); err == nil || b.m.dlg == nil || !b.m.dlg.exp.confirm {
		t.Fatalf("a typed-ahead y wrote the file (%v), or closed the question", err)
	}
	b.keys("n")
	if b.m.dlg == nil || b.m.dlg.exp.confirm {
		t.Fatal("n did not go back to the path")
	}
	b.keys("enter")
	b.run(350 * time.Millisecond)
	b.keys("y")
	b.exported()
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// TestExportPackagesAndSpace: w in the Packages view prefills SPDX; w in
// the Space view offers json and text only, and its document is the one
// --report-object :space writes.
func TestExportPackagesAndSpace(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true, width: 80, height: 24})
	home := b.exportHome()
	b.openPackages("python:3.12-slim")
	b.keys("w")
	if got, want := b.exportPath(), "~/docker-desk-packages-python-3.12-slim-20261109.spdx.json"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	b.keys("enter", "y")
	b.exported()
	if !b.m.pk.open {
		t.Error("exporting closed the Packages view")
	}
	raw, _ := os.ReadFile(filepath.Join(home, "docker-desk-packages-python-3.12-slim-20261109.spdx.json"))
	var spdx struct {
		SPDXVersion string
		Packages    []struct{ Name, Comment string }
	}
	if err := json.Unmarshal(raw, &spdx); err != nil || spdx.SPDXVersion != "SPDX-2.3" || len(spdx.Packages) == 0 {
		t.Fatalf("SPDX %v %+v", err, spdx)
	}
	b.keys("esc")

	b.openSpace()
	b.keys("w")
	if got, want := b.exportPath(), "~/docker-desk-space-default-20261109.json"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	if n := len(b.m.dlg.exp.formats); n != 2 {
		t.Errorf("%d formats for Space", n)
	}
	b.keys("tab", "tab") // back to json
	b.keys("enter", "y")
	b.exported()
	got, _ := os.ReadFile(filepath.Join(home, "docker-desk-space-default-20261109.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := collect.FetchSpace(ctx, b.eng, "")
	if err != nil {
		t.Fatal(err)
	}
	cli, _ := report.Render(report.JSON, report.Document{Space: &s})
	sameBytes(t, "the TUI's Space export", got, cli)
	if !b.m.sp.open {
		t.Error("exporting closed the Space view")
	}
	b.noMutations("exporting packages and Space in read-only mode")
}
