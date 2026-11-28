package ui

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// ---- helpers ----------------------------------------------------------------------

// webFiles opens the Files view on shop-web-1 and waits for its changes;
// with tree, the image's archive is read too (a, from any row).
func (b *bench) webFiles(tree bool) {
	b.t.Helper()
	b.openFilesOn(KindContainers, "shop-web-1")
	b.until("the change list", func() bool { return b.m.fs.chgRead })
	if tree {
		b.keys("a")
		b.until("the archive to be read", func() bool { return b.m.filesBase() != nil && b.m.fs.run == nil })
		b.settle()
	}
}

// openFile is Enter on a path, and the read's end.
func (b *bench) openFile(p string) *filesReader {
	b.t.Helper()
	if b.m.fs.cursor != p {
		b.filesGo(p)
	}
	b.keys("enter")
	if b.m.fs.rd == nil {
		b.t.Fatalf("Enter on %s opened no reader:\n%s", p, b.screen())
	}
	b.until("the file to be read", func() bool { return b.m.fs.rd.run == nil })
	b.settle()
	return b.m.fs.rd
}

// fileGets are the container archive reads, with the path each asked for.
func (b *bench) fileGets() []string {
	b.t.Helper()
	var out []string
	for _, r := range b.srv.Requests() {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/archive") {
			q, err := url.ParseQuery(r.Query)
			if err != nil {
				b.t.Fatal(err)
			}
			out = append(out, q.Get("path"))
		}
	}
	return out
}

// containerRequests are the requests about any one container, other than
// the lists and inspects the desk makes on its own.
func (b *bench) containerRequests() []string {
	var out []string
	for _, r := range b.srv.Requests() {
		if strings.HasSuffix(r.Path, "/archive") || strings.HasSuffix(r.Path, "/changes") || strings.HasSuffix(r.Path, "/export") || r.Mutating() {
			out = append(out, r.String())
		}
	}
	return out
}

// rawSafe fails if the frame, as it is written to the terminal, carries
// any part of an escape sequence a file or a name chose.
func (b *bench) rawSafe(planted ...string) {
	b.t.Helper()
	b.m.dirty = true
	frame := b.m.View()
	for _, p := range planted {
		if strings.Contains(frame, p) {
			b.t.Fatalf("%q reached the frame raw", p)
		}
	}
	for _, r := range frame {
		// The frame's own controls are newlines and its SGR sequences.
		if r == '\n' || r == 0x1b {
			continue
		}
		if unicode.IsControl(r) || r == 0x202e || r == 0x2066 || r == 0xfffd {
			b.t.Fatalf("a raw %U reached the frame", r)
		}
	}
}

type filesSnapshot struct {
	cursor, filter string
	order          files.Order
	expVer         int
	expanded       int
	typing, open   bool
	off            int
}

func (b *bench) filesSnap() filesSnapshot {
	b.screen() // the list's offset is settled when it is drawn
	fs := &b.m.fs
	return filesSnapshot{fs.cursor, fs.filter, fs.order, fs.expVer, len(fs.expanded), fs.typing, fs.open, fs.off}
}

// ---- 2.1 Enter's routing --------------------------------------------------------------

func TestReaderNothingIsSentBeforeEnter(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(true)
	b.srv.ResetRequests()
	b.filesGo("/etc/nginx/nginx.conf")
	b.keys("j", "k", "s", "s", "right", "left")
	b.filesGo("/usr/share/nginx/html/index.html")
	b.settle()
	if b.m.fs.rd != nil || len(b.fileGets()) != 0 || len(b.containerRequests()) != 0 {
		t.Fatalf("moving in the tree sent %v", b.srv.Requests())
	}
	foot := b.screenLines()[len(b.screenLines())-1]
	if !strings.Contains(foot, "⏎ read the file") {
		t.Fatalf("the footer does not offer the read: %q", foot)
	}
	rd := b.openFile("/usr/share/nginx/html/index.html")
	if got := b.fileGets(); len(got) != 1 || got[0] != "/usr/share/nginx/html/index.html" {
		t.Fatalf("archive requests: %q", got)
	}
	if !rd.loaded || rd.pv.Binary {
		t.Fatalf("not loaded: %+v", rd)
	}
	b.see("<h1>shop</h1>", "the sandbox's front page", "path       /usr/share/nginx/html/index.html", "container  shop-web-1 ·")
	if b.exports() != 0 {
		t.Fatal("the image's archive was read for a file's contents")
	}
	b.noMutations("the reader")
}

func TestReaderOpensAnAddedFileBeforeTheTreeIsBuilt(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(false)
	b.srv.ResetRequests()
	b.filesGo("/run/nginx.pid")
	foot := b.screenLines()[len(b.screenLines())-1]
	if !strings.Contains(foot, "⏎ read the file") || !strings.Contains(foot, "a read the archive") {
		t.Fatalf("footer: %q", foot)
	}
	rd := b.openFile("/run/nginx.pid")
	if !rd.loaded || len(rd.pv.Text) != 1 || rd.pv.Text[0] != "1" {
		t.Fatalf("the added file: %+v", rd.pv)
	}
	if b.exports() != 0 || b.m.fs.run != nil || b.m.filesBase() != nil {
		t.Fatal("Enter on a file read the image's archive")
	}
	if got := b.fileGets(); len(got) != 1 || got[0] != "/run/nginx.pid" {
		t.Fatalf("archive requests: %q", got)
	}
	b.see("2B in the container now", "the whole file: 1 line, 2B")
	b.keys("esc")

	// A changed path the tree does not hold yet is read the same way.
	rd = b.openFile("/etc/nginx/nginx.conf")
	if !rd.loaded || rd.pv.Lines != 4 {
		t.Fatalf("the changed file: %+v", rd.pv)
	}
	b.keys("esc")

	// Enter on a directory is still the image's archive, and so is a.
	b.filesGo("/etc/nginx")
	b.keys("enter")
	if b.m.fs.run == nil {
		t.Fatal("Enter on a directory did not start the archive read")
	}
	b.until("the archive", func() bool { return b.m.fs.run == nil })
	if b.m.filesBase() == nil || b.exports() != 1 {
		t.Fatalf("the archive: base %v, %d requests", b.m.filesBase() != nil, b.exports())
	}
}

