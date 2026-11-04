package forensics

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// The ingredient list of an image, two ways: what its SBOM declares, and
// what its own package databases say is installed. Both are read while the
// archive streams (sbom.go, pkgdb.go); they are joined when the view is
// drawn (JoinPackages), within one ecosystem at a time — a deb package is
// checked only against dpkg, an apk one against apk, an rpm one against
// rpm. Nothing else can be checked, and is never called undeclared.

// PackageSet is both sides.
type PackageSet struct {
	SBOM SBOMPackages
	// DBs are the installed databases: deb (dpkg), apk, rpm, in that order.
	DBs []InstalledDB
}

// SBOMPackages is the declared side.
type SBOMPackages struct {
	// Listed is set when the image lists an SBOM attestation.
	Listed bool
	// Format names the SBOM format ("SPDX-2.3", "CycloneDX 1.5").
	Format string
	Digest string
	// Packages is the table kept; Total counts every package the document
	// lists, kept or not.
	Packages []DeclaredPackage
	Total    int
	// Unknown says why the packages are not known; "" when they were read.
	Unknown string
	// Others counts further SBOM attestations, which were not read into
	// the table.
	Others int
}

// Known reports whether the declared list was read.
func (s SBOMPackages) Known() bool { return s.Listed && s.Unknown == "" }

// Capped reports whether the table holds fewer packages than the SBOM.
func (s SBOMPackages) Capped() bool { return len(s.Packages) < s.Total }

// DeclaredPackage is one package an SBOM lists.
type DeclaredPackage struct {
	Name, Version, License, PURL string
	// Type is the purl type (deb, apk, rpm, npm, golang…); "" without a purl.
	Type string
	// Arch is the purl's arch qualifier.
	Arch string
}

// DBState is what is known about one ecosystem's installed database.
type DBState int

const (
	// DBAbsent: no layer has the database, and every layer was read.
	DBAbsent DBState = iota
	// DBRead: the copy in the final filesystem was read.
	DBRead
	// DBUnknown: what is installed cannot be known; Unknown says why.
	DBUnknown
)

// InstalledDB is one ecosystem's installed side.
type InstalledDB struct {
	Ecosystem string // deb, apk, rpm
	Manager   string // dpkg, apk, rpm
	State     DBState
	// Path names the copy read; Layer is the layer it is in.
	Path  string
	Layer int
	// Packages is what the database lists, bounded; Total counts them all.
	Packages []InstalledPackage
	Total    int
	Unknown  string
	// Notes are facts that do not make the list unknown.
	Notes []string
}

// Capped reports whether fewer packages are kept than the database lists.
func (d InstalledDB) Capped() bool { return len(d.Packages) < d.Total }

// InstalledPackage is one package a database lists.
type InstalledPackage struct {
	Name, Version, Arch, License string
	// Layer is the layer that introduced this name and version: the
	// first whose copy of the database lists it, with every later copy
	// listing it too. 0 when no copy could say.
	Layer int
	// LayerUnsure says why Layer may be wrong; "" when it is certain.
	LayerUnsure string
	// Source is the database file that lists it.
	Source string
	// Pseudo marks an entry that is not a package (rpm's gpg-pubkey).
	Pseudo bool
}

// ---- the join -------------------------------------------------------------------

// PkgState is how the two sides agree about one package.
type PkgState int

const (
	// StateBoth: declared, and installed at that version.
	StateBoth PkgState = iota
	// StateDiffers: declared, and installed at another version.
	StateDiffers
	// StateDeclaredOnly: declared, and the database does not list it.
	StateDeclaredOnly
	// StateUndeclared: installed, and the SBOM does not list it.
	StateUndeclared
	// StateUncheckable: one side cannot be read, or the ecosystem has no
	// database the desk reads.
	StateUncheckable
	stateCount
)

func (s PkgState) String() string {
	return [...]string{"both", "version differs", "declared only", "undeclared", "not checkable"}[s]
}

