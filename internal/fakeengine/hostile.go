package fakeengine

// The hostile fixture: one container, with its image, a volume and a
// network, whose strings are what a daemon that means harm would send. A
// daemon chooses every one of them — a name, a label, a path, a command —
// and the desk draws them all (openspec change escape-daemon-text, D8).
//
// Each field carries a different class of value, so that a test which finds
// one drawn raw knows which rule was missed. It is a test fixture: it is not
// part of SeedDemo, so the sandbox and the captured screens stay as they
// are.

// The hostile values, one class each.
const (
	// HostileConceal is SGR 8: a colour sequence, which the frame guard
	// keeps, and which hides the text after it.
	HostileConceal = "\x1b[8m"
	// HostileColour is a plain colour sequence.
	HostileColour = "\x1b[38;5;199m"
	// HostileNewline forges a line.
	HostileNewline = "\n"
	// HostileC1 is a C1 control (CSI as one character) and what follows it:
	// "erase the display" to a terminal that honours C1.
	HostileC1 = "\u009b2J"
	// HostileInvalidUTF8 is a byte that is no character.
	HostileInvalidUTF8 = "\xff"
	// HostileRLO is the right-to-left override: it draws nothing and
	// reverses what follows.
	HostileRLO = "\u202e"
	// HostileOSC retitles the window.
	HostileOSC = "\x1b]0;pwned\x07"
)

// Hostile is what SeedHostile seeded, as the daemon holds it: the raw bytes.
type Hostile struct {
	ContainerID string
	// Name carries a backslash and an OSC sequence.
	Name string
	// ImageRef is the image's tag, carrying a plain colour sequence; ImageID
	// is its ID (hex).
	ImageRef string
	ImageID  string
	// ImageLabel is the image's `maintainer` label, carrying conceal.
	ImageLabel string
	// Label is the container's `note` label, carrying conceal, and
	// Backslash its `path` label: one backslash, and nothing else to escape.
	Label     string
	Backslash string
	// MountDest is a bind mount's destination, carrying a newline.
	MountDest string
	// Project is the compose project label, carrying a C1 control; Service,
	// ProjectDir and ConfigFiles are the other compose labels.
	Project     string
	Service     string
	ProjectDir  string
	ConfigFiles string
	// WorkingDir carries a byte that is not UTF-8. JSON cannot carry one:
	// what the daemon's encoder sends, and any decoder reads, is U+FFFD in
	// its place (WorkingDirDecoded).
	WorkingDir        string
	WorkingDirDecoded string
	// Command carries a right-to-left override.
	Command []string
	// User, Health and StateError are inspect's, each carrying a sequence.
	User       string
	Health     string
	StateError string
	// Volume and Network are a volume and a network whose names carry a
	// conceal sequence; VolumeLabel and NetworkLabel are their `owner` and
	// `zone` labels. The container mounts the one and is attached to the
	// other. NetworkID is the network's ID.
	Volume       string
	VolumeLabel  string
	Network      string
	NetworkID    string
	NetworkLabel string
}

// SeedHostile adds the hostile fixture and returns what it seeded. The
// container is running.
func (s *Server) SeedHostile() Hostile {
	h := Hostile{
		Name:              `evil\box` + HostileOSC,
		ImageRef:          "registry.test/evil" + HostileColour + "pink:1",
		ImageLabel:        "ops" + HostileConceal + "hidden-maintainer",
		Label:             "shown" + HostileConceal + "hidden-note",
		Backslash:         `C:\dir`,
		MountDest:         "/data" + HostileNewline + "forged-line",
		Project:           "proj" + HostileC1 + "x",
		Service:           "web" + HostileConceal + "svc",
		ProjectDir:        "/srv/" + HostileColour + "proj",
		ConfigFiles:       "/srv/proj/compose" + HostileConceal + ".yaml",
		WorkingDir:        "/work" + HostileInvalidUTF8 + "dir",
		WorkingDirDecoded: "/work\ufffddir",
		Command:           []string{"sh", "-c", "echo " + HostileRLO + "drow"},
		User:              "root" + HostileConceal + "user",
		Health:            "un" + HostileConceal + "healthy",
		StateError:        "boom" + HostileConceal + "hidden-error",
		Volume:            "vol" + HostileConceal + "data",
		VolumeLabel:       "team" + HostileConceal + "hidden-owner",
		Network:           "net" + HostileConceal + "lan",
		NetworkLabel:      "dmz" + HostileConceal + "hidden-zone",
	}
	s.AddVolume(h.Volume, map[string]string{"owner": h.VolumeLabel})
	h.NetworkID = s.AddNetwork(h.Network, map[string]string{"zone": h.NetworkLabel})
	h.ContainerID = s.AddContainer(ContainerSpec{
		Name:  h.Name,
		Image: "hostile-base:1",
		Cmd:   h.Command,
		Labels: map[string]string{
			"note":                                   h.Label,
			"path":                                   h.Backslash,
			"com.docker.compose.project":             h.Project,
			"com.docker.compose.service":             h.Service,
			"com.docker.compose.project.working_dir": h.ProjectDir,
			"com.docker.compose.project.config_files": h.ConfigFiles,
		},
		Volumes:    []string{h.Volume},
		Networks:   []string{h.Network},
		Health:     h.Health,
		User:       h.User,
		StateError: h.StateError,
	})

	// What no specification can express: a tag no parser accepts, a mount
	// destination and a working directory no daemon would have recorded.
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.containers[h.ContainerID]
	img := s.images[c.ImageID]
	img.RepoTags = []string{h.ImageRef}
	img.Labels = map[string]string{"maintainer": h.ImageLabel}
	h.ImageID = img.ID
	c.Image = h.ImageRef
	c.WorkingDir = h.WorkingDir
	c.Mounts = append(c.Mounts, MountSpec{Type: "bind", Source: "/host" + HostileColour + "/src", Destination: h.MountDest, RW: true})
	return h
}
