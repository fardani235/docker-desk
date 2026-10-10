// Package engine is the desk's only door to the Docker daemon.
//
// The rest of the program depends on the Engine interface and on the small
// value types in this file, never on the client library's types. That keeps
// library churn in one place and lets read-only mode be a wrapper.
//
// There is deliberately no "force" parameter anywhere in this package: a
// forced removal cannot be requested by mistake because it cannot be
// expressed. See docs/DECISIONS.md, D6 and D13.
package engine

import (
	"strings"
	"time"
)

// Label keys the desk gives meaning to.
const (
	LabelComposeProject    = "com.docker.compose.project"
	LabelComposeService    = "com.docker.compose.service"
	LabelComposeWorkingDir = "com.docker.compose.project.working_dir"
	LabelComposeConfig     = "com.docker.compose.project.config_files"
	LabelComposeNetwork    = "com.docker.compose.network"
	LabelComposeVolume     = "com.docker.compose.volume"
	// LabelAnonymousVolume is set by the daemon (Docker 23+) on volumes that
	// were created without a name.
	LabelAnonymousVolume = "com.docker.volume.anonymous"
)

// Container states as reported by the daemon.
const (
	StateCreated    = "created"
	StateRunning    = "running"
	StatePaused     = "paused"
	StateRestarting = "restarting"
	StateRemoving   = "removing"
	StateExited     = "exited"
	StateDead       = "dead"
)

// Info identifies the daemon.
type Info struct {
	Name            string
	ServerVersion   string
	APIVersion      string
	OperatingSystem string
	OSType          string
	Architecture    string
	NCPU            int
	MemTotal        int64
	StorageDriver   string
	RootDir         string
}

// Port is one published or exposed port of a container.
type Port struct {
	HostIP      string
	HostPort    uint16
	PrivatePort uint16
	Proto       string
}

// Mount is one mount of a container.
type Mount struct {
	Type        string // volume, bind, tmpfs, …
	Name        string // volume name when Type is "volume"
	Source      string
	Destination string
	RW          bool
}

// Attachment is a container's membership of a network.
type Attachment struct {
	Network   string // network name as the container knows it
	NetworkID string
	IP        string
	Gateway   string
	Mac       string
}

// Container is one row of the container list.
type Container struct {
	ID      string
	Name    string
	Image   string
	ImageID string
	Command string
	Created time.Time
	State   string
	Status  string
	Health  string
	Ports   []Port
	Labels  map[string]string
	Mounts  []Mount
	// Networks lists every network the container is attached to or, when it
	// is stopped, configured to use.
	Networks []Attachment
}

// IsActive reports whether the container holds running processes, i.e. a
// non-forced removal would be refused.
func (c Container) IsActive() bool {
	return c.State == StateRunning || c.State == StatePaused || c.State == StateRestarting
}

// Project returns the compose project the container is labelled with.
func (c Container) Project() string { return c.Labels[LabelComposeProject] }

// Service returns the compose service the container is labelled with.
func (c Container) Service() string { return c.Labels[LabelComposeService] }

// ContainerDetail is what inspect adds to a Container.
type ContainerDetail struct {
	Container
	Path          string
	Args          []string
	Entrypoint    []string
	Cmd           []string
	WorkingDir    string
	User          string
	Hostname      string
	TTY           bool
	Pid           int
	ExitCode      int
	OOMKilled     bool
	Error         string
	StartedAt     time.Time
	FinishedAt    time.Time
	RestartPolicy string
	RestartCount  int
	NetworkMode   string
	Privileged    bool
	EnvCount      int
	// Raw is the daemon's complete inspect document.
	Raw []byte
}

// Image is one image known to the daemon. The list shows one row per
// reference; see Refs.
type Image struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	Size        int64
	Created     time.Time
	Labels      map[string]string
	// Dangling is the daemon's own verdict (its dangling=true filter), not a
	// guess from RepoTags: with the containerd image store an image pulled by
	// digest has no tag yet is not dangling.
	Dangling bool
	// Containers is the daemon's count of containers using the image, or -1
	// when the daemon did not compute it.
	Containers int64
}

