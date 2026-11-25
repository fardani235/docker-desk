package ui

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

// Images › t and u (openspec change tag-or-untag-image).

const (
	tagRowA = "acme/api:3.29.0" // image A, which also has acme/api:stable
	tagRowB = "acme/api:latest" // image B, which also has other/thing:1
	tagSolo = "solo:1"          // an image with one tag
)

// tagIDs are the seeded images' IDs, as the daemon lists them.
type tagIDs struct{ a, b, solo, untagged string }

func newTagBench(t *testing.T, o benchOpts) (*bench, tagIDs) {
	t.Helper()
	var ids tagIDs
	o.seed = func(s *fakeengine.Server) {
		ids.a = "sha256:" + s.AddImage(30_000_000, 0, tagRowA, "acme/api:stable")
		ids.b = "sha256:" + s.AddImage(20_000_000, time.Hour, tagRowB, "other/thing:1")
		ids.solo = "sha256:" + s.AddImage(1_000_000, 2*time.Hour, tagSolo)
		ids.untagged = "sha256:" + s.AddImage(5_000_000, 3*time.Hour)
	}
	b := newBench(t, o)
	b.kind(KindImages)
	b.m.focus = PaneList
	return b, ids
}

func (b *bench) untaggedRow() string {
	b.t.Helper()
	for _, n := range b.rowNames() {
		if strings.HasPrefix(n, "<untagged>") {
			return n
		}
	}
	b.t.Fatalf("no untagged row: %v", b.rowNames())
	return ""
}

// tagFormOn highlights row and presses t; it fails unless the tag form
// opened, and unless nothing at all was sent that could change the daemon.
func (b *bench) tagFormOn(row string) *dialog {
	b.t.Helper()
	b.selectRow(row)
	b.srv.ResetRequests()
	b.keys("t")
	d := b.m.dlg
	if d == nil || d.kind != dlgForm || d.form != formTag || d.tg == nil {
		b.t.Fatalf("t on %s opened no tag form:\n%s", row, b.screen())
	}
	b.noMutations("after t")
	return d
}

// untagDialog highlights row and presses u; it fails unless the typed
// confirmation opened.
func (b *bench) untagDialog(row string) *dialog {
	b.t.Helper()
	b.selectRow(row)
	b.srv.ResetRequests()
	b.keys("u")
	d := b.m.dlg
	if d == nil || d.kind != dlgPlan || d.plan.Title != "Untag image" {
		b.t.Fatalf("u on %s opened no untag confirmation (status %q):\n%s", row, b.m.status.text, b.screen())
	}
	b.noMutations("after u")
	return d
}

func (b *bench) refsOf(id string) []string {
	b.t.Helper()
	d, err := b.eng.InspectImage(context.Background(), id)
	if err != nil {
		b.t.Fatalf("inspect %s: %v", id, err)
	}
	return d.Refs()
}

// noRegistryRequests fails if anything the daemon would turn into a
// registry request was sent: a pull, a push, a distribution inspect, a login.
func (b *bench) noRegistryRequests() {
	b.t.Helper()
	for _, r := range b.srv.Requests() {
		if strings.Contains(r.Path, "/distribution/") || strings.HasSuffix(r.Path, "/push") || r.Path == "/images/create" || r.Path == "/auth" {
			b.t.Fatalf("a registry request was sent: %s", r)
		}
	}
	if n := len(b.srv.PushRequests()); n != 0 {
		b.t.Fatalf("%d push request(s)", n)
	}
}

// noForce fails if any mutating request asked the daemon to force.
func (b *bench) noForce() {
	b.t.Helper()
	for _, r := range b.srv.Mutations() {
		if strings.Contains(strings.ToLower(r.Query), "force") {
			b.t.Fatalf("a forced request: %s", r)
		}
	}
}

