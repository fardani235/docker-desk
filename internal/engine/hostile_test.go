package engine

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// The hostile fixture reaches the engine's decoded types as it was seeded:
// the engine does not escape, drop or repair a daemon's strings (the desk
// escapes where it draws, and sends back what it was given). The one
// exception is JSON's own: a byte that is not UTF-8 cannot be carried, and
// is U+FFFD by the time anything is decoded.
func TestHostileFixtureReachesTheDecodedTypes(t *testing.T) {
	var h fakeengine.Hostile
	_, eng := startFake(t, func(s *fakeengine.Server) { h = s.SeedHostile() })
	ctx := ctxT(t)

	cs, err := eng.Containers(ctx)
	if err != nil || len(cs) != 1 {
		t.Fatalf("containers: %v, %d", err, len(cs))
	}
	c := cs[0]
	d, err := eng.InspectContainer(ctx, h.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	var mountDest string
	for _, m := range c.Mounts {
		if m.Type == "bind" {
			mountDest = m.Destination
		}
	}
	for _, f := range []struct{ field, got, want, carries string }{
		{"name", c.Name, h.Name, fakeengine.HostileOSC},
		{"label note", c.Labels["note"], h.Label, fakeengine.HostileConceal},
		{"label path", c.Labels["path"], h.Backslash, `\`},
		{"mount destination", mountDest, h.MountDest, fakeengine.HostileNewline},
		{"compose project label", c.Project(), h.Project, fakeengine.HostileC1},
		{"compose service label", c.Service(), h.Service, fakeengine.HostileConceal},
		{"working directory", d.WorkingDir, h.WorkingDirDecoded, "�"},
		{"command", strings.Join(d.Cmd, " "), strings.Join(h.Command, " "), fakeengine.HostileRLO},
		{"image tag", c.Image, h.ImageRef, fakeengine.HostileColour},
		{"user", d.User, h.User, fakeengine.HostileConceal},
		{"health", c.Health, h.Health, fakeengine.HostileConceal},
		{"state error", d.Error, h.StateError, fakeengine.HostileConceal},
	} {
		t.Logf("%-22s %q", f.field, f.got)
		if f.got != f.want {
			t.Errorf("%s: decoded %q, seeded %q", f.field, f.got, f.want)
		}
		if !strings.Contains(f.got, f.carries) {
			t.Errorf("%s: %q does not carry %q", f.field, f.got, f.carries)
		}
	}
	// The byte that is no character was seeded, and JSON did not carry it.
	if !strings.Contains(h.WorkingDir, fakeengine.HostileInvalidUTF8) || strings.Contains(d.WorkingDir, fakeengine.HostileInvalidUTF8) {
		t.Errorf("working directory: seeded %q, decoded %q", h.WorkingDir, d.WorkingDir)
	}

	imgs, err := eng.Images(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, img := range imgs {
		for _, r := range img.Refs() {
			if r == h.ImageRef {
				found = true
				t.Logf("%-22s %q", "image label", img.Labels["maintainer"])
				if img.Labels["maintainer"] != h.ImageLabel {
					t.Errorf("image label: %q", img.Labels["maintainer"])
				}
			}
		}
	}
	if !found {
		t.Errorf("no image is listed under %q", h.ImageRef)
	}
	vols, err := eng.Volumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, v := range vols {
		if v.Name == h.Volume {
			found = true
			t.Logf("%-22s %q", "volume label", v.Labels["owner"])
			if v.Labels["owner"] != h.VolumeLabel {
				t.Errorf("volume label: %q", v.Labels["owner"])
			}
		}
	}
	if !found {
		t.Errorf("no volume is listed under %q", h.Volume)
	}
	nets, err := eng.Networks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, n := range nets {
		if n.Name == h.Network {
			found = true
			t.Logf("%-22s %q", "network label", n.Labels["zone"])
			if n.Labels["zone"] != h.NetworkLabel || n.ID != h.NetworkID {
				t.Errorf("network: label %q, id %q", n.Labels["zone"], n.ID)
			}
		}
	}
	if !found {
		t.Errorf("no network is listed under %q", h.Network)
	}
}

// The hostile fixture is a test's, not the sandbox's: the demo seed holds
// nothing of it, so the captured screens stay as they are.
func TestHostileFixtureIsNotInTheDemoSeed(t *testing.T) {
	_, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	cs, err := eng.Containers(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		for _, s := range append([]string{c.Name, c.Image, c.Command}, values(c.Labels)...) {
			if strings.ContainsAny(s, "\x1b\n\u009b\u202e\\") {
				t.Errorf("the demo seed's %s carries a hostile value: %q", c.Name, s)
			}
		}
	}
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
