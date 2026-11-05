// Package registry is the desk's registry client: reference normalisation,
// credentials from the operator's Docker configuration, the Bearer and Basic
// challenges, and the walk from a tag to the config of one platform's
// build. It sends GET requests only and writes nothing to disk. See
// docs/DECISIONS.md, round eleven (U2, U4, U5, U7, U12).
package registry

import (
	"errors"
	"strings"

	"github.com/distribution/reference"
)

// DockerHubKey is the configuration key the Docker CLI files Docker Hub's
// credential under. It is not the host requests go to.
const DockerHubKey = "https://index.docker.io/v1/"

// dockerHubHost is where Docker Hub's registry API answers.
const dockerHubHost = "registry-1.docker.io"

// Ref is a tagged reference, normalised.
type Ref struct {
	// Name is the canonical name: docker.io/library/alpine.
	Name string
	// Domain is the reference's own domain: docker.io, ghcr.io, host:5000.
	Domain string
	// Host is where requests go: registry-1.docker.io for Docker Hub.
	Host string
	// Path is the repository as the registry knows it: library/alpine.
	Path string
	Tag  string
	// Key is the configuration key a credential is filed under.
	Key string
}

// String is the reference as the operator would type it.
func (r Ref) String() string { return reference.FamiliarString(mustNamed(r.Name)) + ":" + r.Tag }

func mustNamed(n string) reference.Named {
	named, err := reference.ParseNormalizedNamed(n)
	if err != nil {
		return nil
	}
	return named
}

// ErrNotApplicable means the reference has no tag to follow: it names a
// digest only. It is not a failure.
var ErrNotApplicable = errors.New("the reference names a digest, not a tag: there is no tag to follow")

// ParseRef normalises s. A reference with no tag and no digest is :latest,
// as the CLI reads it. A reference with a digest and no tag is
// ErrNotApplicable; one with both follows its tag.
func ParseRef(s string) (Ref, error) {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(s))
	if err != nil {
		return Ref{}, err
	}
	tag := ""
	if t, ok := named.(reference.Tagged); ok {
		tag = t.Tag()
	} else if _, ok := named.(reference.Digested); ok {
		return Ref{}, ErrNotApplicable
	} else {
		tag = "latest"
	}
	domain := reference.Domain(named)
	r := Ref{
		Name:   named.Name(),
		Domain: domain,
		Host:   domain,
		Path:   reference.Path(named),
		Tag:    tag,
		Key:    domain,
	}
	if domain == "docker.io" {
		r.Host, r.Key = dockerHubHost, DockerHubKey
	}
	return r, nil
}

// SameRepository reports whether repoDigest (name@sha256:…, as the daemon
// records it in RepoDigests) belongs to r's repository, and returns its
// digest.
func (r Ref) SameRepository(repoDigest string) (string, bool) {
	named, err := reference.ParseNormalizedNamed(repoDigest)
	if err != nil || named.Name() != r.Name {
		return "", false
	}
	d, ok := named.(reference.Digested)
	if !ok {
		return "", false
	}
	return d.Digest().String(), true
}
