package repository

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeregistry"
	"github.com/fardani235/docker-desk/internal/registry"
	"github.com/fardani235/docker-desk/internal/testguard"
)

func TestMain(m *testing.M) {
	testguard.Install()
	os.Exit(m.Run())
}

func TestNaturalOrder(t *testing.T) {
	tags := []string{"latest", "3.10", "3.9", "3.28.3", "3.28.10", "3.2", "10", "9", "3.28", "edge", "3.28.3-r1", "v1", "v01", "3.28.03", "1.0.0-rc.10", "1.0.0-rc.9", "1.0.0"}
	Sort(tags)
	want := []string{"1.0.0", "1.0.0-rc.9", "1.0.0-rc.10", "3.2", "3.9", "3.10", "3.28", "3.28.03", "3.28.3", "3.28.3-r1", "3.28.10", "9", "10", "edge", "latest", "v01", "v1"}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("natural order:\n got %v\nwant %v", tags, want)
	}
	// A total order: stable however the input is arranged.
	for i := 0; i < len(want); i++ {
		for j := 0; j < len(want); j++ {
			if Less(want[i], want[j]) != (i < j) {
				t.Errorf("Less(%q, %q) = %v", want[i], want[j], Less(want[i], want[j]))
			}
		}
	}
}

func page(tags []string, next string) registry.TagPage {
	return registry.TagPage{Tags: tags, Next: next, At: time.Now()}
}

func TestListingCap(t *testing.T) {
	l := NewListing(registry.Ref{Path: "x"})
	n := 0
	for p := 0; p < 9; p++ {
		var tags []string
		for i := 0; i < 100; i++ {
			tags = append(tags, fmt.Sprintf("t%04d", n))
			n++
		}
		l.Add(page(tags, "/v2/x/tags/list?n=100&last=more"))
		if !l.More() || l.Capped {
			t.Fatalf("after %d pages: more=%v capped=%v", l.Pages, l.More(), l.Capped)
		}
	}
	// The tenth page reaches the cap exactly, and the registry has more.
	var tenth []string
	for i := 0; i < 100; i++ {
		tenth = append(tenth, fmt.Sprintf("t%04d", n))
		n++
	}
	l.Add(page(tenth, "/v2/x/tags/list?n=100&last=t0999"))
	if len(l.Tags) != Cap || !l.Capped || l.More() || l.Next != "" {
		t.Fatalf("at the cap: %d held, capped=%v, more=%v", len(l.Tags), l.Capped, l.More())
	}

	// A registry that ignores n sends one page larger than the cap: the
	// rest of it is not held.
	big := NewListing(registry.Ref{Path: "x"})
	var all []string
	for i := 0; i < 1500; i++ {
		all = append(all, fmt.Sprintf("b%04d", i))
	}
	big.Add(page(all, ""))
	if len(big.Tags) != Cap || !big.Capped || big.More() || big.Tags[Cap-1] != "b0999" {
		t.Fatalf("oversized page: %d held, capped=%v, last %s", len(big.Tags), big.Capped, big.Tags[len(big.Tags)-1])
	}

	// Under the cap, with no next page: not capped, nothing more.
	small := NewListing(registry.Ref{Path: "x"})
	small.Add(page([]string{"3.10", "3.9", "3.9"}, ""))
	if small.Capped || small.More() || !reflect.DeepEqual(small.Tags, []string{"3.9", "3.10"}) {
		t.Fatalf("small: %+v", small)
	}
}

