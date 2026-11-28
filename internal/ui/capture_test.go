package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/capture"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// ---- helpers ----------------------------------------------------------------------

// talkerLines are what "talker" has written when the desk opens: colour,
// padding and a line every filter below tells apart.
var talkerLines = []string{
	"\x1b[32mINFO\x1b[0m boot complete        ",
	"\x1b[31mERROR\x1b[0m disk quota exceeded",
	"plain line three",
	"\x1b[33mWARN\x1b[0m retrying in 5s",
	"ERROR second failure",
}

// captureSeed is the demo, plus a quiet container whose log is known, and
// a long command line in shop-web-1's process table.
func captureSeed(s *fakeengine.Server) {
	s.SeedDemo()
	s.AddContainer(fakeengine.ContainerSpec{Name: "talker", Image: "alpine:3.20", Logs: talkerLines})
	s.SetProcesses("shop-web-1", &fakeengine.ProcessTable{
		Titles: []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
		Rows: [][]string{
			{"root", "1", "0", "0", "10:00", "?", "00:00:00", "java -cp " + longClasspath + " com.example.Main"},
			{"a-user-name-longer-than-twenty-four-cells", "7", "1", "0", "10:00", "?", "00:00:00", "sleep 1"},
		},
	})
}

var longClasspath = strings.Repeat("/opt/app/lib/dependency.jar:", 200) + "/opt/app/end.jar"

// toTab highlights name in kind and shows tab, Details focused.
func (b *bench) toTab(k Kind, name, tab string) {
	b.t.Helper()
	b.kind(k)
	b.m.focus = PaneList
	b.selectRow(name)
	for b.m.tabName() != tab {
		b.keys("]")
	}
	b.m.focus = PaneDetails
	b.settle()
}

// captureDialog presses W and returns the collected capture.
func (b *bench) captureDialog() *paneCapture {
	b.t.Helper()
	b.keys("W")
	if b.m.dlg == nil || b.m.dlg.kind != dlgExport || b.m.dlg.exp.capture == nil {
		b.t.Fatalf("W opened no capture dialog (status %q):\n%s", b.m.status.text, b.screen())
	}
	return b.m.dlg.exp.capture
}

// saved waits for the write's sentence.
func (b *bench) saved() string {
	b.t.Helper()
	b.until("the capture to be written", func() bool {
		return strings.HasPrefix(b.m.status.text, "Saved") || strings.HasPrefix(b.m.status.text, "Not saved")
	})
	return b.m.status.text
}

// saveCapture is W, Enter, y: the file's path and bytes.
func (b *bench) saveCapture() (string, []byte) {
	b.t.Helper()
	b.captureDialog()
	b.keys("enter")
	if !b.m.dlg.exp.confirm {
		b.t.Fatalf("no confirmation: %q\n%s", b.m.dlg.formErr, b.screen())
	}
	p := b.m.dlg.exp.abs
	b.keys("y")
	if s := b.saved(); !strings.HasPrefix(s, "Saved") {
		b.t.Fatalf("status %q", s)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		b.t.Fatal(err)
	}
	return p, data
}

func (b *bench) resize(w, h int) {
	b.step(tea.WindowSizeMsg{Width: w, Height: h})
	b.settle()
	b.screen()
}

// filesIn lists a folder's names.
func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func noEscapes(t *testing.T, what string, data []byte) {
	t.Helper()
	for _, r := range string(data) {
		if r == 0x1b || (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("%s: a control character %U is in the file", what, r)
		}
	}
}

// waitLogs waits for talker's lines to be held.
func (b *bench) waitLogs(n int) {
	b.t.Helper()
	b.until("the log lines", func() bool {
		ring, st := b.m.hub.Log()
		_, end := ring.Bounds()
		return st.Target == b.srv.ContainerID("talker") && end >= uint64(n)
	})
}

// ---- 2.1, 2.2, 2.3: the key, the dialog, the writer ------------------------------------

