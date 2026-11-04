package forensics

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// The installed side: the package databases an image's own package
// managers keep. Each copy of a database a layer carries is parsed as the
// layer streams past, into records; its bytes are dropped. Which copy
// counts is decided afterwards from the final filesystem (finalFS), and a
// package's layer from how the copies differ (installed.go).

// pkgRecord is one package a database lists.
type pkgRecord struct {
	name, version, arch, license string
	// pseudo marks an entry the manager lists that is not a package:
	// rpm's gpg-pubkey headers are imported signing keys.
	pseudo bool
}

func (r pkgRecord) key() string { return r.name + "\x00" + r.version + "\x00" + r.arch }

// Managers, by the purl type their packages carry.
const (
	ecoDeb = "deb"
	ecoAPK = "apk"
	ecoRPM = "rpm"
)

var managerOf = map[string]string{ecoDeb: "dpkg", ecoAPK: "apk", ecoRPM: "rpm"}

// dbFile is what a path in a layer is to the package databases.
type dbFile int

const (
	dbNone        dbFile = iota
	dbDpkgStatus         // /var/lib/dpkg/status
	dbDpkgStatusD        // /var/lib/dpkg/status.d/<package> (distroless)
	dbAPK                // /lib/apk/db/installed
	dbRPMSQLite          // rpmdb.sqlite
	dbRPMOther           // Packages (Berkeley DB) or Packages.db (NDB)
	dbRPMLog             // rpmdb.sqlite-wal, rpmdb.sqlite-journal
)

// rpmDirs are where rpm keeps its database: Fedora and openSUSE moved it
// to /usr/lib/sysimage/rpm and left /var/lib/rpm as a link.
var rpmDirs = []string{"/var/lib/rpm", "/usr/lib/sysimage/rpm"}

func classifyDB(p string) dbFile {
	dir, base := path.Split(p)
	dir = path.Clean(dir)
	switch {
	case p == "/var/lib/dpkg/status":
		return dbDpkgStatus
	case dir == "/var/lib/dpkg/status.d":
		if strings.HasSuffix(base, ".md5sums") {
			return dbNone
		}
		return dbDpkgStatusD
	case p == "/lib/apk/db/installed":
		return dbAPK
	}
	for _, d := range rpmDirs {
		if dir != d {
			continue
		}
		switch base {
		case "rpmdb.sqlite":
			return dbRPMSQLite
		case "Packages", "Packages.db":
			return dbRPMOther
		case "rpmdb.sqlite-wal", "rpmdb.sqlite-journal":
			return dbRPMLog
		}
	}
	return dbNone
}

func (k dbFile) ecosystem() string {
	switch k {
	case dbDpkgStatus, dbDpkgStatusD:
		return ecoDeb
	case dbAPK:
		return ecoAPK
	}
	return ecoRPM
}

// dbCopy is one copy of a database file in one layer.
type dbCopy struct {
	path string
	kind dbFile
	size int64
	recs []pkgRecord
	// total counts the records the copy holds, kept or not.
	total int
	// capped is set when recs holds fewer than total.
	capped bool
	// unreadable says why the copy's records are not known.
	unreadable string
}

