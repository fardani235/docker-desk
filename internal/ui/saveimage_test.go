package ui

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// ---- helpers ----------------------------------------------------------------------

// saveDay is the clock the save tests date the prefilled path with.
var saveDay = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// saveBench is a bench whose ~ is a fresh folder and whose clock is fixed.
// untagged, when non-nil, receives the ID of an extra untagged image.
func saveBench(t *testing.T, o benchOpts, untagged *string) (*bench, string) {
	t.Helper()
	if o.seed == nil {
		o.seed = func(s *fakeengine.Server) {
			s.SeedDemo()
			id := s.AddImage(5_000_000, time.Hour)
			if untagged != nil {
				*untagged = id
			}
		}
	}
	b := newBench(t, o)
	home := t.TempDir()
	b.m.opts.HomeDir, b.m.opts.WorkDir = home, home
	b.m.opts.Now = func() time.Time { return saveDay }
	b.kind(KindImages)
	b.m.focus = PaneList
	return b, home
}

func (b *bench) archiveGets() []fakeengine.Request {
	var out []fakeengine.Request
	for _, r := range b.srv.Requests() {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/get") {
			out = append(out, r)
		}
	}
	return out
}

func (b *bench) imageInspects() int {
	return b.srv.CountRequests(http.MethodGet, `^/images/.+/json$`)
}

// detailLoaded waits for the Details pane's inspect of the highlighted image.
func (b *bench) detailLoaded() {
	b.t.Helper()
	b.until("the image's details", func() bool {
		return b.m.detail.image != nil && !b.m.detail.loading && b.m.detail.key == b.m.wantDetail
	})
}

// saveForm highlights name, presses w and waits for the form.
func (b *bench) saveForm(name string) *dialog {
	b.t.Helper()
	b.selectRow(name)
	b.m.focus = PaneList
	b.keys("w")
	b.until("the save form", func() bool {
		return b.m.dlg != nil && b.m.dlg.kind == dlgForm && b.m.dlg.form == formSaveImage
	})
	return b.m.dlg
}

// setPath replaces the form's path.
func (b *bench) setPath(p string) {
	in := &b.m.dlg.fields[0].rows[0]
	in.SetValue(p)
	in.CursorEnd()
}

// submitSave presses Enter in the save form and returns the job it started.
func (b *bench) submitSave() jobs.Job {
	b.t.Helper()
	before := len(b.m.jobs.List())
	b.keys("enter")
	if b.m.dlg != nil {
		b.t.Fatalf("the form stayed open: %q", b.m.dlg.formErr)
	}
	if len(b.m.jobs.List()) != before+1 {
		b.t.Fatalf("no job was started:\n%s", b.screen())
	}
	return b.lastJob()
}

