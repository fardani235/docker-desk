package fakeengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func getTop(t *testing.T, s *Server, ref string) (int, container.TopResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.47/containers/"+ref+"/top", nil))
	var out container.TopResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out, rec.Body.String()
}

func TestTopHTTP(t *testing.T) {
	s := New()
	s.AddContainer(ContainerSpec{Name: "run", Image: "a:1", Cmd: []string{"nginx", "-g", "daemon off;"}})
	s.AddContainer(ContainerSpec{Name: "empty", Image: "a:1"})
	s.AddContainer(ContainerSpec{Name: "odd", Image: "a:1"})
	s.AddContainer(ContainerSpec{Name: "off", Image: "a:1", State: "exited"})
	s.SetProcesses("empty", &ProcessTable{Titles: []string{"PID", "CMD"}, Rows: [][]string{}})
	s.SetProcesses("odd", &ProcessTable{Titles: []string{"USER", "PID", "%CPU"}, Rows: [][]string{{"app", "7"}, {"app", "8", "0.1", "extra"}}})

	code, top, _ := getTop(t, s, "run")
	if code != 200 || len(top.Titles) != 8 || top.Titles[7] != "CMD" || len(top.Processes) != 2 || top.Processes[0][7] != "nginx -g daemon off;" {
		t.Fatalf("running: %d %+v", code, top)
	}
	// Deterministic: the same answer twice.
	if _, again, _ := getTop(t, s, "run"); again.Processes[0][1] != top.Processes[0][1] {
		t.Fatal("default listing is not deterministic")
	}
	if code, top, _ := getTop(t, s, "empty"); code != 200 || len(top.Titles) != 2 || len(top.Processes) != 0 {
		t.Fatalf("empty: %d %+v", code, top)
	}
	if code, top, _ := getTop(t, s, "odd"); code != 200 || top.Titles[2] != "%CPU" || len(top.Processes[0]) != 2 || len(top.Processes[1]) != 4 {
		t.Fatalf("variable columns: %d %+v", code, top)
	}
	if code, _, body := getTop(t, s, "off"); code != http.StatusConflict || !contains(body, "is not running") {
		t.Fatalf("stopped: %d %s", code, body)
	}
	if code, _, _ := getTop(t, s, "ghost"); code != http.StatusNotFound {
		t.Fatalf("missing: %d", code)
	}
	s.FailNext(http.MethodGet, `/containers/.*/top`, 500, "ps failed", 1)
	if code, _, body := getTop(t, s, "run"); code != 500 || !contains(body, "ps failed") {
		t.Fatalf("injected failure: %d %s", code, body)
	}
	if n := s.CountRequests(http.MethodGet, `^/containers/[^/]+/top$`); n != 7 {
		t.Fatalf("observed %d top requests, want 7", n)
	}
	s.SetProcesses("odd", nil)
	if _, top, _ := getTop(t, s, "odd"); top.Titles[0] != "UID" {
		t.Fatalf("reset to default: %+v", top)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
