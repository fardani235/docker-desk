package forensics

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"path"
	"sort"
	"strings"
)

// fileObs is a file whose mode or attributes a rule reports.
type fileObs struct {
	rule, path, evidence string
}

// secretObs is a secret found in a file of a layer.
type secretObs struct {
	path string
	hit  secretHit
}

type elfObs struct {
	path  string
	facts elfFacts
}

// aggregate counts files of one kind (a cache, build junk) in a layer.
type aggregate struct {
	kind    string
	root    string // the directory the kind lives in, for the deletion check
	files   int
	bytes   int64
	example string
	certain bool
}

// layerScan is everything kept about one layer blob while the archive
// streams past. Layer order is known only once manifest.json has been read,
// so cross-layer rules run afterwards, on these.
type layerScan struct {
	path, compression string
	digest, diffID    string
	stored, size      int64
	files, dirs       int
	whiteouts         map[string]bool
	opaques           map[string]bool
	paths             []uint64 // fingerprints of every path added, sorted
	pathsCapped       bool
	symlinks          map[string]string
	fileObs           []fileObs
	secrets           []secretObs
	elfs              []elfObs
	aggs              map[string]*aggregate
	dropped           map[string]int // observations beyond Limits.PerLayerObs
	unreadable        string
	dbs               []dbCopy   // copies of package databases (pkgdb.go)
	dbsDropped        bool       // more than Limits.DBCopies
	fl                LayerFiles // the Files listing's headers (files.go)
}

func fingerprint(p string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(p))
	return h.Sum64()
}

func (s *layerScan) has(p string) bool {
	f := fingerprint(p)
	i := sort.Search(len(s.paths), func(i int) bool { return s.paths[i] >= f })
	return i < len(s.paths) && s.paths[i] == f
}

// hashCounter hashes and counts what passes through it.
type hashCounter struct {
	h hash.Hash
	n int64
}

