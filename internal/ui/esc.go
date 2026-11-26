package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

// The escaping rule's helpers (see the package documentation, "Text the
// desk did not choose"). Each takes a value as a daemon sent it and returns
// it as drawn text; none is applied to a string that is text already — a
// row's name, a plan's strings, another helper's result.

// esc is a daemon's string as drawn text: escape.Name, the one mapping
// the lists, the exports and the Files tree share.
func esc(s string) string { return escape.Name(s) }

// escID is the short form of an identifier the daemon chose, as text.
func escID(id string) string { return escape.Name(engine.ShortID(id)) }

// escJoin is each of a daemon's strings as text, joined by sep: escaped
// one by one, so that the separator stays the desk's.
func escJoin(items []string, sep string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = escape.Name(s)
	}
	return strings.Join(out, sep)
}

// escErr is an error's text when a daemon may have written it: its own
// words (engine.Error), or whatever the error says, as text. It is not for
// an error the plan composed, whose names are escaped already.
func escErr(err error) string {
	if err == nil {
		return ""
	}
	return escape.Name(plainError(err))
}

// escInput is a text input's own line as it is drawn. The line is the
// operator's keystrokes, echoed: the input itself drops every control
// character before it holds one, so what can remain is a character that
// draws nothing and reorders what is around it — and that one is shown.
// Nothing else is changed: this is not the escaping of a value drawn back
// (a backslash stays one), only the echo made honest.
func escInput(view string) string {
	clean := true
	for _, r := range view {
		if r >= 0x80 && escape.Reorders(r) {
			clean = false
			break
		}
	}
	if clean {
		return view
	}
	var b strings.Builder
	for _, r := range view {
		if r >= 0x80 && escape.Reorders(r) {
			b.WriteString(escape.Rune(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// typedWord is the word a typed confirmation asks for, as it is shown —
// and it is compared as it is shown: what the operator has to type is what
// the dialog lets them read. Every word a plan sets is a fixed verb or a
// tag the plan has checked, so this is the word itself; were one ever to
// carry a character that cannot be drawn, the dialog would ask for its
// escaped form rather than for something that cannot be read or typed.
func typedWord(p plan.Plan) string { return escape.Name(p.Word) }

// textJob is a job whose error is text by the time the job holds it. A job
// of the desk's own (an analysis, a comparison, a read) returns whatever
// stopped it, and that can be the daemon's refusal in the daemon's words:
// those are escaped here, once (jobErrText). Cancellation passes through
// untouched — it is recognised by identity, not by its text.
func textJob(fn jobs.Func) jobs.Func {
	return func(ctx context.Context, r *jobs.Run) error {
		err := fn(ctx, r)
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return textErr{err}
	}
}

// textErr is an error whose text has the daemon's words escaped.
type textErr struct{ err error }

func (e textErr) Error() string { return jobErrText(e.err) }
func (e textErr) Unwrap() error { return e.err }

// jobErrText is an error's text with the daemon's own message in it — the
// Msg of the engine.Error it is or wraps — escaped, and the rest as it is:
// what the desk wrote around the message is the desk's, and a name in it
// was escaped by whoever wrote it. An error that holds no such message is
// returned as it is; one whose text no longer contains the message was
// rewritten by its composer (escaped, or a value hidden) and is left alone.
func jobErrText(err error) string {
	full := err.Error()
	var e *engine.Error
	if !errors.As(err, &e) || e.Msg == "" {
		return full
	}
	escaped := escape.Name(e.Msg)
	if escaped == e.Msg {
		return full
	}
	// The last place the message stands as the daemon sent it. A place
	// where its escaped form stands is its composer's work and is passed
	// over: the escaped form of a message ending in a backslash begins
	// with the message's own bytes, and is not a raw copy of it.
	for end := len(full); end > 0; {
		i := strings.LastIndex(full[:end], e.Msg)
		if i < 0 {
			break
		}
		if !strings.HasPrefix(full[i:], escaped) {
			return full[:i] + escaped + full[i+len(e.Msg):]
		}
		end = i + len(e.Msg) - 1
	}
	return full
}

// jsonText is one line of a JSON document as drawn text. The Inspect tab
// shows the daemon's document, not a value out of it, and a document has
// its own escaping: a character that could act on the terminal or reorder
// the line — DEL, a C1 control, a format character — is written as JSON's
// own \uNNNN (a surrogate pair above the basic plane), which is the same
// document, and a byte that is no character as �, which is what any
// decoder makes of it. A backslash is left alone: it is already JSON's.
// (A control character below U+0020 cannot be in the line at all: a
// document carrying one unescaped is not JSON, and is not shown.)
func jsonText(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n <= 1:
			b.WriteString(`�`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || escape.Reorders(r):
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// isHexName reports that s is lower-case hex digits and nothing else: the
// form of a name the daemon made up.
func isHexName(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}
