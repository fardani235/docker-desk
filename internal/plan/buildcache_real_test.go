package plan

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

// TestRealDaemonBuildCacheReadOnly builds the build-cache preview from a
// real daemon's account and checks that the prune is refused. The engine is
// read-only — the wrapper and the HTTP hook below it both refuse anything
// but GET/HEAD — so nothing on the daemon can change. Opt-in:
// DOCKER_DESK_REAL_HOST (make real-readonly-check).
func TestRealDaemonBuildCacheReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if !engine.IsReadOnly(eng) {
		t.Fatal("engine is not read-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	du, err := eng.DiskUsage(ctx)
	if err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	for _, all := range []bool{false, true} {
		p := BuildCachePrune(du, all, time.Now())
		for _, c := range p.BuildCache.Candidates {
			if c.InUse {
				t.Fatalf("an in-use record is a candidate: %+v", c)
			}
			if !all && (c.Type == "internal" || c.Type == "frontend") {
				t.Fatalf("the default scope offers a %s record", c.Type)
			}
		}
		t.Logf("all=%v: %d candidate(s), %d kept in use, %d excluded; runnable=%v; daemon reclaimable %d B",
			all, len(p.BuildCache.Candidates), p.BuildCache.Kept, p.BuildCache.Excluded, p.Runnable(), p.BuildCache.Reclaimable)
	}
	if _, err := eng.BuildCachePrune(ctx, false); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("read-only build-cache prune: %v", err)
	}
}
