package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/search"
)

// Plain markers, so that what is marked can be read in the test.
var testMarks = search.Marks{On: "<", Off: ">", CurOn: "{", CurOff: "}"}

func newFindView(lines []string, q string) *textView {
	v := &textView{wrap: true}
	v.set(lines)
	v.find.mk = testMarks
	v.find.setQuery(q)
	return v
}

// ---- textView: offsets, the wrapped-row seek, n and N ---------------------------------

// Matches are found in the stripped text and marked in the styled one, at
// the right places, with the line's own colour kept around them.
func TestFindHighlightOffsets(t *testing.T) {
	red, reset := "\x1b[31m", "\x1b[0m"
	v := newFindView([]string{
		"State   " + red + "running" + reset + "   Health " + red + "healthy" + reset,
		"nothing here",
		"HEALTH check: unhealthy",
	}, "health")
	rows := v.render(80, 10)
	if len(v.findMatches()) != 4 {
		t.Fatalf("want 4 matches, got %v", v.findMatches())
	}
	// The first match is current: nothing scrolled, the view is at the top.
	// The mark of "health" opens where the red does, so it is written again
	// after the red, and the red is restored after the mark.
	want0 := "State   " + red + "running" + reset + "   {Health}" + " " + "<" + red + "<health>" + red + "y" + reset
	if rows[0] != want0 {
		t.Errorf("row 0\n got %q\nwant %q", rows[0], want0)
	}
	if rows[1] != "nothing here" {
		t.Errorf("a line without a match must be untouched: %q", rows[1])
	}
	if got := ansi.Strip(rows[2]); got != "<HEALTH> check: un<health>y" {
		t.Errorf("row 2: %q", got)
	}
	if v.find.cur != 0 {
		t.Errorf("current is %d", v.find.cur)
	}
}

// A match in the third row of a wrapped line is brought into view by its
// own row, not the line's first — and only by render, after layout: the key
// handler's request leaves the offset alone.
func TestFindSeeksTheWrappedRow(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i)
	}
	// Line 30 wraps into four rows of 10 at width 10; the match is in the third.
	lines[30] = "aaaaaaaaaabbbbbbbbbbccccNEEDLEdddddddddddd"
	v := newFindView(lines, "needle")
	v.render(10, 5)
	if v.off != 0 {
		t.Fatalf("typing must not scroll: off %d", v.off)
	}
	v.find.seek = true // what Enter does
	if v.off != 0 {
		t.Fatal("a key handler must not move the view")
	}
	rows := v.render(10, 5)
	matchRow := v.lineRow[30] + 2
	if v.rowLine[matchRow] != 30 || v.rowStart[matchRow] != 20 {
		t.Fatalf("row map: line %d start %d", v.rowLine[matchRow], v.rowStart[matchRow])
	}
	if matchRow < v.off || matchRow >= v.off+5 {
		t.Fatalf("the match's row %d is not in view (off %d)", matchRow, v.off)
	}
	if v.off != matchRow-5/3 {
		t.Errorf("an off-screen match goes a third of the way down: off %d, row %d", v.off, matchRow)
	}
	var shown string
	for _, r := range rows {
		if strings.Contains(r, "{NEEDLE}") {
			shown = r
		}
	}
	if shown != "cccc{NEEDLE}" {
		t.Errorf("the marked row is %q; rows %q", shown, rows)
	}

	// The same at another width: the seek is resolved for the width drawn at.
	v.off = 0
	v.find.seek = true
	v.render(7, 3)
	row := v.rowOf(v.findMatches()[0])
	if row < v.off || row >= v.off+3 {
		t.Errorf("at width 7 the match's row %d is not in view (off %d)", row, v.off)
	}

	// A match broken by the wrap is marked on both rows.
	w := newFindView([]string{"xxxxxxxNEEDLExxx"}, "needle")
	got := w.render(10, 3)
	if got[0] != "xxxxxxx{NEE}" || got[1] != "{DLE}xxx" {
		t.Errorf("split match: %q", got)
	}
}

