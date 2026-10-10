package plan

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

type rig struct {
	srv *fakeengine.Server
	eng engine.Engine
	jm  *jobs.Manager
	t   *testing.T
}

func newRig(t *testing.T, seed func(*fakeengine.Server)) *rig {
	t.Helper()
	srv := fakeengine.New()
	srv.PullStepDelay = time.Millisecond
	if seed != nil {
		seed(srv)
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	jm := jobs.NewManager()
	t.Cleanup(func() { jm.Close(); eng.Close() })
	return &rig{srv: srv, eng: eng, jm: jm, t: t}
}

func (r *rig) snap() Snapshot {
	r.t.Helper()
	s, err := Take(context.Background(), r.eng)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// run executes a plan as a job, exactly as the desk does, and returns the
// finished job.
func (r *rig) run(p Plan) jobs.Job {
	r.t.Helper()
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, r.eng, p, run)
	})
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	return j
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func assertNoForce(t *testing.T, srv *fakeengine.Server) {
	t.Helper()
	for _, req := range srv.Mutations() {
		q := strings.ToLower(req.Query)
		if strings.Contains(q, "force") || strings.Contains(q, "v=1") {
			t.Errorf("a forced or cascading request was sent: %s", req)
		}
	}
}

func TestPruneDeletesExactlyTheListedSet(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p := VolumePrune(r.snap(), false)
	if len(p.Steps) != 2 {
		t.Fatalf("want the 2 unused anonymous volumes listed, got %d", len(p.Steps))
	}
	listed := []string{p.Steps[0].ID, p.Steps[1].ID}

	// After the preview, another unused anonymous volume appears. Docker's
	// own prune endpoint would take it too.
	late := r.srv.AddAnonymousVolume("created-after-the-preview")
	before := len(r.srv.VolumeNames())
	r.srv.ResetRequests()

	j := r.run(p)
	if j.State != jobs.Succeeded || j.Done != 2 || j.Total != 2 {
		t.Fatalf("job: %+v", j)
	}
	names := r.srv.VolumeNames()
	if !has(names, late) {
		t.Fatal("a volume that was not in the preview was deleted")
	}
	for _, id := range listed {
		if has(names, id) {
			t.Fatalf("listed volume %s was not deleted", id)
		}
	}
	if len(names) != before-2 {
		t.Fatalf("%d volumes removed, want exactly 2", before-len(names))
	}
	// Exactly one DELETE per listed object and nothing else — in particular
	// no call to a prune endpoint.
	muts := r.srv.Mutations()
	if len(muts) != 2 {
		t.Fatalf("mutating requests: %v", muts)
	}
	for i, req := range muts {
		if req.Method != "DELETE" || req.Path != "/volumes/"+listed[i] {
			t.Errorf("request %d: %s", i, req)
		}
	}
	assertNoForce(t, r.srv)
}

func TestListedObjectThatBecameInUseIsRefusedAndTheRestContinue(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p := ImagePrune(r.snap(), false)
	if len(p.Steps) != 2 {
		t.Fatalf("want 2 dangling images, got %d", len(p.Steps))
	}
	// Between preview and confirmation a container starts using the first.
	victim := strings.TrimPrefix(p.Steps[0].ID, "sha256:")
	r.srv.AddContainer(fakeengine.ContainerSpec{Name: "latecomer", Image: victim, State: "exited"})

	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "1 of 2 failed") {
		t.Fatalf("job: state=%v err=%q", j.State, j.Err)
	}
	if !r.srv.HasImage(victim) {
		t.Fatal("an image that became in use was deleted")
	}
	if r.srv.HasImage(strings.TrimPrefix(p.Steps[1].ID, "sha256:")) {
		t.Fatal("the remaining listed image was not processed")
	}
	log := strings.Join(j.Log, "\n")
	if !strings.Contains(log, "✗ remove image") || !strings.Contains(log, "image is being used by stopped container") {
		t.Fatalf("the daemon's refusal must be recorded per object:\n%s", log)
	}
	if !strings.Contains(log, "✓ remove image") || !strings.Contains(log, "deleted") {
		t.Fatalf("per-object success must be recorded:\n%s", log)
	}
	assertNoForce(t, r.srv)
}

