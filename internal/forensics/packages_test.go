package forensics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

func rowsByName(rep *Report) map[string][]PkgRow {
	out := map[string][]PkgRow{}
	for _, r := range JoinPackages(rep.Packages) {
		out[r.Name()] = append(out[r.Name()], r)
	}
	return out
}

func stateOf(t *testing.T, rows map[string][]PkgRow, name string) PkgRow {
	t.Helper()
	rs := rows[name]
	if len(rs) != 1 {
		t.Fatalf("%d rows for %s", len(rs), name)
	}
	return rs[0]
}

func dbOf(t *testing.T, rep *Report, eco string) InstalledDB {
	t.Helper()
	d, ok := rep.Packages.DB(eco)
	if !ok {
		t.Fatalf("no %s database in the report", eco)
	}
	return d
}

// TestDemoPackages: the fake daemon's python image, whose SBOM was made
// before its last layer and from an older build.
func TestDemoPackages(t *testing.T) {
	rep := analyse(t, fixture.OCI(fixture.DemoPackages("python:3.12-slim")), Options{})
	sb := rep.Packages.SBOM
	if !sb.Known() || sb.Format != "SPDX-2.3" || sb.Total != fixture.DemoPackagesDeclared || sb.Capped() {
		t.Fatalf("SBOM side: %+v", sb)
	}
	deb := dbOf(t, rep, "deb")
	if deb.State != DBRead || deb.Path != "/var/lib/dpkg/status" || deb.Layer != 4 || deb.Total != 66 || deb.Capped() {
		t.Fatalf("dpkg side: state %d path %q layer %d total %d", deb.State, deb.Path, deb.Layer, deb.Total)
	}
	for _, eco := range []string{"apk", "rpm"} {
		if d := dbOf(t, rep, eco); d.State != DBAbsent {
			t.Errorf("%s: state %d (%s)", eco, d.State, d.Unknown)
		}
	}
	rows := rowsByName(rep)
	for name, want := range map[string]struct {
		state PkgState
		layer int
	}{
		"bash": {StateBoth, 1}, "bsdutils": {StateBoth, 1}, "openssl": {StateBoth, 2},
		"curl": {StateUndeclared, 4}, "libcurl4": {StateUndeclared, 4}, "netcat-openbsd": {StateUndeclared, 4},
		"libssl3": {StateDiffers, 2}, "libgnutls30": {StateDeclaredOnly, 0},
		"requests": {StateUncheckable, 0}, "python": {StateUncheckable, 0}, "golang.org/x/sys": {StateUncheckable, 0},
	} {
		r := stateOf(t, rows, name)
		if r.State != want.state || r.Layer() != want.layer {
			t.Errorf("%s: %s at layer %d, want %s at layer %d", name, r.State, r.Layer(), want.state, want.layer)
		}
		if r.Installed != nil && r.Installed.LayerUnsure != "" {
			t.Errorf("%s: layer unsure: %s", name, r.Installed.LayerUnsure)
		}
	}
	if r := stateOf(t, rows, "python"); !strings.Contains(r.Why, "no purl") {
		t.Errorf("python: %q", r.Why)
	}
	if r := stateOf(t, rows, "libssl3"); r.Declared.Version != "3.0.11-1~deb12u2" || r.Installed.Version != "3.0.15-1~deb12u1" {
		t.Errorf("libssl3: %+v / %+v", r.Declared, r.Installed)
	}
	n := PkgCounts(JoinPackages(rep.Packages))
	if n[StateUndeclared] != 3 || n[StateDiffers] != 1 || n[StateDeclaredOnly] != 1 || n[StateUncheckable] != 6 || n[StateBoth] != 62 {
		t.Errorf("counts %v", n)
	}

	f := one(t, rep, "supply.undeclared", "/var/lib/dpkg/status")
	if f.Layer != 4 || !strings.Contains(f.Instruction, "apt-get install -y curl netcat-openbsd") || f.Certainty != Certain {
		t.Errorf("undeclared finding: %+v", f)
	}
	if f.Evidence != "3 installed packages the SBOM does not list: curl, libcurl4, netcat-openbsd" || len(f.Detail) != 3 {
		t.Errorf("evidence %q, detail %v", f.Evidence, f.Detail)
	}
	if !strings.Contains(f.Unknown, "cannot tell") {
		t.Errorf("the finding must say what cannot be known about why: %q", f.Unknown)
	}
	d := one(t, rep, "supply.sbom-drift", "")
	if d.Certainty != Hint || d.Evidence != "1 declared OS package is not installed, 1 installed at another version" {
		t.Errorf("drift: %+v", d)
	}
	if s := one(t, rep, "supply.sbom", ""); !strings.Contains(s.Unknown, "deb (dpkg)") {
		t.Errorf("the SBOM finding should say what it was checked against: %q", s.Unknown)
	}
}