func jobLog(j jobs.Job) string { return strings.Join(j.Log, "\n") }

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func imageID(t *testing.T, b *bench, ref string) string {
	t.Helper()
	d, err := b.eng.InspectImage(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// dirEntries lists a folder's names.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}

// ---- 2.1 the action and the platform -------------------------------------------------

func TestSaveArchiveKeyAsksNothingUntilEnter(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.detailLoaded()
	b.srv.ResetRequests()
	d := b.saveForm("nginx:1.27")
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("w with the details loaded sent %d request(s): %v", n, b.srv.Requests())
	}
	if d.si.imageRef != "nginx:1.27" || d.si.platform.String() != "linux/amd64" {
		t.Fatalf("form bound to %+v", d.si)
	}
	b.see("Save the image archive")
	b.run(50 * time.Millisecond)
	if n := len(b.archiveGets()); n != 0 {
		t.Fatalf("the archive was asked for before Enter: %d", n)
	}
	b.keys("esc")
	if b.m.dlg != nil || len(b.srv.Requests()) != 0 || len(dirEntries(t, home)) != 0 || len(b.m.jobs.List()) != 0 {
		t.Fatalf("Esc: dialog %v, requests %v, files %v", b.m.dlg, b.srv.Requests(), dirEntries(t, home))
	}
}

func TestSaveArchiveInspectsOnceWithoutDetails(t *testing.T) {
	b, _ := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.detailLoaded()
	b.m.detail.image = nil // as if the Details pane had not inspected it yet
	b.srv.ResetRequests()
	b.step(keyMsg("w"))
	if !strings.Contains(b.m.status.text, "Reading the image's platform") || b.m.si.pending == nil {
		t.Fatalf("status %q while reading", b.m.status.text)
	}
	// A second w while it reads asks nothing more.
	b.step(keyMsg("w"))
	b.until("the save form", func() bool { return b.m.dlg != nil && b.m.dlg.form == formSaveImage })
	if n := b.imageInspects(); n != 1 {
		t.Fatalf("%d inspects, want exactly one", n)
	}
	if n := len(b.srv.Requests()); n != 1 {
		t.Fatalf("requests %v, want the one inspect", b.srv.Requests())
	}
	if b.m.dlg.si.platform.String() != "linux/amd64" {
		t.Fatalf("platform %v", b.m.dlg.si.platform)
	}
}

func TestSaveArchiveFailedInspectOpensNothing(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.detailLoaded()
	b.m.detail.image = nil
	b.srv.FailNext(http.MethodGet, `^/images/.+/json$`, http.StatusInternalServerError, "the image store is locked", 1)
	b.srv.ResetRequests()
	b.step(keyMsg("w"))
	b.until("the inspect's answer", func() bool { return b.m.si.pending == nil })
	b.settle()
	if b.m.dlg != nil {
		t.Fatalf("a dialog opened after a failed inspect: %v", b.m.dlg.kind)
	}
	if s := b.m.status.text; !strings.Contains(s, "the image store is locked") || !strings.Contains(s, "Nothing was written") {
		t.Fatalf("status %q", s)
	}
	if len(b.archiveGets()) != 0 || len(dirEntries(t, home)) != 0 {
		t.Fatal("something was read or written")
	}
}

// An answer that arrives after focus moved to another daemon opens nothing.
func TestSaveArchiveInspectAnswerForAnotherFocusIsDropped(t *testing.T) {
	b, _ := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	m := b.m
	b.step(saveInspectMsg{seq: m.si.seq, gen: m.gen + 1, target: saveImage{imageID: "x", name: "x"}})
	b.step(saveInspectMsg{seq: m.si.seq + 5, gen: m.gen, target: saveImage{imageID: "x", name: "x"}})
	if m.dlg != nil {
		t.Fatal("a stale answer opened a form")
	}
}

// The platform is the one the image is inspected as, in the form and in
// the request.
func TestSaveArchivePlatform(t *testing.T) {
	b, home := saveBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddImage(9_000_000, time.Hour, "edge/arm:1")
		f := fixture.Plain("edge/arm:1")
		f.Architecture, f.Variant = "arm64", "v8"
		s.SetImageFixture("edge/arm:1", f)
	}}, nil)
	d := b.saveForm("edge/arm:1")
	if got := d.si.platform.String(); got != "linux/arm64/v8" {
		t.Fatalf("form platform %q", got)
	}
	if txt := b.dialogText(); !strings.Contains(txt, "platform linux/arm64/v8") {
		t.Fatalf("form does not name the platform: %s", txt)
	}
	b.setPath(filepath.Join(home, "arm.tar"))
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s\n%s", j.State, j.Err, jobLog(j))
	}
	gets := b.archiveGets()
	if len(gets) != 1 {
		t.Fatalf("archive requests %v", gets)
	}
	q, _ := url.ParseQuery(gets[0].Query)
	if !strings.Contains(q.Get("platform"), `"architecture":"arm64"`) || !strings.Contains(q.Get("platform"), `"variant":"v8"`) {
		t.Fatalf("platform asked for: %q", q.Get("platform"))
	}
	if !strings.Contains(jobLog(j), "linux/arm64/v8") {
		t.Fatalf("the job does not name the platform:\n%s", jobLog(j))
	}
}

// ---- 2.2 the job ----------------------------------------------------------------

