package forensics

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The declared side, read as the SBOM streams past. Every JSON blob of the
// archive goes through this scanner — which blob is the SBOM is known only
// from index.json, which may come last — so a document of any size is read
// in one pass with bounded memory: strings are cut at a bound while the
// rest of them is consumed, nesting is bounded, and only name, version,
// licence and purl of each package are kept, up to Limits.Declared of them.
// The count of every package is kept regardless.
//
// The fields read are SPDX 2.x's packages[] (name, versionInfo,
// licenseDeclared or licenseConcluded, the purl among externalRefs) and
// CycloneDX's components[] (name, version, purl, licenses, and nested
// components), at the top level of the document or under predicate, where
// an in-toto statement carries it.

const (
	jsonStrMax   = 2048 // bytes of one string kept; the rest is read and dropped
	jsonDepthMax = 64
)

// sbomScan is the result of scanning one JSON blob.
type sbomScan struct {
	format string // "" when the document is not an SBOM
	recs   []DeclaredPackage
	total  int
	err    string // why the document could not be read to its end
}

type jsonTok byte

const (
	tokEOF    jsonTok = 0
	tokObj    jsonTok = '{'
	tokObjEnd jsonTok = '}'
	tokArr    jsonTok = '['
	tokArrEnd jsonTok = ']'
	tokColon  jsonTok = ':'
	tokComma  jsonTok = ','
	tokString jsonTok = 's'
	tokLit    jsonTok = 'l' // a number, true, false or null
)

// jsonLexer reads tokens with bounded memory.
type jsonLexer struct {
	r     *bufio.Reader
	off   int64
	depth int
	str   []byte // the current string token, cut at jsonStrMax
	back  jsonTok
	has   bool // back holds a token pushed back
}

var errJSONDepth = errors.New("nested deeper than 64 levels")

func (l *jsonLexer) fail(what string) error {
	return fmt.Errorf("not valid JSON at byte %d (%s)", l.off, what)
}

func (l *jsonLexer) byte_() (byte, error) {
	c, err := l.r.ReadByte()
	if err == nil {
		l.off++
	}
	return c, err
}

// unread pushes a token back; it is delivered again by next, and the
// nesting it counted is uncounted until then.
func (l *jsonLexer) unread(t jsonTok) {
	l.back, l.has = t, true
	switch t {
	case tokObj, tokArr:
		l.depth--
	case tokObjEnd, tokArrEnd:
		l.depth++
	}
}

func (l *jsonLexer) next() (jsonTok, error) {
	t, err := l.lex()
	if err != nil {
		return tokEOF, err
	}
	switch t {
	case tokObj, tokArr:
		if l.depth++; l.depth > jsonDepthMax {
			return tokEOF, errJSONDepth
		}
	case tokObjEnd, tokArrEnd:
		l.depth--
	}
	return t, nil
}

func (l *jsonLexer) lex() (jsonTok, error) {
	if l.has {
		l.has = false
		return l.back, nil
	}
	for {
		c, err := l.byte_()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return tokEOF, nil
			}
			return tokEOF, err
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[', '}', ']', ':', ',':
			return jsonTok(c), nil
		case '"':
			return tokString, l.readString()
		}
		if c == '-' || (c >= '0' && c <= '9') || c == 't' || c == 'f' || c == 'n' {
			return tokLit, l.readLiteral()
		}
		return tokEOF, l.fail(fmt.Sprintf("unexpected byte 0x%02x", c))
	}
}

// peek returns the next token without taking it.
func (l *jsonLexer) peek() (jsonTok, error) {
	t, err := l.next()
	if err == nil {
		l.unread(t)
	}
	return t, err
}

func (l *jsonLexer) readLiteral() error {
	for n := 0; ; n++ {
		c, err := l.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !(c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			_ = l.r.UnreadByte()
			return nil
		}
		l.off++
		if n > 512 {
			return l.fail("a literal longer than 512 bytes")
		}
	}
}

func (l *jsonLexer) readString() error {
	l.str = l.str[:0]
	add := func(b ...byte) {
		if len(l.str)+len(b) <= jsonStrMax {
			l.str = append(l.str, b...)
		}
	}
	for {
		c, err := l.byte_()
		if err != nil {
			return l.eofIn(err, "a string")
		}
		switch {
		case c == '"':
			return nil
		case c < 0x20:
			return l.fail("a control character in a string")
		case c != '\\':
			add(c)
			continue
		}
		e, err := l.byte_()
		if err != nil {
			return l.eofIn(err, "a string")
		}
		switch e {
		case '"', '\\', '/':
			add(e)
		case 'b', 'f', 'n', 'r', 't':
			add(' ') // whitespace controls become spaces: nothing kept needs them
		case 'u':
			r, err := l.hex4()
			if err != nil {
				return err
			}
			if utf16.IsSurrogate(r) {
				// A pair is two escapes; a lone half becomes U+FFFD.
				if b, _ := l.r.Peek(2); len(b) == 2 && b[0] == '\\' && b[1] == 'u' {
					_, _ = l.byte_()
					_, _ = l.byte_()
					r2, err := l.hex4()
					if err != nil {
						return err
					}
					r = utf16.DecodeRune(r, r2)
				} else {
					r = utf8.RuneError
				}
			}
			var buf [4]byte
			add(buf[:utf8.EncodeRune(buf[:], r)]...)
		default:
			return l.fail("an unknown escape in a string")
		}
	}
}

