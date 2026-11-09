package report

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func sampleImage() Image {
	return Image{
		ToolVersion: "test",
		ID:          "sha256:aaaa", Reference: "demo:1", Platform: "linux/amd64", Layout: "oci",
		Config: Config{Present: true, User: "", EnvNames: []string{"PATH", "B", "A"}},
		Layers: []Layer{{Index: 2, Digest: "sha256:2"}, {Index: 1, Digest: "sha256:1"}},
		Findings: []Finding{
			{Rule: "secret.file", Group: "secrets", Source: "image", Certainty: "likely", Layer: 2, Path: "/app/.env", Evidence: "DB_PASSWORD=***", Reason: "r", Masked: true},
			{Rule: "config.root-user", Group: "config", Source: "image", Certainty: "certain", Evidence: "User \"\"", Reason: "r"},
			{Rule: "live.published", Group: "live", Source: "daemon", Certainty: "certain", Subject: "web", Evidence: "web: 80", Reason: "r"},
			{Rule: "fs.setuid", Group: "filesystem", Source: "image", Certainty: "certain", Layer: 1, Path: "/b", Evidence: "e", Reason: "r"},
			{Rule: "fs.setuid", Group: "filesystem", Source: "image", Certainty: "certain", Layer: 1, Path: "/a", Evidence: "e", Reason: "r"},
			{Rule: "x.odd", Group: "future", Source: "desk", Certainty: "hint", Path: "line\nbreak\tand tab", Evidence: "e", Reason: "r"},
		},
		Notes: []string{"z note", "a note"},
		Packages: &Packages{
			SBOM:      SBOM{Listed: true, Format: "SPDX-2.3", Total: 5, Kept: 5},
			Databases: []Database{{Ecosystem: "rpm", Manager: "rpm", State: "absent"}, {Ecosystem: "deb", Manager: "dpkg", State: "read", Path: "/var/lib/dpkg/status"}},
			Rows: []Package{
				{State: "both", Type: "deb", Declared: &Declared{Name: "zlib", Version: "1.2", PURL: "pkg:deb/debian/zlib@1.2", License: "Zlib"}, Installed: &Installed{Name: "zlib", Version: "1.2", Layer: 1}},
				{State: "not checkable", Type: "npm", Why: "only deb, apk and rpm", Declared: &Declared{Name: "left-pad", Version: "1.3.0", PURL: "pkg:npm/left-pad@1.3.0"}},
				{State: "not checkable", Type: "", Why: "no purl", Declared: &Declared{Name: "mystery"}},
				{State: "version differs", Type: "deb", Declared: &Declared{Name: "libssl3", Version: "3.0.11", PURL: "pkg:deb/debian/libssl3@3.0.11"}, Installed: &Installed{Name: "libssl3", Version: "3.0.15", Layer: 2}},
				{State: "undeclared", Type: "deb", Installed: &Installed{Name: "curl", Version: "7.88", Layer: 3, Source: "/var/lib/dpkg/status"}},
				{State: "both", Type: "deb", Declared: &Declared{Name: "zlib", Version: "1.1"}, Installed: &Installed{Name: "zlib", Version: "1.1"}},
			},
		},
	}
}

// shuffled returns img with every list in another order.
func shuffled(img Image, seed int64) Image {
	r := rand.New(rand.NewSource(seed))
	out := img
	out.Findings = append([]Finding(nil), img.Findings...)
	r.Shuffle(len(out.Findings), func(i, j int) { out.Findings[i], out.Findings[j] = out.Findings[j], out.Findings[i] })
	out.Layers = append([]Layer(nil), img.Layers...)
	r.Shuffle(len(out.Layers), func(i, j int) { out.Layers[i], out.Layers[j] = out.Layers[j], out.Layers[i] })
	out.Notes = append([]string(nil), img.Notes...)
	r.Shuffle(len(out.Notes), func(i, j int) { out.Notes[i], out.Notes[j] = out.Notes[j], out.Notes[i] })
	out.Config.EnvNames = append([]string(nil), img.Config.EnvNames...)
	r.Shuffle(len(out.Config.EnvNames), func(i, j int) {
		out.Config.EnvNames[i], out.Config.EnvNames[j] = out.Config.EnvNames[j], out.Config.EnvNames[i]
	})
	p := *img.Packages
	p.Rows = append([]Package(nil), p.Rows...)
	r.Shuffle(len(p.Rows), func(i, j int) { p.Rows[i], p.Rows[j] = p.Rows[j], p.Rows[i] })
	p.Databases = append([]Database(nil), p.Databases...)
	r.Shuffle(len(p.Databases), func(i, j int) { p.Databases[i], p.Databases[j] = p.Databases[j], p.Databases[i] })
	out.Packages = &p
	return out
}

