package plan

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
)

// Copying one image from one daemon of the session to another, from Images
// (openspec change copy-image-between-daemons, D1–D11). It is the one plan
// that names two daemons: the archive GET /images/get returns on the source
// is the body of POST /images/load on the target. No file, no registry, no
// retag; the source is only read.

// CopyDaemon is one end of a copy, as the confirmation names it. It is
// display data and an identity — never an engine: the engines are looked up
// from the session, by Key, when the job starts (D6).
type CopyDaemon struct {
	// Key is the member's canonical endpoint in the session; it survives
	// focus changes and reconnections.
	Key string
	// Name is how the session calls the daemon; Endpoint is its address as
	// the operator wrote it, and Context its Docker context.
	Name, Endpoint, Context string
	// LinkLabel is the "how is it reached" tag ("" for a local socket), and
	// Insecure is set when traffic to it can be read or the daemon
	// impersonated.
	LinkLabel string
	Insecure  bool
	// Remote is set when the daemon is on another machine (display only).
	Remote bool
}

// CopySource is the image to copy, as the source daemon described it when
// the action was asked for.
type CopySource struct {
	Daemon CopyDaemon
	// ImageID is the row's image; Ref the row's reference ("" for an
	// untagged row).
	ImageID, Ref string
	// Facts is the source's inspect of ImageID: the platform that will be
	// sent and the layer chain the result is compared with.
	Facts engine.ImageFacts
}

// CopyDestination is the target daemon and what it answered, before the
// dialog, for the reference the copy will carry (or, for an untagged copy,
// for the image's ID).
type CopyDestination struct {
	Daemon CopyDaemon
	// Held is set when the target has an image under that name; Facts is
	// then its inspect.
	Held  bool
	Facts engine.ImageFacts
	// Err is why the target could not be read ("" when it was). A plan
	// built on it is blocked.
	Err string
}

// ImageCopySpec is what a copy plan was confirmed on. It holds identifiers and
// display text only.
type ImageCopySpec struct {
	Source, Target CopyDaemon
	// ImageID is the source image's ID: the pin.
	ImageID string
	// Ref is the tag that travels with the archive, in its familiar form;
	// "" when the image arrives untagged.
	Ref string
	// Platform is the platform the source inspected the image as.
	Platform engine.Platform
	// TargetOwner is the full ID of the image the target held under Ref (or
	// under ImageID, for an untagged copy) when the dialog was built; ""
	// when it held none. The job refuses to stream if that has changed.
	TargetOwner string
}

// By is what the source is asked to save: the tag, or the image's ID.
func (c *ImageCopySpec) By() string {
	if c.Ref != "" {
		return c.Ref
	}
	return c.ImageID
}

// Notes the copy job's log ends with: what is and is not known (D1).
const (
	CopyCancelledNote = "cancelled: both streams were closed. Layers the target already accepted may remain in its store; no image and no moved tag is claimed"
	CopySourceNote    = "the source was only read: nothing on it was created, moved or removed"
)

// copyRef decides what a copy carries for the row's reference: a tag, in
// exactly the form the daemon listed it, travels; a digest reference names
// content, not a tag, and such a row is copied by ID and arrives untagged.
func copyRef(ref string) (tag string, err error) {
	if ref == "" || strings.Contains(ref, "@") {
		return "", nil
	}
	// Only a reference that reads back as itself is acted on: it goes into
	// two requests' paths and its tag is what the operator types.
	if n, nerr := NormalizeTag(ref); nerr != nil || n != ref {
		return "", fmt.Errorf("%s is not a tag the desk can name exactly; nothing was sent", escape.Name(ref))
	}
	return ref, nil
}

