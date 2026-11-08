package fakeengine

import (
	"fmt"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeregistry"
)

// SandboxRepository is the repository the sandbox's registry serves.
const SandboxRepository = "acme/api"

// SeedRepository fills reg with a repository worth browsing and gives this
// daemon three images of it, so that the repository browser (B) shows every
// answer its HERE column has:
//
//   - releases from 3.9.0 to 3.28.3, built for amd64 and arm64/v8, with 3.28,
//     3 and latest naming 3.28.3; edge; and 3.29.0-rc.1, built for arm64 only;
//   - nightly-20260701 … nightly-20260930, so the list is longer than one page;
//   - here: acme/api:3.28.2, as pulled;
//   - here, the tag moved upstream: acme/api:3.28, pulled when it named an
//     older build;
//   - same build as stable: acme/api:stable, which is what 3.27.0 names.
//
// It returns the repository as the desk names it (127.0.0.1:port/acme/api).
func (s *Server) SeedRepository(reg *fakeregistry.Registry) string {
	day := func(y int, m time.Month, d int) *time.Time {
		t := time.Date(y, m, d, 9, 30, 0, 0, time.UTC)
		return &t
	}
	both := func(tag string, created *time.Time) fakeregistry.Pushed {
		return reg.Push(SandboxRepository, tag, true,
			fakeregistry.Image{OS: "linux", Arch: "amd64", Created: created, DiffIDs: []string{"sha256:" + stableID("base", "3"), "sha256:" + stableID("api", tag)}},
			fakeregistry.Image{OS: "linux", Arch: "arm64", Variant: "v8", Created: created, DiffIDs: []string{"sha256:" + stableID("base", "3-arm"), "sha256:" + stableID("api-arm", tag)}})
	}
	// 3.28 named an older build first; the daemon pulled it then.
	old := both("3.28", day(2026, 8, 3))

	releases := []struct {
		tag  string
		when *time.Time
	}{
		{"3.9.0", day(2025, 3, 4)}, {"3.10.0", day(2025, 5, 12)}, {"3.10.1", day(2025, 6, 2)},
		{"3.27.0", day(2026, 6, 15)}, {"3.28.0", day(2026, 8, 3)}, {"3.28.1", day(2026, 8, 27)},
		{"3.28.2", day(2026, 9, 21)}, {"3.28.3", day(2026, 10, 20)}, {"edge", day(2026, 11, 6)},
	}
	pushed := map[string]fakeregistry.Pushed{}
	for _, r := range releases {
		pushed[r.tag] = both(r.tag, r.when)
	}
	reg.AliasTags(SandboxRepository, "3.28.3", "3.28", "3", "latest")
	reg.Push(SandboxRepository, "3.29.0-rc.1", true,
		fakeregistry.Image{OS: "linux", Arch: "arm64", Variant: "v8", Created: day(2026, 11, 2), DiffIDs: []string{"sha256:" + stableID("api-arm", "rc")}})
	for d := time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC); d.Month() < 10; d = d.Add(24 * time.Hour) {
		at := d
		both(fmt.Sprintf("nightly-%s", d.Format("20060102")), &at)
	}

	repo := reg.Host() + "/" + SandboxRepository
	s.AddImageWithID(pushed["3.28.2"].Digest, 41_200_000, 47*24*time.Hour, repo+":3.28.2")
	s.AddImageWithID(old.Digest, 40_800_000, 97*24*time.Hour, repo+":3.28")
	s.AddImageWithID(pushed["3.27.0"].Digest, 39_900_000, 140*24*time.Hour, repo+":stable")
	return repo
}
