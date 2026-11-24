package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// entries lists a folder, so a test can say nothing else was left in it.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}

func TestWriteFileCreates0600(t *testing.T) {
	// A permissive umask cannot widen the mode; a strict one is no excuse
	// for the test to pass by accident.
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := WriteFile(p, []byte("{}\n"), false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != "{}\n" {
		t.Errorf("content %q", b)
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("left behind: %v", got)
	}
}

func TestWriteFileRefusals(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.json")
	os.WriteFile(existing, []byte("evidence"), 0o644)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(existing, link)

	cases := []struct {
		name, path string
		force      bool
		reason     string
		exists     bool
	}{
		{"existing file", existing, false, "a file is already there; nothing was overwritten", true},
		{"symbolic link", link, false, "a file is already there", true},
		{"directory", sub, false, "it is a folder", false},
		{"directory, forced", sub, true, "it is a folder", false},
		{"missing parent", filepath.Join(dir, "nope", "x.json"), false, "does not exist", false},
		{"parent is a file", filepath.Join(existing, "x.json"), false, "is not a folder", false},
		{"no path", "", false, "no path was given", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := WriteFile(c.path, []byte("new"), c.force)
			var we *WriteError
			if !errors.As(err, &we) {
				t.Fatalf("got %v, want a WriteError", err)
			}
			if !strings.Contains(we.Error(), c.reason) || we.Exists != c.exists {
				t.Errorf("got %q (exists %v), want %q (exists %v)", we.Error(), we.Exists, c.reason, c.exists)
			}
			if strings.Count(we.Error(), "\n") != 0 {
				t.Errorf("not one line: %q", we.Error())
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "evidence" {
		t.Errorf("the existing file was changed: %q", b)
	}
	if got := strings.Join(entries(t, dir), ","); got != "link,old.json,sub" {
		t.Errorf("left behind: %s", got)
	}
	if got := entries(t, sub); len(got) != 0 {
		t.Errorf("left in the directory: %v", got)
	}
}

func TestWriteFileUnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0500 folder")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := WriteFile(filepath.Join(dir, "x.json"), []byte("x"), false)
	if err == nil || !strings.Contains(err.Error(), "cannot be written to (permission denied)") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("not a permission error underneath: %v", err)
	}
	os.Chmod(dir, 0o700)
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

func TestWriteFileForceReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	os.WriteFile(p, []byte("old"), 0o644)
	if err := WriteFile(p, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "new" || fi.Mode().Perm() != 0o600 {
		t.Errorf("content %q mode %v", b, fi.Mode().Perm())
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("left behind: %v", got)
	}
}

func TestExpandPath(t *testing.T) {
	cases := []struct{ in, want, err string }{
		{"~/x.json", "/home/u/x.json", ""},
		{"~", "/home/u", ""},
		{"  ~/a/../b.txt ", "/home/u/b.txt", ""},
		{"rel/x.json", "/work/rel/x.json", ""},
		{"/abs/x.json", "/abs/x.json", ""},
		{"~bob/x.json", "", "only ~/ is expanded"},
		{"", "", "no path"},
	}
	for _, c := range cases {
		got, err := ExpandPath(c.in, "/home/u", "/work")
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: got %q, %v; want error %q", c.in, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := ExpandPath("~/x", "", "/w"); err == nil {
		t.Error("~ expanded with no home folder")
	}
}

func TestDefaultPathAndExtensions(t *testing.T) {
	day := time.Date(2026, 11, 9, 23, 59, 0, 0, time.UTC)
	cases := []struct {
		kind, object string
		f            Format
		want         string
	}{
		{"findings", "python:3.12-slim", JSON, "~/docker-desk-findings-python-3.12-slim-20261109.json"},
		{"packages", "registry.example:5000/Team/app@sha256:ab", SPDX, "~/docker-desk-packages-registry.example-5000-team-app-sha256-ab-20261109.spdx.json"},
		{"space", "default", Text, "~/docker-desk-space-default-20261109.txt"},
		{"findings", "sha256:0123456789ab", CycloneDX, "~/docker-desk-findings-0123456789ab-20261109.cdx.json"},
		{"findings", "../../etc", JSON, "~/docker-desk-findings-etc-20261109.json"},
		{"findings", "", JSON, "~/docker-desk-findings-object-20261109.json"},
	}
	for _, c := range cases {
		if got := DefaultPath(c.kind, c.object, c.f, day); got != c.want {
			t.Errorf("DefaultPath(%q, %q) = %q, want %q", c.kind, c.object, got, c.want)
		}
	}
	p := "~/a.json"
	for _, f := range []Format{SPDX, CycloneDX, Text, JSON} {
		p = SwapExt(p, f)
		if !strings.HasSuffix(p, "a."+f.Ext()) {
			t.Errorf("SwapExt to %s: %q", f, p)
		}
	}
	if got := SwapExt("~/notes.md", SPDX); got != "~/notes.md" {
		t.Errorf("a path the operator named was changed: %q", got)
	}
}

// ---- WriteStream ----------------------------------------------------------------

// chunks writes n chunks of size bytes each, as a stream copied in pieces
// would, and returns what it wrote.
func chunks(n, size int) (func(io.Writer) error, *[]byte) {
	var sent []byte
	return func(w io.Writer) error {
		for i := 0; i < n; i++ {
			b := bytes.Repeat([]byte{byte('a' + i%26)}, size)
			if _, err := w.Write(b); err != nil {
				return err
			}
			sent = append(sent, b...)
		}
		return nil
	}, &sent
}

func TestWriteStreamCreates0600(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := t.TempDir()
	p := filepath.Join(dir, "image.tar")
	write, sent := chunks(9, 32<<10)
	if err := WriteStream(p, write); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, *sent) {
		t.Errorf("content: %d bytes, want the %d sent", len(b), len(*sent))
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("left behind: %v", got)
	}
}

// The source failing part-way — the daemon, a cancel — leaves nothing under
// either name, and its own error comes back, not a WriteError.
func TestWriteStreamSourceErrorLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "image.tar")
	for _, cause := range []error{errors.New("the daemon hung up"), context.Canceled} {
		var tmp string
		err := WriteStream(p, func(w io.Writer) error {
			w.Write(bytes.Repeat([]byte("x"), 64<<10))
			got := entries(t, dir)
			if len(got) != 1 || !strings.HasPrefix(got[0], ".docker-desk-export-") {
				t.Errorf("while writing, the folder has %v; want one hidden temporary file", got)
			} else {
				tmp = got[0]
			}
			return fmt.Errorf("reading the archive: %w", cause)
		})
		if !errors.Is(err, cause) {
			t.Fatalf("got %v, want %v underneath", err, cause)
		}
		var we *WriteError
		if errors.As(err, &we) {
			t.Errorf("the source's error came back as a WriteError: %v", err)
		}
		if tmp == "" {
			t.Fatal("the temporary file was never seen")
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Errorf("left behind: %v", got)
		}
	}
}

