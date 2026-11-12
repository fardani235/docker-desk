package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/transfer"
)

// ---- copying into a container (docs/DECISIONS.md W6) ---------------------------------

// CopySpec is a copy into a container as the form gives it: an absolute
// local source and the container path that becomes it (W3).
type CopySpec struct {
	Source string // absolute, on this machine
	Dest   string // absolute, in the container: the destination itself
}

// ContainerRef is the container a copy goes into.
type ContainerRef struct {
	ID   string
	Name string
}

// CopyStep is an OpCopyIn step's payload: what is read, where it goes and
// what the walk found when the plan was made.
type CopyStep struct {
	Source string
	Dest   string
	Scan   transfer.Tree
}

// CopyIn plans a copy of a local file or directory into a container. The
// source is walked first (transfer.Scan), off the UI's goroutine: a missing
// or unreadable source, the root "/", a source that is itself a special
// file and a destination that is not an absolute path inside the container
// are errors, so the form stays open with the reason. Nothing is a blocker:
// the daemon decides what happens at the destination.
func CopyIn(ctx context.Context, c ContainerRef, spec CopySpec) (Plan, error) {
	if c.ID == "" {
		return Plan{}, errors.New("no container to copy into")
	}
	dest := strings.TrimSpace(spec.Dest)
	if !path.IsAbs(dest) {
		return Plan{}, fmt.Errorf("the container path must be absolute (start with /), not %q", dest)
	}
	dest = path.Clean(dest)
	if dest == "/" {
		return Plan{}, errors.New("the container path is the destination itself, and / cannot be replaced: name a path inside it")
	}
	if !filepath.IsAbs(spec.Source) {
		return Plan{}, fmt.Errorf("internal error: the local source %q is not absolute", spec.Source)
	}
	src := filepath.Clean(spec.Source)
	scan, err := transfer.Scan(ctx, src)
	if err != nil {
		return Plan{}, err
	}
	shown := escape.Name(src)
	where := escape.Name(c.Name) + ":" + escape.Name(dest)
	p := Plan{
		Title:     "Copy into container",
		JobTitle:  "copy into container",
		JobTarget: where,
		Target:    Target{Kind: "container", Name: c.Name, ID: engine.ShortID(c.ID)},
		Tier:      TierConfirm,
		Steps: []Step{{
			Op: OpCopyIn, Kind: "container", ID: c.ID, Name: c.Name, IDFrom: -1,
			Copy: &CopyStep{Source: src, Dest: dest, Scan: scan},
		}},
	}
	p.Effects = append(p.Effects,
		fmt.Sprintf("From this machine: %s — %s, %s.", shown, scanWords(scan), humanBytes(scan.Bytes)),
		fmt.Sprintf("Into the container at %s: that path is the destination itself, not a folder the source is put in. Its parent must already exist.", escape.Name(dest)),
		"Files already at the destination with the same names are replaced; other files there are left alone. A directory is not replaced by a file, nor a file by a directory.",
		"Modes are kept; ownership is the daemon's default, not your user's. Symlinks are sent as links and never followed.",
		"The source is read again when the job runs: what it sends is reported there.",
	)
	if n := len(scan.Skipped); n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s skipped: %s", plural(n, "entry is", "entries are"), listSome(scan.Skipped, 3)))
	}
	return p, nil
}

func scanWords(t transfer.Tree) string {
	parts := []string{plural(t.Files, "file", "files")}
	if t.Dirs > 0 {
		parts = append(parts, plural(t.Dirs, "directory", "directories"))
	}
	if t.Links > 0 {
		parts = append(parts, plural(t.Links, "symlink", "symlinks"))
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(" (%s)", plural(t.Entries, "entry", "entries"))
}

func listSome(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, "; ")
	}
	return strings.Join(items[:n], "; ") + fmt.Sprintf("; and %d more", len(items)-n)
}

// errUploadEnded closes the pipe when the upload stopped before the archive
// was all written; it is never the error reported.
var errUploadEnded = errors.New("the upload ended before the archive was written")

// copyIn streams the source as a tar into the container: the walk writes
// into a pipe on its own goroutine while the request reads from it, so the
// source is never held whole. The first error from either side is the one
// returned; a local error is the cause of the upload's and wins over it.
func copyIn(ctx context.Context, eng engine.Engine, st Step, run *jobs.Run) error {
	cp := st.Copy
	if cp == nil {
		return errors.New("internal error: a copy step without its source and destination")
	}
	pr, pw := io.Pipe()
	var sent transfer.Tree
	var tarErr error
	done := make(chan struct{})
	var last time.Time
	total := cp.Scan.Bytes
	go func() {
		defer close(done)
		sent, tarErr = transfer.Tar(ctx, pw, cp.Source, path.Base(cp.Dest), func(t transfer.Tree) {
			if now := time.Now(); now.Sub(last) >= 100*time.Millisecond {
				last = now
				run.Status(fmt.Sprintf("sent %s · %s", humanBytes(t.Bytes), plural(t.Entries, "entry", "entries")))
				if total > 0 {
					run.Fraction(min(float64(t.Bytes)/float64(total), 0.99))
				}
			}
		})
		_ = pw.CloseWithError(tarErr) // nil: the request reads EOF
	}()
	err := eng.CopyToContainer(ctx, st.ID, cp.Dest, pr)
	_ = pr.CloseWithError(errUploadEnded)
	<-done
	for _, s := range sent.Skipped {
		run.Logf("  skipped %s", s)
	}
	switch {
	case tarErr != nil && !errors.Is(tarErr, errUploadEnded):
		run.Logf("  sent %s before the local error", humanBytes(sent.Bytes))
		return tarErr
	case err != nil:
		return err
	}
	run.Logf("  sent %s — %s — from %s to %s:%s", humanBytes(sent.Bytes), scanWords(sent),
		escape.Name(cp.Source), escape.Name(st.Name), escape.Name(cp.Dest))
	if sent.Entries != cp.Scan.Entries || sent.Bytes != cp.Scan.Bytes {
		run.Logf("  the source changed after the confirmation: it showed %s, %s", plural(cp.Scan.Entries, "entry", "entries"), humanBytes(cp.Scan.Bytes))
	}
	run.Status(fmt.Sprintf("sent %s · %s", humanBytes(sent.Bytes), plural(sent.Entries, "entry", "entries")))
	return nil
}
