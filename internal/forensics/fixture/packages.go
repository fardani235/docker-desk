package fixture

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Pkg is one package, for a database or an SBOM.
type Pkg struct {
	Name, Version, Arch, License string
	// PURL overrides the purl an SBOM gives it; "-" gives none.
	PURL string
	// Type is the purl type for an SBOM entry (deb when empty).
	Type string
}

func (p Pkg) purl(distro string) string {
	switch p.PURL {
	case "-":
		return ""
	case "":
	default:
		return p.PURL
	}
	typ := p.Type
	if typ == "" {
		typ = "deb"
	}
	s := "pkg:" + typ + "/" + distro + "/" + p.Name + "@" + strings.ReplaceAll(p.Version, ":", "%3A")
	if p.Arch != "" {
		s += "?arch=" + p.Arch
	}
	return s
}

// DpkgStatus is a dpkg status file listing the packages as installed.
// Removed lists packages left with only their configuration ("rc").
func DpkgStatus(pkgs []Pkg, removed ...string) []byte {
	var b strings.Builder
	for _, p := range pkgs {
		arch := p.Arch
		if arch == "" {
			arch = "amd64"
		}
		fmt.Fprintf(&b, "Package: %s\nStatus: install ok installed\nPriority: optional\nSection: misc\nInstalled-Size: 100\n"+
			"Maintainer: Desk Fixtures <fixtures@example.invalid>\nArchitecture: %s\nVersion: %s\n"+
			"Description: %s for the fixtures\n a continuation line: Package: not-a-package\n .\n more text\n\n", p.Name, arch, p.Version, p.Name)
	}
	for _, n := range removed {
		fmt.Fprintf(&b, "Package: %s\nStatus: deinstall ok config-files\nArchitecture: amd64\nVersion: 1.0\n\n", n)
	}
	return []byte(b.String())
}

// APKInstalled is an apk installed database.
func APKInstalled(pkgs []Pkg) []byte {
	var b strings.Builder
	for _, p := range pkgs {
		arch := p.Arch
		if arch == "" {
			arch = "x86_64"
		}
		fmt.Fprintf(&b, "C:Q1fixture=\nP:%s\nV:%s\nA:%s\nS:1000\nI:4000\nT:%s for the fixtures\nU:https://example.invalid\nL:%s\no:%s\nm:Desk Fixtures\nt:1700000000\nc:0000\nD:so:libc.musl-x86_64.so.1\nF:usr/bin\nR:%s\na:0:0:755\nZ:Q1abc=\n\n",
			p.Name, p.Version, arch, p.Name, p.License, p.Name, p.Name)
	}
	return []byte(b.String())
}

// SPDX is an SPDX 2.3 document listing the packages, as BuildKit's
// scanner writes it: a purl among each package's external references.
func SPDX(distro string, pkgs []Pkg) []byte {
	type ref struct {
		Category string `json:"referenceCategory"`
		Type     string `json:"referenceType"`
		Locator  string `json:"referenceLocator"`
	}
	var out []map[string]any
	for i, p := range pkgs {
		lic := p.License
		if lic == "" {
			lic = "NOASSERTION"
		}
		e := map[string]any{
			"name": p.Name, "SPDXID": fmt.Sprintf("SPDXRef-Package-%d", i), "versionInfo": p.Version,
			"downloadLocation": "NOASSERTION", "licenseConcluded": "NOASSERTION", "licenseDeclared": lic,
			"copyrightText": "NOASSERTION", "filesAnalyzed": false,
		}
		if u := p.purl(distro); u != "" {
			e["externalRefs"] = []ref{
				{Category: "SECURITY", Type: "cpe23Type", Locator: "cpe:2.3:a:" + p.Name + ":" + p.Name + ":" + p.Version + ":*:*:*:*:*:*:*"},
				{Category: "PACKAGE-MANAGER", Type: "purl", Locator: u},
			}
		}
		out = append(out, e)
	}
	b, _ := json.Marshal(map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT", "name": "sbom",
		"creationInfo": map[string]any{"created": "2024-01-02T03:04:05Z", "creators": []string{"Tool: fixture"}},
		"packages":     out,
	})
	return b
}

