package forensics

import (
	"archive/tar"
	"time"
)

// The Files listing: what each layer's tar headers say, kept in ModeFiles
// and ModeBoth for the Files view (docs/DECISIONS.md, round fifteen). It is
// a second projection of the walk the analysis already makes, not a second
// archive parser: the layout, the platform, the layer order and the
// compressions are this package's, and the merge into one filesystem is
// internal/files'.
//
// Only headers are kept — a path, its type, mode, size, owner, time and link
// target. No file's bytes are read in ModeFiles, and none are kept in either
// mode.

// FileEntry is one tar header of a layer.
type FileEntry struct {
	// Path is absolute and cleaned the way the walk cleans every layer
	// name, so it can never name anything above the root. It is the raw
	// name: escape it (files.Escape) before it reaches a screen or a file.
	Path string
	// Type is the tar type flag (tar.TypeReg, tar.TypeDir, …).
	Type     byte
	Mode     int64
	Size     int64 // as recorded; 0 for anything but a regular file
	UID, GID int
	ModTime  time.Time
	// Link is a symlink's target as recorded, or a hard link's target
	// cleaned to an absolute path.
	Link string
}

// LayerFiles is what one layer of the manifest holds, in order.
type LayerFiles struct {
	Index   int // 1-based
	Entries []FileEntry
	// Whiteouts are the paths this layer removes from the layers below;
	// Opaques the directories whose lower contents it hides.
	Whiteouts []string
	Opaques   []string
	// Unread says why the layer, or the rest of it, was not read; "" when
	// it was read to its end.
	Unread string
	// Instruction is the build instruction that made the layer; "" when
	// the history does not map (never a guess).
	Instruction string
}

// FileListing is every layer's headers and what the bounds left out.
type FileListing struct {
	Layers []LayerFiles
	// Seen counts every header of every layer (whiteouts and opaque
	// markers included); Kept the ones kept.
	Seen, Kept int
	// What each bound dropped, counted after it was hit.
	DroppedEntries  int // over Limits.FileEntries
	DroppedLongPath int // a path or link target longer than Limits.FilePath
	DroppedText     int // over Limits.FileText
	// The bounds the listing was made under.
	MaxEntries, MaxPath int
	MaxText             int64
}

// Cut reports whether a bound dropped anything: a path that is not in the
// listing may still be in the image.
func (l *FileListing) Cut() bool {
	return l.DroppedEntries+l.DroppedLongPath+l.DroppedText > 0
}

// Unread lists the layers that were not read to their end.
func (l *FileListing) Unread() []LayerFiles {
	var out []LayerFiles
	for _, x := range l.Layers {
		if x.Unread != "" {
			out = append(out, LayerFiles{Index: x.Index, Unread: x.Unread})
		}
	}
	return out
}

// fileBounds is the listing's running count across layers.
type fileBounds struct {
	seen, kept                   int
	text                         int64
	dropEntries, dropLong, dropT int
}

// admit applies the three bounds to one header whose kept text is n bytes
// and whose longest name is longest bytes; it counts the header either way.
func (a *analyser) admit(longest, n int) bool {
	f := a.files
	f.seen++
	switch {
	case longest > a.lim.FilePath:
		f.dropLong++
	case f.kept >= a.lim.FileEntries:
		f.dropEntries++
	case f.text+int64(n) > a.lim.FileText:
		f.dropT++
	default:
		f.kept++
		f.text += int64(n)
		return true
	}
	return false
}

// fileMarker keeps a whiteout or an opaque marker of a layer.
func (a *analyser) fileMarker(s *layerScan, p string, opaque bool) {
	if a.files == nil || !a.admit(len(p), len(p)) {
		return
	}
	if opaque {
		s.fl.Opaques = append(s.fl.Opaques, p)
	} else {
		s.fl.Whiteouts = append(s.fl.Whiteouts, p)
	}
}

// fileEntry keeps one header of a layer.
func (a *analyser) fileEntry(s *layerScan, p string, h *tar.Header) {
	if a.files == nil {
		return
	}
	link := ""
	switch h.Typeflag {
	case tar.TypeSymlink:
		link = h.Linkname
	case tar.TypeLink:
		link = layerPath(h.Linkname)
	}
	if !a.admit(max(len(p), len(link)), len(p)+len(link)) {
		return
	}
	e := FileEntry{
		Path: p, Type: h.Typeflag, Mode: h.Mode, UID: h.Uid, GID: h.Gid, ModTime: h.ModTime, Link: link,
	}
	if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA { //nolint:staticcheck // old layers use TypeRegA
		e.Type, e.Size = tar.TypeReg, h.Size
	}
	s.fl.Entries = append(s.fl.Entries, e)
}

// listing assembles the layers in the manifest's order.
func (a *analyser) listing(scans []*layerScan, instr []string) *FileListing {
	f := a.files
	out := &FileListing{
		Seen: f.seen, Kept: f.kept,
		DroppedEntries: f.dropEntries, DroppedLongPath: f.dropLong, DroppedText: f.dropT,
		MaxEntries: a.lim.FileEntries, MaxPath: a.lim.FilePath, MaxText: a.lim.FileText,
	}
	for i, s := range scans {
		l := LayerFiles{Index: i + 1, Instruction: instr[i]}
		switch {
		case s == nil:
			l.Unread = "the manifest lists the layer, but the archive does not hold it"
		default:
			l.Entries, l.Whiteouts, l.Opaques = s.fl.Entries, s.fl.Whiteouts, s.fl.Opaques
			l.Unread = s.unreadable
		}
		out.Layers = append(out.Layers, l)
	}
	return out
}
