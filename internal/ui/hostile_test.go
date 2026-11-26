package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/session"
)

// The hostile sweep (openspec change escape-daemon-text, D9).
//
// A daemon of the session may be hostile. The fixture's strings each carry
// a different class of value, and every view and dialog that draws one is
// rendered over it. The check is made on the frame as the desk drew it —
// before the frame guard — so it proves the escaping, not the guard: a
// string drawn raw fails here even though the guard would have removed
// what acts.

// hostileSizes are the two terminal sizes every sweep runs at.
var hostileSizes = [][2]int{{120, 40}, {80, 24}}

// hostileBench is a bench over the hostile fixture and one plain container.
func hostileBench(t *testing.T, w, h int, more func(*fakeengine.Server)) (*bench, fakeengine.Hostile) {
	t.Helper()
	var hs fakeengine.Hostile
	b := newBench(t, benchOpts{width: w, height: h, seed: func(s *fakeengine.Server) {
		hs = s.SeedHostile()
		s.AddContainer(fakeengine.ContainerSpec{Name: "plain", Image: "app:1"})
		if more != nil {
			more(s)
		}
	}})
	return b, hs
}

// rawProblem is what is wrong with one line as the desk drew it, or "".
// Nothing may be in it for the guard to remove, and no colour sequence a
// daemon sent: the fixture's are conceal and one colour no theme uses.
func rawProblem(l string) string {
	switch {
	case !lineIsClean(l):
		return "carries something the guard would have to remove"
	case strings.Contains(l, fakeengine.HostileConceal):
		return "carries the daemon's conceal sequence"
	case strings.Contains(l, fakeengine.HostileColour):
		return "carries the daemon's colour sequence"
	}
	for _, r := range l {
		if r >= 0x80 && escape.Reorders(r) {
			return "carries a character that reorders text (" + escape.Rune(r) + ")"
		}
	}
	return ""
}

// inert draws the frame and fails if any line of it carries a daemon's
// sequence, before or after the guard, or if the frame is not the size the
// layout gave it. It returns the frame as text.
func (b *bench) inert(what string) string {
	b.t.Helper()
	b.m.dirty = true
	b.m.View()
	for i, l := range b.m.render() {
		if why := rawProblem(l); why != "" {
			b.t.Fatalf("%s (%dx%d): line %d %s:\n%q", what, b.m.width, b.m.height, i, why, l)
		}
	}
	b.m.dirty = true
	raw := b.m.View()
	for _, bad := range []string{fakeengine.HostileConceal, fakeengine.HostileColour, "\x1b]", "\x07", "\u009b", "\u202e"} {
		if strings.Contains(raw, bad) {
			b.t.Fatalf("%s (%dx%d): the frame written to the terminal carries %q", what, b.m.width, b.m.height, bad)
		}
	}
	b.assertFrameGeometry()
	return ansi.Strip(raw)
}

// shows fails unless the frame, with the Details pane wrapped and scrolled
// to wherever the text is, shows every one of subs.
func (b *bench) shows(what string, subs ...string) {
	b.t.Helper()
	s := b.inert(what)
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			b.t.Fatalf("%s (%dx%d): %q is not drawn:\n%s", what, b.m.width, b.m.height, sub, s)
		}
	}
}

// dialogPages is the open confirmation read to its end: every page of its
// list is drawn, each checked like any frame, and their text is returned
// together. A small terminal scrolls a long confirmation.
func (b *bench) dialogPages(what string) string {
	b.t.Helper()
	var all strings.Builder
	for i := 0; i < 12; i++ {
		all.WriteString(b.inert(what))
		all.WriteString("\n")
		before := b.m.dlg.listOff
		b.keys("pgdown")
		b.m.dirty = true
		b.m.View()
		if b.m.dlg == nil || b.m.dlg.listOff == before {
			break
		}
	}
	return all.String()
}

// infoText is the Info tab's lines as text: what the pane draws, whatever
// part of it the pane's height shows.
func (b *bench) infoText(what string) string {
	b.t.Helper()
	lines := b.m.infoLines()
	for i, l := range lines {
		if why := rawProblem(l); why != "" {
			b.t.Fatalf("%s: Info line %d %s: %q", what, i, why, l)
		}
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}

func wantText(t *testing.T, what, text string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(text, sub) {
			t.Fatalf("%s: %q is not drawn:\n%s", what, sub, text)
		}
	}
}

