package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/shell"
)

// ---- helpers ----------------------------------------------------------------------

func press(x, y int, btn tea.MouseButton) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: btn}
}

// draw renders a fresh frame, as the runtime does after every Update.
func (b *bench) draw() {
	b.m.dirty = true
	b.m.View()
}

// mouse delivers one pointer event and draws the frame that follows it.
func (b *bench) mouse(msg tea.MouseMsg) {
	b.t.Helper()
	b.step(msg)
	b.until("idle after a pointer event", func() bool { return !b.m.preparing })
	b.draw()
}

// hitOf finds, in a fresh frame, the first region of kind that ok accepts.
func (b *bench) hitOf(kind hitKind, ok func(hit) bool) hit {
	b.t.Helper()
	b.draw()
	for _, h := range b.m.hits {
		if h.kind == kind && (ok == nil || ok(h)) {
			return h
		}
	}
	b.t.Fatalf("no region of kind %d in the frame:\n%s", kind, b.screen())
	return hit{}
}

func (b *bench) hits(kind hitKind) []hit {
	b.draw()
	var out []hit
	for _, h := range b.m.hits {
		if h.kind == kind {
			out = append(out, h)
		}
	}
	return out
}

func centre(r rect) (int, int) { return r.x + r.w/2, r.y + r.h/2 }

func (b *bench) click(h hit) {
	b.t.Helper()
	x, y := centre(h.r)
	b.mouse(press(x, y, tea.MouseButtonLeft))
}

func (b *bench) wheel(h hit, delta int) {
	b.t.Helper()
	x, y := centre(h.r)
	btn := tea.MouseButtonWheelDown
	if delta < 0 {
		btn = tea.MouseButtonWheelUp
	}
	b.mouse(press(x, y, btn))
}

func paneHit(p Pane) func(hit) bool { return func(h hit) bool { return h.pane == p } }

// paneCentre is the centre cell of a pane in the current layout.
func (b *bench) paneCentre(p Pane) (int, int) {
	l := computeLayout(b.m.width, b.m.height)
	return centre([paneCount]rect{l.kinds, l.list, l.details, l.events, l.jobs}[p])
}

func (b *bench) clickPane(p Pane) {
	b.t.Helper()
	b.draw()
	x, y := b.paneCentre(p)
	b.mouse(press(x, y, tea.MouseButtonLeft))
}

// emitEvents puts n engine events into the ring.
func (b *bench) emitEvents(n int) {
	b.t.Helper()
	_, before := b.m.hub.Events.Bounds()
	id := b.srv.ContainerID("redis-cache")
	for i := 0; i < n; i++ {
		b.srv.Emit("container", "exec_start: sh", id, map[string]string{"name": "redis-cache"})
	}
	b.until("the events", func() bool {
		_, end := b.m.hub.Events.Bounds()
		return end >= before+uint64(n)
	})
	b.settle()
}

// startJobs starts n jobs that finish at once.
func (b *bench) startJobs(n int) {
	b.t.Helper()
	for i := 0; i < n; i++ {
		b.m.startJob("test job", "t", func(context.Context, *jobs.Run) error { return nil })
	}
	b.until("the jobs", func() bool { return len(b.m.jobs.List()) >= n && b.m.jobs.RunningCount() == 0 })
	b.settle()
}

// deskState is what an ignored pointer event must leave as it was.
type deskState struct {
	focus  Pane
	kind   Kind
	sel    selection
	dialog bool
	marks  int
	held   bool
}

func (b *bench) state() deskState {
	return deskState{b.m.focus, b.m.kind, b.m.sel[b.m.kind], b.m.dlg != nil, len(b.m.marks[b.m.kind]), b.m.evHeld}
}

// ---- 1.3 the shell hand-off ----------------------------------------------------------

