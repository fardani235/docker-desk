// Package files is the Files view's model: an image's layers merged into
// one tree, its directory sizes, its ordering, filtering and flattening into
// rows, a container's changes laid over it, and the escaping every name
// takes before it reaches a screen or a file (docs/DECISIONS.md, round
// fifteen).
//
// It is pure. It is fed the per-layer headers package forensics lists, and
// it never opens, stats, resolves or joins a path to anything: a path here
// is a string from an archive or from the daemon, and it stays one.
package files

import (
	"archive/tar"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/forensics"
)

// Type is what an entry is, as the archive records it.
type Type int

const (
	TypeFile Type = iota
	TypeDir
	TypeSymlink
	TypeHardLink
	TypeChar
	TypeBlock
	TypeFIFO
	TypeOther
	// TypeUnknown is a path known only from a container's change list,
	// which does not say what a path is.
	TypeUnknown
)

var typeNames = [...]string{"file", "dir", "symlink", "hardlink", "chardev", "blockdev", "fifo", "other", "?"}

func (t Type) String() string { return typeNames[t] }

// Words is the type in a sentence.
func (t Type) Words() string {
	switch t {
	case TypeFile:
		return "regular file"
	case TypeDir:
		return "directory"
	case TypeSymlink:
		return "symbolic link (shown, never followed)"
	case TypeHardLink:
		return "hard link (one entry naming another)"
	case TypeChar:
		return "character device (typed, never opened)"
	case TypeBlock:
		return "block device (typed, never opened)"
	case TypeFIFO:
		return "named pipe (typed, never opened)"
	case TypeUnknown:
		return "not known: the daemon's change list does not say"
	}
	return "other archive entry (typed, never opened)"
}

func typeOf(flag byte) Type {
	switch flag {
	case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // old layers use TypeRegA
		return TypeFile
	case tar.TypeDir:
		return TypeDir
	case tar.TypeSymlink:
		return TypeSymlink
	case tar.TypeLink:
		return TypeHardLink
	case tar.TypeChar:
		return TypeChar
	case tar.TypeBlock:
		return TypeBlock
	case tar.TypeFifo:
		return TypeFIFO
	}
	return TypeOther
}

// ChangeKind is what a container did to a path since its image.
type ChangeKind int

const (
	Unchanged ChangeKind = iota
	Changed
	Added
	Deleted
)

func (k ChangeKind) String() string {
	switch k {
	case Changed:
		return "changed"
	case Added:
		return "added"
	case Deleted:
		return "deleted"
	}
	return ""
}

// Node is one path of the tree.
type Node struct {
	Path string // absolute, raw: Escape it before showing it
	Name string // the last element, raw
	Type Type
	Mode int64
	// Size is a file's size as the archive records it; a directory's is
	// the sum of the files beneath it, and Computed says so.
	Size     int64
	Computed bool
	// Files counts the non-directories beneath a directory.
	Files    int
	UID, GID int
	ModTime  time.Time
	Link     string // a symlink's target as recorded, a hard link's path
	// Layer is the 1-based layer that last wrote the path; 0 for a
	// directory no layer has a header for. A view shows no layer for a
	// directory: many layers write one (D3).
	Layer int
	// Implicit is a directory that exists only because something beneath
	// it does: no layer, or no change, names it.
	Implicit bool

	// The overlay (Apply). Change is what the container did; FromChanges
	// marks a path the image's tree does not hold; Beneath counts the
	// changes under this path — drawn below it, or, under an added
	// path, counted and not drawn, since what it holds is not read.
	Change      ChangeKind
	FromChanges bool
	Beneath     int

	kids     map[string]*Node // while merging
	children []*Node          // sorted by name once merged
}

// Children are the node's entries, by name.
func (n *Node) Children() []*Node { return n.children }

// IsDir reports whether the node is a directory.
func (n *Node) IsDir() bool { return n.Type == TypeDir }

// Expandable reports whether the node can be opened: a directory the
// image holds, or one the change list implies. A path the container added
// cannot be, since its contents are not read; a symlink never is.
func (n *Node) Expandable() bool { return n.Type == TypeDir && n.Change != Added }

