package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/registry"
)

// repoBench is a desk whose daemon holds three images of the sandbox's
// repository, served by a fake registry.
func repoBench(t *testing.T, o benchOpts) (*bench, *fakeregistry.Registry, string) {
	t.Helper()
	reg := fakeregistry.New()
	t.Cleanup(reg.Close)
	var repo string
	o.seed = func(s *fakeengine.Server) { repo = s.SeedRepository(reg) }
	if o.width == 0 {
		o.width, o.height = 160, 44
	}
	b := newBench(t, o)
	b.m.info.Architecture, b.m.info.OSType = "x86_64", "linux"
	return b, reg, repo
}

// registryCalls counts the registry's own requests (not the token realm's)
// whose path ends in suffix.
func registryCalls(reg *fakeregistry.Registry, suffix string) int {
	n := 0
	for _, r := range reg.Requests() {
		if !r.Realm && strings.Contains(r.Path, suffix) {
			n++
		}
	}
	return n
}

func (b *bench) selectImage(ref string) {
	b.t.Helper()
	b.kind(KindImages)
	for i, r := range b.m.rows[KindImages] {
		if r.imageRef == ref {
			b.m.selectIndex(i)
			b.m.afterChange()
			b.settle()
			return
		}
	}
	b.t.Fatalf("no image row %s: %v", ref, b.rowNames())
}

// described waits until the highlighted row has an answer.
func (b *bench) described(tag string) {
	b.t.Helper()
	b.until("a description of "+tag, func() bool {
		cur, ok := b.m.repoHighlighted()
		if !ok || cur != tag {
			return false
		}
		_, kept := b.m.rb.browser.Described(b.m.rb.repo, tag)
		return kept && b.m.rb.descTag == ""
	})
}

func (b *bench) repoMoveTo(tag string) {
	b.t.Helper()
	b.until("the tag list", func() bool { return b.m.rb.listing != nil })
	rows := b.m.repoRows()
	for i, t := range rows {
		if t == tag {
			b.m.repoMove(rows, i)
			b.dirty()
			return
		}
	}
	b.t.Fatalf("no row %s", tag)
}

// With no image highlighted, B opens on an empty name line and asks
// nothing; every key is text until Enter or Esc; Esc steps out, then closes.
func TestRepositoryNoRequestOnOpen(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{})
	b.kind(KindVolumes)
	b.m.focus = PaneDetails
	b.keys("B")
	b.settle()
	if !b.m.rb.open || !b.m.rb.editing || reg.Count() != 0 {
		t.Fatalf("open=%v editing=%v, %d registry requests", b.m.rb.open, b.m.rb.editing, reg.Count())
	}
	b.see("Type a repository", "type a repository")
	// Keys that mean something elsewhere are text here.
	b.typeText("qdBe?/")
	if b.m.quitting || !b.m.rb.open || b.m.dlg != nil || b.m.rb.name.Value() != "qdBe?/" || reg.Count() != 0 {
		t.Fatalf("a key in the name line acted: quitting=%v open=%v dlg=%v name=%q", b.m.quitting, b.m.rb.open, b.m.dlg != nil, b.m.rb.name.Value())
	}
	b.keys("esc") // steps out of the line, asking nothing
	if !b.m.rb.open || b.m.rb.editing || reg.Count() != 0 {
		t.Fatalf("Esc: open=%v editing=%v requests=%d", b.m.rb.open, b.m.rb.editing, reg.Count())
	}
	b.keys("esc") // then closes, the desk as it was
	if b.m.rb.open || b.m.focus != PaneDetails || b.m.kind != KindVolumes {
		t.Fatalf("closed onto focus %v kind %v", b.m.focus, b.m.kind)
	}

	// e, a name, Enter: the tag list is asked, and nothing else yet.
	b.keys("B")
	b.typeText(repo)
	b.keys("enter")
	b.until("the tag list", func() bool { return b.m.rb.listing != nil })
	if n := registryCalls(reg, "/tags/list"); n != 1 {
		t.Fatalf("%d tag list requests", n)
	}
	if registryCalls(reg, "/blobs/") != 0 {
		t.Fatal("opening read a config")
	}
	if len(b.srv.Mutations()) != 0 {
		t.Fatal("browsing sent a mutation")
	}
}

