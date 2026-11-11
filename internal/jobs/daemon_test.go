package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// A job reports the daemon it was started on, and keeps it when it ends.
func TestJobRecordsItsDaemon(t *testing.T) {
	m := NewManager()
	defer m.Close()
	gate := make(chan struct{})
	id, err := m.StartOn("ssh://ops@staging", "pull image", "alpine:3.20", func(ctx context.Context, r *Run) error {
		<-gate
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := m.Get(id); j.Daemon != "ssh://ops@staging" || j.State != Running {
		t.Fatalf("running job: daemon %q, state %s", j.Daemon, j.State)
	}
	if got := m.RunningOn("ssh://ops@staging"); len(got) != 1 || got[0].ID != id {
		t.Fatalf("RunningOn = %+v", got)
	}
	if got := m.RunningOn("unix:///var/run/docker.sock"); len(got) != 0 {
		t.Fatalf("a job on another daemon is listed: %+v", got)
	}
	close(gate)
	waitFor(t, "job to finish", func() bool { j, _ := m.Get(id); return j.State == Succeeded })
	if j, _ := m.Get(id); j.Daemon != "ssh://ops@staging" {
		t.Fatalf("finished job: daemon %q", j.Daemon)
	}
	if fin := m.TakeFinished(); len(fin) != 1 || fin[0].Daemon != "ssh://ops@staging" {
		t.Fatalf("TakeFinished = %+v", fin)
	}
	// Start, without a daemon, is unchanged.
	if id := m.Start("x", "y", func(context.Context, *Run) error { return nil }); id == 0 {
		t.Fatal("Start returned no ID")
	}
}

// Removal is refused while a job runs on the daemon, and names it; once the
// daemon is closing, nothing starts on it.
func TestCloseDaemonRefusesWhileAJobRuns(t *testing.T) {
	m := NewManager()
	defer m.Close()
	const d = "tcp://10.0.0.5:2376"
	gate := make(chan struct{})
	id, _ := m.StartOn(d, "pull image", "nginx:1.27", func(ctx context.Context, r *Run) error {
		<-gate
		return nil
	})
	running := m.CloseDaemon(d)
	if len(running) != 1 || running[0].ID != id || running[0].Title != "pull image" || running[0].Target != "nginx:1.27" {
		t.Fatalf("CloseDaemon = %+v, want the pull", running)
	}
	if m.Closing(d) {
		t.Fatal("a refused removal marked the daemon closing")
	}
	close(gate)
	waitFor(t, "job to finish", func() bool { return len(m.RunningOn(d)) == 0 })
	if running := m.CloseDaemon(d); running != nil {
		t.Fatalf("idle daemon: CloseDaemon = %+v", running)
	}
	ran := false
	id2, err := m.StartOn(d, "stop container", "web", func(context.Context, *Run) error { ran = true; return nil })
	if !errors.Is(err, ErrDaemonClosing) {
		t.Fatalf("StartOn a closing daemon: err %v", err)
	}
	j, _ := m.Get(id2)
	if j.State != Failed || j.Err != ErrDaemonClosing.Error() || ran {
		t.Fatalf("refused job: %+v ran=%v", j, ran)
	}
	// Another daemon is not affected, and reopening lets jobs start again.
	if _, err := m.StartOn("unix:///var/run/docker.sock", "a", "b", func(context.Context, *Run) error { return nil }); err != nil {
		t.Fatal(err)
	}
	m.OpenDaemon(d)
	if _, err := m.StartOn(d, "a", "b", func(context.Context, *Run) error { return nil }); err != nil {
		t.Fatal(err)
	}
	m.Wait()
}

// Starting a job and removing its daemon at the same moment: exactly one of
// the two wins, every time.
func TestStartAndRemoveRaceHasOneWinner(t *testing.T) {
	for i := 0; i < 500; i++ {
		m := NewManager()
		const d = "ssh://prod"
		var ran atomic.Bool
		gate := make(chan struct{})
		var wg sync.WaitGroup
		var startErr error
		var refused []Job
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			_, startErr = m.StartOn(d, "pull image", "alpine", func(ctx context.Context, r *Run) error {
				ran.Store(true)
				<-gate
				return nil
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			refused = m.CloseDaemon(d)
		}()
		close(start)
		wg.Wait()
		jobWon := startErr == nil
		removalWon := refused == nil
		if jobWon == removalWon {
			t.Fatalf("round %d: job started=%v (err %v), removal went ahead=%v (refused by %+v)", i, jobWon, startErr, removalWon, refused)
		}
		close(gate)
		m.Close()
		if removalWon && ran.Load() {
			t.Fatalf("round %d: the job ran on a daemon that was removed", i)
		}
		if jobWon && !ran.Load() {
			t.Fatalf("round %d: the job that won never ran", i)
		}
	}
}
