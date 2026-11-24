package fakeengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/jsonstream"
)

// PushRequest is one POST /images/{name}/push the fake received.
type PushRequest struct {
	// Name is the repository in the path, Tag the tag query parameter.
	Name, Tag string
	// Ref is name:tag in its familiar form.
	Ref string
	// Auth is the X-Registry-Auth header exactly as received ("" for none).
	// It is test evidence that the credential reached the daemon.
	Auth string
	Time time.Time
}

type pushState struct {
	mu       sync.Mutex
	requests []PushRequest
	// blobs are the layers the fake registry already holds, by repository:
	// a second push of the same image reports "Layer already exists".
	blobs map[string]map[string]bool
	// manifests are what each pushed tag now names in the fake registry.
	manifests map[string]string
	// refused, by repository, is the registry's refusal message.
	refused map[string]string
	// needsAuth lists repositories whose registry refuses a push that
	// carries no credential.
	needsAuth map[string]bool
}

func (s *Server) pushes() *pushState {
	s.pushOnce.Do(func() {
		s.push = &pushState{
			blobs: map[string]map[string]bool{}, manifests: map[string]string{},
			refused: map[string]string{}, needsAuth: map[string]bool{},
		}
	})
	return s.push
}

// PushRequests returns every push request received, in order.
func (s *Server) PushRequests() []PushRequest {
	p := s.pushes()
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PushRequest(nil), p.requests...)
}

// PushedManifest is the digest the fake registry holds for ref after a
// completed push, or "".
func (s *Server) PushedManifest(ref string) string {
	n, ok := normalizeRef(ref)
	if !ok {
		return ""
	}
	p := s.pushes()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifests[n]
}

// RefusePush makes the fake registry refuse every push to repo (a familiar
// repository name, e.g. host:5000/acme/api) with the registry's message, as
// a denied namespace or an exhausted quota would.
func (s *Server) RefusePush(repo, message string) {
	p := s.pushes()
	p.mu.Lock()
	p.refused[familiarRepo(repo)] = message
	p.mu.Unlock()
}

// RequirePushAuth makes the fake registry refuse a push to repo that
// carries no X-Registry-Auth.
func (s *Server) RequirePushAuth(repo string) {
	p := s.pushes()
	p.mu.Lock()
	p.needsAuth[familiarRepo(repo)] = true
	p.mu.Unlock()
}

func familiarRepo(repo string) string {
	named, err := reference.ParseNormalizedNamed(repo)
	if err != nil {
		return repo
	}
	return reference.FamiliarName(named)
}

// pushManifestDigest is what the fake registry names a pushed image by:
// stable for one image under one reference.
func pushManifestDigest(ref, imageID string) string {
	return "sha256:" + stableID("push-manifest", ref+"|"+imageID)
}

// handleImagePush serves POST /images/{name}/push?tag=…. It reads the image
// store and changes nothing in it: a push neither creates, moves nor removes
// a local image or tag.
func (s *Server) handleImagePush(w http.ResponseWriter, r *http.Request, name string) {
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		// The real daemon pushes every tag of the repository here. The desk
		// must never ask for that, so the fake refuses loudly.
		httpError(w, http.StatusBadRequest, "fake engine: refusing to push all tags of %s (no tag given)", name)
		return
	}
	if strings.HasPrefix(tag, "sha256:") {
		httpError(w, http.StatusBadRequest, "invalid tag format")
		return
	}
	ref, ok := normalizeRef(name + ":" + tag)
	if !ok {
		httpError(w, http.StatusBadRequest, "invalid reference format")
		return
	}
	repo := familiarRepo(name)
	auth := r.Header.Get("X-Registry-Auth")
	p := s.pushes()
	p.mu.Lock()
	p.requests = append(p.requests, PushRequest{Name: name, Tag: tag, Ref: ref, Auth: auth, Time: time.Now()})
	refusal, refused := p.refused[repo]
	needsAuth := p.needsAuth[repo]
	p.mu.Unlock()

	s.mu.Lock()
	var img *Image
	for _, i := range s.images {
		for _, t := range i.RepoTags {
			if t == ref {
				img = i
			}
		}
	}
	var layers []Layer
	var imageID string
	if img != nil {
		layers = append(layers, img.layersLocked()...)
		imageID = img.ID
	}
	delay := s.PushStepDelay
	s.mu.Unlock()
	if img == nil {
		httpError(w, http.StatusNotFound, "No such image: %s", ref)
		return
	}
	if strings.Contains(repo, "nosuch") && !refused {
		refused, refusal = true, "denied: requested access to the resource is denied"
	}
	if needsAuth && auth == "" && !refused {
		refused, refusal = true, "unauthorized: authentication required"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(m jsonstream.Message) bool {
		if enc.Encode(m) != nil {
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return false
		case <-s.closed:
			return false
		case <-time.After(delay):
			return true
		}
	}

	named, _ := reference.ParseNormalizedNamed(name)
	if !send(jsonstream.Message{Status: "The push refers to repository [" + named.Name() + "]"}) {
		return
	}
	ids := make([]string, len(layers))
	for i := len(layers) - 1; i >= 0; i-- { // the daemon lists the top layer first
		ids[i] = short(strings.TrimPrefix(layers[i].diffID(), "sha256:"))
		if !send(jsonstream.Message{Status: "Preparing", ID: ids[i], Progress: &jsonstream.Progress{}}) {
			return
		}
	}
	if refused {
		send(jsonstream.Message{Error: &jsonstream.Error{Message: refusal}})
		return
	}
	p.mu.Lock()
	held := p.blobs[repo]
	if held == nil {
		held = map[string]bool{}
		p.blobs[repo] = held
	}
	already := make([]bool, len(layers))
	for i := range layers {
		already[i] = held[ids[i]]
	}
	p.mu.Unlock()

	for i := range layers {
		if already[i] {
			if !send(jsonstream.Message{Status: "Layer already exists", ID: ids[i], Progress: &jsonstream.Progress{}}) {
				return
			}
			continue
		}
		if !send(jsonstream.Message{Status: "Waiting", ID: ids[i], Progress: &jsonstream.Progress{}}) {
			return
		}
	}
	const steps = 4
	for step := 1; step <= steps; step++ {
		for i, l := range layers {
			if already[i] {
				continue
			}
			total := max(l.Size, 1)
			if !send(jsonstream.Message{Status: "Pushing", ID: ids[i], Progress: &jsonstream.Progress{Current: total * int64(step) / steps, Total: total}}) {
				return
			}
		}
	}
	for i := range layers {
		if already[i] {
			continue
		}
		if !send(jsonstream.Message{Status: "Pushed", ID: ids[i], Progress: &jsonstream.Progress{}}) {
			return
		}
		p.mu.Lock()
		held[ids[i]] = true // an uploaded blob stays, whatever happens next
		p.mu.Unlock()
	}

	digest := pushManifestDigest(ref, imageID)
	size := int64(400 + 160*len(layers))
	p.mu.Lock()
	p.manifests[ref] = digest
	p.mu.Unlock()
	s.emit("image", "push", ref, map[string]string{"name": ref})
	if !send(jsonstream.Message{Status: fmt.Sprintf("%s: digest: %s size: %d", tag, digest, size)}) {
		return
	}
	aux, _ := json.Marshal(map[string]any{"Tag": tag, "Digest": digest, "Size": size})
	raw := json.RawMessage(aux)
	send(jsonstream.Message{Progress: &jsonstream.Progress{}, Aux: &raw})
}
