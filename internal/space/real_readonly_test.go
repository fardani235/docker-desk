package space

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

// TestRealDaemonDiskUsageReadOnly is opt-in, like the engine's real-daemon
// checks (`make real-readonly-check`): it reads a real daemon's disk account
// and every image's layers through a read-only engine — GET and HEAD only,
// refused below the wrapper too — and reports how much of the sharing the
// daemon's own per-layer figures can attribute. It logs counts and sizes,
// never names.
func TestRealDaemonDiskUsageReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if !engine.IsReadOnly(eng) {
		t.Fatal("engine is not read-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	start := time.Now()
	du, err := eng.DiskUsage(ctx)
	if err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	t.Logf("df in %s, derived=%v: images %d (%d B, %d B reclaimable), containers %d, volumes %d, build cache %d",
		time.Since(start).Round(time.Millisecond), du.Derived, du.Images.TotalCount, du.Images.TotalSize, du.Images.Reclaimable,
		du.Containers.TotalCount, du.Volumes.TotalCount, du.BuildCache.TotalCount)

	var imgs []Image
	mapped := 0
	for _, it := range du.ImageItems {
		l, err := eng.ImageLayers(ctx, it.ID)
		if err != nil {
			t.Logf("layers of one image could not be read: %v", err)
			imgs = append(imgs, Image{ID: it.ID, Size: it.Size, SharedSize: it.SharedSize})
			continue
		}
		img := Image{ID: it.ID, Size: it.Size, SharedSize: it.SharedSize, Chains: ChainIDs(l.DiffIDs)}
		if sizes, ok := LayerSizes(len(l.DiffIDs), l.History); ok {
			img.Sizes = sizes
			mapped++
		}
		imgs = append(imgs, img)
	}
	ix := Build(imgs)
	if os.Getenv("DD_SPACE_DETAIL") != "" {
		for _, img := range imgs {
			var sum, shared int64
			for i, c := range img.Chains {
				if i < len(img.Sizes) {
					sum += img.Sizes[i]
					if len(ix.holders[c]) > 1 {
						shared += img.Sizes[i]
					}
				}
			}
			t.Logf("size %d shared %d | layers %d sum %d shared-by-chain %d | size-sum %d", img.Size, img.SharedSize, len(img.Chains), sum, shared, img.Size-sum)
		}
	}
	var unique, grouped int64
	agree := 0
	for _, img := range imgs {
		if ix.Agrees(img.ID) {
			agree++
		}
		if img.SharedSize >= 0 {
			unique += img.Size - img.SharedSize
		}
	}
	for _, g := range ix.Groups() {
		grouped += g.Bytes
	}
	t.Logf("%d images: history mapped for %d, layer sizes agree with the daemon for %d; %d shared groups",
		len(imgs), mapped, agree, len(ix.Groups()))
	t.Logf("unique %d B + attributed shared %d B = %d B; the daemon's image total is %d B", unique, grouped, unique+grouped, du.Images.TotalSize)
	if agree == len(imgs) && len(imgs) > 0 && unique+grouped != du.Images.TotalSize {
		t.Errorf("every image agrees with the daemon, yet unique and shared bytes do not add up to its total")
	}
}
