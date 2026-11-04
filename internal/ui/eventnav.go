package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/live"
)

// ---- Events pane: the held view and going to an event's object -------------------------
//
// Live, the pane is a tail of the ring. Held, it is a copy of the ring taken
// when the hold began: the rows on screen are that copy's, so they stay put
// at any event rate, and a cursor moves over them. Holding only stops the
// drawing — the hub keeps reading the daemon into the ring, recording
// history and marking lists dirty (docs/DECISIONS.md, round nine).

// holdEvents takes the snapshot and puts the cursor on its newest entry. It
// reports false, and holds nothing, when there is nothing to hold.
func (m *Model) holdEvents() bool {
	first, end := m.hub.Events.Bounds()
	entries, _ := m.hub.Events.Read(first, end)
	if len(entries) == 0 {
		m.setStatus(toneDim, "No events to hold yet.")
		return false
	}
	m.evHeld = true
	m.evSnap = entries
	m.evSnapEnd = end
	m.evCur = len(entries) - 1
	m.evOff = max(len(entries)-max(m.eventHeight, 1), 0)
	return true
}

// resumeEvents drops the snapshot and follows the ring again.
func (m *Model) resumeEvents() {
	m.evHeld = false
	m.evSnap = nil
	m.evCur, m.evOff = 0, 0
}

// eventsNewer is how many entries the ring received since the snapshot,
// including any it has already discarded.
func (m *Model) eventsNewer() uint64 {
	_, end := m.hub.Events.Bounds()
	if end < m.evSnapEnd {
		return 0 // the ring was reset
	}
	return end - m.evSnapEnd
}

// moveEventCursor moves the cursor within the snapshot, stopping at its ends.
func (m *Model) moveEventCursor(delta int) {
	m.evCur = clamp(m.evCur+delta, 0, len(m.evSnap)-1)
}

// readBack is what a key that looks at older events does: hold first if
// live, then move the cursor.
func (m *Model) readBack(delta int) {
	if !m.evHeld && !m.holdEvents() {
		return
	}
	m.moveEventCursor(delta)
}

// eventTarget is where an event leads: a kind and the row that is its actor.
type eventTarget struct {
	kind  Kind
	name  string
	match []func(row) bool // tried in order; the first that accepts a row decides
}

// unlistedTypes are event types for objects the desk has no list for.
var unlistedTypes = map[string]bool{"daemon": true, "plugin": true, "service": true, "node": true, "secret": true, "config": true}

// targetOf resolves an event to the object it is about, or explains why
// there is none.
func targetOf(e live.EventEntry) (eventTarget, string) {
	if e.Marker != "" {
		return eventTarget{}, "That line is about the event stream itself; it names no object."
	}
	ev := e.Event
	id := ev.ID
	switch {
	case unlistedTypes[ev.Type] || eventKinds[ev.Type] == "":
		return eventTarget{}, "The desk does not list " + ev.Type + " objects, so there is no object to go to."
	case id == "":
		return eventTarget{}, "That event names no object."
	}
	switch ev.Type {
	case "container":
		// Exec events carry the container's ID too.
		return eventTarget{kind: KindContainers, name: ev.Name(), match: []func(row) bool{
			func(r row) bool { return r.containerID == id },
		}}, ""
	case "image":
		ref := ev.Attributes["name"]
		name := ref
		if name == "" {
			name = engine.ShortID(id)
		}
		var match []func(row) bool
		if ref != "" {
			match = append(match, func(r row) bool { return r.imageRef == ref })
		}
		if !strings.HasPrefix(id, "sha256:") {
			// A pull's actor is the reference it pulled, not an ID.
			match = append(match, func(r row) bool { return r.imageRef == id })
		}
		match = append(match, func(r row) bool { return r.imageID == id })
		return eventTarget{kind: KindImages, name: name, match: match}, ""
	case "volume":
		return eventTarget{kind: KindVolumes, name: id, match: []func(row) bool{
			func(r row) bool { return r.volume == id },
		}}, ""
	default: // network; connect and disconnect too: the actor is the network
		return eventTarget{kind: KindNetworks, name: ev.Name(), match: []func(row) bool{
			func(r row) bool { return r.networkID == id },
		}}, ""
	}
}

// eventJump is a jump whose object was not in its list yet: an object just
// created can be announced before the debounced refresh lists it.
type eventJump struct {
	eventTarget
	deadline time.Time
}

// jumpToEvent goes to the object of the event under the cursor. It reads
// nothing but lists and changes nothing on the engine.
func (m *Model) jumpToEvent() {
	if !m.evHeld || len(m.evSnap) == 0 {
		m.setStatus(toneDim, "Hold the view first (Space) so the event stays put.")
		return
	}
	t, why := targetOf(m.evSnap[m.evCur])
	if why != "" {
		m.setStatus(toneDim, why)
		return
	}
	m.evJump = nil
	if m.landOn(t.kind, t.name, t.match...) {
		return
	}
	li := listOf(t.kind)
	m.pending |= listDirtyBit[li]
	m.lastStart[li] = time.Time{} // the next frame asks, debounce or not
	m.evJump = &eventJump{eventTarget: t, deadline: time.Now().Add(m.opts.JumpWait)}
	m.setStatus(toneDim, fmt.Sprintf("Looking for %s: refreshing the %s list…", t.name, kindNames[t.kind]))
}

// retryEventJump runs after every change: a pending jump selects its row as
// soon as a rebuilt list has it, and is dropped once the operator has left
// its kind.
func (m *Model) retryEventJump() {
	j := m.evJump
	if j == nil {
		return
	}
	if m.kind != j.kind {
		m.evJump = nil
		return
	}
	if m.selectMatch(j.kind, j.name, j.match...) {
		m.evJump = nil
		if strings.HasPrefix(m.status.text, "Looking for ") {
			m.setStatus(toneDim, "Found "+j.name+".")
		}
	}
}

// expireEventJump gives up on a pending jump whose deadline has passed.
func (m *Model) expireEventJump(now time.Time) {
	j := m.evJump
	if j == nil || now.Before(j.deadline) {
		return
	}
	m.evJump = nil
	if m.kind == j.kind {
		m.setStatus(toneWarn, j.name+" is no longer in the "+kindNames[j.kind]+" list.")
	}
}
