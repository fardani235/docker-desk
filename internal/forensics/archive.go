package forensics

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
)

// Limits bound every buffer the analysis holds. What a bound cuts off is
// reported as unknown.
type Limits struct {
	JSONBlob    int64 // largest JSON document read whole (config, manifests, attestations)
	JSONTotal   int64 // all JSON documents together
	ScanBytes   int   // leading bytes of a file searched for secrets
	ScanFileMax int64 // larger files are searched only when their name suggests a secret
	ELFPrefix   int   // leading bytes of an ELF file read for its headers
	GoWindow    int   // bytes kept from Go's build information once found
	Paths       int   // path fingerprints kept across all layers (8 bytes each)
	Symlinks    int   // symbolic links kept across all layers
	PerLayerObs int   // observations of each kind kept per layer
	PerRule     int   // findings listed per rule and layer; the rest are counted
	Findings    int   // findings in a report
	// SecretReveal bounds a secret's kept value.
	SecretReveal int

	// The package lists (packages.go). Declared bounds the packages kept
	// from one SBOM, SBOMDocs the SBOM documents whose tables are kept.
	Declared int
	SBOMDocs int
	// Installed bounds the packages kept from one copy of a database, and
	// the final list; InstalledTotal the records kept across every copy
	// of every layer, for the layer diff; DBCopies the database files kept
	// per layer.
	Installed      int
	InstalledTotal int
	DBCopies       int
	// RPMDB and TextDB bound the bytes of one database file read: an
	// rpmdb.sqlite, a dpkg or apk text database.
	RPMDB  int64
	TextDB int64

	// The Files view's index (files.go): FileEntries bounds the entries
	// kept across every layer, FilePath the bytes of one path (Linux's
	// PATH_MAX), FileText the bytes of path text kept altogether. A zero
	// value takes the default, so a caller that sets other bounds keeps
	// these.
	FileEntries int
	FilePath    int
	FileText    int64
}

// DefaultLimits are the bounds the desk uses.
func DefaultLimits() Limits {
	return Limits{
		JSONBlob:     4 << 20,
		JSONTotal:    16 << 20,
		ScanBytes:    64 << 10,
		ScanFileMax:  2 << 20,
		ELFPrefix:    1 << 20,
		GoWindow:     64 << 10,
		Paths:        2_000_000,
		Symlinks:     50_000,
		PerLayerObs:  300,
		PerRule:      25,
		Findings:     3000,
		SecretReveal: 512,

		Declared:       20_000,
		SBOMDocs:       4,
		Installed:      10_000,
		InstalledTotal: 200_000,
		DBCopies:       4096,
		RPMDB:          64 << 20,
		TextDB:         8 << 20,

		FileEntries: 200_000,
		FilePath:    4096,
		FileText:    48 << 20,
	}
}

// Thresholds are where the layer rules start reporting.
type Thresholds struct {
	SizeShare float64 // a layer holding at least this share of the bytes
	SizeMin   int64   // … of an image at least this large
	FileCount int     // a layer adding at least this many files
}

// DefaultThresholds are the thresholds the desk uses.
func DefaultThresholds() Thresholds {
	return Thresholds{SizeShare: 0.5, SizeMin: 10 << 20, FileCount: 10_000}
}

// Mode is what an analysis reads the archive for.
type Mode int

const (
	// ModeFindings is the Findings analysis: every rule, the secret scan,
	// the package databases. The zero value, so every caller that predates
	// the Files view is unchanged.
	ModeFindings Mode = iota
	// ModeFiles reads tar headers only, for the Files view: no file's
	// bytes are read, no rule is evaluated, no secret is searched for and
	// no package database is captured. Report.Files holds the listing.
	ModeFiles
	// ModeBoth is the Findings analysis with the listing kept as well.
	ModeBoth
)

// Options configures one analysis.
type Options struct {
	// Mode selects what is read; the zero value is ModeFindings.
	Mode     Mode
	ImageID  string
	Ref      string // the reference the image was chosen by, to pick its manifest entry
	Platform string // the platform asked for, os/arch[/variant]
	// DaemonOS and DaemonArch are the daemon's own platform, for the
	// platform mismatch rule; empty skips it.
	DaemonOS, DaemonArch string
	Limits               Limits
	Thresholds           Thresholds
}

// Progress is how far an analysis has got.
type Progress struct {
	BytesRead int64
	Layers    int // layer blobs found so far
	Entries   int // headers seen for the Files listing; 0 in ModeFindings
}

// analysisError carries only the desk's own words: no daemon text and no
// byte of the image can reach it, so it is safe in a job record.
type analysisError string

func (e analysisError) Error() string { return string(e) }

func failf(format string, args ...any) error { return analysisError(fmt.Sprintf(format, args...)) }