// bigSBOM is an SPDX statement above the JSON bound: a files array and
// one string of several megabytes, and n packages.
func bigSBOM(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"spdxVersion":"SPDX-2.3","comment":"`)
	b.WriteString(strings.Repeat("c", 3<<20))
	b.WriteString(`","files":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"fileName":"/usr/share/doc/pkg-%d/copyright","SPDXID":"SPDXRef-File-%d","checksums":[{"algorithm":"SHA1","checksumValue":"%040d"}]}`, i, i, i)
	}
	b.WriteString(`],"packages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"pkg-%d","versionInfo":"1.%d","licenseConcluded":"NOASSERTION","licenseDeclared":"MIT","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:deb/debian/pkg-%d@1.%d?arch=amd64"}]}`, i, i, i, i)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func debImage(status []fixture.Pkg, sbom []byte) fixture.Image {
	return fixture.Image{
		Cmd: []string{"/bin/true"}, RepoTags: []string{"pk/test:1"},
		Layers: []fixture.Layer{{CreatedBy: "ADD rootfs /", Files: []fixture.File{
			{Path: "var/lib/dpkg/status", Body: fixture.DpkgStatus(status)},
		}}},
		SBOM: sbom != nil, SBOMDoc: sbom,
	}
}

func pkgs(n int) []fixture.Pkg {
	var out []fixture.Pkg
	for i := 0; i < n; i++ {
		out = append(out, fixture.Pkg{Name: fmt.Sprintf("pkg-%d", i), Version: fmt.Sprintf("1.%d", i)})
	}
	return out
}

// TestSBOMAboveTheJSONBoundIsStillBrowsable: the document is too large to
// keep, and its packages are read as it streams past all the same.
func TestSBOMAboveTheJSONBoundIsStillBrowsable(t *testing.T) {
	doc := bigSBOM(500)
	if int64(len(doc)) <= DefaultLimits().JSONBlob {
		t.Fatalf("the document is %d bytes, not above the bound", len(doc))
	}
	rep := analyse(t, fixture.OCI(debImage(pkgs(501), doc)), Options{})
	sb := rep.Packages.SBOM
	if !sb.Known() || sb.Total != 500 || len(sb.Packages) != 500 {
		t.Fatalf("SBOM side: known %v total %d kept %d (%s)", sb.Known(), sb.Total, len(sb.Packages), sb.Unknown)
	}
	if p := sb.Packages[499]; p.Name != "pkg-499" || p.Version != "1.499" || p.License != "MIT" || p.Type != "deb" || p.Arch != "amd64" {
		t.Errorf("last package: %+v", p)
	}
	if !strings.Contains(one(t, rep, "supply.sbom", "").Evidence, "SPDX-2.3, 500 packages") {
		t.Errorf("the summary should count a large SBOM too:\n%s", dump(rep))
	}
	rows := rowsByName(rep)
	if r := stateOf(t, rows, "pkg-500"); r.State != StateUndeclared {
		t.Errorf("pkg-500: %s", r.State)
	}
	if f := one(t, rep, "supply.undeclared", ""); !strings.Contains(f.Evidence, "1 installed package the SBOM does not list: pkg-500") {
		t.Errorf("%+v", f)
	}
}

