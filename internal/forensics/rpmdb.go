package forensics

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// rpm's database: rpmdb.sqlite holds one row per installed header in its
// Packages table (hnum INTEGER PRIMARY KEY, blob BLOB). Each blob is an rpm
// header as rpm stores it — no lead and no magic: a count of index entries,
// the length of the data store, the entries, the store. Only the tags the
// view needs are read.

const (
	rpmTagName    = 1000
	rpmTagVersion = 1001
	rpmTagRelease = 1002
	rpmTagEpoch   = 1003
	rpmTagLicense = 1014
	rpmTagArch    = 1022

	rpmTypeInt32       = 4
	rpmTypeString      = 6
	rpmTypeStringArray = 8
	rpmTypeI18N        = 9
)

// rpmHeader is what one header says about its package.
type rpmHeader struct {
	name, version, release, arch, license string
	epoch                                 int64
	hasEpoch                              bool
}

func parseRPMHeader(b []byte) (rpmHeader, error) {
	var h rpmHeader
	if len(b) < 8 {
		return h, errors.New("an rpm header is shorter than its preamble")
	}
	il := binary.BigEndian.Uint32(b)
	dl := binary.BigEndian.Uint32(b[4:])
	// rpm's own bounds: at most 0xffff entries and 256 MiB of data.
	if il == 0 || il > 0xffff || dl > 256<<20 {
		return h, fmt.Errorf("an rpm header claims %d entries and %d bytes", il, dl)
	}
	start := 8 + 16*int(il)
	if start+int(dl) > len(b) {
		return h, fmt.Errorf("an rpm header claims %d bytes past its blob", start+int(dl)-len(b))
	}
	store := b[start : start+int(dl)]
	str := func(off uint32) (string, bool) {
		if int(off) >= len(store) {
			return "", false
		}
		end := bytes.IndexByte(store[off:], 0)
		if end < 0 {
			return "", false
		}
		return string(store[off : int(off)+end]), true
	}
	seen := map[uint32]bool{}
	for i := 0; i < int(il); i++ {
		e := b[8+16*i:]
		tag, typ := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:])
		off, count := binary.BigEndian.Uint32(e[8:]), binary.BigEndian.Uint32(e[12:])
		if tag < rpmTagName || tag > rpmTagArch || seen[tag] {
			continue // the first entry of a tag is the one rpm reads
		}
		var dst *string
		switch tag {
		case rpmTagName:
			dst = &h.name
		case rpmTagVersion:
			dst = &h.version
		case rpmTagRelease:
			dst = &h.release
		case rpmTagLicense:
			dst = &h.license
		case rpmTagArch:
			dst = &h.arch
		case rpmTagEpoch:
			if typ != rpmTypeInt32 || count < 1 || int(off)+4 > len(store) {
				return h, errors.New("an rpm header's epoch is malformed")
			}
			seen[tag] = true
			h.epoch, h.hasEpoch = int64(binary.BigEndian.Uint32(store[off:])), true
			continue
		default:
			continue
		}
		if typ != rpmTypeString && typ != rpmTypeI18N && typ != rpmTypeStringArray {
			return h, fmt.Errorf("an rpm header's tag %d has type %d, not a string", tag, typ)
		}
		s, ok := str(off)
		if !ok {
			return h, fmt.Errorf("an rpm header's tag %d runs past its data", tag)
		}
		seen[tag] = true
		*dst = s
	}
	if h.name == "" {
		return h, errors.New("an rpm header has no name")
	}
	return h, nil
}

// evr is the version as rpm compares it: [epoch:]version-release.
func (h rpmHeader) evr() string {
	s := h.version
	if h.release != "" {
		s += "-" + h.release
	}
	if h.hasEpoch {
		s = strconv.FormatInt(h.epoch, 10) + ":" + s
	}
	return s
}

// readRPMDB lists the packages of an rpmdb.sqlite held in memory. keep
// bounds the records returned; total counts them all. Any fault fails the
// whole read.
func readRPMDB(b []byte, keep int) (recs []pkgRecord, total int, err error) {
	defer func() {
		// The reader checks every bound it relies on; this is the net
		// under that, so a database it did not foresee fails the read,
		// not the analysis.
		if p := recover(); p != nil {
			recs, total, err = nil, 0, errSQLite("internal error while reading it")
		}
	}()
	return rpmdbRows(b, keep)
}

// rpmdbRows is readRPMDB without the net; the fuzz test calls it, so that
// a fault the checks miss is found rather than caught.
func rpmdbRows(b []byte, keep int) (recs []pkgRecord, total int, err error) {
	db, err := openSQLite(b)
	if err != nil {
		return nil, 0, err
	}
	root, sql, err := db.tableRoot("Packages")
	if err != nil {
		return nil, 0, err
	}
	if !strings.Contains(strings.ToLower(sql), "blob") {
		return nil, 0, errSQLite("the Packages table has no blob column")
	}
	err = db.tableRows(root, func(_ int64, rec []sqliteValue) error {
		if len(rec) < 2 || rec[1].isInt || rec[1].null {
			return errSQLite("a Packages row has no header blob")
		}
		h, err := parseRPMHeader(rec[1].b)
		if err != nil {
			return err
		}
		total++
		if len(recs) < keep {
			recs = append(recs, pkgRecord{
				name: sanitize(h.name, 128), version: sanitize(h.evr(), 128),
				arch: sanitize(h.arch, 32), license: sanitize(h.license, 1024),
				pseudo: h.name == "gpg-pubkey",
			})
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return recs, total, nil
}
