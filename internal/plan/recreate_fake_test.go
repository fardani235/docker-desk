package plan

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// A container whose named volume carries the daemon's default mode is
// re-creatable. The fake reports the mode the daemon reports, so this test
// fails if the clone treats the default as an SELinux relabel.
func TestCloneCarriesADefaultVolumeFromTheFake(t *testing.T) {
	srv := fakeengine.New()
	srv.AddImage(7_000_000, time.Hour, "shop/web:2")
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := eng.CreateContainer(ctx, engine.ContainerCreateOptions{
		Image:  "shop/web:2",
		Name:   "web",
		Mounts: []engine.MountOption{{Type: "volume", Source: "web-data", Target: "/data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.InspectContainer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Raw), `"Mode":"z"`) {
		t.Fatalf("the fake does not report the daemon's default mode: %s", d.Raw)
	}
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec, refused, err := Clone(d, engine.ImageDetail{Raw: []byte(`{}`)}, DefaultsFrom(info))
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 0 {
		t.Fatalf("refused: %v", refused)
	}
	want := []MountSpec{{Type: "volume", Source: "web-data", Destination: "/data"}}
	if !reflect.DeepEqual(spec.Mounts, want) {
		t.Errorf("mounts %+v; want %+v", spec.Mounts, want)
	}
}
