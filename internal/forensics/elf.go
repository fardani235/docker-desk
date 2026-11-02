package forensics

import (
	"bytes"
	"encoding/binary"
	"runtime/debug"
	"strings"
)

// elfFacts is what the first block of an ELF file says about hardening. A
// field the block could not answer is left unknown rather than assumed.
type elfFacts struct {
	exec    bool // an executable, not a shared library
	pie     bool
	dynamic bool
	relro   bool
	bindNow bool
	// canaryKnown is set when the dynamic string table was within the bytes
	// read; canary then says whether it names the stack protector.
	canaryKnown bool
	canary      bool
	packed      bool
	// headersKnown is false when the program headers lay past the bytes read.
	headersKnown bool
	goVersion    string
	goMain       string
	goModules    []string // "path version"
	goModsKnown  bool
}

const (
	etExec      = 2
	etDyn       = 3
	ptLoad      = 1
	ptDynamic   = 2
	ptInterp    = 3
	ptGNURelro  = 0x6474e552
	dtNull      = 0
	dtStrtab    = 5
	dtStrsz     = 10
	dtBindNow   = 24
	dtFlags     = 30
	dtFlags1    = 0x6ffffffb
	dfBindNow   = 0x8
	df1Now      = 0x1
	df1PIE      = 0x08000000
	maxPhdrs    = 256
	maxDynEnts  = 4096
	upxMarker   = "UPX!"
	upxScanSize = 4096
)

var goMagic = []byte("\xff Go buildinf:")

func isELF(b []byte) bool { return len(b) >= 4 && string(b[:4]) == "\x7fELF" }

type phdr struct {
	typ                uint32
	off, vaddr, filesz uint64
}

// parseELF reads hardening facts from the leading bytes of an ELF file.
func parseELF(b []byte) elfFacts {
	var f elfFacts
	if len(b) < 52 || !isELF(b) {
		return f
	}
	is64 := b[4] == 2
	var bo binary.ByteOrder = binary.LittleEndian
	if b[5] == 2 {
		bo = binary.BigEndian
	}
	typ := bo.Uint16(b[16:])
	var phoff uint64
	var phentsize, phnum int
	if is64 {
		if len(b) < 64 {
			return f
		}
		phoff = bo.Uint64(b[32:])
		phentsize, phnum = int(bo.Uint16(b[54:])), int(bo.Uint16(b[56:]))
	} else {
		phoff = uint64(bo.Uint32(b[28:]))
		phentsize, phnum = int(bo.Uint16(b[42:])), int(bo.Uint16(b[44:]))
	}
	if phnum > maxPhdrs || phentsize < 32 {
		return f
	}
	end := phoff + uint64(phentsize*phnum)
	if end > uint64(len(b)) {
		return f // the program headers are past what was read
	}
	f.headersKnown = true
	var ph []phdr
	interp := false
	for i := 0; i < phnum; i++ {
		p := b[phoff+uint64(i*phentsize):]
		var h phdr
		h.typ = bo.Uint32(p)
		if is64 {
			h.off, h.vaddr, h.filesz = bo.Uint64(p[8:]), bo.Uint64(p[16:]), bo.Uint64(p[32:])
		} else {
			h.off, h.vaddr, h.filesz = uint64(bo.Uint32(p[4:])), uint64(bo.Uint32(p[8:])), uint64(bo.Uint32(p[16:]))
		}
		switch h.typ {
		case ptInterp:
			interp = true
		case ptDynamic:
			f.dynamic = true
		case ptGNURelro:
			f.relro = true
		}
		ph = append(ph, h)
	}
	toOff := func(vaddr uint64) (uint64, bool) {
		for _, h := range ph {
			if h.typ == ptLoad && vaddr >= h.vaddr && vaddr < h.vaddr+h.filesz {
				return h.off + (vaddr - h.vaddr), true
			}
		}
		return 0, false
	}
	var flags1 uint64
	var strtab, strsz uint64
	for _, h := range ph {
		if h.typ != ptDynamic || h.off+h.filesz > uint64(len(b)) {
			continue
		}
		dyn := b[h.off : h.off+h.filesz]
		ent := 16
		if !is64 {
			ent = 8
		}
		for i := 0; i+ent <= len(dyn) && i/ent < maxDynEnts; i += ent {
			var tag, val uint64
			if is64 {
				tag, val = bo.Uint64(dyn[i:]), bo.Uint64(dyn[i+8:])
			} else {
				tag, val = uint64(bo.Uint32(dyn[i:])), uint64(bo.Uint32(dyn[i+4:]))
			}
			switch tag {
			case dtNull:
				i = len(dyn)
			case dtBindNow:
				f.bindNow = true
			case dtFlags:
				if val&dfBindNow != 0 {
					f.bindNow = true
				}
			case dtFlags1:
				flags1 = val
				if val&df1Now != 0 {
					f.bindNow = true
				}
			case dtStrtab:
				strtab = val
			case dtStrsz:
				strsz = val
			}
		}
	}
	if strtab != 0 && strsz != 0 && strsz < 1<<24 {
		if off, ok := toOff(strtab); ok && off+strsz <= uint64(len(b)) {
			names := b[off : off+strsz]
			f.canaryKnown = true
			f.canary = bytes.Contains(names, []byte("__stack_chk_fail")) || bytes.Contains(names, []byte("__stack_chk_guard"))
		}
	}
	switch typ {
	case etExec:
		f.exec = true
	case etDyn:
		f.pie = interp || flags1&df1PIE != 0
		f.exec = f.pie
	}
	f.packed = bytes.Contains(b[:min(len(b), upxScanSize)], []byte(upxMarker))
	return f
}