// CopyName is the name a copy of the row (imageID, ref) asks both daemons
// about: the tag, when the row has one that reads back as itself, and the
// image's ID otherwise. It is validated here, before the dialog's read of
// the target puts it into a request: only an image ID in the daemon's own
// form, or a normalised tag, is ever sent.
func CopyName(imageID, ref string) (string, error) {
	if !imageIDForm.MatchString(imageID) {
		return "", na("the daemon lists this image under an ID that is not a sha256 digest (%s): it cannot be pinned for a copy; nothing was sent", escape.Name(imageID))
	}
	tag, err := copyRef(ref)
	if err != nil {
		return "", na("%s", err.Error())
	}
	if tag != "" {
		return tag, nil
	}
	return imageID, nil
}

// platformPart is one component of a platform as the desk will pass it
// on: the source reports it, and it is sent to the target in the load's
// query. The engine's client encodes it; this keeps it to what a platform
// is made of, and short.
func platformPart(s string) bool {
	if len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// plainPlatform reports whether every part of p is a plain platform word.
func plainPlatform(p engine.Platform) bool {
	return platformPart(p.OS) && platformPart(p.Architecture) && platformPart(p.Variant)
}

// SameBuild compares two inspects of what should be one image in two
// stores, and names the first fact that differs ("" when none does). The
// image ID is deliberately not compared: a classic store names an image by
// its config's digest and a containerd store by its manifest's, so the same
// build has two IDs (D9). What is compared does not depend on the store:
// the platform, the ordered layer diff IDs, and the config's command,
// entrypoint, user, working directory, environment names, ports and labels.
func SameBuild(want, got engine.ImageFacts) string {
	short := func(ids []string, i int) string {
		if i < len(ids) {
			return escape.Name(engine.ShortID(ids[i]))
		}
		return "none"
	}
	if want.Platform() != got.Platform() {
		return fmt.Sprintf("the platform is %s, not %s", escape.Name(got.Platform().String()), escape.Name(want.Platform().String()))
	}
	for i := 0; i < max(len(want.DiffIDs), len(got.DiffIDs)); i++ {
		if i >= len(want.DiffIDs) || i >= len(got.DiffIDs) || want.DiffIDs[i] != got.DiffIDs[i] {
			return fmt.Sprintf("layer %d is %s, not %s (%d layers, the source has %d)",
				i+1, short(got.DiffIDs, i), short(want.DiffIDs, i), len(got.DiffIDs), len(want.DiffIDs))
		}
	}
	for _, c := range []struct {
		what string
		a, b []string
	}{
		{"command", want.Cmd, got.Cmd},
		{"entrypoint", want.Entrypoint, got.Entrypoint},
		{"user", []string{want.User}, []string{got.User}},
		{"working directory", []string{want.WorkingDir}, []string{got.WorkingDir}},
		{"set of environment variables", want.EnvNames, got.EnvNames},
		{"set of exposed ports", want.ExposedPorts, got.ExposedPorts},
		{"set of labels", labelPairs(want.Labels), labelPairs(got.Labels)},
	} {
		if len(c.a) != len(c.b) {
			return "the " + c.what + " differs"
		}
		for i := range c.a {
			if c.a[i] != c.b[i] {
				return "the " + c.what + " differs"
			}
		}
	}
	return ""
}

func labelPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// daemonWords names a daemon in a sentence: its session name and endpoint.
func daemonWords(d CopyDaemon) string {
	name, ep := escape.Name(d.Name), escape.Name(d.Endpoint)
	if name == "" || name == ep {
		return ep
	}
	return name + " (" + ep + ")"
}

// daemonShort names a daemon inside a confirmation's sentences, whose
// identity block already gives both endpoints in full: the session name, or
// the endpoint when it has none.
func daemonShort(d CopyDaemon) string {
	if d.Name != "" {
		return escape.Name(d.Name)
	}
	return escape.Name(d.Endpoint)
}

// CopyImage plans copying the image source.ImageID, as the row named it, to
// destination. snap is the source daemon's state when the action was asked
// for; source.Facts and destination are the two reads made for the dialog.
// Nothing here sends anything, and the plan carries no engine.
//
// The plan is typed: the word is the tag, or the short image ID for an
// image that arrives untagged. It is blocked when the target could not be
// read or already holds the same build under that name, and warns when the
// load will move a tag the target has given another image.
func CopyImage(snap Snapshot, source CopySource, destination CopyDestination) (Plan, error) {
	img, ok := snap.image(source.ImageID)
	if !ok {
		return Plan{}, na("that image no longer exists on %s; nothing was sent", daemonShort(source.Daemon))
	}
	if !imageIDForm.MatchString(img.ID) {
		return Plan{}, na("the daemon lists this image under an ID that is not a sha256 digest (%s): it cannot be pinned for a copy; nothing was sent", escape.Name(img.ID))
	}
	if source.Ref != "" {
		found := false
		for _, r := range img.Refs() {
			found = found || r == source.Ref
		}
		if !found {
			return Plan{}, na("%s is no longer a reference of image %s; nothing was sent", escape.Name(source.Ref), engine.ShortID(img.ID))
		}
	}
	ref, err := copyRef(source.Ref)
	if err != nil {
		return Plan{}, na("%s", err.Error())
	}
	if source.Facts.ID != img.ID {
		return Plan{}, na("the daemon answered for another image (%s) when asked about %s; nothing was sent",
			escape.Name(engine.ShortID(source.Facts.ID)), engine.ShortID(img.ID))
	}
	src, dst := source.Daemon, destination.Daemon
	if src.Key == "" || dst.Key == "" {
		return Plan{}, errors.New("internal error: a copy needs both daemons named; nothing was sent")
	}
	if src.Key == dst.Key {
		return Plan{}, na("a copy needs two different daemons: %s is the one the image is on; nothing was sent", daemonWords(src))
	}

	short := engine.ShortID(img.ID)
	plat := source.Facts.Platform()
	if !plainPlatform(plat) {
		return Plan{}, na("the daemon reports a platform for this image that the desk cannot name exactly (%s); nothing was sent", escape.Name(clipName(plat.String())))
	}
	platWords := escape.Name(plat.String())
	noPlatform := plat.OS == "" || plat.Architecture == ""
	if noPlatform {
		platWords = "the daemon's own (it reported none)"
	}
	spec := &ImageCopySpec{Source: src, Target: dst, ImageID: img.ID, Ref: ref, Platform: plat}
	name, word := ref, ""
	if ref != "" {
		_, word = engine.SplitRef(ref)
	} else {
		name, word = short, short
	}
	from, to := daemonShort(src), daemonShort(dst)

	p := Plan{
		Title:     "Copy image to another daemon",
		JobTitle:  "copy image",
		JobTarget: name,
		Target:    Target{Kind: "image", Name: name, ID: short},
		Steps:     []Step{{Op: OpCopyImage, Kind: "image", ID: spec.By(), Name: name, Detail: short, Pin: img.ID, IDFrom: -1}},
		Tier:      TierTyped,
		Word:      word,
		ImageCopy: spec,
	}
	p.Effects = append(p.Effects,
		fmt.Sprintf("Copies image %s (%s as the source lists it), platform %s, from %s to %s.", short, humanBytes(img.Size), platWords, from, to),
		"The archive streams directly from the source daemon into the target daemon, through this desk's memory only: no file is written on this machine and no registry is contacted.",
	)

	// What the target holds under the name the copy carries.
	switch {
	case destination.Err != "":
		p.Blockers = append(p.Blockers, fmt.Sprintf("%s could not be read (%s): what the copy would do there is not known. Nothing was sent.",
			to, escape.Name(destination.Err)))
	case !destination.Held && ref != "":
		p.Effects = append(p.Effects, fmt.Sprintf("%s does not exist on %s: the tag is created there, naming the copied image.", ref, to))
	case !destination.Held:
		p.Effects = append(p.Effects, fmt.Sprintf("The image arrives untagged on %s: this row has no tag to carry. (t tags it there afterwards.)", to))
	case !imageIDForm.MatchString(destination.Facts.ID):
		p.Blockers = append(p.Blockers, fmt.Sprintf("%s holds %s under an ID that is not a sha256 digest (%s): it cannot be checked again before the stream. Nothing was sent.",
			to, escape.Name(name), escape.Name(destination.Facts.ID)))
	default:
		held := engine.ShortID(destination.Facts.ID)
		spec.TargetOwner = destination.Facts.ID
		diff := SameBuild(source.Facts, destination.Facts)
		switch {
		case diff == "":
			p.Blockers = append(p.Blockers, fmt.Sprintf("Already present: %s holds this build as %s (image %s there: the same layers, platform and config). Nothing needs copying, and nothing was sent.",
				to, escape.Name(name), held))
		case ref == "":
			// The target answered for the image's ID with something else.
			p.Blockers = append(p.Blockers, fmt.Sprintf("%s answers for the ID %s with an image that is not this build (%s). Nothing was sent.", to, short, diff))
		default:
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s names image %s on %s now: the load moves the tag to the copied image. Image %s keeps its data there and loses this name. The desk cannot undo this.",
				ref, held, to, held))
		}
	}

	p.Effects = append(p.Effects,
		"Only this platform is sent: one archive, one image. The copy never retags and sends no other tag: an archive that names anything else is stopped before the target has it whole.",
		fmt.Sprintf("Immediately before the stream, %s is checked to still name image %s on the source, and the target to hold what is shown here; otherwise nothing is sent.", escape.Name(spec.By()), short),
		"Afterwards the image is inspected on the target: its layers, platform and config must be the source's. The two daemons may give the same build different IDs.",
		"The source is only read: nothing on it is created, moved or removed.",
		"It runs as one background job that holds both daemons: neither can be removed from the session until it ends. Cancelling closes both streams; layers the target already accepted may remain there, with no image and no moved tag.",
	)
	if noPlatform {
		p.Warnings = append(p.Warnings, "The source reported no platform for this image: the daemons choose the variant, and it cannot be compared afterwards.")
	}
	for _, d := range []struct {
		end  CopyDaemon
		what string
	}{{src, "read from"}, {dst, "written to"}} {
		if d.end.Insecure {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The connection to %s is not secured (%s): the image's contents are %s it where others on that network can read or alter them.",
				daemonShort(d.end), escape.Name(d.end.LinkLabel), d.what))
		}
	}
	return p, nil
}

