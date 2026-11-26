package plan

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/distribution/reference"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// Tagging and untagging an image, from Images (openspec change
// tag-or-untag-image, D1–D6). Both are local: a tag is one POST
// /images/{id}/tag, an untag is the removal request of one reference among
// several. Neither contacts a registry and neither is ever forced.

// NormalizeTag validates the target of a tag as the operator typed it and
// returns exactly the reference that will be created, in the familiar form
// the image list shows: ":latest" is filled in when no tag was given. A
// digest is refused — a digest names content, it cannot be given to an
// image — and so is anything that is not a named reference.
func NormalizeTag(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("enter the new reference, e.g. acme/api:1.2")
	}
	if strings.IndexFunc(input, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return "", errors.New("an image reference cannot contain spaces")
	}
	named, err := reference.ParseNormalizedNamed(input)
	if err != nil {
		return "", fmt.Errorf("not a valid image reference: %v", err)
	}
	if _, ok := named.(reference.Digested); ok {
		return "", errors.New("a tag cannot name a digest: give a repository and a tag, e.g. acme/api:1.2")
	}
	return reference.FamiliarString(reference.TagNameOnly(named)), nil
}

// TagOwnerKind is who holds a tag target now.
type TagOwnerKind int

const (
	// TagFree: no image has the reference; tagging creates it.
	TagFree TagOwnerKind = iota
	// TagThisImage: the image being tagged already has it.
	TagThisImage
	// TagOtherImage: another image has it; tagging moves it.
	TagOtherImage
)

// TagOwner is the image a tag target names in a snapshot.
type TagOwner struct {
	Kind TagOwnerKind
	// ID is the full ID of the image holding the target ("" when free);
	// Short is its short form, for display.
	ID, Short string
	// Refs are that image's references, for display.
	Refs []string
}

// TagOwner looks a normalised tag target (NormalizeTag's result) up among
// the snapshot's images, for the image imageID that is about to be tagged.
// The daemon's references are compared in their normalised form, so
// "docker.io/library/alpine:3" and "alpine:3" are the same target.
func (s Snapshot) TagOwner(imageID, target string) TagOwner {
	var other *engine.Image
	for i := range s.Images {
		img := &s.Images[i]
		for _, r := range img.Refs() {
			if r != target {
				if n, err := normalizeRef(r); err != nil || n != target {
					continue
				}
			}
			if trimSHA(img.ID) == trimSHA(imageID) {
				return TagOwner{Kind: TagThisImage, ID: img.ID, Short: engine.ShortID(img.ID), Refs: img.Refs()}
			}
			if other == nil {
				other = img
			}
		}
	}
	if other != nil {
		return TagOwner{Kind: TagOtherImage, ID: other.ID, Short: engine.ShortID(other.ID), Refs: other.Refs()}
	}
	return TagOwner{Kind: TagFree}
}

// TagStep is what an OpTagImage step was confirmed on. The step's ID is the
// source image's ID.
type TagStep struct {
	// Target is the normalised reference that will be created.
	Target string
	// Owner is the full ID of the image the form showed as holding Target,
	// or "" when it showed that no image does.
	Owner string
}

