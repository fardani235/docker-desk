package escape

import (
	"testing"
	"unicode"
)

func TestNameIsInert(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain.txt", "plain.txt"},
		{"caf\u00e9/\u65e5\u672c", "caf\u00e9/\u65e5\u672c"},
		{"a\x1b[31mb\nc\xff", `a\x1b[31mb\nc\xff`},
		{`back\slash`, `back\\slash`},
		{"c1\u009b31m", `c1\u009b31m`},
		{"exe\u202egnp.txt", `exe\u202egnp.txt`},
		{"zero\u200bwidth\u200d soft\u00adhyphen", `zero\u200bwidth\u200d soft\u00adhyphen`},
		{"tag\U000e0041", `tag\U000e0041`},
		{"\ufeffbom \u2066iso\u2069 \u2028sep \u200fmark", `\ufeffbom \u2066iso\u2069 \u2028sep \u200fmark`},
	} {
		if got := Name(c.in); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// No rune at all comes out as a control or a format character.
	for r := rune(0); r <= unicode.MaxRune; r++ {
		for _, o := range Name("a" + string(r) + "b") {
			if unicode.IsControl(o) || Reorders(o) {
				t.Fatalf("%U came out of Name(%U)", o, r)
			}
		}
	}
}