// Returning from a shell asks for mouse reporting again; under --no-mouse it
// asks for nothing.
func TestMouseReEnabledAfterShell(t *testing.T) {
	enables := func(cmd tea.Cmd) int {
		n := 0
		var walk func(tea.Cmd)
		walk = func(c tea.Cmd) {
			if c == nil {
				return
			}
			switch msg := c().(type) {
			case tea.BatchMsg:
				for _, cc := range msg {
					walk(cc)
				}
			default:
				if reflect.TypeOf(msg) == reflect.TypeOf(tea.EnableMouseCellMotion()) {
					n++
				}
			}
		}
		walk(cmd)
		return n
	}
	b := newBench(t, benchOpts{})
	done := shellDoneMsg{session: &shell.Session{}, name: "redis-cache"}
	if _, cmd := b.m.Update(done); enables(cmd) != 1 {
		t.Fatal("returning from a shell did not re-enable mouse reporting")
	}
	b.m.opts.NoMouse = true
	if _, cmd := b.m.Update(done); enables(cmd) != 0 {
		t.Fatal("--no-mouse: returning from a shell enabled mouse reporting")
	}
}

// ---- 2.x the hit map ------------------------------------------------------------------

// Each pane's centre is that pane in the frame's map, and a cached frame
// keeps the same map.
func TestMouseHitMapPanes(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 40})
	b.draw()
	for p := Pane(0); p < paneCount; p++ {
		x, y := b.paneCentre(p)
		h, ok := b.m.hitAt(x, y)
		if !ok || h.pane != p {
			t.Errorf("centre of %s: hit %+v ok=%v", paneNames[p], h, ok)
		}
	}
	before := b.m.hits
	b.m.View() // nothing dirty: the cached frame
	if len(b.m.hits) == 0 || &b.m.hits[0] != &before[0] {
		t.Fatal("a cached frame replaced the hit map")
	}
}

// One coordinate per region, at both layouts; a row scrolled out of view
// has no region.
func TestMouseHitRegions(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newMultiBench(t, benchOpts{width: size[0], height: size[1]})
		beta := b.daemon("beta", containers("b1"))
		b.add("beta", beta)
		b.focusName(b.m.sess.Members()[0].Name())
		b.emitEvents(3)
		b.startJobs(2)

		check := func(kind hitKind, ok func(hit) bool) {
			t.Helper()
			h := b.hitOf(kind, ok)
			x, y := centre(h.r)
			got, found := b.m.hitAt(x, y)
			if !found || got.kind != kind {
				t.Errorf("%dx%d: kind %d at (%d,%d) resolves to %+v", size[0], size[1], kind, x, y, got)
			}
		}
		check(hitKindRow, nil)
		check(hitListRow, nil)
		if size[0] >= wideMinWidth {
			b.selectRow("redis-cache") // a short name: the whole tab strip fits
			check(hitDetailsTab, nil)  // compact: "[Info] 1/6", nothing to pick
		}
		check(hitEventRow, nil)
		check(hitJobRow, nil)
		check(hitDaemon, nil)

		// The last row highlighted: in the compact layout the first is
		// scrolled out and has no region.
		rows := b.m.rows[b.m.kind]
		b.m.selectIndex(len(rows) - 1)
		b.draw()
		first := rows[0].key
		visible := b.m.listHeight
		found := false
		for _, h := range b.hits(hitListRow) {
			found = found || h.key == first
		}
		if scrolled := len(rows) > visible; scrolled && found {
			t.Errorf("%dx%d: a row scrolled out of view can be clicked", size[0], size[1])
		}
		if size[0] == 80 && len(rows) <= visible {
			t.Fatalf("the compact list shows all %d rows; the test needs one scrolled out", len(rows))
		}
	}
}