// Unread is a layer that was not read to its end.
type Unread struct {
	Layer  int
	Reason string
}

// Index is one image's merged tree and what it cost.
type Index struct {
	Root  *Node
	nodes map[string]*Node
	// own holds the nodes an overlay copied or added; Lookup reads it
	// before nodes, which it shares with the index it was made from.
	own map[string]*Node

	// Image says whether an image's tree is underneath: false for an
	// overlay of a container's changes alone.
	Image  bool
	Layers int
	Unread []Unread
	// Instructions[i] is layer i+1's build instruction, "" when unknown.
	Instructions []string

	// The listing's counts and bounds (forensics.FileListing).
	Seen, Kept                                   int
	DroppedEntries, DroppedLongPath, DroppedText int
	MaxEntries, MaxPath                          int
	MaxText                                      int64

	// Removed counts the whiteouts that removed a path the tree held;
	// Hidden the entries beneath a removed or opaque directory that went
	// with it.
	Removed, Hidden int
	// Entries is the paths in the tree; Total the bytes of its files.
	Entries int
	Total   int64

	// Applied is what Apply laid over the tree; nil on an image's own.
	Applied *Applied
}

// Lookup returns the node at an absolute path, or nil.
func (ix *Index) Lookup(p string) *Node {
	if ix == nil {
		return nil
	}
	if n := ix.own[p]; n != nil {
		return n
	}
	return ix.nodes[p]
}

// Cut reports whether a bound dropped an entry.
func (ix *Index) Cut() bool { return ix.DroppedEntries+ix.DroppedLongPath+ix.DroppedText > 0 }

// Incomplete reports whether a path not in the tree may still be in the
// image: a bound cut the index or a layer was not read.
func (ix *Index) Incomplete() bool { return ix.Cut() || len(ix.Unread) > 0 }

// Unsure returns the unread layers above a 1-based layer: any of them may
// have replaced or removed what that layer wrote, so its attribution — and
// the path's being there at all — is not certain.
func (ix *Index) Unsure(layer int) []Unread {
	var out []Unread
	for _, u := range ix.Unread {
		if u.Layer > layer {
			out = append(out, u)
		}
	}
	return out
}

// Instruction is a 1-based layer's build instruction, or "unknown".
func (ix *Index) Instruction(layer int) string {
	if layer < 1 || layer > len(ix.Instructions) || ix.Instructions[layer-1] == "" {
		return "unknown"
	}
	return ix.Instructions[layer-1]
}

// Build merges a ModeFiles or ModeBoth report's listing; nil when the
// report has none.
func Build(rep *forensics.Report) *Index {
	if rep == nil || rep.Files == nil {
		return nil
	}
	l := rep.Files
	ix := Merge(l.Layers)
	ix.Seen, ix.Kept = l.Seen, l.Kept
	ix.DroppedEntries, ix.DroppedLongPath, ix.DroppedText = l.DroppedEntries, l.DroppedLongPath, l.DroppedText
	ix.MaxEntries, ix.MaxPath, ix.MaxText = l.MaxEntries, l.MaxPath, l.MaxText
	if len(rep.Instructions) == len(l.Layers) {
		ix.Instructions = append([]string(nil), rep.Instructions...)
	}
	return ix
}

func newRoot() *Node {
	return &Node{Path: "/", Name: "/", Type: TypeDir, Implicit: true, kids: map[string]*Node{}}
}

// clean makes any name an absolute path under the root: "../../etc/passwd"
// is /etc/passwd, never a place above it.
func clean(p string) string { return path.Clean("/" + p) }

