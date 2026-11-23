package transfer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/escape"
)

// newFile creates a regular file that must not exist. Tests replace it to
// inject a write error.
var newFile = func(p string) (io.WriteCloser, error) {
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// Extract reads a tar whose single top-level entry may have any name and
// writes it at dest, which must not exist and whose parent must. The
// top-level entry becomes dest itself: a file is written to dest, a
// directory's contents beneath it. See the package comment for the rules.
//
// On any error, and when ctx ends, everything Extract created is removed
// before it returns; Tree.Removed says how many paths that was.
func Extract(ctx context.Context, r io.Reader, dest string, prog Progress) (Tree, error) {
	x := &extractor{ctx: ctx, dest: filepath.Clean(dest), prog: prog, created: map[string]bool{}, links: map[string]bool{}}
	err := x.run(r)
	if err == nil {
		return x.t, nil
	}
	if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = ctx.Err()
	}
	x.t.Removed = len(x.order)
	if cerr := x.cleanup(); cerr != nil {
		err = fmt.Errorf("%w; and what was written could not all be removed: %v", err, cerr)
	}
	return x.t, err
}

type extractor struct {
	ctx  context.Context
	dest string
	prog Progress
	t    Tree

	top     string          // the archive's top-level name
	created map[string]bool // archive-relative paths this extraction made ("" is dest)
	order   []string        // the same, in the order they were made
	dirs    []dirMode       // directories, whose modes are applied last
	links   map[string]bool // archive-relative symlink names, seen so far
	later   []*tar.Header   // symlinks and hard links, made last
}

type dirMode struct {
	rel  string
	mode fs.FileMode
}

func (x *extractor) local(rel string) string {
	if rel == "" {
		return x.dest
	}
	return filepath.Join(x.dest, filepath.FromSlash(rel))
}

func (x *extractor) made(rel string) {
	x.created[rel] = true
	x.order = append(x.order, rel)
}

func (x *extractor) run(r io.Reader) error {
	tr := tar.NewReader(r)
	seen := false
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the archive could not be read: %w", err)
		}
		name, err := CheckName(hdr.Name)
		if err != nil {
			return err
		}
		top, rel := splitTop(name)
		if !seen {
			x.top, seen = top, true
		} else if top != x.top {
			return fmt.Errorf("the archive holds more than one top-level entry (%s and %s): refused", escape.Name(x.top), escape.Name(top))
		}
		if l, ok := x.beneathLink(rel); ok {
			return fmt.Errorf("the archive writes %s beneath its own symlink %s: refused", escape.Name(name), escape.Name(joinRel(x.top, l)))
		}
		if err := x.entry(tr, hdr, rel); err != nil {
			return err
		}
		x.prog.report(&x.t)
	}
	if !seen {
		return errors.New("the archive was empty: nothing was written")
	}
	if len(x.order) == 0 && len(x.later) == 0 {
		return fmt.Errorf("nothing was written: %s is %s", escape.Name(x.top), skipReason)
	}
	if err := x.makeLinks(); err != nil {
		return err
	}
	return x.applyDirModes()
}

// beneathLink returns the symlink of the archive, seen so far, that rel
// lies strictly beneath ("" is the top-level entry itself).
func (x *extractor) beneathLink(rel string) (string, bool) {
	if rel == "" {
		return "", false
	}
	for a := parentRel(rel); ; a = parentRel(a) {
		if x.links[a] {
			return a, true
		}
		if a == "" {
			return "", false
		}
	}
}

func (x *extractor) entry(tr *tar.Reader, hdr *tar.Header, rel string) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return x.dir(rel, hdr)
	case tar.TypeReg, tar.TypeRegA:
		if err := x.parents(rel); err != nil {
			return err
		}
		return x.file(tr, rel, hdr)
	case tar.TypeSymlink:
		// A path already made under this name is refused when the links
		// are made: as the parent of entries, or as a name given twice.
		x.links[rel] = true
		x.later = append(x.later, hdr)
		return nil
	case tar.TypeLink:
		x.later = append(x.later, hdr)
		return nil
	default:
		// A device, a FIFO, a socket, or anything newer: never made.
		x.t.Skipped = append(x.t.Skipped, skipped(hdr.Name))
		return nil
	}
}