func (c *hashCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

func (c *hashCounter) sum() string { return "sha256:" + hex.EncodeToString(c.h.Sum(nil)) }

func (a *analyser) layer(name string, _ int64, r io.Reader, compression string) error {
	a.layers++
	s := &layerScan{
		path: name, compression: compression,
		whiteouts: map[string]bool{}, opaques: map[string]bool{},
		symlinks: map[string]string{}, aggs: map[string]*aggregate{}, dropped: map[string]int{},
	}
	a.scans[name] = s
	raw := &hashCounter{h: sha256.New()}
	in := io.TeeReader(r, raw)
	diff := &hashCounter{h: sha256.New()}
	var plain io.Reader
	switch compression {
	case "gzip":
		zr, err := gzip.NewReader(in)
		if err != nil {
			s.unreadable = "the layer is marked gzip but does not decompress"
			break
		}
		zr.Multistream(false)
		plain = zr
	case "zstd":
		s.unreadable = "the layer is zstd-compressed; reading it needs a decoder the desk does not carry"
	default:
		plain = in
	}
	if plain != nil {
		src := io.TeeReader(plain, diff)
		if compression == "none" {
			src = plain // the stored bytes are the tar: one hash is both
		}
		walkErr := a.walk(s, src)
		if walkErr != nil && a.ctx.Err() != nil {
			return a.ctx.Err()
		}
		if walkErr != nil {
			s.unreadable = walkErr.Error()
		}
		// The tar ends with padding the reader does not consume: the diff
		// ID covers it.
		if _, err := io.Copy(io.Discard, src); err != nil && a.ctx.Err() != nil {
			return a.ctx.Err()
		}
	}
	if _, err := io.Copy(io.Discard, in); err != nil {
		if a.ctx.Err() != nil {
			return a.ctx.Err()
		}
		return failf("the archive stream broke off inside a layer")
	}
	s.digest, s.stored = raw.sum(), raw.n
	switch {
	case s.unreadable != "":
	case compression == "none":
		s.diffID = s.digest
	default:
		s.diffID = diff.sum()
	}
	sort.Slice(s.paths, func(i, j int) bool { return s.paths[i] < s.paths[j] })
	a.tell()
	return nil
}

// layerPath normalises a path inside a layer to an absolute one.
func layerPath(name string) string { return path.Clean("/" + name) }

func (a *analyser) walk(s *layerScan, r io.Reader) error {
	tr := tar.NewReader(r)
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if a.ctx.Err() != nil {
				return a.ctx.Err()
			}
			return fmt.Errorf("the layer's tar stream is damaged after %d entries", entries)
		}
		entries++
		p := layerPath(h.Name)
		if p == "/" {
			continue
		}
		dir, base := path.Split(p)
		dir = path.Clean(dir)
		if base == ".wh..wh..opq" {
			s.opaques[dir] = true
			a.fileMarker(s, dir, true)
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			gone := path.Join(dir, strings.TrimPrefix(base, ".wh."))
			if len(s.whiteouts) < a.lim.PerLayerObs*100 {
				s.whiteouts[gone] = true
			}
			a.fileMarker(s, gone, false)
			continue
		}
		a.fileEntry(s, p, h)
		if a.opts.Mode == ModeFiles {
			// Headers only: the layer's counts, and nothing of any body.
			switch h.Typeflag {
			case tar.TypeDir:
				s.dirs++
			case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // old layers use TypeRegA
				s.files++
				s.size += h.Size
			default:
				s.files++
			}
			continue
		}
		if a.pathsUsed < a.lim.Paths {
			s.paths = append(s.paths, fingerprint(p))
			a.pathsUsed++
		} else {
			s.pathsCapped = true
		}
		var body io.Reader = tr
		if kind := classifyDB(p); kind != dbNone && h.Typeflag != tar.TypeDir {
			r, err := a.dbCapture(s, p, kind, h, tr)
			if err != nil {
				return err
			}
			body = r
		}
		mode := h.Mode
		switch h.Typeflag {
		case tar.TypeDir:
			s.dirs++
			if mode&0o002 != 0 && mode&0o1000 == 0 {
				a.observe(s, "fs.world-writable", p, fmt.Sprintf("directory mode %04o, no sticky bit", mode&0o7777))
			}
			continue
		case tar.TypeSymlink:
			s.files++
			if a.linksUsed < a.lim.Symlinks {
				s.symlinks[p] = h.Linkname
				a.linksUsed++
			}
			continue
		case tar.TypeReg, tar.TypeRegA, tar.TypeLink: //nolint:staticcheck // old layers use TypeRegA
		default:
			s.files++
			continue
		}
		s.files++
		s.size += h.Size
		if mode&0o4000 != 0 {
			a.observe(s, "fs.setuid", p, fmt.Sprintf("mode %04o, owner uid %d", mode&0o7777, h.Uid))
		}
		if mode&0o2000 != 0 {
			a.observe(s, "fs.setgid", p, fmt.Sprintf("mode %04o, group gid %d", mode&0o7777, h.Gid))
		}
		if mode&0o002 != 0 {
			a.observe(s, "fs.world-writable", p, fmt.Sprintf("file mode %04o", mode&0o7777))
		}
		if v, ok := xattr(h, "security.capability"); ok {
			a.observe(s, "fs.capabilities", p, "file capabilities "+decodeCaps([]byte(v)))
		}
		a.classify(s, p, h.Size)
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if err := a.content(s, p, h.Size, body); err != nil {
				return err
			}
		}
	}
}

func xattr(h *tar.Header, name string) (string, bool) {
	if v, ok := h.PAXRecords["SCHILY.xattr."+name]; ok {
		return v, true
	}
	v, ok := h.Xattrs[name] //nolint:staticcheck // older writers fill only Xattrs
	return v, ok
}

func (a *analyser) observe(s *layerScan, rule, p, evidence string) {
	if len(s.fileObs) >= a.lim.PerLayerObs*4 {
		s.dropped[rule]++
		return
	}
	s.fileObs = append(s.fileObs, fileObs{rule: rule, path: p, evidence: evidence})
}

// content reads a bounded part of a file: its first bytes are searched for
// secrets, an ELF file's headers are read, and its whole length is passed
// through the Go build information scanner.
func (a *analyser) content(s *layerScan, p string, size int64, r io.Reader) error {
	n, err := io.ReadFull(r, a.head[:min(int64(len(a.head)), size)])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return a.readErr(err)
	}
	head := a.head[:n]
	if isELF(head) {
		return a.binary(s, p, head, r)
	}
	hint, named := nameIsSecret(p)
	if size > a.lim.ScanFileMax && !named {
		return nil
	}
	if !textLike(head) && !named {
		return nil
	}
	hits := scanContent(head, a.lim.SecretReveal, 4)
	if packaged(p) {
		for i := range hits {
			// A NAME=value line in a file a package ships is most often
			// the package's own example (openssl.cnf has several). Shapes
			// specific to a credential keep their certainty.
			if hits[i].kind == genericKind {
				hits[i].certainty = Hint
				hits[i].unknown = "whether it is more than an example: the file is where packages put their own"
			}
		}
	}
	if len(hits) == 0 && named {
		hits = []secretHit{{
			kind: hint.kind, certainty: hint.certainty,
			masked:  fmt.Sprintf("%s, %s; no known secret format in the first %s", hint.kind, humanSize(size), humanSize(int64(n))),
			unknown: "what the file holds: only its name points at a credential",
		}}
	}
	for _, h := range hits {
		if len(s.secrets) >= a.lim.PerLayerObs {
			s.dropped["secret"]++
			break
		}
		s.secrets = append(s.secrets, secretObs{path: p, hit: h})
	}
	return nil
}

