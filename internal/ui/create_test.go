package ui

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/jobs"
)

// openCreate brings the desk to the create form.
func (b *bench) openCreate() *dialog {
	b.t.Helper()
	b.m.focus = PaneList
	b.kind(KindContainers)
	b.keys("n")
	if b.m.dlg == nil || b.m.dlg.kind != dlgForm || b.m.dlg.form != formCreate {
		b.t.Fatalf("n did not open the create form:\n%s", b.screen())
	}
	return b.m.dlg
}

// toField tabs to a field of the create form.
func (b *bench) toField(i int) {
	b.t.Helper()
	for n := 0; b.m.dlg.focus != i; n++ {
		if n > len(b.m.dlg.fields) {
			b.t.Fatalf("field %d cannot be reached with Tab", i)
		}
		b.keys("tab")
	}
}

func TestCreateFormOpensSendingNothing(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.srv.ResetRequests()
	d := b.openCreate()
	b.noMutations("opening the create form")
	if len(d.fields) != 7 || d.focus != createImage {
		t.Fatalf("fields %d, focus %d", len(d.fields), d.focus)
	}
	b.see("Create container", "Image", "Name", "Ports", "Environment", "Mounts", "Restart", "Command")
	b.keys("esc")
	if b.m.dlg != nil || len(b.m.jobs.List()) != 0 {
		t.Fatal("Esc must close the form and record nothing")
	}
	b.noMutations("Esc in the create form")

	// With a selection the form opens and the selection is unaffected.
	b.mark("redis-cache", "shop-web-1")
	b.keys("n")
	if b.m.dlg == nil || b.m.dlg.form != formCreate {
		t.Fatalf("a selection must not stop the create form:\n%s", b.screen())
	}
	if got := b.markedNames(); len(got) != 2 {
		t.Fatalf("selection changed: %v", got)
	}
	b.keys("esc")
	// It is also in the action menu, with a selection.
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgMenu || !strings.Contains(b.screen(), "new…") {
		t.Fatalf("the menu does not offer new…:\n%s", b.screen())
	}
	b.noMutations("the menu")
}

