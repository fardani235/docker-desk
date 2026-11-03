package fakeengine

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
)

// Layer is one layer of a fake image. Layers are stored, as by the real
// daemon, under their chain ID: two images share a layer exactly when the
// layers below it are the same too. Name stands in for the layer's content;
// equal names are equal diff IDs.
type Layer struct {
	Name      string
	Size      int64
	CreatedBy string
}

func (l Layer) diffID() string { return "sha256:" + stableID("layer", l.Name) }

// chainIDs returns the chain ID of every layer: the first layer's diff ID,
// then sha256 of the previous chain ID, a space and the next diff ID — the
// rule of the OCI image specification.
func chainIDs(layers []Layer) []string {
	out := make([]string, len(layers))
	for i, l := range layers {
		if i == 0 {
			out[i] = l.diffID()
			continue
		}
		sum := sha256.Sum256([]byte(out[i-1] + " " + l.diffID()))
		out[i] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out
}

// layersLocked returns the image's layers. An image seeded with a size alone
// has two layers of its own that add up to it.
func (img *Image) layersLocked() []Layer {
	if img.layers != nil {
		return img.layers
	}
	first := img.Size / 3
	return []Layer{
		{Name: img.ID + "0", Size: first, CreatedBy: "/bin/sh -c #(nop) ADD file:" + short(img.ID) + " in / "},
		{Name: img.ID + "1", Size: img.Size - first, CreatedBy: "COPY . /app # buildkit"},
	}
}

// SetImageLayers gives the image a reference or ID resolves to these layers,
// bottom first, and sets its size to their sum. It reports whether the image
// exists.
func (s *Server) SetImageLayers(ref string, layers ...Layer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	img := s.findImageLocked(ref)
	if img == nil {
		return false
	}
	img.layers = append([]Layer(nil), layers...)
	img.Size = 0
	for _, l := range layers {
		img.Size += l.Size
	}
	return true
}

// chainUseLocked maps every chain ID to its size and to the images holding it.
func (s *Server) chainUseLocked() (size map[string]int64, holders map[string]int) {
	size, holders = map[string]int64{}, map[string]int{}
	for _, img := range s.images {
		layers := img.layersLocked()
		for i, c := range chainIDs(layers) {
			size[c] = layers[i].Size
			holders[c]++
		}
	}
	return size, holders
}

// sharedSizeLocked is what the daemon reports as SharedSize: the bytes of the
// image's layers that at least one other image holds as well.
func (s *Server) sharedSizeLocked(img *Image, holders map[string]int) int64 {
	layers := img.layersLocked()
	var n int64
	for i, c := range chainIDs(layers) {
		if holders[c] > 1 {
			n += layers[i].Size
		}
	}
	return n
}

func (s *Server) handleImageHistory(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	img := s.findImageLocked(ref)
	if img == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such image: %s", ref)
		return
	}
	layers := img.layersLocked()
	created := img.Created
	id := img.ID
	s.mu.Unlock()

	// Newest first, as the daemon lists it, with the entry for CMD on top:
	// it made no layer, and its size is 0.
	out := []image.HistoryResponseItem{{
		ID: "sha256:" + id, Created: created.Unix(), CreatedBy: `/bin/sh -c #(nop)  CMD ["/bin/app"]`, Tags: []string{},
	}}
	for i := len(layers) - 1; i >= 0; i-- {
		out = append(out, image.HistoryResponseItem{
			ID: "<missing>", Created: created.Unix(), CreatedBy: layers[i].CreatedBy, Size: layers[i].Size, Tags: []string{},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// writableSize is a container's writable layer: set by the seed, or a stable
// figure derived from its name.
func (c *Container) writableSize() int64 {
	if c.SizeRw > 0 {
		return c.SizeRw
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(c.Name))
	return 8_192 + int64(h.Sum32()%24_000_000)
}

// BuildRecord is one build-cache record of the fake.
type BuildRecord struct {
	ID          string
	Type        string
	Description string
	Size        int64
	InUse       bool
	Shared      bool
	LastUsed    time.Time
}

// AddBuildCache seeds build-cache records. The fake has no builder: they are
// only reported, by GET /system/df.
func (s *Server) AddBuildCache(records ...BuildRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buildCache = append(s.buildCache, records...)
}

// SetVolumeSize sets the bytes a volume holds, as /system/df measures them.
func (s *Server) SetVolumeSize(name string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.volumes[name]; ok {
		v.Size = size
	}
}

// SetWritableSize sets a container's writable layer.
func (s *Server) SetWritableSize(name string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.findContainerLocked(name); c != nil {
		c.SizeRw = size
	}
}

// apiVersionOf returns the version a request's path names ("1.56"), or the
// fake's own when it names none.
func apiVersionOf(r *http.Request) string {
	if v := versionPrefix.FindString(r.URL.Path); v != "" {
		return strings.TrimPrefix(v, "/v")
	}
	return APIVersion
}

func versionLess(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	if len(pa) != 2 || len(pb) != 2 {
		return false
	}
	ma, _ := strconv.Atoi(pa[0])
	mb, _ := strconv.Atoi(pb[0])
	na, _ := strconv.Atoi(pa[1])
	nb, _ := strconv.Atoi(pb[1])
	if ma != mb {
		return ma < mb
	}
	return na < nb
}

// waitLocked pauses a request the way a daemon walking filesystems would,
// without holding the lock. It reports false if the request went away.
func (s *Server) wait(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-time.After(d):
		return true
	case <-r.Context().Done():
		return false
	case <-s.closed:
		return false
	}
}

// handleDiskUsage serves GET /system/df the way the daemon does: totals and,
// with verbose, the objects — or, to a client older than API 1.52, the
// legacy shape with lists only, from which the client sums the totals.
func (s *Server) handleDiskUsage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delay := s.DiskUsageDelay
	s.mu.Unlock()
	if !s.wait(r, delay) {
		return
	}
	q := r.URL.Query()
	types := map[string]bool{}
	for _, t := range q["type"] {
		types[t] = true
	}
	want := func(t system.DiskUsageObject) bool { return len(types) == 0 || types[string(t)] }
	verbose := boolParam(r, "verbose")
	legacy := versionLess(apiVersionOf(r), "1.52")

	s.mu.Lock()
	defer s.mu.Unlock()
	sizes, holders := s.chainUseLocked()

	var imgs []image.Summary
	var iu image.DiskUsage
	for _, img := range s.sortedImagesLocked() {
		users := int64(len(s.imageUsersLocked(img.ID)))
		shared := s.sharedSizeLocked(img, holders)
		imgs = append(imgs, image.Summary{
			ID: "sha256:" + img.ID, RepoTags: append([]string{}, img.RepoTags...), RepoDigests: img.digests(),
			Created: img.Created.Unix(), Size: img.Size, SharedSize: shared, Containers: users, Labels: img.Labels,
		})
		iu.TotalCount++
		if users > 0 {
			iu.ActiveCount++
		} else {
			iu.Reclaimable += img.Size - shared
		}
	}
	for _, n := range sizes {
		iu.TotalSize += n
	}

	var ctrs []container.Summary
	var cu container.DiskUsage
	for _, c := range s.sortedContainersLocked() {
		sum := c.summary()
		sum.SizeRw = c.writableSize()
		if img := s.images[c.ImageID]; img != nil {
			sum.SizeRootFs = sum.SizeRw + img.Size
		}
		ctrs = append(ctrs, sum)
		cu.TotalCount++
		cu.TotalSize += sum.SizeRw
		if c.active() {
			cu.ActiveCount++
		} else {
			cu.Reclaimable += sum.SizeRw
		}
	}

	var vols []volume.Volume
	var vu volume.DiskUsage
	names := make([]string, 0, len(s.volumes))
	for n := range s.volumes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v := s.volumes[n].api()
		refs := int64(len(s.volumeUsersLocked(n)))
		v.UsageData = &volume.UsageData{Size: s.volumes[n].Size, RefCount: refs}
		vols = append(vols, v)
		vu.TotalCount++
		vu.TotalSize += s.volumes[n].Size
		if refs > 0 {
			vu.ActiveCount++
		} else {
			vu.Reclaimable += s.volumes[n].Size
		}
	}

	var recs []build.CacheRecord
	var bu build.DiskUsage
	for _, b := range s.buildCache {
		last := b.LastUsed
		recs = append(recs, build.CacheRecord{
			ID: b.ID, Type: b.Type, Description: b.Description, Size: b.Size,
			InUse: b.InUse, Shared: b.Shared, CreatedAt: last, LastUsedAt: &last, UsageCount: 1,
		})
		bu.TotalCount++
		if !b.Shared {
			bu.TotalSize += b.Size
		}
		if b.InUse {
			bu.ActiveCount++
		} else if !b.Shared {
			bu.Reclaimable += b.Size
		}
	}

	if legacy {
		out := map[string]any{}
		if want(system.ImageObject) {
			out["LayersSize"] = iu.TotalSize
			out["Images"] = nonNil(imgs)
		}
		if want(system.ContainerObject) {
			out["Containers"] = nonNil(ctrs)
		}
		if want(system.VolumeObject) {
			out["Volumes"] = nonNil(vols)
		}
		if want(system.BuildCacheObject) {
			out["BuildCache"] = nonNil(recs)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var du system.DiskUsage
	if want(system.ImageObject) {
		if verbose {
			iu.Items = imgs
		}
		du.ImageUsage = &iu
	}
	if want(system.ContainerObject) {
		if verbose {
			cu.Items = ctrs
		}
		du.ContainerUsage = &cu
	}
	if want(system.VolumeObject) {
		if verbose {
			vu.Items = vols
		}
		du.VolumeUsage = &vu
	}
	if want(system.BuildCacheObject) {
		if verbose {
			bu.Items = recs
		}
		du.BuildCacheUsage = &bu
	}
	writeJSON(w, http.StatusOK, du)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