func TestImagePruneAllUntagsEveryReference(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p := ImagePrune(r.snap(), true)
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v\n%s", j, strings.Join(j.Log, "\n"))
	}
	refs := r.srv.ImageRefs()
	for _, gone := range []string{"python:3.12-slim", "node:22-alpine", "node:lts-alpine"} {
		if has(refs, gone) {
			t.Errorf("unused image %s survived", gone)
		}
	}
	for _, kept := range []string{"nginx:1.27", "postgres:16", "ghost:5", "mysql:8", "shop/worker:dev"} {
		if !has(refs, kept) {
			t.Errorf("in-use image %s was deleted", kept)
		}
	}
	if !strings.Contains(strings.Join(j.Log, "\n"), "reclaimed about") {
		t.Errorf("job output must report reclaimed space:\n%s", strings.Join(j.Log, "\n"))
	}
	assertNoForce(t, r.srv)
}

func TestNetworkPruneKeepsNetworksOfStoppedContainers(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p := NetworkPrune(r.snap())
	if len(p.Steps) != 1 || p.Steps[0].Name != "orphan-net" {
		t.Fatalf("steps: %+v", p.Steps)
	}
	if j := r.run(p); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	names := r.srv.NetworkNames()
	if has(names, "orphan-net") {
		t.Error("the unused network survived")
	}
	// blog_default is referenced only by stopped containers. The fake, like
	// Docker, would let it be removed — the plan must never ask.
	if !has(names, "blog_default") {
		t.Error("a network used only by stopped containers was deleted")
	}
}

func TestProjectRemoveTouchesOnlyItsContainers(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })

	// Running project: refused at plan time, nothing sent.
	p, err := ProjectRemove(r.snap(), "shop")
	if err != nil || p.Runnable() {
		t.Fatalf("running project must be blocked: %+v %v", p.Blockers, err)
	}

	// Stop it as a whole, then remove it.
	stop, err := ProjectStop(r.snap(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if j := r.run(stop); j.State != jobs.Succeeded || j.Total != 3 {
		t.Fatalf("project stop: %+v", j)
	}
	for _, c := range []string{"shop-web-1", "shop-api-1", "shop-db-1"} {
		if st, _ := r.srv.ContainerState(c); st != "exited" {
			t.Errorf("%s is %s after project stop", c, st)
		}
	}
	nets, vols := len(r.srv.NetworkNames()), len(r.srv.VolumeNames())
	r.srv.ResetRequests()
	rm, err := ProjectRemove(r.snap(), "shop")
	if err != nil || !rm.Runnable() || len(rm.Steps) != 4 {
		t.Fatalf("project remove plan: %+v %v", rm, err)
	}
	if j := r.run(rm); j.State != jobs.Succeeded {
		t.Fatalf("project remove: %+v", j)
	}
	for _, c := range r.srv.ContainerNames() {
		if strings.HasPrefix(c, "shop-") {
			t.Errorf("%s survived project removal", c)
		}
	}
	if len(r.srv.ContainerNames()) != 8 {
		t.Errorf("containers outside the project were touched: %v", r.srv.ContainerNames())
	}
	if len(r.srv.NetworkNames()) != nets || len(r.srv.VolumeNames()) != vols {
		t.Error("project removal touched networks or volumes")
	}
	for _, req := range r.srv.Mutations() {
		if req.Method != "DELETE" || !strings.HasPrefix(req.Path, "/containers/") {
			t.Errorf("unexpected request during project removal: %s", req)
		}
	}
	if len(Projects(r.snap())) != 1 {
		t.Error("the removed project is still listed")
	}
	assertNoForce(t, r.srv)
}

func TestCancelStopsFurtherSteps(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		for _, n := range []string{"p-a", "p-b", "p-c"} {
			s.AddContainer(fakeengine.ContainerSpec{
				Name: n, Image: "app:1", StopDelay: 150 * time.Millisecond,
				Labels: map[string]string{engine.LabelComposeProject: "p"},
			})
		}
	})
	p, err := ProjectStop(r.snap(), "p")
	if err != nil {
		t.Fatal(err)
	}
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, r.eng, p, run)
	})
	// Cancel once the first container is done and the second is in flight.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := r.jm.Get(id); j.Done >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.jm.Cancel(id)
	r.jm.Wait()

	j, _ := r.jm.Get(id)
	if j.State != jobs.Cancelled {
		t.Fatalf("state %v, want cancelled", j.State)
	}
	if st, _ := r.srv.ContainerState("p-a"); st != "exited" {
		t.Errorf("p-a (already stopped before cancel) is %s", st)
	}
	if st, _ := r.srv.ContainerState("p-c"); st != "running" {
		t.Errorf("p-c must not have been touched after cancel, is %s", st)
	}
	if n := r.srv.CountRequests("POST", `^/containers/[0-9a-f]+/stop$`); n > 2 {
		t.Errorf("%d stop requests sent; nothing may be sent after cancel", n)
	}
	log := strings.Join(j.Log, "\n")
	if !strings.Contains(log, "✓ stop container p-a") || !strings.Contains(log, "cancelled") {
		t.Errorf("the output must say what was already done:\n%s", log)
	}
}

