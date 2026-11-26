package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/session"
)

// ---- helpers ---------------------------------------------------------------------------

const (
	attnListPath    = `^/containers/json$`
	attnInspectPath = `^/containers/[^/]+/json$`
)

// mixed seeds one container of every class, named <p>-<what>.
func mixed(p string) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-ok", Image: "app:1", Health: "healthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-nocheck", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-starting", Image: "app:1", Health: "starting"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-failed", Image: "app:1", State: "exited", ExitCode: 3, Age: 2 * time.Hour,
			Labels: map[string]string{"com.docker.compose.project": "shop"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-oom", Image: "app:1", State: "exited", ExitCode: 137, OOMKilled: true, Age: 2 * time.Minute})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-done", Image: "app:1", State: "exited"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-sick", Image: "app:1", Health: "unhealthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-loop", Image: "app:1", State: "restarting", RestartCount: 12, ExitCode: 1})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-dead", Image: "app:1", State: "dead"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-flap", Image: "app:1", RestartCount: 5, Health: "healthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-new", Image: "app:1", State: "created"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-paused", Image: "app:1", State: "paused"})
	}
}

// calm seeds containers none of which needs anyone.
func calm(p string) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-ok", Image: "app:1", Health: "healthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-nocheck", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-done", Image: "app:1", State: "exited"})
		s.AddContainer(fakeengine.ContainerSpec{Name: p + "-paused", Image: "app:1", State: "paused"})
	}
}

// attnSettled reports that every connected block is read and nothing is on
// its way.
func (b *bench) attnSettled() bool {
	at := &b.m.at
	if !at.open || len(at.blocks) == 0 {
		return false
	}
	for _, blk := range at.blocks {
		if blk.State != session.Connected {
			continue
		}
		if !blk.Loaded || blk.Reading || blk.Block.Counts.Reading > 0 {
			return false
		}
	}
	return true
}

// openAttention presses H and waits for every connected daemon's answer.
func (b *bench) openAttention() {
	b.t.Helper()
	b.keys(keyAttention)
	if !b.m.at.open {
		b.t.Fatalf("H did not open the view:\n%s", b.screen())
	}
	b.until("the attention view to settle", b.attnSettled)
}

func (b *bench) attnBlock(name string) session.AttentionBlock {
	b.t.Helper()
	for _, blk := range b.m.at.blocks {
		if blk.Member.Name() == name {
			return blk
		}
	}
	b.t.Fatalf("no block for %s", name)
	return session.AttentionBlock{}
}

// attnCursorName is the name of the container under the cursor.
func (b *bench) attnCursorName() string {
	rows := b.m.attnRows()
	i := b.m.attnCursor(rows)
	if i < 0 {
		return ""
	}
	return rows[i].name
}

// attnTo moves the cursor to the named row with the keyboard.
func (b *bench) attnTo(name string) {
	b.t.Helper()
	b.keys("g")
	for i := 0; i < 200 && b.attnCursorName() != name; i++ {
		b.keys("down")
	}
	if b.attnCursorName() != name {
		b.t.Fatalf("no row %s:\n%s", name, b.screen())
	}
}

func lineWith(screen, sub string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func noMutations(t *testing.T, srvs ...*fakeengine.Server) {
	t.Helper()
	for _, s := range srvs {
		if ms := s.Mutations(); len(ms) != 0 {
			t.Fatalf("%s received %d mutating request(s): %v", s.Name, len(ms), ms)
		}
	}
}

// ---- 3.1 open, close, restore ---------------------------------------------------------

func TestAttentionOpensAndClosesWhereItWas(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	gamma := b.daemon("gamma", calm("gamma"))
	b.add("beta", beta)
	b.add("gamma", gamma)
	b.srv.ResetRequests()
	beta.ResetRequests()
	gamma.ResetRequests()

	// From several places on the desk: each is put back exactly.
	for _, from := range []struct {
		pane Pane
		kind Kind
		row  string
	}{
		{PaneList, KindContainers, "alpha-sick"},
		{PaneDetails, KindContainers, "alpha-paused"},
		{PaneEvents, KindImages, "app:1"},
		{PaneKinds, KindNetworks, "bridge"},
	} {
		for _, closeKey := range []string{"esc", keyAttention} {
			b.kind(from.kind)
			b.selectRow(from.row)
			b.m.focus = from.pane
			key := b.m.sel[from.kind].key
			before := b.state()

			b.openAttention()
			s := b.screen()
			if !strings.Contains(s, "Attention — ") || !strings.Contains(s, "need you") {
				t.Fatalf("the view's title:\n%s", s)
			}
			// One block per daemon, the focused one first.
			ia, ib, ig := strings.Index(s, "alpha-loop"), strings.Index(s, "beta-loop"), strings.Index(s, "gamma")
			if ia < 0 || ib < ia {
				t.Fatalf("blocks are not focused first:\n%s", s)
			}
			_ = ig
			if got := []string{b.m.at.blocks[0].Member.Name(), b.m.at.blocks[1].Member.Name(), b.m.at.blocks[2].Member.Name()}; got[0] != b.m.cur.Name() || got[1] != "beta" || got[2] != "gamma" {
				t.Fatalf("block order %v", got)
			}
			b.assertFrameGeometry()

			b.keys(closeKey)
			if b.m.at.open {
				t.Fatalf("%q did not close the view", closeKey)
			}
			if after := b.state(); after != before {
				t.Fatalf("after %q from %s/%s: %+v, was %+v", closeKey, paneNames[from.pane], kindNames[from.kind], after, before)
			}
			if b.m.sel[from.kind].key != key {
				t.Fatalf("the highlight moved: %q, was %q", b.m.sel[from.kind].key, key)
			}
		}
	}
	// Three daemons were read, and nothing was changed on any of them.
	for _, srv := range []*fakeengine.Server{beta, gamma} {
		if srv.CountRequests("GET", attnListPath) == 0 {
			t.Fatalf("%s was never read", srv.Name)
		}
	}
	noMutations(t, b.srv, beta, gamma)
}

func TestAttentionTitleTotalsHintsAndHelp(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	b.openAttention()
	s := b.screen()
	for _, want := range []string{
		"Attention — 10 need you · 4 to watch · 24 containers on 2 daemons",
		"read ", // the read time, in the title and in each block
		"12 containers · 5 need you · 2 to watch · read ",
		"(the desk's list)",
		"● connected", "Engine " + fakeengine.EngineVersion, b.srv.Host(), beta.Host(), "ctx beta", "focused",
		// Rows, with what was read about each.
		"alpha-loop", "restarting · exit 1 · 12 restarts",
		"alpha-sick", "running · unhealthy",
		"alpha-dead", "dead",
		"alpha-oom", "exited 137 · OOM-killed · finished ",
		"alpha-failed", "exited 3 · finished ",
		"alpha-flap", "running · healthy · 5 restarts",
		"alpha-new", "created, never started",
		// The classes that are only counted.
		"1 fine · 2 unknown (no health verdict) · 2 not a problem (paused, exited 0)",
		// Hints.
		"go to container", "Tab next daemon", "r read again", keyAttention + " Esc close", "? keys",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the view lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("\n%s", s)
	}
	// Needs-you rows in the ladder's order, then watch; fine and unknown
	// containers are not listed at all.
	order := []string{"alpha-loop", "alpha-sick", "alpha-dead", "alpha-oom", "alpha-failed", "alpha-flap", "alpha-new"}
	at := -1
	for _, name := range order {
		i := strings.Index(s, name)
		if i < at {
			t.Fatalf("%s is out of order:\n%s", name, s)
		}
		at = i
	}
	for _, hidden := range []string{"alpha-ok", "alpha-nocheck", "alpha-starting", "alpha-done", "alpha-paused"} {
		if strings.Contains(s, hidden) {
			t.Fatalf("%s is listed:\n%s", hidden, s)
		}
	}
	// Nothing of another kind of object.
	for _, other := range []string{"Images", "Volumes", "Reclaimable", "compose file", "registry"} {
		if strings.Contains(lineWithout(s, 2), other) {
			t.Fatalf("the view shows %q:\n%s", other, s)
		}
	}
	b.dump("attention, two daemons")

	// ? opens the help over the view, and the view is still there after.
	b.keys("?")
	if b.m.dlg == nil || b.m.dlg.kind != dlgHelp {
		t.Fatal("? did not open the help")
	}
	b.keys("esc")
	if !b.m.at.open {
		t.Fatal("closing the help closed the view")
	}
}

// lineWithout drops the first n lines (the header and the strip, which are
// the desk's, not the view's).
func lineWithout(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n+2 {
		return ""
	}
	return strings.Join(lines[n:len(lines)-2], "\n")
}

// ---- 2.2 the focused block is the list already held -----------------------------------

func TestAttentionFocusedBlockIsInstant(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	b.srv.ResetRequests()
	beta.ResetRequests()
	beta.Stall("GET", attnListPath, 400*time.Millisecond)

	// The very frame of the key: no message has been processed since.
	b.step(keyMsg(keyAttention))
	s := b.screen()
	if !strings.Contains(s, "alpha-loop") || !strings.Contains(s, "alpha-sick") || !strings.Contains(s, "12 containers") || !strings.Contains(s, "(the desk's list)") {
		t.Fatalf("the focused block is not filled at once:\n%s", s)
	}
	if strings.Contains(s, "beta-loop") || !strings.Contains(s, "reading its containers") {
		t.Fatalf("beta answered already, or is not shown as being read:\n%s", s)
	}
	// What needs an inspect is being read: not yet a failure, not yet fine.
	blk := b.attnBlock(b.m.cur.Name())
	if c := blk.Block.Counts; c.Fine != 0 || c.NotAProblem != 1 || c.Reading == 0 {
		t.Fatalf("counts before the details: %+v", c)
	}
	if !strings.Contains(s, "exited · reading its exit code…") || !strings.Contains(s, "still being read") {
		t.Fatalf("an unread exit is not shown as being read:\n%s", s)
	}
	if strings.Contains(s, "exited 3") {
		t.Fatalf("an exit code is shown before it was read:\n%s", s)
	}

	// The focused daemon's details arrive, and its rows settle, while beta
	// has still not answered.
	b.until("alpha's details", func() bool {
		blk := b.attnBlock(b.m.cur.Name())
		return !blk.Reading && blk.Block.Counts.Reading == 0
	})
	if b.attnBlock("beta").Loaded {
		t.Fatal("beta answered before alpha settled: the stall did not hold")
	}
	b.see("exited 3 · finished", "alpha-oom")
	// No list request was made for the focused daemon: the desk's was used.
	if n := b.srv.CountRequests("GET", attnListPath); n != 0 {
		t.Fatalf("%d list requests to the focused daemon", n)
	}
	b.until("beta", b.attnSettled)
	b.see("beta-loop")
	if n := beta.CountRequests("GET", attnListPath); n != 1 {
		t.Fatalf("%d list requests to beta", n)
	}
}

// With the focused daemon's list not loaded, the view reads it itself.
func TestAttentionReadsTheFocusedDaemonWhenItsListIsNotHeld(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	b.m.loaded[listContainers] = false
	b.srv.ResetRequests()
	b.keys(keyAttention)
	if s := b.screen(); strings.Contains(s, "alpha-loop") {
		t.Fatalf("rows from a list that is not held:\n%s", s)
	}
	b.until("the view's own read", b.attnSettled)
	b.see("alpha-loop", "12 containers · 5 need you")
	if strings.Contains(b.screen(), "(the desk's list)") {
		t.Fatal("a list the view read itself is called the desk's")
	}
}

// ---- 3.2 the empty answer, one daemon, a lost member, long endpoints, a failed read --------

func TestAttentionEmptyAnswerIsSaidOnce(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newMultiBench(t, benchOpts{seed: calm("alpha"), width: size[0], height: size[1]})
		beta := b.daemon("beta", calm("beta"))
		b.add("beta", beta)
		b.openAttention()
		s := b.flat()
		if n := strings.Count(s, "Nothing needs attention"); n != 1 {
			t.Fatalf("said %d times:\n%s", n, s)
		}
		for _, want := range []string{
			"✓ Nothing needs attention on 2 daemons: 8 containers read.",
			"Attention — nothing needs you · 8 containers on 2 daemons",
			"4 containers · 0 need you · 0 to watch · read ",
			"1 fine · 1 unknown", "2 not a problem",
		} {
			if !strings.Contains(s, want) {
				t.Fatalf("%dx%d lacks %q:\n%s", size[0], size[1], want, s)
			}
		}
		// Each daemon's totals and read time, and no table, no spinner.
		if n := strings.Count(s, "4 containers · 0 need you"); n != 2 {
			t.Fatalf("%d blocks with totals:\n%s", n, s)
		}
		if strings.Contains(s, "Nothing here needs you") || strings.Contains(s, "reading") {
			t.Fatalf("an empty answer with a per-daemon line or a spinner:\n%s", s)
		}
		b.assertFrameGeometry()
		b.dump("attention, nothing wrong")
	}
}

