package plan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func TestNormalizeTag(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ok := []struct{ in, want string }{
		{"acme/api", "acme/api:latest"},
		{"acme/api:3.29.0", "acme/api:3.29.0"},
		{"alpine", "alpine:latest"},
		{"docker.io/library/alpine:3", "alpine:3"},
		{"docker.io/acme/api", "acme/api:latest"},
		{"localhost:5000/team/api", "localhost:5000/team/api:latest"},
		{"localhost:5000/team/api:v1", "localhost:5000/team/api:v1"},
		{"registry.example.com:8443/a/b/c:1_2-3.x", "registry.example.com:8443/a/b/c:1_2-3.x"},
		{"  acme/api:1  ", "acme/api:1"},
		{"acme/api:" + strings.Repeat("t", 128), "acme/api:" + strings.Repeat("t", 128)},
	}
	for _, c := range ok {
		got, err := NormalizeTag(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeTag(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		// What it returns is stable: it is what the job sends and compares.
		if again, err := NormalizeTag(got); err != nil || again != got {
			t.Errorf("NormalizeTag(%q) is not a fixed point: %q, %v", got, again, err)
		}
	}
	bad := []struct{ name, in, want string }{
		{"empty", "", "enter the new reference"},
		{"blank", "   ", "enter the new reference"},
		{"space", "acme/api 1", "spaces"},
		{"tab", "acme/api\t1", "spaces"},
		{"newline", "acme/api\n:1", "spaces"},
		{"escape", "acme/\x1b[31mapi", "spaces"},
		{"digest", "acme/api@" + digest, "digest"},
		{"tag and digest", "acme/api:1@" + digest, "digest"},
		{"upper-case repository", "Acme/API:1", "not a valid image reference"},
		{"bad tag", "acme/api:-1", "not a valid image reference"},
		{"empty tag", "acme/api:", "not a valid image reference"},
		{"tag too long", "acme/api:" + strings.Repeat("t", 129), "not a valid image reference"},
		{"two colons", "acme/api:1:2", "not a valid image reference"},
		{"path traversal", "../etc/passwd", "not a valid image reference"},
		{"query", "acme/api?force=1", "not a valid image reference"},
		{"scheme", "https://acme/api", "not a valid image reference"},
		{"a bare image ID", strings.Repeat("a", 64), "not a valid image reference"},
		{"non-ASCII", "acmé/api", "not a valid image reference"},
	}
	for _, c := range bad {
		got, err := NormalizeTag(c.in)
		if err == nil || got != "" || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: NormalizeTag(%q) = %q, %v; want an error containing %q", c.name, c.in, got, err, c.want)
		}
	}
}

// tagWorld is three images with IDs in the daemon's form.
func tagWorld() (Snapshot, string, string, string) {
	a := "sha256:" + strings.Repeat("a", 64)
	b := "sha256:" + strings.Repeat("b", 64)
	u := "sha256:" + strings.Repeat("c", 64)
	return Snapshot{Images: []engine.Image{
		{ID: a, RepoTags: []string{"acme/api:3.29.0", "acme/api:stable"}, Size: 100},
		{ID: b, RepoTags: []string{"acme/api:latest", "localhost:5000/team/api:v1"}, Size: 200},
		{ID: u, Dangling: true, Size: 7},
	}}, a, b, u
}

func TestTagOwner(t *testing.T) {
	snap, a, b, u := tagWorld()
	if o := snap.TagOwner(a, "acme/web:1"); o.Kind != TagFree || o.ID != "" || o.Short != "" {
		t.Errorf("free target: %+v", o)
	}
	// A target that is also a repository tag of this image.
	if o := snap.TagOwner(a, "acme/api:stable"); o.Kind != TagThisImage || o.ID != a {
		t.Errorf("this image's own tag: %+v", o)
	}
	if o := snap.TagOwner(a, "acme/api:latest"); o.Kind != TagOtherImage || o.ID != b || o.Short != strings.Repeat("b", 12) ||
		!slices.Equal(o.Refs, []string{"acme/api:latest", "localhost:5000/team/api:v1"}) {
		t.Errorf("another image's tag: %+v", o)
	}
	if o := snap.TagOwner(b, "acme/api:latest"); o.Kind != TagThisImage {
		t.Errorf("the same target, asked for its holder: %+v", o)
	}
	if o := snap.TagOwner(u, "localhost:5000/team/api:v1"); o.Kind != TagOtherImage || o.ID != b {
		t.Errorf("an untagged source, a held target with a registry port: %+v", o)
	}
	// The repository alone is not the tag.
	if o := snap.TagOwner(a, "acme/api:nope"); o.Kind != TagFree {
		t.Errorf("same repository, other tag: %+v", o)
	}
	// A daemon that lists the long form names the same target.
	long := Snapshot{Images: []engine.Image{{ID: b, RepoTags: []string{"docker.io/library/alpine:3"}}}}
	if o := long.TagOwner(a, "alpine:3"); o.Kind != TagOtherImage || o.ID != b {
		t.Errorf("a long-form reference: %+v", o)
	}
}

func TestImageTagPlan(t *testing.T) {
	snap, a, b, u := tagWorld()

	p, err := ImageTag(snap, a, "acme/web")
	if err != nil || !p.Runnable() || p.Tier != TierForm || len(p.Warnings) != 0 {
		t.Fatalf("create: %+v %v", p, err)
	}
	st := p.Steps[0]
	if len(p.Steps) != 1 || st.Op != OpTagImage || st.ID != a || st.Name != "acme/web:latest" ||
		st.Tag == nil || st.Tag.Target != "acme/web:latest" || st.Tag.Owner != "" || st.Pin != "" {
		t.Fatalf("create step: %+v %+v", st, st.Tag)
	}
	if p.JobTitle != "tag image" || p.JobTarget != "acme/web:latest" {
		t.Errorf("job labels: %q %q", p.JobTitle, p.JobTarget)
	}

	// Another image holds the target: the move is named, and recorded.
	p, err = ImageTag(snap, a, "acme/api:latest")
	if err != nil || !p.Runnable() {
		t.Fatalf("move: %+v %v", p, err)
	}
	if p.Steps[0].ID != a || p.Steps[0].Tag.Owner != b {
		t.Fatalf("move step: %+v %+v", p.Steps[0], p.Steps[0].Tag)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], strings.Repeat("b", 12)) || !strings.Contains(p.Warnings[0], "moves") {
		t.Errorf("move warning: %v", p.Warnings)
	}

	// An untagged image is tagged by its ID like any other.
	p, err = ImageTag(snap, u, "rescued:1")
	if err != nil || p.Steps[0].ID != u || p.Steps[0].Tag.Target != "rescued:1" {
		t.Fatalf("untagged source: %+v %v", p, err)
	}

	var notApplicable *NotApplicable
	if _, err := ImageTag(snap, a, "acme/api:stable"); !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "already names this image") {
		t.Errorf("already this image: %v", err)
	}
	if _, err := ImageTag(snap, "sha256:"+strings.Repeat("d", 64), "x:1"); !errors.As(err, &notApplicable) {
		t.Errorf("missing image: %v", err)
	}
	if _, err := ImageTag(snap, a, "acme/api@sha256:"+strings.Repeat("0", 64)); err == nil || errors.As(err, &notApplicable) {
		t.Errorf("a digest target is the form's error, not a status: %v", err)
	}
	// An ID that is not a digest never reaches a request path.
	odd := Snapshot{Images: []engine.Image{{ID: "../containers/x", RepoTags: []string{"a:1"}}}}
	if _, err := ImageTag(odd, "../containers/x", "b:1"); !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("odd image ID: %v", err)
	}
}