func TestEveryFormatIgnoresInputOrder(t *testing.T) {
	img := sampleImage()
	for _, f := range Formats {
		want, err := Render(f, Document{Image: &img})
		if err != nil {
			t.Fatal(err)
		}
		for seed := int64(1); seed <= 20; seed++ {
			s := shuffled(img, seed)
			got, _ := Render(f, Document{Image: &s})
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: input order %d changed the output", f, seed)
			}
		}
	}
	// The caller's value is not reordered underneath it.
	if img.Findings[0].Rule != "secret.file" || img.Packages.Rows[0].Name() != "zlib" {
		t.Error("a serializer sorted the caller's slices in place")
	}
}

func TestPackagesByNameThenVersion(t *testing.T) {
	img := sampleImage()
	b, _ := ImageJSON(img)
	var doc struct {
		Packages struct {
			Rows []struct {
				State     string
				Declared  *Declared
				Installed *Installed
			}
			Counts []StateCount
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range doc.Packages.Rows {
		p := Package{Declared: r.Declared, Installed: r.Installed}
		got = append(got, p.Name()+"@"+p.Version())
	}
	want := "curl@7.88 left-pad@1.3.0 libssl3@3.0.15 mystery@ zlib@1.1 zlib@1.2"
	if strings.Join(got, " ") != want {
		t.Errorf("order %v, want %s", got, want)
	}
	if c := doc.Packages.Counts; len(c) != 5 || c[0].State != "undeclared" || c[3] != (StateCount{"not checkable", 2}) || c[4] != (StateCount{"both", 2}) {
		t.Errorf("counts %+v", c)
	}
}

func TestTextIsOneLinePerFinding(t *testing.T) {
	img := sampleImage()
	out := string(ImageText(img))
	var findings, groups []string
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "## "):
			groups = append(groups, strings.Fields(l)[1])
		case l == "" || strings.HasPrefix(l, "#"):
		default:
			findings = append(findings, l)
			if n := strings.Count(l, "\t"); n != 7 {
				t.Errorf("%d tabs in %q", n, l)
			}
		}
	}
	if len(findings) != len(img.Findings) {
		t.Errorf("%d finding lines for %d findings:\n%s", len(findings), len(img.Findings), out)
	}
	if g := strings.Join(groups, " "); g != "config filesystem secrets live future" {
		t.Errorf("groups %q", g)
	}
	if !strings.Contains(out, `line\nbreak\tand tab`) {
		t.Errorf("control characters not escaped:\n%s", out)
	}
	if !strings.Contains(out, "fs.setuid\tcertain\timage\tL1\t/a\t") || strings.Index(out, "\t/a\t") > strings.Index(out, "\t/b\t") {
		t.Errorf("findings within a group not in path order:\n%s", out)
	}
	if !strings.Contains(out, "DB_PASSWORD=*** (value masked)") {
		t.Errorf("a masked value not said to be masked:\n%s", out)
	}
}