// oneTagRequest is the single mutating request of a tag job, checked to be
// the tag of source with exactly repo and tag.
func (b *bench) oneTagRequest(source, repo, tag string) {
	b.t.Helper()
	muts := b.srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/images/"+source+"/tag" {
		b.t.Fatalf("mutations = %v, want exactly POST /images/%s/tag", muts, source)
	}
	q, err := url.ParseQuery(muts[0].Query)
	if err != nil || q.Get("repo") != repo || q.Get("tag") != tag || len(q) != 2 {
		b.t.Fatalf("query = %q, want exactly repo=%s tag=%s", muts[0].Query, repo, tag)
	}
}

func TestTagKeyFormAndJob(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	d := b.tagFormOn(tagRowA)
	if d.tg.imageID != ids.a || !slices.Equal(d.tg.refs, []string{tagRowA, "acme/api:stable"}) {
		t.Fatalf("the form is bound to %+v", d.tg)
	}
	text := b.dialogText()
	for _, s := range []string{
		"Tag image", tagRowA, "(" + engine.ShortID(ids.a) + ")", "acme/api:3.29.0, acme/api:stable", b.srv.Host(),
		"A name without a tag means :latest", "Local only: no registry is contacted", "tagged by its ID",
		"checked again just before the request", "Enter tag",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("the form lacks %q:\n%s", s, text)
		}
	}

	// Typing sends nothing, and the form restates the reference exactly.
	b.typeText("acme/web")
	text = b.dialogText()
	if !strings.Contains(text, "will create acme/web:latest") || !strings.Contains(text, "No image has this reference: it is created.") {
		t.Fatalf("the form does not restate the target:\n%s", text)
	}
	b.settle()
	b.noMutations("while typing")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s) before Enter", n)
	}

	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the form stayed open: %q\n%s", d.formErr, b.screen())
	}
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded || j.Title != "tag image" || j.Target != "acme/web:latest" {
		t.Fatalf("job: %+v", j)
	}
	b.oneTagRequest(ids.a, "docker.io/acme/web", "latest")
	b.noForce()
	b.noRegistryRequests()
	if got := b.refsOf(ids.a); !slices.Equal(got, []string{tagRowA, "acme/api:stable", "acme/web:latest"}) {
		t.Fatalf("refs of the image = %v", got)
	}
	// The fake emitted the daemon's tag event, and the list shows the row.
	if tr := b.srv.TagRequests(); len(tr) != 1 || tr[0].Ref != "acme/web:latest" || "sha256:"+tr[0].ImageID != ids.a || tr[0].MovedFrom != "" {
		t.Fatalf("the fake's tag log: %+v", tr)
	}
	b.until("the new row", func() bool { return slices.Contains(b.rowNames(), "acme/web:latest") })
	b.see("img tag", "acme/web:latest") // the daemon's tag event, in the Events pane
	if log := strings.Join(j.Log, "\n"); !strings.Contains(log, "no registry was contacted") {
		t.Errorf("job log: %s", log)
	}
}

func TestTagExplicitTagAndRegistryPort(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	b.tagFormOn(tagRowA)
	b.typeText("localhost:5000/team/api:3.29.0")
	if text := b.dialogText(); !strings.Contains(text, "will create localhost:5000/team/api:3.29.0") {
		t.Fatalf("form:\n%s", text)
	}
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	b.oneTagRequest(ids.a, "localhost:5000/team/api", "3.29.0")
	b.noRegistryRequests()
}

