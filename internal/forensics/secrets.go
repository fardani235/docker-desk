package forensics

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// secretHit is one secret seen in content, a name, a build argument or the
// config's environment.
type secretHit struct {
	kind      string // "private key", "AWS access key ID", …
	value     string // unmasked, bounded by Limits.SecretReveal
	masked    string
	certainty Certainty
	unknown   string
}

// contentPattern recognises a secret by the shape of its bytes. group is the
// submatch holding the value (0 = the whole match).
type contentPattern struct {
	kind      string
	re        *regexp.Regexp
	group     int
	certainty Certainty
	keep      int // characters of the value left visible in the mask
	// need lists literals of which a match must contain one. The regular
	// expression runs only where one occurs: finding literals is two orders
	// of magnitude faster, and most files contain none.
	need []string
	// lines runs the expression on the lines holding a need literal,
	// rather than on the whole block.
	lines bool
}

const unknownLive = "whether the value is a live credential, a test fixture or revoked"

// genericKind is the NAME=value pattern: a credential by its name only.
const genericKind = "password or token assignment"

var contentPatterns = []contentPattern{
	// The header only: the block, up to its END line, is taken in code
	// (a regular expression cannot bound a repetition that long).
	{kind: "private key", re: regexp.MustCompile(`-----BEGIN ((RSA|DSA|EC|OPENSSH|ENCRYPTED|PGP) )?PRIVATE KEY( BLOCK)?-----`), certainty: Certain},
	{kind: "AWS access key ID", re: regexp.MustCompile(`\b((?:AKIA|ASIA)[0-9A-Z]{16})\b`), group: 1, certainty: Certain, keep: 4},
	{kind: "AWS secret access key", re: regexp.MustCompile(`(?i)aws_secret_access_key\s*[=:]\s*["']?([A-Za-z0-9/+=]{40})`), group: 1, certainty: Certain},
	{kind: "GitHub token", re: regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`), group: 1, certainty: Certain, keep: 4},
	{kind: "Slack token", re: regexp.MustCompile(`\b(xox[abposr]-[A-Za-z0-9-]{10,200})\b`), group: 1, certainty: Certain, keep: 5},
	{kind: "Stripe live key", re: regexp.MustCompile(`\b((?:sk|rk)_live_[0-9A-Za-z]{20,200})\b`), group: 1, certainty: Certain, keep: 8},
	{kind: "Google API key", re: regexp.MustCompile(`\b(AIza[0-9A-Za-z_\-]{35})\b`), group: 1, certainty: Certain, keep: 4},
	{kind: "npm token", re: regexp.MustCompile(`\b(npm_[A-Za-z0-9]{36})\b`), group: 1, certainty: Certain, keep: 4},
	{kind: "npm auth token", re: regexp.MustCompile(`:_authToken\s*=\s*([^\s$][^\s]{7,})`), group: 1, certainty: Certain},
	{kind: "registry credentials", re: regexp.MustCompile(`"auth"\s*:\s*"([A-Za-z0-9+/=]{8,})"`), group: 1, certainty: Certain},
	{kind: "URL credentials", re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]{1,20}://[^:/\s@"']{1,64}:([^@/\s"']{4,128})@`), group: 1, certainty: Likely},
	{kind: genericKind, re: regexp.MustCompile(`(?im)^\s*(?:export\s+)?([A-Za-z0-9_.-]*(?:PASSWORD|PASSWD|PASSPHRASE|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY|CREDENTIALS?)[A-Za-z0-9_.-]*)\s*[=:]\s*["']?([^\s"'#]{6,256})`), group: 2, certainty: Likely},
}

