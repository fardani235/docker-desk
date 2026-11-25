package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

const (
	e2eTagRow  = "acme/api:3.29.0" // also acme/api:stable
	e2eTagSolo = "acme/solo:1"     // an image with one tag
)

func tagSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.AddImage(48_000_000, 2*24*time.Hour, e2eTagRow, "acme/api:stable")
	s.AddImage(52_000_000, 9*24*time.Hour, "acme/api:latest", "acme/api:previous")
	s.AddImage(6_000_000, 5*24*time.Hour, e2eTagSolo)
}

func (d *desk) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.keys(string(r))
	}
}

func (d *desk) mutations() []string {
	var out []string
	for _, r := range d.srv.Mutations() {
		out = append(out, r.String())
	}
	return out
}

// noRegistryRequest fails if anything the daemon would take to a registry
// was sent.
func (d *desk) noRegistryRequest() {
	d.t.Helper()
	for _, r := range d.srv.Requests() {
		if strings.Contains(r.Path, "/distribution/") || strings.HasSuffix(r.Path, "/push") || r.Path == "/images/create" || r.Path == "/auth" {
			d.t.Fatalf("a registry request was sent: %s", r)
		}
	}
}

// TestTagAndUntagImage is the two local actions through the real binary: t
// opens the form, the reference is typed and restated, Enter starts one
// job and the new row appears; the form names a move without making it; u
// asks for the tag, refuses y and removes one reference, and the image
// stays under its others.
func TestTagAndUntagImage(t *testing.T) {
	srv := startFake(t, tagSeed)
	d := launch(t, srv, 42, 150, "--no-registry") // local actions: the flag leaves them available
	d.imagesRow("acme/api", e2eTagRow)
	d.keys("2", "t")
	d.see("Tag image", "New reference, repository[:tag]:", "Local only: no registry is contacted", "acme/api:3.29.0, acme/api:stable")
	d.typeText("acme/api:release-7")
	d.see("will create acme/api:release-7", "No image has this reference: it is created.")
	d.shot("74-tag-form")
	if m := d.mutations(); len(m) != 0 {
		t.Fatalf("typing into the tag form sent %v", m)
	}
	d.keys(ptyscreen.Enter)
	d.gone("New reference, repository[:tag]:")
	d.see("✓ tag image", "release-7", "img tag")
	id := ""
	if tr := srv.TagRequests(); len(tr) != 1 || tr[0].Ref != "acme/api:release-7" || tr[0].MovedFrom != "" || !strings.HasPrefix(tr[0].Source, "sha256:") {
		t.Fatalf("tag requests: %+v", tr)
	} else {
		id = tr[0].ImageID
	}
	if m := d.mutations(); len(m) != 1 || m[0] != "POST /images/sha256:"+id+"/tag?repo=docker.io%2Facme%2Fapi&tag=release-7" {
		t.Fatalf("mutations: %v", m)
	}
	d.shot("75-tag-done")

	// A target another image holds: the form names it and the move, and Esc
	// leaves everything as it was.
	d.keys("t")
	d.see("Tag image")
	d.typeText("acme/api")
	// The warning wraps: its parts are looked for, not the whole sentence.
	d.see("will create acme/api:latest", "It names image", "which keeps its 1 other reference", "MOVE to image "+id[:12])
	d.shot("76-tag-move-warning")
	d.keys(ptyscreen.Esc)
	d.gone("MOVE to image")
	if m := d.mutations(); len(m) != 1 {
		t.Fatalf("the move warning, declined, sent %v", m)
	}

	// Untag: the word is the tag. y is not consent.
	d.keys("u")
	d.see("Untag image", "Only the reference "+e2eTagRow+" is removed", "The image data stays", "Type 3.29.0 and press Enter")
	d.shot("77-untag-confirm")
	d.consent("y", ptyscreen.Enter)
	d.see(`That is not "3.29.0". Nothing was sent.`)
	d.keys("\x7f")
	if m := d.mutations(); len(m) != 1 {
		t.Fatalf("y sent %v", m)
	}
	d.typeText("3.29.0")
	d.keys(ptyscreen.Enter)
	d.gone("Type 3.29.0 and press Enter")
	d.see("✓ untag image", "img untag")
	if m := d.mutations(); len(m) != 2 || m[1] != "DELETE /images/"+e2eTagRow {
		t.Fatalf("mutations: %v", m)
	}
	refs := srv.ImageRefs()
	if slices.Contains(refs, e2eTagRow) || !slices.Contains(refs, "acme/api:stable") || !slices.Contains(refs, "acme/api:release-7") {
		t.Fatalf("refs after the untag: %v", refs)
	}
	if !srv.HasImage(id) {
		t.Fatal("the untag deleted the image")
	}
	d.gone("  3.29.0  ") // the list's row is gone; the image is still listed under stable
	d.see("stable", "release-7")
	d.shot("78-untag-done")
	d.noRegistryRequest()
	for _, m := range d.mutations() {
		if strings.Contains(strings.ToLower(m), "force") {
			t.Fatalf("a forced request: %s", m)
		}
	}
	d.keys("q")
	d.quit()
}

// On an image's last tag, u refuses before any dialog and points to d.
func TestUntagLastTagRefused(t *testing.T) {
	srv := startFake(t, tagSeed)
	d := launch(t, srv, 42, 150)
	d.imagesRow("acme/solo", e2eTagSolo)
	d.keys("2", "u")
	d.see("is the image's only tag", "would delete the image", "use d remove")
	d.shot("79-untag-last-tag-refused")
	if strings.Contains(d.term.Screen(), "Untag image") {
		t.Fatalf("a confirmation opened on the last tag:\n%s", d.term.Screen())
	}
	if m := d.mutations(); len(m) != 0 {
		t.Fatalf("u on the last tag sent %v", m)
	}
	if !srv.HasImage(e2eTagSolo) {
		t.Fatal("the image is gone")
	}
	d.keys("q")
	d.quit()
}

// At 80×24, read-only refuses t and u with nothing sent.
func TestTagAndUntagReadOnly(t *testing.T) {
	srv := startFake(t, tagSeed)
	d := launch(t, srv, 24, 80, "--read-only")
	d.see("Containers")
	d.keys("1", ptyscreen.Down, "2", "/")
	d.typeText("acme/api")
	d.keys(ptyscreen.Enter)
	d.see("acme/api")
	d.keys("t")
	d.see("Read-only mode", "tag…", "Nothing was sent")
	d.shot("80-tag-80x24-read-only")
	d.keys("u")
	d.see("Read-only mode", "untag…", "Nothing was sent")
	if strings.Contains(d.term.Screen(), "New reference") || strings.Contains(d.term.Screen(), "Untag image") {
		t.Fatalf("read-only opened a dialog:\n%s", d.term.Screen())
	}
	if m := d.mutations(); len(m) != 0 {
		t.Fatalf("read-only sent %v", m)
	}
	if len(srv.TagRequests()) != 0 {
		t.Fatal("read-only tagged")
	}
	d.keys("q")
	d.quit()
}
