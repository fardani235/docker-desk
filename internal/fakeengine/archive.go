package fakeengine

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/fardani235/docker-desk/internal/transfer"
)

// A container's filesystem, as far as `docker cp` sees it: a small
// in-memory tree of directories, files, symlinks and special files, and the
// two archive endpoints over it (docs/DECISIONS.md W8). It is not the
// image's tree — that is the image archive the Files view reads — and the
// two are only made to agree where the demo's change list says so.

// FileSpec is one path in a fake container's filesystem.
type FileSpec struct {
	Path    string      // absolute
	Mode    fs.FileMode // type bits and permissions; 0 means a 0644 file
	Content []byte
	Link    string // a symlink's target
}

type fileNode struct {
	mode    fs.FileMode
	content []byte
	link    string
	mtime   time.Time
}

// Upload is one PUT /containers/{id}/archive the fake accepted or refused.
type Upload struct {
	Container string // the container's name
	Path      string // the path= query
	Query     string
	Entries   []string // the tar's entry names, in order
	Err       string   // the refusal, when there was one
}

// fileEpoch is the modification time of every seeded path: fixed, so that
// archives and screens are the same on every run.
var fileEpoch = time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)

// fsLocked returns the container's tree, made on first use with the
// directories every Linux root has.
func (c *Container) fsLocked() map[string]*fileNode {
	if c.files == nil {
		c.files = map[string]*fileNode{}
		for _, d := range []string{"/", "/bin", "/dev", "/etc", "/home", "/root", "/run", "/srv", "/tmp", "/usr", "/var", "/var/lib"} {
			c.files[d] = &fileNode{mode: fs.ModeDir | 0o755, mtime: fileEpoch}
		}
		c.files["/tmp"].mode = fs.ModeDir | fs.ModeSticky | 0o777
		c.files["/root"].mode = fs.ModeDir | 0o700
	}
	return c.files
}

// SeedFiles puts paths into a container's filesystem, with the directories
// above them. A path already there is replaced.
func (s *Server) SeedFiles(ref string, specs ...FileSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		panic("fakeengine: SeedFiles on an unknown container " + ref)
	}
	tree := c.fsLocked()
	for _, sp := range specs {
		p := path.Clean("/" + sp.Path)
		mkdirAll(tree, path.Dir(p))
		mode := sp.Mode
		if mode == 0 {
			mode = 0o644
		}
		tree[p] = &fileNode{mode: mode, content: append([]byte(nil), sp.Content...), link: sp.Link, mtime: fileEpoch}
	}
}

func mkdirAll(tree map[string]*fileNode, dir string) {
	for d := dir; ; d = path.Dir(d) {
		if _, ok := tree[d]; !ok {
			tree[d] = &fileNode{mode: fs.ModeDir | 0o755, mtime: fileEpoch}
		}
		if d == "/" {
			return
		}
	}
}

// ContainerFiles lists the container's paths at and beneath root, sorted.
func (s *Server) ContainerFiles(ref, root string) []FileSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return nil
	}
	root = path.Clean("/" + root)
	var out []FileSpec
	for p, n := range c.fsLocked() {
		if p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/") {
			out = append(out, FileSpec{Path: p, Mode: n.mode, Content: append([]byte(nil), n.content...), Link: n.link})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ContainerFile is one path of the container's filesystem.
func (s *Server) ContainerFile(ref, p string) (FileSpec, bool) {
	for _, f := range s.ContainerFiles(ref, p) {
		if f.Path == path.Clean("/"+p) {
			return f, true
		}
	}
	return FileSpec{}, false
}

// Uploads returns every archive upload the fake received.
func (s *Server) Uploads() []Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Upload(nil), s.uploads...)
}

// recordWriteLocked adds what writing p does to the container's change
// list, as the daemon's diff would show it: p added, or changed if it was
// there, and every directory above it changed unless it is already listed.
func (c *Container) recordWriteLocked(tree map[string]*fileNode, p string) {
	listed := map[string]bool{}
	for _, ch := range c.Changes {
		listed[ch.Path] = true
	}
	var add []Change
	for d := path.Dir(p); d != "/"; d = path.Dir(d) {
		if listed[d] {
			break
		}
		if _, ok := tree[d]; ok {
			add = append([]Change{Changed(d)}, add...)
		} else {
			add = append([]Change{Added(d)}, add...)
		}
	}
	if !listed[p] {
		if _, ok := tree[p]; ok {
			add = append(add, Changed(p))
		} else {
			add = append(add, Added(p))
		}
	}
	c.Changes = append(c.Changes, add...)
}

