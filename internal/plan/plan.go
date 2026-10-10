// Package plan decides what a mutation will do before anything is sent to
// the daemon.
//
// A Plan is built by pure functions from a snapshot of the engine. The
// confirmation dialog renders the Plan; the job executes the very same Plan.
// There is no second lookup between showing and doing, which is what makes
// "confirm the exact object" and "a prune destroys only what it showed" true
// by construction. See docs/DECISIONS.md, D11–D15.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Snapshot is the engine state a plan is computed from.
type Snapshot struct {
	Containers []engine.Container
	Images     []engine.Image
	Volumes    []engine.Volume
	Networks   []engine.Network
}

// Tier is how much deliberate input a plan needs before it may run.
type Tier int

const (
	// TierConfirm: a reversible state change; press `y`.
	TierConfirm Tier = iota
	// TierTyped: a deletion; type the word shown, then Enter.
	TierTyped
	// TierForm: the operator typed the object's name into a form; Enter.
	TierForm
)

// Op is one request to the daemon.
type Op int

const (
	OpStart Op = iota
	OpStop
	OpRestart
	OpPause
	OpUnpause
	OpKill
	OpRemoveContainer
	OpPullImage
	OpRemoveImage
	OpCreateVolume
	OpRemoveVolume
	OpCreateNetwork
	OpRemoveNetwork
	OpConnect
	OpDisconnect
)

var opVerbs = map[Op]string{
	OpStart: "start", OpStop: "stop", OpRestart: "restart", OpPause: "pause", OpUnpause: "unpause",
	OpKill: "kill", OpRemoveContainer: "remove", OpPullImage: "pull", OpRemoveImage: "remove",
	OpCreateVolume: "create", OpRemoveVolume: "remove", OpCreateNetwork: "create",
	OpRemoveNetwork: "remove", OpConnect: "connect", OpDisconnect: "disconnect",
}

func (o Op) String() string { return opVerbs[o] }

// Step is one object a plan acts on and the single request that does it.
type Step struct {
	Op   Op
	Kind string // container, image, volume, network
	// ID is the identifier sent to the daemon: a container ID, an image
	// reference or ID, a volume name, a network ID.
	ID string
	// Name is how the object is shown to the operator.
	Name string
	// Other is the second object of a two-object request (the container ID
	// for connect/disconnect), with OtherName for display.
	Other     string
	OtherName string
	// Detail is extra display text (state, short ID).
	Detail string
	Size   int64

	// Pin ties a step that addresses its object by a reusable name to the
	// object the operator was shown: the image ID a reference pointed at, or
	// a volume's creation time. Execute re-checks it immediately before the
	// request and refuses if the name now means something else.
	Pin string
	// KeepsData marks an untag that was promised to leave the image in
	// place. Execute refuses it if the reference has since become the
	// image's last one (the request would then delete the image).
	KeepsData bool
	// MustBeUnused marks a prune step on a tagged image. An untag is not
	// refused by the daemon when the image is in use, so Execute checks for
	// containers using the image itself before sending it.
	MustBeUnused bool
}

// Target identifies what a plan is about.
type Target struct {
	Kind string // "container", "image", …, or a plural for bulk plans
	Name string
	ID   string // short form for display; empty for bulk plans
}

// Plan is a mutation, fully decided and not yet started.
type Plan struct {
	// Title is the dialog heading, e.g. "Stop container".
	Title string
	// JobTitle and JobTarget label the job in the Jobs pane.
	JobTitle  string
	JobTarget string
	Target    Target
	// Effects say in plain words what will happen.
	Effects []string
	// Warnings are shown prominently but do not prevent the plan.
	Warnings []string
	// Blockers are reasons the plan must not run. A plan with blockers can
	// only be dismissed.
	Blockers []string
	// Steps are exactly the requests that will be sent, in order.
	Steps []Step
	// Listed is true when the dialog must show every step individually
	// (prunes and whole-project actions).
	Listed bool
	// Nothing explains why there is nothing to do; such a plan cannot run.
	Nothing string
	Tier    Tier
	// Word is what must be typed for TierTyped.
	Word      string
	TotalSize int64
	// Scope describes a switchable scope (prunes), for display.
	Scope string
}

