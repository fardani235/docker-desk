package ui

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/live"
)

func floodSeed(rate int) func(*fakeengine.Server) {
	return func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "aa-flood", Image: "app:1", LogRate: rate})
		for i := 0; i < 8; i++ {
			s.AddContainer(fakeengine.ContainerSpec{Name: fmt.Sprintf("svc-%d", i), Image: "app:1", LogRate: 50})
		}
		s.AddContainer(fakeengine.ContainerSpec{Name: "zz-stopped", Image: "app:1", State: "exited"})
	}
}

func percentile(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

// TestFloodDoesNotStarveTheDesk is the responsiveness proof at the model
// level. A container floods its log (≥ 50,000 lines/s asked for, far more
// delivered) while nine statistics streams and the event stream run. It
// checks the four bounds the design promises:
//
//   - key handling plus a full redraw stays far below 100 ms;
//   - frames are rendered at the frame rate, not per line;
//   - stream data does not travel through the message queue at all;
//   - buffers stay at their fixed size.
func TestFloodDoesNotStarveTheDesk(t *testing.T) {
	const (
		rate  = 300_000
		frame = 50 * time.Millisecond // the production frame interval
	)
	b := newBench(t, benchOpts{seed: floodSeed(rate), frame: frame, stats: 100 * time.Millisecond})
	b.m.focus = PaneList
	b.selectRow("aa-flood")
	b.keys("]")
	b.see("[Logs]", "● following")
	b.until("statistics for every running container", func() bool { return b.m.hub.StatsStreams() == 9 })

	// --- a quiet second of pure streaming: count frames and queue traffic.
	ring, _ := b.m.hub.Log()
	linesBefore := b.srv.LinesWritten("aa-flood")
	_, seenBefore := ring.Bounds()
	framesBefore, stepsBefore := b.m.Frames(), b.steps
	const window = 1500 * time.Millisecond
	start := time.Now()
	deadline := time.After(window)
loop:
	for {
		select {
		case msg := <-b.msgs:
			b.step(msg)
			_ = b.m.View() // the runtime asks for a frame after every message
		case <-deadline:
			break loop
		}
	}
	elapsed := time.Since(start)
	written := b.srv.LinesWritten("aa-flood") - linesBefore
	_, seenAfter := ring.Bounds()
	ingested := seenAfter - seenBefore
	frames, steps := b.m.Frames()-framesBefore, b.steps-stepsBefore

	perSec := float64(ingested) / elapsed.Seconds()
	t.Logf("flood: the container wrote %.0f lines/s; the desk ingested %.0f lines/s", float64(written)/elapsed.Seconds(), perSec)
	t.Logf("in %s: %d frames rendered, %d messages through the queue", elapsed.Round(time.Millisecond), frames, steps)
	if perSec < 50_000 {
		if !raceEnabled {
			t.Fatalf("only %.0f lines/s reached the desk; the test proves nothing below 50,000", perSec)
		}
		t.Logf("(race detector on: throughput is not asserted, the bounds below still are)")
	}
	maxFrames := uint64(elapsed/frame) + 3
	if frames > maxFrames {
		t.Fatalf("%d frames in %s; the frame rate bound is %d", frames, elapsed, maxFrames)
	}
	if frames < 5 {
		t.Fatalf("only %d frames in %s: the log view is not updating", frames, elapsed)
	}
	// Frame ticks, a few list refreshes and detail loads — nothing per line.
	if maxSteps := int(elapsed/frame) + 30; steps > maxSteps {
		t.Fatalf("%d messages for %d log lines: stream data is travelling through the message queue (bound %d)", steps, ingested, maxSteps)
	}

	// --- keys under flood: Update + a forced full redraw.
	var lat []time.Duration
	for i := 0; i < 60; i++ {
		key := "tab"
		if i%3 == 1 {
			key = "down"
		} else if i%3 == 2 {
			key = "up"
		}
		t0 := time.Now()
		b.step(keyMsg(key))
		_ = b.m.View()
		lat = append(lat, time.Since(t0))
		b.run(5 * time.Millisecond)
	}
	p50, p95, worst := percentile(lat, 0.5), percentile(lat, 0.95), percentile(lat, 1)
	t.Logf("key → state + redraw under flood: p50 %s, p95 %s, max %s", p50, p95, worst)
	if worst > 100*time.Millisecond {
		t.Fatalf("a key took %s to take effect under flood (bound 100 ms)", worst)
	}

	// --- typing in the focused pane while the noisy stream is elsewhere.
	b.m.focus = PaneList
	b.keys("/")
	b.typeText("svc-3")
	b.keys("enter")
	b.see("(1 of 10)", "filter: svc-3")
	b.keys("esc")

	// --- memory: buffers are at their bound and not beyond.
	b.selectRow("aa-flood")
	b.see("● following", "older lines scrolled out")
	ring, _ = b.m.hub.Log()
	b.until("buffer to fill", func() bool { return ring.Len() == live.LogLines })
	first, end := ring.Bounds()
	if ring.Len() != live.LogLines || end-first != live.LogLines || first == 0 {
		t.Fatalf("log buffer: len %d, bounds [%d,%d), bound %d", ring.Len(), first, end, live.LogLines)
	}
	for _, c := range b.m.snap.Containers {
		if h := b.m.hub.StatsHistory(c.ID, 1000); len(h) > live.StatsHistory {
			t.Fatalf("stats history for %s has %d samples, bound %d", c.Name, len(h), live.StatsHistory)
		}
	}
	b.assertFrameGeometry()
}

// TestLongLinesAreBounded: megabytes without a line break.
func TestLongLinesAreBounded(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "blob", Image: "app:1"})
	}})
	b.m.focus = PaneList
	b.keys("]")
	b.see("● following")
	huge := make([]byte, 3<<20)
	for i := range huge {
		huge[i] = 'x'
	}
	b.srv.WriteLog("blob", 1, string(huge))
	ring, _ := b.m.hub.Log()
	b.until("the blob to arrive", func() bool { _, end := ring.Bounds(); return end > 1000 })
	b.settle()
	for _, l := range ring.Tail(live.LogLines) {
		if len(l) > live.MaxLineBytes {
			t.Fatalf("a %d-byte line was kept; bound is %d", len(l), live.MaxLineBytes)
		}
	}
	if n := ring.Len(); n > live.LogLines {
		t.Fatalf("%d lines kept, bound %d", n, live.LogLines)
	}
	b.assertFrameGeometry()
}

