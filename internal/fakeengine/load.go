package fakeengine

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/jsonstream"
)

// POST /images/load: the other end of GET /images/get (openspec change
// copy-image-between-daemons). The fake reads the archive whole, finds the
// image in Docker's manifest.json — which both layouts a daemon writes
// carry — and the image's config, and registers the image under the
// archive's references. As on the daemon, a reference that names another
// image here is moved without a refusal.
//
// It is a classic image store: the loaded image's ID is the digest of its
// config, whatever ID the daemon that saved it reported. The layers'
// contents are not unpacked or hashed: the config's rootfs.diff_ids are
// taken at their word, which is what inspect then reports.

// LoadRequest is one POST /images/load the fake received.
type LoadRequest struct {
	Query string
	// Platform is the platform parameter, exactly as it was sent ("" when
	// the request carried none).
	Platform string
	// Bytes is how much of the archive arrived before the request ended.
	Bytes int64
	// ImageID is the image (hex) the archive was registered as; "" when
	// nothing was loaded.
	ImageID string
	// Refs are the references the image was given, in their familiar form.
	Refs []string
	// MovedFrom maps a reference to the image (hex) that held it before.
	MovedFrom map[string]string
	// Err is why nothing was loaded, when nothing was.
	Err  string
	Time time.Time
}

// maxLoadArchive bounds the archive the fake reads into memory.
const maxLoadArchive = 256 << 20

// LoadRequests returns every load request the fake received, in order.
func (s *Server) LoadRequests() []LoadRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LoadRequest(nil), s.loadReqs...)
}

// FailLoad makes the next load read its archive and then report message as
// an error inside its answer — a 200 whose stream ends in an errorDetail,
// which is how a daemon reports a load that failed after it began. Nothing
// is registered.
func (s *Server) FailLoad(message string) {
	s.mu.Lock()
	s.loadFail = message
	s.mu.Unlock()
}

// loadedImage is what an archive says of the one image it carries.
type loadedImage struct {
	id       string // hex: the config's digest
	refs     []string
	config   ImageConfig
	labels   map[string]string
	layers   []Layer
	created  time.Time
	size     int64
	platform string
}