func TestReaderRefusesWhatIsNotARegularContainerFile(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(true)
	b.srv.ResetRequests()
	for _, c := range []struct{ path, want string }{
		{"/bin/sh", "/bin/sh is a symbolic link to /usr/bin/bash: shown, never followed or opened. Nothing was sent."},
		{"/usr/bin/gunzip", "/usr/bin/gunzip is a hard link to /usr/bin/gzip: links are not followed or opened. Nothing was sent."},
		{"/dev/null", "/dev/null is a character device: only regular files are opened. Nothing was sent."},
		{"/dev/initctl", "/dev/initctl is a named pipe (FIFO): only regular files are opened. Nothing was sent."},
		{"/tmp/seed", "/tmp/seed was deleted by the container: there is nothing to read. Nothing was sent."},
	} {
		b.filesGo(c.path)
		b.keys("enter")
		if b.m.fs.rd != nil {
			t.Fatalf("%s: a reader opened", c.path)
		}
		if b.m.status.text != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.path, b.m.status.text, c.want)
		}
	}
	// Types the fixture's tree does not hold, and a path beneath a
	// directory the container deleted.
	for _, c := range []struct {
		row  files.Row
		want string
	}{
		{files.Row{Node: &files.Node{Path: "/dev/sda", Type: files.TypeBlock}}, "/dev/sda is a block device"},
		{files.Row{Node: &files.Node{Path: "/run/x.sock", Type: files.TypeOther}}, "/run/x.sock is an entry of another type (a socket, or one the archive does not name)"},
		{files.Row{Node: &files.Node{Path: "/gone/f", Type: files.TypeFile}, Gone: true}, "/gone/f was deleted by the container"},
		{files.Row{Node: &files.Node{Path: "/gone/u", Type: files.TypeUnknown, Change: files.Deleted}}, "/gone/u was deleted by the container"},
	} {
		if !b.m.filesEnter(&c.row) || b.m.fs.rd != nil || !strings.HasPrefix(b.m.status.text, c.want) || !strings.HasSuffix(b.m.status.text, "Nothing was sent.") {
			t.Errorf("%s: reader %v, status %q", c.row.Node.Path, b.m.fs.rd != nil, b.m.status.text)
		}
		if b.m.filesReadable(&c.row) {
			t.Errorf("%s is offered as readable", c.row.Node.Path)
		}
	}
	// A directory is opened as before: no reader, no request.
	b.filesGo("/etc/nginx")
	delete(b.m.fs.expanded, "/etc/nginx")
	b.m.fs.expVer++
	b.keys("enter")
	if b.m.fs.rd != nil || !b.m.fs.expanded["/etc/nginx"] {
		t.Fatal("Enter on a directory did not open it")
	}
	b.settle()
	if got := b.containerRequests(); len(got) != 0 || len(b.m.jobs.List()) != 1 { // the one job is the archive read
		t.Fatalf("refusals sent %v; jobs %d", got, len(b.m.jobs.List()))
	}
}

func TestReaderDoesNotReadImageFiles(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.openFilesOn(KindImages, "nginx:1.27")
	b.readArchive()
	b.srv.ResetRequests()
	jobsBefore := len(b.m.jobs.List())
	b.filesGo("/etc/nginx/nginx.conf")
	foot := b.screenLines()[len(b.screenLines())-1]
	if strings.Contains(foot, "read the file") {
		t.Fatalf("an image's file is offered: %q", foot)
	}
	b.keys("enter")
	b.settle()
	if b.m.fs.rd != nil {
		t.Fatal("a reader opened on an image's file")
	}
	b.see("Image files are not read — nothing was sent. Open Files on a container made from it, or save the archive (w in Images).")
	if b.exports() != 0 || len(b.fileGets()) != 0 || len(b.containerRequests()) != 0 || len(b.m.jobs.List()) != jobsBefore {
		t.Fatalf("an image's file sent %v", b.srv.Requests())
	}
	// A link in an image is named, as on a container.
	b.filesGo("/bin/sh")
	b.keys("enter")
	b.see("/bin/sh is a symbolic link to /usr/bin/bash")
	b.settle()
	if b.exports() != 0 || len(b.fileGets()) != 0 || len(b.m.jobs.List()) != jobsBefore {
		t.Fatalf("requests: %v", b.srv.Requests())
	}
}

// ---- 2.2 the read job -----------------------------------------------------------------

// stubEngine answers CopyFromContainer alone.
type stubEngine struct {
	engine.Engine
	calls   []string
	archive func(ctx context.Context) (engine.Archive, error)
}

func (s *stubEngine) CopyFromContainer(ctx context.Context, id, p string) (engine.Archive, error) {
	s.calls = append(s.calls, id+":"+p)
	return s.archive(ctx)
}

// closeTracker says whether the stream was closed.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return nil }

