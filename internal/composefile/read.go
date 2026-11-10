package composefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// MaxSize is the largest file Read accepts: 1 MiB.
const MaxSize = 1 << 20

// Read refusals. Each is a distinct error that errors.Is can name; the UI
// turns each into one sentence.
var (
	ErrNotFound   = errors.New("no file at that path")
	ErrDirectory  = errors.New("the path is a directory, not a regular file")
	ErrNotRegular = errors.New("the path is not a regular file (a device, pipe or socket)")
	ErrTooLarge   = fmt.Errorf("the file is larger than %d KiB", MaxSize>>10)
	ErrUnreadable = errors.New("the file cannot be opened")
	ErrRelative   = errors.New("the recorded path is not absolute")
)

// ReadError is a refusal for a path.
type ReadError struct {
	Path string
	Err  error // one of the Err* refusals above
	// Cause is the operating system's reason, for ErrUnreadable.
	Cause error
}

func (e *ReadError) Error() string {
	if e.Cause != nil {
		var pe *fs.PathError
		if errors.As(e.Cause, &pe) {
			return fmt.Sprintf("%s: %v (%v)", e.Path, e.Err, pe.Err)
		}
		return fmt.Sprintf("%s: %v (%v)", e.Path, e.Err, e.Cause)
	}
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

func (e *ReadError) Unwrap() error { return e.Err }

// Read returns the contents of the regular file at path, at most MaxSize
// bytes. It stats the path before opening it, so a directory, device, pipe
// or oversized file is refused without being opened; the descriptor it then
// opens (read-only, non-blocking) is checked again, so a path swapped in
// between is refused as well.
func Read(path string) ([]byte, error) {
	b, _, err := ReadInfo(path)
	return b, err
}

// ReadInfo is Read, also returning what the opened file's stat says (its
// write time is reported, as a fact).
func ReadInfo(path string) ([]byte, fs.FileInfo, error) {
	fail := func(e error, cause error) ([]byte, fs.FileInfo, error) {
		return nil, nil, &ReadError{Path: path, Err: e, Cause: cause}
	}
	if !isAbs(path) {
		return fail(ErrRelative, nil)
	}
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fail(ErrNotFound, nil)
		}
		return fail(ErrUnreadable, err)
	}
	if err := check(st); err != nil {
		return fail(err, nil)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fail(ErrNotFound, nil)
		}
		return fail(ErrUnreadable, err)
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return fail(ErrUnreadable, err)
	}
	if err := check(got); err != nil {
		return fail(err, nil)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return fail(ErrUnreadable, err)
	}
	if len(b) > MaxSize {
		return fail(ErrTooLarge, nil) // it grew after the stat
	}
	return b, got, nil
}

func check(st fs.FileInfo) error {
	switch {
	case st.IsDir():
		return ErrDirectory
	case !st.Mode().IsRegular():
		return ErrNotRegular
	case st.Size() > MaxSize:
		return ErrTooLarge
	}
	return nil
}

// isAbs is filepath.IsAbs for the paths compose records: absolute on the
// machine that ran it.
func isAbs(p string) bool {
	if len(p) > 0 && p[0] == '/' {
		return true
	}
	// C:\… or C:/… on Windows.
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
