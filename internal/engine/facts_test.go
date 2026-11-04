package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

func TestImageFactsKeepsNoEnvValue(t *testing.T) {
	var oldID, newID string
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		oldID, newID = s.SeedMovedTag(fakeengine.ContainerSpec{Name: "job", Image: "acme/report:1.4", State: "exited", Cmd: []string{"x"}},
			fixture.ChangesReport("acme/report:1.4", false), fixture.ChangesReport("acme/report:1.4", true), nil, nil, 0)
	})
	ctx := ctxT(t)
	srv.ResetRequests()
	f, err := eng.ImageFacts(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range srv.Requests() {
		if r.Path == "/_ping" {
			continue // the client's version negotiation, once
		}
		n++
		if r.Method != "GET" {
			t.Errorf("non-GET request: %+v", r)
		}
	}
	if n != 2 {
		t.Errorf("ImageFacts sent %d requests, want 2 (inspect, history)", n)
	}
	if f.ID != "sha256:"+newID || f.User != "report" || len(f.DiffIDs) != 5 || len(f.History) < 5 ||
		strings.Join(f.ExposedPorts, ",") != "8000/tcp" || len(f.Healthcheck) == 0 || f.Labels["org.opencontainers.image.version"] != "1.4.2" {
		t.Fatalf("facts: %+v", f)
	}
	if got := strings.Join(f.EnvNames, ","); got != "FONT_DIR,PATH,REPORT_API_KEY" {
		t.Errorf("env names: %s", got)
	}
	old, err := eng.ImageFacts(ctx, oldID)
	if err != nil || old.DiffIDs[2] != f.DiffIDs[2] {
		t.Fatalf("old: %+v %v", old, err)
	}
	dump := fmt.Sprintf("%+v %#v %+v %#v", f, f, old, old)
	for _, v := range fixture.ChangesEnvValues {
		if strings.Contains(dump, v) {
			t.Errorf("an environment value (%s…) survived decoding", v[:4])
		}
	}
	if _, err := eng.ImageFacts(ctx, "nosuch"); !IsNotFound(err) {
		t.Errorf("missing image: %v", err)
	}
}