func tarOf(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		body := []byte(nil)
		if h.Typeflag == tar.TypeReg {
			body = []byte(strings.Repeat("s3cret-line\n", int(h.Size)/12))
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReaderAcceptsOnlyTheOneRegularFileAskedFor(t *testing.T) {
	reg := func(name string) *tar.Header {
		return &tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 24, Mode: 0o640, Uid: 101, Gid: 102}
	}
	okStat := engine.PathStat{Name: "app.conf", Mode: 0o640, Size: 24}
	for _, c := range []struct {
		name string
		stat engine.PathStat
		tar  []byte
		want string // "" accepts
	}{
		{"the file", okStat, tarOf(t, reg("app.conf")), ""},
		{"a stat for another name", engine.PathStat{Name: "passwd", Mode: 0o644}, tarOf(t, reg("passwd")), "the daemon answered for passwd, not app.conf"},
		{"a directory by the stat", engine.PathStat{Name: "app.conf", Mode: iofs.ModeDir | 0o755}, tarOf(t, reg("app.conf")), "answered with a directory"},
		{"a symlink by the stat", engine.PathStat{Name: "app.conf", Mode: iofs.ModeSymlink | 0o777, LinkTarget: "/etc/shadow"}, tarOf(t, reg("app.conf")), "a symbolic link to /etc/shadow: it is not followed"},
		{"a FIFO by the stat", engine.PathStat{Name: "app.conf", Mode: iofs.ModeNamedPipe | 0o600}, tarOf(t, reg("app.conf")), "a named pipe (FIFO), not a regular file"},
		{"a socket by the stat", engine.PathStat{Name: "app.conf", Mode: iofs.ModeSocket | 0o600}, tarOf(t, reg("app.conf")), "a socket, not a regular file"},
		{"a device by the stat", engine.PathStat{Name: "app.conf", Mode: iofs.ModeDevice | 0o600}, tarOf(t, reg("app.conf")), "a device, not a regular file"},
		// The stat says a file; the archive says otherwise.
		{"a directory in the archive", okStat, tarOf(t, &tar.Header{Name: "app.conf/", Typeflag: tar.TypeDir, Mode: 0o755}, reg("app.conf/inner")), "holds a directory named app.conf/, not a regular file"},
		{"a symlink in the archive", okStat, tarOf(t, &tar.Header{Name: "app.conf", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"}), "holds a symbolic link to /etc/shadow named app.conf"},
		{"a hard link in the archive", okStat, tarOf(t, &tar.Header{Name: "app.conf", Typeflag: tar.TypeLink, Linkname: "other"}), "holds a hard link to other named app.conf"},
		{"a FIFO in the archive", okStat, tarOf(t, &tar.Header{Name: "app.conf", Typeflag: tar.TypeFifo}), "holds a named pipe (FIFO) named app.conf"},
		{"a device in the archive", okStat, tarOf(t, &tar.Header{Name: "app.conf", Typeflag: tar.TypeChar}), "holds a character device named app.conf"},
		{"another name in the archive", okStat, tarOf(t, reg("shadow")), "holds shadow, not app.conf"},
		{"a path in the archive", okStat, tarOf(t, reg("../app.conf")), `holds ../app.conf, not app.conf`},
		{"a hostile name in the archive", okStat, tarOf(t, reg("\x1b[31mapp.conf")), `holds \x1b[31mapp.conf, not app.conf`},
		{"two entries", okStat, tarOf(t, reg("app.conf"), reg("shadow")), "holds more than the one file asked for (shadow as well)"},
		{"an empty archive", okStat, tarOf(t), "the daemon's archive is empty"},
		{"not an archive", okStat, []byte(strings.Repeat("not a tar at all ", 64)), "could not be read"},
		{"a cut archive", okStat, tarOf(t, reg("app.conf"))[:520], "the read stopped"},
		{"no trailer", okStat, tarOf(t, reg("app.conf"))[:1024+7], "did not end as an archive does"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := &closeTracker{Reader: bytes.NewReader(c.tar)}
			eng := &stubEngine{archive: func(context.Context) (engine.Archive, error) {
				return engine.Archive{Content: body, Stat: c.stat}, nil
			}}
			pv, meta, err := readContainerFile(context.Background(), eng, "cid", "/etc/app.conf", nil)
			if !body.closed {
				t.Error("the stream was not closed")
			}
			if len(eng.calls) != 1 || eng.calls[0] != "cid:/etc/app.conf" {
				t.Errorf("requests: %q", eng.calls)
			}
			if c.want == "" {
				if err != nil || pv.Lines != 2 || meta.size != 24 || meta.mode != 0o640 || meta.uid != 101 || meta.gid != 102 {
					t.Fatalf("err %v, preview %+v, meta %+v", err, pv, meta)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if pv.Text != nil || pv.Read != 0 || meta != (readerMeta{}) {
				t.Errorf("a refused answer kept %+v %+v", pv, meta)
			}
			if strings.Contains(err.Error(), "s3cret") || strings.ContainsRune(err.Error(), 0x1b) {
				t.Errorf("the error carries contents or a raw escape: %q", err)
			}
		})
	}
}

func TestReaderReportsADaemonFailureAndACancellation(t *testing.T) {
	eng := &stubEngine{archive: func(context.Context) (engine.Archive, error) {
		return engine.Archive{}, &engine.Error{Kind: engine.KindNotFound, Op: "copy from container", Msg: "Could not find the file /etc/app.conf in container cid"}
	}}
	_, _, err := readContainerFile(context.Background(), eng, "cid", "/etc/app.conf", nil)
	if err == nil || !strings.Contains(err.Error(), "is not there any more — the daemon says: Could not find the file /etc/app.conf") {
		t.Fatalf("a missing path: %v", err)
	}
	eng.archive = func(context.Context) (engine.Archive, error) {
		return engine.Archive{}, &engine.Error{Kind: engine.KindUnavailable, Op: "copy from container", Msg: "storage driver \x1b[2Jbusy"}
	}
	_, _, err = readContainerFile(context.Background(), eng, "cid", "/etc/app.conf", nil)
	if err == nil || !strings.Contains(err.Error(), `the daemon refused: storage driver \x1b[2Jbusy`) {
		t.Fatalf("a refusal: %v", err)
	}

	// Cancelled while the stream is being read: the context's error, the
	// stream closed, nothing kept.
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	body := &closeTracker{Reader: pr}
	eng.archive = func(context.Context) (engine.Archive, error) {
		return engine.Archive{Content: body, Stat: engine.PathStat{Name: "app.conf", Mode: 0o644}}, nil
	}
	whole := tarOf(t, &tar.Header{Name: "app.conf", Typeflag: tar.TypeReg, Size: 1200, Mode: 0o644})
	go func() {
		_, _ = pw.Write(whole[:700]) // the header and part of the file
		cancel()
		_ = pw.CloseWithError(errors.New("connection closed"))
	}()
	pv, _, err := readContainerFile(ctx, eng, "cid", "/etc/app.conf", nil)
	if !errors.Is(err, context.Canceled) || pv.Text != nil || !body.closed {
		t.Fatalf("cancelled: err %v, kept %v, closed %v", err, pv.Text != nil, body.closed)
	}
}

func TestReaderJobLogsNoContent(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(true)
	b.srv.ResetRequests()
	rd := b.openFile("/etc/nginx/nginx.conf")
	if !rd.loaded {
		t.Fatalf("not loaded: %q", rd.err)
	}
	j := b.lastJob()
	if j.State != jobs.Succeeded || j.Title != "read a container file" || j.Target != "shop-web-1:/etc/nginx/nginx.conf" {
		t.Fatalf("job: %+v", j)
	}
	j, _ = b.m.jobs.Get(j.ID)
	all := strings.Join(j.Log, "\n") + "\n" + j.Status + "\n" + j.Err
	for _, word := range []string{"worker_processes", "user nginx", "include", "events"} {
		if strings.Contains(all, word) {
			t.Fatalf("the job's output holds the file's text (%q):\n%s", word, all)
		}
	}
	for _, want := range []string{"a regular file of 85B, by the archive's header", "the whole file: 4 lines, 85B", "nothing was written", "only read (GET)"} {
		if !strings.Contains(all, want) {
			t.Errorf("the job's output lacks %q:\n%s", want, all)
		}
	}
	// The job's output, opened from the Jobs pane, is the same.
	b.keys("esc", "esc")
	b.m.focus, b.m.jobSel = PaneJobs, j.ID
	b.keys("enter")
	if s := b.screen(); strings.Contains(s, "worker_processes") {
		t.Fatalf("the job dialog shows the file:\n%s", s)
	}
}

func TestReaderFailures(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(true)

	// Gone by the time the reader asks.
	b.srv.FailNext("GET", `/archive$`, 404, "Could not find the file /etc/nginx/nginx.conf in container shop-web-1", 1)
	rd := b.openFile("/etc/nginx/nginx.conf")
	if rd.loaded || rd.pv.Text != nil || len(rd.view.lines) != 0 {
		t.Fatal("a missing path kept contents")
	}
	if got := b.flat(); !strings.Contains(got, "not read The path, or its container, is not there any more — the daemon says: Could not find the file /etc/nginx/nginx.conf") ||
		!strings.Contains(got, "No contents were kept.") {
		t.Fatalf("missing:\n%s", b.screen())
	}
	b.keys("esc")

	// The daemon fails.
	b.srv.FailNext("GET", `/archive$`, 500, "storage driver busy", 1)
	rd = b.openFile("/etc/nginx/nginx.conf")
	if rd.loaded {
		t.Fatal("a failed read is shown as loaded")
	}
	if got := b.flat(); !strings.Contains(got, "The daemon refused: storage driver busy. No contents were kept.") {
		t.Fatalf("refused:\n%s", b.screen())
	}
	b.keys("q")

	// The path is a link now, whatever the tree says: not followed.
	b.srv.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/etc/nginx/nginx.conf", Mode: iofs.ModeSymlink | 0o777, Link: "/etc/shadow"},
		fakeengine.FileSpec{Path: "/etc/shadow", Content: []byte("root:$6$hash\n")})
	rd = b.openFile("/etc/nginx/nginx.conf")
	if rd.loaded {
		t.Fatal("a link was read")
	}
	if got := b.flat(); !strings.Contains(got, "The daemon answered with a symbolic link to /etc/shadow: it is not followed, and nothing was read.") || strings.Contains(got, "root:") {
		t.Fatalf("a link:\n%s", b.screen())
	}
	b.keys("esc")

	// And a directory.
	b.srv.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/usr/share/nginx/html/index.html", Mode: iofs.ModeDir | 0o755},
		fakeengine.FileSpec{Path: "/usr/share/nginx/html/index.html/inner", Content: []byte("inside\n")})
	rd = b.openFile("/usr/share/nginx/html/index.html")
	if got := b.flat(); rd.loaded || !strings.Contains(got, "The daemon answered with a directory, not a regular file: nothing was read.") || strings.Contains(got, "inside") {
		t.Fatalf("a directory:\n%s", b.screen())
	}
	b.keys("esc")

	// The container has gone.
	b.srv.FailNext("GET", `/archive$`, 404, "No such container: shop-web-1", 1)
	b.openFile("/run/nginx.pid")
	if got := b.flat(); !strings.Contains(got, "is not there any more — the daemon says: No such container: shop-web-1") {
		t.Fatalf("a vanished container:\n%s", b.screen())
	}
	b.keys("esc")
	if !b.m.fs.open || b.m.fs.rd != nil {
		t.Fatal("the tree is not back")
	}
	b.noMutations("failed reads")
}

func TestReaderCancel(t *testing.T) {
	big := bytes.Repeat([]byte("a line of the big file\n"), 20_000) // 460 kB: several chunks
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/usr/share/nginx/html/index.html", Content: big})
		s.ArchiveChunkDelay = 300 * time.Millisecond
	}})
	b.webFiles(true)
	before := b.filesSnap()

	// c: the reader stays and says so.
	b.filesGo("/usr/share/nginx/html/index.html")
	before = b.filesSnap()
	b.keys("enter")
	rd := b.m.fs.rd
	if rd == nil || rd.run == nil {
		t.Fatal("no read in progress")
	}
	b.see("reading", "one request for this path (GET /containers/{id}/archive)", "c cancels")
	foot := b.screenLines()[len(b.screenLines())-1]
	if !strings.Contains(foot, "c cancel the read") || !strings.Contains(foot, "Esc q close") {
		t.Fatalf("footer while reading: %q", foot)
	}
	jobID := rd.run.jobID
	b.keys("c")
	if j := b.waitJob(jobID); j.State != jobs.Cancelled {
		t.Fatalf("the job: %v %q", j.State, j.Err)
	}
	b.until("the reader to say so", func() bool { return rd.cancelled })
	b.see("cancelled", "The read was cancelled: the stream was closed and nothing was kept.")
	if rd.loaded || rd.pv.Text != nil || len(rd.view.lines) != 0 || strings.Contains(b.screen(), "a line of the big file") {
		t.Fatal("a cancelled read kept contents")
	}
	b.keys("esc")
	if after := b.filesSnap(); after != before {
		t.Fatalf("the tree changed: %+v, was %+v", after, before)
	}

	// Esc while reading: cancelled and closed, and the job keeps nothing.
	b.keys("enter")
	run := b.m.fs.rd.run
	b.keys("esc")
	if b.m.fs.rd != nil || !b.m.fs.open {
		t.Fatal("Esc did not close the reader onto the tree")
	}
	if j := b.waitJob(run.jobID); j.State != jobs.Cancelled {
		t.Fatalf("the job after Esc: %v", j.State)
	}
	if pv, _, done := run.take(); done || pv.Text != nil {
		t.Fatal("the job of a closed reader kept its contents")
	}
	b.see("Files — the image's tree")
}

