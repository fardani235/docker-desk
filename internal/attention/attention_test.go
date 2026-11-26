package attention

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func det(code, restarts int, oom bool) *Detail {
	return &Detail{ExitCode: code, RestartCount: restarts, OOMKilled: oom}
}

// 1.1: every class, and the conflicts the precedence settles.
func TestClassifyLadder(t *testing.T) {
	cases := []struct {
		name   string
		c      Container
		d      *Detail
		class  Class
		signal Signal
	}{
		{"dead", Container{State: StateDead}, nil, NeedsYou, SignalDead},
		{"dead, whatever its health", Container{State: StateDead, Health: HealthHealthy}, det(0, 0, false), NeedsYou, SignalDead},
		{"restarting", Container{State: StateRestarting}, nil, NeedsYou, SignalRestarting},
		{"restarting, detail read", Container{State: StateRestarting}, det(1, 9, false), NeedsYou, SignalRestarting},
		{"exited, code unread", Container{State: StateExited}, nil, Reading, SignalNone},
		{"exited non-zero", Container{State: StateExited}, det(1, 0, false), NeedsYou, SignalExited},
		{"exited 137 by the OOM killer", Container{State: StateExited}, det(137, 0, true), NeedsYou, SignalExited},
		{"exited 0 but OOM-killed", Container{State: StateExited}, det(0, 0, true), NeedsYou, SignalExited},
		{"exited 0", Container{State: StateExited}, det(0, 0, false), NotAProblem, SignalNone},
		{"exited 0 after many restarts", Container{State: StateExited}, det(0, 12, false), NotAProblem, SignalNone},
		{"paused", Container{State: StatePaused}, nil, NotAProblem, SignalNone},
		{"removing", Container{State: StateRemoving}, nil, NotAProblem, SignalNone},
		{"created", Container{State: StateCreated}, nil, Watch, SignalCreated},
		{"running unhealthy", Container{State: StateRunning, Health: HealthUnhealthy}, nil, NeedsYou, SignalUnhealthy},
		{"running healthy, restarts unread", Container{State: StateRunning, Health: HealthHealthy}, nil, Reading, SignalNone},
		{"running healthy", Container{State: StateRunning, Health: HealthHealthy}, det(0, 0, false), Fine, SignalNone},
		{"running healthy, two restarts", Container{State: StateRunning, Health: HealthHealthy}, det(0, FlapThreshold-1, false), Fine, SignalNone},
		{"running healthy, three restarts", Container{State: StateRunning, Health: HealthHealthy}, det(0, FlapThreshold, false), Watch, SignalFlapping},
		{"running starting", Container{State: StateRunning, Health: HealthStarting}, det(0, 0, false), Unknown, SignalNone},
		{"running, a health word not known here", Container{State: StateRunning, Health: "splendid"}, det(0, 0, false), Unknown, SignalNone},
		{"a state not known here", Container{State: "hibernating", Health: HealthHealthy}, det(0, 0, false), Unknown, SignalNone},
		{"no state at all", Container{}, nil, Unknown, SignalNone},

		// The precedence conflicts.
		{"paused + unhealthy: frozen on purpose", Container{State: StatePaused, Health: HealthUnhealthy}, nil, NotAProblem, SignalNone},
		{"exited + unhealthy, code 0: the exit decides", Container{State: StateExited, Health: HealthUnhealthy}, det(0, 0, false), NotAProblem, SignalNone},
		{"exited + unhealthy, code 2: needs you as an exit", Container{State: StateExited, Health: HealthUnhealthy}, det(2, 0, false), NeedsYou, SignalExited},
		{"exited + unhealthy, code unread: still reading", Container{State: StateExited, Health: HealthUnhealthy}, nil, Reading, SignalNone},
		{"running + no healthcheck: unknown, never fine", Container{State: StateRunning}, det(0, 0, false), Unknown, SignalNone},
		{"running + no healthcheck + restarts: watch", Container{State: StateRunning}, det(0, 5, false), Watch, SignalFlapping},
		{"unhealthy + restarts: unhealthy wins", Container{State: StateRunning, Health: HealthUnhealthy}, det(0, 50, false), NeedsYou, SignalUnhealthy},
		{"restarting + unhealthy: restarting wins", Container{State: StateRestarting, Health: HealthUnhealthy}, nil, NeedsYou, SignalRestarting},
		{"dead + unhealthy: dead wins", Container{State: StateDead, Health: HealthUnhealthy}, nil, NeedsYou, SignalDead},
		{"created + unhealthy: created decides", Container{State: StateCreated, Health: HealthUnhealthy}, nil, Watch, SignalCreated},
	}
	for _, tc := range cases {
		class, signal := Classify(tc.c, tc.d)
		if class != tc.class || signal != tc.signal {
			t.Errorf("%s: got %v/%d, want %v/%d", tc.name, class, signal, tc.class, tc.signal)
		}
	}
}