func TestSaveArchiveWritesTheFixture(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	archive := randomBytes(t, 300<<10) // several 32 KiB chunks
	b.srv.SetImageArchive("nginx:1.27", archive)
	id := imageID(t, b, "nginx:1.27")
	b.saveForm("nginx:1.27")
	dest := filepath.Join(home, "nginx.tar")
	b.setPath(dest)
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s\n%s", j.State, j.Err, jobLog(j))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, archive) {
		t.Fatalf("the file has %d bytes that are not the daemon's %d", len(got), len(archive))
	}
	fi, _ := os.Stat(dest)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if names := dirEntries(t, home); len(names) != 1 {
		t.Errorf("left behind: %v", names)
	}

	// One pin, one archive GET by the reference with the platform, and
	// nothing else.
	gets := b.archiveGets()
	if len(gets) != 1 {
		t.Fatalf("archive requests %v", gets)
	}
	q, _ := url.ParseQuery(gets[0].Query)
	if q.Get("names") != "nginx:1.27" {
		t.Errorf("saved %q, want the reference", q.Get("names"))
	}
	if p := q.Get("platform"); !strings.Contains(p, `"os":"linux"`) || !strings.Contains(p, `"architecture":"amd64"`) {
		t.Errorf("platform %q", p)
	}
	// The save's own requests: one pin by the reference, then the archive.
	// (Any finished job has the lists read again afterwards: GETs only.)
	reqs := b.srv.Requests()
	if len(reqs) < 2 || reqs[0].Path != "/images/nginx:1.27/json" || !strings.HasSuffix(reqs[1].Path, "/get") {
		t.Errorf("requests %v, want the pin and then the archive", reqs)
	}
	if n := b.srv.CountRequests(http.MethodGet, `^/images/nginx:1.27/json$`); n != 1 {
		t.Errorf("%d pins, want one", n)
	}
	for _, r := range reqs {
		if r.Method != http.MethodGet {
			t.Errorf("sent %s", r)
		}
	}
	b.noMutations("saving an archive")

	log := jobLog(j)
	for _, want := range []string{"nginx:1.27", engine.ShortID(id), "platform linux/amd64", dest, "mode 0600", "tagged nginx:1.27", "only read (GET)"} {
		if !strings.Contains(log, want) {
			t.Errorf("the job log does not say %q:\n%s", want, log)
		}
	}
	if !strings.Contains(j.Status, "wrote") {
		t.Errorf("status %q", j.Status)
	}
}

func TestSaveArchiveUntaggedByID(t *testing.T) {
	var id string
	b, home := saveBench(t, benchOpts{}, &id)
	archive := randomBytes(t, 40<<10)
	b.srv.SetImageArchive(id, archive)
	d := b.saveForm("<untagged> " + engine.ShortID(id))
	if d.si.imageRef != "" {
		t.Fatalf("untagged row has reference %q", d.si.imageRef)
	}
	dest := filepath.Join(home, "untagged.tar")
	b.setPath(dest)
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	gets := b.archiveGets()
	q, _ := url.ParseQuery(gets[0].Query)
	if len(gets) != 1 || strings.TrimPrefix(q.Get("names"), "sha256:") != id {
		t.Fatalf("archive requests %v", gets)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, archive) {
		t.Fatal("not the daemon's archive")
	}
	if !strings.Contains(jobLog(j), "untagged") {
		t.Fatalf("log:\n%s", jobLog(j))
	}
}

// ---- 2.3 cancellation and failure ----------------------------------------------------