// A read that ends after the reader closed hands nothing over.
func TestReaderRunDroppedKeepsNothing(t *testing.T) {
	run := &readerRun{}
	run.drop()
	run.deliver(files.Preview{Text: []string{"secret"}}, readerMeta{size: 6})
	if pv, _, done := run.take(); done || pv.Text != nil {
		t.Fatalf("a dropped run kept %+v", pv)
	}
	run = &readerRun{}
	run.deliver(files.Preview{Text: []string{"secret"}}, readerMeta{size: 6})
	if pv, _, done := run.take(); !done || len(pv.Text) != 1 {
		t.Fatal("a delivery was lost")
	}
	if pv, _, done := run.take(); done || pv.Text != nil {
		t.Fatal("take left the contents in the run")
	}
}

func TestReaderInReadOnlyMode(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.webFiles(true)
	b.srv.ResetRequests()
	rd := b.openFile("/etc/nginx/nginx.conf")
	if !rd.loaded || rd.pv.Lines != 4 {
		t.Fatalf("read-only: %+v %q", rd.pv, rd.err)
	}
	b.see("worker_processes 4;")
	if got := b.containerRequests(); len(got) != 1 || !strings.HasPrefix(got[0], "GET ") || !strings.Contains(got[0], "/archive?") {
		t.Fatalf("the reader's requests: %v", got)
	}
	for _, r := range b.srv.Requests() {
		if r.Method != http.MethodGet {
			t.Fatalf("a %s reached the daemon: %v", r.Method, r)
		}
	}
	b.noMutations("the reader in read-only mode")
}

