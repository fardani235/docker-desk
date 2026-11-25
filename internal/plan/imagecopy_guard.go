package plan

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/distribution/reference"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
)

// The archive guard (docs/DECISIONS.md, round twenty-six, K6).
//
// A load applies whatever the archive names: every image in it, under every
// reference it lists, moving any of those the target has given another
// image. The archive is the source daemon's to write, and the confirmation
// promised one image under one tag — or none. So the copy does not hand the
// source's bytes to the target unread: as they pass, the guard reads the
// tar's own structure (never the layers' contents) and, in the three files
// from which a daemon takes an image's names — Docker's manifest.json, the
// OCI layout's index.json, and the legacy repositories file — allows only
// the reference that was confirmed. Anything else stops the stream *before*
// the bytes that complete it are passed on: the target is left with a
// request that broke off, which it cannot load.
//
// The guard is strict about what it does not recognise. An archive it
// cannot follow as a tar, a naming file that is not a regular file or not
// the JSON a daemon writes, an entry placed through a link, a second image,
// bytes after the archive's end: each is refused rather than passed on with
// a shrug. A real daemon's archive has none of these.

// guardMaxMeta bounds a naming file the guard reads whole. A daemon's are a
// few hundred bytes to a few kilobytes.
const guardMaxMeta = 8 << 20

// guardNames are the files a daemon reads an image's names from.
var guardNames = map[string]bool{"manifest.json": true, "index.json": true, "repositories": true}

// archiveGuard follows an image archive as it streams. feed is called with
// each piece before it is passed on; it returns once the parser has taken
// the whole piece and asked for more, or has reached a verdict.
type archiveGuard struct {
	// want is the one reference the archive may name, in its familiar
	// form; "" means it may name none.
	want string

	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte // the piece being fed; the parser empties it
	// waiting is set by the parser when it has emptied buf and needs more.
	waiting bool
	eof     bool  // no more pieces will come
	done    bool  // the parser has ended
	err     error // its verdict, once done; nil is "nothing to object to"
	// ids are the IDs under which a store may hold the archive's image:
	// the manifests index.json lists (a containerd store's name for it)
	// and the config manifest.json points at (a classic store's). They
	// are only ever inspected on the target, and only in the ID form.
	ids []string
}

// candidates returns the image IDs the archive itself suggests, in the
// order they were read, without duplicates.
func (g *archiveGuard) candidates() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.ids...)
}

// note records an ID a store may give the image, if it has an ID's form.
func (g *archiveGuard) note(id string) {
	if !imageIDForm.MatchString(id) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, have := range g.ids {
		if have == id {
			return
		}
	}
	if len(g.ids) < 8 {
		g.ids = append(g.ids, id)
	}
}

func newArchiveGuard(want string) *archiveGuard {
	g := &archiveGuard{want: want}
	g.cond = sync.NewCond(&g.mu)
	go g.parse()
	return g
}

// Read is the parser's side: it blocks until a piece is fed.
func (g *archiveGuard) Read(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for len(g.buf) == 0 && !g.eof {
		g.waiting = true
		g.cond.Broadcast()
		g.cond.Wait()
	}
	g.waiting = false
	if len(g.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// feed gives the parser one piece and waits for it to be read through. A
// non-nil result is the guard's refusal: the piece must not be passed on.
func (g *archiveGuard) feed(p []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		if g.err != nil {
			return g.err
		}
		// The archive has ended; a tar may be padded with zeros, and
		// nothing else may follow it.
		for _, b := range p {
			if b != 0 {
				g.err = errors.New("there is data after the archive's end")
				return g.err
			}
		}
		return nil
	}
	g.buf = p
	g.cond.Broadcast()
	for !g.done && !(g.waiting && len(g.buf) == 0) {
		g.cond.Wait()
	}
	rest := g.buf // what the parser left when it reached the archive's end
	g.buf = nil
	if g.done && g.err == nil {
		for _, b := range rest {
			if b != 0 {
				g.err = errors.New("there is data after the archive's end")
				break
			}
		}
	}
	return g.err
}

// finish says no more pieces will come and returns the final verdict. It is
// safe to call more than once, and it is how the parser's goroutine is
// released on every way out.
func (g *archiveGuard) finish() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eof = true
	g.cond.Broadcast()
	for !g.done {
		g.cond.Wait()
	}
	return g.err
}

func (g *archiveGuard) parse() {
	err := g.scan()
	g.mu.Lock()
	g.done, g.err = true, err
	g.cond.Broadcast()
	g.mu.Unlock()
}

