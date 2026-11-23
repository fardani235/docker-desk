package plan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

func copyRig(t *testing.T) (*rig, ContainerRef) {
	t.Helper()
	r := newRig(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27"})
		s.SeedFiles("shop-web-1", fakeengine.DemoContainerFiles()...)
	})
	return r, ContainerRef{ID: r.srv.ContainerID("shop-web-1"), Name: "shop-web-1"}
}

func assets(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "assets")
	for _, d := range []string{"js", "css"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{"js/app.js": "let a = 1;\n", "css/site.css": "body{}\n", "index.html": "<h1>x</h1>\n"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("js/app.js", filepath.Join(root, "main.js")); err != nil {
		t.Fatal(err)
	}
	return root
}

// ---- 3.1 ----------------------------------------------------------------------------

func TestCopyInPlan(t *testing.T) {
	src := assets(t)
	c := ContainerRef{ID: "abcdef0123456789", Name: "shop-web-1"}
	p, err := CopyIn(context.Background(), c, CopySpec{Source: src, Dest: "/srv/assets/"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Runnable() || p.Tier != TierConfirm || len(p.Steps) != 1 {
		t.Fatalf("plan %+v", p)
	}
	st := p.Steps[0]
	if st.Op != OpCopyIn || st.ID != c.ID || st.Copy == nil || st.Copy.Dest != "/srv/assets" || st.Copy.Source != src {
		t.Fatalf("step %+v %+v", st, st.Copy)
	}
	if sc := st.Copy.Scan; sc.Files != 3 || sc.Dirs != 3 || sc.Links != 1 || sc.Entries != 7 || sc.Bytes != 29 {
		t.Fatalf("scan %+v", sc)
	}
	text := strings.Join(p.Effects, "\n")
	for _, want := range []string{src, "3 files, 3 directories, 1 symlink (7 entries)", "29 B", "/srv/assets", "the destination itself", "parent must already exist", "are replaced", "ownership is the daemon's default"} {
		if !strings.Contains(text, want) {
			t.Errorf("effects lack %q:\n%s", want, text)
		}
	}
	if p.JobTarget != "shop-web-1:/srv/assets" || p.Target.Name != "shop-web-1" {
		t.Fatalf("labels %q %+v", p.JobTarget, p.Target)
	}
}

func TestCopyInRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	c := ContainerRef{ID: "abc", Name: "c"}
	cases := []struct{ src, dest, want string }{
		{filepath.Join(dir, "missing"), "/tmp/x", "does not exist"},
		{"/", "/tmp/x", "root of this machine's filesystem"},
		{filepath.Join(dir, "fifo"), "/tmp/x", "not a regular file, directory or symlink"},
		{dir, "/", "/ cannot be replaced"},
		{dir, "tmp/x", "must be absolute"},
		{dir, "", "must be absolute"},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, struct{ src, dest, want string }{locked, "/tmp/x", "permission denied"})
	}
	for _, tc := range cases {
		p, err := CopyIn(context.Background(), c, CopySpec{Source: tc.src, Dest: tc.dest})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CopyIn(%q, %q) = %v, want %q", tc.src, tc.dest, err, tc.want)
		}
		if len(p.Steps) != 0 {
			t.Errorf("CopyIn(%q, %q) built %d steps", tc.src, tc.dest, len(p.Steps))
		}
	}
}

// ---- 3.2 ----------------------------------------------------------------------------

