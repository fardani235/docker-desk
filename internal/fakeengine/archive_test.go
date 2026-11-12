package fakeengine

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func archiveGet(t *testing.T, s *Server, ref, p string) (*httptest.ResponseRecorder, container.PathStat, []tar.Header, map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.47/containers/"+ref+"/archive?path="+url.QueryEscape(p), nil))
	var st container.PathStat
	var hdrs []tar.Header
	bodies := map[string]string{}
	if rec.Code != http.StatusOK {
		return rec, st, nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(rec.Header().Get("X-Docker-Container-Path-Stat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(rec.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		hdrs = append(hdrs, *h)
		bodies[h.Name] = string(b)
	}
	return rec, st, hdrs, bodies
}

func names(hs []tar.Header) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

func demoWeb(t *testing.T) *Server {
	t.Helper()
	s := New()
	s.AddContainer(ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27"})
	s.SeedFiles("shop-web-1", DemoContainerFiles()...)
	return s
}

// ---- 2.4 GET ----------------------------------------------------------------------

func TestArchiveGet(t *testing.T) {
	s := demoWeb(t)
	// A file: one entry named for its base, and its stat.
	rec, st, hs, bodies := archiveGet(t, s, "shop-web-1", "/var/lib/nginx/state.db")
	if rec.Code != 200 || st.Name != "state.db" || st.Size != 4096 || st.Mode != 0o640 {
		t.Fatalf("file: %d %+v", rec.Code, st)
	}
	if !reflect.DeepEqual(names(hs), []string{"state.db"}) || hs[0].Mode != 0o640 || len(bodies["state.db"]) != 4096 {
		t.Fatalf("file entries %q", names(hs))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
		t.Fatalf("content type %q", ct)
	}
	// A directory: base/ and everything beneath, with a symlink as a link.
	_, st, hs, bodies = archiveGet(t, s, "shop-web-1", "/etc/nginx")
	want := []string{"nginx/", "nginx/conf.d/", "nginx/conf.d/default.conf", "nginx/current.conf", "nginx/nginx.conf"}
	if !st.Mode.IsDir() || st.Name != "nginx" || !reflect.DeepEqual(names(hs), want) {
		t.Fatalf("directory: %+v %q", st, names(hs))
	}
	if hs[3].Typeflag != tar.TypeSymlink || hs[3].Linkname != "nginx.conf" || !strings.HasPrefix(bodies["nginx/nginx.conf"], "user nginx;") {
		t.Fatalf("directory entries: %+v", hs[3])
	}
	// A symlink itself: the link, never its target.
	_, st, hs, _ = archiveGet(t, s, "shop-web-1", "/bin/sh")
	if st.Mode&fs.ModeSymlink == 0 || st.LinkTarget != "/usr/bin/bash" || len(hs) != 1 || hs[0].Typeflag != tar.TypeSymlink {
		t.Fatalf("symlink: %+v %+v", st, hs)
	}
	// A FIFO is in the archive as a FIFO: the desk skips it, not the fake.
	_, _, hs, _ = archiveGet(t, s, "shop-web-1", "/run")
	if got := names(hs); !reflect.DeepEqual(got, []string{"run/", "run/nginx.fifo", "run/nginx.pid"}) || hs[1].Typeflag != tar.TypeFifo {
		t.Fatalf("run: %q", got)
	}
	// A path through a symlinked directory is resolved; the last component
	// is not.
	s.SeedFiles("shop-web-1", FileSpec{Path: "/conf", Mode: fs.ModeSymlink | 0o777, Link: "/etc/nginx"})
	if rec, st, _, _ := archiveGet(t, s, "shop-web-1", "/conf/nginx.conf"); rec.Code != 200 || st.Name != "nginx.conf" {
		t.Fatalf("through a link: %d %+v", rec.Code, st)
	}
	// Missing: the daemon's 404 and words.
	rec, _, _, _ = archiveGet(t, s, "shop-web-1", "/nope/x")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "Could not find the file /nope/x in container shop-web-1") {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	if rec, _, _, _ := archiveGet(t, s, "no-such", "/etc"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "No such container") {
		t.Fatalf("no container: %d %s", rec.Code, rec.Body)
	}
	// A GET changes nothing.
	if n := len(s.Mutations()); n != 0 {
		t.Fatalf("%d mutations", n)
	}
}

func TestArchiveStatHEAD(t *testing.T) {
	s := demoWeb(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/v1.47/containers/shop-web-1/archive?path=/etc/nginx", nil))
	if rec.Code != 200 || rec.Header().Get("X-Docker-Container-Path-Stat") == "" || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/v1.47/containers/shop-web-1/archive?path=/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("HEAD missing: %d", rec.Code)
	}
}

// ---- 2.5 PUT ----------------------------------------------------------------------

func tarOf(t *testing.T, entries ...tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = h.Linkname // the test's way to pass a body
			h.Linkname = ""
			h.Size = int64(len(body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return &buf
}

func put(t *testing.T, s *Server, ref, p string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1.47/containers/"+ref+"/archive?noOverwriteDirNonDir=true&path="+url.QueryEscape(p), body))
	return rec
}

func reg(name, body string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeReg, Linkname: body}
}
func dirH(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o750} }

func TestArchivePut(t *testing.T) {
	s := demoWeb(t)
	// A file to a file: exactly the path named.
	if rec := put(t, s, "shop-web-1", "/etc/nginx", tarOf(t, reg("nginx.conf", "replaced\n"))); rec.Code != 200 {
		t.Fatalf("put file: %d %s", rec.Code, rec.Body)
	}
	if f, _ := s.ContainerFile("shop-web-1", "/etc/nginx/nginx.conf"); string(f.Content) != "replaced\n" {
		t.Fatalf("not replaced: %q", f.Content)
	}
	// A directory into a directory: its entries created or replaced, the
	// others left alone.
	rec := put(t, s, "shop-web-1", "/etc", tarOf(t, dirH("nginx/"), reg("nginx/conf.d/new.conf", "n\n"), tar.Header{Name: "nginx/l", Typeflag: tar.TypeSymlink, Linkname: "/x"}))
	if rec.Code != 200 {
		t.Fatalf("put dir: %d %s", rec.Code, rec.Body)
	}
	var got []string
	for _, f := range s.ContainerFiles("shop-web-1", "/etc/nginx") {
		got = append(got, f.Path)
	}
	want := []string{"/etc/nginx", "/etc/nginx/conf.d", "/etc/nginx/conf.d/default.conf", "/etc/nginx/conf.d/new.conf", "/etc/nginx/current.conf", "/etc/nginx/l", "/etc/nginx/nginx.conf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree %q", got)
	}
	if f, _ := s.ContainerFile("shop-web-1", "/etc/nginx"); f.Mode != fs.ModeDir|0o750 {
		t.Fatalf("directory mode %v", f.Mode)
	}
	if f, _ := s.ContainerFile("shop-web-1", "/etc/nginx/l"); f.Link != "/x" {
		t.Fatalf("link %+v", f)
	}
	// Recorded, with its entries.
	ups := s.Uploads()
	if len(ups) != 2 || ups[1].Path != "/etc" || ups[1].Container != "shop-web-1" || !reflect.DeepEqual(ups[1].Entries, []string{"nginx/", "nginx/conf.d/new.conf", "nginx/l"}) {
		t.Fatalf("uploads %+v", ups)
	}
	if n := s.CountRequests(http.MethodPut, `^/containers/shop-web-1/archive$`); n != 2 {
		t.Fatalf("Requests() has %d uploads", n)
	}
	// The change list follows, as `docker diff` would.
	s.mu.Lock()
	var diff []string
	for _, ch := range s.findContainerLocked("shop-web-1").Changes {
		diff = append(diff, "CAD"[ch.Kind:ch.Kind+1]+" "+ch.Path)
	}
	s.mu.Unlock()
	wantDiff := []string{"C /etc", "C /etc/nginx", "C /etc/nginx/nginx.conf", "C /etc/nginx/conf.d", "A /etc/nginx/conf.d/new.conf", "A /etc/nginx/l"}
	if !reflect.DeepEqual(diff, wantDiff) {
		t.Fatalf("changes %q", diff)
	}
}

func TestArchivePutRefuses(t *testing.T) {
	for _, c := range []struct {
		name   string
		path   string
		body   []tar.Header
		status int
		want   string
	}{
		{"missing parent", "/srv/none", []tar.Header{reg("a", "x")}, 404, "Could not find the file /srv/none"},
		{"not a directory", "/etc/nginx/nginx.conf", []tar.Header{reg("a", "x")}, 400, "extraction point is not a directory"},
		{"file over directory", "/etc", []tar.Header{reg("nginx", "x")}, 400, "cannot overwrite directory"},
		{"directory over file", "/etc/nginx", []tar.Header{dirH("nginx.conf/")}, 400, "cannot overwrite non-directory"},
		{"traversal", "/tmp", []tar.Header{reg("../etc/passwd", "x")}, 400, "climbs out"},
		{"absolute", "/tmp", []tar.Header{reg("/etc/passwd", "x")}, 400, "absolute name"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := demoWeb(t)
			before := s.ContainerFiles("shop-web-1", "/")
			rec := put(t, s, "shop-web-1", c.path, tarOf(t, c.body...))
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if after := s.ContainerFiles("shop-web-1", "/"); !reflect.DeepEqual(before, after) {
				t.Fatal("a refused upload changed the filesystem")
			}
			if ups := s.Uploads(); len(ups) != 1 || ups[0].Err == "" {
				t.Fatalf("the refusal was not recorded: %+v", ups)
			}
		})
	}
}
