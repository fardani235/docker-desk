package repository

import (
	"strings"

	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/registry"
)

// Here is how a remote build relates to the images on the daemon.
type Here int

const (
	// Absent: no local image of the repository records this digest, and
	// none carries the tag with a recorded digest.
	Absent Here = iota
	// Present: a local image records the digest, under this tag or under
	// no tag of the repository (pulled by digest).
	Present
	// TagMoved: a local image carries the tag, but records another digest:
	// the tag has moved upstream since it was pulled.
	TagMoved
	// SameBuild: a local image records the digest under another tag of the
	// repository (Join.Other).
	SameBuild
)

// Join is the HERE column's answer for one row.
type Join struct {
	Here Here
	// Other is the local tag that records the same build (SameBuild).
	Other string
	// ImageID is the local image the answer is about, for the detail line.
	ImageID string
}

// Words is the column's text.
func (j Join) Words() string {
	switch j.Here {
	case Present:
		return "here"
	case TagMoved:
		return "here, the tag moved upstream"
	case SameBuild:
		return "same build as " + j.Other
	}
	return "-"
}

// JoinLocal joins a remote digest with the in-memory image list. It compares
// the digest with the RepoDigests of local images of the same repository
// (Ref.SameRepository) — never with an image ID, which on the classic store
// is a different digest. An image built here, which records no digest,
// joins nothing.
func JoinLocal(repo registry.Ref, tag, digest string, images []engine.Image) Join {
	var moved, same, bare Join
	for _, img := range images {
		var digests []string
		for _, rd := range img.RepoDigests {
			if d, ok := repo.SameRepository(rd); ok {
				digests = append(digests, d)
			}
		}
		tags := repoTags(repo, img.RepoTags)
		records := contains(digests, digest)
		carries := contains(tags, tag)
		switch {
		case records && carries:
			return Join{Here: Present, ImageID: img.ID}
		case records && len(tags) == 0:
			bare = Join{Here: Present, ImageID: img.ID}
		case records:
			Sort(tags)
			if same.Here == Absent || Less(tags[0], same.Other) {
				same = Join{Here: SameBuild, Other: tags[0], ImageID: img.ID}
			}
		case carries && len(digests) > 0:
			moved = Join{Here: TagMoved, ImageID: img.ID}
		}
	}
	for _, j := range []Join{bare, moved, same} {
		if j.Here != Absent {
			return j
		}
	}
	return Join{}
}

// repoTags are the tags of repo among an image's RepoTags. A digest-only
// entry, which the containerd store lists there, is not a tag.
func repoTags(repo registry.Ref, refs []string) []string {
	var out []string
	for _, s := range refs {
		named, err := reference.ParseNormalizedNamed(s)
		if err != nil || named.Name() != repo.Name {
			continue
		}
		if t, ok := named.(reference.Tagged); ok {
			out = append(out, t.Tag())
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// DaemonPlatform is the daemon's platform as an index names it, from
// engine.Info: the daemon reports the kernel's machine name (x86_64,
// aarch64), an index the Go name (amd64, arm64). The zero platform when the
// daemon did not say.
func DaemonPlatform(info engine.Info) ocispec.Platform {
	arch, variant := "", ""
	switch strings.ToLower(info.Architecture) {
	case "":
		return ocispec.Platform{}
	case "x86_64", "x86-64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64", "armv8", "armv8l":
		arch = "arm64"
	case "armv7l", "armv7", "armhf":
		arch, variant = "arm", "v7"
	case "armv6l", "armv6", "armel":
		arch, variant = "arm", "v6"
	case "i386", "i486", "i586", "i686", "386":
		arch = "386"
	default:
		arch = strings.ToLower(info.Architecture) // ppc64le, s390x, riscv64 are named alike
	}
	os := strings.ToLower(info.OSType)
	if os == "" {
		os = "linux"
	}
	return ocispec.Platform{OS: os, Architecture: arch, Variant: variant}
}

// PlatformString is os/arch[/variant].
func PlatformString(p ocispec.Platform) string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// NoBuildFor reports whether d is an index that publishes no build for
// daemon. A single manifest, or a daemon whose platform is not known, is
// never said to lack one: nothing was read that could say so.
func NoBuildFor(d registry.Description, daemon ocispec.Platform) bool {
	if !d.Index || daemon.Architecture == "" {
		return false
	}
	for _, p := range d.Platforms {
		if registry.PlatformMatches(daemon, p) {
			return false
		}
	}
	return true
}