// resolveLocked follows symlinks in every component of p but the last, as
// the daemon does, and returns the path it names. ok is false when a
// component does not exist or is not a directory.
func resolveLocked(tree map[string]*fileNode, p string) (string, bool) {
	p = path.Clean("/" + p)
	if p == "/" {
		return p, true
	}
	cur := "/"
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, part := range parts {
		next := path.Join(cur, part)
		if i == len(parts)-1 {
			return next, true
		}
		for hops := 0; ; hops++ {
			n, ok := tree[next]
			if !ok || hops > 16 {
				return "", false
			}
			if n.mode&fs.ModeSymlink == 0 {
				if !n.mode.IsDir() {
					return "", false
				}
				break
			}
			if path.IsAbs(n.link) {
				next = path.Clean(n.link)
			} else {
				next = path.Join(path.Dir(next), n.link)
			}
		}
		cur = next
	}
	return cur, true
}

func notInContainer(w http.ResponseWriter, p, ref string) {
	httpError(w, http.StatusNotFound, "Could not find the file %s in container %s", p, ref)
}

// statHeader is X-Docker-Container-Path-Stat: the stat, as JSON, base64.
func statHeader(name string, n *fileNode) string {
	st := container.PathStat{Name: name, Size: int64(len(n.content)), Mode: n.mode, Mtime: n.mtime, LinkTarget: n.link}
	if n.mode.IsDir() {
		st.Size = 4096
	}
	b, _ := json.Marshal(st)
	return base64.StdEncoding.EncodeToString(b)
}

// handleArchiveStat is HEAD /containers/{id}/archive: the stat alone, which
// `docker cp` asks for before an upload.
func (s *Server) handleArchiveStat(w http.ResponseWriter, r *http.Request, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query().Get("path")
	p, ok := resolveLocked(c.fsLocked(), q)
	n := c.fsLocked()[p]
	if !ok || n == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("X-Docker-Container-Path-Stat", statHeader(path.Base(p), n))
	w.WriteHeader(http.StatusOK)
}

// handleArchiveGet is GET /containers/{id}/archive?path=…: a tar whose
// single top-level entry is the path's base name, and its stat.
func (s *Server) handleArchiveGet(w http.ResponseWriter, r *http.Request, ref string) {
	q := r.URL.Query().Get("path")
	if q == "" {
		httpError(w, http.StatusBadRequest, "path is required")
		return
	}
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	tree := c.fsLocked()
	p, ok := resolveLocked(tree, q)
	n := tree[p]
	if !ok || n == nil {
		s.mu.Unlock()
		notInContainer(w, q, ref)
		return
	}
	if p == "/" {
		s.mu.Unlock()
		httpError(w, http.StatusBadRequest, "the fake daemon does not archive a container's whole root")
		return
	}
	base := path.Base(p)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var paths []string
	for q := range tree {
		if q == p || strings.HasPrefix(q, p+"/") && n.mode.IsDir() {
			paths = append(paths, q)
		}
	}
	sort.Strings(paths)
	for _, q := range paths {
		e := tree[q]
		name := base + strings.TrimPrefix(q, p)
		h := &tar.Header{Name: name, Mode: int64(e.mode.Perm()), ModTime: e.mtime, Format: tar.FormatPAX}
		switch {
		case e.mode.IsDir():
			h.Typeflag, h.Name = tar.TypeDir, name+"/"
		case e.mode&fs.ModeSymlink != 0:
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.mode&fs.ModeNamedPipe != 0:
			h.Typeflag = tar.TypeFifo
		case e.mode&fs.ModeCharDevice != 0:
			h.Typeflag = tar.TypeChar
		case e.mode&fs.ModeDevice != 0:
			h.Typeflag = tar.TypeBlock
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.content))
		}
		_ = tw.WriteHeader(h)
		if h.Size > 0 {
			_, _ = tw.Write(e.content)
		}
	}
	_ = tw.Close()
	stat := statHeader(base, n)
	s.emit("container", "archive-path", c.ID, map[string]string{"name": c.Name, "path": p})
	s.mu.Unlock()

	w.Header().Set("X-Docker-Container-Path-Stat", stat)
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	// Streamed in pieces, with ArchiveChunkDelay between them, so that a
	// copy can be watched and cancelled.
	for b := buf.Bytes(); len(b) > 0; {
		k := min(len(b), 32<<10)
		if _, err := w.Write(b[:k]); err != nil {
			return
		}
		b = b[k:]
		if s.ArchiveChunkDelay > 0 && len(b) > 0 {
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			select {
			case <-time.After(s.ArchiveChunkDelay):
			case <-r.Context().Done():
				return
			case <-s.closed:
				return
			}
		}
	}
}

