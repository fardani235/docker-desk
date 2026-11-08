package registry

import (
	"context"
	"fmt"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/secret"
)

func repo(t *testing.T, reg *fakeregistry.Registry, path string) Ref {
	t.Helper()
	r, err := ParseRepository(reg.Host() + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseRepository(t *testing.T) {
	for in, want := range map[string][3]string{
		"alpine":      {"registry-1.docker.io", "library/alpine", "alpine"},
		"alpine:3.20": {"registry-1.docker.io", "library/alpine", "alpine"},
		"ghcr.io/acme/api@sha256:" + strings.Repeat("a", 64): {"ghcr.io", "acme/api", "ghcr.io/acme/api"},
		"127.0.0.1:5000/acme/api:1":                          {"127.0.0.1:5000", "acme/api", "127.0.0.1:5000/acme/api"},
	} {
		r, err := ParseRepository(in)
		if err != nil || r.Host != want[0] || r.Path != want[1] || r.Repository() != want[2] || r.Tag != "" {
			t.Errorf("%s: %+v %v", in, r, err)
		}
	}
	if _, err := ParseRepository("Not A Name"); err == nil {
		t.Error("an invalid name parsed")
	}
}

// Paging follows the registry's Link, one request a page, on the same host
// and path, until the registry names no next page.
func TestListTagsFollowsLink(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("acme/api", "t000", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	var all []string
	for i := 1; i < 250; i++ {
		all = append(all, fmt.Sprintf("t%03d", i))
	}
	reg.AliasTags("acme/api", "t000", all...)
	c := New(Options{})
	r := repo(t, reg, "acme/api")

	var got []string
	cursor, pages := "", 0
	for {
		p, err := c.ListTags(context.Background(), r, Credential{}, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		got = append(got, p.Tags...)
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if pages != 3 || len(got) != 250 || got[0] != "t000" || got[249] != "t249" {
		t.Fatalf("%d pages, %d tags (%v…)", pages, len(got), got[:3])
	}
	reqs := reg.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests for 3 pages", len(reqs))
	}
	if reqs[0].Query != "n=100" || reqs[1].Query != "n=100&last=t099" || reqs[2].Query != "n=100&last=t199" {
		t.Errorf("queries: %q %q %q", reqs[0].Query, reqs[1].Query, reqs[2].Query)
	}
	for _, q := range reqs {
		if q.Method != "GET" || q.Path != "/v2/acme/api/tags/list" {
			t.Errorf("request %s %s", q.Method, q.Path)
		}
	}

	// A cursor for another repository is refused before a request.
	reg.Reset()
	if _, err := c.ListTags(context.Background(), r, Credential{}, "/v2/other/tags/list?n=100&last=x"); KindOf(err) != KindRefused || reg.Count() != 0 {
		t.Errorf("foreign cursor: %v, %d requests", err, reg.Count())
	}
}

func TestListTagsRefusesAnOffHostLink(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("acme/api", "a", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	var many []string
	for i := 0; i < 120; i++ {
		many = append(many, fmt.Sprintf("b%03d", i))
	}
	reg.AliasTags("acme/api", "a", many...)
	reg.SetLinkBase("http://127.0.0.2:1")
	p, err := New(Options{}).ListTags(context.Background(), repo(t, reg, "acme/api"), Credential{}, "")
	if err != nil || p.Next != "" || !p.LinkRefused || len(p.Tags) != 100 {
		t.Fatalf("off-host link: %+v %v", p, err)
	}
}

func TestListTagsDropsInvalidNames(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("x", "ok", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.AliasTags("x", "ok", "\x1b[31mred", ".dot")
	p, err := New(Options{}).ListTags(context.Background(), repo(t, reg, "x"), Credential{}, "")
	if err != nil || len(p.Tags) != 1 || p.Invalid != 2 {
		t.Fatalf("%+v %v", p, err)
	}
}

// A 401 never claims the repository is absent; a 404 does, because it says so.
func TestRepository401Wording(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("team/private", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	reg.Conceal("team/private")
	c := New(Options{})

	for _, cred := range []Credential{{}, {Username: "op", Password: secret.New("pw"), Source: "auths"}} {
		_, err := c.ListTags(context.Background(), repo(t, reg, "team/private"), cred, "")
		if KindOf(err) != KindUnauthorized || !strings.Contains(err.Error(), Unauthorized401) {
			t.Errorf("tags/list 401 with %q: %v", cred.Source, err)
		}
		if strings.Contains(err.Error(), "no such") || strings.Contains(err.Error(), "does not exist") {
			t.Errorf("a 401 claimed absence: %v", err)
		}
		_, err = c.Describe(context.Background(), repo(t, reg, "team/private").WithTag("1"), cred)
		if KindOf(err) != KindUnauthorized || !strings.Contains(err.Error(), Unauthorized401) {
			t.Errorf("manifest 401 with %q: %v", cred.Source, err)
		}
	}
	if !strings.Contains(Unauthorized401, "does not have this repository, or this credential is not allowed to see it") {
		t.Fatal("the sentence changed")
	}

	_, err := c.ListTags(context.Background(), repo(t, reg, "team/nothing"), Credential{}, "")
	if KindOf(err) != KindNotFound || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
}

// Describe is one manifest GET: an index's platforms, attestations left
// out, and never the child manifest or the config.
func TestDescribeIsOneRequest(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	idx := reg.Push("acme/api", "1", true,
		fakeregistry.Image{OS: "linux", Arch: "amd64"},
		fakeregistry.Image{OS: "linux", Arch: "arm64", Variant: "v8"})
	one := reg.Push("acme/api", "single", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	c := New(Options{})
	r := repo(t, reg, "acme/api")

	d, err := c.Describe(context.Background(), r.WithTag("1"), Credential{})
	if err != nil || d.Digest != idx.Digest || !d.Index || len(d.Platforms) != 2 || d.Platforms[1].Variant != "v8" {
		t.Fatalf("index: %+v %v", d, err)
	}
	if reg.Count() != 1 || !strings.HasSuffix(reg.Requests()[0].Path, "/manifests/1") {
		t.Fatalf("describe sent %d requests: %+v", reg.Count(), reg.Requests())
	}
	d, err = c.Describe(context.Background(), r.WithTag("single"), Credential{})
	if err != nil || d.Index || d.Digest != one.Digest || len(d.Platforms) != 0 {
		t.Fatalf("single: %+v %v", d, err)
	}
	if !PlatformMatches(ocispec.Platform{OS: "linux", Architecture: "arm64"}, ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}) {
		t.Error("arm64 does not match arm64/v8")
	}
}

func TestNoRegistryRefusesListing(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	c := New(Options{Disabled: true})
	if _, err := c.ListTags(context.Background(), repo(t, reg, "x"), Credential{}, ""); KindOf(err) != KindRefused || reg.Count() != 0 {
		t.Fatalf("--no-registry: %v", err)
	}
}
