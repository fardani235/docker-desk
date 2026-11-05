package fakeengine

import (
	"io"
	"net/http"
	"strings"
	"sync"
)

// Upstream is what the fake daemon's registry says a tag names now, for
// GET /distribution/{name}/json. The fake asks no real registry.
type Upstream struct {
	// Digest is what the tag names; "" with Gone unset means the image's
	// own ID (the tag has not moved).
	Digest    string
	MediaType string
	Platforms []UpstreamPlatform
	// Gone: the registry has no such manifest.
	Gone bool
	// Denied: the registry refused the credential.
	Denied bool
}

// UpstreamPlatform is one platform an index publishes.
type UpstreamPlatform struct{ OS, Architecture, Variant string }

type distState struct {
	mu          sync.Mutex
	upstream    map[string]Upstream // by normalised reference
	unsupported bool
	lastAuth    string
}

func (s *Server) dist() *distState {
	s.distOnce.Do(func() { s.distribution = &distState{upstream: map[string]Upstream{}} })
	return s.distribution
}

// SetUpstream says what ref's tag names in the fake daemon's registry.
func (s *Server) SetUpstream(ref string, u Upstream) {
	n, ok := normalizeRef(ref)
	if !ok {
		return
	}
	d := s.dist()
	d.mu.Lock()
	d.upstream[n] = u
	d.mu.Unlock()
}

// SetDistributionUnsupported makes the fake answer the endpoint with the
// daemon's own "page not found", as a daemon that does not serve it does.
func (s *Server) SetDistributionUnsupported(v bool) {
	d := s.dist()
	d.mu.Lock()
	d.unsupported = v
	d.mu.Unlock()
}

// LastRegistryAuth is the X-Registry-Auth header of the last distribution
// request, exactly as received.
func (s *Server) LastRegistryAuth() string {
	d := s.dist()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastAuth
}

func (s *Server) handleDistribution(w http.ResponseWriter, r *http.Request, name string) {
	d := s.dist()
	d.mu.Lock()
	d.lastAuth = r.Header.Get("X-Registry-Auth")
	unsupported := d.unsupported
	n, _ := normalizeRef(name)
	u, ok := d.upstream[n]
	d.mu.Unlock()
	if unsupported {
		httpError(w, http.StatusNotFound, "page not found")
		return
	}
	if !ok || u.Gone {
		httpError(w, http.StatusNotFound, "manifest unknown: manifest unknown")
		return
	}
	if u.Denied {
		httpError(w, http.StatusUnauthorized, "unauthorized: authentication required")
		return
	}
	dg := u.Digest
	if dg == "" {
		s.mu.Lock()
		if img := s.findImageLocked(name); img != nil {
			dg = "sha256:" + img.ID
		}
		s.mu.Unlock()
	}
	mt := u.MediaType
	if mt == "" {
		mt = "application/vnd.oci.image.index.v1+json"
	}
	plats := []map[string]string{}
	for _, p := range u.Platforms {
		m := map[string]string{"os": p.OS, "architecture": p.Architecture}
		if p.Variant != "" {
			m["variant"] = p.Variant
		}
		plats = append(plats, m)
	}
	if len(plats) == 0 {
		plats = append(plats, map[string]string{"os": "linux", "architecture": "amd64"})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Descriptor": map[string]any{"mediaType": mt, "digest": dg, "size": 1024},
		"Platforms":  plats,
	})
}

// distributionName is the reference in /distribution/{name}/json, which may
// contain slashes.
func distributionName(seg []string) (string, bool) {
	if len(seg) < 3 || seg[0] != "distribution" || seg[len(seg)-1] != "json" {
		return "", false
	}
	return strings.Join(seg[1:len(seg)-1], "/"), true
}

// handleAuth answers POST /auth, which `docker login` sends to the daemon
// before it stores the credential. The fake checks nothing and keeps
// nothing: it exists so the real CLI can write a configuration file the
// desk is then tested against.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
	writeJSON(w, http.StatusOK, map[string]string{"Status": "Login Succeeded"})
}