func TestFindNextPrevWrapAround(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "filler"
	}
	lines[5], lines[25], lines[50] = "match a", "match b", "match c"
	v := newFindView(lines, "match")
	v.render(40, 10)
	if v.find.cur != 0 {
		t.Fatalf("cur %d", v.find.cur)
	}
	step := func(d int) {
		v.find.step += d
		v.find.seek = true
		v.render(40, 10)
	}
	step(1)
	if v.find.cur != 1 {
		t.Fatalf("n: cur %d", v.find.cur)
	}
	step(1)
	if v.find.cur != 2 || v.off > 50 || v.off+10 <= 50 {
		t.Fatalf("n to the last: cur %d off %d", v.find.cur, v.off)
	}
	step(1)
	if v.find.cur != 0 || v.off > 5 || v.off+10 <= 5 {
		t.Fatalf("n past the last wraps to the first: cur %d off %d", v.find.cur, v.off)
	}
	step(-1)
	if v.find.cur != 2 {
		t.Fatalf("N before the first wraps to the last: cur %d", v.find.cur)
	}
	// Two presses before a frame are both counted.
	v.find.step += 2
	v.render(40, 10)
	if v.find.cur != 1 {
		t.Fatalf("n n: cur %d", v.find.cur)
	}

	// While typing, the current match is the first at or below the top row.
	v.off = 30
	v.find.setQuery("matc")
	v.render(40, 10)
	if v.find.cur != 2 {
		t.Errorf("from row 30 the first match below is c: cur %d", v.find.cur)
	}
}

// Matches are kept until the lines or the query change; a view redrawn with
// the same lines every frame does not search again.
func TestFindIsMemoised(t *testing.T) {
	v := newFindView([]string{"a", "b a"}, "a")
	first := v.findMatches()
	ver := v.ver
	v.set([]string{"a", "b a"})
	if v.ver != ver {
		t.Fatal("the same lines must not count as a change")
	}
	if again := v.findMatches(); &again[0] != &first[0] {
		t.Fatal("matches were found again for the same lines and query")
	}
	v.set([]string{"a a a"})
	if n := len(v.findMatches()); n != 3 {
		t.Fatalf("new lines: %d matches", n)
	}
}

// ---- the desk ---------------------------------------------------------------------------

func searchSeed(s *fakeengine.Server) {
	s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1", Health: "healthy",
		Labels: map[string]string{"com.example.tier": "web"}, Logs: []string{"boot ok"}})
	s.AddContainer(fakeengine.ContainerSpec{Name: "other", Image: "app:1"})
	s.AddVolume("data", nil)
}

func (b *bench) inspectTab() {
	b.t.Helper()
	b.m.focus = PaneDetails
	for i := 0; i < 8 && b.m.tabName() != tabInspect; i++ {
		b.keys("]")
	}
	b.see("[Inspect]", `": "`)
}

func TestInspectFind(t *testing.T) {
	b := newBench(t, benchOpts{seed: searchSeed, width: 120, height: 40})
	b.m.focus = PaneList
	b.selectRow("app")
	b.inspectTab()
	closed := b.screen()

	b.keys("/")
	b.see("find: ")
	b.typeText("Status")
	b.see("find: Status", "1 of ")
	n := len(b.m.inspect.findMatches())
	if n < 2 {
		t.Fatalf("want several matches for Status, got %d", n)
	}
	b.see(fmt.Sprintf("1 of %d", n), "at State.Status")
	if b.m.inspect.off != 0 {
		t.Fatal("typing must not scroll")
	}
	// Enter keeps the query, closes the input and jumps; n steps on.
	b.keys("enter")
	if b.m.ds.typing {
		t.Fatal("Enter must close the input")
	}
	b.see("find: Status", fmt.Sprintf("1 of %d", n))
	b.typeText("n")
	b.see(fmt.Sprintf("2 of %d", n), "at State.Health.Status")
	// Its row is on screen.
	if !strings.Contains(b.screen(), `"»Status«": "healthy"`) {
		t.Fatalf("the current match is not in view\n%s", b.screen())
	}
	for i := 2; i < n; i++ {
		b.keys("n")
	}
	b.see(fmt.Sprintf("%d of %d", n, n))
	b.keys("n")
	b.see(fmt.Sprintf("1 of %d", n))
	b.keys("N")
	b.see(fmt.Sprintf("%d of %d", n, n))

	// A label key is named unambiguously.
	b.keys("/")
	for range "Status" {
		b.keys("backspace")
	}
	b.typeText("tier")
	b.keys("enter")
	b.see(`at Config.Labels["com.example.tier"]`)

	// No match is said with what was searched.
	b.keys("/")
	b.typeText("zzzz")
	b.see("no match in the ", " lines")
	b.keys("esc")
	b.notSee("find:")
	_ = closed
	b.assertFrameGeometry()
}