// TestBoundsAreSaid: a capped table keeps the true total, and nothing the
// cap hides is called undeclared or declared only.
func TestBoundsAreSaid(t *testing.T) {
	lim := DefaultLimits()
	lim.Declared = 5
	img := debImage(pkgs(12), fixture.SPDX("debian", pkgs(10)))
	rep := analyse(t, fixture.OCI(img), Options{Limits: lim})
	if sb := rep.Packages.SBOM; !sb.Capped() || sb.Total != 10 || len(sb.Packages) != 5 {
		t.Fatalf("declared cap: total %d kept %d", sb.Total, len(sb.Packages))
	}
	n := PkgCounts(JoinPackages(rep.Packages))
	if n[StateUndeclared] != 0 || n[StateBoth] != 5 || n[StateUncheckable] != 7 {
		t.Errorf("with the SBOM capped: %v", n)
	}
	if len(find(rep, "supply.undeclared", "")) != 0 {
		t.Error("an undeclared finding from a capped SBOM")
	}

	lim = DefaultLimits()
	lim.Installed = 4
	rep = analyse(t, fixture.OCI(img), Options{Limits: lim})
	deb := dbOf(t, rep, "deb")
	if !deb.Capped() || deb.Total != 12 || len(deb.Packages) != 4 {
		t.Fatalf("installed cap: total %d kept %d", deb.Total, len(deb.Packages))
	}
	n = PkgCounts(JoinPackages(rep.Packages))
	if n[StateDeclaredOnly] != 0 || n[StateBoth] != 4 || n[StateUncheckable] != 6 {
		t.Errorf("with the database capped: %v", n)
	}

	lim = DefaultLimits()
	lim.TextDB = 1 << 10
	rep = analyse(t, fixture.OCI(img), Options{Limits: lim})
	if deb := dbOf(t, rep, "deb"); deb.State != DBUnknown || !strings.Contains(deb.Unknown, "above the 1.0kB") {
		t.Errorf("text bound: %d %q", deb.State, deb.Unknown)
	}
	if DefaultLimits().Declared != 20_000 || DefaultLimits().Installed != 10_000 || DefaultLimits().RPMDB != 64<<20 || DefaultLimits().TextDB != 8<<20 {
		t.Error("the bounds the view states have changed")
	}
}

// TestZstdImage: no layer can be read, so what is installed is unknown;
// the SBOM half is still there to browse.
func TestZstdImage(t *testing.T) {
	img := debImage(pkgs(3), fixture.SPDX("debian", pkgs(3)))
	img.Layers[0].Zstd = true
	rep := analyse(t, fixture.OCI(img), Options{})
	for _, eco := range []string{"deb", "apk", "rpm"} {
		d := dbOf(t, rep, eco)
		if d.State != DBUnknown || !strings.Contains(d.Unknown, "zstd") {
			t.Errorf("%s: state %d %q", eco, d.State, d.Unknown)
		}
	}
	if sb := rep.Packages.SBOM; !sb.Known() || len(sb.Packages) != 3 {
		t.Fatalf("SBOM side: %+v", sb)
	}
	for _, r := range JoinPackages(rep.Packages) {
		if r.State != StateUncheckable || r.Declared == nil || !strings.Contains(r.Why, "unknown") {
			t.Errorf("%s: %s (%s)", r.Name(), r.State, r.Why)
		}
	}
	if len(find(rep, "supply.undeclared", ""))+len(find(rep, "supply.sbom-drift", "")) != 0 {
		t.Errorf("findings from an unknown database:\n%s", dump(rep))
	}
}

