package engine

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// TestDiskUsageAsTheDaemonStatesIt reads the account and checks it against
// what the fake computes, and that the only request for it is a GET.
func TestDiskUsageAsTheDaemonStatesIt(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	srv.ResetRequests()
	du, err := eng.DiskUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if du.Derived {
		t.Fatal("API 1.56 states its totals: nothing should be derived")
	}
	if du.Images.TotalCount != 14 || du.Images.ActiveCount != 10 || du.Images.TotalSize <= 0 {
		t.Fatalf("images: %+v", du.Images)
	}
	if du.Containers.TotalCount != 12 || du.Volumes.TotalCount != 7 || du.BuildCache.TotalCount != 4 {
		t.Fatalf("counts: %+v %+v %+v", du.Containers, du.Volumes, du.BuildCache)
	}
	if len(du.ImageItems) != 14 || len(du.VolumeItems) != 7 || len(du.BuildItems) != 4 {
		t.Fatalf("items: %d %d %d", len(du.ImageItems), len(du.VolumeItems), len(du.BuildItems))
	}
	for _, it := range du.ImageItems {
		if it.SharedSize < 0 || it.Unique() > it.Size {
			t.Fatalf("image %s: size %d shared %d", it.ID, it.Size, it.SharedSize)
		}
	}
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	for _, r := range reqs {
		if r.Mutating() {
			t.Fatalf("disk usage sent %s", r)
		}
		if r.Path == "/system/df" && !strings.Contains(r.Query, "verbose=1") {
			t.Fatalf("df without verbose: %s", r)
		}
	}
}

// TestLegacyDiskUsageIsMarkedDerived: a daemon older than API 1.52 states no
// totals. The client library sums them, and the account says so.
func TestLegacyDiskUsageIsMarkedDerived(t *testing.T) {
	_, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo(); s.AdvertiseAPI = "1.51" })
	_, cur := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	du, err := eng.DiskUsage(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if !du.Derived {
		t.Fatal("an API 1.51 account must be marked derived")
	}
	ref, err := cur.DiskUsage(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	// The same engine state either way: the sums equal what a current
	// daemon states. (Reclaimable images are summed by the same rule.)
	if du.Images.TotalSize != ref.Images.TotalSize || du.Containers.TotalSize != ref.Containers.TotalSize ||
		du.Volumes.TotalSize != ref.Volumes.TotalSize || du.BuildCache.TotalSize != ref.BuildCache.TotalSize {
		t.Fatalf("legacy %+v\ncurrent %+v", du, ref)
	}
}

func TestSizesAreOnlyMeasuredWhenAsked(t *testing.T) {
	srv, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	srv.ResetRequests()
	if _, err := eng.Containers(ctx); err != nil {
		t.Fatal(err)
	}
	if n := srv.CountRequests("GET", `^/containers/json$`); n != 1 {
		t.Fatalf("%d list requests", n)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r.Query, "size=") {
			t.Fatalf("an ordinary list asked for sizes: %s", r)
		}
	}
	id := srv.ContainerID("shop-api-1")
	one, err := eng.ContainerSizes(ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Name != "shop-api-1" || one[0].SizeRw != 18_400_000 || one[0].SizeRootFs <= one[0].SizeRw {
		t.Fatalf("one container: %+v", one)
	}
	all, err := eng.ContainerSizes(ctx, nil)
	if err != nil || len(all) != 12 {
		t.Fatalf("all: %d %v", len(all), err)
	}
}

func TestImageLayersAndSharedSizes(t *testing.T) {
	_, eng := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	ctx := ctxT(t)
	sizes, err := eng.ImageSizes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var worker ImageUsage
	for _, s := range sizes {
		for _, tag := range s.RepoTags {
			if tag == "shop/worker:dev" {
				worker = s
			}
		}
	}
	if worker.ID == "" || worker.SharedSize != 131_000_000 || worker.Unique() != worker.Size-131_000_000 {
		t.Fatalf("shop/worker:dev: %+v", worker)
	}
	l, err := eng.ImageLayers(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.DiffIDs) != 7 || len(l.History) != 8 || l.History[len(l.History)-1].Size != 0 || l.History[0].Size != 74_800_000 {
		t.Fatalf("layers %d, history %+v", len(l.DiffIDs), l.History)
	}
}
