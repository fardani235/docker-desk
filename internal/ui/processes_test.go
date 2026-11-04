package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

const topPath = `^/containers/[^/]+/top$`

func procSeed(s *fakeengine.Server) {
	s.AddContainer(fakeengine.ContainerSpec{Name: "web", Image: "nginx:1", Cmd: []string{"nginx", "--password=hunter2"}})
	s.AddContainer(fakeengine.ContainerSpec{Name: "api", Image: "api:1", Cmd: []string{"api-server"}})
	s.AddContainer(fakeengine.ContainerSpec{Name: "off", Image: "api:1", State: "exited", ExitCode: 0})
	s.AddContainer(fakeengine.ContainerSpec{Name: "quiet", Image: "api:1"})
	s.SetProcesses("quiet", &fakeengine.ProcessTable{Titles: []string{"PID", "CMD"}, Rows: [][]string{}})
}

// openProcesses highlights name and switches the Details pane to Processes.
func openProcesses(b *bench, name string) {
	b.t.Helper()
	b.m.focus = PaneList
	b.selectRow(name)
	for b.m.tabName() != tabProcesses {
		b.keys("]")
	}
	b.settle()
}

func (b *bench) tops() int { return b.srv.CountRequests("GET", topPath) }

func TestProcessesTabOnlyForContainers(t *testing.T) {
	for k := Kind(0); k < kindCount; k++ {
		has := false
		for _, tab := range kindTabs[k] {
			has = has || tab == tabProcesses
		}
		if has != (k == KindContainers) {
			t.Errorf("kind %d: Processes tab present=%v", k, has)
		}
	}
}

func TestProcessesLifecycle(t *testing.T) {
	b := newBench(t, benchOpts{seed: procSeed})
	b.m.focus = PaneList
	b.selectRow("web")
	b.settle()
	if n := b.tops(); n != 0 {
		t.Fatalf("%d process requests before the tab was opened", n)
	}

	// First entry fetches and shows the daemon's titles and rows, verbatim.
	openProcesses(b, "web")
	b.see("[Processes]", "UID", "PID", "CMD", "nginx --password=hunter2", "snapshot at", "2 processes")
	if n := b.tops(); n != 1 {
		t.Fatalf("first entry: %d requests, want 1", n)
	}

	// No polling while it stays visible, even across a list refresh.
	b.srv.Emit("container", "start", b.srv.ContainerID("api"), nil)
	b.run(300 * time.Millisecond)
	if n := b.tops(); n != 1 {
		t.Fatalf("visible tab polled: %d requests", n)
	}

	// r while visible takes a new snapshot.
	b.keys("r")
	b.until("refreshed", func() bool { return b.tops() == 2 })

	// Highlight change: old rows go at once, a request for the new one follows.
	b.m.focus = PaneList
	b.selectRow("api")
	if b.m.proc.list != nil && b.m.proc.target != b.m.currentContainerID() {
		t.Fatal("rows of the previous container kept under the new highlight")
	}
	b.notSee("nginx --password=hunter2")
	b.until("next container's snapshot", func() bool { return b.tops() == 3 })
	b.selectRow("web")
	b.until("back", func() bool { return b.tops() == 4 })
	b.see("nginx --password=hunter2")

	// r while hidden invalidates without a request; the next visit fetches.
	b.keys("[") // Stats
	b.settle()
	b.keys("r")
	b.run(200 * time.Millisecond)
	if n := b.tops(); n != 4 {
		t.Fatalf("hidden refresh sent a request: %d", n)
	}
	b.keys("]")
	b.until("fresh snapshot on the next visit", func() bool { return b.tops() == 5 })

	// Nothing of it leaves the pane.
	if len(b.m.jobs.List()) != 0 || strings.Contains(b.m.status.text, "hunter2") {
		t.Fatal("process data reached jobs or the status line")
	}
	for _, r := range b.srv.Mutations() {
		t.Fatalf("the Processes view sent a mutation: %+v", r)
	}
}

func TestProcessesStates(t *testing.T) {
	b := newBench(t, benchOpts{seed: procSeed})

	// Stopped: an explanation, no request, no dialog.
	openProcesses(b, "off")
	b.see("off is not running (exited)", "Processes are available only for running containers.")
	if n := b.tops(); n != 0 || b.m.dlg != nil {
		t.Fatalf("stopped container: %d requests, dialog %v", n, b.m.dlg != nil)
	}

	// Valid but empty.
	b.m.focus = PaneList
	b.selectRow("quiet")
	b.see("No processes were reported.", "PID", "0 processes")
	b.notSee("failed")

	// Race: the container stops after the list said running.
	b.selectRow("api")
	b.see("api-server")
	b.srv.FailNext("GET", topPath, 409, "container abc is not running", 1)
	b.keys("r")
	b.see("api is no longer running.")
	b.notSee("api-server")
	if b.m.dlg != nil {
		t.Fatal("race opened a dialog")
	}

	// General failure stays in the pane and a later refresh replaces it.
	b.srv.FailNext("GET", topPath, 500, "ps: exec format error", 1)
	b.keys("r")
	b.see("Process listing failed: ps: exec format error", "r tries again.")
	b.keys("r")
	b.see("api-server")
	b.notSee("Process listing failed")

	// Stops after a successful snapshot: the rows are hidden.
	b.srv.Crash("api", 1)
	b.see("api is not running (exited)")
	b.notSee("api-server")
}

