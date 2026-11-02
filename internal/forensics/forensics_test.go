package forensics

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

func analyse(t *testing.T, b fixture.Built, opts Options) *Report {
	t.Helper()
	if opts.ImageID == "" {
		opts.ImageID = b.ID
	}
	rep, err := Analyse(context.Background(), bytes.NewReader(b.Archive), opts, nil)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	return rep
}

func find(rep *Report, rule, path string) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Rule == rule && (path == "" || f.Path == path) {
			out = append(out, f)
		}
	}
	return out
}

func one(t *testing.T, rep *Report, rule, path string) Finding {
	t.Helper()
	fs := find(rep, rule, path)
	if len(fs) != 1 {
		t.Fatalf("want one %s finding for %q, got %d:\n%s", rule, path, len(fs), dump(rep))
	}
	return fs[0]
}

func dump(rep *Report) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		fmt.Fprintf(&b, "  L%d %-8s %-20s %-40s %s | %s\n", f.Layer, f.Certainty, f.Rule, f.Path, f.Evidence, f.Reason)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}

var layouts = []struct {
	name  string
	build func(fixture.Image) fixture.Built
}{
	{"legacy", fixture.Legacy},
	{"oci", fixture.OCI},
}

// TestDeletedSecretIsStillInTheImageBytes is the case that motivates the
// tab: a credential added in one layer and deleted in a later one is gone
// from every container's filesystem and still in every copy of the image.
func TestDeletedSecretIsStillInTheImageBytes(t *testing.T) {
	for _, lay := range layouts {
		t.Run(lay.name, func(t *testing.T) {
			rep := analyse(t, lay.build(fixture.Demo()), Options{})
			fs := find(rep, "secret.deleted", "/root/.aws/credentials")
			if len(fs) != 2 {
				t.Fatalf("want the key ID and the secret key reported as deleted, got %d:\n%s", len(fs), dump(rep))
			}
			for _, f := range fs {
				if f.Layer != 2 || f.Source != SourceImage || f.Certainty != Certain {
					t.Errorf("finding: %+v", f)
				}
				if !strings.Contains(f.Reason, "still in the image bytes") || !strings.Contains(f.Reason, "deleted in layer 3") {
					t.Errorf("reason does not say the bytes remain: %q", f.Reason)
				}
				if f.Instruction != "COPY . /root" {
					t.Errorf("instruction = %q, want the layer's COPY", f.Instruction)
				}
				if !f.Secret.Present() || f.Unknown == "" {
					t.Errorf("no revealable value or no unknown: %+v", f)
				}
				for _, s := range fixture.DemoSecrets() {
					if strings.Contains(f.Evidence, s) {
						t.Errorf("evidence shows the secret: %q", f.Evidence)
					}
				}
			}
			if got := fs[0].Secret.Reveal() + fs[1].Secret.Reveal(); !strings.Contains(got, fixture.DemoAWSKeyID) || !strings.Contains(got, fixture.DemoAWSSecret) {
				t.Errorf("reveal returns %q", got)
			}
			ow := one(t, rep, "secret.overwritten", "/app/config.yml")
			if ow.Layer != 2 || !strings.Contains(ow.Reason, "Replaced in layer 4") {
				t.Errorf("overwritten: %+v", ow)
			}
			env := one(t, rep, "secret.file", "/app/.env")
			if env.Layer != 4 || !strings.Contains(env.Reason, "final filesystem") || rep.FinalSecrets != 1 {
				t.Errorf("present secret: %+v (final %d)", env, rep.FinalSecrets)
			}
			arg := one(t, rep, "secret.buildarg", "")
			if arg.Layer != 3 || arg.Certainty != Certain || !strings.HasPrefix(arg.Evidence, "NPM_TOKEN=") {
				t.Errorf("build arg: %+v", arg)
			}
			if strings.Contains(arg.Instruction, fixture.DemoNPMToken) || !strings.Contains(arg.Instruction, "build args: NPM_TOKEN") {
				t.Errorf("instruction must name the argument, never its value: %q", arg.Instruction)
			}
		})
	}
}