func TestImageUntagPlan(t *testing.T) {
	snap, a, b, u := tagWorld()
	var notApplicable *NotApplicable

	p, err := ImageUntag(snap, a, "acme/api:3.29.0")
	if err != nil || !p.Runnable() {
		t.Fatalf("untag: %+v %v", p, err)
	}
	st := p.Steps[0]
	if len(p.Steps) != 1 || st.Op != OpRemoveImage || st.ID != "acme/api:3.29.0" || st.Pin != a || !st.KeepsData || !st.KeepsTag || st.Size != 0 || st.MustBeUnused {
		t.Fatalf("untag step: %+v", st)
	}
	if p.Title != "Untag image" || p.JobTitle != "untag image" || p.Tier != TierTyped || p.Word != "3.29.0" || p.TotalSize != 0 {
		t.Fatalf("untag plan: title %q job %q tier %v word %q size %d", p.Title, p.JobTitle, p.Tier, p.Word, p.TotalSize)
	}
	effects := strings.Join(p.Effects, "\n")
	if !strings.Contains(effects, "acme/api:stable") || !strings.Contains(effects, "The image data stays") {
		t.Errorf("effects do not name what remains: %s", effects)
	}
	// A registry host with a port: the word is the tag, not the port.
	if p, err := ImageUntag(snap, b, "localhost:5000/team/api:v1"); err != nil || p.Word != "v1" {
		t.Errorf("word for a reference with a port: %q %v", p.Word, err)
	}

	last := Snapshot{Images: []engine.Image{{ID: a, RepoTags: []string{"only:1"}}}}
	_, err = ImageUntag(last, a, "only:1")
	if !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "only tag") || !strings.Contains(err.Error(), "d remove") ||
		!strings.Contains(err.Error(), "would delete the image") {
		t.Errorf("last reference: %v", err)
	}
	for name, c := range map[string]struct{ id, ref string }{
		"missing image":     {"sha256:" + strings.Repeat("d", 64), "acme/api:stable"},
		"missing reference": {a, "acme/api:gone"},
		"another's tag":     {a, "acme/api:latest"},
		"untagged row":      {u, ""},
	} {
		if _, err := ImageUntag(snap, c.id, c.ref); !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "nothing was sent") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A digest reference is not a tag; and a reference the daemon lists in
	// a form that does not read back as itself is never put into a request.
	odd := Snapshot{Images: []engine.Image{{ID: a, RepoTags: []string{
		"kic/stable@sha256:" + strings.Repeat("0", 64), "kic/stable:1", "x/../../containers/c:1", "Up:1", "esc:\x1b[2J",
	}}}}
	for _, ref := range odd.Images[0].RepoTags {
		_, err := ImageUntag(odd, a, ref)
		if ref == "kic/stable:1" {
			if err != nil {
				t.Errorf("%q: %v", ref, err)
			}
			continue
		}
		if !errors.As(err, &notApplicable) || strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("%q: %q", ref, err)
		}
	}
}