// Details › Info, all five kinds: every value the daemon chose is drawn as
// the text of what it is — `\x1b[8m`, not a conceal sequence — at both
// sizes, and no line is added or lost.
func TestInfoDrawsHostileTextAsText(t *testing.T) {
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], nil)
		name := escape.Name(h.Name)

		// Containers.
		b.kind(KindContainers)
		b.selectRow(name)
		b.until("the container's inspect", func() bool { return b.m.detail.container != nil && b.m.detail.container.ID == h.ContainerID })
		b.inert("Containers › Info")
		info := b.infoText("Containers › Info")
		wantText(t, "Containers › Info", info,
			`evil\\box\x1b]0;pwned\x07`,                 // name: a backslash doubled, an OSC as text
			`registry.test/evil\x1b[38;5;199mpink:1`,    // image
			`note=shown\x1b[8mhidden-note`,              // label: conceal as text
			`path=C:\\dir`,                              // label: one backslash, drawn as two
			`/data\nforged-line`,                        // mount destination: a newline as text
			`proj\u009b2Jx / web\x1b[8msvc`,             // compose project and service
			`/work�dir`,                                 // working directory: what JSON left of the byte
			`sh -c echo \u202edrow`,                     // command: the override as text
			`root\x1b[8muser`,                           // user
			`un\x1b[8mhealthy`,                          // health
			`boom\x1b[8mhidden-error`,                   // the daemon's error prose
			`vol\x1b[8mdata`, `/host\x1b[38;5;199m/src`, // mount sources
			`net\x1b[8mlan`, // network
		)
		// A newline in a value forges no line: one line per mount.
		if n := strings.Count(info, "forged-line"); n != 1 {
			t.Fatalf("the mount destination is drawn %d times", n)
		}
		for _, l := range strings.Split(info, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "forged-line") {
				t.Fatalf("a newline in a mount destination began a line: %q", l)
			}
		}
		if strings.Contains(info, `\\\\`) {
			t.Fatalf("a value was escaped twice:\n%s", info)
		}

		// Images: the tag and the label.
		b.kind(KindImages)
		b.selectRow(escape.Name(h.ImageRef))
		b.until("the image's inspect", func() bool { return b.m.detail.image != nil })
		b.inert("Images › Info")
		wantText(t, "Images › Info", b.infoText("Images › Info"),
			`registry.test/evil\x1b[38;5;199mpink:1`, `evil\\box\x1b]0;pwned\x07`)
		// An image's labels are not drawn in Info (ED11); they are in the
		// Inspect document, which is drawn with JSON's own escapes.
		b.m.tab[KindImages] = 1
		b.m.dirty = true
		b.m.afterChange()
		b.until("the image's Inspect document", func() bool { return len(b.m.inspect.lines) > 0 })
		insp := strings.Join(b.m.inspect.lines, "\n")
		if why := rawProblem(insp[:min(len(insp), 1<<16)]); why != "" && !strings.Contains(why, "guard") {
			t.Fatalf("Images › Inspect %s", why)
		}
		if !strings.Contains(insp, `ops\u001b[8mhidden-maintainer`) {
			t.Fatalf("the image's label is not in its Inspect document as text:\n%s", insp)
		}
		b.inert("Images › Inspect")
		b.m.tab[KindImages] = 0

		// Volumes.
		b.kind(KindVolumes)
		b.selectRow(escape.Name(h.Volume))
		b.inert("Volumes › Info")
		wantText(t, "Volumes › Info", b.infoText("Volumes › Info"),
			`vol\x1b[8mdata`, `owner=team\x1b[8mhidden-owner`, `evil\\box\x1b]0;pwned\x07`, `at /data/vol\x1b[8mdata`)

		// Networks.
		b.kind(KindNetworks)
		b.selectRow(escape.Name(h.Network))
		b.inert("Networks › Info")
		wantText(t, "Networks › Info", b.infoText("Networks › Info"),
			`net\x1b[8mlan`, `zone=dmz\x1b[8mhidden-zone`, `evil\\box\x1b]0;pwned\x07`)

		// Compose: the project is a label's value.
		b.kind(KindCompose)
		b.selectRow(escape.Name(h.Project))
		b.inert("Compose › Info")
		wantText(t, "Compose › Info", b.infoText("Compose › Info"),
			`proj\u009b2Jx`, `/srv/\x1b[38;5;199mproj`, `/srv/proj/compose\x1b[8m.yaml`, `web\x1b[8msvc`, `evil\\box\x1b]0;pwned\x07`)
	}
}

// Every tab of every kind, the action menu and every action's first dialog
// over the hostile fixture: nothing a daemon sent reaches a frame as
// anything but text.
func TestEveryViewDrawsHostileTextAsText(t *testing.T) {
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], nil)
		rows := [kindCount]string{
			KindContainers: escape.Name(h.Name),
			KindImages:     escape.Name(h.ImageRef),
			KindVolumes:    escape.Name(h.Volume),
			KindNetworks:   escape.Name(h.Network),
			KindCompose:    escape.Name(h.Project),
		}
		for k := Kind(0); k < kindCount; k++ {
			b.kind(k)
			b.selectRow(rows[k])
			b.inert(kindNames[k])
			for tab := range kindTabs[k] {
				b.m.tab[k] = tab
				b.m.dirty = true
				b.m.afterChange()
				b.run(120 * time.Millisecond)
				b.inert(kindNames[k] + " › " + kindTabs[k][tab])
			}
			b.m.tab[k] = 0
			// The action menu names the object.
			b.m.focus = PaneList
			b.keys("enter")
			if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
				t.Fatalf("%s: no action menu", kindNames[k])
			}
			b.inert(kindNames[k] + ": the action menu")
			b.keys("esc")
			// Every action: whatever it opens or says.
			for _, a := range kindActions[k] {
				if a.key == "e" {
					continue // the shell form hands the terminal over on Enter; drawn below
				}
				b.keys(a.key)
				b.run(150 * time.Millisecond)
				b.inert(kindNames[k] + ": " + a.label)
				for i := 0; i < 3 && b.m.dlg != nil; i++ {
					b.keys("esc")
				}
				if b.m.dlg != nil {
					t.Fatalf("%s: %s left a dialog open", kindNames[k], a.label)
				}
			}
		}
	}
}

