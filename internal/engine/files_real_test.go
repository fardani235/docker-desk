package engine_test

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/forensics"
)

// TestRealDaemonFilesReadOnly is the Files view's two reads against a real
// daemon, through the read-only engine (whose HTTP hook refuses anything
// but GET and HEAD before it is sent): the smallest image's archive read in
// ModeFiles and merged, and every container's change list compared, line
// for line, with what the real `docker diff` prints for it — both sorted,
// since the daemon's own order differs from one call to the next.
//
//	DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
//	go test ./internal/engine/ -run TestRealDaemonFilesReadOnly -v
func TestRealDaemonFilesReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST to run the read-only Files check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	imgs, err := eng.Images(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) > 0 {
		sort.Slice(imgs, func(i, j int) bool { return imgs[i].Size < imgs[j].Size })
		img := imgs[0]
		d, err := eng.InspectImage(ctx, img.ID)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		rc, err := eng.SaveImage(ctx, d.ID, d.Platform())
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		rep, err := forensics.Analyse(ctx, rc, forensics.Options{Mode: forensics.ModeFiles, ImageID: d.ID, Platform: d.Platform().String()}, nil)
		rc.Close()
		if err != nil {
			t.Fatalf("analyse: %v", err)
		}
		ix := files.Build(rep)
		if len(rep.Findings) != 0 || rep.Packages != nil || ix == nil {
			t.Fatalf("ModeFiles made findings or packages, or no index")
		}
		t.Logf("image %s (%s, daemon size %s): %s layout, %d layers (inspect says %d), %s read in %v",
			engine.ShortID(d.ID), strings.Join(img.Refs(), " "), files.Bytes(img.Size), rep.Layout, len(rep.Layers), d.Layers,
			files.Bytes(rep.BytesRead), time.Since(start).Round(time.Millisecond))
		for _, l := range ix.Totals(img.Size).Lines() {
			t.Logf("  %-9s %s", l.Label, l.Text)
		}
		if len(rep.Layers) != d.Layers {
			t.Errorf("the archive has %d layers, inspect %d", len(rep.Layers), d.Layers)
		}
		if ix.Entries == 0 {
			t.Error("an empty tree")
		}
	}

	cs, err := eng.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		var got, want string
		// A running container can write between the two reads: ask again
		// before calling it a disagreement.
		for try := 0; try < 3; try++ {
			changes, err := eng.ContainerChanges(ctx, c.ID)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			// The daemon lists changes in no fixed order — two calls can
			// differ — and the CLI prints whichever order it got; so both
			// sides are compared sorted. The view sorts them too.
			var lines []string
			for _, ch := range changes {
				lines = append(lines, ch.Kind.Letter()+" "+ch.Path)
			}
			sort.Strings(lines)
			got = strings.Join(lines, "\n")
			out, err := exec.CommandContext(ctx, "docker", "-H", host, "diff", c.ID).Output()
			if err != nil {
				t.Skipf("the docker CLI could not be run: %v", err)
			}
			cli := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if len(cli) == 1 && cli[0] == "" {
				cli = nil
			}
			sort.Strings(cli)
			want = strings.Join(cli, "\n")
			if got == want {
				break
			}
		}
		n := 0
		if got != "" {
			n = strings.Count(got, "\n") + 1
		}
		t.Logf("container %s (%s): %d changes; docker diff agrees: %v", c.Name, c.State, n, got == want)
		if got != want {
			t.Errorf("%s: ContainerChanges and docker diff disagree:\n got %q\nwant %q", c.Name, got, want)
		}
	}
}