func TestAttentionSingleDaemon(t *testing.T) {
	b := newBench(t, benchOpts{seed: mixed("solo"), width: 80, height: 24})
	b.openAttention()
	s := b.flat()
	for _, want := range []string{"Attention — 5 need you · 2 to watch · 12 containers", "solo-loop", "solo-sick", "(the desk's list)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lacks %q:\n%s", want, s)
		}
	}
	// One daemon: nothing about "daemons", and no key for the next one.
	if strings.Contains(s, "daemons") && !strings.Contains(lineWith(s, "daemons"), "Daemons") {
		t.Fatalf("a single daemon is counted as several:\n%s", s)
	}
	if strings.Contains(s, "Tab next daemon") {
		t.Fatalf("Tab is offered with one daemon:\n%s", s)
	}
	b.assertFrameGeometry()
	// A clear block next to nothing else is said once, for the session.
	b2 := newBench(t, benchOpts{seed: calm("solo"), width: 80, height: 24})
	b2.openAttention()
	if s := b2.flat(); !strings.Contains(s, "✓ Nothing needs attention on 1 daemon: 4 containers read.") {
		t.Fatalf("\n%s", s)
	}
}

func TestAttentionDisconnectedMemberHasAReasonAndNoRows(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newMultiBench(t, benchOpts{seed: mixed("alpha"), width: size[0], height: size[1]})
		beta := b.daemon("beta", mixed("beta"))
		mem := b.add("beta", beta)
		b.openAttention()
		b.see("alpha-loop")
		if size[1] > 30 {
			b.see("beta-loop")
		}
		beta.Stop()
		b.until("beta's block to say it is disconnected", func() bool {
			return mem.State() == session.Disconnected && b.attnBlock("beta").State == session.Disconnected && b.m.attnTotals().disconnected == 1
		})
		b.keys("end") // the last block, wherever it scrolled to
		s := b.flat()
		for _, want := range []string{"✗ disconnected", beta.Host(), "Not read:", "No container of this daemon is shown."} {
			if !strings.Contains(s, want) {
				t.Fatalf("%dx%d lacks %q:\n%s", size[0], size[1], want, s)
			}
		}
		if strings.Contains(s, "beta-loop") || strings.Contains(s, "beta-sick") {
			t.Fatalf("rows of a disconnected daemon:\n%s", s)
		}
		// The other block is whole and the view still works.
		b.keys("home")
		b.see("alpha-loop", "12 containers · 5 need you")
		if !strings.Contains(b.screen(), "on 1 of 2 daemons") {
			t.Fatalf("the title still counts both:\n%s", b.screen())
		}
		b.assertFrameGeometry()

		// It comes back: read again, unasked.
		if err := beta.Restart(); err != nil {
			t.Fatal(err)
		}
		b.until("beta read again", func() bool {
			blk := b.attnBlock("beta")
			return blk.State == session.Connected && blk.Loaded && !blk.Reading && blk.Block.Counts.NeedsYou == 5
		})
	}
}

func TestAttentionLongEndpointFits(t *testing.T) {
	long := "ssh://deploy-operator@a-very-long-bastion-host-name.prod.eu-central-1.internal.example.com:2222/var/run/docker/tenant-0042/docker.sock"
	for _, size := range [][2]int{{80, 24}, {100, 30}, {200, 50}} {
		b := newBench(t, benchOpts{seed: mixed("alpha"), width: size[0], height: size[1],
			endpoint: &dockerctx.Endpoint{Host: long, Context: "a-context-with-quite-a-long-name-for-production-eu", Source: "test"}})
		b.openAttention()
		b.assertFrameGeometry()
		s := b.screen()
		// Its beginning and its end are both there; the rows are too.
		if !strings.Contains(s, "ssh://deploy-operator@") || !strings.Contains(s, "docker.sock") {
			t.Fatalf("%dx%d: the endpoint lost an end:\n%s", size[0], size[1], s)
		}
		if size[0] >= 200 && !strings.Contains(s, long) {
			t.Fatalf("a wide terminal does not show the endpoint in full:\n%s", s)
		}
		if !strings.Contains(s, "alpha-loop") || !strings.Contains(s, "12 containers · 5 need you") {
			t.Fatalf("%dx%d: the block lost its rows:\n%s", size[0], size[1], s)
		}
	}
}

