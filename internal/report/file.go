package report

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The one place the desk writes a file. The rules (X9, X10):
//
//   - the file is created with mode 0600 — only its owner reads it;
//   - an existing path is refused, unless the caller passes force (the
//     command line's --force; the TUI never does), and a directory, or
//     anything that is not a regular file, is refused even then;
//   - the folder must exist: none is created;
//   - the bytes go to a hidden temporary file beside the target first, and
//     reach the target's name only once they are all written and synced —
//     by a hard link, which cannot replace a file that appeared meanwhile,
//     or, with force, a rename. On any error the temporary file is removed,
//     so no partial file is ever left under either name.

// WriteError is why an export was not written, in one sentence.
type WriteError struct {
	Path   string
	Reason string
	// Exists is set when the path is already taken by a file (force
	// would replace it).
	Exists bool
	Err    error
}

func (e *WriteError) Error() string { return "cannot write " + e.Path + ": " + e.Reason }
func (e *WriteError) Unwrap() error { return e.Err }

func werr(path, reason string, err error) error {
	return &WriteError{Path: path, Reason: reason, Err: err}
}

// ExpandPath turns what the operator typed into the absolute path that will
// be written: a leading ~ or ~/ is the home folder (and only that: ~user is
// refused), a relative path is taken from cwd.
func ExpandPath(p, home, cwd string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", errors.New("no path was given")
	case p == "~" || strings.HasPrefix(p, "~/"):
		if home == "" {
			return "", errors.New("the home folder is not known, so ~ cannot be expanded")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case strings.HasPrefix(p, "~"):
		return "", errors.New("only ~/ is expanded: name the folder in full")
	case !filepath.IsAbs(p):
		if cwd == "" {
			return "", errors.New("the current folder is not known; give an absolute path")
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), nil
}

// WriteFile writes data to path under the rules above.
func WriteFile(path string, data []byte, force bool) error {
	if path == "" {
		return werr(path, "no path was given", nil)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return werr(path, err.Error(), err)
	}
	if fi, err := os.Lstat(abs); err == nil {
		switch {
		case fi.IsDir():
			return werr(abs, "it is a folder; name a file inside it", fs.ErrExist)
		case fi.Mode()&fs.ModeSymlink == 0 && !fi.Mode().IsRegular():
			return werr(abs, "it is not a regular file", fs.ErrExist)
		case !force:
			return &WriteError{Path: abs, Reason: "a file is already there; nothing was overwritten", Exists: true, Err: fs.ErrExist}
		}
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		// (ENOTDIR: a parent is a file; said below, in those words.)
		return werr(abs, plain(err), err)
	}
	dir := filepath.Dir(abs)
	di, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return werr(abs, "the folder "+dir+" does not exist", err)
	case err != nil:
		return werr(abs, plain(err), err)
	case !di.IsDir():
		return werr(abs, dir+" is not a folder", fs.ErrInvalid)
	}

	tmp, err := os.CreateTemp(dir, ".docker-desk-export-*.tmp")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return werr(abs, "the folder "+dir+" cannot be written to (permission denied)", err)
		}
		return werr(abs, plain(err), err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return werr(abs, plain(err), err)
	}
	if _, err := tmp.Write(data); err != nil {
		return werr(abs, "writing failed: "+plain(err), err)
	}
	if err := tmp.Sync(); err != nil {
		return werr(abs, "writing failed: "+plain(err), err)
	}
	if err := tmp.Close(); err != nil {
		return werr(abs, "writing failed: "+plain(err), err)
	}

	if force {
		if err := os.Rename(tmpName, abs); err != nil {
			return werr(abs, plain(err), err)
		}
		done = true
		return nil
	}
	err = os.Link(tmpName, abs)
	switch {
	case err == nil:
		done = true
		os.Remove(tmpName)
		return nil
	case errors.Is(err, fs.ErrExist):
		return &WriteError{Path: abs, Reason: "a file appeared there while writing; nothing was overwritten", Exists: true, Err: err}
	}
	// A filesystem without hard links: create the name exclusively and
	// write it again, removing it if that fails part-way.
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &WriteError{Path: abs, Reason: "a file appeared there while writing; nothing was overwritten", Exists: true, Err: err}
		}
		return werr(abs, plain(err), err)
	}
	_, werr1 := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if e := errors.Join(werr1, serr, cerr); e != nil {
		os.Remove(abs)
		return werr(abs, "writing failed: "+plain(e), e)
	}
	return nil
}

// plain is an error's last clause: "open /x: permission denied" says the
// path twice once a WriteError names it.
func plain(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}