// After the kind, the filter, the scroll, the focus or the size change, a
// click at the same cell means what the new frame drew there.
func TestMouseHitMapRebuilt(t *testing.T) {
	b := newBench(t, benchOpts{})
	h := b.hitOf(hitListRow, func(h hit) bool { return h.index == 0 })
	x, y := centre(h.r)

	b.kind(KindVolumes)
	b.draw()
	if got, _ := b.m.hitAt(x, y); got.key == h.key || Kind(got.sub) != KindVolumes {
		t.Fatalf("after the kind changed the cell still names %q", got.key)
	}
	b.mouse(press(x, y, tea.MouseButtonLeft))
	if r, _ := b.m.currentRow(); r.key != b.m.rows[KindVolumes][0].key {
		t.Fatalf("the click selected %q, not the volume drawn there", r.key)
	}

	b.kind(KindContainers)
	b.keys("2", "/")
	b.typeText("redis")
	b.keys("enter")
	b.draw()
	got, _ := b.m.hitAt(x, y)
	if got.kind != hitListRow || b.m.rows[KindContainers][got.index].key != got.key || !strings.Contains(got.key, b.srv.ContainerID("redis-cache")) {
		t.Fatalf("after filtering the cell names %+v", got)
	}
	b.keys("esc")

	b.step(tea.WindowSizeMsg{Width: 120, Height: 36})
	if len(b.m.hits) != 0 {
		t.Fatal("a resize kept the old map")
	}
	before := b.state()
	b.step(press(x, y, tea.MouseButtonLeft)) // before the repaint
	if b.state() != before {
		t.Fatal("a click between a resize and its repaint acted")
	}
}

// ---- 3.x clicks -----------------------------------------------------------------------

func TestMouseClicksNavigate(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.srv.ResetRequests()

	// Focusing another pane leaves the highlight.
	sel := b.m.sel[b.m.kind]
	b.clickPane(PaneDetails)
	if b.m.focus != PaneDetails || b.m.sel[b.m.kind] != sel {
		t.Fatalf("focus %v, selection %+v → %+v", b.m.focus, sel, b.m.sel[b.m.kind])
	}

	// A kind.
	b.click(b.hitOf(hitKindRow, func(h hit) bool { return Kind(h.index) == KindVolumes }))
	if b.m.kind != KindVolumes || b.m.focus != PaneKinds {
		t.Fatalf("kind %v focus %v", b.m.kind, b.m.focus)
	}
	b.see("2 Volumes")

	// The third container row: highlighted, no menu, no mark.
	b.kind(KindContainers)
	third := b.hitOf(hitListRow, func(h hit) bool { return h.index == 2 })
	b.click(third)
	if r, _ := b.m.currentRow(); r.key != third.key || b.m.focus != PaneList {
		t.Fatalf("highlight %q focus %v", r.key, b.m.focus)
	}
	if b.m.dlg != nil || len(b.m.marks[KindContainers]) != 0 {
		t.Fatal("a row click opened a menu or marked the row")
	}
	b.until("Details to follow", func() bool { return b.m.detail.key == b.m.currentDetailKey() && !b.m.detail.loading })

	// A tab.
	b.click(b.hitOf(hitDetailsTab, func(h hit) bool { return h.key == tabStats }))
	if b.m.tabName() != tabStats || b.m.focus != PaneDetails {
		t.Fatalf("tab %s focus %v", b.m.tabName(), b.m.focus)
	}

	// A blank area of Events.
	b.clickPane(PaneEvents)
	if b.m.focus != PaneEvents || b.m.evHeld {
		t.Fatalf("Events: focus %v held %v", b.m.focus, b.m.evHeld)
	}

	// A job.
	b.startJobs(3)
	job := b.hitOf(hitJobRow, func(h hit) bool { return h.index != b.m.jobSel })
	b.click(job)
	if b.m.jobSel != job.index || b.m.focus != PaneJobs {
		t.Fatalf("job %d focus %v", b.m.jobSel, b.m.focus)
	}
	b.noMutations("after navigating with the pointer")
}

func TestMouseDaemonStrip(t *testing.T) {
	b := newMultiBench(t, benchOpts{})
	beta := b.daemon("beta", containers("b1", "b2"))
	mem := b.add("beta", beta)
	b.focusName(b.m.sess.Members()[0].Name())
	b.click(b.hitOf(hitDaemon, func(h hit) bool { return h.index == mem.ID() }))
	if b.m.cur != mem {
		t.Fatalf("focused %v, want beta", b.m.cur.Name())
	}
	b.see("b1", "b2")
}