func TestSPDXShape(t *testing.T) {
	img := sampleImage()
	b, err := ImageSPDX(img)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"spdxVersion", "dataLicense", "SPDXID", "name", "documentNamespace", "creationInfo", "packages"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("no %s", k)
		}
	}
	if doc["spdxVersion"] != "SPDX-2.3" || doc["dataLicense"] != "CC0-1.0" || doc["SPDXID"] != "SPDXRef-DOCUMENT" {
		t.Errorf("header %v %v %v", doc["spdxVersion"], doc["dataLicense"], doc["SPDXID"])
	}
	if ns := doc["documentNamespace"].(string); strings.Contains(ns, "#") || !strings.HasPrefix(ns, "https://") {
		t.Errorf("namespace %q", ns)
	}
	created := doc["creationInfo"].(map[string]any)["created"]
	if created != spdxCreated {
		t.Errorf("created %v", created)
	}
	idRe := regexp.MustCompile(`^SPDXRef-[A-Za-z0-9.\-]+$`)
	seen := map[string]bool{}
	pkgs := doc["packages"].([]any)
	if len(pkgs) != 6 {
		t.Fatalf("%d packages", len(pkgs))
	}
	for _, p := range pkgs {
		m := p.(map[string]any)
		id := m["SPDXID"].(string)
		if !idRe.MatchString(id) || seen[id] {
			t.Errorf("SPDXID %q", id)
		}
		seen[id] = true
		for _, k := range []string{"name", "downloadLocation", "filesAnalyzed", "licenseConcluded", "licenseDeclared", "copyrightText"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s has no %s", id, k)
			}
		}
		name := m["name"].(string)
		comment := m["comment"].(string)
		switch name {
		case "mystery":
			// Never invent a version.
			if _, ok := m["versionInfo"]; ok {
				t.Errorf("a version was invented for %s: %v", name, m["versionInfo"])
			}
			if !strings.Contains(comment, "state: not checkable; why: no purl") {
				t.Errorf("state lost: %q", comment)
			}
		case "left-pad":
			if !strings.Contains(comment, "state: not checkable") {
				t.Errorf("an npm package is not left not checkable: %q", comment)
			}
		case "libssl3":
			if _, ok := m["externalRefs"]; ok {
				t.Errorf("the declared purl is written beside the installed version: %v", m["externalRefs"])
			}
			if m["versionInfo"] != "3.0.15" || !strings.Contains(comment, "declared-version: 3.0.11") {
				t.Errorf("libssl3: %v %q", m["versionInfo"], comment)
			}
		}
	}
	if rels := doc["relationships"].([]any); len(rels) != 6 {
		t.Errorf("%d relationships", len(rels))
	}
}

func TestCycloneDXShape(t *testing.T) {
	img := sampleImage()
	b, err := ImageCycloneDX(img)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		BOMFormat, SpecVersion, SerialNumber string
		Version                              int
		Metadata                             struct{ Component cdxComponent }
		Components                           []cdxComponent
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.5" || doc.Version != 1 {
		t.Errorf("header %+v", doc)
	}
	// The pattern of the CycloneDX 1.5 schema.
	if !regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(doc.SerialNumber) {
		t.Errorf("serial number %q", doc.SerialNumber)
	}
	if doc.Metadata.Component.Type != "container" || doc.Metadata.Component.Version != "sha256:aaaa" {
		t.Errorf("subject %+v", doc.Metadata.Component)
	}
	refs := map[string]bool{}
	for _, c := range doc.Components {
		if refs[c.BOMRef] || c.Type != "library" || c.Name == "" {
			t.Errorf("component %+v", c)
		}
		refs[c.BOMRef] = true
		prop := map[string]string{}
		for _, p := range c.Properties {
			prop[p.Name] = p.Value
		}
		if prop["docker-desk:state"] == "" {
			t.Errorf("%s has no state", c.Name)
		}
		if c.Name == "mystery" && (c.Version != "" || c.PURL != "") {
			t.Errorf("invented for mystery: %+v", c)
		}
		if c.Name == "libssl3" && (c.PURL != "" || prop["docker-desk:declared-purl"] == "") {
			t.Errorf("libssl3: %+v", c)
		}
	}
	other := sampleImage()
	other.Packages.Rows = other.Packages.Rows[:2]
	b2, _ := ImageCycloneDX(other)
	if bytes.Equal(b, b2) || strings.Contains(string(b2), doc.SerialNumber) {
		t.Error("two different package lists share a serial number")
	}
}

