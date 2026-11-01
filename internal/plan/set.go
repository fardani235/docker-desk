package plan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Member is one object of a set the operator selected in the list: how the
// planner finds it, and how the list showed it — so that an object that
// cannot be included is still named, even when it no longer exists.
type Member struct {
	// ID is what the single-object constructor takes: a container ID, an
	// image ID, a volume name, a network ID.
	ID string
	// Ref is the image reference of an image row; empty for an untagged
	// image and for every other kind.
	Ref string
	// Name is the row's name as the operator saw it.
	Name string
}

// Skip is a selected object a set plan leaves out, and why. Nothing is sent
// for it.
type Skip struct {
	Name   string
	Reason string
}

// SetCapable reports whether an operation can be applied to a selection.
// Connect and disconnect pair two objects and are single-object only, as is
// everything that creates something.
func SetCapable(op Op) bool {
	_, ok := setWords[op]
	return ok
}

type setWording struct {
	noun, nouns string
	outcome     string // "acted on" in the effects: "started", "deleted"…
	effects     []string
}

var setWords = map[Op]setWording{
	OpStart: {"container", "containers", "started", []string{
		"Each container listed below is started with its existing configuration.",
	}},
	OpStop: {"container", "containers", "stopped", []string{
		"Each container listed below is sent its stop signal (SIGTERM unless configured otherwise).",
		"One that has not exited after its grace period (10 s by default) is killed by the daemon.",
	}},
	OpRestart: {"container", "containers", "restarted", []string{
		"Each container listed below is stopped (signal, then killed after the grace period) and started again; one that is not running is just started.",
	}},
	OpPause: {"container", "containers", "paused", []string{
		"Every process in each container listed below is frozen. Nothing is lost; unpause resumes them.",
	}},
	OpUnpause: {"container", "containers", "unpaused", []string{
		"Every process in each container listed below resumes.",
	}},
	OpKill: {"container", "containers", "killed", []string{
		"The main process of each container listed below is sent SIGKILL immediately.",
	}},
	OpRemoveContainer: {"container", "containers", "deleted", []string{
		"Each container listed below, and its writable layer, is permanently deleted.",
		"Their volumes are kept.",
	}},
	OpRemoveImage: {"image", "images", "deleted", []string{
		"Each image listed below is deleted. Layers it does not share with other images are freed; untagged parent layers nothing else uses go with it.",
	}},
	OpRemoveVolume: {"volume", "volumes", "deleted", []string{
		"Each volume listed below, and all data in it, is permanently deleted.",
	}},
	OpRemoveNetwork: {"network", "networks", "deleted", []string{
		"Each network listed below is deleted.",
	}},
}

// Set plans one operation on every object of a selection.
//
// Each member is planned by the same constructor a single object would be,
// from the same snapshot. A member that constructor finds not applicable, or
// would refuse (in use, running, predefined), is not an error for the set:
// it is left out and named in Skipped with that reason, and the rest is
// planned. Nothing is dropped without being named, and nothing is included
// that the single-object rules would not allow.
//
// The confirmation is always typed, and the word carries the count of
// requests ("remove 7"): one keystroke must not be able to consent to many
// objects, and the number is what the operator has to have read.
func Set(op Op, snap Snapshot, members []Member) (Plan, error) {
	w, ok := setWords[op]
	if !ok {
		return Plan{}, fmt.Errorf("plan: %v cannot be applied to a selection", op)
	}
	p := Plan{Listed: true, Tier: TierTyped}
	included := 0
	skip := func(name, reason string) { p.Skipped = append(p.Skipped, Skip{Name: name, Reason: reason}) }
	add := func(sp Plan) {
		included++
		p.Steps = append(p.Steps, sp.Steps...)
		for _, warn := range sp.Warnings {
			p.Warnings = appendUnique(p.Warnings, warn)
		}
	}

	seen := map[string]bool{}
	var unique []Member
	for _, mb := range members {
		key := mb.ID + "|" + mb.Ref
		if !seen[key] {
			seen[key] = true
			unique = append(unique, mb)
		}
	}

	if op == OpRemoveImage {
		if err := imageSet(snap, unique, add, skip); err != nil {
			return Plan{}, err
		}
	} else {
		for _, mb := range unique {
			sp, err := single(op, snap, mb)
			if reason, left := excluded(sp, err); left {
				skip(memberName(op, mb), reason)
				continue
			} else if err != nil {
				return Plan{}, err
			}
			add(sp)
		}
	}

	for _, st := range p.Steps {
		p.TotalSize += st.Size
	}
	verb := op.String()
	p.Title = strings.ToUpper(verb[:1]) + verb[1:] + " " + w.nouns
	p.JobTitle = verb
	p.Target = Target{Kind: "selected", Name: plural(included, w.noun, w.nouns)}
	if len(p.Skipped) > 0 {
		p.Target.Name += fmt.Sprintf(" · %d not included", len(p.Skipped))
	}
	p.JobTarget = plural(included, w.noun, w.nouns)
	if len(p.Steps) == 0 {
		p.Nothing = fmt.Sprintf("Nothing to do: none of the %s selected can be %s.", plural(len(p.Skipped), w.noun, w.nouns), w.outcome)
		return p, nil
	}
	p.Word = fmt.Sprintf("%s %d", verb, len(p.Steps))
	p.Effects = append([]string{}, w.effects...)
	var untagOnly, wholeImage bool
	for _, st := range p.Steps {
		untagOnly = untagOnly || strings.HasPrefix(st.Detail, detailUntagOnly)
		wholeImage = wholeImage || strings.HasPrefix(st.Detail, detailUntag)
	}
	if untagOnly {
		p.Effects = append(p.Effects, `A row marked "untag only" removes just that reference: the image stays under its other references.`)
	}
	if wholeImage {
		p.Effects = append(p.Effects, "Where every reference of an image is selected, they are removed one by one and the last removal deletes the image.")
	}
	p.Effects = append(p.Effects,
		"One request each, in this order, never forced. If one fails the rest still run, and the job reports how many failed.")
	if len(p.Skipped) > 0 {
		p.Effects = append(p.Effects, "The selected objects under \"Not included\" are left alone: nothing is sent for them.")
	}
	return p, nil
}

