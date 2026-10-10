package fakeengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

// Volume is a volume in the fake engine.
type Volume struct {
	Name    string
	Created time.Time
	Labels  map[string]string
}

func (v *Volume) anonymous() bool {
	_, ok := v.Labels["com.docker.volume.anonymous"]
	return ok
}

// AddVolume seeds a volume.
func (s *Server) AddVolume(name string, labels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureVolumeLocked(name, labels)
}

// AddAnonymousVolume seeds an unused anonymous volume and returns its name.
func (s *Server) AddAnonymousVolume(seed string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := stableID("anonvol", seed)
	s.ensureVolumeLocked(name, map[string]string{"com.docker.volume.anonymous": ""})
	return name
}

func (s *Server) ensureVolumeLocked(name string, labels map[string]string) *Volume {
	if v, ok := s.volumes[name]; ok {
		return v
	}
	v := &Volume{Name: name, Created: time.Now().Add(-time.Hour), Labels: cloneMap(labels)}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	s.volumes[name] = v
	return v
}

func (s *Server) volumeUsersLocked(name string) []*Container {
	var out []*Container
	for _, c := range s.sortedContainersLocked() {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == name {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func (v *Volume) api() volume.Volume {
	return volume.Volume{
		Name:       v.Name,
		Driver:     "local",
		Mountpoint: "/var/lib/fake-docker/volumes/" + v.Name + "/_data",
		CreatedAt:  v.Created.UTC().Format(time.RFC3339),
		Labels:     v.Labels,
		Options:    map[string]string{},
		Scope:      "local",
	}
}

func (s *Server) handleVolumeList(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)
	s.mu.Lock()
	names := make([]string, 0, len(s.volumes))
	for n := range s.volumes {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []volume.Volume{}
	for _, n := range names {
		if vs, ok := filters["dangling"]; ok && len(vs) > 0 {
			want := vs[0] == "true" || vs[0] == "1"
			if (len(s.volumeUsersLocked(n)) == 0) != want {
				continue
			}
		}
		out = append(out, s.volumes[n].api())
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"Volumes": out, "Warnings": []string{}})
}

func (s *Server) handleVolumeInspect(w http.ResponseWriter, name string) {
	s.mu.Lock()
	v, ok := s.volumes[name]
	var resp volume.Volume
	if ok {
		resp = v.api()
	}
	s.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, "get %s: no such volume", name)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleVolumeCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string
		Driver string
		Labels map[string]string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.volumes[body.Name]; ok && body.Name != "" {
		writeJSON(w, http.StatusCreated, v.api())
		return
	}
	labels := cloneMap(body.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	name := body.Name
	if name == "" {
		name = s.newIDLocked("volume", "anonymous")
		labels["com.docker.volume.anonymous"] = ""
	}
	v := &Volume{Name: name, Created: time.Now(), Labels: labels}
	s.volumes[name] = v
	s.emit("volume", "create", name, map[string]string{"driver": "local"})
	writeJSON(w, http.StatusCreated, v.api())
}

func (s *Server) handleVolumeRemove(w http.ResponseWriter, r *http.Request, name string) {
	_ = boolParam(r, "force") // recorded in the request log; never honoured for in-use volumes
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.volumes[name]; !ok {
		httpError(w, http.StatusNotFound, "get %s: no such volume", name)
		return
	}
	if users := s.volumeUsersLocked(name); len(users) > 0 {
		ids := make([]string, len(users))
		for i, u := range users {
			ids[i] = u.ID
		}
		httpError(w, http.StatusConflict, "remove %s: volume is in use - [%s]", name, strings.Join(ids, ", "))
		return
	}
	delete(s.volumes, name)
	s.emit("volume", "destroy", name, map[string]string{"driver": "local"})
	noContent(w)
}

// VolumeNames returns the names of all volumes, sorted.
func (s *Server) VolumeNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.volumes))
	for n := range s.volumes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---- networks ---------------------------------------------------------------------

