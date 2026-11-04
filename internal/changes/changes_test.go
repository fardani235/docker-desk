package changes

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
)

func TestFindPairRefusals(t *testing.T) {
	images := []engine.Image{
		{ID: "sha256:aaaaaaaaaaaaaaaa", RepoTags: []string{"app:1"}},
		{ID: "sha256:bbbbbbbbbbbbbbbb", RepoTags: []string{"other:2"}},
	}
	cases := []struct{ ref, running, code, newID string }{
		{"app:1", "sha256:cccccccccccccccc", "", "sha256:aaaaaaaaaaaaaaaa"},
		{"docker.io/library/app:1", "sha256:cccccccccccccccc", "", "sha256:aaaaaaaaaaaaaaaa"},
		{"app@sha256:0123456789abcdef", "sha256:cccccccccccccccc", "pinned", ""},
		{"sha256:cccccccccccccccc", "sha256:cccccccccccccccc", "from-id", ""},
		{"cccccccccccc", "sha256:cccccccccccccccc", "from-id", ""},
		{"gone:3", "sha256:cccccccccccccccc", "unresolved", ""},
		{"app", "sha256:cccccccccccccccc", "unresolved", ""}, // app:latest is not app:1
		{"other:2", "sha256:bbbbbbbbbbbbbbbb", "same", ""},
	}
	for _, c := range cases {
		p := FindPair(c.ref, c.running, images)
		if p.Code != c.code || p.NewID != c.newID {
			t.Errorf("%s: got %q %q, want %q %q", c.ref, p.Code, p.NewID, c.code, c.newID)
		}
		if p.Code != "" && p.Text == "" {
			t.Errorf("%s: refusal without a reason", c.ref)
		}
	}
	if r := SamePlatform(engine.ImageFacts{OS: "linux", Architecture: "amd64"}, engine.ImageFacts{OS: "linux", Architecture: "arm64", Variant: "v8"}); r.Code != "platform" || !strings.Contains(r.Text, "linux/arm64/v8") {
		t.Errorf("platform: %+v", r)
	}
	if r := SamePlatform(engine.ImageFacts{OS: "linux", Architecture: "amd64"}, engine.ImageFacts{OS: "linux", Architecture: "amd64"}); !r.None() {
		t.Errorf("same platform refused: %+v", r)
	}
}

func hist(by ...string) []engine.HistoryEntry {
	out := []engine.HistoryEntry{{CreatedBy: "/bin/sh -c #(nop)  ENV A=1"}} // made no layer
	for _, b := range by {
		out = append(out, engine.HistoryEntry{CreatedBy: b, Size: 10})
	}
	return out
}

func TestLineage(t *testing.T) {
	old := engine.ImageFacts{DiffIDs: []string{"a", "b", "c"}, History: hist("ADD base", "RUN apt-get install x # buildkit", "COPY . /app")}
	added := engine.ImageFacts{DiffIDs: []string{"a", "b", "c", "d", "e"},
		History: hist("ADD base", "RUN apt-get install x # buildkit", "COPY . /app", "RUN |1 TOKEN=s3cr3t-value /bin/sh -c make", "/bin/sh -c #(nop) COPY file:x in /etc")}
	l := CompareLayers(old, added)
	if !l.BaseIntact() || l.Shared != 3 || len(l.NewOnly) != 2 || l.NewOnly[0].Index != 4 {
		t.Fatalf("added: %+v", l)
	}
	if got := l.NewOnly[0].Instruction; strings.Contains(got, "s3cr3t") || !strings.Contains(got, "make") || !strings.Contains(got, "TOKEN") {
		t.Errorf("a build argument's value reached the instruction: %q", got)
	}
	if l.NewOnly[1].Instruction != "COPY file:x in /etc" {
		t.Errorf("instruction: %q", l.NewOnly[1].Instruction)
	}

	diverged := engine.ImageFacts{DiffIDs: []string{"a", "x", "y"}, History: hist("ADD base", "RUN other", "COPY . /app")}
	l = CompareLayers(old, diverged)
	if l.BaseIntact() || l.Shared != 1 || len(l.OldOnly) != 2 || len(l.NewOnly) != 2 || l.OldOnly[0].Index != 2 {
		t.Fatalf("diverged: %+v", l)
	}

	// A history that cannot be mapped gives no instruction, and says why.
	unmapped := engine.ImageFacts{DiffIDs: []string{"a", "b", "c", "d"}, History: []engine.HistoryEntry{{CreatedBy: "x", Size: 1}}}
	l = CompareLayers(old, unmapped)
	if l.NewUnmapped == "" || l.NewOnly[0].Instruction != "" || l.NewOnly[0].Size != -1 || l.OldUnmapped != "" {
		t.Fatalf("unmapped: %+v", l)
	}
	// Nothing in common at all.
	if l := CompareLayers(old, engine.ImageFacts{DiffIDs: []string{"z"}}); l.Shared != 0 || len(l.OldOnly) != 3 {
		t.Fatalf("disjoint: %+v", l)
	}
}

