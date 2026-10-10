package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestMain(m *testing.M) {
	// The opt-in real-daemon test reads its endpoint from its own variable
	// and opens it read-only; everything else is confined to temp sockets.
	InstallTestGuard()
	os.Exit(m.Run())
}

func startFake(t *testing.T, seed func(*fakeengine.Server)) (*fakeengine.Server, Engine) {
	t.Helper()
	srv := fakeengine.New()
	srv.StatsInterval = 20 * time.Millisecond
	srv.PullStepDelay = time.Millisecond
	if seed != nil {
		seed(srv)
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := New(Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return srv, eng
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func byName(cs []Container, name string) Container {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return Container{}
}

func TestGuardRefusesRealSockets(t *testing.T) {
	for _, host := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "unix:///run/user/1000/docker.sock"} {
		if _, err := New(Options{Host: host}); err == nil || !strings.Contains(err.Error(), "test guard") {
			t.Errorf("%s: a read-write engine must be refused under test, got %v", host, err)
		}
	}
}

func TestIdentityAndLists(t *testing.T) {
	_, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)

	if err := eng.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "sandbox" || info.ServerVersion != fakeengine.EngineVersion || info.APIVersion != "1.56" {
		t.Fatalf("info: %+v", info)
	}

	cs, err := eng.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 12 {
		t.Fatalf("want 12 containers (stopped ones included), got %d", len(cs))
	}
	web := byName(cs, "shop-web-1")
	if web.State != StateRunning || web.Project() != "shop" || web.Service() != "web" || !web.IsActive() {
		t.Fatalf("shop-web-1: %+v", web)
	}
	if len(web.Ports) != 1 || web.Ports[0].HostPort != 8080 || web.Ports[0].PrivatePort != 80 || web.Ports[0].Proto != "tcp" {
		t.Fatalf("ports: %+v", web.Ports)
	}
	if len(web.Networks) != 1 || web.Networks[0].Network != "shop_default" || web.Networks[0].NetworkID == "" || web.Networks[0].IP == "" {
		t.Fatalf("networks: %+v", web.Networks)
	}
	db := byName(cs, "shop-db-1")
	if db.Health != "healthy" || len(db.Mounts) != 1 || db.Mounts[0].Type != "volume" || db.Mounts[0].Name != "shop_pgdata" {
		t.Fatalf("shop-db-1: %+v", db)
	}
	worker := byName(cs, "shop-worker-1")
	if worker.State != StateExited || worker.IsActive() || !strings.Contains(worker.Status, "Exited (1)") {
		t.Fatalf("shop-worker-1: %+v", worker)
	}
	// A stopped container keeps naming its network by ID but has no address.
	blog := byName(cs, "blog-app-1")
	if len(blog.Networks) != 1 || blog.Networks[0].NetworkID == "" || blog.Networks[0].IP != "" {
		t.Fatalf("stopped container network: %+v", blog.Networks)
	}

	imgs, err := eng.Images(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dangling, inUse := 0, 0
	for _, im := range imgs {
		if im.Dangling {
			dangling++
			if len(im.Refs()) != 0 {
				t.Errorf("dangling image has refs: %+v", im)
			}
		}
		if im.Containers > 0 {
			inUse++
		}
	}
	if dangling != 2 || inUse == 0 {
		t.Fatalf("want 2 dangling and some in-use images, got %d dangling, %d in use of %d", dangling, inUse, len(imgs))
	}

	vols, err := eng.Volumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	anon := 0
	for _, v := range vols {
		if v.Anonymous() {
			anon++
		}
	}
	if len(vols) != 7 || anon != 3 {
		t.Fatalf("want 7 volumes of which 3 anonymous, got %d/%d", len(vols), anon)
	}

	nets, err := eng.Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Network{}
	for _, n := range nets {
		names[n.Name] = n
	}
	if len(nets) != 7 || !names["bridge"].Predefined() || names["shop_default"].Predefined() || len(names["shop_default"].Subnets) != 1 {
		t.Fatalf("networks: %+v", nets)
	}
}

func TestInspect(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)

	d, err := eng.InspectContainer(ctx, srv.ContainerID("shop-db-1"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "shop-db-1" || d.State != StateRunning || d.Health != "healthy" || d.StartedAt.IsZero() || !d.FinishedAt.IsZero() {
		t.Fatalf("detail: %+v", d)
	}
	if d.Image != "postgres:16" || d.RestartPolicy != "no" || len(d.Raw) == 0 || !bytes.Contains(d.Raw, []byte(`"Id"`)) {
		t.Fatalf("detail: image=%q policy=%q raw=%d", d.Image, d.RestartPolicy, len(d.Raw))
	}
	if len(d.Ports) != 1 || d.Ports[0].PrivatePort != 5432 || d.Ports[0].HostPort != 0 {
		t.Fatalf("exposed-only port: %+v", d.Ports)
	}

	ex, err := eng.InspectContainer(ctx, "shop-worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if ex.ExitCode != 1 || ex.FinishedAt.IsZero() {
		t.Fatalf("exited detail: %+v", ex)
	}

	im, err := eng.InspectImage(ctx, "nginx:1.27")
	if err != nil || im.OS != "linux" || im.Layers != 2 || len(im.Raw) == 0 {
		t.Fatalf("image detail: %+v %v", im, err)
	}
	v, err := eng.InspectVolume(ctx, "shop_pgdata")
	if err != nil || v.Driver != "local" || v.Labels[LabelComposeProject] != "shop" || len(v.Raw) == 0 {
		t.Fatalf("volume detail: %+v %v", v, err)
	}
	n, err := eng.InspectNetwork(ctx, srv.NetworkID("shop_default"))
	if err != nil || len(n.Endpoints) != 3 || n.Endpoints[0].Name != "shop-api-1" || n.Endpoints[0].IPv4 == "" || len(n.Raw) == 0 {
		t.Fatalf("network detail: %+v %v", n, err)
	}

	for name, call := range map[string]func() error{
		"container": func() error { _, err := eng.InspectContainer(ctx, "ghost"); return err },
		"image":     func() error { _, err := eng.InspectImage(ctx, "ghost:1"); return err },
		"volume":    func() error { _, err := eng.InspectVolume(ctx, "ghost"); return err },
		"network":   func() error { _, err := eng.InspectNetwork(ctx, "ghost"); return err },
	} {
		if err := call(); !IsNotFound(err) {
			t.Errorf("inspect missing %s: want not-found, got %v", name, err)
		}
	}
}

func TestContainerLifecycle(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "done", Image: "app:1", State: "exited"})
	})
	ctx := ctxT(t)
	id := srv.ContainerID("app")
	state := func() string { s, _ := srv.ContainerState("app"); return s }

	steps := []struct {
		name string
		call func(context.Context, string) error
		want string
	}{
		{"pause", eng.PauseContainer, "paused"},
		{"unpause", eng.UnpauseContainer, "running"},
		{"stop", eng.StopContainer, "exited"},
		{"start", eng.StartContainer, "running"},
		{"restart", eng.RestartContainer, "running"},
		{"kill", eng.KillContainer, "exited"},
	}
	for _, st := range steps {
		if err := st.call(ctx, id); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got := state(); got != st.want {
			t.Fatalf("after %s: state %q, want %q", st.name, got, st.want)
		}
	}

	// Daemon refusals come back as conflicts carrying the daemon's words.
	if err := eng.KillContainer(ctx, id); !IsConflict(err) || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("kill of a stopped container: %v", err)
	}
	if err := eng.PauseContainer(ctx, id); !IsConflict(err) {
		t.Fatalf("pause of a stopped container: %v", err)
	}
	if err := eng.StartContainer(ctx, "ghost"); !IsNotFound(err) {
		t.Fatalf("start of a missing container: %v", err)
	}

	// Removal is never forced: a running container is refused and survives.
	if err := eng.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	err := eng.RemoveContainer(ctx, id)
	if !IsConflict(err) || !strings.Contains(err.Error(), "container is running") {
		t.Fatalf("remove of a running container: %v", err)
	}
	if strings.Contains(err.Error(), "Error response from daemon") {
		t.Fatalf("client-library framing leaked into the message: %q", err.Error())
	}
	if state() != "running" {
		t.Fatal("a refused removal changed the container")
	}
	if err := eng.RemoveContainer(ctx, srv.ContainerID("done")); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.ContainerState("done"); ok {
		t.Fatal("container was not removed")
	}
}

