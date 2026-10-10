// Package jobs runs operations in the background and keeps a bounded ledger
// of them: what was asked, how far it got, how it ended and everything it
// reported.
//
// Every mutation the desk sends to the daemon runs as a job (see
// docs/DECISIONS.md, D10), so the desk never blocks on the daemon and the
// Jobs pane doubles as a record of everything the desk did.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// State is where a job is in its life.
type State int

const (
	Running State = iota
	Succeeded
	Failed
	Cancelled
)

func (s State) String() string {
	switch s {
	case Running:
		return "running"
	case Succeeded:
		return "succeeded"
	case Failed:
		return "failed"
	case Cancelled:
		return "cancelled"
	}
	return "unknown"
}

// Finished reports whether the job has ended, one way or another.
func (s State) Finished() bool { return s != Running }

const (
	// MaxJobs is the number of jobs kept; the oldest finished ones go first.
	MaxJobs = 50
	// MaxLogLines is the number of output lines kept per job.
	MaxLogLines = 500
)

// Job is a point-in-time copy of a job's state.
type Job struct {
	ID       int
	Title    string // what it does, e.g. "stop container"
	Target   string // what it does it to, e.g. "shop-web-1"
	State    State
	Started  time.Time
	Finished time.Time
	// Done and Total count objects for multi-object jobs (Total 0 = n/a).
	Done, Total int
	// Fraction is progress in [0,1] when the operation reports one, else -1.
	Fraction float64
	// Status is the operation's latest one-line status.
	Status string
	// Err is the error the job ended with.
	Err string
	// Log is everything the job reported; only filled by Get.
	Log []string
	// LogDropped counts lines that scrolled out of Log.
	LogDropped int
}

// Elapsed returns how long the job ran, or has been running.
func (j Job) Elapsed() time.Duration {
	if j.State.Finished() {
		return j.Finished.Sub(j.Started)
	}
	return time.Since(j.Started)
}

// Func is the body of a job. Returning the context's error after
// cancellation marks the job cancelled; any other error marks it failed.
type Func func(ctx context.Context, run *Run) error

type job struct {
	Job
	cancel          context.CancelFunc
	cancelRequested bool
}

// Run is a job's handle for reporting progress. All methods are safe to call
// from the job's goroutine at any rate; they never block on the UI.
type Run struct {
	m *Manager
	j *job
}

// Logf appends a line to the job's output.
func (r *Run) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.m.mu.Lock()
	if len(r.j.Log) >= MaxLogLines {
		r.j.Log = r.j.Log[1:]
		r.j.LogDropped++
	}
	r.j.Log = append(r.j.Log, line)
	r.m.mu.Unlock()
	r.m.version.Add(1)
}

// Status sets the one-line status shown next to the job.
func (r *Run) Status(s string) {
	r.m.mu.Lock()
	changed := r.j.Status != s
	r.j.Status = s
	r.m.mu.Unlock()
	if changed {
		r.m.version.Add(1)
	}
}

// Steps reports that done of total objects have been processed.
func (r *Run) Steps(done, total int) {
	r.m.mu.Lock()
	r.j.Done, r.j.Total = done, total
	r.m.mu.Unlock()
	r.m.version.Add(1)
}

// Fraction reports progress in [0,1].
func (r *Run) Fraction(f float64) {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	r.m.mu.Lock()
	changed := r.j.Fraction != f
	r.j.Fraction = f
	r.m.mu.Unlock()
	if changed {
		r.m.version.Add(1)
	}
}

// Manager owns the jobs.
type Manager struct {
	mu       sync.Mutex
	jobs     []*job // oldest first
	nextID   int
	finished []int // IDs finished since the last TakeFinished
	wg       sync.WaitGroup
	version  atomic.Uint64
	ctx      context.Context
	stop     context.CancelFunc
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{ctx: ctx, stop: cancel}
}

// Version changes whenever any job's visible state changes.
func (m *Manager) Version() uint64 { return m.version.Load() }

// Start runs fn in the background and returns the job's ID immediately.
func (m *Manager) Start(title, target string, fn Func) int {
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	m.nextID++
	j := &job{Job: Job{ID: m.nextID, Title: title, Target: target, State: Running, Started: time.Now(), Fraction: -1}, cancel: cancel}
	m.jobs = append(m.jobs, j)
	m.trimLocked()
	m.mu.Unlock()
	m.version.Add(1)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := runProtected(ctx, fn, &Run{m: m, j: j})
		m.mu.Lock()
		j.Finished = time.Now()
		switch {
		case err == nil:
			j.State = Succeeded
			if j.Total > 0 {
				j.Done = j.Total
			}
			if j.Fraction >= 0 {
				j.Fraction = 1
			}
		case j.cancelRequested && errors.Is(err, context.Canceled):
			// Only a job that actually stopped because it was cancelled is
			// "cancelled". One that failed on its own just before or after
			// the cancel keeps its error.
			j.State = Cancelled
			j.Err = "cancelled"
		default:
			j.State = Failed
			j.Err = err.Error()
		}
		m.finished = append(m.finished, j.ID)
		m.mu.Unlock()
		m.version.Add(1)
	}()
	return j.ID
}

// runProtected turns a panic in a job into a failed job instead of taking
// the whole desk (and the terminal state) down with it.
func runProtected(ctx context.Context, fn Func, run *Run) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn(ctx, run)
}

// trimLocked drops the oldest finished jobs beyond MaxJobs. Running jobs are
// never dropped.
func (m *Manager) trimLocked() {
	for len(m.jobs) > MaxJobs {
		dropped := false
		for i, j := range m.jobs {
			if j.State.Finished() {
				m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return
		}
	}
}

// Cancel asks a running job to stop. It reports whether the job was running.
func (m *Manager) Cancel(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id && j.State == Running {
			j.cancelRequested = true
			j.cancel()
			return true
		}
	}
	return false
}

// ClearFinished removes finished jobs and returns how many were removed.
func (m *Manager) ClearFinished() int {
	m.mu.Lock()
	kept := m.jobs[:0]
	n := 0
	for _, j := range m.jobs {
		if j.State.Finished() {
			n++
			continue
		}
		kept = append(kept, j)
	}
	m.jobs = kept
	m.mu.Unlock()
	if n > 0 {
		m.version.Add(1)
	}
	return n
}

// List returns all jobs, newest first, without their output.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		j := m.jobs[i].Job
		j.Log = nil
		out = append(out, j)
	}
	return out
}

// Get returns one job with its output.
func (m *Manager) Get(id int) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id {
			out := j.Job
			out.Log = append([]string(nil), j.Log...)
			return out, true
		}
	}
	return Job{}, false
}

// RunningCount returns the number of jobs still running.
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if j.State == Running {
			n++
		}
	}
	return n
}

// TakeFinished returns the jobs that finished since the previous call, in
// the order they finished.
func (m *Manager) TakeFinished() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.finished) == 0 {
		return nil
	}
	var out []Job
	for _, id := range m.finished {
		for _, j := range m.jobs {
			if j.ID == id {
				c := j.Job
				c.Log = nil
				out = append(out, c)
			}
		}
	}
	m.finished = nil
	return out
}

// Wait blocks until every job started so far has finished.
func (m *Manager) Wait() { m.wg.Wait() }

// Close cancels every running job and waits for them.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.State == Running {
			j.cancelRequested = true
		}
	}
	m.mu.Unlock()
	m.stop()
	m.wg.Wait()
}