func TestProcessesStaleOnDisconnect(t *testing.T) {
	b := newBench(t, benchOpts{seed: procSeed})
	openProcesses(b, "web")
	b.see("nginx --password=hunter2")
	b.srv.Stop()
	b.keys("r")
	b.see("stale: disconnected", "nginx --password=hunter2")
	if err := b.srv.Restart(); err != nil {
		t.Fatal(err)
	}
	b.until("recovered", func() bool {
		return !b.m.proc.stale && b.m.proc.valid && b.m.hub.Connected() && !strings.Contains(b.screen(), "stale")
	})
	b.see("nginx --password=hunter2", "r takes a new one")
}

func TestProcessesRejectsLateAnswers(t *testing.T) {
	b := newBench(t, benchOpts{seed: procSeed})
	openProcesses(b, "web")
	b.see("nginx")
	web := b.m.proc.target
	seq := b.m.proc.seq
	b.m.focus = PaneList
	b.selectRow("api")
	b.see("api-server")

	// An answer for web, however late, never appears under api.
	b.step(procMsg{target: web, seq: seq, list: engine.ProcessList{Titles: []string{"CMD"}, Rows: [][]string{{"LATE-WEB"}}}})
	b.notSee("LATE-WEB")
	// Nor does an overtaken answer for api itself.
	b.step(procMsg{target: b.m.proc.target, seq: b.m.proc.seq - 1, list: engine.ProcessList{Titles: []string{"CMD"}, Rows: [][]string{{"OLD-API"}}}})
	b.notSee("OLD-API")
}

func TestProcessesTableAndScrolling(t *testing.T) {
	long := strings.Repeat("x", 300)
	rows := [][]string{{"1"}, {"2", "a", long, "extra", "cells"}, {"3", "\x1b[31mred\x1b[0m", "line\nbreak"}}
	for i := 4; i <= 80; i++ {
		rows = append(rows, []string{fmt.Sprint(i), "u", fmt.Sprintf("proc-%02d", i)})
	}
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		procSeed(s)
		s.SetProcesses("web", &fakeengine.ProcessTable{Titles: []string{"PID", "USER", "COMMAND"}, Rows: rows})
	}})
	openProcesses(b, "web")
	b.see("PID", "USER", "COMMAND", "1–", "of 80")
	b.notSee("TIME") // no assumed ps columns

	for _, sz := range [][2]int{{140, 40}, {80, 24}, {81, 25}, {160, 24}} {
		b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		b.assertFrameGeometry()
	}
	b.step(tea.WindowSizeMsg{Width: 140, Height: 40})

	b.keys("3")
	b.keys("G")
	b.see("proc-80")
	b.notSee("proc-04")
	b.keys("g")
	b.see("proc-04")
	b.notSee("proc-80")
	b.keys("pgdown")
	b.notSee("proc-04")
	b.keys("pgup", "down")
	b.see("2–")
	b.keys("up")
	b.see("1–")
}

func TestProcessTableCells(t *testing.T) {
	list := &engine.ProcessList{Titles: []string{"A", "B"}, Rows: [][]string{{"1"}, {"2", "x", "y"}, {"3", "\x1b]0;evil\x07ok\tz"}}}
	h, rows := processTable(list, 20)
	if len(rows) != 3 || !strings.HasPrefix(h, "A B") {
		t.Fatalf("%q %q", h, rows)
	}
	for _, r := range append(rows, h) {
		if len([]rune(r)) != 20 || strings.ContainsAny(r, "\x1b\x07\t\n") {
			t.Fatalf("unsafe or misfit row %q", r)
		}
	}
	if !strings.Contains(rows[1], "x y") {
		t.Fatalf("extra cells dropped: %q", rows[1])
	}
	if h, rows := processTable(&engine.ProcessList{}, 10); len(rows) != 0 || len(h) != 10 {
		t.Fatalf("empty: %q %q", h, rows)
	}
}

// TestDetailsTabStripFitsNarrowPanes: with six container tabs the strip no
// longer fits at 80 columns; the active tab is still named.
func TestDetailsTabStripFitsNarrowPanes(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 24})
	openProcesses(b, "shop-web-1")
	b.see("[Processes] 5/6")
	b.step(tea.WindowSizeMsg{Width: 140, Height: 40})
	b.see("Info Changes Logs Stats [Processes] Inspect")
}