// TestDeletedDatabaseIsUnknownNotEmpty: removing /var/lib/dpkg leaves the
// packages on disk and their record gone — not an image without packages.
func TestDeletedDatabaseIsUnknownNotEmpty(t *testing.T) {
	img := debImage(pkgs(3), fixture.SPDX("debian", pkgs(3)))
	img.Layers = append(img.Layers, fixture.Layer{CreatedBy: "RUN rm -rf /var/lib/dpkg", Delete: []string{"var/lib/dpkg"}})
	rep := analyse(t, fixture.OCI(img), Options{})
	deb := dbOf(t, rep, "deb")
	if deb.State != DBUnknown || !strings.Contains(deb.Unknown, "deleted in layer 2") {
		t.Fatalf("state %d %q", deb.State, deb.Unknown)
	}
	if n := PkgCounts(JoinPackages(rep.Packages)); n[StateDeclaredOnly] != 0 || n[StateUncheckable] != 3 {
		t.Errorf("counts %v", n)
	}
}

// TestDistrolessStatusD: one file per package, added and deleted layer by
// layer; the final filesystem decides which count.
func TestDistrolessStatusD(t *testing.T) {
	stanza := func(name, ver string) []byte {
		return []byte("Package: " + name + "\nVersion: " + ver + "\nArchitecture: amd64\n")
	}
	img := fixture.Image{
		Cmd: []string{"/app"}, RepoTags: []string{"distroless/x:1"},
		Layers: []fixture.Layer{
			{CreatedBy: "ADD base /", Files: []fixture.File{
				{Path: "var/lib/dpkg/status.d/base-files", Body: stanza("base-files", "12.4")},
				{Path: "var/lib/dpkg/status.d/base-files.md5sums", Body: []byte("abc  etc/os-release\n")},
				{Path: "var/lib/dpkg/status.d/libc6", Body: stanza("libc6", "2.36-9")},
				{Path: "var/lib/dpkg/status.d/tzdata", Body: stanza("tzdata", "2024a")},
			}},
			{CreatedBy: "ADD openssl /", Files: []fixture.File{{Path: "var/lib/dpkg/status.d/openssl", Body: stanza("openssl", "3.0.15")}}},
			{CreatedBy: "RUN rm tzdata", Delete: []string{"var/lib/dpkg/status.d/tzdata"}},
		},
		SBOM: true, SBOMDoc: fixture.SPDX("debian", []fixture.Pkg{{Name: "base-files", Version: "12.4"}, {Name: "libc6", Version: "2.36-9"}, {Name: "tzdata", Version: "2024a"}}),
	}
	rep := analyse(t, fixture.OCI(img), Options{})
	deb := dbOf(t, rep, "deb")
	if deb.State != DBRead || deb.Total != 3 || deb.Path != "/var/lib/dpkg/status.d (3 files)" || deb.Layer != 2 {
		t.Fatalf("%+v", deb)
	}
	rows := rowsByName(rep)
	if r := stateOf(t, rows, "openssl"); r.State != StateUndeclared || r.Layer() != 2 || r.Installed.Source != "/var/lib/dpkg/status.d/openssl" {
		t.Errorf("openssl: %s layer %d", r.State, r.Layer())
	}
	if r := stateOf(t, rows, "tzdata"); r.State != StateDeclaredOnly {
		t.Errorf("tzdata: %s", r.State)
	}
	if r := stateOf(t, rows, "libc6"); r.State != StateBoth || r.Layer() != 1 {
		t.Errorf("libc6: %s layer %d", r.State, r.Layer())
	}
}