// B on an image asks its repository's tag list at once — one request —
// lands on the tag the image carries, and describes that row once the
// highlight rests: one manifest request a row, never repeated.
func TestRepositoryOnePerHighlightedRow(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{})
	b.selectImage(repo + ":3.28.2")
	b.m.focus = PaneList
	reg.Reset()
	b.keys("B")
	b.described("3.28.2")
	b.settle()
	if lists, manifests := registryCalls(reg, "/tags/list"), registryCalls(reg, "/manifests/"); lists != 1 || manifests != 1 {
		t.Fatalf("on open: %d tag list and %d manifest requests, want 1 and 1", lists, manifests)
	}
	if !strings.HasSuffix(reg.Requests()[len(reg.Requests())-1].Path, "/manifests/3.28.2") {
		t.Fatalf("described another row: %+v", reg.Requests())
	}
	b.see(repo, "3.28.2", "here", fmt.Sprintf("%d tags held", 100), "Tab asks for the next page", "text order", "never by recency")

	// One down: one more request.
	b.keys("j")
	b.described("3.28.3")
	if n := registryCalls(reg, "/manifests/"); n != 2 {
		t.Fatalf("after moving one row: %d manifest requests", n)
	}
	// Back: kept, nothing asked.
	b.keys("k")
	b.settle()
	if n := registryCalls(reg, "/manifests/"); n != 2 {
		t.Fatalf("returning to a described row asked again: %d", n)
	}
	// Holding ↓ through rows: only where the highlight rests is asked.
	b.keys("j", "j", "j", "j", "j")
	b.settle()
	if n := registryCalls(reg, "/manifests/"); n != 3 {
		t.Fatalf("passing rows were described: %d requests", n)
	}
	if registryCalls(reg, "/blobs/") != 0 {
		t.Fatal("a description read a config")
	}

	// Close: the desk as it was.
	b.keys("B")
	if b.m.rb.open || b.m.focus != PaneList || b.m.kind != KindImages {
		t.Fatalf("closed onto %v %v", b.m.focus, b.m.kind)
	}
	if r, _ := b.m.currentRow(); r.imageRef != repo+":3.28.2" {
		t.Fatalf("the highlight moved to %q", r.imageRef)
	}
	// Reopened with no image: an empty name line over nothing.
	b.kind(KindVolumes)
	b.keys("B")
	if !b.m.rb.editing || b.m.rb.listing != nil || b.m.rb.name.Value() != "" {
		t.Fatalf("reopened with no image: editing=%v listing=%v name=%q", b.m.rb.editing, b.m.rb.listing != nil, b.m.rb.name.Value())
	}
	b.keys("esc", "esc")
	b.selectImage(repo + ":3.28.2")
	b.m.focus = PaneList

	// Reopened: the descriptions are kept; the list is asked again.
	reg.Reset()
	b.keys("B")
	b.described("3.28.2")
	b.settle()
	if registryCalls(reg, "/manifests/") != 0 {
		t.Error("a kept description was asked again on reopening")
	}
}

func TestRepositoryJoinAndPlatforms(t *testing.T) {
	b, _, repo := repoBench(t, benchOpts{width: 200, height: 50})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.described("3.28.2")
	for tag, want := range map[string]string{
		"3.28.2":      "here",
		"3.28":        "here, the tag moved upstream",
		"3.27.0":      "same build as stable",
		"3.10.0":      "-",
		"3.28.3":      "-",
		"latest":      "-",
		"edge":        "-",
		"3.29.0-rc.1": "-",
	} {
		b.repoMoveTo(tag)
		b.described(tag)
		cells, _ := b.m.repoCells(tag, time.Now())
		if cells[3] != want {
			t.Errorf("%s: HERE %q, want %q", tag, cells[3], want)
		}
	}
	// arm64 only: said before any pull, on the row and below it.
	b.repoMoveTo("3.29.0-rc.1")
	b.see("no amd64 · arm64/v8", "No build for linux/amd64, this daemon's platform")
	b.keys("enter")
	b.until("the refusal", func() bool { return b.m.dlg != nil })
	if p := b.m.dlg.plan; p.Runnable() || !strings.Contains(strings.Join(p.Blockers, ""), "no build for linux/amd64") {
		t.Fatalf("pull of an arm-only tag: %+v", p)
	}
	b.see("REFUSED")
	b.keys("esc")

	// The order is text order, numbers by value.
	rows := b.m.repoRows()
	pos := map[string]int{}
	for i, r := range rows {
		pos[r] = i
	}
	if !(pos["3"] < pos["3.9.0"] && pos["3.9.0"] < pos["3.10.0"] && pos["3.10.0"] < pos["3.27.0"] && pos["3.28.3"] < pos["3.29.0-rc.1"] && pos["edge"] < pos["latest"]) {
		t.Errorf("not natural order: %v", rows[:14])
	}
	for _, th := range themes {
		b.m.setTheme(th)
		b.assertFrameGeometry()
	}
}

