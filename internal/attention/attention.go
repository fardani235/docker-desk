// Package attention decides which containers need a person.
//
// It is pure: it takes a daemon's container list and whatever inspect
// details have been read so far, and returns rows and counts. It has no
// engine, no terminal and no clock, so the rules can be argued with — and
// tested — without a daemon.
//
// # The ladder
//
// Every container is in exactly one class. The first rule that matches
// decides (Classify):
//
//  1. state dead                                  → needs you
//  2. state restarting                            → needs you
//  3. state exited, exit code not read yet        → reading
//     state exited, code ≠ 0 or OOM-killed        → needs you (OOM named)
//     state exited, code 0                        → not a problem
//  4. state paused or removing                    → not a problem
//  5. state created (never started)               → watch
//  6. state running, health unhealthy             → needs you
//  7. state running, restart count not read yet   → reading
//     state running, restart count ≥ FlapThreshold → watch
//  8. state running, health healthy               → fine
//  9. state running, no healthcheck or one still
//     starting; and any state not known here      → unknown
//
// So a paused container whose last probe failed is not a problem (rule 4
// comes before rule 6: it is frozen on purpose), and a running container
// without a healthcheck is unknown, never fine: nothing was measured.
//
// # Reading, and what is never claimed early
//
// Two rules depend on a detail only an inspect gives: an exited container's
// exit code and OOM verdict, and a running container's restart count. Until
// that detail is in, the container is Reading: it is counted as neither fine
// nor as needing anyone. A detail that will not be read at all — the reader
// stopped at its bound, or the inspect failed — leaves the container out of
// every class, counted as not inspected (Counts.NotInspected,
// Counts.InspectFailed). The daemon's STATUS prose ("Exited (137) …") is
// never parsed to fill the gap.
//
// # The three-restart threshold
//
// FlapThreshold is 3: a container that is up now but that its restart policy
// has restarted three or more times (the daemon's RestartCount) is worth a
// look — watch, not needs you, because it is serving at the moment. Three is
// a judgement call, kept as one constant so that it can be tuned.
//
// # Order
//
// Needs-you rows come first: restarting, unhealthy, dead, then failed exits
// newest first. Watch rows follow: repeatedly restarted (most restarts
// first), then never started. Exited containers whose code is still being
// read come last, as rows that say so. Fine, unknown and not-a-problem
// containers are counts only.
package attention

import (
	"sort"
	"time"
)

// FlapThreshold is the restart count from which a running container is
// watched.
const FlapThreshold = 3

// Container states and health words, as the daemon spells them.
const (
	StateCreated    = "created"
	StateRunning    = "running"
	StatePaused     = "paused"
	StateRestarting = "restarting"
	StateRemoving   = "removing"
	StateExited     = "exited"
	StateDead       = "dead"

	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthStarting  = "starting"
)

// Class is where a container stands.
type Class int

const (
	// Reading: the class depends on a detail that has not been read yet.
	Reading Class = iota
	// NeedsYou: restarting, unhealthy, dead, or exited with a failure.
	NeedsYou
	// Watch: running but restarted repeatedly, or created and never started.
	Watch
	// Fine: running and healthy.
	Fine
	// Unknown: running with no healthcheck, or one still starting.
	Unknown
	// NotAProblem: paused, removing, or exited with code 0.
	NotAProblem
	// NotInspected: the detail the class depends on will not be read.
	NotInspected
)

func (c Class) String() string {
	switch c {
	case Reading:
		return "reading"
	case NeedsYou:
		return "needs you"
	case Watch:
		return "watch"
	case Fine:
		return "fine"
	case Unknown:
		return "unknown"
	case NotAProblem:
		return "not a problem"
	case NotInspected:
		return "not inspected"
	}
	return "?"
}

// Signal is the rule that put a container in its class.
type Signal int

const (
	SignalNone Signal = iota
	// Needs you, in display order.
	SignalRestarting
	SignalUnhealthy
	SignalDead
	SignalExited // exited with a non-zero code, or OOM-killed
	// Watch, in display order.
	SignalFlapping // running, restarted FlapThreshold times or more
	SignalCreated  // created, never started
)

// Container is what the daemon's list says about one container.
type Container struct {
	ID      string
	Name    string
	Image   string
	State   string
	Health  string // "" when the daemon reports no healthcheck
	Status  string // the daemon's prose; shown, never parsed
	Project string // compose project, "" for none
	Service string
}

// Detail is what one inspect adds.
type Detail struct {
	ExitCode     int
	OOMKilled    bool
	RestartCount int
	FinishedAt   time.Time
	Error        string // the daemon's own account of a failed start
}

// Skip is why a container's detail will not be read.
type Skip int

const (
	// SkipBound: the reader stopped at its bound before this container.
	SkipBound Skip = iota + 1
	// SkipFailed: the inspect was sent and failed.
	SkipFailed
)

// DecidedByDetail reports whether c's class depends on an inspect: an exited
// container's exit code, a running container's restart count.
func DecidedByDetail(c Container) bool {
	switch c.State {
	case StateExited:
		return true
	case StateRunning:
		return c.Health != HealthUnhealthy
	}
	return false
}

// WantsDetail reports whether an inspect says something about c that the
// view shows: every container DecidedByDetail, and the restarting and dead
// ones, whose rows name the exit code and the restart count.
func WantsDetail(c Container) bool {
	return DecidedByDetail(c) || c.State == StateRestarting || c.State == StateDead
}

