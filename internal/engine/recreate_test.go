package engine

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// formBodyBefore is the body the create form's options produced before a
// re-create's fields were added to ContainerCreateOptions, captured from
// that version of the engine. The form's request must not change.
const formBodyBefore = `{"Hostname":"","Domainname":"","User":"","AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"ExposedPorts":{"5432/tcp":{},"80/tcp":{},"9000/tcp":{}},"Tty":false,"OpenStdin":false,"StdinOnce":false,"Env":["LOG_LEVEL=info","EMPTY="],"Cmd":["sh","-c","sleep 1000"],"Image":"alpine:3.20","Volumes":null,"WorkingDir":"","Entrypoint":null,"Labels":null,"HostConfig":{"Binds":null,"ContainerIDFile":"","LogConfig":{"Type":"","Config":null},"NetworkMode":"","PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"5432"}],"80/tcp":[{"HostIp":"","HostPort":"8080"}],"9000/tcp":[{"HostIp":"","HostPort":""}]},"RestartPolicy":{"Name":"on-failure","MaximumRetryCount":3},"AutoRemove":false,"VolumeDriver":"","VolumesFrom":null,"ConsoleSize":[0,0],"CapAdd":null,"CapDrop":null,"CgroupnsMode":"","Dns":null,"DnsOptions":null,"DnsSearch":null,"ExtraHosts":null,"GroupAdd":null,"IpcMode":"","Cgroup":"","Links":null,"OomScoreAdj":0,"PidMode":"","Privileged":false,"PublishAllPorts":false,"ReadonlyRootfs":false,"SecurityOpt":null,"UTSMode":"","UsernsMode":"","ShmSize":0,"Isolation":"","CpuShares":0,"Memory":0,"NanoCpus":0,"CgroupParent":"","BlkioWeight":0,"BlkioWeightDevice":null,"BlkioDeviceReadBps":null,"BlkioDeviceWriteBps":null,"BlkioDeviceReadIOps":null,"BlkioDeviceWriteIOps":null,"CpuPeriod":0,"CpuQuota":0,"CpuRealtimePeriod":0,"CpuRealtimeRuntime":0,"CpusetCpus":"","CpusetMems":"","Devices":null,"DeviceCgroupRules":null,"DeviceRequests":null,"MemoryReservation":0,"MemorySwap":0,"MemorySwappiness":null,"OomKillDisable":null,"PidsLimit":null,"Ulimits":null,"CpuCount":0,"CpuPercent":0,"IOMaximumIOps":0,"IOMaximumBandwidth":0,"Mounts":[{"Type":"bind","Source":"/srv/data","Target":"/data","ReadOnly":true},{"Type":"volume","Source":"cache","Target":"/var/cache"}],"MaskedPaths":null,"ReadonlyPaths":null}}`

