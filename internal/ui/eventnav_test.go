package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/live"
)

// ---- the held view and going to an event's object ---------------------------------------

// emitAndWait publishes an event and waits until the desk has recorded it.
func (b *bench) emitAndWait(typ, action, id string, attrs map[string]string) {
	b.t.Helper()
	_, end := b.m.hub.Events.Bounds()
	b.srv.Emit(typ, action, id, attrs)
	b.until(typ+" "+action+" recorded", func() bool { _, e := b.m.hub.Events.Bounds(); return e > end })
	b.settle()
}

// holdOn focuses the Events pane and holds it, the cursor on the newest event.
func (b *bench) holdOn() {
	b.t.Helper()
	b.keys("4")
	if b.m.evHeld {
		b.t.Fatal("the Events pane was already held")
	}
	b.keys(" ")
	if !b.m.evHeld || b.m.evCur != len(b.m.evSnap)-1 {
		b.t.Fatalf("Space did not hold the view on the newest event (held %v, cursor %d of %d)", b.m.evHeld, b.m.evCur, len(b.m.evSnap))
	}
}

// jumpVia emits an event, holds, and presses Enter on it.
func (b *bench) jumpVia(typ, action, id string, attrs map[string]string) {
	b.t.Helper()
	b.emitAndWait(typ, action, id, attrs)
	b.holdOn()
	b.keys("enter")
	b.settle()
}

func (b *bench) highlighted() row {
	b.t.Helper()
	r, ok := b.m.currentRow()
	if !ok {
		b.t.Fatalf("no row highlighted in %s", kindNames[b.m.kind])
	}
	return r
}

func (b *bench) imageID(ref string) string {
	b.t.Helper()
	for _, img := range b.m.snap.Images {
		if slices.Contains(img.RepoTags, ref) {
			return img.ID
		}
	}
	b.t.Fatalf("no image %s", ref)
	return ""
}

func (b *bench) networkID(name string) string {
	b.t.Helper()
	for _, n := range b.m.snap.Networks {
		if n.Name == name {
			return n.ID
		}
	}
	b.t.Fatalf("no network %s", name)
	return ""
}

// eventsPane is the Events pane's rows as drawn now.
func (b *bench) eventsPane() string {
	return ansi.Strip(strings.Join(b.m.renderEvents(40, b.m.eventHeight, true), "\n"))
}

func TestHoldSurvivesABurstLargerThanTheRing(t *testing.T) {
	b := newBench(t, benchOpts{})
	id := b.srv.ContainerID("shop-web-1")
	for i := 0; i < 30; i++ {
		b.srv.Emit("container", "top", id, map[string]string{"name": fmt.Sprintf("before-%02d", i)})
	}
	b.see("before-29")
	b.holdOn()
	b.see("‖ held +0", "▸ ")
	held := append([]live.EventEntry(nil), b.m.evSnap...)
	pane := b.eventsPane()

	_, end := b.m.hub.Events.Bounds()
	burst := b.m.hub.Events.Cap() + 200
	for i := 0; i < burst; i++ {
		b.srv.Emit("container", "top", id, map[string]string{"name": fmt.Sprintf("burst-%04d", i)})
	}
	b.until("the burst recorded", func() bool { _, e := b.m.hub.Events.Bounds(); return e >= end+uint64(burst) })
	b.settle()

	// The ring has turned over: none of the held entries is in it any more.
	if first, _ := b.m.hub.Events.Bounds(); first < end {
		t.Fatalf("the ring still holds entry %d; the burst did not evict the held ones", first)
	}
	b.see(fmt.Sprintf("‖ held +%d", burst))
	if !slices.EqualFunc(held, b.m.evSnap, func(a, c live.EventEntry) bool {
		return a.Time.Equal(c.Time) && a.Event.Action == c.Event.Action && a.Event.Attributes["name"] == c.Event.Attributes["name"]
	}) {
		t.Fatal("the snapshot changed while held")
	}
	if got := b.eventsPane(); got != pane {
		t.Fatalf("held rows moved:\nbefore:\n%s\nafter:\n%s", pane, got)
	}
	b.notSeeNow("burst-")

	// The subscription kept going underneath: lists are still marked dirty.
	if b.srv.EventSubscribers() != 1 {
		t.Fatal("holding the view stopped the event subscription")
	}

	// Resuming follows the ring again.
	b.keys(" ")
	b.see("● live", fmt.Sprintf("burst-%04d", burst-1))
	b.assertFrameGeometry()
}