func TestMouseEventRowHolds(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.emitEvents(5)
	rows := b.hits(hitEventRow)
	if len(rows) < 5 {
		t.Fatalf("%d event rows drawn", len(rows))
	}
	older := rows[1]
	b.click(older)
	if !b.m.evHeld || b.m.focus != PaneEvents {
		t.Fatalf("held %v focus %v", b.m.evHeld, b.m.focus)
	}
	base := b.m.evSnapEnd - uint64(len(b.m.evSnap))
	if got := base + uint64(b.m.evCur); got != uint64(older.index) {
		t.Fatalf("cursor on ring entry %d, clicked %d", got, older.index)
	}
	// Held, a click on another row moves the cursor there.
	b.click(b.hitOf(hitEventRow, func(h hit) bool { return h.index == rows[3].index }))
	if base+uint64(b.m.evCur) != uint64(rows[3].index) {
		t.Fatal("a click on a held row did not move the cursor")
	}
}

// ---- 3.3 text rows --------------------------------------------------------------------

func TestMouseTextRows(t *testing.T) {
	b := newBench(t, benchOpts{})
	// The filter, then a row: the filter is kept and the row highlighted.
	b.keys("2", "/")
	b.typeText("re")
	row := b.hitOf(hitListRow, func(h hit) bool { return h.index == 1 })
	b.click(row)
	if b.m.filtering || b.m.filter[KindContainers] != "re" {
		t.Fatalf("filtering %v filter %q", b.m.filtering, b.m.filter[KindContainers])
	}
	if r, _ := b.m.currentRow(); r.key != row.key {
		t.Fatal("the row was not highlighted after the filter ended")
	}
	// The kept filter in the title takes the keyboard again, caret at the end.
	b.click(b.hitOf(hitText, func(h hit) bool { return textRow(h.index) == textListFilter }))
	if !b.m.filtering || b.m.filterIn.Position() != len("re") {
		t.Fatalf("filtering %v caret %d", b.m.filtering, b.m.filterIn.Position())
	}
	b.keys("d") // text, not an action
	if b.m.filter[KindContainers] != "red" || b.m.dlg != nil {
		t.Fatalf("filter %q dialog %v", b.m.filter[KindContainers], b.m.dlg != nil)
	}
	b.keys("enter")

	// The Details search.
	b.keys("3", "/")
	b.typeText("x")
	b.keys("enter")
	b.click(b.hitOf(hitText, func(h hit) bool { return textRow(h.index) == textSearch }))
	if !b.m.ds.typing || b.m.ds.in.Position() != 1 || b.m.focus != PaneDetails {
		t.Fatalf("search typing %v caret %d", b.m.ds.typing, b.m.ds.in.Position())
	}
	b.clickPane(PaneJobs)
	if b.m.ds.typing || b.m.tabQuery() != "x" {
		t.Fatalf("leaving the search: typing %v query %q", b.m.ds.typing, b.m.tabQuery())
	}
	b.noMutations("text rows")
}

func TestMouseFormField(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.keys("2", "n")
	b.until("the create form", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgForm })
	b.typeText("alpine")
	f := b.hitOf(hitFormRow, func(h hit) bool { return h.index == 1 })
	b.click(f)
	d := b.m.dlg
	if d == nil || d.focus != 1 {
		t.Fatalf("form focus %v", d)
	}
	b.typeText("web")
	if got := d.fields[1].value(); got != "web" {
		t.Fatalf("second field %q", got)
	}
	// Back to the first, caret at its end.
	b.click(b.hitOf(hitFormRow, func(h hit) bool { return h.index == 0 }))
	if d.focus != 0 || d.fields[0].rows[0].Position() != len("alpine") {
		t.Fatalf("focus %d caret %d", d.focus, d.fields[0].rows[0].Position())
	}
	// A click anywhere else in the form submits nothing.
	b.click(b.hitOf(hitDialog, nil))
	if b.m.dlg != d || d.formBusy {
		t.Fatal("a click submitted the form")
	}
	b.noMutations("clicking a form")
}

