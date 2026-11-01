package fakeengine

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
)

const (
	fakeCPUs     = 4
	fakeMemTotal = 16 << 30
	logRetention = 5000
)

// ---- logs -----------------------------------------------------------------------

type logEntry struct {
	stream byte // 1 stdout, 2 stderr
	text   string
}

// logBuffer holds a container's recent output and feeds followers.
type logBuffer struct {
	mu        sync.Mutex
	lines     []logEntry
	followers map[chan []logEntry]struct{}
	written   uint64
}

func newLogBuffer() *logBuffer {
	return &logBuffer{followers: map[chan []logEntry]struct{}{}}
}

func (b *logBuffer) append(stream byte, text string) {
	b.appendBatch([]logEntry{{stream, text}})
}

func (b *logBuffer) appendBatch(batch []logEntry) {
	b.mu.Lock()
	b.lines = append(b.lines, batch...)
	if len(b.lines) > 2*logRetention {
		b.lines = append([]logEntry(nil), b.lines[len(b.lines)-logRetention:]...)
	}
	b.written += uint64(len(batch))
	for ch := range b.followers {
		select {
		case ch <- batch:
		default: // a follower that cannot keep up loses the batch
		}
	}
	b.mu.Unlock()
}

// follow returns the last tail lines (all when tail < 0) and, when subscribe
// is set, a channel of everything written afterwards.
func (b *logBuffer) follow(tail int, subscribe bool) ([]logEntry, chan []logEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := b.lines
	if len(lines) > logRetention {
		lines = lines[len(lines)-logRetention:]
	}
	if tail >= 0 && tail < len(lines) {
		lines = lines[len(lines)-tail:]
	}
	snap := append([]logEntry(nil), lines...)
	if !subscribe {
		return snap, nil
	}
	ch := make(chan []logEntry, 256)
	b.followers[ch] = struct{}{}
	return snap, ch
}

func (b *logBuffer) unfollow(ch chan []logEntry) {
	b.mu.Lock()
	delete(b.followers, ch)
	b.mu.Unlock()
}

// closeFollowers ends every follow stream, as happens when a container exits.
func (b *logBuffer) closeFollowers() {
	b.mu.Lock()
	for ch := range b.followers {
		close(ch)
		delete(b.followers, ch)
	}
	b.mu.Unlock()
}

// generateLogs writes LogRate lines per second until the run ends.
func (s *Server) generateLogs(c *Container, done chan struct{}) {
	defer s.wg.Done()
	const tick = 20 * time.Millisecond
	t := time.NewTicker(tick)
	defer t.Stop()
	var n uint64
	var carry float64
	last := time.Now()
	for {
		select {
		case <-done:
			return
		case <-s.closed:
			return
		case now := <-t.C:
			s.mu.Lock()
			rate, state, text := c.LogRate, c.State, c.LogLine
			s.mu.Unlock()
			if rate <= 0 {
				return
			}
			elapsed := max(now.Sub(last).Seconds(), 0) // a tick's time can trail the clock read before it
			last = now
			if state != "running" {
				continue
			}
			carry += elapsed * float64(rate)
			count := int(carry)
			carry -= float64(count)
			if count <= 0 {
				continue
			}
			batch := make([]logEntry, count)
			for i := range batch {
				n++
				stream := byte(1)
				if n%17 == 0 {
					stream = 2
				}
				if text != "" {
					batch[i] = logEntry{stream, text + " " + strconv.FormatUint(n, 10)}
				} else {
					batch[i] = logEntry{stream, "line " + strconv.FormatUint(n, 10) + " level=info msg=\"request handled\" path=/api/items status=200"}
				}
			}
			c.logs.appendBatch(batch)
		}
	}
}

// LinesWritten returns how many log lines a container has written in total.
func (s *Server) LinesWritten(ref string) uint64 {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	s.mu.Unlock()
	if c == nil {
		return 0
	}
	c.logs.mu.Lock()
	defer c.logs.mu.Unlock()
	return c.logs.written
}

// LogFollowers returns the number of open follow streams on a container.
func (s *Server) LogFollowers(ref string) int {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	s.mu.Unlock()
	if c == nil {
		return 0
	}
	c.logs.mu.Lock()
	defer c.logs.mu.Unlock()
	return len(c.logs.followers)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, ref string) {
	q := r.URL.Query()
	follow := boolParam(r, "follow")
	tail := -1
	if v := q.Get("tail"); v != "" && v != "all" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tail = n
		}
	}
	wantOut, wantErr := boolParam(r, "stdout"), boolParam(r, "stderr")

	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	tty := c.Tty
	running := c.active()
	logs := c.logs
	s.mu.Unlock()
	if !wantOut && !wantErr {
		httpError(w, http.StatusBadRequest, "Bad parameters: you must choose at least one stream")
		return
	}

	snap, ch := logs.follow(tail, follow && running)
	if ch != nil {
		defer logs.unfollow(ch)
	}

	if tty {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	} else {
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
	}
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 64<<10)

	var hdr [8]byte
	write := func(batch []logEntry) error {
		for _, e := range batch {
			if (e.stream == 1 && !wantOut) || (e.stream == 2 && !wantErr) {
				continue
			}
			if tty {
				if _, err := bw.WriteString(e.text + "\r\n"); err != nil {
					return err
				}
				continue
			}
			hdr[0] = e.stream
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(e.text)+1))
			if _, err := bw.Write(hdr[:]); err != nil {
				return err
			}
			if _, err := bw.WriteString(e.text); err != nil {
				return err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	if write(snap) != nil || ch == nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		case batch, ok := <-ch:
			if !ok {
				return // the container exited
			}
			if write(batch) != nil {
				return
			}
		}
	}
}

