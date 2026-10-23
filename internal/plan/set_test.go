package plan

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func skipped(p Plan) map[string]string {
	out := map[string]string{}
	for _, s := range p.Skipped {
		out[s.Name] = s.Reason
	}
	return out
}

// TestSetHoldsExactlyTheRunnableMembers: a selection is planned member by
// member with the single-object rules. What they allow is in the plan, in
// the order selected; what they do not is named in Skipped with their
// reason — including an object that has vanished — and is not in the plan.
func TestSetHoldsExactlyTheRunnableMembers(t *testing.T) {
	snap := world()
	p, err := Set(OpStop, snap, []Member{
		{ID: "c-db", Name: "db"},
		{ID: "c-job", Name: "job"},
		{ID: "c-web", Name: "web"},
		{ID: "c-gone", Name: "vanished"},
		{ID: "c-db", Name: "db"}, // selected twice: once in the plan
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(p); got != "c-db,c-web" {
		t.Fatalf("steps = %s, want exactly the running members in selection order", got)
	}
	for _, st := range p.Steps {
		if st.Op != OpStop || st.Kind != "container" {
			t.Fatalf("unexpected step %+v", st)
		}
	}
	sk := skipped(p)
	if len(sk) != 2 || sk["job"] != "job is not running (exited)" || sk["vanished"] != "that container no longer exists" {
		t.Fatalf("skipped = %v", sk)
	}
	if !p.Runnable() || !p.Listed || p.Tier != TierTyped || p.Word != "stop 2" {
		t.Fatalf("runnable=%v listed=%v tier=%v word=%q", p.Runnable(), p.Listed, p.Tier, p.Word)
	}
	if p.Title != "Stop containers" || p.JobTitle != "stop" || p.JobTarget != "2 containers" {
		t.Fatalf("title=%q job=%q %q", p.Title, p.JobTitle, p.JobTarget)
	}
	if !strings.Contains(p.Target.Name, "2 containers · 2 not included") {
		t.Fatalf("target = %q", p.Target.Name)
	}
	if !strings.Contains(joined(p.Effects), "Not included") {
		t.Fatalf("the effects do not say that excluded objects are left alone: %v", p.Effects)
	}
}

// TestSetLeavesOutWhatTheSingleRulesRefuse: an object a single-object plan
// would refuse — in use, running, predefined — is not in a set plan; it is
// named under Skipped with the refusal's reason, and the rest still runs.
func TestSetLeavesOutWhatTheSingleRulesRefuse(t *testing.T) {
	snap := world()
	cases := []struct {
		name    string
		op      Op
		members []Member
		steps   string
		reasons map[string]string // name -> substring of the reason
	}{
		{"running containers are not removed", OpRemoveContainer,
			[]Member{{ID: "c-web", Name: "web"}, {ID: "c-job", Name: "job"}, {ID: "c-paused", Name: "paused"}},
			"c-job", map[string]string{"web": "web is running. Docker Desk never force-removes", "paused": "paused is paused"}},
		{"volumes in use are kept", OpRemoveVolume,
			[]Member{{ID: "shop_pgdata", Name: "shop_pgdata"}, {ID: "old-backups", Name: "old-backups"}, {ID: "blog_data", Name: "blog_data"}},
			"old-backups", map[string]string{"shop_pgdata": "In use by 1 container: db (running)", "blog_data": "blog (exited)"}},
		{"networks in use or predefined are kept", OpRemoveNetwork,
			[]Member{{ID: "n-bridge", Name: "bridge"}, {ID: "n-orphan", Name: "orphan"}, {ID: "n-blog", Name: "blog_default"}},
			"n-orphan", map[string]string{"bridge": "predefined network", "blog_default": "Used by 1 container: blog (exited)"}},
		{"images in use are kept", OpRemoveImage,
			[]Member{{ID: "sha256:img-nginx", Ref: "nginx:1.27", Name: "nginx:1.27"}, {ID: "sha256:img-dangling1", Name: "<untagged> d1"}},
			"sha256:img-dangling1", map[string]string{"nginx:1.27": "In use by 2 containers"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Set(tc.op, snap, tc.members)
			if err != nil {
				t.Fatal(err)
			}
			if got := stepIDs(p); got != tc.steps {
				t.Fatalf("steps = %s, want %s", got, tc.steps)
			}
			sk := skipped(p)
			if len(sk) != len(tc.reasons) {
				t.Fatalf("skipped = %v, want %v", sk, tc.reasons)
			}
			for name, want := range tc.reasons {
				if !strings.Contains(sk[name], want) {
					t.Errorf("%s: reason %q does not say %q", name, sk[name], want)
				}
			}
			if len(p.Blockers) != 0 || !p.Runnable() {
				t.Fatalf("a set with runnable members must run: blockers=%v", p.Blockers)
			}
			if p.Word != "remove 1" {
				t.Fatalf("word = %q", p.Word)
			}
		})
	}
}

func TestSetWithNothingRunnableCannotRun(t *testing.T) {
	p, err := Set(OpStart, world(), []Member{{ID: "c-web", Name: "web"}, {ID: "c-paused", Name: "paused"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Runnable() || len(p.Steps) != 0 || p.Word != "" {
		t.Fatalf("runnable=%v steps=%d word=%q", p.Runnable(), len(p.Steps), p.Word)
	}
	if !strings.Contains(p.Nothing, "none of the 2 containers selected can be started") {
		t.Fatalf("nothing = %q", p.Nothing)
	}
	if sk := skipped(p); sk["web"] != "web is already running" || !strings.Contains(sk["paused"], "unpause it instead") {
		t.Fatalf("skipped = %v", sk)
	}
}

func TestSetOnlyForSetCapableOperations(t *testing.T) {
	for _, op := range []Op{OpStart, OpStop, OpRestart, OpPause, OpUnpause, OpKill, OpRemoveContainer, OpRemoveImage, OpRemoveVolume, OpRemoveNetwork} {
		if !SetCapable(op) {
			t.Errorf("%v should apply to a selection", op)
		}
	}
	for _, op := range []Op{OpConnect, OpDisconnect, OpPullImage, OpCreateVolume, OpCreateNetwork} {
		if SetCapable(op) {
			t.Errorf("%v must not apply to a selection", op)
		}
		if _, err := Set(op, world(), []Member{{ID: "n-orphan", Name: "orphan"}}); err == nil || isNA(err) {
			t.Errorf("Set(%v) = %v, want a plain error", op, err)
		}
	}
}

// TestImageSetSizesAndReferences: the total is the sum of the steps' sizes;
// one reference among others is an untag that frees nothing; every
// reference of an image selected deletes the image, the last untag carrying
// its size and the in-use re-check.
func TestImageSetSizesAndReferences(t *testing.T) {
	snap := world()
	p, err := Set(OpRemoveImage, snap, []Member{
		{ID: "sha256:img-dangling1", Name: "<untagged> 1"},
		{ID: "sha256:img-dangling2", Name: "<untagged> 2"},
		{ID: "sha256:img-tool", Ref: "tool:latest", Name: "tool:latest"},
		{ID: "sha256:img-tool", Ref: "tool:1", Name: "tool:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(p); got != "sha256:img-dangling1,sha256:img-dangling2,tool:1,tool:latest" {
		t.Fatalf("steps = %s", got)
	}
	if p.TotalSize != 7+9+50 {
		t.Fatalf("total = %d, want the sum of the steps' sizes", p.TotalSize)
	}
	untag, last := p.Steps[2], p.Steps[3]
	if !untag.KeepsData || untag.Size != 0 || untag.Pin != "sha256:img-tool" {
		t.Fatalf("first reference: %+v", untag)
	}
	if last.KeepsData || last.Size != 50 || !last.MustBeUnused || last.Pin != "sha256:img-tool" {
		t.Fatalf("last reference: %+v", last)
	}

	one, err := Set(OpRemoveImage, snap, []Member{{ID: "sha256:img-tool", Ref: "tool:1", Name: "tool:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if st := one.Steps[0]; !st.KeepsData || st.Size != 0 || !strings.HasPrefix(st.Detail, "untag only") || one.TotalSize != 0 {
		t.Fatalf("one of two references: %+v total=%d", st, one.TotalSize)
	}
	if !strings.Contains(joined(one.Effects), `"untag only"`) {
		t.Fatalf("effects do not explain untag only: %v", one.Effects)
	}

	// Every reference of an image that is in use: left out, with the users.
	pg, err := Set(OpRemoveImage, snap, []Member{
		{ID: "sha256:img-pg", Ref: "postgres:16", Name: "postgres:16"},
		{ID: "sha256:img-pg", Ref: "postgres:latest", Name: "postgres:latest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pg.Runnable() || len(pg.Skipped) != 2 || !strings.Contains(pg.Skipped[0].Reason, "db (running)") {
		t.Fatalf("in-use image: runnable=%v skipped=%v", pg.Runnable(), pg.Skipped)
	}
}

// TestSetExecutesAndReportsPartialFailure: a set plan goes through the
// unchanged Execute — one request per step, never forced, continuing past a
// failure and reporting how many failed.
func TestSetExecutesAndReportsPartialFailure(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	snap := r.snap()
	var members []Member
	for _, v := range snap.Volumes {
		members = append(members, Member{ID: v.Name, Name: v.Name})
	}
	p, err := Set(OpRemoveVolume, snap, members)
	if err != nil {
		t.Fatal(err)
	}
	// Seven volumes: four are mounted by containers and left out.
	if len(p.Steps) != 3 || len(p.Skipped) != 4 || p.Word != "remove 3" {
		t.Fatalf("steps=%s skipped=%v word=%q", stepIDs(p), p.Skipped, p.Word)
	}
	r.srv.FailNext("DELETE", "^/volumes/old-backups$", 409, "volume is being prepared for backup", 1)
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "1 of 3 failed") {
		t.Fatalf("job: state=%v err=%q", j.State, j.Err)
	}
	if n := len(r.srv.Mutations()); n != 3 {
		t.Fatalf("want 3 requests (the failure does not stop the rest), got %d", n)
	}
	names := strings.Join(r.srv.VolumeNames(), ",")
	for _, keep := range []string{"old-backups", "shop_pgdata", "shop_uploads", "blog_db"} {
		if !strings.Contains(names, keep) {
			t.Errorf("%s was deleted", keep)
		}
	}
	assertNoForce(t, r.srv)
}

// TestImageSetOfEveryReferenceDeletesTheImage: selecting both references
// of an image removes the image. Planned one by one, each reference would
// have been an untag that promised to keep the data, and the second one
// would have been refused when it was sent.
func TestImageSetOfEveryReferenceDeletesTheImage(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	snap := r.snap()
	var id string
	for _, img := range snap.Images {
		if has(img.Refs(), "node:lts-alpine") {
			id = img.ID
		}
	}
	p, err := Set(OpRemoveImage, snap, []Member{
		{ID: id, Ref: "node:lts-alpine", Name: "node:lts-alpine"},
		{ID: id, Ref: "node:22-alpine", Name: "node:22-alpine"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if j := r.run(p); j.State != jobs.Succeeded {
		t.Fatalf("job: %v %s", j.State, j.Err)
	}
	if r.srv.HasImage(id) {
		t.Fatal("the image is still there")
	}
	assertNoForce(t, r.srv)
}
