package fakeengine

import (
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// Container is a container in the fake engine. Fields are guarded by the
// server's mutex.
type Container struct {
	ID            string
	Name          string
	Image         string // reference it was created from
	ImageID       string
	Cmd           []string
	Created       time.Time
	State         string
	ExitCode      int
	StartedAt     time.Time
	FinishedAt    time.Time
	Labels        map[string]string
	Tty           bool
	Mounts        []MountSpec
	Networks      map[string]*Endpoint // by network name
	Ports         []PortSpec
	RestartPolicy string
	Health        string

	// LogRate is the number of log lines per second the container writes
	// while running. Zero means it only has its seeded lines.
	LogRate int
	// LogLine, when set, is the text of every generated line (a sequence
	// number is appended). Used to produce very long or hostile lines.
	LogLine string
	// StopDelay is how long the container takes to stop gracefully.
	StopDelay time.Duration
	// NoShell makes every exec of a shell fail, like a distroless image.
	NoShell bool

	logs         *logBuffer
	runDone      chan struct{} // closed when the current run ends
	statsStalled bool
}

// Endpoint is a container's attachment to a network.
type Endpoint struct {
	NetworkID  string
	EndpointID string
	IP         string
	Gateway    string
	Mac        string
}

// MountSpec is a mount of a fake container.
type MountSpec struct {
	Type        string // "volume" or "bind"
	Name        string // volume name
	Source      string
	Destination string
	RW          bool
}

// PortSpec is a port mapping of a fake container.
type PortSpec struct {
	Private uint16
	Public  uint16 // 0 = exposed only
	Proto   string
}

// ContainerSpec describes a container to seed.
type ContainerSpec struct {
	Name     string
	Image    string
	State    string // default "running"
	ExitCode int
	Cmd      []string
	Labels   map[string]string
	// Volumes are names of volumes to mount (created if missing).
	Volumes []string
	// AnonymousVolumes is the number of anonymous volumes to create and mount.
	AnonymousVolumes int
	// Binds are host paths to bind-mount.
	Binds []string
	// Networks are names of networks to attach to (created if missing).
	// Default: ["bridge"].
	Networks  []string
	Ports     []PortSpec
	Tty       bool
	Logs      []string
	LogRate   int
	LogLine   string
	StopDelay time.Duration
	NoShell   bool
	Health    string
	// Age is how long ago the container was created and started.
	Age time.Duration
}

// AddContainer seeds a container and returns its ID. It emits no events:
// seeded state is what the engine looked like before the desk connected.
func (s *Server) AddContainer(spec ContainerSpec) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	img := s.ensureImageLocked(spec.Image)
	state := spec.State
	if state == "" {
		state = "running"
	}
	age := spec.Age
	if age == 0 {
		age = 10 * time.Minute
	}
	now := time.Now()
	c := &Container{
		ID:            s.newIDLocked("container", spec.Name),
		Name:          spec.Name,
		Image:         spec.Image,
		ImageID:       img.ID,
		Cmd:           spec.Cmd,
		Created:       now.Add(-age),
		State:         state,
		ExitCode:      spec.ExitCode,
		Labels:        cloneMap(spec.Labels),
		Tty:           spec.Tty,
		Networks:      map[string]*Endpoint{},
		Ports:         spec.Ports,
		RestartPolicy: "no",
		Health:        spec.Health,
		LogRate:       spec.LogRate,
		LogLine:       spec.LogLine,
		StopDelay:     spec.StopDelay,
		NoShell:       spec.NoShell,
		logs:          newLogBuffer(),
	}
	if len(c.Cmd) == 0 {
		c.Cmd = []string{"/bin/app"}
	}
	if c.Labels == nil {
		c.Labels = map[string]string{}
	}
	switch state {
	case "running", "paused", "restarting":
		c.StartedAt = now.Add(-age)
	case "exited", "dead":
		c.StartedAt = now.Add(-age)
		c.FinishedAt = now.Add(-age / 2)
	}
	for _, v := range spec.Volumes {
		s.ensureVolumeLocked(v, nil)
		c.Mounts = append(c.Mounts, MountSpec{Type: "volume", Name: v, Source: "/var/lib/fake-docker/volumes/" + v + "/_data", Destination: "/data/" + v, RW: true})
	}
	for i := 0; i < spec.AnonymousVolumes; i++ {
		name := stableID("anonvol", fmt.Sprintf("%s-%d", spec.Name, i))
		s.ensureVolumeLocked(name, map[string]string{"com.docker.volume.anonymous": ""})
		c.Mounts = append(c.Mounts, MountSpec{Type: "volume", Name: name, Source: "/var/lib/fake-docker/volumes/" + name + "/_data", Destination: fmt.Sprintf("/anon%d", i), RW: true})
	}
	for _, b := range spec.Binds {
		c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: b, Destination: b, RW: true})
	}
	nets := spec.Networks
	if nets == nil {
		nets = []string{"bridge"}
	}
	active := c.active()
	for _, name := range nets {
		n := s.networkByNameLocked(name)
		if n == nil {
			var labels map[string]string
			if p := c.Labels["com.docker.compose.project"]; p != "" {
				labels = map[string]string{"com.docker.compose.project": p, "com.docker.compose.network": strings.TrimPrefix(name, p+"_")}
			}
			n = s.addNetworkLocked(name, "bridge", "", labels)
		}
		s.attachLocked(c, n, active)
	}
	for _, l := range spec.Logs {
		c.logs.append(1, l)
	}
	s.containers[c.ID] = c
	if state == "running" {
		s.beginRunLocked(c)
	}
	return c.ID
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (c *Container) active() bool {
	return c.State == "running" || c.State == "paused" || c.State == "restarting"
}