// mutated reports whether the daemon received a mutating request whose
// method is method and whose path contains every one of parts.
func (b *bench) mutated(method string, parts ...string) bool {
	for _, r := range b.srv.Mutations() {
		if r.Method != method {
			continue
		}
		all := true
		for _, p := range parts {
			all = all && (strings.Contains(r.Path, p) || strings.Contains(r.Query, p))
		}
		if all {
			return true
		}
	}
	return false
}

// jobText is a job's output dialog as text, checked like any frame.
func (b *bench) jobText(id int) string {
	b.t.Helper()
	b.m.dlg = &dialog{kind: dlgJob, jobID: id}
	s := b.inert("the job's output")
	b.m.dlg = nil
	return s
}

// The confirmations restate the object in escaped text, the status line and
// the job say what was done in the same text — once escaped — and what is
// sent is the object's identifier as the daemon gave it.
func TestConfirmationsAndJobsOverHostileObjects(t *testing.T) {
	const shownName = `evil\\box\x1b]0;pwned\x07`
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], func(s *fakeengine.Server) {
			s.AddVolume("idle"+fakeengine.HostileConceal+"vol", nil)
			s.AddNetwork("idle"+fakeengine.HostileConceal+"net", nil)
		})
		name := escape.Name(h.Name)
		if name != shownName {
			t.Fatalf("the fixture's name is drawn as %q", name)
		}

		// Removing a running container: refused, and the refusal names it.
		b.kind(KindContainers)
		b.m.focus = PaneList
		b.selectRow(name)
		b.keys("d")
		d := b.planDialog("the removal's confirmation")
		b.shows("remove, refused", shownName, "REFUSED")
		if strings.Contains(b.dialogText(), `\\\\`) {
			t.Fatalf("the confirmation escapes the name twice: %s", b.dialogText())
		}
		if d.plan.Steps[0].ID != h.ContainerID {
			t.Fatalf("the step's identifier is %q", d.plan.Steps[0].ID)
		}
		b.keys("esc")

		// A selection of several: each is listed, the hostile one as text.
		b.mark(name, "plain")
		b.keys("t")
		b.planDialog("the set's confirmation")
		wantText(t, "stop a selection", b.dialogPages("stop a selection"), shownName, "plain", "stop 2")
		b.keys("esc", "esc")

		// Stopping it: y, the status line, the job.
		b.selectRow(name)
		b.keys("t")
		b.planDialog("the stop's confirmation")
		b.shows("stop", shownName)
		b.keys("y")
		b.shows("the status line as the job starts", "started: stop container "+shownName)
		j := b.waitJob(b.lastJob().ID)
		if j.Err != "" {
			t.Fatalf("the stop failed: %s", j.Err)
		}
		if !b.mutated("POST", "/containers/"+h.ContainerID+"/stop") {
			t.Fatalf("the stop was not sent for the container's ID: %v", b.srv.Mutations())
		}
		out := b.jobText(j.ID)
		if !strings.Contains(out, "stop container "+shownName) {
			t.Fatalf("the job does not name the container as text:\n%s", out)
		}
		if strings.Contains(out, `\\\\`) || strings.Contains(j.Target, `\\\\`) {
			t.Fatalf("the job escapes the name twice: %q\n%s", j.Target, out)
		}
		b.inert("after the stop")

		// Now it can be removed: the typed confirmation, then the request.
		b.until("the list to show it stopped", func() bool {
			r, ok := b.m.currentRow()
			return ok && r.containerID == h.ContainerID && r.state == "exited"
		})
		b.keys("d")
		b.planDialog("the removal's confirmation")
		b.shows("remove", shownName, "Type remove")
		b.typeText("remove")
		b.keys("enter")
		j = b.waitJob(b.lastJob().ID)
		if j.Err != "" || !b.mutated("DELETE", "/containers/"+h.ContainerID) {
			t.Fatalf("the removal: %q, %v", j.Err, b.srv.Mutations())
		}
		b.inert("after the removal")

		// A prune lists exactly what it removes, as text.
		b.kind(KindVolumes)
		b.m.focus = PaneList
		b.keys("X")
		b.planDialog("the prune's confirmation")
		b.keys("tab") // every unused volume, named ones included
		b.until("the other scope", func() bool { return b.m.dlg != nil && !b.m.dlg.recomputing && b.m.dlg.scopeAll })
		wantText(t, "prune volumes", b.dialogPages("prune volumes"), `idle\x1b[8mvol`)
		b.keys("esc")
		b.kind(KindNetworks)
		b.m.focus = PaneList
		b.keys("X")
		b.planDialog("the network prune's confirmation")
		wantText(t, "prune networks", b.dialogPages("prune networks"), `idle\x1b[8mnet`)
		b.keys("esc")
	}
}

