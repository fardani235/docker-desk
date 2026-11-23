package fakeengine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
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
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "reading the body: %v", err)
		return
	}
	var req container.CreateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
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
	s.createReqs = append(s.createReqs, CreateRequest{Name: name, Body: req, Raw: raw})
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

	// Networks: every endpoint named must exist, and an alias needs a
	// user-defined network, as the daemon insists. Checked before anything
	// is created.
	mode := string(host.NetworkMode)
	if mode == "default" {
		mode = ""
	}
	if strings.HasPrefix(mode, "container:") {
		httpError(w, http.StatusBadRequest, "the fake daemon does not share network namespaces (%s)", mode)
		return
	}
	var endpoints []string
	aliases := map[string][]string{}
	if req.NetworkingConfig != nil {
		for netName, ep := range req.NetworkingConfig.EndpointsConfig {
			if netName == "default" {
				netName = "bridge" // the daemon's name for its default
			}
			n := s.findNetworkLocked(netName)
			if n == nil {
				httpError(w, http.StatusNotFound, "network %s not found", netName)
				return
			}
			if ep != nil && len(ep.Aliases) > 0 {
				if n.predefined() {
					httpError(w, http.StatusBadRequest, "network-scoped alias is supported only for containers in user defined networks")
					return
				}
				aliases[n.Name] = append([]string(nil), ep.Aliases...)
			}
			endpoints = append(endpoints, n.Name)
		}
	}
	if mode != "" {
		n := s.findNetworkLocked(mode)
		if n == nil {
			httpError(w, http.StatusNotFound, "network %s not found", mode)
			return
		}
		if !hasName(endpoints, n.Name) {
			endpoints = append(endpoints, n.Name)
		}
	}
	if len(endpoints) == 0 {
		endpoints = []string{"bridge"}
	}
	sort.Strings(endpoints)

	// What the daemon stores is the request merged over the image's
	// configuration: inspect then reports both as the container's own.
	ic := img.configLocked()
	cfg := req.Config
	c := &Container{
		ID:             s.newIDLocked("container", name),
		Name:           name,
		Image:          cfg.Image,
		ImageID:        img.ID,
		Cmd:            append([]string(nil), cfg.Cmd...),
		Entrypoint:     append([]string(nil), cfg.Entrypoint...),
		Env:            mergeEnv(ic.Env, cfg.Env),
		Created:        time.Now(),
		State:          "created",
		Labels:         cloneMap(img.Labels),
		Tty:            cfg.Tty,
		OpenStdin:      cfg.OpenStdin,
		User:           cfg.User,
		WorkingDir:     cfg.WorkingDir,
		Hostname:       cfg.Hostname,
		StopSignal:     cfg.StopSignal,
		StopTimeout:    cfg.StopTimeout,
		Healthcheck:    cfg.Healthcheck,
		Init:           host.Init,
		Resources:      host.Resources,
		ShmSize:        host.ShmSize,
		NetworkMode:    mode,
		Networks:       map[string]*Endpoint{},
		RestartPolicy:  policy,
		RestartRetries: host.RestartPolicy.MaximumRetryCount,
		logs:           newLogBuffer(),
	}
	if host.LogConfig.Type != "" {
		c.LogDriver, c.LogOptions = host.LogConfig.Type, cloneMap(host.LogConfig.Config)
	}
	if len(cfg.Entrypoint) == 1 && cfg.Entrypoint[0] == "" {
		c.Entrypoint = nil // cleared: neither the request's nor the image's
	} else if len(cfg.Entrypoint) == 0 {
		c.Entrypoint = append([]string(nil), ic.Entrypoint...)
		if len(c.Cmd) == 0 {
			c.Cmd = append([]string(nil), ic.Cmd...)
		}
	}
	if len(c.Cmd) == 0 && len(c.Entrypoint) == 0 {
		c.Cmd = []string{"/bin/app"}
	}
	if len(c.Entrypoint) == 0 {
		c.Entrypoint = nil
	}
	if c.WorkingDir == "" {
		c.WorkingDir = ic.WorkingDir
	}
	if c.User == "" {
		c.User = ic.User
	}
	if c.Healthcheck == nil && len(ic.Healthcheck) > 0 {
		c.Healthcheck = &container.HealthConfig{Test: append([]string(nil), ic.Healthcheck...)}
	}
	if c.Labels == nil {
		c.Labels = map[string]string{}
	}
	for k, v := range cfg.Labels {
		c.Labels[k] = v
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
				ps.Random = true
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
			bound[port.String()] = true
			c.Ports = append(c.Ports, PortSpec{Private: port.Num(), Proto: string(port.Proto())})
		}
	}
	for _, e := range ic.ExposedPorts {
		port, err := network.ParsePort(e)
		if err == nil && !bound[port.String()] {
			bound[port.String()] = true
			c.Ports = append(c.Ports, PortSpec{Private: port.Num(), Proto: string(port.Proto())})
		}
	}
	sort.SliceStable(c.Ports, func(i, j int) bool {
		if c.Ports[i].Private != c.Ports[j].Private {
			return c.Ports[i].Private < c.Ports[j].Private
		}
		return c.Ports[i].Proto < c.Ports[j].Proto
	})

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
			c.Mounts = append(c.Mounts, s.volumeMountLocked(m.Source, m.Target, "", !m.ReadOnly))
		case mount.TypeBind:
			c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
		}
	}
	for _, b := range host.Binds {
		parts := strings.Split(b, ":")
		mode := ""
		if len(parts) > 2 {
			mode = parts[2]
		}
		rw := !strings.Contains(mode, "ro")
		if strings.HasPrefix(parts[0], "/") {
			c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: parts[0], Destination: parts[1], Mode: mode, RW: rw})
		} else {
			c.Mounts = append(c.Mounts, s.volumeMountLocked(parts[0], parts[1], mode, rw))
		}
	}

	for _, netName := range endpoints {
		if n := s.findNetworkLocked(netName); n != nil {
			s.attachLocked(c, n, false)
			c.Networks[n.Name].Aliases = aliases[n.Name]
		}
	}
	s.containers[c.ID] = c
	s.emit("container", "create", c.ID, map[string]string{"name": c.Name, "image": c.Image})
	writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