// cursorDrawn checks whether the Events pane draws its cursor glyph, on
// the cursor row only.
func (b *bench) cursorDrawn(want bool) {
	b.t.Helper()
	n := 0
	for i, l := range strings.Split(b.eventsPane(), "\n") {
		if strings.HasPrefix(l, "▸ ") {
			n++
			if i != b.m.evCur-b.m.evOff {
				b.t.Fatalf("the cursor glyph is on row %d, the cursor on %d", i, b.m.evCur-b.m.evOff)
			}
		} else if !strings.HasPrefix(l, "  ") {
			b.t.Fatalf("row without its gutter: %q", l)
		}
	}
	if (n == 1) != want || n > 1 {
		b.t.Fatalf("cursor glyphs drawn: %d, want cursor %v\n%s", n, want, b.eventsPane())
	}
}

// notSeeNow checks the current screen, without waiting.
func (b *bench) notSeeNow(sub string) {
	b.t.Helper()
	if strings.Contains(b.screen(), sub) {
		b.t.Fatalf("the screen shows %q:\n%s", sub, b.screen())
	}
}

func TestHeldCountAndScrolling(t *testing.T) {
	b := newBench(t, benchOpts{})
	id := b.srv.ContainerID("shop-web-1")
	for i := 0; i < 80; i++ {
		b.srv.Emit("container", "top", id, map[string]string{"name": fmt.Sprintf("probe-%02d", i)})
	}
	b.see("probe-79")
	b.keys("4")
	b.see("Space hold")

	// A read-back key from the live view holds it and moves the cursor.
	b.keys("up")
	if !b.m.evHeld || b.m.evCur != len(b.m.evSnap)-2 {
		t.Fatalf("↑ while live: held %v, cursor %d of %d", b.m.evHeld, b.m.evCur, len(b.m.evSnap))
	}
	b.see("probe-78", "⏎ go to object")
	b.cursorDrawn(true)

	// +N counts what arrived since.
	for i := 0; i < 3; i++ {
		b.srv.Emit("container", "top", id, map[string]string{"name": fmt.Sprintf("late-%d", i)})
	}
	b.see("‖ held +3")
	b.notSeeNow("late-0")

	// Inside the hold: to the top, a page, back to the newest.
	b.keys("g")
	if b.m.evCur != 0 {
		t.Fatalf("g put the cursor at %d", b.m.evCur)
	}
	b.cursorDrawn(true) // the cursor row stays on screen
	b.keys("pgdown")
	if b.m.evCur != max(b.m.eventHeight-1, 1) {
		t.Fatalf("PgDn put the cursor at %d (page %d)", b.m.evCur, b.m.eventHeight-1)
	}
	for i := 0; i < 20; i++ {
		b.keys("pgdown")
	}
	if !b.m.evHeld || b.m.evCur != len(b.m.evSnap)-1 {
		t.Fatalf("PgDn must stop at the newest held entry (held %v, cursor %d of %d)", b.m.evHeld, b.m.evCur, len(b.m.evSnap))
	}
	b.keys("up", "down")
	if !b.m.evHeld {
		t.Fatal("↓ onto the newest entry resumed")
	}
	// ↓ past the newest held entry resumes and follows.
	b.keys("down")
	b.see("● live", "late-2")
	b.cursorDrawn(false)

	// G and f resume too; f holds.
	b.keys("f")
	b.see("‖ held +0")
	b.keys("G")
	b.see("● live")
	b.keys("pgup", "end")
	b.see("● live")

	// Esc leaves the pane; the cursor is drawn without the selection style.
	b.keys("k", "esc")
	if b.m.focus != PaneList || !b.m.evHeld {
		t.Fatal("Esc must leave the pane and keep the hold")
	}
	b.cursorDrawn(true)
	if rows := b.m.renderEvents(40, b.m.eventHeight, false); !strings.HasPrefix(rows[b.m.evCur-b.m.evOff], "▸ ") {
		t.Fatalf("unfocused cursor row %q", rows[b.m.evCur-b.m.evOff])
	}
	b.assertFrameGeometry()
}