func TestAttentionFailedReadKeepsTheRest(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newMultiBench(t, benchOpts{seed: mixed("alpha"), width: size[0], height: size[1]})
		beta := b.daemon("beta", mixed("beta"))
		b.add("beta", beta)
		beta.FailNext("GET", attnListPath, 500, "the storage driver is on fire", -1)
		b.keys(keyAttention)
		b.until("beta's failure", func() bool { blk := b.attnBlock("beta"); return blk.Err != nil && !blk.Reading })
		b.until("alpha", func() bool { blk := b.attnBlock(b.m.cur.Name()); return !blk.Reading && blk.Block.Counts.Reading == 0 })
		b.keys("end")
		s := b.flat()
		for _, want := range []string{"▲ The read at ", "failed: the storage driver is on fire", "r reads again", "Nothing of this daemon is shown."} {
			if !strings.Contains(s, want) {
				t.Fatalf("%dx%d lacks %q:\n%s", size[0], size[1], want, s)
			}
		}
		b.keys("home")
		b.see("alpha-loop", "12 containers · 5 need you")
		b.assertFrameGeometry()
		// Not "nothing needs attention": a daemon that was not read is not clear.
		if strings.Contains(b.screen(), "Nothing needs attention") {
			t.Fatal("a failed read is called clear")
		}

		// r, once the daemon answers: the block is whole again.
		beta.ClearFailures()
		b.keys("r")
		b.until("beta read", func() bool { blk := b.attnBlock("beta"); return blk.Loaded && blk.Err == nil && !blk.Reading })
		read := b.attnBlock("beta").ReadAt

		// A later failure keeps the rows, with the time they were read.
		beta.FailNext("GET", attnListPath, 500, "again", -1)
		b.keys("r")
		b.until("beta's second failure", func() bool { blk := b.attnBlock("beta"); return blk.Err != nil && !blk.Reading })
		blk := b.attnBlock("beta")
		if !blk.Loaded || blk.Block.Counts.NeedsYou != 5 || !blk.ReadAt.Equal(read) {
			t.Fatalf("after a failed re-read: %+v", blk)
		}
		b.keys("end")
		s = b.flat()
		if !strings.Contains(s, "The rows below are those of "+read.Local().Format("15:04:05")) || !strings.Contains(s, "beta-failed") {
			t.Fatalf("the old rows are not kept and dated:\n%s", s)
		}
		b.assertFrameGeometry()
	}
}

// ---- 2.3 the bound, in the view --------------------------------------------------------

func TestAttentionSaysWhatWasNotInspected(t *testing.T) {
	seed := func(s *fakeengine.Server) {
		for i := 0; i < 7; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("failed%d", i), Image: "app:1", State: "exited", ExitCode: 2})
		}
		for i := 0; i < 5; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("up%d", i), Image: "app:1", Health: "healthy"})
		}
	}
	b := newBench(t, benchOpts{seed: seed, attention: session.AttentionConfig{MaxInspects: 4}})
	b.srv.ResetRequests()
	b.openAttention()
	s := b.flat()
	for _, want := range []string{"12 containers · 4 need you", "8 not inspected (the view stops at 4 inspects a daemon)", "0 fine"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lacks %q:\n%s", want, s)
		}
	}
	if n := b.srv.CountRequests("GET", attnInspectPath); n != 4 {
		t.Fatalf("%d inspects for a bound of 4", n)
	}
	// Not everything was read: the session is not called clear, and nothing
	// uninspected is called fine.
	if strings.Contains(s, "Nothing needs attention") || strings.Contains(s, "5 fine") {
		t.Fatalf("uninspected containers were given a class:\n%s", s)
	}
}

// A daemon with more problems than the view lists: the rest are counted, in
// a line that says so, and the screen is still drawn.
func TestAttentionListsABoundedNumberOfRows(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		for i := 0; i < attnRowsMax+37; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("loop%04d", i), Image: "app:1", State: "restarting"})
		}
	}, attention: session.AttentionConfig{MaxInspects: 5}})
	b.openAttention()
	if n := len(b.m.attnRows()); n != attnRowsMax {
		t.Fatalf("%d rows listed", n)
	}
	b.see(fmt.Sprintf("%d need you", attnRowsMax+37))
	b.keys("G")
	for i := 0; i < 4; i++ {
		b.keys("down")
	}
	if s := b.flat(); !strings.Contains(s, "… and 37 more that need you or are watched") {
		t.Fatalf("the rows left out are not counted:\n%s", b.screen())
	}
	b.assertFrameGeometry()
}

// ---- 2.4 events, and the quiet view ----------------------------------------------------

func TestAttentionFollowsABackgroundDaemonsEvents(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	b.openAttention()
	first := b.attnBlock("beta").ReadAt
	b.srv.ResetRequests()
	beta.ResetRequests()

	// Nothing happens: nothing is asked of the background daemon, and the
	// view asks the focused one for nothing either.
	b.run(500 * time.Millisecond)
	if n := beta.CountRequests("GET", `^/containers/`); n != 0 {
		t.Fatalf("%d container requests to beta while nothing happened", n)
	}
	if n := b.srv.CountRequests("GET", attnInspectPath); n != 0 {
		t.Fatalf("%d inspects of the focused daemon while nothing happened", n)
	}
	if !b.attnBlock("beta").ReadAt.Equal(first) {
		t.Fatal("beta's read time moved by itself")
	}

	// A container exits on the background daemon: its block is read again.
	beta.Crash("beta-ok", 7)
	b.until("beta re-read", func() bool {
		blk := b.attnBlock("beta")
		return blk.ReadAt.After(first) && !blk.Reading && blk.Block.Counts.NeedsYou == 6 && blk.Block.Counts.Reading == 0
	})
	b.see("beta-ok", "exited 7")
	if n := beta.CountRequests("GET", attnListPath); n != 1 {
		t.Fatalf("%d list requests for one exit", n)
	}
	// And one that becomes unhealthy.
	second := b.attnBlock("beta").ReadAt
	beta.SetHealth("beta-flap", "unhealthy")
	b.until("beta re-read on a health change", func() bool {
		blk := b.attnBlock("beta")
		return blk.ReadAt.After(second) && !blk.Reading && blk.Block.Counts.NeedsYou == 7
	})

	// Closing ends it: an exit afterwards is a notice, not a read.
	b.keys("esc")
	beta.ResetRequests()
	beta.Crash("beta-nocheck", 1)
	b.see("beta: container beta-nocheck exited (code 1)")
	b.run(200 * time.Millisecond)
	if n := beta.CountRequests("GET", `^/containers/`); n != 0 {
		t.Fatalf("%d container requests to beta after the view closed", n)
	}
	noMutations(t, b.srv, beta)
}

// ---- 2.5 a member leaves, or the focus changes, under the open view ------------------------

func TestAttentionMemberRemovedWhileOpen(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	mem := b.add("beta", beta)
	beta.Stall("GET", attnListPath, 300*time.Millisecond)
	b.keys(keyAttention)
	b.until("beta's read under way", func() bool { return b.attnBlock("beta").Reading })
	if _, err := b.m.sess.Remove(mem.ID()); err != nil {
		t.Fatal(err)
	}
	b.until("beta's block gone", func() bool { return len(b.m.at.blocks) == 1 })
	b.see("beta left the session", "alpha-loop")
	if strings.Contains(lineWithout(b.screen(), 2), "beta") {
		t.Fatalf("beta is still in the view:\n%s", b.screen())
	}
	b.until("alpha settled", b.attnSettled)
	b.run(350 * time.Millisecond)
	if n := beta.CountRequests("GET", attnInspectPath); n != 0 {
		t.Fatalf("%d inspects were sent to a removed daemon", n)
	}
	b.assertFrameGeometry()
}

// The focused daemon is removed under the open view: focus moves, and the
// view stays, with the newly focused daemon first.
func TestAttentionSurvivesAFocusChange(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	alpha := b.m.cur
	b.openAttention()
	b.attnTo("beta-sick")
	if _, err := b.m.sess.Remove(alpha.ID()); err != nil {
		t.Fatal(err)
	}
	b.until("focus on beta", func() bool { return b.m.cur != nil && b.m.cur.Name() == "beta" })
	if !b.m.at.open {
		t.Fatal("the view closed with the focus change")
	}
	b.until("one block", func() bool { return len(b.m.at.blocks) == 1 && b.attnSettled() })
	if got := b.attnCursorName(); got != "beta-sick" {
		t.Fatalf("the cursor is on %q", got)
	}
	b.see("beta-loop")
	// Closing restores what can be: the pane and the kind.
	b.keys("esc")
	if b.m.at.open || b.m.focus != PaneList || b.m.kind != KindContainers {
		t.Fatal("closing after a focus change")
	}
}

// ---- 3.3 navigation and scrolling ------------------------------------------------------

func many(p string, failed, fine int) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		for i := 0; i < failed; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("%s-failed%02d", p, i), Image: "app:1", State: "exited", ExitCode: 1,
				Age: time.Duration(i+1) * time.Hour})
		}
		for i := 0; i < fine; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("%s-fine%02d", p, i), Image: "app:1", Health: "healthy"})
		}
	}
}

var attnPos = regexp.MustCompile(`lines (\d+)–(\d+) of (\d+)`)