func TestCopyInExecutes(t *testing.T) {
	r, c := copyRig(t)
	src := assets(t)
	p, err := CopyIn(context.Background(), c, CopySpec{Source: src, Dest: "/srv/assets"})
	if err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job %v: %s\n%s", j.State, j.Err, strings.Join(j.Log, "\n"))
	}
	var got []string
	for _, f := range r.srv.ContainerFiles("shop-web-1", "/srv/assets") {
		line := f.Path + " " + f.Mode.String()
		if f.Mode&fs.ModeSymlink != 0 {
			line += " -> " + f.Link
		} else if f.Mode.IsRegular() {
			line += " " + string(f.Content)
		}
		got = append(got, line)
	}
	want := []string{
		"/srv/assets drwxr-xr-x",
		"/srv/assets/css drwxr-xr-x",
		"/srv/assets/css/site.css -rw-r----- body{}\n",
		"/srv/assets/index.html -rw-r----- <h1>x</h1>\n",
		"/srv/assets/js drwxr-xr-x",
		"/srv/assets/js/app.js -rw-r----- let a = 1;\n",
		"/srv/assets/main.js Lrwxrwxrwx -> js/app.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the container's tree:\n%q\nwant\n%q", got, want)
	}
	ups := r.srv.Uploads()
	if len(ups) != 1 || ups[0].Path != "/srv" || ups[0].Entries[0] != "assets/" {
		t.Fatalf("upload %+v", ups)
	}
	log := strings.Join(j.Log, "\n")
	if !strings.Contains(log, "sent 29 B — 3 files, 3 directories, 1 symlink (7 entries) — from "+src+" to shop-web-1:/srv/assets") {
		t.Fatalf("log:\n%s", log)
	}
	// A single file, to a different name: exactly that path.
	one := filepath.Join(t.TempDir(), "a.conf")
	_ = os.WriteFile(one, []byte("listen 81;\n"), 0o600)
	p, _ = CopyIn(context.Background(), c, CopySpec{Source: one, Dest: "/etc/nginx/b.conf"})
	if j := r.run(p); j.State != jobs.Succeeded {
		t.Fatalf("file: %s", j.Err)
	}
	if f, ok := r.srv.ContainerFile("shop-web-1", "/etc/nginx/b.conf"); !ok || string(f.Content) != "listen 81;\n" || f.Mode != 0o600 {
		t.Fatalf("file %+v", f)
	}
	if _, ok := r.srv.ContainerFile("shop-web-1", "/etc/nginx/b.conf/a.conf"); ok {
		t.Fatal("the file was nested")
	}
}

func TestCopyInFailures(t *testing.T) {
	r, c := copyRig(t)
	src := assets(t)
	// The daemon refuses: its words are the job's error.
	p, _ := CopyIn(context.Background(), c, CopySpec{Source: src, Dest: "/srv/none/assets"})
	j := r.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "Could not find the file /srv/none in container") {
		t.Fatalf("missing parent: %v %q", j.State, j.Err)
	}
	// A file over a directory.
	one := filepath.Join(t.TempDir(), "nginx")
	_ = os.WriteFile(one, []byte("x"), 0o600)
	p, _ = CopyIn(context.Background(), c, CopySpec{Source: one, Dest: "/etc/nginx"})
	if j := r.run(p); j.State != jobs.Failed || !strings.Contains(j.Err, "cannot overwrite directory") {
		t.Fatalf("file over directory: %v %q", j.State, j.Err)
	}
	// The source disappears between the confirmation and the job: the
	// local error, not the daemon's, is reported.
	p, _ = CopyIn(context.Background(), c, CopySpec{Source: src, Dest: "/srv/assets"})
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if j := r.run(p); j.State != jobs.Failed || !strings.Contains(j.Err, "does not exist") {
		t.Fatalf("vanished source: %v %q", j.State, j.Err)
	}
	if _, ok := r.srv.ContainerFile("shop-web-1", "/srv/assets"); ok {
		t.Fatal("something was written for a source that is gone")
	}
}

// ---- 3.3 ----------------------------------------------------------------------------

func TestCopyInNamesPathsNeverContents(t *testing.T) {
	r, c := copyRig(t)
	const sentinel = "SENTINEL-CONTENTS-7f3a"
	dir := filepath.Join(t.TempDir(), "secrets")
	_ = os.Mkdir(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "token"), []byte(sentinel), 0o600)
	_ = syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600)
	p, err := CopyIn(context.Background(), c, CopySpec{Source: dir, Dest: "/root/secrets"})
	if err != nil {
		t.Fatal(err)
	}
	j := r.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("%v %s", j.State, j.Err)
	}
	all := strings.Join(append(append([]string{p.Title, p.JobTitle, p.JobTarget, describe(p.Steps[0]), j.Title, j.Target, j.Status, j.Err}, p.Effects...), j.Log...), "\n")
	if strings.Contains(all, sentinel) {
		t.Fatalf("a file's contents reached the plan or the job:\n%s", all)
	}
	for _, want := range []string{"shop-web-1", dir, "/root/secrets", "skipped secrets/pipe: not a regular file, directory or symlink"} {
		if !strings.Contains(all, want) {
			t.Errorf("the plan and job do not name %q:\n%s", want, all)
		}
	}
	if d := describe(p.Steps[0]); d != "copy "+dir+" into shop-web-1:/root/secrets" {
		t.Errorf("describe = %q", d)
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "1 entry is skipped") {
		t.Errorf("warnings %q", p.Warnings)
	}
}
