package report

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// The two SBOM formats carry the packages and nothing else: the rows of the
// join, each with the state the view gives it. A row's state, and why it is
// not checkable, go where each format keeps what it has no field for (an
// SPDX package's comment, a CycloneDX component's properties). Nothing is
// filled in: a version neither side states is left out, a purl is the
// SBOM's own and is written only beside the version it names, and no
// licence is turned into an SPDX expression the source did not write.

// spdxCreated is SPDX's required creation time. A document of the image is
// the same whenever it is written, so the time is fixed (X6).
const spdxCreated = "1970-01-01T00:00:00Z"

type spdxDoc struct {
	SPDXVersion       string        `json:"spdxVersion"`
	DataLicense       string        `json:"dataLicense"`
	SPDXID            string        `json:"SPDXID"`
	Name              string        `json:"name"`
	DocumentNamespace string        `json:"documentNamespace"`
	CreationInfo      spdxCreation  `json:"creationInfo"`
	Comment           string        `json:"comment"`
	Packages          []spdxPackage `json:"packages"`
	Relationships     []spdxRel     `json:"relationships"`
}

type spdxCreation struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
	Comment  string   `json:"comment"`
}

type spdxPackage struct {
	SPDXID           string       `json:"SPDXID"`
	Name             string       `json:"name"`
	VersionInfo      string       `json:"versionInfo,omitempty"`
	DownloadLocation string       `json:"downloadLocation"`
	FilesAnalyzed    bool         `json:"filesAnalyzed"`
	LicenseConcluded string       `json:"licenseConcluded"`
	LicenseDeclared  string       `json:"licenseDeclared"`
	CopyrightText    string       `json:"copyrightText"`
	ExternalRefs     []spdxExtRef `json:"externalRefs,omitempty"`
	Comment          string       `json:"comment"`
}

type spdxExtRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type spdxRel struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

// pkgPURL is the purl a row may be written with: the SBOM's, unless the
// installed version differs from the one it names.
func pkgPURL(p Package) string {
	if p.Declared == nil || p.Declared.PURL == "" || p.State == "version differs" {
		return ""
	}
	return p.Declared.PURL
}

// pkgFacts are the row's facts the formats have no field for, in a fixed
// order.
func pkgFacts(p Package) [][2]string {
	out := [][2]string{{"state", p.State}}
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	add("why", p.Why)
	add("type", p.Type)
	if p.Declared != nil {
		add("declared-version", p.Declared.Version)
		if p.State == "version differs" {
			add("declared-purl", p.Declared.PURL)
		}
		add("declared-license", p.Declared.License)
	} else {
		out = append(out, [2]string{"declared", "no"})
	}
	if p.Installed != nil {
		add("installed-version", p.Installed.Version)
		add("installed-license", p.Installed.License)
		if p.Installed.Layer > 0 {
			add("layer", strconv.Itoa(p.Installed.Layer))
		}
		add("layer-unsure", p.Installed.LayerUnsure)
		add("database", p.Installed.Source)
		if p.Installed.Pseudo {
			add("not-a-package", "true")
		}
	} else {
		out = append(out, [2]string{"installed", "no"})
	}
	add("arch", p.arch())
	return out
}

func packagesOf(img Image) (Image, []Package) {
	n := normalise(img)
	if n.Packages == nil {
		return n, nil
	}
	return n, n.Packages.Rows
}