// CycloneDX is a CycloneDX 1.5 document listing the packages, the last of
// them nested under the first to exercise nested components.
func CycloneDX(distro string, pkgs []Pkg) []byte {
	var comps []map[string]any
	for _, p := range pkgs {
		c := map[string]any{"type": "library", "name": p.Name, "version": p.Version}
		if u := p.purl(distro); u != "" {
			c["purl"] = u
		}
		if p.License != "" {
			c["licenses"] = []any{map[string]any{"license": map[string]string{"id": p.License}}}
		}
		comps = append(comps, c)
	}
	if len(comps) > 1 {
		last := comps[len(comps)-1]
		comps = comps[:len(comps)-1]
		comps[0]["components"] = []any{last}
	}
	b, _ := json.Marshal(map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1,
		"metadata":   map[string]any{"component": map[string]any{"type": "container", "name": "image"}},
		"components": comps,
	})
	return b
}

// ---- the desk's demo of a package list ----------------------------------------

// debBase is what a slim Debian base layer installs.
var debBase = []Pkg{
	{Name: "adduser", Version: "3.134", Arch: "all", License: "GPL-2.0-only"},
	{Name: "apt", Version: "2.6.1", License: "GPL-2.0-only"},
	{Name: "base-files", Version: "12.4+deb12u5", License: "GPL-2.0-or-later"},
	{Name: "base-passwd", Version: "3.6.1", License: "GPL-2.0-only"},
	{Name: "bash", Version: "5.2.15-2+b2", License: "GPL-3.0-or-later"},
	{Name: "bsdutils", Version: "1:2.38.1-5+deb12u1", License: "BSD-4-Clause"},
	{Name: "coreutils", Version: "9.1-1", License: "GPL-3.0-or-later"},
	{Name: "dash", Version: "0.5.12-2", License: "BSD-3-Clause"},
	{Name: "debconf", Version: "1.5.82", Arch: "all", License: "BSD-2-Clause"},
	{Name: "debian-archive-keyring", Version: "2023.3+deb12u1", Arch: "all", License: "GPL-2.0-or-later"},
	{Name: "debianutils", Version: "5.7-0.5~deb12u1", License: "GPL-2.0-or-later"},
	{Name: "diffutils", Version: "1:3.8-4", License: "GPL-3.0-or-later"},
	{Name: "dpkg", Version: "1.21.22", License: "GPL-2.0-or-later"},
	{Name: "e2fsprogs", Version: "1.47.0-2", License: "GPL-2.0-only"},
	{Name: "findutils", Version: "4.9.0-4", License: "GPL-3.0-or-later"},
	{Name: "gcc-12-base", Version: "12.2.0-14", License: "GPL-3.0-only"},
	{Name: "gpgv", Version: "2.2.40-1.1", License: "GPL-3.0-or-later"},
	{Name: "grep", Version: "3.8-5", License: "GPL-3.0-or-later"},
	{Name: "gzip", Version: "1.12-1", License: "GPL-3.0-or-later"},
	{Name: "hostname", Version: "3.23+nmu1", License: "GPL-2.0-only"},
	{Name: "init-system-helpers", Version: "1.65.2", Arch: "all", License: "BSD-3-Clause"},
	{Name: "libacl1", Version: "2.3.1-3", License: "LGPL-2.1-or-later"},
	{Name: "libapt-pkg6.0", Version: "2.6.1", License: "GPL-2.0-only"},
	{Name: "libattr1", Version: "1:2.5.1-4", License: "LGPL-2.1-or-later"},
	{Name: "libaudit1", Version: "1:3.0.9-1", License: "LGPL-2.1-or-later"},
	{Name: "libblkid1", Version: "2.38.1-5+deb12u1", License: "LGPL-2.1-or-later"},
	{Name: "libbz2-1.0", Version: "1.0.8-5+b1", License: "BSD-4-Clause"},
	{Name: "libc-bin", Version: "2.36-9+deb12u7", License: "LGPL-2.1-or-later"},
	{Name: "libc6", Version: "2.36-9+deb12u7", License: "LGPL-2.1-or-later"},
	{Name: "libcap-ng0", Version: "0.8.3-1+b3", License: "LGPL-2.1-or-later"},
	{Name: "libcrypt1", Version: "1:4.4.33-2", License: "LGPL-2.1-or-later"},
	{Name: "libdb5.3", Version: "5.3.28+dfsg2-1", License: "BSD-3-Clause"},
	{Name: "libffi8", Version: "3.4.4-1", License: "MIT"},
	{Name: "libgcc-s1", Version: "12.2.0-14", License: "GPL-3.0-only"},
	{Name: "liblzma5", Version: "5.4.1-0.2", License: "PD"},
	{Name: "libpcre2-8-0", Version: "10.42-1", License: "BSD-3-Clause"},
	{Name: "libselinux1", Version: "3.4-1+b6", License: "PD"},
	{Name: "libsqlite3-0", Version: "3.40.1-2", License: "PD"},
	{Name: "libstdc++6", Version: "12.2.0-14", License: "GPL-3.0-only"},
	{Name: "libsystemd0", Version: "252.26-1~deb12u2", License: "LGPL-2.1-or-later"},
	{Name: "libtinfo6", Version: "6.4-4", License: "MIT"},
	{Name: "libudev1", Version: "252.26-1~deb12u2", License: "LGPL-2.1-or-later"},
	{Name: "libzstd1", Version: "1.5.4+dfsg2-5", License: "BSD-3-Clause"},
	{Name: "login", Version: "1:4.13+dfsg1-1+b1", License: "GPL-2.0-or-later"},
	{Name: "mawk", Version: "1.3.4.20200120-3.1", License: "GPL-2.0-only"},
	{Name: "mount", Version: "2.38.1-5+deb12u1", License: "GPL-2.0-or-later"},
	{Name: "ncurses-base", Version: "6.4-4", Arch: "all", License: "MIT"},
	{Name: "passwd", Version: "1:4.13+dfsg1-1+b1", License: "GPL-2.0-or-later"},
	{Name: "perl-base", Version: "5.36.0-7+deb12u1", License: "GPL-1.0-or-later"},
	{Name: "sed", Version: "4.9-1", License: "GPL-3.0-or-later"},
	{Name: "sysvinit-utils", Version: "3.06-4", License: "GPL-2.0-or-later"},
	{Name: "tar", Version: "1.34+dfsg-1.2+deb12u1", License: "GPL-3.0-or-later"},
	{Name: "tzdata", Version: "2024a-0+deb12u1", Arch: "all", License: "PD"},
	{Name: "util-linux", Version: "2.38.1-5+deb12u1", License: "GPL-2.0-or-later"},
	{Name: "zlib1g", Version: "1:1.2.13.dfsg-1", License: "Zlib"},
}