// goScanner looks for Go's build information in a file streamed past it,
// keeping only a bounded window.
type goScanner struct {
	window int
	carry  []byte
	found  []byte // the bytes from the magic onward, once seen
	done   bool
}

func newGoScanner(window int) *goScanner { return &goScanner{window: window} }

// feed passes the next chunk of the file.
func (g *goScanner) feed(chunk []byte) {
	if g.done {
		return
	}
	if g.found != nil {
		need := g.window - len(g.found)
		g.found = append(g.found, chunk[:min(need, len(chunk))]...)
		if len(g.found) >= g.window {
			g.done = true
		}
		return
	}
	buf := append(g.carry, chunk...)
	if i := bytes.Index(buf, goMagic); i >= 0 {
		g.found = append([]byte(nil), buf[i:min(len(buf), i+g.window)]...)
		g.carry = nil
		if len(g.found) >= g.window {
			g.done = true
		}
		return
	}
	keep := len(goMagic) - 1
	if len(buf) > keep {
		buf = buf[len(buf)-keep:]
	}
	g.carry = append(g.carry[:0], buf...)
}

// parse decodes the build information, Go 1.18 and later (inline strings).
func (g *goScanner) parse(f *elfFacts) {
	b := g.found
	if len(b) < 32 {
		return
	}
	if b[15]&0x2 == 0 {
		// Before Go 1.18 the header held pointers into the data segment,
		// which a stream read in passing cannot follow.
		f.goVersion = "go (version recorded by pointer: before Go 1.18)"
		return
	}
	rest := b[32:]
	read := func() (string, bool) {
		n, k := binary.Uvarint(rest)
		if k <= 0 || n > uint64(len(rest)-k) {
			return "", false
		}
		s := string(rest[k : k+int(n)])
		rest = rest[k+int(n):]
		return s, true
	}
	vers, ok := read()
	if !ok || !strings.HasPrefix(vers, "go") {
		return
	}
	f.goVersion = sanitize(vers, 40)
	mod, ok := read()
	if !ok {
		return
	}
	if len(mod) >= 33 && mod[len(mod)-17] == '\n' {
		mod = mod[16 : len(mod)-16]
	} else {
		mod = ""
	}
	if mod == "" {
		return
	}
	bi, err := debug.ParseBuildInfo(mod)
	if err != nil {
		return
	}
	f.goModsKnown = true
	f.goMain = sanitize(bi.Main.Path, 200)
	for _, d := range bi.Deps {
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		f.goModules = append(f.goModules, sanitize(m.Path+" "+m.Version, 200))
	}
}
