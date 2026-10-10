package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestJobLifecycleAndProgress(t *testing.T) {
	m := NewManager()
	defer m.Close()
	gate := make(chan struct{})
	v0 := m.Version()

	id := m.Start("pull image", "alpine:3.20", func(ctx context.Context, r *Run) error {
		r.Status("Downloading")
		r.Fraction(0.4)
		r.Logf("layer %d of %d", 1, 3)
		<-gate
		return nil
	})
	// Start returns at once; the job runs behind it.
	waitFor(t, "progress", func() bool { j, _ := m.Get(id); return j.Fraction == 0.4 })
	j, ok := m.Get(id)
	if !ok || j.State != Running || j.Title != "pull image" || j.Target != "alpine:3.20" || j.Status != "Downloading" {
		t.Fatalf("running job: %+v", j)
	}
	if len(j.Log) != 1 || j.Log[0] != "layer 1 of 3" {
		t.Fatalf("log: %q", j.Log)
	}
	if m.Version() == v0 {
		t.Fatal("version must advance as the job reports")
	}
	if m.RunningCount() != 1 || len(m.TakeFinished()) != 0 {
		t.Fatal("job should still be running")
	}

	close(gate)
	m.Wait()
	j, _ = m.Get(id)
	if j.State != Succeeded || j.Fraction != 1 || j.Err != "" || j.Finished.IsZero() {
		t.Fatalf("finished job: %+v", j)
	}
	fin := m.TakeFinished()
	if len(fin) != 1 || fin[0].ID != id || len(m.TakeFinished()) != 0 {
		t.Fatalf("TakeFinished: %+v", fin)
	}
}

func TestJobFailureKeepsTheError(t *testing.T) {
	m := NewManager()
	defer m.Close()
	id := m.Start("remove volume", "data", func(ctx context.Context, r *Run) error {
		return errors.New("remove data: volume is in use - [abc123]")
	})
	m.Wait()
	j, _ := m.Get(id)
	if j.State != Failed || j.Err != "remove data: volume is in use - [abc123]" {
		t.Fatalf("failed job: %+v", j)
	}
}

func TestJobPanicBecomesFailure(t *testing.T) {
	m := NewManager()
	defer m.Close()
	id := m.Start("explode", "", func(ctx context.Context, r *Run) error {
		var p *int
		_ = *p
		return nil
	})
	m.Wait()
	j, _ := m.Get(id)
	if j.State != Failed || !strings.Contains(j.Err, "internal error") {
		t.Fatalf("panicking job: %+v", j)
	}
}

func TestJobsRunConcurrently(t *testing.T) {
	m := NewManager()
	defer m.Close()
	gate := make(chan struct{})
	slow := m.Start("slow", "", func(ctx context.Context, r *Run) error { <-gate; return nil })
	fast := m.Start("fast", "", func(ctx context.Context, r *Run) error { return nil })
	// The second job finishes while the first is still running.
	waitFor(t, "fast job", func() bool { j, _ := m.Get(fast); return j.State == Succeeded })
	if j, _ := m.Get(slow); j.State != Running {
		t.Fatalf("slow job: %v", j.State)
	}
	// Newest first.
	if l := m.List(); len(l) != 2 || l[0].ID != fast || l[1].ID != slow {
		t.Fatalf("list order: %+v", l)
	}
	close(gate)
}

func TestCancelMultiStepJob(t *testing.T) {
	m := NewManager()
	defer m.Close()
	stepped := make(chan int, 16)
	proceed := make(chan struct{})
	id := m.Start("prune volumes", "10 volumes", func(ctx context.Context, r *Run) error {
		for i := 1; i <= 10; i++ {
			if err := ctx.Err(); err != nil {
				r.Logf("stopped before volume %d", i)
				return err
			}
			r.Logf("removed volume %d", i)
			r.Steps(i, 10)
			stepped <- i
			<-proceed
		}
		return nil
	})
	for i := 1; i <= 3; i++ {
		<-stepped
		if i < 3 {
			proceed <- struct{}{}
		}
	}
	if !m.Cancel(id) {
		t.Fatal("Cancel on a running job must report true")
	}
	proceed <- struct{}{}
	m.Wait()

	j, _ := m.Get(id)
	if j.State != Cancelled || j.Done != 3 || j.Total != 10 {
		t.Fatalf("cancelled job: state=%v done=%d/%d", j.State, j.Done, j.Total)
	}
	// The output says what was already done and that nothing more was.
	want := []string{"removed volume 1", "removed volume 2", "removed volume 3", "stopped before volume 4"}
	if strings.Join(j.Log, "|") != strings.Join(want, "|") {
		t.Fatalf("log: %q", j.Log)
	}
	if m.Cancel(id) {
		t.Fatal("Cancel on a finished job must report false")
	}
}

