package forensics

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Which copy of a database counts, and which layer put each package there.
//
// The copies were captured layer by layer (pkgdb.go). The state of an
// ecosystem's database "as of layer j" is what a container would read if
// the image stopped at layer j: for each database path, the topmost copy
// at or below j, if finalFS says it is still visible at j. The final state
// is the state as of the last layer. A package's layer is the first layer
// of the unbroken run of states, ending at the last, that list its name and
// version: the diff of the database across layers, never a guess from file
// paths.

// dbState is one ecosystem's database as of one layer.
type dbState struct {
	// unknown says why the state cannot be known; "" when it is.
	unknown string
	// absent: no copy exists at or below this layer.
	absent bool
	recs   []srcRecord
	capped bool
	path   string // what was read, for the header
	layer  int    // the highest layer among the copies read (1-based)
	notes  []string
}

type srcRecord struct {
	pkgRecord
	source string
}

func (a *analyser) installed(scans []*layerScan) []InstalledDB {
	var out []InstalledDB
	for _, eco := range []string{ecoDeb, ecoAPK, ecoRPM} {
		out = append(out, a.installedFor(eco, scans))
	}
	return out
}

func (a *analyser) installedFor(eco string, scans []*layerScan) InstalledDB {
	db := InstalledDB{Ecosystem: eco, Manager: managerOf[eco]}
	states := make([]dbState, len(scans))
	for j := range scans {
		states[j] = a.stateAt(eco, scans, j)
	}
	if len(scans) == 0 {
		db.State, db.Unknown = DBUnknown, "the image has no layers the manifest lists"
		return db
	}
	final := states[len(scans)-1]
	db.Notes = final.notes
	switch {
	case final.unknown != "":
		db.State, db.Unknown = DBUnknown, final.unknown
		return db
	case final.absent:
		db.State = DBAbsent
		return db
	}
	db.State, db.Path, db.Layer = DBRead, final.path, final.layer
	db.Total = len(final.recs)
	if final.capped {
		// A copy held more than was kept: its own count is the total.
		db.Total = max(db.Total, a.finalTotal(eco, scans, len(scans)-1))
	}
	intro, unsure := attribute(states)
	for _, r := range final.recs {
		if len(db.Packages) >= a.lim.Installed {
			break
		}
		k := r.key()
		db.Packages = append(db.Packages, InstalledPackage{
			Name: r.name, Version: r.version, Arch: r.arch, License: r.license,
			Layer: intro[k], LayerUnsure: unsure[k], Source: r.source, Pseudo: r.pseudo,
		})
	}
	return db
}

// attribute walks the states upwards and returns, for each key in the last
// state, the layer that introduced it, and why that may be wrong.
func attribute(states []dbState) (map[string]int, map[string]string) {
	intro := map[string]int{}
	unsure := map[string]string{}
	var prev map[string]bool
	prevWhy := "" // why the state before this one is not known
	for j, st := range states {
		if st.unknown != "" || st.capped {
			why := st.unknown
			if st.capped {
				why = "its copy holds more packages than the desk keeps"
			}
			prevWhy = fmt.Sprintf("the database as of layer %d is not known (%s)", j+1, why)
			prev = nil
			continue
		}
		cur := map[string]bool{}
		for _, r := range st.recs {
			k := r.key()
			cur[k] = true
			switch {
			case prev != nil && prev[k]:
				// listed before too: introduced earlier
			case prev == nil && prevWhy != "" && intro[k] != 0:
				// listed before the unknown stretch and after it
				unsure[k] = prevWhy + "; it may have been removed and installed again"
			case prev == nil && prevWhy != "":
				intro[k], unsure[k] = j+1, prevWhy+"; it may have come from an earlier layer"
			default:
				intro[k] = j + 1
				delete(unsure, k)
			}
		}
		for k := range intro {
			if !cur[k] {
				delete(intro, k)
				delete(unsure, k)
			}
		}
		prev, prevWhy = cur, ""
	}
	return intro, unsure
}

// copiesAt returns, for each path of an ecosystem's databases, the topmost
// copy at or below layer j, with its layer index.
func copiesAt(eco string, scans []*layerScan, j int) map[string]indexedCopy {
	top := map[string]indexedCopy{}
	for i := 0; i <= j; i++ {
		s := scans[i]
		if s == nil {
			continue
		}
		for ci := range s.dbs {
			c := &s.dbs[ci]
			if c.kind.ecosystem() == eco {
				top[c.path] = indexedCopy{layer: i, c: c}
			}
		}
	}
	return top
}

type indexedCopy struct {
	layer int
	c     *dbCopy
}

