package forensics

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The rpm fixtures (testdata/rpm):
//
//   - ubi9-micro.rpmdb.sqlite.gz is /var/lib/rpm/rpmdb.sqlite taken
//     unchanged from registry.access.redhat.com/ubi9/ubi-micro (image
//     sha256:5be877a2a280ae36eb9ab80ca4efb28bbf77c57abbba38137cdc37a03b8465e6,
//     built 2026-10-13; file sha256 82c2735e…66e0a). ubi9-micro.rpm-qa.tsv
//     is what rpm 4.16.1.3 itself reports for that file, from the
//     ubi9/ubi-minimal image (ubi-micro carries no rpm):
//     rpm --dbpath <copy> -qa --qf '%{NAME}\t%{EPOCH}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\t%{LICENSE}\n' | sort
//   - synthetic.rpmdb.sqlite.gz and tiny.rpmdb.sqlite are written by SQLite
//     itself; see make_synthetic.py for what they exercise.

// gunzipped holds each fixture decompressed once per test binary; callers
// get their own copy.
var gunzipped sync.Map

func gunzipFile(t testing.TB, name string) []byte {
	t.Helper()
	if b, ok := gunzipped.Load(name); ok {
		return append([]byte(nil), b.([]byte)...)
	}
	f, err := os.Open("testdata/rpm/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	gunzipped.Store(name, b)
	return append([]byte(nil), b...)
}

// rpmQA renders records the way the fixture's rpm -qa query does.
func rpmQA(recs []pkgRecord) []string {
	var out []string
	for _, r := range recs {
		epoch, rest := "(none)", r.version
		if e, v := splitEpoch(r.version); e != "" || strings.HasPrefix(r.version, "0:") {
			epoch, rest = e, v
			if epoch == "" {
				epoch = "0"
			}
		}
		ver, rel := rest, ""
		if i := strings.LastIndexByte(rest, '-'); i >= 0 {
			ver, rel = rest[:i], rest[i+1:]
		}
		arch := r.arch
		if arch == "" {
			arch = "(none)"
		}
		out = append(out, strings.Join([]string{r.name, epoch, ver, rel, arch, r.license}, "\t"))
	}
	sort.Strings(out)
	return out
}

func expectedQA(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/rpm/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	sort.Strings(lines)
	return lines
}

func sameLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d packages, rpm reports %d", len(got), len(want))
	}
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			t.Fatalf("package %d:\n got  %q\n want %q", i, got[i], want[i])
		}
	}
}

