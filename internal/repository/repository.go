// Package repository is the repository browser's own knowledge: a
// repository's tags as the registry lists them, held in text order and
// capped; what each tag names, kept with its age; how a remote build joins
// the images already on the daemon; and the build-time batch. The wire is
// internal/registry. See docs/DECISIONS.md, round twelve (L1–L14).
package repository

import (
	"sort"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/registry"
)

// Cap is how many tags the browser holds for one repository. A repository
// with more says so; the rest are not fetched.
const Cap = 1000

// Less is natural text order: runs of digits compare by value, everything
// else byte by byte — 3.9 before 3.10 before 3.28.3. It is an order of
// names. It says nothing about which tag is newer.
func Less(a, b string) bool {
	if c := compareNatural(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

func compareNatural(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigit(ca) && isDigit(cb) {
			si := i
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			sj := j
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na := strings.TrimLeft(a[si:i], "0")
			nb := strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return sign(len(na) - len(nb))
			}
			if na != nb {
				return strings.Compare(na, nb)
			}
			continue
		}
		if ca != cb {
			return sign(int(ca) - int(cb))
		}
		i++
		j++
	}
	return sign((len(a) - i) - (len(b) - j))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Sort orders tags naturally, in place.
func Sort(tags []string) {
	sort.SliceStable(tags, func(i, j int) bool { return Less(tags[i], tags[j]) })
}

// Listing is what is held of one repository's tags.
type Listing struct {
	Repo registry.Ref
	// Tags are held in natural order, at most Cap.
	Tags []string
	// Next is the cursor of the page the registry linked to; "" when it
	// named none or the cap was reached.
	Next string
	// Capped: Cap tags are held and the registry has more.
	Capped bool
	// Pages fetched; Invalid names dropped; LinkRefused when the registry
	// named a next page elsewhere.
	Pages, Invalid int
	LinkRefused    bool
	// At is when the first page arrived.
	At time.Time

	seen map[string]bool
}

// NewListing starts a listing of repo.
func NewListing(repo registry.Ref) *Listing {
	return &Listing{Repo: repo, seen: map[string]bool{}}
}

// Add takes one page: its tags are merged (a repeat is ignored), sorted, and
// cut at Cap. What lies past the cap is never held.
func (l *Listing) Add(p registry.TagPage) {
	if l.Pages == 0 {
		l.At = p.At
	}
	l.Pages++
	l.Invalid += p.Invalid
	l.LinkRefused = l.LinkRefused || p.LinkRefused
	l.Next = p.Next
	for _, t := range p.Tags {
		if l.seen[t] {
			continue
		}
		if len(l.Tags) >= Cap {
			// The page went past the cap: the rest of it, and every page
			// after it, stay with the registry.
			l.Capped = true
			break
		}
		l.seen[t] = true
		l.Tags = append(l.Tags, t)
	}
	if len(l.Tags) >= Cap && l.Next != "" {
		l.Capped = true
	}
	if l.Capped {
		l.Next = ""
	}
	Sort(l.Tags)
}

// More reports whether another page can be asked for.
func (l *Listing) More() bool { return l.Next != "" && !l.Capped }
