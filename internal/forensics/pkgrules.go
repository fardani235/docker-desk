package forensics

import (
	"fmt"
	"strings"
)

// declared picks the SBOM the package view compares: the first SBOM
// attestation whose document was read, else the first listed.
func (a *analyser) declared(rep *Report) SBOMPackages {
	var sboms []Attestation
	for _, at := range rep.Attestations {
		if at.IsSBOM() {
			sboms = append(sboms, at)
		}
	}
	if len(sboms) == 0 {
		return SBOMPackages{}
	}
	pick := sboms[0]
	for _, at := range sboms {
		if sc, ok := a.sboms[blobPath(at.Digest)]; ok && sc.err == "" && sc.format != "" {
			pick = at
			break
		}
	}
	out := SBOMPackages{Listed: true, Digest: pick.Digest, Others: len(sboms) - 1}
	sc, scanned := a.sboms[blobPath(pick.Digest)]
	switch {
	case !pick.Present:
		out.Unknown = "its blob is not in the archive"
	case scanned && sc.err != "":
		out.Unknown = sc.err
	case !scanned || sc.format == "":
		out.Unknown = "the document is neither SPDX nor CycloneDX JSON"
	default:
		out.Format, out.Packages, out.Total = sc.format, sc.recs, sc.total
	}
	return out
}

// checkedAgainst names the ecosystems whose declared packages were checked.
func checkedAgainst(p *PackageSet) string {
	if p == nil || !p.SBOM.Known() {
		return ""
	}
	var out []string
	for _, d := range p.DBs {
		if d.State == DBRead {
			out = append(out, d.Ecosystem+" ("+d.Manager+")")
		}
	}
	return strings.Join(out, ", ")
}

// packageRules turns the join's disagreements about OS packages into
// findings: one for what is installed and undeclared, one hint for what is
// declared and not installed as declared.
func (a *analyser) packageRules(b *builder, rep *Report, instr []string) {
	rows := JoinPackages(rep.Packages)
	SortPackages(rows, PkgByName)
	var undeclared, drift []PkgRow
	for _, r := range rows {
		switch {
		case r.State == StateUndeclared:
			undeclared = append(undeclared, r)
		case (r.State == StateDeclaredOnly || r.State == StateDiffers) && IsOS(r.Type):
			drift = append(drift, r)
		}
	}
	if len(undeclared) > 0 {
		layers := map[int]bool{}
		sources := map[string]bool{}
		var names, detail []string
		for _, r := range undeclared {
			ip := r.Installed
			layers[ip.Layer] = true
			sources[ip.Source] = true
			if len(names) < 6 {
				names = append(names, ip.Name)
			}
			if len(detail) < 200 {
				detail = append(detail, fmt.Sprintf("%s %s  (%s, %s)", ip.Name, ip.Version, r.Type, layerWords(ip.Layer)))
			}
		}
		ev := fmt.Sprintf("%d installed %s the SBOM does not list: %s", len(undeclared), plural(len(undeclared), "package", "packages"), strings.Join(names, ", "))
		if len(undeclared) > len(names) {
			ev += fmt.Sprintf(" (+%d more)", len(undeclared)-len(names))
		}
		f := Finding{
			Rule: "supply.undeclared", Group: GroupSupply, Source: SourceImage, Certainty: Certain,
			Evidence: ev, Detail: detail,
			Reason: "The image's own package database lists them as installed; the SBOM that travels with it does not declare them. " +
				"Whoever relies on the SBOM, for licences or for vulnerabilities, does not know they are there.",
			Unknown: "why: the SBOM may have been made before they were installed (from an earlier stage or the base image), " +
				"by a tool that skips them, or edited afterwards. The image's bytes cannot tell which.",
		}
		if len(sources) == 1 {
			for s := range sources {
				f.Path = s
			}
		}
		if len(layers) == 1 {
			for l := range layers {
				if l > 0 && l <= len(instr) {
					f.Layer, f.Instruction = l, instr[l-1]
				}
			}
		}
		b.add(f)
	}
	if len(drift) > 0 {
		var only, differs int
		var detail []string
		for _, r := range drift {
			d := r.Declared
			line := fmt.Sprintf("%s %s  (%s): declared, not installed", d.Name, d.Version, r.Type)
			if r.State == StateDiffers {
				differs++
				line = fmt.Sprintf("%s: declared %s, installed %s  (%s)", d.Name, d.Version, r.Installed.Version, r.Type)
			} else {
				only++
			}
			if len(detail) < 200 {
				detail = append(detail, line)
			}
		}
		var parts []string
		if only > 0 {
			parts = append(parts, fmt.Sprintf("%d declared OS %s not installed", only, plural(only, "package is", "packages are")))
		}
		if differs > 0 {
			parts = append(parts, fmt.Sprintf("%d installed at another version", differs))
		}
		b.add(Finding{
			Rule: "supply.sbom-drift", Group: GroupSupply, Source: SourceImage, Certainty: Hint,
			Evidence: strings.Join(parts, ", "), Detail: detail,
			Reason:  "The SBOM declares OS packages the image's package database does not list as declared: it may describe another build of the image.",
			Unknown: "which side is stale: an SBOM made from another build, or a database changed without the package manager",
		})
	}
}

func layerWords(l int) string {
	if l <= 0 {
		return "layer not known"
	}
	return fmt.Sprintf("layer %d", l)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