func TestAttentionNavigatesAndScrolls(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {150, 30}} {
		b := newMultiBench(t, benchOpts{seed: many("alpha", 25, 3), width: size[0], height: size[1]})
		beta := b.daemon("beta", many("beta", 25, 3))
		b.add("beta", beta)
		b.openAttention()
		b.assertFrameGeometry()
		pos := func() string { return attnPos.FindString(b.screen()) }
		start := pos()
		if !strings.HasPrefix(start, "lines 1–") {
			t.Fatalf("does not open at the top: %q", start)
		}
		// Newest failure first.
		if got := b.attnCursorName(); got != "alpha-failed00" {
			t.Fatalf("cursor starts on %q", got)
		}

		// Down, a row at a time: the cursor is always on screen.
		for i := 1; i < 25; i++ {
			b.keys("down")
			want := fmt.Sprintf("alpha-failed%02d", i)
			if got := b.attnCursorName(); got != want {
				t.Fatalf("after %d downs: on %q, want %q", i, got, want)
			}
			if l := lineWith(b.screen(), "▸"); !strings.Contains(l, want) {
				t.Fatalf("the cursor row %s is not on screen:\n%s", want, b.screen())
			}
		}
		if pos() == start {
			t.Fatalf("%dx%d: the list did not scroll", size[0], size[1])
		}
		// Across the block boundary.
		b.keys("down")
		if got := b.attnCursorName(); got != "beta-failed00" {
			t.Fatalf("crossing into the next block: on %q", got)
		}
		s := b.screen()
		if !strings.Contains(lineWith(s, "▸"), "beta-failed00") || !strings.Contains(s, beta.Host()) {
			t.Fatalf("the next block's heading and first row are not both on screen:\n%s", s)
		}
		// Paging, and the ends.
		b.keys("pgdown")
		if got := b.attnCursorName(); got == "beta-failed00" {
			t.Fatal("PgDn did not move")
		}
		b.keys("pgup", "pgup", "pgup", "pgup", "pgup", "pgup")
		b.keys("G")
		if got := b.attnCursorName(); got != "beta-failed24" {
			t.Fatalf("G: on %q", got)
		}
		// Past the last row, down scrolls: the counts under it can be read.
		for i := 0; i < 6; i++ {
			b.keys("down")
		}
		b.see("3 fine · 0 unknown")
		m := attnPos.FindStringSubmatch(b.screen())
		if m == nil || m[2] != m[3] {
			t.Fatalf("the end is not reachable: %v\n%s", m, b.screen())
		}
		b.keys("g")
		if got := b.attnCursorName(); got != "alpha-failed00" || pos() != start {
			t.Fatalf("g: on %q at %q", got, pos())
		}
		// Up at the first row stays there.
		b.keys("up", "up")
		if got := b.attnCursorName(); got != "alpha-failed00" {
			t.Fatalf("up at the top: on %q", got)
		}

		// Tab: the next daemon's block to the top, its first row under the
		// cursor; Shift+Tab back.
		b.keys("tab")
		s = b.screen()
		if got := b.attnCursorName(); got != "beta-failed00" {
			t.Fatalf("Tab: on %q", got)
		}
		body := strings.Split(s, "\n")
		if !strings.Contains(body[3], "beta") {
			t.Fatalf("Tab did not bring beta's heading to the top:\n%s", s)
		}
		b.keys("tab") // no further block: stays
		if got := b.attnCursorName(); got != "beta-failed00" {
			t.Fatalf("Tab at the last block: on %q", got)
		}
		b.keys("shift+tab")
		if got := b.attnCursorName(); got != "alpha-failed00" {
			t.Fatalf("Shift+Tab: on %q", got)
		}
		b.assertFrameGeometry()
		noMutations(t, b.srv, beta)
	}
}

// The cursor stays on its container across a re-read, and on its place when
// the container goes.
func TestAttentionCursorSurvivesAReRead(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha"), width: 80, height: 24})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	b.openAttention()
	b.attnTo("beta-dead")
	off := b.m.at.off

	// A new failure on the same daemon: listed above it; the cursor stays on
	// its container.
	read := b.attnBlock("beta").ReadAt
	beta.Crash("beta-ok", 5)
	b.until("beta re-read", func() bool {
		blk := b.attnBlock("beta")
		return blk.ReadAt.After(read) && !blk.Reading && blk.Block.Counts.NeedsYou == 6 && blk.Block.Counts.Reading == 0
	})
	if got := b.attnCursorName(); got != "beta-dead" {
		t.Fatalf("after a re-read the cursor is on %q", got)
	}
	if !strings.Contains(lineWith(b.screen(), "▸"), "beta-dead") {
		t.Fatalf("the cursor row left the screen (offset %d → %d):\n%s", off, b.m.at.off, b.screen())
	}
	// r, too.
	b.keys("r")
	b.until("r", func() bool { return b.attnBlock("beta").ReadAt.After(read) && b.attnSettled() })
	if got := b.attnCursorName(); got != "beta-dead" {
		t.Fatalf("after r the cursor is on %q", got)
	}

	// Its container goes: the cursor is on the row now at that place.
	rows := b.m.attnRows()
	at := b.m.attnCursor(rows)
	next := rows[at+1].name
	read = b.attnBlock("beta").ReadAt
	beta.RemoveContainer("beta-dead")
	b.keys("r")
	b.until("the read without it", func() bool {
		blk := b.attnBlock("beta")
		return blk.ReadAt.After(read) && b.attnSettled() && blk.Block.Counts.Total == 11
	})
	if got := b.attnCursorName(); got != next {
		t.Fatalf("after its row went the cursor is on %q, want %q", got, next)
	}
}

// ---- 3.4 Enter: the jump ---------------------------------------------------------------

func TestAttentionJumpToAnotherDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	mem := b.add("beta", beta)
	b.kind(KindImages)
	b.m.focus = PaneDetails
	b.openAttention()
	b.attnTo("beta-oom")
	b.srv.ResetRequests()
	beta.ResetRequests()
	b.keys("enter")
	if b.m.at.open {
		t.Fatal("Enter did not close the view")
	}
	if b.m.cur != mem {
		t.Fatalf("focus is on %s", b.m.cur.Name())
	}
	b.until("the container selected on beta", func() bool {
		r, ok := b.m.currentRow()
		return b.m.at.jump == nil && b.m.kind == KindContainers && b.m.focus == PaneList && ok && r.name == "beta-oom"
	})
	s := b.screen()
	if !strings.Contains(s, beta.Host()) || !strings.Contains(lineWith(s, "▸ beta-oom"), "beta-oom") || strings.Contains(s, "alpha-") {
		t.Fatalf("beta's Containers list with the container selected:\n%s", s)
	}
	b.see("beta-oom on beta.")
	if r, _ := b.m.currentRow(); r.containerID != beta.ContainerID("beta-oom") {
		t.Fatal("the wrong container is selected")
	}
	// The view's reads have ended: nothing but the desk's own lists follows.
	b.run(150 * time.Millisecond)
	if n := b.srv.CountRequests("GET", `^/containers/`); n != 0 {
		t.Fatalf("%d container requests to alpha after the jump", n)
	}
	noMutations(t, b.srv, beta)
}

// A filter that hides the container is cleared, as for any other jump.
func TestAttentionJumpOnTheFocusedDaemon(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	b.keys("/")
	b.typeText("ok")
	b.keys("enter")
	b.kind(KindVolumes)
	b.openAttention()
	b.attnTo("alpha-sick")
	b.keys("enter")
	r, ok := b.m.currentRow()
	if b.m.at.open || b.m.kind != KindContainers || b.m.focus != PaneList || !ok || r.name != "alpha-sick" {
		t.Fatalf("not on alpha-sick:\n%s", b.screen())
	}
	if b.m.filter[KindContainers] != "" {
		t.Fatal("the filter that hid it was kept")
	}
	if b.m.at.jump != nil {
		t.Fatal("a jump is still pending")
	}
}

