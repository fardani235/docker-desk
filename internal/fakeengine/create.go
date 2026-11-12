package fakeengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

var validContainerName = regexp.MustCompile(`^/?[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

var (
	nameAdjectives = []string{"eager", "quiet", "brave", "lucid", "nimble", "sober", "witty", "zealous"}
	nameSurnames   = []string{"turing", "hopper", "lovelace", "noether", "ritchie", "hamilton", "shannon", "liskov"}
)

// handleContainerCreate is POST /containers/create. Like the daemon it needs
// the image to be present (the CLI, not the daemon, pulls on a 404), refuses
// a name in use with 409, and leaves the new container in the created state
// for POST /containers/{id}/start.
func (s *Server) handleContainerCreate(w http.ResponseWriter, r *http.Request) {
	var req container.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.Config == nil || req.Config.Image == "" {
		httpError(w, http.StatusBadRequest, "config cannot be empty in order to create a container")
		return
	}
	host := req.HostConfig
	if host == nil {
		host = &container.HostConfig{}
	}
	name := strings.TrimPrefix(r.URL.Query().Get("name"), "/")
	s.mu.Lock()
	s.createReqs = append(s.createReqs, CreateRequest{Name: name, Body: req})
	s.mu.Unlock()
	if name != "" && !validContainerName.MatchString(name) {
		httpError(w, http.StatusBadRequest, "Invalid container name (%s), only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed", name)
		return
	}
	policy := string(host.RestartPolicy.Name)
	switch policy {
	case "":
		policy = "no"
	case "no", "always", "unless-stopped", "on-failure":
	default:
		httpError(w, http.StatusBadRequest, "invalid restart policy: unknown policy '%s'; use one of 'no', 'always', 'on-failure', or 'unless-stopped'", policy)
		return
	}
	if policy != "on-failure" && host.RestartPolicy.MaximumRetryCount != 0 {
		httpError(w, http.StatusBadRequest, "invalid restart policy: maximum retry count can only be used with 'on-failure'")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	img := s.findImageLocked(req.Config.Image)
	if img == nil {
		httpError(w, http.StatusNotFound, "No such image: %s", req.Config.Image)
		return
	}
	if name != "" {
		for _, c := range s.containers {
			if c.Name == name {
				httpError(w, http.StatusConflict, "Conflict. The container name \"/%s\" is already in use by container \"%s\". You have to remove (or rename) that container to be able to reuse that name.", name, c.ID)
				return
			}
		}
	} else {
		name = s.assignNameLocked()
	}

	c := &Container{
		ID:             s.newIDLocked("container", name),
		Name:           name,
		Image:          req.Config.Image,
		ImageID:        img.ID,
		Cmd:            append([]string(nil), req.Config.Cmd...),
		Env:            append([]string(nil), req.Config.Env...),
		Created:        time.Now(),
		State:          "created",
		Labels:         cloneMap(req.Config.Labels),
		Tty:            req.Config.Tty,
		Networks:       map[string]*Endpoint{},
		RestartPolicy:  policy,
		RestartRetries: host.RestartPolicy.MaximumRetryCount,
		logs:           newLogBuffer(),
	}
	if len(c.Cmd) == 0 {
		c.Cmd = []string{"/bin/app"}
	}
	if c.Labels == nil {
		c.Labels = map[string]string{}
	}

	// Ports: every binding, and exposed ports without one.
	bound := map[string]bool{}
	for port, bindings := range host.PortBindings {
		bound[port.String()] = true
		for _, b := range bindings {
			ps := PortSpec{Private: port.Num(), Proto: string(port.Proto())}
			if b.HostIP.IsValid() {
				ps.HostIP = b.HostIP.String()
			}
			if b.HostPort == "" {
				s.seq++
				ps.Public = uint16(32768 + s.seq%28000)
			} else {
				n, err := strconv.Atoi(b.HostPort)
				if err != nil || n < 1 || n > 65535 {
					httpError(w, http.StatusBadRequest, "invalid port specification: %q", b.HostPort)
					return
				}
				ps.Public = uint16(n)
			}
			c.Ports = append(c.Ports, ps)
		}
	}
	for port := range req.Config.ExposedPorts {
		if !bound[port.String()] {
			c.Ports = append(c.Ports, PortSpec{Private: port.Num(), Proto: string(port.Proto())})
		}
	}

	// Mounts: the typed form, and the CLI's -v strings. Every one is checked
	// before any volume is created, as the daemon refuses the whole request.
	for _, m := range host.Mounts {
		if !strings.HasPrefix(m.Target, "/") {
			httpError(w, http.StatusBadRequest, "invalid mount config: mount path must be absolute: %s", m.Target)
			return
		}
		if m.Type != mount.TypeVolume && m.Type != mount.TypeBind {
			httpError(w, http.StatusBadRequest, "invalid mount config: mount type %q is not supported by the fake daemon", m.Type)
			return
		}
	}
	for _, b := range host.Binds {
		if parts := strings.Split(b, ":"); len(parts) < 2 || !strings.HasPrefix(parts[1], "/") {
			httpError(w, http.StatusBadRequest, "invalid volume specification: '%s'", b)
			return
		}
	}
	for _, m := range host.Mounts {
		switch m.Type {
		case mount.TypeVolume:
			c.Mounts = append(c.Mounts, s.volumeMountLocked(m.Source, m.Target, !m.ReadOnly))
		case mount.TypeBind:
			c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
		}
	}
	for _, b := range host.Binds {
		parts := strings.Split(b, ":")
		rw := !(len(parts) > 2 && strings.Contains(parts[2], "ro"))
		if strings.HasPrefix(parts[0], "/") {
			c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: parts[0], Destination: parts[1], RW: rw})
		} else {
			c.Mounts = append(c.Mounts, s.volumeMountLocked(parts[0], parts[1], rw))
		}
	}

	if n := s.networkByNameLocked("bridge"); n != nil {
		s.attachLocked(c, n, false)
	}
	s.containers[c.ID] = c
	s.emit("container", "create", c.ID, map[string]string{"name": c.Name, "image": c.Image})
	writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

// volumeMountLocked mounts a named volume, creating it the way the daemon
// does when the name is new.
func (s *Server) volumeMountLocked(name, target string, rw bool) MountSpec {
	if _, ok := s.volumes[name]; !ok {
		s.ensureVolumeLocked(name, nil)
		s.emit("volume", "create", name, map[string]string{"driver": "local"})
	}
	return MountSpec{Type: "volume", Name: name, Source: "/var/lib/fake-docker/volumes/" + name + "/_data", Destination: target, RW: rw}
}

// assignNameLocked picks an unused adjective_surname, as the daemon does.
func (s *Server) assignNameLocked() string {
	taken := map[string]bool{}
	for _, c := range s.containers {
		taken[c.Name] = true
	}
	for i := 0; ; i++ {
		s.seq++
		name := nameAdjectives[s.seq%len(nameAdjectives)] + "_" + nameSurnames[(s.seq/len(nameAdjectives))%len(nameSurnames)]
		if i >= len(nameAdjectives)*len(nameSurnames) {
			name += fmt.Sprint(i)
		}
		if !taken[name] {
			return name
		}
	}
}

// Rename gives a container a new name, as `docker rename` would, emitting
// the event. It is how tests move a name between two steps of a plan.
func (s *Server) Rename(ref, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return false
	}
	old := c.Name
	c.Name = name
	s.emit("container", "rename", c.ID, map[string]string{"name": name, "oldName": "/" + old, "image": c.Image})
	return true
}

// CreateRequest is a POST /containers/create as the fake decoded it.
type CreateRequest struct {
	Name string // the name query parameter
	Body container.CreateRequest
}

// CreateRequests returns every create request received, in order.
func (s *Server) CreateRequests() []CreateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CreateRequest(nil), s.createReqs...)
}
