package files

import (
	"sort"
	"strings"
)

// Order is how a directory's entries are listed.
type Order int

const (
	// ByName is byte order of the names, as `ls` in the C locale.
	ByName Order = iota
	// BySize is the largest first — a file by its own size, a directory
	// by the sum of its files — and by name among equals.
	BySize
)

func (o Order) String() string {
	if o == BySize {
		return "size"
	}
	return "name"
}

// Next is the other order.
func (o Order) Next() Order { return 1 - o }

// Row is one visible line of the tree.
type Row struct {
	Node  *Node
	Depth int  // 0 for an entry of the root
	Open  bool // a directory whose entries are the rows after it
	// Match is set on a row the filter matched; the others of a filtered
	// view are the directories that hold a match.
	Match bool
	// Gone is set beneath a path the container deleted.
	Gone bool
}

// View is the rows the tree shows, and what the filter found.
type View struct {
	Rows []Row
	pos  map[string]int

	// Filter is the filter the rows were made with; "" for none.
	Filter string
	// Matches counts the paths the filter matched; Searched the paths it
	// looked at — all the index holds.
	Matches, Searched int
	// MayMiss is set when the index is incomplete: a path the filter does
	// not match may still be in the image.
	MayMiss bool
}

// children returns n's entries in the order asked for. The stored order is
// by name; another is a sorted copy.
func children(n *Node, o Order) []*Node {
	if o != BySize || len(n.children) < 2 {
		return n.children
	}
	out := append([]*Node(nil), n.children...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// Flatten turns the tree into the rows to draw: the root's entries, and
// the entries of every directory that is expanded, depth first. With a
// filter, every path whose full path contains it — case-insensitively —
// is shown with the directories that contain it, whatever is expanded.
func Flatten(ix *Index, expanded map[string]bool, filter string, order Order) View {
	v := View{Filter: filter, pos: map[string]int{}}
	if ix == nil {
		return v
	}
	v.MayMiss = ix.Incomplete()
	var show map[*Node]bool
	q := strings.ToLower(filter)
	if filter != "" {
		show = map[*Node]bool{}
		var search func(n *Node) bool
		search = func(n *Node) bool {
			any := false
			for _, k := range n.children {
				v.Searched++
				hit := strings.Contains(strings.ToLower(k.Path), q)
				if hit {
					v.Matches++
				}
				if search(k) || hit {
					show[k], any = true, true
				}
			}
			return any
		}
		search(ix.Root)
	}
	var walk func(n *Node, depth int, gone bool)
	walk = func(n *Node, depth int, gone bool) {
		for _, k := range children(n, order) {
			if show != nil && !show[k] {
				continue
			}
			r := Row{Node: k, Depth: depth, Gone: gone}
			if show != nil {
				r.Match = strings.Contains(strings.ToLower(k.Path), q)
				r.Open = k.Expandable() && hasShown(k, show)
			} else {
				r.Open = k.Expandable() && expanded[k.Path]
			}
			v.pos[k.Path] = len(v.Rows)
			v.Rows = append(v.Rows, r)
			if r.Open {
				walk(k, depth+1, gone || k.Change == Deleted)
			}
		}
	}
	walk(ix.Root, 0, false)
	return v
}

func hasShown(n *Node, show map[*Node]bool) bool {
	for _, k := range n.children {
		if show[k] {
			return true
		}
	}
	return false
}

// Find returns the row the cursor on path p is drawn on: p's own, or,
// while p is hidden in a collapsed directory or by the filter, its
// nearest shown ancestor's; -1 when neither is shown.
func (v View) Find(p string) int {
	if i, ok := v.pos[p]; ok {
		return i
	}
	as := ancestors(p)
	for i := len(as) - 1; i >= 0; i-- {
		if r, ok := v.pos[as[i]]; ok {
			return r
		}
	}
	return -1
}

// Has reports whether p has a row of its own.
func (v View) Has(p string) bool { _, ok := v.pos[p]; return ok }

// Step moves the cursor on p by delta rows, clamped to the list, and
// returns the path it lands on. A cursor whose path is not shown starts
// from the row it is drawn on; with no rows it stays where it was.
func (v View) Step(p string, delta int) string {
	if len(v.Rows) == 0 {
		return p
	}
	i := v.Find(p)
	switch {
	case i < 0 && delta > 0:
		i = -1
	case i < 0:
		i = len(v.Rows)
	}
	i = max(0, min(len(v.Rows)-1, i+delta))
	return v.Rows[i].Node.Path
}

// Top and Bottom are the paths of the first and last rows.
func (v View) Top() string {
	if len(v.Rows) == 0 {
		return ""
	}
	return v.Rows[0].Node.Path
}

func (v View) Bottom() string {
	if len(v.Rows) == 0 {
		return ""
	}
	return v.Rows[len(v.Rows)-1].Node.Path
}

// Parent is the directory p is in, or "" for an entry of the root.
func Parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return ""
	}
	return p[:i]
}