func TestAttentionJumpToAContainerThatIsGone(t *testing.T) {
	// On another daemon: focus goes there, finds nothing, and comes back to
	// the pane, kind and highlight it left.
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	b.add("beta", beta)
	alpha := b.m.cur
	b.kind(KindImages)
	b.selectRow("app:1")
	b.m.focus = PaneDetails
	before := b.state()
	key := b.m.sel[KindImages].key

	b.openAttention()
	b.attnTo("beta-failed")
	beta.RemoveContainer("beta-failed") // a destroy: not an event the view re-reads for
	b.run(100 * time.Millisecond)
	if b.attnCursorName() != "beta-failed" {
		t.Fatal("the row went before Enter: the test proves nothing")
	}
	b.keys("enter")
	b.until("the desk back on alpha", func() bool {
		return b.m.at.jump == nil && b.m.at.back == nil && b.m.cur == alpha && b.m.loaded[listImages]
	})
	b.see("beta-failed is no longer on beta", "The desk is where it was.")
	if after := b.state(); after != before {
		t.Fatalf("the desk moved: %+v, was %+v", after, before)
	}
	if b.m.sel[KindImages].key != key {
		t.Fatalf("the highlight moved: %q, was %q", b.m.sel[KindImages].key, key)
	}
	if strings.Contains(b.screen(), "beta-ok") {
		t.Fatalf("beta's objects on alpha's desk:\n%s", b.screen())
	}

	// On the focused daemon: nothing moves at all.
	b.kind(KindContainers)
	b.selectRow("alpha-paused")
	b.m.focus = PaneList
	before = b.state()
	b.openAttention()
	b.attnTo("alpha-failed")
	b.srv.RemoveContainer("alpha-failed")
	// The desk's own list drops it; the view, which does not re-read for a
	// removal, still shows the row.
	b.until("the desk's list without it", func() bool {
		for _, c := range b.m.snap.Containers {
			if c.Name == "alpha-failed" {
				return false
			}
		}
		return true
	})
	if b.attnCursorName() != "alpha-failed" {
		t.Fatal("the row went before Enter: the test proves nothing")
	}
	b.keys("enter")
	if b.m.at.open || b.m.at.jump == nil {
		t.Fatal("Enter did not start a jump")
	}
	b.until("the jump given up", func() bool { return b.m.at.jump == nil })
	b.see("alpha-failed is no longer on")
	b.selectRow("alpha-paused") // the list was rebuilt without the container
	if after := b.state(); after != before || b.m.cur != alpha {
		t.Fatalf("the desk moved: %+v, was %+v", after, before)
	}
	noMutations(t, b.srv, beta)
}

func TestAttentionJumpToADisconnectedMember(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	mem := b.add("beta", beta)
	alpha := b.m.cur
	b.kind(KindNetworks)
	b.m.focus = PaneKinds
	before := b.state()
	b.openAttention()
	b.attnTo("beta-sick")
	// The daemon goes while its rows are still on screen: no frame runs
	// between its loss and the key.
	beta.Stop()
	deadline := time.Now().Add(8 * time.Second)
	for mem.State() == session.Connected {
		if time.Now().After(deadline) {
			t.Fatal("beta never noticed its daemon was gone")
		}
		time.Sleep(3 * time.Millisecond)
	}
	b.step(keyMsg("enter"))
	if b.m.cur != alpha {
		t.Fatal("focus moved to a daemon that is not connected")
	}
	s := b.screen()
	if !strings.Contains(s, "beta is disconnected") || !strings.Contains(s, "beta-sick cannot be gone to. Nothing moved.") {
		t.Fatalf("the desk does not say why:\n%s", s)
	}
	// The view is still there; closing it finds the desk as it was.
	if !b.m.at.open {
		t.Fatal("the view closed")
	}
	b.keys("esc")
	if after := b.state(); after != before {
		t.Fatalf("the desk moved: %+v, was %+v", after, before)
	}

	// A member that left the session altogether.
	if err := beta.Restart(); err != nil {
		t.Fatal(err)
	}
	b.until("beta back", func() bool { return mem.State() == session.Connected })
	b.openAttention()
	b.attnTo("beta-sick")
	if _, err := b.m.sess.Remove(mem.ID()); err != nil {
		t.Fatal(err)
	}
	b.step(keyMsg("enter"))
	if s := b.screen(); !strings.Contains(s, "has left the session") || b.m.cur != alpha {
		t.Fatalf("\n%s", s)
	}
}

// A key or a click while a jump waits ends the wait, says so, and does
// nothing else: what was typed for the container the jump was going to must
// not act on whatever row is highlighted instead.
func TestAttentionJumpIsDroppedByAKey(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	beta := b.daemon("beta", mixed("beta"))
	mem := b.add("beta", beta)
	alpha := b.m.cur

	// To another daemon: its list is slow, and `s` (start) is typed ahead.
	b.openAttention()
	b.attnTo("beta-sick")
	beta.Stall("GET", attnListPath, 250*time.Millisecond)
	b.keys("enter")
	if b.m.at.jump == nil || b.m.cur != mem {
		t.Fatal("no jump is pending")
	}
	b.step(keyMsg("s"))
	if b.m.at.jump != nil {
		t.Fatal("the jump survived a key")
	}
	if b.m.dlg != nil || b.m.preparing {
		t.Fatal("the key that ended the wait also acted")
	}
	s := b.screen()
	if !strings.Contains(s, "Stopped looking for beta-sick on beta") || !strings.Contains(s, "The focus stays on beta.") {
		t.Fatalf("the desk does not say the jump was given up:\n%s", s)
	}
	b.until("beta's list", func() bool { return b.m.loaded[listContainers] })
	b.run(100 * time.Millisecond)
	if r, ok := b.m.currentRow(); ok && r.name == "beta-sick" {
		t.Fatal("the dropped jump still moved the highlight")
	}
	if b.m.dlg != nil {
		t.Fatal("a confirmation opened")
	}
	beta.ClearFailures()
	b.focusName(alpha.Name())
	b.until("alpha's lists", func() bool { return b.m.loaded[listContainers] })

	// On the focused daemon, for a container the desk's list does not have
	// yet: `d` typed ahead removes nothing and opens nothing.
	b.kind(KindContainers)
	b.selectRow("alpha-paused")
	b.openAttention()
	b.attnTo("alpha-sick")
	id := b.srv.ContainerID("alpha-sick")
	var kept []engine.Container
	for _, c := range b.m.snap.Containers {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	b.m.snap.Containers = kept // as if the desk's list predated the container
	b.m.dataVersion++
	b.srv.Stall("GET", attnListPath, 250*time.Millisecond)
	b.step(keyMsg("enter"))
	if b.m.at.jump == nil {
		t.Fatal("no jump is pending on the focused daemon")
	}
	b.step(keyMsg("d"))
	if b.m.at.jump != nil || b.m.dlg != nil || b.m.preparing {
		t.Fatalf("d during the wait: jump %v, dialog %v, preparing %v", b.m.at.jump != nil, b.m.dlg != nil, b.m.preparing)
	}
	if s := b.screen(); !strings.Contains(s, "Stopped looking for alpha-sick") || strings.Contains(s, "The focus stays") {
		t.Fatalf("\n%s", s)
	}
	b.srv.ClearFailures()

	// A click ends a wait the same way, and selects nothing.
	b.until("the list", func() bool {
		for _, c := range b.m.snap.Containers {
			if c.ID == id {
				return true
			}
		}
		return false
	})
	b.selectRow("alpha-paused")
	before := b.state()
	b.m.at.jump = &attnJump{member: alpha.ID(), id: "gone", name: "ghost", daemon: "alpha", until: time.Now().Add(time.Hour), asked: time.Now().Add(time.Hour)}
	b.click(b.hitOf(hitListRow, func(h hit) bool { return h.index == 0 }))
	if b.m.at.jump != nil || b.state() != before {
		t.Fatal("the click that ended the wait also selected a row")
	}
	b.see("Stopped looking for ghost on alpha")
	noMutations(t, b.srv, beta)
}

// Enter acts on the row on screen: a highlighted row scrolled out of view
// is brought back first, and only the next Enter goes to it.
func TestAttentionEnterNeedsItsRowOnScreen(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: many("alpha", 25, 3), width: 80, height: 24})
	b.openAttention()
	b.draw()
	if !b.m.at.onScreen || b.attnCursorName() != "alpha-failed00" {
		t.Fatal("the cursor does not start on screen")
	}
	// Past the last row the view scrolls; the cursor can then be anywhere.
	b.m.at.off, b.m.at.follow = 40, false
	b.draw()
	if b.m.at.onScreen || strings.Contains(b.screen(), "alpha-failed00") {
		t.Fatalf("the cursor's row is still on screen:\n%s", b.screen())
	}
	b.keys("enter")
	if !b.m.at.open || b.m.at.jump != nil || b.m.kind != KindContainers {
		t.Fatal("Enter went to a row that was not on screen")
	}
	b.see("The highlighted container is alpha-failed00: Enter again goes to it.")
	if !strings.Contains(lineWith(b.screen(), "▸"), "alpha-failed00") {
		t.Fatalf("the row was not brought back:\n%s", b.screen())
	}
	b.keys("enter")
	if r, ok := b.m.currentRow(); b.m.at.open || !ok || r.name != "alpha-failed00" {
		t.Fatal("the second Enter did not go to it")
	}
}

// A list longer than the view looks at: the rest is counted, in words, and
// the session is not called clear.
func TestAttentionSaysWhatWasNotLookedAt(t *testing.T) {
	b := newBench(t, benchOpts{seed: calm("alpha"), attention: session.AttentionConfig{MaxContainers: 3}})
	b.openAttention()
	s := b.flat()
	if !strings.Contains(s, "1 more containers of this daemon were not looked at: the view reads the first 3 of a list") {
		t.Fatalf("\n%s", b.screen())
	}
	if strings.Contains(s, "Nothing needs attention") || !strings.Contains(s, "not everything was read") {
		t.Fatalf("a list that was cut is called clear:\n%s", b.screen())
	}
	b.assertFrameGeometry()
}