// TestAPKAndCycloneDX: apk's database, a CycloneDX SBOM with a nested
// component, licences from both.
func TestAPKAndCycloneDX(t *testing.T) {
	installed := []fixture.Pkg{
		{Name: "musl", Version: "1.2.5-r0", License: "MIT"},
		{Name: "busybox", Version: "1.36.1-r29", License: "GPL-2.0-only"},
		{Name: "zlib", Version: "1.3.1-r1", License: "Zlib"},
	}
	declared := []fixture.Pkg{
		{Name: "musl", Version: "1.2.5-r0", Type: "apk", License: "MIT"},
		{Name: "busybox", Version: "1.36.1-r28", Type: "apk"},
		{Name: "left-pad", Version: "1.3.0", PURL: "pkg:npm/left-pad@1.3.0", License: "WTFPL"},
	}
	img := fixture.Image{
		Cmd: []string{"/bin/sh"}, RepoTags: []string{"alpine/x:1"},
		Layers: []fixture.Layer{{CreatedBy: "ADD alpine /", Gzip: true, Files: []fixture.File{{Path: "lib/apk/db/installed", Body: fixture.APKInstalled(installed)}}}},
		SBOM:   true, SBOMDoc: fixture.CycloneDX("alpine", declared), SBOMPredicate: "https://cyclonedx.org/bom",
	}
	rep := analyse(t, fixture.OCI(img), Options{})
	if sb := rep.Packages.SBOM; sb.Format != "CycloneDX 1.5" || sb.Total != 3 {
		t.Fatalf("SBOM side: %+v", sb)
	}
	rows := rowsByName(rep)
	if r := stateOf(t, rows, "busybox"); r.State != StateDiffers || r.License() != "GPL-2.0-only" {
		t.Errorf("busybox: %s %q", r.State, r.License())
	}
	if r := stateOf(t, rows, "zlib"); r.State != StateUndeclared || r.License() != "Zlib" {
		t.Errorf("zlib: %s", r.State)
	}
	if r := stateOf(t, rows, "left-pad"); r.State != StateUncheckable || r.Type != "npm" || r.License() != "WTFPL" {
		t.Errorf("left-pad (nested): %s %q", r.State, r.Type)
	}
	if r := stateOf(t, rows, "musl"); r.State != StateBoth {
		t.Errorf("musl: %s", r.State)
	}
}

func rpmImage(t *testing.T, layers ...fixture.Layer) fixture.Image {
	real := gunzipFile(t, "ubi9-micro.rpmdb.sqlite.gz")
	var declared []fixture.Pkg
	recs, _, err := readRPMDB(real, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.pseudo || r.name == "tzdata" {
			continue // tzdata is left out of the SBOM: undeclared
		}
		declared = append(declared, fixture.Pkg{Name: r.name, Version: r.version, Arch: r.arch, Type: "rpm", License: r.license})
	}
	declared = append(declared, fixture.Pkg{Name: "openssl-libs", Version: "3.5.1-4.el9", Arch: "x86_64", Type: "rpm"})
	base := fixture.Layer{CreatedBy: "ADD ubi-micro /", Files: []fixture.File{
		{Path: "var/lib/rpm/rpmdb.sqlite", Body: real},
		{Path: "var/lib/rpm/rpmdb.sqlite-wal", Body: nil},
		{Path: "var/lib/rpm/rpmdb.sqlite-shm", Body: make([]byte, 32768)},
	}}
	return fixture.Image{
		Cmd: []string{"/bin/bash"}, RepoTags: []string{"ubi/micro:9"},
		Layers: append([]fixture.Layer{base}, layers...),
		SBOM:   true, SBOMDoc: fixture.SPDX("redhat", declared),
	}
}

// TestRealRPMDBInAnImage: the real database, read in a layer as it
// streams, joined with an SBOM.
func TestRealRPMDBInAnImage(t *testing.T) {
	rep := analyse(t, fixture.OCI(rpmImage(t)), Options{})
	d := dbOf(t, rep, "rpm")
	if d.State != DBRead || d.Total != 22 || d.Path != "/var/lib/rpm/rpmdb.sqlite" || d.Layer != 1 {
		t.Fatalf("%+v", d)
	}
	rows := rowsByName(rep)
	if r := stateOf(t, rows, "tzdata"); r.State != StateUndeclared || r.Layer() != 1 {
		t.Errorf("tzdata: %s", r.State)
	}
	if r := stateOf(t, rows, "openssl-libs"); r.State != StateDeclaredOnly {
		t.Errorf("openssl-libs: %s", r.State)
	}
	for _, r := range rows["gpg-pubkey"] {
		if r.State != StateUncheckable {
			t.Errorf("gpg-pubkey: %s", r.State)
		}
	}
	if n := PkgCounts(JoinPackages(rep.Packages)); n[StateBoth] != 19 {
		t.Errorf("counts %v", n)
	}
	if f := one(t, rep, "supply.undeclared", "/var/lib/rpm/rpmdb.sqlite"); !strings.HasSuffix(f.Evidence, ": tzdata") {
		t.Errorf("%q", f.Evidence)
	}
}