// Tab asks for the page the registry linked; the filter works on what is held.
func TestRepositoryPagingAndFilter(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.described("3.28.2")
	if l := b.m.rb.listing; len(l.Tags) != 100 || !l.More() {
		t.Fatalf("first page: %d held, more=%v", len(l.Tags), l.More())
	}
	b.keys("tab")
	b.until("the second page", func() bool { return len(b.m.rb.listing.Tags) == 105 })
	var queries []string
	for _, r := range reg.Requests() {
		if strings.HasSuffix(r.Path, "/tags/list") {
			queries = append(queries, r.Query)
		}
	}
	if len(queries) != 2 || queries[0] != "n=100" || !strings.HasPrefix(queries[1], "n=100&last=nightly-") {
		t.Fatalf("pages asked: %v", queries)
	}
	b.see("105 tags held", "every tag the registry lists")
	if cur, _ := b.m.repoHighlighted(); cur != "3.28.2" {
		t.Errorf("a page moved the highlight to %s", cur)
	}
	b.keys("tab")
	b.see("That is every tag the registry lists")

	b.keys("/")
	b.typeText("3.28")
	b.keys("enter")
	if rows := b.m.repoRows(); len(rows) != 5 || rows[0] != "3.28" {
		t.Fatalf("filter: %v", rows)
	}
	b.see("5 of 105 held")
	b.keys("esc") // the filter first
	if !b.m.rb.open || b.m.rb.query != "" {
		t.Fatal("Esc did not clear the filter first")
	}
	b.keys("esc")
	if b.m.rb.open {
		t.Fatal("Esc did not close")
	}
}

// Enter opens the pinned pull: the full reference, the digest fetched, the
// tag typed. It pulls exactly name:tag@digest, and the row then says here.
func TestRepositoryPinnedPull(t *testing.T) {
	b, _, repo := repoBench(t, benchOpts{width: 200, height: 50})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.repoMoveTo("3.28.3")
	b.described("3.28.3")
	d, _ := b.m.rb.browser.Described(b.m.rb.repo, "3.28.3")
	want := repo + ":3.28.3@" + d.Digest
	b.keys("enter")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	p := b.m.dlg.plan
	if p.Tier != plan.TierTyped || p.Word != "3.28.3" || p.Steps[0].ID != want {
		t.Fatalf("plan: tier %v word %q step %q", p.Tier, p.Word, p.Steps[0].ID)
	}
	// The dialog's own text, its lines joined: the reference is wrapped,
	// never cut.
	box, _, _ := b.m.renderDialog()
	var text strings.Builder
	for _, l := range box {
		text.WriteString(strings.TrimSpace(strings.Trim(ansi.Strip(l), "┃━┏┓┗┛ ")))
	}
	if !strings.Contains(text.String(), want) {
		t.Fatalf("the dialog does not show the full reference %s:\n%s", want, b.screen())
	}
	b.see("fetches the digest, not the tag", "Type 3.28.3 and press Enter")

	// The wrong word, the digest, or nothing: refused.
	b.typeText("remove")
	b.keys("enter")
	if b.m.dlg == nil || !b.m.dlg.mismatch {
		t.Fatal("a wrong word was accepted")
	}
	for range "remove" {
		b.keys("backspace")
	}
	b.typeText("3.28.3")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the tag was not accepted:\n%s", b.screen())
	}
	b.until("the pull to finish", func() bool {
		js := b.m.jobs.List()
		return len(js) == 1 && js[0].State.Finished()
	})
	if j := b.m.jobs.List()[0]; j.State != jobs.Succeeded {
		t.Fatalf("pull: %+v", j)
	}
	pulls := 0
	for _, r := range b.srv.Mutations() {
		if r.Path == "/images/create" {
			pulls++
			if !strings.Contains(r.Query, "tag=sha256%3A"+strings.TrimPrefix(d.Digest, "sha256:")) {
				t.Errorf("the pull did not ask for the digest: %s", r)
			}
		}
	}
	if pulls != 1 || len(b.srv.Mutations()) != 1 {
		t.Fatalf("mutations: %v", b.srv.Mutations())
	}
	b.until("the row to say here", func() bool {
		cells, _ := b.m.repoCells("3.28.3", time.Now())
		return cells[3] == "here"
	})
}

func TestRepositoryReadOnly(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{readOnly: true})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.described("3.28.2")
	b.notSee("pull by digest…")
	b.keys("enter")
	b.settle()
	if b.m.dlg != nil {
		t.Fatal("read-only opened a pull confirmation")
	}
	b.see("Read-only mode", "Nothing was sent")
	b.keys("d")
	b.until("the build times", func() bool { return b.m.rb.batch == nil && len(b.m.jobs.List()) == 1 })
	if j := b.m.jobs.List()[0]; j.State != jobs.Succeeded {
		t.Fatalf("build times in read-only: %+v", j)
	}
	if len(b.srv.Mutations()) != 0 {
		t.Fatalf("mutations: %v", b.srv.Mutations())
	}
	for _, r := range reg.Requests() {
		if r.Method != "GET" {
			t.Errorf("registry request %s %s", r.Method, r.Path)
		}
	}
}

