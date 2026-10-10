package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// TestRealDaemonReadOnly is the one test that talks to a real Docker daemon,
// and it is opt-in: it runs only when DOCKER_DESK_REAL_HOST names an endpoint
// (see `make real-readonly-check`).
//
// It exists to validate the decoding half of the engine layer against
// Docker's own server. The engine is built in read-only mode, so the HTTP
// request hook rejects anything that is not GET/HEAD before it is sent; the
// test additionally proves that by attempting a mutation on a name that
// cannot exist.
func TestRealDaemonReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only check against a real daemon")
	}
	eng, err := New(Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := eng.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.ServerVersion == "" || info.APIVersion == "" {
		t.Fatalf("info decoded empty: %+v", info)
	}
	t.Logf("daemon %q engine %s api %s %s/%s", info.Name, info.ServerVersion, info.APIVersion, info.OSType, info.Architecture)

	// Mutations must be refused locally. The target names cannot exist, so
	// even a failure of the guard could not touch a real object.
	const ghost = "docker-desk-readonly-guard-check-does-not-exist"
	if err := eng.RemoveContainer(ctx, ghost); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("wrapper did not refuse: %v", err)
	}
	if inner, ok := eng.(*readOnlyEngine); ok {
		err := inner.Engine.RemoveContainer(ctx, ghost)
		if !errors.Is(err, ErrReadOnly) {
			t.Fatalf("HTTP hook did not refuse a DELETE: %v", err)
		}
	} else {
		t.Fatal("engine is not read-only")
	}

	validJSON := func(what string, raw []byte) {
		t.Helper()
		if !json.Valid(raw) || len(raw) < 2 {
			t.Errorf("%s: raw inspect is not valid JSON (%d bytes)", what, len(raw))
		}
	}

	containers, err := eng.Containers(ctx)
	if err != nil {
		t.Fatalf("containers: %v", err)
	}
	for _, c := range containers {
		if c.ID == "" || c.Name == "" || c.State == "" {
			t.Errorf("container decoded with empty fields: %+v", c)
		}
		d, err := eng.InspectContainer(ctx, c.ID)
		if err != nil {
			t.Errorf("inspect container %s: %v", c.Name, err)
			continue
		}
		validJSON("container "+c.Name, d.Raw)
		if d.ID != c.ID || d.State != c.State {
			t.Errorf("inspect disagrees with list for %s: %s/%s vs %s/%s", c.Name, d.ID, d.State, c.ID, c.State)
		}
		t.Logf("container %-20s %-8s image=%s nets=%d mounts=%d ports=%d started=%s",
			c.Name, c.State, c.Image, len(c.Networks), len(c.Mounts), len(d.Ports), d.StartedAt.Format(time.RFC3339))
		for _, n := range c.Networks {
			t.Logf("    network %q id=%s ip=%q", n.Network, ShortID(n.NetworkID), n.IP)
		}

		var buf bytes.Buffer
		lctx, lcancel := context.WithTimeout(ctx, 5*time.Second)
		if err := eng.Logs(lctx, c.ID, 3, false, &buf); err != nil {
			t.Errorf("logs %s: %v", c.Name, err)
		}
		lcancel()
		t.Logf("    logs tail: %d bytes", buf.Len())

		if c.State == StateRunning {
			sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
			n := 0
			var last RawStats
			_ = eng.Stats(sctx, c.ID, func(s RawStats) {
				n++
				last = s
				if n == 2 {
					scancel()
				}
			})
			scancel()
			if n < 2 {
				t.Errorf("stats %s: got %d samples", c.Name, n)
			} else if last.CPUSystem == 0 || last.MemUsage == 0 || last.OnlineCPUs == 0 {
				t.Errorf("stats %s decoded zeros: %+v", c.Name, last)
			} else {
				t.Logf("    stats: cpus=%d mem=%d/%d rx=%d tx=%d blk=%d/%d pids=%d",
					last.OnlineCPUs, last.MemUsage-last.MemCache, last.MemLimit, last.NetRx, last.NetTx, last.BlkRead, last.BlkWrite, last.Pids)
			}
		}
	}

	images, err := eng.Images(ctx)
	if err != nil {
		t.Fatalf("images: %v", err)
	}
	for _, im := range images {
		d, err := eng.InspectImage(ctx, im.ID)
		if err != nil {
			t.Errorf("inspect image %s: %v", ShortID(im.ID), err)
			continue
		}
		validJSON("image "+ShortID(im.ID), d.Raw)
		t.Logf("image %s tags=%v size=%d dangling=%v os=%s/%s layers=%d", ShortID(im.ID), im.RepoTags, im.Size, im.Dangling, d.OS, d.Architecture, d.Layers)
	}

	volumes, err := eng.Volumes(ctx)
	if err != nil {
		t.Fatalf("volumes: %v", err)
	}
	for _, v := range volumes {
		d, err := eng.InspectVolume(ctx, v.Name)
		if err != nil {
			t.Errorf("inspect volume %s: %v", v.Name, err)
			continue
		}
		validJSON("volume "+v.Name, d.Raw)
		t.Logf("volume %s driver=%s anonymous=%v", v.Name, v.Driver, v.Anonymous())
	}

	networks, err := eng.Networks(ctx)
	if err != nil {
		t.Fatalf("networks: %v", err)
	}
	for _, n := range networks {
		d, err := eng.InspectNetwork(ctx, n.ID)
		if err != nil {
			t.Errorf("inspect network %s: %v", n.Name, err)
			continue
		}
		validJSON("network "+n.Name, d.Raw)
		t.Logf("network %-10s id=%s driver=%s subnets=%v endpoints=%d", n.Name, ShortID(n.ID), n.Driver, n.Subnets, len(d.Endpoints))
	}

	// The event stream must open and stay open (no events are expected).
	ectx, ecancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer ecancel()
	err = eng.Events(ectx, time.Time{}, func(Event) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("events: want the stream to stay open until the deadline, got %v", err)
	}

	if _, err := eng.InspectContainer(ctx, ghost); !IsNotFound(err) {
		t.Errorf("inspect of a missing container: want not-found, got %v", err)
	}
}