func TestNoPackageSet(t *testing.T) {
	img := sampleImage()
	img.Packages = nil
	for _, f := range []Format{SPDX, CycloneDX, JSON} {
		b, err := Render(f, Document{Image: &img})
		if err != nil || !json.Valid(b) {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func sampleSpace() Space {
	sh := int64(10)
	return Space{
		ToolVersion:  "test",
		TotalsSource: "daemon",
		Totals:       []SpaceTotal{{Type: "Build Cache"}, {Type: "Images", Count: 3, Size: 100}, {Type: "Local Volumes"}, {Type: "Containers"}},
		Images: []SpaceImage{
			{ID: "sha256:b", References: []string{"b:1"}, Size: 50, Shared: &sh, Alone: Figure{Bytes: 40, Measure: Exact, Source: "daemon"},
				Together: &Figure{Bytes: 40, Measure: AtLeast, Source: "daemon", Reason: "1 shared layer has no size the daemon's own figures confirm"}, Sharers: []string{"sha256:a"}},
			{ID: "sha256:a", References: []string{"a:2", "a:1"}, Size: 50, Alone: Figure{Measure: Unknown, Reason: "the daemon did not compute the shared size"}},
		},
		Groups: []SpaceGroup{
			{Images: []string{"sha256:b", "sha256:a"}, Layers: 2, Unsized: 1, Size: Figure{Bytes: 5, Measure: AtLeast, Reason: "1 of its 2 layers have no size"}},
			{Images: []string{"sha256:c", "sha256:a"}, Layers: 1, Size: Figure{Bytes: 5, Measure: Exact}},
		},
		Volumes: []SpaceVolume{{Name: "z"}, {Name: "a"}},
	}
}

func TestSpaceCarriesMeasures(t *testing.T) {
	s := sampleSpace()
	b, err := Render(JSON, Document{Space: &s})
	if err != nil {
		t.Fatal(err)
	}
	var doc Space
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Totals[0].Type != "Images" || doc.Totals[3].Type != "Build Cache" {
		t.Errorf("totals order %+v", doc.Totals)
	}
	if doc.Images[0].ID != "sha256:a" || doc.Images[0].References[0] != "a:1" || doc.Images[0].Shared != nil {
		t.Errorf("images %+v", doc.Images[0])
	}
	if g := doc.Images[1].Together; g == nil || g.Measure != AtLeast || g.Reason == "" {
		t.Errorf("together lost its measure: %+v", g)
	}
	if doc.Groups[0].Images[0] != "sha256:a" || doc.Groups[0].Size.Measure == "" {
		t.Errorf("groups %+v", doc.Groups)
	}
	text := string(SpaceText(s))
	for _, want := range []string{
		"image\ta:1\t50\tunknown\t-\t-\t-\tthe daemon did not compute the shared size",
		"image\tb:1\t50\texact\t40\tat least\t40\t1 shared layer has no size",
		"shared\tat least\t5\t2\ta:1 · b:1\t1 of its 2 layers have no size",
		"shared\texact\t5\t1\ta:1 · sha256:c\t-",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
	for _, f := range []Format{SPDX, CycloneDX} {
		if _, err := Render(f, Document{Space: &s}); err == nil {
			t.Errorf("%s accepted for Space", f)
		}
	}
	s2 := sampleSpace()
	s2.Images[0], s2.Images[1] = s2.Images[1], s2.Images[0]
	s2.Groups[0], s2.Groups[1] = s2.Groups[1], s2.Groups[0]
	s2.Volumes[0], s2.Volumes[1] = s2.Volumes[1], s2.Volumes[0]
	for _, f := range []Format{JSON, Text} {
		a, _ := Render(f, Document{Space: &s})
		b, _ := Render(f, Document{Space: &s2})
		if !bytes.Equal(a, b) {
			t.Errorf("%s depends on input order", f)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"json": JSON, "SPDX": SPDX, "cyclonedx": CycloneDX, "cdx": CycloneDX, "text": Text, " txt ": Text} {
		if got, err := ParseFormat(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("xml accepted")
	}
}