// Merge lays the layers over one another, lowest first, as the daemon's
// storage does: within a layer, its whiteouts and opaque markers apply to
// the layers below it, and its own entries are then added; a later entry
// replaces an earlier one at the same path and keeps the later layer as
// its writer; a whiteout removes a path and everything beneath it; an
// opaque directory keeps only what its own and later layers put in it.
func Merge(layers []forensics.LayerFiles) *Index {
	ix := &Index{Root: newRoot(), nodes: map[string]*Node{}, Image: true, Layers: len(layers)}
	ix.nodes["/"] = ix.Root
	for i, l := range layers {
		n := i + 1
		if l.Index > 0 {
			n = l.Index
		}
		if l.Unread != "" {
			ix.Unread = append(ix.Unread, Unread{Layer: n, Reason: l.Unread})
		}
		ix.Instructions = append(ix.Instructions, l.Instruction)
		for _, d := range l.Opaques {
			if dn := ix.nodes[clean(d)]; dn != nil && dn.IsDir() {
				for _, k := range dn.kids {
					ix.Hidden += ix.drop(k)
				}
			}
		}
		for _, w := range l.Whiteouts {
			if wn := ix.nodes[clean(w)]; wn != nil && wn != ix.Root {
				ix.Removed++
				ix.Hidden += ix.drop(wn) - 1
			}
		}
		for _, e := range l.Entries {
			ix.put(e, n)
		}
	}
	ix.finish()
	return ix
}

// drop removes a node and everything beneath it; it returns how many.
func (ix *Index) drop(n *Node) int {
	c := 1
	for _, k := range n.kids {
		c += ix.drop(k)
	}
	delete(ix.nodes, n.Path)
	if p := ix.nodes[path.Dir(n.Path)]; p != nil && p.kids[n.Name] == n {
		delete(p.kids, n.Name)
	}
	return c
}

// dir returns the directory at p, making it (and its parents) when no
// layer has: a tar may name a file before, or without, its directory.
func (ix *Index) dir(p string) *Node {
	if n := ix.nodes[p]; n != nil {
		if n.IsDir() {
			return n
		}
		ix.drop(n) // a file in the way: what is beneath it says it is a directory now
	}
	parent := ix.dir(path.Dir(p))
	n := &Node{Path: p, Name: path.Base(p), Type: TypeDir, Implicit: true, kids: map[string]*Node{}}
	parent.kids[n.Name] = n
	ix.nodes[p] = n
	return n
}

func (ix *Index) put(e forensics.FileEntry, layer int) {
	p := clean(e.Path)
	if p == "/" {
		return
	}
	t := typeOf(e.Type)
	n := ix.nodes[p]
	switch {
	case n != nil && n.IsDir() && t == TypeDir:
		// The same directory again: its own fields change, its
		// contents stay.
	case n != nil:
		ix.drop(n)
		n = nil
	}
	if n == nil {
		parent := ix.dir(path.Dir(p))
		n = &Node{Path: p, Name: path.Base(p)}
		parent.kids[n.Name] = n
		ix.nodes[p] = n
	}
	n.Type, n.Mode, n.UID, n.GID, n.ModTime, n.Layer, n.Implicit = t, e.Mode, e.UID, e.GID, e.ModTime, layer, false
	n.Size, n.Link = 0, e.Link
	if t == TypeFile {
		n.Size = e.Size
	}
	if t == TypeDir && n.kids == nil {
		n.kids = map[string]*Node{}
	}
}

// finish sorts every directory's entries by name and adds up its files.
func (ix *Index) finish() {
	var walk func(n *Node)
	walk = func(n *Node) {
		if !n.IsDir() {
			return
		}
		n.children = make([]*Node, 0, len(n.kids))
		for _, k := range n.kids {
			n.children = append(n.children, k)
		}
		n.kids = nil
		sort.Slice(n.children, func(i, j int) bool { return n.children[i].Name < n.children[j].Name })
		n.Size, n.Files, n.Computed = 0, 0, true
		for _, k := range n.children {
			walk(k)
			n.Size += k.Size
			if k.IsDir() {
				n.Files += k.Files
			} else {
				n.Files++
			}
		}
	}
	walk(ix.Root)
	ix.Entries = len(ix.nodes) - 1
	ix.Total = ix.Root.Size
}

// ancestors lists the directories above p, outermost first, without "/".
func ancestors(p string) []string {
	var out []string
	for i := 1; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// isUnder reports whether p lies beneath dir.
func isUnder(p, dir string) bool {
	return dir == "/" || strings.HasPrefix(p, dir+"/")
}
