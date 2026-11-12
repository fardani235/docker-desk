package plan

import (
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // arguments joined by |; "" means none
	}{
		{"sh", "sh"},
		{"bash -l", "bash|-l"},
		{`sh -c 'echo "a b"; exit'`, `sh|-c|echo "a b"; exit`},
		{`  psql   -U  "my user"  `, "psql|-U|my user"},
		{"a\tb", "a|b"},
		{`''`, ""},                 // an empty quoted argument is one argument
		{`echo ''`, "echo|"},       // …kept as such
		{`a\ b`, "a b"},            // escaped space outside quotes
		{`say \"hi\"`, `say|"hi"`}, // escaped quote outside quotes
		{`"a \"q\" \\ \n"`, `a "q" \ \n`},
		{`'it\s'`, `it\s`}, // no escapes inside single quotes
		{"", ""},
		{"   ", ""},
	} {
		got, err := SplitCommand(tc.in)
		if err != nil {
			t.Errorf("SplitCommand(%q): %v", tc.in, err)
			continue
		}
		if tc.in == `''` {
			if len(got) != 1 || got[0] != "" {
				t.Errorf("SplitCommand(%q) = %q, want one empty argument", tc.in, got)
			}
			continue
		}
		if j := strings.Join(got, "|"); j != tc.want {
			t.Errorf("SplitCommand(%q) = %q (%d args), want %q", tc.in, j, len(got), tc.want)
		}
	}
	for _, bad := range []string{`sh -c 'oops`, `"open`, `trailing\`, `'it\'s'`} {
		if _, err := SplitCommand(bad); err == nil {
			t.Errorf("SplitCommand(%q) must be refused", bad)
		}
	}
	if _, err := SplitCommand(`sh -c 'oops`); err == nil || !strings.Contains(err.Error(), "unterminated quote") {
		t.Errorf("an unterminated quote is named: %v", err)
	}
}