// handleArchivePut is PUT /containers/{id}/archive?path=…: the tar is
// extracted relative to path, which must be an existing directory. Names
// are held to the same rules as the desk's own extractor; same-named
// entries are replaced; a directory is not replaced by a file, nor a file by
// a directory, when noOverwriteDirNonDir is set. The upload is checked
// whole before anything is changed.
func (s *Server) handleArchivePut(w http.ResponseWriter, r *http.Request, ref string) {
	q := r.URL.Query()
	dest := q.Get("path")
	up := Upload{Path: dest, Query: r.URL.RawQuery}
	type item struct {
		h    *tar.Header
		name string
		body []byte
	}
	var items []item
	tr := tar.NewReader(r.Body)
	var readErr error
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
		up.Entries = append(up.Entries, h.Name)
		name, err := transfer.CheckName(h.Name)
		if err != nil {
			readErr = err
			break
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			readErr = err
			break
		}
		items = append(items, item{h, name, body})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	refuse := func(status int, format string, args ...any) {
		up.Err = fmt.Sprintf(format, args...)
		s.uploads = append(s.uploads, up)
		httpError(w, status, "%s", up.Err)
	}
	c := s.findContainerLocked(ref)
	if c == nil {
		refuse(http.StatusNotFound, "No such container: %s", ref)
		return
	}
	up.Container = c.Name
	if readErr != nil {
		refuse(http.StatusBadRequest, "%v", readErr)
		return
	}
	tree := c.fsLocked()
	root, ok := resolveLocked(tree, dest)
	if n := tree[root]; !ok || n == nil {
		refuse(http.StatusNotFound, "Could not find the file %s in container %s", dest, ref)
		return
	} else if !n.mode.IsDir() {
		refuse(http.StatusBadRequest, "extraction point is not a directory")
		return
	}
	noClobber := boolParam(r, "noOverwriteDirNonDir")
	planned := map[string]bool{} // directories this upload makes
	for _, it := range items {
		p := path.Join(root, it.name)
		isDir := it.h.Typeflag == tar.TypeDir
		if old, ok := tree[p]; ok && noClobber && !planned[p] {
			switch {
			case old.mode.IsDir() && !isDir:
				refuse(http.StatusBadRequest, "cannot overwrite directory %q with non-directory %q", p, it.name)
				return
			case !old.mode.IsDir() && isDir:
				refuse(http.StatusBadRequest, "cannot overwrite non-directory %q with directory %q", p, it.name)
				return
			}
		}
		for d := path.Dir(p); d != root && d != "/"; d = path.Dir(d) {
			if n, ok := tree[d]; ok && !n.mode.IsDir() && !planned[d] {
				refuse(http.StatusBadRequest, "cannot create %q: %s is not a directory", p, d)
				return
			}
		}
		if isDir {
			planned[p] = true
		}
	}
	for _, it := range items {
		p := path.Join(root, it.name)
		c.recordWriteLocked(tree, p)
		mkdirAll(tree, path.Dir(p))
		perm := fs.FileMode(it.h.Mode) & fs.ModePerm
		n := &fileNode{mode: perm, mtime: it.h.ModTime}
		switch it.h.Typeflag {
		case tar.TypeDir:
			n.mode |= fs.ModeDir
		case tar.TypeSymlink:
			n.mode, n.link = fs.ModeSymlink|0o777, it.h.Linkname
		case tar.TypeLink:
			if t, err := transfer.CheckName(it.h.Linkname); err == nil {
				if src := tree[path.Join(root, t)]; src != nil {
					n.content = append([]byte(nil), src.content...)
				}
			}
		case tar.TypeFifo:
			n.mode |= fs.ModeNamedPipe
		case tar.TypeChar:
			n.mode |= fs.ModeDevice | fs.ModeCharDevice
		case tar.TypeBlock:
			n.mode |= fs.ModeDevice
		default:
			n.content = it.body
		}
		if old, ok := tree[p]; ok && old.mode.IsDir() && n.mode.IsDir() {
			old.mode, old.mtime = n.mode, n.mtime // a directory keeps what is in it
			continue
		}
		if old, ok := tree[p]; ok && old.mode.IsDir() {
			for k := range tree {
				if strings.HasPrefix(k, p+"/") {
					delete(tree, k)
				}
			}
		}
		tree[p] = n
	}
	s.uploads = append(s.uploads, up)
	s.emit("container", "extract-to-dir", c.ID, map[string]string{"name": c.Name, "path": root})
	w.WriteHeader(http.StatusOK)
}