// TestIdleDeskDoesNotRedraw: with nothing streaming and no key pressed, no
// frame is rendered at all.
func TestIdleDeskDoesNotRedraw(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "off-1", Image: "app:1", State: "exited"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "off-2", Image: "app:1", State: "exited"})
	}})
	b.run(200 * time.Millisecond)
	_ = b.m.View()
	before := b.m.Frames()
	deadline := time.After(500 * time.Millisecond)
	ticks := 0
loop:
	for {
		select {
		case msg := <-b.msgs:
			b.step(msg)
			_ = b.m.View()
			ticks++
		case <-deadline:
			break loop
		}
	}
	if ticks < 50 {
		t.Fatalf("only %d frame ticks in 500 ms; the test did not exercise the tick", ticks)
	}
	if got := b.m.Frames() - before; got != 0 {
		t.Fatalf("an idle desk rendered %d frames in 500 ms (%d ticks)", got, ticks)
	}
	// One event → exactly one new frame.
	b.srv.Emit("container", "top", b.srv.ContainerID("off-1"), map[string]string{"name": "off-1"})
	b.until("the event", func() bool { _ = b.m.View(); return b.m.Frames() > before })
}

// BenchmarkFrame measures one full redraw of the desk while a log flood is
// being ingested: the cost that bounds how fast the screen can follow.
func BenchmarkFrame(b *testing.B) {
	bn := newBench(b, benchOpts{seed: floodSeed(200_000), frame: 50 * time.Millisecond, width: 180, height: 50})
	bn.m.focus = PaneList
	bn.selectRow("aa-flood")
	bn.keys("]")
	bn.see("● following")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bn.m.dirty = true
		_ = bn.m.View()
	}
}