// imageIDForm is an image ID as the daemon lists it. The source of a tag is
// sent in the request's path; only this form is ever put there.
var imageIDForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ImageTag plans giving the image imageID the reference the operator typed.
// snap is the image list the form was read against: the plan records the
// image the target named there, and Execute refuses to tag if the target
// names any other image by the time the request would be sent. The source
// is the image's ID, never one of its references, so a reference that moves
// meanwhile cannot redirect the new tag to another image.
//
// A target that already names this image is NotApplicable: there is nothing
// to send.
func ImageTag(snap Snapshot, imageID, input string) (Plan, error) {
	target, err := NormalizeTag(input)
	if err != nil {
		return Plan{}, err
	}
	img, ok := snap.image(imageID)
	if !ok {
		return Plan{}, na("that image no longer exists; nothing was sent")
	}
	if !imageIDForm.MatchString(img.ID) {
		return Plan{}, na("the daemon lists this image under an ID that is not a sha256 digest (%s): it cannot be tagged by ID; nothing was sent", escape.Name(img.ID))
	}
	short := shortID(img.ID)
	owner := snap.TagOwner(img.ID, target)
	if owner.Kind == TagThisImage {
		return Plan{}, na("%s already names this image (%s): there is nothing to tag. Nothing was sent.", target, short)
	}
	if owner.Kind == TagOtherImage && !imageIDForm.MatchString(owner.ID) {
		// The holder's ID is compared with the daemon's answer later, and
		// named in the job: only the daemon's own form is carried.
		return Plan{}, na("%s is held by an image the daemon lists under an ID that is not a sha256 digest (%s); nothing was sent", target, escape.Name(owner.ID))
	}
	p := Plan{
		Title:     "Tag image",
		JobTitle:  "tag image",
		JobTarget: target,
		Target:    Target{Kind: "image", Name: target, ID: short},
		Steps: []Step{{
			Op: OpTagImage, Kind: "image", ID: img.ID, Name: target, Detail: short, IDFrom: -1,
			Tag: &TagStep{Target: target, Owner: owner.ID},
		}},
		Tier: TierForm,
		Effects: []string{
			fmt.Sprintf("Image %s gains the local reference %s. Its data and its other references are not changed.", short, target),
			"Nothing is pulled, pushed or sent to a registry.",
		},
	}
	if owner.Kind == TagOtherImage {
		p.Warnings = []string{fmt.Sprintf("%s names image %s now: the tag moves to image %s. Image %s keeps its data and loses this name.", target, escape.Name(owner.Short), short, escape.Name(owner.Short))}
	}
	return p, nil
}

// executeTag sends an OpTagImage step: the target is inspected immediately
// before the request, and nothing is tagged if it names an image other than
// the one the form showed. The daemon moves a tag without a word; this is
// the only place that can notice.
func executeTag(ctx context.Context, eng engine.Engine, st Step, run *jobs.Run) error {
	if st.Tag == nil || st.Tag.Target == "" {
		return errors.New("internal error: a tag step without its target; nothing was sent")
	}
	if !imageIDForm.MatchString(st.ID) {
		return errors.New("internal error: a tag's source must be an image ID; nothing was sent")
	}
	target, err := NormalizeTag(st.Tag.Target)
	if err != nil || target != st.Tag.Target {
		return errors.New("internal error: a tag step whose target is not normalised; nothing was sent")
	}
	if st.Tag.Owner != "" && !imageIDForm.MatchString(st.Tag.Owner) {
		return errors.New("internal error: a tag step whose recorded holder is not an image ID; nothing was sent")
	}
	// Read-only is refused here, before the inspect: no request at all.
	if engine.IsReadOnly(eng) {
		return engine.ErrReadOnly
	}
	short := shortID(st.ID)
	shown := "no image"
	if st.Tag.Owner != "" {
		shown = "image " + shortID(st.Tag.Owner)
	}
	run.Status("checking " + target)
	now := ""
	d, err := eng.InspectImage(ctx, target)
	switch {
	case err == nil:
		if now = d.ID; now == "" {
			// An answer that names no image is not "no image has it".
			return fmt.Errorf("the daemon answered for %s without an image ID; nothing was tagged", target)
		}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case engine.IsNotFound(err):
		// No image has the reference: the tag creates it.
	default:
		return fmt.Errorf("%s could not be checked (%s); nothing was tagged", target, escape.Name(daemonMessage(err)))
	}
	switch {
	case now == "":
		if st.Tag.Owner != "" {
			run.Logf("  %s named %s when the form was read and names no image now: the tag is created, nothing moves", target, shown)
		}
	case trimSHA(now) == trimSHA(st.ID):
		run.Logf("  %s already names image %s: nothing needed doing, nothing was sent", target, short)
		run.Status("already tagged")
		return nil
	case trimSHA(now) != trimSHA(st.Tag.Owner):
		return fmt.Errorf("the target changed: %s named %s when the form was read and names image %s now; nothing was tagged",
			target, shown, shortID(now))
	default:
		run.Logf("  %s moves from image %s to image %s", target, shortID(st.Tag.Owner), short)
	}
	run.Status("tagging " + short)
	if err := eng.TagImage(ctx, st.ID, target); err != nil {
		return err
	}
	run.Logf("  image %s is now also %s (local only: no registry was contacted)", short, target)
	run.Status("tagged " + target)
	return nil
}

