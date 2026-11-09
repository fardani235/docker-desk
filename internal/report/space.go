package report

import (
	"fmt"
	"sort"
	"strings"
)

// SpaceSchema names the native schema of a Space document.
const SpaceSchema = "docker-desk/space-report/v1"

// Measure says how far a figure can be trusted.
type Measure string

const (
	// Exact: the figure is the bytes.
	Exact Measure = "exact"
	// AtLeast: the bytes are at least the figure; Reason says why no more
	// is known.
	AtLeast Measure = "at least"
	// Unknown: not even a lower bound is known; Reason says why.
	Unknown Measure = "unknown"
)

// Figure is a number of bytes with its measure, its source and, unless it
// is exact, the reason it is not.
type Figure struct {
	Bytes   int64   `json:"bytes"`
	Measure Measure `json:"measure"`
	Source  string  `json:"source"`
	Reason  string  `json:"reason,omitempty"`
}

// Space is the Space view's account: the daemon's totals, every image's
// size with what it frees alone and together with the images sharing its
// layers, the groups of shared layers, the volumes, and the writable
// layers when they were measured.
type Space struct {
	Schema      string `json:"schema"`
	Tool        Tool   `json:"tool"`
	ToolVersion string `json:"-"`

	// TotalsSource is the daemon's, or the client library's sums of its
	// lists (API < 1.52).
	TotalsSource string       `json:"totals_source"`
	Totals       []SpaceTotal `json:"totals"`
	// Stale is set when the engine changed after the account was taken.
	Stale bool `json:"stale"`

	Images     []SpaceImage     `json:"images"`
	Groups     []SpaceGroup     `json:"shared_groups"`
	Volumes    []SpaceVolume    `json:"volumes"`
	Writable   *SpaceWritable   `json:"writable_layers"`
	BuildCache []SpaceBuildItem `json:"build_cache"`
	Notes      []string         `json:"notes"`
}

// SpaceTotal is one row of `docker system df`.
type SpaceTotal struct {
	Type        string `json:"type"`
	Count       int64  `json:"count"`
	Active      int64  `json:"active"`
	Size        int64  `json:"size"`
	Reclaimable int64  `json:"reclaimable"`
}

// SpaceImage is one image of the account. Shared is nil when the daemon
// did not compute it.
type SpaceImage struct {
	ID         string   `json:"id"`
	References []string `json:"references"`
	Size       int64    `json:"size"`
	Shared     *int64   `json:"shared"`
	Containers int64    `json:"containers"`
	// Layers is "attributed" (sizes confirmed by the daemon's figures),
	// "unattributed" (read; sizes do not map or add up) or "not read".
	Layers string `json:"layers"`
	// Alone is what removing the image alone frees; Together, what
	// removing it with every image sharing a layer frees, and Sharers
	// those images (nil when none do, or the layers were not read).
	Alone    Figure   `json:"alone"`
	Together *Figure  `json:"together"`
	Sharers  []string `json:"sharers"`
}

// SpaceGroup is the bytes held by exactly one set of images.
type SpaceGroup struct {
	Images  []string `json:"images"`
	Layers  int      `json:"layers"`
	Unsized int      `json:"layers_unsized"`
	Size    Figure   `json:"size"`
}

// SpaceVolume is one local volume. Size is nil when the daemon could not
// measure it; Links when it did not count them.
type SpaceVolume struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	Size   *int64 `json:"size"`
	Links  *int64 `json:"links"`
}

// SpaceWritable is the containers' writable layers, measured with m.
type SpaceWritable struct {
	Stale      bool             `json:"stale"`
	Containers []SpaceContainer `json:"containers"`
}

// SpaceContainer is one container's writable layer.
type SpaceContainer struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Writable int64  `json:"writable"`
	Virtual  int64  `json:"virtual"`
}

// SpaceBuildItem is one build-cache record, as the daemon lists it.
type SpaceBuildItem struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Size        int64  `json:"size"`
	InUse       bool   `json:"in_use"`
	Shared      bool   `json:"shared"`
}

var totalRank = map[string]int{"Images": 0, "Containers": 1, "Local Volumes": 2, "Build Cache": 3}

func joinKey(s []string) string { return strings.Join(s, "\x00") }

func label(img SpaceImage) string {
	if len(img.References) > 0 {
		return img.References[0]
	}
	return img.ID
}

