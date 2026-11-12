package engine

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func webFake(t *testing.T, readOnly bool) (*fakeengine.Server, Engine) {
	t.Helper()
	srv, eng := startFake(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27"})
		s.SeedFiles("shop-web-1", fakeengine.DemoContainerFiles()...)
	})
	if readOnly {
		ro, err := New(Options{Host: srv.Host(), ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ro.Close() })
		return srv, ro
	}
	return srv, eng
}

func tarNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

// ---- 2.1 ----------------------------------------------------------------------------

func TestCopyFromContainer(t *testing.T) {
	srv, eng := webFake(t, false)
	ctx := ctxT(t)
	id := srv.ContainerID("shop-web-1")

	a, err := eng.CopyFromContainer(ctx, id, "/var/lib/nginx/state.db")
	if err != nil {
		t.Fatal(err)
	}
	if a.Stat.Name != "state.db" || a.Stat.Size != 4096 || a.Stat.Mode != 0o640 || a.Stat.IsDir() {
		t.Fatalf("stat %+v", a.Stat)
	}
	if got := tarNames(t, a.Content); len(got) != 1 || got[0] != "state.db" {
		t.Fatalf("stream %q", got)
	}
	a.Content.Close()

	a, err = eng.CopyFromContainer(ctx, id, "/etc/nginx")
	if err != nil || !a.Stat.IsDir() || a.Stat.Name != "nginx" {
		t.Fatalf("directory: %+v %v", a.Stat, err)
	}
	if got := tarNames(t, a.Content); len(got) != 5 || got[0] != "nginx/" {
		t.Fatalf("directory stream %q", got)
	}
	a.Content.Close()

	a, err = eng.CopyFromContainer(ctx, id, "/bin/sh")
	if err != nil || a.Stat.Mode&fs.ModeSymlink == 0 || a.Stat.LinkTarget != "/usr/bin/bash" {
		t.Fatalf("symlink: %+v %v", a.Stat, err)
	}
	a.Content.Close()

	_, err = eng.CopyFromContainer(ctx, id, "/nope")
	if KindOf(err) != KindNotFound || !strings.Contains(err.Error(), "Could not find the file /nope") {
		t.Fatalf("missing path: kind %v, %v", KindOf(err), err)
	}
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("a copy out sent %d mutating requests", n)
	}
}

// ---- 2.2 ----------------------------------------------------------------------------

func TestCopyToContainer(t *testing.T) {
	srv, eng := webFake(t, false)
	ctx := ctxT(t)
	id := srv.ContainerID("shop-web-1")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "app.conf", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3})
	_, _ = tw.Write([]byte("on\n"))
	_ = tw.Close()
	if err := eng.CopyToContainer(ctx, id, "/etc/nginx/app.conf", &buf); err != nil {
		t.Fatal(err)
	}
	// The query: the destination's parent, no overwriting of a directory
	// by a file, no ownership copied.
	var q url.Values
	for _, r := range srv.Requests() {
		if r.Method == "PUT" {
			q, _ = url.ParseQuery(r.Query)
		}
	}
	if q.Get("path") != "/etc/nginx" || q.Get("noOverwriteDirNonDir") != "true" || q.Has("copyUIDGID") {
		t.Fatalf("query %v", q)
	}
	ups := srv.Uploads()
	if len(ups) != 1 || len(ups[0].Entries) != 1 || ups[0].Entries[0] != "app.conf" {
		t.Fatalf("the fake received %+v", ups)
	}
	if f, ok := srv.ContainerFile("shop-web-1", "/etc/nginx/app.conf"); !ok || string(f.Content) != "on\n" || f.Mode != 0o600 {
		t.Fatalf("file %+v %v", f, ok)
	}

	// A missing parent is the daemon's refusal.
	err := eng.CopyToContainer(ctx, id, "/srv/none/app.conf", strings.NewReader(""))
	if KindOf(err) != KindNotFound || !strings.Contains(err.Error(), "Could not find the file /srv/none") {
		t.Fatalf("missing parent: %v", err)
	}
	// "/" and a relative path are refused before a request.
	before := len(srv.Requests())
	for _, d := range []string{"/", "etc/x", ""} {
		if err := eng.CopyToContainer(ctx, id, d, strings.NewReader("")); err == nil {
			t.Errorf("CopyToContainer(%q) was accepted", d)
		}
	}
	if len(srv.Requests()) != before {
		t.Fatal("a refused destination sent a request")
	}
}

// ---- 2.3 ----------------------------------------------------------------------------

func TestReadOnlyCopy(t *testing.T) {
	srv, eng := webFake(t, true)
	ctx := ctxT(t)
	id := srv.ContainerID("shop-web-1")
	a, err := eng.CopyFromContainer(ctx, id, "/etc/nginx/nginx.conf")
	if err != nil {
		t.Fatalf("a read-only copy out: %v", err)
	}
	if got := tarNames(t, a.Content); len(got) != 1 {
		t.Fatalf("stream %q", got)
	}
	a.Content.Close()
	if err := eng.CopyToContainer(ctx, id, "/tmp/x", strings.NewReader("")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only copy in: %v", err)
	}
	// And beneath the wrapper, the request hook.
	if err := eng.(*readOnlyEngine).Engine.CopyToContainer(ctx, id, "/tmp/x", strings.NewReader("")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("the request hook let a copy in through: %v", err)
	}
	if n := len(srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating requests reached the daemon", n)
	}
}