func TestSaveArchiveCancelMidStream(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.srv.SetImageArchive("nginx:1.27", randomBytes(t, 2<<20)) // 64 chunks
	b.srv.ExportChunkDelay = 30 * time.Millisecond
	b.saveForm("nginx:1.27")
	dest := filepath.Join(home, "nginx.tar")
	b.setPath(dest)
	j := b.submitSave()
	b.until("part of the archive written", func() bool {
		x, _ := b.m.jobs.Get(j.ID)
		names := dirEntries(t, home)
		return strings.HasPrefix(x.Status, "wrote") && len(names) == 1 && strings.HasPrefix(names[0], ".docker-desk-export-")
	})
	b.m.jobs.Cancel(j.ID)
	j = b.waitJob(j.ID)
	if j.State != jobs.Cancelled {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	if names := dirEntries(t, home); len(names) != 0 {
		t.Fatalf("left behind after cancel: %v", names)
	}
	log := jobLog(j)
	if !strings.Contains(log, "partial archive") || !strings.Contains(log, "nothing remains at "+dest) || !strings.Contains(log, "stream was closed") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestSaveArchiveDaemonError(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.saveForm("nginx:1.27")
	b.setPath(filepath.Join(home, "nginx.tar"))
	b.srv.FailNext(http.MethodGet, `/get$`, http.StatusInternalServerError, "layer store is corrupt", 1)
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "layer store is corrupt") {
		t.Fatalf("job %v: %q", j.State, j.Err)
	}
	if names := dirEntries(t, home); len(names) != 0 {
		t.Fatalf("left behind: %v", names)
	}
	if !strings.Contains(jobLog(j), "nothing was written on this machine") {
		t.Fatalf("log:\n%s", jobLog(j))
	}
}

func TestSaveArchiveTagMoved(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	old := imageID(t, b, "nginx:1.27")
	b.saveForm("nginx:1.27")
	b.setPath(filepath.Join(home, "nginx.tar"))
	moved := b.srv.MoveTag("nginx:1.27", 1_000)
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the tag moved") ||
		!strings.Contains(j.Err, engine.ShortID(moved)) || !strings.Contains(j.Err, engine.ShortID(old)) {
		t.Fatalf("job %v: %q", j.State, j.Err)
	}
	if n := len(b.archiveGets()); n != 0 {
		t.Fatalf("the archive was asked for after the tag moved: %d", n)
	}
	if names := dirEntries(t, home); len(names) != 0 {
		t.Fatalf("written: %v", names)
	}
}

// A local write failure is the job's reason, and nothing is left.
func TestSaveArchiveWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0500 folder")
	}
	b, home := saveBench(t, benchOpts{}, nil)
	dir := filepath.Join(home, "ro")
	os.Mkdir(dir, 0o700)
	b.saveForm("nginx:1.27")
	b.setPath(filepath.Join(dir, "nginx.tar"))
	os.Chmod(dir, 0o500) // after the form's check: the job meets it
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "permission denied") {
		t.Fatalf("job %v: %q", j.State, j.Err)
	}
	if n := len(b.archiveGets()); n != 0 {
		t.Fatalf("the archive was asked for although nothing could be written: %d", n)
	}
	os.Chmod(dir, 0o700)
	if names := dirEntries(t, dir); len(names) != 0 {
		t.Fatalf("left behind: %v", names)
	}
}

