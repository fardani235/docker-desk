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

func TestContainerRenameHTTP(t *testing.T) {
	s := New()
	s.AddContainer(ContainerSpec{Name: "web", Image: "alpine:3.20"})
	s.AddContainer(ContainerSpec{Name: "db", Image: "alpine:3.20"})
	id := s.ContainerID("web")
	for _, tc := range []struct {
		path string
		code int
		says string
	}{
		{"/containers/" + strings.Repeat("f", 64) + "/rename?name=x1", 404, "No such container"},
		{"/containers/" + id + "/rename?name=bad/name", 400, "Invalid container name"},
		{"/containers/" + id + "/rename?name=", 400, "Invalid container name"},
		{"/containers/" + id + "/rename?name=web", 400, "same name"},
		{"/containers/" + id + "/rename?name=db", 409, "already in use"},
	} {
		if rec := post(s, tc.path, ""); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	if s.ContainerID("web") != id {
		t.Fatal("a refused rename changed the name")
	}
	if rec := post(s, "/containers/"+id+"/rename?name=web-previous-1", ""); rec.Code != 204 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if s.ContainerID("web-previous-1") != id || s.ContainerID("web") != "" {
		t.Error("the name did not change")
	}
	var ev bool
	for _, e := range s.evHistory {
		ev = ev || (e.Type == "container" && e.Action == "rename" && e.Actor.ID == id && e.Actor.Attributes["name"] == "web-previous-1" && e.Actor.Attributes["oldName"] == "/web")
	}
	if !ev {
		t.Error("no container/rename event")
	}
	var recorded int
	for _, r := range s.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/rename") {
			recorded++
		}
	}
	if recorded != 6 {
		t.Errorf("%d rename requests recorded", recorded)
	}
}

// A container created with a re-create's fields is reported back with them,
// merged over its image's configuration as the daemon stores it.
func TestContainerCreateThenInspectReportsTheCloneFields(t *testing.T) {
	s := New()
	s.AddImage(7_000_000, time.Hour, "shop/web:2")
	s.AddNetwork("shop_default", nil)
	body := `{"Image":"shop/web:2","Entrypoint":["/bin/sh","-c"],"Cmd":["serve"],"Env":["TOKEN=x"],
	  "Labels":{"com.docker.compose.project":"shop"},"Tty":true,"OpenStdin":true,"WorkingDir":"/srv","User":"1000","Hostname":"web-host",
	  "Healthcheck":{"Test":["CMD","probe"],"Interval":30000000000,"Retries":3},"StopSignal":"SIGQUIT","StopTimeout":30,
	  "ExposedPorts":{"9000/tcp":{},"80/tcp":{}},
	  "HostConfig":{"NetworkMode":"shop_default","Init":true,"ShmSize":134217728,"Memory":536870912,"NanoCpus":1000000000,"PidsLimit":200,
	    "PortBindings":{"80/tcp":[{"HostPort":""}]},"LogConfig":{"Type":"json-file","Config":{"max-size":"10m"}}},
	  "NetworkingConfig":{"EndpointsConfig":{"shop_default":{"Aliases":["web"]}}}}`
	rec := post(s, "/containers/create?name=web", body)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	get := httptest.NewRecorder()
	s.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1.47/containers/web/json", nil))
	var in struct {
		Config struct {
			Hostname, User, WorkingDir, StopSignal string
			Entrypoint, Cmd, Env                   []string
			Labels                                 map[string]string
			Tty, OpenStdin                         bool
			StopTimeout                            int
			Healthcheck                            struct {
				Test     []string
				Interval int64
				Retries  int
			}
			ExposedPorts map[string]any
		}
		HostConfig struct {
			NetworkMode  string
			Init         bool
			ShmSize      int64
			Memory       int64
			NanoCpus     int64
			PidsLimit    int64
			PortBindings map[string][]struct{ HostIp, HostPort string }
			LogConfig    struct {
				Type   string
				Config map[string]string
			}
		}
		NetworkSettings struct {
			Networks map[string]struct{ Aliases []string }
		}
	}
	if err := json.Unmarshal(get.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}
	c, h := in.Config, in.HostConfig
	if c.Hostname != "web-host" || c.User != "1000" || c.WorkingDir != "/srv" || c.StopSignal != "SIGQUIT" || c.StopTimeout != 30 ||
		strings.Join(c.Entrypoint, " ") != "/bin/sh -c" || strings.Join(c.Cmd, " ") != "serve" || !c.Tty || !c.OpenStdin ||
		c.Labels["com.docker.compose.project"] != "shop" || c.Healthcheck.Interval != 30e9 || c.Healthcheck.Retries != 3 {
		t.Errorf("config: %+v", c)
	}
	// The image's PATH is merged in after the request's own entries.
	if len(c.Env) != 2 || c.Env[0] != "TOKEN=x" || !strings.HasPrefix(c.Env[1], "PATH=") {
		t.Errorf("env %v", c.Env)
	}
	if _, ok := c.ExposedPorts["9000/tcp"]; !ok {
		t.Errorf("exposed %v", c.ExposedPorts)
	}
	if h.NetworkMode != "shop_default" || !h.Init || h.ShmSize != 128<<20 || h.Memory != 512<<20 || h.NanoCpus != 1e9 || h.PidsLimit != 200 ||
		h.LogConfig.Config["max-size"] != "10m" {
		t.Errorf("host config: %+v", h)
	}
	if b := h.PortBindings["80/tcp"]; len(b) != 1 || b[0].HostPort != "" {
		t.Errorf("a port the daemon picked is reported as asked for: %+v", b)
	}
	if _, bound := h.PortBindings["9000/tcp"]; bound {
		t.Error("exposed-only port reported as bound")
	}
	if a := in.NetworkSettings.Networks["shop_default"].Aliases; len(a) != 1 || a[0] != "web" {
		t.Errorf("networks %+v", in.NetworkSettings.Networks)
	}
	if _, ok := in.NetworkSettings.Networks["bridge"]; ok {
		t.Error("attached to the default bridge as well")
	}

	// Refusals before anything is created.
	if rec := post(s, "/containers/create?name=x2", `{"Image":"shop/web:2","NetworkingConfig":{"EndpointsConfig":{"nope":{}}}}`); rec.Code != 404 {
		t.Errorf("missing network: %d %s", rec.Code, rec.Body)
	}
	if rec := post(s, "/containers/create?name=x3", `{"Image":"shop/web:2","NetworkingConfig":{"EndpointsConfig":{"bridge":{"Aliases":["a"]}}}}`); rec.Code != 400 {
		t.Errorf("alias on the default bridge: %d %s", rec.Code, rec.Body)
	}
	if s.ContainerID("x2") != "" || s.ContainerID("x3") != "" {
		t.Error("a refused create created a container")
	}
}

