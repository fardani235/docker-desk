package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
)

// Re-creating a container on the build its tag names now (docs/DECISIONS.md,
// round nineteen): the configuration is derived from the container's own
// inspect document as a closed model — every field is either carried or
// refuses the action — and the plan stops the old container, renames it out
// of the way, creates the new one under the original name and starts it.
// Nothing is deleted.

// RecreateWord is what the operator types to confirm a re-create.
const RecreateWord = "recreate"

// previousNameBound is the longest derived name the desk will propose.
const previousNameBound = 128

// previousNameTries is how many "-2", "-3", … suffixes are tried before
// the plan is blocked.
const previousNameTries = 9

// CloneDefaults are the daemon's own defaults, which a container's inspect
// reports as if the container had asked for them. A setting equal to the
// daemon's default is not a setting of the container's.
type CloneDefaults struct {
	LoggingDriver string // "" means json-file
	Runtime       string // "" means runc
	CgroupVersion string // "1" or "2"; "" means 2
}

// DefaultsFrom reads the defaults from the daemon's /info answer.
func DefaultsFrom(i engine.Info) CloneDefaults {
	return CloneDefaults{LoggingDriver: i.LoggingDriver, Runtime: i.DefaultRuntime, CgroupVersion: i.CgroupVersion}
}

// CloneSpec is a container's own configuration, as a re-create carries it.
// It holds environment values because the daemon needs them; nothing that
// renders a CloneSpec may show them (Env is never displayed, only EnvNames).
type CloneSpec struct {
	Image string // the reference the container was created from
	Name  string

	// Entrypoint nil leaves the image's; []string{""} clears it.
	Entrypoint []string
	// Cmd nil leaves the image's.
	Cmd        []string
	Env        []string // NAME=value: never displayed
	WorkingDir string
	User       string
	Hostname   string
	Tty        bool
	OpenStdin  bool
	Init       *bool
	Labels     map[string]string

	Healthcheck *engine.HealthConfig
	StopSignal  string
	StopTimeout *int

	Ports       []PortSpec
	ExposedOnly []string // "80/tcp"
	Mounts      []MountSpec
	NetworkMode string // "" the daemon's default
	Networks    []engine.NetworkAttachment

	Restart   RestartSpec
	Resources engine.ResourceLimits
	ShmSize   int64

	// LogDriver and LogOptions are the daemon's default driver and the
	// options the container has for it. Options may hold credentials:
	// never displayed.
	LogDriver  string
	LogOptions map[string]string

	// FromImage names what the old image supplied and is therefore left to
	// the new image: environment names, "command", "labels (3)", ….
	FromImage []string
}

// EnvNames are the names of the carried environment entries, in order.
func (c CloneSpec) EnvNames() []string {
	out := make([]string, 0, len(c.Env))
	for _, e := range c.Env {
		n, _, _ := strings.Cut(e, "=")
		out = append(out, n)
	}
	return out
}

