package plan

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

const pushRef = "host.test:5000/acme/api:3.28.3"

func TestPushPlan(t *testing.T) {
	img := engine.Image{ID: "sha256:abcdef0123456789aa", Size: 42_000_000,
		RepoTags: []string{pushRef, "host.test:5000/acme/api:latest"}}
	snap := Snapshot{Images: []engine.Image{img, {ID: "sha256:feedfacecafe00", RepoTags: []string{"other:1"}}, {ID: "sha256:0000dangling00"}}}

	p, err := Push(snap, img.ID, pushRef)
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != TierTyped || p.Word != "3.28.3" || !p.Runnable() {
		t.Fatalf("tier %v word %q runnable %v", p.Tier, p.Word, p.Runnable())
	}
	if len(p.Steps) != 1 {
		t.Fatalf("steps: %+v", p.Steps)
	}
	st := p.Steps[0]
	if st.Op != OpPushImage || st.ID != pushRef || st.Pin != img.ID || st.Kind != "image" {
		t.Fatalf("step: %+v", st)
	}
	if p.Target.Name != pushRef || p.Target.ID != "abcdef012345" || p.Registry != "host.test:5000" || p.JobTitle != "push image" || p.JobTarget != pushRef {
		t.Fatalf("plan identity: %+v / %q", p.Target, p.Registry)
	}
	all := strings.Join(p.Effects, "\n")
	for _, s := range []string{
		"Uploads " + pushRef + " to host.test:5000",
		"remote tag 3.28.3 is created, or moved",
		"cannot be undone from this desk",
		"local image and its tags are not changed",
		"Nothing is built",
		"still name image abcdef012345",
		"layers already uploaded may remain",
		"1 other tag stays local",
	} {
		if !strings.Contains(all, s) {
			t.Errorf("effects lack %q:\n%s", s, all)
		}
	}
	// The plan holds no credential: the caller adds the source and the
	// callback.
	if p.CredSource != "" || p.RegistryAuth != nil {
		t.Fatal("the plan builder touched the credential")
	}

	// Docker Hub: the familiar name, docker.io as the registry.
	hub := engine.Image{ID: "sha256:1111", RepoTags: []string{"acme/api:1"}}
	p, err = Push(Snapshot{Images: []engine.Image{hub}}, hub.ID, "acme/api:1")
	if err != nil || p.Registry != "docker.io" || p.Word != "1" {
		t.Fatalf("hub: %+v %v", p, err)
	}

	for name, c := range map[string]struct{ id, ref, want string }{
		"untagged":        {"sha256:0000dangling00", "", "no tag to push"},
		"not its tag":     {img.ID, "other:1", "is not a tag of image"},
		"unknown tag":     {img.ID, "host.test:5000/acme/api:9", "is not a tag of image"},
		"gone":            {"sha256:nothere", pushRef, "no longer exists"},
		"untagged row id": {img.ID, "", "no tag to push"},
	} {
		_, err := Push(snap, c.id, c.ref)
		var na *NotApplicable
		if !errors.As(err, &na) || !strings.Contains(na.Reason, c.want) || !strings.Contains(na.Reason, "nothing was sent") && c.want != "no longer exists" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIsPushPlan(t *testing.T) {
	if push, err := isPushPlan(Plan{Steps: []Step{{Op: OpRemoveImage}}}); push || err != nil {
		t.Fatal("a remove is not a push")
	}
	if push, err := isPushPlan(Plan{Steps: []Step{{Op: OpPushImage}}}); !push || err != nil {
		t.Fatal("one push step")
	}
	if push, err := isPushPlan(Plan{Steps: []Step{{Op: OpPushImage}, {Op: OpRemoveImage}}}); !push || err == nil {
		t.Fatal("a push mixed with another request must be refused")
	}
}

// pushRig is a fake daemon with one image under pushRef and the image's
// push plan, with a credential callback that counts its calls.
func pushRig(t *testing.T, seed func(*fakeengine.Server)) (*rig, Plan, *atomic.Int32) {
	t.Helper()
	r := newRig(t, func(s *fakeengine.Server) {
		s.PushStepDelay = time.Millisecond
		s.AddImage(30_000_000, 0, pushRef)
		if seed != nil {
			seed(s)
		}
	})
	snap := r.snap()
	var id string
	for _, img := range snap.Images {
		if has(img.Refs(), pushRef) {
			id = img.ID
		}
	}
	p, err := Push(snap, id, pushRef)
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	p.CredSource = "auths"
	p.RegistryAuth = func(context.Context) (string, error) {
		calls.Add(1)
		return "c2VjcmV0LWhlYWRlci12YWx1ZQ==", nil
	}
	return r, p, calls
}

func logText(j jobs.Job) string { return strings.Join(j.Log, "\n") }

func TestExecutePushSuccess(t *testing.T) {
	r, p, calls := pushRig(t, nil)
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v\n%s", j, logText(j))
	}
	reqs := r.srv.PushRequests()
	if len(reqs) != 1 || reqs[0].Tag != "3.28.3" || reqs[0].Auth != "c2VjcmV0LWhlYWRlci12YWx1ZQ==" {
		t.Fatalf("push requests: %+v", reqs)
	}
	if calls.Load() != 1 {
		t.Fatalf("credential resolved %d times", calls.Load())
	}
	// The pin was inspected before the push, and the push is the only
	// mutation.
	var order []string
	for _, q := range r.srv.Requests() {
		if strings.HasSuffix(q.Path, "/json") && strings.HasPrefix(q.Path, "/images/") || strings.HasSuffix(q.Path, "/push") {
			order = append(order, q.Method)
		}
	}
	if len(order) < 2 || order[0] != http.MethodGet || order[len(order)-1] != http.MethodPost {
		t.Fatalf("request order: %v", order)
	}
	if m := r.srv.Mutations(); len(m) != 1 {
		t.Fatalf("mutations: %v", m)
	}
	log := logText(j)
	digest := r.srv.PushedManifest(pushRef)
	for _, s := range []string{"✓ push image " + pushRef + " done", "digest " + digest, "credential: auths", PushUnchangedNote, "2 layers uploaded"} {
		if !strings.Contains(log, s) {
			t.Errorf("log lacks %q:\n%s", s, log)
		}
	}
	if strings.Contains(log, "c2VjcmV0") || strings.Contains(j.Status, "c2VjcmV0") {
		t.Fatalf("the credential reached the job:\n%s", log)
	}
	if !strings.HasPrefix(j.Status, "pushed ") {
		t.Errorf("status %q", j.Status)
	}
	// Nothing local changed.
	if tags := r.srv.ImageRefs(); len(tags) != 1 || tags[0] != pushRef {
		t.Fatalf("local tags: %v", tags)
	}
}

// The credential is read before the pin is inspected: the pin is the last
// request before the push.
func TestExecutePushCredentialBeforePin(t *testing.T) {
	r, p, _ := pushRig(t, nil)
	inspectsAtRead := -1
	p.RegistryAuth = func(context.Context) (string, error) {
		inspectsAtRead = r.srv.CountRequests(http.MethodGet, `^/images/.+/json$`)
		return "", nil
	}
	r.srv.ResetRequests()
	if j := r.run(p); j.State != jobs.Succeeded {
		t.Fatalf("job %+v", j)
	}
	if inspectsAtRead != 0 {
		t.Fatalf("the pin was inspected before the credential was read (%d inspects)", inspectsAtRead)
	}
	reqs := r.srv.Requests()
	if len(reqs) < 2 || !strings.HasSuffix(reqs[len(reqs)-1].Path, "/push") || !strings.HasSuffix(reqs[len(reqs)-2].Path, "/json") {
		t.Fatalf("the last two requests are not the pin and the push: %v", reqs)
	}
}

func TestExecutePushMovedTag(t *testing.T) {
	r, p, calls := pushRig(t, nil)
	r.srv.MoveTag(pushRef, 5)
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the tag moved") || !strings.Contains(j.Err, "nothing was sent") {
		t.Fatalf("job: %+v\n%s", j, logText(j))
	}
	// The credential is read before the pin (so a slow helper cannot
	// widen the window); nothing is sent.
	if n := len(r.srv.PushRequests()); n != 0 {
		t.Fatalf("%d push requests after the tag moved", n)
	}
	_ = calls
	if !strings.Contains(logText(j), PushUnchangedNote) {
		t.Errorf("log:\n%s", logText(j))
	}
}