// TestNeverForces looks at what actually went over the wire for every
// mutating method: no request may carry a force flag, a volume-removal flag
// on container delete, or an untagged pull.
func TestNeverForces(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "a", Image: "app:1", State: "exited", AnonymousVolumes: 1})
		s.AddContainer(fakeengine.ContainerSpec{Name: "b", Image: "app:2", Networks: []string{"bridge", "extra"}})
		s.AddImage(1000, time.Hour, "unused:1")
		s.AddVolume("v", nil)
		s.AddNetwork("n", nil)
	})
	ctx := ctxT(t)
	a, b := srv.ContainerID("a"), srv.ContainerID("b")
	srv.ResetRequests()

	_ = eng.StopContainer(ctx, b)
	_ = eng.StartContainer(ctx, b)
	_ = eng.RestartContainer(ctx, b)
	_ = eng.PauseContainer(ctx, b)
	_ = eng.UnpauseContainer(ctx, b)
	_ = eng.DisconnectNetwork(ctx, srv.NetworkID("extra"), b)
	_ = eng.ConnectNetwork(ctx, srv.NetworkID("n"), b)
	_ = eng.KillContainer(ctx, b)
	_ = eng.RemoveContainer(ctx, a)
	_ = eng.RemoveContainer(ctx, b)
	_, _ = eng.RemoveImage(ctx, "unused:1")
	_ = eng.RemoveVolume(ctx, "v")
	_ = eng.RemoveNetwork(ctx, srv.NetworkID("extra"))
	_ = eng.PullImage(ctx, "alpine", nil)

	muts := srv.Mutations()
	if len(muts) != 14 {
		t.Fatalf("want 14 mutating requests, got %d: %v", len(muts), muts)
	}
	for _, r := range muts {
		q := strings.ToLower(r.Query)
		if strings.Contains(q, "force") || strings.Contains(q, "v=1") || strings.Contains(q, "v=true") {
			t.Errorf("request carries a force/cascade flag: %s", r)
		}
	}
	// Anonymous volumes of a removed container are kept.
	if n := len(srv.VolumeNames()); n != 1 {
		t.Errorf("want the anonymous volume to survive container removal, have %d volumes: %v", n, srv.VolumeNames())
	}
	// A pull without a tag asks for exactly :latest.
	last := muts[len(muts)-1]
	if !strings.Contains(last.Query, "tag=latest") || !strings.Contains(last.Query, "fromImage=") {
		t.Errorf("pull request: %s", last)
	}
}

