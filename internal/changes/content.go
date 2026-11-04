package changes

import (
	"fmt"
	"sort"

	"github.com/fardani235/docker-desk/internal/forensics"
)

// Pkg is one package on one side.
type Pkg struct {
	Ecosystem, Name, Arch, Version string
	Layer                          int // the layer that introduced it; 0 when not known
}

// PkgChange is a package installed on both sides at different versions.
type PkgChange struct {
	Ecosystem, Name, Arch string
	Old, New              string
}

// Content is what the two archives' analyses differ in.
type Content struct {
	Added, Removed        []Pkg
	Changed               []PkgChange
	Appeared, Disappeared []forensics.Finding
	// Compared names the package databases compared ("dpkg", "apk", "rpm").
	Compared []string
	// Unknowns are what could not be compared, each with its reason.
	Unknowns []string
}

// Unreadable refuses a comparison when a layer of either image could not be
// read: a package or finding seen on one side only could be in it.
func Unreadable(old, new *forensics.Report) Refusal {
	for _, s := range []struct {
		name string
		rep  *forensics.Report
	}{{"running", old}, {"new", new}} {
		for _, l := range s.rep.Layers {
			why := l.Unreadable
			if l.Missing {
				why = "the archive does not contain it"
			}
			if why != "" {
				return Refusal{"unreadable", fmt.Sprintf("Layer %d of the %s image could not be read (%s): anything seen on one side only could be in it.", l.Index, s.name, why)}
			}
		}
	}
	return Refusal{}
}

// CompareContent compares the installed packages and the archive findings
// of the running image (old) and the new one.
func CompareContent(old, new *forensics.Report) Content {
	var c Content
	dbs := func(r *forensics.Report) map[string]forensics.InstalledDB {
		out := map[string]forensics.InstalledDB{}
		if r.Packages != nil {
			for _, d := range r.Packages.DBs {
				out[d.Ecosystem] = d
			}
		}
		return out
	}
	a, b := dbs(old), dbs(new)
	for _, eco := range []string{"deb", "apk", "rpm"} {
		da, inA := a[eco]
		db, inB := b[eco]
		absent := func(d forensics.InstalledDB, in bool) bool { return !in || d.State == forensics.DBAbsent }
		if absent(da, inA) && absent(db, inB) {
			continue
		}
		mgr := da.Manager
		if mgr == "" {
			mgr = db.Manager
		}
		switch {
		case inA && da.State == forensics.DBUnknown:
			c.Unknowns = append(c.Unknowns, fmt.Sprintf("%s packages of the running image: %s", mgr, da.Unknown))
			continue
		case inB && db.State == forensics.DBUnknown:
			c.Unknowns = append(c.Unknowns, fmt.Sprintf("%s packages of the new image: %s", mgr, db.Unknown))
			continue
		case da.Capped() || db.Capped():
			c.Unknowns = append(c.Unknowns, fmt.Sprintf("%s packages: a database lists more packages than are kept (%d and %d), so the lists are not compared", mgr, da.Total, db.Total))
			continue
		}
		c.Compared = append(c.Compared, mgr)
		type key struct{ name, arch string }
		index := func(d forensics.InstalledDB) map[key]forensics.InstalledPackage {
			m := map[key]forensics.InstalledPackage{}
			for _, p := range d.Packages {
				if !p.Pseudo {
					m[key{p.Name, p.Arch}] = p
				}
			}
			return m
		}
		ia, ib := index(da), index(db)
		for k, p := range ia {
			q, ok := ib[k]
			switch {
			case !ok:
				c.Removed = append(c.Removed, Pkg{eco, oneLine(p.Name, 80), oneLine(p.Arch, 20), oneLine(p.Version, 60), p.Layer})
			case q.Version != p.Version:
				c.Changed = append(c.Changed, PkgChange{eco, oneLine(p.Name, 80), oneLine(p.Arch, 20), oneLine(p.Version, 60), oneLine(q.Version, 60)})
			}
		}
		for k, q := range ib {
			if _, ok := ia[k]; !ok {
				c.Added = append(c.Added, Pkg{eco, oneLine(q.Name, 80), oneLine(q.Arch, 20), oneLine(q.Version, 60), q.Layer})
			}
		}
	}
	byName := func(e1, n1, a1, e2, n2, a2 string) bool {
		if e1 != e2 {
			return e1 < e2
		}
		if n1 != n2 {
			return n1 < n2
		}
		return a1 < a2
	}
	sort.Slice(c.Added, func(i, j int) bool {
		x, y := c.Added[i], c.Added[j]
		return byName(x.Ecosystem, x.Name, x.Arch, y.Ecosystem, y.Name, y.Arch)
	})
	sort.Slice(c.Removed, func(i, j int) bool {
		x, y := c.Removed[i], c.Removed[j]
		return byName(x.Ecosystem, x.Name, x.Arch, y.Ecosystem, y.Name, y.Arch)
	})
	sort.Slice(c.Changed, func(i, j int) bool {
		x, y := c.Changed[i], c.Changed[j]
		return byName(x.Ecosystem, x.Name, x.Arch, y.Ecosystem, y.Name, y.Arch)
	})

	fa, fb := findingIndex(old), findingIndex(new)
	for _, f := range new.Findings {
		if !fa[FindingKey(f)] {
			c.Appeared = append(c.Appeared, f)
		}
	}
	for _, f := range old.Findings {
		if !fb[FindingKey(f)] {
			c.Disappeared = append(c.Disappeared, f)
		}
	}
	forensics.Sort(c.Appeared, 0)
	forensics.Sort(c.Disappeared, 0)
	if old.Omitted > 0 || new.Omitted > 0 {
		c.Unknowns = append(c.Unknowns, fmt.Sprintf("findings: %d and %d were dropped by the findings limit, so a finding listed as new or gone may be among them", old.Omitted, new.Omitted))
	}
	return c
}

func findingIndex(r *forensics.Report) map[string]bool {
	m := map[string]bool{}
	for _, f := range r.Findings {
		m[FindingKey(f)] = true
	}
	return m
}

// FindingKey identifies a finding across two images. The layer number is
// left out: the same file in a shared base keeps its number, but a finding
// about the config has none and a rebuilt layer may sit elsewhere. A layer
// finding's evidence is relative to the whole image (a share of its size),
// so it is matched by its instruction instead.
func FindingKey(f forensics.Finding) string {
	if f.Group == forensics.GroupLayer {
		return f.Rule + "|" + f.Instruction
	}
	return f.Rule + "|" + f.Path + "|" + f.Evidence
}
