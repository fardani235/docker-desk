package fakeengine

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// ProcessTable is a fixed top listing: titles and rows exactly as the
// daemon would return them. Rows may be shorter or longer than Titles.
type ProcessTable struct {
	Titles []string
	Rows   [][]string
}

// SetProcesses fixes the listing GET /containers/{id}/top returns for a
// container. A nil table restores the derived default listing. An empty Rows
// slice yields a valid answer with no processes.
func (s *Server) SetProcesses(ref string, t *ProcessTable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.findContainerLocked(ref); c != nil {
		c.Processes = t
	}
}

// handleTop answers the container-top endpoint the way dockerd does on
// Linux: 404 for an unknown container, 409 for one that is not running and
// the default `ps -ef` columns otherwise.
func (s *Server) handleTop(w http.ResponseWriter, r *http.Request, ref string) {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	if c.State != "running" {
		id := c.ID
		s.mu.Unlock()
		httpError(w, http.StatusConflict, "container %s is not running", id)
		return
	}
	var out container.TopResponse
	if t := c.Processes; t != nil {
		out.Titles = append([]string{}, t.Titles...)
		out.Processes = make([][]string, 0, len(t.Rows))
		for _, row := range t.Rows {
			out.Processes = append(out.Processes, append([]string{}, row...))
		}
	} else {
		out = defaultTop(c)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// defaultTop is a deterministic `ps -ef` listing: the container's command as
// PID 1 and, for a shell-capable container, one child shell.
func defaultTop(c *Container) container.TopResponse {
	stime := c.StartedAt.Format("15:04")
	base := 1000 + int(c.Created.Unix()%1000)
	rows := [][]string{
		{"root", strconv.Itoa(base), strconv.Itoa(base - 1), "0", stime, "?", "00:00:01", strings.Join(c.Cmd, " ")},
	}
	if !c.NoShell {
		rows = append(rows, []string{"root", strconv.Itoa(base + 1), strconv.Itoa(base), "0", stime, "?", "00:00:00", "/bin/sh -c sleep infinity"})
	}
	return container.TopResponse{
		Titles:    []string{"UID", "PID", "PPID", "C", "STIME", "TTY", "TIME", "CMD"},
		Processes: rows,
	}
}