// TestBothLayoutsSayTheSame: the layout changes how the bytes are packed,
// not what is in them.
func TestBothLayoutsSayTheSame(t *testing.T) {
	keys := func(rep *Report) []string {
		var out []string
		for _, f := range rep.Findings {
			if f.Group != GroupSupply {
				out = append(out, f.Key())
			}
		}
		return out
	}
	legacy := analyse(t, fixture.Legacy(fixture.Demo()), Options{})
	oci := analyse(t, fixture.OCI(fixture.Demo()), Options{})
	if legacy.Layout != "docker-archive" || oci.Layout != "oci" {
		t.Fatalf("layouts: %q %q", legacy.Layout, oci.Layout)
	}
	a, b := strings.Join(keys(legacy), "\n"), strings.Join(keys(oci), "\n")
	if a != b {
		t.Fatalf("legacy:\n%s\noci:\n%s", dump(legacy), dump(oci))
	}
	for _, rep := range []*Report{legacy, oci} {
		if len(rep.Layers) != 4 {
			t.Fatalf("layers: %+v", rep.Layers)
		}
		for _, l := range rep.Layers {
			if !l.Verified {
				t.Errorf("%s layer %d does not verify against its diff_id: %+v", rep.Layout, l.Index, l)
			}
		}
	}
	if oci.Layers[3].Compression != "gzip" || legacy.Layers[3].Compression != "none" {
		t.Errorf("compression: %q %q", oci.Layers[3].Compression, legacy.Layers[3].Compression)
	}
	// Order inside the archive does not matter either.
	img := fixture.Demo()
	img.ManifestFirst = true
	first := analyse(t, fixture.Legacy(img), Options{})
	if strings.Join(keys(first), "\n") != a {
		t.Errorf("manifest-first archive differs:\n%s", dump(first))
	}
}

func TestAttestationsAndAbsentBlobs(t *testing.T) {
	rep := analyse(t, fixture.OCI(fixture.Demo()), Options{})
	sbom := one(t, rep, "supply.sbom", "")
	if sbom.Evidence != "SBOM present: SPDX-2.3, 12 packages" || len(sbom.Detail) != 0 {
		t.Errorf("a present SBOM is one line: %+v", sbom)
	}
	one(t, rep, "supply.provenance", "")
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "other manifest(s) whose blobs are not in the archive") {
		t.Errorf("the absent arm64 manifest is not noted: %v", rep.Notes)
	}

	img := fixture.Demo()
	img.MissingAttestationBlobs = true
	img.OtherPlatforms = []string{"linux/arm64", "linux/arm/v7", "windows/amd64"}
	rep = analyse(t, fixture.OCI(img), Options{})
	sbom = one(t, rep, "supply.sbom", "")
	if !strings.Contains(sbom.Evidence, "not in the archive") {
		t.Errorf("an absent SBOM blob must be said: %q", sbom.Evidence)
	}
	if len(find(rep, "secret.deleted", "")) != 2 {
		t.Errorf("skipping absent blobs lost the rest of the analysis:\n%s", dump(rep))
	}

	legacy := analyse(t, fixture.Legacy(fixture.Demo()), Options{})
	att := one(t, legacy, "supply.attestations", "")
	if att.Certainty != Hint || !strings.Contains(att.Unknown, "registry") {
		t.Errorf("classic layout: %+v", att)
	}
}

func TestIndexOnlyArchive(t *testing.T) {
	b := fixture.OCI(fixture.Demo())
	// Rewrite the archive without manifest.json: index.json is then the
	// only way to the config and the layers.
	b.Archive = dropEntry(t, b.Archive, "manifest.json")
	rep := analyse(t, b, Options{Platform: "linux/amd64"})
	if len(rep.Layers) != 4 || len(find(rep, "secret.deleted", "")) != 2 {
		t.Fatalf("index-only archive:\n%s", dump(rep))
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "no manifest.json") {
		t.Errorf("notes: %v", rep.Notes)
	}
}

func TestInstructionsAreOmittedWhenTheHistoryDoesNotMap(t *testing.T) {
	img := fixture.Demo()
	img.History = img.History[:len(img.History)-2] // one layer-making entry short
	rep := analyse(t, fixture.Legacy(img), Options{})
	for _, f := range rep.Findings {
		if f.Instruction != "" && f.Rule != "secret.buildarg" {
			t.Errorf("%s has an instruction although the history does not map: %q", f.Rule, f.Instruction)
		}
	}
	for _, l := range rep.Layers {
		if l.Instruction != "" {
			t.Errorf("layer %d: %q", l.Index, l.Instruction)
		}
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "build instructions are not shown") {
		t.Errorf("notes: %v", rep.Notes)
	}
}