// Analyse reads an image archive from r to its end and reports on it.
// progress, when set, is called from the reading goroutine as bytes arrive.
func Analyse(ctx context.Context, r io.Reader, opts Options, progress func(Progress)) (rep *Report, err error) {
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}
	if opts.Thresholds == (Thresholds{}) {
		opts.Thresholds = DefaultThresholds()
	}
	def := DefaultLimits()
	if opts.Limits.FileEntries == 0 {
		opts.Limits.FileEntries = def.FileEntries
	}
	if opts.Limits.FilePath == 0 {
		opts.Limits.FilePath = def.FilePath
	}
	if opts.Limits.FileText == 0 {
		opts.Limits.FileText = def.FileText
	}
	defer func() {
		// A malformed archive must fail the analysis, not the desk — and
		// a panic's text could quote the bytes being read, so it is not
		// passed on.
		if p := recover(); p != nil {
			rep, err = nil, failf("internal error while reading the archive")
		}
	}()
	a := newAnalyser(ctx, opts, progress)
	if err := a.read(r); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return a.finish()
}

type analyser struct {
	ctx      context.Context
	opts     Options
	lim      Limits
	progress func(Progress)

	read_    atomic.Int64
	lastTold int64
	layers   int

	meta      map[string][]byte // manifest.json, index.json, oci-layout
	json      map[string][]byte // other JSON documents, by archive path
	jsonUsed  int64
	bigJSON   map[string]int64 // JSON documents too large to keep, by path
	aliases   map[string]string
	scans     map[string]*layerScan
	notes     []string
	pathsUsed int
	linksUsed int

	head []byte // reused: leading bytes of each file
	elf  []byte // reused: leading bytes of an ELF file

	sboms  map[string]sbomScan // what each JSON blob declares, by archive path
	dbBuf  []byte              // reused: one package database file
	dbUsed int                 // database records kept across all layers

	files *fileBounds // the Files listing's counts; nil in ModeFindings
}

func newAnalyser(ctx context.Context, opts Options, progress func(Progress)) *analyser {
	a := &analyser{
		ctx: ctx, opts: opts, lim: opts.Limits, progress: progress,
		meta: map[string][]byte{}, json: map[string][]byte{}, bigJSON: map[string]int64{},
		aliases: map[string]string{}, scans: map[string]*layerScan{}, sboms: map[string]sbomScan{},
	}
	if opts.Mode != ModeFiles {
		// Headers only: no file's leading bytes are ever read.
		a.head, a.elf = make([]byte, opts.Limits.ScanBytes), make([]byte, opts.Limits.ELFPrefix)
	}
	if opts.Mode != ModeFindings {
		a.files = &fileBounds{}
	}
	return a
}

func (a *analyser) note(format string, args ...any) {
	n := fmt.Sprintf(format, args...)
	for _, x := range a.notes {
		if x == n {
			return
		}
	}
	if len(a.notes) < 64 {
		a.notes = append(a.notes, n)
	}
}

// countingReader counts bytes, reports progress and stops on cancellation
// even when the reader underneath would not.
type countingReader struct {
	r io.Reader
	a *analyser
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.a.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	total := c.a.read_.Add(int64(n))
	if total-c.a.lastTold >= 1<<20 {
		c.a.tell()
	}
	return n, err
}

func (a *analyser) tell() {
	a.lastTold = a.read_.Load()
	if a.progress != nil {
		p := Progress{BytesRead: a.lastTold, Layers: a.layers}
		if a.files != nil {
			p.Entries = a.files.seen
		}
		a.progress(p)
	}
}

// archivePath normalises an entry name of the outer archive.
func archivePath(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

func (a *analyser) read(r io.Reader) error {
	cr := &countingReader{r: r, a: a}
	tr := tar.NewReader(cr)
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if a.ctx.Err() != nil {
				return a.ctx.Err()
			}
			if entries == 0 {
				return failf("the daemon's answer is not an image archive (no tar header could be read)")
			}
			return failf("the archive stream broke off after %d entries and %s", entries, humanSize(a.read_.Load()))
		}
		entries++
		name := archivePath(h.Name)
		switch h.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			target := h.Linkname
			if h.Typeflag == tar.TypeSymlink && !strings.HasPrefix(target, "/") {
				target = path.Join(path.Dir("/"+name), target)
			}
			a.aliases[name] = archivePath(target)
			continue
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // old archives use TypeRegA
		default:
			continue
		}
		switch name {
		case "manifest.json", "index.json", "oci-layout", "repositories":
			b, err := a.readJSON(tr, h.Size)
			if err != nil {
				return err
			}
			if b == nil {
				a.note("%s is larger than %s and was not read", name, humanSize(a.lim.JSONBlob))
				continue
			}
			a.meta[name] = b
		default:
			if err := a.blob(name, h.Size, tr); err != nil {
				return err
			}
		}
		a.tell()
	}
	if entries == 0 {
		return failf("the daemon sent an empty archive")
	}
	// Whatever follows the end-of-archive blocks is read too, so the
	// count is the whole answer and the connection can be reused.
	_, _ = io.Copy(io.Discard, cr)
	a.tell()
	return nil
}