// envValues are the carried values worth hiding from a daemon's message.
func (c CloneSpec) envValues() []string {
	var out []string
	for _, e := range c.Env {
		if _, v, _ := strings.Cut(e, "="); len(v) >= minRedacted {
			out = append(out, v)
		}
	}
	// Longest first, so a value containing another is hidden whole.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// minRedacted is the shortest environment value hidden from a daemon's
// message: replacing every "1" or "on" would make the message unreadable,
// and a value that short is not a secret.
const minRedacted = 4

// CloneOptions maps a CloneSpec to the engine's create options.
func CloneOptions(c CloneSpec) engine.ContainerCreateOptions {
	o := engine.ContainerCreateOptions{
		Image:       c.Image,
		Name:        c.Name,
		Env:         append([]string(nil), c.Env...),
		Cmd:         append([]string(nil), c.Cmd...),
		Entrypoint:  append([]string(nil), c.Entrypoint...),
		Restart:     engine.RestartPolicy{Name: c.Restart.Name, MaximumRetryCount: c.Restart.MaximumRetryCount},
		Labels:      cloneLabels(c.Labels),
		Tty:         c.Tty,
		OpenStdin:   c.OpenStdin,
		WorkingDir:  c.WorkingDir,
		User:        c.User,
		Hostname:    c.Hostname,
		Init:        c.Init,
		StopSignal:  c.StopSignal,
		StopTimeout: c.StopTimeout,
		NetworkMode: c.NetworkMode,
		ExposedOnly: append([]string(nil), c.ExposedOnly...),
		Resources:   c.Resources,
		ShmSize:     c.ShmSize,
		LogDriver:   c.LogDriver,
		LogOptions:  cloneLabels(c.LogOptions),
	}
	if len(o.Env) == 0 {
		o.Env = nil
	}
	if len(o.Cmd) == 0 {
		o.Cmd = nil
	}
	if len(o.Entrypoint) == 0 {
		o.Entrypoint = nil
	}
	if len(o.ExposedOnly) == 0 {
		o.ExposedOnly = nil
	}
	if h := c.Healthcheck; h != nil {
		hc := *h
		hc.Test = append([]string(nil), h.Test...)
		o.Healthcheck = &hc
	}
	for _, p := range c.Ports {
		o.Ports = append(o.Ports, engine.PortBinding{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Proto: p.Proto})
	}
	for _, m := range c.Mounts {
		o.Mounts = append(o.Mounts, engine.MountOption{Type: m.Type, Source: m.Source, Target: m.Destination, ReadOnly: m.ReadOnly})
	}
	for _, a := range c.Networks {
		o.Networks = append(o.Networks, engine.NetworkAttachment{Network: a.Network, Aliases: append([]string(nil), a.Aliases...)})
	}
	return o
}

func cloneLabels(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- the inspect document, narrowly ---------------------------------------
//
// Only the fields the clone carries or refuses on are decoded. The client
// library's types are not used: the planning layer keeps its own
// vocabulary, and a narrow struct makes the reviewed set explicit.

type rawInspect struct {
	ID              string        `json:"Id"`
	Name            string        `json:"Name"`
	Config          *rawConfig    `json:"Config"`
	HostConfig      *rawHost      `json:"HostConfig"`
	Mounts          []rawMountPt  `json:"Mounts"`
	NetworkSettings *rawNetworkSt `json:"NetworkSettings"`
}

type rawConfig struct {
	Hostname        string                     `json:"Hostname"`
	Domainname      string                     `json:"Domainname"`
	User            string                     `json:"User"`
	Tty             bool                       `json:"Tty"`
	OpenStdin       bool                       `json:"OpenStdin"`
	StdinOnce       bool                       `json:"StdinOnce"`
	Env             []string                   `json:"Env"`
	Cmd             []string                   `json:"Cmd"`
	Entrypoint      []string                   `json:"Entrypoint"`
	Healthcheck     *rawHealth                 `json:"Healthcheck"`
	Image           string                     `json:"Image"`
	WorkingDir      string                     `json:"WorkingDir"`
	Labels          map[string]string          `json:"Labels"`
	StopSignal      string                     `json:"StopSignal"`
	StopTimeout     *int                       `json:"StopTimeout"`
	ExposedPorts    map[string]json.RawMessage `json:"ExposedPorts"`
	NetworkDisabled bool                       `json:"NetworkDisabled"`
}

type rawHealth struct {
	Test          []string `json:"Test"`
	Interval      int64    `json:"Interval"`
	Timeout       int64    `json:"Timeout"`
	StartPeriod   int64    `json:"StartPeriod"`
	StartInterval int64    `json:"StartInterval"`
	Retries       int      `json:"Retries"`
}

type rawBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type rawHost struct {
	LogConfig struct {
		Type   string            `json:"Type"`
		Config map[string]string `json:"Config"`
	} `json:"LogConfig"`
	NetworkMode   string                  `json:"NetworkMode"`
	PortBindings  map[string][]rawBinding `json:"PortBindings"`
	RestartPolicy struct {
		Name              string `json:"Name"`
		MaximumRetryCount int    `json:"MaximumRetryCount"`
	} `json:"RestartPolicy"`
	AutoRemove      bool              `json:"AutoRemove"`
	VolumeDriver    string            `json:"VolumeDriver"`
	VolumesFrom     []string          `json:"VolumesFrom"`
	Annotations     map[string]string `json:"Annotations"`
	CapAdd          []string          `json:"CapAdd"`
	CapDrop         []string          `json:"CapDrop"`
	CgroupnsMode    string            `json:"CgroupnsMode"`
	DNS             []string          `json:"Dns"`
	DNSOptions      []string          `json:"DnsOptions"`
	DNSSearch       []string          `json:"DnsSearch"`
	ExtraHosts      []string          `json:"ExtraHosts"`
	GroupAdd        []string          `json:"GroupAdd"`
	IpcMode         string            `json:"IpcMode"`
	Cgroup          string            `json:"Cgroup"`
	Links           []string          `json:"Links"`
	OomScoreAdj     int               `json:"OomScoreAdj"`
	PidMode         string            `json:"PidMode"`
	Privileged      bool              `json:"Privileged"`
	PublishAllPorts bool              `json:"PublishAllPorts"`
	ReadonlyRootfs  bool              `json:"ReadonlyRootfs"`
	SecurityOpt     []string          `json:"SecurityOpt"`
	StorageOpt      map[string]string `json:"StorageOpt"`
	Tmpfs           map[string]string `json:"Tmpfs"`
	UTSMode         string            `json:"UTSMode"`
	UsernsMode      string            `json:"UsernsMode"`
	ShmSize         int64             `json:"ShmSize"`
	Sysctls         map[string]string `json:"Sysctls"`
	Runtime         string            `json:"Runtime"`
	Umask           *uint32           `json:"Umask"`
	Isolation       string            `json:"Isolation"`
	Init            *bool             `json:"Init"`
	Mounts          []rawHostMount    `json:"Mounts"`

	// Resources, embedded in the daemon's HostConfig.
	CPUShares            int64             `json:"CpuShares"`
	Memory               int64             `json:"Memory"`
	NanoCPUs             int64             `json:"NanoCpus"`
	CgroupParent         string            `json:"CgroupParent"`
	BlkioWeight          uint16            `json:"BlkioWeight"`
	BlkioWeightDevice    []json.RawMessage `json:"BlkioWeightDevice"`
	BlkioDeviceReadBps   []json.RawMessage `json:"BlkioDeviceReadBps"`
	BlkioDeviceWriteBps  []json.RawMessage `json:"BlkioDeviceWriteBps"`
	BlkioDeviceReadIOps  []json.RawMessage `json:"BlkioDeviceReadIOps"`
	BlkioDeviceWriteIOps []json.RawMessage `json:"BlkioDeviceWriteIOps"`
	CPUPeriod            int64             `json:"CpuPeriod"`
	CPUQuota             int64             `json:"CpuQuota"`
	CPURealtimePeriod    int64             `json:"CpuRealtimePeriod"`
	CPURealtimeRuntime   int64             `json:"CpuRealtimeRuntime"`
	CpusetCpus           string            `json:"CpusetCpus"`
	CpusetMems           string            `json:"CpusetMems"`
	Devices              []struct {
		PathOnHost string `json:"PathOnHost"`
	} `json:"Devices"`
	DeviceCgroupRules []string `json:"DeviceCgroupRules"`
	DeviceRequests    []struct {
		Driver       string     `json:"Driver"`
		Capabilities [][]string `json:"Capabilities"`
	} `json:"DeviceRequests"`
	MemoryReservation int64  `json:"MemoryReservation"`
	MemorySwap        int64  `json:"MemorySwap"`
	MemorySwappiness  *int64 `json:"MemorySwappiness"`
	OomKillDisable    *bool  `json:"OomKillDisable"`
	PidsLimit         *int64 `json:"PidsLimit"`
	Ulimits           []struct {
		Name string `json:"Name"`
	} `json:"Ulimits"`
	CPUCount           int64  `json:"CpuCount"`
	CPUPercent         int64  `json:"CpuPercent"`
	IOMaximumIOps      uint64 `json:"IOMaximumIOps"`
	IOMaximumBandwidth uint64 `json:"IOMaximumBandwidth"`
}

type rawHostMount struct {
	Type        string `json:"Type"`
	Target      string `json:"Target"`
	BindOptions *struct {
		Propagation            string `json:"Propagation"`
		NonRecursive           bool   `json:"NonRecursive"`
		CreateMountpoint       bool   `json:"CreateMountpoint"`
		ReadOnlyNonRecursive   bool   `json:"ReadOnlyNonRecursive"`
		ReadOnlyForceRecursive bool   `json:"ReadOnlyForceRecursive"`
	} `json:"BindOptions"`
	VolumeOptions *struct {
		NoCopy       bool            `json:"NoCopy"`
		Subpath      string          `json:"Subpath"`
		DriverConfig json.RawMessage `json:"DriverConfig"`
	} `json:"VolumeOptions"`
	ImageOptions   json.RawMessage `json:"ImageOptions"`
	TmpfsOptions   json.RawMessage `json:"TmpfsOptions"`
	ClusterOptions json.RawMessage `json:"ClusterOptions"`
}

type rawMountPt struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

type rawNetworkSt struct {
	Networks map[string]*rawEndpoint `json:"Networks"`
}

type rawEndpoint struct {
	IPAMConfig *struct {
		IPv4Address  string   `json:"IPv4Address"`
		IPv6Address  string   `json:"IPv6Address"`
		LinkLocalIPs []string `json:"LinkLocalIPs"`
	} `json:"IPAMConfig"`
	Links      []string          `json:"Links"`
	Aliases    []string          `json:"Aliases"`
	DriverOpts map[string]string `json:"DriverOpts"`
	GwPriority int               `json:"GwPriority"`
}

// decodeDoc reads an inspect document. The decoder's own message is not
// passed on: a syntax error quotes the bytes around it, which may be part
// of an environment value.
func decodeDoc(what string, raw []byte, v any) error {
	if len(raw) == 0 {
		return fmt.Errorf("the %s's inspect document is missing", what)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return fmt.Errorf("the %s's inspect document could not be read: field %s has an unexpected type", what, escape.Name(te.Field))
		}
		return fmt.Errorf("the %s's inspect document could not be read", what)
	}
	return nil
}

// ---- refusal families --------------------------------------------------------

// The families a re-create refuses, in the order the dialog lists them.
const (
	FamPrivilege    = "Privilege"
	FamCapabilities = "Capabilities"
	FamDevices      = "Devices"
	FamSecurity     = "Security options"
	FamRuntime      = "Runtime"
	FamKernel       = "Kernel settings"
	FamLimits       = "Resource limits"
	FamTmpfs        = "Tmpfs"
	FamHost         = "Host overrides"
	FamLogging      = "Logging"
	FamRootfs       = "Root filesystem"
	FamNamespaces   = "Namespaces"
	FamNetwork      = "Network identity"
	FamPorts        = "Ports"
	FamProcess      = "Process"
	FamStorage      = "Storage"
)

var familyOrder = []string{
	FamPrivilege, FamCapabilities, FamDevices, FamSecurity, FamRuntime, FamKernel, FamLimits,
	FamTmpfs, FamHost, FamLogging, FamRootfs, FamNamespaces, FamNetwork, FamPorts, FamProcess, FamStorage,
}

type refusals map[string][]string

func (r refusals) add(family, format string, args ...any) {
	r[family] = append(r[family], fmt.Sprintf(format, args...))
}

// lines renders one line per family, "Family: detail; detail".
func (r refusals) lines() []string {
	var out []string
	for _, f := range familyOrder {
		if d := r[f]; len(d) > 0 {
			out = append(out, f+": "+strings.Join(d, "; "))
		}
	}
	return out
}

// shown is a daemon-supplied string as the dialog may draw it: escaped and
// bounded. It is never applied to an environment value.
func shown(s string) string {
	s = escape.Name(s)
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "…"
	}
	return s
}