func TestCreateFormReadOnlyRefuses(t *testing.T) {
	b := newBench(t, benchOpts{readOnly: true})
	b.m.focus = PaneList
	b.kind(KindContainers)
	b.srv.ResetRequests()
	b.keys("n")
	b.settle()
	if b.m.dlg != nil {
		t.Fatal("a form opened in read-only mode")
	}
	b.see(`Read-only mode: "new…" is disabled. Nothing was sent to the daemon.`)
	if n := len(b.srv.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestFormTabWrapsAndShiftTabGoesBack(t *testing.T) {
	b := newBench(t, benchOpts{})
	d := b.openCreate()
	for i := 1; i <= 7; i++ {
		b.keys("tab")
		if d.focus != i%7 {
			t.Fatalf("after %d Tab focus is %d", i, d.focus)
		}
	}
	b.keys("shift+tab")
	if d.focus != createCommand {
		t.Fatalf("Shift+Tab from the first field: %d", d.focus)
	}
	// Typing goes to the focused field only.
	b.typeText("echo hi")
	if d.fields[createCommand].value() != "echo hi" || d.fields[createImage].value() != "" {
		t.Fatalf("typed into the wrong field: %q / %q", d.fields[createCommand].value(), d.fields[createImage].value())
	}

	// The single-field forms are the same model: Tab stays put, Esc closes
	// without a job.
	b.keys("esc")
	b.kind(KindImages)
	b.keys("P")
	if b.m.dlg == nil || b.m.dlg.form != formPull || len(b.m.dlg.fields) != 1 {
		t.Fatal("pull form")
	}
	b.typeText("alpine")
	b.keys("tab", "shift+tab", "up", "down", "ctrl+n", "ctrl+x")
	if got := b.m.dlg.formText(); got != "alpine" {
		t.Fatalf("form keys changed a single-field form's text: %q", got)
	}
	b.keys("esc")
	if b.m.dlg != nil || len(b.m.jobs.List()) != 0 {
		t.Fatal("Esc in the pull form")
	}
	b.noMutations("moving around forms")
}

func TestFormRepeatableRows(t *testing.T) {
	b := newBench(t, benchOpts{})
	d := b.openCreate()
	b.toField(createPorts)
	ports := &d.fields[createPorts]
	b.typeText("8080:80")
	b.keys("ctrl+n")
	b.typeText("53:53/udp")
	b.keys("ctrl+n")
	b.typeText("9000")
	if got := strings.Join(ports.values(), ","); got != "8080:80,53:53/udp,9000" {
		t.Fatalf("Ctrl+N twice: %q", got)
	}
	b.keys("ctrl+x")
	if got := strings.Join(ports.values(), ","); got != "8080:80,53:53/udp" || ports.row != 1 {
		t.Fatalf("Ctrl+X removed the focused entry: %q, focus row %d", got, ports.row)
	}
	b.keys("up")
	if ports.row != 0 {
		t.Fatalf("↑: row %d", ports.row)
	}
	b.keys("ctrl+x")
	if got := strings.Join(ports.values(), ","); got != "53:53/udp" {
		t.Fatalf("%q", got)
	}
	// The last entry is cleared, not removed.
	b.keys("ctrl+x")
	if len(ports.rows) != 1 || ports.rows[0].Value() != "" {
		t.Fatalf("the last entry: %d rows, %q", len(ports.rows), ports.rows[0].Value())
	}
	// Ctrl+N / Ctrl+X never reach a text input: not in a list, and not in a
	// text field.
	b.toField(createName)
	b.typeText("ab")
	b.keys("ctrl+n", "ctrl+x")
	if got := d.fields[createName].value(); got != "ab" || len(d.fields[createName].rows) != 1 {
		t.Fatalf("name: %q", got)
	}
	b.noMutations("editing entries")
}

// fillCreate types a complete configuration.
func (b *bench) fillCreate(image, name string) {
	b.t.Helper()
	b.toField(createImage)
	b.typeText(image)
	b.toField(createName)
	b.typeText(name)
	b.toField(createPorts)
	b.typeText("127.0.0.1:18080:80")
	b.keys("ctrl+n")
	b.typeText("53:53/udp")
	b.toField(createEnv)
	b.typeText("TOKEN=s3cret")
	b.keys("ctrl+n")
	b.typeText("LOG_LEVEL=info")
	b.toField(createMounts)
	b.typeText("/srv/data:/data:ro")
	b.keys("ctrl+n")
	b.typeText("cache:/var/cache")
	b.toField(createRestart)
	b.keys("right", "right", "right", "down")
	b.typeText("3")
	b.toField(createCommand)
	b.typeText(`sh -c 'sleep 1000'`)
}

func TestCreateSummaryThenSubmit(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 70})
	b.openCreate()
	b.srv.ResetRequests()
	b.fillCreate("alpine", "made-here")
	b.see(
		"daemon", b.srv.Host(),
		"Will create and start, on this daemon:",
		"alpine:latest", "it will be pulled first",
		"made-here",
		"127.0.0.1:18080 → 80/tcp", "53 → 53/udp",
		"TOKEN=s3cret", "LOG_LEVEL=info",
		"/srv/data → /data (bind, ro)", "cache → /var/cache (volume)",
		"on-failure, at most 3 retries",
		`sh -c "sleep 1000"`,
	)
	b.noMutations("filling the form")

	b.keys("enter")
	b.finishJobs()
	if b.m.dlg != nil {
		t.Fatalf("the form stayed open:\n%s", b.screen())
	}
	list := b.m.jobs.List()
	if len(list) != 1 {
		t.Fatalf("%d jobs", len(list))
	}
	j, _ := b.m.jobs.Get(list[0].ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v\n%s", j, strings.Join(j.Log, "\n"))
	}
	if st, _ := b.srv.ContainerState("made-here"); st != "running" {
		t.Fatalf("state %q", st)
	}
	if b.srv.CountRequests("POST", `^/images/create`) != 1 {
		t.Error("the missing image was not pulled exactly once")
	}
	// The environment value is in the request, and nowhere else.
	reqs := b.srv.CreateRequests()
	if len(reqs) != 1 || strings.Join(reqs[0].Body.Env, ",") != "TOKEN=s3cret,LOG_LEVEL=info" {
		t.Fatalf("request: %+v", reqs)
	}
	record := j.Title + "\n" + j.Target + "\n" + j.Status + "\n" + j.Err + "\n" + strings.Join(j.Log, "\n") + "\n" + b.m.status.text
	if strings.Contains(record, "s3cret") {
		t.Fatalf("an environment value leaked into the job or status:\n%s", record)
	}
	if !strings.Contains(record, "made-here") || !strings.Contains(record, "alpine:latest") {
		t.Errorf("the job names the container and its image:\n%s", record)
	}
	// The new container appears in the list without operator action.
	b.until("the new row", func() bool { return strings.Contains(b.screen(), "made-here") })
	b.notSee("s3cret")
}

func TestCreateWithPresentImageSaysSo(t *testing.T) {
	b := newBench(t, benchOpts{width: 120, height: 60})
	b.openCreate()
	b.typeText("nginx:1.27")
	b.see("nginx:1.27", "present on this daemon", "none given: the daemon assigns one", "the image's own")
	b.srv.ResetRequests()
	b.keys("enter")
	b.finishJobs()
	if b.srv.CountRequests("POST", `^/images/create`) != 0 {
		t.Fatal("a present image was pulled")
	}
	if b.srv.CountRequests("POST", `^/containers/create$`) != 1 || b.srv.CountRequests("POST", `/start$`) != 1 {
		t.Fatalf("requests: %v", b.srv.Mutations())
	}
}

func TestCreateRefusedValuesKeepTheFormOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field int
		entry int
		fill  func(b *bench)
		says  string
	}{
		{"bad port", createPorts, 1, func(b *bench) {
			b.toField(createPorts)
			b.typeText("8080:80")
			b.keys("ctrl+n")
			b.typeText("70000:80")
		}, "the port must be between 1 and 65535"},
		{"taken name", createName, 0, func(b *bench) {
			b.toField(createName)
			b.typeText("shop-web-1")
		}, `the name "shop-web-1" is taken`},
		{"malformed env", createEnv, 0, func(b *bench) {
			b.toField(createEnv)
			b.typeText("=value")
		}, "an environment entry needs a name"},
		{"relative destination", createMounts, 0, func(b *bench) {
			b.toField(createMounts)
			b.typeText("/srv/data:data")
		}, "the destination must be an absolute path"},
		{"command", createCommand, 0, func(b *bench) {
			b.toField(createCommand)
			b.typeText("sh -c 'oops")
		}, "unterminated quote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, benchOpts{width: 120, height: 60})
			d := b.openCreate()
			b.typeText("nginx:1.27")
			tc.fill(b)
			b.toField(createImage) // the error must bring focus back
			b.srv.ResetRequests()
			b.keys("enter")
			b.settle()
			b.noMutations(tc.name)
			if b.m.dlg != d || d.formBusy {
				t.Fatalf("the form must stay open:\n%s", b.screen())
			}
			if !strings.Contains(d.formErr, tc.says) {
				t.Fatalf("error %q, want %q", d.formErr, tc.says)
			}
			b.see(tc.says)
			if d.focus != tc.field || d.fields[d.focus].row != tc.entry {
				t.Fatalf("focus %d row %d, want field %d row %d", d.focus, d.fields[d.focus].row, tc.field, tc.entry)
			}
			if len(b.m.jobs.List()) != 0 {
				t.Fatal("a job was started")
			}
		})
	}
}

func TestCreateEnterTypedAheadSendsNothing(t *testing.T) {
	b := newBench(t, benchOpts{arm: time.Second})
	b.m.focus = PaneList
	b.kind(KindContainers)
	b.srv.ResetRequests()
	// Opening and Enter in quick succession, with a valid value typed.
	b.keys("n")
	b.typeText("nginx:1.27")
	b.keys("enter")
	b.settle()
	b.noMutations("Enter before the form could be read")
	if b.m.dlg == nil || b.m.dlg.form != formCreate || b.m.dlg.formBusy {
		t.Fatal("the form must stay open, unsubmitted")
	}
	// Once it has been on screen long enough, Enter submits.
	b.m.dlg.openedAt = time.Now().Add(-2 * time.Second)
	b.keys("enter")
	b.finishJobs()
	if b.srv.CountRequests("POST", `^/containers/create$`) != 1 {
		t.Fatalf("after arming: %v", b.srv.Mutations())
	}
}

// At 80×24, with every field filled and three entries in each list, the
// focused field, the syntax hint and the keys are always on screen.
func TestCreateFormFitsSmallTerminal(t *testing.T) {
	b := newBench(t, benchOpts{width: 80, height: 24})
	d := b.openCreate()
	b.fillCreate("alpine", "small")
	for _, f := range []int{createPorts, createEnv, createMounts} {
		b.toField(f)
		b.keys("ctrl+n")
		b.typeText(map[int]string{createPorts: "9000", createEnv: "X=1", createMounts: "v2:/v2"}[f])
	}
	for i := 0; i < 2*len(d.fields); i++ {
		b.keys("tab")
		f := &d.fields[d.focus]
		scr := b.screen()
		if !strings.Contains(scr, "field "+strconv.Itoa(d.focus+1)+" of 7") {
			t.Fatalf("focus %d: no position line:\n%s", d.focus, scr)
		}
		if !strings.Contains(scr, "Esc cancel") {
			t.Fatalf("focus %d: the keys are cut off:\n%s", d.focus, scr)
		}
		if hint := f.hint; !strings.Contains(scr, strings.Fields(hint)[0]) {
			t.Fatalf("focus %d: no hint line:\n%s", d.focus, scr)
		}
		if !strings.Contains(scr, "▸ "+f.label) {
			t.Fatalf("focus %d: the focused field %q is not drawn:\n%s", d.focus, f.label, scr)
		}
		b.assertFrameGeometry()
	}
	// The last row of a long list is reachable too.
	b.toField(createEnv)
	b.keys("down", "down")
	if !strings.Contains(b.screen(), "X=1") {
		t.Fatalf("the focused entry is out of view:\n%s", b.screen())
	}
}

// The README's words for n are the help's, and the help lists the form's keys.
func TestCreateIsInTheHelp(t *testing.T) {
	b := newBench(t, benchOpts{})
	help := strings.Join(b.m.helpLines(200), "\n")
	for _, want := range []string{
		"create a container from an image, and start it (pulls the image first if it is missing)",
		"Create container form (n on Containers)", "Ctrl+N  Ctrl+X", "Tab / Shift+Tab",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "`n` new (create and start, pulling the image first if it is missing)") {
		t.Error("README's key table does not document n on Containers")
	}
}
