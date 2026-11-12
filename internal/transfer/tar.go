package transfer

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/fardani235/docker-desk/internal/escape"
)

// Tar writes src as a tar whose single top-level entry is name: a file is
// one entry called name, a directory is name/ and everything beneath it as
// name/…. Regular files are streamed, never held whole; a symlink is written
// as a link and never followed; a hard link is written as a regular file; a
// special file is skipped and listed. Headers carry the permission bits and
// modification time, uid and gid 0 and no user names (W10), in the PAX format
// so that long names survive.
//
// The tree is read as the walk reaches it: a file that changes size while it
// is read is an error, and the totals are what was actually written.
func Tar(ctx context.Context, w io.Writer, src, name string, prog Progress) (Tree, error) {
	var t Tree
	src = filepath.Clean(src)
	if err := checkSource(src); err != nil {
		return t, err
	}
	name, err := CheckName(name)
	if err != nil {
		return t, err
	}
	tw := tar.NewWriter(w)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		entry := name
		if rel != "." {
			entry = path.Join(name, filepath.ToSlash(rel))
		}
		if err := writeEntry(ctx, tw, p, entry, fi, &t, prog); err != nil {
			return err
		}
		prog.report(&t)
		return nil
	})
	if err != nil {
		return t, err
	}
	return t, tw.Close()
}

func writeEntry(ctx context.Context, tw *tar.Writer, p, entry string, fi fs.FileInfo, t *Tree, prog Progress) error {
	m := fi.Mode()
	hdr := &tar.Header{
		Name:    entry,
		Mode:    int64(m.Perm()),
		ModTime: fi.ModTime().Truncate(1e9),
		Format:  tar.FormatPAX,
	}
	switch {
	case m.IsDir():
		hdr.Typeflag, hdr.Name = tar.TypeDir, entry+"/"
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		t.Dirs++
		t.Entries++
		return nil
	case m&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return localError(p, err)
		}
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, target
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		t.Links++
		t.Entries++
		return nil
	case m.IsRegular():
		return writeFile(ctx, tw, p, hdr, fi, t, prog)
	default:
		t.Skipped = append(t.Skipped, skipped(entry))
		return nil
	}
}

func writeFile(ctx context.Context, tw *tar.Writer, p string, hdr *tar.Header, fi fs.FileInfo, t *Tree, prog Progress) error {
	f, err := openNoFollow(p)
	if err != nil {
		return localError(p, err)
	}
	defer f.Close()
	// What was opened must be what was walked: a path replaced by
	// something else since is not sent under the walked one's header.
	now, err := f.Stat()
	if err != nil {
		return localError(p, err)
	}
	if !now.Mode().IsRegular() || !os.SameFile(fi, now) {
		return fmt.Errorf("%s changed while the copy was reading it: nothing more was sent", escape.Name(p))
	}
	hdr.Typeflag, hdr.Size = tar.TypeReg, now.Size()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.CopyN(tw, &ctxReader{ctx: ctx, r: f, n: func(n int) {
		t.Bytes += int64(n)
		prog.report(t)
	}}, hdr.Size)
	if err == io.EOF {
		return fmt.Errorf("%s shrank while the copy was reading it: nothing more was sent", escape.Name(p))
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return localError(p, err)
	}
	t.Files++
	t.Entries++
	return nil
}