// ---- 3.5 the pointer -------------------------------------------------------------------

func TestAttentionMouse(t *testing.T) {
	b := newMultiBench(t, benchOpts{seed: many("alpha", 25, 3), width: 100, height: 30})
	beta := b.daemon("beta", many("beta", 25, 3))
	b.add("beta", beta)
	b.openAttention()
	b.srv.ResetRequests()
	beta.ResetRequests()
	rows := b.hits(hitAttnRow)
	if len(rows) < 5 {
		t.Fatalf("%d rows recorded", len(rows))
	}
	// Each recorded row is drawn where it says.
	lines := strings.Split(b.screen(), "\n")
	for _, h := range rows {
		id := strings.SplitN(h.key, "/", 2)[1]
		name := ""
		for _, r := range b.m.attnRows() {
			if r.id == id {
				name = r.name
			}
		}
		if name == "" || !strings.Contains(lines[h.r.y], name) {
			t.Fatalf("row %q is recorded on line %d: %q", name, h.r.y, lines[h.r.y])
		}
	}

	// A click highlights the row, and goes nowhere.
	target := rows[4]
	b.click(target)
	if b.m.at.cursorKey != target.key {
		t.Fatalf("the click did not move the cursor: %q", b.m.at.cursorKey)
	}
	if !b.m.at.open || b.m.dlg != nil {
		t.Fatal("a click left the view, or opened something")
	}
	b.click(target) // again: still only a highlight
	if !b.m.at.open || b.m.at.jump != nil {
		t.Fatal("a second click jumped")
	}

	// The wheel moves the cursor, three rows a notch; held at the end it
	// scrolls.
	before := b.attnCursorName()
	b.wheel(target, 1)
	rowsNow := b.m.attnRows()
	if i := b.m.attnCursor(rowsNow); rowsNow[i-3].name != before {
		t.Fatalf("one notch moved from %s to %s", before, rowsNow[i].name)
	}
	for i := 0; i < 40; i++ {
		b.wheel(b.hitOf(hitOverlay, nil), 1)
	}
	if got := b.attnCursorName(); got != "beta-failed24" {
		t.Fatalf("the wheel stopped on %q", got)
	}
	if m := attnPos.FindStringSubmatch(b.screen()); m == nil || m[2] != m[3] {
		t.Fatalf("the wheel does not reach the end:\n%s", b.screen())
	}
	for i := 0; i < 40; i++ {
		b.wheel(b.hitOf(hitOverlay, nil), -1)
	}
	if got := b.attnCursorName(); got != "alpha-failed00" {
		t.Fatalf("the wheel back up stopped on %q", got)
	}

	// Nothing behind the view can be clicked: not a pane, not the strip.
	for _, h := range b.m.hits {
		if h.kind == hitPane || h.kind == hitListRow || h.kind == hitKindRow || h.kind == hitDetailsTab || h.kind == hitJobRow {
			t.Fatalf("a region of kind %d is clickable under the view", h.kind)
		}
	}
	focused := b.m.cur
	b.mouse(press(30, 1, tea.MouseButtonLeft)) // where the strip names beta
	if b.m.cur != focused || !b.m.at.open {
		t.Fatal("a click on the strip moved the focus under the view")
	}
	// A right click and a release do nothing.
	x, y := centre(rows[1].r)
	b.mouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress})
	b.mouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if got := b.attnCursorName(); got != "alpha-failed00" {
		t.Fatalf("a right click or a release moved the cursor to %q", got)
	}
	noMutations(t, b.srv, beta)
}

// ---- 3.6 the view only reads -----------------------------------------------------------

// attnAllowed reports whether a request is one the view, or the desk it lies
// over, has any business sending while the view is open.
func attnAllowed(r fakeengine.Request, focused bool) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	switch {
	case strings.HasSuffix(r.Path, "/_ping"), r.Path == "/events",
		regexp.MustCompile(attnListPath).MatchString(r.Path), regexp.MustCompile(attnInspectPath).MatchString(r.Path):
		return true
	}
	// The focused daemon's own panes go on as before under the view.
	return focused
}

func TestAttentionIsReadOnly(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		b := newMultiBench(t, benchOpts{seed: mixed("alpha"), readOnly: readOnly})
		beta := b.daemon("beta", mixed("beta"))
		b.add("beta", beta)
		b.srv.ResetRequests()
		beta.ResetRequests()
		b.openAttention() // opens under --read-only too
		if readOnly && !strings.Contains(b.screen(), "[READ-ONLY]") {
			t.Fatal("the session is not read-only")
		}
		b.see("alpha-loop", "beta-loop")

		// Every key the keyboard has, on a row of each daemon. None of them
		// may send anything that is not a read; whatever closes the view or
		// opens a dialog is undone before the next.
		keys := []string{"enter", "tab", "shift+tab", "up", "down", "left", "right", "pgup", "pgdown", "home", "end",
			"backspace", " ", "ctrl+n", "ctrl+x", "esc"}
		for c := rune('!'); c <= '~'; c++ {
			if c != 'q' { // quits the desk; tested where quitting is
				keys = append(keys, string(c))
			}
		}
		for _, row := range []string{"alpha-sick", "beta-failed"} {
			for _, k := range keys {
				if !b.m.at.open {
					b.openAttention()
				}
				b.attnTo(row)
				b.keys(k)
				b.run(15 * time.Millisecond)
				for i := 0; i < 3 && b.m.dlg != nil; i++ {
					if b.m.dlg.kind != dlgHelp {
						t.Fatalf("%q opened a dialog of kind %d from the view", k, b.m.dlg.kind)
					}
					b.keys("esc")
				}
				if b.m.preparing {
					t.Fatalf("%q asked for a confirmation from the view", k)
				}
				if k != "enter" && k != "esc" && k != keyAttention && !b.m.at.open {
					t.Fatalf("%q closed the view", k)
				}
				// Enter may have focused the other daemon: come back.
				if b.m.cur.Name() == "beta" {
					b.until("beta's lists", func() bool { return b.m.loaded[listContainers] })
					b.focusName(b.m.sess.Members()[0].Name())
					b.until("alpha's lists", func() bool { return b.m.loaded[listContainers] })
				}
			}
		}
		if b.quit {
			t.Fatal("a key quit the desk")
		}
		noMutations(t, b.srv, beta)
		if n := b.m.jobs.RunningCount() + len(b.m.jobs.List()); n != 0 {
			t.Fatalf("%d job(s) were started from the view", n)
		}
		for _, r := range beta.Requests() {
			// beta is focused only for the moment of a jump; nothing but
			// reads ever reaches it.
			if r.Method != "GET" && r.Method != "HEAD" {
				t.Fatalf("beta received %s", r)
			}
		}
		// A mutating key says why nothing happened.
		if !b.m.at.open {
			b.openAttention()
		}
		b.attnTo("alpha-sick")
		b.keys("d")
		b.see("Attention only reads: nothing was sent.")
		if b.m.dlg != nil || !b.m.at.open {
			t.Fatal("d did something in the view")
		}

		// With the view simply open — no jump — a background daemon gets
		// nothing but the view's reads.
		beta.ResetRequests()
		b.keys("r")
		b.until("r", func() bool { return beta.CountRequests("GET", attnListPath) == 1 && b.attnSettled() })
		beta.Crash("beta-ok", 2)
		b.until("the event's read", func() bool { return beta.CountRequests("GET", attnListPath) == 2 && b.attnSettled() })
		for _, r := range beta.Requests() {
			if !attnAllowed(r, false) {
				t.Fatalf("beta received %s while the view was open", r)
			}
		}
		noMutations(t, b.srv, beta)
	}
}

// ---- what a daemon says is drawn as text -----------------------------------------------