// contentID is a digest of what the packages document says, so that two
// documents of one image with different contents are told apart.
func contentID(n Image, rows []Package) string {
	b, _ := marshal(struct {
		ID, Platform string
		Rows         []Package
	}{n.ID, n.Platform, rows})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ImageSPDX is the packages as an SPDX 2.3 JSON document.
func ImageSPDX(img Image) ([]byte, error) {
	n, rows := packagesOf(img)
	cid := contentID(n, rows)
	doc := spdxDoc{
		SPDXVersion: "SPDX-2.3",
		DataLicense: "CC0-1.0",
		SPDXID:      "SPDXRef-DOCUMENT",
		Name:        fmt.Sprintf("docker-desk packages of %s (%s)", orNone(n.Reference), orNone(n.Platform)),
		DocumentNamespace: fmt.Sprintf("https://docker-desk.invalid/spdx/%s/%s/%s",
			slug(strings.TrimPrefix(n.ID, "sha256:")), slug(n.Platform), cid[:32]),
		CreationInfo: spdxCreation{
			Created:  spdxCreated,
			Creators: []string{"Tool: docker-desk-" + n.Tool.Version},
			Comment:  "The creation time is fixed: the document depends on the image alone, and is the same bytes whenever it is written.",
		},
		Comment:       spdxComment(n),
		Packages:      []spdxPackage{},
		Relationships: []spdxRel{},
	}
	for i, p := range rows {
		id := "SPDXRef-Package-" + strconv.Itoa(i+1)
		sp := spdxPackage{
			SPDXID: id, Name: p.Name(), VersionInfo: p.Version(),
			DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION",
			LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
		}
		if sp.Name == "" {
			sp.Name = "NOASSERTION"
		}
		if u := pkgPURL(p); u != "" {
			sp.ExternalRefs = []spdxExtRef{{ReferenceCategory: "PACKAGE-MANAGER", ReferenceType: "purl", ReferenceLocator: u}}
		}
		var parts []string
		for _, kv := range pkgFacts(p) {
			parts = append(parts, kv[0]+": "+kv[1])
		}
		sp.Comment = "docker-desk " + strings.Join(parts, "; ")
		doc.Packages = append(doc.Packages, sp)
		doc.Relationships = append(doc.Relationships, spdxRel{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: id})
	}
	return marshal(doc)
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func spdxComment(n Image) string {
	s := "The packages of image " + orNone(n.ID) + " for " + orNone(n.Platform) +
		": what its SBOM declares joined with what its own package databases list. Each package's comment gives its state; a package that is not checkable keeps that state and its reason."
	if n.Packages == nil {
		s += " The analysis kept no package list."
	}
	return s
}

// ---- CycloneDX ------------------------------------------------------------------

type cdxDoc struct {
	BOMFormat    string         `json:"bomFormat"`
	SpecVersion  string         `json:"specVersion"`
	SerialNumber string         `json:"serialNumber"`
	Version      int            `json:"version"`
	Metadata     cdxMetadata    `json:"metadata"`
	Components   []cdxComponent `json:"components"`
}

type cdxMetadata struct {
	Tools      cdxTools     `json:"tools"`
	Component  cdxComponent `json:"component"`
	Properties []cdxProp    `json:"properties,omitempty"`
}

type cdxTools struct {
	Components []cdxComponent `json:"components"`
}

type cdxComponent struct {
	Type       string       `json:"type"`
	BOMRef     string       `json:"bom-ref,omitempty"`
	Name       string       `json:"name"`
	Version    string       `json:"version,omitempty"`
	PURL       string       `json:"purl,omitempty"`
	Licenses   []cdxLicense `json:"licenses,omitempty"`
	Properties []cdxProp    `json:"properties,omitempty"`
}

type cdxLicense struct {
	License cdxLicenseName `json:"license"`
}

type cdxLicenseName struct {
	Name string `json:"name"`
}

type cdxProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// uuid5 is an RFC 4122 version-5 (name-based, SHA-1) UUID: derived from the
// document's content, so it is the same for the same document.
func uuid5(name string) string {
	ns := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8} // the URL namespace
	h := sha1.New()
	h.Write(ns)
	h.Write([]byte(name))
	u := h.Sum(nil)[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	x := hex.EncodeToString(u)
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}

// ImageCycloneDX is the packages as a CycloneDX 1.5 JSON document.
func ImageCycloneDX(img Image) ([]byte, error) {
	n, rows := packagesOf(img)
	cid := contentID(n, rows)
	subject := cdxComponent{Type: "container", BOMRef: "image", Name: orNone(n.Reference), Version: n.ID}
	if n.Platform != "" {
		subject.Properties = []cdxProp{{Name: "docker-desk:platform", Value: n.Platform}}
	}
	doc := cdxDoc{
		BOMFormat: "CycloneDX", SpecVersion: "1.5",
		SerialNumber: "urn:uuid:" + uuid5("https://docker-desk.invalid/cyclonedx/"+cid),
		Version:      1,
		Metadata: cdxMetadata{
			Tools:     cdxTools{Components: []cdxComponent{{Type: "application", Name: "docker-desk", Version: n.Tool.Version}}},
			Component: subject,
		},
		Components: []cdxComponent{},
	}
	if n.Packages == nil {
		doc.Metadata.Properties = []cdxProp{{Name: "docker-desk:packages", Value: "the analysis kept no package list"}}
	}
	for i, p := range rows {
		c := cdxComponent{Type: "library", BOMRef: "pkg-" + strconv.Itoa(i+1), Name: p.Name(), Version: p.Version(), PURL: pkgPURL(p)}
		if l := p.License(); l != "" {
			c.Licenses = []cdxLicense{{License: cdxLicenseName{Name: l}}}
		}
		for _, kv := range pkgFacts(p) {
			c.Properties = append(c.Properties, cdxProp{Name: "docker-desk:" + kv[0], Value: kv[1]})
		}
		doc.Components = append(doc.Components, c)
	}
	return marshal(doc)
}