func TestExecutePushImageGone(t *testing.T) {
	r, p, calls := pushRig(t, nil)
	if _, err := r.eng.RemoveImage(context.Background(), pushRef); err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "no longer names an image") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.PushRequests()); n != 0 {
		t.Fatalf("%d push requests", n)
	}
	_ = calls
}

func TestExecutePushHelperFailure(t *testing.T) {
	r, p, _ := pushRig(t, nil)
	p.CredSource = "credHelpers ecr-login"
	p.RegistryAuth = func(context.Context) (string, error) {
		return "", errors.New("credential helper ecr-login exited with status 1")
	}
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "credential could not be read") || !strings.Contains(j.Err, "exited with status 1") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.PushRequests()); n != 0 {
		t.Fatalf("%d push requests after a helper failure", n)
	}
}

func TestExecutePushNoCredential(t *testing.T) {
	r, p, _ := pushRig(t, nil)
	p.CredSource, p.RegistryAuth = "", func(context.Context) (string, error) { return "", nil }
	j := r.run(p)
	if j.State != jobs.Succeeded || !strings.Contains(logText(j), "credential: none found") {
		t.Fatalf("job: %+v\n%s", j, logText(j))
	}
	if reqs := r.srv.PushRequests(); len(reqs) != 1 || reqs[0].Auth != "" {
		t.Fatalf("requests: %+v", reqs)
	}
}