// stateAt is the ecosystem's database as of layer j (0-based).
func (a *analyser) stateAt(eco string, scans []*layerScan, j int) dbState {
	fs := &finalFS{scans: scans[:j+1]}
	// A layer that was not read may hold a copy, or a whiteout: below the
	// topmost copy that matters only to dpkg, whose status.d files add up.
	unread := func(upTo int) (int, string) {
		for i := 0; i <= upTo; i++ {
			s := scans[i]
			switch {
			case s == nil:
				return i + 1, "it is missing from the archive"
			case s.unreadable != "":
				return i + 1, s.unreadable
			case s.dbsDropped:
				return i + 1, fmt.Sprintf("it holds more database files than the desk keeps (%d)", a.lim.DBCopies)
			}
		}
		return 0, ""
	}
	top := copiesAt(eco, scans, j)
	if len(top) == 0 {
		if n, why := unread(j); n > 0 {
			return dbState{unknown: fmt.Sprintf("layer %d could not be read (%s), and a package database may be in it", n, why)}
		}
		return dbState{absent: true}
	}
	// What each copy's fate is as of layer j.
	var visible []liveCopy
	deleted := ""
	paths := make([]string, 0, len(top))
	for p := range top {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		ic := top[p]
		f := fs.fate(ic.layer, p)
		switch {
		case f.visible:
			visible = append(visible, liveCopy{p, ic})
		case f.unknown:
			return dbState{unknown: fmt.Sprintf("layer %d could not be read, and it may change or delete %s (from layer %d)", f.by, p, ic.layer+1)}
		case f.how == "deleted":
			if deleted == "" {
				deleted = fmt.Sprintf("%s was deleted in layer %d", p, f.by)
			}
		default:
			return dbState{unknown: fmt.Sprintf("%s (from layer %d) is replaced in layer %d by something that is not a database file", p, ic.layer+1, f.by)}
		}
	}
	if eco == ecoDeb {
		if n, why := unread(j); n > 0 {
			return dbState{unknown: fmt.Sprintf("layer %d could not be read (%s): it may hold dpkg status.d entries", n, why)}
		}
	} else if n, why := unread(j); n > 0 && !anyAbove(visible, n-1) {
		return dbState{unknown: fmt.Sprintf("layer %d could not be read (%s), and a package database may be in it", n, why)}
	}
	if len(visible) == 0 {
		why := deleted
		if why == "" {
			why = "no copy is left"
		}
		return dbState{unknown: why + ": the packages it listed may still be on disk, but which are installed is no longer recorded"}
	}
	var st dbState
	switch eco {
	case ecoRPM:
		var dbs, logs []liveCopy
		for _, v := range visible {
			if v.ic.c.kind == dbRPMLog {
				logs = append(logs, v)
			} else {
				dbs = append(dbs, v)
			}
		}
		if len(dbs) == 0 {
			return dbState{unknown: "only rpm's log files are left, not its database"}
		}
		// The copy written last is the one rpm wrote last.
		sort.SliceStable(dbs, func(x, y int) bool { return dbs[x].ic.layer > dbs[y].ic.layer })
		pick := dbs[0]
		for _, o := range dbs[1:] {
			st.notes = append(st.notes, fmt.Sprintf("%s (layer %d) is also present; the copy written last, %s in layer %d, was read", o.path, o.ic.layer+1, pick.path, pick.ic.layer+1))
		}
		dir := path.Dir(pick.path)
		for _, l := range logs {
			if path.Dir(l.path) == dir && l.ic.c.size > 0 {
				return dbState{unknown: fmt.Sprintf("%s holds %s not yet written into the database file; the desk reads only the file", l.path, humanSize(l.ic.c.size))}
			}
		}
		visible = []liveCopy{pick}
	case ecoAPK:
	default:
		// dpkg: status and every status.d file add up.
	}
	seen := map[string]bool{}
	var files []string
	for _, v := range visible {
		c := v.ic.c
		if c.unreadable != "" {
			return dbState{unknown: fmt.Sprintf("%s (layer %d) cannot be read: %s", v.path, v.ic.layer+1, c.unreadable)}
		}
		if c.capped {
			st.capped = true
		}
		st.layer = max(st.layer, v.ic.layer+1)
		files = append(files, v.path)
		for _, r := range c.recs {
			if !seen[r.key()] {
				seen[r.key()] = true
				st.recs = append(st.recs, srcRecord{pkgRecord: r, source: v.path})
			}
		}
	}
	st.path = describeFiles(files)
	return st
}

type liveCopy struct {
	path string
	ic   indexedCopy
}

// anyAbove reports whether a database copy (not a log) is above layer.
func anyAbove(vs []liveCopy, layer int) bool {
	for _, v := range vs {
		if v.ic.layer > layer && v.ic.c.kind != dbRPMLog {
			return true
		}
	}
	return false
}

// finalTotal is the count the visible copies hold, kept or not.
func (a *analyser) finalTotal(eco string, scans []*layerScan, j int) int {
	fs := &finalFS{scans: scans[:j+1]}
	n := 0
	for p, ic := range copiesAt(eco, scans, j) {
		if ic.c.kind != dbRPMLog && fs.fate(ic.layer, p).visible {
			n += ic.c.total
		}
	}
	return n
}

// describeFiles names what was read: one file, or a directory of them.
func describeFiles(files []string) string {
	var plain []string
	statusD := 0
	for _, f := range files {
		if path.Dir(f) == "/var/lib/dpkg/status.d" {
			statusD++
		} else {
			plain = append(plain, f)
		}
	}
	if statusD > 0 {
		plain = append(plain, fmt.Sprintf("/var/lib/dpkg/status.d (%d files)", statusD))
	}
	return strings.Join(plain, " + ")
}
