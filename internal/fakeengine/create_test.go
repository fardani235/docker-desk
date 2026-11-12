package fakeengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func post(s *Server, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1.47"+path, strings.NewReader(body)))
	return rec
}

func TestContainerCreateHTTP(t *testing.T) {
	s := New()
	s.AddImage(7_000_000, time.Hour, "alpine:3.20")
	s.AddContainer(ContainerSpec{Name: "taken", Image: "alpine:3.20"})

	if rec := post(s, "/containers/create?name=x1", `{"Image":"nope:1"}`); rec.Code != 404 || !strings.Contains(rec.Body.String(), "No such image: nope:1") {
		t.Errorf("missing image: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, "/containers/create?name=taken", `{"Image":"alpine:3.20"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "already in use") {
		t.Errorf("taken name: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, "/containers/create?name=bad/name", `{"Image":"alpine:3.20"}`); rec.Code != 400 {
		t.Errorf("invalid name: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, "/containers/create", `{"Image":"alpine:3.20","HostConfig":{"Mounts":[{"Type":"volume","Source":"v","Target":"rel"}]}}`); rec.Code != 400 {
		t.Errorf("relative target: %d %s", rec.Code, rec.Body)
	}
	if _, ok := s.volumes["v"]; ok {
		t.Error("a refused create created a volume")
	}

	body := `{"Image":"alpine:3.20","Cmd":["sleep","9"],"Env":["A=1"],
	  "ExposedPorts":{"80/tcp":{},"9000/tcp":{}},
	  "HostConfig":{"PortBindings":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"8080"}]},
	    "Mounts":[{"Type":"volume","Source":"fresh","Target":"/data"},{"Type":"bind","Source":"/srv","Target":"/srv","ReadOnly":true}],
	    "RestartPolicy":{"Name":"on-failure","MaximumRetryCount":3}}}`
	rec := post(s, "/containers/create?name=made", body)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		ID       string `json:"Id"`
		Warnings []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.ID != s.ContainerID("made") || res.Warnings == nil {
		t.Fatalf("answer: %s %v", rec.Body, err)
	}
	if st, _ := s.ContainerState("made"); st != "created" {
		t.Fatalf("state %q", st)
	}
	c := s.containers[res.ID]
	if c.RestartPolicy != "on-failure" || c.RestartRetries != 3 || c.Env[0] != "A=1" || c.Cmd[0] != "sleep" || len(c.Mounts) != 2 || c.Mounts[1].RW || len(c.Ports) != 2 {
		t.Errorf("container: %+v", c)
	}
	if _, ok := s.volumes["fresh"]; !ok {
		t.Error("a named volume is created as the daemon does")
	}
	sawCreate := false
	for _, e := range s.evHistory {
		sawCreate = sawCreate || (e.Type == "container" && e.Action == "create" && e.Actor.ID == res.ID && e.Actor.Attributes["name"] == "made")
	}
	if !sawCreate {
		t.Error("no container/create event")
	}

	// The existing start handler runs it.
	if rec := post(s, "/containers/"+res.ID+"/start", ""); rec.Code != 204 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if st, _ := s.ContainerState("made"); st != "running" {
		t.Fatalf("after start: %q", st)
	}

	// No name: the daemon picks one.
	rec = post(s, "/containers/create", `{"Image":"alpine:3.20"}`)
	if rec.Code != 201 {
		t.Fatalf("unnamed: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if n := s.containers[res.ID].Name; !strings.Contains(n, "_") {
		t.Errorf("assigned name %q", n)
	}
	if got := len(s.CreateRequests()); got != 6 {
		t.Errorf("%d create requests recorded", got)
	}
}