// The bar takes no row while closed: the pane draws exactly as before.
func TestSearchBarCostsNoRowWhenClosed(t *testing.T) {
	b := newBench(t, benchOpts{seed: searchSeed, width: 80, height: 24})
	b.m.focus = PaneList
	b.selectRow("app")
	b.inspectTab()
	before := b.screen()
	b.keys("/", "esc")
	if b.screen() != before {
		t.Fatalf("opening and closing the bar changed the pane\nbefore\n%s\nafter\n%s", before, b.screen())
	}
	b.keys("/")
	b.typeText("id")
	b.see("find: id")
	b.assertFrameGeometry()
	// Seven content rows at 80×24: six for the text, one for the bar.
	if b.m.detailHeight != 7 {
		t.Fatalf("the Details pane has %d content rows at 80×24", b.m.detailHeight)
	}
}

// Esc one step at a time: the query, then the pane — whether the bar is
// being typed in or the query was kept.
func TestSearchEscLadder(t *testing.T) {
	b := newBench(t, benchOpts{seed: searchSeed})
	b.m.focus = PaneList
	b.selectRow("app")
	b.inspectTab()

	b.keys("/")
	b.typeText("image")
	b.keys("esc")
	if b.m.ds.typing || b.m.tabQuery() != "" || b.m.focus != PaneDetails {
		t.Fatalf("Esc while typing: typing %v query %q focus %v", b.m.ds.typing, b.m.tabQuery(), b.m.focus)
	}
	b.keys("esc")
	if b.m.focus != PaneList {
		t.Fatal("the second Esc must return to the List")
	}

	b.keys("3", "/")
	b.typeText("image")
	b.keys("enter")
	if b.m.tabQuery() != "image" {
		t.Fatal("Enter must keep the query")
	}
	b.keys("esc")
	if b.m.tabQuery() != "" || b.m.focus != PaneDetails {
		t.Fatal("Esc with a kept query clears it and stays")
	}
	b.keys("esc")
	if b.m.focus != PaneList {
		t.Fatal("then Esc returns to the List")
	}
}

// Typing in the bar never reaches an action, a global key or another pane:
// every key is text, including each action key of the kind.
func TestSearchTypingNeverActs(t *testing.T) {
	for _, k := range []Kind{KindContainers, KindVolumes, KindNetworks, KindImages} {
		t.Run(kindNames[k], func(t *testing.T) {
			b := newBench(t, benchOpts{seed: searchSeed})
			b.kind(k)
			b.m.focus = PaneDetails
			tab, theme := b.m.tabName(), b.m.st.theme
			mut := len(b.srv.Mutations())
			b.keys("/")
			typed := ""
			for _, a := range kindActions[k] {
				typed += a.key
			}
			typed += "q?ST r12345[]hjklgGfwxXcaDnN/ "
			b.typeText(typed)
			b.keys("tab", "shift+tab", "up", "down", "pgdown", "right", "left")
			switch {
			case b.m.dlg != nil:
				t.Fatalf("a dialog opened while typing: %v", b.m.dlg.kind)
			case b.m.preparing:
				t.Fatal("a plan is being prepared")
			case b.quit || b.m.quitting:
				t.Fatal("q quit")
			case b.m.sp.open || b.m.pk.open:
				t.Fatal("a view opened")
			case b.m.focus != PaneDetails || b.m.tabName() != tab:
				t.Fatalf("focus %v tab %s", b.m.focus, b.m.tabName())
			case b.m.st.theme != theme:
				t.Fatal("T changed the theme")
			case !b.m.ds.typing:
				t.Fatal("the bar closed")
			}
			if got := b.m.ds.in.Value(); !strings.HasPrefix(got, typed[:20]) {
				t.Fatalf("the keys were not taken as text: %q", got)
			}
			b.keys("enter")
			b.settle()
			if len(b.srv.Mutations()) != mut {
				t.Fatal("a mutation reached the daemon")
			}
			if k == KindVolumes || k == KindNetworks {
				// With a query kept, n is the next match, not "new".
				b.keys("n")
				if b.m.dlg != nil {
					t.Fatal("n opened a form while a search is open")
				}
				b.keys("esc", "n")
				if b.m.dlg == nil || b.m.dlg.kind != dlgForm {
					t.Fatal("with the search cleared, n is new again")
				}
				b.keys("esc")
			}
		})
	}
}

// A different object clears every query. If the bar is being typed in it
// stays open, empty: the next key is still text.
func TestSearchClearedWithTheObject(t *testing.T) {
	b := newBench(t, benchOpts{seed: searchSeed})
	b.m.focus = PaneList
	b.selectRow("app")
	b.inspectTab()
	b.keys("/")
	b.typeText("running")
	b.keys("enter")
	b.keys("[") // to Processes: its own query, none yet
	if b.m.tabQuery() != "" {
		t.Fatal("queries are per tab")
	}
	b.keys("]")
	if b.m.tabQuery() != "running" {
		t.Fatal("Inspect lost its query")
	}
	b.keys("/")
	b.selectRow("other")
	if b.m.tabQuery() != "" || b.m.ds.in.Value() != "" {
		t.Fatal("the query survived a change of object")
	}
	if !b.m.ds.typing {
		t.Fatal("the bar must stay open: a key typed next must not act")
	}
	b.typeText("t")
	if b.m.dlg != nil || b.m.preparing {
		t.Fatal("t stopped a container")
	}
}

