// Package space accounts for the bytes images share, from figures the daemon
// reports (docs/DECISIONS.md, U2–U4).
//
// The daemon states each image's size and the part of it that other images
// share, but not with which images, nor what removing several together
// would free. Both follow from the image's layers: the daemon stores a
// layer under its chain ID, so two images hold the same bytes exactly where
// their chain IDs are equal, and it reports each layer's size in the image's
// history. This package joins the two. It reads nothing, guesses nothing,
// and says when a figure cannot be known.
package space

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
)

// ChainIDs returns the chain ID of every layer, bottom first: the first
// layer's diff ID, then the digest of the previous chain ID, a space and the
// next diff ID (OCI image specification, "ChainID").
func ChainIDs(diffIDs []string) []string {
	out := make([]string, len(diffIDs))
	for i, d := range diffIDs {
		if i == 0 {
			out[i] = d
			continue
		}
		sum := sha256.Sum256([]byte(out[i-1] + " " + d))
		out[i] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out
}

// LayerSizes maps an image's history, oldest entry first, onto its layers.
// The daemon gives the entries that made a layer that layer's size and the
// others 0, and says nothing else about which is which. So the mapping is
// made only when it cannot be wrong: when exactly as many entries have a
// size as there are layers, or when every entry made a layer. Otherwise ok
// is false and no size is attributed.
func LayerSizes(layers int, history []engine.HistoryEntry) (sizes []int64, ok bool) {
	var nonzero []int64
	for _, h := range history {
		if h.Size > 0 {
			nonzero = append(nonzero, h.Size)
		}
	}
	switch {
	case layers == 0:
		return nil, false
	case len(nonzero) == layers:
		return nonzero, true
	case len(history) == layers:
		sizes = make([]int64, layers)
		for i, h := range history {
			sizes[i] = h.Size
		}
		return sizes, true
	}
	return nil, false
}

// Image is what the index knows of one image.
type Image struct {
	ID string
	// Size and SharedSize are the daemon's (SharedSize -1 when not
	// computed).
	Size, SharedSize int64
	// Chains are the layers' chain IDs, bottom first; nil when the layers
	// could not be read.
	Chains []string
	// Sizes are the layers' sizes from the history; nil when it did not
	// map onto the layers.
	Sizes []int64
}

// Index is the sharing of layers among a set of images: every image the
// daemon listed at one moment.
type Index struct {
	images  map[string]*Image
	order   []string
	holders map[string][]string // chain → images holding it, sorted
	size    map[string]int64    // chain → bytes, where known
	agrees  map[string]bool
}

// Build indexes images. A layer's size is taken only from an image whose
// layer sizes add up to the daemon's own Size and SharedSize for it: where
// they do not (the containerd image store counts content the history does
// not), the daemon's figure stands and the layers stay unattributed.
func Build(images []Image) *Index {
	ix := &Index{images: map[string]*Image{}, holders: map[string][]string{}, size: map[string]int64{}, agrees: map[string]bool{}}
	for i := range images {
		img := images[i]
		if _, dup := ix.images[img.ID]; dup {
			continue
		}
		ix.images[img.ID] = &img
		ix.order = append(ix.order, img.ID)
		seen := map[string]bool{}
		for _, c := range img.Chains {
			if !seen[c] {
				seen[c] = true
				ix.holders[c] = append(ix.holders[c], img.ID)
			}
		}
	}
	for _, h := range ix.holders {
		sort.Strings(h)
	}
	for _, id := range ix.order {
		img := ix.images[id]
		if img.Chains == nil || len(img.Sizes) != len(img.Chains) {
			continue
		}
		var total, shared int64
		for i, c := range img.Chains {
			total += img.Sizes[i]
			if len(ix.holders[c]) > 1 {
				shared += img.Sizes[i]
			}
		}
		if total == img.Size && (img.SharedSize < 0 || shared == img.SharedSize) {
			ix.agrees[id] = true
		}
	}
	for _, id := range ix.order {
		if !ix.agrees[id] {
			continue
		}
		img := ix.images[id]
		for i, c := range img.Chains {
			if _, ok := ix.size[c]; !ok {
				ix.size[c] = img.Sizes[i]
			}
		}
	}
	return ix
}

// Has reports whether the image is in the index.
func (ix *Index) Has(id string) bool { _, ok := ix.images[id]; return ok }

// Indexed reports whether the image's layers are known.
func (ix *Index) Indexed(id string) bool {
	img, ok := ix.images[id]
	return ok && img.Chains != nil
}

// Agrees reports whether the image's layer sizes add up to the daemon's
// figures for it.
func (ix *Index) Agrees(id string) bool { return ix.agrees[id] }

// Disagreeing returns the indexed images whose layer sizes did not map, or
// did not add up to the daemon's figures.
func (ix *Index) Disagreeing() []string {
	var out []string
	for _, id := range ix.order {
		if ix.Indexed(id) && !ix.agrees[id] {
			out = append(out, id)
		}
	}
	return out
}

// Unindexed returns the images whose layers are not known.
func (ix *Index) Unindexed() []string {
	var out []string
	for _, id := range ix.order {
		if !ix.Indexed(id) {
			out = append(out, id)
		}
	}
	return out
}

// Sharers returns the other images holding at least one layer of id, in
// index order.
func (ix *Index) Sharers(id string) []string {
	img, ok := ix.images[id]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, c := range img.Chains {
		for _, h := range ix.holders[c] {
			if h != id {
				set[h] = true
			}
		}
	}
	var out []string
	for _, o := range ix.order {
		if set[o] {
			out = append(out, o)
		}
	}
	return out
}

// Group is the bytes held by exactly one set of two or more images. Every
// shared layer is in exactly one group, so no byte is counted twice.
type Group struct {
	Holders []string
	Bytes   int64 // of the layers whose size is known
	Layers  int
	// Unknown counts layers of the group whose size is not known.
	Unknown int
}

// Groups returns the shared layers grouped by the exact set of images that
// hold them, largest first.
func (ix *Index) Groups() []Group {
	by := map[string]*Group{}
	for c, h := range ix.holders {
		if len(h) < 2 {
			continue
		}
		key := strings.Join(h, ",")
		g := by[key]
		if g == nil {
			g = &Group{Holders: h}
			by[key] = g
		}
		g.Layers++
		if n, ok := ix.size[c]; ok {
			g.Bytes += n
		} else {
			g.Unknown++
		}
	}
	out := make([]Group, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return strings.Join(out[i].Holders, ",") < strings.Join(out[j].Holders, ",")
	})
	return out
}

