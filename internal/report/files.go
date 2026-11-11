package report

import (
	"fmt"
	"sort"
	"strings"
)

// FilesSchema names the native schema of a Files document.
const FilesSchema = "docker-desk/files-listing/v1"

// FilesNames says how every name in a Files document is written.
const FilesNames = `escaped: a newline is \n, other control characters, DEL and each byte of invalid UTF-8 are \xNN, a C1 control is \u00NN, a backslash is \\; everything else is as the archive or the daemon names it`

// Files is the Files view's listing: every path of an image's merged tree
// with its type, size, mode and layer and, for a container, what the
// container did to it. It holds names and numbers only — never a byte of
// any file — and every name is already escaped (FilesNames), so neither a
// file nor a terminal showing it can be acted on by a name.
type Files struct {
	Schema      string `json:"schema"`
	Tool        Tool   `json:"tool"`
	ToolVersion string `json:"-"`

	Image     FilesImage      `json:"image"`
	Container *FilesContainer `json:"container"`
	Names     string          `json:"names"`

	Bounds  FilesBounds   `json:"bounds"`
	Unread  []FilesUnread `json:"layers_not_read"`
	Removed int           `json:"removed_by_later_layers"`
	Hidden  int           `json:"hidden_beneath_removed_or_opaque"`
	// TreeBytes is the sum of the tree's files; the daemon's size for the
	// image is a different measurement (Image.DaemonSize).
	TreeBytes    int64              `json:"tree_bytes"`
	Instructions []FilesInstruction `json:"instructions"`
	Entries      []FilesEntry       `json:"entries"`
	Notes        []string           `json:"notes"`
}

// FilesImage is the image whose tree is listed.
type FilesImage struct {
	ID         string `json:"id"`
	Reference  string `json:"reference"`
	Platform   string `json:"platform"`
	Layers     int    `json:"layers"`
	DaemonSize int64  `json:"daemon_size"`
}

// FilesContainer is the container whose changes are laid over the tree.
type FilesContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Changes int    `json:"changes_listed"`
	// Beneath counts changes under a path the container added: listed by
	// the daemon, not in this file, since what it holds is not read.
	Beneath int `json:"changes_beneath_added_paths"`
	// DeletedMissing counts deleted paths the index does not hold.
	DeletedMissing int `json:"deleted_paths_not_in_the_index"`
}

// FilesBounds is what the index kept of what it saw.
type FilesBounds struct {
	Seen            int   `json:"seen"`
	Kept            int   `json:"kept"`
	DroppedEntries  int   `json:"dropped_past_entry_bound"`
	DroppedLongPath int   `json:"dropped_past_length_bound"`
	DroppedText     int   `json:"dropped_past_text_bound"`
	MaxEntries      int   `json:"max_entries"`
	MaxPath         int   `json:"max_path_bytes"`
	MaxText         int64 `json:"max_text_bytes"`
}

// FilesUnread is a layer that was not read to its end.
type FilesUnread struct {
	Layer  int    `json:"layer"`
	Reason string `json:"reason"`
}

// FilesInstruction is one layer's build instruction, "unknown" where the
// history does not say.
type FilesInstruction struct {
	Layer       int    `json:"layer"`
	Instruction string `json:"instruction"`
}

// FilesEntry is one path. Size is nil where it is not known (a path the
// container added); Layer is 0 for a directory or a path not in the image.
type FilesEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   *int64 `json:"size"`
	Mode   string `json:"mode"`
	Layer  int    `json:"layer"`
	Link   string `json:"link,omitempty"`
	Change string `json:"change,omitempty"`
}

func normaliseFiles(f Files) Files {
	f.Schema = FilesSchema
	f.Tool = toolOf(f.ToolVersion)
	f.Names = FilesNames
	f.Entries = append([]FilesEntry{}, f.Entries...)
	sort.SliceStable(f.Entries, func(i, j int) bool { return f.Entries[i].Path < f.Entries[j].Path })
	f.Unread = append([]FilesUnread{}, f.Unread...)
	sort.SliceStable(f.Unread, func(i, j int) bool { return f.Unread[i].Layer < f.Unread[j].Layer })
	f.Instructions = append([]FilesInstruction{}, f.Instructions...)
	sort.SliceStable(f.Instructions, func(i, j int) bool { return f.Instructions[i].Layer < f.Instructions[j].Layer })
	f.Notes = append([]string{}, f.Notes...)
	return f
}