// TestRPMWriteAheadLogMakesItUnknown: a database file with transactions
// still in its log is not the database.
func TestRPMWriteAheadLogMakesItUnknown(t *testing.T) {
	img := rpmImage(t, fixture.Layer{CreatedBy: "RUN microdnf install -y x", Files: []fixture.File{{Path: "var/lib/rpm/rpmdb.sqlite-wal", Body: make([]byte, 4096)}}})
	rep := analyse(t, fixture.OCI(img), Options{})
	if d := dbOf(t, rep, "rpm"); d.State != DBUnknown || !strings.Contains(d.Unknown, "rpmdb.sqlite-wal holds 4.1kB") {
		t.Fatalf("%d %q", d.State, d.Unknown)
	}
}

// TestFedoraLayoutFollowsTheFinalFilesystem: /var/lib/rpm becomes a link
// to /usr/lib/sysimage/rpm in a later layer; the copy under the link is
// hidden, and the one it points at is read.
func TestFedoraLayoutFollowsTheFinalFilesystem(t *testing.T) {
	synth := gunzipFile(t, "synthetic.rpmdb.sqlite.gz")
	img := rpmImage(t, fixture.Layer{CreatedBy: "RUN rpmdb --rebuilddb", Delete: []string{"var/lib/rpm"}, Files: []fixture.File{
		{Path: "var/lib/rpm", Type: '2', Link: "../../usr/lib/sysimage/rpm"},
		{Path: "usr/lib/sysimage/rpm/rpmdb.sqlite", Body: synth},
	}})
	rep := analyse(t, fixture.OCI(img), Options{})
	d := dbOf(t, rep, "rpm")
	if d.State != DBRead || d.Path != "/usr/lib/sysimage/rpm/rpmdb.sqlite" || d.Total != 1001 || len(d.Notes) != 0 {
		t.Fatalf("state %d path %q total %d notes %v (%s)", d.State, d.Path, d.Total, d.Notes, d.Unknown)
	}
	// Without the whiteout, only the link hides the old directory.
	img.Layers[1].Delete = nil
	rep = analyse(t, fixture.OCI(img), Options{})
	if d := dbOf(t, rep, "rpm"); d.Path != "/usr/lib/sysimage/rpm/rpmdb.sqlite" || len(d.Notes) != 0 {
		t.Fatalf("path %q notes %v", d.Path, d.Notes)
	}
}

// TestAttributionAcrossAnUnreadableCopy: a copy that cannot be read makes
// the layers around it uncertain, and says so.
func TestAttributionAcrossAnUnreadableCopy(t *testing.T) {
	apk := func(names ...string) []byte {
		var ps []fixture.Pkg
		for _, n := range names {
			ps = append(ps, fixture.Pkg{Name: n, Version: "1.0-r0"})
		}
		return fixture.APKInstalled(ps)
	}
	img := fixture.Image{
		Cmd: []string{"/bin/sh"}, RepoTags: []string{"a/b:1"},
		Layers: []fixture.Layer{
			{CreatedBy: "ADD base /", Files: []fixture.File{{Path: "lib/apk/db/installed", Body: apk("musl", "busybox")}}},
			{CreatedBy: "RUN apk add x", Files: []fixture.File{{Path: "lib/apk/db/installed", Body: []byte("P:x\x00\x00\x00binary")}}},
			{CreatedBy: "RUN apk add y", Files: []fixture.File{{Path: "lib/apk/db/installed", Body: apk("musl", "busybox", "x", "y")}}},
			{CreatedBy: "RUN apk add z", Files: []fixture.File{{Path: "lib/apk/db/installed", Body: apk("musl", "busybox", "x", "y", "z")}}},
		},
	}
	rep := analyse(t, fixture.OCI(img), Options{})
	d := dbOf(t, rep, "apk")
	if d.State != DBRead || d.Total != 5 {
		t.Fatalf("%+v", d)
	}
	got := map[string]InstalledPackage{}
	for _, p := range d.Packages {
		got[p.Name] = p
	}
	if p := got["z"]; p.Layer != 4 || p.LayerUnsure != "" {
		t.Errorf("z: %+v", p)
	}
	if p := got["y"]; p.Layer != 3 || !strings.Contains(p.LayerUnsure, "layer 2 is not known") {
		t.Errorf("y: %+v", p)
	}
	if p := got["musl"]; p.Layer != 1 || !strings.Contains(p.LayerUnsure, "removed and installed again") {
		t.Errorf("musl: %+v", p)
	}
	// No SBOM: every row is there to browse, and none can be checked.
	for _, r := range JoinPackages(rep.Packages) {
		if r.State != StateUncheckable || !strings.Contains(r.Why, "no SBOM") {
			t.Errorf("%s: %s %q", r.Name(), r.State, r.Why)
		}
	}
}