// checkCopyPlan refuses a copy plan that is not exactly what CopyImage
// builds: every identifier that will be sent is validated again, here,
// where the requests are made.
func checkCopyPlan(p Plan) (*ImageCopySpec, error) {
	bad := func(what string) (*ImageCopySpec, error) {
		return nil, errors.New("internal error: " + what + "; nothing was sent")
	}
	c := p.ImageCopy
	switch {
	case c == nil:
		return bad("not a copy plan")
	case !p.Runnable():
		return bad("tried to execute a copy plan that is not runnable")
	case len(p.Steps) != 1 || p.Steps[0].Op != OpCopyImage:
		return bad("a copy plan has exactly one step")
	case !imageIDForm.MatchString(c.ImageID) || p.Steps[0].Pin != c.ImageID:
		return bad("a copy without the image it was confirmed for")
	case c.TargetOwner != "" && !imageIDForm.MatchString(c.TargetOwner):
		return bad("a copy whose recorded target image is not an image ID")
	case c.Source.Key == "" || c.Target.Key == "" || c.Source.Key == c.Target.Key:
		return bad("a copy needs two different daemons")
	case p.Steps[0].ID != c.By():
		return bad("a copy whose step does not name what was confirmed")
	case !plainPlatform(c.Platform):
		return bad("a copy whose platform is not a plain platform")
	case p.Tier != TierTyped || p.Word == "":
		return bad("a copy that was not confirmed by its typed word")
	}
	if c.Ref != "" {
		if n, err := NormalizeTag(c.Ref); err != nil || n != c.Ref {
			return bad("a copy whose tag is not normalised")
		}
	}
	return c, nil
}