// FilesJSON is the native document.
func FilesJSON(f Files) ([]byte, error) { return marshal(normaliseFiles(f)) }

// FilesText is one path per line, tab-separated: path, type, size, mode,
// layer, link, change. The names are already escaped and so hold no tab or
// newline; the desk's own words are passed through oneLine.
func FilesText(f Files) []byte {
	n := normaliseFiles(f)
	var b strings.Builder
	fmt.Fprintf(&b, "# docker-desk %s files\n", n.Tool.Version)
	fmt.Fprintf(&b, "# image\t%s\n", oneLine(n.Image.ID))
	fmt.Fprintf(&b, "# reference\t%s\n", oneLine(n.Image.Reference))
	fmt.Fprintf(&b, "# platform\t%s\n", oneLine(n.Image.Platform))
	fmt.Fprintf(&b, "# layers\t%d\n", n.Image.Layers)
	fmt.Fprintf(&b, "# daemon size\t%d\n", n.Image.DaemonSize)
	fmt.Fprintf(&b, "# tree bytes\t%d\tthe files left in the merged tree: a different measurement from the daemon's size\n", n.TreeBytes)
	if c := n.Container; c != nil {
		fmt.Fprintf(&b, "# container\t%s\t%s\n", oneLine(c.ID), c.Name)
		fmt.Fprintf(&b, "# changes listed\t%d\n", c.Changes)
		if c.Beneath > 0 {
			fmt.Fprintf(&b, "# changes beneath added paths, not listed\t%d\n", c.Beneath)
		}
		if c.DeletedMissing > 0 {
			fmt.Fprintf(&b, "# deleted paths not in the index, not listed\t%d\n", c.DeletedMissing)
		}
	}
	fmt.Fprintf(&b, "# names\t%s\n", oneLine(n.Names))
	bd := n.Bounds
	fmt.Fprintf(&b, "# indexed\t%d of %d seen\n", bd.Kept, bd.Seen)
	if bd.DroppedEntries+bd.DroppedLongPath+bd.DroppedText > 0 {
		fmt.Fprintf(&b, "# dropped\tentries %d (bound %d)\tlong paths %d (bound %d bytes)\ttext %d (bound %d bytes)\n",
			bd.DroppedEntries, bd.MaxEntries, bd.DroppedLongPath, bd.MaxPath, bd.DroppedText, bd.MaxText)
	}
	for _, u := range n.Unread {
		fmt.Fprintf(&b, "# layer not read\t%d\t%s\n", u.Layer, oneLine(u.Reason))
	}
	if n.Removed+n.Hidden > 0 {
		fmt.Fprintf(&b, "# removed by later layers\t%d\thidden beneath them or under opaque directories\t%d\n", n.Removed, n.Hidden)
	}
	for _, in := range n.Instructions {
		fmt.Fprintf(&b, "# layer %d\t%s\n", in.Layer, oneLine(in.Instruction))
	}
	for _, note := range n.Notes {
		fmt.Fprintf(&b, "# note\t%s\n", oneLine(note))
	}
	b.WriteString("\n## path\ttype\tsize\tmode\tlayer\tlink\tchange\n")
	for _, e := range n.Entries {
		size := "-"
		if e.Size != nil {
			size = fmt.Sprint(*e.Size)
		}
		layer := "-"
		if e.Layer > 0 {
			layer = fmt.Sprint(e.Layer)
		}
		link, change := e.Link, e.Change
		if link == "" {
			link = "-"
		}
		if change == "" {
			change = "-"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Path, e.Type, size, e.Mode, layer, link, change)
	}
	return []byte(b.String())
}
