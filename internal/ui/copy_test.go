package ui

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// ---- helpers ----------------------------------------------------------------------

func (b *bench) archiveReads() int {
	return b.srv.CountRequests(http.MethodGet, `^/containers/[^/]+/archive$`)
}

func (b *bench) uploads() int {
	return b.srv.CountRequests(http.MethodPut, `^/containers/[^/]+/archive$`)
}

// flat is the screen as one line of words, without the boxes' borders:
// what a sentence wrapped across lines of a dialog reads as.
func (b *bench) flat() string {
	s := strings.Map(func(r rune) rune {
		if r >= 0x2500 && r <= 0x257f {
			return ' '
		}
		return r
	}, b.screen())
	return strings.Join(strings.Fields(s), " ")
}

// dialogText is the open dialog alone, flattened as flat does.
func (b *bench) dialogText() string {
	lines, _, _ := b.m.renderDialog()
	s := strings.Map(func(r rune) rune {
		if r >= 0x2500 && r <= 0x257f {
			return ' '
		}
		return r
	}, ansi.Strip(strings.Join(lines, "\n")))
	return strings.Join(strings.Fields(s), " ")
}

// copyForm presses o or i on a path of shop-web-1's Files view.
func (b *bench) copyForm(key, at string) *dialog {
	b.t.Helper()
	if !b.m.fs.open {
		b.openFilesOn(KindContainers, "shop-web-1")
		b.until("the change list", func() bool { return b.m.fs.chgRead })
	}
	b.filesGo(at)
	b.keys(key)
	if b.m.dlg == nil || b.m.dlg.kind != dlgForm {
		b.t.Fatalf("%s on %s opened no form:\n%s", key, at, b.screen())
	}
	return b.m.dlg
}

func (b *bench) lastJob() jobs.Job {
	b.t.Helper()
	js := b.m.jobs.List()
	if len(js) == 0 {
		b.t.Fatal("no job")
	}
	j := js[0]
	for _, x := range js {
		if x.ID > j.ID {
			j = x
		}
	}
	return j
}

func (b *bench) waitJob(id int) jobs.Job {
	b.t.Helper()
	var j jobs.Job
	b.until("job to finish", func() bool {
		j, _ = b.m.jobs.Get(id)
		return j.State != jobs.Running
	})
	b.settle()
	return j
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, _ := d.Info()
		line := rel + " " + fi.Mode().String()
		if fi.Mode()&fs.ModeSymlink != 0 {
			to, _ := os.Readlink(p)
			line += " -> " + to
		}
		out = append(out, line)
		return nil
	})
	sort.Strings(out)
	return out
}

// ---- 4.1 the keys ------------------------------------------------------------------

func TestFilesCopyKeys(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.srv.ResetRequests()
	d := b.copyForm("o", "/etc/nginx/nginx.conf")
	if d.form != formCopyOut || d.cp.path != "/etc/nginx/nginx.conf" || d.cp.containerName != "shop-web-1" {
		t.Fatalf("copy-out form %+v", d.cp)
	}
	b.see("Copy out of the container", "/etc/nginx/nginx.conf", "Local destination")
	b.keys("esc")
	d = b.copyForm("i", "/etc/nginx")
	if d.form != formCopyIn || d.fields[copyInDest].value() != "/etc/nginx" || d.focus != copyInSource {
		t.Fatalf("copy-in form: dest %q focus %d", d.fields[copyInDest].value(), d.focus)
	}
	b.see("Copy into the container", "Local source", "Container destination")
	b.keys("esc")
	if n := b.archiveReads() + b.uploads(); n != 0 {
		t.Fatalf("opening the forms sent %d archive requests", n)
	}
	b.noMutations("opening the copy forms")

	// A path the container deleted has nothing to copy out. Deleted paths
	// are drawn once the image's tree is read.
	b.readArchive()
	b.filesGo("/tmp/seed")
	b.keys("o")
	if b.m.dlg != nil {
		t.Fatal("o opened a form on a deleted path")
	}
	b.see("was deleted by the container: there is nothing to copy out")

	// An image: one sentence, no form, for either key.
	b.keys("esc")
	b.openFilesOn(KindImages, "nginx:1.27")
	for _, k := range []string{"o", "i"} {
		b.keys(k)
		if b.m.dlg != nil {
			t.Fatalf("%s opened a form on an image", k)
		}
		b.see("Copies are between a container and this machine")
	}
}