// Candidates returns the IDs to inspect, most telling first: exited
// containers (a failure may hide there), then restarting and dead ones (the
// reason of a problem already known), then running ones (the restart
// count). A reader with a bound takes them in this order.
func Candidates(cs []Container) []string {
	rank := func(c Container) int {
		switch c.State {
		case StateExited:
			return 0
		case StateRestarting:
			return 1
		case StateDead:
			return 2
		}
		return 3
	}
	var want []Container
	for _, c := range cs {
		if WantsDetail(c) {
			want = append(want, c)
		}
	}
	sort.SliceStable(want, func(i, j int) bool { return rank(want[i]) < rank(want[j]) })
	out := make([]string, len(want))
	for i, c := range want {
		out[i] = c.ID
	}
	return out
}

// Classify places one container. d is its inspect detail, or nil when none
// has been read.
func Classify(c Container, d *Detail) (Class, Signal) {
	switch c.State {
	case StateDead:
		return NeedsYou, SignalDead
	case StateRestarting:
		return NeedsYou, SignalRestarting
	case StateExited:
		switch {
		case d == nil:
			return Reading, SignalNone
		case d.ExitCode != 0 || d.OOMKilled:
			return NeedsYou, SignalExited
		}
		return NotAProblem, SignalNone
	case StatePaused, StateRemoving:
		return NotAProblem, SignalNone
	case StateCreated:
		return Watch, SignalCreated
	case StateRunning:
		switch {
		case c.Health == HealthUnhealthy:
			return NeedsYou, SignalUnhealthy
		case d == nil:
			return Reading, SignalNone
		case d.RestartCount >= FlapThreshold:
			return Watch, SignalFlapping
		case c.Health == HealthHealthy:
			return Fine, SignalNone
		}
		return Unknown, SignalNone
	}
	// A state this desk has no rule for is not called fine.
	return Unknown, SignalNone
}

// Certain reports whether the list alone — no inspect — says c needs a
// person: dead, restarting, or running and unhealthy. It is the subset of
// NeedsYou that costs nothing to know, and always agrees with Classify.
func Certain(c Container) bool {
	class, sig := Classify(c, nil)
	return class == NeedsYou && sig != SignalExited
}

// CountCertain counts the containers Certain accepts.
func CountCertain(cs []Container) int {
	n := 0
	for _, c := range cs {
		if Certain(c) {
			n++
		}
	}
	return n
}

// Row is one container the view lists.
type Row struct {
	Container
	Class  Class
	Signal Signal
	// Detail is what its inspect said; HasDetail is false until it is read.
	Detail    Detail
	HasDetail bool
}

// Counts are a block's totals. Total is every container of the list; the
// others partition it.
type Counts struct {
	Total       int
	NeedsYou    int
	Watch       int
	Fine        int
	Unknown     int
	NotAProblem int
	// Reading: the class waits for a detail that is on its way.
	Reading int
	// NotInspected: the reader's bound was reached first.
	NotInspected int
	// InspectFailed: the inspect was sent and failed.
	InspectFailed int
}

// Clear reports that nothing is listed and nothing is still open: no
// container needs anyone or is watched, and none is being read.
func (c Counts) Clear() bool { return c.NeedsYou == 0 && c.Watch == 0 && c.Reading == 0 }

// Block is one daemon's answer.
type Block struct {
	// Rows are the needs-you rows, then the watch rows, then the exited
	// containers whose exit code is still being read.
	Rows   []Row
	Counts Counts
}

// Build classifies a daemon's containers. details holds the inspects read so
// far, by container ID; skipped names the containers whose detail will not
// be read, and why. Neither map is modified.
func Build(cs []Container, details map[string]Detail, skipped map[string]Skip) Block {
	b := Block{Counts: Counts{Total: len(cs)}}
	for _, c := range cs {
		r := Row{Container: c}
		var dp *Detail
		if d, ok := details[c.ID]; ok {
			r.Detail, r.HasDetail = d, true
			dp = &r.Detail
		}
		r.Class, r.Signal = Classify(c, dp)
		if r.Class == Reading {
			switch skipped[c.ID] {
			case SkipBound:
				b.Counts.NotInspected++
				continue
			case SkipFailed:
				b.Counts.InspectFailed++
				continue
			}
		}
		switch r.Class {
		case NeedsYou:
			b.Counts.NeedsYou++
			b.Rows = append(b.Rows, r)
		case Watch:
			b.Counts.Watch++
			b.Rows = append(b.Rows, r)
		case Fine:
			b.Counts.Fine++
		case Unknown:
			b.Counts.Unknown++
		case NotAProblem:
			b.Counts.NotAProblem++
		case Reading:
			b.Counts.Reading++
			if c.State == StateExited {
				// It may be a failure: it gets a row that says it is being
				// read. A running container waiting for its restart count
				// is only counted.
				b.Rows = append(b.Rows, r)
			}
		}
	}
	sort.SliceStable(b.Rows, func(i, j int) bool { return less(b.Rows[i], b.Rows[j]) })
	return b
}

// rank is a row's place among the groups of a block.
func rank(r Row) int {
	switch r.Class {
	case NeedsYou:
		return int(r.Signal) // restarting, unhealthy, dead, exited
	case Watch:
		return 10 + int(r.Signal) // flapping, created
	}
	return 100 // still being read
}

func less(a, b Row) bool {
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra < rb
	}
	switch a.Signal {
	case SignalExited:
		// Newest failure first; one with no finish time goes last.
		if !a.Detail.FinishedAt.Equal(b.Detail.FinishedAt) {
			return a.Detail.FinishedAt.After(b.Detail.FinishedAt)
		}
	case SignalFlapping:
		if a.Detail.RestartCount != b.Detail.RestartCount {
			return a.Detail.RestartCount > b.Detail.RestartCount
		}
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}
