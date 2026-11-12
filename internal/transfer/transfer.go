// Package transfer is the host side of a copy between this machine and a
// container (docs/DECISIONS.md, round seventeen, W2): walking a local path,
// writing it as a tar, and extracting a tar to a local path. It has no UI and
// no engine; every rule about names, links, special files, overwriting and
// clean-up is enforced here, in one tested place.
//
// The rules:
//
//   - A name is cleaned with path.Clean. An empty or absolute name, a ".."
//     component or a NUL is refused: the archive is trying to escape.
//   - Directories and regular files are written first, symlinks and hard
//     links last, so nothing is written through a link the same archive
//     made; an entry beneath a symlink of the archive is refused.
//   - A symlink is recreated as a link and never followed. A hard link is
//     recreated only to a regular file this extraction wrote, and is sent as
//     a regular file when archiving.
//   - A character or block device, a FIFO or a socket is never opened or
//     recreated: it is counted in Tree.Skipped, its name escaped.
//   - The destination is created by Extract itself — O_EXCL for a file,
//     Mkdir for a directory — so an existing path is refused atomically,
//     never replaced.
//   - On an error or a cancelled context Extract removes everything it
//     created before returning.
//   - Permission bits (0777) are applied with Chmod, so the umask does not
//     narrow them; set-id and sticky bits are not, and ownership, times and
//     extended attributes are not restored.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/fardani235/docker-desk/internal/escape"
)

// Tree is what a copy holds, or has handled so far.
type Tree struct {
	// Entries is every entry written or counted: directories, files and
	// links. Skipped entries are not among them.
	Entries int
	Files   int // regular files (and hard links)
	Dirs    int
	Links   int // symlinks
	Bytes   int64
	// Skipped are the entries that are not a regular file, a directory or
	// a symlink, each as "name: why", the name escaped (escape.Name).
	Skipped []string
	// Removed is set by a failed or cancelled Extract: how many paths it
	// had created and removed again.
	Removed int
}

// Progress is told the running totals after each entry and as a file's
// bytes are copied. It is called on the copying goroutine; callers that
// draw must throttle and hand the figures over themselves.
type Progress func(Tree)

func (p Progress) report(t *Tree) {
	if p != nil {
		p(*t)
	}
}

// skipReason is the one sentence W9 gives a skipped entry.
const skipReason = "not a regular file, directory or symlink"

func skipped(name string) string { return escape.Name(name) + ": " + skipReason }

// special reports whether a mode is one Tar and Extract never open.
func special(m fs.FileMode) bool {
	return m&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0
}

// ---- scanning ---------------------------------------------------------------------

// Scan counts what a copy-in of src would send, without opening a file. A
// missing source, the root "/", a source that is itself a special file and
// a directory that cannot be read are errors; special files beneath a
// directory are counted in Skipped.
func Scan(ctx context.Context, src string) (Tree, error) {
	var t Tree
	src = filepath.Clean(src)
	if err := checkSource(src); err != nil {
		return t, err
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return localError(p, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return localError(p, err)
		}
		count(&t, p, src, fi)
		return nil
	})
	return t, err
}

// checkSource is what refuses a source before anything is walked.
func checkSource(src string) error {
	if src == string(filepath.Separator) || filepath.Dir(src) == src {
		return errors.New("the source is the root of this machine's filesystem: name a file or a directory inside it")
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return localError(src, err)
	}
	if special(fi.Mode()) {
		return fmt.Errorf("%s is %s: there is nothing to copy", escape.Name(src), skipReason)
	}
	return nil
}

func count(t *Tree, p, root string, fi fs.FileInfo) {
	switch m := fi.Mode(); {
	case m.IsDir():
		t.Dirs++
		t.Entries++
	case m&fs.ModeSymlink != 0:
		t.Links++
		t.Entries++
	case m.IsRegular():
		t.Files++
		t.Entries++
		t.Bytes += fi.Size()
	default:
		rel, _ := filepath.Rel(filepath.Dir(root), p)
		t.Skipped = append(t.Skipped, skipped(filepath.ToSlash(rel)))
	}
}

// localError says in one sentence what went wrong on this machine.
func localError(p string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist", escape.Name(p))
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s cannot be read: permission denied", escape.Name(p))
	}
	return err
}

// ---- names ------------------------------------------------------------------------

// CheckName applies the archive name rules to one entry name and returns it
// cleaned, without a trailing slash: an empty or absolute name, a ".."
// component and a NUL are refused.
func CheckName(name string) (string, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("the archive holds a name with a NUL byte (%s): refused", escape.Name(name))
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("the archive holds an absolute name, %s: refused, nothing is written outside the destination", escape.Name(name))
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("the archive holds a name that climbs out of the destination, %s: refused", escape.Name(name))
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("the archive holds an entry with no name (%q): refused", name)
	}
	return clean, nil
}

// splitTop separates an archive name into its top-level entry and the rest.
func splitTop(name string) (top, rel string) {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// ---- reading with a context -------------------------------------------------------

// ctxReader stops a copy when its context ends and counts what passes.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
	n   func(int)
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(b)
	if n > 0 && c.n != nil {
		c.n(n)
	}
	return n, err
}