// A file that appears at the destination while the archive streams is
// never replaced.
func TestSaveArchiveDestinationTakenWhileWriting(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.srv.SetImageArchive("nginx:1.27", randomBytes(t, 512<<10))
	b.srv.ExportChunkDelay = 20 * time.Millisecond
	b.saveForm("nginx:1.27")
	dest := filepath.Join(home, "nginx.tar")
	b.setPath(dest)
	j := b.submitSave()
	b.until("the stream to begin", func() bool {
		x, _ := b.m.jobs.Get(j.ID)
		return strings.HasPrefix(x.Status, "wrote")
	})
	if err := os.WriteFile(dest, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	j = b.waitJob(j.ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "appeared there while writing") {
		t.Fatalf("job %v: %q", j.State, j.Err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "theirs" {
		t.Fatalf("the file that appeared was replaced: %d bytes", len(got))
	}
	if names := dirEntries(t, home); len(names) != 1 {
		t.Fatalf("left behind: %v", names)
	}
	if !strings.Contains(jobLog(j), "it was not replaced") {
		t.Fatalf("log:\n%s", jobLog(j))
	}
}

// ---- 3.1 the form ---------------------------------------------------------------

func TestSaveArchiveFormTagged(t *testing.T) {
	b, _ := saveBench(t, benchOpts{}, nil)
	id := imageID(t, b, "nginx:1.27")
	d := b.saveForm("nginx:1.27")
	if got, want := d.formText(), "~/docker-desk-image-nginx-1.27-20261124.tar"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	txt := b.dialogText()
	for _, want := range []string{
		"Save the image archive", "image nginx:1.27", engine.ShortID(id),
		"the reference nginx:1.27: the archive keeps the tag", "platform linux/amd64",
		"tar", "mode 0600", "never replaced", "the folder must exist", "Nothing on the daemon changes",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("the form does not say %q:\n%s", want, txt)
		}
	}
	// The whole path, wrapped but never cut.
	if !strings.Contains(strings.ReplaceAll(txt, " ", ""), "willwrite"+b.m.opts.HomeDir+"/docker-desk-image-nginx-1.27-20261124.tar") {
		t.Errorf("the form does not show the whole path:\n%s", txt)
	}
	if strings.Contains(txt, "…") {
		t.Errorf("something in the form is cut:\n%s", txt)
	}
	// No invented file size: the daemon's listed size is not the tar's.
	size := humanBytes(d.si.size)
	if d.si.size <= 0 || strings.Contains(txt, size) || strings.Contains(txt, "bytes") {
		t.Errorf("the form quotes a size (%s):\n%s", size, txt)
	}
	b.assertFrameGeometry()
}

func TestSaveArchiveFormUntagged(t *testing.T) {
	var id string
	b, _ := saveBench(t, benchOpts{}, &id)
	d := b.saveForm("<untagged> " + engine.ShortID(id))
	if got, want := d.formText(), "~/docker-desk-image-"+engine.ShortID(id)+"-20261124.tar"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	txt := b.dialogText()
	for _, want := range []string{"the image ID " + engine.ShortID(id), "will be untagged", "platform linux/amd64"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the form does not say %q:\n%s", want, txt)
		}
	}
}

// ---- 3.2 the path ---------------------------------------------------------------

func TestSaveArchivePathRefusals(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	os.WriteFile(filepath.Join(home, "taken.tar"), []byte("evidence"), 0o644)
	os.Mkdir(filepath.Join(home, "folder"), 0o755)
	os.Symlink(filepath.Join(home, "nowhere.tar"), filepath.Join(home, "link.tar"))
	cases := []struct{ name, path, want string }{
		{"existing file", "~/taken.tar", "the desk never replaces one"},
		{"directory", "~/folder", "is a folder: name a file inside it"},
		{"symlink", "~/link.tar", "is a symbolic link"},
		{"missing folder", "~/no/such/x.tar", "does not exist: the folder must exist; none is created"},
		{"parent is a file", "~/taken.tar/x.tar", "is not a folder"},
		{"another user's home", "~bob/x.tar", "Only ~/ is expanded"},
		{"empty", "   ", "No path was given"},
	}
	b.saveForm("nginx:1.27")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b.setPath(c.path)
			b.srv.ResetRequests()
			b.keys("enter")
			if b.m.dlg == nil || b.m.dlg.form != formSaveImage {
				t.Fatal("the form closed")
			}
			if !strings.Contains(b.m.dlg.formErr, c.want) {
				t.Fatalf("formErr %q, want %q", b.m.dlg.formErr, c.want)
			}
			if !strings.Contains(b.dialogText(), c.want) {
				t.Fatalf("the form does not show the refusal:\n%s", b.dialogText())
			}
			if n := len(b.srv.Requests()); n != 0 {
				t.Fatalf("sent %v", b.srv.Requests())
			}
		})
	}
	if len(b.m.jobs.List()) != 0 {
		t.Fatal("a job was started")
	}
	if got, _ := os.ReadFile(filepath.Join(home, "taken.tar")); string(got) != "evidence" {
		t.Fatal("the existing file was changed")
	}
	if _, err := os.Lstat(filepath.Join(home, "no")); err == nil {
		t.Fatal("a folder was created")
	}
	if _, err := os.Lstat(filepath.Join(home, "nowhere.tar")); err == nil {
		t.Fatal("the link was written through")
	}
}

// ~ is expanded on submission only, and to the home folder.
func TestSaveArchiveTildeExpansion(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.saveForm("nginx:1.27")
	b.setPath("~/out.tar")
	if !strings.Contains(b.dialogText(), "will write "+filepath.Join(home, "out.tar")) {
		t.Fatalf("form:\n%s", b.dialogText())
	}
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	if _, err := os.Stat(filepath.Join(home, "out.tar")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "~")); err == nil {
		t.Fatal("a folder named ~ was written")
	}
}

// ---- 3.3 the form is bound to its image -----------------------------------------

