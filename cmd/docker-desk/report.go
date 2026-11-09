package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/report/collect"
)

// spaceObject is the --report-object that names the daemon's disk account.
// No image reference begins with a colon, so it cannot shadow one.
const spaceObject = ":space"

// reportFlags are --report, --report-format, --report-object and --force.
type reportFlags struct {
	path, format, object string
	force                bool
}

// check validates the flags before anything is asked of the daemon, and
// reports whether this run is a headless export.
func (r *reportFlags) check(checkFlag bool) (bool, error) {
	if r.path == "" {
		switch {
		case r.format != "":
			return false, errors.New("--report-format needs --report PATH")
		case r.object != "":
			return false, errors.New("--report-object needs --report PATH")
		case r.force:
			return false, errors.New("--force applies only to --report; the workspace never replaces a file")
		}
		return false, nil
	}
	if checkFlag {
		return false, errors.New("--check and --report cannot be used together")
	}
	if strings.TrimSpace(r.object) == "" {
		return false, errors.New("--report needs --report-object: an image reference or ID, or :space")
	}
	if r.format == "" {
		r.format = string(report.JSON)
	}
	f, err := report.ParseFormat(r.format)
	if err != nil {
		return false, err
	}
	r.format = string(f)
	if r.object == spaceObject && f != report.JSON && f != report.Text {
		return false, fmt.Errorf("%s holds packages only; the Space account (:space) is written as json or text", f)
	}
	return true, nil
}

// runReport is the headless export: the same requests and the same
// document as the TUI's `w`, written to one path, then exit. 0 when the file
// is written; 1 when it is not, with nothing left at the path.
func runReport(ctx context.Context, eng engine.Engine, info engine.Info, r reportFlags, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	path, err := report.ExpandPath(r.path, home, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "docker-desk: not exported: %v.\n", err)
		return 1
	}
	// Refuse what will be refused before reading the archive, which can
	// take minutes; WriteFile checks again when it writes.
	if fi, err := os.Lstat(path); err == nil {
		switch {
		case fi.IsDir():
			fmt.Fprintf(stderr, "docker-desk: not exported: %s is a folder; name a file inside it.\n", path)
			return 1
		case !r.force:
			fmt.Fprintf(stderr, "docker-desk: not exported: %s already exists; nothing was overwritten (--force replaces it).\n", path)
			return 1
		}
	}

	var doc report.Document
	if r.object == spaceObject {
		s, err := collect.FetchSpace(ctx, eng, version)
		if err != nil {
			return reportFailed(ctx, stderr, err)
		}
		doc.Space = &s
	} else {
		img, err := collect.FetchImage(ctx, eng, info, version, r.object)
		if err != nil {
			return reportFailed(ctx, stderr, err)
		}
		doc.Image = &img
	}
	f := report.Format(r.format)
	data, err := report.Render(f, doc)
	if err != nil {
		return reportFailed(ctx, stderr, err)
	}
	if err := report.WriteFile(path, data, r.force); err != nil {
		var we *report.WriteError
		if errors.As(err, &we) && we.Exists {
			fmt.Fprintf(stderr, "docker-desk: not exported: %v (--force replaces it).\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "docker-desk: not exported: %v.\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (%s, %d bytes, mode 0600)\n", path, f, len(data))
	return 0
}

func reportFailed(ctx context.Context, stderr io.Writer, err error) int {
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "docker-desk: interrupted; nothing was written.")
		return 130
	}
	fmt.Fprintf(stderr, "docker-desk: not exported: %v; nothing was written.\n", err)
	return 1
}