// Refs returns the references the image can be addressed (and untagged) by:
// its repository tags, including digest-only references such as
// "repo@sha256:…" that the containerd image store reports as tags.
func (i Image) Refs() []string {
	var out []string
	for _, t := range i.RepoTags {
		if t != "" && t != "<none>:<none>" && t != "<none>@<none>" {
			out = append(out, t)
		}
	}
	return out
}

// SplitRef splits an image reference into repository and tag for display.
// A digest reference has the tag "<none>".
func SplitRef(ref string) (repo, tag string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at], "<none>"
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "<none>"
}

// ImageDetail is what inspect adds to an Image.
type ImageDetail struct {
	Image
	Architecture string
	OS           string
	Author       string
	Entrypoint   []string
	Cmd          []string
	WorkingDir   string
	User         string
	ExposedPorts []string
	Layers       int
	Raw          []byte
}

// ImageDelete is one line of the daemon's answer to an image removal.
type ImageDelete struct {
	Untagged string
	Deleted  string
}

// Volume is one volume.
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	Scope      string
	CreatedAt  string
	Labels     map[string]string
	Options    map[string]string
}

// Anonymous reports whether the daemon marked the volume as created without a
// name. Only the daemon's own label is trusted; a 64-hex name alone is not.
func (v Volume) Anonymous() bool {
	_, ok := v.Labels[LabelAnonymousVolume]
	return ok
}

// VolumeDetail is a Volume plus the raw inspect document.
type VolumeDetail struct {
	Volume
	Raw []byte
}

// Network is one network.
type Network struct {
	ID       string
	Name     string
	Driver   string
	Scope    string
	Internal bool
	IPv6     bool
	Created  time.Time
	Labels   map[string]string
	Subnets  []string
	Gateways []string
}

// Predefined reports whether this is one of the networks the daemon creates
// itself and refuses to remove.
func (n Network) Predefined() bool {
	return n.Name == "bridge" || n.Name == "host" || n.Name == "none"
}

// NetworkEndpoint is a container currently attached to a network.
type NetworkEndpoint struct {
	ContainerID string
	Name        string
	IPv4        string
	IPv6        string
	Mac         string
}

// NetworkDetail is what inspect adds to a Network.
type NetworkDetail struct {
	Network
	Endpoints []NetworkEndpoint
	Options   map[string]string
	Raw       []byte
}

// Event is one message of the engine's event stream.
type Event struct {
	Time       time.Time
	Type       string // container, image, volume, network, …
	Action     string // start, die, pull, connect, …
	ID         string
	Attributes map[string]string
}

// Name returns the most useful short name for the event's object.
func (e Event) Name() string {
	if n := e.Attributes["name"]; n != "" {
		return n
	}
	return ShortID(e.ID)
}

// PullProgress is the aggregated state of an image pull.
type PullProgress struct {
	Status  string // latest status line from the daemon
	Layer   string // layer the status line refers to, if any
	Current int64  // bytes done across all layers that report totals
	Total   int64  // bytes expected across those layers
}

// RawStats is one raw statistics sample for a container. Rates and
// percentages are derived from pairs of samples by package live.
type RawStats struct {
	Read       time.Time
	CPUTotal   uint64 // cumulative container CPU time, ns
	CPUSystem  uint64 // cumulative host CPU time, ns
	OnlineCPUs uint32
	MemUsage   uint64
	MemCache   uint64 // reclaimable file cache included in MemUsage
	MemLimit   uint64
	NetRx      uint64
	NetTx      uint64
	BlkRead    uint64
	BlkWrite   uint64
	Pids       uint64
}

// ExecStatus is the state of an exec'd process.
type ExecStatus struct {
	Running  bool
	ExitCode int
}

// ShortID returns the 12-character form of an object ID, as the CLI prints it.
func ShortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