// ---- 3.4 the gate ---------------------------------------------------------------------

func TestMouseIgnored(t *testing.T) {
	cases := []struct {
		name  string
		setup func(b *bench)
		msg   func(x, y int) tea.MouseMsg
	}{
		{"--no-mouse", func(b *bench) { b.m.opts.NoMouse = true }, nil},
		{"too small", func(b *bench) { b.step(tea.WindowSizeMsg{Width: 60, Height: 20}); b.draw() }, nil},
		{"no daemon", func(b *bench) { b.m.cur = nil }, nil},
		{"no frame", func(b *bench) { b.m.frames, b.m.hits = 0, nil }, nil},
		{"motion", nil, func(x, y int) tea.MouseMsg {
			return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone}
		}},
		{"release", nil, func(x, y int) tea.MouseMsg {
			return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
		}},
		{"right", nil, func(x, y int) tea.MouseMsg { return press(x, y, tea.MouseButtonRight) }},
		{"middle", nil, func(x, y int) tea.MouseMsg { return press(x, y, tea.MouseButtonMiddle) }},
		{"wheel sideways", nil, func(x, y int) tea.MouseMsg { return press(x, y, tea.MouseButtonWheelLeft) }},
	}
	b := newBench(t, benchOpts{})
	b.emitEvents(2)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b.t = t
			b.step(tea.WindowSizeMsg{Width: 140, Height: 40})
			cur := b.m.cur
			b.m.opts.NoMouse = false
			b.m.focus = PaneList
			b.draw()
			x, y := b.paneCentre(PaneEvents)
			ex, ey := centre(b.hitOf(hitEventRow, nil).r)
			if tc.setup != nil {
				tc.setup(b)
			}
			before := b.state()
			msg := press
			if tc.msg != nil {
				msg = func(x, y int, _ tea.MouseButton) tea.MouseMsg { return tc.msg(x, y) }
			}
			for _, at := range [][2]int{{x, y}, {ex, ey}} {
				for _, btn := range []tea.MouseButton{tea.MouseButtonLeft, tea.MouseButtonWheelUp} {
					b.step(msg(at[0], at[1], btn))
				}
			}
			after := b.state()
			b.m.cur = cur
			if after != before {
				t.Fatalf("ignored event changed the desk: %+v → %+v", before, after)
			}
		})
	}
}

// ---- 3.5 dialogs ----------------------------------------------------------------------

func TestMouseMenuEntryIsEnter(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.selectRow("redis-cache")
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
		t.Fatal("no menu")
	}
	stop := -1
	for i, a := range b.m.dlg.menu {
		if a.key == "t" {
			stop = i
		}
	}
	b.srv.ResetRequests()
	b.click(b.hitOf(hitMenuRow, func(h hit) bool { return h.index == stop }))
	b.until("the stop confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	if b.m.dlg.plan.Title != "Stop container" {
		t.Fatalf("opened %q", b.m.dlg.plan.Title)
	}
	b.noMutations("clicking Stop in the menu")
}

func TestMousePickerSelectThenChoose(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneList
	b.kind(KindNetworks)
	b.selectRow("orphan-net")
	b.keys("c")
	b.until("the picker", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPicker })
	want := ""
	for _, it := range b.m.dlg.items {
		if it.label == "redis-cache" {
			want = it.id
		}
	}
	row := b.hitOf(hitPickerRow, func(h hit) bool { return h.key == want })
	b.click(row)
	if b.m.dlg == nil || b.m.dlg.kind != dlgPicker || b.m.dlg.items[b.m.dlg.pickSel].id != want {
		t.Fatal("the first click did not just select the row")
	}
	b.click(b.hitOf(hitPickerRow, func(h hit) bool { return h.key == want }))
	b.until("the connect confirmation", func() bool { return b.m.dlg != nil && b.m.dlg.kind == dlgPlan })
	b.noMutations("choosing in the picker")
}