// PkgRow is one row of the joined list.
type PkgRow struct {
	State PkgState
	// Type is the ecosystem: the purl type, or the database's.
	Type      string
	Declared  *DeclaredPackage
	Installed *InstalledPackage
	// Why explains a not-checkable row.
	Why string
}

// Name is the package's name, from whichever side has it.
func (r PkgRow) Name() string {
	if r.Installed != nil {
		return r.Installed.Name
	}
	if r.Declared != nil {
		return r.Declared.Name
	}
	return ""
}

// Layer is the installed package's layer, or 0.
func (r PkgRow) Layer() int {
	if r.Installed != nil {
		return r.Installed.Layer
	}
	return 0
}

// License is the declared licence, or the installed one.
func (r PkgRow) License() string {
	if r.Declared != nil && r.Declared.License != "" {
		return r.Declared.License
	}
	if r.Installed != nil {
		return r.Installed.License
	}
	return ""
}

// Version is the installed version, or the declared one.
func (r PkgRow) Version() string {
	if r.Installed != nil {
		return r.Installed.Version
	}
	if r.Declared != nil {
		return r.Declared.Version
	}
	return ""
}

// Key identifies the package across reorderings and filters, for a cursor
// to follow.
func (r PkgRow) Key() string {
	var dv, iv, arch, purl string
	if r.Declared != nil {
		dv, arch, purl = r.Declared.Version, r.Declared.Arch, r.Declared.PURL
	}
	if r.Installed != nil {
		iv, arch = r.Installed.Version, r.Installed.Arch
	}
	return r.Type + "|" + r.Name() + "|" + dv + "|" + iv + "|" + arch + "|" + purl
}

// IsOS reports whether the ecosystem is one the desk reads a database for.
func IsOS(typ string) bool { return managerOf[typ] != "" }

// DB returns the installed side for an ecosystem.
func (p *PackageSet) DB(eco string) (InstalledDB, bool) {
	for _, d := range p.DBs {
		if d.Ecosystem == eco {
			return d, true
		}
	}
	return InstalledDB{}, false
}