func TestSaveArchiveHighlightMoves(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	archive := randomBytes(t, 50<<10)
	b.srv.SetImageArchive("nginx:1.27", archive)
	b.saveForm("nginx:1.27")
	dlg := b.m.dlg
	// The highlight moves under the open form.
	b.selectRow("python:3.12-slim")
	b.m.dlg = dlg
	b.setPath(filepath.Join(home, "x.tar"))
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	gets := b.archiveGets()
	q, _ := url.ParseQuery(gets[0].Query)
	if len(gets) != 1 || q.Get("names") != "nginx:1.27" {
		t.Fatalf("saved %v, want nginx:1.27", gets)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "x.tar")); !bytes.Equal(got, archive) {
		t.Fatal("not nginx:1.27's archive")
	}
}

func TestSaveArchiveImageGone(t *testing.T) {
	var id string
	b, home := saveBench(t, benchOpts{}, &id)
	name := "<untagged> " + engine.ShortID(id)
	b.saveForm(name)
	dlg := b.m.dlg
	b.setPath(filepath.Join(home, "x.tar"))
	if _, err := b.eng.RemoveImage(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Before the list catches up: the job's pin says it is gone.
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "no longer exists") {
		t.Fatalf("job %v: %q", j.State, j.Err)
	}
	if len(b.archiveGets()) != 0 || len(dirEntries(t, home)) != 0 {
		t.Fatal("something was read or written")
	}
	// After it has: the form says so and starts nothing.
	b.until("the list without it", func() bool {
		for _, img := range b.m.snap.Images {
			if img.ID == id {
				return false
			}
		}
		return true
	})
	b.m.dlg = dlg
	b.setPath(filepath.Join(home, "y.tar"))
	before := len(b.m.jobs.List())
	b.srv.ResetRequests()
	b.keys("enter")
	if b.m.dlg != nil || len(b.m.jobs.List()) != before || !strings.Contains(b.m.status.text, "no longer exists") {
		t.Fatalf("dialog %v, jobs %d, status %q", b.m.dlg, len(b.m.jobs.List()), b.m.status.text)
	}
	if len(b.srv.Requests()) != 0 {
		t.Fatalf("sent %v", b.srv.Requests())
	}
}

func TestSaveArchiveRefusesASelection(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.m.focus = PaneList
	b.keys(" ")
	b.selectRow("python:3.12-slim")
	b.m.focus = PaneList
	b.keys(" ")
	b.srv.ResetRequests()
	b.keys("w")
	b.settle()
	if b.m.dlg != nil {
		t.Fatalf("a dialog opened with a selection: %v", b.m.dlg.kind)
	}
	if s := b.m.status.text; !strings.Contains(s, "save archive works on one object at a time and 2 are selected") || !strings.Contains(s, "Nothing was sent") {
		t.Fatalf("status %q", s)
	}
	if len(b.srv.Requests()) != 0 || len(dirEntries(t, home)) != 0 {
		t.Fatal("something was sent or written")
	}
	// The menu, with a selection, does not offer it either.
	b.keys("enter")
	if b.m.dlg != nil && b.m.dlg.kind == dlgMenu {
		for _, a := range b.m.dlg.menu {
			if a.key == "w" {
				t.Fatal("the selection's menu offers save archive")
			}
		}
	}
}

func TestSaveArchiveReadOnly(t *testing.T) {
	b, home := saveBench(t, benchOpts{readOnly: true}, nil)
	b.see("[READ-ONLY]")
	archive := randomBytes(t, 70<<10)
	b.srv.SetImageArchive("nginx:1.27", archive)
	b.saveForm("nginx:1.27")
	dest := filepath.Join(home, "ro.tar")
	b.setPath(dest)
	b.srv.ResetRequests()
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s", j.State, j.Err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, archive) {
		t.Fatal("not the daemon's archive")
	}
	for _, r := range b.srv.Requests() {
		if r.Method != http.MethodGet {
			t.Fatalf("read-only save sent %s", r)
		}
	}
	b.noMutations("a read-only save")
	if !strings.Contains(jobLog(j), "as read-only mode allows") {
		t.Fatalf("log:\n%s", jobLog(j))
	}
}

