package plan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/fardani235/docker-desk/internal/engine"
)

// TestRealDaemonRecreateReadOnly derives a re-create's configuration from
// every container of a real daemon and checks each carried field against
// the daemon's own document, decoded independently with the client
// library's types. It is opt-in (DOCKER_DESK_REAL_HOST, see `make
// real-readonly-check`) and read-only: the engine refuses anything but GET
// and HEAD before it is sent, and no create, rename or stop is attempted on
// a real object. It is the only evidence from a real daemon for the
// derivation; no re-create has been sent to one (docs/TESTING.md).
func TestRealDaemonRecreateReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only re-create derivation check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const ghost = "docker-desk-readonly-guard-check-does-not-exist"
	if err := eng.RenameContainer(ctx, ghost, ghost+"-2"); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("a rename was not refused locally: %v", err)
	}
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultsFrom(info)
	t.Logf("daemon %s, defaults %+v", info.ServerVersion, def)

	before, err := eng.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Skip("the daemon has no containers to derive from")
	}
	for _, c := range before {
		d, err := eng.InspectContainer(ctx, c.ID)
		if err != nil {
			t.Errorf("%s: inspect: %v", c.Name, err)
			continue
		}
		base, err := eng.InspectImage(ctx, d.ImageID)
		if err != nil {
			t.Errorf("%s: inspect image: %v", c.Name, err)
			continue
		}
		spec, refused, err := Clone(d, base, def)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		checkAgainstSource(t, c.Name, d.Raw, base.Raw, spec, refused)
		t.Logf("%s: %d env carried, %d labels, %d ports, %d mounts, %d networks; left to the image: %v; refused: %v",
			c.Name, len(spec.Env), len(spec.Labels), len(spec.Ports), len(spec.Mounts), len(spec.Networks), spec.FromImage, refused)
	}

	after, err := eng.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summary := func(cs []engine.Container) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID+" "+c.Name+" "+c.State)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(summary(before), summary(after)) {
		t.Errorf("the daemon's containers changed during a read-only check:\n before %v\n after  %v", summary(before), summary(after))
	}
}