// ---- 2.3 the box ----------------------------------------------------------------------

func TestReaderHeaderAndClosing(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.webFiles(true)
	// A filter, an order and a scrolled list: all must survive the reader.
	b.keys("s", "/")
	b.typeText("nginx")
	b.keys("enter")
	b.filesGo("/etc/nginx/nginx.conf")
	before := b.filesSnap()
	if before.filter != "nginx" || before.order != files.BySize {
		t.Fatalf("setup: %+v", before)
	}
	treeScreen := b.screen()

	rd := b.openFile("/etc/nginx/nginx.conf")
	b.see("File — read from the container, shown here only",
		"path       /etc/nginx/nginx.conf",
		"container  shop-web-1 · "+engine.ShortID(rd.containerID),
		"type       regular file · mode 0644 (-rw-r--r--) · uid 0 gid 0 · modified 2026-11-01 09:30:00 UTC",
		"size       85B in the container now; the tree's 88B is the image's — the container changed this file",
		"source     the container's current filesystem, as the daemon read it for this request — not a snapshot",
		"shown      the whole file: 4 lines, 85B",
		"all of it · wrapped",
		"user nginx;", "worker_processes 4;", "events {}", "http { include /etc/nginx/conf.d/*.conf; }")
	b.notSee("NAME")
	b.assertFrameGeometry()
	foot := b.screenLines()[len(b.screenLines())-1]
	for _, k := range []string{"↑↓ scroll", "PgUp PgDn page", "g G ends", "w no wrap", "Esc q close", "? keys"} {
		if !strings.Contains(foot, k) {
			t.Errorf("footer lacks %q: %q", k, foot)
		}
	}
	for _, k := range []string{"export", "filter", "copy", "order"} {
		if strings.Contains(foot, k) {
			t.Errorf("the reader's footer offers %q: %q", k, foot)
		}
	}

	for _, key := range []string{"esc", "q"} {
		if b.m.fs.rd == nil {
			b.openFile("/etc/nginx/nginx.conf")
		}
		b.keys(key)
		if b.m.fs.rd != nil || b.quit {
			t.Fatalf("%s: reader %v, quit %v", key, b.m.fs.rd != nil, b.quit)
		}
		if after := b.filesSnap(); after != before {
			t.Fatalf("%s: the tree changed:\n got %+v\nwant %+v", key, after, before)
		}
		b.settle()
		if s := b.screen(); strings.Contains(s, "worker_processes") || strings.Contains(s, "user nginx;") {
			t.Fatalf("%s: contents remain on screen", key)
		}
	}
	// The tree is drawn as it was (the status line and ages aside).
	now := b.screenLines()
	was := strings.Split(treeScreen, "\n")
	for i := 3; i < len(was)-3; i++ {
		if strings.Contains(was[i], "ago") {
			continue
		}
		if now[i] != was[i] {
			t.Fatalf("line %d of the tree changed:\n got %q\nwant %q", i, now[i], was[i])
		}
	}

	// An unchanged file names the image's size too, without the warning.
	b.keys("esc") // the filter
	b.openFile("/usr/share/nginx/html/index.html")
	b.see("in the container now; the tree's", "is the image's")
	b.notSee("the container changed this file")
	b.keys("esc")
	// An added one has no size in the tree to speak of.
	b.openFile("/run/nginx.pid")
	b.see("size       2B in the container now")
	b.notSee("is the image's")
}