// ---- Logs: filter --------------------------------------------------------------------------

func (b *bench) logsTab(name string) {
	b.t.Helper()
	b.kind(KindContainers)
	b.m.focus = PaneList
	b.selectRow(name)
	b.m.focus = PaneDetails
	for i := 0; i < 8 && b.m.tabName() != tabLogs; i++ {
		b.keys("]")
	}
	b.see("[Logs]", "● following")
}

func TestLogsFilterAndFollow(t *testing.T) {
	b := newBench(t, benchOpts{seed: searchSeed})
	b.logsTab("app")
	for i := 0; i < 60; i++ {
		if i%10 == 3 {
			b.srv.WriteLog("app", 1, fmt.Sprintf("line %02d ERROR disk full", i))
		} else {
			b.srv.WriteLog("app", 1, fmt.Sprintf("line %02d ok", i))
		}
	}
	b.seeText("line 59 ok")

	b.keys("/")
	b.typeText("error")
	// Hidden as typed, and counted against the lines held.
	b.seeText("filter: error", "● following · 6 of 61 match", "line 53 ERROR")
	if strings.Contains(b.screen(), "line 59 ok") {
		t.Fatal("a line that does not match is still shown")
	}
	b.keys("enter")
	b.seeText("filter: error  ● following · 6 of 61 match")

	// Follow anchors to the newest matching line; a line that does not
	// match changes nothing but the count of lines held.
	b.srv.WriteLog("app", 1, "line 60 ok")
	b.seeText("6 of 62 match")
	b.srv.WriteLog("app", 2, "line 61 error again")
	b.seeText("7 of 63 match", "line 61 error again")
	if !strings.Contains(lastBodyRow(b), "line 61 error again") {
		t.Fatalf("the newest match is not at the bottom: %q\n%s", lastBodyRow(b), b.screen())
	}

	// Scrolling back moves through matching lines only.
	b.keys("up")
	b.seeText("‖ scrolled back — 1 newer matches")
	if !strings.Contains(lastBodyRow(b), "line 53 ERROR") {
		t.Fatalf("one up from the newest match is the one before it\n%s", b.screen())
	}
	b.srv.WriteLog("app", 1, "line 62 ERROR while held")
	b.seeText("2 newer matches")
	if strings.Contains(b.screen(), "line 62") {
		t.Fatal("the view moved while scrolled back")
	}
	b.keys("g")
	if !strings.Contains(lastBodyRow(b), "line 03 ERROR") {
		t.Fatalf("g goes to the oldest match\n%s", b.screen())
	}
	b.keys("down", "down", "down", "down", "down", "down", "down", "down")
	b.seeText("● following · 8 of 64 match", "line 62 ERROR while held")

	// Nothing matches: said with the number of lines that were looked at.
	b.keys("/")
	b.typeText("zzz")
	b.seeText("no match in the 64 lines held")
	if strings.Contains(b.screen(), "line ") {
		t.Fatal("lines are shown although none matches")
	}
	b.keys("esc")
	b.seeText("● following · 64 lines", "line 62 ERROR")
	b.assertFrameGeometry()
}

// unmarked is the screen without the brackets that mark matches when there
// is no colour.
func unmarked(s string) string {
	return strings.NewReplacer("›", "", "‹", "", "»", "", "«", "").Replace(s)
}

// seeText waits for the screen to show subs once matches' marks are removed.
func (b *bench) seeText(subs ...string) {
	b.t.Helper()
	b.until("screen to show "+strings.Join(subs, " + "), func() bool {
		s := unmarked(b.screen())
		for _, sub := range subs {
			if !strings.Contains(s, sub) {
				return false
			}
		}
		return true
	})
}

// lastBodyRow is the Logs body's lowest line of text: the newest shown.
// (Fewer lines than the pane holds are drawn from its top, as ever.)
func lastBodyRow(b *bench) string {
	lines := strings.Split(unmarked(b.screen()), "\n")
	inner := func(l string) string {
		parts := strings.Split(l, "┃") // the focused Details pane's sides
		if len(parts) < 3 {
			return ""
		}
		return strings.TrimSpace(parts[1])
	}
	for i, l := range lines {
		if strings.Contains(l, "● following") || strings.Contains(l, "‖ scrolled back") {
			for j := i - 1; j > 0; j-- {
				if c := inner(lines[j]); c != "" {
					return c
				}
			}
		}
	}
	return ""
}