// dbCapture reads a package database file in a layer: parses it into
// records within the bounds, and returns a reader over the same bytes for
// the rest of the walk (the secret scan reads it too, as before).
func (a *analyser) dbCapture(s *layerScan, p string, kind dbFile, h *tar.Header, r io.Reader) (io.Reader, error) {
	c := dbCopy{path: p, kind: kind, size: h.Size}
	defer func() {
		if len(s.dbs) < a.lim.DBCopies {
			s.dbs = append(s.dbs, c)
		} else {
			s.dbsDropped = true
		}
	}()
	switch {
	case h.Typeflag == tar.TypeSymlink:
		c.unreadable = "it is a symbolic link (to " + sanitize(h.Linkname, 120) + "), not the database itself"
		return r, nil
	case h.Typeflag == tar.TypeLink:
		c.unreadable = "it is a hard link to " + sanitize(h.Linkname, 120) + " in the same layer, which the desk does not follow"
		return r, nil
	case h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA: //nolint:staticcheck // old layers use TypeRegA
		c.unreadable = "it is not a regular file"
		return r, nil
	case kind == dbRPMLog:
		return r, nil // only its size matters: see installed.go
	case kind == dbRPMOther:
		c.unreadable = "it is rpm's Berkeley DB or NDB format, which the desk does not read (only rpmdb.sqlite)"
		return r, nil
	}
	bound := a.lim.TextDB
	if kind == dbRPMSQLite {
		bound = a.lim.RPMDB
	}
	if h.Size > bound {
		c.unreadable = fmt.Sprintf("it is %s, above the %s the desk reads of one database", humanSize(h.Size), humanSize(bound))
		return r, nil
	}
	if a.dbUsed >= a.lim.InstalledTotal {
		c.unreadable = fmt.Sprintf("the desk keeps %d records across all copies of the databases, and they were used up by earlier layers", a.lim.InstalledTotal)
		return r, nil
	}
	if cap(a.dbBuf) < int(h.Size) {
		a.dbBuf = make([]byte, h.Size)
	}
	buf := a.dbBuf[:h.Size]
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, a.readErr(err)
	}
	keep := min(a.lim.Installed, a.lim.InstalledTotal-a.dbUsed)
	var err error
	switch kind {
	case dbRPMSQLite:
		c.recs, c.total, err = readRPMDB(buf, keep)
	case dbAPK:
		c.recs, c.total, err = parseAPK(buf, keep)
	default:
		c.recs, c.total, err = parseDpkg(buf, keep, kind == dbDpkgStatusD)
	}
	if err != nil {
		c.recs, c.total, c.unreadable = nil, 0, err.Error()
	}
	c.capped = len(c.recs) < c.total
	a.dbUsed += len(c.recs)
	return bytes.NewReader(buf), nil
}

// ---- dpkg ----------------------------------------------------------------------

// parseDpkg reads dpkg's status file: stanzas of Field: value lines,
// separated by blank lines. Only packages whose status says installed are
// listed — a package removed with its configuration kept ("rc") is not.
// A distroless status.d file is one stanza, often without a Status field.
func parseDpkg(b []byte, keep int, statusD bool) ([]pkgRecord, int, error) {
	var recs []pkgRecord
	total := 0
	err := stanzas(b, func(f map[string]string) {
		name := f["Package"]
		if name == "" {
			return
		}
		st, has := f["Status"]
		if has || !statusD {
			if w := strings.Fields(st); len(w) != 3 || w[2] != "installed" {
				return
			}
		}
		total++
		if len(recs) < keep {
			recs = append(recs, pkgRecord{name: sanitize(name, 128), version: sanitize(f["Version"], 128), arch: sanitize(f["Architecture"], 32)})
		}
	}, ':')
	if err != nil {
		return nil, 0, fmt.Errorf("dpkg's status is not readable: %w", err)
	}
	return recs, total, nil
}

// ---- apk -----------------------------------------------------------------------

// parseAPK reads apk's installed database: blocks of one-letter fields
// (P name, V version, A arch, L licence) separated by blank lines.
func parseAPK(b []byte, keep int) ([]pkgRecord, int, error) {
	var recs []pkgRecord
	total := 0
	err := stanzas(b, func(f map[string]string) {
		if f["P"] == "" {
			return
		}
		total++
		if len(recs) < keep {
			recs = append(recs, pkgRecord{name: sanitize(f["P"], 128), version: sanitize(f["V"], 128), arch: sanitize(f["A"], 32), license: sanitize(f["L"], 256)})
		}
	}, ':')
	if err != nil {
		return nil, 0, fmt.Errorf("apk's installed database is not readable: %w", err)
	}
	return recs, total, nil
}

// stanzas splits a text database into blocks of "Key<sep>value" lines.
// Continuation lines (leading space or tab) and keys nobody asked about
// are skipped; only the first occurrence of a key in a block counts.
func stanzas(b []byte, each func(map[string]string), sep byte) error {
	if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
		return errors.New("it holds NUL bytes, so it is not text")
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	f := map[string]string{}
	flush := func() {
		if len(f) > 0 {
			each(f)
			f = map[string]string{}
		}
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		i := bytes.IndexByte(line, sep)
		if i <= 0 {
			continue
		}
		k := string(line[:i])
		if _, ok := f[k]; !ok && wantedField[k] {
			f[k] = strings.TrimSpace(string(line[i+1:]))
		}
	}
	if err := sc.Err(); err != nil {
		return errors.New("a line is longer than 1 MiB")
	}
	flush()
	return nil
}

var wantedField = map[string]bool{
	"Package": true, "Version": true, "Architecture": true, "Status": true,
	"P": true, "V": true, "A": true, "L": true,
}
