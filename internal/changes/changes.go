// Package changes explains what differs between the image a container runs
// and the image its tag names now (docs/DECISIONS.md, round eight, C1–C12).
//
// Everything here is pure: the desk fetches, this package compares. The
// cheap half — identities, layer lineage, config — works on two
// engine.ImageFacts; the expensive half compares two finished forensics
// reports. Neither ever sees an environment variable's value.
package changes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
)

// Refusal says why there is nothing to compare. The zero value means there
// is something.
type Refusal struct {
	Code string // "pinned", "from-id", "unresolved", "same", "platform", "unreadable"
	Text string // one sentence, for the operator
}

// None reports whether there is something to compare.
func (r Refusal) None() bool { return r.Code == "" }

// Pair is the two images: what a container runs and what its reference
// names now.
type Pair struct {
	Ref   string // what the container was created from (Config.Image)
	OldID string // the image the container runs
	NewID string // the image Ref names now; "" when refused
	Refusal
}

// FindPair decides, from the container's reference and the image list
// alone, whether there are two images to compare.
func FindPair(ref, runningID string, images []engine.Image) Pair {
	p := Pair{Ref: ref, OldID: runningID}
	bare := strings.TrimPrefix(ref, "sha256:")
	switch {
	case strings.HasPrefix(ref, "sha256:") || (len(bare) >= 12 && strings.HasPrefix(strings.TrimPrefix(runningID, "sha256:"), bare)):
		p.Refusal = Refusal{"from-id", "Created from an image ID, not a tag: no name can move, so there is no other image to compare."}
	case forensics.Pinned(ref):
		p.Refusal = Refusal{"pinned", "Pinned to a digest (" + oneLine(ref, 100) + "): the reference names these bytes and no others, so there is no other image to compare."}
	default:
		now, ok := forensics.ResolveRef(images, ref)
		switch {
		case !ok:
			p.Refusal = Refusal{"unresolved", oneLine(ref, 80) + " no longer resolves here: no local image carries that tag, so what it would name is unknown. This desk does not pull to find out."}
		case now.ID == runningID:
			p.Refusal = Refusal{"same", oneLine(ref, 80) + " still names the image this container runs: the same image on both sides."}
		default:
			p.NewID = now.ID
		}
	}
	return p
}

// SamePlatform refuses a pair whose images are for different platforms:
// their layers and packages are not two versions of one thing.
func SamePlatform(old, new engine.ImageFacts) Refusal {
	a, b := old.Platform().String(), new.Platform().String()
	if a == b {
		return Refusal{}
	}
	return Refusal{"platform", fmt.Sprintf("Different platforms: the container runs %s, the tag names %s here. Their layers and packages are not comparable.", a, b)}
}

// ---- layers ---------------------------------------------------------------------

// LayerLine is one layer outside the shared prefix.
type LayerLine struct {
	Index       int // 1-based
	DiffID      string
	Instruction string // "" when the history does not map onto the layers
	Size        int64  // -1 when not known
}

// Lineage is how the two layer stacks relate.
type Lineage struct {
	Shared  int // layers both start with, in order
	OldOnly []LayerLine
	NewOnly []LayerLine
	// OldUnmapped and NewUnmapped say why a side's instructions are not
	// known; "" when they are.
	OldUnmapped, NewUnmapped string
}

// BaseIntact reports whether every layer of the running image is the start
// of the new one: the new image only adds.
func (l Lineage) BaseIntact() bool { return len(l.OldOnly) == 0 }

// CompareLayers compares the ordered diff IDs.
func CompareLayers(old, new engine.ImageFacts) Lineage {
	var l Lineage
	for l.Shared < len(old.DiffIDs) && l.Shared < len(new.DiffIDs) && old.DiffIDs[l.Shared] == new.DiffIDs[l.Shared] {
		l.Shared++
	}
	side := func(f engine.ImageFacts) ([]LayerLine, string) {
		instr, sizes, ok := MapHistory(len(f.DiffIDs), f.History)
		why := ""
		if !ok {
			why = fmt.Sprintf("its history (%d entries) does not map onto its %d layers", len(f.History), len(f.DiffIDs))
		}
		var out []LayerLine
		for i := l.Shared; i < len(f.DiffIDs); i++ {
			ln := LayerLine{Index: i + 1, DiffID: f.DiffIDs[i], Size: -1}
			if ok {
				ln.Instruction, ln.Size = instr[i], sizes[i]
			}
			out = append(out, ln)
		}
		return out, why
	}
	l.OldOnly, l.OldUnmapped = side(old)
	l.NewOnly, l.NewUnmapped = side(new)
	return l
}