func (l *jsonLexer) hex4() (rune, error) {
	var r rune
	for i := 0; i < 4; i++ {
		c, err := l.byte_()
		if err != nil {
			return 0, l.eofIn(err, "a string")
		}
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, l.fail("a bad \\u escape")
		}
	}
	return r, nil
}

func (l *jsonLexer) eofIn(err error, what string) error {
	if errors.Is(err, io.EOF) {
		return l.fail("the document ends inside " + what)
	}
	return err
}

// ---- the grammar the scanner needs ----------------------------------------------

func (l *jsonLexer) expect(want jsonTok) error {
	t, err := l.next()
	if err != nil {
		return err
	}
	if t != want {
		return l.fail(fmt.Sprintf("expected %q", rune(want)))
	}
	return nil
}

// skip reads one value whole and keeps nothing of it.
func (l *jsonLexer) skip() error {
	t, err := l.next()
	if err != nil {
		return err
	}
	switch t {
	case tokString, tokLit:
		return nil
	case tokObj, tokArr:
		depth := l.depth - 1
		for l.depth > depth {
			t, err := l.next()
			if err != nil {
				return err
			}
			if t == tokEOF {
				return l.fail("the document ends inside a value")
			}
		}
		return nil
	}
	return l.fail("expected a value")
}

// object reads an object whose '{' is next, calling field for each key;
// field must read the value.
func (l *jsonLexer) object(field func(key string) error) error {
	if err := l.expect(tokObj); err != nil {
		return err
	}
	t, err := l.next()
	if err != nil || t == tokObjEnd {
		return err
	}
	for {
		if t != tokString {
			return l.fail("expected a key")
		}
		key := string(l.str)
		if err := l.expect(tokColon); err != nil {
			return err
		}
		if err := field(key); err != nil {
			return err
		}
		if t, err = l.next(); err != nil {
			return err
		}
		switch t {
		case tokObjEnd:
			return nil
		case tokComma:
			if t, err = l.next(); err != nil {
				return err
			}
		default:
			return l.fail("expected , or }")
		}
	}
}

// array reads an array whose '[' is next, calling elem for each element;
// elem must read the element.
func (l *jsonLexer) array(elem func() error) error {
	if err := l.expect(tokArr); err != nil {
		return err
	}
	t, err := l.peek()
	if err != nil {
		return err
	}
	if t == tokArrEnd {
		_, err = l.next()
		return err
	}
	for {
		if err := elem(); err != nil {
			return err
		}
		t, err := l.next()
		if err != nil {
			return err
		}
		switch t {
		case tokArrEnd:
			return nil
		case tokComma:
		default:
			return l.fail("expected , or ]")
		}
	}
}

// strValue reads a value that should be a string; anything else is
// skipped and yields "".
func (l *jsonLexer) strValue() (string, error) {
	t, err := l.next()
	if err != nil {
		return "", err
	}
	if t == tokString {
		return string(l.str), nil
	}
	l.unread(t)
	return "", l.skip()
}

// objValue reads a value with read when it is an object, and skips it
// otherwise.
func (l *jsonLexer) objValue(read func() error) error {
	t, err := l.peek()
	if err != nil {
		return err
	}
	if t != tokObj {
		return l.skip()
	}
	return read()
}

// ---- SBOM ----------------------------------------------------------------------------

type sbomReader struct {
	l      *jsonLexer
	keep   int
	format string
	spdx   string
	bom    string
	spec   string
	recs   []DeclaredPackage
	total  int
}

// scanJSON reads one JSON document from r to its end and returns what it
// declares, if it is an SBOM. r is read to the end of the value only.
func scanJSON(r io.Reader, keep int) sbomScan {
	s := &sbomReader{l: &jsonLexer{r: bufio.NewReaderSize(r, 64<<10)}, keep: keep}
	err := s.document()
	out := sbomScan{total: s.total}
	switch {
	case s.spdx != "":
		out.format = sanitize(s.spdx, 20)
	case s.bom != "":
		out.format = sanitize(strings.TrimSpace(s.bom+" "+s.spec), 30)
	}
	if err != nil {
		// What was read before the fault is not offered as the list.
		out.err, out.total = err.Error(), 0
		return out
	}
	if out.format != "" {
		out.recs = s.recs
	}
	return out
}