func shownList(items []string) string {
	const most = 6
	out := make([]string, 0, min(len(items), most)+1)
	for i, s := range items {
		if i == most {
			out = append(out, fmt.Sprintf("and %d more", len(items)-most))
			break
		}
		out = append(out, shown(s))
	}
	return strings.Join(out, ", ")
}

// optionKeys names security options by their key only ("seccomp", not the
// profile it was given).
func optionKeys(opts []string) []string {
	var out []string
	for _, o := range opts {
		k := o
		if i := strings.IndexAny(o, "=:"); i >= 0 {
			k = o[:i]
		}
		out = append(out, k)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- the derivation ------------------------------------------------------------

// Clone derives the configuration a re-create carries from the container's
// inspect document and the inspect document of the image it runs now.
//
// What the old image supplied — its environment entries, command,
// entrypoint, labels, working directory, user, stop signal, health check and
// exposed ports, wherever the container's value equals the image's — is left
// out, so the new image supplies its own. What the container set itself is
// carried. A setting outside the carried set is returned in the refusal
// list, one line per family; the list never contains an environment value.
// The error is for a document that cannot be read at all.
func Clone(detail engine.ContainerDetail, base engine.ImageDetail, def CloneDefaults) (CloneSpec, []string, error) {
	var doc rawInspect
	if err := decodeDoc("container", detail.Raw, &doc); err != nil {
		return CloneSpec{}, nil, err
	}
	var img struct {
		Config *rawConfig `json:"Config"`
	}
	if err := decodeDoc("running image", base.Raw, &img); err != nil {
		return CloneSpec{}, nil, err
	}
	if doc.Config == nil || doc.HostConfig == nil {
		return CloneSpec{}, nil, errors.New("the container's inspect document has no configuration")
	}
	ic := img.Config
	if ic == nil {
		ic = &rawConfig{}
	}
	cfg, host := doc.Config, doc.HostConfig
	ref := refusals{}
	spec := CloneSpec{
		Image: cfg.Image,
		Name:  strings.TrimPrefix(doc.Name, "/"),
		Tty:   cfg.Tty, OpenStdin: cfg.OpenStdin,
		Init:        host.Init,
		StopTimeout: cfg.StopTimeout,
	}
	shortID := trimSHA(doc.ID)
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	cloneProcess(&spec, cfg, ic, shortID, host.NetworkMode, ref)
	cloneEnvLabels(&spec, cfg, ic)
	clonePorts(&spec, cfg, host, ic, ref)
	cloneStorage(&spec, doc.Mounts, host, ref)
	cloneNetwork(&spec, host.NetworkMode, doc.NetworkSettings, shortID, cfg, ref)
	cloneLimits(&spec, host, ref)
	refuseHost(host, def, ref)

	// The container's options for the daemon's default log driver cannot be
	// told apart from the daemon's own default options, which it merges into
	// every container's configuration: all are carried, and never shown.
	logDriver := def.LoggingDriver
	if logDriver == "" {
		logDriver = "json-file"
	}
	if host.LogConfig.Type == logDriver && len(host.LogConfig.Config) > 0 {
		spec.LogDriver, spec.LogOptions = logDriver, cloneLabels(host.LogConfig.Config)
	}

	spec.Restart = RestartSpec{Name: host.RestartPolicy.Name, MaximumRetryCount: host.RestartPolicy.MaximumRetryCount}
	if spec.Restart.Name == "" {
		spec.Restart.Name = "no"
	}
	return spec, ref.lines(), nil
}

// CloneRefusals is Clone's refusal list alone. It does not depend on the
// image, so the Changes tab can decide whether to offer a re-create from
// the inspect it already has.
func CloneRefusals(detail engine.ContainerDetail, def CloneDefaults) ([]string, error) {
	_, refused, err := Clone(detail, engine.ImageDetail{Raw: []byte(`{}`)}, def)
	return refused, err
}

// cloneProcess: entrypoint, command, working directory, user, hostname,
// health check and stop signal, each left to the image where it is the
// image's.
func cloneProcess(spec *CloneSpec, cfg, ic *rawConfig, shortID, netMode string, ref refusals) {
	ep, cmd := cfg.Entrypoint, cfg.Cmd
	switch {
	case len(ep) == 0 && len(ic.Entrypoint) > 0:
		// The container cleared the image's entrypoint.
		spec.Entrypoint, spec.Cmd = []string{""}, nonEmpty(cmd)
	case equalArgs(ep, ic.Entrypoint):
		if len(cmd) == 0 && len(ic.Cmd) > 0 {
			// The daemon inherits the command only when the entrypoint was
			// not given: an empty command means the entrypoint was.
			spec.Entrypoint = nonEmpty(ep)
		} else if equalArgs(cmd, ic.Cmd) {
			spec.FromImage = append(spec.FromImage, "command")
		} else {
			spec.Cmd = nonEmpty(cmd)
		}
		if len(ic.Entrypoint) > 0 && spec.Entrypoint == nil {
			spec.FromImage = append(spec.FromImage, "entrypoint")
		}
	default:
		spec.Entrypoint, spec.Cmd = nonEmpty(ep), nonEmpty(cmd)
	}
	pick := func(own, image, what string) string {
		if own == image {
			if image != "" {
				spec.FromImage = append(spec.FromImage, what)
			}
			return ""
		}
		return own
	}
	spec.WorkingDir = pick(cfg.WorkingDir, ic.WorkingDir, "working directory")
	spec.User = pick(cfg.User, ic.User, "user")
	spec.StopSignal = pick(cfg.StopSignal, ic.StopSignal, "stop signal")
	// The daemon names a container after its short ID unless told
	// otherwise, and in the host's network the host's name is used.
	if cfg.Hostname != shortID && netMode != "host" {
		spec.Hostname = cfg.Hostname
	}
	if cfg.Domainname != "" {
		ref.add(FamHost, "domain name %s", shown(cfg.Domainname))
	}
	if cfg.StdinOnce {
		ref.add(FamProcess, "stdin closes after the first attach (StdinOnce)")
	}
	spec.Healthcheck = cloneHealth(cfg.Healthcheck, ic.Healthcheck)
	if ic.Healthcheck != nil && len(ic.Healthcheck.Test) > 0 && (spec.Healthcheck == nil || spec.Healthcheck.Test == nil) {
		spec.FromImage = append(spec.FromImage, "health check")
	}
}

// cloneHealth keeps each part of the health check the container set; a
// part equal to the image's is left zero, which the daemon reads as "the
// image's".
func cloneHealth(own, image *rawHealth) *engine.HealthConfig {
	if own == nil {
		return nil
	}
	if image == nil {
		image = &rawHealth{}
	}
	h := engine.HealthConfig{}
	if !equalArgs(own.Test, image.Test) {
		h.Test = nonEmpty(own.Test)
	}
	pickDur := func(o, i int64) time.Duration {
		if o == i {
			return 0
		}
		return time.Duration(o)
	}
	h.Interval = pickDur(own.Interval, image.Interval)
	h.Timeout = pickDur(own.Timeout, image.Timeout)
	h.StartPeriod = pickDur(own.StartPeriod, image.StartPeriod)
	h.StartInterval = pickDur(own.StartInterval, image.StartInterval)
	if own.Retries != image.Retries {
		h.Retries = own.Retries
	}
	if reflect.DeepEqual(h, engine.HealthConfig{}) {
		return nil
	}
	return &h
}

// cloneEnvLabels keeps every environment entry and label the image did not
// supply with the same value.
func cloneEnvLabels(spec *CloneSpec, cfg, ic *rawConfig) {
	fromImage := map[string]bool{}
	for _, e := range ic.Env {
		fromImage[e] = true
	}
	var inherited []string
	for _, e := range cfg.Env {
		if fromImage[e] {
			n, _, _ := strings.Cut(e, "=")
			inherited = append(inherited, n)
			continue
		}
		spec.Env = append(spec.Env, e)
	}
	if len(inherited) > 0 {
		spec.FromImage = append(spec.FromImage, plural(len(inherited), "environment variable", "environment variables")+" ("+shownList(inherited)+")")
	}
	dropped := 0
	for k, v := range cfg.Labels {
		if iv, ok := ic.Labels[k]; ok && iv == v {
			dropped++
			continue
		}
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels[k] = v
	}
	if dropped > 0 {
		spec.FromImage = append(spec.FromImage, plural(dropped, "label", "labels"))
	}
}

// clonePorts carries what was asked for at create (HostConfig), not what the
// daemon happens to have bound now, and the ports exposed without a binding
// that the image does not expose itself.
func clonePorts(spec *CloneSpec, cfg *rawConfig, host *rawHost, ic *rawConfig, ref refusals) {
	if host.PublishAllPorts {
		ref.add(FamPorts, "every exposed port is published to a random host port (-P)")
	}
	keys := make([]string, 0, len(host.PortBindings))
	for k := range host.PortBindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		num, proto, ok := splitPortKey(k)
		if !ok {
			ref.add(FamPorts, "port %s", shown(k))
			continue
		}
		for _, b := range host.PortBindings[k] {
			p := PortSpec{ContainerPort: num, Proto: proto}
			if b.HostIP != "" {
				a, err := netip.ParseAddr(b.HostIP)
				if err != nil {
					ref.add(FamPorts, "host address %s for %s", shown(b.HostIP), shown(k))
					continue
				}
				p.HostIP = a.String()
			}
			if b.HostPort != "" {
				n, err := strconv.Atoi(b.HostPort)
				if err != nil || n < 1 || n > 65535 {
					ref.add(FamPorts, "host port range %s for %s", shown(b.HostPort), shown(k))
					continue
				}
				p.HostPort = uint16(n)
			}
			spec.Ports = append(spec.Ports, p)
		}
	}
	var exposed []string
	for k := range cfg.ExposedPorts {
		if _, bound := host.PortBindings[k]; bound {
			continue
		}
		if _, image := ic.ExposedPorts[k]; image {
			continue
		}
		if _, _, ok := splitPortKey(k); !ok {
			ref.add(FamPorts, "exposed port %s", shown(k))
			continue
		}
		exposed = append(exposed, k)
	}
	sort.Strings(exposed)
	spec.ExposedOnly = exposed
}

func splitPortKey(k string) (uint16, string, bool) {
	num, proto, ok := strings.Cut(k, "/")
	if !ok {
		proto = "tcp"
	}
	switch proto {
	case "tcp", "udp", "sctp":
	default:
		return 0, "", false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > 65535 {
		return 0, "", false
	}
	return uint16(n), proto, true
}

// cloneStorage carries bind and volume mounts as the daemon reports them in
// effect — an anonymous volume by the name the daemon gave it — and refuses
// every other type and every option the engine's mount does not reproduce.
func cloneStorage(spec *CloneSpec, mounts []rawMountPt, host *rawHost, ref refusals) {
	for _, m := range mounts {
		switch m.Type {
		case "bind", "volume":
		default:
			ref.add(FamStorage, "a %s mount at %s", shown(m.Type), shown(m.Destination))
			continue
		}
		if !strings.HasPrefix(m.Destination, "/") {
			ref.add(FamStorage, "mount destination %s", shown(m.Destination))
			continue
		}
		if bad := mountModeExtras(m.Type, m.Mode); len(bad) > 0 {
			ref.add(FamStorage, "mount option %s at %s", shownList(bad), shown(m.Destination))
			continue
		}
		if m.Propagation != "" && m.Propagation != "rprivate" {
			ref.add(FamStorage, "%s propagation at %s", shown(m.Propagation), shown(m.Destination))
			continue
		}
		ms := MountSpec{Type: m.Type, Destination: m.Destination, ReadOnly: !m.RW}
		if m.Type == "volume" {
			ms.Source = m.Name
		} else {
			ms.Source = m.Source
		}
		if ms.Source == "" {
			ref.add(FamStorage, "a %s mount at %s without a source", m.Type, shown(m.Destination))
			continue
		}
		spec.Mounts = append(spec.Mounts, ms)
	}
	for _, m := range host.Mounts {
		var opts []string
		if b := m.BindOptions; b != nil {
			if b.Propagation != "" && b.Propagation != "rprivate" {
				opts = append(opts, "propagation "+b.Propagation)
			}
			if b.NonRecursive {
				opts = append(opts, "non-recursive")
			}
			if b.CreateMountpoint {
				opts = append(opts, "create mountpoint")
			}
			if b.ReadOnlyNonRecursive || b.ReadOnlyForceRecursive {
				opts = append(opts, "read-only recursion")
			}
		}
		if v := m.VolumeOptions; v != nil {
			if v.NoCopy {
				opts = append(opts, "no copy")
			}
			if v.Subpath != "" {
				opts = append(opts, "subpath")
			}
			if len(v.DriverConfig) > 0 && string(v.DriverConfig) != "null" {
				opts = append(opts, "volume driver options")
			}
		}
		if len(opts) > 0 {
			ref.add(FamStorage, "mount option %s at %s", shownList(opts), shown(m.Target))
		}
	}
	if len(host.VolumesFrom) > 0 {
		ref.add(FamStorage, "volumes from %s", shownList(host.VolumesFrom))
	}
	if host.VolumeDriver != "" {
		ref.add(FamStorage, "volume driver %s", shown(host.VolumeDriver))
	}
}

// mountModeExtras returns the parts of a mount's mode other than read-only,
// read-write and the default propagation, for a mount of this type. A volume
// the daemon mounted with no explicit mode reports z: that is the daemon's
// own default, which it applies again to the new container, so it is not an
// extra. A private label (Z) on any mount, and any label on a bind, stay
// extras: a private label is tied to the container it was made for.
func mountModeExtras(typ, mode string) []string {
	var out []string
	for _, t := range strings.Split(mode, ",") {
		switch t {
		case "", "ro", "rw", "rprivate":
		case "z":
			if typ != "volume" {
				out = append(out, t)
			}
		default:
			out = append(out, t)
		}
	}
	return out
}

// cloneNetwork carries the network mode and every attachment with its
// aliases, and refuses a borrowed namespace and fixed addresses.
func cloneNetwork(spec *CloneSpec, mode string, ns *rawNetworkSt, shortID string, cfg *rawConfig, ref refusals) {
	switch {
	case mode == "" || mode == "default":
	case strings.HasPrefix(mode, "container:"):
		ref.add(FamNamespaces, "network namespace of another container (%s)", shown(mode))
		return
	case strings.Contains(mode, ":"):
		ref.add(FamNetwork, "network mode %s", shown(mode))
		return
	default:
		spec.NetworkMode = mode
	}
	if cfg.NetworkDisabled {
		ref.add(FamNetwork, "networking disabled")
	}
	if ns == nil {
		return
	}
	names := make([]string, 0, len(ns.Networks))
	for n := range ns.Networks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if (mode == "host" || mode == "none") && n == mode {
			continue
		}
		ep := ns.Networks[n]
		a := engine.NetworkAttachment{Network: n}
		if ep != nil {
			if c := ep.IPAMConfig; c != nil && (c.IPv4Address != "" || c.IPv6Address != "" || len(c.LinkLocalIPs) > 0) {
				ref.add(FamNetwork, "a fixed address on %s", shown(n))
			}
			if len(ep.Links) > 0 {
				ref.add(FamNetwork, "links on %s", shown(n))
			}
			if len(ep.DriverOpts) > 0 {
				ref.add(FamNetwork, "driver options on %s", shown(n))
			}
			if ep.GwPriority != 0 {
				ref.add(FamNetwork, "gateway priority on %s", shown(n))
			}
			for _, al := range ep.Aliases {
				// Older daemons list the short ID the daemon added itself.
				if al != shortID {
					a.Aliases = append(a.Aliases, al)
				}
			}
		}
		spec.Networks = append(spec.Networks, a)
	}
}

// cloneLimits carries the common limits and refuses the rest.
func cloneLimits(spec *CloneSpec, h *rawHost, ref refusals) {
	spec.Resources = engine.ResourceLimits{
		Memory: h.Memory, MemorySwap: h.MemorySwap, MemoryReservation: h.MemoryReservation,
		NanoCPUs: h.NanoCPUs, CPUShares: h.CPUShares, CPUQuota: h.CPUQuota, CPUPeriod: h.CPUPeriod,
		CpusetCpus: h.CpusetCpus,
	}
	if h.PidsLimit != nil && *h.PidsLimit != 0 {
		v := *h.PidsLimit
		spec.Resources.PidsLimit = &v
	}
	spec.ShmSize = h.ShmSize
	if h.BlkioWeight != 0 || len(h.BlkioWeightDevice)+len(h.BlkioDeviceReadBps)+len(h.BlkioDeviceWriteBps)+len(h.BlkioDeviceReadIOps)+len(h.BlkioDeviceWriteIOps) > 0 {
		ref.add(FamLimits, "block I/O limits")
	}
	if h.CPURealtimePeriod != 0 || h.CPURealtimeRuntime != 0 {
		ref.add(FamLimits, "real-time CPU limits")
	}
	if h.CpusetMems != "" {
		ref.add(FamLimits, "memory nodes %s", shown(h.CpusetMems))
	}
	if h.CPUCount != 0 || h.CPUPercent != 0 || h.IOMaximumIOps != 0 || h.IOMaximumBandwidth != 0 {
		ref.add(FamLimits, "Windows CPU or I/O limits")
	}
}

// refuseHost names every HostConfig setting outside the carried set.
func refuseHost(h *rawHost, def CloneDefaults, ref refusals) {
	if h.Privileged {
		ref.add(FamPrivilege, "runs privileged")
	}
	if len(h.CapAdd) > 0 {
		ref.add(FamCapabilities, "adds %s", shownList(h.CapAdd))
	}
	if len(h.CapDrop) > 0 {
		ref.add(FamCapabilities, "drops %s", shownList(h.CapDrop))
	}
	if len(h.Devices) > 0 {
		var paths []string
		for _, d := range h.Devices {
			paths = append(paths, d.PathOnHost)
		}
		ref.add(FamDevices, "%s", shownList(paths))
	}
	if len(h.DeviceRequests) > 0 {
		var what []string
		for _, d := range h.DeviceRequests {
			w := d.Driver
			if w == "" && len(d.Capabilities) > 0 {
				w = strings.Join(d.Capabilities[0], "+")
			}
			if w == "" {
				w = "device"
			}
			what = append(what, w)
		}
		ref.add(FamDevices, "device requests (%s)", shownList(what))
	}
	if len(h.DeviceCgroupRules) > 0 {
		ref.add(FamDevices, "device cgroup rules")
	}
	if len(h.SecurityOpt) > 0 {
		ref.add(FamSecurity, "%s", shownList(optionKeys(h.SecurityOpt)))
	}
	rt := def.Runtime
	if rt == "" {
		rt = "runc"
	}
	if h.Runtime != "" && h.Runtime != rt {
		ref.add(FamRuntime, "runtime %s", shown(h.Runtime))
	}
	if len(h.Annotations) > 0 {
		ref.add(FamRuntime, "annotations %s", shownList(sortedKeys(h.Annotations)))
	}
	if h.Isolation != "" && h.Isolation != "default" {
		ref.add(FamRuntime, "isolation %s", shown(h.Isolation))
	}
	if len(h.StorageOpt) > 0 {
		ref.add(FamRuntime, "storage driver options")
	}
	if len(h.Sysctls) > 0 {
		ref.add(FamKernel, "sysctls %s", shownList(sortedKeys(h.Sysctls)))
	}
	if len(h.Ulimits) > 0 {
		var names []string
		for _, u := range h.Ulimits {
			names = append(names, u.Name)
		}
		ref.add(FamKernel, "ulimits %s", shownList(names))
	}
	if h.OomKillDisable != nil && *h.OomKillDisable {
		ref.add(FamKernel, "OOM killer disabled")
	}
	if h.MemorySwappiness != nil && *h.MemorySwappiness != -1 {
		ref.add(FamKernel, "memory swappiness %d", *h.MemorySwappiness)
	}
	if h.OomScoreAdj != 0 {
		ref.add(FamKernel, "OOM score adjustment %d", h.OomScoreAdj)
	}
	if len(h.Tmpfs) > 0 {
		ref.add(FamTmpfs, "%s", shownList(sortedKeys(h.Tmpfs)))
	}
	if len(h.ExtraHosts) > 0 {
		ref.add(FamHost, "%s", plural(len(h.ExtraHosts), "extra host entry", "extra host entries"))
	}
	if len(h.DNS) > 0 {
		ref.add(FamHost, "DNS servers %s", shownList(h.DNS))
	}
	if len(h.DNSSearch) > 0 {
		ref.add(FamHost, "DNS search domains")
	}
	if len(h.DNSOptions) > 0 {
		ref.add(FamHost, "DNS options")
	}
	if len(h.GroupAdd) > 0 {
		ref.add(FamHost, "additional groups %s", shownList(h.GroupAdd))
	}
	if len(h.Links) > 0 {
		ref.add(FamHost, "%s", plural(len(h.Links), "legacy link", "legacy links"))
	}
	logDriver := def.LoggingDriver
	if logDriver == "" {
		logDriver = "json-file"
	}
	if h.LogConfig.Type != "" && h.LogConfig.Type != logDriver {
		// The driver only: its options may hold credentials.
		ref.add(FamLogging, "log driver %s", shown(h.LogConfig.Type))
	}
	if h.ReadonlyRootfs {
		ref.add(FamRootfs, "read-only root filesystem")
	}
	if h.AutoRemove {
		ref.add(FamRootfs, "removed automatically when it exits")
	}
	if h.PidMode != "" {
		ref.add(FamNamespaces, "PID namespace %s", shown(h.PidMode))
	}
	switch h.IpcMode {
	case "", "private", "shareable":
	default:
		ref.add(FamNamespaces, "IPC namespace %s", shown(h.IpcMode))
	}
	if h.UTSMode != "" {
		ref.add(FamNamespaces, "UTS namespace %s", shown(h.UTSMode))
	}
	if h.UsernsMode != "" {
		ref.add(FamNamespaces, "user namespace %s", shown(h.UsernsMode))
	}
	switch {
	case h.CgroupnsMode == "" || h.CgroupnsMode == "private":
	case h.CgroupnsMode == "host" && def.CgroupVersion == "1":
		// The default on cgroup v1.
	default:
		ref.add(FamNamespaces, "cgroup namespace %s", shown(h.CgroupnsMode))
	}
	if h.CgroupParent != "" {
		ref.add(FamNamespaces, "cgroup parent %s", shown(h.CgroupParent))
	}
	if h.Cgroup != "" {
		ref.add(FamNamespaces, "cgroup %s", shown(h.Cgroup))
	}
	if h.Umask != nil {
		ref.add(FamProcess, "umask %04o", *h.Umask)
	}
}

func nonEmpty(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	return append([]string(nil), a...)
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- the previous name ------------------------------------------------------------

// PreviousName is the name the old container is given: its own name,
// "-previous-" and the first twelve hex digits of its ID, the name shortened
// to keep the whole within 128 characters, and "-2", "-3", … appended while
// another container in the snapshot has that name.
func PreviousName(name, id string, snap Snapshot) (string, error) {
	name = strings.TrimPrefix(name, "/")
	short := trimSHA(id)
	if len(short) > 12 {
		short = short[:12]
	}
	if short == "" {
		return "", errors.New("the container has no ID")
	}
	taken := map[string]bool{}
	for _, c := range snap.Containers {
		taken[strings.TrimPrefix(c.Name, "/")] = true
	}
	for n := 1; n <= previousNameTries; n++ {
		tail := "-previous-" + short
		if n > 1 {
			tail += "-" + strconv.Itoa(n)
		}
		base := name
		if len(base)+len(tail) > previousNameBound {
			base = base[:previousNameBound-len(tail)]
		}
		cand := base + tail
		if err := validName("container", cand, 2); err != nil {
			return "", fmt.Errorf("the old container cannot be given a name: %v", err)
		}
		if !taken[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no free name for the old container: %s-previous-%s and %d variants are all taken", shown(name), short, previousNameTries-1)
}

// SetAsideName reports whether name is one PreviousName gave the container
// with this ID, and if so the name it had before. Such a container was put
// aside by a re-create: its old name belongs to its replacement.
func SetAsideName(name, id string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	short := trimSHA(id)
	if len(short) > 12 {
		short = short[:12]
	}
	if short == "" {
		return "", false
	}
	marker := "-previous-" + short
	i := strings.LastIndex(name, marker)
	if i <= 0 {
		return "", false
	}
	rest := name[i+len(marker):]
	if rest != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(rest, "-"))
		if !strings.HasPrefix(rest, "-") || err != nil || n < 2 {
			return "", false
		}
	}
	return name[:i], true
}

// ---- the plan ------------------------------------------------------------------------

// ImagePin is the reference a re-create uses and the image ID it named when
// the dialog was built. The plan refuses to start if it names another.
type ImagePin struct {
	Ref string
	ID  string
}

// ContainerRecreate plans replacing the container with one created from the
// build its reference names now: a stop when it is active, a rename of the
// old container to PreviousName, the create under the original name, and
// the start of the ID the create returns — each depending on the one before
// it. Nothing is removed. refused is Clone's list; each line, and every
// problem found here, becomes a blocker, so the dialog opens and explains.
func ContainerRecreate(snap Snapshot, detail engine.ContainerDetail, spec CloneSpec, refused []string, pin ImagePin) (Plan, error) {
	c, ok := snap.container(detail.ID)
	if !ok {
		return Plan{}, na("that container no longer exists")
	}
	name := strings.TrimPrefix(detail.Name, "/")
	if strings.TrimPrefix(c.Name, "/") != name {
		return Plan{}, na("%s was renamed while the plan was read: open the tab again", escape.Name(name))
	}
	if pin.Ref == "" || pin.ID == "" {
		return Plan{}, na("there is no build to re-create %s onto", escape.Name(name))
	}
	if spec.Image != pin.Ref {
		return Plan{}, na("%s was created from %s, not %s", escape.Name(name), shown(spec.Image), shown(pin.Ref))
	}
	spec.Name = name
	p := Plan{
		Title:     "Re-create container",
		JobTitle:  "re-create container",
		JobTarget: name,
		Target:    Target{Kind: "container", Name: name, ID: engine.ShortID(c.ID)},
		Tier:      TierTyped,
		Word:      RecreateWord,
		PinRef:    pin.Ref,
		PinID:     pin.ID,
	}
	p.Blockers = append(p.Blockers, refused...)

	if orig, aside := SetAsideName(name, c.ID); aside {
		p.Blockers = append(p.Blockers, fmt.Sprintf("Name: this container was set aside by a re-create; its name was %s.", escape.Name(orig)))
	}
	for _, o := range snap.Containers {
		if o.ID != c.ID && strings.TrimPrefix(o.Name, "/") == name {
			p.Blockers = append(p.Blockers, fmt.Sprintf("Name: %s is held by another container (%s).", escape.Name(name), engine.ShortID(o.ID)))
		}
	}
	netNames := map[string]bool{}
	for _, n := range snap.Networks {
		netNames[n.Name] = true
	}
	var missing []string
	for _, a := range spec.Networks {
		if !netNames[a.Network] {
			missing = append(missing, a.Network)
		}
	}
	switch spec.NetworkMode {
	case "", "bridge", "host", "none":
	default:
		if !netNames[spec.NetworkMode] && !containsStr(missing, spec.NetworkMode) {
			missing = append(missing, spec.NetworkMode)
		}
	}
	if len(missing) > 0 {
		p.Blockers = append(p.Blockers, "Networks: "+shownList(missing)+" no longer exist on this daemon.")
	}
	prev, err := PreviousName(name, c.ID, snap)
	if err != nil {
		p.Blockers = append(p.Blockers, "Name: "+err.Error())
		prev = name + "-previous-" + engine.ShortID(c.ID)
	}

	shownName, shownPrev, shownRef := escape.Name(name), escape.Name(prev), shown(pin.Ref)
	active := c.IsActive()
	at := 0
	if active {
		p.Steps = append(p.Steps, Step{Op: OpStop, Kind: "container", ID: c.ID, Name: name, Detail: c.State, IDFrom: -1,
			FailNote: shownName + " is left as it was; nothing was renamed or created."})
		at = 1
	}
	keeps := shownName + " keeps its name; nothing was created."
	if active {
		keeps = shownName + " is stopped and keeps its name; nothing was created."
	}
	rename := Step{Op: OpRename, Kind: "container", ID: c.ID, Name: name, Rename: prev, IDFrom: -1, FailNote: keeps}
	if active {
		rename.DependsOn = []int{0}
	}
	cl := spec
	create := Step{Op: OpCreateContainer, Kind: "container", Name: name, Detail: pin.Ref, Clone: &cl, Pin: pin.ID,
		ProducesID: true, IDFrom: -1, DependsOn: []int{at},
		FailNote: fmt.Sprintf("The old container stays stopped as %s; nothing was started. `docker rename %s %s` gives it its name back.", shownPrev, shownPrev, shownName)}
	start := Step{Op: OpStart, Kind: "container", Name: name, DependsOn: []int{at + 1}, IDFrom: at + 1,
		FailNote: fmt.Sprintf("The old container stays stopped as %s.", shownPrev)}
	p.Steps = append(p.Steps, rename, create, start)

	oldShort, newShort := engine.ShortID(detail.ImageID), engine.ShortID(pin.ID)
	if active {
		p.Effects = append(p.Effects, shownName+" is stopped: its stop signal, then killed after its grace period.")
	}
	p.Effects = append(p.Effects,
		fmt.Sprintf("%s is renamed %s and kept, stopped, with its data and configuration.", shownName, shownPrev),
		fmt.Sprintf("A new %s is created from %s — the build %s, replacing %s — and started.", shownName, shownRef, newShort, oldShort),
		"Carried: "+carriedSummary(spec)+".",
	)
	if len(spec.FromImage) > 0 {
		p.Effects = append(p.Effects, "From the new image instead (the old image supplied them): "+strings.Join(spec.FromImage, ", ")+".")
	}
	p.Effects = append(p.Effects, "Nothing is deleted; the old container stays stopped under "+shownPrev+".")

	var binds []string
	for _, m := range spec.Mounts {
		if m.Type == "bind" {
			binds = append(binds, m.Source)
		}
	}
	if len(binds) > 0 {
		p.Warnings = append(p.Warnings, "Bind mounts are mounted again from the same host paths, which are not checked: "+shownList(binds)+".")
	}
	if active {
		p.Warnings = append(p.Warnings, "Any shell or attached session in "+shownName+" ends when it stops.")
	}
	if proj := spec.Labels[engine.LabelComposeProject]; proj != "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s belongs to compose project %s: the old container keeps its compose labels, so compose tools may list %s under the same service until it is removed. The compose file is not read.", shownName, shown(proj), shownPrev))
	}
	p.SuccessNote = fmt.Sprintf("%s now runs the build %s names (%s); the old container is %s, stopped. Nothing was deleted.", shownName, shownRef, newShort, shownPrev)
	return p, nil
}