// secretName matches variable names that conventionally hold credentials.
var secretName = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASSPHRASE|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY|ACCESS_?KEY|CREDENTIAL)`)

// placeholder reports values that are evidently not a credential: a
// reference to another variable, a path to a secrets file, an example.
func placeholder(v string) bool {
	l := strings.ToLower(v)
	switch {
	case strings.HasPrefix(v, "$"), strings.HasPrefix(v, "%"), strings.HasPrefix(v, "<"), strings.HasPrefix(v, "{{"),
		strings.HasPrefix(v, "/"), strings.HasPrefix(v, "./"):
		return true
	case strings.Contains(l, "changeme"), strings.Contains(l, "example"), strings.Contains(l, "placeholder"),
		strings.Contains(l, "xxxxxx"), strings.Contains(l, "your_"), strings.Contains(l, "your-"),
		l == "null", l == "none", l == "true", l == "false", l == "required", l == "optional":
		return true
	}
	return false
}

// nameHint is a file whose name alone says it holds a credential.
type nameHint struct {
	match     func(p, base string) bool
	kind      string
	certainty Certainty
}

var nameHints = []nameHint{
	{func(_, b string) bool {
		return b == "id_rsa" || b == "id_dsa" || b == "id_ecdsa" || b == "id_ed25519" || b == "id_ecdsa_sk" || b == "id_ed25519_sk"
	}, "SSH private key file", Likely},
	{func(p, b string) bool { return b == "credentials" && strings.Contains(p, "/.aws/") }, "AWS credentials file", Likely},
	{func(_, b string) bool { return b == ".git-credentials" }, "git credential store", Likely},
	{func(_, b string) bool { return b == ".netrc" || b == "_netrc" }, "netrc login file", Likely},
	{func(_, b string) bool { return b == ".pgpass" }, "PostgreSQL password file", Likely},
	{func(_, b string) bool { return b == ".htpasswd" }, "htpasswd file", Likely},
	{func(_, b string) bool {
		return b == ".env" || (strings.HasPrefix(b, ".env.") && !strings.Contains(b, "example") && !strings.Contains(b, "sample"))
	}, "dotenv file", Hint},
	{func(_, b string) bool {
		e := path.Ext(b)
		return e == ".p12" || e == ".pfx" || e == ".jks" || e == ".keystore"
	}, "key store", Hint},
	{func(_, b string) bool { return path.Ext(b) == ".key" }, "key file", Hint},
}

// nameIsSecret returns a hint for a path whose name suggests a credential.
func nameIsSecret(p string) (nameHint, bool) {
	base := path.Base(p)
	if strings.HasPrefix(p, "/usr/share/doc/") || strings.HasPrefix(p, "/usr/share/man/") {
		return nameHint{}, false
	}
	for _, h := range nameHints {
		if h.match(p, base) {
			return h, true
		}
	}
	return nameHint{}, false
}

// needs gives each pattern its literals, by kind.
var needs = map[string]struct {
	lits  []string
	lines bool
}{
	"private key":                  {lits: []string{"PRIVATE KEY"}},
	"AWS access key ID":            {lits: []string{"AKIA", "ASIA"}},
	"AWS secret access key":        {lits: []string{"aws_secret_access_key"}, lines: true},
	"GitHub token":                 {lits: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}},
	"Slack token":                  {lits: []string{"xox"}},
	"Stripe live key":              {lits: []string{"_live_"}},
	"Google API key":               {lits: []string{"AIza"}},
	"npm token":                    {lits: []string{"npm_"}},
	"npm auth token":               {lits: []string{":_authToken"}, lines: true},
	"registry credentials":         {lits: []string{`"auth"`}, lines: true},
	"URL credentials":              {lits: []string{"://"}, lines: true},
	"password or token assignment": {lits: []string{"password", "passwd", "passphrase", "secret", "token", "api_key", "apikey", "private_key", "privatekey", "access_key", "accesskey", "credential"}, lines: true},
}

func init() {
	for i := range contentPatterns {
		n, ok := needs[contentPatterns[i].kind]
		if !ok {
			panic("forensics: no literals for pattern " + contentPatterns[i].kind)
		}
		contentPatterns[i].need, contentPatterns[i].lines = n.lits, n.lines
	}
}

// lowerASCII lowers A–Z only, so that offsets into the result are offsets
// into the original.
func lowerASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// literal is one need literal, lower-cased, and the pattern it belongs to.
type literal struct {
	lit     []byte
	pattern int
}

// byFirst indexes every literal by its first byte, so that one pass over a
// block finds them all (a substring search per literal was most of the
// analysis's time).
var byFirst [256][]literal

func init() {
	for i, p := range contentPatterns {
		for _, n := range p.need {
			if len(n) < 3 {
				panic("forensics: a literal shorter than three bytes: " + n)
			}
			l := lowerASCII([]byte(n))
			byFirst[l[0]] = append(byFirst[l[0]], literal{lit: l, pattern: i})
		}
	}
}

// candidates finds, in one pass, which patterns have a literal in low (the
// block lowered), and for line patterns the lines holding one. A pattern
// whose literal is case-sensitive is matched case-insensitively here; its
// regular expression decides.
func candidates(low []byte) (whole []bool, lines [][][2]int) {
	whole = make([]bool, len(contentPatterns))
	lines = make([][][2]int, len(contentPatterns))
	lineStart, lineEnd := 0, -1
	for i := 0; i < len(low); i++ {
		c := low[i]
		if c == '\n' {
			lineStart = i + 1
			continue
		}
		cands := byFirst[c]
		if cands == nil {
			continue
		}
		for _, l := range cands {
			// Every literal is at least three bytes; most first-byte hits
			// fail on the second, without a call.
			if i+len(l.lit) > len(low) || low[i+1] != l.lit[1] || !bytes.Equal(low[i:i+len(l.lit)], l.lit) {
				continue
			}
			p := &contentPatterns[l.pattern]
			if !p.lines {
				whole[l.pattern] = true
				continue
			}
			if i > lineEnd {
				lineEnd = bytes.IndexByte(low[i:], '\n')
				if lineEnd < 0 {
					lineEnd = len(low)
				} else {
					lineEnd += i
				}
			}
			ls := lines[l.pattern]
			if len(ls) < 256 && (len(ls) == 0 || ls[len(ls)-1][0] != lineStart) {
				lines[l.pattern] = append(ls, [2]int{lineStart, lineEnd})
			}
		}
	}
	return whole, lines
}

// scanContent finds secrets in a bounded block of text. At most max hits.
func scanContent(b []byte, revealMax, max int) []secretHit {
	var out []secretHit
	seen := map[string]bool{}
	whole, lines := candidates(lowerASCII(b))
	for i := range contentPatterns {
		p := &contentPatterns[i]
		regions := lines[i]
		if whole[i] {
			regions = [][2]int{{0, len(b)}}
		}
		for _, r := range regions {
			if hits := p.match(b, r, seen, revealMax, max-len(out)); len(hits) > 0 {
				out = append(out, hits...)
				if len(out) >= max {
					return out
				}
			}
		}
	}
	return out
}

// match runs the pattern over b[r[0]:r[1]].
func (p *contentPattern) match(b []byte, r [2]int, seen map[string]bool, revealMax, max int) []secretHit {
	var out []secretHit
	region := b[r[0]:r[1]]
	for _, m := range p.re.FindAllSubmatchIndex(region, 8) {
		for k := range m {
			if m[k] >= 0 {
				m[k] += r[0]
			}
		}
		{
			lo, hi := m[2*p.group], m[2*p.group+1]
			if lo < 0 {
				continue
			}
			if p.kind == "private key" {
				hi = pemEnd(b, lo)
			}
			v := string(b[lo:hi])
			if seen[v] || (p.certainty != Certain && placeholder(v)) {
				continue
			}
			seen[v] = true
			h := secretHit{kind: p.kind, certainty: p.certainty, unknown: unknownLive}
			if p.kind == "private key" {
				first, _, _ := strings.Cut(v, "\n")
				h.masked = strings.TrimSpace(first) + " ******** (" + fmt.Sprintf("%d bytes", len(v)) + ")"
				if !strings.Contains(v, "-----END") {
					h.masked = strings.TrimSpace(first) + " ******** (block runs past the bytes read)"
				}
				if strings.Contains(first, "ENCRYPTED") {
					h.certainty, h.unknown = Likely, "whether the passphrase protecting the key is weak or known"
				}
			} else {
				h.masked = maskValue(v, p.keep)
				if p.group == 2 {
					name := string(b[m[2]:m[3]])
					h.masked = sanitize(name, 64) + "=" + h.masked
				}
			}
			h.value = boundReveal(v, revealMax)
			out = append(out, h)
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}

// pemEnd returns the end of the PEM block starting at lo: just past its
// END line, or the end of the bytes read when that is not among them.
func pemEnd(b []byte, lo int) int {
	limit := min(len(b), lo+16384)
	rest := b[lo:limit]
	i := strings.Index(string(rest), "-----END")
	if i < 0 {
		return limit
	}
	j := strings.Index(string(rest[i+8:]), "-----")
	if j < 0 {
		return limit
	}
	return lo + i + 8 + j + 5
}

// maskValue hides a value: keep characters of its known prefix stay, the
// rest becomes asterisks, and its length is said.
func maskValue(v string, keep int) string {
	n := utf8.RuneCountInString(v)
	if keep > n/2 {
		keep = 0
	}
	r := []rune(v)
	stars := n - keep
	if stars > 12 {
		stars = 12
	}
	return sanitize(string(r[:keep]), 16) + strings.Repeat("*", stars) + fmt.Sprintf(" (%d chars)", n)
}

func boundReveal(v string, max int) string {
	v = strings.Join(strings.Fields(v), " ")
	if len(v) > max {
		v = v[:max] + " …"
	}
	return sanitize(v, max+4)
}

// maskText replaces every secret-shaped substring of s with its mask. It is
// applied to build instructions, which are shown with findings.
func maskText(s string) string {
	for _, p := range contentPatterns {
		s = p.re.ReplaceAllStringFunc(s, func(m string) string {
			sub := p.re.FindStringSubmatchIndex(m)
			lo, hi := sub[2*p.group], sub[2*p.group+1]
			if lo < 0 || (p.certainty != Certain && placeholder(m[lo:hi])) {
				return m
			}
			return m[:lo] + strings.Repeat("*", 8) + m[hi:]
		})
	}
	return s
}

// sanitize makes text from the image safe to draw: control characters —
// an escape sequence in a file name would otherwise reach the terminal —
// become '?', invalid UTF-8 is replaced, and the length is bounded.
// bidiControl reports the characters that reorder the text around them
// (Trojan Source): a name holding one could display as another.
func bidiControl(r rune) bool {
	return (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') || r == '\u200e' || r == '\u200f' || r == '\u061c'
}

func sanitize(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\u2028' || r == '\u2029' || r == '\ufeff' || bidiControl(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
