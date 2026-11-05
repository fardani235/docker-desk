// Package forensics reads an image's own archive — the stream the daemon
// sends for GET /images/get — and reports what is inside it: the config, the
// ordered layers, every file each layer adds or deletes, and what those
// files are. Findings are joined with the daemon's live state by LiveFindings.
//
// Three rules shape it (docs/DECISIONS.md, F1–F12):
//
//   - It streams. Nothing is written to disk and every buffer has a bound
//     (Limits); what a bound cuts off is reported as unknown, not guessed.
//   - Every finding names its evidence, where that evidence came from and
//     what the evidence cannot tell. There is no severity score.
//   - A secret's value lives only in a Secret, which prints as a mask in
//     every format — %v, %+v, %#v, JSON, text — so it cannot reach a job
//     record, an error, a log line or a file by accident.
package forensics

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/secret"
)

// Source says where a finding's evidence came from.
type Source string

const (
	// SourceDaemon: the daemon's own API answers (container state, flags,
	// image references).
	SourceDaemon Source = "daemon"
	// SourceImage: the bytes of the image archive.
	SourceImage Source = "image"
	// SourceDesk: the desk's own join of the two.
	SourceDesk Source = "desk"
)

// Certainty is how strongly the evidence supports the finding.
type Certainty int

const (
	// Certain: the evidence is the fact (a mode bit, a config field, a
	// structure that matches a format exactly).
	Certain Certainty = iota
	// Likely: the evidence fits, but has an innocent reading too.
	Likely
	// Hint: worth a look; the evidence is circumstantial.
	Hint
)

func (c Certainty) String() string {
	switch c {
	case Certain:
		return "certain"
	case Likely:
		return "likely"
	}
	return "hint"
}

// Group is the family a rule belongs to.
type Group string

const (
	GroupConfig     Group = "config"
	GroupLayer      Group = "layer"
	GroupFilesystem Group = "filesystem"
	GroupSecrets    Group = "secrets"
	GroupBinaries   Group = "binaries"
	GroupSupply     Group = "supply"
	GroupLive       Group = "live"
)

var groupOrder = map[Group]int{
	GroupConfig: 0, GroupLayer: 1, GroupFilesystem: 2, GroupSecrets: 3,
	GroupBinaries: 4, GroupSupply: 5, GroupLive: 6,
}

// Finding is one thing the analysis found.
type Finding struct {
	Rule      string // e.g. "secret.deleted"
	Group     Group
	Source    Source
	Certainty Certainty
	// Layer is the 1-based layer the evidence is in; 0 when the finding is
	// about the config, the archive as a whole, or live state.
	Layer int
	Path  string
	// Evidence is what was seen, with any secret masked.
	Evidence string
	// Reason is one line: why this is reported.
	Reason string
	// Instruction is the build instruction that produced the layer, when
	// the history maps onto the layers; "" otherwise (never a guess).
	Instruction string
	// Unknown is what the evidence cannot tell.
	Unknown string
	// Detail is extra evidence (a module list), shown with the finding.
	Detail []string
	// Secret holds the unmasked value for an explicit reveal. It prints as
	// a mask; see Secret.
	Secret Secret
	// Subject names the container a live finding is about.
	Subject string
}

// Key identifies the finding across recomputations, for a cursor to follow.
func (f Finding) Key() string {
	return fmt.Sprintf("%s|%d|%s|%s|%s", f.Rule, f.Layer, f.Path, f.Subject, f.Evidence)
}

// Secret is a value that must never be printed except on purpose. It is
// the desk-wide type of internal/secret; see that package.
type Secret = secret.Secret

// NewSecret wraps a value.
func NewSecret(v string) Secret { return secret.New(v) }

// Order is how findings are listed.
type Order int

const (
	OrderLayer Order = iota
	OrderGroup
	OrderCertainty
	orderCount
)

func (o Order) String() string {
	switch o {
	case OrderGroup:
		return "group"
	case OrderCertainty:
		return "certainty"
	}
	return "layer"
}

