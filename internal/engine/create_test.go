package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestCreateContainerRequestBody(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.AddImage(7_000_000, time.Hour, "alpine:3.20") })
	ctx := ctxT(t)
	id, err := eng.CreateContainer(ctx, ContainerCreateOptions{
		Image: "alpine:3.20",
		Name:  "made",
		Ports: []PortBinding{
			{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
			{HostIP: "127.0.0.1", HostPort: 5432, ContainerPort: 5432, Proto: "tcp"},
			{HostPort: 53, ContainerPort: 53, Proto: "udp"},
			{ContainerPort: 9000, Proto: "tcp"},
		},
		Env:     []string{"LOG_LEVEL=info", "EMPTY="},
		Mounts:  []MountOption{{Type: "bind", Source: "/srv/data", Target: "/data", ReadOnly: true}, {Type: "volume", Source: "cache", Target: "/var/cache"}},
		Restart: RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
		Cmd:     []string{"sh", "-c", "sleep 1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != srv.ContainerID("made") || len(id) != 64 {
		t.Fatalf("returned ID %q, fake has %q", id, srv.ContainerID("made"))
	}
	reqs := srv.CreateRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d create requests", len(reqs))
	}
	r := reqs[0]
	b := r.Body
	if r.Name != "made" || b.Image != "alpine:3.20" || strings.Join(b.Cmd, "|") != "sh|-c|sleep 1000" || strings.Join(b.Env, "|") != "LOG_LEVEL=info|EMPTY=" {
		t.Errorf("config: name %q %+v", r.Name, b.Config)
	}
	hc := b.HostConfig
	if hc.RestartPolicy.Name != "on-failure" || hc.RestartPolicy.MaximumRetryCount != 3 {
		t.Errorf("restart: %+v", hc.RestartPolicy)
	}
	if hc.Privileged || hc.AutoRemove || hc.PublishAllPorts || len(hc.Binds) != 0 {
		t.Errorf("nothing beyond the options may be set: %+v", hc)
	}
	p80 := network.MustParsePort("80/tcp")
	if got := hc.PortBindings[p80]; len(got) != 1 || got[0].HostPort != "8080" || got[0].HostIP.IsValid() {
		t.Errorf("80/tcp: %+v", got)
	}
	if got := hc.PortBindings[network.MustParsePort("5432/tcp")]; len(got) != 1 || got[0].HostIP.String() != "127.0.0.1" {
		t.Errorf("5432/tcp: %+v", got)
	}
	if got := hc.PortBindings[network.MustParsePort("53/udp")]; len(got) != 1 || got[0].HostPort != "53" {
		t.Errorf("53/udp: %+v", got)
	}
	if got := hc.PortBindings[network.MustParsePort("9000/tcp")]; len(got) != 1 || got[0].HostPort != "" {
		t.Errorf("9000/tcp lets the daemon choose: %+v", got)
	}
	for _, p := range []string{"80/tcp", "5432/tcp", "53/udp", "9000/tcp"} {
		if _, ok := b.ExposedPorts[network.MustParsePort(p)]; !ok {
			t.Errorf("%s not exposed", p)
		}
	}
	want := []struct {
		typ      mount.Type
		src, dst string
		ro       bool
	}{{mount.TypeBind, "/srv/data", "/data", true}, {mount.TypeVolume, "cache", "/var/cache", false}}
	if len(hc.Mounts) != 2 {
		t.Fatalf("mounts: %+v", hc.Mounts)
	}
	for i, w := range want {
		m := hc.Mounts[i]
		if m.Type != w.typ || m.Source != w.src || m.Target != w.dst || m.ReadOnly != w.ro {
			t.Errorf("mount %d: %+v", i, m)
		}
	}
	if st, _ := srv.ContainerState("made"); st != "created" {
		t.Errorf("state %q: create never starts", st)
	}
	// The existing start runs it.
	if err := eng.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	d, err := eng.InspectContainer(ctx, id)
	if err != nil || d.State != "running" {
		t.Fatalf("after start: %v %+v", err, d.Container)
	}
}

func TestCreateContainerRefusals(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	_, err := eng.CreateContainer(ctx, ContainerCreateOptions{Image: "never-pulled:1"})
	if !IsNotFound(err) || !strings.Contains(err.Error(), "No such image: never-pulled:1") {
		t.Errorf("missing image: %v", err)
	}
	_, err = eng.CreateContainer(ctx, ContainerCreateOptions{Image: "python:3.12-slim", Name: "shop-web-1"})
	if !IsConflict(err) || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("name taken: %v", err)
	}
	if n := len(srv.CreateRequests()); n != 2 {
		t.Errorf("%d create requests", n)
	}
}