// GroupsOf returns the groups an image belongs to.
func (ix *Index) GroupsOf(id string) []Group {
	var out []Group
	for _, g := range ix.Groups() {
		for _, h := range g.Holders {
			if h == id {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// Freed is what removing a set of images frees.
type Freed struct {
	// Bytes is exact when Exact is set, and otherwise at least this much
	// (the sum of the members' unique sizes, as the daemon states them).
	Bytes int64
	Exact bool
	// Known is false when not even a lower bound can be given.
	Known bool
	// Reason says why the figure is not exact, or not known.
	Reason string
}

// Frees returns what removing every image of set frees: the bytes of every
// layer all of whose holders are in the set. Layers that an image outside
// the set also holds stay.
func (ix *Index) Frees(set []string) Freed {
	in := map[string]bool{}
	for _, id := range set {
		in[id] = true
	}
	// The members' unique bytes are freed whatever else is true: the
	// daemon's own figures, and the floor of every other answer.
	var floor int64
	floorKnown := true
	for id := range in {
		img, ok := ix.images[id]
		if !ok || img.SharedSize < 0 {
			floorKnown = false
			continue
		}
		floor += img.Size - img.SharedSize
	}
	if un := ix.Unindexed(); len(un) > 0 {
		return Freed{Bytes: floor, Known: floorKnown, Reason: "the layers of " + plural(len(un), "image were", "images were") + " not read, so what they share is not known"}
	}
	var bytes int64
	unknown := 0
	seen := map[string]bool{}
	for id := range in {
		img, ok := ix.images[id]
		if !ok {
			continue
		}
		for _, c := range img.Chains {
			if seen[c] {
				continue
			}
			seen[c] = true
			all := true
			for _, h := range ix.holders[c] {
				all = all && in[h]
			}
			if !all {
				continue
			}
			if n, ok := ix.size[c]; ok {
				bytes += n
			} else {
				unknown++
			}
		}
	}
	if unknown > 0 {
		return Freed{Bytes: floor, Known: floorKnown, Reason: plural(unknown, "shared layer has", "shared layers have") + " no size the daemon's own figures confirm"}
	}
	return Freed{Bytes: bytes, Exact: true, Known: true}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
