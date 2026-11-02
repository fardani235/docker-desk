package forensics

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
)

// ContainerFacts are what only inspecting a container tells: the list does
// not carry them.
type ContainerFacts struct {
	Privileged bool
	User       string
	// Ref is the reference the container was created from (Config.Image).
	// The list may show an image ID there instead once the tag has moved.
	Ref string
}

// refOf is the reference a container was created from, as best known.
func refOf(c engine.Container, facts map[string]ContainerFacts) string {
	if f, ok := facts[c.ID]; ok && f.Ref != "" {
		return f.Ref
	}
	return c.Image
}

// LiveInput is the daemon's state at one moment.
type LiveInput struct {
	Image      engine.Image // the analysed image, as listed now
	Images     []engine.Image
	Containers []engine.Container
	// Facts holds inspect results by container ID; a container missing
	// from it has not been inspected (yet), and nothing is said about it.
	Facts map[string]ContainerFacts
}

// Users returns the containers created from the image, by name.
func Users(in LiveInput) []engine.Container {
	var out []engine.Container
	for _, c := range in.Containers {
		if c.ImageID == in.Image.ID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pinned reports whether a reference names a digest.
func pinned(ref string) bool { return strings.Contains(ref, "@sha256:") }

// resolveRef returns the image a reference names now.
func resolveRef(images []engine.Image, ref string) (engine.Image, bool) {
	want := normalRef(ref)
	for _, img := range images {
		for _, t := range img.RepoTags {
			if normalRef(t) == want {
				return img, true
			}
		}
	}
	return engine.Image{}, false
}

// normalRef gives a reference the ":latest" it implies and drops the
// default registry, so that "nginx" and "docker.io/library/nginx:latest"
// compare equal.
func normalRef(ref string) string {
	r := strings.TrimPrefix(ref, "docker.io/")
	r = strings.TrimPrefix(r, "library/")
	if !strings.Contains(r, "@") {
		if c := strings.LastIndex(r, ":"); c <= strings.LastIndex(r, "/") {
			r += ":latest"
		}
	}
	return r
}

func allInterfaces(ip string) bool { return ip == "" || ip == "0.0.0.0" || ip == "::" }

// LiveFindings joins a report with the daemon's state. It is cheap and has
// no side effects, so the desk recomputes it every frame.
func LiveFindings(r *Report, in LiveInput) []Finding {
	var out []Finding
	users := Users(in)
	running := 0
	for _, c := range users {
		facts, inspected := in.Facts[c.ID]
		if c.IsActive() {
			running++
		}
		if inspected && facts.Privileged {
			f := Finding{Rule: "live.privileged", Group: GroupLive, Source: SourceDaemon, Certainty: Certain, Subject: c.Name,
				Evidence: c.Name + ": HostConfig.Privileged true (" + c.State + ")",
				Reason:   "Privileged: the container has every capability and the host's devices.",
				Unknown:  "what the processes inside do with that access"}
			if r != nil && r.RootUser && facts.User == "" {
				f.Source = SourceDesk
				f.Reason = "Privileged and running as root (no USER in the image, none given at run time): root inside is close to root on the host."
			}
			out = append(out, f)
		}
		if c.NetworkMode == "host" {
			out = append(out, Finding{Rule: "live.host-network", Group: GroupLive, Source: SourceDaemon, Certainty: Certain, Subject: c.Name,
				Evidence: c.Name + ": network mode host",
				Reason:   "Shares the host's network stack: whatever it listens on is open on the host.",
				Unknown:  "which ports its processes listen on"})
		}
		var open []string
		for _, p := range c.Ports {
			if p.HostPort != 0 && allInterfaces(p.HostIP) {
				open = append(open, fmt.Sprintf("%d→%d/%s", p.HostPort, p.PrivatePort, p.Proto))
			}
		}
		if len(open) > 0 {
			out = append(out, Finding{Rule: "live.published", Group: GroupLive, Source: SourceDaemon, Certainty: Certain, Subject: c.Name,
				Evidence: c.Name + ": " + strings.Join(open, ", "),
				Reason:   "Published on every host interface, not only on loopback.",
				Unknown:  "whether a firewall in front of the host filters it"})
		}
		ref := refOf(c, in.Facts)
		switch {
		case ref == "" || strings.HasPrefix(ref, "sha256:") || pinned(ref):
		default:
			out = append(out, Finding{Rule: "supply.tag-unpinned", Group: GroupSupply, Source: SourceDaemon, Certainty: Certain, Subject: c.Name,
				Evidence: c.Name + " was created from " + sanitize(ref, 120),
				Reason:   "A tag, not a digest: re-creating the container can give it different bytes.",
				Unknown:  "whether the tag is managed so that it never moves"})
			if now, ok := resolveRef(in.Images, ref); ok && now.ID != c.ImageID {
				out = append(out, Finding{Rule: "supply.tag-moved", Group: GroupSupply, Source: SourceDesk, Certainty: Certain, Subject: c.Name,
					Evidence: fmt.Sprintf("%s now names %s; %s runs %s", sanitize(ref, 80), engine.ShortID(now.ID), c.Name, engine.ShortID(c.ImageID)),
					Reason:   "The tag moved after the container was created: it runs bytes the tag no longer names.",
					Unknown:  "which of the two images is the intended one"})
			}
		}
	}
	// Containers created from a tag that has since moved onto this image.
	for _, c := range in.Containers {
		ref := refOf(c, in.Facts)
		if c.ImageID == in.Image.ID || ref == "" || pinned(ref) {
			continue
		}
		if now, ok := resolveRef(in.Images, ref); ok && now.ID == in.Image.ID {
			out = append(out, Finding{Rule: "supply.tag-moved", Group: GroupSupply, Source: SourceDesk, Certainty: Certain, Subject: c.Name,
				Evidence: fmt.Sprintf("%s now names this image; %s runs %s", sanitize(ref, 80), c.Name, engine.ShortID(c.ImageID)),
				Reason:   "The tag moved onto this image after the container was created: the container does not run these bytes.",
				Unknown:  "which of the two images is the intended one"})
		}
	}
	if len(in.Image.RepoDigests) == 0 && in.Image.ID != "" {
		out = append(out, Finding{Rule: "supply.no-digest", Group: GroupSupply, Source: SourceDaemon, Certainty: Hint,
			Evidence: "no repository digest",
			Reason:   "Built or loaded here, or its registry digest was not kept: nothing ties it to a manifest in a registry.",
			Unknown:  "where the image came from"})
	}
	if r != nil && r.FinalSecrets > 0 && running > 0 {
		var names []string
		for _, c := range users {
			if c.IsActive() {
				names = append(names, c.Name)
			}
		}
		out = append(out, Finding{Rule: "live.secret-exposed", Group: GroupLive, Source: SourceDesk, Certainty: Likely,
			Evidence: fmt.Sprintf("%d secret(s) in the final filesystem; running: %s", r.FinalSecrets, strings.Join(names, ", ")),
			Reason:   "Secrets the image carries are readable inside containers running now.",
			Unknown:  "whether a volume mounted over the path hides the file in those containers"})
	}
	return out
}