func TestImages(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "web", Image: "nginx:1.27", State: "exited"})
		s.AddImage(1000, time.Hour, "tool:1", "tool:latest")
	})
	ctx := ctxT(t)

	var mu sync.Mutex
	var seen []PullProgress
	if err := eng.PullImage(ctx, "alpine:3.20", func(p PullProgress) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	if !srv.HasImage("alpine:3.20") {
		t.Fatal("pulled image is missing")
	}
	if len(seen) < 10 {
		t.Fatalf("want progress callbacks, got %d", len(seen))
	}
	final := seen[len(seen)-1]
	if final.Total == 0 || final.Current != final.Total || !strings.Contains(final.Status, "Downloaded newer image") {
		t.Fatalf("final progress: %+v", final)
	}
	prev := int64(-1)
	for _, p := range seen {
		if p.Current < prev {
			t.Fatalf("progress went backwards: %d after %d", p.Current, prev)
		}
		prev = p.Current
	}

	if err := eng.PullImage(ctx, "nosuch/thing:1", nil); err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("pull of a missing image: %v", err)
	}
	if err := eng.PullImage(ctx, "Not A Reference", nil); err == nil {
		t.Fatal("malformed reference must fail")
	}

	// In use → refused, image survives.
	if _, err := eng.RemoveImage(ctx, "nginx:1.27"); !IsConflict(err) {
		t.Fatalf("remove of an in-use image: %v", err)
	}
	if !srv.HasImage("nginx:1.27") {
		t.Fatal("refused removal deleted the image")
	}
	// One of two tags → untag only.
	res, err := eng.RemoveImage(ctx, "tool:latest")
	if err != nil || len(res) != 1 || res[0].Untagged != "tool:latest" || res[0].Deleted != "" {
		t.Fatalf("untag: %+v %v", res, err)
	}
	// Last tag → untag and delete.
	res, err = eng.RemoveImage(ctx, "tool:1")
	if err != nil || len(res) != 2 || res[0].Untagged != "tool:1" || !strings.HasPrefix(res[1].Deleted, "sha256:") {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if _, err := eng.RemoveImage(ctx, "tool:1"); !IsNotFound(err) {
		t.Fatalf("remove of a missing image: %v", err)
	}
}