func TestContextErrorWithoutCancelIsAFailure(t *testing.T) {
	m := NewManager()
	defer m.Close()
	// A job that returns a timeout nobody asked for failed; it was not cancelled.
	id := m.Start("x", "", func(ctx context.Context, r *Run) error { return context.DeadlineExceeded })
	m.Wait()
	if j, _ := m.Get(id); j.State != Failed {
		t.Fatalf("state %v, want failed", j.State)
	}
}

func TestClearFinishedKeepsRunning(t *testing.T) {
	m := NewManager()
	defer m.Close()
	gate := make(chan struct{})
	defer close(gate)
	running := m.Start("running", "", func(ctx context.Context, r *Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	})
	a := m.Start("a", "", func(ctx context.Context, r *Run) error { return nil })
	b := m.Start("b", "", func(ctx context.Context, r *Run) error { return errors.New("boom") })
	waitFor(t, "two finished", func() bool {
		ja, _ := m.Get(a)
		jb, _ := m.Get(b)
		return ja.State.Finished() && jb.State.Finished()
	})
	if n := m.ClearFinished(); n != 2 {
		t.Fatalf("cleared %d, want 2", n)
	}
	l := m.List()
	if len(l) != 1 || l[0].ID != running || l[0].State != Running {
		t.Fatalf("after clear: %+v", l)
	}
}

func TestRetentionIsBounded(t *testing.T) {
	m := NewManager()
	defer m.Close()
	gate := make(chan struct{})
	first := m.Start("long runner", "", func(ctx context.Context, r *Run) error {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	})
	for i := 0; i < MaxJobs+30; i++ {
		m.Start(fmt.Sprintf("job %d", i), "", func(ctx context.Context, r *Run) error { return nil })
		if i%10 == 0 {
			waitFor(t, "jobs to finish", func() bool { return m.RunningCount() == 1 })
		}
	}
	waitFor(t, "jobs to finish", func() bool { return m.RunningCount() == 1 })
	m.Start("one more", "", func(ctx context.Context, r *Run) error { return nil })
	if n := len(m.List()); n > MaxJobs {
		t.Fatalf("%d jobs retained, bound is %d", n, MaxJobs)
	}
	// The running job is the oldest and must have survived every trim.
	if j, ok := m.Get(first); !ok || j.State != Running {
		t.Fatal("a running job was dropped by retention")
	}
	close(gate)

	// Output is bounded per job too.
	id := m.Start("chatty", "", func(ctx context.Context, r *Run) error {
		for i := 0; i < MaxLogLines+250; i++ {
			r.Logf("line %d", i)
		}
		return nil
	})
	m.Wait()
	j, _ := m.Get(id)
	if len(j.Log) != MaxLogLines || j.LogDropped != 250 || j.Log[len(j.Log)-1] != fmt.Sprintf("line %d", MaxLogLines+249) {
		t.Fatalf("log bound: %d lines, %d dropped", len(j.Log), j.LogDropped)
	}
}

func TestCloseCancelsRunningJobs(t *testing.T) {
	m := NewManager()
	id := m.Start("forever", "", func(ctx context.Context, r *Run) error {
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}
	if j, _ := m.Get(id); j.State != Cancelled {
		t.Fatalf("state %v, want cancelled", j.State)
	}
}

// TestAFailureIsNotRelabelledAsCancelled: pressing cancel on a job that then
// ends with a real error must not hide the error.
func TestAFailureIsNotRelabelledAsCancelled(t *testing.T) {
	m := NewManager()
	defer m.Close()
	release := make(chan struct{})
	id := m.Start("prune images", "3 images", func(ctx context.Context, r *Run) error {
		<-release
		return errors.New("2 of 3 failed (last: image is in use)")
	})
	m.Cancel(id)
	close(release)
	m.Wait()
	j, _ := m.Get(id)
	if j.State != Failed || j.Err != "2 of 3 failed (last: image is in use)" {
		t.Fatalf("job: state=%v err=%q", j.State, j.Err)
	}
}
