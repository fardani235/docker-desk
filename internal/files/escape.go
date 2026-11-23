package files

import "github.com/fardani235/docker-desk/internal/escape"

// Escape renders a name — an archive's or the daemon's, both chosen by
// whoever built the image or runs the container — as text that cannot act
// on a terminal or forge a line of a listing (D8). It is escape.Name: see
// there for the mapping.
func Escape(s string) string { return escape.Name(s) }