// From the independent review: what the daemon lists beside the reference
// being removed is drawn in the confirmation, so it is escaped; and a digest
// reference is not "another tag" — a daemon that lists one among an image's
// tags drops it with the repository's last tag, and the image with it.
func TestImageUntagPlanOtherReferences(t *testing.T) {
	a := "sha256:" + strings.Repeat("a", 64)
	var notApplicable *NotApplicable

	hostile := Snapshot{Images: []engine.Image{{ID: a, RepoTags: []string{"a:1", "b:1\x1b[2J\u202e", "c:2"}}}}
	hostile.Images[0].RepoTags[1] = "b:1\x1b[2J\u202e"
	p, err := ImageUntag(hostile, a, "a:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range append(append([]string{}, p.Effects...), p.Warnings...) {
		if strings.ContainsAny(e, "\x1b\u202e") {
			t.Errorf("a raw control or reordering character in the confirmation: %q", e)
		}
	}

	digest := "kic/stable@sha256:" + strings.Repeat("0", 64)
	only := Snapshot{Images: []engine.Image{{ID: a, RepoTags: []string{"kic/stable:1", digest}}}}
	if _, err := ImageUntag(only, a, "kic/stable:1"); !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "only tag") {
		t.Errorf("a digest reference was counted as another tag: %v", err)
	}
	// The same image through d is still ImageRemove's untag: unchanged.
	if p, err := ImageRemove(only, a, "kic/stable:1"); err != nil || !p.Steps[0].KeepsData || p.Steps[0].KeepsTag {
		t.Errorf("d on the same row: %+v %v", p.Steps, err)
	}
	two := Snapshot{Images: []engine.Image{{ID: a, RepoTags: []string{"kic/stable:1", digest, "kic/stable:2"}}}}
	if p, err := ImageUntag(two, a, "kic/stable:1"); err != nil || !p.Steps[0].KeepsTag {
		t.Errorf("another tag remains: %v", err)
	}
}