func TestSingleStepFailureIsTheDaemonsMessage(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1", State: "exited"})
	})
	p, err := Container(OpRemoveContainer, r.snap(), r.srv.ContainerID("app"))
	if err != nil || !p.Runnable() {
		t.Fatal(err, p.Blockers)
	}
	// It starts running after the confirmation was shown.
	if err := r.eng.StartContainer(context.Background(), r.srv.ContainerID("app")); err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "container is running") || strings.Contains(j.Err, "remove container:") {
		t.Fatalf("job error should be the daemon's own sentence: %q", j.Err)
	}
	if _, ok := r.srv.ContainerState("app"); !ok {
		t.Fatal("the container was removed although it was running")
	}
	assertNoForce(t, r.srv)
}

func TestPullJobReportsProgress(t *testing.T) {
	r := newRig(t, nil)
	p, err := Pull(r.snap(), "alpine")
	if err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Succeeded || j.Fraction != 1 || !strings.Contains(j.Status, "Downloaded newer image for alpine:latest") {
		t.Fatalf("pull job: %+v", j)
	}
	if !r.srv.HasImage("alpine:latest") {
		t.Fatal("image missing after pull")
	}
	bad, _ := Pull(r.snap(), "nosuch/image")
	j = r.run(bad)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "pull access denied") {
		t.Fatalf("failed pull: %+v", j)
	}
}

func TestExecuteRefusesUnrunnablePlans(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	blocked, _ := Container(OpRemoveContainer, r.snap(), r.srv.ContainerID("shop-web-1"))
	r.srv.ResetRequests()
	j := r.run(blocked)
	if j.State != jobs.Failed || len(r.srv.Mutations()) != 0 {
		t.Fatalf("a blocked plan reached the daemon: %+v %v", j, r.srv.Mutations())
	}
}

// TestReferenceMovedAfterConfirmationIsRefused: an image reference is a name,
// and a name can be moved to another image between the dialog and the
// request. The daemon would act on the new image without complaint.
func TestReferenceMovedAfterConfirmationIsRefused(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p, err := ImageRemove(r.snap(), imageID(t, r, "python:3.12-slim"), "python:3.12-slim")
	if err != nil || !p.Runnable() {
		t.Fatal(err, p.Blockers)
	}
	// A rebuild moves the tag to a brand-new image nobody was shown.
	newID := r.srv.MoveTag("python:3.12-slim", 777)
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "now refers to a different image") {
		t.Fatalf("job: %+v", j)
	}
	if !r.srv.HasImage(newID) || !r.srv.HasImage("python:3.12-slim") {
		t.Fatal("the image the reference moved to was removed")
	}
	if n := len(r.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating request(s) sent for a reference that had moved", n)
	}
}

func imageID(t *testing.T, r *rig, ref string) string {
	t.Helper()
	for _, img := range r.snap().Images {
		for _, x := range img.Refs() {
			if x == ref {
				return img.ID
			}
		}
	}
	t.Fatalf("no image carries %s", ref)
	return ""
}

