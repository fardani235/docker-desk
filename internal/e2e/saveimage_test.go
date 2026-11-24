package e2e

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// imagesRow focuses Images, filters the list and waits for the named row
// to be the one described.
func (d *desk) imagesRow(filter, name string) {
	d.t.Helper()
	d.see("2 Containers (13)")
	d.keys("1", ptyscreen.Down, "2", "/")
	for _, r := range filter {
		d.keys(string(r))
	}
	d.keys(ptyscreen.Enter)
	d.see("3 Details: " + name)
}

// savedTar opens the written archive as a tar and returns its manifest's
// RepoTags: what `docker load` would name the image.
func savedTar(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var tags []string
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s is not a tar: %v", path, err)
		}
		if h.Name != "manifest.json" {
			continue
		}
		var man []struct{ RepoTags []string }
		if err := json.NewDecoder(tr).Decode(&man); err != nil {
			t.Fatalf("manifest.json: %v", err)
		}
		found = true
		for _, m := range man {
			tags = append(tags, m.RepoTags...)
		}
	}
	if !found {
		t.Fatalf("%s has no manifest.json", path)
	}
	return tags
}

// onlyFile waits for exactly one docker-desk-image-*.tar in dir and returns it.
func onlyFile(t *testing.T, dir, size string) string {
	t.Helper()
	var path string
	waitFor(t, "the saved archive", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "docker-desk-image-nginx-1.27-*.tar"))
		if len(m) != 1 {
			return false
		}
		fi, err := os.Stat(m[0])
		path = m[0]
		return err == nil && fi.Size() > 0 && (size == "" || fi.Size() == int64(len(size)))
	})
	return path
}

// TestSaveImageArchive saves nginx:1.27 through the real binary: w, Enter
// on the prefilled form, the job, and the file — a tar that is the fixture
// byte for byte, mode 0600, carrying the tag. Then the same in read-only
// mode at 80×24, with the same bytes and only GETs.
func TestSaveImageArchive(t *testing.T) {
	want := fixture.Legacy(fixture.DemoFiles("nginx:1.27")).Archive
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"65-save-image-form", 42, 150, nil},
		{"66-save-image-form-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
			d, home := launchHome(t, srv, tc.rows, tc.cols, tc.args...)
			d.imagesRow("nginx", "nginx:1.27")
			d.keys("2", "w")
			d.see("Save the image archive", "platform", "linux/amd64", "docker-desk-image-nginx-1.27-")
			d.shot(tc.name)
			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 0 {
				t.Fatalf("the form asked for the archive (%d)", n)
			}
			d.consent(ptyscreen.Enter)
			path := onlyFile(t, home, "")
			waitFor(t, "the whole archive", func() bool {
				fi, err := os.Stat(path)
				return err == nil && fi.Size() == int64(len(want))
			})
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("the file is not the fixture: %v (%d bytes, want %d)", err, len(got), len(want))
			}
			if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v", fi.Mode().Perm())
			}
			if tags := savedTar(t, path); len(tags) != 1 || tags[0] != "nginx:1.27" {
				t.Fatalf("the archive names %v", tags)
			}
			if names, _ := os.ReadDir(home); len(names) != 1 {
				t.Fatalf("left in the home folder: %v", names)
			}
			var gets []fakeengine.Request
			for _, r := range srv.Requests() {
				if r.Mutating() {
					t.Fatalf("saving sent %s", r)
				}
				if strings.HasSuffix(r.Path, "/get") {
					gets = append(gets, r)
				}
			}
			if len(gets) != 1 {
				t.Fatalf("archive requests: %v", gets)
			}
			if q, _ := url.ParseQuery(gets[0].Query); q.Get("names") != "nginx:1.27" || q.Get("platform") == "" {
				t.Fatalf("asked for %v", q)
			}
			d.keys("5")
			d.see("save image archive", "nginx:1.27")
			d.keys("q")
			d.quit()
		})
	}
}

// TestSaveImageArchiveCancel cancels a save from the Jobs pane while the
// archive streams: nothing is left in the folder, not even the temporary
// file, and the job says so.
func TestSaveImageArchiveCancel(t *testing.T) {
	big := make([]byte, 4<<20)
	_, _ = rand.Read(big)
	srv := startFake(t, func(s *fakeengine.Server) {
		s.SeedDemo()
		s.SetImageArchive("nginx:1.27", big)
	})
	srv.ExportChunkDelay = 60 * time.Millisecond // 128 chunks: about eight seconds
	d, home := launchHome(t, srv, 42, 150)
	d.imagesRow("nginx", "nginx:1.27")
	d.keys("2", "w")
	d.see("Save the image archive")
	d.consent(ptyscreen.Enter)
	waitFor(t, "part of the archive", func() bool {
		names, _ := os.ReadDir(home)
		return len(names) == 1 && strings.HasPrefix(names[0].Name(), ".docker-desk-export-")
	})
	d.keys("5", "c")
	d.see("cancelled")
	waitFor(t, "the partial file to go", func() bool {
		names, _ := os.ReadDir(home)
		return len(names) == 0
	})
	d.keys(ptyscreen.Enter)
	d.see("partial archive", "nothing remains at")
	d.shot("67-save-image-cancelled")
	d.keys(ptyscreen.Esc, "q") // nothing runs: no question
	d.quit()
	if names, _ := os.ReadDir(home); len(names) != 0 {
		t.Fatalf("left behind: %v", names)
	}
}
