package plan

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"unicode"

	"github.com/fardani235/docker-desk/internal/engine"
)

// ---- the create specification --------------------------------------------------------
//
// A ContainerCreateSpec is what the create form parses to and what the plan
// carries to the engine. It is the form's vocabulary, not the daemon's: the
// engine maps it to its own ContainerCreateOptions (docs/DECISIONS.md D24).

// ContainerCreateSpec is a container to create, fully decided.
type ContainerCreateSpec struct {
	Image   string      // normalized with NormalizeRef
	Name    string      // "" means the daemon names it
	Ports   []PortSpec  // in the order entered
	Env     []string    // "NAME=value", in the order entered
	Mounts  []MountSpec // in the order entered
	Restart RestartSpec
	Command []string // nil means the image's own command
}

// PortSpec is one published port.
type PortSpec struct {
	HostIP        string // "" means every address
	HostPort      uint16 // 0 means the daemon picks one
	ContainerPort uint16
	Proto         string // "tcp" or "udp"
}

// String is the entry as the summary draws it: host → container/proto.
func (p PortSpec) String() string {
	host := "any port"
	if p.HostPort != 0 {
		host = strconv.Itoa(int(p.HostPort))
	}
	if p.HostIP != "" {
		addr := p.HostIP
		if strings.Contains(addr, ":") {
			addr = "[" + addr + "]"
		}
		host = addr + ":" + host
	}
	return fmt.Sprintf("%s → %d/%s", host, p.ContainerPort, p.Proto)
}

// MountSpec is one mount.
type MountSpec struct {
	Type        string // "volume" or "bind"
	Source      string // volume name or absolute host path
	Destination string // absolute path in the container
	ReadOnly    bool
}

// String is the entry as the summary draws it.
func (m MountSpec) String() string {
	mode := m.Type
	if m.ReadOnly {
		mode += ", ro"
	}
	return fmt.Sprintf("%s → %s (%s)", m.Source, m.Destination, mode)
}

// RestartSpec is a restart policy.
type RestartSpec struct {
	Name              string // no, always, unless-stopped, on-failure
	MaximumRetryCount int    // only for on-failure; 0 means no limit
}

func (r RestartSpec) String() string {
	if r.Name == "on-failure" && r.MaximumRetryCount > 0 {
		return fmt.Sprintf("on-failure, at most %d retries", r.MaximumRetryCount)
	}
	if r.Name == "" {
		return "no"
	}
	return r.Name
}

// RestartPolicies are the policies the form offers, in its order.
var RestartPolicies = []string{"no", "always", "unless-stopped", "on-failure"}

// ---- parsers -------------------------------------------------------------------------

// ParsePort reads `[host-address:][host-port:]container-port[/tcp|udp]`. An
// IPv6 host address is written in brackets: [::1]:8080:80.
func ParsePort(s string) (PortSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return PortSpec{}, errors.New("a port entry is [host-address:][host-port:]container-port[/tcp|udp], e.g. 8080:80")
	}
	p := PortSpec{Proto: "tcp"}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto := strings.ToLower(s[i+1:])
		if proto != "tcp" && proto != "udp" {
			return PortSpec{}, fmt.Errorf("%q: the protocol is tcp or udp, not %q", s, s[i+1:])
		}
		p.Proto = proto
		s = s[:i]
	}
	var parts []string
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return PortSpec{}, fmt.Errorf("%q: an IPv6 address needs its closing bracket", s)
		}
		rest := s[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return PortSpec{}, fmt.Errorf("%q: after the address comes :host-port:container-port", s)
		}
		parts = append([]string{s[1:end]}, strings.Split(rest[1:], ":")...)
		if len(parts) != 3 {
			return PortSpec{}, fmt.Errorf("%q: after the address comes :host-port:container-port", s)
		}
	} else {
		parts = strings.Split(s, ":")
		if len(parts) > 3 {
			return PortSpec{}, fmt.Errorf("%q: an IPv6 host address must be in brackets, e.g. [::1]:8080:80", s)
		}
	}
	var hostPort, containerPort string
	switch len(parts) {
	case 1:
		containerPort = parts[0]
	case 2:
		hostPort, containerPort = parts[0], parts[1]
	case 3:
		addr, err := netip.ParseAddr(parts[0])
		if err != nil {
			return PortSpec{}, fmt.Errorf("%q: %q is not an IP address", s, parts[0])
		}
		p.HostIP = addr.String()
		hostPort, containerPort = parts[1], parts[2]
	}
	n, err := portNumber(containerPort)
	if err != nil {
		return PortSpec{}, fmt.Errorf("%q: container port %v", s, err)
	}
	p.ContainerPort = n
	if hostPort != "" {
		n, err := portNumber(hostPort)
		if err != nil {
			return PortSpec{}, fmt.Errorf("%q: host port %v", s, err)
		}
		p.HostPort = n
	} else if len(parts) == 2 {
		return PortSpec{}, fmt.Errorf("%q: the host port is missing before the colon", s)
	}
	return p, nil
}

func portNumber(s string) (uint16, error) {
	if s == "" {
		return 0, errors.New("is missing")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("%d: the port must be between 1 and 65535", n)
	}
	return uint16(n), nil
}

