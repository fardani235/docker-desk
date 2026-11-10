package composefile

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
)

// Actual is one container of the project, as the desk saw it.
type Actual struct {
	Service   string
	Container string
	// Image is the reference the container was created from, as the
	// daemon reports it (may be an image ID when the tag has since moved
	// away from it or was removed).
	Image string
	// Normalized is Image in Docker's familiar form with a tag filled in,
	// or "" when Image is not a reference (an ID).
	Normalized string
	Created    time.Time
	// ConfigHash is the com.docker.compose.config-hash label: compose's own
	// fingerprint of the service configuration it created the container
	// from. Shown as evidence; never recomputed.
	ConfigHash string
	// OneOff marks a container left by `docker compose run`. Such
	// containers are not compared.
	OneOff bool
}

// Verdict is a row's outcome, in the order rows are listed: disagreements
// first.
type Verdict int

const (
	DeclaredOnly Verdict = iota
	Differs
	PresentOnly
	NotCompared
	Match
	verdictCount
)

func (v Verdict) String() string {
	switch v {
	case DeclaredOnly:
		return "declared only"
	case Differs:
		return "differs"
	case PresentOnly:
		return "present only"
	case NotCompared:
		return "not compared"
	case Match:
		return "match"
	}
	return "unknown"
}

// Row is one service.
type Row struct {
	Service string
	Verdict Verdict
	// Declared is the image value as written, "" when the file declares
	// none (or does not declare the service).
	Declared string
	// Images are the distinct references the service's containers run.
	Images []string
	// Containers are the service's containers, by name.
	Containers []string
	// ConfigHashes are the distinct config-hash labels of those containers.
	ConfigHashes []string
	Profiles     []string
	// Reason says why: for Differs, what differs (tag, digest, repository);
	// for NotCompared and PresentOnly, a sentence.
	Reason string
}

// Result is a comparison.
type Result struct {
	Rows   []Row
	Counts [verdictCount]int
	// OneOffs counts containers excluded because they were left by
	// `docker compose run`.
	OneOffs int
	// Newest is the newest creation time among compared containers, and
	// NewestName its container.
	Newest     time.Time
	NewestName string
	// DeclaredName is the file's top-level name:, when it has one.
	DeclaredName string
}

// Count returns the number of rows with verdict v.
func (r Result) Count(v Verdict) int {
	if v < 0 || v >= verdictCount {
		return 0
	}
	return r.Counts[v]
}

// Merge combines several files in the order compose read them: the union of
// service names, a service's image from the last file that declares one,
// profiles from the last file that lists them, and the last name: given.
func Merge(files ...File) File {
	out := File{Services: map[string]Service{}}
	for _, f := range files {
		if f.Name != "" {
			out.Name = f.Name
		}
		out.Includes = out.Includes || f.Includes
		for name, s := range f.Services {
			cur := out.Services[name]
			if s.Image != "" {
				cur.Image = s.Image
			}
			if len(s.Profiles) > 0 {
				cur.Profiles = s.Profiles
			}
			cur.Build = cur.Build || s.Build
			cur.Extends = cur.Extends || s.Extends
			out.Services[name] = cur
		}
	}
	return out
}

// IsTemplate reports whether a declared value is interpolated by compose:
// it contains a $ (${VAR}, ${VAR:-default}, $VAR). The desk does not
// interpolate, so such a value is never compared.
func IsTemplate(v string) bool { return strings.Contains(v, "$") }

var imageID = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

// Normalize returns an image reference in Docker's familiar form with
// ":latest" filled in when neither tag nor digest is given, or "" when it is
// not a reference (an image ID, or text that does not parse).
func Normalize(ref string) string {
	if ref == "" || imageID.MatchString(ref) || strings.ContainsAny(ref, " \t") {
		return ""
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ""
	}
	return reference.FamiliarString(reference.TagNameOnly(named))
}