func TestReaderBoundsAndBinary(t *testing.T) {
	long := strings.Repeat("y", 3<<10)
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1",
			fakeengine.FileSpec{Path: "/srv/big.log", Content: bytes.Repeat([]byte(strings.Repeat("x", 1023)+"\n"), 3<<10)},
			fakeengine.FileSpec{Path: "/srv/many.log", Content: bytes.Repeat([]byte("row\n"), 20_000)},
			fakeengine.FileSpec{Path: "/srv/long.txt", Content: []byte("first\n" + long + "\nlast\n")},
			fakeengine.FileSpec{Path: "/srv/empty", Content: nil},
			fakeengine.FileSpec{Path: "/srv/latin1.txt", Content: []byte("caf\xe9 au lait\n")},
		)
		s.SetChanges("shop-web-1", append(fakeengine.DemoChanges(), fakeengine.Changed("/srv"),
			fakeengine.Added("/srv/big.log"), fakeengine.Added("/srv/many.log"), fakeengine.Added("/srv/long.txt"),
			fakeengine.Added("/srv/empty"), fakeengine.Added("/srv/latin1.txt"))...)
	}})
	b.webFiles(true)

	rd := b.openFile("/srv/big.log")
	if !rd.pv.CutBytes || rd.pv.Read != files.PreviewMaxBytes || rd.meta.size != 3<<20 {
		t.Fatalf("3 MiB: %+v", rd.meta)
	}
	if got := b.flat(); !strings.Contains(got, "3.1MB in the container now") ||
		!strings.Contains(got, "shown cut — the first 1 MiB of 3.1MB: the rest was cut, unread · 1,024 lines (o copies the whole file out)") {
		t.Fatalf("3 MiB:\n%s", b.screen())
	}
	b.assertFrameGeometry()
	b.keys("esc")

	rd = b.openFile("/srv/many.log")
	if !rd.pv.CutLines || rd.pv.Lines != files.PreviewMaxLines {
		t.Fatalf("20,000 lines: %d", rd.pv.Lines)
	}
	if got := b.flat(); !strings.Contains(got, "shown cut — the first 10,000 lines: the lines after them were cut") {
		t.Fatalf("20,000 lines:\n%s", b.screen())
	}
	// Scrolling: to the end and back, a page and a row at a time.
	b.see("rows 1–")
	b.keys("G")
	b.see("of 10000 · wrapped")
	if rd.view.off != len(rd.view.wrapped)-rd.pageH {
		t.Fatalf("G: off %d of %d, page %d", rd.view.off, len(rd.view.wrapped), rd.pageH)
	}
	b.keys("g", "pgdown", "j", "j", "k")
	if want := rd.pageH - 1 + 1; rd.view.off != want {
		t.Fatalf("after g PgDn j j k: off %d, want %d", rd.view.off, want)
	}
	b.keys("pgup", "up", "up")
	if rd.view.off != 0 {
		t.Fatalf("off %d at the top", rd.view.off)
	}
	b.keys("esc")

	rd = b.openFile("/srv/long.txt")
	if !rd.pv.CutLine || rd.pv.LongLines != 1 || rd.pv.Lines != 3 {
		t.Fatalf("a 3 KiB line: %+v", rd.pv.Lines)
	}
	if got := b.flat(); !strings.Contains(got, "shown cut — 3 lines · 1 line longer than 2 KiB cut there (o copies the whole file out)") {
		t.Fatalf("a long line:\n%s", b.screen())
	}
	// Wrapped, the whole kept line is on screen; unwrapped, one row a line.
	if n := strings.Count(b.screen(), "y"); n < 2000 {
		t.Fatalf("wrapped: %d of the line's 2048 characters are drawn", n)
	}
	b.keys("w")
	b.see("long lines end at the edge, w wraps", "first", "last")
	if len(rd.view.wrapped) != 3 {
		t.Fatalf("unwrapped rows: %d", len(rd.view.wrapped))
	}
	b.assertFrameGeometry()
	b.keys("w")
	b.see("· wrapped")
	b.keys("esc")

	b.openFile("/srv/empty")
	b.see("0B in the container now", "the whole file: it is empty", "The file is empty.")
	b.keys("esc")

	// Binary: metadata, and not a byte.
	for _, p := range []string{"/var/lib/nginx/state.db", "/srv/latin1.txt"} {
		rd = b.openFile(p)
		if !rd.pv.Binary || rd.pv.Text != nil || len(rd.view.lines) != 0 {
			t.Fatalf("%s: %+v", p, rd.pv)
		}
		got := b.flat()
		for _, want := range []string{"path " + p, "type regular file · mode 06", "in the container now", "shown not text: the", "contains a NUL byte or is not valid UTF-8, so no contents are shown, and there is no hex view",
			"Not text. The reader shows valid UTF-8 without NUL bytes only: no bytes of this file are drawn"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", p, want, b.screen())
			}
		}
		if strings.Contains(got, "SQLite") || strings.Contains(got, "caf") || strings.Contains(got, "lait") {
			t.Fatalf("%s: bytes of a binary file are drawn:\n%s", p, b.screen())
		}
		foot := b.screenLines()[len(b.screenLines())-1]
		if strings.Contains(foot, "scroll") {
			t.Errorf("a binary file offers scrolling: %q", foot)
		}
		b.assertFrameGeometry()
		b.keys("esc")
	}
}

func TestReaderAt80Columns(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 24})
	b.webFiles(true)
	b.openFile("/etc/nginx/nginx.conf")
	b.see("File", "path       /etc/nginx/nginx.conf", "user nginx;", "worker_processes 4;", "events {}")
	b.assertFrameGeometry()
	// A terminal resized while it is open keeps its geometry.
	b.step(tea.WindowSizeMsg{Width: 96, Height: 27})
	b.assertFrameGeometry()
	b.see("user nginx;")
}

// ---- 2.4 keys and the pointer ---------------------------------------------------------

func TestReaderTakesEveryKey(t *testing.T) {
	b := newBench(t, benchOpts{})
	home := b.exportHome()
	b.webFiles(true)
	b.filesGo("/etc/nginx/nginx.conf")
	b.openFile("/etc/nginx/nginx.conf")
	before := b.filesSnap()
	deskFocus, deskKind := b.m.focus, b.m.kind
	b.srv.ResetRequests()
	jobsBefore := len(b.m.jobs.List())
	for _, k := range []string{
		"j", "k", "down", "up", "pgdown", "pgup", "g", "G", "home", "end", " ", "ctrl+f", "ctrl+b",
		"s", "/", "x", "enter", "right", "left", "l", "h", "o", "i", "r", "a", "F", "T", "c", "w", "w",
		"1", "2", "tab", "shift+tab", "]", "[", "S", "B", "d", "n", "y", "X", "M", "backspace",
	} {
		b.keys(k)
		if b.m.fs.rd == nil || !b.m.fs.open {
			t.Fatalf("%q closed the reader", k)
		}
		if b.m.dlg != nil {
			t.Fatalf("%q opened a dialog behind the reader", k)
		}
		if after := b.filesSnap(); after != before {
			t.Fatalf("%q reached the tree:\n got %+v\nwant %+v", k, after, before)
		}
		if b.m.focus != deskFocus || b.m.kind != deskKind {
			t.Fatalf("%q reached the desk", k)
		}
	}
	b.settle()
	if len(b.srv.Requests()) != 0 || len(b.m.jobs.List()) != jobsBefore {
		t.Fatalf("keys in the reader sent %v", b.srv.Requests())
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("something was written: %v", ents)
	}
	// ? is the key list, over the reader; closing it returns to the reader.
	b.keys("?")
	if b.m.dlg == nil || b.m.dlg.kind != dlgHelp {
		t.Fatal("? did not open the keys")
	}
	b.keys("esc")
	if b.m.dlg != nil || b.m.fs.rd == nil {
		t.Fatal("closing the keys closed the reader")
	}
	b.see("worker_processes 4;")
}