// The daemon records the mode each mount spelling carried, and applies the
// shared label to a named volume that carried none; the fake reports both,
// because a clone reads this answer (docs/DECISIONS.md, round twenty).
func TestContainerCreateReportsMountModes(t *testing.T) {
	s := New()
	s.AddImage(7_000_000, time.Hour, "shop/web:2")
	body := `{"Image":"shop/web:2","HostConfig":{
	  "Mounts":[{"Type":"volume","Source":"web-data","Target":"/data"},
	            {"Type":"bind","Source":"/srv/data","Target":"/data-host","ReadOnly":true}],
	  "Binds":["web-data:/from-ro:ro","web-data:/from-z:Z","web-data:/from-rw:rw","/srv/data:/from-bind"]}}`
	if rec := post(s, "/containers/create?name=web", body); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	get := httptest.NewRecorder()
	s.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1.47/containers/web/json", nil))
	if get.Code != 200 {
		t.Fatalf("inspect: %d %s", get.Code, get.Body)
	}
	var in struct {
		Mounts []struct {
			Destination, Type, Mode string
			RW                      bool
		}
	}
	if err := json.Unmarshal(get.Body.Bytes(), &in); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		mode string
		rw   bool
	}{
		"/data":      {"z", true}, // Mounts, volume: the daemon's default
		"/data-host": {"", false}, // Mounts, bind: no mode at all
		"/from-ro":   {"ro", false},
		"/from-z":    {"Z", true},
		"/from-rw":   {"rw", true},
		"/from-bind": {"", true},
	}
	for _, m := range in.Mounts {
		w, ok := want[m.Destination]
		if !ok {
			t.Errorf("unexpected mount %s", m.Destination)
			continue
		}
		if m.Mode != w.mode || m.RW != w.rw {
			t.Errorf("%s: mode %q rw %v; want %q %v", m.Destination, m.Mode, m.RW, w.mode, w.rw)
		}
		delete(want, m.Destination)
	}
	for d := range want {
		t.Errorf("mount %s missing", d)
	}
}
