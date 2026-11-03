package forensics

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// A read-only SQLite reader that does one thing: enumerate the rows of a
// table, from a database file held in memory. It is what rpm's
// rpmdb.sqlite needs and no more — no SQL, no indexes, no write-ahead log,
// no journal (the caller refuses a database that has either; see
// pkgdb.go). Every page number, offset and length read from the file is
// checked against the file before it is used, every page is visited at most
// once, and any inconsistency fails the whole read: a table that cannot be
// read whole is not read at all.
//
// The format is https://www.sqlite.org/fileformat2.html; the sections used
// are 1.3 (the header), 1.6 (b-tree pages), 1.7 (cell payload and overflow)
// and 2.1 (the record format).

type sqliteDB struct {
	b        []byte
	pageSize int
	usable   int
	pages    int
	seen     []bool // pages visited by the current walk
}

// sqliteValue is one column of a record. Integers are decoded; text and
// blobs are slices of a buffer the caller must not keep.
type sqliteValue struct {
	null  bool
	isInt bool
	i     int64
	text  bool
	b     []byte
}

func errSQLite(format string, args ...any) error {
	return errors.New("the SQLite file is not readable: " + fmt.Sprintf(format, args...))
}

func openSQLite(b []byte) (*sqliteDB, error) {
	if len(b) < 100 || string(b[:16]) != "SQLite format 3\x00" {
		return nil, errSQLite("no SQLite header")
	}
	ps := int(binary.BigEndian.Uint16(b[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps > 65536 || ps&(ps-1) != 0 {
		return nil, errSQLite("page size %d", ps)
	}
	reserved := int(b[20])
	if b[21] != 64 || b[22] != 32 || b[23] != 32 {
		return nil, errSQLite("payload fractions are not 64/32/32")
	}
	if enc := binary.BigEndian.Uint32(b[56:60]); enc != 1 && enc != 0 {
		return nil, errSQLite("text is not UTF-8 (encoding %d)", enc)
	}
	usable := ps - reserved
	if usable < 480 {
		return nil, errSQLite("usable page size %d", usable)
	}
	if len(b)%ps != 0 {
		return nil, errSQLite("file length %d is not a whole number of %d-byte pages", len(b), ps)
	}
	pages := len(b) / ps
	// The header's own page count is valid only when written by a version
	// that keeps it; when it is, it must agree with the file.
	if n := int(binary.BigEndian.Uint32(b[28:32])); n != 0 && binary.BigEndian.Uint32(b[24:28]) == binary.BigEndian.Uint32(b[92:96]) && n != pages {
		return nil, errSQLite("the header counts %d pages, the file holds %d", n, pages)
	}
	return &sqliteDB{b: b, pageSize: ps, usable: usable, pages: pages}, nil
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	if n < 1 || int(n) > db.pages {
		return nil, errSQLite("page %d is outside the file (%d pages)", n, db.pages)
	}
	if db.seen[n] {
		return nil, errSQLite("page %d is reached twice", n)
	}
	db.seen[n] = true
	off := int(n-1) * db.pageSize
	return db.b[off : off+db.pageSize], nil
}

// readVarint decodes SQLite's big-endian variable-length integer.
func readVarint(b []byte) (uint64, int, bool) {
	var v uint64
	for i := 0; i < 9; i++ {
		if i >= len(b) {
			return 0, 0, false
		}
		if i == 8 {
			return v<<8 | uint64(b[i]), 9, true
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

// tableRows calls each with the rowid and record of every row of the table
// b-tree rooted at root, in rowid order.
func (db *sqliteDB) tableRows(root uint32, each func(rowid int64, rec []sqliteValue) error) error {
	db.seen = make([]bool, db.pages+1)
	return db.walk(root, 0, each)
}

func (db *sqliteDB) walk(n uint32, depth int, each func(int64, []sqliteValue) error) error {
	if depth > 20 {
		return errSQLite("the b-tree is deeper than 20 levels")
	}
	pg, err := db.page(n)
	if err != nil {
		return err
	}
	hdr := 0
	if n == 1 {
		hdr = 100 // page 1 starts with the file header
	}
	if len(pg) < hdr+12 {
		return errSQLite("page %d is too short", n)
	}
	kind := pg[hdr]
	cells := int(binary.BigEndian.Uint16(pg[hdr+3:]))
	ptrs := hdr + 8
	if kind == 0x05 {
		ptrs = hdr + 12
	}
	if ptrs+2*cells > db.usable {
		return errSQLite("page %d claims %d cells", n, cells)
	}
	cell := func(i int) (int, error) {
		off := int(binary.BigEndian.Uint16(pg[ptrs+2*i:]))
		if off < ptrs+2*cells || off >= db.usable {
			return 0, errSQLite("page %d: cell %d at offset %d", n, i, off)
		}
		return off, nil
	}
	switch kind {
	case 0x05: // interior table page: child pointers, then the right-most child
		for i := 0; i < cells; i++ {
			off, err := cell(i)
			if err != nil {
				return err
			}
			if off+4 > db.usable {
				return errSQLite("page %d: cell %d is cut off", n, i)
			}
			if err := db.walk(binary.BigEndian.Uint32(pg[off:]), depth+1, each); err != nil {
				return err
			}
		}
		return db.walk(binary.BigEndian.Uint32(pg[hdr+8:]), depth+1, each)
	case 0x0d: // leaf table page: rows
		for i := 0; i < cells; i++ {
			off, err := cell(i)
			if err != nil {
				return err
			}
			p := pg[off:db.usable]
			size, k, ok := readVarint(p)
			if !ok {
				return errSQLite("page %d: cell %d payload size", n, i)
			}
			p = p[k:]
			rowid, k, ok := readVarint(p)
			if !ok {
				return errSQLite("page %d: cell %d rowid", n, i)
			}
			p = p[k:]
			payload, err := db.payload(p, size)
			if err != nil {
				return fmt.Errorf("%w (page %d, cell %d)", err, n, i)
			}
			rec, err := parseRecord(payload)
			if err != nil {
				return fmt.Errorf("%w (page %d, cell %d)", err, n, i)
			}
			if err := each(int64(rowid), rec); err != nil {
				return err
			}
		}
		return nil
	}
	return errSQLite("page %d is not a table b-tree page (type %d)", n, kind)
}

// payload assembles a cell's payload: the part stored on the page, then
// the overflow chain.
func (db *sqliteDB) payload(local []byte, size uint64) ([]byte, error) {
	if size > uint64(len(db.b)) {
		return nil, errSQLite("a payload of %d bytes in a %d-byte file", size, len(db.b))
	}
	p := int(size)
	u := db.usable
	x := u - 35
	inPage := p
	if p > x {
		m := ((u-12)*32/255 - 23)
		k := m + (p-m)%(u-4)
		inPage = m
		if k <= x {
			inPage = k
		}
	}
	if inPage == p {
		if len(local) < p {
			return nil, errSQLite("a payload runs off its page")
		}
		return local[:p], nil
	}
	if len(local) < inPage+4 {
		return nil, errSQLite("a payload's overflow pointer runs off its page")
	}
	out := make([]byte, 0, p)
	out = append(out, local[:inPage]...)
	next := binary.BigEndian.Uint32(local[inPage:])
	for len(out) < p {
		if next == 0 {
			return nil, errSQLite("an overflow chain ends %d bytes short", p-len(out))
		}
		pg, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = binary.BigEndian.Uint32(pg)
		take := min(u-4, p-len(out))
		out = append(out, pg[4:4+take]...)
	}
	if next != 0 {
		return nil, errSQLite("an overflow chain continues past its payload")
	}
	return out, nil
}

// parseRecord decodes the record format: a header of serial types, then
// the values.
func parseRecord(p []byte) ([]sqliteValue, error) {
	hsize, k, ok := readVarint(p)
	if !ok || hsize < uint64(k) || hsize > uint64(len(p)) {
		return nil, errSQLite("a record header is malformed")
	}
	types := p[k:hsize]
	body := p[hsize:]
	var out []sqliteValue
	for len(types) > 0 {
		st, k, ok := readVarint(types)
		if !ok {
			return nil, errSQLite("a record's serial type is malformed")
		}
		types = types[k:]
		var n int
		v := sqliteValue{}
		switch {
		case st == 0:
			v.null = true
		case st >= 1 && st <= 6:
			n = []int{0, 1, 2, 3, 4, 6, 8}[st]
			v.isInt = true
		case st == 7:
			n = 8
		case st == 8 || st == 9:
			v.isInt, v.i = true, int64(st-8)
		case st >= 12:
			if st > uint64(len(body))*2+13 {
				return nil, errSQLite("a record value is longer than its record")
			}
			n = int((st - 12) / 2)
			v.text = st%2 == 1
		default:
			return nil, errSQLite("a record uses reserved serial type %d", st)
		}
		if n > len(body) {
			return nil, errSQLite("a record value runs past its record")
		}
		if v.isInt && n > 0 {
			var x int64
			for _, c := range body[:n] {
				x = x<<8 | int64(c)
			}
			if shift := 64 - 8*n; shift > 0 {
				x = x << shift >> shift // sign-extend
			}
			v.i = x
		} else {
			v.b = body[:n]
		}
		body = body[n:]
		out = append(out, v)
	}
	return out, nil
}

// tableRoot finds a table's root page, and its CREATE statement, in the
// schema table on page 1.
func (db *sqliteDB) tableRoot(name string) (uint32, string, error) {
	var root uint32
	var sql string
	err := db.tableRows(1, func(_ int64, rec []sqliteValue) error {
		if len(rec) < 5 || root != 0 {
			return nil
		}
		if string(rec[0].b) == "table" && strings.EqualFold(string(rec[1].b), name) && rec[3].isInt {
			if rec[3].i < 1 || rec[3].i > int64(db.pages) {
				return errSQLite("table %s has root page %d", name, rec[3].i)
			}
			root, sql = uint32(rec[3].i), string(rec[4].b)
		}
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	if root == 0 {
		return 0, "", errSQLite("there is no %s table", name)
	}
	return root, sql, nil
}