// packaged reports paths where distribution packages install their files.
func packaged(p string) bool {
	for _, pre := range []string{"/usr/", "/etc/ssl/", "/etc/pki/", "/lib/"} {
		if strings.HasPrefix(p, pre) && !strings.HasPrefix(p, "/usr/src/app/") && !strings.HasPrefix(p, "/usr/local/app/") {
			return true
		}
	}
	return false
}

func (a *analyser) readErr(err error) error {
	if a.ctx.Err() != nil {
		return a.ctx.Err()
	}
	return errors.New("the layer's tar stream broke off inside a file")
}

func textLike(b []byte) bool {
	for _, c := range b[:min(len(b), 4096)] {
		if c == 0 {
			return false
		}
	}
	return true
}

func (a *analyser) binary(s *layerScan, p string, head []byte, r io.Reader) error {
	buf := a.elf[:copy(a.elf, head)]
	if len(head) == len(a.head) {
		n, err := io.ReadFull(r, a.elf[len(buf):])
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return a.readErr(err)
		}
		buf = a.elf[:len(buf)+n]
	}
	f := parseELF(buf)
	g := newGoScanner(a.lim.GoWindow)
	g.feed(buf)
	if len(buf) == len(a.elf) {
		// The rest of the file streams past in chunks; only the window
		// after Go's marker, if there is one, is kept.
		chunk := a.head
		for !g.done {
			n, err := r.Read(chunk)
			g.feed(chunk[:n])
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return a.readErr(err)
			}
		}
	}
	g.parse(&f)
	if len(s.elfs) >= a.lim.PerLayerObs*4 {
		s.dropped["binary"]++
		return nil
	}
	s.elfs = append(s.elfs, elfObs{path: p, facts: f})
	return nil
}

// ---- path classification ------------------------------------------------------

type pathKind struct {
	kind    string
	cache   bool // a package-manager cache (else build junk)
	certain bool
	match   func(p, base string) (root string, ok bool)
}

func under(prefix string) func(p, _ string) (string, bool) {
	return func(p, _ string) (string, bool) {
		if strings.HasPrefix(p, prefix) {
			return strings.TrimSuffix(prefix, "/"), true
		}
		return "", false
	}
}

func containing(seg string) func(p, _ string) (string, bool) {
	return func(p, _ string) (string, bool) {
		if i := strings.Index(p, seg); i >= 0 {
			return p[:i+len(seg)-1], true
		}
		return "", false
	}
}