// TestStringsFromTheImageAreSanitised: control characters and bidirectional
// overrides in a database or an SBOM never reach the screen.
func TestStringsFromTheImageAreSanitised(t *testing.T) {
	evil := "evil\x1b[2J‮pkg⁦"
	status := []byte("Package: " + evil + "\nStatus: install ok installed\nVersion: 1\x07.0\n\n")
	sbom := []byte(`{"spdxVersion":"SPDX-2.3","packages":[{"name":"x\u001b[31m‮y","versionInfo":"1","licenseDeclared":"MIT\u0000","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:deb/debian/x\u001by@1"}]}]}`)
	img := fixture.Image{Cmd: []string{"/x"}, RepoTags: []string{"s/s:1"},
		Layers: []fixture.Layer{{Files: []fixture.File{{Path: "var/lib/dpkg/status", Body: status}}}},
		SBOM:   true, SBOMDoc: sbom}
	rep := analyse(t, fixture.OCI(img), Options{})
	var all []string
	for _, r := range JoinPackages(rep.Packages) {
		if d := r.Declared; d != nil {
			all = append(all, d.Name, d.Version, d.License, d.PURL)
		}
		if i := r.Installed; i != nil {
			all = append(all, i.Name, i.Version, i.License, i.Arch)
		}
	}
	for _, f := range rep.Findings {
		all = append(all, f.Evidence)
		all = append(all, f.Detail...)
	}
	if len(all) < 8 {
		t.Fatalf("too little to check: %q", all)
	}
	for _, s := range all {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				t.Fatalf("%q holds %U", s, r)
			}
		}
	}
}

func TestDpkgStatusSkipsWhatIsNotInstalled(t *testing.T) {
	recs, total, err := parseDpkg(fixture.DpkgStatus(pkgs(2), "old-config"), 100, false)
	if err != nil || total != 2 || len(recs) != 2 {
		t.Fatalf("%v %d %v", recs, total, err)
	}
	if recs[0].name != "pkg-0" || recs[0].version != "1.0" || recs[0].arch != "amd64" {
		t.Errorf("%+v", recs[0])
	}
}