func TestExecutePushRefused(t *testing.T) {
	r, p, _ := pushRig(t, func(s *fakeengine.Server) {
		s.RefusePush("host.test:5000/acme/api", "denied: requested access to the resource is denied\x1b[2J")
	})
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "denied: requested access to the resource is denied") {
		t.Fatalf("job: %+v", j)
	}
	log := logText(j)
	if !strings.Contains(log, PushUnchangedNote) || strings.Contains(log, "✓") || strings.Contains(log, "digest") {
		t.Fatalf("a refused push claimed something:\n%s", log)
	}
	// The registry's words cannot act on the terminal.
	if strings.ContainsRune(log, 0x1b) || strings.ContainsRune(j.Err, 0x1b) {
		t.Fatalf("an escape sequence reached the job: %q", log)
	}
	if d := r.srv.PushedManifest(pushRef); d != "" {
		t.Fatalf("refused, yet the registry holds %s", d)
	}
	if tags := r.srv.ImageRefs(); len(tags) != 1 || tags[0] != pushRef {
		t.Fatalf("local tags: %v", tags)
	}
}

func TestExecutePushConnectionBroken(t *testing.T) {
	r, p, _ := pushRig(t, func(s *fakeengine.Server) { s.PushStepDelay = 30 * time.Millisecond })
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, r.eng, p, run)
	})
	deadline := time.Now().Add(5 * time.Second)
	for len(r.srv.PushRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	r.srv.Close() // the daemon goes away mid-stream
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	log := logText(j)
	if j.State != jobs.Failed || !strings.Contains(log, "whether the remote tag moved is not known") || strings.Contains(log, "✓") {
		t.Fatalf("job %+v\n%s", j, log)
	}
}

func TestExecutePushCancelled(t *testing.T) {
	r, p, _ := pushRig(t, func(s *fakeengine.Server) { s.PushStepDelay = 40 * time.Millisecond })
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, r.eng, p, run)
	})
	deadline := time.Now().Add(5 * time.Second)
	for len(r.srv.PushRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // a layer or two into the stream
	r.jm.Cancel(id)
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	if j.State != jobs.Cancelled {
		t.Fatalf("job: %+v\n%s", j, logText(j))
	}
	log := logText(j)
	if !strings.Contains(log, PushCancelledNote) || strings.Contains(log, "✓") || strings.Contains(log, "digest") {
		t.Fatalf("log:\n%s", log)
	}
	if strings.Contains(log, "not attempted") {
		t.Fatalf("a cancelled push said it was not attempted:\n%s", log)
	}
	if d := r.srv.PushedManifest(pushRef); d != "" {
		t.Fatalf("a cancelled push completed: %s", d)
	}
}

func TestExecutePushReadOnly(t *testing.T) {
	r, p, calls := pushRig(t, nil)
	ro := engine.ReadOnly(r.eng)
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return Execute(ctx, ro, p, run)
	})
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "read-only") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.PushRequests()); n != 0 || calls.Load() != 0 {
		t.Fatalf("%d push requests, %d credential reads in read-only mode", n, calls.Load())
	}
}