func TestVolumesAndNetworks(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "db", Image: "pg:16", Volumes: []string{"data"}, Networks: []string{"backend"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "cli", Image: "pg:16"})
	})
	ctx := ctxT(t)

	v, err := eng.CreateVolume(ctx, "scratch")
	if err != nil || v.Name != "scratch" || v.Driver != "local" {
		t.Fatalf("create volume: %+v %v", v, err)
	}
	if err := eng.RemoveVolume(ctx, "scratch"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RemoveVolume(ctx, "data"); !IsConflict(err) || !strings.Contains(err.Error(), "volume is in use") {
		t.Fatalf("remove of an in-use volume: %v", err)
	}
	if err := eng.RemoveVolume(ctx, "ghost"); !IsNotFound(err) {
		t.Fatalf("remove of a missing volume: %v", err)
	}

	id, err := eng.CreateNetwork(ctx, "front")
	if err != nil || id == "" {
		t.Fatalf("create network: %q %v", id, err)
	}
	if _, err := eng.CreateNetwork(ctx, "front"); !IsConflict(err) {
		t.Fatalf("duplicate network: %v", err)
	}
	cli := srv.ContainerID("cli")
	if err := eng.ConnectNetwork(ctx, id, cli); err != nil {
		t.Fatal(err)
	}
	if got := srv.ContainerNetworks("cli"); !reflect.DeepEqual(got, []string{"bridge", "front"}) {
		t.Fatalf("after connect: %v", got)
	}
	// The daemon answers 403 for these; they are still "refused because of state".
	if err := eng.ConnectNetwork(ctx, id, cli); !IsConflict(err) {
		t.Fatalf("connect twice: %v", err)
	}
	if err := eng.RemoveNetwork(ctx, id); !IsConflict(err) || !strings.Contains(err.Error(), "active endpoints") {
		t.Fatalf("remove of a network with endpoints: %v", err)
	}
	if err := eng.DisconnectNetwork(ctx, id, cli); err != nil {
		t.Fatal(err)
	}
	if err := eng.DisconnectNetwork(ctx, id, cli); !IsConflict(err) {
		t.Fatalf("disconnect twice: %v", err)
	}
	if err := eng.RemoveNetwork(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := eng.RemoveNetwork(ctx, srv.NetworkID("bridge")); !IsConflict(err) || !strings.Contains(err.Error(), "pre-defined") {
		t.Fatalf("remove of a predefined network: %v", err)
	}
}

func TestEvents(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	})
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()

	got := make(chan Event, 64)
	done := make(chan error, 1)
	go func() { done <- eng.Events(ctx, time.Time{}, func(e Event) { got <- e }) }()
	waitFor(t, "event subscription", func() bool { return srv.EventSubscribers() == 1 })

	if err := eng.StopContainer(ctx, srv.ContainerID("app")); err != nil {
		t.Fatal(err)
	}
	want := []string{"container kill", "container die", "network disconnect", "container stop"}
	for _, w := range want {
		select {
		case e := <-got:
			if e.Type+" "+e.Action != w {
				t.Fatalf("want event %q, got %q", w, e.Type+" "+e.Action)
			}
			if e.Type == "container" && e.Name() != "app" {
				t.Fatalf("event name: %q", e.Name())
			}
			if e.Action == "die" && e.Attributes["exitCode"] != "0" {
				t.Fatalf("die event attributes: %v", e.Attributes)
			}
			if e.Time.IsZero() || time.Since(e.Time) > time.Minute {
				t.Fatalf("event time: %v", e.Time)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", w)
		}
	}

	// A dropped stream is reported as "daemon unavailable", not swallowed.
	srv.DropEventStreams()
	select {
	case err := <-done:
		if !IsUnavailable(err) {
			t.Fatalf("dropped event stream: want unavailable, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Events did not return after the stream was dropped")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogs(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "plain", Image: "app:1", Logs: []string{"one", "two", "three"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "tty", Image: "app:1", Tty: true, Logs: []string{"alpha", "beta"}})
		s.AddContainer(fakeengine.ContainerSpec{Name: "dead", Image: "app:1", State: "exited", Logs: []string{"last words"}})
	})
	ctx := ctxT(t)

	// Multiplexed (no TTY): the 8-byte frame headers must be gone.
	var buf bytes.Buffer
	if err := eng.Logs(ctx, srv.ContainerID("plain"), 2, false, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "two\nthree\n" {
		t.Fatalf("demultiplexed tail: %q", buf.String())
	}
	// stderr is interleaved into the same writer.
	srv.WriteLog("plain", 2, "to stderr")
	buf.Reset()
	if err := eng.Logs(ctx, "plain", 1, false, &buf); err != nil || buf.String() != "to stderr\n" {
		t.Fatalf("stderr: %q %v", buf.String(), err)
	}
	// TTY containers are raw streams.
	buf.Reset()
	if err := eng.Logs(ctx, "tty", 10, false, &buf); err != nil || buf.String() != "alpha\r\nbeta\r\n" {
		t.Fatalf("tty logs: %q %v", buf.String(), err)
	}
	// A stopped container still has logs, and follow returns instead of hanging.
	buf.Reset()
	if err := eng.Logs(ctx, "dead", 10, true, &buf); err != nil || buf.String() != "last words\n" {
		t.Fatalf("stopped container logs: %q %v", buf.String(), err)
	}

	// Follow delivers new lines and ends when the caller cancels.
	fctx, cancel := context.WithCancel(ctx)
	var live syncBuffer
	done := make(chan error, 1)
	go func() { done <- eng.Logs(fctx, "plain", 0, true, &live) }()
	waitFor(t, "log follower", func() bool { return srv.LogFollowers("plain") == 1 })
	srv.WriteLog("plain", 1, "fresh line")
	waitFor(t, "followed line", func() bool { return strings.Contains(live.String(), "fresh line\n") })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled follow: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follow did not stop on cancel")
	}
	waitFor(t, "follower closed", func() bool { return srv.LogFollowers("plain") == 0 })

	// Follow ends by itself when the container exits.
	done2 := make(chan error, 1)
	go func() { done2 <- eng.Logs(ctx, "plain", 0, true, io.Discard) }()
	waitFor(t, "log follower", func() bool { return srv.LogFollowers("plain") == 1 })
	srv.Crash("plain", 3)
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("follow after container exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follow did not end when the container exited")
	}

	if err := eng.Logs(ctx, "ghost", 1, false, io.Discard); !IsNotFound(err) {
		t.Fatalf("logs of a missing container: %v", err)
	}
}