func TestTagRefusedTargetsSendNothing(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("0", 64)
	cases := []struct{ name, text, live, onEnter string }{
		{"empty", "", "A name without a tag means :latest", "Enter the new reference"},
		{"digest", "acme/web" + digest, "A tag cannot name a digest", "A tag cannot name a digest"},
		{"tag and digest", "acme/web:1" + digest, "A tag cannot name a digest", "A tag cannot name a digest"},
		{"upper case", "Acme/Web:1", "Not a valid image reference", "Not a valid image reference"},
		{"bad tag", "acme/web:-1", "Not a valid image reference", "Not a valid image reference"},
		{"space", "acme/web 1", "cannot contain spaces", "cannot contain spaces"},
		{"path", "../../containers/x", "Not a valid image reference", "Not a valid image reference"},
		{"image ID", strings.Repeat("a", 64), "Not a valid image reference", "Not a valid image reference"},
	}
	b, _ := newTagBench(t, benchOpts{})
	for _, c := range cases {
		d := b.tagFormOn(tagRowA)
		b.typeText(c.text)
		if text := b.dialogText(); !strings.Contains(text, c.live) || strings.Contains(text, "will create") {
			t.Errorf("%s: the form does not say why before Enter:\n%s", c.name, text)
		}
		b.keys("enter")
		b.settle()
		if b.m.dlg != d || !strings.Contains(d.formErr, c.onEnter) || !strings.Contains(d.formErr, "Nothing was sent") {
			t.Errorf("%s: Enter must keep the form open and say why; formErr %q", c.name, d.formErr)
		}
		b.noMutations(c.name)
		if n := len(b.m.jobs.List()); n != 0 {
			t.Fatalf("%s: %d job(s)", c.name, n)
		}
		b.keys("esc")
		if b.m.dlg != nil {
			t.Fatalf("%s: Esc did not close the form", c.name)
		}
	}
	b.noRegistryRequests()
}

func TestTagUntaggedImageByID(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	d := b.tagFormOn(b.untaggedRow())
	if d.tg.imageID != ids.untagged || len(d.tg.refs) != 0 {
		t.Fatalf("form: %+v", d.tg)
	}
	if text := b.dialogText(); !strings.Contains(text, "none: the image is untagged, and is tagged by its ID") {
		t.Fatalf("form:\n%s", text)
	}
	b.typeText("rescued:1")
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	b.oneTagRequest(ids.untagged, "docker.io/library/rescued", "1")
	if got := b.refsOf(ids.untagged); !slices.Equal(got, []string{"rescued:1"}) {
		t.Fatalf("refs = %v", got)
	}
	b.until("the row", func() bool { return slices.Contains(b.rowNames(), "rescued:1") })
}

func TestTagAndUntagRefusals(t *testing.T) {
	for _, key := range []string{"t", "u"} {
		t.Run("read-only "+key, func(t *testing.T) {
			b, _ := newTagBench(t, benchOpts{readOnly: true})
			b.selectRow(tagRowA)
			b.srv.ResetRequests()
			b.keys(key)
			b.settle()
			if b.m.dlg != nil || !strings.Contains(b.m.status.text, "Read-only mode") || !strings.Contains(b.m.status.text, "Nothing was sent") {
				t.Fatalf("read-only: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
			}
			b.noMutations("read-only " + key)
			if n := len(b.m.jobs.List()); n != 0 {
				t.Fatalf("%d job(s)", n)
			}
		})
		t.Run("selection "+key, func(t *testing.T) {
			b, _ := newTagBench(t, benchOpts{})
			b.selectRow(tagRowA)
			b.keys(" ")
			b.selectRow(tagRowB)
			b.keys(" ")
			b.srv.ResetRequests()
			b.keys(key)
			b.settle()
			if b.m.dlg != nil || !strings.Contains(b.m.status.text, "one object at a time") || !strings.Contains(b.m.status.text, "Nothing was sent") {
				t.Fatalf("selection: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
			}
			b.noMutations("selection " + key)
		})
	}
	t.Run("the menu offers neither for a selection", func(t *testing.T) {
		b, _ := newTagBench(t, benchOpts{})
		b.selectRow(tagRowA)
		b.keys(" ")
		b.keys("enter")
		if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
			t.Fatal("no menu")
		}
		for _, a := range b.m.dlg.menu {
			if a.key == "t" || a.key == "u" {
				t.Errorf("the selection's menu offers %q", a.label)
			}
		}
	})
}

// A read-only desk refuses in the form too, should one ever be open.
func TestTagFormRefusesWhenReadOnly(t *testing.T) {
	b, _ := newTagBench(t, benchOpts{})
	b.tagFormOn(tagRowA)
	b.typeText("acme/web:1")
	b.m.readOnly = true
	b.keys("enter")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "Read-only mode") {
		t.Fatalf("dialog %v status %q", b.m.dlg != nil, b.m.status.text)
	}
	b.noMutations("read-only form")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}
}