func normaliseSpace(s Space) Space {
	out := s
	out.Schema = SpaceSchema
	out.Tool = toolOf(s.ToolVersion)

	out.Totals = append([]SpaceTotal{}, s.Totals...)
	sort.SliceStable(out.Totals, func(i, j int) bool {
		ri, iok := totalRank[out.Totals[i].Type]
		rj, jok := totalRank[out.Totals[j].Type]
		if iok != jok {
			return iok
		}
		if ri != rj {
			return ri < rj
		}
		return out.Totals[i].Type < out.Totals[j].Type
	})

	out.Images = make([]SpaceImage, len(s.Images))
	for i, img := range s.Images {
		img.References = sorted(img.References)
		img.Sharers = sorted(img.Sharers)
		if img.Shared != nil {
			v := *img.Shared
			img.Shared = &v
		}
		if img.Together != nil {
			f := *img.Together
			img.Together = &f
		}
		out.Images[i] = img
	}
	sort.SliceStable(out.Images, func(i, j int) bool {
		a, b := out.Images[i], out.Images[j]
		return less([]string{label(a), a.ID}, []string{label(b), b.ID})
	})

	out.Groups = make([]SpaceGroup, len(s.Groups))
	for i, g := range s.Groups {
		g.Images = sorted(g.Images)
		out.Groups[i] = g
	}
	sort.SliceStable(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.Size.Bytes != b.Size.Bytes {
			return a.Size.Bytes > b.Size.Bytes // largest first, as the view
		}
		return joinKey(a.Images) < joinKey(b.Images)
	})

	out.Volumes = make([]SpaceVolume, len(s.Volumes))
	for i, v := range s.Volumes {
		if v.Size != nil {
			x := *v.Size
			v.Size = &x
		}
		if v.Links != nil {
			x := *v.Links
			v.Links = &x
		}
		out.Volumes[i] = v
	}
	sort.SliceStable(out.Volumes, func(i, j int) bool { return out.Volumes[i].Name < out.Volumes[j].Name })

	if s.Writable != nil {
		w := *s.Writable
		w.Containers = append([]SpaceContainer{}, s.Writable.Containers...)
		sort.SliceStable(w.Containers, func(i, j int) bool {
			a, b := w.Containers[i], w.Containers[j]
			return less([]string{a.Name, a.State}, []string{b.Name, b.State})
		})
		out.Writable = &w
	}

	out.BuildCache = append([]SpaceBuildItem{}, s.BuildCache...)
	sort.SliceStable(out.BuildCache, func(i, j int) bool {
		a, b := out.BuildCache[i], out.BuildCache[j]
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		return less([]string{a.Type, a.Description}, []string{b.Type, b.Description})
	})
	out.Notes = sorted(s.Notes)
	return out
}

// SpaceJSON is the native document of the Space account.
func SpaceJSON(s Space) ([]byte, error) {
	return marshal(normaliseSpace(s))
}

func figureText(f Figure) string {
	switch f.Measure {
	case Exact:
		return fmt.Sprintf("exact\t%d", f.Bytes)
	case AtLeast:
		return fmt.Sprintf("at least\t%d", f.Bytes)
	}
	return "unknown\t-"
}

// SpaceText is the totals, then the images, then the shared groups, one
// line each, tab-separated; every figure says whether it is exact or at
// least, and why when it is not.
func SpaceText(s Space) []byte {
	n := normaliseSpace(s)
	var b strings.Builder
	fmt.Fprintf(&b, "# docker-desk %s space\n", n.Tool.Version)
	fmt.Fprintf(&b, "# totals\t%s", oneLine(n.TotalsSource))
	if n.Stale {
		b.WriteString("\tSTALE: the engine changed after this account was taken")
	}
	b.WriteString("\n")
	for _, note := range n.Notes {
		fmt.Fprintf(&b, "# note\t%s\n", oneLine(note))
	}
	b.WriteString("\n## totals\ttype\tcount\tactive\tsize\treclaimable\n")
	for _, t := range n.Totals {
		fmt.Fprintf(&b, "total\t%s\t%d\t%d\t%d\t%d\n", oneLine(t.Type), t.Count, t.Active, t.Size, t.Reclaimable)
	}
	b.WriteString("\n## images\timage\tsize\talone\tbytes\ttogether\tbytes\treason\n")
	for _, img := range n.Images {
		together := "-\t-"
		reason := img.Alone.Reason
		if img.Together != nil {
			together = figureText(*img.Together)
			if img.Together.Reason != "" {
				reason = img.Together.Reason
			}
		}
		fmt.Fprintf(&b, "image\t%s\t%d\t%s\t%s\t%s\n", oneLine(label(img)), img.Size, figureText(img.Alone), together, oneLine(reason))
	}
	b.WriteString("\n## shared\tmeasure\tbytes\tlayers\timages\treason\n")
	names := map[string]string{}
	for _, img := range n.Images {
		names[img.ID] = label(img)
	}
	for _, g := range n.Groups {
		var hs []string
		for _, id := range g.Images {
			if l, ok := names[id]; ok {
				hs = append(hs, l)
			} else {
				hs = append(hs, id)
			}
		}
		sort.Strings(hs)
		fmt.Fprintf(&b, "shared\t%s\t%d\t%s\t%s\n", figureText(g.Size), g.Layers, oneLine(strings.Join(hs, " · ")), oneLine(g.Size.Reason))
	}
	if len(n.Groups) == 0 {
		b.WriteString("# no two images share a layer\n")
	}
	return []byte(b.String())
}