var pathKinds = []pathKind{
	{kind: "apt package lists", cache: true, certain: true, match: func(p, base string) (string, bool) {
		if strings.HasPrefix(p, "/var/lib/apt/lists/") && base != "lock" && !strings.Contains(p, "/partial/") {
			return "/var/lib/apt/lists", true
		}
		return "", false
	}},
	{kind: "apt package archives", cache: true, certain: true, match: under("/var/cache/apt/")},
	{kind: "apk package cache", cache: true, certain: true, match: under("/var/cache/apk/")},
	{kind: "yum package cache", cache: true, certain: true, match: under("/var/cache/yum/")},
	{kind: "dnf package cache", cache: true, certain: true, match: under("/var/cache/dnf/")},
	{kind: "pip download cache", cache: true, certain: true, match: containing("/.cache/pip/")},
	{kind: "npm cache", cache: true, certain: true, match: containing("/.npm/_cacache/")},
	{kind: "yarn cache", cache: true, certain: true, match: containing("/.cache/yarn/")},
	{kind: "Go build cache", cache: true, certain: true, match: containing("/.cache/go-build/")},
	{kind: "Go module download cache", cache: true, certain: true, match: containing("/pkg/mod/cache/download/")},
	{kind: "node_modules/.cache", cache: true, certain: true, match: containing("/node_modules/.cache/")},
	{kind: ".git directory", certain: true, match: containing("/.git/")},
	{kind: "editor and OS litter", match: func(p, base string) (string, bool) {
		if base == ".DS_Store" || base == "Thumbs.db" || strings.HasSuffix(base, ".swp") || (strings.HasSuffix(base, "~") && len(base) > 1) {
			return path.Dir(p), true
		}
		return "", false
	}},
	{kind: "object files outside the system library paths", match: func(p, base string) (string, bool) {
		if (strings.HasSuffix(base, ".o") || strings.HasSuffix(base, ".obj")) &&
			!strings.HasPrefix(p, "/usr/") && !strings.HasPrefix(p, "/lib") && !strings.HasPrefix(p, "/opt/") {
			return path.Dir(p), true
		}
		return "", false
	}},
	{kind: "files in /tmp", match: func(p, _ string) (string, bool) {
		switch {
		case strings.HasPrefix(p, "/tmp/"):
			return "/tmp", true
		case strings.HasPrefix(p, "/var/tmp/"):
			return "/var/tmp", true
		}
		return "", false
	}},
	{kind: "downloaded archives", match: func(p, base string) (string, bool) {
		if !(strings.HasPrefix(p, "/root/") || strings.HasPrefix(p, "/usr/src/") || strings.HasPrefix(p, "/build/") || strings.HasPrefix(p, "/src/")) {
			return "", false
		}
		for _, ext := range []string{".tar.gz", ".tgz", ".tar.xz", ".tar.bz2", ".tar.zst", ".zip"} {
			if strings.HasSuffix(base, ext) {
				return path.Dir(p), true
			}
		}
		return "", false
	}},
	{kind: "build and CI files", match: func(p, base string) (string, bool) {
		switch base {
		case "Dockerfile", "Containerfile", ".dockerignore", "docker-compose.yml", "docker-compose.yaml",
			"compose.yaml", "compose.yml", "Jenkinsfile", ".gitlab-ci.yml", ".travis.yml":
			return path.Dir(p), true
		}
		if strings.Contains(p, "/.github/workflows/") {
			return path.Dir(p), true
		}
		return "", false
	}},
}

func (a *analyser) classify(s *layerScan, p string, size int64) {
	base := path.Base(p)
	for _, k := range pathKinds {
		root, ok := k.match(p, base)
		if !ok {
			continue
		}
		g := s.aggs[k.kind+"|"+root]
		if g == nil {
			if len(s.aggs) >= a.lim.PerLayerObs {
				s.dropped["aggregate"]++
				return
			}
			g = &aggregate{kind: k.kind, root: root, example: p, certain: k.certain}
			s.aggs[k.kind+"|"+root] = g
		}
		g.files++
		g.bytes += size
		return
	}
}

// ---- capabilities -----------------------------------------------------------------

var capNames = []string{
	"chown", "dac_override", "dac_read_search", "fowner", "fsetid", "kill", "setgid", "setuid",
	"setpcap", "linux_immutable", "net_bind_service", "net_broadcast", "net_admin", "net_raw",
	"ipc_lock", "ipc_owner", "sys_module", "sys_rawio", "sys_chroot", "sys_ptrace", "sys_pacct",
	"sys_admin", "sys_boot", "sys_nice", "sys_resource", "sys_time", "sys_tty_config", "mknod",
	"lease", "audit_write", "audit_control", "setfcap", "mac_override", "mac_admin", "syslog",
	"wake_alarm", "block_suspend", "audit_read", "perfmon", "bpf", "checkpoint_restore",
}

// decodeCaps renders a security.capability value (struct vfs_cap_data).
func decodeCaps(v []byte) string {
	if len(v) < 12 {
		return "(unreadable value)"
	}
	magic := binary.LittleEndian.Uint32(v)
	perm := uint64(binary.LittleEndian.Uint32(v[4:]))
	if magic&0xff000000 != 0x01000000 && len(v) >= 20 {
		perm |= uint64(binary.LittleEndian.Uint32(v[12:])) << 32
	}
	var names []string
	for i, n := range capNames {
		if perm&(1<<uint(i)) != 0 {
			names = append(names, "cap_"+n)
		}
	}
	if len(names) == 0 {
		return "(none permitted)"
	}
	s := strings.Join(names, ",")
	if magic&1 != 0 {
		s += "+ep"
	} else {
		s += "+p"
	}
	return s
}