// Next is the order after o, cycling.
func (o Order) Next() Order { return (o + 1) % orderCount }

// Sort orders findings in place. Every order breaks ties the same way, so
// a list holds still between frames.
func Sort(fs []Finding, o Order) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		keys := func(f Finding) []int {
			switch o {
			case OrderGroup:
				return []int{groupOrder[f.Group], f.Layer, int(f.Certainty)}
			case OrderCertainty:
				return []int{int(f.Certainty), f.Layer, groupOrder[f.Group]}
			}
			return []int{f.Layer, groupOrder[f.Group], int(f.Certainty)}
		}
		ka, kb := keys(a), keys(b)
		for k := range ka {
			if ka[k] != kb[k] {
				return ka[k] < kb[k]
			}
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Subject < b.Subject
	})
}

// Layer is what the analysis learned about one layer.
type Layer struct {
	Index  int    // 1-based
	Path   string // the layer's path inside the archive
	Digest string // sha256 of the blob as stored
	DiffID string // sha256 of the uncompressed tar
	// Verified is set when DiffID equals the config's rootfs entry.
	Verified bool
	// Compression is "none", "gzip" or "zstd".
	Compression string
	Stored      int64 // bytes in the archive
	Size        int64 // bytes of file content in the layer
	Files       int   // non-directory entries added
	Dirs        int
	Whiteouts   int // paths deleted from lower layers
	Opaques     int // directories whose lower contents are hidden
	// Instruction produced the layer; "" when the history does not map.
	Instruction string
	// Unreadable says why the layer's contents could not be walked.
	Unreadable string
	// Missing is set when the manifest lists a layer the archive lacks.
	Missing bool
}

// Attestation is an in-toto attestation listed in the archive's index.
type Attestation struct {
	PredicateType string
	Digest        string
	// Present is set when its blob is in the archive and was read.
	Present bool
	// Packages is the SBOM's package count, or -1 when not known.
	Packages int
	// Format names the SBOM format ("SPDX-2.3", "CycloneDX 1.5").
	Format string
}

// IsSBOM reports whether the attestation is a software bill of materials.
func (a Attestation) IsSBOM() bool {
	p := strings.ToLower(a.PredicateType)
	return strings.Contains(p, "spdx") || strings.Contains(p, "cyclonedx")
}

// IsProvenance reports whether the attestation records how the image was built.
func (a Attestation) IsProvenance() bool {
	return strings.Contains(strings.ToLower(a.PredicateType), "slsa.dev/provenance")
}

// Config is the part of the image config the rules read.
type Config struct {
	OS, Architecture, Variant string
	User                      string
	Cmd, Entrypoint           []string
	Env                       []string
	WorkingDir                string
	// Healthcheck is the test command; nil when there is none.
	Healthcheck []string
	Present     bool // the config was found and parsed
}

// Platform is the config's os/arch[/variant].
func (c Config) Platform() string {
	s := c.OS + "/" + c.Architecture
	if c.Variant != "" {
		s += "/" + c.Variant
	}
	return s
}

// Report is the result of one analysis.
type Report struct {
	ImageID  string
	Platform string // the platform asked for
	// Layout is "docker-archive" (classic) or "oci" (OCI image layout).
	Layout       string
	Config       Config
	Layers       []Layer
	Attestations []Attestation
	// Findings come from the archive alone; LiveFindings adds the rest.
	Findings  []Finding
	BytesRead int64
	// Notes are what the archive could not tell, archive-wide.
	Notes []string
	// Omitted counts findings dropped by Limits.Findings.
	Omitted int
	// RootUser is set when the config runs processes as UID 0.
	RootUser bool
	// FinalSecrets counts secrets still in the final filesystem.
	FinalSecrets int
	// Packages is what the SBOM declares and the package databases list.
	Packages *PackageSet
}

// TotalSize is the sum of the layers' content.
func (r *Report) TotalSize() int64 {
	var n int64
	for _, l := range r.Layers {
		n += l.Size
	}
	return n
}
