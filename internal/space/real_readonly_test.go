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

// TestRealDaemonLayersReadOnly is opt-in in the same way. It reads every
// image's layers through a read-only engine and reports what Images ›
// Layers rests on (docs/DECISIONS.md, LY3 and LY4): how often the daemon's
// history binds to the layers by counting sizes, and how the holders of a
// layer differ when layers are compared by content instead of by chain. It
// logs counts, never names, and asserts the one thing that must hold on
// any engine: an image that shares a chain with another shares that
// layer's content with it too.
func TestRealDaemonLayersReadOnly(t *testing.T) {
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
	list, err := eng.Images(ctx)
	if err != nil {
		t.Fatalf("images: %v", err)
	}
	var imgs []Image
	counted, unread := 0, 0
	for i, it := range list {
		l, err := eng.ImageLayers(ctx, it.ID)
		if err != nil {
			unread++
			imgs = append(imgs, Image{ID: it.ID, SharedSize: -1})
			continue
		}
		sized := 0
		for _, h := range l.History {
			if h.Size > 0 {
				sized++
			}
		}
		binds := len(l.DiffIDs) > 0 && sized == len(l.DiffIDs)
		if binds {
			counted++
		}
		t.Logf("image %d: %d layers, %d history entries, %d with a size, binds by counting: %v", i+1, len(l.DiffIDs), len(l.History), sized, binds)
		imgs = append(imgs, Image{ID: it.ID, SharedSize: -1, Chains: ChainIDs(l.DiffIDs), Diffs: l.DiffIDs})
	}
	ix := Build(imgs)
	byChain, byContent, repeated := 0, 0, 0
	for _, img := range imgs {
		if img.Diffs == nil {
			continue
		}
		chain := map[string]bool{}
		for _, s := range ix.Sharers(img.ID) {
			chain[s] = true
		}
		content := map[string]bool{}
		shared := 0
		for _, hs := range ix.ContentSharers(img.ID) {
			if len(hs) > 0 {
				shared++
			}
			for _, h := range hs {
				content[h] = true
			}
		}
		seen := map[string]bool{}
		for _, d := range img.Diffs {
			if seen[d] {
				repeated++
				break
			}
			seen[d] = true
		}
		for s := range chain {
			if !content[s] {
				t.Errorf("an image shares a chain with another and no layer content with it")
			}
		}
		if len(chain) > 0 {
			byChain++
		}
		if len(content) > 0 {
			byContent++
		}
		t.Logf("%d layers: %d held elsewhere by content; %d other images by chain, %d by content", len(img.Diffs), shared, len(chain), len(content))
	}
	t.Logf("%d images (%d unread): the history binds by counting for %d; %d share a layer by chain, %d by content; %d hold one diff ID more than once",
		len(imgs), unread, counted, byChain, byContent, repeated)
}