// volumeMountLocked mounts a named volume, creating it the way the daemon
// does when the name is new.
func (s *Server) volumeMountLocked(name, target, mode string, rw bool) MountSpec {
	if _, ok := s.volumes[name]; !ok {
		s.ensureVolumeLocked(name, nil)
		s.emit("volume", "create", name, map[string]string{"driver": "local"})
	}
	return MountSpec{Type: "volume", Name: name, Source: "/var/lib/fake-docker/volumes/" + name + "/_data", Destination: target, Mode: mode, RW: rw}
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
// the event. It is how tests move a name between two steps of a plan; it
// checks nothing, unlike the HTTP route.
func (s *Server) Rename(ref, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return false
	}
	s.renameLocked(c, name)
	return true
}

func (s *Server) renameLocked(c *Container, name string) {
	old := c.Name
	c.Name = name
	s.emit("container", "rename", c.ID, map[string]string{"name": name, "oldName": "/" + old, "image": c.Image})
}

// handleContainerRename is POST /containers/{id}/rename?name=…: 204 with
// the event, 404 for an unknown container, 400 for an invalid or unchanged
// name, 409 for a name another container holds — as the daemon answers.
func (s *Server) handleContainerRename(w http.ResponseWriter, r *http.Request, ref string) {
	name := strings.TrimPrefix(r.URL.Query().Get("name"), "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	if name == "" || !validContainerName.MatchString(name) {
		httpError(w, http.StatusBadRequest, "Invalid container name (%s), only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed", name)
		return
	}
	if name == c.Name {
		httpError(w, http.StatusBadRequest, "Renaming a container with the same name as its current name")
		return
	}
	for _, o := range s.containers {
		if o.Name == name {
			httpError(w, http.StatusConflict, "Conflict. The container name \"/%s\" is already in use by container \"%s\". You have to remove (or rename) that container to be able to reuse that name.", name, o.ID)
			return
		}
	}
	s.renameLocked(c, name)
	noContent(w)
}

// mergeEnv is the daemon's merge of an image's environment under a
// request's: the request's entries, then each image entry whose name the
// request does not set.
func mergeEnv(image, req []string) []string {
	out := append([]string(nil), req...)
	set := map[string]bool{}
	for _, e := range req {
		n, _, _ := strings.Cut(e, "=")
		set[n] = true
	}
	for _, e := range image {
		if n, _, _ := strings.Cut(e, "="); !set[n] {
			out = append(out, e)
		}
	}
	return out
}

func hasName(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CreateRequest is a POST /containers/create as the fake decoded it.
type CreateRequest struct {
	Name string // the name query parameter
	Body container.CreateRequest
	// Raw is the request body exactly as it arrived.
	Raw []byte
}

// CreateRequests returns every create request received, in order.
func (s *Server) CreateRequests() []CreateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CreateRequest(nil), s.createReqs...)
}