func TestCreateFormRequestIsUnchanged(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.AddImage(7_000_000, time.Hour, "alpine:3.20") })
	_, err := eng.CreateContainer(ctxT(t), ContainerCreateOptions{
		Image: "alpine:3.20", Name: "made",
		Ports:   []PortBinding{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}, {HostIP: "127.0.0.1", HostPort: 5432, ContainerPort: 5432, Proto: "tcp"}, {ContainerPort: 9000, Proto: "tcp"}},
		Env:     []string{"LOG_LEVEL=info", "EMPTY="},
		Mounts:  []MountOption{{Type: "bind", Source: "/srv/data", Target: "/data", ReadOnly: true}, {Type: "volume", Source: "cache", Target: "/var/cache"}},
		Restart: RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
		Cmd:     []string{"sh", "-c", "sleep 1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := bytes.TrimSpace(srv.CreateRequests()[0].Raw)
	if string(got) != formBodyBefore {
		t.Errorf("the create form's request changed:\n got %s\nwant %s", got, formBodyBefore)
	}
}

func TestCreateCloneRequestBody(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddImage(7_000_000, time.Hour, "shop/web:2")
		s.AddNetwork("shop_default", nil)
		s.AddNetwork("shop_backend", nil)
	})
	init, timeout, pids := true, 30, int64(200)
	opts := ContainerCreateOptions{
		Image: "shop/web:2", Name: "web",
		Env:         []string{"TOKEN=s3cret"},
		Cmd:         []string{"serve"},
		Entrypoint:  []string{"/bin/sh", "-c"},
		Labels:      map[string]string{"com.docker.compose.project": "shop"},
		Tty:         true,
		OpenStdin:   true,
		WorkingDir:  "/srv",
		User:        "1000",
		Hostname:    "web-host",
		Init:        &init,
		Healthcheck: &HealthConfig{Test: []string{"CMD", "probe"}, Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: time.Second, StartInterval: 2 * time.Second, Retries: 3},
		StopSignal:  "SIGQUIT",
		StopTimeout: &timeout,
		NetworkMode: "shop_default",
		Networks:    []NetworkAttachment{{Network: "shop_default", Aliases: []string{"web"}}, {Network: "shop_backend"}},
		ExposedOnly: []string{"9000/tcp"},
		Resources:   ResourceLimits{Memory: 1 << 29, MemorySwap: 1 << 30, MemoryReservation: 1 << 28, NanoCPUs: 1e9, CPUShares: 512, CPUQuota: 50000, CPUPeriod: 100000, CpusetCpus: "0-1", PidsLimit: &pids},
		ShmSize:     128 << 20,
		LogDriver:   "json-file",
		LogOptions:  map[string]string{"max-size": "10m"},
		Ports:       []PortBinding{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
		Restart:     RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
	}
	if _, err := eng.CreateContainer(ctxT(t), opts); err != nil {
		t.Fatal(err)
	}
	b := srv.CreateRequests()[0].Body
	c, h := b.Config, b.HostConfig
	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v, want %#v", name, got, want)
		}
	}
	check("entrypoint", []string(c.Entrypoint), opts.Entrypoint)
	check("cmd", []string(c.Cmd), opts.Cmd)
	check("env", c.Env, opts.Env)
	check("labels", c.Labels, opts.Labels)
	check("tty", c.Tty, true)
	check("open stdin", c.OpenStdin, true)
	check("working dir", c.WorkingDir, "/srv")
	check("user", c.User, "1000")
	check("hostname", c.Hostname, "web-host")
	check("health", []any{c.Healthcheck.Test, c.Healthcheck.Interval, c.Healthcheck.Timeout, c.Healthcheck.StartPeriod, c.Healthcheck.StartInterval, c.Healthcheck.Retries},
		[]any{opts.Healthcheck.Test, 30 * time.Second, 5 * time.Second, time.Second, 2 * time.Second, 3})
	check("stop signal", c.StopSignal, "SIGQUIT")
	check("stop timeout", *c.StopTimeout, 30)
	check("init", *h.Init, true)
	check("network mode", string(h.NetworkMode), "shop_default")
	check("shm", h.ShmSize, int64(128<<20))
	check("log", []any{h.LogConfig.Type, h.LogConfig.Config}, []any{"json-file", map[string]string{"max-size": "10m"}})
	r := h.Resources
	check("limits", []any{r.Memory, r.MemorySwap, r.MemoryReservation, r.NanoCPUs, r.CPUShares, r.CPUQuota, r.CPUPeriod, r.CpusetCpus, *r.PidsLimit},
		[]any{int64(1 << 29), int64(1 << 30), int64(1 << 28), int64(1e9), int64(512), int64(50000), int64(100000), "0-1", int64(200)})
	if _, ok := c.ExposedPorts[network.MustParsePort("9000/tcp")]; !ok {
		t.Error("9000/tcp not exposed")
	}
	if _, bound := h.PortBindings[network.MustParsePort("9000/tcp")]; bound {
		t.Error("an exposed-only port was published")
	}
	if got := h.PortBindings[network.MustParsePort("80/tcp")]; len(got) != 1 || got[0].HostPort != "8080" {
		t.Errorf("80/tcp %+v", got)
	}
	if b.NetworkingConfig == nil || len(b.NetworkingConfig.EndpointsConfig) != 2 {
		t.Fatalf("networking config %+v", b.NetworkingConfig)
	}
	check("aliases", b.NetworkingConfig.EndpointsConfig["shop_default"].Aliases, []string{"web"})
	if ep := b.NetworkingConfig.EndpointsConfig["shop_backend"]; ep == nil || ep.Aliases != nil || ep.IPAMConfig != nil {
		t.Errorf("backend endpoint %+v", ep)
	}
	if h.Privileged || h.AutoRemove || h.PublishAllPorts || len(h.CapAdd) != 0 || h.Runtime != "" {
		t.Errorf("nothing beyond the options may be set: %+v", h)
	}
}

func TestCreateCloneRefusesAnUnparseablePort(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.AddImage(7_000_000, time.Hour, "alpine:3.20") })
	_, err := eng.CreateContainer(ctxT(t), ContainerCreateOptions{Image: "alpine:3.20", ExposedOnly: []string{"http"}})
	if err == nil || !strings.Contains(err.Error(), "not a port") || len(srv.CreateRequests()) != 0 {
		t.Errorf("%v, %d requests", err, len(srv.CreateRequests()))
	}
}

func TestRenameContainer(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "web", Image: "alpine:3.20"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "db", Image: "alpine:3.20"})
	})
	ctx := ctxT(t)
	id := srv.ContainerID("web")
	if err := eng.RenameContainer(ctx, id, "web-previous-1"); err != nil {
		t.Fatal(err)
	}
	if srv.ContainerID("web-previous-1") != id || srv.ContainerID("web") != "" {
		t.Error("not renamed")
	}
	if err := eng.RenameContainer(ctx, id, "db"); !IsConflict(err) || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("taken: %v", err)
	}
	if err := eng.RenameContainer(ctx, strings.Repeat("f", 64), "x1"); !IsNotFound(err) {
		t.Errorf("unknown: %v", err)
	}
	var sent bool
	for _, r := range srv.Requests() {
		sent = sent || (r.Method == "POST" && r.Path == "/containers/"+id+"/rename" && r.Query == "name=web-previous-1")
	}
	if !sent {
		t.Error("the rename request was not recorded")
	}
}