// The holder of a tag target is compared with the daemon's answer and named
// in the job: an ID that is not the daemon's own form is never carried.
func TestImageTagPlanRefusesAnOddHolder(t *testing.T) {
	a := "sha256:" + strings.Repeat("a", 64)
	odd := Snapshot{Images: []engine.Image{
		{ID: a, RepoTags: []string{"a:1"}},
		{ID: "sha256:\x1b[2Jnot-an-id", RepoTags: []string{"b:1"}},
	}}
	var notApplicable *NotApplicable
	_, err := ImageTag(odd, a, "b:1")
	if !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "nothing was sent") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("an odd holder ID: %q", err)
	}
}

// ---- execution, against the fake daemon ----------------------------------

func tagRig(t *testing.T) (*rig, string, string) {
	var a, b string
	r := newRig(t, func(s *fakeengine.Server) {
		a = "sha256:" + s.AddImage(1000, 0, "acme/api:3.29.0", "acme/api:stable")
		b = "sha256:" + s.AddImage(2000, 0, "acme/api:latest", "acme/api:2")
	})
	return r, a, b
}

func refsOf(t *testing.T, r *rig, id string) []string {
	t.Helper()
	d, err := r.eng.InspectImage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.Refs()
}

func tagPosts(srv *fakeengine.Server) []fakeengine.Request {
	var out []fakeengine.Request
	for _, q := range srv.Mutations() {
		if strings.HasSuffix(q.Path, "/tag") {
			out = append(out, q)
		}
	}
	return out
}