// ParseEnv reads `NAME=value`; the value is everything after the first `=`
// and may be empty. A bare NAME (Docker's "take it from my environment") is
// refused: the desk does not pass its own environment on.
func ParseEnv(s string) (string, error) {
	name, value, ok := strings.Cut(s, "=")
	if !ok {
		return "", fmt.Errorf("%q: an environment entry is NAME=value", redactEnv(s))
	}
	if name == "" {
		return "", errors.New("an environment entry needs a name before the =")
	}
	for _, r := range name {
		if unicode.IsSpace(r) {
			return "", fmt.Errorf("environment name %q contains whitespace", name)
		}
	}
	return name + "=" + value, nil
}

// redactEnv keeps a malformed entry's beginning only: it is about to be shown
// in an error, and the operator may have typed a value without the name.
func redactEnv(s string) string {
	if r := []rune(s); len(r) > 12 {
		return string(r[:12]) + "…"
	}
	return s
}

// ParseMount reads `source:destination[:ro|:rw]`. A source that begins with
// `/` is a host path (a bind mount); anything else is a volume name.
func ParseMount(s string) (MountSpec, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return MountSpec{}, fmt.Errorf("%q: a mount entry is source:destination[:ro], e.g. data:/var/lib/data", s)
	}
	m := MountSpec{Source: parts[0], Destination: parts[1]}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			m.ReadOnly = true
		case "rw":
		default:
			return MountSpec{}, fmt.Errorf("%q: the mode is ro or rw, not %q", s, parts[2])
		}
	}
	if m.Source == "" {
		return MountSpec{}, fmt.Errorf("%q: the source (a volume name or a host path) is missing", s)
	}
	if !strings.HasPrefix(m.Destination, "/") {
		return MountSpec{}, fmt.Errorf("%q: the destination must be an absolute path, e.g. /data", s)
	}
	if strings.HasPrefix(m.Source, "/") {
		m.Type = "bind"
	} else {
		if err := validName("volume", m.Source, 2); err != nil {
			return MountSpec{}, fmt.Errorf("%q: %v (a host path starts with /)", s, err)
		}
		m.Type = "volume"
	}
	return m, nil
}

// ParseRestart reads a restart policy: no, always, unless-stopped,
// on-failure or on-failure:N.
func ParseRestart(s string) (RestartSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return RestartSpec{Name: "no"}, nil
	}
	name, count, hasCount := strings.Cut(s, ":")
	known := false
	for _, p := range RestartPolicies {
		known = known || p == name
	}
	if !known {
		return RestartSpec{}, fmt.Errorf("%q is not a restart policy: no, always, unless-stopped or on-failure", name)
	}
	r := RestartSpec{Name: name}
	if hasCount {
		if name != "on-failure" {
			return RestartSpec{}, fmt.Errorf("only on-failure takes a retry limit, not %s", name)
		}
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return RestartSpec{}, fmt.Errorf("the retry limit is a whole number, not %q", count)
		}
		r.MaximumRetryCount = n
	}
	return r, nil
}

// ---- the form's raw values -----------------------------------------------------------

// Field names a field of the create form, so an error can be placed on it.
type Field int

const (
	FieldImage Field = iota
	FieldName
	FieldPorts
	FieldEnv
	FieldMounts
	FieldRestart
	FieldCommand
)

// FieldError is a value the create form must correct. Entry is the index of
// the offending entry of a list field (ports, environment, mounts), counted
// over the entries as typed, blank ones included; -1 otherwise.
type FieldError struct {
	Field Field
	Entry int
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

func fieldErr(f Field, entry int, err error) error {
	return &FieldError{Field: f, Entry: entry, Msg: err.Error()}
}

// CreateInput is the create form's text, exactly as typed.
type CreateInput struct {
	Image   string
	Name    string
	Ports   []string
	Env     []string
	Mounts  []string
	Restart string // as ParseRestart reads it
	Command string
}

// ParseCreate reads the form's text into a specification. Blank entries of a
// list field are ignored (an entry added and left empty sends nothing). It
// checks syntax only; ContainerCreate checks the values against the daemon.
func ParseCreate(in CreateInput) (ContainerCreateSpec, error) {
	var spec ContainerCreateSpec
	ref, err := NormalizeRef(in.Image)
	if err != nil {
		return spec, fieldErr(FieldImage, -1, err)
	}
	spec.Image = ref
	spec.Name = strings.TrimSpace(in.Name)
	for i, s := range in.Ports {
		if strings.TrimSpace(s) == "" {
			continue
		}
		p, err := ParsePort(s)
		if err != nil {
			return spec, fieldErr(FieldPorts, i, err)
		}
		spec.Ports = append(spec.Ports, p)
	}
	for i, s := range in.Env {
		if strings.TrimSpace(s) == "" {
			continue
		}
		e, err := ParseEnv(s)
		if err != nil {
			return spec, fieldErr(FieldEnv, i, err)
		}
		spec.Env = append(spec.Env, e)
	}
	for i, s := range in.Mounts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		m, err := ParseMount(s)
		if err != nil {
			return spec, fieldErr(FieldMounts, i, err)
		}
		spec.Mounts = append(spec.Mounts, m)
	}
	if spec.Restart, err = ParseRestart(in.Restart); err != nil {
		return spec, fieldErr(FieldRestart, -1, err)
	}
	if spec.Command, err = SplitCommand(in.Command); err != nil {
		return spec, fieldErr(FieldCommand, -1, fmt.Errorf("command: %v", err))
	}
	return spec, nil
}