// The menu reaches it from the List and the Details pane; the direct key is
// the List pane's (w wraps text in Details).
func TestSaveArchiveMenuAndDetailsKey(t *testing.T) {
	b, _ := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.detailLoaded()
	b.m.focus = PaneDetails
	b.m.tab[KindImages] = 0
	b.srv.ResetRequests()
	b.keys("w")
	if b.m.dlg != nil {
		t.Fatalf("w in the Details pane opened %v", b.m.dlg.kind)
	}
	if strings.Contains(b.screen(), "w save archive") {
		t.Error("the Details hints offer w save archive")
	}
	b.m.focus = PaneList
	b.m.dirty = true
	b.see("w save archive")
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
		t.Fatal("no menu")
	}
	sel := -1
	for i, a := range b.m.dlg.menu {
		if a.key == "w" {
			sel = i
		}
	}
	if sel < 0 {
		t.Fatal("the menu does not offer save archive")
	}
	b.m.dlg.menuSel = sel
	b.keys("enter")
	b.until("the save form", func() bool { return b.m.dlg != nil && b.m.dlg.form == formSaveImage })
	if b.m.dlg.si.imageRef != "nginx:1.27" {
		t.Fatalf("menu bound the form to %+v", b.m.dlg.si)
	}
	if len(b.archiveGets()) != 0 {
		t.Fatal("the archive was asked for before Enter")
	}
}

// The "reading" status does not outlive the answer.
func TestSaveArchiveReadingStatusClears(t *testing.T) {
	b, _ := saveBench(t, benchOpts{}, nil)
	b.selectRow("nginx:1.27")
	b.detailLoaded()
	b.m.detail.image = nil
	b.step(keyMsg("w"))
	b.until("the save form", func() bool { return b.m.dlg != nil && b.m.dlg.form == formSaveImage })
	if strings.Contains(b.m.status.text, "Reading") {
		t.Fatalf("status %q after the form opened", b.m.status.text)
	}
}

// The help, the hints and the README say the same about w.
func TestSaveArchiveIsDocumented(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(data)), " ")
	b, _ := saveBench(t, benchOpts{}, nil)
	help := strings.Join(strings.Fields(ansi.Strip(strings.Join(b.m.helpLines(200), "\n"))), " ")
	for _, want := range []string{
		"w (List) write this image's archive (docker save) to one local file; the tag is kept",
		"w saves one image (never a selection) to a local tar, also in read-only mode",
		"refuses if the tag has moved",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not say %q", want)
		}
	}
	for _, want := range []string{
		"### Save — an image's archive to a file",
		"`w` save archive… (List pane; a read, allowed in read-only mode",
		"`~/docker-desk-image-<object>-<YYYYMMDD>.tar`",
		"if it names another image now, nothing is written and the job says *the tag moved*",
		"mode `0600`, and takes the path's name only when complete",
		"`c` in the Jobs pane closes the daemon's stream and removes the partial file",
		"save an image's archive (`w` on Images)",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
}

// A daemon's message is drawn escaped: a hostile remote daemon cannot reach
// the terminal through a failed save.
func TestSaveArchiveDaemonMessageIsEscaped(t *testing.T) {
	b, home := saveBench(t, benchOpts{}, nil)
	b.saveForm("nginx:1.27")
	b.setPath(filepath.Join(home, "nginx.tar"))
	b.srv.FailNext(http.MethodGet, `/get$`, http.StatusInternalServerError, "boom \x1b]0;owned\x07\x1b[2J", 1)
	j := b.waitJob(b.submitSave().ID)
	if j.State != jobs.Failed {
		t.Fatalf("job %v", j.State)
	}
	if strings.ContainsRune(j.Err, 0x1b) || !strings.Contains(j.Err, `\x1b]0;owned\x07`) {
		t.Fatalf("the error is not escaped: %q", j.Err)
	}
	b.m.dlg = &dialog{kind: dlgJob, jobID: j.ID}
	if raw := b.m.View(); strings.Contains(raw, "\x1b]0;owned") || strings.Contains(raw, "\x1b[2J") {
		t.Fatal("the daemon's escape sequence reached the screen")
	}
}