// TestCaptureLogsAsksThenWritesOneFile: W on Logs opens the dialog with
// one format and what the file holds; nothing is written before y, and one
// file after it, 0600, with the status sentence.
func TestCaptureLogsAsksThenWritesOneFile(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	home := b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(talkerLines))

	c := b.captureDialog()
	if got, want := b.exportPath(), "~/docker-desk-logs-talker-20261109.txt"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	b.see("Save this Logs text", "holds", "5 held log lines as the desk shows them, colour removed",
		"▸ text", "(one for this pane)", "It is not a report.")
	if c.format != "text" || len(b.m.dlg.exp.formats) != 1 {
		t.Fatalf("formats %v", b.m.dlg.exp.formats)
	}
	// Tab and the arrows change nothing.
	before := b.exportPath()
	b.keys("tab", "down", "shift+tab", "up")
	if b.exportPath() != before || b.m.dlg.exp.fsel != 0 {
		t.Errorf("Tab changed the format or the path: %q", b.exportPath())
	}
	b.keys("enter")
	b.see("Write this file?", "0600: only you can read it", "It is not a report.")
	if len(filesIn(t, home)) != 0 {
		t.Fatal("written before y")
	}
	want := filepath.Join(home, "docker-desk-logs-talker-20261109.txt")
	b.keys("y")
	s := b.saved()
	if !strings.HasPrefix(s, "Saved text to "+want+" (") || !strings.HasSuffix(s, " bytes, mode 0600).") {
		t.Fatalf("status %q", s)
	}
	if got := filesIn(t, home); len(got) != 1 || got[0] != filepath.Base(want) {
		t.Fatalf("files written: %v", got)
	}
	fi, _ := os.Stat(want)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	data, _ := os.ReadFile(want)
	noEscapes(t, "logs", data)
	if string(data) != string(c.data) {
		t.Error("the file is not the bytes collected when W was pressed")
	}
	b.noMutations("a capture")
}

// TestCaptureNeverReplaces: a path holding a file is refused, and so is a
// folder that does not exist; nothing is replaced or created.
func TestCaptureNeverReplaces(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	home := b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(talkerLines))

	existing := filepath.Join(home, "docker-desk-logs-talker-20261109.txt")
	os.WriteFile(existing, []byte("keep me"), 0o644)
	b.captureDialog()
	b.keys("enter")
	b.see("A file is already there; the desk never replaces one.")
	if b.m.dlg.exp.confirm {
		t.Fatal("an existing file reached the confirmation")
	}
	// A file that appears after the review is refused by the writer.
	b.typePath("~/late.txt")
	b.keys("enter")
	late := filepath.Join(home, "late.txt")
	os.WriteFile(late, []byte("late"), 0o644)
	b.keys("y")
	if s := b.saved(); !strings.HasPrefix(s, "Not saved: cannot write "+late+": a file") {
		t.Errorf("status %q", s)
	}
	for p, w := range map[string]string{existing: "keep me", late: "late"} {
		if got, _ := os.ReadFile(p); string(got) != w {
			t.Errorf("%s was replaced: %q", p, got)
		}
	}
	// A missing folder: nothing written, nothing created.
	b.captureDialog()
	b.typePath("~/missing/x.txt")
	b.keys("enter", "y")
	if s := b.saved(); !strings.Contains(s, "Not saved: cannot write") || !strings.Contains(s, "does not exist") {
		t.Errorf("status %q", s)
	}
	if _, err := os.Stat(filepath.Join(home, "missing")); err == nil {
		t.Error("a folder was created")
	}
}

// ---- 2.4: nothing held, nothing written ---------------------------------------------

