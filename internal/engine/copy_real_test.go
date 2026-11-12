package engine_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/transfer"
)

// TestRealDaemonCopyReadOnly is copy out against a real daemon, through the
// read-only engine (whose HTTP hook refuses anything but GET and HEAD before
// it is sent): one path of one container is read with CopyFromContainer and
// extracted at a new local path, and the result is compared — names, types,
// permission bits, link targets and bytes — with what the real `docker cp`
// writes for the same path to another new local path (also a GET). That is
// the wire semantics of W3 for the way out: the top-level entry is the
// path's base name, and it lands at exactly the path named.
//
// The way in is a mutation. The test guard refuses, for every test, to open
// a real daemon read-write (B3), so it is not sent here: the read-only
// engine's refusal of CopyToContainer is asserted instead, and the way in
// is proven against the fake, and the fake against the real `docker cp`
// (make crosscheck).
//
//	DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
//	DOCKER_DESK_REAL_COPY_CONTAINER=<name> [DOCKER_DESK_REAL_COPY_PATH=/etc] \
//	go test ./internal/engine/ -run TestRealDaemonCopyReadOnly -v
func TestRealDaemonCopyReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	name := os.Getenv("DOCKER_DESK_REAL_COPY_CONTAINER")
	if host == "" || name == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST and DOCKER_DESK_REAL_COPY_CONTAINER to run the read-only copy check against a real daemon")
	}
	src := os.Getenv("DOCKER_DESK_REAL_COPY_PATH")
	if src == "" {
		src = "/etc"
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if !engine.IsReadOnly(eng) {
		t.Fatal("the engine is not read-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	a, err := eng.CopyFromContainer(ctx, name, src)
	if err != nil {
		t.Fatal(err)
	}
	if a.Stat.Name != path.Base(src) {
		t.Errorf("the stat names %q, not %q", a.Stat.Name, path.Base(src))
	}
	dir := t.TempDir()
	ours := filepath.Join(dir, "desk-copy")
	tree, err := transfer.Extract(ctx, a.Content, ours, nil)
	a.Content.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s:%s (%s, %v): %d files, %d directories, %d links, %d bytes, %d skipped",
		name, src, a.Stat.Name, a.Stat.Mode, tree.Files, tree.Dirs, tree.Links, tree.Bytes, len(tree.Skipped))
	if a.Stat.Mode.IsRegular() && a.Stat.Size != tree.Bytes {
		t.Errorf("the stat says %d bytes, %d were written", a.Stat.Size, tree.Bytes)
	}

	theirs := filepath.Join(dir, "cli-copy")
	cmd := exec.CommandContext(ctx, "docker", "-H", host, "cp", name+":"+src, theirs)
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("the docker CLI could not copy for comparison: %v\n%s", err, out)
	}
	if a, b := describeTree(t, ours), describeTree(t, theirs); !reflect.DeepEqual(a, b) {
		t.Errorf("the desk's copy and docker cp's differ:\ndesk %q\ncli  %q", a, b)
	}

	// The way in is refused before the wire.
	if err := eng.CopyToContainer(ctx, name, "/tmp/docker-desk-never-written", strings.NewReader("")); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("a copy in through the read-only engine: %v", err)
	}
}

// describeTree lists a local tree as the comparison needs it: special files
// left out (the desk skips them, the CLI makes them), permission bits only
// (the desk does not apply set-id bits), and no owners or times.
func describeTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		m := fi.Mode()
		if m&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		line := rel + " " + (m & (fs.ModeType | fs.ModePerm)).String()
		switch {
		case m&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			line += " -> " + to
		case m.IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %d bytes sha256:%x", len(b), sha256.Sum256(b))
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}