func TestMouseHelpIsInert(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.keys("?")
	help := strings.Join(b.m.helpLines(80), "\n")
	for _, want := range []string{"Mouse", "click", "wheel", "--no-mouse", "never opens a menu"} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not say %q", want)
		}
	}
	box := b.hitOf(hitDialog, nil)
	before := b.state()
	off := b.m.dlg.text.off
	b.click(box)
	if b.m.dlg == nil || b.m.dlg.kind != dlgHelp || b.state() != before || b.m.dlg.text.off != off {
		t.Fatal("a click in the help did something")
	}
	b.wheel(box, 1)
	if b.m.dlg.text.off != off+mouseWheelRows {
		t.Fatalf("the wheel scrolled the help to %d", b.m.dlg.text.off)
	}
	// Outside the dialog: nothing behind it.
	b.clickPane(PaneKinds)
	if b.m.focus != before.focus || b.m.dlg == nil {
		t.Fatal("a click behind the help reached the desk")
	}
}

// ---- 4.x the wheel --------------------------------------------------------------------

func TestMouseWheelDesk(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.m.focus = PaneDetails

	// List: three rows, focus kept.
	b.m.selectIndex(0)
	b.wheel(b.hitOf(hitPane, paneHit(PaneList)), 1)
	if b.m.sel[KindContainers].idx != 3 || b.m.focus != PaneDetails {
		t.Fatalf("list at %d, focus %v", b.m.sel[KindContainers].idx, b.m.focus)
	}
	b.wheel(b.hitOf(hitPane, paneHit(PaneList)), -1)
	if b.m.sel[KindContainers].idx != 0 {
		t.Fatalf("list back at %d", b.m.sel[KindContainers].idx)
	}

	// Kinds: one kind.
	b.wheel(b.hitOf(hitPane, paneHit(PaneKinds)), 1)
	if b.m.kind != KindImages || b.m.focus != PaneDetails {
		t.Fatalf("kind %v focus %v", b.m.kind, b.m.focus)
	}
	b.kind(KindContainers)

	// Jobs: three jobs.
	b.startJobs(5)
	list := b.m.jobs.List()
	b.m.jobSel = list[0].ID
	b.wheel(b.hitOf(hitPane, paneHit(PaneJobs)), 1)
	if b.m.jobSel != list[3].ID {
		t.Fatalf("job %d, want %d", b.m.jobSel, list[3].ID)
	}

	// Details, Inspect: three lines.
	b.selectRow("redis-cache")
	b.m.tab[KindContainers] = 5 // Inspect
	b.until("inspect", func() bool { return len(b.m.inspect.lines) > 10 })
	b.wheel(b.hitOf(hitPane, paneHit(PaneDetails)), 1)
	if b.m.inspect.off != 3 {
		t.Fatalf("inspect at %d", b.m.inspect.off)
	}

	// Details, Logs: following stops, three lines back.
	b.selectRow("noisy-logger")
	b.m.tab[KindContainers] = 2
	if b.m.tabName() != tabLogs {
		t.Fatal(b.m.tabName())
	}
	b.until("log lines", func() bool {
		ring, st := b.m.hub.Log()
		first, end := ring.Bounds()
		return st.Target == b.m.currentContainerID() && end-first > 6
	})
	ring, _ := b.m.hub.Log()
	_, end := ring.Bounds()
	b.wheel(b.hitOf(hitPane, paneHit(PaneDetails)), -1)
	_, end2 := ring.Bounds()
	if b.m.logFollow || b.m.logEnd < end-3 || b.m.logEnd > end2-3 {
		t.Fatalf("follow %v logEnd %d (ring end %d..%d)", b.m.logFollow, b.m.logEnd, end, end2)
	}
	if b.m.focus != PaneDetails {
		t.Fatal("focus moved")
	}
}