func TestCaptureRefusesAnEmptyPane(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	home := b.exportHome()
	refused := func(what, want string) {
		t.Helper()
		b.m.status.text = ""
		b.keys("W")
		if b.m.dlg != nil {
			t.Fatalf("%s: a dialog opened", what)
		}
		if b.m.status.text != "Nothing to save: "+want {
			t.Errorf("%s: status %q, want %q", what, b.m.status.text, "Nothing to save: "+want)
		}
		if n := len(filesIn(t, home)); n != 0 {
			t.Fatalf("%s: %d files written", what, n)
		}
	}

	// Inspect, before its document has arrived.
	b.toTab(KindContainers, "talker", tabInspect)
	b.until("inspect", func() bool { return b.m.detail.container != nil })
	b.m.detail.container, b.m.detail.loading = nil, true
	refused("inspect loading", "the inspect document has not arrived yet.")

	// Logs, with no stream for the highlighted container.
	b.toTab(KindContainers, "talker", tabLogs)
	b.m.hub.SetLogTarget("")
	refused("no log stream", "the log stream for this container is not open yet, so no line is held.")

	// Changes, before the comparison is built.
	b.toTab(KindContainers, "talker", tabChanges)
	b.m.detail.container = nil
	refused("changes not built", "the comparison has not been built yet.")

	// Declared, before Enter.
	b.toTab(KindCompose, "shop", tabDeclared)
	refused("declared not compared", "the comparison with the compose file has not been made yet (Enter makes it).")

	// A job with no output.
	quiet := b.m.jobs.Start("quiet job", "nothing", func(ctx context.Context, r *jobs.Run) error {
		<-ctx.Done()
		return ctx.Err()
	})
	b.m.focus = PaneJobs
	b.m.jobSel = quiet
	refused("job without output", "this job has no output yet.")
	b.m.jobs.Cancel(quiet)

	// Stats, Findings, the List: no dialog, nothing said, nothing done.
	for _, at := range []func(){
		func() { b.toTab(KindContainers, "shop-web-1", tabStats) },
		func() { b.toTab(KindImages, "shop/api:dev", tabFindings) },
		func() { b.kind(KindContainers); b.m.focus = PaneList },
	} {
		at()
		b.m.status.text = ""
		b.srv.ResetRequests()
		b.keys("W")
		if b.m.dlg != nil || b.m.status.text != "" {
			t.Errorf("W did something on %s: %v %q", b.m.tabName(), b.m.dlg, b.m.status.text)
		}
		b.noMutations("W on a pane with no capture")
	}
}

// ---- 2.5: read-only, and no request ------------------------------------------------

func TestCaptureInReadOnlyModeSendsNothing(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, readOnly: true})
	home := b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(talkerLines))
	b.settle()

	before := len(b.srv.Requests())
	b.captureDialog()
	b.keys("enter", "y")
	if s := b.saved(); !strings.HasPrefix(s, "Saved") {
		t.Fatalf("read-only: %q", s)
	}
	if after := len(b.srv.Requests()); after != before {
		t.Errorf("the capture sent %d request(s): %v", after-before, b.srv.Requests()[before:])
	}
	if n := len(filesIn(t, home)); n != 1 {
		t.Errorf("%d files", n)
	}
}

// ---- 3.1 Info ------------------------------------------------------------------------

func TestCaptureInfoIsTheSameAtAnyWidth(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, width: 80, height: 30})
	b.exportHome()
	b.toTab(KindContainers, "shop-web-1", tabInfo)
	b.until("inspect", func() bool { return b.m.detail.container != nil && b.m.detail.container.Name == "shop-web-1" })
	b.settle()
	narrow := b.captureDialog().data
	b.keys("esc")
	b.resize(200, 50)
	wide := b.captureDialog().data
	if string(narrow) != string(wide) {
		t.Fatalf("80 and 200 columns differ:\n%s\n---\n%s", narrow, wide)
	}
	noEscapes(t, "info", wide)
	for _, w := range []string{"# pane: Info\n", "# object: shop-web-1\n", "\nName  ", "shop-web-1\n", "Ports\n"} {
		if !strings.Contains(string(wide), w) {
			t.Errorf("info lacks %q:\n%s", w, wide)
		}
	}
}

// ---- 3.2 Inspect ---------------------------------------------------------------------

func TestCaptureInspectIsTheDaemonsDocument(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	home := b.exportHome()
	b.toTab(KindContainers, "shop-web-1", tabInspect)
	b.until("inspect", func() bool { return b.m.detail.container != nil && b.m.detail.container.Name == "shop-web-1" })
	b.captureDialog()
	if got, want := b.exportPath(), "~/docker-desk-inspect-shop-web-1-20261109.json"; got != want {
		t.Fatalf("prefilled %q, want %q", got, want)
	}
	b.see("Save the inspect document", "▸ json")
	if !strings.Contains(b.m.dlg.exp.capture.holds, "environment variable values included") {
		t.Errorf("holds %q", b.m.dlg.exp.capture.holds)
	}
	b.keys("esc")
	p, data := b.saveCapture()
	if filepath.Dir(p) != home {
		t.Fatal(p)
	}
	if data[0] != '{' {
		t.Errorf("the first byte is %q, not the document's brace", data[0])
	}
	var doc struct {
		ID, Name string
		Config   struct{ Env []string }
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("does not parse: %v", err)
	}
	if doc.ID != b.srv.ContainerID("shop-web-1") || doc.Name != "/shop-web-1" {
		t.Errorf("describes %s %s", doc.ID, doc.Name)
	}
	if want, _ := capture.JSON(b.m.detail.container.Raw); string(want) != string(data) {
		t.Error("not the daemon's document, indented")
	}
}

