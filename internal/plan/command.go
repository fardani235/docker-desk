package plan

import (
	"errors"
	"strings"
)

// SplitCommand turns a typed command line into an argument vector. It
// understands single and double quotes and the backslash; nothing is
// expanded or interpreted — there is no shell between the desk and the
// daemon.
//
//   - Inside single quotes every character is literal.
//   - Inside double quotes a backslash escapes only `"` and `\`.
//   - Outside quotes a backslash makes the next character literal (so `a\ b`
//     is one argument).
//
// Empty or blank input yields no arguments and no error: whether a command is
// required is the caller's decision (the shell form requires one; the create
// form's empty command means the image's own).
func SplitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	in := false
	quote := rune(0)
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			if quote == '"' && r != '"' && r != '\\' {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, in = true, true
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				args = append(args, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if escaped {
		return nil, errors.New("the command ends with a lone backslash")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if in {
		args = append(args, cur.String())
	}
	return args, nil
}