func TestTagAlreadyThisImageSendsNothing(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	d := b.tagFormOn(tagRowA)
	b.typeText("acme/api:stable")
	if text := b.dialogText(); !strings.Contains(text, "Already names this image: there is nothing to tag, and Enter sends nothing.") {
		t.Fatalf("form:\n%s", text)
	}
	b.srv.ResetRequests()
	b.keys("enter")
	b.settle()
	if b.m.dlg != d || !strings.Contains(d.formErr, "already names this image") || !strings.Contains(d.formErr, "Nothing was sent") {
		t.Fatalf("Enter must keep the form and say so: %q", d.formErr)
	}
	b.noMutations("a tag the image already has")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}
	if got := b.refsOf(ids.a); !slices.Equal(got, []string{tagRowA, "acme/api:stable"}) {
		t.Fatalf("refs = %v", got)
	}
}

func TestTagMoveIsNamedAndCompletes(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	b.tagFormOn(tagRowA)
	b.typeText("acme/api:latest")
	text := b.dialogText()
	shortA, shortB := engine.ShortID(ids.a), engine.ShortID(ids.b)
	for _, s := range []string{
		"will create acme/api:latest", "It names image " + shortB + " now", "which keeps its 1 other reference",
		"the tag will MOVE to image " + shortA, "Image " + shortB + " keeps its data",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("the move warning lacks %q:\n%s", s, text)
		}
	}
	b.noMutations("before Enter")
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	b.oneTagRequest(ids.a, "docker.io/acme/api", "latest")
	if got := b.refsOf(ids.a); !slices.Contains(got, "acme/api:latest") {
		t.Fatalf("image a: %v", got)
	}
	if got := b.refsOf(ids.b); !slices.Equal(got, []string{"other/thing:1"}) {
		t.Fatalf("image b keeps its data and its other reference: %v", got)
	}
	if tr := b.srv.TagRequests(); len(tr) != 1 || "sha256:"+tr[0].MovedFrom != ids.b {
		t.Fatalf("tag log: %+v", tr)
	}
	if log := strings.Join(j.Log, "\n"); !strings.Contains(log, "moves from image "+shortB+" to image "+shortA) {
		t.Errorf("job log: %s", log)
	}
}

// The target named no image, or image B, when the form was read; by the
// time the job inspects it, it names image C. The list on screen has not
// caught up: Enter is pressed for what the form showed.
func TestTagTargetChangedAfterTheForm(t *testing.T) {
	for name, target := range map[string]string{"absent to another image": "acme/web:1", "image B to image C": "acme/api:latest"} {
		t.Run(name, func(t *testing.T) {
			b, ids := newTagBench(t, benchOpts{})
			b.tagFormOn(tagRowA)
			b.typeText(target)
			b.dialogText() // the form has been drawn: this is what was shown
			c := b.srv.MoveTag(target, 4_000_000)
			b.srv.ResetRequests()
			b.keys("enter") // no frame has run: the list is still the old one
			if b.m.dlg != nil {
				t.Fatalf("the form stayed open: %q", b.m.dlg.formErr)
			}
			j := b.waitJob(b.lastJob().ID)
			if j.State != jobs.Failed || !strings.Contains(j.Err, "the target changed") || !strings.Contains(j.Err, "nothing was tagged") {
				t.Fatalf("job: %+v", j)
			}
			if !strings.Contains(j.Err, engine.ShortID(c)) {
				t.Errorf("the refusal does not name the image the target has now: %s", j.Err)
			}
			b.noMutations("a target that changed")
			if got := b.refsOf(target); !slices.Equal(got, []string{target}) {
				t.Fatalf("%s must still name only the new image: %v", target, got)
			}
			if got := b.refsOf(ids.a); slices.Contains(got, target) {
				t.Fatalf("image a was tagged: %v", got)
			}
		})
	}
}