// ---- 3.3 Logs ------------------------------------------------------------------------

func TestCaptureLogsHoldsTheBuffer(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, width: 80, height: 24})
	b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(talkerLines))

	// Unfiltered, scrolled back to the top: every line, the newest too.
	b.keys("g")
	if b.m.logFollow {
		t.Fatal("not scrolled back")
	}
	c := b.captureDialog()
	b.keys("esc")
	body := captureBody(t, c.data)
	for i, l := range talkerLines {
		if want := strings.TrimRight(stripSGR(l), " "); body[i] != want {
			t.Errorf("line %d: %q, want %q", i, body[i], want)
		}
	}
	if len(body) != len(talkerLines) {
		t.Errorf("%d lines, want %d", len(body), len(talkerLines))
	}
	if !strings.Contains(string(c.data), "# lines: 5 held, oldest first — every held line is below\n") {
		t.Errorf("header:\n%s", c.data)
	}
	b.resize(200, 50)
	if wide := b.captureDialog().data; string(wide) != string(c.data) {
		t.Errorf("80 and 200 columns differ")
	}
	b.keys("esc")

	// Filtered: only the matches, and the counts.
	b.keys("/")
	b.typeText("error")
	b.keys("enter")
	f := b.captureDialog()
	got := captureBody(t, f.data)
	if strings.Join(got, "|") != "ERROR disk quota exceeded|ERROR second failure" {
		t.Errorf("filtered body %q", got)
	}
	if !strings.Contains(string(f.data), "# filter: error — 2 of 5 held lines match; only those are below\n") {
		t.Errorf("header:\n%s", f.data)
	}
	b.see("2 of 5 held log lines")
	noEscapes(t, "filtered logs", f.data)
}

// TestCaptureLogsRefusesAnotherContainersStream: a stream that is not the
// highlighted container's is never written under its name.
func TestCaptureLogsRefusesAnotherContainersStream(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(talkerLines))
	r, _ := b.m.currentRow()
	r.containerID = b.srv.ContainerID("shop-web-1")
	if c := b.m.captureLogs(r); c != nil || !strings.Contains(b.m.status.text, "not open yet") {
		t.Errorf("captured another container's log: %v %q", c, b.m.status.text)
	}
}

func stripSGR(s string) string { return capture.Plain(s) }

// captureBody is a text capture's lines after its header.
func captureBody(t *testing.T, data []byte) []string {
	t.Helper()
	_, body, ok := strings.Cut(string(data), "\n#\n")
	if !ok {
		t.Fatalf("no header end:\n%s", data)
	}
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}

// ---- 3.4 Events ----------------------------------------------------------------------

func TestCaptureEventsHoldsEveryEventWhole(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, width: 80, height: 24})
	b.exportHome()
	long := strings.Repeat("a-very-long-network-name-", 6)
	for i := 0; i < 40; i++ {
		b.srv.Emit("network", "create", "n"+itoa(i), map[string]string{"name": long + itoa(i)})
	}
	b.until("the events", func() bool { return b.m.hub.Events.Len() >= 40 })
	b.m.focus = PaneEvents
	b.settle()
	b.screen()
	c := b.captureDialog()
	if got, want := b.exportPath(), "~/docker-desk-events-default-20261109.txt"; got != want {
		t.Errorf("prefilled %q, want %q", got, want)
	}
	body := captureBody(t, c.data)
	if len(body) < 40 || len(body) <= b.m.eventHeight {
		t.Errorf("%d lines; the pane shows %d", len(body), b.m.eventHeight)
	}
	if strings.Contains(string(c.data), "…") {
		t.Errorf("a cut value:\n%s", c.data)
	}
	if !strings.Contains(string(c.data), long+"39") {
		t.Errorf("the newest event's name is not whole")
	}
}