func (s *sbomReader) document() error {
	t, err := s.l.peek()
	if err != nil || t != tokObj {
		return err // an array or a scalar: not an SBOM, nothing to say
	}
	return s.l.object(func(key string) error {
		if key == "predicate" {
			return s.l.objValue(func() error { return s.l.object(s.body) })
		}
		return s.body(key)
	})
}

// body reads a field of the SBOM document itself.
func (s *sbomReader) body(key string) error {
	var err error
	switch key {
	case "spdxVersion":
		s.spdx, err = s.l.strValue()
	case "bomFormat":
		s.bom, err = s.l.strValue()
	case "specVersion":
		s.spec, err = s.l.strValue()
	case "packages":
		return s.list(s.spdxPackage)
	case "components":
		return s.list(s.cdxComponent)
	default:
		return s.l.skip()
	}
	return err
}

// list reads an array of objects; anything else is skipped.
func (s *sbomReader) list(elem func() error) error {
	t, err := s.l.peek()
	if err != nil {
		return err
	}
	if t != tokArr {
		return s.l.skip()
	}
	return s.l.array(func() error { return s.l.objValue(elem) })
}

func (s *sbomReader) spdxPackage() error {
	var name, version, declared, concluded, purl string
	err := s.l.object(func(key string) error {
		var err error
		switch key {
		case "name":
			name, err = s.l.strValue()
		case "versionInfo":
			version, err = s.l.strValue()
		case "licenseDeclared":
			declared, err = s.l.strValue()
		case "licenseConcluded":
			concluded, err = s.l.strValue()
		case "externalRefs":
			return s.list(func() error {
				var typ, loc string
				err := s.l.object(func(k string) error {
					var err error
					switch k {
					case "referenceType":
						typ, err = s.l.strValue()
					case "referenceLocator":
						loc, err = s.l.strValue()
					default:
						return s.l.skip()
					}
					return err
				})
				if typ == "purl" && purl == "" {
					purl = loc
				}
				return err
			})
		default:
			return s.l.skip()
		}
		return err
	})
	if err != nil {
		return err
	}
	lic := spdxLicense(declared)
	if lic == "" {
		lic = spdxLicense(concluded)
	}
	s.emit(name, version, lic, purl)
	return nil
}

func spdxLicense(s string) string {
	switch strings.TrimSpace(s) {
	case "NOASSERTION", "NONE":
		return ""
	}
	return s
}

func (s *sbomReader) cdxComponent() error {
	var name, version, purl string
	var lics []string
	err := s.l.object(func(key string) error {
		var err error
		switch key {
		case "name":
			name, err = s.l.strValue()
		case "version":
			version, err = s.l.strValue()
		case "purl":
			purl, err = s.l.strValue()
		case "components":
			return s.list(s.cdxComponent)
		case "licenses":
			return s.list(func() error {
				return s.l.object(func(k string) error {
					switch k {
					case "expression":
						v, err := s.l.strValue()
						if v != "" && len(lics) < 8 {
							lics = append(lics, v)
						}
						return err
					case "license":
						var id, nm string
						err := s.l.objValue(func() error {
							return s.l.object(func(k string) error {
								var err error
								switch k {
								case "id":
									id, err = s.l.strValue()
								case "name":
									nm, err = s.l.strValue()
								default:
									return s.l.skip()
								}
								return err
							})
						})
						if id == "" {
							id = nm
						}
						if id != "" && len(lics) < 8 {
							lics = append(lics, id)
						}
						return err
					}
					return s.l.skip()
				})
			})
		default:
			return s.l.skip()
		}
		return err
	})
	if err != nil {
		return err
	}
	s.emit(name, version, strings.Join(lics, ", "), purl)
	return nil
}

func (s *sbomReader) emit(name, version, license, purl string) {
	s.total++
	if len(s.recs) >= s.keep {
		return
	}
	d := DeclaredPackage{Name: sanitize(name, 128), Version: sanitize(version, 128), License: sanitize(license, 256), PURL: sanitize(purl, 512)}
	if typ, pname, pver, arch, epoch, ok := parsePURL(purl); ok {
		d.Type, d.Arch = sanitize(typ, 24), sanitize(arch, 32)
		if pname != "" && IsOS(d.Type) {
			d.Name = sanitize(pname, 128) // the name the package database lists
		}
		if d.Version == "" {
			d.Version = sanitize(pver, 128)
		}
		if epoch != "" && epoch != "0" && !strings.Contains(d.Version, ":") {
			d.Version = sanitize(epoch, 12) + ":" + d.Version
		}
	}
	if d.Name == "" {
		d.Name = "(no name)"
	}
	s.recs = append(s.recs, d)
}