// carriedSummary names what is carried, environment by name only.
func carriedSummary(c CloneSpec) string {
	var parts []string
	if n := len(c.Env); n > 0 {
		parts = append(parts, plural(n, "environment variable", "environment variables")+" ("+shownList(c.EnvNames())+")")
	} else {
		parts = append(parts, "no environment variables of its own")
	}
	if c.Entrypoint != nil {
		parts = append(parts, "entrypoint")
	}
	if c.Cmd != nil {
		parts = append(parts, "command")
	}
	if n := len(c.Labels); n > 0 {
		parts = append(parts, plural(n, "label", "labels"))
	}
	if n := len(c.Ports); n > 0 {
		parts = append(parts, plural(n, "published port", "published ports"))
	}
	if n := len(c.ExposedOnly); n > 0 {
		parts = append(parts, plural(n, "exposed port", "exposed ports"))
	}
	if n := len(c.Mounts); n > 0 {
		parts = append(parts, plural(n, "mount", "mounts"))
	}
	if n := len(c.Networks); n > 0 {
		var names []string
		for _, a := range c.Networks {
			names = append(names, a.Network)
		}
		parts = append(parts, plural(n, "network", "networks")+" ("+shownList(names)+")")
	} else if c.NetworkMode != "" {
		parts = append(parts, "network "+shown(c.NetworkMode))
	}
	parts = append(parts, "restart "+c.Restart.String())
	if c.Healthcheck != nil {
		parts = append(parts, "health check")
	}
	if c.StopSignal != "" || c.StopTimeout != nil {
		parts = append(parts, "stop behaviour")
	}
	if c.Resources != (engine.ResourceLimits{}) {
		parts = append(parts, "resource limits")
	}
	for _, s := range []struct {
		set  bool
		name string
	}{{c.WorkingDir != "", "working directory"}, {c.User != "", "user"}, {c.Hostname != "", "hostname"}, {c.Tty, "TTY"}, {c.OpenStdin, "open stdin"}, {c.Init != nil, "init"}} {
		if s.set {
			parts = append(parts, s.name)
		}
	}
	return strings.Join(parts, " · ")
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// redactEnvValues hides every carried environment value from a message the
// daemon wrote: the job's log and error, and the status line, show it.
func redactEnvValues(msg string, c *CloneSpec) string {
	if c == nil {
		return msg
	}
	for _, v := range c.envValues() {
		msg = strings.ReplaceAll(msg, v, "‹value hidden›")
	}
	return msg
}
