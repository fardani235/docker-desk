package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ImageSchema names the native schema of an image document.
const ImageSchema = "docker-desk/image-report/v1"

// Image is everything the Findings analysis computed about one image and
// platform, joined with the daemon's live state as the tab drew it.
type Image struct {
	Schema string `json:"schema"`
	Tool   Tool   `json:"tool"`
	// ToolVersion is the version written into Tool; set by the caller.
	ToolVersion string `json:"-"`

	ID        string `json:"id"`
	Reference string `json:"reference"`
	Platform  string `json:"platform"`
	Layout    string `json:"layout"`
	BytesRead int64  `json:"bytes_read"`

	Config       Config        `json:"config"`
	Layers       []Layer       `json:"layers"`
	Attestations []Attestation `json:"attestations"`
	// RootUser: the config runs processes as UID 0. FinalSecrets counts
	// secrets still in the final filesystem.
	RootUser     bool `json:"root_user"`
	FinalSecrets int  `json:"final_secrets"`

	Findings []Finding `json:"findings"`
	// Omitted counts findings the analysis's bounds dropped.
	Omitted int `json:"omitted"`
	// Notes are what the archive could not tell.
	Notes []string `json:"notes"`

	// Packages is nil when the analysis kept no package set.
	Packages *Packages `json:"packages"`
}

// Config is the part of the image config the analysis read. Environment
// variables are named, never valued (B23).
type Config struct {
	Present      bool     `json:"present"`
	OS           string   `json:"os,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	User         string   `json:"user"`
	WorkingDir   string   `json:"working_dir,omitempty"`
	Entrypoint   []string `json:"entrypoint"`
	Cmd          []string `json:"cmd"`
	Healthcheck  []string `json:"healthcheck"`
	EnvNames     []string `json:"env_names"`
}

// Layer is one layer of the archive.
type Layer struct {
	Index       int    `json:"index"`
	Digest      string `json:"digest"`
	DiffID      string `json:"diff_id"`
	Verified    bool   `json:"diff_id_verified"`
	Compression string `json:"compression"`
	Stored      int64  `json:"stored_bytes"`
	Size        int64  `json:"content_bytes"`
	Files       int    `json:"files"`
	Dirs        int    `json:"dirs"`
	Whiteouts   int    `json:"whiteouts"`
	Opaques     int    `json:"opaque_dirs"`
	Instruction string `json:"instruction,omitempty"`
	Unreadable  string `json:"unreadable,omitempty"`
	Missing     bool   `json:"missing,omitempty"`
}

// Attestation is an in-toto attestation the archive's index lists.
type Attestation struct {
	PredicateType string `json:"predicate_type"`
	Digest        string `json:"digest"`
	Present       bool   `json:"present"`
	// Packages is the SBOM's package count, or -1 when not known.
	Packages int    `json:"packages"`
	Format   string `json:"format,omitempty"`
}

// Finding is one finding. A secret's value is never here: Masked says that
// the finding had one, which stays on the screen (F9).
type Finding struct {
	Rule        string   `json:"rule"`
	Group       string   `json:"group"`
	Source      string   `json:"source"`
	Certainty   string   `json:"certainty"`
	Layer       int      `json:"layer"`
	Path        string   `json:"path,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	Evidence    string   `json:"evidence"`
	Reason      string   `json:"reason"`
	Instruction string   `json:"instruction,omitempty"`
	Unknown     string   `json:"unknown,omitempty"`
	Detail      []string `json:"detail,omitempty"`
	Masked      bool     `json:"value_masked,omitempty"`
}

// Packages is the declared list against the installed one, joined.
type Packages struct {
	SBOM      SBOM       `json:"sbom"`
	Databases []Database `json:"databases"`
	// Counts are the rows by state, in the view's words.
	Counts []StateCount `json:"counts"`
	Rows   []Package    `json:"rows"`
}

// SBOM is the declared side.
type SBOM struct {
	Listed  bool   `json:"listed"`
	Format  string `json:"format,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Total   int    `json:"total"`
	Kept    int    `json:"kept"`
	Others  int    `json:"others"`
	Unknown string `json:"unknown,omitempty"`
}

// Database is one ecosystem's installed side. State is "read", "absent"
// or "unknown".
type Database struct {
	Ecosystem string   `json:"ecosystem"`
	Manager   string   `json:"manager"`
	State     string   `json:"state"`
	Path      string   `json:"path,omitempty"`
	Layer     int      `json:"layer"`
	Total     int      `json:"total"`
	Kept      int      `json:"kept"`
	Unknown   string   `json:"unknown,omitempty"`
	Notes     []string `json:"notes"`
}

// StateCount is how many rows are in one state.
type StateCount struct {
	State string `json:"state"`
	Rows  int    `json:"rows"`
}

// Package is one row of the join. State is the view's word for it: both,
// version differs, declared only, undeclared, not checkable. Why says why a
// row is not checkable. A side the join did not find is nil; a version
// neither side states is "" — never filled in.
type Package struct {
	State     string     `json:"state"`
	Type      string     `json:"type"`
	Why       string     `json:"why,omitempty"`
	Declared  *Declared  `json:"declared"`
	Installed *Installed `json:"installed"`
}

// Declared is what the SBOM says of a package.
type Declared struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license,omitempty"`
	PURL    string `json:"purl,omitempty"`
	Arch    string `json:"arch,omitempty"`
}