// Compare sets what a file declares against the containers that run. One
// row per service: declared, present or both. A row is filled only from
// what both sides say; what cannot be compared says why.
func Compare(f File, actual []Actual) Result {
	res := Result{DeclaredName: f.Name}
	by := map[string][]Actual{}
	for _, a := range actual {
		if a.OneOff {
			res.OneOffs++
			continue
		}
		by[a.Service] = append(by[a.Service], a)
		if a.Created.After(res.Newest) || (a.Created.Equal(res.Newest) && res.NewestName == "") {
			res.Newest, res.NewestName = a.Created, a.Container
		}
	}
	names := map[string]bool{}
	for n := range f.Services {
		names[n] = true
	}
	for n := range by {
		names[n] = true
	}
	for name := range names {
		s, declared := f.Services[name]
		cs := by[name]
		row := Row{Service: name, Declared: s.Image, Profiles: s.Profiles}
		for _, c := range cs {
			row.Containers = append(row.Containers, c.Container)
			row.Images = appendUnique(row.Images, c.Image)
			if c.ConfigHash != "" {
				row.ConfigHashes = appendUnique(row.ConfigHashes, c.ConfigHash)
			}
		}
		sort.Strings(row.Containers)
		switch {
		case !declared && f.Includes:
			row.Verdict = NotCompared
			row.Reason = "the file does not declare it, but includes other files, which the desk does not read"
		case !declared:
			row.Verdict = PresentOnly
			row.Reason = "the file does not declare it"
		case len(cs) == 0 && len(s.Profiles) > 0:
			row.Verdict = NotCompared
			row.Reason = "declared with profiles (" + strings.Join(s.Profiles, ", ") + "); the desk cannot know if they were enabled"
		case len(cs) == 0:
			row.Verdict = DeclaredOnly
			row.Reason = "no container of the project carries this service"
		default:
			row.Verdict, row.Reason = compareImage(s, cs)
		}
		res.Counts[row.Verdict]++
		res.Rows = append(res.Rows, row)
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if a.Verdict != b.Verdict {
			return a.Verdict < b.Verdict
		}
		return a.Service < b.Service
	})
	return res
}

// compareImage compares the declared image with every container's.
func compareImage(s Service, cs []Actual) (Verdict, string) {
	switch {
	case s.Image == "" && s.Extends:
		return NotCompared, "no image declared here; extends may supply one from elsewhere"
	case s.Image == "" && s.Build:
		return NotCompared, "the service builds its image; none is declared to compare with"
	case s.Image == "":
		return NotCompared, "no image declared"
	case IsTemplate(s.Image):
		return NotCompared, "the image is a template (" + s.Image + "); the desk does not interpolate"
	}
	want := Normalize(s.Image)
	if want == "" {
		return NotCompared, "the declared image is not a reference the desk can normalise"
	}
	var why []string
	for _, c := range cs {
		got := c.Normalized
		if got == "" {
			got = Normalize(c.Image)
		}
		switch {
		case got == "":
			return NotCompared, c.Container + " runs an image known only by ID (" + short(c.Image) + "); its reference is gone"
		case got != want:
			why = appendUnique(why, difference(want, got))
		}
	}
	if len(why) > 0 {
		return Differs, strings.Join(why, ", ")
	}
	return Match, ""
}

// difference says what part of two normalised references differs.
func difference(want, got string) string {
	wn, err1 := reference.ParseNormalizedNamed(want)
	gn, err2 := reference.ParseNormalizedNamed(got)
	if err1 != nil || err2 != nil {
		return "reference"
	}
	if wn.Name() != gn.Name() {
		return "repository"
	}
	wt, _ := wn.(reference.Tagged)
	gt, _ := gn.(reference.Tagged)
	if wt != nil && gt != nil && wt.Tag() != gt.Tag() {
		return "tag"
	}
	return "digest"
}

func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// WrittenRelative states the file's write time against the newest
// container's creation as a fact: "written 4s before these were created",
// "written 3h after these were created". It never says the file is wrong.
func WrittenRelative(written, created time.Time) string {
	if created.IsZero() {
		return "no container to measure it against"
	}
	d := written.Sub(created)
	switch {
	case d < 0:
		return "written " + human(-d) + " before these were created"
	case d > 0:
		return "written " + human(d) + " after these were created"
	}
	return "written the same second these were created"
}

// FileIsNewer reports whether the file was written after the newest
// container was created.
func FileIsNewer(written, created time.Time) bool {
	return !created.IsZero() && written.After(created)
}

func human(d time.Duration) string {
	switch {
	case d < time.Second:
		return "under 1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