// scan reads the archive's entries. It returns nil at the archive's end
// when nothing was objected to.
func (g *archiveGuard) scan() error {
	tr := tar.NewReader(g)
	var links []string // the paths of every link entry so far
	entries := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			if entries == 0 {
				return errors.New("it is empty: the source sent no archive")
			}
			return nil
		}
		entries++
		if err != nil {
			return fmt.Errorf("it cannot be read as a tar archive, or ended early (%s)", escape.Name(err.Error()))
		}
		// Lower case: a daemon on a file system that ignores case would
		// open Manifest.json as manifest.json.
		name := strings.ToLower(path.Clean("/" + h.Name)[1:])
		// Nothing may be placed through a link: a daemon that unpacks the
		// archive would follow it, and the entry would land under another
		// name than the one read here.
		for _, l := range links {
			if name == l || strings.HasPrefix(name, l+"/") {
				return fmt.Errorf("it places %s through a link, which no daemon's archive does", escape.Name(clipName(h.Name)))
			}
		}
		switch h.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			if guardNames[name] {
				return fmt.Errorf("its %s is a link, not a file", name)
			}
			links = append(links, name)
			if len(links) > 4096 {
				return errors.New("it holds more links than any image archive")
			}
			continue
		case tar.TypeReg:
		default:
			if guardNames[name] {
				return fmt.Errorf("its %s is not a regular file", name)
			}
			continue
		}
		if !guardNames[name] {
			continue // a layer, a config, a blob: not read
		}
		if h.Size > guardMaxMeta {
			return fmt.Errorf("its %s is %d bytes, larger than any daemon writes", name, h.Size)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("it ended early, inside %s (%s)", name, escape.Name(err.Error()))
		}
		switch name {
		case "manifest.json":
			err = g.checkManifest(body)
		case "index.json":
			err = g.checkIndex(body)
		case "repositories":
			err = g.checkRepositories(body)
		}
		if err != nil {
			return err
		}
	}
}

// allowed reports whether name, as an archive writes a reference, is the
// one the copy was confirmed for.
func (g *archiveGuard) allowed(name string) bool {
	if g.want == "" || name == "" || strings.ContainsAny(name, " \t\r\n") {
		return false
	}
	named, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return false
	}
	if _, digested := named.(reference.Digested); digested {
		return false
	}
	return reference.FamiliarString(reference.TagNameOnly(named)) == g.want
}

// other is the refusal for a reference the archive names beside, or
// instead of, the confirmed one.
func (g *archiveGuard) other(file, name string) error {
	if g.want == "" {
		return fmt.Errorf("its %s names %s, and the copy was confirmed for an image that arrives untagged", file, escape.Name(clipName(name)))
	}
	return fmt.Errorf("its %s names %s, which is not %s: the copy was confirmed for that one reference", file, escape.Name(clipName(name)), g.want)
}

// checkManifest is Docker's manifest.json: one image, and no reference but
// the confirmed one.
func (g *archiveGuard) checkManifest(body []byte) error {
	var man []struct {
		Config   string
		RepoTags []string
	}
	if err := json.Unmarshal(body, &man); err != nil {
		return errors.New("its manifest.json is not the document a daemon writes")
	}
	if len(man) != 1 {
		return fmt.Errorf("its manifest.json lists %d images: the copy was confirmed for one", len(man))
	}
	for _, t := range man[0].RepoTags {
		if !g.allowed(t) {
			return g.other("manifest.json", t)
		}
	}
	// A classic store names the image by its config's digest, which is the
	// config's file name in either layout.
	cfg := strings.TrimSuffix(path.Base(man[0].Config), ".json")
	g.note("sha256:" + cfg)
	return nil
}

// checkIndex is the OCI layout's index.json: whatever it lists is named — if
// at all — as the confirmed reference. It may list several descriptors: a
// daemon with the containerd image store writes the platform's manifest and,
// beside it, the attestation manifest that refers to it, unnamed. An unnamed
// descriptor creates or moves no reference, so the number of descriptors is
// not judged; their names are.
func (g *archiveGuard) checkIndex(body []byte) error {
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return errors.New("its index.json is not the document a daemon writes")
	}
	for _, m := range idx.Manifests {
		if n, ok := m.Annotations["io.containerd.image.name"]; ok && !g.allowed(n) {
			return g.other("index.json", n)
		}
		// The OCI reference name is the tag alone, or the whole reference.
		if n, ok := m.Annotations["org.opencontainers.image.ref.name"]; ok {
			_, tag := engine.SplitRef(g.want)
			if g.want == "" || (n != tag && !g.allowed(n)) {
				return g.other("index.json", n)
			}
		}
		// A containerd store names the image by this digest; a manifest
		// that only refers to another (an attestation) is not the image.
		if _, refers := m.Annotations["io.containerd.manifest.subject"]; !refers {
			g.note(m.Digest)
		}
	}
	return nil
}

// checkRepositories is the legacy repositories file: repository → tag → ID.
func (g *archiveGuard) checkRepositories(body []byte) error {
	var repos map[string]map[string]string
	if err := json.Unmarshal(body, &repos); err != nil {
		return errors.New("its repositories file is not the document a daemon writes")
	}
	for repo, tags := range repos {
		for tag := range tags {
			if name := repo + ":" + tag; !g.allowed(name) {
				return g.other("repositories", name)
			}
		}
	}
	return nil
}

// clipName bounds a name of the archive's that is quoted in a refusal.
func clipName(s string) string {
	if len(s) > 200 {
		return strings.ToValidUTF8(s[:200], "") + "…"
	}
	return s
}