func TestReaderMouse(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/usr/share/nginx/html/index.html", Content: bytes.Repeat([]byte("row\n"), 500)})
	}})
	b.webFiles(true)
	b.filesGo("/usr/share/nginx/html/index.html")
	b.draw()
	if len(b.hits(hitFilesRow)) == 0 {
		t.Fatal("the tree records no rows")
	}
	treeRow := b.hits(hitFilesRow)[0]
	rd := b.openFile("/usr/share/nginx/html/index.html")
	before := b.filesSnap()
	b.srv.ResetRequests()
	b.draw()
	if rows := b.hits(hitFilesRow); len(rows) != 0 {
		t.Fatalf("%d tree rows are in the hit map behind the reader", len(rows))
	}
	if texts := b.hits(hitText); len(texts) != 0 {
		t.Fatalf("a text row is in the hit map behind the reader: %v", texts)
	}
	over := b.hitOf(hitOverlay, nil)
	// A click where a tree row was does nothing.
	b.click(treeRow)
	b.click(over)
	if after := b.filesSnap(); after != before || b.m.fs.rd == nil || rd.view.off != 0 {
		t.Fatalf("a click reached something: %+v", after)
	}
	// The wheel scrolls the reader, and only it.
	b.wheel(over, 1)
	b.wheel(treeRow, 1)
	if rd.view.off != 2*mouseWheelRows {
		t.Fatalf("after two notches the reader is at %d", rd.view.off)
	}
	b.wheel(over, -1)
	b.draw()
	if rd.view.off != mouseWheelRows {
		t.Fatalf("after a notch back the reader is at %d", rd.view.off)
	}
	if after := b.filesSnap(); after != before {
		t.Fatalf("the wheel reached the tree: %+v", after)
	}
	b.settle()
	if len(b.srv.Requests()) != 0 {
		t.Fatalf("the pointer sent %v", b.srv.Requests())
	}
	// Closed, the tree's rows are back.
	b.keys("esc")
	b.draw()
	if len(b.hits(hitFilesRow)) == 0 {
		t.Fatal("the tree's rows did not come back")
	}
}

// ---- 3.1 the content boundary ---------------------------------------------------------

func TestReaderContentStaysInTheReader(t *testing.T) {
	const marker = "TOP-SECRET-7f3a"
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/etc/nginx/nginx.conf", Content: []byte("password " + marker + ";\nsecond line\n")})
	}})
	home := b.exportHome()
	b.webFiles(true)
	b.srv.ResetRequests()
	rd := b.openFile("/etc/nginx/nginx.conf")
	b.see("password " + marker + ";")
	// The only request about the container is the one archive GET.
	if got := b.containerRequests(); len(got) != 1 || !strings.HasPrefix(got[0], "GET ") || !strings.Contains(got[0], "/archive?path=%2Fetc%2Fnginx%2Fnginx.conf") {
		t.Fatalf("requests: %v", got)
	}
	if b.exports() != 0 {
		t.Fatal("the image's archive was read")
	}
	// Not in the index, nor in what the filter searches.
	ix := b.m.filesTree()
	v := files.Flatten(ix, map[string]bool{}, marker, files.ByName)
	if v.Matches != 0 {
		t.Fatal("the filter finds the file's contents")
	}
	// Not in any job.
	for _, j := range b.m.jobs.List() {
		j, _ = b.m.jobs.Get(j.ID)
		if all := j.Title + j.Target + j.Status + j.Err + strings.Join(j.Log, "\n"); strings.Contains(all, marker) || strings.Contains(all, "second line") {
			t.Fatalf("job %d holds the contents: %s", j.ID, all)
		}
	}
	// Closed: dropped, and the screen, the status line and the reader's
	// state hold none of it.
	run := rd.run
	b.keys("esc")
	b.settle()
	if b.m.fs.rd != nil || run != nil {
		t.Fatal("the reader's state remains")
	}
	if s := b.screen(); strings.Contains(s, marker) || strings.Contains(b.m.status.text, marker) {
		t.Fatal("contents remain after closing")
	}
	// The export of the listing is names and metadata, as before.
	b.keys("w")
	if b.m.dlg == nil {
		t.Fatalf("no export dialog:\n%s", b.screen())
	}
	b.keys("enter")
	b.see("Write this file?")
	b.keys("y")
	b.see("Exported text to")
	exported, _ := filepath.Glob(filepath.Join(home, "docker-desk-files-*"))
	if len(exported) != 1 {
		t.Fatalf("exports: %v", exported)
	}
	body, err := os.ReadFile(exported[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("/etc/nginx/nginx.conf")) {
		t.Fatalf("the export lacks the path:\n%s", body)
	}
	if bytes.Contains(body, []byte(marker)) || bytes.Contains(body, []byte("second line")) {
		t.Fatal("the file's contents reached the export")
	}
	// Nothing else was written on this machine.
	if ents, _ := os.ReadDir(home); len(ents) != 1 {
		t.Fatalf("files written: %v", ents)
	}
	b.noMutations("the reader")
}

// ---- 3.2 escaping, end to end ---------------------------------------------------------