// Installed is what the package database says of it.
type Installed struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Arch        string `json:"arch,omitempty"`
	License     string `json:"license,omitempty"`
	Layer       int    `json:"layer"`
	LayerUnsure string `json:"layer_unsure,omitempty"`
	Source      string `json:"source,omitempty"`
	Pseudo      bool   `json:"not_a_package,omitempty"`
}

// Name is the package's name, from whichever side has it.
func (p Package) Name() string {
	if p.Installed != nil {
		return p.Installed.Name
	}
	if p.Declared != nil {
		return p.Declared.Name
	}
	return ""
}

// Version is the installed version, or else the declared one, or "".
func (p Package) Version() string {
	if p.Installed != nil {
		return p.Installed.Version
	}
	if p.Declared != nil {
		return p.Declared.Version
	}
	return ""
}

// License is the declared licence, or else the installed one.
func (p Package) License() string {
	if p.Declared != nil && p.Declared.License != "" {
		return p.Declared.License
	}
	if p.Installed != nil {
		return p.Installed.License
	}
	return ""
}

func (p Package) declaredVersion() string {
	if p.Declared != nil {
		return p.Declared.Version
	}
	return ""
}

func (p Package) installedVersion() string {
	if p.Installed != nil {
		return p.Installed.Version
	}
	return ""
}

func (p Package) purl() string {
	if p.Declared != nil {
		return p.Declared.PURL
	}
	return ""
}

func (p Package) arch() string {
	if p.Installed != nil && p.Installed.Arch != "" {
		return p.Installed.Arch
	}
	if p.Declared != nil {
		return p.Declared.Arch
	}
	return ""
}

// ---- ordering -----------------------------------------------------------------

// groupRank is the order of the Findings tab's groups; a group it does not
// know sorts after them, by name.
var groupRank = map[string]int{
	"config": 0, "layer": 1, "filesystem": 2, "secrets": 3, "binaries": 4, "supply": 5, "live": 6,
}

func rankOf(g string) int {
	if r, ok := groupRank[g]; ok {
		return r
	}
	return len(groupRank)
}

var certaintyRank = map[string]int{"certain": 0, "likely": 1, "hint": 2}

// less compares key lists element by element.
func less(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func pad(n int) string { return fmt.Sprintf("%010d", n) }

func findingKey(f Finding) []string {
	return []string{
		pad(rankOf(f.Group)), f.Group, pad(f.Layer), f.Rule, f.Path, f.Subject, f.Evidence,
		pad(certaintyRank[f.Certainty]), f.Certainty, f.Source, f.Reason, f.Instruction, f.Unknown,
		strings.Join(f.Detail, "\x00"), strconv.FormatBool(f.Masked),
	}
}

// packageKey orders packages by name, then version, then everything else
// that can tell two rows apart, so that no two distinct rows tie.
func packageKey(p Package) []string {
	return []string{
		p.Name(), p.Version(), p.Type, p.arch(), p.declaredVersion(), p.installedVersion(),
		p.purl(), p.State, p.Why, p.License(),
	}
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool { return less(findingKey(fs[i]), findingKey(fs[j])) })
}

func sortPackages(ps []Package) {
	sort.SliceStable(ps, func(i, j int) bool { return less(packageKey(ps[i]), packageKey(ps[j])) })
}

var ecoRank = map[string]int{"deb": 0, "apk": 1, "rpm": 2}

var stateOrder = []string{"undeclared", "version differs", "declared only", "not checkable", "both"}

func strs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string{}, s...)
}

func sorted(s []string) []string {
	out := strs(s)
	sort.Strings(out)
	return out
}