// ---- 3.5 Processes -------------------------------------------------------------------

func TestCaptureProcessesKeepsEveryCellWhole(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, width: 80, height: 24})
	b.exportHome()
	b.toTab(KindContainers, "shop-web-1", tabProcesses)
	b.until("the snapshot", func() bool { return b.m.proc.list != nil })
	tops := b.tops()
	c := b.captureDialog()
	if b.tops() != tops {
		t.Error("the capture took a new snapshot")
	}
	s := string(c.data)
	for _, w := range []string{"java -cp " + longClasspath + " com.example.Main", "a-user-name-longer-than-twenty-four-cells", "# snapshot taken at "} {
		if !strings.Contains(s, w) {
			t.Errorf("lacks %.60q", w)
		}
	}
	if strings.Contains(s, "…") {
		t.Error("a cell was cut")
	}
	b.keys("esc")
	b.resize(200, 50)
	if wide := b.captureDialog().data; string(wide) != s {
		t.Error("80 and 200 columns differ")
	}
}

// ---- 3.6 Changes and Declared ---------------------------------------------------------

func TestCaptureChangesWrapsAtThePage(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 24})
	b.exportHome()
	b.kind(KindContainers)
	b.m.focus = PaneList
	b.selectRow("report-job")
	b.until("container inspect", func() bool { return b.m.detail.container != nil && b.m.detail.container.Name == "report-job" })
	for b.m.tabName() != tabChanges {
		b.keys("]")
	}
	b.m.focus = PaneDetails
	b.until("both images read", func() bool { return len(b.m.chg.facts) == 2 })
	b.settle()
	c := b.captureDialog()
	b.keys("esc")
	body := captureBody(t, c.data)
	longest := 0
	for _, l := range body {
		longest = max(longest, len([]rune(l)))
	}
	if longest <= 80 || longest > capture.PageWidth {
		t.Errorf("the longest line is %d cells: not wrapped at the page (%d)", longest, capture.PageWidth)
	}
	b.resize(200, 50)
	if wide := b.captureDialog().data; string(wide) != string(c.data) {
		t.Error("80 and 200 columns differ")
	}
	noEscapes(t, "changes", c.data)
}

func TestCaptureDeclaredAfterTheComparison(t *testing.T) {
	b, _, _ := declaredBench(t, benchOpts{width: 80, height: 24})
	home := b.exportHome()
	b.openDeclaredTab("shop")
	b.keys("W")
	if b.m.dlg != nil || len(filesIn(t, home)) != 0 {
		t.Fatal("a capture before the comparison")
	}
	b.keys("enter")
	b.until("the comparison", b.declaredDone)
	b.settle()
	c := b.captureDialog()
	if !strings.Contains(string(c.data), "# pane: Declared\n") {
		t.Errorf("%s", c.data)
	}
	for _, l := range captureBody(t, c.data) {
		if n := len([]rune(l)); n > capture.PageWidth {
			t.Errorf("a line of %d cells", n)
		}
	}
	b.keys("esc")
	b.resize(200, 50)
	if wide := b.captureDialog().data; string(wide) != string(c.data) {
		t.Error("80 and 200 columns differ")
	}
}

// ---- 3.7 Job output and the file reader ----------------------------------------------

func TestCaptureJobOutputIsWhatTheViewerShows(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed})
	b.exportHome()
	id := b.m.jobs.Start("a job", "target-x", func(ctx context.Context, r *jobs.Run) error {
		r.Logf("first line")
		r.Logf("second line")
		return nil
	})
	b.until("the job", func() bool { j, _ := b.m.jobs.Get(id); return j.State == jobs.Succeeded })
	b.m.focus = PaneJobs
	b.m.jobSel = id
	c := b.captureDialog()
	if got, want := b.exportPath(), "~/docker-desk-output-target-x-20261109.txt"; got != want {
		t.Errorf("prefilled %q, want %q", got, want)
	}
	j, _ := b.m.jobs.Get(id)
	var want []string
	for _, l := range jobOutput(j, b.m.st) {
		want = append(want, capture.Plain(l))
	}
	if got := captureBody(t, c.data); strings.Join(got, "\n") != strings.TrimRight(strings.Join(want, "\n"), "\n") {
		t.Errorf("job output:\n%q\nwant\n%q", got, want)
	}
}