// ---- the plan ------------------------------------------------------------------------

// ImagePresent reports whether the snapshot has an image under exactly this
// (normalized) reference.
func (s Snapshot) ImagePresent(ref string) bool {
	for _, img := range s.Images {
		for _, r := range img.Refs() {
			if r == ref {
				return true
			}
		}
	}
	return false
}

// ContainerCreate plans creating and starting one container: a pull of its
// exact image reference when the daemon does not have it, the create, and
// the start of the ID the create returns. Each step depends on the one
// before it (docs/DECISIONS.md D23).
//
// Input problems are returned as a *FieldError, never as Blockers: a form
// plan that cannot run would otherwise be dropped silently.
func ContainerCreate(snap Snapshot, spec ContainerCreateSpec) (Plan, error) {
	ref, err := NormalizeRef(spec.Image)
	if err != nil {
		return Plan{}, fieldErr(FieldImage, -1, err)
	}
	spec.Image = ref
	if spec.Name != "" {
		if err := validName("container", spec.Name, 2); err != nil {
			return Plan{}, fieldErr(FieldName, -1, err)
		}
		for _, c := range snap.Containers {
			if strings.TrimPrefix(c.Name, "/") == spec.Name {
				return Plan{}, fieldErr(FieldName, -1, fmt.Errorf("the name %q is taken by container %s (%s)", spec.Name, engine.ShortID(c.ID), c.State))
			}
		}
	}
	type bind struct {
		ip    string
		port  uint16
		proto string
	}
	seen := map[bind]bool{}
	for i, p := range spec.Ports {
		if p.ContainerPort == 0 {
			return Plan{}, fieldErr(FieldPorts, i, errors.New("the port must be between 1 and 65535"))
		}
		if p.HostPort == 0 {
			continue
		}
		ip := p.HostIP
		if ip == "0.0.0.0" || ip == "::" {
			ip = ""
		}
		k := bind{ip, p.HostPort, p.Proto}
		if seen[k] {
			return Plan{}, fieldErr(FieldPorts, i, fmt.Errorf("host port %d/%s is published twice on the same address", p.HostPort, p.Proto))
		}
		seen[k] = true
	}
	for i, e := range spec.Env {
		if _, err := ParseEnv(e); err != nil {
			return Plan{}, fieldErr(FieldEnv, i, err)
		}
	}
	for i, m := range spec.Mounts {
		if !strings.HasPrefix(m.Destination, "/") {
			return Plan{}, fieldErr(FieldMounts, i, fmt.Errorf("%s: the destination must be an absolute path", m.Destination))
		}
	}
	if spec.Restart.Name == "" {
		spec.Restart.Name = "no"
	}
	if _, err := ParseRestart(spec.Restart.Name); err != nil {
		return Plan{}, fieldErr(FieldRestart, -1, err)
	}
	if spec.Restart.Name != "on-failure" && spec.Restart.MaximumRetryCount != 0 {
		return Plan{}, fieldErr(FieldRestart, -1, fmt.Errorf("only on-failure takes a retry limit, not %s", spec.Restart.Name))
	}

	display := spec.Name
	if display == "" {
		display = "(named by the daemon)"
	}
	target := spec.Name
	if target == "" {
		target = ref
	}
	p := Plan{
		Title:     "Create container",
		JobTitle:  "create container",
		JobTarget: target,
		Target:    Target{Kind: "container", Name: target},
		Tier:      TierForm,
	}
	createAt := 0
	if !snap.ImagePresent(ref) {
		p.Steps = append(p.Steps, Step{Op: OpPullImage, Kind: "image", ID: ref, Name: ref, IDFrom: -1})
		p.Effects = append(p.Effects, fmt.Sprintf("%s is not on this daemon: it is pulled first (exactly that reference).", ref))
		createAt = 1
	}
	create := Step{Op: OpCreateContainer, Kind: "container", Name: display, Detail: ref,
		Create: &spec, ProducesID: true, IDFrom: -1}
	if createAt > 0 {
		create.DependsOn = []int{0}
	}
	p.Steps = append(p.Steps, create,
		Step{Op: OpStart, Kind: "container", Name: display, DependsOn: []int{createAt}, IDFrom: createAt})
	p.Effects = append(p.Effects,
		fmt.Sprintf("A container is created from %s and started.", ref),
		"Nothing else on the daemon is started, stopped or removed.")
	if n := len(spec.Env); n > 0 {
		// The count only: the values stay in the form (D29).
		p.Effects = append(p.Effects, fmt.Sprintf("%s set.", plural(n, "environment variable is", "environment variables are")))
	}
	return p, nil
}
