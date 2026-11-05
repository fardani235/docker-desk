package registry

import (
	"errors"
	"testing"
)

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in                       string
		name, host, path, tag, k string
	}{
		{"alpine", "docker.io/library/alpine", "registry-1.docker.io", "library/alpine", "latest", DockerHubKey},
		{"alpine:3.20", "docker.io/library/alpine", "registry-1.docker.io", "library/alpine", "3.20", DockerHubKey},
		{"docker.io/library/alpine:3.20", "docker.io/library/alpine", "registry-1.docker.io", "library/alpine", "3.20", DockerHubKey},
		{"docker.io/acme/api:1", "docker.io/acme/api", "registry-1.docker.io", "acme/api", "1", DockerHubKey},
		{"ghcr.io/acme/api:v2", "ghcr.io/acme/api", "ghcr.io", "acme/api", "v2", "ghcr.io"},
		{"registry.internal:5000/api", "registry.internal:5000/api", "registry.internal:5000", "api", "latest", "registry.internal:5000"},
		{"localhost:5000/team/api:dev@sha256:" + sixtyFour, "localhost:5000/team/api", "localhost:5000", "team/api", "dev", "localhost:5000"},
	} {
		r, err := ParseRef(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if r.Name != tc.name || r.Host != tc.host || r.Path != tc.path || r.Tag != tc.tag || r.Key != tc.k {
			t.Errorf("%s: got %+v", tc.in, r)
		}
	}
	if _, err := ParseRef("alpine@sha256:" + sixtyFour); !errors.Is(err, ErrNotApplicable) {
		t.Errorf("digest-only: %v, want ErrNotApplicable", err)
	}
	if _, err := ParseRef("Not A Ref"); err == nil || errors.Is(err, ErrNotApplicable) {
		t.Errorf("garbage: %v", err)
	}
}

func TestSameRepository(t *testing.T) {
	r, _ := ParseRef("alpine:3.20")
	if d, ok := r.SameRepository("alpine@sha256:" + sixtyFour); !ok || d != "sha256:"+sixtyFour {
		t.Errorf("familiar repo digest: %q %v", d, ok)
	}
	if _, ok := r.SameRepository("ghcr.io/x/alpine@sha256:" + sixtyFour); ok {
		t.Error("another repository matched")
	}
}

const sixtyFour = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
