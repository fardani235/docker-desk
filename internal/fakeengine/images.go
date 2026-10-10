package fakeengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
)

// Image is an image in the fake engine.
type Image struct {
	ID       string // hex, without the sha256: prefix
	RepoTags []string
	Size     int64
	Created  time.Time
	Labels   map[string]string
}

// normalizeRef returns the familiar form of a reference with ":latest"
// filled in, e.g. "docker.io/library/alpine" → "alpine:latest".
func normalizeRef(ref string) (string, bool) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", false
	}
	return reference.FamiliarString(reference.TagNameOnly(named)), true
}

// AddImage seeds an image with the given references and size. With no
// references the image is dangling. It returns the image ID (hex).
func (s *Server) AddImage(size int64, age time.Duration, refs ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addImageLocked(size, age, refs...).ID
}

func (s *Server) addImageLocked(size int64, age time.Duration, refs ...string) *Image {
	key := strings.Join(refs, ",")
	img := &Image{
		ID:      s.newIDLocked("image", key),
		Size:    size,
		Created: time.Now().Add(-age),
	}
	for _, r := range refs {
		if n, ok := normalizeRef(r); ok {
			img.RepoTags = append(img.RepoTags, n)
		}
	}
	s.images[img.ID] = img
	return img
}

func (s *Server) ensureImageLocked(ref string) *Image {
	if ref == "" {
		ref = "scratch"
	}
	if img := s.findImageLocked(ref); img != nil {
		return img
	}
	size := int64(20_000_000 + len(ref)*3_141_592)
	return s.addImageLocked(size, 24*time.Hour, ref)
}