func TestStats(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
	})
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()

	var mu sync.Mutex
	var samples []RawStats
	done := make(chan error, 1)
	go func() {
		done <- eng.Stats(ctx, srv.ContainerID("app"), func(s RawStats) {
			mu.Lock()
			samples = append(samples, s)
			mu.Unlock()
		})
	}()
	waitFor(t, "three samples", func() bool { mu.Lock(); defer mu.Unlock(); return len(samples) >= 3 })

	mu.Lock()
	a, b := samples[0], samples[len(samples)-1]
	mu.Unlock()
	if b.CPUTotal <= a.CPUTotal || b.CPUSystem <= a.CPUSystem || b.OnlineCPUs != 4 {
		t.Fatalf("cpu counters must advance: %+v → %+v", a, b)
	}
	if b.MemUsage == 0 || b.MemLimit != 16<<30 || b.MemCache == 0 || b.MemCache >= b.MemUsage {
		t.Fatalf("memory: %+v", b)
	}
	if b.NetRx == 0 || b.NetTx == 0 || b.Pids == 0 || !b.Read.After(a.Read) {
		t.Fatalf("counters: %+v", b)
	}

	// The daemon ends the stream when the container stops: that is a clean
	// end, not an error.
	srv.Crash("app", 1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stats after container exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stats did not end when the container exited")
	}
}