// Matching strips escape sequences even while colour is drawn: a word split
// by a colour change matches, a sequence's own bytes never do.
func TestMatchingStripsANSIWithColourOn(t *testing.T) {
	withColour(t, termenv.TrueColor)
	b := newBench(t, benchOpts{seed: searchSeed, theme: "dark"})
	if !b.m.st.color {
		t.Fatal("colour is off")
	}
	b.logsTab("app")
	b.srv.WriteLog("app", 1, "\x1b[31mERR\x1b[0mOR in \x1b[1mworker\x1b[0m")
	b.srv.WriteLog("app", 1, "plain line")
	b.see("ERROR in worker")
	b.keys("/")
	b.typeText("error in w")
	b.see("1 of 3 match")
	if !strings.Contains(b.m.View(), "\x1b[4") && !strings.Contains(b.m.View(), ";4") {
		t.Error("the match is not underlined in the frame")
	}
	b.keys("backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	b.typeText("31m")
	b.see("no match in the 3 lines held")
	b.keys("esc")

	// The same on a find tab: the Info tab's labels are coloured.
	b.m.focus = PaneDetails
	for b.m.tabName() != tabInfo {
		b.keys("[")
	}
	b.keys("/")
	b.typeText("[2;")
	b.see("no match in the ")
	b.keys("esc", "/")
	b.typeText("image")
	b.see("1 of ")
	raw := b.m.View()
	if !strings.Contains(raw, "\x1b[") || !strings.Contains(raw, "Image") {
		t.Fatal("expected a coloured frame")
	}
}

// ---- styles ---------------------------------------------------------------------------------

// The match is bold and underlined in every theme — meaning that survives
// without colour — in a colour that passes the contrast check where there
// is one; the current match is reversed too.
func TestMatchStyleInEveryTheme(t *testing.T) {
	withColour(t, termenv.TrueColor)
	for _, th := range themes {
		st := newStyles(th)
		for name, s := range map[string]interface {
			GetBold() bool
			GetUnderline() bool
		}{"match": st.match, "current match": st.matchCur} {
			if !s.GetBold() || !s.GetUnderline() {
				t.Errorf("%s: the %s is not bold and underlined", th.Name, name)
			}
		}
		if !st.matchCur.GetReverse() {
			t.Errorf("%s: the current match is not reversed", th.Name)
		}
		if st.match.GetForeground() != th.Accent && th.Accent != nil {
			t.Errorf("%s: the match is not in the accent colour", th.Name)
		}
		if st.mark.None() || !strings.Contains(st.mark.On, "4") {
			t.Errorf("%s: no underline is written around a match: %q", th.Name, st.mark.On)
		}
	}
	// Without colour no attribute is drawn at all (T5): matches are bracketed.
	withColour(t, termenv.Ascii)
	st := newStyles(themes[0])
	if st.mark != (search.Marks{On: "›", Off: "‹", CurOn: "»", CurOff: "«"}) {
		t.Errorf("no-colour marks: %+v", st.mark)
	}
}

// ---- nothing is sent ------------------------------------------------------------------------

// A search sends nothing — in either mode — and behaves identically in both.
func TestSearchSendsNothing(t *testing.T) {
	var screens [2]string
	for i, ro := range []bool{false, true} {
		b := newBench(t, benchOpts{seed: searchSeed, readOnly: ro})
		b.kind(KindVolumes)
		b.m.focus = PaneList
		b.selectRow("data")
		b.inspectTab()
		b.settle()
		before := len(b.srv.Requests())
		b.keys("/")
		b.typeText("name")
		b.keys("enter", "n", "N", "n")
		screens[i] = b.screen()
		b.keys("esc")
		b.settle()
		if got := b.srv.Requests()[before:]; len(got) != 0 {
			t.Errorf("read-only %v: the search sent %v", ro, got)
		}
	}
	strip := func(s string) string {
		s = strings.ReplaceAll(s, "[READ-ONLY]", "")
		return strings.Join(strings.Fields(s), " ")
	}
	a, c := strings.Split(screens[0], "\n"), strings.Split(screens[1], "\n")
	// Compare the Details pane rows: the header and hints differ by design.
	for r := 1; r < len(a)-2 && r < len(c)-2; r++ {
		if strings.Contains(a[r], "find:") && strip(a[r]) != strip(c[r]) {
			t.Errorf("the bar differs under --read-only:\n%q\n%q", a[r], c[r])
		}
	}
}
