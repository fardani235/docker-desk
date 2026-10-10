package live

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/engine"
)

func TestRingKeepsNewestAndStableIndices(t *testing.T) {
	var notify atomic.Uint64
	r := NewRing[int](4, &notify)
	if r.Len() != 0 || r.Version() != 0 {
		t.Fatal("new ring must be empty")
	}
	for i := 0; i < 10; i++ {
		r.Append(i)
	}
	if r.Len() != 4 || r.Cap() != 4 {
		t.Fatalf("len %d cap %d", r.Len(), r.Cap())
	}
	first, end := r.Bounds()
	if first != 6 || end != 10 {
		t.Fatalf("bounds [%d,%d), want [6,10)", first, end)
	}
	if got := r.Tail(3); !reflect.DeepEqual(got, []int{7, 8, 9}) {
		t.Fatalf("tail: %v", got)
	}
	// Absolute indices: entry 8 is entry 8 no matter how much has scrolled out.
	if got, at := r.Read(8, 9); !reflect.DeepEqual(got, []int{8}) || at != 8 {
		t.Fatalf("read [8,9): %v at %d", got, at)
	}
	// A request for discarded entries is clamped, and says where it starts.
	if got, at := r.Read(2, 8); !reflect.DeepEqual(got, []int{6, 7}) || at != 6 {
		t.Fatalf("clamped read: %v at %d", got, at)
	}
	if got, _ := r.Read(20, 30); got != nil {
		t.Fatalf("read beyond end: %v", got)
	}
	if v, ok := r.Last(); !ok || v != 9 {
		t.Fatalf("last: %v %v", v, ok)
	}
	if r.Version() != 10 || notify.Load() != 10 {
		t.Fatalf("version %d notify %d, want 10", r.Version(), notify.Load())
	}

	// AppendMany larger than the capacity keeps only the newest, in order,
	// and still counts everything.
	r.AppendMany([]int{10, 11, 12, 13, 14, 15, 16})
	if got := r.Tail(10); !reflect.DeepEqual(got, []int{13, 14, 15, 16}) {
		t.Fatalf("after oversized AppendMany: %v", got)
	}
	if _, end := r.Bounds(); end != 17 {
		t.Fatalf("total %d, want 17", end)
	}
	r.Reset()
	if r.Len() != 0 {
		t.Fatal("reset did not empty the ring")
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing[int](128, nil)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				r.Append(i)
			}
		}()
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.Tail(40)
				_, _ = r.Bounds()
			}
		}
	}()
	wg.Wait()
	close(stop)
	if _, end := r.Bounds(); end != 20000 || r.Len() != 128 {
		t.Fatalf("end %d len %d", end, r.Len())
	}
}

func lines(r *Ring[string]) []string { return r.Tail(r.Cap()) }

func TestLineWriter(t *testing.T) {
	r := NewRing[string](100, nil)
	w := NewLineWriter(r)

	// Lines may arrive split across writes in any way.
	for _, chunk := range []string{"hel", "lo\nwor", "ld\n", "crlf line\r\n", "partial"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if got := lines(r); !reflect.DeepEqual(got, []string{"hello", "world", "crlf line"}) {
		t.Fatalf("lines: %q", got)
	}
	w.Flush()
	if got := lines(r); got[len(got)-1] != "partial" {
		t.Fatalf("flush: %q", got)
	}

	// A lone carriage return rewinds the line (progress bars).
	r.Reset()
	_, _ = w.Write([]byte("10%\r50%\r100%\n"))
	if got := lines(r); !reflect.DeepEqual(got, []string{"100%"}) {
		t.Fatalf("carriage returns: %q", got)
	}
}

func TestLineWriterBoundsLineLength(t *testing.T) {
	r := NewRing[string](100, nil)
	w := NewLineWriter(r)
	// 10,000 bytes and never a newline.
	_, _ = w.Write([]byte(strings.Repeat("x", 10_000)))
	got := lines(r)
	if len(got) != 4 {
		t.Fatalf("want 4 full lines emitted so far, got %d", len(got))
	}
	for _, l := range got {
		if len(l) != MaxLineBytes {
			t.Fatalf("line length %d, want %d", len(l), MaxLineBytes)
		}
	}

	// Splitting must not cut a multi-byte character in half.
	r.Reset()
	w = NewLineWriter(r)
	_, _ = w.Write([]byte(strings.Repeat("é", 3000) + "\n")) // 6000 bytes
	total := 0
	for _, l := range lines(r) {
		if !utf8.ValidString(l) || strings.ContainsRune(l, utf8.RuneError) {
			t.Fatalf("split produced invalid UTF-8: %q…", l[:20])
		}
		if len(l) > MaxLineBytes {
			t.Fatalf("line too long: %d", len(l))
		}
		total += utf8.RuneCountInString(l)
	}
	if total != 3000 {
		t.Fatalf("characters lost in splitting: %d of 3000", total)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "GET /api 200", "GET /api 200"},
		{"colour is kept and closed", "\x1b[31merror\x1b[0m done", "\x1b[31merror\x1b[0m done\x1b[0m"},
		{"256 colour kept", "\x1b[38;5;196mx", "\x1b[38;5;196mx\x1b[0m"},
		{"cursor movement removed", "a\x1b[2Ab\x1b[10;20Hc", "abc"},
		{"clear screen removed", "\x1b[2J\x1b[Hhello", "hello"},
		{"alt screen removed", "\x1b[?1049hx\x1b[?1049l", "x"},
		{"window title removed", "\x1b]0;pwned\x07ok", "ok"},
		{"clipboard write removed", "\x1b]52;c;ZXZpbA==\x1b\\ok", "ok"},
		{"hyperlink removed", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"reset terminal removed", "\x1bcok", "ok"},
		{"charset selection removed", "\x1b(Bok", "ok"},
		{"unterminated CSI drops the rest", "ok\x1b[31", "ok"},
		{"unterminated OSC drops the rest", "ok\x1b]0;title", "ok"},
		{"bell and backspace removed", "a\x07b\x08c", "abc"},
		{"C1 CSI removed", "a\u009bb", "ab"},
		{"tab expands to the next stop", "ab\tc", "ab      c"},
		{"tab at a stop", "12345678\tx", "12345678        x"},
		{"invalid utf-8 replaced", "a\xffb", "a�b"},
		{"unicode kept", "héllo → 世界", "héllo → 世界"},
		{"DEL removed", "a\x7fb", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize([]byte(tc.in))
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Whatever went in, nothing but SGR may come out as an escape.
			rest := got
			for {
				i := strings.IndexByte(rest, 0x1b)
				if i < 0 {
					break
				}
				end := strings.IndexByte(rest[i:], 'm')
				if end < 0 || !strings.HasPrefix(rest[i:], "\x1b[") {
					t.Fatalf("non-SGR escape survived in %q", got)
				}
				rest = rest[i+end+1:]
			}
			for _, r := range got {
				if r < 0x20 && r != 0x1b {
					t.Fatalf("control character %#x survived in %q", r, got)
				}
			}
		})
	}
}