func TestEnterIsRefusedWhileLive(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.emitAndWait("container", "restart", b.srv.ContainerID("redis-cache"), map[string]string{"name": "redis-cache"})
	b.kind(KindVolumes)
	b.keys("4", "enter")
	b.see("Hold the view first (Space) so the event stays put")
	if b.m.focus != PaneEvents || b.m.kind != KindVolumes || b.m.evHeld {
		t.Fatal("Enter while live changed something")
	}
}

func TestJumpToAContainerAndAnExec(t *testing.T) {
	b := newBench(t, benchOpts{})
	id := b.srv.ContainerID("redis-cache")
	b.kind(KindVolumes)
	b.jumpVia("container", "restart", id, map[string]string{"name": "redis-cache"})
	if b.m.focus != PaneList || b.m.kind != KindContainers || b.highlighted().containerID != id {
		t.Fatalf("landed on %s in %s, focus %v", b.highlighted().name, kindNames[b.m.kind], b.m.focus)
	}

	// An exec event names the container it ran in.
	other := b.srv.ContainerID("shop-db-1")
	b.keys("4", " ") // resume
	b.jumpVia("container", "exec_start: sh -c ls", other, map[string]string{"name": "shop-db-1", "execID": "abc"})
	if b.highlighted().containerID != other {
		t.Fatalf("exec jumped to %s", b.highlighted().name)
	}
}

func TestJumpToAnImageWithSeveralReferences(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddImage(50<<20, time.Hour, "acme/multi:1.0", "acme/multi:latest", "acme/multi:stable")
	}})
	id := b.imageID("acme/multi:latest")

	// The reference the event names is preferred over the image's first row.
	b.jumpVia("image", "tag", id, map[string]string{"name": "acme/multi:stable"})
	if r := b.highlighted(); b.m.kind != KindImages || r.imageRef != "acme/multi:stable" {
		t.Fatalf("tag event landed on %q", r.name)
	}

	// A reference that is gone falls back to the first row of the image.
	b.keys("4", " ")
	b.jumpVia("image", "untag", id, map[string]string{"name": "acme/multi:old"})
	if r := b.highlighted(); r.imageID != id || r.imageRef != "acme/multi:1.0" {
		t.Fatalf("untag event landed on %q", r.name)
	}

	// A pull names the reference, not an ID.
	b.keys("4", " ")
	b.jumpVia("image", "pull", "acme/multi:latest", map[string]string{"name": "acme/multi"})
	if r := b.highlighted(); r.imageRef != "acme/multi:latest" {
		t.Fatalf("pull event landed on %q", r.name)
	}
}

func TestJumpToAVolumeAndANetworkDisconnect(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.jumpVia("volume", "mount", "shop_pgdata", map[string]string{"container": b.srv.ContainerID("shop-db-1")})
	if r := b.highlighted(); b.m.kind != KindVolumes || r.volume != "shop_pgdata" {
		t.Fatalf("volume event landed on %q in %s", r.name, kindNames[b.m.kind])
	}

	// The actor of a disconnect is the network; the container is an attribute.
	net := b.networkID("staging")
	b.keys("4", " ")
	b.jumpVia("network", "disconnect", net, map[string]string{"name": "staging", "container": b.srv.ContainerID("redis-cache")})
	if r := b.highlighted(); b.m.kind != KindNetworks || r.networkID != net {
		t.Fatalf("disconnect landed on %q in %s", r.name, kindNames[b.m.kind])
	}
}

func TestJumpClearsAFilterThatHidesTheRow(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindContainers)
	b.keys("2", "/")
	b.typeText("shop")
	b.keys("enter")
	b.jumpVia("container", "restart", b.srv.ContainerID("redis-cache"), map[string]string{"name": "redis-cache"})
	b.see("Cleared the Containers filter: it hid redis-cache.")
	if b.highlighted().name != "redis-cache" || b.m.filter[KindContainers] != "" {
		t.Fatal("the filter was not cleared for the jump")
	}
}