// TestUntagThatWouldNowDeleteIsRefused: "only the reference is removed, the
// data stays" was the promise; if the other tag vanished meanwhile, the same
// request would delete the image.
func TestUntagThatWouldNowDeleteIsRefused(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p, err := ImageRemove(r.snap(), imageID(t, r, "node:lts-alpine"), "node:lts-alpine")
	if err != nil || p.Title != "Untag image" || !p.Steps[0].KeepsData {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if _, err := r.eng.RemoveImage(context.Background(), "node:22-alpine"); err != nil {
		t.Fatal(err) // someone else removes the other tag
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "only reference") {
		t.Fatalf("job: %+v", j)
	}
	if !r.srv.HasImage("node:lts-alpine") || len(r.srv.Mutations()) != 0 {
		t.Fatal("the image was deleted by what had been confirmed as an untag")
	}
}

// TestPruneDoesNotUntagAnImageThatBecameInUse: the daemon refuses to delete
// an in-use image but not to untag it, so "left alone" has to be checked here.
func TestPruneDoesNotUntagAnImageThatBecameInUse(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p := ImagePrune(r.snap(), true)
	// node has two tags and no containers: two steps, both pinned.
	var nodeSteps int
	for _, st := range p.Steps {
		if strings.HasPrefix(st.ID, "node:") {
			nodeSteps++
			if st.Pin == "" || !st.MustBeUnused {
				t.Fatalf("prune step for a tagged image is not guarded: %+v", st)
			}
		}
	}
	if nodeSteps != 2 {
		t.Fatalf("want 2 steps for the two node tags, got %d", nodeSteps)
	}
	// After the preview a container is created from it.
	r.srv.AddContainer(fakeengine.ContainerSpec{Name: "latecomer", Image: "node:22-alpine", State: "exited"})
	j := r.run(p)
	if j.State != jobs.Failed {
		t.Fatalf("job: %+v", j)
	}
	refs := strings.Join(r.srv.ImageRefs(), ",")
	if !strings.Contains(refs, "node:22-alpine") || !strings.Contains(refs, "node:lts-alpine") {
		t.Fatalf("an image that became in use lost a tag: %s", refs)
	}
	log := strings.Join(j.Log, "\n")
	if !strings.Contains(log, "now used by container latecomer") {
		t.Fatalf("the refusal must name the container:\n%s", log)
	}
	// The rest of the listed set was still processed.
	if strings.Contains(refs, "python:3.12-slim") {
		t.Fatal("the other listed images were not removed")
	}
}

func TestVolumeRecreatedAfterConfirmationIsRefused(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) { s.SeedDemo() })
	p, err := VolumeRemove(r.snap(), "old-backups")
	if err != nil || !p.Runnable() || p.Steps[0].Pin == "" {
		t.Fatalf("plan: %+v %v", p, err)
	}
	r.srv.RecreateVolume("old-backups") // same name, a different volume
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "a different volume now has the name old-backups") {
		t.Fatalf("job: %+v", j)
	}
	if !has(r.srv.VolumeNames(), "old-backups") || len(r.srv.Mutations()) != 0 {
		t.Fatal("the recreated volume was deleted")
	}
}

func TestUntagOfAnInUseImageSaysWhoKeepsUsingIt(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		s.AddImage(100, time.Hour, "app:v1", "app:latest")
		s.AddContainer(fakeengine.ContainerSpec{Name: "web", Image: "app:v1"})
	})
	p, err := ImageRemove(r.snap(), imageID(t, r, "app:latest"), "app:latest")
	if err != nil || !p.Runnable() || p.Title != "Untag image" {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if eff := strings.Join(p.Effects, " | "); !strings.Contains(eff, "1 container using the image (web (running)) keep running on it") {
		t.Fatalf("the dialog must say the image is in use: %s", eff)
	}
	if j := r.run(p); j.State != jobs.Succeeded || !r.srv.HasImage("app:v1") {
		t.Fatalf("untag: %+v", j)
	}
}

// TestFailuresBeforeACancelAreNotHidden: a job that had failures and is then
// cancelled says both.
func TestFailuresBeforeACancelAreNotHidden(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		for _, n := range []string{"p-a", "p-b", "p-c"} {
			s.AddContainer(fakeengine.ContainerSpec{
				Name: n, Image: "app:1", StopDelay: 150 * time.Millisecond,
				Labels: map[string]string{engine.LabelComposeProject: "p"},
			})
		}
	})
	p, err := ProjectStop(r.snap(), "p")
	if err != nil {
		t.Fatal(err)
	}
	r.srv.FailNext("POST", `/stop$`, 500, "cannot stop container: permission denied", 1)
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, r.eng, p, run)
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if j, _ := r.jm.Get(id); j.Done >= 1 {
			break
		}
	}
	r.jm.Cancel(id)
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	log := strings.Join(j.Log, "\n")
	if j.State != jobs.Cancelled || !strings.Contains(log, "✗ stop container p-a — cannot stop container: permission denied") ||
		!strings.Contains(log, "1 failed") || !strings.Contains(log, "not attempted") {
		t.Fatalf("state=%v\n%s", j.State, log)
	}
}