// Runnable reports whether the plan may be confirmed.
func (p Plan) Runnable() bool {
	return len(p.Blockers) == 0 && p.Nothing == "" && len(p.Steps) > 0
}

// NotApplicable is returned instead of a Plan when an action makes no sense
// for the object in its current state. The desk shows the reason and opens
// no dialog.
type NotApplicable struct{ Reason string }

func (e *NotApplicable) Error() string { return e.Reason }

func na(format string, args ...any) error {
	return &NotApplicable{Reason: fmt.Sprintf(format, args...)}
}

// ---- lookups ------------------------------------------------------------------------

func (s Snapshot) container(id string) (engine.Container, bool) {
	for _, c := range s.Containers {
		if c.ID == id {
			return c, true
		}
	}
	return engine.Container{}, false
}

func (s Snapshot) image(id string) (engine.Image, bool) {
	for _, i := range s.Images {
		if i.ID == id {
			return i, true
		}
	}
	return engine.Image{}, false
}

func (s Snapshot) volume(name string) (engine.Volume, bool) {
	for _, v := range s.Volumes {
		if v.Name == name {
			return v, true
		}
	}
	return engine.Volume{}, false
}

func (s Snapshot) network(id string) (engine.Network, bool) {
	for _, n := range s.Networks {
		if n.ID == id {
			return n, true
		}
	}
	return engine.Network{}, false
}

func trimSHA(id string) string { return strings.TrimPrefix(id, "sha256:") }

// ImageUsers returns every container, running or not, created from the image.
func (s Snapshot) ImageUsers(img engine.Image) []engine.Container {
	var out []engine.Container
	for _, c := range s.Containers {
		if c.ImageID != "" && trimSHA(c.ImageID) == trimSHA(img.ID) {
			out = append(out, c)
		}
	}
	return out
}