// No state, health and detail puts a container in fine without a healthy
// probe, and Certain never disagrees with Classify.
func TestNothingIsFineWithoutEvidence(t *testing.T) {
	states := []string{StateCreated, StateRunning, StatePaused, StateRestarting, StateRemoving, StateExited, StateDead, "", "weird"}
	healths := []string{"", HealthHealthy, HealthUnhealthy, HealthStarting, "none", "weird"}
	details := []*Detail{nil, det(0, 0, false), det(1, 0, false), det(0, 3, false), det(137, 9, true)}
	for _, s := range states {
		for _, h := range healths {
			c := Container{State: s, Health: h}
			for _, d := range details {
				class, sig := Classify(c, d)
				if class == Fine && (s != StateRunning || h != HealthHealthy || d == nil) {
					t.Errorf("%s/%s/%v is called fine", s, h, d)
				}
				if class == NotInspected {
					t.Errorf("Classify returned NotInspected for %s/%s", s, h)
				}
				if (sig != SignalNone) != (class == NeedsYou || class == Watch) {
					t.Errorf("%s/%s/%v: class %v with signal %d", s, h, d, class, sig)
				}
				if Certain(c) && class != NeedsYou {
					t.Errorf("%s/%s/%v: Certain, but classified %v", s, h, d, class)
				}
				if class == Reading && (!DecidedByDetail(c) || d != nil) {
					t.Errorf("%s/%s/%v: reading, though no detail is awaited", s, h, d)
				}
				if DecidedByDetail(c) && !WantsDetail(c) {
					t.Errorf("%s/%s: decided by a detail nobody wants", s, h)
				}
			}
		}
	}
}

func TestCertainIsTheCheapSubset(t *testing.T) {
	cs := []Container{
		{ID: "a", State: StateRestarting},
		{ID: "b", State: StateRestarting},
		{ID: "c", State: StateRunning, Health: HealthUnhealthy},
		{ID: "d", State: StateDead},
		{ID: "e", State: StateExited},                          // a failure, perhaps: not known without an inspect
		{ID: "f", State: StateRunning, Health: HealthHealthy},  // fine
		{ID: "g", State: StateRunning},                         // unknown
		{ID: "h", State: StatePaused, Health: HealthUnhealthy}, // not a problem
		{ID: "i", State: StateCreated},                         // watch
		{ID: "j", State: StateRunning, Health: HealthStarting},
	}
	if n := CountCertain(cs); n != 4 {
		t.Fatalf("CountCertain = %d, want 4", n)
	}
	if n := CountCertain(nil); n != 0 {
		t.Fatalf("CountCertain(nil) = %d", n)
	}
}