// normalise returns a copy of img with every list in its document order,
// and no nil list where the schema has an array. The caller's value is not
// changed.
func normalise(img Image) Image {
	out := img
	out.Schema = ImageSchema
	out.Tool = toolOf(img.ToolVersion)
	out.Config.Entrypoint = strs(img.Config.Entrypoint)
	out.Config.Cmd = strs(img.Config.Cmd)
	out.Config.Healthcheck = strs(img.Config.Healthcheck)
	out.Config.EnvNames = sorted(img.Config.EnvNames)

	out.Layers = append([]Layer{}, img.Layers...)
	sort.SliceStable(out.Layers, func(i, j int) bool {
		a, b := out.Layers[i], out.Layers[j]
		return less([]string{pad(a.Index), a.Digest, a.DiffID}, []string{pad(b.Index), b.Digest, b.DiffID})
	})
	out.Attestations = append([]Attestation{}, img.Attestations...)
	sort.SliceStable(out.Attestations, func(i, j int) bool {
		a, b := out.Attestations[i], out.Attestations[j]
		return less([]string{a.PredicateType, a.Digest, a.Format}, []string{b.PredicateType, b.Digest, b.Format})
	})
	out.Findings = make([]Finding, len(img.Findings))
	for i, f := range img.Findings {
		f.Detail = append([]string(nil), f.Detail...) // evidence order: kept
		out.Findings[i] = f
	}
	sortFindings(out.Findings)
	out.Notes = sorted(img.Notes)

	if img.Packages != nil {
		p := *img.Packages
		p.Databases = make([]Database, len(img.Packages.Databases))
		for i, d := range img.Packages.Databases {
			d.Notes = sorted(d.Notes)
			p.Databases[i] = d
		}
		sort.SliceStable(p.Databases, func(i, j int) bool {
			a, b := p.Databases[i], p.Databases[j]
			ra, rb := ecoRank[a.Ecosystem], ecoRank[b.Ecosystem]
			if _, ok := ecoRank[a.Ecosystem]; !ok {
				ra = len(ecoRank)
			}
			if _, ok := ecoRank[b.Ecosystem]; !ok {
				rb = len(ecoRank)
			}
			return less([]string{pad(ra), a.Ecosystem, a.Manager}, []string{pad(rb), b.Ecosystem, b.Manager})
		})
		p.Rows = make([]Package, len(img.Packages.Rows))
		for i, r := range img.Packages.Rows {
			if r.Declared != nil {
				d := *r.Declared
				r.Declared = &d
			}
			if r.Installed != nil {
				in := *r.Installed
				r.Installed = &in
			}
			p.Rows[i] = r
		}
		sortPackages(p.Rows)
		// The counts are recomputed from the rows, in the view's order of
		// `s`, so that they cannot disagree with them.
		n := map[string]int{}
		for _, r := range p.Rows {
			n[r.State]++
		}
		p.Counts = nil
		for _, s := range stateOrder {
			p.Counts = append(p.Counts, StateCount{State: s, Rows: n[s]})
			delete(n, s)
		}
		var rest []string
		for s := range n {
			rest = append(rest, s)
		}
		sort.Strings(rest)
		for _, s := range rest {
			p.Counts = append(p.Counts, StateCount{State: s, Rows: n[s]})
		}
		out.Packages = &p
	}
	return out
}

// marshal writes v as indented JSON with a final newline. HTML characters
// are not escaped: the file is read by people and by JSON parsers, not
// embedded in a page.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ImageJSON is the native document: everything the analysis computed.
func ImageJSON(img Image) ([]byte, error) {
	return marshal(normalise(img))
}

// ---- text ---------------------------------------------------------------------

// oneLine makes s safe for one field of one line: tabs, line breaks and
// every other control character are written as escapes, so no value can
// start a line of its own or shift a column.
func oneLine(s string) string {
	if s == "" {
		return "-"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ImageText lists the findings grouped by group, one line per finding,
// tab-separated, in the document order. Lines beginning with # are not
// findings.
func ImageText(img Image) []byte {
	n := normalise(img)
	var b strings.Builder
	fmt.Fprintf(&b, "# docker-desk %s findings\n", n.Tool.Version)
	fmt.Fprintf(&b, "# image\t%s\n", oneLine(n.ID))
	fmt.Fprintf(&b, "# reference\t%s\n", oneLine(n.Reference))
	fmt.Fprintf(&b, "# platform\t%s\n", oneLine(n.Platform))
	fmt.Fprintf(&b, "# findings\t%d", len(n.Findings))
	if n.Omitted > 0 {
		fmt.Fprintf(&b, " (%d more dropped by the analysis's bounds)", n.Omitted)
	}
	b.WriteString("\n")
	for _, note := range n.Notes {
		fmt.Fprintf(&b, "# unknown\t%s\n", oneLine(note))
	}
	b.WriteString("# columns\trule\tcertainty\tsource\tlayer\tpath\tsubject\tevidence\treason\n")
	group := "\x00"
	for _, f := range n.Findings {
		if f.Group != group {
			group = f.Group
			count := 0
			for _, g := range n.Findings {
				if g.Group == group {
					count++
				}
			}
			fmt.Fprintf(&b, "\n## %s\t%d\n", oneLine(group), count)
		}
		layer := "-"
		if f.Layer > 0 {
			layer = "L" + strconv.Itoa(f.Layer)
		}
		ev := f.Evidence
		if f.Masked {
			ev += " (value masked)"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			oneLine(f.Rule), oneLine(f.Certainty), oneLine(f.Source), layer,
			oneLine(f.Path), oneLine(f.Subject), oneLine(ev), oneLine(f.Reason))
	}
	if len(n.Findings) == 0 {
		b.WriteString("\n# nothing to report\n")
	}
	return []byte(b.String())
}