func TestConfigRules(t *testing.T) {
	rep := analyse(t, fixture.Legacy(fixture.Demo()), Options{DaemonOS: "linux", DaemonArch: "x86_64"})
	one(t, rep, "config.root-user", "")
	one(t, rep, "config.healthcheck", "")
	if len(find(rep, "config.platform", "")) != 0 || len(find(rep, "config.inert", "")) != 0 {
		t.Errorf("amd64 on x86_64 with a present command:\n%s", dump(rep))
	}
	if !rep.RootUser {
		t.Error("RootUser not set")
	}

	img := fixture.Plain("plain:1")
	img.Architecture = "arm64"
	rep = analyse(t, fixture.Legacy(img), Options{DaemonOS: "linux", DaemonArch: "x86_64"})
	p := one(t, rep, "config.platform", "")
	if p.Evidence != "image linux/arm64, daemon linux/amd64" {
		t.Errorf("platform: %q", p.Evidence)
	}
	if len(find(rep, "config.root-user", "")) != 0 || len(find(rep, "config.healthcheck", "")) != 0 {
		t.Errorf("a non-root image with a health check:\n%s", dump(rep))
	}

	img = fixture.Plain("plain:1")
	img.Cmd = []string{"sh"}
	if f := one(t, analyse(t, fixture.Legacy(img), Options{}), "config.inert", ""); f.Certainty != Likely {
		t.Errorf("bare shell: %+v", f)
	}
	img.Cmd = nil
	if f := one(t, analyse(t, fixture.Legacy(img), Options{}), "config.inert", ""); f.Certainty != Certain {
		t.Errorf("no command: %+v", f)
	}
	img.Cmd = []string{"/usr/bin/missing"}
	one(t, analyse(t, fixture.Legacy(img), Options{}), "config.inert", "/usr/bin/missing")
	// A command reached through a symbolic link exists.
	img.Cmd = []string{"/usr/local/bin/app"}
	img.Layers = append(img.Layers, fixture.Layer{Files: []fixture.File{{Path: "usr/local/bin", Type: '2', Link: "/bin"}}})
	if fs := find(analyse(t, fixture.Legacy(img), Options{}), "config.inert", ""); len(fs) != 0 {
		t.Errorf("a command behind a symlink was reported missing: %+v", fs)
	}
	img.Healthcheck = []string{"NONE"}
	if f := one(t, analyse(t, fixture.Legacy(img), Options{}), "config.healthcheck", ""); f.Evidence != "HEALTHCHECK NONE" {
		t.Errorf("healthcheck none: %+v", f)
	}
}

func TestFilesystemAndBinaryRules(t *testing.T) {
	rep := analyse(t, fixture.OCI(fixture.Demo()), Options{})
	if f := one(t, rep, "fs.setuid", "/usr/bin/passwd"); !strings.Contains(f.Evidence, "4755") || f.Layer != 1 {
		t.Errorf("setuid: %+v", f)
	}
	if f := one(t, rep, "fs.capabilities", "/usr/bin/ping"); !strings.Contains(f.Evidence, "cap_net_raw+ep") {
		t.Errorf("capabilities: %+v", f)
	}
	if f := one(t, rep, "fs.world-writable", "/var/spool/drop"); !strings.Contains(f.Reason, "sticky") {
		t.Errorf("world-writable: %+v", f)
	}
	one(t, rep, "bin.pie", "/bin/sh")
	one(t, rep, "bin.pie", "/app/tool")
	if f := one(t, rep, "bin.relro", "/app/server"); !strings.Contains(f.Reason, "Partial RELRO") {
		t.Errorf("relro: %+v", f)
	}
	one(t, rep, "bin.canary", "/app/server")
	one(t, rep, "bin.packed", "/app/helper")
	g := one(t, rep, "bin.go", "/app/tool")
	if g.Evidence != "go1.22.4 · example.com/desk/tool · 2 modules" || len(g.Detail) != 2 || g.Detail[1] != "github.com/spf13/cobra v1.8.0" {
		t.Errorf("go: %+v", g)
	}
	if len(find(rep, "bin.pie", "/usr/bin/ping"))+len(find(rep, "bin.relro", "/usr/bin/ping"))+len(find(rep, "bin.canary", "/usr/bin/ping")) != 0 {
		t.Errorf("a fully hardened binary was reported:\n%s", dump(rep))
	}
	if len(find(rep, "bin.canary", "/app/tool")) != 0 {
		t.Error("Go programs do not use the C stack protector; no canary finding")
	}
}