// A daemon chooses its container names, its status prose, its health word
// and its error text. None of them may reach the terminal as anything but
// characters: no escape sequence, no forged line.
func TestAttentionDrawsHostileTextAsText(t *testing.T) {
	const osc = "\x1b]0;pwned\x07"
	const clear = "\x1b[2J\x1b[H"
	hostile := func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "evil" + osc + clear + "\nforged-row", Image: "img" + osc + ":1",
			State: "exited", ExitCode: 9, Labels: map[string]string{"com.docker.compose.project": "proj" + clear}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "sick\u202egnp.exe", Image: "app:1", Health: "unhealthy"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "dead-one", Image: "app:1", State: "dead",
			StateError: "oci runtime error" + osc + clear + strings.Repeat("A", 5000)})
		s.AddContainer(fakeengine.ContainerSpec{Name: "odd-health", Image: "app:1", Health: "un" + clear + "healthy", RestartCount: 9})
	}
	for _, size := range [][2]int{{140, 40}, {80, 24}} {
		b := newMultiBench(t, benchOpts{seed: mixed("alpha"), width: size[0], height: size[1]})
		beta := b.daemon("beta", hostile)
		beta.Name = "host" + osc
		b.add("beta", beta)
		b.openAttention()
		for _, k := range []string{"", "tab", "down", "down", "down", "end"} {
			if k != "" {
				b.keys(k)
			}
			b.m.dirty = true
			raw := b.m.View()
			for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[H", "\x07", "\u202e"} {
				if strings.Contains(raw, bad) {
					t.Fatalf("%dx%d after %q: the frame carries %q", size[0], size[1], k, bad)
				}
			}
			b.assertFrameGeometry() // no forged line either
		}
		b.keys("tab")
		s := b.screen()
		if !strings.Contains(s, `evil\x1b]0;pwned\x07`) {
			t.Fatalf("the name is not shown escaped:\n%s", s)
		}
		if !strings.Contains(s, `sick\u202egnp.exe`) {
			t.Fatalf("the reordering character is not shown escaped:\n%s", s)
		}
		// The 5000-character error did not become a 5000-character screen.
		if n := strings.Count(s, "AAAAAAAAAA"); n > 60 {
			t.Fatalf("an unbounded error was laid out (%d runs)", n)
		}

		// Enter on a row that is there: the desk it lands on — the
		// Containers list, the Details title and tab, the status line —
		// draws the same strings, and none of them raw.
		b.attnTo("sick\u202egnp.exe")
		b.keys("enter")
		b.until("the jump landed", func() bool {
			r, ok := b.m.currentRow()
			return b.m.at.jump == nil && ok && b.m.cur.Name() == "beta" && r.containerID == beta.ContainerID("sick\u202egnp.exe")
		})
		b.selectRow(`evil\x1b]0;pwned\x07\x1b[2J\x1b[H\nforged-row`)
		b.settle()
		for i := 0; i < 2; i++ {
			b.m.dirty = true
			raw := b.m.View()
			for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[H", "\x07", "\u202e"} {
				if strings.Contains(raw, bad) {
					t.Fatalf("%dx%d after a jump (Details tab %d): the frame carries %q", size[0], size[1], i, bad)
				}
			}
			b.assertFrameGeometry()
			b.keys("3", "]", "2")
			b.settle()
		}
		if s := b.screen(); !strings.Contains(s, `evil\x1b]0;pwned`) {
			t.Fatalf("the list the jump lands on does not show the name escaped:\n%s", s)
		}
		b.focusName(b.m.sess.Members()[0].Name())
		b.until("alpha's lists", func() bool { return b.m.loaded[listContainers] })
		b.openAttention()

		// The status line names the container too: Enter on a row whose
		// container is gone.
		b.attnTo("evil" + osc + clear + "\nforged-row")
		beta.RemoveContainer("evil" + osc + clear + "\nforged-row")
		b.keys("enter")
		b.until("the jump given up", func() bool { return b.m.at.jump == nil && b.m.at.back == nil })
		b.m.dirty = true
		raw := b.m.View()
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\x07"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("the status line carries %q", bad)
			}
		}
		b.assertFrameGeometry()
	}
}

func TestClipText(t *testing.T) {
	if got := clipText("plain"); got != "plain" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 1000) // two bytes each: the cut must not split one
	got := clipText(long)
	if !strings.HasSuffix(got, "…") || len(got) > attnTextMax+4 || strings.Contains(got, `\x`) {
		t.Fatalf("clipped to %d bytes: %q", len(got), got[len(got)-12:])
	}
	if got := clipText("a\x1b[31mb\nc"); got != `a\x1b[31mb\nc` {
		t.Fatalf("%q", got)
	}
	// A state or health word is bounded in cells, whatever it is made of.
	for _, w := range []string{"un\x1b[2Jhealthy" + strings.Repeat("y", 100), strings.Repeat("\x01", 500), strings.Repeat("é", 300), "\xff\xfe\xfd" + strings.Repeat("\x9b", 40)} {
		got := daemonWord(w)
		if strings.Contains(got, "\x1b") || ansi.StringWidth(got) > wordMax || !lineIsClean(got) {
			t.Fatalf("%q → %q (%d cells)", w[:8], got, ansi.StringWidth(got))
		}
	}
	for _, w := range []string{"running", "restarting", "unhealthy", "starting", "healthy"} {
		if daemonWord(w) != w {
			t.Fatalf("%q is changed", w)
		}
	}
	// The state is escaped like the health beside it.
	if got := stateCell("run\x1b[2Jning", "healthy", false); strings.Contains(got, "\x1b") {
		t.Fatalf("%q", got)
	}
	if got := stateCell("run\x1b[2Jning", "", false); strings.Contains(got, "\x1b") {
		t.Fatalf("%q", got)
	}
}

// ---- 4.1 health in the STATE cell ------------------------------------------------------

func TestContainersStateCellShowsReportedHealth(t *testing.T) {
	b := newBench(t, benchOpts{seed: mixed("alpha"), width: 200, height: 40})
	cell := func(name string) (string, tone) {
		for _, r := range b.m.rows[KindContainers] {
			if r.name == name {
				return r.cells[colContainerState], r.tone
			}
		}
		t.Fatalf("no row %s", name)
		return "", 0
	}
	for _, tc := range []struct {
		name, cell string
		tone       tone
	}{
		{"alpha-sick", "● running · unhealthy", toneWarn},
		{"alpha-starting", "● running · starting", toneWarn},
		{"alpha-ok", "● running · healthy", toneGood},
		{"alpha-nocheck", "● running", toneGood}, // no healthcheck: nothing is invented
		{"alpha-paused", "‖ paused", toneWarn},
		{"alpha-done", "○ exited", toneDim},
		{"alpha-dead", "✗ dead", toneBad},
		{"alpha-loop", "↻ restarting", toneWarn},
	} {
		got, tn := cell(tc.name)
		if got != tc.cell || tn != tc.tone {
			t.Errorf("%s: cell %q tone %d, want %q tone %d", tc.name, got, tn, tc.cell, tc.tone)
		}
	}
	// On screen, wide: both words; the unhealthy row is in the warning
	// colour, and the row without a healthcheck is not called healthy.
	s := b.screen()
	for _, want := range []string{"● running · unhealthy", "● running · starting", "● running · healthy"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lacks %q:\n%s", want, s)
		}
	}
	if l := lineWith(s, "alpha-nocheck"); strings.Contains(l, "healthy") || !strings.Contains(l, "● running") {
		t.Fatalf("the row without a healthcheck: %q", l)
	}
	b.selectRow("alpha-ok") // so that the unhealthy row is not drawn selected
	b.m.dirty = true
	raw := lineWith(b.m.View(), "alpha-sick")
	warn := b.m.st.tone(toneWarn, "x")
	if !strings.Contains(raw, strings.TrimSuffix(warn, "x"+ansiReset(warn))) && !strings.Contains(raw, warn[:strings.Index(warn, "x")]) {
		t.Fatalf("the unhealthy row is not drawn in the warning colour: %q", raw)
	}
	good := b.m.st.tone(toneGood, "x")
	if l := lineWith(b.m.View(), "alpha-nocheck"); !strings.Contains(l, good[:strings.Index(good, "x")]) {
		t.Fatalf("the row without a healthcheck lost its own colour: %q", l)
	}
	// The filter finds a container by its health.
	b.keys("/")
	b.typeText("unhealthy")
	b.keys("enter")
	if names := b.rowNames(); len(names) != 1 || names[0] != "alpha-sick" {
		t.Fatalf("filtering by health: %v", names)
	}
	b.keys("esc")

	// Narrow: the health takes the state's place rather than a column's;
	// nothing is cut, and still nothing is invented.
	n := newBench(t, benchOpts{seed: mixed("alpha"), width: 80, height: 50})
	s = n.screen()
	if !strings.Contains(s, "● unhealthy") || !strings.Contains(s, "● starting") || !strings.Contains(s, "↻ restarting") {
		t.Fatalf("the narrow list:\n%s", s)
	}
	if l := lineWith(s, "alpha-nocheck"); strings.Contains(l, "healthy") || !strings.Contains(l, "● running") {
		t.Fatalf("the narrow row without a healthcheck: %q", l)
	}
	if !strings.Contains(s, "IMAGE") {
		t.Fatalf("the health cost the narrow list its IMAGE column:\n%s", s)
	}
	n.assertFrameGeometry()

	// No health anywhere: the list is laid out exactly as before.
	p := newBench(t, benchOpts{seed: containers("plain-a", "plain-b"), width: 80, height: 24})
	if s := p.screen(); !strings.Contains(s, "● running") || strings.Contains(s, "healthy") {
		t.Fatalf("\n%s", s)
	}
}

// ansiReset is the reset sequence a styled string ends with.
func ansiReset(s string) string {
	if i := strings.LastIndex(s, "\x1b["); i >= 0 {
		return s[i:]
	}
	return ""
}