func TestFilesCopyInReadOnly(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.openFilesOn(KindContainers, "shop-web-1")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	b.filesGo("/etc/nginx")
	b.keys("i")
	if b.m.dlg != nil {
		t.Fatal("i opened a form in read-only mode")
	}
	b.see("Read-only mode: copying into a container is disabled. Nothing was sent")
	if strings.Contains(b.screenLines()[len(b.screenLines())-1], "i copy in") {
		t.Fatal("the read-only footer offers copy in")
	}
	// Copy out only reads: it is offered, and works.
	home := b.exportHome()
	b.copyForm("o", "/etc/nginx/nginx.conf")
	b.typeText("~/nginx.conf")
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("read-only copy out: %v %s", j.State, j.Err)
	}
	if body, _ := os.ReadFile(filepath.Join(home, "nginx.conf")); !strings.Contains(string(body), "worker_processes 4;") {
		t.Fatalf("copied %q", body)
	}
	if !strings.Contains(strings.Join(j.Log, "\n"), "nothing was changed on the daemon") {
		t.Fatalf("log:\n%s", strings.Join(j.Log, "\n"))
	}
	if b.archiveReads() != 1 {
		t.Fatalf("%d archive reads", b.archiveReads())
	}
	b.noMutations("a read-only copy out")
}

// ---- 4.2 the forms ------------------------------------------------------------------

func TestFilesCopyForms(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	b.copyForm("i", "/etc/nginx")
	text := b.dialogText()
	for _, want := range []string{
		"container shop-web-1", "daemon " + b.srv.Host(), "> /etc/nginx",
		"The path is the destination itself — the copy is written there, not inside it — and its parent must already exist.",
		"Files already at the destination with the same names are replaced",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the copy-in form lacks %q:\n%s", want, text)
		}
	}
	b.keys("esc")
	b.copyForm("o", "/var/lib/nginx/state.db")
	text = b.dialogText()
	for _, want := range []string{"/var/lib/nginx/state.db", "The path is the destination itself", "never replaced", "~/state.db"} {
		if !strings.Contains(text, want) {
			t.Errorf("the copy-out form lacks %q:\n%s", want, text)
		}
	}
}

// ---- 4.3 the local path -----------------------------------------------------------

func TestFilesCopyOutRefusesLocalPaths(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	home := b.exportHome()
	if err := os.WriteFile(filepath.Join(home, "taken"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(home, "dir"), 0o755)
	b.copyForm("o", "/etc/nginx")
	for _, c := range []struct{ typed, want string }{
		{"~/taken", filepath.Join(home, "taken") + " already exists: a copy never replaces a path"},
		{"~/dir", filepath.Join(home, "dir") + " already exists"},
		{"~/none/nginx", filepath.Join(home, "none") + " does not exist: the parent must exist"},
		{"~/taken/nginx", filepath.Join(home, "taken") + " is not a folder"},
		{"~someone/x", "Only ~/ is expanded"},
	} {
		b.m.dlg.fields[0].rows[0].SetValue(c.typed)
		b.keys("enter")
		if b.m.dlg == nil || b.m.dlg.form != formCopyOut || !strings.Contains(b.m.dlg.formErr, c.want) {
			t.Fatalf("%s: dialog %v, error %q", c.typed, b.m.dlg != nil, b.m.dlg.formErr)
		}
	}
	if body, _ := os.ReadFile(filepath.Join(home, "taken")); string(body) != "keep" {
		t.Fatal("the existing file was touched")
	}
	if n := b.archiveReads(); n != 0 {
		t.Fatalf("a refused copy read the archive %d times", n)
	}
	if len(b.m.jobs.List()) != 0 {
		t.Fatal("a refused copy started a job")
	}
	// The absolute path is shown before Enter: ~ and a relative path.
	b.m.dlg.fields[0].rows[0].SetValue("~/x")
	b.see("will write " + filepath.Join(home, "x"))
	b.m.dlg.fields[0].rows[0].SetValue("sub/nginx.conf")
	b.see("will write " + filepath.Join(home, "sub", "nginx.conf")) // WorkDir is home in the bench
}

// ---- 4.4 the copy-out job ---------------------------------------------------------

func TestFilesCopyOutJob(t *testing.T) {
	b := newBench(t, benchOpts{})
	home := b.exportHome()
	b.copyForm("o", "/var/lib/nginx/state.db")
	b.typeText("~/copy.db")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatal("the form stayed open")
	}
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded || j.Title != "copy out of container" || j.Target != "shop-web-1:/var/lib/nginx/state.db" {
		t.Fatalf("job %+v", j)
	}
	fi, err := os.Lstat(filepath.Join(home, "copy.db"))
	if err != nil || fi.Size() != 4096 || fi.Mode() != 0o640 {
		t.Fatalf("copied file: %v %v", fi, err)
	}
	// A directory, with its link.
	b.copyForm("o", "/etc/nginx")
	b.typeText("~/nginx-copy")
	b.keys("enter")
	if j := b.waitJob(b.lastJob().ID); j.State != jobs.Succeeded {
		t.Fatalf("directory: %s", j.Err)
	}
	want := []string{". drwxr-xr-x", "conf.d drwxr-xr-x", "conf.d/default.conf -rw-r--r--", "current.conf Lrwxrwxrwx -> nginx.conf", "nginx.conf -rw-r--r--"}
	if got := listTree(t, filepath.Join(home, "nginx-copy")); !reflect.DeepEqual(got, want) {
		t.Fatalf("copied tree %q", got)
	}
	// Skipped entries are named in the job.
	b.copyForm("o", "/run")
	b.typeText("~/run")
	b.keys("enter")
	j = b.waitJob(b.lastJob().ID)
	if log := strings.Join(j.Log, "\n"); j.State != jobs.Succeeded || !strings.Contains(log, "skipped run/nginx.fifo: not a regular file, directory or symlink") {
		t.Fatalf("%v\n%s", j.State, log)
	}
	// The daemon refuses: nothing is left.
	b.srv.FailNext("GET", `/containers/.+/archive$`, 404, "Could not find the file /etc/nginx in container shop-web-1", 1)
	b.copyForm("o", "/etc/nginx")
	b.typeText("~/gone")
	b.keys("enter")
	j = b.waitJob(b.lastJob().ID)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "Could not find the file") {
		t.Fatalf("refused: %v %q", j.State, j.Err)
	}
	if _, err := os.Lstat(filepath.Join(home, "gone")); err == nil {
		t.Fatal("a refused copy left a path")
	}
}