func TestMouseWheelEvents(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.emitEvents(8)
	ev := b.hitOf(hitPane, paneHit(PaneEvents))
	b.wheel(ev, -1)
	if !b.m.evHeld || b.m.evCur != len(b.m.evSnap)-1-3 || b.m.focus != PaneList {
		t.Fatalf("held %v cursor %d of %d, focus %v", b.m.evHeld, b.m.evCur, len(b.m.evSnap), b.m.focus)
	}
	b.wheel(ev, 1) // to the newest
	if !b.m.evHeld || b.m.evCur != len(b.m.evSnap)-1 {
		t.Fatalf("cursor %d of %d", b.m.evCur, len(b.m.evSnap))
	}
	b.wheel(ev, 1) // past it: live again
	if b.m.evHeld {
		t.Fatal("a downward notch on the newest entry did not resume")
	}
}

func TestMouseWheelConfirmation(t *testing.T) {
	b := newBench(t, benchOpts{width: 100, height: 26})
	b.open(mutations(b.srv)[15]) // prune images: a list
	d := b.m.dlg
	box := b.hitOf(hitDialog, nil)
	off := d.listOff
	b.wheel(box, 1)
	b.draw()
	if d.listOff == off {
		t.Fatal("the wheel did not move the confirmation's list")
	}
	b.click(box)
	if b.m.dlg != d {
		t.Fatal("a click closed or replaced the confirmation")
	}
	b.noMutations("wheel and click on a confirmation")
}

// ---- 5.x Files, Space, Repository -----------------------------------------------------

func TestMouseFiles(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.readArchive()
	b.srv.ResetRequests()
	rows := b.hits(hitFilesRow)
	if len(rows) < 6 {
		t.Fatalf("%d file rows", len(rows))
	}
	b.click(rows[2])
	if b.m.fs.cursor != rows[2].key {
		t.Fatalf("cursor %q, clicked %q", b.m.fs.cursor, rows[2].key)
	}
	b.wheel(rows[0], 1)
	v, _ := b.m.filesView()
	if v.Find(b.m.fs.cursor) != rows[2].index+3 {
		t.Fatalf("after a notch the cursor is on row %d", v.Find(b.m.fs.cursor))
	}
	// The filter line.
	b.keys("/")
	b.typeText("etc")
	b.click(b.hitOf(hitFilesRow, nil))
	if b.m.fs.typing || b.m.fs.filter != "etc" {
		t.Fatalf("typing %v filter %q", b.m.fs.typing, b.m.fs.filter)
	}
	b.click(b.hitOf(hitText, func(h hit) bool { return textRow(h.index) == textFilesFilter }))
	if !b.m.fs.typing {
		t.Fatal("the filter line did not take the keyboard")
	}
	b.settle()
	if b.m.fs.run != nil || len(b.srv.Requests()) != 0 {
		t.Fatalf("the pointer started a read: %v", b.srv.Requests())
	}
}

func TestMouseSpace(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openSpace()
	b.srv.ResetRequests()
	rows := b.hits(hitSpaceRow)
	if len(rows) < 5 {
		t.Fatalf("%d space rows", len(rows))
	}
	b.click(rows[1])
	if b.m.sp.cursorKey != rows[1].key {
		t.Fatal("the click did not move the cursor")
	}
	b.wheel(rows[0], 1)
	all := b.m.spaceObjectRows()
	if got := b.m.spaceCursor(all); all[got].key != rows[4].key {
		t.Fatalf("after a notch the cursor is on %q, want %q", all[got].key, rows[4].key)
	}
	b.settle()
	if b.m.sp.run != nil || b.m.sp.sizeRun != nil || len(b.srv.Requests()) != 0 {
		t.Fatalf("the pointer measured: %v", b.srv.Requests())
	}
}