// single plans one member with the single-object constructor.
func single(op Op, snap Snapshot, mb Member) (Plan, error) {
	switch op {
	case OpRemoveImage:
		return ImageRemove(snap, mb.ID, mb.Ref)
	case OpRemoveVolume:
		return VolumeRemove(snap, mb.ID)
	case OpRemoveNetwork:
		return NetworkRemove(snap, mb.ID)
	}
	return Container(op, snap, mb.ID)
}

// excluded reports whether a member's single-object plan leaves it out of
// the set, and the reason: not applicable, or refused.
func excluded(p Plan, err error) (string, bool) {
	var notApp *NotApplicable
	if errors.As(err, &notApp) {
		return notApp.Reason, true
	}
	if err == nil && len(p.Blockers) > 0 {
		return p.Blockers[0], true
	}
	return "", false
}

func memberName(op Op, mb Member) string {
	if op == OpRemoveVolume {
		return displayVolume(engine.Volume{Name: mb.Name})
	}
	return mb.Name
}

// imageSet plans image rows. Rows are planned one by one, except when the
// selection holds every reference of an image: then the operator asked for
// the image to go, and planning each reference alone would promise that the
// image stays (each would be an untag that "keeps the data") — the last one
// would then rightly be refused at execution. Such an image is planned like
// a prune plans it: one untag per reference, the last of which deletes it,
// and only if nothing uses it.
func imageSet(snap Snapshot, members []Member, add func(Plan), skip func(name, reason string)) error {
	byImage := map[string][]Member{}
	var order []string
	for _, mb := range members {
		if _, ok := byImage[mb.ID]; !ok {
			order = append(order, mb.ID)
		}
		byImage[mb.ID] = append(byImage[mb.ID], mb)
	}
	for _, id := range order {
		group := byImage[id]
		var valid []Member
		for _, mb := range group {
			_, err := ImageRemove(snap, mb.ID, mb.Ref)
			var notApp *NotApplicable
			if errors.As(err, &notApp) {
				skip(mb.Name, notApp.Reason)
				continue
			} else if err != nil {
				return err
			}
			valid = append(valid, mb)
		}
		img, ok := snap.image(id)
		refs := img.Refs()
		if !ok || len(refs) < 2 || len(valid) < len(refs) {
			for _, mb := range valid {
				sp, err := ImageRemove(snap, mb.ID, mb.Ref)
				if err != nil {
					return err
				}
				if len(sp.Blockers) > 0 {
					skip(mb.Name, sp.Blockers[0])
					continue
				}
				if sp.Steps[0].KeepsData {
					sp.Steps[0].Detail = detailUntagOnly + sp.Steps[0].Detail
				}
				add(sp)
			}
			continue
		}
		// Every reference of this image is selected: delete the image.
		if users := snap.ImageUsers(img); len(users) > 0 {
			reason := fmt.Sprintf("In use by %s: %s.", plural(len(users), "container", "containers"), describeContainers(users))
			for _, mb := range valid {
				skip(mb.Name, reason)
			}
			continue
		}
		for i, r := range refs {
			st := Step{Op: OpRemoveImage, Kind: "image", ID: r, Name: r, Detail: engine.ShortID(img.ID), Pin: img.ID}
			if i < len(refs)-1 {
				st.KeepsData = true
				st.Detail = detailUntag + st.Detail
			} else {
				st.Size = img.Size
				st.MustBeUnused = true
			}
			add(Plan{Steps: []Step{st}})
		}
	}
	return nil
}

const (
	detailUntagOnly = "untag only, "
	detailUntag     = "untag, "
)

func appendUnique(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}