// attachLocked records the container's membership of a network. Addresses
// are only allocated while the container is active, as in Docker.
func (s *Server) attachLocked(c *Container, n *Network, active bool) {
	ep := &Endpoint{NetworkID: n.ID}
	if active && n.Subnet != "" {
		s.assignAddressLocked(c, n, ep)
	}
	c.Networks[n.Name] = ep
}

func (s *Server) assignAddressLocked(c *Container, n *Network, ep *Endpoint) {
	if n.Subnet == "" {
		return
	}
	s.ipSeq[n.ID]++
	host := s.ipSeq[n.ID] + 1
	prefix := strings.TrimSuffix(n.Subnet, ".0.0/16")
	ep.IP = fmt.Sprintf("%s.0.%d", prefix, host)
	ep.Gateway = prefix + ".0.1"
	ep.Mac = fmt.Sprintf("02:42:ac:11:%02x:%02x", len(n.ID)%256, host%256)
	ep.EndpointID = stableID("endpoint", c.ID+n.ID)
}

// beginRunLocked starts whatever a running container does: writing logs.
func (s *Server) beginRunLocked(c *Container) {
	c.runDone = make(chan struct{})
	if c.LogRate > 0 {
		s.wg.Add(1)
		go s.generateLogs(c, c.runDone)
	}
}

func (c *Container) stopGenerators() {
	if c.runDone != nil {
		select {
		case <-c.runDone:
		default:
			close(c.runDone)
		}
	}
}

// endRunLocked makes the container exit: streams end, addresses are freed.
func (s *Server) endRunLocked(c *Container, exitCode int) {
	c.stopGenerators()
	c.State = "exited"
	c.ExitCode = exitCode
	c.FinishedAt = time.Now()
	for _, ep := range c.Networks {
		ep.IP, ep.Gateway, ep.Mac, ep.EndpointID = "", "", "", ""
	}
	c.logs.closeFollowers()
	for _, e := range s.execs {
		if e.containerID == c.ID {
			e.kill(137)
		}
	}
}

// findContainerLocked resolves a full ID, an ID prefix or a name.
func (s *Server) findContainerLocked(ref string) *Container {
	ref = strings.TrimPrefix(ref, "/")
	if c, ok := s.containers[ref]; ok {
		return c
	}
	for _, c := range s.containers {
		if c.Name == ref {
			return c
		}
	}
	var match *Container
	if len(ref) >= 2 {
		for id, c := range s.containers {
			if strings.HasPrefix(id, ref) {
				if match != nil {
					return nil
				}
				match = c
			}
		}
	}
	return match
}