// parseLoadArchive reads an image archive in either layout.
func parseLoadArchive(data []byte) (loadedImage, error) {
	files := map[string][]byte{}
	links := map[string]string{}
	sizes := map[string]int64{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return loadedImage{}, fmt.Errorf("invalid tar header: %v", err)
		}
		name := path.Clean(h.Name)
		switch h.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			links[name] = h.Linkname
		case tar.TypeReg:
			sizes[name] = h.Size
			b, err := io.ReadAll(tr)
			if err != nil {
				return loadedImage{}, fmt.Errorf("unexpected end of archive in %s: %v", name, err)
			}
			files[name] = b
		}
	}
	raw, ok := files["manifest.json"]
	if !ok {
		return loadedImage{}, errors.New("invalid archive: does not contain a manifest.json")
	}
	var manifest []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return loadedImage{}, fmt.Errorf("invalid archive: manifest.json: %v", err)
	}
	if len(manifest) != 1 {
		return loadedImage{}, fmt.Errorf("fake engine: load exactly one image per archive (got %d)", len(manifest))
	}
	man := manifest[0]
	cfgRaw, ok := files[path.Clean(man.Config)]
	if !ok || man.Config == "" {
		return loadedImage{}, fmt.Errorf("invalid archive: the config %q named by manifest.json is not in it", man.Config)
	}
	var cfg struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
		Created      string `json:"created"`
		Config       struct {
			User         string
			Env          []string
			Cmd          []string
			Entrypoint   []string
			WorkingDir   string
			Labels       map[string]string
			ExposedPorts map[string]any
			Healthcheck  *struct{ Test []string }
		} `json:"config"`
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
		History []struct {
			CreatedBy  string `json:"created_by"`
			EmptyLayer bool   `json:"empty_layer"`
		} `json:"history"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return loadedImage{}, fmt.Errorf("invalid archive: image config: %v", err)
	}
	if len(cfg.RootFS.DiffIDs) != len(man.Layers) {
		return loadedImage{}, fmt.Errorf("invalid archive: manifest.json lists %d layers, the config %d", len(man.Layers), len(cfg.RootFS.DiffIDs))
	}
	sum := sha256.Sum256(cfgRaw)
	img := loadedImage{id: hex.EncodeToString(sum[:]), labels: cfg.Config.Labels, platform: cfg.OS + "/" + cfg.Architecture,
		layers: []Layer{}} // not nil: an image of no layers has none
	if cfg.Variant != "" {
		img.platform += "/" + cfg.Variant
	}
	img.config = ImageConfig{
		Architecture: cfg.Architecture, Variant: cfg.Variant,
		Cmd: cfg.Config.Cmd, Entrypoint: cfg.Config.Entrypoint, User: cfg.Config.User,
		WorkingDir: cfg.Config.WorkingDir, Env: cfg.Config.Env,
	}
	if cfg.Config.Healthcheck != nil {
		img.config.Healthcheck = cfg.Config.Healthcheck.Test
	}
	for p := range cfg.Config.ExposedPorts {
		img.config.ExposedPorts = append(img.config.ExposedPorts, p)
	}
	sort.Strings(img.config.ExposedPorts)
	if t, err := time.Parse(time.RFC3339Nano, cfg.Created); err == nil {
		img.created = t
	}
	var createdBy []string
	for _, h := range cfg.History {
		if !h.EmptyLayer {
			createdBy = append(createdBy, h.CreatedBy)
		}
	}
	for i, diffID := range cfg.RootFS.DiffIDs {
		p := path.Clean(man.Layers[i])
		if l, ok := links[p]; ok {
			// An identical layer is stored once and linked.
			p = path.Join(path.Dir(p), l)
		}
		size, ok := sizes[p]
		if !ok {
			return loadedImage{}, fmt.Errorf("invalid archive: the layer %q named by manifest.json is not in it", man.Layers[i])
		}
		l := Layer{Name: diffID, DiffID: diffID, Size: size}
		if i < len(createdBy) {
			l.CreatedBy = createdBy[i]
		}
		img.layers = append(img.layers, l)
		img.size += size
	}
	for _, t := range man.RepoTags {
		n, ok := normalizeRef(t)
		if !ok {
			return loadedImage{}, fmt.Errorf("invalid archive: %q is not a valid reference", t)
		}
		img.refs = append(img.refs, n)
	}
	return img, nil
}

// handleImageLoad serves POST /images/load. The archive is read first, in
// pieces with LoadChunkDelay between them, so that a load can be watched
// and cancelled; nothing is registered unless it arrived whole and parses.
func (s *Server) handleImageLoad(w http.ResponseWriter, r *http.Request) {
	req := LoadRequest{Query: r.URL.RawQuery, Platform: r.URL.Query().Get("platform"), Time: time.Now()}
	s.mu.Lock()
	delay, stepDelay := s.LoadChunkDelay, s.LoadStepDelay
	s.mu.Unlock()
	record := func() {
		s.mu.Lock()
		s.loadReqs = append(s.loadReqs, req)
		s.mu.Unlock()
	}
	refuse := func(status int, format string, args ...any) {
		req.Err = fmt.Sprintf(format, args...)
		record()
		httpError(w, status, "%s", req.Err)
	}

	var data bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		data.Write(buf[:n])
		req.Bytes += int64(n)
		if data.Len() > maxLoadArchive {
			refuse(http.StatusRequestEntityTooLarge, "fake engine: the archive is larger than %d bytes", maxLoadArchive)
			return
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			// The client went away mid-archive: nothing is loaded.
			req.Err = "the archive did not arrive whole: " + err.Error()
			record()
			return
		}
		if delay > 0 && n > 0 {
			select {
			case <-r.Context().Done():
				req.Err = "the archive did not arrive whole: the request was closed"
				record()
				return
			case <-s.closed:
				return
			case <-time.After(delay):
			}
		}
	}
	img, err := parseLoadArchive(data.Bytes())
	if err != nil {
		refuse(http.StatusInternalServerError, "%v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(m any) bool {
		if enc.Encode(m) != nil {
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		if stepDelay <= 0 {
			return r.Context().Err() == nil
		}
		select {
		case <-r.Context().Done():
			return false
		case <-s.closed:
			return false
		case <-time.After(stepDelay):
			return true
		}
	}
	for _, l := range img.layers {
		id := short(l.DiffID)
		if !send(jsonstream.Message{Status: "Loading layer", ID: id, Progress: &jsonstream.Progress{Current: l.Size, Total: l.Size}}) {
			req.Err = "the request was closed before the load ended"
			record()
			return
		}
	}

	s.mu.Lock()
	if msg := s.loadFail; msg != "" {
		s.loadFail = ""
		req.Err = msg
		s.loadReqs = append(s.loadReqs, req)
		s.mu.Unlock()
		_ = enc.Encode(map[string]any{"errorDetail": map[string]string{"message": msg}, "error": msg})
		return
	}
	target := s.images[img.id]
	if target == nil {
		created := img.created
		if created.IsZero() {
			created = time.Now()
		}
		cfg := img.config
		target = &Image{ID: img.id, Size: img.size, Created: created, Labels: img.labels, layers: img.layers, config: &cfg}
		s.images[img.id] = target
	}
	target.archive = data.Bytes()
	req.ImageID = img.id
	req.MovedFrom = map[string]string{}
	for _, ref := range img.refs {
		for _, other := range s.images {
			if other == target {
				continue
			}
			for _, t := range other.RepoTags {
				if t == ref {
					req.MovedFrom[ref] = other.ID
				}
			}
			other.RepoTags = removeString(other.RepoTags, ref)
		}
		has := false
		for _, t := range target.RepoTags {
			has = has || t == ref
		}
		if !has {
			target.RepoTags = append(target.RepoTags, ref)
		}
		req.Refs = append(req.Refs, ref)
		s.emit("image", "tag", "sha256:"+img.id, map[string]string{"name": ref})
	}
	s.emit("image", "load", "sha256:"+img.id, map[string]string{"name": "sha256:" + img.id})
	s.loadReqs = append(s.loadReqs, req)
	s.mu.Unlock()

	if len(img.refs) == 0 {
		send(jsonstream.Message{Stream: "Loaded image ID: sha256:" + img.id + "\n"})
		return
	}
	for _, ref := range img.refs {
		send(jsonstream.Message{Stream: "Loaded image: " + ref + "\n"})
	}
}

// LoadedPlatform reports the platform the archive of a loaded image states,
// for tests that check which variant was sent.
func LoadedPlatform(archive []byte) string {
	img, err := parseLoadArchive(archive)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(img.platform)
}