// JoinPackages joins the two sides, within each ecosystem. The rows come
// out in no particular order; SortPackages orders them.
func JoinPackages(p *PackageSet) []PkgRow {
	if p == nil {
		return nil
	}
	var rows []PkgRow
	sbomWhy := ""
	switch {
	case !p.SBOM.Listed:
		sbomWhy = "the image carries no SBOM to compare with"
	case p.SBOM.Unknown != "":
		sbomWhy = "the SBOM could not be read: " + p.SBOM.Unknown
	}
	byEco := map[string][]int{}
	for i := range p.SBOM.Packages {
		d := &p.SBOM.Packages[i]
		if !IsOS(d.Type) {
			why := "only deb, apk and rpm packages are checked against the image's own databases"
			if d.Type == "" {
				why = "the SBOM gives no purl, so its ecosystem is not known"
			}
			rows = append(rows, PkgRow{State: StateUncheckable, Type: d.Type, Declared: d, Why: why})
			continue
		}
		byEco[d.Type] = append(byEco[d.Type], i)
	}
	for di := range p.DBs {
		db := &p.DBs[di]
		eco := db.Ecosystem
		dbWhy := ""
		switch db.State {
		case DBUnknown:
			dbWhy = "what " + db.Manager + " has installed is unknown: " + db.Unknown
		case DBRead:
			if db.Capped() {
				dbWhy = fmt.Sprintf("the desk kept %d of the %d packages %s lists, so one not kept cannot be told from one not installed", len(db.Packages), db.Total, db.Manager)
			}
		}
		declared := byEco[eco]
		if db.State == DBUnknown {
			for _, i := range declared {
				rows = append(rows, PkgRow{State: StateUncheckable, Type: eco, Declared: &p.SBOM.Packages[i], Why: dbWhy})
			}
			continue
		}
		installedRow := func(ip *InstalledPackage) {
			r := PkgRow{Type: eco, Installed: ip}
			switch {
			case ip.Pseudo:
				r.State, r.Why = StateUncheckable, "gpg-pubkey is a signing key rpm lists as a package; SBOMs leave it out"
			case sbomWhy != "":
				r.State, r.Why = StateUncheckable, sbomWhy
			case p.SBOM.Capped():
				r.State, r.Why = StateUncheckable, fmt.Sprintf("the desk kept %d of the SBOM's %d packages, so one not kept cannot be told from one not declared", len(p.SBOM.Packages), p.SBOM.Total)
			default:
				r.State = StateUndeclared
			}
			rows = append(rows, r)
		}
		for _, r := range joinEcosystem(eco, db, p.SBOM.Packages, declared) {
			switch {
			case r.Installed == nil && dbWhy != "":
				// The database lists more than was kept: a declared
				// package not among the kept ones may be in the rest.
				r.State, r.Why = StateUncheckable, dbWhy
			case r.Declared == nil:
				installedRow(r.Installed)
				continue
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// joinEcosystem pairs one ecosystem's declared and installed packages by
// name: the same version first, then the same name at another version.
// An installed package nothing declares comes back with Declared nil.
func joinEcosystem(eco string, db *InstalledDB, all []DeclaredPackage, declared []int) []PkgRow {
	var rows []PkgRow
	byName := map[string][]int{} // installed indexes, by name
	for i := range db.Packages {
		byName[db.Packages[i].Name] = append(byName[db.Packages[i].Name], i)
	}
	used := make([]bool, len(db.Packages))
	var pending []int // declared without an exact match
	seen := map[string]bool{}
	for _, di := range declared {
		d := &all[di]
		// An SBOM that lists a package twice (found at two locations)
		// declares it once.
		if k := d.Type + "|" + d.Name + "|" + d.Version + "|" + d.Arch; seen[k] {
			continue
		} else {
			seen[k] = true
		}
		match := -1
		for _, ii := range byName[d.Name] {
			ip := &db.Packages[ii]
			if !used[ii] && sameVersion(eco, d.Version, ip.Version) && (d.Arch == "" || ip.Arch == "" || d.Arch == ip.Arch) {
				match = ii
				break
			}
		}
		if match < 0 {
			pending = append(pending, di)
			continue
		}
		used[match] = true
		rows = append(rows, PkgRow{State: StateBoth, Type: eco, Declared: d, Installed: &db.Packages[match]})
	}
	for _, di := range pending {
		d := &all[di]
		match := -1
		for _, ii := range byName[d.Name] {
			if !used[ii] {
				match = ii
				break
			}
		}
		if match < 0 {
			rows = append(rows, PkgRow{State: StateDeclaredOnly, Type: eco, Declared: d})
			continue
		}
		used[match] = true
		rows = append(rows, PkgRow{State: StateDiffers, Type: eco, Declared: d, Installed: &db.Packages[match]})
	}
	for i := range db.Packages {
		if !used[i] {
			rows = append(rows, PkgRow{Type: eco, Installed: &db.Packages[i]})
		}
	}
	return rows
}

// sameVersion compares a declared and an installed version. An epoch of 0,
// or one only one side states, does not make them differ: SBOM tools
// differ on whether to write it.
func sameVersion(eco, declared, installed string) bool {
	if declared == installed {
		return true
	}
	de, dv := splitEpoch(declared)
	ie, iv := splitEpoch(installed)
	if dv != iv {
		return false
	}
	return de == ie || de == "" || ie == "" || (de == "0" && ie == "") || (ie == "0" && de == "")
}

func splitEpoch(v string) (string, string) {
	if i := strings.IndexByte(v, ':'); i > 0 && strings.Trim(v[:i], "0123456789") == "" {
		return strings.TrimLeft(v[:i], "0"), v[i+1:]
	}
	return "", v
}

// parsePURL reads the parts of a package URL the join needs:
// pkg:type/namespace/name@version?qualifiers#subpath.
func parsePURL(s string) (typ, name, version, arch, epoch string, ok bool) {
	rest, found := strings.CutPrefix(s, "pkg:")
	if !found {
		return "", "", "", "", "", false
	}
	rest, _, _ = strings.Cut(rest, "#")
	rest, query, _ := strings.Cut(rest, "?")
	rest, ver, _ := strings.Cut(rest, "@")
	rest = strings.TrimLeft(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[len(parts)-1] == "" {
		return "", "", "", "", "", false
	}
	unesc := func(x string) string {
		if u, err := url.PathUnescape(x); err == nil {
			return u
		}
		return x
	}
	typ = strings.ToLower(parts[0])
	name = unesc(parts[len(parts)-1])
	version = unesc(ver)
	if q, err := url.ParseQuery(query); err == nil {
		arch, epoch = q.Get("arch"), q.Get("epoch")
	}
	return typ, name, version, arch, epoch, true
}

// ---- counting, ordering, filtering ---------------------------------------------

// PkgCounts counts rows by state.
func PkgCounts(rows []PkgRow) [stateCount]int {
	var n [stateCount]int
	for _, r := range rows {
		n[r.State]++
	}
	return n
}

// PkgOrder is how the view lists packages.
type PkgOrder int

const (
	PkgByName PkgOrder = iota
	PkgByState
	PkgByLayer
	PkgByLicense
	pkgOrderCount
)

func (o PkgOrder) String() string {
	return [...]string{"name", "state", "layer", "licence"}[o]
}

// Next is the order after o, cycling.
func (o PkgOrder) Next() PkgOrder { return (o + 1) % pkgOrderCount }

var stateRank = [stateCount]int{StateUndeclared: 0, StateDiffers: 1, StateDeclaredOnly: 2, StateUncheckable: 3, StateBoth: 4}

// SortPackages orders rows in place. Ties break the same way in every
// order, so a list holds still between frames.
func SortPackages(rows []PkgRow, o PkgOrder) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch o {
		case PkgByState:
			if ra, rb := stateRank[a.State], stateRank[b.State]; ra != rb {
				return ra < rb // the disagreements first, agreement last
			}
		case PkgByLayer:
			if la, lb := a.Layer(), b.Layer(); la != lb {
				if la == 0 || lb == 0 {
					return lb == 0 // no layer known: last
				}
				return la < lb
			}
		case PkgByLicense:
			la, lb := strings.ToLower(a.License()), strings.ToLower(b.License())
			if (la == "") != (lb == "") {
				return lb == "" // a stated licence before none
			}
			if la != lb {
				return la < lb
			}
		}
		if na, nb := strings.ToLower(a.Name()), strings.ToLower(b.Name()); na != nb {
			return na < nb
		}
		return a.Key() < b.Key()
	})
}

// PkgFilter is which states the view shows.
type PkgFilter int

const (
	ShowAll PkgFilter = iota
	ShowDisagree
	ShowUndeclared
	ShowDiffers
	ShowDeclaredOnly
	ShowUncheckable
	ShowBoth
	pkgFilterCount
)

func (f PkgFilter) String() string {
	return [...]string{"all", "disagreements", "undeclared", "version differs", "declared only", "not checkable", "both"}[f]
}

// Next is the filter after f, cycling.
func (f PkgFilter) Next() PkgFilter { return (f + 1) % pkgFilterCount }

// Shows reports whether a row in state s passes the filter.
func (f PkgFilter) Shows(s PkgState) bool {
	switch f {
	case ShowDisagree:
		return s == StateDiffers || s == StateDeclaredOnly || s == StateUndeclared
	case ShowUndeclared:
		return s == StateUndeclared
	case ShowDiffers:
		return s == StateDiffers
	case ShowDeclaredOnly:
		return s == StateDeclaredOnly
	case ShowUncheckable:
		return s == StateUncheckable
	case ShowBoth:
		return s == StateBoth
	}
	return true
}

// Matches reports whether a row holds the search text (any case) in its
// name, either version, its licence or its purl.
func (r PkgRow) Matches(q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	if d := r.Declared; d != nil && (has(d.Name) || has(d.Version) || has(d.License) || has(d.PURL)) {
		return true
	}
	if i := r.Installed; i != nil && (has(i.Name) || has(i.Version) || has(i.License)) {
		return true
	}
	return false
}