func raw(at time.Time, cpu, sys, mem, cache, rx, tx, rd, wr uint64) engine.RawStats {
	return engine.RawStats{
		Read: at, CPUTotal: cpu, CPUSystem: sys, OnlineCPUs: 4,
		MemUsage: mem, MemCache: cache, MemLimit: 8 << 30,
		NetRx: rx, NetTx: tx, BlkRead: rd, BlkWrite: wr, Pids: 7,
	}
}

func TestDerive(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	t1 := t0.Add(2 * time.Second)

	// Over 2 s the host's 4 CPUs accumulate 8 s of CPU time; the container
	// used 1 s of it → 50% of one core by Docker's formula
	// (1/8 × 4 CPUs × 100).
	prev := raw(t0, 10e9, 1000e9, 500<<20, 100<<20, 1000, 2000, 0, 0)
	cur := raw(t1, 11e9, 1008e9, 600<<20, 100<<20, 5000, 2600, 4096, 8192)
	s, ok := Derive(prev, cur)
	if !ok {
		t.Fatal("pair should be usable")
	}
	if got := s.CPUPercent; got < 49.999 || got > 50.001 {
		t.Fatalf("cpu %.3f%%, want 50", got)
	}
	if s.MemUsed != 500<<20 || s.MemLimit != 8<<30 {
		t.Fatalf("memory used %d (cache must be subtracted), limit %d", s.MemUsed, s.MemLimit)
	}
	if want := float64(500<<20) / float64(8<<30) * 100; s.MemPercent != want {
		t.Fatalf("mem%% %v, want %v", s.MemPercent, want)
	}
	if s.NetRxRate != 2000 || s.NetTxRate != 300 || s.BlkRdRate != 2048 || s.BlkWrRate != 4096 {
		t.Fatalf("rates: %+v", s)
	}
	if s.Pids != 7 || !s.Time.Equal(t1) || s.NetRx != 5000 {
		t.Fatalf("carried fields: %+v", s)
	}

	// Counters that go backwards (container restarted) give zero, not a
	// huge unsigned difference.
	back, ok := Derive(cur, raw(t1.Add(time.Second), 1e9, 1012e9, 1, 0, 10, 10, 0, 0))
	if !ok || back.CPUPercent != 0 || back.NetRxRate != 0 || back.BlkWrRate != 0 {
		t.Fatalf("counter reset: %+v", back)
	}
	// Same or reversed timestamps are unusable.
	if _, ok := Derive(cur, cur); ok {
		t.Fatal("identical timestamps must be rejected")
	}
	if _, ok := Derive(cur, prev); ok {
		t.Fatal("reversed timestamps must be rejected")
	}
	// No host CPU delta → 0%, not NaN or Inf.
	flat, _ := Derive(prev, raw(t1, 11e9, 1000e9, 1, 0, 0, 0, 0, 0))
	if flat.CPUPercent != 0 {
		t.Fatalf("zero system delta: %v", flat.CPUPercent)
	}
}

func BenchmarkLineWriter(b *testing.B) {
	r := NewRing[string](LogLines, nil)
	w := NewLineWriter(r)
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "line %d level=info msg=\"request handled\" path=/api/items status=200\n", i)
	}
	chunk := []byte(sb.String())
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = w.Write(chunk)
	}
	b.ReportMetric(float64(b.N)*500/b.Elapsed().Seconds(), "lines/s")
}
