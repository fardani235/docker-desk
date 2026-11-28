package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/report"
)

// The report keeps every history step for the desk's Layers tab
// (forensics.Report.Steps); the exported image report does not gain it. A
// step that made no layer is in no layer's instruction, so its text is the
// probe: it is in the report the analysis made and in no exported format.
func TestExportedImageReportHasNoStepList(t *testing.T) {
	const probe = "STEP_ONLY_MARKER_qz81"
	img := fixture.Image{
		OS: "linux", Architecture: "amd64",
		Layers: []fixture.Layer{{CreatedBy: "base", Files: []fixture.File{{Path: "etc/base", Body: []byte("x")}}}},
		History: []fixture.History{
			{CreatedBy: "/bin/sh -c #(nop) ADD file:9a3c in / "},
			{CreatedBy: "/bin/sh -c #(nop)  LABEL note=" + probe, EmptyLayer: true},
		},
	}
	b := fixture.Legacy(img)
	rep, err := forensics.Analyse(context.Background(), bytes.NewReader(b.Archive), forensics.Options{ImageID: b.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Steps) != 2 || !strings.Contains(rep.Steps[1].Instruction, probe) {
		t.Fatalf("the analysis should keep the config-only step: %+v", rep.Steps)
	}
	doc := Image("test", "probe:1", rep, nil)
	for _, f := range report.Formats {
		out, err := report.Render(f, report.Document{Image: &doc})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if bytes.Contains(out, []byte(probe)) {
			t.Errorf("%s: the exported report carries a step that made no layer", f)
		}
	}
	js, err := report.ImageJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(js, &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		if strings.Contains(strings.ToLower(k), "step") {
			t.Errorf("the JSON document gained a %q field", k)
		}
	}
}
