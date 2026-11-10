package composefile

import (
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func mustRead(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseGitlabShapedFile(t *testing.T) {
	f, err := Parse(mustRead(t, "gitlab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := File{Name: "gitlab", Services: map[string]Service{
		"gitlab": {Image: "gitlab/gitlab-ce:${GITLAB_VERSION:-19.5.1-ce.0}"},
		"runner": {Image: "gitlab/gitlab-runner:v18.5.0", Profiles: []string{"ci"}},
	}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got %+v\nwant %+v", f, want)
	}
}

func TestParseKeepsOnlyWhatItNeeds(t *testing.T) {
	f, err := Parse([]byte(`
services:
  web:
    image: nginx:1.27
    build: .
    environment: { SECRET: hunter2 }
  api:
    extends: { file: base.yaml, service: api }
  num:
    image: 1234
include:
  - other.yaml
`))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Includes {
		t.Error("include: not noticed")
	}
	if s := f.Services["web"]; s.Image != "nginx:1.27" || !s.Build || s.Extends {
		t.Errorf("web: %+v", s)
	}
	if s := f.Services["api"]; s.Image != "" || !s.Extends {
		t.Errorf("api: %+v", s)
	}
	if s := f.Services["num"]; s.Image != "1234" {
		t.Errorf("a scalar image is kept as text: %+v", s)
	}
}

// What Parse refuses, it refuses whole: no partial File comes back.
func TestParseRefusals(t *testing.T) {
	cases := map[string]string{
		"malformed":       "services:\n  web:\n    image: nginx\n   bad: [\n",
		"two documents":   "services: {}\n---\nservices: {}\n",
		"reset tag":       "services:\n  web:\n    image: nginx\n    ports: !reset []\n",
		"override tag":    "services:\n  web: !override\n    image: nginx\n",
		"explicit str":    "services:\n  web:\n    image: !!str nginx\n",
		"image is a list": "services:\n  web:\n    image: [a, b]\n",
		"services list":   "services:\n  - web\n",
		"top level list":  "- services\n",
		"empty":           "",
		"only a comment":  "# nothing\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(doc))
			if err == nil {
				t.Fatalf("accepted: %+v", f)
			}
			if !errors.Is(err, ErrUnparseable) {
				t.Fatalf("error does not wrap ErrUnparseable: %v", err)
			}
			if !reflect.DeepEqual(f, File{}) {
				t.Fatalf("a partial File came back with the error: %+v", f)
			}
		})
	}
}

// allowedErr is the only shape a Parse error may take: the desk's words and
// at most a line number — never a byte of the document.
var allowedErr = regexp.MustCompile(`^not a compose file the desk can read: [a-zA-Z ,().!'-]+( \(line \d+\))?$`)

func TestParseErrorsCarryNoContent(t *testing.T) {
	for _, doc := range []string{
		"services:\n  web:\n    image: { secret: hunter2-value }\n",
		"services:\n  web:\n    profiles: hunter2-value\n",
		"name: [hunter2-value]\n",
		"services: hunter2-value\n",
		"hunter2-value: [\n",
		"services:\n  web:\n    image: !!binary aHVudGVyMi12YWx1ZQ==\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil {
			t.Fatalf("accepted %q", doc)
		}
		if strings.Contains(err.Error(), "hunter2") || !allowedErr.MatchString(err.Error()) {
			t.Errorf("error leaks or has an unexpected shape: %q", err)
		}
	}
}

func TestParseExcessiveAliasingIsRefused(t *testing.T) {
	doc := `a: &a ["x","x","x","x","x","x","x","x","x"]
b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
h: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]
services:
  web: { image: nginx, profiles: *h }
`
	start := time.Now()
	f, err := Parse([]byte(doc))
	if err == nil {
		t.Fatalf("an alias bomb was accepted: %d profiles", len(f.Services["web"].Profiles))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// ---- the net ------------------------------------------------------------------

// Parse turns a fault inside the decoder into a refusal. parse, which the
// fuzz and mutation tests call, does not: with the hook planted, the same
// input panics there. That is what makes those tests fail on a fault rather
// than have it caught — and what would happen to Parse without its net.
func TestParseNetCatchesWhatParseDoesNot(t *testing.T) {
	parseHook = func() { panic("planted fault") }
	defer func() { parseHook = nil }()

	if _, err := Parse([]byte("services: {}\n")); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("Parse with a fault inside: %v, want a refusal", err)
	}
	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		_, _ = parse([]byte("services: {}\n"))
		return false
	}()
	if !panicked {
		t.Fatal("parse caught the fault itself: the fuzz test would not see one")
	}
}

// mutate yields systematic damage to a document: every truncation, and each
// structural byte replaced by another.
func mutate(doc []byte, yield func([]byte)) {
	for i := 0; i <= len(doc); i++ {
		yield(doc[:i])
	}
	swaps := []byte{':', '-', '[', ']', '{', '}', '&', '*', '!', '|', '>', '"', '\'', '\n', ' ', '\t', '#', '<'}
	for i := 0; i < len(doc); i += mutationStride {
		for _, s := range swaps {
			if doc[i] == s {
				continue
			}
			m := append([]byte{}, doc...)
			m[i] = s
			yield(m)
		}
	}
}

// TestParseMutations calls parse — without the net — on thousands of damaged
// documents: a panic fails the test, and every refusal keeps its shape.
func TestParseMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("mutation sweep")
	}
	seeds := [][]byte{mustRead(t, "gitlab.yaml"), []byte("name: x\nservices:\n  a: { image: \"r:1\", profiles: [p] }\n  b:\n    <<: *x\n")}
	n := 0
	for _, seed := range seeds {
		mutate(seed, func(b []byte) {
			n++
			_, err := parse(b)
			if err != nil && !allowedErr.MatchString(err.Error()) {
				t.Fatalf("refusal with an unexpected shape: %q", err)
			}
		})
	}
	t.Logf("%d mutations parsed without a fault", n)
}

func FuzzParse(f *testing.F) {
	f.Add(mustRead(f, "gitlab.yaml"))
	f.Add([]byte("services:\n  web: { image: nginx }\n"))
	f.Add([]byte("a: &a [x]\nservices: { s: { image: *a } }\n"))
	f.Add([]byte("services:\n  web: !reset\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxSize {
			return
		}
		got, err := parse(b) // no net: a panic is a failure
		if err != nil {
			if !allowedErr.MatchString(err.Error()) {
				t.Fatalf("refusal with an unexpected shape: %q", err)
			}
			if !reflect.DeepEqual(got, File{}) {
				t.Fatalf("partial File with an error: %+v", got)
			}
		}
	})
}