// readJSON reads a document whole when it fits the bounds; nil when not.
func (a *analyser) readJSON(r io.Reader, size int64) ([]byte, error) {
	if size > a.lim.JSONBlob || a.jsonUsed+size > a.lim.JSONTotal {
		_, err := io.Copy(io.Discard, r)
		return nil, err
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		if a.ctx.Err() != nil {
			return nil, a.ctx.Err()
		}
		return nil, failf("the archive stream broke off inside a document")
	}
	a.jsonUsed += size
	return b, nil
}

// jsonBlob reads a JSON document: kept whole when it fits the bounds, and
// in every case scanned as it streams for the packages an SBOM declares,
// so a document too large to keep is still read for those.
func (a *analyser) jsonBlob(name string, size int64, r io.Reader) error {
	keep := size <= a.lim.JSONBlob && a.jsonUsed+size <= a.lim.JSONTotal
	if a.opts.Mode == ModeFiles {
		// The config is all the listing needs; no SBOM is scanned.
		b, err := a.readJSON(r, size)
		if err != nil {
			return err
		}
		if b == nil {
			a.bigJSON[name] = size
		} else {
			a.json[name] = b
		}
		return nil
	}
	var whole *bytes.Buffer
	src := r
	if keep {
		whole = bytes.NewBuffer(make([]byte, 0, size))
		src = io.TeeReader(r, whole)
	}
	declared := a.lim.Declared
	kept := 0
	for _, sc := range a.sboms {
		if sc.format != "" {
			kept++
		}
	}
	if kept >= a.lim.SBOMDocs {
		declared = 0 // counted, not kept
	}
	sc := scanJSON(src, declared)
	if _, err := io.Copy(io.Discard, src); err != nil {
		if a.ctx.Err() != nil {
			return a.ctx.Err()
		}
		return failf("the archive stream broke off inside a document")
	}
	if sc.format != "" || sc.err != "" {
		if sc.err != "" && a.ctx.Err() != nil {
			return a.ctx.Err()
		}
		if declared == 0 && sc.format != "" && sc.total > 0 {
			sc.err = fmt.Sprintf("the archive holds more than %d SBOM documents; this one was counted, not kept", a.lim.SBOMDocs)
		}
		a.sboms[name] = sc
	}
	if !keep {
		a.bigJSON[name] = size
		return nil
	}
	if int64(whole.Len()) != size {
		return failf("the archive stream broke off inside a document")
	}
	a.jsonUsed += size
	a.json[name] = whole.Bytes()
	return nil
}

// blob classifies an archive entry by its first bytes — the layout does not
// say which blobs are layers until manifest.json, which may come last — and
// reads it accordingly.
func (a *analyser) blob(name string, size int64, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
	peek, _ := br.Peek(512)
	switch {
	case len(peek) >= 2 && peek[0] == 0x1f && peek[1] == 0x8b:
		return a.layer(name, size, br, "gzip")
	case len(peek) >= 4 && string(peek[:4]) == "\x28\xb5\x2f\xfd":
		return a.layer(name, size, br, "zstd")
	case len(peek) >= 262 && string(peek[257:262]) == "ustar":
		return a.layer(name, size, br, "none")
	case len(peek) == 512 && bytes.Count(peek, []byte{0}) == 512:
		return a.layer(name, size, br, "none") // an empty layer: two zero blocks
	case firstNonSpace(peek) == '{' || firstNonSpace(peek) == '[':
		return a.jsonBlob(name, size, br)
	}
	_, err := io.Copy(io.Discard, br)
	return err
}

func firstNonSpace(b []byte) byte {
	for _, c := range b {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return c
		}
	}
	return 0
}

// resolve follows archive symlinks (the classic layout links identical
// layers to one copy).
func (a *analyser) resolve(p string) string {
	p = archivePath(p)
	for i := 0; i < 8; i++ {
		t, ok := a.aliases[p]
		if !ok {
			return p
		}
		p = t
	}
	return p
}

// humanSize is the desk's own size format (decimal units, no space).
func humanSize(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(1000), 0
	for v := n / 1000; v >= 1000; v /= 1000 {
		div *= 1000
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f%cB", v, "kMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%cB", v, "kMGTPE"[exp])
}