func TestExecuteTagCreates(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageTag(r.snap(), a, "localhost:5000/team/api")
	if err != nil {
		t.Fatal(err)
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	muts := r.srv.Mutations()
	if len(muts) != 1 || muts[0].Path != "/images/"+a+"/tag" {
		t.Fatalf("mutations = %v, want one tag of the image by its ID", muts)
	}
	if got := refsOf(t, r, a); !slices.Equal(got, []string{"acme/api:3.29.0", "acme/api:stable", "localhost:5000/team/api:latest"}) {
		t.Fatalf("refs = %v", got)
	}
	assertNoForce(t, r.srv)
	if log := strings.Join(j.Log, "\n"); !strings.Contains(log, "no registry was contacted") {
		t.Errorf("log: %s", log)
	}
}

func TestExecuteTagMovesWhatWasShown(t *testing.T) {
	r, a, b := tagRig(t)
	p, err := ImageTag(r.snap(), a, "acme/api:latest")
	if err != nil || p.Steps[0].Tag.Owner != b {
		t.Fatalf("plan: %+v %v", p, err)
	}
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	if got := refsOf(t, r, "acme/api:latest"); !slices.Contains(got, "acme/api:stable") {
		t.Fatalf("acme/api:latest did not move to image a: %v", got)
	}
	if got := refsOf(t, r, b); !slices.Equal(got, []string{"acme/api:2"}) {
		t.Fatalf("image b after the move: %v", got)
	}
	if tr := r.srv.TagRequests(); len(tr) != 1 || "sha256:"+tr[0].MovedFrom != b {
		t.Fatalf("tag log: %+v", tr)
	}
}

// The target named no image, or image B, when the form was read; by the
// time the job inspects it, it names an image nobody was shown.
func TestExecuteTagRefusesATargetThatChanged(t *testing.T) {
	for name, target := range map[string]string{"absent then another image": "acme/web:1", "image B then image C": "acme/api:latest"} {
		t.Run(name, func(t *testing.T) {
			r, a, _ := tagRig(t)
			p, err := ImageTag(r.snap(), a, target)
			if err != nil {
				t.Fatal(err)
			}
			c := r.srv.MoveTag(target, 555) // a build lands on the target meanwhile
			r.srv.ResetRequests()
			j := r.run(p)
			if j.State != jobs.Failed || !strings.Contains(j.Err, "the target changed") || !strings.Contains(j.Err, "nothing was tagged") {
				t.Fatalf("job: %+v", j)
			}
			if n := len(r.srv.Mutations()); n != 0 {
				t.Fatalf("%d mutating request(s) for a target that had changed: %v", n, r.srv.Mutations())
			}
			if got := refsOf(t, r, target); !slices.Equal(got, []string{target}) {
				t.Fatalf("%s no longer names only image c (%s): %v", target, c, got)
			}
		})
	}
}

func TestExecuteTagAlreadyThisImageSendsNothing(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageTag(r.snap(), a, "acme/web:1")
	if err != nil {
		t.Fatal(err)
	}
	// Someone else gives the image that very tag meanwhile.
	if err := r.eng.TagImage(context.Background(), a, "acme/web:1"); err != nil {
		t.Fatal(err)
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Succeeded || !strings.Contains(strings.Join(j.Log, "\n"), "nothing needed doing") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating request(s) for a tag already in place", n)
	}
}

// The image the form showed as holding the target has lost it: the tag is a
// plain create, nothing moves, and it goes ahead.
func TestExecuteTagTargetFreedMeanwhile(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageTag(r.snap(), a, "acme/api:latest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.eng.RemoveImage(context.Background(), "acme/api:latest"); err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Succeeded || !slices.Contains(refsOf(t, r, a), "acme/api:latest") {
		t.Fatalf("job: %+v", j)
	}
}

func TestExecuteTagSourceGone(t *testing.T) {
	r, a, _ := tagRig(t)
	snap := r.snap()
	p, err := ImageTag(snap, a, "acme/web:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"acme/api:3.29.0", "acme/api:stable"} {
		if _, err := r.eng.RemoveImage(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
	before := r.srv.ImageRefs()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "No such image") {
		t.Fatalf("job: %+v", j)
	}
	if got := r.srv.ImageRefs(); !slices.Equal(got, before) {
		t.Fatalf("a tag of a vanished image changed the store: %v", got)
	}
}

func TestExecuteTagRefusesMalformedSteps(t *testing.T) {
	r, a, _ := tagRig(t)
	good, err := ImageTag(r.snap(), a, "acme/web:1")
	if err != nil {
		t.Fatal(err)
	}
	mutate := map[string]func(*Step){
		"no tag step":            func(s *Step) { s.Tag = nil },
		"source by reference":    func(s *Step) { s.ID = "acme/api:stable" },
		"source with a path":     func(s *Step) { s.ID = a + "/../x" },
		"target not normalised":  func(s *Step) { s.Tag = &TagStep{Target: "acme/web"} },
		"target with a digest":   func(s *Step) { s.Tag = &TagStep{Target: "acme/web@sha256:" + strings.Repeat("0", 64)} },
		"target with whitespace": func(s *Step) { s.Tag = &TagStep{Target: "acme/web:1 "} },
		"holder not an image ID": func(s *Step) { s.Tag = &TagStep{Target: "acme/web:1", Owner: "sha256:\x1b[2J"} },
		"holder without sha256:": func(s *Step) { s.Tag = &TagStep{Target: "acme/web:1", Owner: strings.Repeat("b", 64)} },
	}
	for name, f := range mutate {
		p := good
		p.Steps = []Step{good.Steps[0]}
		f(&p.Steps[0])
		r.srv.ResetRequests()
		j := r.run(p)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "nothing was sent") {
			t.Errorf("%s: %+v", name, j)
		}
		if n := len(r.srv.Requests()); n != 0 {
			t.Errorf("%s: %d request(s) sent", name, n)
		}
	}
}

func TestExecuteTagReadOnlySendsNothing(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageTag(r.snap(), a, "acme/web:1")
	if err != nil {
		t.Fatal(err)
	}
	ro := engine.ReadOnly(r.eng)
	r.srv.ResetRequests()
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error { return Execute(ctx, ro, p, run) })
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "read-only") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Fatalf("read-only tag sent %d request(s): %v", n, r.srv.Requests())
	}
}