func TestJSONScanner(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		format    string
		total     int
		err       string
	}{
		{"bare SPDX", `{"spdxVersion":"SPDX-2.2","packages":[{"name":"a"},{"name":"b"}]}`, "SPDX-2.2", 2, ""},
		{"in-toto", `{"_type":"x","predicate":{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"name":"a","components":[{"name":"b"}]}]},"subject":[]}`, "CycloneDX 1.6", 2, ""},
		{"not an SBOM", `{"architecture":"amd64","rootfs":{"diff_ids":["a"]},"packages":[{"name":"x"}]}`, "", 1, ""},
		{"an array", `[{"spdxVersion":"SPDX-2.3"}]`, "", 0, ""},
		{"escapes", `{"spdxVersion":"SPDX-2.3","packages":[{"name":"café 😀 \"q\" \\ \/","versionInfo":1}]}`, "SPDX-2.3", 1, ""},
		{"cut short", `{"spdxVersion":"SPDX-2.3","packages":[{"name":"a"},{"name":"b`, "SPDX-2.3", 0, "ends inside a string"},
		{"broken", `{"spdxVersion":"SPDX-2.3","packages":[{"name" "a"}]}`, "SPDX-2.3", 0, "expected"},
		{"trailing comma", `{"spdxVersion":"SPDX-2.3","packages":[{"name":"a"},]}`, "SPDX-2.3", 0, "not valid JSON"},
		{"deep", `{"spdxVersion":"SPDX-2.3","x":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `}`, "SPDX-2.3", 0, "deeper than 64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := scanJSON(strings.NewReader(tc.doc), 100)
			if sc.format != tc.format || sc.total != tc.total || !strings.Contains(sc.err, tc.err) || (tc.err == "" && sc.err != "") {
				t.Fatalf("format %q total %d err %q", sc.format, sc.total, sc.err)
			}
			if tc.err != "" && len(sc.recs) != 0 {
				t.Fatal("a document that failed to read offers a partial list")
			}
			// Every valid document here is valid for encoding/json too.
			if tc.err == "" && !json.Valid([]byte(tc.doc)) {
				t.Fatal("test document")
			}
		})
	}
	sc := scanJSON(strings.NewReader(`{"spdxVersion":"SPDX-2.3","packages":[{"name":"café 😀 \"q\"","versionInfo":"`+strings.Repeat("9", 10000)+`"}]}`), 100)
	if sc.err != "" || sc.recs[0].Name != `café 😀 "q"` || len([]rune(sc.recs[0].Version)) != 129 {
		t.Errorf("%+v", sc)
	}
}

func TestPURLAndVersions(t *testing.T) {
	typ, name, ver, arch, epoch, ok := parsePURL("pkg:rpm/redhat/openssl-libs@3.0.7-27.el9?arch=x86_64&epoch=1&distro=rhel-9.4")
	if !ok || typ != "rpm" || name != "openssl-libs" || ver != "3.0.7-27.el9" || arch != "x86_64" || epoch != "1" {
		t.Errorf("%s %s %s %s %s", typ, name, ver, arch, epoch)
	}
	if _, name, ver, _, _, ok := parsePURL("pkg:deb/debian/zlib1g@1%3A1.2.13.dfsg-1?arch=amd64"); !ok || name != "zlib1g" || ver != "1:1.2.13.dfsg-1" {
		t.Errorf("%s %s", name, ver)
	}
	if _, name, _, _, _, ok := parsePURL("pkg:golang/golang.org/x/sys@v0.20.0"); !ok || name != "sys" {
		t.Errorf("%s", name)
	}
	for _, bad := range []string{"", "deb/debian/x", "pkg:", "pkg:deb"} {
		if _, _, _, _, _, ok := parsePURL(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"1.0", "1.0", true}, {"1:1.0", "1.0", true}, {"0:1.0", "1.0", true}, {"1:1.0", "2:1.0", false},
		{"1.0", "1.1", false}, {"1:1.0-1", "1:1.0-2", false},
	} {
		if got := sameVersion("deb", c.a, c.b); got != c.same {
			t.Errorf("sameVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// TestPackagesDoNotSlowTheStream: a report on an image with no package
// databases and no SBOM still has the three ecosystems, absent.
func TestNoDatabasesIsAbsent(t *testing.T) {
	rep, err := Analyse(context.Background(), bytes.NewReader(fixture.Legacy(fixture.Plain("p/p:1")).Archive), Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Packages.DBs) != 3 || rep.Packages.SBOM.Listed {
		t.Fatalf("%+v", rep.Packages)
	}
	for _, d := range rep.Packages.DBs {
		if d.State != DBAbsent {
			t.Errorf("%s: %d", d.Ecosystem, d.State)
		}
	}
	if len(JoinPackages(rep.Packages)) != 0 {
		t.Error("rows from nothing")
	}
}
