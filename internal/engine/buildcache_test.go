package engine

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func seedBuildCacheScopes(s *fakeengine.Server) {
	s.SeedDemo()
	s.SeedBuildCacheScopes()
}

func ids(recs []fakeengine.BuildRecord) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func TestBuildCachePruneDefaultScope(t *testing.T) {
	srv, eng := startFake(t, seedBuildCacheScopes)
	srv.ResetRequests()
	rep, err := eng.BuildCachePrune(ctxT(t), false)
	if err != nil {
		t.Fatal(err)
	}
	muts := srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/build/prune" {
		t.Fatalf("want one POST /build/prune, got %v", muts)
	}
	if muts[0].Query != "" {
		t.Fatalf("default scope must send no parameter (no all, filter or force): %q", muts[0].Query)
	}
	got := append([]string(nil), rep.Deleted...)
	sort.Strings(got)
	want := []string{"b5c9d3e7f1g5h9j3k7l1m5n9", "kq3x9w2m7v4n8p1r5t6y0z2a", "p0q4r8s2t6u0v4w8x2y6z0a4", "u8c2d7e1f5g9h3j6k0l4m8n2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	if rep.Reclaimed != 233_000_000+61_300_000+1_900_000+276_800_000 {
		t.Fatalf("reclaimed %d", rep.Reclaimed)
	}
	left := ids(srv.BuildCache())
	if strings.Join(left, ",") != "front0000000000000000001,intern000000000000000001,inuse0000000000000000001" {
		t.Fatalf("left %v", left)
	}
}

func TestBuildCachePruneAllKeepsInUse(t *testing.T) {
	srv, eng := startFake(t, seedBuildCacheScopes)
	srv.ResetRequests()
	rep, err := eng.BuildCachePrune(ctxT(t), true)
	if err != nil {
		t.Fatal(err)
	}
	muts := srv.Mutations()
	if len(muts) != 1 || muts[0].Path != "/build/prune" || muts[0].Query != "all=1" {
		t.Fatalf("want one POST /build/prune?all=1, got %v", muts)
	}
	if len(rep.Deleted) != 6 {
		t.Fatalf("deleted %v", rep.Deleted)
	}
	for _, id := range rep.Deleted {
		if strings.HasPrefix(id, "inuse") {
			t.Fatal("an in-use record was deleted")
		}
	}
	if left := ids(srv.BuildCache()); len(left) != 1 || left[0] != "inuse0000000000000000001" {
		t.Fatalf("left %v", left)
	}
}

func TestBuildCachePruneRefusalCarriesDaemonMessage(t *testing.T) {
	srv, eng := startFake(t, seedBuildCacheScopes)
	srv.FailNext("POST", `^/build/prune$`, 500, "builder is busy", 1)
	_, err := eng.BuildCachePrune(ctxT(t), false)
	if err == nil || !strings.Contains(err.Error(), "builder is busy") {
		t.Fatalf("err %v", err)
	}
	if len(srv.BuildCache()) != 7 {
		t.Fatal("a refused prune removed records")
	}
}

func TestBuildCachePruneReadOnlySendsNothing(t *testing.T) {
	srv, eng := startFake(t, seedBuildCacheScopes)
	ro := ReadOnly(eng)
	srv.ResetRequests()
	if _, err := ro.BuildCachePrune(ctxT(t), true); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err %v", err)
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Fatalf("read-only prune sent %v", reqs)
	}
}