// The list changes what the target names while the form is open: the form
// redraws with the move warning, and an Enter in that same moment — pressed
// for what it said before — does not count.
func TestTagOwnerChangeUnderTheFormMustBeRead(t *testing.T) {
	const arm = 400 * time.Millisecond
	b, ids := newTagBench(t, benchOpts{arm: arm})
	d := b.tagFormOn(tagRowA)
	b.typeText("acme/web:1")
	b.see("No image has this reference")
	c := b.srv.MoveTag("acme/web:1", 4_000_000)
	b.see("MOVE")
	b.srv.ResetRequests()
	b.keys("enter")
	if b.m.dlg != d || !strings.Contains(d.formErr, "has only just appeared") || !strings.Contains(d.formErr, "Nothing was sent") {
		t.Fatalf("an Enter on a warning that only just appeared was taken: dialog %v formErr %q", b.m.dlg != nil, d.formErr)
	}
	b.noMutations("an early Enter")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}
	// Read, then confirmed: the move the form now names goes ahead.
	b.run(arm + 100*time.Millisecond)
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the form stayed open: %q", d.formErr)
	}
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	if tr := b.srv.TagRequests(); len(tr) != 1 || tr[0].MovedFrom != c || "sha256:"+tr[0].ImageID != ids.a {
		t.Fatalf("tag log: %+v", tr)
	}
}

// From the independent review: a target another image holds, typed and
// entered in one motion. The move warning appears with the last character;
// an Enter before it could have been read does not move the tag. A plain
// create, which takes nothing from any image, is not held up.
func TestTagMoveTypedAndEnteredAtOnceIsNotTaken(t *testing.T) {
	const arm = 400 * time.Millisecond
	b, ids := newTagBench(t, benchOpts{arm: arm})
	d := b.tagFormOn(tagRowA)
	b.typeText("acme/api:latest")
	b.srv.ResetRequests()
	b.keys("enter") // not even drawn yet
	if b.m.dlg != d || !strings.Contains(d.formErr, "has only just appeared") || !strings.Contains(d.formErr, engine.ShortID(ids.b)) ||
		!strings.Contains(d.formErr, "Nothing was sent") {
		t.Fatalf("a move was taken before its warning could be read: dialog %v formErr %q", b.m.dlg != nil, d.formErr)
	}
	b.keys("enter", "enter") // nor does insisting, inside the same moment
	b.settle()
	b.noMutations("an Enter typed ahead of the move warning")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}
	if got := b.refsOf(ids.b); !slices.Contains(got, "acme/api:latest") {
		t.Fatalf("the tag moved: %v", got)
	}
	b.see("MOVE")
	b.run(arm + 100*time.Millisecond)
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the form stayed open after the warning had been on screen: %q", d.formErr)
	}
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	if tr := b.srv.TagRequests(); len(tr) != 1 || "sha256:"+tr[0].MovedFrom != ids.b {
		t.Fatalf("tag log: %+v", tr)
	}

	// A create is the operator's own typed name and moves nothing: Enter
	// right after the last character is taken, as in the pull form.
	b.tagFormOn(tagRowA)
	b.typeText("acme/fresh:1")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("a plain create was held up: %q", b.m.dlg.formErr)
	}
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
}

// The source is the image that was highlighted, by ID: the row's reference
// moving to another image meanwhile cannot redirect the new tag.
func TestTagSourceIsTheImageIDNotItsReference(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	b.tagFormOn(tagRowA)
	b.typeText("acme/web:9")
	c := b.srv.MoveTag(tagRowA, 4_000_000) // the row's own reference now names image C
	b.srv.ResetRequests()
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	b.oneTagRequest(ids.a, "docker.io/acme/web", "9")
	if got := b.refsOf("acme/web:9"); !slices.Contains(got, "acme/api:stable") || slices.Contains(got, tagRowA) {
		t.Fatalf("acme/web:9 must name image a, not the image its old reference moved to (%s): %v", c, got)
	}
}