// A compose project is a label's value. It is drawn as text, and its
// containers are found — and stopped — by the value the daemon gave.
func TestProjectNamedByAHostileLabelIsStopped(t *testing.T) {
	b, h := hostileBench(t, 120, 40, nil)
	b.kind(KindCompose)
	b.m.focus = PaneList
	b.selectRow(escape.Name(h.Project))
	b.keys("t")
	b.planDialog("the project's confirmation")
	b.shows("stop project", `proj\u009b2Jx`, `evil\\box\x1b]0;pwned\x07`)
	b.keys("y")
	j := b.waitJob(b.lastJob().ID)
	if j.Err != "" || !b.mutated("POST", "/containers/"+h.ContainerID+"/stop") {
		t.Fatalf("the project's container was not stopped: %q, %v", j.Err, b.srv.Mutations())
	}
	if j.Target != `proj\u009b2Jx` {
		t.Fatalf("the job's target is %q", j.Target)
	}
	b.inert("after the project stop")
}

// The connect and disconnect pickers list containers by name, as text, and
// what is sent names the network and the container by their IDs.
func TestPickersOverHostileObjects(t *testing.T) {
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], nil)
		b.kind(KindNetworks)
		b.m.focus = PaneList
		b.selectRow(escape.Name(h.Network))
		b.keys("D")
		b.until("the picker", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPicker })
		b.shows("the disconnect picker", `net\x1b[8mlan`, `evil\\box\x1b]0;pwned\x07`)
		b.keys("enter")
		b.planDialog("the disconnect's confirmation")
		b.shows("disconnect", `net\x1b[8mlan`, `evil\\box\x1b]0;pwned\x07`)
		b.keys("y")
		j := b.waitJob(b.lastJob().ID)
		if j.Err != "" || !b.mutated("POST", "/networks/"+h.NetworkID+"/disconnect") {
			t.Fatalf("the disconnect: %q, %v", j.Err, b.srv.Mutations())
		}
		out := b.jobText(j.ID)
		if !strings.Contains(out, `disconnect evil\\box\x1b]0;pwned\x07 from net\x1b[8mlan`) {
			t.Fatalf("the job's line:\n%s", out)
		}
		// And back: the connect picker now offers it.
		b.keys("c")
		b.until("the picker", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPicker })
		b.shows("the connect picker", `evil\\box\x1b]0;pwned\x07`)
		b.keys("esc")

		// The daemon picker and the help name the daemon and the endpoint.
		b.keys(keyDaemons)
		b.inert("the daemon picker")
		b.keys("esc")
		b.keys("?")
		b.inert("the help")
		b.keys("esc")
	}
}

// The Events pane draws the daemon's event — action, outcome, name,
// attributes — as text, and still tones it by what the action is.
func TestEventsDrawHostileTextAsText(t *testing.T) {
	b, h := hostileBench(t, 160, 44, nil)
	b.srv.Emit("container", "die", h.ContainerID, map[string]string{"name": h.Name, "exitCode": "1" + fakeengine.HostileConceal})
	b.srv.Emit("container", "kill"+fakeengine.HostileConceal, h.ContainerID, map[string]string{"name": h.Name, "signal": "9"})
	b.srv.Emit("network", "connect", h.NetworkID, map[string]string{"name": h.Network, "container": h.ContainerID})
	b.srv.Emit("image", "pull", h.ImageRef, map[string]string{"name": h.ImageRef})
	b.srv.Emit("volume", "mount", h.Volume, nil)
	b.srv.Emit("plug"+fakeengine.HostileConceal, "enable", "x", map[string]string{"name": "p" + fakeengine.HostileOSC})
	b.until("the events", func() bool { _, end := b.m.hub.Events.Bounds(); return end >= 6 })
	b.settle()
	b.shows("the Events pane",
		`die exit 1\x1b[8m`, `evil\\box\x1b]0;pwned\x07`, `kill\x1b[8m`, `net\x1b[8mlan`, `vol\x1b[8mdata`, `registry.test/evil\x1b[38;5;199mpink:1`)

	// The tone is decided by the raw action: a die with a failing code is
	// bad, whatever its exit code's text carries; an action the desk does
	// not know is not toned as the one it resembles.
	entries, _ := b.m.hub.Events.Read(0, 6)
	nameOf := func(id string) string { return id }
	if _, tn := eventText(entries[0], 100, nameOf); tn != toneBad {
		t.Fatalf("a failing die is toned %v", tn)
	}
	if _, tn := eventText(entries[1], 100, nameOf); tn != toneNormal {
		t.Fatalf("an unknown action is toned %v", tn)
	}
	// The columns are chosen by the raw action too: the exit code sits
	// beside a real die, and a look-alike gets no outcome column.
	if text, _ := eventText(entries[1], 100, nameOf); strings.Contains(text, " sig ") {
		t.Fatalf("a look-alike of kill was given its column: %q", text)
	}

	// Held, with the cursor on each: the selected row and the jump's words.
	b.keys("4", " ")
	for i := 0; i < 6; i++ {
		b.inert("the Events pane, held")
		b.keys("enter")
		b.run(80 * time.Millisecond)
		b.inert("after going to an event's object")
		b.keys("4")
		b.keys("up")
	}
}