func TestJoinOutcomes(t *testing.T) {
	repo, _ := registry.ParseRepository("127.0.0.1:5000/acme/api")
	const (
		d1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		d2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
		d3 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
		d4 = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	)
	r := "127.0.0.1:5000/acme/api"
	images := []engine.Image{
		// 1.0 pulled, still what the tag names.
		{ID: "sha256:aaa", RepoTags: []string{r + ":1.0"}, RepoDigests: []string{r + "@" + d1}},
		// 2.0 pulled when it named d2; upstream it names d3 now.
		{ID: "sha256:bbb", RepoTags: []string{r + ":2.0"}, RepoDigests: []string{r + "@" + d2}},
		// d4 is here as "stable" and "prod" only.
		{ID: "sha256:ccc", RepoTags: []string{r + ":stable", r + ":prod"}, RepoDigests: []string{r + "@" + d4}},
		// Another repository records d3: it does not count.
		{ID: "sha256:ddd", RepoTags: []string{"other/api:2.0"}, RepoDigests: []string{"other/api@" + d3}},
		// Built here: carries 9.0 and records nothing.
		{ID: "sha256:eee", RepoTags: []string{r + ":9.0"}},
		// An image whose ID is d3 — never joined by ID.
		{ID: d3, RepoTags: []string{"elsewhere:1"}},
	}
	cases := []struct {
		tag, digest string
		want        string
	}{
		{"1.0", d1, "here"},
		{"2.0", d3, "here, the tag moved upstream"},
		{"4.0", d4, "same build as prod"},
		{"9.0", d1, "same build as 1.0"},
		{"9.0", d2, "same build as 2.0"},
		{"9.0", d3, "-"},
		{"5.0", d3, "-"},
	}
	for _, c := range cases {
		if got := JoinLocal(repo, c.tag, c.digest, images).Words(); got != c.want {
			t.Errorf("%s @ %s: %q, want %q", c.tag, c.digest[:12], got, c.want)
		}
	}
	// Pulled by digest: the containerd store lists the digest reference
	// among the tags, the classic store lists nothing; either way, here.
	for _, tags := range [][]string{{r + "@" + d3}, nil} {
		pinned := append(images, engine.Image{ID: "sha256:fff", RepoTags: tags, RepoDigests: []string{r + "@" + d3}})
		if got := JoinLocal(repo, "5.0", d3, pinned).Words(); got != "here" {
			t.Errorf("pulled by digest (%v): %q", tags, got)
		}
		// Here beats the tag having moved: the build the tag names is here.
		if got := JoinLocal(repo, "2.0", d3, pinned).Words(); got != "here" {
			t.Errorf("moved but pulled by digest: %q", got)
		}
	}
}

func TestDaemonPlatformAndNoBuild(t *testing.T) {
	for arch, want := range map[string]string{"x86_64": "linux/amd64", "aarch64": "linux/arm64", "armv7l": "linux/arm/v7", "s390x": "linux/s390x"} {
		if got := PlatformString(DaemonPlatform(engine.Info{Architecture: arch, OSType: "linux"})); got != want {
			t.Errorf("%s: %s, want %s", arch, got, want)
		}
	}
	if p := DaemonPlatform(engine.Info{}); p.Architecture != "" {
		t.Errorf("unknown daemon: %+v", p)
	}
	armOnly := registry.Description{Index: true, Platforms: []ocispec.Platform{{OS: "linux", Architecture: "arm64", Variant: "v8"}}}
	amd := DaemonPlatform(engine.Info{Architecture: "x86_64"})
	if !NoBuildFor(armOnly, amd) || NoBuildFor(armOnly, DaemonPlatform(engine.Info{Architecture: "aarch64"})) {
		t.Error("arm-only index")
	}
	if NoBuildFor(registry.Description{}, amd) || NoBuildFor(armOnly, ocispec.Platform{}) {
		t.Error("a single manifest, or an unknown daemon, claimed a missing build")
	}
}