func TestExec(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "app", Image: "app:1"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "bare", Image: "distroless:1", NoShell: true})
		s.AddContainer(fakeengine.ContainerSpec{Name: "off", Image: "app:1", State: "exited"})
	})
	ctx := ctxT(t)

	id, err := eng.ExecCreate(ctx, srv.ContainerID("app"), []string{"sh"}, 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := eng.ExecAttach(ctx, id, 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	out := &syncBuffer{}
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(out, conn); close(copied) }()

	waitFor(t, "prompt", func() bool { return strings.Contains(out.String(), ":/# ") })
	if st, err := eng.ExecInspect(ctx, id); err != nil || !st.Running {
		t.Fatalf("inspect while running: %+v %v", st, err)
	}
	_, _ = conn.Write([]byte("echo over-the-wire\r"))
	waitFor(t, "echo output", func() bool { return strings.Contains(out.String(), "\r\nover-the-wire\r\n") })

	_, _ = conn.Write([]byte("stty size\r"))
	waitFor(t, "initial size", func() bool { return strings.Contains(out.String(), "\r\n30 100\r\n") })
	if err := eng.ExecResize(ctx, id, 50, 132); err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("stty size\r"))
	waitFor(t, "resized", func() bool { return strings.Contains(out.String(), "\r\n50 132\r\n") })

	_, _ = conn.Write([]byte("exit 7\r"))
	select {
	case <-copied:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end after exit")
	}
	waitFor(t, "exit code", func() bool { st, _ := eng.ExecInspect(ctx, id); return !st.Running && st.ExitCode == 7 })

	// A process that cannot start: the stream carries the error and the exit
	// code is 127 — exactly how the real daemon reports it.
	id2, err := eng.ExecCreate(ctx, srv.ContainerID("bare"), []string{"sh"}, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	conn2, err := eng.ExecAttach(ctx, id2, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(conn2)
	conn2.Close()
	if !strings.Contains(string(msg), "executable file not found") {
		t.Fatalf("start failure text: %q", msg)
	}
	if st, _ := eng.ExecInspect(ctx, id2); st.Running || st.ExitCode != 127 {
		t.Fatalf("start failure status: %+v", st)
	}

	if _, err := eng.ExecCreate(ctx, srv.ContainerID("off"), []string{"sh"}, 24, 80); !IsConflict(err) {
		t.Fatalf("exec in a stopped container: %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	if _, err := eng.Containers(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	if _, err := eng.Containers(ctx); !IsUnavailable(err) {
		t.Fatalf("list against a stopped daemon: want unavailable, got %v", err)
	}
	if err := eng.Ping(ctx); !IsUnavailable(err) {
		t.Fatalf("ping against a stopped daemon: want unavailable, got %v", err)
	}
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	if cs, err := eng.Containers(ctx); err != nil || len(cs) != 12 {
		t.Fatalf("after restart: %d containers, %v", len(cs), err)
	}
}

func TestDaemonErrorSurfaces(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	srv.FailNext("GET", `^/containers/json$`, 500, "layer store is corrupted", 1)
	_, err := eng.Containers(ctx)
	if err == nil || err.Error() != "list containers: layer store is corrupted" {
		t.Fatalf("want the daemon's words with the operation, got %v", err)
	}
	if _, err := eng.Containers(ctx); err != nil {
		t.Fatalf("the engine must keep working after an error: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