func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// 1.2: the order of a mixed block, and its counts.
func TestBuildOrdersAndCounts(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cs := []Container{
		{ID: "1", Name: "watch-created", State: StateCreated},
		{ID: "2", Name: "exit-old", State: StateExited},
		{ID: "3", Name: "dead", State: StateDead},
		{ID: "4", Name: "fine", State: StateRunning, Health: HealthHealthy},
		{ID: "5", Name: "exit-new", State: StateExited},
		{ID: "6", Name: "unhealthy", State: StateRunning, Health: HealthUnhealthy},
		{ID: "7", Name: "flap-3", State: StateRunning},
		{ID: "8", Name: "restarting", State: StateRestarting},
		{ID: "9", Name: "exit-mid", State: StateExited},
		{ID: "10", Name: "clean-exit", State: StateExited},
		{ID: "11", Name: "paused", State: StatePaused},
		{ID: "12", Name: "no-check", State: StateRunning},
		{ID: "13", Name: "flap-9", State: StateRunning, Health: HealthHealthy},
		{ID: "14", Name: "starting", State: StateRunning, Health: HealthStarting},
	}
	details := map[string]Detail{
		"2":  {ExitCode: 1, FinishedAt: t0.Add(-3 * time.Hour)},
		"5":  {ExitCode: 137, OOMKilled: true, FinishedAt: t0.Add(-time.Minute)},
		"9":  {ExitCode: 2, FinishedAt: t0.Add(-time.Hour)},
		"10": {ExitCode: 0, FinishedAt: t0},
		"4":  {},
		"7":  {RestartCount: 3},
		"12": {},
		"13": {RestartCount: 9},
		"14": {},
		"8":  {ExitCode: 1, RestartCount: 40},
	}
	b := Build(cs, details, nil)
	want := []string{"restarting", "unhealthy", "dead", "exit-new", "exit-mid", "exit-old", "flap-9", "flap-3", "watch-created"}
	if got := names(b.Rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}
	wantCounts := Counts{Total: 14, NeedsYou: 6, Watch: 3, Fine: 1, Unknown: 2, NotAProblem: 2}
	if b.Counts != wantCounts {
		t.Fatalf("counts: got %+v, want %+v", b.Counts, wantCounts)
	}
	// Every needs-you row precedes every watch row.
	seenWatch := false
	for _, r := range b.Rows {
		if r.Class == Watch {
			seenWatch = true
		}
		if r.Class == NeedsYou && seenWatch {
			t.Fatalf("%s (needs you) is listed after a watch row", r.Name)
		}
	}
	// The row carries what the inspect said.
	if r := b.Rows[3]; !r.HasDetail || !r.Detail.OOMKilled || r.Detail.ExitCode != 137 {
		t.Fatalf("the newest failure lost its reason: %+v", r)
	}
	if r := b.Rows[0]; !r.HasDetail || r.Detail.RestartCount != 40 {
		t.Fatalf("the restarting row lost its detail: %+v", r)
	}
	if b.Counts.Clear() {
		t.Fatal("a block with problems is called clear")
	}
}

// The spec's "mixed problems" and "counts" scenarios.
func TestBuildSpecScenarios(t *testing.T) {
	b := Build([]Container{
		{ID: "d", Name: "d", State: StateDead},
		{ID: "u", Name: "u", State: StateRunning, Health: HealthUnhealthy},
		{ID: "w", Name: "w", State: StateCreated},
		{ID: "r", Name: "r", State: StateRestarting},
	}, nil, nil)
	if got := names(b.Rows); !reflect.DeepEqual(got, []string{"r", "u", "d", "w"}) {
		t.Fatalf("restarting, unhealthy, dead, then watch: got %v", got)
	}

	var cs []Container
	details := map[string]Detail{}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("ok%d", i)
		cs = append(cs, Container{ID: id, Name: id, State: StateRunning, Health: HealthHealthy})
		details[id] = Detail{}
	}
	cs = append(cs, Container{ID: "x", Name: "x", State: StateRestarting}, Container{ID: "y", Name: "y", State: StateRunning, Health: HealthUnhealthy})
	b = Build(cs, details, nil)
	if b.Counts.Total != 12 || b.Counts.NeedsYou != 2 || b.Counts.Fine != 10 || len(b.Rows) != 2 {
		t.Fatalf("twelve containers, two needing a person: %+v", b.Counts)
	}
}