// The build-time batch: up to three requests a tag, the platform named,
// stopped by a quota with what was read kept, stopped by cancellation.
func TestBuildTimesQuotaAndCancel(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var tags []string
	for i := 0; i < 6; i++ {
		tag := fmt.Sprintf("1.%d", i)
		c := created.Add(time.Duration(i) * 24 * time.Hour)
		reg.Push("acme/api", tag, true,
			fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &c, DiffIDs: []string{"sha256:" + tag}},
			fakeregistry.Image{OS: "linux", Arch: "arm64", Variant: "v8", Created: &c, DiffIDs: []string{"sha256:" + tag + "arm"}})
		tags = append(tags, tag)
	}
	epoch := time.Unix(0, 0)
	reg.Push("acme/api", "repro", false, fakeregistry.Image{OS: "linux", Arch: "amd64", Created: &epoch})
	reg.Push("acme/api", "armonly", true, fakeregistry.Image{OS: "linux", Arch: "arm64"})
	repo, _ := registry.ParseRepository(reg.Host() + "/acme/api")
	b := New(Options{})
	amd := ocispec.Platform{OS: "linux", Architecture: "amd64"}

	res := b.BuildTimes(context.Background(), repo, []string{"1.0", "repro", "armonly"}, amd, nil)
	if res.Read != 3 || res.Quota || res.Cancelled {
		t.Fatalf("result: %+v", res)
	}
	if n := reg.Count(); n > 3*3 {
		t.Errorf("%d requests for 3 tags", n)
	}
	if bt, _ := b.TimeOf(repo, "1.0"); !bt.Created.Equal(created) || bt.Platform != "linux/amd64" {
		t.Errorf("1.0: %+v", bt)
	}
	if bt, _ := b.TimeOf(repo, "repro"); !bt.Epoch || !bt.Created.IsZero() {
		t.Errorf("epoch: %+v", bt)
	}
	if bt, _ := b.TimeOf(repo, "armonly"); !bt.PlatformMissing || bt.Platform != "" {
		t.Errorf("arm only: %+v", bt)
	}
	if d, ok := b.Described(repo, "1.0"); !ok || !d.Index || len(d.Platforms) != 2 {
		t.Errorf("the batch did not keep the description: %+v", d)
	}

	// The quota: three requests allowed, so the first tag's three succeed
	// and the second tag's first is refused. What was read is kept.
	reg.Reset()
	reg.SetQuota(4)
	b2 := New(Options{})
	var seen []string
	res = b2.BuildTimes(context.Background(), repo, tags, amd, func(done int, tag string, _ BuildTime, err error) {
		seen = append(seen, fmt.Sprintf("%d %s %v", done, tag, err != nil))
	})
	if !res.Quota || res.Reset.IsZero() || res.Read != 1 || res.Failed != 1 {
		t.Fatalf("quota: %+v %v", res, seen)
	}
	if _, ok := b2.TimeOf(repo, "1.0"); !ok {
		t.Error("the answer read before the quota was dropped")
	}
	if _, ok := b2.TimeOf(repo, "1.2"); ok || reg.Count() != 4 {
		t.Errorf("the batch went on after the quota: %d requests", reg.Count())
	}

	// Cancelled: nothing after the cancellation is asked.
	reg.SetQuota(0)
	reg.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	b3 := New(Options{})
	res = b3.BuildTimes(ctx, repo, tags, amd, func(done int, _ string, _ BuildTime, _ error) {
		if done == 2 {
			cancel()
		}
	})
	if !res.Cancelled || res.Read != 2 {
		t.Fatalf("cancel: %+v", res)
	}
	if n := reg.Count(); n > 2*3 {
		t.Errorf("%d requests after cancelling at 2 tags", n)
	}
}

func TestBrowserDescribeKeepsAnswers(t *testing.T) {
	reg := fakeregistry.New()
	defer reg.Close()
	reg.Push("acme/api", "1", false, fakeregistry.Image{OS: "linux", Arch: "amd64"})
	repo, _ := registry.ParseRepository(reg.Host() + "/acme/api")
	b := New(Options{})
	if _, ok := b.Described(repo, "1"); ok {
		t.Fatal("described before asking")
	}
	d, err := b.Describe(context.Background(), repo, "1")
	if err != nil || !strings.HasPrefix(d.Digest, "sha256:") {
		t.Fatal(d, err)
	}
	if k := Key(repo, "1"); k != reg.Host()+"/acme/api:1" {
		t.Errorf("key %q", k)
	}
	if kept, ok := b.Described(repo, "1"); !ok || kept.Digest != d.Digest || kept.At.IsZero() {
		t.Errorf("kept: %+v", kept)
	}
	b.Forget(repo)
	if _, ok := b.Described(repo, "1"); ok {
		t.Error("Forget kept the answer")
	}
}
