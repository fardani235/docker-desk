package fakeengine

import (
	"net/http"
	"time"

	"github.com/distribution/reference"
)

// TagRequest is one POST /images/{name}/tag the fake received and served.
type TagRequest struct {
	// Source is the image named in the path, exactly as it was sent.
	Source string
	// Repo and Tag are the query parameters, exactly as they were sent.
	Repo, Tag string
	// Ref is the target in its familiar form, ":latest" filled in.
	Ref string
	// ImageID is the image (hex) the source resolved to, which now carries
	// Ref; MovedFrom is the image that carried it before, or "".
	ImageID, MovedFrom string
	Time               time.Time
}

// TagRequests returns every tag request the fake served, in order.
func (s *Server) TagRequests() []TagRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TagRequest(nil), s.tagReqs...)
}

// tagTarget reads the repo and tag parameters the way the daemon does: repo
// is a name (a digest is refused), tag is optional and means ":latest" when
// absent.
func tagTarget(repo, tag string) (string, int, string) {
	if repo == "" {
		return "", http.StatusBadRequest, "repo is required"
	}
	named, err := reference.ParseNormalizedNamed(repo)
	if err != nil {
		return "", http.StatusBadRequest, "invalid reference format"
	}
	if _, ok := named.(reference.Digested); ok {
		return "", http.StatusBadRequest, "refusing to create a tag with a digest reference"
	}
	if tag != "" {
		if named, err = reference.WithTag(reference.TrimNamed(named), tag); err != nil {
			return "", http.StatusBadRequest, "invalid tag format"
		}
	}
	return reference.FamiliarString(reference.TagNameOnly(named)), 0, ""
}

// handleImageTag serves POST /images/{name}/tag?repo=…&tag=…: the source
// image gains the target reference. As on the daemon, a target that names
// another image is moved from it without a refusal (that image keeps its
// data and loses the name), tagging an image with a reference it already
// has changes nothing, and no registry is involved.
func (s *Server) handleImageTag(w http.ResponseWriter, r *http.Request, name string) {
	q := r.URL.Query()
	target, status, msg := tagTarget(q.Get("repo"), q.Get("tag"))
	if status != 0 {
		httpError(w, status, "%s", msg)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	img := s.findImageLocked(name)
	if img == nil {
		httpError(w, http.StatusNotFound, "No such image: %s", name)
		return
	}
	req := TagRequest{Source: name, Repo: q.Get("repo"), Tag: q.Get("tag"), Ref: target, ImageID: img.ID, Time: time.Now()}
	for _, other := range s.images {
		if other == img {
			continue
		}
		for _, t := range other.RepoTags {
			if t == target {
				req.MovedFrom = other.ID
			}
		}
		other.RepoTags = removeString(other.RepoTags, target)
	}
	has := false
	for _, t := range img.RepoTags {
		has = has || t == target
	}
	if !has {
		img.RepoTags = append(img.RepoTags, target)
	}
	s.tagReqs = append(s.tagReqs, req)
	s.emit("image", "tag", "sha256:"+img.ID, map[string]string{"name": target})
	w.WriteHeader(http.StatusCreated)
}