// ---- stats ----------------------------------------------------------------------

// profile is a container's deterministic resource behaviour.
type profile struct {
	cpu    float64 // average cores
	period float64 // seconds
	mem    float64 // bytes
	rx, tx float64 // bytes/s
	rd, wr float64 // bytes/s
	pids   uint64
}

func profileOf(name string) profile {
	h := fnv.New64a()
	h.Write([]byte(name))
	v := h.Sum64()
	f := func(shift uint, lo, hi float64) float64 {
		x := float64((v>>shift)&0xff) / 255
		return lo + x*(hi-lo)
	}
	return profile{
		cpu:    f(0, 0.02, 0.9),
		period: f(8, 12, 40),
		mem:    f(16, 24<<20, 900<<20),
		rx:     f(24, 2_000, 400_000),
		tx:     f(32, 1_000, 150_000),
		rd:     f(40, 0, 2_000_000),
		wr:     f(48, 0, 800_000),
		pids:   3 + (v>>56)%40,
	}
}

// integral of rate*(1+a*sin(2πt/p)) from 0 to e.
func wave(rate, a, p, e float64) float64 {
	w := 2 * math.Pi / p
	return rate * (e - a/w*(math.Cos(w*e)-1))
}

func statsAt(c *Container, at time.Time, interval time.Duration) container.StatsResponse {
	p := profileOf(c.Name)
	sample := func(t time.Time) (container.CPUStats, float64) {
		e := t.Sub(c.StartedAt).Seconds()
		if e < 0 {
			e = 0
		}
		return container.CPUStats{
			CPUUsage: container.CPUUsage{
				TotalUsage:        uint64(wave(p.cpu, 0.8, p.period, e) * 1e9),
				UsageInUsermode:   uint64(wave(p.cpu, 0.8, p.period, e) * 0.7e9),
				UsageInKernelmode: uint64(wave(p.cpu, 0.8, p.period, e) * 0.3e9),
			},
			SystemUsage: uint64(1e15 + e*fakeCPUs*1e9),
			OnlineCPUs:  fakeCPUs,
		}, e
	}
	cur, e := sample(at)
	prev, _ := sample(at.Add(-interval))
	mem := p.mem * (1 + 0.3*math.Sin(2*math.Pi*e/(p.period*1.7)))
	return container.StatsResponse{
		ID:          c.ID,
		Name:        "/" + c.Name,
		Read:        at,
		PreRead:     at.Add(-interval),
		CPUStats:    cur,
		PreCPUStats: prev,
		MemoryStats: container.MemoryStats{
			Usage: uint64(mem),
			Limit: fakeMemTotal,
			Stats: map[string]uint64{"inactive_file": uint64(mem * 0.1), "file": uint64(mem * 0.15), "anon": uint64(mem * 0.8)},
		},
		Networks: map[string]container.NetworkStats{
			"eth0": {
				RxBytes: uint64(wave(p.rx, 0.6, p.period*0.7, e)),
				TxBytes: uint64(wave(p.tx, 0.6, p.period*0.9, e)),
			},
		},
		BlkioStats: container.BlkioStats{
			IoServiceBytesRecursive: []container.BlkioStatEntry{
				{Major: 8, Minor: 0, Op: "read", Value: uint64(wave(p.rd, 0.9, p.period*1.3, e))},
				{Major: 8, Minor: 0, Op: "write", Value: uint64(wave(p.wr, 0.9, p.period*1.1, e))},
			},
		},
		PidsStats: container.PidsStats{Current: p.pids},
		NumProcs:  0,
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, ref string) {
	stream := true
	if v := r.URL.Query().Get("stream"); v != "" {
		stream = boolParam(r, "stream")
	}
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	interval := s.StatsInterval
	running := c.active()
	done := c.runDone
	var first container.StatsResponse
	if running {
		first = statsAt(c, time.Now(), interval)
	} else {
		first = container.StatsResponse{ID: c.ID, Name: "/" + c.Name, Read: time.Now()}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	if enc.Encode(first) != nil {
		return
	}
	if fl != nil {
		fl.Flush()
	}
	if !stream || !running {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		case <-done:
			return // the container exited
		case now := <-t.C:
			s.mu.Lock()
			if c.statsStalled {
				s.mu.Unlock()
				continue
			}
			sample := statsAt(c, now, interval)
			s.mu.Unlock()
			if enc.Encode(sample) != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// StallStats makes a container's statistics stream stop delivering samples
// (the connection stays open), or resume.
func (s *Server) StallStats(ref string, stalled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.findContainerLocked(ref); c != nil {
		c.statsStalled = stalled
	}
}

// dropAllStreams severs hijacked connections, which http.Server.Close does
// not know about.
func (s *Server) dropAllStreams() {
	s.mu.Lock()
	for _, e := range s.execs {
		e.kill(137)
	}
	s.mu.Unlock()
}