// TestRealRPMDBYieldsWhatRPMReports: the database from a real image reads
// as exactly the package set rpm itself reports for it.
func TestRealRPMDBYieldsWhatRPMReports(t *testing.T) {
	db := gunzipFile(t, "ubi9-micro.rpmdb.sqlite.gz")
	recs, total, err := readRPMDB(db, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(recs) || total != 22 {
		t.Fatalf("total %d, kept %d; want 22 (20 packages and 2 signing keys)", total, len(recs))
	}
	sameLines(t, rpmQA(recs), expectedQA(t, "ubi9-micro.rpm-qa.tsv"))
	pseudo := 0
	for _, r := range recs {
		if r.pseudo {
			pseudo++
			if r.name != "gpg-pubkey" {
				t.Errorf("%s marked as not a package", r.name)
			}
		}
	}
	if pseudo != 2 {
		t.Errorf("%d gpg-pubkey entries marked, want 2", pseudo)
	}
}

// TestSyntheticRPMDB: a database SQLite wrote with 512-byte pages — deep
// b-trees, long overflow chains, freed pages, epochs.
func TestSyntheticRPMDB(t *testing.T) {
	db := gunzipFile(t, "synthetic.rpmdb.sqlite.gz")
	recs, total, err := readRPMDB(db, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1001 {
		t.Fatalf("total %d, want 1001", total)
	}
	sameLines(t, rpmQA(recs), expectedQA(t, "synthetic.expected.tsv"))
	// The bound keeps the first records and still counts them all.
	recs, total, err = readRPMDB(db, 10)
	if err != nil || len(recs) != 10 || total != 1001 {
		t.Fatalf("bounded read: %d kept, %d total, %v", len(recs), total, err)
	}
}

// TestDamagedRPMDBFailsWhole: a database that is cut short, or has bytes
// changed, either fails to read or reads whole — never a part of it, never
// a panic.
func TestDamagedRPMDBFailsWhole(t *testing.T) {
	// The suite's other packages run beside this one, some of them on a
	// clock: the mutations reuse one buffer, and the 7 MB real database
	// gets fewer of them than the synthetic one, whose pages are smaller.
	for _, tc := range []struct {
		name      string
		mutations int
	}{{"ubi9-micro.rpmdb.sqlite.gz", 10}, {"synthetic.rpmdb.sqlite.gz", 80}} {
		name := tc.name
		orig := gunzipFile(t, name)
		if _, _, err := readRPMDB(orig, 1<<20); err != nil {
			t.Fatal(err)
		}
		// Cut short: no longer a whole number of pages, or missing pages.
		for _, n := range []int{0, 50, 99, 100, 4096, len(orig) / 2, len(orig) - 512} {
			if recs, _, err := readRPMDB(orig[:n], 1<<20); err == nil {
				t.Errorf("%s cut to %d bytes read %d packages without error", name, n, len(recs))
			}
		}
		// Bytes changed at random: whatever happens, it is whole or nothing.
		rnd := rand.New(rand.NewSource(1))
		b := make([]byte, len(orig))
		for i := 0; i < tc.mutations; i++ {
			copy(b, orig)
			for k := 0; k < 1+rnd.Intn(8); k++ {
				b[rnd.Intn(len(b))] = byte(rnd.Intn(256))
			}
			recs, total, err := readRPMDB(b, 1<<20)
			if err == nil && total != len(recs) {
				t.Fatalf("%s: %d of %d records returned without error", name, len(recs), total)
			}
		}
	}
	// Page 1 points the schema at itself: a cycle, caught.
	b := gunzipFile(t, "synthetic.rpmdb.sqlite.gz")
	if b[100] == 0x05 {
		binary.BigEndian.PutUint32(b[108:], 1)
		if _, _, err := readRPMDB(b, 10); err == nil || !strings.Contains(err.Error(), "reached twice") {
			t.Errorf("a cycle in the b-tree: %v", err)
		}
	} else {
		t.Error("the synthetic schema should span pages: page 1 is not an interior page")
	}
}

func TestRPMHeaderBounds(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0, 0, 0, 1},
		{0, 0, 0, 0, 0, 0, 0, 0},                // no entries
		{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0},    // too many entries
		{0, 0, 0, 1, 0, 0, 0, 8, 0, 0, 3, 0xe8}, // runs past its blob
	} {
		if _, err := parseRPMHeader(b); err == nil {
			t.Errorf("%x parsed", b)
		}
	}
	// A string without its terminator.
	h := []byte{0, 0, 0, 1, 0, 0, 0, 3, 0, 0, 3, 0xe8, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 1, 'a', 'b', 'c'}
	if _, err := parseRPMHeader(h); err == nil {
		t.Error("an unterminated name parsed")
	}
	h[len(h)-1] = 0
	if got, err := parseRPMHeader(h); err != nil || got.name != "ab" {
		t.Errorf("%+v %v", got, err)
	}
}

func FuzzReadRPMDB(f *testing.F) {
	tiny, err := os.ReadFile("testdata/rpm/tiny.rpmdb.sqlite")
	if err != nil {
		f.Fatal(err)
	}
	if recs, _, err := readRPMDB(tiny, 100); err != nil || len(recs) != 4 || recs[3].version != "3:1-1" {
		f.Fatalf("the seed does not read: %v %v", recs, err)
	}
	f.Add(tiny)
	f.Fuzz(func(t *testing.T, b []byte) {
		recs, total, err := rpmdbRows(b, 100)
		if err == nil && total < len(recs) {
			t.Fatal("more kept than counted")
		}
	})
}

func TestSQLiteRefusesWhatItDoesNotRead(t *testing.T) {
	db := gunzipFile(t, "ubi9-micro.rpmdb.sqlite.gz")
	b := append([]byte{}, db...)
	binary.BigEndian.PutUint32(b[56:], 2) // UTF-16le
	if _, _, err := readRPMDB(b, 10); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("UTF-16: %v", err)
	}
	b = append([]byte{}, db...)
	copy(b, "SQLite format 2\x00")
	if _, _, err := readRPMDB(b, 10); err == nil {
		t.Error("a bad header read")
	}
	if !bytes.HasPrefix(db, []byte("SQLite format 3")) {
		t.Fatal("fixture")
	}
}

// TestMutatedRPMDBNeverPanics runs the reader without its net over many
// mutations of a small database: every fault must be one it checks for.
func TestMutatedRPMDBNeverPanics(t *testing.T) {
	tiny, err := os.ReadFile("testdata/rpm/tiny.rpmdb.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(7))
	start := time.Now()
	for i := 0; i < 20000; i++ {
		b := append([]byte{}, tiny...)
		for k := 0; k < 1+rnd.Intn(4); k++ {
			b[rnd.Intn(len(b))] = byte(rnd.Intn(256))
		}
		recs, total, err := rpmdbRows(b, 100)
		if err == nil && total != len(recs) {
			t.Fatalf("%d of %d records without error", len(recs), total)
		}
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("20000 small databases took %v", d)
	}
}