func TestBuildEmptyAndClear(t *testing.T) {
	b := Build(nil, nil, nil)
	if len(b.Rows) != 0 || b.Counts != (Counts{}) || !b.Counts.Clear() {
		t.Fatalf("an empty daemon: %+v", b)
	}
	cs := []Container{
		{ID: "a", Name: "a", State: StateRunning, Health: HealthHealthy},
		{ID: "b", Name: "b", State: StateRunning},
		{ID: "c", Name: "c", State: StatePaused},
		{ID: "d", Name: "d", State: StateExited},
	}
	b = Build(cs, map[string]Detail{"a": {}, "b": {}, "d": {}}, nil)
	if len(b.Rows) != 0 || !b.Counts.Clear() {
		t.Fatalf("nothing wrong, but: %+v", b)
	}
	if b.Counts != (Counts{Total: 4, Fine: 1, Unknown: 1, NotAProblem: 2}) {
		t.Fatalf("counts: %+v", b.Counts)
	}
}

func partition(c Counts) int {
	return c.NeedsYou + c.Watch + c.Fine + c.Unknown + c.NotAProblem + c.Reading + c.NotInspected + c.InspectFailed
}

// 1.3: an unread exited container is neither fine nor needing anyone, and
// settles as soon as the answer arrives.
func TestReadingIsNeitherAndSettles(t *testing.T) {
	cs := []Container{
		{ID: "x", Name: "worker", State: StateExited},
		{ID: "r", Name: "web", State: StateRunning, Health: HealthHealthy},
	}
	b := Build(cs, nil, nil)
	if b.Counts.NeedsYou != 0 || b.Counts.Fine != 0 || b.Counts.NotAProblem != 0 || b.Counts.Reading != 2 {
		t.Fatalf("before any answer: %+v", b.Counts)
	}
	// The exited one has a row that says it is being read; the running one
	// is only counted.
	if len(b.Rows) != 1 || b.Rows[0].Name != "worker" || b.Rows[0].Class != Reading || b.Rows[0].HasDetail {
		t.Fatalf("rows before any answer: %+v", b.Rows)
	}
	if b.Counts.Clear() {
		t.Fatal("a block still being read is called clear")
	}

	// A non-zero code: needs you at once, and counted.
	b = Build(cs, map[string]Detail{"x": {ExitCode: 3}}, nil)
	if b.Counts.NeedsYou != 1 || b.Counts.Reading != 1 || b.Rows[0].Class != NeedsYou || b.Rows[0].Signal != SignalExited {
		t.Fatalf("after the exit code: %+v %+v", b.Counts, b.Rows)
	}
	// Code 0: not a problem, and no row.
	b = Build(cs, map[string]Detail{"x": {ExitCode: 0}, "r": {}}, nil)
	if b.Counts != (Counts{Total: 2, Fine: 1, NotAProblem: 1}) || len(b.Rows) != 0 {
		t.Fatalf("after both answers: %+v %+v", b.Counts, b.Rows)
	}
	// The restart count settles the running one as watch.
	b = Build(cs, map[string]Detail{"x": {}, "r": {RestartCount: 4}}, nil)
	if b.Counts.Watch != 1 || b.Counts.Fine != 0 {
		t.Fatalf("after the restart count: %+v", b.Counts)
	}
}

// A detail that will not be read leaves the container out of every class.
func TestSkippedIsNeverClassified(t *testing.T) {
	cs := []Container{
		{ID: "a", Name: "a", State: StateExited},
		{ID: "b", Name: "b", State: StateRunning, Health: HealthHealthy},
		{ID: "c", Name: "c", State: StateExited},
		{ID: "d", Name: "d", State: StateRestarting}, // its class needs no detail
		{ID: "e", Name: "e", State: StateRunning},
	}
	skipped := map[string]Skip{"a": SkipBound, "b": SkipBound, "c": SkipFailed, "d": SkipBound}
	b := Build(cs, nil, skipped)
	want := Counts{Total: 5, NeedsYou: 1, NotInspected: 2, InspectFailed: 1, Reading: 1}
	if b.Counts != want {
		t.Fatalf("got %+v, want %+v", b.Counts, want)
	}
	if got := names(b.Rows); !reflect.DeepEqual(got, []string{"d"}) {
		t.Fatalf("rows: %v", got)
	}
	if partition(b.Counts) != b.Counts.Total {
		t.Fatalf("the counts do not add up: %+v", b.Counts)
	}
	// A detail that did arrive wins over a stale skip.
	b = Build(cs, map[string]Detail{"a": {ExitCode: 9}}, skipped)
	if b.Counts.NeedsYou != 2 || b.Counts.NotInspected != 1 {
		t.Fatalf("a read detail was ignored: %+v", b.Counts)
	}
}

