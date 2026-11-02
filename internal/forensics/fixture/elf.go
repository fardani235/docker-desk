package fixture

import (
	"bytes"
	"encoding/binary"
)

// ELF describes a minimal 64-bit little-endian executable: just enough
// structure — header, program headers, a dynamic section and its string
// table — for the analyser's hardening checks, and optionally Go build
// information. It does not run.
type ELF struct {
	PIE       bool // ET_DYN with an interpreter, instead of ET_EXEC
	Dynamic   bool // PT_INTERP and PT_DYNAMIC
	RELRO     bool // PT_GNU_RELRO
	BindNow   bool // DT_FLAGS BIND_NOW: full RELRO together with RELRO
	Canary    bool // __stack_chk_fail among the dynamic symbol names
	UPX       bool // a UPX! marker in the first block
	GoVersion string
	// GoMod is the module information as runtime/debug prints it
	// ("path\t…\nmod\t…\ndep\t…\n").
	GoMod string
	// Pad adds that many bytes of filler after the structures.
	Pad int
}

const (
	ptLoad      = 1
	ptDynamic   = 2
	ptInterp    = 3
	ptGNURelro  = 0x6474e552
	dtNull      = 0
	dtStrtab    = 5
	dtStrsz     = 10
	dtFlags     = 30
	dfBindNow   = 0x8
	elfBase     = 0x400000
	ehdrSize    = 64
	phdrSize    = 56
	maxPhdrs    = 5
	headerBlock = ehdrSize + phdrSize*maxPhdrs
)

// Bytes builds the file.
func (e ELF) Bytes() []byte {
	le := binary.LittleEndian
	var body bytes.Buffer
	// Layout after the header block: interpreter, dynstr, dynamic, filler,
	// build info.
	off := func() uint64 { return uint64(headerBlock + body.Len()) }
	base := uint64(elfBase)
	if e.PIE {
		base = 0
	}

	var interpOff, interpLen uint64
	var dynOff, dynLen uint64
	if e.Dynamic || e.PIE {
		interpOff = off()
		interp := []byte("/lib64/ld-linux-x86-64.so.2\x00")
		body.Write(interp)
		interpLen = uint64(len(interp))

		strOff := off()
		names := "\x00libc.so.6\x00puts\x00"
		if e.Canary {
			names += "__stack_chk_fail\x00"
		}
		body.WriteString(names)
		for body.Len()%8 != 0 {
			body.WriteByte(0)
		}
		dynOff = off()
		put := func(tag, val uint64) {
			var b [16]byte
			le.PutUint64(b[:8], tag)
			le.PutUint64(b[8:], val)
			body.Write(b[:])
		}
		put(dtStrtab, base+strOff)
		put(dtStrsz, uint64(len(names)))
		if e.BindNow {
			put(dtFlags, dfBindNow)
		}
		put(dtNull, 0)
		dynLen = off() - dynOff
	}
	if e.UPX {
		body.WriteString("\x00\x00UPX!\x0d\x0a")
	}
	body.Write(make([]byte, e.Pad))
	if e.GoVersion != "" {
		for off()%16 != 0 {
			body.WriteByte(0)
		}
		body.WriteString("\xff Go buildinf:")
		body.WriteByte(8)            // pointer size
		body.WriteByte(0x2)          // flags: inline strings
		body.Write(make([]byte, 16)) // the header is 32 bytes
		writeVarString(&body, e.GoVersion)
		mod := ""
		if e.GoMod != "" {
			// The linker brackets module information with 16-byte markers.
			mod = "0123456789abcdef" + e.GoMod + "fedcba9876543210"
		}
		writeVarString(&body, mod)
	}

	var phdrs [][]uint64 // type, flags, offset, vaddr, filesz
	total := uint64(headerBlock + body.Len())
	phdrs = append(phdrs, []uint64{ptLoad, 5, 0, base, total})
	if interpLen > 0 {
		phdrs = append(phdrs, []uint64{ptInterp, 4, interpOff, base + interpOff, interpLen})
		phdrs = append(phdrs, []uint64{ptDynamic, 6, dynOff, base + dynOff, dynLen})
	}
	if e.RELRO {
		phdrs = append(phdrs, []uint64{ptGNURelro, 4, dynOff, base + dynOff, dynLen})
	}

	h := make([]byte, headerBlock)
	copy(h, "\x7fELF")
	h[4], h[5], h[6] = 2, 1, 1 // 64-bit, little-endian, version 1
	etype := uint16(2)         // ET_EXEC
	if e.PIE {
		etype = 3 // ET_DYN
	}
	le.PutUint16(h[16:], etype)
	le.PutUint16(h[18:], 62) // x86-64
	le.PutUint32(h[20:], 1)
	le.PutUint64(h[24:], base+uint64(headerBlock)) // entry
	le.PutUint64(h[32:], ehdrSize)                 // phoff
	le.PutUint16(h[52:], ehdrSize)
	le.PutUint16(h[54:], phdrSize)
	le.PutUint16(h[56:], uint16(len(phdrs)))
	for i, p := range phdrs {
		b := h[ehdrSize+i*phdrSize:]
		le.PutUint32(b[0:], uint32(p[0]))
		le.PutUint32(b[4:], uint32(p[1]))
		le.PutUint64(b[8:], p[2])
		le.PutUint64(b[16:], p[3])
		le.PutUint64(b[24:], p[3])
		le.PutUint64(b[32:], p[4])
		le.PutUint64(b[40:], p[4])
		le.PutUint64(b[48:], 0x1000)
	}
	return append(h, body.Bytes()...)
}

func writeVarString(b *bytes.Buffer, s string) {
	var n [binary.MaxVarintLen64]byte
	b.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
	b.WriteString(s)
}