// VolumeUsers returns every container, running or not, that mounts the volume.
func (s Snapshot) VolumeUsers(name string) []engine.Container {
	var out []engine.Container
	for _, c := range s.Containers {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == name {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// NetworkUsers returns every container attached to the network or, when
// stopped, configured to use it. This is deliberately wider than Docker's
// own notion of "in use", which only counts running containers.
func (s Snapshot) NetworkUsers(n engine.Network) []engine.Container {
	var out []engine.Container
	for _, c := range s.Containers {
		for _, a := range c.Networks {
			if a.NetworkID == n.ID || (a.NetworkID == "" && a.Network == n.Name) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func describeContainers(cs []engine.Container) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s (%s)", c.Name, c.State))
	}
	sort.Strings(parts)
	const max = 6
	if len(parts) > max {
		parts = append(parts[:max], fmt.Sprintf("and %d more", len(parts)-max))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- containers ---------------------------------------------------------------------

// Container plans a lifecycle action on one container.
func Container(op Op, snap Snapshot, id string) (Plan, error) {
	c, ok := snap.container(id)
	if !ok {
		return Plan{}, na("that container no longer exists")
	}
	p := Plan{
		Target:    Target{Kind: "container", Name: c.Name, ID: engine.ShortID(c.ID)},
		JobTarget: c.Name,
		Steps:     []Step{{Op: op, Kind: "container", ID: c.ID, Name: c.Name, Detail: c.State}},
		Tier:      TierConfirm,
	}
	switch op {
	case OpStart:
		switch c.State {
		case engine.StateRunning:
			return Plan{}, na("%s is already running", c.Name)
		case engine.StatePaused:
			return Plan{}, na("%s is paused — unpause it instead", c.Name)
		case engine.StateRestarting, engine.StateRemoving:
			return Plan{}, na("%s is %s", c.Name, c.State)
		}
		p.Title, p.JobTitle = "Start container", "start container"
		p.Effects = []string{"The container is started with its existing configuration."}
	case OpStop:
		if !c.IsActive() {
			return Plan{}, na("%s is not running (%s)", c.Name, c.State)
		}
		p.Title, p.JobTitle = "Stop container", "stop container"
		p.Effects = []string{
			"The container is sent its stop signal (SIGTERM unless configured otherwise).",
			"If it has not exited after its grace period (10 s by default) the daemon kills it.",
		}
	case OpRestart:
		if c.State == engine.StateRemoving {
			return Plan{}, na("%s is being removed", c.Name)
		}
		p.Title, p.JobTitle = "Restart container", "restart container"
		if c.IsActive() {
			p.Effects = []string{"The container is stopped (signal, then killed after the grace period) and started again."}
		} else {
			p.Effects = []string{"The container is not running; it will be started."}
		}
	case OpPause:
		switch {
		case c.State == engine.StatePaused:
			return Plan{}, na("%s is already paused", c.Name)
		case c.State != engine.StateRunning:
			return Plan{}, na("%s is not running (%s)", c.Name, c.State)
		}
		p.Title, p.JobTitle = "Pause container", "pause container"
		p.Effects = []string{"Every process in the container is frozen. Nothing is lost; unpause resumes them."}
	case OpUnpause:
		if c.State != engine.StatePaused {
			return Plan{}, na("%s is not paused (%s)", c.Name, c.State)
		}
		p.Title, p.JobTitle = "Unpause container", "unpause container"
		p.Effects = []string{"Every process in the container resumes."}
	case OpKill:
		if !c.IsActive() {
			return Plan{}, na("%s is not running (%s)", c.Name, c.State)
		}
		p.Title, p.JobTitle = "Kill container", "kill container"
		p.Effects = []string{"The container's main process is sent SIGKILL immediately."}
		p.Warnings = []string{"It gets no chance to shut down cleanly; unsaved state inside it is lost."}
	case OpRemoveContainer:
		p.Title, p.JobTitle = "Remove container", "remove container"
		p.Tier, p.Word = TierTyped, "remove"
		p.Effects = []string{"The container and its writable layer are permanently deleted."}
		named, anon := 0, 0
		for _, m := range c.Mounts {
			if m.Type != "volume" {
				continue
			}
			if v, ok := snap.volume(m.Name); ok && v.Anonymous() {
				anon++
			} else {
				named++
			}
		}
		if named+anon > 0 {
			p.Effects = append(p.Effects, fmt.Sprintf("Its volumes are kept (%s).", plural(named+anon, "volume", "volumes")))
		}
		if c.IsActive() {
			p.Blockers = []string{
				fmt.Sprintf("%s is %s. Docker Desk never force-removes: stop or kill it first.", c.Name, c.State),
			}
		}
	default:
		return Plan{}, fmt.Errorf("plan: %v is not a container action", op)
	}
	return p, nil
}

// ---- images -------------------------------------------------------------------------

// NormalizeRef validates an image reference as typed by the operator and
// returns the exact reference that will be pulled: ":latest" is filled in
// when neither a tag nor a digest is given.
func NormalizeRef(input string) (string, error) {
	return normalizeRef(strings.TrimSpace(input))
}

// Pull plans pulling one image reference.
func Pull(snap Snapshot, input string) (Plan, error) {
	ref, err := NormalizeRef(input)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{
		Title:     "Pull image",
		JobTitle:  "pull image",
		JobTarget: ref,
		Target:    Target{Kind: "image", Name: ref},
		Steps:     []Step{{Op: OpPullImage, Kind: "image", ID: ref, Name: ref}},
		Effects:   []string{fmt.Sprintf("%s is downloaded from its registry into this daemon's image store.", ref)},
		Tier:      TierForm,
	}
	for _, img := range snap.Images {
		for _, r := range img.Refs() {
			if r == ref {
				p.Effects = append(p.Effects, "It is already present; the pull updates it if the registry has a newer one.")
			}
		}
	}
	return p, nil
}

// ImageRemove plans removing one row of the image list: a reference of an
// image, or — when ref is empty — an untagged image by ID.
func ImageRemove(snap Snapshot, imageID, ref string) (Plan, error) {
	img, ok := snap.image(imageID)
	if !ok {
		return Plan{}, na("that image no longer exists")
	}
	refs := img.Refs()
	if ref != "" {
		found := false
		for _, r := range refs {
			found = found || r == ref
		}
		if !found {
			return Plan{}, na("the image no longer has the reference %s", ref)
		}
	}
	name := ref
	target := ref
	if ref == "" {
		if len(refs) > 0 {
			return Plan{}, na("that image now has a tag (%s); remove it by its reference", refs[0])
		}
		name = "<untagged> " + engine.ShortID(img.ID)
		target = img.ID
	}
	step := Step{Op: OpRemoveImage, Kind: "image", ID: target, Name: name, Detail: engine.ShortID(img.ID), Size: img.Size}
	if ref != "" {
		step.Pin = img.ID // the reference must still mean this image when the request is sent
	}
	p := Plan{
		Title:     "Remove image",
		JobTitle:  "remove image",
		JobTarget: name,
		Target:    Target{Kind: "image", Name: name, ID: engine.ShortID(img.ID)},
		Steps:     []Step{step},
		Tier:      TierTyped,
		Word:      "remove",
		TotalSize: img.Size,
	}
	others := 0
	if ref != "" {
		others = len(refs) - 1
	}
	if others > 0 {
		var rest []string
		for _, r := range refs {
			if r != ref {
				rest = append(rest, r)
			}
		}
		p.Title = "Untag image"
		p.TotalSize = 0
		p.Steps[0].Size = 0
		p.Steps[0].KeepsData = true
		p.Effects = []string{
			fmt.Sprintf("Only the reference %s is removed.", ref),
			fmt.Sprintf("The image data stays: it is still referenced as %s.", strings.Join(rest, ", ")),
		}
		if users := snap.ImageUsers(img); len(users) > 0 {
			// Not a blocker: nothing they depend on goes away. But say so.
			p.Effects = append(p.Effects, fmt.Sprintf("%s using the image (%s) keep running on it, unaffected.",
				plural(len(users), "container", "containers"), describeContainers(users)))
		}
		return p, nil
	}
	p.Effects = []string{
		"The image is deleted.",
		"Layers it does not share with other images are freed; untagged parent layers nothing else uses go with it.",
	}
	if users := snap.ImageUsers(img); len(users) > 0 {
		p.Blockers = []string{
			fmt.Sprintf("In use by %s: %s.", plural(len(users), "container", "containers"), describeContainers(users)),
			"Docker Desk never forces a removal: remove those containers first.",
		}
	}
	return p, nil
}

// ImagePrune plans removing unused images: dangling ones only, or — with all
// — every image no container was created from.
func ImagePrune(snap Snapshot, all bool) Plan {
	p := Plan{
		Title:    "Prune images",
		JobTitle: "prune images",
		Tier:     TierTyped,
		Word:     "prune",
		Listed:   true,
		Scope:    "dangling images only (untagged, unused)",
	}
	if all {
		p.Scope = "all images not used by any container"
		p.Warnings = []string{"This includes tagged images. Pulling or rebuilding them again costs time and network."}
	}
	images := append([]engine.Image(nil), snap.Images...)
	sort.SliceStable(images, func(i, j int) bool { return images[i].Created.Before(images[j].Created) })
	n := 0
	for _, img := range images {
		if len(snap.ImageUsers(img)) > 0 {
			continue
		}
		refs := img.Refs()
		switch {
		case img.Dangling:
			p.Steps = append(p.Steps, Step{
				Op: OpRemoveImage, Kind: "image", ID: img.ID,
				Name: "<untagged>", Detail: engine.ShortID(img.ID), Size: img.Size,
			})
		case all && len(refs) > 0:
			// One request per reference: removing by ID would need force
			// when an image has several tags. The last untag deletes it.
			for i, r := range refs {
				st := Step{
					Op: OpRemoveImage, Kind: "image", ID: r, Name: r, Detail: engine.ShortID(img.ID),
					Pin: img.ID, MustBeUnused: true,
				}
				if i == len(refs)-1 {
					st.Size = img.Size
				}
				p.Steps = append(p.Steps, st)
			}
		default:
			continue
		}
		n++
		p.TotalSize += img.Size
	}
	if n == 0 {
		p.Nothing = "Nothing to prune: no " + map[bool]string{false: "dangling", true: "unused"}[all] + " images."
		return p
	}
	p.Target = Target{Kind: "images", Name: plural(n, "image", "images")}
	p.JobTarget = p.Target.Name
	p.Effects = []string{
		"Exactly the images listed below are removed, one request each, never forced.",
		"Untagged parent layers that nothing else uses go with them, as with `docker rmi`.",
		"Anything that became in use, or whose name now means another image, is refused and left alone.",
	}
	return p
}

// ---- volumes ------------------------------------------------------------------------

// VolumeCreate plans creating a named volume with the local driver.
func VolumeCreate(snap Snapshot, input string) (Plan, error) {
	name := strings.TrimSpace(input)
	if err := validName("volume", name, 2); err != nil {
		return Plan{}, err
	}
	if _, exists := snap.volume(name); exists {
		return Plan{}, fmt.Errorf("a volume named %q already exists", name)
	}
	return Plan{
		Title:     "Create volume",
		JobTitle:  "create volume",
		JobTarget: name,
		Target:    Target{Kind: "volume", Name: name},
		Steps:     []Step{{Op: OpCreateVolume, Kind: "volume", ID: name, Name: name}},
		Effects:   []string{fmt.Sprintf("An empty volume %q is created with the local driver.", name)},
		Tier:      TierForm,
	}, nil
}

// VolumeRemove plans removing one volume.
func VolumeRemove(snap Snapshot, name string) (Plan, error) {
	v, ok := snap.volume(name)
	if !ok {
		return Plan{}, na("that volume no longer exists")
	}
	p := Plan{
		Title:     "Remove volume",
		JobTitle:  "remove volume",
		JobTarget: displayVolume(v),
		Target:    Target{Kind: "volume", Name: displayVolume(v)},
		Steps:     []Step{{Op: OpRemoveVolume, Kind: "volume", ID: v.Name, Name: displayVolume(v), Pin: v.CreatedAt}},
		Effects:   []string{"The volume and all data in it are permanently deleted."},
		Tier:      TierTyped,
		Word:      "remove",
	}
	if users := snap.VolumeUsers(v.Name); len(users) > 0 {
		p.Blockers = []string{
			fmt.Sprintf("In use by %s: %s.", plural(len(users), "container", "containers"), describeContainers(users)),
			"Docker Desk never forces a removal: remove those containers first.",
		}
	}
	return p, nil
}

func displayVolume(v engine.Volume) string {
	if len(v.Name) == 64 && isHex(v.Name) {
		return v.Name[:12] + "… (anonymous)"
	}
	return v.Name
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return s != ""
}

// VolumePrune plans removing unused volumes: anonymous ones only, or — with
// all — named ones too. A volume referenced by any container, running or
// stopped, is in use.
func VolumePrune(snap Snapshot, all bool) Plan {
	p := Plan{
		Title:    "Prune volumes",
		JobTitle: "prune volumes",
		Tier:     TierTyped,
		Word:     "prune",
		Listed:   true,
		Scope:    "anonymous volumes no container references",
	}
	if all {
		p.Scope = "all volumes no container references, named ones included"
		p.Warnings = []string{"Named volumes usually hold data that was kept on purpose. Read the list."}
	}
	vols := append([]engine.Volume(nil), snap.Volumes...)
	sort.SliceStable(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	for _, v := range vols {
		if len(snap.VolumeUsers(v.Name)) > 0 {
			continue
		}
		if !all && !v.Anonymous() {
			continue
		}
		detail := "named volume"
		if v.Anonymous() {
			detail = "" // the display name already says so
		}
		p.Steps = append(p.Steps, Step{Op: OpRemoveVolume, Kind: "volume", ID: v.Name, Name: displayVolume(v), Detail: detail, Pin: v.CreatedAt})
	}
	if len(p.Steps) == 0 {
		p.Nothing = "Nothing to prune: no unused " + map[bool]string{false: "anonymous ", true: ""}[all] + "volumes."
		return p
	}
	p.Target = Target{Kind: "volumes", Name: plural(len(p.Steps), "volume", "volumes")}
	p.JobTarget = p.Target.Name
	p.Effects = []string{
		"Exactly the volumes listed below, and all data in them, are permanently deleted.",
		"One request each, never forced; anything that became in use is refused by the daemon and left alone.",
	}
	return p
}

// ---- networks -----------------------------------------------------------------------

// NetworkCreate plans creating a bridge network.
func NetworkCreate(snap Snapshot, input string) (Plan, error) {
	name := strings.TrimSpace(input)
	if err := validName("network", name, 1); err != nil {
		return Plan{}, err
	}
	for _, n := range snap.Networks {
		if n.Name == name {
			return Plan{}, fmt.Errorf("a network named %q already exists", name)
		}
	}
	return Plan{
		Title:     "Create network",
		JobTitle:  "create network",
		JobTarget: name,
		Target:    Target{Kind: "network", Name: name},
		Steps:     []Step{{Op: OpCreateNetwork, Kind: "network", ID: name, Name: name}},
		Effects:   []string{fmt.Sprintf("A bridge network %q is created; the engine assigns its subnet.", name)},
		Tier:      TierForm,
	}, nil
}

// NetworkRemove plans removing one network.
func NetworkRemove(snap Snapshot, id string) (Plan, error) {
	n, ok := snap.network(id)
	if !ok {
		return Plan{}, na("that network no longer exists")
	}
	p := Plan{
		Title:     "Remove network",
		JobTitle:  "remove network",
		JobTarget: n.Name,
		Target:    Target{Kind: "network", Name: n.Name, ID: engine.ShortID(n.ID)},
		Steps:     []Step{{Op: OpRemoveNetwork, Kind: "network", ID: n.ID, Name: n.Name, Detail: engine.ShortID(n.ID)}},
		Effects:   []string{"The network is deleted."},
		Tier:      TierTyped,
		Word:      "remove",
	}
	if n.Predefined() {
		p.Blockers = []string{fmt.Sprintf("%q is a predefined network; Docker does not allow removing it.", n.Name)}
		return p, nil
	}
	if users := snap.NetworkUsers(n); len(users) > 0 {
		p.Blockers = []string{
			fmt.Sprintf("Used by %s: %s.", plural(len(users), "container", "containers"), describeContainers(users)),
			"Stopped containers count: they would fail to start without it. Disconnect or remove them first.",
		}
	}
	return p, nil
}

// NetworkPrune plans removing custom networks no container references.
func NetworkPrune(snap Snapshot) Plan {
	p := Plan{
		Title:    "Prune networks",
		JobTitle: "prune networks",
		Tier:     TierTyped,
		Word:     "prune",
		Listed:   true,
		Scope:    "custom networks no container, running or stopped, references",
	}
	nets := append([]engine.Network(nil), snap.Networks...)
	sort.SliceStable(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	for _, n := range nets {
		if n.Predefined() || len(snap.NetworkUsers(n)) > 0 {
			continue
		}
		p.Steps = append(p.Steps, Step{Op: OpRemoveNetwork, Kind: "network", ID: n.ID, Name: n.Name, Detail: engine.ShortID(n.ID)})
	}
	if len(p.Steps) == 0 {
		p.Nothing = "Nothing to prune: every custom network is referenced by a container."
		return p
	}
	p.Target = Target{Kind: "networks", Name: plural(len(p.Steps), "network", "networks")}
	p.JobTarget = p.Target.Name
	p.Effects = []string{
		"Exactly the networks listed below are deleted, one request each.",
		"Unlike `docker network prune`, networks used only by stopped containers are kept.",
	}
	return p
}

func noAttach(n engine.Network) error {
	if n.Name == "host" || n.Name == "none" || n.Driver == "host" || n.Driver == "null" {
		return na("containers cannot be connected to or disconnected from the %q network", n.Name)
	}
	return nil
}

// ConnectCandidates returns the containers that can be connected to a
// network: those not already attached to it.
func ConnectCandidates(snap Snapshot, networkID string) ([]engine.Container, error) {
	n, ok := snap.network(networkID)
	if !ok {
		return nil, na("that network no longer exists")
	}
	if err := noAttach(n); err != nil {
		return nil, err
	}
	attached := map[string]bool{}
	for _, c := range snap.NetworkUsers(n) {
		attached[c.ID] = true
	}
	var out []engine.Container
	for _, c := range snap.Containers {
		if !attached[c.ID] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, na("every container is already connected to %s", n.Name)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DisconnectCandidates returns the containers attached to a network.
func DisconnectCandidates(snap Snapshot, networkID string) ([]engine.Container, error) {
	n, ok := snap.network(networkID)
	if !ok {
		return nil, na("that network no longer exists")
	}
	if err := noAttach(n); err != nil {
		return nil, err
	}
	out := snap.NetworkUsers(n)
	if len(out) == 0 {
		return nil, na("no container is connected to %s: nothing to disconnect", n.Name)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// NetworkConnect plans attaching one container to one network.
func NetworkConnect(snap Snapshot, networkID, containerID string) (Plan, error) {
	n, ok := snap.network(networkID)
	if !ok {
		return Plan{}, na("that network no longer exists")
	}
	if err := noAttach(n); err != nil {
		return Plan{}, err
	}
	c, ok := snap.container(containerID)
	if !ok {
		return Plan{}, na("that container no longer exists")
	}
	for _, u := range snap.NetworkUsers(n) {
		if u.ID == c.ID {
			return Plan{}, na("%s is already connected to %s", c.Name, n.Name)
		}
	}
	p := Plan{
		Title:     "Connect container to network",
		JobTitle:  "connect to " + n.Name,
		JobTarget: c.Name,
		Target:    Target{Kind: "network", Name: n.Name, ID: engine.ShortID(n.ID)},
		Steps: []Step{{
			Op: OpConnect, Kind: "network", ID: n.ID, Name: n.Name,
			Other: c.ID, OtherName: c.Name, Detail: engine.ShortID(c.ID),
		}},
		Effects: []string{
			fmt.Sprintf("Container %s (%s) is attached to network %s.", c.Name, engine.ShortID(c.ID), n.Name),
			"It gets an address there and can reach, and be reached by, the other containers on it.",
		},
		Tier: TierConfirm,
	}
	if !c.IsActive() {
		p.Effects = append(p.Effects, "The container is not running; the attachment takes effect when it starts.")
	}
	return p, nil
}

// NetworkDisconnect plans detaching one container from one network.
func NetworkDisconnect(snap Snapshot, networkID, containerID string) (Plan, error) {
	n, ok := snap.network(networkID)
	if !ok {
		return Plan{}, na("that network no longer exists")
	}
	if err := noAttach(n); err != nil {
		return Plan{}, err
	}
	c, ok := snap.container(containerID)
	if !ok {
		return Plan{}, na("that container no longer exists")
	}
	attached := false
	for _, u := range snap.NetworkUsers(n) {
		attached = attached || u.ID == c.ID
	}
	if !attached {
		return Plan{}, na("%s is not connected to %s", c.Name, n.Name)
	}
	p := Plan{
		Title:     "Disconnect container from network",
		JobTitle:  "disconnect from " + n.Name,
		JobTarget: c.Name,
		Target:    Target{Kind: "network", Name: n.Name, ID: engine.ShortID(n.ID)},
		Steps: []Step{{
			Op: OpDisconnect, Kind: "network", ID: n.ID, Name: n.Name,
			Other: c.ID, OtherName: c.Name, Detail: engine.ShortID(c.ID),
		}},
		Effects: []string{
			fmt.Sprintf("Container %s (%s) is detached from network %s.", c.Name, engine.ShortID(c.ID), n.Name),
			"Its connections over that network are dropped.",
		},
		Tier: TierConfirm,
	}
	if len(c.Networks) == 1 {
		p.Warnings = []string{"This is the container's only network: it will have no network at all."}
	}
	return p, nil
}

// ---- helpers ------------------------------------------------------------------------

func validName(kind, name string, min int) error {
	if name == "" {
		return fmt.Errorf("enter a name for the %s", kind)
	}
	for i, r := range name {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !alnum {
			return fmt.Errorf("a %s name must start with a letter or digit", kind)
		}
		if !alnum && r != '_' && r != '.' && r != '-' {
			return fmt.Errorf("a %s name may only contain letters, digits, '_', '.' and '-' (found %q)", kind, r)
		}
	}
	if len(name) < min {
		return fmt.Errorf("a %s name needs at least %d characters", kind, min)
	}
	return nil
}