func TestCaptureReaderIsThePreview(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.exportHome()
	b.webFiles(false)
	rd := b.openFile("/etc/nginx/nginx.conf")
	c := b.captureDialog()
	if got, want := b.exportPath(), "~/docker-desk-file-nginx.conf-20261109.txt"; got != want {
		t.Errorf("prefilled %q, want %q", got, want)
	}
	if got := strings.Join(captureBody(t, c.data), "\n"); got != strings.TrimRight(strings.Join(plainAll(rd.view.lines), "\n"), "\n") {
		t.Errorf("reader:\n%s", got)
	}
	b.keys("esc")
	if b.m.fs.rd == nil {
		t.Error("Esc in the dialog closed the reader too")
	}
}

func plainAll(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = capture.Plain(l)
	}
	return out
}

// ---- class and hostile text ------------------------------------------------------------

// TestCaptureOfHostileLogIsInert: a container that writes escape sequences
// and C1 controls cannot put one in the file.
func TestCaptureOfHostileLogIsInert(t *testing.T) {
	hostile := []string{
		"\x1b]52;c;ZXZpbA==\x07clipboard",
		"\x1b]0;title\x1b\\title",
		"\x1b[2J\x1b[Hclear",
		"c1 \u009b31m csi",
		"bell\x07 and \r return",
		"rlo \u202eevil\u2066 isolate",
		"osc c1 \x1b]0;t\u009c st",
	}
	b := newBench(t, benchOpts{seed: func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddContainer(fakeengine.ContainerSpec{Name: "talker", Image: "alpine:3.20", Logs: hostile})
	}})
	b.exportHome()
	b.toTab(KindContainers, "talker", tabLogs)
	b.waitLogs(len(hostile))
	_, data := b.saveCapture()
	noEscapes(t, "hostile logs", data)
	if strings.ContainsAny(string(data), "\u202e\u2066") {
		t.Error("a reordering character is raw in the file")
	}
	for _, w := range []string{"clipboard", "title", "clear", "c1 31m csi", `rlo \u202eevil\u2066 isolate`, "osc c1"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("the text %q was lost", w)
		}
	}
}

// ---- 4.1, 4.2 the key is listed ---------------------------------------------------------

// TestCaptureKeyIsInTheFooterAndTheHelp: the Details footer names W where
// the line has room, and the help lists it next to w.
func TestCaptureKeyIsInTheFooterAndTheHelp(t *testing.T) {
	b := newBench(t, benchOpts{seed: captureSeed, width: 200, height: 40})
	b.toTab(KindContainers, "talker", tabLogs)
	b.see("W save text")
	b.toTab(KindContainers, "talker", tabStats)
	b.notSee("W save text")
	b.m.focus = PaneEvents
	b.see("W save events")
	b.m.focus = PaneJobs
	b.see("W save output")

	help := ansi.Strip(strings.Join(b.m.helpLines(100), "\n"))
	i, j := strings.Index(help, "  w  "), strings.Index(help, "  W  ")
	if i < 0 || j < 0 || j < i || !strings.Contains(help[j:], "save this tab's text to a file") {
		t.Errorf("the help does not list W after w:\n%s", help)
	}
	for _, w := range []string{"save every event the pane holds", "save the highlighted job's output", "W saves the text"} {
		if !strings.Contains(help, w) {
			t.Errorf("the help does not say %q", w)
		}
	}
}

// TestDocumentedCaptureKey: the README's key table and its Capture section
// say what W saves, and the help says the same.
func TestDocumentedCaptureKey(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		"| `" + keyCapture + "` | save the focused pane's text to a file",
		"### Capture — a pane's text to a file",
		"`~/docker-desk-logs-shop-web-1-<YYYYMMDD>.txt`",
		"| Capture (`W`) | Report (`w`) |",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}
	b := newBench(t, benchOpts{})
	help := ansi.Strip(strings.Join(b.m.helpLines(100), "\n"))
	if !strings.Contains(help, keyCapture+" ") || !strings.Contains(help, "save this tab's text to a file") {
		t.Errorf("the help does not list %s", keyCapture)
	}
}