// The source image vanishes between the form and the request: the daemon's
// 404 is the job's error and nothing else is tagged.
func TestTagSourceGone(t *testing.T) {
	b, _ := newTagBench(t, benchOpts{})
	b.tagFormOn(tagSolo)
	b.typeText("acme/web:1")
	if _, err := b.eng.RemoveImage(context.Background(), tagSolo); err != nil {
		t.Fatal(err)
	}
	before := b.srv.ImageRefs()
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "No such image") {
		t.Fatalf("job: %+v", j)
	}
	if got := b.srv.ImageRefs(); !slices.Equal(got, before) {
		t.Fatalf("refs changed: %v", got)
	}
}

func TestTagAndUntagFromDetailsAndMenu(t *testing.T) {
	b, _ := newTagBench(t, benchOpts{})
	b.selectRow(tagRowA)
	b.m.focus = PaneDetails
	b.srv.ResetRequests()
	b.keys("t")
	if d := b.m.dlg; d == nil || d.form != formTag || d.kind != dlgForm {
		t.Fatalf("t in the Details pane opened no tag form:\n%s", b.screen())
	}
	b.keys("esc")
	b.keys("u")
	if d := b.m.dlg; d == nil || d.kind != dlgPlan || d.plan.Title != "Untag image" {
		t.Fatalf("u in the Details pane opened no untag confirmation:\n%s", b.screen())
	}
	b.keys("esc")
	b.noMutations("keys in the Details pane")

	b.m.focus = PaneList
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
		t.Fatal("no menu")
	}
	labels := map[string]string{}
	for _, a := range b.m.dlg.menu {
		labels[a.key] = a.label
	}
	if labels["t"] != "tag…" || labels["u"] != "untag…" {
		t.Fatalf("the menu lacks tag or untag: %v", labels)
	}
	b.keys("u")
	if d := b.m.dlg; d == nil || d.kind != dlgPlan || d.plan.Word != "3.29.0" {
		t.Fatalf("the menu's untag opened no confirmation:\n%s", b.screen())
	}
	b.noMutations("the menu")
}

func TestUntagTypedTagKeepsTheImage(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	d := b.untagDialog(tagRowA)
	p := d.plan
	if p.Tier != plan.TierTyped || p.Word != "3.29.0" || !p.Runnable() || len(p.Steps) != 1 ||
		p.Steps[0].Pin != ids.a || !p.Steps[0].KeepsData || p.Steps[0].ID != tagRowA || p.Steps[0].Size != 0 {
		t.Fatalf("plan: %+v", p)
	}
	text := b.dialogText()
	for _, s := range []string{
		"Untag image", tagRowA, "(" + engine.ShortID(ids.a) + ")", b.srv.Host(),
		"Only the reference " + tagRowA + " is removed", "The image data stays: it is still referenced as acme/api:stable",
		"Nothing is sent to a registry", "Type 3.29.0 and press Enter",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("the confirmation lacks %q:\n%s", s, text)
		}
	}

	// Not consent: y, a bare Enter, the wrong word, the word "remove".
	b.keys("y")
	b.keys("backspace")
	b.keys("enter")
	for _, wrong := range []string{"3.29.1", "remove", "stable", tagRowA} {
		b.typeText(wrong)
		b.keys("enter")
		if b.m.dlg != d || !d.mismatch || !strings.Contains(b.dialogText(), `That is not "3.29.0". Nothing was sent.`) {
			t.Fatalf("%q was taken, or not refused in words:\n%s", wrong, b.dialogText())
		}
		for range wrong {
			b.keys("backspace")
		}
	}
	b.settle()
	b.noMutations("after the wrong word")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}

	b.typeText("3.29.0")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the dialog stayed open:\n%s", b.screen())
	}
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded || j.Title != "untag image" {
		t.Fatalf("job: %+v", j)
	}
	muts := b.srv.Mutations()
	if len(muts) != 1 || muts[0].Method != "DELETE" || muts[0].Path != "/images/"+tagRowA {
		t.Fatalf("mutations = %v, want exactly DELETE /images/%s", muts, tagRowA)
	}
	b.noForce()
	b.noRegistryRequests()
	if got := b.refsOf(ids.a); !slices.Equal(got, []string{"acme/api:stable"}) {
		t.Fatalf("the image must remain under its other reference: %v", got)
	}
	b.until("the row to go", func() bool { return !slices.Contains(b.rowNames(), tagRowA) })
	if !slices.Contains(b.rowNames(), "acme/api:stable") {
		t.Fatalf("rows: %v", b.rowNames())
	}
	if log := strings.Join(j.Log, "\n"); strings.Contains(log, "deleted") {
		t.Errorf("an untag reported a deletion: %s", log)
	}
}

