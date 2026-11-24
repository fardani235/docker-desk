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
	"io"
	"io/fs"
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
	// LabelComposeConfigHash is compose's fingerprint of the service
	// configuration a container was created from. The Declared tab shows it
	// as evidence; the desk never recomputes it.
	LabelComposeConfigHash = "com.docker.compose.config-hash"
	// LabelComposeOneOff is "True" on containers left by `docker compose
	// run`; they are not compared against the declared services.
	LabelComposeOneOff = "com.docker.compose.oneoff"
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
	// LoggingDriver, DefaultRuntime and CgroupVersion are the daemon's
	// defaults, which a container's inspect reports as its own settings.
	LoggingDriver  string
	DefaultRuntime string
	CgroupVersion  string
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
	// NetworkMode is the container's network mode as the list reports it:
	// "bridge", "host", "none", "container:…" or a network name.
	NetworkMode string
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
	Variant      string
	Author       string
	Entrypoint   []string
	Cmd          []string
	WorkingDir   string
	User         string
	ExposedPorts []string
	Layers       int
	Raw          []byte
}

// Platform names an image variant: operating system, architecture and,
// where the architecture has them, a variant ("v8" for arm64).
type Platform struct {
	OS           string
	Architecture string
	Variant      string
}

// String is the usual os/arch[/variant] form.
func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Platform returns the platform the image was inspected as.
func (d ImageDetail) Platform() Platform {
	return Platform{OS: d.OS, Architecture: d.Architecture, Variant: d.Variant}
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

// PushProgress is the aggregated state of an image push. Every string is
// the daemon's, scrubbed of the credential the push was sent with.
type PushProgress struct {
	Status  string // latest status line from the daemon
	Layer   string // layer the status line refers to, if any
	Current int64  // bytes uploaded across all layers that report totals
	Total   int64  // bytes to upload across those layers
	Layers  int    // layers the daemon has named
	Done    int    // of those, pushed or already present in the registry
}

// PushResult is what the daemon reported at the end of a push that it
// completed: the tag it pushed and the manifest digest the registry now
// holds under it, as the daemon stated them — never computed by the desk.
type PushResult struct {
	Tag    string
	Digest string // sha256:<64 hex>
	Size   int64  // the manifest's size in bytes, when reported
	Layers int    // layers the daemon named
	Pushed int    // of those, uploaded by this push
	Exists int    // of those, already present in the registry
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

// ---- disk usage -----------------------------------------------------------

// Usage is one line of the daemon's own disk account (`docker system df`):
// how many objects of a type, how many are in use, the bytes they hold and
// the part the daemon considers reclaimable.
type Usage struct {
	TotalCount  int64
	ActiveCount int64
	TotalSize   int64
	Reclaimable int64
}

// DiskUsage is the daemon's answer to GET /system/df.
type DiskUsage struct {
	Images, Containers, Volumes, BuildCache Usage
	// Derived is set when the daemon is older than API 1.52. Such a daemon
	// states no totals, only per-object lists; the client library sums them
	// exactly as the docker CLI does, and the desk says so.
	Derived bool

	ImageItems  []ImageUsage
	VolumeItems []VolumeUsage
	BuildItems  []BuildCacheUsage
}

// ImageUsage is the daemon's size account of one image.
type ImageUsage struct {
	ID       string
	RepoTags []string
	Created  time.Time
	// Size counts every layer of the image. SharedSize counts the layers
	// it shares with at least one other image; -1 when not computed.
	Size       int64
	SharedSize int64
	// Containers is the daemon's count of containers using the image, or -1.
	Containers int64
}

// Unique is what the image holds that no other image holds — the CLI's
// UNIQUE SIZE — or -1 when the daemon did not compute the shared size.
func (u ImageUsage) Unique() int64 {
	if u.SharedSize < 0 || u.Size < 0 {
		return -1
	}
	return u.Size - u.SharedSize
}

// VolumeUsage is the daemon's size account of one volume.
type VolumeUsage struct {
	Name   string
	Driver string
	// Size is -1 when the daemon could not measure it (non-local driver).
	Size     int64
	RefCount int64
}

// BuildCacheUsage is one build-cache record.
type BuildCacheUsage struct {
	ID          string
	Type        string
	Description string
	Size        int64
	InUse       bool
	Shared      bool
	LastUsed    time.Time
}

// BuildCachePruneReport is the daemon's answer to POST /build/prune: the
// records it deleted and the bytes it says it reclaimed.
type BuildCachePruneReport struct {
	Deleted   []string
	Reclaimed int64
}

// ContainerSize is a container's writable layer as the daemon measured it
// by walking its filesystem.
type ContainerSize struct {
	ID    string
	Name  string
	State string
	// SizeRw is the writable layer; SizeRootFs adds the image's layers.
	SizeRw     int64
	SizeRootFs int64
}

// ImageLayers is what the daemon says about an image's layers: their diff
// IDs, in order (inspect), and its build history, oldest entry first.
type ImageLayers struct {
	ID      string
	DiffIDs []string
	History []HistoryEntry
}

// ImageFacts is one image as the Changes tab compares it. It deliberately
// has no Raw document and no environment values: only their names.
type ImageFacts struct {
	ID           string
	RepoTags     []string
	RepoDigests  []string
	Size         int64
	Created      time.Time
	OS           string
	Architecture string
	Variant      string

	Cmd, Entrypoint  []string
	User, WorkingDir string
	ExposedPorts     []string // sorted, "80/tcp"
	// Healthcheck is the test command; nil when the image sets none.
	Healthcheck []string
	Labels      map[string]string
	// EnvNames are the names of the environment variables the image sets,
	// sorted; the values are dropped where the inspect answer is decoded.
	EnvNames []string

	DiffIDs []string
	History []HistoryEntry // oldest first
}

// Platform is the platform the image was inspected as.
func (f ImageFacts) Platform() Platform {
	return Platform{OS: f.OS, Architecture: f.Architecture, Variant: f.Variant}
}

// Distribution is what the daemon's registry said a tag names now: its
// digest, media type and, for an index, the platforms it publishes. It
// carries no build time — the daemon does not fetch the config.
type Distribution struct {
	Digest    string
	MediaType string
	Platforms []Platform
}

// HistoryEntry is one entry of an image's history. The daemon reports the
// size of the layer the entry made, and 0 for an entry that made none.
type HistoryEntry struct {
	CreatedBy string
	Size      int64
}

// ProcessList is one process snapshot of a container as the daemon reported
// it. Titles and rows are kept verbatim: their number and meaning depend on
// the daemon's platform, and a row may have more or fewer cells than Titles.
type ProcessList struct {
	Titles []string
	Rows   [][]string
}

// ChangeKind is what a container did to a path, numbered as the Engine API
// numbers it.
type ChangeKind int

const (
	ChangeModified ChangeKind = 0 // `C` in docker diff
	ChangeAdded    ChangeKind = 1 // `A`
	ChangeDeleted  ChangeKind = 2 // `D`
)

// Letter is the kind as docker diff prints it; "?" for a number the API
// does not define.
func (k ChangeKind) Letter() string {
	switch k {
	case ChangeModified:
		return "C"
	case ChangeAdded:
		return "A"
	case ChangeDeleted:
		return "D"
	}
	return "?"
}

func (k ChangeKind) String() string {
	switch k {
	case ChangeModified:
		return "changed"
	case ChangeAdded:
		return "added"
	case ChangeDeleted:
		return "deleted"
	}
	return "unknown"
}

// Change is one entry of a container's change list.
type Change struct {
	Path string
	Kind ChangeKind
}

// PathStat is the daemon's stat of a path in a container, as it sends it
// with an archive (the X-Docker-Container-Path-Stat header). Name is the
// base name, untrusted: escape it before showing it.
type PathStat struct {
	Name       string
	Size       int64
	Mode       fs.FileMode // Go's type bits: fs.ModeDir, fs.ModeSymlink, …
	LinkTarget string
}

// IsDir reports whether the path is a directory.
func (s PathStat) IsDir() bool { return s.Mode.IsDir() }

// Archive is a container path's tar stream and its stat. The caller reads
// Content and must close it.
type Archive struct {
	Content io.ReadCloser
	Stat    PathStat
}

// ContainerCreateOptions is a container to create, in the daemon's terms
// (docs/DECISIONS.md D24). The planning layer has its own specification
// type; the UI and the fake daemon never see the client library's.
type ContainerCreateOptions struct {
	Image   string
	Name    string // "" lets the daemon choose
	Ports   []PortBinding
	Env     []string // NAME=value
	Mounts  []MountOption
	Restart RestartPolicy
	Cmd     []string // nil keeps the image's command

	// The fields below are set only by a re-create (docs/DECISIONS.md,
	// round nineteen). Their zero values add nothing to the request, so the
	// create form's request is unchanged.

	// Entrypoint nil keeps the image's; []string{""} clears it.
	Entrypoint []string
	Labels     map[string]string
	Tty        bool
	// OpenStdin keeps stdin open (docker run -i), which a detached shell
	// container needs to stay up.
	OpenStdin  bool
	WorkingDir string
	User       string
	Hostname   string
	// Init nil leaves the daemon's default.
	Init        *bool
	Healthcheck *HealthConfig
	StopSignal  string
	// StopTimeout nil leaves the daemon's default grace period (seconds).
	StopTimeout *int
	// NetworkMode "" is the daemon's default network; otherwise "bridge",
	// "host", "none" or a user-defined network's name.
	NetworkMode string
	// Networks are the endpoints to attach at create, with their aliases.
	Networks []NetworkAttachment
	// ExposedOnly are ports exposed without being published, "80/tcp".
	ExposedOnly []string
	Resources   ResourceLimits
	// ShmSize is /dev/shm in bytes; 0 leaves the daemon's default.
	ShmSize int64
	// LogDriver and LogOptions, when LogDriver is set, are sent as the
	// container's log configuration. Options may hold credentials: they
	// are never displayed.
	LogDriver  string
	LogOptions map[string]string
}

// HealthConfig is a container's health check. Zero durations and retries
// inherit the image's or the daemon's.
type HealthConfig struct {
	// Test is {"CMD", args…}, {"CMD-SHELL", command} or {"NONE"}.
	Test          []string
	Interval      time.Duration
	Timeout       time.Duration
	StartPeriod   time.Duration
	StartInterval time.Duration
	Retries       int
}

// NetworkAttachment is one network a new container is attached to.
type NetworkAttachment struct {
	Network string // name
	Aliases []string
}

// ResourceLimits are the limits a re-create carries. Zero means unset.
type ResourceLimits struct {
	Memory            int64 // bytes
	MemorySwap        int64 // bytes, memory plus swap; -1 unlimited
	MemoryReservation int64 // bytes
	NanoCPUs          int64
	CPUShares         int64
	CPUQuota          int64
	CPUPeriod         int64
	CpusetCpus        string
	// PidsLimit nil leaves the daemon's default.
	PidsLimit *int64
}

// PortBinding publishes one container port.
type PortBinding struct {
	HostIP        string // "" means every address
	HostPort      uint16 // 0 lets the daemon choose
	ContainerPort uint16
	Proto         string // tcp or udp
}

// MountOption is one mount with an explicit type, so a named volume and a
// host path cannot be confused.
type MountOption struct {
	Type     string // volume or bind
	Source   string
	Target   string
	ReadOnly bool
}

// RestartPolicy is the daemon's restart policy.
type RestartPolicy struct {
	Name              string // no, always, unless-stopped, on-failure
	MaximumRetryCount int
}