func (s *Server) sortedContainersLocked() []*Container {
	out := make([]*Container, 0, len(s.containers))
	for _, c := range s.containers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func (c *Container) status() string {
	switch c.State {
	case "running":
		s := "Up " + humanDuration(time.Since(c.StartedAt))
		if c.Health != "" {
			s += " (" + c.Health + ")"
		}
		return s
	case "paused":
		return "Up " + humanDuration(time.Since(c.StartedAt)) + " (Paused)"
	case "restarting":
		return "Restarting"
	case "created":
		return "Created"
	case "exited":
		return fmt.Sprintf("Exited (%d) %s ago", c.ExitCode, humanDuration(time.Since(c.FinishedAt)))
	default:
		return c.State
	}
}

func parseAddr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

func (c *Container) endpointSettings() map[string]*network.EndpointSettings {
	out := map[string]*network.EndpointSettings{}
	for name, ep := range c.Networks {
		es := &network.EndpointSettings{
			NetworkID:  ep.NetworkID,
			EndpointID: ep.EndpointID,
			IPAddress:  parseAddr(ep.IP),
			Gateway:    parseAddr(ep.Gateway),
		}
		if ep.IP != "" {
			es.IPPrefixLen = 16
		}
		if ep.Mac != "" {
			_ = es.MacAddress.UnmarshalText([]byte(ep.Mac))
		}
		out[name] = es
	}
	return out
}

func (c *Container) mountPoints() []container.MountPoint {
	out := make([]container.MountPoint, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		mp := container.MountPoint{
			Type:        mount.Type(m.Type),
			Name:        m.Name,
			Source:      m.Source,
			Destination: m.Destination,
			RW:          m.RW,
		}
		if m.Type == "volume" {
			mp.Driver = "local"
		}
		out = append(out, mp)
	}
	return out
}

func (c *Container) summary() container.Summary {
	sum := container.Summary{
		ID:              c.ID,
		Names:           []string{"/" + c.Name},
		Image:           c.Image,
		ImageID:         "sha256:" + c.ImageID,
		Command:         strings.Join(c.Cmd, " "),
		Created:         c.Created.Unix(),
		Labels:          c.Labels,
		State:           container.ContainerState(c.State),
		Status:          c.status(),
		NetworkSettings: &container.NetworkSettingsSummary{Networks: c.endpointSettings()},
		Mounts:          c.mountPoints(),
		Ports:           []container.PortSummary{},
	}
	sum.HostConfig.NetworkMode = "bridge"
	if c.Health != "" {
		sum.Health = &container.HealthSummary{Status: container.HealthStatus(c.Health)}
	}
	if c.active() {
		for _, p := range c.Ports {
			ps := container.PortSummary{PrivatePort: p.Private, PublicPort: p.Public, Type: p.Proto}
			if p.Public != 0 {
				ps.IP = parseAddr("0.0.0.0")
			}
			sum.Ports = append(sum.Ports, ps)
		}
	}
	return sum
}

func (s *Server) handleContainerList(w http.ResponseWriter, r *http.Request) {
	all := boolParam(r, "all")
	filters := parseFilters(r)
	s.mu.Lock()
	out := []container.Summary{}
	for _, c := range s.sortedContainersLocked() {
		if !all && !c.active() {
			continue
		}
		if !matchContainerFilters(c, filters) {
			continue
		}
		out = append(out, c.summary())
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func matchContainerFilters(c *Container, f map[string][]string) bool {
	for term, values := range f {
		ok := false
		for _, v := range values {
			switch term {
			case "status":
				ok = ok || c.State == v
			case "name":
				ok = ok || strings.Contains(c.Name, strings.TrimPrefix(v, "/"))
			case "id":
				ok = ok || strings.HasPrefix(c.ID, v)
			case "label":
				if k, val, has := strings.Cut(v, "="); has {
					ok = ok || c.Labels[k] == val
				} else {
					_, present := c.Labels[k]
					ok = ok || present
				}
			case "volume":
				for _, m := range c.Mounts {
					ok = ok || m.Name == v || m.Destination == v
				}
			case "network":
				for name, ep := range c.Networks {
					ok = ok || name == v || ep.NetworkID == v
				}
			case "ancestor":
				ok = ok || c.Image == v || c.ImageID == strings.TrimPrefix(v, "sha256:")
			default:
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) handleContainerInspect(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	state := &container.State{
		Status:     container.ContainerState(c.State),
		Running:    c.State == "running" || c.State == "paused",
		Paused:     c.State == "paused",
		Restarting: c.State == "restarting",
		ExitCode:   c.ExitCode,
		StartedAt:  rfc3339(c.StartedAt),
		FinishedAt: rfc3339(c.FinishedAt),
	}
	if state.Running {
		state.Pid = 4242
	}
	if c.Health != "" {
		state.Health = &container.Health{Status: container.HealthStatus(c.Health)}
	}
	ports := network.PortMap{}
	if c.active() {
		for _, p := range c.Ports {
			port, ok := network.PortFrom(p.Private, network.IPProtocol(p.Proto))
			if !ok {
				continue
			}
			if p.Public == 0 {
				ports[port] = nil
				continue
			}
			ports[port] = append(ports[port], network.PortBinding{HostIP: parseAddr("0.0.0.0"), HostPort: strconv.Itoa(int(p.Public))})
		}
	}
	resp := container.InspectResponse{
		ID:           c.ID,
		Created:      c.Created.UTC().Format(time.RFC3339Nano),
		Path:         c.Cmd[0],
		Args:         c.Cmd[1:],
		State:        state,
		Image:        "sha256:" + c.ImageID,
		Name:         "/" + c.Name,
		RestartCount: 0,
		Driver:       "overlayfs",
		Platform:     "linux",
		Mounts:       c.mountPoints(),
		Config: &container.Config{
			Hostname: short(c.ID),
			Image:    c.Image,
			Cmd:      c.Cmd,
			Labels:   c.Labels,
			Tty:      c.Tty,
			Env:      []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   "bridge",
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(c.RestartPolicy)},
		},
		NetworkSettings: &container.NetworkSettings{
			Ports:    ports,
			Networks: c.endpointSettings(),
		},
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return "0001-01-01T00:00:00Z"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request, ref, action string) {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	if c == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	attrs := func() map[string]string {
		a := cloneMap(c.Labels)
		if a == nil {
			a = map[string]string{}
		}
		a["name"] = c.Name
		a["image"] = c.Image
		return a
	}

	switch action {
	case "start":
		switch c.State {
		case "running":
			s.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		case "paused":
			s.mu.Unlock()
			httpError(w, http.StatusConflict, "cannot start a paused container, try unpause instead")
			return
		}
		s.startLocked(c, attrs())
		s.mu.Unlock()
		noContent(w)

	case "stop":
		if !c.active() {
			s.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		delay := c.StopDelay
		s.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		if c.active() {
			s.stopLocked(c, attrs(), "15", 0)
		}
		s.mu.Unlock()
		noContent(w)

	case "restart":
		delay := c.StopDelay
		wasActive := c.active()
		s.mu.Unlock()
		if wasActive && delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		if c.active() {
			s.stopLocked(c, attrs(), "15", 0)
		}
		s.startLocked(c, attrs())
		s.emit("container", "restart", c.ID, attrs())
		s.mu.Unlock()
		noContent(w)

	case "pause":
		switch c.State {
		case "paused":
			s.mu.Unlock()
			httpError(w, http.StatusConflict, "Container %s is already paused", c.ID)
			return
		case "running":
		default:
			s.mu.Unlock()
			httpError(w, http.StatusConflict, "Container %s is not running", c.ID)
			return
		}
		c.State = "paused"
		s.emit("container", "pause", c.ID, attrs())
		s.mu.Unlock()
		noContent(w)

	case "unpause":
		if c.State != "paused" {
			s.mu.Unlock()
			httpError(w, http.StatusConflict, "Container %s is not paused", c.ID)
			return
		}
		c.State = "running"
		s.emit("container", "unpause", c.ID, attrs())
		s.mu.Unlock()
		noContent(w)

	case "kill":
		if !c.active() {
			s.mu.Unlock()
			httpError(w, http.StatusConflict, "cannot kill container: %s: container %s is not running", ref, c.ID)
			return
		}
		sig := strings.TrimPrefix(strings.ToUpper(r.URL.Query().Get("signal")), "SIG")
		num, code := "9", 137
		switch sig {
		case "", "KILL", "9":
		case "TERM", "15":
			num, code = "15", 143
		case "INT", "2":
			num, code = "2", 130
		default:
			num = sig
		}
		a := attrs()
		a["signal"] = num
		s.emit("container", "kill", c.ID, a)
		s.dieLocked(c, attrs(), code)
		s.mu.Unlock()
		noContent(w)

	default:
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "page not found")
	}
}

func (s *Server) startLocked(c *Container, attrs map[string]string) {
	c.State = "running"
	c.StartedAt = time.Now()
	c.FinishedAt = time.Time{}
	c.ExitCode = 0
	for name, ep := range c.Networks {
		if n := s.networks[ep.NetworkID]; n != nil {
			s.assignAddressLocked(c, n, ep)
			s.emit("network", "connect", n.ID, map[string]string{"container": c.ID, "name": name, "type": n.Driver})
		}
	}
	s.beginRunLocked(c)
	c.logs.append(1, "container started")
	s.emit("container", "start", c.ID, attrs)
}

func (s *Server) stopLocked(c *Container, attrs map[string]string, signal string, exitCode int) {
	a := cloneMap(attrs)
	a["signal"] = signal
	s.emit("container", "kill", c.ID, a)
	s.dieLocked(c, attrs, exitCode)
	s.emit("container", "stop", c.ID, attrs)
}

func (s *Server) dieLocked(c *Container, attrs map[string]string, exitCode int) {
	type netRef struct{ id, name, driver string }
	var nets []netRef
	for name, ep := range c.Networks {
		if n := s.networks[ep.NetworkID]; n != nil {
			nets = append(nets, netRef{n.ID, name, n.Driver})
		}
	}
	s.endRunLocked(c, exitCode)
	a := cloneMap(attrs)
	a["exitCode"] = strconv.Itoa(exitCode)
	s.emit("container", "die", c.ID, a)
	for _, n := range nets {
		s.emit("network", "disconnect", n.id, map[string]string{"container": c.ID, "name": n.name, "type": n.driver})
	}
}

// Crash makes a running container die with the given exit code, as if its
// process had exited on its own.
func (s *Server) Crash(ref string, exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil || !c.active() {
		return
	}
	a := cloneMap(c.Labels)
	if a == nil {
		a = map[string]string{}
	}
	a["name"], a["image"] = c.Name, c.Image
	s.dieLocked(c, a, exitCode)
}

func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request, ref string) {
	force := boolParam(r, "force")
	removeVolumes := boolParam(r, "v")
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	attrs := cloneMap(c.Labels)
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["name"], attrs["image"] = c.Name, c.Image
	if c.active() {
		if !force {
			what := "running"
			if c.State == "paused" {
				what = "paused"
			}
			httpError(w, http.StatusConflict,
				"cannot remove container \"/%s\": container is %s: stop the container before removing or force remove", c.Name, what)
			return
		}
		a := cloneMap(attrs)
		a["signal"] = "9"
		s.emit("container", "kill", c.ID, a)
		s.dieLocked(c, attrs, 137)
	}
	c.stopGenerators()
	delete(s.containers, c.ID)
	s.emit("container", "destroy", c.ID, attrs)
	if removeVolumes {
		for _, m := range c.Mounts {
			v := s.volumes[m.Name]
			if m.Type == "volume" && v != nil && v.anonymous() && len(s.volumeUsersLocked(m.Name)) == 0 {
				delete(s.volumes, m.Name)
				s.emit("volume", "destroy", m.Name, map[string]string{"driver": "local"})
			}
		}
	}
	noContent(w)
}

// ---- test and sandbox helpers --------------------------------------------------

// ContainerState returns the state of a container by name or ID.
func (s *Server) ContainerState(ref string) (state string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return "", false
	}
	return c.State, true
}

// ContainerID returns the full ID of a container by name.
func (s *Server) ContainerID(ref string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.findContainerLocked(ref); c != nil {
		return c.ID
	}
	return ""
}

// ContainerNames returns the names of all containers, sorted.
func (s *Server) ContainerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.containers {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// ContainerNetworks returns the names of the networks a container is on.
func (s *Server) ContainerNetworks(ref string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return nil
	}
	var out []string
	for n := range c.Networks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// WriteLog makes a container write one line to stdout (stream 1) or stderr
// (stream 2).
func (s *Server) WriteLog(ref string, stream byte, line string) {
	s.mu.Lock()
	c := s.findContainerLocked(ref)
	s.mu.Unlock()
	if c != nil {
		c.logs.append(stream, line)
	}
}

// SetLogRate changes how many lines per second a running container writes.
func (s *Server) SetLogRate(ref string, linesPerSecond int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		return
	}
	had := c.LogRate > 0
	c.LogRate = linesPerSecond
	if c.State == "running" && !had && linesPerSecond > 0 {
		s.wg.Add(1)
		go s.generateLogs(c, c.runDone)
	}
}