func TestUntagRefusedBeforeAnyDialog(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})

	// The image's only tag: removing it would delete the image.
	b.selectRow(tagSolo)
	b.srv.ResetRequests()
	b.keys("u")
	b.settle()
	st := b.m.status.text
	if b.m.dlg != nil || !strings.Contains(st, "only tag") || !strings.Contains(st, "would delete the image") ||
		!strings.Contains(st, "d remove") || !strings.Contains(st, "Nothing was sent") {
		t.Fatalf("last tag: dialog %v status %q", b.m.dlg != nil, st)
	}
	b.noMutations("u on the last tag")
	if got := b.refsOf(ids.solo); !slices.Equal(got, []string{tagSolo}) {
		t.Fatalf("refs = %v", got)
	}

	// An untagged row has nothing to untag.
	b.selectRow(b.untaggedRow())
	b.srv.ResetRequests()
	b.keys("u")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "no tag to remove") || !strings.Contains(b.m.status.text, "nothing was sent") {
		t.Fatalf("untagged: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
	}
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("u on an untagged row sent %d request(s)", n)
	}

	// The other tag went away since the list was drawn: the fresh snapshot
	// refuses, whatever the row still says.
	if _, err := b.eng.RemoveImage(context.Background(), "acme/api:stable"); err != nil {
		t.Fatal(err)
	}
	b.selectRow(tagRowA)
	b.srv.ResetRequests()
	b.keys("u")
	b.settle()
	if b.m.dlg != nil || !strings.Contains(b.m.status.text, "only tag") {
		t.Fatalf("became the last tag: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
	}
	b.noMutations("u on what became the last tag")
	if n := len(b.m.jobs.List()); n != 0 {
		t.Fatalf("%d job(s)", n)
	}
}

func TestUntagRefusesAMovedTag(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	b.untagDialog(tagRowA)
	c := b.srv.MoveTag(tagRowA, 4_000_000)
	b.srv.ResetRequests()
	b.typeText("3.29.0")
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the tag moved") || !strings.Contains(j.Err, "nothing was removed") {
		t.Fatalf("job: %+v", j)
	}
	b.noMutations("a moved tag")
	if !b.srv.HasImage(c) || !slices.Equal(b.refsOf(tagRowA), []string{tagRowA}) || !slices.Equal(b.refsOf(ids.a), []string{"acme/api:stable"}) {
		t.Fatal("a moved tag was acted on")
	}
}