func TestConfigNeverCarriesEnvValues(t *testing.T) {
	old := engine.ImageFacts{
		Cmd: []string{"app"}, User: "", WorkingDir: "/app", ExposedPorts: []string{"8000/tcp"},
		Labels:   map[string]string{"version": "1.4", "gone": "x", "same": "s"},
		EnvNames: engine.EnvNames([]string{"PATH=/bin", "DEBUG=1", "API_TOKEN=tok-old-s3cret"}),
	}
	new := engine.ImageFacts{
		Cmd: []string{"app", "--fonts"}, User: "report", WorkingDir: "/app", ExposedPorts: []string{"8000/tcp", "9090/tcp"},
		Healthcheck: []string{"CMD", "true"},
		Labels:      map[string]string{"version": "1.5", "same": "s", "new": "y"},
		EnvNames:    engine.EnvNames([]string{"PATH=/usr/bin", "API_TOKEN=tok-new-s3cret", "FONT_DIR=/f"}),
	}
	got := CompareConfig(old, new)
	var lines []string
	for _, c := range got {
		lines = append(lines, c.Field+" "+c.Op+" "+c.Name+" "+c.Old+" → "+c.New)
		if c.Field == "env" && (c.Old != "" || c.New != "") {
			t.Errorf("env change carries a value: %+v", c)
		}
	}
	all := strings.Join(lines, "\n")
	for _, bad := range []string{"s3cret", "/usr/bin", "/f"} {
		if strings.Contains(all, bad) {
			t.Errorf("an environment value %q appears:\n%s", bad, all)
		}
	}
	want := []string{
		`command changed  ["app"] → ["app", "--fonts"]`,
		`user added   → report`,
		`healthcheck added   → ["CMD", "true"]`,
		`port added 9090/tcp  → `,
		`label removed gone x → `,
		`label added new  → y`,
		`label changed version 1.4 → 1.5`,
		`env removed DEBUG  → `,
		`env added FONT_DIR  → `,
	}
	if strings.Join(want, "\n") != all {
		t.Errorf("config diff:\n%s\nwant:\n%s", all, strings.Join(want, "\n"))
	}
}

func report(pkgs []forensics.InstalledPackage, fs ...forensics.Finding) *forensics.Report {
	return &forensics.Report{
		Layers:   []forensics.Layer{{Index: 1}},
		Packages: &forensics.PackageSet{DBs: []forensics.InstalledDB{{Ecosystem: "deb", Manager: "dpkg", State: forensics.DBRead, Packages: pkgs, Total: len(pkgs)}}},
		Findings: fs,
	}
}

func TestContent(t *testing.T) {
	p := func(n, v string) forensics.InstalledPackage {
		return forensics.InstalledPackage{Name: n, Version: v, Arch: "amd64", Layer: 2}
	}
	root := forensics.Finding{Rule: "config.root-user", Group: forensics.GroupConfig, Evidence: `User ""`}
	share := func(e string) forensics.Finding {
		return forensics.Finding{Rule: "layer.size-share", Group: forensics.GroupLayer, Layer: 2, Evidence: e, Instruction: "RUN apt"}
	}
	key := forensics.Finding{Rule: "secret.private-key", Group: forensics.GroupSecrets, Layer: 5, Path: "/root/.ssh/id_rsa"}
	old := report([]forensics.InstalledPackage{p("libc6", "2.36-9"), p("openssl", "3.0.11"), p("gone", "1")}, root, share("10 MB of 20 MB (50%)"))
	new := report([]forensics.InstalledPackage{p("libc6", "2.36-9"), p("openssl", "3.0.15"), p("curl", "7.88"), {Name: "gpg-pubkey", Pseudo: true}}, share("10 MB of 25 MB (40%)"), key)
	c := CompareContent(old, new)
	if len(c.Added) != 1 || c.Added[0].Name != "curl" || len(c.Removed) != 1 || c.Removed[0].Name != "gone" ||
		len(c.Changed) != 1 || c.Changed[0].Old != "3.0.11" || c.Changed[0].New != "3.0.15" {
		t.Fatalf("packages: %+v", c)
	}
	if len(c.Appeared) != 1 || c.Appeared[0].Rule != "secret.private-key" || len(c.Disappeared) != 1 || c.Disappeared[0].Rule != "config.root-user" {
		t.Fatalf("findings: appeared %+v disappeared %+v", c.Appeared, c.Disappeared)
	}
	if len(c.Compared) != 1 || len(c.Unknowns) != 0 {
		t.Fatalf("compared %v unknowns %v", c.Compared, c.Unknowns)
	}

	// An unknown database is said, with its reason, and not compared.
	unk := report(nil)
	unk.Packages.DBs[0] = forensics.InstalledDB{Ecosystem: "deb", Manager: "dpkg", State: forensics.DBUnknown, Unknown: "layer 3 is zstd-compressed"}
	c = CompareContent(old, unk)
	if len(c.Added)+len(c.Removed)+len(c.Changed) != 0 || len(c.Unknowns) != 1 || !strings.Contains(c.Unknowns[0], "zstd") {
		t.Fatalf("unknown db: %+v", c)
	}

	// An unreadable layer on either side refuses the comparison.
	bad := report(nil)
	bad.Layers = []forensics.Layer{{Index: 1}, {Index: 2, Unreadable: "zstd compression is not read"}}
	if r := Unreadable(old, bad); r.Code != "unreadable" || !strings.Contains(r.Text, "Layer 2 of the new image") {
		t.Fatalf("unreadable: %+v", r)
	}
	if r := Unreadable(old, new); !r.None() {
		t.Fatalf("readable refused: %+v", r)
	}
}