// Network is a network in the fake engine.
type Network struct {
	ID      string
	Name    string
	Driver  string
	Subnet  string
	Created time.Time
	Labels  map[string]string
}

// AddNetwork seeds a custom bridge network and returns its ID.
func (s *Server) AddNetwork(name string, labels map[string]string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.networkByNameLocked(name); n != nil {
		return n.ID
	}
	return s.addNetworkLocked(name, "bridge", "", labels).ID
}

func (s *Server) addNetworkLocked(name, driver, subnet string, labels map[string]string) *Network {
	n := &Network{
		ID:      s.newIDLocked("network", name),
		Name:    name,
		Driver:  driver,
		Subnet:  subnet,
		Created: time.Now().Add(-2 * time.Hour),
		Labels:  cloneMap(labels),
	}
	if n.Labels == nil {
		n.Labels = map[string]string{}
	}
	if subnet == "" && driver == "bridge" {
		custom := 0
		for _, o := range s.networks {
			if o.Driver == "bridge" {
				custom++
			}
		}
		n.Subnet = fmt.Sprintf("172.%d.0.0/16", 17+custom)
	}
	s.networks[n.ID] = n
	return n
}

func (s *Server) networkByNameLocked(name string) *Network {
	for _, n := range s.networks {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func (s *Server) findNetworkLocked(ref string) *Network {
	if n, ok := s.networks[ref]; ok {
		return n
	}
	if n := s.networkByNameLocked(ref); n != nil {
		return n
	}
	var match *Network
	if len(ref) >= 4 {
		for id, n := range s.networks {
			if strings.HasPrefix(id, ref) {
				if match != nil {
					return nil
				}
				match = n
			}
		}
	}
	return match
}

func (n *Network) predefined() bool {
	return n.Name == "bridge" || n.Name == "host" || n.Name == "none"
}

func (n *Network) api() network.Network {
	out := network.Network{
		Name:       n.Name,
		ID:         n.ID,
		Created:    n.Created,
		Scope:      "local",
		Driver:     n.Driver,
		EnableIPv4: n.Subnet != "",
		IPAM:       network.IPAM{Driver: "default", Config: []network.IPAMConfig{}},
		Options:    map[string]string{},
		Labels:     n.Labels,
	}
	if n.Subnet != "" {
		p, _ := netip.ParsePrefix(n.Subnet)
		gw, _ := netip.ParseAddr(strings.TrimSuffix(n.Subnet, ".0.0/16") + ".0.1")
		out.IPAM.Config = append(out.IPAM.Config, network.IPAMConfig{Subnet: p, Gateway: gw})
	}
	return out
}

// activeEndpointsLocked returns the active containers attached to a network.
func (s *Server) activeEndpointsLocked(n *Network) map[string]network.EndpointResource {
	out := map[string]network.EndpointResource{}
	for _, c := range s.containers {
		if !c.active() {
			continue
		}
		for _, ep := range c.Networks {
			if ep.NetworkID != n.ID {
				continue
			}
			res := network.EndpointResource{Name: c.Name, EndpointID: ep.EndpointID}
			if ep.IP != "" {
				res.IPv4Address, _ = netip.ParsePrefix(ep.IP + "/16")
			}
			if ep.Mac != "" {
				_ = res.MacAddress.UnmarshalText([]byte(ep.Mac))
			}
			out[c.ID] = res
		}
	}
	return out
}

func (s *Server) handleNetworkList(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)
	s.mu.Lock()
	nets := make([]*Network, 0, len(s.networks))
	for _, n := range s.networks {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	out := []network.Summary{}
	for _, n := range nets {
		if vs, ok := filters["name"]; ok && len(vs) > 0 && !strings.Contains(n.Name, vs[0]) {
			continue
		}
		out = append(out, network.Summary{Network: n.api()})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNetworkInspect(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	n := s.findNetworkLocked(ref)
	if n == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "network %s not found", ref)
		return
	}
	resp := network.Inspect{Network: n.api(), Containers: s.activeEndpointsLocked(n)}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleNetworkCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string
		Driver string
		Labels map[string]string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		httpError(w, http.StatusBadRequest, "network name is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networkByNameLocked(body.Name) != nil {
		httpError(w, http.StatusConflict, "network with name %s already exists", body.Name)
		return
	}
	driver := body.Driver
	if driver == "" {
		driver = "bridge"
	}
	n := s.addNetworkLocked(body.Name, driver, "", body.Labels)
	n.Created = time.Now()
	s.emit("network", "create", n.ID, map[string]string{"name": n.Name, "type": n.Driver})
	writeJSON(w, http.StatusCreated, map[string]any{"Id": n.ID, "Warning": ""})
}

func (s *Server) handleNetworkRemove(w http.ResponseWriter, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.findNetworkLocked(ref)
	if n == nil {
		httpError(w, http.StatusNotFound, "network %s not found", ref)
		return
	}
	if n.predefined() {
		httpError(w, http.StatusForbidden, "%s is a pre-defined network and cannot be removed", n.Name)
		return
	}
	if eps := s.activeEndpointsLocked(n); len(eps) > 0 {
		var names []string
		for _, e := range eps {
			names = append(names, e.Name)
		}
		sort.Strings(names)
		httpError(w, http.StatusForbidden,
			"error while removing network: network %s has active endpoints (%s)", n.Name, strings.Join(names, ", "))
		return
	}
	delete(s.networks, n.ID)
	s.emit("network", "destroy", n.ID, map[string]string{"name": n.Name, "type": n.Driver})
	noContent(w)
}

func (s *Server) handleNetworkConnect(w http.ResponseWriter, r *http.Request, ref string) {
	var body struct{ Container string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.findNetworkLocked(ref)
	if n == nil {
		httpError(w, http.StatusNotFound, "network %s not found", ref)
		return
	}
	c := s.findContainerLocked(body.Container)
	if c == nil {
		httpError(w, http.StatusNotFound, "No such container: %s", body.Container)
		return
	}
	if n.Driver == "host" || n.Driver == "null" {
		httpError(w, http.StatusForbidden, "container cannot be connected to network %s: it is not attachable", n.Name)
		return
	}
	if _, ok := c.Networks[n.Name]; ok {
		httpError(w, http.StatusForbidden, "endpoint with name %s already exists in network %s", c.Name, n.Name)
		return
	}
	s.attachLocked(c, n, c.active())
	s.emit("network", "connect", n.ID, map[string]string{"container": c.ID, "name": n.Name, "type": n.Driver})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleNetworkDisconnect(w http.ResponseWriter, r *http.Request, ref string) {
	var body struct {
		Container string
		Force     bool
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.findNetworkLocked(ref)
	if n == nil {
		httpError(w, http.StatusNotFound, "network %s not found", ref)
		return
	}
	c := s.findContainerLocked(body.Container)
	if c == nil {
		httpError(w, http.StatusNotFound, "No such container: %s", body.Container)
		return
	}
	if _, ok := c.Networks[n.Name]; !ok {
		httpError(w, http.StatusForbidden, "container %s is not connected to network %s", c.ID, n.Name)
		return
	}
	delete(c.Networks, n.Name)
	s.emit("network", "disconnect", n.ID, map[string]string{"container": c.ID, "name": n.Name, "type": n.Driver})
	w.WriteHeader(http.StatusOK)
}

// NetworkNames returns the names of all networks, sorted.
func (s *Server) NetworkNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.networks))
	for _, n := range s.networks {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out
}

// NetworkID returns the ID of a network by name.
func (s *Server) NetworkID(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.networkByNameLocked(name); n != nil {
		return n.ID
	}
	return ""
}