// debRuntime is what the image's second layer installs with apt-get.
var debRuntime = []Pkg{
	{Name: "ca-certificates", Version: "20230311", Arch: "all", License: "MPL-2.0"},
	{Name: "libexpat1", Version: "2.5.0-1+deb12u1", License: "MIT"},
	{Name: "libncursesw6", Version: "6.4-4", License: "MIT"},
	{Name: "libreadline8", Version: "8.2-1.3", License: "GPL-3.0-or-later"},
	{Name: "libssl3", Version: "3.0.15-1~deb12u1", License: "Apache-2.0"},
	{Name: "netbase", Version: "6.4", Arch: "all", License: "GPL-2.0-only"},
	{Name: "openssl", Version: "3.0.15-1~deb12u1", License: "Apache-2.0"},
	{Name: "readline-common", Version: "8.2-1.3", Arch: "all", License: "GPL-3.0-or-later"},
}

// debLate is what a later RUN installs: the SBOM was made without it.
var debLate = []Pkg{
	{Name: "curl", Version: "7.88.1-10+deb12u8", License: "curl"},
	{Name: "libcurl4", Version: "7.88.1-10+deb12u8", License: "curl"},
	{Name: "netcat-openbsd", Version: "1.219-1", License: "BSD-2-Clause"},
}

