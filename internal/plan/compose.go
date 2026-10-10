package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Project is a group of containers that carry the same
// com.docker.compose.project label. Nothing else defines a project: no
// compose file is read. See docs/DECISIONS.md, D15.
type Project struct {
	Name        string
	Containers  []engine.Container
	Running     int
	WorkingDir  string
	ConfigFiles string
}

// Projects groups the snapshot's containers by compose project label.
func Projects(snap Snapshot) []Project {
	byName := map[string]*Project{}
	for _, c := range snap.Containers {
		name := c.Project()
		if name == "" {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &Project{Name: name}
			byName[name] = p
		}
		p.Containers = append(p.Containers, c)
		if c.IsActive() {
			p.Running++
		}
		if p.WorkingDir == "" {
			p.WorkingDir = c.Labels[engine.LabelComposeWorkingDir]
		}
		if p.ConfigFiles == "" {
			p.ConfigFiles = c.Labels[engine.LabelComposeConfig]
		}
	}
	out := make([]Project, 0, len(byName))
	for _, p := range byName {
		sort.SliceStable(p.Containers, func(i, j int) bool { return p.Containers[i].Name < p.Containers[j].Name })
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func project(snap Snapshot, name string) (Project, bool) {
	for _, p := range Projects(snap) {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// ProjectLeftovers returns the names of networks and volumes that carry the
// project's label. They are never removed with the project; the confirmation
// names them so the operator knows what stays.
func ProjectLeftovers(snap Snapshot, name string) (networks, volumes []string) {
	for _, n := range snap.Networks {
		if n.Labels[engine.LabelComposeProject] == name {
			networks = append(networks, n.Name)
		}
	}
	for _, v := range snap.Volumes {
		if v.Labels[engine.LabelComposeProject] == name {
			volumes = append(volumes, v.Name)
		}
	}
	sort.Strings(networks)
	sort.Strings(volumes)
	return networks, volumes
}

// ProjectStop plans stopping every running container of a project.
func ProjectStop(snap Snapshot, name string) (Plan, error) {
	proj, ok := project(snap, name)
	if !ok {
		return Plan{}, na("compose project %q no longer has any containers", name)
	}
	p := Plan{
		Title:     "Stop compose project",
		JobTitle:  "stop project",
		JobTarget: name,
		Tier:      TierConfirm,
		Listed:    true,
	}
	for _, c := range proj.Containers {
		if c.IsActive() {
			p.Steps = append(p.Steps, Step{Op: OpStop, Kind: "container", ID: c.ID, Name: c.Name, Detail: c.State})
		}
	}
	if len(p.Steps) == 0 {
		return Plan{}, na("nothing to stop: no container of project %q is running", name)
	}
	p.Target = Target{Kind: "compose project", Name: fmt.Sprintf("%s (%s)", name, plural(len(p.Steps), "running container", "running containers"))}
	p.Effects = []string{
		"Each container listed below is stopped: stop signal, then killed after its grace period.",
		"Containers are stopped one after another; nothing is removed.",
	}
	return p, nil
}

// ProjectRemove plans removing every container of a project. It is refused
// while any of them is running, and it never touches networks or volumes.
func ProjectRemove(snap Snapshot, name string) (Plan, error) {
	proj, ok := project(snap, name)
	if !ok {
		return Plan{}, na("compose project %q no longer has any containers", name)
	}
	p := Plan{
		Title:     "Remove compose project",
		JobTitle:  "remove project",
		JobTarget: name,
		Target:    Target{Kind: "compose project", Name: fmt.Sprintf("%s (%s)", name, plural(len(proj.Containers), "container", "containers"))},
		Tier:      TierTyped,
		Word:      "remove",
		Listed:    true,
	}
	var active []engine.Container
	for _, c := range proj.Containers {
		p.Steps = append(p.Steps, Step{Op: OpRemoveContainer, Kind: "container", ID: c.ID, Name: c.Name, Detail: c.State})
		if c.IsActive() {
			active = append(active, c)
		}
	}
	p.Effects = []string{"Each container listed below, and its writable layer, is permanently deleted."}
	nets, vols := ProjectLeftovers(snap, name)
	left := "Networks and volumes are not touched."
	if len(nets)+len(vols) > 0 {
		var parts []string
		if len(nets) > 0 {
			parts = append(parts, "networks "+strings.Join(nets, ", "))
		}
		if len(vols) > 0 {
			parts = append(parts, "volumes "+strings.Join(vols, ", "))
		}
		left = "Left in place (this is not `compose down`): " + strings.Join(parts, "; ") + "."
	}
	p.Effects = append(p.Effects, left)
	if len(active) > 0 {
		p.Blockers = []string{
			fmt.Sprintf("%s still running: %s.", plural(len(active), "container is", "containers are"), describeContainers(active)),
			"Docker Desk never force-removes: stop the project first.",
		}
	}
	return p, nil
}
