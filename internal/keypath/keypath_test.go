package keypath

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// indented is what the Inspect tab shows: the daemon's JSON, indented by
// encoding/json.
func indented(t *testing.T, doc string) []string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(doc), "", "  "); err != nil {
		t.Fatal(err)
	}
	return strings.Split(b.String(), "\n")
}

// pathOf returns the path of the first line containing sub.
func pathOf(t *testing.T, lines []string, sub string) string {
	t.Helper()
	paths := Paths(lines, 2)
	for i, l := range lines {
		if strings.Contains(l, sub) {
			if at := At(lines, i, 2); at != paths[i] {
				t.Errorf("At and Paths disagree on %q: %q, %q", l, at, paths[i])
			}
			return paths[i]
		}
	}
	t.Fatalf("no line contains %q", sub)
	return ""
}

const container = `{
 "Id": "4f1c",
 "State": {"Status": "running", "Health": {"Status": "healthy", "Log": [{"ExitCode": 0, "Output": "ok"}, {"ExitCode": 1, "Output": "bad"}]}},
 "Config": {
   "Env": ["PATH=/usr/bin", "LANG=C"],
   "Health": {"Status": "starting", "Test": ["CMD", "curl"]},
   "Labels": {"com.docker.compose.project": "shop", "plain": "yes"},
   "ExposedPorts": {"80/tcp": {}},
   "Empty": [],
   "Nested": [[1, 2], [3]]
 },
 "Mounts": [{"Type": "bind", "Source": "/srv"}, {"Type": "volume", "Source": "data"}],
 "Weird \"key\"": "v"
}`

func TestPaths(t *testing.T) {
	lines := indented(t, container)
	cases := map[string]string{
		`"Id": "4f1c"`:            "Id",
		`"Status": "running"`:     "State.Status",
		`"Status": "healthy"`:     "State.Health.Status",
		`"Output": "bad"`:         "State.Health.Log[1].Output",
		`"ExitCode": 0`:           "State.Health.Log[0].ExitCode",
		`"LANG=C"`:                "Config.Env[1]",
		`"PATH=/usr/bin"`:         "Config.Env[0]",
		`"Status": "starting"`:    "Config.Health.Status",
		`"curl"`:                  "Config.Health.Test[1]",
		`compose.project": "shop`: `Config.Labels["com.docker.compose.project"]`,
		`"plain": "yes"`:          "Config.Labels.plain",
		`"80/tcp": {}`:            `Config.ExposedPorts["80/tcp"]`,
		`"Empty": []`:             "Config.Empty",
		`"Source": "data"`:        "Mounts[1].Source",
		`"Type": "bind"`:          "Mounts[0].Type",
		`"Weird \"key\""`:         `["Weird \"key\""]`,
		`"Health": {`:             "State.Health", // the first: State's
	}
	for sub, want := range cases {
		if got := pathOf(t, lines, sub); got != want {
			t.Errorf("%s: got %q, want %q", sub, got, want)
		}
	}
	// Arrays of arrays count their own elements.
	paths := Paths(lines, 2)
	var nested []string
	inNested := false
	for i, l := range lines {
		if strings.Contains(l, `"Nested"`) {
			inNested = true
			continue
		}
		if inNested && strings.TrimSpace(l) == "]" && paths[i] == "Config.Nested" {
			break
		}
		if inNested {
			nested = append(nested, paths[i])
		}
	}
	want := []string{"Config.Nested[0]", "Config.Nested[0][0]", "Config.Nested[0][1]", "Config.Nested[0]",
		"Config.Nested[1]", "Config.Nested[1][0]", "Config.Nested[1]"}
	if strings.Join(nested, " ") != strings.Join(want, " ") {
		t.Errorf("nested arrays:\n got %v\nwant %v", nested, want)
	}
}

// A closing line has the path of what it closes; the document's own braces
// have none.
func TestClosingLinesAndRoot(t *testing.T) {
	lines := indented(t, `{"A": {"B": 1}, "C": [1]}`)
	got := Paths(lines, 2)
	want := []string{"", "A", "A.B", "A", "C", "C[0]", "C", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Malformed or partial input gives a best-effort path, never a panic.
func TestTolerant(t *testing.T) {
	for _, lines := range [][]string{
		nil,
		{""},
		{"}", "]", "    }"},
		{`      "Deep": 1`},
		{`"Top": {`, `  "In": 1`},
		{`  "unterminated`, `  "x":`},
		{"\t{", "not json at all"},
	} {
		_ = Paths(lines, 2)
		_ = Paths(lines, 0)
		_ = At(lines, len(lines)-1, 2)
		_ = At(lines, 99, 2)
	}
	if got := Paths([]string{`"Top": {`, `  "In": 1`}, 2); got[1] != "Top.In" {
		t.Errorf("fragment: %q", got)
	}
}