// Untag cannot become a deletion: the other reference vanishes after the
// confirmation, and the job refuses rather than delete the image.
func TestUntagCannotBecomeADeletion(t *testing.T) {
	b, ids := newTagBench(t, benchOpts{})
	b.untagDialog(tagRowA)
	if _, err := b.eng.RemoveImage(context.Background(), "acme/api:stable"); err != nil {
		t.Fatal(err)
	}
	b.srv.ResetRequests()
	b.typeText("3.29.0")
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "only reference") {
		t.Fatalf("job: %+v", j)
	}
	b.noMutations("an untag that would delete")
	if !b.srv.HasImage(ids.a) || !slices.Equal(b.refsOf(ids.a), []string{tagRowA}) {
		t.Fatal("the image was deleted, or its last reference removed")
	}
}

// Both actions are local, so --no-registry leaves them available — and
// neither sends anything a registry would see.
func TestTagAndUntagUnderNoRegistry(t *testing.T) {
	ran := countingHelper(t)
	b, ids := newTagBench(t, benchOpts{noRegistry: true, dockerConfig: helperConfig(t)})
	b.tagFormOn(tagRowA)
	b.typeText("host.test:5000/acme/api:9")
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("tag under --no-registry: %+v", j)
	}
	b.oneTagRequest(ids.a, "host.test:5000/acme/api", "9")
	b.noRegistryRequests()

	b.until("the new row", func() bool { return slices.Contains(b.rowNames(), "host.test:5000/acme/api:9") })
	b.untagDialog("host.test:5000/acme/api:9")
	b.typeText("9")
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("untag under --no-registry: %+v", j)
	}
	b.noRegistryRequests()
	b.noForce()
	if ran() {
		t.Fatal("a credential helper was run for a local tag")
	}
	if got := b.refsOf(ids.a); !slices.Equal(got, []string{tagRowA, "acme/api:stable"}) {
		t.Fatalf("refs = %v", got)
	}
}

// The tag form fits the smallest terminal the desk is laid out for, with
// its longest warning on screen.
func TestTagFormFitsTheMinimumTerminal(t *testing.T) {
	b, _ := newTagBench(t, benchOpts{width: MinWidth, height: MinHeight})
	d := b.tagFormOn(tagRowA)
	b.typeText("acme/api:latest")
	d.formErr = "acme/api:latest names image 0123456789ab: the warning that the tag will move has only just appeared. Read it, then press Enter again. Nothing was sent."
	lines, _, h := b.m.renderDialog()
	if h > MinHeight || len(lines) > MinHeight {
		t.Fatalf("the tag form is %d lines on a %d-line terminal:\n%s", h, MinHeight, b.screen())
	}
	text := b.dialogText()
	for _, s := range []string{"the tag will MOVE", "Enter tag", "Esc cancel", "has only just appeared"} {
		if !strings.Contains(text, s) {
			t.Errorf("at %dx%d the form lacks %q:\n%s", MinWidth, MinHeight, s, text)
		}
	}
	b.assertFrameGeometry()
}

// 5.1: the keys list documents t and u in the actions' own words, and the
// README names them, the rules and the captures.
func TestTagAndUntagAreDocumented(t *testing.T) {
	b, _ := newTagBench(t, benchOpts{width: 140, height: 50})
	help := strings.Join(b.m.helpLines(120), "\n")
	for _, want := range []string{
		tagAction.desc, untagAction.desc,
		"acme/api is acme/api:latest", "tags nothing if it", "on the last tag it",
		"use d remove", "--no-registry leaves them available",
		"untag, push: type the tag shown and press Enter",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the keys lack %q", want)
		}
	}
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		"`t` tag…", "`u` untag…", "### Tag and untag — local names, and nothing else",
		"**`acme/api:latest`**", "the tag will MOVE", "*the target changed*", "last tag", "`d` remove",
		"no registry is contacted", "`--no-registry` leaves", "tagged by its ID",
		"docs/screens/74-tag-form.txt", "docs/screens/75-tag-done.txt", "docs/screens/76-tag-move-warning.txt",
		"docs/screens/77-untag-confirm.txt", "docs/screens/78-untag-done.txt",
		"docs/screens/79-untag-last-tag-refused.txt", "docs/screens/80-tag-80x24-read-only.txt",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
}