func TestMouseRepository(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.until("the tag list", func() bool { return b.m.rb.listing != nil })
	b.described("3.28.2")
	calls := reg.Count()
	rows := b.hits(hitRepoRow)
	if len(rows) < 5 {
		t.Fatalf("%d tag rows", len(rows))
	}
	target := rows[0]
	if target.key == b.m.rb.cursorKey {
		target = rows[1]
	}
	b.click(target)
	if b.m.rb.cursorKey != target.key {
		t.Fatal("the click did not highlight the tag")
	}
	b.wheel(target, 1)
	b.run(100 * time.Millisecond)
	if reg.Count() != calls {
		t.Fatalf("the pointer asked the registry: %d → %d", calls, reg.Count())
	}
	b.see("not asked: the pointer placed the highlight")

	// The name line: typed, then a click away keeps it and asks nothing.
	b.click(b.hitOf(hitText, func(h hit) bool { return textRow(h.index) == textRepoName }))
	if !b.m.rb.editing || b.m.rb.name.Position() != len(b.m.rb.name.Value()) {
		t.Fatal("the name line did not take the keyboard with the caret at the end")
	}
	b.typeText("x")
	name := b.m.rb.name.Value()
	b.click(b.hitOf(hitRepoRow, nil))
	b.run(100 * time.Millisecond)
	if b.m.rb.editing || b.m.rb.name.Value() != name || reg.Count() != calls {
		t.Fatalf("editing %v name %q (want %q), registry %d → %d", b.m.rb.editing, b.m.rb.name.Value(), name, calls, reg.Count())
	}
	if len(b.srv.Mutations()) != 0 {
		t.Fatal("a mutation")
	}
}

// ---- 6.1 the pointer never mutates ----------------------------------------------------

// Every cell of a y-confirmation and of a typed-word confirmation is
// clicked: nothing is sent, no job starts, the dialog stays.
func TestMouseNeverConfirms(t *testing.T) {
	b := newBench(t, benchOpts{})
	all := mutations(b.srv)
	for _, mu := range []mutation{all[1], all[6]} { // stop; remove container (typed)
		b.t = t
		b.open(mu)
		d := b.m.dlg
		if mu.tier == plan.TierTyped {
			b.typeText(mu.word) // the word typed: only Enter is missing
		}
		jobsBefore := len(b.m.jobs.List())
		box := b.hitOf(hitDialog, nil)
		for y := box.r.y; y < box.r.y+box.r.h; y++ {
			for x := box.r.x; x < box.r.x+box.r.w; x++ {
				b.step(press(x, y, tea.MouseButtonLeft))
				b.draw()
				if b.m.dlg != d {
					t.Fatalf("%s: a click at (%d,%d) closed the confirmation", mu.name, x, y)
				}
			}
		}
		for _, h := range b.m.hits {
			if h.kind > hitDialog {
				t.Fatalf("%s: the confirmation offers a clickable row: %+v", mu.name, h)
			}
		}
		b.settle()
		b.noMutations(mu.name + ": every cell clicked")
		if len(b.m.jobs.List()) != jobsBefore {
			t.Fatalf("%s: a job started", mu.name)
		}
		b.keys("esc")
	}
}

// A menu's Remove opens the same confirmation its key opens; in read-only
// mode it is refused exactly as the key is.
func TestMouseMenuRemove(t *testing.T) {
	for _, ro := range []bool{false, true} {
		b := newBench(t, benchOpts{readOnly: ro})
		b.m.focus = PaneList
		b.selectRow("old-migration")
		b.keys("d")
		var byKey string
		if b.m.dlg != nil {
			byKey = b.m.dlg.plan.Title
		}
		keyStatus := b.m.status.text
		b.keys("esc")
		b.m.status = statusLine{}
		b.keys("enter")
		if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
			if ro {
				continue // nothing to offer: the key path is covered elsewhere
			}
			t.Fatal("no menu")
		}
		idx := -1
		for i, a := range b.m.dlg.menu {
			if a.key == "d" {
				idx = i
			}
		}
		if idx < 0 {
			if ro {
				continue
			}
			t.Fatal("no Remove in the menu")
		}
		b.click(b.hitOf(hitMenuRow, func(h hit) bool { return h.index == idx }))
		b.settle()
		var byClick string
		if b.m.dlg != nil {
			byClick = b.m.dlg.plan.Title
		}
		if byClick != byKey || (ro && b.m.status.text != keyStatus) {
			t.Fatalf("read-only=%v: click opened %q / %q, key opened %q / %q", ro, byClick, b.m.status.text, byKey, keyStatus)
		}
		b.noMutations("clicking Remove")
	}
}