func TestLayerRules(t *testing.T) {
	rep := analyse(t, fixture.Legacy(fixture.Demo()), Options{Thresholds: Thresholds{SizeShare: 0.4, SizeMin: 1, FileCount: 7}})
	c := one(t, rep, "layer.cache", "/var/lib/apt/lists")
	if c.Layer != 2 || !strings.Contains(c.Reason, "removed in layer 3") || !strings.Contains(c.Reason, "stay in layer 2") {
		t.Errorf("cache cleaned too late: %+v", c)
	}
	one(t, rep, "layer.junk", "/app/.git")
	share := find(rep, "layer.size-share", "")
	if len(share) != 1 {
		t.Fatalf("size share:\n%s", dump(rep))
	}
	if fc := find(rep, "layer.file-count", ""); len(fc) != 1 || fc[0].Layer != 4 { // layer 4 adds 7 files
		t.Errorf("file count:\n%s", dump(rep))
	}
	for _, l := range rep.Layers {
		if l.Index == 3 && l.Whiteouts != 3 {
			t.Errorf("layer 3 whiteouts: %d", l.Whiteouts)
		}
	}
}

// TestSecretsNeverPrint: whatever formats a finding or a report — fmt in
// any verb, JSON — the value does not come out. Only Reveal returns it.
func TestSecretsNeverPrint(t *testing.T) {
	rep := analyse(t, fixture.OCI(fixture.Demo()), Options{})
	js, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{fmt.Sprintf("%v", rep), fmt.Sprintf("%+v", *rep), fmt.Sprintf("%#v", *rep), string(js), dump(rep)}
	for _, f := range rep.Findings {
		outputs = append(outputs, fmt.Sprintf("%v %+v %#v %s %q", f, f, f, f.Secret, f.Secret), f.Key())
	}
	for _, out := range outputs {
		for _, s := range fixture.DemoSecrets() {
			if strings.Contains(out, s) {
				t.Fatalf("secret %q printed in:\n%.400s", s, out)
			}
		}
	}
	revealed := 0
	for _, f := range rep.Findings {
		for _, s := range fixture.DemoSecrets() {
			if strings.Contains(f.Secret.Reveal(), s) {
				revealed++
			}
		}
	}
	if revealed < 5 {
		t.Errorf("only %d of the demo's secrets can be revealed on purpose", revealed)
	}
}

func TestBrokenInputFailsInItsOwnWords(t *testing.T) {
	_, err := Analyse(context.Background(), strings.NewReader("not a tar "+fixture.DemoAWSSecret+strings.Repeat("x", 2000)), Options{}, nil)
	var ae analysisError
	if !errors.As(err, &ae) || strings.Contains(err.Error(), fixture.DemoAWSSecret) {
		t.Fatalf("err = %v", err)
	}
	// A truncated archive fails without quoting it.
	b := fixture.Legacy(fixture.Demo())
	_, err = Analyse(context.Background(), bytes.NewReader(b.Archive[:len(b.Archive)/2]), Options{}, nil)
	if err == nil {
		t.Fatal("a truncated archive analysed")
	}
	for _, s := range fixture.DemoSecrets() {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error quotes a secret: %v", err)
		}
	}
	if _, err := Analyse(context.Background(), strings.NewReader(""), Options{}, nil); err == nil {
		t.Fatal("an empty answer analysed")
	}
}

type slowReader struct {
	r      io.Reader
	cancel func()
	n      int
}