func TestRepositoryNoRegistry(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{noRegistry: true})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.settle()
	if b.m.rb.open {
		t.Fatal("the view opened under --no-registry")
	}
	b.see("--no-registry", "does not open")
	if reg.Count() != 0 {
		t.Fatalf("%d registry requests", reg.Count())
	}
}

func TestRepository401(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{width: 200, height: 50})
	reg.Conceal(fakeengine.SandboxRepository)
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.until("the refusal", func() bool { return b.m.rb.listErr != "" })
	b.see("the registry answered 401 - it does not have this repository, or this credential is not allowed to see it")
	scr := b.screen()
	for _, claim := range []string{"no such", "does not exist", "not found"} {
		if strings.Contains(scr, claim) {
			t.Errorf("a 401 claimed absence (%q):\n%s", claim, scr)
		}
	}
}

// d reads build times for the rows on screen as one job; a quota stops it,
// and what was read is kept.
func TestRepositoryBuildTimesQuota(t *testing.T) {
	b, reg, repo := repoBench(t, benchOpts{width: 200, height: 50})
	b.selectImage(repo + ":3.28.2")
	b.keys("B")
	b.described("3.28.2")
	b.keys("g")
	b.settle()
	reg.Reset()
	reg.SetQuota(8) // 7 answered: the first two tags in full, part of the third
	b.screen()      // d reads the rows on screen: draw them
	b.keys("d")
	b.until("the batch to stop", func() bool { return b.m.rb.batch == nil && len(b.m.jobs.List()) == 1 })
	j, _ := b.m.jobs.Get(b.m.jobs.List()[0].ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "quota") || !strings.Contains(j.Err, "resets at") {
		t.Fatalf("batch: %+v", j)
	}
	if n := reg.Count(); n != 8 {
		t.Errorf("%d requests: the batch went on after the quota", n)
	}
	rows := b.m.repoRows()
	held := 0
	for _, tag := range rows[:5] {
		if bt, ok := b.m.rb.browser.TimeOf(b.m.rb.repo, tag); ok {
			held++
			if bt.Platform != "linux/amd64" || bt.Created.IsZero() {
				t.Errorf("%s: %+v", tag, bt)
			}
		}
	}
	if held < 2 {
		t.Errorf("%d build times kept before the quota", held)
	}
	b.see("linux/amd64 build's config")

	// Cancelled midway: c stops it before the next request.
	reg.SetQuota(0)
	b.keys("r")
	b.described(b.m.rb.cursorKey)
	b.screen()
	b.keys("d", "c")
	b.until("the batch to end", func() bool { return b.m.rb.batch == nil })
	if j := b.m.jobs.List()[0]; j.State != jobs.Cancelled && j.State != jobs.Succeeded {
		t.Errorf("cancelled: %+v", j)
	}
}

// Images › Upstream's moved verdict pulls through the same plan: P opens
// the pinned pull, typed with the tag.
func TestUpstreamMovedPullsByDigest(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	var ref string
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		ref = pushed(s, reg, "team/api", "1.4")
		created := time.Now().Add(-time.Hour)
		reg.Push("team/api", "1.4", true, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &created, DiffIDs: []string{"sha256:new"}})
	}})
	b.openUpstream(ref)
	v := b.verdict(ref)
	if v.State != freshness.Moved {
		t.Fatalf("verdict %+v", v)
	}
	b.see("P pulls the build the tag names now")
	b.keys("P")
	b.until("the confirmation", func() bool { return b.m.dlg != nil })
	p := b.m.dlg.plan
	r, _ := registry.ParseRef(ref)
	want, _ := plan.PinnedRef(plan.Pinned{Name: r.Name, Tag: "1.4", Digest: v.RemoteDigest})
	if p.Title != "Pull image by digest" || p.Word != "1.4" || p.Steps[0].ID != want || p.Tier != plan.TierTyped {
		t.Fatalf("P on moved: %+v", p)
	}
	b.keys("esc")

	// Elsewhere P is the pull form, as before.
	b.m.focus = PaneList
	b.keys("[")
	b.keys("P")
	if b.m.dlg == nil || b.m.dlg.kind != dlgForm {
		t.Fatal("P off the Upstream tab is not the pull form")
	}
}

func TestRepositoryLayouts(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		for _, ro := range []bool{false, true} {
			b, _, repo := repoBench(t, benchOpts{width: size[0], height: size[1], readOnly: ro})
			b.selectImage(repo + ":3.28.2")
			b.keys("B")
			b.described("3.28.2")
			for _, th := range themes {
				b.m.setTheme(th)
				b.assertFrameGeometry()
			}
			b.keys("e")
			b.assertFrameGeometry()
			b.keys("esc", "/")
			b.assertFrameGeometry()
		}
	}
}
