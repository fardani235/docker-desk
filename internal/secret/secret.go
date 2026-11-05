// Package secret holds the one value type the desk uses for anything that
// must never be printed: a credential found in an image, a registry
// password, a bearer token, a credential helper's answer. Every formatting
// path — fmt verbs, JSON, text marshalling — yields the mask; only Reveal
// returns the value. See docs/DECISIONS.md, round eleven, U6.
package secret

import (
	"fmt"
	"io"
)

// Secret is a value that must never be printed except on purpose.
type Secret struct {
	v *string
}

// Mask is what every formatting of a Secret prints.
const Mask = "[masked]"

// New wraps a value.
func New(v string) Secret { return Secret{v: &v} }

// Present reports whether there is a value to reveal.
func (s Secret) Present() bool { return s.v != nil }

// Reveal returns the value. Callers use it and keep it nowhere else.
func (s Secret) Reveal() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

func (s Secret) String() string   { return Mask }
func (s Secret) GoString() string { return Mask }

// Format makes every fmt verb print the mask.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Mask) }

// MarshalJSON and MarshalText keep the value out of any encoding.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Mask + `"`), nil }
func (s Secret) MarshalText() ([]byte, error) { return []byte(Mask), nil }
