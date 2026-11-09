// Package collect turns what the desk has computed — a Findings report and
// its live join, the Space account — into the plain structs package report
// serializes. It is the one place that conversion is written: the views and
// the command line both call it, so an export from the TUI and one from
// --report of the same object are the same document (X3).
//
// It decides what a file may carry. A secret's value never: a finding says
// only that it had one. Environment variables: names, not values. Container
// IDs, the daemon's address and name, times: not at all.
package collect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/space"
)

// AnalysisOptions are the options the Findings tab analyses an image with;
// the command line uses the same, so that both read an archive alike.
func AnalysisOptions(imageID, ref string, plat engine.Platform, daemon engine.Info) forensics.Options {
	return forensics.Options{
		ImageID: imageID, Ref: ref, Platform: plat.String(),
		DaemonOS: daemon.OSType, DaemonArch: daemon.Architecture,
		Limits: forensics.DefaultLimits(), Thresholds: forensics.DefaultThresholds(),
	}
}

// FactsCap bounds the containers inspected for the live join (F12).
const FactsCap = 20

// FactsTargets are the containers the live join inspects: those using the
// image, and those whose listed image is an ID, at most FactsCap, in list
// order.
func FactsTargets(img engine.Image, containers []engine.Container) []string {
	var ids []string
	for _, c := range containers {
		if (c.ImageID == img.ID || strings.HasPrefix(c.Image, "sha256:")) && len(ids) < FactsCap {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// InspectFacts inspects the containers FactsTargets names. A container that
// cannot be inspected is left out, and nothing is said about it.
func InspectFacts(ctx context.Context, eng engine.Engine, ids []string) map[string]forensics.ContainerFacts {
	out := map[string]forensics.ContainerFacts{}
	for _, id := range ids {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		d, err := eng.InspectContainer(c, id)
		cancel()
		if err == nil {
			out[id] = forensics.ContainerFacts{Privileged: d.Privileged, User: d.User, Ref: d.Image}
		}
	}
	return out
}

// Image is the document of one analysed image: the report's findings and
// the live ones (as forensics.LiveFindings made them), its layers, config,
// attestations, notes and the packages joined.
func Image(version, ref string, rep *forensics.Report, live []forensics.Finding) report.Image {
	out := report.Image{
		ToolVersion: version,
		ID:          rep.ImageID, Reference: ref, Platform: rep.Platform, Layout: rep.Layout,
		BytesRead: rep.BytesRead, RootUser: rep.RootUser, FinalSecrets: rep.FinalSecrets,
		Omitted: rep.Omitted, Notes: append([]string(nil), rep.Notes...),
	}
	c := rep.Config
	out.Config = report.Config{
		Present: c.Present, OS: c.OS, Architecture: c.Architecture, Variant: c.Variant,
		User: c.User, WorkingDir: c.WorkingDir,
		Entrypoint: append([]string(nil), c.Entrypoint...), Cmd: append([]string(nil), c.Cmd...),
		Healthcheck: append([]string(nil), c.Healthcheck...),
	}
	for _, e := range c.Env {
		name, _, _ := strings.Cut(e, "=")
		out.Config.EnvNames = append(out.Config.EnvNames, name)
	}
	for _, l := range rep.Layers {
		out.Layers = append(out.Layers, report.Layer{
			Index: l.Index, Digest: l.Digest, DiffID: l.DiffID, Verified: l.Verified,
			Compression: l.Compression, Stored: l.Stored, Size: l.Size, Files: l.Files, Dirs: l.Dirs,
			Whiteouts: l.Whiteouts, Opaques: l.Opaques, Instruction: l.Instruction,
			Unreadable: l.Unreadable, Missing: l.Missing,
		})
	}
	for _, a := range rep.Attestations {
		out.Attestations = append(out.Attestations, report.Attestation{
			PredicateType: a.PredicateType, Digest: a.Digest, Present: a.Present, Packages: a.Packages, Format: a.Format,
		})
	}
	for _, fs := range [][]forensics.Finding{rep.Findings, live} {
		for _, f := range fs {
			out.Findings = append(out.Findings, finding(f))
		}
	}
	if rep.Packages != nil {
		out.Packages = packages(rep.Packages)
	}
	return out
}

func finding(f forensics.Finding) report.Finding {
	return report.Finding{
		Rule: f.Rule, Group: string(f.Group), Source: string(f.Source), Certainty: f.Certainty.String(),
		Layer: f.Layer, Path: f.Path, Subject: f.Subject, Evidence: f.Evidence, Reason: f.Reason,
		Instruction: f.Instruction, Unknown: f.Unknown, Detail: append([]string(nil), f.Detail...),
		// The value stays in the Secret; the file says only that there was one.
		Masked: f.Secret.Present(),
	}
}

func dbState(s forensics.DBState) string {
	switch s {
	case forensics.DBRead:
		return "read"
	case forensics.DBAbsent:
		return "absent"
	}
	return "unknown"
}

func packages(p *forensics.PackageSet) *report.Packages {
	out := &report.Packages{SBOM: report.SBOM{
		Listed: p.SBOM.Listed, Format: p.SBOM.Format, Digest: p.SBOM.Digest,
		Total: p.SBOM.Total, Kept: len(p.SBOM.Packages), Others: p.SBOM.Others, Unknown: p.SBOM.Unknown,
	}}
	for _, d := range p.DBs {
		out.Databases = append(out.Databases, report.Database{
			Ecosystem: d.Ecosystem, Manager: d.Manager, State: dbState(d.State), Path: d.Path, Layer: d.Layer,
			Total: d.Total, Kept: len(d.Packages), Unknown: d.Unknown, Notes: append([]string(nil), d.Notes...),
		})
	}
	// The same join the view draws (K6, K7): a row the view calls not
	// checkable is not checkable here, with the same reason.
	for _, r := range forensics.JoinPackages(p) {
		row := report.Package{State: r.State.String(), Type: r.Type, Why: r.Why}
		if d := r.Declared; d != nil {
			row.Declared = &report.Declared{Name: d.Name, Version: d.Version, License: d.License, PURL: d.PURL, Arch: d.Arch}
		}
		if i := r.Installed; i != nil {
			row.Installed = &report.Installed{
				Name: i.Name, Version: i.Version, Arch: i.Arch, License: i.License,
				Layer: i.Layer, LayerUnsure: i.LayerUnsure, Source: i.Source, Pseudo: i.Pseudo,
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// ---- Space ------------------------------------------------------------------------

// SpaceInput is what the Space view holds.
type SpaceInput struct {
	Version string
	DU      engine.DiskUsage
	// Index is the sharing of layers; nil while no image's layers are read.
	Index *space.Index
	// Stale: the engine changed after the account was taken. Reading:
	// the layers were still being read.
	Stale, Reading bool
	// Writable is every container's writable layer by ID, nil when not
	// measured; Containers names them.
	Writable      map[string]engine.ContainerSize
	WritableStale bool
	Containers    []engine.Container
}

const (
	srcDaemon = "daemon"
	srcLayers = "computed from the daemon's layer sizes"
)

func ptr(n int64) *int64 { return &n }

// Space is the document of the Space account: the same figures the view
// shows, each exact or at least with its reason.
func Space(in SpaceInput) report.Space {
	du := in.DU
	out := report.Space{ToolVersion: in.Version, TotalsSource: srcDaemon, Stale: in.Stale}
	if du.Derived {
		out.TotalsSource = "client library sums of the daemon's lists (API < 1.52 states no totals)"
	}
	for _, t := range []struct {
		name string
		u    engine.Usage
	}{{"Images", du.Images}, {"Containers", du.Containers}, {"Local Volumes", du.Volumes}, {"Build Cache", du.BuildCache}} {
		out.Totals = append(out.Totals, report.SpaceTotal{Type: t.name, Count: t.u.TotalCount, Active: t.u.ActiveCount, Size: t.u.TotalSize, Reclaimable: t.u.Reclaimable})
	}

	ix := in.Index
	for _, it := range du.ImageItems {
		img := report.SpaceImage{ID: it.ID, References: (engine.Image{RepoTags: it.RepoTags}).Refs(), Size: it.Size, Containers: it.Containers, Layers: "not read"}
		if it.SharedSize >= 0 {
			img.Shared = ptr(it.SharedSize)
		}
		if u := it.Unique(); u >= 0 {
			img.Alone = report.Figure{Bytes: u, Measure: report.Exact, Source: srcDaemon}
		} else {
			img.Alone = report.Figure{Measure: report.Unknown, Source: srcDaemon, Reason: "the daemon did not compute the shared size"}
		}
		switch {
		case ix == nil || !ix.Indexed(it.ID):
			img.Together = &report.Figure{Measure: report.Unknown, Source: srcLayers, Reason: "its layers could not be read"}
		default:
			img.Layers = "unattributed"
			if ix.Agrees(it.ID) {
				img.Layers = "attributed"
			}
			sharers := ix.Sharers(it.ID)
			if len(sharers) == 0 {
				break
			}
			img.Sharers = sharers
			f := ix.Frees(append([]string{it.ID}, sharers...))
			switch {
			case f.Exact:
				img.Together = &report.Figure{Bytes: f.Bytes, Measure: report.Exact, Source: srcLayers}
			case f.Known:
				img.Together = &report.Figure{Bytes: f.Bytes, Measure: report.AtLeast, Source: srcDaemon, Reason: "their unique sizes: " + f.Reason}
			default:
				img.Together = &report.Figure{Measure: report.Unknown, Source: srcDaemon, Reason: f.Reason}
			}
		}
		out.Images = append(out.Images, img)
	}

	if ix != nil {
		for _, g := range ix.Groups() {
			sg := report.SpaceGroup{Images: append([]string(nil), g.Holders...), Layers: g.Layers, Unsized: g.Unknown}
			switch {
			case g.Unknown == 0:
				sg.Size = report.Figure{Bytes: g.Bytes, Measure: report.Exact, Source: srcLayers}
			case g.Unknown == g.Layers:
				sg.Size = report.Figure{Measure: report.Unknown, Source: srcLayers, Reason: "no layer of the group has a size the daemon's own figures confirm"}
			default:
				sg.Size = report.Figure{Bytes: g.Bytes, Measure: report.AtLeast, Source: srcLayers,
					Reason: fmt.Sprintf("%d of its %d layers have no size the daemon's own figures confirm", g.Unknown, g.Layers)}
			}
			out.Groups = append(out.Groups, sg)
		}
		if dis := ix.Disagreeing(); len(dis) > 0 {
			indexed := len(du.ImageItems) - len(ix.Unindexed())
			if len(dis) == indexed {
				out.Notes = append(out.Notes, "Sizes not attributed: no image's layer sizes add up to the daemon's size for it (the containerd image store counts each layer's compressed content as well as its unpacked files; the history gives only the second). What a set frees is given as at least its members' unique sizes.")
			} else {
				ids := append([]string(nil), dis...)
				sort.Strings(ids)
				out.Notes = append(out.Notes, "Not attributed: the layer sizes of "+strings.Join(ids, ", ")+" do not map onto their layers, or do not add up to the daemon's size for them. Their figures are the daemon's alone.")
			}
		}
		if un := ix.Unindexed(); len(un) > 0 && in.Reading {
			out.Notes = append(out.Notes, fmt.Sprintf("The layers of %d image(s) were still being read: what they share is not known yet.", len(un)))
		} else if len(un) > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("The layers of %d image(s) could not be read: what they share is not known.", len(un)))
		}
	} else if len(du.ImageItems) > 0 {
		out.Notes = append(out.Notes, "No image's layers were read: which images share bytes is not known.")
	}

	for _, v := range du.VolumeItems {
		sv := report.SpaceVolume{Name: v.Name, Driver: v.Driver}
		if v.Size >= 0 {
			sv.Size = ptr(v.Size)
		}
		if v.RefCount >= 0 {
			sv.Links = ptr(v.RefCount)
		}
		out.Volumes = append(out.Volumes, sv)
	}

	if in.Writable != nil {
		w := &report.SpaceWritable{Stale: in.WritableStale, Containers: []report.SpaceContainer{}}
		seen := map[string]bool{}
		for _, c := range in.Containers {
			if s, ok := in.Writable[c.ID]; ok {
				seen[c.ID] = true
				w.Containers = append(w.Containers, report.SpaceContainer{Name: c.Name, State: c.State, Writable: s.SizeRw, Virtual: s.SizeRootFs})
			}
		}
		for id, s := range in.Writable {
			if !seen[id] {
				// Measured, and gone from the list since: named as measured.
				w.Containers = append(w.Containers, report.SpaceContainer{Name: s.Name, State: s.State, Writable: s.SizeRw, Virtual: s.SizeRootFs})
			}
		}
		out.Writable = w
	}

	for _, b := range du.BuildItems {
		out.BuildCache = append(out.BuildCache, report.SpaceBuildItem{Type: b.Type, Description: b.Description, Size: b.Size, InUse: b.InUse, Shared: b.Shared})
	}
	return out
}

// LayersOf is what Space reads of one image's layers: chain IDs and, when
// the history maps, sizes; ok is false when they could not be read.
func LayersOf(ctx context.Context, eng engine.Engine, id string) (chains []string, sizes []int64, err error) {
	l, err := eng.ImageLayers(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	chains = space.ChainIDs(l.DiffIDs)
	if s, ok := space.LayerSizes(len(l.DiffIDs), l.History); ok {
		sizes = s
	}
	return chains, sizes, nil
}
