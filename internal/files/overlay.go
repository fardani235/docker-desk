package files

import (
	"path"
	"sort"
)

// Change is one entry of a container's change list (GET
// /containers/{id}/changes), as the daemon names it.
type Change struct {
	Path string
	Kind ChangeKind
}

// Applied is what laying a change list over a tree did.
type Applied struct {
	// The changes marked, by kind.
	Added, Changed, Deleted int
	// Outside counts changed paths the index does not hold; they are
	// drawn, marked as not in it.
	Outside int
	// DeletedMissing counts deleted paths the index does not hold. They
	// are not drawn — there is nothing to strike through — and they are
	// counted, because a cut index may be why (D6).
	DeletedMissing int
	// Beneath counts changes under a path the container added: not drawn,
	// because what an added directory holds is not read.
	Beneath int
	// Ignored counts entries that name the root or nothing.
	Ignored int
}

// Total is every change in the list.
func (a Applied) Total() int {
	return a.Added + a.Changed + a.Deleted + a.DeletedMissing + a.Beneath + a.Ignored
}

// Apply lays a container's changes over its image's tree and returns the
// result; ix itself is not modified, so one image's tree serves every
// container made from it. Only the paths from the root to a change are
// copied. ix may be nil: the image has not been read, and the result is
// the changes alone, with no sizes.
//
// Every path is cleaned as the archive's are, so a change can never name a
// place above the root. A path the index holds is marked; an added path it
// does not hold is drawn with no size and cannot be expanded; a changed one
// is drawn and counted as outside the index; a deleted one is counted.
func Apply(ix *Index, changes []Change) *Index {
	var out Index
	if ix != nil {
		out = *ix
	} else {
		out = Index{nodes: map[string]*Node{}}
		out.Root = newRoot()
		out.Root.kids = nil
		out.nodes["/"] = out.Root
	}
	base := out.nodes
	out.Root = copyNode(out.Root)
	out.own = map[string]*Node{"/": out.Root}
	a := &Applied{}
	out.Applied = a

	cs := make([]Change, 0, len(changes))
	for _, c := range changes {
		cs = append(cs, Change{Path: clean(c.Path), Kind: c.Kind})
	}
	// Parents before children, so a change under an added path finds it.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Path < cs[j].Path })

	for _, c := range cs {
		if c.Path == "/" || c.Kind < Changed || c.Kind > Deleted {
			a.Ignored++
			continue
		}
		// A change under a path the container added is part of it.
		if top := out.addedAbove(c.Path); top != nil {
			top.Beneath++
			top.Type = TypeDir // something is beneath it: it is a directory
			a.Beneath++
			continue
		}
		if base[c.Path] != nil {
			n := out.reach(c.Path)
			n.Change = c.Kind
			switch c.Kind {
			case Added:
				a.Added++
			case Changed:
				a.Changed++
			case Deleted:
				a.Deleted++
			}
			continue
		}
		switch c.Kind {
		case Deleted:
			a.DeletedMissing++
			continue
		case Changed:
			a.Outside++
		case Added:
			a.Added++
		}
		n := out.reach(c.Path)
		n.Change, n.FromChanges = c.Kind, true
	}
	return &out
}

func copyNode(n *Node) *Node {
	c := *n
	c.children = append([]*Node(nil), n.children...)
	return &c
}

// addedAbove returns the nearest node above p the container added.
func (ix *Index) addedAbove(p string) *Node {
	as := ancestors(p)
	for i := len(as) - 1; i >= 0; i-- {
		if n := ix.own[as[i]]; n != nil && n.Change == Added {
			return n
		}
	}
	return nil
}

// reach returns this overlay's own copy of the node at p, copying every
// node on the way down from the root and making the ones that are not
// there — the directories a change implies, and the change's own path.
// Each directory above it counts the change as beneath it.
func (ix *Index) reach(p string) *Node {
	cur := ix.Root
	for _, step := range append(ancestors(p), p) {
		cur.Beneath++
		if n := ix.own[step]; n != nil {
			cur = n
			continue
		}
		name := path.Base(step)
		i := sort.Search(len(cur.children), func(i int) bool { return cur.children[i].Name >= name })
		var n *Node
		if i < len(cur.children) && cur.children[i].Name == name {
			n = copyNode(cur.children[i])
			cur.children[i] = n
		} else {
			n = &Node{Path: step, Name: name, Type: TypeUnknown, FromChanges: true}
			if step != p {
				n.Type, n.Implicit = TypeDir, true
			}
			cur.children = append(cur.children, nil)
			copy(cur.children[i+1:], cur.children[i:])
			cur.children[i] = n
		}
		if cur.Type == TypeUnknown {
			cur.Type = TypeDir // something is beneath it
		}
		ix.own[step] = n
		cur = n
	}
	return cur
}
