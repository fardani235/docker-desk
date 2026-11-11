package fakeengine

import (
	"net/http"

	"github.com/moby/moby/api/types/container"
)

// Change is one entry of a container's change list, numbered as the Engine
// API numbers it: 0 changed, 1 added, 2 deleted.
type Change struct {
	Path string
	Kind int
}

// Changed, Added and Deleted build a Change.
func Changed(p string) Change { return Change{Path: p, Kind: 0} }
func Added(p string) Change   { return Change{Path: p, Kind: 1} }
func Deleted(p string) Change { return Change{Path: p, Kind: 2} }

// SetChanges fixes what GET /containers/{id}/changes returns for a
// container: what `docker diff` would print, in the order given. A
// container nobody set has written nothing since its image.
func (s *Server) SetChanges(ref string, changes ...Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.findContainerLocked(ref); c != nil {
		c.Changes = append([]Change(nil), changes...)
	}
}

// handleChanges answers the container-changes endpoint as dockerd does: 404
// for an unknown container, and the list — empty for a container that has
// written nothing — for any other, whatever its state.
func (s *Server) handleChanges(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	out := make([]container.FilesystemChange, 0, len(c.Changes))
	for _, ch := range c.Changes {
		out = append(out, container.FilesystemChange{Path: ch.Path, Kind: container.ChangeType(ch.Kind)})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