func TestExecuteUntagKeepsTheImage(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageUntag(r.snap(), a, "acme/api:3.29.0")
	if err != nil {
		t.Fatal(err)
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	muts := r.srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "DELETE" || muts[0].Path != "/images/acme/api:3.29.0" {
		t.Fatalf("mutations = %v, want one removal of the reference", muts)
	}
	assertNoForce(t, r.srv)
	if got := refsOf(t, r, a); !slices.Equal(got, []string{"acme/api:stable"}) {
		t.Fatalf("the image must remain under its other reference: %v", got)
	}
	if log := strings.Join(j.Log, "\n"); strings.Contains(log, "deleted") {
		t.Errorf("an untag reported a deletion: %s", log)
	}
}

func TestExecuteUntagRefusesAMovedTag(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageUntag(r.snap(), a, "acme/api:3.29.0")
	if err != nil {
		t.Fatal(err)
	}
	c := r.srv.MoveTag("acme/api:3.29.0", 555)
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the tag moved") || !strings.Contains(j.Err, "nothing was removed") {
		t.Fatalf("job: %+v", j)
	}
	if len(r.srv.Mutations()) != 0 || !r.srv.HasImage(c) || !r.srv.HasImage("acme/api:3.29.0") {
		t.Fatalf("a moved tag was acted on: %v", r.srv.Mutations())
	}
}

// The other reference vanished after the confirmation: the same request
// would now delete the image.
func TestExecuteUntagRefusesWhatBecameTheLastReference(t *testing.T) {
	r, a, _ := tagRig(t)
	p, err := ImageUntag(r.snap(), a, "acme/api:3.29.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.eng.RemoveImage(context.Background(), "acme/api:stable"); err != nil {
		t.Fatal(err)
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "only reference") {
		t.Fatalf("job: %+v", j)
	}
	if len(r.srv.Mutations()) != 0 || !r.srv.HasImage(a) {
		t.Fatal("an untag deleted the image")
	}
	// And planned afresh, it is refused before any dialog.
	var notApplicable *NotApplicable
	if _, err := ImageUntag(r.snap(), a, "acme/api:3.29.0"); !errors.As(err, &notApplicable) || !strings.Contains(err.Error(), "d remove") {
		t.Fatalf("last reference: %v", err)
	}
}

// From the independent review: the image's other tag vanishes after the
// confirmation and only a digest reference is left beside the one being
// removed. That is not "the image stays": nothing is removed.
func TestExecuteUntagRefusesWhenOnlyADigestReferenceRemains(t *testing.T) {
	digest := "kic/stable@sha256:" + strings.Repeat("0", 64)
	var id string
	r := newRig(t, func(s *fakeengine.Server) {
		id = "sha256:" + s.AddImage(1000, 0, "kic/stable:1", "kic/stable:2", digest)
	})
	if got := refsOf(t, r, id); !slices.Contains(got, digest) {
		t.Skipf("the fake does not list a digest reference among an image's tags: %v", got)
	}
	p, err := ImageUntag(r.snap(), id, "kic/stable:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.eng.RemoveImage(context.Background(), "kic/stable:2"); err != nil {
		t.Fatal(err)
	}
	r.srv.ResetRequests()
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "only tag") {
		t.Fatalf("job: %+v", j)
	}
	if len(r.srv.Mutations()) != 0 || !slices.Contains(refsOf(t, r, id), "kic/stable:1") {
		t.Fatalf("the image's last tag was removed: %v", r.srv.Mutations())
	}
}

// noIDEngine answers every image inspect with success and no ID.
type noIDEngine struct{ engine.Engine }

func (noIDEngine) InspectImage(context.Context, string) (engine.ImageDetail, error) {
	return engine.ImageDetail{}, nil
}

// An inspect answer that names no image is not taken to mean the target is
// free: the tag could otherwise move a reference nobody was shown.
func TestExecuteTagRefusesAnAnswerWithoutAnID(t *testing.T) {
	r, a, b := tagRig(t)
	p, err := ImageTag(r.snap(), a, "acme/web:1") // the form showed a free target
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.TagImage(context.Background(), b, "acme/web:1"); err != nil {
		t.Fatal(err) // …which image b takes meanwhile
	}
	r.srv.ResetRequests()
	eng := noIDEngine{r.eng}
	id := r.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error { return Execute(ctx, eng, p, run) })
	r.jm.Wait()
	j, _ := r.jm.Get(id)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "without an image ID") || !strings.Contains(j.Err, "nothing was tagged") {
		t.Fatalf("job: %+v", j)
	}
	if n := len(r.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating request(s)", n)
	}
	if got := refsOf(t, r, b); !slices.Contains(got, "acme/web:1") {
		t.Fatalf("the tag moved: %v", got)
	}
}
