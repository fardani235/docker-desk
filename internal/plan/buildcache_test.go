package plan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func scopesSeed(s *fakeengine.Server) { s.SeedDemo(); s.SeedBuildCacheScopes() }

func (r *rig) account() engine.DiskUsage {
	r.t.Helper()
	du, err := r.eng.DiskUsage(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return du
}

func candidateIDs(p Plan) map[string]bool {
	out := map[string]bool{}
	for _, c := range p.BuildCache.Candidates {
		out[c.ID] = true
	}
	return out
}

func TestBuildCachePlanDefaultScope(t *testing.T) {
	r := newRig(t, scopesSeed)
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	p := BuildCachePrune(r.account(), false, at)
	if !p.Runnable() || p.Tier != TierTyped || p.Word != "prune" || p.Listed {
		t.Fatalf("plan: %+v", p)
	}
	if len(p.Steps) != 1 || p.Steps[0].Op != OpPruneBuildCache || p.Steps[0].Size != 0 {
		t.Fatalf("steps: %+v", p.Steps)
	}
	ids := candidateIDs(p)
	if len(ids) != 4 || ids["inuse0000000000000000001"] || ids["intern000000000000000001"] || ids["front0000000000000000001"] {
		t.Fatalf("default candidates: %v", ids)
	}
	pv := p.BuildCache
	if pv.Kept != 1 || pv.Excluded != 2 || pv.Shared != 1 || !pv.At.Equal(at) || pv.All {
		t.Fatalf("preview: %+v", pv)
	}
	for i := 1; i < len(pv.Candidates); i++ {
		if pv.Candidates[i-1].Size < pv.Candidates[i].Size {
			t.Fatal("candidates are not largest first")
		}
	}
	text := strings.Join(append(p.Effects, p.Warnings...), "\n")
	for _, want := range []string{"daemon decides the final set", "may also remove", "not a pinned set", "never forced"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "exactly") {
		t.Errorf("a build-cache plan must not claim an exact set:\n%s", text)
	}
}

func TestBuildCachePlanAllScope(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), true, time.Now())
	ids := candidateIDs(p)
	if len(ids) != 6 || ids["inuse0000000000000000001"] || !ids["intern000000000000000001"] || !ids["front0000000000000000001"] {
		t.Fatalf("all candidates: %v", ids)
	}
	if !p.BuildCache.All || p.BuildCache.Excluded != 0 || p.BuildCache.Kept != 1 {
		t.Fatalf("preview: %+v", p.BuildCache)
	}
}

func TestBuildCachePlanNothing(t *testing.T) {
	du := engine.DiskUsage{BuildItems: []engine.BuildCacheUsage{
		{ID: "a", Type: "regular", InUse: true, Size: 10},
		{ID: "b", Type: "internal", Size: 5},
	}}
	p := BuildCachePrune(du, false, time.Now())
	if p.Runnable() || len(p.Steps) != 0 || !strings.Contains(p.Nothing, "Tab") {
		t.Fatalf("default: %+v", p)
	}
	du.BuildItems = du.BuildItems[:1]
	if p := BuildCachePrune(du, true, time.Now()); p.Runnable() || !strings.Contains(p.Nothing, "in use") {
		t.Fatalf("all in use: %+v", p)
	}
	if p := BuildCachePrune(engine.DiskUsage{}, true, time.Now()); p.Runnable() || p.Nothing == "" {
		t.Fatalf("empty: %+v", p)
	}
}

func TestBuildCacheExecuteSendsOnePrune(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), false, time.Now())
	r.srv.ResetRequests()
	j := r.run(p)
	log := strings.Join(j.Log, "\n")
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v %s\n%s", j.State, j.Err, log)
	}
	reqs := r.srv.Requests()
	if len(reqs) != 1 || reqs[0].Method != "POST" || reqs[0].Path != "/build/prune" || reqs[0].Query != "" {
		t.Fatalf("requests: %v", reqs)
	}
	if !strings.Contains(log, "the daemon deleted 4 build-cache records and reported 573.0 MB reclaimed") {
		t.Fatalf("log:\n%s", log)
	}
	if strings.Contains(log, "reclaimed about") || strings.Contains(log, "also removed") || strings.Contains(log, "kept") {
		t.Fatalf("log claims more than the report:\n%s", log)
	}
	assertNoForce(t, r.srv)
}

func TestBuildCacheExecuteAllScopeSendsAll(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), true, time.Now())
	r.srv.ResetRequests()
	if j := r.run(p); j.State != jobs.Succeeded {
		t.Fatalf("job %v %s", j.State, j.Err)
	}
	if m := r.srv.Mutations(); len(m) != 1 || m[0].Query != "all=1" {
		t.Fatalf("mutations: %v", m)
	}
	if left := r.srv.BuildCache(); len(left) != 1 || !left[0].InUse {
		t.Fatalf("left: %+v", left)
	}
}

// The daemon's set differs from the preview both ways: a record released
// after the account is removed too, and a listed one that came into use is
// kept. The job names both.
func TestBuildCacheExecuteNamesDifferences(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), false, time.Now())
	r.srv.SetBuildCacheInUse("inuse0000000000000000001", false)
	r.srv.SetBuildCacheInUse("kq3x9w2m7v4n8p1r5t6y0z2a", true)
	j := r.run(p)
	log := strings.Join(j.Log, "\n")
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v %s", j.State, j.Err)
	}
	for _, want := range []string{
		"the daemon also removed 1 record the preview did not list", "also removed inuse0000000",
		"the daemon kept 1 record the preview listed", "kept kq3x9w2m7v4",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestBuildCacheExecuteRefusal(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), false, time.Now())
	r.srv.FailNext("POST", `^/build/prune$`, 500, "builder is busy", 1)
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "builder is busy") {
		t.Fatalf("job %v %q", j.State, j.Err)
	}
	if strings.Contains(strings.Join(j.Log, "\n"), "reclaimed") {
		t.Fatal("a refused prune claimed reclaimed bytes")
	}
	if len(r.srv.BuildCache()) != 7 {
		t.Fatal("a refused prune removed records")
	}
}

func TestBuildCacheExecuteReadOnlyRefuses(t *testing.T) {
	r := newRig(t, scopesSeed)
	p := BuildCachePrune(r.account(), true, time.Now())
	r.eng = engine.ReadOnly(r.eng)
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || len(r.srv.Requests()) != 0 {
		t.Fatalf("job %v %q, requests %v", j.State, j.Err, r.srv.Requests())
	}
}

// Daemon-chosen IDs are untrusted: logged escaped, and bounded.
func TestBuildCacheLogEscapesAndBoundsIDs(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		for i := 0; i < 30; i++ {
			s.AddBuildCache(fakeengine.BuildRecord{ID: "evil\x1b[2J" + strings.Repeat("x", i), Type: "regular", Size: 1})
		}
	})
	p := BuildCachePrune(r.account(), false, time.Now())
	j := r.run(p)
	log := strings.Join(j.Log, "\n")
	if strings.Contains(log, "\x1b") {
		t.Fatal("an escape sequence reached the job log")
	}
	if !strings.Contains(log, "… and 10 more") {
		t.Fatalf("IDs not bounded:\n%s", log)
	}
}