func TestFilesCopyOutCancelLeavesNothing(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/var/lib/nginx/big.db", Mode: 0o600, Content: make([]byte, 512<<10)})
	}})
	b.srv.ArchiveChunkDelay = 5 * time.Second
	home := b.exportHome()
	dest := filepath.Join(home, "nginx-lib")
	b.copyForm("o", "/var/lib/nginx")
	b.typeText("~/nginx-lib")
	b.keys("enter")
	id := b.lastJob().ID
	b.until("the first chunk to be written", func() bool { _, err := os.Lstat(filepath.Join(dest, "big.db")); return err == nil })
	b.m.jobs.Cancel(id)
	j := b.waitJob(id)
	if j.State != jobs.Cancelled {
		t.Fatalf("job %v %q", j.State, j.Err)
	}
	if _, err := os.Lstat(dest); err == nil {
		t.Fatalf("the cancelled copy left %q", listTree(t, dest))
	}
	if log := strings.Join(j.Log, "\n"); !strings.Contains(log, "removed what was written: "+dest) {
		t.Fatalf("log:\n%s", log)
	}
}

// ---- 4.5 copy in -------------------------------------------------------------------

func copySource(t *testing.T, home string) string {
	t.Helper()
	src := filepath.Join(home, "assets")
	_ = os.MkdirAll(filepath.Join(src, "js"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "app.js"), []byte("console.log(1)\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "js", "lib.js"), []byte("export {}\n"), 0o644)
	return src
}

func (b *bench) copyInConfirmation(source, dest string) *dialog {
	b.t.Helper()
	d := b.copyForm("i", "/etc/nginx")
	d.fields[copyInSource].rows[0].SetValue(source)
	d.fields[copyInDest].rows[0].SetValue(dest)
	b.keys("enter")
	b.until("the confirmation", func() bool { return b.m.dlg != nil && (b.m.dlg.kind == dlgPlan || b.m.dlg.formErr != "") })
	return b.m.dlg
}

func TestFilesCopyInConfirms(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	home := b.exportHome()
	src := copySource(t, home)

	// A source that does not exist: the form stays, with the reason.
	d := b.copyInConfirmation("~/nope", "/srv/assets")
	if d.kind != dlgForm || !strings.Contains(d.formErr, filepath.Join(home, "nope")+" does not exist") {
		t.Fatalf("missing source: %+v", d)
	}
	b.keys("esc")

	d = b.copyInConfirmation("~/assets", "/srv/assets")
	if d.kind != dlgPlan {
		t.Fatalf("no confirmation: %s", d.formErr)
	}
	text := b.dialogText()
	for _, want := range []string{"container shop-web-1", "daemon " + b.srv.Host(), src, "/srv/assets", "2 files, 2 directories (4 entries)", "25 B", "are replaced", "y proceed"} {
		if !strings.Contains(text, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, text)
		}
	}
	// Enter and n are not consent.
	b.keys("enter")
	if b.m.dlg == nil {
		t.Fatal("Enter closed the confirmation")
	}
	b.keys("n")
	if b.m.dlg != nil || len(b.m.jobs.List()) != 0 || b.uploads() != 0 {
		t.Fatalf("n: dialog %v, %d jobs, %d uploads", b.m.dlg != nil, len(b.m.jobs.List()), b.uploads())
	}
	b.copyInConfirmation("~/assets", "/srv/assets")
	b.keys("esc")
	if len(b.m.jobs.List()) != 0 || b.uploads() != 0 {
		t.Fatal("Esc started the copy")
	}
	b.noMutations("declined copy-in confirmations")

	// y: the copy, as one job.
	b.copyInConfirmation("~/assets", "/srv/assets")
	b.keys("y")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded || j.Title != "copy into container" || j.Target != "shop-web-1:/srv/assets" {
		t.Fatalf("job %+v", j)
	}
	if f, ok := b.srv.ContainerFile("shop-web-1", "/srv/assets/js/lib.js"); !ok || string(f.Content) != "export {}\n" {
		t.Fatalf("the container has %+v", f)
	}
}

// ---- 4.6 the view afterwards ------------------------------------------------------

func TestFilesCopyInReadsTheChangesAgain(t *testing.T) {
	b := newBench(t, benchOpts{})
	home := b.exportHome()
	copySource(t, home)
	b.copyInConfirmation("~/assets", "/srv/assets")
	before, reads := b.changeRequests(), b.archiveReads()
	b.keys("y")
	b.waitJob(b.lastJob().ID)
	b.until("the change list read again", func() bool { return b.changeRequests() == before+1 && !b.m.fs.chgBusy })
	b.settle()
	if n := b.changeRequests(); n != before+1 {
		t.Fatalf("the change list was read %d times", n-before)
	}
	if b.archiveReads() != reads {
		t.Fatal("the view read the container's archive")
	}
	b.see("Copied into shop-web-1; its change list was read again")
	// The view's rules are unchanged (V22): a directory the container
	// added is one row, marked added, with what is beneath it counted.
	b.filesGo("/srv/assets")
	v, _ := b.m.filesView()
	r := v.Rows[v.Find("/srv/assets")]
	if r.Node.Change != files.Added || !r.Node.FromChanges || r.Node.Beneath != 3 || r.Node.Expandable() {
		t.Fatalf("row %+v", r.Node)
	}
	// A file into a directory the image has: its own row, added.
	_ = os.WriteFile(filepath.Join(home, "app.js"), []byte("1"), 0o644)
	b.copyInConfirmation("~/app.js", "/etc/nginx/app.js")
	before = b.changeRequests()
	b.keys("y")
	b.waitJob(b.lastJob().ID)
	b.until("the change list read again", func() bool { return b.changeRequests() == before+1 && !b.m.fs.chgBusy })
	b.settle()
	b.filesGo("/etc/nginx/app.js")
	v, _ = b.m.filesView()
	if r := v.Rows[v.Find("/etc/nginx/app.js")]; r.Node.Change != files.Added {
		t.Fatalf("row %+v", r.Node)
	}

	// Closed before the job ends: nothing is read.
	b.srv.Stall("PUT", `/containers/.+/archive$`, 300*time.Millisecond)
	_ = os.WriteFile(filepath.Join(home, "one.txt"), []byte("1"), 0o644)
	b.copyInConfirmation("~/one.txt", "/srv/one.txt")
	b.keys("y")
	id := b.lastJob().ID
	b.keys("esc", "esc") // the filter is empty: Esc closes the view
	if b.m.fs.open || b.m.fs.copyWatch != nil {
		t.Fatalf("view open %v, watch %+v", b.m.fs.open, b.m.fs.copyWatch)
	}
	before = b.changeRequests()
	if j := b.waitJob(id); j.State != jobs.Succeeded {
		t.Fatalf("%v %s", j.State, j.Err)
	}
	b.run(200 * time.Millisecond)
	if n := b.changeRequests(); n != before {
		t.Fatalf("a closed view read the change list %d times", n-before)
	}
}

// ---- 5.2 the documentation ----------------------------------------------------------

// The README names o and i with the help's own words for them.
func TestCopyKeysAreDocumented(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(data)), " ")
	b := newBench(t, benchOpts{})
	help := ansi.Strip(strings.Join(b.m.helpLines(200), "\n"))
	for key, words := range map[string]string{
		"o": "copy the highlighted path out to a path on this machine",
		"i": "copy a local path in to the highlighted path",
	} {
		if !strings.Contains(help, words) {
			t.Errorf("the help does not say %q", words)
		}
		if !strings.Contains(readme, "**`"+key+"` "+words+".**") {
			t.Errorf("README.md does not document %s as %q", key, words)
		}
	}
	for _, want := range []string{
		"The destination is the path named.", "deliberate difference from `docker cp`",
		"A local path that already exists — file, folder or link — is refused",
		"devices, FIFOs and sockets are skipped and named in the job's output",
		"what you copy in gets the daemon's default owner",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
}