func (s *slowReader) Read(p []byte) (int, error) {
	s.n++
	if s.n == 3 {
		s.cancel()
	}
	return s.r.Read(p[:min(len(p), 512)])
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := fixture.OCI(fixture.Demo())
	var progress []Progress
	_, err := Analyse(ctx, &slowReader{r: bytes.NewReader(b.Archive), cancel: cancel}, Options{}, func(p Progress) { progress = append(progress, p) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestProgressAndBounds(t *testing.T) {
	img := fixture.Demo()
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 MiB, larger than ScanFileMax below
	img.Layers = append(img.Layers, fixture.Layer{Files: []fixture.File{
		{Path: "data/blob.bin", Body: big},
		{Path: "data/big-elf", Body: append(fixture.ELF{GoVersion: "go1.23.0"}.Bytes(), big...), Mode: 0o755},
	}})
	img.History = nil
	b := fixture.OCI(img)
	lim := DefaultLimits()
	lim.ELFPrefix = 4096
	lim.ScanFileMax = 1 << 20
	lim.Paths = 10 // far fewer than the image has
	var last Progress
	rep, err := Analyse(context.Background(), bytes.NewReader(b.Archive), Options{Limits: lim}, func(p Progress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if last.BytesRead != int64(len(b.Archive)) || last.Layers != 5 || rep.BytesRead != int64(len(b.Archive)) {
		t.Errorf("progress %+v, archive %d bytes", last, len(b.Archive))
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "more paths than the desk keeps") {
		t.Errorf("the path bound is not noted: %v", rep.Notes)
	}
	// The Go marker sits past the ELF prefix; the streaming scanner finds it.
	if fs := find(rep, "bin.go", "/data/big-elf"); len(fs) != 1 {
		t.Errorf("Go build info past the prefix:\n%s", dump(rep))
	}
	// With paths uncounted, the command check says nothing rather than guess.
	if fs := find(rep, "config.inert", ""); len(fs) != 0 {
		t.Errorf("inert reported with incomplete path data: %+v", fs)
	}
}

func TestPerRuleCap(t *testing.T) {
	var files []fixture.File
	for i := 0; i < 40; i++ {
		files = append(files, fixture.File{Path: fmt.Sprintf("srv/w%02d", i), Body: []byte("x"), Mode: 0o666})
	}
	img := fixture.Plain("plain:1")
	img.Layers = append(img.Layers, fixture.Layer{Files: files})
	img.History = nil
	lim := DefaultLimits()
	lim.PerRule = 10
	rep, err := Analyse(context.Background(), bytes.NewReader(fixture.Legacy(img).Archive), Options{Limits: lim}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ww := find(rep, "fs.world-writable", "")
	if len(ww) != 11 || ww[len(ww)-1].Evidence != "30 more" {
		t.Fatalf("cap: %d findings\n%s", len(ww), dump(rep))
	}
}

func TestSortOrders(t *testing.T) {
	rep := analyse(t, fixture.OCI(fixture.Demo()), Options{})
	fs := append([]Finding{}, rep.Findings...)
	for o := OrderLayer; o < orderCount; o++ {
		Sort(fs, o)
		for i := 1; i < len(fs); i++ {
			a, b := fs[i-1], fs[i]
			switch o {
			case OrderLayer:
				if a.Layer > b.Layer {
					t.Fatalf("layer order: %d before %d", a.Layer, b.Layer)
				}
			case OrderGroup:
				if groupOrder[a.Group] > groupOrder[b.Group] {
					t.Fatalf("group order: %s before %s", a.Group, b.Group)
				}
			case OrderCertainty:
				if a.Certainty > b.Certainty {
					t.Fatalf("certainty order: %s before %s", a.Certainty, b.Certainty)
				}
			}
		}
	}
	if OrderCertainty.Next() != OrderLayer {
		t.Error("o does not cycle back to layer order")
	}
}

func TestLiveFindings(t *testing.T) {
	rep := analyse(t, fixture.Legacy(fixture.Demo()), Options{})
	img := engine.Image{ID: "sha256:aaa", RepoTags: []string{"shop/api:dev"}}
	newer := engine.Image{ID: "sha256:bbb", RepoTags: []string{"shop/api:next"}, RepoDigests: []string{"shop/api@sha256:1"}}
	in := LiveInput{
		Image:  img,
		Images: []engine.Image{img, newer},
		Containers: []engine.Container{
			{ID: "c1", Name: "api", ImageID: "sha256:aaa", Image: "shop/api:dev", State: "running",
				Ports: []engine.Port{{HostIP: "0.0.0.0", HostPort: 3000, PrivatePort: 3000, Proto: "tcp"}, {HostIP: "127.0.0.1", HostPort: 9000, PrivatePort: 9000, Proto: "tcp"}}},
			{ID: "c2", Name: "edge", ImageID: "sha256:aaa", Image: "shop/api:dev", State: "exited", NetworkMode: "host"},
			{ID: "c3", Name: "old", ImageID: "sha256:aaa", Image: "sha256:aaa", State: "exited"},
			{ID: "c4", Name: "other", ImageID: "sha256:ccc", Image: "shop/api:dev", State: "running"},
		},
		Facts: map[string]ContainerFacts{
			"c1": {Privileged: true},
			"c3": {Ref: "shop/api:next"}, // the list shows an ID; inspect keeps the ref
		},
	}
	live := LiveFindings(rep, in)
	byRule := map[string][]Finding{}
	for _, f := range live {
		byRule[f.Rule] = append(byRule[f.Rule], f)
	}
	if p := byRule["live.privileged"]; len(p) != 1 || p[0].Source != SourceDesk || !strings.Contains(p[0].Reason, "root") {
		t.Errorf("privileged root: %+v", p)
	}
	if h := byRule["live.host-network"]; len(h) != 1 || h[0].Subject != "edge" {
		t.Errorf("host network: %+v", h)
	}
	if p := byRule["live.published"]; len(p) != 1 || p[0].Evidence != "api: 3000→3000/tcp" {
		t.Errorf("published: %+v", p)
	}
	if u := byRule["supply.tag-unpinned"]; len(u) != 3 {
		t.Errorf("unpinned: %+v", u)
	}
	moved := byRule["supply.tag-moved"]
	if len(moved) != 2 {
		t.Fatalf("moved: %+v", moved)
	}
	if moved[0].Subject != "old" || moved[1].Subject != "other" {
		t.Errorf("moved: %+v", moved)
	}
	if s := byRule["live.secret-exposed"]; len(s) != 1 || !strings.Contains(s[0].Evidence, "api") {
		t.Errorf("secret exposed: %+v", s)
	}
	if d := byRule["supply.no-digest"]; len(d) != 1 {
		t.Errorf("no digest: %+v", d)
	}
	if users := Users(in); len(users) != 3 {
		t.Errorf("users: %d", len(users))
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a\x1b[31mb\nc\u2028d", 100); got != "a?[31mb?c?d" {
		t.Errorf("sanitize = %q", got)
	}
	if got := maskValue("AKIAQX7DESKTESTK3Y9Z", 4); got != "AKIA************ (20 chars)" {
		t.Errorf("mask = %q", got)
	}
	if got := maskText("curl -H token=" + fixture.DemoAPIToken); strings.Contains(got, fixture.DemoAPIToken) {
		t.Errorf("maskText = %q", got)
	}
}

// dropEntry rewrites an archive without one entry.
func dropEntry(t *testing.T, archive []byte, name string) []byte {
	t.Helper()
	var out bytes.Buffer
	tr := tar.NewReader(bytes.NewReader(archive))
	tw := tar.NewWriter(&out)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == name {
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestPackagedExamplesAreHints: the generic NAME=value rule, in a file a
// package ships, is only a hint; a credential-shaped value is not softened.
func TestPackagedExamplesAreHints(t *testing.T) {
	img := fixture.Plain("plain:1")
	img.Layers = append(img.Layers, fixture.Layer{Files: []fixture.File{
		{Path: "etc/ssl/openssl.cnf", Body: []byte("[ cmp ]\nsecret = pass:insta\n")},
		{Path: "usr/share/app/key.txt", Body: []byte("token = " + fixture.DemoAPIToken + "\n")},
		{Path: "srv/app/.env", Body: []byte("API_TOKEN=abcdef123456\n")},
	}})
	img.History = nil
	rep := analyse(t, fixture.Legacy(img), Options{})
	if f := one(t, rep, "secret.file", "/etc/ssl/openssl.cnf"); f.Certainty != Hint {
		t.Errorf("packaged example: %+v", f)
	}
	if f := one(t, rep, "secret.file", "/usr/share/app/key.txt"); f.Certainty != Certain {
		t.Errorf("a GitHub token in a packaged path: %+v", f)
	}
	if f := one(t, rep, "secret.file", "/srv/app/.env"); f.Certainty != Likely {
		t.Errorf("an application's .env: %+v", f)
	}
}

// BenchmarkAnalyse reads an archive whose layer holds 2,000 ordinary text
// files of 32 KiB — the common case, where no secret pattern's literal
// occurs and the prefilter skips every regular expression.
func BenchmarkAnalyse(b *testing.B) {
	text := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog; config value = 42\n", 520))[:32<<10]
	var files []fixture.File
	for i := 0; i < 2000; i++ {
		files = append(files, fixture.File{Path: fmt.Sprintf("usr/share/doc/pkg%04d/README", i), Body: text})
	}
	img := fixture.Plain("bench:1")
	img.Layers = append(img.Layers, fixture.Layer{Files: files})
	img.History = nil
	archive := fixture.Legacy(img).Archive
	b.SetBytes(int64(len(archive)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Analyse(context.Background(), bytes.NewReader(archive), Options{}, nil); err != nil {
			b.Fatal(err)
		}
	}
}