// A failing local write — a full disk — is the destination's failure: a
// WriteError saying writing failed, and nothing left. The temporary file is
// closed under write to make its next Write fail, as ENOSPC would.
func TestWriteStreamDiskErrorIsAWriteError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "image.tar")
	err := WriteStream(p, func(w io.Writer) error {
		fw := w.(*fileWriter)
		fw.f.Close() // the next write fails: "file already closed"
		_, err := io.Copy(w, strings.NewReader("archive"))
		return err
	})
	var we *WriteError
	if !errors.As(err, &we) || !strings.Contains(we.Reason, "writing failed") {
		t.Fatalf("got %v, want a WriteError saying writing failed", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
	// A write function that swallows the file's error does not publish a
	// short file either.
	err = WriteStream(p, func(w io.Writer) error {
		w.(*fileWriter).f.Close()
		w.Write([]byte("lost"))
		return nil
	})
	if !errors.As(err, &we) || !strings.Contains(we.Reason, "writing failed") {
		t.Fatalf("swallowed: got %v, want a WriteError", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

func TestWriteStreamRefusals(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.tar")
	os.WriteFile(existing, []byte("evidence"), 0o644)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(filepath.Join(dir, "nowhere"), link) // dangling: still taken
	cases := []struct {
		name, path, reason string
		exists             bool
	}{
		{"existing file", existing, "a file is already there; nothing was overwritten", true},
		{"dangling symbolic link", link, "a file is already there", true},
		{"directory", sub, "it is a folder", false},
		{"missing folder", filepath.Join(dir, "nope", "x.tar"), "does not exist", false},
		{"parent is a file", filepath.Join(existing, "x.tar"), "is not a folder", false},
		{"no path", "", "no path was given", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			err := WriteStream(c.path, func(w io.Writer) error { called = true; return nil })
			var we *WriteError
			if !errors.As(err, &we) {
				t.Fatalf("got %v, want a WriteError", err)
			}
			if !strings.Contains(we.Error(), c.reason) || we.Exists != c.exists {
				t.Errorf("got %q (exists %v), want %q (exists %v)", we.Error(), we.Exists, c.reason, c.exists)
			}
			if called {
				t.Error("the source was read for a path that is refused")
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "evidence" {
		t.Errorf("the existing file was changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a folder was created: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the symbolic link was followed: %v", err)
	}
	if got := strings.Join(entries(t, dir), ","); got != "link,old.tar,sub" {
		t.Errorf("left behind: %s", got)
	}
	if got := entries(t, sub); len(got) != 0 {
		t.Errorf("left in the directory: %v", got)
	}
}

// A file that appears at the destination while the stream is written is
// never replaced, and the temporary file goes.
func TestWriteStreamDestinationTakenMeanwhile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "image.tar")
	err := WriteStream(p, func(w io.Writer) error {
		w.Write([]byte("archive"))
		return os.WriteFile(p, []byte("theirs"), 0o644)
	})
	var we *WriteError
	if !errors.As(err, &we) || !we.Exists || !strings.Contains(we.Reason, "appeared there while writing") {
		t.Fatalf("got %v, want the appeared-meanwhile refusal", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "theirs" {
		t.Errorf("the file that appeared was replaced: %q", b)
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("left behind: %v", got)
	}
}