func TestEventsWithoutAnObject(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.kind(KindVolumes)
	b.jumpVia("daemon", "reload", "DAEMONID", map[string]string{"name": "sandbox"})
	b.see("The desk does not list daemon objects")
	if b.m.focus != PaneEvents || b.m.kind != KindVolumes || !b.m.evHeld {
		t.Fatal("an unlisted type changed the view")
	}

	// A marker row is about the stream, not an object.
	b.keys(" ")
	b.srv.DropEventStreams()
	b.see("event stream resumed")
	b.keys(" ")
	if e := b.m.evSnap[b.m.evCur]; e.Marker == "" {
		t.Fatalf("the newest entry is not a marker: %+v", e)
	}
	b.keys("enter")
	b.see("it names no object")
	if b.m.focus != PaneEvents || b.m.kind != KindVolumes {
		t.Fatal("a marker row changed the view")
	}
}

func TestJumpToAVanishedObject(t *testing.T) {
	b := newBench(t, benchOpts{})
	gone := strings.Repeat("d", 64)
	b.srv.ResetRequests()
	b.jumpVia("container", "destroy", gone, map[string]string{"name": "long-gone"})
	b.see("Looking for long-gone")
	b.see("long-gone is no longer in the Containers list.")
	if b.m.evJump != nil || b.m.kind != KindContainers {
		t.Fatal("the pending jump outlived its deadline")
	}
	if b.srv.CountRequests("GET", `^/containers/json$`) == 0 {
		t.Fatal("the list was not asked again before giving up")
	}
}

func TestPendingJumpCompletesAfterARefresh(t *testing.T) {
	b := newBench(t, benchOpts{})
	id := b.srv.AddContainer(fakeengine.ContainerSpec{Name: "fresh-one", Image: "alpine:3.20", State: "created"})
	b.emitAndWait("container", "create", id, map[string]string{"name": "fresh-one"})
	b.until("listed", func() bool {
		return slices.ContainsFunc(b.m.snap.Containers, func(c engine.Container) bool { return c.ID == id })
	})

	// The desk's list has not caught up with the event yet.
	b.m.snap.Containers = slices.DeleteFunc(slices.Clone(b.m.snap.Containers), func(c engine.Container) bool { return c.ID == id })
	b.m.dataVersion++
	b.kind(KindImages)
	b.holdOn()
	b.keys("enter")
	if b.m.evJump == nil {
		t.Fatal("no jump is pending for an object the list does not have yet")
	}
	b.until("the row selected", func() bool {
		r, ok := b.m.currentRow()
		return ok && b.m.kind == KindContainers && r.containerID == id
	})
	b.see("Found fresh-one.")
	if b.m.evJump != nil {
		t.Fatal("the jump is still pending after it landed")
	}

	// Leaving the kind cancels a pending jump.
	b.m.snap.Containers = slices.DeleteFunc(slices.Clone(b.m.snap.Containers), func(c engine.Container) bool { return c.ID == id })
	b.m.dataVersion++
	b.keys("4", "enter")
	if b.m.evJump == nil {
		t.Fatal("no jump pending")
	}
	b.kind(KindVolumes)
	if b.m.evJump != nil {
		t.Fatal("leaving the kind did not cancel the pending jump")
	}
	b.run(400 * time.Millisecond)
	if b.m.kind != KindVolumes {
		t.Fatal("a cancelled jump moved the desk")
	}
}

func TestJumpInReadOnlyMode(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.srv.ResetRequests()
	b.jumpVia("network", "connect", b.networkID("staging"), map[string]string{"name": "staging", "container": b.srv.ContainerID("redis-cache")})
	if r := b.highlighted(); b.m.kind != KindNetworks || r.name != "staging" {
		t.Fatalf("read-only jump landed on %q", r.name)
	}
	if muts := b.srv.Mutations(); len(muts) > 0 {
		t.Fatalf("a jump sent %v", muts)
	}
}