// The overlay views — Space, Files, Attention — over the hostile fixture.
func TestOverlayViewsDrawHostileTextAsText(t *testing.T) {
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], nil)
		b.openSpace()
		b.inert("Space")
		for range b.m.spaceObjectRows() {
			b.keys("down")
			b.inert("Space, moving")
		}
		b.keys("enter")
		b.run(80 * time.Millisecond)
		b.inert("Space: enter")
		for i := 0; i < 2 && (b.m.dlg != nil || b.m.sp.open); i++ {
			b.keys("esc")
		}

		b.openAttention()
		b.inert("Attention")
		b.keys("esc")

		b.openFilesOn(KindContainers, escape.Name(h.Name))
		b.run(150 * time.Millisecond)
		b.inert("Files on the container")
		b.keys("enter")
		b.run(300 * time.Millisecond)
		b.inert("Files on the container, archive read")
		b.keys("esc")
		for i := 0; i < 3 && b.m.fs.open; i++ {
			b.keys("esc")
		}
		b.openFilesOn(KindImages, escape.Name(h.ImageRef))
		b.inert("Files on the image")
		b.keys("enter")
		b.run(300 * time.Millisecond)
		b.inert("Files on the image, archive read")
		for i := 0; i < 3 && b.m.fs.open; i++ {
			b.keys("esc")
		}

		// Findings, analysed: the live line and the live findings name the
		// container.
		b.kind(KindImages)
		b.selectRow(escape.Name(h.ImageRef))
		for i, tab := range kindTabs[KindImages] {
			if tab == tabFindings {
				b.m.tab[KindImages] = i
			}
		}
		b.m.focus = PaneDetails
		b.m.dirty = true
		b.m.afterChange()
		b.settle()
		b.keys("enter")
		b.until("the analysis", func() bool { return b.m.fx.run == nil })
		b.settle()
		b.shows("Findings, analysed", `evil\\box\x1b]0;pwned\x07`)
		for i := 0; i < 12; i++ {
			b.keys("down")
			b.inert("Findings, moving")
		}
	}
}

// rawBytesProblem is what in a written file could act on a terminal when
// the file is shown, or "".
func rawBytesProblem(data []byte) string {
	for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u202e", fakeengine.HostileInvalidUTF8} {
		if strings.Contains(string(data), bad) {
			return "carries " + escape.Name(bad)
		}
	}
	return ""
}