// The counts partition the list, whatever is read.
func TestCountsPartitionTheList(t *testing.T) {
	states := []string{StateCreated, StateRunning, StatePaused, StateRestarting, StateRemoving, StateExited, StateDead, "odd"}
	healths := []string{"", HealthHealthy, HealthUnhealthy, HealthStarting}
	var cs []Container
	details := map[string]Detail{}
	skipped := map[string]Skip{}
	i := 0
	for _, s := range states {
		for _, h := range healths {
			for v := 0; v < 4; v++ {
				id := fmt.Sprintf("c%03d", i)
				cs = append(cs, Container{ID: id, Name: id, State: s, Health: h})
				switch v {
				case 1:
					details[id] = Detail{ExitCode: i % 3, RestartCount: i % 5}
				case 2:
					skipped[id] = SkipBound
				case 3:
					skipped[id] = SkipFailed
				}
				i++
			}
		}
	}
	b := Build(cs, details, skipped)
	if partition(b.Counts) != len(cs) || b.Counts.Total != len(cs) {
		t.Fatalf("%d containers, counts %+v", len(cs), b.Counts)
	}
	listed := 0
	for _, r := range b.Rows {
		if r.Class == NeedsYou || r.Class == Watch {
			listed++
		}
	}
	if listed != b.Counts.NeedsYou+b.Counts.Watch {
		t.Fatalf("%d rows for %d needs-you and %d watch", listed, b.Counts.NeedsYou, b.Counts.Watch)
	}
}

func TestExitedTiesAndZeroTimes(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cs := []Container{
		{ID: "1", Name: "zeta", State: StateExited},
		{ID: "2", Name: "no-time", State: StateExited},
		{ID: "3", Name: "alpha", State: StateExited},
		{ID: "4", Name: "newest", State: StateExited},
	}
	b := Build(cs, map[string]Detail{
		"1": {ExitCode: 1, FinishedAt: t0},
		"2": {ExitCode: 1},
		"3": {ExitCode: 1, FinishedAt: t0},
		"4": {ExitCode: 1, FinishedAt: t0.Add(time.Second)},
	}, nil)
	if got := names(b.Rows); !reflect.DeepEqual(got, []string{"newest", "alpha", "zeta", "no-time"}) {
		t.Fatalf("got %v", got)
	}
}

func TestCandidatesOrder(t *testing.T) {
	cs := []Container{
		{ID: "run1", State: StateRunning},
		{ID: "paused", State: StatePaused},
		{ID: "dead", State: StateDead},
		{ID: "exit1", State: StateExited},
		{ID: "unhealthy", State: StateRunning, Health: HealthUnhealthy},
		{ID: "restart", State: StateRestarting},
		{ID: "created", State: StateCreated},
		{ID: "exit2", State: StateExited},
		{ID: "run2", State: StateRunning, Health: HealthHealthy},
	}
	want := []string{"exit1", "exit2", "restart", "dead", "run1", "run2"}
	if got := Candidates(cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := Candidates(nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestClassNames(t *testing.T) {
	for c, want := range map[Class]string{Reading: "reading", NeedsYou: "needs you", Watch: "watch", Fine: "fine",
		Unknown: "unknown", NotAProblem: "not a problem", NotInspected: "not inspected", Class(99): "?"} {
		if c.String() != want {
			t.Errorf("%d: %q", c, c.String())
		}
	}
}
