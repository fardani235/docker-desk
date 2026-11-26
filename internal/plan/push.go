package plan

import (
	"context"
	"errors"
	"fmt"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// Push lines the job log ends with: what is and is not known (D6).
const (
	PushCancelledNote = "cancelled: the stream was closed; layers already uploaded may remain in the registry, and the tag may not have moved"
	PushUnchangedNote = "the local image and tag are unchanged"
)

// Push plans uploading one image's existing tag to the registry the tag
// names. The tag is the typed word, as in a pinned pull; the image ID is the
// pin, re-checked immediately before the request. The plan never retags,
// never pushes another or every tag, and builds nothing.
//
// The credential is not part of what is built here: the caller sets
// CredSource (for display) and RegistryAuth (called only when the job runs).
func Push(snap Snapshot, imageID, ref string) (Plan, error) {
	img, ok := snap.image(imageID)
	if !ok {
		return Plan{}, na("that image no longer exists; nothing was sent")
	}
	if ref == "" {
		return Plan{}, na("this image has no tag to push; nothing was sent")
	}
	found := false
	for _, r := range img.Refs() {
		found = found || r == ref
	}
	if !found {
		return Plan{}, na("%s is not a tag of image %s; nothing was sent", escape.Name(ref), shortID(img.ID))
	}
	_, tag, domain, err := engine.PushableRef(ref)
	if err != nil {
		return Plan{}, na("%s cannot be pushed: %v", escape.Name(ref), err)
	}
	short := shortID(img.ID)
	// What is sent is ref; shownRef is the same reference as text.
	shownRef := text(ref)
	p := Plan{
		Title:     "Push image",
		JobTitle:  "push image",
		JobTarget: shownRef,
		Target:    Target{Kind: "image", Name: shownRef, ID: short},
		Registry:  domain,
		Steps:     []Step{{Op: OpPushImage, Kind: "image", ID: ref, Name: shownRef, Detail: short, Pin: img.ID}},
		Tier:      TierTyped,
		Word:      tag,
		Effects: []string{
			fmt.Sprintf("Uploads %s to %s: the layers of image %s (%s on this daemon) that the registry does not already hold, then the manifest.", shownRef, text(domain), short, humanBytes(img.Size)),
			fmt.Sprintf("The remote tag %s is created, or moved to this build if it names another one. This cannot be undone from this desk.", text(tag)),
			"The local image and its tags are not changed. Nothing is built, nothing is retagged, and no other tag is pushed.",
			fmt.Sprintf("Immediately before the upload, %s is checked to still name image %s; if it does not, nothing is sent.", shownRef, short),
			"It runs as a background job. Cancelling closes the stream: layers already uploaded may remain in the registry, and the tag may not have moved.",
		},
	}
	switch others := len(img.Refs()) - 1; {
	case others == 1:
		p.Effects = append(p.Effects, fmt.Sprintf("The image's 1 other tag stays local: only %s is pushed.", shownRef))
	case others > 1:
		p.Effects = append(p.Effects, fmt.Sprintf("The image's %d other tags stay local: only %s is pushed.", others, shownRef))
	}
	return p, nil
}

// isPushPlan reports whether p is a push, and refuses a malformed one: a
// push is exactly one step, never mixed with other requests.
func isPushPlan(p Plan) (bool, error) {
	n := 0
	for _, st := range p.Steps {
		if st.Op == OpPushImage {
			n++
		}
	}
	switch {
	case n == 0:
		return false, nil
	case n == 1 && len(p.Steps) == 1:
		return true, nil
	}
	return true, errors.New("internal error: a push plan has exactly one step")
}

// executePush runs a push plan: the pin first, then the credential, then
// the one request. A moved tag, a missing image or a credential that cannot
// be read stops it with nothing sent. Only the daemon's own success report
// is taken to mean the remote tag was written.
func executePush(ctx context.Context, eng engine.Engine, p Plan, run *jobs.Run) error {
	st := p.Steps[0]
	ref := escape.Name(st.ID)
	notSent := func(status, msg string) error {
		run.Logf("✗ before the push: %s; nothing was sent", msg)
		run.Logf("%s", PushUnchangedNote)
		run.Status("not started: " + status)
		return fmt.Errorf("%s; nothing was sent", msg)
	}

	// Read-only refuses a push before anything, the credential included:
	// no helper is run for a request that will not be sent.
	if engine.IsReadOnly(eng) {
		run.Logf("✗ %s", engine.ErrReadOnly.Error())
		return engine.ErrReadOnly
	}
	// The credential first: a helper can take seconds (a keychain prompt,
	// its timeout), and that time must not sit between the pin and the
	// request. Reading it sends nothing.
	auth := ""
	var err error
	if p.RegistryAuth != nil {
		run.Status("reading the credential (" + credLabel(p.CredSource) + ")")
		auth, err = p.RegistryAuth(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				run.Logf("cancelled before the push: nothing was sent")
				return err
			}
			// The resolver's errors name how a helper failed, never what
			// it said; escape it all the same.
			return notSent("the credential could not be read", "the credential could not be read: "+escape.Name(err.Error()))
		}
	}

	run.Status("checking " + ref)
	if st.Pin == "" {
		return notSent("no pin", "internal error: a push without the image it was confirmed for")
	}
	d, err := eng.InspectImage(ctx, st.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			run.Logf("cancelled before the push: nothing was sent")
			return err
		}
		if engine.IsNotFound(err) {
			return notSent("the image is gone", ref+" no longer names an image on this daemon")
		}
		return notSent("the image could not be checked", ref+" could not be checked: "+escape.Name(daemonMessage(err)))
	}
	if trimSHA(d.ID) != trimSHA(st.Pin) {
		return notSent("the tag moved", fmt.Sprintf("the tag moved: %s now names %s, not the build %s that was shown", ref, shortID(d.ID), shortID(st.Pin)))
	}

	run.Logf("pushing %s (image %s) to %s, credential: %s", ref, shortID(st.Pin), escape.Name(p.Registry), credLabel(p.CredSource))
	run.Status("pushing " + ref)
	res, err := eng.PushImage(ctx, st.ID, auth, func(pr engine.PushProgress) {
		status := escape.Name(pr.Status)
		if pr.Layer != "" {
			status = escape.Name(pr.Layer) + ": " + status
		}
		if pr.Layers > 0 {
			status = fmt.Sprintf("%d/%d layers · %s", pr.Done, pr.Layers, status)
		}
		run.Status(status)
		if pr.Total > 0 {
			run.Fraction(float64(pr.Current) / float64(pr.Total))
		}
	})
	switch {
	case errors.Is(err, context.Canceled):
		run.Logf("%s", PushCancelledNote)
		run.Logf("%s", PushUnchangedNote)
		run.Status("cancelled: the remote tag may not have moved")
		return err
	case err != nil:
		msg := escape.Name(daemonMessage(err))
		run.Logf("✗ push image %s — %s", ref, msg)
		if engine.IsUnavailable(err) {
			run.Logf("the connection broke during the push: layers already uploaded may remain, and whether the remote tag moved is not known")
		}
		run.Logf("%s", PushUnchangedNote)
		run.Status("failed: nothing local changed")
		return errors.New(msg)
	}
	run.Logf("✓ push image %s done", ref)
	run.Logf("  digest %s", text(res.Digest))
	run.Logf("  %s uploaded, %s already in the registry", plural(res.Pushed, "layer", "layers"), plural(res.Exists, "layer", "layers"))
	run.Logf("  the daemon reports that %s now names this digest in %s", escape.Name(res.Tag), escape.Name(p.Registry))
	run.Logf("%s", PushUnchangedNote)
	run.Status("pushed " + shortID(res.Digest))
	run.Fraction(1)
	return nil
}

// credLabel is a credential source as the operator reads it.
func credLabel(source string) string {
	if source == "" {
		return "none found"
	}
	return escape.Name(source)
}

// CredLabel is credLabel for the dialog.
func CredLabel(source string) string { return credLabel(source) }