// Screen and export use one mapping: the container a finding names is the
// same escaped text in the Findings tab and in every format the export
// writes, and no format carries the sequence itself.
func TestFindingsExportSaysWhatTheScreenSays(t *testing.T) {
	const shownName = `evil\\box\x1b]0;pwned\x07`
	b, h := hostileBench(t, 140, 44, nil)
	b.exportHome()
	b.kind(KindImages)
	b.selectRow(escape.Name(h.ImageRef))
	for i, tab := range kindTabs[KindImages] {
		if tab == tabFindings {
			b.m.tab[KindImages] = i
		}
	}
	b.m.focus = PaneDetails
	b.m.dirty = true
	b.m.afterChange()
	b.settle()
	b.keys("enter")
	b.until("the analysis", func() bool { return b.m.fx.run == nil && strings.Contains(b.screen(), "findings · by") })
	b.settle()
	b.shows("Findings", shownName)

	b.keys("w")
	if b.m.dlg == nil || b.m.dlg.kind != dlgExport {
		t.Fatalf("w opened no export dialog:\n%s", b.screen())
	}
	b.inert("the export dialog")
	x := b.m.dlg.exp
	for _, f := range x.formats {
		data, err := report.Render(f, x.doc)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if why := rawBytesProblem(data); why != "" {
			t.Fatalf("the %s export %s", f, why)
		}
		switch f {
		case report.Text:
			if !strings.Contains(string(data), shownName) {
				t.Fatalf("the text export does not name the container as the screen does:\n%s", data)
			}
			if strings.Contains(string(data), `evil\\\\box`) {
				t.Fatalf("the text export escapes the name twice:\n%s", data)
			}
		case report.JSON:
			// JSON quotes the escaped text once more, as JSON does for
			// any string: decoded, it is the screen's text.
			var doc struct {
				Findings []struct{ Subject, Evidence string }
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, fd := range doc.Findings {
				found = found || (fd.Subject == shownName && strings.Contains(fd.Evidence, shownName))
			}
			if !found {
				t.Fatalf("the JSON export does not name the container as the screen does:\n%s", data)
			}
		}
	}
}

// With colour disabled the desk emits no colour sequence of its own, and a
// daemon's is still drawn as text: it does not change the terminal's
// colours.
func TestHostileTextWithColourDisabled(t *testing.T) {
	var h fakeengine.Hostile
	b := newBench(t, benchOpts{width: 120, height: 40, theme: "mono", seed: func(s *fakeengine.Server) { h = s.SeedHostile() }})
	b.kind(KindContainers)
	b.selectRow(escape.Name(h.Name))
	b.until("the container's inspect", func() bool { return b.m.detail.container != nil })
	b.inert("Details, no colour")
	wantText(t, "Details, no colour", b.infoText("Details, no colour"), `note=shown\x1b[8mhidden-note`)
	b.m.dirty = true
	raw := b.m.View()
	for i := 0; i < len(raw); i++ {
		if n := sgrLen(raw[i:]); n > 0 {
			seq := raw[i : i+n]
			// No colour is chosen by anyone: only attributes and resets.
			if strings.Contains(seq, "38;") || strings.Contains(seq, "48;") || strings.Contains(seq, "[3") || strings.Contains(seq, "[4") || strings.Contains(seq, "[9") {
				if !strings.HasPrefix(seq, "\x1b[4m") { // underline is an attribute
					t.Fatalf("a colour sequence in a frame drawn without colour: %q", seq)
				}
			}
			i += n - 1
		}
	}
}

// A re-create's confirmation over the hostile container: the plan refuses
// (the name is none a daemon accepts), and says why in text.
func TestRecreateConfirmationOverAHostileContainer(t *testing.T) {
	for _, size := range hostileSizes {
		b, h := hostileBench(t, size[0], size[1], nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		d, err := b.eng.InspectContainer(ctx, h.ContainerID)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := plan.Take(ctx, b.eng)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		spec, refused, err := plan.Clone(d, engine.ImageDetail{Raw: []byte(`{}`)}, plan.DefaultsFrom(b.m.info))
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.ContainerRecreate(snap, d, spec, refused, plan.ImagePin{Ref: spec.Image, ID: d.ImageID})
		if err != nil {
			// Not applicable is drawn on the status line.
			b.m.setStatus(toneWarn, err.Error())
			b.shows("re-create, not applicable", `evil\\box\x1b]0;pwned\x07`)
			continue
		}
		b.m.dlg = &dialog{kind: dlgPlan, plan: p, hasInput: true, input: newInput("", 24)}
		text := b.dialogPages("the re-create's confirmation")
		wantText(t, "re-create", text, `evil\\box\x1b]0;pwned\x07`)
		if strings.Contains(text, `\\\\`) {
			t.Fatalf("the re-create's confirmation escapes twice:\n%s", text)
		}
		for _, st := range p.Steps {
			if st.Op == plan.OpRename && st.ID != h.ContainerID {
				t.Fatalf("the rename addresses %q", st.ID)
			}
		}
		b.m.dlg = nil
	}
}

// Operator input drawn back: a path typed into the copy-in form is drawn
// as text in the form and in the confirmation, and the request carries the
// path as it was typed — one backslash, not two.
func TestCopyInDrawsTypedPathsAsTextAndSendsThemRaw(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	home := b.exportHome()
	src := filepath.Join(home, `as\sets`+"\u202e"+"x")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.copyForm("i", "/etc/nginx")
	d := b.m.dlg
	// The input drops control characters itself: an ESC cannot be typed
	// into it. What it keeps — a backslash, a character that reorders —
	// is what is drawn back.
	d.fields[copyInSource].rows[0].SetValue("~/as\\sets\u202ex\x1b[8m")
	if v := d.fields[copyInSource].rows[0].Value(); strings.Contains(v, "\x1b") {
		t.Fatalf("the input holds an ESC: %q", v)
	}
	d.fields[copyInSource].rows[0].SetValue(`~/as\sets` + "\u202e" + "x")
	d.fields[copyInDest].rows[0].SetValue(`/srv/a\b`)
	form := b.inert("the copy-in form")
	// The editing line echoes the keystrokes — a backslash stays one — and
	// shows the character that would have reordered it.
	if !strings.Contains(form, `~/as\sets\u202ex`) || !strings.Contains(form, `/srv/a\b`) {
		t.Fatalf("the form does not draw the typed paths as text:\n%s", form)
	}
	b.keys("enter")
	b.until("the confirmation", func() bool { return b.m.dlg != nil && (b.m.dlg.kind == dlgPlan || b.m.dlg.formErr != "") })
	if b.m.dlg.kind != dlgPlan {
		t.Fatalf("no confirmation: %s", b.m.dlg.formErr)
	}
	conf := b.dialogPages("the copy-in confirmation")
	wantText(t, "the copy-in confirmation", conf, "shop-web-1")
	// A long path is wrapped by the dialog; what it wraps is the plan's.
	wantText(t, "the copy-in plan", strings.Join(b.m.dlg.plan.Effects, "\n"), `as\\sets\u202ex`, `/srv/a\\b`)
	if strings.Contains(strings.Join(b.m.dlg.plan.Effects, "\n"), `\\\\`) {
		t.Fatalf("the plan escapes a path twice: %q", b.m.dlg.plan.Effects)
	}
	st := b.m.dlg.plan.Steps[0]
	if st.Copy.Dest != `/srv/a\b` || st.Copy.Source != src {
		t.Fatalf("the step: dest %q, source %q", st.Copy.Dest, st.Copy.Source)
	}
	b.srv.ResetRequests()
	b.keys("y")
	j := b.waitJob(b.lastJob().ID)
	if j.Err != "" {
		t.Fatalf("the copy failed: %s", j.Err)
	}
	// The request names the destination with one backslash: the upload is
	// sent to its parent, and the archive's entries are named by its last
	// element — as it was typed.
	ups := b.srv.Uploads()
	if len(ups) != 1 || ups[0].Path != "/srv" || ups[0].Err != "" || len(ups[0].Entries) == 0 {
		t.Fatalf("the upload: %+v", ups)
	}
	for _, e := range ups[0].Entries {
		if e != `a\b` && !strings.HasPrefix(e, `a\b/`) {
			t.Fatalf("the archive names %q, not the typed destination", e)
		}
	}
	if _, ok := b.srv.ContainerFile("shop-web-1", `/srv/a\b/a.txt`); !ok {
		t.Fatalf("the container has no /srv/a\\b/a.txt: %v", b.srv.ContainerFiles("shop-web-1", "/srv"))
	}
	out := b.jobText(j.ID)
	if !strings.Contains(out, `/srv/a\\b`) || strings.Contains(out, `/srv/a\\\\b`) {
		t.Fatalf("the job's output:\n%s", out)
	}
}

// An archive's names are whoever built the image's. A finding's path is
// drawn as text in its row and in the detail block, the export writes the
// same text, and a secret beside it is still masked.
func TestFindingsDrawHostileArchiveNamesAsText(t *testing.T) {
	const secret = "s3cr3t-Hunter2-Value-9911"
	img := fixture.Plain("acme/evil:1")
	img.Layers = append(img.Layers, fixture.Layer{
		CreatedBy: "COPY . /app # buildkit",
		Files: []fixture.File{
			{Path: "app/ev\x1b[8mil\\dir/.env", Body: []byte("DB_PASSWORD=" + secret + "\n")},
			{Path: "bin/su\x1b[8mid\u202eexe", Body: []byte("#!/bin/sh\n"), Mode: 0o4755},
		},
	})
	for _, size := range hostileSizes {
		b := newBench(t, benchOpts{width: size[0], height: size[1], seed: func(s *fakeengine.Server) {
			s.AddImage(1<<20, time.Hour, "acme/evil:1")
			s.SetImageArchive("acme/evil:1", fixture.OCI(img).Archive)
		}})
		b.exportHome()
		b.openFindings("acme/evil:1")
		b.keys("enter")
		rep := b.waitReport()
		var paths []string
		for _, f := range rep.Findings {
			paths = append(paths, f.Path)
			for _, s := range []string{f.Path, f.Evidence, f.Reason, f.Instruction, f.Unknown} {
				if why := rawBytesProblem([]byte(s)); why != "" {
					t.Fatalf("%s: a finding's text %s: %q", f.Rule, why, s)
				}
				if strings.Contains(s, secret) {
					t.Fatalf("%s: the secret is in the finding's text: %q", f.Rule, s)
				}
			}
		}
		const envPath, suidPath = `/app/ev\x1b[8mil\\dir/.env`, `/bin/su\x1b[8mid\u202eexe`
		joined := strings.Join(paths, "\n")
		if !strings.Contains(joined, strings.TrimPrefix(envPath, "/")) || !strings.Contains(joined, strings.TrimPrefix(suidPath, "/")) {
			t.Fatalf("the findings' paths: %q", paths)
		}
		// Every finding in turn: its row and its detail block.
		seenEnv, seenSuid := false, false
		for i := 0; i < len(rep.Findings)+2; i++ {
			s := b.inert("Findings over a hostile archive")
			if strings.Contains(s, secret) {
				t.Fatalf("the secret is on screen:\n%s", s)
			}
			flat := b.flat()
			// A row cuts a long path at its end: the beginning is what
			// carries the sequence, and it is text.
			seenEnv = seenEnv || strings.Contains(flat, `/app/ev\x1b[8mil\\dir`)
			seenSuid = seenSuid || strings.Contains(flat, `/bin/su\x1b[8mid`)
			b.keys("down")
		}
		if size[0] >= 120 && (!seenEnv || !seenSuid) {
			t.Fatalf("the paths were not drawn as text (env %v, setuid %v):\n%s", seenEnv, seenSuid, b.screen())
		}
		// The export writes the same text.
		b.keys("w")
		if b.m.dlg == nil || b.m.dlg.kind != dlgExport {
			t.Fatalf("w opened no export dialog:\n%s", b.screen())
		}
		x := b.m.dlg.exp
		for _, f := range x.formats {
			data, err := report.Render(f, x.doc)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if why := rawBytesProblem(data); why != "" {
				t.Fatalf("the %s export %s", f, why)
			}
			if strings.Contains(string(data), secret) {
				t.Fatalf("the %s export carries the secret", f)
			}
			if f == report.Text && !strings.Contains(string(data), strings.TrimPrefix(suidPath, "/")) {
				t.Fatalf("the text export does not write the path as the screen draws it:\n%s", data)
			}
		}
	}
}

// The image-copy destination list, and what choosing from it says, over an
// image whose tag carries a colour sequence: the picker names the image as
// text, and the copy is refused in text — the reference is not one the desk
// can name exactly — with nothing sent to either daemon.
func TestImageCopyPickerOverAHostileImage(t *testing.T) {
	var h fakeengine.Hostile
	b := newCopyBench(t, benchOpts{width: 120, height: 40, seed: func(s *fakeengine.Server) { h = s.SeedHostile() }}, nil)
	b.kind(KindImages)
	b.m.focus = PaneList
	b.selectRow(escape.Name(h.ImageRef))
	b.srv.ResetRequests()
	b.keys("C")
	b.run(150 * time.Millisecond)
	s := b.inert("the image-copy destination list")
	if b.m.dlg != nil && b.m.dlg.kind == dlgPicker {
		if !strings.Contains(s, `registry.test/evil\x1b[38;5;199mpink:1`) {
			t.Fatalf("the picker does not name the image as text:\n%s", s)
		}
		b.keys("enter")
		b.run(200 * time.Millisecond)
		b.inert("after choosing the destination")
	}
	for i := 0; i < 3 && b.m.dlg != nil; i++ {
		b.keys("esc")
	}
	b.inert("after the image-copy picker")
	if n := len(b.srv.Mutations()) + len(b.beta.Mutations()); n != 0 {
		t.Fatalf("%d mutating requests were sent for a reference that cannot be named", n)
	}
}

// What the review found, each held by a line here (docs/DECISIONS.md, ED15).
func TestReviewFindingsStayFixed(t *testing.T) {
	hostile := &engine.Error{Op: "disk usage", Msg: "no\x1b[8m\\x\u202e"}
	const shown = `no\x1b[8m\\x\u202e`

	// 4: every "could not read the daemon's …" error names the daemon's
	// reason as text, whichever read it was.
	if got := (stateErr{err: hostile}).Error(); got != "could not read the daemon's current state: "+shown {
		t.Fatalf("stateErr: %q", got)
	}
	if got := (stateErr{err: hostile, what: "build-cache account"}).Error(); got != "could not read the daemon's build-cache account: "+shown {
		t.Fatalf("stateErr: %q", got)
	}
	// 8e: a job's error has the daemon's message escaped once — also when
	// the message ends in a backslash, and when its composer escaped it.
	if got := jobErrText(fmt.Errorf("reading: %w", hostile)); got != "reading: disk usage: "+shown {
		t.Fatalf("jobErrText: %q", got)
	}
	tail := &engine.Error{Msg: `x\`}
	if got := jobErrText(fmt.Errorf("already %s: %w", escape.Name(tail.Msg), tail)); got != `already x\\: x\\` {
		t.Fatalf("jobErrText: %q", got)
	}
	if got := jobErrText(textErr{escapedOnly{tail}}); got != `x\\` {
		t.Fatalf("jobErrText escaped an escaped message again: %q", got)
	}
	both := &engine.Error{Msg: "a\x1b[8m"}
	if got := jobErrText(fmt.Errorf("said %s: %w", escape.Name(both.Msg), both)); got != `said a\x1b[8m: a\x1b[8m` {
		t.Fatalf("jobErrText left a raw copy: %q", got)
	}
	// 11: only a name of 64 hex digits is a name the daemon made up.
	if isHexName(strings.Repeat("g", 64)) || !isHexName(strings.Repeat("a", 64)) {
		t.Fatal("isHexName")
	}

	// 2: a member that cannot be reached says why in the daemon's words,
	// which are drawn as text on the no-daemon screen and in the picker.
	b, _ := hostileBench(t, 120, 40, nil)
	for _, mem := range b.m.sess.Members() {
		if r := mem.Reason(); r != "" {
			t.Fatalf("the fixture's daemon has a reason: %q", r)
		}
	}
	b.keys(keyDaemons)
	b.inert("the daemon picker")
	b.keys("esc")
}

// A daemon that answers the ping and refuses everything else says why in
// its own words. The picker draws them as text (review finding 2).
func TestUnreachableDaemonsReasonIsDrawnAsText(t *testing.T) {
	b := newMultiBench(t, benchOpts{width: 120, height: 40})
	beta := b.daemon("beta", nil)
	beta.FailNext("GET", "/info$", 500, "refused"+fakeengine.HostileConceal+`\x`+"\u202e", 1000)
	b.pickerTo("c:beta")
	b.keys("enter")
	var mem *session.Member
	b.until("beta to say why it cannot be reached", func() bool {
		for _, m := range b.m.sess.Members() {
			if m.Name() == "beta" && m.Reason() != "" {
				mem = m
			}
		}
		return mem != nil
	})
	if !strings.Contains(mem.Reason(), fakeengine.HostileConceal) {
		t.Fatalf("the reason does not carry the daemon's words: %q", mem.Reason())
	}
	b.run(100 * time.Millisecond)
	s := b.inert("the picker, with a daemon that cannot be reached")
	flat := strings.Join(strings.Fields(s), " ")
	if !strings.Contains(flat, `refused\x1b[8m\\x\u202e`) {
		t.Fatalf("the reason is not drawn as text:\n%s", s)
	}
	b.keys("esc")
	b.inert("the desk, with the notice")
}

// escapedOnly is an error whose text is its inner error's, escaped — and
// which still unwraps to it: what a composer that escaped already looks
// like to jobErrText.
type escapedOnly struct{ err error }

func (e escapedOnly) Error() string { return escape.Name(e.err.Error()) }
func (e escapedOnly) Unwrap() error { return e.err }
