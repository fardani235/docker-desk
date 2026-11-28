package forensics

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// stepsImage is three layers under a five-entry history: two entries made
// no layer.
func stepsImage() fixture.Image {
	file := func(name string) []fixture.File {
		return []fixture.File{{Path: name, Body: []byte(name)}}
	}
	return fixture.Image{
		OS: "linux", Architecture: "amd64",
		Layers: []fixture.Layer{
			{CreatedBy: "base", Files: file("etc/base")},
			{CreatedBy: "build", Files: file("usr/bin/tool")},
			{CreatedBy: "copy", Files: file("app/main")},
		},
		History: []fixture.History{
			{CreatedBy: "/bin/sh -c #(nop) ADD file:9a3c in / "},
			{CreatedBy: "/bin/sh -c #(nop)  ENV APP_HOME=/app", EmptyLayer: true},
			{CreatedBy: "RUN |1 TOKEN=s3cr3t-value-0123456789 /bin/sh -c make build # buildkit"},
			{CreatedBy: "COPY app /app # buildkit"},
			{CreatedBy: `/bin/sh -c #(nop)  CMD ["/app/main"]`, EmptyLayer: true},
		},
	}
}

// Tasks 1.1: every entry of the config's history is a step, in order, with
// its empty_layer flag and the layer it made.
func TestStepsKeepEveryHistoryEntry(t *testing.T) {
	for name, build := range map[string]func(fixture.Image) fixture.Built{"legacy": fixture.Legacy, "oci": fixture.OCI} {
		rep := analyse(t, build(stepsImage()), Options{})
		if len(rep.Steps) != 5 {
			t.Fatalf("%s: %d steps, want 5: %+v", name, len(rep.Steps), rep.Steps)
		}
		wantEmpty := []bool{false, true, false, false, true}
		wantLayer := []int{1, 0, 2, 3, 0}
		wantStart := []string{"ADD file:9a3c in /", "ENV APP_HOME=/app", "RUN make build", "COPY app /app", "CMD ["}
		for i, s := range rep.Steps {
			if s.Empty != wantEmpty[i] || s.Layer != wantLayer[i] {
				t.Errorf("%s: step %d = %+v, want empty %v layer %d", name, i, s, wantEmpty[i], wantLayer[i])
			}
			if !strings.HasPrefix(s.Instruction, wantStart[i]) {
				t.Errorf("%s: step %d instruction %q, want prefix %q", name, i, s.Instruction, wantStart[i])
			}
		}
	}
}

// A step is the masked form, the only form: the value of a sealed build
// argument never reaches it.
func TestStepsHoldNoBuildArgumentValue(t *testing.T) {
	rep := analyse(t, fixture.Legacy(stepsImage()), Options{})
	for _, s := range rep.Steps {
		if strings.Contains(s.Instruction, "s3cr3t") {
			t.Fatalf("a step carries a build argument's value: %q", s.Instruction)
		}
	}
	if got := rep.Steps[2].Instruction; !strings.Contains(got, "TOKEN") || !strings.Contains(got, "make build") {
		t.Errorf("the step should name the argument and the command: %q", got)
	}
}

// A history whose count does not bind gives layer 0 for every step: the
// flags are still the config's, the binding is never a guess.
func TestStepsDoNotBindWhenTheCountDisagrees(t *testing.T) {
	img := stepsImage()
	img.History = img.History[:3] // two layer-making entries for three layers
	rep := analyse(t, fixture.Legacy(img), Options{})
	if len(rep.Steps) != 3 {
		t.Fatalf("%d steps, want 3", len(rep.Steps))
	}
	for i, s := range rep.Steps {
		if s.Layer != 0 {
			t.Errorf("step %d is bound to layer %d although the history does not map", i, s.Layer)
		}
	}
	if !rep.Steps[1].Empty || rep.Steps[0].Empty {
		t.Errorf("the empty_layer flags were lost: %+v", rep.Steps)
	}
}

// Task 1.2: Instructions and Steps are one fact.
func TestStepsAndInstructionsAgree(t *testing.T) {
	for _, img := range []fixture.Image{stepsImage(), fixture.Demo()} {
		rep := analyse(t, fixture.Legacy(img), Options{})
		bound := 0
		for i, s := range rep.Steps {
			if s.Empty {
				if s.Layer != 0 {
					t.Errorf("step %d made no layer and names layer %d", i, s.Layer)
				}
				continue
			}
			bound++
			if got := rep.Instruction(s.Layer); got != s.Instruction {
				t.Errorf("step %d: Instruction(%d) = %q, the step says %q", i, s.Layer, got, s.Instruction)
			}
			if rep.Layers[s.Layer-1].Instruction != s.Instruction {
				t.Errorf("step %d: the layer's instruction differs from the step's", i)
			}
		}
		if bound != len(rep.Layers) {
			t.Errorf("%d bound steps for %d layers", bound, len(rep.Layers))
		}
	}
}