// MapHistory maps a history, oldest entry first, onto n layers, by the rule
// the Space view uses for sizes: only when exactly n entries made a layer
// (have a size), or when every entry did. Otherwise ok is false and no
// instruction is attributed — never a guess.
func MapHistory(n int, history []engine.HistoryEntry) (instr []string, sizes []int64, ok bool) {
	var made []engine.HistoryEntry
	for _, h := range history {
		if h.Size > 0 {
			made = append(made, h)
		}
	}
	switch {
	case n == 0:
		return nil, nil, false
	case len(made) == n:
	case len(history) == n:
		made = history
	default:
		return nil, nil, false
	}
	for _, h := range made {
		instr = append(instr, oneLine(forensics.Instruction(h.CreatedBy), 200))
		sizes = append(sizes, h.Size)
	}
	return instr, sizes, true
}

// ---- config ---------------------------------------------------------------------

// Change is one config difference.
type Change struct {
	Field string // "command", "entrypoint", "user", "workdir", "port", "healthcheck", "label", "env"
	// Op is "added", "removed" or "changed".
	Op string
	// Name is the port, label key or environment variable name; "" for a
	// single-valued field.
	Name string
	// Old and New are the values; always "" for env, whose values are
	// never read.
	Old, New string
}

// CompareConfig lists what the new image's config changes, in a fixed
// field order. Environment variables are compared by name only.
func CompareConfig(old, new engine.ImageFacts) []Change {
	var out []Change
	one := func(field, a, b string) {
		switch {
		case a == b:
		case a == "":
			out = append(out, Change{Field: field, Op: "added", New: b})
		case b == "":
			out = append(out, Change{Field: field, Op: "removed", Old: a})
		default:
			out = append(out, Change{Field: field, Op: "changed", Old: a, New: b})
		}
	}
	one("entrypoint", argv(old.Entrypoint), argv(new.Entrypoint))
	one("command", argv(old.Cmd), argv(new.Cmd))
	one("user", oneLine(old.User, 60), oneLine(new.User, 60))
	one("workdir", oneLine(old.WorkingDir, 120), oneLine(new.WorkingDir, 120))
	one("healthcheck", argv(old.Healthcheck), argv(new.Healthcheck))
	for _, c := range setDiff(old.ExposedPorts, new.ExposedPorts) {
		out = append(out, Change{Field: "port", Op: c.op, Name: oneLine(c.name, 40)})
	}
	keys := map[string]bool{}
	for k := range old.Labels {
		keys[k] = true
	}
	for k := range new.Labels {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		a, inA := old.Labels[k]
		b, inB := new.Labels[k]
		c := Change{Field: "label", Name: oneLine(k, 80), Old: oneLine(a, 120), New: oneLine(b, 120)}
		switch {
		case inA && !inB:
			c.Op, c.New = "removed", ""
		case !inA && inB:
			c.Op, c.Old = "added", ""
		case a != b:
			c.Op = "changed"
		default:
			continue
		}
		out = append(out, c)
	}
	for _, c := range setDiff(old.EnvNames, new.EnvNames) {
		out = append(out, Change{Field: "env", Op: c.op, Name: oneLine(c.name, 80)})
	}
	return out
}

type nameOp struct{ name, op string }

// setDiff returns the names only one side has: removed (old only), then
// added (new only), each sorted.
func setDiff(old, new []string) []nameOp {
	a, b := map[string]bool{}, map[string]bool{}
	for _, s := range old {
		a[s] = true
	}
	for _, s := range new {
		b[s] = true
	}
	var rm, add []string
	for s := range a {
		if !b[s] {
			rm = append(rm, s)
		}
	}
	for s := range b {
		if !a[s] {
			add = append(add, s)
		}
	}
	sort.Strings(rm)
	sort.Strings(add)
	var out []nameOp
	for _, s := range rm {
		out = append(out, nameOp{s, "removed"})
	}
	for _, s := range add {
		out = append(out, nameOp{s, "added"})
	}
	return out
}

func argv(a []string) string {
	if a == nil {
		return ""
	}
	q := make([]string, len(a))
	for i, s := range a {
		q[i] = fmt.Sprintf("%q", s)
	}
	return oneLine("["+strings.Join(q, ", ")+"]", 200)
}

// oneLine makes daemon-supplied text safe for one row, bounded to n runes.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}