// ImageUntag plans removing one reference of an image while the image stays
// under at least one other. It is ImageRemove's untag and nothing else: on
// the image's last reference it refuses, because the daemon cannot remove
// the last reference without deleting the image, and that is `d remove`.
// The reference is pinned to the image, and KeepsData and KeepsTag have
// Execute refuse if it has become the last reference, or the last tag, by
// the time the request would be sent.
func ImageUntag(snap Snapshot, imageID, ref string) (Plan, error) {
	img, ok := snap.image(imageID)
	if !ok {
		return Plan{}, na("that image no longer exists; nothing was sent")
	}
	if ref == "" {
		return Plan{}, na("this image has no tag to remove; nothing was sent. (d removes an untagged image.)")
	}
	refs := img.Refs()
	// rest is every other reference; tags is those of them that are tags.
	// Only another tag counts as "the image stays": a daemon that lists a
	// digest reference among an image's tags drops it with the repository's
	// last tag, and the image with it.
	var rest, tags []string
	for _, r := range refs {
		if r == ref {
			continue
		}
		rest = append(rest, r)
		if !strings.Contains(r, "@") {
			tags = append(tags, r)
		}
	}
	if len(rest) == len(refs) {
		return Plan{}, na("the image no longer has the reference %s; nothing was sent", escape.Name(ref))
	}
	if strings.Contains(ref, "@") {
		return Plan{}, na("%s is a digest reference, not a tag: u removes tags. Nothing was sent. (d removes this reference.)", escape.Name(ref))
	}
	// Only a reference that reads back as itself is acted on: it goes into
	// the request's path and its tag is what the operator types, and both
	// come from the daemon's list.
	if n, err := NormalizeTag(ref); err != nil || n != ref {
		return Plan{}, na("%s is not a tag the desk can name exactly; nothing was sent", escape.Name(ref))
	}
	_, tag := engine.SplitRef(ref)
	if len(tags) == 0 {
		return Plan{}, na("%s is the image's only tag: removing it would delete the image. Nothing was sent — use d remove to delete it.", escape.Name(ref))
	}
	p, err := ImageRemove(snap, imageID, ref)
	if err != nil {
		return Plan{}, err
	}
	// ImageRemove built the untag (other references remain): one removal
	// request for the reference, pinned, size 0, promised to keep the data.
	if len(p.Steps) != 1 || !p.Steps[0].KeepsData || p.Steps[0].Pin == "" || p.Steps[0].ID != ref {
		return Plan{}, errors.New("internal error: an untag must be one pinned removal that keeps the image")
	}
	p.Title = "Untag image"
	p.JobTitle = "untag image"
	p.JobTarget = text(ref)
	p.Word = tag
	p.Steps[0].IDFrom = -1
	p.Steps[0].KeepsTag = true
	p.Effects = append(p.Effects,
		fmt.Sprintf("Immediately before the request, %s is checked to still name image %s and not to be its last reference; otherwise nothing is removed.", text(ref), shortID(img.ID)),
		"Nothing is sent to a registry: a tag pushed earlier stays there.",
	)
	return p, nil
}