// findImageLocked resolves a reference, a full ID or an ID prefix.
func (s *Server) findImageLocked(ref string) *Image {
	id := strings.TrimPrefix(ref, "sha256:")
	if img, ok := s.images[id]; ok {
		return img
	}
	if n, ok := normalizeRef(ref); ok {
		for _, img := range s.images {
			for _, t := range img.RepoTags {
				if t == n {
					return img
				}
			}
		}
	}
	if isHex(id) && len(id) >= 4 {
		var match *Image
		for iid, img := range s.images {
			if strings.HasPrefix(iid, id) {
				if match != nil {
					return nil
				}
				match = img
			}
		}
		return match
	}
	return nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (s *Server) imageUsersLocked(id string) []*Container {
	var out []*Container
	for _, c := range s.sortedContainersLocked() {
		if c.ImageID == id {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) sortedImagesLocked() []*Image {
	out := make([]*Image, 0, len(s.images))
	for _, i := range s.images {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].Created.Equal(out[b].Created) {
			return out[a].Created.After(out[b].Created)
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func (i *Image) digests() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range i.RepoTags {
		repo := t
		if at := strings.Index(repo, "@"); at >= 0 {
			repo = repo[:at]
		} else if c := strings.LastIndex(repo, ":"); c > strings.LastIndex(repo, "/") {
			repo = repo[:c]
		}
		if !seen[repo] {
			seen[repo] = true
			out = append(out, repo+"@sha256:"+i.ID)
		}
	}
	return out
}

func (s *Server) handleImageList(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)
	s.mu.Lock()
	out := []image.Summary{}
	for _, img := range s.sortedImagesLocked() {
		if vs, ok := filters["dangling"]; ok && len(vs) > 0 {
			want := vs[0] == "true" || vs[0] == "1"
			if (len(img.RepoTags) == 0) != want {
				continue
			}
		}
		tags := append([]string{}, img.RepoTags...)
		out = append(out, image.Summary{
			ID:          "sha256:" + img.ID,
			RepoTags:    tags,
			RepoDigests: img.digests(),
			Created:     img.Created.Unix(),
			Size:        img.Size,
			SharedSize:  -1,
			Containers:  int64(len(s.imageUsersLocked(img.ID))),
			Labels:      img.Labels,
		})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleImageInspect(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	img := s.findImageLocked(ref)
	if img == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such image: %s", ref)
		return
	}
	resp := map[string]any{
		"Id":           "sha256:" + img.ID,
		"RepoTags":     append([]string{}, img.RepoTags...),
		"RepoDigests":  img.digests(),
		"Created":      img.Created.UTC().Format(time.RFC3339Nano),
		"Architecture": "amd64",
		"Os":           "linux",
		"Size":         img.Size,
		"Config": map[string]any{
			"Cmd":        []string{"/bin/app"},
			"Env":        []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			"WorkingDir": "/",
			"Labels":     img.Labels,
		},
		"RootFS": map[string]any{
			"Type":   "layers",
			"Layers": []string{"sha256:" + stableID("layer", img.ID+"0"), "sha256:" + stableID("layer", img.ID+"1")},
		},
		"Metadata": map[string]any{"LastTagTime": "0001-01-01T00:00:00Z"},
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleImageRemove(w http.ResponseWriter, r *http.Request, ref string) {
	force := boolParam(r, "force")
	s.mu.Lock()
	defer s.mu.Unlock()
	img := s.findImageLocked(ref)
	if img == nil {
		httpError(w, http.StatusNotFound, "No such image: %s", ref)
		return
	}
	users := s.imageUsersLocked(img.ID)
	byID := strings.HasPrefix(img.ID, strings.TrimPrefix(ref, "sha256:"))
	out := []image.DeleteResponse{}

	if !byID {
		norm, _ := normalizeRef(ref)
		if len(img.RepoTags) > 1 {
			img.RepoTags = removeString(img.RepoTags, norm)
			s.emit("image", "untag", "sha256:"+img.ID, map[string]string{"name": norm})
			writeJSON(w, http.StatusOK, append(out, image.DeleteResponse{Untagged: norm}))
			return
		}
		if len(users) > 0 && !force {
			httpError(w, http.StatusConflict,
				"conflict: unable to remove repository reference %q (must force) - container %s is using its referenced image %s",
				ref, short(users[0].ID), short(img.ID))
			return
		}
	} else {
		if len(users) > 0 {
			for _, u := range users {
				if u.active() {
					httpError(w, http.StatusConflict,
						"conflict: unable to delete %s (cannot be forced) - image is being used by running container %s",
						short(img.ID), short(u.ID))
					return
				}
			}
			if !force {
				httpError(w, http.StatusConflict,
					"conflict: unable to delete %s (must be forced) - image is being used by stopped container %s",
					short(img.ID), short(users[0].ID))
				return
			}
		}
		if len(img.RepoTags) > 1 && !force {
			httpError(w, http.StatusConflict,
				"conflict: unable to delete %s (must be forced) - image is referenced in multiple repositories", short(img.ID))
			return
		}
	}
	for _, t := range img.RepoTags {
		out = append(out, image.DeleteResponse{Untagged: t})
		s.emit("image", "untag", "sha256:"+img.ID, map[string]string{"name": t})
	}
	out = append(out, image.DeleteResponse{Deleted: "sha256:" + img.ID})
	delete(s.images, img.ID)
	s.emit("image", "delete", "sha256:"+img.ID, map[string]string{"name": "sha256:" + img.ID})
	writeJSON(w, http.StatusOK, out)
}

func removeString(in []string, v string) []string {
	out := in[:0:0]
	for _, x := range in {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// pullLayers is the shape of every image the fake registry serves.
var pullLayers = []int64{3_400_000, 28_000_000, 9_600_000}

func (s *Server) handleImagePull(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, tag := q.Get("fromImage"), q.Get("tag")
	if from == "" {
		httpError(w, http.StatusBadRequest, "fromImage is required")
		return
	}
	if tag == "" {
		// The real daemon pulls every tag here. The desk must never ask for
		// that, so the fake refuses loudly instead of pretending.
		httpError(w, http.StatusBadRequest, "fake engine: refusing to pull all tags of %s (no tag given)", from)
		return
	}
	full := from + ":" + tag
	if strings.HasPrefix(tag, "sha256:") {
		full = from + "@" + tag
	}
	ref, ok := normalizeRef(full)
	if !ok {
		httpError(w, http.StatusBadRequest, "invalid reference format")
		return
	}
	repo := ref
	if c := strings.LastIndex(repo, ":"); c > strings.LastIndex(repo, "/") {
		repo = repo[:c]
	}
	s.mu.Lock()
	refused := s.Unpullable[repo] || strings.Contains(repo, "nosuch")
	delay := s.PullStepDelay
	existing := s.findImageLocked(ref)
	s.mu.Unlock()
	if refused {
		httpError(w, http.StatusNotFound,
			"pull access denied for %s, repository does not exist or may require 'docker login': denied: requested access to the resource is denied", repo)
		return
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

	if !send(jsonstream.Message{Status: "Pulling from " + repo, ID: tag}) {
		return
	}
	if existing != nil {
		send(jsonstream.Message{Status: "Digest: sha256:" + existing.ID})
		send(jsonstream.Message{Status: "Status: Image is up to date for " + ref})
		return
	}
	ids := make([]string, len(pullLayers))
	for i := range pullLayers {
		ids[i] = short(stableID("pull-layer", fmt.Sprintf("%s-%d", ref, i)))
		if !send(jsonstream.Message{Status: "Pulling fs layer", ID: ids[i]}) {
			return
		}
	}
	const steps = 4
	for step := 1; step <= steps; step++ {
		for i, total := range pullLayers {
			cur := total * int64(step) / steps
			if !send(jsonstream.Message{Status: "Downloading", ID: ids[i], Progress: &jsonstream.Progress{Current: cur, Total: total}}) {
				return
			}
		}
	}
	for i, total := range pullLayers {
		if !send(jsonstream.Message{Status: "Download complete", ID: ids[i]}) {
			return
		}
		if !send(jsonstream.Message{Status: "Extracting", ID: ids[i], Progress: &jsonstream.Progress{Current: total, Total: total}}) {
			return
		}
		if !send(jsonstream.Message{Status: "Pull complete", ID: ids[i]}) {
			return
		}
	}

	var size int64
	for _, l := range pullLayers {
		size += l
	}
	s.mu.Lock()
	img := s.findImageLocked(ref)
	if img == nil {
		img = s.addImageLocked(size, 0, ref)
	}
	id := img.ID
	s.emit("image", "pull", ref, map[string]string{"name": repo})
	s.mu.Unlock()

	send(jsonstream.Message{Status: "Digest: sha256:" + id})
	send(jsonstream.Message{Status: "Status: Downloaded newer image for " + ref})
}

// ---- test helpers ----------------------------------------------------------------

// ImageRefs returns every repository tag known to the fake, sorted.
func (s *Server) ImageRefs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, i := range s.images {
		out = append(out, i.RepoTags...)
	}
	sort.Strings(out)
	return out
}

// ImageIDs returns the IDs (hex) of every image, sorted.
func (s *Server) ImageIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id := range s.images {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// HasImage reports whether a reference or ID resolves to an image.
func (s *Server) HasImage(ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findImageLocked(ref) != nil
}

// MoveTag makes a reference point at a different image, as rebuilding or
// re-pulling a tag does. It returns the ID of the image now carrying it.
func (s *Server) MoveTag(ref string, size int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	norm, ok := normalizeRef(ref)
	if !ok {
		return ""
	}
	for _, img := range s.images {
		img.RepoTags = removeString(img.RepoTags, norm)
	}
	img := s.addImageLocked(size, 0)
	img.RepoTags = []string{norm}
	s.emit("image", "tag", "sha256:"+img.ID, map[string]string{"name": norm})
	return img.ID
}

// RecreateVolume removes a volume and creates a new, empty one with the same
// name and a different creation time.
func (s *Server) RecreateVolume(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.volumes[name]
	created := time.Now().Add(time.Hour)
	if old != nil {
		created = old.Created.Add(time.Hour)
	}
	s.volumes[name] = &Volume{Name: name, Created: created, Labels: map[string]string{}}
}
