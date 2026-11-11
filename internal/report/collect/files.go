package collect

import (
	"fmt"

	"github.com/fardani235/docker-desk/internal/files"
	"github.com/fardani235/docker-desk/internal/report"
)

// FilesInput is what the Files view shows: the tree (an image's, or an
// image's with a container's changes over it) and the object's identity.
type FilesInput struct {
	Version    string
	Index      *files.Index
	ImageID    string
	Ref        string
	Platform   string
	DaemonSize int64
	// ContainerID and ContainerName are set for a container's view.
	ContainerID, ContainerName string
}

// Files collects the listing. Every name — paths, link targets, the
// container's name — goes through files.Escape here, the function the
// screen draws with, so the file and the screen say the same thing.
func Files(in FilesInput) report.Files {
	ix := in.Index
	out := report.Files{
		ToolVersion: in.Version,
		Image: report.FilesImage{
			ID: in.ImageID, Reference: files.Escape(in.Ref), Platform: files.Escape(in.Platform),
			Layers: ix.Layers, DaemonSize: in.DaemonSize,
		},
		Bounds: report.FilesBounds{
			Seen: ix.Seen, Kept: ix.Kept,
			DroppedEntries: ix.DroppedEntries, DroppedLongPath: ix.DroppedLongPath, DroppedText: ix.DroppedText,
			MaxEntries: ix.MaxEntries, MaxPath: ix.MaxPath, MaxText: ix.MaxText,
		},
		Removed: ix.Removed, Hidden: ix.Hidden, TreeBytes: ix.Total,
	}
	for _, u := range ix.Unread {
		out.Unread = append(out.Unread, report.FilesUnread{Layer: u.Layer, Reason: u.Reason})
	}
	for l := 1; l <= ix.Layers; l++ {
		out.Instructions = append(out.Instructions, report.FilesInstruction{Layer: l, Instruction: ix.Instruction(l)})
	}
	if in.ContainerID != "" && ix.Applied != nil {
		a := ix.Applied
		out.Container = &report.FilesContainer{
			ID: in.ContainerID, Name: files.Escape(in.ContainerName),
			Changes: a.Total(), Beneath: a.Beneath, DeletedMissing: a.DeletedMissing,
		}
	}
	if ix.Incomplete() {
		out.Notes = append(out.Notes, "the index is incomplete (a bound, or a layer not read): a path not listed here may still be in the image")
	}
	var walk func(n *files.Node)
	walk = func(n *files.Node) {
		for _, k := range n.Children() {
			e := report.FilesEntry{
				Path: files.Escape(k.Path), Type: k.Type.String(), Layer: k.Layer,
				Mode: fmt.Sprintf("%04o", k.Mode&0o7777), Change: k.Change.String(),
			}
			if !k.FromChanges {
				size := k.Size
				e.Size = &size
			} else {
				e.Mode, e.Layer = "-", 0
			}
			if k.IsDir() {
				e.Layer = 0 // several layers can write one directory (D3)
			}
			if k.Link != "" {
				e.Link = files.Escape(k.Link)
			}
			out.Entries = append(out.Entries, e)
			walk(k)
		}
	}
	walk(ix.Root)
	return out
}