// ---- 4.2 the ambient count -------------------------------------------------------------

func TestAttentionCueCountsOnlyTheCertainSignals(t *testing.T) {
	seed := func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "loop-a", Image: "app:1", State: "restarting"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "loop-b", Image: "app:1", State: "restarting"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "sick", Image: "app:1", Health: "unhealthy"})
		// Not counted: a failed exit and a flapping container need an
		// inspect; a paused one with a failed probe is not a problem.
		s.AddContainer(fakeengine.ContainerSpec{Name: "failed", Image: "app:1", State: "exited", ExitCode: 1})
		s.AddContainer(fakeengine.ContainerSpec{Name: "flap", Image: "app:1", RestartCount: 9})
		s.AddContainer(fakeengine.ContainerSpec{Name: "new", Image: "app:1", State: "created"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "starting", Image: "app:1", Health: "starting"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "ok", Image: "app:1", Health: "healthy"})
	}
	for _, tc := range []struct {
		w, h  int
		title string
	}{
		{200, 40, "2 Containers (8) · 3 need attention — H reads exit codes and restarts too"},
		{140, 40, "2 Containers (8) · 3 need attention — H reads exit codes, restarts"},
		{80, 24, "2 Containers (8) · 3 need attention, H reads more"},
	} {
		b := newBench(t, benchOpts{seed: seed, width: tc.w, height: tc.h})
		if n := b.m.attentionCue(); n != 3 {
			t.Fatalf("the cue counts %d, want 3", n)
		}
		s := b.screen()
		if !strings.Contains(s, tc.title) {
			t.Fatalf("%dx%d: the Containers title lacks %q:\n%s", tc.w, tc.h, tc.title, s)
		}
		if !strings.Contains(s, "▲ 3 need you") || !strings.Contains(s, "H reads more") {
			t.Fatalf("%dx%d: the Kinds pane lacks the count:\n%s", tc.w, tc.h, s)
		}
		b.assertFrameGeometry()

		// It follows the list: one recovers, one more fails.
		b.srv.SetHealth("sick", "healthy")
		b.see("2 need attention", "▲ 2 need you")
		b.srv.SetHealth("ok", "unhealthy")
		b.see("3 need attention")
		// A crash is not counted: its exit code needs the view.
		b.srv.Crash("flap", 1)
		b.until("the list with the exit", func() bool {
			for _, c := range b.m.snap.Containers {
				if c.Name == "flap" && c.State == "exited" {
					return true
				}
			}
			return false
		})
		if n := b.m.attentionCue(); n != 3 {
			t.Fatalf("a failed exit moved the cue to %d", n)
		}
		// Singular.
		b.srv.RemoveContainer("loop-a")
		b.srv.RemoveContainer("loop-b")
		b.see("1 needs attention", "▲ 1 needs you")
		// And it clears.
		b.srv.SetHealth("ok", "healthy")
		b.notSee("attention")
		if s := b.screen(); strings.Contains(s, "▲") || strings.Contains(s, "reads more") {
			t.Fatalf("the cue did not clear:\n%s", s)
		}
		b.assertFrameGeometry()

		// The view finds more than the cue counted, as the cue said.
		b.openAttention()
		b.see("failed", "exited 1")
		if c := b.m.at.blocks[0].Block.Counts; c.NeedsYou < 2 {
			t.Fatalf("the view's count: %+v", c)
		}
		b.keys("esc")
	}
	// On another kind the title carries nothing; the Kinds pane still does.
	b := newBench(t, benchOpts{seed: seed})
	b.kind(KindImages)
	if s := b.screen(); strings.Contains(s, "need attention") || !strings.Contains(s, "▲ 3 need you") {
		t.Fatalf("\n%s", s)
	}
	// With a filter in the title, the count gives way before the filter does.
	b.kind(KindContainers)
	n := newBench(t, benchOpts{seed: seed, width: 80, height: 24})
	n.keys("/")
	n.typeText("loop")
	n.keys("enter")
	if s := n.screen(); !strings.Contains(s, "filter: loop") {
		t.Fatalf("the count pushed the filter out of the title:\n%s", s)
	}
	n.assertFrameGeometry()
}

func TestCueTitleForms(t *testing.T) {
	if got := cueTitle(0, 200); got != "" {
		t.Fatalf("%q", got)
	}
	if got := cueTitle(3, 2); got != "" {
		t.Fatalf("%q", got)
	}
	last := 1 << 30
	for _, room := range []int{200, 50, 34, 9} {
		got := cueTitle(3, room)
		if got == "" || ansi.StringWidth(got) > room || !strings.Contains(got, keyAttention) {
			t.Fatalf("room %d: %q", room, got)
		}
		if w := ansi.StringWidth(got); w > last {
			t.Fatalf("room %d gave a longer form", room)
		} else {
			last = w
		}
	}
	if got := cueTitle(1, 200); !strings.Contains(got, "1 needs attention") {
		t.Fatalf("%q", got)
	}
}

// ---- 4.3, 5.1 documentation ------------------------------------------------------------

func TestAttentionIsDocumented(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		// The key tables.
		"| `" + keyAttention + "` | Attention: what needs a person, on every connected daemon",
		"**In the Attention view** (`" + keyAttention + "`)",
		"`Enter` close the view, focus the row's daemon and select the container there",
		"`Tab` `Shift+Tab` the next / previous daemon's block",
		"`r` read every connected daemon's containers again",
		"`" + keyAttention + "` or `Esc` close",
		// The section: the ladder, unknown, the read model, the jump, the scope.
		"### Attention — is anything wrong, anywhere?",
		"**needs you**", "**watch**", "**fine**", "**unknown**", "**not a problem**",
		"never called fine",
		"three or more",
		"at most 200",
		"at most once a second",
		"nothing polls",
		"read time",
		"exit code other than 0",
		"OOM",
		"The desk is where it was",
		"read-only",
		"What it does not do here",
		"reads exit codes and restarts too",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}

	b := newMultiBench(t, benchOpts{seed: mixed("alpha")})
	help := ansi.Strip(strings.Join(b.m.helpLines(100), "\n"))
	for _, want := range []string{
		keyAttention + "                Attention: what needs a person, on every connected daemon of the session",
		"Attention view (" + keyAttention + ", from any pane)",
		"close the view, focus the row's daemon and select the container there",
		"the next / previous daemon's block",
		"read every connected daemon's containers again",
		keyAttention + ", Esc",
		"restarted 3 times or more", "never called fine", "at most 200 a daemon", "at most once a",
		"nothing polls", "works in read-only mode", "reads exit codes and restarts too",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not say %q", want)
		}
	}
	// The numbers the texts give are the ones the code uses.
	if session.AttentionInspects != 200 || session.AttentionGap != time.Second {
		t.Fatalf("the documented bounds are 200 inspects and one second; the code has %d and %s", session.AttentionInspects, session.AttentionGap)
	}
	if session.AttentionContainers != 5000 || attnRowsMax != 500 {
		t.Fatalf("the documented bounds are 5000 containers and 500 rows; the code has %d and %d", session.AttentionContainers, attnRowsMax)
	}
	for _, want := range []string{"first 5000 containers", "at most 500", "ends the wait and does nothing else", "neither a\ncharacter nor a colour"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
	for _, want := range []string{"first 5000 containers", "at most 500 rows", "ends the wait, says so, and does nothing else", "q  ?  T"} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not say %q", want)
		}
	}

	// And each key does what both say.
	beta := b.daemon("beta", mixed("beta"))
	mem := b.add("beta", beta)
	for _, closeKey := range []string{keyAttention, "esc"} {
		b.openAttention()
		b.keys(closeKey)
		if b.m.at.open {
			t.Fatalf("%q does not close the view", closeKey)
		}
	}
	b.openAttention()
	b.keys("tab")
	if got := b.attnCursorName(); !strings.HasPrefix(got, "beta-") {
		t.Fatalf("Tab went to %q", got)
	}
	b.keys("shift+tab")
	if got := b.attnCursorName(); !strings.HasPrefix(got, "alpha-") {
		t.Fatalf("Shift+Tab went to %q", got)
	}
	beta.ResetRequests()
	b.keys("r")
	b.until("r reads beta", func() bool { return beta.CountRequests("GET", attnListPath) == 1 })
	b.until("settled", b.attnSettled)
	b.attnTo("beta-sick")
	b.keys("enter")
	b.until("Enter selects the container on its daemon", func() bool {
		r, ok := b.m.currentRow()
		return b.m.cur == mem && ok && r.name == "beta-sick"
	})
	// H is in no kind's action map, and the hints name it.
	for kind, acts := range kindActions {
		for _, a := range acts {
			if a.key == keyAttention {
				t.Fatalf("%s is also %s's %q action", keyAttention, kindNames[kind], a.label)
			}
		}
	}
}