func TestReaderEscapesHostileContentAndPath(t *testing.T) {
	// (The change list travels as JSON, so the path is valid UTF-8.)
	const hostilePath = "/srv/\x1b[31mred\nforged\u202e.txt"
	ansiFile := "plain\n" +
		"\x1b[2J\x1b[H\x1b[31mRED\x1b[0m\n" + // clear screen, home, colour
		"\x1b]0;owned\x07 \x1b]52;c;ZWNobyBwd25lZA==\x07\n" + // title, clipboard
		"\u009b31m c1 \u202egnp.exe\n" + // C1 CSI, bidi override
		"a\rb\x08\x7f\ttab\n"
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1",
			fakeengine.FileSpec{Path: "/srv/ansi.txt", Content: []byte(ansiFile)},
			fakeengine.FileSpec{Path: "/srv/nul.txt", Content: []byte("text then\x00\x1b[31mNUL\n")},
			fakeengine.FileSpec{Path: "/srv/bad.txt", Content: []byte("text then \xff\xfe\x1b[31m bad\n")},
			fakeengine.FileSpec{Path: hostilePath, Content: []byte("inside\x1b[5m\n")},
		)
		s.SetChanges("shop-web-1", append(fakeengine.DemoChanges(), fakeengine.Changed("/srv"),
			fakeengine.Added("/srv/ansi.txt"), fakeengine.Added("/srv/nul.txt"), fakeengine.Added("/srv/bad.txt"), fakeengine.Added(hostilePath))...)
	}})
	b.webFiles(true)
	planted := []string{"\x1b[2J", "\x1b[H", "\x1b[31mRED", "\x1b]0;", "\x1b]52;", "\x07", "\u009b", "\u202e", "\x1b[31mNUL", "\x1b[31m bad", "\x1b[5m", "\x1b[31mred", "\xff"}

	rd := b.openFile("/srv/ansi.txt")
	if rd.pv.Binary || rd.pv.Lines != 5 {
		t.Fatalf("%+v", rd.pv)
	}
	b.see("plain", `\x1b[2J\x1b[H\x1b[31mRED\x1b[0m`, `\x1b]0;owned\x07 \x1b]52;c;ZWNobyBwd25lZA==\x07`, `\u009b31m c1 \u202egnp.exe`, `a\rb\x08\x7f    tab`)
	b.rawSafe(planted...)
	b.keys("w") // unwrapped too
	b.rawSafe(planted...)
	b.assertFrameGeometry()
	b.keys("esc")

	for _, p := range []string{"/srv/nul.txt", "/srv/bad.txt"} {
		rd = b.openFile(p)
		if !rd.pv.Binary || rd.pv.Text != nil {
			t.Fatalf("%s is shown as text", p)
		}
		b.see("Not text.")
		if s := b.screen(); strings.Contains(s, "text then") || strings.Contains(s, "NUL\n") || strings.Contains(s, " bad") {
			t.Fatalf("%s: bytes are drawn:\n%s", p, s)
		}
		b.rawSafe(planted...)
		b.keys("esc")
	}

	// The hostile path: asked for byte for byte, drawn escaped everywhere.
	b.srv.ResetRequests()
	rd = b.openFile(hostilePath)
	if got := b.fileGets(); len(got) != 1 || got[0] != hostilePath {
		t.Fatalf("the path asked for: %q", got)
	}
	if !rd.loaded {
		t.Fatalf("not read: %q", rd.err)
	}
	escaped := `/srv/\x1b[31mred\nforged\u202e.txt`
	b.see("path       "+escaped, `inside\x1b[5m`)
	for _, l := range b.screenLines() {
		if strings.HasPrefix(strings.TrimLeft(l, "┃ "), "forged") {
			t.Fatalf("the path forged a line: %q", l)
		}
	}
	b.rawSafe(planted...)
	j := b.lastJob()
	j, _ = b.m.jobs.Get(j.ID)
	if all := j.Title + j.Target + j.Status + j.Err + strings.Join(j.Log, "\n"); strings.ContainsRune(all, 0x1b) || strings.Contains(all, "\n"+"forged") || !strings.Contains(j.Target, escaped) {
		t.Fatalf("the job names the path raw: %q", all)
	}
	b.rawSafe(planted...) // the status line that announces the job, too
	b.keys("esc")
	b.rawSafe(planted...)
}

// ---- help -----------------------------------------------------------------------------

func TestReaderIsDocumentedInTheKeys(t *testing.T) {
	b := newBench(t, benchOpts{width: 140, height: 50})
	text := strings.Join(b.m.helpLines(120), "\n")
	for _, want := range []string{
		"a container, on a regular file: read that file and show its text",
		"read the image's archive from any row",
		"Esc or q closes",
		"At most 1 MiB, 10,000 lines and 2 KiB a line are kept",
		"there is no hex view",
		"never exported, searched or logged, written to a file only",
		"by W and its confirmation",
		"An image's files are not read",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the keys lack %q", want)
		}
	}
}

// ---- from the independent review -------------------------------------------------------

// A daemon's megabyte of error text, or an archive's megabyte of name, is
// repeated as a sentence, not in full.
func TestReaderRepeatsOnlyASentenceOfWhatTheDaemonSays(t *testing.T) {
	huge := strings.Repeat("\x1b[31mA", 200_000)
	eng := &stubEngine{archive: func(context.Context) (engine.Archive, error) {
		return engine.Archive{}, &engine.Error{Kind: engine.KindUnavailable, Msg: huge}
	}}
	_, _, err := readContainerFile(context.Background(), eng, "cid", "/etc/app.conf", nil)
	if err == nil || len(err.Error()) > 4*readerErrMax+200 || !strings.Contains(err.Error(), "… (cut)") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("a huge refusal: %d bytes", len(err.Error()))
	}
	body := &closeTracker{Reader: bytes.NewReader(tarOf(t, &tar.Header{Name: strings.Repeat("n", 5000), Typeflag: tar.TypeReg, Size: 12, Format: tar.FormatPAX}))}
	eng.archive = func(context.Context) (engine.Archive, error) {
		return engine.Archive{Content: body, Stat: engine.PathStat{Name: "app.conf", Mode: 0o644}}, nil
	}
	_, _, err = readContainerFile(context.Background(), eng, "cid", "/etc/app.conf", nil)
	if err == nil || len(err.Error()) > readerErrMax+200 || !strings.Contains(err.Error(), "… (cut), not app.conf") || !body.closed {
		t.Fatalf("a huge name: %v", err)
	}
	// A cut inside a character leaves no raw byte.
	if got := shown(strings.Repeat("a", readerErrMax-1) + "é"); strings.ContainsRune(got, 0xfffd) || !strings.HasSuffix(got, `\xc3… (cut)`) {
		t.Fatalf("shown: %q", got[len(got)-20:])
	}
}

// The focused daemon changing while a file is read cancels the read, and
// its job hands nothing over.
func TestReaderIsDroppedWhenTheDaemonChanges(t *testing.T) {
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SeedFiles("shop-web-1", fakeengine.FileSpec{Path: "/usr/share/nginx/html/index.html", Content: bytes.Repeat([]byte("a line of the big file\n"), 20_000)})
		s.ArchiveChunkDelay = 300 * time.Millisecond
	}})
	b.webFiles(true)
	b.filesGo("/usr/share/nginx/html/index.html")
	b.keys("enter")
	run := b.m.fs.rd.run
	if run == nil {
		t.Fatal("no read in progress")
	}
	b.exec(b.m.switchTo(nil))
	if b.m.fs.rd != nil {
		t.Fatal("the reader outlived its daemon")
	}
	if j := b.waitJob(run.jobID); j.State != jobs.Cancelled {
		t.Fatalf("the job: %v", j.State)
	}
	if pv, _, done := run.take(); done || pv.Text != nil {
		t.Fatal("the orphaned job kept its contents")
	}
}