// DemoPackagesDeclared counts what the demo's SBOM declares.
const DemoPackagesDeclared = 70

// DemoPackages is a Debian-based Python image whose SBOM and package
// database disagree, as an SBOM made from another build would: three
// packages installed after it was made are undeclared, one library is
// declared at an older version, one declared library is not installed, and
// the Python packages and an entry without a purl cannot be checked.
func DemoPackages(ref string) Image {
	l1 := debBase
	l2 := append(append([]Pkg{}, debBase...), debRuntime...)
	l4 := append(append([]Pkg{}, l2...), debLate...)
	sort.Slice(l4, func(i, j int) bool { return l4[i].Name < l4[j].Name })

	var declared []Pkg
	for _, p := range l2 {
		if p.Name == "libssl3" {
			p.Version = "3.0.11-1~deb12u2" // the SBOM is from an older build
		}
		declared = append(declared, p)
	}
	declared = append(declared,
		Pkg{Name: "libgnutls30", Version: "3.7.9-2+deb12u3", License: "LGPL-2.1-or-later"},
		Pkg{Name: "pip", Version: "24.0", Type: "pypi", PURL: "pkg:pypi/pip@24.0", License: "MIT"},
		Pkg{Name: "setuptools", Version: "69.5.1", PURL: "pkg:pypi/setuptools@69.5.1", License: "MIT"},
		Pkg{Name: "wheel", Version: "0.43.0", PURL: "pkg:pypi/wheel@0.43.0", License: "MIT"},
		Pkg{Name: "requests", Version: "2.32.3", PURL: "pkg:pypi/requests@2.32.3", License: "Apache-2.0"},
		Pkg{Name: "python", Version: "3.12.4", PURL: "-", License: "PSF-2.0"},
		Pkg{Name: "golang.org/x/sys", Version: "v0.20.0", PURL: "pkg:golang/golang.org/x/sys@v0.20.0", License: "BSD-3-Clause"},
	)
	return Image{
		User: "10001", Cmd: []string{"python3"},
		Healthcheck: []string{"CMD", "python3", "-c", "pass"},
		RepoTags:    []string{ref},
		Layers: []Layer{
			{CreatedBy: "/bin/sh -c #(nop) ADD file:5d6b639e8b6bcc01 in / ", Files: []File{
				{Path: "etc/os-release", Body: []byte("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n")},
				{Path: "var/lib/dpkg/status", Body: DpkgStatus(l1)},
			}},
			{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y --no-install-recommends ca-certificates openssl netbase && rm -rf /var/lib/apt/lists/* # buildkit", Files: []File{
				{Path: "var/lib/dpkg/status", Body: DpkgStatus(l2)},
				{Path: "etc/ssl/certs/ca-certificates.crt", Body: []byte("# certificates\n")},
			}},
			{CreatedBy: "RUN /bin/sh -c pip install --no-cache-dir requests # buildkit", Files: []File{
				{Path: "usr/local/lib/python3.12/site-packages/requests/__init__.py", Body: []byte("__version__ = '2.32.3'\n")},
			}},
			{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y curl netcat-openbsd && rm -rf /var/lib/apt/lists/* # buildkit", Files: []File{
				{Path: "var/lib/dpkg/status", Body: DpkgStatus(l4)},
				{Path: "usr/bin/curl", Body: ELF{PIE: true, Dynamic: true, RELRO: true, BindNow: true, Canary: true}.Bytes(), Mode: 0o755},
			}},
		},
		SBOM: true, SBOMDoc: SPDX("debian", declared),
	}
}