func checkAgainstSource(t *testing.T, name string, raw, imageRaw []byte, spec CloneSpec, refused []string) {
	t.Helper()
	var src container.InspectResponse
	if err := json.Unmarshal(raw, &src); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var img struct{ Config *container.Config }
	_ = json.Unmarshal(imageRaw, &img)
	if img.Config == nil {
		img.Config = &container.Config{}
	}
	cfg, host := src.Config, src.HostConfig
	fail := func(what string, got, want any) {
		t.Helper()
		t.Errorf("%s: %s: carried %v, the daemon says %v", name, what, got, want)
	}
	if spec.Image != cfg.Image || spec.Name != strings.TrimPrefix(src.Name, "/") {
		fail("identity", spec.Image+" "+spec.Name, cfg.Image+" "+src.Name)
	}

	// Environment: every carried entry is the container's, and every entry
	// of the container is carried or is the image's own.
	have := map[string]bool{}
	for _, e := range cfg.Env {
		have[e] = true
	}
	imageEnv := map[string]bool{}
	for _, e := range img.Config.Env {
		imageEnv[e] = true
	}
	carried := map[string]bool{}
	for _, e := range spec.Env {
		carried[e] = true
		if !have[e] {
			fail("an environment entry the container does not have", strings.SplitN(e, "=", 2)[0], "absent")
		}
	}
	for _, e := range cfg.Env {
		if !carried[e] && !imageEnv[e] {
			fail("an environment entry dropped", strings.SplitN(e, "=", 2)[0], "set")
		}
	}
	for _, line := range append(append([]string{}, refused...), spec.FromImage...) {
		for _, e := range cfg.Env {
			if _, v, _ := strings.Cut(e, "="); len(v) >= minRedacted && strings.Contains(line, v) {
				t.Errorf("%s: a shown line contains an environment value", name)
			}
		}
	}
	for k, v := range cfg.Labels {
		if got, ok := spec.Labels[k]; ok && got != v {
			fail("label "+k, got, v)
		} else if !ok && img.Config.Labels[k] != v {
			fail("label dropped", k, v)
		}
	}
	if spec.Entrypoint != nil && !reflect.DeepEqual(spec.Entrypoint, []string{""}) && !equalArgs(spec.Entrypoint, cfg.Entrypoint) {
		fail("entrypoint", spec.Entrypoint, cfg.Entrypoint)
	}
	if spec.Cmd != nil && !equalArgs(spec.Cmd, cfg.Cmd) {
		fail("command", spec.Cmd, cfg.Cmd)
	}
	for _, f := range []struct{ what, got, want string }{
		{"working directory", spec.WorkingDir, cfg.WorkingDir}, {"user", spec.User, cfg.User},
		{"stop signal", spec.StopSignal, cfg.StopSignal}, {"hostname", spec.Hostname, cfg.Hostname},
	} {
		if f.got != "" && f.got != f.want {
			fail(f.what, f.got, f.want)
		}
	}
	if spec.Tty != cfg.Tty || spec.OpenStdin != cfg.OpenStdin {
		fail("tty / open stdin", []bool{spec.Tty, spec.OpenStdin}, []bool{cfg.Tty, cfg.OpenStdin})
	}
	if !reflect.DeepEqual(spec.Init, host.Init) || !reflect.DeepEqual(spec.StopTimeout, cfg.StopTimeout) {
		fail("init / stop timeout", []any{spec.Init, spec.StopTimeout}, []any{host.Init, cfg.StopTimeout})
	}
	if spec.Restart.MaximumRetryCount != host.RestartPolicy.MaximumRetryCount ||
		(spec.Restart.Name != string(host.RestartPolicy.Name) && !(spec.Restart.Name == "no" && host.RestartPolicy.Name == "")) {
		fail("restart", spec.Restart, host.RestartPolicy)
	}
	r := spec.Resources
	if r.Memory != host.Memory || r.MemorySwap != host.MemorySwap || r.MemoryReservation != host.MemoryReservation ||
		r.NanoCPUs != host.NanoCPUs || r.CPUShares != host.CPUShares || r.CPUQuota != host.CPUQuota ||
		r.CPUPeriod != host.CPUPeriod || r.CpusetCpus != host.CpusetCpus || spec.ShmSize != host.ShmSize {
		fail("limits", r, host.Resources)
	}
	refusedText := strings.Join(refused, "\n")
	if !strings.Contains(refusedText, FamPorts+":") {
		n := 0
		for _, bs := range host.PortBindings {
			n += len(bs)
		}
		if n != len(spec.Ports) {
			fail("published ports", len(spec.Ports), n)
		}
	}
	if !strings.Contains(refusedText, FamStorage+":") {
		for _, m := range src.Mounts {
			found := false
			for _, ms := range spec.Mounts {
				srcName := m.Source
				if m.Type == "volume" {
					srcName = m.Name
				}
				found = found || (string(m.Type) == ms.Type && ms.Source == srcName && ms.Destination == m.Destination && ms.ReadOnly == !m.RW)
			}
			if !found {
				fail("mount", m.Destination, "not carried")
			}
		}
	}
	mode := string(host.NetworkMode)
	if src.NetworkSettings != nil && !strings.HasPrefix(mode, "container:") {
		var want, got []string
		for n := range src.NetworkSettings.Networks {
			if (mode == "host" || mode == "none") && n == mode {
				continue
			}
			want = append(want, n)
		}
		for _, a := range spec.Networks {
			got = append(got, a.Network)
			ep := src.NetworkSettings.Networks[a.Network]
			for _, al := range a.Aliases {
				if ep == nil || !hasStr(ep.Aliases, al) {
					fail("alias on "+a.Network, al, "absent")
				}
			}
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(want, got) {
			fail("networks", got, want)
		}
	}
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
