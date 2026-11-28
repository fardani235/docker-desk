package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// captureLog is what "talker" has written when the desk opens: colour, an
// OSC that would set the terminal's title, and padding.
var captureLog = []string{
	"\x1b[32mINFO\x1b[0m boot complete      ",
	"\x1b]0;owned\x07\x1b[31mERROR\x1b[0m disk quota exceeded",
	"plain line three",
}

// TestCaptureFromTheSandbox: in read-only mode, W on a container's Logs and
// on its Inspect tab, each through the dialog's consent, into a HOME of the
// test's own. The log file's bytes are asserted whole; the inspect file
// parses and names the container; neither capture sent the daemon a request.
func TestCaptureFromTheSandbox(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddContainer(fakeengine.ContainerSpec{Name: "talker", Image: "alpine:3.20", Logs: captureLog})
	})
	home := t.TempDir()
	term, err := ptyscreen.Start(40, 150, []string{"DOCKER_HOST=" + srv.Host(), "HOME=" + home}, binary, "--read-only")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	d := &desk{t: t, srv: srv, term: term}
	d.see("Docker Desk", "READ-ONLY", "talker")

	d.keys("2", "/", "t", "a", "l", "k", "e", "r", ptyscreen.Enter)
	d.see("3 Details: talker")
	d.keys("3", "]", "]")
	d.see("[Logs]", "plain line three")
	d.see("W save text")

	quiet := func(what string, act func()) {
		t.Helper()
		time.Sleep(300 * time.Millisecond) // let the desk's own reads of the tab settle
		before := len(srv.Requests())
		act()
		if after := srv.Requests(); len(after) != before {
			t.Errorf("%s sent %d request(s): %v", what, len(after)-before, after[before:])
		}
	}

	quiet("the logs capture", func() {
		d.keys("W")
		d.see("Save this Logs text", "3 held log lines", "It is not a report.", "docker-desk-logs-talker-")
		d.shot("91-capture-logs")
		d.keys(ptyscreen.Enter)
		d.see("Write this file?", "0600: only you can read it")
		d.consent("y")
		d.see("Saved text to")
	})
	logs := one(t, filepath.Join(home, "docker-desk-logs-talker-*.txt"))
	data, _ := os.ReadFile(logs)
	_, body, ok := strings.Cut(string(data), "\n#\n")
	if !ok {
		t.Fatalf("no header:\n%s", data)
	}
	if want := "INFO boot complete\nERROR disk quota exceeded\nplain line three\n"; body != want {
		t.Errorf("logs body:\n%q\nwant\n%q", body, want)
	}
	for _, w := range []string{"# pane: Logs\n", "# object: talker\n", "# lines: 3 held, oldest first — every held line is below\n", "not a report"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("logs header lacks %q:\n%s", w, data)
		}
	}
	if strings.ContainsAny(string(data), "\x1b\x07") || strings.Contains(string(data), "owned") {
		t.Errorf("an escape sequence reached the file:\n%q", data)
	}
	if fi, _ := os.Stat(logs); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	d.keys("]", "]", "]")
	d.see("[Inspect]", `"Id":`)
	quiet("the inspect capture", func() {
		d.keys("W")
		d.see("Save the inspect document", "environment variable", "docker-desk-inspect-talker-")
		d.keys(ptyscreen.Enter)
		d.see("Write this file?")
		d.shot("92-capture-inspect-confirm")
		d.consent("y")
		d.see("Saved json to")
	})
	var doc struct{ ID, Name string }
	readJSON(t, one(t, filepath.Join(home, "docker-desk-inspect-talker-*.json")), &doc)
	if doc.ID != srv.ContainerID("talker") || doc.Name != "/talker" {
		t.Errorf("the inspect file describes %s %s", doc.ID, doc.Name)
	}
	raw, _ := os.ReadFile(one(t, filepath.Join(home, "docker-desk-inspect-talker-*.json")))
	if raw[0] != '{' || !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("not the document alone: %.20q … %q", raw, raw[len(raw)-4:])
	}
	if muts := srv.Mutations(); len(muts) != 0 {
		t.Errorf("mutations: %v", muts)
	}
	if es, _ := os.ReadDir(home); len(es) != 2 {
		t.Errorf("%d files in HOME, want the two captures", len(es))
	}
}