// parents makes the directories above rel that the archive did not list
// before it. Until the links are made, nothing under dest is a link, so
// nothing here can be followed out of it.
func (x *extractor) parents(rel string) error {
	if rel == "" {
		return nil
	}
	var missing []string
	for p := parentRel(rel); ; p = parentRel(p) {
		if x.created[p] {
			break
		}
		missing = append(missing, p)
		if p == "" {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := x.mkdir(missing[i]); err != nil {
			return err
		}
		x.dirs = append(x.dirs, dirMode{missing[i], 0o755})
	}
	return nil
}

func parentRel(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

func (x *extractor) mkdir(rel string) error {
	p := x.local(rel)
	if err := os.Mkdir(p, 0o700); err != nil {
		return x.createError(rel, err)
	}
	x.made(rel)
	x.t.Dirs++
	x.t.Entries++
	return nil
}

func (x *extractor) createError(rel string, err error) error {
	if errors.Is(err, fs.ErrExist) {
		if rel == "" {
			return fmt.Errorf("%s already exists: nothing is replaced", escape.Name(x.dest))
		}
		return fmt.Errorf("the archive names %s twice: refused", escape.Name(joinRel(x.top, rel)))
	}
	return err
}

func (x *extractor) dir(rel string, hdr *tar.Header) error {
	if x.created[rel] {
		// A directory listed again, or after an entry beneath it: it is
		// ours, and only its mode is taken.
		if !x.isDir(rel) {
			return fmt.Errorf("the archive names %s as a file and as a directory: refused", escape.Name(hdr.Name))
		}
	} else {
		if err := x.parents(rel); err != nil {
			return err
		}
		if err := x.mkdir(rel); err != nil {
			return err
		}
	}
	x.dirs = append(x.dirs, dirMode{rel, fs.FileMode(hdr.Mode) & fs.ModePerm})
	return nil
}

func (x *extractor) isDir(rel string) bool {
	fi, err := os.Lstat(x.local(rel))
	return err == nil && fi.IsDir()
}

func (x *extractor) file(tr *tar.Reader, rel string, hdr *tar.Header) error {
	p := x.local(rel)
	f, err := newFile(p)
	if err != nil {
		return x.createError(rel, err)
	}
	x.made(rel)
	_, err = io.Copy(f, &ctxReader{ctx: x.ctx, r: tr, n: func(n int) {
		x.t.Bytes += int64(n)
		x.prog.report(&x.t)
	}})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if x.ctx.Err() != nil {
			return x.ctx.Err()
		}
		return fmt.Errorf("writing %s: %w", escape.Name(p), err)
	}
	if err := os.Chmod(p, fs.FileMode(hdr.Mode)&fs.ModePerm); err != nil {
		return err
	}
	x.t.Files++
	x.t.Entries++
	return nil
}

// makeLinks makes the symlinks and hard links, after every file and
// directory, so that none of them was written through a link.
func (x *extractor) makeLinks() error {
	for _, hdr := range x.later {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		name, _ := CheckName(hdr.Name)
		_, rel := splitTop(name)
		// An entry the archive listed before the symlink it lies beneath
		// was refused nowhere else: it is refused here, before the link.
		if hdr.Typeflag == tar.TypeSymlink {
			for _, c := range x.order {
				if rel == "" || strings.HasPrefix(c, rel+"/") {
					return fmt.Errorf("the archive writes %s beneath its own symlink %s: refused", escape.Name(joinRel(x.top, c)), escape.Name(hdr.Name))
				}
			}
		}
		if err := x.parents(rel); err != nil {
			return err
		}
		p := x.local(rel)
		switch hdr.Typeflag {
		case tar.TypeSymlink:
			if strings.IndexByte(hdr.Linkname, 0) >= 0 {
				return fmt.Errorf("the archive's symlink %s has a target with a NUL byte: refused", escape.Name(hdr.Name))
			}
			if err := os.Symlink(hdr.Linkname, p); err != nil {
				return x.createError(rel, err)
			}
			x.made(rel)
			x.t.Links++
		case tar.TypeLink:
			target, err := x.linkTarget(hdr)
			if err != nil {
				return err
			}
			if err := os.Link(x.local(target), p); err != nil {
				return x.createError(rel, err)
			}
			x.made(rel)
			x.t.Files++
		}
		x.t.Entries++
		x.prog.report(&x.t)
	}
	return nil
}

// linkTarget checks a hard link: it must name, under the same top-level
// entry, a regular file this extraction wrote.
func (x *extractor) linkTarget(hdr *tar.Header) (string, error) {
	refuse := func() (string, error) {
		return "", fmt.Errorf("the archive's hard link %s points at %s, which is not a file it wrote inside the destination: refused",
			escape.Name(hdr.Name), escape.Name(hdr.Linkname))
	}
	name, err := CheckName(hdr.Linkname)
	if err != nil {
		return refuse()
	}
	top, rel := splitTop(name)
	if top != x.top || !x.created[rel] {
		return refuse()
	}
	fi, err := os.Lstat(x.local(rel))
	if err != nil || !fi.Mode().IsRegular() {
		return refuse()
	}
	return rel, nil
}

// applyDirModes sets each directory's mode once everything beneath it is
// written, deepest first: a directory without write permission would
// otherwise refuse its own entries. A directory listed twice takes the mode
// it was given last.
func (x *extractor) applyDirModes() error {
	last := map[string]fs.FileMode{}
	for _, d := range x.dirs {
		last[d.rel] = d.mode
	}
	rels := make([]string, 0, len(last))
	for r := range last {
		rels = append(rels, r)
	}
	sort.Slice(rels, func(i, j int) bool {
		if a, b := depth(rels[i]), depth(rels[j]); a != b {
			return a > b
		}
		return rels[i] < rels[j]
	})
	for _, r := range rels {
		if err := os.Chmod(x.local(r), last[r]); err != nil {
			return err
		}
	}
	return nil
}

func depth(rel string) int {
	if rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func joinRel(top, rel string) string {
	if rel == "" {
		return top
	}
	return top + "/" + rel
}

// cleanup removes what this extraction made, deepest first. A directory
// whose mode was already narrowed is opened up again so it can be emptied.
func (x *extractor) cleanup() error {
	var first error
	for i := len(x.order) - 1; i >= 0; i-- {
		rel := x.order[i]
		p := x.local(rel)
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		if parent := x.local(parentRel(rel)); rel != "" {
			if fi, err := os.Lstat(parent); err == nil && fi.IsDir() && x.created[parentRel(rel)] {
				_ = os.Chmod(parent, 0o700)
			}
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}
